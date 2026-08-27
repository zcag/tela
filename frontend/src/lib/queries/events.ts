import { useInfiniteQuery } from '@tanstack/react-query'
import { api } from '../api'
import type { EventEntry } from '../types'

// The unified activity feed (GET /api/admin/events), instance-admin only.
// First useInfiniteQuery in the app: the backend returns `next_cursor` (the last
// row's id), which we pass back as `before` for keyset pagination.

export interface EventFilters {
  // Event-type codes to include (empty = all).
  types?: string[]
  userId?: number
  // Free-text over actor/target/detail.
  q?: string
  // 'YYYY-MM-DD' lower bound on created_at.
  since?: string
  // Include instance-admin activity (default: hidden as operator noise).
  includeAdmins?: boolean
}

interface EventsPage {
  events: EventEntry[]
  next_cursor: number | null
}

export const eventsKeys = {
  all: ['events'] as const,
  list: (f: EventFilters) => [...eventsKeys.all, 'list', f] as const,
}

// Filter taxonomy for the UI — grouped chips. A token ending in '.' is a family
// prefix the backend expands with LIKE (so 'access.' covers every access.<action>).
export const EVENT_TYPE_GROUPS: { label: string; types: string[] }[] = [
  { label: 'Auth', types: ['auth.login', 'auth.login_failed', 'auth.logout'] },
  { label: 'Pages', types: ['page.view', 'page.create', 'page.edit'] },
  { label: 'Access', types: ['access.'] },
  { label: 'Ask', types: ['ask'] },
  { label: 'API', types: ['api.request'] },
  { label: 'Errors', types: ['client.error'] },
  { label: 'Billing', types: ['billing.'] },
]

export function useInfiniteEvents(filters: EventFilters) {
  return useInfiniteQuery({
    queryKey: eventsKeys.list(filters),
    initialPageParam: null as number | null,
    queryFn: async ({ pageParam }) => {
      const params = new URLSearchParams()
      if (filters.types && filters.types.length > 0) {
        params.set('types', filters.types.join(','))
      }
      if (filters.userId) params.set('user_id', String(filters.userId))
      if (filters.q && filters.q.trim()) params.set('q', filters.q.trim())
      if (filters.since) params.set('since', filters.since)
      if (filters.includeAdmins) params.set('include_admins', '1')
      if (pageParam != null) params.set('before', String(pageParam))
      const qs = params.toString()
      return api<EventsPage>(`/api/admin/events${qs ? `?${qs}` : ''}`)
    },
    getNextPageParam: (last) => last.next_cursor,
    staleTime: 10_000,
  })
}
