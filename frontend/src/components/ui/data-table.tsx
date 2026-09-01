import { useMemo, useState } from 'react'
import { ArrowDown, ArrowUp, ChevronsUpDown } from 'lucide-react'
import { cn } from '../../lib/utils'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from './tooltip'

// A sortable, sticky-header table. Owned primitive (no table lib): the app had
// no table at all before this, so every "list of records" screen — the admin
// People table first, Events/Errors/Feedback next — was rebuilt as a <ul> of
// cards that couldn't be ordered by anything.
//
// Sorting is client-side and total: `sortValue` projects a row to a comparable
// (number or string), so a column sorts by what it MEANS (raw bytes, a
// timestamp) rather than by the string it renders.

export interface DataTableColumn<Row> {
  key: string
  header: React.ReactNode
  // Cell renderer. Keep it presentational — sorting reads sortValue, not this.
  cell: (row: Row) => React.ReactNode
  // Omit to make the column unsortable (actions, avatars).
  sortValue?: (row: Row) => number | string
  // Numeric columns right-align and get tabular figures so digits line up.
  numeric?: boolean
  // Pin the column to an edge while the rest of the table scrolls under it.
  // A table wide enough to need this is exactly the one where the identity
  // column and the row actions must stay reachable — scrolling away from the
  // name you're reading, or off the end of the actions, makes it unusable.
  sticky?: 'left' | 'right'
  // Applied to both the <th> and every <td> in the column.
  className?: string
  // Long-form explanation, shown in a tooltip on the header. A real tooltip,
  // not `title=` — these carry load-bearing caveats and a native tooltip is
  // invisible on touch and unreachable by keyboard.
  title?: string
  // Family this column belongs to ("Wrote", "Read", …). Columns sharing a group
  // are separated from their neighbours by a rule and labelled by a band above
  // the header. A dozen equal-weight columns give the eye nowhere to land.
  group?: string
  // Draw a track under the number scaled to the largest value in the column.
  // Turns a block of digits into a shape you can scan; zero becomes an empty
  // track instead of a dash. Numeric columns only.
  scale?: (row: Row) => number
}

export type SortDir = 'asc' | 'desc'

export interface DataTableProps<Row> {
  rows: Row[]
  columns: DataTableColumn<Row>[]
  rowKey: (row: Row) => string | number
  // Initial sort. Defaults to the table's natural (unsorted) row order.
  defaultSort?: { key: string; dir: SortDir }
  // Controlled sort: pass both to own the state (e.g. to keep it in the URL),
  // or neither and the table keeps its own.
  sort?: { key: string; dir: SortDir }
  onSortChange?: (next: { key: string; dir: SortDir }) => void
  onRowClick?: (row: Row) => void
  // Accessible description of what a row click does, e.g. "Open details for".
  // Combined with the row's own label for the keyboard affordance.
  rowActionLabel?: (row: Row) => string
  // Dim the table while a refetch is in flight, instead of unmounting it. A
  // control that blanks the screen to answer feels destructive.
  stale?: boolean
  // Rendered in place of the table body when there are no rows.
  empty?: React.ReactNode
  caption?: string
  className?: string
}

