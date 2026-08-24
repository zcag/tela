package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/zcag/tela/backend/internal/auth"
)

// fakeOIDC is a minimal OIDC provider: discovery doc + JWKS + a token endpoint
// that returns a pre-set id_token. Enough to exercise the real org-SSO flow
// (discovery → code exchange → id_token verification → provisioning) without a
// live IdP.
type fakeOIDC struct {
	*httptest.Server
	priv    *rsa.PrivateKey
	mu      sync.Mutex
	idToken string
}

func (f *fakeOIDC) setIDToken(s string) { f.mu.Lock(); f.idToken = s; f.mu.Unlock() }

func startFakeOIDC(t *testing.T) *fakeOIDC {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeOIDC{priv: priv}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{
			"issuer": %q,
			"authorization_endpoint": %q,
			"token_endpoint": %q,
			"jwks_uri": %q,
			"id_token_signing_alg_values_supported": ["RS256"]
		}`, f.URL, f.URL+"/authorize", f.URL+"/token", f.URL+"/jwks")
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		pub := &priv.PublicKey
		n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"keys":[{"kty":"RSA","use":"sig","alg":"RS256","kid":%q,"n":%q,"e":%q}]}`, testJWTKID, n, e)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		tok := f.idToken
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":"at","token_type":"Bearer","expires_in":3600,"id_token":%q}`, tok)
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// mintOIDCToken signs an id_token for the fake provider.
func (f *fakeOIDC) mintToken(t *testing.T, aud, sub, email string, verified bool, nonce string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss":            f.URL,
		"aud":            aud,
		"sub":            sub,
		"email":          email,
		"email_verified": verified,
		"name":           "",
		"nonce":          nonce,
		"iat":            time.Now().Unix(),
		"exp":            time.Now().Add(time.Hour).Unix(),
	})
	tok.Header["kid"] = testJWTKID
	s, err := tok.SignedString(f.priv)
	if err != nil {
		t.Fatalf("sign id_token: %v", err)
	}
	return s
}

// runOrgSSO runs /start then /callback against an org connection and returns
// the final callback response (redirects disabled). email/sub identify the IdP
// user; verified controls email_verified.
func runOrgSSO(t *testing.T, ts *httptest.Server, srv *Server, idp *fakeOIDC, client *http.Client, domain, sub, email string, verified bool, next string) *http.Response {
	t.Helper()
	startURL := ts.URL + "/api/auth/sso/org/start?domain=" + url.QueryEscape(domain)
	if next != "" {
		startURL += "&next=" + url.QueryEscape(next)
	}
	r1, err := client.Get(startURL)
	if err != nil {
		t.Fatal(err)
	}
	r1.Body.Close()
	if r1.StatusCode != http.StatusFound {
		t.Fatalf("/start: want 302, got %d", r1.StatusCode)
	}
	st := stateFromResponse(t, srv, r1)
	idp.setIDToken(idp.mintToken(t, "test-client", sub, email, verified, st.Nonce))

	r2, err := client.Get(ts.URL + "/api/auth/sso/org/callback?code=xyz&state=" + url.QueryEscape(st.Token))
	if err != nil {
		t.Fatal(err)
	}
	return r2
}

// stateFromResponse pulls the signed SSO state cookie off a /start response and
// verifies it with the server's secret.
func stateFromResponse(t *testing.T, srv *Server, r *http.Response) ssoState {
	t.Helper()
	for _, c := range r.Cookies() {
		if c.Name == ssoStateCookie {
			st, ok := srv.verifySSOState(c.Value)
			if !ok {
				t.Fatal("state cookie failed verification")
			}
			return st
		}
	}
	t.Fatal("no sso state cookie on /start response")
	return ssoState{}
}

