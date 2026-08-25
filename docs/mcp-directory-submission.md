# Getting tela listed in the Claude & ChatGPT directories

Standalone working doc for submitting the tela MCP server (`https://telawiki.com/api/mcp`)
to the **Claude connector directory** and the **ChatGPT apps directory** so users
discover it inside the host UI (not just manual custom-connector add). Researched
from primary sources (mid-2026). Action items are checkboxes; flagged items need
live verification.

The separate Claude Code **plugin** channel — same endpoint, different shelf — is in
[`claude-code-plugin.md`](claude-code-plugin.md).

**tela's current state (what we already have):** Streamable HTTP via the official
MCP Go SDK; OAuth 2.1 via WorkOS AuthKit (PKCE S256, DCR/CIMD, user-consent);
45 tools (20 when this was written; re-counted 2026-08-25) — all with `title` +
read/write annotations, ≤15-char names, read and write cleanly separated, no behavioral directives; typed output schemas; 2
resource templates (`tela://page/{id}`, `tela://space/{id}`); 2 interactive MCP
Apps widgets (page-reader + search-results cards); `search`+`fetch` Deep-Research
pair; full-bleed connector icon + server branding.

---

## Status (2026-06-05, PM — WorkOS recreated, OAuth back UP)
- ✅ **OAuth Connect chain LIVE & verified end-to-end.** WorkOS team was recreated → new issuer **`https://decisive-relation-32-staging.authkit.app`** (old `pleasing-puzzle-31` is dead — that's all last session's "down" was). Verified live: PRM → AS discovery 200 · DCR `POST /oauth2/register` → **201 + client_id, accepts `https://claude.ai/api/mcp/auth_callback`** · PKCE **S256** · grants `authorization_code`+`refresh_token` (no `client_credentials`) · `/api/mcp` 401 → correct `WWW-Authenticate` → path-suffixed PRM 200 (CORS `*`) · authorize 302 → tela Standalone bridge `/oauth/workos/login`. **The exact DCR step that failed last session now works.**
- ✅ **Privacy policy + public MCP docs pages LIVE** at https://telawiki.com/privacy/ and /mcp/ (deployed 86ee942).
- ✅ **Code fixes shipped (prod @ 86ee942):** all 20 tools now carry explicit `openWorldHint`/`destructiveHint` (the SDK omitempty-default had them advertising as open-world/destructive — a directory-reject trigger); `get_page`/`fetch` bodies capped at 80k chars (~20k tokens) so a huge page can't blow the ~25k-token result budget; `mcp_test` guards the openWorldHint roster.
- ✅ **MCP Inspector pass (live, 86ee942):** `tools/list` shows the correct hint matrix over the wire (reads=readOnly+closed, import_mira the only open-world, deletes destructive, updates/move idempotent); `tools/call list_spaces` OK; both resource templates + all 4 widget resources advertised. Write tools proven by Go `TestMCP_WriteTools` over the real transport.
- ✅ **Privacy + docs LIVE:** https://telawiki.com/privacy/ and /mcp/ both 200 with real content.
- ✅ **Branding asset:** `https://telawiki.com/icon-512.png` (512²) + `/favicon.svg` already served — covers the logo upload.
- ✅ **Submission payloads drafted:** `docs/mcp-submission-claude.md`, `docs/mcp-submission-chatgpt.md`.
- ✅ **Demo account CREATED + populated:** `mcp-demo` / `mcp-demo@cagdas.io` (email-verified directly in the prod DB, no MFA, not admin); space "Demo" (id 4) seeded with 5 pages. Reusable seed: `scripts/seed-demo.py`. Password held outside the repo (paste into the submission form).
- ✅ **Cloudflare allowlist APPLIED & live** — ruleset `0b545114` (skip WAF/rate-limit/bot for `160.79.104.0/21` on `telawiki.com`); terraform in zcag/dotty, state drift-free.
- 🟡 **CAGDAS (in progress):** OpenAI org identity verification at `platform.openai.com/settings/organization/general` (confirmed correct page; gear-icon/org-name top-right, not the consumer ChatGPT UI) — **Cagdas continuing later**. Then: `api.apps.write` (org owner), global/non-EU residency project, final form submits (Claude + ChatGPT).
- ✅ **Widgets WORKING & verified in-host** (deployed `82d4ef6`): `get_page` renders the page-reader, `search` renders the result cards; both advertise widget `_meta` live. Only the ChatGPT submission *screenshots* of them remain.
- ⬜ **Remaining for ChatGPT:** OpenAI org identity verification (in progress) + global-residency project; capture widget screenshots; final submit. (None block Claude.)
- **Critical path now:** **Claude is fully unblocked — submit it** at `clau.de/mcp-directory-submission` (paste the demo password into the form). Then ChatGPT: finish OpenAI org verification + global-residency project + widget screenshots → submit at `platform.openai.com/apps-manage`.

