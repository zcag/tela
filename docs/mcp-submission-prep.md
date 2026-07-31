# MCP directory submission — master prep checklist

> ✅ **CLAUDE SUBMITTED — 2026-06-06.** Confirmation page received ("Thank you for indicating your interest in being included in the Anthropic MCP Directory"). No duplicate (verified first-time on Page 6).
> Surfaces attested: Claude.ai web + Claude Desktop + Claude Code. Contact/support: tela@telawiki.com. Review has no SLA; escalation mcp-review@anthropic.com.
>
> ⚠️ **The Google Form is DEPRECATED (confirmed 2026-07-31)** — "no longer accepting responses". Its per-response `edit2=` link is dead and has been removed from this doc (it was a bearer URL sitting in a public repo since `cf30acd`; inert now, but it stays in git history). **The submission surface is now the native dashboard: `https://claude.ai/admin-settings/directory/submissions`** (`clau.de/mcp-directory-submission` 302s there). Note it's an *admin-settings* path, and the review correspondence goes to `cagdas.salur@ngss.io` — so sign in as the work account. If the dashboard isn't reachable, the review Intercom thread is the working channel ("Reply here").

> 🔄 **REVIEW RESPONSE — 2026-07-29.** Anthropic replied (Intercom, `directory.workspace@platform-operations---anthropic.intercom-mail.com`): three changes before listing. **Inspected the live dashboard 2026-07-31 — the submission under review is NOT the June 2026 Google Form.** It's a later dashboard submission (39 tools, Atlas + deck copy) that was deliberately **PAT-first**; its Additional notes read *"We are also interested in moving to OAuth once the directory supports it natively for Bearer-authenticated servers."* Their reply is the answer to that ask — the endpoint now advertises standard MCP OAuth, so the beta Bearer path (the thing that would have held the listing) is no longer needed.
> 1. **Auth** — two halves. ⚠️ **The auth setting is NOT self-editable:** *"The connector URL and authentication settings are managed by Anthropic — contact mcp-review@anthropic.com to change them."* So the reply is the mechanism, not a courtesy. The **copy** is editable: the `Description` field ends with *"Auth: personal access token (PAT) from the tela profile."* → OAuth-first framing, per `/mcp` (0fd75bb) and `mcp-submission-chatgpt.md`.
> 2. **`fetch` description** — **not a form field.** The Tools section holds tool *names* only, *"populated from a live probe of your server at submission time"* — i.e. before `1451dad` (2026-07-05). ✅ Fixed in code (`d70acf8`); **a deploy is the whole remaining fix.**
> 3. **Safety attestation** — a checkbox, currently checked: *"My connector does not use AI models to generate images, video, or audio."* False since `generate_deck_image`. ⚠️ The section states "All items are required", so unchecking may not save — if it refuses, this goes in the reply too.
>
> **Also stale in the listing (not flagged by them, fix while editing):** `Connection requirements` and `Test setup instructions` are both written entirely around creating and pasting a PAT; and Additional notes claims *"Tool title annotations are missing from the current build"* — untrue, every tool carries a `Title`.
>
> **Staging issuer:** they asked to confirm or repoint. **Decision 2026-07-31 — staging stays** (`decisive-relation-32-staging.authkit.app`); Cagdas explains in the reply. Not a blocker — their wording offers confirmation as a valid answer. Production AuthKit remains the eventual target (custom domain `auth.telawiki.com` is the real payoff; per WorkOS docs envs are fully separate, so the ~12 live MCP connections would need re-consent, but tela accounts are untouched because Standalone mode keeps tela as the identity source).


