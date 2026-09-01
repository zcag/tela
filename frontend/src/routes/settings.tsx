import { useMemo } from 'react'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { ImportSection } from '../components/app/ImportSection'
import { SettingsApiKeysTab } from '../components/app/SettingsApiKeysTab'
import { SettingsBillingTab } from '../components/app/SettingsBillingTab'
import { SettingsAuditTab } from '../components/app/SettingsAuditTab'
import { SettingsEventsTab } from '../components/app/SettingsEventsTab'
import { SettingsErrorsTab } from '../components/app/SettingsErrorsTab'
import { SettingsInsightsTab } from '../components/app/SettingsInsightsTab'
import { SettingsUsageTab } from '../components/app/SettingsUsageTab'
import { SettingsFeedbackTab } from '../components/app/SettingsFeedbackTab'
import { SettingsNotificationsTab } from '../components/app/SettingsNotificationsTab'
import { SettingsFollowingTab } from '../components/app/SettingsFollowingTab'
import { SettingsOrgsTab } from '../components/app/SettingsOrgsTab'
import { SettingsProfileTab } from '../components/app/SettingsProfileTab'
import { SettingsInstanceTab } from '../components/app/SettingsInstanceTab'
import { SettingsLicenseTab } from '../components/app/SettingsLicenseTab'
import { SettingsLicensesTab } from '../components/app/SettingsLicensesTab'
import { SettingsSearchIndexTab } from '../components/app/SettingsSearchIndexTab'
import { SettingsSummariesTab } from '../components/app/SettingsSummariesTab'
import { SettingsSyncTab } from '../components/app/SettingsSyncTab'
import { SettingsUsersTab } from '../components/app/SettingsUsersTab'
import { Badge } from '../components/ui/badge'
import { Button } from '../components/ui/button'
import { useMe } from '../lib/queries/auth'
import { useMyLicenses } from '../lib/queries/billing'
import { useOrgs } from '../lib/queries/orgs'
import { cn } from '../lib/utils'

interface SettingsTab {
  id: string
  label: string
  render: () => React.ReactNode
  // Optional unread count shown as a badge in the left nav, so a notification
  // surfaced elsewhere (e.g. the unseen-feedback dot on the account menu) points
  // at the tab it actually belongs to instead of going cold at /settings.
  badge?: number
  // Opt out of the 48rem reading measure. Settings is prose-width by default —
  // right for forms and copy, too narrow for a data table with a dozen columns.
  wide?: boolean
}

// A labeled cluster of tabs in the left nav — the label says why you can see them.
interface SettingsGroup {
  label: string
  tabs: SettingsTab[]
}

const PROFILE_TAB: SettingsTab = {
  id: 'profile',
  label: 'Profile',
  render: () => <SettingsProfileTab />,
}

const NOTIFICATIONS_TAB: SettingsTab = {
  id: 'notifications',
  label: 'Notifications',
  render: () => <SettingsNotificationsTab />,
}

const FOLLOWING_TAB: SettingsTab = {
  id: 'following',
  label: 'Following',
  render: () => <SettingsFollowingTab />,
}

const IMPORT_TAB: SettingsTab = {
  id: 'import',
  label: 'Import',
  render: () => <ImportSection />,
}

const API_KEYS_TAB: SettingsTab = {
  id: 'api-keys',
  label: 'API Keys',
  render: () => <SettingsApiKeysTab />,
}

const USERS_TAB: SettingsTab = {
  id: 'users',
  label: 'Users',
  wide: true,
  render: () => <SettingsUsersTab />,
}

const ORGS_TAB: SettingsTab = {
  id: 'orgs',
  label: 'Organizations',
  render: () => <SettingsOrgsTab scope="instance" />,
}

// The org-admin self-service variant — shown to non-instance-admins who
// administer at least one org. Scoped to their orgs; no create/delete/domains.
const ORG_ADMIN_TAB: SettingsTab = {
  id: 'orgs',
  label: 'Organizations',
  render: () => <SettingsOrgsTab scope="admin" />,
}

const AUDIT_TAB: SettingsTab = {
  id: 'audit',
  label: 'Audit',
  render: () => <SettingsAuditTab />,
}

