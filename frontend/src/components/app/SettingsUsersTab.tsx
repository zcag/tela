import { useCallback, useMemo, useState } from 'react'
import {
  Boxes,
  Download,
  MoreHorizontal,
  Plug,
  Search,
  Sparkles,
  UserPlus,
} from 'lucide-react'
import { ApiError } from '../../lib/api'
import { useMe } from '../../lib/queries/auth'
import {
  useAdminUsers,
  useAdminUserActivity,
  useCreateAdminUser,
  useUpdateAdminUser,
} from '../../lib/queries/admin-users'
import { navigateToPage } from '../../lib/pageHitItem'
import {
  Sheet,
  SheetBody,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '../ui/sheet'
import {
  daysSinceSqlite,
  localDateFromSqlite,
  relativeTimeFromSqlite,
} from '../../lib/relativeTime'
import { formatBytes } from '../../lib/format'
import type {
  AdminUserMetrics,
  AdminUserRow,
  AdminUserUsage,
  AdminUserWindow,
} from '../../lib/types'
import { PlanTierSelect } from './PlanTierSelect'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Checkbox } from '../ui/checkbox'
import { DataTable, type DataTableColumn } from '../ui/data-table'
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '../ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '../ui/dropdown-menu'
import { Input } from '../ui/input'
import { cn } from '../../lib/utils'

const MIN_PASSWORD_LEN = 8

type RoleFilter = 'all' | 'admin'
type StatusFilter = 'all' | 'active' | 'inactive'
type McpFilter = 'all' | 'yes'

// A zero-metrics fallback so a row that predates the enrichment (or lost it to
// a failed aggregate) still sorts and renders as "did nothing" rather than
// blanking the whole table.
const NO_METRICS: AdminUserMetrics = {
  edits: 0,
  agent_edits: 0,
  pages_created: 0,
  views: 0,
  asks: 0,
  logins: 0,
  days_active: 0,
  llm_calls: 0,
}

const metricsOf = (row: AdminUserRow): AdminUserMetrics => row.metrics ?? NO_METRICS

