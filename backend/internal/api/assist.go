package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/zcag/tela/backend/internal/agreement"
	"github.com/zcag/tela/backend/internal/auth"
	"github.com/zcag/tela/backend/internal/rag"
)

// Ask-first ("talk to your docs") — generative features layered on retrieval.
// They all share ONE seam so this doesn't fan out into N copies of the same
// retrieve→prompt→complete dance:
//
//   askContext  — retrieve the top chunks + render the cited excerpt block
//   askComplete — apply the per-account compute caps and run the LLM
//
// RAGAsk is refactored onto these too (so the seam is exercised, not bypassed).

// Parent-document retrieval knobs for the ask path. Chunk retrieval finds the
// right neighbourhood, but an answer that spans a whole page — most painfully a
// "services using X" registry TABLE the chunker had to split — can't be
// reconstructed from one ~1400-char fragment. So the ask path pulls a deeper
// chunk pool, dedups to pages, and feeds the LLM the WHOLE body of the pages that
// matter, falling back to chunk text for the long tail.
const (
	// askRetrieveDepth: how many chunks to pull for the ask path (vs askMaxChunks
	// for plain search). Deeper so a topical hub page surfaces even when a
	// cross-encoder buried its terse table low.
	askRetrieveDepth = 24
	// askExpandTopRank: always expand the top-N pages by rank (precision — the
	// clearly-relevant pages get read in full).
	askExpandTopRank = 4
	// askDenseChunks: ...AND expand any page contributing at least this many
	// chunks to the pool. A page the corpus discusses densely for this query is
	// its hub (the Kafka registry page for "which projects use kafka"); read it
	// whole even if its best chunk ranked low. This is what rescues split tables.
	askDenseChunks = 3
	// askPageBodyCap / askExpandBudget bound the cost: per-page and cumulative
	// rune caps on expanded full bodies. Past the budget, pages degrade to their
	// chunk text. ~28k chars ≈ 7k tokens — full answers, bounded context.
	askPageBodyCap  = 12000
	askExpandBudget = 30000
	// askMaxPages caps how many distinct sources we render at all (expanded or
	// not). It is a DEFAULT, not a ceiling: a caller's `limit` raises it (bounded
	// by askMaxSourceCap) — before that was wired, `limit` only deepened chunk
	// retrieval and this cap silently clipped the result back to 12, which is why
	// 79% of logged asks came back at exactly 12 hits.
	askMaxPages = 12
	// askMaxSourceCap bounds what `limit` can buy. Every source past the expand
	// budget renders as chunk text (~1.4k chars), so the tail is cheap but not
	// free; this keeps a large limit from blowing the model's context.
	askMaxSourceCap = 40
	// askHubProbe: how many top-by-density pages the rerank-independent hub probe
	// returns (whole-page answers the reranker would bury).
	askHubProbe = 8
)

// askLowConfidenceScore: when reranking is on, a top hit scoring below this
// cross-encoder logit means retrieval found nothing strongly relevant. The answer
// is still produced (best effort) but flagged low-confidence so the reader knows
// to verify. Defined in rag (with its calibration) because the SAME line decides
// what counts as a knowledge gap — a question the answer told you not to trust is
// exactly a question worth writing a page for. One constant, one judgement.
const askLowConfidenceScore = rag.LowConfidenceTopScore

// lowConfidenceNote prefixes an answer the system isn't confident in — rendered
// as a tela CAUTION callout. Deterministic (not left to the model) so the
// declaration is reliable.
const lowConfidenceNote = "> [!CAUTION]\n" +
	"> Low confidence — I didn't find strongly relevant material in your docs for this. " +
	"The answer below is a best effort from loosely related excerpts; verify it before relying on it.\n\n"

// lowConfidence reports whether an answer should be flagged low-confidence: only
// meaningful when reranking is on (the score is then a cross-encoder logit
// comparable to askLowConfidenceScore); the RRF-only scale has no equivalent.
func lowConfidence(rerankOn bool, topScore float64) bool {
	return rerankOn && topScore < askLowConfidenceScore
}

