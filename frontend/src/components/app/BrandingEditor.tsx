import { useRef, useState, type ReactNode } from 'react'
import { Globe, Palette, Trash2, Upload } from 'lucide-react'
import { ApiError } from '../../lib/api'
import { useBranding, useDeleteLogo, usePutBranding, useUploadLogo } from '../../lib/queries/branding'
import { Button } from '../ui/button'
import { Input } from '../ui/input'

// BrandingEditor edits a logo (stored in tela), an accent color and, for the
// instance, a name, against one branding endpoint (an org's or the instance's).
// Surfaces the backend's 400 validation. The accent input shows a live swatch
// using the entered value as an inline background: runtime user data, not a
// hardcoded token.
export function BrandingEditor({
  base,
  intro,
  withName = false,
  idPrefix,
}: {
  base: string
  intro: ReactNode
  withName?: boolean
  idPrefix: string
}) {
  const branding = useBranding(base)
  const put = usePutBranding(base)
  const uploadLogo = useUploadLogo(base)
  const deleteLogo = useDeleteLogo(base)
  const fileRef = useRef<HTMLInputElement>(null)
  const [name, setName] = useState('')
  const [accent, setAccent] = useState('')
  const [importUrl, setImportUrl] = useState('')
  const [showImport, setShowImport] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)
  // Prefill the editable color/style fields from the loaded branding once.
  const [hydratedFor, setHydratedFor] = useState<string | null>(null)
  if (branding.data && hydratedFor !== base) {
    setName(branding.data.name ?? '')
    setAccent(branding.data.accent)
    setHydratedFor(base)
  }
  const fields = () => (withName ? { name: name.trim(), accent: accent.trim() } : { accent: accent.trim() })

  const hasLogo = branding.data?.has_logo ?? false
  const logoUrl = branding.data?.logo_url ?? ''

  // The accent is saved via the form. The logo is its own immediate action
  // (upload / import / remove), each storing to tela + refetching.
  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError(null)
    setSaved(false)
    try {
      await put.mutateAsync(fields())
      setSaved(true)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Failed to save branding.')
    }
  }

  async function onPickFile(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0]
    e.target.value = '' // allow re-picking the same file
    if (!file) return
    setError(null)
    try {
      await uploadLogo.mutateAsync(file)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Logo upload failed.')
    }
  }

  async function doImport() {
    if (!importUrl.trim()) return
    setError(null)
    try {
      await put.mutateAsync({ ...fields(), logo_import_url: importUrl.trim() })
      setImportUrl('')
      setShowImport(false)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Logo import failed.')
    }
  }

  async function removeLogo() {
    setError(null)
    try {
      await deleteLogo.mutateAsync()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not remove the logo.')
    }
  }

  return (
    <div className="flex flex-col gap-[var(--space-5)] max-w-[40rem]">
      <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)] leading-[var(--leading-relaxed)]">
        {intro}
      </p>

      {/* Logo: stored in tela (so it renders everywhere, incl. exported decks). */}
      <div className="flex flex-col gap-[var(--space-2)]">
        <span className="text-[length:var(--text-xs)] uppercase tracking-wider text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
          Logo
        </span>
        <div className="flex items-center gap-[var(--space-3)]">
          {hasLogo ? (
            <img
              src={logoUrl}
              alt="Logo"
              className="block max-h-[var(--space-8)] w-auto max-w-[12rem] rounded-[var(--radius-xs)] border border-[var(--border-subtle)] bg-[var(--surface-2)] p-[var(--space-1)]"
            />
          ) : (
            <span className="flex h-[var(--space-8)] w-[var(--space-8)] shrink-0 items-center justify-center rounded-[var(--radius-xs)] border border-dashed border-[var(--border-default)] text-[var(--text-muted)]">
              <Palette width={16} height={16} />
            </span>
          )}
          <input
            ref={fileRef}
            type="file"
            accept="image/png,image/jpeg,image/webp,image/gif"
            className="hidden"
            onChange={onPickFile}
          />
          <div className="flex flex-wrap items-center gap-[var(--space-2)]">
            <Button
              type="button"
              variant="secondary"
              size="sm"
              disabled={uploadLogo.isPending}
              onClick={() => fileRef.current?.click()}
            >
              <Upload width={14} height={14} />
              {uploadLogo.isPending ? 'Uploading…' : hasLogo ? 'Replace' : 'Upload'}
            </Button>
            <Button type="button" variant="ghost" size="sm" onClick={() => setShowImport((v) => !v)}>
              <Globe width={14} height={14} /> Import from URL
            </Button>
            {hasLogo ? (
              <Button type="button" variant="ghost" size="sm" disabled={deleteLogo.isPending} onClick={removeLogo}>
                <Trash2 width={14} height={14} /> Remove
              </Button>
            ) : null}
          </div>
        </div>
        {showImport ? (
          <div className="flex items-center gap-[var(--space-2)]">
            <div className="flex-1 min-w-0">
              <Input
                type="url"
                inputMode="url"
                autoComplete="off"
                placeholder="https://example.com/logo.png"
                value={importUrl}
                onChange={(e) => setImportUrl(e.target.value)}
              />
            </div>
            <Button type="button" variant="secondary" size="sm" disabled={put.isPending || !importUrl.trim()} onClick={doImport}>
              {put.isPending ? 'Importing…' : 'Import'}
            </Button>
          </div>
        ) : null}
        <p className="m-0 text-[length:var(--text-xs)] text-[var(--text-muted)]">
          PNG, JPEG, WebP or GIF, up to 2 MB. tela stores and serves it, so it renders everywhere.
        </p>
      </div>

      <form onSubmit={handleSubmit} noValidate className="flex flex-col gap-[var(--space-3)]">
        {withName ? (
          <div className="flex flex-col gap-[var(--space-1)]">
            <label
              htmlFor={`${idPrefix}-name`}
              className="text-[length:var(--text-xs)] uppercase tracking-wider text-[var(--text-muted)] font-[family-name:var(--font-sans)]"
            >
              Name
            </label>
            <Input
              id={`${idPrefix}-name`}
              autoComplete="off"
              maxLength={60}
              placeholder="tela"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </div>
        ) : null}
        <div className="flex flex-col gap-[var(--space-1)]">
          <label
            htmlFor={`${idPrefix}-accent`}
            className="text-[length:var(--text-xs)] uppercase tracking-wider text-[var(--text-muted)] font-[family-name:var(--font-sans)]"
          >
            Accent color
          </label>
          <div className="flex items-center gap-[var(--space-2)]">
            <div className="flex-1 min-w-0">
              <Input
                id={`${idPrefix}-accent`}
                autoComplete="off"
                placeholder="#4f46e5 or oklch(…)"
                value={accent}
                onChange={(e) => setAccent(e.target.value)}
              />
            </div>
            {accent.trim() ? (
              // Swatch fed by the entered value (runtime data). An invalid color
              // just paints transparent, a harmless preview.
              <span
                aria-hidden
                style={{ background: accent.trim() }}
                className="block h-[var(--space-6)] w-[var(--space-6)] shrink-0 rounded-[var(--radius-xs)] border border-[var(--border-subtle)]"
              />
            ) : null}
          </div>
        </div>

        {error ? (
          <p role="alert" className="m-0 text-[length:var(--text-xs)] text-[var(--danger)]">
            {error}
          </p>
        ) : saved ? (
          <p className="m-0 text-[length:var(--text-xs)] text-[var(--text-muted)]">Saved.</p>
        ) : null}

        <div>
          <Button type="submit" variant="secondary" size="sm" disabled={put.isPending}>
            {put.isPending ? 'Saving…' : 'Save'}
          </Button>
        </div>
      </form>
    </div>
  )
}
