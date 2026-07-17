# Self-hosting tela

tela self-hosts as a Docker Compose stack: Postgres, the Go backend, the static
frontend, an optional embedder, and a Caddy reverse proxy. You need **Docker
with Compose** — and nothing else on the host (the marketing landing is built
inside the proxy image, so no Node toolchain is required).

> tela is AGPL-3.0. You may run, modify, and redistribute it under those terms;
> see [`../LICENSE`](../LICENSE) and [`../TRADEMARK.md`](../TRADEMARK.md).

## Quick start

```bash
git clone https://github.com/zcag/tela && cd tela
make setup            # writes deploy/.env from the example, with generated secrets
$EDITOR deploy/.env   # set TELA_PUBLIC_BASE_URL, TELA_ADMIN_*, and (for multi-user) TELA_SMTP_*
make up               # build + start the stack
make logs             # watch boot; the bootstrap admin password is printed once here
```

The stack publishes host port **8780** (Caddy → app). Visit
`http://localhost:8780` (or your domain once TLS is set up, below).

## Configuration

Every variable is documented inline in [`deploy/.env.example`](../deploy/.env.example).
The ones you must get right:

| Variable | Why it matters |
|---|---|
| `TELA_PG_PASSWORD` | No default — Postgres won't start without it. `make setup` generates one. |
| `TELA_PUBLIC_BASE_URL` | Drives emailed links, cookie `Secure`, and OAuth audience. **Must match how users reach the instance** (see the cookie gotcha below). |
| `TELA_SHARE_SECRET`, `TELA_API_KEY_SECRET` | HMAC keys for share cookies and PATs. If unset, tela generates and **persists** them on first boot (stable across restarts). Set them explicitly if you want to pin/rotate from the environment. |
| `TELA_ADMIN_USERNAME` / `_PASSWORD` / `_EMAIL` | Optional env path for the first admin, created only when the users table is empty **and `_PASSWORD` is set**. If you leave the admin env unset, the first admin is created instead through the web **setup wizard** at `/setup` — a fresh instance redirects there automatically. Setting `_PASSWORD` skips the wizard and seeds the admin at boot (set `_EMAIL` too for a working password-reset address). |
| `TELA_SMTP_*` | Required for a usable multi-user instance — see below. |

The backend logs its effective config at boot (`config: …` lines): the public
base URL, whether cookies are `Secure`, the SMTP mode, and the RAG embedder
target. Check these first when something behaves unexpectedly.

## TLS

tela's proxy defaults to plain HTTP on `:80` (mapped to host `:8780`) — correct
when an **external terminator** (Cloudflare, a load balancer, another reverse
proxy) handles HTTPS. Two modes:

### Behind a terminator (default)

Point your terminator at the host's `:8780`. Set
`TELA_PUBLIC_BASE_URL=https://your.domain` (the public HTTPS URL). Nothing else
to change.

### Direct TLS (no terminator)

Let Caddy provision a Let's Encrypt certificate itself:

1. In `deploy/.env`, set `TELA_SITE_ADDRESS=your.domain`.
2. In [`deploy/docker-compose.yml`](../deploy/docker-compose.yml), under the
   `proxy` service, uncomment the `80:80` and `443:443` port lines (Caddy needs
   both — `:80` for the ACME challenge, `:443` for HTTPS).
3. Set `TELA_PUBLIC_BASE_URL=https://your.domain` and `make up`.

> **Cookie gotcha.** The login cookie gets the `Secure` flag only when
> `TELA_PUBLIC_BASE_URL` starts with `https://`. If you set an `https://` base
> URL but actually serve plain HTTP, browsers silently drop the cookie and
> **login appears to do nothing**. Keep the base URL's scheme matched to how
> users actually connect. The boot log prints `cookie_secure=true|false` so you
> can verify.

## Email (SMTP)

With `TELA_SMTP_HOST` unset, tela **logs** verification/reset links to stdout
instead of sending them. That's fine for a single-admin instance, but
**open self-registration is unusable without SMTP** — a new user's confirmation
link only appears in the server log, and login is blocked until the email is
verified. For any multi-user instance, configure `TELA_SMTP_*`. The boot log
warns when SMTP is unset.

If you can't provide SMTP, ship with self-registration closed and create users
from the admin UI instead — set `TELA_REGISTRATION_OPEN=false` to seed a closed
default on first boot (a later admin toggle overrides it). Packaged deploys with
no SMTP path (e.g. the Umbrel app) do exactly this.

## Semantic search (RAG) — optional

Full-text search works out of the box. Semantic ("ask your docs") search needs
an embedder; it ships **dark** (returns 503) until you point
`TELA_RAG_EMBED_URL` at one.

To self-host the embedder with the bundled Ollama:

```bash
docker compose -f deploy/docker-compose.yml --profile embed up -d
docker compose -f deploy/docker-compose.yml exec ollama ollama pull qwen3-embedding:0.6b
# then in deploy/.env:
#   TELA_RAG_EMBED_URL=http://ollama:11434
make up
```

The embedding model **must be 1024-dimensional** (`page_chunks.embedding` is
`vector(1024)`); `qwen3-embedding:0.6b` is and is the default. Changing the
model means re-embedding everything: after editing the model, run
`docker compose -f deploy/docker-compose.yml exec backend /tela reindex-all`.

### Managed embedder (tela cloud)

Don't want to run an embedder at all? Point at tela cloud's managed endpoint
with a telawiki.com PAT (requires a plan that includes managed semantic
search):

```bash
# in deploy/.env
TELA_RAG_EMBED_URL=https://telawiki.com/api/cloud/ollama
TELA_RAG_EMBED_TOKEN=tela_pat_xxxxxxxx
TELA_RAG_EMBED_MODEL=qwen3-embedding:0.6b   # match the cloud's model/dim
```

This is the same embedder client — it just calls tela cloud's managed embed
proxy instead of a local Ollama. Your instance stays fully open-source; only the
embedding compute is offloaded. Everything else (search, indexing, storage) runs
locally as before.

### Failover with a relief endpoint — optional

To keep AI working when an endpoint clogs (rate-limited / slow / down), put the
opt-in **LiteLLM relief proxy** in front of chat + embeddings: each gets a
primary and a relief endpoint, and traffic fails over automatically. Set the
endpoints in `deploy/.env` (the *AI relief proxy* block in `.env.example`), then
`make up-relief`. The admin **Settings → Insights → AI endpoints & reliability**
card shows each service's live health and whether it's behind a relief pool. Full
detail: [operations.md → AI reliability & failover](operations.md).

## Backups

The sanctioned backup path dumps Postgres (all your wiki data lives there):

```bash
make backup                                   # → ./backups/tela-<timestamp>.sql
make restore FILE=backups/tela-20260608-1200.sql
```

`make backup` runs `pg_dump` inside the postgres container (no host credentials
needed). Restores use `--clean --if-exists`, so they drop and recreate objects.
For a live instance, accept a momentary inconsistent snapshot or stop writers
while dumping. Automate `make backup` with cron for real deployments.

## Upgrades

```bash
git pull
make up        # rebuilds images; migrations run automatically on backend boot
```

Migrations are **forward-only** and run in-transaction on every boot; a failed
migration aborts startup (your data is untouched). There are no down-migrations,
so **`make backup` before upgrading.**

## Operations

Day-2 operations — admin tasks, health checks, recovering admin access, resetting
the database — are in [`operations.md`](operations.md).
