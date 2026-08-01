# tela — Access model (canonical)

> Status: **authoritative.** This is the single source of truth for *who can do
> what* in tela. Pairs with [`visibility-model.md`](visibility-model.md) (public
> exposure / share links). When the two disagree, fix the code, not this doc.
>
> Scope as of #153 (orgs) + the lock-down pass + #155 (groups): users, orgs,
> groups, grants, auto-join, auditing. All three principal kinds are live.

## Vocabulary (locked — use these words everywhere: code, UI, docs)

- **Principal** — anything that can be *granted* access to a space: a **user**,
  an **org**, or (planned) a **group**. Concrete tables, not one polymorphic
  entity; the only thing they share is appearing as a grant target.
- **Grant** — one `(principal → space, role)` edge. Direct **user** grants live
  in `space_members`; **org**/**group** grants live in `space_grants`
  (`principal_kind`). One unifying read surface: the `space_access` view.
- **Space role** — `owner` | `editor` | `viewer`. Capability over a space's
  *content* (pages, comments, sharing).
- **Org role** — `admin` | `member`. Capability over *the org itself* (its
  members, its name, its grants). **A different axis from space role** — being an
  org admin grants nothing inside a space by itself.
- **Effective role** — the *single* role a user has on a space =
  `max(all grants reaching them)`, precedence **owner > editor > viewer**. This
  is what every gate checks (`spaceRole` → `space_access`).
- **Access source / provenance** — *how* a user reaches a space: `direct`, or
  `via <Org>` (planned: `via <Group>`). A user may have several sources; the
  effective role is the max across them.
- **Membership** — reserved for principal-in-principal containment: **org
  membership** (`org_members`), and planned **group membership**. Do **not** say
  "space membership" for org/group-derived access — say "access" or "granted via
  <org>". Direct user grants on a space are "**direct members**".
- **Instance-admin** — the operator (`users.is_instance_admin`). Above all
  tenants: virtual admin of *every* org, sees every org, provisions orgs.

## Invariants (must always hold; enforced, not hoped)

1. **Owner is only ever a direct user.** Org/group grants are `editor`/`viewer`
   only. Enforced in the API *and* at the DB layer (triggers on `space_grants`).
   Rationale: `owner` carries management + deletion power; handing it to a whole
   group would make "who can delete this space" unanswerable, and would break
   invariant 2.
2. **Every space has ≥ 1 direct owner.** The last-owner guard (counts
   `space_members` owners) refuses to remove/demote the final one. Combined with
   (1), there is always exactly one place that answers "who governs this space."
3. **Effective role is a pure max.** No grant can *lower* access; adding any
   grant can only raise (or not change) a user's effective role. A direct viewer
   who is also in an editor-granted org is an **editor**.
4. **One resolution path.** Every access decision goes through `space_access`
   (via `spaceRole`/`spaceRoleTx` or a `space_id IN (SELECT … space_access …)`
   gate). No handler queries `space_members` to *decide* access — only to
   *manage* direct members.

## Auto-join domains — identity-derived (locked semantics)

An instance-admin maps an email **domain → org**. The mapping means "anyone with
a verified address at this domain *is* part of this org." Therefore:

- **Member-only.** Auto-join always grants `org_role = member`. Admin is never
  conferred by a domain — it is always a deliberate, manual elevation.
- **Non-discretionary (can't leave / can't be removed).** Membership is derived
  from the identity, not chosen. The "leave" affordance is hidden for
  domain-managed members, and the API rejects removing/demoting-below-member a
  member whose verified email still matches a mapping for that org
  (`domain_managed`). To remove them: remove the domain mapping, or their email
  changes. Manual **elevation** above member (→ admin) is allowed and sticks
  (auto-join uses `INSERT OR IGNORE`, so it never overwrites a manual role).
- **Applied on verify + every login**, best-effort and idempotent, so a mapping
  added *after* a user already verified still catches them next sign-in.
- **Operator-curated only.** Never auto-join public providers (gmail.com, …);
  the UI warns and the instance-admin owns that judgement.

## Email invitations — one mechanism, two targets

You can be invited by email to an **org** (`org_invites`, migration `0060`) or to
a single **space** (`space_invites`, migration `0069`). Both are the *same*
mechanism, deliberately: one token shape, one public lookup, one accept endpoint,
one auto-apply hook (`internal/api/invites.go`; the scope-specific create / list /
revoke handlers live in `org_invites.go` and `space_invites.go`).

- **Token.** 32 random bytes; only the SHA-256 **hash** is stored (mirrors
  `email_tokens`). 14-day TTL. One outstanding invite per (scope, email) —
  re-inviting refreshes that row (partial unique index on `accepted_at IS NULL`).
- **Email-targeted.** Accepting requires the caller's own verified email to match
  the invite, so forwarding the link can't enrol the wrong person
  (`403 email_mismatch`).
- **Public lookup.** `GET /api/invites/{token}` is on `auth.IsPublicPath` and
  self-authenticates via the unguessable token — it renders the accept page for a
  logged-out invitee. It returns a `kind` discriminator (`org` | `space`) plus the
  scope name, the inviter and the invited address: exactly what the email already
  said, nothing more. Accepting (`POST /api/me/accept-invite`) needs a session.
- **Auto-apply on verify.** `applyPendingInvites` (called from `VerifyEmail`,
  beside `applyAutoJoin`) enrols a freshly-verified address into every pending
  org *and* space invite — so someone invited before they had an account finds it
  waiting, with no second action from the inviter. Seat-blocked **org** invites
  stay pending; space invites have no quota.
- **Space share-by-email has two outcomes** (`POST /api/spaces/{id}/invites`,
  owner-gated like `AddSpaceMember`): when a verified account already owns the
  address, it writes the `space_members` row **immediately** and fires the normal
  `space_added` notification (in-app + email) — nothing to accept, response
  `{member}`. Otherwise it parks a `space_invites` row and mails the invitation —
  response `{invite}`. An account with an *unconfirmed* email takes the invite
  path; it lands when they verify.

## Who can do what (authority matrix)

| Action | Who |
|---|---|
| Create / delete an org | Instance-admin |
| Rename an org | Org admin · Instance-admin |
| Add / remove / re-role org members | Org admin · Instance-admin (subject to `last_admin` + `domain_managed`) |
| Map / unmap auto-join domains | Instance-admin |
| Create a space | Any user (becomes its owner) |
| Manage direct space members | Space **owner** |
| Invite someone to a space by email | Space **owner** |
| Share a space with an org/group (grant) | Space **owner** (role ≤ editor) |
| Edit pages / comment | Effective `editor`+ on the space |
| View pages | Effective `viewer`+ on the space |
| Delete a space | Effective `owner` (⇒ a direct user, by invariant 1) |

## Auditing

Membership/grant/auto-join/org-lifecycle changes are recorded in `access_audit`
(actor, action, target, detail, time). Read surface is instance-admin only. This
is the answer to "who added whom / who shared what with whom" as tenants grow.

## Groups (sub-teams) — **shipped** (#155)

Groups are the third `principal_kind`. They slot into everything above with **no
new resolution path** — just a third leg in `space_access`.

- **A group belongs to exactly one org** (`groups.org_id`); it cannot span orgs.
- **Group membership ⊆ org membership.** You can only add an org member to that
  org's groups (DB trigger `group_members_require_org_member`); leaving the org
  removes you from its groups (trigger `org_members_cascade_groups`). Containment
  is enforced, not hoped: org ⊇ its groups.
- **Managed by org admins** (no separate "group lead" role — that's a later,
  additive `group_members.group_role` if needed). Self-leave is allowed.
- **Grantable principal**, `editor`/`viewer` only (invariant 1 applies via the
  same `space_grants` triggers, which already cover `principal_kind='group'`).
- **Resolution:** `space_access` has the group leg
  (`… JOIN group_members gm ON gm.group_id = sg.principal_id WHERE
  sg.principal_kind='group'`). Effective role is still a pure max over
  direct ∪ org ∪ group.
- **Provenance:** the effective-access panel renders a `via <Group>` source
  generically (accessSource carries `kind` + `name`), no special-casing.
- **Vocabulary:** "group" is the technical noun; UI label "Group". A group *is*
  the sub-team.
