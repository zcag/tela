import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react'
import { Type } from 'lucide-react'
import {
  relativeTimeFromSqlite,
  postDateFromSqlite,
} from '../../lib/relativeTime'
import { useHeadMeta } from '../../lib/useHeadMeta'
import { pageSlug } from '../../lib/slug'
import {
  getTheme,
  setTheme,
  subscribeToTheme,
  THEMES,
  type ThemeName,
} from '../../lib/theme'
import { Button } from '../ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from '../ui/dropdown-menu'
import { ToggleGroup, ToggleGroupItem } from '../ui/toggle'
import { SummaryTitle } from './SummaryHint'
import { WikilinkHoverPreview } from './wikilink-hover-preview'
import { MarkdownView } from '../view/MarkdownView'

// Inlined (not imported from milkdown-wikilink-decoration.ts) so Rollup doesn't
// pull the wikilink-decoration module in as a shared dep of the reader chunk —
// that split was producing a separate ~320 KB chunk. 5-line dupe is cheaper.
function parseWikilinkPageId(href: string): number | null {
  const prefix = 'tela://page/'
  if (!href.startsWith(prefix)) return null
  const tail = href.slice(prefix.length)
  if (!/^\d+$/.test(tail)) return null
  return Number(tail)
}

// Footnotes. MarkdownView already renders `.reader-footnote-def` (id `fn-<label>`)
// and `.reader-footnote-ref` (id `fnref-<label>`, linking to its def). Here the
// reader just marks the first definition as the "Footnotes" section start and
// appends a back-link (↩) to each definition. Non-destructive — the static view
// never re-renders.
function wireFootnotes(root: HTMLElement) {
  const defs = Array.from(
    root.querySelectorAll('.reader-footnote-def'),
  ) as HTMLElement[]
  defs.forEach((def, i) => {
    if (i === 0) def.classList.add('reader-footnotes-start')
    const label = def.id.replace(/^fn-/, '')
    if (label && !def.querySelector(':scope > .reader-footnote-back')) {
      const back = document.createElement('a')
      back.className = 'reader-footnote-back'
      back.href = `#fnref-${label}`
      // U+FE0E forces text (not emoji) presentation of the return arrow.
      back.textContent = '↩︎'
      back.setAttribute('aria-label', 'Back to reference')
      def.appendChild(back)
    }
  })
}

type ReaderSize = 's' | 'm' | 'l'
type ReaderFont = 'sans' | 'serif'
const SIZE_KEY = 'tela:reader:size'
const FONT_KEY = 'tela:reader:font'
const WORDS_PER_MIN = 220

function readPref<T extends string>(key: string, fallback: T, valid: readonly T[]): T {
  try {
    const v = localStorage.getItem(key)
    return v && (valid as readonly string[]).includes(v) ? (v as T) : fallback
  } catch {
    return fallback
  }
}

function writePref(key: string, value: string) {
  try {
    localStorage.setItem(key, value)
  } catch {
    // private-mode / quota — preference just won't persist this session.
  }
}

// Rough word count for the reading-time estimate. Strips the loudest markdown
// noise so fenced code / link URLs don't inflate the count — good enough for an
// at-a-glance "N min read", not a parser.
function readingMinutes(body: string): number {
  const text = body
    .replace(/```[\s\S]*?```/g, ' ')
    .replace(/`[^`]*`/g, ' ')
    .replace(/!\[[^\]]*\]\([^)]*\)/g, ' ')
    .replace(/\[([^\]]+)\]\([^)]*\)/g, '$1')
    .replace(/[#>*_~`-]/g, ' ')
  const words = text.split(/\s+/).filter(Boolean).length
  return Math.max(1, Math.round(words / WORDS_PER_MIN))
}

interface TocEntry {
  id: string
  text: string
  level: number
}

