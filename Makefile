# Optional, untracked make-var overrides (deploy host/paths for the remote-deploy
# targets). Keeps personal values out of git — see the Remote deploy section.
-include deploy/deploy.env

# Use BuildKit for every docker build below (the legacy builder is serial and
# can't use the `RUN --mount=type=cache` mounts the Dockerfiles rely on for the
# Go module/build cache + npm cache). `export` so it reaches every recipe shell,
# including compose builds. Docker's daemon has BuildKit built in — no buildx
# plugin needed.
export DOCKER_BUILDKIT := 1

COMPOSE := docker compose -f deploy/docker-compose.yml

# Auto-stamp git metadata into the backend image so GET /api/version reports
# real values instead of dev/unknown. `?=` defers to operator overrides (env
# or deploy/.env). `git describe --tags --always --dirty` falls back to a
# short SHA when no tags exist; the `|| echo` covers detached / non-git checkouts.
TELA_VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
TELA_COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
# Flatten to simply-expanded so the shell runs ONCE per `make` invocation, not on
# every $(TELA_COMMIT) reference. Otherwise a commit landing mid-deploy moves HEAD
# and the build / push / health-gate end up disagreeing on the tag. A command-line
# override (TELA_COMMIT=…) still wins — it takes precedence over these assignments,
# which is how deploy pins the child `_push`/`health-gate` makes to one commit.
TELA_VERSION := $(TELA_VERSION)
TELA_COMMIT  := $(TELA_COMMIT)
EXPORT_BUILD := TELA_VERSION=$(TELA_VERSION) TELA_COMMIT=$(TELA_COMMIT)

# ── Local dev / test Postgres ───────────────────────────────────────────────
# The backend now requires Postgres (no more file SQLite). `dev-db` boots a
# single throwaway container shared by `be-dev` and `make test`; it persists
# between runs (just `docker start` if it already exists). `make test` provisions
# isolated throwaway databases on it (see internal/testdb), so it never collides
# with the `tela` dev database. Port 55433 avoids clashing with a host Postgres.
DEV_PG_CONTAINER := tela-dev-pg
DEV_DATABASE_URL := postgres://tela:tela@localhost:55433/tela?sslmode=disable
# Dev backend port. Override (`make dev DEV_BE_PORT=18080`) when something
# else on the box already holds :8080 — be-dev binds it and fe-dev points the
# vite /api proxy at it, so the pair stays consistent.
DEV_BE_PORT ?= 8080
# Maintenance DSN the test harness connects to in order to CREATE/DROP per-test DBs.
TEST_DATABASE_URL := postgres://tela:tela@localhost:55433/postgres?sslmode=disable

# ── Deploy (split topology, build-local + on-box registry) ──────────────────
# ONE command: `make deploy` (build here → push changed layers to the on-box
# registry over an SSH tunnel → box pulls + recreates → health-gate). Partial
# pushes: `deploy-backend` / `deploy-frontend` / `deploy-landing`. Offline
# fallback (no registry, full-image `docker save`): `deploy-offline`. Full design
# + the "move the registry elsewhere" notes are in docs/deploy.md.
#
# Runs FROM ANY machine; set the host + paths in your shell, on the command line
# (`make deploy REMOTE=<host>`), or in untracked deploy/deploy.env.
DEPLOY_HOST ?=
DEPLOY_DIR  ?= ~/proj/tela
PUBLIC_URL  ?= https://telawiki.com

# REMOTE — ssh host. REMOTE_DIR — repo path on it. REMOTE_WEB — dir the external
# shared edge serves static from (landing + sites.caddy ride it). EDGE_CONTAINER,
# if set, is reloaded after the sync so the edge re-reads sites.caddy.
REMOTE         ?= $(DEPLOY_HOST)
REMOTE_DIR     ?= $(DEPLOY_DIR)
REMOTE_WEB     ?= /srv/web
EDGE_CONTAINER ?=

