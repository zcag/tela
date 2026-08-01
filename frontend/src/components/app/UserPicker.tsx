import { useMemo, useState } from 'react'
import { Check, ChevronsUpDown, Mail, UserRound } from 'lucide-react'
import { cn } from '../../lib/utils'
import { Popover, PopoverContent, PopoverTrigger } from '../ui/popover'
import { CommandInlinePicker, type CommandItem } from '../ui/command'
import { useUsers, type MentionUser } from '../../lib/queries/users'

export interface UserPickerProps {
  value: MentionUser | null
  onChange: (user: MentionUser | null) => void
  id?: string
  placeholder?: string
  disabled?: boolean
  invalid?: boolean
  // Users already present (e.g. existing members) — hidden from the list so
  // the same person can't be offered twice.
  excludeIds?: Iterable<number>
  // When set, only these user ids are offered (e.g. a group can only contain
  // members of its org). Undefined = the whole directory.
  restrictToIds?: Iterable<number>
  // When set, typing an address that belongs to nobody in the directory offers
  // an extra "Invite <email>" row — so one control both picks an existing
  // person and reaches someone who has no account yet.
  onInviteEmail?: (email: string) => void
  // Sub-label on that row, e.g. "Send an email invitation".
  inviteHint?: string
  // A chosen-but-not-yet-submitted email, rendered in the trigger the way a
  // picked user is — so the control reads the same whichever path you took.
  emailValue?: string | null
}

// Deliberately permissive — the server does the real RFC parse. This only
// decides whether the typed text is worth offering as an invitation.
function looksLikeEmail(s: string): boolean {
  return /^[^\s@]+@[^\s@.]+\.[^\s@]+$/.test(s.trim())
}

// Searchable user picker over the instance mention directory (GET /api/users),
// matching on both username and email. Reusable across every "add a member"
// surface (space members, org members, group members) so nobody has to type an
// exact username blind. Trigger looks like an Input; the list is a cmdk popover.
export function UserPicker({
  value,
  onChange,
  id,
  placeholder = 'Search by username or email…',
  disabled,
  invalid,
  excludeIds,
  restrictToIds,
  onInviteEmail,
  inviteHint = 'Send an email invitation',
  emailValue,
}: UserPickerProps) {
  const [open, setOpen] = useState(false)
  const [search, setSearch] = useState('')
  const { data: users, isLoading } = useUsers()

  const excluded = useMemo(() => new Set(excludeIds ?? []), [excludeIds])
  const allowed = useMemo(
    () => (restrictToIds ? new Set(restrictToIds) : null),
    [restrictToIds],
  )

  const items = useMemo<CommandItem[]>(() => {
    const known = (users ?? [])
      .filter((u) => (allowed ? allowed.has(u.id) : true))
      .filter((u) => !excluded.has(u.id))
      .map((u) => ({
        id: String(u.id),
        title: u.username,
        subtitle: u.email,
        keywords: u.email ? [u.email] : undefined,
        icon:
          value?.id === u.id ? (
            <Check width={14} height={14} />
          ) : (
            <UserRound width={14} height={14} />
          ),
        onSelect: () => {
          onChange(u)
          setOpen(false)
        },
      }))
    // A typed address nobody in the directory owns → offer to invite it.
    const typed = search.trim().toLowerCase()
    if (
      onInviteEmail &&
      looksLikeEmail(typed) &&
      !(users ?? []).some((u) => u.email?.toLowerCase() === typed)
    ) {
      known.push({
        id: `invite:${typed}`,
        title: typed,
        subtitle: inviteHint,
        keywords: [typed, 'invite'],
        icon: <Mail width={14} height={14} />,
        onSelect: () => {
          onInviteEmail(typed)
          setSearch('')
          setOpen(false)
        },
      })
    }
    return known
  }, [users, excluded, allowed, value, onChange, search, onInviteEmail, inviteHint])

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        id={id}
        type="button"
        disabled={disabled}
        aria-invalid={invalid || undefined}
        className={cn(
          'flex w-full items-center gap-[var(--space-2)]',
          'rounded-[var(--radius-md)] border border-[var(--border-subtle)]',
          'bg-[var(--surface-1)] px-[var(--space-3)]',
          'h-[var(--space-8)] text-[length:var(--text-sm)]',
          'font-[family-name:var(--font-sans)] leading-[var(--leading-tight)]',
          'transition-[background-color,color,border-color,box-shadow] duration-[var(--duration-fast)] ease-[var(--ease-out)]',
          'outline-none',
          'focus-visible:border-[var(--accent)] focus-visible:ring-2 focus-visible:ring-[var(--accent)] focus-visible:ring-offset-1 focus-visible:ring-offset-[var(--surface-1)]',
          'disabled:cursor-not-allowed disabled:opacity-50 disabled:bg-[var(--surface-2)]',
          'aria-invalid:border-[var(--danger)] aria-invalid:focus-visible:ring-[var(--danger)]',
        )}
      >
        {value ? (
          <span className="flex-1 min-w-0 flex items-baseline gap-[var(--space-2)] text-left">
            <span className="truncate text-[var(--text-primary)]">
              {value.username}
            </span>
            {value.email ? (
              <span className="truncate text-[length:var(--text-xs)] text-[var(--text-muted)]">
                {value.email}
              </span>
            ) : null}
          </span>
        ) : emailValue ? (
          <span className="flex-1 min-w-0 flex items-center gap-[var(--space-2)] text-left">
            <Mail
              width={14}
              height={14}
              aria-hidden
              className="shrink-0 text-[var(--text-muted)]"
            />
            <span className="truncate text-[var(--text-primary)]">
              {emailValue}
            </span>
          </span>
        ) : (
          <span className="flex-1 min-w-0 truncate text-left text-[var(--text-muted)]">
            {placeholder}
          </span>
        )}
        <ChevronsUpDown
          width={14}
          height={14}
          aria-hidden
          className="shrink-0 text-[var(--text-muted)]"
        />
      </PopoverTrigger>
      <PopoverContent
        align="start"
        portal={false}
        className="p-0 w-[var(--radix-popover-trigger-width)] min-w-[16rem]"
      >
        <CommandInlinePicker
          label="Users"
          placeholder={placeholder}
          emptyMessage={
            isLoading
              ? 'Loading users…'
              : onInviteEmail
                ? 'No match — type a full email address to invite.'
                : 'No matching users.'
          }
          items={items}
          search={search}
          onSearchChange={setSearch}
        />
      </PopoverContent>
    </Popover>
  )
}
