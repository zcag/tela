// Shared model + helpers for the Atlas operator UI: the canonical pipeline,
// formatters, status→tone mapping, and the progress/ETA engine that turns a
// run's SSE event stream into per-stage state, timing, and time-remaining
// estimates. Kept framework-light so every Atlas screen reads the same.
import { useMemo } from 'react'
import type { StatusBadgeProps } from '../../ui/status-badge'
import type {
  AtlasRun,
  AtlasRunEvent,
  AtlasRunStatus,
  AtlasRunSummary,
} from '../../../lib/queries/atlas'

export type Tone = NonNullable<StatusBadgeProps['tone']>

// The 12-stage pipeline, in order (engine.Default()). `weight` is the rough
// fraction of total wall-clock a stage takes — used to estimate overall ETA
// when there's no prior-run baseline. Draft dominates (LLM generation).
export interface StageDef {
  key: string
  label: string
  desc: string
  weight: number
}
export const ATLAS_STAGES: StageDef[] = [
  { key: 'acquire', label: 'Acquire', desc: 'Clone & pin the source at a commit', weight: 0.03 },
  { key: 'inventory', label: 'Inventory', desc: 'Discover & classify in-scope files', weight: 0.01 },
  { key: 'spine', label: 'Spine', desc: 'Extract the deterministic surface', weight: 0.01 },
  { key: 'chunk', label: 'Chunk', desc: 'Symbol-aware chunks with line ranges', weight: 0.04 },
  { key: 'embed', label: 'Embed', desc: 'Vector-embed every chunk', weight: 0.18 },
  { key: 'index', label: 'Index', desc: 'Build the hybrid retriever', weight: 0.01 },
  { key: 'outline', label: 'Outline', desc: 'Plan the wiki pages', weight: 0.08 },
  { key: 'draft', label: 'Draft', desc: 'Generate grounded, cited pages', weight: 0.45 },
  { key: 'refine', label: 'Refine', desc: 'Multi-pass critique & expand', weight: 0.12 },
  { key: 'validate', label: 'Validate', desc: 'Audit coverage, citations, diagrams', weight: 0.02 },
  { key: 'repair', label: 'Repair', desc: 'Regenerate gaps to threshold', weight: 0.04 },
  { key: 'publish', label: 'Publish', desc: 'Write the docs into the space', weight: 0.01 },
]
const STAGE_INDEX: Record<string, number> = Object.fromEntries(
  ATLAS_STAGES.map((s, i) => [s.key, i]),
)

// ── status → tone / label ─────────────────────────────────────────────────────

export function runTone(s?: AtlasRunStatus | null): Tone {
  switch (s) {
    case 'running':
      return 'running'
    case 'done':
      return 'positive'
    case 'failed':
      return 'negative'
    case 'canceled':
      return 'neutral'
    default:
      return 'info' // pending
  }
}

export function runLabel(s?: AtlasRunStatus | null): string {
  // A 'pending' run is waiting on the global run-slot queue — call it "Queued"
  // so a not-yet-started run reads as deliberately waiting, not stuck.
  if (s === 'pending') return 'Queued'
  return s ? s[0].toUpperCase() + s.slice(1) : 'Queued'
}

// Freshness of a project, from its last run + schedule — the home/console hero
// signal (not coverage). 'never' when it has no runs yet. 'unreachable' means the
// drift probe can't see upstream at all, which outranks both 'stale' and the
// 'fresh' a long-finished run would otherwise claim.
export type Freshness = 'never' | 'running' | 'fresh' | 'failed' | 'pending' | 'stale' | 'unreachable'
export function freshnessTone(f: Freshness): Tone {
  switch (f) {
    case 'fresh':
      return 'positive'
    case 'running':
      return 'running'
    case 'failed':
    case 'unreachable':
      return 'negative'
    case 'stale':
      return 'warning'
    default:
      return 'neutral'
  }
}

// ── formatters ────────────────────────────────────────────────────────────────

