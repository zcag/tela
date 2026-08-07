package api

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"

	"github.com/zcag/tela/backend/internal/testdb"
)

// stampRunUsage records embed-token usage on an existing run, the shape the
// token meter reads (stats_json is only written by a run that got far enough to
// report, which is why a failed run contributes nothing).
func stampRunUsage(t *testing.T, d *sql.DB, runID, embedTokens int64) {
	t.Helper()
	stats := `{"files":10,"usage":{"embed_tokens":` + strconv.FormatInt(embedTokens, 10) + `}}`
	if _, err := d.ExecContext(context.Background(),
		`UPDATE atlas_runs SET stats_json = $2 WHERE id = $1`, runID, stats); err != nil {
		t.Fatalf("stamp usage: %v", err)
	}
}

// seedQuotaSource creates a user-owned project plus one source — the minimum
// shape the quota counters join over — reusing the existing project/source seeders.
func seedQuotaSource(t *testing.T, d *sql.DB, ownerID int64) int64 {
	t.Helper()
	projectID := seedAtlasProject(t, d, "p", accountUser, ownerID, 0, 0)
	return seedAtlasSource(t, d, projectID, "https://example.com/r", "ref1")
}

// TestAtlasQuota_FailedRunsAreFree pins the fairness rule: a run that failed is
// not consumption. The runs that motivated this quota were an hourly retry loop
// dying on OUR encoding bug having embedded nothing — metering those against a
// user's monthly allowance would charge them for our defect.
func TestAtlasQuota_FailedRunsAreFree(t *testing.T) {
	d := testdb.New(t)
	uid := seedUser(t, d, "quota-user", "pw", false)
	src := seedQuotaSource(t, d, uid)
	acct := account{Kind: accountUser, ID: uid}

	for i := 0; i < 5; i++ {
		seedAtlasRun(t, d, src, "failed")
	}
	n, err := countAtlasRunsThisMonth(context.Background(), d, acct)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("5 failed runs counted as %d, want 0", n)
	}

	stampRunUsage(t, d, seedAtlasRun(t, d, src, "done"), 1000)
	seedAtlasRun(t, d, src, "running")
	if n, _ = countAtlasRunsThisMonth(context.Background(), d, acct); n != 2 {
		t.Fatalf("done+running counted as %d, want 2 (in-flight must count so a burst can't slip through)", n)
	}
}

// TestAtlasQuota_EmbedTokenSum proves the token meter totals only runs that
// reported usage, and that a nil (never-run) account reads 0 rather than erroring
// on a NULL SUM.
func TestAtlasQuota_EmbedTokenSum(t *testing.T) {
	d := testdb.New(t)
	uid := seedUser(t, d, "quota-user", "pw", false)
	acct := account{Kind: accountUser, ID: uid}

	got, err := sumEmbedTokensThisMonth(context.Background(), d, acct)
	if err != nil {
		t.Fatalf("empty sum errored: %v", err)
	}
	if got != 0 {
		t.Fatalf("empty sum = %d, want 0", got)
	}

	src := seedQuotaSource(t, d, uid)
	stampRunUsage(t, d, seedAtlasRun(t, d, src, "done"), 2_000_000)
	stampRunUsage(t, d, seedAtlasRun(t, d, src, "done"), 1_500_000)
	seedAtlasRun(t, d, src, "failed")
	if got, err = sumEmbedTokensThisMonth(context.Background(), d, acct); err != nil {
		t.Fatalf("sum: %v", err)
	}
	if got != 3_500_000 {
		t.Fatalf("sum = %d, want 3500000", got)
	}
}

// TestAtlasQuota_GateBlocksAndMessages checks the free plan actually refuses once
// the monthly budget is spent, that the message names real numbers (a bare
// "quota exceeded" leaves the user guessing what to do), and that an unlimited
// plan is untouched.
func TestAtlasQuota_GateBlocksAndMessages(t *testing.T) {
	d := testdb.New(t)
	s := &Server{DB: d}
	uid := seedUser(t, d, "quota-user", "pw", false)
	setPlan(t, d, accountUser, uid, "personal_free")
	acct := account{Kind: accountUser, ID: uid}
	src := seedQuotaSource(t, d, uid)

	if ae := s.checkAtlasRunQuota(context.Background(), acct); ae != nil {
		t.Fatalf("fresh free account blocked: %s", ae.Message)
	}

	// Blow the 3M token budget from migration 0070.
	stampRunUsage(t, d, seedAtlasRun(t, d, src, "done"), 4_000_000)
	ae := s.checkAtlasRunQuota(context.Background(), acct)
	if ae == nil {
		t.Fatal("over-budget account was not blocked")
	}
	if ae.Status != 402 || ae.Code != "quota_exceeded" {
		t.Fatalf("got %d/%s, want 402/quota_exceeded", ae.Status, ae.Code)
	}
	for _, want := range []string{"3M", "4M", "resets"} {
		if !strings.Contains(ae.Message, want) {
			t.Fatalf("message %q missing %q — it must say the limit, the usage, and what happens next", ae.Message, want)
		}
	}

	// An unlimited tier has NULL caps and must be unaffected.
	setPlan(t, d, accountUser, uid, "personal_unlimited")
	if ae := s.checkAtlasRunQuota(context.Background(), acct); ae != nil {
		t.Fatalf("unlimited plan blocked: %s", ae.Message)
	}
}