# Image registry. TELA_REGISTRY is the host:port prefix images are tagged/pushed
# under; default is the on-box loopback registry (docker-compose.registry.yml),
# reached via an SSH tunnel (REG_TUNNEL=1). To move to a real registry (e.g.
# GHCR), set TELA_REGISTRY=ghcr.io/zcag + REG_TUNNEL=0 in deploy/deploy.env — the
# rest of the flow is unchanged. Images are tagged :<commit> (immutable, for
# rollback) and :latest. Layer-deduped push = only changed layers cross the wire.
TELA_REGISTRY      ?= 127.0.0.1:5000
TELA_REGISTRY_PORT ?= 5000
REG_TUNNEL         ?= 1
REG_SOCK           := /tmp/tela-registry-tunnel.sock
SPLIT              := docker compose -f deploy/docker-compose.split.yml
# Image refs the box's compose resolves to (pinned to this checkout's commit).
DEPLOY_IMAGE_ENV   = TELA_BACKEND_IMAGE=$(TELA_REGISTRY)/tela-backend:$(TELA_COMMIT) \
                     TELA_FRONTEND_IMAGE=$(TELA_REGISTRY)/tela-frontend:$(TELA_COMMIT) \
                     TELA_DECK_IMAGE=$(TELA_REGISTRY)/tela-deck:$(TELA_COMMIT)

.PHONY: up down logs build clean dev be-dev fe-dev storybook help test-mcp-integration \
        test dev-db dev-db-clean setup backup restore blocks-gen blocks-gate \
        landing-dev landing-build landing-gate landing-clean \
        deploy deploy-backend deploy-frontend deploy-landing deploy-offline registry-up _push \
        release-mcp reset-prod-db health-gate

# setup: first-run convenience. Copies deploy/.env.example → deploy/.env and
# fills the three load-bearing secrets with `openssl rand -hex 32` so a
# self-hoster never ships blank/weak secrets. Refuses to clobber an existing
# .env. Edit the result (admin creds, public base URL, SMTP) before `make up`.
setup:
	@if [ -f deploy/.env ]; then \
	  echo "deploy/.env already exists — not overwriting. Delete it first to regenerate."; exit 1; fi
	@cp deploy/.env.example deploy/.env
	@for key in TELA_API_KEY_SECRET TELA_SHARE_SECRET TELA_PG_PASSWORD; do \
	  secret=$$(openssl rand -hex 32); \
	  sed -i "s|^$$key=.*|$$key=$$secret|" deploy/.env; \
	done
	@echo "Wrote deploy/.env with generated secrets."
	@echo "Next: edit deploy/.env (TELA_ADMIN_*, TELA_PUBLIC_BASE_URL, TELA_SMTP_*), then 'make up'."

help:
	@echo "tela — common targets"
	@echo "  make setup      # first run: write deploy/.env from the example with generated secrets"
	@echo "  make up         # build and start the stack (auto-stamps git version/commit) on http://localhost:8780"
	@echo "  make down       # stop the stack"
	@echo "  make logs       # tail logs from all services"
	@echo "  make backup     # dump Postgres to ./backups/tela-<timestamp>.sql"
	@echo "  make restore FILE=backups/...  # restore a dump"
	@echo "  make build      # rebuild images without starting (auto-stamps git version/commit)"
	@echo "  make clean      # stop and DELETE volumes (requires FORCE=1)"
	@echo "  make dev        # run backend + frontend in dev mode in parallel (no compose)"
	@echo "  make be-dev     # run backend in dev mode (go run; boots a local Postgres)"
	@echo "  make fe-dev     # run frontend in dev mode (vite)"
	@echo "  make test       # backend tests against a throwaway Postgres (boots dev-db)"
	@echo "  make dev-db     # boot the local dev/test Postgres container (:55433)"
	@echo "  make storybook  # run Storybook for the frontend"
	@echo "  make test-mcp-integration  # live MCP <-> backend E2E (boots stack, runs tests, tears down)"
	@echo "  make landing-dev    # run the marketing landing (Astro) dev server on :4321"
	@echo "  make landing-build  # build the static landing into landing/dist/"
	@echo "  make landing-gate   # run the landing production gates (a11y, tokens, motion, lighthouse)"
	@echo ""
	@echo ""
	@echo "Deploy (set REMOTE in deploy/deploy.env; run from any machine):"
	@echo "  make deploy           # THE deploy: build → push changed layers → recreate → verify"
	@echo "  make deploy-backend   # build + push + recreate ONLY the backend, then verify /api/version"
	@echo "  make deploy-frontend  # build + push + recreate ONLY the frontend"
	@echo "  make deploy-landing   # build + sync landing + sites.caddy, reload the edge (no image)"
	@echo "  make deploy-offline   # fallback: no registry, full-image 'docker save | ssh load'"
	@echo "  make registry-up      # ensure the on-box image registry is running (deploy does this)"
	@echo "  make reset-prod-db    # DESTROY + recreate the prod Postgres volume (requires FORCE=1)"
	@echo "  make release-mcp      # publish tela-mcp to npm (BUMP=patch|minor|major, default patch)"