export interface ReaderShellProps {
  /** Page being read — drives the editor key + reading-time/meta. */
  pageId: number
  title: string
  /** Frontmatter standfirst — when set, a hover hint by the title shows it. */
  summary?: string | null
  body: string
  /**
   * When set, renders in place of the markdown body — for non-prose doc-types
   * (e.g. a sheet's read-only grid). `body` is still used for reading-time/meta.
   */
  bodySlot?: ReactNode
  updatedAt: string
  /** Decoration mode for the read-only editor. */
  wikilinkMode: 'edit' | 'share'
  /**
   * Ids the wikilink decoration treats as "alive"/in-scope. In read mode this
   * is every page; in share mode it's the in-scope subtree.
   */
  aliveWikilinkIds: Set<number> | null
  /**
   * Slug→id map for resolving `[[Name]]` bracket wikilinks, scoped to the
   * reading context (the page's space in read mode, the in-scope subtree in
   * share mode). `null` leaves bracket links neutral/non-navigable.
   */
  wikilinkResolveIndex: Map<string, number> | null
  /**
   * Navigation policy for a clicked wikilink. The shell preventDefaults every
   * `tela://page/N` anchor (the scheme is dead to the browser); the caller
   * decides whether/where to navigate (no-op for out-of-scope or broken).
   */
  onNavigateWikilink: (targetPageId: number) => void
  /** Far-left of the top bar — close button (read) or wordmark (share). */
  topbarLeading?: ReactNode
  /** Right of the top bar, before the Display/Print controls — e.g. Sign in. */
  topbarTrailing?: ReactNode
  /** Optional persistent left rail (share subtree nav). */
  sidebar?: ReactNode
  /** Bound to Escape when provided (read mode → back to editor). */
  onEscape?: () => void
  /** Optional source line in the cover meta (e.g. the canonical/share URL).
   * Shown in print/PDF and share contexts so an exported doc reads as published. */
  sourceLabel?: string
  /** Mount the wikilink hover-preview over the article. Authed reader only —
   * the preview fetches via the authed API, so it stays off for share/print. */
  enableLinkPreview?: boolean
  /** Public-reader SEO/social head: description, canonical, OG image, feed. When
   * present the shell emits full per-page meta (indexable surfaces); when absent
   * it only manages the document title (authed read / share are noindex). */
  headMeta?: {
    description?: string
    canonicalPath?: string
    image?: string
    feedHref?: string
  }
  /** Blog-style article presentation (public reader). */
  /** Hero cover image above the title (a post's `cover:`); omitted when unset. */
  coverImage?: string
  /** Byline node shown first in the cover meta, e.g. "by @author". */
  byline?: ReactNode
  /** Published date (tela ts). When set, the meta shows it instead of "Updated". */
  publishedAt?: string
  /** Rendered after the article body — e.g. previous/next post navigation. */
  articleFooter?: ReactNode
  /** Attachments strip rendered just below the title (page files). */
  attachmentStrip?: ReactNode
}

// Set on the window once the reader has painted (fonts ready + a short settle so
// async mermaid/katex/diagrams land). gotenberg's PDF export waits on this flag
// before capturing — see backend renderPDF (waitForExpression).
declare global {
  interface Window {
    __telaPdfReady?: boolean
  }
}

