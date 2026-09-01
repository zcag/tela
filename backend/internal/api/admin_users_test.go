package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// The admin users list is newest-account-first (most recent signup on top), not
// alphabetical — the operator wants to see who just joined.
func TestListAdminUsers_NewestFirst(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	adminID := seedUser(t, d, "admin", "adminpw123", true)
	charlieID := seedUser(t, d, "charlie", "charliepw", false)
	bobID := seedUser(t, d, "bob", "bobpw1234", false)

	// Stamp distinct created_at so the ordering is unambiguous (admin oldest, bob
	// newest) regardless of same-second insert ties.
	for _, s := range []struct {
		id int64
		ts string
	}{
		{adminID, "2026-01-01 00:00:00"},
		{charlieID, "2026-02-01 00:00:00"},
		{bobID, "2026-03-01 00:00:00"},
	} {
		if _, err := d.Exec(`UPDATE users SET created_at=$1 WHERE id=$2`, s.ts, s.id); err != nil {
			t.Fatalf("stamp created_at: %v", err)
		}
	}

	req := userRequest(http.MethodGet, "/api/admin/users", "", authUser(adminID, "admin", true))
	rec := recordHandler(srv.ListAdminUsers, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q want 200", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	bobIdx := strings.Index(body, `"bob"`)
	charlieIdx := strings.Index(body, `"charlie"`)
	adminIdx := strings.Index(body, `"admin"`)
	if adminIdx < 0 || bobIdx < 0 || charlieIdx < 0 {
		t.Fatalf("missing user(s) in body=%q", body)
	}
	// Newest first: bob (Mar) < charlie (Feb) < admin (Jan) by position.
	if !(bobIdx < charlieIdx && charlieIdx < adminIdx) {
		t.Fatalf("ordering wrong (want newest first): bob=%d charlie=%d admin=%d body=%q", bobIdx, charlieIdx, adminIdx, body)
	}
}

func TestListAdminUsers_IncludesUsage(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	adminID := seedUser(t, d, "admin", "adminpw123", true)

	req := userRequest(http.MethodGet, "/api/admin/users", "", authUser(adminID, "admin", true))
	rec := recordHandler(srv.ListAdminUsers, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q want 200", rec.Code, rec.Body.String())
	}
	var out struct {
		Users []adminUserDTO `json:"users"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v body=%q", err, rec.Body.String())
	}
	if len(out.Users) != 1 {
		t.Fatalf("want 1 user, got %d", len(out.Users))
	}
	u := out.Users[0]
	// Usage is enriched from buildUsage; a fresh account has no created spaces
	// (the personal home is excluded from the space count) and no stored files.
	if u.Usage == nil {
		t.Fatalf("usage not populated: %+v", u)
	}
	if u.Usage.Spaces != 0 || u.Usage.StorageBytes != 0 {
		t.Fatalf("unexpected fresh-account usage: %+v", u.Usage)
	}
}

// The admin activity view is instance-wide: an admin sees a user's edits even
// in a space the admin isn't a member of (no space_access gate). That's the
// whole point of #3 — and why it's instance-admin-only.
func TestListUserActivity_AdminSeesUserEditsInstanceWide(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	ctx := context.Background()

	admin := seedUser(t, d, "admin", "adminpw123", true)
	alice := seedUser(t, d, "alice", "alicepw123", false)
	// Space owned by alice; the admin is NOT a member.
	spaceID := seedSpace(t, d, "Alice Space", "alice-space", alice)
	au := authUser(alice, "alice", false)
	if _, ae := srv.createPageCore(ctx, au, nil, pageCreateRequest{SpaceID: spaceID, Title: "Alice Note", Body: "x"}, true); ae != nil {
		t.Fatalf("create: %v", ae)
	}

	rec := routedRecorder("GET /api/admin/users/{id}/activity", srv.ListUserActivity,
		userRequest(http.MethodGet, "/api/admin/users/"+strconv.FormatInt(alice, 10)+"/activity", "", authUser(admin, "admin", true)))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Alice Note") {
		t.Fatalf("admin didn't see alice's edit instance-wide: %q", rec.Body.String())
	}
}

func TestListUserActivity_NonAdmin_Forbidden(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	uid := seedUser(t, d, "user", "userpw1234", false)
	rec := routedRecorder("GET /api/admin/users/{id}/activity", srv.ListUserActivity,
		userRequest(http.MethodGet, "/api/admin/users/"+strconv.FormatInt(uid, 10)+"/activity", "", authUser(uid, "user", false)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d want 403 body=%q", rec.Code, rec.Body.String())
	}
}

func TestListAdminUsers_NonAdmin_Forbidden(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	uid := seedUser(t, d, "user", "userpw1234", false)

	req := userRequest(http.MethodGet, "/api/admin/users", "", authUser(uid, "user", false))
	rec := recordHandler(srv.ListAdminUsers, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403 body=%q", rec.Code, rec.Body.String())
	}
}

func TestCreateAdminUser_OkAndDuplicateConflict(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	adminID := seedUser(t, d, "admin", "adminpw123", true)

	req := userRequest(http.MethodPost, "/api/admin/users",
		`{"username":"alice","password":"alicepw123"}`,
		authUser(adminID, "admin", true))
	rec := recordHandler(srv.CreateAdminUser, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first create: status=%d body=%q want 201", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"username":"alice"`) {
		t.Fatalf("first create: body missing alice: %q", rec.Body.String())
	}

	req2 := userRequest(http.MethodPost, "/api/admin/users",
		`{"username":"alice","password":"differentpw"}`,
		authUser(adminID, "admin", true))
	rec2 := recordHandler(srv.CreateAdminUser, req2)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("duplicate: status=%d body=%q want 409", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), `"code":"conflict"`) {
		t.Fatalf("duplicate: missing code=conflict body=%q", rec2.Body.String())
	}
}

