package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/zcag/tela/backend/internal/auth"
	"github.com/zcag/tela/backend/internal/rag"
)

// RAG (semantic retrieval) handlers. Thin wrappers over the internal/rag
// Service: this file owns auth + HTTP shape, the rag package owns the logic.
// Both endpoints 503 when the feature is unconfigured (TELA_RAG_EMBED_URL
// unset), so the routes can be registered unconditionally.

// RAGSearch handles GET /api/rag/search?q=&space_id=&limit=&mode=
// Hybrid chunk search scoped to the caller's space_access. Returns ranked
// chunks with page id + heading path for citation.
// embedRateOK enforces the per-account fair-use limit on the shared embedder,
// the scarcest resource on a single-box instance. REST variant: on exhaustion it
// writes 429 + Retry-After and returns false. Keyed per account so one PAT or
// script can't saturate the embedder for everyone; the ask paths' generation is
// separately bounded by cloudLimiter. See embedRateErr for the MCP variant.
func (s *Server) embedRateOK(w http.ResponseWriter, acct account) bool {
	ok, retry := s.embedLimiter.allow("embed", fmt.Sprintf("%s:%d", acct.Kind, acct.ID))
	if !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "AI search rate limit reached; slow down and retry shortly")
		return false
	}
	return true
}

// embedRateErr is the MCP variant of embedRateOK: a 429 *apiErr (surfaced as a
// tool error) when the per-account embed budget is spent, nil otherwise.
func (s *Server) embedRateErr(acct account) *apiErr {
	if ok, _ := s.embedLimiter.allow("embed", fmt.Sprintf("%s:%d", acct.Kind, acct.ID)); !ok {
		return &apiErr{http.StatusTooManyRequests, "rate_limited", "AI search rate limit reached; slow down and retry shortly"}
	}
	return nil
}

func (s *Server) RAGSearch(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	if !s.aiEnabled() {
		writeError(w, http.StatusServiceUnavailable, "rag_disabled", "semantic search is not configured")
		return
	}
	if !s.embedRateOK(w, account{Kind: accountUser, ID: u.ID}) {
		return
	}

	q := r.URL.Query()
	var spaceID *int64
	if v := q.Get("space_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "space_id must be a positive integer")
			return
		}
		spaceID = &id
	}
	// A space-scoped bearer key may only ever see its one space — force the
	// narrow even if the caller passed a different (or no) space_id. Mirrors the
	// Search handler's bearer branch.
	if k, isBearer := auth.APIKeyFromContext(r.Context()); isBearer && k.SpaceID != nil {
		spaceID = k.SpaceID
	}

	limit, _ := strconv.Atoi(q.Get("limit"))
	hits, err := s.rag.Search(r.Context(), u.ID, q.Get("q"), spaceID, limit, q.Get("mode"))
	if err != nil {
		if clientCanceled(w, r, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "semantic search failed")
		return
	}
	enrichFileCitations(hits)
	writeJSON(w, http.StatusOK, map[string]any{"results": hits})
}

// RAGReadChunk handles GET /api/rag/chunk?chunk_id=
// Returns one chunk's full section text (the chunk-granularity read between a
// search snippet and the whole-page get_page). Scoped to the caller's
// space_access; 404 when the chunk doesn't exist or is out of scope.
func (s *Server) RAGReadChunk(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	if !s.aiEnabled() {
		writeError(w, http.StatusServiceUnavailable, "rag_disabled", "semantic search is not configured")
		return
	}

	chunkID, err := strconv.ParseInt(r.URL.Query().Get("chunk_id"), 10, 64)
	if err != nil || chunkID <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "chunk_id must be a positive integer")
		return
	}
	var spaceID *int64
	if k, isBearer := auth.APIKeyFromContext(r.Context()); isBearer && k.SpaceID != nil {
		spaceID = k.SpaceID
	}

	chunk, err := s.rag.ReadChunk(r.Context(), u.ID, chunkID, spaceID)
	if errors.Is(err, rag.ErrChunkNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "chunk not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "read chunk failed")
		return
	}
	enrichFileChunk(chunk)
	writeJSON(w, http.StatusOK, map[string]any{"chunk": chunk})
}

