package api

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zcag/tela/backend/internal/auth"
)

//go:embed widgets/mcp_icon.svg
var mcpIconSVG []byte

// mcpServerName is the MCP server identity reported to clients on initialize.
const mcpServerName = "tela"

// TokenInfo.Extra keys under which the bearer verifier stashes the resolved
// tela identity so tool handlers can read it back per request via
// req.GetExtra().TokenInfo.Extra (the SDK threads TokenInfo from the request
// context into every tools/call — see streamable.go servePOST).
const (
	tokenExtraUser   = "tela.user"
	tokenExtraAPIKey = "tela.apiKey"
)

// MCPHandler builds the Streamable-HTTP handler mounted at /api/mcp. It is
// self-authenticated via the SDK's bearer middleware over tela PATs (the route
// is on auth.IsPublicPath so tela's own Middleware skips it). One shared server
// backs all sessions; identity is per-request, carried in TokenInfo.
func (s *Server) MCPHandler() http.Handler {
	server := s.newMCPServer()
	streamable := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server }, nil)
	opts := &sdkauth.RequireBearerTokenOptions{}
	if s.oauth != nil {
		// On 401, point clients at our Protected Resource Metadata so the OAuth
		// "Connect" flow can bootstrap. (No Scopes here — that gate applies to
		// ALL tokens incl. PATs, which don't carry openid/email.)
		opts.ResourceMetadataURL = s.oauth.resourceMetadataURL()
	}
	authed := sdkauth.RequireBearerToken(s.mcpVerifier, opts)(streamable)
	// Cap the request body: the SDK's streamable handler imposes no limit and the
	// endpoint is mounted public, so without this a client could POST a multi-GB
	// JSON-RPC body (memory-exhaustion DoS). SSE GET streams carry no request
	// body, so this only bounds the POST bodies.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A JSON-RPC POST with no bearer is the "connection went dark" signal — a
		// host whose OAuth session expired (e.g. WorkOS inactivity timeout) stops
		// sending a token and silently drops to unauthenticated. RequireBearerToken
		// 401s it before mcpVerifier runs, so nothing else records who/when; log it.
		if r.Method == http.MethodPost && !hasBearerToken(r) {
			logMCPUnauthenticated(r)
		}
		r.Body = http.MaxBytesReader(w, r.Body, mcpMaxRequestBytes)
		authed.ServeHTTP(w, r)
	})
}

// hasBearerToken reports whether r carries a non-empty `Authorization: Bearer …`
// header (the same shape RequireBearerToken accepts).
func hasBearerToken(r *http.Request) bool {
	h := r.Header.Get("Authorization")
	return len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") && strings.TrimSpace(h[7:]) != ""
}

// mcpNoBearerThrottle collapses bursts of unauthenticated /api/mcp hits from the
// same client so the drop breadcrumb stays readable.
var mcpNoBearerThrottle sync.Map // clientKey(string) -> time.Time

// logMCPUnauthenticated records (throttled per client) a tokenless POST to
// /api/mcp — the drop signal for a lapsed OAuth connection.
func logMCPUnauthenticated(r *http.Request) {
	ip := clientIPForRateLimit(r)
	ua := safePrefix(r.UserAgent(), 60)
	key := ip + "\x00" + ua
	now := time.Now()
	if last, ok := mcpNoBearerThrottle.Load(key); ok && now.Sub(last.(time.Time)) < 2*time.Minute {
		return
	}
	mcpNoBearerThrottle.Store(key, now)
	slog.Info("mcp: unauthenticated request (no bearer token)", "ip", ip, "ua", ua)
}

// mcpMaxRequestBytes bounds a single MCP HTTP request body. Generous enough for
// a batched JSON-RPC call, with headroom.
const mcpMaxRequestBytes = 4 << 20 // 4 MiB

// newMCPServer constructs the MCP server and registers the tool + resource
// surface. Capabilities (tools / resources) are inferred by the SDK from what's
// registered. The Implementation carries display branding (title, website, icon)
// for the host's connector card — website/icon are derived from the public base
// URL so self-hosters get their own.
func (s *Server) newMCPServer() *mcp.Server {
	impl := &mcp.Implementation{Name: mcpServerName, Title: "Tela", Version: Version}
	if base := canonicalBaseURL(); base != "" {
		impl.WebsiteURL = base
	}
	// Full-bleed square icon as a data URI — the favicon has baked-in rounded
	// corners (rx=56) that render as white corners once a host applies its own
	// rounding mask; this fills the square edge-to-edge so the mask is clean.
	impl.Icons = []mcp.Icon{{
		Source:   "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(mcpIconSVG),
		MIMEType: "image/svg+xml",
		Sizes:    []string{"any"},
	}}
	// Instructions carry a short "how to find things" retrieval guide (so a host
	// picks the right search tool by intent rather than guessing) followed by the
	// authoring guide, so every connected host learns the rich block palette on
	// initialize (generated from the editor's block manifest — see mcp_authoring.go).
	server := mcp.NewServer(impl, &mcp.ServerOptions{Instructions: retrievalGuideMarkdown() + authoringGuideMarkdown(false) + importInstructionsSnippet()})
	s.registerMCPTools(server)
	s.registerMCPResources(server)
	s.registerMCPWidgets(server)
	return server
}

