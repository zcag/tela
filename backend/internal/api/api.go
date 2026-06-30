package api

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"sync/atomic"

	"github.com/zcag/tela/backend/internal/agreement"
	"github.com/zcag/tela/backend/internal/auth"
	"github.com/zcag/tela/backend/internal/billing"
	"github.com/zcag/tela/backend/internal/ee"
	"github.com/zcag/tela/backend/internal/llm"
	"github.com/zcag/tela/backend/internal/mailer"
	"github.com/zcag/tela/backend/internal/rag"
	"github.com/zcag/tela/backend/internal/settings"
	"github.com/zcag/tela/backend/internal/summarize"
)

// Server bundles dependencies shared across HTTP handlers.
type Server struct {
	DB *sql.DB

	// settings is the instance-level runtime config store (instance_settings
	// table, cached). It backs the admin settings API, persisted secrets, and
	// (later) per-plan feature flags + the cloud-connect token. Never nil —
	// built in New().
	settings *settings.Store

	rooms        *roomRegistry
	shareSecret  []byte
	shareLimiter *shareRateLimiter

	// Mailer sends transactional email (verify / reset). Defaults to the
	// env-selected driver (SMTP, or a log fallback when unconfigured) so it is
	// never nil; tests overwrite it with a capturing fake after New().
	Mailer mailer.Mailer

	// authLimiter throttles the unauthenticated, email-sending endpoints
	// (register / resend / forgot-password) per client IP so the relay can't
	// be turned into a mail bomb.
	authLimiter *authRateLimiter

	// loginLimiter throttles POST /api/auth/login per client IP to blunt
	// credential-stuffing and brute-force attacks. Separate from authLimiter
	// so the two budgets are independent (a typo-locked login doesn't exhaust
	// the password-reset budget, and vice-versa).
	loginLimiter *authRateLimiter

	// unfurlLimiter throttles GET /api/unfurl per client IP. The endpoint is
	// session-gated but still caps outbound HTTP relaying at scale.
	unfurlLimiter *authRateLimiter

	// cloudLimiter throttles the managed-compute proxies (cloud embed/chat,
	// ask-your-docs) per ACCOUNT so a single entitled PAT can't hammer paid
	// LLM/embedder compute into an unbounded bill or DoS the shared clients.
	cloudLimiter *authRateLimiter

	// clientErrorLimiter throttles the browser error-report beacon
	// (/api/client-errors) per user so a tab stuck in an error loop can't flood
	// the events table. Reuses the same sliding-window machinery.
	clientErrorLimiter *authRateLimiter

	// davDeletes is the WebDAV mass-delete brake (sync §6): a per-(api_key,space)
	// sliding window that refuses a sync client's delete once an anomalous
	// fraction of the space has already vanished, so a runaway client can't wipe a
	// space. Pairs with the per-page cursor gate in the davFS RemoveAll path.
	davDeletes *davDeleteGuard

	// auditWriter buffers bearer-authed audit-log writes. M16.A.2: every
	// request that resolved to a valid api_keys row is recorded here
	// (method, path, status_code, ts). Lives on Server because the
	// /api/api_keys/{id}/audit handler queries the rows it produces, and
	// because tests need a stable handle to call Flush() between submit and
	// assert. Owned by New() so it always exists — never nil — which keeps
	// the dependency injection trivial.
	auditWriter *auth.AuditWriter

	// rag is the semantic-retrieval service (chunk index + hybrid search).
	// Constructed from env (TELA_RAG_EMBED_URL); disabled — but never nil —
	// when unconfigured, so handlers can `if !s.rag.Enabled()` → 503. Tests
	// inject a fake embedder by overwriting this field.
	rag *rag.Service

	// llm is the chat-completion service (OpenAI-compatible / Ollama). Sibling
	// to rag: constructed from env (TELA_LLM_URL), disabled — but never nil —
	// when unconfigured, so handlers can `if !s.llm.Enabled()` → 503. Consumed
	// in-process by /api/rag/ask and over HTTP via the managed cloud proxy.
	// Tests inject a fake completer by overwriting this field.
	llm *llm.Service

	// summarize is the auto-summary service (LLM-generated props.summary +
	// page_summaries bookkeeping). Built on llm, so it shares its enablement:
	// disabled — but never nil — when TELA_LLM_URL is unset. Page writes call
	// s.summarize.Queue alongside s.rag.QueueReindex. Tests inject a service
	// with a fake completer by overwriting this field.
	summarize *summarize.Service

	// agreement is the corroboration/contradiction service for the epistemic
	// trust strip (page_agreement bookkeeping). Built on llm + rag, so it shares
	// their enablement: disabled — but never nil — unless BOTH a chat model and
	// the embedder are configured. Page writes call s.agreement.Queue.
	agreement *agreement.Service

	// atlas owns documentation-generation runs (source → coverage-audited pages
	// in a managed space). Reuses s.rag (embed + reindex) and the instance LLM;
	// disabled at the edges when those are unconfigured. Never nil — built in New.
	atlas *atlasManager

	// oauth is the MCP endpoint's OAuth 2.1 Resource-Server config (WorkOS JWT
	// acceptance + Protected Resource Metadata). nil = disabled (PAT-only),
	// unless TELA_WORKOS_ISSUER is set. Tests inject a configured one.
	oauth *mcpOAuth

	// sso is the federated-login registry (social providers from TELA_SSO_*).
	// Never nil — an unconfigured instance just has an empty provider map, so
	// the login screen shows no social buttons. Per-org OIDC connections aren't
	// held here; they're built per-request from the org_sso row.
	sso *ssoRegistry

	// deckWarm pre-builds a deck's interactive SPA after its source changes so the
	// next "Present" opens instantly. Never nil — built in New(). Best-effort and
	// correctness-independent (renders are content-keyed); see deck_warm.go.
	deckWarm *deckWarmer

	// aiHealth caches the background prober's latest reachability verdict for the
	// embedder + chat model, so host-context's `ai_available` reflects whether AI
	// is actually serving (not just configured). Zero value = "not yet probed",
	// which aiHealthy() treats optimistically. The prober is started from the real
	// entrypoint via StartAIHealthProbe; tests never run it. See ai_health.go.
	aiHealth aiHealthState

	// seedWelcome controls whether POST /api/spaces seeds a starter "Welcome"
	// page into a freshly created space (so a new team doesn't land in an empty
	// void). On by default; the test package disables it via
	// TELA_DISABLE_WELCOME_SEED so space-creation tests keep asserting on exact
	// page sets.
	seedWelcome bool

	// billing is the Polar client for self-serve subscriptions (checkout, the
	// customer portal, webhook verification). Never nil — built from env in New();
	// disabled (handlers 503) when TELA_POLAR_TOKEN/SECRET are unset, mirroring
	// rag/llm. The webhook reconciler (billing.go) maps Polar events onto plan_key.
	billing *billing.Client

	// license is the self-host Enterprise entitlement (a verified offline key) —
	// nil until a valid key is installed. Read on every paid-feature request by
	// entitled() and mutated at runtime by the admin License API, so it's atomic.
	license atomic.Pointer[ee.License]

	// managedCloud marks THIS instance as the managed cloud (Polar billing on, or
	// TELA_CLOUD=1). On the cloud the account's plan flag is an authoritative
	// entitlement; on self-host it is NOT (plan_key is freely admin-assignable),
	// so there a license key is the only unlock for ee features. See entitled().
	managedCloud bool

	// askJobs holds detached ask-generation jobs so a streamed answer survives a
	// dropped connection (backgrounded mobile Safari): the LLM runs in a goroutine
	// appending to a replayable event log, the SSE handler tails it, and a
	// reconnect re-attaches by id. Never nil — built in New(). See ask_job.go.
	askJobs *askStore
}

