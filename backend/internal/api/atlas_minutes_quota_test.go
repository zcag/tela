package api

import (
	"context"
	"database/sql"
	"strconv"
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
