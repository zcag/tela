import { useEffect, useMemo } from 'react'
import { useParams } from '@tanstack/react-router'
import { ShareError, useShareRoot, useSharePage, useShareTree } from '../lib/queries/share'
import { pageSlug } from '../lib/slug'
import { ShareLayout } from '../components/app/ShareLayout'
import { ShareReaderView } from '../components/app/ShareReader'
import { SharePasswordScreen } from '../components/app/SharePasswordScreen'
import { ThemeSwitcher } from '../components/ThemeSwitcher'
import { Card, CardDescription, CardHeader, CardTitle } from '../components/ui/card'
import { useBrand } from '../lib/queries/host-context'

// Public share routes — attached to `rootRoute` (NOT appLayoutRoute) because
// share-mode is unauthenticated. The router config wires both routes through
// lazyRouteComponent so the share bundle ships as its own chunk and the main
// chunk for logged-in users isn't bloated.

function ShareShell({ children }: { children: React.ReactNode }) {
  const brandName = useBrand().name
  // Bare shell for error / loading states (no Sidebar / Header chrome from
  // the in-scope share metadata yet).
  return (
    <div className="min-h-dvh flex flex-col bg-[var(--surface-1)] text-[var(--text-primary)]">
      <header className="flex items-center justify-between px-[var(--space-6)] py-[var(--space-3)] border-b border-[var(--border-subtle)] shrink-0">
        <h1 className="m-0 text-[length:var(--text-lg)] leading-[var(--leading-tight)] font-[family-name:var(--font-sans)]">
          {/* Plain <a> (full nav) to the apex marketing landing — share-mode
              wordmark escapes the SPA rather than client-routing into the app. */}
          <a
            href="/"
            aria-label={`${brandName} home`}
            className="inline-block rounded-[var(--radius-xs)] text-[var(--text-primary)] no-underline transition-opacity duration-[var(--duration-fast)] hover:opacity-70 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]"
          >
            {brandName}
          </a>
        </h1>
        <ThemeSwitcher />
      </header>
      <main className="flex-1 flex items-center justify-center p-[var(--space-7)]">
        {children}
      </main>
    </div>
  )
}

function ShareLoading() {
  return (
    <ShareShell>
      <p
        role="status"
        className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]"
      >
        Loading…
      </p>
    </ShareShell>
  )
}

function ShareUnavailable({
  message = "This share link is not available.",
}: {
  message?: string
}) {
  return (
    <ShareShell>
      <Card className="w-full max-w-[24rem]">
        <CardHeader>
          <CardTitle className="text-[length:var(--text-2xl)]">
            Not available
          </CardTitle>
          <CardDescription>{message}</CardDescription>
        </CardHeader>
      </Card>
    </ShareShell>
  )
}

// Build an in-scope page-id set from the share tree. Used by ShareReader to
// scope wikilink decoration: out-of-scope `tela://page/N` links render as
// plain text, in-scope ones stay clickable + route to /share/{token}/p/{N}.
function useInScopePageIds(
  token: string,
  includeDescendants: boolean,
): Set<number> {
  const tree = useShareTree(token, includeDescendants)
  return useMemo(() => {
    const set = new Set<number>()
    if (tree.data?.pages) {
      for (const p of tree.data.pages) set.add(p.id)
    }
    return set
  }, [tree.data])
}

