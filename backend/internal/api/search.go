package api

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	"github.com/zcag/tela/backend/internal/auth"
)

const searchLimit = 25

// publicRankPenalty soft-demotes hits from public-but-non-member spaces in the
// cross-space palette: a member page wins a near-tie, a strongly-matching public
// page still ranks. (Within a single space the whole result set shares one
// membership state, so the factor is a no-op there.)
const publicRankPenalty = 0.4

// headlineOpts configures ts_headline: one <mark>-wrapped fragment, ~8-24 words,
// matching the snippet contract the frontend renders.
const headlineOpts = `StartSel=<mark>, StopSel=</mark>, MaxFragments=1, MaxWords=24, MinWords=8, ShortWord=2`

// stripExcalidrawSQL is the in-SQL Excalidraw-fence strip (mirrors the Go
// rag.StripExcalidrawFences) applied before ts_headline so snippets never show
// drawing JSON. The body fed to search_tsv (migration 0004) is stripped the
// same way, so ranking and snippeting agree.
const stripExcalidrawSQL = "regexp_replace(p.body, '```excalidraw.*?```', '', 'g')"

// searchAccessSQL is the joinable access set for palette search: the caller's
// own space_access UNION every space published public. Yields space_id plus
// is_member (1 = reachable via membership, 0 = reachable only because the space
// is public); a space that is both collapses to 1, so membership always wins.
//
// Mirrors rag.accessibleSpacesSQL for the lexical tier — same rule, and the two
// must agree or a page would surface in one tier and 403 in the other. is_member
// is load-bearing twice over: it soft-demotes public-only hits (publicRankPenalty)
// AND decides which URL the hit links to (authed route vs public reader).
//
// uid must be the already-bound $N placeholder for the user id.
func searchAccessSQL(uid string) string {
	return `(SELECT space_id, max(is_member) AS is_member FROM (
	           SELECT space_id, 1 AS is_member FROM space_access WHERE user_id = ` + uid + `
	           UNION ALL SELECT id, 0 FROM spaces WHERE visibility = 'public'
	         ) a GROUP BY space_id)`
}

type searchHit struct {
	PageID     int64    `json:"page_id"`
	SpaceID    int64    `json:"space_id"`
	Title      string   `json:"title"`
	Snippet    string   `json:"snippet"`
	Breadcrumb []string `json:"breadcrumb"`
	// Public marks a hit the caller can read ONLY because its space is published
	// (they hold no space_access). Such a hit must link to the no-login reader —
	// the authed route 403s — so the palette branches on this, and URL below is
	// already built accordingly.
	Public bool `json:"public"`
	// URL is the human-shareable in-app link, used by the search-results widget
	// (click-through) and as the ChatGPT search/fetch `url` field.
	URL string `json:"url"`
	// ID + Text are ChatGPT Deep-Research compatibility aliases: that connector
	// drives `fetch` off result `id` and reads the snippet from `text`. They mirror
	// PageID (as a string) and Snippet so one `search` tool serves both surfaces.
	ID   string `json:"id"`
	Text string `json:"text"`
}

