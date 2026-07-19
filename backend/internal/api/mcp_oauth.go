package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/zcag/tela/backend/internal/auth"
)

// mcpOAuth is the OAuth 2.1 Resource-Server config for /api/mcp: it lets the
// endpoint accept WorkOS-issued JWTs (the Claude.ai / ChatGPT "Connect" flow) in
// addition to tela PATs, and serves the Protected Resource Metadata (RFC 9728)
// that bootstraps that flow.
//
// It is nil (OAuth disabled) unless TELA_WORKOS_ISSUER is set — when
// unconfigured the MCP endpoint stays PAT-only and advertises no OAuth, the same
// "no-op until configured" posture as the RAG embedder. So this whole layer is
// inert in prod until the WorkOS env vars land.
type mcpOAuth struct {
	issuer     string // AuthKit domain, e.g. https://x.authkit.app
	resource   string // the aud we enforce (this MCP endpoint's public URL)
	keyfunc    keyfunc.Keyfunc
	prmHandler http.Handler

	// apiKey (WORKOS_API_KEY) + completeURL back the 5b Standalone login bridge:
	// after tela authenticates the user, POST {external_auth_id, user} here to
	// finish the OAuth dance. completeURL is overridable in tests.
	apiKey      string
	completeURL string
}

const workosCompleteURL = "https://api.workos.com/authkit/oauth2/complete"

// loadMCPOAuth builds the OAuth config from env, or returns nil when
// TELA_WORKOS_ISSUER is unset (disabled). ctx bounds the JWKS refresh goroutine.
func loadMCPOAuth(ctx context.Context) *mcpOAuth {
	issuer := strings.TrimRight(os.Getenv("TELA_WORKOS_ISSUER"), "/")
	if issuer == "" {
		return nil
	}
	resource := strings.TrimSpace(os.Getenv("TELA_MCP_RESOURCE"))
	if resource == "" {
		resource = strings.TrimRight(os.Getenv("TELA_PUBLIC_BASE_URL"), "/") + "/api/mcp"
	}
	o, err := newMCPOAuth(ctx, issuer, resource, issuer+"/oauth2/jwks")
	if err != nil {
		slog.Warn("mcp oauth: init failed — OAuth disabled, MCP stays PAT-only", "issuer", issuer, "err", err)
		return nil
	}
	o.apiKey = strings.TrimSpace(os.Getenv("WORKOS_API_KEY"))
	slog.Info("mcp oauth: enabled", "issuer", issuer, "resource", resource,
		"login_bridge", map[bool]string{true: "ready", false: "disabled: WORKOS_API_KEY unset"}[o.apiKey != ""])
	return o
}

// newMCPOAuth is the testable core: wires the JWKS keyfunc + the PRM handler for
// an explicit issuer/resource/jwks. Tests point jwksURL at an in-test JWK Set.
func newMCPOAuth(ctx context.Context, issuer, resource, jwksURL string) (*mcpOAuth, error) {
	kf, err := keyfunc.NewDefaultCtx(ctx, []string{jwksURL})
	if err != nil {
		return nil, err
	}
	prm := &oauthex.ProtectedResourceMetadata{
		Resource:             resource,
		AuthorizationServers: []string{issuer},
		// offline_access is advertised so the client requests it and WorkOS issues
		// a refresh token — without it the access token silently expires after its
		// TTL and the host's "reload tools" 401s with no way to refresh (forcing a
		// full reconnect). The verifier gates on iss/aud/sig only, never on scope.
		ScopesSupported:        []string{"openid", "email", "offline_access"},
		BearerMethodsSupported: []string{"header"},
	}
	return &mcpOAuth{
		issuer:      issuer,
		resource:    resource,
		keyfunc:     kf,
		prmHandler:  sdkauth.ProtectedResourceMetadataHandler(prm),
		completeURL: workosCompleteURL,
	}, nil
}

// resourceMetadataURL is the path-scoped PRM URL the 401 WWW-Authenticate points
// at: {origin}/.well-known/oauth-protected-resource{resource-path}.
func (o *mcpOAuth) resourceMetadataURL() string {
	u, err := url.Parse(o.resource)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + wellKnownPRMPath + u.Path
}

