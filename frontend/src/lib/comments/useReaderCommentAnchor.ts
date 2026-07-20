import { useCallback, useEffect, useState } from 'react'
import { anchorForSelection } from './view-anchor'
import type { CommentAnchor } from './anchor'

export interface ReaderSelection {
  anchor: CommentAnchor
  preview: string
}

// Read-view selection bridge — the twin of the editor's PM selection bridge
// (PageView's handleSelectionChange). It watches the reader's text selection
// and snapshots the most recent non-empty one as a comment anchor.
//
// Why snapshot instead of read-at-submit like the editor does: a native DOM
// selection COLLAPSES the instant focus moves to the composer textarea, so by
// submit time there's nothing left to capture. We therefore capture on
// selection and hold it — ignoring the collapse — which reproduces the editor's
// behaviour of keeping its selection alive while you type in the side panel.
export function useReaderCommentAnchor(
  root: HTMLElement | null,
  enabled: boolean,
): { selection: ReaderSelection | null; clear: () => void } {
  const [selection, setSelection] = useState<ReaderSelection | null>(null)
  const clear = useCallback(() => setSelection(null), [])

  useEffect(() => {
    if (!enabled || !root) return
    let raf = 0
    const sample = () => {
      raf = 0
      const sel = document.getSelection()
      // Collapsed / empty / outside-the-reader selections are ignored, not
      // cleared — the last real selection stays live for the composer.
      if (!sel || sel.rangeCount === 0 || sel.isCollapsed) return
      const range = sel.getRangeAt(0)
      if (!root.contains(range.commonAncestorContainer)) return
      const next = anchorForSelection(root, range)
      if (next) setSelection(next)
    }
    // selectionchange fires per-pixel during a drag; coalesce to one sample per
    // frame so we don't reflatten the doc on every mousemove.
    const schedule = () => {
      if (!raf) raf = requestAnimationFrame(sample)
    }
    document.addEventListener('selectionchange', schedule)
    return () => {
      document.removeEventListener('selectionchange', schedule)
      if (raf) cancelAnimationFrame(raf)
    }
  }, [root, enabled])

  return { selection, clear }
}
