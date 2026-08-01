package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zcag/tela/backend/internal/auth"
)

func createSpaceInvite(srv *Server, spaceID int64, body string, u *auth.User) *httptest.ResponseRecorder {
	return routedRecorder("POST /api/spaces/{id}/invites", srv.CreateSpaceInvite,
		userRequest(http.MethodPost, "/api/spaces/"+intStr(spaceID)+"/invites", body, u))
}

// Branch 1: the address already belongs to a verified tela user → immediate
// space_members row + a space_added notification, no invite row, no accept step.
func TestSpaceInvite_ExistingUserGrantedNow(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	ownerID := seedUser(t, d, "alice", "alicepw12", false)
	space := seedSpace(t, d, "Handbook", "handbook", ownerID)
	owner := authUser(ownerID, "alice", false)

	bobID := seedUser(t, d, "bob", "bobpw1234", false)
	setUserEmail(t, d, bobID, "bob@acme.com")

	rec := createSpaceInvite(srv, space, `{"email":"BOB@acme.com","role":"editor"}`, owner)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"member"`) {
		t.Fatalf("create = %d body=%q; want 201 with a member envelope", rec.Code, rec.Body)
	}
	var role string
	mustQueryRow(t, d, `SELECT role FROM space_members WHERE space_id=$1 AND user_id=$2`, &role, space, bobID)
	if role != "editor" {
		t.Fatalf("bob role = %q; want editor", role)
	}
	var invites int
	mustQueryRow(t, d, `SELECT COUNT(*) FROM space_invites WHERE space_id=$1`, &invites, space)
	if invites != 0 {
		t.Fatalf("existing-user grant should mint no invite, got %d", invites)
	}
	var notifs int
	mustQueryRow(t, d, `SELECT COUNT(*) FROM notifications WHERE user_id=$1 AND type='space_added'`, &notifs, bobID)
	if notifs != 1 {
		t.Fatalf("expected 1 space_added notification, got %d", notifs)
	}

	// Re-sharing with someone who already has access is a conflict.
	rec = createSpaceInvite(srv, space, `{"email":"bob@acme.com","role":"viewer"}`, owner)
	if rec.Code != http.StatusConflict {
		t.Fatalf("re-share = %d; want 409", rec.Code)
	}
}

// Branch 2: nobody owns the address → a pending invite the owner can list and
// revoke. Non-owners can't touch any of it.
func TestSpaceInvite_CreateListRevoke(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	ownerID := seedUser(t, d, "alice", "alicepw12", false)
	space := seedSpace(t, d, "Handbook", "handbook", ownerID)
	owner := authUser(ownerID, "alice", false)

	rec := createSpaceInvite(srv, space, `{"email":"newbie@acme.com","role":"viewer"}`, owner)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"invite"`) {
		t.Fatalf("create = %d body=%q; want 201 with an invite envelope", rec.Code, rec.Body)
	}
	var n int
	mustQueryRow(t, d, `SELECT COUNT(*) FROM space_invites WHERE space_id=$1 AND lower(email)='newbie@acme.com' AND accepted_at IS NULL`, &n, space)
	if n != 1 {
		t.Fatalf("expected 1 pending invite, got %d", n)
	}

	// Re-inviting the same address refreshes rather than duplicating.
	if rec := createSpaceInvite(srv, space, `{"email":"newbie@acme.com","role":"editor"}`, owner); rec.Code != http.StatusCreated {
		t.Fatalf("re-invite = %d body=%q", rec.Code, rec.Body)
	}
	mustQueryRow(t, d, `SELECT COUNT(*) FROM space_invites WHERE space_id=$1 AND accepted_at IS NULL`, &n, space)
	if n != 1 {
		t.Fatalf("re-invite should refresh the pending row, got %d", n)
	}

	// An account whose email is still unconfirmed is NOT the grant branch — it
	// gets an invite, which lands the moment they verify.
	danID := seedUser(t, d, "dan", "danpw1234", false)
	mustExec(t, d, `UPDATE users SET email = 'dan@acme.com' WHERE id = $1`, danID)
	if rec := createSpaceInvite(srv, space, `{"email":"dan@acme.com"}`, owner); rec.Code != http.StatusCreated ||
		!strings.Contains(rec.Body.String(), `"invite"`) {
		t.Fatalf("unverified-user invite = %d body=%q; want 201 with an invite envelope", rec.Code, rec.Body)
	}

	// A garbage address is rejected.
	if rec := createSpaceInvite(srv, space, `{"email":"nope","role":"viewer"}`, owner); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad email = %d; want 400", rec.Code)
	}
	// So is a bogus role.
	if rec := createSpaceInvite(srv, space, `{"email":"x@acme.com","role":"admin"}`, owner); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad role = %d; want 400", rec.Code)
	}

	// Owner lists it.
	rec = routedRecorder("GET /api/spaces/{id}/invites", srv.ListSpaceInvites,
		userRequest(http.MethodGet, "/api/spaces/"+intStr(space)+"/invites", "", owner))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "newbie@acme.com") {
		t.Fatalf("list = %d body=%q", rec.Code, rec.Body)
	}

	// An editor (non-owner) can neither invite nor list.
	editorID := seedUser(t, d, "eve", "evepw1234", false)
	seedMember(t, d, space, editorID, "editor")
	editor := authUser(editorID, "eve", false)
	if rec := createSpaceInvite(srv, space, `{"email":"other@acme.com"}`, editor); rec.Code != http.StatusForbidden {
		t.Fatalf("non-owner create = %d; want 403", rec.Code)
	}
	rec = routedRecorder("GET /api/spaces/{id}/invites", srv.ListSpaceInvites,
		userRequest(http.MethodGet, "/api/spaces/"+intStr(space)+"/invites", "", editor))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-owner list = %d; want 403", rec.Code)
	}

	// Revoke.
	var id int64
	mustQueryRow(t, d, `SELECT id FROM space_invites WHERE space_id=$1 AND accepted_at IS NULL`, &id, space)
	rec = routedRecorder("DELETE /api/spaces/{id}/invites/{inviteId}", srv.RevokeSpaceInvite,
		userRequest(http.MethodDelete, "/api/spaces/"+intStr(space)+"/invites/"+intStr(id), "", owner))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d body=%q", rec.Code, rec.Body)
	}
	rec = routedRecorder("DELETE /api/spaces/{id}/invites/{inviteId}", srv.RevokeSpaceInvite,
		userRequest(http.MethodDelete, "/api/spaces/"+intStr(space)+"/invites/"+intStr(id), "", owner))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("re-revoke = %d; want 404", rec.Code)
	}
}

