import {
  Suspense,
  lazy,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from 'react'
import {
  Link,
  useLocation,
  useNavigate,
  useParams,
  useSearch,
} from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import {
  BookOpen,
  ChevronLeft,
  ChevronRight,
  FileDown,
  FileQuestion,
  FileText,
  Hash,
  History,
  Link2,
  Lock,
  MessageSquare,
  MoreHorizontal,
  Pencil,
  Plus,
  Presentation,
  Share2,
  Trash2,
  TriangleAlert,
  Users,
} from 'lucide-react'
import type { EditorView } from '@milkdown/kit/prose/view'
import { ApiError } from '../../lib/api'
import { reportClientError } from '../../lib/client-errors'
import { pushRecentPage } from '../../lib/recentPages'
import { recordPageView } from '../../lib/recordPageView'
import { buildWikilinkResolveIndex, pageSlug } from '../../lib/slug'
import type { TelaProvider } from '../../lib/collab/tela-provider'
import { captureAnchor, type CommentAnchor } from '../../lib/comments/anchor'
import { scrollAndFlashPanelThread } from '../../lib/comments/coordination'
import { useReaderCommentAnchor } from '../../lib/comments/useReaderCommentAnchor'
import { useComments } from '../../lib/comments/use-comments'
import { useMe } from '../../lib/queries/auth'
import { useRevision } from '../../lib/queries/page-revisions'
import { AttachmentStrip } from './AttachmentStrip'
import { CommentsPanel } from './CommentsPanel'
import { PageProperties } from './PageProperties'
import { SummaryTitle, pageSummary } from './SummaryHint'
import { LocalGraphCard } from './LocalGraphCard'
import { LocalGraphPanel } from './LocalGraphPanel'
import { PresenceAvatars } from './presence-avatars'
import { FavoriteStar } from './FavoriteStar'
import { FollowButton } from './FollowButton'
import { WikilinkHoverPreview } from './wikilink-hover-preview'

// ShareManagerSheet only opens when an editor+ clicks the header Share button;
// its Sheet + create-form + management hooks + Lucide icons are ~15 KB raw that
// shouldn't ride in the main entry chunk on every paint. Split into its own
// lazy chunk (loaded on first Share click).
const ShareManagerSheet = lazy(() =>
  import('./ShareManagerSheet').then((m) => ({ default: m.ShareManagerSheet })),
)

// Full-bleed reader (?view=read). Lazy so ReaderShell + its TOC/scroll-spy/
// print-stylesheet machinery stay off the main entry chunk until a reader opens
// it — same split the retired /read route used to give us.
const PageReader = lazy(() =>
  import('./PageReader').then((m) => ({ default: m.PageReader })),
)
// A deck Presents as the live Slidev SPA, opened in a new tab (real presenter,
// overview, drawing). Same-origin → the session cookie carries RBAC.
const openDeckPresent = (pageId: number) =>
  window.open(`/api/pages/${pageId}/deck/spa/`, '_blank', 'noopener')
const DeckOverview = lazy(() =>
  import('./DeckOverview').then((m) => ({ default: m.DeckOverview })),
)
// The sheet grid pulls in @defterjs/* (grid + formula engine) — lazy so it only
// loads for sheet pages, never on the common doc path.
const GridEditor = lazy(() =>
  import('./grid-editor').then((m) => ({ default: m.GridEditor })),
)
// A read-only grid never emits body changes; a stable no-op keeps GridEditor's
// onChange required (edit paths always pass a real handler).
const noopBodyChange = () => {}
const DeckEditorOutline = lazy(() =>
  import('./DeckEditorOutline').then((m) => ({ default: m.DeckEditorOutline })),
)
const PageLintNotice = lazy(() =>
  import('./PageLintNotice').then((m) => ({ default: m.PageLintNotice })),
)
import {
  prefetchPage,
  useAllPages,
  useCreatePage,
  useDeletePage,
  usePage,
  usePages,
  useUpdatePage,
} from '../../lib/queries/pages'
import { useSpace, useSpaceRole } from '../../lib/queries/spaces'
import type { Page, PageTreeNode } from '../../lib/types'
import { Button } from '../ui/button'
import { EmptyState } from '../ui/empty-state'
import {
  Card,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from '../ui/card'
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '../ui/dialog'
import { SaveIndicator, type SaveStatus } from '../ui/save-indicator'
import { VisibilityBadge } from '../ui/visibility-badge'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '../ui/dropdown-menu'
import { cn } from '../../lib/utils'
import { BacklinksSection } from './BacklinksSection'
import { RelatedPagesSection } from './RelatedPagesSection'
import { PageTrustStrip } from './PageTrustStrip'
import { MarkdownView } from '../view/MarkdownView'
import { internalRoutePath } from '../../lib/markdown/link-target'
import { scrollToHashIn } from '../../lib/markdown/heading-anchors'
import { SheetExportMenu } from './sheet-export-menu'
import { prefetchMilkdownEditor } from '../../lib/prefetchEditor'
import { useFileDownload } from './use-file-download'
import { toast, updateToast } from '../ui/toast'

// Milkdown is the largest dependency in the app (~700 KB raw). Lazy-load it so
// non-editor routes (sidebar, spaces list, command palette) don't pay for it
// on first paint. M13.3b — the editor also owns the Excalidraw Edit Sheet
// lazy chunk internally, so opening a diagram never lands a byte in main.
const MilkdownEditor = lazy(() =>
  import('./milkdown-editor').then((m) => ({ default: m.MilkdownEditor })),
)

const EDITOR_MIN_H = 'min-h-[calc(var(--space-8)*8)]'

// Shared by the read and edit headers so the two stay pixel-identical across
// the edit toggle. `sticky` is load-bearing on touch: the app shell releases to
// document scroll on coarse pointers (see router.tsx), so an ordinary header
// scrolls away and takes Edit / Done with it — unreachable without scrolling
// all the way back up. On desktop the flex shell already pins it and sticky is
// a no-op. The opaque background is what makes it safe to overlap content.
const PAGE_HEADER_CLASS = cn(
  'sticky top-0 z-20 shrink-0 flex items-center justify-between',
  'gap-[var(--space-2)] md:gap-[var(--space-4)]',
  'px-[var(--space-4)] md:px-[var(--space-6)] py-[var(--space-2)] md:py-[var(--space-3)]',
  'border-b border-[var(--border-subtle)] bg-[var(--surface-1)]',
)

// The reading column's own gutter. 2rem a side is 16% of a 390px phone, so it
// tightens to 1rem below sm and the prose gets its measure back.
const PAGE_CONTENT_CLASS =
  'flex flex-col gap-[var(--space-4)] p-[var(--space-4)] sm:p-[var(--space-7)] max-w-[56rem] w-full mx-auto'

function EditorFallback() {
  return (
    <div
      role="status"
      aria-busy="true"
      aria-label="Loading editor"
      className={cn(
        EDITOR_MIN_H,
        'p-[var(--space-2)]',
        'rounded-[var(--radius-sm)]',
        'bg-[var(--surface-2)]',
      )}
    />
  )
}

const SAVED_FLASH_MS = 1500
const BODY_DEBOUNCE_MS = 500
const RETRY_DELAY_MS = 1500

// Persisted preference: "show resolved comments" on/off. Stored as a single
// app-wide flag rather than per-page since reviewers usually want a stable
// global default ("I always want to see resolved threads" vs "I never do").
const SHOW_RESOLVED_STORAGE_KEY = 'tela:comments:show-resolved'

interface PageViewProps {
  spaceId: number
  pageId: number
}

export function PageView({ spaceId, pageId }: PageViewProps) {
  const page = usePage(pageId)
  const navigate = useNavigate()
  // Cache read (the shell already fetched it) — only used to name the signed-in
  // account in the no-access state, where "wrong account" is the usual cause.
  const me = useMe()
  // M9.3 — soft-draft mode is opted into via `?draft=$revId`. The param is
  // typed by the route's validateSearch in router.tsx.
  const { draft: draftRevId, edit: editParam, view } = useSearch({
    from: '/_app/spaces/$spaceId/pages/$pageId/{-$slug}',
  })
  const { slug: currentSlug } = useParams({
    from: '/_app/spaces/$spaceId/pages/$pageId/{-$slug}',
  })

  // Confluence-style canonical URL: keep the address bar at /pages/{id}/{slug}
  // (or bare when the title yields no slug), refreshed on rename. This is a
  // param change on the SAME route, so it never remounts the editor.
  const persistedTitle = page.data?.title ?? ''
  const inThisSpace = page.data?.space_id === spaceId
  useEffect(() => {
    if (!page.data || !inThisSpace) return
    const desired = pageSlug(persistedTitle)
    if (desired === (currentSlug ?? '')) return
    void navigate({
      to: '/spaces/$spaceId/pages/$pageId/{-$slug}',
      params: { spaceId, pageId, slug: desired || undefined },
      replace: true,
      search: (prev) => prev,
    })
  }, [page.data, inThisSpace, persistedTitle, currentSlug, spaceId, pageId, navigate])

  // Log a page view once the page has actually loaded — keyed on the resolved id
  // so a title edit (or read/edit toggling) doesn't re-fire. Covers read, edit,
  // and viewer paths below; hover-prefetch never mounts PageView, so it doesn't
  // over-count. Fire-and-forget (see recordPageView).
  const loadedPageId = page.data?.id
  useEffect(() => {
    if (loadedPageId != null) recordPageView(loadedPageId)
  }, [loadedPageId])

  // Pre-warm a deck's Present build when its page opens, so clicking Present opens
  // instantly instead of waiting on the cold ~5s slidev build. Fire-and-forget +
  // same-origin (cookie carries RBAC); the response is discarded — we only want
  // the server-side build cached. Covers "view then present" and re-warms decks
  // after a deploy invalidates the build cache (the server-side warm only fires on
  // writes). Correctness is independent: builds are content-keyed, so warming an
  // unchanged deck is a cheap cache hit. MUST live here with the other hooks,
  // ABOVE the early returns below — a hook after a conditional return changes the
  // hook count between the loading and loaded renders (React #310).
  const deckWarmId = page.data?.props?.deck === true ? loadedPageId : null
  useEffect(() => {
    if (deckWarmId == null) return
    void fetch(`/api/pages/${deckWarmId}/deck/spa/`, {
      credentials: 'same-origin',
    }).catch(() => {})
  }, [deckWarmId])

  // Shared scroll offset so toggling view↔edit lands you where you were rather
  // than jumping to the top. PageView doesn't remount on the toggle (only the
  // ?edit param flips), so a ref here survives the PageViewer↔PageEditor swap.
  // Both the view and the editor scroll an inner column with the header as a
  // fixed sibling above it (identical geometry), so the offset is interchangeable
  // — one ref suffices. The stored pageId lets the consumers ignore a stale offset
  // after navigating to a different page (fresh page always opens at the top).
  const scrollRef = useRef({ pageId, top: 0 })

  // Drop the stored offset when we leave for a different page. PageView owns
  // the ref and does NOT remount on navigation, so the offset outlived the swap
  // it was written for: a page you once hit Edit on halfway down then reopened
  // halfway down every later time you clicked it in the sidebar. Keyed on
  // pageId — the edit↔read toggle doesn't change it, so the handoff survives,
  // and clearing is idempotent (unlike consuming it on read, which StrictMode's
  // double-invoked effects would swallow before the other view could read it).
  useEffect(() => {
    return () => {
      scrollRef.current = { pageId: -1, top: 0 }
    }
  }, [pageId])

  // A denial on PUBLISHED content isn't a denial at all — the backend hands
  // back where the same page reads without an account (code=forbidden_public),
  // so follow it instead of walling the user off. `replace` keeps Back going to
  // wherever they came from rather than bouncing through the dead URL again.
  // Covers every way an authed link travels: a pasted address-bar URL, an old
  // link, a semantic-search hit — none of which know the space is public.
  // MUST stay above the early returns (hook-count stability, React #310).
  const pageError = page.error instanceof ApiError ? page.error : null
  // Navigated as the server's path verbatim — the canonical public URL shape is
  // the backend's to decide (it alone knows the owning handle), and it HAS
  // changed: this used to parse /public/spaces/{id}/pages/{id} into typed route
  // params, which silently stopped matching the day the canonical form became
  // /{handle}/{space-slug}/{id}/{slug} and quietly restored the 403 wall. Only
  // a same-origin absolute path is followed, so the value can't become an
  // off-site redirect if the field is ever wrong.
  const publicPath =
    pageError?.publicPath?.startsWith('/') &&
    !pageError.publicPath.startsWith('//')
      ? pageError.publicPath
      : null
  useEffect(() => {
    if (publicPath) void navigate({ to: publicPath, replace: true })
  }, [publicPath, navigate])

  // queryClient's reporting policy drops every non-401 4xx as "handled by the
  // UI" — right in general, but a 403 here is NOT handled: it's a dead end the
  // user can't act on, and it stayed invisible while a real user sat on it.
  // Reported from the one place that knows it terminated in an error state.
  // The server fingerprint normalizes digits, so every page folds into one issue.
  // forbidden_public is excluded — it self-heals into a redirect, so reporting
  // it would file a bug on the fix working.
  const forbiddenPageId =
    page.isError && pageError?.status === 403 && !publicPath ? pageId : null
  useEffect(() => {
    if (forbiddenPageId == null) return
    reportClientError({
      kind: 'query',
      message: `403 forbidden reading page ${forbiddenPageId}`,
      component: 'PageView',
      pageId: forbiddenPageId,
    })
  }, [forbiddenPageId])

  // 403 / 404 / wrong-space handling
  if (page.isError) {
    const status = pageError?.status ?? null
    if (status === 404) return <PageNotFound spaceId={spaceId} />
    // Redirect to the public reader is in flight — don't flash the wall.
    if (publicPath) return <PageLoading />
    // A permission denial is not a server failure: saying "something went wrong,
    // try again" sent a locked-out user chasing a non-existent outage, and Retry
    // can never succeed.
    if (status === 403) return <PageForbidden username={me.data?.username} />
    return <PageError onRetry={() => void page.refetch()} />
  }

  if (page.isLoading || !page.data) return <PageLoading />

  if (page.data.space_id !== spaceId) {
    // Page exists but in a different space — treat as not-found for this URL.
    return <PageNotFound spaceId={spaceId} />
  }

  const onDeleted = () =>
    void navigate({ to: '/spaces/$spaceId', params: { spaceId } })

  // A deck (props.deck) is just a doc whose body happens to be Slidev markdown:
  // it reuses the SAME read view (PageViewer) and edit flow (PageEditor) as any
  // page — the editor only drops to a plain-markdown surface so the rich editor
  // can't mangle Slidev syntax (see PageEditor's isDeck branch). The one
  // deck-specific view is PRESENT: ?view=read renders the body as full-screen
  // slides (via the deck sidecar) instead of the prose reader.
  const isDeck = page.data.props?.deck === true
  // A sheet (props.sheet) is a doc whose body is Defter markdown, rendered as a
  // live spreadsheet grid: read view shows it read-only, edit mounts the collab
  // GridEditor (see PageEditor/PageViewer isSheet branches). Like a deck it never
  // uses the prose reader overlay.
  const isSheet = page.data.props?.sheet === true

  // Confluence-style: read view is the default; the editor mounts only on an
  // explicit Edit (?edit=1) or when restoring a draft. This is what keeps
  // reading instant — no editor chunk, no Yjs/collab, no /yjs round-trip — and
  // stops a reader from being one keystroke into editing. Entering edit mounts
  // PageEditor (collab spins up); leaving unmounts it (collab tears down).
  // Precedence: edit wins over ?view=read — you can't be reading and editing at
  // once, and an explicit Edit intent shouldn't be swallowed by a stale ?view.
  if (editParam || draftRevId != null) {
    return (
      <PageEditor
        key={page.data.id}
        page={page.data}
        spaceId={spaceId}
        draftRevId={draftRevId ?? null}
        onDeleted={onDeleted}
        isDeck={isDeck}
        isSheet={isSheet}
        scrollRef={scrollRef}
      />
    )
  }
  // ?view=read — full-bleed distraction-free prose reader overlay above the app
  // shell (which stays mounted underneath, so closing is instant). Docs only:
  // a deck Presents as the live Slidev SPA in a new tab (see Present button), so
  // it never uses this overlay.
  if (view === 'read' && !isDeck && !isSheet) {
    return (
      // A fixed overlay can't hand its overflow to the document, so on touch —
      // where the reader releases its own inner scroll frame (reader.css) — this
      // box has to be the scroller or nothing scrolls at all.
      <div className="fixed inset-0 z-50 bg-[var(--surface-1)] pointer-coarse:overflow-y-auto pointer-coarse:overflow-x-clip pointer-coarse:overscroll-contain">
        <Suspense fallback={null}>
          <PageReader spaceId={spaceId} pageId={page.data.id} />
        </Suspense>
      </div>
    )
  }
  return (
    <PageViewer
      key={page.data.id}
      page={page.data}
      spaceId={spaceId}
      onDeleted={onDeleted}
      isDeck={isDeck}
      isSheet={isSheet}
      scrollRef={scrollRef}
    />
  )
}

// PageViewer — the default read view (Confluence-style). Renders the page body
// through the Milkdown-free MarkdownView (no editor chunk, no Yjs/collab, no
// /yjs round-trip → instant, and a reader can't accidentally edit). The header
// reuses the same chrome as the editor minus the edit-only bits, plus an Edit
// button (role-gated) that flips ?edit=1 to mount PageEditor in place.
function PageViewer({
  page,
  spaceId,
  onDeleted,
  isDeck,
  isSheet,
  scrollRef,
}: {
  page: Page
  spaceId: number
  onDeleted: () => void
  isDeck: boolean
  isSheet: boolean
  scrollRef: React.MutableRefObject<{ pageId: number; top: number }>
}) {
  const navigate = useNavigate()
  const me = useMe()
  const { resolved: roleResolved, isViewer, isOwner: isSpaceOwner } = useSpaceRole(spaceId)
  const canEdit = roleResolved && !isViewer

  const [graphOpen, setGraphOpen] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)
  // Active sheet index (lifted from the grid) so a sheet export can scope to it.
  const [activeSheet, setActiveSheet] = useState(0)
  const [commentsOpen, setCommentsOpen] = useState(false)
  const [showResolvedComments, setShowResolvedComments] = useState(false)
  const contentRef = useRef<HTMLDivElement>(null)

  // Read + reply + new-comment-from-selection all work in view now, matching the
  // editor. The panel, inline highlights, and selection bridge are gated to
  // non-viewers, same as the editor.
  const commentsEnabled = roleResolved && !isViewer
  // Rendered-content root (from MarkdownView.onReady) that the selection bridge
  // watches, and the snapshot of the reader's current passage to anchor to.
  const [readerContentEl, setReaderContentEl] = useState<HTMLElement | null>(null)
  const { selection: readerSelection, clear: clearReaderSelection } =
    useReaderCommentAnchor(readerContentEl, commentsEnabled)
  // Deep-link hash (`#some-section`). The browser tries to jump on load, before
  // MarkdownView has rendered a thing, finds no target and never retries — so
  // the jump has to happen here, once the body is on screen. The hash comes
  // from the ROUTER, not `window.location`: on a fresh load the router
  // normalizes the URL after the first render, so reading the window at
  // onReady time sees an empty hash and the jump silently never happens.
  const locationHash = useLocation({ select: (l) => l.hash })
  const [viewContentEl, setViewContentEl] = useState<HTMLElement | null>(null)
  const handleViewReady = useCallback(
    (el: HTMLElement) => {
      setViewContentEl(el)
      if (commentsEnabled) setReaderContentEl(el)
    },
    [commentsEnabled],
  )
  useEffect(() => {
    if (viewContentEl && locationHash) scrollToHashIn(viewContentEl, locationHash)
  }, [viewContentEl, locationHash])
  const handleCommentsOpenChange = useCallback(
    (next: boolean) => {
      setCommentsOpen(next)
      // Drop the snapshot on close so the next session starts from a fresh
      // selection instead of a stale "Commenting on …" preview.
      if (!next) clearReaderSelection()
    },
    [clearReaderSelection],
  )
  const commentsQuery = useComments({ pageId: page.id, enabled: commentsEnabled })
  const openThreadCount = useMemo(
    () => commentsQuery.data?.filter((t) => !t.root.resolved).length ?? null,
    [commentsQuery.data],
  )
  const commentThreads = commentsQuery.data ?? null

  // The two side panels are mutually exclusive. Named so the header bar and the
  // compact "•••" menu can share one handler instead of duplicating the pairing.
  const openComments = useCallback(() => {
    setGraphOpen(false)
    setCommentsOpen(true)
  }, [])
  const openGraph = useCallback(() => {
    handleCommentsOpenChange(false)
    setGraphOpen(true)
  }, [handleCommentsOpenChange])
  const openReadMode = useCallback(() => {
    if (isDeck) {
      openDeckPresent(page.id)
      return
    }
    void navigate({
      to: '/spaces/$spaceId/pages/$pageId/{-$slug}',
      params: { spaceId, pageId: page.id, slug: pageSlug(page.title) || undefined },
      search: (prev) => ({ ...prev, view: 'read' }),
    })
  }, [isDeck, navigate, spaceId, page.id, page.title])

  // Wikilink resolution (slug → id), space-scoped, like the editor builds it.
  const allPagesQuery = useAllPages()
  const resolveIndex = useMemo(
    () =>
      allPagesQuery.data
        ? buildWikilinkResolveIndex(
            allPagesQuery.data.filter((p) => p.space_id === page.space_id),
          )
        : null,
    [allPagesQuery.data, page.space_id],
  )
  // Only hand MarkdownView a resolver once the page index has loaded — until
  // then it's `undefined`, so wikilinks render as neutral styled spans rather
  // than flashing "broken" red before resolution (mirrors the editor's
  // alive-ids-null behaviour).
  const resolveWikilink = useMemo(
    () =>
      resolveIndex
        ? (slug: string) => resolveIndex.get(slug) ?? null
        : undefined,
    [resolveIndex],
  )
  const pageHref = useCallback(
    (id: number) => `/spaces/${spaceId}/pages/${id}`,
    [spaceId],
  )

  useEffect(() => {
    pushRecentPage({
      pageId: page.id,
      spaceId,
      title: page.title,
      viewedAt: Date.now(),
    })
  }, [spaceId, page.id, page.title])

  // Restore the shared scroll offset on mount (0 on a fresh page, or the last
  // edit-scroll position when returning from edit — a stale offset from a
  // different page is ignored). MarkdownView renders synchronously, so the
  // content height is available by layout time.
  useLayoutEffect(() => {
    const el = contentRef.current
    if (el)
      el.scrollTop =
        scrollRef.current.pageId === page.id ? scrollRef.current.top : 0
    // Once per mount — the scroller is fresh (keyed on page.id upstream).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const enterEdit = useCallback(() => {
    // Capture the read-scroll offset at click time (deterministic — before the
    // PageViewer↔PageEditor swap) so the editor opens at the same spot. Doing
    // this on the transition rather than via a continuous onScroll avoids a
    // teardown scroll event clobbering the value to 0 mid-swap.
    scrollRef.current = { pageId: page.id, top: contentRef.current?.scrollTop ?? 0 }
    void navigate({
      to: '/spaces/$spaceId/pages/$pageId/{-$slug}',
      params: { spaceId, pageId: page.id, slug: pageSlug(page.title) || undefined },
      search: (prev) => ({ ...prev, edit: true }),
    })
  }, [navigate, spaceId, page.id, page.title, scrollRef])

  // SPA-navigate internal page links (MarkdownView emits plain <a href>);
  // without this they'd full-reload the whole app. This covers wikilinks AND
  // ordinary markdown links to another tela page — the latter used to reload,
  // since only the tela:// wikilink form was ever intercepted.
  //
  // Everything else falls through to the browser on purpose: external links
  // (which now carry target=_blank), #anchors, and server-owned paths like
  // /api/ attachment downloads — see internalRoutePath. Modifier and middle
  // clicks are left alone so "open in new tab" keeps working.
  const onContentClick = useCallback(
    (e: React.MouseEvent) => {
      if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
      if (e.button !== 0) return
      const a = (e.target as HTMLElement).closest('a[href]') as HTMLAnchorElement | null
      if (!a || a.target === '_blank') return
      const to = internalRoutePath(a.getAttribute('href') ?? '')
      if (!to) return
      e.preventDefault()
      void navigate({ to })
    },
    [navigate],
  )

  const summary = pageSummary(page.props)

  return (
    <div className="flex-1 flex flex-col min-h-0">
      <header className={PAGE_HEADER_CLASS}>
        <Breadcrumb spaceId={spaceId} pageId={page.id} />
        {/* Secondary actions live here on desktop and fold into the "•••" menu
            below md — a phone can't show nine controls, and the old
            overflow-x-auto row silently pushed Edit off-screen entirely. */}
        <div className="flex items-center gap-[var(--space-1)] md:gap-[var(--space-3)] shrink-0">
          {/* Properties stays on the bar at every width: it self-hides unless the
              page actually has frontmatter, so it costs nothing on the pages
              that don't have any, and it has no sensible menu-row form. */}
          <PageProperties props={page.props} />
          <div className="hidden md:flex items-center gap-[var(--space-3)]">
            {roleResolved ? <FavoriteStar pageId={page.id} /> : null}
            {roleResolved ? <FollowButton id={page.id} /> : null}
            {commentsEnabled ? (
              <Button
                type="button"
                variant="ghost"
                size="sm"
                aria-label="Comments"
                onClick={openComments}
                className="h-[var(--space-8)] px-[var(--space-3)]"
              >
                <MessageSquare width={16} height={16} />
                {openThreadCount != null ? (
                  <span>Comments ({openThreadCount})</span>
                ) : (
                  <span>Comments</span>
                )}
              </Button>
            ) : null}
            {roleResolved ? (
              <Button
                type="button"
                variant="ghost"
                size="sm"
                aria-label="Graph"
                title="Graph — this page's connections"
                onClick={openGraph}
                className="h-[var(--space-8)] w-[var(--space-8)] p-0"
              >
                <Share2 width={16} height={16} />
              </Button>
            ) : null}
            {roleResolved ? (
              <VisibilityBadge
                state={page.exposure?.state ?? 'private'}
                inherited={page.exposure?.inherited ?? false}
              />
            ) : null}
          </div>
          {/* Sheets have no read mode; their export menu stays on the bar. */}
          {isSheet ? (
            <SheetExportMenu body={page.body} title={page.title} activeSheet={activeSheet} />
          ) : null}
          {/* Sheets have no reading view (the ?view=read route excludes them), so
              don't offer a button that lands back on the same screen. */}
          {roleResolved && !isSheet ? (
            <Button
              type="button"
              variant={isDeck ? 'primary' : 'ghost'}
              size="sm"
              aria-label={isDeck ? 'Present' : 'Read mode'}
              title={
                isDeck
                  ? 'Present — full-screen slides'
                  : 'Read mode — distraction-free reading view'
              }
              onClick={openReadMode}
              // A phone bar should carry one labelled action, not two: an
              // unlabelled book icon next to Edit is exactly the guesswork this
              // sweep is removing, so Read mode drops into "•••" below md. A
              // deck's Present is its primary action and stays, labelled.
              className={cn(
                'h-[var(--space-8)] px-[var(--space-3)]',
                !isDeck && 'hidden md:inline-flex',
              )}
            >
              {isDeck ? (
                <Presentation width={16} height={16} />
              ) : (
                <BookOpen width={16} height={16} />
              )}
              <span>{isDeck ? 'Present' : 'Read mode'}</span>
            </Button>
          ) : null}
          {canEdit ? (
            <Button
              type="button"
              variant="primary"
              size="sm"
              onClick={enterEdit}
              // Warm the editor chunk on Edit intent so entering edit is instant
              // — reads themselves never load it.
              onMouseEnter={prefetchMilkdownEditor}
              onFocus={prefetchMilkdownEditor}
              className="h-[var(--space-8)] px-[var(--space-3)]"
            >
              <Pencil width={16} height={16} />
              <span>Edit</span>
            </Button>
          ) : null}
          {roleResolved ? (
            <PageActionsMenu
              spaceId={spaceId}
              pageId={page.id}
              title={page.title}
              isViewer={isViewer}
              onDelete={() => setDeleteOpen(true)}
              compactActions={
                <>
                  {!isDeck && !isSheet ? (
                    <DropdownMenuItem onSelect={openReadMode}>
                      <BookOpen width={14} height={14} /> Read mode
                    </DropdownMenuItem>
                  ) : null}
                  {commentsEnabled ? (
                    <DropdownMenuItem onSelect={openComments}>
                      <MessageSquare width={14} height={14} />
                      {openThreadCount != null
                        ? `Comments (${openThreadCount})`
                        : 'Comments'}
                    </DropdownMenuItem>
                  ) : null}
                  <DropdownMenuItem onSelect={openGraph}>
                    <Share2 width={14} height={14} /> Page connections
                  </DropdownMenuItem>
                  <FavoriteStar pageId={page.id} asMenuItem />
                  <FollowButton id={page.id} asMenuItem />
                </>
              }
            />
          ) : null}
        </div>
      </header>

      {isSheet ? (
        // First-class sheet surface: the grid owns the full width + height of the
        // content area (no reading column, no doc furniture, no padding) and
        // scrolls internally — a spreadsheet app, edge-to-edge, not a card. Title
        // lives in the header breadcrumb. Edit flips to the collaborative grid.
        <div className="flex-1 min-h-0 flex flex-col">
          <Suspense fallback={<div className="flex-1" />}>
            <GridEditor
              defaultValue={page.body}
              onChange={noopBodyChange}
              collabPageId={null}
              readOnly
              pageId={page.id}
              ariaLabel="Spreadsheet (read-only)"
              onSheetChange={setActiveSheet}
            />
          </Suspense>
        </div>
      ) : (
      /* The scroller spans the full width (so wheel over the empty gutters
          scrolls the page); the 48rem reading column is an inner wrapper. */
      <div
        ref={contentRef}
        onClick={onContentClick}
        /* overflow-x-clip keeps this a vertical-only scroller on desktop.
           pointer-coarse:overflow-visible releases it to document scroll on
           touch so a pinch-zoomed page pans; see the app shell in router.tsx. */
        className="flex-1 overflow-y-auto overflow-x-clip min-h-0 pointer-coarse:overflow-visible"
      >
        <div className={PAGE_CONTENT_CLASS}>
        <WikilinkHoverPreview containerRef={contentRef} />
        <SummaryTitle
          summary={summary}
          hintClassName="absolute top-[var(--space-3)] left-[calc(-1*(var(--space-6)+var(--space-1)))] hidden sm:inline-flex"
        >
          <h1
            className={cn(
              'm-0 px-[var(--space-2)] py-[var(--space-2)]',
              'text-[length:var(--text-3xl)] leading-[var(--leading-tight)] font-medium',
              'text-[var(--text-primary)]',
            )}
          >
            {page.title || 'Untitled page'}
          </h1>
        </SummaryTitle>

        <PageTrustStrip
          spaceId={spaceId}
          pageId={page.id}
          updatedAt={page.updated_at}
          props={page.props}
        />

        <AttachmentStrip pageId={page.id} />

        {isDeck ? (
          // A deck's default view shows its identity (outline + Present), not the
          // raw Slidev markdown as prose. Present (?view=read) renders the slides.
          <Suspense fallback={<div className={EDITOR_MIN_H} />}>
            <DeckOverview page={page} />
          </Suspense>
        ) : (
          <MarkdownView
            body={page.body}
            pageId={page.id}
            // Editors can cast poll votes here; viewers get read-only results.
            canVote={canEdit}
            resolveWikilink={resolveWikilink}
            pageHref={pageHref}
            commentThreads={commentsEnabled ? commentThreads : null}
            onCommentClick={() => {
              setGraphOpen(false)
              setCommentsOpen(true)
            }}
            // Hand the rendered root to the selection bridge so a passage the
            // reader highlights can be anchored into a new comment, and honour
            // a deep-link hash now that the headings exist.
            onReady={handleViewReady}
            className={EDITOR_MIN_H}
          />
        )}

        <ChildPagesSection
          spaceId={spaceId}
          pageId={page.id}
          bodyIsEmpty={page.body.trim().length === 0}
        />

        <BacklinksSection pageId={page.id} />

        <RelatedPagesSection pageId={page.id} spaceId={spaceId} />

        <LocalGraphCard pageId={page.id} />
        </div>
      </div>
      )}

      <DeletePageConfirmDialog
        page={page}
        spaceId={spaceId}
        open={deleteOpen}
        onOpenChange={setDeleteOpen}
        onDeleted={onDeleted}
      />

      {roleResolved ? (
        <LocalGraphPanel
          pageId={page.id}
          open={graphOpen}
          onOpenChange={setGraphOpen}
        />
      ) : null}

      {commentsEnabled && me.data ? (
        <CommentsPanel
          pageId={page.id}
          open={commentsOpen}
          onOpenChange={handleCommentsOpenChange}
          // Selecting a passage in the reader snapshots its anchor; the composer
          // then works exactly like the editor's.
          hasSelection={readerSelection != null}
          captureAnchor={() => readerSelection?.anchor ?? null}
          anchorPreview={readerSelection?.preview ?? null}
          canCompose
          me={{ id: me.data.id, username: me.data.username }}
          isSpaceOwner={isSpaceOwner}
          orphanIds={EMPTY_ORPHAN_IDS}
          showResolved={showResolvedComments}
          onShowResolvedChange={setShowResolvedComments}
        />
      ) : null}
    </div>
  )
}

const EMPTY_ORPHAN_IDS: Set<number> = new Set()

// PageActionsMenu — the header "•••" overflow. Keeps the bar to its frequent
// actions (Comments, Share) and tucks the rest here, Confluence-style. "Copy
// link" is the primary, obvious action (pretty /p/{id}/{slug}); "Copy short
// link" (bare /p/{id}) is present but deliberately demoted so people don't grab
// the opaque one by default. Links are built off window.location.origin and
// resolve via the canonical id, surviving rename.
// `compactActions` are header controls that don't fit below md — the caller
// hands them in as menu items and they show only there, so the phone bar can
// stay down to Edit + "•••" without any action becoming unreachable.
function PageActionsMenu({
  spaceId,
  pageId,
  title,
  isViewer,
  onDelete,
  compactActions,
}: {
  spaceId: number
  pageId: number
  title: string
  isViewer: boolean
  onDelete: () => void
  compactActions?: React.ReactNode
}) {
  const navigate = useNavigate()
  // /pdf is page-type aware on the backend: a deck renders to a real per-slide
  // Slidev PDF, a doc to the gotenberg-rendered reader. So this one call is
  // correct for both.
  const { download: downloadPdf } = useFileDownload(`/api/pages/${pageId}/pdf`, {
    themed: true,
  })
  const { download: downloadMd } = useFileDownload(`/api/pages/${pageId}/md`, {
    fallbackName: 'page.md',
  })
  const origin = window.location.origin
  const slug = pageSlug(title)
  const pretty = slug ? `${origin}/p/${pageId}/${slug}` : `${origin}/p/${pageId}`
  const short = `${origin}/p/${pageId}`
  const copy = (url: string) => {
    void navigator.clipboard?.writeText?.(url)
  }
  // The menu closes on select, so there's no inline spot for the busy state —
  // a deck PDF renders headless Chromium frames and can take several seconds.
  // Surface a spinner toast that morphs to the result when the download lands.
  const exportPdf = () => {
    const id = toast({ title: 'Preparing PDF…', loading: true, duration: 0 })
    void downloadPdf().then((ok) =>
      updateToast(
        id,
        ok
          ? {
              title: 'PDF ready',
              description: 'Your download has started.',
              variant: 'success',
              loading: false,
              duration: 4000,
            }
          : {
              title: 'PDF export failed',
              description: 'Please try again.',
              variant: 'destructive',
              loading: false,
              duration: 6000,
            },
      ),
    )
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          aria-label="More actions"
          className="h-[var(--space-8)] w-[var(--space-8)] p-0"
        >
          <MoreHorizontal width={16} height={16} />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-[12rem]">
        {compactActions ? (
          <div className="md:hidden">
            {compactActions}
            <DropdownMenuSeparator />
          </div>
        ) : null}
        <DropdownMenuItem onSelect={() => copy(pretty)}>
          <Link2 width={14} height={14} /> Copy link
        </DropdownMenuItem>
        <DropdownMenuItem
          onSelect={() => copy(short)}
          className="text-[var(--text-muted)]"
        >
          <Hash width={14} height={14} /> Copy short link
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem
          onSelect={() =>
            void navigate({ to: '/graph', search: { focus: pageId } })
          }
        >
          <Share2 width={14} height={14} /> Open in graph
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={exportPdf}>
          <FileDown width={14} height={14} /> Export PDF
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => void downloadMd()}>
          <FileDown width={14} height={14} /> Export Markdown
        </DropdownMenuItem>
        {!isViewer ? (
          <DropdownMenuItem
            onSelect={() =>
              void navigate({
                to: '/spaces/$spaceId/pages/$pageId/history',
                params: { spaceId, pageId },
              })
            }
          >
            <History width={14} height={14} /> Version history
          </DropdownMenuItem>
        ) : null}
        {!isViewer ? (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem destructive onSelect={onDelete}>
              <Trash2 width={14} height={14} /> Delete page
            </DropdownMenuItem>
          </>
        ) : null}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

interface PageEditorProps {
  page: Page
  spaceId: number
  draftRevId: number | null
  onDeleted: () => void
  isDeck: boolean
  isSheet: boolean
  scrollRef: React.MutableRefObject<{ pageId: number; top: number }>
}

function PageEditor({ page, spaceId, draftRevId, onDeleted, isDeck, isSheet, scrollRef }: PageEditorProps) {
  const updatePage = useUpdatePage()
  const navigate = useNavigate()
  const [title, setTitle] = useState(page.title)
  const [body, setBody] = useState(page.body)
  // Active sheet index (lifted from the grid) so a sheet export scopes to it.
  const [activeSheet, setActiveSheet] = useState(0)
  // Title is a textarea (so long titles wrap instead of clipping); keep its
  // height pinned to its content so it reads like a heading, not a box.
  const titleRef = useRef<HTMLTextAreaElement>(null)
  const fitTitleHeight = useCallback(() => {
    const el = titleRef.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${el.scrollHeight}px`
  }, [])
  useLayoutEffect(fitTitleHeight, [fitTitleHeight, title])
  const [deleteOpen, setDeleteOpen] = useState(false)
  // Container for wikilink hover-preview delegation (covers the editor body).
  const contentRef = useRef<HTMLDivElement>(null)

  // M7.2 — viewer role gating. The backend rejects ws upgrade for viewers
  // (HTTP 403 + code `viewer_no_write`). We prefer Option A from the task
  // brief: check role up-front (my_role on the already-needed space query —
  // the caller's EFFECTIVE role, so org/group-granted viewers gate the same
  // as direct ones) and skip opening a ws entirely for viewers — they render
  // the page from pages.body as a non-editable Milkdown. Editors and owners
  // get a live Yjs session via MilkdownEditor's `collabPageId` path.
  const me = useMe()
  // The role must resolve before we mount the editor — otherwise the
  // role-pending window would mount Milkdown in non-collab mode, then
  // re-mount it in collab mode once role resolves. The re-mount would
  // throw away local PM state and (worse) double-seed the Y.Doc fragment.
  // The space detail query has the global SWR staleTime so within-space
  // navigation is instant; the first page open per session has a brief
  // loading-skeleton window.
  const { resolved: roleResolved, isViewer, isOwner: isSpaceOwner } = useSpaceRole(spaceId)

  // M9.3 — soft-draft mode. Owner-only (Q30): non-owners that paste a
  // ?draft=N URL silently drop back to normal mode. We can't compute
  // `isDraftMode` until role resolves; until then, treat as not-in-draft so
  // the regular editor / viewer-empty branches behave as before. A
  // useEffect emits the one-shot console.warn for non-owner URL paste so
  // the param doesn't disappear silently from the developer's POV.
  const isDraftMode = draftRevId != null && roleResolved && isSpaceOwner
  const draftRevisionQuery = useRevision({
    pageId: page.id,
    revId: draftRevId,
    enabled: isDraftMode,
  })
  useEffect(() => {
    if (draftRevId != null && roleResolved && !isSpaceOwner) {
      console.warn(
        `tela: ?draft=${draftRevId} ignored — only space owners can open a revision as draft.`,
      )
    }
  }, [draftRevId, roleResolved, isSpaceOwner])

  // Seed the editor with the revision body + title exactly once per draft
  // entry. A ref tracks which revision we've already seeded so React's
  // strict-mode double-invoke or any unrelated re-render can't clobber
  // in-flight edits. Cleared when draft mode exits.
  const seededForRevIdRef = useRef<number | null>(null)
  useEffect(() => {
    if (!isDraftMode) {
      seededForRevIdRef.current = null
      return
    }
    if (draftRevId == null) return
    if (seededForRevIdRef.current === draftRevId) return
    const rev = draftRevisionQuery.data
    if (!rev) return
    seededForRevIdRef.current = draftRevId
    // One-shot seed per draft entry (guarded above) — correct-by-design
    // effect-driven setState (memory.md "set-state-in-effect snapshot pattern").
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setTitle(rev.title)
    setBody(rev.body)
  }, [isDraftMode, draftRevId, draftRevisionQuery.data])

  const stripDraftParam = useCallback(() => {
    void navigate({
      to: '/spaces/$spaceId/pages/$pageId/{-$slug}',
      params: { spaceId, pageId: page.id, slug: undefined },
      search: {},
      replace: true,
    })
  }, [navigate, spaceId, page.id])

  // Exit edit mode (Confluence "Done") → drop ?edit and fall back to the read
  // view. Keeps the canonical slug; no `replace` so Back returns to edit.
  const exitEdit = useCallback(() => {
    // Symmetric to enterEdit: carry the edit-scroll offset back to the read view.
    scrollRef.current = { pageId: page.id, top: contentRef.current?.scrollTop ?? 0 }
    void navigate({
      to: '/spaces/$spaceId/pages/$pageId/{-$slug}',
      params: { spaceId, pageId: page.id, slug: pageSlug(page.title) || undefined },
      search: {},
    })
  }, [navigate, spaceId, page.id, page.title, scrollRef])

  // M8.3 — comments surface. The panel toggle, panel itself, and selection
  // bridge are all gated behind a non-viewer role; viewers don't see the
  // button at all and the editor's selection bridge is unused. The editor's
  // PM view ref is owned here so the composer's captureAnchor() reads the
  // live selection at submit time without prop-drilling the view down.
  const editorViewRef = useRef<EditorView | null>(null)
  const [commentsOpen, setCommentsOpen] = useState(false)
  const [graphOpen, setGraphOpen] = useState(false)
  const [shareOpen, setShareOpen] = useState(false)
  const [selectionEmpty, setSelectionEmpty] = useState(true)
  const [selectionPreview, setSelectionPreview] = useState('')
  const handleViewReady = useCallback((view: EditorView | null) => {
    editorViewRef.current = view
  }, [])
  // The side panels are mutually exclusive; one handler each, shared by the
  // header bar and the compact "•••" menu (mirrors PageViewer).
  const openComments = useCallback(() => {
    setGraphOpen(false)
    setCommentsOpen(true)
  }, [])
  const openGraph = useCallback(() => {
    setCommentsOpen(false)
    setGraphOpen(true)
  }, [])
  const handleSelectionChange = useCallback(
    ({ isEmpty, text }: { isEmpty: boolean; text: string }) => {
      setSelectionEmpty(isEmpty)
      setSelectionPreview(text)
    },
    [],
  )
  const captureCurrentAnchor = useCallback((): CommentAnchor | null => {
    const view = editorViewRef.current
    if (!view) return null
    const { from, to } = view.state.selection
    if (from === to) return null
    try {
      return captureAnchor(view, from, to)
    } catch {
      return null
    }
  }, [])
  // Drives the "Comments (N)" badge on the header toggle, feeds the
  // CommentsPanel, and (M8.4) feeds the in-body anchor-decoration plugin.
  // Shared queryKey so all three consume the same cache; skipped entirely
  // for viewers (backend 403s them).
  const commentsForHeader = useComments({
    pageId: page.id,
    enabled: roleResolved && !isViewer,
  })
  const openThreadCount = useMemo(() => {
    const data = commentsForHeader.data
    if (!data) return null
    return data.reduce((n, t) => n + (t.root.resolved ? 0 : 1), 0)
  }, [commentsForHeader.data])

  // M8.4 — orphan-thread map. The editor's anchor-decoration plugin reports
  // each thread's resolution after every rebuild (debounced 250ms on
  // typing, immediate on thread-list change). null = no match in the
  // current text → render an "Orphaned" tag in the panel. Stored as a
  // sorted-ish array of ids in a Set for O(1) lookup per thread row.
  const [orphanIds, setOrphanIds] = useState<Set<number>>(() => new Set())
  const handleAnchorsResolved = useCallback(
    (resolutions: Map<number, { from: number; to: number } | null>) => {
      const next = new Set<number>()
      for (const [id, range] of resolutions) {
        if (range === null) next.add(id)
      }
      // Avoid setState if the new set is identical to the previous one —
      // a same-size, same-membership Set should not retrigger renders.
      setOrphanIds((prev) => {
        if (prev.size !== next.size) return next
        for (const id of next) if (!prev.has(id)) return next
        return prev
      })
    },
    [],
  )

  // M8.4 — body-click → panel-open + scroll. The decoration plugin fires
  // this when the user clicks any .tela-comment-anchor span. Opens the
  // Sheet if closed and scrolls/flashes the matching thread row. A short
  // setTimeout gives Radix one frame to mount the row's DOM before the
  // scroll attempt.
  const handleAnchorClick = useCallback((threadId: number) => {
    setCommentsOpen(true)
    window.setTimeout(() => {
      scrollAndFlashPanelThread(threadId)
    }, 50)
  }, [])

  // M8.5 — "Show resolved" filter. Lifted into PageView so the editor's
  // anchor decoration plugin and the panel's thread list stay in lockstep:
  // the body underline mutes for resolved threads only while this is true.
  // Persisted under a single localStorage key so the preference survives
  // reloads but doesn't bleed across pages (one rule for all comments).
  const [showResolvedComments, setShowResolvedComments] = useState<boolean>(
    () => {
      try {
        return localStorage.getItem(SHOW_RESOLVED_STORAGE_KEY) === '1'
      } catch {
        return false
      }
    },
  )
  const handleShowResolvedChange = useCallback((next: boolean) => {
    setShowResolvedComments(next)
    try {
      localStorage.setItem(SHOW_RESOLVED_STORAGE_KEY, next ? '1' : '0')
    } catch {
      // Private-mode / quota — fall through; UX still works for this session.
    }
  }, [])

  // Pass an empty array (not undefined) to the editor when comments are
  // enabled — see milkdown-editor.tsx commentsEnabled doc: the editor
  // builder closure captures the prop ONCE at build time, so undefined-now
  // would never upgrade to plugin-mounted when the query later returns.
  // Memoized so its identity is stable across unrelated re-renders (e.g. typing
  // the title) — otherwise a fresh [] every render defeats MilkdownEditor's
  // React.memo and the editor re-renders on each keystroke (triggering a
  // flushSync storm from the prosemirror-adapter that steals title focus).
  const commentThreadsForEditor = useMemo(
    () =>
      roleResolved && !isViewer ? (commentsForHeader.data ?? []) : undefined,
    [roleResolved, isViewer, commentsForHeader.data],
  )

  // M7.4 — collab provider, lifted into PageView so the header
  // <PresenceAvatars /> and the local-awareness user-state seeding share
  // the exact provider instance Milkdown is binding y-prosemirror against.
  // `provider` is set via the MilkdownEditor onCollabReady callback once
  // the editor mounts and the provider is constructed. It's nulled by the
  // PageEditor remount on page change (parent key=page.id) — no manual
  // teardown needed here.
  const [provider, setProvider] = useState<TelaProvider | null>(null)
  useEffect(() => {
    if (!provider) return
    if (!me.data) return
    const myId = me.data.id
    const myUsername = me.data.username
    // colorIdx = stable per-user hue index into --collab-cursor-{1..8}. id%8
    // matches the brief; deterministic across reloads so a peer's avatar /
    // cursor colour doesn't drift.
    const seedLocal = () => {
      // setLocalStateField (not setLocalState) so we MERGE the user field rather
      // than replacing the whole state — otherwise a reconnect would clobber
      // other awareness fields (e.g. the editor's editingDiagramId presence).
      provider.awareness.setLocalStateField('user', {
        id: myId,
        username: myUsername,
        colorIdx: myId % 8,
      })
    }
    if (provider.getStatus() === 'connected') seedLocal()
    return provider.onStatus((status) => {
      if (status === 'connected') seedLocal()
    })
  }, [provider, me.data])

  // The editor body scrolls its own inner column (contentRef), matching the
  // read view — so the header stays put (main never scrolls). Restore the
  // incoming offset on entry, re-pinning it every frame: the lazy editor grows
  // after mount (so a deep target is briefly out of range), and — once collab
  // syncs, which can take a beat on a live connection — its autofocus yanks the
  // view to the cursor at the top. Keep re-applying the target until the user
  // scrolls for real (wheel/touch/key → we never fight them) or a short safety
  // budget elapses. A 0 target (fresh page) just pins the top.
  useLayoutEffect(() => {
    const scroller = contentRef.current
    if (!scroller) return
    const target =
      scrollRef.current.pageId === page.id ? scrollRef.current.top : 0
    if (target <= 0) {
      scroller.scrollTop = 0
      return
    }
    let raf = 0
    let frames = 0
    let done = false
    const stop = () => {
      if (done) return
      done = true
      cancelAnimationFrame(raf)
      scroller.removeEventListener('wheel', stop)
      scroller.removeEventListener('touchmove', stop)
      scroller.removeEventListener('keydown', stop, true)
    }
    const apply = () => {
      if (done) return
      if (scroller.scrollTop !== target) scroller.scrollTop = target
      // ~3s safety net — long enough to outlast a late post-sync autofocus.
      if ((frames += 1) > 180) stop()
      else raf = requestAnimationFrame(apply)
    }
    scroller.addEventListener('wheel', stop, { passive: true })
    scroller.addEventListener('touchmove', stop, { passive: true })
    scroller.addEventListener('keydown', stop, true)
    raf = requestAnimationFrame(apply)
    return stop
    // Once per page entry — PageEditor remounts on page.id via its key.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [page.id])

  // Alive-page-ids snapshot powers M5.2d broken-wikilink rendering. `null` is
  // the "don't know yet" state — the editor's decoration plugin keeps every
  // wikilink in the alive style until the query lands, so first-paint never
  // flashes everything as broken. Set reference is memoized so unchanged data
  // doesn't keep retriggering the decoration plugin's rebuild path.
  const allPagesQuery = useAllPages()
  const allPagesData = allPagesQuery.data
  const aliveWikilinkIds = useMemo<Set<number> | null>(() => {
    if (!allPagesData) return null
    return new Set(allPagesData.map((p) => p.id))
  }, [allPagesData])

  // Slug→id map for `[[Name]]` bracket-wikilink resolution, scoped to this
  // page's space to match the backend's space-scoped backlink resolution.
  const wikilinkResolveIndex = useMemo<Map<string, number> | null>(() => {
    if (!allPagesData) return null
    return buildWikilinkResolveIndex(
      allPagesData.filter((p) => p.space_id === page.space_id),
    )
  }, [allPagesData, page.space_id])

  // Record this visit in the recently-viewed list (consumed by the M5.1
  // palette empty state). Re-fires only when the page id changes — renaming
  // the open page shouldn't bump its position in the recents list, and the
  // cached title goes stale until the user navigates to the page from
  // elsewhere. Acceptable trade-off vs. either pushing on every keystroke or
  // missing rename-after-visit updates.
  useEffect(() => {
    pushRecentPage({
      pageId: page.id,
      spaceId: page.space_id,
      title: page.title,
      viewedAt: Date.now(),
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [page.id])

  // Track last server-confirmed values so we can skip no-op saves and detect
  // dirtiness on blur.
  const lastSavedRef = useRef({ title: page.title, body: page.body })

  const [status, setStatus] = useState<SaveStatus>('idle')

  // Reset "Saved" flash back to idle after a short delay so the badge doesn't
  // linger.
  useEffect(() => {
    if (status !== 'saved') return
    const t = window.setTimeout(() => setStatus('idle'), SAVED_FLASH_MS)
    return () => window.clearTimeout(t)
  }, [status])

  // Debounce timer for body auto-save and retry timer for the one-shot 5xx
  // retry. Both are cancelled together: any fresh save attempt (or any new
  // keystroke that schedules one) supersedes a queued retry — last-write-wins.
  const debounceRef = useRef<number | null>(null)
  const retryTimerRef = useRef<number | null>(null)

  const cancelPendingSave = useCallback(() => {
    if (debounceRef.current != null) {
      window.clearTimeout(debounceRef.current)
      debounceRef.current = null
    }
    if (retryTimerRef.current != null) {
      window.clearTimeout(retryTimerRef.current)
      retryTimerRef.current = null
      // The displayed 'retrying' state is no longer accurate; reset so the
      // user doesn't see a stale label until the next save call sets 'saving'.
      setStatus((s) => (s === 'retrying' ? 'idle' : s))
    }
  }, [])

  useEffect(
    () => () => {
      if (debounceRef.current != null) window.clearTimeout(debounceRef.current)
      if (retryTimerRef.current != null)
        window.clearTimeout(retryTimerRef.current)
    },
    [],
  )

  const save = useCallback(
    async (patch: { title?: string; body?: string }): Promise<boolean> => {
      // Any new save attempt supersedes a pending retry of an earlier payload.
      if (retryTimerRef.current != null) {
        window.clearTimeout(retryTimerRef.current)
        retryTimerRef.current = null
      }
      setStatus('saving')
      try {
        const updated = await updatePage.mutateAsync({ id: page.id, ...patch })
        lastSavedRef.current = { title: updated.title, body: updated.body }
        setStatus('saved')
        return true
      } catch (err) {
        // 5xx → one automatic retry after a short delay. 4xx and network
        // errors (status === 0) go straight to 'error' — those won't
        // self-heal within the retry window.
        if (err instanceof ApiError && err.status >= 500) {
          setStatus('retrying')
          retryTimerRef.current = window.setTimeout(async () => {
            retryTimerRef.current = null
            try {
              const updated = await updatePage.mutateAsync({
                id: page.id,
                ...patch,
              })
              lastSavedRef.current = {
                title: updated.title,
                body: updated.body,
              }
              setStatus('saved')
            } catch {
              setStatus('error')
            }
          }, RETRY_DELAY_MS)
          return false
        }
        setStatus('error')
        return false
      }
    },
    [page.id, updatePage],
  )

  const handleTitleBlur = useCallback(() => {
    // In draft mode the user commits explicitly via the Save button; blurring
    // the title input must not persist.
    if (isDraftMode) return
    const trimmed = title.trim()
    if (trimmed === lastSavedRef.current.title) return
    if (!trimmed) {
      // Empty title — revert to last saved value rather than persisting blank.
      setTitle(lastSavedRef.current.title)
      return
    }
    void save({ title: trimmed })
  }, [title, save, isDraftMode])

  // Latest body, readable from onBlur — which is wired into Milkdown's
  // listener once at mount and would otherwise close over a stale value.
  // Updated in the change handler (the only body writer outside draft
  // seeding, where blur-save is suppressed anyway), not during render.
  const bodyRef = useRef(body)

  // Debounced body auto-save: schedule a save 500ms after the last keystroke;
  // blur cancels the timer and fires immediately if there's a pending change.
  // In draft mode we still update local `body` state but suppress the
  // debounced save — user commits via the explicit Save button.
  const handleBodyChange = useCallback(
    (next: string) => {
      bodyRef.current = next
      setBody(next)
      if (isDraftMode) return
      cancelPendingSave()
      debounceRef.current = window.setTimeout(() => {
        debounceRef.current = null
        if (next === lastSavedRef.current.body) return
        void save({ body: next })
      }, BODY_DEBOUNCE_MS)
    },
    [save, cancelPendingSave, isDraftMode],
  )

  const handleBodyBlur = useCallback(() => {
    if (isDraftMode) return
    cancelPendingSave()
    const current = bodyRef.current
    if (current === lastSavedRef.current.body) return
    void save({ body: current })
  }, [save, cancelPendingSave, isDraftMode])

  // M9.3 — explicit draft commit. Force-flushes any in-flight debounce
  // (no-op in draft mode since we suppressed auto-save, but cheap and safe)
  // then PATCHes title + body via the existing save flow. On success the
  // ?draft= param is stripped — PageView re-renders and the editor remounts
  // (key flips from `draft-N` to `live`) with the now-canonical body.
  const handleDraftSave = useCallback(async () => {
    cancelPendingSave()
    const trimmedTitle = title.trim() || lastSavedRef.current.title
    const ok = await save({ title: trimmedTitle, body })
    if (!ok) return
    stripDraftParam()
  }, [cancelPendingSave, title, body, save, stripDraftParam])

  const handleDraftCancel = useCallback(() => {
    stripDraftParam()
  }, [stripDraftParam])

  // autoFocus rule: empty title → focus title; non-empty → focus body.
  const titleAutoFocus = page.title.length === 0
  const bodyAutoFocus = page.title.length > 0

  return (
    <div className="flex-1 flex flex-col min-h-0">
      <header className={PAGE_HEADER_CLASS}>
        <Breadcrumb
          spaceId={spaceId}
          pageId={page.id}
          titleEditor={
            isSheet && !isViewer
              ? { value: title, onChange: setTitle, onBlur: handleTitleBlur }
              : undefined
          }
        />
        <div className="flex items-center gap-[var(--space-1)] md:gap-[var(--space-3)] shrink-0">
          {isDraftMode ? (
            <>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={handleDraftCancel}
                className="h-[var(--space-8)] px-[var(--space-3)]"
              >
                Cancel
              </Button>
              <Button
                type="button"
                variant="primary"
                size="sm"
                onClick={() => void handleDraftSave()}
                disabled={status === 'saving' || status === 'retrying'}
                className="h-[var(--space-8)] px-[var(--space-3)]"
              >
                {status === 'saving' || status === 'retrying'
                  ? 'Saving…'
                  : 'Save'}
              </Button>
            </>
          ) : (
            <>
              {isSheet ? (
                <SheetExportMenu body={body} title={title} activeSheet={activeSheet} />
              ) : null}
              {/* Confluence "Done" — leave edit mode back to the read view. */}
              {roleResolved && !isViewer ? (
                <Button
                  type="button"
                  variant="secondary"
                  size="sm"
                  onClick={exitEdit}
                  className="h-[var(--space-8)] px-[var(--space-3)]"
                >
                  Done
                </Button>
              ) : null}
              {isDeck && roleResolved ? (
                <Button
                  type="button"
                  variant="primary"
                  size="sm"
                  aria-label="Present"
                  title="Present — live slides in a new tab"
                  onClick={() => openDeckPresent(page.id)}
                  className="h-[var(--space-8)] px-[var(--space-3)]"
                >
                  <Presentation width={16} height={16} />
                  <span>Present</span>
                </Button>
              ) : null}
              <PresenceAvatars awareness={provider?.awareness ?? null} />
              <SaveIndicator status={status} />
              {/* Frequent actions stay on the bar; the rest live in the "•••"
                  menu (PageActionsMenu) to keep the header uncluttered. Below md
                  the whole strip folds into that menu — see the read header. */}
              {/* Self-hides without frontmatter — see the read header. */}
              <PageProperties props={page.props} />
              <div className="hidden md:flex items-center gap-[var(--space-3)]">
                {roleResolved ? <FavoriteStar pageId={page.id} /> : null}
                {roleResolved ? <FollowButton id={page.id} /> : null}
                {roleResolved && !isViewer ? (
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    aria-label="Comments"
                    onClick={openComments}
                    className="h-[var(--space-8)] px-[var(--space-3)]"
                  >
                    <MessageSquare width={16} height={16} />
                    {openThreadCount != null ? (
                      <span>Comments ({openThreadCount})</span>
                    ) : (
                      <span>Comments</span>
                    )}
                  </Button>
                ) : null}
                {roleResolved ? (
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    aria-label="Graph"
                    title="Graph — this page's connections"
                    onClick={openGraph}
                    className="h-[var(--space-8)] w-[var(--space-8)] p-0"
                  >
                    <Share2 width={16} height={16} />
                  </Button>
                ) : null}
                {/* Visibility pill — ambient exposure indicator; editors click to
                    manage sharing, viewers see a static status chip. */}
                {roleResolved && !isViewer ? (
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    aria-label="Manage sharing"
                    onClick={() => setShareOpen(true)}
                    className="h-[var(--space-8)] px-[var(--space-2)]"
                  >
                    <VisibilityBadge
                      state={page.exposure?.state ?? 'private'}
                      inherited={page.exposure?.inherited ?? false}
                    />
                  </Button>
                ) : null}
                {roleResolved && isViewer ? (
                  <VisibilityBadge
                    state={page.exposure?.state ?? 'private'}
                    inherited={page.exposure?.inherited ?? false}
                    className="ml-[var(--space-1)]"
                  />
                ) : null}
              </div>
              {roleResolved ? (
                <PageActionsMenu
                  spaceId={spaceId}
                  pageId={page.id}
                  title={page.title}
                  isViewer={isViewer}
                  onDelete={() => setDeleteOpen(true)}
                  compactActions={
                    <>
                      {roleResolved && !isViewer ? (
                        <DropdownMenuItem onSelect={openComments}>
                          <MessageSquare width={14} height={14} />
                          {openThreadCount != null
                            ? `Comments (${openThreadCount})`
                            : 'Comments'}
                        </DropdownMenuItem>
                      ) : null}
                      <DropdownMenuItem onSelect={openGraph}>
                        <Share2 width={14} height={14} /> Page connections
                      </DropdownMenuItem>
                      {!isViewer ? (
                        <DropdownMenuItem onSelect={() => setShareOpen(true)}>
                          <Users width={14} height={14} /> Sharing &amp; access
                        </DropdownMenuItem>
                      ) : null}
                      <FavoriteStar pageId={page.id} asMenuItem />
                      <FollowButton id={page.id} asMenuItem />
                    </>
                  }
                />
              ) : null}
            </>
          )}
        </div>
      </header>

      {isSheet ? (
        // First-class sheet edit surface: the collaborative grid fills the whole
        // content area, edge-to-edge, identical geometry to the read view (no
        // title row → no layout shift when toggling edit). Renaming is inline in
        // the header breadcrumb (titleEditor below).
        <div className="flex-1 min-h-0 flex flex-col">
          <Suspense fallback={<EditorFallback />}>
            <GridEditor
              defaultValue={page.body}
              onChange={handleBodyChange}
              onBlur={handleBodyBlur}
              collabPageId={isViewer ? null : page.id}
              readOnly={isViewer}
              autoFocus={bodyAutoFocus}
              ariaLabel="Spreadsheet"
              pageId={page.id}
              onSheetChange={setActiveSheet}
            />
          </Suspense>
        </div>
      ) : (
      /* Mirror the read view: a full-width inner scroller (so the header stays
          put and wheel over the gutters scrolls) with the 56rem editing column
          as an inner wrapper. `min-h-full` lets the deck's fill-height textarea
          size against the column. data-page-scroll so the block handle hides on
          this scroll (its .closest resolves to the nearest one — here, not main). */
      <div
        ref={contentRef}
        data-page-scroll
        /* overflow-x-clip: vertical-only scroller on desktop.
           pointer-coarse:overflow-visible releases it to document scroll on
           touch so a pinch-zoomed page pans. See the app shell in router.tsx. */
        className="flex-1 overflow-y-auto overflow-x-clip min-h-0 pointer-coarse:overflow-visible"
      >
        <div className={cn(PAGE_CONTENT_CLASS, 'min-h-full')}>
        <WikilinkHoverPreview containerRef={contentRef} />
        {isDraftMode ? (
          <div
            role="status"
            className={cn(
              'flex items-center gap-[var(--space-2)]',
              'bg-[var(--surface-2)] border border-[var(--border-subtle)]',
              'rounded-[var(--radius-sm)]',
              'px-[var(--space-3)] py-[var(--space-2)]',
              'text-[length:var(--text-sm)] text-[var(--text-muted)]',
            )}
          >
            <History aria-hidden width={14} height={14} />
            <span>
              Restoring Revision #{draftRevId} · review and press Save to
              commit, or Cancel to abandon.
            </span>
          </div>
        ) : null}

        <textarea
          ref={titleRef}
          rows={1}
          autoFocus={titleAutoFocus && !isDraftMode}
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          onBlur={handleTitleBlur}
          onKeyDown={(e) => {
            // Titles are single-logical-line: Enter commits and moves on
            // rather than inserting a newline.
            if (e.key === 'Enter') {
              e.preventDefault()
              e.currentTarget.blur()
            }
          }}
          placeholder="Untitled page"
          aria-label="Page title"
          className={cn(
            // shrink-0: the title sits in a height-constrained flex column, and
            // a textarea is a shrinkable flex item — without this, flex compresses
            // it below its content and overflow-hidden clips the title to nothing.
            'block w-full shrink-0 resize-none overflow-hidden bg-transparent',
            'rounded-[var(--radius-md)] border border-transparent outline-none',
            'px-[var(--space-2)] py-[var(--space-2)]',
            'text-[length:var(--text-3xl)] leading-[var(--leading-tight)] font-medium',
            'text-[var(--text-primary)] placeholder:text-[var(--text-muted)]',
            'focus-visible:border-[var(--border-subtle)]',
          )}
        />

        {!isDraftMode ? <AttachmentStrip pageId={page.id} editable /> : null}

        {isDeck ? (
          // Deck body is Slidev markdown — edited as raw text so the rich editor
          // can't normalize/break `---` slide breaks or layout frontmatter on
          // save. Same body state + autosave (handleBodyChange/Blur) as any page.
          // A live outline (parsed from the unsaved buffer) sits alongside on
          // wide screens.
          <div className="flex min-h-0 flex-1 gap-[var(--space-4)]">
            <textarea
              value={body}
              onChange={(e) => handleBodyChange(e.target.value)}
              onBlur={handleBodyBlur}
              autoFocus={bodyAutoFocus}
              spellCheck={false}
              aria-label="Deck markdown"
              placeholder={'# Slide one\n\nWrite slides in Markdown.\n\n---\n\n# Slide two\n\n- Separate slides with ---'}
              className={cn(
                EDITOR_MIN_H,
                'min-w-0 flex-1 resize-none bg-transparent outline-none',
                'font-[family-name:var(--font-mono)] text-[length:var(--text-sm)]',
                'leading-relaxed text-[var(--text-primary)] placeholder:text-[var(--text-muted)]',
              )}
            />
            <Suspense fallback={null}>
              <DeckEditorOutline body={body} pageId={page.id} className="hidden w-[16rem] shrink-0 lg:flex" />
            </Suspense>
          </div>
        ) : isDraftMode ? (
          draftRevisionQuery.isError ? (
            <Card>
              <CardHeader>
                <CardTitle>Couldn't load revision</CardTitle>
                <CardDescription>
                  Revision #{draftRevId} couldn't be retrieved. Cancel to
                  return to the live page.
                </CardDescription>
              </CardHeader>
              <CardFooter>
                <Button
                  type="button"
                  variant="secondary"
                  onClick={handleDraftCancel}
                >
                  Cancel draft
                </Button>
              </CardFooter>
            </Card>
          ) : !draftRevisionQuery.data ? (
            <EditorFallback />
          ) : (
            <Suspense fallback={<EditorFallback />}>
              <MilkdownEditor
                key={`draft-${draftRevId}`}
                defaultValue={draftRevisionQuery.data.body}
                onChange={handleBodyChange}
                onBlur={handleBodyBlur}
                autoFocus={true}
                ariaLabel="Page body (draft)"
                className={EDITOR_MIN_H}
                aliveWikilinkIds={aliveWikilinkIds}
                wikilinkResolveIndex={wikilinkResolveIndex}
                collabPageId={null}
                readOnly={false}
                pageId={page.id}
              />
            </Suspense>
          )
        ) : roleResolved ? (
          <Suspense fallback={<EditorFallback />}>
            <MilkdownEditor
              key="live"
              defaultValue={page.body}
              onChange={handleBodyChange}
              onBlur={handleBodyBlur}
              autoFocus={bodyAutoFocus}
              ariaLabel="Page body"
              className={EDITOR_MIN_H}
              aliveWikilinkIds={aliveWikilinkIds}
              wikilinkResolveIndex={wikilinkResolveIndex}
              collabPageId={isViewer ? null : page.id}
              readOnly={isViewer}
              onCollabReady={setProvider}
              onViewReady={isViewer ? undefined : handleViewReady}
              onSelectionChange={isViewer ? undefined : handleSelectionChange}
              commentThreads={commentThreadsForEditor}
              onAnchorClick={isViewer ? undefined : handleAnchorClick}
              onAnchorsResolved={isViewer ? undefined : handleAnchorsResolved}
              showResolvedAnchors={showResolvedComments}
              pageId={page.id}
            />
          </Suspense>
        ) : (
          <EditorFallback />
        )}

        {/* Decks and sheets have their own validators (DeckEditorOutline / the
            grid); this is for prose bodies, and stays invisible unless the
            render would differ from what's written. */}
        {!isDeck && !isSheet && !isViewer ? (
          <Suspense fallback={null}>
            <PageLintNotice body={body} pageId={page.id} />
          </Suspense>
        ) : null}

        <ChildPagesSection
          spaceId={spaceId}
          pageId={page.id}
          bodyIsEmpty={body.trim().length === 0}
        />

        <BacklinksSection pageId={page.id} />

        <RelatedPagesSection pageId={page.id} spaceId={spaceId} />

        <LocalGraphCard pageId={page.id} />
        </div>
      </div>
      )}

      <DeletePageConfirmDialog
        page={page}
        spaceId={spaceId}
        open={deleteOpen}
        onOpenChange={setDeleteOpen}
        onDeleted={onDeleted}
      />

      {roleResolved ? (
        <LocalGraphPanel
          pageId={page.id}
          open={graphOpen}
          onOpenChange={setGraphOpen}
        />
      ) : null}

      {roleResolved && !isViewer && !isDraftMode && me.data ? (
        <CommentsPanel
          pageId={page.id}
          open={commentsOpen}
          onOpenChange={setCommentsOpen}
          hasSelection={!selectionEmpty}
          captureAnchor={captureCurrentAnchor}
          anchorPreview={selectionEmpty ? null : selectionPreview}
          me={{ id: me.data.id, username: me.data.username }}
          isSpaceOwner={isSpaceOwner}
          orphanIds={orphanIds}
          showResolved={showResolvedComments}
          onShowResolvedChange={handleShowResolvedChange}
        />
      ) : null}

      {roleResolved && !isViewer && !isDraftMode ? (
        <Suspense fallback={null}>
          <ShareManagerSheet
            pageId={page.id}
            open={shareOpen}
            onOpenChange={setShareOpen}
          />
        </Suspense>
      ) : null}
    </div>
  )
}

function findAncestorChain(
  tree: PageTreeNode[],
  pageId: number,
): PageTreeNode[] | null {
  for (const node of tree) {
    if (node.id === pageId) return [node]
    const sub = findAncestorChain(node.children, pageId)
    if (sub) return [node, ...sub]
  }
  return null
}

const EXCERPT_MAX = 80

function bodyExcerpt(body: string): string {
  // Strip the most common markdown noise so the snippet reads as plain prose
  // rather than syntax. Good enough for an at-a-glance preview; not a parser.
  const stripped = body
    .replace(/^#{1,6}\s+/gm, '')
    .replace(/`{1,3}[^`]*`{1,3}/g, ' ')
    .replace(/!\[[^\]]*\]\([^)]*\)/g, ' ')
    .replace(/\[([^\]]+)\]\([^)]*\)/g, '$1')
    .replace(/[*_~>]/g, '')
    .replace(/\s+/g, ' ')
    .trim()
  if (stripped.length <= EXCERPT_MAX) return stripped
  return stripped.slice(0, EXCERPT_MAX).trimEnd() + '…'
}

