import { useEffect, useMemo, useRef, useState } from 'react'
import { Link } from '@tanstack/react-router'
import {
  ArrowLeft,
  Boxes,
  Copy,
  Globe,
  HardDrive,
  LogIn,
  Palette,
  Sparkles,
  Trash2,
  Upload,
  UserPlus,
  Users,
} from 'lucide-react'
import { ApiError } from '../../lib/api'
import { useMe } from '../../lib/queries/auth'
import { useOrgUsage } from '../../lib/queries/billing'
import { formatBytes } from '../../lib/format'
import { Metric } from './SettingsBillingTab'
import { useOrgAudit } from '../../lib/queries/org-audit'
import { useOrgDomains } from '../../lib/queries/org-domains'
import {
  useAddOrgMember,
  useDeleteOrg,
  useOrgMembers,
  useOrgs,
  useRemoveOrgMember,
  useUpdateOrgMember,
} from '../../lib/queries/orgs'
import { useCreateOrgInvite, useOrgInvites, useRevokeOrgInvite } from '../../lib/queries/invites'
import {
  useAddGroupMember,
  useCreateGroup,
  useDeleteGroup,
  useGroupMembers,
  useOrgGroups,
  useRemoveGroupMember,
} from '../../lib/queries/groups'
import { useDeleteOrgSSO, useOrgSSO, usePutOrgSSO } from '../../lib/queries/org-sso'
import {
  useAddOrgHostname,
  useAdminDomainLogin,
  useDeleteOrgHostname,
  useOrgHostnameHealth,
  useOrgHostnames,
  useVerifyOrgHostname,
} from '../../lib/queries/org-hostnames'
import {
  useOrgBranding,
  usePutOrgBranding,
  useUploadOrgLogo,
  useDeleteOrgLogo,
} from '../../lib/queries/org-branding'
import {
  useOrgLoginSettings,
  usePutOrgLoginSettings,
} from '../../lib/queries/org-login-settings'
import type {
  Group,
  HostnameHealth,
  Org,
  OrgHostname,
  OrgMember,
  OrgRole,
} from '../../lib/types'
import { AuditRow } from './SettingsAuditTab'
import { PlanTierSelect } from './PlanTierSelect'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Checkbox } from '../ui/checkbox'
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
import { Select } from '../ui/select'
import { UserPicker } from './UserPicker'
import type { MentionUser } from '../../lib/queries/users'
import { cn } from '../../lib/utils'

// Dedicated per-org management page (route /settings/orgs/$orgId). Replaces the
// old crowded "Members of X" dialog: the org's people, groups, single sign-on,
// and activity each get their own sub-tab with room to grow. Reached from the
// Organizations list in Settings. Instance admins see every org plus the SSO
// tab and the danger zone; org admins see only the orgs they administer and
// manage members/groups/activity.

export function OrgManageView({ orgId }: { orgId: number }) {
  const me = useMe()
  const orgs = useOrgs()
  const isInstance = me.data?.is_instance_admin ?? false
  const org = orgs.data?.find((o) => o.id === orgId)
  const canManage = isInstance || org?.my_role === 'admin'

  if (orgs.isLoading || me.isLoading) {
    return (
      <div className="flex-1 overflow-y-auto p-[var(--space-7)]">
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">Loading organization…</p>
      </div>
    )
  }
  if (!org || !canManage) {
    return (
      <div className="flex-1 overflow-y-auto">
        <div className="max-w-[48rem] w-full mx-auto p-[var(--space-7)] flex flex-col gap-[var(--space-4)]">
          <Link
            to="/settings"
            search={{ tab: 'orgs' }}
            className="inline-flex items-center gap-[var(--space-2)] text-[length:var(--text-sm)] text-[var(--text-muted)] hover:text-[var(--text-primary)] no-underline"
          >
            <ArrowLeft width={14} height={14} />
            Organizations
          </Link>
          <h1 className="m-0 font-[family-name:var(--font-sans)] text-[length:var(--text-2xl)] text-[var(--text-primary)]">
            {org ? 'No access' : 'Organization not found'}
          </h1>
          <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
            {org
              ? "You don't administer this organization."
              : "This organization doesn't exist or you can't see it."}
          </p>
        </div>
      </div>
    )
  }
  return <OrgManageBody org={org} isInstance={isInstance} />
}

interface OrgSection {
  id: string
  label: string
  render: () => React.ReactNode
}

// First-class per-org management: a left section-nav (like the instance-admin
// Settings) + a content pane, instead of cramped top-tabs. Same shape whether
// you reached it as an org admin (your orgs) or an instance admin (every org).
function OrgManageBody({ org, isInstance }: { org: Org; isInstance: boolean }) {
  const sections = useMemo<OrgSection[]>(() => {
    const s: OrgSection[] = [
      { id: 'overview', label: 'Overview', render: () => <OrgOverviewPanel org={org} isInstance={isInstance} /> },
      { id: 'members', label: 'Members', render: () => <MembersPanel org={org} /> },
      { id: 'groups', label: 'Groups', render: () => <GroupsPanel org={org} /> },
    ]
    s.push({ id: 'branding', label: 'Branding', render: () => <BrandingSection orgId={org.id} /> })
    if (isInstance) s.push({ id: 'sso', label: 'Single sign-on', render: () => <SSOPanel org={org} /> })
    s.push({ id: 'domains', label: 'Custom domains', render: () => <CustomDomainsPanel org={org} /> })
    s.push({ id: 'activity', label: 'Activity', render: () => <OrgActivityPanel orgId={org.id} /> })
    if (isInstance) s.push({ id: 'danger', label: 'Danger zone', render: () => <DangerZone org={org} /> })
    return s
  }, [org, isInstance])
  const [activeId, setActiveId] = useState('overview')
  const active = sections.find((s) => s.id === activeId) ?? sections[0]

  return (
    <div className="flex-1 flex min-h-0">
      <nav
        aria-label="Organization sections"
        className="shrink-0 w-[var(--space-8)] sm:w-[15rem] border-r border-[var(--border-subtle)] bg-[var(--surface-2)] py-[var(--space-4)] px-[var(--space-3)] flex flex-col gap-[var(--space-2)]"
      >
        <Link
          to="/settings"
          search={{ tab: 'orgs' }}
          className="hidden sm:inline-flex items-center gap-[var(--space-2)] px-[var(--space-2)] text-[length:var(--text-sm)] text-[var(--text-muted)] hover:text-[var(--text-primary)] no-underline"
        >
          <ArrowLeft width={14} height={14} />
          Organizations
        </Link>
        <div className="hidden sm:flex flex-col gap-[var(--space-1)] px-[var(--space-2)] pb-[var(--space-2)] border-b border-[var(--border-subtle)]">
          <span className="truncate font-medium text-[var(--text-primary)] font-[family-name:var(--font-sans)]">
            {org.name}
          </span>
          <span className="text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-mono)]">
            {org.slug}
          </span>
        </div>
        <div className="flex flex-col gap-[var(--space-1)]">
          {sections.map((s) => (
            <Button
              key={s.id}
              type="button"
              variant="ghost"
              size="sm"
              className={cn(
                'w-full justify-start',
                s.id === active.id && 'bg-[var(--surface-3)] text-[var(--text-primary)] font-medium',
                s.id === 'danger' && 'text-[var(--danger)]',
              )}
              aria-current={s.id === active.id ? 'page' : undefined}
              onClick={() => setActiveId(s.id)}
            >
              {s.label}
            </Button>
          ))}
        </div>
      </nav>
      <div className="flex-1 overflow-y-auto">
        <div className="max-w-[48rem] w-full mx-auto p-[var(--space-7)] flex flex-col gap-[var(--space-6)]">
          <header className="flex items-center gap-[var(--space-2)]">
            <h1 className="m-0 font-[family-name:var(--font-sans)] text-[length:var(--text-2xl)] leading-[var(--leading-tight)] text-[var(--text-primary)]">
              {active.label}
            </h1>
          </header>
          {active.render()}
        </div>
      </div>
    </div>
  )
}

