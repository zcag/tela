import { useEffect, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { AlertTriangle, CircleAlert } from 'lucide-react'
import { api } from '../../lib/api'
import { cn } from '../../lib/utils'

interface DeckSlide {
  no: number
  title: string
  layout: string
  note: string
}
interface DeckParse {
  count: number
  slides: DeckSlide[]
  errors?: { row?: number; message: string }[]
}
interface DeckLintIssue {
  slide: number
  level: 'error' | 'warn'
  field?: string
  message: string
}
interface DeckLint {
  ok: boolean
  errors: number
  warnings: number
  issues: DeckLintIssue[]
}

const MAX_ISSUES = 6

// Live slide navigator beside the deck markdown editor. Parses the CURRENT
// (unsaved) editor buffer via the real @slidev/parser (POST .../deck/parse) — a
// naive `---` split would break on code fences and frontmatter — so the outline
// always matches what will render. Debounced; keepPreviousData avoids flicker
// while typing. Read-only structure + validation; no render, no Chromium.
//
// It also runs the theme's structural validator (POST .../deck/lint) over the same
// buffer and shows what it finds. Purely ADVISORY — the save path never blocks a
// human (see PostPageDeckLint). Without it a structural error stayed invisible
// until Present failed to build, which is a long way from the keystroke that
// caused it.
export function DeckEditorOutline({
  body,
  pageId,
  className,
}: {
  body: string
  pageId: number
  className?: string
}) {
  const [debounced, setDebounced] = useState(body)
  useEffect(() => {
    const t = setTimeout(() => setDebounced(body), 400)
    return () => clearTimeout(t)
  }, [body])

  const draft = {
    method: 'POST' as const,
    body: debounced,
    headers: { 'Content-Type': 'text/markdown' },
  }
  const enabled = debounced.trim().length > 0

  const { data, isError } = useQuery({
    queryKey: ['deck-outline-draft', pageId, debounced],
    queryFn: () => api<DeckParse>(`/api/pages/${pageId}/deck/parse`, draft),
    enabled,
    staleTime: Infinity, // same text → same parse
    placeholderData: keepPreviousData,
    retry: false,
  })

  const { data: lint } = useQuery({
    queryKey: ['deck-lint-draft', pageId, debounced],
    queryFn: () => api<DeckLint>(`/api/pages/${pageId}/deck/lint`, draft),
    enabled,
    staleTime: Infinity, // same text → same verdict
    placeholderData: keepPreviousData,
    retry: false, // a sidecar outage silently drops the advisory; it never blocks authoring
  })

  // Parse failures come back without a slide number; lint issues carry one. Both
  // are the same thing to the author — "this won't render" — so show one list,
  // errors first.
  const issues: DeckLintIssue[] = [
    ...(data?.errors ?? []).map((e) => ({
      slide: 0,
      level: 'error' as const,
      message: e.row != null ? `line ${e.row}: ${e.message}` : e.message,
    })),
    ...(lint?.issues ?? []),
  ].sort((a, b) => (a.level === b.level ? a.slide - b.slide : a.level === 'error' ? -1 : 1))

  return (
    <aside
      className={cn(
        'flex min-h-0 flex-col gap-[var(--space-2)] overflow-y-auto',
        'rounded-[var(--radius-md)] border border-[var(--border-subtle)] bg-[var(--surface-2)] p-[var(--space-3)]',
        className,
      )}
      aria-label="Slide outline"
    >
      <div className="flex items-center justify-between text-[var(--text-xs)] uppercase tracking-wide text-[var(--text-muted)]">
        <span>Outline</span>
        <span className="tabular-nums">{data ? `${data.count} slide${data.count === 1 ? '' : 's'}` : ''}</span>
      </div>

      {issues.length ? (
        <ul className="flex flex-col gap-[1px]" aria-label="Deck issues">
          {issues.slice(0, MAX_ISSUES).map((x, i) => (
            <li
              key={`${x.level}-${x.slide}-${i}`}
              className={cn(
                'flex items-start gap-[var(--space-2)] rounded-[var(--radius-sm)] p-[var(--space-2)] text-[var(--text-xs)]',
                x.level === 'error'
                  ? 'bg-[var(--accent-negative-soft)] text-[var(--accent-negative-fg)]'
                  : 'bg-[var(--accent-warning-soft)] text-[var(--accent-warning-fg)]',
              )}
            >
              {x.level === 'error' ? (
                <CircleAlert width={14} height={14} className="mt-[2px] shrink-0" />
              ) : (
                <AlertTriangle width={14} height={14} className="mt-[2px] shrink-0" />
              )}
              {/* Messages carry the full fix instructions (they're written for agents
                  too); clamp so a long one can't push the outline itself out of view. */}
              <span className="line-clamp-8 min-w-0" title={x.message}>
                {x.slide > 0 ? <span className="font-medium tabular-nums">Slide {x.slide}: </span> : null}
                {x.message}
              </span>
            </li>
          ))}
          {issues.length > MAX_ISSUES ? (
            <li className="px-[var(--space-2)] pt-[var(--space-1)] text-[var(--text-xs)] text-[var(--text-muted)]">
              +{issues.length - MAX_ISSUES} more
            </li>
          ) : null}
        </ul>
      ) : null}

      {isError ? (
        <p className="text-[var(--text-xs)] text-[var(--text-muted)]">Outline unavailable.</p>
      ) : !data ? (
        <p className="text-[var(--text-xs)] text-[var(--text-muted)]">Start typing slides…</p>
      ) : (
        <ol className="flex flex-col gap-[1px]">
          {data.slides.map((s) => (
            <li
              key={s.no}
              className="flex items-center gap-[var(--space-2)] rounded-[var(--radius-sm)] px-[var(--space-2)] py-[var(--space-1)] text-[var(--text-sm)]"
            >
              <span className="w-5 shrink-0 text-right text-[var(--text-xs)] tabular-nums text-[var(--text-muted)]">{s.no}</span>
              <span className="min-w-0 flex-1 truncate text-[var(--text-primary)]">
                {s.title || <span className="text-[var(--text-muted)]">Untitled</span>}
              </span>
              <span className="shrink-0 rounded-[var(--radius-sm)] bg-[var(--surface-3)] px-[var(--space-1)] text-[var(--text-xs)] text-[var(--text-muted)]">
                {s.layout}
              </span>
            </li>
          ))}
        </ol>
      )}
    </aside>
  )
}
