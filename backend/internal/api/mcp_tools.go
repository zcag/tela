package api

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zcag/tela/backend/internal/models"
	"github.com/zcag/tela/backend/internal/rag"
	"github.com/zcag/tela/backend/internal/sheetproj"
)

// retrievalGuideMarkdown is the "how to find things" preamble carried in the
// server Instructions. The retrieval surface is deliberately two doors, split by
// intent; this states the split once globally so a host doesn't have to infer it
// from N look-alike tool descriptions.
func retrievalGuideMarkdown() string {
	return "# Finding things in the wiki\n\n" +
		"Two ways in, by intent:\n\n" +
		"- **`search`** — keyword / full-text lookup. Use when you can name the page, or know an exact term, identifier, or error string in it. Ranked, snippet-highlighted, and always available (needs no embedder).\n" +
		"- **`research`** — semantic, answer-oriented. Use to answer a question or gather everything relevant on a topic by meaning. One call returns assembled grounding (full relevant page bodies, not fragments), the cited `sources`, any flagged `disagreements`, and a `low_confidence` flag — you write the answer from it and cite by `[n]`. Needs a configured embedder.\n\n" +
		"Then read deeper as needed: **`read_chunk`** for one section (`chunk_id` from a `research` source), **`get_page`** for a whole page, **`list_backlinks`** / **`related_pages`** to follow the graph.\n\n"
}

