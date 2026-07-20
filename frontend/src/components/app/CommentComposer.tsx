import { useState } from 'react'
import type { CommentAnchor } from '../../lib/comments/anchor'
import { ApiError } from '../../lib/api'
import { Button } from '../ui/button'
import { TextArea } from '../ui/textarea'
import { cn } from '../../lib/utils'

interface CommentComposerProps {
  // True when the editor currently has a non-empty selection. When false,
  // the textarea + submit are disabled and a hint is shown instead.
  hasSelection: boolean
  // Snapshot the current editor selection at submit time. Returns null if the
  // selection has collapsed between hint-render and submit (race window).
  captureAnchor: () => CommentAnchor | null
  onSubmit: (input: {
    body: string
    anchor_prefix: string
    anchor_exact: string
    anchor_suffix: string
  }) => Promise<void>
  // When non-null, surfaces the exact text that would be anchored on
  // submit. Drives the inline "Commenting on: …" preview above the
  // textarea so the user can confirm the right passage is selected.
  anchorPreview: string | null
}

export function CommentComposer({
  hasSelection,
  captureAnchor,
  onSubmit,
  anchorPreview,
}: CommentComposerProps) {
  const [body, setBody] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const disabled = !hasSelection || busy

  async function handleSubmit() {
    const trimmed = body.trim()
    if (!trimmed) {
      setError('Body is required.')
      return
    }
    const anchor = captureAnchor()
    if (!anchor) {
      setError('Select a passage in the page before commenting.')
      return
    }
    setBusy(true)
    setError(null)
    try {
      await onSubmit({
        body: trimmed,
        anchor_prefix: anchor.prefix,
        anchor_exact: anchor.exact,
        anchor_suffix: anchor.suffix,
      })
      setBody('')
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Failed to post comment.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex flex-col gap-[var(--space-2)]">
      {hasSelection && anchorPreview ? (
        <div
          className={cn(
            'rounded-[var(--radius-sm)] px-[var(--space-3)] py-[var(--space-2)]',
            'bg-[var(--surface-2)] border-l-2 border-[var(--accent)]',
            'text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]',
            'overflow-hidden',
          )}
        >
          <div className="mb-[2px] uppercase tracking-wider">
            Commenting on
          </div>
          <div
            className={cn(
              'truncate text-[var(--text-primary)]',
              'whitespace-pre-wrap line-clamp-3',
            )}
          >
            {anchorPreview}
          </div>
        </div>
      ) : null}
      {!hasSelection ? (
        <p
          className={cn(
            'm-0 px-[var(--space-3)] py-[var(--space-2)]',
            'rounded-[var(--radius-sm)] bg-[var(--surface-2)]',
            'text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]',
          )}
        >
          Select text in the page to comment on a passage.
        </p>
      ) : null}
      <TextArea
        font="sans"
        size="sm"
        value={body}
        onChange={(e) => setBody(e.target.value)}
        placeholder={hasSelection ? 'Add a comment…' : 'Select a passage first'}
        disabled={disabled}
        aria-label="New comment body"
      />
      {error ? (
        <p
          role="alert"
          className="m-0 text-[length:var(--text-xs)] text-[var(--danger)]"
        >
          {error}
        </p>
      ) : null}
      <div className="flex justify-end">
        <Button
          type="button"
          variant="primary"
          size="sm"
          onClick={() => void handleSubmit()}
          disabled={disabled || body.trim().length === 0}
        >
          {busy ? 'Posting…' : 'Comment'}
        </Button>
      </div>
    </div>
  )
}