// RAGFreshness handles GET /api/rag/freshness[?space_id=]
// Without space_id: per-space index-health summary across every space the caller
// can access. With space_id: per-page status within that space. Always 200 with
// an `enabled` flag (the counts are real even when the embedder is off, so the
// admin view can show what's indexed vs what would need an embedder).
func (s *Server) RAGFreshness(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	if v := r.URL.Query().Get("space_id"); v != "" {
		spaceID, err := strconv.ParseInt(v, 10, 64)
		if err != nil || spaceID <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "space_id must be a positive integer")
			return
		}
		pages, err := s.rag.SpacePageFreshness(r.Context(), u.ID, spaceID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "freshness query failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"enabled": s.aiEnabled(), "space_id": spaceID, "pages": pages})
		return
	}

	spaces, err := s.rag.Freshness(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "freshness query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": s.aiEnabled(), "spaces": spaces})
}

// RAGReindex handles POST /api/rag/reindex?space_id=
// Chunks + embeds every page in the space. Requires membership (the same gate
// as reading the space); synchronous — fine for a wiki-scale corpus.
func (s *Server) RAGReindex(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUser(w, r); !ok {
		return
	}
	if !s.aiEnabled() {
		writeError(w, http.StatusServiceUnavailable, "rag_disabled", "semantic search is not configured")
		return
	}

	v := r.URL.Query().Get("space_id")
	spaceID, err := strconv.ParseInt(v, 10, 64)
	if err != nil || spaceID <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "space_id is required")
		return
	}
	if _, ok := s.requireMembership(w, r, spaceID); !ok {
		return
	}

	pages, chunks, err := s.rag.ReindexSpace(r.Context(), spaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "reindex failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"indexed_pages":  pages,
		"indexed_chunks": chunks,
	})
}

// askRequest is the POST /api/rag/ask body.
type askRequest struct {
	Question string `json:"question"`
	SpaceID  *int64 `json:"space_id"`
	Limit    int    `json:"limit"`
}

// RAGAsk handles POST /api/rag/ask {question, space_id?, limit?}
// "Ask your docs": retrieve the top chunks via the EXISTING hybrid search
// (scoped to the caller's space_access — same anti-leak path as RAGSearch),
// build a grounded prompt from the chunk texts + their page references, call the
// in-process LLM (s.llm.Complete — the SAME client a self-hoster reaches over
// the managed cloud proxy), and return the answer plus the cited source pages.
// 503 when the LLM is unconfigured, mirroring the rag handlers.
func (s *Server) RAGAsk(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	if !s.aiEnabled() {
		writeError(w, http.StatusServiceUnavailable, "rag_disabled", "semantic search is not configured")
		return
	}
	if !s.llm.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "llm_disabled", "managed AI is not configured")
		return
	}
	if !s.embedRateOK(w, account{Kind: accountUser, ID: u.ID}) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, cloudMaxRequestBytes)
	var req askRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}
	if strings.TrimSpace(req.Question) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "question is required")
		return
	}

	spaceID := req.SpaceID
	// A space-scoped bearer key may only ever see its one space — force the
	// narrow. Mirrors RAGSearch.
	if b := bearerSpace(r); b != nil {
		spaceID = b
	}

	// Retrieve grounding via the shared seam (also used by draft/answer-to-page).
	excerpts, hits, top, err := s.askContext(r.Context(), u.ID, req.Question, spaceID, req.Limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "retrieval failed")
		return
	}
	// Log every ask with its retrieval confidence (best-effort) — feeds the
	// knowledge-gaps view, including the zero-hit case (a clear gap).
	_ = s.rag.LogAsk(r.Context(), u.ID, spaceID, req.Question, len(hits), top)
	s.recordRequestEvent(r, eventInput{
		Type: evtAsk, ActorUserID: &u.ID, ActorLabel: u.Username,
		Detail: fmt.Sprintf("%q (%d hits)", req.Question, len(hits)),
	})
	if len(hits) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"answer":  "I couldn't find anything in your documents to answer that.",
			"sources": []rag.Hit{},
		})
		return
	}

	conflicts := s.askConflictNote(r.Context(), hits)
	answer, ok := s.askComplete(w, r, u, "ask", askSystemPrompt,
		askUserPrompt(excerpts, conflicts, req.Question))
	if !ok {
		return
	}

	// Weak retrieval → still answer, but declare it. The caller (UI) gets a flag;
	// the answer text carries a deterministic callout so the warning survives even
	// when only the prose is shown.
	low := lowConfidence(s.rag.RerankEnabled(), top)
	if low {
		answer = lowConfidenceNote + answer
	}
	resp := map[string]any{"answer": answer, "sources": hits, "low_confidence": low}
	// Suggest follow-up questions so an answer becomes a thread to pull on
	// (ask-first navigation). Best-effort.
	if f := s.genFollowups(r.Context(), u, req.Question, answer); len(f) > 0 {
		resp["followups"] = f
	}
	writeJSON(w, http.StatusOK, resp)
}

