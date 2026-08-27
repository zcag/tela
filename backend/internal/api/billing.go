package api

// billing.go — self-serve subscriptions via Polar (internal/billing). Three
// surfaces:
//   POST /api/billing/checkout  session-authed; returns a hosted checkout URL
//   POST /api/billing/portal    session-authed; returns a manage-subscription URL
//   POST /api/billing/webhook   PUBLIC; Polar → us, self-authenticates by signature
//
// Entitlement is NOT granted from the checkout redirect (the user can close the
// tab); it's granted by the webhook reconciler, which maps a Polar product back
// onto an account's plan_key — the same column limits.go enforces. The Polar
// product↔plan wiring lives in env (TELA_POLAR_PRODUCTS); see internal/billing.

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zcag/tela/backend/internal/billing"
)

// acctExternalID encodes an account as Polar's external_customer_id ("user:7" /
// "org:3"). Polar echoes it on every subscription/order webhook as
// data.customer.external_id, so the reconciler resolves the account with no
// prior customer-id lookup.
func acctExternalID(a account) string {
	return a.Kind + ":" + strconv.FormatInt(a.ID, 10)
}

// parseAcctExternalID is the inverse, tolerant of anything that isn't ours.
func parseAcctExternalID(s string) (account, bool) {
	kind, idStr, ok := strings.Cut(s, ":")
	if !ok || (kind != accountUser && kind != accountOrg) {
		return account{}, false
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		return account{}, false
	}
	return account{Kind: kind, ID: id}, true
}

// freePlanKey is the tier an account falls back to when its subscription is
// revoked.
func freePlanKey(kind string) string {
	if kind == accountOrg {
		return "org_free"
	}
	return "personal_free"
}

// acctTable is the table holding plan_key + billing state for an account kind.
// Both are fixed literals (never user input), safe to interpolate.
func acctTable(kind string) string {
	if kind == accountOrg {
		return "orgs"
	}
	return "users"
}

type checkoutRequest struct {
	PlanKey  string `json:"plan_key"`
	OrgID    int64  `json:"org_id"`   // 0 = the caller's personal account
	Interval string `json:"interval"` // "month" (default) | "year"
}

// CreateCheckout starts a Polar checkout for a tier and returns its hosted URL.
// Personal upgrade needs only a session; an org upgrade requires the caller be an
// admin of that org. The tier must exist, match the account kind, and be wired to
// a Polar product (free/enterprise tiers aren't self-serve).
func (s *Server) CreateCheckout(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	if !s.billing.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "billing_disabled", "self-serve billing is not configured on this instance")
		return
	}
	var req checkoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}
	if req.PlanKey == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "plan_key is required")
		return
	}

	var (
		acct  account
		email string
	)
	if req.OrgID > 0 {
		if !s.requireOrgAdmin(w, r, req.OrgID) {
			return
		}
		acct = account{Kind: accountOrg, ID: req.OrgID}
	} else {
		acct = account{Kind: accountUser, ID: u.ID}
		email = u.Email
	}

	ctx := r.Context()
	var planKind string
	if err := s.DB.QueryRowContext(ctx, `SELECT account_kind FROM plans WHERE key = $1`, req.PlanKey).Scan(&planKind); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "unknown plan_key")
		return
	}
	if planKind != acct.Kind {
		writeError(w, http.StatusBadRequest, "bad_request", "plan_key does not match the account")
		return
	}
	// Cadence: yearly when explicitly asked, monthly otherwise. A tier may have a
	// monthly product but no yearly one — ProductFor then 400s plan_not_purchasable.
	interval := billing.IntervalMonth
	if req.Interval == billing.IntervalYear {
		interval = billing.IntervalYear
	}
	product, ok := s.billing.ProductFor(req.PlanKey, interval)
	if !ok {
		writeError(w, http.StatusBadRequest, "plan_not_purchasable", "this plan can't be purchased self-serve at that cadence")
		return
	}

	// Per-seat tiers (org): seed the quantity from the current member count so the
	// first invoice matches the team. Seat changes after that are a follow-up.
	seats := 0
	if acct.Kind == accountOrg {
		if n, err := countOrgMembers(ctx, s.DB, acct.ID); err == nil {
			seats = int(n)
		}
		if seats < 1 {
			seats = 1
		}
	}

	url, err := s.billing.CreateCheckout(ctx, billing.CheckoutInput{
		ProductID:          product,
		SuccessURL:         s.linkOrigin(r) + "/settings?tab=billing&checkout={CHECKOUT_ID}",
		ExternalCustomerID: acctExternalID(acct),
		CustomerEmail:      email,
		Seats:              seats,
		Metadata: map[string]string{
			"plan_key":     req.PlanKey,
			"account_kind": acct.Kind,
			"account_id":   strconv.FormatInt(acct.ID, 10),
			"interval":     interval,
		},
	})
	if err != nil {
		slog.Error("billing: create checkout", "account_kind", acct.Kind, "account_id", acct.ID, "plan", req.PlanKey, "err", err)
		writeError(w, http.StatusBadGateway, "billing_error", "could not start checkout")
		return
	}
	// Recorded on the events feed as billing.*, NOT via s.audit: access_audit is
	// the access-control trail (org / membership / grant / auto-join / domain —
	// see audit.go) kept forever for compliance, and its mirror would file this
	// as `access.billing.checkout`. A checkout start is neither an access change
	// nor a sibling of one; filing it there put the only billing row we had under
	// a name no billing query would think to look for.
	s.recordRequestEvent(r, eventInput{
		Type:       evtBillingCheckout,
		TargetKind: acct.Kind,
		TargetID:   &acct.ID,
		Detail:     billingDetail("plan="+req.PlanKey, "interval="+interval),
	})
	writeJSON(w, http.StatusOK, map[string]string{"url": url})
}