export function SettingsUsersTab() {
  const me = useMe()
  const [range, setRange] = useState<AdminUserWindow>('1m')
  const users = useAdminUsers(range)
  const [createOpen, setCreateOpen] = useState(false)
  const [q, setQ] = useState('')
  const [role, setRole] = useState<RoleFilter>('all')
  const [status, setStatus] = useState<StatusFilter>('all')
  const [mcp, setMcp] = useState<McpFilter>('all')
  // Row dialogs live at the tab level, not per row: a table cell is a bad owner
  // for a modal, and only one can be open at a time anyway.
  const [activityFor, setActivityFor] = useState<AdminUserRow | null>(null)
  const [resetFor, setResetFor] = useState<AdminUserRow | null>(null)
  const [rowError, setRowError] = useState<string | null>(null)
  const updateUser = useUpdateAdminUser()

  const all = useMemo(() => users.data?.users ?? [], [users.data])
  const filtered = useMemo(() => {
    const needle = q.trim().toLowerCase()
    return all.filter((u) => {
      if (role === 'admin' && !u.is_instance_admin) return false
      if (status === 'active' && !u.is_active) return false
      if (status === 'inactive' && u.is_active) return false
      if (mcp === 'yes' && !(u.used_mcp || u.has_api_key)) return false
      if (needle) {
        const hay = `${u.username} ${u.display_name ?? ''} ${u.email ?? ''}`.toLowerCase()
        if (!hay.includes(needle)) return false
      }
      return true
    })
  }, [all, q, role, status, mcp])

  const hasFilters = q.trim() !== '' || role !== 'all' || status !== 'all' || mcp !== 'all'

  // The activity log is pruned on a retention schedule, so the events-derived
  // columns can be shorter than the selected window. Say so only when that's
  // actually true — on a young instance the 30-day window is complete and the
  // caveat would be noise (or worse, read as a bug).
  const eventsSince = users.data?.events_since ?? ''
  const retentionClips =
    eventsSince !== '' &&
    (range === 'all' || daysSinceSqlite(eventsSince) < (range === '3m' ? 90 : 30))

  const runUpdate = useCallback(
    async (row: AdminUserRow, input: { is_active?: boolean; is_instance_admin?: boolean }) => {
      setRowError(null)
      try {
        await updateUser.mutateAsync({ id: row.id, ...input })
      } catch (err) {
        if (err instanceof ApiError && err.code === 'last_admin') {
          setRowError(
            "Can't deactivate or demote the last instance admin — promote someone first.",
          )
        } else if (err instanceof ApiError) {
          setRowError(`${row.username}: ${err.message}`)
        } else {
          setRowError(`${row.username}: something went wrong. Try again.`)
        }
      }
    },
    [updateUser],
  )

  const columns = useMemo<DataTableColumn<AdminUserRow>[]>(
    () => [
      {
        key: 'person',
        header: 'Person',
        // Sort on what's displayed — the display name when there is one, so the
        // column reads alphabetically rather than by a hidden handle.
        sortValue: (u) => (u.display_name || u.username).toLowerCase(),
        cell: (u) => (
          <div className="flex flex-col gap-[2px] min-w-[11rem]">
            <div className="flex items-center gap-[var(--space-2)] flex-wrap">
              <span className="font-medium">{u.display_name || u.username}</span>
              {u.is_instance_admin ? <Badge variant="accent">Admin</Badge> : null}
              {!u.is_active ? <Badge variant="muted">Deactivated</Badge> : null}
              {me.data?.id === u.id ? <Badge variant="muted">You</Badge> : null}
              <McpBadge row={u} />
            </div>
            <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">
              {u.display_name ? `@${u.username}` : u.email || `Joined ${localDateFromSqlite(u.created_at)}`}
            </span>
          </div>
        ),
      },
      {
        key: 'edits',
        header: 'Edits',
        title: 'Page revisions authored in this window. The sub-line is how many came through an agent (MCP).',
        numeric: true,
        sortValue: (u) => metricsOf(u).edits,
        cell: (u) => {
          const m = metricsOf(u)
          return (
            <div className="flex flex-col items-end">
              <Count n={m.edits} />
              {m.agent_edits > 0 ? (
                <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">
                  {m.agent_edits} agent
                </span>
              ) : null}
            </div>
          )
        },
      },
      {
        key: 'pages',
        header: 'Pages',
        title: 'Pages this user created in the window (they authored the page’s first revision).',
        numeric: true,
        sortValue: (u) => metricsOf(u).pages_created,
        cell: (u) => <Count n={metricsOf(u).pages_created} />,
      },
      {
        key: 'views',
        header: 'Views',
        title: 'Page views recorded for this user. Limited by the activity-log retention window.',
        numeric: true,
        sortValue: (u) => metricsOf(u).views,
        cell: (u) => <Count n={metricsOf(u).views} />,
      },
      {
        key: 'asks',
        header: 'Asks',
        title: 'Questions asked of the wiki (Ask).',
        numeric: true,
        sortValue: (u) => metricsOf(u).asks,
        cell: (u) => <Count n={metricsOf(u).asks} />,
      },
      {
        key: 'ai',
        header: 'AI',
        title: 'Metered AI calls. Counted per calendar month, so this follows month boundaries rather than the exact window.',
        numeric: true,
        sortValue: (u) => metricsOf(u).llm_calls,
        cell: (u) => <Count n={metricsOf(u).llm_calls} />,
      },
      {
        key: 'days',
        header: 'Days',
        title: 'Distinct days with any recorded activity — the honest "is this account alive" number: one busy afternoon and a month of daily use look the same under a raw event count.',
        numeric: true,
        sortValue: (u) => metricsOf(u).days_active,
        cell: (u) => <Count n={metricsOf(u).days_active} />,
      },
      {
        key: 'last_active',
        header: 'Last seen',
        // Never-signed-in sorts below every real timestamp in both directions.
        sortValue: (u) => u.last_active_at ?? '',
        cell: (u) =>
          u.last_active_at ? (
            <span className="whitespace-nowrap">{relativeTimeFromSqlite(u.last_active_at)}</span>
          ) : (
            <span className="text-[var(--text-muted)]">Never</span>
          ),
      },
      {
        key: 'storage',
        header: 'Storage',
        title: 'Owned spaces and attachment bytes, against the account’s plan limits.',
        sortValue: (u) => u.usage?.storage_bytes ?? -1,
        cell: (u) => {
          const usage = u.usage
          // No spaces and nothing stored is the common case — a row of "0 · 0 B"
          // is just noise next to the accounts that actually hold something.
          if (!usage || (usage.spaces === 0 && usage.storage_bytes === 0)) {
            return <span className="text-[var(--text-muted)]">—</span>
          }
          return (
            <span
              className={cn(
                'whitespace-nowrap tabular-nums',
                isOverLimit(usage) ? 'text-[var(--danger)] font-medium' : '',
              )}
            >
              {usage.spaces} · {formatBytes(usage.storage_bytes)}
            </span>
          )
        },
      },
      {
        key: 'plan',
        header: 'Plan',
        sortValue: (u) => u.plan_key,
        // The select is a control, not a link into the row — swallow the click
        // so changing a plan doesn't also open the activity sheet behind it.
        cell: (u) => (
          <div onClick={(e) => e.stopPropagation()}>
            <PlanTierSelect
              accountKind="user"
              accountId={u.id}
              currentKey={u.plan_key}
              className="w-[7.5rem]"
            />
          </div>
        ),
      },
      {
        key: 'actions',
        header: <span className="sr-only">Actions</span>,
        cell: (u) =>
          me.data?.id === u.id ? null : (
            <div onClick={(e) => e.stopPropagation()} className="text-right">
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button
                    variant="ghost"
                    size="sm"
                    aria-label={`Actions for ${u.username}`}
                    className="h-[var(--space-7)] w-[var(--space-7)] p-0"
                    disabled={updateUser.isPending}
                  >
                    <MoreHorizontal width={14} height={14} />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuItem onSelect={() => setActivityFor(u)}>
                    View activity
                  </DropdownMenuItem>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem onSelect={() => setResetFor(u)}>
                    Reset password
                  </DropdownMenuItem>
                  <DropdownMenuItem
                    onSelect={() => void runUpdate(u, { is_instance_admin: !u.is_instance_admin })}
                  >
                    {u.is_instance_admin ? 'Revoke instance admin' : 'Make instance admin'}
                  </DropdownMenuItem>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem
                    destructive={u.is_active}
                    onSelect={() => void runUpdate(u, { is_active: !u.is_active })}
                  >
                    {u.is_active ? 'Deactivate' : 'Reactivate'}
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          ),
      },
    ],
    [me.data?.id, runUpdate, updateUser.isPending],
  )

  return (
    <section
      aria-labelledby="settings-users"
      className="flex flex-col gap-[var(--space-4)]"
    >
      <header className="flex items-start justify-between gap-[var(--space-3)]">
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)] leading-[var(--leading-relaxed)]">
          Everyone on this instance, with what they've actually done. Sort any
          column to find your most active people — or the accounts that never
          got going. Row actions reset passwords, grant instance-admin, and
          deactivate sign-ins.
        </p>
        <div className="flex items-center gap-[var(--space-2)] shrink-0">
          <Button
            type="button"
            variant="ghost"
            onClick={() => exportUsersCsv(filtered, range)}
            disabled={filtered.length === 0}
            title="Download the rows below as CSV"
          >
            <Download width={14} height={14} />
            <span>Export</span>
          </Button>
          <Button type="button" variant="primary" onClick={() => setCreateOpen(true)}>
            <UserPlus width={14} height={14} />
            <span>Create user</span>
          </Button>
        </div>
      </header>

      {/* Filter bar. The window pills sit first — they change what every
          activity column MEANS, so they read as the frame for the rest. */}
      <div className="flex flex-wrap items-center gap-[var(--space-2)]">
        <FilterPills<AdminUserWindow>
          value={range}
          onChange={setRange}
          options={[
            ['1m', 'Last 30 days'],
            ['3m', 'Last 3 months'],
            ['all', 'All time'],
          ]}
        />
        <div className="relative flex-1 min-w-[12rem]">
          <Search
            width={14}
            height={14}
            aria-hidden
            className="pointer-events-none absolute left-[var(--space-2)] top-1/2 -translate-y-1/2 text-[var(--text-muted)]"
          />
          <Input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Search name or email…"
            aria-label="Search users"
            className="pl-[var(--space-6)]"
          />
        </div>
        <FilterPills<RoleFilter>
          value={role}
          onChange={setRole}
          options={[
            ['all', 'All'],
            ['admin', 'Admins'],
          ]}
        />
        <FilterPills<StatusFilter>
          value={status}
          onChange={setStatus}
          options={[
            ['all', 'Any status'],
            ['active', 'Active'],
            ['inactive', 'Inactive'],
          ]}
        />
        <FilterPills<McpFilter>
          value={mcp}
          onChange={setMcp}
          options={[
            ['all', 'Any MCP'],
            ['yes', 'MCP set up'],
          ]}
        />
      </div>

      {rowError ? (
        <p role="alert" className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]">
          {rowError}
        </p>
      ) : null}

      {users.isLoading ? (
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
          Loading users…
        </p>
      ) : users.isError ? (
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]">
          Couldn't load users.
        </p>
      ) : (
        <>
          <SummaryStrip rows={filtered} />
          <DataTable
            rows={filtered}
            columns={columns}
            rowKey={(u) => u.id}
            defaultSort={{ key: 'edits', dir: 'desc' }}
            onRowClick={(u) => setActivityFor(u)}
            caption="Users on this instance with their activity"
            empty={hasFilters ? 'No users match these filters.' : 'No users found.'}
          />
          <p className="m-0 text-[length:var(--text-xs)] text-[var(--text-muted)] tabular-nums">
            {filtered.length} of {all.length} {all.length === 1 ? 'user' : 'users'}
            {retentionClips ? (
              <>
                {' · '}
                <span>
                  Views, sign-ins and days-active only go back to{' '}
                  <span className="tabular-nums">{localDateFromSqlite(eventsSince)}</span>{' '}
                  (activity-log retention)
                </span>
              </>
            ) : null}
          </p>
        </>
      )}

      <CreateUserDialog open={createOpen} onOpenChange={setCreateOpen} />
      {resetFor ? (
        <ResetPasswordDialog
          user={resetFor}
          open
          onOpenChange={(next) => !next && setResetFor(null)}
        />
      ) : null}
      {activityFor ? (
        <UserActivitySheet
          user={activityFor}
          open
          onOpenChange={(next) => !next && setActivityFor(null)}
        />
      ) : null}
    </section>
  )
}

