// Cosmetic page slugs — the Confluence-style URL piece (docs/visibility-model.md).
//
// MUST stay in parity with the backend (backend/internal/api/slug.go): same
// transliteration map, same truncation. The slug is never canonical (the page
// id / share token is), but matching the backend keeps the address bar from
// flickering between a backend-emitted slug (og:url, share links) and the
// FE-canonicalised one.

const TRANSLIT: Record<string, string> = {
  ç: 'c', Ç: 'c', ğ: 'g', Ğ: 'g', ı: 'i', İ: 'i',
  ö: 'o', Ö: 'o', ş: 's', Ş: 's', ü: 'u', Ü: 'u',
  à: 'a', á: 'a', â: 'a', ä: 'a', ã: 'a', å: 'a',
  è: 'e', é: 'e', ê: 'e', ë: 'e',
  ì: 'i', í: 'i', î: 'i', ï: 'i',
  ò: 'o', ó: 'o', ô: 'o', õ: 'o',
  ù: 'u', ú: 'u', û: 'u',
  ñ: 'n', Ñ: 'n', ß: 'ss', æ: 'ae', œ: 'oe',
}

const MAX_SLUG_LEN = 60

// pageSlug derives a URL-safe, lowercase, hyphen-joined slug from a title,
// truncated at a word boundary. Returns '' when nothing usable remains (e.g. an
// emoji- or CJK-only title) — callers then use the bare /p/{id} form.
export function pageSlug(title: string): string {
  let out = ''
  // for...of iterates by code point, so emoji don't split.
  for (const ch of title) {
    out += ch in TRANSLIT ? TRANSLIT[ch] : ch.toLowerCase()
  }
  let s = out.replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '')
  if (s.length > MAX_SLUG_LEN) {
    s = s.slice(0, MAX_SLUG_LEN)
    const i = s.lastIndexOf('-')
    if (i > 0) s = s.slice(0, i)
    s = s.replace(/^-+|-+$/g, '')
  }
  return s
}

// buildWikilinkResolveIndex maps page-title slugs to ids for resolving
// Obsidian-style `[[Name]]` bracket wikilinks (milkdown-wikilink-bracket.ts).
// Lowest id wins on a slug clash, matching the backend's `ORDER BY id ASC`
// (resolveWikiTitleSlugs in pages.go). Callers pass a space-scoped page list so
// resolution can't cross a space (membership) boundary. Lives here — not in the
// milkdown module — so the lazy reader/editor hosts can build the index without
// pulling the heavy Milkdown chunk into their entry bundle.
export function buildWikilinkResolveIndex(
  pages: ReadonlyArray<{ id: number; title: string }>,
): Map<string, number> {
  const m = new Map<string, number>()
  for (const p of [...pages].sort((a, b) => a.id - b.id)) {
    const s = pageSlug(p.title)
    if (s && !m.has(s)) m.set(s, p.id)
  }
  return m
}

// pagePath builds the in-app route path for a page, with the cosmetic slug
// appended when the title yields one. Mirrors the backend's pageAppPath.
export function pagePath(spaceId: number, pageId: number, title: string): string {
  const base = `/spaces/${spaceId}/pages/${pageId}`
  const s = pageSlug(title)
  return s ? `${base}/${s}` : base
}

// publicReaderPath builds the no-login reader route for a page in a PUBLIC
// space. Mirrors the backend's publicReaderPath.
//
// Which of the two a page gets is a real decision, not a formatting detail: the
// authed route 403s for anyone without space_access, so linking a published
// page there dead-ends every non-member who finds it via search or a shared
// URL. Route the choice through this pair — don't re-derive either path inline.
export function publicReaderPath(
  spaceId: number,
  pageId: number,
  title: string,
): string {
  const base = `/public/spaces/${spaceId}/pages/${pageId}`
  const s = pageSlug(title)
  return s ? `${base}/${s}` : base
}
