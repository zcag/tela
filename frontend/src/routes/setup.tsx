import { useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { ApiError } from '../lib/api'
import { useSetup } from '../lib/queries/auth'
import { Button } from '../components/ui/button'
import {
  Card,
  CardBody,
  CardDescription,
  CardHeader,
  CardTitle,
} from '../components/ui/card'
import { Input } from '../components/ui/input'
import { AuthShell, AuthField } from '../components/app/AuthShell'
import { useBrand } from '../lib/queries/host-context'

// First-run setup wizard. Mirrors register.tsx's owned-component styling. The
// route's beforeLoad has already decided this instance needs setup (no users
// yet); this screen creates the first admin and lands straight in the app.
export function SetupPage() {
  const brandName = useBrand().name
  const navigate = useNavigate()
  const [username, setUsername] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const setup = useSetup()

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    const un = username.trim()
    const em = email.trim()
    if (!un || !em || !password) {
      setError('Username, email and password are all required.')
      return
    }
    if (password.length < 8) {
      setError('Password must be at least 8 characters.')
      return
    }
    setError(null)
    try {
      await setup.mutateAsync({ username: un, email: em, password })
      // The backend signed us in; land on the dashboard.
      void navigate({ to: '/' })
    } catch (err) {
      if (err instanceof ApiError && err.code === 'already_setup') {
        // Someone (or another tab) already finished setup — send to login.
        void navigate({ to: '/login' })
      } else if (err instanceof ApiError) {
        setError(err.message)
      } else {
        setError('Something went wrong. Please try again.')
      }
    }
  }

  return (
    <AuthShell>
      <Card className="tela-auth-card w-full bg-[var(--surface-1)] shadow-[var(--shadow-lg)]">
        <CardHeader>
          <CardTitle className="text-[length:var(--text-2xl)]">
            Welcome to {brandName}
          </CardTitle>
          <CardDescription>
            Create the first admin account to set up this instance.
          </CardDescription>
        </CardHeader>
        <CardBody>
          <form
            onSubmit={handleSubmit}
            className="flex flex-col gap-[var(--space-4)]"
            noValidate
          >
            <AuthField id="setup-username" label="Username">
              <Input
                id="setup-username"
                autoFocus
                autoComplete="username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                aria-invalid={error != null}
              />
            </AuthField>
            <AuthField id="setup-email" label="Email">
              <Input
                id="setup-email"
                type="email"
                autoComplete="email"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                aria-invalid={error != null}
              />
            </AuthField>
            <AuthField id="setup-password" label="Password">
              <Input
                id="setup-password"
                type="password"
                autoComplete="new-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
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
              disabled={setup.isPending}
            >
              {setup.isPending ? 'Creating admin…' : 'Create admin account'}
            </Button>
          </form>
        </CardBody>
      </Card>
    </AuthShell>
  )
}
