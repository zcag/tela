package api

// usage.go — read APIs for metering (the write/enforcement side is limits.go):
//   GET  /api/usage              caller's personal-account plan + usage
//   GET  /api/orgs/{id}/usage    an org's plan + usage (org member or instance-admin)
//   GET  /api/plans              every tier (for the UI's plan comparison)
//   PATCH /api/admin/plan        instance-admin sets an account's plan (no payments)

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
)

// usageOut is the plan + current usage for one account. Members is nil for a
// personal account (seats are an org concept).
type usageOut struct {
	AccountKind string `json:"account_kind"`
	AccountID   int64  `json:"account_id"`
	Plan        plan   `json:"plan"`
	Usage       struct {
		Spaces       int64  `json:"spaces"`
		StorageBytes int64  `json:"storage_bytes"`
		Members      *int64 `json:"members,omitempty"`
		LLMCalls     int64  `json:"llm_calls"` // managed AI calls this calendar month
		// Atlas cost meters for this calendar month, so the UI can show how close an
		// account is BEFORE a run is refused. AtlasRuns excludes failed runs, matching
		// what the gate counts.
		AtlasRuns   int64 `json:"atlas_runs"`
		EmbedTokens int64 `json:"embed_tokens"`
	} `json:"usage"`
	// Subscription is the live Polar billing state, omitted when the account has
	// never subscribed (status 'none'). Drives the "Manage subscription" button
	// and the "cancels on <date>" notice in the UI.
	Subscription *subscriptionOut `json:"subscription,omitempty"`
}

type subscriptionOut struct {
	Status            string  `json:"status"`     // active|canceled|past_due
	PeriodEnd         *string `json:"period_end"` // paid-through, when known
	CancelAtPeriodEnd bool    `json:"cancel_at_period_end"`
}

