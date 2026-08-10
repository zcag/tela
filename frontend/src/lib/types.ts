// Wire types — mirror the Go JSON tags exactly (snake_case).
// Timestamps come from SQLite as `YYYY-MM-DD HH:MM:SS` (no `Z`, no offset). Keep them
// as strings on the wire and parse with `parseSqliteTs` so they aren't interpreted as
// local time. See memory.md known pitfall.

// One org/group a space is shared with — the "what" in the sidebar access
// summary. Mirrors backend's spacePrincipal.
export interface SpacePrincipal {
  kind: 'org' | 'group'
  name: string
}

// The org that *owns* a space (spaces.org_id) — distinct from the orgs it's
// shared with. Absent for a personally-owned space. Mirrors backend's
// spaceOwnerOrg. Present on the spaces list rows.
export interface SpaceOwnerOrg {
  id: number
  name: string
}

export interface Space {
  id: number
  name: string
  slug: string
  // 'private' (members-only) or 'public' (whole space readable on the web, no
  // login). Present on the spaces list + detail; optional on constructed Spaces.
  visibility?: 'private' | 'public'
  // Blog standfirst shown on a public space's front page. Present on list/detail.
  description?: string
  // Canonical public path (/{handle}/{space-slug}) when published, absent
  // otherwise. Server-derived — never re-derive the owning-handle rule (org slug
  // vs personal username) client-side. Present only on the single-space fetch.
  public_path?: string
  created_at: string
  updated_at: string
  // Access summary — present on the spaces list (sidebar), optional elsewhere.
  // member_count is the effective distinct-user count (direct ∪ via org/group);
  // is_personal flags the auto-provisioned personal home; principals are the
  // orgs/groups the space is shared with.
  member_count?: number
  is_personal?: boolean
  principals?: SpacePrincipal[]
  // Set when an org owns this space (after transfer / org-owned creation);
  // absent ⇒ owned by you. Present on the spaces list rows.
  owner_org?: SpaceOwnerOrg
  // The caller's effective role on the space (direct ∪ org ∪ group — the
  // backend space_access view). Present on the single-space fetch only;
  // absent on list rows / constructed Spaces.
  my_role?: SpaceRole
}

export type SpaceRole = 'owner' | 'editor' | 'viewer'

// Resolved public-link exposure of a page (backend exposure.go). Read-only,
// derived from active share links — pages have no stored visibility. "private"
// = space members only (the resting state); "public"/"password" = reachable by
// an open / password-protected link. `inherited` = exposure comes only from an
// ancestor's include-descendants share. See docs/visibility-model.md.
export type ExposureState = 'private' | 'public' | 'password'

export interface PageExposure {
  state: ExposureState
  inherited: boolean
  expires_at: string | null
}

export interface Page {
  id: number
  space_id: number
  parent_id: number | null
  title: string
  body: string
  position: number
  // Free-form page properties (frontmatter). Reserved/system keys (id, title,
  // slug, created, …) never appear here — only the user/agent bag. Omitted by
  // the backend when empty, so optional; treat absent as `{}`.
  props?: Record<string, unknown>
  created_at: string
  updated_at: string
  // Present on tree (`?tree=1`) and flat list rows, and attached by `usePage`
  // from the GET /api/pages/{id} sibling field. Optional so older cached rows
  // and optimistic nodes stay valid.
  exposure?: PageExposure | null
}

export interface PageTreeNode extends Page {
  children: PageTreeNode[]
}

// Flat cross-space row returned by `/api/pages/all` (M5.2b). Powers the
// `[[Page]]` autocomplete picker — breadcrumb is root → immediate parent
// (page title excluded). `space_name` lets the picker disambiguate rows
// whose visible breadcrumb collides across spaces.
export interface PageListItem {
  id: number
  space_id: number
  space_name: string
  title: string
  breadcrumb: string[]
}

// Row in /api/pages/{id}/backlinks (M5.2b). `snippet` wraps a nearby word
// in literal `<mark>…</mark>` from a ±60-byte window around the first
// `tela://page/{id}` URL (URL stripped). Same raw-HTML contract as
// `/api/search` — render via `HighlightedSnippet`, never
// `dangerouslySetInnerHTML`. Empty snippet = bare-URL source body; the
// renderer should hide the snippet line in that case.
export interface Backlink {
  page_id: number
  space_id: number
  space_name: string
  title: string
  breadcrumb: string[]
  snippet: string
}

