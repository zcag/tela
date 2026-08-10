import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../api'

// Mirrors the backend atlas DTOs (internal/api/atlas_projects.go,
// atlas_http.go, atlas_credentials.go) + core types (internal/atlas/core).
// atlas turns git repos / Jira projects into coverage-audited docs in a managed
// output space, organised under owner-scoped Projects. See docs/atlas.md.
//
// Direction-agnostic data layer: query hooks, mutations with cache
// invalidation, and the SSE run-stream hook. The screen visuals live elsewhere.

// ── enums ───────────────────────────────────────────────────────────────────

export type AtlasOwnerKind = 'user' | 'org'
export type AtlasSourceType = 'git' | 'jira'
export type AtlasCadence = '' | 'hourly' | 'daily' | 'weekly' | 'monthly'
// Projected monthly Atlas GPU spend against the plan cap (GET /api/atlas/budget).
// cap_minutes null = unlimited plan. `estimated` means at least one project had
// too little run history to cost from its own past, so the corpus median stood
// in — the UI must say so rather than present it as measured.
export type AtlasBudgetProject = {
  id: number
  name: string
  cadence: string
  auto_update: boolean
  sources: number
  minutes_per_run: number
  runs_per_month: number
  projected_minutes: number
  estimated: boolean
}

// One entry per account the caller can see (their personal one + each org they
// belong to). The cap is per ACCOUNT, so a project must be matched to the budget
// that actually governs it rather than to "the" budget.
export type AtlasBudgetEntry = AtlasBudget & {
  owner_kind: 'user' | 'org'
  owner_id: number
  owner_name: string
}

export type AtlasBudget = {
  cap_minutes: number | null
  used_minutes: number
  projected_minutes: number
  over: boolean
  estimated: boolean
  projects: AtlasBudgetProject[]
  suggestion: { cadence: string; projected_minutes: number; applies_to: number[] } | null
}

export type AtlasRunStatus =
  | 'pending'
  | 'running'
  | 'done'
  | 'failed'
  | 'canceled'

// ── owner / refs ──────────────────────────────────────────────────────────────

export interface AtlasOwner {
  kind: AtlasOwnerKind
  id: number
  name: string
}

export interface AtlasSpaceRef {
  id: number
  name: string
}

// The latest run across a project's sources, summarised for the list/cards.
// must_rate is the FE-computed coverage method the backend folds in here.
export interface AtlasLastRun {
  id: number
  status: AtlasRunStatus
  must_rate?: number
}

// ── project ───────────────────────────────────────────────────────────────────

// Mirrors atlasProjectDTO (GET /api/atlas/projects, GET /api/atlas/projects/{id}).
// output_space is null until the first run materializes the space; next_due /
// last_refresh_at are present only when scheduled / already refreshed.
export interface AtlasProject {
  id: number
  name: string
  owner: AtlasOwner
  output_space: AtlasSpaceRef | null
  output_parent_page_id?: number
  cadence: AtlasCadence
  auto_update: boolean
  last_refresh_at?: string
  next_due?: string
  sources_count: number
  // Generated sources whose upstream has moved past what's published (drift
  // detection); 0 when everything's in sync. Distinct from last_run status.
  stale_sources: number
  last_run: AtlasLastRun | null
  created_at: string
  can_manage: boolean
  // Whether the owning account's plan includes scheduled auto-refresh (paid/trial).
  // When false, auto_update is a server-side no-op — the UI offers manual-only and
  // an upgrade affordance instead of the automatic cadences.
  scheduled_allowed: boolean
}

// ── source ────────────────────────────────────────────────────────────────────

// Mirrors atlasSourceDTO. The latest-run summary (last_run_*) is nil until the
// source has run at least once.
export interface AtlasSource {
  id: number
  project_id: number
  cred_id?: number
  type: AtlasSourceType
  location: string
  name: string
  ref: string
  branch?: string
  subpath?: string
  include?: string
  exclude?: string
  created_at: string
  // Drift: a timestamp when detection has seen upstream move past the last
  // generated ref; absent/'' when the docs match upstream.
  stale_since?: string
  last_run_id?: number
  last_run_status?: AtlasRunStatus
  last_must_rate?: number
  last_surface_rate?: number
  last_pages?: number
  last_generated_at?: string
}

// ── coverage / stats (core.Coverage, core.RunStats) ──────────────────────────

export interface AtlasGap {
  kind: string
  name: string
  file: string
  line: number
}

export interface AtlasMermaidGap {
  page: string
  err: string
}