interface ChildPagesSectionProps {
  spaceId: number
  pageId: number
  bodyIsEmpty: boolean
}

function ChildPagesSection({
  spaceId,
  pageId,
  bodyIsEmpty,
}: ChildPagesSectionProps) {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const tree = usePages({ spaceId, tree: true })
  const createPage = useCreatePage()
  const nodes = (tree.data as PageTreeNode[] | undefined) ?? []
  const chain = findAncestorChain(nodes, pageId)
  const current = chain ? chain[chain.length - 1] : null
  const children = current?.children ?? []

  async function handleAddChild() {
    try {
      const created = await createPage.mutateAsync({
        space_id: spaceId,
        parent_id: pageId,
        title: 'Untitled',
      })
      void navigate({
        to: '/spaces/$spaceId/pages/$pageId/{-$slug}',
        params: { spaceId, pageId: created.id, slug: undefined },
      })
    } catch {
      // Tree refetch surfaces failure; v0 has no toast layer.
    }
  }

  if (children.length === 0) {
    if (!bodyIsEmpty) return null
    return (
      <div className="pt-[var(--space-3)] border-t border-[var(--border-subtle)]">
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={() => void handleAddChild()}
          disabled={createPage.isPending}
          className="text-[var(--text-muted)] hover:text-[var(--text-primary)] px-[var(--space-2)]"
        >
          <Plus width={14} height={14} /> Add child page
        </Button>
      </div>
    )
  }

  return (
    <section
      aria-labelledby={`child-pages-${pageId}`}
      className="flex flex-col gap-[var(--space-2)] pt-[var(--space-4)] border-t border-[var(--border-subtle)]"
    >
      <h2
        id={`child-pages-${pageId}`}
        className="m-0 text-[length:var(--text-xs)] uppercase tracking-wider text-[var(--text-muted)] font-[family-name:var(--font-sans)]"
      >
        Child pages
      </h2>
      <ul className="m-0 p-0 list-none flex flex-col gap-[1px]">
        {children.map((child) => {
          const excerpt = bodyExcerpt(child.body)
          return (
            <li key={child.id} className="m-0 p-0 list-none">
              <button
                type="button"
                onMouseEnter={() => prefetchPage(qc, child.id)}
                onFocus={() => prefetchPage(qc, child.id)}
                onClick={() =>
                  void navigate({
                    to: '/spaces/$spaceId/pages/$pageId/{-$slug}',
                    params: { spaceId, pageId: child.id, slug: undefined },
                  })
                }
                className={cn(
                  'group w-full text-left',
                  'flex items-start gap-[var(--space-3)]',
                  'px-[var(--space-3)] py-[var(--space-2)]',
                  'rounded-[var(--radius-sm)]',
                  'bg-transparent border-0 cursor-pointer outline-none',
                  'hover:bg-[var(--surface-2)]',
                  'focus-visible:ring-2 focus-visible:ring-[var(--accent)]',
                )}
              >
                <FileText
                  aria-hidden
                  width={14}
                  height={14}
                  className="mt-[2px] shrink-0 text-[var(--text-muted)] group-hover:text-[var(--text-primary)]"
                />
                <span className="flex-1 min-w-0 flex flex-col gap-[2px]">
                  <span className="truncate text-[length:var(--text-sm)] text-[var(--text-primary)] font-medium font-[family-name:var(--font-sans)]">
                    {child.title || 'Untitled'}
                  </span>
                  {excerpt ? (
                    <span className="truncate text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
                      {excerpt}
                    </span>
                  ) : null}
                </span>
              </button>
            </li>
          )
        })}
      </ul>
    </section>
  )
}

