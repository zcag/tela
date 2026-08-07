import type { ReactNode } from 'react'
import { Link } from '@tanstack/react-router'
import { FileText } from 'lucide-react'
import { relativeTimeFromSqlite } from '../../../lib/relativeTime'
import { Monogram } from './Monogram'
import { MetaDot } from './PublicMasthead'

// One public space in a directory grid — used by /discover and the handle home.
// The whole card is a stretched link into the space; the only thing that varies
// between surfaces is *where* the title links (a route-typed `Link` differs per
// caller) and whether an owner byline shows, so those are the two props. Chrome,
// monogram, description, and the meta row are defined once here.
export function SpaceCard({
  name,
  seed,
  description,
  pageCount,
  updatedAt,
  owner,
  renderTitleLink,
}: {
  name: string
  /** Monogram tint seed — usually the space slug. */
  seed?: string
  description?: string
  pageCount: number
  updatedAt?: string
  /** Owner handle → byline link to /u/{handle}. Omitted on a handle home. */
  owner?: string
  /** The card's stretched title link; caller supplies the route-typed Link. */
  renderTitleLink: (props: { className: string; children: ReactNode }) => ReactNode
}) {
  const safeName = name || 'Untitled space'
  return (
    <div
      className={[
        'group relative flex h-full flex-col gap-[var(--space-3)] rounded-[var(--radius-lg)] border border-[var(--border-subtle)]',
        'bg-[var(--surface-1)] p-[var(--space-5)] transition-all duration-[var(--duration-fast)]',
        'hover:border-[var(--border-strong)] hover:bg-[var(--surface-2)] focus-within:border-[var(--border-strong)]',
      ].join(' ')}
    >
      <div className="flex items-center gap-[var(--space-3)]">
        <Monogram name={safeName} seed={seed ?? safeName} size="sm" />
        {renderTitleLink({
          className: [
            'min-w-0 font-[family-name:var(--font-sans)] text-[length:var(--text-lg)] font-semibold leading-[var(--leading-tight)]',
            'tracking-[-0.01em] text-[var(--text-primary)] no-underline transition-colors duration-[var(--duration-fast)]',
            // Stretched link — the whole card is clickable; nested links lift above.
            'after:absolute after:inset-0 hover:text-[var(--accent)]',
            'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]',
          ].join(' '),
          children: <span className="line-clamp-2">{safeName}</span>,
        })}
      </div>

      {description ? (
        <p className="m-0 line-clamp-3 text-[length:var(--text-sm)] leading-[var(--leading-normal)] text-[var(--text-muted)]">
          {description}
        </p>
      ) : null}

      <div className="mt-auto flex flex-wrap items-center gap-x-[var(--space-2)] gap-y-[var(--space-1)] pt-[var(--space-2)] text-[length:var(--text-xs)] text-[var(--text-muted)]">
        {owner ? (
          <>
            <Link
              to="/$handle"
              params={{ handle: owner }}
              // relative z-index lifts this above the card's stretched ::after.
              className="relative z-10 font-medium text-[var(--text-muted)] no-underline hover:text-[var(--accent)]"
            >
              @{owner}
            </Link>
            <MetaDot />
          </>
        ) : null}
        <span className="inline-flex items-center gap-[var(--space-1)]">
          <FileText size="0.9em" aria-hidden />
          {pageCount} {pageCount === 1 ? 'page' : 'pages'}
        </span>
        {updatedAt ? (
          <>
            <MetaDot />
            <span>Updated {relativeTimeFromSqlite(updatedAt)}</span>
          </>
        ) : null}
      </div>
    </div>
  )
}
