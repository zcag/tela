package api

import (
	"context"
	"testing"

	"github.com/zcag/tela/backend/internal/auth"
	"github.com/zcag/tela/backend/internal/testdb"
)

// TestAtlasBudget_ProjectsTheIncidentConfig reproduces the config that started
// all this — one source, hourly, ~50 min/run — and asserts the projection lands
// on the number measured in production (36,706 min/month against a 900 cap).
// If this drifts, the warning is lying about the thing it exists to warn about.
func TestAtlasBudget_ProjectsTheIncidentConfig(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	s := &Server{DB: d}

	uid := seedUser(t, d, "budget-incident", "pw", false)
	pid := seedAtlasProject(t, d, "test", accountUser, uid, 0, 0)
	src := seedAtlasSource(t, d, pid, "https://example.com/repowise", "ref1")
	if _, err := d.ExecContext(ctx,
		`UPDATE atlas_projects SET cadence='hourly', auto_update=1 WHERE id=$1`, pid); err != nil {
		t.Fatalf("set cadence: %v", err)
	}
	if _, err := d.ExecContext(ctx,
		`UPDATE users SET plan_key='personal_plus', trial_plan_key=NULL, trial_ends_at=NULL WHERE id=$1`, uid); err != nil {
		t.Fatalf("set plan: %v", err)
	}
	// Three finished runs at 50.3 min — enough history to be trusted, not estimated.
	for i := 0; i < 3; i++ {
		stampRunDuration(t, d, seedAtlasRun(t, d, src, "done"), 3018)
	}

	b, err := s.atlasBudgetFor(ctx, account{Kind: accountUser, ID: uid})
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if b.CapMinutes == nil || *b.CapMinutes != 900 {
		t.Fatalf("cap = %v, want 900", b.CapMinutes)
	}
	if b.Estimated {
		t.Fatal("marked estimated despite 3 finished runs of real history")
	}
	// 730 runs/month x 50.3 min = 36,719 (production measured 36,706 from a
	// slightly different mean; within a rounding of each other).
	if b.Projected < 36000 || b.Projected > 37500 {
		t.Fatalf("projected %.0f min, want ~36,700", b.Projected)
	}
	if !b.Over {
		t.Fatal("36,700 projected against a 900 cap did not register as over")
	}
	if b.Suggestion == nil {
		t.Fatal("no suggestion offered for a config 40x over cap")
	}
	// 4.3 runs/month x 50.3 = 216 min — weekly is the fastest cadence that fits.
	if b.Suggestion.Cadence != "weekly" {
		t.Fatalf("suggested %q, want weekly (daily would be 1,509 min, still over)",
			b.Suggestion.Cadence)
	}
	if b.Suggestion.Projected > 900 {
		t.Fatalf("suggestion projects %.0f min, which is still over the 900 cap", b.Suggestion.Projected)
	}
	if len(b.Suggestion.AppliesTo) != 1 || b.Suggestion.AppliesTo[0] != pid {
		t.Fatalf("applies_to = %v, want [%d]", b.Suggestion.AppliesTo, pid)
	}
}

// A small repo on daily must NOT be warned about: 30 runs x 7 min = 210 of 900.
// This is the false-positive guard — a warning that fires on a fine config is
// the failure mode that makes people ignore the real one.
func TestAtlasBudget_SmallRepoOnDailyIsNotWarned(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	s := &Server{DB: d}

	uid := seedUser(t, d, "budget-small", "pw", false)
	pid := seedAtlasProject(t, d, "small", accountUser, uid, 0, 0)
	src := seedAtlasSource(t, d, pid, "https://example.com/small", "ref1")
	if _, err := d.ExecContext(ctx,
		`UPDATE atlas_projects SET cadence='daily', auto_update=1 WHERE id=$1`, pid); err != nil {
		t.Fatalf("set cadence: %v", err)
	}
	if _, err := d.ExecContext(ctx,
		`UPDATE users SET plan_key='personal_plus', trial_plan_key=NULL, trial_ends_at=NULL WHERE id=$1`, uid); err != nil {
		t.Fatalf("set plan: %v", err)
	}
	for i := 0; i < 3; i++ {
		stampRunDuration(t, d, seedAtlasRun(t, d, src, "done"), 420) // 7 min
	}

	b, err := s.atlasBudgetFor(ctx, account{Kind: accountUser, ID: uid})
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if b.Over || b.Suggestion != nil {
		t.Fatalf("warned about %.0f min against a 900 cap — false positive", b.Projected)
	}
}

