import {
  useActivityGroups,
  type ActivityGroup,
  type ActivityGroupBy,
} from '../../lib/queries/admin-activity-groups'
import type { AdminUserWindow } from '../../lib/types'
import { DataTable, type DataTableColumn } from '../ui/data-table'
import { relativeTimeFromSqlite } from '../../lib/relativeTime'

// The People table keyed by content (spaces) or by team (orgs). Same window,
// same primitive, same reading — "what here is actually alive".
export function ActivityGroupsTable({
  by,
  window,
}: {
  by: ActivityGroupBy
  window: AdminUserWindow
}) {
  const groups = useActivityGroups(by, window, true)
  const rows = groups.data ?? []

  const columns: DataTableColumn<ActivityGroup>[] = [
    {
      key: 'name',
      header: by === 'org' ? 'Team' : 'Space',
      sticky: 'left',
      group: 'Who',
      sortValue: (g) => g.name.toLowerCase(),
      cell: (g) => (
        <div className="flex flex-col gap-[2px] min-w-[10rem]">
          <span className="font-medium">{g.name || 'Untitled'}</span>
          {g.detail ? (
            <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">
              {by === 'org' ? g.detail : `@${g.detail}`}
            </span>
          ) : null}
        </div>
      ),
    },
    {
      key: 'people',
      header: by === 'org' ? 'Active members' : 'Contributors',
      group: 'Who',
      title:
        by === 'org'
          ? 'Members with any activity in the window, out of the team’s size.'
          : 'Distinct people who wrote in this space during the window.',
      numeric: true,
      sortValue: (g) => g.active_people,
      cell: (g) =>
        by === 'org' ? (
          <span>
            {g.active_people} <span className="text-[var(--text-muted)]">/ {g.people}</span>
          </span>
        ) : (
          <Num n={g.people} />
        ),
    },
    {
      key: 'edits',
      header: 'Edits',
      group: 'Wrote',
      scale: (g: ActivityGroup) => g.edits,
      title: 'Revisions written in the window. Agent and sync shares are called out — a synced vault can post thousands nobody typed.',
      numeric: true,
      sortValue: (g) => g.edits,
      cell: (g) => (
        <div className="flex flex-col items-end">
          <Num n={g.edits} />
          {g.agent_edits > 0 || g.sync_edits > 0 ? (
            <span className="text-[length:var(--text-xs)] text-[var(--text-muted)] whitespace-nowrap">
              {[
                g.agent_edits > 0 ? `${g.agent_edits} agent` : '',
                g.sync_edits > 0 ? `${g.sync_edits} sync` : '',
              ]
                .filter(Boolean)
                .join(' · ')}
            </span>
          ) : null}
        </div>
      ),
    },
    {
      key: 'views',
      header: 'Views',
      group: 'Read',
      numeric: true,
      scale: (g: ActivityGroup) => g.views,
      sortValue: (g) => g.views,
      cell: (g) => <Num n={g.views} />,
    },
    {
      key: 'asks',
      header: 'Asks',
      group: 'Read',
      numeric: true,
      scale: (g: ActivityGroup) => g.asks,
      sortValue: (g) => g.asks,
      cell: (g) => <Num n={g.asks} />,
    },
    ...(by === 'org'
      ? [
          {
            key: 'ai',
            header: 'AI',
            group: 'Read',
            numeric: true,
            sortValue: (g: ActivityGroup) => g.llm_calls,
            cell: (g: ActivityGroup) => <Num n={g.llm_calls} />,
          },
        ]
      : [
          {
            key: 'pages',
            header: 'Pages',
            group: 'Holds',
            title: 'Live pages in the space right now — not window-scoped.',
            numeric: true,
            sortValue: (g: ActivityGroup) => g.pages,
            cell: (g: ActivityGroup) => <Num n={g.pages} />,
          },
          {
            key: 'last',
            header: 'Last edited',
            group: 'Holds',
            sortValue: (g: ActivityGroup) => g.last_active,
            cell: (g: ActivityGroup) =>
              g.last_active ? (
                <span className="whitespace-nowrap">{relativeTimeFromSqlite(g.last_active)}</span>
              ) : (
                <span className="text-[var(--text-muted)]">Never</span>
              ),
          },
        ]),
  ]

  if (groups.isLoading) {
    return (
      <div
        aria-busy="true"
        aria-label="Loading activity"
        className="flex flex-col gap-[1px] overflow-hidden rounded-[var(--radius-md)] border border-[var(--border-subtle)]"
      >
        {Array.from({ length: 4 }, (_, i) => (
          <div key={i} className="h-[var(--space-8)] bg-[var(--surface-2)] opacity-60 animate-pulse" />
        ))}
      </div>
    )
  }
  if (groups.isError) {
    return (
      <p className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]">
        Couldn't load activity.
      </p>
    )
  }

  return (
    <>
      <DataTable
        rows={rows}
        columns={columns}
        rowKey={(g) => g.id}
        defaultSort={{ key: 'edits', dir: 'desc' }}
        stale={groups.isFetching}
        caption={by === 'org' ? 'Activity by team' : 'Activity by space'}
        empty={by === 'org' ? 'No teams yet.' : 'No spaces yet.'}
      />
      <p className="m-0 text-[length:var(--text-xs)] text-[var(--text-muted)]">
        {by === 'org'
          ? 'A team’s activity is its members’ activity, wherever they did it — so somebody in two teams counts toward both.'
          : 'A space’s activity is what happened to its pages, whoever did it. Personal spaces are included.'}
      </p>
    </>
  )
}

function Num({ n }: { n: number }) {
  if (n === 0) return <span className="text-[var(--text-muted)]">—</span>
  return <span>{n.toLocaleString()}</span>
}
