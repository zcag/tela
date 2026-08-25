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

There is deliberately **no rule about a blank line before `</details>`**. An
earlier version warned that a closer joined to the body paragraph broke the
collapsible; it fired 66 times across the live wiki and was wrong every time.
`</details>` is a CommonMark type-6 HTML block, which *may interrupt a
paragraph*, so it always becomes its own node and the collapsible pairs up
fine. Measured in the reader across all four spacings: only "body absorbed
into the opener" and "everything on one line" actually lose content.
| `chart-invalid` | error | dead/blank chart frame |
| `unknown-directive` | warning | unwraps to bare contents; frame, labels and attributes gone |
| `unknown-callout` | warning | renders as a plain quote with the literal `[!TYPE]` in it |
| `dropped-html` | warning | a real element's tag and styling vanish (inner text survives); a placeholder like `<all>`/`<org>` is deleted outright |
| `empty-block` | warning | a `:::tabs`/`:::kanban`/`:::stats` with no `###` renders empty |
| `undefined-footnote` | warning | renders as the literal `[^1]` |
| `prose-as-math` | warning | prose inside `$$…$$` renders as a formula |
| `ragged-table` | warning | a stray column the header doesn't cover, or a short row |
| `oversized-table` | warning | most of the table sits off-screen behind a horizontal scroll |
| `dangling-wikilink` | warning | renders as a broken link that goes nowhere (**agent-only**, see below) |
| `broken-page-link` | warning | the link goes nowhere |
| `missing-attachment` | warning | broken image / dead download |

Three exclusions keep the noise down, and two of them exist because the first
version of this rule blamed authors for tela's own behaviour.

**Backslash escapes are honoured.** `\<all>` is literal text, not a tag — and
tela's editor EMITS those escapes when it serializes, so every page a person
typed in the browser arrived here pre-escaped and got reported anyway. Scanning
raw text meant flagging the editor's correct output. `MaskCode` now blanks
`\`+punctuation along with code.

**`<br>`/`<wbr>` are never reported.** Not because they're minor: the editor's
commonmark preset serializes every *empty paragraph* as `<br />`
(`remarkPreserveEmptyLinePlugin`). A page whose author added no markup at all
comes back full of them. Nothing visible goes wrong either, so there is no
finding to make.

**`dropped-html` reports once per tag NAME per page**, anchored at the first
occurrence with a count, because a tag repeated down a page is one thing to fix,
not twelve.

The two shapes of `dropped-html` also read differently on purpose. `<span style>`
loses its styling but keeps its text; a placeholder like `<all>` or `<commit>`
has no inner text, so the word is **deleted from the page** — that message says
so and tells the author to use backticks.

### `oversized-table`: measure the render, not the markdown

A table wider than the page column doesn't lose anything — it scrolls sideways —
but past a point a reader never sees most of it at once, and the data wants a
**sheet** (`sheet: true`, whose body is the same GFM table) rather than a
document. That's what the rule suggests.

The trigger is the table's estimated **rendered width**, not its column count.
A sweep of the live wiki (295 tables on 229 prose pages) is why: the widest
tables by column count are an 11-column tracker of two-digit numbers and two
12-column stubs, all of which fit the page column comfortably. Column count
would have been wrong three times out of four on its first run.

Width is estimated per column as its widest cell, capped at the point a cell
wraps instead of widening, and measured in RENDERED characters — a link counts
as its text and `**bold**` markers count as nothing, or a column of long URLs
would read as unbearably wide. On that measure the sweep separates cleanly: one
table at 1973px, the next at 993px, so the 1250px cap (~1.5 page columns) fires
once in 295 and has room either side.

Unlike `dangling-wikilink` this is shown to humans too. The finding isn't "your
table is wide" (which the author can see) but "this belongs in a sheet" (which
they can't), and at one hit in 295 tables it can't become nagging.

The table above was **verified against the live reader**, not derived from
reading the renderer. That mattered: `ragged-table`'s first message claimed the
extra cells were dropped (what the GFM spec says); the reader actually renders
them as a stray column. A lint that misdescribes the render is worse than no lint,
because its whole value is being trusted about something the author can't see.

### Who is the finding for?

A rule earns a place in the EDITOR panel only if the author can't already see
the problem. `dangling-wikilink` fails that: an unresolved `[[Name]]` renders as
a **red** broken link (`tela-wikilink--broken`) in the body itself, and linking
before creating is normal wiki practice. Warning about it produced 166 of the
live wiki's 179 findings — 93% — burying the 13 real ones.

An agent sees markdown, not colour, so the finding is genuine for them. Hence
`rulesTheReaderAlreadyShows`: the MCP report and the write advisory keep it, the
REST route strips it via `forHumans` and recounts. The general principle — a
true finding is not automatically a useful one; what earns a row is whether the
audience could have seen it otherwise.

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

### Palette names vs directive names

A block's `id`/label and the `:::directive` it is written as are **different
namespaces, and they can't be unified**: the pull-quote block (id `pull-quote`)
is written `:::quote`, while `quote` is already the plain blockquote's id.
Trying to make them equal collides.

So the relationship is made explicit and checked instead. The generator derives
each block's `directive` from its own `syntax` (never hand-declared, so it can't
drift from the example agents are shown) and **fails the gate** if that name
isn't one the renderer knows — a block can no longer document syntax that
silently unwraps.

Guessing the palette name is still natural, and did happen: a live page wrote
`:::stat-grid`. That's now self-correcting rather than a dead block — the
backend maps each block's id and slugified label to its real directive, so the
report says *"`stat-grid` is the block's name in the palette, not the directive
you write. Use `:::stats`."*

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

**The screenshot must request `emulatedMediaType: print`.** tela's desktop shell
is fixed-height (`h-dvh`, content scrolling inner `overflow-y` frames — see the
two-scroll-models gotcha in CLAUDE.md), so under screen media the *document* is
exactly one viewport tall and Chromium cannot capture what lives inside an inner
scroller. A "full page" capture then returns the first screenful and nothing
says so — it shipped that way for one deploy and looked entirely plausible.
Print media is what releases the content to flow, which is why `renderPDF` never
had the problem. Verified: 1100×600 before, 1100×4306 after, callout backgrounds
and tables intact.

Deck and sheet pages are refused by every entry point (`wrongBodyKind`), with a
pointer to the validator that owns them. This is not tidiness: a sweep of the
live docs space ran the prose rules over a real deck and produced ~30
`dropped-html` reports against tahta components (`<callout>`, `<stat>`,
`<badge>`) that are entirely correct in Slidev markdown. A lint that cries wolf
on a good page is worse than none.

The text arrives via the PDF text layer, so it carries **layout** artifacts —
ligatures (`ﬁ`), hyphenation, line breaks around inline code. The result says so
explicitly, because an agent asked to diff intent against output will otherwise
report `ﬁlter` as a rendering defect. What the preview is good for is presence,
wording and order: anything missing there is genuinely missing on the page.

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
