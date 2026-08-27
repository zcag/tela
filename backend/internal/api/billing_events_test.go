package api

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/zcag/tela/backend/internal/testdb"
)

// billing_events_test.go — the money-path trail. These tests exist because the
// reconciler previously wrote plan state and recorded nothing, so every question
// about a funnel step other than "someone clicked" was unanswerable after the
// fact. Each test pins one step to an event that survives the request.

// billingEvents returns the detail strings recorded for a type, oldest first.
func billingEvents(t *testing.T, d *sql.DB, typ string) []string {
	t.Helper()
	rows, err := d.QueryContext(context.Background(),
		`SELECT detail FROM events WHERE type = $1 ORDER BY id`, typ)
	if err != nil {
		t.Fatalf("load %s events: %v", typ, err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var detail string
		if err := rows.Scan(&detail); err != nil {
			t.Fatalf("scan %s event: %v", typ, err)
		}
		out = append(out, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s events: %v", typ, err)
	}
	return out
}

// setTrial gives a user a live trial ending `days` from now.
func setTrial(t *testing.T, d *sql.DB, uid int64, days int) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(), `
		UPDATE users SET trial_plan_key = 'personal_plus',
		       trial_ends_at = to_char((now() AT TIME ZONE 'UTC') + make_interval(days => $1), 'YYYY-MM-DD HH24:MI:SS')
		 WHERE id = $2`, days, uid); err != nil {
		t.Fatalf("set trial: %v", err)
	}
}

func TestReconcileRecordsLifecycleEvents(t *testing.T) {
	s, d := wiredBillingServer(t)
	ctx := context.Background()
	uid := seedUser(t, d, "alice", "pw123456", false)
	ext := acctExternalID(account{Kind: accountUser, ID: uid})

	for _, e := range []struct{ typ, product, status string }{
		{"subscription.active", "prod_plus", "active"},
		{"subscription.canceled", "prod_plus", "active"},
		{"subscription.revoked", "prod_plus", "canceled"},
	} {
		if err := s.reconcileBilling(ctx, subEvent(e.typ, ext, e.product, e.status, false)); err != nil {
			t.Fatalf("%s: %v", e.typ, err)
		}
	}
	order := subEvent("order.paid", ext, "prod_plus", "active", false)
	if err := s.reconcileBilling(ctx, order); err != nil {
		t.Fatalf("order.paid: %v", err)
	}

	for _, typ := range []string{
		evtBillingSubUpdate, evtBillingSubCanceled, evtBillingSubRevoked,
	} {
		if got := billingEvents(t, d, typ); len(got) != 1 {
			t.Fatalf("%s: want 1 event, got %d", typ, len(got))
		}
	}
	// order.paid only records once the account actually carries a subscription;
	// the revoke above cleared it, so this asserts the reconciler's own guard is
	// what gates the event, not a second copy of the rule here.
	if got := billingEvents(t, d, evtBillingPaid); len(got) != 1 {
		t.Fatalf("%s: want 1 event, got %d", evtBillingPaid, len(got))
	}

	upd := billingEvents(t, d, evtBillingSubUpdate)[0]
	if !strings.Contains(upd, "status=active") || !strings.Contains(upd, "plan=personal_plus") {
		t.Fatalf("subscription_update detail lost its state: %q", upd)
	}
	if !strings.Contains(upd, "granted=1") {
		t.Fatalf("subscription_update should record whether the plan was granted: %q", upd)
	}
}

// The one number that cannot be recovered later: converting mid-trial NULLs
// trial_ends_at, so unless the forfeited days are captured in the same call,
// nothing anywhere can say whether a subscriber converted early or at expiry.
func TestReconcileRecordsForfeitedTrialDays(t *testing.T) {
	s, d := wiredBillingServer(t)
	ctx := context.Background()
	uid := seedUser(t, d, "jason", "pw123456", false)
	setTrial(t, d, uid, 20)
	ext := acctExternalID(account{Kind: accountUser, ID: uid})

	if err := s.reconcileBilling(ctx, subEvent("subscription.active", ext, "prod_plus", "active", false)); err != nil {
		t.Fatalf("active: %v", err)
	}
	got := billingEvents(t, d, evtBillingSubUpdate)
	if len(got) != 1 || !strings.Contains(got[0], "trial_days_left=19") {
		// 19, not 20: a trial ending 20 days out has 19 whole days left.
		t.Fatalf("want trial_days_left=19 in the conversion event, got %q", got)
	}

	// And a conversion with no trial in flight must not invent one.
	uid2 := seedUser(t, d, "nontrial", "pw123456", false)
	ext2 := acctExternalID(account{Kind: accountUser, ID: uid2})
	if err := s.reconcileBilling(ctx, subEvent("subscription.active", ext2, "prod_plus", "active", false)); err != nil {
		t.Fatalf("active (no trial): %v", err)
	}
	for _, detail := range billingEvents(t, d, evtBillingSubUpdate) {
		if strings.Contains(detail, "trial_days_left=") && !strings.Contains(detail, "trial_days_left=19") {
			t.Fatalf("no-trial conversion recorded a trial: %q", detail)
		}
	}
}

