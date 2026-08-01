import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../api'
import { orgKeys } from './orgs'
import { spaceKeys } from './spaces'
import { memberKeys } from './members'
import { spaceAccessKeys } from './space-grants'
import type { SpaceMember } from '../types'

// Email invitations. One link shape (/invite/{token}) covers both things you can
// be invited to — an organization and a single space — so the public lookup and
// the accept call are shared and discriminated by `kind`. Managing pending
// invites is scoped per kind (org admin / space owner).

export interface InviteInfo {
  valid: boolean
  kind?: 'org' | 'space'
  org_name?: string
  space_name?: string
  inviter?: string
  role?: string
  email?: string
}

export interface OrgInvite {
  id: number
  email: string
  org_role: string
  created_at?: string
  expires_at?: string
}

export interface SpaceInvite {
  id: number
  email: string
  role: SpaceMember['role']
  created_at?: string
  expires_at?: string
}

// POST /api/spaces/{id}/invites answers one of two ways: the address already had
// an account (access granted immediately) or an invitation was emailed.
export type ShareSpaceByEmailResult =
  | { member: SpaceMember; invite?: undefined }
  | { invite: SpaceInvite; member?: undefined }

const inviteKeys = {
  one: (token: string) => ['invite', token] as const,
  list: (orgId: number) => [...orgKeys.all, orgId, 'invites'] as const,
  spaceList: (spaceId: number) => [...spaceKeys.all, spaceId, 'invites'] as const,
}

// GET /api/invites/{token} — public; renders the accept page for a logged-out
// invitee, org or space. `valid:false` when missing/expired (never an error).
export function useInvite(token: string) {
  return useQuery({
    queryKey: inviteKeys.one(token),
    queryFn: () => api<InviteInfo>(`/api/invites/${encodeURIComponent(token)}`),
    enabled: !!token,
    retry: false,
  })
}

// POST /api/me/accept-invite — the logged-in invitee joins the org or space.
export function useAcceptInvite() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (token: string) =>
      api<{ org?: unknown; space?: unknown }>('/api/me/accept-invite', {
        method: 'POST',
        body: JSON.stringify({ token }),
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: orgKeys.list() })
      void qc.invalidateQueries({ queryKey: spaceKeys.lists() })
    },
  })
}

// GET /api/orgs/{id}/invites — pending invites (org admin).
export function useOrgInvites(orgId: number | null | undefined) {
  return useQuery({
    queryKey: inviteKeys.list(orgId ?? -1),
    queryFn: async () => (await api<{ invites: OrgInvite[] }>(`/api/orgs/${orgId}/invites`)).invites,
    enabled: orgId != null,
  })
}

// POST /api/orgs/{id}/invites — invite a teammate by email (org admin).
export function useCreateOrgInvite(orgId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: { email: string; org_role?: string }) =>
      api<{ invite: OrgInvite }>(`/api/orgs/${orgId}/invites`, {
        method: 'POST',
        body: JSON.stringify(input),
      }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: inviteKeys.list(orgId) }),
  })
}

// DELETE /api/orgs/{id}/invites/{inviteId} — revoke a pending invite (org admin).
export function useRevokeOrgInvite(orgId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (inviteId: number) =>
      api<void>(`/api/orgs/${orgId}/invites/${inviteId}`, { method: 'DELETE' }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: inviteKeys.list(orgId) }),
  })
}

// GET /api/spaces/{id}/invites — pending invitations to this space (owner only).
export function useSpaceInvites(spaceId: number | null | undefined) {
  return useQuery({
    queryKey: inviteKeys.spaceList(spaceId ?? -1),
    queryFn: async () =>
      (await api<{ invites: SpaceInvite[] }>(`/api/spaces/${spaceId}/invites`)).invites,
    enabled: spaceId != null,
  })
}

// POST /api/spaces/{id}/invites — share the space with an email address (owner
// only). Grants access immediately when that address already has an account,
// otherwise emails an invitation — the response says which happened.
export function useShareSpaceByEmail(spaceId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: { email: string; role: SpaceMember['role'] }) =>
      api<ShareSpaceByEmailResult>(`/api/spaces/${spaceId}/invites`, {
        method: 'POST',
        body: JSON.stringify(input),
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: inviteKeys.spaceList(spaceId) })
      void qc.invalidateQueries({ queryKey: memberKeys.list(spaceId) })
      void qc.invalidateQueries({ queryKey: spaceAccessKeys.list(spaceId) })
    },
  })
}

// DELETE /api/spaces/{id}/invites/{inviteId} — revoke a pending invitation.
export function useRevokeSpaceInvite(spaceId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (inviteId: number) =>
      api<void>(`/api/spaces/${spaceId}/invites/${inviteId}`, { method: 'DELETE' }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: inviteKeys.spaceList(spaceId) }),
  })
}