type portalRequest struct {
	OrgID        int64  `json:"org_id"`        // 0 = the caller's personal account
	CancelReason string `json:"cancel_reason"` // optional; stored for churn analysis
}

// CreateBillingPortal returns a Polar customer-portal URL so the account holder
// can manage / cancel / update payment. Requires an existing customer on the
// account (set by the first checkout's webhook); 400s otherwise.
func (s *Server) CreateBillingPortal(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	if !s.billing.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "billing_disabled", "self-serve billing is not configured on this instance")
		return
	}
	var req portalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}

	var acct account
	if req.OrgID > 0 {
		if !s.requireOrgAdmin(w, r, req.OrgID) {
			return
		}
		acct = account{Kind: accountOrg, ID: req.OrgID}
	} else {
		acct = account{Kind: accountUser, ID: u.ID}
	}

	ctx := r.Context()
	var custID sql.NullString
	if err := s.DB.QueryRowContext(ctx,
		`SELECT polar_customer_id FROM `+acctTable(acct.Kind)+` WHERE id = $1`, acct.ID).Scan(&custID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "load account failed")
		return
	}
	if !custID.Valid || custID.String == "" {
		writeError(w, http.StatusBadRequest, "no_subscription", "no subscription to manage yet")
		return
	}

	if r := strings.TrimSpace(req.CancelReason); r != "" {
		if len(r) > 500 {
			r = r[:500]
		}
		_, _ = s.DB.ExecContext(ctx,
			`UPDATE `+acctTable(acct.Kind)+` SET cancel_reason = $1, updated_at = tela_now() WHERE id = $2`,
			r, acct.ID)
	}

	url, err := s.billing.CreateCustomerSession(ctx, acctExternalID(acct))
	if err != nil {
		slog.Error("billing: create customer session", "account_kind", acct.Kind, "account_id", acct.ID, "err", err)
		writeError(w, http.StatusBadGateway, "billing_error", "could not open billing portal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": url})
}

