package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zcag/tela/backend/internal/atlas/core"
)

const (
	retrieveK     = 18
	contextBudget = 22000 // chars of source per page
	pageFanout    = 16    // pages drafted/refined in flight; the LLM gate is the real limiter

	// refListBudget caps the ITEM LIST in one reference-page prompt. The list used
	// to be unbounded — every item of the page's surface in a single call — so the
	// prompt grew with the repo. A 4,503-item surface rendered a ~456 KB list and
	// produced a 406 KB / ~101k-token prompt, which reliably killed the local model
	// with a Metal OOM (taking every in-flight request down with it) and failed the
	// chat over to the paid relief layer.
	//
	// The list is split into batches instead of truncated: refSystem tells the
	// model the list is COMPLETE and that every item must appear, so dropping items
	// would produce a page that lies about its own coverage — and the coverage
	// audit, which checks the same surface, would then flag them as gaps.
	//
	// 32000 chars of list + contextBudget of excerpts + scaffolding lands around
	// 13k tokens: near the measured p90 of ordinary traffic (21.4k) and far below
	// the crash region. Roughly 320 items per call at typical item length.
	refListBudget = 32000
)

// draftStage writes each page. Narrative pages hybrid-retrieve the most relevant
// source and generate under the strict grounded+cited prompt. Reference pages get
// the COMPLETE extracted item list injected (so they can't omit anything) plus
// retrieved context to describe each item.
type draftStage struct{}

func (draftStage) Name() core.StageName { return core.StageDraft }

func (draftStage) Run(ctx context.Context, rc *RunContext) error {
	// Per-page drafts are independent: each worker writes only its own
	// Pages[i].Body (a pre-sized, distinct slot) and persists its own row via
	// UpdatePageBody(p.ID). Progress goes through the atomic StepDone counter.
	rc.resetProgress()
	n := len(rc.Art.Pages)
	if err := parallelN(ctx, pageFanout, n, func(ctx context.Context, i int) error {
		p := &rc.Art.Pages[i]
		// Idempotent on resume: a page that already carries a body was drafted in a
		// prior (interrupted) run — skip it so a restart redoes only unfinished pages.
		if strings.TrimSpace(p.Body) != "" {
			rc.StepDone(n, "drafting: %s (already drafted)", p.Title)
			return nil
		}
		var body string
		var err error
		if p.Kind == core.PageReference {
			body, err = draftReference(ctx, rc, p)
		} else {
			body, err = draftNarrative(ctx, rc, p)
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err() // run canceled / shutting down — abort for real
			}
			// Tolerate a per-page failure (e.g. a transient endpoint 502): warn, leave
			// the body empty (the page is dropped below), and carry on — one bad page
			// must not throw away a whole multi-page run. Its surface resurfaces as a
			// coverage gap that repair can retry.
			rc.Warn("draft %q failed — skipping, repair will retry: %v", p.Title, err)
			rc.StepDone(n, "drafting: %s (failed)", p.Title)
			return nil
		}
		p.Body = body
		if err := rc.Store.UpdatePageBody(p.ID, body); err != nil {
			return err
		}
		rc.StepDone(n, "drafting: %s", p.Title)
		return nil
	}); err != nil {
		return err
	}
	// Drop pages that failed to draft (empty body). All failed ⇒ the endpoint is
	// down, so fail the run rather than publishing nothing.
	kept := rc.Art.Pages[:0]
	for i := range rc.Art.Pages {
		if strings.TrimSpace(rc.Art.Pages[i].Body) != "" {
			kept = append(kept, rc.Art.Pages[i])
		}
	}
	if len(kept) == 0 {
		return fmt.Errorf("draft: all %d pages failed (endpoint unavailable)", n)
	}
	if len(kept) < n {
		rc.Warn("draft: %d of %d pages failed and were dropped (surface becomes a coverage gap)", n-len(kept), n)
	}
	rc.Art.Pages = kept
	return nil
}

