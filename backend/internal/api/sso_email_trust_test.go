package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/zcag/tela/backend/internal/auth"
)

// TestSSO_OrgUnverifiedEmailStillWorks pins org SSO for IdPs that send no
// email_verified (Microsoft Entra, e.g. a live org on telawiki.com): an org
// login is trusted by the org owning the email domain, never by the social
// providers' email rules, so a change to those must not lock an org out.
// Covers new user, returning user, and linking a pre-existing in-domain account.
func TestSSO_OrgUnverifiedEmailStillWorks(t *testing.T) {
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
	mustExec(t, d, `UPDATE orgs SET plan_key = 'org_enterprise' WHERE id = $1`, orgID)
	provider := fmt.Sprintf("org:%d", orgID)

	signIn := func(sub, email string) {
		t.Helper()
		r := runOrgSSO(t, ts, srv, idp, noRedirJarClient(t), "acme.test", sub, email, false, "/spaces")
		r.Body.Close()
		if r.StatusCode != http.StatusFound || r.Header.Get("Location") != "/spaces" || !hasCookie(r, auth.CookieName) {
			t.Fatalf("org sign-in %s: got %d %q, want 302 /spaces with a session", email, r.StatusCode, r.Header.Get("Location"))
		}
	}

	signIn("ext-neo", "neo@acme.test")
	neoID := userIDByEmail(t, d, "neo@acme.test")
	assertIdentity(t, d, provider, "ext-neo", neoID)
	signIn("ext-neo", "neo@acme.test")
	if n := countUsers(t, d); n != 1 {
		t.Fatalf("returning user duplicated: %d users", n)
	}

	var trinityID int64
	hash, _ := auth.HashPassword("password123")
	mustQueryRow(t, d, `INSERT INTO users (username, email, email_verified_at, password_hash, is_active)
		VALUES ('trinity','trinity@acme.test',tela_now(),$1,1) RETURNING id`, &trinityID, hash)
	signIn("ext-trinity", "trinity@acme.test")
	assertIdentity(t, d, provider, "ext-trinity", trinityID)
}

// mintClaims signs an id_token for the fake provider with arbitrary claims on
// top of the standard iss/aud/nonce/iat/exp.
func (f *fakeOIDC) mintClaims(t *testing.T, aud, nonce string, extra jwt.MapClaims) string {
	t.Helper()
	claims := jwt.MapClaims{"iss": f.URL, "aud": aud, "nonce": nonce,
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
	for k, v := range extra {
		claims[k] = v
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = testJWTKID
	s, err := tok.SignedString(f.priv)
	if err != nil {
		t.Fatalf("sign id_token: %v", err)
	}
	return s
}

// TestSSO_MicrosoftEmailNeedsEDOV: the social Microsoft provider sends no
// email_verified, and its email is whatever the issuing tenant's admin typed.
// Without xms_edov it must neither link into an existing account (the nOAuth
// takeover) nor mint one; with it, it links. A returning user matched by
// subject signs in without it.
func TestSSO_MicrosoftEmailNeedsEDOV(t *testing.T) {
	t.Setenv("TELA_SHARE_SECRET", "tela-test-share-secret-fixed-32-byte!")
	idp := startFakeOIDC(t)
	d := newAPITestDB(t)
	handler, srv := HandlerWithServer(d)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	p, err := buildOIDCProvider(context.Background(), ts.URL, "microsoft", "Microsoft", idp.URL,
		"test-client", "test-secret", []string{"openid", "email"}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.trustEmail = true
	srv.sso.social["microsoft"] = p

	var aliceID int64
	hash, _ := auth.HashPassword("password123")
	mustQueryRow(t, d, `INSERT INTO users (username, email, email_verified_at, password_hash, is_active)
		VALUES ('alice','alice@example.com',tela_now(),$1,1) RETURNING id`, &aliceID, hash)

	signIn := func(sub, email string, edov any) *http.Response {
		t.Helper()
		client := noRedirJarClient(t)
		r1, err := client.Get(ts.URL + "/api/auth/sso/microsoft/start")
		if err != nil {
			t.Fatal(err)
		}
		r1.Body.Close()
		st := stateFromResponse(t, srv, r1)
		extra := jwt.MapClaims{"sub": sub, "email": email}
		if edov != nil {
			extra["xms_edov"] = edov
		}
		idp.setIDToken(idp.mintClaims(t, "test-client", st.Nonce, extra))
		r2, err := client.Get(ts.URL + "/api/auth/sso/microsoft/callback?code=xyz&state=" + url.QueryEscape(st.Token))
		if err != nil {
			t.Fatal(err)
		}
		r2.Body.Close()
		return r2
	}
	identities := func() int {
		var n int
		mustQueryRow(t, d, `SELECT count(*) FROM sso_identities WHERE provider = 'microsoft'`, &n)
		return n
	}

	// Attacker tenant claims alice's address: refused, nothing linked or created.
	for _, edov := range []any{nil, false, "false"} {
		if r := signIn("attacker-sub", "alice@example.com", edov); hasCookie(r, auth.CookieName) {
			t.Fatalf("xms_edov=%v: unverified Microsoft email got a session", edov)
		}
	}
	if n := identities(); n != 0 {
		t.Fatalf("unverified email linked %d identities", n)
	}
	if n := countUsers(t, d); n != 1 {
		t.Fatalf("unverified email created an account: %d users", n)
	}

	// Domain-verified email: links into alice.
	if r := signIn("alice-sub", "alice@example.com", true); !hasCookie(r, auth.CookieName) {
		t.Fatal("xms_edov=true: no session")
	}
	assertIdentity(t, d, "microsoft", "alice-sub", aliceID)

	// Returning alice, claim absent (optional claim switched off later): still in.
	if r := signIn("alice-sub", "alice@example.com", nil); !hasCookie(r, auth.CookieName) {
		t.Fatal("returning user refused without xms_edov")
	}
	if n := identities(); n != 1 {
		t.Fatalf("returning user added identities: %d", n)
	}
}
