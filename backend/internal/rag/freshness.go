package rag

import "context"

// SpaceFreshness summarizes how well-indexed one space is, for the admin
// freshness view and the stale indicators. All counts are scoped to pages the
// caller can access.
type SpaceFreshness struct {
	SpaceID      int64  `json:"space_id"`
	Name         string `json:"name"`
	Pages        int    `json:"pages"`         // total pages in the space
	IndexedPages int    `json:"indexed_pages"` // pages with indexable content that have been indexed
	StalePages   int    `json:"stale_pages"`   // non-empty pages edited since last index (or never indexed)
	ChunkCount   int    `json:"chunk_count"`
	LastIndexed  string `json:"last_indexed"` // latest pages.indexed_at across the space ("" if none)
}

// PageFreshness is the per-page status within a space.
type PageFreshness struct {
	PageID      int64  `json:"page_id"`
	Title       string `json:"title"`
	Status      string `json:"status"` // "fresh" | "stale" | "unindexed" | "empty"
	ChunkCount  int    `json:"chunk_count"`
	UpdatedAt   string `json:"updated_at"`
	LastIndexed string `json:"last_indexed"` // "" if never indexed
}

// excalidrawStripSQL is StripExcalidrawFences (chunk.go) expressed in SQL: a
// page's body with every ```excalidraw drawing fence removed. The indexer
// strips these before chunking, so a drawing-only page yields ZERO chunks even
// though its raw body is large — measuring "has content" against the raw body
// then makes such a page read as perpetually stale (no chunks, never catches
// up). Keep the pattern in sync with excalidrawFenceRE in chunk.go.
const excalidrawStripSQL = "regexp_replace(p.body, '```excalidraw([ \\t]+[^\\n]*)?\\n.*?\\n?```', '', 'g')"

// hasIndexableBody is true when a page has any non-whitespace content AFTER the
// excalidraw strip — i.e. content the indexer would actually chunk. This is the
// predicate's notion of "non-empty", aligned with what the indexer does so a
// drawing-only page reads as empty (nothing to index), not stale.
const hasIndexableBody = excalidrawStripSQL + ` ~ '[^[:space:]]'`

// staleExpr is the SQL predicate for "this page's index is out of date": it was
// never indexed, OR it was edited since it was indexed, OR some of its chunks
// have no vector yet. Datetimes are lexically comparable TEXT. Shared so the
// space rollup and the per-page status agree exactly.
//
// It keys on pages.indexed_at (migration 0076), NOT on whether chunk rows exist.
// A page can index to ZERO chunks legitimately — strip the ```excalidraw fence
// off a drawing-only page and nothing chunkable is left — and inferring "never
// indexed" from that made such a page permanently stale and re-swept forever.
//
// The unembedded clause is the other half: a reindex writes its chunk rows
// before it embeds them (see writeChunks) so the work survives a failure, which
// means a page can hold a full set of rows and still be half-indexed. Counting
// rows alone would read that as fresh, the sweep would stop re-queueing it, and
// the page would sit silently missing from vector search.
const staleExpr = hasIndexableBody + ` AND (p.indexed_at IS NULL OR p.updated_at > p.indexed_at OR pc.unembedded > 0)`

// chunkAggCTE aggregates page_chunks to (page_id, chunk count, unembedded count,
// last-indexed). unembedded is coalesced by every caller's LEFT JOIN, so a page
// with no chunks contributes NULL — never a false "has unembedded chunks".
const chunkAggCTE = `pc AS (SELECT page_id, count(*) AS cnt,
	count(*) FILTER (WHERE embedding IS NULL) AS unembedded,
	max(updated_at) AS idx FROM page_chunks GROUP BY page_id)`

// IndexHealth is a corpus-wide (NOT user-scoped) snapshot of index health, for
// the background sweep's log line and ops/observability. ModelDriftChunks counts
// chunks embedded by a model other than the one currently configured (excluding
// legacy blank-model rows) — the signal for "a re-embed against the live model is pending".
type IndexHealth struct {
	ContentPages     int // pages with non-empty body
	IndexedPages     int // pages with indexable content that have been indexed
	StalePages       int // non-empty pages with a missing or out-of-date index
	Chunks           int // total chunk rows
	ModelDriftChunks int // chunks on a non-current, non-legacy model
}