export function ShareRootRoute() {
  // Route-agnostic: rendered by both /share/$token and the slugged
  // /share/$token/$slug, so read params loosely.
  const params = useParams({ strict: false }) as { token: string }
  const token = params.token
  const rootQuery = useShareRoot(token)
  // The "in-scope" id set drives wikilink scoping. When the share is
  // single-page, the set is just the root id (the root page is always in
  // scope). Adding it eagerly here also avoids the small flash where every
  // wikilink would briefly look out-of-scope before the tree query lands.
  const inScopeFromTree = useInScopePageIds(
    token,
    rootQuery.data?.share.include_descendants ?? false,
  )
  const inScope = useMemo(() => {
    const set = new Set(inScopeFromTree)
    if (rootQuery.data?.page.id != null) set.add(rootQuery.data.page.id)
    return set
  }, [inScopeFromTree, rootQuery.data])

  // Canonicalise the share URL to /share/{token}/{slug} once the title is known
  // (and on rename). history.replaceState rather than router navigation, so a
  // slug change never re-matches routes / remounts the reader. The token is
  // canonical; this only prettifies the address bar.
  const rootTitle = rootQuery.data?.page.title ?? ''
  useEffect(() => {
    if (!rootQuery.data) return
    const slug = pageSlug(rootTitle)
    const base = `/share/${token}`
    const desiredPath = slug ? `${base}/${slug}` : base
    if (window.location.pathname !== desiredPath) {
      window.history.replaceState(
        window.history.state,
        '',
        desiredPath + window.location.search + window.location.hash,
      )
    }
  }, [rootQuery.data, rootTitle, token])

  if (rootQuery.isLoading) return <ShareLoading />

  const err = rootQuery.error
  if (err instanceof ShareError) {
    if (err.kind === 'password_required') {
      return <SharePasswordScreen token={token} />
    }
    if (err.kind === 'not_found') {
      return <ShareUnavailable />
    }
    return <ShareUnavailable message="Couldn't open this share. Please try again." />
  }

  const data = rootQuery.data
  if (!data) return <ShareLoading />

  return (
    <ShareReaderView
      token={token}
      share={data.share}
      pageId={data.page.id}
      pageTitle={data.page.title}
      pageBody={data.page.body}
      updatedAt={data.page.updated_at}
      inScopePageIds={inScope}
    />
  )
}

export function ShareDescendantRoute() {
  const { token, pageId } = useParams({ from: '/share/$token/p/$pageId' })
  const rootQuery = useShareRoot(token)
  // Only fetch descendant page after the root resolves — the page query needs
  // the same auth cookie (or no-password share) the root query establishes.
  // Fetching in parallel would race the cookie set in the root path.
  const pageQuery = useSharePage(
    token,
    pageId,
    rootQuery.data != null,
  )
  const inScopeFromTree = useInScopePageIds(
    token,
    rootQuery.data?.share.include_descendants ?? false,
  )
  const inScope = useMemo(() => {
    const set = new Set(inScopeFromTree)
    if (rootQuery.data?.page.id != null) set.add(rootQuery.data.page.id)
    return set
  }, [inScopeFromTree, rootQuery.data])

  if (rootQuery.isLoading) return <ShareLoading />

  const rootErr = rootQuery.error
  if (rootErr instanceof ShareError) {
    if (rootErr.kind === 'password_required') {
      return <SharePasswordScreen token={token} />
    }
    if (rootErr.kind === 'not_found') {
      return <ShareUnavailable />
    }
    return <ShareUnavailable message="Couldn't open this share. Please try again." />
  }
  const rootData = rootQuery.data
  if (!rootData) return <ShareLoading />

  if (pageQuery.isLoading) {
    return (
      <ShareLayout token={token} share={rootData.share}>
        <div className="flex-1 flex items-center justify-center p-[var(--space-7)]">
          <p
            role="status"
            className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]"
          >
            Loading…
          </p>
        </div>
      </ShareLayout>
    )
  }

  const pageErr = pageQuery.error
  if (pageErr instanceof ShareError && pageErr.kind === 'not_found') {
    return (
      <ShareLayout token={token} share={rootData.share}>
        <div className="flex-1 flex items-center justify-center p-[var(--space-7)]">
          <Card className="w-full max-w-[28rem] text-center">
            <CardHeader className="items-center">
              <CardTitle>Page not part of this share</CardTitle>
              <CardDescription>
                The page you're looking for isn't in the shared subtree.
              </CardDescription>
            </CardHeader>
          </Card>
        </div>
      </ShareLayout>
    )
  }
  const pageData = pageQuery.data
  if (!pageData) {
    return (
      <ShareLayout token={token} share={rootData.share}>
        <div className="flex-1 flex items-center justify-center p-[var(--space-7)]">
          <Card className="w-full max-w-[28rem] text-center">
            <CardHeader className="items-center">
              <CardTitle>Couldn't load page</CardTitle>
              <CardDescription>
                Please reload and try again.
              </CardDescription>
            </CardHeader>
          </Card>
        </div>
      </ShareLayout>
    )
  }

  return (
    <ShareReaderView
      token={token}
      share={rootData.share}
      pageId={pageData.page.id}
      pageTitle={pageData.page.title}
      pageBody={pageData.page.body}
      updatedAt={pageData.page.updated_at}
      inScopePageIds={inScope}
    />
  )
}
