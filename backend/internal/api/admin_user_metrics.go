package api

import (
	"context"
	"database/sql"
	"time"
)

// admin_user_metrics.go — the windowed per-user activity figures behind the
// admin People table (Settings → Users). Sibling of admin_stats.go: same
// sources (page_revisions, events, ask_log, cloud_usage), but pivoted per
// ACTOR instead of per day, so an operator can sort the whole population by
// who actually does anything.
//
// Every figure is one batched GROUP BY over the population — never per user —
// because the list already pays an N+1 for the quota snapshot (see
// ListAdminUsers) and adding a second one per metric would be six times worse.
//
// RETENTION CAVEAT: page_revisions is kept forever, so edits/pages_created are
// true all-time. `events` is pruned at TELA_EVENTS_RETENTION_DAYS (180d default,
// events_gc.go) and ask_log is unpruned, so views/logins/days_active can only
// ever mean "since the retention horizon". ListAdminUsers returns that horizon
// as `events_since` so the UI can say so instead of implying lifetime totals.

// adminUserWindow is a parsed ?window= value: the label echoed back, the
// datetime cut for row-level tables, and the 'YYYY-MM' cut for cloud_usage
// (which is bucketed per calendar month, not per day). An empty cut means
// "no lower bound" — the all-time window.
type adminUserWindow struct {
	Key       string
	Cut       string
	PeriodCut string
}

// parseAdminUserWindow maps ?window=1m|3m|all onto cut points. Anything else
// (including absent) falls back to 1m — the same grain the tab showed before
// it gained a window switch, so an old bookmark reads the same.
func parseAdminUserWindow(q string, now time.Time) adminUserWindow {
	switch q {
	case "all":
		return adminUserWindow{Key: "all"}
	case "3m":
		return adminUserWindow{
			Key: "3m",
			Cut: now.AddDate(0, 0, -90).Format("2006-01-02 15:04:05"),
			// Current month plus the two before it — three calendar months, the
			// finest grain cloud_usage can answer.
			PeriodCut: now.AddDate(0, -2, 0).Format("2006-01"),
		}
	default:
		return adminUserWindow{
			Key:       "1m",
			Cut:       now.AddDate(0, 0, -30).Format("2006-01-02 15:04:05"),
			PeriodCut: now.Format("2006-01"),
		}
	}
}

// adminUserMetrics is one user's activity inside the requested window. Zero is
// a real answer (an account that did nothing), so every field is a plain count
// and the struct is always populated — never nil — on list rows.
type adminUserMetrics struct {
	// Edits is the total; the three below partition it by WHO actually wrote the
	// revision. Folding them together is what made a vault-sync account read as
	// the instance's most prolific author.
	Edits      int64 `json:"edits"`       // page revisions authored (all sources)
	HumanEdits int64 `json:"human_edits"` // typed in the app ('create' / 'manual')
	AgentEdits int64 `json:"agent_edits"` // written through an agent (source='agent')
	SyncEdits  int64 `json:"sync_edits"`  // snapshots taken by file sync ('sync-*')

	PagesCreated int64 `json:"pages_created"` // pages whose first revision is theirs
	Views        int64 `json:"views"`         // page.view events
	Asks         int64 `json:"asks"`          // ask_log rows
	Logins       int64 `json:"logins"`        // auth.login events
	// DaysActive counts distinct days with an event OR an authored revision.
	// Events alone misses the sync path entirely (it records none), which made
	// heavy sync users look dormant while their edit count said otherwise.
	DaysActive int64 `json:"days_active"`
	LLMCalls   int64 `json:"llm_calls"` // metered AI calls (calendar-month grain)

	// Days30 is days-active over the last 30 days REGARDLESS of the selected
	// window — the segment has to describe the person, not the view, or every
	// account would change lifecycle when you switch to "all time".
	Days30 int64 `json:"days30"`
	// Weeks is active-days per ISO week (0-7) over adminUserWeeks, oldest→newest
	// and dense. The sparkline, the trend delta and the retention grid are all
	// read off this one series.
	Weeks []int64 `json:"weeks"`
}

