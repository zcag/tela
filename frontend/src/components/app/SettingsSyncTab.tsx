import { useState } from 'react'
import { Copy, Folder, Link2, ShieldAlert } from 'lucide-react'
import { ApiError } from '../../lib/api'
import {
  useCreateSyncConnection,
  useRevokeSyncConnection,
  useSyncConnections,
  type CreateSyncConnectionInput,
  type RcloneSetup,
  type SyncConnectionCreated,
} from '../../lib/queries/sync-connections'
import { useSpaces } from '../../lib/queries/spaces'
import { localDateFromSqlite, relativeTimeFromSqlite } from '../../lib/relativeTime'
import type { ApiKeyRow } from '../../lib/types'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '../ui/dialog'
import { Input } from '../ui/input'
import { Select } from '../ui/select'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '../ui/tabs'
import { ToggleGroup, ToggleGroupItem } from '../ui/toggle'
import { cn } from '../../lib/utils'
import { DOCS } from '../../lib/docs'

const NAME_MAX_LEN = 64
const ALL_SPACES_VALUE = '__all__'
const COPIED_FLASH_MS = 1200

export function SettingsSyncTab() {
  const connections = useSyncConnections()
  const [created, setCreated] = useState<SyncConnectionCreated | null>(null)

  return (
    <section
      aria-labelledby="settings-sync"
      className="flex flex-col gap-[var(--space-5)]"
    >
      <header className="flex flex-col gap-[var(--space-1)]" id="settings-sync">
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)] leading-[var(--leading-relaxed)]">
          Mount your spaces as a local folder with{' '}
          <a
            href="https://rclone.org"
            target="_blank"
            rel="noreferrer"
            className="text-[var(--accent)] underline underline-offset-2"
          >
            rclone
          </a>{' '}
          and edit them as plain markdown in any app. Connect a vault below — it
          mints a sync token and walks you through the setup. Edits merge on the
          server, so editing in the app and on disk at once is safe.
        </p>
        <p className="m-0 text-[length:var(--text-xs)] text-[var(--text-muted)]">
          <a
            href={DOCS.webdav}
            target="_blank"
            rel="noreferrer"
            className="text-[var(--accent)] underline underline-offset-2"
          >
            WebDAV &amp; rclone setup guide →
          </a>
        </p>
      </header>

      <ConnectForm onCreated={setCreated} />

      <ConnectionsList
        connections={connections.data ?? []}
        loading={connections.isLoading}
        isError={connections.isError}
      />

      <SetupDialog
        created={created}
        onOpenChange={(open) => {
          if (!open) setCreated(null)
        }}
      />
    </section>
  )
}

interface ConnectFormProps {
  onCreated: (created: SyncConnectionCreated) => void
}