# `up` builds the marketing landing first so the proxy's /srv/landing mount is
# populated (Caddy serves it at the apex). All images — including the proxy,
# which now bakes the landing build (deploy/proxy/Dockerfile) — build via
# compose, so `make up` needs only Docker, no host Node.
#
# The Caddyfile is still a single-file bind mount; compose won't recreate the
# proxy on a Caddyfile-only change, and the mount pins the OLD inode after a
# `git pull` rewrites the file. Force-recreate the proxy so it re-mounts the
# current Caddyfile.
up:
	$(EXPORT_BUILD) $(COMPOSE) up -d --build
	$(COMPOSE) up -d --force-recreate proxy

# Standalone stack WITH local AI (Ollama + auto-pulled models) — Atlas, ask, and
# semantic search work out of the box, no external AI service. See
# deploy/docker-compose.ai.yml. First boot pulls a few GB of models.
up-ai:
	$(EXPORT_BUILD) $(COMPOSE) -f deploy/docker-compose.ai.yml --profile embed up -d --build
	$(COMPOSE) up -d --force-recreate proxy

# Standalone stack WITH the AI relief proxy (LiteLLM) — chat + embeddings fail
# over to a relief endpoint when the primary clogs. Set the primary/relief
# endpoints + LITELLM_MASTER_KEY + TELA_LLM_URL/TELA_RAG_EMBED_URL=http://litellm:4000/v1
# in deploy/.env first (the relief block in .env.example). The `litellm` service
# is profile-gated, so this just turns the profile on. See docs/operations.md.
up-relief:
	$(EXPORT_BUILD) COMPOSE_PROFILES=relief $(COMPOSE) up -d --build
	COMPOSE_PROFILES=relief $(COMPOSE) up -d --force-recreate proxy

down:
	$(COMPOSE) down

logs:
	$(COMPOSE) logs -f --tail=100

# backup: dump the Postgres DB to ./backups/tela-<timestamp>.sql via the running
# postgres container, using the container's own POSTGRES_USER/DB so no host
# credentials are needed. restore: load a dump back (drops + recreates objects;
# the dump is taken with --clean --if-exists). The sanctioned self-host data
# backup/restore path. Stop writers (or accept a slightly inconsistent snapshot)
# while dumping a live instance.
backup:
	@mkdir -p backups
	@out=backups/tela-$$(date +%Y%m%d-%H%M%S).sql; \
	  $(COMPOSE) exec -T postgres sh -c 'pg_dump -U "$$POSTGRES_USER" -d "$$POSTGRES_DB" --no-owner --clean --if-exists' > $$out && \
	  echo "wrote $$out"

restore:
	@test -n "$(FILE)" || { echo "usage: make restore FILE=backups/tela-YYYYMMDD-HHMMSS.sql"; exit 1; }
	@$(COMPOSE) exec -T postgres sh -c 'psql -v ON_ERROR_STOP=1 -U "$$POSTGRES_USER" -d "$$POSTGRES_DB"' < $(FILE) && \
	  echo "restored from $(FILE)"

build:
	$(EXPORT_BUILD) $(COMPOSE) build

clean:
ifeq ($(FORCE),1)
	$(COMPOSE) down -v
else
	@echo "make clean removes named volumes (tela-pgdata — the POSTGRES DATABASE — caddy-data, caddy-config) and destroys all data."
	@echo "Re-run with FORCE=1 to confirm:   make clean FORCE=1"
	@exit 1
endif

# dev-db: idempotently ensure the local dev/test Postgres is running. Starts the
# existing container if present, else creates it. Waits for pg_isready so the
# first `go run` / `go test` doesn't race the server's startup.
dev-db:
	@docker start $(DEV_PG_CONTAINER) >/dev/null 2>&1 || \
	  docker run -d --name $(DEV_PG_CONTAINER) \
	    -e POSTGRES_USER=tela -e POSTGRES_PASSWORD=tela -e POSTGRES_DB=tela \
	    -p 55433:5432 pgvector/pgvector:pg17 >/dev/null
	@echo "waiting for postgres…"; \
	for i in $$(seq 1 30); do \
	  docker exec $(DEV_PG_CONTAINER) pg_isready -U tela -d tela >/dev/null 2>&1 && break; \
	  sleep 0.5; \
	done

