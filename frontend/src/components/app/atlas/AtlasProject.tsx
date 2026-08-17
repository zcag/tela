import { useState } from 'react'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import {
  ArrowLeft,
  ExternalLink,
  FolderGit2,
  GitBranch,
  KeyRound,
  Layers,
  Loader2,
  Play,
  Plus,
  RefreshCw,
  Settings2,
  Trash2,
} from 'lucide-react'
import { Button } from '../../ui/button'
import { Card, CardBody, CardHeader, CardTitle } from '../../ui/card'
import { StatusBadge } from '../../ui/status-badge'
import { EmptyState } from '../../ui/empty-state'
import {
  type AtlasProject as AtlasProjectT,
  type AtlasRunSummary,
  type AtlasSource,
  mustCoverRate,
  useAtlasProject,
  useDeleteSource,
  useStartProjectRun,
  useStartSourceRun,
  useSyncSource,
} from '../../../lib/queries/atlas'
import { type Tone, fmtRelative, fmtUntil, runLabel, runTone } from './atlas-lib'
import { AddSourceDialog } from './AddSourceDialog'

function headerState(project: AtlasProjectT): { tone: Tone; label: string } {
  const r = project.last_run
  if (!r) return { tone: 'neutral', label: 'Never run' }
  if (r.status === 'pending') return { tone: 'info', label: 'Queued' }
  if (r.status === 'running') return { tone: 'running', label: 'Generating' }
  if (r.status === 'failed') return { tone: 'negative', label: 'Failed' }
  // Ahead of Stale, and ahead of the Fresh a finished run would otherwise earn:
  // a project whose sources can't be reached is not fresh, it's out of contact.
  if (project.unreachable_sources > 0) return { tone: 'negative', label: 'Unreachable' }
  if (project.stale_sources > 0) return { tone: 'warning', label: 'Stale' }
  return { tone: 'positive', label: 'Fresh' }
}

// "all in sync" is a claim about upstream — only sayable when every source was
// actually reached. Unreachable ones are reported first, since the drift count
// can't include them.
function sourcesSub(project: AtlasProjectT): string {
  if (project.unreachable_sources > 0) return `${project.unreachable_sources} unreachable`
  if (project.stale_sources > 0) return `${project.stale_sources} behind upstream`
  return 'all in sync'
}

