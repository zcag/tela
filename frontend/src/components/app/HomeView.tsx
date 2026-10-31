import { useMemo } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import {
  Bot,
  Clock,
  Compass,
  FileClock,
  FilePlus,
  FileText,
  FolderPlus,
  Network,
  PencilLine,
  Search,
  Star,
  type LucideIcon,
} from 'lucide-react'
import { useMe } from '../../lib/queries/auth'
import { useRecentChanges } from '../../lib/queries/recent-changes'
import { useFavorites } from '../../lib/queries/favorites'
import { useSpaces } from '../../lib/queries/spaces'
import { readRecentPages } from '../../lib/recentPages'
import { relativeTimeFromSqlite } from '../../lib/relativeTime'
import { emitOpenNewPage } from '../../lib/newPageEvent'
import { emitOpenNewSpace } from '../../lib/newSpaceEvent'
import { emitOpenPalette } from '../../lib/paletteEvent'
import { Button } from '../ui/button'
import { cn } from '../../lib/utils'
import { DOCS } from '../../lib/docs'
import { ConnectAgentNudge } from './ConnectAgentNudge'
import { useDocumentTitle } from '../../lib/useHeadMeta'

// Home dashboard — the app's landing surface (mounted at `/`). A launchpad plus
// lenses on the wiki: what changed, what your agents changed, what you starred /
// visited. Flat + token-driven to match the app (depth via surface + accent
// tints, not shadows).

// Per-widget row cap — the dashboard is a glance, so no single list shoves the
// others far down. (The sidebar Favorites list + the command palette show the
// full sets.)
const DASH_LIMIT = 5

function greetingFor(hour: number): string {
  if (hour < 5) return 'Good evening'
  if (hour < 12) return 'Good morning'
  if (hour < 18) return 'Good afternoon'
  return 'Good evening'
}