be-dev: dev-db
	cd backend && TELA_ADDR=":$(DEV_BE_PORT)" TELA_DATABASE_URL="$(DEV_DATABASE_URL)" go run ./cmd/tela

# Backend unit/integration tests against a real Postgres (the testdb harness
# clones a fresh throwaway database per test). Boots dev-db first. Runs the
# blocks-manifest gate first so a stale agent guide / uncovered block fails CI.
test: blocks-gate dev-db
	cd backend && TELA_TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test ./...

# ── Block authoring manifest (editor slash menu + agent guide source) ───────
# Source of truth: frontend/src/components/app/blocks-manifest.json. blocks-gen
# regenerates the go:embed copy the backend renders into the MCP authoring
# guide; blocks-gate verifies it's in sync AND that every renderer plugin is
# covered (a new block can't ship invisible to agents).
blocks-gen:
	node scripts/blocks-manifest.mjs --write

blocks-gate:
	node scripts/blocks-manifest.mjs --check

# Vendor defter's agent authoring contract (AGENTS.md) into the backend so the
# sheet_authoring_guide MCP surface can go:embed it (go:embed can't reach into
# frontend/node_modules). Single source of truth is the published @defterjs/core
# package — re-run after bumping the @defterjs dep.
sheets-gen:
	cp frontend/node_modules/@defterjs/core/AGENTS.md backend/internal/api/sheet_authoring.md

# Stop + remove the local dev/test Postgres (and its data).
dev-db-clean:
	-docker rm -f $(DEV_PG_CONTAINER)

fe-dev:
	cd frontend && TELA_PROXY_TARGET="http://localhost:$(DEV_BE_PORT)" npm run dev

dev:
	@echo "Starting backend (be-dev) and frontend (fe-dev) in parallel — Ctrl-C stops both."
	@$(MAKE) -j2 be-dev fe-dev

storybook:
	cd frontend && npm run storybook

# ── Marketing landing (standalone Astro in landing/) ────────────────────────
# Separate static build from the app (backend/ + frontend/). Deployed as static
# files; the apex / serves this, the app keeps /login, /spaces, /share, etc.
landing-dev:
	cd landing && npm run dev

landing-build:
	cd landing && PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 npm ci && npm run build

# Depends on landing-build: the gate's Lighthouse pass runs against `astro preview`,
# which serves dist/ and never rebuilds. Without this, the gate silently grades a
# STALE build — it can pass on output that predates your edit, or keep failing on a
# defect you already fixed.
landing-gate: landing-build
	cd landing && npm run gate

landing-clean:
	rm -rf landing/dist landing/node_modules landing/.astro

# M16.C.2 — live integration test for the MCP server against a real backend.
# Boots the stack with -p tela-test (isolated volumes), seeds deterministic
# admin credentials, builds the MCP, runs vitest.integration.config, then
# tears everything down. The `trap` ensures we always clean up even when the
# test step exits non-zero.
TEST_COMPOSE := docker compose -p tela-test -f deploy/docker-compose.yml -f deploy/docker-compose.test.yml
test-mcp-integration:
	@set -eu; \
	TELA_PUBLIC_BASE_URL="http://localhost:18780"; \
	TELA_SHARE_SECRET="$$(openssl rand -hex 32)"; \
	TELA_API_KEY_SECRET="$$(openssl rand -hex 32)"; \
	TELA_ADMIN_USERNAME="testadmin"; \
	TELA_ADMIN_PASSWORD="testpassword123"; \
	export TELA_PUBLIC_BASE_URL TELA_SHARE_SECRET TELA_API_KEY_SECRET TELA_ADMIN_USERNAME TELA_ADMIN_PASSWORD $(EXPORT_BUILD); \
	trap '$(TEST_COMPOSE) down -v --remove-orphans >/dev/null 2>&1 || true' EXIT INT TERM; \
	echo "[test-mcp-integration] booting tela-test stack…"; \
	$(TEST_COMPOSE) up -d --build --wait; \
	echo "[test-mcp-integration] building MCP server (npm ci → prepare → tsc)…"; \
	npm --prefix mcp ci --silent; \
	echo "[test-mcp-integration] running live vitest suite…"; \
	TELA_BASE_URL="$$TELA_PUBLIC_BASE_URL" \
	TELA_ADMIN_USERNAME="$$TELA_ADMIN_USERNAME" \
	TELA_ADMIN_PASSWORD="$$TELA_ADMIN_PASSWORD" \
	  npm --prefix mcp run test:integration