Grounded in the **actual** submission surfaces + policies (researched 2026-06-05):
- Claude form: the real 6-page Google Form behind `clau.de/mcp-directory-submission` (parsed from its own field data) + [Software Directory Policy](https://support.claude.com/en/articles/13145358-anthropic-software-directory-policy) + [review criteria](https://claude.com/docs/connectors/building/review-criteria) + [auth](https://www.claude.com/docs/connectors/building/authentication) + [IP ranges](https://platform.claude.com/docs/en/api/ip-addresses).
- ChatGPT: [app-submission-guidelines](https://developers.openai.com/apps-sdk/app-submission-guidelines) + [deploy/submission](https://developers.openai.com/apps-sdk/deploy/submission) (via Wayback — OpenAI TLS-blocks this host) + dashboard form per secondary walkthroughs.

Legend: ✅ done · 🔨 I can do now · 👤 Cagdas (account/dashboard) · 🖥️ in-host (needs a live Claude/ChatGPT render) · 🔎 verify

---

## A. Published artifacts (both hosts)
| Item | Claude | ChatGPT | Status |
|---|---|---|---|
| Privacy policy (public URL) | mandatory | mandatory | ✅ `telawiki.com/privacy` |
| **Terms of Service (public URL, same domain)** | form attestation (Page 6) | required form field | ✅ `telawiki.com/terms` |
| Public documentation | mandatory | (docs link) | ✅ `telawiki.com/mcp` — now includes Troubleshooting + Limitations |
| Support contact | mandatory | mandatory | ✅ `tela@telawiki.com` |
| Security vulnerability reporting mechanism | mandatory (ongoing) | — | ✅ security-report line in /privacy + /mcp |

## B. Assets
| Item | Spec | Status |
|---|---|---|
| Square logo SVG (Claude) | 1:1 SVG, served at a URL | ✅ `telawiki.com/favicon.svg` |
| Favicon verification (Claude) | `s2/favicons?domain=telawiki.com&sz=64` must show tela's mark | ✅ raster `favicon.ico`+PNG added & linked (Google cache may lag a few days) |
| App icon 64×64 PNG (ChatGPT) | 64×64 PNG | ✅ `landing/public/icon-64.png` |
| Widget screenshots | Claude: 3–5 PNG ≥1000px. ChatGPT: 1–4 PNG, no chat prompt | ✅ 4 PNGs (page-reader + search-results, light/dark, 1640px) in `docs/submission-assets/` |
| Promo/demo (optional) | Drive link; ChatGPT may want an MP4 demo on same domain | 👤/🖥️ optional |

## C. Account / dashboard
| Item | Host | Status |
|---|---|---|
| OAuth 2.1 + S256 PKCE + DCR + form `/token` | both | ✅ verified live (issuer `decisive-relation-32`) |
| Anthropic egress `160.79.104.0/21` allowlisted | Claude | ✅ Cloudflare rule `0b545114` live |
| Org identity verification (individual) | ChatGPT | 🟡 in progress — `platform.openai.com/settings/organization/general` |
| Billing: $5 prepaid, auto-recharge OFF | ChatGPT | 👤 in progress |
| `api.apps.write` (org owner) | ChatGPT | 👤 |
| Global (non-EU) data-residency project | ChatGPT | 👤 |
| Final submit | both | 👤 Claude: `clau.de/mcp-directory-submission` · ChatGPT: `platform.openai.com/apps-manage` |

## D. Content to write
| Item | For | Status |
|---|---|---|
| Field-by-field answers | Claude | ✅ render `mira.cagdas.io/r/wqchm6` + `docs/mcp-submission-claude.md` |
| App name/desc/category/tagline | both | ✅ in submission docs |
| 20-tool list + human names + annotations | both | ✅ |
| `openWorldHint`/`destructiveHint` per-tool **written justifications** | ChatGPT (required) | ✅ in `docs/mcp-submission-chatgpt.md` |
| Test cases: **5 positive + 3 negative** | ChatGPT | ✅ in `docs/mcp-submission-chatgpt.md` |
| Demo-account reviewer script | both | ✅ |

## E. Verify (engineering)
| Item | Status |
|---|---|
| Tool annotations correct over the wire | ✅ Inspector-verified live |
| Response size cap (≤~25k tokens) | ✅ `get_page`/`fetch` capped 80k chars |
| Read/write split, names ≤64, actionable errors | ✅ |
| **Origin-header validation** on `/api/mcp` | ✅ satisfied by bearer-token auth (DNS-rebind moot without the token); SDK guard left off so it can't break the browser-context widget round-trips |
| **Data minimization** — no telemetry/internal IDs in tool outputs | ✅ audited: get_page/search/fetch expose only wiki content (id/title/body/hierarchy/content timestamps) |
| Widgets render in-host | ✅ verified (other agent) |

## F. Done (engineering, confirmed live)
Transport (Streamable HTTP) · OAuth chain · 20 annotated tools · resources · widgets · search+fetch · privacy + docs live · demo account seeded · Cloudflare allowlist · MCP Inspector pass.

---

## The actual gap list (what's genuinely left to *prepare*)
**Done this pass (✅):** Terms of Service `/terms` · Troubleshooting + Limitations in `/mcp` · security-contact line · 64×64 icon · raster favicon · ChatGPT test-cases · both verifies (Origin = bearer-auth-satisfied, data-min = clean). All deployed live.
**In-host (🖥️):** _none — widget screenshots done (rendered from the real bundles, `docs/submission-assets/`)._
**Cagdas (👤):** finish OpenAI org verification + billing + residency project + `api.apps.write`; the two final submits. (Favicon fixed; Google cache may lag.)
