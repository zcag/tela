package api

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zcag/tela/backend/internal/auth"
	"github.com/zcag/tela/backend/internal/models"
	"github.com/zcag/tela/backend/internal/settings"
)

// M15.0 PublicShare. Editors create per-page share links that can be opened
// without a Tela session. Optional argon2id password protects the share;
// a successful POST /api/share/{token}/auth sets an HttpOnly path-scoped
// cookie whose value is HMAC(token + page_id + password_hash, server_secret)
// — no per-share session table needed.
//
// File scope: /api/share/* (public, token-gated) + /api/pages/{id}/shares
// + /api/shares/{id} (session-authed editor+ management). The bot-UA OG
// gate on /share/{token} and the FE share-mode UI ship in later M15 slices.

const (
	shareTokenBytes   = 8                     // → 11-char base64.RawURLEncoding token (64-bit)
	shareSecretBytes  = 32                    // HMAC-SHA256 key length
	shareTokenRetries = 3                     // INSERT retries on UNIQUE collision
	shareRateLimit    = 5                     // password attempts per (token, IP)
	shareRateWindow   = time.Minute           // rolling window for the limiter
	shareSubtreeDepth = 50                    // recursive-CTE depth cap
	tsLayout          = "2006-01-02 15:04:05" // canonical 'YYYY-MM-DD HH:MM:SS' wire format
)

// shareLink mirrors a row in share_links. PasswordHash / ExpiresAt / RevokedAt
// are SQL-nullable. The handlers convert this to a DTO before responding.
type shareLink struct {
	ID                 int64
	Token              string
	PageID             int64
	IncludeDescendants bool
	PasswordHash       sql.NullString
	CreatedBy          int64
	CreatedAt          string
	ExpiresAt          sql.NullString
	RevokedAt          sql.NullString
}

// shareLinkDTO is the management envelope: includes id, created_by, created_at,
// revoked_at, and the absolute URL. Never carries password_hash — has_password
// only.
type shareLinkDTO struct {
	ID                 int64   `json:"id"`
	Token              string  `json:"token"`
	PageID             int64   `json:"page_id"`
	IncludeDescendants bool    `json:"include_descendants"`
	HasPassword        bool    `json:"has_password"`
	CreatedBy          int64   `json:"created_by"`
	CreatedAt          string  `json:"created_at"`
	ExpiresAt          *string `json:"expires_at"`
	RevokedAt          *string `json:"revoked_at"`
	URL                string  `json:"url"`
}

// shareLinkPublicDTO is the slimmed envelope returned on /api/share/{token}.
// Hides id, created_by, created_at, revoked_at to minimise leakage to
// unauthenticated viewers.
type shareLinkPublicDTO struct {
	Token              string  `json:"token"`
	IncludeDescendants bool    `json:"include_descendants"`
	HasPassword        bool    `json:"has_password"`
	ExpiresAt          *string `json:"expires_at"`
	// #3 — canonical public base (e.g. https://telawiki.com). The share
	// reader shows it in the cover meta; it must come from the server, not
	// window.location, because the PDF export renders the reader from an
	// internal origin (where window.location.host would read "proxy").
	SourceURL string `json:"source_url"`
}

// sharePageDTO is the minimal page payload returned through public endpoints.
// Intentionally excludes comments / backlinks / history pointers — none of
// that should leak through a share link.
type sharePageDTO struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	UpdatedAt string `json:"updated_at"`
}

// sharePageNode is the sidebar shape returned by /api/share/{token}/tree —
// just enough for the FE to render an in-scope tree.
type sharePageNode struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	ParentID *int64 `json:"parent_id"`
	Position int64  `json:"position"`
}

type shareCreateRequest struct {
	IncludeDescendants bool    `json:"include_descendants"`
	Password           *string `json:"password,omitempty"`
	ExpiresAt          *string `json:"expires_at,omitempty"`
}

type shareAuthRequest struct {
	Password string `json:"password"`
}

// resolveShareSecret reads TELA_SHARE_SECRET, else returns a stable secret
// persisted in instance_settings (generated once on first boot). Precedence:
// env override → persisted store value → generated-and-persisted. This replaces
// the old random-per-process fallback that invalidated outstanding share
// cookies on every restart; the persisted secret now survives restarts.
const shareSecretPlaceholder = "replace-me-with-openssl-rand-hex-32"

func resolveShareSecret(ctx context.Context, st *settings.Store) []byte {
	if v := os.Getenv("TELA_SHARE_SECRET"); v != "" {
		if v == shareSecretPlaceholder {
			slog.Warn("SECURITY: TELA_SHARE_SECRET is set to the public placeholder from .env.example — " +
				"share cookies are FORGEABLE by anyone who has read the repo. " +
				"Run: openssl rand -hex 32")
		}
		return []byte(v)
	}
	b, err := st.GetOrInitSecret(ctx, "share", shareSecretBytes)
	if err != nil {
		// Boot-fatal: share cookies can't be signed without the secret.
		slog.Error("share: resolve persisted secret", "err", err)
		os.Exit(1)
	}
	return b
}