function Breadcrumb({
  spaceId,
  pageId,
  // When set (sheet edit mode), the last crumb (the page title) renders as an
  // inline input controlled by the editor — renaming happens here instead of a
  // content title row, so entering edit never shifts/shrinks the grid.
  titleEditor,
}: {
  spaceId: number
  pageId: number
  titleEditor?: { value: string; onChange: (v: string) => void; onBlur: () => void }
}) {
  const space = useSpace(spaceId)
  const tree = usePages({ spaceId, tree: true })
  const nodes = (tree.data as PageTreeNode[] | undefined) ?? []
  const chain = findAncestorChain(nodes, pageId) ?? []
  const current = chain[chain.length - 1]
  const parent = chain[chain.length - 2]

  return (
    <nav
      aria-label="Breadcrumb"
      className="flex items-center min-w-0 text-[length:var(--text-sm)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]"
    >
      {/* Phone: the full chain never fits — it truncated to "P… › D…", which
          named neither the space nor the page. One level up as a back
          affordance plus this page's title instead; the title also keeps the
          sticky header meaningful once the H1 has scrolled past. */}
      <div className="flex md:hidden items-center min-w-0 gap-[var(--space-1)]">
        {parent ? (
          <Link
            to="/spaces/$spaceId/pages/$pageId/{-$slug}"
            params={{ spaceId, pageId: parent.id, slug: undefined }}
            aria-label={`Up to ${parent.title || 'Untitled'}`}
            className="shrink-0 -ml-[var(--space-2)] inline-flex h-[var(--space-8)] w-[var(--space-8)] items-center justify-center rounded-[var(--radius-xs)] hover:text-[var(--text-primary)]"
          >
            <ChevronLeft width={18} height={18} aria-hidden />
          </Link>
        ) : (
          <Link
            to="/spaces/$spaceId"
            params={{ spaceId }}
            aria-label={`Up to ${space.data?.name ?? 'space'}`}
            className="shrink-0 -ml-[var(--space-2)] inline-flex h-[var(--space-8)] w-[var(--space-8)] items-center justify-center rounded-[var(--radius-xs)] hover:text-[var(--text-primary)]"
          >
            <ChevronLeft width={18} height={18} aria-hidden />
          </Link>
        )}
        {titleEditor ? (
          <input
            value={titleEditor.value}
            onChange={(e) => titleEditor.onChange(e.target.value)}
            onBlur={titleEditor.onBlur}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault()
                e.currentTarget.blur()
              }
            }}
            placeholder="Untitled sheet"
            aria-label="Sheet title"
            className="min-w-0 flex-1 rounded-[var(--radius-sm)] border border-transparent bg-transparent px-[var(--space-1)] text-[var(--text-primary)] outline-none focus-visible:border-[var(--border-subtle)]"
          />
        ) : (
          <span className="truncate font-medium text-[var(--text-primary)]">
            {current?.title || space.data?.name || 'Untitled'}
          </span>
        )}
      </div>

      <span className="hidden md:flex items-center min-w-0">
      <Link
        to="/spaces/$spaceId"
        params={{ spaceId }}
        className="truncate hover:text-[var(--text-primary)] hover:underline underline-offset-2"
      >
        {space.data?.name ?? 'Space'}
      </Link>
      {chain.map((node, idx) => {
        const isLast = idx === chain.length - 1
        return (
          <span
            key={node.id}
            className="flex items-center min-w-0"
            aria-current={isLast ? 'page' : undefined}
          >
            <ChevronRight
              aria-hidden
              width={14}
              height={14}
              className="mx-[var(--space-1)] shrink-0"
            />
            {isLast ? (
              titleEditor ? (
                <input
                  value={titleEditor.value}
                  onChange={(e) => titleEditor.onChange(e.target.value)}
                  onBlur={titleEditor.onBlur}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') {
                      e.preventDefault()
                      e.currentTarget.blur()
                    }
                  }}
                  placeholder="Untitled sheet"
                  aria-label="Sheet title"
                  className="min-w-0 w-[min(28rem,50vw)] rounded-[var(--radius-sm)] border border-transparent bg-transparent px-[var(--space-1)] -mx-[var(--space-1)] text-[var(--text-primary)] outline-none hover:bg-[var(--surface-2)] focus-visible:border-[var(--border-subtle)]"
                />
              ) : (
                <span className="truncate text-[var(--text-primary)]">
                  {node.title || 'Untitled'}
                </span>
              )
            ) : (
              <Link
                to="/spaces/$spaceId/pages/$pageId/{-$slug}"
                params={{ spaceId, pageId: node.id, slug: undefined }}
                className="truncate hover:text-[var(--text-primary)] hover:underline underline-offset-2"
              >
                {node.title || 'Untitled'}
              </Link>
            )}
          </span>
        )
      })}
      </span>
    </nav>
  )
}

