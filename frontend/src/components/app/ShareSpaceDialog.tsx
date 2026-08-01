import { useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { Building2, Globe, Lock, Mail, Trash2, UserPlus, Users } from 'lucide-react'
import { ApiError } from '../../lib/api'
import { useMe } from '../../lib/queries/auth'
import {
  useAddSpaceMember,
  useRemoveSpaceMember,
  useSpaceMembers,
  useUpdateSpaceMember,
} from '../../lib/queries/members'
import { useSpaceRole, useTransferSpace, useUpdateSpace } from '../../lib/queries/spaces'
import { useOrgs } from '../../lib/queries/orgs'
import {
  useRevokeSpaceInvite,
  useShareSpaceByEmail,
  useSpaceInvites,
} from '../../lib/queries/invites'
import { useMyGroups } from '../../lib/queries/groups'
import {
  useAddSpaceGrant,
  useRemoveSpaceGrant,
  useSpaceAccess,
  useSpaceGrants,
  useUpdateSpaceGrant,
} from '../../lib/queries/space-grants'
import type {
  AccessSource,
  Org,
  Space,
  SpaceGrant,
  SpaceMember,
} from '../../lib/types'
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
import { TextArea } from '../ui/textarea'
import { Select } from '../ui/select'
import { UserPicker } from './UserPicker'
import type { MentionUser } from '../../lib/queries/users'
import { cn } from '../../lib/utils'
import { spaceOwnership } from '../../lib/space-owner'

const ROLE_LABEL: Record<SpaceMember['role'], string> = {
  owner: 'Owner',
  editor: 'Editor',
  viewer: 'Viewer',
}

interface ShareSpaceDialogProps {
  space: Space
  open: boolean
  onOpenChange: (next: boolean) => void
}

export function ShareSpaceDialog({
  space,
  open,
  onOpenChange,
}: ShareSpaceDialogProps) {
  const me = useMe()
  const members = useSpaceMembers(open ? space.id : null)
  const { isOwner: iAmOwner } = useSpaceRole(open ? space.id : null)

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Share "{space.name}"</DialogTitle>
          <DialogDescription>
            Members of this space can view its pages. Owners can change roles
            and invite new members.
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-col gap-[var(--space-4)]">
          <OwnedByLine space={space} />

          {open ? <SpaceAccessSummary spaceId={space.id} /> : null}

          <div className="flex flex-col gap-[var(--space-2)] pt-[var(--space-3)] border-t border-[var(--border-subtle)]">
            <span className="text-[length:var(--text-xs)] uppercase tracking-wider text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
              Direct members
            </span>
          {members.isLoading ? (
            <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
              Loading members…
            </p>
          ) : members.isError ? (
            <p
              role="alert"
              className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]"
            >
              Couldn't load members.
            </p>
          ) : members.data && members.data.length > 0 ? (
            <ul className="m-0 p-0 list-none flex flex-col gap-[var(--space-1)]">
              {members.data.map((member) => (
                <MemberRow
                  key={member.user_id}
                  space={space}
                  member={member}
                  isSelf={me.data?.id === member.user_id}
                  iAmOwner={iAmOwner}
                  onSelfLeave={() => onOpenChange(false)}
                />
              ))}
            </ul>
          ) : null}

          {iAmOwner ? (
            <>
              <AddMemberForm spaceId={space.id} />
              {open ? <PendingInvites spaceId={space.id} /> : null}
            </>
          ) : null}
          </div>

          {open ? <SpaceOrgGrants space={space} iAmOwner={iAmOwner} /> : null}

          <PublicAccessSection space={space} iAmOwner={iAmOwner} />

          {iAmOwner ? <TransferOwnershipSection space={space} /> : null}
        </div>

        <DialogFooter>
          <DialogClose asChild>
            <Button type="button" variant="ghost">
              Close
            </Button>
          </DialogClose>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// Ownership at a glance — the account this space belongs to (an org vs you),
// which the rest of the dialog (members, grants, transfer) never states
// outright. Org-owned shows a Building2; personal/you stays an unadorned line.
function OwnedByLine({ space }: { space: Space }) {
  const owner = spaceOwnership(space)
  return (
    <p className="m-0 flex items-center gap-[var(--space-2)] text-[length:var(--text-sm)] text-[var(--text-muted)]">
      {owner.kind === 'org' ? (
        <Building2
          width={14}
          height={14}
          aria-hidden
          className="shrink-0 text-[var(--text-muted)]"
        />
      ) : null}
      <span>
        {owner.kind === 'org' ? (
          <>
            Owned by{' '}
            <span className="font-medium text-[var(--text-primary)]">
              {owner.org}
            </span>
          </>
        ) : owner.kind === 'personal' ? (
          'Personal space'
        ) : (
          <>
            Owned by{' '}
            <span className="font-medium text-[var(--text-primary)]">you</span>
          </>
        )}
      </span>
    </p>
  )
}

interface MemberRowProps {
  space: Space
  member: SpaceMember
  isSelf: boolean
  iAmOwner: boolean
  onSelfLeave: () => void
}

function MemberRow({
  space,
  member,
  isSelf,
  iAmOwner,
  onSelfLeave,
}: MemberRowProps) {
  const [rowError, setRowError] = useState<string | null>(null)
  const [leaveOpen, setLeaveOpen] = useState(false)
  const updateMember = useUpdateSpaceMember()
  const removeMember = useRemoveSpaceMember()

  const canEditRole = iAmOwner && !isSelf

  async function handleRoleChange(role: SpaceMember['role']) {
    if (role === member.role) return
    setRowError(null)
    try {
      await updateMember.mutateAsync({
        spaceId: space.id,
        userId: member.user_id,
        role,
      })
    } catch (err) {
      setRowError(memberErrorMessage(err))
    }
  }

  async function handleRemove() {
    setRowError(null)
    try {
      await removeMember.mutateAsync({
        spaceId: space.id,
        userId: member.user_id,
      })
    } catch (err) {
      setRowError(memberErrorMessage(err))
    }
  }

  return (
    <li
      className={cn(
        'm-0 list-none',
        'flex items-center gap-[var(--space-3)]',
        'px-[var(--space-3)] py-[var(--space-2)]',
        'rounded-[var(--radius-sm)]',
        'border border-[var(--border-subtle)] bg-[var(--surface-1)]',
      )}
    >
      <div className="flex-1 min-w-0 flex flex-col gap-[2px]">
        <div className="flex items-center gap-[var(--space-2)] min-w-0 flex-wrap">
          <span className="truncate text-[length:var(--text-sm)] text-[var(--text-primary)] font-medium font-[family-name:var(--font-sans)]">
            {member.username}
          </span>
          {isSelf ? <Badge variant="muted">You</Badge> : null}
        </div>
        {rowError ? (
          <span
            role="alert"
            className="text-[length:var(--text-xs)] text-[var(--danger)]"
          >
            {rowError}
          </span>
        ) : null}
      </div>

      {canEditRole ? (
        <div className="w-[6.5rem] shrink-0">
          <Select
            size="sm"
            aria-label={`Role for ${member.username}`}
            value={member.role}
            disabled={updateMember.isPending}
            onChange={(e) =>
              void handleRoleChange(e.target.value as SpaceMember['role'])
            }
          >
            <option value="owner">Owner</option>
            <option value="editor">Editor</option>
            <option value="viewer">Viewer</option>
          </Select>
        </div>
      ) : (
        <Badge variant={member.role === 'owner' ? 'accent' : 'muted'}>
          {ROLE_LABEL[member.role]}
        </Badge>
      )}

      {iAmOwner && !isSelf ? (
        <Button
          type="button"
          variant="ghost"
          size="sm"
          aria-label={`Remove ${member.username}`}
          onClick={() => void handleRemove()}
          disabled={removeMember.isPending}
          className="text-[var(--text-muted)] hover:text-[var(--danger)]"
        >
          <Trash2 width={14} height={14} />
        </Button>
      ) : null}

      {!iAmOwner && isSelf ? (
        <>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            onClick={() => setLeaveOpen(true)}
          >
            Leave space
          </Button>
          <LeaveSpaceConfirmDialog
            space={space}
            open={leaveOpen}
            onOpenChange={setLeaveOpen}
            onLeft={onSelfLeave}
          />
        </>
      ) : null}
    </li>
  )
}

interface LeaveSpaceConfirmDialogProps {
  space: Space
  open: boolean
  onOpenChange: (next: boolean) => void
  onLeft: () => void
}

function LeaveSpaceConfirmDialog({
  space,
  open,
  onOpenChange,
  onLeft,
}: LeaveSpaceConfirmDialogProps) {
  const me = useMe()
  const navigate = useNavigate()
  const removeMember = useRemoveSpaceMember()
  const [error, setError] = useState<string | null>(null)

  function handleClose(next: boolean) {
    if (!next) setError(null)
    onOpenChange(next)
  }

  async function handleConfirm() {
    if (me.data == null) return
    setError(null)
    try {
      await removeMember.mutateAsync({
        spaceId: space.id,
        userId: me.data.id,
        isSelf: true,
      })
      handleClose(false)
      onLeft()
      void navigate({ to: '/' })
    } catch (err) {
      setError(memberErrorMessage(err))
    }
  }

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Leave "{space.name}"?</DialogTitle>
          <DialogDescription>
            You'll lose access to this space and its pages. An owner will need
            to re-invite you to come back.
          </DialogDescription>
        </DialogHeader>
        {error ? (
          <p
            role="alert"
            className="m-0 text-[length:var(--text-xs)] text-[var(--danger)]"
          >
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
            onClick={() => void handleConfirm()}
            disabled={removeMember.isPending}
          >
            {removeMember.isPending ? 'Leaving…' : 'Leave space'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// One control, two ways in: pick someone who already has an account, or type
// an email address for someone who doesn't. Typing a full address the directory
// doesn't know offers an "invite" row in the same picker (see UserPicker), so
// this reads as a single "share with a person" action — the backend then either
// grants access straight away (the address has an account) or emails an
// invitation that lands the moment they sign up.
function AddMemberForm({ spaceId }: { spaceId: number }) {
  const [user, setUser] = useState<MentionUser | null>(null)
  const [email, setEmail] = useState<string | null>(null)
  const [role, setRole] = useState<SpaceMember['role']>('viewer')
  const [error, setError] = useState<string | null>(null)
  const [outcome, setOutcome] = useState<string | null>(null)
  const addMember = useAddSpaceMember()
  const shareByEmail = useShareSpaceByEmail(spaceId)
  const members = useSpaceMembers(spaceId)
  const busy = addMember.isPending || shareByEmail.isPending

  function reset() {
    setUser(null)
    setEmail(null)
    setRole('viewer')
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!user && !email) {
      setError('Pick someone, or type an email address to invite.')
      return
    }
    setError(null)
    setOutcome(null)
    try {
      if (email) {
        const res = await shareByEmail.mutateAsync({ email, role })
        setOutcome(
          res.member
            ? `Shared with ${res.member.username} — they have access now.`
            : `Invitation sent to ${email}.`,
        )
      } else if (user) {
        await addMember.mutateAsync({ spaceId, username: user.username, role })
        setOutcome(`Shared with ${user.username}.`)
      }
      reset()
    } catch (err) {
      setError(addMemberErrorMessage(err))
    }
  }

  return (
    <form
      onSubmit={handleSubmit}
      noValidate
      className={cn(
        'flex flex-col gap-[var(--space-2)]',
        'pt-[var(--space-3)]',
        'border-t border-[var(--border-subtle)]',
      )}
    >
      <label
        htmlFor={`add-member-username-${spaceId}`}
        className="text-[length:var(--text-xs)] uppercase tracking-wider text-[var(--text-muted)] font-[family-name:var(--font-sans)]"
      >
        Share with a person
      </label>
      <div className="flex items-start gap-[var(--space-2)]">
        <div className="flex-1 min-w-0">
          <UserPicker
            id={`add-member-username-${spaceId}`}
            placeholder="Search people, or type an email…"
            value={user}
            onChange={(u) => {
              setUser(u)
              setEmail(null)
              setError(null)
            }}
            emailValue={email}
            onInviteEmail={(addr) => {
              setEmail(addr)
              setUser(null)
              setError(null)
            }}
            inviteHint="Invite by email"
            invalid={error != null}
            excludeIds={(members.data ?? []).map((m) => m.user_id)}
          />
        </div>
        <div className="w-[6.5rem] shrink-0">
          <Select
            size="md"
            aria-label="Role for new member"
            value={role}
            onChange={(e) => setRole(e.target.value as SpaceMember['role'])}
          >
            <option value="owner">Owner</option>
            <option value="editor">Editor</option>
            <option value="viewer">Viewer</option>
          </Select>
        </div>
        <Button
          type="submit"
          variant="secondary"
          disabled={busy || (user == null && email == null)}
        >
          {email ? (
            <Mail width={14} height={14} />
          ) : (
            <UserPlus width={14} height={14} />
          )}
          <span>
            {busy ? 'Sharing…' : email ? 'Invite' : 'Add'}
          </span>
        </Button>
      </div>
      <p className="m-0 text-[length:var(--text-xs)] text-[var(--text-muted)]">
        No account yet? Type their email — they'll get an invitation and this
        space the moment they sign up.
      </p>
      {outcome ? (
        <p
          role="status"
          className="m-0 text-[length:var(--text-xs)] text-[var(--accent)]"
        >
          {outcome}
        </p>
      ) : null}
      {error ? (
        <p
          role="alert"
          className="m-0 text-[length:var(--text-xs)] text-[var(--danger)]"
        >
          {error}
        </p>
      ) : null}
    </form>
  )
}

// Invitations sent to addresses that have no tela account yet — they're not
// members until they sign up, so they'd otherwise be invisible. Owner-only
// (the endpoint is too).
function PendingInvites({ spaceId }: { spaceId: number }) {
  const invites = useSpaceInvites(spaceId)
  const revoke = useRevokeSpaceInvite(spaceId)
  const [error, setError] = useState<string | null>(null)

  const list = invites.data ?? []
  if (list.length === 0) return null

  async function handleRevoke(id: number) {
    setError(null)
    try {
      await revoke.mutateAsync(id)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Failed to revoke.')
    }
  }

  return (
    <div className="flex flex-col gap-[var(--space-2)] pt-[var(--space-3)]">
      <span className="text-[length:var(--text-xs)] uppercase tracking-wider text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
        Pending invitations
      </span>
      <ul className="m-0 p-0 list-none flex flex-col gap-[var(--space-1)]">
        {list.map((inv) => (
          <li
            key={inv.id}
            className={cn(
              'm-0 list-none flex items-center gap-[var(--space-3)]',
              'px-[var(--space-3)] py-[var(--space-2)]',
              'rounded-[var(--radius-sm)]',
              'border border-dashed border-[var(--border-subtle)]',
            )}
          >
            <Mail
              width={13}
              height={13}
              aria-hidden
              className="shrink-0 text-[var(--text-muted)]"
            />
            <div className="flex-1 min-w-0 flex flex-col gap-[1px]">
              <span className="truncate text-[length:var(--text-sm)] text-[var(--text-primary)] font-[family-name:var(--font-sans)]">
                {inv.email}
              </span>
              <span className="truncate text-[length:var(--text-xs)] text-[var(--text-muted)]">
                Invited — joins as {ROLE_LABEL[inv.role].toLowerCase()} when they
                sign up
              </span>
            </div>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              aria-label={`Revoke invitation to ${inv.email}`}
              onClick={() => void handleRevoke(inv.id)}
              disabled={revoke.isPending}
              className="text-[var(--text-muted)] hover:text-[var(--danger)]"
            >
              <Trash2 width={14} height={14} />
            </Button>
          </li>
        ))}
      </ul>
      {error ? (
        <p role="alert" className="m-0 text-[length:var(--text-xs)] text-[var(--danger)]">
          {error}
        </p>
      ) : null}
    </div>
  )
}

const GRANT_ROLE_LABEL: Record<SpaceGrant['role'], string> = {
  editor: 'Editor',
  viewer: 'Viewer',
}

// The authoritative answer to "who can access this space, and why" — resolves
// direct members + everyone reached via an org into one list with each person's
// effective (max) role and provenance. Read-only; editing happens in the
// Direct members / Organizations sections below. Reconciles the sidebar access
// count with what's actually shown.
function SpaceAccessSummary({ spaceId }: { spaceId: number }) {
  const access = useSpaceAccess(spaceId)
  const people = access.data ?? []

  return (
    <div className="flex flex-col gap-[var(--space-2)]">
      <span className="text-[length:var(--text-xs)] uppercase tracking-wider text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
        People with access{people.length > 0 ? ` (${people.length})` : ''}
      </span>
      {access.isLoading ? (
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
          Resolving access…
        </p>
      ) : access.isError ? (
        <p role="alert" className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]">
          Couldn't resolve access.
        </p>
      ) : people.length > 0 ? (
        <ul className="m-0 p-0 list-none flex flex-col gap-[var(--space-1)]">
          {people.map((p) => (
            <li
              key={p.user_id}
              className="m-0 list-none flex items-center gap-[var(--space-3)] px-[var(--space-2)] py-[var(--space-1)]"
            >
              <div className="flex-1 min-w-0 flex flex-col gap-[1px]">
                <span className="truncate text-[length:var(--text-sm)] text-[var(--text-primary)] font-[family-name:var(--font-sans)]">
                  {p.username}
                </span>
                <span className="truncate text-[length:var(--text-xs)] text-[var(--text-muted)]">
                  {provenanceLabel(p.sources)}
                </span>
              </div>
              <Badge variant={p.effective_role === 'owner' ? 'accent' : 'muted'}>
                {ROLE_LABEL[p.effective_role]}
              </Badge>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  )
}

// "Direct" and/or "via <Org/Group>, …" — explains every route a person has in.
function provenanceLabel(sources: AccessSource[]): string {
  const parts: string[] = []
  if (sources.some((s) => s.kind === 'direct')) parts.push('Direct')
  const vias = sources
    .filter((s) => s.kind !== 'direct' && s.name)
    .map((s) => s.name as string)
  if (vias.length > 0) parts.push(`via ${vias.join(', ')}`)
  return parts.join(' · ')
}

// Principal grants: share the whole space with an org or a group so every
// member gets access. The list is readable by any member; only owners get the
// controls.
function SpaceOrgGrants({
  space,
  iAmOwner,
}: {
  space: Space
  iAmOwner: boolean
}) {
  const grants = useSpaceGrants(space.id)

  // Nothing to show to non-owners when there are no grants yet — keeps the
  // dialog quiet for plain spaces.
  if (!iAmOwner && (grants.data == null || grants.data.length === 0)) {
    return null
  }

  return (
    <div className="flex flex-col gap-[var(--space-2)] pt-[var(--space-3)] border-t border-[var(--border-subtle)]">
      <span className="text-[length:var(--text-xs)] uppercase tracking-wider text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
        Organizations &amp; groups
      </span>

      {grants.isError ? (
        <p role="alert" className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]">
          Couldn't load shared access.
        </p>
      ) : grants.data && grants.data.length > 0 ? (
        <ul className="m-0 p-0 list-none flex flex-col gap-[var(--space-1)]">
          {grants.data.map((grant) => (
            <GrantRow
              key={grant.id}
              spaceId={space.id}
              grant={grant}
              iAmOwner={iAmOwner}
            />
          ))}
        </ul>
      ) : null}

      {iAmOwner ? (
        <AddGrantForm spaceId={space.id} granted={grants.data ?? []} />
      ) : null}
    </div>
  )
}

function GrantRow({
  spaceId,
  grant,
  iAmOwner,
}: {
  spaceId: number
  grant: SpaceGrant
  iAmOwner: boolean
}) {
  const [rowError, setRowError] = useState<string | null>(null)
  const updateGrant = useUpdateSpaceGrant()
  const removeGrant = useRemoveSpaceGrant()

  async function handleRoleChange(role: SpaceGrant['role']) {
    if (role === grant.role) return
    setRowError(null)
    try {
      await updateGrant.mutateAsync({ spaceId, grantId: grant.id, role })
    } catch (err) {
      setRowError(err instanceof ApiError ? err.message : 'Something went wrong.')
    }
  }

  async function handleRemove() {
    setRowError(null)
    try {
      await removeGrant.mutateAsync({ spaceId, grantId: grant.id })
    } catch (err) {
      setRowError(err instanceof ApiError ? err.message : 'Something went wrong.')
    }
  }

  return (
    <li
      className={cn(
        'm-0 list-none flex items-center gap-[var(--space-3)]',
        'px-[var(--space-3)] py-[var(--space-2)]',
        'rounded-[var(--radius-sm)]',
        'border border-[var(--border-subtle)] bg-[var(--surface-1)]',
      )}
    >
      <div className="flex-1 min-w-0 flex flex-col gap-[2px]">
        <div className="flex items-center gap-[var(--space-2)] min-w-0">
          {grant.principal_kind === 'group' ? (
            <Users width={13} height={13} className="text-[var(--text-muted)] shrink-0" />
          ) : (
            <Building2 width={13} height={13} className="text-[var(--text-muted)] shrink-0" />
          )}
          <span className="truncate text-[length:var(--text-sm)] text-[var(--text-primary)] font-medium font-[family-name:var(--font-sans)]">
            {grant.principal_name}
          </span>
        </div>
        <span className="truncate text-[length:var(--text-xs)] text-[var(--text-muted)]">
          {grant.principal_kind === 'group'
            ? `Group${grant.context_name ? ` · ${grant.context_name}` : ''}`
            : 'Organization'}
        </span>
        {rowError ? (
          <span role="alert" className="text-[length:var(--text-xs)] text-[var(--danger)]">
            {rowError}
          </span>
        ) : null}
      </div>

      {iAmOwner ? (
        <div className="w-[6.5rem] shrink-0">
          <Select
            size="sm"
            aria-label={`Role for ${grant.principal_name}`}
            value={grant.role}
            disabled={updateGrant.isPending}
            onChange={(e) => void handleRoleChange(e.target.value as SpaceGrant['role'])}
          >
            <option value="editor">Editor</option>
            <option value="viewer">Viewer</option>
          </Select>
        </div>
      ) : (
        <Badge variant="muted">{GRANT_ROLE_LABEL[grant.role]}</Badge>
      )}

      {iAmOwner ? (
        <Button
          type="button"
          variant="ghost"
          size="sm"
          aria-label={`Remove ${grant.principal_name}`}
          onClick={() => void handleRemove()}
          disabled={removeGrant.isPending}
          className="text-[var(--text-muted)] hover:text-[var(--danger)]"
        >
          <Trash2 width={14} height={14} />
        </Button>
      ) : null}
    </li>
  )
}

function AddGrantForm({
  spaceId,
  granted,
}: {
  spaceId: number
  granted: SpaceGrant[]
}) {
  const orgs = useOrgs()
  const groups = useMyGroups()
  // Encoded principal: "org:<id>" / "group:<id>".
  const [principal, setPrincipal] = useState('')
  const [role, setRole] = useState<SpaceGrant['role']>('viewer')
  const [error, setError] = useState<string | null>(null)
  const addGrant = useAddSpaceGrant()

  const grantedKeys = new Set(
    granted.map((g) => `${g.principal_kind}:${g.principal_id}`),
  )
  const orgOptions = (orgs.data ?? []).filter(
    (o) => !grantedKeys.has(`org:${o.id}`),
  )
  const groupOptions = (groups.data ?? []).filter(
    (g) => !grantedKeys.has(`group:${g.id}`),
  )

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (principal === '') {
      setError('Pick an org or group to share with.')
      return
    }
    const [kind, idStr] = principal.split(':')
    setError(null)
    try {
      await addGrant.mutateAsync({
        spaceId,
        principal_kind: kind as SpaceGrant['principal_kind'],
        principal_id: Number(idStr),
        role,
      })
      setPrincipal('')
      setRole('viewer')
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        setError('That org or group already has access.')
      } else if (err instanceof ApiError) {
        setError(err.message)
      } else {
        setError('Failed to share.')
      }
    }
  }

  // Nothing left to offer — stay quiet rather than show an empty picker.
  if (orgOptions.length === 0 && groupOptions.length === 0) {
    return null
  }

  return (
    <form onSubmit={handleSubmit} noValidate className="flex flex-col gap-[var(--space-2)]">
      <div className="flex items-start gap-[var(--space-2)]">
        <div className="flex-1 min-w-0">
          <Select
            size="md"
            aria-label="Org or group to share with"
            value={principal}
            onChange={(e) => setPrincipal(e.target.value)}
          >
            <option value="">Share with an org or group…</option>
            {orgOptions.length > 0 ? (
              <optgroup label="Organizations">
                {orgOptions.map((o) => (
                  <option key={`org:${o.id}`} value={`org:${o.id}`}>
                    {o.name}
                  </option>
                ))}
              </optgroup>
            ) : null}
            {groupOptions.length > 0 ? (
              <optgroup label="Groups">
                {groupOptions.map((g) => (
                  <option key={`group:${g.id}`} value={`group:${g.id}`}>
                    {g.name} — {g.org_name}
                  </option>
                ))}
              </optgroup>
            ) : null}
          </Select>
        </div>
        <div className="w-[6.5rem] shrink-0">
          <Select
            size="md"
            aria-label="Role"
            value={role}
            onChange={(e) => setRole(e.target.value as SpaceGrant['role'])}
          >
            <option value="editor">Editor</option>
            <option value="viewer">Viewer</option>
          </Select>
        </div>
        <Button
          type="submit"
          variant="secondary"
          disabled={addGrant.isPending || principal === ''}
        >
          <span>{addGrant.isPending ? 'Sharing…' : 'Share'}</span>
        </Button>
      </div>
      {error ? (
        <p role="alert" className="m-0 text-[length:var(--text-xs)] text-[var(--danger)]">
          {error}
        </p>
      ) : null}
    </form>
  )
}

function memberErrorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.code === 'last_owner') {
      return "Can't remove the last owner — promote someone else first."
    }
    return err.message
  }
  return 'Something went wrong. Try again.'
}

function addMemberErrorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 404) return 'User not found.'
    if (err.status === 409) return 'That person already has access.'
    if (err.code === 'bad_request') return err.message
    return err.message
  }
  return 'Failed to share.'
}

