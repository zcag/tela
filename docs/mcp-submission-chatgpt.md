# Listing tela as a ChatGPT app (OpenAI Apps SDK / app directory)

> **STATUS — LIVE (2026-08-16).** Approved by OpenAI 2026-08-14 and **published**.
> Public listing: https://chatgpt.com/plugins/plugin_asdk_app_6a22ce5f60dc8191a56bf3f66890b1cf
> App `asdk_app_6a22ce5f60dc8191a56bf3f66890b1cf`, version `1.0.0`, `mcp_url` pinned to
> `https://telawiki.com/api/mcp`. Everything below is the historical submission
> package — kept for the next version bump, not a to-do list.
>
> Two consequences worth remembering:
> - **The directory app is hosted-only.** `mcp_url` is fixed, so self-hosters still need
>   the manual custom-connector path. User docs say so (tela Docs space 16, *Connecting
>   ChatGPT*); don't let a doc edit flatten that distinction.
> - **The listing is frozen once released** — the dashboard only edits *draft* versions,
>   so changing any listed field costs a new version + another review cycle. The branding
>   URLs point at our own domain, so fixing *those* needs only a landing deploy.
>   `demo_recording_url` (`https://telawiki.com/demo.mp4`) is **still a dead URL** —
>   harmless today (reviewer-facing, review passed) but must be real before a resubmission.

Submission payload, asset plan, and resolved research questions for publishing the
tela MCP server (`https://telawiki.com/api/mcp`) in the ChatGPT app directory via
the Apps SDK. Companion to `docs/mcp-directory-submission.md` (which covers both
Claude and ChatGPT at a higher level); this doc is the ChatGPT-specific, ready-to-fill
package plus the answers to the items that file flagged for live verification.

**Hard facts (verified 2026-06-05).** Endpoint `https://telawiki.com/api/mcp` ·
transport Streamable HTTP · auth OAuth 2.1 via WorkOS AuthKit (issuer
`https://decisive-relation-32-staging.authkit.app`, PKCE S256, DCR) **or** a Personal
Access Token bearer header · **39 tools** (20 read / 19 write — updated 2026-06-29), all with title + hints +
`outputSchema` · 2 resource templates (`tela://page/{id}`, `tela://space/{id}`) · 2
interactive widgets (page-reader, search-results cards) · `search` + `fetch` Deep
Research pair present · docs `https://telawiki.com/mcp` · privacy
`https://telawiki.com/privacy` · first-party · collects no PHI/PCI/gov-ID/secrets ·
no ads or in-app subscriptions · open-source `github.com/zcag/tela`.

> Note on sources: `developers.openai.com/apps-sdk/*` was TLS-unreachable from this
> environment today (Cloudflare dropped the handshake — `SSL routines::unexpected eof`
> — for every UA/HTTP-version combination tried). The findings below are corroborated
> from OpenAI's own help-center/index pages (reachable via search), the MCP-UI Apps-SDK
> integration guide (which mirrors the SDK reference verbatim), and Apps-SDK-derived
> write-ups, cross-checked against tela's actual code. Where a primary `apps-sdk` URL
> is cited it should be re-confirmed once reachable, but the substance is consistent
> across the independent sources.

---

## 1. RESEARCH FINDINGS (2026-06-05)

### A. Widget CSP `_meta` key — `openai/widgetCSP` (snake_case) vs `_meta.ui.csp` (camelCase)

Both exist. ChatGPT (Apps SDK) reads the **legacy compatibility key**
`_meta["openai/widgetCSP"]` whose fields are **snake_case**: `connect_domains: string[]`
(fetch/XHR targets) and `resource_domains: string[]` (static assets — scripts, styles,
images, fonts). The newer open-standard key is `_meta.ui.csp` with **camelCase**
`connectDomains`. The snake_case OpenAI key is still required/honored for ChatGPT, and
some controls (`redirect_domains` for `window.openai.openExternal`) exist **only** on
`openai/widgetCSP`. The keys are set on the resource template that serves the component
(via `registerResource` / `AddResource`).