export function HomeRoute() {
  useDocumentTitle('Home')
  const me = useMe()
  const spaces = useSpaces()
  const recent = useRecentChanges({ limit: DASH_LIMIT })
  // The pair: pages YOU edited by hand vs pages YOUR AI edited (both author=you,
  // split by revision source — an agent write via MCP authenticates as you).
  const myEdits = useRecentChanges({ mine: true, source: 'human', limit: DASH_LIMIT })
  const agentChanges = useRecentChanges({ mine: true, source: 'agent', limit: DASH_LIMIT })
  const favorites = useFavorites()
  const visited = useMemo(() => readRecentPages(), [])
  const greeting = useMemo(() => greetingFor(new Date().getHours()), [])
  // Address the user by their human name; fall back to the (slug) username.
  const displayName = me.data?.display_name?.trim() || me.data?.username

  const favItems = (favorites.data ?? []).slice(0, DASH_LIMIT)
  const visitedItems = visited.slice(0, DASH_LIMIT)
  const spaceCount = spaces.data?.length ?? 0
  const favCount = favorites.data?.length ?? 0
  const noSpaces = spaces.isSuccess && spaceCount === 0

  return (
    <div className="flex-1 overflow-y-auto">
      {/* Roving target for the j/k keyboard layer (see lib/keys): the cursor
          flows through every page row (PageRow) across the dashboard widgets. */}
      <div
        data-keynav-region="list"
        className="max-w-[64rem] w-full mx-auto px-[var(--space-6)] py-[var(--space-7)] flex flex-col gap-[var(--space-6)]"
      >
        <header className="flex flex-col gap-[var(--space-2)]">
          <h1 className="m-0 font-[family-name:var(--font-sans)] text-[length:var(--text-2xl)] leading-[var(--leading-tight)] font-semibold text-[var(--text-primary)]">
            {displayName ? `${greeting}, ${displayName}` : greeting}
          </h1>
          <div className="flex items-center gap-[var(--space-2)] text-[length:var(--text-sm)] text-[var(--text-muted)]">
            <Stat value={spaceCount} label={spaceCount === 1 ? 'space' : 'spaces'} />
            <Dot />
            <Stat value={favCount} label={favCount === 1 ? 'favorite' : 'favorites'} />
          </div>
        </header>

        {/* Quick actions launchpad. */}
        <div className="flex flex-wrap gap-[var(--space-2)]">
          <Button variant="primary" size="sm" onClick={() => emitOpenNewPage()}>
            <FilePlus width={14} height={14} />
            <span>New page</span>
          </Button>
          <Button asChild variant="secondary" size="sm">
            <Link to="/n">
              <PencilLine width={14} height={14} />
              <span>Quick note</span>
            </Link>
          </Button>
          <Button variant="secondary" size="sm" onClick={() => emitOpenPalette('pages')}>
            <Search width={14} height={14} />
            <span>Search</span>
          </Button>
          <Button asChild variant="secondary" size="sm">
            <Link to="/graph">
              <Network width={14} height={14} />
              <span>Graph</span>
            </Link>
          </Button>
          <Button asChild variant="secondary" size="sm">
            <a href={DOCS.tour} target="_blank" rel="noreferrer">
              <Compass width={14} height={14} />
              <span>Take the tour</span>
            </a>
          </Button>
        </div>

        <ConnectAgentNudge />

        {noSpaces ? (
          <FirstRunEmptyState />
        ) : (
          <>
            <SpacesGrid />

            <div className="grid grid-cols-1 lg:grid-cols-3 gap-[var(--space-4)]">
              <Widget
                className="lg:col-span-2"
                icon={FileClock}
                title="Recent changes"
                count={recent.data?.length}
                loading={recent.isLoading}
                error={recent.isError}
                empty={!recent.data || recent.data.length === 0}
                emptyText="No edits yet. Create or edit a page and it shows up here."
              >
                {recent.data?.map((c) => (
                  <PageRow
                    key={`change-${c.page_id}`}
                    spaceId={c.space_id}
                    pageId={c.page_id}
                    title={c.title}
                    meta={`${c.space_name} · ${
                      c.author_username ? `${c.author_username} · ` : ''
                    }${relativeTimeFromSqlite(c.updated_at)}`}
                  />
                ))}
              </Widget>

              <div className="flex flex-col gap-[var(--space-4)]">
                <Widget
                  icon={Star}
                  title="Favorites"
                  count={favItems.length}
                  loading={favorites.isLoading}
                  error={favorites.isError}
                  empty={favItems.length === 0}
                  emptyText="Star a page to pin it here."
                >
                  {favItems.map((f) => (
                    <PageRow
                      key={`fav-${f.page_id}`}
                      spaceId={f.space_id}
                      pageId={f.page_id}
                      title={f.title}
                      meta={f.space_name}
                    />
                  ))}
                </Widget>

                <Widget
                  icon={Clock}
                  title="Recently visited"
                  count={visitedItems.length}
                  empty={visitedItems.length === 0}
                  emptyText="Pages you open will appear here."
                >
                  {visitedItems.map((v) => (
                    <PageRow
                      key={`visited-${v.pageId}`}
                      spaceId={v.spaceId}
                      pageId={v.pageId}
                      title={v.title}
                    />
                  ))}
                </Widget>
              </div>
            </div>

            {/* You vs your AI — a matched pair: pages you edited by hand, and
                pages your agents edited (via MCP) on your behalf. */}
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-[var(--space-4)]">
              <Widget
                icon={PencilLine}
                title="My recent edits"
                count={myEdits.data?.length}
                loading={myEdits.isLoading}
                error={myEdits.isError}
                empty={!myEdits.data || myEdits.data.length === 0}
                emptyText="Pages you edit by hand will appear here."
              >
                {myEdits.data?.map((c) => (
                  <PageRow
                    key={`mine-${c.page_id}`}
                    spaceId={c.space_id}
                    pageId={c.page_id}
                    title={c.title}
                    meta={`${c.space_name} · ${relativeTimeFromSqlite(c.updated_at)}`}
                  />
                ))}
              </Widget>

              <Widget
                icon={Bot}
                title="Changes by your AI"
                count={agentChanges.data?.length}
                loading={agentChanges.isLoading}
                error={agentChanges.isError}
                empty={!agentChanges.data || agentChanges.data.length === 0}
                emptyText="Pages your agents edit (via MCP) will appear here."
              >
                {agentChanges.data?.map((c) => (
                  <PageRow
                    key={`agent-${c.page_id}`}
                    spaceId={c.space_id}
                    pageId={c.page_id}
                    title={c.title}
                    meta={`${c.space_name} · ${relativeTimeFromSqlite(c.updated_at)}`}
                  />
                ))}
              </Widget>
            </div>
          </>
        )}
      </div>
    </div>
  )
}

