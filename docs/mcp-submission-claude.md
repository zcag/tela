# tela — Claude connector directory submission

Ready-to-paste payload for `https://clau.de/mcp-directory-submission`.
Values verified live 2026-06-05; tool count re-checked 2026-08-28 — the surface is **47 tools**
(the earlier "39" is stale; count from `Name:` fields across `backend/internal/api/mcp*.go`). `‹TODO›` marks anything Cagdas still has to fill or confirm.

---

## 1. Listing copy

**Server name:** tela

**Tagline (one line):** The team wiki your AI agents can read, search, and write — over MCP.

**Short description (≤ 50 words):**
tela is a self-hostable, markdown-native team wiki with a built-in MCP server. Agents read pages, run full-text and semantic search, and create or edit content alongside the human team. Page bodies are canonical markdown — what an agent writes is exactly what the team sees.

**Long description (2–3 short paragraphs):**

tela is a markdown-native team wiki you self-host. The MCP server is built into the backend, not bolted on as a chat feature: agents authenticate with your account's permissions and get the same read/write surface the web UI uses — list and read spaces and pages, search, create and update pages, comment, and import external pages. Reads and writes are cleanly separated, and write access is scoped to the token.

Knowledge lives as plain markdown (`pages.body` is canonical — there is no proprietary block format), so an agent's edits are diffable, exportable, and identical to what the team reads. Three search paths are exposed: ranked full-text search, body search, and meaning-aware semantic search with citations (page id + heading path), which lets an agent answer a question and cite where the answer came from.

tela is open source (`github.com/zcag/tela`) and first-party — the MCP server domain is the service domain (`telawiki.com`). On hosts that support interactive MCP Apps, results can render as a page-reader widget or search-results cards instead of raw text.

---

## 2. Use cases

Concrete agent workflows, each grounded in the actual tools:

1. **Answer a question from the wiki, with citations.** An agent runs `research` over the team's pages — one call returns assembled grounding (full relevant page bodies, cited `sources`, flagged disagreements, a confidence signal) — reads deeper with `read_chunk` / `get_page` if needed, and answers, citing the page id and heading path each claim came from.
2. **Draft a runbook from a deploy log.** Given a deploy or incident log in the conversation, an agent writes a structured runbook and persists it with `create_page` in the relevant space, so the next session (human or agent) starts from durable team memory instead of re-pasting context.
3. **Keep a doc current after a change.** An agent that just shipped a change finds the affected page via `search`, patches the body with `update_page` (which auto-snapshots a revision), and leaves an anchored note with `add_comment`.
4. **Audit what links to a page before editing it.** Before a rename or restructure, an agent calls `list_backlinks` to see every page that references the target, then uses `move_page` / `update_page` without breaking references.
5. **Attach a file to a page and make it searchable.** An agent uploads a PDF with `upload_attachment` — it renders inline on the page and is immediately indexed so `research` can cite it in answers alongside page content.

---

## 3. Technical

| Field | Value |
|---|---|
| **Endpoint URL** | `https://telawiki.com/api/mcp` |
| **Transport** | Streamable HTTP (current MCP transport; SSE not used) |
| **Auth type** | OAuth 2.1 — WorkOS AuthKit, issuer `https://decisive-relation-32-staging.authkit.app`; PKCE S256 + Dynamic Client Registration. HTTP-transport hosts run the flow automatically; sign-in happens on first use, no token handling. A Personal Access Token bearer is also accepted, but only for stdio-only hosts via the `tela-mcp` npm proxy — the directory flow uses OAuth. |
| **Read capabilities** | List/read spaces and pages, list backlinks and shares, read page chunks, full-text and semantic search, Atlas project listing and run status, deck guide/lint/preview, file listings, knowledge hygiene (related, overlaps, gaps), document fetch (fixed-shape `search`/`fetch` contract). |
| **Write capabilities** | Create/update/patch/delete pages and spaces, move pages, add comments, share/revoke links, upload/delete files, trigger Atlas runs, author deck images, submit feedback. All write tools require editor+ scope on the target space; token permissions gate every call. |
| **Allowed Link URIs** | `https://telawiki.com/*` (the app surfaces page/space deep links under this origin). `‹TODO: confirm whether the form wants this populated; otherwise leave blank›` |

---

## 4. Data & compliance

- **Third-party connections:**
  - **WorkOS AuthKit** — OAuth 2.1 identity provider for the Connect flow (issuer `decisive-relation-32-staging.authkit.app`). Handles sign-in/consent; tela receives the resulting identity.
  - **Remote embedder (optional, `research`)** — see embedder note below.
  - **Remote embedder (optional, `research`)** — semantic retrieval embeds query/content via an operator-configured Ollama instance (`TELA_RAG_EMBED_URL`); on the public instance this is a dedicated, isolated embed-only endpoint. If unconfigured the tool no-ops (503). No content leaves the operator's infrastructure beyond this embedder.
