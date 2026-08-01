package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// CookieName is the canonical session cookie. HttpOnly + SameSite=Lax + Path=/,
// Secure flag conditional on TELA_PUBLIC_BASE_URL being https://.
const CookieName = "tela_session"

// SessionMaxAge is the rolling lifetime extended on every authenticated
// request and used as the cookie's MaxAge.
const SessionMaxAge = 30 * 24 * time.Hour

const sessionIDBytes = 32

// User is the subset of the users row that authenticated handlers need.
// Email is "" for legacy username-only rows (and the bootstrap admin until
// TELA_ADMIN_EMAIL is set).
type User struct {
	ID              int64
	Username        string
	Email           string
	IsInstanceAdmin bool
}

type contextKey int

const userCtxKey contextKey = 1

// WithUser returns a context that carries u. Used by Middleware after a
// session has been validated.
func WithUser(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, userCtxKey, u)
}

// UserFromContext pulls the authenticated user out of ctx. The second return
// is false when called from a handler that did not run behind Middleware.
func UserFromContext(ctx context.Context) (*User, bool) {
	u, ok := ctx.Value(userCtxKey).(*User)
	return u, ok
}

// ErrInvalidSession signals that the session id was missing, expired, or
// pointed at an inactive/deleted user. Callers should treat this as 401.
var ErrInvalidSession = errors.New("auth: invalid session")

// LoadSessionAndSlide validates sessionID and best-effort slides expires_at
// forward by SessionMaxAge + stamps last_seen_at. Returns ErrInvalidSession
// for missing/expired/inactive; any other error is a real DB problem the
// caller should propagate as 500.
//
// Implementation note: the validate + slide used to share a DEFERRED tx, but
// under SQLite WAL two parallel requests sharing one cookie both pass the
// SELECT in read mode, then both try to upgrade to writer on the UPDATE —
// the loser hits SQLITE_BUSY because busy_timeout doesn't apply to write
// promotion of a DEFERRED tx. Splitting into a single-statement SELECT plus
// a best-effort UPDATE serializes naturally via per-statement locking. The
// slide is an optimisation (rolling-window extension), not a correctness
// invariant — a failed slide just means the session expires earlier than it
// would have; the current request still completes.
func LoadSessionAndSlide(ctx context.Context, d *sql.DB, sessionID string) (*User, error) {
	var (
		userID   int64
		username string
		email    sql.NullString
		isAdmin  int
		boundOrg sql.NullInt64
	)
	err := d.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.email, u.is_instance_admin, s.org_id
		  FROM sessions s
		  JOIN users u ON u.id = s.user_id
		 WHERE s.id = $1
		   AND s.expires_at > tela_now()
		   AND u.is_active = 1`, sessionID).Scan(&userID, &username, &email, &isAdmin, &boundOrg)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidSession
	}
	if err != nil {
		return nil, err
	}

	// Session↔org binding: a session is valid only on the front door it was
	// created under. The host→org middleware put the request's org context on
	// ctx (absent ⇒ canonical host). A mismatch ⇒ this cookie is being presented
	// on the wrong host; treat it as invalid so login happens per front door.
	if !sessionOrgMatches(boundOrg, ctx) {
		return nil, ErrInvalidSession
	}

	newExpires := time.Now().UTC().Add(SessionMaxAge).Format("2006-01-02 15:04:05")
	if _, err := d.ExecContext(ctx,
		`UPDATE sessions SET expires_at = $1, last_seen_at = tela_now() WHERE id = $2`,
		newExpires, sessionID); err != nil {
		slog.Error("auth: session slide failed", "err", err)
	}

	return &User{ID: userID, Username: username, Email: email.String, IsInstanceAdmin: isAdmin == 1}, nil
}

// CreateSession inserts a fresh session row for userID. The id is base64url
// of 32 random bytes — collision-resistant and URL-safe for the cookie value.
// The session is bound to the org front door it was created under (read from
// ctx's OrgContext; NULL on the canonical host) so it can't later be replayed
// on a different host — see LoadSessionAndSlide.
func CreateSession(ctx context.Context, d *sql.DB, userID int64, userAgent string) (string, error) {
	buf := make([]byte, sessionIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(buf)
	expires := time.Now().UTC().Add(SessionMaxAge).Format("2006-01-02 15:04:05")
	var orgID sql.NullInt64
	if oc, ok := OrgContextFromContext(ctx); ok {
		orgID = sql.NullInt64{Int64: oc.OrgID, Valid: true}
	}
	if _, err := d.ExecContext(ctx, `
		INSERT INTO sessions(id, user_id, expires_at, last_seen_at, user_agent, org_id)
		VALUES ($1, $2, $3, tela_now(), $4, $5)`,
		id, userID, expires, userAgent, orgID); err != nil {
		return "", err
	}
	return id, nil
}

// sessionOrgMatches reports whether a session bound to boundOrg may be used on
// the front door described by ctx's OrgContext. The binding is exact: a
// canonical-host session (boundOrg NULL) is valid only where there is no org
// context, and an org-bound session only on that same org's host.
func sessionOrgMatches(boundOrg sql.NullInt64, ctx context.Context) bool {
	oc, ok := OrgContextFromContext(ctx)
	if !ok {
		return !boundOrg.Valid
	}
	return boundOrg.Valid && boundOrg.Int64 == oc.OrgID
}

// DeleteSession removes a session row by id. No error if the row is missing.
func DeleteSession(ctx context.Context, d *sql.DB, sessionID string) error {
	_, err := d.ExecContext(ctx, `DELETE FROM sessions WHERE id = $1`, sessionID)
	return err
}

// sessionExec is the subset of *sql.DB / *sql.Tx the session helpers use, so
// the same call works whether the caller already has an open tx or not.
type sessionExec interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// DeleteUserSessions removes every session for userID. Used by admin
// password reset + admin deactivate so the affected user is logged out of
// every device immediately.
func DeleteUserSessions(ctx context.Context, d sessionExec, userID int64) error {
	_, err := d.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
	return err
}

// DeleteUserSessionsExcept removes every session for userID except exceptID.
// Used by self-service password change + "logout everywhere" so the caller's
// own session survives.
func DeleteUserSessionsExcept(ctx context.Context, d sessionExec, userID int64, exceptID string) error {
	_, err := d.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id = $1 AND id != $2`, userID, exceptID)
	return err
}