// IndexHealth computes the corpus-wide index-health snapshot in two small
// aggregate queries.
func (s *Service) IndexHealth(ctx context.Context) (IndexHealth, error) {
	var h IndexHealth
	if err := s.db.QueryRowContext(ctx, `
		WITH `+chunkAggCTE+`
		SELECT
		  count(p.id) FILTER (WHERE `+hasIndexableBody+`)      AS content_pages,
		  count(p.id) FILTER (WHERE `+hasIndexableBody+` AND p.indexed_at IS NOT NULL) AS indexed_pages,
		  count(p.id) FILTER (WHERE `+staleExpr+`)             AS stale_pages
		  FROM pages p
		  LEFT JOIN pc ON pc.page_id = p.id
		 WHERE p.deleted_at IS NULL`,
	).Scan(&h.ContentPages, &h.IndexedPages, &h.StalePages); err != nil {
		return h, err
	}
	model := ""
	if s.emb != nil {
		model = s.emb.Model()
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE embed_model <> '' AND embed_model <> $1)
		  FROM page_chunks`, model,
	).Scan(&h.Chunks, &h.ModelDriftChunks); err != nil {
		return h, err
	}
	return h, nil
}

// stalePageIDs returns up to limit page ids whose index is missing or out of
// date (corpus-wide, most-recently-edited first) — the stale-sweep's work list.
func (s *Service) stalePageIDs(ctx context.Context, limit int) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH `+chunkAggCTE+`
		SELECT p.id
		  FROM pages p
		  LEFT JOIN pc ON pc.page_id = p.id
		 WHERE p.deleted_at IS NULL AND `+staleExpr+`
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

// Freshness returns per-space index health for every space userID can access.
func (s *Service) Freshness(ctx context.Context, userID int64) ([]SpaceFreshness, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH acc AS (SELECT DISTINCT space_id FROM space_access WHERE user_id = $1),
		     `+chunkAggCTE+`
		SELECT sp.id, sp.name,
		       count(p.id) AS pages,
		       count(p.id) FILTER (WHERE `+hasIndexableBody+` AND p.indexed_at IS NOT NULL) AS indexed_pages,
		       count(p.id) FILTER (WHERE `+staleExpr+`) AS stale_pages,
		       coalesce(sum(pc.cnt), 0) AS chunk_count,
		       coalesce(max(p.indexed_at), '') AS last_indexed
		  FROM acc
		  JOIN spaces sp ON sp.id = acc.space_id
		  LEFT JOIN pages p ON p.space_id = sp.id AND p.deleted_at IS NULL
		  LEFT JOIN pc ON pc.page_id = p.id
		 GROUP BY sp.id, sp.name
		 ORDER BY sp.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SpaceFreshness{}
	for rows.Next() {
		var f SpaceFreshness
		if err := rows.Scan(&f.SpaceID, &f.Name, &f.Pages, &f.IndexedPages, &f.StalePages, &f.ChunkCount, &f.LastIndexed); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// SpacePageFreshness returns the per-page index status for one space, scoped by
// access (the caller must be able to read the space; the query joins
// space_access so a non-member gets an empty list).
func (s *Service) SpacePageFreshness(ctx context.Context, userID, spaceID int64) ([]PageFreshness, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH acc AS (SELECT DISTINCT space_id FROM space_access WHERE user_id = $1),
		     `+chunkAggCTE+`
		SELECT p.id, p.title, coalesce(pc.cnt, 0) AS chunk_count,
		       p.updated_at, coalesce(p.indexed_at, '') AS last_indexed,
		       CASE
		         WHEN NOT (`+hasIndexableBody+`) THEN 'empty'
		         WHEN p.indexed_at IS NULL THEN 'unindexed'
		         WHEN p.updated_at > p.indexed_at OR pc.unembedded > 0 THEN 'stale'
		         ELSE 'fresh'
		       END AS status
		  FROM pages p
		  JOIN acc ON acc.space_id = p.space_id
		  LEFT JOIN pc ON pc.page_id = p.id
		 WHERE p.space_id = $2 AND p.deleted_at IS NULL
		 ORDER BY p.title`, userID, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PageFreshness{}
	for rows.Next() {
		var f PageFreshness
		if err := rows.Scan(&f.PageID, &f.Title, &f.ChunkCount, &f.UpdatedAt, &f.LastIndexed, &f.Status); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
