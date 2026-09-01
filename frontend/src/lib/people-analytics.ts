// people-analytics.ts — the pure arithmetic behind the admin People table's
// trend column and cohort grid. Extracted from the component so it can be
// tested without a DOM: the week bucketing and the incomplete-cohort handling
// are the parts most likely to be quietly wrong, and "it rendered" is not a
// check on either.

// Weeks per side of the trend delta: the last 4 against the 4 before them.
export const DELTA_WEEKS = 4

// Percent change in active days across the two windows. Null when there is no
// prior activity to compare against — an account's first weeks are not
// "+∞% growth", they're just new, and the caller says so in words instead.
export function trendDelta(weeks: number[]): number | null {
  if (weeks.length < DELTA_WEEKS * 2) return null
  const sum = (xs: number[]) => xs.reduce((a, b) => a + b, 0)
  const recent = sum(weeks.slice(-DELTA_WEEKS))
  const prior = sum(weeks.slice(-DELTA_WEEKS * 2, -DELTA_WEEKS))
  if (prior === 0) return null
  return Math.round(((recent - prior) / prior) * 100)
}

// True when every week before the comparison window is empty — i.e. the person
// has no history to be measured against rather than a flat one.
export function isNewcomer(weeks: number[]): boolean {
  return weeks.slice(0, -DELTA_WEEKS).every((v) => v === 0)
}

// The Monday on or before a SQLite-native UTC timestamp, as 'YYYY-MM-DD' — the
// same bucket key the backend's week axis uses (Postgres date_trunc('week') is
// Monday-based, so both halves must agree or every cohort lands a row off).
export function mondayOf(ts: string): string {
  const d = new Date(`${ts.replace(' ', 'T')}Z`)
  if (Number.isNaN(d.getTime())) return ''
  const back = (d.getUTCDay() + 6) % 7
  d.setUTCDate(d.getUTCDate() - back)
  return d.toISOString().slice(0, 10)
}

// The minimum a row needs to be cohorted: when it was created, and its weekly
// active-day series aligned to the shared axis.
export interface CohortRow {
  created_at: string
  metrics?: { weeks: number[] } | null
}

export interface Cohort {
  week: string
  size: number
  // retention[k] = % of the cohort active k weeks after signup. The array stops
  // at the present, so an incomplete cohort reads as absent rather than as 0%
  // — "hasn't happened yet" and "nobody came back" must never look the same.
  retention: number[]
}

// Group rows by signup week and measure how many were still active k weeks on.
// Rows that signed up before the axis begins are dropped (the caller reports
// how many, rather than letting them silently vanish).
export function buildCohorts(rows: CohortRow[], weeks: string[]): Cohort[] {
  if (weeks.length === 0) return []
  const idxOfWeek = new Map(weeks.map((w, i) => [w, i]))
  const groups = new Map<string, CohortRow[]>()
  for (const r of rows) {
    const wk = mondayOf(r.created_at)
    if (!idxOfWeek.has(wk)) continue
    const g = groups.get(wk)
    if (g) g.push(r)
    else groups.set(wk, [r])
  }
  return [...groups.entries()]
    .sort((a, b) => (a[0] < b[0] ? 1 : -1)) // newest cohort first
    .map(([week, members]) => {
      const start = idxOfWeek.get(week) as number
      const retention: number[] = []
      for (let k = 0; k < weeks.length - start; k++) {
        const active = members.filter((m) => (m.metrics?.weeks?.[start + k] ?? 0) > 0).length
        retention.push(Math.round((active / members.length) * 100))
      }
      return { week, size: members.length, retention }
    })
}
