# tela — API

## Stability contract

The REST API (`/api/*`) is **internal** — it powers the React frontend and the `tela-mcp` stdio proxy. It carries no version prefix and may change between releases without prior notice. Do not build external integrations against it directly.

**The stable integration surface is the MCP server** (`POST /api/mcp`, streamable HTTP or via the `tela-mcp` npm proxy). The MCP tool/resource schema is versioned (`protocolVersion: 2024-11-05`) and is the intended external API for agents, scripts, and third-party tools. Breaking changes to MCP tools will be documented in the [GitHub changelog](https://github.com/zcag/tela).

Exception: `/api/health`, `/api/version`, and `/metrics` are stable and safe to poll.

**Machine-readable description:** [`backend/internal/api/openapi.json`](../backend/internal/api/openapi.json) — an OpenAPI 3.1 document, `go:embed`'d and served at `/openapi.json` on the public origin (Caddy rewrites it to `GET /api/public/openapi.json`; see [`api/openapi.go`](../backend/internal/api/openapi.go)). It covers the integration surface — auth, PATs, spaces, pages, search, comments, attachments, shares, the public read API, feedback — and deliberately omits admin, orgs, billing, Atlas, generative AI, decks, sync and the collab websocket. `openapi_test.go` fails if the spec names a route the mux does not register, so it cannot drift into describing a route that no longer exists; **adding** a route does not fail the gate, so update the spec in the same commit when you change a documented one.

---

Base: `/api`. JSON in/out (imports are `multipart/form-data`). Auth is a session cookie (`tela_session`) **or** `Authorization: Bearer tela_pat_...`. Bearer is checked before the cookie; an invalid bearer returns 401 (no cookie fallback).

## Errors

Baseline envelope: `{ "error": "...", "code": "..." }`. Known codes: `bad_request`, `not_found`, `unauthorized`, `forbidden`, `conflict`, `cycle`, `last_admin`, `last_owner`, `internal`, `viewer_no_write`, `comment_*`, `revision_not_found`, `invalid_query`, `space_not_found`, `password_required`, `rate_limited`, `api_key_scope`, `api_key_space_scope`.

When adding a code that carries extra wire fields beyond `{ error, code }`, document it here **and** check the MCP `safeParseEnvelope`.

## Meta
- `GET /api/health` — liveness.
- `GET /api/version` — `{ version, commit, built_at }`, public, build-stamped.

## Auth
- `POST /api/auth/login` → 200 + cookie, or 401.
- `POST /api/auth/logout` → 204.
- `GET /api/auth/me` → current user.

Middleware bypasses every prefix in `auth.IsPublicPath` — including `/api/health`, `/api/version`, `/api/auth/`, `/api/setup`, `/api/invites/`, `/api/mcp`, `/api/cloud/`, `/api/internal/`, `/api/share/`, `/api/public/`, `/api/files/`, `/api/uploads/`, `/api/images/`, `/api/diagrams/`, `/api/deck/`, `/api/print/`, `/api/billing/webhook`, `/dav/`, `/share/`, `/public/`, `/u/`, `/f/`, `/p/`, and `/.well-known/oauth-protected-resource`. Read the function, not this list, before adding a route under one of them — **anything under a public prefix must self-authenticate.**

## Spaces & membership
- `GET /api/spaces` — spaces the caller can access (direct membership **or** via an org grant; resolved through the `space_access` view).
- `POST /api/spaces` — create (creator becomes owner).
- `GET|PATCH|DELETE /api/spaces/{id}`.
- `GET|POST|PATCH|DELETE` space members under the space (owner-gated; `last_owner` guard). These are **direct user** grants (`space_members`).
- `GET|POST|DELETE /api/spaces/{id}/invites[/{inviteId}]` — share a space **by email** (owner-gated). `POST {email, role}` answers `{member}` when a verified account already owns that address (access granted immediately + `space_added` notification) or `{invite}` when it doesn't (a pending, email-targeted invitation is mailed). The invitee accepts via the shared `/invite/{token}` flow below, or gets the space automatically when they verify a signup with that address. See [`access-model.md`](access-model.md) → *Email invitations*.

## Organizations (#153)
An org is a *grantable principal*: share a space with an org and every member gains the granted role. Access resolves through the `space_access` view = direct user grants ∪ org grants. Slot reserved for future `group` principals (same view, same routes).
- `GET /api/orgs` — caller's orgs (instance-admins see all; each row carries `my_role`, `member_count`).
- `POST /api/orgs` — create. **Instance-admin only.**
- `GET|PATCH|DELETE /api/orgs/{id}` — get/rename (org-admin or instance-admin) / delete (instance-admin only; tears down the org's space_grants).
- `GET|POST|PATCH|DELETE /api/orgs/{id}/members[/{user_id}]` — membership (org-admin or instance-admin; self-leave allowed; `last_admin` guard). Removing a **domain-managed** member (verified email domain maps to the org) is refused with `409 domain_managed` — membership is identity-derived (see access-model.md).
- `GET|POST|PATCH|DELETE /api/orgs/{id}/groups[/{group_id}]` — group sub-teams within an org (org-admin gated; #155).
- `GET|POST|DELETE /api/orgs/{id}/groups/{group_id}/members[/{user_id}]` — group membership (org-admin or self-leave). A user must already be an org member (`409 not_org_member`; DB-enforced). Leaving the org cascades out of its groups.
- `GET /api/groups` — flat list of groups the caller can grant a space to (their orgs' groups; instance-admin: all). Powers the share picker.
- `GET|POST|PATCH|DELETE /api/spaces/{id}/grants[/{grant_id}]` — share a space with a **principal** (`{principal_kind: "org"|"group", principal_id, role}`). **Space owner only**; role limited to `editor`/`viewer` (`owner` reserved for direct users so the last-owner guard stays sound; also enforced by a DB trigger). Grant rows are principal-generic (`principal_name`, `context_name` = parent org for groups).
- `GET /api/spaces/{id}/access` — resolved access list (any member): each user with their **effective role** (max over sources) + **sources** (`direct` / `via <org>`). The authoritative "who can see this, and why".
- `GET|POST|DELETE /api/admin/org-domains[/{domain}]` — auto-join email-domain → org mappings. **Instance-admin only.** Member-only (no per-domain role). A user whose verified email domain matches is enrolled into the org on verify/login (idempotent, best-effort, non-discretionary).
- `GET|POST|DELETE /api/orgs/{id}/invites[/{inviteId}]` — pending email invitations to the org (org-admin gated). Seat quota is enforced at accept time, not at create.
- `GET /api/invites/{token}` — **public** (on `IsPublicPath`, self-authenticates via the token): renders the accept page for a possibly-logged-out invitee. Returns `{valid, kind: "org"|"space", org_name|space_name, inviter, email}`.
- `POST /api/me/accept-invite {token}` — accept, session required. Serves **both** invite kinds; the caller's verified email must match the invited address (`403 email_mismatch`).
- `GET /api/admin/access-audit?limit` — access-control change log (org/membership/grant/auto-join/domain). **Instance-admin only.**

See [`access-model.md`](access-model.md) for the canonical principal/grant/role model, precedence, and the group (sub-team) design.

## Pages
- `GET /api/pages?space_id={id}` — pages in a space (optional `parent_id`; `tree=1` for the nested tree).
- `POST /api/pages` — create (`space_id` in the body).
- `GET /api/pages/{id}` — page (markdown body + metadata; envelope `{ page: ... }`). `?draft=$revId` for owner soft-draft.
- `PATCH /api/pages/{id}` — update title/body/parent/position; snapshots a revision on body/title change.
- `DELETE /api/pages/{id}` — soft delete.
- `GET /api/pages/{id}/revisions` — page history. (cross-page rev → 404 `revision_not_found`.)
- `GET /api/pages/{id}/backlinks` — pages linking here.
- `GET /api/pages/bodies?space_id&...` — bodies for the per-space fuzzy index.
- WebSocket `/ws/...` — live collab (custom 1-byte-tag protocol; see architecture.md).

## Search
- `GET /api/search?q=...` — ranked Postgres full-text (`tsvector` / `ts_rank_cd`) over title + body, snippet-highlighted via `ts_headline`.
- `GET /api/pages/bodies?space_id&since&cursor&limit` — cursor-paginated page bodies for one space, the bulk-read/mirroring endpoint (member-gated, bearer-`read` ok). Limit defaults to 200, clamped to 500. (There is no `/api/search/bodies` — that name is stale.)

## Diagrams (Excalidraw)
- `GET /api/diagrams/{page_id}/{file}` — public, content-addressed, immutable (ETag/304).
- `PUT /api/pages/{id}/diagrams` — editor+, 8 MiB PNG (magic-byte checked), idempotent upsert.

## Import
- `POST /api/spaces/{id}/import` — editor+, `multipart/form-data`: `parent_id`, `dry_run`, `files` (`.md`/zip). Flatten-root + README-as-index handling.

## Public share
- Management (editor+, session): `POST|GET /api/pages/{id}/shares`, `PATCH|DELETE /api/shares/{id}`.
- Public (no session): `GET /api/share/{token}`, `POST /api/share/{token}/auth`, `GET /api/share/{token}/page/{page_id}`, `GET /api/share/{token}/tree`. Identical 404 for missing/revoked/expired. Rate-limited per (token, IP).

## API keys (instance-admin)
- `POST /api/api_keys` → 201 with the raw `key` **once** (`tela_pat_<43 chars>`); stored as HMAC.
- `GET /api/api_keys` → list (prefix only).
- `DELETE /api/api_keys/{id}` → soft-revoke (admin or owner), idempotent 204.
- `GET /api/api_keys/{id}/audit?limit&before` → bearer-auth request log (owner/admin), 30-day retention.

## Feedback
- `POST /api/feedback` — session OR bearer (any scope, incl. `read`). `{ subject, body }` (1–200 / 1–8000) → 201 `{ feedback: {...} }`. Write-only (no GET, no admin UI).

## Machine discovery (well-known paths)

What tela publishes so a crawler, an agent, or a directory can find the API without a human pointing. Two homes, and the split is deliberate: **anything describing the API lives with the backend** (`go:embed`, so a route change and its description are one diff); **anything describing the site lives in `landing/public/`** and ships with `make deploy-landing`.

| Path | Served from | Notes |
| --- | --- | --- |
| `/openapi.json` | backend, `api/openapi.go` → `GET /api/public/openapi.json` | OpenAPI 3.1. Caddy rewrite in `deploy/proxy/sites.caddy`; `servers[0].url` is rewritten to the instance's own origin. Indexable on purpose. |
| `/llms.txt`, `/llms-full.txt` | `landing/public/` | llmstxt.org. Both link to `/openapi.json` — the format has no dedicated field for one, so a link in a `##` section is the convention. |
| `/.well-known/mcp.json` | `landing/public/` | No ratified spec (four competing proposals, none merged), but directory reviewers probe it and it is honest. |
| `/.well-known/oauth-protected-resource[/api/mcp]` | backend, `api/mcp_oauth.go` | RFC 9728. The one file here a major client reads every day — Claude's MCP OAuth handshake. 404s when OAuth is unconfigured. |
| `/.well-known/agent-skills/index.json` | `landing/public/`, generated | Cloudflare agent-skills discovery v0.2.0, read by `npx skills`. The index carries a sha256 of the skill body, so it is **generated** from `plugin/plugins/tela/skills/*/SKILL.md` by `make skills-gen` and gated by `make skills-gate` (run from `make test`). A stale digest hard-fails an install; never hand-edit it. |
| `/.well-known/security.txt` | `landing/public/` | RFC 9116. Only `Contact` and `Expires` are required, and an expired file is treated as invalid — **refresh `Expires` annually**. |
| `/.well-known/glama.json` | Caddy `respond` block | Single-vendor ownership proof for glama.ai. |
| `/robots.txt`, `/sitemap.xml`, `/sitemap-public.xml` | landing / backend | See `public_og.go` for the generated public sitemap. |

Considered and **not** shipped, so nobody re-litigates them:

- `/.well-known/agent-tools.txt` — agent-tools.org defines **no content format** for it; the path is only a carrier for a domain-ownership token you receive after submitting a product. Nothing to write honestly until we self-submit and choose file verification over DNS.
- `/.well-known/agent-card.json` (A2A) — tela is not an A2A agent: no task delegation, no A2A endpoint, and `supportedInterfaces`/`skills` could only be filled with a false claim.
- `/.well-known/ai-plugin.json` — the OpenAI plugin format; the plugin beta shut down 2024-04-09.
- `ai.txt` — three incompatible things share the name and no crawler reads any of them.
- `Content-Signal` in `robots.txt` — no consumer honors it and the IETF draft behind it expired unadopted. The real work (IETF AIPREF `Content-Usage`) has no RFC yet.
- `/.well-known/api-catalog` (RFC 9727) — the only *registered* standard for pointing at an OpenAPI document, and now cheap to add since the spec is real, but the research found no named consumer and a handful of deployers worldwide. Revisit if that changes.
- `/.well-known/ai-catalog.json` — the right container for tela's shape (an MCP server + a skill + docs, without claiming to be an agent), Linux Foundation-backed, but essentially one deployer today. Ship it if the MCP or A2A steering committees adopt it.
- `/.well-known/mcp-registry-auth` — real consumer (the official MCP registry) and would upgrade the server name from `io.github.zcag/tela` to `com.telawiki/tela`. Needs an Ed25519 keypair with a private half to store as a deploy secret, so it is a deliberate operational decision, not a file drop.