// askSystemPrompt is the grounding instruction shared by the JSON and streaming
// ask handlers (single source so the two never drift).
const askSystemPrompt = "You are a helpful assistant answering questions strictly from the provided document excerpts. " +
	"Cite the relevant sources by their [n] number. If the excerpts don't contain the answer, say so — do not invent facts. " +
	"Each excerpt is labeled with its source location as 'Space › path › Title'. When the question is about one project, space, or area, answer only from the excerpts whose location matches it and ignore the rest; never merge facts across different spaces/projects, and when excerpts differ because they describe different projects, keep them distinct (name the location) instead of blending them into one answer. " +
	"Sources may also be flagged in a 'Known disagreements' note, and excerpts can themselves conflict on a value, status, or claim. " +
	"When a conflict bears on the question, surface it explicitly and give both sides — never present a contested value as settled or silently pick one. " +
	"In particular, if a flagged disagreement is about the very thing the question asks for, you MUST report both values and name the conflicting sources, even when one excerpt states a single value confidently. " +
	"Ignore a flagged disagreement only when it is unrelated to the question, and never invent a conflict that isn't stated."

// askUserPrompt assembles the grounded user turn shared by both ask handlers: the
// cited excerpts, the (optional) known-disagreements block, the question, and the
// always-on, self-scoping completeness directive (askEnumerationDirective).
func askUserPrompt(excerpts, conflicts, question string) string {
	return "Answer the question using only these document excerpts.\n\n" + excerpts + conflicts +
		"Question: " + question + askEnumerationDirective
}

// askEnumerationDirective is appended to EVERY ask. It is self-scoping ("If this
// question asks for a list/table…"), so the model applies it only to enumeration
// questions — which makes it language-agnostic (it fires on Turkish "tabloda ver"
// too, where an English-keyword gate silently did not) and a no-op for ordinary
// Q&A (validated: a non-list answer was unchanged and coherent).
//
// It targets the GENERATION-DROP class: items present in the grounding that a
// small model omits while building a table (a topic dropped 4/4 runs went to 8/8
// 4/4 with this). The other class — items never retrieved because output-format
// words skewed the query embedding — is handled upstream by stripPresentation on
// the retrieval query (see askContext); the two are independent and both needed.
// `tela ask-eval` reports which class any remaining miss falls in.
const askEnumerationDirective = "\n\nIf this question asks for a list, table, or complete set of items, be exhaustive within the question's scope: " +
	"scan every excerpt that belongs to that scope (the same project/space/area — see each excerpt's 'Space › path' label) and include every item there that qualifies — " +
	"including items mentioned only in passing or in prose, not just those already collected in a list or table — but do NOT pull in items from unrelated projects/spaces. " +
	"Before finishing, re-read those excerpts and add any you missed."