// The public token page renders a space invite (kind discriminator + names the
// space and inviter), and accepting is email-targeted.
func TestSpaceInvite_GetPublicAndAccept(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	ownerID := seedUser(t, d, "alice", "alicepw12", false)
	space := seedSpace(t, d, "Handbook", "handbook", ownerID)
	raw := "raw-space-invite-token-1234567890"
	mustExec(t, d, `INSERT INTO space_invites (space_id, email, role, token_hash, invited_by, expires_at)
		VALUES ($1, 'bob@acme.com', 'editor', $2, $3, to_char((now() AT TIME ZONE 'UTC') + interval '7 days', 'YYYY-MM-DD HH24:MI:SS'))`,
		space, hashEmailToken(raw), ownerID)

	rec := routedRecorder("GET /api/invites/{token}", srv.GetInvite,
		userRequest(http.MethodGet, "/api/invites/"+raw, "", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `"valid":true`) ||
		!strings.Contains(body, `"kind":"space"`) || !strings.Contains(body, "Handbook") ||
		!strings.Contains(body, "bob@acme.com") || !strings.Contains(body, `"inviter":"alice"`) {
		t.Fatalf("get invite = %d body=%q", rec.Code, body)
	}

	// Wrong-email user is refused — a forwarded link can't enrol the wrong person.
	eveID := seedUser(t, d, "eve", "evepw1234", false)
	eve := &auth.User{ID: eveID, Username: "eve", Email: "eve@other.com"}
	rec = recordHandler(srv.AcceptInvite, userRequest(http.MethodPost, "/api/me/accept-invite", `{"token":"`+raw+`"}`, eve))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("wrong-email accept = %d; want 403", rec.Code)
	}

	// The invited user accepts and gets the space at the invited role.
	bobID := seedUser(t, d, "bob", "bobpw1234", false)
	bob := &auth.User{ID: bobID, Username: "bob", Email: "bob@acme.com"}
	rec = recordHandler(srv.AcceptInvite, userRequest(http.MethodPost, "/api/me/accept-invite", `{"token":"`+raw+`"}`, bob))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"space"`) {
		t.Fatalf("accept = %d body=%q", rec.Code, rec.Body)
	}
	var role string
	mustQueryRow(t, d, `SELECT role FROM space_members WHERE space_id=$1 AND user_id=$2`, &role, space, bobID)
	if role != "editor" {
		t.Fatalf("bob role = %q; want editor", role)
	}
	// Consumed — re-accepting fails.
	rec = recordHandler(srv.AcceptInvite, userRequest(http.MethodPost, "/api/me/accept-invite", `{"token":"`+raw+`"}`, bob))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("re-accept = %d; want 400", rec.Code)
	}
}

// An expired invite is invalid on both the public page and accept.
func TestSpaceInvite_Expired(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	ownerID := seedUser(t, d, "alice", "alicepw12", false)
	space := seedSpace(t, d, "Handbook", "handbook", ownerID)
	raw := "expired-space-invite-token"
	mustExec(t, d, `INSERT INTO space_invites (space_id, email, role, token_hash, expires_at)
		VALUES ($1, 'bob@acme.com', 'viewer', $2, to_char((now() AT TIME ZONE 'UTC') - interval '1 day', 'YYYY-MM-DD HH24:MI:SS'))`,
		space, hashEmailToken(raw))

	rec := routedRecorder("GET /api/invites/{token}", srv.GetInvite,
		userRequest(http.MethodGet, "/api/invites/"+raw, "", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"valid":false`) {
		t.Fatalf("expired lookup = %d body=%q", rec.Code, rec.Body)
	}
	bobID := seedUser(t, d, "bob", "bobpw1234", false)
	bob := &auth.User{ID: bobID, Username: "bob", Email: "bob@acme.com"}
	rec = recordHandler(srv.AcceptInvite, userRequest(http.MethodPost, "/api/me/accept-invite", `{"token":"`+raw+`"}`, bob))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expired accept = %d; want 400", rec.Code)
	}
	// The pending list hides it too.
	rec = routedRecorder("GET /api/spaces/{id}/invites", srv.ListSpaceInvites,
		userRequest(http.MethodGet, "/api/spaces/"+intStr(space)+"/invites", "", authUser(ownerID, "alice", false)))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "bob@acme.com") {
		t.Fatalf("expired invite should not be listed: %d body=%q", rec.Code, rec.Body)
	}
}

