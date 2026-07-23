# Decks (Slidev presentations)

A tela **deck** is a normal **page** whose body is **[Slidev](https://sli.dev)
markdown** — a page is *either* a doc or a deck (set by the `deck` page property),
never slides embedded mid-document. The visual design lives entirely in
**[`slidev-theme-tahta`](https://github.com/zcag/tahta)** (an npm dependency); tela
owns no layouts/styles and injects only a per-deck visual config.

Rendering runs in a **sidecar** (`deck/`, a tiny Node service on `:3344`, the
gotenberg pattern) that the backend proxies. Two output paths off one
parse+theme-injection core:

- **Present** = the **live interactive Slidev SPA** (`slidev build` → pure Vite,
  no Chromium): the real app — presenter mode, slide overview, drawing, clicks.
  Served page-scoped + membership-gated, opened in a new tab.
- **Export / agent-preview / thumbnails** = **static frames** (`slidev export` →
  headless Chromium): PNG / PDF / PPTX.

> Two doc surfaces: this file is the developer/internals view. The end-user
> writeup is the tela **Docs space** (space 16) → *Using Tela → Presentations &
> decks*. Keep both current when deck UX changes.

## Files

- `deck/server.mjs` — the sidecar. Routes: `POST /spa` (build-if-needed + serve one
  SPA file), `POST /render` + `POST /export/{pdf,pptx}` (Chromium), `GET /d/<id>/<file>`
  (rendered asset), `POST /parse` + `POST /lint` (`@slidev/parser` + tahta's
  `lint.mjs`, no render), `GET /authoring` (the agent guide), `GET /themes`, `/health`.
- `backend/internal/api/deck_render.go` — backend proxy: the `requirePageRead`
  gate, `deckSPA`/`deckSPA` serving, export, outline/parse, `deckThemeConfig` (reads
  `variant`/`accent`/`lang` from page props).
- `backend/internal/api/deck_warm.go` — the Present pre-warmer (see below).
- `backend/internal/api/mcp_deck_authoring.go` — the `tela://deck-authoring-guide`
  MCP resource + the create/update tool hint.
- `backend/internal/api/mcp_deck_tools.go` — the `lint_deck` / `preview_deck` MCP tools.
- `frontend/src/components/app/DeckOverview.tsx` — the deck's non-present view
  (slide count, variant picker, Present, PDF/PPTX export). `PageView.tsx` has the
  "Convert to slide deck" action + the Present buttons + the warm-on-open effect.
  `DeckEditorOutline.tsx` — the live slide outline while editing.

## Routes

Page-scoped, **membership-gated** (`requirePageRead`): not in `IsPublicPath`.

```
GET  /api/pages/{id}/deck/spa/{path...}   live SPA (Present) — proxies sidecar /spa
GET  /api/pages/{id}/deck/outline         structure only (editor outline) — /parse
POST /api/pages/{id}/deck/parse           parse a draft (editor outline)
POST /api/pages/{id}/deck/lint            validate a draft (editor advisory)
GET  /api/pages/{id}/deck.pdf | .pptx     export (Chromium)
GET  /api/pages/{id}/deck.md              raw Slidev source (verbatim body)
```

`/deck/lint` is the **human** half of the validation story. Agent writes are
*gated* (`deckWriteGate` rejects a deck with lint errors — an agent ships and
walks away), but a human autosaves keystroke-by-keystroke through transiently
broken states, so their saves are deliberately never blocked. That left them with
no signal at all: a structural error stayed invisible until Present failed to
build, a long way from the keystroke that caused it. This route runs the same
tahta-lint report over the live editor buffer and `DeckEditorOutline` shows the
issues beside the slide list — advisory, never blocking, and silently absent if
the sidecar is down.

`deck.md` (`ExportPageDeckMarkdown`) hands back the deck **body verbatim** — the
real Slidev source, headmatter and all, asset URLs absolutized so images resolve
off-origin — for running in a standalone Slidev (the reader supplies the
`slidev-theme-tahta` look itself). It's deliberately *not* the generic `/md`
export, which prepends a tela frontmatter block (id/title/link/…) for round-trip
and would give a deck two headmatter blocks. Bundling a runnable project
(package.json pinning slidev + tahta, theme baked into headmatter) is a
considered non-goal — it would fork tahta's themeConfig schema into tela; if
built, it belongs behind a tahta sidecar endpoint so the schema stays in tahta.

The **generic page-export routes are deck-aware**: `GET /api/pages/{id}/pdf` and
the share-link `GET /api/share/{token}/pdf` (`pdf_export.go`) detect a deck
(`isDeckBag`) and route to the Slidev export (`streamDeckPDF`, shared with
`/deck.pdf`) instead of feeding the Slidev source to gotenberg — so "Export PDF"
from anywhere yields real per-slide frames, never a wall of `---`-separated YAML.
(A public-space deck has no PDF route at all — it renders `PublicDeckView` /
Present, not a prose reader.)

Public (`/api/deck/` + `/api/public/` are in `IsPublicPath`) — content-addressed
or visibility-gated, self-authenticating:

```
GET  /api/deck/d/{renderId}/{file}                         a rendered PNG/PDF/PPTX (immutable)
GET  /api/deck/themes                                      tahta variant catalog (FE picker)
GET  /api/pages/{id}/deck/cover                            first-slide cover (gated; 302 → asset)
GET  /api/public/spaces/{id}/pages/{pid}/deck/spa/{path…}  live Present for a PUBLIC space's deck
GET  /api/public/spaces/{id}/pages/{pid}/deck/cover        first-slide cover for a public deck
```

The public deck routes self-authenticate via `publicSpacePage` (space
`visibility='public'` + page∈space + `deck`), GET-only — same posture as the rest
of `/api/public/`. `streamDeckSPA` is shared by the gated and public Present paths
(only the gate + the `--base` differ).

A page becomes a deck via **⋯ menu → Convert to slide deck** (sets `props.deck =
true`), or the API/MCP setting `deck: true`.

## Theme injection (shared by both paths)

The stored markdown never references the theme. The sidecar injects `theme:
slidev-theme-tahta` + a per-deck `themeConfig` (`variant`/`accent`/`lang`/`logo`/
`logoInvert`) + `mdc: true` (tahta is MDC-authored), via the parser (`parse` → set
keys on the first-slide YAML → `prettifySlide` → `stringify`), overriding any user
values for those keys. Variant catalog comes from the theme's own `variants.json`;
tela hardcodes nothing visual. (`accent` is hue-only as of tahta 0.11.0 — the
variant clamps L/C into its envelope, so a raw brand hex stays legible/on-style.)

**Org-brand inheritance — identity only, never the variant.** A deck's `accent`/
`logo` come from page props first; if unset they inherit the page's **owning org's
brand identity** — `org_branding` (logo_url + accent), the same white-label brand an
org sets for its custom-domain login/app shell (`deckThemeConfig`/`deckOrgBrand` in
`deck_render.go`, one `spaces ⋈ org_branding` lookup). So org decks carry the brand
mark + color with zero setup; a personal space (no `org_id`) inherits nothing.
`logo` is an image URL (a tela attachment or external https); tahta renders it as
the hero on openers + a footer mark on content slides (`logoInvert` flips a
monochrome mark for the scheme).

The **`variant` is deliberately NOT inherited or defaulted** — it's the biggest
visual decision (typeface/scheme/texture) and must be a conscious per-deck choice,
so the author (human or agent) can't coast. The org has **no say over the variant
at all** — branding is the logo + accent, never the style, and there's no
brand→variant mapping (even a "recommendation" nudges every org deck toward one
look). An unset variant falls back to tahta's own default in the sidecar purely as
a don't-crash safety net (public/OG decks must render) — not a choice tela makes. The MCP authoring guide marks the variant a required, deliberate
pick; the in-app `DeckOverview` shows a "Choose a style" prompt instead of a
defaulted cover.

**Deck body is stored verbatim** — the one exception to tela's "frontmatter never
lives in `pages.body`" invariant. A deck's leading `---…---` is Slidev headmatter /
the first slide, NOT page properties, so `createPageCore`/`applyUpdateTx` keep deck
bodies byte-for-byte (`isDeckBag`/`pageIsDeckTx`). Stripping it blanks the cover slide.

## The live SPA (Present) — and its load-bearing gotcha

`buildSPA` runs `slidev build --base /api/pages/<id>/deck/spa/ --out <cache>` (pure
Vite, no Chromium), then `serveSPA` streams files. History routing; non-asset
client routes (slide N, `presenter/N`) fall back to `index.html`. The browser hits
`/api/pages/{id}/deck/spa/...` → backend `requirePageRead` → proxies the sidecar's
`/spa` (same session cookie carries RBAC; Present opens in a new tab same-origin).

> **GOTCHA — Slidev nav doubles a non-root `--base`.** `getSlidePath()` prepends
> `import.meta.env.BASE_URL` to the path it `router.push()`es, but vue-router's
> history layer **also** prepends base on push. Under our sub-path `--base`,
> programmatic nav (next/prev/goto/presenter) produces a *doubled* base
> (`/api/pages/288/deck/spa/api/pages/288/deck/spa/2`) that matches no route →
> Slidev's NotFound → the "404 on slide 2" bug. `<RouterLink>` is unaffected (it
> pushes base-relative paths). **Fix:** the sidecar writes a Slidev `setup/main.mjs`
> into the build root (auto-discovered, runs with `{app, router}`) that installs a
> `router.beforeEach` guard stripping the duplicated base prefix; no-ops at root
> base. This affects *both* history and hash routing, so it's the only clean fix
> short of hosting at root. See `SPA_NAV_FIX` in `server.mjs`.

Cross-device presenter↔audience sync is **out of scope** by design — Slidev static
builds only sync same-device (BroadcastChannel/localStorage); cross-device needs a
dev server + `--remote`, which we don't run.

## Caching & invalidation

Everything is content-addressed and recomputable.

- **Cache key = `CACHE_EPOCH` (`RENDER_VERSION | THEME_VERSION`) + visual config +
  base + markdown.** Folding the installed theme version in means a **tahta bump
  auto-rebuilds every deck** (no manual `RENDER_VERSION` bump), and any source
  change via any path (editor/MCP/WebDAV/automation) yields a new key → **a stale
  slide can never be served.** Bump `RENDER_VERSION` only for *pipeline* changes
  (e.g. the nav-guard).
- **GC** (`gcSweep`): built SPAs + rendered frames are never overwritten, so edits
  and theme bumps orphan dirs. A periodic sweep caps total (`spa/` + `d/`) size
  (`DECK_CACHE_MAX_MB`, default 512), evicting least-recently-served dirs (in-mem
  `lastUsed`, mtime fallback), protecting dirs < 5 min old, and reaping stale
  `work/*.md` build entries. Eviction is safe — a dropped deck rebuilds on request.

## Pre-warm (instant Present)

A cold `slidev build` is ~5 s on the prod box, so it's pre-built off the click.
Correctness never depends on this (content-keyed); it only moves the cost.

- **Server-side** (`deck_warm.go`): `afterPageWrite` + `createPageCore` — the
  universal write chokepoint — schedule a warm for any deck whose body changed. So
  **every** save path warms uniformly: UI, MCP `update_page`, WebDAV/rsync, sync,
  automation. Debounced per page (reads the page fresh at fire time → always warms
  the latest content) + globally capped.
- **Frontend** (`PageView.tsx`): a fire-and-forget fetch of the SPA base on deck
  open (covers viewing, and re-warms decks after a deploy invalidates the cache —
  the server warm only fires on writes). Must sit with the other hooks **above**
  the early returns (a hook after a conditional return = React #310).

## Agent authoring — drift-proof guide

The whole reason agent-written decks used to be flat bullets: the rich palette was
never disclosed. Now tahta ships its **own** agent contract — `AGENTS.md`,
auto-generated from its `layouts.json`/`variants.json` (every rule, variant,
universal field, layout, component with fields/props/examples). The sidecar serves
it **verbatim** at `/authoring`; the backend frames it with a short tela preamble
("set `deck:true` + a `variant` prop; tela injects the theme") and serves the whole
thing as the `tela://deck-authoring-guide` MCP resource.

This is a deliberate **pass-through**: the theme is the single source of truth, so
the guide **cannot drift** as tahta grows — a new layout/component appears the
moment tahta is bumped, with zero tela changes (no typed field list, no codegen,
no vendoring). The authoring trigger fires on **"presentation" / "slides" / "deck"
/ "talk"** (any wording), not just "deck".

Agent loop tools: `lint_deck` (tahta's structural validator) and `preview_deck`
(renders frames to images so the agent can *see* its output and iterate). The
agent self-corrects via discover-guide → create → lint → preview → fix.

**Capability modules.** tahta 0.11.0 ships optional *capability modules*
(`modules/modules.json` + `branding.md`/`imagery.md`) — prompt fragments meant to
be appended only when relevant, so the always-served core guide stays lean. The
sidecar returns them in `/authoring`; the backend advertises each (id + `when` +
`adds`) at the bottom of the framed guide and serves the fragment on demand via
`deck_authoring_guide` with `module:"<id>"` (`mcp_deck_authoring.go`). Theme-owned
→ the module set grows with tahta, no tela change.

**Imagery — treat step.** tahta's posture is *it never generates images; the
operating agent does* — it ships only the recipe (`imagery.md`) + a deterministic
**treat** step (`tahta-imagine`): crop → scheme-aware duotone → grain → optional
scrim, reading the variant's palette. tela exposes it as the `treat_deck_image` MCP
tool (`mcp_deck_tools.go` → sidecar `POST /treat`, `sharp`): source must be an
existing **page attachment** (no arbitrary-URL fetch → no SSRF), the treated JPEG is
saved as a new attachment for a `bg:`/`image:` slot. Treatment is a *fallback*
(prefer rich on-palette images raw; never duotone a real-colour focal subject).

**Imagery — generation.** tahta's posture is "you bring the image model"; tela
supplies it via the **`generate_deck_image`** MCP tool (`mcp_deck_tools.go` +
`imagegen.go`). It POSTs an OpenAI-compatible Images request (`{url}/images/
generations` → `{data:[{b64_json}]}`) to an env-configured endpoint
(`TELA_IMAGE_GEN_URL`, e.g. an mflux/FLUX box) and saves the result as a page
attachment with a ready `![](…)` snippet. **Env-gated** (unset → 503) and honours
the **`ai.disabled` kill-switch** — same posture as RAG/LLM (`TELA_LLM_URL`); from a
Docker backend reach a private-overlay box by IP, not name. Generation and treatment
are separate steps: generate raw on-palette imagery, reach for `treat_deck_image`
only as a fallback. The imagery capability module (surfaced via `deck_authoring_
guide module="imagery"`) governs *what* to make and *where*.

## First slide = the deck's visual identity (cover / reader / OG)

A deck has no prose to excerpt or render, so the **first slide is its cover**
everywhere it appears as an image/link. The sidecar's `POST /cover` renders slide
1 only (`slidev export --range 1`, cheap, cached under `d/` like everything else);
`deckCover` proxies it. Used by:

- **Public index card** — `blogMetaFor` flags decks (`kind:"deck"`), skips the
  prose excerpter (which mangles Slidev source) in favour of the `summary`, and the
  tree handler sets the card cover to the public cover route. `PostCard` shows a
  play badge + "Presentation".
- **Public reader** — for a deck the reader renders `PublicDeckView` (first-slide
  hero + a **Present** button → the public SPA) instead of `MarkdownView`, so a
  public deck presents instead of dumping YAML/`---`.
- **OG share image** — `HandleOGImage` (`/p/{id}/og.png`) serves the first slide
  for decks (bounded + falls back to the generic card on a slow/failed render);
  the public reader's crawler OG already points here. Covers are pre-warmed
  alongside the SPA in `deck_warm.go` so these hit a cached render.
- **In-app** — `DeckOverview` shows the first slide as a clickable thumbnail via
  the gated cover route.

> **Privacy:** the OG image serves a **private** deck's first slide as a public
> image too (a deliberate product choice). The rest of a private deck stays gated —
> only slide 1, via `/p/{id}/og.png`, and only to someone with the link. If that
> ever needs to change, gate `HandleOGImage`'s deck branch on space visibility.

## Richness/quality guidance

> Richness/quality guidance (layouts-vs-components, pacing, empty-slide lint) lives
> on the **tahta** side (`docs/notes-from-tela.md` there) precisely because of the
> pass-through — improving the guide there improves tela's agent flow automatically.
