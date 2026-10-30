import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate, useSearch } from '@tanstack/react-router'
import { useVerifyEmail } from '../lib/queries/auth'
import { Button } from '../components/ui/button'
import {
  Card,
  CardBody,
  CardDescription,
  CardHeader,
  CardTitle,
} from '../components/ui/card'
import { AuthShell } from '../components/app/AuthShell'
import { useBrand } from '../lib/queries/host-context'

type Phase = 'verifying' | 'done' | 'error' | 'missing'

export function VerifyEmailPage() {
  const brandName = useBrand().name
  const search = useSearch({ from: '/verify-email' }) as { token?: string }
  const verify = useVerifyEmail()
  const navigate = useNavigate()
  const [phase, setPhase] = useState<Phase>(
    search.token ? 'verifying' : 'missing',
  )
  // StrictMode mounts effects twice in dev; the ref guard keeps us from
  // burning the single-use token on the throwaway first mount.
  const ran = useRef(false)

  useEffect(() => {
    if (ran.current || !search.token) return
    ran.current = true
    verify
      .mutateAsync(search.token)
      .then(() => setPhase('done'))
      .catch(() => setPhase('error'))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [search.token])

  if (phase === 'done') {
    return (
      <AuthShell>
        <Card className="tela-auth-card w-full bg-[var(--surface-1)] shadow-[var(--shadow-lg)] text-center">
          <CardHeader className="items-center">
            <CardTitle className="text-[length:var(--text-2xl)]">
              Email confirmed
            </CardTitle>
            <CardDescription>
              Your account is active. Welcome to {brandName}.
            </CardDescription>
          </CardHeader>
          <CardBody>
            <Button
              variant="primary"
              size="lg"
              onClick={() => void navigate({ to: '/' })}
            >
              Go to {brandName}
            </Button>
          </CardBody>
        </Card>
      </AuthShell>
    )
  }

  if (phase === 'verifying') {
    return (
      <AuthShell>
        <Card className="tela-auth-card w-full bg-[var(--surface-1)] shadow-[var(--shadow-lg)] text-center">
          <CardHeader className="items-center">
            <CardTitle className="text-[length:var(--text-2xl)]">
              Confirming…
            </CardTitle>
            <CardDescription>Hang tight for a moment.</CardDescription>
          </CardHeader>
        </Card>
      </AuthShell>
    )
  }

  // error | missing
  return (
    <AuthShell>
      <Card className="tela-auth-card w-full bg-[var(--surface-1)] shadow-[var(--shadow-lg)] text-center">
        <CardHeader className="items-center">
          <CardTitle className="text-[length:var(--text-2xl)]">
            Link invalid or expired
          </CardTitle>
          <CardDescription>
            This confirmation link can't be used. Sign in to request a new one.
          </CardDescription>
        </CardHeader>
        <CardBody>
          <Button asChild variant="primary" size="lg">
            <Link to="/login">Back to sign in</Link>
          </Button>
        </CardBody>
      </Card>
    </AuthShell>
  )
}