export function fmtDuration(sec: number): string {
  if (!isFinite(sec) || sec < 0) return '—'
  const s = Math.round(sec)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ${s % 60}s`
  const h = Math.floor(m / 60)
  return `${h}h ${m % 60}m`
}

// Compact "time remaining" — coarser than fmtDuration (no seconds past a minute).
export function fmtEta(sec: number): string {
  if (!isFinite(sec) || sec <= 0) return 'almost done'
  if (sec < 60) return `~${Math.round(sec)}s left`
  const m = Math.round(sec / 60)
  if (m < 60) return `~${m}m left`
  const h = Math.floor(m / 60)
  return `~${h}h ${m % 60}m left`
}

export function fmtNum(n?: number | null): string {
  if (n == null) return '—'
  return n.toLocaleString('en-US')
}

// Parse the backend's TEXT timestamps ('YYYY-MM-DD HH:MM:SS' UTC) and the SSE
// event 'at' (RFC3339). Returns ms epoch, or NaN.
export function parseTs(ts?: string | null): number {
  if (!ts) return NaN
  // SQL 'YYYY-MM-DD HH:MM:SS' has no zone → treat as UTC.
  const sql = /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/.test(ts)
  const v = Date.parse(sql ? ts.replace(' ', 'T') + 'Z' : ts)
  return v
}

export function fmtRelative(ts?: string | null, now = Date.now()): string {
  const t = parseTs(ts)
  if (isNaN(t)) return '—'
  const diff = Math.max(0, now - t)
  const m = Math.floor(diff / 60000)
  if (m < 1) return 'just now'
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ago`
  const d = Math.floor(h / 24)
  return `${d}d ago`
}

// "next in 3h" style, from an ISO/SQL next-due timestamp.
export function fmtUntil(ts?: string | null, now = Date.now()): string {
  const t = parseTs(ts)
  if (isNaN(t)) return ''
  const diff = t - now
  if (diff <= 0) return 'due now'
  const m = Math.floor(diff / 60000)
  if (m < 60) return `in ${m}m`
  const h = Math.floor(m / 60)
  if (h < 24) return `in ${h}h`
  return `in ${Math.floor(h / 24)}d`
}

// Run duration in seconds from started/finished (or now for a live run).
export function runDurationSec(
  r: { started_at: string; finished_at?: string },
  now = Date.now(),
): number {
  const start = parseTs(r.started_at)
  if (isNaN(start)) return NaN
  const end = r.finished_at ? parseTs(r.finished_at) : now
  return Math.max(0, (end - start) / 1000)
}

// ── progress / ETA engine ─────────────────────────────────────────────────────

export type StageState = 'done' | 'running' | 'pending' | 'failed'

export interface StageProgress {
  def: StageDef
  state: StageState
  cur: number
  total: number
  durationSec?: number // wall-clock the stage took (done) or has taken (running)
  etaSec?: number // estimate for the running stage
  lastMsg?: string
}

export interface RunProgress {
  stages: StageProgress[]
  currentIndex: number // -1 when terminal
  doneCount: number
  overallProgress: number // 0..1 (weighted)
  elapsedSec: number
  etaSec?: number // overall time remaining (running only)
}