// newShareToken returns an 11-char URL-safe random token. crypto/rand is the
// only source of entropy; 8 bytes (64 bits) keeps the URL short while staying
// far out of brute-force/enumeration range, and the caller retries on the rare
// UNIQUE collision.
func newShareToken() (string, error) {
	buf := make([]byte, shareTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// shareCookieName returns the per-share cookie name. The path-scoped cookie
// lives at /api/share/{token} so revoking one share never logs the user out
// of another.
func shareCookieName(token string) string { return "tela_share_" + token }

// shareCookieValue produces the HMAC-SHA256 the FE will round-trip back as the
// password cookie. The hash includes the password hash so changing the
// password invalidates outstanding cookies for free.
func shareCookieValue(secret []byte, token string, pageID int64, passwordHash string) string {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(token))
	h.Write([]byte{0})
	h.Write([]byte(strconv.FormatInt(pageID, 10)))
	h.Write([]byte{0})
	h.Write([]byte(passwordHash))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// validateShareCookie returns true when the request carries a cookie whose
// HMAC matches what the server would have produced for this share. Constant-
// time compare to avoid leaking byte-by-byte agreement. Shares without a
// password are always "valid" (no proof required).
func validateShareCookie(r *http.Request, secret []byte, share *shareLink) bool {
	if !share.PasswordHash.Valid {
		return true
	}
	c, err := r.Cookie(shareCookieName(share.Token))
	if err != nil || c.Value == "" {
		return false
	}
	want := shareCookieValue(secret, share.Token, share.PageID, share.PasswordHash.String)
	got := c.Value
	if len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// shareRateLimiter is the in-memory bucket used to throttle password attempts
// on /api/share/{token}/auth. Keyed by (token, IP) so one bad actor can't
// lock out every other viewer of the same share. Process-local; restart
// resets — acceptable for v0 and avoids a persistent dependency.
type shareRateLimiter struct {
	mu      sync.Mutex
	buckets map[string][]time.Time
}

func newShareRateLimiter() *shareRateLimiter {
	return &shareRateLimiter{buckets: map[string][]time.Time{}}
}

// sweep removes empty buckets and prunes stale times from non-empty ones.
// Bounds buckets memory under adversarial load — without it a stream of
// distinct (token, IP) keys grows the map monotonically.
func (rl *shareRateLimiter) sweep() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	cutoff := time.Now().Add(-shareRateWindow)
	for k, times := range rl.buckets {
		kept := times[:0]
		for _, t := range times {
			if t.After(cutoff) {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			delete(rl.buckets, k)
		} else {
			rl.buckets[k] = kept
		}
	}
}

// sweepLoop runs sweep once per shareRateWindow until ctx is cancelled. Started
// from api.New for the production server lifetime; tests inherit the goroutine
// but each test process is short-lived so the leak is benign.
func (rl *shareRateLimiter) sweepLoop(ctx context.Context) {
	t := time.NewTicker(shareRateWindow)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rl.sweep()
		}
	}
}

// allow returns (ok, retryAfter). When ok=false the second value is how long
// the caller should wait before retrying — the time until the oldest in-bucket
// attempt rolls out of the window.
func (rl *shareRateLimiter) allow(token, ip string) (bool, time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	key := token + "\x00" + ip
	now := time.Now()
	cutoff := now.Add(-shareRateWindow)

	times := rl.buckets[key]
	pruned := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			pruned = append(pruned, t)
		}
	}
	if len(pruned) >= shareRateLimit {
		retry := pruned[0].Add(shareRateWindow).Sub(now)
		if retry < 0 {
			retry = 0
		}
		rl.buckets[key] = pruned
		return false, retry
	}
	pruned = append(pruned, now)
	rl.buckets[key] = pruned
	return true, 0
}

// normalizeIPForBucket folds an IP-or-IP:port string into a canonical bucket
// key. Strips port (IPv4 1.2.3.4:5; IPv6 [::1]:5), strips IPv6 brackets, drops
// the zone suffix (fe80::1%eth0). Falls back to the raw trimmed string when
// net.ParseIP cannot make sense of the input — this is rate-limit metadata,
// not auth, so a best-effort key is preferable to refusing the request.
func normalizeIPForBucket(raw string) string {
	s := strings.TrimSpace(raw)
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	s = strings.Trim(s, "[]")
	if i := strings.Index(s, "%"); i >= 0 {
		s = s[:i]
	}
	if ip := net.ParseIP(s); ip != nil {
		return ip.String()
	}
	return s
}

// clientIPForRateLimit returns the client IP to key the rate-limit bucket on.
// Reads the RIGHTMOST X-Forwarded-For entry — Caddy's trusted_proxies block
// strips any client-supplied XFF and appends a single trustworthy hop, so the
// rightmost is what Caddy itself authored. Reading the leftmost (the previous
// behaviour) made the limiter trivial to defeat: an attacker could rotate the
// header per request and mint a fresh 5-attempt bucket every time.
func clientIPForRateLimit(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.LastIndexByte(xff, ','); i >= 0 {
			return normalizeIPForBucket(xff[i+1:])
		}
		return normalizeIPForBucket(xff)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return normalizeIPForBucket(host)
	}
	return normalizeIPForBucket(r.RemoteAddr)
}

