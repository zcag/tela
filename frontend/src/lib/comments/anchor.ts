import type { EditorView } from '@milkdown/kit/prose/view'

// Text-fingerprint anchors for M8-Comments. Pure module — no Yjs, no React,
// no side effects. `captureAnchor` is the only function that touches a
// ProseMirror EditorView (read-only) at capture time; resolution is a pure
// string operation against the live editor's plain-text projection.

export interface CommentAnchor {
  prefix: string
  exact: string
  suffix: string
}

const CONTEXT_WINDOW = 32

// Plain-text contract: the doc must be flattened with '\n' as the block
// separator everywhere — capture, resolve, and the in-body decoration
// (lib/comments/anchor-decoration.ts) all read `doc.textBetween(0, size, '\n')`.
// Anything that diverges from that breaks anchor resolution.
const BLOCK_SEPARATOR = '\n'

// The shared windowing rule: keep the exact passage plus up to CONTEXT_WINDOW
// chars of surrounding context on each side. Both capture paths (the PM editor
// via `captureAnchor`, the read view via `anchorFromText`) funnel through here
// so the fingerprint shape can't drift between the two surfaces.
function windowAnchor(
  before: string,
  exact: string,
  after: string,
): CommentAnchor {
  return {
    prefix: before.slice(-CONTEXT_WINDOW),
    exact,
    suffix: after.slice(0, CONTEXT_WINDOW),
  }
}

export function captureAnchor(
  view: EditorView,
  from: number,
  to: number,
): CommentAnchor {
  const doc = view.state.doc
  const size = doc.content.size
  if (from < 0 || to > size || to <= from) {
    throw new Error(
      `captureAnchor: invalid range [${from}, ${to}] for doc size ${size}`,
    )
  }
  const exact = doc.textBetween(from, to, BLOCK_SEPARATOR)
  if (exact.length === 0) {
    throw new Error('captureAnchor: empty selection text (zero-length anchor)')
  }
  // Plain-text positions ≠ ProseMirror positions when block boundaries
  // contribute separators. Slice the canonical projection instead of doing
  // PM-position arithmetic so prefix/suffix line up with what resolve sees.
  return windowAnchor(
    doc.textBetween(0, from, BLOCK_SEPARATOR),
    exact,
    doc.textBetween(to, size, BLOCK_SEPARATOR),
  )
}

// Read-view twin of `captureAnchor`: the reader has no ProseMirror doc, so its
// caller (lib/comments/view-anchor.ts) flattens the rendered DOM into one plain
// string and hands us `[from, to)` offsets into it. Same windowing as the
// editor so a read-made anchor resolves in the editor and vice-versa.
export function anchorFromText(
  text: string,
  from: number,
  to: number,
): CommentAnchor {
  if (from < 0 || to > text.length || to <= from) {
    throw new Error(
      `anchorFromText: invalid range [${from}, ${to}] for text length ${text.length}`,
    )
  }
  const exact = text.slice(from, to)
  if (exact.length === 0) {
    throw new Error('anchorFromText: empty selection text (zero-length anchor)')
  }
  return windowAnchor(text.slice(0, from), exact, text.slice(to))
}

export function resolveAnchor(
  currentText: string,
  anchor: CommentAnchor,
): { from: number; to: number } | null {
  const { prefix, exact, suffix } = anchor
  if (exact.length === 0) return null

  // Tier 1: full prefix + exact + suffix, must be unique.
  {
    const needle = prefix + exact + suffix
    const idx = uniqueIndexOf(currentText, needle)
    if (idx >= 0) {
      const from = idx + prefix.length
      return { from, to: from + exact.length }
    }
  }

  // Tier 2: partial context, shrinking the window in steps of 4.
  for (const N of [16, 12, 8, 4]) {
    const p = prefix.slice(-N)
    const s = suffix.slice(0, N)
    const needle = p + exact + s
    const idx = uniqueIndexOf(currentText, needle)
    if (idx >= 0) {
      const from = idx + p.length
      return { from, to: from + exact.length }
    }
  }

  // Tier 3: exact only — accept iff there's exactly one occurrence.
  {
    const idx = uniqueIndexOf(currentText, exact)
    if (idx >= 0) {
      return { from: idx, to: idx + exact.length }
    }
  }

  return null
}

function uniqueIndexOf(haystack: string, needle: string): number {
  if (needle.length === 0) return -1
  const first = haystack.indexOf(needle)
  if (first < 0) return -1
  const second = haystack.indexOf(needle, first + 1)
  if (second >= 0) return -1
  return first
}