// verifyJWT validates a WorkOS access token against the JWKS, enforcing issuer,
// audience (== resource; RFC 8707 replay defense), signing alg, and a small
// clock-skew leeway. Returns the claims on success.
//
// It accepts the configured resource URL and any legacy aliases in
// mcpResourceAliases — both resolve to the same instance, so accepting older
// audience values is safe and allows clients holding pre-rename tokens to keep
// working until they re-authenticate.
func (o *mcpOAuth) verifyJWT(token string) (jwt.MapClaims, error) {
	audiences := append([]string{o.resource}, mcpResourceAliases...)
	var primaryErr error // error from the canonical-audience attempt, for diagnosis
	for i, aud := range audiences {
		claims := jwt.MapClaims{}
		tok, err := jwt.ParseWithClaims(token, claims, o.keyfunc.Keyfunc,
			jwt.WithIssuer(o.issuer),
			jwt.WithAudience(aud),
			jwt.WithValidMethods([]string{"RS256", "ES256"}),
			jwt.WithLeeway(60*time.Second),
		)
		if err == nil && tok.Valid {
			if aud != o.resource {
				slog.Warn("mcp oauth: accepted legacy-domain audience; update WorkOS resource URL to match TELA_MCP_RESOURCE", "legacy_aud", aud, "current_resource", o.resource)
			}
			return claims, nil
		}
		if i == 0 {
			primaryErr = err
		}
	}
	// All audiences failed; log the classified reason + expiry so a drop
	// ("connection expired", the WorkOS inactivity/session case) is unambiguous
	// rather than a generic verify failure.
	diagClaims := jwt.MapClaims{}
	jwt.ParseWithClaims(token, diagClaims, o.keyfunc.Keyfunc, jwt.WithValidMethods([]string{"RS256", "ES256"})) //nolint:errcheck
	slog.Warn("mcp oauth: JWT verify failed",
		"reason", jwtFailReason(primaryErr),
		"aud", diagClaims["aud"], "iss", diagClaims["iss"], "sub", diagClaims["sub"],
		"exp", diagClaims["exp"], "token_prefix", safePrefix(token, 20))
	return nil, sdkauth.ErrInvalidToken
}

// jwtFailReason maps a jwt/v5 validation error to a short, greppable reason so
// verify failures are self-explaining in the logs (an expired access token — the
// normal "reconnect me" case — reads differently from a bad signature or a
// stale audience left over from a domain rename).
func jwtFailReason(err error) string {
	switch {
	case err == nil:
		return "unknown"
	case errors.Is(err, jwt.ErrTokenExpired):
		return "expired"
	case errors.Is(err, jwt.ErrTokenNotValidYet):
		return "not_yet_valid"
	case errors.Is(err, jwt.ErrTokenInvalidAudience):
		return "aud_mismatch"
	case errors.Is(err, jwt.ErrTokenInvalidIssuer):
		return "iss_mismatch"
	case errors.Is(err, jwt.ErrTokenSignatureInvalid):
		return "bad_signature"
	default:
		return "invalid"
	}
}

// mcpResourceAliases lists historical resource URLs that are aliases for this
// instance. Tokens bearing these audiences are accepted alongside the primary
// TELA_MCP_RESOURCE value — they refer to the same backend, just under an old
// domain name. Remove entries once all clients have re-authenticated.
var mcpResourceAliases = []string{
	"https://tela.cagdas.io/api/mcp", // pre-June-2026 domain, renamed to telawiki.com
}

