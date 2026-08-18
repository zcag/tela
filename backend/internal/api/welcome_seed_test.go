package api

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestMain disables welcome-page seeding for the whole api test package so the
// many space-creation tests keep asserting on exact page sets. The seed test
// below re-enables it on its own server instance.
func TestMain(m *testing.M) {
	os.Setenv("TELA_DISABLE_WELCOME_SEED", "1")
	// The api suite exercises the managed-cloud product, where the account's plan
	// flag is an authoritative entitlement. Self-host mode (plan flag does NOT
	// grant ee features) is covered explicitly in TestEntitledViaLicense, which
	// builds its Server directly and leaves managedCloud at its false zero value.
	os.Setenv("TELA_CLOUD", "1")
	os.Exit(m.Run())
}

func TestCreateSpace_SeedsWelcomePage(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	srv.seedWelcome = true // re-enable for this test (TestMain turned it off)

	uid := seedUser(t, d, "alice", "alicepw123", false)
	u := authUser(uid, "alice", false)

	rec := routedRecorder("POST /api/spaces", srv.CreateSpace,
		userRequest(http.MethodPost, "/api/spaces", `{"name":"Engineering"}`, u))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create space: code=%d body=%q", rec.Code, rec.Body.String())
	}

	var title, body string
	err := d.QueryRowContext(context.Background(),
		`SELECT title, body FROM pages WHERE space_id = (SELECT id FROM spaces WHERE slug = 'engineering')`).
		Scan(&title, &body)
	if err != nil {
		t.Fatalf("welcome page not found: %v", err)
	}
	if title != "Welcome to Engineering" {
		t.Fatalf("title=%q want 'Welcome to Engineering'", title)
	}
	if !strings.Contains(body, "home of **Engineering**") {
		t.Fatalf("unexpected welcome body: %q", body)
	}
}

func TestCreateSpace_NoSeedWhenDisabled(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d) // seeding disabled by TestMain's env

	uid := seedUser(t, d, "bob", "bobpw12345", false)
	u := authUser(uid, "bob", false)

	rec := routedRecorder("POST /api/spaces", srv.CreateSpace,
		userRequest(http.MethodPost, "/api/spaces", `{"name":"Ops"}`, u))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create space: code=%d body=%q", rec.Code, rec.Body.String())
	}
	var n int
	if err := d.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM pages WHERE space_id = (SELECT id FROM spaces WHERE slug = 'ops')`).Scan(&n); err != nil {
		t.Fatalf("count pages: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected no seeded page when disabled, got %d", n)
	}
}

// registerAndVerify drives the real signup path — register, pull the token out
// of the captured email, confirm — and returns the new user's id. The personal
// space is provisioned (and seeded) as a side effect of confirmation.
func registerAndVerify(t *testing.T, ts *httptest.Server, cm *captureMailer, d *sql.DB, username, email string) int64 {
	t.Helper()
	resp := authPost(t, ts, "/api/auth/register",
		`{"email":"`+email+`","username":"`+username+`","password":"hunter2hunter"}`)
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("register: status=%d body=%s", resp.StatusCode, b)
	}
	resp.Body.Close()

	msg, ok := cm.last()
	if !ok {
		t.Fatal("no verification email captured")
	}
	vresp := authPost(t, ts, "/api/auth/verify-email", `{"token":"`+tokenFromMessage(t, msg)+`"}`)
	if vresp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(vresp.Body)
		t.Fatalf("verify: status=%d body=%s", vresp.StatusCode, b)
	}
	vresp.Body.Close()

	var uid int64
	if err := d.QueryRowContext(context.Background(),
		`SELECT id FROM users WHERE username = $1`, username).Scan(&uid); err != nil {
		t.Fatalf("load user: %v", err)
	}
	return uid
}

// personalSpaceOf returns the user's personal space id, failing if they have none.
func personalSpaceOf(t *testing.T, d *sql.DB, uid int64) int64 {
	t.Helper()
	var id int64
	if err := d.QueryRowContext(context.Background(),
		`SELECT id FROM spaces WHERE personal_user_id = $1`, uid).Scan(&id); err != nil {
		t.Fatalf("personal space for user %d: %v", uid, err)
	}
	return id
}

func pageTitlesIn(t *testing.T, d *sql.DB, spaceID int64) []string {
	t.Helper()
	rows, err := d.QueryContext(context.Background(),
		`SELECT title FROM pages WHERE space_id = $1 ORDER BY id`, spaceID)
	if err != nil {
		t.Fatalf("list pages: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan title: %v", err)
		}
		out = append(out, s)
	}
	return out
}

// TestRegister_SeedsPersonalWelcomePage is the regression this whole change
// exists for: a brand-new signup used to land on the "No pages in Personal"
// empty state, because EnsurePersonalSpace seeds nothing.
func TestRegister_SeedsPersonalWelcomePage(t *testing.T) {
	ts, cm, d, srv := newAuthServerFull(t)
	srv.seedWelcome = true // re-enable for this test (TestMain turned it off)

	uid := registerAndVerify(t, ts, cm, d, "sam", "sam@example.com")
	spaceID := personalSpaceOf(t, d, uid)

	titles := pageTitlesIn(t, d, spaceID)
	if len(titles) != 1 || titles[0] != "Welcome to tela" {
		t.Fatalf("personal space pages = %v, want exactly [Welcome to tela]", titles)
	}

	var body string
	if err := d.QueryRowContext(context.Background(),
		`SELECT body FROM pages WHERE space_id = $1`, spaceID).Scan(&body); err != nil {
		t.Fatalf("load seeded body: %v", err)
	}
	if !strings.Contains(body, "your personal space") {
		t.Fatalf("unexpected personal welcome body: %q", body)
	}
	// The page must go through createPageCore like any other, or it won't be
	// searchable or link-synced.
	var tsv int
	if err := d.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM pages WHERE space_id = $1 AND search_tsv IS NOT NULL`, spaceID).Scan(&tsv); err != nil {
		t.Fatalf("check search_tsv: %v", err)
	}
	if tsv != 1 {
		t.Fatal("seeded personal page has no search_tsv — it bypassed createPageCore")
	}
}

