# Billing (self-serve subscriptions)

tela sells its paid tiers self-serve through **[Polar](https://polar.sh)**, a
merchant-of-record billing platform (it handles VAT/sales tax and card data; we
never touch either). This doc is the design + operations for that integration.
It sits on top of [metering](metering.md), which owns the tiers themselves and
enforces their limits — billing only decides **which tier an account is on** by
moving `plan_key`.

> Billing ships **dark**. With `TELA_POLAR_TOKEN` / `TELA_POLAR_WEBHOOK_SECRET`
> unset, checkout + portal 503 and plans stay operator-assigned
> (`PATCH /api/admin/plan`). Everything below activates only when Polar is wired.

## Where the code lives

- `backend/internal/billing/polar.go` — a thin, DB-free Polar API client +
  Standard Webhooks signature verifier. `ConfigFromEnv()`, `Enabled()`,
  `CreateCheckout`, `CreateCustomerSession`, `VerifyWebhook`, `ParseEvent`. Unit
  tests in `polar_test.go`.
- `backend/internal/api/billing.go` — the HTTP handlers + the webhook
  reconciler (`reconcileBilling`) that maps a Polar event onto an account's
  `plan_key` + billing state. Tests in `billing_test.go`.
- `backend/internal/db/migrations/0053_billing.sql` — billing columns on
  `users`/`orgs` + the `polar_webhook_events` dedup table.
- Frontend: `frontend/src/lib/queries/billing.ts` (`useCheckout`,
  `useBillingPortal`) + the upgrade/manage controls in
  `frontend/src/components/app/SettingsBillingTab.tsx`.

## Model

A subscription attaches to the same **account** metering charges — a `user` or
an `org` (see [metering](metering.md#model)). Both tables carry symmetric
billing state (migration `0053`):

| column | meaning |
|---|---|
| `polar_customer_id` | Polar Customer id (stable per account; set on first checkout) |
| `polar_subscription_id` | current subscription id (NULL once revoked) |
| `subscription_status` | `none` \| `active` \| `canceled` \| `past_due` |
| `subscription_period_end` | current paid-through (UTC `YYYY-MM-DD HH:MM:SS`) |
| `subscription_cancel_at_period_end` | `1` = cancels at `period_end` |

**Entitlement is `plan_key`, not these columns.** The reconciler sets `plan_key`
to the purchased tier and the existing limit enforcement (`limits.go`) is
unchanged. The billing columns are bookkeeping for the UI ("Manage subscription",
"cancels on X") and the portal.

The link between a Polar customer and a tela account is the **external customer
id**: we set `external_customer_id = "user:<id>"` / `"org:<id>"` at checkout, and
Polar echoes it on every webhook as `data.customer.external_id`. (We also stamp
`account_kind`/`account_id` into checkout metadata as a fallback resolver.)

## Plan ↔ product mapping

Polar products are created in the Polar dashboard; tela references them by UUID.
`TELA_POLAR_PRODUCTS` maps tela plan keys to those UUIDs. A `<plan>@year` key is
the **yearly** product for that tier; the bare key is monthly. Each paid tier
therefore has two Polar products (one per cadence) — both grant the same
`plan_key`:

```
TELA_POLAR_PRODUCTS=personal_plus:<m-uuid>,personal_plus@year:<y-uuid>,org_team:<m-uuid>,org_team@year:<y-uuid>
```

Only mapped (tier, cadence) pairs are purchasable self-serve. Free tiers and
`org_enterprise` (custom-priced) are intentionally unmapped — checkout for them
400s `plan_not_purchasable`. The map is read both ways: forward
(`ProductFor(planKey, interval)`, at checkout) and reverse (`PlanFor`, in
reconciliation — it strips the `@year` suffix, so cadence is irrelevant to which
tier a subscription grants). Yearly **display** prices live in
`plans.price_cents_yearly` (migrations `0054` + `0055`). Discounts are per-tier
(chosen so the per-month equivalent is a clean number): Plus `$72/yr` = `$6/mo`
(25% off `$8/mo`); Team `$60/seat/yr` = `$5/mo` (2 months free off `$6/mo`). The
landing mirrors these. **The display price must match the Polar product price** —
repricing a tier means editing both `plans.price_cents_yearly` *and* the tier's
Polar product.

## Flows

### Checkout — `POST /api/billing/checkout`

Session-authed. Body `{plan_key, org_id?, interval?}` (omit `org_id` for the
personal account; an org upgrade requires the caller be that org's admin;
`interval` is `month` (default) or `year`). Validates the tier exists, matches the
account kind, and has a product for that cadence, then creates a Polar checkout
and returns its hosted `url`. Org checkouts seed the seat quantity from the
current member count. The frontend redirects the browser to `url`. (The landing's
yearly toggle deep-links `?interval=year`, which the in-app billing tab honors to
pre-select yearly.)

Entitlement is **not** granted from the redirect — the user can close the tab.
It's granted by the webhook below.

### Manage — `POST /api/billing/portal`

Session-authed (org variant = org admin). Returns a short-lived Polar
**customer-portal** URL where the account holder can change/cancel/update payment.
400s `no_subscription` if the account has no `polar_customer_id` yet.

### Webhook — `POST /api/billing/webhook`

**Public** (on `auth.IsPublicPath`) — Polar → us with no session. Self-authenticates
by verifying the Standard Webhooks signature against `TELA_POLAR_WEBHOOK_SECRET`,
then reconciles. Idempotent: each delivery's `webhook-id` is recorded in
`polar_webhook_events` and a redelivery is acknowledged without re-applying.

Reconciliation (`reconcileBilling`), by event `type`:

| event | effect |
|---|---|
| `subscription.created` / `.active` / `.updated` | when `status` is `active`/`trialing`: set `plan_key` to the mapped tier, store sub/customer/period/cancel state, and (for a user) clear the trial. Other statuses (`past_due`) update state but keep the plan. |
| `subscription.canceled` | cancellation **scheduled** — flag `cancel_at_period_end=1`, keep the plan and access. UI shows "cancels on `<period_end>`". |
| `subscription.revoked` | period actually **ended** — downgrade `plan_key` to the account-kind free tier, clear sub state. |
| `order.paid` | renewal/first payment — ensure `status='active'` for an account that already has a subscription. |
| `checkout.updated` / `checkout.expired` | **observational only** — records Polar's verdict on a checkout session (`expired` = the page was opened and abandoned). Touches no columns. Both are handled: Polar exposes abandonment as a status transition *and* as a dedicated event, and listening for only one is a silent gap. |

The **canceled vs revoked** distinction is load-bearing: `canceled` means "will
end later" (keep access), `revoked` means "ended now" (downgrade). Gating the
downgrade on `revoked` is what keeps a user paid through the period they bought.

Unresolvable or out-of-scope events are a logged no-op, never an error — only a
genuine DB failure returns 500 (so Polar redelivers).

## Going live — operator checklist

1. **Products.** In the Polar dashboard create a product+price for each paid
   tier **and cadence**: Plus `$8/mo` + Plus (Yearly) `$72/yr`, Team `$6/seat/mo`
   + Team (Yearly) `$60/seat/yr`. A yearly product is a recurring product with the
   billing cycle set to `year`. Note each product UUID. Keep numbers in sync with
   the `plans` table (`price_cents` + `price_cents_yearly`) — the source of truth
   (see [metering](metering.md)).
2. **Token.** Create an Organization Access Token with scopes `checkouts:write`,
   `customer_sessions:write`, `products:read`, `subscriptions:read`,
   `subscriptions:write` (for org seat re-sync), `orders:read`, `customers:read`
   → `TELA_POLAR_TOKEN`.
3. **Webhook.** Add an endpoint → `<PUBLIC_BASE_URL>/api/billing/webhook`,
   format **Raw**, subscribing to the `subscription.*`, `order.paid` **and both
   `checkout.updated` + `checkout.expired`** events. Copy its signing secret
   **verbatim** → `TELA_POLAR_WEBHOOK_SECRET`. Without the two checkout events
   subscribed in the dashboard, the abandonment signal below never arrives — the
   code cannot subscribe itself, and the gap is silent (the endpoint stays green).
   *Already done on the production endpoint (2026-08-27).*
4. **Map** plan keys → product UUIDs in `TELA_POLAR_PRODUCTS`.
5. Redeploy the backend. Verify `Enabled()` by hitting the upgrade button in
   Settings → Plan & Usage.

Test the whole loop against the **sandbox** first:
`TELA_POLAR_BASE_URL=https://sandbox-api.polar.sh` with a sandbox token, secret,
and product UUIDs (sandbox is a fully separate environment; card `4242…`).

## The funnel trail (`billing.*` events)

Every step from seeing a price to money landing is recorded on the unified
events feed (`events` table, migration `0033`) — see `billing_events.go`. This
exists because the reconciler used to change plan state and record **nothing**:
the feed held the checkout click and no outcome, so "three people reached
checkout, none paid" was answerable and every follow-up — did they open the
Polar page, was anything broken, what did a converting user give up, when did a
trial actually lapse — was not.

| type | means | written by |
|---|---|---|
| `billing.plans_viewed` | the paid tiers were rendered to a real person | `ListPlans` on `?src=billing` |
| `billing.checkout` | a Polar checkout URL was minted (**intent, not money**) | `CreateCheckout` |
| `billing.checkout_status` | Polar's verdict on that session (`status=expired` = opened and abandoned) | `checkout.updated` / `checkout.expired` webhook |
| `billing.subscription_update` | a subscription state event landed; `status=` carries which | `subscription.*` webhook |
| `billing.subscription_canceled` | cancellation scheduled (access runs to period end) | `subscription.canceled` |
| `billing.subscription_revoked` | period ended, account fell to free | `subscription.revoked` |
| `billing.payment` | **money actually moved** | `order.paid` |
| `billing.trial_started` | a self-serve signup was granted the trial tier | both signup paths |
| `billing.trial_ending` / `billing.trial_expired` | the trial crossed its last-7-days line / lapsed unconverted | the sweep, below |

Read them in the admin **Events** screen (filter group *Billing*) or straight
from SQL — `detail` is `key=value` pairs, so `detail LIKE '%trial_days_left=%'`
works.

Three things worth knowing:

- **`billing.*` is exempt from the events retention GC** (`events_gc.go`). The
  180-day retention bounds high-volume types; billing rows are single digits a
  month and are the one series whose whole value is longitudinal — a signup in
  January and the payment it becomes in June are one story.
- **`trial_days_left` is captured before the write that destroys it.** Converting
  mid-trial forfeits the remaining free days (no trial period is passed to Polar,
  so billing starts immediately) and `reconcileBilling` NULLs `trial_ends_at` in
  the same UPDATE. Recording it at that moment is the only chance — it is what
  distinguishes "converted early by choice" from "converted at expiry".
- **Trial expiry is derived, so it needs an observer.** `planFor` resolves the
  effective plan from `trial_ends_at` on every request (`limits.go`), which is
  why expiry needs no job — and why nothing *happened* when a trial lapsed.
  `billing_trial_sweep.go` runs every 6h and emits `trial_ending` /
  `trial_expired` for new crossings, guarded by `NOT EXISTS` on the same
  (type, user) and bounded to the last 30 days so a first run on an existing
  database doesn't replay years of history as today's news. It writes events
  only — no plan state, no email.

Deliberately **not** recorded: the checkout start is no longer written to
`access_audit`. That table is the access-control trail (org / membership / grant
/ domain, kept forever for compliance) and its mirror filed the row as
`access.billing.checkout` — a name no billing query would look under.

## Gotchas

- **Webhook secret encoding.** Polar's dashboard secret is the **raw HMAC key**
  — do *not* base64-decode it and there is *no* `whsec_` prefix to strip (that's
  the Svix convention; Polar's SDK nets the raw UTF-8 bytes). `VerifyWebhook`
  uses `[]byte(secret)` directly. This is the #1 first-timer failure.
- **Raw body.** The signature covers `id.timestamp.body` over the *exact* bytes —
  the handler verifies before any JSON re-marshal.
- **Redelivery.** Polar redelivers on any non-2xx/slow response and can duplicate
  or reorder events; reconciliation is idempotent and deduped on `webhook-id`.
- **Sandbox isolation.** Separate token, secret, product UUIDs, and dashboard
  from prod — everything routes through env so it's a one-config switch.
- **Seats.** Org checkout seeds seats from the member count at purchase time, and
  `syncOrgSeats` keeps them in step afterward: adding/removing an org member
  fire-and-forget `PATCH /v1/subscriptions/{id}` `{seats}` to the live count
  (best-effort — logged, never blocks the membership change). Needs the token's
  **`subscriptions:write`** scope; Polar prorates per the org's default. (We bill
  capacity only — we don't drive Polar *seat assignment* — so a decrement is never
  blocked by an "assigned > new count" check.)
- **Rotating `TELA_POLAR_WEBHOOK_SECRET`** invalidates in-flight signatures; the
  verifier accepts any signature in the (space-separated) header list, which
  covers Polar's own rotation window.
