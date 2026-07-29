import { describe, expect, it } from 'vitest'
import { Schema } from '@milkdown/kit/prose/model'
import { EditorState } from '@milkdown/kit/prose/state'
import { TableMap, tableNodes } from '@milkdown/kit/prose/tables'
import { createTableFixPlugin } from './milkdown-table-fix'

const schema = new Schema({
  nodes: {
    doc: { content: 'block+' },
    paragraph: { group: 'block', content: 'inline*' },
    text: { group: 'inline' },
    ...tableNodes({
      tableGroup: 'block',
      cellContent: 'paragraph',
      cellAttributes: {},
    }),
  },
})

// A table whose last row is SHORT — exactly the shape GFM produces from
// `| Compass | A-K |` under a three-column header.
function raggedDoc() {
  const cell = (t: string) =>
    schema.nodes.table_cell.create(null, schema.nodes.paragraph.create(null, schema.text(t)))
  const row = (...t: string[]) => schema.nodes.table_row.create(null, t.map(cell))
  return schema.nodes.doc.create(null, [
    schema.nodes.paragraph.create(null, schema.text('intro')),
    schema.nodes.table.create(null, [
      row('Proje', 'A / K / D', 'Hangi fazlar'),
      row('UDN / IPDR', 'A-K', 'Koordinasyon'),
      row('Magellan', 'A-K', 'Planlama'),
      row('Compass', 'A-K'), // ← only two cells
    ]),
  ])
}

function rowWidths(doc: ReturnType<typeof raggedDoc>) {
  const table = doc.child(1)
  return Array.from({ length: table.childCount }, (_, i) => table.child(i).childCount)
}

// appendTransaction fires for any applied transaction, including a step-free
// one — which keeps the trigger from perturbing the table we're asserting on.
function tick(state: EditorState) {
  return state.apply(state.tr.setMeta('tick', true))
}

describe('ragged-table repair', () => {
  it('reproduces the crash without the plugin', () => {
    const doc = raggedDoc()
    const table = doc.child(1)
    expect(rowWidths(doc)).toEqual([3, 3, 3, 2])
    // TableMap claims a 3-wide grid; positionAt walks table.child(i) past the
    // end. This is the RangeError that escaped prosemirror-tables' handlePaste.
    const map = TableMap.get(table)
    expect(() => map.positionAt(4, 0, table)).toThrow(/Index 4 out of range/)
  })

  it('pads the short row on the first transaction', () => {
    const state = EditorState.create({
      doc: raggedDoc(),
      plugins: [createTableFixPlugin()],
    })
    // The first run scans the whole document even though nothing about the
    // table changed — the ragged row arrived with the doc.
    const next = tick(state)
    expect(rowWidths(next.doc)).toEqual([3, 3, 3, 3])
  })

  it('makes the repaired table safe for the op that used to throw', () => {
    const state = tick(
      EditorState.create({
        doc: raggedDoc(),
        plugins: [createTableFixPlugin()],
      }),
    )
    const table = state.doc.child(1)
    const map = TableMap.get(table)
    expect(() => map.positionAt(3, 2, table)).not.toThrow()
  })

  it('leaves an already well-formed table alone', () => {
    const fixed = tick(
      EditorState.create({
        doc: raggedDoc(),
        plugins: [createTableFixPlugin()],
      }),
    ).doc
    const twice = tick(
      EditorState.create({ doc: fixed, plugins: [createTableFixPlugin()] }),
    ).doc
    expect(rowWidths(twice)).toEqual([3, 3, 3, 3])
    expect(twice.eq(fixed)).toBe(true)
  })
})
