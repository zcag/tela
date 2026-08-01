import { Fragment, useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import {
  Building2,
  ChevronDown,
  ChevronRight,
  Eye,
  EyeOff,
  FileDown,
  Globe,
  Lock,
  MoreHorizontal,
  Plus,
  RotateCw,
  Users,
  UsersRound,
} from 'lucide-react'
import { ApiError } from '../../lib/api'
import {
  useDeleteSpace,
  useSpaces,
  useUpdateSpace,
} from '../../lib/queries/spaces'
import type { Space } from '../../lib/types'
import { spaceOwnership } from '../../lib/space-owner'
import { Button } from '../ui/button'
import { Card, CardBody, CardFooter } from '../ui/card'
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '../ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '../ui/dropdown-menu'
import { Input } from '../ui/input'
import { Tooltip, TooltipContent, TooltipTrigger } from '../ui/tooltip'
import { useSpaceAccess } from '../../lib/queries/space-grants'
import {
  useHiddenSpaces,
  useToggleHiddenSpace,
} from '../../lib/queries/hidden-spaces'
import { useFreshness } from '../../lib/queries/freshness'
import { useSummaries } from '../../lib/queries/summaries'
import { spaceStaleLabel } from './staleness'
import { cn } from '../../lib/utils'
import { emitOpenNewSpace } from '../../lib/newSpaceEvent'
import { useFileDownload } from './use-file-download'
import { ShareSpaceDialog } from './ShareSpaceDialog'
import { StalenessDot } from './StalenessDot'
import { SpacePages } from './SpacePages'

interface SpaceTreeProps {
  activeSpaceId: number | null
  activePageId: number | null
}

// Sentinel key for the (label-less) personal cluster in the collapsed-orgs set.
const OWN_KEY = '\0own'

// Cluster the flat list by owning org so same-source spaces sit adjacent: your
// own spaces first, then each org alphabetically. Order within a group is the
// alphabetical order useSpaces() already gives us. Grouping uses the same
// spaceOwnership() resolver that drives the "Owned by …" label — so a space
// shown as org-owned (incl. the sole-org-share heuristic) clusters under that
// org rather than landing in your personal group. Keyed by org name (the
// principals fallback carries no id). Empty groups are dropped.
function groupByOrg(spaces: Space[]): { org: string | null; spaces: Space[] }[] {
  const own: Space[] = []
  const byOrg = new Map<string, Space[]>()
  for (const s of spaces) {
    const owner = spaceOwnership(s)
    if (owner.kind === 'org' && owner.org) {
      const g = byOrg.get(owner.org) ?? []
      g.push(s)
      byOrg.set(owner.org, g)
    } else {
      own.push(s)
    }
  }
  const orgGroups = [...byOrg.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([org, spaces]) => ({ org, spaces }))
  return [{ org: null, spaces: own }, ...orgGroups].filter((g) => g.spaces.length > 0)
}

export function SpaceTree({ activeSpaceId, activePageId }: SpaceTreeProps) {
  const navigate = useNavigate()
  const spaces = useSpaces()

  // Three independent, persisted collapse levels. Spaces collapse by default
  // (the set holds the *open* ones); orgs are open by default (the set holds the
  // *collapsed* ones, so a fresh user sees everything).
  const expandedSpaces = usePersistentSet('tela.sidebar.expandedSpaces')
  const collapsedOrgs = usePersistentSet('tela.sidebar.collapsedOrgs')

  // Auto-expand the space you're in so its pages are always there without a
  // click. The active-page ancestor reveal lives inside SpacePages.
  const addSpace = expandedSpaces.add
  useEffect(() => {
    if (activeSpaceId != null) addSpace(String(activeSpaceId))
  }, [activeSpaceId, addSpace])

  // Per-space backfill backlog for the sidebar dots: the union of pending RAG
  // indexing and pending summaries. Each subsystem is gated by its own enabled
  // flag (else everything reads behind — noise on an instance that isn't running
  // it). Failed summaries are excluded (see staleness.ts) so the dot stays a
  // "catching up" signal, not a stuck error.
  const freshness = useFreshness()
  const summaries = useSummaries()
  const staleLabelBySpace = useMemo(() => {
    const indexing = new Map<number, number>()
    if (freshness.data?.enabled) {
      for (const f of freshness.data.spaces) {
        if (f.stale_pages > 0) indexing.set(f.space_id, f.stale_pages)
      }
    }
    const summarizing = new Map<number, number>()
    if (summaries.data?.enabled) {
      for (const f of summaries.data.spaces) {
        if (f.stale > 0) summarizing.set(f.space_id, f.stale)
      }
    }
    const labels = new Map<number, string>()
    for (const id of new Set([...indexing.keys(), ...summarizing.keys()])) {
      const label = spaceStaleLabel(indexing.get(id) ?? 0, summarizing.get(id) ?? 0)
      if (label) labels.set(id, label)
    }
    return labels
  }, [freshness.data, summaries.data])

  // Alphabetical order useSpaces() already gives us, clustered by org below.
  const all = spaces.data ?? []

  // Per-user hidden spaces — a sidebar-only declutter (they stay in /spaces,
  // search and the palette). The active space is always revealed: navigating
  // into a space must never make it vanish from the tree.
  const hiddenQuery = useHiddenSpaces()
  const [showHidden, setShowHidden] = useState(false)
  // hiddenIds = what the row shows as hidden (drives muting + the menu label);
  // tuckedIds = what the reveal actually tucks away, i.e. minus the active
  // space. Intersected with the spaces we hold — the API list is re-gated, but
  // the two queries can be a beat apart.
  const hiddenIds = useMemo(() => {
    const ids = new Set(hiddenQuery.data ?? [])
    return new Set(all.filter((s) => ids.has(s.id)).map((s) => s.id))
  }, [hiddenQuery.data, all])
  const tuckedIds = useMemo(() => {
    const ids = new Set(hiddenIds)
    if (activeSpaceId != null) ids.delete(activeSpaceId)
    return ids
  }, [hiddenIds, activeSpaceId])

  const visible = showHidden ? all : all.filter((s) => !tuckedIds.has(s.id))

  const renderRow = (space: Space) => (
    <SpaceRow
      key={space.id}
      space={space}
      active={space.id === activeSpaceId}
      activePageId={activePageId}
      hidden={hiddenIds.has(space.id)}
      staleLabel={staleLabelBySpace.get(space.id) ?? null}
      expanded={expandedSpaces.set.has(String(space.id))}
      onToggleExpand={() => expandedSpaces.toggle(String(space.id))}
      onSelect={() =>
        void navigate({ to: '/spaces/$spaceId', params: { spaceId: space.id } })
      }
    />
  )

  // Render the rest, clustered by org. Each cluster is a collapsible section
  // header (uppercase label + hairline) — collapsing it tucks away its spaces
  // without indenting anything. The personal cluster only gets a header once at
  // least one org cluster exists (solo users keep a bare, flat list).
  const renderGrouped = (list: Space[]) => {
    const groups = groupByOrg(list)
    const hasOrgGroups = groups.some((g) => g.org != null)
    return groups.map(({ org, spaces: group }) => {
      const key = org ?? OWN_KEY
      const labelled = org != null || hasOrgGroups
      const collapsed = collapsedOrgs.set.has(key)
      return (
        <Fragment key={key}>
          {labelled ? (
            <ClusterHeader
              label={org ?? 'My spaces'}
              collapsed={collapsed}
              onToggle={() => collapsedOrgs.toggle(key)}
            />
          ) : null}
          {collapsed ? null : <ul className="m-0 p-0 list-none flex flex-col gap-[1px]">{group.map(renderRow)}</ul>}
        </Fragment>
      )
    })
  }

  return (
    <div
      // Roving target for the j/k keyboard layer (see lib/keys); the engine
      // walks the visible data-keynav-item rows (space names + page titles).
      data-keynav-region="nav"
      className="flex-1 min-h-0 overflow-y-auto flex flex-col pb-[var(--space-3)] mt-[var(--space-2)] border-t border-[var(--border-subtle)] pt-[var(--space-3)]"
    >
      {/* No section title or chrome — the org/personal cluster headers carry the
          labelling, and "New space" lives in the command palette + home
          dashboard. The tree is purely navigation. */}
      <section
        className="flex flex-col gap-[1px] px-[var(--space-3)]"
        aria-label="Spaces"
      >
        {spaces.isLoading ? <SpacesSkeleton /> : null}

        {spaces.isError ? (
          <SpacesError onRetry={() => void spaces.refetch()} />
        ) : null}

        {spaces.data && spaces.data.length === 0 ? (
          <Card className="bg-[var(--surface-1)]">
            <CardBody className="px-[var(--space-4)] py-[var(--space-3)] gap-[var(--space-1)]">
              <p className="m-0 text-[length:var(--text-sm)] font-medium text-[var(--text-primary)]">
                No spaces yet
              </p>
              <p className="m-0 text-[length:var(--text-xs)] text-[var(--text-muted)] leading-[var(--leading-relaxed)]">
                Create a space to get started.
              </p>
            </CardBody>
            <CardFooter className="px-[var(--space-4)] pt-0 pb-[var(--space-3)]">
              <Button
                variant="primary"
                size="sm"
                className="w-full"
                onClick={() => emitOpenNewSpace()}
              >
                <Plus width={14} height={14} /> New space
              </Button>
            </CardFooter>
          </Card>
        ) : null}

        {renderGrouped(visible)}

        {/* Hidden-spaces reveal — deliberately quiet, and only there when you
            actually hid something. Revealed rows stay in their own cluster,
            just muted. */}
        {tuckedIds.size > 0 ? (
          <button
            type="button"
            onClick={() => setShowHidden((v) => !v)}
            aria-expanded={showHidden}
            className="mt-[var(--space-1)] flex w-full items-center gap-[var(--space-1)] rounded-[var(--radius-sm)] bg-transparent border-0 px-[var(--space-1)] py-[var(--space-1)] cursor-pointer outline-none text-[length:var(--text-xs)] text-[var(--text-muted)] hover:text-[var(--text-primary)] hover:bg-[var(--sidebar-item-hover)] focus-visible:ring-2 focus-visible:ring-[var(--accent)]"
          >
            {showHidden ? (
              <EyeOff width={12} height={12} aria-hidden className="shrink-0" />
            ) : (
              <Eye width={12} height={12} aria-hidden className="shrink-0" />
            )}
            <span className="truncate">
              {showHidden ? 'Hide hidden' : `Show hidden (${tuckedIds.size})`}
            </span>
          </button>
        ) : null}
      </section>
    </div>
  )
}

// ClusterHeader — a collapsible org/personal section divider. The chevron is
// always shown (the cluster is the disclosure), the label leads, a hairline
// fills the rest.
function ClusterHeader({
  label,
  collapsed,
  onToggle,
}: {
  label: string
  collapsed: boolean
  onToggle: () => void
}) {
  return (
    <button
      type="button"
      onClick={onToggle}
      aria-expanded={!collapsed}
      aria-label={label}
      className="group flex w-full items-center gap-[var(--space-1)] bg-transparent border-0 px-[var(--space-1)] py-[var(--space-1)] cursor-pointer outline-none"
    >
      <ChevronRight
        width={12}
        height={12}
        aria-hidden
        className={cn(
          'shrink-0 text-[var(--text-muted)] transition-transform',
          !collapsed && 'rotate-90',
        )}
      />
      <span className="min-w-0 truncate text-[length:var(--text-xs)] uppercase tracking-wider text-[var(--text-muted)] group-hover:text-[var(--text-primary)] font-[family-name:var(--font-sans)]">
        {label}
      </span>
      <span
        aria-hidden
        className="flex-1 min-w-[var(--space-4)] border-t border-[var(--border-subtle)]"
      />
    </button>
  )
}

// usePersistentSet — a localStorage-backed Set<string>, mirroring
// useExpandedNodes' read/write guards. Collapsing on reload (private mode /
// quota) is acceptable, so failures fall back to empty.
function usePersistentSet(key: string) {
  const [set, setSet] = useState<Set<string>>(() => {
    if (typeof window === 'undefined') return new Set()
    try {
      const raw = window.localStorage.getItem(key)
      const parsed: unknown = raw ? JSON.parse(raw) : []
      if (!Array.isArray(parsed)) return new Set()
      return new Set(parsed.filter((v): v is string => typeof v === 'string'))
    } catch {
      return new Set()
    }
  })
  const toggle = useCallback((k: string) => {
    setSet((prev) => {
      const next = new Set(prev)
      if (next.has(k)) next.delete(k)
      else next.add(k)
      try {
        window.localStorage.setItem(key, JSON.stringify([...next]))
      } catch {
        // ignore
      }
      return next
    })
  }, [key])
  const add = useCallback((k: string) => {
    setSet((prev) => {
      if (prev.has(k)) return prev
      const next = new Set(prev)
      next.add(k)
      try {
        window.localStorage.setItem(key, JSON.stringify([...next]))
      } catch {
        // ignore
      }
      return next
    })
  }, [key])
  return { set, toggle, add }
}

function SpacesSkeleton() {
  return (
    <div
      className="flex flex-col gap-[var(--space-1)] px-[var(--space-2)]"
      aria-hidden="true"
    >
      {[0, 1, 2].map((i) => (
        <div
          key={i}
          className="h-[var(--space-7)] rounded-[var(--radius-sm)] bg-[var(--surface-2)]"
        />
      ))}
    </div>
  )
}

function SpacesError({ onRetry }: { onRetry: () => void }) {
  return (
    <div className="flex items-center justify-between gap-[var(--space-2)] px-[var(--space-2)] py-[var(--space-2)] rounded-[var(--radius-sm)] bg-[var(--surface-2)] text-[length:var(--text-sm)] text-[var(--danger)]">
      <span>Couldn't load spaces.</span>
      <Button variant="ghost" size="sm" onClick={onRetry} aria-label="Retry">
        <RotateCw width={14} height={14} />
      </Button>
    </div>
  )
}

interface SpaceRowProps {
  space: Space
  active: boolean
  activePageId: number | null
  /** Hidden for this user — rendered muted, only while the reveal is on. */
  hidden: boolean
  staleLabel: string | null
  expanded: boolean
  onToggleExpand: () => void
  onSelect: () => void
}

function SpaceRow({
  space,
  active,
  activePageId,
  hidden,
  staleLabel,
  expanded,
  onToggleExpand,
  onSelect,
}: SpaceRowProps) {
  const [renameOpen, setRenameOpen] = useState(false)
  const [shareOpen, setShareOpen] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const toggleHidden = useToggleHiddenSpace()
  const { download: downloadZip } = useFileDownload(
    `/api/spaces/${space.id}/export.zip`,
    { fallbackName: 'space.zip' },
  )

  return (
    <li className="m-0 p-0 list-none">
      <div
        className={cn(
          'group relative flex items-center gap-[var(--space-1)] pl-[var(--space-1)] pr-[var(--space-1)] rounded-[var(--radius-sm)]',
          'hover:bg-[var(--sidebar-item-hover)]',
          active &&
            'bg-[var(--sidebar-item-active)] shadow-[inset_2px_0_0_0_var(--sidebar-item-active-bar)]',
        )}
      >
        <button
          type="button"
          aria-label={expanded ? 'Collapse' : 'Expand'}
          aria-expanded={expanded}
          onClick={onToggleExpand}
          className="inline-flex items-center justify-center h-[var(--space-6)] w-[var(--space-4)] shrink-0 rounded-[var(--radius-xs)] bg-transparent border-0 cursor-pointer text-[var(--text-muted)] hover:text-[var(--text-primary)] outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]"
        >
          {expanded ? (
            <ChevronDown width={13} height={13} />
          ) : (
            <ChevronRight width={13} height={13} />
          )}
        </button>

        <button
          type="button"
          data-keynav-item
          onClick={onSelect}
          className={cn(
            'flex-1 min-w-0 text-left truncate py-[var(--space-2)]',
            'font-[family-name:var(--font-sans)] text-[length:var(--text-sm)] leading-[var(--leading-tight)]',
            'text-[var(--text-primary)] bg-transparent border-0 cursor-pointer outline-none',
            hidden && 'text-[var(--text-muted)]',
            active && 'text-[var(--accent)] font-medium',
          )}
        >
          {space.name || (
            <span className="text-[var(--text-muted)]">Untitled space</span>
          )}
        </button>

        {/* Resting state stays calm: only a published space flags itself, plus a
            staleness rollup. Both yield to the access cluster + ⋯ on hover. */}
        {space.visibility === 'public' ? (
          <Globe
            width={13}
            height={13}
            aria-label="Public on the web"
            className="shrink-0 text-[var(--text-muted)] group-hover:hidden"
          />
        ) : null}

        {staleLabel ? (
          <span className="shrink-0 inline-flex items-center group-hover:hidden">
            <StalenessDot label={staleLabel} />
          </span>
        ) : null}

        {/* Access cluster + actions — revealed on hover (or on the active row for
            no-hover devices), so the row reads as just a name at rest. Click the
            cluster to manage; hover it for the full who/what peek. */}
        <Tooltip>
          <TooltipTrigger asChild>
            <button
              type="button"
              aria-label={`${accessAriaLabel(space)} — manage access`}
              onClick={(e) => {
                e.stopPropagation()
                setShareOpen(true)
              }}
              className={cn(
                'shrink-0 inline-flex items-center bg-transparent border-0 p-[var(--space-1)] cursor-pointer outline-none rounded-[var(--radius-xs)] focus-visible:ring-2 focus-visible:ring-[var(--accent)]',
                'hidden group-hover:inline-flex focus-visible:inline-flex',
                active && '[@media(hover:none)]:inline-flex',
              )}
            >
              <AccessCluster space={space} />
            </button>
          </TooltipTrigger>
          <TooltipContent side="right">
            <SpaceAccessPeek space={space} />
          </TooltipContent>
        </Tooltip>

        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              variant="ghost"
              size="sm"
              aria-label={`Actions for ${space.name || 'space'}`}
              className={cn(
                'shrink-0 h-[var(--space-6)] w-[var(--space-6)] p-0 hidden group-hover:inline-flex data-[state=open]:inline-flex focus-visible:inline-flex',
                active && '[@media(hover:none)]:inline-flex',
              )}
            >
              <MoreHorizontal width={14} height={14} />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onSelect={() => setRenameOpen(true)}>Rename</DropdownMenuItem>
            <DropdownMenuItem onSelect={() => setShareOpen(true)}>
              <UsersRound width={14} height={14} /> Share
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => void downloadZip()}>
              <FileDown width={14} height={14} /> Export Markdown (.zip)
            </DropdownMenuItem>
            <DropdownMenuItem
              onSelect={() =>
                void toggleHidden.mutate({ spaceId: space.id, hidden })
              }
            >
              {hidden ? (
                <>
                  <Eye width={14} height={14} /> Unhide space
                </>
              ) : (
                <>
                  <EyeOff width={14} height={14} /> Hide space
                </>
              )}
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem destructive onSelect={() => setDeleteOpen(true)}>
              Delete
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      {expanded ? (
        <SpacePages spaceId={space.id} activePageId={activePageId} />
      ) : null}

      <RenameSpaceDialog
        space={space}
        open={renameOpen}
        onOpenChange={setRenameOpen}
      />
      <ShareSpaceDialog
        space={space}
        open={shareOpen}
        onOpenChange={setShareOpen}
      />
      <DeleteSpaceDialog
        space={space}
        open={deleteOpen}
        onOpenChange={setDeleteOpen}
      />
    </li>
  )
}

