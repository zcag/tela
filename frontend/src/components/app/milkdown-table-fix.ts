import { Plugin } from '@milkdown/kit/prose/state'
import { fixTables } from '@milkdown/kit/prose/tables'

// Ragged-table repair.
//
// GFM lets a table row carry FEWER cells than the header — `| Compass | A-K |`
// under a three-column header is valid markdown, and remark parses it as a row
// with two cells. That becomes a ProseMirror table whose rows disagree in
// length, and prosemirror-tables' TableMap then describes a grid the document
// doesn't actually have. Any table operation that walks the map by row —
// `TableMap.positionAt` iterating `table.child(i)` — runs off the end and
// throws `RangeError: Index N out of range`.
//
// It surfaced as an uncaught crash out of prosemirror-tables' own `handlePaste`
// (pasting into such a table), which aborts the paste. Nothing repaired the
// table because `fixTables` — the library's own normalizer, which pads short
// rows — was never wired up.
//
// Passing oldState lets fixTables skip unchanged parts of the doc, but that
// also means it never looks at a table the user hasn't touched — and the ragged
// ones arrive that way, straight from markdown (or from a Yjs sync). So the
// first run deliberately omits oldState for one full-document scan, and every
// run after that is incremental.
export function createTableFixPlugin(): Plugin {
  let scanned = false
  return new Plugin({
    appendTransaction: (_trs, oldState, newState) => {
      const fix = fixTables(newState, scanned ? oldState : undefined)
      scanned = true
      return fix
    },
  })
}
