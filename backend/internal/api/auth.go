package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/zcag/tela/backend/internal/auth"
)

type authLoginRequest struct {
	// Identifier is an email or a username. The legacy Username field is still
	// accepted as a fallback so older clients keep working.
	Identifier string `json:"identifier"`
	Username   string `json:"username"`
	Password   string `json:"password"`
}

type authUserDTO struct {
	ID              int64     `json:"id"`
	Username        string    `json:"username"`
	DisplayName     string    `json:"display_name"`
	Email           *string   `json:"email"`
	EmailVerified   bool      `json:"email_verified"`
	IsInstanceAdmin bool      `json:"is_instance_admin"`
	Bio             string    `json:"bio"`
	Trial           *trialDTO `json:"trial,omitempty"`           // active trial in its notify window, else nil
	FeedbackUnseen  *int      `json:"feedback_unseen,omitempty"` // unread feedback count (instance admins only)
	MCPConnected    bool      `json:"mcp_connected"`             // has ever made an authenticated MCP request
}

// trialDTO drives the in-app trial banner. Ended distinguishes "ends soon" from
// "ended, in grace until GraceEndsAt" (planFor keeps benefits until then).
type trialDTO struct {
	// PlanKey identifies the trialled TIER, so a client can tell whether the
	// thing it is offering to sell is the thing already being trialled.
	PlanKey     string `json:"plan_key"`
	PlanName    string `json:"plan_name"`
	EndsAt      string `json:"ends_at"`
	GraceEndsAt string `json:"grace_ends_at"`
	Ended       bool   `json:"ended"`
}

// Login authenticates an email-or-username + password pair. On success it
// creates a session, sets the canonical cookie, and returns the user. Failure
// returns a single generic 401 envelope (no bad-user vs bad-password split; the
// argon2id verify still runs against a dummy hash when the user is missing to
// keep response time roughly constant). An account that has an email but hasn't
// confirmed it gets a 403 email_unverified so the UI can offer to resend.
func (s *Server) Login(w http.ResponseWriter, r *http.Request) {
	if !s.allowRateLimit(w, r, "login", s.loginLimiter) {
		return
	}
	var req authLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}
	identifier := strings.TrimSpace(req.Identifier)
	if identifier == "" {
		identifier = strings.TrimSpace(req.Username)
	}
	if identifier == "" || req.Password == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid credentials")
		return
	}

	ctx := r.Context()
	var (
		userID        int64
		username      string
		email         sql.NullString
		emailVerified sql.NullString
		hash          string
		isAdmin       int
	)
	// Match either the username verbatim or the email (case-insensitively, via
	// the lowercased-email column). A username never contains '@', so the two
	// namespaces don't collide in practice.
	err := s.DB.QueryRowContext(ctx, `
		SELECT id, username, email, email_verified_at, password_hash, is_instance_admin
		  FROM users
		 WHERE (username = $1 OR email = $2) AND is_active = 1`,
		identifier, normalizeEmail(identifier),
	).Scan(&userID, &username, &email, &emailVerified, &hash, &isAdmin)

	userMissing := errors.Is(err, sql.ErrNoRows)
	if !userMissing && err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "lookup user failed")
		return
	}
	if userMissing {
		hash = auth.DummyVerifyHash()
	}

	ok, _ := auth.VerifyPassword(req.Password, hash)
	if userMissing || !ok {
		// Failed sign-in. Name the real account when the identifier matched one
		// (wrong password); otherwise log the attempted identifier (no user row).
		var aid *int64
		label := identifier
		if !userMissing {
			aid = &userID
			label = username
		}
		s.recordRequestEvent(r, eventInput{Type: evtAuthLoginFailed, ActorUserID: aid, ActorLabel: label, Detail: "invalid credentials: " + identifier})
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid credentials")
		return
	}

	// Email accounts must confirm before they can sign in. Legacy/bootstrap
	// rows with no email are exempt.
	if email.Valid && !emailVerified.Valid {
		s.recordRequestEvent(r, eventInput{Type: evtAuthLoginFailed, ActorUserID: &userID, ActorLabel: username, Detail: "blocked: email unverified"})
		writeError(w, http.StatusForbidden, "email_unverified", "confirm your email before signing in")
		return
	}

	// Orgs may enforce SSO for their domains: password login is refused so the
	// user is funnelled through their SSO provider. Instance admins are exempt
	// so a misconfigured enforced connection can't lock the operator out.
	if email.Valid && isAdmin != 1 && s.passwordLoginBlocked(ctx, email.String) {
		s.recordRequestEvent(r, eventInput{Type: evtAuthLoginFailed, ActorUserID: &userID, ActorLabel: username, Detail: "blocked: SSO required"})
		writeError(w, http.StatusForbidden, "sso_required", "your organization requires single sign-on")
		return
	}

	// A custom-domain org that disabled the password method blocks it server-
	// side too, not just by hiding the form (the SPA reads the same flag from
	// /api/host-context). Instance admins are exempt as above.
	if isAdmin != 1 && s.passwordLoginBlockedByHost(r) {
		s.recordRequestEvent(r, eventInput{Type: evtAuthLoginFailed, ActorUserID: &userID, ActorLabel: username, Detail: "blocked: password sign-in disabled on domain"})
		writeError(w, http.StatusForbidden, "sso_required", "password sign-in is disabled on this domain")
		return
	}

	// Apply auto-join domains on every sign-in so a domain mapping added after
	// the user already verified still takes effect (idempotent, best-effort).
	if email.Valid {
		applyAutoJoin(ctx, s.DB, userID, email.String)
	}

	sid, err := auth.CreateSession(ctx, s.DB, userID, r.UserAgent())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "create session failed")
		return
	}
	auth.SetSessionCookie(w, sid)
	s.recordRequestEvent(r, eventInput{Type: evtAuthLogin, ActorUserID: &userID, ActorLabel: username})
	writeJSON(w, http.StatusOK, map[string]any{
		"user": authUserDTO{
			ID:              userID,
			Username:        username,
			Email:           nullableString(email),
			EmailVerified:   emailVerified.Valid,
			IsInstanceAdmin: isAdmin == 1,
		},
	})
}

