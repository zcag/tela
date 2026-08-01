import { useState } from 'react'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { useInvite, useAcceptInvite } from '../lib/queries/invites'
import { useMe } from '../lib/queries/auth'
import { ApiError } from '../lib/api'
import { Button } from '../components/ui/button'
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from '../components/ui/card'
import { AuthShell } from '../components/app/AuthShell'

function Shell({
  title,
  description,
  children,
}: {
  title: string
  description?: string
  children?: React.ReactNode
}) {
  return (
    <AuthShell>
      <Card className="tela-auth-card w-full bg-[var(--surface-1)] shadow-[var(--shadow-lg)] text-center">
        <CardHeader className="items-center">
          <CardTitle className="text-[length:var(--text-2xl)]">{title}</CardTitle>
          {description ? <CardDescription>{description}</CardDescription> : null}
        </CardHeader>
        {children ? <CardBody>{children}</CardBody> : null}
      </Card>
    </AuthShell>
  )
}

// One page for both invite kinds — an organization (self-serve teams) and a
// single space (share-by-email). The backend's `kind` discriminator decides the
// wording; the flows (accept while signed in, sign up and auto-join on verify,
// wrong-account) are identical.
export function InvitePage() {
  const { token } = useParams({ from: '/invite/$token' })
  const invite = useInvite(token)
  const me = useMe()
  const accept = useAcceptInvite()
  const navigate = useNavigate()
  const [error, setError] = useState<string | null>(null)

  if (invite.isPending || me.isPending) {
    return <Shell title="Loading invitation…" description="One moment." />
  }
  const data = invite.data
  if (!data || !data.valid) {
    return (
      <Shell
        title="Invitation invalid or expired"
        description="This invitation can't be used. Ask the team to send a fresh one."
      >
        <Button asChild variant="primary" size="lg">
          <Link to="/login">Go to tela</Link>
        </Button>
      </Shell>
    )
  }

  const isSpace = data.kind === 'space'
  // What you're joining, and the noun for it.
  const target = (isSpace ? data.space_name : data.org_name) ?? (isSpace ? 'a space' : 'the team')
  const what = isSpace ? `the "${target}" space` : target
  const invitedBy = data.inviter
    ? `${data.inviter} invited you to ${what} on tela.`
    : `You've been invited to ${what} on tela.`
  const invEmail = (data.email ?? '').toLowerCase()
  const user = me.data
  const myEmail = (user?.email ?? '').toLowerCase()

  async function onAccept() {
    setError(null)
    try {
      await accept.mutateAsync(token)
      void navigate({ to: '/' })
    } catch (e) {
      setError(e instanceof ApiError ? e.message : 'Could not accept the invitation.')
    }
  }

  // Signed in with the invited address → one-click join.
  if (user && myEmail === invEmail) {
    return (
      <Shell title={`Join ${target}`} description={invitedBy}>
        <div className="flex flex-col items-center gap-[var(--space-3)]">
          <Button variant="primary" size="lg" disabled={accept.isPending} onClick={onAccept}>
            {accept.isPending ? 'Joining…' : `Join ${target}`}
          </Button>
          {error ? (
            <p className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]">{error}</p>
          ) : null}
        </div>
      </Shell>
    )
  }

  // Signed in as someone else.
  if (user) {
    return (
      <Shell
        title={`Invitation for ${data.email}`}
        description={`This invite is for ${data.email}, but you're signed in as ${user.email ?? user.username}. Sign in with ${data.email} to accept it.`}
      >
        <Button asChild variant="secondary" size="lg">
          <Link to="/login" search={{ next: `/invite/${token}` }}>
            Switch account
          </Link>
        </Button>
      </Shell>
    )
  }

  // Logged out — sign up (auto-joins on email verify) or sign in.
  return (
    <Shell
      title={`Join ${target} on tela`}
      description={`${invitedBy} Create an account with ${data.email} or sign in to accept.`}
    >
      <div className="flex flex-col items-stretch gap-[var(--space-2)]">
        <Button asChild variant="primary" size="lg">
          <Link to="/register" search={{ email: data.email }}>
            Create your account
          </Link>
        </Button>
        <Button asChild variant="secondary">
          <Link to="/login" search={{ next: `/invite/${token}` }}>
            I already have an account
          </Link>
        </Button>
      </div>
    </Shell>
  )
}