function ConnectForm({ onCreated }: ConnectFormProps) {
  const spaces = useSpaces()
  const create = useCreateSyncConnection()
  const [name, setName] = useState('')
  const [spaceValue, setSpaceValue] = useState<string>(ALL_SPACES_VALUE)
  const [mode, setMode] = useState<'two-way' | 'read-only'>('two-way')
  const [error, setError] = useState<string | null>(null)

  function resetForm() {
    setName('')
    setSpaceValue(ALL_SPACES_VALUE)
    setMode('two-way')
    setError(null)
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    const trimmed = name.trim()
    if (!trimmed) {
      setError('Give this connection a name (e.g. the device).')
      return
    }
    if (trimmed.length > NAME_MAX_LEN) {
      setError(`Name must be at most ${NAME_MAX_LEN} characters.`)
      return
    }
    setError(null)
    const input: CreateSyncConnectionInput = {
      name: trimmed,
      space_id: spaceValue === ALL_SPACES_VALUE ? null : Number(spaceValue),
      read_only: mode === 'read-only',
    }
    try {
      onCreated(await create.mutateAsync(input))
      resetForm()
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message)
        return
      }
      setError('Failed to connect. Try again.')
    }
  }

  return (
    <section
      aria-label="Connect a vault"
      className={cn(
        'rounded-[var(--radius-md)] border border-[var(--border-subtle)]',
        'bg-[var(--surface-1)]',
        'px-[var(--space-4)] py-[var(--space-4)]',
      )}
    >
      <h2 className="m-0 mb-[var(--space-3)] font-[family-name:var(--font-sans)] text-[length:var(--text-base)] leading-[var(--leading-tight)] text-[var(--text-primary)]">
        Connect a vault
      </h2>
      <form
        onSubmit={handleSubmit}
        className="flex flex-col gap-[var(--space-4)]"
        noValidate
      >
        <Field label="Connection name" htmlFor="sync-name">
          <Input
            id="sync-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. laptop, work-desktop"
            maxLength={NAME_MAX_LEN}
            autoComplete="off"
          />
        </Field>

        <Field label="What to sync" htmlFor="sync-space">
          {spaces.isLoading ? (
            <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
              Loading spaces…
            </p>
          ) : spaces.isError || !spaces.data ? (
            <p
              role="alert"
              className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]"
            >
              Couldn't load spaces.
            </p>
          ) : (
            <Select
              id="sync-space"
              value={spaceValue}
              onChange={(e) => setSpaceValue(e.target.value)}
            >
              <option value={ALL_SPACES_VALUE}>
                Whole workspace (all spaces)
              </option>
              {spaces.data.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name}
                </option>
              ))}
            </Select>
          )}
        </Field>

        <Field label="Direction">
          <ToggleGroup
            type="single"
            value={mode}
            onValueChange={(next) => {
              if (next === 'two-way' || next === 'read-only') setMode(next)
            }}
            aria-label="Sync direction"
          >
            <ToggleGroupItem value="two-way">Two-way</ToggleGroupItem>
            <ToggleGroupItem value="read-only">Read-only</ToggleGroupItem>
          </ToggleGroup>
          <p className="m-0 text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
            {mode === 'two-way'
              ? 'Edit on disk and in the app; changes merge both ways.'
              : 'Pull a local mirror only — local edits are not pushed back.'}
          </p>
        </Field>

        {error ? (
          <p
            role="alert"
            className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]"
          >
            {error}
          </p>
        ) : null}

        <div className="flex">
          <Button type="submit" variant="primary" disabled={create.isPending}>
            <Link2 width={14} height={14} />
            <span>{create.isPending ? 'Connecting…' : 'Connect'}</span>
          </Button>
        </div>
      </form>
    </section>
  )
}

interface ConnectionsListProps {
  connections: ApiKeyRow[]
  loading: boolean
  isError: boolean
}

function ConnectionsList({ connections, loading, isError }: ConnectionsListProps) {
  if (loading) {
    return (
      <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
        Loading connections…
      </p>
    )
  }
  if (isError) {
    return (
      <p
        role="alert"
        className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]"
      >
        Couldn't load connections.
      </p>
    )
  }
  const active = connections.filter((c) => !c.revoked_at)
  if (active.length === 0) {
    return (
      <p
        className={cn(
          'm-0 rounded-[var(--radius-md)] border border-dashed border-[var(--border-subtle)]',
          'px-[var(--space-4)] py-[var(--space-5)]',
          'text-[length:var(--text-sm)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]',
          'text-center',
        )}
      >
        No connected vaults yet. Connect one above.
      </p>
    )
  }
  return (
    <ul className="m-0 p-0 list-none flex flex-col gap-[var(--space-2)]">
      {active.map((c) => (
        <ConnectionRow key={c.id} row={c} />
      ))}
    </ul>
  )
}