// askResult is one retrieval pass's grounding. Considered vs len(Hits) is the
// honesty bit: retrieval routinely finds more distinct sources than the render
// cap shows, and until this was reported a caller could not tell whether the
// answer stopped because the corpus ran out or because we did.
type askResult struct {
	Context    string    // numbered [n] excerpt block
	Hits       []rag.Hit // sources aligned to the [n] numbering
	Top        float64   // best retrieval score (0 when nothing matched)
	Considered int       // distinct sources retrieval found, before the render cap
}

// Truncated reports that retrieval found more sources than were rendered — the
// caller can re-ask with a bigger `limit` to see them.
func (r askResult) Truncated() bool { return r.Considered > len(r.Hits) }

// askContext retrieves grounding for query and renders the numbered, cited
// excerpt block that feeds every generative feature, plus the per-source hits
// (for citation, aligned to the [n] numbering), the top fused score, and how many
// distinct sources were considered. It dedups chunks to sources and expands
// topically-central pages to their full body (see the knobs above). An empty Hits
// means "nothing retrieved".
func (s *Server) askContext(ctx context.Context, userID int64, query string, spaceID *int64, limit int) (askResult, error) {
	depth, maxSources := askRetrieveDepth, askMaxPages
	if limit > 0 {
		if limit > askMaxSourceCap {
			limit = askMaxSourceCap
		}
		if limit > depth {
			depth = limit
		}
		maxSources = limit
	}
	hits, err := s.rag.Search(ctx, userID, query, spaceID, depth, "hybrid")
	if err != nil {
		return askResult{}, err
	}
	if len(hits) == 0 {
		return askResult{}, nil
	}
	// Dedup to SOURCES, in rank order of first appearance. A source is a page or a
	// file (a file hit's key is "f<id>", a page's "p<id>") so multiple root files
	// — which share parent page 0 — never collapse into one entry, and a file never
	// merges with the page it's attached to. Only page sources expand to a full
	// body (HubPages/PageBodies below); file sources render their chunk text.
	order := make([]string, 0, len(hits))
	count := map[string]int{}
	best := map[string]rag.Hit{}
	chunkIDs := make([]int64, 0, len(hits))
	pageIDs := make([]int64, 0, len(hits)) // page sources only, for PageBodies
	for _, h := range hits {
		k := hitKey(h)
		if _, seen := best[k]; !seen {
			best[k] = h
			order = append(order, k)
			chunkIDs = append(chunkIDs, h.ChunkID)
			if h.SourceKind != "file" {
				pageIDs = append(pageIDs, h.PageID)
			}
		}
		count[k]++
	}
	// Topical-hub signal (rerank-INDEPENDENT). A precision reranker demotes terse
	// tables, so a page whose WHOLE body answers an aggregate question (a "services
	// using X" registry) can have every chunk ranked low — present but never
	// expanded. Pull topical HUBS to the FRONT so they expand before the per-page
	// budget is spent on lower-value pages (the registry page ranking ~8th was
	// getting the budget-exhausted fallback — the bug that kept the table out).
	//
	// A hub is detected two ways, unioned: (1) TITLE match (HubPages — the page
	// named for the topic), and (2) CONTENT density — a page the query retrieved
	// many chunks from (count >= askDenseChunks), even if its title doesn't name the
	// topic. (2) is load-bearing: an "Architecture Overview" page enumerates the
	// topics without naming them in its title, so title-match alone left it as a
	// budget-starved snippet and its enumerated items vanished from the grounding.
	titleHubs := map[string]bool{}
	if hubs, herr := s.rag.HubPages(ctx, userID, query, spaceID, askHubProbe); herr == nil {
		for _, hp := range hubs {
			if hp.Count < askDenseChunks {
				continue
			}
			k := "p" + strconv.FormatInt(hp.PageID, 10)
			titleHubs[k] = true
			count[k] += hp.Count
			if _, seen := best[k]; !seen {
				best[k] = rag.Hit{SourceKind: "page", PageID: hp.PageID, SpaceID: hp.SpaceID, Title: hp.Title, ChunkID: hp.ChunkID}
				chunkIDs = append(chunkIDs, hp.ChunkID)
				pageIDs = append(pageIDs, hp.PageID)
				order = append(order, k)
			}
		}
	}
	order = frontHubs(order, count, titleHubs, askDenseChunks)
	bodies, err := s.rag.PageBodies(ctx, userID, pageIDs, spaceID)
	if err != nil {
		return askResult{}, err
	}
	contents, err := s.rag.ChunkContents(ctx, userID, chunkIDs, spaceID)
	if err != nil {
		return askResult{}, err
	}
	locations := s.sourceLocations(ctx, order, best)
	block, pageHits := buildAskContext(order, best, count, bodies, contents, locations, maxSources)
	return askResult{Context: block, Hits: pageHits, Top: hits[0].Score, Considered: len(order)}, nil
}