// buildUsage assembles the plan + live usage snapshot for an account.
func (s *Server) buildUsage(ctx context.Context, acct account) (usageOut, error) {
	var out usageOut
	out.AccountKind, out.AccountID = acct.Kind, acct.ID

	p, err := planFor(ctx, s.DB, acct)
	if err != nil {
		return usageOut{}, err
	}
	out.Plan = p

	if out.Usage.Spaces, err = countOwnedSpaces(ctx, s.DB, acct); err != nil {
		return usageOut{}, err
	}
	if out.Usage.StorageBytes, err = sumOwnedStorage(ctx, s.DB, acct); err != nil {
		return usageOut{}, err
	}
	if acct.Kind == accountOrg {
		n, err := countOrgMembers(ctx, s.DB, acct.ID)
		if err != nil {
			return usageOut{}, err
		}
		out.Usage.Members = &n
	}
	if out.Usage.AtlasRuns, err = countAtlasRunsThisMonth(ctx, s.DB, acct); err != nil {
		return usageOut{}, err
	}
	if out.Usage.EmbedTokens, err = sumEmbedTokensThisMonth(ctx, s.DB, acct); err != nil {
		return usageOut{}, err
	}
	// AI calls used this calendar month (the same period cloud_usage is keyed by).
	if err = s.DB.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(llm_calls),0) FROM cloud_usage
		  WHERE account_kind=$1 AND account_id=$2
		    AND period = to_char((now() AT TIME ZONE 'UTC'),'YYYY-MM')`,
		acct.Kind, acct.ID).Scan(&out.Usage.LLMCalls); err != nil {
		return usageOut{}, err
	}

	// Billing state from the account's row (subscription_* columns, migration
	// 0053). Surfaced only when the account has a subscription on file.
	var (
		status    string
		periodEnd sql.NullString
		cancelInt int
	)
	if err = s.DB.QueryRowContext(ctx,
		`SELECT subscription_status, subscription_period_end, subscription_cancel_at_period_end
		   FROM `+acctTable(acct.Kind)+` WHERE id = $1`, acct.ID).
		Scan(&status, &periodEnd, &cancelInt); err != nil {
		return usageOut{}, err
	}
	if status != "none" && status != "" {
		sub := &subscriptionOut{Status: status, CancelAtPeriodEnd: cancelInt == 1}
		if periodEnd.Valid {
			sub.PeriodEnd = &periodEnd.String
		}
		out.Subscription = sub
	}
	return out, nil
}

// GetMyUsage returns the caller's personal-account plan + usage.
func (s *Server) GetMyUsage(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	out, err := s.buildUsage(r.Context(), account{Kind: accountUser, ID: u.ID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "load usage failed")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GetOrgUsage returns an org's plan + usage. Any org member (or instance-admin)
// may read it.
func (s *Server) GetOrgUsage(w http.ResponseWriter, r *http.Request) {
	orgID, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	if _, ok := s.requireOrgMember(w, r, orgID); !ok {
		return
	}
	out, err := s.buildUsage(r.Context(), account{Kind: accountOrg, ID: orgID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "load usage failed")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ListPlans returns every tier so the UI can render a plan comparison. Any
// authenticated user may read.
func (s *Server) ListPlans(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUser(w, r); !ok {
		return
	}
	rows, err := s.DB.QueryContext(r.Context(),
		`SELECT `+planCols+` FROM plans p ORDER BY p.account_kind, p.max_storage_bytes NULLS LAST`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "list plans failed")
		return
	}
	defer rows.Close()
	plans := []plan{}
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "scan plan failed")
			return
		}
		plans = append(plans, p)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "iterate plans failed")
		return
	}
	// ?src=billing marks the render of the paid tiers to a real person, as
	// opposed to the same catalog being read by the admin tier-picker. It is the
	// step BEFORE intent: without it, someone who opened the billing screen and
	// declined is indistinguishable from someone who never saw a price, and the
	// two call for opposite fixes. Undercounts slightly by design — the client
	// caches the catalog, so a revisit inside its stale window sends no request.
	if r.URL.Query().Get("src") == "billing" {
		s.recordRequestEvent(r, eventInput{Type: evtBillingPlansViewed})
	}
	writeJSON(w, http.StatusOK, map[string]any{"plans": plans})
}

type setPlanRequest struct {
	AccountKind string `json:"account_kind"` // "user" | "org"
	AccountID   int64  `json:"account_id"`
	PlanKey     string `json:"plan_key"`
}

// SetAccountPlan assigns a plan to a user or org. Instance-admin only — plan
// changes are an operator action (there's no self-serve billing). The plan's
// account_kind must match the target, so a user can't be put on an org plan.
func (s *Server) SetAccountPlan(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireInstanceAdmin(w, r); !ok {
		return
	}
	var req setPlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}
	if req.AccountKind != accountUser && req.AccountKind != accountOrg {
		writeError(w, http.StatusBadRequest, "bad_request", "account_kind must be 'user' or 'org'")
		return
	}
	if req.AccountID <= 0 || req.PlanKey == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "account_id and plan_key are required")
		return
	}
	ctx := r.Context()

	// The plan must exist and be for the same account kind.
	var planKind string
	err := s.DB.QueryRowContext(ctx, `SELECT account_kind FROM plans WHERE key = $1`, req.PlanKey).Scan(&planKind)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "unknown plan_key")
		return
	}
	if planKind != req.AccountKind {
		writeError(w, http.StatusBadRequest, "bad_request", "plan_key does not match account_kind")
		return
	}

	table := "users"
	set := "plan_key = $1, updated_at = tela_now()"
	if req.AccountKind == accountOrg {
		table = "orgs"
	} else {
		// Clear any active trial so the admin's assignment takes effect now —
		// otherwise the trial CASE in planFor overrides it for up to ~37 days
		// (mirrors how the billing webhook clears the trial on subscribe).
		set += ", trial_plan_key = NULL, trial_ends_at = NULL"
	}
	res, err := s.DB.ExecContext(ctx,
		`UPDATE `+table+` SET `+set+` WHERE id = $2`, req.PlanKey, req.AccountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "set plan failed")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "account not found")
		return
	}
	s.audit(ctx, r, "account.set_plan", req.AccountKind, req.AccountID, req.PlanKey)
	out, err := s.buildUsage(ctx, account{Kind: req.AccountKind, ID: req.AccountID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "load usage failed")
		return
	}
	writeJSON(w, http.StatusOK, out)
}