func TestCreateAdminUser_RejectsShortPassword(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	adminID := seedUser(t, d, "admin", "adminpw123", true)

	req := userRequest(http.MethodPost, "/api/admin/users",
		`{"username":"alice","password":"short"}`,
		authUser(adminID, "admin", true))
	rec := recordHandler(srv.CreateAdminUser, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%q", rec.Code, rec.Body.String())
	}
}

func TestPatchAdminUser_PasswordResetWipesSessions(t *testing.T) {
	ctx := context.Background()
	d := newAPITestDB(t)
	srv := New(d)
	adminID := seedUser(t, d, "admin", "adminpw123", true)
	bobID := seedUser(t, d, "bob", "bobpw1234", false)

	seedSession(t, d, bobID)
	seedSession(t, d, bobID)
	seedSession(t, d, adminID)

	req := userRequest(http.MethodPatch, "/api/admin/users/"+intStr(bobID),
		`{"password":"brand-new-pw"}`,
		authUser(adminID, "admin", true))
	rec := routedRecorder("PATCH /api/admin/users/{id}", srv.PatchAdminUser, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q want 200", rec.Code, rec.Body.String())
	}

	var bobSessions int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE user_id = $1`, bobID).Scan(&bobSessions); err != nil {
		t.Fatalf("count bob sessions: %v", err)
	}
	if bobSessions != 0 {
		t.Fatalf("bob has %d sessions after pw reset, want 0", bobSessions)
	}
	var adminSessions int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE user_id = $1`, adminID).Scan(&adminSessions); err != nil {
		t.Fatalf("count admin sessions: %v", err)
	}
	if adminSessions != 1 {
		t.Fatalf("admin sessions=%d, want 1", adminSessions)
	}
}