// askPathMaxSegments bounds a source's location label (Space › a › b › …) so a
// deep tree doesn't spend grounding tokens on the middle of the path: the space
// and the first/last dirs carry the disambiguating signal, the middle is elided.
const askPathMaxSegments = 4

// sourceLocations builds the "Space › path" provenance prefix for each retrieved
// source, keyed by the same source key used in `order`/`best`. This is what lets
// the model keep one project's facts out of another's answer (the cross-space
// "soft leak" an all-spaces ask is prone to): the space name is the load-bearing
// signal; the breadcrumb dirs additionally separate sub-areas within a big space.
// The source's own title is NOT included — buildAskContext appends it after the
// prefix. Reuses pageBreadcrumb (the same ancestor walk the search UI shows).
// Best-effort per source: a breadcrumb error degrades that label to the space name
// alone, and a missing space name to the breadcrumb alone. For a file source
// PageID is its parent page, so the path stops at that parent (the file's own name
// is the title).
func (s *Server) sourceLocations(ctx context.Context, order []string, best map[string]rag.Hit) map[string]string {
	names := map[int64]string{} // space id → name, cached across sources
	out := make(map[string]string, len(order))
	for _, k := range order {
		h := best[k]
		name, ok := names[h.SpaceID]
		if !ok {
			name = s.spaceName(ctx, &h.SpaceID)
			names[h.SpaceID] = name
		}
		segs := make([]string, 0, askPathMaxSegments)
		if name != "" {
			segs = append(segs, name)
		}
		if h.PageID > 0 {
			if crumbs, err := pageBreadcrumb(ctx, s.DB, h.PageID); err == nil {
				segs = append(segs, crumbs...)
			}
		}
		out[k] = boundSegments(segs, askPathMaxSegments)
	}
	return out
}

// boundSegments joins location segments with " › ", eliding the middle when there
// are more than max so a deep path stays short: the first segment (the space) and
// the last (the deepest dir) are the disambiguating ones. Pure — unit-testable.
func boundSegments(segs []string, max int) string {
	if len(segs) <= max {
		return strings.Join(segs, " › ")
	}
	kept := append(append([]string{}, segs[:max-2]...), "…", segs[len(segs)-1])
	return strings.Join(kept, " › ")
}

// frontHubs moves topical-hub sources to the front of the order so they expand to
// full body before the per-page budget is spent on lower-value pages. A source is
// a hub if its title matched (titleHubs) OR the query retrieved at least `dense`
// of its chunks (count) — the density signal catches enumerator pages whose title
// doesn't name the topic. Stable: relative order within hubs and within non-hubs
// is preserved (so rank still breaks ties). Pure, so it's unit-testable.
func frontHubs(order []string, count map[string]int, titleHubs map[string]bool, dense int) []string {
	front := make([]string, 0, len(order))
	rest := make([]string, 0, len(order))
	for _, k := range order {
		if titleHubs[k] || count[k] >= dense {
			front = append(front, k)
		} else {
			rest = append(rest, k)
		}
	}
	return append(front, rest...)
}

