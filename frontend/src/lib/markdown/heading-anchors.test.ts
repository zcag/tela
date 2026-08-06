import { describe, expect, it } from 'vitest'
import { headingId } from './heading-anchors'

// The anchor id is a contract, not an implementation detail: a page's own
// "Contents" table is written by hand (by a person or an agent) against the
// slug they'd guess from the heading text. If this rule drifts, every in-body
// table of contents in the wiki silently stops jumping — the URL still updates,
// so it looks like nothing happened at all.
describe('headingId', () => {
  it('slugs a heading the way an author would guess', () => {
    const used = new Map<string, number>()
    expect(headingId('Alerting (Microsoft Teams)', 0, used)).toBe(
      'alerting-microsoft-teams',
    )
    expect(headingId('What synthetic monitoring is', 1, used)).toBe(
      'what-synthetic-monitoring-is',
    )
  })

  it('deduplicates repeated headings instead of colliding', () => {
    const used = new Map<string, number>()
    expect(headingId('Overview', 0, used)).toBe('overview')
    expect(headingId('Overview', 1, used)).toBe('overview-2')
    expect(headingId('Overview', 2, used)).toBe('overview-3')
  })

  it('treats punctuation-only differences as the same slug', () => {
    const used = new Map<string, number>()
    expect(headingId('Cost', 0, used)).toBe('cost')
    expect(headingId('Cost?', 1, used)).toBe('cost-2')
  })

  it('falls back to a positional id when nothing slugs (emoji/CJK titles)', () => {
    const used = new Map<string, number>()
    expect(headingId('🎉', 4, used)).toBe('section-5')
  })

  it('transliterates Turkish characters, matching page URLs', () => {
    const used = new Map<string, number>()
    expect(headingId('Ölçüm ve İzleme', 0, used)).toBe('olcum-ve-izleme')
  })
})
