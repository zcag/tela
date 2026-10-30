import { useState } from 'react'
import { Link, useSearch } from '@tanstack/react-router'
import { ApiError } from '../lib/api'
import { useRegister, useResendVerification } from '../lib/queries/auth'
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
import { useDocumentTitle } from '../lib/useHeadMeta'

export function RegisterPage() {
  useDocumentTitle('Sign up')
  // Pre-fill the email when arriving from an invite link (/register?email=…).
  const { email: invitedEmail } = useSearch({ from: '/register' })
  const [email, setEmail] = useState(invitedEmail ?? '')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [sentTo, setSentTo] = useState<string | null>(null)
  const register = useRegister()
  const resend = useResendVerification()

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    const em = email.trim()
    const un = username.trim()
    if (!em || !un || !password) {
      setError('Email, username and password are all required.')
      return
    }
    if (password.length < 8) {
      setError('Password must be at least 8 characters.')
      return
    }
    if (password !== confirm) {
      setError("Passwords don't match.")
      return
    }
    setError(null)
    try {
      const confirmed = await register.mutateAsync({
        email: em,
        username: un,
        password,
      })
      setSentTo(confirmed)
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        setError('That email or username is already taken.')
      } else if (err instanceof ApiError) {
        setError(err.message)
      } else {
        setError('Something went wrong. Please try again.')
      }
    }
  }

  // Post-submit confirmation state: the account exists but is unconfirmed.
  if (sentTo) {
    return (
      <AuthShell>
        <Card className="tela-auth-card w-full bg-[var(--surface-1)] shadow-[var(--shadow-lg)]">
          <CardHeader>
            <CardTitle className="text-[length:var(--text-2xl)]">
              Check your email
            </CardTitle>
            <CardDescription>
              We sent a confirmation link to{' '}
              <span className="text-[var(--text-primary)]">{sentTo}</span>.
              Follow it to activate your account.
            </CardDescription>
          </CardHeader>
          <CardBody className="flex flex-col gap-[var(--space-4)]">
            <Button
              type="button"
              variant="secondary"
              onClick={() => void resend.mutateAsync(sentTo).catch(() => {})}
              disabled={resend.isPending}
            >
              {resend.isPending
                ? 'Resending…'
                : resend.isSuccess
                  ? 'Sent again'
                  : 'Resend confirmation email'}
            </Button>
            <AuthFooterLink>
              Already confirmed?{' '}
              <Link
                to="/login"
                className="text-[var(--accent)] no-underline hover:underline"
              >
                Sign in
              </Link>
            </AuthFooterLink>
          </CardBody>
        </Card>
      </AuthShell>
    )
  }

  return (
    <AuthShell>
      <Card className="tela-auth-card w-full bg-[var(--surface-1)] shadow-[var(--shadow-lg)]">
        <CardHeader>
          <CardTitle className="text-[length:var(--text-2xl)]">
            Create your account
          </CardTitle>
          <CardDescription>
            Sign up to start writing in tela.
          </CardDescription>
        </CardHeader>
        <CardBody>
          <form
            onSubmit={handleSubmit}
            className="flex flex-col gap-[var(--space-4)]"
            noValidate
          >
            <AuthField id="register-email" label="Email">
              <Input
                id="register-email"
                type="email"
                autoFocus
                autoComplete="email"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                aria-invalid={error != null}
              />
            </AuthField>
            <AuthField id="register-username" label="Username">
              <Input
                id="register-username"
                autoComplete="username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                aria-invalid={error != null}
              />
            </AuthField>
            <AuthField id="register-password" label="Password">
              <Input
                id="register-password"
                type="password"
                autoComplete="new-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                aria-invalid={error != null}
              />
            </AuthField>
            <AuthField id="register-confirm" label="Confirm password">
              <Input
                id="register-confirm"
                type="password"
                autoComplete="new-password"
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
                aria-invalid={error != null}
              />
            </AuthField>
            {error ? (
              <p
                role="alert"
                className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]"
              >
                {error}
              </p>
            ) : null}
            <Button
              type="submit"
              variant="primary"
              size="lg"
              disabled={register.isPending}
            >
              {register.isPending ? 'Creating account…' : 'Create account'}
            </Button>
          </form>
          <AuthFooterLink>
            Already have an account?{' '}
            <Link
              to="/login"
              className="text-[var(--accent)] no-underline hover:underline"
            >
              Sign in
            </Link>
          </AuthFooterLink>
        </CardBody>
      </Card>
    </AuthShell>
  )
}