// safePrefix returns the first n chars of s or s itself when len(s)<n.
func safePrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// verifyWorkOSToken resolves a validated WorkOS JWT to a tela identity. In the
// Standalone-bridge model the JWT's `sub` IS the tela users.id, so we map by sub
// — no email lookup, no mapping table. A synthetic *auth.APIKey makes a connector
// caller look like a write-scoped PAT user to the rest of the tool layer
// (mcpIdentity / mcpRequireWrite), so every tool keeps working unchanged.
// Connector tokens are deliberately never admin-scoped.
func (s *Server) verifyWorkOSToken(ctx context.Context, token string) (*sdkauth.TokenInfo, error) {
	claims, err := s.oauth.verifyJWT(token)
	if err != nil {
		return nil, sdkauth.ErrInvalidToken
	}
	sub, _ := claims["sub"].(string)
	uid, err := strconv.ParseInt(sub, 10, 64)
	if err != nil {
		return nil, sdkauth.ErrInvalidToken
	}
	u, err := auth.UserForAPIKey(ctx, s.DB, uid)
	if err != nil {
		return nil, sdkauth.ErrInvalidToken
	}
	exp := time.Now().Add(time.Hour)
	if e, ok := claims["exp"].(float64); ok {
		exp = time.Unix(int64(e), 0)
	}
	logMCPTokenAccepted(uid, exp)
	k := &auth.APIKey{UserID: uid, Scope: auth.ScopeWrite}
	return &sdkauth.TokenInfo{
		Scopes:     []string{k.Scope},
		UserID:     sub,
		Expiration: exp,
		Extra: map[string]any{
			tokenExtraUser:   u,
			tokenExtraAPIKey: k,
		},
	}, nil
}

const wellKnownPRMPath = "/.well-known/oauth-protected-resource"

// ServePRM serves the Protected Resource Metadata (RFC 9728), or 404 when OAuth
// is unconfigured. Registered at both the root well-known and the path-scoped
// variant (Claude probes both). Public + static (on auth.IsPublicPath).
func (s *Server) ServePRM(w http.ResponseWriter, r *http.Request) {
	if s.oauth == nil {
		http.NotFound(w, r)
		return
	}
	s.oauth.prmHandler.ServeHTTP(w, r)
}

// WorkOSLogin (GET) is the Standalone "OAuth Bridge" Login URI (5b): WorkOS
// sends the user here with ?external_auth_id=… during the Connect flow. It
// authenticates against tela's OWN session (bouncing through /login if not
// signed in) and then renders an explicit consent page. It does NOT complete the
// flow on the GET — completion happens only on the CSRF-protected POST to
// WorkOSLoginComplete. This prevents login/account-linking CSRF: a bare GET with
// an attacker-supplied external_auth_id can't silently bind a victim's session
// to the attacker's pending connector authorization. On auth.IsPublicPath: it
// self-authenticates (unauthenticated → redirect to login, not 401).
func (s *Server) WorkOSLogin(w http.ResponseWriter, r *http.Request) {
	if s.oauth == nil {
		http.NotFound(w, r)
		return
	}
	externalAuthID := r.URL.Query().Get("external_auth_id")
	if externalAuthID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "external_auth_id is required")
		return
	}

	c, _ := r.Cookie(auth.CookieName)
	u := s.sessionUser(r)
	if u == nil || c == nil {
		// Not signed in → bounce through tela's own login, returning here after
		// (the SPA hard-redirects to this backend path on success).
		ret := "/oauth/workos/login?external_auth_id=" + url.QueryEscape(externalAuthID)
		http.Redirect(w, r, "/login?next="+url.QueryEscape(ret), http.StatusFound)
		return
	}
	if u.Email == "" {
		http.Error(w, "your tela account has no email address; cannot connect via OAuth", http.StatusBadRequest)
		return
	}
	renderWorkOSConsent(w, u, externalAuthID, s.workosBridgeCSRF(c.Value, externalAuthID))
}

// WorkOSLoginComplete (POST) finishes the bridge after the user explicitly
// confirms on the consent page. It re-checks the session and a CSRF token bound
// to that session + external_auth_id (so the POST can't be forged), then calls
// the WorkOS completion API and redirects to the returned redirect_uri.
func (s *Server) WorkOSLoginComplete(w http.ResponseWriter, r *http.Request) {
	if s.oauth == nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not parse form")
		return
	}
	externalAuthID := r.PostFormValue("external_auth_id")
	if externalAuthID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "external_auth_id is required")
		return
	}
	c, _ := r.Cookie(auth.CookieName)
	u := s.sessionUser(r)
	if u == nil || c == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "session required")
		return
	}
	expected := s.workosBridgeCSRF(c.Value, externalAuthID)
	if subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(expected)) != 1 {
		writeError(w, http.StatusForbidden, "forbidden", "invalid or expired confirmation")
		return
	}
	if u.Email == "" {
		http.Error(w, "your tela account has no email address; cannot connect via OAuth", http.StatusBadRequest)
		return
	}

	redirectURI, err := s.oauth.completeStandalone(r.Context(), externalAuthID, u)
	if err != nil {
		slog.Error("workos standalone complete failed", "user_id", u.ID, "err", err)
		http.Error(w, "could not complete authorization", http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, redirectURI, http.StatusFound)
}