// isPrivateSpace — reachable only by you (not the personal home, no shares).
function isPrivateSpace(space: Space): boolean {
  return (
    !space.is_personal &&
    (space.member_count ?? 0) <= 1 &&
    (space.principals?.length ?? 0) === 0
  )
}

function accessAriaLabel(space: Space): string {
  if (space.visibility === 'public') return 'Public on the web'
  if (space.is_personal) return 'Personal space, only you'
  if (isPrivateSpace(space)) return 'Private, only you'
  const people = space.member_count ?? 0
  const shared = (space.principals ?? []).map((p) => p.name).join(', ')
  return `${people} ${people === 1 ? 'person' : 'people'}${shared ? `, shared with ${shared}` : ''}`
}

// AccessCluster — the compact access signal shown on row hover: a lock for solo
// spaces, a globe for public, else the effective people count plus a group icon
// when shared with a group. The owning org is conveyed by the cluster header,
// so it's no longer repeated here; the full who/what lives in the hover peek.
function AccessCluster({ space }: { space: Space }) {
  const tone = 'text-[var(--text-muted)] group-hover:text-[var(--text-primary)]'

  if (space.visibility === 'public') {
    return <Globe width={13} height={13} aria-hidden className={tone} />
  }
  if (space.is_personal || isPrivateSpace(space)) {
    return <Lock width={13} height={13} aria-hidden className={tone} />
  }

  const principals = space.principals ?? []
  const hasGroup = principals.some((p) => p.kind === 'group')

  return (
    <span
      className={cn(
        'inline-flex items-center gap-[3px] text-[length:var(--text-xs)] leading-none tabular-nums',
        tone,
      )}
    >
      <UsersRound width={13} height={13} aria-hidden />
      <span>{space.member_count ?? 0}</span>
      {hasGroup ? <Users width={12} height={12} aria-hidden /> : null}
    </span>
  )
}

