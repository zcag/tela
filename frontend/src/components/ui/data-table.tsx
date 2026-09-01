import { useMemo, useState } from 'react'
import { ArrowDown, ArrowUp, ChevronsUpDown } from 'lucide-react'
import { cn } from '../../lib/utils'

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
  // Long-form explanation shown as the header's title attribute.
  title?: string
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

  return (
    <div
      className={cn(
        'w-full overflow-x-auto',
        'rounded-[var(--radius-md)] border border-[var(--border-subtle)]',
        className,
      )}
    >
      <table className="w-full border-collapse text-[length:var(--text-sm)]">
        {caption ? <caption className="sr-only">{caption}</caption> : null}
        <thead>
          <tr>
            {columns.map((col) => {
              const active = sort?.key === col.key
              const sortable = !!col.sortValue
              return (
                <th
                  key={col.key}
                  scope="col"
                  title={col.title}
                  aria-sort={
                    active ? (sort.dir === 'asc' ? 'ascending' : 'descending') : 'none'
                  }
                  className={cn(
                    'sticky top-0 bg-[var(--surface-2)]',
                    // A pinned header cell has to outrank both its own row and
                    // the pinned body cells sliding beneath it.
                    col.sticky ? 'z-[3]' : 'z-[1]',
                    col.sticky === 'left' && 'left-0',
                    col.sticky === 'right' && 'right-0',
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
                  ) : (
                    col.header
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
                className={cn(
                  'border-b border-[var(--border-subtle)] last:border-b-0',
                  // An explicit row background is what pinned cells inherit —
                  // without it the scrolling columns show straight through them.
                  'bg-[var(--surface-1)]',
                  onRowClick && 'cursor-pointer hover:bg-[var(--surface-2)]',
                )}
              >
                {columns.map((col) => (
                  <td
                    key={col.key}
                    className={cn(
                      col.numeric
                        ? 'px-[var(--space-2)] py-[var(--space-2)] text-right tabular-nums'
                        : 'px-[var(--space-3)] py-[var(--space-2)]',
                      'text-[var(--text-primary)] align-middle',
                      col.sticky && 'sticky z-[2] bg-inherit',
                      col.sticky === 'left' && 'left-0',
                      col.sticky === 'right' && 'right-0',
                      col.className,
                    )}
                  >
                    {col.cell(row)}
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
