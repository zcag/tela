# Editions & Pricing — the model

Canonical model for how tela is licensed, packaged, gated, and priced across **cloud**
and **self-host**. This is the source of truth for the strategy; the landing
(`CONTENT.md` + `landing/`), the backend `plans` table, the EE module, and Polar
products all implement *this*. Supersedes the scratch `pricing-handoff.md` (research
only) and the prior split-ladder model frozen in `CONTENT.md` §8b.

> Status (2026-06-30): **built + tested, NOT deployed.** Phases 1–4 are implemented and
> committed (unified ladder + `entitled()` gate, `internal/ee` offline license keys + admin
> License tab, org-audit gating, one-command BYO-AI). SCIM + live premium connectors are a
> scoped follow-on (need external OAuth apps). The live product + Polar still run the old
> split ladder — **do not deploy until the Polar reprice + SSO-org checks in the runbook are
> done.** See [`editions-deploy-runbook.md`](editions-deploy-runbook.md).

---

## 1. The one-line model

> **Where it runs decides the AI. Which edition decides the company-layer.**
>
> - **Cloud** = we run it, **AI included** (managed, metered).
> - **Self-host** = you run it, **bring your own AI** (any OpenAI-compatible LLM + embedder) — always, Community *and* Enterprise.
> - **Enterprise** = the same company-of-record feature layer (identity, governance, compliance, scale, support), bought bundled on cloud or as a license key on self-host.

We never gate the wiki or the AI *capability*. We charge companies for the things only
companies need. That is the whole posture, and it's a marketing asset — say it out loud.

## 2. Three revenue levers

| Lever | What it is | Captures |
|---|---|---|
| **1. Cloud subscriptions** | The Free / Personal / Team / Enterprise ladder | Individuals + teams who want managed AI + zero-ops |
| **2. Enterprise module (self-host)** | Closed `ee/` features unlocked by a per-seat **license key** | Companies self-hosting that need identity / governance / scale |
| **3. Commercial license** | Legal relief from the core's AGPL copyleft | Companies whose lawyers won't allow AGPL — even without EE |

The current AGPL-only / one-binary state has only lever 1 (and a vague, un-operationalized
lever 3). This model turns on all three.

## 3. License & packaging

- **Core stays AGPL-3.0**, now **dual-licensed**. AGPL is deliberate: a company that
  won't ship AGPL must buy a **commercial license** (lever 3) — even if it never touches
  an Enterprise feature. The core is *already* AGPL, so this is **additive, not a
  relicense** — no existing code changes license, no community blast radius.
- **Enterprise module = proprietary, closed-source, in a carved `ee/` directory**,
  compiled into the same binary but **inert without a valid license key**. One image
  ships to everyone; the key unlocks EE features + the seat cap (the GitLab model —
  simplest distribution).
- **License keys = signed offline tokens** (tier + seat cap + expiry) so air-gapped
  self-host works with **no phone-home**. Optional periodic online check.
- **One EE codebase powers both** cloud Enterprise and self-host Enterprise — build the
  enterprise features once.
- **Brand/name reserved** separately (`TRADEMARK.md`) — unchanged.

## 4. The gating line — core vs Enterprise