export function AtlasProject() {
  const { projectId } = useParams({ from: '/_app/atlas/projects/$projectId' })
  const q = useAtlasProject(projectId)
  const [addOpen, setAddOpen] = useState(false)
  const runAll = useStartProjectRun()

  if (q.isLoading) {
    return <Shell><p className="text-[length:var(--text-sm)] text-[var(--text-muted)]">Loading project…</p></Shell>
  }
  if (!q.data) {
    return <Shell><EmptyState icon={FolderGit2} title="Project not found" description="It doesn't exist or you can't access it." /></Shell>
  }

  const { project, sources, runs } = q.data
  const canManage = project.can_manage
  const hs = headerState(project)
  const schedule = project.cadence && project.auto_update && project.scheduled_allowed
    ? `auto · ${project.cadence}${project.next_due ? ` · next ${fmtUntil(project.next_due)}` : ''}`
    : 'manual refresh'

  const totalPages = sources.reduce((n, s) => n + (s.last_pages ?? 0), 0)
  const mustRates = sources.map((s) => s.last_must_rate).filter((r): r is number => r != null)
  const minMust = mustRates.length ? Math.min(...mustRates) : null

  return (
    <Shell>
      <Link to="/atlas" className="mb-[var(--space-2)] inline-flex items-center gap-[var(--space-1)] text-[length:var(--text-xs)] text-[var(--text-muted)] hover:text-[var(--text-primary)]">
        <ArrowLeft className="size-[var(--space-3)]" /> Atlas
      </Link>

      {/* header */}
      <div className="flex flex-wrap items-start justify-between gap-[var(--space-3)]">
        <div className="min-w-0">
          <div className="flex items-center gap-[var(--space-3)]">
            <h1 className="text-[length:var(--text-2xl)] font-semibold text-[var(--text-primary)]">{project.name}</h1>
            <StatusBadge tone={hs.tone} dot={hs.label === 'Generating'}>{hs.label}</StatusBadge>
          </div>
          <div className="mt-[var(--space-2)] flex flex-wrap items-center gap-x-[var(--space-3)] gap-y-[var(--space-1)] text-[length:var(--text-sm)] text-[var(--text-muted)]">
            <span>{project.owner.kind === 'org' ? `${project.owner.name} · org` : 'Personal'}</span>
            <span className="opacity-60">·</span>
            <span>{schedule}</span>
            {project.last_refresh_at && (
              <>
                <span className="opacity-60">·</span>
                <span>updated {fmtRelative(project.last_refresh_at)}</span>
              </>
            )}
            {project.output_space ? (
              <>
                <span className="opacity-60">·</span>
                <a href={`/spaces/${project.output_space.id}`} className="inline-flex items-center gap-[2px] text-[var(--accent)] hover:underline">
                  {project.output_space.name} <ExternalLink className="size-[var(--space-3)]" />
                </a>
              </>
            ) : (
              <>
                <span className="opacity-60">·</span>
                <span>output space created on first run</span>
              </>
            )}
          </div>
        </div>
        {canManage && (
          <div className="flex items-center gap-[var(--space-2)]">
            <Button asChild variant="ghost" aria-label="Project settings">
              <Link to="/atlas/projects/$projectId/settings" params={{ projectId: project.id }}><Settings2 className="size-[var(--space-4)]" /></Link>
            </Button>
            <Button variant="secondary" onClick={() => setAddOpen(true)}><Plus className="size-[var(--space-4)]" /> Add source</Button>
            <Button variant="primary" disabled={sources.length === 0 || runAll.isPending} onClick={() => runAll.mutate(project.id)}>
              {runAll.isPending ? <Loader2 className="size-[var(--space-4)] motion-safe:animate-spin" /> : <Play className="size-[var(--space-4)]" />} Run all
            </Button>
          </div>
        )}
      </div>

      {/* stats strip */}
      <div className="mt-[var(--space-5)] grid grid-cols-2 gap-[var(--space-3)] sm:grid-cols-4">
        <StatTile label="Sources" value={String(sources.length)} sub={sourcesSub(project)} />
        <StatTile label="Pages" value={totalPages ? String(totalPages) : '—'} sub="generated docs" />
        <StatTile label="Must-cover" value={minMust != null ? `${Math.round(minMust * 100)}%` : '—'} sub={mustRates.length > 1 ? 'lowest source' : 'critical surface'} />
        <StatTile label="Last built" value={project.last_refresh_at ? fmtRelative(project.last_refresh_at) : '—'} sub={schedule} />
      </div>

      {/* sources */}
      <div className="mt-[var(--space-6)] mb-[var(--space-2)] flex items-center gap-[var(--space-2)] px-[var(--space-1)]">
        <h2 className="text-[length:var(--text-xs)] font-bold uppercase tracking-[0.08em] text-[var(--text-muted)]">Sources · {sources.length}</h2>
        <span className="h-px flex-1 bg-[var(--border-subtle)]" />
      </div>
      {sources.length === 0 ? (
        <div className="flex flex-col items-center gap-[var(--space-3)] rounded-[var(--radius-lg)] border border-dashed border-[var(--border-subtle)] py-[var(--space-6)] text-center">
          <p className="text-[length:var(--text-sm)] text-[var(--text-muted)]">No sources yet. Add a git repo or Jira project to generate docs from.</p>
          {canManage && <Button variant="secondary" onClick={() => setAddOpen(true)}><Plus className="size-[var(--space-4)]" /> Add source</Button>}
        </div>
      ) : (
        <div className="flex flex-col gap-[var(--space-3)]">
          {sources.map((s) => (
            <SourceCard key={s.id} s={s} projectId={project.id} canManage={canManage} space={project.output_space} />
          ))}
        </div>
      )}

      {/* runs */}
      <Card className="mt-[var(--space-6)]">
        <CardHeader><CardTitle>Recent runs</CardTitle></CardHeader>
        <CardBody>
          {runs.length === 0 ? (
            <p className="py-[var(--space-2)] text-[length:var(--text-sm)] text-[var(--text-muted)]">No runs yet.</p>
          ) : (
            <ul className="flex flex-col">
              {runs.map((r, i) => <RunRow key={r.id} r={r} first={i === 0} />)}
            </ul>
          )}
        </CardBody>
      </Card>

      {canManage && <AddSourceDialog open={addOpen} onOpenChange={setAddOpen} projectId={project.id} owner={project.owner} />}
    </Shell>
  )
}