// shareURLFor produces the absolute https URL the FE serves for a share token,
// with the page's cosmetic slug appended (/share/{token}/{slug}) when present.
// origin is the share's effective origin (the owning org's custom domain when
// it has one, else canonical — see Server.shareOrigin); a localhost fallback
// keeps dev share-creation URLs complete.
func shareURLFor(origin, token, slug string) string {
	url := cmp.Or(origin, devBaseURL) + "/share/" + token
	if slug != "" {
		url += "/" + slug
	}
	return url
}

// nullableString returns *string for a sql.NullString — JSON null when absent,
// a value when present. Matches the pattern used in comments/page-revisions.
func nullableString(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	v := ns.String
	return &v
}

// shareLinkToDTO assembles the management envelope. Never includes the
// password_hash; has_password reports the hash's presence. pageTitle feeds the
// cosmetic slug in the share URL — pass the live page title.
// shareLinkToDTO builds the wire shape. origin is the share's effective origin
// (the owning org's custom domain when it has one, else canonical — see
// Server.shareOrigin) so the copy-link URL is branded with the org's domain.
func shareLinkToDTO(s *shareLink, pageTitle, origin string) shareLinkDTO {
	return shareLinkDTO{
		ID:                 s.ID,
		Token:              s.Token,
		PageID:             s.PageID,
		IncludeDescendants: s.IncludeDescendants,
		HasPassword:        s.PasswordHash.Valid,
		CreatedBy:          s.CreatedBy,
		CreatedAt:          s.CreatedAt,
		ExpiresAt:          nullableString(s.ExpiresAt),
		RevokedAt:          nullableString(s.RevokedAt),
		URL:                shareURLFor(origin, s.Token, pageSlug(pageTitle)),
	}
}

const shareLinkSelectColumns = `
	SELECT id, token, page_id, include_descendants, password_hash,
	       created_by, created_at, expires_at, revoked_at
	  FROM share_links`

func scanShareLink(r rowScanner) (shareLink, error) {
	var s shareLink
	var includeDesc int
	if err := r.Scan(&s.ID, &s.Token, &s.PageID, &includeDesc, &s.PasswordHash,
		&s.CreatedBy, &s.CreatedAt, &s.ExpiresAt, &s.RevokedAt); err != nil {
		return s, err
	}
	s.IncludeDescendants = includeDesc != 0
	return s, nil
}

func selectShareLinkByIDTx(ctx context.Context, tx *sql.Tx, id int64) (shareLink, error) {
	row := tx.QueryRowContext(ctx, shareLinkSelectColumns+` WHERE id = $1`, id)
	return scanShareLink(row)
}

// selectShareLinkByToken is the public resolution of a share token (every
// /share and /api/share handler goes through it). A share of a DRAFT page
// resolves as not found, the same as a revoked one, until the page is
// published: drafts are never served on a public surface.
func selectShareLinkByToken(ctx context.Context, q *sql.DB, token string) (shareLink, error) {
	row := q.QueryRowContext(ctx, shareLinkSelectColumns+` WHERE token = $1
		AND NOT EXISTS (SELECT 1 FROM pages dp WHERE dp.id = share_links.page_id AND dp.status = 'draft')`, token)
	return scanShareLink(row)
}

// shareAuditItem is one row of the cross-space audit view (GET /api/shares):
// the management envelope plus the page/space context a list needs to render.
type shareAuditItem struct {
	shareLinkDTO
	SpaceID   int64  `json:"space_id"`
	SpaceName string `json:"space_name"`
	PageTitle string `json:"page_title"`
}

// ListAllShares — GET /api/shares. Every active (non-revoked) share link across
// the spaces the caller is a member of, with page + space context. Powers the
// "Shared" audit view: one place to see everything currently reachable by link
// instead of opening each page's manager. A bearer key restricted to one space
// sees only that space's shares (same ceiling as ListAllPages).
func (s *Server) ListAllShares(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	query := `
		SELECT sl.id, sl.token, sl.page_id, sl.include_descendants, sl.password_hash,
		       sl.created_by, sl.created_at, sl.expires_at, sl.revoked_at,
		       p.space_id, sp.name, p.title
		  FROM share_links sl
		  JOIN pages p ON p.id = sl.page_id AND p.deleted_at IS NULL
		  JOIN spaces sp ON sp.id = p.space_id
		  JOIN (SELECT DISTINCT space_id FROM space_access WHERE user_id = $1) sm ON sm.space_id = p.space_id
		 WHERE sl.revoked_at IS NULL`
	args := []any{u.ID}
	if k, isBearer := auth.APIKeyFromContext(r.Context()); isBearer && k.SpaceID != nil {
		query += ` AND p.space_id = $2`
		args = append(args, *k.SpaceID)
	}
	query += ` ORDER BY sp.name ASC, p.title ASC, sl.created_at ASC`

	rows, err := s.DB.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "list shares failed")
		return
	}
	defer rows.Close()

	items := []shareAuditItem{}
	for rows.Next() {
		var (
			sl          shareLink
			includeDesc int
			spaceID     int64
			spaceName   string
			pageTitle   string
		)
		if err := rows.Scan(&sl.ID, &sl.Token, &sl.PageID, &includeDesc, &sl.PasswordHash,
			&sl.CreatedBy, &sl.CreatedAt, &sl.ExpiresAt, &sl.RevokedAt,
			&spaceID, &spaceName, &pageTitle); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "scan share row failed")
			return
		}
		sl.IncludeDescendants = includeDesc != 0
		// Instance-admin audit overview: canonical origin (stable identity);
		// per-row org-domain derivation isn't worth an N+1 here.
		items = append(items, shareAuditItem{
			shareLinkDTO: shareLinkToDTO(&sl, pageTitle, canonicalBaseURL()),
			SpaceID:      spaceID,
			SpaceName:    spaceName,
			PageTitle:    pageTitle,
		})
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "iterate shares failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": items})
}