// Totals across the rows currently in view — the same numbers the table holds,
// added up, so filtering to a segment ("admins", "MCP set up") answers "how
// much does this group actually do" without a second endpoint.
function SummaryStrip({ rows }: { rows: AdminUserRow[] }) {
  const t = useMemo(() => {
    return rows.reduce(
      (acc, u) => {
        const m = metricsOf(u)
        acc.edits += m.edits
        acc.agentEdits += m.agent_edits
        acc.pages += m.pages_created
        acc.asks += m.asks
        acc.ai += m.llm_calls
        if (m.days_active > 0) acc.active += 1
        return acc
      },
      { edits: 0, agentEdits: 0, pages: 0, asks: 0, ai: 0, active: 0 },
    )
  }, [rows])
  const agentShare = t.edits > 0 ? Math.round((t.agentEdits / t.edits) * 100) : 0
  return (
    <dl className="m-0 grid grid-cols-2 sm:grid-cols-5 gap-[var(--space-2)]">
      <SummaryStat label="Active people" value={`${t.active} of ${rows.length}`} />
      <SummaryStat label="Edits" value={t.edits.toLocaleString()} sub={t.edits > 0 ? `${agentShare}% by agents` : undefined} />
      <SummaryStat label="Pages created" value={t.pages.toLocaleString()} />
      <SummaryStat label="Asks" value={t.asks.toLocaleString()} />
      <SummaryStat label="AI calls" value={t.ai.toLocaleString()} />
    </dl>
  )
}

