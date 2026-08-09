# Agent authoring & the block manifest

## The problem

tela's reader renders a rich block palette far beyond plain markdown — callouts,
tabs, kanban, pull-quotes, embeds (YouTube/Vimeo/Loom), Mermaid, math, footnotes,
highlights, collapsibles, wikilinks. But an agent writing a page over MCP only
ever saw `body: "markdown body"` in the `create_page`/`update_page` schema. With
no signal that the rich palette exists, agents fell back to generic CommonMark
(headings, paragraphs, bullet lists) — correct, but flat. They can't use features
they were never told about. A block-native editor that hands an agent a *typed
block* palette, by contrast, makes richness the path of least resistance.

The fix is **capability disclosure**: tell the agent, through the MCP surface it
actually reads, what blocks exist and how to write them.

## The setup (single source → many consumers)

The truth about authorable blocks lives in **one** file:

```
frontend/src/components/app/blocks-manifest.json   ← SOURCE OF TRUTH
```

Each entry: `id, label, hint, category, slash, agent, keywords, syntax, when, note`.
Everything else derives from it, so there is no second copy to drift:

```
blocks-manifest.json
  │
  ├─► editor slash menu        milkdown-slash.tsx projects `slash` entries into
  │                            menu items, joining an insert fn by `id` (RUN map).
  │
  └─► agent authoring guide    scripts/blocks-manifest.mjs --write generates
        (codegen)              backend/internal/api/blocks_gen.json (go:embed
                               can't reach into frontend/). The backend renders
                               it (mcp_authoring.go) into:
                                 • MCP server Instructions (sent on initialize)
                                 • create_page / update_page tool descriptions
                                 • the tela://authoring-guide resource (+ example)
```

Because the manifest is **load-bearing for the editor's slash menu**, it can't
silently rot — forget an entry and the human palette breaks first, visibly.

## Anti-drift gates

`make blocks-gate` enforces two things. It runs first in `make test` locally
**and** as its own `blocks-gate` CI workflow (`.github/workflows/blocks-gate.yml`)
on every push/PR that touches the manifest, a `milkdown-*` plugin, the codegen
script, or the generated copy — so the contract holds even on a frontend-only
change (which the heavier `ci.yml` integration job ignores by path):

1. **Freshness** — regenerates the backend copy and fails if `blocks_gen.json` is
   out of sync with the source. Fix: `make blocks-gen` and commit.
2. **Coverage** — every `frontend/src/components/app/milkdown-*.ts(x)` is either
   an authorable block (mapped in `PLUGIN_BLOCKS`) or declared non-authorable
   infra (`INFRA`) in `scripts/blocks-manifest.mjs`. A new plugin in neither set
   fails the gate, forcing a conscious "manifest entry or infra?" decision — so a
   new block can never ship invisible to agents.

A dev-only assertion in `milkdown-slash.tsx` additionally throws if the slash
`RUN` map and the manifest's `slash` blocks drift.

## Adding a new block

1. Add the renderer plugin (`milkdown-<name>.ts`) as usual.
2. Add an entry to `blocks-manifest.json` (set `agent: true` + a `when:` line if
   agents should author it; `slash: true` if it belongs in the palette).
3. If it's slash-insertable, add a `RUN[id]` insert fn in `milkdown-slash.tsx`.
4. Map the plugin file → block id in `PLUGIN_BLOCKS` (or add to `INFRA` if it's
   not an authorable block) in `scripts/blocks-manifest.mjs`.
5. `make blocks-gen` to regenerate, then `make blocks-gate` to verify.

## Known limitations of the gate

The gate guarantees *every plugin file is classified*, which is weaker than
*every renderable block is disclosed*. Two cases it does **not** catch — covered
by the "Adding a new block" checklist by convention, not by CI:

1. **A block added inside an existing (already-mapped or infra) file** — e.g. a
   second block in `milkdown-math.ts`, a new directive in `milkdown-directives.ts`,
   or a stock remark plugin wired directly into `milkdown-editor.tsx`. No new file
   appears, so the gate stays green with no manifest entry. Closing this needs the
   gate to enumerate actual registered ProseMirror schemas / directive names
   rather than filenames.
2. **Wrong `syntax`** — the `syntax` strings are hand-authored and never validated
   against the real serializer, so a renamed directive (`:::quote` → `:::pullquote`)
   would silently lie to agents. Closing this needs a headless editor round-trip
   test (blocked on the frontend having any test infra — see CLAUDE.md "no test
   infra"); a natural first FE test when that lands.

## Field reference

| field | used by | meaning |
|---|---|---|
| `id` | slash + gate | stable id; joins to the slash `RUN` fn |
| `label` / `hint` | slash menu | palette display |
| `keywords` | slash menu | palette search |
| `category` | agent guide | grouping (`authoringCategoryOrder` sets order) |
| `slash` | slash menu | appears in the editor palette |
| `agent` | agent guide | appears in the MCP guide / instructions / tool hint |
| `syntax` | agent guide | exact round-trip markdown to write the block |
| `when` | agent guide | one-line guidance (required when `agent`) |
| `note` | agent guide | optional caveat |

`agent: false` blocks (headings, lists, dividers, **date**, **excalidraw**) stay
out of the agent guide — basics agents already know, or editor-only blocks like
excalidraw whose JSON scene isn't hand-writable.

## The same principle, applied to import

The block palette isn't the only capability agents were blind to. **Bulk import**
(`POST /api/spaces/{id}/import`) already turns a markdown tree into a page tree,
carries frontmatter into `props` (so `sheet: true` yields a real sheet), and has a
`dry_run` preview — but nothing on the MCP surface referenced it, so an agent asked
to "import this folder" hand-created pages one file at a time and never converted
spreadsheets to sheets. Same capability-not-disclosed failure as flat pages.

The fix mirrors the sheet/deck guides: an **`import_guide` tool + `tela://import-guide`
resource + a pointer in the server Instructions** (`mcp_import.go`, content in
`import_guide.md`). Because file bytes can't ride MCP's 4 MiB request cap, the guide
is disclosure only — it hands an agent-with-shell the recipe: convert Office files
locally (docx → pandoc → md page; xlsx → GFM + `sheet: true` → live sheet, formulas
preserved), attach PDFs/images, then drive the endpoint's dry-run → confirm → commit
loop. No server-side conversion, no sidecar, no change to the importer itself.

## The other half: showing agents what the reader did

Disclosure tells an agent what it *can* write. It doesn't tell it what the reader
actually *did* with what it wrote — and those differ, silently, in ways no amount
of re-reading the source can reveal (a `:::typo` unwraps to bare content, raw HTML
is dropped on the floor, a collapsible missing a blank line vanishes whole).

`lint_page` and `preview_page` close that loop, the way `lint_deck`/`preview_deck`
close it for decks. They reuse this manifest: the generator lifts the frontend's
`KNOWN_DIRECTIVE_NAMES` and `CALLOUT_TYPES` into `blocks_gen.json` next to the
block palette, so the lint's idea of a valid block can't drift from the reader's.
Full design in [`page-lint.md`](page-lint.md).