function Stat({ value, label }: { value: number; label: string }) {
  return (
    <span>
      <span className="text-[var(--text-primary)] font-medium tabular-nums">{value}</span> {label}
    </span>
  )
}

function Dot() {
  return <span aria-hidden className="text-[var(--border-strong)]">·</span>
}

// Spaces you can reach, as compact jump-in cards with a colored monogram (the
// same 8-hue presence palette the graph uses, so a space's colour is consistent
// across the app).
function SpacesGrid() {
  const spaces = useSpaces()
  if (!spaces.data || spaces.data.length === 0) return null
  return (
    <section className="flex flex-col gap-[var(--space-3)]">
      <div className="flex items-center justify-between gap-[var(--space-2)]">
        <h2 className="m-0 flex items-center gap-[var(--space-2)] text-[length:var(--text-xs)] uppercase tracking-wider text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
          Your spaces
        </h2>
        <Button variant="ghost" size="sm" onClick={() => emitOpenNewSpace()}>
          <FolderPlus width={14} height={14} /> New space
        </Button>
      </div>
      <ul className="m-0 p-0 list-none grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-[var(--space-2)]">
        {spaces.data.map((s, i) => (
          <li key={s.id}>
            <Link
              to="/spaces/$spaceId"
              params={{ spaceId: s.id }}
              className={cn(
                'flex items-center gap-[var(--space-3)] p-[var(--space-3)]',
                'rounded-[var(--radius-md)] border border-[var(--border-subtle)] bg-[var(--surface-1)]',
                'no-underline transition-colors duration-[var(--duration-fast)]',
                'hover:border-[var(--border-strong)] hover:bg-[var(--surface-2)]',
              )}
            >
              <Monogram name={s.name} index={i} />
              <span className="flex flex-col min-w-0">
                <span className="truncate text-[length:var(--text-sm)] font-medium text-[var(--text-primary)]">
                  {s.name}
                </span>
                <span className="truncate text-[length:var(--text-xs)] text-[var(--text-muted)]">
                  {s.is_personal
                    ? 'Personal'
                    : `${s.member_count ?? 0} ${(s.member_count ?? 0) === 1 ? 'member' : 'members'}`}
                </span>
              </span>
            </Link>
          </li>
        ))}
      </ul>
    </section>
  )
}

function Monogram({ name, index }: { name: string; index: number }) {
  const hue = `--collab-cursor-${(index % 8) + 1}`
  return (
    <span
      aria-hidden
      className="flex items-center justify-center size-[var(--space-7)] shrink-0 rounded-[var(--radius-md)] text-[length:var(--text-sm)] font-semibold"
      style={{
        background: `color-mix(in oklch, var(${hue}) 16%, transparent)`,
        color: `var(${hue})`,
      }}
    >
      {(name.trim()[0] || '?').toUpperCase()}
    </span>
  )
}

function FirstRunEmptyState() {
  const navigate = useNavigate()
  return (
    <section
      className={cn(
        'flex flex-col items-start gap-[var(--space-3)] p-[var(--space-6)]',
        'rounded-[var(--radius-lg)] border border-[var(--border-subtle)] bg-[var(--surface-1)]',
      )}
    >
      <h2 className="m-0 text-[length:var(--text-lg)] font-semibold text-[var(--text-primary)]">
        Make your first space
      </h2>
      <p className="m-0 max-w-[40rem] text-[length:var(--text-sm)] text-[var(--text-muted)] leading-[var(--leading-relaxed)]">
        A space is a tree of markdown pages your team writes together. Create one,
        and we’ll drop in a short Welcome page to get you going.
      </p>
      <div className="flex flex-wrap gap-[var(--space-2)]">
        <Button variant="primary" size="sm" onClick={() => void navigate({ to: '/n' })}>
          <PencilLine width={14} height={14} />
          <span>Start a quick note</span>
        </Button>
        <Button asChild variant="secondary" size="sm">
          <a href={DOCS.tour} target="_blank" rel="noreferrer">
            <Compass width={14} height={14} />
            <span>Take the tour</span>
          </a>
        </Button>
      </div>
    </section>
  )
}

