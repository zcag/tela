import { useEffect, useMemo, useState } from 'react'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { Crosshair, Maximize2, Search, X } from 'lucide-react'
import {
  buildSpacePalette,
  isStaleNode,
  neighborhood,
  useGraph,
  type GraphLink,
  type GraphNode,
} from '../../lib/queries/graph'
import { navigateToPage } from '../../lib/pageHitItem'
import { Button } from '../ui/button'
import { PageGraph } from './PageGraph'
import { useDocumentTitle } from '../../lib/useHeadMeta'

interface GraphSearchParams {
  focus?: number
  space?: number
}

export function GraphRoute() {
  useDocumentTitle('Graph')
  const { focus, space } = useSearch({ from: '/_app/graph' }) as GraphSearchParams
  const navigate = useNavigate()
  const [showSemantic, setShowSemantic] = useState(false)
  const graph = useGraph(space, showSemantic)

  const [showLinks, setShowLinks] = useState(true)
  const [showTree, setShowTree] = useState(true)
  const [showTrust, setShowTrust] = useState(false)
  const [recency, setRecency] = useState(false)
  const [orphansOnly, setOrphansOnly] = useState(false)
  const [query, setQuery] = useState('')
  const [hiddenSpaces, setHiddenSpaces] = useState<Set<number>>(new Set())
  const [showStats, setShowStats] = useState(false)
  const [fitNonce, setFitNonce] = useState(0)
  // Refit whenever the focus target changes or Fit is pressed.
  const fitSignal = `${focus ?? 'all'}:${fitNonce}`

  const allNodes = useMemo(() => graph.data?.nodes ?? [], [graph.data])
  const allLinks = useMemo(() => graph.data?.links ?? [], [graph.data])
  const palette = useMemo(() => buildSpacePalette(allNodes), [allNodes])

  // Esc clears search → focus → (nothing).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      if (query) setQuery('')
      else if (focus != null)
        navigate({ to: '/graph', search: (s) => ({ ...s, focus: undefined }) })
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [query, focus, navigate])

  const { nodes, links } = useMemo(() => {
    let keep = new Set(allNodes.map((n) => n.id))
    if (focus != null) keep = neighborhood(focus, allLinks, 2)
    keep = new Set(
      [...keep].filter((id) => {
        const n = allNodes.find((x) => x.id === id)
        return n != null && !hiddenSpaces.has(n.space_id)
      }),
    )
    if (orphansOnly) {
      const connected = new Set<number>()
      for (const l of allLinks) {
        connected.add(l.source)
        connected.add(l.target)
      }
      keep = new Set([...keep].filter((id) => !connected.has(id)))
    }
    const ns = allNodes.filter((n) => keep.has(n.id))
    const ls = allLinks.filter((l) => keep.has(l.source) && keep.has(l.target))
    return { nodes: ns, links: ls }
  }, [allNodes, allLinks, focus, orphansOnly, hiddenSpaces])

  const matchedIds = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return null
    return new Set(nodes.filter((n) => n.title.toLowerCase().includes(q)).map((n) => n.id))
  }, [nodes, query])

  const stats = useMemo(() => computeStats(nodes, links), [nodes, links])

  const onNavigate = (id: number) => {
    const n = allNodes.find((x) => x.id === id)
    if (n) navigateToPage(n.space_id, id)
  }
  const toggleSpace = (id: number) =>
    setHiddenSpaces((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  return (
    <div className="flex h-full flex-col">
      {/* Controls */}
      <div className="flex flex-wrap items-center gap-[var(--space-3)] border-b border-[var(--border-subtle)] px-[var(--space-5)] py-[var(--space-3)]">
        <h1 className="m-0 text-[length:var(--text-base)] font-medium text-[var(--text-primary)]">
          Graph
        </h1>
        {focus != null ? (
          <Button
            variant="secondary"
            size="sm"
            onClick={() =>
              navigate({ to: '/graph', search: (s) => ({ ...s, focus: undefined }) })
            }
          >
            <X width={14} height={14} />
            Focused — show all
          </Button>
        ) : null}

        <div className="relative ml-auto">
          <Search
            width={14}
            height={14}
            className="pointer-events-none absolute left-[var(--space-2)] top-1/2 -translate-y-1/2 text-[var(--text-muted)]"
            aria-hidden
          />
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Highlight…"
            aria-label="Highlight pages"
            className="h-[var(--space-8)] w-[12rem] rounded-[var(--radius-sm)] border border-[var(--border-subtle)] bg-[var(--surface-1)] pl-[calc(var(--space-2)*2+var(--space-2))] pr-[var(--space-2)] text-[length:var(--text-sm)] text-[var(--text-primary)] outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]"
          />
        </div>

        <ToggleChip active={showLinks} onClick={() => setShowLinks((v) => !v)}>
          Links
        </ToggleChip>
        <ToggleChip active={showTree} onClick={() => setShowTree((v) => !v)}>
          Hierarchy
        </ToggleChip>
        <ToggleChip active={showSemantic} onClick={() => setShowSemantic((v) => !v)}>
          Semantic
        </ToggleChip>
        <ToggleChip active={showTrust} onClick={() => setShowTrust((v) => !v)}>
          Trust
        </ToggleChip>
        <ToggleChip active={recency} onClick={() => setRecency((v) => !v)}>
          Recency
        </ToggleChip>
        <ToggleChip active={orphansOnly} onClick={() => setOrphansOnly((v) => !v)}>
          Orphans
        </ToggleChip>
        <ToggleChip active={showStats} onClick={() => setShowStats((v) => !v)}>
          Stats
        </ToggleChip>
        <Button
          variant="ghost"
          size="sm"
          aria-label="Fit graph to view"
          title="Fit to view"
          onClick={() => setFitNonce((n) => n + 1)}
        >
          <Maximize2 width={14} height={14} />
        </Button>
      </div>

      {/* Canvas + overlays */}
      <div className="relative min-h-0 flex-1">
        {graph.isLoading ? (
          <CenterNote>Loading graph…</CenterNote>
        ) : nodes.length === 0 ? (
          <CenterNote>
            {orphansOnly
              ? 'No orphan pages — everything is linked.'
              : 'No pages to graph yet.'}
          </CenterNote>
        ) : (
          <PageGraph
            nodes={nodes}
            links={links}
            showLinks={showLinks}
            showTree={showTree}
            showSemantic={showSemantic}
            showTrust={showTrust}
            recency={recency}
            currentId={focus ?? null}
            matchedIds={matchedIds}
            onNavigate={onNavigate}
            fitSignal={fitSignal}
          />
        )}

        {/* Legend — click a space to show/hide it. */}
        {palette.length > 1 ? (
          <div className="absolute bottom-[var(--space-3)] left-[var(--space-3)] flex max-w-[16rem] flex-col gap-px rounded-[var(--radius-md)] border border-[var(--border-subtle)] bg-[color-mix(in_srgb,var(--surface-1)_90%,transparent)] p-[var(--space-1)] backdrop-blur">
            {palette.map((p) => {
              const hidden = hiddenSpaces.has(p.spaceId)
              return (
                <button
                  key={p.spaceId}
                  type="button"
                  onClick={() => toggleSpace(p.spaceId)}
                  aria-pressed={!hidden}
                  className="flex items-center gap-[var(--space-2)] rounded-[var(--radius-sm)] px-[var(--space-2)] py-[var(--space-1)] text-left text-[length:var(--text-xs)] text-[var(--text-muted)] hover:bg-[var(--surface-2)]"
                  style={{ opacity: hidden ? 0.4 : 1 }}
                >
                  <span
                    className="inline-block h-[var(--space-2)] w-[var(--space-2)] shrink-0 rounded-full"
                    style={{ background: `var(${p.varName})` }}
                    aria-hidden
                  />
                  <span className="truncate">{p.spaceName}</span>
                </button>
              )
            })}
          </div>
        ) : null}

        {showStats ? (
          <StatsPanel stats={stats} onNavigate={onNavigate} onClose={() => setShowStats(false)} />
        ) : null}
      </div>
    </div>
  )
}

interface Stats {
  hubs: { id: number; title: string; deg: number }[]
  recent: { id: number; title: string }[]
  disputed: { id: number; title: string; n: number }[]
  stale: { id: number; title: string }[]
  orphanCount: number
  brokenCount: number
}

function computeStats(nodes: GraphNode[], links: GraphLink[]): Stats {
  const deg = new Map<number, number>()
  const connected = new Set<number>()
  for (const l of links) {
    deg.set(l.source, (deg.get(l.source) ?? 0) + 1)
    deg.set(l.target, (deg.get(l.target) ?? 0) + 1)
    connected.add(l.source)
    connected.add(l.target)
  }
  const hubs = [...nodes]
    .map((n) => ({ id: n.id, title: n.title || 'Untitled', deg: deg.get(n.id) ?? 0 }))
    .sort((a, b) => b.deg - a.deg)
    .slice(0, 6)
  const recent = [...nodes]
    .sort((a, b) => (a.updated_at < b.updated_at ? 1 : -1))
    .slice(0, 6)
    .map((n) => ({ id: n.id, title: n.title || 'Untitled' }))
  // Trust health: disputed first (most disputes first), then the stale worklist.
  const disputed = nodes
    .filter((n) => (n.dispute ?? 0) > 0)
    .sort((a, b) => (b.dispute ?? 0) - (a.dispute ?? 0))
    .map((n) => ({ id: n.id, title: n.title || 'Untitled', n: n.dispute ?? 0 }))
  const stale = nodes
    .filter((n) => isStaleNode(n.updated_at))
    .sort((a, b) => (a.updated_at < b.updated_at ? -1 : 1))
    .map((n) => ({ id: n.id, title: n.title || 'Untitled' }))
  return {
    hubs,
    recent,
    disputed,
    stale,
    orphanCount: nodes.filter((n) => !connected.has(n.id)).length,
    brokenCount: nodes.filter((n) => n.broken > 0).length,
  }
}

function StatsPanel({
  stats,
  onNavigate,
  onClose,
}: {
  stats: Stats
  onNavigate: (id: number) => void
  onClose: () => void
}) {
  return (
    <div className="absolute right-[var(--space-3)] top-[var(--space-3)] flex w-[15rem] flex-col gap-[var(--space-3)] rounded-[var(--radius-md)] border border-[var(--border-subtle)] bg-[color-mix(in_srgb,var(--surface-1)_92%,transparent)] p-[var(--space-3)] backdrop-blur">
      <div className="flex items-center justify-between">
        <span className="text-[length:var(--text-sm)] font-medium text-[var(--text-primary)]">
          Stats
        </span>
        <button
          type="button"
          onClick={onClose}
          aria-label="Close stats"
          className="text-[var(--text-muted)] hover:text-[var(--text-primary)]"
        >
          <X width={14} height={14} />
        </button>
      </div>
      <div className="flex flex-wrap gap-x-[var(--space-3)] gap-y-[var(--space-1)] text-[length:var(--text-xs)] text-[var(--text-muted)]">
        <span>{stats.orphanCount} orphans</span>
        <span>{stats.brokenCount} broken-link</span>
        {stats.disputed.length > 0 ? (
          <span className="text-[var(--danger)]">{stats.disputed.length} disputed</span>
        ) : null}
        {stats.stale.length > 0 ? (
          <span className="text-[var(--warning)]">{stats.stale.length} stale</span>
        ) : null}
      </div>
      {stats.disputed.length > 0 ? (
        <StatList
          label="Disputed"
          rows={stats.disputed.map((d) => ({ id: d.id, title: d.title, meta: `${d.n}` }))}
          onNavigate={onNavigate}
        />
      ) : null}
      {stats.stale.length > 0 ? (
        <StatList
          label="Stale — needs review"
          rows={stats.stale.slice(0, 8).map((s) => ({ id: s.id, title: s.title }))}
          onNavigate={onNavigate}
        />
      ) : null}
      <StatList label="Most connected" rows={stats.hubs.map((h) => ({ id: h.id, title: h.title, meta: `${h.deg}` }))} onNavigate={onNavigate} />
      <StatList label="Recently updated" rows={stats.recent.map((r) => ({ id: r.id, title: r.title }))} onNavigate={onNavigate} />
    </div>
  )
}

function StatList({
  label,
  rows,
  onNavigate,
}: {
  label: string
  rows: { id: number; title: string; meta?: string }[]
  onNavigate: (id: number) => void
}) {
  return (
    <div className="flex flex-col gap-[var(--space-1)]">
      <span className="text-[length:var(--text-xs)] font-medium uppercase tracking-wide text-[var(--text-muted)]">
        {label}
      </span>
      {rows.map((r) => (
        <button
          key={r.id}
          type="button"
          onClick={() => onNavigate(r.id)}
          className="flex items-center gap-[var(--space-2)] rounded-[var(--radius-sm)] px-[var(--space-1)] py-px text-left text-[length:var(--text-xs)] text-[var(--text-primary)] hover:bg-[var(--surface-2)]"
        >
          <Crosshair width={11} height={11} className="shrink-0 text-[var(--text-muted)]" aria-hidden />
          <span className="flex-1 truncate">{r.title}</span>
          {r.meta ? <span className="text-[var(--text-muted)]">{r.meta}</span> : null}
        </button>
      ))}
    </div>
  )
}

function ToggleChip({
  active,
  onClick,
  children,
}: {
  active: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={[
        'h-[var(--space-7)] rounded-[var(--radius-sm)] border px-[var(--space-3)] text-[length:var(--text-sm)] transition-colors',
        active
          ? 'border-[var(--accent)] bg-[color-mix(in_srgb,var(--accent)_12%,transparent)] text-[var(--text-primary)]'
          : 'border-[var(--border-subtle)] text-[var(--text-muted)] hover:text-[var(--text-primary)]',
      ].join(' ')}
    >
      {children}
    </button>
  )
}

function CenterNote({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex h-full items-center justify-center p-[var(--space-7)] text-center text-[length:var(--text-sm)] text-[var(--text-muted)]">
      {children}
    </div>
  )
}