function ConnectionRow({ row }: { row: ApiKeyRow }) {
  const spaces = useSpaces()
  const revoke = useRevokeSyncConnection()
  const [confirming, setConfirming] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const spaceLabel =
    row.space_id == null
      ? 'Whole workspace'
      : (spaces.data?.find((s) => s.id === row.space_id)?.name ??
        `Space #${row.space_id}`)
  const lastUsedLabel =
    row.last_used_at != null ? relativeTimeFromSqlite(row.last_used_at) : 'never'

  async function handleRevoke() {
    setError(null)
    try {
      await revoke.mutateAsync(row.id)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Failed to disconnect.')
      setConfirming(false)
    }
  }

  return (
    <li
      className={cn(
        'm-0 list-none flex flex-col gap-[var(--space-2)]',
        'px-[var(--space-3)] py-[var(--space-3)] rounded-[var(--radius-sm)]',
        'border border-[var(--border-subtle)] bg-[var(--surface-1)]',
      )}
    >
      <div className="flex items-start gap-[var(--space-3)]">
        <div className="flex-1 min-w-0 flex flex-col gap-[2px]">
          <div className="flex items-center gap-[var(--space-2)] min-w-0 flex-wrap">
            <span className="truncate text-[length:var(--text-sm)] text-[var(--text-primary)] font-medium font-[family-name:var(--font-sans)]">
              {row.name}
            </span>
            <Badge variant="muted">
              {row.scope === 'read' ? 'Read-only' : 'Two-way'}
            </Badge>
            <Badge variant="muted">{spaceLabel}</Badge>
          </div>
          <span className="text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
            Last synced {lastUsedLabel} · Connected{' '}
            {localDateFromSqlite(row.created_at)}
          </span>
        </div>
        {!confirming ? (
          <Button
            type="button"
            variant="ghost"
            size="sm"
            onClick={() => {
              setError(null)
              setConfirming(true)
            }}
            aria-label={`Disconnect ${row.name}`}
            disabled={revoke.isPending}
          >
            Disconnect
          </Button>
        ) : null}
      </div>

      {confirming ? (
        <div
          className={cn(
            'flex items-center justify-between gap-[var(--space-2)]',
            'rounded-[var(--radius-sm)] bg-[var(--surface-2)]',
            'px-[var(--space-2)] py-[var(--space-2)]',
          )}
        >
          <span className="text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
            Disconnect <strong>{row.name}</strong>? Its token stops working
            immediately; your local files stay put.
          </span>
          <div className="flex items-center gap-[var(--space-1)] shrink-0">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => setConfirming(false)}
              disabled={revoke.isPending}
            >
              Cancel
            </Button>
            <Button
              type="button"
              variant="danger"
              size="sm"
              onClick={() => void handleRevoke()}
              disabled={revoke.isPending}
            >
              {revoke.isPending ? 'Disconnecting…' : 'Disconnect'}
            </Button>
          </div>
        </div>
      ) : null}

      {error ? (
        <p
          role="alert"
          className="m-0 text-[length:var(--text-xs)] text-[var(--danger)]"
        >
          {error}
        </p>
      ) : null}
    </li>
  )
}

interface SetupDialogProps {
  created: SyncConnectionCreated | null
  onOpenChange: (open: boolean) => void
}

type VaultOS = 'linux' | 'macos'