- **Health data (PHI):** No. tela collects no PHI, PCI, government-ID, or secrets.
- **Data category:** Productivity / knowledge management (team wiki content — markdown documents, comments, space metadata).
- **What is read:** spaces, pages (markdown bodies + metadata), backlinks, comments, and search indexes the authenticated account can access.
- **What is written:** pages (create/update/patch/delete), spaces (create/update/delete), comments, uploaded files, share links (mint/revoke), Atlas documentation runs, and free-text feedback — all under the account's permissions; body changes auto-snapshot a revision.
- **What is stored:** content the agent creates/edits is stored in the operator's PostgreSQL database on the operator's server. tela is self-hosted; on the public instance, data lives on `telawiki.com`'s host. No agent chat history or host memory is read or stored.

---

## 5. Tools, resources, widgets

**Every tool carries a human-readable `title` and read/write annotations** (`readOnlyHint`, `destructiveHint`, `openWorldHint`) set explicitly on every tool. Read and write are cleanly separated. **39 tools total (20 read / 19 write)** — updated 2026-06-29 to reflect the current surface.

### Read tools (`readOnlyHint: true`, `destructiveHint: false`, `openWorldHint: false` unless noted)

| Tool | Title | Description |
|---|---|---|
| `list_spaces` | List spaces | List every space the caller can access (id, name, slug, visibility). |
| `get_space` | Get space | Fetch a single space's metadata by id. |
| `list_pages` | List pages | Flat or child-filtered page listing in a space. |
| `get_page` | Get page | Full markdown body + metadata for a page id. Optional `format: map` returns section paths for `patch_page`. |
| `list_backlinks` | List backlinks | Pages that link to the given page via `[[wikilink]]` or `tela://page/{id}`. |
| `list_shares` | List shares | Active public share links for a page. |
| `search` | Search | Keyword (full-text) lookup over title + body, snippet-highlighted. Always available (no embedder). |
| `research` | Research wiki | Semantic, answer-oriented retrieval: assembled grounding (full relevant page bodies, cited `sources`, flagged `disagreements`, `low_confidence`). Requires a configured embedder. |
| `read_chunk` | Read chunk | Fetch one chunk's full section text by `chunk_id` (from a `research` source). Spans pages and uploaded files. |
| `fetch` | Fetch document | Fetch a page's full text by id (the id comes from a search result); the fixed-shape companion to `search`. |
| `related_pages` | Related pages | Semantic nearest-neighbor lookup from a page's chunks. |
| `suggest_links` | Suggest links | Pages whose content strongly overlaps a given page — link opportunities. |
| `find_overlaps` | Find overlaps | Near-duplicate page detection across a space. |
| `knowledge_gaps` | Knowledge gaps | Topic areas mentioned but not covered in depth (admin-scoped). |
| `list_attachments` | List attachments | Uploaded files on a page (id, name, mime, size, url). |
| `list_comments` | List comments | A page's comment threads, or an inbox of open threads across a space; filter by status and poll with a cursor. |
| `deck_authoring_guide` | Deck guide | Returns the full slide-authoring guide (layouts, fields, variants). Read before authoring a deck. |
| `lint_deck` | Lint deck | Validate a deck body's frontmatter, layouts, and field values before saving. |
| `preview_deck` | Preview deck | Render a live preview URL for a deck body — confirm appearance before committing. |
| `atlas_list_projects` | List Atlas | List Atlas projects the caller can see (id, name, source type, last-run status). |
| `atlas_run_status` | Atlas status | Read an Atlas run's current stage, coverage metrics, and statistics. |

### Write tools (`readOnlyHint: false`; `openWorldHint` noted where true)