// hitKey is the dedup key for a retrieved source: page hits collapse by page id,
// file hits by file id (so multiple root files, all with parent page 0, stay
// distinct). Page is the default for a zero-value SourceKind (HubPages-built hits).
func hitKey(h rag.Hit) string {
	if h.SourceKind == "file" {
		return "f" + strconv.FormatInt(h.FileID, 10)
	}
	return "p" + strconv.FormatInt(h.PageID, 10)
}

// buildAskContext renders the numbered excerpt block from ranked sources (pages
// and files, keyed by `order`), expanding topically-central PAGES (top-by-rank or
// dense hubs) to their full body and falling back to chunk text otherwise. File
// sources have no body — they always render their chunk text. Pure (no I/O) so
// it's unit-testable. `bodies` is keyed by PAGE id; `contents` by chunk id;
// `locations` (keyed by source key) prefixes each label with its "Space › path"
// provenance so the model can tell projects apart — nil/missing → title only.
// maxSources caps how many sources are rendered at all; the caller compares it
// against len(order) to know whether the grounding was truncated.
// Returns the block and the per-source hits aligned to the [n] numbering.
func buildAskContext(order []string, best map[string]rag.Hit, count map[string]int, bodies, contents map[int64]string, locations map[string]string, maxSources int) (string, []rag.Hit) {
	var b strings.Builder
	pageHits := make([]rag.Hit, 0, len(order))
	spent, n := 0, 0
	for rank, key := range order {
		if n >= maxSources {
			break
		}
		h := best[key]
		n++
		full, hasBody := "", false
		if h.SourceKind != "file" {
			full, hasBody = bodies[h.PageID]
		}
		expand := hasBody && full != "" && spent < askExpandBudget &&
			(rank < askExpandTopRank || count[key] >= askDenseChunks)

		label := h.Title
		if loc := locations[key]; loc != "" {
			label = loc + " › " + h.Title // "Space › path › Title" provenance keeps projects distinct
		}
		if h.SourceKind == "file" {
			label += " (file)" // mark an attachment source so the model cites it as a file
		}
		fmt.Fprintf(&b, "[%d] %s", n, label)
		if !expand && h.HeadingPath != "" {
			fmt.Fprintf(&b, " — %s", h.HeadingPath) // heading path only adds context to a fragment
		}
		b.WriteString("\n")

		body := h.Snippet
		if expand {
			body = clampRunes(full, askPageBodyCap)
			spent += len(body)
		} else if c, ok := contents[h.ChunkID]; ok && c != "" {
			body = c
		}
		b.WriteString(body)
		b.WriteString("\n\n")
		pageHits = append(pageHits, h)
	}
	return b.String(), pageHits
}

// askMaxConflicts caps how many known-disagreement lines we hand the model — a
// noise/cost bound; the LLM filters these to the question-relevant ones anyway.
const askMaxConflicts = 6

// askConflictNote builds the "known disagreements" block for the cited sources:
// precomputed contradictions (the agreement worker) keyed to the [n] excerpt
// numbers, INCLUDING ones whose other side wasn't retrieved. Returns "" when none.
// The system prompt tells the model to raise only the question-relevant ones. The
// caller must pass the access-scoped cited hits (same order as the [n] numbering).
func (s *Server) askConflictNote(ctx context.Context, pageHits []rag.Hit) string {
	if s.agreement == nil || len(pageHits) == 0 {
		return ""
	}
	// Disagreements are tracked between PAGES; a file source has no page identity
	// of its own (its PageID is the parent page), so skip file hits here.
	ids := make([]int64, 0, len(pageHits))
	for _, h := range pageHits {
		if h.SourceKind != "file" {
			ids = append(ids, h.PageID)
		}
	}
	if len(ids) == 0 {
		return ""
	}
	byPage, err := s.agreement.DisputesFor(ctx, ids)
	if err != nil || len(byPage) == 0 {
		return ""
	}
	return formatConflicts(pageHits, byPage)
}

