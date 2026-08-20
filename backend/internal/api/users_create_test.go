package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/zcag/tela/backend/internal/auth"
)

// The signup trial exists in exactly one place (users_create.go) and every
// user-creating path states whether it grants one. These tests pin the policy
// per path, because the failure mode that produced them was silent: an INSERT
// that simply didn't mention the columns, on a path nobody re-read for ten
// weeks, while the pricing page promised otherwise.

// trialOf reads a user's trial columns. Empty strings mean no trial.
func trialOf(t *testing.T, d *sql.DB, userID int64) (planKey, endsAt string) {
	t.Helper()
	var p, e sql.NullString
	if err := d.QueryRow(`SELECT trial_plan_key, trial_ends_at FROM users WHERE id = $1`, userID).Scan(&p, &e); err != nil {
		t.Fatalf("read trial columns for user %d: %v", userID, err)
	}
	return p.String, e.String
}

// assertTrialGranted checks the trial is present, on the right tier, and lands
// signupTrialDays out (±1 day of slack for clock/rounding).
func assertTrialGranted(t *testing.T, d *sql.DB, userID int64, who string) {
	t.Helper()
	planKey, endsAt := trialOf(t, d, userID)
	if planKey != signupTrialPlan {
		t.Fatalf("%s: trial_plan_key = %q, want %q", who, planKey, signupTrialPlan)
	}
	if endsAt == "" {
		t.Fatalf("%s: trial_plan_key set but trial_ends_at empty", who)
	}
	var days float64
	if err := d.QueryRow(
		`SELECT EXTRACT(epoch FROM ($1::timestamp - (now() AT TIME ZONE 'UTC'))) / 86400`, endsAt).Scan(&days); err != nil {
		t.Fatalf("%s: measure trial length: %v", who, err)
	}
	if days < signupTrialDays-1 || days > signupTrialDays+1 {
		t.Fatalf("%s: trial ends in %.2f days, want ~%d", who, days, signupTrialDays)
	}
}

func assertNoTrial(t *testing.T, d *sql.DB, userID int64, who string) {
	t.Helper()
	if planKey, endsAt := trialOf(t, d, userID); planKey != "" || endsAt != "" {
		t.Fatalf("%s: want no trial, got plan=%q ends=%q", who, planKey, endsAt)
	}
}

