package api

import (
	"context"
	"database/sql"
	"math"
	"strconv"
	"strings"
	"time"
)

// billing_events.go — the money-path trail on the unified events feed.
//
// Why this file exists: reconcileBilling mutated plan state and recorded
// NOTHING, so the feed held the checkout click and no outcome. That made
// "three people reached checkout, none paid" answerable and every follow-up
// unanswerable — did they ever open the Polar page, did anything fail, what did
// a converting user give up, when did a trial actually lapse. Each type below
// is one step on the path from intent to money, so the funnel is reconstructable
// from `events` alone, after the fact, without new tooling.
//
// Detail is `key=value` pairs joined by spaces. It stays greppable from psql
// (`detail LIKE 'plan=personal_plus%'`) and parses cleanly if a real report ever
// wants it — freeform prose in this column would make both awkward.
//
// These rows are rare (single digits a month) and are exempted from the events
// retention GC in events_gc.go: a funnel you can only see 180 days of is not a
// funnel.
const (
	// evtBillingPlansViewed — the paid tiers were rendered to someone. The step
	// before intent: it separates "never saw the offer" from "saw it, declined".
	evtBillingPlansViewed = "billing.plans_viewed"
	// evtBillingCheckout — a Polar checkout URL was minted for this account.
	// Intent, not money: it says the button was pressed, nothing more.
	evtBillingCheckout = "billing.checkout"
	// evtBillingCheckoutStatus — Polar's own verdict on that checkout session
	// (`expired` = the page was opened and abandoned). The ONLY signal that
	// distinguishes "was blocked" from "looked and declined"; needs the
	// checkout.updated webhook enabled in the Polar dashboard, see billing.go.
	evtBillingCheckoutStatus = "billing.checkout_status"
	// evtBillingSubUpdate — a subscription state event landed. The type says an
	// event arrived; `status=` in the detail says which (active / past_due / …),
	// so a new Polar status doesn't need a new type here.
	evtBillingSubUpdate = "billing.subscription_update"
	// evtBillingSubCanceled — cancellation SCHEDULED; access runs to period end.
	evtBillingSubCanceled = "billing.subscription_canceled"
	// evtBillingSubRevoked — the period ended and the account fell to free.
	evtBillingSubRevoked = "billing.subscription_revoked"
	// evtBillingPaid — money actually received (first payment or renewal).
	evtBillingPaid = "billing.payment"
	// evtBillingTrialStarted — a self-serve signup was granted the trial tier.
	evtBillingTrialStarted = "billing.trial_started"
	// evtBillingTrialEnding / evtBillingTrialExpired — emitted by the sweep in
	// billing_trial_sweep.go, because trial expiry is derived and nothing else
	// observes it.
	evtBillingTrialEnding  = "billing.trial_ending"
	evtBillingTrialExpired = "billing.trial_expired"
)

// billingDetail joins `key=value` fragments, skipping empties so a caller can
// pass a conditional field inline without building a slice first.
func billingDetail(parts ...string) string {
	kept := parts[:0:0]
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " ")
}

// planDetail renders `plan=<key>` for a Polar product id, or "" when the product
// is not one of ours — an unmapped SKU must not invent a plan name in the trail.
func (s *Server) planDetail(productID string) string {
	if productID == "" {
		return ""
	}
	if k, ok := s.billing.PlanFor(productID); ok {
		return "plan=" + k
	}
	return ""
}

// recordBillingEvent appends one billing.* row for an account.
//
// Webhook-driven events have no session and no request, so the actor is the
// account's own user for a personal account and NULL (system) for an org — an
// org's plan change is not any one person's action. The target always names the
// account either way, so `target_kind='org' AND target_id=N` finds an org's
// whole billing history regardless.
func (s *Server) recordBillingEvent(ctx context.Context, acct account, typ, detail string) {
	id := acct.ID
	e := eventInput{
		Type:       typ,
		TargetKind: acct.Kind,
		TargetID:   &id,
		Detail:     detail,
	}
	if acct.Kind == accountUser {
		e.ActorUserID = &id
	}
	recordEvent(ctx, s.DB, e)
}

// recordTrialStarted logs the grant of a signup trial. Called from each site
// that sets newUser.Trial (password registration, social SSO). `ex` is the same
// handle the insert used, so a signup inside a transaction records its trial in
// that transaction — a rollback takes the event with it.
func recordTrialStarted(ctx context.Context, ex emailTokenExec, userID int64, username, planKey string, days int) {
	recordEvent(ctx, ex, eventInput{
		Type:        evtBillingTrialStarted,
		ActorUserID: &userID,
		ActorLabel:  username,
		TargetKind:  accountUser,
		TargetID:    &userID,
		Detail:      billingDetail("plan="+planKey, "days="+strconv.Itoa(days)),
	})
}

// boolDetail renders a flag as `key=1` / `key=0` — the schema's own convention
// for booleans (INTEGER 0/1), kept here so the trail reads the same as the rows.
func boolDetail(key string, v bool) string {
	if v {
		return key + "=1"
	}
	return key + "=0"
}

// kvTime renders `key=<utc timestamp>`, or "" for a nil time — fmtPolarTime
// returns `any` (nil for the SQL NULL its other callers want), which does not
// concatenate.
func kvTime(key string, t *time.Time) string {
	if t == nil {
		return ""
	}
	return key + "=" + t.UTC().Format("2006-01-02T15:04:05Z")
}

// trialDaysRemaining reports whole days left on a user's live tela trial.
//
// This is THE number for anything that acts on a trial's remaining time, and it
// has two consumers that must never disagree: CreateCheckout, which hands it to
// Polar as the days to defer the first charge, and the events trail, which
// records what a conversion deferred. If they computed it separately, the trail
// would eventually describe a different deal from the one the customer got.
//
// Rounds UP. The count becomes a charge date, and rounding down bills a day
// before the date the UI promised — on a payment path that is a refund request,
// not a rounding error. Ceil errs in the customer's favour by at most 24h.
//
// ok is false when there is no live trial (no row, already lapsed, or a lookup
// failure): callers then charge immediately, which is the conservative outcome.
func trialDaysRemaining(ctx context.Context, q queryer, userID int64) (int, bool) {
	var days sql.NullFloat64
	err := q.QueryRowContext(ctx, `
		SELECT EXTRACT(EPOCH FROM (u.trial_ends_at::timestamp - (now() AT TIME ZONE 'UTC'))) / 86400
		  FROM users u
		 WHERE u.id = $1
		   AND NULLIF(u.trial_ends_at, '') IS NOT NULL
		   AND u.trial_ends_at::timestamp > (now() AT TIME ZONE 'UTC')`, userID).Scan(&days)
	if err != nil || !days.Valid || days.Float64 <= 0 {
		return 0, false
	}
	return int(math.Ceil(days.Float64)), true
}

// trialDaysDeferredDetail renders `trial_days_deferred=N` for the conversion
// event, or "" when no trial was in flight.
//
// The key says DEFERRED, not "left": since checkout passes the remaining days to
// Polar, converting early no longer forfeits them — it moves the first charge to
// the day the trial would have ended. The old name would now describe the
// opposite of what happens, in the one record built to explain conversions.
func trialDaysDeferredDetail(ctx context.Context, q queryer, userID int64) string {
	n, ok := trialDaysRemaining(ctx, q, userID)
	if !ok {
		return ""
	}
	return "trial_days_deferred=" + strconv.Itoa(n)
}
