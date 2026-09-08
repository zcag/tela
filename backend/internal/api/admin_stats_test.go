package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// TestAdminStats_Aggregates — seeds activity and asserts the dashboard payload
// reflects it: totals, the daily view series, the top-pages leaderboard, and the
// ask answer-rate.
func TestAdminStats_Aggregates(t *testing.T) {
	ts, d := newWiredServer(t)
	ctx := context.Background()
	admin := seedUser(t, d, "admin", "testpass123", true)
	bob := seedUser(t, d, "bob", "bobpw1234", false)
	spaceID := seedSpace(t, d, "Docs", "docs", admin)
	pageID := seedPage(t, d, spaceID, "Guide")

	// 5 views + 2 edits today, by non-admin bob — the activity the default view
	// should surface.
	for i := 0; i < 5; i++ {
		if _, err := d.ExecContext(ctx,
			`INSERT INTO events (type, target_kind, target_id, actor_user_id, actor_label)
			 VALUES ('page.view','page',$1,$2,'bob')`, pageID, bob); err != nil {
			t.Fatalf("insert view: %v", err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := d.ExecContext(ctx,
			`INSERT INTO events (type, target_kind, target_id, actor_user_id, actor_label)
			 VALUES ('page.edit','page',$1,$2,'bob')`, pageID, bob); err != nil {
			t.Fatalf("insert edit: %v", err)
		}
	}
	// One extra view by the admin — noise that the default view must hide (and
	// ?include_admins must reveal).
	if _, err := d.ExecContext(ctx,
		`INSERT INTO events (type, target_kind, target_id, actor_user_id, actor_label)
		 VALUES ('page.view','page',$1,$2,'admin')`, pageID, admin); err != nil {
		t.Fatalf("insert admin view: %v", err)
	}
	// An ask that retrieved nothing + one that did, both by bob.
	if _, err := d.ExecContext(ctx,
		`INSERT INTO ask_log (user_id, question, answered) VALUES ($1,'q1',0),($1,'q2',1)`, bob); err != nil {
		t.Fatalf("insert ask_log: %v", err)
	}
	// A page revision authored by the admin → they count as "activated" (a
	// population signal, never filtered by the admin toggle).
	if _, err := d.ExecContext(ctx,
		`INSERT INTO page_revisions (page_id, title, body, author_id, source, byte_size, created_at)
		 VALUES ($1,'Guide','hello world',$2,'edit',11,tela_now())`, pageID, admin); err != nil {
		t.Fatalf("insert page_revision: %v", err)
	}

	c := loginClient(t, ts, "admin", "testpass123")
	resp, err := c.Get(ts.URL + "/api/admin/stats")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var got adminStats
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got.Pages < 1 || got.Users < 1 || got.Spaces < 1 {
		t.Fatalf("totals look empty: %+v", got)
	}
	if len(got.Days) != statsWindowDays || len(got.Views) != statsWindowDays {
		t.Fatalf("series length wrong: days=%d views=%d", len(got.Days), len(got.Views))
	}
	// Today is the last bucket; bob's 5 views landed there. The admin's extra view
	// is hidden by default (5, not 6).
	if got.Views[statsWindowDays-1] != 5 {
		t.Fatalf("today's views=%d want 5 (admin view excluded)", got.Views[statsWindowDays-1])
	}
	if got.Edits[statsWindowDays-1] != 2 {
		t.Fatalf("today's edits=%d want 2", got.Edits[statsWindowDays-1])
	}
	if len(got.TopPages) != 1 || got.TopPages[0].PageID != pageID || got.TopPages[0].Count != 5 {
		t.Fatalf("top pages wrong: %+v", got.TopPages)
	}
	if len(got.TopContributors) != 1 || got.TopContributors[0].Label != "bob" || got.TopContributors[0].Count != 2 {
		t.Fatalf("top contributors wrong: %+v", got.TopContributors)
	}
	if got.Asks30 != 2 || got.AsksAnswered30 != 1 {
		t.Fatalf("asks=%d answered=%d want 2/1", got.Asks30, got.AsksAnswered30)
	}
	// Active users: the admin acted today → DAU ≥ 1.
	if got.DAU < 1 {
		t.Fatalf("DAU=%d want ≥1", got.DAU)
	}
	// Cumulative growth ends at the current totals.
	if got.UsersCum[statsWindowDays-1] != got.Users {
		t.Fatalf("users_cum tail=%d want %d", got.UsersCum[statsWindowDays-1], got.Users)
	}
	// Per-day signups: everyone here was created today, and the daily series has
	// to agree with the curve it's derived from rather than drift beside it.
	if len(got.Signups) != statsWindowDays {
		t.Fatalf("signups length=%d want %d", len(got.Signups), statsWindowDays)
	}
	if got.Signups[statsWindowDays-1] != got.Users {
		t.Fatalf("today's signups=%d want %d", got.Signups[statsWindowDays-1], got.Users)
	}
	var signupSum int64
	for _, n := range got.Signups {
		signupSum += n
	}
	if signupSum != got.NewUsers30 {
		t.Fatalf("signups sum=%d want new_users_30=%d", signupSum, got.NewUsers30)
	}

	// Operator signals: the admin signed up in-window, authored a revision, and
	// asked one question that returned nothing.
	if got.NewUsers30 < 1 {
		t.Fatalf("new_users_30=%d want ≥1", got.NewUsers30)
	}
	if got.Activated < 1 {
		t.Fatalf("activated=%d want ≥1 (admin authored a revision)", got.Activated)
	}
	var adminRow *statsSignup
	for i := range got.RecentSignups {
		if got.RecentSignups[i].Username == "admin" {
			adminRow = &got.RecentSignups[i]
		}
	}
	if adminRow == nil || !adminRow.Activated {
		t.Fatalf("recent signups missing an activated admin: %+v", got.RecentSignups)
	}
	if len(got.UnansweredAsks) != 1 || got.UnansweredAsks[0].Question != "q1" {
		t.Fatalf("unanswered asks wrong: %+v", got.UnansweredAsks)
	}

	// ?include_admins=1 folds the admin's extra view back in → 6 today.
	resp2, err := c.Get(ts.URL + "/api/admin/stats?include_admins=1")
	if err != nil {
		t.Fatalf("get stats (include_admins): %v", err)
	}
	defer resp2.Body.Close()
	var withAdmins adminStats
	if err := json.NewDecoder(resp2.Body).Decode(&withAdmins); err != nil {
		t.Fatalf("decode include_admins: %v", err)
	}
	if withAdmins.Views[statsWindowDays-1] != 6 {
		t.Fatalf("include_admins today's views=%d want 6", withAdmins.Views[statsWindowDays-1])
	}
}

// TestAdminStats_AdminOnly — non-admins are forbidden.
func TestAdminStats_AdminOnly(t *testing.T) {
	ts, d := newWiredServer(t)
	seedUser(t, d, "bob", "testpass123", false)
	c := loginClient(t, ts, "bob", "testpass123")
	resp, err := c.Get(ts.URL + "/api/admin/stats")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d want 403", resp.StatusCode)
	}
}
