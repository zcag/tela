import { lazy, Suspense, useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { Menu, Play } from 'lucide-react'
import { applyPdfThemeParam } from '../../lib/theme'
import { buildWikilinkResolveIndex, pageSlug } from '../../lib/slug'
import { bodyExcerpt } from '../../lib/search/body-excerpt'
import { buildPageTree, sortByNewest, topLevelPosts, type TreeNode } from '../../lib/blog'
import {
  usePublicSpaceTree,
  type PublicPageNode,
  type PublicSpacePayload,
} from '../../lib/queries/public'
import { Button } from '../ui/button'
import {
  Sheet,
  SheetBody,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from '../ui/sheet'
import { ChildGallery } from './blog/ChildGallery'
import { DeckCoverImage } from './deck-cover-image'
import { DownloadPdfButton } from './DownloadPdfButton'
import { ReaderShell } from './ReaderShell'
import { pageSummary } from './SummaryHint'
import { useTelaHomeHref } from '../../lib/queries/host-context'

// Read-only spreadsheet grid for public sheet pages. Lazy — pulls in @defterjs/*
// only when a public sheet is actually viewed.
const GridEditor = lazy(() =>
  import('./grid-editor').then((m) => ({ default: m.GridEditor })),
)

interface PublicReaderViewProps {
  space: PublicSpacePayload
  pageId: number
  pageTitle: string
  pageBody: string
  pageProps?: Record<string, unknown>
  createdAt: string
  updatedAt: string
  // Page byline: original author + last editor (usernames; editor shown only
  // when it differs). Falls back to the space owner when absent.
  author?: string
  editor?: string
}

// A no-login reader for a page in a PUBLIC space — the blog surface. Same
// chrome-free reading-mode shell as authenticated /read and the share reader,
// with public-space wiring: the whole space is in scope (every page links
// freely), wikilinks hop within the public route, and a multi-page space gets a
// slim nav rail. No editor, no comments, no collab — purely read-only.
export function PublicReaderView({
  space,
  pageId,
  pageTitle,
  pageBody,
  pageProps,
  createdAt,
  updatedAt,
  author,
  editor,
}: PublicReaderViewProps) {
  const navigate = useNavigate()
  const telaHome = useTelaHomeHref()

  // SEO/social head for this public article: an author summary in frontmatter
  // wins, else the body lead. Canonical is the current (pretty) reader URL.
  const summary = pageSummary(pageProps)
  // A deck isn't an article — it presents. Detected from public props; the paths
  // are the public, visibility-gated deck routes (cover = first slide, present =
  // live SPA). Branch is taken below, AFTER all hooks, to keep hook order stable.
  const isDeck = pageProps?.deck === true
  const isSheet = pageProps?.sheet === true
  const metaDescription = useMemo(
    () => summary ?? bodyExcerpt(pageBody, '', 90).trim(),
    [summary, pageBody],
  )
  // Apply ?theme= once, pre-paint, for a themed PDF export; no-op for humans.
  useState(() => applyPdfThemeParam())

  const tree = usePublicSpaceTree(space.id)
  const pages = useMemo<PublicPageNode[]>(() => tree.data?.pages ?? [], [tree.data])

  // The whole public space is in scope — every page is freely linkable.
  const inScopePageIds = useMemo(() => {
    const set = new Set<number>()
    for (const p of pages) set.add(p.id)
    set.add(pageId)
    return set
  }, [pages, pageId])

  const wikilinkResolveIndex = useMemo<Map<string, number> | null>(
    () => (tree.data ? buildWikilinkResolveIndex(pages) : null),
    [tree.data, pages],
  )

  const onNavigateWikilink = useCallback(
    (targetPageId: number) => {
      if (!inScopePageIds.has(targetPageId)) return
      void navigate({
        to: '/public/spaces/$spaceId/pages/$pageId/{-$slug}',
        params: { spaceId: space.id, pageId: targetPageId, slug: undefined },
      })
    },
    [navigate, space.id, inScopePageIds],
  )

  const showSidebar = pages.length > 1

  // Hero cover only when the post actually sets one (props.cover) — no gradient
  // fallback in the article, that's an index-card affordance.
  const coverImage =
    typeof pageProps?.cover === 'string' && pageProps.cover.trim()
      ? pageProps.cover.trim()
      : typeof pageProps?.image === 'string' && pageProps.image.trim()
        ? pageProps.image.trim()
        : undefined

  // Byline → the page's own author (linked to their /u/{username} home), with
  // the last editor appended when a different person touched it last. Falls back
  // to the space owner for legacy pages with no recorded author.
  const authorLink = (name: string) => (
    <Link
      to="/$handle"
      params={{ handle: name }}
      className="reader-meta-link"
    >
      {name}
    </Link>
  )
  const byline = author ? (
    <>
      by {authorLink(author)}
      {editor && editor !== author ? <> · edited by {authorLink(editor)}</> : null}
    </>
  ) : space.owner_handle ? (
    <>
      by {authorLink(space.owner_handle)}
    </>
  ) : undefined

  // Direct children of the current page, in author order — any page with
  // children is a "section" and shows them as a card gallery at the foot of the
  // reader (decks, multi-part articles), so its contents present as real cards
  // rather than a hand-written link list. Position order matches the nav rail.
  const childPosts = useMemo(
    () =>
      pages
        .filter((p) => p.parent_id === pageId)
        .sort((a, b) => a.position - b.position || a.id - b.id),
    [pages, pageId],
  )

  // Previous/next among the space's top-level posts (the "posts"), in published
  // order. Only shown when the current page is itself a top-level post.
  const postNav = useMemo(() => {
    const posts = sortByNewest(topLevelPosts(pages))
    const i = posts.findIndex((p) => p.id === pageId)
    if (i === -1) return null
    return { newer: posts[i - 1], older: posts[i + 1] } // newest-first order
  }, [pages, pageId])

  const hasPostNav = !!(postNav && (postNav.newer || postNav.older))
  const articleFooter =
    childPosts.length > 0 || hasPostNav ? (
      <>
        <ChildGallery spaceId={space.id} posts={childPosts} />
        {hasPostNav ? (
          <nav className="reader-postnav" aria-label="More posts">
            {postNav!.older ? (
              <PostNavLink spaceId={space.id} post={postNav!.older} dir="older" />
            ) : (
              <span />
            )}
            {postNav!.newer ? (
              <PostNavLink spaceId={space.id} post={postNav!.newer} dir="newer" />
            ) : (
              <span />
            )}
          </nav>
        ) : null}
      </>
    ) : undefined

  if (isDeck) {
    return (
      <PublicDeckView
        spaceId={space.id}
        spaceName={space.name}
        pageId={pageId}
        pageTitle={pageTitle}
        summary={summary}
        telaHome={telaHome}
      />
    )
  }

  const sheetSlot = isSheet ? (
    <Suspense fallback={null}>
      <GridEditor
        defaultValue={pageBody}
        onChange={() => {}}
        collabPageId={null}
        readOnly
        pageId={pageId}
        ariaLabel="Spreadsheet (read-only)"
      />
    </Suspense>
  ) : undefined

  return (
    <ReaderShell
      pageId={pageId}
      title={pageTitle}
      summary={summary}
      body={pageBody}
      bodySlot={sheetSlot}
      updatedAt={updatedAt}
      wikilinkMode="share"
      aliveWikilinkIds={inScopePageIds}
      wikilinkResolveIndex={wikilinkResolveIndex}
      onNavigateWikilink={onNavigateWikilink}
      coverImage={coverImage}
      byline={byline}
      publishedAt={createdAt}
      articleFooter={articleFooter}
      headMeta={{
        description: metaDescription,
        canonicalPath: window.location.pathname,
        image: `/p/${pageId}/og.png`,
        feedHref: `/api/public/spaces/${space.id}/feed.xml`,
      }}
      sidebar={
        showSidebar ? (
          <PublicSpaceNav
            spaceId={space.id}
            spaceName={space.name}
            pages={pages}
            activePageId={pageId}
          />
        ) : undefined
      }
      topbarLeading={
        <span className="flex items-center gap-[var(--space-2)] min-w-0">
          {/* Mobile-only: open the space's page tree (the fixed rail is hidden
              below lg). */}
          {showSidebar ? (
            <PublicSpaceNavSheet
              spaceId={space.id}
              spaceName={space.name}
              pages={pages}
              activePageId={pageId}
            />
          ) : null}
          <a
            href={telaHome}
            aria-label="tela home"
            // shrink-0: a flex child shrinks below its content by default, so on
            // a phone the wordmark was squeezed until its text spilled out of its
            // own box and collided with the trailing controls. The space name
            // (truncate, below) is what should absorb the shrink instead.
            className="inline-block shrink-0 rounded-[var(--radius-xs)] font-[family-name:var(--font-sans)] text-[length:var(--text-base)] font-medium text-[var(--text-primary)] no-underline transition-opacity duration-[var(--duration-fast)] hover:opacity-70 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]"
          >
            tela
          </a>
          <span aria-hidden className="shrink-0 text-[var(--text-muted)]">
            /
          </span>
          {/* Back to the space's front page (its blog index). */}
          <Link
            to="/public/spaces/$spaceId"
            params={{ spaceId: space.id }}
            className="truncate rounded-[var(--radius-xs)] font-[family-name:var(--font-sans)] text-[length:var(--text-sm)] text-[var(--text-muted)] no-underline transition-colors duration-[var(--duration-fast)] hover:text-[var(--text-primary)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]"
          >
            {space.name}
          </Link>
        </span>
      }
      sourceLabel={space.name}
      topbarTrailing={
        <>
          <DownloadPdfButton url={`${window.location.pathname}.pdf`} themed compact />
          <Button asChild variant="ghost" size="sm">
            <a href="/login">Sign in</a>
          </Button>
        </>
      }
    />
  )
}

// A public deck isn't read as prose — it presents. Show the first-slide cover as
// a hero that opens the live Slidev SPA (the public, visibility-gated present
// route), with the same lightweight topbar as the reader. The crawler-facing OG
// card is handled server-side (HandlePublicReaderOG → /p/{id}/og.png, the first
// slide), so this view is purely for humans.
function PublicDeckView({
  spaceId,
  spaceName,
  pageId,
  pageTitle,
  summary,
  telaHome,
}: {
  spaceId: number
  spaceName: string
  pageId: number
  pageTitle: string
  summary?: string | null
  telaHome: string | null
}) {
  const presentPath = `/api/public/spaces/${spaceId}/pages/${pageId}/deck/spa/`
  const coverPath = `/api/public/spaces/${spaceId}/pages/${pageId}/deck/cover`
  const present = () => window.open(presentPath, '_blank', 'noopener')
  // Pre-warm the public Present build while the visitor looks at the cover, so
  // clicking Present opens instantly instead of waiting on a cold slidev build.
  // The public base is a separate build from the gated one — fire-and-forget.
  useEffect(() => {
    void fetch(presentPath, { credentials: 'same-origin' }).catch(() => {})
  }, [presentPath])
  return (
    <div className="flex min-h-dvh flex-col bg-[var(--surface-1)] text-[var(--text-primary)]">
      <header className="flex items-center justify-between gap-[var(--space-2)] border-b border-[var(--border-subtle)] px-[var(--space-4)] py-[var(--space-3)]">
        <span className="flex min-w-0 items-center gap-[var(--space-2)]">
          <a
            href={telaHome ?? '/'}
            aria-label="tela home"
            className="font-[family-name:var(--font-sans)] text-[length:var(--text-base)] font-medium text-[var(--text-primary)] no-underline hover:opacity-70"
          >
            tela
          </a>
          <span aria-hidden className="text-[var(--text-muted)]">
            /
          </span>
          <Link
            to="/public/spaces/$spaceId"
            params={{ spaceId }}
            className="truncate text-[length:var(--text-sm)] text-[var(--text-muted)] no-underline hover:text-[var(--text-primary)]"
          >
            {spaceName}
          </Link>
        </span>
        <Button asChild variant="ghost" size="sm">
          <a href="/login">Sign in</a>
        </Button>
      </header>
      <main className="mx-auto flex w-full max-w-3xl flex-1 flex-col items-center justify-center gap-[var(--space-5)] p-[var(--space-6)]">
        <button
          type="button"
          onClick={present}
          aria-label={`Present ${pageTitle || 'deck'}`}
          className="group relative block aspect-video w-full overflow-hidden rounded-[var(--radius-lg)] border border-[var(--border-subtle)] bg-[var(--surface-2)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]"
        >
          <DeckCoverImage
            src={coverPath}
            alt={pageTitle || 'Deck cover'}
            className="h-full w-full object-cover"
            loading="eager"
          />
          <span className="absolute inset-0 flex items-center justify-center bg-[color-mix(in_srgb,var(--text-primary)_45%,transparent)] opacity-0 transition-opacity duration-[var(--duration-fast)] group-hover:opacity-100">
            <span className="flex items-center gap-[var(--space-2)] rounded-[var(--radius-md)] bg-[var(--surface-1)] px-[var(--space-4)] py-[var(--space-2)] text-[length:var(--text-sm)] font-medium shadow-[var(--shadow-md)]">
              <Play width={16} height={16} /> Present
            </span>
          </span>
        </button>
        <div className="flex flex-col items-center gap-[var(--space-3)] text-center">
          <h1 className="font-[family-name:var(--font-serif)] text-[length:var(--text-2xl)] font-semibold">
            {pageTitle || 'Untitled deck'}
          </h1>
          {summary ? (
            <p className="max-w-prose text-[length:var(--text-base)] text-[var(--text-muted)]">
              {summary}
            </p>
          ) : null}
          <Button onClick={present} size="md">
            <Play width={16} height={16} /> Present
          </Button>
        </div>
      </main>
    </div>
  )
}

// One end of the previous/next post navigation under an article.
function PostNavLink({
  spaceId,
  post,
  dir,
}: {
  spaceId: number
  post: PublicPageNode
  dir: 'older' | 'newer'
}) {
  return (
    <Link
      to="/public/spaces/$spaceId/pages/$pageId/{-$slug}"
      params={{ spaceId, pageId: post.id, slug: pageSlug(post.title) || undefined }}
      className={`reader-postnav-link reader-postnav-${dir}`}
    >
      <span className="reader-postnav-dir">
        {dir === 'older' ? '← Older' : 'Newer →'}
      </span>
      <span className="reader-postnav-title">{post.title || 'Untitled'}</span>
    </Link>
  )
}

// PublicSpaceNav — the structural left rail in the public reader. Reflects the
// space's actual page hierarchy (nested children indented under their parent,
// siblings in author position order) so it's clear what belongs to what. Width
// is bounded in reader.css; long titles truncate rather than ballooning the rail.
function PublicSpaceNav({
  spaceId,
  spaceName,
  pages,
  activePageId,
}: {
  spaceId: number
  spaceName: string
  pages: PublicPageNode[]
  activePageId: number
}) {
  const tree = useMemo(() => buildPageTree(pages), [pages])
  return (
    <nav aria-label={`${spaceName} pages`} className="reader-spacenav">
      <p className="reader-spacenav-title">{spaceName}</p>
      <PublicSpaceNavList
        spaceId={spaceId}
        nodes={tree}
        activePageId={activePageId}
      />
    </nav>
  )
}

function PublicSpaceNavList({
  spaceId,
  nodes,
  activePageId,
  onNavigate,
  sub = false,
}: {
  spaceId: number
  nodes: TreeNode<PublicPageNode>[]
  activePageId: number
  /** Fired on a link click — lets the mobile sheet close itself on navigation. */
  onNavigate?: () => void
  sub?: boolean
}) {
  return (
    <ul className={sub ? 'reader-spacenav-sublist' : 'reader-spacenav-list'}>
      {nodes.map((node) => (
        <li key={node.id}>
          <Link
            to="/public/spaces/$spaceId/pages/$pageId/{-$slug}"
            params={{
              spaceId,
              pageId: node.id,
              slug: pageSlug(node.title) || undefined,
            }}
            className="reader-spacenav-link"
            data-active={node.id === activePageId}
            aria-current={node.id === activePageId ? 'page' : undefined}
            title={node.title || 'Untitled'}
            onClick={onNavigate}
          >
            {node.title || 'Untitled'}
          </Link>
          {node.children.length > 0 ? (
            <PublicSpaceNavList
              spaceId={spaceId}
              nodes={node.children}
              activePageId={activePageId}
              onNavigate={onNavigate}
              sub
            />
          ) : null}
        </li>
      ))}
    </ul>
  )
}

// Mobile counterpart of the space rail — a left Sheet drawer triggered from the
// reader topbar (the fixed rail is hidden below lg). Same hierarchy; closes on
// navigation.
function PublicSpaceNavSheet({
  spaceId,
  spaceName,
  pages,
  activePageId,
}: {
  spaceId: number
  spaceName: string
  pages: PublicPageNode[]
  activePageId: number
}) {
  const [open, setOpen] = useState(false)
  const tree = useMemo(() => buildPageTree(pages), [pages])
  return (
    <Sheet open={open} onOpenChange={setOpen}>
      <SheetTrigger asChild>
        <Button
          variant="ghost"
          size="sm"
          aria-label="Pages in this space"
          className="lg:hidden h-[var(--space-8)] w-[var(--space-8)] p-0"
        >
          <Menu width={18} height={18} />
        </Button>
      </SheetTrigger>
      <SheetContent side="left" className="w-[18rem] max-w-[85vw]">
        <SheetHeader>
          <SheetTitle>{spaceName}</SheetTitle>
        </SheetHeader>
        <SheetBody>
          <nav aria-label={`${spaceName} pages`}>
            <PublicSpaceNavList
              spaceId={spaceId}
              nodes={tree}
              activePageId={activePageId}
              onNavigate={() => setOpen(false)}
            />
          </nav>
        </SheetBody>
      </SheetContent>
    </Sheet>
  )
}
