// atlas_budget.go — projects an account's Atlas GPU spend against its plan cap,
// so a user finds out their cadence is unaffordable BEFORE it silently eats the
// month's allowance rather than after a 402.
//
// WHY THIS EXISTS. The cap is denominated in GPU minutes (plans
// .max_atlas_minutes_per_month) but a user configures SOURCES and a CADENCE —
// two steps removed from the unit they're charged in, with a per-repo run cost
// they've never been shown. Nobody can do that arithmetic in their head: the
// same "daily" costs 210 min/month on a small repo and 1,500 on a large one.
// Until 2026-08-10 every project defaulted to hourly, and all five auto-update
// accounts projected between 5,403 and 109,581 minutes against a 900 cap without
// a single one of them having chosen anything.
//
// WHAT MAKES THE PROJECTION HONEST rather than a scare:
//   - It costs each source from THAT SOURCE's own finished runs, not a global
//     guess. Measured spread is 4.7 to 80.2 minutes; one threshold would be
//     wrong for nearly everyone.
//   - Run duration is genuinely noisy (per-source stddev is comparable to the
//     mean, because it depends on how busy the box is, not just repo size), so
//     the estimate is a median and the API says `estimated` when it is thin.
//   - With fewer than minRunsForEstimate finished runs it falls back to the
//     corpus median and flags it, instead of extrapolating from one sample.
//   - The remedy is COMPUTED, not gestured at: it solves for the fastest cadence
//     that actually fits the remaining budget and returns it, so the UI can offer
//     a concrete "switch to weekly" rather than "consider reducing frequency".
package api

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/zcag/tela/backend/internal/auth"
)

// budgetMonthDays is the month length the projection assumes. Matches the
// "monthly" interval in atlasCadenceIntervals (30 days), so a monthly cadence
// projects to exactly one run.
const budgetMonthDays = 30

// runsPerMonth is DERIVED from the scheduler's own interval table rather than
// restating it. The first cut kept a second literal map here, which meant adding
// a cadence to atlasCadenceIntervals would leave this one silently returning 0 —
// the projection would price the new cadence at nothing and never warn about it.
// A quota tool that silently under-reports is worse than none, and it is the same
// two-sources-of-truth bug this whole day was spent unwinding.
//
// A cadence with no interval (or "") never fires, so it costs 0.
func runsPerMonth(cadence string) float64 {
	iv, ok := atlasCadenceIntervals[cadence]
	if !ok || iv <= 0 {
		return 0
	}
	return float64(budgetMonthDays*24*time.Hour) / float64(iv)
}

// cadencesFastestFirst is the order the suggester walks, so it recommends the
// LEAST disruptive change that works rather than jumping straight to monthly.
// "hourly" is deliberately absent: checkCadenceAffordable refuses it on every
// capped plan, so suggesting it would recommend something the API rejects — and
// only capped accounts ever reach the suggester.
var cadencesFastestFirst = []string{"daily", "weekly", "monthly"}

// minRunsForEstimate is how many finished runs a source needs before its own
// history is trusted over the corpus median. Two is deliberately low: with the
// variance seen here a third sample barely narrows the interval, and refusing to
// estimate is worse than estimating and saying so.
const minRunsForEstimate = 2

// corpusMedianMinutes backs a source with too little history. Measured across
// all runs with stats (median 27.2 min). It is a starting point, not a claim
// about any particular repo — responses built on it set Estimated.
const corpusMedianMinutes = 27.0

type atlasBudgetProject struct {
	ID            int64   `json:"id"`
	Name          string  `json:"name"`
	Cadence       string  `json:"cadence"`
	AutoUpdate    bool    `json:"auto_update"`
	Sources       int     `json:"sources"`
	MinutesPerRun float64 `json:"minutes_per_run"`
	RunsPerMonth  float64 `json:"runs_per_month"`
	Projected     float64 `json:"projected_minutes"`
	Estimated     bool    `json:"estimated"`
}

type atlasBudget struct {
	// CapMinutes nil = unlimited plan; the UI shows usage without a warning.
	CapMinutes  *int64               `json:"cap_minutes"`
	UsedMinutes int64                `json:"used_minutes"`
	Projected   float64              `json:"projected_minutes"`
	Over        bool                 `json:"over"`
	Estimated   bool                 `json:"estimated"`
	Projects    []atlasBudgetProject `json:"projects"`
	// Suggestion is the fastest cadence that would fit, with what it'd cost.
	// Empty Cadence = nothing to suggest (already fitting, or unlimited).
	Suggestion *atlasBudgetSuggestion `json:"suggestion"`
}

