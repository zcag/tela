import { useCallback, useMemo, useState } from 'react'
import { useNavigate, useSearch } from '@tanstack/react-router'
import {
  Boxes,
  Download,
  HelpCircle,
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
  shortRelativeFromSqlite,
} from '../../lib/relativeTime'
import { formatBytes } from '../../lib/format'
import type {
  AdminUserMetrics,
  AdminUserRow,
  AdminUserSegment,
  AdminUserUsage,
  AdminUserWindow,
} from '../../lib/types'
import { PlanTierSelect } from './PlanTierSelect'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Checkbox } from '../ui/checkbox'
import { DataTable, type DataTableColumn, type SortDir } from '../ui/data-table'
import { Sparkline } from '../ui/sparkline'
import { ActivityGroupsTable } from './ActivityGroupsTable'
import type { ActivityGroupBy } from '../../lib/queries/admin-activity-groups'
import { Popover, PopoverContent, PopoverTrigger } from '../ui/popover'
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
import { EmptyState } from '../ui/empty-state'
import { Input } from '../ui/input'
import { cn } from '../../lib/utils'
import {
  DELTA_WEEKS,
  buildCohorts,
  isNewcomer,
  trendDelta,
} from '../../lib/people-analytics'

const MIN_PASSWORD_LEN = 8

type RoleFilter = 'all' | 'admin'
type StatusFilter = 'all' | 'active' | 'inactive'
type McpFilter = 'all' | 'yes'
type SegmentFilter = 'all' | AdminUserSegment
// What the table is a list OF. People is the default; the other two answer the
// question people actually ask on a multi-team instance ("which team uses
// this?", "which spaces are alive?") without making anyone sum a column by eye.
type GroupBy = 'people' | ActivityGroupBy

// How many trailing weeks the trend sparkline draws. The payload carries 26 (the
// events-retention ceiling) because the cohort grid needs the depth; a row-height
// sparkline can only resolve a quarter of that, and a longer one just looks noisy.
const TREND_WEEKS = 12
// DELTA_WEEKS (4 either side) lives with the arithmetic in lib/people-analytics.
// Active days per week is bounded at 7 by definition, so every sparkline in the
// column shares that domain. Per-series auto-scaling would draw "one day a week"
// and "every day" as the same shape — see Sparkline's `domain` prop.
const WEEK_DOMAIN: [number, number] = [0, 7]

const NO_METRICS: AdminUserMetrics = {
  edits: 0,
  human_edits: 0,
  agent_edits: 0,
  sync_edits: 0,
  pages_created: 0,
  views: 0,
  asks: 0,
  logins: 0,
  days_active: 0,
  llm_calls: 0,
  days30: 0,
  weeks: [],
}

const metricsOf = (row: AdminUserRow): AdminUserMetrics => row.metrics ?? NO_METRICS

// The lifecycle vocabulary, best to worst. Tone rides the semantic accent scale
// rather than --accent, which means "interactive" everywhere else in the app.
const SEGMENTS: {
  key: AdminUserSegment
  label: string
  short: string
  tone: 'positive' | 'accent' | 'muted' | 'warning' | 'negative'
  // `band` fills the proportional bar (a large area, so it's mixed toward the
  // surface); `dot` is the same hue at full strength for the small legend mark.
  band: string
  dot: string
  hint: string
}[] = [
  {
    key: 'power',
    label: 'Power',
    short: 'Power',
    tone: 'positive',
    band: 'color-mix(in srgb, var(--accent-positive-fg) 62%, var(--surface-1))',
    dot: 'var(--accent-positive-fg)',
    hint: '12+ active days in the last 30',
  },
  {
    key: 'regular',
    label: 'Regular',
    short: 'Regular',
    tone: 'accent',
    band: 'color-mix(in srgb, var(--accent) 62%, var(--surface-1))',
    dot: 'var(--accent)',
    hint: '4–11 active days in the last 30',
  },
  {
    key: 'dabbler',
    label: 'Dabbler',
    short: 'Dabbler',
    tone: 'muted',
    band: 'color-mix(in srgb, var(--text-muted) 48%, var(--surface-1))',
    dot: 'var(--text-muted)',
    hint: '1–3 active days in the last 30',
  },
  {
    key: 'churned',
    label: 'Churned',
    short: 'Churned',
    tone: 'warning',
    band: 'color-mix(in srgb, var(--accent-warning-fg) 62%, var(--surface-1))',
    dot: 'var(--accent-warning-fg)',
    hint: 'was active once, then silent for 30+ days',
  },
  {
    key: 'never',
    label: 'Never started',
    short: 'Never',
    tone: 'negative',
    band: 'color-mix(in srgb, var(--accent-negative-fg) 62%, var(--surface-1))',
    dot: 'var(--accent-negative-fg)',
    hint: 'signed up and never did anything',
  },
]
const SEGMENT_BY_KEY = new Map(SEGMENTS.map((s) => [s.key, s]))
// Sort order for the segment column: healthiest first, so a descending sort
// surfaces your champions and an ascending one surfaces the problems.
const SEGMENT_RANK: Record<AdminUserSegment, number> = {
  power: 4,
  regular: 3,
  dabbler: 2,
  churned: 1,
  never: 0,
}

