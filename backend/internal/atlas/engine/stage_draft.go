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
	// This is the INPUT guard and it stays. It is no longer the only guard: see
	// refBatchPlan for the output-side budget, which is what actually decides how
	// many items a part carries. Raised from 32000 because each item now carries a
	// few lines of its own source inline (EvidenceIndex) while batches hold far
	// fewer items — the assembled prompt is the same order of size as before, and
	// the separate retrieved-excerpt block the reference prompt used to append is
	// gone.
	refListBudget = 48000

	// --- EXPERIMENT 2026-09-01 (atlas output-budget pack) ---------------------
	// docs/atlas-output-budget-experiment.md
	//
	// A reference part's OUTPUT is hard-capped by the provider's max_tokens, and
	// nothing used to bound the work assigned to one call against it. The prompt
	// handed the model up to refListBudget characters of items and told it to
	// document EVERY one — a list it could not even echo inside the cap, let alone
	// describe. The model wrote until it was cut off mid-word, and the missing
	// tail was then reported by the coverage audit as a documentation gap. On the
	// SerpApi run the flag list stopped at "--save-interval", losing 19 flags and
	// ALL 36 environment variables (the page never reached its Environment
	// section); per-page enumeration completeness decayed 100% → 53% → 31% purely
	// with item count.
	//
	// So batches are sized by what a call can ANSWER, not by what it can be told.

	// refCharsPerToken is the measured density of reference-page markdown:
	// 16,063 raw chars against a 4,096-token cap on run 133 ≈ 3.9.
	refCharsPerToken = 3.9
	// refOutputSafety reserves room for what a part spends outside its items —
	// the H1, intro and group headings — plus per-item variance.
	refOutputSafety = 0.80
	// Measured output cost per item on published pages: a "### heading + 3
	// bullets" item ran ~250 chars; a table row ~90.
	refRichItemChars    = 250
	refCompactItemChars = 90
	// refMaxRichParts is when a page stops being written as headed sections and
	// becomes a table. A 3,817-export page rendered richly would be ~75 calls;
	// as a table it is ~27, and a surface that large reads better as a table
	// anyway. This bounds cost — it does NOT drop items.
	refMaxRichParts = 6
	// refMinItemsPerBatch floors the split so truncation retries terminate.
	refMinItemsPerBatch = 8
	// refTruncRetries bounds the halve-and-retry when a part still comes back
	// cut off, so a pathological page costs at most a few extra calls.
	refTruncRetries = 2
	// Lines of source shown inline per item (see EvidenceIndex). A flag's
	// add_argument(...) call carries its own help text — that is the difference
	// between a real description and "Enables dev mode".
	refRichEvidenceLines    = 6
	refCompactEvidenceLines = 2
	// refAssumedMaxTokens is used when the provider's cap is unknown (MaxTokens
	// unset = the endpoint's own default). Guessing low is the safe direction:
	// it produces more, smaller parts rather than truncated ones.
	refAssumedMaxTokens = 4096
)

// refBatchPlan sizes one reference-page call from the model's OUTPUT capacity:
// how many items it can actually document, and whether the page should be
// written as headed sections or as a table.
//
// Note what this deliberately preserves: every calibrated baseline (compass 35
// must-cover items, udn 17, COM 15, nest 20) plans to a single rich part, which
// is byte-identical to the prompt the single-call path always sent. The change
// only engages in the regime that was never benchmarked.
func refBatchPlan(items, maxTokens int) (perBatch int, compact bool) {
	if maxTokens <= 0 {
		maxTokens = refAssumedMaxTokens
	}
	usable := float64(maxTokens) * refCharsPerToken * refOutputSafety
	rich := atLeast(int(usable/refRichItemChars), refMinItemsPerBatch)
	if items <= rich*refMaxRichParts {
		return rich, false
	}
	return atLeast(int(usable/refCompactItemChars), refMinItemsPerBatch), true
}

func atLeast(v, floor int) int {
	if v < floor {
		return floor
	}
	return v
}

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
	body, truncated, err := chatBody(ctx, rc, draftSystem, user, 0.3)
	if err != nil {
		return "", err
	}
	if truncated {
		// Prose can't be re-split the way a reference list can, so this is a
		// warning rather than a retry — but it must be VISIBLE, not silently
		// published as a page that stops mid-sentence.
		rc.Warn("narrative %q hit the model's output cap and may end mid-section", p.Title)
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
	body, summary, err := renderReferenceBody(ctx, rc, p, nil)
	if err != nil {
		return "", err
	}
	// A reference page's outline summary is only a generic placeholder ("Complete
	// reference, anchored to the extracted surface"), so — unlike a narrative page,
	// whose outline summary is a real plan line worth keeping — overwrite it
	// unconditionally. If the agent emitted no marker, leaving it empty makes
	// publish skip the lock so the auto-summarizer writes a real one from the body,
	// rather than freezing the placeholder.
	p.Summary = summary
	return body, nil
}