// registerMCPTools wires the tela tool surface onto the MCP server. Each tool
// reads identity from the request (mcpIdentity), calls the shared xCore that
// also backs the REST route, and returns a typed Out so the SDK emits an output
// schema + structured content. Write tools additionally gate on key scope via
// mcpRequireWrite (the public-path mount defers method-scope to the tool).
func (s *Server) registerMCPTools(server *mcp.Server) {
	// Hints default to OPEN/DESTRUCTIVE when absent (MCP spec: OpenWorldHint and
	// DestructiveHint default true), so every tool sets them explicitly — both
	// directories reject tools whose advertised hints don't match behavior. tela
	// is a closed world (its own DB): every tool sets OpenWorldHint false. *bool
	// so the value survives the SDK's omitempty.
	no, yes := false, true
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: &yes, DestructiveHint: &no, OpenWorldHint: &no}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_spaces",
		Title:       "List spaces",
		Description: "List every space the API key can access (id, name, slug). Read-only. Start here to discover a `space_id` for list_pages / search / create_page when you don't already have one; for a single space you already know, use get_space instead.",
		Annotations: readOnly,
	}, s.mcpListSpaces)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_space",
		Title:       "Get space",
		Description: "Fetch one space's metadata (id, name, slug) by id. Read-only. Use when you already hold a space_id and just need its name/slug; to discover spaces use list_spaces, to list its pages use list_pages.",
		Annotations: readOnly,
	}, s.mcpGetSpace)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_pages",
		Title:       "List pages",
		Description: "Flat page listing in a space (read-only). Pass parent_id for the direct children of one page; omit for top-level pages. Use to browse a space's structure or find a page_id; to find pages by keyword use `search`, and to read a page's body use get_page.",
		Annotations: readOnly,
	}, s.mcpListPages)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_page",
		Title:       "Get page",
		Description: "Full markdown body + metadata for a numeric page id. Includes an `epistemic` block — trust signals computed from the wiki's own state: freshness (age, stale, review_overdue), provenance (human / agent / sync), and corroboration vs. dispute against same-space pages. Weigh it: prefer fresh, corroborated, human-reviewed pages; treat a stale or disputed page as lower-confidence and check its listed disputes before relying on it.",
		Annotations: readOnly,
		Meta:        widgetToolMeta(uiPageReaderOpenAI, uiPageReaderMCPApp, "Renders the page as formatted markdown.", "Opening page…", "Page ready"),
	}, s.mcpGetPage)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_backlinks",
		Title:       "List backlinks",
		Description: "List the pages that link TO the given page via [[wikilink]] / tela://page/{id} — its incoming references (read-only). Use to see what depends on a page before you edit or delete it, or to walk the explicit link graph; for semantically similar pages that don't link it, use related_pages.",
		Annotations: readOnly,
	}, s.mcpListBacklinks)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search",
		Title:       "Search",
		Description: "Keyword (full-text) lookup over title + body, ranked + snippet-highlighted. Use to find a page you can name or that contains an exact term/identifier/error string. Works WITHOUT an embedder (always available). Optional space_id narrows to one space. To answer a question or gather material on a topic by meaning, use `research` instead.",
		Annotations: readOnly,
		Meta:        widgetToolMeta(uiSearchOpenAI, uiSearchMCPApp, "Renders search hits as a clickable result list.", "Searching…", "Results ready"),
	}, s.mcpSearch)

	mcp.AddTool(server, &mcp.Tool{
		Name:  "research",
		Title: "Research the wiki",
		Description: "Answer a question — or gather everything relevant on a topic — from the wiki by MEANING. One call assembles answer-ready grounding: the full bodies of the pages that matter (not isolated fragments), pulled from pages AND attached files (PDFs, docs), plus any flagged disagreements among the sources and a low_confidence signal. " +
			"Returns `context` (a numbered [n] excerpt block to ground your answer), `sources` (the cited hits aligned to [n], each with page_id/chunk_id for drill-in and a download_url for file sources), `disagreements` (conflicts to surface, [n]-keyed), and `low_confidence`. " +
			"YOU write the answer from `context` and cite sources by their [n]. To read one section deeper use `read_chunk` (chunk_id from a source) or `get_page` (full page). For exact-name/term lookup use `search`. Requires a configured embedder (503 otherwise).",
		Annotations: readOnly,
	}, s.mcpResearch)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "read_chunk",
		Title:       "Read chunk",
		Description: "Fetch one chunk's full section text by chunk_id (from a `research` source), for a page OR a file chunk (read-only). Middle granularity — use it to expand a single cited section; for the whole page use get_page. A file chunk cites its file (file_name + parent page_id + download_url).",
		Annotations: readOnly,
	}, s.mcpReadChunk)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "related_pages",
		Title:       "Related pages",
		Description: "Pages semantically related to a given page (\"see also\"), ranked by similarity — discovery beyond explicit links (read-only). Use for topically similar pages; for pages that explicitly link this one, use list_backlinks. Works without a live embedder (uses stored vectors).",
		Annotations: readOnly,
	}, s.mcpRelatedPages)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "suggest_links",
		Title:       "Suggest links",
		Description: "Given draft text (a page you're writing), return existing pages it should link to, by semantic similarity. Use while authoring to wire a new page into the knowledge base instead of leaving it an orphan. Requires a configured embedder.",
		Annotations: readOnly,
	}, s.mcpSuggestLinks)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "find_overlaps",
		Title:       "Find overlapping pages",
		Description: "Near-duplicate page PAIRS that share a near-identical passage (real merge/redirect candidates) for wiki hygiene. Optional space_id restricts to one space; threshold (0..1, default 0.92) is the minimum chunk-level similarity to count as a duplicate.",
		Annotations: readOnly,
	}, s.mcpFindOverlaps)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "knowledge_gaps",
		Title:       "Knowledge gaps",
		Description: "The most-asked \"ask your docs\" questions the corpus could NOT answer — a content roadmap. Instance-admin only (exposes users' questions). Optional since_days window.",
		Annotations: readOnly,
	}, s.mcpKnowledgeGaps)

	// `search` (above) + `fetch` are the ChatGPT Deep Research / company-knowledge
	// compatibility pair (read-only, fixed names/shapes). `search` already returns
	// id/title/text/url per result; `fetch` returns a page's full text by that id.
	mcp.AddTool(server, &mcp.Tool{
		Name:        "fetch",
		Title:       "Fetch document",
		Description: "Fetch a tela page's full text by id (the id comes from a search result) — the fixed-shape companion to `search`. Read-only. Prefer get_page for normal use (same body plus richer metadata and trust signals); reach for `fetch` only when the host requires the fixed search/fetch contract.",
		Annotations: readOnly,
	}, s.mcpFetch)

	// ---- write tools (gate on write/admin scope via mcpRequireWrite) ----
	// Writes are closed-world (OpenWorldHint:false) and additive (DestructiveHint:
	// false) unless noted; deletes set DestructiveHint:true, idempotent patches
	// set IdempotentHint:true.

	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_page",
		Title:       "Create page",
		Description: "Create a page in a space (editor+). Body is markdown; tela://page/{id} links and [[Page Title]] wikilinks (resolved by title within the space) are indexed as backlinks. " + authoringToolHint() + deckAuthoringToolHint() + sheetAuthoringToolHint(),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: &no, DestructiveHint: &no, OpenWorldHint: &no},
	}, s.mcpCreatePage)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "update_page",
		Title:       "Update page",
		Description: "Patch a page's title and/or body (editor+). A body change auto-snapshots a revision. " + authoringToolHint() + deckAuthoringToolHint() + sheetAuthoringToolHint(),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: &no, IdempotentHint: true, DestructiveHint: &no, OpenWorldHint: &no},
	}, s.mcpUpdatePage)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "deck_authoring_guide",
		Title:       "Deck authoring guide",
		Description: "Return the full tela deck authoring guide as markdown — every tahta layout with its required/optional fields, the components, and the style variants. Read this FIRST when creating or editing a deck (a deck=true page) so you don't guess at layouts/fields. The guide lists optional capability modules (e.g. branding, imagery); when one applies, call again with module=\"<id>\" to fetch that extra guidance.",
		Annotations: readOnly,
	}, s.mcpDeckAuthoringGuide)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "sheet_authoring_guide",
		Title:       "Sheet authoring guide",
		Description: "Return the full tela spreadsheet (sheet) authoring guide as markdown — the Defter text format, coordinates, the formula functions, and the ```defter-style styling/format/chart syntax. Read this FIRST when creating or editing a sheet (a sheet=true page) so you write valid, well-formatted Defter markdown instead of guessing.",
		Annotations: readOnly,
	}, s.mcpSheetAuthoringGuide)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "import_guide",
		Title:       "Import an existing folder of files",
		Description: "Return the guide for BULK-importing an existing folder of documents (Word docs, spreadsheets, PDFs, notes) into a space as a page tree — rather than hand-creating pages one at a time. Read this FIRST for any 'import', 'migrate my docs', 'bring in this folder', or 'load these files' request. Covers converting Office files locally (docx → pages, spreadsheets → live sheets), attaching PDFs/images, and the import endpoint's dry-run → confirm → commit contract.",
		Annotations: readOnly,
	}, s.mcpImportGuide)

	mcp.AddTool(server, &mcp.Tool{
		Name:  "edit_sheet",
		Title: "Edit sheet (structured)",
		Description: "Make a STRUCTURED edit to a sheet (a sheet=true page), editor+. Prefer this over update_page for sheets: you pass a typed operation and tela rewrites the Defter markdown correctly — inserting a row shifts every formula below it, deleting a column re-references the rest, so you never corrupt the grid by hand-editing text. " +
			"Pass one `op` (or a batch via `ops`, applied atomically). Each op is `{kind, ...}` where kind is one of: " +
			"setCells {cells:[{ref:\"B2\",text:\"=A2*1.2\"}]} — write literals/formulas by A1 ref; " +
			"insertRows/deleteRows {at:<1-based row>, count?}; insertCols/deleteCols {at:<col letter or 1-based index>, count?}; " +
			"setStyle {target:\"A1:C1\"|\"B\"|\"2\", attrs:{...}} — bold/align/fill/number-format a range/col/row; " +
			"setFreeze {rows?, cols?} — freeze N header rows / M leading cols (0 clears); " +
			"addSheet {name, after?}; renameSheet {sheet, name}; deleteSheet {sheet}. " +
			"Ops that target a specific tab take an optional `sheet` (name or 0-based index; defaults to the first). A bad ref/range/sheet is rejected with a fixable error. Call sheet_authoring_guide for the full op reference, cell/style syntax, and formula functions. Returns the updated sheet with formulas computed.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: &no, DestructiveHint: &no, OpenWorldHint: &no},
	}, s.mcpEditSheet)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "lint_deck",
		Title:       "Lint slide deck",
		Description: "Validate a deck page's slides against the tahta theme contract — unknown layouts, missing required fields, type/format mistakes. Run after authoring/editing a deck to catch problems before presenting. Returns structured issues per slide. For the full list of valid layouts and each layout's fields, call deck_authoring_guide.",
		Annotations: readOnly,
	}, s.mcpLintDeck)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "preview_deck",
		Title:       "Preview slide deck",
		Description: "Render a deck page to slide images and return them, so you can SEE how the deck looks (don't author blind). Pass `slides` to preview specific 1-based frames; omit for the first few. Renders are cached.",
		Annotations: readOnly,
	}, s.mcpPreviewDeck)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "lint_page",
		Title:       "Lint page",
		Description: "Check a page for places where the READER renders something different from what the markdown says — an unknown `:::block` or `[!CALLOUT]` that silently unwraps, raw HTML the reader drops, a collapsible whose blank lines are missing so its body vanishes, a `[[wikilink]]` that matches no page, a broken link or attachment, a ragged table, a table so wide the page column shows only part of it. These are invisible when you re-read your own source, which is why re-reading it is not a substitute for calling this. Run it after writing or editing a page.",
		Annotations: readOnly,
	}, s.mcpLintPage)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "preview_page",
		Title:       "Preview page",
		Description: "Render a page and read it back as the reader actually shows it — the same render the PDF export uses, so callouts, tables, math, mermaid and diagrams are all resolved. Use it to check that what you wrote is what appears (text you can diff against your intent), and after any edit whose result you can't be sure of from the markdown alone. `format`: \"text\" (default), \"image\" for a screenshot, \"both\". For a deck page, call preview_deck instead.",
		Annotations: readOnly,
	}, s.mcpPreviewPage)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "patch_page",
		Title:       "Patch page section",
		Description: "Surgically edit ONE section of a page instead of rewriting the whole body (editor+). First call get_page format:\"map\" to see the section paths, then patch the target. Cheaper and safer than update_page on a long page — it never touches the rest of the document. Snapshots a revision like any edit.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: &no, DestructiveHint: &no, OpenWorldHint: &no},
	}, s.mcpPatchPage)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_page",
		Title:       "Delete page",
		Description: "Delete a page (editor+). Backlinks from other pages are preserved with the last-known title.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: &no, DestructiveHint: &yes, OpenWorldHint: &no},
	}, s.mcpDeletePage)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "move_page",
		Title:       "Move page",
		Description: "Move a page: reparent (parent_id), detach to top-level (make_root), reorder (position), and/or relocate to another space (space_id). Editor+ in both source and target space. Provide at least one of space_id / parent_id / make_root / position.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: &no, IdempotentHint: true, DestructiveHint: &no, OpenWorldHint: &no},
	}, s.mcpMovePage)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "share_page",
		Title:       "Share page (public link)",
		Description: "Mint a public share link for a page — a URL anyone can open with NO tela login (editor+). Returns the absolute `url` plus the link's `id` and `token`. Options: include_descendants shares the whole subtree (not just this page); password gates it behind a passphrase; expires_at (UTC 'YYYY-MM-DD HH:MM:SS') auto-expires it. Each call mints a NEW link — use list_shares to see existing ones and revoke_share to disable one. This shares a single page tree; to publish a WHOLE space, use the space's visibility setting instead.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: &no, DestructiveHint: &no, OpenWorldHint: &no},
	}, s.mcpSharePage)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_shares",
		Title:       "List share links",
		Description: "List a page's public share links (editor+): id, token, absolute url, has_password, include_descendants, expires_at, revoked_at. Active links only by default; pass include_revoked to also see revoked ones. Tokens are bearer secrets, so this needs a write-scoped key.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: &yes, DestructiveHint: &no, OpenWorldHint: &no},
	}, s.mcpListShares)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "revoke_share",
		Title:       "Revoke share link",
		Description: "Disable a public share link by its share id (editor+; ids come from list_shares). The link stops working immediately. Idempotent — revoking an already-revoked link is a no-op.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: &no, IdempotentHint: true, DestructiveHint: &yes, OpenWorldHint: &no},
	}, s.mcpRevokeShare)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "add_comment",
		Title:       "Add comment",
		Description: "Attach a root (non-reply) comment to a page, anchored to a specific passage by a {prefix, exact, suffix} text triplet so it stays pinned to the right spot as the page changes (editor+). Pass idempotency_key to make retries safe (a repeat returns the original comment instead of posting a duplicate). Use for feedback ON page content; to report problems with tela itself use submit_feedback.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: &no, DestructiveHint: &no, OpenWorldHint: &no},
	}, s.mcpAddComment)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_space",
		Title:       "Create space",
		Description: "Create a space; the caller becomes its owner (write scope). `slug` is derived from `name` when omitted. Use to start a new top-level container of pages; to add a page inside an existing space use create_page, and to rename a space use update_space.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: &no, DestructiveHint: &no, OpenWorldHint: &no},
	}, s.mcpCreateSpace)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "update_space",
		Title:       "Update space",
		Description: "Patch a space's name and/or slug (editor+); idempotent — pass only the field(s) you want to change. Changing the slug updates the space's URL path (existing page ids and links still resolve). To create a space use create_space, to read it use get_space, to remove it use delete_space.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: &no, IdempotentHint: true, DestructiveHint: &no, OpenWorldHint: &no},
	}, s.mcpUpdateSpace)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "invite_to_space",
		Title:       "Invite to space (by email)",
		Description: "Give a person access to a space by EMAIL ADDRESS — space owner only, write scope. Two outcomes, and the response tells you which happened: if a tela account already uses that address they are added right away and returns `{member}`; if nobody has that address yet, tela SENDS THEM AN EMAIL INVITATION (an outward action reaching a third party) and returns `{invite}` — they get the space automatically when they sign up with that address. Role is owner|editor|viewer (default viewer). Use this to onboard someone who has no tela account; to add an existing user you already know by username, or to change/remove access, use the app's space sharing UI.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: &no, DestructiveHint: &no, OpenWorldHint: &no},
	}, s.mcpInviteToSpace)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_space",
		Title:       "Delete space",
		Description: "Delete a space AND all its pages, comments, revisions and share links. Owner only. Irreversible.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: &no, DestructiveHint: &yes, OpenWorldHint: &no},
	}, s.mcpDeleteSpace)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "submit_feedback",
		Title:       "Submit feedback",
		Description: "Submit free-text feedback about tela / tela-mcp itself (friction, bugs, missing capabilities). NOT for page content — use add_comment for that.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: &no, DestructiveHint: &no, OpenWorldHint: &no},
	}, s.mcpSubmitFeedback)

	// ---- attachments (files on a page: images, PDFs, datasets, …) ----

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_attachments",
		Title:       "List attachments",
		Description: "List the files attached to a page (uploads AND rclone-synced files): name, mime, byte size, a stable serve URL, an absolute download_url, and a ready-to-embed `markdown` snippet. `embedded` tells you the page body already references the file.",
		Annotations: readOnly,
	}, s.mcpListAttachments)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "upload_attachment",
		Title:       "Upload attachment",
		Description: "Upload a file (base64) and attach it to a page (editor+) — an image, PDF, dataset, etc. Returns the serve URL plus a ready-to-paste `markdown` snippet; then call update_page or patch_page to place it in the body (images render inline as ![](…), other files as a download card). The payload is inline base64 and rides through the model's context, so it is capped at 5 MB — keep it to small files (screenshots, charts, short PDFs). For larger files use request_attachment_upload (a direct PUT URL, bytes off-context), or the tela editor (drag-drop).",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: &no, DestructiveHint: &no, OpenWorldHint: &no},
	}, s.mcpUploadAttachment)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "treat_deck_image",
		Title:       "Treat deck image",
		Description: "Make an image tahta-grade for a deck's variant (editor+): crop to 16:9, apply a scheme-aware duotone (palette-lock), grain, and an optional contrast scrim. Upload the source with upload_attachment first, then pass its attachment_id; the treated JPEG is saved as a new attachment and returned with a ready-to-place ![](…) snippet for a bg:/image: slot. This is the tahta-imagine treat step — a FALLBACK for off-palette or reused images; prefer rich on-palette images raw, and never duotone (mode=duotone) a real-colour focal subject — use mode=none for those. See the imagery capability module (deck_authoring_guide module=\"imagery\").",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: &no, DestructiveHint: &no, OpenWorldHint: &no},
	}, s.mcpTreatDeckImage)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "generate_deck_image",
		Title:       "Generate deck image",
		Description: "Generate an image from a prompt and attach it to a deck page (editor+), ready for a bg:/image: slot. Returns the serve URL + a ![](…) snippet; reference it by path (don't regenerate on re-render). Read the imagery module first (deck_authoring_guide module=\"imagery\"): most slides need NO image — use it for atmosphere/concept/focal only, reuse ONE background, write rich on-palette prompts, and prefer images raw. May be unavailable (503) if the instance hasn't configured image generation or AI is paused; generation can take from ~20s to a few minutes depending on the model.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: &no, DestructiveHint: &no, OpenWorldHint: &no},
	}, s.mcpGenerateDeckImage)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_attachment",
		Title:       "Delete attachment",
		Description: "Detach a file from a page by attachment id (editor+; ids come from list_attachments). Soft-delete. It does NOT edit the page body, so remove any inline embed separately with update_page/patch_page.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: &no, DestructiveHint: &yes, OpenWorldHint: &no},
	}, s.mcpDeleteAttachment)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "request_attachment_upload",
		Title:       "Request a direct upload URL",
		Description: "Get a short-lived signed PUT URL to upload a file WITHOUT sending its bytes through the model context — for files over upload_attachment's 5 MB inline cap, or to avoid context bloat. Flow: call this → the host PUTs the raw bytes to the returned `put_url` over HTTP → then either read that PUT response or call confirm_attachment_upload to get the embed snippet, and place it with update_page/patch_page. Editor+. Only works on hosts that can make an outbound HTTP PUT; otherwise use upload_attachment.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: &no, DestructiveHint: &no, OpenWorldHint: &no},
	}, s.mcpRequestAttachmentUpload)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "confirm_attachment_upload",
		Title:       "Confirm a direct upload",
		Description: "After the bytes have been PUT to a request_attachment_upload URL, return the stored file's serve URL + ready-to-embed `markdown` (for hosts that couldn't read the PUT response). Editor+. Then place the snippet with update_page/patch_page.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: &no, IdempotentHint: true, DestructiveHint: &no, OpenWorldHint: &no},
	}, s.mcpConfirmAttachmentUpload)

	// ---- atlas (source-grounded, coverage-audited doc generation) ----
	// atlas turns a project's sources (git repos / Jira projects) into generated,
	// coverage-audited docs published into the project's output space. Listing
	// projects + reading a run's coverage is view-gated (owner + org members);
	// triggering a run is management-gated (a run spends LLM budget and rewrites
	// the generated subtree — never read-only).

	mcp.AddTool(server, &mcp.Tool{
		Name:        "atlas_list_projects",
		Title:       "List atlas projects",
		Description: "List the atlas doc-generation projects you can see (your personal ones plus those of every org you belong to). Each project carries its owner, output space, schedule, source count, and latest-run summary (status + last must-cover rate); `can_manage` says whether you may trigger runs. Start here to find a project_id for atlas_run, or to read coverage health at a glance.",
		Annotations: readOnly,
	}, s.mcpAtlasListProjects)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "atlas_run",
		Title:       "Run atlas generation",
		Description: "Trigger a FULL doc-generation run for every source in an atlas project (project_id from atlas_list_projects): re-ingest the sources, regenerate the cited pages, and re-audit coverage. Management-level (project owner / org admin) — a run fetches the sources, spends LLM budget, and rewrites the generated subtree (creating the output space on the first run). Returns run_ids (one per source); poll each with atlas_run_status. Returns 503 ai_unavailable when the instance has no embedder/LLM configured.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: &no, DestructiveHint: &no, OpenWorldHint: &no},
	}, s.mcpAtlasRun)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "atlas_run_status",
		Title:       "Get atlas run status",
		Description: "Read an atlas run's status and coverage by run_id (view+). Returns the run's status + current stage and, once auditing has run, coverage (must_rate = the headline fraction of must-cover surface documented, surface_rate, gap_count + the gap list of undocumented surface items) and stats (files / surface / chunks / pages). Use to follow a run started by atlas_run to completion and judge whether the docs are complete.",
		Annotations: readOnly,
	}, s.mcpAtlasRunStatus)
}