// formatConflicts renders the disagreement block (pure, so it's unit-testable).
// It walks pageHits in [n] order, dedups symmetric pairs (a conflict recorded on
// both pages' rows), skips reasonless entries, and caps the list.
func formatConflicts(pageHits []rag.Hit, byPage map[int64][]agreement.Dispute) string {
	numOf := make(map[int64]int, len(pageHits))
	for i, h := range pageHits {
		if h.SourceKind != "file" {
			numOf[h.PageID] = i + 1
		}
	}
	seen := map[[2]int64]bool{}
	var b strings.Builder
	lines := 0
	for _, h := range pageHits {
		if h.SourceKind == "file" {
			continue
		}
		for _, d := range byPage[h.PageID] {
			if lines >= askMaxConflicts {
				break
			}
			reason := strings.TrimSpace(d.Reason)
			if reason == "" {
				continue
			}
			key := [2]int64{h.PageID, d.PageID}
			if key[0] > key[1] {
				key[0], key[1] = key[1], key[0]
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			fmt.Fprintf(&b, "- [%d] %q may conflict with %q — %s\n", numOf[h.PageID], h.Title, d.Title, reason)
			lines++
		}
	}
	if lines == 0 {
		return ""
	}
	return "\nKnown disagreements among these sources (raise only if relevant to the question, and give both values):\n" + b.String() + "\n"
}

// clampRunes truncates s to at most n runes (never splitting a rune).
func clampRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// askComplete applies the compute caps (rate + monthly) and runs the LLM,
// writing the HTTP error itself on any failure. Returns (answer, true) only on
// success. label categorises the rate bucket (e.g. "ask", "draft").
func (s *Server) askComplete(w http.ResponseWriter, r *http.Request, u *auth.User, label, system, user string) (string, bool) {
	if !s.askComputeOK(w, r, u, label) {
		return "", false
	}
	answer, err := s.llm.Complete(r.Context(), system, user)
	if err != nil {
		writeError(w, http.StatusBadGateway, "completion_failed", "generation failed")
		return "", false
	}
	return answer, true
}

// askComputeOK runs the compute caps (per-request rate + monthly cap), writing
// the HTTP error itself on failure. Shared by askComplete (JSON) and the
// streaming ask path, which must clear the guards BEFORE it starts the SSE body
// so a 429/cap stays a clean HTTP status rather than a mid-stream surprise.
func (s *Server) askComputeOK(w http.ResponseWriter, r *http.Request, u *auth.User, label string) bool {
	acct := account{Kind: accountUser, ID: u.ID}
	if !s.cloudRateOK(w, label, acct) {
		return false
	}
	if ae := s.checkAndRecordLLMCall(r.Context(), acct); ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return false
	}
	return true
}

// askGuards is the shared precondition for every generative endpoint: embedder +
// LLM configured. Returns false (and writes the 503) when unavailable.
func (s *Server) askGuards(w http.ResponseWriter) bool {
	if !s.aiEnabled() {
		writeError(w, http.StatusServiceUnavailable, "rag_disabled", "semantic search is not configured")
		return false
	}
	if !s.llm.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "llm_disabled", "managed AI is not configured")
		return false
	}
	return true
}

// genFollowups returns up to 3 suggested follow-up questions for an answer.
// Best-effort: a cap hit or LLM error yields no follow-ups (never fails the ask).
func (s *Server) genFollowups(ctx context.Context, u *auth.User, question, answer string) []string {
	if ae := s.checkAndRecordLLMCall(ctx, account{Kind: accountUser, ID: u.ID}); ae != nil {
		return nil
	}
	const sys = "Suggest up to 3 concise follow-up questions a reader might ask next, " +
		"each answerable from a team wiki. Output one question per line, no numbering, no preamble."
	out, err := s.llm.Complete(ctx, sys, "Question: "+question+"\n\nAnswer: "+answer)
	if err != nil {
		return nil
	}
	return parseLines(out, 3)
}

