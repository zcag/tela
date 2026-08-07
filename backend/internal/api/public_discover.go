package api

import (
	"net/http"
	"strconv"
)

// Cross-tenant public-space DISCOVERY — GET /api/public/discover. The "network"
// view: a login-free directory of every space with visibility='public' across
// the whole instance, regardless of owner. Under /api/public/ (auth.IsPublicPath),
// so the session middleware skips it; it self-authenticates the only way it can —
// by selecting ONLY rows where spaces.visibility = 'public'. A private space can
// never appear: the WHERE clause is the gate, same posture as public_spaces.go.
//
// Strictly read-only and GET-only. The projection is the same narrow, public-by-
// design surface the rest of /api/public/ already exposes (id/name/slug/owner
// handle) plus two aggregate signals — page count and last-updated — computed
// in-query, never any private body or member data.

// discoverSpaceDTO is one directory card. PageCount and UpdatedAt are the
// "popular" and "recent" sort signals. OwnerHandle is the space's OWNING handle
// — the org's slug for an org space, else the personal owner's username — and
// links to /{handle}; PublicPath is the card's canonical /{handle}/{space-slug}.
type discoverSpaceDTO struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
	OwnerHandle string `json:"owner_handle,omitempty"`
	PublicPath  string `json:"public_path,omitempty"`
	PageCount   int64  `json:"page_count"`
	UpdatedAt   string `json:"updated_at"`
}

const (
	discoverDefaultLimit = 20
	discoverMaxLimit     = 100
)

// GetPublicDiscover — GET /api/public/discover?sort=recent|popular&limit=&offset=.
// Returns the public-space network directory with simple pagination + sort.
func (s *Server) GetPublicDiscover(w http.ResponseWriter, r *http.Request) {
	limit := clampQueryInt(r, "limit", discoverDefaultLimit, 1, discoverMaxLimit)
	offset := clampQueryInt(r, "offset", 0, 0, 1<<31)

	// Sort: "popular" ranks by live page count desc; default "recent" ranks by
	// the freshest page activity in the space (NULLS LAST so an empty space sinks).
	// id is the deterministic tiebreaker either way.
	order := "last_updated DESC NULLS LAST, s.id DESC"
	if r.URL.Query().Get("sort") == "popular" {
		order = "page_count DESC, last_updated DESC NULLS LAST, s.id DESC"
	}

	// One pass: public spaces, their owner handle, live (non-deleted) page count,
	// and the most-recent page activity. The page_count / last_updated aggregates
	// come from a LATERAL over non-deleted pages so a space with zero pages still
	// lists (count 0, NULL recency). Owner handle is the 'owner' space_member.
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT s.id, s.name, s.slug, s.description,
		       `+spaceHandleExpr+` AS owner_handle,
		       agg.page_count, agg.last_updated
		  FROM spaces s
		  LEFT JOIN orgs o ON o.id = s.org_id
		  LEFT JOIN LATERAL (
		         SELECT COUNT(*) AS page_count, MAX(p.updated_at) AS last_updated
		           FROM pages p
		          WHERE p.space_id = s.id AND p.deleted_at IS NULL
		       ) agg ON TRUE
		 WHERE s.visibility = 'public'
		 ORDER BY `+order+`
		 LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "load discover failed")
		return
	}
	defer rows.Close()

	out := []discoverSpaceDTO{}
	for rows.Next() {
		var (
			d           discoverSpaceDTO
			lastUpdated *string
		)
		if err := rows.Scan(&d.ID, &d.Name, &d.Slug, &d.Description, &d.OwnerHandle, &d.PageCount, &lastUpdated); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "scan discover row failed")
			return
		}
		if lastUpdated != nil {
			d.UpdatedAt = *lastUpdated
		}
		d.PublicPath = handleSpacePath(d.OwnerHandle, d.Slug)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "iterate discover failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"spaces": out,
		"limit":  limit,
		"offset": offset,
	})
}

// clampQueryInt reads a non-negative integer query param, falling back to def on
// absence/parse error and clamping into [min,max].
func clampQueryInt(r *http.Request, key string, def, min, max int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}