function Widget({
  icon: Icon,
  title,
  count,
  loading,
  error,
  empty,
  emptyText,
  className,
  children,
}: {
  icon: LucideIcon
  title: string
  count?: number
  loading?: boolean
  error?: boolean
  empty?: boolean
  emptyText: string
  className?: string
  children?: React.ReactNode
}) {
  return (
    <section
      className={cn(
        'flex flex-col gap-[var(--space-3)] p-[var(--space-5)]',
        'rounded-[var(--radius-lg)] border border-[var(--border-subtle)] bg-[var(--surface-1)]',
        className,
      )}
    >
      <div className="flex items-center gap-[var(--space-2)]">
        <span className="flex items-center justify-center size-[var(--space-6)] shrink-0 rounded-[var(--radius-sm)] bg-[color-mix(in_oklch,var(--accent)_10%,transparent)] text-[var(--accent)]">
          <Icon width={14} height={14} aria-hidden />
        </span>
        <h2 className="m-0 flex-1 text-[length:var(--text-sm)] font-semibold text-[var(--text-primary)] font-[family-name:var(--font-sans)]">
          {title}
        </h2>
        {!loading && !error && count != null && count > 0 ? (
          <span className="text-[length:var(--text-xs)] text-[var(--text-muted)] tabular-nums">
            {count}
          </span>
        ) : null}
      </div>
      {loading ? (
        <WidgetSkeleton />
      ) : error ? (
        <p role="alert" className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]">
          Couldn’t load.
        </p>
      ) : empty ? (
        <div className="flex flex-col items-center gap-[var(--space-2)] py-[var(--space-4)] text-center">
          <Icon width={18} height={18} aria-hidden className="text-[var(--text-muted)] opacity-50" />
          <p className="m-0 max-w-[20rem] text-[length:var(--text-sm)] text-[var(--text-muted)] leading-[var(--leading-normal)]">
            {emptyText}
          </p>
        </div>
      ) : (
        <ul className="m-0 p-0 list-none flex flex-col gap-[1px]">{children}</ul>
      )}
    </section>
  )
}

function WidgetSkeleton() {
  return (
    <div className="flex flex-col gap-[var(--space-3)] py-[var(--space-1)]">
      {['w-4/5', 'w-3/5', 'w-2/3'].map((w, i) => (
        <div key={i} className="flex flex-col gap-[var(--space-1)]">
          <div className={cn('h-[var(--space-3)] rounded-[var(--radius-xs)] bg-[var(--surface-2)] animate-pulse', w)} />
          <div className="h-[var(--space-2)] w-2/5 rounded-[var(--radius-xs)] bg-[var(--surface-2)] animate-pulse" />
        </div>
      ))}
    </div>
  )
}

function PageRow({
  spaceId,
  pageId,
  title,
  meta,
}: {
  spaceId: number
  pageId: number
  title: string
  meta?: React.ReactNode
}) {
  return (
    <li>
      <Link
        to="/spaces/$spaceId/pages/$pageId/{-$slug}"
        params={{ spaceId, pageId, slug: undefined }}
        data-keynav-item
        className={cn(
          'flex items-center gap-[var(--space-2)] px-[var(--space-2)] py-[var(--space-2)]',
          'rounded-[var(--radius-sm)] no-underline',
          'hover:bg-[var(--surface-2)] transition-colors duration-[var(--duration-fast)]',
        )}
      >
        <FileText
          width={14}
          height={14}
          aria-hidden
          className="shrink-0 text-[var(--text-muted)]"
        />
        <span className="flex flex-col min-w-0 flex-1">
          <span className="truncate text-[length:var(--text-sm)] text-[var(--text-primary)]">
            {title || 'Untitled'}
          </span>
          {meta ? (
            <span className="truncate text-[length:var(--text-xs)] text-[var(--text-muted)]">
              {meta}
            </span>
          ) : null}
        </span>
      </Link>
    </li>
  )
}