// A project with no run history must be flagged `estimated` rather than
// projected from nothing, and must not silently read as 0 cost.
func TestAtlasBudget_NoHistoryIsEstimatedNotZero(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	s := &Server{DB: d}

	uid := seedUser(t, d, "budget-fresh", "pw", false)
	pid := seedAtlasProject(t, d, "fresh", accountUser, uid, 0, 0)
	seedAtlasSource(t, d, pid, "https://example.com/fresh", "ref1")
	if _, err := d.ExecContext(ctx,
		`UPDATE atlas_projects SET cadence='daily', auto_update=1 WHERE id=$1`, pid); err != nil {
		t.Fatalf("set cadence: %v", err)
	}

	b, err := s.atlasBudgetFor(ctx, account{Kind: accountUser, ID: uid})
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if !b.Estimated {
		t.Fatal("a project with zero runs was not flagged as estimated")
	}
	if b.Projected <= 0 {
		t.Fatal("a project with zero runs projected 0 minutes — it must cost the corpus median, not nothing")
	}
}

// Manual-only projects (auto_update off) predict nothing and must never warn.
func TestAtlasBudget_ManualProjectCostsNothing(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	s := &Server{DB: d}

	uid := seedUser(t, d, "budget-manual", "pw", false)
	pid := seedAtlasProject(t, d, "manual", accountUser, uid, 0, 0)
	src := seedAtlasSource(t, d, pid, "https://example.com/manual", "ref1")
	if _, err := d.ExecContext(ctx,
		`UPDATE atlas_projects SET cadence='', auto_update=0 WHERE id=$1`, pid); err != nil {
		t.Fatalf("set cadence: %v", err)
	}
	for i := 0; i < 3; i++ {
		stampRunDuration(t, d, seedAtlasRun(t, d, src, "done"), 4812) // 80 min each
	}

	b, err := s.atlasBudgetFor(ctx, account{Kind: accountUser, ID: uid})
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if b.Projected != 0 || b.Over {
		t.Fatalf("manual project projected %.0f min; scheduled cost of an unscheduled project must be 0", b.Projected)
	}
}

// The suggester must never propose speeding a project UP as a "fix", and must
// leave already-slower projects out of applies_to.
func TestAtlasBudget_SuggestionNeverSpeedsUpOrTouchesSlowerProjects(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	s := &Server{DB: d}

	uid := seedUser(t, d, "budget-mixed", "pw", false)
	if _, err := d.ExecContext(ctx,
		`UPDATE users SET plan_key='personal_plus', trial_plan_key=NULL, trial_ends_at=NULL WHERE id=$1`, uid); err != nil {
		t.Fatalf("set plan: %v", err)
	}
	// One hourly offender, one already on monthly.
	fast := seedAtlasProject(t, d, "fast", accountUser, uid, 0, 0)
	fastSrc := seedAtlasSource(t, d, fast, "https://example.com/fast", "r")
	slow := seedAtlasProject(t, d, "slow", accountUser, uid, 0, 0)
	slowSrc := seedAtlasSource(t, d, slow, "https://example.com/slow", "r")
	if _, err := d.ExecContext(ctx, `UPDATE atlas_projects SET cadence='hourly', auto_update=1 WHERE id=$1`, fast); err != nil {
		t.Fatalf("cadence: %v", err)
	}
	if _, err := d.ExecContext(ctx, `UPDATE atlas_projects SET cadence='monthly', auto_update=1 WHERE id=$1`, slow); err != nil {
		t.Fatalf("cadence: %v", err)
	}
	for i := 0; i < 3; i++ {
		stampRunDuration(t, d, seedAtlasRun(t, d, fastSrc, "done"), 3018)
		stampRunDuration(t, d, seedAtlasRun(t, d, slowSrc, "done"), 3018)
	}

	b, err := s.atlasBudgetFor(ctx, account{Kind: accountUser, ID: uid})
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if b.Suggestion == nil {
		t.Fatal("no suggestion for an over-cap account")
	}
	for _, id := range b.Suggestion.AppliesTo {
		if id == slow {
			t.Fatal("suggestion would change a project already slower than the recommendation")
		}
	}
}