// core.Coverage. Rate() / MustRate() are Go methods (not serialized) — compute
// FE-side with coverageRate / mustCoverRate below.
export interface AtlasCoverage {
  total: number
  covered: number
  must_total: number
  must_covered: number
  gaps: AtlasGap[]
  citations: number
  bad_citations: number
  bad_cites?: string[]
  mermaid: number
  mermaid_valid: number
  mermaid_invalid: number
  mermaid_gaps?: AtlasMermaidGap[]
}

export interface AtlasUsage {
  chat_calls: number
  embed_calls: number
  prompt_tokens: number
  completion_tokens: number
  embed_tokens: number
}

// core.RunStats — set at publish.
export interface AtlasStats {
  files: number
  surface: number
  chunks: number
  pages: number
  duration_sec: number
  chat_model: string
  embed_model: string
  usage: AtlasUsage
  cost: number
}

export interface AtlasChangeSet {
  added?: string[]
  modified?: string[]
  deleted?: string[]
}

// ── run ───────────────────────────────────────────────────────────────────────

// The full run record (GET /api/atlas/runs/{id} → core.Run). coverage is set
// once validate/repair run; stats at publish.
export interface AtlasRun {
  id: number
  source_id: number
  kind: 'full' | 'delta'
  baseline_id?: number
  changeset?: AtlasChangeSet
  status: AtlasRunStatus
  stage: string
  err?: string
  coverage?: AtlasCoverage
  stats?: AtlasStats
  started_at: string
  finished_at?: string
}

// The lighter run row returned by the run-list endpoints (project detail `runs`
// and GET /api/atlas/sources/{id}/runs). source_id rides along on the project
// listing only; stats is not included in either listing (fetch the full run for
// that). coverage is present once the run reached validate/repair.
export interface AtlasRunSummary {
  id: number
  source_id?: number
  kind: 'full' | 'delta'
  status: AtlasRunStatus
  stage: string
  err?: string
  coverage?: AtlasCoverage
  started_at: string
  finished_at?: string
}

// GET /api/atlas/projects/{id} → project + its sources + recent runs.
export interface AtlasProjectDetail {
  project: AtlasProject
  sources: AtlasSource[]
  runs: AtlasRunSummary[]
}

// ── credential (atlasCredDTO) ─────────────────────────────────────────────────

// The read shape: the token value is NEVER returned (write-only on create).
export interface AtlasCredential {
  id: number
  owner_kind: AtlasOwnerKind
  owner_id: number
  name: string
  kind: AtlasSourceType
  meta?: Record<string, string>
  created_at: string
}

// ── SSE event (core.Event) ────────────────────────────────────────────────────

export interface AtlasRunEvent {
  run_id: number
  stage: string
  level: 'info' | 'warn' | 'error'
  msg: string
  cur: number
  total: number
  at: string
}

// ── derived helpers (Go methods, recomputed FE-side) ─────────────────────────

export function coverageRate(c?: AtlasCoverage | null): number {
  if (!c || c.total === 0) return 1
  return c.covered / c.total
}

export function mustCoverRate(c?: AtlasCoverage | null): number {
  if (!c || c.must_total === 0) return 1
  return c.must_covered / c.must_total
}

export const TERMINAL_STATUSES: AtlasRunStatus[] = ['done', 'failed', 'canceled']
export function isTerminal(s?: AtlasRunStatus | null): boolean {
  return s != null && TERMINAL_STATUSES.includes(s)
}

// ── request payloads ──────────────────────────────────────────────────────────

export interface AtlasProjectOutputInput {
  space_id?: number
  new_space_name?: string
  parent_page_id?: number
}

export interface CreateAtlasProjectInput {
  name: string
  owner_kind: AtlasOwnerKind
  owner_id: number
  output?: AtlasProjectOutputInput
  cadence?: AtlasCadence
  auto_update?: boolean
}

export interface PatchAtlasProjectInput {
  name?: string
  cadence?: AtlasCadence
  auto_update?: boolean
  output?: AtlasProjectOutputInput
}

export interface CreateAtlasSourceInput {
  type?: AtlasSourceType // 'git' default
  location: string
  name?: string
  branch?: string
  subpath?: string
  include?: string
  exclude?: string
  cred_id?: number
}

export interface PatchAtlasSourceInput {
  location?: string
  name?: string
  branch?: string
  subpath?: string
  include?: string
  exclude?: string
  cred_id?: number
}

export interface CreateAtlasCredentialInput {
  owner_kind: AtlasOwnerKind
  owner_id: number
  name: string
  kind: AtlasSourceType
  value: string
  meta?: Record<string, string>
}

// ── key factory ───────────────────────────────────────────────────────────────