// ---- shared output shapes ------------------------------------------------

// mcpPage is a page row plus the human-shareable in-app URL. Embeds models.Page
// so the body + metadata fields are promoted verbatim.
type mcpPage struct {
	models.Page
	URL       string `json:"url"`
	Truncated bool   `json:"truncated,omitempty"`
	// Epistemic — trust signals (freshness, provenance, corroboration/dispute) so
	// an agent weighs the page, not just reads it. Set on get_page; nil elsewhere.
	Epistemic *EpistemicStatus `json:"epistemic,omitempty"`
	// Sections — the heading outline, returned (and Body emptied) when get_page is
	// called with format:"map". Each section's path is a patch_page target.
	Sections []pageSection `json:"sections,omitempty"`
}

// mcpBodyCap bounds a tool-result body so a single huge page can't blow the
// host's tool-result token budget (Claude Code truncates at ~25k tokens). At
// ~4 chars/token, 80k chars ≈ 20k tokens, leaving headroom for the envelope.
// On truncation it appends a pointer to the paging tools and returns ok=false.
const mcpBodyCap = 80_000

func mcpCapBody(body string) (string, bool) {
	if len(body) <= mcpBodyCap {
		return body, true
	}
	// Trim to the cap on a rune boundary, then add a machine- and human-readable
	// marker so the agent knows to page the rest via research/read_chunk.
	cut := mcpBodyCap
	for cut > 0 && !utf8.RuneStart(body[cut]) {
		cut--
	}
	return body[:cut] + "\n\n…[truncated: page exceeds the tool-result size cap. Use research + read_chunk to read specific sections, or open the page URL.]", false
}

// mcpOrigin is the base origin links into spaceID should use: the owning org's
// active custom domain when it has one (e.g. https://ngss.cagdas.io), else the
// canonical base URL. Mirrors shareOrigin so MCP page links match share links.
func (s *Server) mcpOrigin(ctx context.Context, spaceID int64) string {
	if host, ok := s.spaceOrgPrimaryHost(ctx, spaceID); ok {
		return "https://" + host
	}
	return canonicalBaseURL()
}

func (s *Server) mcpPageURL(ctx context.Context, p models.Page) string {
	return s.mcpOrigin(ctx, p.SpaceID) + pageAppPath(p.SpaceID, p.ID, p.Title)
}

// ---- list_spaces ---------------------------------------------------------

type listSpacesIn struct{}

type mcpSpace struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Visibility  string `json:"visibility"`
	OwnerHandle string `json:"owner_handle,omitempty"` // personal owner's username ("" when org-owned)
	OwnerOrg    string `json:"owner_org,omitempty"`    // owning org's name when org-owned
	MemberCount int    `json:"member_count"`
}

type listSpacesOut struct {
	Spaces []mcpSpace `json:"spaces"`
}

func (s *Server) mcpListSpaces(ctx context.Context, req *mcp.CallToolRequest, _ listSpacesIn) (*mcp.CallToolResult, listSpacesOut, error) {
	u, _ := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), listSpacesOut{}, nil
	}
	spaces, ae := s.listSpacesCore(ctx, u)
	if ae != nil {
		return mcpErr(ae), listSpacesOut{}, nil
	}
	owners := s.spaceOwnerHandles(ctx, u.ID)
	out := listSpacesOut{Spaces: make([]mcpSpace, len(spaces))}
	for i, sp := range spaces {
		// Match the UI's ownership logic (spaceOwnership): org_id owner, else the
		// sole org grant on a non-personal space. Only a personal/"you" space has a
		// personal owner_handle — never report a member as owner of an org space.
		orgIDName := ""
		if sp.OwnerOrg != nil {
			orgIDName = sp.OwnerOrg.Name
		}
		var orgGrants []string
		for _, p := range sp.Principals {
			if p.Kind == "org" {
				orgGrants = append(orgGrants, p.Name)
			}
		}
		ownerOrg := resolveOwnerOrg(orgIDName, sp.IsPersonal, orgGrants)
		m := mcpSpace{
			ID: sp.ID, Name: sp.Name, Slug: sp.Slug,
			Visibility: sp.Visibility, OwnerOrg: ownerOrg, MemberCount: sp.MemberCount,
		}
		if ownerOrg == "" {
			m.OwnerHandle = owners[sp.ID]
		}
		out.Spaces[i] = m
	}
	return nil, out, nil
}