// A semantically-related page from GET /api/pages/{id}/related — computed from
// stored chunk embeddings (no live model call), ranked by cosine similarity to
// the source page's centroid. Unlike a Backlink, no human ever drew this edge.
export interface RelatedPage {
  page_id: number
  space_id: number
  title: string
  similarity: number // cosine similarity in [0,1], higher = closer
  updated_at: string
}

// The "who/what last touched this" signal for the epistemic trust strip, from
// GET /api/pages/{id}/provenance. Computed (read-only) from the latest revision.
export interface PageProvenance {
  source: 'human' | 'agent' | 'sync'
  raw_source?: string
  editor?: string
  // Original author (first revision); the byline shows this, appending the
  // editor only when it differs.
  author?: string
  edited_at: string
}

// One same-space page that contradicts the current one (agreement Slice 2).
export interface PageDispute {
  page_id: number
  title: string
  reason: string
}

// The corroboration/contradiction read for the trust strip, from
// GET /api/pages/{id}/agreement. `computed` is false until the background worker
// has produced a result (unconfigured instance / brand-new page) — distinct from
// a computed zero.
export interface PageAgreement {
  computed: boolean
  corroborate: number
  dispute: number
  disputes: PageDispute[]
  computed_at?: string
}

export interface CreateSpaceInput {
  name: string
  slug?: string
  // When set, the space is owned by that org (caller must be a member): the
  // org's plan governs quotas and org members get editor access. Omit for a
  // personal space owned by the caller.
  org_id?: number
}

// Metering & tiers (backend internal/api/limits.go). A plan is a tier carried by
// an account (a user's personal account or an org). A null max_* means
// unlimited. Mirrors backend's `plan`.
export interface Plan {
  key: string
  account_kind: 'user' | 'org'
  name: string
  max_spaces: number | null
  max_pages_per_space: number | null
  max_storage_bytes: number | null
  max_members: number | null
  max_llm_calls_per_month: number | null
  // How many Atlas sources (a git repo / Jira project Atlas keeps generated +
  // refreshed) the account may keep live. null = unlimited.
  max_atlas_sources: number | null
  // Boolean entitlement map (e.g. managed_rag, publishing, sso, audit) — the
  // feature layer beside the numeric quotas.
  features: Record<string, boolean>
  // false = an internal/comp tier kept out of the public catalog (still
  // admin-assignable). The plan-comparison UI shows only listed tiers.
  listed: boolean
  // Display pricing. null = custom/contact, 0 = free. price_cents_yearly is the
  // annual amount (null = no yearly option); the yearly cadence is a separate
  // Polar product. Self-serve checkout is live (Polar).
  price_cents: number | null
  price_period: string
  price_cents_yearly: number | null
  // Cost-shaped Atlas caps; null = unlimited.
  max_atlas_files_per_run: number | null
  max_atlas_runs_per_month: number | null
  max_embed_tokens_per_month: number | null
  // GPU minutes — the cap that actually binds, since a run ranges 5-80 min and
  // a source count says nothing about cost.
  max_atlas_minutes_per_month: number | null
}

// GET /api/usage and /api/orgs/{id}/usage. members is present for orgs only.
export interface Usage {
  account_kind: 'user' | 'org'
  account_id: number
  plan: Plan
  usage: {
    spaces: number
    storage_bytes: number
    members?: number
    llm_calls: number
    // Atlas cost meters for the current calendar month. atlas_runs excludes
    // failed runs, matching what the server's gate counts.
    atlas_runs: number
    embed_tokens: number
  }
  // Live Polar billing state; absent until the account has subscribed. Drives
  // the "Manage subscription" button and the cancel/past-due notices.
  subscription?: {
    status: 'active' | 'canceled' | 'past_due'
    period_end: string | null
    cancel_at_period_end: boolean
  }
}

export interface UpdateSpaceInput {
  name?: string
  slug?: string
  visibility?: 'private' | 'public'
  // Blog standfirst for a public space. '' clears it.
  description?: string
}

export interface CreatePageInput {
  space_id: number
  parent_id?: number | null
  title: string
  body?: string
  // Free-form page props at creation — used to stamp a doc-type (e.g.
  // `{ sheet: true }` for a spreadsheet, `{ deck: true }` for a deck). The
  // backend keeps a verbatim body for these types (no frontmatter stripping).
  props?: Record<string, unknown>
}

