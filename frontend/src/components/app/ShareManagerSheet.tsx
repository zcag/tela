import {
  useCreateShare,
  useRevokeShare,
  useSharesForPage,
  useUpdateShare,
} from '../../lib/queries/share'
import {
  Sheet,
  SheetBody,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '../ui/sheet'
import { CreateShareForm } from './ShareManagerSheet-create-form'
import { ShareRow } from './ShareManagerSheet-row'
import { shareErrorMessage } from './ShareManagerSheet-utils'

interface ShareManagerSheetProps {
  pageId: number
  // A draft's share links resolve as not found until the page is published.
  isDraft?: boolean
  open: boolean
  onOpenChange: (next: boolean) => void
}

export function ShareManagerSheet({
  pageId,
  isDraft = false,
  open,
  onOpenChange,
}: ShareManagerSheetProps) {
  const sharesQuery = useSharesForPage(pageId)
  const createShare = useCreateShare(pageId)
  const updateShare = useUpdateShare()
  const revokeShare = useRevokeShare()

  // Active shares only — the backend default already filters revoked, but we
  // also drop any in-flight `revoked_at` to keep the row from flickering
  // before invalidation lands.
  const shares = (sharesQuery.data ?? []).filter((s) => !s.revoked_at)

  return (
    // modal={false} — the share manager sits beside the editor; opening it
    // mid-edit must not steal the caret or trap focus. Mirrors the M8
    // CommentsPanel side-panel-Sheet pattern.
    <Sheet open={open} onOpenChange={onOpenChange} modal={false}>
      <SheetContent
        side="right"
        className="flex flex-col"
        withOverlay={false}
        // Don't auto-pull focus out of the editor on open — preserves the
        // user's selection / caret position.
        onOpenAutoFocus={(e) => e.preventDefault()}
        // Radix dispatches `interactOutside` for both real pointer clicks
        // AND focus-loss events from sibling floating layers (e.g. the
        // kebab DropdownMenu inside a row). Allow real clicks outside to
        // dismiss the sheet, but ignore Radix-dispatched focus loss so
        // nested popovers don't immediately close it.
        onInteractOutside={(e) => {
          if (e.detail.originalEvent.type !== 'pointerdown')
            e.preventDefault()
        }}
      >
        <SheetHeader>
          <SheetTitle>Share this page</SheetTitle>
          <SheetDescription>
            Anyone with the link can read this page. Optional password and
            subtree options below.
          </SheetDescription>
          {isDraft ? (
            <p className="text-[length:var(--text-sm)] text-[var(--text-muted)]">
              This page is a <strong>draft</strong>: its links won&rsquo;t open until you
              publish it (••• → Publish).
            </p>
          ) : null}
        </SheetHeader>

        <SheetBody className="flex flex-col gap-[var(--space-5)]">
          <section
            aria-labelledby={`share-active-${pageId}`}
            className="flex flex-col gap-[var(--space-3)]"
          >
            <h3
              id={`share-active-${pageId}`}
              className="m-0 text-[length:var(--text-xs)] uppercase tracking-wider text-[var(--text-muted)] font-[family-name:var(--font-sans)]"
            >
              Active shares
            </h3>
            {sharesQuery.isLoading ? (
              <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
                Loading…
              </p>
            ) : sharesQuery.isError ? (
              <p
                role="alert"
                className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]"
              >
                {shareErrorMessage(sharesQuery.error)}
              </p>
            ) : shares.length === 0 ? (
              <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)] font-[family-name:var(--font-sans)]">
                No active shares yet. Create one below.
              </p>
            ) : (
              <ul className="m-0 p-0 list-none flex flex-col gap-[var(--space-3)]">
                {shares.map((share) => (
                  <li key={share.id} className="m-0 p-0 list-none">
                    <ShareRow
                      share={share}
                      onUpdate={(patch) =>
                        updateShare.mutateAsync({
                          id: share.id,
                          pageId,
                          patch,
                        })
                      }
                      onRevoke={() =>
                        revokeShare.mutateAsync({ id: share.id, pageId })
                      }
                    />
                  </li>
                ))}
              </ul>
            )}
          </section>

          <CreateShareForm
            pending={createShare.isPending}
            error={createShare.error}
            onCreate={(input) => createShare.mutateAsync(input)}
            onReset={() => createShare.reset()}
          />
        </SheetBody>
      </SheetContent>
    </Sheet>
  )
}