# ── Deploy targets ──────────────────────────────────────────────────────────
# Build-local: images are built on THIS machine and shipped to the box (registry
# push, or `docker save` for deploy-offline). The box only pulls — it never
# builds. registry-up `git pull --ff-only`s the box repo so its compose +
# sites.caddy source is current; a non-fast-forward fails loudly.
#
# PRECONDITION: push your commit before deploying. The health gate compares the
# locally-captured HEAD ($(TELA_COMMIT), the tag the image is built+pushed under)
# against what /api/version reports after the box recreates. If you forgot to
# push, the box's pulled compose lags HEAD and the gate fails — exactly the
# "commit ≠ deploy" trap we want to make impossible.

# health-gate: poll PUBLIC_URL/api/version until the reported `commit` equals
# EXPECT_COMMIT (default: this checkout's HEAD), or fail. This is the permanent
# fix for "pushed but prod still runs the old binary". jq is used when present
# for robust JSON parsing; otherwise we fall back to grep/sed so the gate never
# hard-depends on jq. 12 tries × 5s ≈ 1 min — covers a backend rebuild+restart.
EXPECT_COMMIT ?= $(TELA_COMMIT)
health-gate:
	@set -eu; \
	expect="$(EXPECT_COMMIT)"; \
	echo "[health-gate] expecting commit '$$expect' at $(PUBLIC_URL)/api/version"; \
	for i in $$(seq 1 12); do \
	  body="$$(curl -fsS --max-time 10 "$(PUBLIC_URL)/api/version" 2>/dev/null || true)"; \
	  if [ -n "$$body" ]; then \
	    if command -v jq >/dev/null 2>&1; then \
	      got="$$(printf '%s' "$$body" | jq -r '.commit // empty')"; \
	    else \
	      got="$$(printf '%s' "$$body" | grep -o '"commit"[[:space:]]*:[[:space:]]*"[^"]*"' | sed -E 's/.*:[[:space:]]*"([^"]*)"/\1/')"; \
	    fi; \
	    if [ "$$got" = "$$expect" ]; then \
	      echo "✓ deploy verified — /api/version reports commit $$got"; exit 0; \
	    fi; \
	    echo "  …reported '$$got' (want '$$expect'), retry $$i/12"; \
	  else \
	    echo "  …unreachable, retry $$i/12"; \
	  fi; \
	  sleep 5; \
	done; \
	echo "✗ deploy NOT verified: $(PUBLIC_URL)/api/version never reported commit '$$expect'." >&2; \
	echo "  (Did you push? Or did 'make up' on $(DEPLOY_HOST) fail to rebuild the backend?)" >&2; \
	exit 1

# registry-up: ensure the on-box image registry is running. Pulls the repo first
# so the registry compose file is present, then `up -d` (idempotent — a no-op if
# already running). Every registry-based deploy target depends on this, so the
# box repo is current (compose + sites.caddy source) before the app `up`.
registry-up:
	@test -n "$(REMOTE)" || { echo "set REMOTE=<ssh-host> (or in deploy/deploy.env)"; exit 1; }
	ssh $(REMOTE) 'cd $(REMOTE_DIR) && git pull --ff-only && \
	  TELA_REGISTRY_PORT=$(TELA_REGISTRY_PORT) docker compose -f deploy/docker-compose.registry.yml up -d'