func noRedirJarClient(t *testing.T) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	return &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// TestSSO_OrgProvisionAndLink covers the org-SSO happy paths end to end: a new
// IdP user is provisioned + signed in; a returning user (same sub) reuses the
// account; and a verified email matching an existing in-domain account links
// instead of duplicating.
func TestSSO_OrgProvisionAndLink(t *testing.T) {
	t.Setenv("TELA_SHARE_SECRET", "tela-test-share-secret-fixed-32-byte!")
	idp := startFakeOIDC(t)

	d := newAPITestDB(t)
	handler, srv := HandlerWithServer(d)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	orgID := seedOrg(t, d, "Acme", "acme")
	mustExec(t, d, `INSERT INTO org_email_domains (domain, org_id) VALUES ('acme.test', $1)`, orgID)
	mustExec(t, d, `INSERT INTO org_sso (org_id, issuer, client_id, client_secret, enforced)
		VALUES ($1, $2, 'test-client', 'test-secret', 0)`, orgID, idp.URL)
	// SSO is an Enterprise entitlement (migration 0059) — put the org on a plan
	// that grants it so the login flow isn't gated off.
	mustExec(t, d, `UPDATE orgs SET plan_key = 'org_enterprise' WHERE id = $1`, orgID)

	// (1) New user → provisioned, session set, redirected to next.
	client := noRedirJarClient(t)
	r := runOrgSSO(t, ts, srv, idp, client, "acme.test", "ext-neo", "neo@acme.test", true, "/spaces")
	r.Body.Close()
	if r.StatusCode != http.StatusFound || r.Header.Get("Location") != "/spaces" {
		t.Fatalf("callback: want 302→/spaces, got %d %q", r.StatusCode, r.Header.Get("Location"))
	}
	if !hasCookie(r, auth.CookieName) {
		t.Fatal("callback did not set a session cookie")
	}
	neoID := userIDByEmail(t, d, "neo@acme.test")
	if neoID == 0 {
		t.Fatal("new SSO user was not provisioned")
	}
	assertIdentity(t, d, fmt.Sprintf("org:%d", orgID), "ext-neo", neoID)

	// (2) Returning user (same sub) → same account, no duplicate.
	r2 := runOrgSSO(t, ts, srv, idp, noRedirJarClient(t), "acme.test", "ext-neo", "neo@acme.test", true, "")
	r2.Body.Close()
	if got := userIDByEmail(t, d, "neo@acme.test"); got != neoID {
		t.Fatalf("returning user remapped: was %d now %d", neoID, got)
	}
	if n := countUsers(t, d); n != 1 {
		t.Fatalf("expected 1 user after re-login, got %d", n)
	}

	// (3) Auto-link: a pre-existing in-domain account with the IdP's verified
	// email gets the identity attached rather than a second account created.
	var trinityID int64
	hash, _ := auth.HashPassword("password123")
	mustQueryRow(t, d, `INSERT INTO users (username, email, email_verified_at, password_hash, is_active)
		VALUES ('trinity','trinity@acme.test',tela_now(),$1,1) RETURNING id`, &trinityID, hash)
	before := countUsers(t, d)
	r3 := runOrgSSO(t, ts, srv, idp, noRedirJarClient(t), "acme.test", "ext-trinity", "trinity@acme.test", true, "")
	r3.Body.Close()
	if after := countUsers(t, d); after != before {
		t.Fatalf("auto-link created a new user: %d → %d", before, after)
	}
	assertIdentity(t, d, fmt.Sprintf("org:%d", orgID), "ext-trinity", trinityID)
}

// TestSSO_StateMismatch — a callback whose state param doesn't match the signed
// cookie is rejected and bounced to /login (no session issued).
func TestSSO_StateMismatch(t *testing.T) {
	t.Setenv("TELA_SHARE_SECRET", "tela-test-share-secret-fixed-32-byte!")
	idp := startFakeOIDC(t)
	d := newAPITestDB(t)
	handler, srv := HandlerWithServer(d)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	_ = srv

	orgID := seedOrg(t, d, "Acme", "acme")
	mustExec(t, d, `INSERT INTO org_email_domains (domain, org_id) VALUES ('acme.test', $1)`, orgID)
	mustExec(t, d, `INSERT INTO org_sso (org_id, issuer, client_id, client_secret, enforced)
		VALUES ($1, $2, 'test-client', 'test-secret', 0)`, orgID, idp.URL)
	// SSO is an Enterprise entitlement (migration 0059) — put the org on a plan
	// that grants it so the login flow isn't gated off.
	mustExec(t, d, `UPDATE orgs SET plan_key = 'org_enterprise' WHERE id = $1`, orgID)

	client := noRedirJarClient(t)
	r1, err := client.Get(ts.URL + "/api/auth/sso/org/start?domain=acme.test")
	if err != nil {
		t.Fatal(err)
	}
	r1.Body.Close()

	// Tampered state param → bounce to /login?sso_error, no session.
	r2, err := client.Get(ts.URL + "/api/auth/sso/org/callback?code=xyz&state=not-the-token")
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusFound || !strings.HasPrefix(r2.Header.Get("Location"), "/login?sso_error=") {
		t.Fatalf("state mismatch: want 302→/login?sso_error, got %d %q", r2.StatusCode, r2.Header.Get("Location"))
	}
	if hasCookie(r2, auth.CookieName) {
		t.Fatal("state-mismatch callback should not set a session")
	}
}