// Derive the full pipeline picture from the run + its live event log.
// `baselineDurationSec` (a prior successful run's duration) sharpens the overall
// ETA; without it we fall back to the stage weights.
export function computeRunProgress(
  run: AtlasRun | undefined,
  events: AtlasRunEvent[],
  baselineDurationSec: number | undefined,
  now = Date.now(),
): RunProgress {
  const status = run?.status
  const terminal = status === 'done' || status === 'failed' || status === 'canceled'

  // First event time per stage (for per-stage durations) + latest cur/total/msg.
  const firstAt: Record<string, number> = {}
  const last: Record<string, AtlasRunEvent> = {}
  let maxSeenIdx = -1
  for (const e of events) {
    const idx = STAGE_INDEX[e.stage]
    if (idx == null) continue
    const t = parseTs(e.at)
    if (firstAt[e.stage] == null && !isNaN(t)) firstAt[e.stage] = t
    last[e.stage] = e
    if (idx > maxSeenIdx) maxSeenIdx = idx
  }

  // Current stage: the run's reported stage if mid-flight, else the furthest seen.
  let currentIndex = terminal ? -1 : Math.max(maxSeenIdx, run?.stage ? STAGE_INDEX[run.stage] ?? -1 : -1)
  const failedIndex =
    status === 'failed' ? (run?.stage ? STAGE_INDEX[run.stage] ?? maxSeenIdx : maxSeenIdx) : -1

  const start = run ? parseTs(run.started_at) : NaN
  const elapsedSec = isNaN(start) ? 0 : Math.max(0, ((terminal && run?.finished_at ? parseTs(run.finished_at) : now) - start) / 1000)

  const stages: StageProgress[] = ATLAS_STAGES.map((def, i) => {
    let state: StageState
    if (status === 'failed' && i === failedIndex) state = 'failed'
    else if (terminal) state = i <= maxSeenIdx || status === 'done' ? 'done' : 'pending'
    else if (i < currentIndex) state = 'done'
    else if (i === currentIndex) state = 'running'
    else state = 'pending'

    const ev = last[def.key]
    const cur = ev?.cur ?? 0
    const total = ev?.total ?? 0

    // Per-stage duration: from this stage's first event to the next stage's first.
    let durationSec: number | undefined
    const sFirst = firstAt[def.key]
    if (sFirst != null) {
      const next = ATLAS_STAGES[i + 1]
      const nextFirst = next ? firstAt[next.key] : undefined
      const endMs = state === 'running' ? now : nextFirst ?? (terminal && run?.finished_at ? parseTs(run.finished_at) : now)
      if (!isNaN(endMs)) durationSec = Math.max(0, (endMs - sFirst) / 1000)
    }

    // Per-stage ETA (running stage with a countable total).
    let etaSec: number | undefined
    if (state === 'running' && total > 0 && cur > 0 && durationSec && durationSec > 0) {
      const rate = cur / durationSec
      if (rate > 0) etaSec = Math.max(0, (total - cur) / rate)
    }

    return { def, state, cur, total, durationSec, etaSec, lastMsg: ev?.msg }
  })

  const doneCount = stages.filter((s) => s.state === 'done').length

  // Weighted overall progress: full weight for done stages + fractional for the
  // running one (by its cur/total, else half).
  let prog = 0
  for (const s of stages) {
    if (s.state === 'done') prog += s.def.weight
    else if (s.state === 'running') prog += s.def.weight * (s.total > 0 ? Math.min(1, s.cur / s.total) : 0.5)
  }
  const overallProgress = Math.min(1, prog)

  let etaSec: number | undefined
  if (status === 'running') {
    if (baselineDurationSec && baselineDurationSec > 0) {
      etaSec = Math.max(0, baselineDurationSec - elapsedSec)
    } else if (overallProgress > 0.02) {
      etaSec = Math.max(0, elapsedSec * (1 - overallProgress) / overallProgress)
    }
  }

  return { stages, currentIndex, doneCount, overallProgress, elapsedSec, etaSec }
}

// Convenience hook: memoized progress, recomputed as events arrive.
export function useRunProgress(
  run: AtlasRun | undefined,
  events: AtlasRunEvent[],
  baselineDurationSec?: number,
): RunProgress {
  return useMemo(
    () => computeRunProgress(run, events, baselineDurationSec),
    [run, events, baselineDurationSec],
  )
}

// Pick the most recent successful run's duration from a source's run list — the
// ETA baseline for a fresh run of the same source.
export function baselineFromRuns(runs?: AtlasRunSummary[]): number | undefined {
  if (!runs) return undefined
  for (const r of runs) {
    if (r.status === 'done' && r.finished_at) {
      const d = runDurationSec(r)
      if (isFinite(d) && d > 0) return d
    }
  }
  return undefined
}
