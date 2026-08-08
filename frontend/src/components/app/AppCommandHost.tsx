import { useCallback, useEffect, useMemo, useState } from 'react'
import { ArrowRight, Clock, Search, Sparkles } from 'lucide-react'
import {
  CommandPalette,
  prefixForMode,
  usePaletteShortcuts,
  type CommandItem,
  type CommandItemGroup,
  type PagesItems,
} from '../ui/command'
import { useSpaces } from '../../lib/queries/spaces'
import { router } from '../../routes/router'
import { useTier3BodyHits } from '../../lib/useTier3BodyHits'
import { bodyExcerpt } from '../../lib/search/body-excerpt'
import {
  getTheme,
  setTheme,
  subscribeToTheme,
  type ThemeName,
} from '../../lib/theme'
import {
  materializeCommands,
  type CommandContext,
  type SubPickerSpec,
} from '../../lib/commands'
// Side-effect: starter commands self-register on import. Keep this import in
// the app-level host so the registry is populated before the palette mounts.
import '../../lib/commands/starters'
import { readLastPage } from '../../lib/lastPage'
import { subscribeToOpenNewPage } from '../../lib/newPageEvent'
import { subscribeToOpenNewSpace } from '../../lib/newSpaceEvent'
import { subscribeToOpenPalette } from '../../lib/paletteEvent'
import { NewPageDialog } from './NewPageDialog'
import { NewSpaceDialog } from './NewSpaceDialog'
import { readRecentPages, type RecentPage } from '../../lib/recentPages'
import { useTier1TitleHits } from '../../lib/useTier1TitleHits'
import { useTier2SearchResults } from '../../lib/useTier2SearchResults'
import { useSemanticHits } from '../../lib/useSemanticHits'
import { useCommandPaletteState } from '../../lib/useCommandPaletteState'
import { pageHitToCommandItem, navigateToPage } from '../../lib/pageHitItem'

const RECENTS_VISIBLE = 8

// Reactive view of the active theme so commands that read currentTheme always
// see the freshest value. Subscribes to setTheme() broadcasts.
function useThemeName(): ThemeName {
  const [theme, setLocal] = useState<ThemeName>(() => getTheme())
  useEffect(() => subscribeToTheme(setLocal), [])
  return theme
}

// Read current route context imperatively. AppCommandHost lives outside the
// RouterProvider, so it can't use useParams; instead it reaches into the
// router instance at the moment the dialog opens. The deepest match wins:
//   /spaces/$spaceId/pages/$pageId  -> {spaceId, pageId}
//   /spaces/$spaceId               -> {spaceId, pageId: null}
//   /                              -> {spaceId: null, pageId: null}
function readRouteContext(): {
  spaceId: number | null
  pageId: number | null
} {
  let spaceId: number | null = null
  let pageId: number | null = null
  for (const m of router.state.matches) {
    const p = m.params as { spaceId?: number; pageId?: number }
    if (typeof p.spaceId === 'number') spaceId = p.spaceId
    if (typeof p.pageId === 'number') pageId = p.pageId
  }
  return { spaceId, pageId }
}