// Plan + live usage for the org — seats, AI calls, attachments, spaces vs the
// org plan's caps. Instance admins can change the tier inline here.
function OrgOverviewPanel({ org, isInstance }: { org: Org; isInstance: boolean }) {
  const usage = useOrgUsage(org.id)
  const u = usage.data
  return (
    <div className="flex flex-col gap-[var(--space-5)]">
      <div className="flex items-center justify-between gap-[var(--space-3)] flex-wrap">
        <div className="flex items-center gap-[var(--space-2)]">
          <Badge variant="muted">
            {org.member_count} {org.member_count === 1 ? 'member' : 'members'}
          </Badge>
          {u ? <Badge variant="accent">{u.plan.name}</Badge> : null}
        </div>
        {isInstance ? (
          <div className="flex items-center gap-[var(--space-2)]">
            <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">Admin · set tier</span>
            <PlanTierSelect
              accountKind="org"
              accountId={org.id}
              currentKey={org.plan_key}
              className="w-[9rem]"
            />
          </div>
        ) : null}
      </div>

      {usage.isLoading ? (
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">Loading usage…</p>
      ) : u ? (
        <div className="flex flex-col gap-[var(--space-4)]">
          <Metric icon={<Users width={15} height={15} />} label="Members" used={u.usage.members ?? org.member_count} max={u.plan.max_members} />
          <Metric icon={<Sparkles width={15} height={15} />} label="AI calls / mo" used={u.usage.llm_calls} max={u.plan.max_llm_calls_per_month} />
          <Metric icon={<HardDrive width={15} height={15} />} label="Attachments" used={u.usage.storage_bytes} max={u.plan.max_storage_bytes} format={formatBytes} />
          <Metric icon={<Boxes width={15} height={15} />} label="Spaces" used={u.usage.spaces} max={u.plan.max_spaces} />
        </div>
      ) : (
        <p role="alert" className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]">Couldn't load usage.</p>
      )}
    </div>
  )
}

function MembersPanel({ org }: { org: Org }) {
  const members = useOrgMembers(org.id)
  return (
    <div className="flex flex-col gap-[var(--space-4)]">
      <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)] leading-[var(--leading-relaxed)]">
        Admins manage members and settings. Members just belong — sharing a space
        with this org gives all of them access.
      </p>
      {members.isLoading ? (
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
          Loading members…
        </p>
      ) : members.isError ? (
        <p role="alert" className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]">
          Couldn't load members.
        </p>
      ) : members.data && members.data.length > 0 ? (
        <ul className="m-0 p-0 list-none flex flex-col gap-[var(--space-1)]">
          {members.data.map((member) => (
            <OrgMemberRow key={member.user_id} orgId={org.id} member={member} />
          ))}
        </ul>
      ) : (
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
          No members yet.
        </p>
      )}
      <AddOrgMemberForm orgId={org.id} />
      <OrgInvitesSection orgId={org.id} orgName={org.name} />
    </div>
  )
}