// checkout.updated is the only signal separating "was blocked" from "looked and
// declined". `open` is the session's own birth and says nothing, so it must not
// land as a terminal verdict.
func TestCheckoutUpdatedRecordsOnlyTerminalStatus(t *testing.T) {
	s, d := wiredBillingServer(t)
	ctx := context.Background()
	uid := seedUser(t, d, "bob", "pw123456", false)
	ext := acctExternalID(account{Kind: accountUser, ID: uid})

	if err := s.reconcileBilling(ctx, subEvent("checkout.updated", ext, "prod_plus", "open", false)); err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := billingEvents(t, d, evtBillingCheckoutStatus); len(got) != 0 {
		t.Fatalf("status=open should record nothing, got %v", got)
	}

	if err := s.reconcileBilling(ctx, subEvent("checkout.updated", ext, "prod_plus", "expired", false)); err != nil {
		t.Fatalf("expired: %v", err)
	}
	got := billingEvents(t, d, evtBillingCheckoutStatus)
	if len(got) != 1 || !strings.Contains(got[0], "status=expired") {
		t.Fatalf("want one status=expired event, got %v", got)
	}
	// A checkout event must not move plan state — the subscription events stay
	// authoritative.
	if plan, _, _ := acctPlan(t, d, "users", uid); plan != "personal_free" {
		t.Fatalf("checkout.updated changed the plan to %q", plan)
	}
}

// Polar exposes abandonment two ways and which one arrives depends on the
// dashboard's ticked events, so the dedicated event must record too — including
// when it carries no status of its own.
func TestCheckoutExpiredEventRecords(t *testing.T) {
	s, d := wiredBillingServer(t)
	ctx := context.Background()
	uid := seedUser(t, d, "carol", "pw123456", false)
	ext := acctExternalID(account{Kind: accountUser, ID: uid})

	e := subEvent("checkout.expired", ext, "prod_plus", "", false)
	if err := s.reconcileBilling(ctx, e); err != nil {
		t.Fatalf("checkout.expired: %v", err)
	}
	got := billingEvents(t, d, evtBillingCheckoutStatus)
	if len(got) != 1 || !strings.Contains(got[0], "status=expired") {
		t.Fatalf("checkout.expired with no status should record status=expired, got %v", got)
	}
}

