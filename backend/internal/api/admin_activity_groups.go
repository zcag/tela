package api

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"time"
)

// admin_activity_groups.go — GET /api/admin/activity/groups?by=space|org&window=
//
// The People table answers "who uses tela". On a multi-team instance the
// question behind that is usually "which TEAM uses tela" or "which spaces are
// alive" — and summing a user column by eye is not an answer. Same windows and
// the same underlying tables as admin_user_metrics.go, keyed differently.
//
// The two groupings are deliberately computed from different sources, because
// they mean different things:
//
//   - SPACE activity is a property of the content: edits on pages in that space,
//     views of those pages. Whoever did it.
//   - ORG activity is a property of the people: the summed activity of its
//     members, wherever they did it. A user in two orgs therefore counts toward
//     both — the alternative (splitting them) would make every number smaller
//     than the truth and answer nobody's question.

type activityGroup struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Kind-specific subtitle: the owning handle for a space, the member count
	// for an org. Rendered under the name; never parsed.
	Detail string `json:"detail"`

	Edits        int64  `json:"edits"`
	AgentEdits   int64  `json:"agent_edits"`
	SyncEdits    int64  `json:"sync_edits"`
	Views        int64  `json:"views"`
	Asks         int64  `json:"asks"`
	LLMCalls     int64  `json:"llm_calls"`
	People       int64  `json:"people"`        // distinct contributors (space) / members (org)
	ActivePeople int64  `json:"active_people"` // of those, with any activity in the window
	Pages        int64  `json:"pages"`         // live pages (space only)
	LastActive   string `json:"last_active"`   // most recent activity, '' when none
}

// AdminActivityGroups returns per-space or per-org activity for the window.
// Instance-admin only.
func (s *Server) AdminActivityGroups(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireInstanceAdmin(w, r); !ok {
		return
	}
	win := parseAdminUserWindow(r.URL.Query().Get("window"), time.Now().UTC())
	ctx := r.Context()

	var groups []activityGroup
	if r.URL.Query().Get("by") == "org" {
		groups = s.orgActivity(ctx, win)
	} else {
		groups = s.spaceActivity(ctx, win)
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups, "window": win.Key})
}

// spaceActivity aggregates by the space the content lives in. Personal spaces
// are included — "which spaces are alive" is not a question about team spaces
// only, and hiding them would silently drop most of an instance's writing.
func (s *Server) spaceActivity(ctx context.Context, win adminUserWindow) []activityGroup {
	out := map[int64]*activityGroup{}
	at := func(id int64) *activityGroup {
		g, ok := out[id]
		if !ok {
			g = &activityGroup{ID: id}
			out[id] = g
		}
		return g
	}

	// Names + live page counts for every space, so a space with pages but no
	// activity still appears (that emptiness is the finding). The owning handle
	// comes from spaceHandleExpr — the ONE definition of who owns a space; see
	// public_handles.go for why re-deriving it is the bug that keeps coming back.
	if rows, err := s.DB.QueryContext(ctx, `
		SELECT s.id, s.name, COUNT(p.id) FILTER (WHERE p.deleted_at IS NULL),
		       `+spaceHandleExpr+`
		  FROM spaces s
		  LEFT JOIN orgs o ON o.id = s.org_id
		  LEFT JOIN pages p ON p.space_id = s.id
		 GROUP BY s.id, s.name, o.slug`); err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, pages int64
			var name, handle string
			if err := rows.Scan(&id, &name, &pages, &handle); err != nil {
				break
			}
			g := at(id)
			g.Name, g.Pages, g.Detail = name, pages, handle
		}
	}

	if rows, err := s.DB.QueryContext(ctx, `
		SELECT p.space_id, COUNT(*),
		       COUNT(*) FILTER (WHERE pr.source = 'agent'),
		       COUNT(*) FILTER (WHERE pr.source LIKE 'sync-%'),
		       COUNT(DISTINCT pr.author_id),
		       MAX(pr.created_at)
		  FROM page_revisions pr
		  JOIN pages p ON p.id = pr.page_id AND p.deleted_at IS NULL
		 WHERE ($1 = '' OR pr.created_at >= $1)
		 GROUP BY p.space_id`, win.Cut); err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, n, agent, sync, people int64
			var last sql.NullString
			if err := rows.Scan(&id, &n, &agent, &sync, &people, &last); err != nil {
				break
			}
			g := at(id)
			g.Edits, g.AgentEdits, g.SyncEdits = n, agent, sync
			g.People, g.ActivePeople = people, people
			g.LastActive = last.String
		}
	}

	// Views resolve through the event's target page — the event row itself has
	// no space, so a deleted page's views drop out with it.
	if rows, err := s.DB.QueryContext(ctx, `
		SELECT p.space_id, COUNT(*)
		  FROM events e
		  JOIN pages p ON p.id = e.target_id AND e.target_kind = 'page' AND p.deleted_at IS NULL
		 WHERE e.type = 'page.view' AND ($1 = '' OR e.created_at >= $1)
		 GROUP BY p.space_id`, win.Cut); err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, n int64
			if err := rows.Scan(&id, &n); err != nil {
				break
			}
			at(id).Views = n
		}
	}

	for id, n := range scanInt64Map(ctx, s.DB, `
		SELECT space_id, COUNT(*) FROM ask_log
		 WHERE space_id IS NOT NULL AND ($1 = '' OR created_at >= $1)
		 GROUP BY space_id`, win.Cut) {
		at(id).Asks = n
	}

	return sortedGroups(out)
}

// orgActivity sums each org's MEMBERS' activity — reusing the per-user metrics
// the People table already computes rather than writing a second, subtly
// different set of aggregates.
func (s *Server) orgActivity(ctx context.Context, win adminUserWindow) []activityGroup {
	perUser := loadAdminUserMetrics(ctx, s.DB, win)
	out := map[int64]*activityGroup{}

	if rows, err := s.DB.QueryContext(ctx, `
		SELECT o.id, o.name, COUNT(m.user_id)
		  FROM orgs o LEFT JOIN org_members m ON m.org_id = o.id
		 GROUP BY o.id, o.name`); err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, members int64
			var name string
			if err := rows.Scan(&id, &name, &members); err != nil {
				break
			}
			label := "1 member"
			if members != 1 {
				label = strconv.FormatInt(members, 10) + " members"
			}
			out[id] = &activityGroup{ID: id, Name: name, People: members, Detail: label}
		}
	}

	if rows, err := s.DB.QueryContext(ctx,
		`SELECT org_id, user_id FROM org_members`); err == nil {
		defer rows.Close()
		for rows.Next() {
			var orgID, userID int64
			if err := rows.Scan(&orgID, &userID); err != nil {
				break
			}
			g, ok := out[orgID]
			if !ok {
				continue
			}
			m, ok := perUser[userID]
			if !ok {
				continue
			}
			g.Edits += m.Edits
			g.AgentEdits += m.AgentEdits
			g.SyncEdits += m.SyncEdits
			g.Views += m.Views
			g.Asks += m.Asks
			g.LLMCalls += m.LLMCalls
			if m.DaysActive > 0 {
				g.ActivePeople++
			}
		}
	}

	return sortedGroups(out)
}

// sortedGroups flattens the map busiest-first, so the caller renders something
// sensible before anyone touches a sort header.
func sortedGroups(m map[int64]*activityGroup) []activityGroup {
	out := make([]activityGroup, 0, len(m))
	for _, g := range m {
		out = append(out, *g)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Edits+out[j].Views > out[j-1].Edits+out[j-1].Views; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