// workosBridgeCSRF is an HMAC over (session cookie value + external_auth_id),
// keyed by the server's share secret. An attacker can't forge it without the
// victim's HttpOnly session value and the secret, so it ties the consent POST to
// the exact session + pending authorization shown on the GET consent page.
func (s *Server) workosBridgeCSRF(sessionValue, externalAuthID string) string {
	mac := hmac.New(sha256.New, s.shareSecret)
	mac.Write([]byte("workos-bridge\x00" + sessionValue + "\x00" + externalAuthID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// renderWorkOSConsent writes the explicit "connect your tela account" page whose
// only action is a same-origin POST carrying the CSRF token.
func renderWorkOSConsent(w http.ResponseWriter, u *auth.User, externalAuthID, csrf string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = fmt.Fprintf(w, workosConsentHTML,
		html.EscapeString(u.Email), html.EscapeString(externalAuthID), html.EscapeString(csrf))
}

const workosConsentHTML = `<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1"><title>Connect to tela</title>
<style>body{font:15px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;
background:#f6f7f9;color:#1a1a1a;display:flex;min-height:100vh;align-items:center;justify-content:center;margin:0}
.card{background:#fff;border:1px solid #e5e7eb;border-radius:14px;padding:28px 30px;max-width:420px;
box-shadow:0 1px 3px rgba(0,0,0,.06)}h1{font-size:19px;margin:0 0 10px}p{color:#4b5563;margin:8px 0}
.who{font-weight:600;color:#1a1a1a}button{margin-top:18px;width:100%%;padding:11px;border:none;border-radius:9px;
background:#4f46e5;color:#fff;font-size:15px;font-weight:600;cursor:pointer}button:hover{background:#4338ca}
.muted{font-size:13px;color:#9ca3af;margin-top:14px}</style></head><body>
<div class="card"><h1>Connect to tela</h1>
<p>An application is requesting access to your tela account as <span class="who">%s</span>.</p>
<p>It will be able to read and edit the spaces and pages you can access. Only continue if you started this from the app.</p>
<form method="post" action="/oauth/workos/login">
<input type="hidden" name="external_auth_id" value="%s">
<input type="hidden" name="csrf" value="%s">
<button type="submit">Connect tela account</button></form>
<p class="muted">If you didn't initiate this, close this page.</p></div></body></html>`

// sessionUser resolves the tela user from the session cookie, or nil when there
// is no valid session. Used by the public-path WorkOS bridge to self-authenticate.
func (s *Server) sessionUser(r *http.Request) *auth.User {
	c, err := r.Cookie(auth.CookieName)
	if err != nil || c.Value == "" {
		return nil
	}
	u, err := auth.LoadSessionAndSlide(r.Context(), s.DB, c.Value)
	if err != nil {
		return nil
	}
	return u
}

// completeStandalone finishes the WorkOS Standalone OAuth flow for an
// authenticated tela user: POST {external_auth_id, user{id,email}} to the
// completion API (Bearer WORKOS_API_KEY) and return the redirect_uri the browser
// must be sent to next. The user.id we send becomes the issued token's `sub`.
func (o *mcpOAuth) completeStandalone(ctx context.Context, externalAuthID string, u *auth.User) (string, error) {
	if o.apiKey == "" {
		return "", errors.New("WORKOS_API_KEY not set")
	}
	body, _ := json.Marshal(map[string]any{
		"external_auth_id": externalAuthID,
		"user": map[string]any{
			"id":    strconv.FormatInt(u.ID, 10),
			"email": u.Email,
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.completeURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return "", fmt.Errorf("workos complete: status %d: %s", resp.StatusCode, b)
	}
	var out struct {
		RedirectURI string `json:"redirect_uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.RedirectURI == "" {
		return "", errors.New("workos complete: empty redirect_uri")
	}
	return out.RedirectURI, nil
}