function PageLoading() {
  return (
    <div className={cn(PAGE_CONTENT_CLASS, 'flex-1 self-center')}>
      <div className="h-[calc(var(--space-8)+var(--space-3))] w-2/3 rounded-[var(--radius-sm)] bg-[var(--surface-2)]" />
      <div className="flex-1 min-h-[calc(var(--space-8)*4)] rounded-[var(--radius-md)] bg-[var(--surface-2)]" />
    </div>
  )
}

function PageError({ onRetry }: { onRetry: () => void }) {
  return (
    <EmptyState
      icon={TriangleAlert}
      tone="danger"
      title="Couldn't load this page"
      description="Something went wrong reaching the server. Try again."
      actions={
        <Button variant="secondary" onClick={onRetry}>
          Retry
        </Button>
      }
    />
  )
}

// The page exists but this account isn't a member of its space. Distinct from
// both PageError (a real outage — retryable) and PageNotFound (nothing there):
// the only thing that resolves it is someone granting access, so say so, and
// name the signed-in account since arriving on the wrong one is the usual cause.
// "Back to space" is deliberately absent — that space 403s too.
function PageForbidden({ username }: { username?: string }) {
  return (
    <EmptyState
      icon={Lock}
      title="You don't have access to this page"
      description={
        username
          ? `Ask an owner of this space to share it with you. You're signed in as ${username}.`
          : 'Ask an owner of this space to share it with you.'
      }
      actions={
        <Button asChild variant="secondary">
          <Link to="/">Your spaces</Link>
        </Button>
      }
    />
  )
}