export const atlasKeys = {
  all: ['atlas'] as const,
  projects: () => [...atlasKeys.all, 'projects'] as const,
  project: (id: number) => [...atlasKeys.all, 'project', id] as const,
  credentials: () => [...atlasKeys.all, 'credentials'] as const,
  sourceRuns: (sourceId: number) =>
    [...atlasKeys.all, 'source-runs', sourceId] as const,
  run: (runId: number) => [...atlasKeys.all, 'run', runId] as const,
  budget: () => [...atlasKeys.all, 'budget'] as const,
}

// ── queries ───────────────────────────────────────────────────────────────────

// Every project the caller can see: their personal ones plus the projects of
// every org they belong to, flat and ordered by id (the FE groups by owner for
// the per-person/org sections). Each row carries can_manage.
export function useAtlasProjects() {
  return useQuery({
    queryKey: atlasKeys.projects(),
    queryFn: () => api<{ projects: AtlasProject[] }>('/api/atlas/projects'),
    staleTime: 30_000,
  })
}

// Projected monthly Atlas GPU spend for the caller's personal account, against
// their plan cap. Drives the budget warning: the cap is denominated in GPU
// minutes while the user configures sources + cadence, so nobody can do this
// arithmetic themselves. Refetched on focus because changing a cadence in
// another tab should not leave a stale verdict on screen.
export function useAtlasBudget() {
  return useQuery({
    queryKey: atlasKeys.budget(),
    queryFn: () => api<{ budgets: AtlasBudgetEntry[] }>('/api/atlas/budget'),
    staleTime: 30_000,
  })
}

// One project with its sources + recent runs.
export function useAtlasProject(id: number | null | undefined) {
  return useQuery({
    queryKey: id != null ? atlasKeys.project(id) : atlasKeys.project(-1),
    queryFn: () => api<AtlasProjectDetail>(`/api/atlas/projects/${id}`),
    enabled: id != null,
  })
}

// Owner-scoped credentials the caller may bind (their personal + the orgs they
// administer). The token value is always blanked.
export function useAtlasCredentials() {
  return useQuery({
    queryKey: atlasKeys.credentials(),
    queryFn: () =>
      api<{ credentials: AtlasCredential[] }>('/api/atlas/credentials'),
    staleTime: 60_000,
  })
}

// A source's recent runs (newest first, capped at 50 server-side).
export function useAtlasSourceRuns(sourceId: number | null | undefined) {
  return useQuery({
    queryKey:
      sourceId != null ? atlasKeys.sourceRuns(sourceId) : atlasKeys.sourceRuns(-1),
    queryFn: () =>
      api<{ runs: AtlasRunSummary[] }>(`/api/atlas/sources/${sourceId}/runs`),
    enabled: sourceId != null,
  })
}

// One run's full status + coverage + stats. project_id rides along so the run
// screen can link back to its owning project (a run belongs to a project via
// its source).
export function useAtlasRun(runId: number | null | undefined) {
  return useQuery({
    queryKey: runId != null ? atlasKeys.run(runId) : atlasKeys.run(-1),
    queryFn: () => api<{ run: AtlasRun; project_id?: number }>(`/api/atlas/runs/${runId}`),
    enabled: runId != null,
  })
}

// ── mutations ─────────────────────────────────────────────────────────────────

export function useCreateProject() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: CreateAtlasProjectInput) =>
      api<{ project: AtlasProject }>('/api/atlas/projects', {
        method: 'POST',
        body: JSON.stringify(input),
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: atlasKeys.projects() })
    },
  })
}

export function usePatchProject() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, patch }: { id: number; patch: PatchAtlasProjectInput }) =>
      api<{ project: AtlasProject }>(`/api/atlas/projects/${id}`, {
        method: 'PATCH',
        body: JSON.stringify(patch),
      }),
    onSuccess: (_data, { id }) => {
      void qc.invalidateQueries({ queryKey: atlasKeys.projects() })
      void qc.invalidateQueries({ queryKey: atlasKeys.project(id) })
    },
  })
}

export function useDeleteProject() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) =>
      api<void>(`/api/atlas/projects/${id}`, { method: 'DELETE' }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: atlasKeys.projects() })
    },
  })
}

export function useCreateSource(projectId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: CreateAtlasSourceInput) =>
      api<{ source: AtlasSource }>(
        `/api/atlas/projects/${projectId}/sources`,
        { method: 'POST', body: JSON.stringify(input) },
      ),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: atlasKeys.project(projectId) })
    },
  })
}

export function usePatchSource(projectId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, patch }: { id: number; patch: PatchAtlasSourceInput }) =>
      api<{ source: AtlasSource }>(`/api/atlas/sources/${id}`, {
        method: 'PATCH',
        body: JSON.stringify(patch),
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: atlasKeys.project(projectId) })
    },
  })
}

