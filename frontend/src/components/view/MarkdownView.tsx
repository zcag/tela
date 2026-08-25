import {
  createContext,
  createElement,
  Fragment,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react'
import katex from 'katex'
import { refractor } from 'refractor/core'
import { parsePageMarkdown } from '../../lib/markdown/remark-stack'
import { stampHeadingIds } from '../../lib/markdown/heading-anchors'
import { configureRefractor } from '../../lib/milkdown/refractor-config'
import {
  CALLOUT_LABELS,
  type CalloutType,
} from '../../lib/markdown/transforms/callouts'
import { buildMermaidElement } from '../../lib/diagrams/mermaid'
import { buildChartWidget } from '../../lib/diagrams/chart'
import {
  buildCalendarGrid,
  CALENDAR_EVENT_RE,
} from '../../lib/blocks/calendar-grid'
import { accentForValue, statLineClass } from '../../lib/blocks/stat-trend'
import { enhanceTables } from '../../lib/blocks/table-enhance'
import { wikilinkSlug } from '../../lib/markdown/transforms/wikilink'
import { isSafeUrl } from '../../lib/markdown/remark-safe-links'
import { embedIframeSrc } from '../../lib/markdown/embed'
import { isPdf, PdfPreviewDialog } from '../ui/pdf-viewer'
import type { CommentThread } from '../../lib/comments/use-comments'
import { useCommentHighlights } from '../../lib/comments/useCommentHighlights'
import { cn } from '../../lib/utils'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '../../lib/api'
import { useMe } from '../../lib/queries/auth'
import { pageKeys } from '../../lib/queries/pages'
import {
  PollWidget,
  type PollData,
  type PollOption,
} from '../app/PollWidget'

// Context for renderers that need page-scoped data (excalidraw PNG URL, wikilink
// resolution; comments later). Provided by MarkdownView.
interface ViewContextValue {
  pageId?: number
  // Resolve a wikilink slug → target page id (null = broken). Omitted (e.g. in
  // a standalone preview) → wikilinks render as neutral styled spans, no href.
  resolveWikilink?: (slug: string) => number | null
  // Build an href for a resolved page id (route differs per surface: app vs
  // public vs share).
  pageHref?: (pageId: number) => string
  // How to render a wikilink whose target didn't resolve: 'broken' (red, the
  // app default — the page is missing) or 'plain' (neutral, for read/share
  // surfaces where out-of-scope links shouldn't shout "broken").
  wikilinkUnresolved?: 'broken' | 'plain'
  // Whether the current surface allows casting a poll vote (app read view =
  // true; public/share = false, read-only results). The backend is still the
  // authority — this only decides whether the vote affordance is shown.
  canVote?: boolean
}
const ViewContext = createContext<ViewContextValue>({})

// Read-only VIEW renderer (docs/view-edit-split.md): markdown → mdast (shared
// parse stack) → React. No ProseMirror, no Yjs, no editor chunk. It reuses the
// editor's exact parse transforms, refractor grammar set, KaTeX, and the same
// `tela-*` DOM classes, so the output matches the editor by construction.
//
// CSS HOOK (temporary): the content is wrapped in `.tela-milkdown .ProseMirror`
// so the existing reader/editor stylesheets apply verbatim with zero new CSS.
// A later phase extracts a shared `.tela-prose` class and drops the fake
// `.ProseMirror` hook. Until then, render this inside a `.tela-reader` scope to
// get the reading typography (see the Storybook story).
//
// Covers the full authorable palette (see scripts/blocks-manifest.mjs
// VIEW_RENDERED): core markdown, callout/highlight/math/code, diagrams, every
// directive block (pull-quote, embed, file, tabs, timeline, kanban, stats,
// calendar), wikilinks, and <details> collapsibles. Unknown nodes degrade
// gracefully by rendering their children so content is never dropped.

interface MdNode {
  type: string
  children?: MdNode[]
  value?: string
  [k: string]: unknown
}

let refractorReady = false
function ensureRefractor() {
  if (!refractorReady) {
    configureRefractor(refractor)
    refractorReady = true
  }
}

interface HastNode {
  type: string
  tagName?: string
  value?: string
  properties?: Record<string, unknown>
  children?: HastNode[]
}

function renderHast(node: HastNode, key: number): ReactNode {
  if (node.type === 'text') return node.value
  if (node.type === 'element' && node.tagName) {
    const cls = node.properties?.className
    const className = Array.isArray(cls)
      ? cls.join(' ')
      : typeof cls === 'string'
        ? cls
        : undefined
    return createElement(
      node.tagName,
      { key, className },
      node.children?.map((c, i) => renderHast(c, i)),
    )
  }
  return null
}

function CodeBlock({ lang, value }: { lang: string | null; value: string }) {
  ensureRefractor()
  let tree: HastNode | null = null
  if (lang && refractor.registered(lang)) {
    try {
      tree = refractor.highlight(value, lang) as unknown as HastNode
    } catch {
      tree = null
    }
  }
  const langClass = lang ? `language-${lang}` : undefined
  return (
    <pre className={langClass}>
      <code className={langClass}>
        {tree ? tree.children?.map((c, i) => renderHast(c, i)) : value}
      </code>
    </pre>
  )
}

function TexMath({ value, display }: { value: string; display: boolean }) {
  const html = katex.renderToString(value || '', {
    displayMode: display,
    throwOnError: false,
  })
  return display ? (
    <div className="tela-math-block" dangerouslySetInnerHTML={{ __html: html }} />
  ) : (
    <span className="tela-math-inline" dangerouslySetInnerHTML={{ __html: html }} />
  )
}

// Mounts an editor render-core element (mermaid/chart) into the React tree.
// Reuses the exact same builder the editor uses (lib/diagrams/*) — zero drift,
// and the heavy lib (mermaid/echarts) stays lazy.
function DiagramWidget({ kind, code }: { kind: 'mermaid' | 'chart'; code: string }) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const host = ref.current
    if (!host) return
    const el = kind === 'mermaid' ? buildMermaidElement(code) : buildChartWidget(code)
    host.appendChild(el)
    return () => {
      try {
        host.removeChild(el)
      } catch {
        /* already detached */
      }
    }
  }, [kind, code])
  return <div ref={ref} />
}