---

## TL;DR — what's actually blocking each

| | Claude | ChatGPT |
|---|---|---|
| Widgets required? | **No** (tools-only is listable) | **Effectively yes** (have them now) |
| Code blockers | none — tela is code-complete | CSP/`openWorldHint`/outputSchema audit |
| **Non-code blockers** | **privacy policy URL · public docs page · test account · WAF allowlist · MCP Inspector pass** | **org identity verification · privacy policy · no-MFA demo account · screenshots · web+mobile test · global-residency project** |
| Submission | `clau.de/mcp-directory-submission` | `platform.openai.com/apps-manage` (dashboard) |
| Front-page placement | review-gated, attainable | **discretionary** "enhanced distribution" — submission only gets searchable/direct-link |

**Recommended order:** do the shared non-code items once (privacy policy, public
docs page, populated demo account, finalize branding) → submit **Claude first**
(lowest barrier, no widgets) → then ChatGPT (more gates, widgets help).

---

## HOST 1 — Claude (Anthropic connector directory)

**Submission:** `https://clau.de/mcp-directory-submission` (remote-MCP form, covers MCP Apps). Escalation/firewall/status: `mcp-review@anthropic.com`. The form is always open; a native Claude.ai submission surface + status dashboard are rolling out.

**Submission-form payload to prepare:** server name, URL (`https://telawiki.com/api/mcp`), tagline, description, use cases; auth type + transport + read/write caps; optional Allowed Link URIs; data/compliance (third-party connections, health-data, category); full tool/resource list with human-readable names + confirmation of annotations; docs + privacy-policy + support-channel links; **test account with step-by-step setup**; GA date + tested surfaces; branding (logo URL/SVG, favicon verification; MCP-App screenshots if listing the widgets).

