# Operating a tela instance

Day-2 runbook for a self-hosted tela. For first-time setup see
[`self-hosting.md`](self-hosting.md).

All commands assume the repo root and the Compose stack from `make up`.
`COMPOSE = docker compose -f deploy/docker-compose.yml`.

## Health & version

- `GET /api/health` → `{status, db}`, 200 when the DB is reachable, 503 otherwise.
- `GET /api/version` → `{version, commit, built_at}` (git metadata stamped at build).

```bash
curl -s localhost:8780/api/health
curl -s localhost:8780/api/version
```

The backend logs its effective config (`config: …`) at boot — base URL, cookie
`Secure`, SMTP mode, RAG target. Tail with `make logs`.

## Admin & users

The first admin is created one of two ways on an empty users table: from
`TELA_ADMIN_*` at boot when `TELA_ADMIN_PASSWORD` is set, or — when no admin env
is configured — through the web **setup wizard** at `/setup` (a fresh instance
redirects there automatically; the form creates a pre-verified instance admin
and signs in). After that, instance-admins manage users and plans in the app
under
**Settings → Users** (create users, reset passwords, toggle active/admin, assign
plan tiers) and configure the instance under **Settings** (admin tabs).

Instance settings (runtime config) are also available via the admin API:

```bash
# (authenticated as an instance admin)
curl -s localhost:8780/api/admin/settings
curl -s -X PATCH localhost:8780/api/admin/settings \
  -H 'content-type: application/json' \
  -d '{"settings":{"registration_open":"false"}}'
```

Secret values (HMAC keys, tokens) are never returned and can't be set this way —
they're managed via the environment / first-boot persistence.

### CLI subcommands

For headless operation (no app UI needed), the backend binary has ops
subcommands; run them in the container:

```bash
$COMPOSE exec backend /tela create-admin <username> <email> <password>  # make an instance admin
$COMPOSE exec backend /tela set-plan <user|org> <id> <plan_key>          # assign a plan tier
$COMPOSE exec backend /tela list-users                                   # id/username/email/admin/active/plan
$COMPOSE exec backend /tela reindex-all [--force]                        # re-embed (—force = full, ignore cache)
$COMPOSE exec backend /tela rag-eval --set golden.json                   # score retrieval (recall@k/MRR/nDCG)
```

### Recovering admin access

The env / wizard bootstrap only fires when the users table is empty. For an
existing instance where admin access is lost, mint a fresh admin directly:

```bash
$COMPOSE exec backend /tela create-admin recovery you@example.com 'a-strong-password'
```

## Semantic search reindex

After changing the embedder model (the chunk hash folds in the model name, so a
new model needs a full re-embed):

```bash
$COMPOSE exec backend /tela reindex-all
```

Runs synchronously to completion, logging per-space progress (and a `failed`
count — un-embeddable pages are skipped, not fatal). Requires `TELA_RAG_EMBED_URL`.

Add `--force` to bypass the per-chunk vector cache and re-embed **everything** —
the clean way to refresh when the model *name* is unchanged but the embedder
setup moved (replaces a manual `TRUNCATE page_chunks`). Normally not needed: the
index **self-heals** (failed reindexes retry with backoff; a background sweep
re-queues stale/unindexed pages and logs an `rag: index health` line each cycle),
so after an embedder outage the backlog clears on its own.

To measure retrieval quality against a golden set (see [`rag.md`](rag.md)):

```bash
$COMPOSE exec backend /tela rag-eval --set golden.json --k 10 --mode hybrid
```

## Backups