# _push (internal): push $(PUSH_IMAGES) — each tagged :<commit> and :latest — to
# the registry. For the on-box loopback registry (REG_TUNNEL=1) it opens a
# transient SSH tunnel (control-master socket, with keepalives so it can't
# idle-reset) so the laptop's :PORT reaches the box registry, and closes it on
# exit via a shell trap (even if a push fails). REG_TUNNEL=0 pushes straight to
# TELA_REGISTRY (e.g. GHCR) — no tunnel.
#
# Each push is retried: Docker uploads blobs in parallel and over a single SSH
# tunnel that contention can starve a large blob into a reset. A retry skips the
# layers that already landed, so each round has fewer (and bigger-windowed)
# uploads in flight and converges — no daemon max-concurrent-uploads tweak needed.
_push:
	@set -e; \
	if [ "$(REG_TUNNEL)" = "1" ]; then \
	  echo "[push] tunnel :$(TELA_REGISTRY_PORT) → $(REMOTE) registry…"; \
	  ssh -fN -M -S $(REG_SOCK) -o ServerAliveInterval=15 -o ServerAliveCountMax=8 \
	    -o ExitOnForwardFailure=yes \
	    -L $(TELA_REGISTRY_PORT):127.0.0.1:$(TELA_REGISTRY_PORT) $(REMOTE); \
	  trap 'ssh -S $(REG_SOCK) -O exit $(REMOTE) 2>/dev/null || true' EXIT; \
	fi; \
	for img in $(PUSH_IMAGES); do \
	  for tag in $(TELA_COMMIT) latest; do \
	    ref=$(TELA_REGISTRY)/$$img:$$tag; n=1; \
	    until docker push $$ref; do \
	      if [ $$n -ge 6 ]; then echo "[push] $$ref failed after 6 attempts" >&2; exit 1; fi; \
	      echo "[push] $$ref attempt $$n failed — retrying (already-pushed layers skip)…"; \
	      n=$$((n+1)); sleep 3; \
	    done; \
	  done; \
	done

# deploy: THE deploy. Build both images locally → push only changed layers to the
# on-box registry → sync landing + sites.caddy → box pulls the just-pushed tag and
# recreates the split stack → health-gate verifies /api/version reports this commit.
# Push your commit first (the gate compares the box's running commit to local HEAD).
deploy: registry-up
	@echo "[deploy] building backend+frontend+deck ($(TELA_COMMIT))…"
	docker build --build-arg VERSION=$(TELA_VERSION) --build-arg COMMIT=$(TELA_COMMIT) \
	  -t $(TELA_REGISTRY)/tela-backend:$(TELA_COMMIT) -t $(TELA_REGISTRY)/tela-backend:latest backend
	docker build -t $(TELA_REGISTRY)/tela-frontend:$(TELA_COMMIT) -t $(TELA_REGISTRY)/tela-frontend:latest frontend
	docker build -t $(TELA_REGISTRY)/tela-deck:$(TELA_COMMIT) -t $(TELA_REGISTRY)/tela-deck:latest deck
	$(MAKE) landing-build
	@$(MAKE) _push PUSH_IMAGES="tela-backend tela-frontend tela-deck" TELA_COMMIT=$(TELA_COMMIT)
	@echo "[deploy] syncing landing + sites.caddy → $(REMOTE):$(REMOTE_WEB)…"
	ssh $(REMOTE) 'mkdir -p $(REMOTE_WEB)/tela-landing $(REMOTE_WEB)/tela'
	rsync -a --delete landing/dist/ $(REMOTE):$(REMOTE_WEB)/tela-landing/
	rsync -a deploy/proxy/sites.caddy $(REMOTE):$(REMOTE_WEB)/tela/sites.caddy
	@echo "[deploy] recreating split stack @ $(TELA_COMMIT)…"
	ssh $(REMOTE) 'cd $(REMOTE_DIR) && $(DEPLOY_IMAGE_ENV) $(SPLIT) up -d'
	@if [ -n "$(EDGE_CONTAINER)" ]; then \
	  ssh $(REMOTE) 'docker exec $(EDGE_CONTAINER) caddy reload --config /etc/caddy/Caddyfile' || true; fi
	@$(MAKE) health-gate EXPECT_COMMIT=$(TELA_COMMIT)

