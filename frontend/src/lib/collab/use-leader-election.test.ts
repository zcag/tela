import { describe, expect, it } from 'vitest'
import * as Y from 'yjs'
import { Awareness, removeAwarenessStates } from 'y-protocols/awareness'
import { computeIsLeader } from './use-leader-election'

// Mirrors useCollabSession: the session seeds local awareness once its mount
// effect claims it, which is what makes this peer visible to leader election.
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

  it('skips an unidentified peer even when it sorts lowest', () => {
    // The orphan case: a provider from a discarded render sits in the room with
    // a bare {} state and no editor behind it. Electing it (lowest clientID
    // wins) made the real editor a permanent non-leader — every keystroke went
    // to the Yjs overlay and pages.body was never PATCHed again, so view mode
    // silently froze while edit mode looked right.
    const states: Array<[number, unknown]> = [
      [10, {}],
      [20, { user: { id: 1, username: 'cagdassalur' } }],
    ]
    const us = {
      clientID: 20,
      getStates: () => new Map(states),
    } as unknown as Awareness
    expect(computeIsLeader(us)).toBe(true)
  })

  it('elects the lowest identified peer when several have editors', () => {
    const states: Array<[number, unknown]> = [
      [10, {}],
      [20, { user: { id: 1 } }],
      [30, { user: { id: 2 } }],
    ]
    const mk = (clientID: number) =>
      ({ clientID, getStates: () => new Map(states) }) as unknown as Awareness
    expect(computeIsLeader(mk(20))).toBe(true)
    expect(computeIsLeader(mk(30))).toBe(false)
  })

  it('falls back to all peers while nobody has identified yet', () => {
    // The beat between the awareness seed and PageView's user seed: no `user`
    // anywhere. Erring toward a leader keeps the room from going saveless.
    const states: Array<[number, unknown]> = [
      [10, {}],
      [20, {}],
    ]
    const mk = (clientID: number) =>
      ({ clientID, getStates: () => new Map(states) }) as unknown as Awareness
    expect(computeIsLeader(mk(10))).toBe(true)
    expect(computeIsLeader(mk(20))).toBe(false)
  })
})
