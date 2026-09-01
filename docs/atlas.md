# atlas — source-grounded, coverage-audited doc generation (inside tela)

> **Current (shipped on `main`, deployed).** The model evolved from the design below to an explicit **Credential → Project → Source → Run** (owner-scoped projects + a write-only credential store). **[As built](#as-built--model-ownership--internals-current--authoritative)** below is the authoritative current architecture; everything from *The native model* onward is **historical design context**. Hardening since launch: a **durable run queue**, **per-page draft/refine tolerance**, hardened LLM retry, an **exported-types reference page** (major coverage lever), kind-tagged source roots, git-auth-kept-off-`Source.Location` (token-leak fix), and a timezone-correct jira staleness probe. End-user docs live in tela Docs space 16 (*Atlas — automated documentation* + 3 children).

> ⚠️ **An experimental changeset is currently on `main`:** [atlas — output-budget changeset](atlas-output-budget-experiment.md) (2026-09-01). Reference pages were being silently truncated at the model's output cap — the SerpApi run lost 19 CLI flags and all 36 environment variables to it — so batches are now sized by what a call can *answer*, `finish_reason` is read instead of discarded, reference items are grounded by their own `file:line` instead of a concatenated-name retrieval query, and the repair loop only keeps a pass that improved coverage. Marked, single-commit, `git revert`-able; read that doc before changing anything in the draft/repair path.

> Status: **Phases 1–6 (backend) done; UI first cut done — all on `feat/atlas`.** Ported engine (`EngineStore` Postgres, chat→`TELA_LLM_URL`, embed→`rag.Embedder` via `EmbedFunc`), in-process publisher, executor + SSE + `ResumeDangling`, HTTP API (gating tested), **delta runs + freshness scheduler + drift** (P4), **per-space secret store + private-git & Jira sources** (P5), **MCP tools** `atlas_list_sources`/`atlas_run`/`atlas_sync`/`atlas_run_status` (P6), **run-finish notifications** via tela's native in-app system (P6). UI: the generation console (`/spaces/$spaceId/atlas`), `StatusBadge` + `CoverageGauge` primitives, a space-header entry point — **visually QA'd**. `cmd/atlasdiff` is the 1-1 fidelity harness; the live compass 1-1 **passed the deterministic gate** (64 files / 77 surface / 433 chunks + exact spine items). **Remaining:** the **tela Docs space (16) end-user docs** (at deploy) and opening the PR off `feat/atlas`. Baseline for reference: compass `fa5b543` → Atlas space 51; models `qwen3:30b-a3b-instruct-2507-q4_K_M` + `qwen3-embedding:0.6b` (compass `fa5b543` → Atlas space 51 reference; models `qwen3:30b-a3b-instruct-2507-q4_K_M` + `qwen3-embedding:0.6b`; baseline 64 files / 77 surface / 433 chunks / 13 pages / 100% must-cover). This is the developer/architecture contract for bringing the standalone `atlas` system natively into tela. It is the source of truth for the build; update it as phases land. End-user docs go to the tela Docs space (space 16) per `CLAUDE.md`.

## What this is

`atlas` (the experimental project at `~/proj/atlas`) turns **source systems** (git repos, Jira projects) into **coverage-audited, grounded documentation**: it runs a deterministic + LLM pipeline that produces a wiki of cited pages, and measures those pages against an objective **spine** of the source's real surface (routes, env vars, CLI flags, DB models, …) — reporting *what fraction of the important surface is actually documented*. That coverage number is the differentiator a free-roaming "ask the repo" tool can't produce, because it has no ground-truth inventory.

atlas already **publishes into tela** today over a REST client. Bringing it *inside* tela collapses the generator and its destination into one system: a tela space becomes both the output and (via tela's existing RAG/`research`/`/ask`) the answer surface. Coverage *guarantees* the askable corpus is complete.

The full description of standalone atlas is `~/proj/atlas/atlasdesc.md` (read it for the domain model and pipeline detail).

## As built — model, ownership & internals (current / authoritative)

The "space *is* the project / no `projects` table" design below was **superseded** during the build. atlas in tela is **Credential → Project → Source → Run**, owner-scoped to a user or org.

### Model & data (added migrations)
- **`atlas_projects`** — `name, owner_kind ('user'|'org'), owner_id, output_space_id, output_parent_page_id, cadence, auto_update`. A project owns its output destination + schedule.
- **`atlas_credentials`** — `owner_kind, owner_id, name, kind ('git'|'jira'), value, meta_json`. The token **value is write-only** (blanked on every read DTO) and **encrypted at rest** (see below); owner-scoped like projects.
- **`atlas_sources`** — `project_id (FK), cred_id (nullable FK), …, stale_since, upstream_checked_at, probe_error`. Belongs to a project, optionally binds a credential.
- **`atlas_page_map`** — `(source_id, slug) → page_id`; the publisher's stable mapping for upsert + publish-prune (replaces atlas's `page_deliveries`).
- Run/ingestion tables (`atlas_runs`, `atlas_run_events`, `atlas_files`, `atlas_symbols`, `atlas_chunks`) are as in the design below.

### Ownership, credentials & auth (security-sensitive)
- Management gated by `atlasOwnerManageErr` (owner user, owner-org admin, or instance admin); viewing by `atlasOwnerViewErr` (org members read).
- **Personal creds on org projects:** `atlasCredBindable` accepts a credential owned by **the project's owner OR the acting user** — so a user can lend a private token to an org project's source without it entering the org's reusable pool. Other admins can *run* it (the run uses the token) but can't see its value or reuse it elsewhere.
- **Credentials are encrypted at rest** (`internal/secretbox`, AES-256-GCM). tela *replays* these outbound — a git PAT to GitHub, a Jira token, an org's OIDC client secret — so unlike passwords and PATs it cannot hash them; they were plaintext columns until this landed. One package covers both `atlas_credentials.value` and `org_sso.client_secret`; each has exactly one seal point (the create/upsert handler) and one open point (`loadAtlasCredential` / `scanOrgSSO`), so a new query can't route around it. `meta_json` stays in the clear — it holds only non-secret adornments (jira email/base URL, git username).
  - **Key:** `TELA_CREDENTIAL_KEY` with the same precedence as the api-key HMAC secret — env → persisted `instance_settings` → generated-once-and-persisted. Unset is a supported single-node default; setting it explicitly is what keeps the key out of a DB dump. **Rotating or losing it strands every stored credential**: decryption fails closed (runs error, SSO logins 500) rather than handing ciphertext to a connector, and each credential must be re-entered.
  - **Stored form is `v1:` + base64url(nonce‖ciphertext), and the missing prefix is load-bearing:** a value without it is a pre-encryption row and is returned verbatim. That's the entire upgrade story — no migration, no re-entry, and rows get wrapped the next time they're saved. Don't drop the fallback until every deployment is known to be re-saved.
- **A git credential's username is host-supplied when the credential doesn't carry one** (`defaultGitUser`, `source/git/git.go`) — for the ERROR MESSAGE, not for whether auth works. Token-as-username authenticates fine on GitHub (fine-grained `github_pat_*` included — atlas run 36 got an authenticated `403 Write access not granted` that way, then run 37 succeeded once the token was rescoped). It fails *badly* though: on a rejected token GitHub 401s with a Basic challenge, git tries to prompt for the password half it never sent, and with no tty dies `could not read Password … No such device or address` — which names a tty problem and a password when the truth is an expired token. **That message cost a full debugging session and produced a confident, wrong diagnosis** (that the auth *shape* was broken); a fake token reproduces it exactly, because it is an invalid-token error, not a shape error. Sending `user:token` leaves git nothing to prompt for, so GitHub's own `Invalid username or token` reaches `probe_error`. github.com → `x-access-token`, gitlab.com → `oauth2`; **an unknown host deliberately keeps the old form**, since changing how a working self-hosted remote authenticates is invisible until someone reads `probe_error`. Fixed 2026-09-01 along with two genuine client-side defects found alongside it: the credentials dialog rendered a username input **only for jira**, hardcoding `meta: undefined` for git (so non-GitHub/GitLab hosts that *need* a username were unreachable), and it sent jira's value under `username` while the API requires `meta.email` — so *every* Jira credential created in the UI 400'd since launch.
- **Git auth is injected at command time, NEVER onto `Source.Location`.** `applyAtlasCred` sets `SecretValue`/`SecretMeta`; the git connector's `authURL` builds the auth'd URL only for the `git clone`/`ls-remote` exec. **Load-bearing:** a token on `Location` previously leaked into the overview page + run events **and** broke `CiteURL` parsing. The clone-failure path also **redacts** the token from git's output (`redactSecret`). `resolveCoreSource` applies the credential for both the run and the cheap detection probe.

### Output structure
`Space → [optional top-dir] → Project folder → per-source root → pages`. The **project folder is auto-created** (named after the project) when outputting to an existing space (`createProjectFolder`); a brand-new space named after the project is itself the namespace. Each per-source root is **kind-tagged** `"<name> - repo|jira"` (`sourceKindLabel`). Publish writes `pages` directly + queues RAG reindex (background-write pattern), keyed by `atlas_page_map`.

### Run queue (durable, global) — `atlas_run.go`
Runs are gated by a **DB-backed dispatcher** (`runDispatcher`/`dispatchPending`/`claimNextPending`), capped at `TELA_ATLAS_MAX_CONCURRENT_RUNS` (**default 1** — generation shares the LLM with interactive ask/research, and overlapping drafts demonstrably 502'd the endpoint). Launch paths (`StartRun`, `StartDelta`, scheduler) **enqueue** a `pending` row + `signalDispatch`; the dispatcher claims the oldest pending run (`pending`→`running`) when a slot frees. The queue **is** the set of `pending` rows, so a restart/redeploy never strands a queued run — on boot the dispatcher repopulates from the DB (`ResumeDangling` separately recovers in-flight `running` runs). One non-terminal run per source (`sourceHasNonTerminalRun`). *(Replaced an in-memory semaphore that lost queued runs on restart.)*

### Resilience
- **Per-page tolerance** (`stage_draft.go`/`stage_refine.go`): a transient LLM failure on one page no longer fails the run — `draft` drops the failed page (its surface → a coverage gap for `repair`), `refine` keeps the existing draft; the run fails only if **every** page fails.
- **Retry:** the lifted `llm` client retries 5xx/429/transport errors **6×** with capped exponential backoff (~30s window).
- **MaxTokens:** `TELA_LLM_MAX_TOKENS` → `ModelCfg.MaxTokens` → `max_tokens` on every chat call. Required — `mlx_lm.server` silently caps output at **512** when unset, truncating outlines/pages mid-JSON.

### Coverage & reference pages — `stage_outline.go`
The outline plans deterministic **reference pages** per spine kind that enumerate the surface with `file:line` citations. **Repos now get a `Components & Exported Types` page (`KindExport`)** — previously jira-only, so class/type-heavy repos (Java/Go/TS) under-covered badly (UDN went 46% → 95% on adding it). Citations linkify (`CiteURL`) to a commit-pinned GitHub blob URL (git) or a Jira browse URL (issues).

### Freshness / staleness — `atlas_scheduler.go`, `source/jira`
`detectStaleness` runs cheap no-clone `HasChanges` probes every 15 min and stamps `stale_since`; `pollRegen` regenerates only stale sources on the project cadence (a delta). **Jira change detection compares instants in Go** (`latestUpdated` vs the stored ref, normalized to UTC) — *not* a JQL `updated >= <literal>` probe, which reads the literal in the **instance timezone** and (with a UTC-formatted ref) perpetually re-matched the ref's own boundary issue → false "stale". `Delta` keeps the JQL as a timezone-approximate superset but trims to issues truly updated after the ref. Git uses exact-SHA `ls-remote` compare (immune).

**A failed probe is a state, not just a log line** (`probe_error`, migration `0077`). The failure path used to stamp `upstream_checked_at` and return, so an unreachable source was indistinguishable from a healthy one: every surface — the source badge, `stale_sources`, the project header, the Atlas home cards, `digestStale` — decided from `stale_since` alone, and a probe that can't reach upstream never sets it. Measured before the fix: 7 live sources failing every cycle for weeks (6 deleted repos + one expired PAT), 5 rendering **Done** and 2 (flagged stale *before* they broke) frozen on **Stale** forever. `probeStaleness` now writes the error into `atlas_sources.probe_error` and clears it on both success paths; **`stale_since` is deliberately untouched on failure** — an unreachable probe knows nothing about drift, and writing either answer would be a guess. The UI ranks **Unreachable above Stale and above the last run's status**, per-source and per-project, because a run that succeeded three weeks ago says nothing about a source that has since disappeared. Storing the text is safe because the git connector already redacts credentials from command output and jira auth rides a header; `probeErrText` only trims and bounds it.

---

## The native model — "the space *is* the project" *(historical design — superseded by* As built *above)*

Standalone atlas's `Project` only existed to group sources + an LLM connection + an output dir + a freshness cadence. Inside tela the connection is gone (instance LLM), the output *is* a space, and cadence lives on the space — so **the managed space is the project**. No `projects` table.

```
Space (atlas-managed)          ← unit of management; freshness/cadence live here
  └─ Source (git | jira)       ← 1+ per space; each optionally under a top-dir parent page
       └─ Run (full | delta)   ← one generation pass; live progress; produces Coverage
            ├─ Pages           ← real tela pages, run-owned (props.generator=atlas)
            └─ Coverage        ← native dashboard: must-cover %, gap list, drift
```

**"Managed" is the one new distinction.** A space becomes atlas-managed when a source is bound to it (`atlas_sources.space_id`). One source per space = "completely atlas-managed" (the primary case); multiple sources nest under per-source parent pages (atlas's current top-dir shape). Managed spaces are surfaced distinctly in the UI and their generated subtree is machine-maintained.

## Access & ownership (reuse tela's model — no new permission system)

The managed space *is* the project, and a tela space is already either **personal** (a user owns) or **org-owned** (`spaces.org_id` set), with access resolved through the `space_access` view to one effective role (`owner > editor > viewer`; org/group grants folded in). So "org or personal projects, org ones managed by org admins" falls out for free:

- **Manage atlas** (add/edit/delete sources, trigger runs, set cadence, unbind): `requireSpaceManage(spaceID)` = effective space role `owner` **OR** (`space.org_id != null` AND `requireOrgAdmin(org_id)`). Instance admins pass as virtual org admins. *Management is admin-level on purpose — a run fetches an external source, spends LLM budget, and rewrites the whole generated subtree.*
- **View** coverage / runs / sources: `requireMembership` (viewer+), like any space resource; the api-key space-scope ceiling applies automatically.
- Creating an org-owned managed space / binding a source uses existing org-space creation rights (`createSpaceCore` already grants owner + org-editor).
- This also fixes the publish-identity question: authority is checked when a **user triggers/configures** a run; the publish **writes** run in the background via direct SQL (the summarize/agreement pattern — no per-write identity, no revision/notification pollution), then queue RAG reindex.

## Decisions (locked with Cagdas)

1. **Space mapping** — a source manages a space (or a top-dir within it). Whole managed spaces are tracked distinctly; no separate `projects` concept. Don't over-engineer the marker.
2. **Models** — **reuse tela's instance LLM + embedder** (same Ollama box, same chat model, same `qwen3-embedding:0.6b`). No per-project model-config UI, no `connections` table. (See *Fidelity* for how this is reconciled with the lifted `llm` client.)
3. **Human-edit contract on generated pages** — *open*; settled at the publish stage (Phase 2). Default to revisit: atlas's model (generated subtree is machine-maintained, re-run clobbers body wholesale, tela revisions recover). "Detach on edit" can layer on later with no schema churn.
4. **Scope** — build all of it; sequencing below.

## Fidelity is the hard requirement

The ingestion/generation pipeline is calibrated (chunk sizes, retrieval fusion weights, prompt wording, temperatures, K/context budgets, repair thresholds, mermaid rules). **No regression.** The existing atlas-generated spaces are the golden baseline for a 1-1 diff.

Strategy: **lift the atlas Go packages verbatim and rewire only the seams.** Re-deriving any stage from a summary is forbidden.

| atlas package | disposition inside tela |
|---|---|
| `internal/core` | **lift verbatim** → `backend/internal/atlas/core` (pure types, no deps) |
| `internal/source` (git, jira, citeurl) | **lift verbatim** → `backend/internal/atlas/source` (pure logic + `git`/REST exec) |
| `internal/llm` | **lift verbatim** → `backend/internal/atlas/llm`. **Chat:** atlas's client pointed at `TELA_LLM_URL` (already `/v1`) — identical `/chat/completions` transport, so the calibrated retry/backoff/concurrency/temperatures are preserved against tela's instance model. **Embed:** tela's embed endpoint is **Ollama-native `/api/embed`**, not OpenAI `/embeddings`, so the lifted client's embed path can't hit it directly. Instead a small optional `EmbedFunc` seam on the client **delegates embedding to tela's `rag.Embedder`** (same instance Ollama, same `qwen3-embedding:0.6b`). Vectors are per-text identical regardless of batching, so there is **no retrieval regression** — and generation now embeds through the exact embedder tela's own RAG uses (one model, one endpoint, shared metering). nil `EmbedFunc` keeps the original HTTP path for standalone use. The delegate must pass the batch **through** (`rag.EmbedMany`, one upstream request for N inputs) and report the request count back — an earlier version looped per input and turned a repo ingest into ~20k requests that starved the chat model on the same GPU; see the embeddings warning in `docs/operations.md`. |
| `internal/engine` incl. `retrieve.go` | **lift verbatim**; only `RunContext.Store` changes type (see Store seam). `retrieve.go` (in-memory dense+BM25 fusion, `denseWeight=0.6`) is **kept as-is** — swapping to tela's pgvector RRF would change retrieval ranking → change drafting inputs → diverge from the reference. pgvector is used only to *persist* chunk vectors for resume/delta; the in-memory retriever is rebuilt per run exactly as atlas does. |
| `internal/store` (SQLite) | **not lifted** — replaced by a narrow `EngineStore` interface backed by tela Postgres. A parallel SQLite layer would be the tech debt we're avoiding. |
| `engine/deliver.go` + `internal/tela` REST client | **rewritten in-process** — publish calls tela's `createPageCore`/`updatePageCore`; the `internal/tela` import vanishes; `destinations`/`page_deliveries` collapse to space-ownership + `props.generator=atlas`. |
| `internal/notify`, `internal/secret` | **pluggable / deferred** (Phase 6 / Phase 5). `notify` is a no-op hook initially; `secret` (git/jira creds) is a new minimal store. |

### The Store seam (the only forced change in the engine)

The engine calls exactly 17 methods on `rc.Store`. Converting `RunContext.Store *store.Store` → `Store EngineStore` (interface) is the single edit the lifted stages need. The pure-pipeline subset (everything except the connection/destination calls, which live in the rewritten publish path):

```
AppendEvent · UpdateRun · GetRun · SetSourceRef
SaveFiles · SaveSpine · SaveChunks · SaveVectors
CopyChunksToRun · RunChunksWithVectors            (delta reuse / resume)
SavePages · UpdatePageBody
SaveRunCoverage · SaveRunStats
```

Connection/destination-shaped calls (`GetConnection`, `ResolveModel`, `ListDestinations`) are removed: the LLM `ModelCfg` is injected from tela env at run construction, and the publish target is the bound space.

## Data model (new migrations, forward-only `NNNN_atlas_*.sql`)

Run-scoped ingestion tables mirror atlas's (disposable, `ON DELETE CASCADE` from runs) but in Postgres with `pgvector`:

- `atlas_sources` — `id, space_id (FK), parent_page_id (nullable), type ('git'|'jira'), location, name, ref, branch, subpath, include, exclude, secret_id (nullable, Phase 5), cadence, auto_update, last_refresh_at, created_at`. Binding a row makes the space atlas-managed.
- `atlas_runs` — `id, source_id (FK), kind ('full'|'delta'), baseline_id, changeset_json, status, stage, err, coverage_json, stats_json, started_at, finished_at`.
- `atlas_run_events` — `id, run_id (FK), stage, level, msg, cur, total, at` (durable SSE replay).
- `atlas_files` — per-run inventory (`path, lang, size, lines, hash`).
- `atlas_symbols` — the spine (`kind, name, file, line, detail`).
- `atlas_chunks` — `run_id, file, start_line, end_line, kind, symbol, text, embedding vector(1024), content_tsv`. Run-scoped; the in-memory retriever is rebuilt from these rows.
- Managed-space marker — derived from `atlas_sources` (lightweight denorm on `spaces` only if a hot path needs it; not over-engineered).

Generated **pages** are ordinary `pages` rows in the bound space, tagged `props.generator=atlas` (+ source/commit/`provenance=agent`/`generated_at`), created/updated via the existing page core funcs (so revisions, link-sync, and RAG reindex all fire automatically).

## Backend layout

```
backend/internal/atlas/            # the lifted engine — no internal/api deps
  core/ · source/{git,jira} · llm/ · engine/ (stages incl. retrieve.go) · coverage seam
backend/internal/api/atlas_*.go    # handlers, run executor, scheduler, SSE, MCP tools, publish-in-process
backend/internal/db/migrations/    # NNNN_atlas_*.sql
```

Generation embeds in bulk against the shared warm instance Ollama; the executor gets a bounded embed-concurrency budget (the lifted `llm` client's gate) so a big repo ingest doesn't starve live page reindexing — the one real ops nuance.

## Run lifecycle / progress

Persisted runs; an executor goroutine pool drives `engine.Default().Run()`; `ResumeDangling` on boot continues runs left `running` (lifted from atlas `resume.go`). Live progress streams over SSE (`/api/atlas/runs/{id}/stream`) replayed from `atlas_run_events`, mirroring tela's `/ask` stream pattern; the FE hook mirrors `useAskDocsStream`.

## UI (Phase 3) — space-centric, native

The managed space view *is* the project detail: **Sources / Runs / Coverage** alongside its pages, live run progress, and a coverage dashboard. Strictly tela tokens + owned primitives (the dark "Instrument Panel" aesthetic does **not** carry over). Two genuinely new owned primitives, each with a Storybook story: a **radial coverage gauge** and a **live run/log stream**.

## 1-1 comparison harness

After Phase 2, generate into a fresh tela space from the same source + same models as a deployed atlas reference space, then diff: spine items (count + kinds), page set (titles/slugs/order), coverage numbers (must-rate, gaps, citations, mermaid), and body content. Divergence = a porting bug. (Build a small diff script under `scripts/` or a Go test fixture.)

## Sequencing

1. **Foundations** — migrations + `internal/atlas` skeleton (verbatim copy + import rewrite) + `EngineStore` interface + managed-space marker.
2. **Core loop (public git, full run)** — spine→…→repair→publish-in-process; executor + persistence + resume; minimal trigger/read API; 1-1 diff vs reference.
3. **Native operator UI** — managed-space Sources/Runs/Coverage + live progress + sidebar distinction.
4. **Delta + freshness** — change-gated delta re-ingest, cadence scheduler, publish-prune, drift.
5. **Jira + secret store** — port `source/jira`; minimal write-only git/jira secret store (unblocks private repos too).
6. **Notifications + MCP tools + docs** — run-finish notifications (reuse mailer), MCP tools (trigger run / read coverage), repo + space-16 docs.

---

## Deferred ideas (explored, not built)

### A `tela`-space source connector (a third source type)

**Status: deferred (2026-07-09). Nothing built — the `source.Connector` seam is unchanged, so this slots in whenever.** Revisit trigger: the "document how the work is done" use case arriving from a *different* direction — one corpus is a single data point, not a signal.

**The idea.** A third source type (`tela`) alongside `git`/`jira`: the source is a **hand-curated tela space/subtree** (filled by the user's agent via MCP `create_page`); atlas ingests it and produces a coverage-audited, cited **analysis wiki in a separate output space**. Sidesteps all document-extraction/OCR/binary-skip problems because the content is already markdown. Origin: "can atlas analyze a non-repo doc corpus?" — pointing it at raw files fails hard (Inventory drops all binaries; spine is code-only), so "curate into tela first, then atlas the space" was the in-between.

**Architecturally clean (the free half).** Chunk reads content off disk (`stage_chunk.go`) and **markdown is a first-class chunker** (`chunkDoc`, `LangMarkdown`) → Acquire just materializes pages to a workdir as `.md`; Inventory/Chunk/Embed/Index/Draft/Refine/Publish run unchanged. **Delta/freshness is trivial** (`HasChanges` = max `updated_at` high-water-mark; no remote fetch, no credentials — `SecretID` unused). **Citations get better** — a `case "tela"` in `CiteURL` maps the materialized path → `/p/{id}` (clickable in-instance links + backlinks).

**Not free reuse (the real work — same shape Jira was).** atlas already has *two* methodologies: the code path and an explicitly "not-a-codebase" Jira path (`outlineJiraSystem`, `KindState` registered via `init()`). A tela-docs path is a **third personality**, built the Jira way: (1) a **new spine notion** — every `SpineKind` today is code (route/export/env/db_model) or tracker (state); need a docs kind (page / heading / **checklist-item**) + extractor + must-cover policy; (2) a **third prompt family** (code and Jira prompts both misfit a process corpus; prompts are calibrated/load-bearing → delicate); (3) outline seeding only has teeth if the docs spine is real.

**The framing that makes it worth it.** Not "point atlas at a folder of PDFs" (that fights atlas on extraction it was never built for) but **atlas as a process/playbook documenter**: *document how work is done, grounded in the artifacts of doing it, completeness audited against the process's own checklist.* Scope is the **process/methodology docs, NOT the underlying data** — exact numbers / account-level reasoning are explicitly out of scope (atlas is the wrong tool there). Exemplar: a Turkish independent-audit engagement archive, where the spine is handed to you in the data — the **PBC checklist + audit-area taxonomy (C→W) + methodology steps**; coverage = "does the process doc cover every required area/step." Generalizes to legal matter workflows, consulting engagements, onboarding runbooks.

**Ask-time note.** If a raw curated layer and the generated analysis coexist, **emphasize the generated layer via scoping, not a retrieval weight**: default the ask to the analysis space (reasoned + coverage-confirmed = the deliverable); keep raw curated docs in a separate space as the cited substrate, reached by drilling into a generated page's citations. Avoid pooling generated + raw in one space and blending them chunk-for-chunk. (There's a per-class score-modifier precedent — `publicRankPenalty`/`publicDistPenalty` in `rag/access.go` — but a static boost is the blunt version; separating surfaces is the honest one.)