export function DataTable<Row>({
  rows,
  columns,
  rowKey,
  defaultSort,
  sort: sortProp,
  onSortChange,
  onRowClick,
  rowActionLabel,
  stale = false,
  empty,
  caption,
  className,
}: DataTableProps<Row>) {
  const [ownSort, setOwnSort] = useState<{ key: string; dir: SortDir } | null>(
    defaultSort ?? null,
  )
  const controlled = sortProp !== undefined
  const sort = controlled ? sortProp : ownSort

  const sorted = useMemo(() => {
    if (!sort) return rows
    const col = columns.find((c) => c.key === sort.key)
    if (!col?.sortValue) return rows
    const project = col.sortValue
    const sign = sort.dir === 'asc' ? 1 : -1
    // Slice first: Array.sort mutates, and `rows` is usually query cache data.
    return rows.slice().sort((a, b) => {
      const av = project(a)
      const bv = project(b)
      if (typeof av === 'number' && typeof bv === 'number') return (av - bv) * sign
      return String(av).localeCompare(String(bv), undefined, { numeric: true }) * sign
    })
  }, [rows, columns, sort])

  function nextSort(
    prev: { key: string; dir: SortDir } | null,
    col: DataTableColumn<Row>,
  ): { key: string; dir: SortDir } {
    if (prev?.key !== col.key) {
      // First click on a numeric column shows the biggest values — that's the
      // question being asked ("who edits most"), not "who edits least".
      return { key: col.key, dir: col.numeric ? 'desc' : 'asc' }
    }
    return { key: col.key, dir: prev.dir === 'asc' ? 'desc' : 'asc' }
  }

  function toggle(col: DataTableColumn<Row>) {
    if (!col.sortValue) return
    if (controlled) {
      onSortChange?.(nextSort(sortProp ?? null, col))
      return
    }
    setOwnSort((prev) => nextSort(prev, col))
  }

  // Per-column maxima for the scale tracks, computed once over the rows in view
  // — the bars answer "big for this table", which is the comparison being made.
  const scaleMax = useMemo(() => {
    const out = new Map<string, number>()
    for (const col of columns) {
      if (!col.scale) continue
      let max = 0
      for (const r of rows) max = Math.max(max, col.scale(r))
      out.set(col.key, max)
    }
    return out
  }, [columns, rows])

  // Column groups get a band above the header and a rule at each boundary.
  // Boundaries only between FAMILIES: a rule between every column is noise, and
  // no rules at all is the soup this replaced.
  const grouped = columns.some((c) => c.group)
  const startsGroup = (i: number) =>
    grouped && i > 0 && columns[i].group !== columns[i - 1].group

  // The pinned-edge shadow only earns its keep once there's something hidden
  // under it; before that it reads as a stray border.
  const [scrolled, setScrolled] = useState(false)

  return (
    <div
      onScroll={(e) => setScrolled(e.currentTarget.scrollLeft > 2)}
      className={cn(
        'w-full overflow-x-auto',
        'rounded-[var(--radius-md)] border border-[var(--border-subtle)]',
        // Refetching keeps the old rows visible, just quieter.
        stale && 'opacity-60 transition-opacity duration-200',
        className,
      )}
    >
      <table className="w-full border-collapse text-[length:var(--text-sm)]">
        {caption ? <caption className="sr-only">{caption}</caption> : null}
        <thead>
          {grouped ? (
            <tr>
              {groupSpans(columns).map((g, i) => (
                <th
                  key={`${g.label}-${i}`}
                  colSpan={g.span}
                  scope="colgroup"
                  className={cn(
                    'sticky top-0 z-[1] bg-[var(--surface-2)]',
                    'px-[var(--space-3)] pt-[var(--space-2)]',
                    'text-left text-[length:var(--text-xs)] font-medium',
                    'text-[var(--text-muted)] opacity-70 whitespace-nowrap',
                    i > 0 && 'border-l border-[var(--border-subtle)]',
                  )}
                >
                  {g.label}
                </th>
              ))}
            </tr>
          ) : null}
          <tr>
            {columns.map((col, i) => {
              const active = sort?.key === col.key
              const sortable = !!col.sortValue
              return (
                <th
                  key={col.key}
                  scope="col"
                  aria-sort={
                    active ? (sort.dir === 'asc' ? 'ascending' : 'descending') : 'none'
                  }
                  className={cn(
                    'sticky bg-[var(--surface-2)]',
                    grouped ? 'top-[var(--space-5)]' : 'top-0',
                    // A pinned header cell has to outrank both its own row and
                    // the pinned body cells sliding beneath it.
                    col.sticky ? 'z-[3]' : 'z-[1]',
                    col.sticky === 'left' && 'left-0',
                    col.sticky === 'right' && 'right-0',
                    col.sticky === 'left' && scrolled && 'shadow-[var(--shadow-sm)]',
                    col.sticky === 'right' && scrolled && 'shadow-[var(--shadow-sm)]',
                    startsGroup(i) && 'border-l border-[var(--border-subtle)]',
                    'border-b border-[var(--border-subtle)]',
                    col.numeric
                      ? 'px-[var(--space-2)] py-[var(--space-2)]'
                      : 'px-[var(--space-3)] py-[var(--space-2)]',
                    'text-[length:var(--text-xs)] font-semibold',
                    'text-[var(--text-muted)] whitespace-nowrap',
                    col.numeric ? 'text-right' : 'text-left',
                    col.className,
                  )}
                >
                  {sortable ? (
                    <HeaderTip text={col.title}>
                    <button
                      type="button"
                      onClick={() => toggle(col)}
                      aria-label={`Sort by ${typeof col.header === 'string' ? col.header : col.key}`}
                      className={cn(
                        'group inline-flex items-center gap-[var(--space-1)]',
                        'rounded-[var(--radius-xs)] outline-none',
                        'focus-visible:ring-2 focus-visible:ring-[var(--accent)]',
                        active
                          ? 'text-[var(--text-primary)]'
                          : 'hover:text-[var(--text-primary)]',
                        col.numeric ? 'flex-row-reverse' : '',
                      )}
                    >
                      <span>{col.header}</span>
                      {active ? (
                        sort.dir === 'asc' ? (
                          <ArrowUp width={12} height={12} aria-hidden />
                        ) : (
                          <ArrowDown width={12} height={12} aria-hidden />
                        )
                      ) : (
                        <ChevronsUpDown
                          width={12}
                          height={12}
                          aria-hidden
                          className="opacity-0 group-hover:opacity-40 group-focus-visible:opacity-40"
                        />
                      )}
                    </button>
                    </HeaderTip>
                  ) : (
                    <HeaderTip text={col.title}>
                      <span>{col.header}</span>
                    </HeaderTip>
                  )}
                </th>
              )
            })}
          </tr>
        </thead>
        <tbody>
          {sorted.length === 0 ? (
            <tr>
              <td
                colSpan={columns.length}
                className="px-[var(--space-3)] py-[var(--space-4)] text-center text-[var(--text-muted)]"
              >
                {empty ?? 'Nothing to show.'}
              </td>
            </tr>
          ) : (
            sorted.map((row) => (
              <tr
                key={rowKey(row)}
                onClick={onRowClick ? () => onRowClick(row) : undefined}
                // A clickable row that only answers to a mouse puts every row's
                // primary action out of reach of the keyboard.
                tabIndex={onRowClick ? 0 : undefined}
                role={onRowClick ? 'button' : undefined}
                aria-label={onRowClick ? rowActionLabel?.(row) : undefined}
                onKeyDown={
                  onRowClick
                    ? (e) => {
                        if (e.key === 'Enter' || e.key === ' ') {
                          e.preventDefault()
                          onRowClick(row)
                        }
                      }
                    : undefined
                }
                className={cn(
                  // A fixed row height is most of what makes a dense table feel
                  // finished: without it a sub-line in one cell nudges that row
                  // taller and the rhythm wobbles down the page.
                  'h-[calc(var(--space-8)*1.5)]',
                  'group/row border-b border-[var(--border-subtle)] last:border-b-0',
                  // An explicit row background is what pinned cells inherit —
                  // without it the scrolling columns show straight through them.
                  'bg-[var(--surface-1)]',
                  onRowClick &&
                    'cursor-pointer hover:bg-[var(--surface-2)] focus-visible:bg-[var(--surface-2)] outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--accent)]',
                )}
              >
                {columns.map((col, i) => (
                  <td
                    key={col.key}
                    className={cn(
                      col.numeric
                        ? 'px-[var(--space-2)] py-[var(--space-2)] text-right tabular-nums'
                        : 'px-[var(--space-3)] py-[var(--space-2)]',
                      'text-[var(--text-primary)] align-middle',
                      startsGroup(i) && 'border-l border-[var(--border-subtle)]',
                      col.sticky && 'sticky z-[2] bg-inherit',
                      col.sticky === 'left' && 'left-0',
                      col.sticky === 'right' && 'right-0',
                      col.sticky && scrolled && 'shadow-[var(--shadow-sm)]',
                      col.className,
                    )}
                  >
                    {col.scale ? (
                      <ScaleCell value={col.scale(row)} max={scaleMax.get(col.key) ?? 0}>
                        {col.cell(row)}
                      </ScaleCell>
                    ) : (
                      col.cell(row)
                    )}
                  </td>
                ))}
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  )
}

// Consecutive columns sharing a `group` collapse into one labelled band.
function groupSpans<Row>(columns: DataTableColumn<Row>[]) {
  const spans: { label: string; span: number }[] = []
  for (const col of columns) {
    const label = col.group ?? ''
    const last = spans[spans.length - 1]
    if (last && last.label === label) last.span += 1
    else spans.push({ label, span: 1 })
  }
  return spans
}

// A number over a track scaled to the column's maximum. The point is the SHAPE:
// twelve columns of digits are unreadable at a glance, twelve columns of bars
// are not — and zero becomes an empty track rather than one more dash.
function ScaleCell({
  value,
  max,
  children,
}: {
  value: number
  max: number
  children: React.ReactNode
}) {
  const pct = max > 0 ? Math.max(0, Math.min(100, (value / max) * 100)) : 0
  return (
    <span className="inline-flex flex-col items-end gap-[3px] w-full">
      <span>{children}</span>
      <span
        aria-hidden
        className="block h-[2px] w-full max-w-[3rem] rounded-[var(--radius-full)] bg-[var(--surface-3)] overflow-hidden"
      >
        <span
          className="block h-full rounded-[var(--radius-full)] bg-[var(--accent)] opacity-55 transition-[width] duration-200"
          style={{ width: `${pct}%` }}
        />
      </span>
    </span>
  )
}

// Column explainers as a real tooltip: `title=` is invisible on touch and
// unreachable by keyboard, and these carry the caveats that keep a number from
// being misread.
function HeaderTip({ text, children }: { text?: string; children: React.ReactNode }) {
  if (!text) return <>{children}</>
  return (
    <TooltipProvider delayDuration={200}>
      <Tooltip>
        <TooltipTrigger asChild>{children}</TooltipTrigger>
        <TooltipContent className="max-w-[22rem] text-left font-normal">
          {text}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}
