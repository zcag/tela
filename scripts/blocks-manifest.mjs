#!/usr/bin/env node
// blocks-manifest — keep the agent-facing copy of the block palette in sync with
// the frontend source of truth, and guard against a renderer plugin shipping
// without a manifest entry (invisible to agents + missing from the slash menu).
//
//   node scripts/blocks-manifest.mjs --write   # regenerate the backend copy
//   node scripts/blocks-manifest.mjs --check    # verify in sync + full coverage (CI)
//
// SOURCE  frontend/src/components/app/blocks-manifest.json
//         frontend/src/lib/markdown/transforms/unknown-directives.ts  (directive names)
//         frontend/src/lib/markdown/transforms/callouts.ts            (callout types)
// GEN     backend/internal/api/blocks_gen.json   (go:embed can't reach into frontend/)
//
// The two name lists ride along because the backend page lint (internal/pagelint)
// has to know which `:::name` / `> [!TYPE]` the RENDERER recognizes: an unlisted
// one still parses, then silently unwraps to its bare children. Hand-copying the
// lists into Go would drift exactly where the lint is meant to be authoritative,
// so they're extracted from the frontend source and gate-checked like the blocks.
//
// Coverage: every frontend/src/components/app/milkdown-*.ts(x) is either an
// authorable block (mapped to >=1 manifest id in PLUGIN_BLOCKS) or declared
// non-authorable infra (INFRA). A new plugin in neither set fails --check, so
// adding a block forces a conscious "manifest entry or infra?" decision.

