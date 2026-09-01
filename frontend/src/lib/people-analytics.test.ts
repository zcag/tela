import { describe, expect, it } from 'vitest'
import {
  buildCohorts,
  isNewcomer,
  mondayOf,
  trendDelta,
  type CohortRow,
} from './people-analytics'

describe('trendDelta', () => {
  it('compares the last four weeks against the four before them', () => {
    // prior = 4, recent = 8 → doubled.
    expect(trendDelta([1, 1, 1, 1, 2, 2, 2, 2])).toBe(100)
    expect(trendDelta([2, 2, 2, 2, 1, 1, 1, 1])).toBe(-50)
    expect(trendDelta([3, 3, 3, 3, 3, 3, 3, 3])).toBe(0)
  })

  it('reads only the trailing eight weeks of a longer series', () => {
    // The busy early weeks must not leak into the comparison: trailing eight
    // are prior=8, recent=12.
    expect(trendDelta([9, 9, 9, 9, 9, 9, 2, 2, 2, 2, 3, 3, 3, 3])).toBe(50)
  })

  it('is null when the recent window follows a silent prior one, however long the history', () => {
    expect(trendDelta([7, 7, 7, 7, 7, 7, 0, 0, 0, 0, 1, 1, 1, 1])).toBeNull()
  })

  it('is null with no prior activity — a first week is not infinite growth', () => {
    expect(trendDelta([0, 0, 0, 0, 3, 3, 3, 3])).toBeNull()
    expect(trendDelta([0, 0, 0, 0, 0, 0, 0, 0])).toBeNull()
  })

  it('is null when the series is too short to hold both windows', () => {
    expect(trendDelta([1, 2, 3])).toBeNull()
    expect(trendDelta([])).toBeNull()
  })
})

describe('isNewcomer', () => {
  it('is true when nothing happened before the comparison window', () => {
    expect(isNewcomer([0, 0, 0, 0, 2, 2, 2, 2])).toBe(true)
  })
  it('is false once there is any earlier history', () => {
    expect(isNewcomer([0, 0, 1, 0, 2, 2, 2, 2])).toBe(false)
  })
})

describe('mondayOf', () => {
  it('rewinds to the Monday of that week, matching the server axis', () => {
    expect(mondayOf('2026-09-03 09:00:00')).toBe('2026-08-31') // Thursday
    expect(mondayOf('2026-08-31 00:00:00')).toBe('2026-08-31') // Monday itself
    expect(mondayOf('2026-09-06 23:59:59')).toBe('2026-08-31') // Sunday, same week
    expect(mondayOf('2026-09-07 00:00:00')).toBe('2026-09-07') // next Monday
  })

  it('treats the wire timestamp as UTC, not local time', () => {
    // 00:30 UTC on a Monday must not fall back into the previous week for a
    // viewer behind UTC.
    expect(mondayOf('2026-08-31 00:30:00')).toBe('2026-08-31')
  })

  it('returns empty for junk rather than throwing', () => {
    expect(mondayOf('not a date')).toBe('')
  })
})

describe('buildCohorts', () => {
  const weeks = ['2026-08-10', '2026-08-17', '2026-08-24', '2026-08-31']
  const row = (created: string, series: number[]): CohortRow => ({
    created_at: `${created} 12:00:00`,
    metrics: { weeks: series },
  })

  it('groups by signup week and measures later activity, newest cohort first', () => {
    const cohorts = buildCohorts(
      [
        // Joined week 0; active in weeks 0 and 2.
        row('2026-08-10', [1, 0, 3, 0]),
        // Joined week 0; active only in week 0 — half the cohort retained at W1.
        row('2026-08-12', [2, 0, 0, 0]),
        // Joined week 2, active ever since.
        row('2026-08-25', [0, 0, 4, 4]),
      ],
      weeks,
    )
    expect(cohorts.map((c) => c.week)).toEqual(['2026-08-24', '2026-08-10'])

    const first = cohorts[1]
    expect(first.size).toBe(2)
    // W0 both active, W1 neither, W2 one of two, W3 neither.
    expect(first.retention).toEqual([100, 0, 50, 0])

    const later = cohorts[0]
    expect(later.size).toBe(1)
    // Only two weeks have elapsed for this cohort — the array stops there
    // rather than padding zeros that would read as churn.
    expect(later.retention).toEqual([100, 100])
  })

  it('drops signups from before the axis instead of bucketing them wrong', () => {
    const cohorts = buildCohorts([row('2026-01-05', [1, 1, 1, 1])], weeks)
    expect(cohorts).toEqual([])
  })

  it('treats a row with no metrics as inactive rather than crashing', () => {
    const cohorts = buildCohorts(
      [{ created_at: '2026-08-10 09:00:00', metrics: null }],
      weeks,
    )
    expect(cohorts[0].retention).toEqual([0, 0, 0, 0])
  })

  it('returns nothing when there is no axis', () => {
    expect(buildCohorts([row('2026-08-10', [1])], [])).toEqual([])
  })
})