// shareLinkActive reports whether a share is currently usable — exists, not
// revoked, not expired. The three failure modes return identical responses to
// public callers, so the helper collapses them into one bool.
func shareLinkActive(s *shareLink) bool {
	if s.RevokedAt.Valid {
		return false
	}
	if s.ExpiresAt.Valid {
		t, err := time.Parse(tsLayout, s.ExpiresAt.String)
		if err == nil && !t.After(time.Now().UTC()) {
			return false
		}
	}
	return true
}

// parseFutureExpires normalises a client-supplied expires_at: must match the
// canonical 'YYYY-MM-DD HH:MM:SS' format AND be strictly in the future.
func parseFutureExpires(raw string) (string, error) {
	t, err := time.Parse(tsLayout, raw)
	if err != nil {
		return "", err
	}
	if !t.After(time.Now().UTC()) {
		return "", errors.New("expires_at must be in the future")
	}
	return t.UTC().Format(tsLayout), nil
}

// shareEditGate resolves a page for the share manager and enforces the
// subsystem's enumeration-resistant editor+ gate: a missing page OR a
// non-member caller both collapse to 404 not_found (no existence oracle), a
// space-pinned bearer key targeting another space → api_key_space_scope, and a
// viewer → 403 viewer_no_write. Shared by the create + list cores (both
// addressed by page id). Revoke runs its own gate (share-id addressed, so a
// viewer also collapses to 404 — see revokeShareLinkCore).
func (s *Server) shareEditGate(ctx context.Context, tx *sql.Tx, u *auth.User, k *auth.APIKey, pageID int64) (models.Page, *apiErr) {
	page, err := selectPageByIDTx(ctx, tx, pageID)
	if errors.Is(err, sql.ErrNoRows) {
		return models.Page{}, &apiErr{http.StatusNotFound, "not_found", "page not found"}
	}
	if err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "lookup page failed"}
	}
	if ae := apiKeySpaceScopeErr(k, page.SpaceID); ae != nil {
		return models.Page{}, ae
	}
	role, err := spaceRoleTx(ctx, tx, u.ID, page.SpaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return models.Page{}, &apiErr{http.StatusNotFound, "not_found", "page not found"}
	}
	if err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "lookup membership failed"}
	}
	if !canEdit(role) {
		return models.Page{}, &apiErr{http.StatusForbidden, "viewer_no_write", "editor or owner role required"}
	}
	return page, nil
}

// createShareLinkCore mints a share link for a page (editor+) and returns the
// management DTO with the absolute share URL. Transport-agnostic core behind
// POST /api/pages/{id}/shares and the MCP share_page tool.
func (s *Server) createShareLinkCore(ctx context.Context, u *auth.User, k *auth.APIKey, pageID int64, req shareCreateRequest) (shareLinkDTO, *apiErr) {
	var expiresArg any = nil
	// Empty string is treated as absent — keeps the FE's optional-input UX clean.
	if req.ExpiresAt != nil && *req.ExpiresAt != "" {
		normalised, err := parseFutureExpires(*req.ExpiresAt)
		if err != nil {
			return shareLinkDTO{}, &apiErr{http.StatusBadRequest, "bad_request", "expires_at must be a future YYYY-MM-DD HH:MM:SS"}
		}
		expiresArg = normalised
	}

	var passwordHash any = nil
	if req.Password != nil && *req.Password != "" {
		h, err := auth.HashPassword(*req.Password)
		if err != nil {
			return shareLinkDTO{}, &apiErr{http.StatusInternalServerError, "internal", "hash password failed"}
		}
		passwordHash = h
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return shareLinkDTO{}, &apiErr{http.StatusInternalServerError, "internal", "begin tx failed"}
	}
	defer tx.Rollback()

	page, ae := s.shareEditGate(ctx, tx, u, k, pageID)
	if ae != nil {
		return shareLinkDTO{}, ae
	}

	includeFlag := 0
	if req.IncludeDescendants {
		includeFlag = 1
	}
	var (
		insertedID int64
		token      string
	)
	for attempt := 0; attempt < shareTokenRetries; attempt++ {
		token, err = newShareToken()
		if err != nil {
			return shareLinkDTO{}, &apiErr{http.StatusInternalServerError, "internal", "generate token failed"}
		}
		ierr := tx.QueryRowContext(ctx, `
			INSERT INTO share_links
			  (token, page_id, include_descendants, password_hash,
			   created_by, created_at, expires_at)
			VALUES ($1, $2, $3, $4, $5, tela_now(), $6) RETURNING id`,
			token, pageID, includeFlag, passwordHash, u.ID, expiresArg).Scan(&insertedID)
		if ierr == nil {
			break
		}
		if !isUniqueConstraintErr(ierr) {
			return shareLinkDTO{}, &apiErr{http.StatusInternalServerError, "internal", "insert share failed"}
		}
		if attempt == shareTokenRetries-1 {
			return shareLinkDTO{}, &apiErr{http.StatusInternalServerError, "internal", "could not allocate unique share token"}
		}
	}

	created, err := selectShareLinkByIDTx(ctx, tx, insertedID)
	if err != nil {
		return shareLinkDTO{}, &apiErr{http.StatusInternalServerError, "internal", "fetch created share failed"}
	}
	if err := tx.Commit(); err != nil {
		return shareLinkDTO{}, &apiErr{http.StatusInternalServerError, "internal", "commit failed"}
	}
	return shareLinkToDTO(&created, page.Title, s.shareOrigin(ctx, page.SpaceID)), nil
}

