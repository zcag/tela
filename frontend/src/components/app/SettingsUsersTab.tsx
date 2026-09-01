import { useCallback, useMemo, useState } from 'react'
import { useNavigate, useSearch } from '@tanstack/react-router'
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
  AdminUserSegment,
  AdminUserUsage,
  AdminUserWindow,
} from '../../lib/types'
import { PlanTierSelect } from './PlanTierSelect'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Checkbox } from '../ui/checkbox'
import { DataTable, type DataTableColumn, type SortDir } from '../ui/data-table'
import { ActivityGroupsTable } from './ActivityGroupsTable'
import type { ActivityGroupBy } from '../../lib/queries/admin-activity-groups'
import { Sparkline } from '../ui/sparkline'
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
type SegmentFilter = 'all' | AdminUserSegment
// What the table is a list OF. People is the default; the other two answer the
// question people actually ask on a multi-team instance ("which team uses
// this?", "which spaces are alive?") without making anyone sum a column by eye.
type GroupBy = 'people' | ActivityGroupBy

// How many trailing weeks the trend sparkline draws. The payload carries 26 (the
// events-retention ceiling) because the cohort grid needs the depth; a row-height
// sparkline can only resolve a quarter of that, and a longer one just looks noisy.
const TREND_WEEKS = 12
// Weeks per side of the trend delta: last 4 vs the 4 before them. A month is
// short enough to catch someone falling off while you can still do something.
const DELTA_WEEKS = 4

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

