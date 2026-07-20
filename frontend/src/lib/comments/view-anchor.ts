import { anchorFromText, type CommentAnchor } from './anchor'

// Shared DOM↔plain-text projection for the read-only view (MarkdownView). The
// reader has no ProseMirror doc, so "the document text" is the concatenation of
// every text node under the content root, in document order, with NO block
// separators. `resolveAnchor` (highlight painting) and `anchorForSelection`
// (creating a comment from a reader selection) both read this same projection,
// so an anchor round-trips: what we fingerprint on capture is exactly what we
// search on resolve.

export interface Seg {
  node: Text
  start: number
  len: number
}

// Flatten the rendered content into one string + a map back to the text nodes
// that produced each slice. Cheap: one TreeWalker pass, no DOM cloning.
export function buildTextMap(root: HTMLElement): { text: string; segs: Seg[] } {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  let text = ''
  const segs: Seg[] = []
  let n = walker.nextNode()
  while (n) {
    const v = (n as Text).nodeValue ?? ''
    if (v.length) {
      segs.push({ node: n as Text, start: text.length, len: v.length })
      text += v
    }
    n = walker.nextNode()
  }
  return { text, segs }
}

// Map a `[from, to)` slice of the flattened text back to a live DOM Range.
export function rangeFor(segs: Seg[], from: number, to: number): Range | null {
  let start: { node: Text; offset: number } | null = null
  let end: { node: Text; offset: number } | null = null
  for (const s of segs) {
    if (start == null && from >= s.start && from < s.start + s.len) {
      start = { node: s.node, offset: from - s.start }
    }
    if (to > s.start && to <= s.start + s.len) {
      end = { node: s.node, offset: to - s.start }
    }
  }
  if (!start || !end) return null
  try {
    const r = document.createRange()
    r.setStart(start.node, start.offset)
    r.setEnd(end.node, end.offset)
    return r
  } catch {
    return null
  }
}

// The reverse of `rangeFor`: a selection boundary (container, offset) → its
// offset in the flattened text. Text-node boundaries (the common case for a
// user drag) map exactly; element boundaries snap to the nearest text-node
// edge in document order, which is what block-level selection endpoints want.
function boundaryOffset(
  segs: Seg[],
  container: Node,
  offset: number,
): number | null {
  if (container.nodeType === Node.TEXT_NODE) {
    for (const s of segs) {
      if (s.node === container) return s.start + Math.min(offset, s.len)
    }
    // A zero-length (unmapped) text node — fall back to its document position.
    return positionByDom(segs, container)
  }
  const ref =
    offset < container.childNodes.length ? container.childNodes[offset] : null
  if (ref == null) {
    // Boundary sits after the container's last child → end of its last text.
    let end: number | null = null
    for (const s of segs) if (container.contains(s.node)) end = s.start + s.len
    return end
  }
  return positionByDom(segs, ref)
}

// First seg that is `node`, lives inside it, or follows it in document order —
// its start is the text offset of the boundary just before `node`.
function positionByDom(segs: Seg[], node: Node): number | null {
  for (const s of segs) {
    if (s.node === node || (node as Element).contains?.(s.node)) return s.start
    const pos = node.compareDocumentPosition(s.node)
    if (pos & Node.DOCUMENT_POSITION_FOLLOWING) return s.start
  }
  const last = segs[segs.length - 1]
  return last ? last.start + last.len : null
}

// Turn a user selection inside the reader into a comment anchor + its exact
// text (for the composer preview). Returns null when the selection is empty,
// outside the content root, or unmappable — the caller then offers no comment
// affordance, so the feature degrades cleanly.
export function anchorForSelection(
  root: HTMLElement,
  range: Range,
): { anchor: CommentAnchor; preview: string } | null {
  if (range.collapsed) return null
  if (!root.contains(range.commonAncestorContainer)) return null
  const { text, segs } = buildTextMap(root)
  const from = boundaryOffset(segs, range.startContainer, range.startOffset)
  const to = boundaryOffset(segs, range.endContainer, range.endOffset)
  if (from == null || to == null || to <= from) return null
  if (!text.slice(from, to).trim()) return null
  try {
    const anchor = anchorFromText(text, from, to)
    return { anchor, preview: anchor.exact }
  } catch {
    return null
  }
}
