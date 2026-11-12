package summarize

import "context"

// SpaceSummaries summarizes one space's summary coverage, for the admin
// status view (the summaries sibling of rag.SpaceFreshness). All counts are
// scoped to pages the caller can access and to non-empty bodies.
type SpaceSummaries struct {
	SpaceID       int64  `json:"space_id"`
	Name          string `json:"name"`
	Pages         int    `json:"pages"`          // non-empty-body pages in the space
	Summarized    int    `json:"summarized"`     // status fresh or locked
	Stale         int    `json:"stale"`          // body changed since generation, or missing
	Failed        int    `json:"failed"`         // last attempt errored
	LastGenerated string `json:"last_generated"` // max generated_at across the space ("" if never)
}

// PageSummary is the per-page status within a space.
type PageSummary struct {
	PageID      int64  `json:"page_id"`
	Title       string `json:"title"`
	Status      string `json:"status"`       // "fresh" | "stale" | "missing" | "failed" | "locked" | "empty"
	GeneratedAt string `json:"generated_at"` // "" when never generated
	Model       string `json:"model"`        // "" when never generated
	LastError   string `json:"last_error"`   // "" unless status failed
	UpdatedAt   string `json:"updated_at"`   // page updated_at
}

// statusExpr resolves one page's summary status in SQL, against pages p LEFT
// JOIN page_summaries ps. Precedence: empty body, then the lock, then a
// pending failure, then missing (no record AND no summary — reported under
// stale in counts but distinct per-page so the UI can label it), then a body
// edited since generation (hash drift), else fresh. A clean row whose src_hash
// matches the live body is a terminal state — either a summary was written, OR
// the model abstained (NONE) and props.summary was deliberately left empty;
// both read fresh, so an empty summary alone is NOT stale (this mirrors
// SummarizePage's SkippedFresh check). A failed-only row keeps src_hash ” so it
// can never read fresh by accident.
const statusExpr = `CASE
	WHEN length(btrim(p.body)) = 0 THEN 'empty'
	WHEN coalesce(p.props->>'summary_lock', '') = 'true' THEN 'locked'
	WHEN coalesce(ps.last_error, '') <> '' THEN 'failed'
	WHEN ps.page_id IS NULL AND coalesce(p.props->>'summary', '') = '' THEN 'missing'
	WHEN ps.src_hash IS NULL OR ps.src_hash <> ` + bodyHashExpr + ` THEN 'stale'
	ELSE 'fresh'
END`

// needsWorkExpr is the SQL predicate for "this page should be (re)summarized":
// real content, not locked, and the summary is missing (no row), failed, or
// generated from a different body (hash drift). A clean row matching the live
// body is done — even with an empty summary (a deliberate NONE abstention) — so
// it is NOT re-queued. Shared by the stale sweep and the POST summarize
// queueing so they agree exactly, and consistent with SummarizePage's
// SkippedFresh check. (statusExpr ∈ stale, missing, failed — kept as a flat
// predicate so it can run without the CASE.)
const needsWorkExpr = `length(btrim(p.body)) > 0
	AND coalesce(p.props->>'summary_lock', '') <> 'true'
	AND (coalesce(ps.last_error, '') <> ''
	     OR ps.src_hash IS NULL OR ps.src_hash <> ` + bodyHashExpr + `)`

// generatedJoin guards the provenance columns: a failed-never-generated row
// (src_hash ”) must report generated_at/model as "", not its bookkeeping row's.
const generatedCols = `
	CASE WHEN coalesce(ps.src_hash, '') <> '' THEN ps.generated_at ELSE '' END,
	CASE WHEN coalesce(ps.src_hash, '') <> '' THEN ps.model ELSE '' END`