// listShareLinksCore returns a page's share links (editor+). Active links only
// unless includeRevoked. Transport-agnostic core behind GET /api/pages/{id}/shares
// and the MCP list_shares tool.
func (s *Server) listShareLinksCore(ctx context.Context, u *auth.User, k *auth.APIKey, pageID int64, includeRevoked bool) ([]shareLinkDTO, *apiErr) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, &apiErr{http.StatusInternalServerError, "internal", "begin tx failed"}
	}
	defer tx.Rollback()

	page, ae := s.shareEditGate(ctx, tx, u, k, pageID)
	if ae != nil {
		return nil, ae
	}

	q := shareLinkSelectColumns + ` WHERE page_id = $1 AND revoked_at IS NULL ORDER BY id DESC`
	if includeRevoked {
		q = shareLinkSelectColumns + ` WHERE page_id = $1 ORDER BY id DESC`
	}
	rows, err := tx.QueryContext(ctx, q, pageID)
	if err != nil {
		return nil, &apiErr{http.StatusInternalServerError, "internal", "list shares failed"}
	}
	defer rows.Close()

	origin := s.shareOrigin(ctx, page.SpaceID)
	out := []shareLinkDTO{}
	for rows.Next() {
		sh, err := scanShareLink(rows)
		if err != nil {
			return nil, &apiErr{http.StatusInternalServerError, "internal", "scan share row failed"}
		}
		out = append(out, shareLinkToDTO(&sh, page.Title, origin))
	}
	if err := rows.Err(); err != nil {
		return nil, &apiErr{http.StatusInternalServerError, "internal", "iterate shares failed"}
	}
	return out, nil
}

// CreateShareLink — POST /api/pages/{id}/shares. Editor+ on the page's space.
// Returns the canonical management envelope, including the absolute share URL.
func (s *Server) CreateShareLink(w http.ResponseWriter, r *http.Request) {
	pageID, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	var req shareCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not parse request body")
		return
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	dto, ae := s.createShareLinkCore(r.Context(), u, k, pageID, req)
	if ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"share": dto})
}

// ListShareLinks — GET /api/pages/{id}/shares. Editor+ on the page's space.
// Default: only active shares (revoked_at IS NULL). ?include_revoked=true
// includes the audit-trail rows.
func (s *Server) ListShareLinks(w http.ResponseWriter, r *http.Request) {
	pageID, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	includeRevoked := r.URL.Query().Get("include_revoked") == "true"
	out, ae := s.listShareLinksCore(r.Context(), u, k, pageID, includeRevoked)
	if ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": out})
}

