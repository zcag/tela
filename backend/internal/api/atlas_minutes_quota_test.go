package api

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"

	"github.com/zcag/tela/backend/internal/testdb"
)

// stampRunDuration records the GPU time a run took, the shape the minutes meter
// reads. duration_sec is written by the engine on every run that reports stats.
func stampRunDuration(t *testing.T, d *sql.DB, runID int64, durationSec float64) {
	t.Helper()
	stats := `{"files":10,"duration_sec":` + strconv.FormatFloat(durationSec, 'f', 2, 64) +
		`,"usage":{"embed_tokens":1000}}`
	if _, err := d.ExecContext(context.Background(),
		`UPDATE atlas_runs SET stats_json = $2 WHERE id = $1`, runID, stats); err != nil {
		t.Fatalf("stamp duration: %v", err)
	}
}

// TestAtlasMinutes_SumsRealGPUTime is the core of the meter: quota is charged in
// the unit that is actually scarce, so runs of wildly different cost are priced
// differently. Measured spread over the real corpus was 4.7 to 80.2 minutes.
func TestAtlasMinutes_SumsRealGPUTime(t *testing.T) {
	d := testdb.New(t)
	uid := seedUser(t, d, "minutes-user", "pw", false)
	src := seedQuotaSource(t, d, uid)
	acct := account{Kind: accountUser, ID: uid}

	stampRunDuration(t, d, seedAtlasRun(t, d, src, "done"), 282)  // 4.7 min
	stampRunDuration(t, d, seedAtlasRun(t, d, src, "done"), 4812) // 80.2 min

	got, err := sumAtlasMinutesThisMonth(context.Background(), d, acct)
	if err != nil {
		t.Fatalf("sum: %v", err)
	}
	if got != 84 {
		t.Fatalf("summed %d minutes, want 84 (4.7 + 80.2)", got)
	}
}

// A cheap run and an expensive one must NOT cost the same, which is precisely
// what the run-count cap got wrong: it funded one 80-minute run and one
// 4.7-minute run out of the same single unit of quota.
func TestAtlasMinutes_PricesCheapAndExpensiveRunsDifferently(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()

	cheapUID := seedUser(t, d, "cheap-user", "pw", false)
	cheapSrc := seedQuotaSource(t, d, cheapUID)
	expensiveUID := seedUser(t, d, "expensive-user", "pw", false)
	expensiveSrc := seedQuotaSource(t, d, expensiveUID)

	for i := 0; i < 10; i++ {
		stampRunDuration(t, d, seedAtlasRun(t, d, cheapSrc, "done"), 282) // 10 x 4.7 = 47 min
	}
	for i := 0; i < 10; i++ {
		stampRunDuration(t, d, seedAtlasRun(t, d, expensiveSrc, "done"), 4812) // 10 x 80.2 = 802 min
	}

	cheap, _ := sumAtlasMinutesThisMonth(ctx, d, account{Kind: accountUser, ID: cheapUID})
	expensive, _ := sumAtlasMinutesThisMonth(ctx, d, account{Kind: accountUser, ID: expensiveUID})

	if cheap != 47 || expensive != 802 {
		t.Fatalf("cheap=%d expensive=%d, want 47 and 802", cheap, expensive)
	}
	// Same run count, ~17x the cost — the whole reason this meter exists.
	if expensive <= cheap*10 {
		t.Fatalf("identical run counts priced within 10x (%d vs %d); the meter is not tracking cost", cheap, expensive)
	}
}

// Failed runs are free, matching the run counter's fairness rule: an hourly
// retry loop dying on our own bug must not eat a user's allowance.
func TestAtlasMinutes_FailedRunsAreFree(t *testing.T) {
	d := testdb.New(t)
	uid := seedUser(t, d, "minutes-failed", "pw", false)
	src := seedQuotaSource(t, d, uid)

	for i := 0; i < 5; i++ {
		stampRunDuration(t, d, seedAtlasRun(t, d, src, "failed"), 4812)
	}
	got, err := sumAtlasMinutesThisMonth(context.Background(), d, account{Kind: accountUser, ID: uid})
	if err != nil {
		t.Fatalf("sum: %v", err)
	}
	if got != 0 {
		t.Fatalf("5 failed runs charged %d minutes, want 0", got)
	}
}

// A run still in flight has no duration_sec yet, so it contributes nothing until
// it finishes. Pinned deliberately: it is a bounded under-count (one run per
// source), not an oversight, and the run COUNT gate is what stops a burst.
func TestAtlasMinutes_InFlightRunContributesNothing(t *testing.T) {
	d := testdb.New(t)
	uid := seedUser(t, d, "minutes-inflight", "pw", false)
	src := seedQuotaSource(t, d, uid)

	seedAtlasRun(t, d, src, "running") // no stats_json yet
	got, err := sumAtlasMinutesThisMonth(context.Background(), d, account{Kind: accountUser, ID: uid})
	if err != nil {
		t.Fatalf("sum: %v", err)
	}
	if got != 0 {
		t.Fatalf("in-flight run charged %d minutes, want 0", got)
	}
}

