import { FileText } from 'lucide-react'
import type { CommandItem } from '../components/ui/command'
import { HighlightedSnippet } from './highlightSnippet'
import { pageSlug } from './slug'
import { router } from '../routes/router'

// Common shape for both tier-1 (orama TitleHit) and tier-2 (SearchResult) rows
// in the command palette. Keeping the factory's input narrow makes the call
// sites at the host explicit about which fields they're mapping.
export interface PageHit {
  pageId: number
  spaceId: number
  title: string
  breadcrumb: string[]
  // Tier-2 only: the hit is in a published space the user isn't a member of.
  // Such a row must open in the no-login reader — the authed route 403s.
  // Tier-1/tier-3 hits come from member-only indexes, so they omit it.
  isPublic?: boolean
  // Canonical reader path for a public hit, straight from the server.
  publicPath?: string
}

export interface PageHitItemOptions {
  // Prefix namespaces the row id so tier-1 and tier-2 hits for the same page
  // don't collide in cmdk's value space.
  idPrefix: string
  // Tier-2 only — backend-supplied snippet with literal <mark> delimiters.
  // Rendered via HighlightedSnippet (XSS-safe) when present.
  snippet?: string
  // Space name, prefixed onto the breadcrumb line ("Space · Parent / Child")
  // so a title match is attributable to its space at a glance. Omit to keep
  // the bare parent chain.
  spaceName?: string
}

// Compose the breadcrumb line: the space name (when known) followed by the
// parent-page chain, e.g. "Docs · Guides / Payments". Either part may be
// absent — space-only ("Docs"), chain-only ("Guides / Payments"), or neither.
function composeBreadcrumb(
  chain: string[],
  spaceName?: string,
): string | undefined {
  const parents = chain.length > 0 ? chain.join(' / ') : ''
  if (spaceName && parents) return `${spaceName} · ${parents}`
  return spaceName || parents || undefined
}

// navigateToPage is the one imperative page-open used across the palette, Ask
// citations, search results and the space views. `isPublic` routes a hit the
// caller can only read because its space is published to the no-login reader —
// sending it to the authed route instead lands them on a 403 wall for content
// that is deliberately world-readable.
export function navigateToPage(
  spaceId: number,
  pageId: number,
  opts?: { isPublic?: boolean; title?: string; publicPath?: string },
) {
  if (opts?.isPublic) {
    // Prefer the server's canonical /{handle}/{space-slug}/{id}/{slug}; the id
    // route below is the fallback when the caller has no path (it works, but
    // canonicalizes elsewhere, so the address bar would show the old form).
    if (opts.publicPath) {
      void router.navigate({ to: opts.publicPath })
      return
    }
    void router.navigate({
      to: '/public/spaces/$spaceId/pages/$pageId/{-$slug}',
      params: { spaceId, pageId, slug: pageSlug(opts.title ?? '') || undefined },
    })
    return
  }
  void router.navigate({
    to: '/spaces/$spaceId/pages/$pageId/{-$slug}',
    params: { spaceId, pageId, slug: undefined },
  })
}

export function pageHitToCommandItem(
  hit: PageHit,
  opts: PageHitItemOptions,
): CommandItem {
  return {
    id: `${opts.idPrefix}:${hit.pageId}`,
    title: hit.title || 'Untitled',
    subtitle:
      opts.snippet != null ? (
        <HighlightedSnippet snippet={opts.snippet} />
      ) : undefined,
    breadcrumb: composeBreadcrumb(hit.breadcrumb, opts.spaceName),
    icon: <FileText aria-hidden width={14} height={14} />,
    onSelect: () =>
      navigateToPage(hit.spaceId, hit.pageId, {
        isPublic: hit.isPublic,
        publicPath: hit.publicPath,
        title: hit.title,
      }),
  }
}