// ---- get_space -----------------------------------------------------------

type getSpaceIn struct {
	ID int64 `json:"id" jsonschema:"space id"`
}

// getSpaceOut enriches the space with ownership + size so an agent knows who owns
// it and how big it is, not just the bare record.
type getSpaceOut struct {
	Space       models.Space `json:"space"`
	OwnerHandle string       `json:"owner_handle,omitempty"` // personal owner's username ("" when org-owned)
	OwnerOrg    string       `json:"owner_org,omitempty"`    // owning org's name when org-owned
	MemberCount int          `json:"member_count"`
	PageCount   int          `json:"page_count"`
}

func (s *Server) mcpGetSpace(ctx context.Context, req *mcp.CallToolRequest, in getSpaceIn) (*mcp.CallToolResult, getSpaceOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), getSpaceOut{}, nil
	}
	sp, ae := s.getSpaceCore(ctx, u, k, in.ID)
	if ae != nil {
		return mcpErr(ae), getSpaceOut{}, nil
	}
	// Match the UI's ownership logic (spaceOwnership): org_id owner, else the sole
	// org grant on a non-personal space; only otherwise is there a personal owner.
	ownerOrg := resolveOwnerOrg(s.spaceOwnerOrgName(ctx, in.ID), s.spaceIsPersonal(ctx, in.ID), s.spaceOrgGrantNames(ctx, in.ID))
	out := getSpaceOut{Space: sp, OwnerOrg: ownerOrg}
	if ownerOrg == "" {
		out.OwnerHandle = s.spaceOwnerHandle(ctx, in.ID)
	}
	_ = s.DB.QueryRowContext(ctx,
		`SELECT (SELECT COUNT(DISTINCT user_id) FROM space_access WHERE space_id = $1),
		        (SELECT COUNT(*) FROM pages WHERE space_id = $1 AND deleted_at IS NULL)`, in.ID,
	).Scan(&out.MemberCount, &out.PageCount)
	return nil, out, nil
}

// ---- list_pages ----------------------------------------------------------

type listPagesIn struct {
	SpaceID  int64  `json:"space_id" jsonschema:"id of the space to list pages in"`
	ParentID *int64 `json:"parent_id,omitempty" jsonschema:"optional parent page id; omit for top-level pages"`
}

type mcpPageListItem struct {
	ID       int64  `json:"id"`
	SpaceID  int64  `json:"space_id"`
	ParentID *int64 `json:"parent_id"`
	Title    string `json:"title"`
	Position int64  `json:"position"`
	URL      string `json:"url"`
}

type listPagesOut struct {
	Pages []mcpPageListItem `json:"pages"`
}

func (s *Server) mcpListPages(ctx context.Context, req *mcp.CallToolRequest, in listPagesIn) (*mcp.CallToolResult, listPagesOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), listPagesOut{}, nil
	}
	pages, ae := s.listPagesCore(ctx, u, k, in.SpaceID, in.ParentID)
	if ae != nil {
		return mcpErr(ae), listPagesOut{}, nil
	}
	// All pages share in.SpaceID — resolve the origin once (org domain or canonical).
	origin := s.mcpOrigin(ctx, in.SpaceID)
	out := listPagesOut{Pages: make([]mcpPageListItem, len(pages))}
	for i, p := range pages {
		out.Pages[i] = mcpPageListItem{
			ID:       p.ID,
			SpaceID:  p.SpaceID,
			ParentID: p.ParentID,
			Title:    p.Title,
			Position: p.Position,
			URL:      origin + pageAppPath(p.SpaceID, p.ID, p.Title),
		}
	}
	return nil, out, nil
}

// ---- get_page ------------------------------------------------------------

type getPageIn struct {
	ID     int64  `json:"id" jsonschema:"numeric page id"`
	Format string `json:"format,omitempty" jsonschema:"'full' (default) returns the markdown body; 'map' returns just the heading outline (section levels + paths) and no body — cheap to read, and each path is a target for patch_page; 'values' — for a SHEET page (sheet=true), returns the computed spreadsheet as self-describing prose (formulas materialized to their numbers, styling stripped) so you read the answers, not the raw =formulas (no effect on non-sheet pages)"`
}

type getPageOut struct {
	Page mcpPage `json:"page"`
}

// createdPage is the create_page result: the new page's identity + metadata,
// deliberately WITHOUT the body. The caller just sent that body, so echoing it
// only doubles the payload; the id/url/parent_id is what a caller actually needs
// to keep building. Fetch the stored (normalized) body via get_page if required.
type createdPage struct {
	ID        int64          `json:"id"`
	SpaceID   int64          `json:"space_id"`
	ParentID  *int64         `json:"parent_id"`
	Title     string         `json:"title"`
	Position  int64          `json:"position"`
	Props     map[string]any `json:"props,omitempty"`
	CreatedAt string         `json:"created_at"`
	UpdatedAt string         `json:"updated_at"`
	URL       string         `json:"url"`
}

type createPageOut struct {
	Page createdPage `json:"page"`
	Lint string      `json:"lint,omitempty"`
}

// writePageOut is update_page's result: get_page's envelope plus the same
// advisory. update_page can't just return getPageOut any more, because the
// lint has to ride along on the WRITE and not on every read.
type writePageOut struct {
	Page mcpPage `json:"page"`
	Lint string  `json:"lint,omitempty"`
}

func (s *Server) newCreatedPage(ctx context.Context, p models.Page) createdPage {
	return createdPage{
		ID:        p.ID,
		SpaceID:   p.SpaceID,
		ParentID:  p.ParentID,
		Title:     p.Title,
		Position:  p.Position,
		Props:     p.Props,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
		URL:       s.mcpPageURL(ctx, p),
	}
}

func (s *Server) mcpGetPage(ctx context.Context, req *mcp.CallToolRequest, in getPageIn) (*mcp.CallToolResult, getPageOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), getPageOut{}, nil
	}
	p, ae := s.getPageCore(ctx, u, k, in.ID)
	if ae != nil {
		return mcpErr(ae), getPageOut{}, nil
	}
	epi := s.pageEpistemic(ctx, p)
	if in.Format == "map" {
		sections := pageOutline(p.Body)
		p.Body = ""
		mp := mcpPage{Page: p, URL: s.mcpPageURL(ctx, p), Epistemic: epi, Sections: sections}
		return nil, getPageOut{Page: mp}, nil
	}
	// A sheet's raw body is Defter markdown (formulas, not answers). 'values' mode
	// returns the computed projection so an agent reads the numbers directly.
	if in.Format == "values" && isSheetBag(p.Props) {
		p.Body = sheetproj.Project(ctx, p.Body)
		out := getPageOut{Page: mcpPage{Page: p, URL: s.mcpPageURL(ctx, p), Epistemic: epi}}
		return nil, out, nil
	}
	body, whole := mcpCapBody(p.Body)
	p.Body = body
	out := getPageOut{Page: mcpPage{Page: p, URL: s.mcpPageURL(ctx, p), Truncated: !whole, Epistemic: epi}}
	return nil, out, nil
}

// ---- list_backlinks ------------------------------------------------------

type listBacklinksIn struct {
	PageID int64 `json:"page_id" jsonschema:"page whose incoming links to list"`
}

type listBacklinksOut struct {
	Backlinks []backlinkHit `json:"backlinks"`
}

func (s *Server) mcpListBacklinks(ctx context.Context, req *mcp.CallToolRequest, in listBacklinksIn) (*mcp.CallToolResult, listBacklinksOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), listBacklinksOut{}, nil
	}
	hits, ae := s.backlinksCore(ctx, u, k, in.PageID)
	if ae != nil {
		return mcpErr(ae), listBacklinksOut{}, nil
	}
	return nil, listBacklinksOut{Backlinks: hits}, nil
}

// ---- search --------------------------------------------------------------

type searchIn struct {
	Query   string `json:"query" jsonschema:"search terms"`
	SpaceID *int64 `json:"space_id,omitempty" jsonschema:"optional space id to restrict results to"`
	Limit   int    `json:"limit,omitempty" jsonschema:"max results (default 25)"`
}

type searchOut struct {
	Results []searchHit `json:"results"`
}

func (s *Server) mcpSearch(ctx context.Context, req *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, searchOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), searchOut{}, nil
	}
	results, ae := s.searchCore(ctx, u, k, in.Query, in.SpaceID, in.Limit)
	if ae != nil {
		return mcpErr(ae), searchOut{}, nil
	}
	out := searchOut{Results: results}
	return nil, out, nil
}

// ---- research ------------------------------------------------------------
//
// The semantic, answer-oriented retrieval tool. It rides the SAME askContext
// seam as the web "Ask your docs" path — deep retrieval, dedup-to-sources,
// topical-hub rescue, and parent-document expansion (whole page bodies, not
// fragments) — but stops BEFORE generation: the calling agent is the LLM, so it
// returns the assembled grounding for the agent to answer from, not a finished
// answer. This is what `semantic_search` should have been for an agent host:
// its `sources` ARE the ranked chunk hits (with chunk_id for drill-in), plus the
// assembled context, the known-disagreement note, and a confidence signal.