export function SettingsUsersTab() {
  const me = useMe()
  const navigate = useNavigate()
  // The whole view lives in the URL: a filtered, sorted table is a finding, and
  // a finding you can't send to anyone is half a tool.
  const search = useSearch({ from: '/_app/settings' })
  const range = (search.window ?? '1m') as AdminUserWindow
  const segment = (search.seg ?? 'all') as SegmentFilter
  const q = search.q ?? ''
  const sortKey = search.sort ?? 'edits'
  const sortDir = (search.dir ?? 'desc') as SortDir
  const groupBy = (search.by ?? 'people') as GroupBy

  const users = useAdminUsers(range)
  const [createOpen, setCreateOpen] = useState(false)
  const [role, setRole] = useState<RoleFilter>('all')
  const [status, setStatus] = useState<StatusFilter>('all')
  const [mcp, setMcp] = useState<McpFilter>('all')
  const [activityFor, setActivityFor] = useState<AdminUserRow | null>(null)
  const [resetFor, setResetFor] = useState<AdminUserRow | null>(null)
  const [planFor, setPlanFor] = useState<AdminUserRow | null>(null)
  const [rowError, setRowError] = useState<string | null>(null)
  const updateUser = useUpdateAdminUser()

  // Merge into the existing search so changing one control never drops another
  // (and `replace` keeps the back button meaning "leave Settings", not "undo my
  // last filter keystroke").
  const setView = useCallback(
    (patch: Record<string, string | undefined>) => {
      void navigate({
        to: '/settings',
        search: (prev: Record<string, unknown>) => ({ ...prev, tab: 'users', ...patch }),
        replace: true,
      })
    },
    [navigate],
  )

  const all = useMemo(() => users.data?.users ?? [], [users.data])
  const weekAxis = useMemo(() => users.data?.weeks ?? [], [users.data])

  const filtered = useMemo(() => {
    const needle = q.trim().toLowerCase()
    return all.filter((u) => {
      if (role === 'admin' && !u.is_instance_admin) return false
      if (status === 'active' && !u.is_active) return false
      if (status === 'inactive' && u.is_active) return false
      if (mcp === 'yes' && !(u.used_mcp || u.has_api_key)) return false
      if (segment !== 'all' && u.segment !== segment) return false
      if (needle) {
        const hay = `${u.username} ${u.display_name ?? ''} ${u.email ?? ''}`.toLowerCase()
        if (!hay.includes(needle)) return false
      }
      return true
    })
  }, [all, q, role, status, mcp, segment])

  const hasFilters =
    q.trim() !== '' || role !== 'all' || status !== 'all' || mcp !== 'all' || segment !== 'all'

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

  // Jump to the Events tab pre-filtered to this person (and, when given, this
  // kind of event). Admin activity is hidden there by default, so asking about
  // a specific person turns that back on — otherwise clicking an admin's row
  // lands on an empty feed that looks broken.
  const openEvents = useCallback(
    (row: AdminUserRow, types?: string) => {
      void navigate({
        to: '/settings',
        search: { tab: 'events', user: row.id, etypes: types, admins: 1 },
      })
    },
    [navigate],
  )

  // A column of nothing but dashes is worse than no column: on most instances
  // Asks and AI are empty, and hiding them gives the ones that do carry data
  // room to breathe.
  const hasAny = useCallback(
    (pick: (m: AdminUserMetrics) => number) => all.some((u) => pick(metricsOf(u)) > 0),
    [all],
  )

  const columns = useMemo<DataTableColumn<AdminUserRow>[]>(() => {
    const cols: DataTableColumn<AdminUserRow>[] = [
      {
        key: 'person',
        header: 'Person',
        group: 'Who',
        sticky: 'left',
        sortValue: (u) => (u.display_name || u.username).toLowerCase(),
        cell: (u) => <PersonCell row={u} isSelf={me.data?.id === u.id} />,
      },
      {
        key: 'segment',
        header: 'Status',
        group: 'Who',
        title:
          'Lifecycle, from activity over the last 30 days — independent of the selected window, so it describes the person and not the view.',
        sortValue: (u) => (u.segment ? SEGMENT_RANK[u.segment] : -1),
        cell: (u) => <SegmentBadge segment={u.segment} />,
      },
      {
        key: 'trend',
        header: 'Trend',
        group: 'Who',
        title: `Active days per week over the last ${TREND_WEEKS} weeks (all rows share a 0–7 scale, so shapes are comparable), and the change from the previous ${DELTA_WEEKS} weeks to the last ${DELTA_WEEKS}.`,
        // Sort by the delta: "who is ramping up" and "who is falling off" are
        // the two ends of the same column.
        sortValue: (u) => trendDelta(metricsOf(u).weeks) ?? -999,
        cell: (u) => <TrendCell row={u} />,
      },
      {
        key: 'edits',
        header: 'Edits',
        group: 'Wrote',
        title:
          'Page revisions in this window, split by who wrote them. Sync snapshots are counted separately — a synced vault can post thousands of revisions nobody typed.',
        numeric: true,
        scale: (u) => metricsOf(u).edits,
        sortValue: (u) => metricsOf(u).edits,
        cell: (u) => {
          const m = metricsOf(u)
          return (
            <DrillCount n={m.edits} onClick={() => openEvents(u, 'page.edit')}>
              <EditSplit m={m} />
            </DrillCount>
          )
        },
      },
    ]

    if (hasAny((m) => m.pages_created)) {
      cols.push({
        key: 'pages',
        header: 'Pages',
        group: 'Wrote',
        title: 'Pages this user created in the window (they authored the page’s first revision).',
        numeric: true,
        scale: (u) => metricsOf(u).pages_created,
        sortValue: (u) => metricsOf(u).pages_created,
        cell: (u) => <Count n={metricsOf(u).pages_created} />,
      })
    }
    if (hasAny((m) => m.views)) {
      cols.push({
        key: 'views',
        header: 'Views',
        group: 'Read',
        title: 'Page views recorded for this user. Limited by the activity-log retention window.',
        numeric: true,
        scale: (u) => metricsOf(u).views,
        sortValue: (u) => metricsOf(u).views,
        cell: (u) => (
          <DrillCount n={metricsOf(u).views} onClick={() => openEvents(u, 'page.view')} />
        ),
      })
    }
    if (hasAny((m) => m.asks)) {
      cols.push({
        key: 'asks',
        header: 'Asks',
        group: 'Read',
        title: 'Questions they put to Ask.',
        numeric: true,
        scale: (u) => metricsOf(u).asks,
        sortValue: (u) => metricsOf(u).asks,
        cell: (u) => <DrillCount n={metricsOf(u).asks} onClick={() => openEvents(u, 'ask')} />,
      })
    }
    if (hasAny((m) => m.llm_calls)) {
      cols.push({
        key: 'ai',
        header: 'AI',
        group: 'Read',
        title:
          'Metered AI calls. Counted per calendar month, so this follows month boundaries rather than the exact window.',
        numeric: true,
        scale: (u) => metricsOf(u).llm_calls,
        sortValue: (u) => metricsOf(u).llm_calls,
        cell: (u) => <Count n={metricsOf(u).llm_calls} />,
      })
    }

    const anyStorage = all.some(
      (u) => (u.usage?.spaces ?? 0) > 0 || (u.usage?.storage_bytes ?? 0) > 0,
    )

    cols.push(
      {
        key: 'days',
        header: 'Days',
        group: 'Cadence',
        title:
          'Distinct days with any activity — an event or an authored revision. One busy afternoon and a month of daily use look identical under a raw event count.',
        numeric: true,
        scale: (u) => metricsOf(u).days_active,
        sortValue: (u) => metricsOf(u).days_active,
        cell: (u) => <Count n={metricsOf(u).days_active} />,
      },
      {
        key: 'last_active',
        header: 'Seen',
        group: 'Cadence',
        title: 'Time since the account’s last authenticated request. Hover a value for the exact date.',
        // Never-signed-in sorts below every real timestamp in both directions.
        sortValue: (u) => u.last_active_at ?? '',
        cell: (u) =>
          u.last_active_at ? (
            <span
              className="whitespace-nowrap tabular-nums"
              title={`${relativeTimeFromSqlite(u.last_active_at)} · ${localDateFromSqlite(u.last_active_at)}`}
            >
              {shortRelativeFromSqlite(u.last_active_at)}
            </span>
          ) : (
            <span className="text-[var(--text-muted)]">Never</span>
          ),
      },
    )
    if (anyStorage) {
      cols.push({
        key: 'storage',
        header: 'Storage',
        group: 'Account',
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
      })
    }

    cols.push({
      key: 'actions',
      header: <span className="sr-only">Actions</span>,
      group: 'Account',
      sticky: 'right',
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
                  <DropdownMenuItem onSelect={() => openEvents(u)}>
                    Open in Events
                  </DropdownMenuItem>
                  <DropdownMenuSeparator />
                  {/* Plan lives here rather than as a column: an inline select
                      fought row-click for the same pixels and cost the width
                      that pushed these actions off the screen. */}
                  <DropdownMenuItem onSelect={() => setPlanFor(u)}>
                    Change plan…
                  </DropdownMenuItem>
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
    })
    return cols
  }, [me.data?.id, runUpdate, updateUser.isPending, openEvents, hasAny, all])

  const isEmptyInstance = !users.isLoading && all.length <= 1

  return (
    <section
      aria-labelledby="settings-users"
      className="flex flex-col gap-[var(--space-4)]"
    >
      <header className="flex items-start justify-between gap-[var(--space-3)]">
        <p className="m-0 max-w-[42rem] text-[length:var(--text-sm)] text-[var(--text-muted)] leading-[var(--leading-relaxed)]">
          Everyone on this instance, with what they've actually done. Sort any
          column, or filter by lifecycle to get straight to the people worth a
          message — the ones ramping up, the ones going quiet, the ones who
          never got started.
        </p>
        <div className="flex items-center gap-[var(--space-2)] shrink-0">
          {/* Export follows the people rows, so it hides rather than quietly
              downloading a different table than the one on screen. */}
          {groupBy === 'people' ? (
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
          ) : null}
          <Button type="button" variant="primary" onClick={() => setCreateOpen(true)}>
            <UserPlus width={14} height={14} />
            <span>Create user</span>
          </Button>
        </div>
      </header>

      {/* Two controls, two jobs, two weights: the group-by changes the SUBJECT
          of the whole table so it reads as tabs; the window only reframes the
          numbers, so it sits quietly on the right. Identical segmented controls
          side by side made them look interchangeable. */}
      <div className="flex flex-wrap items-end justify-between gap-[var(--space-3)] border-b border-[var(--border-subtle)]">
        <div className="flex items-center gap-[var(--space-1)]">
          {(
            [
              ['people', 'People'],
              ['org', 'Teams'],
              ['space', 'Spaces'],
            ] as [GroupBy, string][]
          ).map(([val, label]) => (
            <button
              key={val}
              type="button"
              onClick={() => setView({ by: val === 'people' ? undefined : val })}
              aria-current={groupBy === val ? 'page' : undefined}
              className={cn(
                'relative px-[var(--space-3)] py-[var(--space-2)]',
                'text-[length:var(--text-sm)] font-medium bg-transparent border-0 cursor-pointer',
                'rounded-t-[var(--radius-sm)] outline-none',
                'focus-visible:ring-2 focus-visible:ring-[var(--accent)]',
                groupBy === val
                  ? 'text-[var(--text-primary)] after:absolute after:inset-x-[var(--space-2)] after:-bottom-px after:h-[2px] after:bg-[var(--accent)] after:rounded-[var(--radius-full)]'
                  : 'text-[var(--text-muted)] hover:text-[var(--text-primary)]',
              )}
            >
              {label}
            </button>
          ))}
        </div>
        <div className="pb-[var(--space-2)]">
          <FilterPills<AdminUserWindow>
            value={range}
            onChange={(next) => setView({ window: next === '1m' ? undefined : next })}
            options={[
              ['1m', '30 days'],
              ['3m', '3 months'],
              ['all', 'All time'],
            ]}
          />
        </div>
      </div>

      {groupBy === 'people' ? (
        <div className="flex flex-wrap items-center gap-[var(--space-2)]">
          <div className="relative flex-1 min-w-[12rem]">
            <Search
              width={14}
              height={14}
              aria-hidden
              className="pointer-events-none absolute left-[var(--space-2)] top-1/2 -translate-y-1/2 text-[var(--text-muted)]"
            />
            <Input
              value={q}
              onChange={(e) => setView({ q: e.target.value || undefined })}
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
      ) : null}

      {rowError ? (
        <p role="alert" className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]">
          {rowError}
        </p>
      ) : null}

      {users.isLoading ? (
        <TableSkeleton />
      ) : users.isError ? (
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]">
          Couldn't load users.
        </p>
      ) : groupBy !== 'people' ? (
        <ActivityGroupsTable by={groupBy} window={range} />
      ) : isEmptyInstance ? (
        <EmptyState
          icon={UserPlus}
          title="Nothing to measure yet"
          description="Invite someone, or wait for activity — reads, edits and questions all show up here as they happen."
          actions={
            <Button type="button" variant="primary" onClick={() => setCreateOpen(true)}>
              <UserPlus width={14} height={14} />
              <span>Create user</span>
            </Button>
          }
        />
      ) : (
        <>
          <SegmentBand
            rows={all}
            active={segment}
            onPick={(next) => setView({ seg: next === 'all' ? undefined : next })}
          />
          <SummaryStrip rows={filtered} />
          <DataTable
            rows={filtered}
            columns={columns}
            rowKey={(u) => u.id}
            sort={{ key: sortKey, dir: sortDir }}
            onSortChange={(next) => setView({ sort: next.key, dir: next.dir })}
            onRowClick={(u) => setActivityFor(u)}
            rowActionLabel={(u) => `Open details for ${u.display_name || u.username}`}
            stale={users.isFetching}
            caption="Users on this instance with their activity"
            empty={hasFilters ? 'No users match these filters.' : 'No users found.'}
          />
          <p className="m-0 text-[length:var(--text-xs)] text-[var(--text-muted)]">
            <span className="tabular-nums">
              {filtered.length} of {all.length} {all.length === 1 ? 'user' : 'users'}
            </span>
            {retentionClips ? (
              <>
                {' · '}
                Views, sign-ins and days-active only go back to{' '}
                <span className="tabular-nums">{localDateFromSqlite(eventsSince)}</span>{' '}
                (activity-log retention)
              </>
            ) : null}
          </p>
          <CohortGrid rows={all} weeks={weekAxis} />
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
      {planFor ? (
        <ChangePlanDialog
          user={planFor}
          open
          onOpenChange={(next) => !next && setPlanFor(null)}
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

// One line per person: name, then role → capability → state, in descending
// weight. Everything past the first two chips collapses so rows keep a rhythm —
// a table whose row heights wobble reads as unfinished.
function PersonCell({ row, isSelf }: { row: AdminUserRow; isSelf: boolean }) {
  const chips: React.ReactNode[] = []
  if (row.is_instance_admin) chips.push(<Badge key="admin" variant="solid">Admin</Badge>)
  if (row.used_mcp) chips.push(<Badge key="mcp" variant="muted">MCP</Badge>)
  else if (row.has_api_key) chips.push(<Badge key="key" variant="muted">API key</Badge>)
  if (!row.is_active) chips.push(<Badge key="off" variant="ghost">Deactivated</Badge>)
  if (isSelf) chips.push(<Badge key="you" variant="ghost">You</Badge>)

  const shown = chips.slice(0, 2)
  const rest = chips.length - shown.length

  return (
    <div className="flex flex-col gap-[1px] min-w-[9rem] max-w-[16rem]">
      <span className="flex items-center gap-[var(--space-2)] whitespace-nowrap">
        <span className="truncate font-medium">{row.display_name || row.username}</span>
        {shown}
        {rest > 0 ? (
          <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">+{rest}</span>
        ) : null}
      </span>
      <span className="truncate text-[length:var(--text-xs)] text-[var(--text-muted)]">
        {row.display_name ? `@${row.username}` : row.email || `Joined ${localDateFromSqlite(row.created_at)}`}
      </span>
    </div>
  )
}

// The lifecycle census as one proportional band. Six numbers in six pills is
// something you read; a band is something you SEE — a fat "never started" slab
// lands in a way that "Never started 1" never does. Each slice is the filter.
function SegmentBand({
  rows,
  active,
  onPick,
}: {
  rows: AdminUserRow[]
  active: SegmentFilter
  onPick: (next: SegmentFilter) => void
}) {
  const counts = useMemo(() => {
    const c = new Map<string, number>()
    for (const r of rows) if (r.segment) c.set(r.segment, (c.get(r.segment) ?? 0) + 1)
    return c
  }, [rows])
  const total = rows.length || 1

  return (
    <div className="flex flex-col gap-[var(--space-2)]">
      <div className="flex items-center gap-[var(--space-2)]">
        <span className="text-[length:var(--text-xs)] font-medium text-[var(--text-muted)]">
          Lifecycle
        </span>
        <SegmentRules />
        {active !== 'all' ? (
          <button
            type="button"
            onClick={() => onPick('all')}
            className="ml-auto bg-transparent border-0 p-0 cursor-pointer text-[length:var(--text-xs)] text-[var(--accent)] hover:underline rounded-[var(--radius-xs)] outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]"
          >
            Clear filter
          </button>
        ) : null}
      </div>

      <div
        className="flex h-[var(--space-2)] w-full overflow-hidden rounded-[var(--radius-full)] bg-[var(--surface-2)]"
        role="group"
        aria-label="Filter by lifecycle"
      >
        {SEGMENTS.map((s) => {
          const n = counts.get(s.key) ?? 0
          if (n === 0) return null
          const selected = active === s.key
          return (
            <button
              key={s.key}
              type="button"
              onClick={() => onPick(selected ? 'all' : s.key)}
              aria-pressed={selected}
              aria-label={`${s.label}: ${n} — ${s.hint}`}
              title={`${s.label} · ${n} · ${s.hint}`}
              style={{ width: `${(n / total) * 100}%`, background: s.band }}
              className={cn(
                'h-full border-0 cursor-pointer p-0 transition-[width,opacity] duration-300',
                'outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--text-primary)]',
                // Selecting one slice dims the others rather than hiding them,
                // so the proportions stay legible while filtered.
                active !== 'all' && !selected ? 'opacity-30' : 'opacity-100 hover:opacity-80',
              )}
            />
          )
        })}
      </div>

      <div className="flex flex-wrap items-center gap-x-[var(--space-4)] gap-y-[var(--space-1)]">
        {SEGMENTS.map((s) => {
          const n = counts.get(s.key) ?? 0
          const selected = active === s.key
          return (
            <button
              key={s.key}
              type="button"
              disabled={n === 0}
              onClick={() => onPick(selected ? 'all' : s.key)}
              aria-pressed={selected}
              className={cn(
                'inline-flex items-center gap-[var(--space-2)] bg-transparent border-0 p-0',
                'text-[length:var(--text-xs)] rounded-[var(--radius-xs)] outline-none',
                'focus-visible:ring-2 focus-visible:ring-[var(--accent)]',
                n === 0 ? 'opacity-40' : 'cursor-pointer hover:text-[var(--text-primary)]',
                selected ? 'text-[var(--text-primary)] font-medium' : 'text-[var(--text-muted)]',
              )}
            >
              <span
                aria-hidden
                className="h-[var(--space-2)] w-[var(--space-2)] rounded-[var(--radius-full)]"
                style={{ background: s.dot }}
              />
              {s.label}
              <span className="tabular-nums">{n}</span>
            </button>
          )
        })}
      </div>
    </div>
  )
}

// The thresholds drive decisions, so they belong on screen rather than in a
// docs page nobody opens mid-triage.
function SegmentRules() {
  return (
    <Popover>
      <PopoverTrigger asChild>
        <button
          type="button"
          aria-label="How lifecycle is calculated"
          className="inline-flex items-center text-[var(--text-muted)] hover:text-[var(--text-primary)] bg-transparent border-0 p-0 cursor-pointer rounded-[var(--radius-xs)] outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]"
        >
          <HelpCircle width={13} height={13} />
        </button>
      </PopoverTrigger>
      <PopoverContent className="w-[22rem] flex flex-col gap-[var(--space-2)]">
        <p className="m-0 text-[length:var(--text-xs)] text-[var(--text-muted)]">
          Counted over the <strong className="text-[var(--text-primary)]">last 30 days</strong>,
          whatever window is selected — it describes the person, not the view. An
          active day is one with any recorded activity, including writes that
          arrive over file sync.
        </p>
        <dl className="m-0 grid grid-cols-[auto_1fr] gap-x-[var(--space-3)] gap-y-[var(--space-1)] text-[length:var(--text-xs)]">
          {SEGMENTS.map((s) => (
            <div key={s.key} className="contents">
              <dt className="m-0 inline-flex items-center gap-[var(--space-2)] text-[var(--text-primary)]">
                <span
                  aria-hidden
                  className="h-[var(--space-2)] w-[var(--space-2)] rounded-[var(--radius-full)]"
                  style={{ background: s.dot }}
                />
                {s.label}
              </dt>
              <dd className="m-0 text-[var(--text-muted)]">{s.hint}</dd>
            </div>
          ))}
        </dl>
        <p className="m-0 text-[length:var(--text-xs)] text-[var(--text-muted)]">
          Recency outranks volume: someone who wrote hundreds of pages and hasn't
          appeared in six weeks is <em>churned</em>, not <em>power</em>.
        </p>
      </PopoverContent>
    </Popover>
  )
}

function SegmentBadge({ segment }: { segment?: AdminUserSegment }) {
  const s = segment ? SEGMENT_BY_KEY.get(segment) : undefined
  if (!s) return <span className="text-[var(--text-muted)]">—</span>
  return (
    <Badge variant={s.tone} title={`${s.label} — ${s.hint}`}>
      {s.short}
    </Badge>
  )
}

// Sparkline of weekly active days plus the change across the last DELTA_WEEKS.
// A total says how much someone did; only the shape says whether they still are.
function TrendCell({ row }: { row: AdminUserRow }) {
  const weeks = metricsOf(row).weeks
  const series = weeks.slice(-TREND_WEEKS)
  const delta = trendDelta(weeks)
  const flat = series.every((v) => v === 0)
  if (flat) {
    return (
      <span className="flex h-[var(--space-6)] items-center text-[var(--text-muted)]">—</span>
    )
  }

  // Somebody with no earlier weeks to compare against isn't "0%" — they're new,
  // which is the most interesting thing a row can say.
  const isNew = delta == null && isNewcomer(weeks)
  const tone =
    delta == null
      ? 'text-[var(--text-muted)]'
      : delta > 0
        ? 'text-[var(--accent-positive-fg)]'
        : delta < 0
          ? 'text-[var(--accent-warning-fg)]'
          : 'text-[var(--text-muted)]'

  return (
    <div className="flex h-[var(--space-6)] flex-col items-start justify-center gap-[1px] w-[3.75rem]">
      <Sparkline
        values={series}
        width={60}
        height={16}
        domain={WEEK_DOMAIN}
        showLast
        baseline
        className={cn(
          'w-full',
          delta != null && delta < 0
            ? 'text-[var(--text-muted)]'
            : 'text-[var(--accent)]',
        )}
        ariaLabel={`Active days per week over the last ${series.length} weeks`}
      />
      <span className={cn('text-[length:var(--text-xs)] tabular-nums whitespace-nowrap leading-none', tone)}>
        {isNew ? 'new' : delta == null ? '' : `${delta > 0 ? '+' : ''}${delta}%`}
      </span>
    </div>
  )
}

// Signup-cohort retention: of the people who joined in week N, how many were
// still active k weeks later. The one view that answers "does this stick"
// rather than "who is here" — and it reads off the same weekly series the
// sparklines use, so it costs no extra query.
function CohortGrid({ rows, weeks }: { rows: AdminUserRow[]; weeks: string[] }) {
  const [open, setOpen] = useState(false)
  const cohorts = useMemo(() => buildCohorts(rows, weeks), [rows, weeks])
  if (weeks.length === 0 || cohorts.length === 0) return null
  const span = Math.min(8, Math.max(...cohorts.map((c) => c.retention.length)))
  const covered = cohorts.reduce((n, c) => n + c.size, 0)
  const older = rows.length - covered

  return (
    <section className="flex flex-col gap-[var(--space-2)]">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="self-start bg-transparent border-0 p-0 cursor-pointer text-[length:var(--text-sm)] font-medium text-[var(--text-primary)] outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)] rounded-[var(--radius-xs)]"
        aria-expanded={open}
      >
        {open ? '▾' : '▸'} Signup cohorts — do they stay?
      </button>
      {open ? (
        <>
          <p className="m-0 text-[length:var(--text-xs)] text-[var(--text-muted)]">
            Each row is the people who signed up that week; each column is how
            many were still active that many weeks later. Week 0 is the signup
            week itself — a row that fades to nothing is the churn you're looking
            for.
            {older > 0 ? (
              <>
                {' '}
                {older} {older === 1 ? 'account' : 'accounts'} signed up before this
                window and can't be shown: the activity log only reaches back{' '}
                {weeks.length} weeks.
              </>
            ) : null}
          </p>
          <div className="overflow-x-auto rounded-[var(--radius-md)] border border-[var(--border-subtle)]">
            <table className="border-collapse text-[length:var(--text-xs)]">
              <thead>
                <tr>
                  <th className="sticky left-0 bg-[var(--surface-2)] px-[var(--space-3)] py-[var(--space-2)] text-left font-semibold text-[var(--text-muted)] whitespace-nowrap">
                    Signed up
                  </th>
                  <th className="bg-[var(--surface-2)] px-[var(--space-2)] py-[var(--space-2)] text-right font-semibold text-[var(--text-muted)]">
                    People
                  </th>
                  {Array.from({ length: span }, (_, i) => (
                    <th
                      key={i}
                      className="bg-[var(--surface-2)] w-[2.75rem] px-0 py-[var(--space-2)] text-center font-semibold text-[var(--text-muted)]"
                    >
                      W{i}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {cohorts.map((c) => (
                  <tr key={c.week} className="border-t border-[var(--border-subtle)]">
                    <td className="sticky left-0 bg-[var(--surface-1)] px-[var(--space-3)] py-[2px] whitespace-nowrap tabular-nums">
                      {c.week}
                    </td>
                    <td className="px-[var(--space-2)] py-[2px] text-right tabular-nums text-[var(--text-muted)]">
                      {c.size}
                    </td>
                    {Array.from({ length: span }, (_, i) => (
                      <CohortCell key={i} pct={c.retention[i] ?? null} />
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      ) : null}
    </section>
  )
}

// One heatmap cell. Value rides the fill; the number only prints once the fill
// is dark enough to carry it, so a near-empty grid reads as a shape rather than
// a wall of "0%". A week that hasn't happened yet is hatched, not blank —
// "no data" and "nobody came back" must not look the same.
function CohortCell({ pct }: { pct: number | null }) {
  if (pct == null) {
    return (
      <td
        aria-label="not yet"
        className="w-[2.75rem] h-[2rem] p-0 align-middle"
        style={{
          backgroundImage:
            'repeating-linear-gradient(45deg, var(--surface-2) 0 3px, transparent 3px 6px)',
        }}
      />
    )
  }
  return (
    <td className="w-[2.75rem] h-[2rem] p-0 align-middle text-center">
      <span
        className="flex h-[2rem] w-full items-center justify-center tabular-nums transition-colors duration-200"
        style={{
          background: `color-mix(in srgb, var(--accent) ${Math.round(pct * 0.65)}%, transparent)`,
          color: pct >= 55 ? 'var(--accent-fg)' : 'var(--text-primary)',
        }}
      >
        {pct >= 15 ? (
          `${pct}%`
        ) : pct === 0 ? (
          // A measured zero and a week that hasn't happened yet must not both
          // render as blank space — the hatch says "not yet", this says "none".
          <span className="opacity-40">0</span>
        ) : (
          ''
        )}
      </span>
    </td>
  )
}

// Totals across the rows currently in view. One figure leads — five equal cards
// gave the eye nothing to land on — and the rest support it.
function SummaryStrip({ rows }: { rows: AdminUserRow[] }) {
  const t = useMemo(() => {
    return rows.reduce(
      (acc, u) => {
        const m = metricsOf(u)
        acc.edits += m.edits
        acc.human += m.human_edits
        acc.agent += m.agent_edits
        acc.sync += m.sync_edits
        acc.pages += m.pages_created
        acc.asks += m.asks
        acc.ai += m.llm_calls
        if (m.days_active > 0) acc.active += 1
        return acc
      },
      { edits: 0, human: 0, agent: 0, sync: 0, pages: 0, asks: 0, ai: 0, active: 0 },
    )
  }, [rows])
  const share = (n: number) => (t.edits > 0 ? Math.round((n / t.edits) * 100) : 0)
  const pct = rows.length > 0 ? Math.round((t.active / rows.length) * 100) : 0

  return (
    <div className="flex flex-wrap items-stretch gap-[var(--space-3)]">
      <div className="flex min-w-[13rem] flex-col justify-center gap-[2px] rounded-[var(--radius-md)] border border-[var(--border-subtle)] bg-[var(--surface-1)] px-[var(--space-4)] py-[var(--space-3)]">
        <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">
          Active people
        </span>
        <span className="text-[length:var(--text-2xl)] font-semibold leading-[var(--leading-tight)] tabular-nums text-[var(--text-primary)]">
          {t.active}
          <span className="text-[length:var(--text-base)] font-normal text-[var(--text-muted)]">
            {' '}
            / {rows.length}
          </span>
        </span>
        <span
          aria-hidden
          className="mt-[var(--space-1)] block h-[3px] w-full overflow-hidden rounded-[var(--radius-full)] bg-[var(--surface-3)]"
        >
          <span
            className="block h-full rounded-[var(--radius-full)] bg-[var(--accent)] transition-[width] duration-300"
            style={{ width: `${pct}%` }}
          />
        </span>
      </div>
      <dl className="m-0 grid flex-1 grid-cols-2 sm:grid-cols-4 gap-[var(--space-2)]">
        <SummaryStat
          label="Edits"
          value={t.edits.toLocaleString()}
          sub={
            t.edits > 0
              ? `${share(t.human)}% human · ${share(t.agent)}% agent · ${share(t.sync)}% sync`
              : undefined
          }
        />
        <SummaryStat label="Pages created" value={t.pages.toLocaleString()} />
        <SummaryStat label="Asks" value={t.asks.toLocaleString()} />
        <SummaryStat label="AI calls" value={t.ai.toLocaleString()} />
      </dl>
    </div>
  )
}

function SummaryStat({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div className="flex flex-col justify-center gap-[1px] rounded-[var(--radius-sm)] border border-[var(--border-subtle)] bg-[var(--surface-1)] px-[var(--space-3)] py-[var(--space-2)]">
      <dt className="m-0 text-[length:var(--text-xs)] text-[var(--text-muted)]">{label}</dt>
      <dd className="m-0 text-[length:var(--text-lg)] font-medium tabular-nums leading-[var(--leading-tight)] text-[var(--text-primary)]">
        {value}
      </dd>
      {sub ? (
        <dd className="m-0 text-[length:var(--text-xs)] text-[var(--text-muted)]">{sub}</dd>
      ) : null}
    </div>
  )
}

// Rows of the right height, so switching windows doesn't jump the layout.
function TableSkeleton() {
  return (
    <div
      aria-busy="true"
      aria-label="Loading users"
      className="flex flex-col gap-[1px] overflow-hidden rounded-[var(--radius-md)] border border-[var(--border-subtle)]"
    >
      {Array.from({ length: 6 }, (_, i) => (
        <div
          key={i}
          className="h-[var(--space-8)] bg-[var(--surface-2)] opacity-60 animate-pulse"
          style={{ animationDelay: `${i * 60}ms` }}
        />
      ))}
    </div>
  )
}

// The provenance line under an edit count. Only shows what's actually there —
// most accounts are pure human, and "0 agent · 0 sync" on every row would bury
// the two that aren't.
function EditSplit({ m }: { m: AdminUserMetrics }) {
  const parts: string[] = []
  if (m.agent_edits > 0) parts.push(`${m.agent_edits.toLocaleString()} agent`)
  if (m.sync_edits > 0) parts.push(`${m.sync_edits.toLocaleString()} sync`)
  if (parts.length === 0) return null
  return (
    <span className="block text-[length:var(--text-xs)] text-[var(--text-muted)] whitespace-nowrap">
      {parts.join(' · ')}
    </span>
  )
}

// A count cell: zero is real data, but a wall of 0s buries the rows that matter,
// so it dims to a dash (and the column's scale track shows empty).
function Count({ n }: { n: number }) {
  if (n === 0) return <span className="text-[var(--text-muted)]">—</span>
  return <span>{n.toLocaleString()}</span>
}

// A count that opens the rows behind it. A number you can't click is a dead end
// — you read it, wonder what's in it, and there's nowhere to go.
function DrillCount({
  n,
  onClick,
  children,
}: {
  n: number
  onClick: () => void
  children?: React.ReactNode
}) {
  if (n === 0) return <span className="text-[var(--text-muted)]">—</span>
  return (
    <button
      type="button"
      onClick={(e) => {
        e.stopPropagation()
        onClick()
      }}
      className="block w-full bg-transparent border-0 p-0 cursor-pointer text-inherit text-right tabular-nums rounded-[var(--radius-xs)] outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)] hover:underline"
      title="Open these in Events"
    >
      {n.toLocaleString()}
      {children}
    </button>
  )
}

// Download the rows currently in view. Client-side on purpose: the table
// already holds every number, so an export endpoint would be a second place
// for the same columns to drift.
function exportUsersCsv(rows: AdminUserRow[], range: AdminUserWindow) {
  const header = [
    'username', 'display_name', 'email', 'plan', 'segment', 'instance_admin', 'active',
    'created_at', 'last_active_at', 'orgs', 'spaces', 'storage_bytes',
    'edits', 'human_edits', 'agent_edits', 'sync_edits', 'pages_created',
    'views', 'asks', 'logins', 'days_active', 'days30', 'trend_pct', 'llm_calls', 'mcp',
  ]
  const cell = (v: unknown) => {
    const s = v == null ? '' : String(v)
    return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
  }
  const lines = [header.join(',')]
  for (const u of rows) {
    const m = metricsOf(u)
    lines.push([
      u.username, u.display_name, u.email ?? '', u.plan_key, u.segment ?? '',
      u.is_instance_admin, u.is_active, u.created_at, u.last_active_at ?? '',
      u.orgs ?? 0, u.usage?.spaces ?? '', u.usage?.storage_bytes ?? '',
      m.edits, m.human_edits, m.agent_edits, m.sync_edits, m.pages_created,
      m.views, m.asks, m.logins, m.days_active, m.days30, trendDelta(m.weeks) ?? '',
      m.llm_calls, u.used_mcp ? 'yes' : u.has_api_key ? 'key-only' : 'no',
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

// Plan changes moved out of the row and into a dialog — see the row menu.
function ChangePlanDialog({
  user,
  open,
  onOpenChange,
}: {
  user: AdminUserRow
  open: boolean
  onOpenChange: (next: boolean) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Plan for {user.display_name || user.username}</DialogTitle>
          <DialogDescription>
            Sets the tier this account is metered against. Takes effect immediately;
            there's no charge either way.
          </DialogDescription>
        </DialogHeader>
        <PlanTierSelect
          accountKind="user"
          accountId={user.id}
          currentKey={user.plan_key}
          className="w-full"
        />
        <DialogFooter>
          <DialogClose asChild>
            <Button type="button" variant="ghost">
              Done
            </Button>
          </DialogClose>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
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
