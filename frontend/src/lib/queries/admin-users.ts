import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../api'
import type { AdminUserRow, AdminUsersPage, AdminUserWindow } from '../types'
import type { RecentChange } from './recent-changes'
import { authKeys } from './auth'

export const adminUserKeys = {
  all: ['admin-users'] as const,
  // Prefix over every window's list — what mutations invalidate, so a user
  // edited while viewing 3m doesn't leave a stale 1m list behind it.
  lists: () => [...adminUserKeys.all, 'list'] as const,
  list: (window: AdminUserWindow) => [...adminUserKeys.lists(), window] as const,
  activity: (id: number) => [...adminUserKeys.all, 'activity', id] as const,
}

// One user's recent edits, instance-wide (GET /api/admin/users/{id}/activity).
// Instance-admin only; unlike the home feed it isn't scoped to the caller's
// space access. `enabled` defers the fetch until the activity drawer opens.
export function useAdminUserActivity(userId: number, enabled: boolean) {
  return useQuery({
    queryKey: adminUserKeys.activity(userId),
    enabled,
    queryFn: async () => {
      const { changes } = await api<{ changes: RecentChange[] }>(
        `/api/admin/users/${userId}/activity`,
      )
      return changes
    },
    staleTime: 15_000,
  })
}

// Lists every user (active + inactive) for the instance-admin Settings tab,
// with each row's activity aggregated over `window`. The whole population comes
// down in one payload and the table sorts client-side, so switching a sort
// column costs no request — only switching the window does. 403 to non-admins
// surfaces as the query erroring; the UI only mounts this from an admin-gated
// tab so that path should not fire in practice.
export function useAdminUsers(window: AdminUserWindow = '1m') {
  return useQuery({
    queryKey: adminUserKeys.list(window),
    queryFn: () => api<AdminUsersPage>(`/api/admin/users?window=${window}`),
    staleTime: 30_000,
  })
}

export interface CreateAdminUserInput {
  username: string
  // Optional. Admin-created accounts with an email are treated as
  // pre-confirmed (no verification email is sent).
  email?: string
  password: string
  is_instance_admin?: boolean
}

export function useCreateAdminUser() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (input: CreateAdminUserInput) => {
      const { user } = await api<{ user: AdminUserRow }>('/api/admin/users', {
        method: 'POST',
        body: JSON.stringify(input),
      })
      return user
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: adminUserKeys.lists() })
    },
  })
}

// PATCH /api/admin/users/{id}. Backend accepts any subset of these fields;
// at least one must be present (server returns 400 `bad_request` otherwise).
// Password reset or is_active=false wipes ALL sessions for the target user
// in the same tx.
export interface UpdateAdminUserInput {
  id: number
  is_active?: boolean
  is_instance_admin?: boolean
  password?: string
}

export function useUpdateAdminUser() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (input: UpdateAdminUserInput) => {
      const { id, ...body } = input
      const { user } = await api<{ user: AdminUserRow }>(
        `/api/admin/users/${id}`,
        { method: 'PATCH', body: JSON.stringify(body) },
      )
      return user
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: adminUserKeys.lists() })
      // Defensive: if the patch ever targeted the caller (the UI hides
      // self-actions, but the backend would also reject), invalidate /me
      // so any cached state stays consistent.
      void qc.invalidateQueries({ queryKey: authKeys.me() })
    },
  })
}