// Invite teammates by email — the self-serve onboarding path. They get a link to
// join and land in the org once they accept or sign up with the invited address.
function OrgInvitesSection({ orgId, orgName }: { orgId: number; orgName: string }) {
  const invites = useOrgInvites(orgId)
  const createInvite = useCreateOrgInvite(orgId)
  const revoke = useRevokeOrgInvite(orgId)
  const [email, setEmail] = useState('')
  const [role, setRole] = useState<OrgRole>('member')
  const [error, setError] = useState<string | null>(null)
  const [sent, setSent] = useState<string | null>(null)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setError(null)
    setSent(null)
    const em = email.trim()
    if (!em) return
    try {
      await createInvite.mutateAsync({ email: em, org_role: role })
      setSent(em)
      setEmail('')
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not send the invitation.')
    }
  }

  return (
    <div className="flex flex-col gap-[var(--space-3)] border-t border-[var(--border-subtle)] pt-[var(--space-4)]">
      <div className="flex flex-col gap-[var(--space-1)]">
        <h3 className="m-0 text-[length:var(--text-sm)] font-medium text-[var(--text-primary)]">
          Invite teammates
        </h3>
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
          Invite by email — they'll get a link to join {orgName}, even if they don't have a tela
          account yet.
        </p>
      </div>
      <form onSubmit={submit} className="flex flex-wrap items-center gap-[var(--space-2)]">
        <Input
          type="email"
          placeholder="teammate@company.com"
          value={email}
          onChange={(e: React.ChangeEvent<HTMLInputElement>) => setEmail(e.target.value)}
          className="flex-1 min-w-[12rem]"
        />
        <Select value={role} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setRole(e.target.value as OrgRole)}>
          <option value="member">Member</option>
          <option value="admin">Admin</option>
        </Select>
        <Button type="submit" variant="secondary" size="sm" disabled={createInvite.isPending || email.trim() === ''}>
          Send invite
        </Button>
      </form>
      {error ? <p className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]">{error}</p> : null}
      {sent ? (
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">Invitation sent to {sent}.</p>
      ) : null}
      {invites.data && invites.data.length > 0 ? (
        <ul className="m-0 p-0 list-none flex flex-col gap-[var(--space-1)]">
          {invites.data.map((inv) => (
            <li
              key={inv.id}
              className="m-0 flex items-center gap-[var(--space-2)] px-[var(--space-3)] py-[var(--space-2)] rounded-[var(--radius-sm)] border border-[var(--border-subtle)] bg-[var(--surface-1)]"
            >
              <span className="flex-1 min-w-0 truncate text-[length:var(--text-sm)] text-[var(--text-primary)]">
                {inv.email}
                <span className="text-[var(--text-muted)]"> · invited · {inv.org_role}</span>
              </span>
              <Button variant="ghost" size="sm" onClick={() => revoke.mutate(inv.id)}>
                Revoke
              </Button>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  )
}

function OrgMemberRow({ orgId, member }: { orgId: number; member: OrgMember }) {
  const [rowError, setRowError] = useState<string | null>(null)
  const updateMember = useUpdateOrgMember()
  const removeMember = useRemoveOrgMember()

  async function handleRoleChange(org_role: OrgRole) {
    if (org_role === member.org_role) return
    setRowError(null)
    try {
      await updateMember.mutateAsync({ orgId, userId: member.user_id, org_role })
    } catch (err) {
      setRowError(orgMemberErrorMessage(err))
    }
  }

  async function handleRemove() {
    setRowError(null)
    try {
      await removeMember.mutateAsync({ orgId, userId: member.user_id })
    } catch (err) {
      setRowError(orgMemberErrorMessage(err))
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
        <span className="truncate text-[length:var(--text-sm)] text-[var(--text-primary)] font-medium font-[family-name:var(--font-sans)]">
          {member.username}
        </span>
        {member.email ? (
          <span className="truncate text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
            {member.email}
          </span>
        ) : null}
        {rowError ? (
          <span role="alert" className="text-[length:var(--text-xs)] text-[var(--danger)]">
            {rowError}
          </span>
        ) : null}
      </div>

      <div className="w-[6.5rem] shrink-0">
        <Select
          size="sm"
          aria-label={`Role for ${member.username}`}
          value={member.org_role}
          disabled={updateMember.isPending}
          onChange={(e) => void handleRoleChange(e.target.value as OrgRole)}
        >
          <option value="admin">Admin</option>
          <option value="member">Member</option>
        </Select>
      </div>

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
    </li>
  )
}

function AddOrgMemberForm({ orgId }: { orgId: number }) {
  const [user, setUser] = useState<MentionUser | null>(null)
  const [role, setRole] = useState<OrgRole>('member')
  const [error, setError] = useState<string | null>(null)
  const addMember = useAddOrgMember()
  const members = useOrgMembers(orgId)

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!user) {
      setError('Pick a user to add.')
      return
    }
    setError(null)
    try {
      await addMember.mutateAsync({ orgId, identifier: user.username, org_role: role })
      setUser(null)
      setRole('member')
    } catch (err) {
      setError(addOrgMemberErrorMessage(err))
    }
  }

  return (
    <form
      onSubmit={handleSubmit}
      noValidate
      className={cn(
        'flex flex-col gap-[var(--space-2)]',
        'pt-[var(--space-4)]',
        'border-t border-[var(--border-subtle)]',
      )}
    >
      <label
        htmlFor={`add-org-member-${orgId}`}
        className="text-[length:var(--text-xs)] uppercase tracking-wider text-[var(--text-muted)] font-[family-name:var(--font-sans)]"
      >
        Add a member
      </label>
      <div className="flex items-start gap-[var(--space-2)]">
        <div className="flex-1 min-w-0">
          <UserPicker
            id={`add-org-member-${orgId}`}
            value={user}
            onChange={(u) => {
              setUser(u)
              setError(null)
            }}
            invalid={error != null}
            excludeIds={(members.data ?? []).map((m) => m.user_id)}
          />
        </div>
        <div className="w-[6.5rem] shrink-0">
          <Select
            size="md"
            aria-label="Role for new member"
            value={role}
            onChange={(e) => setRole(e.target.value as OrgRole)}
          >
            <option value="admin">Admin</option>
            <option value="member">Member</option>
          </Select>
        </div>
        <Button
          type="submit"
          variant="secondary"
          disabled={addMember.isPending || user == null}
        >
          <UserPlus width={14} height={14} />
          <span>{addMember.isPending ? 'Adding…' : 'Add'}</span>
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

function GroupsPanel({ org }: { org: Org }) {
  const groups = useOrgGroups(org.id)
  const [name, setName] = useState('')
  const [error, setError] = useState<string | null>(null)
  const createGroup = useCreateGroup()

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    const trimmed = name.trim()
    if (!trimmed) {
      setError('Group name is required.')
      return
    }
    setError(null)
    try {
      await createGroup.mutateAsync({ orgId: org.id, name: trimmed })
      setName('')
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        setError('A group with that name already exists.')
      } else if (err instanceof ApiError) {
        setError(err.message)
      } else {
        setError('Failed to create group.')
      }
    }
  }

  return (
    <div className="flex flex-col gap-[var(--space-4)]">
      <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)] leading-[var(--leading-relaxed)]">
        Groups are sub-teams within the org. Share a space with a group to give
        just part of the org access.
      </p>

      {groups.data && groups.data.length > 0 ? (
        <ul className="m-0 p-0 list-none flex flex-col gap-[var(--space-1)]">
          {groups.data.map((g) => (
            <GroupRow key={g.id} org={org} group={g} />
          ))}
        </ul>
      ) : (
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
          No groups yet.
        </p>
      )}

      <form
        onSubmit={handleSubmit}
        noValidate
        className="flex items-start gap-[var(--space-2)] pt-[var(--space-4)] border-t border-[var(--border-subtle)]"
      >
        <div className="flex-1 min-w-0">
          <Input
            placeholder="New group name (e.g. Engineering)"
            autoComplete="off"
            value={name}
            onChange={(e) => setName(e.target.value)}
            aria-invalid={error != null}
          />
        </div>
        <Button type="submit" variant="secondary" disabled={createGroup.isPending || name.trim() === ''}>
          <Users width={14} height={14} />
          <span>{createGroup.isPending ? 'Adding…' : 'Add group'}</span>
        </Button>
      </form>
      {error ? (
        <p role="alert" className="m-0 text-[length:var(--text-xs)] text-[var(--danger)]">
          {error}
        </p>
      ) : null}
    </div>
  )
}