// PATCH /api/pages/{id} only accepts title and body — space_id and parent_id are
// move-only via /move. Typed accordingly to keep callers honest.
export interface UpdatePageInput {
  title?: string
  body?: string
  // Replace the whole props bag (PUT semantics) — e.g. mark a page a deck + its
  // theme. Reserved keys are ignored server-side.
  props?: Record<string, unknown>
}

// `parent_id`: omit to keep current; pass explicit `null` to make root.
// `space_id`: omit to keep current; pass to move across spaces (descendants follow).
export interface MovePageInput {
  space_id?: number
  parent_id?: number | null
  position?: number
}

// Row in GET /api/users/me/sessions (M6.2). Timestamps are SQLite-native
// `YYYY-MM-DD HH:MM:SS` strings (no `Z`); parse via `parseSqliteTs` and
// related helpers — never `new Date(s)` directly. `current=true` flags the
// session whose cookie made the request, which the UI uses to mark the row
// and disable its revoke control.
export interface SessionRow {
  id: string
  last_seen_at: string
  expires_at: string
  created_at: string
  user_agent: string
  current: boolean
}

// Row in GET /api/spaces/{id}/members (M6.2). Mirrors backend's
// spaceMemberDTO. Backend orders rows owner → editor → viewer (role ASC
// sorts that way alphabetically) then username ASC.
export interface SpaceMember {
  user_id: number
  username: string
  role: SpaceRole
  created_at: string
  updated_at: string
}

// Row in GET /api/admin/users (M6.2). Mirrors backend's adminUserDTO.
// Timestamps are SQLite-native — render via localDateFromSqlite for the
// 'Created' column.
export interface AdminUserRow {
  id: number
  username: string
  // Human-readable name; '' when unset (fall back to username).
  display_name: string
  email: string | null
  email_verified: boolean
  is_instance_admin: boolean
  is_active: boolean
  // The user's personal-account plan tier key (metering). Settable by an admin.
  plan_key: string
  created_at: string
  updated_at: string
  // Last authenticated request (most recent session touch); null = never signed in.
  last_active_at: string | null
  // Usage vs the account's plan limits. null when the snapshot couldn't be built.
  usage: AdminUserUsage | null
  // List-only enrichments (absent ⇒ zero/false).
  orgs?: number // org memberships
  llm_calls?: number // AI calls this calendar month
  has_api_key?: boolean // ≥1 non-revoked PAT
  used_mcp?: boolean // connected MCP (any credential, incl. OAuth)
  mcp_last_seen_at?: string | null // last authenticated MCP request
}

// Per-user resource snapshot in the admin list — current usage beside the plan
// limits. A null max means unlimited (mirrors the backend's adminUserUsage).
export interface AdminUserUsage {
  spaces: number
  storage_bytes: number
  max_spaces: number | null
  max_storage_bytes: number | null
}

// Organizations (#153). An org is a grantable principal: a space can be shared
// with a whole org via space_grants, and every member gains the granted role
// through the space_access view. Mirrors backend's orgDTO.
export type OrgRole = 'admin' | 'member'

export interface Org {
  id: number
  name: string
  slug: string
  member_count: number
  // The caller's own org_role, or null when they only see the org as an
  // instance-admin (no membership row).
  my_role: OrgRole | null
  // The org's plan tier key (metering). Settable by an admin.
  plan_key: string
  created_at: string
  updated_at: string
}

// Row in GET /api/orgs/{id}/members. Mirrors backend's orgMemberDTO.
export interface OrgMember {
  user_id: number
  username: string
  email: string | null
  org_role: OrgRole
  created_at: string
  updated_at: string
}

// A group sub-team within an org (GET /api/orgs/{id}/groups). Mirrors backend's
// groupDTO.
export interface Group {
  id: number
  org_id: number
  name: string
  member_count: number
  created_at: string
  updated_at: string
}

// Flat cross-org group for the share picker (GET /api/groups). Mirrors
// backend's myGroupDTO.
export interface MyGroup {
  id: number
  org_id: number
  name: string
  org_name: string
}

// Row in GET /api/orgs/{id}/groups/{group_id}/members. Mirrors groupMemberDTO.
export interface GroupMember {
  user_id: number
  username: string
  email: string | null
  created_at: string
}

