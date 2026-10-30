import { useState } from 'react'
import { Link, useNavigate, useSearch } from '@tanstack/react-router'
import { ApiError } from '../lib/api'
import {
  sanitizeNextPath,
  useLogin,
  useResendVerification,
} from '../lib/queries/auth'
import { useHostContext } from '../lib/queries/host-context'
import { Button } from '../components/ui/button'
import {
  Card,
  CardBody,
  CardDescription,
  CardHeader,
  CardTitle,
} from '../components/ui/card'
import { Input } from '../components/ui/input'
import { AuthShell, AuthField, AuthFooterLink } from '../components/app/AuthShell'
import { SSOButtons } from '../components/app/SSOButtons'
import { MaintenanceBanner } from '../components/app/MaintenanceBanner'
import { useDocumentTitle } from '../lib/useHeadMeta'

export function LoginPage() {
  useDocumentTitle('Log in')
  const navigate = useNavigate()
  const search = useSearch({ from: '/login' }) as {
    next?: string
    sso_error?: string
  }
  const [identifier, setIdentifier] = useState('')
  const [password, setPassword] = useState('')
  // Seed with any federated-login failure bounced back via ?sso_error=.
  const [error, setError] = useState<string | null>(search.sso_error ?? null)
  // When login fails because the account's email isn't confirmed, we surface a
  // dedicated "resend" affordance instead of the generic error.
  const [unverified, setUnverified] = useState(false)
  const [resent, setResent] = useState(false)
  const login = useLogin()
  const resend = useResendVerification()
  const nextPath = sanitizeNextPath(search.next) ?? '/'
  // Host-derived white-labeling: on an org's custom domain we address the org by
  // name and honor its login-method toggles. Degrades to the canonical default
  // (org null, all methods on) while loading / on error — never blocks the form.
  const host = useHostContext().data
  const org = host?.org ?? null
  const passwordEnabled = host?.login.password_enabled ?? true

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    const id = identifier.trim()
    if (!id || !password) {
      setError('Email or username and password are required.')
      return
    }
    setError(null)
    setUnverified(false)
    setResent(false)
    try {
      await login.mutateAsync({ identifier: id, password })
      // The WorkOS OAuth bridge (/oauth/workos/login) is a BACKEND route, not an
      // SPA route — hard-navigate so the browser hits the server (which completes
      // the Connect flow), not the client router.
      if (nextPath.startsWith('/oauth/')) {
        window.location.assign(nextPath)
        return
      }
      // Cast: `nextPath` is an in-app path validated by sanitizeNextPath; the
      // router's typed route tree doesn't know it as a literal.
      void navigate({ to: nextPath as never })
    } catch (err) {
      if (err instanceof ApiError && err.code === 'email_unverified') {
        setUnverified(true)
      } else if (err instanceof ApiError && err.code === 'sso_required') {
        setError(
          'Your organization requires single sign-on. Use an SSO option below.',
        )
      } else if (err instanceof ApiError && err.status === 401) {
        setError('Invalid credentials.')
      } else if (err instanceof ApiError) {
        setError(err.message)
      } else {
        setError('Something went wrong. Please try again.')
      }
    }
  }

  async function handleResend() {
    try {
      await resend.mutateAsync(identifier.trim())
      setResent(true)
    } catch {
      setResent(true) // endpoint is always-202; treat any outcome as "sent"
    }
  }

  return (
    <AuthShell>
      <div className="w-full max-w-[24rem] empty:hidden">
        <MaintenanceBanner />
      </div>
      <Card className="tela-auth-card w-full bg-[var(--surface-1)] shadow-[var(--shadow-lg)]">
        <CardHeader className="gap-[var(--space-2)] px-[var(--space-7)] pt-[var(--space-7)] pb-[var(--space-2)]">
          <CardTitle className="text-[length:var(--text-2xl)] font-semibold tracking-[-0.01em]">
            {org ? `Sign in to ${org.name}` : 'Sign in'}
          </CardTitle>
          <CardDescription>
            {org
              ? `Welcome back — sign in to ${org.name}.`
              : 'Welcome back — sign in to your tela workspace.'}
          </CardDescription>
        </CardHeader>
        <CardBody className="gap-[var(--space-5)] px-[var(--space-7)] pb-[var(--space-7)] pt-[var(--space-2)]">
          {/* On a custom domain that's disabled password sign-in, the credential
              form is hidden entirely — the user signs in via SSO / social below.
              Any bounced-back error (e.g. ?sso_error) still surfaces. */}
          {passwordEnabled ? (
          <form
            onSubmit={handleSubmit}
            className="flex flex-col gap-[var(--space-4)]"
            noValidate
          >
            <AuthField id="login-identifier" label="Email or username">
              <Input
                id="login-identifier"
                autoFocus
                autoComplete="username"
                value={identifier}
                onChange={(e) => setIdentifier(e.target.value)}
                aria-invalid={error != null}
              />
            </AuthField>
            <AuthField
              id="login-password"
              label="Password"
              labelSlot={
                <Link
                  to="/forgot-password"
                  className="text-[length:var(--text-sm)] text-[var(--text-muted)] no-underline hover:text-[var(--text-primary)] hover:underline"
                >
                  Forgot password?
                </Link>
              }
            >
              <Input
                id="login-password"
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                aria-invalid={error != null}
              />
            </AuthField>
            {unverified ? (
              <div
                role="alert"
                className="flex flex-col gap-[var(--space-2)] text-[length:var(--text-sm)] text-[var(--text-muted)]"
              >
                {resent ? (
                  <p className="m-0">
                    If that account needs confirming, a new link is on its way.
                  </p>
                ) : (
                  <>
                    <p className="m-0 text-[var(--danger)]">
                      Confirm your email before signing in.
                    </p>
                    <button
                      type="button"
                      onClick={() => void handleResend()}
                      disabled={resend.isPending}
                      className="self-start bg-transparent border-none p-0 text-[var(--accent)] underline cursor-pointer disabled:opacity-60"
                    >
                      {resend.isPending
                        ? 'Sending…'
                        : 'Resend confirmation email'}
                    </button>
                  </>
                )}
              </div>
            ) : null}
            {error ? (
              <div
                role="alert"
                style={{
                  borderColor:
                    'color-mix(in oklch, var(--danger) 25%, transparent)',
                  backgroundColor:
                    'color-mix(in oklch, var(--danger) 8%, transparent)',
                }}
                className="m-0 rounded-[var(--radius-sm)] border px-[var(--space-3)] py-[var(--space-2)] text-[length:var(--text-sm)] text-[var(--danger)]"
              >
                {error}
              </div>
            ) : null}
            <Button
              type="submit"
              variant="primary"
              size="lg"
              disabled={login.isPending}
            >
              {login.isPending ? 'Signing in…' : 'Sign in'}
            </Button>
          </form>
          ) : (
            <div className="flex flex-col gap-[var(--space-4)]">
              <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)] leading-[var(--leading-relaxed)]">
                Password sign-in is turned off for this domain. Use one of the
                options below.
              </p>
              {error ? (
                <div
                  role="alert"
                  style={{
                    borderColor:
                      'color-mix(in oklch, var(--danger) 25%, transparent)',
                    backgroundColor:
                      'color-mix(in oklch, var(--danger) 8%, transparent)',
                  }}
                  className="m-0 rounded-[var(--radius-sm)] border px-[var(--space-3)] py-[var(--space-2)] text-[length:var(--text-sm)] text-[var(--danger)]"
                >
                  {error}
                </div>
              ) : null}
            </div>
          )}
          <SSOButtons
            next={nextPath}
            mainEmail={
              passwordEnabled
                ? {
                    value: identifier,
                    focus: () =>
                      document.getElementById('login-identifier')?.focus(),
                  }
                : undefined
            }
          />
          <AuthFooterLink>
            New to tela?{' '}
            <Link
              to="/register"
              className="text-[var(--accent)] no-underline hover:underline"
            >
              Create an account
            </Link>
          </AuthFooterLink>
        </CardBody>
      </Card>
    </AuthShell>
  )
}