// CookieSecure reports whether the session cookie should carry the Secure
// flag. True only when TELA_PUBLIC_BASE_URL has an https:// scheme — local
// dev (http://localhost:8780) keeps the cookie usable.
func CookieSecure() bool {
	return strings.HasPrefix(strings.ToLower(os.Getenv("TELA_PUBLIC_BASE_URL")), "https://")
}

// SetSessionCookie writes the canonical session cookie. Pass id="" to clear.
func SetSessionCookie(w http.ResponseWriter, id string) {
	c := &http.Cookie{
		Name:     CookieName,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   CookieSecure(),
	}
	if id == "" {
		c.MaxAge = -1
	} else {
		c.MaxAge = int(SessionMaxAge.Seconds())
	}
	http.SetCookie(w, c)
}

// IsPublicPath returns true for routes that bypass Middleware. Per the M6.1
// brief: /api/health and everything under /api/auth/. Login is anonymous
// by definition; logout + me handle cookie presence themselves. M11.0 adds
// /p/* — the public OG-share route, gated by User-Agent allowlist instead of
// session cookie because crawlers don't carry sessions. M15.0 adds
// /api/share/* — token-scoped public read API; the share handlers gate access
// themselves via the token + an optional HMAC password cookie. M15.5 adds
// /share/* — bot-UA-gated OG envelope for share links; crawlers don't carry
// sessions, so the session middleware would otherwise 401 before the handler
// could decide whether to serve OG HTML or a 404. M13.2 adds /api/diagrams/* —
// public PNG sidecar served content-addressed by scene_hash for read-only
// Excalidraw rendering (zero runtime cost for viewers). Mirrors the
// /p/{id}/og.png precedent: page-derived image, opaque hash, identical
// Cache-Control posture.
func IsPublicPath(p string) bool {
	if p == "/api/health" {
		return true
	}
	// M16.A.1.5 build-metadata probe. Public so the MCP server (M16.B.1) can
	// compat-check the backend on startup with no credentials. Prefix not
	// equality to match the convention of other public-prefix branches —
	// future sub-routes under /api/version/ would auto-bypass.
	if strings.HasPrefix(p, "/api/version") {
		return true
	}
	if strings.HasPrefix(p, "/api/auth/") {
		return true
	}
	// One-click digest unsubscribe (api/digest.go). Self-authenticates via an
	// HMAC token in the link so it works from an email with no session. Only this
	// exact route is public — the digest preview/pref routes require a session.
	if strings.HasPrefix(p, "/api/digest/unsubscribe") {
		return true
	}
	// Invitation lookup (api/invites.go — org AND space invites share one link
	// shape). GET /api/invites/{token} renders the accept page for a possibly-
	// logged-out invitee, self-authenticating via the unguessable token.
	// Accepting (POST /api/me/accept-invite) is NOT here — it requires a
	// session. No write route lives under /api/invites/.
	if strings.HasPrefix(p, "/api/invites/") {
		return true
	}
	// First-run setup wizard (api/setup.go). Public because a fresh instance has
	// no users (so no session can exist yet). GET /api/setup/status reports
	// emptiness; POST /api/setup creates the first admin and self-gates by only
	// succeeding while the users table is empty (atomic insert-if-empty), so a
	// missing session here is expected, not a hole.
	if p == "/api/setup" || strings.HasPrefix(p, "/api/setup/") {
		return true
	}
	// MCP Streamable-HTTP transport. Bypasses Middleware because it
	// self-authenticates via the SDK's bearer verifier (mcp.go) over the same
	// tela PATs — a single transport endpoint serves both read and write tools
	// over POST, so the method-level scope gate here can't apply; per-tool scope
	// is enforced inside the tool handlers. The verifier rejects missing/invalid
	// tokens with 401 + WWW-Authenticate (RFC 9728), so this is not an open hole.
	if strings.HasPrefix(p, "/api/mcp") {
		return true
	}
	// Caddy on-demand-TLS ask endpoint (/api/internal/tls-check). Called by the
	// proxy over the internal docker network with no session — must bypass
	// Middleware. Discloses only "is this host active" (200/404); the public
	// site blocks 404 the /api/internal/ prefix from the WAN.
	if strings.HasPrefix(p, "/api/internal/") {
		return true
	}
	// Host context (org branding + sign-in methods for the request host). The
	// SPA fetches it pre-login to brand the login screen, so it must be public.
	if p == "/api/host-context" {
		return true
	}
	// Polar billing webhook (api/billing.go). Polar → us with no session; the
	// handler self-authenticates by verifying the Standard Webhooks signature
	// against the configured secret. Only the webhook is public — checkout/portal
	// stay session-authed. Exact match, not a prefix, so nothing else under
	// /api/billing/ is exposed by widening this later.
	if p == "/api/billing/webhook" {
		return true
	}
	// Cloud control plane — managed services (RAG embed proxy, entitlements)
	// that a connected self-hoster's instance calls over HTTP. Bypasses
	// Middleware because the cloud handlers self-authenticate the bearer PAT via
	// the same auth.LookupAPIKey path the rest of the system uses, then gate on
	// the account's plan entitlement (api/cloud.go). The main instance hosts
	// these and consumes the underlying code in-process.
	if strings.HasPrefix(p, "/api/cloud/") {
		return true
	}
	// WebDAV file-sync (sync spec §7–9). Self-authenticates via PAT-as-Basic and
	// scope-gates per verb inside the handler — stock WebDAV clients send Basic,
	// not Bearer, so the generic Middleware can't authenticate them. The bare
	// /dav (no trailing slash) is included so ServeMux can issue its redirect to
	// /dav/ without the Middleware 401'ing first.
	if p == "/dav" || strings.HasPrefix(p, "/dav/") {
		return true
	}
	// OAuth Protected Resource Metadata (RFC 9728) for the MCP endpoint — public
	// discovery doc, served as static JSON by api.ServePRM (which self-gates on
	// whether OAuth is configured). Clients fetch it unauthenticated to bootstrap
	// the Connect flow.
	if strings.HasPrefix(p, "/.well-known/oauth-protected-resource") {
		return true
	}
	// WorkOS Standalone login bridge (Phase 5b) — self-authenticates (reads the
	// session cookie itself, redirects to /login when absent), so it must bypass
	// the middleware's blanket 401.
	if strings.HasPrefix(p, "/oauth/workos/") {
		return true
	}
	if strings.HasPrefix(p, "/api/share/") {
		return true
	}
	// Public-space read API — a space with visibility='public' is readable with
	// no login. Every /api/public/ handler self-authenticates by requiring the
	// space be public and is strictly GET/read-only. See api/public_spaces.go.
	if strings.HasPrefix(p, "/api/public/") {
		return true
	}
	if strings.HasPrefix(p, "/api/diagrams/") {
		return true
	}
	// Deck render output — content-addressed slide images/PDF from the deck
	// sidecar (opaque renderId, immutable cache), same posture as /api/diagrams/.
	if strings.HasPrefix(p, "/api/deck/") {
		return true
	}
	// Image-upload public serve — content-addressed BLOBs, same posture as
	// /api/diagrams/ (opaque hash URL, immutable cache). The serve handler
	// validates the hash + page id; bytes are public assets.
	if strings.HasPrefix(p, "/api/images/") {
		return true
	}
	// Unified attachment blob serve — content-addressed space_files (migration
	// 0015), same posture as /api/images/ (opaque hash URL, immutable cache). The
	// serve handler validates the hash, forces download for non-image types, and
	// serves bytes as public assets.
	if strings.HasPrefix(p, "/api/files/") {
		return true
	}
	// #3 PDF export: gotenberg's headless Chromium fetches /api/print/{token}
	// (page id encoded in the signed token) with no session. The handler
	// validates the short-lived HMAC token itself.
	if strings.HasPrefix(p, "/api/print/") {
		return true
	}
	// Signed-PUT attachment upload (attachment_uploads.go): PUT /api/uploads/{token}.
	// The short-lived HMAC token (page id + name + max size, minted for an editor)
	// IS the authorization, so the agent's host can upload bytes out-of-band with
	// no session. The handler validates the token + single-use itself.
	if strings.HasPrefix(p, "/api/uploads/") {
		return true
	}
	if strings.HasPrefix(p, "/share/") {
		return true
	}
	// Crawler-facing SEO/social surfaces for public spaces. Caddy bot-gates
	// /public/* and /u/* — only crawler UAs reach the backend here (humans get
	// the SPA), and these handlers emit OG HTML + JSON-LD self-authenticating on
	// the space/user being public (api/public_og.go). Read-only.
	if strings.HasPrefix(p, "/public/") || strings.HasPrefix(p, "/u/") {
		return true
	}
	// OG card routes (og_feature.go / og_space.go): crawler card + og.png for app
	// routes that have a public/shareable purpose but no page behind them. Caddy
	// bot-gates the HTML; the og.png is fetched by arbitrary-UA link-preview bots.
	// All self-contained (name/copy only, no private contents).
	switch p {
	case "/ask", "/ask/og.png", "/graph", "/graph/og.png", "/discover", "/discover/og.png",
		"/atlas", "/atlas/og.png",
		// Root card for an org's white-label apex (og_root.go). Only the
		// custom-domain Caddy block routes "/" + "/og.png" to the backend; on the
		// canonical host the landing owns "/", so this never overrides it.
		"/", "/og.png":
		return true
	}
	// Space overview card: /spaces/{id} and /spaces/{id}/og.png (id numeric). The
	// app SPA's /spaces/* never reaches the backend (Caddy serves it from the FE);
	// only these crawler routes do, and they render the space name only.
	if isSpaceOGPath(p) {
		return true
	}
	return strings.HasPrefix(p, "/p/")
}