// The lifecycle vocabulary, in the order a human reads it: best to worst, with
// the two that are actually actionable ('never', 'churned') at the ends.
const SEGMENTS: {
  key: AdminUserSegment
  label: string
  tone: 'accent' | 'muted' | 'danger'
  hint: string
}[] = [
  { key: 'power', label: 'Power', tone: 'accent', hint: '12+ active days in the last 30' },
  { key: 'regular', label: 'Regular', tone: 'accent', hint: '4–11 active days in the last 30' },
  { key: 'dabbler', label: 'Dabbler', tone: 'muted', hint: '1–3 active days in the last 30' },
  { key: 'churned', label: 'Churned', tone: 'danger', hint: 'was active once, silent for 30+ days' },
  { key: 'never', label: 'Never started', tone: 'danger', hint: 'signed up and never did anything' },
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

  const columns = useMemo<DataTableColumn<AdminUserRow>[]>(
    () => [
      {
        key: 'person',
        header: 'Person',
        sticky: 'left',
        sortValue: (u) => (u.display_name || u.username).toLowerCase(),
        cell: (u) => (
          <div className="flex flex-col gap-[2px] min-w-[9rem]">
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
        key: 'segment',
        header: 'Status',
        title: 'Lifecycle, from activity over the last 30 days — independent of the selected window, so it describes the person and not the view.',
        sortValue: (u) => (u.segment ? SEGMENT_RANK[u.segment] : -1),
        cell: (u) => <SegmentBadge segment={u.segment} />,
      },
      {
        key: 'trend',
        header: 'Trend',
        title: `Active days per week over the last ${TREND_WEEKS} weeks, and the change from the previous ${DELTA_WEEKS} weeks to the last ${DELTA_WEEKS}.`,
        // Sort by the delta: "who is ramping up" and "who is falling off" are
        // the two ends of the same column.
        sortValue: (u) => trendDelta(metricsOf(u).weeks) ?? -999,
        cell: (u) => <TrendCell weeks={metricsOf(u).weeks} />,
      },
      {
        key: 'edits',
        header: 'Edits',
        title: 'Page revisions in this window, split by who wrote them. Sync snapshots are counted separately — a synced vault can post thousands of revisions nobody typed.',
        numeric: true,
        sortValue: (u) => metricsOf(u).edits,
        cell: (u) => {
          const m = metricsOf(u)
          return (
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation()
                openEvents(u, 'page.edit')
              }}
              className="flex flex-col items-end bg-transparent border-0 p-0 cursor-pointer text-inherit rounded-[var(--radius-xs)] outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)] hover:underline"
              title="Open these edits in Events"
            >
              <Count n={m.edits} />
              <EditSplit m={m} />
            </button>
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
        cell: (u) => (
          <DrillCount n={metricsOf(u).views} onClick={() => openEvents(u, 'page.view')} />
        ),
      },
      {
        key: 'asks',
        header: 'Asks',
        title: 'Questions they put to Ask.',
        numeric: true,
        sortValue: (u) => metricsOf(u).asks,
        cell: (u) => <DrillCount n={metricsOf(u).asks} onClick={() => openEvents(u, 'ask')} />,
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
        title: 'Distinct days with any activity — an event or an authored revision. One busy afternoon and a month of daily use look identical under a raw event count.',
        numeric: true,
        sortValue: (u) => metricsOf(u).days_active,
        cell: (u) => <Count n={metricsOf(u).days_active} />,
      },
      {
        key: 'last_active',
        header: 'Last seen',
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
        cell: (u) => (
          <div onClick={(e) => e.stopPropagation()}>
            <PlanTierSelect
              accountKind="user"
              accountId={u.id}
              currentKey={u.plan_key}
              className="w-[6.5rem]"
            />
          </div>
        ),
      },
      {
        key: 'actions',
        header: <span className="sr-only">Actions</span>,
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
    [me.data?.id, runUpdate, updateUser.isPending, openEvents],
  )

  return (
    <section
      aria-labelledby="settings-users"
      className="flex flex-col gap-[var(--space-4)]"
    >
      <header className="flex items-start justify-between gap-[var(--space-3)]">
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)] leading-[var(--leading-relaxed)]">
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

      {/* Filter bar. The window pills sit first — they change what every
          activity column MEANS, so they read as the frame for the rest. */}
      <div className="flex flex-wrap items-center gap-[var(--space-2)]">
        <FilterPills<GroupBy>
          value={groupBy}
          onChange={(next) => setView({ by: next === 'people' ? undefined : next })}
          options={[
            ['people', 'People'],
            ['org', 'Teams'],
            ['space', 'Spaces'],
          ]}
        />
        <FilterPills<AdminUserWindow>
          value={range}
          onChange={(next) => setView({ window: next === '1m' ? undefined : next })}
          options={[
            ['1m', 'Last 30 days'],
            ['3m', 'Last 3 months'],
            ['all', 'All time'],
          ]}
        />
        {groupBy !== 'people' ? null : (
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
        )}
        {groupBy !== 'people' ? null : (
        <>
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
        </>
        )}
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
      ) : groupBy !== 'people' ? (
        <ActivityGroupsTable by={groupBy} window={range} />
      ) : (
        <>
          <SegmentBar
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

// The lifecycle census, doubling as the filter. Reads as a sentence about the
// instance ("6 power, 12 regular, 40 never started") and every number is the
// list behind it — which is the whole point: "never started" is a to-do, not a
// statistic.
function SegmentBar({
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

  return (
    <div className="flex flex-wrap items-center gap-[var(--space-2)]">
      <button
        type="button"
        onClick={() => onPick('all')}
        aria-pressed={active === 'all'}
        className={cn(segmentChipClass, active === 'all' ? segmentChipActive : '')}
      >
        Everyone <span className="tabular-nums">{rows.length}</span>
      </button>
      {SEGMENTS.map((s) => {
        const n = counts.get(s.key) ?? 0
        return (
          <button
            key={s.key}
            type="button"
            onClick={() => onPick(active === s.key ? 'all' : s.key)}
            aria-pressed={active === s.key}
            title={s.hint}
            disabled={n === 0}
            className={cn(
              segmentChipClass,
              active === s.key ? segmentChipActive : '',
              n === 0 ? 'opacity-40 cursor-default' : '',
              s.tone === 'danger' && n > 0 ? 'text-[var(--danger)]' : '',
            )}
          >
            {s.label} <span className="tabular-nums">{n}</span>
          </button>
        )
      })}
    </div>
  )
}

const segmentChipClass =
  'inline-flex items-center gap-[var(--space-1)] rounded-[var(--radius-sm)] border border-[var(--border-subtle)] bg-[var(--surface-1)] px-[var(--space-3)] py-[var(--space-1)] text-[length:var(--text-xs)] font-medium text-[var(--text-muted)] outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)] hover:text-[var(--text-primary)]'
const segmentChipActive = 'bg-[var(--surface-3)] text-[var(--text-primary)] border-[var(--accent)]'

function SegmentBadge({ segment }: { segment?: AdminUserSegment }) {
  const s = segment ? SEGMENT_BY_KEY.get(segment) : undefined
  if (!s) return <span className="text-[var(--text-muted)]">—</span>
  return (
    <Badge variant={s.tone} title={s.hint}>
      {s.label}
    </Badge>
  )
}

// Sparkline of weekly active days plus the change across the last DELTA_WEEKS.
// A total says how much someone did; only the shape says whether they're still
// doing it.
function TrendCell({ weeks }: { weeks: number[] }) {
  const series = weeks.slice(-TREND_WEEKS)
  const delta = trendDelta(weeks)
  const flat = series.every((v) => v === 0)
  if (flat) return <span className="text-[var(--text-muted)]">—</span>
  // Stacked, not side by side: laid out in a row this cell was wide enough to
  // push the row actions off the screen, and the delta sat close enough to the
  // next column to read as part of it.
  return (
    <div className="flex flex-col items-start gap-[1px] w-[3.75rem]">
      <Sparkline
        values={series}
        width={60}
        height={16}
        className={cn(
          'w-full',
          delta != null && delta < 0 ? 'text-[var(--text-muted)]' : 'text-[var(--accent)]',
        )}
        ariaLabel={`Active days per week over the last ${series.length} weeks`}
      />
      {delta != null ? (
        <span
          className={cn(
            'text-[length:var(--text-xs)] tabular-nums whitespace-nowrap leading-none',
            delta > 0 ? 'text-[var(--success)]' : delta < 0 ? 'text-[var(--danger)]' : 'text-[var(--text-muted)]',
          )}
        >
          {delta > 0 ? '+' : ''}
          {delta}%
        </span>
      ) : null}
    </div>
  )
}

// Percent change in active days: the last DELTA_WEEKS against the DELTA_WEEKS
// before them. Null when there's no prior activity to compare against — an
// account's first weeks are not "+∞% growth", they're just new.
function trendDelta(weeks: number[]): number | null {
  if (weeks.length < DELTA_WEEKS * 2) return null
  const sum = (xs: number[]) => xs.reduce((a, b) => a + b, 0)
  const recent = sum(weeks.slice(-DELTA_WEEKS))
  const prior = sum(weeks.slice(-DELTA_WEEKS * 2, -DELTA_WEEKS))
  if (prior === 0) return null
  return Math.round(((recent - prior) / prior) * 100)
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
            week itself — anything below it that fades to nothing is the churn
            you're looking for.
          </p>
          <div className="overflow-x-auto rounded-[var(--radius-md)] border border-[var(--border-subtle)]">
            <table className="w-full border-collapse text-[length:var(--text-xs)]">
              <thead>
                <tr>
                  <th className="sticky left-0 bg-[var(--surface-2)] px-[var(--space-3)] py-[var(--space-2)] text-left font-semibold text-[var(--text-muted)] whitespace-nowrap">
                    Signed up
                  </th>
                  <th className="px-[var(--space-2)] py-[var(--space-2)] text-right font-semibold text-[var(--text-muted)]">
                    People
                  </th>
                  {Array.from({ length: span }, (_, i) => (
                    <th
                      key={i}
                      className="px-[var(--space-2)] py-[var(--space-2)] text-right font-semibold text-[var(--text-muted)] whitespace-nowrap"
                    >
                      W{i}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {cohorts.map((c) => (
                  <tr key={c.week} className="border-t border-[var(--border-subtle)]">
                    <td className="sticky left-0 bg-[var(--surface-1)] px-[var(--space-3)] py-[var(--space-2)] whitespace-nowrap tabular-nums">
                      {c.week}
                    </td>
                    <td className="px-[var(--space-2)] py-[var(--space-2)] text-right tabular-nums text-[var(--text-muted)]">
                      {c.size}
                    </td>
                    {Array.from({ length: span }, (_, i) => {
                      const pct = c.retention[i]
                      return (
                        <td key={i} className="px-[var(--space-2)] py-[var(--space-2)] text-right tabular-nums">
                          {pct == null ? (
                            <span className="text-[var(--text-muted)]">·</span>
                          ) : (
                            <span
                              className="inline-block w-full rounded-[var(--radius-xs)] px-[var(--space-1)]"
                              style={{
                                // Opacity carries the value so the grid reads as
                                // a heatmap at a glance; the number stays for
                                // anyone who needs the actual figure.
                                background: `color-mix(in srgb, var(--accent) ${Math.round(pct * 0.6)}%, transparent)`,
                              }}
                            >
                              {pct}%
                            </span>
                          )}
                        </td>
                      )
                    })}
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

interface Cohort {
  week: string
  size: number
  // retention[k] = % of the cohort active k weeks after signup; null once the
  // week hasn't happened yet (so an incomplete cohort reads as absent, not 0%).
  retention: (number | null)[]
}

function buildCohorts(rows: AdminUserRow[], weeks: string[]): Cohort[] {
  if (weeks.length === 0) return []
  const idxOfWeek = new Map(weeks.map((w, i) => [w, i]))
  // Signup week = the Monday on or before created_at, matching the server axis.
  const groups = new Map<string, AdminUserRow[]>()
  for (const r of rows) {
    const wk = mondayOf(r.created_at)
    if (!idxOfWeek.has(wk)) continue // signed up before the series begins
    const g = groups.get(wk)
    if (g) g.push(r)
    else groups.set(wk, [r])
  }
  return [...groups.entries()]
    .sort((a, b) => (a[0] < b[0] ? 1 : -1)) // newest cohort first
    .map(([week, members]) => {
      const start = idxOfWeek.get(week) as number
      const available = weeks.length - start
      const retention: (number | null)[] = []
      for (let k = 0; k < available; k++) {
        const active = members.filter((m) => (metricsOf(m).weeks[start + k] ?? 0) > 0).length
        retention.push(Math.round((active / members.length) * 100))
      }
      return { week, size: members.length, retention }
    })
}

// The Monday on or before a SQLite-native timestamp, as 'YYYY-MM-DD' — the same
// bucket key the backend's week axis uses.
function mondayOf(ts: string): string {
  const d = new Date(`${ts.replace(' ', 'T')}Z`)
  if (Number.isNaN(d.getTime())) return ''
  const back = (d.getUTCDay() + 6) % 7
  d.setUTCDate(d.getUTCDate() - back)
  return d.toISOString().slice(0, 10)
}

// Totals across the rows currently in view — the same numbers the table holds,
// added up, so filtering to a segment answers "how much does this group
// actually do" without a second endpoint.
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
  return (
    <dl className="m-0 grid grid-cols-2 sm:grid-cols-5 gap-[var(--space-2)]">
      <SummaryStat label="Active people" value={`${t.active} of ${rows.length}`} />
      <SummaryStat
        label="Edits"
        value={t.edits.toLocaleString()}
        sub={t.edits > 0 ? `${share(t.human)}% human · ${share(t.agent)}% agent · ${share(t.sync)}% sync` : undefined}
      />
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

// The provenance line under an edit count. Only shows what's actually there —
// most accounts are pure human, and "0 agent · 0 sync" on every row would bury
// the two that aren't.
function EditSplit({ m }: { m: AdminUserMetrics }) {
  const parts: string[] = []
  if (m.agent_edits > 0) parts.push(`${m.agent_edits.toLocaleString()} agent`)
  if (m.sync_edits > 0) parts.push(`${m.sync_edits.toLocaleString()} sync`)
  if (parts.length === 0) return null
  return (
    <span className="text-[length:var(--text-xs)] text-[var(--text-muted)] whitespace-nowrap">
      {parts.join(' · ')}
    </span>
  )
}

// A count cell: zero is real data, but rendering a wall of 0s buries the rows
// that matter, so it dims to a dash.
function Count({ n }: { n: number }) {
  if (n === 0) return <span className="text-[var(--text-muted)]">—</span>
  return <span>{n.toLocaleString()}</span>
}

// A count that opens the rows behind it. A number you can't click is a dead end
// — you read it, wonder what's in it, and there's nowhere to go.
function DrillCount({ n, onClick }: { n: number; onClick: () => void }) {
  if (n === 0) return <span className="text-[var(--text-muted)]">—</span>
  return (
    <button
      type="button"
      onClick={(e) => {
        e.stopPropagation()
        onClick()
      }}
      className="bg-transparent border-0 p-0 cursor-pointer text-inherit tabular-nums rounded-[var(--radius-xs)] outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)] hover:underline"
      title="Open these in Events"
    >
      {n.toLocaleString()}
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