type atlasBudgetSuggestion struct {
	Cadence   string  `json:"cadence"`
	Projected float64 `json:"projected_minutes"`
	// AppliesTo lists the projects that would change, so the UI never implies it
	// is about to touch a project the user already set slower than the suggestion.
	AppliesTo []int64 `json:"applies_to"`
}

// atlasBudgetFor computes the account's projected monthly Atlas spend.
//
// Projection is steady-state ("if nothing changes, a month of this config costs
// X"), deliberately NOT a to-end-of-month forecast: the user is deciding on a
// cadence, and a number that shrinks as the month runs out would make the same
// setting look affordable on the 28th and not on the 2nd.
func (s *Server) atlasBudgetFor(ctx context.Context, acct account) (atlasBudget, error) {
	out := atlasBudget{Projects: []atlasBudgetProject{}}

	p, err := planFor(ctx, s.DB, acct)
	if err != nil {
		return out, err
	}
	out.CapMinutes = p.MaxAtlasMinutesPerMonth

	if out.UsedMinutes, err = sumAtlasMinutesThisMonth(ctx, s.DB, acct); err != nil {
		return out, err
	}

	// Per project: its cadence, how many sources it has, and the median finished
	// run cost across those sources. LEFT JOIN so a project whose sources have
	// never run still appears (it is exactly the case a user needs warned about).
	rows, err := s.DB.QueryContext(ctx, `
		SELECT pr.id, pr.name, pr.cadence, pr.auto_update,
		       COUNT(DISTINCT src.id) AS sources,
		       COUNT(r.id)            AS finished_runs,
		       COALESCE(percentile_cont(0.5) WITHIN GROUP (
		           ORDER BY (r.stats_json::jsonb->>'duration_sec')::float8), 0) AS median_sec
		  FROM atlas_projects pr
		  LEFT JOIN atlas_sources src ON src.project_id = pr.id
		  LEFT JOIN atlas_runs r ON r.source_id = src.id
		       AND r.status <> 'failed' AND r.stats_json <> ''
		 WHERE pr.owner_kind = $1 AND pr.owner_id = $2
		 GROUP BY pr.id, pr.name, pr.cadence, pr.auto_update
		 ORDER BY pr.id`, acct.Kind, acct.ID)
	if err != nil {
		return out, err
	}
	defer rows.Close()

	for rows.Next() {
		var b atlasBudgetProject
		var auto int
		var finishedRuns int
		var medianSec float64
		if err := rows.Scan(&b.ID, &b.Name, &b.Cadence, &auto, &b.Sources, &finishedRuns, &medianSec); err != nil {
			return out, err
		}
		b.AutoUpdate = auto == 1

		// Cost is per RUN, and a run covers one source — so a project with three
		// sources costs three runs each time its cadence fires.
		perRun := medianSec / 60
		if finishedRuns < minRunsForEstimate || perRun <= 0 {
			perRun, b.Estimated = corpusMedianMinutes, true
			out.Estimated = true
		}
		sources := float64(b.Sources)
		if sources == 0 {
			sources = 1 // a project with no source yet still costs this much once one lands
		}
		b.MinutesPerRun = round1(perRun)
		b.RunsPerMonth = runsPerMonth(b.Cadence) * sources
		if !b.AutoUpdate {
			b.RunsPerMonth = 0 // manual-only: predicts nothing, and must not warn
		}
		b.Projected = round1(b.RunsPerMonth * perRun)
		out.Projected += b.Projected
		out.Projects = append(out.Projects, b)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.Projected = round1(out.Projected)

	if out.CapMinutes != nil && out.Projected > float64(*out.CapMinutes) {
		out.Over = true
		out.Suggestion = suggestCadence(out.Projects, float64(*out.CapMinutes))
	}
	return out, nil
}

// suggestCadence finds the fastest single cadence that, applied to the
// auto-updating projects currently faster than it, brings the total inside cap.
//
// One cadence for all of them rather than a per-project optimum: the UI offers a
// single button, and a mixed recommendation the user can't reason about is worse
// than a slightly conservative one they can. Projects already slower than the
// suggestion keep their setting and are left out of AppliesTo.
func suggestCadence(projects []atlasBudgetProject, cap float64) *atlasBudgetSuggestion {
	for _, cad := range cadencesFastestFirst {
		total, applies := 0.0, []int64{}
		for _, p := range projects {
			if !p.AutoUpdate {
				continue
			}
			cur, want := runsPerMonth(p.Cadence), runsPerMonth(cad)
			// Never speed a project UP as a "fix".
			if cur <= want {
				total += p.Projected
				continue
			}
			sources := float64(p.Sources)
			if sources == 0 {
				sources = 1
			}
			total += want * sources * p.MinutesPerRun
			applies = append(applies, p.ID)
		}
		if total <= cap && len(applies) > 0 {
			return &atlasBudgetSuggestion{Cadence: cad, Projected: round1(total), AppliesTo: applies}
		}
	}
	return nil // even monthly doesn't fit — the plan, not the cadence, is the problem
}

func round1(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }

// ownedAtlasAccounts lists every billable account whose Atlas budget the caller
// can see: their personal one, plus each org they belong to. Mirrors the owner
// scope of listAtlasProjectsFor exactly — Atlas home shows projects grouped by
// owner, so a budget that covered only the personal account would leave an
// org-team customer with projects on screen and no budget for them.
func (s *Server) ownedAtlasAccounts(ctx context.Context, u *auth.User) ([]atlasBudgetOwner, error) {
	out := []atlasBudgetOwner{{Kind: accountUser, ID: u.ID, Name: u.Username}}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT o.id, o.name FROM orgs o
		   JOIN org_members om ON om.org_id = o.id AND om.user_id = $1
		  ORDER BY o.id`, u.ID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var o atlasBudgetOwner
		if err := rows.Scan(&o.ID, &o.Name); err != nil {
			return out, err
		}
		o.Kind = accountOrg
		out = append(out, o)
	}
	return out, rows.Err()
}

type atlasBudgetOwner struct {
	Kind string `json:"owner_kind"`
	ID   int64  `json:"owner_id"`
	Name string `json:"owner_name"`
}

// atlasBudgetEntry is one account's budget, tagged with whose it is so the UI can
// match a project to the budget that actually governs it.
type atlasBudgetEntry struct {
	atlasBudgetOwner
	atlasBudget
}

// GetAtlasBudget serves a projection per account the caller can see — their
// personal account and every org they belong to. An account with no Atlas
// projects still appears (with an empty projects list) so the UI can show the
// meter rather than nothing.
func (s *Server) GetAtlasBudget(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	owners, err := s.ownedAtlasAccounts(r.Context(), u)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "budget lookup failed")
		return
	}
	budgets := []atlasBudgetEntry{}
	for _, o := range owners {
		b, err := s.atlasBudgetFor(r.Context(), account{Kind: o.Kind, ID: o.ID})
		if err != nil {
			if err == sql.ErrNoRows {
				continue // account has no plan row; nothing meaningful to report
			}
			writeError(w, http.StatusInternalServerError, "internal", "budget lookup failed")
			return
		}
		budgets = append(budgets, atlasBudgetEntry{atlasBudgetOwner: o, atlasBudget: b})
	}
	writeJSON(w, http.StatusOK, map[string]any{"budgets": budgets})
}

// hourlyRunsPerMonth is what "hourly" actually commits an account to, derived
// from the same interval table as everything else.
func hourlyRunsPerMonth() float64 { return runsPerMonth("hourly") }

// checkCadenceAffordable refuses a cadence the account's plan can never pay for.
//
// This is a floor, not a projection: at 730 runs a month, even a 2.5-minute run
// costs 1,825 minutes — more than the most generous capped plan (org_team, 1800).
// So `hourly` cannot fit ANY plan with a minutes cap, for any repo, ever. It is
// not "probably too much"; it is arithmetically impossible.
//
// Offering it anyway is what produced the incident this all came from: hourly was
// the hardcoded default with no control in the create dialog, every project got
// it, and the first signal was a 402 after the allowance was gone. Refusing it at
// the seam means the pricing copy can describe the cadence list truthfully
// instead of advertising an option that always fails.
//
// Uncapped plans (personal_unlimited, org_enterprise, self-host) keep hourly.
func (s *Server) checkCadenceAffordable(ctx context.Context, acct account, cadence string) *apiErr {
	if cadence != "hourly" {
		return nil
	}
	p, err := planFor(ctx, s.DB, acct)
	if err != nil {
		return nil // can't read the plan: don't block a config change on our lookup
	}
	if p.MaxAtlasMinutesPerMonth == nil {
		return nil
	}
	return quotaErr("hourly refresh needs more indexing time than the %s plan includes (%d minutes a month — hourly is %d runs). Choose daily, weekly or monthly, or upgrade.",
		p.Name, *p.MaxAtlasMinutesPerMonth, int(hourlyRunsPerMonth()))
}
