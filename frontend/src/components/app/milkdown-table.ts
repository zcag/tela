import { $prose } from '@milkdown/kit/utils'
import { Plugin, PluginKey } from '@milkdown/kit/prose/state'
import { Decoration, DecorationSet } from '@milkdown/kit/prose/view'
import type { Node as ProseNode } from '@milkdown/kit/prose/model'
import { enhanceTables } from '../../lib/blocks/table-enhance'

// M19 — table upgrades, folded into the stock GFM table (no new block type, no
// new syntax, perfect round-trip). Everything is derived from cell content or
// is a reader-side affordance:
//
//   • Glyph cells — a cell whose entire text is `check`/`cross`/`dash` (or
//     ✓/✗/–, yes/no) renders as a themed, semantically-coloured icon
//     (check=green, cross=red, dash=muted). Click into the cell to edit the
//     keyword (the icon reveals the text on focus). This is the comparison-
//     matrix ✓/✗ grid, absorbed into the table.
//   • Sticky first column — the row-label column pins while you scroll a wide
//     table horizontally (pure CSS; invisible when there's nothing to scroll).
//   • Sort + filter — in read-only / reader / share / PDF views, a genuinely
//     large table (≥8 rows) gets clickable sort headers + a filter box. Reader-
//     only so it never fights editing; small tables stay clean. Those surfaces
//     render through MarkdownView, which applies it itself; the call below only
//     covers a non-editable Milkdown (a viewer opening the edit route).
//
// Glyph cells are ProseMirror node decorations (both edit + read modes, CSS does
// the visual). Sort/filter is a pure DOM enhancement and lives in
// lib/blocks/table-enhance, shared with MarkdownView.

const tableKey = new PluginKey('tela-table-enhance')

// Exact-match glyph keywords (case-insensitive). Symbols pass through unchanged.
const GLYPHS: Record<string, 'check' | 'cross' | 'dash'> = {
  check: 'check',
  '✓': 'check',
  '✔': 'check',
  yes: 'check',
  cross: 'cross',
  '✗': 'cross',
  '✕': 'cross',
  '×': 'cross',
  no: 'cross',
  dash: 'dash',
  '–': 'dash',
  '—': 'dash',
  '-': 'dash',
  'n/a': 'dash',
}

function glyphFor(text: string): 'check' | 'cross' | 'dash' | null {
  const t = text.trim()
  if (!t) return null
  return GLYPHS[t.toLowerCase()] ?? null
}

function buildDecorations(doc: ProseNode): DecorationSet {
  const decos: Decoration[] = []
  doc.descendants((node, pos) => {
    if (node.type.name !== 'table') return true
    node.forEach((row, rowOffset) => {
      if (row.type.name !== 'table_row') return
      const rowPos = pos + 1 + rowOffset
      row.forEach((cell, cellOffset) => {
        const cellPos = rowPos + 1 + cellOffset
        const g = glyphFor(cell.textContent)
        if (g) {
          decos.push(
            Decoration.node(cellPos, cellPos + cell.nodeSize, {
              class: `tela-cell-glyph tela-cell-glyph-${g}`,
            }),
          )
        }
      })
    })
    return false // handled the whole table; don't descend into its cells
  })
  return DecorationSet.create(doc, decos)
}

export const tableEnhancePlugin = $prose(() => {
  return new Plugin({
    key: tableKey,
    props: {
      decorations(state) {
        return buildDecorations(state.doc)
      },
    },
    view(editorView) {
      const run = () => {
        if (editorView.editable) return
        enhanceTables(editorView.dom as HTMLElement)
      }
      run()
      return { update: run }
    },
  })
})
