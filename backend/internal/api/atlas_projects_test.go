package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/zcag/tela/backend/internal/secretbox"
)

// ── shared seed helpers (project model) ─────────────────────────────────────

// seedAtlasProject inserts a project and returns its id. outputSpaceID 0 = NULL
// (the output space is created on the first run).
func seedAtlasProject(t *testing.T, d *sql.DB, name, ownerKind string, ownerID, outputSpaceID int64, autoUpdate int) int64 {
	t.Helper()
	var space any
	if outputSpaceID != 0 {
		space = outputSpaceID
	}
	var id int64
	if err := d.QueryRowContext(context.Background(),
		`INSERT INTO atlas_projects (name, owner_kind, owner_id, output_space_id, cadence, auto_update)
		 VALUES ($1,$2,$3,$4,'daily',$5) RETURNING id`,
		name, ownerKind, ownerID, space, autoUpdate).Scan(&id); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return id
}

// seedAtlasCredential inserts an owner-scoped credential and returns its id.
func seedAtlasCredential(t *testing.T, d *sql.DB, ownerKind string, ownerID int64, name, kind, value string, meta map[string]string) int64 {
	t.Helper()
	metaJSON := ""
	if len(meta) > 0 {
		b, _ := json.Marshal(meta)
		metaJSON = string(b)
	}
	// Seal like the real handler does, so seeded rows exercise the encrypted path
	// rather than the legacy-plaintext fallback (that one has its own test).
	sealed, err := secretbox.Seal(value)
	if err != nil {
		t.Fatalf("seal credential: %v", err)
	}
	var id int64
	if err := d.QueryRowContext(context.Background(),
		`INSERT INTO atlas_credentials (owner_kind, owner_id, name, kind, value, meta_json)
		 VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
		ownerKind, ownerID, name, kind, sealed, metaJSON).Scan(&id); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	return id
}

// ── project gating (personal + org) ─────────────────────────────────────────

// TestAtlasProjects_PersonalGating locks the personal-project access model: the
// owner manages, a stranger is denied both manage (create/run/delete) and view.
func TestAtlasProjects_PersonalGating(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	seedUser(t, d, "bob", "bobpw1234", false)
	ca := loginClient(t, ts, "alice", "alicepw12")
	cb := loginClient(t, ts, "bob", "bobpw1234")

	projects := ts.URL + "/api/atlas/projects"

	// Alice creates a personal project, output space deferred to the first run.
	body := fmt.Sprintf(`{"name":"My Docs","owner_kind":"user","owner_id":%d}`, alice)
	st, resp := atlasReq(t, ca, "POST", projects, body)
	if st != http.StatusCreated {
		t.Fatalf("owner create project: status=%d body=%s", st, resp)
	}
	var created struct {
		Project struct {
			ID          int64 `json:"id"`
			OutputSpace *struct {
				ID int64 `json:"id"`
			} `json:"output_space"`
		} `json:"project"`
	}
	if json.Unmarshal([]byte(resp), &created) != nil || created.Project.ID == 0 {
		t.Fatalf("decode created project: %s", resp)
	}
	if created.Project.OutputSpace != nil {
		t.Fatalf("deferred output should be null until first run, got %+v", created.Project.OutputSpace)
	}
	pid := created.Project.ID

	// Bob cannot create a project owned by Alice.
	if st, _ := atlasReq(t, cb, "POST", projects, body); st != http.StatusForbidden {
		t.Fatalf("stranger create as alice: want 403, got %d", st)
	}

	// Bob can't see Alice's project list entry, view it, or manage it.
	st, lb := atlasReq(t, cb, "GET", projects, "")
	if st != http.StatusOK || strings.Contains(lb, `"My Docs"`) {
		t.Fatalf("stranger list leaks alice's project: status=%d body=%s", st, lb)
	}
	getURL := fmt.Sprintf("%s/api/atlas/projects/%d", ts.URL, pid)
	if st, _ := atlasReq(t, cb, "GET", getURL, ""); st != http.StatusForbidden {
		t.Fatalf("stranger get project: want 403, got %d", st)
	}
	if st, _ := atlasReq(t, cb, "DELETE", getURL, ""); st != http.StatusForbidden {
		t.Fatalf("stranger delete project: want 403, got %d", st)
	}

	// Owner sees + can manage it; list flags can_manage true.
	st, lb = atlasReq(t, ca, "GET", projects, "")
	if st != http.StatusOK || !strings.Contains(lb, `"My Docs"`) || !strings.Contains(lb, `"can_manage":true`) {
		t.Fatalf("owner list: status=%d body=%s", st, lb)
	}

	// PATCH cadence (owner) then run with no sources → 400 no_sources (gate passed).
	if st, rb := atlasReq(t, ca, "PATCH", getURL, `{"cadence":"weekly"}`); st != http.StatusOK || !strings.Contains(rb, `"cadence":"weekly"`) {
		t.Fatalf("owner patch cadence: status=%d body=%s", st, rb)
	}
	runURL := getURL + "/run"
	if st, rb := atlasReq(t, ca, "POST", runURL, ""); st != http.StatusBadRequest || !strings.Contains(rb, "no_sources") {
		t.Fatalf("run empty project: want 400 no_sources, got %d %s", st, rb)
	}
	// Stranger run is denied by the manage gate (before the no_sources check).
	if st, _ := atlasReq(t, cb, "POST", runURL, ""); st != http.StatusForbidden {
		t.Fatalf("stranger run: want 403, got %d", st)
	}
}

// TestAtlasProjects_OrgGating locks the org-project access model: an org admin
// manages, a plain member views but cannot manage, a non-member sees nothing.
func TestAtlasProjects_OrgGating(t *testing.T) {
	ts, d := newWiredServer(t)
	admin := seedUser(t, d, "admin", "adminpw12", false)
	member := seedUser(t, d, "member", "memberpw1", false)
	seedUser(t, d, "outsider", "outsiderp", false)
	org := seedOrg(t, d, "Acme", "acme")
	seedOrgMember(t, d, org, admin, orgRoleAdmin)
	seedOrgMember(t, d, org, member, orgRoleMember)

	cAdmin := loginClient(t, ts, "admin", "adminpw12")
	cMember := loginClient(t, ts, "member", "memberpw1")
	cOut := loginClient(t, ts, "outsider", "outsiderp")
	projects := ts.URL + "/api/atlas/projects"
	body := fmt.Sprintf(`{"name":"Acme Docs","owner_kind":"org","owner_id":%d}`, org)

	// Admin creates the org project; a plain member and an outsider cannot.
	st, resp := atlasReq(t, cAdmin, "POST", projects, body)
	if st != http.StatusCreated {
		t.Fatalf("admin create org project: status=%d body=%s", st, resp)
	}
	var created struct {
		Project struct {
			ID int64 `json:"id"`
		} `json:"project"`
	}
	if json.Unmarshal([]byte(resp), &created) != nil || created.Project.ID == 0 {
		t.Fatalf("decode created org project: %s", resp)
	}
	pid := created.Project.ID
	if st, _ := atlasReq(t, cMember, "POST", projects, body); st != http.StatusForbidden {
		t.Fatalf("member create org project: want 403, got %d", st)
	}
	if st, _ := atlasReq(t, cOut, "POST", projects, body); st != http.StatusForbidden {
		t.Fatalf("outsider create org project: want 403, got %d", st)
	}

	getURL := fmt.Sprintf("%s/api/atlas/projects/%d", ts.URL, pid)

	// Member can VIEW (org member) but can_manage is false.
	st, gb := atlasReq(t, cMember, "GET", getURL, "")
	if st != http.StatusOK || !strings.Contains(gb, `"can_manage":false`) {
		t.Fatalf("member get org project: status=%d body=%s", st, gb)
	}
	// Member cannot manage (delete).
	if st, _ := atlasReq(t, cMember, "DELETE", getURL, ""); st != http.StatusForbidden {
		t.Fatalf("member delete org project: want 403, got %d", st)
	}
	// Outsider can't view at all.
	if st, _ := atlasReq(t, cOut, "GET", getURL, ""); st != http.StatusForbidden {
		t.Fatalf("outsider get org project: want 403, got %d", st)
	}
	// Admin manages (delete).
	if st, _ := atlasReq(t, cAdmin, "DELETE", getURL, ""); st != http.StatusNoContent {
		t.Fatalf("admin delete org project: want 204, got %d", st)
	}
}

// TestAtlasOutputSpace_CreateIfMissing checks the create-on-first-run flow: a
// project with no output space materializes one (named after the project, owned
// by the project's owner) on the first run, persisted back onto the project; a
// second resolve reuses it.
func TestAtlasOutputSpace_CreateIfMissing(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	ctx := context.Background()
	owner := seedUser(t, d, "alice", "alicepw12", false)
	pid := seedAtlasProject(t, d, "Widget Docs", accountUser, owner, 0, 0)

	proj, err := loadAtlasProject(ctx, d, pid)
	if err != nil {
		t.Fatalf("load project: %v", err)
	}
	if proj.OutputSpaceID != nil {
		t.Fatalf("output space should start null")
	}

	spaceID, parent, ae := srv.ensureOutputSpace(ctx, proj)
	if ae != nil {
		t.Fatalf("ensureOutputSpace: %+v", ae)
	}
	if spaceID == 0 || parent != nil {
		t.Fatalf("ensureOutputSpace returned space=%d parent=%v", spaceID, parent)
	}

	// The space is named after the project and owned by the project's owner.
	var name string
	if err := d.QueryRow(`SELECT name FROM spaces WHERE id=$1`, spaceID).Scan(&name); err != nil {
		t.Fatalf("load created space: %v", err)
	}
	if name != "Widget Docs" {
		t.Fatalf("created space name = %q, want project name", name)
	}
	var role string
	if err := d.QueryRow(`SELECT role FROM space_members WHERE space_id=$1 AND user_id=$2`, spaceID, owner).Scan(&role); err != nil || role != "owner" {
		t.Fatalf("owner not seeded on created space: role=%q err=%v", role, err)
	}

	// Persisted on the project, and a second resolve reuses it (no new space).
	reloaded, err := loadAtlasProject(ctx, d, pid)
	if err != nil || reloaded.OutputSpaceID == nil || *reloaded.OutputSpaceID != spaceID {
		t.Fatalf("output space not persisted: %+v err=%v", reloaded.OutputSpaceID, err)
	}
	again, _, ae := srv.ensureOutputSpace(ctx, reloaded)
	if ae != nil || again != spaceID {
		t.Fatalf("second resolve created a new space: got %d (ae=%+v)", again, ae)
	}
}

// TestAtlasProjects_ExistingOutputSpace checks the explicit-output path: a project
// pointed at an existing space requires the caller to have write access to it.
func TestAtlasProjects_ExistingOutputSpace(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	bob := seedUser(t, d, "bob", "bobpw1234", false)
	// Alice owns a space; bob has no access to it.
	space := seedSpace(t, d, "Existing", "existing", alice)
	ca := loginClient(t, ts, "alice", "alicepw12")
	cb := loginClient(t, ts, "bob", "bobpw1234")
	projects := ts.URL + "/api/atlas/projects"

	// Alice (write access) may target it.
	body := fmt.Sprintf(`{"name":"P","owner_kind":"user","owner_id":%d,"output":{"space_id":%d}}`, alice, space)
	if st, rb := atlasReq(t, ca, "POST", projects, body); st != http.StatusCreated || !strings.Contains(rb, fmt.Sprintf(`"id":%d`, space)) {
		t.Fatalf("owner target own space: status=%d body=%s", st, rb)
	}
	// Bob may not target a space he can't write (owned by bob, space he can't reach).
	bobBody := fmt.Sprintf(`{"name":"P2","owner_kind":"user","owner_id":%d,"output":{"space_id":%d}}`, bob, space)
	if st, rb := atlasReq(t, cb, "POST", projects, bobBody); st != http.StatusForbidden {
		t.Fatalf("target unreachable space: want 403, got %d %s", st, rb)
	}
}