// TestSSO_EnforcedBlocksPassword — when an org enforces SSO, password login for
// an account in its domain is refused with 403 sso_required; a non-enforced
// domain still allows password login.
func TestSSO_EnforcedBlocksPassword(t *testing.T) {
	d := newAPITestDB(t)
	handler := Handler(d)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	orgID := seedOrg(t, d, "Corp", "corp")
	mustExec(t, d, `INSERT INTO org_email_domains (domain, org_id) VALUES ('corp.test', $1)`, orgID)
	mustExec(t, d, `INSERT INTO org_sso (org_id, issuer, client_id, client_secret, enforced)
		VALUES ($1, 'https://idp.corp.test', 'cid', 'csec', 1)`, orgID)
	// SSO is an Enterprise entitlement (migration 0059) — entitle the org so the
	// enforced-SSO password block applies (an unentitled org wouldn't block).
	mustExec(t, d, `UPDATE orgs SET plan_key = 'org_enterprise' WHERE id = $1`, orgID)

	hash, _ := auth.HashPassword("password123")
	mustExec(t, d, `INSERT INTO users (username, email, email_verified_at, password_hash, is_active)
		VALUES ('bob','bob@corp.test',tela_now(),$1,1)`, hash)

	resp := postLogin(t, ts, "bob@corp.test", "password123")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("enforced domain: want 403, got %d", resp.StatusCode)
	}
	var body struct{ Code string }
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Code != "sso_required" {
		t.Fatalf("enforced domain: want code sso_required, got %q", body.Code)
	}

	// A user outside any enforced domain still logs in with a password.
	hash2, _ := auth.HashPassword("password123")
	mustExec(t, d, `INSERT INTO users (username, email, email_verified_at, password_hash, is_active)
		VALUES ('carol','carol@free.test',tela_now(),$1,1)`, hash2)
	ok := postLogin(t, ts, "carol@free.test", "password123")
	ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("non-enforced domain: want 200, got %d", ok.StatusCode)
	}
}

// --- small test helpers ---