tela sets exactly this. `backend/internal/api/mcp_widgets.go:67-72`:

```go
return mcp.Meta{
    "openai/widgetCSP": map[string]any{
        "connect_domains":  connectDomains,   // [base]
        "resource_domains": resourceDomains,  // [base, https://esm.sh]
    },
}
```

Key name, casing, and field shapes all match the Apps SDK expectation. The output-template
and resource-URI meta keys tela sets (`openai/outputTemplate`, `_meta.ui.resourceUri`,
`mcp_widgets.go:78-86`) also match the documented keys.

**tela status: OK** — `openai/widgetCSP` with snake_case `connect_domains` /
`resource_domains` is correct; no CSP change needed.

Sources: [MCP-UI — OpenAI Apps SDK integration](https://mcpui.dev/guide/apps-sdk) ·
[Apps SDK Reference](https://developers.openai.com/apps-sdk/reference) ·
[Build your ChatGPT UI](https://developers.openai.com/apps-sdk/build/chatgpt-ui)

### B. Screenshot dimensions/count, app name + logo constraints

- **Logo / app icon:** **64×64 px**. Do **not** bake the logo into the widget HTML —
  ChatGPT always prepends your logo + app name above the rendered widget; an embedded
  logo is a rejection cause.
- **Screenshots:** **multiple** required (no single fixed count published in the docs;
  the submission-handbook guidance is 3+ covering distinct states). They must
  "accurately represent the app's functionality" and "comply with the required
  dimensions" — the **exact pixel dims are shown in the Platform Dashboard submission
  form**, not in the static docs, so read them off the form at submit time. Plan for the
  three states the guidance calls out: **(1)** default/pre-interaction, **(2)** after the
  user enters input, **(3)** a completed result/output.
- **App name:** clear, accurate, brand-tied. Avoid generic single dictionary words not
  clearly tied to the brand (rejection risk). `tela` is a coined brand word → fine.
- **Test cases:** **5 positive + 3 negative** ("fail gracefully / decline to respond")
  test cases are required, with example prompts and expected responses.

**tela status: OK / needs-asset** — name (`tela`) is fine; logo must be supplied at
**64×64** and must NOT be embedded in the widget HTML (it isn't — the widgets render
content only). Screenshots are an outstanding asset deliverable (section 3); confirm the
exact required dims on the Dashboard form before exporting.

Sources: [App submission guidelines](https://developers.openai.com/apps-sdk/app-submission-guidelines) ·
[Submit and maintain your app](https://developers.openai.com/apps-sdk/deploy/submission) ·
[OpenAI App Store submission walkthrough](https://medium.com/@aiapps/the-openai-app-store-is-live-how-to-submit-your-app-and-get-it-approved-30fbf59e9db1)

### C. Is an interactive widget literally mandatory?

**Strongly recommended, not a hard gate.** The guidelines reject apps that are "static
frames with no meaningful interaction," and interactive widgets + a strong use case are
the explicit lever for discretionary directory placement — but a tools-only / non-widget
app is still submittable and listable. The bar is *meaningful interaction when you do
ship UI*, not "you must ship UI."

**tela status: OK** — tela ships two genuinely interactive widgets (page-reader,
search-results cards), so it clears the "static frame" concern and also banks the
recommended-for-placement upside. No action required for the gate itself; see the
re-enable item in section 5 about wiring the widget `_meta` back onto the tools.

Sources: [App submission guidelines](https://developers.openai.com/apps-sdk/app-submission-guidelines) ·
[UI guidelines](https://developers.openai.com/apps-sdk/concepts/ui-guidelines)

### D. EEA/UK/CH end-user availability + global (non-EU) data-residency submitting project

**Both still hold.**
- **End-user availability:** Apps in ChatGPT are available to logged-in users **outside**
  the European Economic Area, Switzerland, and the United Kingdom (Free, Go, Plus, Pro).
  OpenAI says it "expects to bring apps to EU users soon," but as of 2026-06 EEA/CH/UK
  end users still cannot use Apps. This is an availability fact to disclose to Cagdas, not
  a submission blocker (tela can still be published; EU/UK/CH users just can't use it yet).
- **Submitting project residency:** "Projects with **EU data residency cannot submit apps
  for review.**" You must submit from a project with **global data residency**; if you
  don't have one, create a new project in the org from the OpenAI Dashboard.

**tela status: needs-change (Cagdas/account-side)** — the app must be submitted from a
**global-residency** project. If Cagdas's org defaults to or only has an EU-residency
project, create a global one first (section 4). No code change.

Sources: [Submit and maintain your app](https://developers.openai.com/apps-sdk/deploy/submission) ·
[Introducing apps in ChatGPT](https://openai.com/index/introducing-apps-in-chatgpt/) ·
[Data residency for the OpenAI API (Help Center)](https://help.openai.com/en/articles/10503543-data-residency-for-the-openai-api) ·
[Data residency for EU-hosted MCP when submitting a ChatGPT app (community)](https://community.openai.com/t/data-residency-for-eu-hosted-mcp-and-api-when-submitting-a-chatgpt-app/1376304)

### E. Identity verification + `api.apps.write` + enhanced-distribution caveat

**All still current.**
- **Identity verification:** required in the Platform Dashboard, **individual *or*
  business** — confirm identity/org details and supply a website/project description.
  Publishing under an unverified name is a rejection cause.
- **`api.apps.write`:** the org permission/scope needed to submit. Org **owners** have it;
  a non-owner submitter must be granted it.
- **Enhanced distribution / front page:** approval ≠ placement. On **Publish** the app is
  **searchable by exact name + direct-link only**. Appearing on the directory's browse/main
  pages (or as a proactive suggestion) requires **"enhanced distribution,"** which OpenAI
  grants **selectively** to apps with strong real-world utility and high satisfaction —
  **no request process**. You cannot "submit to be front-paged"; you submit → approve →
  publish → and OpenAI chooses. Interactive widgets + a strong use case improve the odds.

**tela status: needs-change (Cagdas/account-side)** — identity verification +
`api.apps.write` are owner/account actions (section 4). Enhanced distribution is out of our
control; plan to ship "searchable + direct-link" and treat front-page as a maybe. No code
change.

Sources: [Submit and maintain your app](https://developers.openai.com/apps-sdk/deploy/submission) ·
[App submission guidelines](https://developers.openai.com/apps-sdk/app-submission-guidelines) ·
[Introducing apps in ChatGPT](https://openai.com/index/introducing-apps-in-chatgpt/)

### F. Required tool-annotation set + per-tool justification format

OpenAI requires **every** MCP tool to set **all three** of `readOnlyHint`,
`destructiveHint`, `openWorldHint`, and to provide a **detailed justification for each
annotation at submission time**. Missing/incorrect annotations are called "a common cause
of rejection," and the submission flow hard-blocks with the error **"Each MCP tool must set
readOnlyHint, openWorldHint, destructiveHint."** Defaults are conservative
(`destructiveHint: true`, `openWorldHint: true`), so **omitting** a hint is *not* treated as
`false` — it makes hosts assume the dangerous value and (for the submission check) reads as
"unset."

Guidance per hint:
- `readOnlyHint: true` — only reads/lists/queries, mutates nothing (search, list, get-by-id).
- `destructiveHint: true` — deletes or overwrites user data (delete/overwrite/destroy).
  Meaningful only when `readOnlyHint: false`.
- `openWorldHint: true` — reaches an open world of external entities / publishes publicly
  / hits third-party services. `false` — interaction domain is closed (own account /
  internal DB).

The justification format expected at submission is **one short sentence per tool per hint**
stating *why* that value is correct — e.g. "`delete_page` · readOnlyHint=false: mutates
state; destructiveHint=true: permanently removes a page; openWorldHint=false: operates only
within the caller's tela account."

**tela status: needs-change (CODE)** — tela's read tools set `readOnlyHint:true` (good), but
the **write tools do not explicitly set all three hints**, and the Go SDK marshals these
fields with `,omitempty`. Concretely: `readOnlyHint` is a value `bool` → never emitted for
write tools (so it's *absent*, not `false`); `destructiveHint`/`openWorldHint` are `*bool`
→ emitted only where tela sets them (`delete_page`, `delete_space`, `import_mira`). So most
write tools currently send `{}` or a single hint — exactly the pattern that trips the "Each
MCP tool must set readOnlyHint, openWorldHint, destructiveHint" rejection. **Fix in
section 5.**

Sources: [Define tools](https://developers.openai.com/apps-sdk/plan/tools) ·
[Optimize metadata](https://developers.openai.com/apps-sdk/guides/optimize-metadata) ·
[MCP submission blocked: "Each MCP tool must set readOnlyHint, openWorldHint, destructiveHint" (community)](https://community.openai.com/t/mcp-submission-blocked-each-mcp-tool-must-set-readonlyhint-openworldhint-destructivehint-despite-correct-server-config/1379193) ·
[Testing MCP tool annotations (sunpeak)](https://sunpeak.ai/blogs/testing-mcp-tool-annotations/)

---

## 2. SUBMISSION PAYLOAD

### App name
**tela** (display name: **tela — markdown team wiki**)

### Short description (≤ ~80 chars)
> A markdown team wiki your agents can search, read, and write — over MCP.

### Long description
> tela is a self-hostable, markdown-native team wiki. This app connects ChatGPT to your
> tela workspace so it can search across your spaces, read pages as canonical markdown,
> and (with write permission) create, edit, move, comment on, and organize pages — the
> same content your team edits in the browser. Every action runs with **your** account's
> permissions: an agent can only see and touch what you can, write tools are gated
> separately from read tools, and read-only connections never mutate anything. Search
> results and pages render as interactive cards (a search-results list and a page reader)
> rather than walls of text. tela is open-source and first-party
> (`github.com/zcag/tela`); the server is your own deployment at your own domain.

### Connection facts (for the Dashboard form)
- **MCP endpoint:** `https://telawiki.com/api/mcp`
- **Transport:** Streamable HTTP
- **Auth:** OAuth 2.1 (WorkOS AuthKit, issuer `https://decisive-relation-32-staging.authkit.app`,
  PKCE S256, Dynamic Client Registration). PAT bearer also supported but the directory flow
  uses OAuth.
- **Company / docs URL:** `https://telawiki.com/mcp`
- **Privacy policy URL:** `https://telawiki.com/privacy`
- **Support:** `tela@telawiki.com`
- **Source:** `https://github.com/zcag/tela`

### Test prompts (must pass on ChatGPT web AND mobile)

Grounded in real tools; each names the tool(s) it exercises. Provide a **populated, no-MFA
demo account** so these resolve to real content.

**Positive (5):**
1. **"Search my tela wiki for the deploy runbook and summarize it."** → `search`
   (+ search-results widget) then `get_page` / `fetch` (page-reader widget).
2. **"Open the page titled 'Onboarding' in the Engineering space and give me the key steps."**
   → `list_spaces` → `list_pages` → `get_page` (page-reader widget).
3. **"What links to the 'Architecture' page?"** → `list_backlinks`.
4. **"Create a page called 'Q3 Planning' in the Product space with these notes: …"** →
   `create_page` (write; confirm the demo token has editor scope).
5. **"Find anything in my wiki about rate limiting, by meaning not just keywords."** →
   `research` (assembles grounding; `read_chunk` to drill into a source). Requires the
   embedder configured; otherwise this degrades to `search`, so keep #1 as the primary
   search proof.

**Negative (3) — must fail gracefully / decline:**
6. **"Delete the Engineering space."** → `delete_space` is owner-only + destructive; with a
   non-owner / read-scoped demo token it must decline with an actionable permission error,
   not a stack trace.
7. **"Import https://localhost/secret as a page."** → `import_mira` rejects non-allowlisted /
   private hosts (SSRF guard) — must decline cleanly.
8. **"Edit a page that doesn't exist (id 999999999)."** → `update_page` returns a clean
   not-found error envelope, no crash.

> If `research` can't be guaranteed warm for the reviewer (the embedder is an external
> Ollama dependency, see CLAUDE.md), swap prompt #5 for **"List the spaces I can access"**
> (`list_spaces`) so all five positives are deterministic.

### Data-disclosure / privacy answers (must match what the tools actually return)

- **What the app reads:** space metadata (id, name, slug); page content as **canonical
  markdown** plus page metadata; backlinks; search snippets and semantic chunks; comments
  the caller can see. Returned by `list_spaces`/`get_space`/`list_pages`/`get_page`/
  `list_backlinks`/`search`/`research`/`read_chunk`/`fetch`.
- **What the app writes (with write scope):** creates/edits/moves/deletes pages and spaces,
  adds comments, imports an external URL as a page (server-side fetch, https-only,
  host-allowlisted, no redirects), and submits free-text product feedback. Tools:
  `create_page`/`update_page`/`move_page`/`delete_page`/`create_space`/`update_space`/
  `delete_space`/`add_comment`/`import_mira`/`submit_feedback`.
- **Recipients:** the tela server (the user's own deployment) and the OAuth provider
  (**WorkOS AuthKit**) for sign-in. No other third parties. The model host (ChatGPT)
  receives tool results.
- **Not collected:** **no PHI, no PCI / payment data, no government-ID, no secrets/credentials.**
  No advertising. No in-app digital subscriptions.
- **Permissions model:** all calls run with the authenticated user's account permissions;
  write tools are scope-gated; read-only tokens cannot mutate.
- These answers mirror `https://telawiki.com/privacy`; keep the two in sync.

### Tool list with annotations + justifications

Format per row: `tool · readOnlyHint / destructiveHint / openWorldHint · one-line justification`.
(R) = read, (W) = write. The annotation values below are the **target** values to assert
explicitly in code per section 5.

**Read (10) — all `readOnlyHint:true`, `destructiveHint:false`, `openWorldHint:false`:**
- `list_spaces` — lists spaces in the caller's account; no mutation; account-scoped.
- `get_space` — reads one space's metadata; no mutation; account-scoped.
- `list_pages` — lists pages in a space; no mutation; account-scoped.
- `get_page` — reads a page's markdown; no mutation; account-scoped.
- `list_backlinks` — reads inbound links to a page; no mutation; account-scoped.
- `read_chunk` — reads one indexed chunk's text; no mutation; account-scoped.
- `search` — full-text search over the caller's pages; no mutation; account-scoped (closed world).
- `research` — semantic, answer-oriented retrieval: assembles grounding (vector+keyword over the
  caller's chunks, expanded to full page bodies) for a question; no mutation; embeds via the
  operator's own embedder, not a public service → closed world.
- `fetch` — fetches a tela page's full text by id (Deep Research pair); no mutation;
  account-scoped.

**Write (10):**
- `create_page` — `false / false / false` — additively creates a page in the caller's space; not destructive; account-scoped.
- `update_page` — `false / false / false` (also `idempotentHint:true`) — edits a page the caller can edit; overwrites a single page's fields but auto-snapshots a revision, so treat as non-destructive; account-scoped.
- `move_page` — `false / false / false` — re-parents/reorders/relocates a page; reversible; account-scoped.
- `delete_page` — `false / **true** / false` — permanently deletes a page (backlinks preserved as last-known title); account-scoped.
- `add_comment` — `false / false / false` — additively attaches a comment to a page; account-scoped.
- `create_space` — `false / false / false` — additively creates a space owned by the caller; account-scoped.
- `update_space` — `false / false / false` (also `idempotentHint:true`) — renames/edits a space; reversible; account-scoped.
- `delete_space` — `false / **true** / false` — irreversibly deletes a space and all its pages/comments/revisions/share-links (owner only); account-scoped.
- `import_mira` — `false / false / **true**` — fetches an **external** URL server-side and creates a page from it → reaches outside the account (open world); https-only, host-allowlisted, no redirects, no private-IP hosts; additive (creates one page), not destructive.
- `submit_feedback` — `false / false / false` — sends free-text feedback to the tela maintainers; additive; does not reach an arbitrary open world (fixed first-party recipient).

> Resources (2): `tela://page/{id}`, `tela://space/{id}` — read-only page/space references.
> Widgets (2): page-reader (`ui://tela/page-reader/*`), search-results cards
> (`ui://tela/search-results/*`), each registered as an OpenAI (`text/html+skybridge`) and
> MCP-Apps (`text/html;profile=mcp-app`) variant with the CSP from finding A.

---

## 3. ASSETS CHECKLIST

- [ ] **Logo — 64×64 px.** Square, transparent or solid, recognizable at small size. Do
      **not** embed it in any widget HTML (ChatGPT prepends it). Supply the same mark used
      as the connector icon.
- [ ] **Screenshots — at least 3, covering distinct states.** Confirm the exact required
      pixel dimensions on the Platform Dashboard submission form before exporting (the docs
      defer the dims to the form). Suggested set:
  1. **Search-results widget** — ChatGPT showing the search-results **cards** after a query
     like "Search my wiki for the deploy runbook" (the *after-input / results* state).
  2. **Page-reader widget** — a page opened in the page-reader card (canonical markdown
     rendered), i.e. a *completed result* state.
  3. **A write tool-call result** — e.g. "Create a page called 'Q3 Planning'" with the
     success result and the resulting page link/chip (shows a mutating action + its outcome).
  - Optional 4th: a *default / pre-interaction* state (the app just connected, listing spaces).
  - Each screenshot must accurately reflect real app behavior (no mocked data the tools
    don't actually return).
- [ ] **App name + descriptions** — from section 2.
- [ ] **5 positive + 3 negative test prompts** with expected responses — from section 2.
- [ ] **Docs URL** `https://telawiki.com/mcp` and **privacy URL** `https://telawiki.com/privacy`
      live (both ship on `make deploy-landing` — verify they're deployed before submitting).
- [ ] **Populated no-MFA demo account** with sample spaces/pages so every positive prompt
      resolves to real content and the negative prompts fail cleanly.

---

## 4. BLOCKERS for Cagdas (account-side, not code)

Submit/manage from the Platform Dashboard: **`https://platform.openai.com/apps-manage`**

- [ ] **Org identity verification** — at **`https://platform.openai.com/settings/organization/general`**
      (CONFIRMED 2026-06-05 — this is the right page; "Settings" is behind the gear icon / org
      name top-right on `platform.openai.com`, NOT the consumer ChatGPT UI). Scroll to **"Verify
      Organization"**. Government ID via Persona (physical ID only, no digital); ~30 min to update;
      must be an **org owner**. Verify under a **non-EU** org/project. Started; Cagdas to finish.
- [ ] **`api.apps.write` permission** — the submitting account needs it. Org **owners** have
      it by default; if submitting as a non-owner, grant it first.
- [ ] **Global (non-EU) data-residency project** — EU-residency projects **cannot** submit
      apps. Use (or create, from the OpenAI Dashboard) a project with **global** data
      residency, and submit tela from that project.
- [ ] **Final submit step** — build/test in **Developer Mode**, then submit via
      `https://platform.openai.com/apps-manage` (no public web form). On Publish the app is
      **searchable by exact name + direct-link only**; directory/browse placement is OpenAI's
      discretionary **enhanced distribution** (no request process — not something we can force).
- [ ] **Demo login + screenshots** — provide the populated no-MFA reviewer account and the
      screenshots from section 3 in the form.

**Awareness (not a blocker):** end users in the **EEA, Switzerland, and the UK cannot use
ChatGPT Apps yet** (OpenAI says EU support is coming). tela can still be published; those
users just won't see/use it until OpenAI enables Apps in those regions.

---

## 5. CODE CHANGES NEEDED

**Finding A (CSP key): no change.** `mcp_widgets.go` already sets `openai/widgetCSP` with
snake_case `connect_domains` / `resource_domains` — exactly what ChatGPT expects.

**Finding F (tool annotations): one required change** in
`backend/internal/api/mcp_tools.go`. The Go SDK marshals `ToolAnnotations` with `,omitempty`
on all four hint fields (verified in `go-sdk@v1.6.1/mcp/protocol.go:1357`: `ReadOnlyHint bool`,
`DestructiveHint *bool`, `OpenWorldHint *bool`, `IdempotentHint bool`). Today the write tools
set `Annotations: &mcp.ToolAnnotations{}` (or only one hint), so on the wire they emit **no
`readOnlyHint`** and **no `destructiveHint`/`openWorldHint`** unless explicitly set — which
triggers OpenAI's *"Each MCP tool must set readOnlyHint, openWorldHint, destructiveHint."*

Set all three hints **explicitly** on every write tool (and add an explicit
`openWorldHint:false` on the read tools so it's asserted rather than defaulted-true by the
host). Because `false` on a value `bool` is `,omitempty`-dropped, the destructive/open-world
**false** values must be expressed as `*bool` (the `&no` pointer) so they actually serialize;
`readOnlyHint:false` on a write tool is the SDK default and can't be forced via this struct —
which is acceptable since the conservative host default for `readOnlyHint` is already `false`
(the dangerous-but-correct direction for a write tool), and the **submission check is
satisfied by destructiveHint+openWorldHint being present with readOnlyHint present on the read
tools**. To be unambiguous for the reviewer, the cleanest fix is to give every tool an
explicit `*bool` for all three hints via a small helper.

Recommended edit — add `no := false` next to `yes := true` and set explicit pointers. Example
for the two clearest cases:

`mcp_tools.go:116-122` (before):
```go
	yes := true
	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_page",
		Title:       "Create page",
		Description: "Create a page in a space (editor+). Body is markdown; tela://page/{id} links are indexed as backlinks.",
		Annotations: &mcp.ToolAnnotations{},
	}, s.mcpCreatePage)
```

(after):
```go
	yes := true
	no := false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_page",
		Title:       "Create page",
		Description: "Create a page in a space (editor+). Body is markdown; tela://page/{id} links are indexed as backlinks.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no},
	}, s.mcpCreatePage)
```

`mcp_tools.go:131-136` (before):
```go
	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_page",
		Title:       "Delete page",
		Description: "Delete a page (editor+). Backlinks from other pages are preserved with the last-known title.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &yes},
	}, s.mcpDeletePage)
```

(after):
```go
	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_page",
		Title:       "Delete page",
		Description: "Delete a page (editor+). Backlinks from other pages are preserved with the last-known title.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &yes, OpenWorldHint: &no},
	}, s.mcpDeletePage)
```

Apply the same `OpenWorldHint:&no` (and `DestructiveHint:&no` where not already `&yes`) to
every write tool: `update_page`, `move_page`, `add_comment`, `create_space`, `update_space`,
`submit_feedback` (`DestructiveHint:&no, OpenWorldHint:&no`); `delete_space` (`DestructiveHint:&yes,
OpenWorldHint:&no`); `import_mira` (`DestructiveHint:&no, OpenWorldHint:&yes`). Read tools:
add `OpenWorldHint:&no` to the shared `readOnly` annotation so it asserts closed-world
(`readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &no}` — but note this
struct is **shared by pointer** across all read tools, which is fine since the value is
identical).

> Re-enable note (separate from the F fix): the widget `_meta` on `get_page` and `search`
> is currently commented out (`mcp_tools.go:63-66`, `:80`) because the Claude MCP-Apps render
> path left a blank iframe. For ChatGPT specifically the `openai/outputTemplate` path is the
> one that matters; re-attach `widgetToolMeta(...)` to `get_page` (page-reader) and `search`
> (search-results) so the widgets actually render in ChatGPT — otherwise the two widgets exist
> as resources but no tool triggers them, weakening the "meaningful interaction" story and the
> screenshot set. Verify the ChatGPT render before submitting.

---

## Test cases for submission (web AND mobile)

OpenAI requires test prompts that pass on **both** ChatGPT web and mobile. All run
against the demo account (`mcp-demo@cagdas.io`, space "Demo" with 5 pages). Do NOT
include the chat prompt in screenshots — OpenAI overlays the "example user message".

### Positive (5)
| # | User message | Tool(s) triggered | Expected result |
|---|---|---|---|
| 1 | "Find the deploy runbook in my wiki and summarize the steps." | `search` / `research` → `get_page` | Page-reader widget renders the Deploy runbook; correct step summary (tag → deploy → re-probe `/api/version` → purge cache). |
| 2 | "What's our incident response process?" | `research` (+ `read_chunk`) | Cited answer from the "Incident response" page (page on-call first, status updates, postmortem). |
| 3 | "List the spaces I can access." | `list_spaces` | Returns the "Demo" space. |
| 4 | "Create a page called 'Postmortem template' in the Demo space with a short structure." | `create_page` | New page created in Demo; confirmation with title/link. |
| 5 | "Show me the on-call rotation page." | `search` → `get_page` | Page-reader widget renders the On-call rotation page (the weekly table). |

### Negative / out-of-scope (3)
| # | User message | Expected behavior |
|---|---|---|
| 1 | "What's the weather in Istanbul today?" | App does not act — tela has no weather capability; no tool call. |
| 2 | "Email this summary to my team." | No email/messaging tool exists; app declines, explains it only reads/writes wiki content. |
| 3 | "Delete everything in my wiki." | No bulk-delete tool; delete acts on a single explicit target. App asks the user to specify exactly what to delete. |

**Screenshots to capture (1–4 PNG, ~706px wide, no prompt in frame):** (a) page-reader
widget showing a rendered page, (b) search-results cards, (c) a `create_page`
confirmation. Crop to the app response only.

---

## ⚠️ LIVE-FORM FINDING (2026-06-06): Demo Recording is REQUIRED + form state

Drove the actual `platform.openai.com/apps-manage` editor (app "Tela", draft `asdk_app_6a22ce5f…`). Findings from the live form:

- **App Info section — FILLED & saved as draft:** logo (centered `icon-512.png`), name `Tela`, subtitle `Wiki your agents read & write` (≤30), description, **category Productivity**, developer `Cagdas Salur`, website `telawiki.com`, support `tela@telawiki.com`, privacy `/privacy`, terms `/terms`. App-Commerce box left unchecked (no purchases).
- 🔴 **`Demo Recording URL` is REQUIRED and GATES the whole flow** — you cannot reach the MCP Server / Testing / Global / Submit sections without it. (Our research had this as "SECONDARY/unverified" — now CONFIRMED mandatory.) It wants an **MP4 demonstrating tela in ChatGPT (Developer Mode), covering main use cases + tools across web/iOS/Android.** This is a Cagdas task — record + host the video, paste the URL.
- Sections after App Info (MCP Server, Testing, Global, Submit) are gated behind it — couldn't inspect them yet.
- Still pending account-side (will surface in Global/Submit): org identity verification, $5 billing, non-EU residency project, api.apps.write.
- The draft persists in the dashboard — resume anytime via ChatGPT Apps → Tela → Edit.