function GroupRow({ org, group }: { org: Org; group: Group }) {
  const [membersOpen, setMembersOpen] = useState(false)
  const deleteGroup = useDeleteGroup()

  return (
    <li
      className={cn(
        'm-0 list-none flex items-center gap-[var(--space-3)]',
        'px-[var(--space-3)] py-[var(--space-2)]',
        'rounded-[var(--radius-sm)]',
        'border border-[var(--border-subtle)] bg-[var(--surface-1)]',
      )}
    >
      <div className="flex-1 min-w-0 flex items-center gap-[var(--space-2)] flex-wrap">
        <span className="truncate text-[length:var(--text-sm)] text-[var(--text-primary)] font-medium font-[family-name:var(--font-sans)]">
          {group.name}
        </span>
        <Badge variant="muted">
          {group.member_count} {group.member_count === 1 ? 'member' : 'members'}
        </Badge>
      </div>
      <Button type="button" variant="ghost" size="sm" onClick={() => setMembersOpen(true)}>
        Members
      </Button>
      <Button
        type="button"
        variant="ghost"
        size="sm"
        aria-label={`Delete ${group.name}`}
        onClick={() => void deleteGroup.mutateAsync({ orgId: org.id, groupId: group.id })}
        disabled={deleteGroup.isPending}
        className="text-[var(--text-muted)] hover:text-[var(--danger)]"
      >
        <Trash2 width={14} height={14} />
      </Button>
      <GroupMembersDialog
        org={org}
        group={group}
        open={membersOpen}
        onOpenChange={setMembersOpen}
      />
    </li>
  )
}

function GroupMembersDialog({
  org,
  group,
  open,
  onOpenChange,
}: {
  org: Org
  group: Group
  open: boolean
  onOpenChange: (next: boolean) => void
}) {
  const members = useGroupMembers(open ? org.id : null, open ? group.id : null)
  const orgMembers = useOrgMembers(open ? org.id : null)
  const [user, setUser] = useState<MentionUser | null>(null)
  const [error, setError] = useState<string | null>(null)
  const addMember = useAddGroupMember()
  const removeMember = useRemoveGroupMember()

  async function handleAdd(e: React.FormEvent) {
    e.preventDefault()
    if (!user) {
      setError('Pick a user to add.')
      return
    }
    setError(null)
    try {
      await addMember.mutateAsync({ orgId: org.id, groupId: group.id, identifier: user.username })
      setUser(null)
    } catch (err) {
      if (err instanceof ApiError && err.code === 'not_org_member') {
        setError('Add them to the org first — group members must be org members.')
      } else if (err instanceof ApiError && err.status === 404) {
        setError('User not found.')
      } else if (err instanceof ApiError && err.status === 409) {
        setError('Already in this group.')
      } else if (err instanceof ApiError) {
        setError(err.message)
      } else {
        setError('Failed to add member.')
      }
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>"{group.name}" members</DialogTitle>
          <DialogDescription>
            Group members must already belong to {org.name}. Sharing a space with
            this group gives all of them access.
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-col gap-[var(--space-3)]">
          {members.isLoading ? (
            <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
              Loading members…
            </p>
          ) : members.data && members.data.length > 0 ? (
            <ul className="m-0 p-0 list-none flex flex-col gap-[var(--space-1)]">
              {members.data.map((m) => (
                <li
                  key={m.user_id}
                  className={cn(
                    'm-0 list-none flex items-center gap-[var(--space-3)]',
                    'px-[var(--space-3)] py-[var(--space-2)]',
                    'rounded-[var(--radius-sm)]',
                    'border border-[var(--border-subtle)] bg-[var(--surface-1)]',
                  )}
                >
                  <div className="flex-1 min-w-0 flex flex-col gap-[1px]">
                    <span className="truncate text-[length:var(--text-sm)] text-[var(--text-primary)] font-medium font-[family-name:var(--font-sans)]">
                      {m.username}
                    </span>
                    {m.email ? (
                      <span className="truncate text-[length:var(--text-xs)] text-[var(--text-muted)]">
                        {m.email}
                      </span>
                    ) : null}
                  </div>
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    aria-label={`Remove ${m.username}`}
                    onClick={() =>
                      void removeMember.mutateAsync({
                        orgId: org.id,
                        groupId: group.id,
                        userId: m.user_id,
                      })
                    }
                    disabled={removeMember.isPending}
                    className="text-[var(--text-muted)] hover:text-[var(--danger)]"
                  >
                    <Trash2 width={14} height={14} />
                  </Button>
                </li>
              ))}
            </ul>
          ) : (
            <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
              No members yet.
            </p>
          )}

          <form
            onSubmit={handleAdd}
            noValidate
            className="flex items-start gap-[var(--space-2)] pt-[var(--space-3)] border-t border-[var(--border-subtle)]"
          >
            <div className="flex-1 min-w-0">
              <UserPicker
                placeholder="Search org members…"
                value={user}
                onChange={(u) => {
                  setUser(u)
                  setError(null)
                }}
                invalid={error != null}
                restrictToIds={(orgMembers.data ?? []).map((m) => m.user_id)}
                excludeIds={(members.data ?? []).map((m) => m.user_id)}
              />
            </div>
            <Button type="submit" variant="secondary" disabled={addMember.isPending || user == null}>
              <UserPlus width={14} height={14} />
              <span>{addMember.isPending ? 'Adding…' : 'Add'}</span>
            </Button>
          </form>
          {error ? (
            <p role="alert" className="m-0 text-[length:var(--text-xs)] text-[var(--danger)]">
              {error}
            </p>
          ) : null}
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

// OrgSSOSection configures a single OIDC connection for the org (instance-admin
// only). The client_secret is write-only: it's never returned by the API, so it
// must be re-entered on each save. enforced=on refuses password login for the
// org's auto-join domains, funnelling those users through SSO.
function SSOPanel({ org }: { org: Org }) {
  const orgId = org.id
  const sso = useOrgSSO(orgId)
  const put = usePutOrgSSO(orgId)
  const del = useDeleteOrgSSO(orgId)
  const domains = useOrgDomains()
  const orgDomains = (domains.data ?? []).filter((d) => d.org_id === orgId)

  const [issuer, setIssuer] = useState('')
  const [clientId, setClientId] = useState('')
  const [clientSecret, setClientSecret] = useState('')
  const [enforced, setEnforced] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)
  // Prefill issuer/client_id/enforced from the loaded connection once.
  const [hydratedFor, setHydratedFor] = useState<number | null>(null)
  if (sso.data && hydratedFor !== orgId) {
    setIssuer(sso.data.issuer)
    setClientId(sso.data.client_id)
    setEnforced(sso.data.enforced)
    setHydratedFor(orgId)
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!issuer.trim() || !clientId.trim() || !clientSecret.trim()) {
      setError('Issuer, client ID and client secret are all required.')
      return
    }
    setError(null)
    setSaved(false)
    try {
      await put.mutateAsync({
        issuer: issuer.trim(),
        client_id: clientId.trim(),
        client_secret: clientSecret.trim(),
        enforced,
      })
      setClientSecret('')
      setSaved(true)
    } catch (err) {
      if (err instanceof ApiError && err.code === 'issuer_unreachable') {
        setError("Couldn't run OIDC discovery against that issuer URL.")
      } else if (err instanceof ApiError) {
        setError(err.message)
      } else {
        setError('Failed to save SSO connection.')
      }
    }
  }

  async function handleRemove() {
    setError(null)
    try {
      await del.mutateAsync()
      setIssuer('')
      setClientId('')
      setClientSecret('')
      setEnforced(false)
      setHydratedFor(orgId)
    } catch {
      setError('Failed to remove SSO connection.')
    }
  }

  return (
    <div className="flex flex-col gap-[var(--space-4)]">
      <header className="flex flex-col gap-[var(--space-1)]">
        <div className="flex items-center gap-[var(--space-2)]">
          <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)] leading-[var(--leading-relaxed)]">
            Members sign in via this org's OIDC identity provider.
          </p>
          {sso.data?.configured ? <Badge variant="muted">configured</Badge> : null}
        </div>
      </header>

      {/* Auto-join domains drive who lands on this connection. Read-only here —
          mapping is managed in Settings → Organizations. */}
      <div className="flex flex-col gap-[var(--space-2)] rounded-[var(--radius-sm)] border border-[var(--border-subtle)] bg-[var(--surface-2)] p-[var(--space-3)]">
        <span className="flex items-center gap-[var(--space-2)] text-[length:var(--text-xs)] uppercase tracking-wider text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
          <Globe width={12} height={12} />
          Auto-join domains
        </span>
        {orgDomains.length > 0 ? (
          <div className="flex flex-wrap gap-[var(--space-2)]">
            {orgDomains.map((d) => (
              <span
                key={d.domain}
                className="text-[length:var(--text-sm)] text-[var(--text-primary)] font-[family-name:var(--font-mono)]"
              >
                {d.domain}
              </span>
            ))}
          </div>
        ) : (
          <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
            No domains mapped yet. Map at least one in{' '}
            <Link
              to="/settings"
              search={{ tab: 'orgs' }}
              className="text-[var(--accent)] no-underline hover:underline"
            >
              Organizations
            </Link>{' '}
            so members resolve to this connection.
          </p>
        )}
      </div>

      <form onSubmit={handleSubmit} noValidate className="flex flex-col gap-[var(--space-2)]">
        <Input
          aria-label="Issuer URL"
          placeholder="https://idp.example.com"
          value={issuer}
          onChange={(e) => setIssuer(e.target.value)}
        />
        <Input
          aria-label="Client ID"
          placeholder="Client ID"
          value={clientId}
          onChange={(e) => setClientId(e.target.value)}
        />
        <Input
          type="password"
          aria-label="Client secret"
          autoComplete="off"
          placeholder={
            sso.data?.configured ? 'Client secret (re-enter to save)' : 'Client secret'
          }
          value={clientSecret}
          onChange={(e) => setClientSecret(e.target.value)}
        />
        <label className="flex items-center gap-[var(--space-2)] text-[length:var(--text-sm)] text-[var(--text-primary)]">
          <Checkbox
            checked={enforced}
            onCheckedChange={(v) => setEnforced(v === true)}
          />
          Require SSO (block password login for this org's domains)
        </label>

        {error ? (
          <p role="alert" className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]">
            {error}
          </p>
        ) : saved ? (
          <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
            Saved.
          </p>
        ) : null}

        <div className="flex items-center gap-[var(--space-2)]">
          <Button type="submit" variant="secondary" size="sm" disabled={put.isPending}>
            {put.isPending ? 'Saving…' : 'Save connection'}
          </Button>
          {sso.data?.configured ? (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => void handleRemove()}
              disabled={del.isPending}
              className="text-[var(--text-muted)] hover:text-[var(--danger)]"
            >
              Remove
            </Button>
          ) : null}
        </div>
      </form>
    </div>
  )
}

