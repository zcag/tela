import { useCallback, useSyncExternalStore } from 'react'
import type { Awareness } from 'y-protocols/awareness'

// M7.3 leader election.
//
// Returns whether this peer is the elected save-leader for the room. The
// leader is the peer with the LOWEST awareness `clientID` currently in the
// awareness map. Recomputed on every awareness 'change' event.
//
// Why elect a leader: Yjs has already converged the doc across all peers,
// so every peer in the room would serialize the same markdown body. If
// every peer PATCHed `/api/pages/{id}` on debounce, we'd get N×writes for
// no benefit (last-writer-wins is correct but wasteful). One designated
// leader saves; everyone else skips.
//
// Fall-back rules:
//   - awareness is null (no collab session) → false. Callers gate the
//     non-collab path differently (legacy single-author path is unconditional).
//   - awareness map is empty → TRUE (claim leadership). The map always
//     contains our own entry once the session seeds it (useCollabSession
//     calls setLocalState({}) at construction), so "empty" does not mean
//     "not ready" — it means our entry was REMOVED, which TelaProvider does
//     on `pagehide`. y-protocols only re-pings a local state that still
//     exists, so nothing ever puts it back on its own: the peer stayed a
//     non-leader forever, silently stopped PATCHing pages.body, and every
//     later keystroke lived only in the Yjs overlay until an agent write
//     dropped it. Erring toward "not leader" trades a wasted duplicate save
//     (harmless — last-write-wins on identical serialized markdown) for
//     total data loss, so the fallback points the other way.
//
// Multi-tab note: until the awareness wire-bridge ships in #65, only this
// peer's local state is in the map (the editor seeds it via
// `setLocalState({})` on collab init). So one-tab-per-page trivially sees
// itself as leader, preserving single-tab save behaviour. Multi-tab still
// produces duplicate saves until #65 lands and peers see each other.
//
// Uses useSyncExternalStore — the idiomatic React API for syncing to an
// external observable, which handles the render/subscribe race React's
// useEffect can't (an awareness 'change' that fires between render and
// effect-mount would otherwise be lost).
export function computeIsLeader(awareness: Awareness | null): boolean {
  if (!awareness) return false
  const states = awareness.getStates()
  if (states.size === 0) return true
  let minId = Infinity
  for (const id of states.keys()) {
    if (id < minId) minId = id
  }
  return minId === awareness.clientID
}

export function useLeaderElection(awareness: Awareness | null): boolean {
  const subscribe = useCallback(
    (onStoreChange: () => void) => {
      if (!awareness) return () => {}
      awareness.on('change', onStoreChange)
      return () => {
        awareness.off('change', onStoreChange)
      }
    },
    [awareness],
  )
  const getSnapshot = useCallback(
    () => computeIsLeader(awareness),
    [awareness],
  )
  return useSyncExternalStore(subscribe, getSnapshot)
}