func New(db *sql.DB) *Server {
	ctx := context.Background()
	st, err := settings.New(ctx, db)
	if err != nil {
		// Boot-fatal: can't run without instance settings.
		slog.Error("settings: load instance_settings", "err", err)
		os.Exit(1)
	}
	// Resolve the api-key HMAC secret through the store (env → persisted →
	// generated-and-persisted) so it survives restarts. Must run before any
	// bearer request; New() is called once at boot.
	if err := auth.InitAPIKeySecret(ctx, st); err != nil {
		// Boot-fatal: a stable api-key secret is required.
		slog.Error("settings: init api-key secret", "err", err)
		os.Exit(1)
	}
	s := &Server{
		DB:                 db,
		settings:           st,
		rooms:              newRoomRegistry(),
		shareSecret:        resolveShareSecret(ctx, st),
		shareLimiter:       newShareRateLimiter(),
		auditWriter:        auth.NewAuditWriter(db),
		Mailer:             mailer.FromEnv(),
		authLimiter:        newAuthRateLimiter(authRateWindow, authRateLimit),
		loginLimiter:       newAuthRateLimiter(loginRateWindow, loginRateLimit),
		unfurlLimiter:      newAuthRateLimiter(unfurlRateWindow, unfurlRateLimit),
		cloudLimiter:       newAuthRateLimiter(cloudRateWindow, cloudRateLimit),
		clientErrorLimiter: newAuthRateLimiter(clientErrorRateWindow, clientErrorRateLimit),
		davDeletes:         newDavDeleteGuard(),
		rag:                rag.NewService(db, rag.ConfigFromEnv()),
		llm:                llm.NewService(llm.ConfigFromEnv()),
		oauth:              loadMCPOAuth(context.Background()),
		sso:                loadSSOProviders(context.Background()),
		seedWelcome:        os.Getenv("TELA_DISABLE_WELCOME_SEED") == "",
		billing:            billing.New(billing.ConfigFromEnv()),
		askJobs:            newAskStore(),
	}
	// AI usage metering: capture token estimates at the service chokepoints so
	// every chat completion + embedding lands in ai_usage (image gen is metered
	// at its MCP tool). One recorder, set before any AI runs. See ai_usage.go.
	s.llm.SetUsageRecorder(func(model string, in, out int) {
		s.recordAIUsage("chat", model, in, out, 0)
	})
	s.llm.SetSpillRecorder(func() { aiForegroundSpills.Inc() })
	s.rag.SetUsageRecorder(func(model string, in int) {
		s.recordAIUsage("embed", model, in, 0, 0)
	})

	// Managed cloud iff Polar billing is configured or TELA_CLOUD=1 — then plan
	// flags are authoritative entitlements; otherwise (self-host) only a license
	// key unlocks ee features. Must be set before any entitled() call.
	s.managedCloud = os.Getenv("TELA_CLOUD") == "1" || s.billing.Enabled()

	// Resolve the self-host Enterprise license (env → persisted) into s.license,
	// so entitled() can grant ee features without a managed-cloud plan. No-op /
	// nil on the cloud + unlicensed self-host. See license.go.
	s.loadLicense(ctx)
	s.warnSelfHostSSO(ctx)

	// Guard against a Polar reprice silently diverging from the plans table (we'd
	// charge a price the UI never shows). Background — a few API calls, advisory
	// (logs loud on mismatch), never blocks or fails boot. See billing_priceguard.go.
	go s.verifyBillingPrices(context.Background())

	// Built after the literal so it can share the llm handle (same enablement).
	s.summarize = summarize.NewService(db, s.llm)
	// Agreement shares llm + rag (needs both: a model to judge, embeddings to
	// find neighbours). Page writes call s.agreement.Queue alongside summarize.
	s.agreement = agreement.NewService(db, s.llm, s.rag)
	// Pre-warms a deck's Present build after any source change (all write paths).
	s.deckWarm = newDeckWarmer(s)
	// Sweep stale share-rate-limit buckets every shareRateWindow so the
	// limiter map cannot grow unbounded under adversarial load. Tied to
	// context.Background() — the goroutine outlives non-graceful tests, which
	// is fine: each test process is short-lived.
	go s.shareLimiter.sweepLoop(context.Background())
	go s.authLimiter.sweepLoop(context.Background())
	go s.loginLimiter.sweepLoop(context.Background())
	go s.unfurlLimiter.sweepLoop(context.Background())
	go s.cloudLimiter.sweepLoop(context.Background())
	go s.clientErrorLimiter.sweepLoop(context.Background())
	go s.davDeletes.sweepLoop(context.Background())
	// Admin AI kill-switch (ai.disabled): pause EVERY background AI worker so a
	// maintenance window on the AI backend isn't hammered by indexing, summaries,
	// or agreement. Each worker leaves its queue intact and resumes (+ stale-sweep
	// backfills) once it clears.
	aiPaused := func() bool {
		v, _ := s.settings.Get("ai.disabled")
		return v == "1"
	}
	// Background auto-reindex worker (no-op when the embedder is unconfigured).
	// Page writes call s.rag.QueueReindex; this drains the debounced queue.
	s.rag.SetPaused(aiPaused)
	s.rag.StartAutoReindex(context.Background())
	// Background auto-summarize worker, the generation sibling (no-op when the
	// LLM is unconfigured). Page writes call s.summarize.Queue.
	s.summarize.SetPaused(aiPaused)
	s.summarize.Start(context.Background())
	// Background agreement worker (no-op unless both llm + embedder are on).
	s.agreement.SetPaused(aiPaused)
	s.agreement.Start(context.Background())
	// Documentation-generation manager (atlas): resume any runs a previous
	// process left mid-flight, then it's ready to drive new runs on demand.
	s.atlas = newAtlasManager(s)
	// Durable run queue: the dispatcher keeps ≤ maxRuns executing, claiming pending
	// runs from the DB (so a restart never strands a queued run). Start it before
	// ResumeDangling so recovered + queued runs are scheduled together.
	go s.atlas.runDispatcher(context.Background())
	s.atlas.ResumeDangling(context.Background())
	// Freshness scheduler: 1-minute poll firing change-gated delta re-ingests for
	// auto_update sources whose cadence has elapsed (no-op while AI is off/paused).
	s.atlas.startScheduler(context.Background(), aiPaused)
	return s
}

// AuditWriter exposes the server's audit-log sink so tests can call Flush()
// between a bearer-authed request and a "did the audit row land?" assertion.
// Production code never needs this — Middleware Submits directly.
func (s *Server) AuditWriter() *auth.AuditWriter {
	return s.auditWriter
}