// Custom login domains — vanity hostnames that serve the org's white-labeled
// sign-in screen (e.g. wiki.example.com). Distinct from the email-domain
// auto-join mappings (those drive membership; these drive the login surface).
// Org-admin accessible. Also hosts the per-org login-method toggles that govern
// what that custom-domain login screen offers.
function CustomDomainsPanel({ org }: { org: Org }) {
  const orgId = org.id
  const hostnames = useOrgHostnames(orgId)

  return (
    <div className="flex flex-col gap-[var(--space-6)]">
      <div className="flex flex-col gap-[var(--space-4)]">
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)] leading-[var(--leading-relaxed)]">
          Serve a white-labeled sign-in screen on your own domain (e.g.{' '}
          <span className="font-[family-name:var(--font-mono)]">
            wiki.example.com
          </span>
          ). Add the hostname, point DNS at us, then verify.
        </p>

        {hostnames.isLoading ? (
          <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
            Loading domains…
          </p>
        ) : hostnames.isError ? (
          <p role="alert" className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]">
            Couldn't load custom domains.
          </p>
        ) : hostnames.data && hostnames.data.length > 0 ? (
          <ul className="m-0 p-0 list-none flex flex-col gap-[var(--space-3)]">
            {hostnames.data.map((h) => (
              <OrgHostnameRow key={h.hostname} orgId={orgId} hostname={h} />
            ))}
          </ul>
        ) : (
          <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
            No custom domains yet.
          </p>
        )}

        <AddHostnameForm orgId={orgId} />
      </div>

      <LoginMethodsSection orgId={orgId} />
    </div>
  )
}