// renderReferenceBody writes a whole reference page, part by part, and is the
// SINGLE implementation shared by the draft stage and the repair loop.
//
// It was two copies: draftReference and repair's redraftReference each batched,
// retrieved and assembled independently. They had already drifted once (repair
// re-sent the whole surface in one prompt long after draft stopped doing so, and
// hit the OOM that made the page need repairing), and any difference between
// them shows up as a page that changes shape when it is repaired. emphasize is
// the only thing repair varies: the items a previous pass omitted, called out
// per part so the emphasis only ever names items the model can actually see.
func renderReferenceBody(ctx context.Context, rc *RunContext, p *core.Page, emphasize map[string]core.Gap) (body, summary string, err error) {
	items := rc.Art.SpineByKind(p.SpineKinds...)
	perBatch, compact := refBatchPlan(len(items), rc.LLM.MaxTokens())
	// Jira's spine items carry a SYNTHETIC file:line — Line is the item's ordinal
	// within schema.md/status.md, not where it appears — so the evidence join
	// would staple unrelated lines onto every item. The tracker path therefore
	// keeps the retrieved-excerpt grounding it has always used; only the code
	// path, where file:line is a real location, joins directly.
	evidence := rc.Source == nil || rc.Source.Type != core.SourceJira
	render := rc.spineRenderer(compact, evidence)
	planned := (len(items) + perBatch - 1) / perBatch
	if planned < 1 {
		planned = 1
	}

	var out strings.Builder
	i, partIdx := 0, 0
	// `partIdx == 0` keeps the loop honest for a page whose surface is empty: it
	// still gets exactly one (empty-list) call, as the single-batch path did.
	for i < len(items) || partIdx == 0 {
		maxItems := perBatch
		for attempt := 0; ; attempt++ {
			n := 0
			if i < len(items) {
				if n = nextBatch(items[i:], refListBudget, maxItems, render); n == 0 {
					n = 1 // an item bigger than the whole budget still goes, alone
				}
			}
			batch := items[i : i+n]
			total := planned
			if partIdx+1 > total {
				total = partIdx + 1 // retries split a part in two; keep the wording truthful
			}
			rp := refPrompt{title: p.Title, itemList: renderSpineList(batch, render),
				part: partIdx, total: total, compact: compact, evidence: evidence}
			if !evidence {
				chunks, rerr := retrieve(ctx, rc, spineQuery(batch), retrieveK)
				if rerr != nil {
					return "", "", rerr
				}
				rp.context = assembleContext(chunks)
			}
			prompt := refUser(rp)
			if emph := emphasisFor(batch, emphasize); emph != "" {
				prompt = emph + prompt
			}
			raw, truncated, cerr := chatBody(ctx, rc, refSystem, prompt, 0.2)
			if cerr != nil {
				return "", "", cerr
			}
			// A part cut off at max_tokens is an INCOMPLETE answer, not a page the
			// model chose to end — publishing it is what silently lost the surface.
			// Halve the batch and ask again; the budget above is an estimate, this
			// is what makes it self-correcting.
			if truncated && n > refMinItemsPerBatch && attempt < refTruncRetries {
				maxItems = atLeast(n/2, refMinItemsPerBatch)
				rc.Warn("reference %q part %d hit the model's output cap — retrying with %d items (was %d)", p.Title, partIdx+1, maxItems, n)
				continue
			}
			if truncated {
				rc.Warn("reference %q part %d still truncated at %d item(s) — some may be incomplete", p.Title, partIdx+1, n)
			}
			clean, sm := extractSummary(sanitizePage(raw))
			if summary == "" {
				summary = sm // only part 1 is asked for one; later parts must not overwrite it
			}
			if partIdx > 0 {
				out.WriteString("\n\n")
			}
			out.WriteString(strings.TrimSpace(clean))
			i += n
			partIdx++
			break
		}
	}
	return out.String(), summary, nil
}

// emphasisFor renders repair's "these were omitted" preamble for the items of
// one part, or "" when none of this part's items are in the gap set.
func emphasisFor(batch []core.SpineItem, gaps map[string]core.Gap) string {
	if len(gaps) == 0 {
		return ""
	}
	var b strings.Builder
	for _, it := range batch {
		if g, ok := gaps[it.Name]; ok {
			fmt.Fprintf(&b, "- %s (%s:%d)\n", g.Name, g.File, g.Line)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "CRITICAL: the previous draft OMITTED these items. They MUST each appear in your output:\n" + b.String() + "\n"
}

// spineRenderer returns the per-item renderer for a reference page: the item
// line plus a few lines of its own source, looked up by its file:line.
func (rc *RunContext) spineRenderer(compact, evidence bool) func(core.SpineItem) string {
	if !evidence {
		return func(it core.SpineItem) string { return renderSpineItem(it, "") }
	}
	lines := refRichEvidenceLines
	if compact {
		lines = refCompactEvidenceLines
	}
	return func(it core.SpineItem) string {
		return renderSpineItem(it, rc.Evidence.Snippet(it.File, it.Line, lines))
	}
}

// spineQuery is the retrieval query for a batch: every item name joined. It is a
// poor query — a bag of 100 names describes none of them — which is exactly why
// the code path stopped using it in favour of the file:line join. It survives
// only for the tracker path, whose items have no real source location, so that
// this change alters nothing there.
func spineQuery(items []core.SpineItem) string {
	names := make([]string, 0, len(items))
	for _, it := range items {
		names = append(names, it.Name)
	}
	return strings.Join(names, " ")
}

// nextBatch reports how many of items fit in ONE call, under both budgets: the
// output-side item cap (what the model can answer) and the input-side char
// budget (what the provider can be sent without an OOM). The first item always
// fits, so an oversized one is emitted alone rather than dropped.
func nextBatch(items []core.SpineItem, budget, maxItems int, render func(core.SpineItem) string) int {
	size, n := 0, 0
	for _, it := range items {
		if n >= maxItems {
			break
		}
		c := len(render(it))
		if n > 0 && size+c > budget {
			break
		}
		n++
		size += c
	}
	return n
}

func renderSpineItem(it core.SpineItem, evidence string) string {
	line := fmt.Sprintf("- [%s] %s  (%s:%d)%s\n", it.Kind, it.Name, it.File, it.Line, detailSuffix(it.Detail))
	return line + evidence
}

// renderSpineList renders one batch as the prompt's item list.
func renderSpineList(items []core.SpineItem, render func(core.SpineItem) string) string {
	var b strings.Builder
	for _, it := range items {
		b.WriteString(render(it))
	}
	return b.String()
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