function ExcalidrawView({
  sceneHash,
  altText,
}: {
  sceneHash: string
  altText: string
}) {
  const { pageId } = useContext(ViewContext)
  // Empty / never-drawn diagram (slash-inserted but never saved → scene_hash
  // is ""). Mirror the editor's atom: a muted placeholder, never the raw JSON.
  if (!sceneHash || !pageId) {
    return (
      <div className="tela-excalidraw tela-excalidraw--empty" data-scene-hash="">
        <span className="tela-excalidraw-empty-label">[Empty diagram]</span>
      </div>
    )
  }
  return (
    <div className="tela-excalidraw" data-scene-hash={sceneHash}>
      <img
        src={`/api/diagrams/${pageId}/${sceneHash}.png`}
        alt={altText || 'Drawing'}
        loading="lazy"
      />
    </div>
  )
}

function WikilinkView({ target, alias }: { target: string; alias: string | null }) {
  const { resolveWikilink, pageHref, wikilinkUnresolved } = useContext(ViewContext)
  const slug = wikilinkSlug(target)
  const label = alias || target
  if (resolveWikilink && pageHref) {
    const id = resolveWikilink(slug)
    if (id != null) {
      return (
        <a className="tela-wikilink" href={pageHref(id)} data-wikilink-slug={slug}>
          {label}
        </a>
      )
    }
    // Unresolved: 'plain' (read/share — don't shout broken at readers) or the
    // app default 'broken' (red, the page is genuinely missing).
    const cls =
      wikilinkUnresolved === 'plain'
        ? 'tela-wikilink'
        : 'tela-wikilink tela-wikilink--broken'
    return (
      <span className={cls} data-wikilink-slug={slug}>
        {label}
      </span>
    )
  }
  // No resolver (preview) → neutral styled span, no navigation.
  return (
    <span className="tela-wikilink" data-wikilink-slug={slug}>
      {label}
    </span>
  )
}