// SpaceAccessPeek — the access line's hover content: the orgs/groups the space
// is shared with, then everyone who can actually open it with their effective
// role and how they reach it (direct / via <org-or-group>). Resolved lazily.
function SpaceAccessPeek({ space }: { space: Space }) {
  const access = useSpaceAccess(space.id)
  const principals = space.principals ?? []

  if (space.visibility === 'public') {
    return (
      <span className="text-[var(--text-muted)]">
        Public on the web — anyone with the link can read
      </span>
    )
  }
  if (space.is_personal || isPrivateSpace(space)) {
    return (
      <span className="text-[var(--text-muted)]">
        {space.is_personal ? 'Your personal space' : 'Private'} — only you
      </span>
    )
  }

  return (
    <div className="flex flex-col gap-[var(--space-2)] max-w-[16rem]">
      {principals.length > 0 ? (
        <div className="flex flex-col gap-[2px]">
          <span className="text-[length:var(--text-xs)] uppercase tracking-wider text-[var(--text-muted)]">
            Shared with
          </span>
          {principals.map((p) => (
            <span key={`${p.kind}:${p.name}`} className="flex items-center gap-[var(--space-1)]">
              {p.kind === 'group' ? (
                <Users width={11} height={11} aria-hidden />
              ) : (
                <Building2 width={11} height={11} aria-hidden />
              )}
              <span className="truncate">{p.name}</span>
              <span className="text-[var(--text-muted)]">
                {p.kind === 'group' ? 'group' : 'org'}
              </span>
            </span>
          ))}
        </div>
      ) : null}

      <div className="flex flex-col gap-[2px]">
        <span className="text-[length:var(--text-xs)] uppercase tracking-wider text-[var(--text-muted)]">
          {access.data ? `People (${access.data.length})` : 'People'}
        </span>
        {access.isLoading ? (
          <span className="text-[var(--text-muted)]">Loading…</span>
        ) : (
          (access.data ?? []).slice(0, 8).map((p) => (
            <div key={p.user_id} className="flex items-center justify-between gap-[var(--space-3)]">
              <span className="truncate">{p.username}</span>
              <span className="shrink-0 text-[var(--text-muted)] capitalize">
                {p.effective_role}
                {peekVia(p.sources) ? ` · ${peekVia(p.sources)}` : ''}
              </span>
            </div>
          ))
        )}
        {(access.data?.length ?? 0) > 8 ? (
          <span className="text-[var(--text-muted)]">
            +{(access.data?.length ?? 0) - 8} more
          </span>
        ) : null}
      </div>
    </div>
  )
}

