import {
  AlertTriangle,
  Bot,
  Clock,
  RefreshCw,
  ShieldAlert,
  User,
} from 'lucide-react'
import { useAgreement, useProvenance } from '../../lib/queries/pages'
import { daysSinceSqlite, relativeTimeFromSqlite } from '../../lib/relativeTime'
import { navigateToPage } from '../../lib/pageHitItem'
import { Popover, PopoverContent, PopoverTrigger } from '../ui/popover'
import { cn } from '../../lib/utils'

// PageTrustStrip — the read-only "epistemic" byline under a page title: a quiet
// line that tells you how much to trust what you're reading, computed (never
// written) from signals tela already has. Slice 1 carries the two honest,
// non-redundant ones:
//   • freshness — how long since the last edit, flipped to a warning when the
//     page is old or past a `review_every_days` cadence it declares in props;
//   • provenance — whether a human, an agent, or a sync last touched it.
// (Corroboration/contradiction — "do my other pages back this up?" — needs the
// agreement pass and lands in a later slice, where it can be labelled honestly.)

// STALE_DAYS: past this with no declared review cadence, a page reads as possibly
// stale. ~4 months — long enough not to nag living docs, short enough to flag rot.
const STALE_DAYS = 120

function numProp(
  props: Record<string, unknown> | undefined,
  key: string,
): number | null {
  const v = props?.[key]
  if (typeof v === 'number' && Number.isFinite(v)) return v
  if (typeof v === 'string' && v.trim() !== '' && !Number.isNaN(Number(v))) {
    return Number(v)
  }
  return null
}

export function PageTrustStrip({
  spaceId,
  pageId,
  updatedAt,
  props,
}: {
  spaceId: number
  pageId: number
  updatedAt: string
  props?: Record<string, unknown>
}) {
  const prov = useProvenance(pageId)
  const agree = useAgreement(pageId)

  const ageDays = daysSinceSqlite(updatedAt)
  const reviewEvery = numProp(props, 'review_every_days')
  const overdue = reviewEvery != null && ageDays > reviewEvery
  const stale = overdue || ageDays > STALE_DAYS
  const source = prov.data?.source
  const editor = prov.data?.editor
  const author = prov.data?.author
  // Byline: the original author, with the last editor appended only when it's a
  // different person. Falls back to whichever single name we know.
  const byline =
    author && editor && editor !== author
      ? `by ${author} · edited by ${editor}`
      : author
        ? `by ${author}`
        : editor
          ? `by ${editor}`
          : null
  const ag = agree.data?.computed ? agree.data : null

  return (
    <div className="flex flex-wrap items-center gap-x-[var(--space-3)] gap-y-[var(--space-1)] text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
      {/* Freshness — the headline signal; warns amber when old / overdue. */}
      <span
        className={cn(
          'inline-flex items-center gap-[var(--space-1)]',
          stale && 'text-[var(--warning)]',
        )}
      >
        {stale ? (
          <AlertTriangle width={12} height={12} aria-hidden />
        ) : (
          <Clock width={12} height={12} aria-hidden />
        )}
        <span>
          Updated {relativeTimeFromSqlite(updatedAt)}
          {overdue ? ' · review overdue' : ''}
        </span>
      </span>

      {/* Provenance — who/what last touched it. Agent is accented (the case worth
          noticing); sync and human are muted. */}
      {source === 'agent' ? (
        <span className="inline-flex items-center gap-[var(--space-1)] text-[var(--accent)]">
          <Bot width={12} height={12} aria-hidden /> Agent-written
        </span>
      ) : source === 'sync' ? (
        <span className="inline-flex items-center gap-[var(--space-1)]">
          <RefreshCw width={12} height={12} aria-hidden /> Synced
        </span>
      ) : source === 'human' && byline ? (
        <span className="inline-flex items-center gap-[var(--space-1)]">
          <User width={12} height={12} aria-hidden /> {byline}
        </span>
      ) : null}

      {/* Corroboration is computed but NOT shown. The judge only ever sees each
          page's opening excerpt, and the openings of related pages restate the
          same summary — so the count mostly measured "these two pages start
          alike", while reading as "this page has been verified". A contradiction
          still earns its place: it names two specific values, each checked to
          appear verbatim in its own page. */}

      {/* Contradiction — the case worth noticing; danger tone. Click opens a
          read-only popover listing each conflicting page (navigable) + the reason
          the model flagged. */}
      {ag && ag.dispute > 0 ? (
        <Popover>
          <PopoverTrigger asChild>
            <button
              type="button"
              className="inline-flex items-center gap-[var(--space-1)] text-[var(--danger)] bg-transparent border-0 p-0 cursor-pointer font-[family-name:var(--font-sans)] text-[length:var(--text-xs)] hover:underline focus-visible:outline-none focus-visible:underline"
            >
              <ShieldAlert width={12} height={12} aria-hidden /> {ag.dispute} may
              dispute this
            </button>
          </PopoverTrigger>
          <PopoverContent align="start">
            <p className="m-0 mb-[var(--space-2)] text-[length:var(--text-xs)] uppercase tracking-wider text-[var(--text-muted)]">
              Possible contradictions
            </p>
            <ul className="m-0 p-0 list-none flex flex-col gap-[var(--space-3)]">
              {ag.disputes.map((d, i) => (
                <li key={`${d.page_id}-${i}`} className="flex flex-col gap-[2px]">
                  {d.page_id ? (
                    <button
                      type="button"
                      onClick={() => navigateToPage(spaceId, d.page_id)}
                      className="text-left text-[length:var(--text-sm)] text-[var(--accent)] font-medium bg-transparent border-0 p-0 cursor-pointer hover:underline"
                    >
                      {d.title || 'Untitled'}
                    </button>
                  ) : (
                    <span className="text-[length:var(--text-sm)] text-[var(--text-primary)] font-medium">
                      {d.title || 'Untitled'}
                    </span>
                  )}
                  {d.reason ? (
                    <span className="text-[length:var(--text-xs)] text-[var(--text-muted)] leading-[var(--leading-normal)]">
                      {d.reason}
                    </span>
                  ) : null}
                </li>
              ))}
            </ul>
          </PopoverContent>
        </Popover>
      ) : null}
    </div>
  )
}