// Row in GET /api/spaces/{id}/grants — a non-user principal's access to a space.
// Principal-generic: principal_name is the org/group name; context_name is the
// parent org's name for a group grant (null for an org). Grants are editor/viewer
// only. Mirrors backend's spaceGrantDTO.
export interface SpaceGrant {
  id: number
  principal_kind: 'org' | 'group'
  principal_id: number
  principal_name: string
  context_name: string | null
  role: 'editor' | 'viewer'
  created_at: string
  updated_at: string
}

// Row in GET /api/admin/org-domains — an auto-join email-domain mapping.
// Mirrors backend's orgDomainDTO. Auto-join is identity-derived and member-only
// (no per-domain role) — see docs/access-model.md.
export interface OrgDomain {
  domain: string
  org_id: number
  org_name: string
  created_at: string
}

// A custom login domain (vanity hostname) an org can attach to white-label its
// sign-in screen — e.g. wiki.example.com. DISTINCT from OrgDomain above: that's
// an email-domain auto-join mapping (identity → membership); THIS is a DNS
// hostname that serves the org's own login surface. Mirrors backend's
// OrgHostname. `status` is 'pending' until DNS verification passes, then
// 'active'. The txt_*/cname_target fields are the DNS records the admin must
// add; verified_at is null while pending.
export interface OrgHostname {
  hostname: string
  status: 'pending' | 'active'
  txt_name: string
  txt_value: string
  cname_target: string
  verified_at: string | null
  created_at: string
}

// Per-org toggles for which sign-in methods its custom-domain login screen
// offers. GET/PUT /api/orgs/{id}/login-settings. The backend rejects disabling
// BOTH when the org has no SSO configured (you'd lock everyone out).
export interface OrgLoginSettings {
  password_enabled: boolean
  social_enabled: boolean
}

// GET /api/host-context — PUBLIC, host-derived. `org` is null on the canonical
// host and the owning org on a custom domain; `login` drives which sign-in
// affordances the (white-labeled) login screen shows. Mirrors backend's
// hostContextDTO. `logo_url`/`accent` carry the org's white-label branding and
// are '' when there's no override (fall back to the org name / default theme).
export interface HostContext {
  org: {
    id: number
    name: string
    slug: string
    logo_url: string
    accent: string
  } | null
  canonical_base: string
  login: {
    password_enabled: boolean
    social_enabled: boolean
    org_sso_available: boolean
  }
  // Admin-set maintenance notice for the app-wide banner; null/absent = none.
  maintenance?: { notice: string; level: 'info' | 'warning' } | null
  // Whether managed AI (ask / semantic search) is serving right now.
  ai_available: boolean
}

// GET/PUT /api/orgs/{id}/branding — an org's white-label overrides. Both '' to
// clear. PUT validates: logo_url must be https://; accent must be a hex
// (#rrggbb) or an oklch()/rgb()/rgba() color (400 bad_request otherwise).
export interface OrgBranding {
  /** Effective logo URL — a tela serve route when a logo is stored, else ''. */
  logo_url: string
  /** A logo is stored in tela (vs. unset). */
  has_logo: boolean
  accent: string
}

// GET /api/orgs/{id}/hostnames/{hostname}/health — a live probe of a custom
// domain: whether DNS resolves to us and HTTPS terminates. `note` carries a
// human hint when a check fails. Mirrors backend's hostnameHealthDTO.
export interface HostnameHealth {
  dns_ok: boolean
  addrs: string[]
  https_ok: boolean
  note?: string
}

// One way a user reaches a space (GET /api/spaces/{id}/access). Mirrors
// backend's accessSource. name is the org/group name; absent for direct.
export interface AccessSource {
  kind: 'direct' | 'org' | 'group'
  role: SpaceRole
  name?: string
}

// Resolved access entry: a user, their effective (max) role, and every source
// it comes through. Mirrors backend's spaceAccessEntry.
export interface SpaceAccessEntry {
  user_id: number
  username: string
  email: string | null
  effective_role: SpaceRole
  sources: AccessSource[]
}

// Row in GET /api/admin/access-audit. Mirrors backend's accessAuditEntry.
// actor_* are null for system actions (auto-join).
export interface AccessAuditEntry {
  id: number
  actor_user_id: number | null
  actor_username: string | null
  action: string
  target_kind: string
  target_id: number | null
  detail: string
  created_at: string
}

