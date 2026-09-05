import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import {
  ChevronDown,
  ChevronRight,
  MoreHorizontal,
  RotateCw,
} from 'lucide-react'
import { ApiError } from '../../lib/api'
import { useQueryClient } from '@tanstack/react-query'
import {
  prefetchPage,
  useCreatePage,
  useDeletePage,
  usePages,
  useUpdatePage,
} from '../../lib/queries/pages'
import { useSpaceFreshness } from '../../lib/queries/freshness'
import { useSpaceSummaries } from '../../lib/queries/summaries'
import { pageStaleLabel } from './staleness'
import type { PageTreeNode } from '../../lib/types'
import { useExpandedNodes } from '../../lib/useExpandedNodes'
import { StalenessDot } from './StalenessDot'
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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '../ui/dropdown-menu'
import { Input } from '../ui/input'
import { VisibilityBadge } from '../ui/visibility-badge'
import { MovePageDialog } from './move-page-dialog'
import { cn } from '../../lib/utils'

const UNTITLED_TITLE = 'Untitled'

// findAncestors returns the chain of ancestor ids from outermost root down to
// the target's immediate parent. Returns [] when the target is itself a root,
// null when the target isn't in the tree at all. Used by the auto-reveal
// effect to expand every collapsed ancestor on a route change.
function findAncestors(nodes: PageTreeNode[], targetId: number): number[] | null {
  for (const node of nodes) {
    if (node.id === targetId) return []
    const childChain = findAncestors(node.children, targetId)
    if (childChain != null) return [node.id, ...childChain]
  }
  return null
}

interface SpacePagesProps {
  spaceId: number
  activePageId: number | null
}

// SpacePages — the page tree for a single space, rendered inline under its
// (expanded) space row in the unified sidebar tree. Mounts lazily: it only
// renders when its space is expanded, so usePages() fires on demand rather than
// for every space at once. The owning space row carries the name + chevron;
// this is just the nested pages, set off by a guide rail.
export function SpacePages({ spaceId, activePageId }: SpacePagesProps) {
  const tree = usePages({ spaceId, tree: true })
  const { expanded, toggle, expand, expandMany } = useExpandedNodes(spaceId)

  const treeData = tree.data as PageTreeNode[] | undefined
  const nodes = treeData ?? []

  // Per-page backfill backlog for the dots: the union of pending RAG indexing
  // and pending summaries, each gated by its own enabled flag (else every page
  // reads behind — noise on an instance not running that subsystem). Failed
  // summaries are excluded (see staleness.ts) so the dot stays a "catching up"
  // signal. Map page id → composed tooltip; absent = nothing outstanding.
  const freshness = useSpaceFreshness(spaceId)
  const summaries = useSpaceSummaries(spaceId)
  const staleLabels = useMemo(() => {
    const indexing = new Map<number, 'stale' | 'unindexed'>()
    if (freshness.data?.enabled) {
      for (const p of freshness.data.pages) {
        if (p.status === 'stale' || p.status === 'unindexed')
          indexing.set(p.page_id, p.status)
      }
    }
    const summarizing = new Map<number, 'stale' | 'missing'>()
    if (summaries.data?.enabled) {
      for (const p of summaries.data.pages) {
        if (p.status === 'stale' || p.status === 'missing')
          summarizing.set(p.page_id, p.status)
      }
    }
    const m = new Map<number, string>()
    for (const id of new Set([...indexing.keys(), ...summarizing.keys()])) {
      const label = pageStaleLabel(indexing.get(id) ?? null, summarizing.get(id) ?? null)
      if (label) m.set(id, label)
    }
    return m
  }, [freshness.data, summaries.data])

  // Auto-reveal the active page when navigation lands on it from outside the
  // sidebar (backlink click, command-palette result, [[wikilink]], direct URL,
  // tela://page/N link). The highlight prop already flows through; this just
  // ensures every collapsed ancestor on the path gets opened so the row is
  // rendered. Negative ids (optimistic collab placeholders) won't match any
  // tree row → findAncestors returns null → no-op.
  useEffect(() => {
    if (activePageId == null || activePageId < 0 || !treeData) return
    const chain = findAncestors(treeData, activePageId)
    if (chain && chain.length > 0) expandMany(chain)
  }, [activePageId, treeData, expandMany])

  if (tree.isLoading) return <PagesSkeleton />
  if (tree.isError) return <PagesError onRetry={() => void tree.refetch()} />
  if (tree.data && nodes.length === 0) {
    return (
      <p className="m-0 py-[var(--space-1)] pl-[var(--space-6)] text-[length:var(--text-xs)] text-[var(--text-muted)]">
        No pages yet
      </p>
    )
  }

  return (
    // Guide rail: a hairline carries the nesting so the page step can stay
    // small, keeping titles wide. Aligned to sit under the space row's chevron.
    <ul className="m-0 p-0 list-none flex flex-col gap-[1px] ml-[calc(var(--space-2)+var(--space-2))] border-l border-[var(--border-subtle)]">
      {nodes.map((node) => (
        <PageNode
          key={node.id}
          node={node}
          depth={0}
          spaceId={spaceId}
          activePageId={activePageId}
          expanded={expanded}
          onToggle={toggle}
          onExpand={expand}
          allNodes={nodes}
          staleLabels={staleLabels}
        />
      ))}
    </ul>
  )
}

