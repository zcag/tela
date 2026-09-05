import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../api'
import { emitPageMutation } from '../pageMutationEvent'
import { notifyBodyIndexUpdate, pageKeys } from './pages'
import type { Page } from '../types'

// The bin (GET /api/spaces/{id}/trash) and putting something back
// (POST /api/pages/{id}/restore). Deleting has always been reversible — the row
// is only stamped, never removed — but until this there was no way to see or
// undo one, so a wrong delete looked exactly like a page that never existed.
//
// The list is scoped server-side: your own deletes, or all of them if you own
// the space. So in a shared space this is your bin, not a public record of what
// everyone has removed.

export interface TrashEntry {
  id: number
  title: string
  deleted_at: string
  // Sub-pages that went down with it and come back with it. The bin lists only
  // the page the delete was aimed at, never these.
  sub_pages: number
  parent_id?: number
  parent_title?: string
  // Who removed it and through what. Empty for pages deleted before tela
  // recorded that — those show to space owners only. deleted_via is
  // 'manual' | 'agent' | 'sync'.
  deleted_by?: string
  deleted_by_you: boolean
  deleted_via?: string
}

export const trashKeys = {
  space: (spaceId: number) => ['space-trash', spaceId] as const,
}

export function useSpaceTrash(spaceId: number | null | undefined, enabled = true) {
  return useQuery({
    queryKey: trashKeys.space(spaceId ?? -1),
    queryFn: () =>
      api<{ pages: TrashEntry[] }>(`/api/spaces/${spaceId}/trash`).then((r) => r.pages),
    enabled: enabled && spaceId != null,
  })
}

// Destroying a page for good — the one irreversible delete in the product, so
// every caller must confirm first. Scoped like restore server-side: your own
// deletes, or anything in the space if you own it.
export function usePurgePage() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id }: { id: number; spaceId: number }) =>
      api<void>(`/api/pages/${id}/purge`, { method: 'POST' }),
    onSuccess: (_void, vars) => {
      void qc.invalidateQueries({ queryKey: trashKeys.space(vars.spaceId) })
    },
  })
}

// Empties what YOU can see: your own deletes, or the whole bin if you own the
// space. Returns how many roots went.
export function useEmptyTrash() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ spaceId }: { spaceId: number }) =>
      api<{ purged: number }>(`/api/spaces/${spaceId}/trash`, { method: 'DELETE' }),
    onSuccess: (_res, vars) => {
      void qc.invalidateQueries({ queryKey: trashKeys.space(vars.spaceId) })
    },
  })
}

export function useRestorePage() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id }: { id: number; spaceId: number }) =>
      api<{ page: Page }>(`/api/pages/${id}/restore`, { method: 'POST' }).then((r) => r.page),
    onSuccess: (page, vars) => {
      void qc.invalidateQueries({ queryKey: trashKeys.space(vars.spaceId) })
      void qc.invalidateQueries({ queryKey: pageKeys.space(vars.spaceId) })
      // Sub-pages come back too, so the whole space view is stale — emitPageMutation
      // is what the tree, sidebar and space overview all listen on.
      emitPageMutation()
      notifyBodyIndexUpdate(page)
    },
  })
}