type researchIn struct {
	Question string `json:"question" jsonschema:"the question to answer, or topic to gather context on"`
	SpaceID  *int64 `json:"space_id,omitempty" jsonschema:"optional space id to restrict retrieval to"`
	Limit    int    `json:"limit,omitempty" jsonschema:"optional retrieval depth override (default service-defined)"`
}

type researchOut struct {
	Context       string    `json:"context"`                 // numbered [n] excerpt block, expanded to full page bodies where they matter
	Sources       []rag.Hit `json:"sources"`                 // cited sources aligned to the [n] numbering; chunk_id/page_id for drill-in, download_url on file sources
	Disagreements string    `json:"disagreements,omitempty"` // known conflicts among the sources, [n]-keyed (empty when none)
	LowConfidence bool      `json:"low_confidence"`          // retrieval found nothing strongly relevant — answer is best-effort, verify it
}

func (s *Server) mcpResearch(ctx context.Context, req *mcp.CallToolRequest, in researchIn) (*mcp.CallToolResult, researchOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), researchOut{}, nil
	}
	if !s.aiEnabled() {
		return mcpErr(&apiErr{503, "rag_disabled", "semantic search is not configured"}), researchOut{}, nil
	}
	if strings.TrimSpace(in.Question) == "" {
		return mcpErr(&apiErr{400, "bad_request", "question is required"}), researchOut{}, nil
	}
	if ae := s.embedRateErr(account{Kind: accountUser, ID: u.ID}); ae != nil {
		return mcpErr(ae), researchOut{}, nil
	}
	// A space-pinned bearer key may only ever see its one space.
	spaceID := in.SpaceID
	if k != nil && k.SpaceID != nil {
		spaceID = k.SpaceID
	}
	excerpts, hits, top, err := s.askContext(ctx, u.ID, in.Question, spaceID, in.Limit)
	if err != nil {
		return mcpErr(&apiErr{500, "internal", "retrieval failed"}), researchOut{}, nil
	}
	// Log every research call with its retrieval confidence — feeds the
	// knowledge-gaps roadmap, including the zero-hit case (a clear gap). Best-effort.
	_ = s.rag.LogAsk(ctx, u.ID, spaceID, in.Question, len(hits), top)
	if len(hits) == 0 {
		return nil, researchOut{Sources: []rag.Hit{}}, nil
	}
	enrichFileCitations(hits)
	out := researchOut{
		Context:       excerpts,
		Sources:       hits,
		Disagreements: s.askConflictNote(ctx, hits),
		LowConfidence: lowConfidence(s.rag.RerankEnabled(), top),
	}
	return nil, out, nil
}

// ---- read_chunk ----------------------------------------------------------

type readChunkIn struct {
	ChunkID int64 `json:"chunk_id" jsonschema:"chunk id from a research result"`
}

type readChunkOut struct {
	Chunk rag.ChunkRead `json:"chunk"`
}

func (s *Server) mcpReadChunk(ctx context.Context, req *mcp.CallToolRequest, in readChunkIn) (*mcp.CallToolResult, readChunkOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), readChunkOut{}, nil
	}
	if !s.aiEnabled() {
		return mcpErr(&apiErr{503, "rag_disabled", "semantic search is not configured"}), readChunkOut{}, nil
	}
	var spaceID *int64
	if k != nil && k.SpaceID != nil {
		spaceID = k.SpaceID
	}
	chunk, err := s.rag.ReadChunk(ctx, u.ID, in.ChunkID, spaceID)
	if err != nil {
		if errors.Is(err, rag.ErrChunkNotFound) {
			return mcpErr(&apiErr{404, "not_found", "chunk not found"}), readChunkOut{}, nil
		}
		return mcpErr(&apiErr{500, "internal", "read chunk failed"}), readChunkOut{}, nil
	}
	enrichFileChunk(chunk)
	return nil, readChunkOut{Chunk: *chunk}, nil
}

// ---- related_pages -------------------------------------------------------

type relatedPagesIn struct {
	PageID int64 `json:"page_id" jsonschema:"the page to find related pages for"`
	Limit  int   `json:"limit,omitempty" jsonschema:"max related pages (default 10)"`
}

type relatedPagesOut struct {
	Related []rag.RelatedPage `json:"related"`
}

func (s *Server) mcpRelatedPages(ctx context.Context, req *mcp.CallToolRequest, in relatedPagesIn) (*mcp.CallToolResult, relatedPagesOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), relatedPagesOut{}, nil
	}
	// Verify read access to the source page (also handles bearer scope).
	if _, ae := s.getPageCore(ctx, u, k, in.PageID); ae != nil {
		return mcpErr(ae), relatedPagesOut{}, nil
	}
	var spaceID *int64
	if k != nil && k.SpaceID != nil {
		spaceID = k.SpaceID
	}
	related, err := s.rag.RelatedPages(ctx, u.ID, in.PageID, spaceID, in.Limit)
	if err != nil {
		return mcpErr(&apiErr{500, "internal", "related lookup failed"}), relatedPagesOut{}, nil
	}
	return nil, relatedPagesOut{Related: related}, nil
}

// ---- suggest_links -------------------------------------------------------

type suggestLinksIn struct {
	Text    string `json:"text" jsonschema:"draft text to find link targets for"`
	SpaceID *int64 `json:"space_id,omitempty" jsonschema:"optional space id to restrict suggestions to"`
	Limit   int    `json:"limit,omitempty" jsonschema:"max suggestions (default 10)"`
}

type suggestLinksOut struct {
	Suggestions []rag.RelatedPage `json:"suggestions"`
}

func (s *Server) mcpSuggestLinks(ctx context.Context, req *mcp.CallToolRequest, in suggestLinksIn) (*mcp.CallToolResult, suggestLinksOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), suggestLinksOut{}, nil
	}
	if !s.aiEnabled() {
		return mcpErr(&apiErr{503, "rag_disabled", "semantic features are not configured"}), suggestLinksOut{}, nil
	}
	if ae := s.embedRateErr(account{Kind: accountUser, ID: u.ID}); ae != nil {
		return mcpErr(ae), suggestLinksOut{}, nil
	}
	spaceID := in.SpaceID
	if k != nil && k.SpaceID != nil {
		spaceID = k.SpaceID
	}
	out, err := s.rag.SuggestLinks(ctx, u.ID, in.Text, spaceID, in.Limit)
	if err != nil {
		return mcpErr(&apiErr{500, "internal", "suggest-links failed"}), suggestLinksOut{}, nil
	}
	return nil, suggestLinksOut{Suggestions: out}, nil
}

// ---- find_overlaps -------------------------------------------------------

type findOverlapsIn struct {
	SpaceID   *int64  `json:"space_id,omitempty" jsonschema:"optional space id to restrict to overlaps within one space"`
	Threshold float64 `json:"threshold,omitempty" jsonschema:"min chunk-level cosine similarity 0..1 to count as a duplicate (default 0.92)"`
	Limit     int     `json:"limit,omitempty" jsonschema:"max pairs (default 50)"`
}

type findOverlapsOut struct {
	Overlaps []rag.OverlapPair `json:"overlaps"`
}

func (s *Server) mcpFindOverlaps(ctx context.Context, req *mcp.CallToolRequest, in findOverlapsIn) (*mcp.CallToolResult, findOverlapsOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), findOverlapsOut{}, nil
	}
	spaceID := in.SpaceID
	if k != nil && k.SpaceID != nil {
		spaceID = k.SpaceID
	}
	pairs, err := s.rag.FindOverlaps(ctx, u.ID, spaceID, in.Threshold, in.Limit)
	if err != nil {
		return mcpErr(&apiErr{500, "internal", "overlap lookup failed"}), findOverlapsOut{}, nil
	}
	return nil, findOverlapsOut{Overlaps: pairs}, nil
}

// ---- knowledge_gaps (admin) ----------------------------------------------

type knowledgeGapsIn struct {
	SinceDays int `json:"since_days,omitempty" jsonschema:"only count asks in the last N days (0 = all time)"`
	Limit     int `json:"limit,omitempty" jsonschema:"max gaps (default 50)"`
}

type knowledgeGapsOut struct {
	Gaps []rag.KnowledgeGap `json:"gaps"`
}

func (s *Server) mcpKnowledgeGaps(ctx context.Context, req *mcp.CallToolRequest, in knowledgeGapsIn) (*mcp.CallToolResult, knowledgeGapsOut, error) {
	u, _ := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), knowledgeGapsOut{}, nil
	}
	if !u.IsInstanceAdmin {
		return mcpErr(&apiErr{403, "forbidden", "knowledge_gaps is instance-admin only"}), knowledgeGapsOut{}, nil
	}
	gaps, err := s.rag.KnowledgeGaps(ctx, in.SinceDays, in.Limit)
	if err != nil {
		return mcpErr(&apiErr{500, "internal", "gaps query failed"}), knowledgeGapsOut{}, nil
	}
	return nil, knowledgeGapsOut{Gaps: gaps}, nil
}

// ---- fetch (ChatGPT Deep Research) ---------------------------------------

type fetchIn struct {
	ID string `json:"id" jsonschema:"page id, as returned by search results"`
}

type fetchOut struct {
	ID       string         `json:"id"`
	Title    string         `json:"title"`
	Text     string         `json:"text"`
	URL      string         `json:"url"`
	Metadata map[string]any `json:"metadata"`
}

