import { useMemo, useState } from 'react'
import { Link, useSearch } from '@tanstack/react-router'
import { Search } from 'lucide-react'
import {
  type PublicPageNode,
  type PublicSpacePayload,
} from '../../lib/queries/public'
import { useHeadMeta } from '../../lib/useHeadMeta'
import { blogChip, sectionLabel, sortByNewest, topLevelPosts } from '../../lib/blog'
import { Input } from '../ui/input'
import { PublicPageShell } from './blog/PublicPageShell'
import { PublicMasthead, MetaDot } from './blog/PublicMasthead'
import { PostCard } from './blog/PostCard'

interface PublicSpaceIndexProps {
  space: PublicSpacePayload
  pages: PublicPageNode[]
}

// The curated front page of a public space — a blog-style index. Top-level pages
// are the "posts" (nested pages are sub-sections, reached by navigating in),
// newest published first. The newest gets a featured lead card; the rest fall
// into a grid. A tag bar filters the list (?tag=, shareable). Chrome mirrors the
// reader/author surfaces so it all reads as one site.
export function PublicSpaceIndex({ space, pages }: PublicSpaceIndexProps) {
  // strict: false — this index renders under BOTH /public/spaces/$spaceId and
  // the unified /$handle/$spaceSlug route, so it can't bind to one route's
  // search schema. The tag filter is a plain optional string either way.
  const search = useSearch({ strict: false }) as { tag?: string }
  const activeTag =
    typeof search.tag === 'string' && search.tag.trim() ? search.tag.trim() : undefined

  const posts = useMemo(() => sortByNewest(topLevelPosts(pages)), [pages])

  const allTags = useMemo(() => {
    const set = new Set<string>()
    for (const p of posts) for (const t of p.tags ?? []) set.add(t)
    return [...set].sort((a, b) => a.localeCompare(b))
  }, [posts])

  // Free-text post filter — client-side over the already-loaded posts (title,
  // excerpt, tags). Local state, not the URL: it's a quick browse aid, while the
  // tag filter stays shareable. Both narrow the same list.
  const [query, setQuery] = useState('')
  const q = query.trim().toLowerCase()
  const shown = useMemo(() => {
    let list = activeTag ? posts.filter((p) => p.tags?.includes(activeTag)) : posts
    if (q) {
      list = list.filter((p) =>
        [p.title, p.excerpt, ...(p.tags ?? [])]
          .join(' ')
          .toLowerCase()
          .includes(q),
      )
    }
    return list
  }, [posts, activeTag, q])

  const feedUrl = `/api/public/spaces/${space.id}/feed.xml`

  useHeadMeta({
    title: `${space.name}${activeTag ? ` · ${activeTag}` : ''} — tela`,
    description: space.description || `Posts from ${space.name}.`,
    canonicalPath: `/public/spaces/${space.id}`,
    image: `/api/public/spaces/${space.id}/og.png`,
    ogType: 'website',
    feedHref: feedUrl,
  })

  // Featured lead only on the unfiltered view; any active filter → a flat grid.
  const [featured, ...rest] = shown
  const showFeatured = !activeTag && !q && featured

  return (
    <PublicPageShell>
      <PublicMasthead
        title={space.name}
        avatarSeed={space.slug || space.name}
        standfirst={space.description || undefined}
        meta={
          <>
            {space.owner_handle ? (
              <>
                <span>
                  by{' '}
                  <Link
                    to="/$handle"
                    params={{ handle: space.owner_handle }}
                    className="font-medium text-[var(--text-primary)] no-underline hover:text-[var(--accent)]"
                  >
                    @{space.owner_handle}
                  </Link>
                </span>
                <MetaDot />
              </>
            ) : null}
            <span>
              {posts.length} {posts.length === 1 ? 'post' : 'posts'}
            </span>
            <MetaDot />
            <a
              href={feedUrl}
              className="text-[var(--text-muted)] no-underline hover:text-[var(--accent)]"
            >
              RSS
            </a>
          </>
        }
      />

      {posts.length > 3 ? (
        <div className="mt-[var(--space-6)] relative max-w-[22rem]">
          <Search
            width={15}
            height={15}
            aria-hidden
            className="pointer-events-none absolute left-[var(--space-3)] top-1/2 -translate-y-1/2 text-[var(--text-muted)]"
          />
          <Input
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={`Search ${posts.length} posts…`}
            aria-label="Search posts"
            className="pl-[calc(var(--space-3)*2+15px)]"
          />
        </div>
      ) : null}

      {allTags.length > 0 ? (
        <TagBar spaceId={space.id} tags={allTags} active={activeTag} />
      ) : null}

      {posts.length === 0 ? (
        <p className="mt-[var(--space-8)] text-[length:var(--text-sm)] text-[var(--text-muted)]">
          Nothing published here yet.
        </p>
      ) : shown.length === 0 ? (
        <p className="mt-[var(--space-6)] text-[length:var(--text-sm)] text-[var(--text-muted)]">
          {q
            ? `No posts match “${query.trim()}”.`
            : `No posts tagged “${activeTag}”.`}
        </p>
      ) : (
        <div className="mt-[var(--space-6)] flex flex-col gap-[var(--space-5)]">
          {showFeatured ? (
            <PostCard
              spaceId={space.id}

              handle={space.owner_handle}

              spaceSlug={space.slug}
              post={featured}
              featured
              headingLevel={2}
              sectionLabel={sectionLabel(pages, featured.id)}
            />
          ) : null}
          {(() => {
            const grid = showFeatured ? rest : shown
            if (grid.length === 0) return null
            // A lone trailing card spans full width rather than sitting
            // half-empty in a 2-col grid.
            if (showFeatured && grid.length === 1)
              return (
                <PostCard
                  spaceId={space.id}

                  handle={space.owner_handle}

                  spaceSlug={space.slug}
                  post={grid[0]}
                  headingLevel={2}
                  sectionLabel={sectionLabel(pages, grid[0].id)}
                />
              )
            return (
              <div className="grid grid-cols-1 gap-[var(--space-5)] sm:grid-cols-2">
                {grid.map((p) => (
                  <PostCard
                    key={p.id}
                    spaceId={space.id}

                    handle={space.owner_handle}

                    spaceSlug={space.slug}
                    post={p}
                    headingLevel={2}
                    sectionLabel={sectionLabel(pages, p.id)}
                  />
                ))}
              </div>
            )
          })()}
        </div>
      )}
    </PublicPageShell>
  )
}

// A row of tag filter chips. "All" clears the filter; each tag links to
// ?tag=<t>. The active chip is highlighted. Links keep the filter shareable and
// back-button friendly.
function TagBar({
  spaceId,
  tags,
  active,
}: {
  spaceId: number
  tags: string[]
  active?: string
}) {
  return (
    <div className="mt-[var(--space-6)] flex flex-wrap items-center gap-[var(--space-2)]">
      <Link
        to="/public/spaces/$spaceId"
        params={{ spaceId }}
        search={{ tag: undefined }}
        className={blogChip(!active)}
      >
        All
      </Link>
      {tags.map((t) => (
        <Link
          key={t}
          to="/public/spaces/$spaceId"
          params={{ spaceId }}
          search={{ tag: t }}
          className={blogChip(active === t)}
        >
          {t}
        </Link>
      ))}
    </div>
  )
}
