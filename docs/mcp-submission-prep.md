# MCP directory submission — master prep checklist

> ✅ **CLAUDE SUBMITTED — 2026-06-06.** Confirmation page received ("Thank you for indicating your interest in being included in the Anthropic MCP Directory"). No duplicate (verified first-time on Page 6). Edit the existing response (don't resubmit) via:
> `https://docs.google.com/forms/u/0/d/e/1FAIpQLSeafJF2NDI7oYx1r8o0ycivCSVLNq92Mpc1FPxMKSw1CzDkqA/viewform?usp=form_confirm&edit2=2_ABaOnuc4rtQ9ONh-MS-RuwIxvBxgxKf9p-4TFUItkQp5sXVyK3kTrArftGpnJNvxW-x2TMw`
> Surfaces attested: Claude.ai web + Claude Desktop + Claude Code. Contact/support: tela@telawiki.com. Review has no SLA; escalation mcp-review@anthropic.com.

> 🔄 **REVIEW RESPONSE — 2026-07-29.** Anthropic replied (Intercom, `directory.workspace@platform-operations---anthropic.intercom-mail.com`): three changes before listing. Root cause for all three: **the form response has never been edited since 2026-06-06, while every other surface moved on.** The form was filed one day after the WorkOS 404 outage, so it describes a PAT-only, 20-tool, pre-deck tela. Nothing was misrepresented — it froze.
> 1. **Auth** — "good news", OAuth is now advertised correctly, so the static Bearer/PAT path (still beta on their side, and the thing that would have held the listing) isn't needed. But the form still reads *"Auth: personal access token (PAT) from the tela profile."* → **must be edited to the OAuth-first framing** already used on `/mcp` (0fd75bb, 2026-06-30) and in `mcp-submission-chatgpt.md`.
> 2. **`fetch` description** — was host-specific. ✅ **Fixed in code + docs (`d70acf8`, 2026-07-31)**; still needs the same edit in the form, and a deploy to go live.
> 3. **Safety attestation** — says no AI media generation, but `generate_deck_image` exists. True when filed (deck tools postdate 2026-06-06); → correct the attestation.
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
