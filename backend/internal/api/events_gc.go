package api

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// events_gc.go — retention sweep for the unified events feed. The feed carries
// high-volume types (page.view, api.request), so unlike access_audit (kept
// forever as the canonical access trail) `events` is pruned. Mirrors
// internal/auth/api_key_audit_gc.go.

// defaultEventsRetentionDays is how long an events row lives before the GC
// deletes it. 180 days is generous for an activity feed while keeping the table
// bounded on a busy instance. Override per-deploy via TELA_EVENTS_RETENTION_DAYS.
const defaultEventsRetentionDays = 180

const eventsGCInterval = 6 * time.Hour

// StartEventsGC launches a background goroutine that periodically deletes events
// rows older than the configured retention. Sweeps once on startup, then every
// eventsGCInterval; stops when ctx is cancelled. Configurable via
// TELA_EVENTS_RETENTION_DAYS (positive int; falls back to the default).
func StartEventsGC(ctx context.Context, d *sql.DB) {
	days := retentionDays()
	slog.Info("events: retention GC", "retention_days", days, "sweep_interval", eventsGCInterval)
	go func() {
		if err := purgeEventsOlderThan(ctx, d, days); err != nil {
			slog.Error("events: GC initial sweep failed", "err", err)
		}
		t := time.NewTicker(eventsGCInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := purgeEventsOlderThan(ctx, d, days); err != nil {
					slog.Error("events: GC sweep failed", "err", err)
				}
			}
		}
	}()
}

// retentionDays resolves the configured events retention. Shared with the trial
// sweep, which must know the retention to reason about its own idempotency
// guard (billing_trial_sweep.go).
func retentionDays() int {
	days := defaultEventsRetentionDays
	if v := os.Getenv("TELA_EVENTS_RETENTION_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			days = n
		} else {
			slog.Warn("events: ignoring invalid TELA_EVENTS_RETENTION_DAYS, using default", "value", v, "default_days", days)
		}
	}
	return days
}

// purgeEventsOlderThan deletes events whose created_at is older than the cutoff,
// computed in SQL into the same 'YYYY-MM-DD HH:MM:SS' UTC text the column stores
// so the comparison is a lexicographic TEXT compare.
func purgeEventsOlderThan(ctx context.Context, d *sql.DB, days int) error {
	if days <= 0 {
		return nil
	}
	// billing.* is exempt. The retention exists to bound high-volume types
	// (page.view, api.request); billing rows are single digits a month and are
	// the one series whose whole value is longitudinal — a signup in January and
	// the payment it becomes in June are one story, and a 180-day window cuts it
	// in half. They are also the rows nothing else can reconstruct: Polar knows
	// its own side, but the trial that was forfeited and the offer that was
	// viewed exist only here.
	_, err := d.ExecContext(ctx,
		`DELETE FROM events
		  WHERE created_at < to_char((now() AT TIME ZONE 'UTC') - make_interval(days => $1), 'YYYY-MM-DD HH24:MI:SS')
		    AND type NOT LIKE 'billing.%'`,
		days)
	return err
}