// The plans table must actually carry the new cap, and the tiers sold as
// unlimited must stay unlimited.
func TestAtlasMinutes_PlanValues(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()

	for _, tc := range []struct {
		key  string
		want int64
	}{
		{"personal_free", 180},
		{"personal_plus", 900},
		{"org_free", 180},
		{"org_team", 1800},
	} {
		var got sql.NullInt64
		if err := d.QueryRowContext(ctx,
			`SELECT max_atlas_minutes_per_month FROM plans WHERE key = $1`, tc.key).Scan(&got); err != nil {
			t.Fatalf("%s: %v", tc.key, err)
		}
		if !got.Valid || got.Int64 != tc.want {
			t.Fatalf("%s minutes cap = %v, want %d", tc.key, got, tc.want)
		}
	}
	for _, key := range []string{"personal_unlimited", "org_enterprise"} {
		var got sql.NullInt64
		if err := d.QueryRowContext(ctx,
			`SELECT max_atlas_minutes_per_month FROM plans WHERE key = $1`, key).Scan(&got); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if got.Valid {
			t.Fatalf("%s has a minutes cap of %d; unlimited tiers must stay NULL", key, got.Int64)
		}
	}
}

// TestAtlasMinutes_GateRefusesWhenOverBudget is the end-to-end proof that the
// meter is actually wired to a refusal, not just computed. It exists because
// production could not demonstrate it: the account that motivated the cap sits
// over budget, but regenProject only starts a delta for a source whose
// stale_since is set, and that repo stopped changing — so the gate is never
// reached and "no run happened" proves nothing either way.
func TestAtlasMinutes_GateRefusesWhenOverBudget(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	s := &Server{DB: d}

	uid := seedUser(t, d, "over-budget", "pw", false)
	src := seedQuotaSource(t, d, uid)
	acct := account{Kind: accountUser, ID: uid}

	// personal_free caps 180 minutes; spend 200 across two finished runs. NULL,
	// not '', is how "no trial" is stored — planFor's CASE reads trial_ends_at as
	// a timestamp, and '' would exercise a state no writer produces.
	if _, err := d.ExecContext(ctx, `UPDATE users SET plan_key='personal_free', trial_plan_key=NULL, trial_ends_at=NULL WHERE id=$1`, uid); err != nil {
		t.Fatalf("set plan: %v", err)
	}
	stampRunDuration(t, d, seedAtlasRun(t, d, src, "done"), 6000) // 100 min
	stampRunDuration(t, d, seedAtlasRun(t, d, src, "done"), 6000) // 100 min

	ae := s.checkAtlasRunQuota(ctx, acct)
	if ae == nil {
		t.Fatal("quota gate allowed a run at 200 of 180 minutes — the meter is not wired to a refusal")
	}
	if ae.Status != 402 || ae.Code != "quota_exceeded" {
		t.Fatalf("got %d/%s, want 402/quota_exceeded", ae.Status, ae.Code)
	}
	if !strings.Contains(ae.Message, "minutes") {
		t.Fatalf("refusal doesn't mention minutes, so the user can't tell which limit bit: %q", ae.Message)
	}

	// And it must NOT refuse an account comfortably inside the budget.
	uid2 := seedUser(t, d, "under-budget", "pw", false)
	src2 := seedQuotaSource(t, d, uid2)
	if _, err := d.ExecContext(ctx, `UPDATE users SET plan_key='personal_free', trial_plan_key=NULL, trial_ends_at=NULL WHERE id=$1`, uid2); err != nil {
		t.Fatalf("set plan: %v", err)
	}
	stampRunDuration(t, d, seedAtlasRun(t, d, src2, "done"), 600) // 10 min
	if ae := s.checkAtlasRunQuota(ctx, account{Kind: accountUser, ID: uid2}); ae != nil {
		t.Fatalf("refused an account at 10 of 180 minutes: %v", ae.Message)
	}
}

// An empty-string trial_ends_at must not blow up the gate. '' is this schema's
// convention for an empty TEXT datetime elsewhere, and ''::timestamp raises in
// Postgres — without the NULLIF guard in planFor this returns 500 instead of a
// clean allow/refuse, which is how a quota check silently becomes an outage.
func TestAtlasQuota_EmptyTrialEndsAtDoesNotBreakTheGate(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	s := &Server{DB: d}

	uid := seedUser(t, d, "empty-trial", "pw", false)
	seedQuotaSource(t, d, uid)
	if _, err := d.ExecContext(ctx,
		`UPDATE users SET plan_key='personal_free', trial_plan_key=NULL, trial_ends_at='' WHERE id=$1`, uid); err != nil {
		t.Fatalf("set plan: %v", err)
	}
	if ae := s.checkAtlasRunQuota(ctx, account{Kind: accountUser, ID: uid}); ae != nil && ae.Status == 500 {
		t.Fatalf("empty trial_ends_at produced a 500: %s", ae.Message)
	}
}