// PublicAccessSection — the Axis-2 space-level "publish to the web" control. A
// public space is readable by anyone with no login (docs/public-spaces.md).
// State is visible to every member (so anyone can tell the space is public);
// only an owner can flip it.
function PublicAccessSection({
  space,
  iAmOwner,
}: {
  space: Space
  iAmOwner: boolean
}) {
  const updateSpace = useUpdateSpace()
  const [error, setError] = useState<string | null>(null)
  const isPublic = space.visibility === 'public'

  async function toggle() {
    setError(null)
    try {
      await updateSpace.mutateAsync({
        id: space.id,
        visibility: isPublic ? 'private' : 'public',
      })
    } catch (err) {
      setError(
        err instanceof ApiError ? err.message : 'Failed to update visibility.',
      )
    }
  }

  return (
    <div className="flex flex-col gap-[var(--space-2)] pt-[var(--space-3)] border-t border-[var(--border-subtle)]">
      <span className="text-[length:var(--text-xs)] uppercase tracking-wider text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
        Public access
      </span>
      <div className="flex items-start gap-[var(--space-3)]">
        <span
          className="mt-[2px] text-[var(--text-muted)]"
          aria-hidden
        >
          {isPublic ? <Globe size="1.1em" /> : <Lock size="1.1em" />}
        </span>
        <div className="flex-1 min-w-0">
          <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-primary)]">
            {isPublic ? 'Public on the web' : 'Private to members'}
          </p>
          <p className="m-0 text-[length:var(--text-xs)] text-[var(--text-muted)]">
            {isPublic
              ? 'Anyone can read every page in this space — no login. Editing stays members-only.'
              : 'Only members can read this space.'}
          </p>
          {isPublic ? (
            <a
              href={`/public/spaces/${space.id}`}
              target="_blank"
              rel="noopener noreferrer"
              className="mt-[var(--space-1)] inline-block truncate text-[length:var(--text-xs)] text-[var(--accent)] no-underline hover:underline"
            >
              {`${window.location.origin}/public/spaces/${space.id}`}
            </a>
          ) : null}
        </div>
        {iAmOwner ? (
          <Button
            type="button"
            variant={isPublic ? 'ghost' : 'primary'}
            size="sm"
            onClick={() => void toggle()}
            disabled={updateSpace.isPending}
          >
            {isPublic ? 'Make private' : 'Make public'}
          </Button>
        ) : null}
      </div>
      {error ? (
        <p
          role="alert"
          className="m-0 text-[length:var(--text-xs)] text-[var(--danger)]"
        >
          {error}
        </p>
      ) : null}

      {isPublic && iAmOwner ? <BlogDescriptionField space={space} /> : null}
    </div>
  )
}

