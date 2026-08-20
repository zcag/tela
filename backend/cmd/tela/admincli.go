package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/zcag/tela/backend/internal/api"
	"github.com/zcag/tela/backend/internal/auth"
)

// Operational CLI subcommands, dispatched from main after migrations run. They
// give a self-hoster headless parity for the common admin tasks the operations
// runbook references — without needing the running app or hand-written SQL.
// Each prints usage and exits non-zero on misuse.

// runCreateAdmin: `tela create-admin <username> <email> <password>` — create a
// (pre-verified) instance admin even when the users table is already populated
// (BootstrapAdmin only fires on an empty table). The recovery path when admin
// access is lost.
func runCreateAdmin(d *sql.DB, args []string) {
	if len(args) != 3 {
		fatal("usage: tela create-admin <username> <email> <password>")
	}
	username, email, password := args[0], args[1], args[2]
	hash, err := auth.HashPassword(password)
	if err != nil {
		fatal("create-admin: hash password", "err", err)
	}
	ctx := context.Background()
	var id int64
	err = d.QueryRowContext(ctx, `
		INSERT INTO users (username, email, email_verified_at, password_hash, is_instance_admin, is_active)
		VALUES ($1, $2, tela_now(), $3, 1, 1) RETURNING id`, username, email, hash).Scan(&id)
	if err != nil {
		fatal("create-admin", "err", err)
	}
	if err := api.EnsurePersonalSpacesForAll(ctx, d); err != nil {
		slog.Error("create-admin: personal space backfill", "err", err)
	}
	slog.Info("create-admin: created instance admin", "username", username, "id", id)
}