// peekVia — compact provenance for the tooltip: "direct" or "via Acme".
function peekVia(sources: { kind: string; name?: string }[]): string {
  if (sources.some((s) => s.kind === 'direct')) return 'direct'
  const name = sources.find((s) => s.kind !== 'direct' && s.name)?.name
  return name ? `via ${name}` : ''
}

interface RenameSpaceDialogProps {
  space: Space
  open: boolean
  onOpenChange: (next: boolean) => void
}

function RenameSpaceDialog({ space, open, onOpenChange }: RenameSpaceDialogProps) {
  const [name, setName] = useState(space.name)
  const [error, setError] = useState<string | null>(null)
  const updateSpace = useUpdateSpace()

  function handleClose(next: boolean) {
    if (!next) {
      setName(space.name)
      setError(null)
    }
    onOpenChange(next)
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    const trimmed = name.trim()
    if (!trimmed) {
      setError('Name is required.')
      return
    }
    if (trimmed === space.name) {
      handleClose(false)
      return
    }
    setError(null)
    try {
      await updateSpace.mutateAsync({ id: space.id, name: trimmed })
      handleClose(false)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Failed to rename space.')
    }
  }

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Rename space</DialogTitle>
        </DialogHeader>
        <form onSubmit={handleSubmit} className="flex flex-col gap-[var(--space-3)]">
          <div className="flex flex-col gap-[var(--space-2)]">
            <label
              htmlFor={`rename-space-${space.id}`}
              className="text-[length:var(--text-sm)] text-[var(--text-muted)]"
            >
              Name
            </label>
            <Input
              id={`rename-space-${space.id}`}
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
              aria-invalid={error != null}
            />
            {error ? (
              <p className="m-0 text-[length:var(--text-xs)] text-[var(--danger)]">
                {error}
              </p>
            ) : null}
          </div>
          <DialogFooter>
            <DialogClose asChild>
              <Button type="button" variant="ghost">
                Cancel
              </Button>
            </DialogClose>
            <Button type="submit" disabled={updateSpace.isPending}>
              {updateSpace.isPending ? 'Saving…' : 'Save'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

interface DeleteSpaceDialogProps {
  space: Space
  open: boolean
  onOpenChange: (next: boolean) => void
}

function DeleteSpaceDialog({
  space,
  open,
  onOpenChange,
}: DeleteSpaceDialogProps) {
  const [error, setError] = useState<string | null>(null)
  const navigate = useNavigate()
  const deleteSpace = useDeleteSpace()
  const spaces = useSpaces()

  function handleClose(next: boolean) {
    if (!next) setError(null)
    onOpenChange(next)
  }

  async function handleDelete() {
    setError(null)
    try {
      await deleteSpace.mutateAsync(space.id)
      handleClose(false)
      // Route to another space if available, otherwise to the empty landing.
      const remaining = spaces.data?.filter((s) => s.id !== space.id) ?? []
      if (remaining.length > 0) {
        void navigate({
          to: '/spaces/$spaceId',
          params: { spaceId: remaining[0].id },
        })
      } else {
        void navigate({ to: '/' })
      }
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Failed to delete space.')
    }
  }

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Delete this space?</DialogTitle>
          <DialogDescription>
            "{space.name}" and all of its pages will be permanently removed. This
            action cannot be undone.
          </DialogDescription>
        </DialogHeader>
        {error ? (
          <p className="m-0 text-[length:var(--text-xs)] text-[var(--danger)]">
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
            onClick={handleDelete}
            disabled={deleteSpace.isPending}
          >
            {deleteSpace.isPending ? 'Deleting…' : 'Delete'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