export function useDeleteSource(projectId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (sourceId: number) =>
      api<void>(`/api/atlas/sources/${sourceId}`, { method: 'DELETE' }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: atlasKeys.project(projectId) })
    },
  })
}

// Triggers a full run for every source in the project → { run_ids }.
export function useStartProjectRun() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (projectId: number) =>
      api<{ run_ids: number[] }>(`/api/atlas/projects/${projectId}/run`, {
        method: 'POST',
      }),
    onSuccess: (_data, projectId) => {
      void qc.invalidateQueries({ queryKey: atlasKeys.projects() })
      void qc.invalidateQueries({ queryKey: atlasKeys.project(projectId) })
    },
  })
}

// Triggers a full run for one source → { run_id }.
export function useStartSourceRun(projectId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (sourceId: number) =>
      api<{ run_id: number }>(`/api/atlas/sources/${sourceId}/run`, {
        method: 'POST',
      }),
    onSuccess: (_data, sourceId) => {
      void qc.invalidateQueries({ queryKey: atlasKeys.project(projectId) })
      void qc.invalidateQueries({ queryKey: atlasKeys.sourceRuns(sourceId) })
    },
  })
}

// Change-gated delta sync for one source → 202 { run_id } when a run started,
// 200 { changed: false } when nothing changed upstream.
export function useSyncSource(projectId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (sourceId: number) =>
      api<{ run_id?: number; changed?: boolean }>(
        `/api/atlas/sources/${sourceId}/sync`,
        { method: 'POST' },
      ),
    onSuccess: (_data, sourceId) => {
      void qc.invalidateQueries({ queryKey: atlasKeys.project(projectId) })
      void qc.invalidateQueries({ queryKey: atlasKeys.sourceRuns(sourceId) })
    },
  })
}

export function useCreateCredential() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: CreateAtlasCredentialInput) =>
      api<{ credential: AtlasCredential }>('/api/atlas/credentials', {
        method: 'POST',
        body: JSON.stringify(input),
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: atlasKeys.credentials() })
    },
  })
}

export function useDeleteCredential() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (credId: number) =>
      api<void>(`/api/atlas/credentials/${credId}`, { method: 'DELETE' }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: atlasKeys.credentials() })
    },
  })
}

// ── live run stream (SSE) ─────────────────────────────────────────────────────

// useAtlasRunStream subscribes to a run's live progress over SSE
// (GET /api/atlas/runs/{id}/stream). The backend replays the full persisted
// event log on every (re)connect, so we clear on `onopen` — a reconnect then
// re-renders the complete log with no duplication. On the terminal '__end__'
// marker it closes and calls onEnd (e.g. to refetch the run for its final
// coverage/stats).
export function useAtlasRunStream(
  runId: number | null | undefined,
  opts?: { enabled?: boolean; onEnd?: () => void },
): { events: AtlasRunEvent[]; streaming: boolean } {
  const [events, setEvents] = useState<AtlasRunEvent[]>([])
  const [streaming, setStreaming] = useState(false)
  const enabled = (opts?.enabled ?? true) && runId != null

  // Hold onEnd in a ref. It's a fresh closure on every render, so depending on
  // it in the effect below would tear down + recreate the EventSource each
  // render — and for any run that ends immediately on connect (a finished run,
  // or a stale 'running' row the backend replays then closes) that loops
  // connect→replay→__end__→onEnd(refetch)→re-render→reconnect, which flickers
  // the pipeline as the event log re-accumulates from scratch. The ref keeps the
  // effect keyed only on [runId, enabled] so the stream opens once per run.
  const onEndRef = useRef(opts?.onEnd)
  useEffect(() => {
    onEndRef.current = opts?.onEnd
  })

  useEffect(() => {
    if (!enabled || runId == null) return
    const es = new EventSource(`/api/atlas/runs/${runId}/stream`)
    es.onopen = () => {
      setEvents([])
      setStreaming(true)
    }
    es.onmessage = (e) => {
      let ev: AtlasRunEvent
      try {
        ev = JSON.parse(e.data) as AtlasRunEvent
      } catch {
        return
      }
      if (ev.stage === '__end__') {
        es.close()
        setStreaming(false)
        onEndRef.current?.()
        return
      }
      setEvents((prev) => [...prev, ev])
    }
    es.onerror = () => {
      // The browser auto-reconnects; the backend replays the full log so the
      // onopen clear keeps the rendered log consistent. Reflect the gap.
      setStreaming(false)
    }
    return () => {
      es.close()
      setStreaming(false)
    }
  }, [runId, enabled])

  return { events, streaming }
}