// Unified activity feed — every event on the instance, filterable. Instance-admin
// only. The firehose to Audit's focused access-control view.
const EVENTS_TAB: SettingsTab = {
  id: 'events',
  label: 'Events',
  render: () => <SettingsEventsTab />,
}

// Instance-analytics dashboard — activity trends, growth, leaderboards, AI +
// error pulse, knowledge health. Instance-admin only. The visual overview that
// sits above the focused Usage / Events / Errors tabs.
const INSIGHTS_TAB: SettingsTab = {
  id: 'insights',
  label: 'Insights',
  render: () => <SettingsInsightsTab />,
}

// Instance-wide usage overview — totals, top AI consumers, knowledge gaps.
// Instance-admin only.
const USAGE_TAB: SettingsTab = {
  id: 'usage',
  label: 'Usage',
  render: () => <SettingsUsageTab />,
}

// Grouped browser-error "Issues" view — client.error reports collapsed by
// fingerprint. Instance-admin only. The triage companion to the raw Events feed.
const ERRORS_TAB: SettingsTab = {
  id: 'errors',
  label: 'Errors',
  render: () => <SettingsErrorsTab />,
}

// Inbox for feedback submitted via the in-app form / MCP submit_feedback tool.
// Instance-admin only.
const FEEDBACK_TAB: SettingsTab = {
  id: 'feedback',
  label: 'Feedback',
  render: () => <SettingsFeedbackTab />,
}

// Instance-wide runtime config (settings substrate) — instance-admin only.
const INSTANCE_TAB: SettingsTab = {
  id: 'instance',
  label: 'Instance',
  render: () => <SettingsInstanceTab />,
}

// Search index freshness — available to all users (scoped to their own spaces).
const SEARCH_INDEX_TAB: SettingsTab = {
  id: 'search-index',
  label: 'Search index',
  render: () => <SettingsSearchIndexTab />,
}

// Auto-summary freshness — available to all users (scoped to their own spaces).
const SUMMARIES_TAB: SettingsTab = {
  id: 'summaries',
  label: 'Summaries',
  render: () => <SettingsSummariesTab />,
}

// "Connect your vault" — user self-service WebDAV sync, available to everyone
// (the backend gates token scope on the user's own space membership).
const SYNC_TAB: SettingsTab = {
  id: 'sync',
  label: 'Sync',
  render: () => <SettingsSyncTab />,
}

// Plan & usage — every account (personal + each org) carries a tier; available
// to all users.
const BILLING_TAB: SettingsTab = {
  id: 'billing',
  label: 'Plan & Usage',
  render: () => <SettingsBillingTab />,
}

// Self-host Enterprise license — instance-admin. Install/view/remove the key that
// unlocks ee-gated features on this instance.
const LICENSE_TAB: SettingsTab = {
  id: 'license',
  label: 'License',
  render: () => <SettingsLicenseTab />,
}

// Buyer-facing self-host license purchase/retrieval (managed cloud). Only shown
// where this instance can sell them, or when the user already owns a key.
const LICENSES_TAB: SettingsTab = {
  id: 'licenses',
  label: 'Self-host licenses',
  render: () => <SettingsLicensesTab />,
}