// One row of the unified activity feed (GET /api/admin/events). Mirrors the
// backend eventDTO. `type` is e.g. 'auth.login', 'page.view', 'access.<action>',
// 'ask', 'api.request'. Labels are denormalized so the feed renders join-free.
export interface EventEntry {
  id: number
  type: string
  actor_user_id: number | null
  actor_label: string
  target_kind: string
  target_id: number | null
  target_label: string
  detail: string
  ip: string
  user_agent: string
  created_at: string
}

// Optional user-chosen feedback type. Mirrors the backend's closed set; the
// widget's chips are optional, so a row may have no kind.
export type FeedbackKind = 'idea' | 'bug' | 'other'

// Where a feedback row originated, stamped server-side.
export type FeedbackSource = 'web' | 'mcp' | 'api'

// Free-form triage context attached to a feedback row. The widget stamps the
// client fields; the backend folds in source/app_version/app_commit/user_agent.
// All optional — agent/api submissions carry little or none.
export interface FeedbackContext {
  source?: FeedbackSource | string
  route?: string
  page_id?: number
  space_id?: number
  page_title?: string
  theme?: string
  viewport?: string
  locale?: string
  app_version?: string
  app_commit?: string
  user_agent?: string
}

// Triage state of a feedback row (migration 0071). Distinct from the unread
// badge, which is a per-admin "last opened the tab" watermark: seen ≠ handled.
export type FeedbackStatus = 'open' | 'done' | 'wontfix'

// One submitted feedback row, instance-admin read (GET /api/admin/feedback).
export interface FeedbackEntry {
  id: number
  created_at: string
  subject: string
  body: string
  kind: FeedbackKind | null
  source: FeedbackSource | string
  user_id: number | null
  username: string | null
  via_api_key: boolean
  context: FeedbackContext | null
  status: FeedbackStatus
  // Server-derived: set when status left 'open', cleared when it returns.
  resolved_at: string | null
}

// Request body for POST /api/feedback from the in-app widget. Subject is
// omitted — the backend derives it from the body's first line.
export interface CreateFeedbackInput {
  body: string
  kind?: FeedbackKind
  context?: FeedbackContext
}

// Instance-wide usage overview (GET /api/admin/usage), instance-admin only.
export interface AdminUsageTotals {
  users: number
  orgs: number
  spaces: number
  pages: number
  storage_bytes: number
  llm_calls: number
  asks: number
  asks_answered: number
}
export interface AdminAccountUsage {
  kind: 'user' | 'org'
  id: number
  label: string
  plan_key: string
  plan_name: string
  llm_calls: number
  llm_cap: number | null
}
export interface KnowledgeGap {
  question: string
  asks: number
  answered: number
  avg_hits: number
  last_asked: string
}
export interface AdminUsage {
  period: string
  totals: AdminUsageTotals
  top: AdminAccountUsage[]
  gaps: KnowledgeGap[]
}

// Three-rung scope ceiling on a personal access token. See
// backend/internal/auth/api_key.go — `admin` implies write+read, `write`
// implies read.
export type ApiKeyScope = 'read' | 'write' | 'admin'

// Row in GET /api/api_keys (M16.A.1). Mirrors backend's apiKeyDTO. `key` is
// populated ONLY on the POST create response — list/get omit it. `space_id`
// null means "all spaces"; otherwise the key is scoped to that single space.
export interface ApiKeyRow {
  id: number
  name: string
  key_prefix: string
  scope: ApiKeyScope
  space_id: number | null
  last_used_at: string | null
  expires_at: string | null
  created_at: string
  revoked_at: string | null
  key?: string
}

export interface ApiErrorBody {
  error: string
  code: string
  // code=forbidden_public only — the no-login reader route to redirect to.
  public_path?: string
}

export function parseSqliteTs(s: string): Date {
  // SQLite emits "YYYY-MM-DD HH:MM:SS" UTC with no zone marker. Replace the space with
  // 'T' and append 'Z' so `new Date()` parses as UTC rather than local time.
  const iso = s.includes('T') ? s : s.replace(' ', 'T')
  const withZone = /[zZ]|[+-]\d{2}:?\d{2}$/.test(iso) ? iso : iso + 'Z'
  return new Date(withZone)
}

