import type { ComponentType } from 'react'
import {
  AlertTriangle,
  Bug,
  Clock,
  CreditCard,
  Eye,
  FilePlus,
  LogIn,
  LogOut,
  Pencil,
  Shield,
  Sparkles,
  Terminal,
  Activity,
} from 'lucide-react'
import type { EventEntry } from '../../lib/types'
import { relativeTimeFromSqlite } from '../../lib/relativeTime'
import { Badge } from '../ui/badge'
import { cn } from '../../lib/utils'

type Tone = 'muted' | 'danger' | 'accent'

interface Descriptor {
  Icon: ComponentType<{ width?: number; height?: number }>
  // Short group label for the trailing badge.
  group: string
  tone: Tone
  // The verb phrase shown after the actor (target rendered separately, bold).
  verb: string
}

// Map an event type to its icon + verb. access.* and unknown types fall through
// to a generic descriptor built from the type code.
function describe(type: string): Descriptor {
  switch (type) {
    case 'auth.login':
      return { Icon: LogIn, group: 'auth', tone: 'muted', verb: 'signed in' }
    case 'auth.logout':
      return { Icon: LogOut, group: 'auth', tone: 'muted', verb: 'signed out' }
    case 'auth.login_failed':
      return { Icon: AlertTriangle, group: 'auth', tone: 'danger', verb: 'failed to sign in' }
    case 'page.view':
      return { Icon: Eye, group: 'page', tone: 'muted', verb: 'viewed' }
    case 'page.create':
      return { Icon: FilePlus, group: 'page', tone: 'accent', verb: 'created' }
    case 'page.edit':
      return { Icon: Pencil, group: 'page', tone: 'muted', verb: 'edited' }
    case 'ask':
      return { Icon: Sparkles, group: 'ask', tone: 'accent', verb: 'asked' }
    case 'api.request':
      return { Icon: Terminal, group: 'api', tone: 'muted', verb: 'API request' }
    case 'client.error':
      return { Icon: Bug, group: 'error', tone: 'danger', verb: 'hit a client error' }
    // billing.* — the money path, in order: saw the prices, pressed the button,
    // Polar's verdict on that session, subscription state, money, trial dates.
    case 'billing.plans_viewed':
      return { Icon: Eye, group: 'billing', tone: 'muted', verb: 'viewed plans' }
    case 'billing.checkout':
      return { Icon: CreditCard, group: 'billing', tone: 'accent', verb: 'started checkout' }
    case 'billing.checkout_status':
      return { Icon: CreditCard, group: 'billing', tone: 'muted', verb: 'checkout ended' }
    case 'billing.subscription_update':
      return { Icon: CreditCard, group: 'billing', tone: 'accent', verb: 'subscription updated' }
    case 'billing.subscription_canceled':
      return { Icon: CreditCard, group: 'billing', tone: 'danger', verb: 'scheduled a cancellation' }
    case 'billing.subscription_revoked':
      return { Icon: CreditCard, group: 'billing', tone: 'danger', verb: 'lost their subscription' }
    case 'billing.payment':
      return { Icon: CreditCard, group: 'billing', tone: 'accent', verb: 'paid' }
    case 'billing.trial_started':
      return { Icon: Clock, group: 'billing', tone: 'muted', verb: 'started a trial' }
    case 'billing.trial_ending':
      return { Icon: Clock, group: 'billing', tone: 'muted', verb: "trial is ending" }
    case 'billing.trial_expired':
      return { Icon: Clock, group: 'billing', tone: 'danger', verb: 'trial expired' }
    default:
      if (type.startsWith('access.')) {
        // 'access.org_member.add' → 'org member add'
        const action = type.slice('access.'.length).replace(/[._]/g, ' ')
        return { Icon: Shield, group: 'access', tone: 'accent', verb: action }
      }
      return { Icon: Activity, group: type.split('.')[0] || 'event', tone: 'muted', verb: type }
  }
}

// Badge primitive ships muted/accent; the danger tone is carried by the red icon
// instead, so its badge stays muted.
const BADGE_VARIANT: Record<Tone, 'muted' | 'accent'> = {
  muted: 'muted',
  danger: 'muted',
  accent: 'accent',
}

// A run of consecutive identical events collapsed into one feed row. `head` is
// the newest occurrence (the feed is newest-first); `count` and `oldestAt` cover
// the whole run.
export interface EventGroup {
  head: EventEntry
  count: number
  oldestAt: string
}