// Recursive plain-text extraction — mirrors the editor blocks' headingText
// walkers (milkdown-tabs/kanban/stat-grid.ts) and PM's `textContent`.
function mdText(node: MdNode): string {
  const parts: string[] = []
  const walk = (n: MdNode) => {
    if (typeof n.value === 'string' && n.type !== 'html') parts.push(n.value)
    n.children?.forEach(walk)
  }
  walk(node)
  return parts.join('').trim()
}

interface HeadingSection {
  label: string
  content: MdNode[]
}

// Split a directive's children into `### Label` sections — the shared grouping
// the tabs/kanban/stats editor parse runners all use. `preface` is whatever
// precedes the first heading (tabs/kanban skip it, stats keeps it as an
// unlabeled tile — per-block choice, mirroring each runner).
function groupByHeading(children: MdNode[]): {
  preface: MdNode[]
  sections: HeadingSection[]
} {
  const preface: MdNode[] = []
  const sections: HeadingSection[] = []
  let cur: HeadingSection | null = null
  for (const child of children) {
    if (child.type === 'heading' && Number(child.depth) === 3) {
      cur = { label: mdText(child), content: [] }
      sections.push(cur)
    } else if (cur) {
      cur.content.push(child)
    } else {
      preface.push(child)
    }
  }
  return { preface, sections }
}

// Group a `:::tabs` directive's children into tabs by `### Label` headings —
// mirrors the editor schema's parse runner (milkdown-tabs.ts).
function groupTabs(children: MdNode[]): HeadingSection[] {
  const tabs = groupByHeading(children).sections.map((s) => ({
    label: s.label || 'Tab',
    content: s.content,
  }))
  if (tabs.length === 0) tabs.push({ label: 'Tab', content: children })
  return tabs
}

function TabsView({ nodes }: { nodes: MdNode[] }) {
  const tabs = useMemo(() => groupTabs(nodes), [nodes])
  const [active, setActive] = useState(0)
  // Panel visibility is CSS-driven off `.tela-tabs[data-active]` (editor.css),
  // matching the editor exactly.
  return (
    <div className="tela-tabs" data-active={active}>
      <div className="tela-tabs-strip">
        {tabs.map((t, i) => (
          <button
            key={i}
            type="button"
            className="tela-tabs-tab"
            data-active={i === active ? 'true' : 'false'}
            onClick={() => setActive(i)}
          >
            {t.label}
          </button>
        ))}
      </div>
      <div className="tela-tabs-panels">
        {tabs.map((t, i) => (
          <div key={i} className="tela-tab" data-label={t.label}>
            {t.content.map((n, j) => renderNode(n, j))}
          </div>
        ))}
      </div>
    </div>
  )
}

// First non-empty text node in a directive body (the URL for embed/file).
function directiveFirstText(node: MdNode): string {
  let found = ''
  const walk = (n: MdNode) => {
    if (found) return
    if (n.type === 'text' && typeof n.value === 'string' && n.value.trim()) {
      found = n.value.trim()
      return
    }
    n.children?.forEach(walk)
  }
  node.children?.forEach(walk)
  return found
}

function directiveAttrs(node: MdNode): Record<string, string> {
  const a = node.attributes
  return a && typeof a === 'object' ? (a as Record<string, string>) : {}
}

function fileExtLabel(name: string): string {
  const i = name.lastIndexOf('.')
  const ext = i >= 0 ? name.slice(i + 1) : ''
  return (ext || 'file').toUpperCase().slice(0, 4)
}

function prettySize(bytes: number): string {
  if (!bytes) return ''
  if (bytes < 1024) return `${bytes} B`
  const kb = bytes / 1024
  if (kb < 1024) return `${kb < 10 ? kb.toFixed(1) : Math.round(kb)} KB`
  const mb = kb / 1024
  return `${mb < 10 ? mb.toFixed(1) : Math.round(mb)} MB`
}

// Directive-block view renderers — mirror each editor schema's toDOM so the
// existing CSS applies. Pure display (no editing chrome).
function PullQuoteView({ node }: { node: MdNode }) {
  const cite = directiveAttrs(node).cite ?? ''
  return (
    <figure className="tela-pullquote" data-cite={cite}>
      <blockquote className="tela-pullquote-body">{renderChildren(node)}</blockquote>
      {cite ? <figcaption className="tela-pullquote-cite">{cite}</figcaption> : null}
    </figure>
  )
}