function Shell({ children }: { children: React.ReactNode }) {
  return <div className="mx-auto w-full max-w-[68rem] px-[var(--space-5)] py-[var(--space-5)]">{children}</div>
}

function StatTile({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div className="rounded-[var(--radius-lg)] border border-[var(--border-subtle)] bg-[var(--surface-1)] px-[var(--space-4)] py-[var(--space-3)] shadow-[var(--shadow-sm)]">
      <div className="text-[length:var(--text-xs)] uppercase tracking-[0.04em] text-[var(--text-muted)]">{label}</div>
      <div className="mt-[2px] text-[length:var(--text-xl)] font-semibold text-[var(--text-primary)]">{value}</div>
      {sub && <div className="truncate text-[length:var(--text-xs)] text-[var(--text-muted)]">{sub}</div>}
    </div>
  )
}

function Metric({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex flex-col">
      <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">{label}</span>
      <span className={['text-[length:var(--text-sm)] font-medium text-[var(--text-primary)]', mono ? 'font-[family-name:var(--font-mono)]' : ''].join(' ')}>{value}</span>
    </div>
  )
}

function SourceCard({ s, projectId, canManage, space }: { s: AtlasSource; projectId: number; canManage: boolean; space: { id: number; name: string } | null }) {
  const navigate = useNavigate()
  const run = useStartSourceRun(projectId)
  const sync = useSyncSource(projectId)
  const del = useDeleteSource(projectId)
  const busy = run.isPending || sync.isPending
  // Unreachable outranks everything: the probe can't see upstream, so neither
  // "Stale" nor the last run's status describes the source any more. A dead
  // credential should look dead, not like a successful run from three weeks ago.
  const unreachable = !!s.probe_error
  const stale = !unreachable && !!s.stale_since && s.last_run_status === 'done'
  const short = s.ref ? s.ref.slice(0, 7) : null

  async function doRun() {
    const { run_id } = await run.mutateAsync(s.id)
    navigate({ to: '/atlas/runs/$runId', params: { runId: run_id } })
  }
  async function doSync() {
    const res = await sync.mutateAsync(s.id)
    if (res.run_id) navigate({ to: '/atlas/runs/$runId', params: { runId: res.run_id } })
  }

  return (
    <div className="rounded-[var(--radius-lg)] border border-[var(--border-subtle)] bg-[var(--surface-1)] p-[var(--space-4)] shadow-[var(--shadow-sm)]">
      <div className="flex items-start justify-between gap-[var(--space-3)]">
        <div className="min-w-0">
          <div className="flex items-center gap-[var(--space-2)]">
            {s.type === 'jira' ? <Layers className="size-[var(--space-4)] text-[var(--text-muted)]" /> : <FolderGit2 className="size-[var(--space-4)] text-[var(--text-muted)]" />}
            <span className="truncate text-[length:var(--text-base)] font-semibold text-[var(--text-primary)]">{s.name}</span>
            {unreachable ? (
              <StatusBadge tone="negative" title={s.probe_error}>Unreachable</StatusBadge>
            ) : stale ? (
              <StatusBadge tone="warning">Stale</StatusBadge>
            ) : s.last_run_status ? (
              <StatusBadge tone={runTone(s.last_run_status)} dot={s.last_run_status === 'running'}>{runLabel(s.last_run_status)}</StatusBadge>
            ) : (
              <StatusBadge tone="neutral">Never run</StatusBadge>
            )}
          </div>
          <div className="mt-[var(--space-1)] flex flex-wrap items-center gap-x-[var(--space-3)] gap-y-[2px] font-[family-name:var(--font-mono)] text-[length:var(--text-xs)] text-[var(--text-muted)]">
            <span className="truncate">{s.location}</span>
            {s.branch && <span className="inline-flex items-center gap-[2px]"><GitBranch className="size-[var(--space-3)]" />{s.branch}</span>}
            {s.subpath && <span>/{s.subpath}</span>}
            {s.cred_id != null && <span className="inline-flex items-center gap-[2px]"><KeyRound className="size-[var(--space-3)]" />cred</span>}
          </div>
        </div>
        {canManage && (
          <div className="flex shrink-0 items-center gap-[var(--space-1)]">
            <Button variant="ghost" size="sm" disabled={busy} onClick={doRun} aria-label="Run now" title="Run now">
              {run.isPending ? <Loader2 className="size-[var(--space-4)] motion-safe:animate-spin" /> : <Play className="size-[var(--space-4)]" />}
            </Button>
            <Button variant="ghost" size="sm" disabled={busy} onClick={doSync} aria-label="Sync changes" title="Sync changes">
              {sync.isPending ? <Loader2 className="size-[var(--space-4)] motion-safe:animate-spin" /> : <RefreshCw className="size-[var(--space-4)]" />}
            </Button>
            <Button variant="ghost" size="sm" disabled={del.isPending} onClick={() => del.mutate(s.id)} aria-label="Delete source" title="Delete source">
              <Trash2 className="size-[var(--space-4)]" />
            </Button>
          </div>
        )}
      </div>

      {/* The badge alone can't say WHY (and there's no hover on touch), so the
          probe's own words go in the card — that text is what tells you the repo
          was deleted vs the token expired. */}
      {unreachable && (
        <p className="mt-[var(--space-3)] border-t border-[var(--border-subtle)] pt-[var(--space-3)] font-[family-name:var(--font-mono)] text-[length:var(--text-xs)] text-[var(--accent-negative-fg)]">
          {s.probe_error}
        </p>
      )}

      {s.last_run_id ? (
        <div className="mt-[var(--space-3)] flex flex-wrap gap-x-[var(--space-6)] gap-y-[var(--space-2)] border-t border-[var(--border-subtle)] pt-[var(--space-3)]">
          {s.last_must_rate != null && <Metric label="must-cover" value={`${Math.round(s.last_must_rate * 100)}%`} />}
          {s.last_surface_rate != null && <Metric label="surface" value={`${Math.round(s.last_surface_rate * 100)}%`} />}
          {s.last_pages != null && <Metric label="pages" value={String(s.last_pages)} />}
          {s.last_generated_at && <Metric label="generated" value={fmtRelative(s.last_generated_at)} />}
          {short && <Metric label="commit" value={short} mono />}
        </div>
      ) : (
        <p className="mt-[var(--space-3)] border-t border-[var(--border-subtle)] pt-[var(--space-3)] text-[length:var(--text-xs)] text-[var(--text-muted)]">
          Not generated yet — run it to create the docs.
        </p>
      )}

      <div className="mt-[var(--space-3)] flex flex-wrap items-center justify-between gap-[var(--space-2)] text-[length:var(--text-xs)]">
        {space ? (
          <a href={`/spaces/${space.id}`} className="inline-flex items-center gap-[2px] text-[var(--text-muted)] hover:text-[var(--accent)]">
            → {space.name} / <span className="font-[family-name:var(--font-mono)]">{s.name}</span>
          </a>
        ) : (
          <span className="text-[var(--text-muted)]">output created on first run</span>
        )}
        {s.last_run_id && (
          <button type="button" onClick={() => navigate({ to: '/atlas/runs/$runId', params: { runId: s.last_run_id! } })} className="text-[var(--text-muted)] hover:text-[var(--text-primary)]">
            View last run →
          </button>
        )}
      </div>
    </div>
  )
}

function RunRow({ r, first }: { r: AtlasRunSummary; first: boolean }) {
  const navigate = useNavigate()
  return (
    <li>
      <button
        type="button"
        onClick={() => navigate({ to: '/atlas/runs/$runId', params: { runId: r.id } })}
        className={['group flex w-full items-center gap-[var(--space-3)] py-[var(--space-3)] text-left', first ? '' : 'border-t border-[var(--border-subtle)]'].join(' ')}
      >
        <span className="font-[family-name:var(--font-mono)] text-[length:var(--text-sm)] text-[var(--text-muted)]">#{r.id}</span>
        <StatusBadge tone={runTone(r.status)} dot={r.status === 'running'}>{runLabel(r.status)}</StatusBadge>
        <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">{r.kind}</span>
        <span className="flex-1" />
        {r.coverage && (
          <span className="hidden font-[family-name:var(--font-mono)] text-[length:var(--text-xs)] text-[var(--text-muted)] sm:inline">
            must-cover {Math.round(mustCoverRate(r.coverage) * 100)}%
          </span>
        )}
        <span className="whitespace-nowrap text-[length:var(--text-xs)] text-[var(--text-muted)]">{fmtRelative(r.started_at)}</span>
      </button>
    </li>
  )
}