// TestSignupTrial_PasswordRegister — the original behaviour, pinned so the
// refactor that unified the insert can't quietly drop it.
func TestSignupTrial_PasswordRegister(t *testing.T) {
	ts, _, d, _ := newAuthServerFull(t)

	resp := authPost(t, ts, "/api/auth/register",
		`{"email":"sam@example.com","username":"sam","password":"hunter2hunter"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: status=%d want 201", resp.StatusCode)
	}
	assertTrialGranted(t, d, userIDByEmail(t, d, "sam@example.com"), "password signup")
}

// registerFakeSocial points an instance-wide social provider at the fake IdP so
// the real /start → /callback flow can be driven for a social login.
func registerFakeSocial(t *testing.T, srv *Server, idp *fakeOIDC, origin, name string) {
	t.Helper()
	p, err := buildOIDCProvider(context.Background(), origin, name, strings.ToUpper(name), idp.URL,
		"test-client", "test-secret", []string{oidc.ScopeOpenID, "email", "profile"}, false, nil)
	if err != nil {
		t.Fatalf("build fake %s provider: %v", name, err)
	}
	srv.sso.social[name] = p
}

// runSocialSSO drives /start then /callback for an instance-wide social
// provider, returning the callback response (redirects disabled).
func runSocialSSO(t *testing.T, ts *httptest.Server, srv *Server, idp *fakeOIDC, client *http.Client, name, sub, email string) *http.Response {
	t.Helper()
	r1, err := client.Get(ts.URL + "/api/auth/sso/" + name + "/start")
	if err != nil {
		t.Fatal(err)
	}
	r1.Body.Close()
	if r1.StatusCode != http.StatusFound {
		t.Fatalf("/start: want 302, got %d", r1.StatusCode)
	}
	st := stateFromResponse(t, srv, r1)
	// email_verified must be true — a social provider that doesn't vouch for the
	// address is refused before any account is created.
	idp.setIDToken(idp.mintToken(t, "test-client", sub, email, true, st.Nonce))

	r2, err := client.Get(ts.URL + "/api/auth/sso/" + name + "/callback?code=xyz&state=" + url.QueryEscape(st.Token))
	if err != nil {
		t.Fatal(err)
	}
	return r2
}

// TestSignupTrial_SocialSSOGrants — the bug this file exists for: a Google /
// GitHub signup creates itself exactly like a password one, so it gets the same
// trial. Also pins that a RETURNING social user is neither re-granted nor
// extended (the trial is a signup event, not a login one).
func TestSignupTrial_SocialSSOGrants(t *testing.T) {
	t.Setenv("TELA_SHARE_SECRET", "tela-test-share-secret-fixed-32-byte!")
	idp := startFakeOIDC(t)
	d := newAPITestDB(t)
	handler, srv := HandlerWithServer(d)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	registerFakeSocial(t, srv, idp, ts.URL, "google")

	r := runSocialSSO(t, ts, srv, idp, noRedirJarClient(t), "google", "ext-neo", "neo@gmail.test")
	r.Body.Close()
	if !hasCookie(r, auth.CookieName) {
		t.Fatalf("social callback did not sign the user in (status %d, %q)", r.StatusCode, r.Header.Get("Location"))
	}
	uid := userIDByEmail(t, d, "neo@gmail.test")
	if uid == 0 {
		t.Fatal("social SSO user was not provisioned")
	}
	assertTrialGranted(t, d, uid, "google signup")

	// Returning user: same account, and the trial window is untouched — a login
	// must not mint a second trial or push the end date out.
	_, endsBefore := trialOf(t, d, uid)
	mustExec(t, d, `UPDATE users SET trial_ends_at = '2030-01-01 00:00:00' WHERE id = $1`, uid)
	r2 := runSocialSSO(t, ts, srv, idp, noRedirJarClient(t), "google", "ext-neo", "neo@gmail.test")
	r2.Body.Close()
	if got := userIDByEmail(t, d, "neo@gmail.test"); got != uid {
		t.Fatalf("returning social user remapped: was %d now %d", uid, got)
	}
	if _, endsAfter := trialOf(t, d, uid); endsAfter != "2030-01-01 00:00:00" {
		t.Fatalf("re-login rewrote the trial window: %q → %q (original %q)", "2030-01-01 00:00:00", endsAfter, endsBefore)
	}
}

// TestSignupTrial_SocialSSOLinkLeavesExistingAccountAlone — when a verified
// social email matches an existing account, resolveSSOUser links the identity
// instead of creating a user. That branch must not touch the account's plan
// state: a trial CASE outranks the base plan_key in planFor, so writing one
// onto an established (possibly paying) account would DOWNGRADE it for ~37 days.
func TestSignupTrial_SocialSSOLinkLeavesExistingAccountAlone(t *testing.T) {
	t.Setenv("TELA_SHARE_SECRET", "tela-test-share-secret-fixed-32-byte!")
	idp := startFakeOIDC(t)
	d := newAPITestDB(t)
	handler, srv := HandlerWithServer(d)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	registerFakeSocial(t, srv, idp, ts.URL, "google")

	// A settled account: trial long expired, now on a paid plan.
	var uid int64
	hash, _ := auth.HashPassword("password123")
	mustQueryRow(t, d, `INSERT INTO users (username, email, email_verified_at, password_hash, is_active,
		plan_key, trial_plan_key, trial_ends_at)
		VALUES ('trinity','trinity@gmail.test',tela_now(),$1,1,'personal_unlimited','personal_plus','2020-01-01 00:00:00')
		RETURNING id`, &uid, hash)
	before := countUsers(t, d)

	r := runSocialSSO(t, ts, srv, idp, noRedirJarClient(t), "google", "ext-trinity", "trinity@gmail.test")
	r.Body.Close()
	if after := countUsers(t, d); after != before {
		t.Fatalf("auto-link created a new user: %d → %d", before, after)
	}
	assertIdentity(t, d, "google", "ext-trinity", uid)
	planKey, endsAt := trialOf(t, d, uid)
	if planKey != "personal_plus" || endsAt != "2020-01-01 00:00:00" {
		t.Fatalf("linking rewrote an existing account's trial: plan=%q ends=%q", planKey, endsAt)
	}
	var basePlan string
	mustQueryRow(t, d, `SELECT plan_key FROM users WHERE id = $1`, &basePlan, uid)
	if basePlan != "personal_unlimited" {
		t.Fatalf("linking rewrote base plan_key: %q", basePlan)
	}
}

// TestSignupTrial_OrgSSODoesNotGrant — an org's own SSO provisions accounts FOR
// people (they work inside the org's plan), so no personal trial. Same INSERT as
// the social path, so the discriminator is worth a test of its own.
func TestSignupTrial_OrgSSODoesNotGrant(t *testing.T) {
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

	r := runOrgSSO(t, ts, srv, idp, noRedirJarClient(t), "acme.test", "ext-neo", "neo@acme.test", true, "")
	r.Body.Close()
	uid := userIDByEmail(t, d, "neo@acme.test")
	if uid == 0 {
		t.Fatal("org SSO user was not provisioned")
	}
	assertNoTrial(t, d, uid, "org SSO signup")
	assertIdentity(t, d, fmt.Sprintf("org:%d", orgID), "ext-neo", uid)
}

// TestSignupTrial_AdminCreatedUserHasNone — provisioned by an operator, not
// self-created. On a self-hosted instance a trial banner on every admin-made
// account would be noise.
func TestSignupTrial_AdminCreatedUserHasNone(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	adminID := seedUser(t, d, "admin", "adminpw123", true)

	req := userRequest(http.MethodPost, "/api/admin/users",
		`{"username":"alice","email":"alice@example.com","password":"alicepw123"}`,
		authUser(adminID, "admin", true))
	rec := recordHandler(srv.CreateAdminUser, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status=%d body=%q want 201", rec.Code, rec.Body.String())
	}
	uid := userIDByEmail(t, d, "alice@example.com")
	if uid == 0 {
		t.Fatal("admin-created user not found")
	}
	assertNoTrial(t, d, uid, "admin-created user")
	// The email an admin sets is pre-confirmed — that behaviour moved into the
	// shared insert, so pin it here too.
	var verified sql.NullString
	if err := d.QueryRow(`SELECT email_verified_at FROM users WHERE id = $1`, uid).Scan(&verified); err != nil {
		t.Fatal(err)
	}
	if !verified.Valid || verified.String == "" {
		t.Fatal("admin-created user with an email should be pre-verified")
	}
}

// TestUserInsertSites_AllAccountedFor is the guard that makes the policy hold
// for code nobody has written yet. The trial went missing because "create a
// user" was six INSERTs with no single definition; this fails the build when a
// seventh appears, so the author has to come to users_create.go and decide.
func TestUserInsertSites_AllAccountedFor(t *testing.T) {
	// path → why it may write its own INSERT INTO users.
	allowed := map[string]string{
		filepath.Join("internal", "api", "users_create.go"): "the shared insert — the one place that decides the trial",
		filepath.Join("internal", "api", "setup.go"):        "first-run wizard: INSERT…WHERE NOT EXISTS under an advisory lock; never grants a trial",
		filepath.Join("internal", "auth", "bootstrap.go"):   "first-boot admin from env, in-tx with a space_members backfill; never grants a trial",
		filepath.Join("cmd", "tela", "admincli.go"):         "create-admin recovery CLI (package main, can't reach the api helper); never grants a trial",
	}
	re := regexp.MustCompile(`INSERT\s+INTO\s+users\b`)
	var stray []string
	// Rooted at the backend module dir so cmd/ is covered too, not just internal/.
	err := filepath.Walk("../..", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !re.Match(b) {
			return nil
		}
		// path is relative to internal/api; normalise to backend-root-relative.
		rel := filepath.Clean(strings.TrimPrefix(filepath.ToSlash(path), "../../"))
		if _, ok := allowed[rel]; !ok {
			stray = append(stray, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk backend sources: %v", err)
	}
	if len(stray) > 0 {
		t.Fatalf("new INSERT INTO users outside the shared helper: %v\n"+
			"Route it through insertUser() in internal/api/users_create.go, or — if it genuinely needs its own SQL — "+
			"add it to the allow-list there WITH the decision it makes about the signup trial.", stray)
	}
}
