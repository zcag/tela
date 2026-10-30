import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import {
  useInstanceSettings,
  useUpdateInstanceSettings,
} from '../../lib/queries/instance-settings'
import { updateKeys, useCheckForUpdate, useUpdateStatus } from '../../lib/queries/updates'
import { authKeys } from '../../lib/queries/auth'
import { relativeTimeFromSqlite } from '../../lib/relativeTime'
import { Checkbox } from '../ui/checkbox'
import { Input } from '../ui/input'
import { Select } from '../ui/select'
import { Button } from '../ui/button'
import { BrandingEditor } from './BrandingEditor'
import { instanceBrandingBase } from '../../lib/queries/branding'

// Instance-admin runtime configuration, backed by the instance_settings store.
// Changes apply immediately with no redeploy.
export function SettingsInstanceTab() {
  const settings = useInstanceSettings()
  const update = useUpdateInstanceSettings()

  // Absent or "true" = open (the default for an open team wiki); only the
  // literal "false" closes registration.
  const registrationOpen = settings.data?.['registration_open'] !== 'false'
  const aiDisabled = settings.data?.['ai.disabled'] === '1'

  return (
    <section
      aria-labelledby="settings-instance"
      className="flex flex-col gap-[var(--space-5)]"
    >
      <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)] leading-[var(--leading-relaxed)]">
        Instance-wide settings. Changes apply immediately — no redeploy.
      </p>

      {settings.isLoading ? (
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">Loading…</p>
      ) : settings.isError ? (
        <p role="alert" className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]">
          Couldn’t load instance settings.
        </p>
      ) : (
        <>
          <UpdatesSection />

          <div className="flex flex-col rounded-[var(--radius-md)] border border-[var(--border-subtle)] overflow-hidden">
            <label className="flex items-start gap-[var(--space-3)] px-[var(--space-4)] py-[var(--space-3)] cursor-pointer">
              <Checkbox
                checked={registrationOpen}
                disabled={update.isPending}
                aria-label="Allow self-registration"
                onCheckedChange={(v) =>
                  update.mutate({ registration_open: v === true ? 'true' : 'false' })
                }
              />
              <span className="flex flex-col gap-[var(--space-1)]">
                <span className="text-[length:var(--text-sm)] text-[var(--text-primary)] font-medium">
                  Allow self-registration
                </span>
                <span className="text-[length:var(--text-xs)] text-[var(--text-muted)] leading-[var(--leading-relaxed)]">
                  When off, new users can’t sign up themselves; instance admins can still
                  create accounts directly.
                </span>
              </span>
            </label>

            <label className="flex items-start gap-[var(--space-3)] px-[var(--space-4)] py-[var(--space-3)] border-t border-[var(--border-subtle)] cursor-pointer">
              <Checkbox
                checked={aiDisabled}
                disabled={update.isPending}
                aria-label="Pause AI features"
                onCheckedChange={(v) =>
                  update.mutate({ 'ai.disabled': v === true ? '1' : '0' })
                }
              />
              <span className="flex flex-col gap-[var(--space-1)]">
                <span className="text-[length:var(--text-sm)] text-[var(--text-primary)] font-medium">
                  Pause AI features
                </span>
                <span className="text-[length:var(--text-xs)] text-[var(--text-muted)] leading-[var(--leading-relaxed)]">
                  Pauses everything that calls the AI backend — “ask your docs”, semantic
                  search, and background semantic indexing (backfilling). Ask shows a calm
                  “paused for maintenance” notice; full-text search keeps working. Pair it
                  with a banner below.
                </span>
              </span>
            </label>
          </div>

          <BrandingBlock />

          <MaintenanceNoticeEditor
            initialNotice={settings.data?.['maintenance.notice'] ?? ''}
            initialLevel={settings.data?.['maintenance.level'] === 'warning' ? 'warning' : 'info'}
            ready={settings.isSuccess}
            saving={update.isPending}
            onSave={(notice, level) =>
              update.mutate({ 'maintenance.notice': notice, 'maintenance.level': level })
            }
          />
        </>
      )}
    </section>
  )
}

