// Tooltip copy for the sidebar StalenessDot. The dot unions the
// background-backfill subsystems that have a freshness rollup — RAG indexing
// and summaries — so one calm marker means "this space/page has background work
// outstanding". Each subsystem is gated by its own enabled flag upstream.
//
// The dot signals *pending* backfill (the worker will catch up and it clears),
// NOT hard errors: a failed summary is deliberately excluded here so a
// permanently-failing page can't wedge the dot on (failures surface in
// Settings → Summaries instead).
//
// The copy answers the question people actually ask of an unexplained marker —
// "how do I make it go away?" — so every label is one sentence that says the
// work is happening and is nearly done, not a state name. Naming the state
// ("Edited since last indexed") was accurate and told a writer nothing.
const TAIL = ' — clears shortly.'

// "1 page" / "2 pages" — the count reads as reassurance, so keep the noun.
function pages(n: number): string {
  return `${n} ${n === 1 ? 'page' : 'pages'}`
}

// Per-space tooltip from per-subsystem backlog counts. Clauses are kept
// separate — a page can be behind on both, so summing would double-count.
// Returns null when nothing is behind.
export function spaceStaleLabel(
  indexing: number,
  summarizing: number,
): string | null {
  const parts: string[] = []
  if (indexing > 0) parts.push(`${pages(indexing)} indexing`)
  if (summarizing > 0) parts.push(`${pages(summarizing)} summarizing`)
  return parts.length ? parts.join(', ') + TAIL : null
}

// Per-page tooltip: which backfills this one page is behind on. null = none.
// "recent edits" is what makes the dot read as a consequence of typing rather
// than as a fault; a page that has never been through has no edits to name.
export function pageStaleLabel(
  indexing: 'stale' | 'unindexed' | null,
  summarizing: 'stale' | 'missing' | null,
): string | null {
  if (!indexing && !summarizing) return null
  if (indexing && summarizing) return 'Indexing and summarizing' + TAIL
  const verb = indexing ? 'Indexing' : 'Summarizing'
  const subject = indexing === 'stale' || summarizing === 'stale' ? 'recent edits' : 'this page'
  return `${verb} ${subject}${TAIL}`
}
