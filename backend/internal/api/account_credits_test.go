package api

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/zcag/tela/backend/internal/testdb"
)

func thisPeriod() string { return time.Now().UTC().Format("2006-01") }

func grant(t *testing.T, d *sql.DB, acct account, metric string, amount int64, period string) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(),
		`INSERT INTO account_credits (account_kind, account_id, period, metric, amount, reason)
		 VALUES ($1,$2,$3,$4,$5,'test')`,
		acct.Kind, acct.ID, period, metric, amount); err != nil {
		t.Fatalf("grant: %v", err)
	}
}

func planUser(t *testing.T, d *sql.DB, name, planKey string) account {
	t.Helper()
	uid := seedUser(t, d, name, "pw", false)
	if _, err := d.ExecContext(context.Background(),
		`UPDATE users SET plan_key=$2, trial_plan_key=NULL, trial_ends_at=NULL WHERE id=$1`, uid, planKey); err != nil {
		t.Fatalf("set plan: %v", err)
	}
	return account{Kind: accountUser, ID: uid}
}

func TestCredits_RaiseTheCapForThisPeriodOnly(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	acct := planUser(t, d, "credit-basic", "personal_plus")

	base, err := planFor(ctx, d, acct)
	if err != nil {
		t.Fatalf("planFor: %v", err)
	}
	if *base.MaxAtlasMinutesPerMonth != 900 {
		t.Fatalf("baseline cap %d, want 900", *base.MaxAtlasMinutesPerMonth)
	}

	grant(t, d, acct, "max_atlas_minutes_per_month", 600, thisPeriod())
	p, _ := planFor(ctx, d, acct)
	if *p.MaxAtlasMinutesPerMonth != 1500 {
		t.Fatalf("cap with credit = %d, want 1500 (900 + 600)", *p.MaxAtlasMinutesPerMonth)
	}

	// A credit for a DIFFERENT month must not apply — the whole point of scoping
	// is that an exception expires by itself.
	other := planUser(t, d, "credit-other-month", "personal_plus")
	grant(t, d, other, "max_atlas_minutes_per_month", 600, "1999-01")
	po, _ := planFor(ctx, d, other)
	if *po.MaxAtlasMinutesPerMonth != 900 {
		t.Fatalf("a credit for 1999-01 applied in %s: cap = %d", thisPeriod(), *po.MaxAtlasMinutesPerMonth)
	}
}

// An empty period means "every period" — a standing allowance rather than a
// dated exception.
func TestCredits_EmptyPeriodAlwaysApplies(t *testing.T) {
	d := testdb.New(t)
	acct := planUser(t, d, "credit-standing", "personal_plus")
	grant(t, d, acct, "max_atlas_minutes_per_month", 100, "")
	p, _ := planFor(context.Background(), d, acct)
	if *p.MaxAtlasMinutesPerMonth != 1000 {
		t.Fatalf("cap = %d, want 1000", *p.MaxAtlasMinutesPerMonth)
	}
}

// Topping up "unlimited" must stay unlimited. Turning nil into a number would
// silently DOWNGRADE an unlimited tier, which is the opposite of a top-up.
func TestCredits_UnlimitedStaysUnlimited(t *testing.T) {
	d := testdb.New(t)
	acct := planUser(t, d, "credit-unlimited", "personal_unlimited")
	grant(t, d, acct, "max_atlas_minutes_per_month", 500, thisPeriod())
	p, _ := planFor(context.Background(), d, acct)
	if p.MaxAtlasMinutesPerMonth != nil {
		t.Fatalf("unlimited cap became %d", *p.MaxAtlasMinutesPerMonth)
	}
}

// A metric that matches no cap grants nothing. `metric` reuses the plans column
// name precisely so a typo is inert rather than crediting the wrong meter.
func TestCredits_UnknownMetricIsInert(t *testing.T) {
	d := testdb.New(t)
	acct := planUser(t, d, "credit-typo", "personal_plus")
	grant(t, d, acct, "max_atlas_minutes", 9999, thisPeriod()) // note: wrong name
	p, _ := planFor(context.Background(), d, acct)
	if *p.MaxAtlasMinutesPerMonth != 900 {
		t.Fatalf("a typo'd metric granted %d", *p.MaxAtlasMinutesPerMonth)
	}
}