function EmbedView({ node }: { node: MdNode }) {
  const url = directiveFirstText(node)
  const src = embedIframeSrc(url)
  if (src) {
    return (
      <div className="tela-embed" data-url={url}>
        <iframe
          src={src}
          loading="lazy"
          allow="accelerometer; encrypted-media; gyroscope; picture-in-picture; fullscreen"
          allowFullScreen
          referrerPolicy="strict-origin-when-cross-origin"
          sandbox="allow-scripts allow-same-origin allow-popups allow-presentation"
        />
      </div>
    )
  }
  return (
    <div className="tela-embed tela-embed-link" data-url={url}>
      {url ? (
        <a href={url} target="_blank" rel="noopener noreferrer nofollow">
          {url}
        </a>
      ) : (
        <span className="tela-embed-empty">Empty embed</span>
      )}
    </div>
  )
}

function FileView({ node }: { node: MdNode }) {
  const url = directiveFirstText(node)
  const attrs = directiveAttrs(node)
  const name = attrs.name ?? ''
  const size = Number(attrs.size ?? '0') || 0
  const [preview, setPreview] = useState(false)
  const pdf = isPdf(name || url)
  const chip = (
    <a
      className="tela-file"
      href={url || '#'}
      download={name || undefined}
      data-url={url}
      data-name={name}
      data-size={String(size)}
    >
      <span className="tela-file-ext">{fileExtLabel(name || url)}</span>
      <span className="tela-file-name">{name || url || 'file'}</span>
      {size ? <span className="tela-file-size">{prettySize(size)}</span> : null}
    </a>
  )
  if (!pdf || !url) return chip
  return (
    <span className="tela-file-row">
      {chip}
      <button
        type="button"
        className="tela-file-preview"
        onClick={() => setPreview(true)}
      >
        Preview
      </button>
      <PdfPreviewDialog
        url={url}
        name={name}
        open={preview}
        onOpenChange={setPreview}
      />
    </span>
  )
}

// Kanban (:::kanban) — static board: `### Column` headings become columns, the
// list under each heading the cards. Same DOM + classes as the editor's
// nodeViews (milkdown-kanban.ts), minus the drag/retitle chrome (read-only).
function KanbanView({ node }: { node: MdNode }) {
  const { sections } = groupByHeading(node.children ?? [])
  const cols = sections.length
    ? sections.map((s) => ({ title: s.label || 'Column', content: s.content }))
    : [{ title: 'Column', content: node.children ?? [] }]
  return (
    <div className="tela-kanban" data-kanban="">
      {cols.map((col, i) => (
        <div key={i} className="tela-kanban-col" data-title={col.title}>
          <div className="tela-kanban-col-header">
            <div className="tela-kanban-col-title">{col.title}</div>
          </div>
          <div className="tela-kanban-col-body">
            {col.content.map((n, j) => renderNode(n, j))}
          </div>
        </div>
      ))}
    </div>
  )
}

// Stat grid (:::stats) — KPI tiles. `### Label` headings become tiles; body
// paragraphs are classified value / trend / description with the shared
// helpers (lib/blocks/stat-trend.ts), so colours and the accent rail match the
// editor's decorations exactly.
function StatTile({ label, content }: { label: string; content: MdNode[] }) {
  let paraIdx = 0
  return (
    <div
      className="tela-stat"
      data-stat-tile=""
      data-label={label}
      data-accent={accentForValue(content.map(mdText).join('\n'))}
    >
      <div className="tela-stat-head">
        <span className="tela-stat-label">{label}</span>
      </div>
      <div className="tela-stat-value">
        {content.map((n, i) => {
          if (n.type !== 'paragraph') return renderNode(n, i)
          const cls =
            paraIdx++ === 0 ? 'tela-stat-figure' : statLineClass(mdText(n))
          return (
            <p key={i} className={cls}>
              {renderChildren(n)}
            </p>
          )
        })}
      </div>
    </div>
  )
}