func (s *Server) mcpFetch(ctx context.Context, req *mcp.CallToolRequest, in fetchIn) (*mcp.CallToolResult, fetchOut, error) {
	// Error returns still carry a struct that the SDK marshals + validates against
	// the output schema; a nil Metadata map serializes to JSON null and fails the
	// `metadata` object type, masking the real error with a schema-validation error.
	// So every error path returns a non-nil (empty) metadata.
	errOut := fetchOut{Metadata: map[string]any{}}
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), errOut, nil
	}
	pid, err := strconv.ParseInt(strings.TrimSpace(in.ID), 10, 64)
	if err != nil || pid <= 0 {
		return mcpErr(&apiErr{400, "bad_request", "id must be a numeric page id"}), errOut, nil
	}
	p, ae := s.getPageCore(ctx, u, k, pid)
	if ae != nil {
		return mcpErr(ae), errOut, nil
	}
	text, whole := mcpCapBody(p.Body)
	return nil, fetchOut{
		ID:       in.ID,
		Title:    p.Title,
		Text:     text,
		URL:      s.mcpPageURL(ctx, p),
		Metadata: map[string]any{"space_id": p.SpaceID, "truncated": !whole},
	}, nil
}

// ---- create_page ---------------------------------------------------------

type createPageIn struct {
	SpaceID        int64          `json:"space_id" jsonschema:"id of the space to create the page in"`
	ParentID       *int64         `json:"parent_id,omitempty" jsonschema:"optional parent page id"`
	Title          string         `json:"title" jsonschema:"page title"`
	Body           string         `json:"body" jsonschema:"markdown body"`
	Props          map[string]any `json:"props,omitempty" jsonschema:"optional page properties (frontmatter); free-form keys, reserved keys like id/title/slug/created are ignored"`
	IdempotencyKey string         `json:"idempotency_key,omitempty" jsonschema:"optional client-generated key; a retry with the same key returns the original result instead of creating a duplicate page (safe retries after a dropped connection)"`
}

func (s *Server) mcpCreatePage(ctx context.Context, req *mcp.CallToolRequest, in createPageIn) (*mcp.CallToolResult, createPageOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), createPageOut{}, nil
	}
	if ae := mcpRequireWrite(k); ae != nil {
		return mcpErr(ae), createPageOut{}, nil
	}
	return mcpIdempotent(ctx, s.DB, u.ID, in.IdempotencyKey, "create_page", func() (*mcp.CallToolResult, createPageOut, error) {
		p, ae := s.createPageCore(withAgentWrite(ctx), u, k, pageCreateRequest{
			SpaceID:  in.SpaceID,
			ParentID: in.ParentID,
			Title:    in.Title,
			Body:     in.Body,
			Props:    in.Props,
		}, true)
		if ae != nil {
			return mcpErr(ae), createPageOut{}, nil
		}
		return nil, createPageOut{Page: s.newCreatedPage(ctx, p), Lint: s.writeAdvisory(ctx, p)}, nil
	})
}

// ---- update_page ---------------------------------------------------------

type updatePageIn struct {
	ID    int64          `json:"id" jsonschema:"page id to patch"`
	Title *string        `json:"title,omitempty" jsonschema:"new title (omit to leave unchanged)"`
	Body  *string        `json:"body,omitempty" jsonschema:"new markdown body (omit to leave unchanged)"`
	Props map[string]any `json:"props,omitempty" jsonschema:"replace the whole properties bag (omit to leave unchanged); reserved keys are ignored"`
}

func (s *Server) mcpUpdatePage(ctx context.Context, req *mcp.CallToolRequest, in updatePageIn) (*mcp.CallToolResult, writePageOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), writePageOut{}, nil
	}
	if ae := mcpRequireWrite(k); ae != nil {
		return mcpErr(ae), writePageOut{}, nil
	}
	// agentWrite=true: an agent rewriting the body must invalidate the Yjs collab
	// overlay so live/next editors see it instead of stale CRDT state.
	p, ae := s.updatePageCore(ctx, u, k, in.ID, pageUpdateRequest{Title: in.Title, Body: in.Body, Props: in.Props}, true)
	if ae != nil {
		return mcpErr(ae), writePageOut{}, nil
	}
	out := writePageOut{Page: mcpPage{Page: p, URL: s.mcpPageURL(ctx, p)}, Lint: s.writeAdvisory(ctx, p)}
	return nil, out, nil
}

// ---- patch_page (surgical section edit) ----------------------------------

