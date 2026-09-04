import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../api'
import { emitPageMutation } from '../pageMutationEvent'
import { notifyBodyIndexUpdate, pageKeys } from './pages'
import type { Page } from '../types'

// The bin (GET /api/spaces/{id}/trash) and putting something back
// (POST /api/pages/{id}/restore). Deleting has always been reversible — the row
// is only stamped, never removed — but until this there was no way to see or
// undo one, so a wrong delete looked exactly like a page that never existed.

export interface TrashEntry {
  id: number
  title: string
  deleted_at: string
  // Sub-pages that went down with it and come back with it. The bin lists only
  // the page the delete was aimed at, never these.
  sub_pages: number
  parent_id?: number
  parent_title?: string
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
