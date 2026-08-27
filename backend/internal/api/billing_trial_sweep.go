package api

import (
	"context"
	"database/sql"
	"log/slog"
	"time"
)

// billing_trial_sweep.go — the observer for trial lifecycle moments.
//
// A trial's expiry is DERIVED, never stored: planFor resolves the effective plan
// from trial_ends_at on every request (limits.go), which is why expiry needs no
// job. The cost of that design is that nothing ever *happens* when a trial
// lapses — no row changes, no code runs — so the single most interesting moment
// in the funnel left no trace at all. You could see that someone signed up and
// that they were not paying today, and nothing in between.
//
// This sweep is the missing observer. It records two moments per user:
//
//	billing.trial_ending  — crossed into the last trialEndingDays, still on trial.
//	                        The moment the offer stops being a bad deal (before
//	                        this line, subscribing forfeits free days for nothing)
//	                        and the moment TrialBanner starts showing.
//	billing.trial_expired — trial_ends_at passed with no subscription. The
//	                        conversion that did not happen.
//
// It writes events only — no plan state, no email, no user-visible effect. The
// resolver stays the authority on what a trial grants.
//
// Idempotency with no schema change: each emit is guarded by NOT EXISTS over the
// same (type, user) pair, and candidates are bounded to trialSweepWindowDays. A
// row aged out by the events GC therefore cannot resurrect an emit — at the
// default 180-day retention the user is five months outside the window before
// their row is eligible for deletion. Shrinking TELA_EVENTS_RETENTION_DAYS below
// the window is what would reintroduce duplicates; it is checked at startup.
const (
	// trialSweepInterval — trials are measured in days, so this only has to be
	// comfortably sub-daily. It also bounds how late an event's timestamp can be
	// relative to the moment it describes.
	trialSweepInterval = 6 * time.Hour
	// trialEndingDays must match the backend's own trial-banner window
	// (userTrialStatus in auth.go), so "we told them" and "they were told" are
	// the same event rather than two nearly-aligned guesses.
	trialEndingDays = 7
	// trialSweepWindowDays bounds candidates to recent crossings: it stops a
	// first run on an existing database from emitting a backlog of years-old
	// expiries as though they happened today, and keeps the NOT EXISTS guard
	// safely inside the events retention.
	trialSweepWindowDays = 30
)

// startTrialSweep runs the trial lifecycle observer until ctx is cancelled.
// Sweeps once at startup (so a restart doesn't skip a crossing) then on the
// interval. Best-effort throughout: a failed sweep is logged and retried next
// tick — this records history, it must never interfere with serving.
func (s *Server) startTrialSweep(ctx context.Context) {
	if retentionDays() < trialSweepWindowDays {
		slog.Warn("billing: events retention is shorter than the trial sweep window; trial events may be re-emitted",
			"retention_days", retentionDays(), "window_days", trialSweepWindowDays)
	}
	if err := s.sweepTrials(ctx); err != nil {
		slog.Error("billing: trial sweep initial run failed", "err", err)
	}
	t := time.NewTicker(trialSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.sweepTrials(ctx); err != nil {
				slog.Error("billing: trial sweep failed", "err", err)
			}
		}
	}
}

// sweepTrials emits both lifecycle events for whoever has newly crossed each
// line. A subscriber is invisible to both queries for free: converting NULLs
// trial_ends_at (see reconcileBilling), so a converted trial has nothing left to
// expire.
func (s *Server) sweepTrials(ctx context.Context) error {
	if err := s.emitTrialEvents(ctx, evtBillingTrialEnding, `
		    u.trial_ends_at::timestamp > (now() AT TIME ZONE 'UTC')
		AND u.trial_ends_at::timestamp <= (now() AT TIME ZONE 'UTC') + make_interval(days => $2)`,
		trialEndingDays); err != nil {
		return err
	}
	return s.emitTrialEvents(ctx, evtBillingTrialExpired, `
		    u.trial_ends_at::timestamp <= (now() AT TIME ZONE 'UTC')
		AND u.trial_ends_at::timestamp > (now() AT TIME ZONE 'UTC') - make_interval(days => $2)`,
		trialSweepWindowDays)
}

// emitTrialEvents records `typ` for every user matching `crossed` who has not
// had it recorded already. `crossed` is a trusted SQL fragment built in this
// file (never user input) using $1 = event type and $2 = its day bound.
func (s *Server) emitTrialEvents(ctx context.Context, typ, crossed string, days int) error {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT u.id, u.username, COALESCE(p.name, u.trial_plan_key), u.trial_ends_at
		  FROM users u
		  LEFT JOIN plans p ON p.key = u.trial_plan_key
		 WHERE NULLIF(u.trial_ends_at, '') IS NOT NULL
		   AND u.trial_plan_key IS NOT NULL
		   AND (`+crossed+`)
		   AND NOT EXISTS (
		         SELECT 1 FROM events e
		          WHERE e.type = $1 AND e.actor_user_id = u.id)`, typ, days)
	if err != nil {
		return err
	}
	defer rows.Close()

	type crosser struct {
		id       int64
		username string
		plan     sql.NullString
		endsAt   string
	}
	// Collected before recording: the emit INSERTs into `events`, which is the
	// same table the NOT EXISTS above reads, and writing through an open cursor
	// on an overlapping query is the kind of thing that works until it doesn't.
	var found []crosser
	for rows.Next() {
		var c crosser
		if err := rows.Scan(&c.id, &c.username, &c.plan, &c.endsAt); err != nil {
			return err
		}
		found = append(found, c)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, c := range found {
		id := c.id
		recordEvent(ctx, s.DB, eventInput{
			Type:        typ,
			ActorUserID: &id,
			ActorLabel:  c.username,
			TargetKind:  accountUser,
			TargetID:    &id,
			Detail:      billingDetail("plan="+c.plan.String, "trial_ends_at="+c.endsAt),
		})
	}
	if len(found) > 0 {
		slog.Info("billing: trial sweep", "type", typ, "users", len(found))
	}
	return nil
}
