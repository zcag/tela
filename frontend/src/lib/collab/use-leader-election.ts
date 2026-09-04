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
//   - awareness map is empty → TRUE (claim leadership). Our own entry is there
//     from construction (y-protocols' Awareness constructor calls
//     setLocalState({}) itself), so "empty" does not mean "not ready" — it
//     means our entry was REMOVED, which TelaProvider does on `pagehide`.
//     y-protocols only re-pings a local state that still exists, so nothing
//     ever puts it back on its own: the peer stayed a non-leader forever,
//     silently stopped PATCHing pages.body, and every later keystroke lived
//     only in the Yjs overlay until an agent write dropped it. Erring toward "not leader" trades a wasted duplicate
//     save (harmless — last-write-wins on identical serialized markdown) for
//     total data loss, so the fallback points the other way.
//   - a peer with no `user` in its state is skipped, unless nobody in the room
//     has identified yet. `user` is seeded once an editor has actually mounted
//     (PageView / CollabGrid), so a bare `{}` entry is a socket in the room
//     with no editor behind it — an orphan from a render React discarded
//     (session-reaper.ts). This build reaps its own orphans, but only after a
//     grace window, and a peer on another device still running the old bundle
//     keeps one for the life of its tab; electing either ends every write to
//     pages.body with nothing to show for it. The nobody-identified fallback
//     covers the beat between construction and the user seed, so the room is
//     never leaderless.
//
// Multi-tab: the awareness wire-bridge (#65) means peers do see each other, so
// two tabs on one page elect a single saver between them rather than both
// PATCHing.
//
// Uses useSyncExternalStore — the idiomatic React API for syncing to an
// external observable, which handles the render/subscribe race React's
// useEffect can't (an awareness 'change' that fires between render and
// effect-mount would otherwise be lost).
export function computeIsLeader(awareness: Awareness | null): boolean {
  if (!awareness) return false
  const states = awareness.getStates()
  if (states.size === 0) return true
  const identified: number[] = []
  for (const [id, state] of states) {
    if (state && (state as { user?: unknown }).user != null) identified.push(id)
  }
  const pool = identified.length > 0 ? identified : [...states.keys()]
  let minId = Infinity
  for (const id of pool) {
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