// PatchShareLink — PATCH /api/shares/{share_id}. Editor+ on the share's
// page's space. Mutable: include_descendants, password (string sets, null
// clears), expires_at. token/page_id/created_by are read-only and rejected
// with 400 if present in the body. Revoked shares cannot be patched (409).
func (s *Server) PatchShareLink(w http.ResponseWriter, r *http.Request) {
	shareID, ok := parseIDParam(w, r, "share_id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}

	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not parse request body")
		return
	}
	for _, ro := range []string{"token", "page_id", "created_by", "id", "created_at", "revoked_at"} {
		if _, ok := raw[ro]; ok {
			writeError(w, http.StatusBadRequest, "bad_request", ro+" is read-only")
			return
		}
	}
	if len(raw) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "no fields to update")
		return
	}

	var (
		setIncludeDescendants bool
		newIncludeDescendants bool

		setPassword     bool
		clearPassword   bool
		newPasswordHash string

		setExpiresAt   bool
		clearExpiresAt bool
		newExpiresAt   string
	)

	if v, ok := raw["include_descendants"]; ok {
		var b bool
		if err := json.Unmarshal(v, &b); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "include_descendants must be a boolean")
			return
		}
		setIncludeDescendants = true
		newIncludeDescendants = b
	}
	if v, ok := raw["password"]; ok {
		setPassword = true
		if string(v) == "null" {
			clearPassword = true
		} else {
			var pw string
			if err := json.Unmarshal(v, &pw); err != nil {
				writeError(w, http.StatusBadRequest, "bad_request", "password must be a string or null")
				return
			}
			if pw == "" {
				clearPassword = true
			} else {
				h, err := auth.HashPassword(pw)
				if err != nil {
					writeError(w, http.StatusInternalServerError, "internal", "hash password failed")
					return
				}
				newPasswordHash = h
			}
		}
	}
	if v, ok := raw["expires_at"]; ok {
		setExpiresAt = true
		if string(v) == "null" {
			clearExpiresAt = true
		} else {
			var raw string
			if err := json.Unmarshal(v, &raw); err != nil {
				writeError(w, http.StatusBadRequest, "bad_request", "expires_at must be a string or null")
				return
			}
			normalised, err := parseFutureExpires(raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, "bad_request", "expires_at must be a future YYYY-MM-DD HH:MM:SS")
				return
			}
			newExpiresAt = normalised
		}
	}

	ctx := r.Context()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "begin tx failed")
		return
	}
	defer tx.Rollback()

	share, err := selectShareLinkByIDTx(ctx, tx, shareID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "share not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "lookup share failed")
		return
	}
	page, err := selectPageByIDTx(ctx, tx, share.PageID)
	if err != nil {
		// share without a page should be impossible (FK cascade), so any
		// error here is a real DB problem.
		writeError(w, http.StatusInternalServerError, "internal", "lookup parent page failed")
		return
	}
	if !enforceAPIKeySpaceScope(w, r, page.SpaceID) {
		return
	}
	role, err := spaceRoleTx(ctx, tx, u.ID, page.SpaceID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "share not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "lookup membership failed")
		return
	}
	if !canEdit(role) {
		// Don't leak existence to viewers either — same 404 shape.
		writeError(w, http.StatusNotFound, "not_found", "share not found")
		return
	}
	if share.RevokedAt.Valid {
		writeError(w, http.StatusConflict, "conflict", "share is revoked")
		return
	}

	sets := []string{}
	args := []any{}
	if setIncludeDescendants {
		sets = append(sets, "include_descendants = $"+strconv.Itoa(len(args)+1))
		flag := 0
		if newIncludeDescendants {
			flag = 1
		}
		args = append(args, flag)
	}
	if setPassword {
		sets = append(sets, "password_hash = $"+strconv.Itoa(len(args)+1))
		if clearPassword {
			args = append(args, nil)
		} else {
			args = append(args, newPasswordHash)
		}
	}
	if setExpiresAt {
		sets = append(sets, "expires_at = $"+strconv.Itoa(len(args)+1))
		if clearExpiresAt {
			args = append(args, nil)
		} else {
			args = append(args, newExpiresAt)
		}
	}
	stmt := "UPDATE share_links SET " + strings.Join(sets, ", ") + " WHERE id = $" + strconv.Itoa(len(args)+1)
	args = append(args, shareID)
	if _, err := tx.ExecContext(ctx, stmt, args...); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "update share failed")
		return
	}
	updated, err := selectShareLinkByIDTx(ctx, tx, shareID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "fetch updated share failed")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "commit failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"share": shareLinkToDTO(&updated, page.Title, s.shareOrigin(ctx, page.SpaceID))})
}

// DeleteShareLink — DELETE /api/shares/{share_id}. Editor+. Soft-deletes by
// stamping revoked_at; a second DELETE on an already-revoked share is a no-op
// 204 (idempotent). Missing share OR viewer caller → 404 so neither can
// enumerate share state.
func (s *Server) DeleteShareLink(w http.ResponseWriter, r *http.Request) {
	shareID, ok := parseIDParam(w, r, "share_id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	if ae := s.revokeShareLinkCore(r.Context(), u, k, shareID); ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// revokeShareLinkCore soft-deletes a share by stamping revoked_at (editor+).
// Idempotent: revoking an already-revoked share is a no-op. The gate is
// share-id addressed, so missing share / non-member / viewer all collapse to
// 404 not_found — neither a non-editor nor an outsider can probe share state.
// Transport-agnostic core behind DELETE /api/shares/{share_id} and the MCP
// revoke_share tool.
func (s *Server) revokeShareLinkCore(ctx context.Context, u *auth.User, k *auth.APIKey, shareID int64) *apiErr {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "begin tx failed"}
	}
	defer tx.Rollback()

	share, err := selectShareLinkByIDTx(ctx, tx, shareID)
	if errors.Is(err, sql.ErrNoRows) {
		return &apiErr{http.StatusNotFound, "not_found", "share not found"}
	}
	if err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "lookup share failed"}
	}
	page, err := selectPageByIDTx(ctx, tx, share.PageID)
	if err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "lookup parent page failed"}
	}
	if ae := apiKeySpaceScopeErr(k, page.SpaceID); ae != nil {
		return ae
	}
	role, err := spaceRoleTx(ctx, tx, u.ID, page.SpaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return &apiErr{http.StatusNotFound, "not_found", "share not found"}
	}
	if err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "lookup membership failed"}
	}
	if !canEdit(role) {
		return &apiErr{http.StatusNotFound, "not_found", "share not found"}
	}
	if !share.RevokedAt.Valid {
		if _, err := tx.ExecContext(ctx,
			`UPDATE share_links SET revoked_at = tela_now() WHERE id = $1`, shareID); err != nil {
			return &apiErr{http.StatusInternalServerError, "internal", "revoke share failed"}
		}
	}
	if err := tx.Commit(); err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "commit failed"}
	}
	return nil
}

