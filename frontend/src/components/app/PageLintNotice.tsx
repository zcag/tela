import { useEffect, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { AlertTriangle, CircleAlert } from 'lucide-react'
import { api } from '../../lib/api'
import { cn } from '../../lib/utils'

interface PageLintIssue {
  line: number
  level: 'error' | 'warning'
  rule: string
  message: string
}
interface PageLint {
  ok: boolean
  errors: number
  warnings: number
  issues: PageLintIssue[]
}

const MAX_ISSUES = 5

// Advisory lint strip under the page editor: places where the READER will show
// something other than what the markdown says — an unknown `:::block` that
// unwraps, raw HTML that's dropped, a collapsible missing its blank lines, a
// `[[wikilink]]` pointing at nothing.
//
// Renders NOTHING when the page is clean (the common case), so it costs no space
// until it has something to say, and never blocks a save — the backend route is
// advisory by design. The same report an agent gets from `lint_page`: without
// this, a human authoring in the browser would be the only party with no signal
// at all, which is exactly the gap the deck editor had to close after the fact.
export function PageLintNotice({
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
    const t = setTimeout(() => setDebounced(body), 600)
    return () => clearTimeout(t)
  }, [body])

  const { data } = useQuery({
    queryKey: ['page-lint-draft', pageId, debounced],
    queryFn: () =>
      api<PageLint>(`/api/pages/${pageId}/lint`, {
        method: 'POST',
        body: debounced,
        headers: { 'Content-Type': 'text/markdown' },
      }),
    enabled: debounced.trim().length > 0,
    staleTime: Infinity, // same text → same verdict
    placeholderData: keepPreviousData,
    retry: false, // a failure silently drops the advisory; it never blocks authoring
  })

  const issues = data?.issues ?? []
  if (!issues.length) return null

  return (
    <aside
      className={cn('flex flex-col gap-[1px]', className)}
      aria-label="Rendering issues"
    >
      <p className="px-[var(--space-1)] text-[var(--text-xs)] uppercase tracking-wide text-[var(--text-muted)]">
        Renders differently than written
      </p>
      <ul className="flex flex-col gap-[1px]">
        {issues.slice(0, MAX_ISSUES).map((x) => (
          <li
            key={`${x.rule}-${x.line}`}
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
            {/* Messages carry the full fix instructions (they're written for
                agents too); clamp so a long one can't dominate the editor. */}
            <span className="line-clamp-4 min-w-0" title={x.message}>
              <span className="font-medium tabular-nums">Line {x.line}: </span>
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
    </aside>
  )
}