// mcpVerifier is the dual-mode bearer verifier for /api/mcp: a `tela_pat_*`
// token takes the PAT path; anything else is tried as a WorkOS OAuth JWT (only
// when OAuth is configured). Both resolve to a TokenInfo carrying the tela
// *User + *APIKey, so the tool layer is identical for either credential.
func (s *Server) mcpVerifier(ctx context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
	var (
		ti  *sdkauth.TokenInfo
		err error
	)
	switch {
	case strings.HasPrefix(token, auth.TokenPrefix):
		ti, err = s.verifyPAT(ctx, token)
	case s.oauth != nil:
		ti, err = s.verifyWorkOSToken(ctx, token)
	default:
		slog.Warn("mcp: token has no recognized prefix and OAuth disabled", "prefix", safePrefix(token, 20))
		return nil, sdkauth.ErrInvalidToken
	}
	// Stamp MCP-last-seen for the resolved user — this is the one place that sees
	// BOTH PAT and OAuth requests, so it's how an OAuth/cowork connection (which
	// leaves no api_keys row) becomes detectable.
	if err == nil && ti != nil {
		if u, ok := ti.Extra[tokenExtraUser].(*auth.User); ok && u != nil {
			s.touchMCPSeen(u.ID)
		}
	}
	return ti, err
}

// mcpSeenThrottle skips re-stamping a user more than once per window (per process),
// so a chatty MCP session doesn't write on every tool call.
var mcpSeenThrottle sync.Map // userID(int64) -> time.Time

// touchMCPSeen records that userID used MCP "now" — throttled in memory and
// written async/best-effort, so it never adds latency to the request path. The
// stamp only needs to flip from NULL to non-NULL to make the connection visible.
func (s *Server) touchMCPSeen(userID int64) {
	now := time.Now()
	if last, ok := mcpSeenThrottle.Load(userID); ok {
		if now.Sub(last.(time.Time)) < 30*time.Minute {
			return
		}
	}
	mcpSeenThrottle.Store(userID, now)
	go func() {
		_, _ = s.DB.ExecContext(context.Background(),
			`UPDATE users SET mcp_last_seen_at = tela_now() WHERE id = $1`, userID)
	}()
}

// mcpOAuthLogThrottle limits the "token accepted" breadcrumb to ~once per user
// per window: an active connector re-verifies its (short, ~5-min) access token on
// every call, so logging each would flood — but a periodic record of liveness +
// token expiry is what lets us reconstruct when/why a connection dropped.
var mcpOAuthLogThrottle sync.Map // userID(int64) -> time.Time

// logMCPTokenAccepted emits a throttled breadcrumb for a verified OAuth token,
// carrying its expiry and remaining TTL so a drop ("connection expired") can be
// dated against the last-seen live token instead of guessed.
func logMCPTokenAccepted(userID int64, exp time.Time) {
	now := time.Now()
	if last, ok := mcpOAuthLogThrottle.Load(userID); ok && now.Sub(last.(time.Time)) < 10*time.Minute {
		return
	}
	mcpOAuthLogThrottle.Store(userID, now)
	slog.Info("mcp oauth: token accepted",
		"user_id", userID,
		"exp", exp.UTC().Format(time.RFC3339),
		"ttl", time.Until(exp).Round(time.Second).String())
}

// verifyPAT resolves a tela PAT to a TokenInfo. UserID is set so the SDK's
// Streamable-HTTP transport can pin a session to one user (anti-hijack).
func (s *Server) verifyPAT(ctx context.Context, token string) (*sdkauth.TokenInfo, error) {
	secret := auth.LoadAPIKeySecret()
	k, err := auth.LookupAPIKey(ctx, s.DB, secret, token)
	if err != nil {
		return nil, sdkauth.ErrInvalidToken
	}
	u, err := auth.UserForAPIKey(ctx, s.DB, k.UserID)
	if err != nil {
		return nil, sdkauth.ErrInvalidToken
	}
	return &sdkauth.TokenInfo{
		Scopes: []string{k.Scope},
		UserID: strconv.FormatInt(u.ID, 10),
		// The PAT's real expiry is enforced inside LookupAPIKey; this is the
		// SDK's required liveness window. The verifier re-runs on every request
		// (RequireBearerToken is per-request middleware), so it re-mints each
		// time and never spuriously expires a live PAT mid-session.
		Expiration: time.Now().Add(time.Hour),
		Extra: map[string]any{
			tokenExtraUser:   u,
			tokenExtraAPIKey: k,
		},
	}, nil
}

