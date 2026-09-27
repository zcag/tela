# tela — Visibility & Sharing model (design note)

> Status: **shipped** (2026-06-03) — exposure model, indicators, audit view,
> and personal-space provisioning are all in. Remaining items are the explicit
> "Deferred" list below.
> Supersedes the implicit, three-mechanism status quo described below.

## Why

Today "who can see this page?" has **no single answer** — it's the sum of three
mechanisms that never reconcile in the UI:

1. **Space membership** (`owner`/`editor`/`viewer` in `space_members`) — all-or-nothing
   access to every page in a space. No per-page notion of privacy.
2. **Share links** (`/share/{token}`) — good, deliberate, per-page: password, expiry,
   include-descendants, revoke. This part stays.
3. **`/p/{id}` permalink** — an **always-on** public OG envelope for *every page that
   exists*. A crawler-UA request (no auth, no share link) returns the title, space
   name, a ~200-char body excerpt, and an OG image — for any sequential page id.

And the UI surfaces **none** of it: no badge on the page, no marker in the sidebar,
the Share sheet is editor-only, so the only way to learn a page's state is to open it.
Net effect: you can't trust it with personal notes (fear of leakage) *and* can't trust
it as a share surface (no confidence in what's exposed). That kills daily use.

## The model

Two independent axes. Each has **exactly one place** you look to answer it.

### Axis 1 — People (identity): lives on the **space**, never the page

A page has **no personal access list**. It inherits its space's locked-down member
set. "Who can open this internally?" is always answered by the page's *space*, the
same answer for every page in it.

- **Truly private to one person = a space with one member (you).** Privacy is not a
  page flag; it's a private space. → see "Frictionless personal space" below.
- No per-page user ACLs. (Deliberate: per-page identity sharing is exactly what made
  Google-Docs sprawl unknowable. Keeping identity at the space level is what makes the
  system answerable at a glance.)

> **Orgs (#153) keep this axis intact.** Identity access can be conferred to a
> *group* (an org) as well as an individual, but still **only at the space level** —
> never per-page. A space's "who can open this" is now: direct members ∪ members of
> any org the space is shared with, resolved in one place (the `space_access` view).
> Org grants are `editor`/`viewer` only; `owner` stays a direct-user responsibility.
> The answer to "who can see this page?" is still a single space-level lookup.

### Axis 2 — Public exposure (link): lives on the **page**

Every page resolves to one **public-exposure state**, computed from active share links
(its own, or an ancestor's `include_descendants` share) and shown ambiently:

| State | Icon | Means |
|---|---|---|
| **Space-only** (resting) | quiet `Space` chip (`Users`/`Lock`) | members of this page's space only |
| **Public link** | `Globe` | anyone with the link — no password |
| **Password link** | `KeyRound` | anyone with the link **+** password |
| **Inherited** | dimmed icon + `CornerDownRight` | exposed via a parent's "include children" share |

Icons are **Lucide** (the FE convention), not emoji — chosen for a clean, consistent
set. The resting **Space-only** chip is shown explicitly, so a missing icon never reads
as "didn't load." A page with multiple links collapses to the *most open* state (open >
password), with the rest visible in the manager.

> "Public" here = an open (no-password) share link. A future, distinct **Published**
> state (clean indexed URL, no token) is out of scope — see Deferred.

## Where each axis is surfaced

1. **Page-header visibility pill** — `🔒 Space` / `🌐 Public` / `🔑 Password`, next to
   the title. **Visible to every space member** (anyone can tell what's exposed).
   Clicking opens the share panel; **creating/revoking still requires editor+**.
   (Today the Share button is editor-only — viewers can't even *see* state. That flips:
   everyone sees state, only editors change it.)
2. **Sidebar marker** — a small icon on exposed pages so you can *scan the tree* and see
   your whole exposure surface at once. Inherited shares get the dimmed `↳` treatment.
3. **"Shared" audit view** — one screen listing everything reachable by link right now,
   across all spaces you can see, with state + expiry. The real antidote to "I'm never
   sure": you check a list instead of trusting memory. (Highest-trust item here.)
4. **Space members view** — the Axis-1 answer: a space clearly shows its locked-down
   member set (a one-member space reads unmistakably as "just you").

## Frictionless personal space (load-bearing)

If "private" means "a private space," then daily *personal writing* lives in a
one-member space — so that space must be **effortless and always there**, or we've
re-created capture friction. Today bootstrap creates no space at all
(`auth/bootstrap.go`).

Shipped: migration `0014_personal_spaces.sql` adds `spaces.personal_user_id`
(partial-unique). `api.EnsurePersonalSpace` idempotently creates a private,
one-member "Personal" space (owner = the user); it runs when the admin creates a
user and is backfilled for everyone (incl. the bootstrap admin) at startup via
`EnsurePersonalSpacesForAll` from `main.go`. "Ensure if missing", so a deleted
personal space returns on next boot — fine for a default home. **Personal
capture is now one click, not "create space → set membership → create page."**

## Implementation sketch (no core migration)

- **Backend:** enrich the page-list / tree payload (`api/pages.go`) with a resolved
  `exposure` per page: `{ state: "private"|"public"|"password", inherited: bool,
  expires_at }`. Computed from `share_links` (own + ancestor `include_descendants`),
  folded into the existing tree build. Read-only derived field; share CRUD is unchanged.
- **Frontend:** a `VisibilityBadge` owned primitive (tokens + Storybook story per the FE
  rules) used in the page header and, compact, in the sidebar. New route/panel for the
  audit view. Badge visible to all members; manager gated to editor+ as today.
- **No schema change** for the core. A future `Published` state may add a `pages`
  column; not now.

## Deferred (explicit, not forgotten)

- **Published state** (🌐 clean indexed URL, no token).
- **Personal-space UI polish** — label/pin the personal space in the spaces list
  ("just you"); the space itself is provisioned, this is only affordance.

## Resolved (2026-06-03)

1. **Personal space — every user, auto.** Each user gets a private one-member space
   provisioned automatically. Capture works out of the box for everyone.
2. **Audit view — its own route/screen.** A dedicated "Shared" page across spaces.
3. **Sidebar markers — on by default.** Add a toggle later only if the tree feels noisy.
4. **Icons — Lucide, not emoji.** A clean, consistent set; no emoji in the UI.

## Resolved (2026-07-02) — `/p/{id}` permalink card

The always-on `/p/{id}` OG permalink is kept for **every page, public or private**,
narrowed to **title-only** — the crawler envelope carries the page title + a
generated OG image, never the body excerpt (`public_share.go`, `writeOGHTMLWithURL`).
A deck's card is its **first-slide cover** (`/p/{id}/og.png` renders it for public
*and* private decks; `og_image.go`). Rationale: a pasted link should unfurl anywhere
it's shared, and a title + cover slide is intentionally shareable, while the body
stays private. The **rich body excerpt** remains gated behind an explicit
`/share/{token}` link. (This supersedes the interim hard-404 for private-space
pages from `f921ccc`/`b0d6c65`, which had over-corrected the leak fix.)

## Resolved (2026-09-27): no title enumeration

Reported privately: page ids are sequential, so walking `/p/1..N` with a crawler UA
harvested every private page's title and space name, and `/p/{id}/og.png` rendered a
private deck's first slide to anyone. The card stays, but a **private** page is now
described only to a link that already carries its title:

- `/p/{id}/{slug}` shows the title card only when `slug` equals the current title's
  slug (`slugMatchesTitle`, `public_share.go`). A bare `/p/{id}`, a wrong or stale slug,
  or a title with no slug gets a generic card: no title, no space name, no image,
  branded by the request host only (the owning org's brand would itself say whose
  page the id is). Every link people share carries the slug: *Copy link* builds
  `/p/{id}/{slug}`, and Caddy's in-app deep-link rewrite (`@page_bots` in
  `sites.caddy`) now keeps the slug segment instead of dropping it.
- The card's `og:image` is signed (`ogImageURL`: HMAC of the page id under a key
  derived from `TELA_SHARE_SECRET`). `/p/{id}/og.png` renders a private page's title or
  deck cover only with that sig; without it, the same generic image for every page.
  `/share/{token}` envelopes use the signed URL too.
- A browser hitting a private `/p/{id}` is redirected to the in-app route **without**
  the title slug unless the link carried it (the `Location` header is as readable
  as the card; the SPA fills the slug in after login).
- Public-space pages are unchanged. Cost: a link shared before a rename unfurls
  generic (it still opens, since the id resolves).
- Found in the same audit: `/api/public/files/{prefix}` resolved an 8-hex prefix and
  returned the full hash (the blob's capability URL) for private files too; it now
  requires the 12-hex shareable prefix every `/f` link carries (`fileHashShortLen`).
