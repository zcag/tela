// Where a rendered markdown link should open.
//
// EXTERNAL links open in a new tab: a tela page is a reference surface, and
// following a citation out to (say) a GitHub blob should not cost the reader
// their place in a 38 KB page. MarkdownView already opens an embedded URL that
// way, so an inline link to the same URL behaving differently was arbitrary.
//
// INTERNAL links stay in the tab — same-tab is right for in-app navigation, and
// a new tab would cold-boot the whole SPA.
//
// Deliberately NOT external: in-document anchors (#…), relative and root-
// relative paths, the tela:// wikilink scheme, and non-web schemes like
// mailto:/tel: — handing those a target would open an empty tab in some
// browsers instead of the mail client.
// `origin` defaults to the document's, and is a parameter so this stays a pure
// function (the unit suite runs headless, where there is no window).
export function isExternalUrl(url: string, origin?: string): boolean {
  const u = url.trim()
  if (!u) return false
  if (u.startsWith('#') || u.startsWith('/') || u.startsWith('./') || u.startsWith('../')) {
    return false
  }
  if (u.startsWith('tela://')) return false
  const self = origin ?? (typeof window === 'undefined' ? undefined : window.location.origin)
  try {
    // A bare relative href ("notes.md") resolves against our own origin, so it
    // is correctly treated as internal.
    const parsed = self ? new URL(u, self) : new URL(u)
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return false
    if (!self) return true // no document to compare against: any web URL is off-site
    return parsed.origin !== self
  } catch {
    return false // unparseable → treat as internal; never force a new tab on a guess
  }
}