// collapseKey identifies "the same repeated action": same type, same actor, same
// target, same detail. client.error rows are never collapsed — each carries its
// own message/stack, and they have a dedicated grouped view (the Errors tab).
function collapseKey(e: EventEntry): string | null {
  if (e.type === 'client.error') return null
  const actor = e.actor_user_id != null ? `u${e.actor_user_id}` : `l:${e.actor_label}`
  return [e.type, actor, e.target_kind, e.target_id ?? '', e.detail].join('|')
}

// collapseEvents folds consecutive identical events into groups so a burst of
// autosave edits (or repeated views) reads as one "×N" row instead of a wall of
// duplicates. Order-preserving; only *adjacent* matches merge, so the timeline is
// never reordered. Recomputes over the full loaded list, so a run that straddles
// an infinite-scroll page boundary still merges as more loads in.
export function collapseEvents(events: EventEntry[]): EventGroup[] {
  const out: (EventGroup & { key: string | null })[] = []
  for (const e of events) {
    const key = collapseKey(e)
    const prev = out[out.length - 1]
    if (key != null && prev && prev.key === key) {
      prev.count++
      prev.oldestAt = e.created_at // newest-first, so each later row is older
    } else {
      out.push({ head: e, count: 1, oldestAt: e.created_at, key })
    }
  }
  return out
}

export function EventRow({
  event,
  count = 1,
  oldestAt,
}: {
  event: EventEntry
  count?: number
  oldestAt?: string
}) {
  const d = describe(event.type)
  const actor = event.actor_label || 'anonymous'
  const collapsed = count > 1
  // page.* events carry a title in target_label; render it set off from the verb.
  const showTarget = event.target_label !== ''
  return (
    <li
      className={cn(
        'm-0 list-none flex items-start gap-[var(--space-3)]',
        'px-[var(--space-3)] py-[var(--space-2)]',
        'rounded-[var(--radius-sm)]',
        'border border-[var(--border-subtle)] bg-[var(--surface-1)]',
      )}
    >
      <span
        className={cn(
          'mt-[2px] shrink-0',
          d.tone === 'danger' ? 'text-[var(--danger)]' : 'text-[var(--text-muted)]',
        )}
      >
        <d.Icon width={15} height={15} />
      </span>
      <div className="flex-1 min-w-0 flex flex-col gap-[1px]">
        <div className="flex items-baseline gap-[var(--space-2)] min-w-0 flex-wrap">
          <span className="text-[length:var(--text-sm)] text-[var(--text-primary)] font-[family-name:var(--font-sans)]">
            <span className="font-medium">{actor}</span> {d.verb}
            {showTarget ? (
              <>
                {' '}
                <span className="font-medium">{event.target_label}</span>
              </>
            ) : null}
          </span>
          {collapsed ? (
            <span
              className="shrink-0 rounded-[var(--radius-xs)] bg-[var(--surface-3)] px-[var(--space-1)] text-[length:var(--text-xs)] font-medium tabular-nums text-[var(--text-muted)]"
              title={`${count} times`}
            >
              ×{count}
            </span>
          ) : null}
          {event.detail && d.group !== 'access' && d.group !== 'error' ? (
            <span className="truncate max-w-full text-[length:var(--text-xs)] text-[var(--text-muted)]">
              {event.detail}
            </span>
          ) : null}
        </div>
        {/* Client errors carry a multi-line message + stack; show it in full
            (the row truncation would hide the stack that makes it useful). */}
        {event.detail && d.group === 'error' ? (
          <pre className="m-0 mt-[var(--space-1)] max-h-[12rem] overflow-auto whitespace-pre-wrap break-words rounded-[var(--radius-sm)] bg-[var(--surface-2)] p-[var(--space-2)] text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-mono)]">
            {event.detail}
          </pre>
        ) : null}
        <span className="text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
          {relativeTimeFromSqlite(event.created_at)}
          {collapsed && oldestAt
            ? ` · earliest ${relativeTimeFromSqlite(oldestAt)}`
            : event.ip
              ? ` · ${event.ip}`
              : ''}
        </span>
      </div>
      <Badge variant={BADGE_VARIANT[d.tone]}>{d.group}</Badge>
    </li>
  )
}