function PagesSkeleton() {
  return (
    <div
      className="flex flex-col gap-[var(--space-1)] pl-[var(--space-6)]"
      aria-hidden="true"
    >
      {[0, 1].map((i) => (
        <div
          key={i}
          className="h-[var(--space-6)] rounded-[var(--radius-sm)] bg-[var(--surface-2)]"
        />
      ))}
    </div>
  )
}

function PagesError({ onRetry }: { onRetry: () => void }) {
  return (
    <div className="flex items-center justify-between gap-[var(--space-2)] ml-[var(--space-4)] px-[var(--space-2)] py-[var(--space-2)] rounded-[var(--radius-sm)] bg-[var(--surface-2)] text-[length:var(--text-sm)] text-[var(--danger)]">
      <span>Couldn't load pages.</span>
      <Button variant="ghost" size="sm" onClick={onRetry} aria-label="Retry">
        <RotateCw width={14} height={14} />
      </Button>
    </div>
  )
}

interface PageNodeProps {
  node: PageTreeNode
  depth: number
  spaceId: number
  activePageId: number | null
  expanded: Set<number>
  onToggle: (id: number) => void
  onExpand: (id: number) => void
  allNodes: PageTreeNode[]
  staleLabels: Map<number, string>
}

function PageNode({
  node,
  depth,
  spaceId,
  activePageId,
  expanded,
  onToggle,
  onExpand,
  allNodes,
  staleLabels,
}: PageNodeProps) {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const createPage = useCreatePage()
  const [renameOpen, setRenameOpen] = useState(false)
  const [moveOpen, setMoveOpen] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)

  const hasChildren = node.children.length > 0
  const isOpen = expanded.has(node.id)
  const active = activePageId === node.id
  const rowRef = useRef<HTMLDivElement | null>(null)

  // Scroll the row into view exactly once each time it transitions to active.
  // `block: 'nearest'` is a no-op when the row is already on-screen, so we
  // don't fight the user who has manually scrolled the sidebar. Fires on
  // mount-active too (which is the path that matters: the auto-expand effect
  // reveals a previously hidden ancestor → child node mounts already active).
  useEffect(() => {
    if (active && rowRef.current) {
      rowRef.current.scrollIntoView({ block: 'nearest' })
    }
  }, [active])

  async function handleNewChild() {
    onExpand(node.id)
    try {
      const created = await createPage.mutateAsync({
        space_id: spaceId,
        parent_id: node.id,
        title: UNTITLED_TITLE,
      })
      void navigate({
        to: '/spaces/$spaceId/pages/$pageId/{-$slug}',
        params: { spaceId, pageId: created.id, slug: undefined },
      })
    } catch {
      // Tree refetch surfaces failure on next interaction.
    }
  }

  return (
    <li className="m-0 p-0 list-none">
      <div
        ref={rowRef}
        className={cn(
          'group flex items-center gap-[var(--space-1)] pr-[var(--space-1)] rounded-[var(--radius-sm)]',
          'hover:bg-[var(--sidebar-item-hover)]',
          active &&
            'bg-[var(--sidebar-item-active)] shadow-[inset_2px_0_0_0_var(--sidebar-item-active-bar)]',
        )}
        style={{
          paddingLeft: `calc(var(--sidebar-indent) * ${depth} + var(--space-1))`,
        }}
      >
        {hasChildren ? (
          <button
            type="button"
            aria-label={isOpen ? 'Collapse' : 'Expand'}
            aria-expanded={isOpen}
            onClick={() => onToggle(node.id)}
            className="inline-flex items-center justify-center h-[var(--space-6)] w-[var(--space-4)] shrink-0 rounded-[var(--radius-xs)] bg-transparent border-0 cursor-pointer text-[var(--text-muted)] hover:text-[var(--text-primary)] outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]"
          >
            {isOpen ? (
              <ChevronDown width={13} height={13} />
            ) : (
              <ChevronRight width={13} height={13} />
            )}
          </button>
        ) : (
          <span
            aria-hidden="true"
            className="inline-block h-[var(--space-6)] w-[var(--space-4)] shrink-0"
          />
        )}

        <button
          type="button"
          data-keynav-item
          aria-current={active ? 'page' : undefined}
          onMouseEnter={() => prefetchPage(qc, node.id)}
          onFocus={() => prefetchPage(qc, node.id)}
          onClick={() =>
            void navigate({
              to: '/spaces/$spaceId/pages/$pageId/{-$slug}',
              params: { spaceId, pageId: node.id, slug: undefined },
            })
          }
          className={cn(
            'flex-1 min-w-0 text-left',
            'py-[var(--space-2)]',
            'font-[family-name:var(--font-sans)] text-[length:var(--text-sm)] leading-[var(--leading-tight)]',
            'text-[var(--text-primary)] bg-transparent border-0 cursor-pointer outline-none',
            'truncate',
            active && 'text-[var(--accent)] font-medium',
          )}
        >
          {node.title || (
            <span className="text-[var(--text-muted)]">{UNTITLED_TITLE}</span>
          )}
        </button>

        {/* Staleness marker — trailing, only when this page has background
            backfill outstanding (indexing and/or summaries). Hides on row hover
            to make room for the ⋯ menu. */}
        {staleLabels.has(node.id) ? (
          <span className="shrink-0 inline-flex items-center group-hover:hidden">
            <StalenessDot label={staleLabels.get(node.id)!} />
          </span>
        ) : null}

        {/* Exposure marker — trailing, only when the page is actually exposed.
            Reclaims the left gutter (titles align tight to the chevron). */}
        {node.exposure && node.exposure.state !== 'private' ? (
          <span className="shrink-0 inline-flex items-center justify-center group-hover:hidden">
            <VisibilityBadge
              state={node.exposure.state}
              inherited={node.exposure.inherited}
              compact
            />
          </span>
        ) : null}

        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              variant="ghost"
              size="sm"
              aria-label={`Actions for ${node.title || UNTITLED_TITLE}`}
              className={cn(
              'shrink-0 h-[var(--space-6)] w-[var(--space-6)] p-0 hidden group-hover:inline-flex data-[state=open]:inline-flex focus-visible:inline-flex',
              // No-hover devices (touch/iPad) can't reveal-on-hover, so surface
              // the menu on the active row only — keeps every other row clean.
              active && '[@media(hover:none)]:inline-flex',
            )}
            >
              <MoreHorizontal width={14} height={14} />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onSelect={() => setRenameOpen(true)}>
              Rename
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => void handleNewChild()}>
              New child page
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => setMoveOpen(true)}>
              Move…
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem destructive onSelect={() => setDeleteOpen(true)}>
              Delete
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      {hasChildren && isOpen ? (
        <ul className="m-0 p-0 list-none flex flex-col gap-[1px]">
          {node.children.map((child) => (
            <PageNode
              key={child.id}
              node={child}
              depth={depth + 1}
              spaceId={spaceId}
              activePageId={activePageId}
              expanded={expanded}
              onToggle={onToggle}
              onExpand={onExpand}
              allNodes={allNodes}
              staleLabels={staleLabels}
            />
          ))}
        </ul>
      ) : null}

      <RenamePageDialog
        node={node}
        open={renameOpen}
        onOpenChange={setRenameOpen}
      />
      <MovePageDialog
        node={node}
        spaceId={spaceId}
        roots={allNodes}
        open={moveOpen}
        onOpenChange={setMoveOpen}
      />
      <DeletePageDialog
        node={node}
        spaceId={spaceId}
        active={active}
        open={deleteOpen}
        onOpenChange={setDeleteOpen}
      />
    </li>
  )
}

