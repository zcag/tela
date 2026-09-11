package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestChangePassword_RejectsWrongOldPassword(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	uid := seedUser(t, d, "alice", "correct-old-pw", false)
	sid := seedSession(t, d, uid)

	req := userRequestWithSession(http.MethodPost, "/api/users/me/password",
		`{"old_password":"wrong","new_password":"brand-new-pw"}`,
		authUser(uid, "alice", false), sid)
	rec := recordHandler(srv.ChangePassword, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%q want 401", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"code":"unauthorized"`) {
		t.Fatalf("missing unauthorized envelope: %q", rec.Body.String())
	}
}

func TestChangePassword_RejectsShortNewPassword(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	uid := seedUser(t, d, "alice", "correct-old-pw", false)
	sid := seedSession(t, d, uid)

	req := userRequestWithSession(http.MethodPost, "/api/users/me/password",
		`{"old_password":"correct-old-pw","new_password":"short"}`,
		authUser(uid, "alice", false), sid)
	rec := recordHandler(srv.ChangePassword, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%q want 400", rec.Code, rec.Body.String())
	}
}

func TestChangePassword_KillsOtherSessionsKeepsCurrent(t *testing.T) {
	ctx := context.Background()
	d := newAPITestDB(t)
	srv := New(d)
	uid := seedUser(t, d, "alice", "correct-old-pw", false)
	current := seedSession(t, d, uid)
	other1 := seedSession(t, d, uid)
	other2 := seedSession(t, d, uid)

	req := userRequestWithSession(http.MethodPost, "/api/users/me/password",
		`{"old_password":"correct-old-pw","new_password":"brand-new-pw"}`,
		authUser(uid, "alice", false), current)
	rec := recordHandler(srv.ChangePassword, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%q want 204", rec.Code, rec.Body.String())
	}

	var n int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE user_id = $1`, uid).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("sessions=%d, want 1 (current only)", n)
	}
	var stillCurrent int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE id = $1`, current).Scan(&stillCurrent); err != nil {
		t.Fatalf("current count: %v", err)
	}
	if stillCurrent != 1 {
		t.Fatalf("current session was wiped (count=%d, want 1)", stillCurrent)
	}
	_ = other1
	_ = other2
}

func TestListMySessions_FlagsCurrent(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	uid := seedUser(t, d, "alice", "alicepw1234", false)
	current := seedSession(t, d, uid)
	seedSession(t, d, uid)

	req := userRequestWithSession(http.MethodGet, "/api/users/me/sessions", "",
		authUser(uid, "alice", false), current)
	rec := recordHandler(srv.ListMySessions, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q want 200", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"current":true`) {
		t.Fatalf("body missing current=true: %q", body)
	}
	if !strings.Contains(body, `"current":false`) {
		t.Fatalf("body missing current=false: %q", body)
	}
}

func TestDeleteMySession_RefusesCurrent(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	uid := seedUser(t, d, "alice", "alicepw1234", false)
	current := seedSession(t, d, uid)

	req := userRequestWithSession(http.MethodDelete, "/api/users/me/sessions/"+current, "",
		authUser(uid, "alice", false), current)
	rec := routedRecorder("DELETE /api/users/me/sessions/{id}", srv.DeleteMySession, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%q want 400", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/api/auth/logout") {
		t.Fatalf("message should mention /api/auth/logout: %q", rec.Body.String())
	}
}

func TestDeleteMySession_NotFoundWhenNotOwner(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	alice := seedUser(t, d, "alice", "alicepw1234", false)
	bob := seedUser(t, d, "bob", "bobpw1234", false)
	bobSid := seedSession(t, d, bob)
	aliceSid := seedSession(t, d, alice)

	req := userRequestWithSession(http.MethodDelete, "/api/users/me/sessions/"+bobSid, "",
		authUser(alice, "alice", false), aliceSid)
	rec := routedRecorder("DELETE /api/users/me/sessions/{id}", srv.DeleteMySession, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%q want 404", rec.Code, rec.Body.String())
	}
}

func TestDeleteAllMySessions_KeepsCurrent(t *testing.T) {
	ctx := context.Background()
	d := newAPITestDB(t)
	srv := New(d)
	uid := seedUser(t, d, "alice", "alicepw1234", false)
	current := seedSession(t, d, uid)
	seedSession(t, d, uid)
	seedSession(t, d, uid)

	req := userRequestWithSession(http.MethodDelete, "/api/users/me/sessions", "",
		authUser(uid, "alice", false), current)
	rec := recordHandler(srv.DeleteAllMySessionsExceptCurrent, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%q want 204", rec.Code, rec.Body.String())
	}

	var n int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE user_id = $1`, uid).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("sessions=%d, want 1", n)
	}
}

// The backfill-dot opt-out rides PATCH /api/users/me with the other profile
// fields. Default is ON, so the interesting case is that turning it off sticks
// and that the field alone is enough to make the request valid (the handler
// rejects a body with no fields at all).
func TestUpdateMyProfile_TogglesBackfillDot(t *testing.T) {
	ctx := context.Background()
	d := newAPITestDB(t)
	srv := New(d)
	uid := seedUser(t, d, "alice", "pw-long-enough", false)
	sid := seedSession(t, d, uid)

	read := func() int {
		var v int
		if err := d.QueryRowContext(ctx, `SELECT show_backfill_dot FROM users WHERE id = $1`, uid).Scan(&v); err != nil {
			t.Fatalf("read show_backfill_dot: %v", err)
		}
		return v
	}
	if got := read(); got != 1 {
		t.Fatalf("default show_backfill_dot = %d, want 1", got)
	}

	req := userRequestWithSession(http.MethodPatch, "/api/users/me",
		`{"show_backfill_dot":false}`, authUser(uid, "alice", false), sid)
	rec := recordHandler(srv.UpdateMyProfile, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q want 200", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"show_backfill_dot":false`) {
		t.Fatalf("response did not echo the saved value: %q", rec.Body.String())
	}
	if got := read(); got != 0 {
		t.Fatalf("after off, show_backfill_dot = %d, want 0", got)
	}

	req = userRequestWithSession(http.MethodPatch, "/api/users/me",
		`{"show_backfill_dot":true}`, authUser(uid, "alice", false), sid)
	if rec := recordHandler(srv.UpdateMyProfile, req); rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q want 200", rec.Code, rec.Body.String())
	}
	if got := read(); got != 1 {
		t.Fatalf("after on, show_backfill_dot = %d, want 1", got)
	}
}
