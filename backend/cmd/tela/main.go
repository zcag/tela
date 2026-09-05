package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zcag/tela/backend/internal/api"
	"github.com/zcag/tela/backend/internal/auth"
	"github.com/zcag/tela/backend/internal/db"
	"github.com/zcag/tela/backend/internal/llm"
	"github.com/zcag/tela/backend/internal/rag"
	"github.com/zcag/tela/backend/internal/summarize"
)

// shutdownGrace is how long http.Server.Shutdown waits for in-flight requests
// to finish before forcing close. Generous enough to let a slow import or
// search response complete; short enough that `docker stop` doesn't escalate
// to SIGKILL (default 10s grace).
const shutdownGrace = 10 * time.Second

// initLogger installs the process-wide slog default: a text handler for
// human-readable dev logs, or JSON when TELA_LOG_FORMAT=json so prod logs flow
// into Loki/Grafana as structured records. Called first thing in main().
func initLogger() {
	var h slog.Handler
	if os.Getenv("TELA_LOG_FORMAT") == "json" {
		h = slog.NewJSONHandler(os.Stderr, nil)
	} else {
		h = slog.NewTextHandler(os.Stderr, nil)
	}
	slog.SetDefault(slog.New(h))
}

// fatal logs at Error level then exits non-zero. Replaces log.Fatalf while
// keeping its boot-fatal semantics (no panic/Goexit, so deferred Close calls
// in callers are intentionally skipped — same as the old log.Fatalf).
func fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}