interface RenamePageDialogProps {
  node: PageTreeNode
  open: boolean
  onOpenChange: (next: boolean) => void
}

function RenamePageDialog({ node, open, onOpenChange }: RenamePageDialogProps) {
  const [title, setTitle] = useState(node.title)
  const [error, setError] = useState<string | null>(null)
  const updatePage = useUpdatePage()

  function handleClose(next: boolean) {
    if (!next) {
      setTitle(node.title)
      setError(null)
    }
    onOpenChange(next)
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    const trimmed = title.trim()
    if (!trimmed) {
      setError('Title is required.')
      return
    }
    if (trimmed === node.title) {
      handleClose(false)
      return
    }
    setError(null)
    try {
      await updatePage.mutateAsync({ id: node.id, title: trimmed })
      handleClose(false)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Failed to rename page.')
    }
  }

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Rename page</DialogTitle>
        </DialogHeader>
        <form onSubmit={handleSubmit} className="flex flex-col gap-[var(--space-3)]">
          <div className="flex flex-col gap-[var(--space-2)]">
            <label
              htmlFor={`rename-page-${node.id}`}
              className="text-[length:var(--text-sm)] text-[var(--text-muted)]"
            >
              Title
            </label>
            <Input
              id={`rename-page-${node.id}`}
              autoFocus
              value={title}
              onChange={(e) => setTitle(e.target.value)}
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
            <Button type="submit" disabled={updatePage.isPending}>
              {updatePage.isPending ? 'Saving…' : 'Save'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

interface DeletePageDialogProps {
  node: PageTreeNode
  spaceId: number
  active: boolean
  open: boolean
  onOpenChange: (next: boolean) => void
}

function DeletePageDialog({
  node,
  spaceId,
  active,
  open,
  onOpenChange,
}: DeletePageDialogProps) {
  const [error, setError] = useState<string | null>(null)
  const navigate = useNavigate()
  const deletePage = useDeletePage()

  function handleClose(next: boolean) {
    if (!next) setError(null)
    onOpenChange(next)
  }

  async function handleDelete() {
    setError(null)
    try {
      await deletePage.mutateAsync({ id: node.id, spaceId })
      handleClose(false)
      if (active) {
        void navigate({ to: '/spaces/$spaceId', params: { spaceId } })
      }
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Failed to delete page.')
    }
  }

  const childCount = node.children.length

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Delete this page?</DialogTitle>
          <DialogDescription>
            "{node.title || UNTITLED_TITLE}"
            {childCount > 0
              ? ` and its ${childCount} ${
                  childCount === 1 ? 'child page' : 'child pages'
                }`
              : ''}{' '}
            move to this space&apos;s Trash. You can put them back from there — the
            space&apos;s Trash tab.
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
            disabled={deletePage.isPending}
          >
            {deletePage.isPending ? 'Deleting…' : 'Delete'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