// runSetPlan: `tela set-plan <user|org> <id> <plan_key>` — assign a plan tier.
// Validates the plan's account_kind matches the target kind.
func runSetPlan(d *sql.DB, args []string) {
	if len(args) != 3 {
		fatal("usage: tela set-plan <user|org> <id> <plan_key>")
	}
	kind, idStr, planKey := args[0], args[1], args[2]
	if kind != "user" && kind != "org" {
		fatal("set-plan: kind must be 'user' or 'org'")
	}
	ctx := context.Background()
	var planKind string
	if err := d.QueryRowContext(ctx, `SELECT account_kind FROM plans WHERE key = $1`, planKey).Scan(&planKind); err != nil {
		fatal("set-plan: unknown plan_key", "plan_key", planKey)
	}
	if planKind != kind {
		fatal("set-plan: plan is for a different account kind", "plan_key", planKey, "plan_kind", planKind, "target_kind", kind)
	}
	table, set := "users", "plan_key = $1, updated_at = tela_now()"
	if kind == "org" {
		table = "orgs"
	} else {
		// Clear any active trial so the assignment takes effect NOW — otherwise
		// the trial CASE in planFor keeps overriding it for up to ~37 days and the
		// operator sees the command do nothing. Mirrors the admin HTTP set-plan
		// (usage.go) and the billing webhook, which both already clear it.
		set += ", trial_plan_key = NULL, trial_ends_at = NULL"
	}
	res, err := d.ExecContext(ctx,
		`UPDATE `+table+` SET `+set+` WHERE id = $2`, planKey, idStr)
	if err != nil {
		fatal("set-plan", "err", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		fatal("set-plan: no matching row", "kind", kind, "id", idStr)
	}
	slog.Info("set-plan", "kind", kind, "id", idStr, "plan_key", planKey)
}

// creditMetrics are the plan columns a top-up may raise. Listed explicitly so a
// typo is rejected at the CLI rather than becoming an inert row nobody notices —
// applyCredits ignores unknown metrics by design, which is safe but silent.
var creditMetrics = map[string]bool{
	"max_atlas_minutes_per_month": true,
	"max_atlas_runs_per_month":    true,
	"max_atlas_files_per_run":     true,
	"max_embed_tokens_per_month":  true,
	"max_llm_calls_per_month":     true,
	"max_atlas_sources":           true,
	"max_spaces":                  true,
	"max_pages_per_space":         true,
	"max_storage_bytes":           true,
	"max_members":                 true,
}

// runGrant: `tela grant <user|org> <id> <metric> <amount> <reason> [period]`
// — add a per-account quota top-up.
//
// ADDITIVE to the account's tier cap, and scoped to one calendar month unless a
// period is given (” would mean every period; pass "always" for that). A reason
// is REQUIRED: an exception nobody can explain later is indistinguishable from a
// mistake, and this is the only record that it was deliberate.
func runGrant(d *sql.DB, args []string) {
	if len(args) < 5 {
		fatal("usage: tela grant <user|org> <id> <metric> <amount> <reason> [YYYY-MM|always]  (default period = current month)")
	}
	kind, idStr, metric, amountStr, reason := args[0], args[1], args[2], args[3], args[4]
	if kind != "user" && kind != "org" {
		fatal("grant: kind must be 'user' or 'org'")
	}
	if !creditMetrics[metric] {
		keys := make([]string, 0, len(creditMetrics))
		for k := range creditMetrics {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fatal("grant: unknown metric", "metric", metric, "known", strings.Join(keys, ", "))
	}
	amount, err := strconv.ParseInt(amountStr, 10, 64)
	if err != nil {
		fatal("grant: amount must be an integer", "amount", amountStr)
	}
	period := time.Now().UTC().Format("2006-01")
	if len(args) >= 6 {
		if args[5] == "always" {
			period = ""
		} else {
			if _, err := time.Parse("2006-01", args[5]); err != nil {
				fatal("grant: period must be YYYY-MM or 'always'", "period", args[5])
			}
			period = args[5]
		}
	}
	ctx := context.Background()

	// Verify the target exists — a grant against a typo'd id is silently inert.
	table := "users"
	if kind == "org" {
		table = "orgs"
	}
	var exists int
	if err := d.QueryRowContext(ctx, `SELECT 1 FROM `+table+` WHERE id = $1`, idStr).Scan(&exists); err != nil {
		fatal("grant: no such account", "kind", kind, "id", idStr)
	}
	if _, err := d.ExecContext(ctx,
		`INSERT INTO account_credits (account_kind, account_id, period, metric, amount, reason)
		 VALUES ($1,$2,$3,$4,$5,$6)`, kind, idStr, period, metric, amount, reason); err != nil {
		fatal("grant", "err", err)
	}
	shown := period
	if shown == "" {
		shown = "always"
	}
	slog.Info("grant: top-up recorded", "kind", kind, "id", idStr, "metric", metric,
		"amount", amount, "period", shown, "reason", reason)
}

// runCredits: `tela credits [<user|org> <id>]` — list top-ups, newest first.
func runCredits(d *sql.DB, args []string) {
	ctx := context.Background()
	q := `SELECT id, account_kind, account_id, period, metric, amount, reason, created_at
	        FROM account_credits`
	var rows *sql.Rows
	var err error
	if len(args) == 2 {
		q += ` WHERE account_kind = $1 AND account_id = $2`
		rows, err = d.QueryContext(ctx, q+` ORDER BY id DESC`, args[0], args[1])
	} else {
		rows, err = d.QueryContext(ctx, q+` ORDER BY id DESC`)
	}
	if err != nil {
		fatal("credits", "err", err)
	}
	defer rows.Close()
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tACCOUNT\tPERIOD\tMETRIC\tAMOUNT\tREASON\tCREATED")
	for rows.Next() {
		var (
			id, acctID, amount           int64
			kind, period, metric, reason string
			created                      string
		)
		if err := rows.Scan(&id, &kind, &acctID, &period, &metric, &amount, &reason, &created); err != nil {
			fatal("credits: scan", "err", err)
		}
		if period == "" {
			period = "always"
		}
		fmt.Fprintf(tw, "%d\t%s:%d\t%s\t%s\t%d\t%s\t%s\n", id, kind, acctID, period, metric, amount, reason, created)
	}
	tw.Flush()
}

// runListUsers: `tela list-users` — id, username, email, admin/active flags, plan.
func runListUsers(d *sql.DB) {
	ctx := context.Background()
	rows, err := d.QueryContext(ctx, `
		SELECT id, username, COALESCE(email, ''), is_instance_admin, is_active, plan_key
		FROM users ORDER BY username`)
	if err != nil {
		fatal("list-users", "err", err)
	}
	defer rows.Close()
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tUSERNAME\tEMAIL\tADMIN\tACTIVE\tPLAN")
	for rows.Next() {
		var (
			id            int64
			username      string
			email         string
			admin, active int
			plan          string
		)
		if err := rows.Scan(&id, &username, &email, &admin, &active, &plan); err != nil {
			fatal("list-users: scan", "err", err)
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%t\t%t\t%s\n", id, username, email, admin == 1, active == 1, plan)
	}
	if err := rows.Err(); err != nil {
		fatal("list-users", "err", err)
	}
	tw.Flush()
}
