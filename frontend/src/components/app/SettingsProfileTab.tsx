import { useMemo, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { LogOut } from 'lucide-react'
import { ApiError } from '../../lib/api'
import { useDeleteMyAccount, useMe, useUpdateProfile } from '../../lib/queries/auth'
import {
  useChangePassword,
  useLogoutEverywhere,
  useRevokeSession,
  useSessions,
} from '../../lib/queries/sessions'
import {
  localDateFromSqlite,
  relativeTimeFromSqlite,
} from '../../lib/relativeTime'
import type { SessionRow } from '../../lib/types'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '../ui/dialog'
import { Input } from '../ui/input'
import { TextArea } from '../ui/textarea'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '../ui/tooltip'
import { cn } from '../../lib/utils'

const MIN_PASSWORD_LEN = 8

export function SettingsProfileTab() {
  return (
    <div className="flex flex-col gap-[var(--space-7)]">
      <AccountSection />
      <Separator />
      <DisplayNameSection />
      <Separator />
      <BioSection />
      <Separator />
      <ChangePasswordSection />
      <Separator />
      <SessionsSection />
      <Separator />
      <DeleteAccountSection />
    </div>
  )
}

function Separator() {
  return <div className="border-t border-[var(--border-subtle)]" />
}

function SectionHeader({
  title,
  description,
}: {
  title: string
  description?: string
}) {
  return (
    <header className="flex flex-col gap-[var(--space-1)]">
      <h2 className="m-0 font-[family-name:var(--font-sans)] text-[length:var(--text-lg)] leading-[var(--leading-tight)] text-[var(--text-primary)]">
        {title}
      </h2>
      {description ? (
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)] leading-[var(--leading-relaxed)]">
          {description}
        </p>
      ) : null}
    </header>
  )
}

function AccountSection() {
  const me = useMe()
  return (
    <section
      aria-labelledby="settings-account"
      className="flex flex-col gap-[var(--space-3)]"
    >
      <SectionHeader
        title="Account"
        description="The username you signed in with. Renames aren't supported yet."
      />
      <div className="flex flex-col gap-[var(--space-1)]" id="settings-account">
        <span className="text-[length:var(--text-xs)] uppercase tracking-wider text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
          Username
        </span>
        <span className="text-[length:var(--text-base)] text-[var(--text-primary)] font-[family-name:var(--font-sans)]">
          {me.data?.username ?? '—'}
        </span>
      </div>
    </section>
  )
}

const MAX_DISPLAY_NAME_LEN = 80

function DisplayNameSection() {
  const me = useMe()
  const updateProfile = useUpdateProfile()
  const saved = me.data?.display_name ?? ''
  const [value, setValue] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [success, setSuccess] = useState(false)
  // null = uninitialised → mirror the loaded value; once edited we own it.
  const name = value ?? saved
  const dirty = value != null && value.trim() !== saved

  async function handleSave() {
    setSuccess(false)
    setError(null)
    try {
      await updateProfile.mutateAsync({ display_name: name.trim() })
      setValue(null)
      setSuccess(true)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Something went wrong. Try again.')
    }
  }

  return (
    <section
      aria-labelledby="settings-display-name"
      className="flex flex-col gap-[var(--space-3)]"
    >
      <SectionHeader
        title="Display name"
        description="How tela addresses you (e.g. in the home greeting). Falls back to your username when blank."
      />
      <div className="flex flex-col gap-[var(--space-2)] max-w-[24rem]" id="settings-display-name">
        <Input
          aria-label="Display name"
          value={name}
          onChange={(e) => {
            setValue(e.target.value.slice(0, MAX_DISPLAY_NAME_LEN))
            setSuccess(false)
          }}
          placeholder={me.data?.username ?? 'Your name'}
        />
        {error ? (
          <p role="alert" className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]">
            {error}
          </p>
        ) : null}
        {success ? (
          <p role="status" className="m-0 text-[length:var(--text-sm)] text-[var(--success)]">
            Display name saved.
          </p>
        ) : null}
        <div className="flex">
          <Button
            type="button"
            variant="primary"
            size="sm"
            onClick={() => void handleSave()}
            disabled={!dirty || updateProfile.isPending}
          >
            {updateProfile.isPending ? 'Saving…' : 'Save name'}
          </Button>
        </div>
      </div>
    </section>
  )
}

const MAX_BIO_LEN = 280