// The whole point: someone invited before they had an account gets the space
// the moment they verify their signup — no second action from the inviter.
func TestSpaceInvite_AutoApplyOnVerify(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	ctx := context.Background()
	ownerID := seedUser(t, d, "alice", "alicepw12", false)
	space := seedSpace(t, d, "Handbook", "handbook", ownerID)
	org := seedOrg(t, d, "Acme", "acme")
	mustExec(t, d, `INSERT INTO space_invites (space_id, email, role, token_hash, expires_at)
		VALUES ($1, 'carol@acme.com', 'editor', 'space-hash', to_char((now() AT TIME ZONE 'UTC') + interval '7 days', 'YYYY-MM-DD HH24:MI:SS'))`, space)
	mustExec(t, d, `INSERT INTO org_invites (org_id, email, org_role, token_hash, expires_at)
		VALUES ($1, 'carol@acme.com', 'member', 'org-hash', to_char((now() AT TIME ZONE 'UTC') + interval '7 days', 'YYYY-MM-DD HH24:MI:SS'))`, org)

	carolID := seedUser(t, d, "carol", "carolpw12", false)
	// Mirrors the VerifyEmail hook — it applies org AND space invites.
	srv.applyPendingInvites(ctx, carolID, "Carol@acme.com")

	var role string
	if err := d.QueryRowContext(ctx, `SELECT role FROM space_members WHERE space_id=$1 AND user_id=$2`, space, carolID).Scan(&role); err != nil || role != "editor" {
		t.Fatalf("carol should have auto-joined the space as editor, role=%q err=%v", role, err)
	}
	var orgRole string
	if err := d.QueryRowContext(ctx, `SELECT org_role FROM org_members WHERE org_id=$1 AND user_id=$2`, org, carolID).Scan(&orgRole); err != nil {
		t.Fatalf("carol should also have auto-joined the org: %v", err)
	}
	var accepted int
	mustQueryRow(t, d, `SELECT COUNT(*) FROM space_invites WHERE space_id=$1 AND accepted_at IS NOT NULL`, &accepted, space)
	if accepted != 1 {
		t.Fatalf("space invite should be marked accepted, got %d", accepted)
	}
}
