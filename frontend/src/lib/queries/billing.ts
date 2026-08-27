import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../api'
import type { Plan, Usage } from '../types'
import { adminUserKeys } from './admin-users'
import { orgKeys } from './orgs'

// Metering & tiers. usage = an account's plan + live consumption; plans = the
// tier catalog (for comparison UI); setPlan = instance-admin assignment (there's
// no self-serve billing).
export const billingKeys = {
  all: ['billing'] as const,
  myUsage: () => [...billingKeys.all, 'usage', 'me'] as const,
  orgUsage: (orgId: number) => [...billingKeys.all, 'usage', 'org', orgId] as const,
  plans: () => [...billingKeys.all, 'plans'] as const,
}

// GET /api/usage — the caller's personal-account plan + usage.
export function useMyUsage() {
  return useQuery({
    queryKey: billingKeys.myUsage(),
    queryFn: () => api<Usage>('/api/usage'),
    staleTime: 30_000,
  })
}

// GET /api/orgs/{id}/usage — an org's plan + usage (any member may read).
export function useOrgUsage(orgId: number | null | undefined) {
  return useQuery({
    queryKey: billingKeys.orgUsage(orgId ?? -1),
    queryFn: () => api<Usage>(`/api/orgs/${orgId}/usage`),
    enabled: orgId != null,
    staleTime: 30_000,
  })
}

// GET /api/plans — every tier, for the plan-comparison UI.
//
// `src` names the surface asking. The billing screen passes 'billing' so the
// backend can record that a real person was shown the prices (billing_events.go)
// — the admin tier-picker reads the same catalog and must not look like demand.
// It is part of the query key so the two callers don't share a cache entry and
// silently swallow each other's signal.
export function usePlans(src?: 'billing') {
  return useQuery({
    queryKey: [...billingKeys.plans(), src ?? 'none'],
    queryFn: async () => {
      const { plans } = await api<{ plans: Plan[] }>(`/api/plans${src ? `?src=${src}` : ''}`)
      return plans
    },
    staleTime: 5 * 60_000,
  })
}

// POST /api/billing/checkout — start a Polar checkout for a tier and hand the
// browser to the hosted URL. org_id omitted = the caller's personal account.
// Entitlement is granted later by the webhook, not this redirect.
export function useCheckout() {
  return useMutation({
    mutationFn: (input: { plan_key: string; org_id?: number; interval?: 'month' | 'year' }) =>
      api<{ url: string }>('/api/billing/checkout', {
        method: 'POST',
        body: JSON.stringify(input),
      }),
    onSuccess: ({ url }) => {
      window.location.href = url
    },
  })
}

// POST /api/billing/portal — open the Polar customer portal to manage / cancel /
// update payment. org_id omitted = personal account.
export function useBillingPortal() {
  return useMutation({
    mutationFn: (input: { org_id?: number }) =>
      api<{ url: string }>('/api/billing/portal', {
        method: 'POST',
        body: JSON.stringify(input),
      }),
    onSuccess: ({ url }) => {
      window.location.href = url
    },
  })
}

// A purchased self-host Enterprise license key (issued by the cloud on subscribe).
export interface SelfHostLicense {
  id: number
  tier: string
  seats: number
  status: string
  token: string
  issued_at: string
  expires_at?: string
}

// GET /api/licenses — the caller's self-host Enterprise keys + whether this
// instance can sell them (managed cloud with the product + signer wired).
export function useMyLicenses() {
  return useQuery({
    queryKey: [...billingKeys.all, 'licenses'] as const,
    queryFn: () => api<{ licenses: SelfHostLicense[]; sales_enabled: boolean }>('/api/licenses'),
    staleTime: 30_000,
  })
}

// POST /api/billing/selfhost-checkout — start a Polar checkout for a self-host
// Enterprise license (seat-based) and hand the browser to the hosted URL. The key
// is minted + emailed by the webhook, and appears under /api/licenses afterwards.
export function useSelfHostCheckout() {
  return useMutation({
    mutationFn: (input: { seats: number }) =>
      api<{ url: string }>('/api/billing/selfhost-checkout', {
        method: 'POST',
        body: JSON.stringify(input),
      }),
    onSuccess: ({ url }) => {
      window.location.href = url
    },
  })
}

// POST /api/billing/selfhost-portal — open the Polar customer portal for the
// caller's self-host license subscription (keyed by external id, so it works even
// for a buyer on the Free cloud plan with no cloud customer id).
export function useSelfHostPortal() {
  return useMutation({
    mutationFn: () => api<{ url: string }>('/api/billing/selfhost-portal', { method: 'POST' }),
    onSuccess: ({ url }) => {
      window.location.href = url
    },
  })
}

export interface SetPlanInput {
  account_kind: 'user' | 'org'
  account_id: number
  plan_key: string
}

// PATCH /api/admin/plan — instance-admin only. Invalidates the affected
// account's usage so the panel reflects the new tier immediately.
export function useSetPlan() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: SetPlanInput) =>
      api<Usage>('/api/admin/plan', {
        method: 'PATCH',
        body: JSON.stringify(input),
      }),
    onSuccess: (updated) => {
      // Usage cards for the affected account.
      if (updated.account_kind === 'org') {
        void qc.invalidateQueries({ queryKey: billingKeys.orgUsage(updated.account_id) })
      } else {
        void qc.invalidateQueries({ queryKey: billingKeys.myUsage() })
      }
      // The admin Users + Orgs lists now carry plan_key — refresh their badges.
      void qc.invalidateQueries({ queryKey: orgKeys.list() })
      void qc.invalidateQueries({ queryKey: adminUserKeys.list() })
    },
  })
}