// Search is the Tier-2 server-side full-text search behind the command palette.
//
// Ranked Postgres FTS over pages.search_tsv (migration 0004): title weighted
// above body, Excalidraw stripped, ordered by ts_rank_cd, snippet via
// ts_headline. websearch_to_tsquery parses the user's raw input forgivingly
// (quotes/operators tolerated, never errors) so the box can't 500 on
// punctuation. Scoped through space_access plus public spaces. The instant
// client-side tiers (Orama
// titles + bodies) paint first; this fills in ranked hits on the debounce.
func (s *Server) Search(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	results, ae := s.searchCore(r.Context(), u, k, r.URL.Query().Get("q"), nil, searchLimit)
	if ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// searchCore is the transport-agnostic core behind GET /api/search and the MCP
// search tool: ranked FTS over the caller's accessible pages. spaceFilter
// narrows to one space (the MCP tool's optional space_id); a space-pinned bearer
// key overrides it to its own space. Empty query returns an empty slice (the
// command palette calls this on every keystroke). limit ≤ 0 falls back to
// searchLimit.
func (s *Server) searchCore(ctx context.Context, u *auth.User, k *auth.APIKey, query string, spaceFilter *int64, limit int) ([]searchHit, *apiErr) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []searchHit{}, nil
	}
	if limit <= 0 {
		limit = searchLimit
	}
	// A space-pinned bearer key is a strict ceiling: force its space regardless
	// of any caller-provided filter (without it we'd surface titles from any
	// space the user is a member of, even though the scope forbids opening them).
	if k != nil && k.SpaceID != nil {
		spaceFilter = k.SpaceID
	}

	var (
		rows *sql.Rows
		err  error
	)
	if spaceFilter != nil {
		rows, err = s.DB.QueryContext(ctx, `
			SELECT p.id, p.space_id, p.title, sm.is_member,
			       ts_headline('english', `+stripExcalidrawSQL+`,
			                   websearch_to_tsquery('english', $3), $4) AS snippet
			FROM pages p
			JOIN `+searchAccessSQL("$1")+` sm ON sm.space_id = p.space_id
			WHERE p.space_id = $2 AND p.deleted_at IS NULL AND p.search_tsv @@ websearch_to_tsquery('english', $3)
			ORDER BY ts_rank_cd(p.search_tsv, websearch_to_tsquery('english', $3)) DESC, p.updated_at DESC
			LIMIT $5`, u.ID, *spaceFilter, query, headlineOpts, limit)
	} else {
		rows, err = s.DB.QueryContext(ctx, `
			SELECT p.id, p.space_id, p.title, sm.is_member,
			       ts_headline('english', `+stripExcalidrawSQL+`,
			                   websearch_to_tsquery('english', $2), $3) AS snippet
			FROM pages p
			JOIN `+searchAccessSQL("$1")+` sm ON sm.space_id = p.space_id
			WHERE p.deleted_at IS NULL AND p.search_tsv @@ websearch_to_tsquery('english', $2)
			ORDER BY ts_rank_cd(p.search_tsv, websearch_to_tsquery('english', $2))
			         * (CASE WHEN sm.is_member = 1 THEN 1.0 ELSE `+strconv.FormatFloat(publicRankPenalty, 'f', -1, 64)+` END) DESC,
			         p.updated_at DESC
			LIMIT $4`, u.ID, query, headlineOpts, limit)
	}
	if err != nil {
		return nil, &apiErr{http.StatusInternalServerError, "internal", "search query failed"}
	}
	defer rows.Close()

	type hitRow struct {
		ID, SpaceID int64
		Title       string
		IsMember    int
		Snippet     string
	}
	hits := []hitRow{}
	for rows.Next() {
		var h hitRow
		if err := rows.Scan(&h.ID, &h.SpaceID, &h.Title, &h.IsMember, &h.Snippet); err != nil {
			return nil, &apiErr{http.StatusInternalServerError, "internal", "scan search row failed"}
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, &apiErr{http.StatusInternalServerError, "internal", "iterate search rows failed"}
	}

	results := make([]searchHit, 0, len(hits))
	// Hits cluster by space, so memoize the pretty prefix per space rather than
	// re-resolving it for every public hit.
	prefixes := map[int64]string{}
	for _, h := range hits {
		bc, err := pageBreadcrumb(ctx, s.DB, h.ID)
		if err != nil {
			return nil, &apiErr{http.StatusInternalServerError, "internal", "build breadcrumb failed"}
		}
		// A public-only hit is unreachable on the authed route, so link it at the
		// no-login reader — for the palette AND for the `url` handed to agents
		// (MCP search, the ChatGPT connector), which was serving 403s.
		public := h.IsMember == 0
		path := pageAppPath(h.SpaceID, h.ID, h.Title)
		if public {
			pre, seen := prefixes[h.SpaceID]
			if !seen {
				pre = s.spacePrettyPrefix(ctx, h.SpaceID)
				prefixes[h.SpaceID] = pre
			}
			path = readerPathUnder(pre, h.SpaceID, h.ID, h.Title)
		}
		results = append(results, searchHit{
			PageID:     h.ID,
			SpaceID:    h.SpaceID,
			Title:      h.Title,
			Snippet:    h.Snippet,
			Breadcrumb: bc,
			Public:     public,
			URL:        canonicalBaseURL() + path,
			ID:         strconv.FormatInt(h.ID, 10),
			Text:       h.Snippet,
		})
	}
	return results, nil
}

// pageBreadcrumb returns ancestor titles for pageID, ordered root → immediate
// parent. The page's own title is excluded (it's already on the hit). Empty
// slice for root pages.
func pageBreadcrumb(ctx context.Context, db *sql.DB, pageID int64) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		WITH RECURSIVE ancestors(id, parent_id, title, depth) AS (
		  SELECT id, parent_id, title, 0 FROM pages
		    WHERE id = (SELECT parent_id FROM pages WHERE id = $1)
		  UNION ALL
		  SELECT p.id, p.parent_id, p.title, a.depth + 1
		    FROM ancestors a JOIN pages p ON p.id = a.parent_id
		)
		SELECT title FROM ancestors ORDER BY depth DESC`, pageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	titles := []string{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		titles = append(titles, t)
	}
	return titles, rows.Err()
}