// publicShareLookup resolves the token from the path, returning the share or
// writing the canonical 404 envelope. Missing / revoked / expired all surface
// the SAME 404 so the response can't distinguish them — no oracle for token
// guessing or revocation status. The SELECT runs unconditionally before any
// status branching to deny timing-side-channel signal as well.
func (s *Server) publicShareLookup(w http.ResponseWriter, r *http.Request) (shareLink, bool) {
	token := r.PathValue("token")
	if token == "" {
		writeError(w, http.StatusNotFound, "not_found", "share not found")
		return shareLink{}, false
	}
	share, err := selectShareLinkByToken(r.Context(), s.DB, token)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "share not found")
		return shareLink{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "lookup share failed")
		return shareLink{}, false
	}
	if !shareLinkActive(&share) {
		writeError(w, http.StatusNotFound, "not_found", "share not found")
		return shareLink{}, false
	}
	return share, true
}

// requirePublicShareAuth gates access to the password-protected portion of a
// share. Returns true and lets the caller continue if no password is set or
// a valid cookie is present; writes 401 password_required and returns false
// otherwise.
func (s *Server) requirePublicShareAuth(w http.ResponseWriter, r *http.Request, share *shareLink) bool {
	if !share.PasswordHash.Valid {
		return true
	}
	if validateShareCookie(r, s.shareSecret, share) {
		return true
	}
	writeError(w, http.StatusUnauthorized, "password_required", "password required")
	return false
}

// GetPublicShare — GET /api/share/{token}. Returns the share metadata + the
// root page body. No session cookie needed; password cookie required when
// the share is password-gated.
func (s *Server) GetPublicShare(w http.ResponseWriter, r *http.Request) {
	share, ok := s.publicShareLookup(w, r)
	if !ok {
		return
	}
	if !s.requirePublicShareAuth(w, r, &share) {
		return
	}
	page, err := selectPageByID(r.Context(), s.DB, share.PageID)
	if err != nil {
		// Page deleted out from under the share — collapse to 404 to match
		// the rest of the public surface.
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "share not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "lookup page failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"share": shareLinkPublicDTO{
			Token:              share.Token,
			IncludeDescendants: share.IncludeDescendants,
			HasPassword:        share.PasswordHash.Valid,
			ExpiresAt:          nullableString(share.ExpiresAt),
			SourceURL:          canonicalBaseURL(),
		},
		"page": sharePageDTO{
			ID:        page.ID,
			Title:     page.Title,
			Body:      page.Body,
			UpdatedAt: page.UpdatedAt,
		},
	})
}

// PublicShareAuth — POST /api/share/{token}/auth. Validates the submitted
// password against the share's argon2id hash; on success, writes the path-
// scoped HMAC cookie the other public endpoints check. Rate-limited per
// (token, IP): 5 attempts / minute, then 429 with Retry-After.
//
// Pitfall watch: rate limiting runs BEFORE the share lookup so we don't leak
// a faster 404-vs-401 timing path for token enumeration. Wrong-password
// attempts on a valid share are counted, matching the brief.
func (s *Server) PublicShareAuth(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		writeError(w, http.StatusNotFound, "not_found", "share not found")
		return
	}
	var req shareAuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not parse request body")
		return
	}

	ip := clientIPForRateLimit(r)
	if ok, retry := s.shareLimiter.allow(token, ip); !ok {
		secs := int(retry.Seconds())
		if secs < 1 {
			secs = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		writeError(w, http.StatusTooManyRequests, "too_many_requests", "too many attempts")
		return
	}

	share, err := selectShareLinkByToken(r.Context(), s.DB, token)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "share not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "lookup share failed")
		return
	}
	if !shareLinkActive(&share) {
		writeError(w, http.StatusNotFound, "not_found", "share not found")
		return
	}
	if !share.PasswordHash.Valid {
		writeError(w, http.StatusBadRequest, "bad_request", "share does not require a password")
		return
	}

	ok, _ := auth.VerifyPassword(req.Password, share.PasswordHash.String)
	if !ok {
		writeError(w, http.StatusUnauthorized, "password_required", "wrong password")
		return
	}

	value := shareCookieValue(s.shareSecret, share.Token, share.PageID, share.PasswordHash.String)
	cookie := &http.Cookie{
		Name:     shareCookieName(share.Token),
		Value:    value,
		Path:     "/api/share/" + share.Token,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   auth.CookieSecure(),
	}
	http.SetCookie(w, cookie)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// GetPublicSharePage — GET /api/share/{token}/page/{page_id}. Returns the
