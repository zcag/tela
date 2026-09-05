package api

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"testing"
)

func notifCountByType(t *testing.T, d *sql.DB, userID int64, typ string) int {
	t.Helper()
	var n int
	if err := d.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM notifications WHERE user_id = $1 AND type = $2`, userID, typ).Scan(&n); err != nil {
		t.Fatalf("count %s for %d: %v", typ, userID, err)
	}
	return n
}

// editBody updates a page's body as the given user (triggers the change path).
func editBody(t *testing.T, srv *Server, uid int64, name string, pageID int64, body string) {
	t.Helper()
	if _, ae := srv.updatePageCore(context.Background(), authUser(uid, name, false), nil, pageID,
		pageUpdateRequest{Body: &body}, false); ae != nil {
		t.Fatalf("edit page: %v", ae)
	}
}

func TestSubscriptions_PageFollow_NotifyCollapseAndAccessGate(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	ctx := context.Background()

	alice := seedUser(t, d, "alice", "alicepw123", false)
	bob := seedUser(t, d, "bob", "bobpw12345", false)
	carol := seedUser(t, d, "carol", "carolpw123", false)
	spaceID := seedSpace(t, d, "Engineering", "engineering", alice)
	seedMember(t, d, spaceID, bob, "viewer")

	page, ae := srv.createPageCore(ctx, authUser(alice, "alice", false), nil,
		pageCreateRequest{SpaceID: spaceID, Title: "Plan", Body: "v0"}, true)
	if ae != nil {
		t.Fatalf("create page: %v", ae)
	}

	// carol (non-member) can't follow what she can't see.
	rec := routedRecorder("POST /api/pages/{id}/subscription", srv.SubscribePage,
		userRequest(http.MethodPost, "/api/pages/"+intStr(page.ID)+"/subscription", "", authUser(carol, "carol", false)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("carol subscribe: code=%d want 403", rec.Code)
	}

	// bob follows the page via the endpoint.
	rec = routedRecorder("POST /api/pages/{id}/subscription", srv.SubscribePage,
		userRequest(http.MethodPost, "/api/pages/"+intStr(page.ID)+"/subscription", "", authUser(bob, "bob", false)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"subscribed":true`) {
		t.Fatalf("bob subscribe: code=%d body=%q", rec.Code, rec.Body.String())
	}

	editBody(t, srv, alice, "alice", page.ID, "v1")

	// bob (follower) notified; alice (editor + author-follower) is excluded.
	if n := notifCountByType(t, d, bob, notifPageUpdated); n != 1 {
		t.Fatalf("bob page_updated = %d, want 1", n)
	}
	if n := notifCountByType(t, d, alice, notifPageUpdated); n != 0 {
		t.Fatalf("alice (editor) page_updated = %d, want 0", n)
	}

	// Second edit while bob hasn't read → collapsed, still one unread.
	editBody(t, srv, alice, "alice", page.ID, "v2")
	if n := notifCountByType(t, d, bob, notifPageUpdated); n != 1 {
		t.Fatalf("bob page_updated after 2nd edit = %d, want 1 (collapsed)", n)
	}

	// bob reads it; the next edit makes a fresh notification.
	if _, err := d.ExecContext(ctx, `UPDATE notifications SET read_at = tela_now() WHERE user_id = $1`, bob); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	editBody(t, srv, alice, "alice", page.ID, "v3")
	if n := notifCountByType(t, d, bob, notifPageUpdated); n != 2 {
		t.Fatalf("bob page_updated after read+edit = %d, want 2", n)
	}
}

func TestSubscriptions_AuthorAutoFollow(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	ctx := context.Background()

	alice := seedUser(t, d, "alice", "alicepw123", false)
	bob := seedUser(t, d, "bob", "bobpw12345", false)
	spaceID := seedSpace(t, d, "Engineering", "engineering", alice)
	seedMember(t, d, spaceID, bob, "editor")

	page, ae := srv.createPageCore(ctx, authUser(alice, "alice", false), nil,
		pageCreateRequest{SpaceID: spaceID, Title: "Plan", Body: "v0"}, true)
	if ae != nil {
		t.Fatalf("create page: %v", ae)
	}

	// bob (member) edits alice's page → alice, the author-follower, is notified.
	editBody(t, srv, bob, "bob", page.ID, "v1")
	if n := notifCountByType(t, d, alice, notifPageUpdated); n != 1 {
		t.Fatalf("alice (author-follower) page_updated = %d, want 1", n)
	}
}

