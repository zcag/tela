package api

import (
	"context"
	"database/sql"
	"net/http"
	"time"
)

// admin_stats.go — GET /api/admin/stats. The instance-analytics dashboard
// (Settings → Insights). Instance-admin only. Everything here is aggregated from
// existing tables — mostly `events` (the activity log) plus page_revisions,
// page_links, page_agreement, ask_log — so there's no new instrumentation.
//
// Read-only and a touch heavy (≈12 aggregations over a 30-day window); it's an
// admin screen, not a hot path. At larger volumes the events scans want a
// nightly rollup table — noted as the scale follow-up.

const (
	statsWindowDays = 30
	staleAfterDays  = 90 // a page not touched in this long is "stale"
)

type statsTopPage struct {
	PageID    int64  `json:"page_id"`
	SpaceID   int64  `json:"space_id"`
	Title     string `json:"title"`
	SpaceName string `json:"space_name"`
	Count     int64  `json:"count"`
}

type statsTopPerson struct {
	Label string `json:"label"`
	Count int64  `json:"count"`
}

type statsTopSpace struct {
	SpaceID int64  `json:"space_id"`
	Name    string `json:"name"`
	Count   int64  `json:"count"`
}

type statsKindCount struct {
	Kind  string `json:"kind"`
	Count int64  `json:"count"`
}

