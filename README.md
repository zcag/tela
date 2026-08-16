<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/submission-assets/logo-lockup-dark.svg">
  <img alt="tela" src="docs/submission-assets/logo-lockup-light.svg" width="184">
</picture>

### The wiki that writes itself — from the code you already ship.

[![Sign up free](https://img.shields.io/badge/▶%20Try%20tela%20free-telawiki.com-7C3AED?style=for-the-badge&labelColor=1a1a1a)](https://telawiki.com/register)
&nbsp;
[![Tour the site](https://img.shields.io/badge/🌐%20Tour%20the%20site-telawiki.com-0b7285?style=for-the-badge&labelColor=1a1a1a)](https://telawiki.com)
&nbsp;
[![Self-host it](https://img.shields.io/badge/⚙%20Self--host%20in%20one%20command-make%20up-2496ED?style=for-the-badge&labelColor=1a1a1a)](#quickstart)

**[Home](https://telawiki.com) · [Pricing](https://telawiki.com/pricing/) · [Compare](https://telawiki.com/compare/) · [MCP for agents](https://telawiki.com/mcp/) · [Docs](https://telawiki.com/tela/docs)**

[![License: AGPL-3.0](https://img.shields.io/badge/license-AGPL--3.0-blue.svg)](LICENSE)
[![GitHub stars](https://img.shields.io/github/stars/zcag/tela?style=flat)](https://github.com/zcag/tela/stargazers)
[![Latest release](https://img.shields.io/github/v/release/zcag/tela?sort=semver)](https://github.com/zcag/tela/releases)
[![Docker](https://img.shields.io/badge/Docker-compose-2496ED?logo=docker&logoColor=white)](deploy/docker-compose.yml)
[![npm tela-mcp](https://img.shields.io/npm/v/tela-mcp?label=npm%20tela-mcp&color=7C3AED&logo=npm)](https://www.npmjs.com/package/tela-mcp)

</div>

tela is a self-hostable, markdown-native team wiki built for a world where agents are first-class authors and readers. It pairs a Go + PostgreSQL backend with a React 19 / Milkdown editor, live Yjs collaboration, ranked full-text and semantic search, and a built-in Model Context Protocol (MCP) server — so the same knowledge base your team edits in the browser is one your agents can search, read, and write directly. Atlas, its documentation engine, turns the artifacts you already produce into maintained wiki pages. Your content stays canonical markdown forever — `pages.body` is markdown, there is no proprietary block store.

> **[👉 Start at telawiki.com](https://telawiki.com)** — the marketing site walks through what tela does (with [pricing](https://telawiki.com/pricing/), a [comparison](https://telawiki.com/compare/), and the [agent/MCP story](https://telawiki.com/mcp/)), and the hosted instance has a free tier: no install, no card.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/submission-assets/tela-page-dark.png">
  <img alt="tela — a page in the Operations space: spaces and a nested page tree in the sidebar, with stat tiles, a callout, and a Mermaid diagram rendered from plain markdown" src="docs/submission-assets/tela-page-light.png" width="820">
</picture>

## Why tela

- **Atlas auto-doc-gen** — point Atlas at your sources and it drafts and maintains real wiki pages, so docs track what you ship instead of rotting.
- **Built-in MCP server** — `/api/mcp` is part of the backend, not a bolt-on. Claude, Cursor, and other agents search, read, and author pages with scoped, per-tool write permissions.
- **Semantic + full-text search** — ranked PostgreSQL FTS works out of the box; add an embedder for `pgvector`-backed semantic retrieval and grounded "ask your docs" answers.
- **Live collaboration** — real-time multi-cursor editing over Yjs in a Milkdown editor, with comments, backlinks, and revision history.
- **Teams that run themselves** — users create orgs and invite teammates by email, no admin ticket. Flip a space to public and it becomes a login-free blog surface with per-author home pages.
- **Self-host & own your markdown** — `pages.body` is canonical markdown forever (no block table). Sync over WebDAV, export to zip/PDF, and run the whole stack with one `make up`.

## Quickstart

You need **Docker** (with Compose) and **make**. The bundled stack builds every image via Compose, so no host Node or Go toolchain is required.

```bash
git clone https://github.com/zcag/tela.git
cd tela

# 1. Write deploy/.env from the example with strong generated secrets
#    (fills TELA_API_KEY_SECRET, TELA_SHARE_SECRET, TELA_PG_PASSWORD via openssl rand)
make setup

# 2. Edit deploy/.env — set TELA_PUBLIC_BASE_URL, and optionally
#    TELA_ADMIN_* and TELA_SMTP_* (see Configuration below)

# 3. Build and start the full stack
make up
```

The stack comes up behind Caddy on **http://localhost:8780**. On first boot tela runs its embedded migrations automatically and lands you on the `/setup` wizard to create the first admin — unless you set `TELA_ADMIN_PASSWORD` in `deploy/.env`, which bootstraps the admin non-interactively.

### One-click cloud deploy

Prefer managed hosting over running Compose? Deploy the published multi-arch images straight to a platform:

[![Deploy to Render](https://render.com/images/deploy-to-render-button.svg)](https://render.com/deploy?repo=https://github.com/zcag/tela) &nbsp; [![Deploy to DigitalOcean](https://www.deploytodo.com/do-btn-blue.svg)](https://cloud.digitalocean.com/apps/new?repo=https://github.com/zcag/tela/tree/main)

> After the first deploy, enable pgvector on the managed database once — `CREATE EXTENSION IF NOT EXISTS vector;` — then point the backend's `TELA_PUBLIC_BASE_URL` at your app's public URL. Or skip ops entirely with the [free cloud tier](https://telawiki.com).

Common targets:

```bash
make up         # build + start the stack on :8780 (auto-stamps git version/commit)
make down       # stop the stack
make logs       # tail logs from all services
make backup     # dump Postgres to ./backups/tela-<timestamp>.sql
make restore FILE=backups/tela-....sql
make clean FORCE=1   # stop and DELETE all volumes (destroys data)
```

`make up` is the same thing as running Compose directly — under the hood it's `docker compose -f deploy/docker-compose.yml up -d --build` (plus a forced proxy recreate so a changed Caddyfile re-mounts). The three load-bearing secrets must be set and **stable** across deploys: rotating `TELA_SHARE_SECRET` invalidates outstanding share cookies, and rotating `TELA_API_KEY_SECRET` invalidates every existing personal access token (PAT).

For full setup, TLS, backups, and upgrades see [`docs/self-hosting.md`](docs/self-hosting.md) and the operations runbook [`docs/operations.md`](docs/operations.md).

### Local development

```bash
make dev        # backend (:8080) + frontend (:5173, proxies /api → :8080); boots a local dev Postgres
make be-dev     # backend only (go run; boots the dev Postgres on :55433)
make fe-dev     # frontend only (vite)
make test       # backend tests against a throwaway Postgres
make storybook  # component dev surface
```

If `:8080` is taken on your box, run `make dev DEV_BE_PORT=18080` so the backend and the vite `/api` proxy stay consistent.

### Optional: semantic search out of the box

The bundled Compose stack ships an optional Ollama embedder behind a profile. Full-text search always works; this only lights up the semantic half:

```bash
docker compose -f deploy/docker-compose.yml --profile embed up -d
docker compose -f deploy/docker-compose.yml exec ollama ollama pull qwen3-embedding:0.6b
# then set TELA_RAG_EMBED_URL=http://ollama:11434 in deploy/.env and restart
```

## Configuration

All configuration is environment-driven via `deploy/.env` (copy from [`deploy/.env.example`](deploy/.env.example)). In the bundled Compose stack `TELA_DATABASE_URL` is auto-constructed from the `TELA_PG_*` vars — you only set it explicitly when running the backend against an external Postgres.

### Required

| Variable | Description |
| --- | --- |
| `TELA_PUBLIC_BASE_URL` | Public origin of the instance (e.g. `https://wiki.example.com`). Used in emails, share links, and OAuth audiences. |
| `TELA_SHARE_SECRET` | Secret signing share-link password cookies. Generate with `openssl rand -hex 32`; never rotate (invalidates outstanding share cookies). |
| `TELA_API_KEY_SECRET` | Secret signing personal access tokens (PATs). Generate with `openssl rand -hex 32`; never rotate (invalidates every existing PAT). |
| `TELA_PG_PASSWORD` | Password for the bundled Postgres. **No default** — must be set. |

### Postgres

| Variable | Description |
| --- | --- |
| `TELA_PG_USER` | Postgres role for the bundled DB. Default `tela`. |
| `TELA_PG_DB` | Postgres database name. Default `tela`. |
| `TELA_DATABASE_URL` | Full DSN — **only** set when running outside the bundled stack (external/managed Postgres). Format `postgres://USER:PASS@HOST:5432/DB?sslmode=disable`. |

### Bootstrap admin (optional)

| Variable | Description |
| --- | --- |
| `TELA_ADMIN_USERNAME` | Bootstrap admin username (first boot only). |
| `TELA_ADMIN_PASSWORD` | Bootstrap admin password. Unset → use the `/setup` web wizard instead. |
| `TELA_ADMIN_EMAIL` | Optional; pre-confirms the admin's email so it's exempt from the confirmation gate. Setting it later backfills the existing admin. |

### Email (transactional + notifications)

With `TELA_SMTP_HOST` unset, tela logs confirmation/reset/notification links to stdout instead of sending (fine for dev / first boot). Works with any SMTP relay.

| Variable | Description |
| --- | --- |
| `TELA_SMTP_HOST` | SMTP relay host (e.g. `smtp.resend.com`). |
| `TELA_SMTP_PORT` | `587` starttls (default) or `465` ssl. |
| `TELA_SMTP_TLS` | `starttls` \| `ssl` \| `none`. |
| `TELA_SMTP_USERNAME` | SMTP username. |
| `TELA_SMTP_PASSWORD` | SMTP password or API key. |
| `TELA_SMTP_FROM` | From identity, e.g. `tela <tela@example.com>`. |

### Custom domains & TLS (optional)

| Variable | Description |
| --- | --- |
| `TELA_SITE_ADDRESS` | Canonical host Caddy binds for direct-TLS mode (e.g. `telawiki.com`). Empty → `:80` (terminator/CF mode), which disables org custom domains. |
| `TELA_CUSTOM_DOMAIN_TARGET` | Shared CNAME target shown to org admins adding a hostname. Defaults to the canonical host. |

### AI: semantic retrieval, ask-your-docs, Atlas (all optional, ship dark)

These features ship **dark** — unset means the relevant endpoints return `503` and nothing is computed. Point them at your own Ollama / OpenAI-compatible endpoints, the bundled `--profile embed` Ollama, or tela cloud's managed endpoints (authenticated with a `telawiki.com` PAT).

| Variable | Description |
| --- | --- |
| `TELA_RAG_EMBED_URL` | Embedder endpoint for semantic chunk search (e.g. `http://ollama:11434`). Unset → `/api/rag/*` 503. |
| `TELA_RAG_EMBED_MODEL` | Embedding model. Must be 1024-d; default `qwen3-embedding:0.6b`. |
| `TELA_RAG_EMBED_DIM` | Advisory embedding dimension (the column is fixed at `vector(1024)`). |
| `TELA_RAG_EMBED_TOKEN` | Bearer token for a managed/authenticated embed endpoint. |
| `TELA_RAG_QUERY_INSTRUCT` | Query-side instruction prefix for asymmetric retrieval. Unset → sensible default. |
| `TELA_RAG_RERANK_URL` | Optional cross-encoder reranker `/rerank` endpoint (Cohere/Jina/TEI-compatible). |
| `TELA_RAG_RERANK_MODEL` / `TELA_RAG_RERANK_TOKEN` | Reranker model name and token. |
| `TELA_RAG_LOG_ASKS` | Log "ask your docs" questions to surface knowledge gaps (admin-only). `0` disables. |
| `TELA_LLM_URL` | OpenAI-compatible chat base **including `/v1`** for grounded answers (`/api/rag/ask`). Unset → 503. |
| `TELA_LLM_MODEL` | Chat model name (e.g. `qwen2.5:7b`). |
| `TELA_LLM_TOKEN` | Bearer token for a managed/authenticated LLM endpoint. |
| `TELA_LLM_MAX_TOKENS` | Completion length cap. Default 1024; `0`/`-1` disables the cap. |
| `TELA_AGREEMENT` | Epistemic trust pass (corroborate/contradict scoring). On when LLM+embedder are set; `0` disables. |
| `TELA_ATLAS_MAX_CONCURRENT_RUNS` | Cap on concurrent Atlas doc-gen runs. Default 1. |
| `ATLAS_LLM_CONCURRENCY` | Per-run client concurrency gate. Default 6. |
| `TELA_ATLAS_WORKDIR` | Where Atlas unpacks working files. Default: OS temp dir. |
| `TELA_IMAGE_GEN_URL` / `TELA_IMAGE_GEN_MODEL` / `TELA_IMAGE_GEN_KEY` | OpenAI-compatible Images endpoint for the MCP `generate_deck_image` tool. Unset → 503. |

### Auth: MCP OAuth & federated sign-in (optional)

| Variable | Description |
| --- | --- |
| `TELA_WORKOS_ISSUER` | WorkOS AuthKit issuer to enable Claude.ai/ChatGPT "Connect" OAuth on `/api/mcp`. Unset → MCP stays PAT-only. |
| `TELA_MCP_RESOURCE` | The MCP endpoint's public URL (OAuth audience). Defaults to `{TELA_PUBLIC_BASE_URL}/api/mcp`. |
| `WORKOS_API_KEY` | Server-side WorkOS secret for the Standalone login bridge. |
| `TELA_SSO_GOOGLE_CLIENT_ID` / `_SECRET` | Google OIDC sign-in. Dark until both are set. |
| `TELA_SSO_MICROSOFT_CLIENT_ID` / `_SECRET` | Microsoft OIDC sign-in. Dark until both are set. |
| `TELA_SSO_GITHUB_CLIENT_ID` / `_SECRET` | GitHub OAuth2 sign-in. Dark until both are set. |

### Billing (optional, ships dark)

| Variable | Description |
| --- | --- |
| `TELA_POLAR_TOKEN` | Polar organization access token. Unset → checkout/portal 503; plans stay operator-assigned. |
| `TELA_POLAR_WEBHOOK_SECRET` | Polar webhook signing secret (verbatim). |
| `TELA_POLAR_BASE_URL` | `https://api.polar.sh` or `https://sandbox-api.polar.sh`. |
| `TELA_POLAR_PRODUCTS` | Maps plan keys to Polar product UUIDs, e.g. `personal_plus:<uuid>,org_team:<uuid>`. |

### Services, sync & ops (optional)

| Variable | Description |
| --- | --- |
| `TELA_GOTENBERG_URL` | PDF render engine. Default `http://gotenberg:3000`. |
| `TELA_PDF_RENDER_BASE_URL` | Internal origin Gotenberg's Chromium loads the reader from. Default `http://proxy`. |
| `TELA_DECK_URL` | Slidev deck render sidecar. Default `http://deck:3344`. |
| `TELA_WEBDAV_ENABLED` | WebDAV sync surface (`/dav/`). Default on; `0`/`false` disables. |
| `TELA_WEBDAV_CREATE_SPACES` | Allow root-level `MKCOL` to mint spaces. Default on (any write-scoped PAT can create spaces via WebDAV). |
| `TELA_WEBDAV_DELETE_FLOOR` / `TELA_WEBDAV_DELETE_FRACTION` | Mass-delete guard tuning. |
| `TELA_WEBDAV_FILE_MAX_BYTES` | Per-file upload cap for space files. |
| `TELA_ADDR` | Backend listen address. Default `:8080`. |
| `TELA_LOG_FORMAT` | `json` for structured logs. Default text. |
| `TELA_API_KEY_AUDIT_DAYS` | PAT audit-log retention in days. |
| `TELA_EVENTS_RETENTION_DAYS` | Activity-feed GC window. Default 180. |
| `TELA_DISABLE_WELCOME_SEED` | Any value skips seeding the welcome space on first boot. |
| `TELA_VERSION` / `TELA_COMMIT` | Build metadata surfaced by `GET /api/version` (auto-stamped by `make`). |

> The split/deploy topology adds image-ref and Umami-analytics vars (`TELA_BACKEND_IMAGE`, `TELA_FRONTEND_IMAGE`, `UMAMI_APP_SECRET`, `UMAMI_DB_PASSWORD`, …). See [`deploy/.env.example`](deploy/.env.example) and `docs/deploy.md`.

## Connect your agents (MCP)

tela's MCP server is built into the backend at **`/api/mcp`** — it self-authenticates with a personal access token (PAT) as a bearer header. Modern hosts speak HTTP transport directly:

```
https://telawiki.com/api/mcp            # tela cloud
https://your-host.example.com/api/mcp   # your self-hosted origin
```

For hosts that can't speak HTTP transport (or want a stdio bridge), the [`tela-mcp`](https://www.npmjs.com/package/tela-mcp) npm package is a thin stdio↔HTTP proxy to the same endpoint — no second tool implementation to drift. Add it to your MCP client config (e.g. Claude Desktop / Cursor):

```jsonc
{
  "mcpServers": {
    "tela": {
      "command": "npx",
      "args": ["-y", "tela-mcp"],
      "env": {
        "TELA_BASE_URL": "https://telawiki.com",
        "TELA_API_KEY": "tela_pat_xxxxxxxx"
      }
    }
  }
}
```

Point `TELA_BASE_URL` at your own origin to use a self-hosted instance. Generate a PAT in **Settings → API tokens**; per-tool write permission is enforced server-side. The proxy requires **Node ≥ 20**. See [`mcp/README.md`](mcp/README.md) for the full tool catalog and troubleshooting.

### One-click install

[![Add to Cursor](https://img.shields.io/badge/Add%20to-Cursor-000000?logo=cursor&logoColor=white)](cursor://anysphere.cursor-deeplink/mcp/install?name=tela&config=eyJ1cmwiOiJodHRwczovL3RlbGF3aWtpLmNvbS9hcGkvbWNwIn0=)
[![Add to VS Code](https://img.shields.io/badge/Add%20to-VS%20Code-0098FF?logo=visualstudiocode&logoColor=white)](https://insiders.vscode.dev/redirect/mcp/install?name=tela&config=%7B%22type%22%3A%22http%22%2C%22url%22%3A%22https%3A%2F%2Ftelawiki.com%2Fapi%2Fmcp%22%7D)

Both buttons add the HTTP endpoint; auth is handled by OAuth on first use, so no token goes in the link.

### Per-client setup

HTTP-transport hosts connect to the endpoint directly and sign in via OAuth. stdio-only hosts use the `tela-mcp` proxy with a PAT (`TELA_BASE_URL` + `TELA_API_KEY`).

**Claude Code** (CLI, HTTP):

```bash
claude mcp add --transport http tela https://telawiki.com/api/mcp
```

**Cursor** (HTTP) — use the button above, or add to `~/.cursor/mcp.json`:

```jsonc
{ "mcpServers": { "tela": { "url": "https://telawiki.com/api/mcp" } } }
```

**VS Code** (HTTP) — use the button above, or:

```bash
code --add-mcp '{"name":"tela","type":"http","url":"https://telawiki.com/api/mcp"}'
```

**ChatGPT** — install [Tela](https://chatgpt.com/plugins/plugin_asdk_app_6a22ce5f60dc8191a56bf3f66890b1cf) from the plugin directory and sign in. Nothing to paste, no Developer Mode. (The directory plugin talks to the hosted service; for a self-hosted instance add a custom connector with your own URL, as below.)

**Claude.ai / self-hosted ChatGPT** (OAuth connector) — add a custom connector and paste the URL; complete the sign-in:

```
https://telawiki.com/api/mcp
```

**Claude Desktop** (stdio proxy) — `claude_desktop_config.json`:

```jsonc
{
  "mcpServers": {
    "tela": {
      "command": "npx",
      "args": ["-y", "tela-mcp"],
      "env": { "TELA_BASE_URL": "https://telawiki.com", "TELA_API_KEY": "tela_pat_xxxxxxxx" }
    }
  }
}
```

**Windsurf** (stdio proxy) — `~/.codeium/windsurf/mcp_config.json`, same `mcpServers` shape as Claude Desktop above.

**Codex** (stdio proxy) — `~/.codex/config.toml`:

```toml
[mcp_servers.tela]
command = "npx"
args = ["-y", "tela-mcp"]
env = { TELA_BASE_URL = "https://telawiki.com", TELA_API_KEY = "tela_pat_xxxxxxxx" }
```

A machine-discovery manifest is published at [`/.well-known/mcp.json`](https://telawiki.com/.well-known/mcp.json).

## Screenshots

Ranked full-text search across every space you can read, with the matching line in context:

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/submission-assets/tela-search-dark.png">
  <img alt="tela — search: ranked results for “shipment” across the Operations, Engineering, and Product spaces, each with its breadcrumb and the matched term highlighted in context" src="docs/submission-assets/tela-search-light.png" width="820">
</picture>

Atlas documents a repo and then audits its own coverage — 6/6 must-cover surfaces documented, 42 citations, and the exact file:line gaps it hasn't covered:

![Atlas coverage audit: 13/19 surface covered, 6/6 must-cover documented, 42 citations with 0 unresolved, and a list of undocumented exports with their file:line locations](docs/demo-atlas-audit.png)

## Architecture

- **Backend** — Go (module `github.com/zcag/tela/backend`, entry `cmd/tela`). Hand-written `database/sql` over the `pgx/v5` stdlib driver — no ORM, no sqlc. Embedded, forward-only SQL migrations run automatically on boot.
- **Database** — PostgreSQL 17 with the `pgvector` extension (`pgvector/pgvector:pg17`). FTS lives in `pages.search_tsv` (ranked `ts_rank_cd`); semantic chunks live in `page_chunks.embedding vector(1024)`.
- **Frontend** — React 19 + Vite + TypeScript + Tailwind v4 + Radix + a Milkdown (`@milkdown/kit`) editor, with TanStack Query/Router, Orama, cmdk, and Storybook. Owned, token-driven UI components only.
- **Live collaboration** — Yjs + y-prosemirror over a custom WebSocket transport, scoped tightly to `src/lib/collab/*` and the collab branch of the editor; it rebases onto the canonical markdown on save.
- **Built-in MCP** — the tool/resource surface lives in the Go backend (`internal/api/mcp*.go`) and calls the same core functions the REST routes do, so there is one implementation. `mcp/` is a dumb stdio↔HTTP pipe published as `tela-mcp` on npm.
- **Atlas** — the documentation engine that drafts and maintains wiki pages from your sources, sharing the configured LLM endpoint.
- **Render sidecars** — Gotenberg for HTML→PDF export; a Slidev deck sidecar for presentation pages.
- **Edge** — Caddy serves the SPA, the API, the marketing landing at the apex, and (in direct-TLS mode) on-demand certificates for org custom domains.

Deeper internals, ops, and gotchas live in [`docs/`](docs/) — start with [`docs/architecture.md`](docs/architecture.md), and [`docs/decisions.md`](docs/decisions.md) for the rationale (PostgreSQL, custom collab transport, MCP-as-thin-client).

## Self-host vs cloud

- **Self-host** — run the whole stack with `make up` (or `docker compose`). You own the data, the markdown, and the Postgres volume; AI features are bring-your-own-endpoint (or the bundled Ollama profile). The split/registry deploy topology for shared-edge boxes is in `docs/deploy.md`.
- **Cloud** — a managed instance is hosted at **[telawiki.com](https://telawiki.com)** with a free tier, plus optional managed semantic search and ask-your-docs so you don't have to run an embedder or LLM yourself.

Both run the same code from this repository.

## Contributing

- Commit format: `type(scope): summary` (e.g. `feat(backend): hybrid chunk search`). Concise messages, no co-author trailer.
- **No issue/task tracker** — please don't open GitHub issues or reference `#NNN`. Discuss via pull requests.
- Backend changes use hand-written SQL and a new forward-only `NNNN_name.sql` migration (never edit an applied one). Frontend changes use owned Radix/token-based primitives — no hardcoded hex/px, no third-party component kits.
- Run `make test` (backend) and `npm run build` in `frontend/` before sending a change. See [`CLAUDE.md`](CLAUDE.md) and [`docs/`](docs/) for the full conventions.

## Security

Please report security issues **privately** to **tela@telawiki.com**. Do **not** open a public issue or PR for a vulnerability. Note that a missing or rotated `TELA_API_KEY_SECRET` / `TELA_SHARE_SECRET` leads to forgeable tokens — keep them set and stable.

## License

tela is **open core**. Copyright © tela contributors. The **Community core — the whole product** — is licensed under the [GNU Affero General Public License v3.0](LICENSE) (AGPL-3.0): self-host, modify, and redistribute under its terms (run a modified version as a network service and you must offer your users the corresponding source). The `tela-mcp` npm package is published under `AGPL-3.0-only`. For a **commercial license** without AGPL obligations (e.g. to embed or offer tela as a closed service), contact the maintainer.

The **Enterprise Edition** (`backend/internal/ee/`, source-available, **not** AGPL) adds the company-of-record layer (SSO, audit, SCIM, governance) and requires a license key for production use — see [`backend/internal/ee/LICENSE.md`](backend/internal/ee/LICENSE.md). Full structure in [`docs/licensing.md`](docs/licensing.md).

**"tela", the tela name, and the tela logo are trademarks** and are **not** licensed under the AGPL — see [`TRADEMARK.md`](TRADEMARK.md). You may run and fork the code, but you may not use the tela branding for a redistributed or hosted version without permission.
