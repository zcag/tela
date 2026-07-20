import { useEffect, type RefObject } from 'react'
import { resolveAnchor } from './anchor'
import { buildTextMap, rangeFor } from './view-anchor'
import type { CommentThread } from './use-comments'

// Paint comment-anchor highlights in the read-only view using the CSS Custom
// Highlight API — purely visual ranges registered with the browser, NO DOM
// mutation, so it can never break the rendered layout. Resolution reuses the
// same text-fingerprint `resolveAnchor` the editor uses; the anchor text is
// matched against the view's flattened text (text-node concatenation), which
// contains each anchored passage, so tier-3 (exact-unique) resolves the common
// single-block anchors. Clicking a highlighted span opens its thread.
//
// Degrades cleanly: if the API is unavailable, or an anchor doesn't resolve,
// that thread simply isn't highlighted (it's still listed in the panel).

const HIGHLIGHT_NAME = 'tela-comment-view'

// Minimal typings for the CSS Custom Highlight API (not in all lib.dom yet).
interface HighlightLike {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  new (...ranges: Range[]): any
}
interface HighlightsRegistry {
  set: (name: string, h: unknown) => void
  delete: (name: string) => void
}

export function useCommentHighlights(
  rootRef: RefObject<HTMLElement | null>,
  threads: CommentThread[] | null | undefined,
  onCommentClick?: (threadId: number) => void,
) {
  useEffect(() => {
    const root = rootRef.current
    const reg = (
      CSS as unknown as { highlights?: HighlightsRegistry }
    ).highlights
    const Ctor = (globalThis as unknown as { Highlight?: HighlightLike })
      .Highlight
    if (!root || !reg || !Ctor) return
    if (!threads || threads.length === 0) {
      reg.delete(HIGHLIGHT_NAME)
      return
    }

    const { text, segs } = buildTextMap(root)
    const ranges: Range[] = []
    const byThread: { id: number; range: Range }[] = []
    for (const t of threads) {
      if (t.root.resolved) continue
      const exact = t.root.anchor_exact ?? ''
      if (!exact) continue
      const res = resolveAnchor(text, {
        prefix: t.root.anchor_prefix ?? '',
        exact,
        suffix: t.root.anchor_suffix ?? '',
      })
      if (!res) continue
      const r = rangeFor(segs, res.from, res.to)
      if (r) {
        ranges.push(r)
        byThread.push({ id: t.root.id, range: r })
      }
    }

    if (ranges.length === 0) {
      reg.delete(HIGHLIGHT_NAME)
      return
    }
    reg.set(HIGHLIGHT_NAME, new Ctor(...ranges))

    // Click a highlighted passage → open its thread. Highlights are paint-only
    // (no events), so hit-test the click point against the resolved ranges.
    const onClick = (e: MouseEvent) => {
      if (!onCommentClick) return
      const caret = document.caretRangeFromPoint?.(e.clientX, e.clientY)
      if (!caret) return
      for (const { id, range } of byThread) {
        if (range.isPointInRange(caret.startContainer, caret.startOffset)) {
          onCommentClick(id)
          return
        }
      }
    }
    root.addEventListener('click', onClick)
    return () => {
      root.removeEventListener('click', onClick)
      reg.delete(HIGHLIGHT_NAME)
    }
  }, [rootRef, threads, onCommentClick])
}