// statsSignup is one recent account for the "who's new" panel. Activated = the
// user has authored ≥1 page revision (the real-vs-tyre-kicker signal); Email is
// admin-eyes-only, same as the admin users list.
type statsSignup struct {
	UserID      int64  `json:"user_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	CreatedAt   string `json:"created_at"`
	Activated   bool   `json:"activated"`
}

// statsUnanswered is one recent Ask question that retrieved nothing — a content
// gap to write toward (the generalized "agent asked, docs didn't answer" signal).
type statsUnanswered struct {
	Question  string `json:"question"`
	Who       string `json:"who"`
	CreatedAt string `json:"created_at"`
}

type adminStats struct {
	// Dense daily buckets (oldest→newest), len == statsWindowDays. `days` are the
	// 'YYYY-MM-DD' labels the others align to.
	Days   []string `json:"days"`
	Views  []int64  `json:"views"`
	Edits  []int64  `json:"edits"`
	Logins []int64  `json:"logins"`
	Asks   []int64  `json:"asks"`
	Errors []int64  `json:"errors"`
	// Cumulative growth over the same days.
	UsersCum []int64 `json:"users_cum"`
	PagesCum []int64 `json:"pages_cum"`

	// Active distinct users over rolling windows.
	DAU int64 `json:"dau"`
	WAU int64 `json:"wau"`
	MAU int64 `json:"mau"`

	// Current totals.
	Users  int64 `json:"users"`
	Spaces int64 `json:"spaces"`
	Pages  int64 `json:"pages"`

	// Leaderboards (30d).
	TopPages        []statsTopPage   `json:"top_pages"`
	TopContributors []statsTopPerson `json:"top_contributors"`
	TopSpaces       []statsTopSpace  `json:"top_spaces"`

	// AI (30d).
	Asks30         int64 `json:"asks_30"`
	AsksAnswered30 int64 `json:"asks_answered_30"`

	// Errors by kind (7d).
	ErrorsByKind []statsKindCount `json:"errors_by_kind"`

	// Knowledge health (current).
	StalePages     int64 `json:"stale_pages"`
	OrphanPages    int64 `json:"orphan_pages"`
	Contradictions int64 `json:"contradictions"`

	// Operator signals: growth/activation + the newest accounts and the questions
	// the docs couldn't answer. Turns "ask someone to run SQL" into a glance.
	NewUsers30     int64             `json:"new_users_30"`     // accounts created in the window
	Activated      int64             `json:"activated"`        // users who have authored ≥1 page revision (all-time)
	ActivatedNew30 int64             `json:"activated_new_30"` // of NewUsers30, how many have made ≥1 page revision (signup→first-edit rate)
	RetainedNew30  int64             `json:"retained_new_30"`  // users who signed up 8-30d ago and were active in the last 7d (early retention proxy)
	RecentSignups  []statsSignup     `json:"recent_signups"`   // newest accounts + whether they activated
	UnansweredAsks []statsUnanswered `json:"unanswered_asks"`  // recent Ask questions that returned nothing

	// Reach / adoption — including the agent surface (tela's first-class audience).
	WAUPrev      int64 `json:"wau_prev"`      // active users in the 7d BEFORE the last 7 (for a WoW delta)
	MCPUsers     int64 `json:"mcp_users"`     // users who have ever connected an agent over MCP
	ActivePATs   int64 `json:"active_pats"`   // live personal access tokens (not revoked/expired)
	PublicSpaces int64 `json:"public_spaces"` // spaces published to the open web

	// Billing signals — revenue visibility at a glance.
	ActiveTrials      int64 `json:"active_trials"`      // users currently on an active (non-expired) trial
	ExpiredTrials     int64 `json:"expired_trials"`     // trials that ended without converting (trial_plan_key set, trial_ends_at past)
	PaidSubscriptions int64 `json:"paid_subscriptions"` // users on any paid plan (not free or trial)

	// AI service health — the last background-probe result from StartAIHealthProbe.
	AIHealthy bool   `json:"ai_healthy"`
	AIReason  string `json:"ai_reason,omitempty"` // non-empty when unhealthy: "embedder: ..." / "chat: ..."
}

func (s *Server) AdminStats(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireInstanceAdmin(w, r); !ok {
		return
	}
	ctx := r.Context()
	now := time.Now().UTC()

	// Activity aggregates hide instance-admin actors by default (the operator's own
	// views/edits/asks/errors dominate an otherwise-quiet instance); ?include_admins
	// brings them back. Only the activity/actor-derived figures are filtered — the
	// population counts (users, pages, signups, billing) always count everyone.
	includeAdmins := wantIncludeAdmins(r)
	evtAdmin := adminActorFilter("actor_user_id", includeAdmins)    // bare `events`
	evtAdminE := adminActorFilter("e.actor_user_id", includeAdmins) // `events e`
	askAdmin := adminActorFilter("user_id", includeAdmins)          // bare `ask_log`
	askAdminA := adminActorFilter("a.user_id", includeAdmins)       // `ask_log a`
	// and prefixes a non-empty filter with " AND " for appending to a WHERE.
	and := func(f string) string {
		if f == "" {
			return ""
		}
		return " AND " + f
	}

	// Dense day axis (oldest→newest) + a day→index map for scattering grouped rows.
	days := make([]string, statsWindowDays)
	idx := map[string]int{}
	for i := 0; i < statsWindowDays; i++ {
		d := now.AddDate(0, 0, -(statsWindowDays - 1 - i)).Format("2006-01-02")
		days[i] = d
		idx[d] = i
	}
	cut30 := days[0] + " 00:00:00"
	cut14 := now.AddDate(0, 0, -14).Format("2006-01-02 15:04:05")
	cut7 := now.AddDate(0, 0, -7).Format("2006-01-02 15:04:05")
	cut1 := now.AddDate(0, 0, -1).Format("2006-01-02 15:04:05")

	out := adminStats{
		Days:     days,
		Views:    make([]int64, statsWindowDays),
		Edits:    make([]int64, statsWindowDays),
		Logins:   make([]int64, statsWindowDays),
		Asks:     make([]int64, statsWindowDays),
		Errors:   make([]int64, statsWindowDays),
		UsersCum: make([]int64, statsWindowDays),
		PagesCum: make([]int64, statsWindowDays),
		// Non-nil slices so the JSON is [] not null when empty.
		TopPages:        []statsTopPage{},
		TopContributors: []statsTopPerson{},
		TopSpaces:       []statsTopSpace{},
		ErrorsByKind:    []statsKindCount{},
		RecentSignups:   []statsSignup{},
		UnansweredAsks:  []statsUnanswered{},
	}

	// --- Daily activity by type ---
	if rows, err := s.DB.QueryContext(ctx, `
		SELECT substr(created_at,1,10) AS day, type, COUNT(*)
		  FROM events
		 WHERE created_at >= $1
		   AND type IN ('page.view','page.edit','auth.login','ask','client.error')`+and(evtAdmin)+`
		 GROUP BY day, type`, cut30); err == nil {
		defer rows.Close()
		for rows.Next() {
			var day, typ string
			var n int64
			if err := rows.Scan(&day, &typ, &n); err != nil {
				break
			}
			i, ok := idx[day]
			if !ok {
				continue
			}
			switch typ {
			case evtPageView:
				out.Views[i] = n
			case evtPageEdit:
				out.Edits[i] = n
			case evtAuthLogin:
				out.Logins[i] = n
			case evtAsk:
				out.Asks[i] = n
			case evtClientError:
				out.Errors[i] = n
			}
		}
		rows.Close()
	}

	// --- Active users (rolling DAU/WAU/MAU + the prior week for a WoW delta) ---
	_ = s.DB.QueryRowContext(ctx, `
		SELECT
		  COUNT(DISTINCT actor_user_id) FILTER (WHERE created_at >= $1),
		  COUNT(DISTINCT actor_user_id) FILTER (WHERE created_at >= $2),
		  COUNT(DISTINCT actor_user_id) FILTER (WHERE created_at >= $3),
		  COUNT(DISTINCT actor_user_id) FILTER (WHERE created_at >= $4 AND created_at < $2)
		  FROM events
		 WHERE actor_user_id IS NOT NULL AND created_at >= $3`+and(evtAdmin),
		cut1, cut7, cut30, cut14).Scan(&out.DAU, &out.WAU, &out.MAU, &out.WAUPrev)

	// --- Current totals ---
	_ = s.DB.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM users),
		       (SELECT COUNT(*) FROM spaces),
		       (SELECT COUNT(*) FROM pages WHERE deleted_at IS NULL)`).
		Scan(&out.Users, &out.Spaces, &out.Pages)

	// --- Cumulative growth (baseline before window + running daily new) ---
	fillCumulative(ctx, s.DB, out.UsersCum, days, idx,
		`SELECT COUNT(*) FROM users WHERE created_at < $1`,
		`SELECT substr(created_at,1,10), COUNT(*) FROM users WHERE created_at >= $1 GROUP BY 1`, cut30)
	fillCumulative(ctx, s.DB, out.PagesCum, days, idx,
		`SELECT COUNT(*) FROM pages WHERE deleted_at IS NULL AND created_at < $1`,
		`SELECT substr(created_at,1,10), COUNT(*) FROM pages WHERE deleted_at IS NULL AND created_at >= $1 GROUP BY 1`, cut30)

	// --- Top pages by views (30d) ---
	if rows, err := s.DB.QueryContext(ctx, `
		SELECT e.target_id, p.space_id, p.title, sp.name, COUNT(*) c
		  FROM events e
		  JOIN pages p  ON p.id = e.target_id AND p.deleted_at IS NULL
		  JOIN spaces sp ON sp.id = p.space_id
		 WHERE e.type = 'page.view' AND e.target_kind = 'page' AND e.created_at >= $1`+and(evtAdminE)+`
		 GROUP BY e.target_id, p.space_id, p.title, sp.name
		 ORDER BY c DESC LIMIT 10`, cut30); err == nil {
		defer rows.Close()
		for rows.Next() {
			var tp statsTopPage
			if err := rows.Scan(&tp.PageID, &tp.SpaceID, &tp.Title, &tp.SpaceName, &tp.Count); err != nil {
				break
			}
			out.TopPages = append(out.TopPages, tp)
		}
		rows.Close()
	}

	// --- Top contributors by edits (30d) ---
	if rows, err := s.DB.QueryContext(ctx, `
		SELECT actor_label, COUNT(*) c
		  FROM events
		 WHERE type = 'page.edit' AND created_at >= $1 AND actor_label <> ''`+and(evtAdmin)+`
		 GROUP BY actor_label ORDER BY c DESC LIMIT 10`, cut30); err == nil {
		defer rows.Close()
		for rows.Next() {
			var p statsTopPerson
			if err := rows.Scan(&p.Label, &p.Count); err != nil {
				break
			}
			out.TopContributors = append(out.TopContributors, p)
		}
		rows.Close()
	}

	// --- Most-active spaces (views+edits, 30d) ---
	if rows, err := s.DB.QueryContext(ctx, `
		SELECT sp.id, sp.name, COUNT(*) c
		  FROM events e
		  JOIN pages p  ON p.id = e.target_id AND p.deleted_at IS NULL
		  JOIN spaces sp ON sp.id = p.space_id
		 WHERE e.target_kind = 'page' AND e.type IN ('page.view','page.edit') AND e.created_at >= $1`+and(evtAdminE)+`
		 GROUP BY sp.id, sp.name ORDER BY c DESC LIMIT 10`, cut30); err == nil {
		defer rows.Close()
		for rows.Next() {
			var ts statsTopSpace
			if err := rows.Scan(&ts.SpaceID, &ts.Name, &ts.Count); err != nil {
				break
			}
			out.TopSpaces = append(out.TopSpaces, ts)
		}
		rows.Close()
	}

	// --- Asks + answer rate (30d) ---
	_ = s.DB.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(answered),0) FROM ask_log WHERE created_at >= $1`+and(askAdmin), cut30).
		Scan(&out.Asks30, &out.AsksAnswered30)

	// --- Client errors by kind (7d), parsed from the "[kind] …" detail header ---
	if rows, err := s.DB.QueryContext(ctx, `
		SELECT COALESCE((regexp_match(detail, '^\[([^\]]+)\]'))[1], 'error') AS kind, COUNT(*) c
		  FROM events
		 WHERE type = 'client.error' AND created_at >= $1`+and(evtAdmin)+`
		 GROUP BY kind ORDER BY c DESC`, cut7); err == nil {
		defer rows.Close()
		for rows.Next() {
			var kc statsKindCount
			if err := rows.Scan(&kc.Kind, &kc.Count); err != nil {
				break
			}
			out.ErrorsByKind = append(out.ErrorsByKind, kc)
		}
		rows.Close()
	}

	// --- Knowledge health ---
	staleCut := now.AddDate(0, 0, -staleAfterDays).Format("2006-01-02 15:04:05")
	_ = s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pages WHERE deleted_at IS NULL AND updated_at < $1`, staleCut).Scan(&out.StalePages)
	_ = s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pages p WHERE p.deleted_at IS NULL
		   AND NOT EXISTS (SELECT 1 FROM page_links l WHERE l.target_id = p.id)`).Scan(&out.OrphanPages)
	_ = s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM page_agreement WHERE dispute > 0`).Scan(&out.Contradictions)

	// --- Reach / adoption (agent surface included) ---
	_ = s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE mcp_last_seen_at IS NOT NULL`).Scan(&out.MCPUsers)
	_ = s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM api_keys WHERE revoked_at IS NULL AND (expires_at IS NULL OR expires_at > tela_now())`).Scan(&out.ActivePATs)
	_ = s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM spaces WHERE visibility = 'public'`).Scan(&out.PublicSpaces)

	// --- Billing ---
	_ = s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE trial_plan_key IS NOT NULL AND trial_ends_at > tela_now() AND deleted_at IS NULL`).Scan(&out.ActiveTrials)
	_ = s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE trial_plan_key IS NOT NULL AND trial_ends_at <= tela_now() AND deleted_at IS NULL`).Scan(&out.ExpiredTrials)
	_ = s.DB.QueryRowContext(ctx,
		// subscription_status='trialing' is excluded: those accounts hold a real
		// subscription whose FIRST CHARGE HAS NOT HAPPENED (bought during a tela
		// trial, so Polar deferred it) and can still cancel for free. Counting
		// them as paid is the same class of flattering lie this whole billing
		// trail exists to remove.
		`SELECT COUNT(*) FROM users
		  WHERE plan_key NOT IN ('personal_free','plus_trial')
		    AND subscription_status <> 'trialing'
		    AND deleted_at IS NULL`).Scan(&out.PaidSubscriptions)

	// --- AI health (last probe verdict from StartAIHealthProbe) ---
	out.AIHealthy = s.aiHealthy()
	out.AIReason = s.aiHealthReason()

	// --- Growth / activation ---
	_ = s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE created_at >= $1`, cut30).Scan(&out.NewUsers30)
	// Activated = has authored ≥1 page revision. page_revisions.author_id is the
	// real-work signal (a tyre-kicker who only logged in has none).
	_ = s.DB.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT author_id) FROM page_revisions WHERE author_id IS NOT NULL`).Scan(&out.Activated)
	// Funnel: of the last-30d cohort, how many made ≥1 edit (signup→first-edit rate).
	_ = s.DB.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT r.author_id)
		   FROM page_revisions r
		   JOIN users u ON u.id = r.author_id
		  WHERE u.created_at >= $1`, cut30).Scan(&out.ActivatedNew30)
	// Early retention: users who signed up >7d ago (within 30d) and had any event in the last 7d.
	_ = s.DB.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT u.id)
		   FROM users u
		  WHERE u.created_at >= $1 AND u.created_at < $2
		    AND EXISTS (
		          SELECT 1 FROM events e
		           WHERE e.actor_user_id = u.id AND e.created_at >= $2
		        )`, cut30, cut7).Scan(&out.RetainedNew30)

	// --- Recent signups (newest accounts + activated flag) ---
	if rows, err := s.DB.QueryContext(ctx, `
		SELECT u.id, u.username, u.display_name, COALESCE(u.email, ''), u.created_at,
		       EXISTS (SELECT 1 FROM page_revisions r WHERE r.author_id = u.id) AS activated
		  FROM users u
		 ORDER BY u.created_at DESC LIMIT 12`); err == nil {
		defer rows.Close()
		for rows.Next() {
			var g statsSignup
			if err := rows.Scan(&g.UserID, &g.Username, &g.DisplayName, &g.Email, &g.CreatedAt, &g.Activated); err != nil {
				break
			}
			out.RecentSignups = append(out.RecentSignups, g)
		}
		rows.Close()
	}

	// --- Unanswered Ask questions (30d) — the content-gap to-do list ---
	if rows, err := s.DB.QueryContext(ctx, `
		SELECT a.question, COALESCE(NULLIF(u.display_name, ''), u.username, '') AS who, a.created_at
		  FROM ask_log a
		  LEFT JOIN users u ON u.id = a.user_id
		 WHERE a.answered = 0 AND a.created_at >= $1`+and(askAdminA)+`
		 ORDER BY a.created_at DESC LIMIT 12`, cut30); err == nil {
		defer rows.Close()
		for rows.Next() {
			var ua statsUnanswered
			if err := rows.Scan(&ua.Question, &ua.Who, &ua.CreatedAt); err != nil {
				break
			}
			out.UnansweredAsks = append(out.UnansweredAsks, ua)
		}
		rows.Close()
	}

	writeJSON(w, http.StatusOK, out)
}

// fillCumulative seeds dst[i] with a running cumulative total across `days`: the
// baseline count (rows that existed before the window) plus each day's new rows,
// carried forward. Best-effort — a query error just leaves zeros.
func fillCumulative(ctx context.Context, db *sql.DB, dst []int64, days []string, idx map[string]int, baselineQ, perDayQ, cut string) {
	var baseline int64
	_ = db.QueryRowContext(ctx, baselineQ, cut).Scan(&baseline)

	perDay := make([]int64, len(days))
	if rows, err := db.QueryContext(ctx, perDayQ, cut); err == nil {
		defer rows.Close()
		for rows.Next() {
			var day string
			var n int64
			if err := rows.Scan(&day, &n); err != nil {
				break
			}
			if i, ok := idx[day]; ok {
				perDay[i] = n
			}
		}
		rows.Close()
	}
	running := baseline
	for i := range days {
		running += perDay[i]
		dst[i] = running
	}
}