function StatGridView({ node }: { node: MdNode }) {
  // Content before the first heading becomes an unlabeled tile — mirrors the
  // editor's parse runner (milkdown-stat-grid.ts).
  const { preface, sections } = groupByHeading(node.children ?? [])
  const tiles = [
    ...(preface.length ? [{ label: '', content: preface }] : []),
    ...sections,
  ]
  return (
    <div className="tela-stats" data-stat-grid="">
      {tiles.map((t, i) => (
        <StatTile key={i} label={t.label} content={t.content} />
      ))}
    </div>
  )
}

// Pull `{date -> [titles]}` from the calendar's event list — the mdast twin of
// the editor's PM-based collectEvents (milkdown-calendar.ts): one event per
// top-level list item whose text starts with an ISO date; non-conforming items
// are ignored (same as read-only editor mode, where the source list is hidden).
function collectCalendarEvents(node: MdNode): Map<string, string[]> {
  const byDay = new Map<string, string[]>()
  const walk = (n: MdNode) => {
    if (n.type === 'listItem') {
      const line = mdText(n).split('\n')[0]
      const m = CALENDAR_EVENT_RE.exec(line)
      if (m) {
        const list = byDay.get(m[1]) ?? []
        list.push(m[2].trim())
        byDay.set(m[1], list)
      }
      return // don't descend into nested lists
    }
    n.children?.forEach(walk)
  }
  node.children?.forEach(walk)
  return byDay
}

// Calendar (:::calendar{month}) — mounts the exact grid builder the editor's
// nodeView uses (lib/blocks/calendar-grid.ts), DiagramWidget-style. View shows
// only the grid, matching the editor's read-only mode (source list hidden).
function CalendarView({ node }: { node: MdNode }) {
  const month = directiveAttrs(node).month ?? ''
  const events = useMemo(() => collectCalendarEvents(node), [node])
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const host = ref.current
    if (!host) return
    const grid = buildCalendarGrid(month, events)
    host.appendChild(grid)
    return () => grid.remove()
  }, [month, events])
  return (
    <div
      ref={ref}
      className="tela-calendar"
      data-month={month}
      data-editable="false"
    />
  )
}

// --- Poll ------------------------------------------------------------------
// A `:::poll{id}` block. Its definition (question + options) AND its votes both
// live in the body: options are top-level list items; each voter is a nested
// `- @handle` under the option they chose (see backend internal/pollmd). This
// view parses that, resolves handles → names, and renders the interactive
// PollWidget. Casting posts a structured body edit (churn-free) and refetches.

interface ParsedPollOption {
  label: string
  handles: string[]
}

function parsePoll(node: MdNode): {
  question: string
  options: ParsedPollOption[]
} {
  let question = ''
  const options: ParsedPollOption[] = []
  for (const child of node.children ?? []) {
    if (child.type === 'heading' && !question) {
      question = mdText(child).trim()
    } else if (child.type === 'list') {
      for (const li of child.children ?? []) {
        if (li.type !== 'listItem') continue
        const para = (li.children ?? []).find((c) => c.type === 'paragraph')
        const label = para ? mdText(para).trim() : ''
        if (!label) continue
        const sub = (li.children ?? []).find((c) => c.type === 'list')
        const handles: string[] = []
        for (const vli of sub?.children ?? []) {
          const t = mdText(vli).trim()
          if (t.startsWith('@')) handles.push(t.slice(1).split(/\s+/)[0])
        }
        options.push({ label, handles })
      }
    }
  }
  return { question, options }
}

// Stable synthetic id for an unresolved handle so avatar tones don't jump once
// /resolve returns.
function handleFallbackId(handle: string): number {
  let h = 0
  for (let i = 0; i < handle.length; i++) h = (h * 31 + handle.charCodeAt(i)) | 0
  return Math.abs(h) || 1
}