// The shared reading surface: chrome-free, full-bleed, read-only page render
// with size/typeface/theme controls, a scroll-spy TOC, reading-progress, and a
// print stylesheet that doubles as Save-as-PDF. Presentational only — auth and
// data-fetching live in the route callers (PageReader / ShareReaderView).
export function ReaderShell({
  pageId,
  title,
  summary,
  body,
  bodySlot,
  updatedAt,
  wikilinkMode,
  wikilinkResolveIndex,
  onNavigateWikilink,
  topbarLeading,
  topbarTrailing,
  sidebar,
  onEscape,
  sourceLabel,
  enableLinkPreview,
  headMeta,
  coverImage,
  byline,
  publishedAt,
  articleFooter,
  attachmentStrip,
}: ReaderShellProps) {
  // Preferences — text size + typeface, persisted; theme is global.
  const [size, setSize] = useState<ReaderSize>(() =>
    readPref<ReaderSize>(SIZE_KEY, 'm', ['s', 'm', 'l']),
  )
  const [font, setFont] = useState<ReaderFont>(() =>
    readPref<ReaderFont>(FONT_KEY, 'sans', ['sans', 'serif']),
  )
  const [theme, setThemeState] = useState<ThemeName>(() => getTheme())
  useEffect(() => subscribeToTheme(setThemeState), [])

  const minutes = useMemo(() => readingMinutes(body), [body])

  // `[[Name]]` resolution for MarkdownView, scoped to the reading context
  // (wikilinkResolveIndex is space-scoped in read mode, subtree-scoped in
  // share). Out-of-scope/unknown → unresolved → rendered plain (not "broken").
  const resolveWikilink = useCallback(
    (slug: string) => wikilinkResolveIndex?.get(slug) ?? null,
    [wikilinkResolveIndex],
  )

  // Document title (+ full SEO/social meta on the public reader, when headMeta
  // is supplied) while in the reader.
  useHeadMeta({
    title: `${title || 'Untitled'} — tela`,
    description: headMeta?.description,
    canonicalPath: headMeta?.canonicalPath,
    image: headMeta?.image,
    ogType: 'article',
    feedHref: headMeta?.feedHref,
  })

  // Esc exits (read mode → editor). No-op when the caller has nowhere to go.
  useEffect(() => {
    if (!onEscape) return
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') {
        e.preventDefault()
        onEscape!()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onEscape])

  // --- TOC + scroll-spy + progress ---------------------------------------
  const scrollRef = useRef<HTMLDivElement | null>(null)
  const progressRef = useRef<HTMLDivElement | null>(null)
  const topbarRef = useRef<HTMLElement | null>(null)
  const headingsRef = useRef<HTMLElement[]>([])
  const [toc, setToc] = useState<TocEntry[]>([])
  const [activeId, setActiveId] = useState<string | null>(null)
  const activeRef = useRef<string | null>(null)

  // Build the TOC from the rendered heading DOM once the editor view is ready.
  // Markdown→DOM heading order is stable, so reading straight off view.dom keeps
  // the TOC guaranteed in-sync with what's on screen.
  const handleContentReady = useCallback((root: HTMLElement | null) => {
    if (!root) return
    requestAnimationFrame(() => {
      const els = Array.from(
        root.querySelectorAll('h1, h2, h3'),
      ) as HTMLElement[]
      const entries: TocEntry[] = []
      // Stable, human-readable slug ids (deduped) so a heading deep-link
      // (`#getting-started`) survives across loads — unlike the old positional
      // `reader-h-${i}`, which changed the moment a heading was added above.
      const used = new Map<string, number>()
      els.forEach((el, i) => {
        const text = (el.textContent ?? '').trim()
        if (!text) return
        const base = pageSlug(text) || `section-${i + 1}`
        const n = used.get(base) ?? 0
        used.set(base, n + 1)
        el.id = n === 0 ? base : `${base}-${n + 1}`
        el.classList.add('reader-heading')
        // Hover affordance: a click-to-copy anchor injected once per heading.
        // The reader dispatches no transactions, so poking PM's static DOM is
        // safe here (no redraw to clobber it).
        if (!el.querySelector(':scope > .reader-anchor')) {
          const a = document.createElement('a')
          a.className = 'reader-anchor'
          a.href = `#${el.id}`
          a.textContent = '#'
          a.setAttribute('contenteditable', 'false')
          a.setAttribute('aria-label', 'Copy link to this section')
          el.prepend(a)
        } else {
          const a = el.querySelector(':scope > .reader-anchor') as HTMLElement
          a.setAttribute('href', `#${el.id}`)
        }
        entries.push({ id: el.id, text, level: Number(el.tagName[1]) })
      })
      headingsRef.current = els.filter((el) => el.id && el.textContent?.trim())
      setToc(entries)
      wireFootnotes(root)
      // Honour a deep-link hash now that ids exist (the browser couldn't on
      // load — the article hadn't rendered yet).
      const hash = decodeURIComponent(window.location.hash.slice(1))
      if (hash) {
        const target = document.getElementById(hash)
        if (target) target.scrollIntoView({ block: 'start' })
      }
      // PDF export readiness signal (gotenberg waits on this). Wait for fonts,
      // then for any charts to finish painting (ECharts lazy-loads ~1MB + renders
      // async, so a fixed delay isn't enough), then a short settle so async
      // mermaid/katex/diagram paints also land in the capture.
      const chartsSettled = (): Promise<void> =>
        new Promise((resolve) => {
          const start = Date.now()
          const check = () => {
            const pending = Array.from(
              document.querySelectorAll('.tela-chart-canvas'),
            ).filter(
              (c) =>
                !c.querySelector('svg') && !c.closest('.tela-chart-error'),
            )
            // Resolve when every chart has painted its SVG (or errored), or
            // after an 8s cap so a stuck/failed chart never blocks the export.
            if (pending.length === 0 || Date.now() - start > 8000) resolve()
            else window.setTimeout(check, 150)
          }
          check()
        })
      const ready = () =>
        void chartsSettled().then(() =>
          window.setTimeout(() => {
            window.__telaPdfReady = true
          }, 400),
        )
      if (document.fonts?.ready) void document.fonts.ready.finally(ready)
      else ready()
    })
  }, [])

  useEffect(() => {
    const sc = scrollRef.current
    if (!sc) return
    // Read progress from whatever actually scrolls. On touch the inner frame is
    // released (so a pinch-zoomed page can be panned — see reader.css), and what
    // takes over depends on where the reader is mounted: the document on
    // /share, but the fixed ?view=read overlay in the app. Resolve it by walking
    // up rather than assuming — a wrong guess silently freezes the progress bar
    // and the TOC scroll-spy.
    const findScroller = (from: HTMLElement): HTMLElement | null => {
      for (let el: HTMLElement | null = from; el; el = el.parentElement) {
        const oy = window.getComputedStyle(el).overflowY
        if (oy === 'auto' || oy === 'scroll') return el
      }
      return null
    }
    const scroller = findScroller(sc)
    const metrics =
      scroller ?? document.scrollingElement ?? document.documentElement
    const scrollTarget: Window | HTMLElement = scroller ?? window
    let raf = 0
    const update = () => {
      raf = 0
      const max = metrics.scrollHeight - metrics.clientHeight
      const p = max > 0 ? metrics.scrollTop / max : 0
      if (progressRef.current) {
        progressRef.current.style.setProperty('--reader-progress', String(p))
      }
      if (topbarRef.current) {
        topbarRef.current.dataset.scrolled =
          metrics.scrollTop > 4 ? 'true' : 'false'
      }
      // Active heading = last one whose top has crossed a band below the bar.
      const threshold = sc.getBoundingClientRect().top + 96
      let next: string | null = headingsRef.current[0]?.id ?? null
      for (const el of headingsRef.current) {
        if (el.getBoundingClientRect().top <= threshold) next = el.id
        else break
      }
      if (next !== activeRef.current) {
        activeRef.current = next
        setActiveId(next)
      }
    }
    const onScroll = () => {
      if (!raf) raf = requestAnimationFrame(update)
    }
    scrollTarget.addEventListener('scroll', onScroll, { passive: true })
    update()
    return () => {
      scrollTarget.removeEventListener('scroll', onScroll)
      if (raf) cancelAnimationFrame(raf)
    }
  }, [toc])

  const jumpTo = useCallback((id: string, flash = false) => {
    const el = document.getElementById(id)
    if (!el) return
    const reduce = window.matchMedia('(prefers-reduced-motion: reduce)').matches
    el.scrollIntoView({ behavior: reduce ? 'auto' : 'smooth', block: 'start' })
    if (flash) {
      el.classList.remove('reader-fn-flash')
      // Reflow so re-adding the class restarts the highlight animation.
      void el.offsetWidth
      el.classList.add('reader-fn-flash')
      window.setTimeout(() => el.classList.remove('reader-fn-flash'), 1400)
    }
  }, [])

  // Wikilink navigation — keep clicks inside the reader. Capture phase so we run
  // before the editor's own broken-wikilink listener (which would otherwise open
  // the new-page dialog). We preventDefault every tela:// link and defer the
  // actual navigation decision to the caller's policy.
  const articleRef = useRef<HTMLDivElement | null>(null)
  useEffect(() => {
    const el = articleRef.current
    if (!el) return
    function onClick(e: MouseEvent) {
      const target = e.target as HTMLElement | null
      // Footnote reference (a <sup>, not an <a>) → jump to its definition.
      const fnref = target?.closest('sup[data-type="footnote_reference"]') as
        | HTMLElement
        | null
      if (fnref) {
        e.preventDefault()
        e.stopPropagation()
        const label = fnref.dataset.label
        if (label) jumpTo(`fn-${label}`, true)
        return
      }
      const anchor = target?.closest('a')
      if (!anchor) return
      // Footnote back-link → jump to the originating reference.
      if (anchor.classList.contains('reader-footnote-back')) {
        e.preventDefault()
        e.stopPropagation()
        jumpTo((anchor.getAttribute('href') ?? '').slice(1), true)
        return
      }
      // Heading copy-link anchor: copy the absolute deep-link, scroll into view,
      // and reflect the hash in the URL — without a full hashchange jump.
      if (anchor.classList.contains('reader-anchor')) {
        e.preventDefault()
        e.stopPropagation()
        const hash = anchor.getAttribute('href') ?? ''
        const url = `${window.location.origin}${window.location.pathname}${window.location.search}${hash}`
        void navigator.clipboard?.writeText(url).catch(() => {})
        window.history.replaceState(null, '', url)
        jumpTo(hash.slice(1))
        anchor.dataset.copied = 'true'
        window.setTimeout(() => delete anchor.dataset.copied, 1100)
        return
      }
      const id = parseWikilinkPageId(anchor.getAttribute('href') ?? '')
      if (id == null) return
      e.preventDefault()
      e.stopPropagation()
      onNavigateWikilink(id)
    }
    el.addEventListener('click', onClick, true)
    return () => el.removeEventListener('click', onClick, true)
  }, [onNavigateWikilink, jumpTo])

  return (
    <div
      className="tela-reader"
      data-reading-size={size}
      data-reading-font={font}
      // Present only with a left rail (public reader): re-centers the article and
      // moves the TOC to the right gutter so two rails don't stack on the left.
      data-has-sidebar={sidebar ? 'true' : undefined}
    >
      <div ref={progressRef} className="reader-progress" aria-hidden />

      <header ref={topbarRef} className="reader-topbar" data-scrolled="false">
        <div className="reader-topbar-left">
          {topbarLeading}
          <span className="reader-topbar-title">{title || 'Untitled'}</span>
        </div>

        <div className="reader-topbar-right">
          {topbarTrailing}
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                variant="ghost"
                size="sm"
                aria-label="Reading display options"
                className="h-[var(--space-8)] px-[var(--space-3)]"
              >
                <Type width={16} height={16} />
                <span>Display</span>
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <div className="reader-prefs">
                <div className="reader-prefs-group">
                  <span className="reader-prefs-label">Text size</span>
                  <ToggleGroup
                    type="single"
                    value={size}
                    onValueChange={(v) => {
                      if (!v) return
                      setSize(v as ReaderSize)
                      writePref(SIZE_KEY, v)
                    }}
                    aria-label="Text size"
                  >
                    <ToggleGroupItem value="s">Small</ToggleGroupItem>
                    <ToggleGroupItem value="m">Medium</ToggleGroupItem>
                    <ToggleGroupItem value="l">Large</ToggleGroupItem>
                  </ToggleGroup>
                </div>
                <div className="reader-prefs-group">
                  <span className="reader-prefs-label">Typeface</span>
                  <ToggleGroup
                    type="single"
                    value={font}
                    onValueChange={(v) => {
                      if (!v) return
                      setFont(v as ReaderFont)
                      writePref(FONT_KEY, v)
                    }}
                    aria-label="Typeface"
                  >
                    <ToggleGroupItem value="sans">Sans</ToggleGroupItem>
                    <ToggleGroupItem value="serif">Serif</ToggleGroupItem>
                  </ToggleGroup>
                </div>
                <div className="reader-prefs-group">
                  <span className="reader-prefs-label">Theme</span>
                  <ToggleGroup
                    type="single"
                    value={theme}
                    onValueChange={(v) => {
                      if (!v) return
                      setTheme(v as ThemeName)
                      setThemeState(v as ThemeName)
                    }}
                    aria-label="Theme"
                  >
                    {THEMES.map((t) => (
                      <ToggleGroupItem key={t} value={t} className="capitalize">
                        {t}
                      </ToggleGroupItem>
                    ))}
                  </ToggleGroup>
                </div>
              </div>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </header>

      <div className="reader-body">
        {sidebar}
        {/* Roving target for the j/k keyboard layer (see lib/keys): in a
            reader, j/k scroll and [ / ] jump headings. */}
        <div ref={scrollRef} className="reader-scroll" data-keynav-region="reader">
          <div className="reader-grid">
            {toc.length > 1 ? (
              <nav className="reader-toc" aria-label="On this page">
                <p className="reader-toc-label">On this page</p>
                <ul className="reader-toc-list">
                  {toc.map((entry) => (
                    <li key={entry.id}>
                      <button
                        type="button"
                        className="reader-toc-link"
                        data-level={entry.level}
                        data-active={activeId === entry.id}
                        onClick={() => jumpTo(entry.id)}
                      >
                        {entry.text}
                      </button>
                    </li>
                  ))}
                </ul>
              </nav>
            ) : (
              // Keep the grid's first column present so the article stays
              // centered on wide viewports even when there's no TOC.
              <div className="reader-toc" aria-hidden />
            )}

            <article className="reader-article" ref={articleRef}>
              {enableLinkPreview ? (
                <WikilinkHoverPreview containerRef={articleRef} />
              ) : null}
              {coverImage ? (
                <img className="reader-cover" src={coverImage} alt="" />
              ) : null}
              <SummaryTitle
                summary={summary ?? null}
                hintClassName="absolute top-[var(--space-2)] left-[calc(-1*(var(--space-6)+var(--space-1)))] hidden sm:inline-flex"
              >
                <h1 className="reader-title">{title || 'Untitled'}</h1>
              </SummaryTitle>
              {attachmentStrip}
              <div className="reader-meta">
                {byline ? (
                  <>
                    <span className="reader-meta-byline">{byline}</span>
                    <span className="reader-meta-dot" aria-hidden />
                  </>
                ) : null}
                <span>{minutes} min read</span>
                <span className="reader-meta-dot" aria-hidden />
                {publishedAt ? (
                  <span>{postDateFromSqlite(publishedAt)}</span>
                ) : (
                  <span>Updated {relativeTimeFromSqlite(updatedAt)}</span>
                )}
                {sourceLabel ? (
                  <>
                    <span className="reader-meta-dot" aria-hidden />
                    <span className="reader-meta-source">{sourceLabel}</span>
                  </>
                ) : null}
              </div>
              {bodySlot ?? (
                <MarkdownView
                  key={`reader-${pageId}`}
                  body={body}
                  pageId={pageId}
                  // App read view lets members vote; share/public are read-only.
                  canVote={wikilinkMode === 'edit'}
                  resolveWikilink={resolveWikilink}
                  // Reader wikilink hrefs use the `tela://page/N` scheme the
                  // shell's click + hover-preview already intercept (see below);
                  // the browser treats the scheme as dead, so nav is fully ours.
                  pageHref={(id) => `tela://page/${id}`}
                  wikilinkUnresolved="plain"
                  onReady={handleContentReady}
                />
              )}
              {articleFooter}
            </article>
          </div>
        </div>
      </div>
    </div>
  )
}