// isSpaceOGPath matches the space overview card routes — /spaces/{id} and
// /spaces/{id}/og.png with a numeric id — and nothing deeper (a page deep link
// /spaces/{id}/pages/{id} has a non-digit segment, so it never matches; those are
// rewritten to /p by Caddy anyway).
func isSpaceOGPath(p string) bool {
	p = strings.TrimSuffix(p, "/og.png")
	rest, ok := strings.CutPrefix(p, "/spaces/")
	if !ok || rest == "" {
		return false
	}
	for _, c := range rest {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Middleware enforces authentication on every request except IsPublicPath.
// Two credential paths:
//
//  1. Authorization: Bearer tela_pat_xxx — M16.A.1 API keys. Checked FIRST,
//     before the session cookie, so a request that explicitly carried a
//     bearer header cannot fall back to a session on failure (explicit
//     failure beats accidental session escalation). On success attaches both
//     the *User and *APIKey to context; per-method scope gating fires here,
//     per-route admin/space gating fires inside individual handlers via
//     APIKeyFromContext.
//
//  2. Cookie tela_session — pre-existing session-cookie path. Unchanged for
//     requests that don't carry an Authorization header.
//
// Bearer mode honours scope at the method level (read → GET/HEAD only) and
// rejects unrecognised scopes (defence-in-depth — the CHECK constraint on
// api_keys.scope already enforces the same set). Space restriction lives in
// the API-key handler layer because the relevant space_id is route-shaped.
//
// aw is the M16.A.2 audit-log sink. nil-safe so tests that don't care about
// audit can keep their existing two-arg signature semantics (just pass nil).
// When non-nil, every bearer-authed request — including scope-blocked 403s,
// downstream-handler 4xx/5xx, and 200 success — submits a single
// fire-and-forget event after the response is written. Invalid bearer tokens
// (no resolved api_key_id) emit nothing: there's no FK target.
func Middleware(d *sql.DB, aw *AuditWriter) func(http.Handler) http.Handler {
	secret := LoadAPIKeySecret()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if IsPublicPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			if tok := extractBearerToken(r); tok != "" {
				k, err := LookupAPIKey(r.Context(), d, secret, tok)
				if errors.Is(err, ErrInvalidAPIKey) {
					// No FK target — nothing to audit.
					writeUnauthorized(w)
					return
				}
				if err != nil {
					slog.Error("auth: api key lookup failed", "err", err)
					writeInternal(w)
					return
				}
				// Wrap the writer so we can capture the final status code
				// the downstream handler (or this middleware) sends, then
				// emit a single audit event in defer regardless of how the
				// response was produced.
				sw := newAuditResponseWriter(w)
				defer func() {
					aw.Submit(AuditEvent{
						APIKeyID:   k.ID,
						Method:     r.Method,
						Path:       r.URL.Path,
						StatusCode: sw.statusCode(),
					})
				}()
				if !scopeAllowsRequest(k.Scope, r.Method, r.URL.Path) {
					writeForbidden(sw, "api_key_scope", "api key scope does not permit this method")
					return
				}
				u, err := userForAPIKey(r.Context(), d, k.UserID)
				if errors.Is(err, ErrInvalidAPIKey) {
					writeUnauthorized(sw)
					return
				}
				if err != nil {
					slog.Error("auth: api key user lookup failed", "err", err)
					writeInternal(sw)
					return
				}
				ctx := WithUser(r.Context(), u)
				ctx = WithAPIKey(ctx, k)
				next.ServeHTTP(sw, r.WithContext(ctx))
				return
			}
			c, err := r.Cookie(CookieName)
			if err != nil || c.Value == "" {
				writeUnauthorized(w)
				return
			}
			u, err := LoadSessionAndSlide(r.Context(), d, c.Value)
			if errors.Is(err, ErrInvalidSession) {
				writeUnauthorized(w)
				return
			}
			if err != nil {
				slog.Error("auth: session lookup failed", "err", err)
				writeInternal(w)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), u)))
		})
	}
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthorized","code":"unauthorized"}`))
}

// writeForbidden emits the canonical 403 envelope. Used by the bearer middleware
// to report scope-gating refusals (api_key_scope) — the body matches the {error,
// code} shape every other handler emits via writeError.
func writeForbidden(w http.ResponseWriter, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	// Escape the message minimally — no untrusted input flows here, callers
	// pass static strings.
	_, _ = w.Write([]byte(`{"error":"` + message + `","code":"` + code + `"}`))
}

func writeInternal(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write([]byte(`{"error":"internal error","code":"internal"}`))
}

var (
	dummyHashOnce sync.Once
	dummyHashStr  string
)

// DummyVerifyHash returns a precomputed argon2id hash used to keep login
// response time constant when the queried username does not exist. Computed
// lazily once per process so test runs aren't penalised on import.
func DummyVerifyHash() string {
	dummyHashOnce.Do(func() {
		h, err := HashPassword("tela-bogus-dummy-not-a-real-password")
		if err != nil {
			// HashPassword only fails when crypto/rand fails — fall back to
			// a static well-formed encoding so the timing path still runs.
			h = argonPrefix + "AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		}
		dummyHashStr = h
	})
	return dummyHashStr
}