// App-level palette mount. Orchestrates:
//  - palette UI state + keyboard contract (via useCommandPaletteState +
//    usePaletteShortcuts)
//  - tier-1 client title hits (via useTier1TitleHits) and tier-2 server
//    search (via useTier2SearchResults), then dedupes tier-2 against tier-1
//  - recently-viewed snapshot for the empty-state list
//  - CommandContext + command-registry materialization
//  - M4.2 new-page dialog bridge (Cmd-N + sidebar "+ New page" + command)
//
// Sits outside RouterProvider in App.tsx (sibling to it), so navigation goes
// through the imported `router` instance rather than useNavigate. Lives inside
// QueryClientProvider so useSpaces() / useQuery resolve from the shared cache.
export function AppCommandHost() {
  const {
    open,
    initialMode,
    subPicker,
    searchRequest,
    trimmedQuery,
    debouncedQuery,
    setPagesQuery,
    handleOpenChange,
    openWith,
    setSubPicker,
    pushSearchRequest,
    close,
  } = useCommandPaletteState()

  const titleHits = useTier1TitleHits(trimmedQuery, open)
  const searchResults = useTier2SearchResults(trimmedQuery)
  const bodyHits = useTier3BodyHits(debouncedQuery.trim(), open)
  // Semantic "Smart results" tier — debounced, server-embedded; streams in
  // after the instant tiers. No-ops (returns undefined) when the instance has
  // no embedder configured. See useSemanticHits.
  const semanticHits = useSemanticHits(debouncedQuery.trim(), open)

  // Snapshot recently-viewed pages each time the palette opens so a navigation
  // away and back surfaces the freshly-visited page without remounting the
  // palette. Window 'storage' events would catch cross-tab writes but those
  // aren't a real scenario for single-user v0.
  const [recents, setRecents] = useState<RecentPage[]>([])
  useEffect(() => {
    // Snapshot recents on open — correct-by-design effect-driven setState
    // (memory.md "set-state-in-effect snapshot-on-open pattern").
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (open) setRecents(readRecentPages().slice(0, RECENTS_VISIBLE))
  }, [open])

  // New-page dialog state + pre-fill defaults snapshotted at open time so
  // route changes after open don't retroactively re-target the dialog.
  const [newPageOpen, setNewPageOpen] = useState(false)
  const [newPageDefaults, setNewPageDefaults] = useState<{
    spaceId: number | null
    parentId: number | null
    title: string
  }>({ spaceId: null, parentId: null, title: '' })
  const [newSpaceOpen, setNewSpaceOpen] = useState(false)

  const currentTheme = useThemeName()
  const spacesQuery = useSpaces()
  const spaces = spacesQuery.data ?? []

  // M10.1 — first-palette-open-per-session: dynamic-import the body-index
  // module and kick off per-space version checks against
  // /api/spaces/{id}/index-version. The kickoff helper guards itself via a
  // module-scoped boolean, so this effect is safe to fire on every palette
  // open. Depending on `spacesQuery.data` (rather than the `?? []`-stabilised
  // `spaces`) keeps the dep array referentially stable across renders.
  const spacesData = spacesQuery.data
  useEffect(() => {
    if (!open || !spacesData || spacesData.length === 0) return
    void import('../../lib/search/body-index').then((m) => {
      m.kickoffPaletteVersionCheck(spacesData)
    })
  }, [open, spacesData])

  const openNewPage = useCallback(
    (opts?: { prefillTitle?: string }) => {
      const ctx = readRouteContext()
      let seedSpace = ctx.spaceId
      if (seedSpace == null) {
        // At app root — fall back to the last-viewed space, then the first
        // available. Resolved against the loaded spaces list when applicable.
        const last = readLastPage()
        if (last && spaces.some((s) => s.id === last.spaceId)) {
          seedSpace = last.spaceId
        } else {
          seedSpace = spaces[0]?.id ?? null
        }
      }
      // Parent only pre-fills when we're standing on an actual page; never
      // carry a parent into the dialog from somewhere other than a page view.
      const seedParent = ctx.spaceId != null ? ctx.pageId : null
      setNewPageDefaults({
        spaceId: seedSpace,
        parentId: seedParent,
        title: opts?.prefillTitle ?? '',
      })
      setNewPageOpen(true)
    },
    [spaces],
  )

  // Sidebar "+ New page" button and the broken-wikilink click handler both
  // dispatch tela:open-new-page (with an optional `{ prefillTitle }`) to ask
  // the host to open the dialog. Window event keeps the bridge simple across
  // the RouterProvider boundary.
  useEffect(
    () => subscribeToOpenNewPage((opts) => openNewPage(opts)),
    [openNewPage],
  )

  // "New space" command (palette), the home dashboard, and the sidebar empty
  // state all dispatch tela:open-new-space to open the single global dialog.
  useEffect(() => subscribeToOpenNewSpace(() => setNewSpaceOpen(true)), [])

  // Visible "Search" button (sidebar) → open the palette, same as ⌘K.
  useEffect(() => subscribeToOpenPalette((mode) => openWith(mode)), [openWith])

  usePaletteShortcuts({
    onOpenPages: () => openWith('pages'),
    onOpenCommands: () => openWith('commands'),
    onNewPage: () => openNewPage(),
  })

  const ctx = useMemo<CommandContext>(
    () => ({
      currentTheme,
      setTheme,
      spaces,
      navigateToSpace: (spaceId) => {
        void router.navigate({
          to: '/spaces/$spaceId',
          params: { spaceId },
        })
      },
      openHelpMode: () => {
        // Switch the open palette into help mode without closing it.
        setSubPicker(null)
        pushSearchRequest(prefixForMode('help'))
      },
      openSubPicker: (spec: SubPickerSpec) => {
        setSubPicker({
          label: spec.label,
          placeholder: spec.placeholder,
          emptyMessage: spec.emptyMessage,
          items: spec.items,
        })
      },
      closePalette: close,
    }),
    [currentTheme, spaces, setSubPicker, pushSearchRequest, close],
  )

  // Recompute commandsItems whenever ctx changes so onSelect closures bind
  // the latest theme / spaces snapshot.
  const commandsItems = useMemo<CommandItem[]>(
    () => materializeCommands(ctx),
    [ctx],
  )

  const recentsItems = useMemo<CommandItem[]>(
    () =>
      recents.map((r) => ({
        id: `recent:${r.pageId}`,
        title: r.title || 'Untitled',
        icon: <Clock aria-hidden width={14} height={14} />,
        onSelect: () => navigateToPage(r.spaceId, r.pageId),
      })),
    [recents],
  )

  // space_id → name, so title/FTS hits can carry their space on the breadcrumb
  // line without a per-row lookup. Rebuilt only when the spaces list changes.
  const spaceNameById = useMemo(() => {
    const m = new Map<number, string>()
    for (const s of spaces) m.set(s.id, s.name)
    return m
  }, [spaces])

  const titleItems = useMemo<CommandItem[]>(
    () =>
      titleHits.map((h) =>
        pageHitToCommandItem(h, {
          idPrefix: 'page-t1',
          spaceName: spaceNameById.get(h.spaceId),
        }),
      ),
    [titleHits, spaceNameById],
  )

  const searchItems = useMemo<CommandItem[]>(() => {
    if (!searchResults) return []
    // Drop server hits whose page already appears in tier-1 — tier-1 wins,
    // since a title-match is a stronger signal than a body-snippet match for
    // the common "find me the page by name" flow.
    const tier1Ids = new Set(titleHits.map((h) => h.pageId))
    return searchResults
      .filter((r) => !tier1Ids.has(r.page_id))
      .map((r) =>
        pageHitToCommandItem(
          {
            pageId: r.page_id,
            spaceId: r.space_id,
            title: r.title,
            breadcrumb: r.breadcrumb,
            isPublic: r.public,
            publicPath: r.public_path,
          },
          {
            idPrefix: 'page-t2',
            snippet: r.snippet,
            // spaceNameById only covers the user's OWN spaces, so a published
            // space they aren't in has no name here — label it rather than
            // rendering a bare parent chain that reads like an own page.
            spaceName: r.public
              ? 'Published'
              : spaceNameById.get(r.space_id),
          },
        ),
      )
  }, [searchResults, titleHits, spaceNameById])

  // Tier-3 body-fuzzy hits. Dedupe against tier-1 AND tier-2 (a body row whose
  // page is already surfaced as a title or FTS5 hit is noise). Excerpt is
  // plain text — no <mark>, no HTML — generated via bodyExcerpt around the
  // first match. Breadcrumb is the space name (parent-chain reconstruction
  // would require a per-row pageKeys.detail fetch, killing perceived perf;
  // deferred from v0). Uses `spacesData` (stable useQuery reference) rather
  // than the `?? []`-derived `spaces` so the memo doesn't re-run every render.
  const bodyItems = useMemo<CommandItem[]>(() => {
    if (bodyHits.length === 0) return []
    const tier1Ids = new Set(titleHits.map((h) => h.pageId))
    const tier2Ids = new Set((searchResults ?? []).map((r) => r.page_id))
    const items: CommandItem[] = []
    for (const hit of bodyHits) {
      if (tier1Ids.has(hit.id) || tier2Ids.has(hit.id)) continue
      const spaceName =
        spacesData?.find((s) => s.id === hit.space_id)?.name ?? ''
      const excerpt = bodyExcerpt(hit.body, trimmedQuery)
      items.push({
        id: `page-t3:${hit.id}`,
        title: hit.title || 'Untitled',
        subtitle: excerpt ? <span>{excerpt}</span> : undefined,
        breadcrumb: spaceName || undefined,
        icon: <Search aria-hidden width={14} height={14} />,
        onSelect: () => navigateToPage(hit.space_id, hit.id),
      })
    }
    if (trimmedQuery.length > 0) {
      items.push({
        id: `search-all:${trimmedQuery}`,
        title: `See all results for "${trimmedQuery}" in /search`,
        keywords: [trimmedQuery],
        icon: <ArrowRight aria-hidden width={14} height={14} />,
        onSelect: () => {
          void router.navigate({
            to: '/search',
            search: { q: trimmedQuery },
          })
        },
      })
    }
    return items
  }, [bodyHits, titleHits, searchResults, spacesData, trimmedQuery])

  // Tier-4 semantic "Smart results": meaning-matched chunks the keyword tiers
  // missed. Chunk-level, so we dedupe to one row per page (results arrive
  // score-ordered, so the first chunk per page is its best) AND drop pages
  // already surfaced by tiers 1-3 — this section is purely the *additional*
  // discoveries meaning-search contributes. heading_path is the section
  // breadcrumb; the snippet is plain text (no <mark>).
  const smartItems = useMemo<CommandItem[]>(() => {
    if (!semanticHits || semanticHits.length === 0) return []
    const shown = new Set<number>([
      ...titleHits.map((h) => h.pageId),
      ...(searchResults ?? []).map((r) => r.page_id),
      ...bodyHits.map((h) => h.id),
    ])
    const seen = new Set<number>()
    const items: CommandItem[] = []
    for (const hit of semanticHits) {
      if (shown.has(hit.page_id) || seen.has(hit.page_id)) continue
      seen.add(hit.page_id)
      items.push({
        id: `page-sem:${hit.page_id}`,
        title: hit.title || 'Untitled',
        subtitle: hit.snippet ? <span>{hit.snippet}</span> : undefined,
        breadcrumb: hit.heading_path || undefined,
        icon: <Sparkles aria-hidden width={14} height={14} />,
        onSelect: () => navigateToPage(hit.space_id, hit.page_id),
      })
    }
    return items
  }, [semanticHits, titleHits, searchResults, bodyHits])

  const pagesItems = useMemo<PagesItems>(() => {
    if (trimmedQuery.length === 0) return recentsItems
    // Grouped form: tier-1 under "Titles" at the top, tier-2 under "All
    // pages", tier-3 under "Body matches" with a trailing "See all results"
    // navigator. Any group may be empty; we filter them out so the surviving
    // group renders unlabelled-ish (the heading still draws unless the group
    // is dropped entirely).
    const groups: CommandItemGroup[] = []
    // Smart (semantic) results lead — they surface meaning-matches the keyword
    // tiers miss. They stream in on the debounce, so the instant keyword groups
    // below settle first and Smart results slot in above them on arrival.
    if (smartItems.length > 0) {
      groups.push({ label: 'Smart results', items: smartItems })
    }
    if (titleItems.length > 0) {
      groups.push({ label: 'Titles', items: titleItems })
    }
    if (searchItems.length > 0) {
      groups.push({ label: 'All pages', items: searchItems })
    }
    if (bodyItems.length > 0) {
      groups.push({ label: 'Body matches', items: bodyItems })
    }
    return groups
  }, [trimmedQuery, recentsItems, titleItems, searchItems, bodyItems, smartItems])

  const pagesEmptyMessage = useMemo<string | undefined>(() => {
    if (trimmedQuery.length === 0) {
      return recents.length > 0
        ? undefined
        : 'Recently viewed pages will appear here. Type to search.'
    }
    // If any tier has results we don't show empty messaging even while others
    // are still in flight — the user sees something instantly.
    if (titleItems.length > 0 || bodyItems.length > 0 || smartItems.length > 0)
      return undefined
    // Loading state — only visible on the very first query for a key, since
    // keepPreviousData keeps stale data around through subsequent re-queries.
    if (searchResults == null) return 'Searching…'
    return `No pages match "${debouncedQuery}".`
  }, [
    trimmedQuery,
    debouncedQuery,
    recents.length,
    searchResults,
    titleItems.length,
    bodyItems.length,
    smartItems.length,
  ])

  return (
    <>
      <CommandPalette
        open={open}
        onOpenChange={handleOpenChange}
        initialMode={initialMode}
        pagesItems={pagesItems}
        commandsItems={commandsItems}
        subPicker={subPicker}
        searchRequest={searchRequest ?? undefined}
        pagesEmptyMessage={pagesEmptyMessage}
        onPagesQueryChange={setPagesQuery}
      />
      <NewPageDialog
        open={newPageOpen}
        onOpenChange={(next) => {
          setNewPageOpen(next)
          if (!next) {
            // Clear the pre-fill on close so the next open from a non-prefill
            // path (sidebar button, Cmd-N) doesn't surface the stale title.
            setNewPageDefaults((prev) => ({ ...prev, title: '' }))
          }
        }}
        defaultSpaceId={newPageDefaults.spaceId}
        defaultParentId={newPageDefaults.parentId}
        defaultTitle={newPageDefaults.title}
      />
      <NewSpaceDialog open={newSpaceOpen} onOpenChange={setNewSpaceOpen} />
    </>
  )
}
