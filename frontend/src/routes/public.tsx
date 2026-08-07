import { useEffect } from 'react'
import { Navigate, useNavigate, useParams, useSearch } from '@tanstack/react-router'
import {
  usePublicByHandle,
  usePublicByHandleSpace,
  usePublicSpace,
  usePublicSpacePage,
  usePublicSpaceTree,
  type DiscoverSort,
} from '../lib/queries/public'
import { pageSlug } from '../lib/slug'
import { PublicDiscover } from '../components/app/PublicDiscover'
import { PublicHandleHome } from '../components/app/PublicHandleHome'
import { PublicReaderView } from '../components/app/PublicReader'
import { PublicSpaceIndex } from '../components/app/PublicSpaceIndex'
import { ThemeSwitcher } from '../components/ThemeSwitcher'
import { BrandLogo } from '../components/BrandLogo'
import { Card, CardDescription, CardHeader, CardTitle } from '../components/ui/card'
import { useHostContext, useTelaHomeHref } from '../lib/queries/host-context'

// Public-space reader route — child of `rootRoute` (NOT appLayoutRoute) because
// it's unauthenticated: a logged-out reader views a public space here. Data
// comes from the /api/public/ endpoints (raw fetch, never `api()`), so a miss
// is a plain 404, not a bounce to /login.

