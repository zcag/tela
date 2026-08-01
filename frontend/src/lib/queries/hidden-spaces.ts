import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../api'

// Per-user hidden spaces. Hiding is decluttering, NOT access control: it only
// tucks a space behind the sidebar tree's "Show hidden" row — /spaces, search
// and the command palette keep listing it, and the space stays reachable by URL.
// Backed by /api/users/me/hidden-spaces + /api/spaces/{id}/hide (backend
// hidden_spaces.go). Ids only — SpaceTree already holds the Space objects from
// useSpaces() and partitions them by this set.

export const hiddenSpaceKeys = {
  all: ['hidden-spaces'] as const,
  list: () => [...hiddenSpaceKeys.all, 'list'] as const,
}

export function useHiddenSpaces() {
  return useQuery({
    queryKey: hiddenSpaceKeys.list(),
    queryFn: async () => {
      const { hidden_spaces } = await api<{
        hidden_spaces: { space_id: number; created_at: string }[]
      }>('/api/users/me/hidden-spaces')
      return hidden_spaces.map((h) => h.space_id)
    },
    staleTime: 15_000,
  })
}

export function useToggleHiddenSpace() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ spaceId, hidden }: { spaceId: number; hidden: boolean }) => {
      if (hidden) {
        await api<void>(`/api/spaces/${spaceId}/hide`, { method: 'DELETE' })
      } else {
        await api<{ is_hidden: boolean }>(`/api/spaces/${spaceId}/hide`, {
          method: 'PUT',
        })
      }
      return spaceId
    },
    onMutate: async ({ spaceId, hidden }) => {
      await qc.cancelQueries({ queryKey: hiddenSpaceKeys.list() })
      const previous = qc.getQueryData<number[]>(hiddenSpaceKeys.list())
      qc.setQueryData<number[]>(hiddenSpaceKeys.list(), (curr) => {
        const list = curr ?? []
        return hidden ? list.filter((id) => id !== spaceId) : [spaceId, ...list]
      })
      return { previous }
    },
    onError: (_err, _vars, ctx) => {
      if (ctx?.previous) qc.setQueryData(hiddenSpaceKeys.list(), ctx.previous)
    },
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: hiddenSpaceKeys.list() })
    },
  })
}
