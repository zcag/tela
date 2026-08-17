package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zcag/tela/backend/internal/atlas/core"
)

// TestProbeStalenessRecordsFailure locks the bookkeeping a failed drift probe
// owes the UI. Before this, a failure was a log line and nothing else: the row
// only got its checked_at stamped, so a source whose repo had been deleted kept
// rendering as its last successful run — seven of them did, on the live
// instance, for weeks.
//
// Three properties, in order of how badly each one bit:
//   - a failure is RECORDED (probe_error), not just logged;
//   - a failure does NOT touch stale_since — a probe that couldn't reach upstream
//     knows nothing about drift, and neither "in sync" nor "behind" is honest;
//   - a later success CLEARS probe_error, so a source that comes back stops
//     being reported as unreachable.
func TestProbeStalenessRecordsFailure(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	m := srv.atlas
	ctx := context.Background()

	owner := seedUser(t, d, "alice", "alicepw12", false)
	space := seedSpace(t, d, "Repo Docs", "repo-docs", owner)
	pid := seedAtlasProject(t, d, "Repo Docs", accountUser, owner, space, 1)
	src := seedAtlasSource(t, d, pid, "https://github.com/gone/repo.git", "abc1234")

	// The source was already flagged behind upstream by an earlier, working probe
	// — the exact state of the two live sources that went on rendering "Stale"
	// after their repos were deleted.
	if _, err := d.Exec(`UPDATE atlas_sources SET stale_since = '2026-07-01 10:00:00' WHERE id = $1`, src); err != nil {
		t.Fatalf("seed stale_since: %v", err)
	}

	read := func() (probeErr, staleSince, checkedAt string) {
		t.Helper()
		if err := d.QueryRow(
			`SELECT probe_error, stale_since, upstream_checked_at FROM atlas_sources WHERE id = $1`,
			src).Scan(&probeErr, &staleSince, &checkedAt); err != nil {
			t.Fatalf("read source: %v", err)
		}
		return
	}

	m.hasChanges = func(context.Context, core.Source, string) (bool, error) {
		return false, errors.New("git ls-remote: exit status 128: remote: Repository not found.")
	}
	m.probeStaleness(ctx, src)

	probeErr, staleSince, checkedAt := read()
	if !strings.Contains(probeErr, "Repository not found") {
		t.Errorf("probe_error after failure = %q, want the git error", probeErr)
	}
	if staleSince != "2026-07-01 10:00:00" {
		t.Errorf("stale_since = %q, want it untouched by a failed probe", staleSince)
	}
	if checkedAt == "" {
		t.Error("upstream_checked_at not stamped — a failing source would be re-probed every minute")
	}

	// Upstream comes back, still ahead of `ref`: the error clears, the drift stands.
	m.hasChanges = func(context.Context, core.Source, string) (bool, error) { return true, nil }
	m.probeStaleness(ctx, src)

	probeErr, staleSince, _ = read()
	if probeErr != "" {
		t.Errorf("probe_error after recovery = %q, want cleared", probeErr)
	}
	if staleSince != "2026-07-01 10:00:00" {
		t.Errorf("stale_since = %q, want the original drift preserved", staleSince)
	}

	// Back in sync: both the error and the drift clear.
	m.hasChanges = func(context.Context, core.Source, string) (bool, error) { return false, nil }
	m.probeStaleness(ctx, src)

	if probeErr, staleSince, _ = read(); probeErr != "" || staleSince != "" {
		t.Errorf("after in-sync probe: probe_error=%q stale_since=%q, want both empty", probeErr, staleSince)
	}
}

// TestProbeErrText: the stored text is trimmed and bounded (git returns several
// lines of remote output), and never empty for a real error — '' is the column's
// "the probe succeeded", so an error rendering as '' would resurrect the bug.
func TestProbeErrText(t *testing.T) {
	if got := probeErrText(errors.New("  fatal: not found\n")); got != "fatal: not found" {
		t.Errorf("trim: got %q", got)
	}
	if got := probeErrText(errors.New("   ")); got != "probe failed" {
		t.Errorf("blank error: got %q, want a non-empty placeholder", got)
	}
	long := probeErrText(errors.New(strings.Repeat("x", atlasProbeErrMax+200)))
	if len([]rune(long)) > atlasProbeErrMax+1 {
		t.Errorf("truncation: got %d chars, want <= %d", len([]rune(long)), atlasProbeErrMax+1)
	}
	if !strings.HasSuffix(long, "…") {
		t.Errorf("truncation: %q should be marked elided", long[len(long)-10:])
	}
}