function PublicShell({ children }: { children: React.ReactNode }) {
  const telaHome = useTelaHomeHref()
  // On an org custom domain, BrandLogo white-labels the public reader to the
  // org (logo / name) and the brand links to the org root; on the canonical
  // host it's the tela wordmark → marketing landing.
  const org = useHostContext().data?.org ?? null
  const brandHref = org ? '/' : telaHome
  const brandLabel = org ? `${org.name} home` : 'tela home'
  return (
    <div className="min-h-dvh flex flex-col bg-[var(--surface-1)] text-[var(--text-primary)]">
      <header className="flex items-center justify-between px-[var(--space-6)] py-[var(--space-3)] border-b border-[var(--border-subtle)] shrink-0">
        <h1 className="m-0 text-[length:var(--text-lg)] leading-[var(--leading-tight)] font-[family-name:var(--font-sans)]">
          <a
            href={brandHref}
            aria-label={brandLabel}
            className="inline-flex items-center rounded-[var(--radius-xs)] no-underline transition-opacity duration-[var(--duration-fast)] hover:opacity-70 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]"
          >
            <BrandLogo size={20} />
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

function PublicUnavailable({
  message = 'This page is not publicly available.',
}: {
  message?: string
}) {
  return (
    <PublicShell>
      <Card className="w-full max-w-[24rem]">
        <CardHeader>
          <CardTitle className="text-[length:var(--text-2xl)]">Not available</CardTitle>
          <CardDescription>{message}</CardDescription>
        </CardHeader>
      </Card>
    </PublicShell>
  )
}

// The cross-tenant public-space directory: /discover. Sort + pagination live in
// the URL (?sort=&offset=) so a view is shareable and back-button friendly.
export function PublicDiscoverRoute() {
  const { sort, offset } = useSearch({ from: '/discover' })
  const navigate = useNavigate({ from: '/discover' })
  return (
    <PublicDiscover
      sort={sort}
      offset={offset}
      onSort={(s: DiscoverSort) =>
        void navigate({ search: { sort: s, offset: 0 } })
      }
      onOffset={(o: number) =>
        void navigate({ search: (prev) => ({ ...prev, offset: o }) })
      }
    />
  )
}

// Legacy /u/{handle} → /{handle}. Handles are now unified GitHub-style at the
// root, so the old author URL just redirects (keeps existing links working).
export function PublicUserRoute() {
  const { username } = useParams({ from: '/u/$username' })
  return <Navigate to="/$handle" params={{ handle: username }} replace />
}

// The unified handle home at /{handle} — resolves a user OR an org handle to its
// public spaces and renders one Home surface for both.
export function PublicHandleRoute() {
  const { handle } = useParams({ from: '/$handle' })
  const query = usePublicByHandle(handle)

  if (query.isLoading) {
    return (
      <PublicShell>
        <p role="status" className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
          Loading…
        </p>
      </PublicShell>
    )
  }
  if (query.error || !query.data) {
    return <PublicUnavailable message={`Nothing public lives at @${handle}.`} />
  }
  return <PublicHandleHome data={query.data} />
}

// A public space addressed by handle + slug: /{handle}/{space-slug}. Resolves the
// space via the by-handle endpoint, then renders the EXISTING public-space front
// page (its tree is fetched by id, same as the /public/spaces/{id} route).
export function PublicHandleSpaceRoute() {
  const { handle, spaceSlug } = useParams({ from: '/$handle/$spaceSlug' })
  const spaceQuery = usePublicByHandleSpace(handle, spaceSlug)
  const spaceId = spaceQuery.data?.space.id
  const treeQuery = usePublicSpaceTree(spaceId ?? -1, spaceId != null)

  if (spaceQuery.isLoading || (spaceId != null && treeQuery.isLoading)) {
    return (
      <PublicShell>
        <p role="status" className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
          Loading…
        </p>
      </PublicShell>
    )
  }
  if (spaceQuery.error || !spaceQuery.data) {
    return <PublicUnavailable message="This space is not publicly available." />
  }
  return (
    <PublicSpaceIndex
      space={spaceQuery.data.space}
      pages={treeQuery.data?.pages ?? []}
    />
  )
}

// The curated front page of a public space: /public/spaces/{id}.
export function PublicSpaceIndexRoute() {
  const { spaceId } = useParams({ from: '/public/spaces/$spaceId' })
  const spaceQuery = usePublicSpace(spaceId)
  const treeQuery = usePublicSpaceTree(spaceId, spaceQuery.data != null)

  if (spaceQuery.isLoading || (spaceQuery.data && treeQuery.isLoading)) {
    return (
      <PublicShell>
        <p role="status" className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
          Loading…
        </p>
      </PublicShell>
    )
  }
  if (spaceQuery.error || !spaceQuery.data) {
    return <PublicUnavailable message="This space is not publicly available." />
  }
  return (
    <PublicSpaceIndex
      space={spaceQuery.data.space}
      pages={treeQuery.data?.pages ?? []}
    />
  )
}

export function PublicReaderRoute() {
  const { spaceId, pageId } = useParams({
    from: '/public/spaces/$spaceId/pages/$pageId/{-$slug}',
  })
  const spaceQuery = usePublicSpace(spaceId)
  const pageQuery = usePublicSpacePage(spaceId, pageId, spaceQuery.data != null)

  // Canonicalise the address to /.../{slug} once the title is known. replaceState
  // (not router nav) so a slug change never remounts the reader.
  const pageTitle = pageQuery.data?.page.title ?? ''
  useEffect(() => {
    if (!pageQuery.data) return
    const slug = pageSlug(pageTitle)
    const base = `/public/spaces/${spaceId}/pages/${pageId}`
    const desired = slug ? `${base}/${slug}` : base
    if (window.location.pathname !== desired) {
      window.history.replaceState(
        window.history.state,
        '',
        desired + window.location.search + window.location.hash,
      )
    }
  }, [pageQuery.data, pageTitle, spaceId, pageId])

  if (spaceQuery.isLoading || (spaceQuery.data && pageQuery.isLoading)) {
    return (
      <PublicShell>
        <p role="status" className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
          Loading…
        </p>
      </PublicShell>
    )
  }

  // Either the space isn't public, or the page isn't in it → one neutral 404
  // surface (never confirm a private space/page exists).
  if (spaceQuery.error || !spaceQuery.data || pageQuery.error || !pageQuery.data) {
    return <PublicUnavailable />
  }

  return (
    <PublicReaderView
      space={spaceQuery.data.space}
      pageId={pageQuery.data.page.id}
      pageTitle={pageQuery.data.page.title}
      pageBody={pageQuery.data.page.body}
      pageProps={pageQuery.data.page.props}
      createdAt={pageQuery.data.page.created_at}
      updatedAt={pageQuery.data.page.updated_at}
      author={pageQuery.data.page.author}
      editor={pageQuery.data.page.editor}
    />
  )
}

// A page addressed by the pretty handle URL: /{handle}/{space-slug}/{id}/{slug}.
// Resolves the space by handle+slug, then renders the SAME reader as the id
// route. The id resolves the page, so a stale slug (shared before a retitle)
// still lands on the right page — it is only canonicalised in the address bar.
export function PublicHandlePageRoute() {
  const { handle, spaceSlug, pageId } = useParams({
    from: '/$handle/$spaceSlug/$pageId/{-$slug}',
  })
  const spaceQuery = usePublicByHandleSpace(handle, spaceSlug)
  const spaceId = spaceQuery.data?.space.id
  const pageQuery = usePublicSpacePage(spaceId ?? -1, pageId, spaceId != null)

  const pageTitle = pageQuery.data?.page.title ?? ''
  useEffect(() => {
    if (!pageQuery.data) return
    const slug = pageSlug(pageTitle)
    const base = `/${handle}/${spaceSlug}/${pageId}`
    const desired = slug ? `${base}/${slug}` : base
    if (window.location.pathname !== desired) {
      window.history.replaceState(
        window.history.state,
        '',
        desired + window.location.search + window.location.hash,
      )
    }
  }, [pageQuery.data, pageTitle, handle, spaceSlug, pageId])

  if (spaceQuery.isLoading || (spaceId != null && pageQuery.isLoading)) {
    return (
      <PublicShell>
        <p role="status" className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
          Loading…
        </p>
      </PublicShell>
    )
  }
  if (spaceQuery.error || !spaceQuery.data || pageQuery.error || !pageQuery.data) {
    return <PublicUnavailable />
  }
  return (
    <PublicReaderView
      space={spaceQuery.data.space}
      pageId={pageQuery.data.page.id}
      pageTitle={pageQuery.data.page.title}
      pageBody={pageQuery.data.page.body}
      pageProps={pageQuery.data.page.props}
      createdAt={pageQuery.data.page.created_at}
      updatedAt={pageQuery.data.page.updated_at}
      author={pageQuery.data.page.author}
      editor={pageQuery.data.page.editor}
    />
  )
}