// Owner-only: hand the whole space to an organization. The space keeps its
// human owner row; the org gets it as its owning account (and editor access).
// Picks a target org, then confirms before the irreversible-feeling move.
function TransferOwnershipSection({ space }: { space: Space }) {
  const orgs = useOrgs()
  const [orgId, setOrgId] = useState('')
  const [confirmOpen, setConfirmOpen] = useState(false)

  const orgOptions = orgs.data ?? []
  const targetOrg = orgOptions.find((o) => String(o.id) === orgId)

  // No orgs to transfer into — stay quiet rather than show an empty picker.
  if (orgOptions.length === 0) return null

  return (
    <div className="flex flex-col gap-[var(--space-2)] pt-[var(--space-3)] border-t border-[var(--border-subtle)]">
      <span className="text-[length:var(--text-xs)] uppercase tracking-wider text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
        Transfer ownership to an organization
      </span>
      <p className="m-0 text-[length:var(--text-xs)] text-[var(--text-muted)]">
        The organization becomes this space's owning account and gets editor
        access. You stay the human owner.
      </p>
      <div className="flex items-start gap-[var(--space-2)]">
        <div className="flex-1 min-w-0">
          <Select
            size="md"
            aria-label="Organization to transfer to"
            value={orgId}
            onChange={(e) => setOrgId(e.target.value)}
          >
            <option value="">Choose an organization…</option>
            {orgOptions.map((o) => (
              <option key={o.id} value={String(o.id)}>
                {o.name}
              </option>
            ))}
          </Select>
        </div>
        <Button
          type="button"
          variant="secondary"
          disabled={orgId === ''}
          onClick={() => setConfirmOpen(true)}
        >
          <Building2 width={14} height={14} />
          <span>Transfer</span>
        </Button>
      </div>
      {targetOrg ? (
        <TransferConfirmDialog
          space={space}
          org={targetOrg}
          open={confirmOpen}
          onOpenChange={setConfirmOpen}
          onTransferred={() => setOrgId('')}
        />
      ) : null}
    </div>
  )
}