See [`self-hosting.md`](self-hosting.md#backups). `make backup` / `make restore
FILE=...`. Schedule `make backup` via cron for production.

## Resetting the database (destructive)

`make reset-prod-db FORCE=1` drops the Postgres volume and re-migrates from
scratch — **all data is lost**. Only for disposable/dev instances.

## Logs

```bash
make logs                              # all services, follow
$COMPOSE logs -f --tail=100 backend    # one service
```

Each request emits a structured access-log line (`http method=… path=… status=…
dur_ms=…` via `slog`; the `/api/health` probe is skipped). `/api/health` also
reports `rag: enabled|disabled`.

## AI usage & cost

Every AI call is metered into the `ai_usage` log (migration `0044`) — one row per
**chat completion**, **embedding**, and **image generation**, captured at the
service chokepoints (`internal/llm` records chat, `internal/rag` wraps the
embedder, the deck image-gen tool records images) via a recorder injected in
`api.New`. **Token counts are length-based estimates** (≈ chars/4, `llm.EstimateTokens`)
— good for budgeting, not invoicing; images record a `units` count, not tokens.

`GET /api/admin/ai-usage` (instance-admin) returns weekly token totals + a
per-model breakdown over the last 12 weeks; it surfaces in **Settings → Insights**
under *AI usage*. Cost estimation is then `tokens × a per-provider price table` —
the same recorded tokens can be priced against any provider (Anthropic / OpenAI /
self-hosted-$0). That price table + the $ view is the next layer; the capture
here is the prerequisite, so it only reflects usage **from when it shipped
onward**.

## AI reliability & failover

tela's AI is a set of external endpoints — the chat model (`TELA_LLM_URL`), the
embedder (`TELA_RAG_EMBED_URL`), and image generation. Each is a single endpoint
by default: if it clogs (rate-limited, slow, down), the dependent features
degrade. Two layers make this robust and visible.

**Relief proxy (chat failover chain).** A profile-gated
[LiteLLM](https://docs.litellm.ai) `litellm` service (in both compose files;
`COMPOSE_PROFILES=relief`, or `make up-relief` for the standalone stack) fronts
chat as an **ordered failover chain** `L1 → L2 → L3`. Its config is **rendered
from env** by `deploy/litellm/gen-config.py` at start: each layer
(`TELA_LLM_{1,2,3}_URL/MODEL/KEY/PROVIDER`) is its own model group, wired with
ordered `fallbacks`. A request hits L1; when L1 is **erroring / cooled-down /
times out**, LiteLLM cascades to L2, then L3 — so a layer going down never cuts
off service. tela calls the chain by L1's model name, so set `TELA_LLM_MODEL =
TELA_LLM_1_MODEL` (and point `TELA_LLM_URL` at `http://litellm:4000/v1`).

**Overload spill is separate, and lives in the backend.** LiteLLM `fallbacks` are
**failure-based, not load-based** — they trigger when a layer errors/cools down,
*not* when L1 is healthy but merely **saturated**. So "L1 **down**" and "L1
**overloaded**" are two different spills handled in two different places:

- **Down → LiteLLM.** The chain's `fallbacks` cascade L1 → L2 → L3 on error.
- **Overloaded → the backend's foreground gate.** `internal/llm` caps concurrent
  **foreground** (ask/assist) completions at `TELA_LLM_MAX_INFLIGHT` (default
  **20**, sized to a healthy local-model batch). The N+1th waits
  `TELA_LLM_OVERFLOW_WAIT_MS` (default **12s**) for a slot; if none frees — genuine
  sustained overload — it spills *that request* to `TELA_LLM_OVERFLOW_MODEL`, a
  stable `tela-chat-overflow` alias `gen-config.py` auto-registers (→ L2, then
  L3). Background work (summarize/agreement) bypasses the gate and **never**
  spills — it rides L1 down so the (often paid) relief layer is reserved for live
  users. Atlas self-limits separately (`ATLAS_LLM_CONCURRENCY`,
  `TELA_ATLAS_MAX_CONCURRENT_RUNS`) and likewise stays on L1.

> [!NOTE]
> Size the gate to L1's real batch capacity, not to "1 request at a time."
> Continuous-batching servers (mlx-lm, vLLM, Ollama) serve tens of concurrent
> requests with low time-to-first-token — they degrade *throughput* gracefully,
> they don't queue. A gate set too low spills to the paid layer under normal
> multi-user load. The per-layer `TELA_LLM_{i}_MAX_PARALLEL` / `_RPM` remain an
> optional hard cap, distinct from this fill-then-spill gate.

A natural ladder: **L1** local model (free, fast), **L2** a subscription-backed
Claude wrapper (free, OpenAI-shaped), **L3** an elastic cloud (e.g. Groq) as the
final backstop. Each spill *down* the chain is a real degradation signal — see
**Grafana + alerts** below.

**Embeddings stay single.** They also ride the proxy (`TELA_RAG_EMBED_URL` →
`http://litellm:4000/v1`), which is why tela has an OpenAI-shaped embedder
(`internal/rag/embed_openai.go`, auto-selected on a `/v1` base) alongside the
native Ollama one — LiteLLM speaks OpenAI, not Ollama's `/api/embed`. There's no
embed chain: a relief embedder would have to be the **same 1024-d model** on a
second host (different-dim vectors aren't interchangeable in `page_chunks`).

**Per-service health + the admin breakdown.** A background prober
(`internal/api/ai_health.go`) pings the chat and embed endpoints separately every
30s (cheap metadata calls, never inference), tracking up/down, probe latency, and
time-in-state. It feeds the `tela_ai_service_up{service}` /
`tela_ai_probe_latency_seconds{service}` gauges and `GET /api/admin/ai-endpoints`,
which powers the **AI endpoints & reliability** card in *Settings → Insights*:
each service's live status, redacted endpoint host, model, whether it's behind a
relief pool, latency, and last-reachable time. Set `TELA_GRAFANA_AI_URL` to
deep-link the card to your Grafana failover dashboard.

**Grafana + alerts.** Point your Prometheus at tela `/metrics` (admin PAT bearer)
**and** at LiteLLM's `/metrics/` (the master key as Bearer; published per
`LITELLM_METRICS_BIND`). LiteLLM exports per-deployment series —
`litellm_deployment_success_responses_total` / `_failure_responses_total` (keyed
by `api_base` + `requested_model`), `litellm_deployment_state` (0 healthy / 1
partial / 2 outage), `litellm_deployment_cooled_down_total` — so you can alarm
precisely on a **spill down the chain**: any success on the L2/L3 `api_base` means
the layer above it failed (a real degradation), and `deployment_state > 0` means a
layer is down. Treat L2 engaging as a warning and L3 (the paid backstop) as a
higher-severity page. A ready dashboard + alert rules ship for the maintainer's
Grafana; adapt the PromQL (`tela_ai_*`, `litellm_*`) to your stack.

## Insights dashboard

**Settings → Insights** (instance-admin) is the instance-analytics overview,
aggregated server-side from existing tables (`GET /api/admin/stats`,
`internal/api/admin_stats.go`) — no new instrumentation:

- **Activity** — 30-day daily sparklines for page views, edits, sign-ins, asks,
  and client errors, sourced from the `events` log.
- **Active users** — rolling DAU / WAU / MAU (distinct event actors).
- **Growth** — cumulative users + pages over the window.
- **Leaderboards** — most-viewed pages, top contributors (by edits), most-active
  spaces (30d).
- **AI, errors & health** — ask volume + answer rate (`ask_log`), client errors
  by kind, and knowledge health: stale pages (90d+ untouched), orphans (no
  inbound `page_links`), and agreement contradictions.
- **AI endpoints & reliability** — per-service (chat/embed) live health, latency,
  relief-pool status, and token volume (see *AI reliability & failover*).

It's an admin screen, not a hot path (~12 aggregations over a 30-day window); at
larger event volumes the scans want a nightly rollup table — the scale follow-up.

## Metrics

A Prometheus exposition is served at **`GET /metrics`** (`internal/api/metrics.go`).
It is **instance-admin gated** — a scraper authenticates with an admin-scoped PAT
(`Authorization: Bearer tela_pat_<key>`). Exported series include
`tela_http_requests_total{method,route,status}`,
`tela_http_request_duration_seconds{method,route}`,
`tela_client_errors_total{kind}` (see below),
`tela_ai_tokens_total{kind}`, `tela_ai_service_up{service}` +
`tela_ai_probe_latency_seconds{service}` (AI reliability, below),
`tela_atlas_stuck_runs_killed_total`, `tela_polar_last_webhook_timestamp_seconds`,
and the standard Go runtime + process collectors. The edge forwards `/metrics`
to the backend; the admin-PAT bearer is the access gate (it's off
`auth.IsPublicPath`, so the session middleware runs).

## Client-side errors

Browser-side failures are beaconed to **`POST /api/client-errors`** and recorded
as `client.error` rows in the activity feed — visible in **Settings → Events**
under the **Errors** filter, with the full message + stack inline. Each report
bumps `tela_client_errors_total{kind}`, so a spike is alertable from `/metrics`.
The net spans both *uncaught* and *handled* failures, bucketed by `kind`:

- `error` / `unhandledrejection` — uncaught exceptions + rejected promises.
- `react` — a render-phase crash caught by the top-level `ErrorBoundary`.
- `query` / `mutation` — a failed data fetch/write, captured globally via
  TanStack Query's `QueryCache`/`MutationCache` (`lib/queryClient.ts`). Filtered
  to genuine breakage — network failures (status 0) and `5xx`; expected/handled
  cases (401 session-expiry, other 4xx, the rag/llm "disabled" 503 sentinels)
  are skipped so the feed stays signal.
- `resource` — a failed asset/chunk/image load.
- `collab` — reserved for live-collab failure reporting.

Reports are de-duped + capped per session client-side and rate-limited per user
server-side. The endpoint is authed (session/bearer); pre-login crashes on the
login screen are not captured. Frontend wiring: `frontend/src/lib/client-errors.ts`.

For triage, **Settings → Errors** (instance-admin) is a grouped "Issues" view:
client errors are collapsed by a server-computed fingerprint (kind + normalized
message + first stack frame, so ids/line-numbers don't fragment a group) into
one row per distinct error with a count, affected-user count, first/last seen,
and an expandable sample stack + recent occurrences. The raw chronological feed
stays under **Events → Errors**. Backend: `internal/api/admin_client_errors.go`
+ the `events.fingerprint` column (migration `0043`).

## Search-engine indexing (SEO)

Indexability is enforced **at the proxy** (`deploy/proxy/Caddyfile`), not the app
— the SPA can't set per-route response headers. The rule is *deny-by-default*:
every surface gets an `X-Robots-Tag: noindex` header **unless** it is meant for
the open web. Indexable surfaces:

- **The marketing landing** (`/`, `/mcp`, `/pricing`, `/privacy`, `/terms`) — any
  file that exists in the mounted `landing/dist`.
- **Public spaces & reader** (`/public/*`), **author/org homes** (`/u/*`), the
  **discovery directory** (`/discover`), and **handle pages** — `/{handle}` (a
  user or org home) and `/{handle}/{space-slug}` (a public space). These are the
  blog/profile surfaces, so they are served **without** the noindex header.
- The **public sitemap** (`/sitemap-public.xml`, backend-generated).

Everything else is noindex: the API (`/api/*`), private permalinks (`/p/*`),
share links (`/share/*`), and the whole auth-gated app shell (`/login`,
`/spaces/*`, `/settings`, `/search`, the editor, …). Crawler-UA hits on the
public surfaces are routed to the backend, which server-renders OG/JSON-LD
metadata (humans get the SPA, which sets its own per-page title/canonical).

> [!NOTE]
> A self-hosted instance is indexable the same way, but a **fresh** instance has
> no landing build and no public spaces — so crawlers see only noindex app routes
> until you publish a space or serve the landing. Keep your own instance private
> by simply not making any space public (the default).