function PollBlockView({ node }: { node: MdNode }) {
  const { pageId, canVote } = useContext(ViewContext)
  const me = useMe()
  const qc = useQueryClient()
  const parsed = useMemo(() => parsePoll(node), [node])
  const attrs = directiveAttrs(node)
  const pollId = attrs.id ?? ''
  const closed = 'closed' in attrs
  const username = me.data?.username

  const handles = useMemo(
    () => Array.from(new Set(parsed.options.flatMap((o) => o.handles))),
    [parsed],
  )

  // Resolve voter handles → display names on authed surfaces; public/share fall
  // back to showing the raw handle (the resolve endpoint requires a session).
  const resolved = useQuery({
    queryKey: ['users', 'resolve', [...handles].sort().join(',')],
    enabled: !!username && handles.length > 0,
    staleTime: 5 * 60_000,
    queryFn: () =>
      api<{ users: { id: number; handle: string; name: string }[] }>(
        '/api/users/resolve',
        { method: 'POST', body: JSON.stringify({ handles }) },
      ),
  })
  const nameByHandle = useMemo(() => {
    const m = new Map<string, { id: number; name: string }>()
    for (const u of resolved.data?.users ?? []) {
      m.set(u.handle, { id: u.id, name: u.name })
    }
    return m
  }, [resolved.data])

  const vote = useMutation({
    mutationFn: (choice: string) =>
      api(`/api/pages/${pageId}/polls/${encodeURIComponent(pollId)}/vote`, {
        method: 'POST',
        body: JSON.stringify({ choice }),
      }),
    onSuccess: () => {
      if (pageId != null) {
        void qc.invalidateQueries({ queryKey: pageKeys.detail(pageId) })
      }
    },
  })

  const options: PollOption[] = parsed.options.map((o) => ({
    id: o.label,
    label: o.label,
    count: o.handles.length,
    voters: o.handles.map((h) => {
      const r = nameByHandle.get(h)
      return { id: r?.id ?? handleFallbackId(h), name: r?.name ?? h, handle: h }
    }),
  }))
  const myChoice =
    username != null
      ? (parsed.options.find((o) => o.handles.includes(username))?.label ?? null)
      : null

  const poll: PollData = {
    id: pollId,
    question: parsed.question || 'Poll',
    options,
    myChoice,
    closed,
    allowChange: true,
    canVote: !!canVote && !!username && !closed && pageId != null,
  }

  return <PollWidget poll={poll} onVote={(choice) => vote.mutate(choice)} />
}

function renderChildren(node: MdNode): ReactNode[] {
  return (node.children ?? []).map((child, i) => renderNode(child, i))
}