func TestTrialSweepEmitsEachMomentOnce(t *testing.T) {
	s, d := wiredBillingServer(t)
	ctx := context.Background()
	ending := seedUser(t, d, "ending", "pw123456", false)
	expired := seedUser(t, d, "expired", "pw123456", false)
	early := seedUser(t, d, "early", "pw123456", false)
	setTrial(t, d, ending, 3)   // inside the 7-day banner window
	setTrial(t, d, expired, -2) // lapsed two days ago
	setTrial(t, d, early, 25)   // nowhere near either line

	if err := s.sweepTrials(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := billingEvents(t, d, evtBillingTrialEnding); len(got) != 1 {
		t.Fatalf("trial_ending: want 1, got %d (%v)", len(got), got)
	}
	if got := billingEvents(t, d, evtBillingTrialExpired); len(got) != 1 {
		t.Fatalf("trial_expired: want 1, got %d (%v)", len(got), got)
	}

	// Idempotent: the sweep runs every few hours forever, so a second pass over
	// the same crossings must add nothing.
	if err := s.sweepTrials(ctx); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if got := billingEvents(t, d, evtBillingTrialEnding); len(got) != 1 {
		t.Fatalf("trial_ending re-emitted: %v", got)
	}
	if got := billingEvents(t, d, evtBillingTrialExpired); len(got) != 1 {
		t.Fatalf("trial_expired re-emitted: %v", got)
	}
}

// A converted trial has nothing left to expire — reconcileBilling NULLs
// trial_ends_at, which must take the user out of the sweep entirely. Otherwise
// every paying customer would be reported as a lapsed trial a month later.
func TestTrialSweepIgnoresConvertedTrials(t *testing.T) {
	s, d := wiredBillingServer(t)
	ctx := context.Background()
	uid := seedUser(t, d, "converted", "pw123456", false)
	setTrial(t, d, uid, 2)
	ext := acctExternalID(account{Kind: accountUser, ID: uid})
	if err := s.reconcileBilling(ctx, subEvent("subscription.active", ext, "prod_plus", "active", false)); err != nil {
		t.Fatalf("active: %v", err)
	}
	if err := s.sweepTrials(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := billingEvents(t, d, evtBillingTrialEnding); len(got) != 0 {
		t.Fatalf("a subscriber was reported as a trial ending: %v", got)
	}
}

// A long-lapsed trial is history, not news: a first sweep on an existing
// database must not replay years of expiries as though they happened today.
func TestTrialSweepIgnoresAncientTrials(t *testing.T) {
	s, d := wiredBillingServer(t)
	ctx := context.Background()
	uid := seedUser(t, d, "ancient", "pw123456", false)
	setTrial(t, d, uid, -(trialSweepWindowDays + 5))
	if err := s.sweepTrials(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := billingEvents(t, d, evtBillingTrialExpired); len(got) != 0 {
		t.Fatalf("expiry outside the window was replayed: %v", got)
	}
}

// The retention GC bounds high-volume types; billing rows are the series whose
// whole value is longitudinal and must outlive it.
func TestEventsGCKeepsBillingRows(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	old := time.Now().UTC().AddDate(0, 0, -400).Format("2006-01-02 15:04:05")
	for _, typ := range []string{evtBillingPaid, evtPageView} {
		if _, err := d.ExecContext(ctx,
			`INSERT INTO events (type, created_at) VALUES ($1, $2)`, typ, old); err != nil {
			t.Fatalf("seed %s: %v", typ, err)
		}
	}
	if err := purgeEventsOlderThan(ctx, d, 180); err != nil {
		t.Fatalf("gc: %v", err)
	}
	if got := billingEvents(t, d, evtBillingPaid); len(got) != 1 {
		t.Fatalf("GC deleted a billing event")
	}
	if got := billingEvents(t, d, evtPageView); len(got) != 0 {
		t.Fatalf("GC kept an expired page.view: %v", got)
	}
}

// The billing screen previously showed "Current: Personal" beside "Upgrade to
// Personal · $8/mo" and never said "trial", because its only trial source was
// the banner — which the backend gates to the last 7 days. The plan screen must
// know for the WHOLE trial, while the banner stays gated.
func TestUsageCarriesTrialOutsideTheBannerWindow(t *testing.T) {
	s, d := wiredBillingServer(t)
	ctx := context.Background()
	uid := seedUser(t, d, "fresh", "pw123456", false)
	setTrial(t, d, uid, 25) // day 5 of 30 — nowhere near the banner window

	if got := s.userTrialStatus(ctx, uid); got != nil {
		t.Fatalf("banner must stay quiet this far out, got %+v", got)
	}
	out, err := s.buildUsage(ctx, account{Kind: accountUser, ID: uid})
	if err != nil {
		t.Fatalf("buildUsage: %v", err)
	}
	if out.Trial == nil {
		t.Fatal("billing screen has no trial to show — the misleading state is back")
	}
	if out.Trial.PlanKey != "personal_plus" {
		t.Fatalf("trial plan_key = %q; the UI compares it against the tier it sells", out.Trial.PlanKey)
	}
	// The trialled tier IS the tier on sale — that identity is the whole reason
	// the screen read as broken, so assert it rather than assume it.
	if out.Plan.Key != out.Trial.PlanKey {
		t.Fatalf("effective plan %q != trialled tier %q", out.Plan.Key, out.Trial.PlanKey)
	}
}

// An org is never trialled; a trial block on an org payload would badge a team's
// paid plan as a trial.
func TestOrgUsageHasNoTrial(t *testing.T) {
	s, d := wiredBillingServer(t)
	ctx := context.Background()
	uid := seedUser(t, d, "orgowner", "pw123456", false)
	setTrial(t, d, uid, 25)
	orgID := seedOrg(t, d, "Acme", "acme")
	seedOrgMember(t, d, orgID, uid, "admin")

	out, err := s.buildUsage(ctx, account{Kind: accountOrg, ID: orgID})
	if err != nil {
		t.Fatalf("buildUsage(org): %v", err)
	}
	if out.Trial != nil {
		t.Fatalf("org payload carries a trial: %+v", out.Trial)
	}
}
