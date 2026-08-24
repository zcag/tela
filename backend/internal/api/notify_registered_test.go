package api

import (
	"fmt"
	"net/http/httptest"
	"testing"
)

// A signup notification is owed to the instance admins once per account that
// CREATED ITSELF — and to no one for an account that merely signed in. The
// failure these pin was silent for ten weeks: notifyUserRegistered had exactly
// one caller, on the password path, so 61 of the 62 SSO signups were never
// announced. The single notified one was an auto-LINK, which is the other half
// of the policy: linking an identity onto an account that registered by
// password months ago must not announce it as new.
//
// The trial policy in users_create_test.go asks the same "signup or not?"
// question at the row level; these ask it at the notification level.

// registeredCount is how many signup notifications an admin holds for anyone.
func registeredCount(t *testing.T, srv *Server, adminID int64) int {
	t.Helper()
	return notifCountByType(t, srv.DB, adminID, string(notifUserRegistered))
}

// ssoTestServer sets up an instance with one admin, a fake IdP, and a running
// server — the common preamble of every test below.
func ssoTestServer(t *testing.T) (*httptest.Server, *Server, *fakeOIDC, int64) {
	t.Helper()
	t.Setenv("TELA_SHARE_SECRET", "tela-test-share-secret-fixed-32-byte!")
	idp := startFakeOIDC(t)
	d := newAPITestDB(t)
	handler, srv := HandlerWithServer(d)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	admin := seedUser(t, d, "operator", "operatorpw1", true)
	return ts, srv, idp, admin
}

// TestNotifyRegistered_SocialSSOSignup — the bug itself: a Google/GitHub signup
// creates an account exactly like a password one, so the admins hear about it.
func TestNotifyRegistered_SocialSSOSignup(t *testing.T) {
	ts, srv, idp, admin := ssoTestServer(t)
	registerFakeSocial(t, srv, idp, ts.URL, "google")

	r := runSocialSSO(t, ts, srv, idp, noRedirJarClient(t), "google", "ext-neo", "neo@gmail.test")
	r.Body.Close()
	if uid := userIDByEmail(t, srv.DB, "neo@gmail.test"); uid == 0 {
		t.Fatal("social SSO user was not provisioned")
	}
	if n := registeredCount(t, srv, admin); n != 1 {
		t.Fatalf("google signup: want 1 user_registered notification, got %d", n)
	}
}

// TestNotifyRegistered_ReturningSocialUserIsSilent — a login is not a signup.
// The dedup key would also catch a repeat, so this pins the `created` flag
// rather than the dedup: it must never reach the notification at all.
func TestNotifyRegistered_ReturningSocialUserIsSilent(t *testing.T) {
	ts, srv, idp, admin := ssoTestServer(t)
	registerFakeSocial(t, srv, idp, ts.URL, "google")

	runSocialSSO(t, ts, srv, idp, noRedirJarClient(t), "google", "ext-neo", "neo@gmail.test").Body.Close()
	runSocialSSO(t, ts, srv, idp, noRedirJarClient(t), "google", "ext-neo", "neo@gmail.test").Body.Close()

	if n := registeredCount(t, srv, admin); n != 1 {
		t.Fatalf("second login re-announced the account: want 1, got %d", n)
	}
}

// TestNotifyRegistered_EmailLinkIsSilent — outcome (2) of resolveSSOUser. An
// established account adding Google to its login options is not a new user, and
// announcing it would have been the one wrong row in production: user 100
// registered by password, was notified, then linked Google 53 s later.
func TestNotifyRegistered_EmailLinkIsSilent(t *testing.T) {
	ts, srv, idp, admin := ssoTestServer(t)
	registerFakeSocial(t, srv, idp, ts.URL, "google")

	// An account that already exists, registered some other way.
	existing := seedUser(t, srv.DB, "trinity", "trinitypw12", false)
	mustExec(t, srv.DB, `UPDATE users SET email = 'trinity@gmail.test', email_verified_at = tela_now() WHERE id = $1`, existing)
	before := registeredCount(t, srv, admin)

	r := runSocialSSO(t, ts, srv, idp, noRedirJarClient(t), "google", "ext-trinity", "trinity@gmail.test")
	r.Body.Close()

	if got := userIDByEmail(t, srv.DB, "trinity@gmail.test"); got != existing {
		t.Fatalf("link branch created a second account: was %d now %d", existing, got)
	}
	if n := registeredCount(t, srv, admin); n != before {
		t.Fatalf("linking an identity onto an existing account announced it: %d → %d", before, n)
	}
}

// TestNotifyRegistered_OrgSSOSignup — an org connection provisions the account,
// so it gets no personal trial; but it IS a new account on this instance, which
// is what the notification reports. The two policies diverge here on purpose.
func TestNotifyRegistered_OrgSSOSignup(t *testing.T) {
	ts, srv, idp, admin := ssoTestServer(t)

	orgID := seedOrg(t, srv.DB, "Acme", "acme")
	mustExec(t, srv.DB, `INSERT INTO org_email_domains (domain, org_id) VALUES ('acme.test', $1)`, orgID)
	mustExec(t, srv.DB, `INSERT INTO org_sso (org_id, issuer, client_id, client_secret, enforced)
		VALUES ($1, $2, 'test-client', 'test-secret', 0)`, orgID, idp.URL)
	// Org SSO is an enterprise feature; without the plan /start 404s.
	mustExec(t, srv.DB, `UPDATE orgs SET plan_key = 'org_enterprise' WHERE id = $1`, orgID)

	r := runOrgSSO(t, ts, srv, idp, noRedirJarClient(t), "acme.test", "ext-neo", "neo@acme.test", true, "")
	r.Body.Close()
	uid := userIDByEmail(t, srv.DB, "neo@acme.test")
	if uid == 0 {
		t.Fatal("org SSO user was not provisioned")
	}
	assertIdentity(t, srv.DB, fmt.Sprintf("org:%d", orgID), "ext-neo", uid)
	if n := registeredCount(t, srv, admin); n != 1 {
		t.Fatalf("org SSO signup: want 1 user_registered notification, got %d", n)
	}
}