// RAGAskStream is the streaming (SSE) twin of RAGAsk: identical retrieval and
// prompt, but the answer is streamed token-by-token over text/event-stream so the
// UI renders it live. Generation runs as a DETACHED job (see ask_job.go) — the
// LLM fills a replayable event log on its own context and this handler merely
// tails it, so a dropped connection (e.g. backgrounded mobile Safari) can't kill
// the answer: the client reconnects via GET ?id= and replays. Events:
//   - meta:      { id: "…" }                                 (first; the resume id)
//   - sources:   { sources: []Hit, low_confidence: bool }  (before generation)
//   - token:     { t: "…" }                                 (per delta)
//   - followups: { followups: []string }                    (after the answer)
//   - done:      {}
//   - error:     { code: "completion_failed" }              (generation failed)
//
// The JSON /api/rag/ask is left untouched for MCP + non-web clients.
func (s *Server) RAGAskStream(w http.ResponseWriter, r *http.Request) {
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
	var req askRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}
	if strings.TrimSpace(req.Question) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "question is required")
		return
	}
	spaceID := req.SpaceID
	if b := bearerSpace(r); b != nil {
		spaceID = b
	}

	// Retrieval + the compute guards run synchronously on the request, BEFORE any
	// SSE byte, so a retrieval 500 / 429 / cap stays a clean HTTP status. Retrieval
	// is sub-second; only the long, silent generation gets detached below.
	excerpts, hits, top, err := s.askContext(r.Context(), u.ID, req.Question, spaceID, req.Limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "retrieval failed")
		return
	}
	_ = s.rag.LogAsk(r.Context(), u.ID, spaceID, req.Question, len(hits), top)
	s.recordRequestEvent(r, eventInput{
		Type: evtAsk, ActorUserID: &u.ID, ActorLabel: u.Username,
		Detail: fmt.Sprintf("%q (%d hits)", req.Question, len(hits)),
	})
	// No LLM call on the zero-hit path, so skip the compute guard there.
	if len(hits) > 0 && !s.askComputeOK(w, r, u, "ask") {
		return
	}

	// Build the job and seed the events known up front (sources, and the zero-hit
	// terminal). The seeded sources event means a reconnect replays the citations
	// too, not just the answer tokens.
	low := lowConfidence(s.rag.RerankEnabled(), top)
	job := newAskJob(newAskID(), u.ID)
	job.emit("sources", map[string]any{"sources": hits, "low_confidence": low})
	if len(hits) == 0 {
		job.emit("token", map[string]string{"t": "I couldn't find anything in your documents to answer that."})
		job.emit("done", map[string]any{})
		job.finish()
	} else {
		// Generation outlives this request: a detached context (capped so a wedged
		// upstream can't leak the goroutine) instead of r.Context(), which cancels
		// the instant the client disconnects.
		go s.runAsk(job, u, req.Question, excerpts, hits, low)
	}
	s.askJobs.put(job)

	sse, ok := newSSEWriter(w)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}
	_ = sse.event("meta", map[string]string{"id": job.id})
	s.streamJob(r.Context(), sse, job)
}

// RAGAskAttach re-attaches to an in-flight or just-finished ask by id (the
// reconnect path: GET /api/rag/ask/stream?id=). It replays the job's event log
// from the start — so the client resets its accumulated answer and rebuilds from
// the replay — then live-tails the rest. Scoped to the job's owner; an unknown or
// expired id is a 404 (the client then surfaces a normal error). Charges no
// compute: the cap was already paid when the job was created.
func (s *Server) RAGAskAttach(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	job := s.askJobs.get(r.URL.Query().Get("id"))
	if job == nil || job.userID != u.ID {
		writeError(w, http.StatusNotFound, "not_found", "no such ask")
		return
	}
	sse, ok := newSSEWriter(w)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}
	_ = sse.event("meta", map[string]string{"id": job.id})
	s.streamJob(r.Context(), sse, job)
}

// streamJob tails a job's event log to the SSE writer until the job finishes or
// the client disconnects. Shared by the initial stream and the reconnect; both
// replay from 0, so the client treats each (re)connection as authoritative.
func (s *Server) streamJob(ctx context.Context, sse *sseWriter, job *askJob) {
	_, _ = job.tail(ctx, 0, func(name string, data json.RawMessage) error {
		return sse.event(name, data)
	})
}

// runAsk is the detached generation goroutine: it streams the LLM completion into
// the job's event log (token frames), then the follow-ups and a terminal done,
// or a single error frame if generation fails. It runs on its own time-bounded
// context so a dropped client connection never cancels it.
func (s *Server) runAsk(job *askJob, u *auth.User, question, excerpts string, hits []rag.Hit, low bool) {
	ctx, cancel := context.WithTimeout(context.Background(), askGenMaxDuration)
	defer cancel()
	defer job.finish()

	// Low retrieval confidence → still answer, but lead with the deterministic
	// callout (emitted as the first tokens so it survives prose-only views).
	var answer strings.Builder
	if low {
		answer.WriteString(lowConfidenceNote)
		job.emit("token", map[string]string{"t": lowConfidenceNote})
	}
	conflicts := s.askConflictNote(ctx, hits)
	err := s.llm.CompleteStream(ctx, askSystemPrompt,
		askUserPrompt(excerpts, conflicts, question),
		func(tok string) error {
			answer.WriteString(tok)
			job.emit("token", map[string]string{"t": tok})
			return nil
		})
	if err != nil {
		slog.Warn("ask: generation failed", "err", err, "job", job.id)
		job.emit("error", map[string]string{"code": "completion_failed"})
		return
	}
	if f := s.genFollowups(ctx, u, question, answer.String()); len(f) > 0 {
		job.emit("followups", map[string]any{"followups": f})
	}
	job.emit("done", map[string]any{})
}