type patchPageIn struct {
	ID             int64  `json:"id" jsonschema:"page id to patch"`
	Target         string `json:"target" jsonschema:"the section to edit, by its heading path from get_page format:\"map\" (e.g. 'Setup' or 'Deploy > Production'); the bare heading text also resolves"`
	Operation      string `json:"operation" jsonschema:"append (add to the end of the section's body), prepend (add right under the heading), replace (swap the section's body, heading kept), or delete (remove the heading and its body). A section means the heading AND EVERYTHING NESTED UNDER IT until the next same-or-higher heading — so replace/delete on a '##' also removes its '###' sub-sections (they come back in the response as removed_subsections), and append lands after the last of them. To edit only the prose under a heading that has sub-sections, target the sub-section instead."`
	Content        string `json:"content,omitempty" jsonschema:"markdown to insert; omit for delete"`
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"optional client-generated key; a retry with the same key returns the original result instead of re-applying the patch"`
}

// patchPageOut is get_page's envelope plus the accounting for what the patch
// took with it: a section spans its subtree, so replace/delete can drop nested
// sub-sections the caller never named. Reporting them is what lets an agent (or
// the human reading its transcript) notice the loss while it's still cheap to
// undo — silence was the actual bug, more than the span semantics.
type patchPageOut struct {
	Page               mcpPage  `json:"page"`
	RemovedSubsections []string `json:"removed_subsections,omitempty"`
	Warning            string   `json:"warning,omitempty"`
}

func (s *Server) mcpPatchPage(ctx context.Context, req *mcp.CallToolRequest, in patchPageIn) (*mcp.CallToolResult, patchPageOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), patchPageOut{}, nil
	}
	if ae := mcpRequireWrite(k); ae != nil {
		return mcpErr(ae), patchPageOut{}, nil
	}
	return mcpIdempotent(ctx, s.DB, u.ID, in.IdempotencyKey, "patch_page", func() (*mcp.CallToolResult, patchPageOut, error) {
		p, ae := s.getPageCore(ctx, u, k, in.ID)
		if ae != nil {
			return mcpErr(ae), patchPageOut{}, nil
		}
		patch, err := applyPatch(p.Body, in.Target, in.Operation, in.Content)
		if err != nil {
			return mcpErr(&apiErr{Status: 400, Code: "bad_request", Message: err.Error()}), patchPageOut{}, nil
		}
		// Write through the normal update path (agentWrite=true) so the revision,
		// reindex, agreement and provenance all fire exactly as for any edit.
		up, ae := s.updatePageCore(ctx, u, k, in.ID, pageUpdateRequest{Body: &patch.Body}, true)
		if ae != nil {
			return mcpErr(ae), patchPageOut{}, nil
		}
		epi := s.pageEpistemic(ctx, up)
		body, whole := mcpCapBody(up.Body)
		up.Body = body
		out := patchPageOut{
			Page:               mcpPage{Page: up, URL: s.mcpPageURL(ctx, up), Truncated: !whole, Epistemic: epi},
			RemovedSubsections: patch.Removed,
		}
		if len(patch.Removed) > 0 {
			out.Warning = fmt.Sprintf("%s on %q also removed %d nested sub-section(s): %s — restore from revisions if that wasn't intended.",
				in.Operation, patch.Section.Path, len(patch.Removed), strings.Join(patch.Removed, ", "))
		}
		return nil, out, nil
	})
}

// ---- delete_page ---------------------------------------------------------

type deletePageIn struct {
	ID int64 `json:"id" jsonschema:"page id to delete"`
}

type okOut struct {
	OK bool `json:"ok"`
}

func (s *Server) mcpDeletePage(ctx context.Context, req *mcp.CallToolRequest, in deletePageIn) (*mcp.CallToolResult, okOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), okOut{}, nil
	}
	if ae := mcpRequireWrite(k); ae != nil {
		return mcpErr(ae), okOut{}, nil
	}
	if ae := s.deletePageCore(ctx, u, k, in.ID); ae != nil {
		return mcpErr(ae), okOut{}, nil
	}
	return nil, okOut{OK: true}, nil
}

// ---- move_page -----------------------------------------------------------

type movePageIn struct {
	ID       int64  `json:"id" jsonschema:"page to move"`
	SpaceID  *int64 `json:"space_id,omitempty" jsonschema:"relocate to this space (omit to keep)"`
	ParentID *int64 `json:"parent_id,omitempty" jsonschema:"new parent page id (omit to keep; mutually exclusive with make_root)"`
	MakeRoot bool   `json:"make_root,omitempty" jsonschema:"detach to top-level (mutually exclusive with parent_id)"`
	Position *int64 `json:"position,omitempty" jsonschema:"new 0-based position among siblings (omit to keep)"`
}

func (s *Server) mcpMovePage(ctx context.Context, req *mcp.CallToolRequest, in movePageIn) (*mcp.CallToolResult, getPageOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), getPageOut{}, nil
	}
	if ae := mcpRequireWrite(k); ae != nil {
		return mcpErr(ae), getPageOut{}, nil
	}
	if in.MakeRoot && in.ParentID != nil {
		return mcpErr(&apiErr{400, "bad_request", "parent_id and make_root are mutually exclusive"}), getPageOut{}, nil
	}
	var mv pageMoveParams
	if in.SpaceID != nil {
		mv.SpaceIDSet = true
		mv.NewSpaceID = *in.SpaceID
	}
	switch {
	case in.MakeRoot:
		mv.ParentIDSet = true
		mv.ParentIDIsNull = true
	case in.ParentID != nil:
		mv.ParentIDSet = true
		mv.NewParentID = *in.ParentID
	}
	if in.Position != nil {
		mv.PositionSet = true
		mv.NewPosition = *in.Position
	}
	p, ae := s.movePageCore(ctx, u, k, in.ID, mv)
	if ae != nil {
		return mcpErr(ae), getPageOut{}, nil
	}
	out := getPageOut{Page: mcpPage{Page: p, URL: s.mcpPageURL(ctx, p)}}
	return nil, out, nil
}

// ---- share_page / list_shares / revoke_share -----------------------------

type sharePageIn struct {
	PageID             int64   `json:"page_id" jsonschema:"page to mint a public link for"`
	IncludeDescendants bool    `json:"include_descendants,omitempty" jsonschema:"share the whole subtree, not just this page"`
	Password           *string `json:"password,omitempty" jsonschema:"gate the link behind a passphrase (omit for open access)"`
	ExpiresAt          *string `json:"expires_at,omitempty" jsonschema:"auto-expire at this UTC 'YYYY-MM-DD HH:MM:SS' (omit for no expiry)"`
}

type sharePageOut struct {
	Share shareLinkDTO `json:"share"`
}

func (s *Server) mcpSharePage(ctx context.Context, req *mcp.CallToolRequest, in sharePageIn) (*mcp.CallToolResult, sharePageOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), sharePageOut{}, nil
	}
	if ae := mcpRequireWrite(k); ae != nil {
		return mcpErr(ae), sharePageOut{}, nil
	}
	dto, ae := s.createShareLinkCore(ctx, u, k, in.PageID, shareCreateRequest{
		IncludeDescendants: in.IncludeDescendants,
		Password:           in.Password,
		ExpiresAt:          in.ExpiresAt,
	})
	if ae != nil {
		return mcpErr(ae), sharePageOut{}, nil
	}
	return nil, sharePageOut{Share: dto}, nil
}

type listSharesIn struct {
	PageID         int64 `json:"page_id" jsonschema:"page whose share links to list"`
	IncludeRevoked bool  `json:"include_revoked,omitempty" jsonschema:"also include revoked links (default: active only)"`
}

type listSharesOut struct {
	Shares []shareLinkDTO `json:"shares"`
}

// mcpListShares gates on write scope even though it mutates nothing: the
// returned tokens are bearer secrets that grant the same public reach as
// share_page, so a read-only key has no business reading them.
func (s *Server) mcpListShares(ctx context.Context, req *mcp.CallToolRequest, in listSharesIn) (*mcp.CallToolResult, listSharesOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), listSharesOut{}, nil
	}
	if ae := mcpRequireWrite(k); ae != nil {
		return mcpErr(ae), listSharesOut{}, nil
	}
	out, ae := s.listShareLinksCore(ctx, u, k, in.PageID, in.IncludeRevoked)
	if ae != nil {
		return mcpErr(ae), listSharesOut{}, nil
	}
	return nil, listSharesOut{Shares: out}, nil
}

type revokeShareIn struct {
	ShareID int64 `json:"share_id" jsonschema:"id of the share link to revoke (from list_shares)"`
}

func (s *Server) mcpRevokeShare(ctx context.Context, req *mcp.CallToolRequest, in revokeShareIn) (*mcp.CallToolResult, okOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), okOut{}, nil
	}
	if ae := mcpRequireWrite(k); ae != nil {
		return mcpErr(ae), okOut{}, nil
	}
	if ae := s.revokeShareLinkCore(ctx, u, k, in.ShareID); ae != nil {
		return mcpErr(ae), okOut{}, nil
	}
	return nil, okOut{OK: true}, nil
}

// ---- add_comment ---------------------------------------------------------

type addCommentAnchor struct {
	Prefix string `json:"prefix" jsonschema:"text immediately before the anchored span"`
	Exact  string `json:"exact" jsonschema:"the exact anchored span"`
	Suffix string `json:"suffix" jsonschema:"text immediately after the anchored span"`
}

type addCommentIn struct {
	PageID         int64            `json:"page_id" jsonschema:"page to comment on"`
	Anchor         addCommentAnchor `json:"anchor" jsonschema:"text-quote anchor locating the comment in the body"`
	Body           string           `json:"body" jsonschema:"comment text (1-10000 chars)"`
	IdempotencyKey string           `json:"idempotency_key,omitempty" jsonschema:"optional client-generated key; a retry with the same key returns the original result instead of posting a duplicate comment (safe retries after a dropped connection)"`
}

type addCommentOut struct {
	Comment models.Comment `json:"comment"`
}

func (s *Server) mcpAddComment(ctx context.Context, req *mcp.CallToolRequest, in addCommentIn) (*mcp.CallToolResult, addCommentOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), addCommentOut{}, nil
	}
	if ae := mcpRequireWrite(k); ae != nil {
		return mcpErr(ae), addCommentOut{}, nil
	}
	return mcpIdempotent(ctx, s.DB, u.ID, in.IdempotencyKey, "add_comment", func() (*mcp.CallToolResult, addCommentOut, error) {
		c, ae := s.createCommentCore(ctx, u, k, in.PageID, commentCreateRequest{
			Body:         in.Body,
			AnchorPrefix: &in.Anchor.Prefix,
			AnchorExact:  &in.Anchor.Exact,
			AnchorSuffix: &in.Anchor.Suffix,
		})
		if ae != nil {
			return mcpErr(ae), addCommentOut{}, nil
		}
		return nil, addCommentOut{Comment: c}, nil
	})
}

// ---- create_space / update_space / delete_space --------------------------

type createSpaceIn struct {
	Name           string `json:"name" jsonschema:"space name (1-200 chars)"`
	Slug           string `json:"slug,omitempty" jsonschema:"optional url slug; derived from name when omitted"`
	OrgID          *int64 `json:"org_id,omitempty" jsonschema:"optional org id to own the space (caller must be a member); omit for a personal space"`
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"optional client-generated key; a retry with the same key returns the original result instead of creating a duplicate space (safe retries after a dropped connection)"`
}

type spaceOut struct {
	Space models.Space `json:"space"`
}

func (s *Server) mcpCreateSpace(ctx context.Context, req *mcp.CallToolRequest, in createSpaceIn) (*mcp.CallToolResult, spaceOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), spaceOut{}, nil
	}
	if ae := mcpRequireWrite(k); ae != nil {
		return mcpErr(ae), spaceOut{}, nil
	}
	return mcpIdempotent(ctx, s.DB, u.ID, in.IdempotencyKey, "create_space", func() (*mcp.CallToolResult, spaceOut, error) {
		sp, ae := s.createSpaceCore(ctx, u, in.Name, in.Slug, in.OrgID)
		if ae != nil {
			return mcpErr(ae), spaceOut{}, nil
		}
		return nil, spaceOut{Space: sp}, nil
	})
}

type updateSpaceIn struct {
	ID   int64   `json:"id" jsonschema:"space id to patch"`
	Name *string `json:"name,omitempty" jsonschema:"new name (omit to leave unchanged)"`
	Slug *string `json:"slug,omitempty" jsonschema:"new slug (omit to leave unchanged)"`
}

func (s *Server) mcpUpdateSpace(ctx context.Context, req *mcp.CallToolRequest, in updateSpaceIn) (*mcp.CallToolResult, spaceOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), spaceOut{}, nil
	}
	if ae := mcpRequireWrite(k); ae != nil {
		return mcpErr(ae), spaceOut{}, nil
	}
	sp, ae := s.updateSpaceCore(ctx, u, k, in.ID, spaceUpdateRequest{Name: in.Name, Slug: in.Slug})
	if ae != nil {
		return mcpErr(ae), spaceOut{}, nil
	}
	return nil, spaceOut{Space: sp}, nil
}

type deleteSpaceIn struct {
	ID int64 `json:"id" jsonschema:"space id to delete (cascades)"`
}

func (s *Server) mcpDeleteSpace(ctx context.Context, req *mcp.CallToolRequest, in deleteSpaceIn) (*mcp.CallToolResult, okOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), okOut{}, nil
	}
	if ae := mcpRequireWrite(k); ae != nil {
		return mcpErr(ae), okOut{}, nil
	}
	if ae := s.deleteSpaceCore(ctx, u, k, in.ID); ae != nil {
		return mcpErr(ae), okOut{}, nil
	}
	return nil, okOut{OK: true}, nil
}

// ---- invite_to_space -----------------------------------------------------

type inviteToSpaceIn struct {
	SpaceID int64  `json:"space_id" jsonschema:"space to share (caller must be its owner)"`
	Email   string `json:"email" jsonschema:"the person's email address"`
	Role    string `json:"role,omitempty" jsonschema:"owner | editor | viewer (default viewer)"`
}

// inviteToSpaceOut carries exactly one of member (access granted immediately)
// or invite (an invitation email was sent), so the calling agent can tell the
// two branches apart without guessing.
type inviteToSpaceOut struct {
	Member *spaceMemberDTO `json:"member,omitempty"`
	Invite *spaceInviteDTO `json:"invite,omitempty"`
}