// Credits are per ACCOUNT, not per tier: one account's grant must not leak to
// another account on the same plan. This is the property that makes the whole
// idea safe, so it is pinned explicitly.
func TestCredits_DoNotLeakToOtherAccountsOnTheSamePlan(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	granted := planUser(t, d, "credit-mine", "personal_plus")
	neighbour := planUser(t, d, "credit-theirs", "personal_plus")

	grant(t, d, granted, "max_atlas_minutes_per_month", 600, thisPeriod())

	pg, _ := planFor(ctx, d, granted)
	pn, _ := planFor(ctx, d, neighbour)
	if *pg.MaxAtlasMinutesPerMonth != 1500 {
		t.Fatalf("granted account cap = %d, want 1500", *pg.MaxAtlasMinutesPerMonth)
	}
	if *pn.MaxAtlasMinutesPerMonth != 900 {
		t.Fatalf("a neighbour on the same tier saw %d — credits leaked across accounts",
			*pn.MaxAtlasMinutesPerMonth)
	}
}

// An org credit must not apply to a user with the same numeric id, and vice
// versa — account_kind is part of the identity, not decoration.
func TestCredits_ScopedByAccountKind(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	uid := seedUser(t, d, "credit-kind", "pw", false)
	orgID := seedOrg(t, d, "CreditOrg", "credit-org")
	if _, err := d.ExecContext(ctx, `UPDATE users SET plan_key='personal_plus', trial_plan_key=NULL, trial_ends_at=NULL WHERE id=$1`, uid); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := d.ExecContext(ctx, `UPDATE orgs SET plan_key='org_team' WHERE id=$1`, orgID); err != nil {
		t.Fatalf("org plan: %v", err)
	}
	// Grant to the ORG with the USER's id, and vice versa — neither must land.
	grant(t, d, account{Kind: accountOrg, ID: uid}, "max_atlas_minutes_per_month", 5000, thisPeriod())

	pu, _ := planFor(ctx, d, account{Kind: accountUser, ID: uid})
	if *pu.MaxAtlasMinutesPerMonth != 900 {
		t.Fatalf("an ORG credit applied to the USER account: %d", *pu.MaxAtlasMinutesPerMonth)
	}
}

// THE OVERSIGHT TEST. Every consumer of a cap must see the credit, because they
// all resolve through planFor. If someone later reads the plans table directly in
// a gate, this catches it.
func TestCredits_EveryConsumerSeesTheTopUp(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	s := &Server{DB: d}

	acct := planUser(t, d, "credit-consumers", "personal_plus")
	src := seedQuotaSource(t, d, acct.ID)
	// Spend 1,000 minutes — over the 900 tier cap.
	for i := 0; i < 10; i++ {
		stampRunDuration(t, d, seedAtlasRun(t, d, src, "done"), 6000) // 100 min each
	}

	// 1. The run gate refuses at the tier cap.
	if ae := s.checkAtlasRunQuota(ctx, acct); ae == nil {
		t.Fatal("gate allowed a run at 1000 of 900 minutes")
	}

	// 2. A top-up lifts it.
	grant(t, d, acct, "max_atlas_minutes_per_month", 600, thisPeriod())
	if ae := s.checkAtlasRunQuota(ctx, acct); ae != nil {
		t.Fatalf("gate still refuses at 1000 of 1500 minutes: %s", ae.Message)
	}

	// 3. The budget projection reports the raised cap, not the tier's.
	b, err := s.atlasBudgetFor(ctx, acct)
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if b.CapMinutes == nil || *b.CapMinutes != 1500 {
		t.Fatalf("budget cap = %v, want 1500 — the projection is reading the tier, not the account", b.CapMinutes)
	}

	// 4. The usage snapshot (Settings → Plan & Usage, and the admin user list)
	//    reports it too.
	u, err := s.buildUsage(ctx, acct)
	if err != nil {
		t.Fatalf("buildUsage: %v", err)
	}
	if u.Plan.MaxAtlasMinutesPerMonth == nil || *u.Plan.MaxAtlasMinutesPerMonth != 1500 {
		t.Fatalf("usage snapshot cap = %v, want 1500", u.Plan.MaxAtlasMinutesPerMonth)
	}

	// 5. The public tier CATALOG must NOT be affected — it describes what a tier
	//    includes, not what one account was granted.
	var catalogCap sql.NullInt64
	if err := d.QueryRowContext(ctx,
		`SELECT max_atlas_minutes_per_month FROM plans WHERE key='personal_plus'`).Scan(&catalogCap); err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if catalogCap.Int64 != 900 {
		t.Fatalf("a per-account credit changed the published tier to %d", catalogCap.Int64)
	}
}

// The cadence floor reads the same resolved plan, so a top-up must not
// accidentally unlock hourly — that limit is about affordability per run, and a
// capped account stays capped however large its allowance.
func TestCredits_DoNotUnlockHourlyCadence(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	s := &Server{DB: d}
	acct := planUser(t, d, "credit-cadence", "personal_plus")
	grant(t, d, acct, "max_atlas_minutes_per_month", 100000, thisPeriod())

	if ae := s.checkCadenceAffordable(ctx, acct, "hourly"); ae == nil {
		t.Fatal("a large top-up unlocked hourly; the account still has a finite cap")
	}
}