import { readFileSync, writeFileSync, readdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')
const SRC = join(ROOT, 'frontend/src/components/app/blocks-manifest.json')
const GEN = join(ROOT, 'backend/internal/api/blocks_gen.json')
const PLUGIN_DIR = join(ROOT, 'frontend/src/components/app')
const DIRECTIVES_SRC = join(ROOT, 'frontend/src/lib/markdown/transforms/unknown-directives.ts')
const CALLOUTS_SRC = join(ROOT, 'frontend/src/lib/markdown/transforms/callouts.ts')

// milkdown-* plugins that are NOT authorable blocks (no manifest entry expected).
const INFRA = new Set([
  'milkdown-block-handle', // drag/insert handle in the gutter
  'milkdown-bubble-toolbar', // inline selection toolbar
  'milkdown-emoji', // `:shortcode:` input rule + `/`-picker plumbing (stores Unicode, not a block node)
  'milkdown-emoji-autocomplete', // `:query` emoji autocomplete view
  'milkdown-directives', // shared remark directive container plumbing
  'milkdown-editor', // the editor host component
  'milkdown-floating', // slash/bubble positioning helper
  'milkdown-image-upload', // upload transport for the `image` block (markdown is plain)
  'milkdown-link-popover', // hover popover (open/copy/edit/remove) for a link (no node)
  'milkdown-list-indent', // Tab/Shift-Tab list nest/un-nest keymap (no node)
  'milkdown-modifier-click', // cmd/ctrl-click navigation
  'milkdown-placeholder', // empty-editor placeholder text (no node)
  'milkdown-plain-paste', // Cmd/Ctrl+Shift+V paste-as-plain-text keymap (no node)
  'milkdown-slash', // the slash menu itself
  'milkdown-table-fix', // repairs ragged tables so table ops don't throw (no node)
  'milkdown-table-select', // table cell-selection behavior (no node)
  'milkdown-upload-placeholder', // transient upload placeholder decoration (no node)
  'milkdown-templates', // composed snippets, not a block type
  'milkdown-excalidraw-presence', // live "editing" badge decoration for the `excalidraw` block (no node)
  'milkdown-typography', // smart-quote/dash/ellipsis input rules (no node)
  'milkdown-url-unfurl', // link unfurl decoration
  'milkdown-wikilink', // wikilink autocomplete/resolve plumbing
  'milkdown-wikilink-decoration', // decoration layer for the `wikilink` block
])

// milkdown-* plugin basename -> manifest block id(s) it backs.
const PLUGIN_BLOCKS = {
  'milkdown-calendar': ['calendar'],
  'milkdown-callouts': ['callout'],
  'milkdown-chart': ['chart'],
  'milkdown-codeblock': ['code'],
  'milkdown-collapsibles': ['collapsible'],
  'milkdown-embed': ['embed'],
  'milkdown-excalidraw': ['excalidraw'],
  'milkdown-file': ['file'],
  'milkdown-highlight': ['highlight'],
  'milkdown-kanban': ['kanban'],
  'milkdown-math': ['equation', 'inline-math'],
  'milkdown-mermaid': ['mermaid'],
  'milkdown-poll': ['poll'],
  'milkdown-pullquote': ['pull-quote'],
  'milkdown-stat-grid': ['stat-grid'],
  'milkdown-table': ['table'],
  'milkdown-tabs': ['tabs'],
  'milkdown-task-list': ['task-list'],
  'milkdown-timeline': ['timeline'],
  'milkdown-wikilink-bracket': ['wikilink'],
}

// VIEW-RENDER COVERAGE (docs/view-edit-split.md). The read-only view renderer
// (frontend/src/components/view/MarkdownView.tsx) must render every authorable
// block — either with a dedicated renderer (VIEW_RENDERED) or via an explicitly
// accepted graceful-degrade-to-children (VIEW_DEGRADES). Every manifest block id
// must be in exactly one set, so a NEW block can't ship rendering in the editor
// but silently broken in the view. Promoting a block from degrades → rendered
// means writing its renderer in MarkdownView, then moving its id here.
const VIEW_RENDERED = new Set([
  'h1', 'h2', 'h3',
  'bullet-list', 'ordered-list', 'task-list',
  'quote', 'callout', 'highlight',
  'code', 'table', 'divider', 'footnote', 'date', 'emoji', 'image',
  'equation', 'inline-math',
  'mermaid', 'chart', 'excalidraw',
  'wikilink', 'tabs',
  'pull-quote', 'embed', 'file', 'timeline',
  'kanban', 'stat-grid', 'calendar', 'collapsible',
  'poll',
])
// Rendered as children (content preserved, chrome not yet ported). Tracked so
// the gap is explicit and reviewable, never silent. Currently empty — the full
// palette has dedicated view renderers; a NEW block goes here only as a
// conscious, temporary exception.
const VIEW_DEGRADES = new Set([])

function loadSource() {
  const raw = JSON.parse(readFileSync(SRC, 'utf8'))
  if (!Array.isArray(raw.blocks)) fail(`${SRC} has no "blocks" array`)
  return raw.blocks
}

// Pull the string members out of a named array/Set literal in a TS source file.
// Deliberately strict: if the declaration moves or changes shape the extraction
// fails loudly here rather than silently emitting an empty list, which would
// make the backend lint quietly stop flagging unknown names.
function extractStringList(file, decl) {
  const src = readFileSync(file, 'utf8')
  const at = src.indexOf(decl)
  if (at < 0) fail(`${file} no longer declares ${decl} — update scripts/blocks-manifest.mjs`)
  const open = src.indexOf('[', at)
  const close = src.indexOf(']', open)
  if (open < 0 || close < 0) fail(`${file}: could not read the array literal after ${decl}`)
  const names = [...src.slice(open + 1, close).matchAll(/'([^']+)'|"([^"]+)"/g)].map(
    (m) => m[1] ?? m[2],
  )
  if (names.length === 0) fail(`${file}: ${decl} extracted as empty`)
  return names
}

// A block's DIRECTIVE name is whatever its own `syntax` opens with, derived
// rather than hand-declared so it can't drift from the example agents are shown.
//
// It is deliberately NOT the same namespace as the block `id`: `quote` is the
// plain markdown blockquote, while the pull-quote block (id `pull-quote`) writes
// `:::quote`. Forcing the two names equal is impossible — they collide. Making
// the relationship explicit and checked is the fix; the backend also uses it to
// tell an author who wrote `:::stat-grid` (the id) that the directive is
// `:::stats`, which is exactly the mistake found on a live page.
function directiveOf(block) {
  const m = /^:::([A-Za-z][A-Za-z0-9_-]*)/.exec(block.syntax ?? '')
  return m ? m[1] : null
}

