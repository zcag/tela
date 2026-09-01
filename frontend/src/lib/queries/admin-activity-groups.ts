import { useQuery } from '@tanstack/react-query'
import { api } from '../api'
import type { AdminUserWindow } from '../types'

// Per-space / per-org activity (GET /api/admin/activity/groups), instance-admin
// only. Same windows as the People table — this is the same question keyed by
// content or by team instead of by person.

export type ActivityGroupBy = 'space' | 'org'

export interface ActivityGroup {
  id: number
  name: string
  // Owning handle for a space, member count for an org. Display only.
  detail: string
  edits: number
  agent_edits: number
  sync_edits: number
  views: number
  asks: number
  llm_calls: number
  people: number
  active_people: number
  pages: number
  last_active: string
}

export const activityGroupKeys = {
  all: ['admin-activity-groups'] as const,
  list: (by: ActivityGroupBy, window: AdminUserWindow) =>
    [...activityGroupKeys.all, by, window] as const,
}

export function useActivityGroups(
  by: ActivityGroupBy,
  window: AdminUserWindow,
  enabled: boolean,
) {
  return useQuery({
    queryKey: activityGroupKeys.list(by, window),
    // Deferred until the group-by is actually selected: most visits to this tab
    // are about people, and this is a second set of aggregate scans.
    enabled,
    queryFn: async () => {
      const { groups } = await api<{ groups: ActivityGroup[] }>(
        `/api/admin/activity/groups?by=${by}&window=${window}`,
      )
      return groups
    },
    staleTime: 30_000,
  })
}
