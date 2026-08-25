// Sort + filter chrome for a rendered GFM table — a pure DOM enhancement, so it
// runs the same on MarkdownView's static HTML (every read surface: read view,
// reader, share, PDF) and on a non-editable Milkdown instance (a viewer opening
// the edit route). It lives here, not next to the editor plugin, because the
// read surfaces render through MarkdownView and must not import the editor.
//
// Idempotent: re-running over the same table is a no-op, so a host can call it
// from a render effect without tracking what it has already done.

// Numeric-aware comparison so "12" sorts after "2", and currency/percent values
// sort by their number. Falls back to locale string compare.
function compareCells(a: string, b: string): number {
  const na = parseFloat(a.replace(/[^0-9.+-]/g, ''))
  const nb = parseFloat(b.replace(/[^0-9.+-]/g, ''))
  const aNum = a.trim() !== '' && !Number.isNaN(na) && /\d/.test(a)
  const bNum = b.trim() !== '' && !Number.isNaN(nb) && /\d/.test(b)
  if (aNum && bNum) return na - nb
  return a.trim().localeCompare(b.trim(), undefined, { numeric: true })
}

function cellText(row: HTMLTableRowElement, i: number): string {
  return row.cells[i]?.textContent?.trim() ?? ''
}

export function enhanceReadonlyTable(table: HTMLTableElement): void {
  if (table.dataset.telaEnhanced) return
  const thead = table.tHead
  const tbody = table.tBodies[0]
  const headRow = thead?.rows[0]
  if (!headRow || !tbody) return
  const headCells = Array.from(headRow.cells)
  if (headCells.length === 0) return
  // Only earn the sort/filter chrome on genuinely large tables (8-row
  // threshold) — small comparison tables stay clean.
  if (tbody.rows.length < 8) return
  table.dataset.telaEnhanced = '1'

  headCells.forEach((th, i) => {
    th.classList.add('tela-th-sortable')
    let dir = 0
    th.addEventListener('click', () => {
      dir = dir === 1 ? -1 : 1
      headCells.forEach((h) => h.removeAttribute('data-sort'))
      th.dataset.sort = dir === 1 ? 'asc' : 'desc'
      const rows = Array.from(tbody.rows)
      rows.sort((ra, rb) => compareCells(cellText(ra, i), cellText(rb, i)) * dir)
      rows.forEach((r) => tbody.appendChild(r))
    })
  })

  // Filter box (we're already past the ≥8-row gate). The bar goes outside the
  // scroll port — the table itself is the horizontal scroller on read surfaces
  // (see editor.css), so a bar inside it would scroll away with the columns.
  const wrap = table.closest('.tableWrapper') ?? table
  const bar = document.createElement('div')
  bar.className = 'tela-table-filter'
  const input = document.createElement('input')
  input.type = 'text'
  input.placeholder = 'Filter rows…'
  input.setAttribute('aria-label', 'Filter table rows')
  input.addEventListener('input', () => {
    const q = input.value.trim().toLowerCase()
    for (const row of Array.from(tbody.rows)) {
      const hit = !q || (row.textContent ?? '').toLowerCase().includes(q)
      row.style.display = hit ? '' : 'none'
    }
  })
  bar.appendChild(input)
  wrap.parentElement?.insertBefore(bar, wrap)
}

// enhanceTables applies it to every GFM content table under `root`. Block-
// internal tables like the calendar month grid are `<table>` too and must never
// get sort/filter chrome.
export function enhanceTables(root: HTMLElement): void {
  root
    .querySelectorAll('table:not(.tela-calendar-table)')
    .forEach((t) => enhanceReadonlyTable(t as HTMLTableElement))
}
