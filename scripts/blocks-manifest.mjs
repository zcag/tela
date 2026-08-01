#!/usr/bin/env node
// blocks-manifest — keep the agent-facing copy of the block palette in sync with
// the frontend source of truth, and guard against a renderer plugin shipping
// without a manifest entry (invisible to agents + missing from the slash menu).
//
//   node scripts/blocks-manifest.mjs --write   # regenerate the backend copy
//   node scripts/blocks-manifest.mjs --check    # verify in sync + full coverage (CI)
//
// SOURCE  frontend/src/components/app/blocks-manifest.json
// GEN     backend/internal/api/blocks_gen.json   (go:embed can't reach into frontend/)
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

// Deterministic serialization for a stable git diff (2-space indent, trailing nl).
function render(blocks) {
  return JSON.stringify({ blocks }, null, 2) + '\n'
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
