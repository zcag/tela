import { pageSlug } from '../slug'

// Heading anchors: the ids that make `#some-section` links work.
//
// These are stamped onto the RENDERED DOM rather than emitted by the renderer
// because the mdast heading node carries children, not text — the id needs the
// heading's flattened text, which the DOM already has. MarkdownView calls this
// after every render, so every surface that renders a page body (the read view,
// the reader, share/public readers, PDF export) gets the same ids. Before that
// it ran only in ReaderShell, so an in-body table-of-contents link resolved in
// read mode and silently did nothing in the normal page view — the URL changed
// and the page sat still, because the target element had no id at all.
//
// Slugs match `pageSlug` (the same rule as page URLs), so a heading
// "Alerting (Microsoft Teams)" is reachable at `#alerting-microsoft-teams` —
// what a human or an agent writing a TOC by hand would guess.

export interface HeadingEntry {
  id: string
  text: string
  level: number
}

/**
 * Give every heading in `root` a stable, human-readable, unique id and return
 * them in document order. Idempotent: re-running on the same DOM reproduces the
 * same ids, so a host may call it again after its own post-processing.
 */
export function stampHeadingIds(root: HTMLElement): HeadingEntry[] {
  const els = Array.from(
    root.querySelectorAll('h1, h2, h3, h4, h5, h6'),
  ) as HTMLElement[]
  const entries: HeadingEntry[] = []
  const used = new Map<string, number>()
  els.forEach((el, i) => {
    const text = headingText(el)
    if (!text) return
    el.id = headingId(text, i, used)
    entries.push({ id: el.id, text, level: Number(el.tagName[1]) })
  })
  return entries
}

/**
 * The id rule itself, kept pure so it can be pinned by a test: this is the
 * contract a hand-written table of contents guesses against, so it must stay
 * `pageSlug` (page-URL rules) and it must stay stable. `used` carries the
 * dedupe state across one document — the second "Overview" becomes
 * `overview-2`, keyed on the slug rather than the raw text so two headings
 * differing only in punctuation can't collide onto one id.
 */
export function headingId(
  text: string,
  index: number,
  used: Map<string, number>,
): string {
  const base = pageSlug(text) || `section-${index + 1}`
  const n = used.get(base) ?? 0
  used.set(base, n + 1)
  return n === 0 ? base : `${base}-${n + 1}`
}

// A heading's own text, ignoring the reader's injected "#" anchor — otherwise
// re-stamping a reader heading would slug the affordance along with the title.
function headingText(el: HTMLElement): string {
  let s = ''
  el.childNodes.forEach((n) => {
    if (n instanceof HTMLElement && n.classList.contains('reader-anchor')) return
    s += n.textContent ?? ''
  })
  return s.trim()
}

/**
 * Honour a deep-link hash once the content exists. The browser tries to jump on
 * load, finds nothing (the body hasn't rendered yet), and never retries — so the
 * host re-runs it after render. `scrollIntoView` walks up to whatever actually
 * scrolls, which matters here: on desktop the page body is an inner overflow
 * frame, not the document (see the two-scroll-models note in CLAUDE.md).
 *
 * Both arguments are explicit on purpose. The lookup is scoped to the render
 * root rather than the document so it can't match a heading in some other
 * mounted copy of the same body; and the hash is passed in because
 * `window.location.hash` is NOT reliable here — on a fresh load the router
 * normalizes the URL after the first render, so reading the window during
 * onReady sees an empty hash and silently does nothing. Callers inside the
 * router pass its hash, which updates when the URL settles.
 */
export function scrollToHashIn(root: ParentNode, hash: string): boolean {
  const id = decodeURIComponent(hash.replace(/^#/, ''))
  if (!id) return false
  const target = root.querySelector(`[id="${CSS.escape(id)}"]`)
  if (!target) return false
  target.scrollIntoView({ block: 'start' })
  return true
}