# deploy-backend: build + push + recreate ONLY the backend (fast path when only Go
# changed). VERSION/COMMIT ldflags stamp the image so /api/version is right and the
# health gate passes. --no-deps is load-bearing: DEPLOY_IMAGE_ENV pins ALL services
# to TELA_COMMIT, but this partial only pushed the backend image — without --no-deps,
# `up backend` would try to pull its deps (deck/frontend) at a tag that doesn't exist
# in the registry and fail. The deps are already running, so recreate backend alone.
deploy-backend: registry-up
	docker build --build-arg VERSION=$(TELA_VERSION) --build-arg COMMIT=$(TELA_COMMIT) \
	  -t $(TELA_REGISTRY)/tela-backend:$(TELA_COMMIT) -t $(TELA_REGISTRY)/tela-backend:latest backend
	@$(MAKE) _push PUSH_IMAGES="tela-backend" TELA_COMMIT=$(TELA_COMMIT)
	ssh $(REMOTE) 'cd $(REMOTE_DIR) && $(DEPLOY_IMAGE_ENV) $(SPLIT) up -d --no-deps backend'
	@$(MAKE) health-gate EXPECT_COMMIT=$(TELA_COMMIT)

# deploy-frontend: build + push + recreate ONLY the frontend. No health gate
# (/api/version reflects the backend, not the FE bundle). --no-deps for the same
# reason as deploy-backend (only the frontend image was pushed at this commit).
deploy-frontend: registry-up
	docker build -t $(TELA_REGISTRY)/tela-frontend:$(TELA_COMMIT) -t $(TELA_REGISTRY)/tela-frontend:latest frontend
	@$(MAKE) _push PUSH_IMAGES="tela-frontend" TELA_COMMIT=$(TELA_COMMIT)
	ssh $(REMOTE) 'cd $(REMOTE_DIR) && $(DEPLOY_IMAGE_ENV) $(SPLIT) up -d --no-deps frontend'

# deploy-deck: build + push + recreate ONLY the deck render sidecar. The fast path when
# only the render pipeline changed — most often a `slidev-theme-tahta` bump in deck/.
# No health gate (/api/version is the backend). --no-deps for the same reason as the
# other partials (only the deck image was pushed at this commit). The deck cache key
# folds in the theme version, so a tahta bump re-renders every deck on next request.
deploy-deck: registry-up
	docker build -t $(TELA_REGISTRY)/tela-deck:$(TELA_COMMIT) -t $(TELA_REGISTRY)/tela-deck:latest deck
	@$(MAKE) _push PUSH_IMAGES="tela-deck" TELA_COMMIT=$(TELA_COMMIT)
	ssh $(REMOTE) 'cd $(REMOTE_DIR) && $(DEPLOY_IMAGE_ENV) $(SPLIT) up -d --no-deps deck'

# deploy-landing: the landing + sites.caddy are static files the EXTERNAL shared
# edge serves/imports from REMOTE_WEB (not an image). Build, rsync, reload the edge.
# This is also the target for a sites.caddy change.
deploy-landing:
	@test -n "$(REMOTE)" || { echo "set REMOTE=<ssh-host> (or in deploy/deploy.env)"; exit 1; }
	$(MAKE) landing-build
	ssh $(REMOTE) 'mkdir -p $(REMOTE_WEB)/tela-landing $(REMOTE_WEB)/tela'
	rsync -a --delete landing/dist/ $(REMOTE):$(REMOTE_WEB)/tela-landing/
	rsync -a deploy/proxy/sites.caddy $(REMOTE):$(REMOTE_WEB)/tela/sites.caddy
	@if [ -n "$(EDGE_CONTAINER)" ]; then \
	  ssh $(REMOTE) 'docker exec $(EDGE_CONTAINER) caddy reload --config /etc/caddy/Caddyfile' || true; fi