func TestPatchAdminUser_RejectsSelfTarget(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	adminID := seedUser(t, d, "admin", "adminpw123", true)

	req := userRequest(http.MethodPatch, "/api/admin/users/"+intStr(adminID),
		`{"is_active":false}`,
		authUser(adminID, "admin", true))
	rec := routedRecorder("PATCH /api/admin/users/{id}", srv.PatchAdminUser, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%q want 400", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "cannot modify self") {
		t.Fatalf("body=%q missing self-target message", rec.Body.String())
	}
}

func TestPatchAdminUser_LastAdminSafeguard(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	adminA := seedUser(t, d, "admin-a", "adminpw123", true)
	adminB := seedUser(t, d, "admin-b", "adminpw123", true)

	// Deactivate admin-b out-of-band so admin-a is the only ACTIVE admin.
	// Then have admin-b (still flagged is_instance_admin=1, test-bypassed
	// middleware) try to demote admin-a — that would leave zero active
	// admins and must be refused with last_admin.
	if _, err := d.Exec(`UPDATE users SET is_active = 0 WHERE id = $1`, adminB); err != nil {
		t.Fatalf("deactivate admin-b: %v", err)
	}

	req := userRequest(http.MethodPatch, "/api/admin/users/"+intStr(adminA),
		`{"is_instance_admin":false}`,
		authUser(adminB, "admin-b", true))
	rec := routedRecorder("PATCH /api/admin/users/{id}", srv.PatchAdminUser, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%q want 400", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"code":"last_admin"`) {
		t.Fatalf("missing last_admin code: %q", rec.Body.String())
	}
}

