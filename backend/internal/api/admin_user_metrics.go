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
	Edits        int64 `json:"edits"`         // page revisions authored
	AgentEdits   int64 `json:"agent_edits"`   // of those, written through an agent (source='agent')
	PagesCreated int64 `json:"pages_created"` // pages whose first revision is theirs
	Views        int64 `json:"views"`         // page.view events
	Asks         int64 `json:"asks"`          // ask_log rows
	Logins       int64 `json:"logins"`        // auth.login events
	DaysActive   int64 `json:"days_active"`   // distinct days with any event
	LLMCalls     int64 `json:"llm_calls"`     // metered AI calls (calendar-month grain)
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

	// Revisions authored, and the agent-written subset. Deleted pages are
	// excluded so the table counts surviving work, matching recent_changes.go.
	if rows, err := d.QueryContext(ctx, `
		SELECT pr.author_id, COUNT(*), COUNT(*) FILTER (WHERE pr.source = 'agent')
		  FROM page_revisions pr
		  JOIN pages p ON p.id = pr.page_id AND p.deleted_at IS NULL
		 WHERE pr.author_id IS NOT NULL AND ($1 = '' OR pr.created_at >= $1)
		 GROUP BY pr.author_id`, w.Cut); err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, n, agent int64
			if err := rows.Scan(&id, &n, &agent); err != nil {
				break
			}
			m := at(id)
			m.Edits, m.AgentEdits = n, agent
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

	// Days active: distinct calendar days on which the user did ANYTHING the
	// events feed records. The best single "is this account alive" sort key —
	// one busy afternoon and a month of daily use look identical under a raw
	// event count, but not under this.
	for id, n := range scanInt64Map(ctx, d, `
		SELECT actor_user_id, COUNT(DISTINCT substr(created_at, 1, 10))
		  FROM events
		 WHERE actor_user_id IS NOT NULL AND ($1 = '' OR created_at >= $1)
		 GROUP BY actor_user_id`, w.Cut) {
		at(id).DaysActive = n
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

	return out
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
