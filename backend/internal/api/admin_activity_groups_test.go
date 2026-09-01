package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func groupsByName(t *testing.T, rec interface{ Bytes() []byte }) map[string]activityGroup {
	t.Helper()
	var out struct {
		Groups []activityGroup `json:"groups"`
	}
	if err := json.Unmarshal(rec.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	by := map[string]activityGroup{}
	for _, g := range out.Groups {
		by[g.Name] = g
	}
	return by
}

// Space activity follows the CONTENT: every revision on a page in that space,
// whoever wrote it — including people who are not members of anything.
func TestAdminActivityGroups_BySpace(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	ctx := context.Background()

	admin := seedUser(t, d, "admin", "adminpw123", true)
	alice := seedUser(t, d, "alice", "alicepw123", false)
	bob := seedUser(t, d, "bob", "bobpw12345", false)
	spaceID := seedSpace(t, d, "Handbook", "handbook", alice)
	pageID := seedPage(t, d, spaceID, "Onboarding")

	if _, err := d.ExecContext(ctx,
		`INSERT INTO page_revisions (page_id, title, body, author_id, source, byte_size, created_at)
		 VALUES ($1,'Onboarding','a',$2,'manual',1, tela_now()),
		        ($1,'Onboarding','b',$3,'agent', 1, tela_now())`,
		pageID, alice, bob); err != nil {
		t.Fatalf("seed revisions: %v", err)
	}
	if _, err := d.ExecContext(ctx,
		`INSERT INTO events (type, actor_user_id, target_kind, target_id, created_at)
		 VALUES ('page.view',$1,'page',$2, tela_now())`, bob, pageID); err != nil {
		t.Fatalf("seed events: %v", err)
	}

	rec := recordHandler(srv.AdminActivityGroups,
		userRequest(http.MethodGet, "/api/admin/activity/groups?by=space&window=all", "",
			authUser(admin, "admin", true)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	g, ok := groupsByName(t, rec.Body)["Handbook"]
	if !ok {
		t.Fatalf("Handbook missing from %q", rec.Body.String())
	}
	if g.Edits != 2 || g.AgentEdits != 1 {
		t.Fatalf("edits=%d agent=%d, want 2/1 — both authors count, whoever they are", g.Edits, g.AgentEdits)
	}
	if g.People != 2 {
		t.Fatalf("contributors=%d, want 2", g.People)
	}
	if g.Views != 1 {
		t.Fatalf("views=%d, want 1 (resolved through the event's target page)", g.Views)
	}
	if g.Pages != 1 {
		t.Fatalf("pages=%d, want 1", g.Pages)
	}
}

// Org activity follows the PEOPLE: the summed activity of its members, wherever
// they did it — so a member of two orgs counts toward both rather than being
// split into halves that add up to less than the truth.
func TestAdminActivityGroups_ByOrg_CountsSharedMemberInBoth(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	ctx := context.Background()

	admin := seedUser(t, d, "admin", "adminpw123", true)
	alice := seedUser(t, d, "alice", "alicepw123", false)
	spaceID := seedSpace(t, d, "Handbook", "handbook", alice)
	pageID := seedPage(t, d, spaceID, "Onboarding")
	if _, err := d.ExecContext(ctx,
		`INSERT INTO page_revisions (page_id, title, body, author_id, source, byte_size, created_at)
		 VALUES ($1,'Onboarding','a',$2,'manual',1, tela_now())`, pageID, alice); err != nil {
		t.Fatalf("seed revisions: %v", err)
	}

	var acme, nomad int64
	if err := d.QueryRowContext(ctx,
		`INSERT INTO orgs (name, slug) VALUES ('Acme','acme') RETURNING id`).Scan(&acme); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	if err := d.QueryRowContext(ctx,
		`INSERT INTO orgs (name, slug) VALUES ('Nomad','nomad') RETURNING id`).Scan(&nomad); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	if _, err := d.ExecContext(ctx,
		`INSERT INTO org_members (org_id, user_id, org_role) VALUES ($1,$3,'member'), ($2,$3,'member')`,
		acme, nomad, alice); err != nil {
		t.Fatalf("seed members: %v", err)
	}

	rec := recordHandler(srv.AdminActivityGroups,
		userRequest(http.MethodGet, "/api/admin/activity/groups?by=org&window=all", "",
			authUser(admin, "admin", true)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	by := groupsByName(t, rec.Body)
	for _, name := range []string{"Acme", "Nomad"} {
		g, ok := by[name]
		if !ok {
			t.Fatalf("%s missing from %q", name, rec.Body.String())
		}
		if g.Edits != 1 {
			t.Fatalf("%s edits=%d, want 1 — a shared member counts toward both teams", name, g.Edits)
		}
		if g.ActivePeople != 1 || g.People != 1 {
			t.Fatalf("%s active=%d of %d, want 1 of 1", name, g.ActivePeople, g.People)
		}
	}
}

func TestAdminActivityGroups_NonAdminForbidden(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	bob := seedUser(t, d, "bob", "bobpw12345", false)
	rec := recordHandler(srv.AdminActivityGroups,
		userRequest(http.MethodGet, "/api/admin/activity/groups?by=space", "", authUser(bob, "bob", false)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", rec.Code)
	}
}