func main() {
	initLogger()

	// `tela license …` (issue / verify) runs fully offline — it needs only the
	// offline signing key, never a database. Intercept before the DATABASE_URL
	// requirement so keys can be minted on an air-gapped ops box.
	if len(os.Args) > 1 && os.Args[1] == "license" {
		runLicense(os.Args[2:])
		return
	}

	addr := ":8080"
	if v := os.Getenv("TELA_ADDR"); v != "" {
		addr = v
	}

	dsn := os.Getenv("TELA_DATABASE_URL")
	if dsn == "" {
		fatal("TELA_DATABASE_URL is required (e.g. postgres://tela:pass@localhost:5432/tela?sslmode=disable)")
	}

	d, err := db.Open(dsn)
	if err != nil {
		fatal("open db", "err", err)
	}
	defer d.Close()

	if err := db.Migrate(context.Background(), d); err != nil {
		fatal("migrate db", "err", err)
	}
	slog.Info("db ready")

	// One-off CLI subcommands run after migrations, then exit (no server).
	// Headless parity for the ops runbook (docs/operations.md).
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "reindex-all":
			// Re-embed every page after an embedder model change (the chunk
			// hash folds in the model name). Add --force for a full re-embed
			// when the model name is unchanged but the embedder setup moved.
			runReindexAll(d, os.Args[2:])
			return
		case "summarize-all":
			// Generate (or backfill) the LLM summary for every page. Hash-skip
			// makes it resumable; --force regenerates everything.
			runSummarizeAll(d, os.Args[2:])
			return
		case "rag-eval":
			// Score retrieval against a golden set (recall@k / MRR / nDCG).
			runRAGEval(d, os.Args[2:])
			return
		case "ask-eval":
			// Score ANSWER completeness (the generation-recall layer rag-eval
			// can't see); splits each miss into generation-drop vs retrieval-gap.
			runAskEval(d, os.Args[2:])
			return
		case "create-admin":
			runCreateAdmin(d, os.Args[2:])
			return
		case "set-plan":
			runSetPlan(d, os.Args[2:])
			return
		case "grant":
			runGrant(d, os.Args[2:])
			return
		case "credits":
			runCredits(d, os.Args[2:])
			return
		case "rewrap-credentials":
			// Seal credentials still stored as plaintext (rows predating
			// internal/secretbox). Idempotent; safe to re-run.
			runRewrapCredentials(d)
			return
		case "list-users":
			runListUsers(d)
			return
		case "digest":
			runDigest(d, os.Args[2:])
			return
		default:
			fatal("unknown subcommand (known: reindex-all, summarize-all, rag-eval, ask-eval, create-admin, set-plan, grant, credits, list-users, digest, rewrap-credentials)", "subcommand", os.Args[1])
		}
	}

	bs, err := auth.BootstrapFromEnv(context.Background(), d)
	if err != nil {
		fatal("bootstrap admin", "err", err)
	}
	if bs.Created {
		slog.Info("bootstrap admin created from TELA_ADMIN_PASSWORD env", "username", bs.Username)
	}

	// Assign TELA_ADMIN_EMAIL to a pre-email-auth bootstrap admin (no-op once
	// set, or when the env is unset).
	auth.BackfillAdminEmailFromEnv(context.Background(), d)

	// Every user gets a private, one-member personal space as their default
	// writing home (docs/visibility-model.md). Backfills the bootstrap admin
	// and any pre-existing users; non-fatal so a hiccup never blocks boot.
	if err := api.EnsurePersonalSpacesForAll(context.Background(), d); err != nil {
		slog.Error("personal space backfill", "err", err)
	}

	// rootCtx is cancelled on SIGINT/SIGTERM. Threaded into StartAuditGC so
	// the ticker loop exits cleanly on shutdown; used as the trigger for
	// HTTP and AuditWriter teardown below.
	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logStartupConfig()

	handler, apiSrv := api.HandlerWithServer(d)

	// M16.A.2 API-key audit retention GC. Background goroutine sweeps
	// api_key_audit rows older than TELA_API_KEY_AUDIT_DAYS (default 30) every
	// 6h. Cancels with rootCtx so the ticker exits before AuditWriter.Close
	// runs and the process exits.
	auth.StartAuditGC(rootCtx, d)
	// Unified events-feed retention GC (TELA_EVENTS_RETENTION_DAYS, default 180d).
	api.StartEventsGC(rootCtx, d)
	// Trash retention sweep — OFF unless TELA_TRASH_RETENTION_DAYS is set.
	api.StartTrashGC(rootCtx, d)
	// Background AI-health prober: pings the embedder + chat model on a timer so
	// host-context's ai_available reflects real reachability and the "AI
	// unavailable" header appears automatically during an outage. No-op (cheap
	// "disabled" verdict, no network) when AI is unconfigured or admin-paused.
	apiSrv.StartAIHealthProbe(rootCtx)

	httpSrv := &http.Server{Addr: addr, Handler: handler}
	serverErr := make(chan error, 1)
	go func() { serverErr <- httpSrv.ListenAndServe() }()

	slog.Info("tela backend listening", "addr", addr)

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal("server failed", "err", err)
		}
	case <-rootCtx.Done():
		slog.Info("shutdown signal received, draining", "grace", shutdownGrace)
		// Order matters: HTTP shutdown FIRST so in-flight bearer requests
		// finish populating the audit buffer, THEN AuditWriter.Close so
		// those last rows make it to the DB before the worker stops.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			slog.Error("http shutdown", "err", err)
		}
		apiSrv.AuditWriter().Close()
	}
}

// logStartupConfig prints the effective operational config at boot so silent
// misconfigurations become visible: the cookie-Secure ↔ TELA_PUBLIC_BASE_URL
// coupling (https base URL behind plain HTTP drops the login cookie), the SMTP
// log-fallback (registration emails go to stdout, blocking multi-user signup),
// and the RAG embedder target.
func logStartupConfig() {
	base := os.Getenv("TELA_PUBLIC_BASE_URL")
	if base == "" {
		base = "(unset; defaulting to http://localhost:8780)"
	}
	slog.Info("config", "public_base_url", base, "cookie_secure", auth.CookieSecure())

	if os.Getenv("TELA_SMTP_HOST") == "" {
		slog.Warn("config: SMTP unset — verify/reset emails are LOGGED, not sent (including raw reset tokens). " +
			"Anyone with log access can reset any account. Set TELA_SMTP_* for production.")
	} else {
		slog.Info("config: SMTP enabled", "host", os.Getenv("TELA_SMTP_HOST"))
	}

	if rcfg := rag.ConfigFromEnv(); rcfg.EmbedURL == "" {
		slog.Warn("config: RAG/semantic search DISABLED (TELA_RAG_EMBED_URL unset). " +
			"Full-text search still works; set the URL to enable embeddings.")
	} else {
		slog.Info("config: RAG embedder", "url", rcfg.EmbedURL, "model", rcfg.EmbedModel, "dim", rcfg.Dim)
	}
}