export function SettingsPage() {
  const me = useMe()
  const orgs = useOrgs()
  // The Users + API Keys tabs are gated on instance-admin; the array itself
  // drops them for non-admins so /settings looks identical to today's
  // Profile-only shell. The backend gates /api/api_keys on instance-admin
  // too — mounting the tab for non-admins would just render a perpetual 403.
  // A non-instance-admin who administers an org gets a scoped Organizations
  // tab (member/group management + audit for their own orgs).
  const isOrgAdmin =
    !me.data?.is_instance_admin &&
    (orgs.data?.some((o) => o.my_role === 'admin') ?? false)
  // The unseen-feedback count drives both the account-menu dot and the badge on
  // the Feedback tab below, so the two always agree.
  const feedbackUnseen = me.data?.feedback_unseen ?? 0
  // Grouped so it's clear WHY each section is visible: "Account" is everyone's,
  // "Organization" appears because you administer an org, "Instance admin" because
  // you're an instance admin.
  // The buyer-facing Self-host licenses tab appears only where the instance can
  // sell them (managed cloud, wired) or the user already owns a key — so it stays
  // invisible on a plain self-hosted instance.
  const licenses = useMyLicenses()
  const showLicenses = (licenses.data?.sales_enabled ?? false) || (licenses.data?.licenses.length ?? 0) > 0
  const groups = useMemo<SettingsGroup[]>(() => {
    const account = [PROFILE_TAB, NOTIFICATIONS_TAB, FOLLOWING_TAB, BILLING_TAB, API_KEYS_TAB, IMPORT_TAB, SEARCH_INDEX_TAB, SUMMARIES_TAB, SYNC_TAB]
    if (showLicenses) account.splice(4, 0, LICENSES_TAB) // after Plan & Usage
    if (me.data?.is_instance_admin) {
      return [
        { label: 'Account', tabs: account },
        { label: 'Instance admin', tabs: [INSIGHTS_TAB, USERS_TAB, ORGS_TAB, USAGE_TAB, { ...FEEDBACK_TAB, badge: feedbackUnseen }, EVENTS_TAB, ERRORS_TAB, AUDIT_TAB, LICENSE_TAB, INSTANCE_TAB] },
      ]
    }
    if (isOrgAdmin) {
      return [
        { label: 'Account', tabs: account },
        { label: 'Organization', tabs: [ORG_ADMIN_TAB] },
      ]
    }
    return [{ label: 'Account', tabs: account }]
  }, [me.data?.is_instance_admin, isOrgAdmin, feedbackUnseen, showLicenses])
  const tabs = useMemo<SettingsTab[]>(() => groups.flatMap((g) => g.tabs), [groups])
  // The active section lives in the URL (`?tab=`), not local state, so a refresh
  // or a shared link lands on the same tab instead of resetting to Profile. When
  // the param is absent (or names a tab this user can't see) it falls back to the
  // first tab — and once `me` loads and gates in the admin tabs, an admin-tab
  // param resolves on the next render.
  const navigate = useNavigate()
  const { tab: tabParam } = useSearch({ from: '/_app/settings' })
  const active = tabs.find((t) => t.id === tabParam) ?? tabs[0]
  // Replace (not push) so switching tabs doesn't stack history entries — Back
  // leaves Settings rather than walking back through every tab visited.
  const selectTab = (id: string) =>
    void navigate({ to: '/settings', search: { tab: id }, replace: true })

  return (
    <div className="flex-1 flex min-h-0">
      <nav
        aria-label="Settings sections"
        className="shrink-0 w-[var(--space-8)] sm:w-[14rem] border-r border-[var(--border-subtle)] bg-[var(--surface-2)] py-[var(--space-4)] px-[var(--space-3)] flex flex-col gap-[var(--space-4)]"
      >
        {groups.map((group) => (
          <div key={group.label} className="flex flex-col gap-[var(--space-1)]">
            <span className="hidden sm:block px-[var(--space-2)] pb-[var(--space-1)] text-[length:var(--text-xs)] font-medium uppercase tracking-wide text-[var(--text-muted)]">
              {group.label}
            </span>
            {group.tabs.map((tab) => {
              const isActive = tab.id === active.id
              return (
                <Button
                  key={tab.id}
                  type="button"
                  variant="ghost"
                  size="sm"
                  className={cn(
                    'w-full justify-start',
                    isActive &&
                      'bg-[var(--surface-3)] text-[var(--text-primary)] font-medium',
                  )}
                  aria-current={isActive ? 'page' : undefined}
                  onClick={() => selectTab(tab.id)}
                >
                  <span className="flex-1 text-left truncate">{tab.label}</span>
                  {tab.badge ? (
                    <Badge variant="accent" className="ml-auto shrink-0">
                      {tab.badge}
                    </Badge>
                  ) : null}
                </Button>
              )
            })}
          </div>
        ))}
      </nav>
      <div className="flex-1 overflow-y-auto">
        <div
          className={cn(
            'w-full mx-auto p-[var(--space-7)] flex flex-col gap-[var(--space-6)]',
            active.wide ? 'max-w-[84rem]' : 'max-w-[48rem]',
          )}
        >
          <header className="flex flex-col gap-[var(--space-1)]">
            <h1 className="m-0 font-[family-name:var(--font-sans)] text-[length:var(--text-2xl)] leading-[var(--leading-tight)] text-[var(--text-primary)]">
              {active.label}
            </h1>
          </header>
          {active.render()}
        </div>
      </div>
    </div>
  )
}
