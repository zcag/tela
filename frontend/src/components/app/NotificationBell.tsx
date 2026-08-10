import { Link } from '@tanstack/react-router'
import { Bell, Check } from 'lucide-react'
import {
  useNotifications,
  useUnreadCount,
  useMarkNotificationRead,
  useMarkAllNotificationsRead,
  type NotificationItem,
} from '../../lib/queries/notifications'
import { Button } from '../ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '../ui/dropdown-menu'
import { relativeTimeFromSqlite } from '../../lib/relativeTime'
import { cn } from '../../lib/utils'

// Human copy per notification type. A new type adds a case here (and an emit
// site in the backend) — nothing else changes. See docs/notifications.md.
function describe(n: NotificationItem): string {
  const actor = n.actor_username ?? 'Someone'
  const title = typeof n.data.page_title === 'string' ? n.data.page_title : 'a page'
  switch (n.type) {
    case 'mention':
      return `${actor} mentioned you in “${title}”`
    case 'page_updated':
      return `${actor} updated “${title}”`
    case 'page_created':
      return `${actor} created “${title}”`
    case 'comment_reply':
      return `${actor} replied to your comment in “${title}”`
    case 'space_added': {
      const space = typeof n.data.space_name === 'string' ? n.data.space_name : 'a space'
      return `${actor} added you to ${space}`
    }
    case 'user_registered': {
      const who =
        typeof n.data.new_display_name === 'string' && n.data.new_display_name
          ? n.data.new_display_name
          : actor
      return `${who} just signed up`
    }
    case 'atlas_run': {
      // The backend pre-renders a headline (title) + one-line detail (summary),
      // e.g. "compass regenerated" / "13 pages, must-cover 100%, surface 97%".
      const t = typeof n.data.title === 'string' ? n.data.title : 'Atlas run finished'
      const summary = typeof n.data.summary === 'string' ? n.data.summary : ''
      return summary ? `${t} — ${summary}` : t
    }
    case 'atlas_cadence_changed': {
      // Backend-rendered like atlas_run: the copy describes a change we made, so
      // it is written where the change is (migration 0074) rather than assembled
      // from fields here.
      const t = typeof n.data.title === 'string' ? n.data.title : 'Atlas refresh cadence changed'
      const summary = typeof n.data.summary === 'string' ? n.data.summary : ''
      return summary ? `${t} — ${summary}` : t
    }
    default:
      return `${actor} sent you a notification`
  }
}

// Header notification bell: a polled unread badge + a panel of recent items,
// each deep-linking to its subject and marking itself read on click.
export function NotificationBell() {
  const unread = useUnreadCount()
  const notifications = useNotifications()
  const markRead = useMarkNotificationRead()
  const markAll = useMarkAllNotificationsRead()
  const count = unread.data ?? 0
  const items = notifications.data ?? []

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="sm"
          aria-label={count > 0 ? `Notifications, ${count} unread` : 'Notifications'}
          className="relative h-[var(--space-8)] w-[var(--space-8)] p-0"
        >
          <Bell width={16} height={16} />
          {count > 0 ? (
            <span className="absolute top-[2px] right-[2px] flex items-center justify-center min-w-[15px] h-[15px] px-[3px] rounded-full bg-[var(--accent)] text-[var(--accent-fg)] text-[length:var(--text-xs)] leading-none">
              {count > 9 ? '9+' : count}
            </span>
          ) : null}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="end"
        className="w-[22rem] max-h-[28rem] overflow-y-auto p-[var(--space-1)]"
      >
        <div className="flex items-center justify-between px-[var(--space-2)] py-[var(--space-1)]">
          <span className="text-[length:var(--text-sm)] font-semibold text-[var(--text-primary)]">
            Notifications
          </span>
          {count > 0 ? (
            <button
              type="button"
              onClick={(e) => {
                e.preventDefault()
                markAll.mutate()
              }}
              className="inline-flex items-center gap-[var(--space-1)] text-[length:var(--text-xs)] text-[var(--text-muted)] hover:text-[var(--text-primary)]"
            >
              <Check width={12} height={12} /> Mark all read
            </button>
          ) : null}
        </div>

        {items.length === 0 ? (
          <p className="m-0 px-[var(--space-2)] py-[var(--space-5)] text-center text-[length:var(--text-sm)] text-[var(--text-muted)]">
            You’re all caught up.
          </p>
        ) : (
          items.map((n) => (
            <NotificationRow
              key={n.id}
              n={n}
              onOpen={() => {
                if (!n.read) markRead.mutate(n.id)
              }}
            />
          ))
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function NotificationRow({ n, onOpen }: { n: NotificationItem; onOpen: () => void }) {
  const className = cn(
    'flex flex-col gap-[2px] items-start no-underline',
    !n.read && 'bg-[var(--sidebar-item-active)]',
  )
  const inner = (
    <>
      <span className="text-[length:var(--text-sm)] text-[var(--text-primary)] whitespace-normal">
        {describe(n)}
      </span>
      <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">
        {relativeTimeFromSqlite(n.created_at)}
      </span>
    </>
  )

  // An atlas-run notification points at its project (valid even when the run
  // failed and no output space was materialized); checked before the space
  // branch since these rows carry subject_kind 'space'.
  if (n.type === 'atlas_run' && typeof n.data.project_id === 'number') {
    return (
      <DropdownMenuItem asChild>
        <Link
          to="/atlas/projects/$projectId"
          params={{ projectId: n.data.project_id }}
          onClick={onOpen}
          className={className}
        >
          {inner}
        </Link>
      </DropdownMenuItem>
    )
  }
  // Deep-link by subject: page → the page, space → the space; else home.
  if (n.subject_kind === 'page' && n.space_id != null) {
    return (
      <DropdownMenuItem asChild>
        <Link
          to="/spaces/$spaceId/pages/$pageId/{-$slug}"
          params={{ spaceId: n.space_id, pageId: n.subject_id, slug: undefined }}
          onClick={onOpen}
          className={className}
        >
          {inner}
        </Link>
      </DropdownMenuItem>
    )
  }
  if (n.subject_kind === 'space') {
    return (
      <DropdownMenuItem asChild>
        <Link
          to="/spaces/$spaceId"
          params={{ spaceId: n.subject_id }}
          onClick={onOpen}
          className={className}
        >
          {inner}
        </Link>
      </DropdownMenuItem>
    )
  }
  // A new-signup notification points at the registrant's public handle home.
  if (n.subject_kind === 'user' && typeof n.data.new_username === 'string') {
    return (
      <DropdownMenuItem asChild>
        <Link
          to="/$handle"
          params={{ handle: n.data.new_username }}
          onClick={onOpen}
          className={className}
        >
          {inner}
        </Link>
      </DropdownMenuItem>
    )
  }
  return (
    <DropdownMenuItem asChild>
      <Link to="/" onClick={onOpen} className={className}>
        {inner}
      </Link>
    </DropdownMenuItem>
  )
}