// runReindexAll re-embeds every page in every space against the currently
// configured embedder, logging per-space progress (in the rag package), then
// returns. Synchronous; the embed calls dominate wall-clock. Pass --force to
// bypass the per-chunk vector cache and re-embed everything from scratch — the
// clean replacement for a manual TRUNCATE when the model name is unchanged but
// the embedder setup moved.
func runReindexAll(d *sql.DB, args []string) {
	force := false
	for _, a := range args {
		switch a {
		case "--force", "-f":
			force = true
		default:
			fatal("reindex-all: unknown flag (known: --force)", "flag", a)
		}
	}

	cfg := rag.ConfigFromEnv()
	if cfg.EmbedURL == "" {
		fatal("reindex-all: TELA_RAG_EMBED_URL is not set — nothing to embed against")
	}
	svc := rag.NewService(d, cfg)
	if !svc.Enabled() {
		fatal("reindex-all: embedder disabled")
	}

	sum, err := svc.ReindexAll(context.Background(), force)
	if err != nil {
		fatal("reindex-all", "err", err)
	}
	if sum.Failed > 0 {
		slog.Warn("reindex-all: completed with failures", "failed_pages", sum.Failed)
	}
}

// runSummarizeAll generates the LLM summary for every page in every space
// against the configured chat model, logging per-space progress (in the
// summarize package), then returns. Serial; the LLM calls dominate wall-clock.
// Resumable by virtue of the hash-skip; pass --force to regenerate everything
// (summary_lock pages stay untouched either way).
func runSummarizeAll(d *sql.DB, args []string) {
	force := false
	for _, a := range args {
		switch a {
		case "--force", "-f":
			force = true
		default:
			fatal("summarize-all: unknown flag (known: --force)", "flag", a)
		}
	}

	cfg := llm.ConfigFromEnv()
	if cfg.URL == "" {
		fatal("summarize-all: TELA_LLM_URL is not set — nothing to generate with")
	}
	svc := summarize.NewService(d, llm.NewService(cfg))
	if !svc.Enabled() {
		fatal("summarize-all: llm disabled")
	}

	sum, err := svc.SummarizeAll(context.Background(), force)
	if err != nil {
		fatal("summarize-all", "err", err)
	}
	if sum.Failed > 0 {
		slog.Warn("summarize-all: completed with failures", "failed_pages", sum.Failed)
	}
}