// PolarWebhook is the Polar → tela reconciliation endpoint. PUBLIC (on
// auth.IsPublicPath); it self-authenticates by verifying the Standard Webhooks
// signature against the configured secret. Idempotent: each delivery's
// webhook-id is recorded so a redelivery is acknowledged without re-applying.
func (s *Server) PolarWebhook(w http.ResponseWriter, r *http.Request) {
	if !s.billing.Enabled() {
		// Can't verify without the secret — refuse rather than trust blindly.
		writeError(w, http.StatusServiceUnavailable, "billing_disabled", "billing not configured")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not read body")
		return
	}
	if err := billing.VerifyWebhook(s.billing.WebhookSecret(), r.Header, body); err != nil {
		slog.Warn("billing: webhook verification failed", "err", err)
		polarWebhookErrors.WithLabelValues("signature").Inc()
		writeError(w, http.StatusBadRequest, "invalid_signature", "webhook signature verification failed")
		return
	}
	evt, err := billing.ParseEvent(body)
	if err != nil {
		polarWebhookErrors.WithLabelValues("parse").Inc()
		writeError(w, http.StatusBadRequest, "bad_request", "could not parse event")
		return
	}

	ctx := r.Context()
	eventID := r.Header.Get("webhook-id")

	// Fast-path dedup: a delivery we've already processed is acknowledged as-is.
	var seen bool
	if err := s.DB.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM polar_webhook_events WHERE event_id = $1)`, eventID).Scan(&seen); err == nil && seen {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "dedup": true})
		return
	}

	// Reconcile (idempotent — a concurrent duplicate that slips past the check
	// above re-applies the same end state). On failure we DON'T record the id and
	// return 500 so Polar redelivers.
	if err := s.reconcileBilling(ctx, evt); err != nil {
		slog.Error("billing: reconcile failed", "type", evt.Type, "id", evt.Data.ID, "err", err)
		polarWebhookErrors.WithLabelValues("reconcile").Inc()
		writeError(w, http.StatusInternalServerError, "internal", "reconcile failed")
		return
	}
	if _, err := s.DB.ExecContext(ctx,
		`INSERT INTO polar_webhook_events (event_id, event_type) VALUES ($1, $2) ON CONFLICT (event_id) DO NOTHING`,
		eventID, evt.Type); err != nil {
		slog.Error("billing: record webhook id", "event_id", eventID, "err", err)
	}
	polarLastWebhook.SetToCurrentTime()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// reconcileBilling maps one Polar event onto the account's plan + billing state.
// Unresolvable / out-of-scope events are a no-op (logged), never an error — only
// a genuine DB failure returns err (→ 500 → redelivery).
func (s *Server) reconcileBilling(ctx context.Context, evt billing.Event) error {
	acct, ok := parseAcctExternalID(evt.Data.Customer.ExternalID)
	if !ok {
		acct, ok = acctFromMetadata(evt.Data.Metadata)
	}
	if !ok {
		slog.Warn("billing: event without a resolvable account", "type", evt.Type, "id", evt.Data.ID)
		return nil
	}

	// The self-host Enterprise license is a separate SKU (its product is kept out
	// of the plan map): route its events to license issuance, not plan changes.
	// Match on the product, OR on a subscription id we already issued against —
	// Polar's cancel/revoke payloads don't reliably carry product_id, and without
	// the sub-id fallback such an event would fall through to the plan reconciler
	// (a no-op there) and leave the license row wrongly showing Active.
	if s.selfHostProductID != "" &&
		(evt.Data.ProductID == s.selfHostProductID || s.isSelfHostSubscription(ctx, evt.Data.ID)) {
		return s.reconcileSelfHostLicense(ctx, evt, acct)
	}

	table := acctTable(acct.Kind)

	switch evt.Type {
	case "checkout.updated":
		// Polar's checkout lifecycle — the only signal that says what became of a
		// URL we minted. `expired` means the page was reached and abandoned, which
		// is what separates "something blocked them" from "they looked and
		// declined"; without it the trail stops at intent and both readings stay
		// alive forever. Purely observational: the subscription events remain
		// authoritative for plan state, so this case touches no columns.
		//
		// Requires checkout.updated to be enabled on the webhook endpoint in the
		// Polar dashboard — code alone does not subscribe us to it.
		if st := evt.Data.Status; st != "" && st != "open" {
			s.recordBillingEvent(ctx, acct, evtBillingCheckoutStatus,
				billingDetail("status="+st, s.planDetail(evt.Data.ProductID)))
		}
		return nil

	case "subscription.created", "subscription.active", "subscription.updated":
		planKey, ok := s.billing.PlanFor(evt.Data.ProductID)
		if !ok {
			slog.Warn("billing: subscription for an unmapped product", "product", evt.Data.ProductID, "type", evt.Type)
			return nil
		}
		cancel := 0
		if evt.Data.CancelAtPeriodEnd {
			cancel = 1
		}
		periodEnd := fmtPolarTime(evt.Data.CurrentPeriodEnd)
		custID := nullIfEmpty(evt.Data.CustomerID)
		status := evt.Data.Status
		if status == "" {
			status = "active"
		}
		// Grant the paid tier only while the sub is actually paying (active /
		// trialing). Other transient states (past_due) keep the existing plan but
		// still record the status so the UI can warn; the eventual `revoked`
		// downgrades.
		grantPlan := status == "active" || status == "trialing"
		// A user's trial is cleared on a real paid plan so the trial banner stops
		// and planFor resolves the base plan_key directly.
		clearTrial := ""
		trialDaysForfeited := ""
		if acct.Kind == accountUser && grantPlan {
			clearTrial = ", trial_plan_key = NULL, trial_ends_at = NULL"
			// Read what the trial was worth BEFORE the UPDATE wipes it. Converting
			// mid-trial forfeits the remaining free days (no trial period is passed
			// to Polar, so billing starts immediately) — and once trial_ends_at is
			// NULL, nothing anywhere can say how many days that was. Recording it
			// here is the only chance: it's what tells you whether people convert
			// early by choice or only ever at expiry.
			trialDaysForfeited = trialDaysLeft(ctx, s.DB, acct.ID)
		}
		setPlan := ""
		args := []any{status, periodEnd, cancel, nullIfEmpty(evt.Data.ID), custID}
		if grantPlan {
			setPlan = "plan_key = $6, "
			args = append(args, planKey)
		}
		args = append(args, acct.ID)
		idPlaceholder := "$" + strconv.Itoa(len(args))
		_, err := s.DB.ExecContext(ctx, `UPDATE `+table+` SET `+setPlan+`
			subscription_status = $1,
			subscription_period_end = $2,
			subscription_cancel_at_period_end = $3,
			polar_subscription_id = $4,
			polar_customer_id = COALESCE($5, polar_customer_id)`+clearTrial+`,
			updated_at = tela_now()
			WHERE id = `+idPlaceholder, args...)
		if err != nil {
			return err
		}
		s.recordBillingEvent(ctx, acct, evtBillingSubUpdate, billingDetail(
			"status="+status, "plan="+planKey, "event="+evt.Type,
			boolDetail("granted", grantPlan), trialDaysForfeited))
		return nil

	case "subscription.canceled":
		// Cancellation scheduled — access continues to period end. Flag it (the UI
		// shows "cancels on <date>"); do NOT downgrade here. Scope to the account's
		// CURRENT subscription so a stale/reordered event for an OLD sub (after a
		// cancel-then-resubscribe) can't flag a healthy active subscription.
		_, err := s.DB.ExecContext(ctx, `UPDATE `+table+` SET
			subscription_cancel_at_period_end = 1,
			subscription_period_end = COALESCE($1, subscription_period_end),
			updated_at = tela_now()
			WHERE id = $2 AND polar_subscription_id = $3`,
			fmtPolarTime(evt.Data.CurrentPeriodEnd), acct.ID, evt.Data.ID)
		if err != nil {
			return err
		}
		// cancel_reason is captured at the portal call (CreateBillingPortal), so
		// the churn reason and the cancellation land in two different places —
		// name it here so a reader of the feed knows where to look for the why.
		s.recordBillingEvent(ctx, acct, evtBillingSubCanceled, billingDetail(
			kvTime("period_end", evt.Data.CurrentPeriodEnd), "reason_in=cancel_reason"))
		return nil

	case "subscription.revoked":
		// Period actually ended → downgrade to the free tier and clear sub state.
		// Scoped to the CURRENT subscription: a late/reordered `revoked` for a
		// superseded OLD sub must NOT downgrade an account that has since
		// resubscribed (created/active already moved polar_subscription_id to the
		// new sub). Without this guard the user pays but sits on Free.
		_, err := s.DB.ExecContext(ctx, `UPDATE `+table+` SET
			plan_key = $1,
			subscription_status = 'canceled',
			subscription_cancel_at_period_end = 0,
			polar_subscription_id = NULL,
			subscription_period_end = NULL,
			updated_at = tela_now()
			WHERE id = $2 AND polar_subscription_id = $3`,
			freePlanKey(acct.Kind), acct.ID, evt.Data.ID)
		if err != nil {
			return err
		}
		s.recordBillingEvent(ctx, acct, evtBillingSubRevoked,
			billingDetail("plan="+freePlanKey(acct.Kind)))
		return nil

	case "order.paid":
		// Renewal or first payment confirming money received. Subscription events
		// carry the authoritative plan/state; here we just make sure status reads
		// active for an account that already has a subscription.
		_, err := s.DB.ExecContext(ctx, `UPDATE `+table+` SET
			subscription_status = 'active', updated_at = tela_now()
			WHERE id = $1 AND polar_subscription_id IS NOT NULL`, acct.ID)
		// Self-heal seat drift: reconcile the org's billed seats to its current
		// membership on every payment, bounding any missed fire-and-forget sync to
		// one billing period. Fire-and-forget; never blocks the webhook ack.
		if err == nil && acct.Kind == accountOrg {
			go s.syncOrgSeats(context.Background(), acct.ID)
		}
		if err != nil {
			return err
		}
		// The only event in the set that means money actually moved. Everything
		// else on the path is intent or state.
		s.recordBillingEvent(ctx, acct, evtBillingPaid,
			billingDetail(s.planDetail(evt.Data.ProductID), "order="+evt.Data.ID))
		return nil
	}
	return nil
}

// syncOrgSeats best-effort updates an org's Polar subscription seat count to its
// current member count, so a seat-billed plan (Team) invoices the real team size.
// No-op unless billing is configured and the org has a subscription on file
// (Free orgs / no sub → nothing to do). Called fire-and-forget from the member
// add/remove paths; any failure is logged, never blocks the membership change.
// Use a background context so it outlives the request that triggered it.
func (s *Server) syncOrgSeats(ctx context.Context, orgID int64) {
	if !s.billing.Enabled() {
		return
	}
	var subID sql.NullString
	if err := s.DB.QueryRowContext(ctx,
		`SELECT polar_subscription_id FROM orgs WHERE id = $1`, orgID).Scan(&subID); err != nil ||
		!subID.Valid || subID.String == "" {
		return
	}
	n, err := countOrgMembers(ctx, s.DB, orgID)
	if err != nil {
		slog.Warn("billing: seat sync count", "org_id", orgID, "err", err)
		return
	}
	if err := s.billing.UpdateSubscriptionSeats(ctx, subID.String, int(n)); err != nil {
		slog.Warn("billing: seat sync", "org_id", orgID, "seats", n, "err", err)
	}
}

// acctFromMetadata recovers an account from the checkout metadata we set, as a
// fallback when external_customer_id didn't round-trip. Values arrive as JSON
// (string or number), so handle both.
func acctFromMetadata(m map[string]any) (account, bool) {
	kind, _ := m["account_kind"].(string)
	if kind != accountUser && kind != accountOrg {
		return account{}, false
	}
	var id int64
	switch v := m["account_id"].(type) {
	case string:
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return account{}, false
		}
		id = n
	case float64:
		id = int64(v)
	default:
		return account{}, false
	}
	if id <= 0 {
		return account{}, false
	}
	return account{Kind: kind, ID: id}, true
}

// fmtPolarTime renders a Polar ISO-8601 timestamp into tela's TEXT-datetime
// convention ('YYYY-MM-DD HH:MM:SS' UTC), or SQL NULL when absent.
func fmtPolarTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format("2006-01-02 15:04:05")
}
