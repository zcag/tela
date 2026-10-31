# The self-host edge: Caddy with tela's routing baked in. It carries NO marketing
# landing: a self-hosted instance's "/" is the app (login when signed out), never
# telawiki.com's sales page. telawiki.com's own edge serves the landing from a
# host directory it points TELA_LANDING_ROOT at (`make deploy-landing`).
#
# Build context is the repo root (see docker-compose.yml: context: ..), so the
# COPY paths are repo-relative. The Caddyfile itself stays bind-mounted by
# compose, so config tweaks don't need an image rebuild, but they DO need a
# container recreate to load (Caddy holds the config in memory and we run no
# file-watcher): recreate via the standalone stack (`make up` / `docker compose
# up -d proxy`).
FROM caddy:2-alpine
# Bake the config as a DEFAULT so the published image is self-contained for
# image-only deploys (one-click app stores: Umbrel/CasaOS/Render/DO/etc.) that
# can't bind-mount the repo. The standalone compose bind-mounts ./proxy/Caddyfile
# + ./proxy/sites.caddy read-only over these, so `make up` is unchanged: the
# bind-mount wins; this only takes effect when nothing is mounted.
COPY deploy/proxy/Caddyfile /etc/caddy/Caddyfile
COPY deploy/proxy/sites.caddy /etc/caddy/sites.caddy