// TestSeedPersonalWelcomePage_Idempotent — the seed must never stack a second
// starter page, including after the user deletes the first one (pages
// soft-delete, and the count deliberately sees deleted rows).
func TestSeedPersonalWelcomePage_Idempotent(t *testing.T) {
	ts, cm, d, srv := newAuthServerFull(t)
	srv.seedWelcome = true

	uid := registerAndVerify(t, ts, cm, d, "sam", "sam@example.com")
	spaceID := personalSpaceOf(t, d, uid)

	srv.seedPersonalWelcomePage(context.Background(), uid, "sam", spaceID)
	if titles := pageTitlesIn(t, d, spaceID); len(titles) != 1 {
		t.Fatalf("second seed stacked a page: %v", titles)
	}

	// Delete it the way the app does, then seed again: it stays gone.
	if _, err := d.ExecContext(context.Background(),
		`UPDATE pages SET deleted_at = tela_now() WHERE space_id = $1`, spaceID); err != nil {
		t.Fatalf("soft-delete page: %v", err)
	}
	srv.seedPersonalWelcomePage(context.Background(), uid, "sam", spaceID)
	var live int
	if err := d.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM pages WHERE space_id = $1 AND deleted_at IS NULL`, spaceID).Scan(&live); err != nil {
		t.Fatalf("count live pages: %v", err)
	}
	if live != 0 {
		t.Fatalf("seed resurrected a deleted starter page (%d live)", live)
	}
}

// TestRegister_NoPersonalSeedWhenDisabled — TELA_DISABLE_WELCOME_SEED covers the
// new signup path too, which is what keeps the rest of the api suite's
// exact-page-set assertions green.
func TestRegister_NoPersonalSeedWhenDisabled(t *testing.T) {
	ts, cm, d, _ := newAuthServerFull(t) // seeding disabled by TestMain's env

	uid := registerAndVerify(t, ts, cm, d, "bob", "bob@example.com")
	if titles := pageTitlesIn(t, d, personalSpaceOf(t, d, uid)); len(titles) != 0 {
		t.Fatalf("expected no seeded page when disabled, got %v", titles)
	}
}

// TestSSO_SeedsPersonalWelcomePage — the other signup path. signInSSO is shared
// by social and org SSO, so one org flow covers both; a second login must not
// stack a second starter page.
func TestSSO_SeedsPersonalWelcomePage(t *testing.T) {
	t.Setenv("TELA_SHARE_SECRET", "tela-test-share-secret-fixed-32-byte!")
	idp := startFakeOIDC(t)

	d := newAPITestDB(t)
	handler, srv := HandlerWithServer(d)
	srv.seedWelcome = true // re-enable for this test (TestMain turned it off)
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
		t.Fatal("SSO user was not provisioned")
	}
	spaceID := personalSpaceOf(t, d, uid)
	if titles := pageTitlesIn(t, d, spaceID); len(titles) != 1 || titles[0] != "Welcome to tela" {
		t.Fatalf("personal space pages = %v, want exactly [Welcome to tela]", titles)
	}

	// Logging in again is another trip through signInSSO — still one page.
	r2 := runOrgSSO(t, ts, srv, idp, noRedirJarClient(t), "acme.test", "ext-neo", "neo@acme.test", true, "")
	r2.Body.Close()
	if titles := pageTitlesIn(t, d, spaceID); len(titles) != 1 {
		t.Fatalf("second SSO login stacked a page: %v", titles)
	}
}
