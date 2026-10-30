import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../api'
import type { Branding } from '../types'
import { hostContextKeys } from './host-context'

// Branding (logo + accent, and a name for the instance) behind one endpoint
// shape: an org's at /api/orgs/{id}/branding, the instance's at
// /api/admin/branding. The logo is always stored IN tela (upload or
// import-from-URL) and served from tela's origin. A save also refreshes
// host-context, which is what the shell renders the brand from.

export const brandingKeys = {
  all: ['branding'] as const,
  detail: (base: string) => [...brandingKeys.all, base] as const,
}

export const orgBrandingBase = (orgId: number) => `/api/orgs/${orgId}/branding`
export const instanceBrandingBase = '/api/admin/branding'

export function useBranding(base: string) {
  return useQuery({
    queryKey: brandingKeys.detail(base),
    queryFn: async () => api<Branding>(base),
    staleTime: 30_000,
  })
}

function useBrandingMutation<T>(base: string, run: (input: T) => Promise<Branding>) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: run,
    onSuccess: (saved) => {
      qc.setQueryData(brandingKeys.detail(base), saved)
      void qc.invalidateQueries({ queryKey: hostContextKeys.all })
    },
  })
}

// PUT: sets accent (and name, for the instance); optionally imports a logo
// from a URL (tela fetches + stores it once).
export function usePutBranding(base: string) {
  return useBrandingMutation(base, (input: { name?: string; accent: string; logo_import_url?: string }) =>
    api<Branding>(base, { method: 'PUT', body: JSON.stringify(input) }),
  )
}

// POST {base}/logo: upload an image file (raw bytes). The File's own type rides
// as Content-Type; the backend validates by sniffing.
export function useUploadLogo(base: string) {
  return useBrandingMutation(base, (file: File) =>
    api<Branding>(`${base}/logo`, {
      method: 'POST',
      body: file,
      headers: { 'Content-Type': file.type || 'application/octet-stream' },
    }),
  )
}

export function useDeleteLogo(base: string) {
  return useBrandingMutation(base, (_: void) => api<Branding>(`${base}/logo`, { method: 'DELETE' }))
}
