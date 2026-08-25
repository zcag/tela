# tela for Claude Code

Connects Claude Code to your [tela](https://telawiki.com) workspace over tela's
built-in MCP server at `https://telawiki.com/api/mcp`.

Sign-in is OAuth — no API key and no config file. On first use Claude Code opens
tela's normal sign-in, and the connection inherits your account's permissions:
the agent sees exactly the spaces you see, and write tools need editor access on
the space they touch.

## What it adds

- **`research`** — semantic, answer-oriented retrieval. One call returns assembled
  grounding plus its sources, so Claude answers from your docs *with citations*
  instead of guessing.
- **`search`** — ranked full-text lookup when you know the term or the page name.
- **Authoring** — create and patch pages using tela's block palette (callouts, tabs,
  collapsibles, diagrams), edit spreadsheets with real formulas, and write Slidev
  decks you can present in the browser.
- **Graph & hygiene** — backlinks, related pages, link suggestions, overlap and
  knowledge-gap detection.

Page bodies are canonical markdown, so what the agent writes is exactly what your
team reads — diffable and exportable, with no proprietary block format in between.

## Install

```
/plugin marketplace add zcag/tela-claude-plugin
/plugin install tela@telawiki
```

Then run `/mcp` and sign in to tela.

## Self-hosting tela

This plugin points at tela's hosted instance. If you run your own tela, skip the
plugin and connect the instance directly:

```
claude mcp add --transport http tela https://your.host/api/mcp
```

See [Agents & MCP](https://telawiki.com/tela/docs) for the full client matrix.

## About this repository

This repo is the plugin wrapper only — a manifest, an MCP declaration, and a skill.
It is generated from [`plugin/`](https://github.com/zcag/tela/tree/main/plugin) in the
[tela repository](https://github.com/zcag/tela), which is where changes belong.
tela itself is open core, AGPL-3.0.