// MountSteps is steps 2–3 for one OS: macOS has no FUSE (macFUSE is a kext
// needing Reduced Security + a reboot), so it mounts over NFS and keeps the
// mount alive with a launchd agent instead of a systemd user unit.
function MountSteps({
  rclone,
  macos,
}: {
  rclone: RcloneSetup
  macos: boolean
}) {
  return (
    <>
      <Step n={2} title="Mount it (try it now)">
        <p className="m-0 text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-sans)] leading-[var(--leading-relaxed)]">
          Your vault shows up at{' '}
          <code className="font-[family-name:var(--font-mono)]">
            {rclone.local_dir}
          </code>
          . Edit files with any app — changes sync both ways
          {rclone.read_only ? ' (read-only)' : ''}. Press Ctrl-C to unmount.
          {macos ? ' No macFUSE needed — this mounts over NFS.' : ''}
        </p>
        <CommandBlock
          command={macos ? rclone.mount_command_macos : rclone.mount_command}
        />
      </Step>

      <Step
        n={3}
        title={
          macos
            ? 'Keep it mounted (macOS · launchd)'
            : 'Keep it mounted (Linux · systemd)'
        }
      >
        <p className="m-0 text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-sans)] leading-[var(--leading-relaxed)]">
          So the vault mounts on login and restarts if it drops. Save this as{' '}
          <code className="font-[family-name:var(--font-mono)]">
            {macos
              ? `~/Library/LaunchAgents/${rclone.launchd_label}.plist`
              : `~/.config/systemd/user/${rclone.service_name}.service`}
          </code>
          :
        </p>
        <CommandBlock
          command={macos ? rclone.launchd_plist : rclone.systemd_unit}
        />
        <p className="m-0 text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
          Then turn it on:
        </p>
        <CommandBlock
          command={macos ? rclone.launchd_install : rclone.systemd_install}
        />
        <p className="m-0 text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-sans)] leading-[var(--leading-relaxed)]">
          {macos ? (
            <>
              Check it with{' '}
              <code className="font-[family-name:var(--font-mono)]">
                launchctl print gui/$(id -u)/{rclone.launchd_label}
              </code>
              .
            </>
          ) : (
            <>
              Check it with{' '}
              <code className="font-[family-name:var(--font-mono)]">
                systemctl --user status {rclone.service_name}
              </code>
              .
            </>
          )}{' '}
          On Windows, or to keep real local files instead of a live mount, see
          the sync docs.
        </p>
      </Step>
    </>
  )
}

