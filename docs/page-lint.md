# Page lint & preview — showing the writer what the reader sees

The ordinary-page counterpart to `lint_deck` / `preview_deck`. Design notes,
the rule set, and why each surface exists.

## The problem

A broken deck fails loudly: `slidev build` produces nothing and Present 502s.
A prose page **never** fails. The reader renders *something* for any input, so a
page whose markdown says one thing and whose render says another just sits there
looking fine.

That gap is invisible from the writing side, by construction. Re-reading your own
markdown cannot reveal a transformation the renderer performs that the markdown
doesn't spell out. The motivating case was the `$…$` defect: two `$` in one
paragraph paired into an `inlineMath` node and silently swallowed the sentence
between them, while the source looked perfect. The same week produced two more of
the same shape outside tela (typst `~` → non-breaking space; rendercv mangling a
phone number). Agents write a large share of tela's pages and cannot see the
reader at all, so for them every defect of this class is undetectable.

Hence two tools, answering two different questions:

- **`lint_page`** — "where does the render disagree with my markdown?" A rule set,
  fast, always available, and specific enough to act on.
- **`preview_page`** — "what does this page actually say?" The real render, read
  back. A rule set only catches divergences someone thought of; reading the output
  catches the rest.

## Rules

Every rule names a **visible** consequence. Nothing here is about taste, length or
heading hygiene — if a rule can't say what the reader will do differently, it
doesn't belong in this package.

`error` = content is lost or restructured. `warning` = the content survives but
the block's chrome doesn't.

| rule | level | what the reader does |
|---|---|---|
| `unclosed-code-fence` | error | everything after renders as code |
| `unclosed-math` | error | the rest of the page renders as one formula |
| `unclosed-directive` | error | the block swallows everything below it |
| `collapsible-not-split` | error | the whole `<details>` is dropped — nothing inside appears |
| `collapsible-body-absorbed` | error | the collapsible renders empty |
| `unclosed-collapsible` | error | opening tag dropped, body loose with no toggle |
| `chart-invalid` | error | dead/blank chart frame |
| `unknown-directive` | warning | unwraps to bare contents; frame, labels and attributes gone |
| `unknown-callout` | warning | renders as a plain quote with the literal `[!TYPE]` in it |
| `dropped-html` | warning | tag and its styling vanish; inner text survives as prose |
| `collapsible-closer-joined` | warning | no collapsible forms; body renders as plain text |
| `empty-block` | warning | a `:::tabs`/`:::kanban`/`:::stats` with no `###` renders empty |
| `undefined-footnote` | warning | renders as the literal `[^1]` |
| `prose-as-math` | warning | prose inside `$$…$$` renders as a formula |
| `ragged-table` | warning | a stray column the header doesn't cover, or a short row |
| `dangling-wikilink` | warning | `[[Name]]` matching no page renders as plain text |
| `broken-page-link` | warning | the link goes nowhere |
| `missing-attachment` | warning | broken image / dead download |

The table above was **verified against the live reader**, not derived from
reading the renderer. That mattered: `ragged-table`'s first message claimed the
extra cells were dropped (what the GFM spec says); the reader actually renders
them as a stray column. A lint that misdescribes the render is worse than no lint,
because its whole value is being trusted about something the author can't see.

## Two halves, two homes

- **`backend/internal/pagelint`** — the structural rules. Pure text, no DB, unit
  tested, reusable. Line-oriented on purpose: a Go reimplementation of the
  frontend's remark stack would drift from it *exactly* where this package is
  meant to be authoritative.
- **`backend/internal/api/page_lint.go`** — the resolution rules (wikilinks, page
  links, attachments). These need the database, so they can only live here. They
  reuse `spaceTitleSlugIndex` — the same lookup that builds `page_links` — so
  "what does `[[Name]]` resolve to" has one answer, not two.

Both land in one report, sorted by line. A caller shouldn't have to know there
are two sources.

### The vocabulary is generated, not copied

The one thing that genuinely must agree with the frontend is *which* `:::name`
and `> [!TYPE]` the reader recognizes. `scripts/blocks-manifest.mjs` lifts
`KNOWN_DIRECTIVE_NAMES` and `CALLOUT_TYPES` out of the frontend transforms into
`blocks_gen.json` alongside the block palette, and `make blocks-gate` fails if
they're stale. A hand-copied list would go wrong silently and confidently report
a valid block as unknown. The extraction is deliberately strict — if the
declarations move or change shape, generation fails loudly rather than emitting
an empty list.

### Code is masked, once

`pagelint.MaskCode` blanks fenced blocks and inline code spans (honoring
CommonMark's rule that a run of N backticks closes only on a run of exactly N,
across lines) and both halves run over its output. Without it, tela's own docs
pages — which are full of syntax examples — would be a wall of false positives.
Two real bugs came out of a smoke run over `docs/*.md`: a wide code span showing
a fence (```` ```lang ````) read as a fence opener, and a code span wrapping
across two lines leaving its tail exposed.

## Surfaces

Three, because the deck work established that shipping fewer leaves a hole:

1. **`lint_page` / `preview_page` MCP tools** — the agent's explicit check.
2. **The advisory on agent writes** — `create_page`/`update_page` return a `lint`
   field. Agents don't call a lint tool on their own initiative, so the finding
   has to arrive unasked-for, in the result of the write that caused it, while
   fixing it is one edit away.
3. **`POST /api/pages/{id}/lint` + the editor panel** — the human's signal.
   Without it, a browser author would be the only party with none. The panel
   renders nothing when the page is clean.

**Nothing is gated.** Unlike `deckWriteGate`, no write is ever rejected: a page is
prose, the markdown is stored verbatim regardless, and refusing a save over an
unknown callout type would fight the author for no gain. A broken deck builds
nothing; a "broken" page is still a page.

## preview_page

No new renderer. It reuses `renderPDF` — gotenberg's Chromium pointed at the same
reading-mode UI a human sees — and pulls the text back out with
`internal/extract`. The PDF *is* the reader, so callouts, tables, math, mermaid
and diagrams are all resolved by the real code.

`format` defaults to `text`: every defect in the motivating class is a text-level
divergence, prose pages have no overflow failure mode to look at, and text is
cheap and universally usable. `image` (and `both`) take a second gotenberg call
via the screenshot route — a full-height JPEG, capped, since a long page's
screenshot would otherwise dominate a tool result.

Deck pages are refused with a pointer to `preview_deck`; their body is Slidev
markdown and would come back as a wall of headmatter.

## Gotchas

- **The reader drops raw HTML.** `MarkdownView`'s `case 'html': return null`
  means anything but the `<details>` collapsible form vanishes — including
  `<br>`, `<img>`, `<span style>`, and prose placeholders like `<org>` or
  `<commit>`, which CommonMark parses as tags. Text *between* the tags survives,
  which is why the loss is so easy to miss.
- **Collapsibles need their blank lines.** `collapsiblesRemark` pairs a separate
  opener node with a separate closer node; without blank lines remark merges them
  into one raw html node, which is then dropped whole. This is the one rule where
  a formatting slip costs the entire block.
- **Attachment existence is content-addressed and not space-scoped**, mirroring
  `ServeSpaceFile`'s deliberate cross-space fallback. Filtering by the space in
  the URL would report working attachments as broken.
- **Errors fail safe.** Any database error in the resolution half yields *no*
  issue rather than a false one. A lint that invents defects when the database
  hiccups is worse than one that misses them.