// parseLines splits a model's line-per-item output into a clean, capped slice:
// strips bullets/numbering, drops blanks, dedupes, caps at max.
func parseLines(s string, max int) []string {
	var out []string
	seen := map[string]bool{}
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		ln = strings.TrimLeft(ln, "-*•0123456789.) \t")
		ln = strings.TrimSpace(ln)
		if ln == "" || seen[strings.ToLower(ln)] {
			continue
		}
		seen[strings.ToLower(ln)] = true
		out = append(out, ln)
		if len(out) >= max {
			break
		}
	}
	return out
}

// enrichFileCitations fills download_url + share_url on file-source hits — the
// rag layer carries the file's space + name + hash but can't build a tela URL
// without importing api, so the citation URLs are composed here (same shapes as
// list_attachments). download_url fetches the bytes; share_url is the file page,
// the one to put in an answer a person reads. Page hits are untouched.
func enrichFileCitations(hits []rag.Hit) {
	base := canonicalBaseURL()
	for i := range hits {
		h := &hits[i]
		if h.SourceKind == "file" && h.FileName != "" && h.Hash != "" {
			h.DownloadURL = base + spaceFileServeURL(h.SpaceID, h.FileName, h.Hash)
			h.ShareURL = base + fileSharePath(h.Hash, h.FileName)
		}
	}
}

// enrichFileChunk is enrichFileCitations for a single read_chunk result.
func enrichFileChunk(c *rag.ChunkRead) {
	if c != nil && c.SourceKind == "file" && c.FileName != "" && c.Hash != "" {
		c.DownloadURL = canonicalBaseURL() + spaceFileServeURL(c.SpaceID, c.FileName, c.Hash)
		c.ShareURL = canonicalBaseURL() + fileSharePath(c.Hash, c.FileName)
	}
}

// sourcesBlock renders retrieved hits as a markdown "Sources" section with
// tela://page links (which the indexer records as backlinks — so a generated
// page wires itself back to its sources).
func sourcesBlock(hits []rag.Hit) string {
	if len(hits) == 0 {
		return ""
	}
	seen := map[string]bool{}
	var b strings.Builder
	b.WriteString("\n\n---\n\n## Sources\n\n")
	for _, h := range hits {
		k := hitKey(h)
		if seen[k] {
			continue
		}
		seen[k] = true
		switch {
		case h.SourceKind == "file" && h.PageID > 0:
			// File source: link to the page it's attached to (the citable location).
			fmt.Fprintf(&b, "- [%s](tela://page/%d) (file)\n", h.Title, h.PageID)
		case h.SourceKind == "file":
			fmt.Fprintf(&b, "- %s (file)\n", h.Title) // space-root file, no parent page
		default:
			fmt.Fprintf(&b, "- [%s](tela://page/%d)\n", h.Title, h.PageID)
		}
	}
	return b.String()
}

// ── draft: ask-first authoring ──────────────────────────────────────────────

type draftRequest struct {
	Topic   string `json:"topic"`
	SpaceID *int64 `json:"space_id"`
	Limit   int    `json:"limit"`
}