func draftNarrative(ctx context.Context, rc *RunContext, p *core.Page) (string, error) {
	chunks, err := narrativeChunks(ctx, rc, p)
	if err != nil {
		return "", err
	}
	ctxStr := assembleContext(chunks)
	user := draftUserCode(p.Title, p.Summary, ctxStr)
	if rc.Source != nil && rc.Source.Type == core.SourceJira {
		user = draftUserJira(p.Title, p.Summary, ctxStr)
	}
	body, err := chatBody(ctx, rc, draftSystem, user, 0.3)
	if err != nil {
		return "", err
	}
	clean, summary := extractSummary(sanitizePage(body))
	if summary != "" {
		p.Summary = summary // body-accurate standfirst supersedes the outline plan
	}
	return clean, nil
}

// narrativeChunks retrieves the source for a narrative page and, for a Jira
// source, prepends a synthetic project-state context chunk (built from status.md)
// so the prose can state "X in QA, Y blocked, the SNMP epic is N% complete" with a
// citation to status.md. The git path is unchanged (state chunk is nil/skipped).
func narrativeChunks(ctx context.Context, rc *RunContext, p *core.Page) ([]core.Chunk, error) {
	query := p.Title + " " + p.Summary + " " + strings.Join(p.Topics, " ")
	chunks, err := retrieve(ctx, rc, query, retrieveK)
	if err != nil {
		return nil, err
	}
	if rc.Source != nil && rc.Source.Type == core.SourceJira {
		if sc, ok := buildJiraStateContext(rc); ok {
			chunks = append([]core.Chunk{sc}, chunks...)
		}
	}
	return chunks, nil
}

// buildJiraStateContext synthesizes status.md into one citeable context chunk
// (Kind doc, cited to status.md) carrying the status counts, epic progress,
// blocked set and recency. Prepended to retrieved context for Jira narrative
// pages so the model grounds current-state claims with a real citation. Returns
// ok=false when status.md is absent (older snapshots).
func buildJiraStateContext(rc *RunContext) (core.Chunk, bool) {
	data, err := os.ReadFile(filepath.Join(rc.Art.RepoDir, "status.md"))
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return core.Chunk{}, false
	}
	text := core.SourceText(data)
	lines := strings.Count(text, "\n") + 1
	return core.Chunk{
		File: "status.md", StartLine: 1, EndLine: lines, Kind: core.ChunkDoc,
		Symbol: "project-state", Text: text,
	}, true
}

func draftReference(ctx context.Context, rc *RunContext, p *core.Page) (string, error) {
	batches := spineBatches(rc.Art.SpineByKind(p.SpineKinds...), refListBudget)
	var out strings.Builder
	var summary string
	for i, batch := range batches {
		list, query := renderSpineList(batch)
		// Retrieval is per batch too. The query used to be every item name in the
		// surface joined together, which the embedder then clamped — so the vector
		// described an arbitrary prefix of the list rather than this page's scope.
		chunks, err := retrieve(ctx, rc, query, retrieveK)
		if err != nil {
			return "", err
		}
		body, err := chatBody(ctx, rc, refSystem, refUser(p.Title, list, assembleContext(chunks), i, len(batches)), 0.2)
		if err != nil {
			return "", err
		}
		clean, s := extractSummary(sanitizePage(body))
		if summary == "" {
			summary = s // only part 1 is asked for one; later parts must not overwrite it
		}
		if i > 0 {
			out.WriteString("\n\n")
		}
		out.WriteString(strings.TrimSpace(clean))
	}
	// A reference page's outline summary is only a generic placeholder ("Complete
	// reference, anchored to the extracted surface"), so — unlike a narrative page,
	// whose outline summary is a real plan line worth keeping — overwrite it
	// unconditionally. If the agent emitted no marker, leaving it empty makes
	// publish skip the lock so the auto-summarizer writes a real one from the body,
	// rather than freezing the placeholder.
	p.Summary = summary
	return out.String(), nil
}

// spineBatches splits a page's surface into groups whose RENDERED list stays
// under budget, so one prompt can never grow with the size of the repo. Always
// returns at least one batch (possibly empty) so a page with no extracted
// surface still gets drafted exactly as before.
func spineBatches(items []core.SpineItem, budget int) [][]core.SpineItem {
	if len(items) == 0 {
		return [][]core.SpineItem{nil}
	}
	var out [][]core.SpineItem
	var cur []core.SpineItem
	size := 0
	for _, it := range items {
		n := len(renderSpineItem(it))
		// An item bigger than the whole budget still goes in a batch of its own
		// rather than being dropped — coverage beats prompt tidiness.
		if len(cur) > 0 && size+n > budget {
			out = append(out, cur)
			cur, size = nil, 0
		}
		cur = append(cur, it)
		size += n
	}
	return append(out, cur)
}

