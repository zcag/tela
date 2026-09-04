import { useMemo } from 'react'
import {
  CalendarClock,
  Clock,
  Copy,
  FileText,
  Folder,
  RotateCcw,
  ShieldAlert,
  Trash2,
  Unlink,
} from 'lucide-react'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '../ui/tabs'
import { Button } from '../ui/button'
import { EmptyState } from '../ui/empty-state'
import { useSpaceOverview } from '../../lib/queries/space-overview'
import { useSpaces } from '../../lib/queries/spaces'
import { useRestorePage, useSpaceTrash, type TrashEntry } from '../../lib/queries/space-trash'
import { FollowButton } from './FollowButton'
import { navigateToPage } from '../../lib/pageHitItem'
import { relativeTimeFromSqlite } from '../../lib/relativeTime'
import { cn } from '../../lib/utils'

// The per-space landing (fills the old "select a page" placeholder). Three tabs:
// Overview — a content-first hub (top-level pages + recent activity); Health —
// the per-space maintenance worklist (disputes, review-overdue, orphans, dupes);
// and Trash — deleted pages, with the one write on this screen: Restore. Nothing
// here authors otherwise; every other row just navigates.
export function SpaceView({ spaceId }: { spaceId: number }) {
  const { data, isLoading } = useSpaceOverview(spaceId)
  const spacesQuery = useSpaces()
  const trash = useSpaceTrash(spaceId)
  const restore = useRestorePage()
  const spaceName = useMemo(
    () => spacesQuery.data?.find((s) => s.id === spaceId)?.name ?? 'Space',
    [spacesQuery.data, spaceId],
  )

  const h = data?.health
  // The badge counts genuine defects only — a contradiction, a redundant page, a
  // page past its own review cadence. Orphans are a mild "could be better linked"
  // nudge (a standalone reference is fine), so they get a section but no alarm.
  const problems =
    (h?.disputed.length ?? 0) + (h?.review_overdue.length ?? 0) + (h?.duplicates.length ?? 0)
  const anything = problems + (h?.orphans.length ?? 0)

  return (
    <div className="flex-1 overflow-y-auto min-h-0">
      <div className="flex flex-col gap-[var(--space-5)] p-[var(--space-7)] max-w-[56rem] w-full mx-auto">
        <header className="flex items-baseline gap-[var(--space-3)]">
          <h1 className="m-0 text-[length:var(--text-3xl)] leading-[var(--leading-tight)] font-medium text-[var(--text-primary)] font-[family-name:var(--font-sans)]">
            {spaceName}
          </h1>
          {data ? (
            <span className="text-[length:var(--text-sm)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
              {data.pages} {data.pages === 1 ? 'page' : 'pages'}
            </span>
          ) : null}
          <span className="ml-auto self-center">
            <FollowButton id={spaceId} kind="space" />
          </span>
        </header>

        <Tabs defaultValue="overview" className="flex flex-col gap-[var(--space-4)]">
          <TabsList>
            <TabsTrigger value="overview">Overview</TabsTrigger>
            <TabsTrigger value="health">
              <span className="inline-flex items-center gap-[var(--space-2)]">
                Health
                {problems > 0 ? (
                  <span className="inline-flex items-center justify-center min-w-[var(--space-5)] h-[var(--space-5)] px-[var(--space-1)] rounded-full text-[length:var(--text-xs)] bg-[color-mix(in_oklch,var(--danger)_18%,transparent)] text-[var(--danger)]">
                    {problems}
                  </span>
                ) : null}
              </span>
            </TabsTrigger>
            <TabsTrigger value="trash">
              <span className="inline-flex items-center gap-[var(--space-2)]">
                Trash
                {trash.data && trash.data.length > 0 ? (
                  <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">
                    {trash.data.length}
                  </span>
                ) : null}
              </span>
            </TabsTrigger>
          </TabsList>

          {/* ---- Overview ---- */}
          <TabsContent value="overview" className="flex flex-col gap-[var(--space-6)]">
            {isLoading ? (
              <Skeleton />
            ) : (
              <>
                <Section title="Contents" icon={Folder}>
                  {data && data.top_level.length > 0 ? (
                    <List>
                      {data.top_level.map((p) => (
                        <Row
                          key={p.id}
                          icon={FileText}
                          title={p.title || 'Untitled'}
                          meta={
                            p.children > 0
                              ? `${p.children} ${p.children === 1 ? 'page' : 'pages'}`
                              : undefined
                          }
                          onSelect={() => navigateToPage(spaceId, p.id)}
                        />
                      ))}
                    </List>
                  ) : (
                    <Muted>No top-level pages yet.</Muted>
                  )}
                </Section>

                {data && data.recent.length > 0 ? (
                  <Section title="Recently updated" icon={Clock}>
                    <List>
                      {data.recent.map((p) => (
                        <Row
                          key={p.id}
                          icon={FileText}
                          title={p.title || 'Untitled'}
                          meta={p.updated_at ? relativeTimeFromSqlite(p.updated_at) : undefined}
                          onSelect={() => navigateToPage(spaceId, p.id)}
                        />
                      ))}
                    </List>
                  </Section>
                ) : null}
              </>
            )}
          </TabsContent>

          {/* ---- Health ---- */}
          <TabsContent value="health" className="flex flex-col gap-[var(--space-6)]">
            {isLoading ? (
              <Skeleton />
            ) : anything === 0 ? (
              <EmptyState
                icon={ShieldAlert}
                title="This space looks healthy"
                description="No disputes, orphans, overdue reviews, or likely duplicates right now."
              />
            ) : (
              <>
                {h && h.disputed.length > 0 ? (
                  <Section title="Disputed" icon={ShieldAlert} tone="danger">
                    <List>
                      {h.disputed.map((d) => (
                        <Row
                          key={d.id}
                          icon={ShieldAlert}
                          tone="danger"
                          title={d.title || 'Untitled'}
                          meta={`${d.n} conflict${d.n === 1 ? '' : 's'}`}
                          onSelect={() => navigateToPage(spaceId, d.id)}
                        />
                      ))}
                    </List>
                  </Section>
                ) : null}

                {h && h.review_overdue.length > 0 ? (
                  <Section title="Needs review" icon={CalendarClock} tone="warning">
                    <List>
                      {h.review_overdue.map((p) => (
                        <Row
                          key={p.id}
                          icon={CalendarClock}
                          tone="warning"
                          title={p.title || 'Untitled'}
                          meta={`due — every ${p.every}d, ${p.age_days}d old`}
                          onSelect={() => navigateToPage(spaceId, p.id)}
                        />
                      ))}
                    </List>
                  </Section>
                ) : null}

                {h && h.orphans.length > 0 ? (
                  <Section title="Orphans" icon={Unlink}>
                    <List>
                      {h.orphans.map((p) => (
                        <Row
                          key={p.id}
                          icon={Unlink}
                          title={p.title || 'Untitled'}
                          meta="no links"
                          onSelect={() => navigateToPage(spaceId, p.id)}
                        />
                      ))}
                    </List>
                  </Section>
                ) : null}

                {h && h.duplicates.length > 0 ? (
                  <Section title="Possible duplicates" icon={Copy}>
                    <List>
                      {h.duplicates.map((d, i) => (
                        <Row
                          key={`${d.page_a}-${d.page_b}-${i}`}
                          icon={Copy}
                          title={`${d.title_a || 'Untitled'}  ·  ${d.title_b || 'Untitled'}`}
                          meta={`${Math.round(d.similarity * 100)}% alike`}
                          onSelect={() => navigateToPage(spaceId, d.page_a)}
                        />
                      ))}
                    </List>
                  </Section>
                ) : null}
              </>
            )}
          </TabsContent>

          {/* ---- Trash ---- */}
          <TabsContent value="trash" className="flex flex-col gap-[var(--space-4)]">
            {trash.isLoading ? (
              <Skeleton />
            ) : !trash.data || trash.data.length === 0 ? (
              <EmptyState
                icon={Trash2}
                title="Nothing deleted"
                description="Deleted pages come here instead of disappearing, and can be put back."
              />
            ) : (
              <Section title="Deleted pages" icon={Trash2}>
                <List>
                  {trash.data.map((e) => (
                    <Row
                      key={e.id}
                      icon={Trash2}
                      title={e.title || 'Untitled'}
                      meta={trashMeta(e)}
                      action={
                        <Button
                          variant="ghost"
                          size="sm"
                          disabled={restore.isPending}
                          onClick={() => restore.mutate({ id: e.id, spaceId })}
                        >
                          <RotateCcw width={13} height={13} aria-hidden />
                          {restore.isPending && restore.variables?.id === e.id
                            ? 'Restoring…'
                            : 'Restore'}
                        </Button>
                      }
                    />
                  ))}
                </List>
                {restore.isError ? (
                  <Muted>Could not restore that page — reload and try again.</Muted>
                ) : null}
              </Section>
            )}
          </TabsContent>
        </Tabs>
      </div>
    </div>
  )
}