// TestAtlasBudget_CoversOrgOwnedProjects is why the endpoint returns a budget
// per account rather than one. Atlas home lists a user's personal projects AND
// their orgs' projects together, so a personal-only budget left an org-team
// customer looking at projects with no budget governing them — and if the UI had
// matched them to the viewer's personal allowance instead, it would have judged
// them against a cap that controls nothing about them.
func TestAtlasBudget_CoversOrgOwnedProjects(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	s := &Server{DB: d}

	uid := seedUser(t, d, "org-budget-user", "pw", false)
	orgID := seedOrg(t, d, "Acme", "acme")
	seedOrgMember(t, d, orgID, uid, "admin")
	if _, err := d.ExecContext(ctx, `UPDATE orgs SET plan_key='org_team' WHERE id=$1`, orgID); err != nil {
		t.Fatalf("set org plan: %v", err)
	}

	pid := seedAtlasProject(t, d, "org-proj", accountOrg, orgID, 0, 0)
	src := seedAtlasSource(t, d, pid, "https://example.com/org", "r")
	if _, err := d.ExecContext(ctx,
		`UPDATE atlas_projects SET cadence='hourly', auto_update=1 WHERE id=$1`, pid); err != nil {
		t.Fatalf("cadence: %v", err)
	}
	for i := 0; i < 3; i++ {
		stampRunDuration(t, d, seedAtlasRun(t, d, src, "done"), 3018) // 50.3 min
	}

	// The org's own budget must see the project and flag it.
	b, err := s.atlasBudgetFor(ctx, account{Kind: accountOrg, ID: orgID})
	if err != nil {
		t.Fatalf("org budget: %v", err)
	}
	if len(b.Projects) != 1 {
		t.Fatalf("org budget saw %d projects, want 1", len(b.Projects))
	}
	if b.CapMinutes == nil || *b.CapMinutes != 1800 {
		t.Fatalf("org cap = %v, want 1800 (org_team)", b.CapMinutes)
	}
	if !b.Over || b.Suggestion == nil {
		t.Fatalf("hourly at 50min/run projected %.0f against 1800 and did not warn", b.Projected)
	}

	// The member's PERSONAL budget must not claim the org's project.
	personal, err := s.atlasBudgetFor(ctx, account{Kind: accountUser, ID: uid})
	if err != nil {
		t.Fatalf("personal budget: %v", err)
	}
	if len(personal.Projects) != 0 {
		t.Fatalf("personal budget claimed %d org project(s); the org's cap governs those",
			len(personal.Projects))
	}
}

// The caller's account list must span personal + every org they belong to, since
// that is exactly the owner scope Atlas home renders.
func TestAtlasBudget_OwnerScopeMatchesProjectListing(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	s := &Server{DB: d}

	uid := seedUser(t, d, "scope-user", "pw", false)
	a := seedOrg(t, d, "A", "a-org")
	b := seedOrg(t, d, "B", "b-org")
	seedOrgMember(t, d, a, uid, "admin")
	seedOrgMember(t, d, b, uid, "member")
	other := seedOrg(t, d, "NotMine", "not-mine") // caller is NOT a member

	owners, err := s.ownedAtlasAccounts(ctx, &auth.User{ID: uid, Username: "scope-user"})
	if err != nil {
		t.Fatalf("owners: %v", err)
	}
	if len(owners) != 3 {
		t.Fatalf("got %d accounts, want 3 (personal + 2 orgs)", len(owners))
	}
	for _, o := range owners {
		if o.Kind == accountOrg && o.ID == other {
			t.Fatal("returned a budget for an org the caller does not belong to")
		}
	}
}