function BioSection() {
  const me = useMe()
  const updateProfile = useUpdateProfile()
  const saved = me.data?.bio ?? ''
  const [value, setValue] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [success, setSuccess] = useState(false)
  // null = uninitialised → mirror the loaded value; once edited we own it.
  const bio = value ?? saved
  const dirty = value != null && value.trim() !== saved

  async function handleSave() {
    setSuccess(false)
    setError(null)
    try {
      await updateProfile.mutateAsync({ bio: bio.trim() })
      setValue(null)
      setSuccess(true)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Something went wrong. Try again.')
    }
  }

  return (
    <section
      aria-labelledby="settings-bio"
      className="flex flex-col gap-[var(--space-3)]"
    >
      <SectionHeader
        title="Bio"
        description="A short line about you, shown on your public profile at /u/your-handle."
      />
      <div className="flex flex-col gap-[var(--space-2)] max-w-[36rem]" id="settings-bio">
        <TextArea
          aria-label="Bio"
          value={bio}
          onChange={(e) => {
            setValue(e.target.value.slice(0, MAX_BIO_LEN))
            setSuccess(false)
          }}
          rows={3}
          font="sans"
          placeholder="Writer. Builder. Tending a small corner of the web."
        />
        <div className="flex items-center justify-between">
          <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">
            {me.data?.username ? (
              <a
                href={`/${me.data.username}`}
                target="_blank"
                rel="noopener noreferrer"
                className="text-[var(--accent)] no-underline hover:underline"
              >
                View your profile →
              </a>
            ) : null}
          </span>
          <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">
            {bio.length}/{MAX_BIO_LEN}
          </span>
        </div>
        {error ? (
          <p role="alert" className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]">
            {error}
          </p>
        ) : null}
        {success ? (
          <p role="status" className="m-0 text-[length:var(--text-sm)] text-[var(--success)]">
            Bio saved.
          </p>
        ) : null}
        <div className="flex">
          <Button
            type="button"
            variant="primary"
            size="sm"
            onClick={() => void handleSave()}
            disabled={!dirty || updateProfile.isPending}
          >
            {updateProfile.isPending ? 'Saving…' : 'Save bio'}
          </Button>
        </div>
      </div>
    </section>
  )
}

function ChangePasswordSection() {
  const [oldPw, setOldPw] = useState('')
  const [newPw, setNewPw] = useState('')
  const [confirmPw, setConfirmPw] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [success, setSuccess] = useState(false)
  const changePassword = useChangePassword()

  function reset() {
    setOldPw('')
    setNewPw('')
    setConfirmPw('')
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setSuccess(false)
    if (!oldPw || !newPw || !confirmPw) {
      setError('All fields are required.')
      return
    }
    if (newPw !== confirmPw) {
      setError('New password and confirmation do not match.')
      return
    }
    if (newPw.length < MIN_PASSWORD_LEN) {
      setError(`New password must be at least ${MIN_PASSWORD_LEN} characters.`)
      return
    }
    setError(null)
    try {
      await changePassword.mutateAsync({
        old_password: oldPw,
        new_password: newPw,
      })
      reset()
      setSuccess(true)
    } catch (err) {
      setSuccess(false)
      if (err instanceof ApiError && err.status === 401) {
        setError('Current password is incorrect.')
      } else if (err instanceof ApiError && err.code === 'bad_request') {
        setError(err.message)
      } else {
        setError('Something went wrong. Try again.')
      }
    }
  }

  return (
    <section
      aria-labelledby="settings-password"
      className="flex flex-col gap-[var(--space-3)]"
    >
      <SectionHeader
        title="Change password"
        description="After a successful change, other devices will be signed out."
      />
      <form
        id="settings-password"
        onSubmit={handleSubmit}
        className="flex flex-col gap-[var(--space-3)] max-w-[24rem]"
        noValidate
      >
        <PasswordField
          id="settings-current-password"
          label="Current password"
          autoComplete="current-password"
          value={oldPw}
          onChange={setOldPw}
          invalid={error != null}
        />
        <PasswordField
          id="settings-new-password"
          label="New password"
          autoComplete="new-password"
          value={newPw}
          onChange={setNewPw}
          invalid={error != null}
        />
        <PasswordField
          id="settings-confirm-password"
          label="Confirm new password"
          autoComplete="new-password"
          value={confirmPw}
          onChange={setConfirmPw}
          invalid={error != null}
        />
        {error ? (
          <p
            role="alert"
            className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]"
          >
            {error}
          </p>
        ) : null}
        {success ? (
          <p
            role="status"
            className={cn(
              'm-0 inline-flex items-center gap-[var(--space-2)]',
              'rounded-[var(--radius-sm)]',
              'bg-[var(--surface-2)] border border-[var(--border-subtle)]',
              'px-[var(--space-3)] py-[var(--space-2)]',
              'text-[length:var(--text-sm)] text-[var(--success)]',
            )}
          >
            Password changed. Other devices have been signed out.
          </p>
        ) : null}
        <div className="flex">
          <Button
            type="submit"
            variant="primary"
            disabled={changePassword.isPending}
          >
            {changePassword.isPending ? 'Changing…' : 'Change password'}
          </Button>
        </div>
      </form>
    </section>
  )
}

