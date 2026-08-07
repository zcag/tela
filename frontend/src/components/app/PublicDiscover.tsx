import { Link } from '@tanstack/react-router'
import { Compass, FrownIcon } from 'lucide-react'
import {
  DISCOVER_PAGE_SIZE,
  usePublicDiscover,
  type DiscoverSort,
  type DiscoverSpace,
} from '../../lib/queries/public'
import { useHeadMeta } from '../../lib/useHeadMeta'
import { blogChip } from '../../lib/blog'
import { Button } from '../ui/button'
import { EmptyState } from '../ui/empty-state'
import { PublicPageShell } from './blog/PublicPageShell'
import { PublicMasthead } from './blog/PublicMasthead'
import { SpaceCard } from './blog/SpaceCard'

// The cross-tenant public-space directory at /discover — a login-free "network"
// view of every public space on the instance. Same no-login chrome as the space
// front page and /u/{handle}, so the whole public surface reads as one site.
// Data comes from GET /api/public/discover via a raw-fetch hook (never api()).
export function PublicDiscover({
  sort,
  offset,
  onSort,
  onOffset,
}: {
  sort: DiscoverSort
  offset: number
  onSort: (s: DiscoverSort) => void
  onOffset: (o: number) => void
}) {
  const query = usePublicDiscover(sort, offset)
  const spaces = query.data?.spaces ?? []
  const page = Math.floor(offset / DISCOVER_PAGE_SIZE) + 1
  // The directory is open-ended; a full page implies there may be more.
  const hasNext = spaces.length === DISCOVER_PAGE_SIZE
  const hasPrev = offset > 0

  useHeadMeta({
    title: 'Discover — tela',
    description: 'Browse public spaces published on tela.',
    canonicalPath: '/discover',
    ogType: 'website',
  })

  return (
    <PublicPageShell>
      <PublicMasthead
        title="Discover"
        avatarSeed="discover"
        standfirst="Public spaces published on tela — browse the network."
        meta={<SortToggle sort={sort} onSort={onSort} disabled={query.isLoading} />}
      />

      <div className="mt-[var(--space-7)]">
            {query.isLoading ? (
              <p
                role="status"
                className="text-[length:var(--text-sm)] text-[var(--text-muted)]"
              >
                Loading…
              </p>
            ) : query.error ? (
              <EmptyState
                icon={FrownIcon}
                tone="danger"
                title="Couldn’t load spaces"
                description="Something went wrong fetching the directory. Try again in a moment."
                actions={
                  <Button variant="secondary" onClick={() => void query.refetch()}>
                    Retry
                  </Button>
                }
              />
            ) : spaces.length === 0 ? (
              <EmptyState
                icon={Compass}
                title={offset > 0 ? 'No more spaces' : 'Nothing published yet'}
                description={
                  offset > 0
                    ? 'You’ve reached the end of the directory.'
                    : 'When people publish public spaces, they’ll show up here.'
                }
                actions={
                  offset > 0 ? (
                    <Button
                      variant="secondary"
                      onClick={() => onOffset(Math.max(0, offset - DISCOVER_PAGE_SIZE))}
                    >
                      Back
                    </Button>
                  ) : undefined
                }
              />
            ) : (
              <>
                <ul className="grid list-none grid-cols-1 gap-[var(--space-5)] p-0 sm:grid-cols-2 lg:grid-cols-3">
                  {spaces.map((s) => (
                    <li key={s.id}>
                      <DiscoverSpaceCard space={s} />
                    </li>
                  ))}
                </ul>

                {(hasPrev || hasNext) && (
                  <nav
                    aria-label="Pagination"
                    className="mt-[var(--space-7)] flex items-center justify-between"
                  >
                    <Button
                      variant="secondary"
                      size="sm"
                      disabled={!hasPrev}
                      onClick={() => onOffset(Math.max(0, offset - DISCOVER_PAGE_SIZE))}
                    >
                      Previous
                    </Button>
                    <span className="text-[length:var(--text-sm)] text-[var(--text-muted)]">
                      Page {page}
                    </span>
                    <Button
                      variant="secondary"
                      size="sm"
                      disabled={!hasNext}
                      onClick={() => onOffset(offset + DISCOVER_PAGE_SIZE)}
                    >
                      Next
                    </Button>
                  </nav>
                )}
              </>
            )}
      </div>
    </PublicPageShell>
  )
}

// Recent / Popular sort toggle, rendered in the masthead meta row. Mirrors the
// TagBar chip styling on the space index so the public surfaces stay consistent.
function SortToggle({
  sort,
  onSort,
  disabled,
}: {
  sort: DiscoverSort
  onSort: (s: DiscoverSort) => void
  disabled?: boolean
}) {
  const opts: { value: DiscoverSort; label: string }[] = [
    { value: 'recent', label: 'Recent' },
    { value: 'popular', label: 'Popular' },
  ]
  return (
    <div role="group" aria-label="Sort" className="flex items-center gap-[var(--space-2)]">
      {opts.map((o) => (
        <button
          key={o.value}
          type="button"
          disabled={disabled}
          aria-pressed={sort === o.value}
          onClick={() => onSort(o.value)}
          className={`${blogChip(sort === o.value)} disabled:opacity-50`}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}

// One space in the directory grid → links into the space's public reader, with
// an owner byline. Just wires DiscoverSpace into the shared SpaceCard.
function DiscoverSpaceCard({ space }: { space: DiscoverSpace }) {
  return (
    <SpaceCard
      name={space.name}
      seed={space.slug || space.name}
      description={space.description}
      pageCount={space.page_count}
      updatedAt={space.updated_at}
      owner={space.owner_handle}
      renderTitleLink={({ className, children }) =>
        // Prefer the canonical /{handle}/{space-slug} the server derived; the id
        // route stays as the fallback for an ownerless space.
        space.public_path ? (
          <a href={space.public_path} className={className}>
            {children}
          </a>
        ) : (
          <Link
            to="/public/spaces/$spaceId"
            params={{ spaceId: space.id }}
            search={{ tag: undefined }}
            className={className}
          >
            {children}
          </Link>
        )
      }
    />
  )
}