// runDigest drives the weekly-digest CLI:
//
//	tela digest preview <username|id> [--out file]   render one user's digest HTML
//	tela digest run [--dry-run]                       run the due-send job now
//	tela digest enable|disable <username|id>          set a user's frequency
func runDigest(d *sql.DB, args []string) {
	if len(args) == 0 {
		fatal("usage: tela digest preview <user> [--out f] | run [--dry-run] | enable|disable <user>")
	}
	srv := api.New(d)
	ctx := context.Background()
	switch args[0] {
	case "preview":
		if len(args) < 2 {
			fatal("usage: tela digest preview <username|id> [--out file]")
		}
		out := ""
		for i := 2; i < len(args); i++ {
			if args[i] == "--out" && i+1 < len(args) {
				out = args[i+1]
				i++
			}
		}
		html, err := srv.RenderDigestForUser(ctx, args[1])
		if err != nil {
			fatal("digest preview", "err", err)
		}
		if out != "" {
			if err := os.WriteFile(out, []byte(html), 0o644); err != nil {
				fatal("digest preview: write", "err", err)
			}
			slog.Info("digest preview written", "user", args[1], "file", out)
		} else {
			fmt.Print(html)
		}
	case "run":
		dry := false
		for _, a := range args[1:] {
			if a == "--dry-run" {
				dry = true
			}
		}
		n, err := srv.SendDueDigests(ctx, dry)
		if err != nil {
			fatal("digest run", "err", err)
		}
		slog.Info("digest run complete", "sent", n, "dry_run", dry)
	case "enable", "disable":
		if len(args) < 2 {
			fatal("usage: tela digest " + args[0] + " <username|id>")
		}
		freq := "weekly"
		if args[0] == "disable" {
			freq = "off"
		}
		res, err := d.ExecContext(ctx,
			`UPDATE users SET digest_frequency = $1 WHERE username = $2 OR CAST(id AS TEXT) = $2`,
			freq, args[1])
		if err != nil {
			fatal("digest "+args[0], "err", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			fatal("digest " + args[0] + ": no such user")
		}
		slog.Info("digest frequency set", "user", args[1], "frequency", freq)
	default:
		fatal("digest: unknown action (preview|run|enable|disable)", "action", args[0])
	}
}

// runRAGEval scores retrieval against a golden set of (query → expected page)
// cases and prints recall@k / MRR / nDCG. The harness that makes every future
// retrieval change measurable instead of vibes-based.
//
//	tela rag-eval --set golden.json [--k 10] [--mode hybrid] [--user <id>]
//
// The golden set is a JSON array of {query, expect_pages?, expect_substr?,
// space_id?}. Scoring runs through the same access-scoped Search users hit, so
// --user must be able to read the spaces under test (defaults to the lowest user
// id — typically the bootstrap admin).
func runRAGEval(d *sql.DB, args []string) {
	setPath, mode := "", "hybrid"
	k, userID := 10, int64(0)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--set":
			i++
			if i < len(args) {
				setPath = args[i]
			}
		case "--k":
			i++
			if i < len(args) {
				k, _ = strconv.Atoi(args[i])
			}
		case "--mode":
			i++
			if i < len(args) {
				mode = args[i]
			}
		case "--user":
			i++
			if i < len(args) {
				userID, _ = strconv.ParseInt(args[i], 10, 64)
			}
		default:
			fatal("rag-eval: unknown flag (known: --set, --k, --mode, --user)", "flag", args[i])
		}
	}
	if setPath == "" {
		fatal("rag-eval: --set <golden.json> is required")
	}

	cfg := rag.ConfigFromEnv()
	svc := rag.NewService(d, cfg)
	if !svc.Enabled() {
		fatal("rag-eval: embedder not configured (set TELA_RAG_EMBED_URL)")
	}

	raw, err := os.ReadFile(setPath)
	if err != nil {
		fatal("rag-eval: read golden set", "err", err)
	}
	var cases []rag.EvalCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		fatal("rag-eval: parse golden set", "err", err)
	}

	ctx := context.Background()
	if userID == 0 {
		if err := d.QueryRowContext(ctx, `SELECT id FROM users ORDER BY id LIMIT 1`).Scan(&userID); err != nil {
			fatal("rag-eval: no user to evaluate as (pass --user)", "err", err)
		}
	}

	res, err := svc.Evaluate(ctx, userID, cases, k, mode)
	if err != nil {
		fatal("rag-eval", "err", err)
	}

	fmt.Printf("\nRAG eval — %d cases, mode=%s, k=%d, user=%d, model=%s\n",
		res.Cases, res.Mode, res.K, userID, svc.EmbedModel())
	fmt.Printf("  recall@%d = %.3f    MRR = %.3f    nDCG@%d = %.3f\n\n", res.K, res.RecallAtK, res.MRR, res.K, res.NDCG)
	fmt.Printf("  %-5s %-5s %s\n", "hit", "rank", "query")
	for _, c := range res.PerCase {
		mark, rank := "✗", "-"
		if c.Hit {
			mark = "✓"
			rank = strconv.Itoa(c.FirstRank)
		}
		q := c.Query
		if len(q) > 64 {
			q = q[:61] + "…"
		}
		fmt.Printf("  %-5s %-5s %s\n", mark, rank, q)
	}
	fmt.Println()
}

