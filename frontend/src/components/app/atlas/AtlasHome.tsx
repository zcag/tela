import { useMemo, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { ChevronRight, FolderGit2, KeyRound, Plus, Sparkles } from 'lucide-react'
import { Button } from '../../ui/button'
import { StatusBadge } from '../../ui/status-badge'
import { EmptyState } from '../../ui/empty-state'
import { useMe } from '../../../lib/queries/auth'
import { useOrgs } from '../../../lib/queries/orgs'
import {
  type AtlasOwner,
  type AtlasProject,
  useAtlasProjects,
} from '../../../lib/queries/atlas'
import {
  type Freshness,
  fmtRelative,
  fmtUntil,
  freshnessTone,
} from './atlas-lib'
import { NewProjectDialog } from './NewProjectDialog'
import { CredentialsDialog } from './CredentialsDialog'
import { AtlasQuotaBar } from './AtlasQuotaBar'

function freshnessOf(p: AtlasProject): { f: Freshness; label: string } {
  const r = p.last_run
  if (!r) return { f: 'never', label: 'Never run' }
  switch (r.status) {
    case 'running':
      return { f: 'running', label: 'Generating' }
    case 'pending':
      return { f: 'pending', label: 'Queued' }
    case 'failed':
      return { f: 'failed', label: 'Failed' }
    case 'done':
      if (p.stale_sources > 0) {
        return { f: 'stale', label: p.last_refresh_at ? `Stale · built ${fmtRelative(p.last_refresh_at)}` : 'Stale' }
      }
      return { f: 'fresh', label: p.last_refresh_at ? `Fresh · ${fmtRelative(p.last_refresh_at)}` : 'Fresh' }
    default:
      return { f: 'never', label: 'Idle' }
  }
}

const ownerKey = (o: AtlasOwner) => `${o.kind}:${o.id}`

export function AtlasHome() {
  const me = useMe().data
  const projectsQ = useAtlasProjects()
  const projects = projectsQ.data?.projects ?? []
  const [newOpen, setNewOpen] = useState(false)
  const [credsOpen, setCredsOpen] = useState(false)

  const groups = useMemo(() => {
    const by = new Map<string, { owner: AtlasOwner; items: AtlasProject[] }>()
    for (const p of projects) {
      const k = ownerKey(p.owner)
      if (!by.has(k)) by.set(k, { owner: p.owner, items: [] })
      by.get(k)!.items.push(p)
    }
    return [...by.values()].sort((a, b) => {
      const aMe = a.owner.kind === 'user' && a.owner.id === me?.id
      const bMe = b.owner.kind === 'user' && b.owner.id === me?.id
      if (aMe !== bMe) return aMe ? -1 : 1
      return a.owner.name.localeCompare(b.owner.name)
    })
  }, [projects, me?.id])

  // Owners the caller may create a project under: themselves (personal) + every
  // org they administer — independent of whether a project already exists there.
  const orgs = useOrgs().data
  const ownerOptions = useMemo<AtlasOwner[]>(() => {
    const out: AtlasOwner[] = []
    if (me) out.push({ kind: 'user', id: me.id, name: me.display_name || me.username })
    for (const o of orgs ?? []) {
      if (o.my_role === 'admin') out.push({ kind: 'org', id: o.id, name: o.name })
    }
    return out
  }, [me, orgs])

  const counts = useMemo(() => {
    const c = { fresh: 0, running: 0, failed: 0, stale: 0 }
    for (const p of projects) {
      const { f } = freshnessOf(p)
      if (f === 'fresh') c.fresh++
      else if (f === 'stale') c.stale++
      else if (f === 'running' || f === 'pending') c.running++
      else if (f === 'failed') c.failed++
    }
    return c
  }, [projects])

  return (
    <div className="mx-auto w-full max-w-[72rem] px-[var(--space-5)] py-[var(--space-5)]">
      <div className="flex flex-wrap items-end justify-between gap-[var(--space-3)]">
        <div>
          <h1 className="flex items-center gap-[var(--space-2)] text-[length:var(--text-2xl)] font-semibold text-[var(--text-primary)]">
            <Sparkles className="size-[var(--space-5)] text-[var(--accent)]" /> Atlas
          </h1>
          <p className="mt-[var(--space-2)] text-[length:var(--text-sm)] text-[var(--text-muted)]">
            {projects.length === 0
              ? 'Living documentation, generated from your systems and kept current.'
              : `${projects.length} project${projects.length === 1 ? '' : 's'} · ${counts.fresh} fresh${counts.stale ? ` · ${counts.stale} stale` : ''}${counts.running ? ` · ${counts.running} generating` : ''}${counts.failed ? ` · ${counts.failed} failed` : ''}`}
          </p>
        </div>
        <div className="flex items-center gap-[var(--space-2)]">
          <Button variant="ghost" onClick={() => setCredsOpen(true)}>
            <KeyRound className="size-[var(--space-4)]" /> Credentials
          </Button>
          <Button variant="primary" onClick={() => setNewOpen(true)} disabled={ownerOptions.length === 0}>
            <Plus className="size-[var(--space-4)]" /> New project
          </Button>
        </div>
      </div>

      <AtlasQuotaBar />

      {projectsQ.isLoading ? (
        <p className="mt-[var(--space-6)] text-[length:var(--text-sm)] text-[var(--text-muted)]">Loading projects…</p>
      ) : projects.length === 0 ? (
        <div className="mt-[var(--space-6)]">
          <EmptyState
            icon={FolderGit2}
            title="No projects yet"
            description="Point Atlas at a git repo or Jira project and it generates coverage-audited docs into a space — then keeps them current."
            actions={
              <Button variant="primary" onClick={() => setNewOpen(true)} disabled={ownerOptions.length === 0}>
                <Plus className="size-[var(--space-4)]" /> New project
              </Button>
            }
          />
        </div>
      ) : (
        <div className="mt-[var(--space-5)] flex flex-col gap-[var(--space-6)]">
          {groups.map((g) => {
            const isMe = g.owner.kind === 'user' && g.owner.id === me?.id
            return (
              <section key={ownerKey(g.owner)}>
                <div className="mb-[var(--space-2)] flex items-center gap-[var(--space-2)] px-[var(--space-1)]">
                  <span className="grid size-[var(--space-5)] place-items-center rounded-[var(--radius-sm)] bg-[color-mix(in_srgb,var(--accent)_14%,transparent)] text-[length:var(--text-xs)] font-bold text-[var(--accent)]">
                    {g.owner.name.slice(0, 1).toUpperCase()}
                  </span>
                  <h2 className="text-[length:var(--text-xs)] font-bold uppercase tracking-[0.08em] text-[var(--text-muted)]">
                    {isMe ? 'Personal' : g.owner.name}
                  </h2>
                  {!isMe && <span className="text-[length:var(--text-xs)] text-[var(--text-muted)] opacity-70">· org</span>}
                  <span className="h-px flex-1 bg-[var(--border-subtle)]" />
                  <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">{g.items.length}</span>
                </div>
                <div className="overflow-hidden rounded-[var(--radius-lg)] border border-[var(--border-subtle)] bg-[var(--surface-1)] shadow-[var(--shadow-sm)]">
                  {g.items.map((p, i) => (
                    <ProjectRow key={p.id} p={p} first={i === 0} />
                  ))}
                </div>
              </section>
            )
          })}
        </div>
      )}

      <NewProjectDialog open={newOpen} onOpenChange={setNewOpen} owners={ownerOptions} />
      <CredentialsDialog open={credsOpen} onOpenChange={setCredsOpen} owners={ownerOptions} />
    </div>
  )
}

function ProjectRow({ p, first }: { p: AtlasProject; first: boolean }) {
  const { f, label } = freshnessOf(p)
  const navigate = useNavigate()
  const schedule =
    p.cadence && p.auto_update && p.scheduled_allowed
      ? `auto · ${p.cadence}${p.next_due ? ` · ${fmtUntil(p.next_due)}` : ''}`
      : 'manual'
  return (
    <button
      type="button"
      onClick={() => navigate({ to: '/atlas/projects/$projectId', params: { projectId: p.id } })}
      className={[
        'group flex w-full items-center gap-[var(--space-4)] px-[var(--space-4)] py-[var(--space-3)] text-left transition-colors hover:bg-[var(--surface-2)]',
        first ? '' : 'border-t border-[var(--border-subtle)]',
      ].join(' ')}
    >
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-[var(--space-2)]">
          <span className="truncate text-[length:var(--text-base)] font-semibold text-[var(--text-primary)]">{p.name}</span>
          <StatusBadge tone={freshnessTone(f)} dot={f === 'running'}>{label}</StatusBadge>
        </div>
        <div className="mt-[var(--space-1)] flex flex-wrap items-center gap-x-[var(--space-3)] gap-y-[2px] text-[length:var(--text-xs)] text-[var(--text-muted)]">
          <span>{p.sources_count} source{p.sources_count === 1 ? '' : 's'}</span>
          <span className="opacity-60">·</span>
          <span>{schedule}</span>
          {p.output_space && (
            <>
              <span className="opacity-60">·</span>
              <span>→ <span className="text-[var(--accent)]">{p.output_space.name}</span></span>
            </>
          )}
        </div>
      </div>
      {p.last_run?.must_rate != null && (
        <span className="hidden whitespace-nowrap font-[family-name:var(--font-mono)] text-[length:var(--text-xs)] text-[var(--text-muted)] sm:inline">
          must-cover {Math.round(p.last_run.must_rate * 100)}%
        </span>
      )}
      <ChevronRight className="size-[var(--space-4)] text-[var(--text-muted)] opacity-0 transition-opacity group-hover:opacity-100" />
    </button>
  )
}
