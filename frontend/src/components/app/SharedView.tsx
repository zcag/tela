import { useEffect, useMemo, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { Copy, Link as LinkIcon, RotateCw } from 'lucide-react'
import { useAllShares, type ShareAuditItem } from '../../lib/queries/share'
import { parseSqliteTs } from '../../lib/types'
import { Button } from '../ui/button'
import { VisibilityBadge } from '../ui/visibility-badge'
import { formatExpiry } from './ShareManagerSheet-utils'
import { cn } from '../../lib/utils'
import { useDocumentTitle } from '../../lib/useHeadMeta'

// SharedView — the cross-space audit screen (docs/visibility-model.md). One
// list answering "what is reachable by link right now", instead of opening each
// page's share manager to remember. Grouped by space; each row shows the live
// exposure state, the page it exposes, the URL, and expiry.

export function SharedRoute() {
  useDocumentTitle('Shared')
  const shares = useAllShares()
  // Ticks every 60s so the "expires in …" labels stay fresh while the page is
  // open (mirrors ShareManagerSheet-row's clock).
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 60_000)
    return () => window.clearInterval(id)
  }, [])

  const groups = useMemo(() => {
    const data = shares.data ?? []
    const bySpace = new Map<number, { name: string; items: ShareAuditItem[] }>()
    for (const s of data) {
      const g = bySpace.get(s.space_id)
      if (g) g.items.push(s)
      else bySpace.set(s.space_id, { name: s.space_name, items: [s] })
    }
    return [...bySpace.values()].sort((a, b) => a.name.localeCompare(b.name))
  }, [shares.data])

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="max-w-[56rem] w-full mx-auto p-[var(--space-7)] flex flex-col gap-[var(--space-6)]">
        <header className="flex flex-col gap-[var(--space-1)]">
          <h1 className="m-0 font-[family-name:var(--font-sans)] text-[length:var(--text-2xl)] leading-[var(--leading-tight)] text-[var(--text-primary)]">
            Shared
          </h1>
          <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)] leading-[var(--leading-relaxed)]">
            Every page in your spaces reachable by a link right now. Pages with
            no link are private to their space.
          </p>
        </header>

        {shares.isLoading ? <SharedSkeleton /> : null}

        {shares.isError ? (
          <div className="flex items-center justify-between gap-[var(--space-2)] px-[var(--space-3)] py-[var(--space-3)] rounded-[var(--radius-md)] bg-[var(--surface-2)] text-[length:var(--text-sm)] text-[var(--danger)]">
            <span>Couldn't load shares.</span>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => void shares.refetch()}
              aria-label="Retry"
            >
              <RotateCw width={14} height={14} />
            </Button>
          </div>
        ) : null}

        {shares.data && groups.length === 0 ? (
          <div className="rounded-[var(--radius-md)] border border-[var(--border-subtle)] bg-[var(--surface-1)] px-[var(--space-4)] py-[var(--space-5)] text-center flex flex-col gap-[var(--space-1)]">
            <p className="m-0 text-[length:var(--text-sm)] font-medium text-[var(--text-primary)]">
              Nothing is shared
            </p>
            <p className="m-0 text-[length:var(--text-xs)] text-[var(--text-muted)] leading-[var(--leading-relaxed)]">
              Pages stay private to their space until you create a share link
              from a page's visibility button.
            </p>
          </div>
        ) : null}

        {groups.map((g) => (
          <section key={g.name} className="flex flex-col gap-[var(--space-2)]">
            <h2 className="m-0 text-[length:var(--text-xs)] uppercase tracking-wider text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
              {g.name}
            </h2>
            <ul className="m-0 p-0 list-none flex flex-col gap-[var(--space-2)]">
              {g.items.map((s) => (
                <li key={s.id} className="m-0 p-0 list-none">
                  <ShareAuditRow item={s} now={now} />
                </li>
              ))}
            </ul>
          </section>
        ))}
      </div>
    </div>
  )
}

function ShareAuditRow({ item, now }: { item: ShareAuditItem; now: number }) {
  const [copied, setCopied] = useState(false)
  const expired =
    !!item.expires_at && parseSqliteTs(item.expires_at).getTime() <= now
  const expiryLabel = formatExpiry(item.expires_at, now)

  async function handleCopy() {
    if (!navigator.clipboard?.writeText) return
    try {
      await navigator.clipboard.writeText(item.url)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      // best-effort; the URL is visible to select manually
    }
  }

  return (
    <div className="flex flex-col gap-[var(--space-2)] rounded-[var(--radius-md)] border border-[var(--border-subtle)] bg-[var(--surface-1)] px-[var(--space-3)] py-[var(--space-3)]">
      <div className="flex items-center gap-[var(--space-2)] flex-wrap">
        <VisibilityBadge
          state={item.has_password ? 'password' : 'public'}
        />
        <Link
          to="/spaces/$spaceId/pages/$pageId/{-$slug}"
          params={{ spaceId: item.space_id, pageId: item.page_id, slug: undefined }}
          className="flex-1 min-w-0 truncate text-[length:var(--text-sm)] font-medium text-[var(--text-primary)] no-underline hover:text-[var(--accent)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)] rounded-[var(--radius-xs)]"
        >
          {item.page_title || 'Untitled'}
        </Link>
        {item.include_descendants ? (
          <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">
            + child pages
          </span>
        ) : null}
        {expiryLabel ? (
          <span
            className={cn(
              'text-[length:var(--text-xs)]',
              expired ? 'text-[var(--danger)]' : 'text-[var(--text-muted)]',
            )}
          >
            {expiryLabel}
          </span>
        ) : null}
      </div>
      <div className="flex items-center gap-[var(--space-2)]">
        <LinkIcon
          aria-hidden
          width={13}
          height={13}
          className="shrink-0 text-[var(--text-muted)]"
        />
        <span
          className="flex-1 min-w-0 truncate text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]"
          title={item.url}
        >
          {item.url}
        </span>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={() => void handleCopy()}
          className="h-[var(--space-7)] px-[var(--space-2)] shrink-0"
          aria-label={copied ? 'Copied' : 'Copy share URL'}
        >
          <Copy width={12} height={12} />
          <span>{copied ? 'Copied!' : 'Copy'}</span>
        </Button>
      </div>
    </div>
  )
}

function SharedSkeleton() {
  return (
    <div className="flex flex-col gap-[var(--space-2)]" aria-hidden="true">
      {[0, 1, 2].map((i) => (
        <div
          key={i}
          className="h-[calc(var(--space-8)*2)] rounded-[var(--radius-md)] bg-[var(--surface-2)]"
        />
      ))}
    </div>
  )
}