function OrgHostnameRow({
  orgId,
  hostname,
}: {
  orgId: number
  hostname: OrgHostname
}) {
  const verify = useVerifyOrgHostname(orgId)
  const del = useDeleteOrgHostname(orgId)
  const adminLogin = useAdminDomainLogin(orgId)
  const isInstanceAdmin = useMe().data?.is_instance_admin ?? false
  const [error, setError] = useState<string | null>(null)
  const [failedAttempts, setFailedAttempts] = useState(0)
  const [confirmOpen, setConfirmOpen] = useState(false)
  const isActive = hostname.status === 'active'

  // Instance-admin self-login: mint a one-time token and open the org's domain
  // in a new tab already signed in (a normal host-bound session there) — the
  // way an admin whose identity isn't in the org's IdP gets through its door.
  async function handleOpenAsAdmin() {
    setError(null)
    try {
      const url = await adminLogin.mutateAsync(hostname.hostname)
      window.open(url, '_blank', 'noopener')
    } catch {
      setError('Could not open the domain. Try again.')
    }
  }
  // Auto-run the health probe once for Active rows; pending rows check on demand.
  const health = useOrgHostnameHealth(orgId, hostname.hostname, isActive)

  // Cancel-safe auto-retry: after a verification_failed, retry the verify a
  // couple times on a short delay (DNS propagation), then stop. Cleared on
  // unmount and whenever a fresh manual attempt resets the counter.
  const retryRef = useRef<number | null>(null)
  useEffect(() => {
    if (isActive || failedAttempts === 0 || failedAttempts > 2) return
    retryRef.current = window.setTimeout(() => {
      void runVerify()
    }, 4000)
    return () => {
      if (retryRef.current) window.clearTimeout(retryRef.current)
    }
    // runVerify is stable enough for this effect; keying on the attempt count
    // is what schedules each retry.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [failedAttempts, isActive])

  async function runVerify() {
    setError(null)
    try {
      await verify.mutateAsync(hostname.hostname)
      setFailedAttempts(0)
    } catch (err) {
      if (err instanceof ApiError && err.code === 'verification_failed') {
        setError('DNS not found yet — propagation can take a few minutes.')
        setFailedAttempts((n) => n + 1)
      } else if (err instanceof ApiError) {
        setError(err.message)
      } else {
        setError('Failed to verify.')
      }
    }
  }

  // A manual Verify resets the retry budget so the auto-retry chain restarts.
  function handleVerify() {
    setFailedAttempts(0)
    void runVerify()
  }

  const retrying = !isActive && failedAttempts > 0 && failedAttempts <= 2

  return (
    <li
      className={cn(
        'm-0 list-none flex flex-col gap-[var(--space-3)]',
        'px-[var(--space-3)] py-[var(--space-3)]',
        'rounded-[var(--radius-sm)]',
        'border border-[var(--border-subtle)] bg-[var(--surface-1)]',
      )}
    >
      <div className="flex items-center gap-[var(--space-3)]">
        <span className="flex-1 min-w-0 truncate text-[length:var(--text-sm)] text-[var(--text-primary)] font-[family-name:var(--font-mono)]">
          {hostname.hostname}
        </span>
        <Badge variant={isActive ? 'accent' : 'muted'}>
          {isActive ? 'Active' : 'Pending'}
        </Badge>
        {!isActive ? (
          <Button
            type="button"
            variant="secondary"
            size="sm"
            onClick={handleVerify}
            disabled={verify.isPending}
          >
            {verify.isPending || retrying
              ? 'Verifying…'
              : failedAttempts > 0
                ? 'Retry verify'
                : 'Verify'}
          </Button>
        ) : null}
        {isActive && isInstanceAdmin ? (
          <Button
            type="button"
            variant="secondary"
            size="sm"
            onClick={handleOpenAsAdmin}
            disabled={adminLogin.isPending}
            title={`Sign in on ${hostname.hostname} as yourself`}
          >
            <LogIn width={14} height={14} />
            {adminLogin.isPending ? 'Opening…' : 'Open'}
          </Button>
        ) : null}
        <Button
          type="button"
          variant="ghost"
          size="sm"
          aria-label={`Remove ${hostname.hostname}`}
          onClick={() => setConfirmOpen(true)}
          disabled={del.isPending}
          className="text-[var(--text-muted)] hover:text-[var(--danger)]"
        >
          <Trash2 width={14} height={14} />
        </Button>
      </div>

      <HealthChips
        health={health.data}
        loading={health.isFetching}
        error={health.isError}
        onCheck={() => void health.refetch()}
      />

      {error ? (
        <p role="alert" className="m-0 text-[length:var(--text-xs)] text-[var(--danger)]">
          {error}
          {failedAttempts > 2 ? ' Records still not visible — double-check them and retry.' : ''}
        </p>
      ) : null}

      {!isActive ? (
        <div className="flex flex-col gap-[var(--space-3)] rounded-[var(--radius-sm)] border border-[var(--border-subtle)] bg-[var(--surface-2)] p-[var(--space-3)]">
          <span className="text-[length:var(--text-xs)] uppercase tracking-wider text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
            Add these DNS records, then verify
          </span>
          <DnsRecord
            kind="TXT"
            name={hostname.txt_name}
            value={hostname.txt_value}
          />
          <DnsRecord
            kind="CNAME"
            name={hostname.hostname}
            value={hostname.cname_target}
          />
        </div>
      ) : null}

      <Dialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Remove "{hostname.hostname}"?</DialogTitle>
            <DialogDescription>
              Its white-labeled login screen stops working. You can re-add it
              later, but DNS verification will need to run again.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <DialogClose asChild>
              <Button type="button" variant="ghost">
                Cancel
              </Button>
            </DialogClose>
            <Button
              type="button"
              variant="danger"
              onClick={() => {
                void del.mutateAsync(hostname.hostname)
                setConfirmOpen(false)
              }}
              disabled={del.isPending}
            >
              {del.isPending ? 'Removing…' : 'Remove'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </li>
  )
}

// A single read-only DNS record line (record type + name + value) with a copy
// affordance on the value. Mirrors the CommandBlock copy pattern in
// SettingsSyncTab.
function DnsRecord({
  kind,
  name,
  value,
}: {
  kind: 'TXT' | 'CNAME'
  name: string
  value: string
}) {
  const [copied, setCopied] = useState(false)
  async function copy() {
    try {
      if (!navigator.clipboard?.writeText) return
      await navigator.clipboard.writeText(value)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
    } catch {
      // Best-effort — the user can select the text manually.
    }
  }
  return (
    <div className="flex flex-col gap-[var(--space-1)]">
      <div className="flex items-center justify-between gap-[var(--space-2)]">
        <span className="flex items-center gap-[var(--space-2)] text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
          <Badge variant="muted">{kind}</Badge>
          <span className="font-[family-name:var(--font-mono)] text-[var(--text-primary)] break-all">
            {name}
          </span>
        </span>
        <Button type="button" variant="ghost" size="sm" onClick={() => void copy()}>
          <Copy width={13} height={13} />
          <span>{copied ? 'Copied!' : 'Copy'}</span>
        </Button>
      </div>
      <pre className="m-0 overflow-x-auto rounded-[var(--radius-sm)] border border-[var(--border-subtle)] bg-[var(--surface-1)] px-[var(--space-3)] py-[var(--space-2)] text-[length:var(--text-xs)] text-[var(--text-primary)] font-[family-name:var(--font-mono)] leading-[var(--leading-relaxed)] whitespace-pre-wrap break-all">
        {value}
      </pre>
    </div>
  )
}

// DNS + HTTPS health chips for a custom domain. Lazy: nothing fires until the
// "Check" button (or the auto-run for Active rows) triggers the probe. A failed
// check shows the backend's `note` so the admin knows what's wrong.
function HealthChips({
  health,
  loading,
  error,
  onCheck,
}: {
  health: HostnameHealth | undefined
  loading: boolean
  error: boolean
  onCheck: () => void
}) {
  return (
    <div className="flex items-center gap-[var(--space-2)] flex-wrap">
      <Button type="button" variant="ghost" size="sm" onClick={onCheck} disabled={loading}>
        {loading ? 'Checking…' : 'Check'}
      </Button>
      {error ? (
        <span className="text-[length:var(--text-xs)] text-[var(--danger)]">
          Health check failed.
        </span>
      ) : health ? (
        <>
          <Badge variant={health.dns_ok ? 'accent' : 'muted'}>
            {health.dns_ok ? 'DNS ✓' : `DNS ${health.note ?? '✗'}`}
          </Badge>
          <Badge variant={health.https_ok ? 'accent' : 'muted'}>
            {health.https_ok ? 'HTTPS ✓' : `HTTPS ${!health.dns_ok ? '—' : (health.note ?? '✗')}`}
          </Badge>
          {health.dns_ok && health.addrs.length > 0 ? (
            <span className="text-[length:var(--text-xs)] text-[var(--text-muted)] font-[family-name:var(--font-mono)] truncate max-w-full">
              {health.addrs.join(', ')}
            </span>
          ) : null}
        </>
      ) : null}
    </div>
  )
}

// Branding overrides for the org's custom-domain login screen (and, via
// host-context, the whole UI on that domain): a logo URL and an accent color.
// Both optional — empty clears the override. Surfaces the backend's 400
// validation (https-only logo; hex/oklch()/rgb() accent). The accent input
// shows a live swatch using the entered value as an inline background — that's
// runtime user data, not a hardcoded token.
function BrandingSection({ orgId }: { orgId: number }) {
  const branding = useOrgBranding(orgId)
  const put = usePutOrgBranding(orgId)
  const uploadLogo = useUploadOrgLogo(orgId)
  const deleteLogo = useDeleteOrgLogo(orgId)
  const fileRef = useRef<HTMLInputElement>(null)
  const [accent, setAccent] = useState('')
  const [importUrl, setImportUrl] = useState('')
  const [showImport, setShowImport] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)
  // Prefill the editable color/style fields from the loaded branding once.
  const [hydratedFor, setHydratedFor] = useState<number | null>(null)
  if (branding.data && hydratedFor !== orgId) {
    setAccent(branding.data.accent)
    setHydratedFor(orgId)
  }

  const hasLogo = branding.data?.has_logo ?? false
  const logoUrl = branding.data?.logo_url ?? ''

  // The accent is saved via the form. The logo is its own immediate action
  // (upload / import / remove), each storing to tela + refetching.
  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError(null)
    setSaved(false)
    try {
      await put.mutateAsync({ accent: accent.trim() })
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
      await put.mutateAsync({ accent: accent.trim(), logo_import_url: importUrl.trim() })
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
        Your logo and accent color theme your custom-domain sign-in screen and are
        applied automatically to slide decks created in this org's spaces. Branding
        is the logo and color, not the style — each deck's variant stays a deliberate
        per-deck choice. Leave a field blank to use the tela default.
      </p>

      {/* Logo — stored in tela (so it renders everywhere, incl. exported decks). */}
      <div className="flex flex-col gap-[var(--space-2)]">
        <span className="text-[length:var(--text-xs)] uppercase tracking-wider text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
          Logo
        </span>
        <div className="flex items-center gap-[var(--space-3)]">
          {hasLogo ? (
            <img
              src={logoUrl}
              alt="Organization logo"
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
          PNG, JPEG, WebP or GIF, up to 2 MB. tela stores and serves it, so it renders everywhere — including exported decks.
        </p>
      </div>

      <form onSubmit={handleSubmit} noValidate className="flex flex-col gap-[var(--space-3)]">
        <div className="flex flex-col gap-[var(--space-1)]">
          <label
            htmlFor={`org-accent-${orgId}`}
            className="text-[length:var(--text-xs)] uppercase tracking-wider text-[var(--text-muted)] font-[family-name:var(--font-sans)]"
          >
            Accent color
          </label>
          <div className="flex items-center gap-[var(--space-2)]">
            <div className="flex-1 min-w-0">
              <Input
                id={`org-accent-${orgId}`}
                autoComplete="off"
                placeholder="#4f46e5 or oklch(…)"
                value={accent}
                onChange={(e) => setAccent(e.target.value)}
              />
            </div>
            {accent.trim() ? (
              // Swatch fed by the entered value (runtime data). An invalid color
              // just paints transparent — harmless preview.
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

function AddHostnameForm({ orgId }: { orgId: number }) {
  const [hostname, setHostname] = useState('')
  const [error, setError] = useState<string | null>(null)
  const add = useAddOrgHostname(orgId)

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    const trimmed = hostname.trim()
    if (!trimmed) {
      setError('Hostname is required.')
      return
    }
    setError(null)
    try {
      await add.mutateAsync(trimmed)
      setHostname('')
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        setError('That hostname is already in use.')
      } else if (err instanceof ApiError && err.status === 400) {
        setError(
          "That's not a valid hostname — use a subdomain like wiki.example.com, not a bare domain.",
        )
      } else if (err instanceof ApiError) {
        setError(err.message)
      } else {
        setError('Failed to add hostname.')
      }
    }
  }

  return (
    <form
      onSubmit={handleSubmit}
      noValidate
      className="flex flex-col gap-[var(--space-2)] pt-[var(--space-4)] border-t border-[var(--border-subtle)]"
    >
      <label
        htmlFor={`add-hostname-${orgId}`}
        className="text-[length:var(--text-xs)] uppercase tracking-wider text-[var(--text-muted)] font-[family-name:var(--font-sans)]"
      >
        Add a custom domain
      </label>
      <div className="flex items-start gap-[var(--space-2)]">
        <div className="flex-1 min-w-0">
          <Input
            id={`add-hostname-${orgId}`}
            placeholder="wiki.example.com"
            autoComplete="off"
            value={hostname}
            onChange={(e) => setHostname(e.target.value)}
            aria-invalid={error != null}
          />
        </div>
        <Button type="submit" variant="secondary" disabled={add.isPending || hostname.trim() === ''}>
          <Globe width={14} height={14} />
          <span>{add.isPending ? 'Adding…' : 'Add'}</span>
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

// Per-org login-method toggles. These ONLY affect the org's custom-domain login
// screen, not the canonical-host login. The backend refuses to disable both
// when the org has no SSO configured (it would lock everyone out) — we surface
// that and roll the optimistic toggle back.
function LoginMethodsSection({ orgId }: { orgId: number }) {
  const settings = useOrgLoginSettings(orgId)
  const put = usePutOrgLoginSettings(orgId)
  const [error, setError] = useState<string | null>(null)

  const current = settings.data
  if (!current) {
    return (
      <section className="flex flex-col gap-[var(--space-2)] pt-[var(--space-5)] border-t border-[var(--border-subtle)]">
        <h2 className="m-0 font-[family-name:var(--font-sans)] text-[length:var(--text-base)] text-[var(--text-primary)]">
          Login methods
        </h2>
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
          {settings.isError ? "Couldn't load login methods." : 'Loading login methods…'}
        </p>
      </section>
    )
  }

  async function save(next: { password_enabled: boolean; social_enabled: boolean }) {
    setError(null)
    try {
      await put.mutateAsync(next)
    } catch (err) {
      if (err instanceof ApiError && err.status === 400) {
        setError(
          "You can't disable both without single sign-on — at least one sign-in method must stay on.",
        )
      } else if (err instanceof ApiError) {
        setError(err.message)
      } else {
        setError('Failed to save login methods.')
      }
    }
  }

  return (
    <section className="flex flex-col gap-[var(--space-3)] pt-[var(--space-5)] border-t border-[var(--border-subtle)]">
      <header className="flex flex-col gap-[var(--space-1)]">
        <h2 className="m-0 font-[family-name:var(--font-sans)] text-[length:var(--text-base)] text-[var(--text-primary)]">
          Login methods
        </h2>
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)] leading-[var(--leading-relaxed)]">
          Choose what the sign-in screen on your custom domains offers. This
          doesn't change the canonical {`${location.hostname}`} login.
        </p>
      </header>

      <label className="flex items-center gap-[var(--space-2)] text-[length:var(--text-sm)] text-[var(--text-primary)]">
        <Checkbox
          checked={current.password_enabled}
          disabled={put.isPending}
          onCheckedChange={(v) =>
            void save({ ...current, password_enabled: v === true })
          }
        />
        Password sign-in
      </label>
      <label className="flex items-center gap-[var(--space-2)] text-[length:var(--text-sm)] text-[var(--text-primary)]">
        <Checkbox
          checked={current.social_enabled}
          disabled={put.isPending}
          onCheckedChange={(v) =>
            void save({ ...current, social_enabled: v === true })
          }
        />
        Social sign-in
      </label>

      {error ? (
        <p role="alert" className="m-0 text-[length:var(--text-xs)] text-[var(--danger)]">
          {error}
        </p>
      ) : null}
    </section>
  )
}

// Org-scoped access history — who added whom, what was shared, which domains
// were mapped — for THIS org only. Backed by the org-admin-gated
// /api/orgs/{id}/audit; reuses AuditRow from the instance-wide audit tab.
function OrgActivityPanel({ orgId }: { orgId: number }) {
  const audit = useOrgAudit(orgId)
  // The audit log is Enterprise-gated; the backend returns 402 upgrade_required
  // for a non-Enterprise org. Show that as an upgrade nudge, not a hard error.
  const needsUpgrade = audit.error instanceof ApiError && audit.error.code === 'upgrade_required'
  return (
    <div className="flex flex-col gap-[var(--space-3)]">
      {audit.isLoading ? (
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
          Loading activity…
        </p>
      ) : needsUpgrade ? (
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
          The audit log is an Enterprise feature. Upgrade this organization's plan to see its
          access history — who added whom, what was shared, which domains were mapped.
        </p>
      ) : audit.isError ? (
        <p role="alert" className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]">
          Couldn't load activity.
        </p>
      ) : audit.data && audit.data.length > 0 ? (
        <ul className="m-0 p-0 list-none flex flex-col gap-[var(--space-1)]">
          {audit.data.map((e) => (
            <AuditRow key={e.id} entry={e} />
          ))}
        </ul>
      ) : (
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
          No changes recorded yet.
        </p>
      )}
    </div>
  )
}

function DangerZone({ org }: { org: Org }) {
  const [open, setOpen] = useState(false)
  return (
    <section className="flex flex-col gap-[var(--space-3)] rounded-[var(--radius-md)] border border-[var(--border-strong)] p-[var(--space-4)]">
      <header className="flex flex-col gap-[var(--space-1)]">
        <h2 className="m-0 font-[family-name:var(--font-sans)] text-[length:var(--text-base)] text-[var(--text-primary)]">
          Danger zone
        </h2>
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)] leading-[var(--leading-relaxed)]">
          Deleting an org removes the space access its members had through it.
          Spaces and direct members are untouched.
        </p>
      </header>
      <div>
        <Button type="button" variant="danger" size="sm" onClick={() => setOpen(true)}>
          <Trash2 width={14} height={14} />
          <span>Delete organization</span>
        </Button>
      </div>
      <DeleteOrgDialog org={org} open={open} onOpenChange={setOpen} />
    </section>
  )
}

function DeleteOrgDialog({
  org,
  open,
  onOpenChange,
}: {
  org: Org
  open: boolean
  onOpenChange: (next: boolean) => void
}) {
  const [error, setError] = useState<string | null>(null)
  const deleteOrg = useDeleteOrg()

  function handleClose(next: boolean) {
    if (!next) setError(null)
    onOpenChange(next)
  }

  async function handleConfirm() {
    setError(null)
    try {
      await deleteOrg.mutateAsync(org.id)
      // Land back on the org list; this org no longer exists.
      window.location.assign('/settings?tab=orgs')
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Failed to delete org.')
    }
  }

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Delete "{org.name}"?</DialogTitle>
          <DialogDescription>
            Members lose any space access they had through this org. Spaces and
            direct members are untouched. This can't be undone.
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
            variant="danger"
            onClick={() => void handleConfirm()}
            disabled={deleteOrg.isPending}
          >
            {deleteOrg.isPending ? 'Deleting…' : 'Delete org'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function orgMemberErrorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.code === 'last_admin') {
      return "Can't remove the last admin — promote someone else first."
    }
    return err.message
  }
  return 'Something went wrong. Try again.'
}

function addOrgMemberErrorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 404) return 'User not found.'
    if (err.status === 409) return 'Already a member.'
    // 402 quota_exceeded — the org's seat limit. The backend message already
    // names the plan + cap; surface it verbatim.
    if (err.status === 402) return err.message
    return err.message
  }
  return 'Failed to add member.'
}
