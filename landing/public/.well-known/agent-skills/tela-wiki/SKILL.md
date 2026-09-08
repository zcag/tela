---
name: tela-wiki
description: Use when the user asks something their team's own documentation would answer, or asks to write or update a page, spreadsheet, or slide deck in their tela wiki.
---

# tela

The team's wiki is reachable over the `tela` MCP server. Two doors in, by intent:

- **`search`** — keyword lookup. Use when you can name the page, or know an exact
  term, identifier, or error string in it. Ranked and snippet-highlighted.
- **`research`** — semantic and answer-oriented. Use to answer a question or gather
  everything on a topic by meaning. One call returns assembled grounding, the cited
  `sources` to cite by `[n]`, any flagged `disagreements`, and a `low_confidence` flag.

Then read deeper: `get_page` for a whole page, `read_chunk` for one section,
`list_backlinks` / `related_pages` to follow the graph.

## Writing

Authoring guidance is **not repeated here** — the server ships it and it stays current
with the product. The MCP instructions carry the block palette (a page body is markdown,
but the reader renders callouts, tabs, collapsibles, and real diagrams); `deck_authoring_guide`
and `sheet_authoring_guide` carry decks and spreadsheets; the `tela://authoring-guide`
resource has the full block reference. Read the relevant one before authoring, and run
`lint_page` before you call a page done.

Two habits those guides don't cover:

- Prefer `patch_page` over `update_page` when changing part of a page — it targets one
  section instead of rewriting the body, so it won't clobber a concurrent edit.
- `list_spaces` and write into an existing space. Never create a space unbidden.