function Section({
  title,
  icon: Icon,
  tone,
  children,
}: {
  title: string
  icon: typeof FileText
  tone?: 'danger' | 'warning'
  children: React.ReactNode
}) {
  const color =
    tone === 'danger' ? 'var(--danger)' : tone === 'warning' ? 'var(--warning)' : 'var(--text-muted)'
  return (
    <section className="flex flex-col gap-[var(--space-2)]">
      <h2
        className="m-0 flex items-center gap-[var(--space-2)] text-[length:var(--text-xs)] uppercase tracking-wider font-[family-name:var(--font-sans)]"
        style={{ color }}
      >
        <Icon width={13} height={13} aria-hidden />
        {title}
      </h2>
      {children}
    </section>
  )
}

function List({ children }: { children: React.ReactNode }) {
  return <ul className="m-0 p-0 list-none flex flex-col gap-[1px]">{children}</ul>
}

function Row({
  icon: Icon,
  title,
  meta,
  tone,
  onSelect,
  action,
}: {
  icon: typeof FileText
  title: string
  meta?: string
  tone?: 'danger' | 'warning'
  // Omitted when the row names something there is nothing to open — a deleted
  // page. The row then renders inert rather than as a dead button.
  onSelect?: () => void
  action?: React.ReactNode
}) {
  const inner = (
    <>
      <Icon
        width={14}
        height={14}
        aria-hidden
        className="shrink-0"
        style={{
          color:
            tone === 'danger'
              ? 'var(--danger)'
              : tone === 'warning'
                ? 'var(--warning)'
                : 'var(--text-muted)',
        }}
      />
      <span className="flex-1 min-w-0 truncate text-[length:var(--text-sm)] text-[var(--text-primary)] font-medium font-[family-name:var(--font-sans)]">
        {title}
      </span>
      {meta ? (
        <span className="shrink-0 text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
          {meta}
        </span>
      ) : null}
    </>
  )
  // The hover tint lives on the <li> so it covers the action too, and so the
  // action can be a real sibling button rather than nested inside one.
  const body = cn(
    'flex-1 min-w-0 text-left flex items-center gap-[var(--space-3)]',
    'px-[var(--space-3)] py-[var(--space-2)] rounded-[var(--radius-sm)]',
    'bg-transparent border-0 outline-none',
  )
  return (
    <li
      className={cn(
        'group m-0 p-0 list-none flex items-center gap-[var(--space-2)]',
        'rounded-[var(--radius-sm)] hover:bg-[var(--surface-2)]',
      )}
    >
      {onSelect ? (
        <button
          type="button"
          onClick={onSelect}
          className={cn(body, 'cursor-pointer focus-visible:ring-2 focus-visible:ring-[var(--accent)]')}
        >
          {inner}
        </button>
      ) : (
        <div className={body}>{inner}</div>
      )}
      {action ? <span className="shrink-0 pr-[var(--space-2)]">{action}</span> : null}
    </li>
  )
}

// "deleted 2 hours ago · 3 sub-pages · in Onboarding" — enough to tell two
// same-named pages apart and to see what a restore will bring back with it.
function trashMeta(e: TrashEntry): string {
  const bits = [`deleted ${relativeTimeFromSqlite(e.deleted_at)}`]
  if (e.sub_pages > 0) bits.push(`${e.sub_pages} sub-page${e.sub_pages === 1 ? '' : 's'}`)
  if (e.parent_title) bits.push(`in ${e.parent_title}`)
  return bits.join(' · ')
}

function Muted({ children }: { children: React.ReactNode }) {
  return (
    <p className="m-0 px-[var(--space-3)] text-[length:var(--text-sm)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
      {children}
    </p>
  )
}

function Skeleton() {
  return (
    <div className="flex flex-col gap-[var(--space-2)]">
      <div className="h-[var(--space-6)] w-1/3 rounded-[var(--radius-sm)] bg-[var(--surface-2)]" />
      <div className="h-[calc(var(--space-8)*2)] rounded-[var(--radius-md)] bg-[var(--surface-2)]" />
    </div>
  )
}