### Hard requirements + tela status
- [x] **Transport** Streamable HTTP (SSE deprecated) — *met*.
- [x] **OAuth 2.1 + PKCE S256, user-consent, no `client_credentials`; `/token` accepts `application/x-www-form-urlencoded`; DCR** — **VERIFIED LIVE 2026-06-05** against issuer `decisive-relation-32-staging.authkit.app`: DCR 201 + accepts the Claude redirect URI, S256, form token endpoint, correct grant set. (DCR registers redirect URIs dynamically — no manual per-host registration needed; `claude.com` callback registers the same way.)
- [x] **Per-tool `title` + `readOnlyHint`/`destructiveHint`; no read/write mixing; names ≤64** — *met* (audited: 20 tools, ≤15 chars, clean split).
- [x] **Narrow descriptions, no behavioral directives, no Claude-memory access** — *met* (descriptions are factual; nothing reads chat history/memory).
- [x] **First-party API, server domain matches service** — *met* (telawiki.com).
- [x] **Actionable errors, sized responses (≤25k tokens tool result)** — error payloads carry `{error,code,status}`; `get_page`/`fetch` now cap bodies at 80k chars (~20k tokens) with a `truncated` flag + pointer to `read_chunk`/`research` (shipped 86ee942).
- [x] **Reachable from Anthropic egress `160.79.104.0/21`** — Cloudflare skip-rule APPLIED & live (ruleset `0b545114`, terraform in zcag/dotty).
- [x] **Privacy policy at a public HTTPS URL** — LIVE at https://telawiki.com/privacy/.
- [x] **Public documentation page** — LIVE at https://telawiki.com/mcp/.
- [x] **Test account with sample data + setup steps** — `mcp-demo` / `mcp-demo@cagdas.io`, email-verified, no MFA; space "Demo" with 5 pages (see `mcp-submission-claude.md` §7). Password out-of-repo.
- [x] **Branding: logo SVG/URL + favicon verification** — connector icon (data-URI) + `https://telawiki.com/icon-512.png` (512²) + `/favicon.svg` all served.
- [x] **MCP Inspector pass** — done live (86ee942): `tools/list` hint matrix correct over the wire, `tools/call list_spaces` OK, resource templates + 4 widget resources advertised; write tools proven by Go `TestMCP_WriteTools`. (Re-run as a custom connector once a demo login exists.)

**Review:** reviewers functionally test every tool + run a policy scan; timeline varies (no SLA). Top rejections: missing/mismatched annotations, read+write in one tool, vague/behavioral descriptions, **WAF blocking egress during OAuth**, JSON-only `/token`, generic errors, **empty test accounts**.

**Gating/flags (unverified from primary sources):** free-plan vs paid directory *visibility* tiers and enterprise-admin listing gating weren't documented; no formal "verified/featured badge" program confirmed (directory is "vetted/reviewed"). MCP **Apps** (interactive UI) is an optional category needing *extra* carousel screenshots — opt in if we want the widgets featured.

**No fee, no business entity required** (agree to the Anthropic Software Directory Terms + Policy).

---

## HOST 2 — ChatGPT (OpenAI Apps SDK / app directory)

**Submission:** build/test in **Developer Mode**, then submit via the **Platform Dashboard** at `https://platform.openai.com/apps-manage` (no public web form). On publish, OpenAI auto-creates a Codex plugin. Guidelines: `developers.openai.com/apps-sdk/app-submission-guidelines` + `/deploy/submission`.