function SetupDialog({ created, onOpenChange }: SetupDialogProps) {
  const rclone = created?.rclone
  // Default to the platform the browser is on — the commands differ per OS and
  // the right one should be the one already on screen.
  const [os, setOs] = useState<VaultOS>(() =>
    /mac/i.test(navigator.userAgent) ? 'macos' : 'linux',
  )
  return (
    <Dialog open={created != null} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Vault connected — mount it as a folder</DialogTitle>
          <DialogDescription>
            rclone presents tela as a normal folder you open with any app; edits
            sync automatically. Paste each command into a terminal, in order.
            (You'll need rclone installed.)
          </DialogDescription>
        </DialogHeader>
        {rclone ? (
          <div className="flex flex-col gap-[var(--space-4)] max-h-[60vh] overflow-y-auto">
            <div
              className={cn(
                'flex items-center gap-[var(--space-2)] rounded-[var(--radius-sm)]',
                'border border-[var(--border-subtle)] bg-[var(--surface-2)]',
                'px-[var(--space-3)] py-[var(--space-2)]',
              )}
            >
              <Folder
                aria-hidden
                width={15}
                height={15}
                className="shrink-0 text-[var(--text-muted)]"
              />
              <span className="text-[length:var(--text-xs)] text-[var(--text-primary)] font-[family-name:var(--font-sans)]">
                Your vault appears at{' '}
                <code className="font-[family-name:var(--font-mono)]">
                  {rclone.local_dir}
                </code>{' '}
                — open it with any editor.
              </span>
            </div>

            <Step n={1} title="Link tela to rclone">
              <p className="m-0 text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
                Run once on this device — creates the connection.
              </p>
              <CommandBlock command={rclone.config_create_command} />
              <div
                className={cn(
                  'flex items-start gap-[var(--space-2)] rounded-[var(--radius-sm)]',
                  'border border-[var(--accent)] bg-[var(--surface-1)]',
                  'px-[var(--space-2)] py-[var(--space-1)]',
                )}
              >
                <ShieldAlert
                  aria-hidden
                  width={14}
                  height={14}
                  className="mt-[2px] shrink-0 text-[var(--accent)]"
                />
                <span className="text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-sans)] leading-[var(--leading-relaxed)]">
                  This line contains your access token — treat it like a
                  password.
                </span>
              </div>
              <p className="m-0 text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-sans)] leading-[var(--leading-relaxed)]">
                Connected a vault on another machine before September 2026? Run
                this there too — older setups used a client profile that can't
                report file times, so rclone silently skipped your edits:
              </p>
              <CommandBlock command={rclone.fix_vendor_command} />
            </Step>

            <Tabs
              value={os}
              onValueChange={(v) => setOs(v as VaultOS)}
              className="flex flex-col gap-[var(--space-4)]"
            >
              <TabsList>
                <TabsTrigger value="linux">Linux</TabsTrigger>
                <TabsTrigger value="macos">macOS</TabsTrigger>
              </TabsList>
              <TabsContent
                value="linux"
                className="flex flex-col gap-[var(--space-4)]"
              >
                <MountSteps rclone={rclone} macos={false} />
              </TabsContent>
              <TabsContent
                value="macos"
                className="flex flex-col gap-[var(--space-4)]"
              >
                <MountSteps rclone={rclone} macos />
              </TabsContent>
            </Tabs>

            <DialogFooter>
              <Button
                type="button"
                variant="primary"
                onClick={() => onOpenChange(false)}
              >
                Done
              </Button>
            </DialogFooter>
          </div>
        ) : null}
      </DialogContent>
    </Dialog>
  )
}

function Step({
  n,
  title,
  children,
}: {
  n: number
  title: string
  children: React.ReactNode
}) {
  return (
    <div className="flex flex-col gap-[var(--space-2)]">
      <div className="flex items-center gap-[var(--space-2)]">
        <span
          aria-hidden
          className={cn(
            'shrink-0 inline-flex items-center justify-center rounded-full',
            'h-[var(--space-5)] w-[var(--space-5)]',
            'bg-[var(--surface-3)] text-[var(--text-primary)]',
            'text-[length:var(--text-xs)] font-medium',
          )}
        >
          {n}
        </span>
        <span className="text-[length:var(--text-sm)] font-medium text-[var(--text-primary)] font-[family-name:var(--font-sans)]">
          {title}
        </span>
      </div>
      <div className="flex flex-col gap-[var(--space-2)] pl-[var(--space-6)]">
        {children}
      </div>
    </div>
  )
}

function CommandBlock({ command, label }: { command: string; label?: string }) {
  const [copied, setCopied] = useState(false)
  async function copy() {
    try {
      if (!navigator.clipboard?.writeText) return
      await navigator.clipboard.writeText(command)
      setCopied(true)
      window.setTimeout(() => setCopied(false), COPIED_FLASH_MS)
    } catch {
      // Best-effort — the user can select the text manually.
    }
  }
  return (
    <div className="flex flex-col gap-[var(--space-1)]">
      <div className="flex items-center justify-between gap-[var(--space-2)]">
        {label ? (
          <span className="text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
            {label}
          </span>
        ) : (
          <span />
        )}
        <Button type="button" variant="ghost" size="sm" onClick={() => void copy()}>
          <Copy width={13} height={13} />
          <span>{copied ? 'Copied!' : 'Copy'}</span>
        </Button>
      </div>
      <pre className="m-0 overflow-x-auto rounded-[var(--radius-sm)] border border-[var(--border-subtle)] bg-[var(--surface-2)] px-[var(--space-3)] py-[var(--space-2)] text-[length:var(--text-xs)] text-[var(--text-primary)] font-[family-name:var(--font-mono)] leading-[var(--leading-relaxed)] whitespace-pre-wrap break-all">
        {command}
      </pre>
    </div>
  )
}

interface FieldProps {
  label: string
  htmlFor?: string
  children: React.ReactNode
}

function Field({ label, htmlFor, children }: FieldProps) {
  return (
    <div className="flex flex-col gap-[var(--space-2)]">
      <label
        htmlFor={htmlFor}
        className="text-[length:var(--text-sm)] font-medium text-[var(--text-primary)] font-[family-name:var(--font-sans)]"
      >
        {label}
      </label>
      {children}
    </div>
  )
}