// body of a descendant page when the share covers it. Pages outside the
// subtree — or any page when include_descendants=false except the root —
// return 404 to avoid leaking unrelated page existence.
func (s *Server) GetPublicSharePage(w http.ResponseWriter, r *http.Request) {
	share, ok := s.publicShareLookup(w, r)
	if !ok {
		return
	}
	if !s.requirePublicShareAuth(w, r, &share) {
		return
	}
	pageID, ok := parseIDParam(w, r, "page_id")
	if !ok {
		return
	}

	if pageID != share.PageID {
		if !share.IncludeDescendants {
			writeError(w, http.StatusNotFound, "not_found", "page not in share scope")
			return
		}
		inScope, err := pageInShareSubtree(r.Context(), s.DB, share.PageID, pageID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "scope check failed")
			return
		}
		if !inScope {
			writeError(w, http.StatusNotFound, "not_found", "page not in share scope")
			return
		}
	}
	page, err := selectPageByID(r.Context(), s.DB, pageID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "page not in share scope")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "lookup page failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"page": sharePageDTO{
			ID:        page.ID,
			Title:     page.Title,
			Body:      page.Body,
			UpdatedAt: page.UpdatedAt,
		},
	})
}

// GetPublicShareTree — GET /api/share/{token}/tree. Returns the in-scope page
// tree as a flat array (id, title, parent_id, position) so the FE can render
// a slim sidebar. When include_descendants=false the array is exactly one
// element: the root page.
func (s *Server) GetPublicShareTree(w http.ResponseWriter, r *http.Request) {
	share, ok := s.publicShareLookup(w, r)
	if !ok {
		return
	}
	if !s.requirePublicShareAuth(w, r, &share) {
		return
	}
	nodes, err := shareSubtree(r.Context(), s.DB, share.PageID, share.IncludeDescendants)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "build share tree failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pages": nodes})
}

// pageInShareSubtree reports whether descendantID is a transitive descendant
// of rootID (or rootID itself), capped at shareSubtreeDepth levels deep.
// The space_id constraint is defensive — the page tree never crosses spaces
// in v0 — but we lock it in here so a future bug can't widen a share.
func pageInShareSubtree(ctx context.Context, q *sql.DB, rootID, descendantID int64) (bool, error) {
	if rootID == descendantID {
		return true, nil
	}
	const query = `
		WITH RECURSIVE scope(id, space_id, depth) AS (
		  SELECT id, space_id, 0 FROM pages WHERE id = $1 AND deleted_at IS NULL
		  UNION ALL
		  SELECT p.id, p.space_id, s.depth + 1
		    FROM pages p
		    JOIN scope s ON p.parent_id = s.id
		   WHERE p.space_id = s.space_id AND p.deleted_at IS NULL AND p.status = 'published' AND s.depth < $2
		)
		SELECT 1 FROM scope WHERE id = $3 LIMIT 1`
	var x int
	err := q.QueryRowContext(ctx, query, rootID, shareSubtreeDepth, descendantID).Scan(&x)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// shareSubtree returns the in-scope pages for a share. When includeDescendants
// is false, returns just the root page (one entry). When true, returns the
// root + all transitive descendants, capped at shareSubtreeDepth levels.
func shareSubtree(ctx context.Context, q *sql.DB, rootID int64, includeDescendants bool) ([]sharePageNode, error) {
	if !includeDescendants {
		var n sharePageNode
		var parentID sql.NullInt64
		row := q.QueryRowContext(ctx,
			`SELECT id, title, parent_id, position FROM pages WHERE id = $1 AND deleted_at IS NULL`, rootID)
		if err := row.Scan(&n.ID, &n.Title, &parentID, &n.Position); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return []sharePageNode{}, nil
			}
			return nil, err
		}
		if parentID.Valid {
			v := parentID.Int64
			n.ParentID = &v
		}
		return []sharePageNode{n}, nil
	}

	const query = `
		WITH RECURSIVE scope(id, title, parent_id, position, space_id, depth) AS (
		  SELECT id, title, parent_id, position, space_id, 0
		    FROM pages WHERE id = $1 AND deleted_at IS NULL
		  UNION ALL
		  SELECT p.id, p.title, p.parent_id, p.position, p.space_id, s.depth + 1
		    FROM pages p
		    JOIN scope s ON p.parent_id = s.id
		   WHERE p.space_id = s.space_id AND p.deleted_at IS NULL AND p.status = 'published' AND s.depth < $2
		)
		SELECT id, title, parent_id, position FROM scope
		 ORDER BY position ASC, id ASC`
	rows, err := q.QueryContext(ctx, query, rootID, shareSubtreeDepth)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []sharePageNode{}
	for rows.Next() {
		var n sharePageNode
		var parentID sql.NullInt64
		if err := rows.Scan(&n.ID, &n.Title, &parentID, &n.Position); err != nil {
			return nil, err
		}
		if parentID.Valid {
			v := parentID.Int64
			n.ParentID = &v
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