### Hard requirements + tela status
- [ ] **Identity verification** in the Platform Dashboard (individual *or* business) for the publish name — **BLOCKING** (publishing under an unverified name = rejection).
- [ ] **`api.apps.write`** permission to submit (org owners have it).
- [x] Public MCP server — *met*.
- [x] **CSP defined** — VERIFIED: ChatGPT expects `openai/widgetCSP` with snake_case `connect_domains`/`resource_domains`; `mcp_widgets.go` sets exactly that. No change needed (see `mcp-submission-chatgpt.md` finding A).
- [ ] **OAuth with a fully-featured demo account, NO 2FA/SMS/email-verification/new-signup** — WorkOS OAuth is fine, but **provide reviewers a plain populated demo login** that doesn't force MFA/email steps.
- [x] **Verb-led unique tool names; descriptions match; no fair-play / model-steering text** — *met* (audit clean).
- [x] **`readOnlyHint`/`destructiveHint`/`openWorldHint` + per-tool justification** — all 20 tools set explicit hints (shipped 86ee942, verified live); 20 justification lines drafted in `mcp-submission-chatgpt.md`. Only `import_mira` is open-world.
- [x] **`outputSchema` for structured tools** — *met* (typed Out on every tool).
- [x] **Privacy policy** (published; data categories/purposes/recipients/retention) — built at `/privacy` (pending deploy). States no PCI/PHI/gov-ID/secrets collection.
- [ ] **App name/logo/screenshots (required dims) + test prompts** that **pass on ChatGPT web AND mobile** — **BLOCKING.**
- [~] **Interactive widget / "meaningful interaction"** — guidelines reject "static frames with no meaningful interaction"; we now have widgets → satisfied (strongly recommended, not a literal hard gate).
- [ ] **Global (non-EU) data-residency submitting project** — EU-residency projects can't submit; create/use a global one.
- [x] **Not "primarily an unofficial connector"** — *met* (first-party).
- [x] **No in-app digital subscriptions / no ads** — *met* (don't add subscription upsell in the app).
- [ ] **`search`+`fetch`** — only needed for Deep Research / company-knowledge (have them); not required for a general directory app.

**Review:** automated scans + manual; status/email via Dashboard; no timeline. Common rejections: can't connect / test creds need MFA / creds expired; **test cases fail on web or mobile**; returns data not disclosed in privacy policy; annotation hints don't match behavior. Appeals: reply to the rejection email.

### ⚠️ The critical caveat — approval ≠ directory placement
After approval you **Publish**, which makes the app **searchable by exact name + direct-link only**. Appearing on the App Directory **main/browse pages** requires **"enhanced distribution,"** which is **selectively granted** ("few apps will receive it"), with **no request process**. So you can't directly "submit to get front-page listed" on ChatGPT — you submit → approve → publish → and OpenAI *chooses* featured apps based on real-world utility + satisfaction (interactive widgets + a strong use case help your odds).

**Press:** coordinate with `press@openai.com` before announcing. No fee; individual or business verification (no entity strictly required).

---

## Shared do-once checklist (both hosts)
- [x] **Privacy policy** at a public HTTPS URL — **BUILT** at `/privacy` (`landing/src/pages/privacy.astro`); covers what tela's MCP reads/writes, recipients = WorkOS + the host, retention. ⏳ goes live on `make deploy-landing`.
- [x] **Public documentation page** — **BUILT** at `/mcp` (`landing/src/pages/mcp.astro`); endpoint, transport, auth, the 20 tools, resources, widgets, how to connect (Claude/ChatGPT/npm proxy). ⏳ goes live on `make deploy-landing`.
- [ ] **Populated demo account** with sample spaces/pages + step-by-step reviewer setup; login path with **no MFA**.  ← blocks BOTH
- [ ] **Branding**: logo (SVG/URL), verify favicon, tagline, description; screenshots of the widgets for the listings.
- [ ] **Cloudflare**: allowlist Anthropic egress `160.79.104.0/21` (Claude reviewer + runtime reachability).
- [ ] **MCP Inspector** pass over all 20 tools + 4 resources + 2 widgets.

## Recommended order of operations
1. **Shared do-once items** above (privacy policy + public docs + demo account + branding).
2. **Claude submit** — register the redirect URI in WorkOS, run MCP Inspector, test as a custom connector, submit `clau.de/mcp-directory-submission`. (No widgets gate; tela is otherwise code-complete.)
3. **ChatGPT submit** — org/identity verification, global-residency project, CSP + `openWorldHint` + justifications, no-MFA demo, screenshots + web/mobile test, submit via `platform.openai.com/apps-manage`. Expect searchable/direct-link; widgets + strong utility are the lever for discretionary front-page placement.

## Flagged for live verification (sources were TLS-flaky / silent)
- Claude: free-plan vs paid directory **visibility** + enterprise-admin listing gating; a formal "verified badge" program.
- ChatGPT: exact CSP `_meta` key/casing the submission expects (`_meta.ui.csp` vs `openai/widgetCSP`); whether a widget is *literally* mandatory; current EEA/UK/CH end-user Apps availability.

**Sources:** claude.com/docs/connectors/building/{submission,review-criteria,authentication} · support.claude.com/.../anthropic-software-directory-policy · platform.claude.com/docs/en/api/ip-addresses · developers.openai.com/apps-sdk/{app-submission-guidelines,deploy/submission}.
