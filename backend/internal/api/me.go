package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/zcag/tela/backend/internal/auth"
)

type changePasswordRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

const maxBioLen = 280
const maxDisplayNameLen = 80

type updateProfileRequest struct {
	// DisplayName is the human-readable name used to address the user (greeting,
	// etc.), distinct from the URL-safe username. Empty string clears it (the UI
	// then falls back to the username).
	DisplayName *string `json:"display_name"`
	// Bio is the author blurb shown on /u/{handle}. Empty string clears it.
	Bio *string `json:"bio"`
	// ShowBackfillDot toggles the sidebar staleness dot for this account (all
	// devices). Nil leaves it alone — the field is opt-in per request like the
	// other two.
	ShowBackfillDot *bool `json:"show_backfill_dot"`
}

// UpdateMyProfile patches the caller's own profile fields (display name, bio).
// PATCH /api/users/me. Self-service, no privilege beyond being signed in. Only
// the supplied fields are touched; the response echoes the saved values.
func (s *Server) UpdateMyProfile(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	var req updateProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}
	if req.Bio == nil && req.DisplayName == nil && req.ShowBackfillDot == nil {
		writeError(w, http.StatusBadRequest, "no_fields", "nothing to update")
		return
	}

	out := map[string]any{}
	if req.DisplayName != nil {
		displayName := strings.TrimSpace(*req.DisplayName)
		if len(displayName) > maxDisplayNameLen {
			writeError(w, http.StatusBadRequest, "invalid_display_name", "display name exceeds 80 characters")
			return
		}
		if _, err := s.DB.ExecContext(r.Context(),
			`UPDATE users SET display_name = $1, updated_at = tela_now() WHERE id = $2`, displayName, u.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "update profile failed")
			return
		}
		out["display_name"] = displayName
	}
	if req.Bio != nil {
		bio := strings.TrimSpace(*req.Bio)
		if len(bio) > maxBioLen {
			writeError(w, http.StatusBadRequest, "invalid_bio", "bio exceeds 280 characters")
			return
		}
		if _, err := s.DB.ExecContext(r.Context(),
			`UPDATE users SET bio = $1, updated_at = tela_now() WHERE id = $2`, bio, u.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "update profile failed")
			return
		}
		out["bio"] = bio
	}
	if req.ShowBackfillDot != nil {
		v := 0
		if *req.ShowBackfillDot {
			v = 1
		}
		if _, err := s.DB.ExecContext(r.Context(),
			`UPDATE users SET show_backfill_dot = $1, updated_at = tela_now() WHERE id = $2`, v, u.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "update profile failed")
			return
		}
		out["show_backfill_dot"] = *req.ShowBackfillDot
	}
	writeJSON(w, http.StatusOK, out)
}

type sessionDTO struct {
	ID         string `json:"id"`
	LastSeenAt string `json:"last_seen_at"`
	ExpiresAt  string `json:"expires_at"`
	CreatedAt  string `json:"created_at"`
	UserAgent  string `json:"user_agent"`
	Current    bool   `json:"current"`
}

// ChangePassword lets the authenticated user rotate their own password.
// Verifies old_password against the stored hash; on success rewrites the
// hash and drops every session for this user except the current one.
func (s *Server) ChangePassword(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	currentSession, ok := currentSessionID(w, r)
	if !ok {
		return
	}

	var req changePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}
	if len(req.NewPassword) < minPasswordLen {
		writeError(w, http.StatusBadRequest, "bad_request", "password must be at least 8 characters")
		return
	}

	ctx := r.Context()
	var hash string
	if err := s.DB.QueryRowContext(ctx,
		`SELECT password_hash FROM users WHERE id = $1 AND is_active = 1`, u.ID).Scan(&hash); err != nil {
		// Middleware already validated the user is active; treat any miss as 401.
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid credentials")
		return
	}
	matched, _ := auth.VerifyPassword(req.OldPassword, hash)
	if !matched {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid credentials")
		return
	}

	newHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "hash password failed")
		return
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "begin tx failed")
		return
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET password_hash = $1, updated_at = tela_now() WHERE id = $2`,
		newHash, u.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "update password failed")
		return
	}
	if err := auth.DeleteUserSessionsExcept(ctx, tx, u.ID, currentSession); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "clear sessions failed")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "commit failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListMySessions returns every active session row for the calling user,
// flagging the one whose id matches the request cookie. Sorted last_seen_at
// DESC so the most-recent device is first.
func (s *Server) ListMySessions(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	currentSession, _ := requestSessionID(r)

	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT id, last_seen_at, expires_at, created_at, user_agent
		  FROM sessions
		 WHERE user_id = $1
		 ORDER BY last_seen_at DESC`, u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "list sessions failed")
		return
	}
	defer rows.Close()

	sessions := []sessionDTO{}
	for rows.Next() {
		var dto sessionDTO
		if err := rows.Scan(&dto.ID, &dto.LastSeenAt, &dto.ExpiresAt, &dto.CreatedAt, &dto.UserAgent); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "scan session row failed")
			return
		}
		dto.Current = dto.ID == currentSession
		sessions = append(sessions, dto)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "iterate sessions failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

// DeleteMySession revokes a single non-current session by id. Refuses to
// delete the current session — callers should hit /api/auth/logout for that.
func (s *Server) DeleteMySession(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	currentSession, ok := currentSessionID(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("id")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "session id is required")
		return
	}
	if sessionID == currentSession {
		writeError(w, http.StatusBadRequest, "bad_request", "use /api/auth/logout for current session")
		return
	}

	var owner int64
	err := s.DB.QueryRowContext(r.Context(),
		`SELECT user_id FROM sessions WHERE id = $1`, sessionID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && owner != u.ID) {
		writeError(w, http.StatusNotFound, "not_found", "session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "lookup session failed")
		return
	}

	if _, err := s.DB.ExecContext(r.Context(),
		`DELETE FROM sessions WHERE id = $1`, sessionID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "delete session failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteAllMySessionsExceptCurrent is the "logout everywhere" endpoint —
// removes every session for the caller except the one tied to the request
// cookie.
func (s *Server) DeleteAllMySessionsExceptCurrent(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	currentSession, ok := currentSessionID(w, r)
	if !ok {
		return
	}
	if err := auth.DeleteUserSessionsExcept(r.Context(), s.DB, u.ID, currentSession); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "clear sessions failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// requestSessionID returns the session id from the request cookie. Returns
// ("", false) when the cookie is absent — distinct from currentSessionID
// which writes 401 on miss.
func requestSessionID(r *http.Request) (string, bool) {
	c, err := r.Cookie(auth.CookieName)
	if err != nil || c.Value == "" {
		return "", false
	}
	return c.Value, true
}

// currentSessionID reads the session cookie or writes a 401 envelope. Used
// by /api/users/me/* handlers that need to scope mutations to the current
// session. Middleware will have already validated the cookie, so a miss
// here implies a misconfiguration — return the same envelope.
func currentSessionID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, ok := requestSessionID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "not authenticated")
		return "", false
	}
	return id, true
}