// loadAdminUserMetrics returns id→metrics for every user with any activity in
// the window. Users with none are simply absent; the caller fills a zero value.
// Best-effort per query, matching the rest of the admin list: a failing
// aggregate leaves its column at zero rather than failing the whole screen.
func loadAdminUserMetrics(ctx context.Context, d *sql.DB, w adminUserWindow) map[int64]*adminUserMetrics {
	out := map[int64]*adminUserMetrics{}
	at := func(id int64) *adminUserMetrics {
		m, ok := out[id]
		if !ok {
			m = &adminUserMetrics{}
			out[id] = m
		}
		return m
	}

	// Revisions authored, split by who wrote them. Deleted pages are excluded so
	// the table counts surviving work, matching recent_changes.go.
	if rows, err := d.QueryContext(ctx, `
		SELECT pr.author_id, COUNT(*),
		       COUNT(*) FILTER (WHERE pr.source = 'agent'),
		       COUNT(*) FILTER (WHERE pr.source LIKE 'sync-%')
		  FROM page_revisions pr
		  JOIN pages p ON p.id = pr.page_id AND p.deleted_at IS NULL
		 WHERE pr.author_id IS NOT NULL AND ($1 = '' OR pr.created_at >= $1)
		 GROUP BY pr.author_id`, w.Cut); err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, n, agent, sync int64
			if err := rows.Scan(&id, &n, &agent, &sync); err != nil {
				break
			}
			m := at(id)
			m.Edits, m.AgentEdits, m.SyncEdits = n, agent, sync
			m.HumanEdits = n - agent - sync
		}
	}

	// Pages created = pages whose EARLIEST revision is this user's. Derived
	// rather than read off a column because `pages` has no created_by, and the
	// create snapshot's source varies ('create' by hand, 'agent' via MCP) so
	// source alone can't tell a creation from an edit.
	for id, n := range scanInt64Map(ctx, d, `
		SELECT f.author_id, COUNT(*)
		  FROM (
		    SELECT DISTINCT ON (pr.page_id) pr.author_id, pr.created_at
		      FROM page_revisions pr
		      JOIN pages p ON p.id = pr.page_id AND p.deleted_at IS NULL
		     ORDER BY pr.page_id, pr.created_at ASC, pr.id ASC
		  ) f
		 WHERE f.author_id IS NOT NULL AND ($1 = '' OR f.created_at >= $1)
		 GROUP BY f.author_id`, w.Cut) {
		at(id).PagesCreated = n
	}

	// Views + sign-ins in one pass over the (large) events table.
	if rows, err := d.QueryContext(ctx, `
		SELECT actor_user_id, type, COUNT(*)
		  FROM events
		 WHERE actor_user_id IS NOT NULL
		   AND type IN ('page.view','auth.login')
		   AND ($1 = '' OR created_at >= $1)
		 GROUP BY actor_user_id, type`, w.Cut); err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, n int64
			var typ string
			if err := rows.Scan(&id, &typ, &n); err != nil {
				break
			}
			switch typ {
			case evtPageView:
				at(id).Views = n
			case evtAuthLogin:
				at(id).Logins = n
			}
		}
	}

	// Days active: distinct calendar days on which the user did ANYTHING —
	// the best single "is this account alive" sort key, since one busy afternoon
	// and a month of daily use look identical under a raw event count.
	for id, n := range scanInt64Map(ctx, d,
		`SELECT uid, COUNT(DISTINCT day) FROM (`+activeDaysUnion+`) x GROUP BY uid`, w.Cut) {
		at(id).DaysActive = n
	}

	// The same measure over a FIXED last-30-days, for the lifecycle segment.
	cut30 := time.Now().UTC().AddDate(0, 0, -30).Format("2006-01-02 15:04:05")
	for id, n := range scanInt64Map(ctx, d,
		`SELECT uid, COUNT(DISTINCT day) FROM (`+activeDaysUnion+`) x GROUP BY uid`, cut30) {
		at(id).Days30 = n
	}

	for id, n := range scanInt64Map(ctx, d, `
		SELECT user_id, COUNT(*) FROM ask_log
		 WHERE user_id IS NOT NULL AND ($1 = '' OR created_at >= $1)
		 GROUP BY user_id`, w.Cut) {
		at(id).Asks = n
	}

	// cloud_usage is keyed by calendar month, so this follows the month grain
	// (PeriodCut), not the exact day cut the other figures use.
	for id, n := range scanInt64Map(ctx, d, `
		SELECT account_id, COALESCE(SUM(llm_calls), 0) FROM cloud_usage
		 WHERE account_kind = 'user' AND ($1 = '' OR period >= $1)
		 GROUP BY account_id`, w.PeriodCut) {
		at(id).LLMCalls = n
	}

	// Weekly shape: active days per week over the trailing adminUserWeeks. Dense
	// and aligned to `axis` so every row's sparkline shares an x-axis.
	axis, idx := weekAxis(time.Now().UTC())
	for _, m := range out {
		m.Weeks = make([]int64, len(axis))
	}
	if rows, err := d.QueryContext(ctx, `
		SELECT uid, to_char(date_trunc('week', day::date), 'YYYY-MM-DD') AS wk, COUNT(DISTINCT day)
		  FROM (`+activeDaysUnion+`) x
		 GROUP BY uid, wk`, axis[0]+" 00:00:00"); err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, n int64
			var wk string
			if err := rows.Scan(&id, &wk, &n); err != nil {
				break
			}
			m := at(id)
			if m.Weeks == nil {
				m.Weeks = make([]int64, len(axis))
			}
			if i, ok := idx[wk]; ok {
				m.Weeks[i] = n
			}
		}
	}
	// Rows discovered by the weekly pass alone still need a full-length series.
	for _, m := range out {
		if len(m.Weeks) != len(axis) {
			grown := make([]int64, len(axis))
			copy(grown, m.Weeks)
			m.Weeks = grown
		}
	}

	return out
}