func renderSpineItem(it core.SpineItem) string {
	return fmt.Sprintf("- [%s] %s  (%s:%d)%s\n", it.Kind, it.Name, it.File, it.Line, detailSuffix(it.Detail))
}

// renderSpineList renders one batch as the prompt's item list plus the retrieval
// query for exactly those items.
func renderSpineList(items []core.SpineItem) (list, query string) {
	var b strings.Builder
	names := make([]string, 0, len(items))
	for _, it := range items {
		b.WriteString(renderSpineItem(it))
		names = append(names, it.Name)
	}
	return b.String(), strings.Join(names, " ")
}

func detailSuffix(d string) string {
	if d == "" {
		return ""
	}
	return " — " + d
}

// summaryMarkerRE matches the trailing standfirst the draft/reference model is
// asked to emit (summaryDirective): <!-- SUMMARY: one sentence -->. Case- and
// whitespace-tolerant; the last match wins if the model emitted more than one.
var summaryMarkerRE = regexp.MustCompile(`(?is)<!--\s*SUMMARY:\s*(.*?)\s*-->`)

// thisPageOpenerRE matches the low-value "This page … / This document …" opener
// the model falls back to despite the prompt forbidding it. Stripped
// deterministically so summaries state the substance directly (a prompt rule
// alone the 30B ignores ~40% of the time).
var thisPageOpenerRE = regexp.MustCompile(`(?i)^this (?:page|document|section)\s+`)

// extractSummary pulls the model's SUMMARY marker out of a drafted page,
// returning the body with every marker removed and the summary text (collapsed
// to one line, "This page…" opener stripped; empty if the model omitted the
// marker — the caller then keeps whatever summary the outline stage planned).
func extractSummary(body string) (string, string) {
	ms := summaryMarkerRE.FindAllStringSubmatch(body, -1)
	summary := ""
	if len(ms) > 0 {
		summary = strings.Join(strings.Fields(ms[len(ms)-1][1]), " ")
		if op := thisPageOpenerRE.FindString(summary); op != "" {
			summary = strings.TrimSpace(summary[len(op):])
			if summary != "" { // recapitalize the new leading word
				summary = strings.ToUpper(summary[:1]) + summary[1:]
			}
		}
	}
	body = strings.TrimSpace(summaryMarkerRE.ReplaceAllString(body, ""))
	return body, summary
}

// sanitizePage strips any prompt scaffolding the model echoed (leaked tags,
// "## Current draft" headers, whole-doc code fences) so the published page is
// just the page.
func sanitizePage(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```markdown")
	s = strings.TrimPrefix(s, "```md")
	for _, m := range []string{"<existing_page>", "</existing_page>", "<source_excerpts>", "</source_excerpts>"} {
		s = strings.ReplaceAll(s, m, "")
	}
	if i := strings.Index(s, "## Current draft"); i >= 0 {
		s = s[i+len("## Current draft"):]
	}
	return strings.TrimSpace(s)
}

// retrieve embeds the query and runs hybrid search.
func retrieve(ctx context.Context, rc *RunContext, query string, k int) ([]core.Chunk, error) {
	vecs, _, err := rc.LLM.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	return rc.Retriever.Search(vecs[0], query, k), nil
}

// assembleContext formats retrieved chunks with file:line headers and numbered
// lines, capped at the context budget, so the model can cite exact ranges.
func assembleContext(chunks []core.Chunk) string {
	var b strings.Builder
	for _, c := range chunks {
		block := fmt.Sprintf("### %s:%d-%d\n```\n%s\n```\n\n", c.File, c.StartLine, c.EndLine, c.Text)
		if b.Len()+len(block) > contextBudget {
			if b.Len() == 0 { // single oversized chunk — include a trimmed head
				b.WriteString(block[:contextBudget])
			}
			break
		}
		b.WriteString(block)
	}
	return b.String()
}