// RAGDraft handles POST /api/rag/draft {topic, space_id?, limit?}
// Returns a grounded markdown DRAFT for a new page about `topic`, built from the
// most relevant existing pages (cited). Not saved — the caller edits then saves.
func (s *Server) RAGDraft(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	if !s.askGuards(w) {
		return
	}
	if !s.embedRateOK(w, account{Kind: accountUser, ID: u.ID}) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, cloudMaxRequestBytes)
	var req draftRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Topic) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "topic is required")
		return
	}
	spaceID := req.SpaceID
	if b := bearerSpace(r); b != nil {
		spaceID = b
	}
	res, err := s.askContext(r.Context(), u.ID, req.Topic, spaceID, req.Limit)
	if err != nil {
		if clientCanceled(w, r, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "retrieval failed")
		return
	}
	excerpts, hits := res.Context, res.Hits
	const sys = "You draft documentation pages for a team wiki in clean markdown (headings, lists, short paragraphs). " +
		"Ground the draft in the provided excerpts and cite them inline as [n] where used. " +
		"If the excerpts are thin, produce a sensible starting outline instead of inventing facts."
	user := "Excerpts:\n\n" + excerpts + "\nDraft a wiki page about: " + req.Topic
	draft, okc := s.askComplete(w, r, u, "draft", sys, user)
	if !okc {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"draft": draft, "sources": hits})
}

// ── answer-to-page: the loop-closer ─────────────────────────────────────────

type answerToPageRequest struct {
	Question string `json:"question"`
	SpaceID  int64  `json:"space_id"`
	ParentID *int64 `json:"parent_id"`
	Title    string `json:"title"`
}

// RAGAnswerToPage handles POST /api/rag/answer-to-page {question, space_id, parent_id?, title?}
// Answers the question from the docs AND saves the answer as a new page (cited),
// turning ephemeral Q&A into durable knowledge — closing the ask → gap → write
// loop. Requires editor access to the target space (createPageCore enforces it).
func (s *Server) RAGAnswerToPage(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	if !s.askGuards(w) {
		return
	}
	if !s.embedRateOK(w, account{Kind: accountUser, ID: u.ID}) {
		return
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, cloudMaxRequestBytes)
	var req answerToPageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Question) == "" || req.SpaceID <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "question and space_id are required")
		return
	}
	res, err := s.askContext(r.Context(), u.ID, req.Question, &req.SpaceID, 0)
	if err != nil {
		if clientCanceled(w, r, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "retrieval failed")
		return
	}
	excerpts, hits, top := res.Context, res.Hits, res.Top
	_ = s.rag.LogAsk(r.Context(), u.ID, &req.SpaceID, req.Question, len(hits), top)
	if len(hits) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "no_answer", "couldn't find anything in the docs to answer that — nothing to save")
		return
	}
	const sys = "Answer the question strictly from the provided excerpts, in clean markdown suitable for a wiki page. " +
		"Cite sources inline as [n]. If the excerpts don't fully answer it, say what's known and what's missing."
	answer, okc := s.askComplete(w, r, u, "answer_to_page", sys, "Excerpts:\n\n"+excerpts+"\nQuestion: "+req.Question)
	if !okc {
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = strings.TrimSpace(req.Question)
	}
	body := answer + sourcesBlock(hits)
	page, ae := s.createPageCore(r.Context(), u, k, pageCreateRequest{
		SpaceID: req.SpaceID, ParentID: req.ParentID, Title: title, Body: body,
	}, true)
	if ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"page": page, "answer": answer, "sources": hits})
}

// ── page questions: "what does this page answer?" ───────────────────────────

// RAGPageQuestions handles GET /api/pages/{id}/questions
// Suggested questions the given page answers — for a "people often ask…" affordance
// and as seeds for ask-first navigation. Read access to the page required.
func (s *Server) RAGPageQuestions(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	if !s.llm.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "llm_disabled", "managed AI is not configured")
		return
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	pid, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || pid <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "page id must be a positive integer")
		return
	}
	p, ae := s.getPageCore(r.Context(), u, k, pid)
	if ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	const sys = "List up to 5 distinct questions that THIS page directly answers. " +
		"One question per line, no numbering, no preamble."
	body, _ := mcpCapBody(p.Body)
	out, okc := s.askComplete(w, r, u, "page_questions", sys, "Title: "+p.Title+"\n\n"+body)
	if !okc {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"questions": parseLines(out, 5)})
}
