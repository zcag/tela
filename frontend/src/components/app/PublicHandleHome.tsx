import type { ReactNode } from 'react'
import { Link } from '@tanstack/react-router'
import {
  type ByHandleResponse,
  type ByHandleSpace,
} from '../../lib/queries/public'
import { useHeadMeta } from '../../lib/useHeadMeta'
import { PublicPageShell } from './blog/PublicPageShell'
import { PublicMasthead, MetaDot } from './blog/PublicMasthead'
import { SpaceCard } from './blog/SpaceCard'
import { PostCard } from './blog/PostCard'

// The GitHub-style handle home at /{handle} — works identically for a user or
// an org. A masthead (name + bio + @handle), a "Latest" strip of the newest
// posts across the account's spaces, then its public spaces as cards (each
// links into that space at /{handle}/{space-slug}). Same no-login chrome as
// /discover and the space front page, so the whole public surface reads as one
// site.
export function PublicHandleHome({ data }: { data: ByHandleResponse }) {
  const { handle, name, bio, spaces, kind } = data
  const posts = data.posts ?? []

  useHeadMeta({
    title: `${name} — tela`,
    description: bio || `${name} (@${handle}) on tela.`,
    canonicalPath: `/${handle}`,
    ogType: kind === 'org' ? 'website' : 'profile',
  })

  return (
    <PublicPageShell>
      <PublicMasthead
        title={name}
        avatarSeed={handle}
        standfirst={bio || undefined}
        meta={
          <>
            <span>@{handle}</span>
            <MetaDot />
            <span>
              {spaces.length} {spaces.length === 1 ? 'space' : 'spaces'}
            </span>
          </>
        }
      />

      {posts.length > 0 ? (
        <SectionHeading className="mt-[var(--space-8)]">Latest</SectionHeading>
      ) : null}
      {posts.length > 0 ? (
        <div className="mt-[var(--space-4)] grid grid-cols-1 gap-[var(--space-5)] sm:grid-cols-2">
          {posts.map((p) => (
            <PostCard
              key={`${p.space_id}-${p.id}`}
              spaceId={p.space_id}
              handle={handle}
              spaceSlug={p.space_slug}
              post={p}
            />
          ))}
        </div>
      ) : null}

      <SectionHeading className="mt-[var(--space-8)]">Spaces</SectionHeading>
      <div className="mt-[var(--space-4)]">
        {spaces.length === 0 ? (
          <p className="text-[length:var(--text-sm)] text-[var(--text-muted)]">
            Nothing published yet.
          </p>
        ) : (
          <ul className="grid list-none grid-cols-1 gap-[var(--space-5)] p-0 sm:grid-cols-2 lg:grid-cols-3">
            {spaces.map((s) => (
              <li key={s.id}>
                <HandleSpaceCard handle={handle} space={s} />
              </li>
            ))}
          </ul>
        )}
      </div>
    </PublicPageShell>
  )
}

// A small uppercase section label, matching the public surfaces' meta type.
function SectionHeading({
  children,
  className,
}: {
  children: ReactNode
  className?: string
}) {
  return (
    <h2
      className={`m-0 text-[length:var(--text-xs)] font-semibold uppercase tracking-[0.08em] text-[var(--text-muted)] ${className ?? ''}`}
    >
      {children}
    </h2>
  )
}

// One public space on the handle home → links to /{handle}/{space-slug} (the
// unified handle route); no owner byline since we're already under the handle.
function HandleSpaceCard({
  handle,
  space,
}: {
  handle: string
  space: ByHandleSpace
}) {
  return (
    <SpaceCard
      name={space.name}
      seed={space.slug || space.name}
      description={space.description}
      pageCount={space.page_count}
      updatedAt={space.updated_at}
      renderTitleLink={({ className, children }) => (
        <Link
          to="/$handle/$spaceSlug"
          params={{ handle, spaceSlug: space.slug }}
          className={className}
        >
          {children}
        </Link>
      )}
    />
  )
}
