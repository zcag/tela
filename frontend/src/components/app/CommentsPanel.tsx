import { useMemo } from 'react'
import type { CommentAnchor } from '../../lib/comments/anchor'
import {
  useComments,
  useCreateComment,
  useDeleteComment,
  useUpdateComment,
} from '../../lib/comments/use-comments'
import {
  Sheet,
  SheetBody,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '../ui/sheet'
import { Toggle } from '../ui/toggle'
import { CommentComposer } from './CommentComposer'
import { CommentThread } from './CommentThread'

interface CommentsPanelProps {
  pageId: number
  open: boolean
  onOpenChange: (open: boolean) => void
  // Live editor selection state. The composer is disabled while empty.
  hasSelection: boolean
  // Snapshot the current PM selection into a CommentAnchor at submit time.
  captureAnchor: () => CommentAnchor | null
  // Live anchor preview (selection text) so users see what they're commenting
  // on without leaving focus on the editor. Empty when selection is empty.
  anchorPreview: string | null
  // Current viewer — needed for optimistic author + edit/delete gates.
  me: { id: number; username: string }
  // True iff the viewer's space role is 'owner'. Owners can delete any
  // comment in the page.
  isSpaceOwner: boolean
  // M8.4 — thread root ids whose anchor failed to resolve against the
  // current editor text. Owned by PageView (populated by the editor's
  // anchor-decoration plugin), passed through so CommentThread can render
  // an "Orphaned" tag.
  orphanIds: Set<number>
  // M8.5 — controls the "Show resolved (N)" filter. Lifted to PageView so
  // the editor's in-body anchor decoration stays in sync with whatever
  // mode the panel is in (resolved threads paint a muted underline only
  // when showResolved is true).
  showResolved: boolean
  onShowResolvedChange: (next: boolean) => void
  // When false, the new-comment composer is hidden (read view — no selection).
  canCompose?: boolean
  // See CommentComposer.focusSignal.
  composeFocusSignal?: number
}

export function CommentsPanel({
  pageId,
  open,
  onOpenChange,
  hasSelection,
  captureAnchor,
  anchorPreview,
  me,
  isSpaceOwner,
  orphanIds,
  showResolved,
  onShowResolvedChange,
  // Hidden in read view, where there's no editor selection to anchor a new
  // comment to (reading + replying to existing threads still works).
  canCompose = true,
  composeFocusSignal,
}: CommentsPanelProps) {
  // Backend orders threads ASC by created_at; show newest at top in the panel.
  const commentsQuery = useComments({ pageId })
  const createComment = useCreateComment(pageId, me)
  const updateComment = useUpdateComment(pageId)
  const deleteComment = useDeleteComment(pageId)

  const threadsData = commentsQuery.data
  const threads = threadsData ?? []
  const totalOpenCount = threads.filter((t) => !t.root.resolved).length
  const totalResolvedCount = threads.length - totalOpenCount

  const displayThreads = useMemo(() => {
    // Newest first. M8.5 — hide resolved threads when the filter is off so
    // the panel reads as "what still needs attention" by default.
    if (!threadsData) return []
    const filtered = showResolved
      ? threadsData
      : threadsData.filter((t) => !t.root.resolved)
    return [...filtered].reverse()
  }, [threadsData, showResolved])

  async function handleCreateRoot(input: {
    body: string
    anchor_prefix: string
    anchor_exact: string
    anchor_suffix: string
  }) {
    await createComment.mutateAsync({
      body: input.body,
      anchor_prefix: input.anchor_prefix,
      anchor_exact: input.anchor_exact,
      anchor_suffix: input.anchor_suffix,
    })
  }

  async function handleReply(parentId: number, body: string) {
    await createComment.mutateAsync({ body, parent_id: parentId })
  }

  async function handleEdit(id: number, body: string) {
    await updateComment.mutateAsync({ id, body })
  }

  async function handleDelete(id: number) {
    await deleteComment.mutateAsync({ id })
  }

  async function handleToggleResolved(id: number, resolved: boolean) {
    await updateComment.mutateAsync({ id, resolved })
  }

  return (
    // modal={false} — the comments panel sits beside the editor; the user
    // must be able to select new passages without first dismissing the
    // panel. Radix Dialog's default modal behaviour would trap focus and
    // block pointer events outside the sheet, which makes "select text →
    // type comment" impossible mid-session.
    <Sheet open={open} onOpenChange={onOpenChange} modal={false}>
      <SheetContent
        side="right"
        className="flex flex-col"
        withOverlay={false}
        // Prevent Radix from auto-pulling focus out of the editor on
        // initial mount — composer focus is opt-in (the user has to click
        // the textarea). Without this, opening the panel would steal a
        // freshly-made selection.
        onOpenAutoFocus={(e) => e.preventDefault()}
        // Same reasoning on close — return focus to the editor naturally
        // by leaving DOM focus where it was, instead of bouncing it to
        // the trigger button.
        onCloseAutoFocus={(e) => e.preventDefault()}
        // Clicks outside the sheet (e.g. into the editor to make a new
        // selection) must NOT close the panel.
        onInteractOutside={(e) => e.preventDefault()}
      >
        <SheetHeader>
          <SheetTitle>Comments</SheetTitle>
          <SheetDescription>
            {totalOpenCount === 0
              ? 'No open threads on this page yet.'
              : totalOpenCount === 1
                ? '1 open thread on this page.'
                : `${totalOpenCount} open threads on this page.`}
          </SheetDescription>
          {totalResolvedCount > 0 ? (
            <div className="mt-[var(--space-2)]">
              <Toggle
                size="sm"
                pressed={showResolved}
                onPressedChange={onShowResolvedChange}
                aria-label={`Show ${totalResolvedCount} resolved ${totalResolvedCount === 1 ? 'thread' : 'threads'}`}
              >
                Show resolved ({totalResolvedCount})
              </Toggle>
            </div>
          ) : null}
        </SheetHeader>

        <SheetBody className="flex flex-col gap-[var(--space-4)]">
          {canCompose ? (
            <CommentComposer
              hasSelection={hasSelection}
              captureAnchor={captureAnchor}
              anchorPreview={anchorPreview}
              onSubmit={handleCreateRoot}
              focusSignal={composeFocusSignal}
            />
          ) : null}

          {commentsQuery.isLoading ? (
            <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
              Loading…
            </p>
          ) : commentsQuery.isError ? (
            <p
              role="alert"
              className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]"
            >
              Couldn't load comments.
            </p>
          ) : displayThreads.length === 0 ? (
            <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
              No comments yet. Select a passage in the page and write the
              first one.
            </p>
          ) : (
            <ul className="m-0 p-0 flex flex-col gap-[var(--space-3)]">
              {displayThreads.map((thread) => (
                <CommentThread
                  key={thread.root.id}
                  thread={thread}
                  currentUserId={me.id}
                  isSpaceOwner={isSpaceOwner}
                  onEditComment={handleEdit}
                  onDeleteComment={handleDelete}
                  onReply={handleReply}
                  onToggleResolved={handleToggleResolved}
                  isOrphan={orphanIds.has(thread.root.id)}
                />
              ))}
            </ul>
          )}
        </SheetBody>
      </SheetContent>
    </Sheet>
  )
}