// bearerSpace returns the space a space-pinned bearer key is locked to (else
// nil), the shared narrow used by every read endpoint below.
func bearerSpace(r *http.Request) *int64 {
	if k, isBearer := auth.APIKeyFromContext(r.Context()); isBearer && k.SpaceID != nil {
		return k.SpaceID
	}
	return nil
}

// RAGRelated handles GET /api/pages/{id}/related[?limit=]
// Semantically related pages ("see also") for a page, access-scoped. 404 when
// the page is out of scope; works without a live embedder (uses stored vectors).
func (s *Server) RAGRelated(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	pid, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || pid <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "page id must be a positive integer")
		return
	}
	// Verify read access to the source page (404s if out of scope).
	if _, ae := s.getPageCore(r.Context(), u, k, pid); ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	related, err := s.rag.RelatedPages(r.Context(), u.ID, pid, bearerSpace(r), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "related lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"related": related})
}

// suggestLinksRequest is the POST /api/rag/suggest-links body.
type suggestLinksRequest struct {
	Text    string `json:"text"`
	SpaceID *int64 `json:"space_id"`
	Limit   int    `json:"limit"`
}

// RAGSuggestLinks handles POST /api/rag/suggest-links {text, space_id?, limit?}
// Existing pages the draft text should link to (assisted authoring). Needs a
// live embedder (the draft isn't indexed). 503 when the embedder is off.
func (s *Server) RAGSuggestLinks(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	if !s.aiEnabled() {
		writeError(w, http.StatusServiceUnavailable, "rag_disabled", "semantic features are not configured")
		return
	}
	if !s.embedRateOK(w, account{Kind: accountUser, ID: u.ID}) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, cloudMaxRequestBytes)
	var req suggestLinksRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}
	spaceID := req.SpaceID
	if b := bearerSpace(r); b != nil {
		spaceID = b
	}
	out, err := s.rag.SuggestLinks(r.Context(), u.ID, req.Text, spaceID, req.Limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "suggest-links failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"suggestions": out})
}

// RAGOverlaps handles GET /api/rag/overlaps[?space_id=&threshold=&limit=]
// Near-duplicate page pairs for wiki hygiene, access-scoped to the caller.
func (s *Server) RAGOverlaps(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	var spaceID *int64
	if v := q.Get("space_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "space_id must be a positive integer")
			return
		}
		spaceID = &id
	}
	if b := bearerSpace(r); b != nil {
		spaceID = b
	}
	threshold, _ := strconv.ParseFloat(q.Get("threshold"), 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	pairs, err := s.rag.FindOverlaps(r.Context(), u.ID, spaceID, threshold, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "overlap lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"overlaps": pairs})
}

// RAGGaps handles GET /api/rag/gaps[?space_id=&since_days=&limit=]
// Knowledge gaps: the most-asked questions the corpus couldn't answer. Open to
// every signed-in user — rag.GapScope, not this handler, is what keeps one user's
// questions out of another's view (own asks + asks in spaces they're a member of;
// instance admins see the whole instance).
func (s *Server) RAGGaps(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	sinceDays, _ := strconv.Atoi(q.Get("since_days"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	scope := rag.GapScope{UserID: u.ID, Instance: u.IsInstanceAdmin}
	if sid, err := strconv.ParseInt(q.Get("space_id"), 10, 64); err == nil && sid > 0 {
		scope.SpaceID = &sid
	}
	// A space-scoped bearer key may only ever see its one space.
	if b := bearerSpace(r); b != nil {
		scope.SpaceID = b
	}
	gaps, err := s.rag.KnowledgeGaps(r.Context(), scope, sinceDays, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "gaps query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"gaps": gaps})
}
