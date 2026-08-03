import { useCallback, useMemo, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { applyPdfThemeParam } from '../../lib/theme'
import { buildWikilinkResolveIndex } from '../../lib/slug'
import { useShareTree, type SharePublicMeta } from '../../lib/queries/share'
import { Button } from '../ui/button'
import { DownloadPdfButton } from './DownloadPdfButton'
import { ReaderShell } from './ReaderShell'
import { ShareSidebar } from './ShareLayout'
import { useTelaHomeHref } from '../../lib/queries/host-context'

interface ShareReaderViewProps {
  token: string
  share: SharePublicMeta
  pageId: number
  pageTitle: string
  pageBody: string
  updatedAt: string
  /** In-scope page ids — drives wikilink scoping and the subtree sidebar. */
  inScopePageIds: Set<number>
}

// Public share view, rendered through the shared reading-mode shell. Same
// chrome-free reader as authenticated /read (TOC, typeface/size/theme controls,
// print-to-PDF), with share-specific wiring: the top-bar leading slot is the
// tela wordmark instead of a close button, the trailing slot offers Sign in,
// wikilinks stay inside the share (in-scope hop, out-of-scope no-op), and a
// multi-page share gets the in-scope subtree as the reader's left rail.
export function ShareReaderView({
  token,
  share,
  pageId,
  pageTitle,
  pageBody,
  updatedAt,
  inScopePageIds,
}: ShareReaderViewProps) {
  const navigate = useNavigate()
  const telaHome = useTelaHomeHref()
  // When gotenberg loads /share/<tok>?theme=dark for a PDF, apply it (once,
  // pre-paint). No-op for human viewers (no ?theme=), so their theme is kept.
  useState(() => applyPdfThemeParam())

  // Subtree nav — only fetched/shown for descendant-inclusive shares with more
  // than the root page. Same gating ShareLayout used.
  const treeEnabled = share.include_descendants
  const tree = useShareTree(token, treeEnabled)
  const pages = tree.data?.pages ?? []
  const showSidebar = treeEnabled && pages.length > 1

  // `[[Name]]` resolution within the shared subtree only — never resolve names
  // to pages outside the share (no leak). A single-page share has no tree, so
  // bracket links there stay non-navigable. `null` until the tree lands.
  const wikilinkResolveIndex = useMemo<Map<string, number> | null>(
    () => (tree.data ? buildWikilinkResolveIndex(tree.data.pages) : null),
    [tree.data],
  )

  const onNavigateWikilink = useCallback(
    (targetPageId: number) => {
      // In-scope links route within share-mode; out-of-scope ones are painted
      // as plain text by the decoration plugin and stay non-navigable here.
      if (!inScopePageIds.has(targetPageId)) return
      void navigate({
        to: '/share/$token/p/$pageId',
        params: { token, pageId: targetPageId },
      })
    },
    [navigate, token, inScopePageIds],
  )

  return (
    <ReaderShell
      pageId={pageId}
      title={pageTitle}
      body={pageBody}
      updatedAt={updatedAt}
      wikilinkMode="share"
      aliveWikilinkIds={inScopePageIds}
      wikilinkResolveIndex={wikilinkResolveIndex}
      onNavigateWikilink={onNavigateWikilink}
      sidebar={showSidebar ? <ShareSidebar token={token} pages={pages} /> : undefined}
      topbarLeading={
        /* Wordmark → apex marketing landing. Plain <a> (full nav) for the same
           reason as Sign in below: share-mode escapes the SPA rather than
           client-routing into an authed/app surface. */
        <a
          href={telaHome}
          aria-label="tela home"
          // shrink-0 so a narrow bar truncates the title beside it rather than
          // squeezing the wordmark's text out of its own box (see PublicReader).
          className="inline-block shrink-0 rounded-[var(--radius-xs)] font-[family-name:var(--font-sans)] text-[length:var(--text-base)] font-medium text-[var(--text-primary)] no-underline transition-opacity duration-[var(--duration-fast)] hover:opacity-70 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]"
        >
          tela
        </a>
      }
      sourceLabel={share.source_url.replace(/^https?:\/\//, '')}
      topbarTrailing={
        <>
          {/* The ".pdf on a share URL" trick: append .pdf to the page the
              viewer is on. credentials are irrelevant for a public share. */}
          <DownloadPdfButton url={`${window.location.pathname}.pdf`} themed compact />
          {/* Plain <a> rather than the router Link: a full page reload on sign-in
              is intentional so the post-login app boots cleanly outside the
              share-mode shell. */}
          <Button asChild variant="ghost" size="sm">
            <a href="/login">Sign in</a>
          </Button>
        </>
      }
    />
  )
}
