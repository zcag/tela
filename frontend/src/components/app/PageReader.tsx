import { useCallback, useMemo } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { X } from 'lucide-react'
import { ApiError } from '../../lib/api'
import { useAllPages, usePage } from '../../lib/queries/pages'
import { buildWikilinkResolveIndex } from '../../lib/slug'
import { Button } from '../ui/button'
import { DownloadPdfButton } from './DownloadPdfButton'
import { ReaderShell } from './ReaderShell'
import { pageSummary } from './SummaryHint'
import { AttachmentStrip } from './AttachmentStrip'

interface PageReaderProps {
  spaceId: number
  pageId: number
}

export function PageReader({ spaceId, pageId }: PageReaderProps) {
  const page = usePage(pageId)

  if (page.isError) {
    const status = page.error instanceof ApiError ? page.error.status : null
    return <ReaderMessage spaceId={spaceId} notFound={status === 404} />
  }
  if (page.isLoading || !page.data) return <ReaderLoading />
  if (page.data.space_id !== spaceId) {
    return <ReaderMessage spaceId={spaceId} notFound />
  }

  return (
    <ReadModeView
      key={page.data.id}
      spaceId={spaceId}
      pageId={page.data.id}
      title={page.data.title}
      summary={pageSummary(page.data.props)}
      body={page.data.body}
      updatedAt={page.data.updated_at}
    />
  )
}

interface ReadModeViewProps {
  spaceId: number
  pageId: number
  title: string
  summary: string | null
  body: string
  updatedAt: string
}

// Authenticated reading mode. Rendered as a full-bleed overlay over the page at
// `?view=read`. Wikilinks keep the reader open by hopping to the target page
// with `?view=read`; Esc / the close button drop the param and fall back to the
// page's normal view.
function ReadModeView({ spaceId, pageId, title, summary, body, updatedAt }: ReadModeViewProps) {
  const navigate = useNavigate()
  // Close target: the same canonical page URL minus `?view=read`. Empty `search`
  // clears the param (the route's validateSearch drops everything it doesn't
  // know), so this lands on the default read view rather than the editor.
  const closeRoute = {
    to: '/spaces/$spaceId/pages/$pageId/{-$slug}' as const,
    params: { spaceId, pageId, slug: undefined },
    search: {},
  }

  // Alive page ids power broken-wikilink rendering; the full list also resolves
  // a clicked wikilink's target space so navigation stays inside reading mode.
  const allPages = useAllPages()
  const aliveIds = useMemo<Set<number> | null>(
    () => (allPages.data ? new Set(allPages.data.map((p) => p.id)) : null),
    [allPages.data],
  )
  const spaceByPageId = useMemo(() => {
    const m = new Map<number, number>()
    for (const p of allPages.data ?? []) m.set(p.id, p.space_id)
    return m
  }, [allPages.data])
  // `[[Name]]` resolution, scoped to this page's space (backend parity).
  const wikilinkResolveIndex = useMemo<Map<string, number> | null>(
    () =>
      allPages.data
        ? buildWikilinkResolveIndex(
            allPages.data.filter((p) => p.space_id === spaceId),
          )
        : null,
    [allPages.data, spaceId],
  )

  const onNavigateWikilink = useCallback(
    (targetPageId: number) => {
      const sp = spaceByPageId.get(targetPageId)
      if (sp == null) return
      void navigate({
        to: '/spaces/$spaceId/pages/$pageId/{-$slug}',
        params: { spaceId: sp, pageId: targetPageId, slug: undefined },
        search: { view: 'read' },
      })
    },
    [navigate, spaceByPageId],
  )

  const onEscape = useCallback(() => {
    void navigate(closeRoute)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [navigate, spaceId, pageId])

  return (
    <ReaderShell
      pageId={pageId}
      title={title}
      summary={summary}
      body={body}
      updatedAt={updatedAt}
      wikilinkMode="edit"
      aliveWikilinkIds={aliveIds}
      wikilinkResolveIndex={wikilinkResolveIndex}
      enableLinkPreview
      onNavigateWikilink={onNavigateWikilink}
      onEscape={onEscape}
      attachmentStrip={<AttachmentStrip pageId={pageId} />}
      // Phone: the "Download PDF" label ate the bar and squeezed the title down
      // to an ellipsis — `compact` makes it icon-only there.
      topbarTrailing={<DownloadPdfButton url={`/api/pages/${pageId}/pdf`} themed compact />}
      topbarLeading={
        <Button
          asChild
          variant="ghost"
          size="sm"
          aria-label="Close reading mode"
          className="h-[var(--space-8)] w-[var(--space-8)] p-0"
        >
          <Link {...closeRoute}>
            <X width={16} height={16} />
          </Link>
        </Button>
      }
    />
  )
}

function ReaderLoading() {
  return (
    <div className="tela-reader">
      <div className="reader-body">
        <div className="reader-scroll">
          <div className="reader-grid">
            <div className="reader-toc" aria-hidden />
            <div className="reader-article flex flex-col gap-[var(--space-4)] pt-[var(--space-8)]">
              <div className="h-[calc(var(--space-8)+var(--space-3))] w-2/3 rounded-[var(--radius-sm)] bg-[var(--surface-2)]" />
              <div className="h-[calc(var(--space-8)*4)] rounded-[var(--radius-md)] bg-[var(--surface-2)]" />
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

function ReaderMessage({
  spaceId,
  notFound,
}: {
  spaceId: number
  notFound: boolean
}) {
  return (
    <div className="tela-reader items-center justify-center">
      <div className="flex flex-col items-center gap-[var(--space-3)] text-center max-w-[28rem] p-[var(--space-7)]">
        <h2 className="m-0 text-[length:var(--text-xl)] leading-[var(--leading-tight)] font-[family-name:var(--font-sans)] text-[var(--text-primary)]">
          {notFound ? 'Page not found' : "Couldn't load this page"}
        </h2>
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
          {notFound
            ? 'The page may have been deleted or moved to another space.'
            : 'Something went wrong loading this page for reading.'}
        </p>
        <Button asChild variant="secondary">
          <Link to="/spaces/$spaceId" params={{ spaceId }}>
            Back to space
          </Link>
        </Button>
      </div>
    </div>
  )
}