# deploy-offline: FALLBACK — no registry. Build locally, stream FULL images with
# `docker save | ssh docker load`, then recreate the split stack with the loaded
# bare-name images (TELA_PULL_POLICY=never so compose uses them as-is). For first
# boot before the registry exists, or if the registry is down. Slow on a thin
# uplink (sends whole images, no layer dedup) — that's the point of `make deploy`.
deploy-offline:
	@test -n "$(REMOTE)" || { echo "set REMOTE=<ssh-host> (or in deploy/deploy.env)"; exit 1; }
	@echo "[deploy-offline] building backend+frontend+deck ($(TELA_COMMIT)) locally…"
	docker build --build-arg VERSION=$(TELA_VERSION) --build-arg COMMIT=$(TELA_COMMIT) -t tela-backend:latest backend
	docker build -t tela-frontend:latest frontend
	docker build -t tela-deck:latest deck
	$(MAKE) landing-build
	@echo "[deploy-offline] streaming full images → $(REMOTE)…"
	docker save tela-backend:latest tela-frontend:latest tela-deck:latest | ssh $(REMOTE) docker load
	ssh $(REMOTE) 'mkdir -p $(REMOTE_WEB)/tela-landing $(REMOTE_WEB)/tela'
	rsync -a --delete landing/dist/ $(REMOTE):$(REMOTE_WEB)/tela-landing/
	rsync -a deploy/proxy/sites.caddy $(REMOTE):$(REMOTE_WEB)/tela/sites.caddy
	ssh $(REMOTE) 'cd $(REMOTE_DIR) && git pull --ff-only && \
	  TELA_BACKEND_IMAGE=tela-backend:latest TELA_FRONTEND_IMAGE=tela-frontend:latest TELA_DECK_IMAGE=tela-deck:latest TELA_PULL_POLICY=never \
	  $(SPLIT) up -d'
	@if [ -n "$(EDGE_CONTAINER)" ]; then \
	  ssh $(REMOTE) 'docker exec $(EDGE_CONTAINER) caddy reload --config /etc/caddy/Caddyfile' || true; fi
	@$(MAKE) health-gate EXPECT_COMMIT=$(TELA_COMMIT)

# reset-prod-db: GUARDED, DESTRUCTIVE. Drops the prod Postgres volume and brings
# it back empty so the backend re-runs migrations from scratch. Prod data is
# disposable by design (pre-1.0); this is the sanctioned reset. Requires FORCE=1,
# same as `clean`. Runs on the split deploy host: stop the backend (releases
# connections) + postgres, delete the named volume, then `up -d` so postgres
# re-initializes and the backend re-migrates against a fresh DB. Recreates from
# the registry images already on the box (compose defaults → :latest); does NOT
# build or push. No commit gate — this isn't a code deploy.
reset-prod-db:
ifeq ($(FORCE),1)
	ssh $(REMOTE) 'cd $(REMOTE_DIR) && \
	  $(SPLIT) stop backend postgres && \
	  $(SPLIT) rm -f postgres && \
	  docker volume rm tela_tela-pgdata && \
	  $(SPLIT) up -d'
else
	@echo "reset-prod-db DESTROYS the production Postgres volume (tela_tela-pgdata) — ALL wiki data is lost."
	@echo "Re-run with FORCE=1 to confirm:   make reset-prod-db FORCE=1"
	@exit 1
endif

# ── MCP npm release (ships to npm, NOT to the deploy host) ──────────────────
# Publishes the tela-mcp package. BUMP selects the semver bump (default patch):
#   make release-mcp BUMP=minor
# Flow (from docs/architecture.md + mcp/README): npm version with the
# tela-mcp-v* tag prefix (so MCP tags don't collide with app tags), publish
# public, then push the commit+tag from repo root.
# HAZARDS (see architecture.md):
#  (1) `npm version` can silently skip — we verify the tag was created and abort
#      if not, rather than publishing an untagged build.
#  (2) npm strips a `bin` value starting with `./` — package.json must use
#      "tela-mcp": "dist/server.js" (no leading ./). We dry-run publish first.
#  (3) registry @latest has CDN lag — verify with `npm view … versions` after.
#  (4) server.js runs its proxy at top level (bin-only entrypoint, no import
#      guard — fine today). If an importable API is ever added, the ESM guard
#      must realpathSync both sides or npx-via-symlink exits 0 silently.
BUMP ?= patch
release-mcp:
	@set -eu; \
	echo "[release-mcp] bumping tela-mcp ($(BUMP)) and publishing to npm…"; \
	cd mcp && \
	new="$$(npm version $(BUMP) --tag-version-prefix=tela-mcp-v)"; \
	echo "[release-mcp] npm version reported: $$new"; \
	if ! git tag --list 'tela-mcp-v*' | grep -qx "tela-mcp-v$${new#tela-mcp-v}"; then \
	  echo "✗ npm version did not create tag $$new — aborting before publish." >&2; exit 1; \
	fi; \
	echo "[release-mcp] dry-run publish (catches bad bin paths / files globs)…"; \
	npm publish --dry-run --access public; \
	npm publish --access public; \
	cd .. && git push --follow-tags; \
	echo "✓ published $$new — verify CDN with: npm view tela-mcp versions --json"