func TestSubscriptions_SpaceFollow(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	ctx := context.Background()

	alice := seedUser(t, d, "alice", "alicepw123", false)
	bob := seedUser(t, d, "bob", "bobpw12345", false)
	spaceID := seedSpace(t, d, "Engineering", "engineering", alice)
	seedMember(t, d, spaceID, bob, "viewer")

	page, ae := srv.createPageCore(ctx, authUser(alice, "alice", false), nil,
		pageCreateRequest{SpaceID: spaceID, Title: "Plan", Body: "v0"}, true)
	if ae != nil {
		t.Fatalf("create page: %v", ae)
	}

	if err := srv.setSubscription(ctx, bob, "space", spaceID); err != nil {
		t.Fatalf("space subscribe: %v", err)
	}
	editBody(t, srv, alice, "alice", page.ID, "v1")
	if n := notifCountByType(t, d, bob, notifPageUpdated); n != 1 {
		t.Fatalf("bob (space follower) page_updated = %d, want 1", n)
	}
}

func TestNotificationPrefs_GatingAndAPI(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	ctx := context.Background()

	alice := seedUser(t, d, "alice", "alicepw123", false)
	bob := seedUser(t, d, "bob", "bobpw12345", false)
	spaceID := seedSpace(t, d, "Engineering", "engineering", alice)
	seedMember(t, d, spaceID, bob, "viewer")
	page, ae := srv.createPageCore(ctx, authUser(alice, "alice", false), nil,
		pageCreateRequest{SpaceID: spaceID, Title: "Plan", Body: "v0"}, true)
	if ae != nil {
		t.Fatalf("create page: %v", ae)
	}
	if err := srv.setSubscription(ctx, bob, "page", page.ID); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	bu := authUser(bob, "bob", false)

	// Default matrix: the event types × 2 channels, all enabled.
	rec := routedRecorder("GET /api/users/me/notification-prefs", srv.GetNotificationPrefs,
		userRequest(http.MethodGet, "/api/users/me/notification-prefs", "", bu))
	if rec.Code != http.StatusOK {
		t.Fatalf("get prefs: code=%d body=%q", rec.Code, rec.Body.String())
	}
	wantPrefs := len(notificationEventTypes) * len(notificationChannels)
	if c := strings.Count(rec.Body.String(), `"enabled":true`); c != wantPrefs {
		t.Fatalf("default prefs enabled=true count = %d, want %d: %q", c, wantPrefs, rec.Body.String())
	}

	// Turn page_updated in-app OFF.
	rec = routedRecorder("PUT /api/users/me/notification-prefs", srv.UpdateNotificationPref,
		userRequest(http.MethodPut, "/api/users/me/notification-prefs",
			`{"event_type":"page_updated","channel":"inapp","enabled":false}`, bu))
	if rec.Code != http.StatusOK {
		t.Fatalf("update pref: code=%d body=%q", rec.Code, rec.Body.String())
	}

	// An edit now produces no page_updated for bob.
	editBody(t, srv, alice, "alice", page.ID, "v1")
	if n := notifCountByType(t, d, bob, notifPageUpdated); n != 0 {
		t.Fatalf("bob page_updated with pref off = %d, want 0", n)
	}

	// Invalid pref is rejected.
	rec = routedRecorder("PUT /api/users/me/notification-prefs", srv.UpdateNotificationPref,
		userRequest(http.MethodPut, "/api/users/me/notification-prefs",
			`{"event_type":"nope","channel":"inapp","enabled":false}`, bu))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid event_type: code=%d want 400", rec.Code)
	}
}

func TestSubscriptions_DeletePageCleansUp(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	ctx := context.Background()

	alice := seedUser(t, d, "alice", "alicepw123", false)
	bob := seedUser(t, d, "bob", "bobpw12345", false)
	spaceID := seedSpace(t, d, "Engineering", "engineering", alice)
	seedMember(t, d, spaceID, bob, "viewer")
	page, ae := srv.createPageCore(ctx, authUser(alice, "alice", false), nil,
		pageCreateRequest{SpaceID: spaceID, Title: "Plan", Body: "v0"}, true)
	if ae != nil {
		t.Fatalf("create page: %v", ae)
	}
	if err := srv.setSubscription(ctx, bob, "page", page.ID); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	editBody(t, srv, alice, "alice", page.ID, "v1") // gives bob a notification

	if ae := srv.deletePageCore(ctx, authUser(alice, "alice", false), nil, page.ID, deleteViaManual); ae != nil {
		t.Fatalf("delete page: %v", ae)
	}

	var subs, notifs int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM subscriptions WHERE subject_kind='page' AND subject_id=$1`, page.ID).Scan(&subs); err != nil {
		t.Fatalf("count subs: %v", err)
	}
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications WHERE subject_kind='page' AND subject_id=$1`, page.ID).Scan(&notifs); err != nil {
		t.Fatalf("count notifs: %v", err)
	}
	if subs != 0 || notifs != 0 {
		t.Fatalf("after page delete: subs=%d notifs=%d, want 0/0", subs, notifs)
	}
}