// Logout removes the current session row and clears the cookie. Always 204,
// even when no cookie is present — logout is idempotent.
func (s *Server) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.CookieName); err == nil && c.Value != "" {
		// Resolve the session's user before deleting it so the logout event is
		// attributed (the route runs without the session middleware's context).
		var uid int64
		var uname string
		_ = s.DB.QueryRowContext(r.Context(),
			`SELECT u.id, u.username FROM sessions ss JOIN users u ON u.id = ss.user_id WHERE ss.id = $1`,
			c.Value).Scan(&uid, &uname)
		if uid != 0 {
			s.recordRequestEvent(r, eventInput{Type: evtAuthLogout, ActorUserID: &uid, ActorLabel: uname})
		}
		_ = auth.DeleteSession(r.Context(), s.DB, c.Value)
	}
	auth.SetSessionCookie(w, "")
	w.WriteHeader(http.StatusNoContent)
}

// Me returns the currently authenticated user. Bypasses the middleware so it
// can answer the frontend's boot probe with a clean 401 envelope. Mirrors the
// error-class split from auth.Middleware: ErrInvalidSession → 401, every
// other DB error → 500 (so a transient backend failure doesn't evict the
// signed-in user).
func (s *Server) Me(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(auth.CookieName)
	if err != nil || c.Value == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "not authenticated")
		return
	}
	u, err := auth.LoadSessionAndSlide(r.Context(), s.DB, c.Value)
	if errors.Is(err, auth.ErrInvalidSession) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "not authenticated")
		return
	}
	if err != nil {
		slog.Error("auth.Me: session lookup failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	var email *string
	if u.Email != "" {
		email = &u.Email
	}
	// display_name + bio aren't on the session-loaded user struct; fetch them
	// directly (cheap, single-row) so /api/auth/me can address the user by name
	// and prefill the profile editor.
	var displayName, bio string
	var mcpSeen sql.NullString
	_ = s.DB.QueryRowContext(r.Context(),
		`SELECT display_name, bio, mcp_last_seen_at FROM users WHERE id = $1`, u.ID).Scan(&displayName, &bio, &mcpSeen)

	dto := authUserDTO{
		ID:          u.ID,
		Username:    u.Username,
		DisplayName: displayName,
		Email:       email,
		// A live session implies the account cleared the login email gate
		// (or has no email at all), so an email here is a confirmed one.
		EmailVerified:   u.Email != "",
		IsInstanceAdmin: u.IsInstanceAdmin,
		Bio:             bio,
		Trial:           s.userTrialStatus(r.Context(), u.ID),
		MCPConnected:    mcpSeen.Valid,
	}
	// Unread feedback badge — instance admins only (the inbox is admin-gated).
	if u.IsInstanceAdmin {
		var n int
		_ = s.DB.QueryRowContext(r.Context(), `
			SELECT COUNT(*) FROM feedback f
			 WHERE f.created_at > COALESCE(
			        (SELECT feedback_seen_at FROM users WHERE id = $1), '')`, u.ID).Scan(&n)
		dto.FeedbackUnseen = &n
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": dto})
}

// userTrialStatus returns the user's trial banner state, or nil when there's no
// trial worth surfacing. It's shown only in the window from 7 days before the
// nominal end through the 7-day grace (planFor keeps benefits over that grace),
// so the app-wide banner appears exactly when it's actionable — a month of
// "you're on a trial" chrome would be nagging, not informing.
func (s *Server) userTrialStatus(ctx context.Context, userID int64) *trialDTO {
	return s.loadTrial(ctx, userID, true)
}

// loadTrial reads a user's trial. notifyWindowOnly narrows it to the banner
// window; false returns the trial whenever one is live.
//
// Both callers need the same five fields, so the window is a condition on one
// query rather than a second copy of it. The distinction is real and worth
// keeping: the BANNER should stay quiet until the trial is nearly up, but the
// BILLING SCREEN must always know — it is the one page whose whole job is to
// state what you are on and what you would be buying, and it previously showed
// "Current: Personal" beside "Upgrade to Personal · $8/mo" with no mention of a
// trial anywhere, because its only source of truth was the gated banner.
func (s *Server) loadTrial(ctx context.Context, userID int64, notifyWindowOnly bool) *trialDTO {
	window := ""
	if notifyWindowOnly {
		window = `AND (now() AT TIME ZONE 'UTC') BETWEEN (u.trial_ends_at::timestamp - interval '7 days')
		                                             AND (u.trial_ends_at::timestamp + interval '7 days')`
	}
	var t trialDTO
	// NULLIF guards the empty string: ''::timestamp RAISES in Postgres and '' is
	// this schema's convention for an empty TEXT datetime (see planFor).
	err := s.DB.QueryRowContext(ctx, `
		SELECT u.trial_plan_key,
		       COALESCE(p.name, u.trial_plan_key),
		       u.trial_ends_at,
		       to_char(u.trial_ends_at::timestamp + interval '7 days', 'YYYY-MM-DD HH24:MI:SS'),
		       (u.trial_ends_at::timestamp <= (now() AT TIME ZONE 'UTC')) AS ended
		  FROM users u
		  LEFT JOIN plans p ON p.key = u.trial_plan_key
		 WHERE u.id = $1
		   AND u.trial_plan_key IS NOT NULL
		   AND NULLIF(u.trial_ends_at, '') IS NOT NULL
		   `+window,
		userID).Scan(&t.PlanKey, &t.PlanName, &t.EndsAt, &t.GraceEndsAt, &t.Ended)
	if err != nil {
		return nil // no row / no trial (in the window)
	}
	return &t
}