// Self-host update check (backend update_check.go): where this instance stands
// against the latest release, and the switch that turns the daily check off.
// Hidden on the managed cloud and on dev builds, where there's nothing to compare.
function UpdatesSection() {
  const status = useUpdateStatus()
  const check = useCheckForUpdate()
  const update = useUpdateInstanceSettings()
  const qc = useQueryClient()
  const st = status.data
  if (!st?.applies) return null

  const toggle = (on: boolean) =>
    update.mutate(
      { 'updates.check': on ? 'on' : 'off' },
      {
        onSuccess: () => {
          void qc.invalidateQueries({ queryKey: updateKeys.status })
          void qc.invalidateQueries({ queryKey: authKeys.me() })
          if (on) check.mutate()
        },
      },
    )

  let line: React.ReactNode = `You're on tela v${st.current}.`
  if (st.enabled && st.update_available) {
    line = (
      <>
        <span className="text-[var(--text-primary)] font-medium">tela v{st.latest} is available.</span>{' '}
        You're on v{st.current}.{' '}
        {st.release_url ? (
          <a
            href={st.release_url}
            target="_blank"
            rel="noopener noreferrer"
            className="text-[var(--accent)] no-underline hover:underline"
          >
            Release notes and upgrade steps →
          </a>
        ) : null}
      </>
    )
  } else if (st.enabled && st.latest) {
    line = `You're on the latest version, v${st.current}.`
  }

  return (
    <div className="flex flex-col rounded-[var(--radius-md)] border border-[var(--border-subtle)] overflow-hidden">
      <div className="flex items-start gap-[var(--space-3)] px-[var(--space-4)] py-[var(--space-3)]">
        <span className="flex-1 min-w-0 flex flex-col gap-[var(--space-1)]">
          <span className="text-[length:var(--text-sm)] text-[var(--text-primary)] font-medium">Updates</span>
          <span className="text-[length:var(--text-sm)] text-[var(--text-muted)] leading-[var(--leading-relaxed)]">
            {line}
          </span>
          {st.enabled && (st.checked_at || st.error) ? (
            <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">
              {st.error
                ? `The last check failed (${st.error}).`
                : `Checked ${relativeTimeFromSqlite(st.checked_at!)}.`}
            </span>
          ) : null}
        </span>
        {st.enabled ? (
          <Button
            type="button"
            variant="secondary"
            size="sm"
            disabled={check.isPending}
            onClick={() => check.mutate()}
          >
            {check.isPending ? 'Checking…' : 'Check now'}
          </Button>
        ) : null}
      </div>
      <label
        className={
          'flex items-start gap-[var(--space-3)] px-[var(--space-4)] py-[var(--space-3)] border-t border-[var(--border-subtle)] ' +
          (st.env_locked ? 'cursor-not-allowed' : 'cursor-pointer')
        }
      >
        <Checkbox
          checked={st.enabled}
          disabled={st.env_locked || update.isPending}
          aria-label="Check for new versions"
          onCheckedChange={(v) => toggle(v === true)}
        />
        <span className="flex flex-col gap-[var(--space-1)]">
          <span className="text-[length:var(--text-sm)] text-[var(--text-primary)] font-medium">
            Check for new versions
          </span>
          <span className="text-[length:var(--text-xs)] text-[var(--text-muted)] leading-[var(--leading-relaxed)]">
            {st.env_locked
              ? 'Turned off by TELA_UPDATE_CHECK in the server environment.'
              : 'Once a day, asks telawiki.com for the latest tela release and tells instance admins when there is a newer one. It sends the version you run and nothing else; telawiki.com counts how many instances check in, without keeping IP addresses.'}
          </span>
        </span>
      </label>
    </div>
  )
}

// The instance's own name, logo and accent: what this wiki calls itself on its
// main address (an org's custom domain keeps the org's brand).
function BrandingBlock() {
  return (
    <div className="flex flex-col gap-[var(--space-3)] rounded-[var(--radius-md)] border border-[var(--border-subtle)] px-[var(--space-4)] py-[var(--space-3)]">
      <span className="text-[length:var(--text-sm)] text-[var(--text-primary)] font-medium">Branding</span>
      <BrandingEditor
        base={instanceBrandingBase}
        withName
        idPrefix="instance-brand"
        intro={
          <>
            Your wiki's name, logo and accent color, shown in the sidebar, sign-in screens, tab
            titles, link previews and emails, and as the favicon and loading screen. Leave a field
            blank to use the tela default. A small "Powered by tela" credit shows whenever a name or
            logo is set.
          </>
        }
      />
    </div>
  )
}

// The app-wide banner an admin sets — any message ("AI is paused for
// maintenance"). Saving an empty notice clears it. Local-edit + Save so it isn't
// written on every keystroke.
function MaintenanceNoticeEditor({
  initialNotice,
  initialLevel,
  ready,
  saving,
  onSave,
}: {
  initialNotice: string
  initialLevel: 'info' | 'warning'
  ready: boolean
  saving: boolean
  onSave: (notice: string, level: 'info' | 'warning') => void
}) {
  const [notice, setNotice] = useState(initialNotice)
  const [level, setLevel] = useState<'info' | 'warning'>(initialLevel)
  // Hydrate once from the loaded settings (adjust-state-when-a-prop-changes).
  const [hydrated, setHydrated] = useState(false)
  if (ready && !hydrated) {
    setNotice(initialNotice)
    setLevel(initialLevel)
    setHydrated(true)
  }

  const dirty = notice !== initialNotice || level !== initialLevel

  return (
    <div className="flex flex-col gap-[var(--space-3)] rounded-[var(--radius-md)] border border-[var(--border-subtle)] p-[var(--space-4)]">
      <div className="flex flex-col gap-[var(--space-1)]">
        <span className="text-[length:var(--text-sm)] text-[var(--text-primary)] font-medium">
          Maintenance banner
        </span>
        <span className="text-[length:var(--text-xs)] text-[var(--text-muted)] leading-[var(--leading-relaxed)]">
          A soft notice shown to everyone, top of the app + login. Leave empty to hide it.
        </span>
      </div>
      <div className="flex items-start gap-[var(--space-2)]">
        <div className="flex-1 min-w-0">
          <Input
            value={notice}
            onChange={(e) => setNotice(e.target.value)}
            placeholder="e.g. AI is temporarily unavailable for maintenance."
            aria-label="Maintenance notice"
          />
        </div>
        <Select
          value={level}
          onChange={(e) => setLevel(e.target.value as 'info' | 'warning')}
          aria-label="Notice level"
          className="w-[8rem] shrink-0"
        >
          <option value="info">Info</option>
          <option value="warning">Warning</option>
        </Select>
        <Button
          type="button"
          variant="secondary"
          disabled={saving || !dirty}
          onClick={() => onSave(notice.trim(), level)}
        >
          {saving ? 'Saving…' : 'Save'}
        </Button>
      </div>
    </div>
  )
}
