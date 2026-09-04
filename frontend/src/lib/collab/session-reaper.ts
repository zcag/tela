// Reaper for collab sessions that were built but never mounted.
//
// A CollabSession is constructed during RENDER — the Milkdown editor factory
// captures it synchronously, so building it in an effect is too late. React is
// free to throw a render away (a concurrent re-render that never commits,
// StrictMode's double invoke, a component that suspends and resumes on a fresh
// fiber), and the discarded copy's teardown effect never runs. Its WebSocket
// stays OPEN and y-protocols renews its awareness entry every 15s, so the 30s
// outdated-GC never reaps it either: the orphan sits in the room for the life
// of the tab.
//
// That is not merely untidy. Leader election picks the lowest clientID in the
// awareness map, so an orphan that sorts below the live editor wins the save
// leadership — and with no editor behind it, nothing ever PATCHes pages.body
// again. Edits keep flowing into the Yjs overlay, so edit mode looks correct
// while view mode (which renders the body) stays frozen at the last snapshot,
// with no error anywhere. Observed in the wild: one tab holding three
// providers, the oldest an unmounted orphan that had been renewing its bare
// `{}` awareness entry for five hours.
//
// So every render-built session is parked here and only survives if a mount
// effect claims it. Anything still unclaimed when the grace window elapses was
// never committed, and is destroyed.

// Structural — the reaper only ever needs to close a session down, and staying
// off CollabSession keeps this module free of React/Yjs imports so it unit
// tests in the node environment.
export interface ReapableSession {
  provider: { destroy(): void }
}

// This window is also how long an orphan can be SEEN by the room, and that is
// not free: y-protocols' Awareness seeds its own `{}` local state in the
// constructor and renews the clock every 15s, and TelaProvider forwards that
// renewal, so an unclaimed provider with an open ws is in every peer's map for
// most of the grace. What makes it harmless is leader election skipping peers
// with no `user` (use-leader-election.ts). The grace stays long because the
// opposite failure is worse: reaping a session whose commit was merely slow
// leaves a mounted editor bound to a dead provider.
const UNCLAIMED_GRACE_MS = 30_000

const pending = new Map<ReapableSession, ReturnType<typeof setTimeout>>()

// Park a session built during render. Idempotent per session.
export function trackUnclaimed(
  session: ReapableSession,
  graceMs: number = UNCLAIMED_GRACE_MS,
): void {
  if (pending.has(session)) return
  pending.set(
    session,
    setTimeout(() => {
      pending.delete(session)
      session.provider.destroy()
    }, graceMs),
  )
}

// Called from the mount effect: this session reached a committed component, so
// cancel its reaping. Returns whether it was still pending (false = already
// claimed, or reaped out from under us).
export function claimSession(session: ReapableSession): boolean {
  const timer = pending.get(session)
  if (timer === undefined) return false
  clearTimeout(timer)
  pending.delete(session)
  return true
}
