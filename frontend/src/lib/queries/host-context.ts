import { useQuery } from '@tanstack/react-query'
import type { HostContext } from '../types'

// Sensible canonical-host default: the org is absent and every standard sign-in
// method is on. A transient fetch failure degrades to this so the login screen
// always renders something usable (never strands behind a white-label probe).
const DEFAULT_HOST_CONTEXT: HostContext = {
  org: null,
  canonical_base: '',
  login: {
    password_enabled: true,
    social_enabled: true,
    org_sso_available: false,
  },
  maintenance: null,
  // Default to available so a transient host-context failure never falsely hides AI.
  ai_available: true,
}

// GET /api/host-context — PUBLIC, host-derived. Raw fetch (not api()) since
// there's no session to gate and api() would treat a transient failure as an
// auth bounce. Never throws: any failure degrades to DEFAULT_HOST_CONTEXT.
export async function fetchHostContext(): Promise<HostContext> {
  try {
    const res = await fetch('/api/host-context')
    if (!res.ok) return DEFAULT_HOST_CONTEXT
    return (await res.json()) as HostContext
  } catch {
    return DEFAULT_HOST_CONTEXT
  }
}

// Host-derived white-labeling context for the login surface. Cacheable and
// host-stable for the page's lifetime, so staleTime: Infinity (mirrors
// useSSOProviders). Degrades to DEFAULT_HOST_CONTEXT while loading / on error.
//
// placeholderData (NOT initialData): initialData is persisted as real,
// already-fresh cache data, so paired with staleTime: Infinity the query would
// never fetch — the login screen would stay on the canonical default forever
// and white-labeling (branding + login-method gating) would never apply on a
// custom domain. placeholderData fills `.data` during the in-flight fetch
// without short-circuiting it.
export const hostContextKeys = { all: ['host-context'] as const }

export function useHostContext() {
  return useQuery({
    queryKey: hostContextKeys.all,
    queryFn: fetchHostContext,
    staleTime: Infinity,
    retry: false,
    placeholderData: DEFAULT_HOST_CONTEXT,
  })
}

// The brand this host wears: the org's on its custom domain, else the
// instance's own brand, else tela. `custom` is true whenever it isn't tela, which
// is also when the "Powered by tela" credit shows.
export interface Brand {
  name: string
  logoUrl: string
  accent: string
  custom: boolean
}

export function useBrand(): Brand {
  const ctx = useHostContext().data
  if (ctx?.org) {
    return { name: ctx.org.name, logoUrl: ctx.org.logo_url, accent: ctx.org.accent, custom: true }
  }
  const b = ctx?.brand
  return {
    name: b?.name || 'tela',
    logoUrl: b?.logo_url ?? '',
    accent: b?.accent ?? '',
    custom: Boolean(b?.name || b?.logo_url),
  }
}

// Brand-link target for the "tela" wordmark: the instance's canonical origin
// when the page is being served from some other host (an org custom domain —
// a relative "/" there lands on the org domain's root), else the local root.
export function useTelaHomeHref(): string {
  const ctx = useHostContext()
  const base = ctx.data?.canonical_base ?? ''
  return base && base !== window.location.origin ? base : '/'
}