| Tool | Title | `destructiveHint` | Description |
|---|---|---|---|
| `create_page` | Create page | false | Create a page in a space (editor+). Body is markdown; `tela://page/{id}` links index as backlinks. |
| `update_page` | Update page | false | Patch a page's title and/or body (editor+). Body change auto-snapshots a revision. Idempotent. |
| `patch_page` | Patch page | false | Surgically edit ONE section of a page by heading path — cheaper and safer than rewriting the full body. |
| `move_page` | Move page | false | Reparent, reorder, or relocate a page to another space (editor+ on both sides). |
| `delete_page` | Delete page | **true** | Delete a page (editor+). Backlinks preserved with last-known title. |
| `add_comment` | Add comment | false | Start a comment thread anchored by a `{prefix, exact, suffix}` text triplet, or reply inside one via `parent_id` (editor+). |
| `update_comment` | Update comment | false | Resolve or reopen a comment thread, or edit the text of a comment the caller wrote (editor+). Idempotent. |
| `create_space` | Create space | false | Create a space; caller becomes owner. Slug derived from name when omitted. |
| `update_space` | Update space | false | Patch a space's name and/or slug (editor+). Idempotent. |
| `delete_space` | Delete space | **true** | Delete a space and all its pages, comments, revisions, share links. Owner only. Irreversible. |
| `share_page` | Share page | false | Mint a scoped public share link for a page (read-only or editor). |
| `revoke_share` | Revoke share | **true** | Revoke a public share link — link stops working immediately. |
| `upload_attachment` | Upload file | false | Upload a file and attach it to a page. Images render inline; PDFs are indexed for `research`. |
| `request_attachment_upload` | Request upload | false | Get a pre-signed URL for large-file upload (call `confirm_attachment_upload` after). |
| `confirm_attachment_upload` | Confirm upload | false | Confirm a pre-signed upload completed and register the attachment. |
| `delete_attachment` | Delete file | **true** | Delete an uploaded file from a page. |
| `atlas_run` | Run Atlas | false | Trigger a full Atlas documentation run for a project (management access required). |
| `generate_deck_image` | Gen deck image | false | Raster-image generation for slide assets (advanced; most decks don't need this directly). |
| `treat_deck_image` | Treat deck img | false | Post-process / composite a generated deck slide image. |
| `submit_feedback` | Submit feedback | false | Submit free-text feedback about tela itself (not page content). |

### Resources (2 templates)

| URI template | Description |
|---|---|
| `tela://page/{id}` | A page by id. |
| `tela://space/{id}` | A space by id. |

### Interactive widgets (2 — MCP Apps category)

| Widget | Description |
|---|---|
| **page-reader** | Renders a page's markdown as an interactive reader component instead of raw text. |
| **search-results** | Renders search hits as interactive result cards. |

---

## 6. Links

| Field | Value |
|---|---|
| **Documentation** | `https://telawiki.com/mcp` |
| **Privacy policy** | `https://telawiki.com/privacy` |
| **Support channel** | `tela@telawiki.com` |
| **Source** | `https://github.com/zcag/tela` |
| **npm proxy** | `tela-mcp` (`https://www.npmjs.com/package/tela-mcp`) |
| **Review escalation (Anthropic)** | `mcp-review@anthropic.com` |

> Note: `/mcp` and `/privacy` deploy today via `make deploy-landing` — confirm both return 200 before submitting.

---

## 7. Test account — reviewer script

A reviewer connects via OAuth and exercises a read→write round-trip in a demo space.

**Credentials:** login **`mcp-demo`** / email **`mcp-demo@cagdas.io`** — password **kept out of this public repo; paste it into the submission form** (it's `Tela…2026`; ask Cagdas / see secrets). Email-verified, **no MFA** (tela has no MFA), not an admin. Already populated: space **"Demo"** with 5 pages — Deploy runbook, Incident response, Release checklist, On-call rotation, Architecture overview (so `search "deploy"` and `research` return real hits). Re-seed any time with `scripts/seed-demo.py`.

**Steps:**

1. **Connect.** Add tela as a custom connector pointing at `https://telawiki.com/api/mcp`. Claude runs the OAuth flow.
2. **Sign in.** The flow opens the tela login screen. Sign in with the demo credentials above (no MFA, no email step). Consent on the WorkOS consent screen.
3. **Tools appear.** After consent the 39 tools become available in chat.
4. **Try, in order:**
   - `list_spaces` → confirm the demo space is listed.
   - `search` for `deploy` → confirm ranked, snippet-highlighted hits.
   - `get_page` on a hit → confirm full markdown body returns.
   - `research` for a paraphrased question → confirm assembled `context` + cited `sources` (page id + heading path).
   - `create_page` in the demo space → confirm a new markdown page is created; re-`search` to see it indexed.
   - (optional) `update_page` then `delete_page` on the page just created → confirm the write/destructive round-trip.

Everything runs with the demo account's permissions; an agent can only touch what that account can.

---

## 8. Branding

- **Connector icon:** served by the backend as a full-bleed square SVG **data URI** in the MCP `Implementation.Icons` (`backend/internal/api/mcp.go`), MIME `image/svg+xml`, sizes `any`. Full-bleed by design (no baked-in rounded corners) so the host's rounding mask renders clean — no white corners.
- **Favicon:** `https://telawiki.com/favicon.svg` (source: `landing/public/favicon.svg`).
- **Server branding:** the MCP `Implementation` advertises title **"Tela"** and `WebsiteURL` `https://telawiki.com` for the connector card.
- **Logo asset (standalone, if the form wants a separate upload):** `‹TODO: provide a standalone logo SVG/PNG URL if the data-URI icon isn't accepted as-is›`
- **Widget screenshots (MCP Apps category):** `‹TODO: capture page-reader + search-results carousel screenshots if opting the widgets into the MCP Apps category›`

---

## 9. GA date & tested surfaces

- **GA date:** `‹TODO›`
- **Tested surfaces:** `‹TODO — e.g. Claude.ai web, Claude desktop; note MCP Inspector pass over all 20 tools + 2 resources + 2 widgets›`

---

## Open `‹TODO›`s for Cagdas

- Populated, no-MFA **demo login** (section 7).
- **GA date** + **tested surfaces** (section 9).
- Confirm whether the form wants **Allowed Link URIs** populated (section 3).
- Optional **standalone logo** asset and **widget screenshots** if listing under MCP Apps (section 8).
- Verify `/mcp` and `/privacy` are **live** (200) post-`make deploy-landing` (section 6).
