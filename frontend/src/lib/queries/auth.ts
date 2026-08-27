import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, api } from '../api'
import { pageKeys } from './pages'
import { spaceKeys } from './spaces'
import type { TrialStatus } from '../types'

export type { TrialStatus }

// Mirrors backend/internal/api/auth.go's authUserDTO.
export interface AuthUser {
  id: number
  username: string
  // Human-readable name used to address the user (greeting, etc.), distinct
  // from the URL-safe username. '' when unset → fall back to username.
  display_name: string
  email: string | null
  email_verified: boolean
  is_instance_admin: boolean
  // Author bio shown on /u/{handle}. '' when unset.
  bio?: string
  // Active trial in its notify window (final 7 days + 7-day grace), else absent.
  trial?: TrialStatus
  // Unread feedback count for the admin inbox badge (instance admins only).
  feedback_unseen?: number
  // Whether the user has ever made an authenticated MCP request (any credential).
  // Drives the "connect an agent" nudge.
  mcp_connected?: boolean
}


export interface LoginInput {
  // Email or username.
  identifier: string
  password: string
}

export interface RegisterInput {
  email: string
  username: string
  password: string
}

export const authKeys = {
  all: ['auth'] as const,
  me: () => [...authKeys.all, 'me'] as const,
}

// Probe the session cookie. The HttpOnly cookie isn't readable from JS, so the
// only honest way to know whether we're logged in is to ask /api/auth/me.
// 401 → null (the canonical "no session" state); other failures bubble up.
export async function fetchMe(): Promise<AuthUser | null> {
  try {
    const { user } = await api<{ user: AuthUser }>('/api/auth/me')
    return user
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) return null
    throw err
  }
}

export function useMe() {
  return useQuery({
    queryKey: authKeys.me(),
    queryFn: fetchMe,
    retry: false,
    staleTime: Infinity,
  })
}

// Patch the caller's own profile (display name and/or bio). Only the supplied
// fields are sent; the server echoes back the saved values, which we merge into
// the me cache in place so the settings form and any open /u/{handle} preview
// reflect them.
export function useUpdateProfile() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (input: { display_name?: string; bio?: string }) => {
      return api<{ display_name?: string; bio?: string }>('/api/users/me', {
        method: 'PATCH',
        body: JSON.stringify(input),
      })
    },
    onSuccess: (saved) => {
      qc.setQueryData<AuthUser | null>(authKeys.me(), (curr) =>
        curr ? { ...curr, ...saved } : curr,
      )
    },
  })
}

export interface SetupInput {
  username: string
  email: string
  password: string
}

// First-run setup status. Public + raw fetch (no session exists yet on a fresh
// instance, and api() would treat any failure as an auth bounce). A failure
// degrades to needs_setup=false so a transient blip never strands a working
// instance on the setup screen.
export async function fetchSetupStatus(): Promise<boolean> {
  try {
    const res = await fetch('/api/setup/status')
    if (!res.ok) return false
    const body = (await res.json()) as { needs_setup?: boolean }
    return body.needs_setup === true
  } catch {
    return false
  }
}

// Create the FIRST instance admin via the setup wizard. On success the backend
// signs the user in (sets the session cookie) and returns the user, so we seed
// the me cache and land straight in the app. 409 already_setup → an admin
// already exists (race / refresh).
export function useSetup() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (input: SetupInput) => {
      const { user } = await api<{ user: AuthUser }>('/api/setup', {
        method: 'POST',
        body: JSON.stringify(input),
      })
      return user
    },
    onSuccess: (user) => {
      qc.setQueryData(authKeys.me(), user)
    },
  })
}

export function useLogin() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (input: LoginInput) => {
      const { user } = await api<{ user: AuthUser }>('/api/auth/login', {
        method: 'POST',
        body: JSON.stringify(input),
      })
      return user
    },
    onSuccess: (user) => {
      // Seed the me cache so AppLayout's beforeLoad doesn't issue an extra
      // round-trip on the post-login redirect.
      qc.setQueryData(authKeys.me(), user)
    },
  })
}

// Self-registration. 201 with the unconfirmed account's email; the user must
// follow the verification link before they can sign in. 409 → email/username
// taken.
export function useRegister() {
  return useMutation({
    mutationFn: async (input: RegisterInput) => {
      const { email } = await api<{ ok: true; email: string }>(
        '/api/auth/register',
        { method: 'POST', body: JSON.stringify(input) },
      )
      return email
    },
  })
}

