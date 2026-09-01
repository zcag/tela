import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useSearch } from '@tanstack/react-router'
import {
  EVENT_TYPE_GROUPS,
  useInfiniteEvents,
  type EventFilters,
} from '../../lib/queries/events'
import { EventRow, collapseEvents } from './EventRow'
import { IncludeAdminsToggle } from './IncludeAdminsToggle'
import { Toggle } from '../ui/toggle'
import { Input } from '../ui/input'
import { Button } from '../ui/button'
import { cn } from '../../lib/utils'

// Instance-admin activity feed: every login, page view/edit, access change, ask,
// and API request, newest-first, with type/search/date filters and keyset-based
// infinite scroll (the first useInfiniteQuery in the app).
export function SettingsEventsTab() {
  // Deep-link filters from the People table: ?user=<id>&etypes=<type>&admins=1.
  // Arriving with a user pinned is how a number in that table becomes the rows
  // behind it — and `admins` comes along because this feed hides instance-admin
  // activity by default, so clicking an admin's count would otherwise land on
  // an empty feed that reads as broken.
  const search$ = useSearch({ from: '/_app/settings' })
  const navigate = useNavigate()
  const pinnedUser = search$.user
  // Selected group labels (empty = all). Each group maps to one or more type
  // tokens passed to the backend.
  const [groups, setGroups] = useState<Set<string>>(new Set())
  const [search, setSearch] = useState('')
  const [debouncedSearch, setDebouncedSearch] = useState('')
  const [since, setSince] = useState('')
  const [includeAdmins, setIncludeAdmins] = useState(search$.admins === 1)

  // Debounce the free-text box so each keystroke doesn't refetch.
  useEffect(() => {
    const t = setTimeout(() => setDebouncedSearch(search), 250)
    return () => clearTimeout(t)
  }, [search])

  const filters = useMemo<EventFilters>(() => {
    const types = EVENT_TYPE_GROUPS.filter((g) => groups.has(g.label)).flatMap(
      (g) => g.types,
    )
    // An explicit ?etypes= from a deep link wins over the chips, which start
    // empty on arrival — the caller asked for one kind of event, not all of them.
    const deepTypes = search$.etypes ? search$.etypes.split(',') : undefined
    return {
      types: deepTypes ?? (types.length > 0 ? types : undefined),
      userId: pinnedUser,
      q: debouncedSearch.trim() || undefined,
      since: since || undefined,
      includeAdmins: includeAdmins || undefined,
    }
  }, [groups, debouncedSearch, since, includeAdmins, pinnedUser, search$.etypes])

  // Drop the deep-link narrowing and go back to the whole feed.
  const clearPin = () =>
    void navigate({
      to: '/settings',
      search: { tab: 'events' },
      replace: true,
    })

  const query = useInfiniteEvents(filters)
  const {
    data,
    isLoading,
    isError,
    hasNextPage,
    isFetchingNextPage,
    fetchNextPage,
  } = query
  const events = useMemo(
    () => data?.pages.flatMap((p) => p.events) ?? [],
    [data],
  )
  // Collapse consecutive identical events (e.g. a burst of autosave edits on one
  // page by one user) into single "×N" rows so the feed stays readable.
  const eventGroups = useMemo(() => collapseEvents(events), [events])

  // Infinite scroll: when the sentinel scrolls into view and there's another
  // page, fetch it. fetchNextPage is referentially stable across renders.
  const sentinelRef = useRef<HTMLDivElement | null>(null)
  useEffect(() => {
    const el = sentinelRef.current
    if (!el) return
    const io = new IntersectionObserver((entries) => {
      if (entries[0]?.isIntersecting && hasNextPage && !isFetchingNextPage) {
        void fetchNextPage()
      }
    })
    io.observe(el)
    return () => io.disconnect()
  }, [hasNextPage, isFetchingNextPage, fetchNextPage])

  function toggleGroup(label: string) {
    setGroups((prev) => {
      const next = new Set(prev)
      if (next.has(label)) next.delete(label)
      else next.add(label)
      return next
    })
  }

  const hasFilters =
    groups.size > 0 || search !== '' || since !== '' || includeAdmins

  return (
    <section
      aria-labelledby="settings-events"
      className="flex flex-col gap-[var(--space-4)] min-h-0"
    >
      <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)] leading-[var(--leading-relaxed)]">
        Everything happening on this instance — sign-ins, page views and edits,
        access changes, asks, and API requests. Most recent first.
      </p>

      {pinnedUser ? (
        <div className="flex items-center gap-[var(--space-2)] rounded-[var(--radius-sm)] border border-[var(--accent)] bg-[var(--surface-2)] px-[var(--space-3)] py-[var(--space-2)] text-[length:var(--text-sm)]">
          <span className="text-[var(--text-primary)]">
            Showing one person's activity
            {search$.etypes ? ` · ${search$.etypes.split(',').join(', ')}` : ''}
          </span>
          <Button type="button" variant="ghost" size="sm" onClick={clearPin}>
            Show everyone
          </Button>
        </div>
      ) : null}

      {/* Filter bar */}
      <div className="flex flex-col gap-[var(--space-3)]">
        <div className="flex flex-wrap items-center gap-[var(--space-2)]">
          {EVENT_TYPE_GROUPS.map((g) => (
            <Toggle
              key={g.label}
              size="sm"
              pressed={groups.has(g.label)}
              onPressedChange={() => toggleGroup(g.label)}
            >
              {g.label}
            </Toggle>
          ))}
        </div>
        <div className="flex flex-wrap items-center gap-[var(--space-3)]">
          <Input
            type="search"
            placeholder="Search actor, page, detail…"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="max-w-[18rem]"
          />
          <label className="flex items-center gap-[var(--space-2)] text-[length:var(--text-sm)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
            Since
            <Input
              type="date"
              value={since}
              onChange={(e) => setSince(e.target.value)}
              className="max-w-[10rem]"
            />
          </label>
          <IncludeAdminsToggle
            checked={includeAdmins}
            onChange={setIncludeAdmins}
          />
          {hasFilters ? (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => {
                setGroups(new Set())
                setSearch('')
                setSince('')
                setIncludeAdmins(false)
              }}
            >
              Clear
            </Button>
          ) : null}
        </div>
      </div>

      {/* Feed */}
      {isLoading ? (
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
          Loading events…
        </p>
      ) : isError ? (
        <p role="alert" className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]">
          Couldn't load events.
        </p>
      ) : events.length > 0 ? (
        <>
          <ul className="m-0 p-0 list-none flex flex-col gap-[var(--space-1)]">
            {eventGroups.map((g) => (
              <EventRow
                key={g.head.id}
                event={g.head}
                count={g.count}
                oldestAt={g.oldestAt}
              />
            ))}
          </ul>
          <div ref={sentinelRef} aria-hidden className="h-[var(--space-6)]" />
          <p
            className={cn(
              'm-0 text-center text-[length:var(--text-xs)] text-[var(--text-muted)]',
              !isFetchingNextPage && 'invisible',
            )}
          >
            Loading more…
          </p>
        </>
      ) : (
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
          {hasFilters ? 'No events match these filters.' : 'No events recorded yet.'}
        </p>
      )}
    </section>
  )
}
