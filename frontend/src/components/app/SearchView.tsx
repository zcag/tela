import { useEffect, useMemo, useState } from 'react'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { Search } from 'lucide-react'
import { Card, CardBody } from '../ui/card'
import { Input } from '../ui/input'
import { Toggle } from '../ui/toggle'
import { useSpaces } from '../../lib/queries/spaces'
import { useDebouncedValue } from '../../lib/useDebouncedValue'
import { bodyExcerpt } from '../../lib/search/body-excerpt'
import {
  BodyIndex,
  getLoadedBodyIndex,
  loadedSpaceIds,
  type BodySearchHit,
} from '../../lib/search/body-index'
import { navigateToPage } from '../../lib/pageHitItem'
import type { Space } from '../../lib/types'
import { SearchResult } from './SearchResult'
import { usePageHoverPreview } from './wikilink-hover-preview'
import { useDocumentTitle } from '../../lib/useHeadMeta'

interface SearchSearchParams {
  q?: string
  spaces?: number[]
  since?: string
}

interface SearchRow extends BodySearchHit {
  space_id: number
}

const PER_SPACE_LIMIT = 50
const TOTAL_LIMIT = 50
const URL_DEBOUNCE_MS = 200
const EXCERPT_HALF_WIDTH = 100

// Lazy-loaded route component for the `/search` page. Loads every readable
// space's BodyIndex on mount (registry de-dupes if already cached), then runs
// the active query against each via Orama, merging client-side. The route's
// `q` / `spaces` / `since` URL params are the source of truth — typing into
// the input commits to URL on a 200 ms debounce so a refresh re-opens to the
// same query.
//
// Yjs scope (Hard Rule #6): zero Yjs imports anywhere on this page.
export function SearchRoute() {
  useDocumentTitle('Search')
  const params = useSearch({ from: '/_app/search' }) as SearchSearchParams
  const navigate = useNavigate()
  const spacesQuery = useSpaces()
  const allSpaces = useMemo<Space[]>(
    () => spacesQuery.data ?? [],
    [spacesQuery.data],
  )

  const urlQ = params.q ?? ''
  const filterSpaces = useMemo(
    () => (Array.isArray(params.spaces) ? params.spaces : []),
    [params.spaces],
  )
  const since = params.since ?? ''

  // URL-debounced input. Local state owns the per-keystroke value; the URL
  // updates on debounce so a refresh restores the same query and back/forward
  // doesn't thrash. Initial value comes from the URL so deep-links work.
  const [inputValue, setInputValue] = useState(urlQ)
  const debouncedInput = useDebouncedValue(inputValue, URL_DEBOUNCE_MS)
  useEffect(() => {
    if (debouncedInput === urlQ) return
    void navigate({
      to: '/search',
      search: (prev: SearchSearchParams) => ({
        ...prev,
        q: debouncedInput.length > 0 ? debouncedInput : undefined,
      }),
      replace: true,
    })
  }, [debouncedInput, urlQ, navigate])

  // Track which space indexes have finished their initial load+refresh. A
  // newly-finished index bumps `loadedIds`, which re-runs the results memo
  // so users see hits appear progressively as each space comes online.
  const [loadedIds, setLoadedIds] = useState<ReadonlySet<number>>(
    () => new Set(loadedSpaceIds()),
  )
  useEffect(() => {
    if (!spacesQuery.data) return
    let cancelled = false
    for (const sp of spacesQuery.data) {
      void BodyIndex.load(sp.id)
        .then((idx) => idx.refresh())
        .finally(() => {
          if (cancelled) return
          setLoadedIds((prev) => {
            if (prev.has(sp.id)) return prev
            const next = new Set(prev)
            next.add(sp.id)
            return next
          })
        })
    }
    return () => {
      cancelled = true
    }
  }, [spacesQuery.data])

  const stillLoading = allSpaces.some((s) => !loadedIds.has(s.id))

  // Effective target space ids: explicit filter or all readable spaces.
  const targetSpaceIds = useMemo<number[]>(() => {
    if (filterSpaces.length > 0) return filterSpaces
    return allSpaces.map((s) => s.id)
  }, [filterSpaces, allSpaces])

  const [results, setResults] = useState<SearchRow[]>([])
  const preview = usePageHoverPreview()
  useEffect(() => {
    const q = urlQ.trim()
    if (q.length === 0) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setResults([])
      return
    }
    let cancelled = false
    void Promise.all(
      targetSpaceIds.map(async (id) => {
        const idx = getLoadedBodyIndex(id)
        if (!idx) return [] as SearchRow[]
        const rows = await idx.search(q, { limit: PER_SPACE_LIMIT })
        return rows.map<SearchRow>((h) => ({ ...h, space_id: id }))
      }),
    )
      .then((perSpace) => {
        if (cancelled) return
        let merged = perSpace.flat()
        if (since.length > 0) {
          merged = merged.filter((r) => r.updated_at > since)
        }
        merged = merged.sort((a, b) => b.score - a.score).slice(0, TOTAL_LIMIT)
        setResults(merged)
      })
      .catch(() => {
        if (!cancelled) setResults([])
      })
    return () => {
      cancelled = true
    }
    // loadedIds participates so newly-finished indexes refresh the result set
    // even when the URL params are unchanged.
  }, [urlQ, targetSpaceIds, since, loadedIds])

  function toggleSpace(spaceId: number) {
    const present = filterSpaces.includes(spaceId)
    const next = present
      ? filterSpaces.filter((id) => id !== spaceId)
      : [...filterSpaces, spaceId]
    void navigate({
      to: '/search',
      search: (prev: SearchSearchParams) => ({
        ...prev,
        spaces: next.length > 0 ? next : undefined,
      }),
      replace: true,
    })
  }

  function clearSpaces() {
    if (filterSpaces.length === 0) return
    void navigate({
      to: '/search',
      search: (prev: SearchSearchParams) => ({ ...prev, spaces: undefined }),
      replace: true,
    })
  }

  function handleSinceChange(e: React.ChangeEvent<HTMLInputElement>) {
    const value = e.target.value
    void navigate({
      to: '/search',
      search: (prev: SearchSearchParams) => ({
        ...prev,
        since: value.length > 0 ? value : undefined,
      }),
      replace: true,
    })
  }

  const trimmed = urlQ.trim()

  return (
    <div className="flex-1 flex flex-col gap-[var(--space-5)] p-[var(--space-7)] max-w-[64rem] w-full mx-auto min-h-0">
      <header className="flex items-center gap-[var(--space-3)]">
        <Search
          aria-hidden
          width={18}
          height={18}
          className="text-[var(--text-muted)]"
        />
        <h1 className="m-0 text-[length:var(--text-xl)] leading-[var(--leading-tight)] font-[family-name:var(--font-sans)] text-[var(--text-primary)]">
          Search
        </h1>
        <span
          aria-live="polite"
          className="ml-auto text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]"
        >
          {trimmed.length > 0
            ? `${results.length} result${results.length === 1 ? '' : 's'}`
            : ''}
        </span>
      </header>

      <Card>
        <CardBody className="gap-[var(--space-4)]">
          <Input
            type="text"
            value={inputValue}
            onChange={(e) => setInputValue(e.target.value)}
            placeholder="Search across all your spaces…"
            aria-label="Search query"
            autoFocus
          />
          <SpaceFilterChips
            spaces={allSpaces}
            selected={filterSpaces}
            onToggle={toggleSpace}
            onClear={clearSpaces}
          />
          <div className="flex items-center gap-[var(--space-3)]">
            <label
              htmlFor="search-since"
              className="text-[length:var(--text-sm)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]"
            >
              Updated since
            </label>
            <Input
              id="search-since"
              type="date"
              value={since}
              onChange={handleSinceChange}
              aria-label="Filter by updated-since date"
              className="max-w-[12rem]"
            />
          </div>
          {stillLoading ? (
            <p
              role="status"
              className="m-0 text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]"
            >
              Loading indexes…
            </p>
          ) : null}
        </CardBody>
      </Card>

      <section
        // Roving target for the j/k keyboard layer (see lib/keys); outranks the
        // sidebar so j/k drives results while on /search.
        data-keynav-region="list"
        aria-label="Search results"
        className="flex flex-col gap-[var(--space-1)] min-h-0"
      >
        {trimmed.length === 0 ? (
          <p className="m-0 px-[var(--space-4)] py-[var(--space-3)] text-[length:var(--text-sm)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
            Type to search across all your spaces.
          </p>
        ) : results.length === 0 ? (
          <p className="m-0 px-[var(--space-4)] py-[var(--space-3)] text-[length:var(--text-sm)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
            No pages match &quot;{trimmed}&quot;.
          </p>
        ) : (
          results.map((row) => (
            <SearchResult
              key={`${row.space_id}-${row.id}`}
              title={row.title}
              breadcrumb={
                allSpaces.find((s) => s.id === row.space_id)?.name ?? ''
              }
              excerpt={bodyExcerpt(row.body, trimmed, EXCERPT_HALF_WIDTH)}
              updatedAt={row.updated_at}
              onSelect={() => navigateToPage(row.space_id, row.id)}
              hoverProps={preview.triggerProps(row.id, row.title)}
            />
          ))
        )}
        {preview.card}
      </section>
    </div>
  )
}

interface SpaceFilterChipsProps {
  spaces: Space[]
  selected: number[]
  onToggle: (spaceId: number) => void
  onClear: () => void
}

function SpaceFilterChips({
  spaces,
  selected,
  onToggle,
  onClear,
}: SpaceFilterChipsProps) {
  const allSelected = selected.length === 0
  return (
    <div className="flex flex-wrap items-center gap-[var(--space-2)]">
      <span className="text-[length:var(--text-sm)] text-[var(--text-muted)] font-[family-name:var(--font-sans)] mr-[var(--space-1)]">
        Spaces
      </span>
      <Toggle
        size="sm"
        pressed={allSelected}
        onPressedChange={(next) => {
          if (next) onClear()
        }}
      >
        All
      </Toggle>
      {spaces.map((sp) => {
        const isOn = selected.includes(sp.id)
        return (
          <Toggle
            key={sp.id}
            size="sm"
            pressed={isOn}
            onPressedChange={() => onToggle(sp.id)}
          >
            {sp.name}
          </Toggle>
        )
      })}
    </div>
  )
}
