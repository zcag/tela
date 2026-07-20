// Hand-rolled smoke harness for lib/comments/anchor.ts.
// FE has no vitest config yet (memory.md "Frontend test infra missing"),
// so this is a script you run manually. tsx isn't a pinned dev dep, so:
//
//   $ cd frontend
//   $ npx tsx src/lib/comments/__smoke__.ts
//
// Exits with code 0 if all cases PASS, 1 otherwise. Hardcoded anchors only —
// captureAnchor needs an EditorView so its sliceing path is exercised in #73
// (anchor-decoration) and the live-verify pass, not here.

import { anchorFromText, resolveAnchor, type CommentAnchor } from './anchor'

interface Case {
  name: string
  text: string
  anchor: CommentAnchor
  expected: { from: number; to: number } | null
}

const cases: Case[] = [
  {
    name: '1: exact match — unique exact, no ambiguity',
    text: 'Hello world, this is a test document.',
    // 'a test' lives at index 21; prefix/suffix unambiguous from tier 1.
    anchor: { prefix: 'this is ', exact: 'a test', suffix: ' document.' },
    expected: { from: 21, to: 27 },
  },
  {
    name: '2: prefix/suffix disambiguates — exact appears twice',
    text: 'foo bar baz. The quick foo bar qux.',
    // 'foo bar' appears at 0 AND 23 — tier 1 (full context) must pick 23.
    anchor: { prefix: 'The quick ', exact: 'foo bar', suffix: ' qux.' },
    expected: { from: 23, to: 30 },
  },
  {
    name: '3: single-char drift — tier 1 fails, tier 2 resolves',
    text: 'Alpha beta gamma delta epsilon zeta eta theta iota.',
    // anchor captured when the trailing word was 'kappa', now drifted to 'iota'.
    // Tier-1 needle has 'kappa' → not found. Tier-2 N=16 trims the drift off
    // both ends and matches uniquely.
    anchor: {
      prefix: 'Alpha beta gamma ',
      exact: 'delta epsilon',
      suffix: ' zeta eta theta kappa.',
    },
    expected: { from: 17, to: 30 },
  },
  {
    name: '4: full orphan — exact removed entirely → null',
    text: 'The quick brown fox jumps over the lazy dog.',
    anchor: {
      prefix: 'before ',
      exact: 'this passage no longer exists',
      suffix: ' after',
    },
    expected: null,
  },
  {
    name: '5: ambiguous-no-context — tier 3 multi-match → null',
    text: 'foo bar foo bar',
    // exact appears twice; prefix + suffix empty so tier 1/2 also non-unique.
    anchor: { prefix: '', exact: 'foo bar', suffix: '' },
    expected: null,
  },
  {
    name: '6: empty-suffix edge — anchor at end-of-document still resolves',
    text: 'Welcome to the page. The end.',
    anchor: {
      prefix: 'Welcome to the ',
      exact: 'page. The end.',
      suffix: '',
    },
    expected: { from: 15, to: 29 },
  },
]

let failed = 0
for (const c of cases) {
  const actual = resolveAnchor(c.text, c.anchor)
  const pass = sameResult(actual, c.expected)
  const tag = pass ? 'PASS' : 'FAIL'
  if (!pass) failed += 1
  console.log(
    `[${tag}] ${c.name}\n        expected=${JSON.stringify(c.expected)} actual=${JSON.stringify(actual)}`,
  )
}

// anchorFromText (read-view capture) round-trips: a passage fingerprinted from
// plain text must resolve back to the same [from, to) in that text. This is the
// path the reader's selection bridge uses (lib/comments/view-anchor.ts).
const roundTrips: { name: string; text: string; from: number; to: number }[] = [
  {
    name: 'R1: mid-document passage round-trips',
    text: 'Hello world, this is a test document about anchors.',
    from: 21,
    to: 27, // 'a test'
  },
  {
    name: 'R2: repeated exact — surrounding context disambiguates',
    text: 'foo bar baz. The quick foo bar qux.',
    from: 23,
    to: 30, // the SECOND 'foo bar'
  },
  {
    name: 'R3: end-of-document passage (empty suffix)',
    text: 'Welcome to the page. The end.',
    from: 15,
    to: 29, // 'page. The end.'
  },
]
for (const c of roundTrips) {
  const anchor = anchorFromText(c.text, c.from, c.to)
  const resolved = resolveAnchor(c.text, anchor)
  const pass = sameResult(resolved, { from: c.from, to: c.to })
  if (!pass) failed += 1
  console.log(
    `[${pass ? 'PASS' : 'FAIL'}] ${c.name}\n        expected={"from":${c.from},"to":${c.to}} actual=${JSON.stringify(resolved)}`,
  )
}

const total = cases.length + roundTrips.length
console.log(`\n${total - failed}/${total} cases passed.`)
if (failed > 0) process.exit(1)

function sameResult(
  a: { from: number; to: number } | null,
  b: { from: number; to: number } | null,
): boolean {
  if (a === null || b === null) return a === b
  return a.from === b.from && a.to === b.to
}