func (s *Server) mcpInviteToSpace(ctx context.Context, req *mcp.CallToolRequest, in inviteToSpaceIn) (*mcp.CallToolResult, inviteToSpaceOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), inviteToSpaceOut{}, nil
	}
	if ae := mcpRequireWrite(k); ae != nil {
		return mcpErr(ae), inviteToSpaceOut{}, nil
	}
	// Same core as POST /api/spaces/{id}/invites — owner gate, both branches, the
	// mail and the audit rows are identical. The invite link is space-derived
	// (the owning org's custom domain when it has one) since there is no request
	// host to follow here.
	origin := cmp.Or(s.shareOrigin(ctx, in.SpaceID), devBaseURL)
	res, ae := s.shareSpaceByEmailCore(ctx, u, k, in.SpaceID, in.Email, in.Role, origin)
	if ae != nil {
		return mcpErr(ae), inviteToSpaceOut{}, nil
	}
	return nil, inviteToSpaceOut{Member: res.Member, Invite: res.Invite}, nil
}

// ---- submit_feedback (any scope, no mcpRequireWrite) ---------------------

type submitFeedbackIn struct {
	Subject string `json:"subject" jsonschema:"short subject (1-200 chars)"`
	Body    string `json:"body" jsonschema:"feedback body (1-8000 chars)"`
	Kind    string `json:"kind,omitempty" jsonschema:"optional type: idea | bug | other"`
}

type submitFeedbackOut struct {
	Feedback feedbackDTO `json:"feedback"`
}

func (s *Server) mcpSubmitFeedback(ctx context.Context, req *mcp.CallToolRequest, in submitFeedbackIn) (*mcp.CallToolResult, submitFeedbackOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), submitFeedbackOut{}, nil
	}
	dto, ae := s.feedbackCore(ctx, u, k, feedbackCreateRequest{Subject: in.Subject, Body: in.Body, Kind: in.Kind}, "mcp", "")
	if ae != nil {
		return mcpErr(ae), submitFeedbackOut{}, nil
	}
	return nil, submitFeedbackOut{Feedback: dto}, nil
}

// ---- attachments (list_attachments / upload_attachment / delete_attachment) ----

// mcpAttachment is an attachmentOut plus two agent conveniences: an absolute
// download_url (the embedded `url` is relative, for the body; this one is
// fetchable over HTTP directly) and a ready-to-paste `markdown` embed snippet.
type mcpAttachment struct {
	attachmentOut
	DownloadURL string `json:"download_url"`
	Markdown    string `json:"markdown"`
}

func newMCPAttachment(a attachmentOut) mcpAttachment {
	return mcpAttachment{
		attachmentOut: a,
		DownloadURL:   canonicalBaseURL() + a.URL,
		Markdown:      attachmentEmbedMarkdown(a),
	}
}

// attachmentEmbedMarkdown is the snippet to drop into a page body: inline image
// syntax for image mimes, a :::file download card for everything else.
func attachmentEmbedMarkdown(a attachmentOut) string {
	if strings.HasPrefix(a.Mime, "image/") {
		return fmt.Sprintf("![%s](%s)", a.Name, a.URL)
	}
	return fmt.Sprintf(":::file{name=%q size=\"%d\"}\n%s\n:::", a.Name, a.ByteSize, a.URL)
}

// mcpInlineUploadCap bounds upload_attachment's base64 payload. Inline base64
// rides through the model's context (a tool argument IS model content), so a
// large blob bloats tokens + cost and can trip a host's content filter. Files
// above this belong on a side channel — the editor drag-drop, or the planned
// request_attachment_upload signed-PUT handshake — where the bytes never enter
// the context. This is the *transport* limit; davFileMaxBytes (50 MiB) remains
// the storage limit. A var so tests can shrink it without generating megabytes.
var mcpInlineUploadCap = 5 << 20 // 5 MiB

// decodeMCPBase64 decodes a base64 tool argument, tolerating a leading
// `data:<mime>;base64,` URL prefix that agents often include.
func decodeMCPBase64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "data:") {
		if i := strings.Index(s, ";base64,"); i >= 0 {
			s = s[i+len(";base64,"):]
		}
	}
	return base64.StdEncoding.DecodeString(s)
}

type listAttachmentsIn struct {
	PageID int64 `json:"page_id" jsonschema:"page whose attachments to list"`
}

type listAttachmentsOut struct {
	Attachments []mcpAttachment `json:"attachments"`
}

func (s *Server) mcpListAttachments(ctx context.Context, req *mcp.CallToolRequest, in listAttachmentsIn) (*mcp.CallToolResult, listAttachmentsOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), listAttachmentsOut{}, nil
	}
	atts, ae := s.listPageAttachmentsCore(ctx, u, k, in.PageID)
	if ae != nil {
		return mcpErr(ae), listAttachmentsOut{}, nil
	}
	out := listAttachmentsOut{Attachments: make([]mcpAttachment, len(atts))}
	for i, a := range atts {
		out.Attachments[i] = newMCPAttachment(a)
	}
	return nil, out, nil
}

type uploadAttachmentIn struct {
	PageID     int64  `json:"page_id" jsonschema:"page to attach the file to"`
	Name       string `json:"name" jsonschema:"file name including extension, e.g. report.pdf or chart.png (drives the displayed name + type detection)"`
	DataBase64 string `json:"data_base64" jsonschema:"the file bytes, base64-encoded; a leading data:<mime>;base64,… URL prefix is also accepted"`
}

type uploadAttachmentOut struct {
	Attachment mcpAttachment `json:"attachment"`
}

func (s *Server) mcpUploadAttachment(ctx context.Context, req *mcp.CallToolRequest, in uploadAttachmentIn) (*mcp.CallToolResult, uploadAttachmentOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), uploadAttachmentOut{}, nil
	}
	if ae := mcpRequireWrite(k); ae != nil {
		return mcpErr(ae), uploadAttachmentOut{}, nil
	}
	data, err := decodeMCPBase64(in.DataBase64)
	if err != nil {
		return mcpErr(&apiErr{400, "bad_request", "data_base64 is not valid base64"}), uploadAttachmentOut{}, nil
	}
	if len(data) > mcpInlineUploadCap {
		return mcpErr(&apiErr{413, "too_large", fmt.Sprintf(
			"file is %.1f MB; inline base64 upload is capped at %d MB (it rides through the model context). For larger files call request_attachment_upload for a direct PUT URL, or use the tela editor (drag-drop).",
			float64(len(data))/(1<<20), mcpInlineUploadCap>>20)}), uploadAttachmentOut{}, nil
	}
	a, ae := s.uploadPageAttachmentCore(ctx, u, k, in.PageID, in.Name, data)
	if ae != nil {
		return mcpErr(ae), uploadAttachmentOut{}, nil
	}
	return nil, uploadAttachmentOut{Attachment: newMCPAttachment(a)}, nil
}

type deleteAttachmentIn struct {
	PageID int64 `json:"page_id" jsonschema:"the page the file is attached to"`
	ID     int64 `json:"id" jsonschema:"attachment id (from list_attachments)"`
}

func (s *Server) mcpDeleteAttachment(ctx context.Context, req *mcp.CallToolRequest, in deleteAttachmentIn) (*mcp.CallToolResult, okOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), okOut{}, nil
	}
	if ae := mcpRequireWrite(k); ae != nil {
		return mcpErr(ae), okOut{}, nil
	}
	if ae := s.deletePageAttachmentCore(ctx, u, k, in.PageID, in.ID); ae != nil {
		return mcpErr(ae), okOut{}, nil
	}
	return nil, okOut{OK: true}, nil
}

// ---- direct upload handshake (request → PUT out-of-band → confirm) ----

type requestAttachmentUploadIn struct {
	PageID int64  `json:"page_id" jsonschema:"page to attach the file to"`
	Name   string `json:"name" jsonschema:"file name including extension, e.g. deck.pdf or photo.png"`
	Mime   string `json:"mime,omitempty" jsonschema:"optional content-type hint; for images the server still trusts magic bytes"`
}

type requestAttachmentUploadOut struct {
	Upload uploadTicket `json:"upload"`
}

func (s *Server) mcpRequestAttachmentUpload(ctx context.Context, req *mcp.CallToolRequest, in requestAttachmentUploadIn) (*mcp.CallToolResult, requestAttachmentUploadOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), requestAttachmentUploadOut{}, nil
	}
	if ae := mcpRequireWrite(k); ae != nil {
		return mcpErr(ae), requestAttachmentUploadOut{}, nil
	}
	t, ae := s.requestAttachmentUploadCore(ctx, u, k, in.PageID, in.Name, in.Mime)
	if ae != nil {
		return mcpErr(ae), requestAttachmentUploadOut{}, nil
	}
	return nil, requestAttachmentUploadOut{Upload: t}, nil
}

type confirmAttachmentUploadIn struct {
	UploadID string `json:"upload_id" jsonschema:"the upload_id from request_attachment_upload"`
}

func (s *Server) mcpConfirmAttachmentUpload(ctx context.Context, req *mcp.CallToolRequest, in confirmAttachmentUploadIn) (*mcp.CallToolResult, uploadAttachmentOut, error) {
	u, k := mcpIdentity(req)
	if u == nil {
		return mcpUnauthErr(), uploadAttachmentOut{}, nil
	}
	if ae := mcpRequireWrite(k); ae != nil {
		return mcpErr(ae), uploadAttachmentOut{}, nil
	}
	a, ae := s.confirmAttachmentUploadCore(ctx, u, k, in.UploadID)
	if ae != nil {
		return mcpErr(ae), uploadAttachmentOut{}, nil
	}
	return nil, uploadAttachmentOut{Attachment: newMCPAttachment(a)}, nil
}