// Confirm an email via the token from the link. On success the backend signs
// the user in (sets the session cookie) and returns the user, so we seed the
// me cache and land straight in the app.
export function useVerifyEmail() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (token: string) => {
      const { user } = await api<{ user: AuthUser }>('/api/auth/verify-email', {
        method: 'POST',
        body: JSON.stringify({ token }),
      })
      return user
    },
    onSuccess: (user) => {
      qc.setQueryData(authKeys.me(), user)
    },
  })
}

// Re-send a confirmation link. Always resolves (the backend returns 202
// regardless of whether the address exists), so the UI can show a single
// neutral "if that account exists, we've sent a link" message.
export function useResendVerification() {
  return useMutation({
    mutationFn: async (email: string) => {
      await api<{ ok: true }>('/api/auth/resend-verification', {
        method: 'POST',
        body: JSON.stringify({ email }),
      })
    },
  })
}

// Request a password-reset link. Always 202 (no account enumeration).
export function useRequestPasswordReset() {
  return useMutation({
    mutationFn: async (email: string) => {
      await api<{ ok: true }>('/api/auth/request-password-reset', {
        method: 'POST',
        body: JSON.stringify({ email }),
      })
    },
  })
}

// Set a new password from a reset token. 400 → invalid/expired link.
export function useResetPassword() {
  return useMutation({
    mutationFn: async (input: { token: string; password: string }) => {
      await api<{ ok: true }>('/api/auth/reset-password', {
        method: 'POST',
        body: JSON.stringify(input),
      })
    },
  })
}

// A configured federated-login button. Mirrors backend ssoProviderDTO.
export interface SSOProvider {
  name: string
  label: string
}

export interface SSOProviders {
  providers: SSOProvider[]
  // True when at least one org has an SSO connection (so the UI can offer the
  // "Sign in with SSO" by-domain affordance).
  org_sso: boolean
}

// Which social buttons to render + whether org SSO exists. Public + cacheable;
// raw fetch (not api()) since there's no session to gate and api() would treat a
// transient failure as an auth bounce. A failure degrades to "no buttons".
export async function fetchSSOProviders(): Promise<SSOProviders> {
  try {
    const res = await fetch('/api/auth/sso/providers')
    if (!res.ok) return { providers: [], org_sso: false }
    return (await res.json()) as SSOProviders
  } catch {
    return { providers: [], org_sso: false }
  }
}

export function useSSOProviders() {
  return useQuery({
    queryKey: ['sso', 'providers'],
    queryFn: fetchSSOProviders,
    staleTime: Infinity,
    retry: false,
  })
}

// Delete the caller's account (GDPR right to erasure). Anonymises the user row,
// wipes sessions/keys/tokens, and removes space memberships. 422 org_owner means
// the user must transfer admin rights or delete their org(s) first.
export function useDeleteMyAccount() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async () => {
      await api<{ ok: true }>('/api/users/me', { method: 'DELETE' })
    },
    onSuccess: () => {
      qc.setQueryData(authKeys.me(), null)
      qc.removeQueries({ queryKey: spaceKeys.all })
      qc.removeQueries({ queryKey: pageKeys.all })
    },
  })
}

export function useLogout() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async () => {
      await api<void>('/api/auth/logout', { method: 'POST' })
    },
    onSettled: () => {
      // Drop the auth state immediately, then sweep any user-scoped data so
      // the next signed-in user doesn't see the previous user's cached
      // spaces/pages. Other namespaces (search, etc.) flow through the same
      // queries on their next mount.
      qc.setQueryData(authKeys.me(), null)
      qc.removeQueries({ queryKey: spaceKeys.all })
      qc.removeQueries({ queryKey: pageKeys.all })
      // Body-fuzzy indexes live in module-scoped state (per-space Orama
      // instances + a once-per-session drift-check flag). Without sweeping
      // them, user A's body content stays addressable in user B's palette
      // until a full page reload. Dynamic-import keeps the Orama runtime
      // off this hot auth path.
      void import('../search/body-index')
        .then((m) => m.clearAllBodyIndexes())
        .catch(() => {
          // Best-effort — chunk-load failure during logout is non-fatal;
          // the next palette-open will still 403-gate non-member spaces.
        })
    },
  })
}

// Validate a `?next=` param before bouncing back into the app. We only accept
// in-app paths (`/foo`), and explicitly reject protocol-relative `//evil.com`
// and absolute / scheme-bearing URLs. We also reject `/login...` so the
// post-login redirect can't ping-pong back through the login route. Empty /
// unrecognised values → null so callers can fall back to '/'.
export function sanitizeNextPath(raw: unknown): string | null {
  if (typeof raw !== 'string') return null
  if (raw.length === 0) return null
  if (!raw.startsWith('/')) return null
  if (raw.startsWith('//')) return null
  if (/^\/login(\/|\?|#|$)/i.test(raw)) return null
  return raw
}