// runAskEval scores ANSWER completeness for enumeration questions — the
// generation-recall layer rag-eval is blind to. It runs the real ask pipeline
// (retrieve → ground → LLM) per case and reports, for each expected item,
// whether it was covered, dropped by the model (in grounding but absent from the
// answer), or never retrieved.
//
//	tela ask-eval --set golden.json [--user <id>] [--answers]
//
// The golden set is a JSON array of {question, expect_all[]?, expect_none[]?,
// space_id?} — expect_none lists terms that must be ABSENT (the cross-project leak
// guard, used with space_id unset). Needs a live embedder AND LLM (so it
// exercises the same model the deployment serves).
func runAskEval(d *sql.DB, args []string) {
	setPath, userID, showAnswers := "", int64(0), false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--set":
			i++
			if i < len(args) {
				setPath = args[i]
			}
		case "--user":
			i++
			if i < len(args) {
				userID, _ = strconv.ParseInt(args[i], 10, 64)
			}
		case "--answers":
			showAnswers = true
		default:
			fatal("ask-eval: unknown flag (known: --set, --user, --answers)", "flag", args[i])
		}
	}
	if setPath == "" {
		fatal("ask-eval: --set <golden.json> is required")
	}

	raw, err := os.ReadFile(setPath)
	if err != nil {
		fatal("ask-eval: read golden set", "err", err)
	}
	var cases []api.AskCompletenessCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		fatal("ask-eval: parse golden set", "err", err)
	}

	srv := api.New(d)
	ctx := context.Background()
	if userID == 0 {
		if err := d.QueryRowContext(ctx, `SELECT id FROM users ORDER BY id LIMIT 1`).Scan(&userID); err != nil {
			fatal("ask-eval: no user to evaluate as (pass --user)", "err", err)
		}
	}

	scores, err := srv.EvalAskCompleteness(ctx, userID, cases, showAnswers)
	if err != nil {
		fatal("ask-eval", "err", err)
	}

	var sumCov float64
	var drops, gaps, leaks int
	for _, sc := range scores {
		sumCov += sc.Coverage
		drops += len(sc.GenerationDrops)
		gaps += len(sc.RetrievalGaps)
		leaks += len(sc.Leaks)
	}
	mean := 0.0
	if len(scores) > 0 {
		mean = sumCov / float64(len(scores))
	}
	fmt.Printf("\nAsk-completeness eval — %d cases, user=%d\n", len(scores), userID)
	fmt.Printf("  mean coverage = %.3f    generation drops = %d    retrieval gaps = %d    leaks = %d\n\n", mean, drops, gaps, leaks)
	for _, sc := range scores {
		fmt.Printf("  [%3.0f%%] %s\n", sc.Coverage*100, sc.Question)
		if len(sc.GenerationDrops) > 0 {
			fmt.Printf("        dropped by model (was in grounding): %s\n", strings.Join(sc.GenerationDrops, ", "))
		}
		if len(sc.RetrievalGaps) > 0 {
			fmt.Printf("        never retrieved: %s\n", strings.Join(sc.RetrievalGaps, ", "))
		}
		if len(sc.Leaks) > 0 {
			fmt.Printf("        LEAKED from another scope (should be absent): %s\n", strings.Join(sc.Leaks, ", "))
		}
		if showAnswers {
			fmt.Printf("        answer: %s\n", strings.ReplaceAll(strings.TrimSpace(sc.Answer), "\n", "\n        "))
		}
	}
	fmt.Println()
}