function SummaryStat({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div className="flex flex-col gap-[2px] rounded-[var(--radius-sm)] border border-[var(--border-subtle)] bg-[var(--surface-1)] px-[var(--space-3)] py-[var(--space-2)]">
      <dt className="m-0 text-[length:var(--text-xs)] text-[var(--text-muted)]">{label}</dt>
      <dd className="m-0 text-[length:var(--text-base)] font-medium tabular-nums text-[var(--text-primary)]">
        {value}
      </dd>
      {sub ? (
        <dd className="m-0 text-[length:var(--text-xs)] text-[var(--text-muted)]">{sub}</dd>
      ) : null}
    </div>
  )
}

// A count cell: zero is real data, but rendering a wall of 0s buries the rows
// that matter, so it dims to a dash.
function Count({ n }: { n: number }) {
  if (n === 0) return <span className="text-[var(--text-muted)]">—</span>
  return <span>{n.toLocaleString()}</span>
}

// Download the rows currently in view. Client-side on purpose: the table
// already holds every number, so an export endpoint would be a second place
// for the same columns to drift.
function exportUsersCsv(rows: AdminUserRow[], range: AdminUserWindow) {
  const header = [
    'username', 'display_name', 'email', 'plan', 'instance_admin', 'active',
    'created_at', 'last_active_at', 'orgs', 'spaces', 'storage_bytes',
    'edits', 'agent_edits', 'pages_created', 'views', 'asks', 'logins',
    'days_active', 'llm_calls', 'mcp',
  ]
  const cell = (v: unknown) => {
    const s = v == null ? '' : String(v)
    return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
  }
  const lines = [header.join(',')]
  for (const u of rows) {
    const m = metricsOf(u)
    lines.push([
      u.username, u.display_name, u.email ?? '', u.plan_key,
      u.is_instance_admin, u.is_active, u.created_at, u.last_active_at ?? '',
      u.orgs ?? 0, u.usage?.spaces ?? '', u.usage?.storage_bytes ?? '',
      m.edits, m.agent_edits, m.pages_created, m.views, m.asks, m.logins,
      m.days_active, m.llm_calls, u.used_mcp ? 'yes' : u.has_api_key ? 'key-only' : 'no',
    ].map(cell).join(','))
  }
  const blob = new Blob([lines.join('\n')], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `tela-users-${range}-${new Date().toISOString().slice(0, 10)}.csv`
  a.click()
  URL.revokeObjectURL(url)
}

// A small segmented control of mutually-exclusive filter values.
function FilterPills<T extends string>({
  value,
  onChange,
  options,
}: {
  value: T
  onChange: (next: T) => void
  options: [T, string][]
}) {
  return (
    <div className="inline-flex items-center gap-[1px] rounded-[var(--radius-sm)] border border-[var(--border-subtle)] p-[2px]">
      {options.map(([val, label]) => (
        <button
          key={val}
          type="button"
          onClick={() => onChange(val)}
          aria-pressed={value === val}
          className={cn(
            'rounded-[var(--radius-xs)] px-[var(--space-2)] py-[2px] text-[length:var(--text-xs)] font-medium',
            'outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]',
            value === val
              ? 'bg-[var(--surface-3)] text-[var(--text-primary)]'
              : 'text-[var(--text-muted)] hover:text-[var(--text-primary)]',
          )}
        >
          {label}
        </button>
      ))}
    </div>
  )
}

// Instance-wide recent edits by one user — the latest edit per page, newest
// first. Reuses the recent-changes feed shape; querying is deferred until the
// drawer opens. Clicking a row jumps to that page (which leaves Settings).
function UserActivitySheet({
  user,
  open,
  onOpenChange,
}: {
  user: AdminUserRow
  open: boolean
  onOpenChange: (next: boolean) => void
}) {
  const activity = useAdminUserActivity(user.id, open)
  const u = user.usage
  const m = metricsOf(user)
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="w-[min(28rem,100vw)]">
        <SheetHeader>
          <SheetTitle>{user.display_name || user.username}</SheetTitle>
          <SheetDescription>
            @{user.username}
            {user.email ? ` · ${user.email}${user.email_verified ? ' ✓' : ' (unverified)'}` : ''}
          </SheetDescription>
        </SheetHeader>
        <SheetBody>
          {/* Stat grid — the account facts the table's activity columns don't
              carry (quota, orgs, MCP, when they joined). */}
          <div className="mb-[var(--space-4)] grid grid-cols-2 gap-[var(--space-2)]">
            <DetailStat icon={<Boxes width={14} height={14} />} label="Spaces" value={u ? String(u.spaces) : '—'} />
            <DetailStat icon={<Boxes width={14} height={14} />} label="Orgs" value={String(user.orgs ?? 0)} />
            <DetailStat icon={<Sparkles width={14} height={14} />} label="AI calls" value={String(m.llm_calls)} />
            <DetailStat
              icon={<Plug width={14} height={14} />}
              label="MCP"
              value={
                user.mcp_last_seen_at
                  ? `Connected · ${relativeTimeFromSqlite(user.mcp_last_seen_at)}`
                  : user.used_mcp
                    ? 'Connected'
                    : user.has_api_key
                      ? 'Key, no use'
                      : 'Not set up'
              }
            />
            <DetailStat label="Attachments" value={u ? formatBytes(u.storage_bytes) : '—'} />
            <DetailStat label="Joined" value={localDateFromSqlite(user.created_at)} />
            <DetailStat
              label="Last active"
              value={user.last_active_at ? relativeTimeFromSqlite(user.last_active_at) : 'Never'}
            />
            <DetailStat label="Sign-ins" value={String(m.logins)} />
          </div>
          <p className="m-0 mb-[var(--space-2)] text-[length:var(--text-xs)] font-medium uppercase tracking-wide text-[var(--text-muted)]">
            Recent edits
          </p>
          {activity.isLoading ? (
            <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
              Loading…
            </p>
          ) : activity.isError ? (
            <p className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]">
              Couldn't load activity.
            </p>
          ) : activity.data && activity.data.length > 0 ? (
            <ul className="m-0 p-0 list-none flex flex-col gap-[var(--space-1)]">
              {activity.data.map((c) => (
                <li key={c.page_id}>
                  <button
                    type="button"
                    onClick={() => {
                      onOpenChange(false)
                      navigateToPage(c.space_id, c.page_id)
                    }}
                    className={cn(
                      'w-full text-left flex flex-col gap-[2px]',
                      'px-[var(--space-3)] py-[var(--space-2)]',
                      'rounded-[var(--radius-sm)] bg-transparent border-0 cursor-pointer',
                      'outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]',
                      'hover:bg-[var(--surface-2)]',
                    )}
                  >
                    <span className="truncate text-[length:var(--text-sm)] text-[var(--text-primary)] font-[family-name:var(--font-sans)]">
                      {c.title || 'Untitled'}
                    </span>
                    <span className="text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
                      {c.space_name} · {relativeTimeFromSqlite(c.updated_at)}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          ) : (
            <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
              No edits yet.
            </p>
          )}
        </SheetBody>
      </SheetContent>
    </Sheet>
  )
}

// True when usage has crossed a finite plan limit (storage or spaces) — drives
// the danger styling so an over-quota account stands out at a glance.
function isOverLimit(u: AdminUserUsage): boolean {
  return (
    (u.max_storage_bytes != null && u.storage_bytes > u.max_storage_bytes) ||
    (u.max_spaces != null && u.spaces > u.max_spaces)
  )
}

// The MCP-setup signal: an accent "MCP" badge once the user has actually hit
// /api/mcp; a quieter "API key" mark if they have a PAT but no MCP traffic yet;
// nothing otherwise. (OAuth/cowork connections leave no DB trace, so a green
// badge can under-report — never over-report.)
function McpBadge({ row }: { row: AdminUserRow }) {
  if (row.used_mcp) return <Badge variant="accent">MCP</Badge>
  if (row.has_api_key) return <Badge variant="muted">API key</Badge>
  return null
}

// One labeled stat in the user detail sheet's grid.
function DetailStat({
  icon,
  label,
  value,
}: {
  icon?: React.ReactNode
  label: string
  value: string
}) {
  return (
    <div className="flex flex-col gap-[2px] rounded-[var(--radius-sm)] border border-[var(--border-subtle)] bg-[var(--surface-1)] px-[var(--space-3)] py-[var(--space-2)]">
      <span className="inline-flex items-center gap-[var(--space-1)] text-[length:var(--text-xs)] text-[var(--text-muted)]">
        {icon}
        {label}
      </span>
      <span className="text-[length:var(--text-sm)] font-medium text-[var(--text-primary)]">
        {value}
      </span>
    </div>
  )
}

function CreateUserDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (next: boolean) => void
}) {
  const [username, setUsername] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [makeAdmin, setMakeAdmin] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const createUser = useCreateAdminUser()

  function reset() {
    setUsername('')
    setEmail('')
    setPassword('')
    setMakeAdmin(false)
    setError(null)
  }

  function handleClose(next: boolean) {
    if (!next) reset()
    onOpenChange(next)
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    const trimmedUsername = username.trim()
    if (!trimmedUsername) {
      setError('Username is required.')
      return
    }
    if (password.length < MIN_PASSWORD_LEN) {
      setError(`Password must be at least ${MIN_PASSWORD_LEN} characters.`)
      return
    }
    setError(null)
    const trimmedEmail = email.trim()
    try {
      await createUser.mutateAsync({
        username: trimmedUsername,
        ...(trimmedEmail ? { email: trimmedEmail } : {}),
        password,
        is_instance_admin: makeAdmin,
      })
      handleClose(false)
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        setError('That username or email is already taken.')
      } else if (err instanceof ApiError) {
        setError(err.message)
      } else {
        setError('Failed to create user.')
      }
    }
  }

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Create a new user</DialogTitle>
          <DialogDescription>
            The user can change their password later from Settings → Profile.
          </DialogDescription>
        </DialogHeader>
        <form
          onSubmit={handleSubmit}
          className="flex flex-col gap-[var(--space-3)]"
          noValidate
        >
          <div className="flex flex-col gap-[var(--space-2)]">
            <label
              htmlFor="new-user-username"
              className="text-[length:var(--text-sm)] text-[var(--text-muted)]"
            >
              Username
            </label>
            <Input
              id="new-user-username"
              autoFocus
              autoComplete="off"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              aria-invalid={error != null}
            />
          </div>
          <div className="flex flex-col gap-[var(--space-2)]">
            <label
              htmlFor="new-user-email"
              className="text-[length:var(--text-sm)] text-[var(--text-muted)]"
            >
              Email <span className="text-[var(--text-muted)]">(optional)</span>
            </label>
            <Input
              id="new-user-email"
              type="email"
              autoComplete="off"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              aria-invalid={error != null}
            />
          </div>
          <div className="flex flex-col gap-[var(--space-2)]">
            <label
              htmlFor="new-user-password"
              className="text-[length:var(--text-sm)] text-[var(--text-muted)]"
            >
              Initial password
            </label>
            <Input
              id="new-user-password"
              type="password"
              autoComplete="new-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              aria-invalid={error != null}
            />
          </div>
          <label className="flex items-center gap-[var(--space-2)] text-[length:var(--text-sm)] text-[var(--text-primary)] cursor-pointer">
            <Checkbox
              checked={makeAdmin}
              onCheckedChange={(next) => setMakeAdmin(next === true)}
            />
            <span>Make this user an instance admin</span>
          </label>
          {error ? (
            <p
              role="alert"
              className="m-0 text-[length:var(--text-xs)] text-[var(--danger)]"
            >
              {error}
            </p>
          ) : null}
          <DialogFooter>
            <DialogClose asChild>
              <Button type="button" variant="ghost">
                Cancel
              </Button>
            </DialogClose>
            <Button type="submit" disabled={createUser.isPending}>
              {createUser.isPending ? 'Creating…' : 'Create user'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function ResetPasswordDialog({
  user,
  open,
  onOpenChange,
}: {
  user: AdminUserRow
  open: boolean
  onOpenChange: (next: boolean) => void
}) {
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const updateUser = useUpdateAdminUser()

  function handleClose(next: boolean) {
    if (!next) {
      setPassword('')
      setError(null)
    }
    onOpenChange(next)
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (password.length < MIN_PASSWORD_LEN) {
      setError(`Password must be at least ${MIN_PASSWORD_LEN} characters.`)
      return
    }
    setError(null)
    try {
      await updateUser.mutateAsync({ id: user.id, password })
      handleClose(false)
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message)
      } else {
        setError('Failed to reset password.')
      }
    }
  }

  // Stable form id keyed off the user so multiple ResetPasswordDialog
  // instances in the same list don't share an input id.
  const formId = useMemo(() => `reset-password-${user.id}`, [user.id])

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Reset password for {user.username}</DialogTitle>
          <DialogDescription>
            The user will be signed out of every device after this change.
          </DialogDescription>
        </DialogHeader>
        <form
          onSubmit={handleSubmit}
          className="flex flex-col gap-[var(--space-3)]"
          noValidate
        >
          <div className="flex flex-col gap-[var(--space-2)]">
            <label
              htmlFor={formId}
              className="text-[length:var(--text-sm)] text-[var(--text-muted)]"
            >
              New password
            </label>
            <Input
              id={formId}
              type="password"
              autoComplete="new-password"
              autoFocus
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              aria-invalid={error != null}
            />
          </div>
          {error ? (
            <p
              role="alert"
              className="m-0 text-[length:var(--text-xs)] text-[var(--danger)]"
            >
              {error}
            </p>
          ) : null}
          <DialogFooter>
            <DialogClose asChild>
              <Button type="button" variant="ghost">
                Cancel
              </Button>
            </DialogClose>
            <Button type="submit" disabled={updateUser.isPending}>
              {updateUser.isPending ? 'Saving…' : 'Reset password'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