func postLogin(t *testing.T, ts *httptest.Server, identifier, password string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"identifier": identifier, "password": password})
	resp, err := http.Post(ts.URL+"/api/auth/login", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func hasCookie(r *http.Response, name string) bool {
	for _, c := range r.Cookies() {
		if c.Name == name && c.Value != "" {
			return true
		}
	}
	return false
}

func mustExec(t *testing.T, d *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func mustQueryRow(t *testing.T, d *sql.DB, query string, dest any, args ...any) {
	t.Helper()
	if err := d.QueryRowContext(context.Background(), query, args...).Scan(dest); err != nil {
		t.Fatalf("queryrow %q: %v", query, err)
	}
}

// TestSSO_CapturesDisplayName — a new SSO account keeps the IdP's properly-cased
// name as display_name, while the username is its slug. This is what lets the UI
// greet "Ekrem Mert Esen" instead of the "ekrem-mert-esen" handle.
func TestSSO_CapturesDisplayName(t *testing.T) {
	d := newAPITestDB(t)
	ctx := context.Background()

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	id := ssoIdentity{
		provider:    "google",
		subject:     "ext-ekrem",
		email:       "ekrem@example.test",
		displayName: "Ekrem Mert Esen",
		linkTrusted: true,
	}
	userID, username, created, err := resolveSSOUser(ctx, tx, id)
	if err != nil {
		t.Fatalf("resolveSSOUser: %v", err)
	}
	if !created {
		t.Fatal("resolveSSOUser reported an existing user for a brand-new identity")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if username != "ekrem-mert-esen" {
		t.Fatalf("username = %q, want slug ekrem-mert-esen", username)
	}
	if got := displayNameOf(t, d, userID); got != "Ekrem Mert Esen" {
		t.Fatalf("display_name = %q, want %q", got, "Ekrem Mert Esen")
	}
}

// TestSSO_BackfillsBlankDisplayName — a returning login on an account whose
// display_name is blank (provisioned before the column / before the IdP gave a
// name) self-heals from the claim; a name the user later set is never clobbered.
func TestSSO_BackfillsBlankDisplayName(t *testing.T) {
	d := newAPITestDB(t)

	// Seed an account + identity with a blank display_name (the pre-migration shape).
	var userID int64
	hash, _ := auth.HashPassword("x")
	mustQueryRow(t, d, `INSERT INTO users (username, display_name, email, password_hash, is_active)
		VALUES ('ekrem-mert-esen','','ekrem@example.test',$1,1) RETURNING id`, &userID, hash)
	mustExec(t, d, `INSERT INTO sso_identities (user_id, provider, subject, email)
		VALUES ($1, 'google', 'ext-ekrem', 'ekrem@example.test')`, userID)

	id := ssoIdentity{provider: "google", subject: "ext-ekrem", email: "ekrem@example.test", displayName: "Ekrem Mert Esen"}
	resolveInTx(t, d, id)
	if got := displayNameOf(t, d, userID); got != "Ekrem Mert Esen" {
		t.Fatalf("after backfill: display_name = %q, want %q", got, "Ekrem Mert Esen")
	}

	// A subsequent login with a different claim must NOT overwrite the now-set name.
	id.displayName = "Someone Else"
	resolveInTx(t, d, id)
	if got := displayNameOf(t, d, userID); got != "Ekrem Mert Esen" {
		t.Fatalf("backfill clobbered a set name: display_name = %q", got)
	}
}

func resolveInTx(t *testing.T, d *sql.DB, id ssoIdentity) {
	t.Helper()
	tx, err := d.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, _, _, err := resolveSSOUser(context.Background(), tx, id); err != nil {
		t.Fatalf("resolveSSOUser: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// TestSSO_AppliesPendingInvites — an SSO signup arrives already email-verified,
// so it must redeem pending invites just like VerifyEmail does. It didn't: a
// real user invited to a space, who then signed up with Google, kept 403'ing on
// every page in it. Drives signInSSO (not applyPendingInvites directly) —
// the helper was always correct, the wiring to this path was what was missing.
func TestSSO_AppliesPendingInvites(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	ctx := context.Background()

	ownerID := seedUser(t, d, "alice", "alicepw12", false)
	space := seedSpace(t, d, "Job Search", "job-search", ownerID)
	org := seedOrg(t, d, "Acme", "acme")
	mustExec(t, d, `INSERT INTO space_invites (space_id, email, role, token_hash, expires_at)
		VALUES ($1, 'carol@acme.com', 'editor', 'sso-space-hash', to_char((now() AT TIME ZONE 'UTC') + interval '7 days', 'YYYY-MM-DD HH24:MI:SS'))`, space)
	mustExec(t, d, `INSERT INTO org_invites (org_id, email, org_role, token_hash, expires_at)
		VALUES ($1, 'carol@acme.com', 'member', 'sso-org-hash', to_char((now() AT TIME ZONE 'UTC') + interval '7 days', 'YYYY-MM-DD HH24:MI:SS'))`, org)

	// Mixed case on the claim: invite matching is case-insensitive on email.
	id := ssoIdentity{
		provider:    "google",
		subject:     "ext-carol",
		email:       "Carol@acme.com",
		displayName: "Carol Danvers",
		linkTrusted: true,
	}
	userID, err := srv.signInSSO(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/auth/sso/google/callback", nil), id)
	if err != nil {
		t.Fatalf("signInSSO: %v", err)
	}

	var role string
	if err := d.QueryRowContext(ctx, `SELECT role FROM space_members WHERE space_id=$1 AND user_id=$2`, space, userID).Scan(&role); err != nil || role != "editor" {
		t.Fatalf("SSO signup should have joined the invited space as editor, role=%q err=%v", role, err)
	}
	if err := d.QueryRowContext(ctx, `SELECT org_role FROM org_members WHERE org_id=$1 AND user_id=$2`, org, userID).Scan(&role); err != nil {
		t.Fatalf("SSO signup should have joined the invited org: %v", err)
	}
	var pending int
	mustQueryRow(t, d, `SELECT COUNT(*) FROM space_invites WHERE space_id=$1 AND accepted_at IS NULL`, &pending, space)
	if pending != 0 {
		t.Fatalf("space invite should be marked accepted, %d still pending", pending)
	}
}

func displayNameOf(t *testing.T, d *sql.DB, userID int64) string {
	t.Helper()
	var name string
	if err := d.QueryRowContext(context.Background(), `SELECT display_name FROM users WHERE id = $1`, userID).Scan(&name); err != nil {
		t.Fatalf("scan display_name: %v", err)
	}
	return name
}

func userIDByEmail(t *testing.T, d *sql.DB, email string) int64 {
	t.Helper()
	var id int64
	err := d.QueryRowContext(context.Background(), `SELECT id FROM users WHERE email = $1`, email).Scan(&id)
	if err != nil {
		return 0
	}
	return id
}

func countUsers(t *testing.T, d *sql.DB) int {
	t.Helper()
	var n int
	if err := d.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		t.Fatalf("count users: %v", err)
	}
	return n
}

func assertIdentity(t *testing.T, d *sql.DB, provider, subject string, wantUser int64) {
	t.Helper()
	var got int64
	err := d.QueryRowContext(context.Background(),
		`SELECT user_id FROM sso_identities WHERE provider = $1 AND subject = $2`, provider, subject).Scan(&got)
	if err != nil {
		t.Fatalf("identity (%s,%s) not found: %v", provider, subject, err)
	}
	if got != wantUser {
		t.Fatalf("identity (%s,%s) → user %d, want %d", provider, subject, got, wantUser)
	}
}

// TestSSO_OrgStartRequiresEntitlement — org SSO login is an Enterprise feature:
// an org without the entitlement can't start an SSO login even with a connection
// row (gated as "not configured"); entitling the org lets it proceed to the IdP.
func TestSSO_OrgStartRequiresEntitlement(t *testing.T) {
	t.Setenv("TELA_SHARE_SECRET", "tela-test-share-secret-fixed-32-byte!")
	idp := startFakeOIDC(t)
	d := newAPITestDB(t)
	handler := Handler(d)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	orgID := seedOrg(t, d, "Acme", "acme") // org_free — no sso entitlement
	mustExec(t, d, `INSERT INTO org_email_domains (domain, org_id) VALUES ('acme.test', $1)`, orgID)
	mustExec(t, d, `INSERT INTO org_sso (org_id, issuer, client_id, client_secret, enforced)
		VALUES ($1, $2, 'cid', 'csec', 0)`, orgID, idp.URL)

	client := noRedirJarClient(t)

	// Unentitled (org_free) → start is gated off as not-configured (404).
	r1, err := client.Get(ts.URL + "/api/auth/sso/org/start?domain=acme.test")
	if err != nil {
		t.Fatal(err)
	}
	r1.Body.Close()
	if r1.StatusCode != http.StatusNotFound {
		t.Fatalf("unentitled org SSO start: want 404, got %d", r1.StatusCode)
	}

	// Entitle the org (Enterprise) → start now proceeds to the IdP redirect.
	mustExec(t, d, `UPDATE orgs SET plan_key = 'org_enterprise' WHERE id = $1`, orgID)
	r2, err := client.Get(ts.URL + "/api/auth/sso/org/start?domain=acme.test")
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusFound {
		t.Fatalf("entitled org SSO start: want 302 redirect, got %d", r2.StatusCode)
	}
}
