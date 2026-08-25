# The Claude Code plugin

A third distribution channel, distinct from the connector directories in
[`mcp-directory-submission.md`](mcp-directory-submission.md): a **Claude Code
plugin** that declares tela's hosted MCP endpoint, so a Claude Code user gets
tela with two commands instead of a manual `claude mcp add`.

Source of truth is [`plugin/`](../plugin) in this repo. The published article is
a separate public repo, `zcag/tela-claude-plugin`, mirrored by `make plugin-publish`.

## Why a separate repo

Anthropic's directory scan is explicit that *"a plugin installed from a git source
clones the ENTIRE repo to the user's disk."* Pointing a plugin at tela proper would
ship backend, frontend, landing and deploy config to every installing user. So the
wrapper lives in its own tiny repo — but is **authored here**, under this repo's
conventions, and mirrored out. Editing the wrapper repo directly is the drift path;
don't.

## What's in it

Five files, none of them tela source:

| File | What it does |
|---|---|
| `.claude-plugin/marketplace.json` | Makes the repo its own marketplace, so `/plugin marketplace add zcag/tela-claude-plugin` works with no directory involved |
| `plugins/tela/.claude-plugin/plugin.json` | Plugin metadata |
| `plugins/tela/.mcp.json` | Six lines pointing at `https://telawiki.com/api/mcp` |
| `plugins/tela/skills/tela-wiki/SKILL.md` | Discovery stub — see below |
| `README.md` | The wrapper repo's GitHub front page |

`make plugin-validate` runs `claude plugin validate --strict` over both manifests
and fails on version drift between `plugin.json` and the marketplace entry (the
validator only *warns* about that, and a mismatch silently pins users to the wrong
version).

## The skill is a stub, deliberately

The backend already ships the retrieval + authoring guides as MCP server
Instructions (`mcp.go` → `retrievalGuideMarkdown() + authoringGuideMarkdown(false)
+ importInstructionsSnippet()`), generated from `blocks_gen.json` and gated by
`make blocks-gate`. **Restating any of that in the plugin puts authoring rules in a
repo the gate can't see** — guaranteed drift, and exactly what the block-manifest
single-source rule exists to prevent.

So the skill carries only what the server can't: a `description` that makes Claude
reach for the wiki unprompted (that's the whole reason it exists — MCP instructions
don't trigger on intent), the two-doors retrieval split, and two habits that aren't
in the generated guides (`patch_page` over `update_page`; never create a space
unbidden). It defers everything else to `deck_authoring_guide`,
`sheet_authoring_guide`, and the `tela://authoring-guide` resource. Cost is ~64
tokens always-on, ~520 on invoke.

## Cloud only, by design

The plugin hardcodes `https://telawiki.com/api/mcp`. Self-hosters don't need it —
they point Claude Code at their own instance
(`claude mcp add --transport http tela https://your.host/api/mcp`), which the docs
space already covers. Don't add a `userConfig` base-URL prompt to serve a case that
already has a better answer.

## Two routes out

1. **Direct** — `/plugin marketplace add zcag/tela-claude-plugin` then
   `/plugin install tela@telawiki`. Live the moment the repo is public: no queue,
   no review, and ours to fix. This is the route to put in docs and launch posts.
2. **The directory** — `platform.claude.com/plugins/submit` lists it in the
   **`claude-community`** marketplace (opt-in; a user must `/plugin marketplace add`
   it), *not* the curated `claude-plugins-official` shelf that auto-loads — that one
   is vendor curation "at Anthropic's discretion" with no documented promotion path
   up from community. Review is their public CI: manifest validation plus an LLM
   security scan against the Software Directory Policy. No SLA. Entries pin to a
   commit SHA and their CI bumps it, so pushes are picked up without re-submitting.

Treat (2) as upside on top of (1), and don't let launch copy call it distribution
to every Claude Code user.

## Known friction

- **Claude-account users already have tela** if they added it as a claude.ai
  connector — Claude Code inherits those. The plugin's value is discovery, plus
  reaching Claude Code users on API/Bedrock setups with no connector list.
- **The AuthKit issuer says `staging`.** Sign-in bounces through
  `decisive-relation-32-staging.authkit.app` (a deliberate 2026-07-31 call — moving
  WorkOS envs would force re-consent on live connections). It's more exposed here
  than in the connector case: developers read the address bar at first install.
  Whether a WorkOS custom domain on the *same* env fixes the hostname without
  changing the issuer is unverified.
