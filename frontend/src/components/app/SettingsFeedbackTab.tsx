import { useEffect, useState } from 'react'
import { Link } from '@tanstack/react-router'
import {
  useAdminFeedback,
  useMarkFeedbackSeen,
  useSetFeedbackStatus,
} from '../../lib/queries/admin-usage'
import { localDateFromSqlite } from '../../lib/relativeTime'
import type {
  FeedbackContext,
  FeedbackEntry,
  FeedbackKind,
  FeedbackStatus,
} from '../../lib/types'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { cn } from '../../lib/utils'

// Instance-admin inbox for feedback submitted via the in-app widget, the MCP
// submit_feedback tool, or a direct API post. Newest first. Each row surfaces
// the type, where it came from, and the silent context (page, build, browser)
// so a report can be triaged — and acted on — without a back-and-forth.
//
// Triage state is per-entry and explicit. It is NOT the unread badge: that's a
// per-admin "last opened this tab" watermark, stamped on open, so with only it
// every entry read as permanently seen and never done — a bug you'd already
// fixed still sat in the list looking untouched.
export function SettingsFeedbackTab() {
  // Default to Open: the inbox's job is what still needs attention.
  const [filter, setFilter] = useState<FeedbackStatus | undefined>('open')
  const fb = useAdminFeedback(filter)
  // Opening the inbox marks everything seen → clears the unread badge.
  const { mutate: markSeen } = useMarkFeedbackSeen()
  useEffect(() => markSeen(), [markSeen])

  return (
    <section aria-labelledby="settings-feedback" className="flex flex-col gap-[var(--space-4)]">
      <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)] leading-[var(--leading-relaxed)]">
        Feedback your users sent about tela — from the in-app widget, an agent's
        submit_feedback tool, or the API. Most recent first.
      </p>

      <div role="group" aria-label="Filter by status" className="flex gap-[var(--space-1)]">
        {FILTERS.map((f) => (
          <Button
            key={f.label}
            type="button"
            size="sm"
            variant={filter === f.value ? 'secondary' : 'ghost'}
            aria-pressed={filter === f.value}
            onClick={() => setFilter(f.value)}
          >
            {f.label}
          </Button>
        ))}
      </div>

      {fb.isLoading ? (
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">Loading feedback…</p>
      ) : fb.isError ? (
        <p role="alert" className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]">
          Couldn't load feedback.
        </p>
      ) : fb.data && fb.data.length > 0 ? (
        <ul className="m-0 p-0 list-none flex flex-col gap-[var(--space-2)]">
          {fb.data.map((e) => (
            <FeedbackRow key={e.id} entry={e} />
          ))}
        </ul>
      ) : (
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
          {filter === 'open' ? 'Nothing open — all caught up.' : 'No feedback yet.'}
        </p>
      )}
    </section>
  )
}

const FILTERS: ReadonlyArray<{ label: string; value: FeedbackStatus | undefined }> = [
  { label: 'Open', value: 'open' },
  { label: 'Done', value: 'done' },
  { label: "Won't fix", value: 'wontfix' },
  { label: 'All', value: undefined },
]

const KIND_VARIANT: Record<FeedbackKind, 'danger' | 'accent' | 'muted'> = {
  bug: 'danger',
  idea: 'accent',
  other: 'muted',
}

// Friendly origin label; 'web' is the common case and stays unlabelled to cut noise.
function sourceLabel(source: string): string | null {
  if (source === 'mcp') return 'agent'
  if (source === 'api') return 'API'
  return null
}

// Condense a user-agent to a short "Browser · OS" hint — no UA-parsing dep.
function describeUA(ua?: string): string | null {
  if (!ua) return null
  const browser = /Edg\//.test(ua)
    ? 'Edge'
    : /Firefox\//.test(ua)
      ? 'Firefox'
      : /Chrome\//.test(ua)
        ? 'Chrome'
        : /Safari\//.test(ua)
          ? 'Safari'
          : null
  const os = /Mac OS X/.test(ua)
    ? 'macOS'
    : /Windows/.test(ua)
      ? 'Windows'
      : /Android/.test(ua)
        ? 'Android'
        : /iPhone|iPad/.test(ua)
          ? 'iOS'
          : /Linux/.test(ua)
            ? 'Linux'
            : null
  const s = [browser, os].filter(Boolean).join(' · ')
  return s || null
}

