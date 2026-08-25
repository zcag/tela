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
2. **The directory** — one form, `platform.claude.com/plugins/submit` (or
   `claude.ai/admin-settings/directory/submissions/plugins/new`), feeding **both**
   marketplaces. Verified 2026-08-25, because the received wisdom here is wrong:
   `claude-plugins-community` (2.282 entries) is the opt-in shelf a user must
   `/plugin marketplace add`; `claude-plugins-official` (289) auto-loads for every
   Claude Code user. The official one is **not** a closed vendor shelf — its repo has
   an `external_plugins/` tree ("third-party plugins from partners and the community")
   and its README points third parties at *the same* `clau.de/plugin-directory-submission`
   form. 167 names appear in both marketplaces, 145 of them resolving to an identical
   repo URL. So there IS a path onto the auto-loaded shelf; it's a curation gate
   ("external plugins must meet quality and security standards"), not a closed door.

   Review is automated screening, no SLA. Entries pin to a commit SHA; pushes are
   picked up automatically without re-submitting. The community mirror syncs nightly.

Ship (1) first — it works today and needs nobody's approval. Until (2) lands on the
*official* shelf, don't let launch copy call it distribution to every Claude Code
user; landing only in `claude-community` is a listing on an opt-in shelf of 2.282.

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

## Submission requirements — checked 2026-08-25

From the [submission docs](https://claude.com/docs/plugins/submit) and the
[Software Directory Policy](https://support.claude.com/en/articles/13145358-anthropic-software-directory-policy).

| Requirement | State |
|---|---|
| Public GitHub repo ("closed-source plugins are not accepted") | ⬜ `zcag/tela-claude-plugin` not created yet |
| `claude plugin validate` passes | ✅ `make plugin-validate`, `--strict`, both manifests |
| Plugin name free in the directory | ✅ no `tela` among the 2.282 community entries |
| Subdirectory layout supported | ✅ 406 entries use the `git-subdir` source shape |
| Privacy policy at a public URL | ✅ `/privacy`, linked from the plugin README |
| Verified contact + support channel | ✅ `tela@telawiki.com`, linked from the plugin README |
| Documented functionality/purpose/troubleshooting | ✅ README + `/mcp` + the docs space |
| ≥3 working examples of core features | ✅ README "Try it"; 5 more in `mcp-submission-claude.md` |
| Testing account with sample data, no MFA | ✅ `mcp-demo`, space 4 (`scripts/seed-demo.py`); password held outside the repo |
| OAuth 2.0, certs from recognized authorities | ✅ WorkOS AuthKit, PKCE S256 + DCR, valid TLS |
| Owns the endpoint/domain it connects to | ✅ `telawiki.com` |
| Collects no extraneous conversation data | ✅ reads only what the authenticated account can see |
| Not a prohibited category | ✅ not financial execution, media generation, or an ad vehicle |
| Submitter role: Console Developer/Admin/Owner, **or** claude.ai Team/Enterprise with directory management | ⚠️ the one open gate — see below |

**The submitter-role gate is the thing to get right.** A free Console org at
`platform.claude.com` satisfies it, and the docs name that path explicitly: *"Individual
authors who aren't part of a claude.ai Team or Enterprise organization can sign up for
Console at platform.claude.com and submit there."* That also routes **around** the stuck
connector-directory submission, which is pinned to a workspace whose owner address is dead
— don't submit the plugin from that org.

**A lapsed trial does not break the demo account.** `planFor` falls back to `plan_key`
(free) after the 7-day grace, and `personal_free` carries 3M embed tokens/month
(`0070_atlas_cost_quotas.sql`), so a reviewer can still exercise `research`. Worth one
manual smoke-test at submit time anyway, since the demo space was seeded in June.