// mcpIdentity pulls the tela identity that mcpVerifier stashed into the
// per-request TokenInfo. Accepts any server request (tool call, resource read,
// prompt get, …) via the GetExtra() method they all share. Returns (nil, nil)
// if somehow unauthenticated — callers must treat that as an error rather than
// acting anonymously.
func mcpIdentity(req interface{ GetExtra() *mcp.RequestExtra }) (*auth.User, *auth.APIKey) {
	ex := req.GetExtra()
	if ex == nil || ex.TokenInfo == nil {
		return nil, nil
	}
	u, _ := ex.TokenInfo.Extra[tokenExtraUser].(*auth.User)
	k, _ := ex.TokenInfo.Extra[tokenExtraAPIKey].(*auth.APIKey)
	return u, k
}

// mcpRequireWrite enforces, at the tool boundary, the write/admin-scope gate
// that the HTTP method-scope middleware applies to mutating REST routes. The
// MCP transport is mounted public (single POST endpoint carries read + write
// tools), so the per-tool scope check moves here: a read-scope key calling a
// write tool gets the same api_key_scope 403 it would get from the middleware.
// Returns nil for write/admin keys (and for nil/session callers — not reachable
// over MCP, but safe).
func mcpRequireWrite(k *auth.APIKey) *apiErr {
	if k == nil || k.Scope != auth.ScopeRead {
		return nil
	}
	return &apiErr{http.StatusForbidden, "api_key_scope", "api key scope does not permit this method"}
}

// mcpErr maps a core *apiErr to a tool-error CallToolResult. The text payload
// is the same {error, code, status} envelope the TS client surfaced, so agents
// keyed on `code` keep working.
func mcpErr(ae *apiErr) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{
			Text: fmt.Sprintf(`{"error":%q,"code":%q,"status":%d}`, ae.Message, ae.Code, ae.Status),
		}},
	}
}

// mcpUnauthErr is returned when a tool runs without a resolved identity.
func mcpUnauthErr() *mcp.CallToolResult {
	return mcpErr(&apiErr{http.StatusUnauthorized, "unauthorized", "missing authenticated identity"})
}

// mcpIdempotent makes a create-type write tool safe to retry. With key=="" it
// just runs fn (the default — no bookkeeping). Otherwise it claims (userID, key)
// in idempotency_keys: the first caller wins the INSERT, runs fn, and memoizes
// the structured result; a replay with the same key returns that stored result
// without re-running fn (so a retry after a dropped connection never creates a
// duplicate). Only SUCCESS is memoized — a tool-error or Go error releases the
// claim so a genuine retry can proceed. A racing duplicate that finds the row
// still in-flight (result NULL) gets a transient idempotency_in_progress error;
// a key reused for a different tool is rejected. Storage failures fall back to
// running fn (best-effort: idempotency degrades, it never blocks the write).
func mcpIdempotent[T any](ctx context.Context, db *sql.DB, userID int64, key, tool string, fn func() (*mcp.CallToolResult, T, error)) (*mcp.CallToolResult, T, error) {
	var zero T
	if key == "" {
		return fn()
	}
	res, err := db.ExecContext(ctx,
		`INSERT INTO idempotency_keys (user_id, idem_key, tool) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		userID, key, tool)
	if err != nil {
		return fn() // best-effort
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// A row already exists for this (user, key): replay or in-flight.
		var storedTool string
		var result sql.NullString
		if err := db.QueryRowContext(ctx,
			`SELECT tool, result FROM idempotency_keys WHERE user_id = $1 AND idem_key = $2`,
			userID, key).Scan(&storedTool, &result); err != nil {
			return fn() // row vanished or scan failed — best-effort
		}
		if storedTool != tool {
			return mcpErr(&apiErr{http.StatusConflict, "idempotency_key_reused",
				"idempotency key already used for a different tool"}), zero, nil
		}
		if !result.Valid {
			return mcpErr(&apiErr{http.StatusConflict, "idempotency_in_progress",
				"a request with this idempotency key is still in progress; retry shortly"}), zero, nil
		}
		var out T
		if err := json.Unmarshal([]byte(result.String), &out); err != nil {
			return fn() // corrupt memo — best-effort
		}
		return nil, out, nil
	}
	// We claimed the key — execute, then memoize on success / release on failure.
	callRes, out, err := fn()
	if err != nil || (callRes != nil && callRes.IsError) {
		_, _ = db.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE user_id = $1 AND idem_key = $2`, userID, key)
		return callRes, out, err
	}
	if b, mErr := json.Marshal(out); mErr == nil {
		_, _ = db.ExecContext(ctx, `UPDATE idempotency_keys SET result = $3 WHERE user_id = $1 AND idem_key = $2`, userID, key, string(b))
	}
	return callRes, out, nil
}
