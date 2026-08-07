import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../api'
import { authKeys } from './auth'
import type { AdminUsage, FeedbackEntry, FeedbackStatus } from '../types'

export const adminUsageKeys = {
  usage: ['admin-usage'] as const,
  // Keyed by filter so switching Open/All doesn't serve the other list's cache.
  // feedbackAll is the shared prefix — invalidate it to refresh every filter at
  // once, which any status change needs (the row moves between lists).
  feedbackAll: ['admin-feedback'] as const,
  feedback: (status?: FeedbackStatus) => ['admin-feedback', status ?? 'all'] as const,
}

// GET /api/admin/usage — instance-wide usage overview. Instance-admin only.
export function useAdminUsage() {
  return useQuery({
    queryKey: adminUsageKeys.usage,
    queryFn: () => api<AdminUsage>('/api/admin/usage'),
    staleTime: 30_000,
  })
}

// GET /api/admin/feedback — submitted feedback, newest first. Instance-admin only.
// `status` narrows to one triage state; omit for everything.
export function useAdminFeedback(status?: FeedbackStatus) {
  return useQuery({
    queryKey: adminUsageKeys.feedback(status),
    queryFn: async () => {
      const qs = status ? `?status=${status}` : ''
      const { feedback } = await api<{ feedback: FeedbackEntry[] }>(`/api/admin/feedback${qs}`)
      return feedback
    },
    staleTime: 15_000,
  })
}

// PATCH /api/admin/feedback/{id} — move an entry between open/done/wontfix.
// Invalidates every filtered list, since a status change moves the row between
// them (an entry marked done must leave the Open list it was actioned from).
export function useSetFeedbackStatus() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, status }: { id: number; status: FeedbackStatus }) =>
      api(`/api/admin/feedback/${id}`, {
        method: 'PATCH',
        body: JSON.stringify({ status }),
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: adminUsageKeys.feedbackAll })
    },
  })
}

// POST /api/admin/feedback/seen — clear the unread badge (stamps feedback_seen_at).
// Invalidates /me so the badge count refreshes.
export function useMarkFeedbackSeen() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => api('/api/admin/feedback/seen', { method: 'POST' }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: authKeys.me() })
    },
  })
}
