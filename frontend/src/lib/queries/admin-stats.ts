import { useQuery } from '@tanstack/react-query'
import { api } from '../api'

// Instance-analytics dashboard payload. Mirrors backend adminStats
// (admin_stats.go). The series are dense daily buckets aligned to `days`.

export interface StatsTopPage {
  page_id: number
  space_id: number
  title: string
  space_name: string
  count: number
}
export interface StatsTopPerson {
  label: string
  count: number
}
export interface StatsTopSpace {
  space_id: number
  name: string
  count: number
}
export interface StatsKindCount {
  kind: string
  count: number
}
export interface StatsSignup {
  user_id: number
  username: string
  display_name: string
  email: string
  created_at: string
  activated: boolean
}
// One row of the signup-source breakdown (30d). `source` is utm_source, else
// the referring host, else 'direct'.
export interface StatsSignupSource {
  source: string
  count: number
}
export interface StatsUnanswered {
  question: string
  who: string
  created_at: string
}

export interface AdminStats {
  days: string[]
  views: number[]
  edits: number[]
  logins: number[]
  asks: number[]
  errors: number[]
  // Accounts created that day, aligned to `days` — the per-day read of growth
  // that `users_cum` only implies.
  signups: number[]
  users_cum: number[]
  pages_cum: number[]
  dau: number
  wau: number
  mau: number
  users: number
  spaces: number
  pages: number
  // Where the last 30 days of signups came from, best first. Counts only the
  // accounts that carry attribution — signup_sources_known of new_users_30.
  signup_sources: StatsSignupSource[]
  signup_sources_known: number
  top_pages: StatsTopPage[]
  top_contributors: StatsTopPerson[]
  top_spaces: StatsTopSpace[]
  asks_30: number
  asks_answered_30: number
  errors_by_kind: StatsKindCount[]
  stale_pages: number
  orphan_pages: number
  contradictions: number
  new_users_30: number
  activated: number
  recent_signups: StatsSignup[]
  unanswered_asks: StatsUnanswered[]
  wau_prev: number
  mcp_users: number
  active_pats: number
  public_spaces: number
  active_trials: number
  paid_subscriptions: number
  ai_healthy: boolean
  ai_reason?: string
}

export const adminStatsKeys = {
  stats: (includeAdmins: boolean) => ['admin-stats', { includeAdmins }] as const,
}

// GET /api/admin/stats — instance-admin only. Admin activity is excluded from the
// activity aggregates by default; includeAdmins re-includes it (?include_admins=1).
export function useAdminStats(includeAdmins = false) {
  return useQuery({
    queryKey: adminStatsKeys.stats(includeAdmins),
    queryFn: () =>
      api<AdminStats>(`/api/admin/stats${includeAdmins ? '?include_admins=1' : ''}`),
    staleTime: 60_000,
  })
}