// Deterministic serialization for a stable git diff (2-space indent, trailing nl).
function render(blocks) {
  return (
    JSON.stringify(
      {
        blocks: blocks.map((b) => {
          const directive = directiveOf(b)
          return directive ? { ...b, directive } : b
        }),
        directives: extractStringList(DIRECTIVES_SRC, 'KNOWN_DIRECTIVE_NAMES'),
        calloutTypes: extractStringList(CALLOUTS_SRC, 'CALLOUT_TYPES'),
      },
      null,
      2,
    ) + '\n'
  )
}

function fail(msg) {
  console.error(`blocks-manifest: ${msg}`)
  process.exit(1)
}

function checkCoverage(blocks) {
  const ids = new Set(blocks.map((b) => b.id))
  const problems = []

  // Every mapped id must exist in the manifest.
  for (const [file, mapped] of Object.entries(PLUGIN_BLOCKS)) {
    for (const id of mapped) {
      if (!ids.has(id)) problems.push(`${file} maps to unknown block id "${id}"`)
    }
  }

  // Every milkdown-* plugin must be classified (block or infra).
  const plugins = readdirSync(PLUGIN_DIR)
    .filter(
      (f) =>
        /^milkdown-.*\.tsx?$/.test(f) &&
        !f.endsWith('.stories.tsx') &&
        !/\.test\.tsx?$/.test(f),
    )
    .map((f) => f.replace(/\.tsx?$/, ''))
  for (const p of plugins) {
    if (!INFRA.has(p) && !(p in PLUGIN_BLOCKS)) {
      problems.push(
        `plugin "${p}" is unclassified — add it to PLUGIN_BLOCKS (with a blocks-manifest.json entry) or INFRA in scripts/blocks-manifest.mjs`,
      )
    }
  }

  // Required fields per block.
  for (const b of blocks) {
    for (const k of ['id', 'label', 'hint', 'category', 'syntax']) {
      if (!b[k]) problems.push(`block "${b.id ?? '?'}" missing field "${k}"`)
    }
    if (b.agent && !b.when) problems.push(`agent block "${b.id}" missing "when"`)
  }

  // A block whose syntax is a `:::directive` must use a name the RENDERER
  // recognizes. Otherwise the palette documents a block that parses and then
  // silently unwraps to bare text — invisible in the source, and the exact
  // failure `unknown-directive` reports.
  const known = new Set(extractStringList(DIRECTIVES_SRC, 'KNOWN_DIRECTIVE_NAMES'))
  for (const b of blocks) {
    const d = directiveOf(b)
    if (d && !known.has(d)) {
      problems.push(
        `block "${b.id}" documents \`:::${d}\`, which the renderer doesn't know — ` +
          `add it to KNOWN_DIRECTIVE_NAMES (and give it a view renderer) or fix the syntax`,
      )
    }
  }

  // Every authorable block must declare a view-render status (rendered or
  // explicitly degrading) — no block silently unhandled by the view renderer.
  for (const id of ids) {
    const r = VIEW_RENDERED.has(id)
    const d = VIEW_DEGRADES.has(id)
    if (r && d) problems.push(`block "${id}" is in BOTH VIEW_RENDERED and VIEW_DEGRADES`)
    if (!r && !d) {
      problems.push(
        `block "${id}" has no view-render status — add it to VIEW_RENDERED (with a renderer in MarkdownView.tsx) or VIEW_DEGRADES in scripts/blocks-manifest.mjs`,
      )
    }
  }
  // No stale ids in the view sets.
  for (const id of [...VIEW_RENDERED, ...VIEW_DEGRADES]) {
    if (!ids.has(id)) problems.push(`view set references unknown block id "${id}"`)
  }

  if (problems.length) fail('coverage failed:\n  - ' + problems.join('\n  - '))
}

const mode = process.argv[2]
const blocks = loadSource()
checkCoverage(blocks)
const out = render(blocks)

if (mode === '--write') {
  writeFileSync(GEN, out)
  console.log(`blocks-manifest: wrote ${GEN} (${blocks.length} blocks)`)
} else if (mode === '--check') {
  let current = ''
  try {
    current = readFileSync(GEN, 'utf8')
  } catch {
    fail(`${GEN} missing — run \`make blocks-gen\``)
  }
  if (current !== out) {
    fail(`${GEN} is stale — run \`make blocks-gen\` and commit the result`)
  }
  console.log(`blocks-manifest: in sync (${blocks.length} blocks)`)
} else {
  fail('usage: blocks-manifest.mjs --write | --check')
}
