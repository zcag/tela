package api

import (
	"net/http"

	"github.com/zcag/tela/backend/internal/auth"
)

// Per-user hidden spaces. A hide is just a (user, space) edge that tucks a space
// behind the sidebar tree's "Show hidden" row. It is decluttering, NOT access
// control: /spaces, search and the command palette are untouched and the space
// stays fully reachable by URL. Visibility is still governed by space_access, so
// the list read re-gates through it and hiding requires at least viewer access.
// The list returns ids only — the frontend already holds the full Space objects
// from GET /api/spaces and partitions them by this set. Mirrors pinned_spaces;
// see migration 0068_hidden_spaces.sql.

// hiddenSpace is the wire shape for the hidden-spaces list: id + hide time.
type hiddenSpace struct {
	SpaceID   int64  `json:"space_id"`
	CreatedAt string `json:"created_at"`
}

// ListHiddenSpaces returns the caller's hidden space ids, most-recent first,
// re-gated through space_access so a hide of a now-inaccessible space drops out.
func (s *Server) ListHiddenSpaces(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT hs.space_id, hs.created_at
		  FROM hidden_spaces hs
		  JOIN (SELECT DISTINCT space_id FROM space_access WHERE user_id = $1) sa
		    ON sa.space_id = hs.space_id
		 WHERE hs.user_id = $1
		 ORDER BY hs.created_at DESC`, u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "list hidden spaces failed")
		return
	}
	defer rows.Close()

	items := []hiddenSpace{}
	for rows.Next() {
		var it hiddenSpace
		if err := rows.Scan(&it.SpaceID, &it.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "scan hidden space row failed")
			return
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "iterate hidden spaces failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hidden_spaces": items})
}

// AddHiddenSpace hides a space for the caller. Requires viewer+ access;
// idempotent (re-hiding is a no-op).
func (s *Server) AddHiddenSpace(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	if _, ae := s.membershipCore(r.Context(), u, k, id); ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	if _, err := s.DB.ExecContext(r.Context(),
		`INSERT INTO hidden_spaces (user_id, space_id) VALUES ($1, $2)
		 ON CONFLICT (user_id, space_id) DO NOTHING`, u.ID, id); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "hide space failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"is_hidden": true})
}

// DeleteHiddenSpace unhides a space for the caller. Idempotent — unhiding a
// space that isn't hidden (or no longer exists) still returns 204.
func (s *Server) DeleteHiddenSpace(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	if _, err := s.DB.ExecContext(r.Context(),
		`DELETE FROM hidden_spaces WHERE user_id = $1 AND space_id = $2`, u.ID, id); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "unhide space failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