Principle: the **community core must be genuinely lovable** (it's the distribution), so it
keeps the crown jewels — including AI *capability*. Enterprise gates what **companies need
and individuals don't**.

| Capability | Core (AGPL, free) | Enterprise (closed `ee/`, key) |
|---|:---:|:---:|
| Wiki, Milkdown editor, history, page properties | ✅ | ✅ |
| Live collaboration (Yjs) | ✅ | ✅ |
| Decks, public spaces / blog, custom domains | ✅ | ✅ |
| WebDAV sync, MCP / agents | ✅ | ✅ |
| **Atlas** (sources → maintained wiki) | ✅ | ✅ |
| **Ask + semantic search** | ✅ | ✅ |
| Keyword search, orgs, basic roles (admin/member), per-space access | ✅ | ✅ |
| **SSO / SAML / OIDC** | — | ✅ |
| **SCIM provisioning** | — | ✅ |
| **Audit logs** | — | ✅ |
| **Advanced RBAC / custom roles / granular permissions** | — | ✅ |
| **Retention / legal hold / compliance** | — | ✅ |
| **Premium Atlas connectors** (Slack / Drive / GitHub / Confluence; scheduled + live sync) | — | ✅ |
| **HA / clustering, white-label** | — | ✅ |
| **Priority support + SLA** | — | ✅ |

Note the deliberate placement of **Atlas / Ask / semantic search in the free core**. On
self-host they need BYO inference (a cost the operator already pays), so they aren't ours
to meter there; keeping them free is what makes the community edition spread. We monetize
that AI through **cloud (managed/included)**, not by crippling self-host.

> **Change from today (verified against code, 2026-06-30):** SSO (social + per-org OIDC,
> `sso_handlers.go`/`org_sso.go`/migration `0016`) and audit (`events` feed + `access_audit`/
> `api_key_audit`, migration `0033`) are **already built and ungated** — free to everyone
> today. The work is to put them behind the **unified entitlement** below, not to build them.
> tela is **pre-release with no self-serve customers**, so this is a clean greenfield gating
> change — no grandfathering, no rug-pull. Basic orgs + per-space roles stay in core.
>
> **Self-host upgrade note:** an instance that used free SSO before this change loses it
> until an Enterprise license key is installed (Settings → License). The backend logs a
> prominent boot warning when `org_sso` rows exist but the instance isn't sso-entitled, and
> affected SSO-provisioned users (random password) can recover via password reset. Call this
> out in self-host release notes.

### One entitlement, two unlock paths

A single `entitled(ctx, acct, feature)` gates every paid feature (`sso`, `audit`, `scim`,
`premium_connectors`, `retention`, …). It returns true if **either**:
- **self-host:** a valid installed **license key** grants it, **or**
- **cloud:** the instance is the managed cloud (`managedCloud` — Polar billing on, or
  `TELA_CLOUD=1`) **and** the account's plan grants it (`plans.features[feature]`).

The plan-flag path is honoured **only** on the managed cloud — on self-host `plan_key` is
freely admin-assignable, so it can't be trusted as an entitlement; there the license key
is the only unlock. Same feature code, gated once; cloud unlocks via the plan, self-host
via the key.

## 5. AI packaging

- **Cloud:** managed AI is **included**, metered by a **monthly answer allowance** + an
  **Atlas source cap** per tier; overage handled by **purchasable credit top-ups**, not a
  forced tier jump. AI is a **first-class, visible line** in every cloud tier — because AI
  usage (not seats) is the real cloud COGS.
- **Built-in vs. bring-your-own — the key framing:** the monthly answer allowance meters
  only tela's **built-in** AI (in-app *ask your docs* / chat on our model). Driving tela from
  the user's **own agent** over MCP (Claude, ChatGPT, …) runs on *their* model + tokens, so it
  is **unmetered / unlimited on every tier, including Free** — the connector is the giveaway
  that makes a stingy free answer-cap acceptable. Semantic *retrieval* (research / semantic
  search) is never monthly-capped but is **fair-use rate-limited per account** (`TELA_EMBED_RATE_LIMIT`,
  default 30/min) so the shared embedder can't be saturated; over-limit returns `429 rate_limited`.
- **Scheduled Atlas refresh is paid.** Free plans get Atlas on **manual refresh only** (drift is
  still detected, so the stale badge prompts a run); paid tiers + the trial refresh
  **automatically** on a cadence. Gated by `plans.features.atlas_scheduled` (managed-cloud only).
- **Self-host (all editions):** **BYO** — point tela at any OpenAI-compatible LLM + an
  Ollama-compatible embedder. To kill the cold-start, ship a **one-command "AI on"**
  (`docker-compose.ai.yml`: a recommended local model, `keep_alive` pinned) so Community
  is magical out of the box without "now go integrate an LLM."
- We do **not** resell/meter AI to self-hosters. That's a deliberate giveaway, not an
  oversight — the single biggest thing left on the table, traded for distribution.

## 6. Cloud ladder

AI is the metered axis. Pages / spaces / storage are **not** headline gates (cheap to run,
nobody gates on them) — keep generous, cap only for abuse.

| | **Free** | **Personal** | **Team** | **Enterprise** |
|---|---|---|---|---|
| Who | Trying tela | Individual power user | Any team (the real business) | Compliance / scale |
| Price | **$0** | **$8/mo** ($72/yr → $6/mo) | **$10/seat/mo** ($96/yr → $8/seat/mo) | Custom (contact-sales, no published price) |
| Seats | 1 | 1 | 2+ | Negotiated |
| Built-in AI answers / mo | 50 | 3,000 | 5,000 pooled (+credits) | Negotiated |
| Your own agent (MCP) | Unlimited | Unlimited | Unlimited | Unlimited |
| Atlas sources | 1 | 5 | 20 | Unlimited |
| Atlas refresh | Manual | Scheduled (daily/weekly/monthly) | Scheduled (daily/weekly/monthly) | Scheduled (hourly available) |
| Atlas indexing / mo | 3h | 15h | 30h | Unlimited |
| Wiki / decks / public spaces / WebDAV / MCP | ✅ | ✅ | ✅ | ✅ |
| Pages / spaces / storage | Soft ceiling | Generous | Generous | Unlimited |
| Custom domain | — | ✅ | ✅ | ✅ |
| Basic RBAC / member mgmt | — | — | ✅ | ✅ advanced |
| SSO / SCIM / audit / governance | — | — | — | ✅ |
| Support | Community | Community | Email | Priority + SLA |

**Cloud Enterprise — `Custom`, contact-sales, NO published price (decided 2026-07-05).** We
briefly published a `from $15/seat` floor, then reverted: Enterprise is a company-of-record
deal (SSO/SCIM/audit/governance/SLA/procurement) sold by conversation, shown with no number
and a `Get in touch` CTA — the norm across the field (Notion/GitLab/Coda all hide it). Note
the optics that first worried us are already handled at the self-host end: self-host
Enterprise is `$8` (= a Team seat's displayed price), so it never reads as *more* than Team,
with or without a cloud number. `plans.org_enterprise.price_cents` is NULL so the in-app card
also reads "let's talk". Free tier carries managed-AI COGS with no revenue — capped to a
**taste**; the "I want real AI for free" crowd is routed to **self-host Community** (their
inference, zero cost to us). Trial: 30-day Personal, granted to every account that
**created itself** — password registration and social SSO (Google/Microsoft/GitHub)
alike. Accounts *provisioned for* someone — an org's own SSO connection, an admin,
the first-boot/CLI admin — get none. That rule lives in exactly one place
(`backend/internal/api/users_create.go`) because it previously didn't: the trial was
added to the password INSERT only, so ten weeks of OAuth signups silently landed on
Free while the pricing page, the FAQ and the Terms all promised otherwise. A scan
test fails the build if a new `INSERT INTO users` appears outside that helper.

## 7. Self-host options

| | **Community** | **Enterprise (self-host)** | **Commercial license (core only)** |
|---|---|---|---|
| Price | **Free** | **$8/user/mo**, annual (license key) — = a Team seat; contact-sales for whales | Flat annual |
| License | AGPL-3.0 | Commercial + EE key | Commercial (AGPL relief) |
| AI | BYO | BYO | BYO |
| Gets | Full core (§4) — wiki, Atlas, Ask, decks, sync, MCP, custom domains | Core + all Enterprise features + support/SLA | Full core, no EE, no copyleft obligation |
| For | Individuals, OSS, small teams | Companies self-hosting at scale/compliance | Copyleft-averse companies that don't need EE |

**Make it a funnel, not a phone number** (GitLab playbook):
- EE features ship **visible-but-locked** in the Community binary ("SSO → Upgrade"), so the
  upsell lives in-product where the admin feels the pain.
- **Self-serve EE keys** (buy online, per-seat, key issued instantly) + a **self-serve EE
  trial key** (14–30 days) to feel SSO before paying.
- This lets self-host stay **two tiers** (Community + Enterprise) instead of inventing a
  mid rung — one paid SKU, self-serve, negotiated lane on top.

**Self-host Enterprise — `$8/user/mo` billed annually (decided 2026-07-05).** Deliberately
pegged to a **Team seat's displayed (yearly) price** so it never reads as *more* than Team
on the default view — the confusing "$12 self-host > $10 Team" optics is removed at the
number level, not just via the reconciler matrix. Self-host is ~zero marginal cost to serve
(they run it, their AI), so floor pricing is near-pure margin + adoption for a new tool;
it's at the market floor (Docmost $3.50, Mattermost $10, Gitea $9.50–19). Commercial-license
flat-annual figure: TBD.

**Self-serve + renewal — SHIPPED & LIVE (2026-07-05).** Buying is self-serve (Polar seat-based
product → webhook mints a signed key → emailed + shown in **Settings → Self-host licenses**;
paste into the instance's **Settings → License**). Renewal is handled two ways: the minted key
carries a stable `lid`, and a self-hosted instance polls the cloud (`GET /api/public/license/
refresh`, self-authing by the key's signature) every 12h to **auto-install the renewed key** —
so EE doesn't lapse on renewal without a manual re-paste. Air-gapped installs (unset
`TELA_LICENSE_REFRESH_URL`, or no network) fall back to the emailed key + a 14-day grace.
Seats are **soft-enforced** (over-seat warning, never a block — offline can't count live
seats). Backend plans/Polar for the CLOUD tiers were reconciled live; cloud Enterprise stays
contact-sales (no published price).

## 8. Naming / how it reads (kills the old confusion)

Present **two orthogonal axes**, never a flat list:

|  | **tela Cloud** (we run it · **AI included**) | **tela Self-Hosted** (you run it · **BYO AI**) |
|---|---|---|
| Free | Free | **Community** |
| Individual / team | Personal · Team | — *(use Community)* |
| Enterprise layer | Cloud Enterprise | Self-host Enterprise (key) |

One read: **the column picks who-runs-it-and-who-brings-the-AI; the row picks how much
company-layer you need.** "Enterprise" = the same feature layer in both columns.

## 9. Anti-reseller stance (decided, not defaulted)

Could someone take Community and resell tela-as-a-service? AGPL deters but doesn't forbid a
compliant reseller. A source-available core (BSL/FSL, time-converting) would forbid it, at
the cost of OSI "open source" cred + goodwill. For a wiki this size the reseller risk is
low → **stay AGPL-dual**, accept it. Revisit only if a reseller actually appears.

## 10. Build order (phased)

Landing copy is **done** (`CONTENT.md` §8b, `Pricing.astro`, `SelfHostPricing.astro`).
SSO + audit + feature-flag infra + Polar + quotas already exist — see §4 note. Remaining:

**Phase 1 — Cloud ladder + entitlement gating (unblocks the landing deploy).**
- Migration: rationalize the `plans` catalog — rename `personal_plus`→Personal, `org_team`→
  Team at **$10/$8**; raise storage/page caps off the headline (keep as anti-abuse); set
  `features = {sso, audit}` true on Enterprise; confirm AI caps (Free 50/1 · Personal 1000/5
  · Team 2000/20 · Ent ∞).
- Add `entitled()` wrapping `featureEnabled` (cloud path; key path stubbed for now).
- Gate SSO config/use + the audit screen behind `entitled(…, 'sso'/'audit')`.
- Polar: repriced Team products ($10/mo, $96/yr); swap `TELA_POLAR_PRODUCTS` on the box.
- Frontend: collapse the hardcoded personal/org grouping into the 4-tier ladder; drop SSO
  from "every plan includes"; scaffold the visible-but-locked affordance.
- Verify checkout end-to-end → **`make deploy-landing`** (cloud half of the site is now true).

**Phase 2 — License keys + `ee/` module (self-host capture infra).**
- Signed offline key (Ed25519: tier + seat cap + expiry + `features[]`); embedded public
  key; one binary, key-gated (no build tags). Admin "License" tab: paste → verify → unlock.
- Wire the key path into `entitled()`; seat enforcement vs the key.

**Phase 3 — Net-new EE features + self-serve keys.**
- The clean greenfield EE: **SCIM**, **premium Atlas connectors** (Slack/Drive/GitHub/
  Confluence), retention/legal-hold, white-label polish.
- Self-serve key issuance (Polar product → webhook mints + emails a key) + trial keys;
  visible-but-locked EE UI fully wired.

**Phase 4 — Commercial license + BYO-AI + docs + full deploy.**
- Published commercial-license (AGPL-relief) SKU + terms; dual-license docs.
- `docker-compose.ai.yml` one-command BYO-AI default for self-host.
- Update `docs/` + tela Docs space 16; deploy the self-host half of `/pricing`.

**Follow-on (not blocking):** AI credit top-ups for cloud overage (a Polar one-time product
+ `cloud_usage` credit balance) — slots after Phase 1 whenever overage volume justifies it.