// SpaceRollup returns per-space summary coverage for every space userID can
// access. Mirrors rag.Service.Freshness (same access scoping).
func (s *Service) SpaceRollup(ctx context.Context, userID int64) ([]SpaceSummaries, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH acc AS (SELECT DISTINCT space_id FROM space_access WHERE user_id = $1)
		SELECT sp.id, sp.name,
		       count(p.id) FILTER (WHERE length(btrim(p.body)) > 0) AS pages,
		       count(p.id) FILTER (WHERE st.s IN ('fresh', 'locked')) AS summarized,
		       count(p.id) FILTER (WHERE st.s IN ('stale', 'missing')) AS stale,
		       count(p.id) FILTER (WHERE st.s = 'failed') AS failed,
		       coalesce(max(CASE WHEN coalesce(ps.src_hash, '') <> '' THEN ps.generated_at END), '') AS last_generated
		  FROM acc
		  JOIN spaces sp ON sp.id = acc.space_id
		  LEFT JOIN pages p ON p.space_id = sp.id AND p.deleted_at IS NULL
		  LEFT JOIN page_summaries ps ON ps.page_id = p.id
		 CROSS JOIN LATERAL (SELECT `+statusExpr+` AS s) st
		 GROUP BY sp.id, sp.name
		 ORDER BY sp.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SpaceSummaries{}
	for rows.Next() {
		var f SpaceSummaries
		if err := rows.Scan(&f.SpaceID, &f.Name, &f.Pages, &f.Summarized, &f.Stale, &f.Failed, &f.LastGenerated); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// SpacePageSummaries returns the per-page summary status for one space, scoped
// by access (a non-member gets an empty list). Mirrors rag.SpacePageFreshness.
func (s *Service) SpacePageSummaries(ctx context.Context, userID, spaceID int64) ([]PageSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH acc AS (SELECT DISTINCT space_id FROM space_access WHERE user_id = $1)
		SELECT p.id, p.title, `+statusExpr+` AS status,`+generatedCols+`,
		       coalesce(ps.last_error, ''), p.updated_at
		  FROM pages p
		  JOIN acc ON acc.space_id = p.space_id
		  LEFT JOIN page_summaries ps ON ps.page_id = p.id
		 WHERE p.space_id = $2 AND p.deleted_at IS NULL
		 ORDER BY p.title`, userID, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PageSummary{}
	for rows.Next() {
		var f PageSummary
		if err := rows.Scan(&f.PageID, &f.Title, &f.Status, &f.GeneratedAt, &f.Model, &f.LastError, &f.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// PageStatus is SpacePageSummaries for exactly one page — same row shape, same
// statusExpr, no access join (the caller has already resolved access).
// Regeneration is async, so props.summary can lag the body it describes by a
// worker hop, and a stale summary is indistinguishable from a current one to
// whatever consumes it — search, RAG grounding, an LLM answering from it. This
// is how a reader (the MCP epistemic block) can tell. GeneratedAt is "" when no
// summary was ever machine-written, which is what separates real drift from a
// hand-authored props.summary that never had a generation to drift from.
func (s *Service) PageStatus(ctx context.Context, pageID int64) (PageSummary, error) {
	var f PageSummary
	err := s.db.QueryRowContext(ctx, `
		SELECT p.id, p.title, `+statusExpr+` AS status,`+generatedCols+`,
		       coalesce(ps.last_error, ''), p.updated_at
		  FROM pages p
		  LEFT JOIN page_summaries ps ON ps.page_id = p.id
		 WHERE p.id = $1 AND p.deleted_at IS NULL`, pageID,
	).Scan(&f.PageID, &f.Title, &f.Status, &f.GeneratedAt, &f.Model, &f.LastError, &f.UpdatedAt)
	return f, err
}

// QueueStaleSpace enqueues every page in spaceID that needs (re)generation —
// stale, missing, or failed — and returns how many it queued. Backs
// POST /api/spaces/{id}/summarize; auth is the caller's job.
func (s *Service) QueueStaleSpace(ctx context.Context, spaceID int64) (int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id
		  FROM pages p
		  LEFT JOIN page_summaries ps ON ps.page_id = p.id
		 WHERE p.space_id = $1 AND p.deleted_at IS NULL AND `+needsWorkExpr+`
		 ORDER BY p.updated_at DESC`, spaceID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return n, err
		}
		s.Queue(id)
		n++
	}
	return n, rows.Err()
}

// Health is a corpus-wide (NOT user-scoped) snapshot for the background
// sweep's log line — the summaries sibling of rag.IndexHealth.
type Health struct {
	ContentPages int // pages with non-empty body
	Summarized   int // status fresh or locked
	Stale        int // stale or missing
	Failed       int // last attempt errored
}

// SummaryHealth computes the corpus-wide snapshot in one aggregate query.
func (s *Service) SummaryHealth(ctx context.Context) (Health, error) {
	var h Health
	err := s.db.QueryRowContext(ctx, `
		SELECT count(p.id) FILTER (WHERE length(btrim(p.body)) > 0),
		       count(p.id) FILTER (WHERE st.s IN ('fresh', 'locked')),
		       count(p.id) FILTER (WHERE st.s IN ('stale', 'missing')),
		       count(p.id) FILTER (WHERE st.s = 'failed')
		  FROM pages p
		  LEFT JOIN page_summaries ps ON ps.page_id = p.id
		 CROSS JOIN LATERAL (SELECT `+statusExpr+` AS s) st
		 WHERE p.deleted_at IS NULL`,
	).Scan(&h.ContentPages, &h.Summarized, &h.Stale, &h.Failed)
	return h, err
}

// stalePageIDs returns up to limit page ids that need (re)generation
// (corpus-wide, most-recently-edited first) — the stale-sweep's work list.
func (s *Service) stalePageIDs(ctx context.Context, limit int) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id
		  FROM pages p
		  LEFT JOIN page_summaries ps ON ps.page_id = p.id
		 WHERE p.deleted_at IS NULL AND `+needsWorkExpr+`
		 ORDER BY p.updated_at DESC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