function PageNotFound({ spaceId }: { spaceId: number }) {
  return (
    <EmptyState
      icon={FileQuestion}
      title="Page not found"
      description="The page may have been deleted or moved to another space."
      actions={
        <Button asChild variant="secondary">
          <Link to="/spaces/$spaceId" params={{ spaceId }}>
            Back to space
          </Link>
        </Button>
      }
    />
  )
}

interface DeletePageConfirmDialogProps {
  page: Page
  spaceId: number
  open: boolean
  onOpenChange: (next: boolean) => void
  onDeleted: () => void
}

function DeletePageConfirmDialog({
  page,
  spaceId,
  open,
  onOpenChange,
  onDeleted,
}: DeletePageConfirmDialogProps) {
  const [error, setError] = useState<string | null>(null)
  const deletePage = useDeletePage()

  function handleClose(next: boolean) {
    if (!next) setError(null)
    onOpenChange(next)
  }

  async function handleDelete() {
    setError(null)
    try {
      await deletePage.mutateAsync({ id: page.id, spaceId })
      handleClose(false)
      onDeleted()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Failed to delete page.')
    }
  }

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Delete this page?</DialogTitle>
          <DialogDescription>
            "{page.title || 'Untitled'}" and any child pages move to this space's
            Trash. You can put them back from there — the space's Trash tab.
          </DialogDescription>
        </DialogHeader>
        {error ? (
          <p className="m-0 text-[length:var(--text-xs)] text-[var(--danger)]">
            {error}
          </p>
        ) : null}
        <DialogFooter>
          <DialogClose asChild>
            <Button type="button" variant="ghost">
              Cancel
            </Button>
          </DialogClose>
          <Button
            type="button"
            variant="danger"
            onClick={handleDelete}
            disabled={deletePage.isPending}
          >
            {deletePage.isPending ? 'Deleting…' : 'Delete'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
