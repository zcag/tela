import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { claimSession, trackUnclaimed, type ReapableSession } from './session-reaper'

function fakeSession(): ReapableSession & { destroyed: number } {
  const s = { destroyed: 0, provider: { destroy: () => { s.destroyed += 1 } } }
  return s
}

describe('session-reaper', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it('destroys a session no mount effect ever claimed', () => {
    // The discarded-render case: the provider opened a ws during a render React
    // threw away, so no teardown effect will ever run for it.
    const s = fakeSession()
    trackUnclaimed(s, 1000)
    vi.advanceTimersByTime(999)
    expect(s.destroyed).toBe(0)
    vi.advanceTimersByTime(1)
    expect(s.destroyed).toBe(1)
  })

  it('leaves a claimed session alone forever', () => {
    const s = fakeSession()
    trackUnclaimed(s, 1000)
    expect(claimSession(s)).toBe(true)
    vi.advanceTimersByTime(60_000)
    expect(s.destroyed).toBe(0)
  })

  it('reports an unknown session as unclaimable and does not destroy it', () => {
    // A second claim (StrictMode's re-run of the mount effect) must not be read
    // as "reaped" by callers, and must never tear the live session down.
    const s = fakeSession()
    trackUnclaimed(s, 1000)
    claimSession(s)
    expect(claimSession(s)).toBe(false)
    vi.advanceTimersByTime(60_000)
    expect(s.destroyed).toBe(0)
  })

  it('tracks each session independently', () => {
    const kept = fakeSession()
    const orphan = fakeSession()
    trackUnclaimed(kept, 1000)
    trackUnclaimed(orphan, 1000)
    claimSession(kept)
    vi.advanceTimersByTime(1000)
    expect(kept.destroyed).toBe(0)
    expect(orphan.destroyed).toBe(1)
  })

  it('ignores a repeat track of the same session', () => {
    const s = fakeSession()
    trackUnclaimed(s, 1000)
    trackUnclaimed(s, 1000)
    vi.advanceTimersByTime(60_000)
    expect(s.destroyed).toBe(1)
  })
})