// adminUserWeeks is how many trailing ISO weeks the per-user activity series
// covers. 26 is the practical ceiling: `events` is pruned at 180 days by
// default, so a longer axis would render a flat tail that looks like inactivity
// rather than missing data. It also sets how far back the signup-cohort grid
// can see.
const adminUserWeeks = 26

// activeDaysUnion is one (user, day) row per day the user did anything —
// an event OR an authored revision. $1 is the lower bound ('' = unbounded).
// Kept as one string because three different aggregates read the same
// definition of "active", and they must not drift apart.
const activeDaysUnion = `
	SELECT actor_user_id AS uid, substr(created_at, 1, 10) AS day
	  FROM events
	 WHERE actor_user_id IS NOT NULL AND ($1 = '' OR created_at >= $1)
	UNION ALL
	SELECT pr.author_id, substr(pr.created_at, 1, 10)
	  FROM page_revisions pr
	  JOIN pages p ON p.id = pr.page_id AND p.deleted_at IS NULL
	 WHERE pr.author_id IS NOT NULL AND ($1 = '' OR pr.created_at >= $1)`

// weekAxis returns the dense list of trailing week-start dates (Monday,
// oldest→newest) plus a label→index map. Postgres date_trunc('week') is
// Monday-based, so the Go side matches it by rewinding to Monday.
func weekAxis(now time.Time) ([]string, map[string]int) {
	monday := now
	// time.Weekday is Sunday=0; shift so Monday=0.
	back := (int(monday.Weekday()) + 6) % 7
	monday = monday.AddDate(0, 0, -back)
	axis := make([]string, adminUserWeeks)
	idx := make(map[string]int, adminUserWeeks)
	for i := 0; i < adminUserWeeks; i++ {
		axis[i] = monday.AddDate(0, 0, -7*(adminUserWeeks-1-i)).Format("2006-01-02")
		idx[axis[i]] = i
	}
	return axis, idx
}

// eventsHorizon is the oldest surviving events row — the point before which the
// retention GC has eaten the history, so the UI can label an "all time" column
// honestly. Empty when the feed is empty.
func eventsHorizon(ctx context.Context, d *sql.DB) string {
	var oldest sql.NullString
	if err := d.QueryRowContext(ctx, `SELECT MIN(created_at) FROM events`).Scan(&oldest); err != nil {
		return ""
	}
	return oldest.String
}