function PasswordField({
  id,
  label,
  value,
  onChange,
  autoComplete,
  invalid,
}: {
  id: string
  label: string
  value: string
  onChange: (next: string) => void
  autoComplete: string
  invalid?: boolean
}) {
  return (
    <div className="flex flex-col gap-[var(--space-2)]">
      <label
        htmlFor={id}
        className="text-[length:var(--text-sm)] text-[var(--text-muted)]"
      >
        {label}
      </label>
      <Input
        id={id}
        type="password"
        autoComplete={autoComplete}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        aria-invalid={invalid ? true : undefined}
      />
    </div>
  )
}

function SessionsSection() {
  const { data, isLoading, isError } = useSessions()
  const revoke = useRevokeSession()
  const [logoutOpen, setLogoutOpen] = useState(false)

  const sessions = useMemo(() => data ?? [], [data])
  const otherCount = sessions.filter((s) => !s.current).length

  return (
    <section
      aria-labelledby="settings-sessions"
      className="flex flex-col gap-[var(--space-3)]"
    >
      <SectionHeader
        title="Sessions"
        description="Devices currently signed in to your account. Sliding 30-day expiry."
      />
      <div id="settings-sessions">
        {isLoading ? (
          <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
            Loading sessions…
          </p>
        ) : isError ? (
          <p className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]">
            Couldn't load sessions.
          </p>
        ) : sessions.length === 0 ? (
          <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
            No sessions found.
          </p>
        ) : (
          <ul className="m-0 p-0 list-none flex flex-col gap-[var(--space-1)]">
            {sessions.map((s) => (
              <SessionRowItem
                key={s.id}
                row={s}
                onRevoke={() => revoke.mutate(s.id)}
                disableRevoke={s.current || revoke.isPending}
              />
            ))}
          </ul>
        )}
      </div>
      {otherCount > 0 ? (
        <div className="flex">
          <Button
            type="button"
            variant="danger"
            onClick={() => setLogoutOpen(true)}
          >
            Sign out of all other devices
          </Button>
        </div>
      ) : null}
      <LogoutEverywhereDialog
        open={logoutOpen}
        onOpenChange={setLogoutOpen}
      />
    </section>
  )
}

function SessionRowItem({
  row,
  onRevoke,
  disableRevoke,
}: {
  row: SessionRow
  onRevoke: () => void
  disableRevoke: boolean
}) {
  const device = describeUserAgent(row.user_agent)
  return (
    <li
      className={cn(
        'm-0 list-none',
        'flex items-center gap-[var(--space-3)]',
        'px-[var(--space-3)] py-[var(--space-3)]',
        'rounded-[var(--radius-sm)]',
        'border border-[var(--border-subtle)] bg-[var(--surface-1)]',
      )}
    >
      <div className="flex-1 min-w-0 flex flex-col gap-[2px]">
        <div className="flex items-center gap-[var(--space-2)] min-w-0">
          <span className="truncate text-[length:var(--text-sm)] text-[var(--text-primary)] font-medium font-[family-name:var(--font-sans)]">
            {device}
          </span>
          {row.current ? (
            <Badge variant="accent" aria-label="Current session">
              This device
            </Badge>
          ) : null}
        </div>
        <span className="text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
          Last seen {relativeTimeFromSqlite(row.last_seen_at)} · Created{' '}
          {localDateFromSqlite(row.created_at)}
        </span>
      </div>
      <RevokeButton
        disabled={disableRevoke}
        currentSession={row.current}
        onClick={onRevoke}
      />
    </li>
  )
}

function RevokeButton({
  disabled,
  currentSession,
  onClick,
}: {
  disabled: boolean
  currentSession: boolean
  onClick: () => void
}) {
  const button = (
    <Button
      type="button"
      variant="ghost"
      size="sm"
      onClick={onClick}
      disabled={disabled}
      aria-label={currentSession ? 'Sign out from this menu' : 'Revoke session'}
    >
      <LogOut width={14} height={14} />
      <span>Revoke</span>
    </Button>
  )
  if (!currentSession) return button
  // The button needs to stay focusable for the tooltip; wrap with a span so
  // the disabled <button> still gets pointer-events for the trigger.
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span tabIndex={0} className="inline-flex">
          {button}
        </span>
      </TooltipTrigger>
      <TooltipContent>Sign out from this menu</TooltipContent>
    </Tooltip>
  )
}

function LogoutEverywhereDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (next: boolean) => void
}) {
  const [error, setError] = useState<string | null>(null)
  const logoutEverywhere = useLogoutEverywhere()

  function handleClose(next: boolean) {
    if (!next) setError(null)
    onOpenChange(next)
  }

  async function handleConfirm() {
    setError(null)
    try {
      await logoutEverywhere.mutateAsync()
      handleClose(false)
    } catch (err) {
      setError(
        err instanceof ApiError
          ? err.message
          : 'Failed to sign out other devices.',
      )
    }
  }

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Sign out of all other devices?</DialogTitle>
          <DialogDescription>
            You'll need to log in again on every device except this one.
          </DialogDescription>
        </DialogHeader>
        {error ? (
          <p className="m-0 text-[length:var(--text-xs)] text-[var(--danger)]">
            {error}
          </p>
        ) : null}
        <DialogFooter>
          <DialogClose asChild>
            <Button type="button" variant="ghost">
              Cancel
            </Button>
          </DialogClose>
          <Button
            type="button"
            variant="danger"
            onClick={handleConfirm}
            disabled={logoutEverywhere.isPending}
          >
            {logoutEverywhere.isPending
              ? 'Signing out…'
              : 'Sign out everywhere'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// Best-effort browser+OS extraction from a UA string. Not a full parser —
// covers the common desktop/mobile chrome/firefox/safari/edge variants and
// falls back to a truncated raw string when nothing matches. Keep this
// small; if it grows we should grab a real UA parser instead.
function describeUserAgent(ua: string): string {
  if (!ua || ua.trim().length === 0) return 'Unknown device'
  const browserMatch = ua.match(/(Edg|Chrome|Firefox|Safari)[\\/ ](\d+)/)
  // Safari/<version> also shows in Chrome UAs, so prefer the more specific
  // engine names first by checking Edg/Chrome before Safari.
  const browser = browserMatch ? browserMatch[1].replace('Edg', 'Edge') : null
  const osMatch = ua.match(
    /(Windows NT|Mac OS X|Linux|Android|iPhone OS|CrOS)[ ;)/]*([\d._]+)?/,
  )
  let os: string | null = null
  if (osMatch) {
    const raw = osMatch[1]
    if (raw === 'Windows NT') os = 'Windows'
    else if (raw === 'Mac OS X') os = 'macOS'
    else if (raw === 'iPhone OS') os = 'iOS'
    else if (raw === 'CrOS') os = 'ChromeOS'
    else os = raw
  }
  if (browser && os) return `${browser} · ${os}`
  if (browser) return browser
  if (os) return os
  return ua.length > 40 ? ua.slice(0, 40) + '…' : ua
}

function DeleteAccountSection() {
  const [open, setOpen] = useState(false)
  return (
    <section
      aria-labelledby="settings-delete-account"
      className="flex flex-col gap-[var(--space-3)]"
    >
      <SectionHeader
        title="Delete account"
        description="Permanently delete your account and remove you from all spaces."
      />
      <div id="settings-delete-account">
        <Button
          type="button"
          variant="danger"
          onClick={() => setOpen(true)}
        >
          Delete account
        </Button>
      </div>
      <DeleteAccountDialog open={open} onOpenChange={setOpen} />
    </section>
  )
}

function DeleteAccountDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (next: boolean) => void
}) {
  const [error, setError] = useState<string | null>(null)
  const deleteAccount = useDeleteMyAccount()
  const navigate = useNavigate()

  function handleClose(next: boolean) {
    if (!next) setError(null)
    onOpenChange(next)
  }

  async function handleConfirm() {
    setError(null)
    try {
      await deleteAccount.mutateAsync()
      void navigate({ to: '/' })
    } catch (err) {
      if (err instanceof ApiError && err.code === 'org_owner') {
        setError(err.message)
      } else {
        setError('Something went wrong. Try again.')
      }
    }
  }

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Delete your account?</DialogTitle>
          <DialogDescription>
            This will permanently delete your account and remove you from all spaces. This cannot be undone.
          </DialogDescription>
        </DialogHeader>
        {error ? (
          <p className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]">
            {error}
          </p>
        ) : null}
        <DialogFooter>
          <DialogClose asChild>
            <Button type="button" variant="ghost">
              Cancel
            </Button>
          </DialogClose>
          <Button
            type="button"
            variant="danger"
            onClick={handleConfirm}
            disabled={deleteAccount.isPending}
          >
            {deleteAccount.isPending ? 'Deleting…' : 'Delete account'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