func TestDeleteAdminUser_SoftDeleteAndSessionWipe(t *testing.T) {
	ctx := context.Background()
	d := newAPITestDB(t)
	srv := New(d)
	adminID := seedUser(t, d, "admin", "adminpw123", true)
	bobID := seedUser(t, d, "bob", "bobpw1234", false)
	seedSession(t, d, bobID)
	seedSession(t, d, bobID)

	req := userRequest(http.MethodDelete, "/api/admin/users/"+intStr(bobID), "",
		authUser(adminID, "admin", true))
	rec := routedRecorder("DELETE /api/admin/users/{id}", srv.DeleteAdminUser, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%q want 204", rec.Code, rec.Body.String())
	}

	var active int
	if err := d.QueryRowContext(ctx, `SELECT is_active FROM users WHERE id = $1`, bobID).Scan(&active); err != nil {
		t.Fatalf("read bob: %v", err)
	}
	if active != 0 {
		t.Fatalf("bob is_active=%d, want 0", active)
	}
	var sessions int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE user_id = $1`, bobID).Scan(&sessions); err != nil {
		t.Fatalf("count bob sessions: %v", err)
	}
	if sessions != 0 {
		t.Fatalf("bob sessions=%d, want 0", sessions)
	}
}

func TestDeleteAdminUser_Idempotent(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	adminID := seedUser(t, d, "admin", "adminpw123", true)
	bobID := seedUser(t, d, "bob", "bobpw1234", false)

	req := userRequest(http.MethodDelete, "/api/admin/users/"+intStr(bobID), "",
		authUser(adminID, "admin", true))
	rec := routedRecorder("DELETE /api/admin/users/{id}", srv.DeleteAdminUser, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("first delete: status=%d body=%q", rec.Code, rec.Body.String())
	}
	rec2 := routedRecorder("DELETE /api/admin/users/{id}", srv.DeleteAdminUser,
		userRequest(http.MethodDelete, "/api/admin/users/"+intStr(bobID), "",
			authUser(adminID, "admin", true)))
	if rec2.Code != http.StatusNoContent {
		t.Fatalf("second delete: status=%d body=%q want 204 (idempotent)", rec2.Code, rec2.Body.String())
	}
}

// intStr is a tiny strconv.FormatInt alias used throughout the M6.2 tests
// to build path strings like /api/admin/users/{id}.
func intStr(id int64) string { return strconv.FormatInt(id, 10) }

// The People table's activity columns are window-scoped: an old edit counts
// under ?window=all but must fall outside 1m, and every row carries a metrics
// block even when the account did nothing.
func TestListAdminUsers_WindowedMetrics(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	ctx := context.Background()

	admin := seedUser(t, d, "admin", "adminpw123", true)
	alice := seedUser(t, d, "alice", "alicepw123", false)
	seedUser(t, d, "idle", "idlepw1234", false) // never does anything — the all-zero row
	spaceID := seedSpace(t, d, "Alice Space", "alice-space", alice)
	oldPage := seedPage(t, d, spaceID, "Alice Note")
	newPage := seedPage(t, d, spaceID, "Fresh Note")

	// oldPage was created a year ago (agent-written) and edited today; newPage
	// was created today. So the window decides BOTH how many edits alice has
	// and how many pages she counts as having created.
	if _, err := d.ExecContext(ctx,
		`INSERT INTO page_revisions (page_id, title, body, author_id, source, byte_size, created_at)
		 VALUES ($1,'Alice Note','y',$3,'agent', 1, '2025-01-01 00:00:00'),
		        ($1,'Alice Note','x',$3,'manual',1, tela_now()),
		        ($2,'Fresh Note','z',$3,'create',1, tela_now())`,
		oldPage, newPage, alice); err != nil {
		t.Fatalf("seed revisions: %v", err)
	}
	// One view today, one outside the 1m window.
	if _, err := d.ExecContext(ctx,
		`INSERT INTO events (type, actor_user_id, created_at)
		 VALUES ('page.view',$1, tela_now()), ('page.view',$1,'2025-01-01 00:00:00')`,
		alice); err != nil {
		t.Fatalf("seed events: %v", err)
	}

	get := func(window string) map[string]adminUserDTO {
		t.Helper()
		rec := recordHandler(srv.ListAdminUsers,
			userRequest(http.MethodGet, "/api/admin/users?window="+window, "", authUser(admin, "admin", true)))
		if rec.Code != http.StatusOK {
			t.Fatalf("window=%s status=%d body=%q", window, rec.Code, rec.Body.String())
		}
		var out struct {
			Users  []adminUserDTO `json:"users"`
			Window string         `json:"window"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v body=%q", err, rec.Body.String())
		}
		want := window
		if window == "bogus" {
			want = "1m" // unknown values fall back, never 500 or list everything
		}
		if out.Window != want {
			t.Fatalf("window echo=%q want %q", out.Window, want)
		}
		by := map[string]adminUserDTO{}
		for _, u := range out.Users {
			if u.Metrics == nil {
				t.Fatalf("metrics nil for %q", u.Username)
			}
			by[u.Username] = u
		}
		return by
	}

	month := get("1m")
	if got := month["alice"].Metrics; got.Edits != 2 || got.AgentEdits != 0 || got.Views != 1 {
		t.Fatalf("1m alice metrics = %+v, want edits=2 agent=0 views=1", got)
	}
	// Only newPage was created inside the window; the year-old page's first
	// revision falls outside it, so editing it today doesn't re-count as a
	// creation.
	if got := month["alice"].Metrics.PagesCreated; got != 1 {
		t.Fatalf("1m alice pages_created=%d want 1 (only the page first written in-window)", got)
	}
	if got := month["idle"].Metrics; got.Edits != 0 || got.Views != 0 || got.DaysActive != 0 {
		t.Fatalf("idle account should be all-zero, got %+v", got)
	}

	all := get("all")
	if got := all["alice"].Metrics; got.Edits != 3 || got.AgentEdits != 1 || got.Views != 2 {
		t.Fatalf("all alice metrics = %+v, want edits=3 agent=1 views=2", got)
	}
	if got := all["alice"].Metrics.PagesCreated; got != 2 {
		t.Fatalf("all alice pages_created=%d want 2", got)
	}
	if got := all["alice"].Metrics.DaysActive; got != 2 {
		t.Fatalf("all alice days_active=%d want 2 (two distinct event days)", got)
	}

	get("bogus")
}