function FeedbackRow({ entry }: { entry: FeedbackEntry }) {
  const who = entry.username ?? (entry.user_id ? `user #${entry.user_id}` : 'unknown')
  const source = sourceLabel(entry.source)
  const { mutate: setStatus, isPending } = useSetFeedbackStatus()
  const open = entry.status === 'open'
  return (
    <li
      className={cn(
        'm-0 list-none flex flex-col gap-[var(--space-2)]',
        'px-[var(--space-3)] py-[var(--space-3)]',
        'rounded-[var(--radius-sm)] border border-[var(--border-subtle)] bg-[var(--surface-1)]',
      )}
    >
      <div className="flex items-start justify-between gap-[var(--space-3)]">
        <span className="text-[length:var(--text-sm)] font-medium text-[var(--text-primary)] font-[family-name:var(--font-sans)]">
          {entry.subject}
        </span>
        <div className="flex items-center gap-[var(--space-1)] shrink-0">
          {entry.kind ? (
            <Badge variant={KIND_VARIANT[entry.kind]} className="capitalize">
              {entry.kind}
            </Badge>
          ) : null}
          {source ? <Badge variant="muted">{source}</Badge> : null}
          {!open ? (
            <Badge variant="muted">{entry.status === 'done' ? 'Done' : "Won't fix"}</Badge>
          ) : null}
        </div>
      </div>
      {entry.body.trim() !== entry.subject.trim() ? (
        // Subject is the body's first line, so for single-line feedback they're
        // identical — only show the body when it adds something.
        <p className="m-0 whitespace-pre-wrap text-[length:var(--text-sm)] text-[var(--text-primary)] leading-[var(--leading-relaxed)]">
          {entry.body}
        </p>
      ) : null}
      <FeedbackMeta context={entry.context} />
      <div className="flex flex-wrap items-center justify-between gap-[var(--space-2)]">
        <span className="text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
          {who} · {localDateFromSqlite(entry.created_at)}
          {entry.resolved_at
            ? ` · resolved ${localDateFromSqlite(entry.resolved_at)}`
            : null}
        </span>
        <div className="flex gap-[var(--space-1)]">
          {open ? (
            <>
              <Button
                type="button"
                size="sm"
                variant="secondary"
                disabled={isPending}
                onClick={() => setStatus({ id: entry.id, status: 'done' })}
              >
                Mark done
              </Button>
              <Button
                type="button"
                size="sm"
                variant="ghost"
                disabled={isPending}
                onClick={() => setStatus({ id: entry.id, status: 'wontfix' })}
              >
                Won't fix
              </Button>
            </>
          ) : (
            <Button
              type="button"
              size="sm"
              variant="ghost"
              disabled={isPending}
              onClick={() => setStatus({ id: entry.id, status: 'open' })}
            >
              Reopen
            </Button>
          )}
        </div>
      </div>
    </li>
  )
}

// The silent triage line: a link to the page the feedback was sent from, then a
// muted "build · browser · viewport" trail. Renders nothing when there's no
// context (agent/api submissions often carry none).
function FeedbackMeta({ context }: { context: FeedbackContext | null }) {
  if (!context) return null
  const bits = [
    context.app_version && context.app_version !== 'dev' ? `v${context.app_version}` : null,
    describeUA(context.user_agent),
    context.viewport,
    context.theme,
  ].filter(Boolean) as string[]

  const hasPage = typeof context.page_id === 'number' && typeof context.space_id === 'number'
  if (!hasPage && bits.length === 0) return null

  return (
    <div className="flex flex-wrap items-center gap-x-[var(--space-2)] gap-y-[var(--space-1)] text-[length:var(--text-xs)] text-[var(--text-muted)]">
      {hasPage ? (
        <>
          <Link
            to="/spaces/$spaceId/pages/$pageId/{-$slug}"
            params={{ spaceId: context.space_id!, pageId: context.page_id!, slug: undefined }}
            className="text-[var(--accent)] no-underline hover:underline truncate max-w-[16rem]"
          >
            {context.page_title ? `“${context.page_title}”` : `page #${context.page_id}`}
          </Link>
          {bits.length > 0 ? <span aria-hidden>·</span> : null}
        </>
      ) : null}
      {bits.length > 0 ? <span>{bits.join(' · ')}</span> : null}
    </div>
  )
}