// TestCadenceFloor_HourlyImpossibleOnAnyCappedPlan pins the arithmetic the floor
// rests on: at 730 runs a month even a 2.5-minute run costs 1,825 minutes, more
// than the most generous capped plan (org_team, 1800). So hourly cannot fit ANY
// capped plan for ANY repo — it is not "usually too much", it is impossible, and
// that is what makes refusing it up front honest rather than paternalistic.
func TestCadenceFloor_HourlyImpossibleOnAnyCappedPlan(t *testing.T) {
	// The FASTEST run ever measured across the whole corpus. Using a real floor
	// rather than a hypothetical one keeps the claim honest: if hourly doesn't fit
	// even at the cheapest run anyone has actually had, it fits nothing.
	const fastestObservedRunMin = 4.7
	for _, cap := range []int64{180, 900, 1800} { // free, plus, org_team
		if cost := hourlyRunsPerMonth() * fastestObservedRunMin; cost <= float64(cap) {
			t.Fatalf("hourly at %.1f min/run costs %.0f min, which FITS a %d cap — the floor's premise is wrong",
				fastestObservedRunMin, cost, cap)
		}
	}
}

// The projection must agree with the SCHEDULER about how often a cadence fires.
// They were two separate literal tables until 2026-08-10; a cadence added to one
// and not the other would have been priced at zero and never warned about.
func TestAtlasBudget_RunsPerMonthDerivesFromTheScheduler(t *testing.T) {
	for cadence := range atlasCadenceIntervals {
		if runsPerMonth(cadence) <= 0 {
			t.Fatalf("cadence %q fires on the scheduler but costs 0 in the projection", cadence)
		}
	}
	if got := runsPerMonth("daily"); got != 30 {
		t.Fatalf("daily = %v runs/month, want 30", got)
	}
	if got := runsPerMonth("monthly"); got != 1 {
		t.Fatalf("monthly = %v runs/month, want 1", got)
	}
	if runsPerMonth("") != 0 || runsPerMonth("fortnightly") != 0 {
		t.Fatal("a cadence the scheduler never fires must cost 0")
	}
}

// The suggester must never recommend a cadence the API would refuse.
func TestAtlasBudget_SuggesterNeverRecommendsARefusedCadence(t *testing.T) {
	for _, c := range cadencesFastestFirst {
		if c == "hourly" {
			t.Fatal("suggester can propose hourly, which checkCadenceAffordable refuses on every capped plan")
		}
	}
}

func TestCadenceFloor_RefusesHourlyButAllowsSlower(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	s := &Server{DB: d}

	uid := seedUser(t, d, "cadence-floor", "pw", false)
	if _, err := d.ExecContext(ctx,
		`UPDATE users SET plan_key='personal_plus', trial_plan_key=NULL, trial_ends_at=NULL WHERE id=$1`, uid); err != nil {
		t.Fatalf("set plan: %v", err)
	}
	acct := account{Kind: accountUser, ID: uid}

	ae := s.checkCadenceAffordable(ctx, acct, "hourly")
	if ae == nil {
		t.Fatal("hourly allowed on a capped plan")
	}
	if ae.Status != 402 {
		t.Fatalf("status %d, want 402", ae.Status)
	}
	for _, c := range []string{"daily", "weekly", "monthly", ""} {
		if ae := s.checkCadenceAffordable(ctx, acct, c); ae != nil {
			t.Fatalf("refused %q, which the plan can afford: %s", c, ae.Message)
		}
	}
}

// Uncapped plans keep hourly — the floor is about affordability, not taste.
func TestCadenceFloor_UncappedPlanKeepsHourly(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	s := &Server{DB: d}

	uid := seedUser(t, d, "cadence-unlimited", "pw", false)
	if _, err := d.ExecContext(ctx,
		`UPDATE users SET plan_key='personal_unlimited', trial_plan_key=NULL, trial_ends_at=NULL WHERE id=$1`, uid); err != nil {
		t.Fatalf("set plan: %v", err)
	}
	if ae := s.checkCadenceAffordable(ctx, account{Kind: accountUser, ID: uid}, "hourly"); ae != nil {
		t.Fatalf("refused hourly on an unlimited plan: %s", ae.Message)
	}
}
