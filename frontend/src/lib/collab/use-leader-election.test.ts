import { describe, expect, it } from 'vitest'
import * as Y from 'yjs'
import { Awareness, removeAwarenessStates } from 'y-protocols/awareness'
import { computeIsLeader } from './use-leader-election'

// Mirrors useCollabSession: the session seeds local awareness at construction,
// which is what makes this peer visible to leader election at all.
function seededAwareness(): Awareness {
  const aw = new Awareness(new Y.Doc())
  aw.setLocalState({})
  return aw
}

describe('computeIsLeader', () => {
  it('elects the lowest clientID among peers', () => {
    const aw = seededAwareness()
    const states: Array<[number, unknown]> = [
      [aw.clientID, {}],
      [aw.clientID + 1, {}],
    ]
    const lower = {
      clientID: aw.clientID,
      getStates: () => new Map(states),
    } as unknown as Awareness
    const higher = {
      clientID: aw.clientID + 1,
      getStates: () => new Map(states),
    } as unknown as Awareness
    expect(computeIsLeader(lower)).toBe(true)
    expect(computeIsLeader(higher)).toBe(false)
  })

  it('makes a freshly seeded solo peer the leader', () => {
    expect(computeIsLeader(seededAwareness())).toBe(true)
  })

  it('keeps saving after our own awareness entry is removed', () => {
    // TelaProvider drops the local entry on `pagehide`. y-protocols only
    // re-pings a local state that still exists, so nothing restores it — the
    // map stays empty. Returning false here made the tab a permanent
    // non-leader: it kept editing the Y.Doc but never PATCHed pages.body
    // again, and the work survived only in the (droppable) Yjs overlay.
    const aw = seededAwareness()
    removeAwarenessStates(aw, [aw.clientID], 'pagehide')
    expect(aw.getStates().size).toBe(0)
    expect(aw.getLocalState()).toBeNull()
    expect(computeIsLeader(aw)).toBe(true)
  })

  it('is not leader with no awareness at all (non-collab path)', () => {
    expect(computeIsLeader(null)).toBe(false)
  })
})
