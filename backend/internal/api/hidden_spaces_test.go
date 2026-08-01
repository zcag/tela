package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestHiddenSpaces_AddListDelete(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	uid := seedUser(t, d, "alice", "alicepw123", false)
	spaceID := seedSpace(t, d, "Engineering", "engineering", uid)
	u := authUser(uid, "alice", false)

	// Initially empty.
	rec := routedRecorder("GET /api/users/me/hidden-spaces",
		srv.ListHiddenSpaces, userRequest(http.MethodGet, "/api/users/me/hidden-spaces", "", u))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"space_id":`) {
		t.Fatalf("list before: code=%d body=%q", rec.Code, rec.Body.String())
	}

	// Hide it.
	rec = routedRecorder("PUT /api/spaces/{id}/hide",
		srv.AddHiddenSpace, userRequest(http.MethodPut, "/api/spaces/"+intStr(spaceID)+"/hide", "", u))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"is_hidden":true`) {
		t.Fatalf("hide: code=%d body=%q", rec.Code, rec.Body.String())
	}

	// Hide again — idempotent.
	rec = routedRecorder("PUT /api/spaces/{id}/hide",
		srv.AddHiddenSpace, userRequest(http.MethodPut, "/api/spaces/"+intStr(spaceID)+"/hide", "", u))
	if rec.Code != http.StatusOK {
		t.Fatalf("hide idempotent: code=%d body=%q", rec.Code, rec.Body.String())
	}

	// Appears in the list.
	rec = routedRecorder("GET /api/users/me/hidden-spaces",
		srv.ListHiddenSpaces, userRequest(http.MethodGet, "/api/users/me/hidden-spaces", "", u))
	if !strings.Contains(rec.Body.String(), `"space_id":`+intStr(spaceID)) {
		t.Fatalf("list missing space: body=%q", rec.Body.String())
	}

	// Unhide.
	rec = routedRecorder("DELETE /api/spaces/{id}/hide",
		srv.DeleteHiddenSpace, userRequest(http.MethodDelete, "/api/spaces/"+intStr(spaceID)+"/hide", "", u))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("unhide: code=%d body=%q", rec.Code, rec.Body.String())
	}

	// Unhide again — still 204.
	rec = routedRecorder("DELETE /api/spaces/{id}/hide",
		srv.DeleteHiddenSpace, userRequest(http.MethodDelete, "/api/spaces/"+intStr(spaceID)+"/hide", "", u))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("unhide idempotent: code=%d body=%q", rec.Code, rec.Body.String())
	}

	// Gone from the list.
	rec = routedRecorder("GET /api/users/me/hidden-spaces",
		srv.ListHiddenSpaces, userRequest(http.MethodGet, "/api/users/me/hidden-spaces", "", u))
	if strings.Contains(rec.Body.String(), `"space_id":`+intStr(spaceID)) {
		t.Fatalf("hide not removed: body=%q", rec.Body.String())
	}
}

// Hiding a space you aren't a member of is 403, same as any other access denial.
func TestHiddenSpaces_NonMember_Add_Returns403(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	owner := seedUser(t, d, "owner", "ownerpw123", false)
	stranger := seedUser(t, d, "stranger", "strangerpw", false)
	spaceID := seedSpace(t, d, "Engineering", "engineering", owner)

	rec := routedRecorder("PUT /api/spaces/{id}/hide",
		srv.AddHiddenSpace, userRequest(http.MethodPut, "/api/spaces/"+intStr(spaceID)+"/hide", "", authUser(stranger, "stranger", false)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-member hide: code=%d body=%q want 403", rec.Code, rec.Body.String())
	}
}

// Hides are per-user: one user's hidden space stays visible to another, and the
// list re-gates through space_access so a hide of an inaccessible space drops out.
func TestHiddenSpaces_IsolatedPerUser(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	alice := seedUser(t, d, "alice", "alicepw123", false)
	bob := seedUser(t, d, "bob", "bobpw12345", false)
	spaceID := seedSpace(t, d, "Engineering", "engineering", alice)
	seedMember(t, d, spaceID, bob, roleEditor)

	rec := routedRecorder("PUT /api/spaces/{id}/hide",
		srv.AddHiddenSpace, userRequest(http.MethodPut, "/api/spaces/"+intStr(spaceID)+"/hide", "", authUser(alice, "alice", false)))
	if rec.Code != http.StatusOK {
		t.Fatalf("alice hide: code=%d body=%q", rec.Code, rec.Body.String())
	}

	// Bob's list is untouched.
	rec = routedRecorder("GET /api/users/me/hidden-spaces",
		srv.ListHiddenSpaces, userRequest(http.MethodGet, "/api/users/me/hidden-spaces", "", authUser(bob, "bob", false)))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"space_id":`) {
		t.Fatalf("bob list: code=%d body=%q want empty", rec.Code, rec.Body.String())
	}

	// Revoke alice's access → her hide drops out of the re-gated list.
	if _, err := d.Exec(`DELETE FROM space_members WHERE space_id = $1 AND user_id = $2`, spaceID, alice); err != nil {
		t.Fatalf("revoke alice: %v", err)
	}
	rec = routedRecorder("GET /api/users/me/hidden-spaces",
		srv.ListHiddenSpaces, userRequest(http.MethodGet, "/api/users/me/hidden-spaces", "", authUser(alice, "alice", false)))
	if strings.Contains(rec.Body.String(), `"space_id":`+intStr(spaceID)) {
		t.Fatalf("inaccessible hide still listed: body=%q", rec.Body.String())
	}
}