function renderNode(node: MdNode, key: number | string): ReactNode {
  switch (node.type) {
    case 'text':
      return node.value
    case 'paragraph':
      return <p key={key}>{renderChildren(node)}</p>
    case 'heading': {
      const depth = Math.min(Math.max(Number(node.depth) || 1, 1), 6)
      return createElement(`h${depth}`, { key }, renderChildren(node))
    }
    case 'strong':
      return <strong key={key}>{renderChildren(node)}</strong>
    case 'emphasis':
      return <em key={key}>{renderChildren(node)}</em>
    case 'delete':
      return <del key={key}>{renderChildren(node)}</del>
    case 'inlineCode':
      return <code key={key}>{node.value}</code>
    case 'break':
      return <br key={key} />
    case 'thematicBreak':
      return <hr key={key} />
    case 'blockquote':
      return <blockquote key={key}>{renderChildren(node)}</blockquote>
    case 'link':
      return (
        <a
          key={key}
          href={isSafeUrl(String(node.url ?? '')) ? String(node.url ?? '') : '#'}
          title={node.title ? String(node.title) : undefined}
        >
          {renderChildren(node)}
        </a>
      )
    case 'image':
      return (
        <img
          key={key}
          src={String(node.url ?? '')}
          alt={String(node.alt ?? '')}
          title={node.title ? String(node.title) : undefined}
          loading="lazy"
        />
      )
    case 'list': {
      const ordered = Boolean(node.ordered)
      const start = node.start == null ? undefined : Number(node.start)
      return ordered ? (
        <ol key={key} start={start}>
          {renderChildren(node)}
        </ol>
      ) : (
        <ul key={key}>{renderChildren(node)}</ul>
      )
    }
    case 'listItem': {
      const checked = node.checked
      // Match the editor's task-item DOM: `li[data-item-type=task][data-checked]`
      // — the checkbox is a CSS ::before in the gutter (editor.css), so we reuse
      // it verbatim and the text flows inline.
      if (checked === true || checked === false) {
        return (
          <li key={key} data-item-type="task" data-checked={String(checked)}>
            {renderChildren(node)}
          </li>
        )
      }
      return <li key={key}>{renderChildren(node)}</li>
    }
    case 'code': {
      const lang = node.lang == null ? null : String(node.lang)
      const value = String(node.value ?? '')
      // mermaid / chart render as diagrams (view shows only the result, not the
      // source) via the shared editor render cores.
      if (lang === 'mermaid') return <DiagramWidget key={key} kind="mermaid" code={value} />
      if (lang === 'chart') return <DiagramWidget key={key} kind="chart" code={value} />
      return <CodeBlock key={key} lang={lang} value={value} />
    }
    case 'excalidraw':
      return (
        <ExcalidrawView
          key={key}
          sceneHash={String(node.sceneHash ?? '')}
          altText={String(node.altText ?? '')}
        />
      )
    case 'table': {
      const align = (node.align as (string | null)[] | undefined) ?? []
      const rows = node.children ?? []
      const [head, ...bodyRows] = rows
      return (
        <table key={key}>
          {head ? (
            <thead>
              <tr>
                {(head.children ?? []).map((cell, i) => (
                  <th key={i} style={alignStyle(align[i])}>
                    {renderChildren(cell)}
                  </th>
                ))}
              </tr>
            </thead>
          ) : null}
          <tbody>
            {bodyRows.map((row, r) => (
              <tr key={r}>
                {(row.children ?? []).map((cell, i) => (
                  <td key={i} style={alignStyle(align[i])}>
                    {renderChildren(cell)}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      )
    }
    case 'callout': {
      const type = calloutType(node.calloutType)
      return (
        <div
          key={key}
          className={`tela-callout tela-callout-${type}`}
          data-callout-type={type}
        >
          <div className="tela-callout-header">
            <span
              className="tela-callout-icon"
              data-callout-icon={type}
              aria-hidden="true"
            />
            <span className="tela-callout-label">{CALLOUT_LABELS[type]}</span>
          </div>
          <div className="tela-callout-body">{renderChildren(node)}</div>
        </div>
      )
    }
    case 'highlight':
      return (
        <mark key={key} className="tela-highlight">
          {renderChildren(node)}
        </mark>
      )
    case 'wikilink':
      return (
        <WikilinkView
          key={key}
          target={String(node.target ?? '')}
          alias={node.alias == null ? null : String(node.alias)}
        />
      )
    case 'math':
      return <TexMath key={key} value={String(node.value ?? '')} display />
    case 'inlineMath':
      return <TexMath key={key} value={String(node.value ?? '')} display={false} />
    case 'footnoteReference': {
      const id = String(node.identifier ?? '')
      const label = String(node.label ?? id)
      return (
        <sup key={key} className="reader-footnote-ref" id={`fnref-${id}`}>
          <a href={`#fn-${id}`}>{label}</a>
        </sup>
      )
    }
    case 'footnoteDefinition': {
      const id = String(node.identifier ?? '')
      return (
        <div key={key} className="reader-footnote-def" id={`fn-${id}`}>
          <sup>{String(node.label ?? id)}</sup> {renderChildren(node)}
        </div>
      )
    }
    case 'containerDirective':
    case 'leafDirective':
    case 'textDirective': {
      const name = String(node.name ?? '')
      if (name === 'tabs') return <TabsView key={key} nodes={node.children ?? []} />
      if (name === 'quote') return <PullQuoteView key={key} node={node} />
      if (name === 'embed') return <EmbedView key={key} node={node} />
      if (name === 'file') return <FileView key={key} node={node} />
      if (name === 'timeline') {
        return (
          <div key={key} className="tela-timeline" data-timeline="">
            {renderChildren(node)}
          </div>
        )
      }
      if (name === 'kanban') return <KanbanView key={key} node={node} />
      if (name === 'stats') return <StatGridView key={key} node={node} />
      if (name === 'calendar') return <CalendarView key={key} node={node} />
      if (name === 'poll') return <PollBlockView key={key} node={node} />
      // Unknown directive — render its children so no content is lost. A
      // Fragment avoids wrapping (possibly block) content in an invalid element.
      return node.children ? (
        <Fragment key={key}>{renderChildren(node)}</Fragment>
      ) : null
    }
    case 'details':
      // Collapsible — grouped by the shared collapsiblesRemark transform.
      // Native <details> gives the toggle for free; the saved `open` attr is
      // honored (matching the editor's read-only nodeView), default closed.
      return (
        <details key={key} className="tela-details" open={Boolean(node.open)}>
          <summary className="tela-details-summary">
            {String(node.summary ?? '')}
          </summary>
          {renderChildren(node)}
        </details>
      )
    case 'html':
      // Remaining raw HTML (anything but the <details> form above) is dropped
      // rather than dangerously injected; any body content between open/close
      // tags renders as normal siblings.
      return null
    default:
      // Unknown node — degrade gracefully by rendering children (no wrapper, so
      // block content stays valid) so nothing is lost.
      return node.children ? (
        <Fragment key={key}>{renderChildren(node)}</Fragment>
      ) : null
  }
}

function alignStyle(a: string | null | undefined) {
  return a ? { textAlign: a as 'left' | 'right' | 'center' } : undefined
}

function calloutType(raw: unknown): CalloutType {
  const t = typeof raw === 'string' ? raw : 'note'
  return (CALLOUT_LABELS as Record<string, string>)[t] ? (t as CalloutType) : 'note'
}

export function MarkdownView({
  body,
  pageId,
  resolveWikilink,
  pageHref,
  wikilinkUnresolved,
  commentThreads,
  onCommentClick,
  onReady,
  canVote,
  className,
}: {
  body: string
  /** Page id — needed to build the excalidraw PNG URL. */
  pageId?: number
  /** Resolve a wikilink slug → target page id (null = broken). */
  resolveWikilink?: (slug: string) => number | null
  /** Build an href for a resolved page id (surface-specific routing). */
  pageHref?: (pageId: number) => string
  /** How unresolved wikilinks render: 'broken' (default) or 'plain'. */
  wikilinkUnresolved?: 'broken' | 'plain'
  /** Comment threads to paint as inline highlights (read view). */
  commentThreads?: CommentThread[] | null
  /** Open a thread when its highlighted passage is clicked. */
  onCommentClick?: (threadId: number) => void
  /** Fires with the rendered content element after each render — lets a host
   *  (the reader) run DOM post-processing (TOC, footnotes, scroll-spy). */
  onReady?: (el: HTMLElement) => void
  /** Show the poll vote affordance (app read view). Omit on public/share. */
  canVote?: boolean
  className?: string
}) {
  const tree = useMemo(() => parsePageMarkdown(body), [body])
  const ctx = useMemo<ViewContextValue>(
    () => ({ pageId, resolveWikilink, pageHref, wikilinkUnresolved, canVote }),
    [pageId, resolveWikilink, pageHref, wikilinkUnresolved, canVote],
  )
  const contentRef = useRef<HTMLDivElement>(null)
  useCommentHighlights(contentRef, commentThreads, onCommentClick)
  useEffect(() => {
    if (!contentRef.current) return
    // Anchor ids first, so a host's onReady (TOC, deep-link scroll) sees them.
    stampHeadingIds(contentRef.current)
    // Sort headers + a filter box on large tables. Self-gated at ≥8 rows and
    // idempotent. This is where every read surface gets it: the editor plugin
    // that used to apply it only runs inside Milkdown, which no longer renders
    // any of them.
    enhanceTables(contentRef.current)
    onReady?.(contentRef.current)
  }, [body, onReady])
  return (
    <ViewContext.Provider value={ctx}>
      <div className={cn('tela-milkdown', className)}>
        {/* Temporary `.ProseMirror` CSS hook — see file header. `whiteSpace:
            normal` overrides the editor's `pre-wrap` so markdown soft-wraps
            collapse to spaces (correct for a static HTML view) instead of
            rendering as hard line breaks. Drops out with the `.tela-prose`
            extraction. */}
        <div
          ref={contentRef}
          className="ProseMirror"
          data-tela-view=""
          style={{ whiteSpace: 'normal' }}
        >
          {renderChildren(tree as unknown as MdNode)}
        </div>
      </div>
    </ViewContext.Provider>
  )
}