function TransferConfirmDialog({
  space,
  org,
  open,
  onOpenChange,
  onTransferred,
}: {
  space: Space
  org: Org
  open: boolean
  onOpenChange: (next: boolean) => void
  onTransferred: () => void
}) {
  const transfer = useTransferSpace()
  const [error, setError] = useState<string | null>(null)

  function handleClose(next: boolean) {
    if (!next) setError(null)
    onOpenChange(next)
  }

  async function handleConfirm() {
    setError(null)
    try {
      await transfer.mutateAsync({ id: space.id, org_id: org.id })
      handleClose(false)
      onTransferred()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Failed to transfer.')
    }
  }

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Transfer "{space.name}" to {org.name}?</DialogTitle>
          <DialogDescription>
            {org.name} becomes the owning account for this space and gets editor
            access. You'll remain its human owner.
          </DialogDescription>
        </DialogHeader>
        {error ? (
          <p role="alert" className="m-0 text-[length:var(--text-xs)] text-[var(--danger)]">
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
            variant="primary"
            onClick={() => void handleConfirm()}
            disabled={transfer.isPending}
          >
            {transfer.isPending ? 'Transferring…' : 'Transfer'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// The blog standfirst for a public space — shown under the space name on its
// front page. Owner-edited inline here, saved on blur when it changed.
function BlogDescriptionField({ space }: { space: Space }) {
  const updateSpace = useUpdateSpace()
  const [value, setValue] = useState(space.description ?? '')
  const [error, setError] = useState<string | null>(null)

  async function save() {
    const next = value.trim()
    if (next === (space.description ?? '')) return
    setError(null)
    try {
      await updateSpace.mutateAsync({ id: space.id, description: next })
    } catch (err) {
      setError(
        err instanceof ApiError ? err.message : 'Failed to save description.',
      )
    }
  }

  return (
    <div className="flex flex-col gap-[var(--space-1)] pl-[calc(1.1em+var(--space-3))]">
      <label
        htmlFor={`blog-desc-${space.id}`}
        className="text-[length:var(--text-xs)] text-[var(--text-muted)]"
      >
        Blog description
      </label>
      <TextArea
        id={`blog-desc-${space.id}`}
        value={value}
        onChange={(e) => setValue(e.target.value.slice(0, 280))}
        onBlur={() => void save()}
        rows={2}
        placeholder="A line that introduces this blog — shown under its title."
        size="sm"
        font="sans"
      />
      <div className="flex items-center justify-between">
        <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">
          {updateSpace.isPending ? 'Saving…' : 'Shown on the front page'}
        </span>
        <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">
          {value.length}/280
        </span>
      </div>
      {error ? (
        <p role="alert" className="m-0 text-[length:var(--text-xs)] text-[var(--danger)]">
          {error}
        </p>
      ) : null}
    </div>
  )
}
