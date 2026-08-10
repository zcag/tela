<!--
  CONTENT.md — the LOCKED content/messaging contract for the tela marketing landing page.
  Source of truth for WHAT the page says and HOW it's phrased. Copy below is final and
  shippable — the build uses it verbatim (copywriting + voice skills enforce it).
  Content hierarchy here drives the visual hierarchy in DESIGN.md.
  STATUS: LOCKED. Repositioned 2026-06 around the RAG + MCP thesis, then re-centered
  2026-06-27 on the co-headline LOOP — Atlas (the wiki documents itself) + the agent layer
  (your AI reasons over it), co-stars at equal weight. See header notes.

  2026-06 REPOSITION (agreed with the user):
  - THESIS now: "RAG + MCP = superpowers on your data." The biggest win is that your
    team's knowledge becomes something your AI can actually reason over — semantic
    retrieval (RAG) + a real read/write API (MCP) — usable right inside Claude and
    ChatGPT, not just a local CLI proxy.
  - Self-host is NO LONGER a headline pillar. It survives as a short reassurance +
    "you can even self-host" escape hatch. The hosted instance is the primary path.
  - Security / org is the trust pillar that replaces the old self-host pillar.
  - FACT CORRECTIONS (the app changed; the old copy was stale):
      * Storage is PostgreSQL + pgvector — NOT "SQLite". NOT "single binary, no Postgres".
      * Keyword search is Postgres native full-text (tsvector + GIN, ranked) — NOT "FTS5".
      * MCP is a remote Streamable-HTTP server with OAuth — NOT just a local npx proxy.
      * 20 MCP tools — NOT 17.
  - These corrections are HARD: never reintroduce "SQLite", "FTS5", or "single binary".

  2026-06 COPY REFINEMENT (post-repositioning, agreed with the user):
  - Voice is PLAIN and capability-first — state what tela does; no story/sales framing
    ("Stop pasting…", "Made a mess?"), no cute tile labels ("Agents on a leash"), no hype.
  - User-facing VISUALS show real UI (a chat exchange, a search box) — NEVER JSON, tool-call
    code, or relevance scores. Users never see JSON, so the page doesn't either.
  - The agent angle leads with placement: "Already in Claude and ChatGPT" (tela goes to where
    your AI already is, vs a chat bolted into a wiki you'd have to open).
  - The landing COMPONENTS hold the authoritative final word-level wording; the section copy
    below captures intent/direction and may trail the components by a refinement pass.

  2026-06-10 UPDATE (shipped since the last landing pass — agreed with the user):
  - NEW SECTION "Ask your docs" (§3a, between Retrieval §3 and the pivot §4): tela's own
    first-party AI — a human asks in plain language in-app and gets an answer grounded in
    their pages WITH CITATIONS; agents ground the same way via the connector. Framed as its
    own thing (both human + agent), distinct from the Claude/ChatGPT connector story.
  - MCP tool count is now 24 (was 20): +4 knowledge-intelligence tools — related_pages,
    suggest_links, find_overlaps, knowledge_gaps (live on the hosted instance). Never write
    "20 tools" again — it's a stale fact now.
  - PRICING: tiers now also meter a monthly AI-answer allowance (Free 50 · Plus 3,000 · Team
    5,000 · Enterprise unlimited) AND a monthly Atlas INDEXING budget in GPU time
    (Free 3h · Plus 15h · Team 30h · Enterprise unlimited), raised/added 2026-08-10 — the metered dimension behind ask-your-docs. Every new
    account auto-starts a 30-day Plus trial. "Tiers change limits, not features" still holds:
    semantic search + ask-your-docs are on every plan; the monthly allowance is the limit.
  - FACT FIX: the live embedder is qwen3-embedding:0.6b (1024-d), not mxbai-embed-large.

  2026-06-27 REPOSITION (Atlas — the "documents itself" half of the loop):
  - CO-HEADLINE, ONE LOOP. The page now sells two co-equal halves of one loop:
    sources in → a wiki that documents itself and stays fresh (ATLAS) → your AI reasons
    over it (RAG + MCP) → humans collaborate on it. Atlas and the agent layer are co-stars
    at equal Tier-1 weight; the RAG/MCP story is NOT demoted — it's paired with Atlas.
    New hero promise (H1): "Docs that write themselves. An AI that already knows your project."
    (the loop in prose stays "documents itself … your AI reasons over it"; the H1 just reads punchier).
  - ATLAS is the public name of the capability (a named sub-brand, like "Linear Agents").
    What it is: a source-grounded, coverage-audited documentation generator. Point it at a
    source; it reads the source, plans a wiki, drafts cited markdown pages, scores how
    completely they cover the source's real surface, publishes them as ordinary tela pages,
    then watches the source and regenerates on a schedule when it drifts.
  - THE AI IS ONE COMPOSED ACT — now THREE beats, not four (2026-06-27 follow-up, agreed with
    the user): §2 ATLAS *writes & refreshes* the wiki → §3 ASK & SEARCH (find by meaning OR get a
    cited answer — tela's own AI) → §3b the AGENT LAYER (MCP), the *external* half (your agents in
    Claude/ChatGPT reason over the same knowledge). **Semantic search/retrieval is NO LONGER a
    standalone section** — it's a mechanism, not an outcome, and it overlapped Ask (Ask = retrieval
    + a written answer), so it's MERGED INTO §3 ASK & SEARCH and shown as "one engine underneath"
    (hybrid keyword + pgvector, heading-aware chunks). The old standalone §3 Retrieval component is
    deleted. All three beats Tier 1; Atlas and the agent layer carry equal headline weight. Nav:
    Atlas · Ask · Agents · Compare · Pricing (Search dropped — it lives inside Ask now).
  - FACT CORRECTIONS (verified against internal/api/mcp_tools.go + the live connector):
      * The MCP surface is now **39 tools** — NOT 24, NOT 20. (Atlas, deck, attachment, and
        sharing tools grew it.) Never write "24 tools" again.
      * There is **no `semantic_search` / `search_bodies` tool**. The live tools are `search`
        (keyword full-text, no embedder) and `research` (semantic, answer-oriented, cited).
        The agent's meaning/answer tool is `research` (+ `read_chunk`). `semantic_search` is a
        RETIRED name — never reintroduce it in copy or code blocks.
  - PRICING (§8b): Atlas reframed as a FLAGSHIP paid-tier ability (lede + Atlas-as-flagship band +
    first in the every-plan-includes list) — "tiers change limits, not features" still holds, and
    the per-tier Atlas generation volume is one of those limits. Dollar prices + the per-tier Atlas
    allowance are intentionally left as `[PRICE TBD]` / `[ATLAS LIMIT TBD]` placeholders for the
    user to set; do NOT invent numbers — fill them in (mirroring the backend `plans` table) later.
  - SOURCES = git + Jira ONLY (both fully implemented), with a quiet "more coming" note.
    NEVER imply a broad integration catalog, "any source", or "your whole stack". Naming
    exactly the two it reads is the point — the contract's claims stay falsifiable.
  - COVERAGE % is the differentiator — lean on it hard. Docs are measured against a
    deterministic spine (the routes, CLI flags, env vars, DB models, entrypoints, state
    extracted from the source); Atlas reports an objective coverage score + named gaps
    (kind, name, file:line) + validated file:line citations. "Measurably complete", not
    "the AI wrote something". This is the line that separates Atlas from ask-the-repo tools.
  - FRESHNESS is real but precise: cheap no-clone drift detection ~every 15 min (git
    ls-remote / a Jira count — no regen, no cost) + scheduled regeneration (hourly / daily /
    weekly / monthly / off, delta runs over changed files). "Documents itself / stays current"
    must cash out as THESE mechanisms — never as bare hype, never "always up to date / instant".
  - BANNED Atlas overclaims (hard): "any source / your whole stack / connects to everything"
    (git + Jira only) · "auto-generated diagrams" (it validates Mermaid, doesn't author them) ·
    "edit freely and Atlas merges your changes" (a re-run REWRITES the generated subtree; older
    revisions can recover an edit, but the human-merge contract is DEFERRED — sell Atlas pages as
    the generated, kept-fresh layer) · "zero-setup / works out of the box on self-host" (needs an
    LLM endpoint AND an embedder) · "custom model per project" (uses the instance LLM) · "instant".
  - RIPPLES: §7 Confluence row reworked (tela now documents FROM Jira via Atlas; Confluence's deep
    two-way Jira WORKFLOW integration is still genuinely better) · §8b Atlas draws on the managed-AI
    path (no separate meter; BYO LLM on self-host) · §10 +2 Atlas FAQs · nav gains an Atlas slot.
-->

# Content Contract — tela  (LOCKED)

One-page marketing landing page. Standalone, anchored sections. Hosted instance + product: https://telawiki.com

---

## Positioning  (Dunford — 5 components)

- **Competitive alternatives:** Notion / Confluence (closed SaaS wikis — proprietary block store, "AI" bolted on as a chat sidebar, no real agent read/write); a folder of markdown in a git repo + grep; Obsidian/Logseq (single-player, local); "we keep docs in Slack/Google Docs and can't find anything." For the *documentation* angle, the alternative is *writing and maintaining the docs by hand* (so they're perpetually half-written and stale), or a generic "ask-the-repo / chat-with-your-codebase" tool that answers in a chat box but never produces a maintained, shareable, coverage-checked wiki. For the agent angle, the alternative is *pasting context into the chat by hand every session* and hoping the model finds the right doc by keyword.
- **Unique attributes (+ proof):**
  - **The wiki documents itself from your sources — and proves how complete it is.** Point **Atlas** at a git repo or a Jira project; it reads the source, plans a wiki structure, drafts cited markdown pages, and publishes them as ordinary tela pages — then measures those pages against a *deterministic spine* (the actual routes, CLI flags, env vars, DB models, entrypoints, and state it extracted from the source) and reports an objective **coverage score** with the specific gaps named (kind, name, `file:line`) and every citation resolved to `file:line`. It then probes the source for drift roughly every 15 minutes and regenerates on a schedule when it moves ahead. *Proof: the spine + coverage score + named gaps are how a run is judged; generated pages carry validated `file:line` citations and are tagged `generator=atlas`; `atlas_*` MCP tools exist. Two sources today (git + Jira); more coming.*
  - **Your agents search your docs by meaning, not just keywords.** tela chunks every page (heading-aware), embeds it, and serves **hybrid retrieval** — keyword (Postgres full-text) and vector similarity (pgvector) fused with reciprocal-rank fusion. Agents call `research` and `read_chunk` to pull the *right section*, with citations, instead of the whole document. *Proof: the RAG service is open (`internal/rag`); `research` is a live MCP tool.*
  - **A real remote MCP server — usable inside Claude and ChatGPT.** Not a local CLI shim. `https://telawiki.com/api/mcp` is a Streamable-HTTP MCP server with OAuth 2.1 sign-in (one click, no token to paste) and **39 scoped tools** that wrap the same API the UI uses — including knowledge-intelligence tools (`related_pages`, `suggest_links`, `find_overlaps`) that keep the wiki connected. Submitted to the Claude and ChatGPT connector directories. *Proof: the OAuth + tool surface is open (`internal/api/mcp*.go`); the connector signs you in with your tela account.*
  - **Markdown is canonical forever — under a block editor that feels like Notion.** `pages.body` is plain markdown text; there is no block table, no proprietary format. The editor is full block-editing — drag-to-reorder, slash menu, turn-into, callouts, tables, diagrams — and every block operation round-trips straight back to clean markdown. *Proof: "no block table" is an architectural rule; drag = a markdown line reorder; bulk import/export is first-class.*
  - **Secure and team-shaped out of the box.** Single sign-on (WorkOS), email-verified accounts (Argon2id), organizations and sub-team groups, per-space roles with hard invariants, and scoped API keys that are HMAC-stored, expiring, space-pinnable, and fully audited. *Proof: the access model is documented (`docs/access-model.md`) and the auth/key code is open.*
  - **Real multiplayer + comments that survive edits.** Live collaborative editing over Yjs, rebased onto the canonical markdown on save; comments anchor to a `{prefix, exact, suffix}` text window so they don't drift when the doc is reflowed. *Proof: collab transport in `lib/collab`; anchoring model in architecture.md.*
- **Value themes (5):** (1) Your docs write themselves from your sources and stay fresh — cited and coverage-checked (Atlas). (2) Your AI reasons over that knowledge — by meaning, with citations. (3) It works where you already work — inside Claude and ChatGPT. (4) Your knowledge stays plain markdown you can take with you. (5) Secure, team-shaped, hosted for you — or self-hosted if you want.
- **Who cares most (ICP):** Small-to-mid technical teams and AI-forward builders who already work *with* agents (Claude, ChatGPT, Cursor, Claude Code) every day, want a shared knowledge base those agents can actually search and write, and are tired of re-pasting context and getting keyword-miss answers. Circumstance: they've put agents on real work and hit the wall where the agent has no durable, searchable, shared memory of the team's docs — *and* their docs are perpetually half-written and out of date because nobody has time to write them.
- **Market category:** "Agent-native team wiki." The familiar frame is *team wiki*; the wedge is *one loop* — the wiki **documents itself** from your sources (Atlas) *and* your AI **reasons over it** (semantic retrieval + MCP, from inside the chat apps). **Style:** new-category wedge inside a known category — lead with the loop (a wiki that writes itself and that your AI reasons over), anchor on the known thing (wiki).
- **Why now:** Agents went mainstream in 2025–26 and MCP became the default way to give them tools and data; remote MCP connectors landed in Claude and ChatGPT. Teams now have agents in the browser but no shared place those agents can semantically retrieve and persist team knowledge. tela is that place.

## The customer  (JTBD + VPC)

- **Job story:** When my team is running AI agents on real work but our docs are half-written and stale, I want a shared wiki that *writes itself from our actual sources and stays current*, and that my agents can *search by meaning and write back to* — from inside Claude or ChatGPT, without me hand-pasting context — so the agent reasons over real, fresh docs and the next session picks up where the last left off.
- **Ranked pains (top → nice-to-have):** our docs are half-written and stale because nobody has time to write them · the agent can't see our knowledge, so I re-paste context constantly · keyword search misses the doc that actually answers the question · "AI" in our wiki is a shallow chat sidebar, not real retrieval + write access · the agent lives in a CLI, not the Claude/ChatGPT app my team uses · docs locked in a proprietary block format · I don't want a free-for-all on who can read what.
- **Ranked gains (top → nice-to-have):** docs that generate themselves from our sources and stay current — measurably, with the gaps named · an agent that retrieves the right section by meaning and cites it · read/write access from the chat app we already use · knowledge in plain markdown we own · SSO, roles, and an audit trail so sharing is safe · real-time multiplayer for the humans · take-it-with-you export, and self-host if we ever want to.
- **Forces:** push `docs nobody has time to write, stale the day after; agents that can't see our docs; keyword search that misses; re-pasting context every session` · pull `a wiki that generates and refreshes itself from the source with a coverage score; semantic retrieval over it; a real MCP connector in Claude/ChatGPT; markdown we own; SSO + roles` · habit `Notion/Confluence is already set up; markdown-in-git works fine; we just live with stale docs` · anxiety `is the generated wiki real and accurate, or hallucinated? is the retrieval real or a demo? is my data secure and access-controlled? will I get locked in?`

## Core message & hierarchy

- **ONE core message (hero promise):** *The team wiki that documents itself — point it at your sources and it writes a cited, coverage-checked wiki that stays fresh — and that your AI reasons over by meaning, right inside Claude and ChatGPT, on markdown you own.*
  - Parity test ("who else could say this?"): Notion AI can't (no source-grounded, coverage-scored generation from your repo/Jira; no semantic MCP read/write for your agent). A "chat-with-your-codebase" tool can't (it answers in a box; it never produces a measured, maintained, shareable wiki your team *and* agents both use). A git-repo-of-markdown can't (no generation, no retrieval, no agent API). Confluence/Obsidian can't. It passes.
- **One-liner (BrandScript):** Technical teams running AI agents struggle because their wiki is half-written and stale and the agent can't really search or write it; tela points **Atlas** at their sources to generate a cited, coverage-checked wiki that stays fresh, and serves it through semantic retrieval and a built-in MCP server — so their agents reason over the team's real docs, by meaning, with citations, from inside Claude and ChatGPT, instead of documenting by hand and starting from zero every session.
- **Supporting pillars (5 — each → one page section):**
  1. **Atlas — docs that write and refresh themselves.** Point it at a git repo or a Jira project; it drafts cited markdown pages, scores them against the source's *real* surface (a coverage %, with the gaps named at `file:line`), publishes them as tela pages, and regenerates as the source drifts. Two sources today (git + Jira); more coming. Proof: deterministic spine + coverage score; validated `file:line` citations; `atlas_*` MCP tools.
  2. **The agent layer — MCP, inside Claude & ChatGPT.** Remote Streamable-HTTP MCP server, OAuth one-click connect, 39 scoped tools over the same API. Proof: open MCP code; live connector.
  3. **Retrieval that reasons — semantic + keyword, fused.** Heading-aware chunking, embeddings, hybrid (pgvector + Postgres full-text) RRF retrieval; agents pull the right section with citations. Proof: open `internal/rag`; `research` tool.
  4. **A real wiki underneath.** Notion-grade block editing on canonical markdown; live multiplayer; text-anchored comments; history; sharing. Proof: "no block table" rule; collab transport.
  5. **Secure, team-shaped, yours.** SSO, orgs + groups, per-space RBAC, scoped + audited keys; hosted for you, self-host if you want. Proof: open access model + auth code.
- **Awareness stage (dominant visitor):** **Solution-aware → Product-aware.** They want a wiki and they want their agents to use it; they don't yet know one tool can *both* generate that wiki from their sources (and keep it fresh, with a coverage score) *and* let their agents reason over it via semantic retrieval + a real MCP connector in Claude/ChatGPT. → **Page leads with the loop + immediate proof** (the source resolving into a covered wiki, the agent reasoning over it from Claude/ChatGPT, the retrieval, the tools).
- **Objections → reassurance:**
  - "Is the generated wiki real, or hallucinated filler?" → Atlas drafts only from the source, resolves every citation to `file:line`, and scores coverage against a deterministic spine of the source's real surface — naming the gaps it didn't cover. You can read what each page is grounded in. *Show the coverage score + the gap list.*
  - "Won't a re-run overwrite my edits?" → Atlas pages are the machine-maintained layer; a re-run rewrites the generated subtree (older revisions can recover an edit). Frame them as the generated, kept-fresh layer, not a doc you hand-maintain — write your own pages alongside. *Be honest; don't sell a merge.*
  - "What can Atlas read?" → git repos and Jira projects today; more sources coming. *Name the two, not a catalog.*
  - "Is the retrieval/agent integration real, or a demo?" → Show the connect flow, the 39-tool catalog, `research` returning a cited chunk; link the open code and the live connector. *Show, don't claim.*
  - "Is my data secure / access-controlled?" → SSO, email-verified accounts, orgs + groups, per-space roles, scoped + audited keys, SSRF-hardened import. Recent independent-style security pass.
  - "Will I get locked in / lose my data?" → Canonical plain markdown, bulk export, and you can self-host the whole thing. Leaving = copying files.
  - "Is this mature?" → Open code, a live instance you can use now, a versioned connector. Honest about stage (v0) rather than inflating.
- **Cut list (parity / table-stakes — demote or drop):** generic "powerful editor", "beautiful UI", "boost productivity", uptime/perf adjectives without numbers, self-host-as-headline, anything Notion could also say. **Banned facts (stale):** "SQLite", "FTS5", "single binary", "no Postgres". **Banned Atlas overclaims:** "any source / your whole stack / connects to everything" (git + Jira only) · "auto-generated diagrams" (validates Mermaid, doesn't author) · "edit freely and Atlas merges your changes" (a re-run rewrites the generated subtree) · "zero-setup / works out of the box on self-host" (needs an LLM endpoint *and* an embedder) · "custom model per project" (uses the instance LLM) · "always up to date / instant" (drift-detected ~15 min + scheduled, not instant). "Documents itself" / "self-documenting" must always cash out as the concrete mechanisms (reads the source, drafts cited pages, scores coverage, detects drift, regenerates on a schedule) — never bare hype.

## Voice  (constant) & Tone (flexes by surface)

**We are precise, dev-credible, and quietly confident — we are NOT hypey, salesy, or vague.**

Write like an engineer wrote it for other engineers: claims are falsifiable, specifics over adjectives, show the thing instead of describing it. Confidence comes from proof, not volume. (The internal shorthand "superpowers on your data" is the *spirit*; in copy it shows up as concrete mechanisms — semantic retrieval, 39 tools, a connector you click — never as the word "superpower".)

| Trait | Do | Don't |
|---|---|---|
| Precise | Name the real thing: "39 MCP tools", "hybrid retrieval", "pgvector", "`tela_pat_`", "`research`". | Round off into vibes ("tons of integrations", "blazing fast", "supercharge"). |
| Dev-credible | Show the connect flow, the tool catalog, a cited chunk. Link the open code. | Marketing screenshots with fake data; claims you won't show. |
| Confident, low-hype | State the differentiator flatly and let proof carry it. | Exclamation marks, "revolutionary", "superpower", "the future of…". |
| Concrete | Every benefit ties to a mechanism the reader can verify. | Abstract outcomes ("work smarter", "unlock productivity"). |
| Honest | Say "v0", "open", "semantic search needs an embedder endpoint" plainly. | Imply scale/maturity that isn't there; fake logos. |

- **Tone dimensions:** formal↔casual `mid — relaxed but technical, never corporate` · serious↔funny `serious; at most one dry aside` · respectful↔irreverent `respectful, lightly opinionated (closed SaaS is a fair foil)` · matter-of-fact↔enthusiastic `matter-of-fact; enthusiasm shows as specificity, not adjectives`
- **Vocabulary:**
  - **Use:** agent-native · MCP server · semantic search · hybrid retrieval · retrieval-augmented · embeddings · pgvector · markdown-native · canonical markdown · block editing · multiplayer · scoped PAT · single sign-on · orgs / groups / roles · audit · spaces · read, write, and search · your data · **Atlas** · source-grounded · coverage score / coverage % · deterministic spine · named gaps · `file:line` citations · drift detection · scheduled regeneration · delta run · git + Jira sources · documents itself / self-documenting (only when cashed out as mechanism).
  - **Brand motif (sparing):** *tela* = fabric/canvas/woven cloth. A woven-grid metaphor may appear at most once if it earns its place. Never force it; never explain the etymology in body copy.
  - **Ban — anti-slop kill-list (hard):** revolutionize · seamless · unleash · supercharge · superpower(s) · game-changer · unlock · elevate · empower · leverage · robust · cutting-edge · best-in-class · world-class · next-level · transformative · innovative · "solutions" (noun) · ecosystem · synergy · holistic · curated · turnkey · "in today's fast-paced world" · "take your X to the next level" · "we help teams grow" · "the possibilities are endless" · "not just a wiki, it's…" · em-dash confetti · forced rule-of-three. **Plus the stale-fact ban:** SQLite · FTS5 · "single binary" · "no Postgres".

## Information architecture

- **Site type:** single-page marketing landing (developer-tool tier — Linear/Vercel register). **In-page anchor nav (≤6, in scroll order — the AI act leads):** Atlas · Ask · Agents · Compare · Pricing (Pricing is the dedicated-page link; Security stays reachable by scroll). Pinned header: wordmark + GitHub link + theme toggle + primary CTA (`Get started`) + secondary (`Log in`).
- **Page:** one page. **Dominant search intent:** commercial-investigation / navigational ("MCP wiki", "wiki Claude can search", "RAG wiki for agents", "ChatGPT connector wiki", "agent-native wiki", "auto-generate docs from a repo", "self-updating documentation from git", "documentation generator from codebase", "tela").

## Page plan  (content model — visual hierarchy MUST mirror this priority)

Section order is the narrative arc. Tier = visual prominence (1 = hero/max, 4 = footer/min).

**The arc is one composed loop — and the AI is one act, not four sibling features.** The hero states the loop; the **AI act (§2–§3b)** then plays it out as a single system, in source-to-answer order:

- **§2 Atlas** *writes & refreshes* the wiki from your sources — coverage-checked, cited. *(the "documents itself" half)*
- **§3 Retrieval** is the **shared engine** that *indexes* every page for meaning the moment it's written.
- **§3a Ask your docs** is tela's own AI *answering* from that index — cited, abstaining when the docs don't cover it.
- **§3b The agent layer (MCP)** is the **external** half: the agents you already run in Claude & ChatGPT reason over the same knowledge.

Read it as: **Atlas + Ask = tela's first-party intelligence** (both grounded-and-cited, same DNA — one writes, one answers); **Retrieval is the engine under all of it**; **the MCP connector is the "works where you already work" beat.** All four are Tier 1 and must read as one continuous act — Atlas and the agent layer carry equal weight as the two named halves of the headline. Then **§4** pivots to the real wiki underneath, and the rest is proof. Visual hierarchy MUST treat §2–§3b as one prominent, composed Tier-1 act, not four stacked feature blocks.

### 1. Hero  — Tier 1  (BAB: the after-state, stated flatly)
- **Eyebrow:** `Agent-native team wiki · documents itself · in Claude & ChatGPT`
- **Headline (H1):** `Docs that write themselves. An AI that already knows your project.`  (two-sentence; accent the two halves: "write themselves" and "already knows your project". Name the referent — "your project" (code + Jira), not a vague "them". The eyebrow carries "team wiki"; the subhead carries the proof. Avoid the clunkier "reasons over" framing.)
- **Subhead:** `Point tela at your sources — a git repo, a Jira project — and Atlas writes a cited, coverage-checked wiki, then keeps it fresh as they change. Your agents search that wiki by meaning and read, write, and cite it from inside Claude and ChatGPT. Real-time editing for the humans; SSO, scoped access, and an audit trail for the team.`
- **Signature / wow moment (described for the build):** A looping *one-loop* moment beside the hero. A source feeds in on the left — a git repo node (or a Jira project) — and the woven-grid threads light up and resolve left-to-right into a freshly written tela page: cited markdown lines with `file:line` citations and a small **coverage badge** (e.g. `coverage 92%`). Then a chat-style turn shows an agent in Claude/ChatGPT calling `research` over that very page and citing it back. It must read in under 5 seconds as *"the source becomes a covered wiki, and the AI reasons over it — one loop."* Real names (a repo URL, real tool names, a `tela_pat_…`/connector shape), no fake data. (Carved-out signature moment — hand to the `wow` skill; DESIGN.md owns the visual.)
- **Primary CTA:** `Get started` → https://telawiki.com (hosted, free to start).  **Secondary CTA:** `Add to Claude or ChatGPT` → the MCP/connect section (`#agents`).
- **Friction microcopy under CTA:** `Free to start · your markdown, exportable anytime · self-host if you'd rather.`
- 5-second test: a stranger learns *what* (markdown team wiki) + *why it's different* (it writes itself from your sources and stays fresh, and your AI reasons over it from Claude/ChatGPT) in one glance.

### 2. Atlas — "Point it at a repo. Get a wiki that stays current."  — Tier 1  (CO-STAR; opens the AI act; the "documents itself" half of the loop; FAB)
- **Purpose:** The opening beat of the AI act and the "documents itself" clause of the headline — where the docs *come from*. Atlas writes the wiki that the rest of the act then indexes, answers from, and reasons over. The freshest news on the page; give it full Tier-1 prominence, equal to the agent layer.
- **Headline (H2, question-led for AEO):** `Where do all these docs come from?`
- **Answer-first block (40–60 words):** `Atlas writes them. Point Atlas at a source — a git repo or a Jira project — and it reads the source, plans a wiki structure, drafts cited markdown pages, and publishes them into a space as ordinary tela pages. Then it watches the source and regenerates when it moves ahead. The pages are real tela pages — searchable, shareable, revisioned — the same wiki your agents reason over.`
- **Connect a source (3 steps, real):**
  1. `Point Atlas at a source — a git repo (public, or private with a token) or a Jira project. Optional branch, subpath, include/exclude globs.`
  2. `Atlas reads the source and extracts its real surface — routes, CLI flags, env vars, DB models, entrypoints, outbound services, config (or, for Jira, the issues, schema, and current state) — then drafts grounded, cited pages and publishes the page tree.`
  3. `It probes the source for drift about every 15 minutes and regenerates on a schedule you set — only the sources that actually changed.`
- **The coverage proof (THE STAR detail — lean on it):** `Atlas doesn't just write some pages and call it done. It measures the docs against a deterministic spine — the actual routes, flags, env vars, models, and state it extracted from the source — and reports a coverage score: the fraction of that real surface the docs cover, with the specific gaps named (kind, name, file:line) and every citation resolved to file:line. Measurably complete — not "the AI wrote something".`
- **Freshness mechanism (stated honestly):** `Always-on drift detection runs a cheap no-clone probe (a git ls-remote, or a Jira count) about every 15 minutes and flags when a source has moved past the last generated version — no regeneration, no cost. Scheduled regeneration (hourly, daily, weekly, monthly, or off) then rebuilds only the drifted sources, as a delta run over just the changed files once a baseline exists. It detects drift cheaply and regenerates on a schedule — fresh, not magic-instant.`
- **Credentials note (real):** `git and Jira tokens are named, reusable credentials owned by a user or an org — write-only over the API, never written to a page or a log. A project can use an org credential or a teammate's personal one, so you can lend a private-repo token to an org project without putting it in a shared pool, with nothing left behind if they leave.`
- **One-loop tie-in (connects Atlas into the agent story):** `Atlas is in the loop with the agent layer, too: MCP tools (atlas_list_projects, atlas_run, atlas_run_status) let an agent in Claude or ChatGPT kick off and check a run — not just read the result.`
- **More-sources honesty line (load-bearing):** `Two source types today — git and Jira. More are coming. We name what Atlas reads rather than imply a catalog it doesn't have.`
- **Show it (visual for the build):** the connect-a-source flow as 3 crisp steps, then a generated tela page (real-feeling cited markdown with `file:line` citations) beside a **coverage panel** — a coverage score (e.g. `coverage 92%`) and a short named-gap list (`kind · name · file:line`). Use the same incident/deploy theme as §3 (e.g. documenting a deploy surface). Real-feeling content, never lorem, never a fake-integration logo wall. No JSON.
- **Honesty note (build + copy):** Atlas needs **an LLM endpoint AND an embedder** — on the hosted instance both are on; self-hosting, you point it at your own. Never imply zero-setup on a fresh self-host. Generated pages are the **machine-maintained** layer: a re-run rewrites the generated subtree (older revisions can recover a hand edit, but don't sell "edit freely and it merges"). Triggering/configuring Atlas is gated to **space managers** (owner / org admin / instance admin) — a run spends LLM budget and rewrites a managed subtree; generated pages respect the same per-space access as everything else. Atlas validates Mermaid blocks; it does **not** author diagrams.
- **CTA (transitional):** `See how Atlas works` → `docs/` (or `/atlas`).

### 3. Retrieval — "The engine underneath: search by meaning."  — Tier 1  (the shared engine of the AI act; FAB)
- **Purpose:** The connective beat — the shared engine the whole act runs on. Atlas drafts against it, ask-your-docs answers from it, and your agents call it. Keyword search misses; tela retrieves the right section by meaning. Make the engine concrete, then hand off to "ask" and the agents who use it.
- **Headline (H2, question-led):** `How does search work?`
- **Answer-first block:** `Two ways, fused. Every page is split into heading-aware chunks, embedded, and indexed. A query runs keyword full-text (Postgres) and vector similarity (pgvector) in parallel, then reciprocal-rank fusion blends them — so a search for "rollback steps" finds the runbook section that never says "rollback". Agents call research to get ranked chunks with citations, then read_chunk for the full section.`
- **Show it (visual for the build):** a query → ranked **chunk** results, each with its `heading path` (e.g. `Deploy ▸ Production`), a snippet, and a score; a small note that the same retrieval powers the human command palette *and* the agent's `research` tool. Real-feeling content, never lorem.
- **Two-line "for humans / for agents" split:**
  - **For humans:** `Instant search in the command palette — titles, bodies, and meaning. Jump anywhere.`
  - **For agents:** `research + read_chunk over the same index — the agent pulls the section that answers the question, with a citation back to the page.`
- **Honesty note (build + copy):** semantic/vector retrieval runs against an embedding endpoint you point tela at (an Ollama-compatible embedder; `qwen3-embedding:0.6b`, 1024-d, in the live instance). Keyword full-text needs nothing extra. Say this plainly — "semantic search needs an embedder endpoint; on the hosted instance it's already on." Never imply embeddings run with zero setup on a fresh self-host.
- No CTA (momentum carries to ask-your-docs, then the agent layer, then the pivot).

### 3a. Ask your docs — "Ask a question. Get an answer from your own pages."  — Tier 1  (tela's first-party AI — the *answer* half; FAB)
- **Purpose:** tela's *own* AI, the **answer** half of its first-party intelligence (Atlas being the **write** half) — same grounded-and-cited DNA, on the shared retrieval engine. A human asks in plain language in-app and gets a written answer grounded in their pages, **with citations**. This is tela answering for itself, before the external agents (§3b) do the same.
- **Headline (H2, question-led):** `Ask a question. Get an answer from your own pages.`
- **Answer-first block:** `A step past search: ask in plain language and tela pulls the relevant sections, then writes a direct answer with citations — grounded only in pages you're allowed to read. When your docs don't cover it, it says so instead of guessing.`
- **Show it (visual for the build):** an "ask" card — the question at top, a written answer with inline `[n]` citation markers, then the cited **source pages** listed below (page title ▸ heading · space). Real-feeling content (the rollback/incident theme, consistent with §3), never lorem. No JSON, no scores.
- **Two front doors (both framing — the user's call):**
  - **For your team:** `Ask inside tela — a question box over every space you can read; you get a written answer and links to the exact pages it came from.`
  - **For your agent:** `Claude and ChatGPT ground their answers the same way through the connector — pulling the relevant sections and citing the source page, scoped to your access.`
- **Honesty note (build + copy):** managed AI is included on the hosted instance, **metered by the plan's monthly answer allowance** (see §8b); self-hosting, bring your own — point tela at any OpenAI-compatible LLM. The model answers strictly from retrieved excerpts and abstains when they don't cover the question. Never imply ask-your-docs runs with zero setup on a fresh self-host (it needs an embedder *and* an LLM).
- No CTA (momentum carries to the agent layer, then the pivot).

### 3b. The agent layer — "Use it inside Claude and ChatGPT."  — Tier 1  (closes the AI act; the EXTERNAL half — "works where you already work"; FAB)
- **Purpose:** Close the act. Atlas wrote the wiki (§2), retrieval indexed it (§3), tela's own AI answered from it (§3a) — now the *external* half: the agents your team already runs in Claude and ChatGPT reason over the same knowledge, scoped to your access. The "works where you already work" differentiator — keep it a strong, standalone beat.
- **Headline (H2, question-led for AEO):** `What can an agent actually do with tela?`
- **Answer-first block (40–60 words):** `Everything your team can. tela runs a remote MCP server with 39 scoped tools over the same API the UI uses: search by meaning, read pages and sections, create and update, move, comment, surface related pages, manage spaces. Connect it in Claude or ChatGPT with one OAuth sign-in — no token to paste — or point any MCP client at the same URL.`
- **Show the connect flow (3 steps, real):**
  1. In Claude or ChatGPT, add a connector → `https://telawiki.com/api/mcp`
  2. Sign in with your tela account (OAuth 2.1 — PKCE, no token pasted)
  3. The agent now searches, reads, and writes your wiki — scoped to what your account can see.
  - Caption: `Submitted to the Claude and ChatGPT connector directories.`
- **Show the config for code agents (code block — modern remote transport, NO npx):**
  ```json
  {
    "mcpServers": {
      "tela": {
        "url": "https://telawiki.com/api/mcp",
        "headers": { "Authorization": "Bearer tela_pat_..." }
      }
    }
  }
  ```
  - Caption: `For Claude Code, Cursor, or your own agent. Scoped token, same server.`
- **Tool catalog (compact, real names — pick ~9 to show, group by scope):**
  - `read` — `research` (meaning + keyword, fused) · `search` (full-text, ranked) · `read_chunk` · `related_pages` · `suggest_links` · `get_page` · `list_pages` · `list_backlinks`
  - `write` — `create_page` · `update_page` (auto-snapshots a revision) · `add_comment` (text-anchored) · `move_page`
  - `admin` — space + key management · `find_overlaps` / `knowledge_gaps` (knowledge-intelligence) · `atlas_run` / `atlas_run_status` (kick off & check an Atlas run)
  - Footnote: `39 tools total. Keys are scoped read / write / admin and can be pinned to a single space. Interactive result cards render in-chat.`
- **Why-you-care line (FAB benefit):** `Your agent stops starting from zero. It retrieves the right section of the team's docs by meaning, cites it, writes back what it learns — and the next session, human or agent, picks up there.`
- **CTA (transitional):** `See the tool catalog` → `mcp/README.md` (or `/mcp`) on the site/GitHub.

### 4. Not-just-AI pivot — "The loop only matters because the wiki is real."  — Tier 2  (reassurance pivot)
- **Purpose:** The skeptic just saw the whole AI act (Atlas, retrieval, ask, agents) and is bracing for "an AI gimmick on a thin note app." Disarm it: Atlas, the MCP server, and RAG all sit *on top of* a real, well-built wiki. One beat, then proof.
- **Headline (H2):** `Atlas and the agent layer are the new parts. The wiki under them is the part that's done.`
- **Body (1–2 lines):** `tela isn't an AI feature looking for a wiki to live in. It's a real markdown wiki — block editing, multiplayer, comments, history, sharing — and Atlas and the agent layer are ways into it. Take all the AI away and you still have a wiki you'd want to use.`
- **Transition line into the editor/showcase:** `Here's what's actually in the box.`

### 5. The editor — "Block editing. Plain markdown underneath."  — Tier 2  (visual proof; the big editor win)
- **Purpose:** Show the new block-editing experience and resolve the apparent tension with "markdown canonical."
- **Headline (H2):** `Edits like a block editor. Saves like a markdown file.`
- **Body:** `Drag a block to reorder it. Hit / for a slash menu — headings, lists, tasks, callouts, tables, code, diagrams, math. "Turn into" changes a block's type in place. It feels like Notion. The difference: every block operation round-trips to clean markdown on disk. Reordering a block reorders markdown lines; there is no block table, no proprietary format to escape from.`
- **Detail chips (real, shipped):** `slash menu` · `drag-to-reorder` · `turn-into` · `callouts` · `tables` · `task lists` · `Mermaid` · `KaTeX math` · `Excalidraw inline` · `paste-to-unfurl`.
- **Visual:** the tela editor with a block being dragged / the slash menu open, beside the same content as raw markdown — the "WYSIWYG ↔ markdown" equivalence shown, not told. Real content.
- No CTA.

### 6. Feature showcase — "What's actually in the box."  — Tier 2  (scannable bento, FAB cells)
- **Purpose:** Prove the pivot. A scannable grid of real capabilities. Cells carry a `real` | `planned` flag; planned cells get a muted style + a small **Planned** tag and never imply they ship today. ~9 cells. As of 2026-06-08 **all cells are `real`** (the graph shipped; the planned Templates cell was replaced by the shipped local-sync cell).
- **Cells (title · one-line description · flag):**
  - **`real`** — **Real-time multiplayer.** `Live cursors and edits over Yjs, rebased onto canonical markdown on save.`
  - **`real`** — **Comments that don't drift.** `Comments anchor to the surrounding text (prefix · exact · suffix), so they stay put when the page is reflowed.`
  - **`real`** — **Block editing → markdown.** `Drag, slash, turn-into — Notion-style editing; the file underneath stays clean markdown.`
  - **`real`** — **History & one-click revisions.** `Every change auto-snapshots. Read any version, diff it, roll back.`
  - **`real`** — **Diagrams & math inline.** `Excalidraw renders in the page; KaTeX math and Mermaid round-trip as markdown.`
  - **`real`** — **Bring your markdown — and take it back.** `Import a directory; export anytime. Plain files in, plain files out.`
  - **`real`** — **Share links, your way.** `Publish any page at a public link with optional password gating and expiry.`
  - **`real`** — **The link graph.** `Wikilinks and backlinks draw a live graph of how pages connect — whole-space or local to the current page.` (shipped 2026-06)
  - **`real`** — **Edit in your own editor.** `Mount a space as a local folder over WebDAV and sync with rclone — Obsidian, VS Code, anything. Round-trips as plain markdown, attachments and all.`
- **Honesty note for the build:** `planned` cells MUST be visually distinct (muted + a "Planned" tag) and never styled identically to shipped cells. Do not promote a planned feature without updating this contract.
- No CTA (momentum carries to the comparison).

### 6b. Spreadsheets — "Spreadsheets, built in."  — Tier 2  (added 2026-07: sheets shipped; THE standalone capability, a sibling of Presentations, not a page trick)
- **Purpose:** Surface spreadsheets as a **standalone big feature tela provides** — not "any page can hold a table" (that framing sells it as a minor page trick; it isn't). tela makes real spreadsheets: live formulas, conditional formatting, number formats, CSV/XLSX in and out, real-time multiplayer — and, the differentiator, **your agent builds the whole sheet for you** from Claude/ChatGPT. **Page order: rendered right after the feature showcase (§6), BEFORE Presentations (§6a)** — the two special page-types sit together (docs → sheets → decks), high on the page because each is a headline ability, not a footnote.
- **Copy rules (load-bearing):**
  - Frame it as a **capability tela has** ("tela makes spreadsheets"), never as a per-page conversion ("any page is a spreadsheet"). Mirror the Presentations framing exactly — they are siblings.
  - **Never sell the engine** — the grid/formula engine's package name means nothing to a buyer and reads as leaking internals, so it never appears in the headline, body, capabilities, or mock chrome (say "spreadsheet / sheet / formulas"). "Markdown" is fine to name (page-wide pillar, widely understood). **Exception:** a small muted **"Powered by defter"** credit line at the very bottom of the section with an outbound link — attribution, not a selling point (parallels the Slidev/tahta credit on §6a).
  - Lead with the universal words **"spreadsheet" / "sheet" / "formulas"**.
  - **Charts: authored, not yet rendered in-grid.** The sheet *format* supports a chart declaration and the underlying library ships a chart component, but tela's grid view does **not** render charts inline today (no `DefterChart` in the app). So charts are **not** claimed as a headline capability and the showcase does **not** show one — honesty rule. Revisit if/when tela wires chart rendering into the grid.
- **Headline (H2):** `Spreadsheets, built in.`
- **Lede:** `tela makes real spreadsheets — live formulas, conditional formatting and number formats, with CSV and XLSX in and out. And because tela is agent-native, your agent can build the whole sheet for you, from scratch: ask Claude or ChatGPT to "build a Q3 marketing budget — a row per channel with spend against each cap, a totals row, and flag anything over" and it lays out the columns, writes the formulas, and styles it.` **The agent example must show authoring a real sheet FROM SCRATCH (structure + formulas + styling from a plain-language ask), not a CSV import/conversion — "turn this CSV into a sheet" undersells it as a basic import.**
- **Visual (showcase the quality — show, don't describe):** use a **real screenshot of the live spreadsheet grid**, not a hand-drawn CSS mock — a genuine tela sheet (a real business budget in a non-tela domain) rendered by the actual grid, exported to optimized WebP in `landing/public/sheets/`. **Light register — a clean, premium spreadsheet look, deliberately distinct from the cool/dark page** (the same "distinct premium register" move as §6a's warm `paper` slides). The sheet must **visibly** show the depth: the **formula bar** with a live `=SUM(...)` selected, computed results in a bold `=SUM` total row, **conditional formatting** (over-budget cells in red), and currency + percent number formats — so the quality is evident, not asserted. Present it in a framed viewer (a title-bar with chips `fx` · `CSV` · `XLSX`). Re-shoot if the grid theme changes materially. Capture recipe lives in the component header comment.
- **Capabilities:** `Agents build whole spreadsheets for you` · `Live formulas, conditional formatting & number formats` · `Import & export CSV and XLSX` · `Plain markdown you own underneath`.
- **Honesty line:** sheets are plain markdown (a compact table format) underneath — portable like everything else; formulas are computed at read time, never baked into stored numbers. Sheets are live-collaborative (cursors + presence), and semantic search reads the *computed* values, not the raw formulas.

### 6a. Presentations — "Presentations, built in."  — Tier 2  (added 2026-06: decks shipped; THE standalone capability, not a page trick)
- **Purpose:** Surface presentations as a **standalone big feature tela provides** — not "any page becomes slides" (that framing sells it as a minor page trick; it isn't). tela makes real presentations: present live in the browser, export, and — the differentiator — **your agent builds the whole thing for you** from Claude/ChatGPT. **Page order: rendered after the feature showcase (§6), before the Comparison (§7)** — high on the page because it's a headline ability, not a footnote.
- **Copy rules (load-bearing):**
  - Frame it as a **capability tela has** ("tela makes presentations"), never as a per-page conversion ("any page is a presentation").
  - **Never sell the engine** — "Slidev", "tahta", theme package names mean nothing to a buyer and read as leaking internals, so they never appear in the headline, body, capabilities, or mock chrome (use "presentations / slides" and a neutral `Theme · Indigo` label). Markdown is fine to name (it's a page-wide pillar and widely understood); the slide *engine* is not. **Exception:** a small muted **"Powered by Slidev and tahta"** credit line at the very bottom of the section, with outbound links — attribution, not a selling point.
  - Lead with the universal word **"presentation" / "slides"**; "deck" only as an occasional synonym.
- **Headline (H2):** `Presentations, built in.`
- **Lede:** `tela makes real presentations — present them live in the browser, or export to PDF, PPTX or PNG. And because tela is agent-native, your agent can build the whole thing for you: ask Claude or ChatGPT to "turn the launch notes into a 10-slide talk" and it does.`
- **Visual (showcase the quality — show, don't describe):** use **real rendered slides**, not a hand-drawn CSS mock — a deck authored *about tela* and rendered with the actual theme, exported to optimized WebP in `landing/public/decks/`. **Variant = `paper` (warm cream, editorial serif, faint grain), indigo accent — a deliberately distinct, premium register vs the cool/dark landing** (not `atelier`, which blends into the dark/indigo page; not the default `editorial`; not `minimal`, which read as too plain). Copy is punchy/opinionated (problem → fix → how → proof → connect → payoff → CTA) and uses richer layouts (aurora/mesh backgrounds, a ghost glyph, a big-type payoff) — not flat title+body slides. Present them in a framed viewer (output chips `Present` · `PDF` · `PPTX`) with a **clickable filmstrip** of real thumbnails that swaps the main slide (progressive enhancement; every thumbnail is a real visible slide without JS). Slides span varied layouts (cover/lead, define, steps, feature, stats, code, statement, end) so the typographic + layout quality is evident. Re-render if the deck theme changes materially.
- **Capabilities:** `Agents build whole presentations for you` · `Present live — presenter mode, overview, draw` · `Export to PDF, PPTX or PNG` · `Plain markdown you own underneath`.
- **Honesty line:** slides are plain markdown underneath — portable like everything else; pick a theme and accent. Agents preview and fix their own slides before handing them over.

### 7. Honest comparison — "Not just another wiki."  — Tier 2  (head-to-head, NOT smug)
- **Purpose:** Beat the reader to the comparison — and concede the one thing each alternative genuinely does better. Conceding builds trust; the synthesis line lands harder. **REFRAMED 2026-06-28 (with the user):** the product has outgrown a wiki-parity scorecard — Atlas (documents itself) + the agent layer are the category move, so the section now leads by *repositioning out of "another wiki"* and every row's edge centers Atlas + agent-native retrieval, not feature parity. **Trimmed** the weak "git repo of markdown" row (the technical ICP already gets it; the AI-wiki row carries the agent-native differentiator better) → 4 rows.
- **Headline (H2):** `Not just another wiki.`
- **Framing line (lede):** `tela documents itself and your agents work it from Claude and ChatGPT — so the honest question isn't "which wiki," it's how it differs from each tool you'd otherwise reach for, and the one thing each still does better.`
- **Row structure (each):** `Alternative` · **`Where tela wins:`** one line (centers Atlas / agent-native) · **`What it still does better:`** one line.
- **Rows (4):**
  - **Notion** — **wins:** `Your docs write themselves — point Atlas at a repo or Jira project and it generates a cited, coverage-checked wiki — over plain markdown you own, that your agents read and write from Claude and ChatGPT. Not a closed block store with a chat sidebar bolted on.` · **better:** `Databases, templates, and all-round polish are years ahead. Want a relational workspace, not a wiki? Use Notion.`
  - **Confluence** — **wins:** `Atlas turns a git repo or a Jira project into a cited, coverage-checked wiki and keeps it fresh; agents search and write it by meaning; and it's portable markdown you can run yourself — no enterprise install, no per-seat pricing.` · **better:** `Deep two-way Jira workflow integration (tela documents from Jira; it doesn't drive Jira), granular permissions, and governance at thousand-user scale. Atlassian shops have reasons to stay.`
  - **Obsidian** — **wins:** `Built for a team and for agents: Atlas-written docs, semantic retrieval, live multiplayer, SSO and roles, and an MCP connector — not a single-player vault you sync by hand.` · **better:** `Local-first single-user is its whole point; the plugin library and graph view are unmatched. Solo? Hard to beat.`
  - **Notion AI / "AI" wikis** — **wins:** `tela generates the wiki from your sources and scores its coverage, then lets your own agent retrieve and write it from Claude or ChatGPT, over markdown you can export — a chatbot bolted onto a wiki does neither, and only works inside their app.` · **better:** `One-vendor convenience and a polished in-app assistant, with no embedder to think about. If you only want their chatbot, it's simpler.`
- **Synthesis line (the close):** `None of them close the loop tela does: your sources become a cited, coverage-checked wiki, your agents reason over and maintain it from Claude and ChatGPT, and it's all plain markdown you own — with real-time editing, SSO, roles, and an audit trail.`

### 8. Security & team — "Secure, team-shaped, and yours."  — Tier 2  (reassurance pillar; replaces the old self-host pillar)
- **Purpose:** Make a technical buyer comfortable putting the team's knowledge in. This is the trust pillar. End with the self-host escape hatch — present, not headline.
- **Headline (H2):** `Built for a team you can trust it with.`
- **Body (answer-first):** `Single sign-on (WorkOS), email-verified accounts with Argon2id password hashing, organizations and sub-team groups, and per-space roles — owner, editor, viewer — with hard invariants (a space always has a real owner; a grant can never silently lower someone's access). Agent keys are scoped read/write/admin, expiring, pinnable to one space, stored only as HMAC, and every key request is audited.`
- **Proof chips / tiles (real, shipped):** `SSO (WorkOS)` · `Orgs + groups` · `Per-space RBAC` · `Scoped, audited API keys` · `Argon2id` · `SSRF-hardened import` · `Password-gated share links`.
- **Self-host escape hatch (short, the demoted pillar):** `Prefer to run it yourself? You can. tela is open and self-hostable — Docker Compose, your Postgres, your disk, your markdown. Read exactly what runs before you run it.`  CTA: `Read the docs` → `docs/` / README.
- **Honesty line:** `Orgs are admin-provisioned (not open self-service signup) and social login isn't wired yet — by design for now. The access model is documented and the auth code is open.`

### 8a. Publish — "Your wiki doubles as a public site."  — Tier 2  (added 2026-06: public-spaces blog shipped)
- **Purpose:** Surface the public/publishing story — a space flipped to `visibility=public` becomes a no-login, magazine-style blog (docs/public-spaces.md). Distinct from the per-page **share links** in the bento.
- **Headline (H2):** `Your wiki doubles as a public site.`
- **Lede:** flip a space public → no-login magazine blog (docs/changelog/handbook) with RSS + SEO; same plain markdown underneath, nothing duplicated/exported.
- **Visual:** a blog front-page mock — masthead (monogram avatar + standfirst + RSS chip) + post cards (cover w/ graph-paper grid + faded monogram, meta `date · reading-time · tag`, title, excerpt). Mirrors the real reader.
- **Capabilities:** `No-login public reader` · `RSS feed per space` · `OG, JSON-LD & sitemap for SEO` · `Author home at /u/handle`.
- **Honesty line:** read-only by design — making a space public grants no write access; owner flips it on and can flip it back.

### 8b. Pricing — "Simple plans. Your markdown either way."  — Tier 2  (reworked 2026-06-30: editions model)
- **Purpose:** Show the plan ladder without hype, and make **Atlas a flagship reason to be on a paid tier**. **REWORKED 2026-06-30 (with the user) — `docs/editions-and-pricing.md` is the source of truth.** The split personal-vs-org grid is gone: cloud is now **one unified per-seat ladder** (Free · Personal · Team · Enterprise). Tiers change the **metered AI** (Atlas source cap + monthly AI-answer allowance) and team/enterprise controls — **NOT** page/space/storage caps (cheap to run, nobody gates on them). The wiki, Atlas, search, ask-your-docs, sheets, decks, sync and the agent connector are on every plan; **SSO + audit move into Enterprise** (basic orgs + per-space roles stay on every plan).
- **Model in one line:** *where it runs decides the AI* — **Cloud** = managed AI included; **self-host** = bring your own AI, every edition. *Which edition decides the company-layer* — **Enterprise** = the same identity/governance/support layer, bundled on cloud or a license key on self-host.
- **Two surfaces:** the **on-page** Pricing section carries the **cloud ladder only**; the dedicated **`/pricing`** page adds the self-host detail — **two editions** (Community, Enterprise), a **Commercial-license strip** (AGPL relief, demoted below the cards — a lawyer's SKU, not a tier), the **reconciler matrix** (one product · two tiers Core/Enterprise · two run-modes Cloud/Self-host), and the core-vs-Enterprise feature line (component `SelfHostPricing.astro`).
- **Headline (H2):** `Simple plans. Your markdown either way.`
- **Lede:** `One ladder, the whole product on every tier — Atlas, semantic search, ask-your-docs, and the agent connector. Paid tiers raise your AI — how many sources Atlas keeps fresh and how many answers a month — and add team controls. Managed AI is included on every cloud plan.`
- **Atlas-as-flagship band (lead the section with it):** `Atlas is on every plan — point it at a git repo or Jira project and it writes a cited, coverage-checked wiki and keeps it fresh. Paid tiers raise how much you can generate.`
- **Trial note (demoted — quiet fine-print under the tiers):** `No card to start — every new account gets a 30-day Personal trial, then settles onto your plan.` Purchase leads; the trial reassures.
- **Plan CTAs (self-serve billing is live via Polar):** **Free** → `Get started` → `/register`. **Paid** (Personal, Team) → `Get Personal` / `Get Team` → in-app checkout (`/settings?tab=billing`, login-gated, one click). **Enterprise** → `Get in touch` (mailto).
- **Dollar pricing (mirror `docs/editions-and-pricing.md` + the backend `plans` table):** Free `$0`; Personal `$8/mo`; Team `$10/seat/mo`; Enterprise `Custom` (contact-sales — a company-of-record quote, shown with **no price**, CTA `Get in touch`).
- **Yearly billing:** a **Monthly/Yearly toggle** sits directly above the cards (hidden until JS); **yearly is the DEFAULT** (no-JS sees yearly too). The card shows the **per-month equivalent** of the annual price, with `Billed annually — $X/yr, save $Y` on a sub-line. Per-tier discounts chosen for a clean per-month number: Personal `$6/mo` (`$72/yr`, save `$24`, **25% off**), Team `$8/seat/mo` (`$96/seat/yr`, save `$24`, 20% off). Toggle pill reads **"Save up to 25%"**. Free + Enterprise have no yearly option. Paid CTA carries `&interval=year` by default. Backend: `plans.price_cents_yearly` + `<plan>@year` Polar products.
- **Cloud ladder (cards — one unified per-seat ladder):** `Free` (`$0` · unlimited with your own agent (MCP) · 50 built-in AI answers/mo · 1 Atlas source · manual refresh · 3h indexing/mo · unlimited pages & spaces; `Get started`) · `Personal` (`$8/mo` · 5 Atlas sources · scheduled refresh · 15h indexing/mo · 3,000 built-in AI answers/mo · custom domain & public spaces) · `Team` *(recommended — the one earned-indigo card)* (`$10/seat/mo` · 20 Atlas sources · scheduled refresh · 30h indexing/mo · 5,000 built-in answers/mo pooled · roles & member management) · `Enterprise` (`Custom` · unlimited Atlas & AI · SSO/SCIM/audit · advanced roles & governance · priority support + SLA; `Get in touch`).
- **Every-plan-includes (checklist band — tiers change limits, never the core product):** `**Atlas — generate & refresh a cited, coverage-checked wiki from a git repo or Jira project** · Semantic search + ask your docs, with citations · Bring your own agent (Claude, ChatGPT) over MCP — ask unlimited, on your model · Local folder sync over WebDAV · Real-time multiplayer editing · Organizations & per-space roles · Plain markdown you own — export anytime.` (SSO/SCIM/audit are **Enterprise**, not every-plan.)
- **Self-host callout (links to the `/pricing` editions detail):** `Or run it yourself — free` — `tela is open source (AGPL). Self-host the whole product — Atlas, search, agents, sync — and bring your own AI. Community is free forever; Enterprise adds SSO, audit and governance.` CTA `Compare editions →` → `/pricing#self-host`.
- **Self-host lede + bridge line (`/pricing`):** the whole product is **free to self-host** — unlimited people, BYO AI; you only pay for the **Enterprise layer** (identity/governance/support, license key). Load-bearing line: **"Everything below Enterprise is free to self-host, forever."** Reconciler note: *cloud is priced on managed AI + seats; self-host is free because you bring your own AI + infra; **Enterprise is the same feature layer either way** — you pick who runs it.* Cloud Enterprise is **contact-sales (no published price)**; self-host Enterprise is **$8/seat** — deliberately pegged to a **Team seat's displayed (yearly) price `$8`** so it never reads as *more* than Team on the default view (the optics trap is structurally impossible, not just explained away by the matrix).
- **Self-host editions (`/pricing` only — `SelfHostPricing.astro`) — TWO cards + one strip:** `Community` (free, AGPL-3.0 · the whole product · unlimited users/spaces/pages · BYO AI · community support; `Get the code` → GitHub) · `Enterprise` *(earned-indigo)* (`$8/user/mo`, billed annually — **the price of a Team seat**, on your own servers · everything in Community + SSO/SAML/OIDC, SCIM, audit, advanced RBAC, premium Atlas connectors, priority support + SLA · unlocked by a license key; `Get a license` → `/settings?tab=licenses` self-serve checkout). Below the cards, a demoted **Commercial-license strip** (flat annual · the full open core under a commercial license, AGPL relief, no EE/key; for legal/compliance constraints; `Contact us`) — a real revenue lever but not a co-equal card.
- **Honesty line:** every cloud number (Atlas sources, AI answers/month, dollar prices) mirrors `docs/editions-and-pricing.md` + the backend `plans` table. The answer allowance meters only tela's **built-in** AI (ask-your-docs on our model); driving tela from **your own agent over MCP is unmetered/unlimited** on every tier. Semantic *search / retrieval* isn't monthly-capped but is fair-use rate-limited per account (`TELA_EMBED_RATE_LIMIT`). Scheduled Atlas refresh is paid — **Free is manual refresh only**, paid + trial refresh on a schedule. **Cadence is bounded by the indexing budget**: hourly is 730 runs/month and cannot fit ANY capped plan (even a 2.5-min run costs 1,825 min against the most generous 1,800 cap), so it is offered only on uncapped tiers and refused server-side elsewhere — never advertise it as available on Free/Personal/Team. **Self-host is BYO AI on every edition** — point tela at your own OpenAI-compatible LLM + embedder. Enterprise features live in a proprietary `ee/` module unlocked by an offline-verifiable license key; the open core stays AGPL. **Status: self-serve billing (Polar) is LIVE — the landing is deployed against the shipped backend.**

### 9. Credibility — "Open. Live. In the directories."  — Tier 3  (transparency as proof; NO fake logos)
- **Headline (H2):** `Why trust it? Don't — read it and run it.`
- **Three proof tiles (transparency, not testimonials):**
  - `Open code` — `Backend, frontend, and the MCP server are open. See exactly what runs.` → GitHub.
  - `Live instance` — `telawiki.com runs the same code. Use it before you commit.` → live link.
  - `Connector you can add` — `A real MCP connector, submitted to the Claude and ChatGPT directories and versioned against the backend.` → `/mcp` docs.
- **Honesty line:** `tela is at v0 and usable today. No fabricated logos, no "trusted by thousands" — just the code, a running instance, a connector you can add, and a spec you can read.`

### 10. FAQ / objections  — Tier 3  (question-led H2s, answer-first prose; NO FAQPage schema)
- `Do I have to write all the docs myself?` → `No. Point Atlas at a source — a git repo or a Jira project — and it reads the source, drafts cited markdown pages, scores them against the source's real surface (a coverage %, with the gaps named at file:line), and publishes them as ordinary tela pages. Two source types today; more coming. You can still write and edit pages by hand alongside the generated ones.`
- `What happens when my code changes?` → `Atlas probes each source for drift about every 15 minutes (a cheap no-clone check) and regenerates on a schedule you set — hourly, daily, weekly, monthly, or off — rebuilding only the sources that actually changed (a delta run over the changed files). Generated pages are the machine-maintained layer, so a re-run rewrites them. Atlas needs an LLM and an embedder — both on for the hosted instance; bring your own on self-host.`
- `Does it work inside Claude and ChatGPT?` → `Yes. tela runs a remote MCP server with OAuth sign-in; add it as a connector in Claude or ChatGPT (it's submitted to both directories) and your agent searches, reads, and writes your wiki — scoped to your account. Code agents like Claude Code or Cursor point at the same URL with a token.`
- `How is search different from a normal wiki?` → `tela does hybrid retrieval: keyword full-text (Postgres) and vector similarity (pgvector) fused with reciprocal-rank fusion, over heading-aware chunks. Agents get research + read_chunk, so they retrieve the section that answers the question — not just keyword matches — with a citation.`
- `Can it answer questions, not just find pages?` → `Yes — "ask your docs". Ask in plain language; tela retrieves the relevant sections and writes an answer with citations to the source pages, grounded only in what your account can read, and abstains when your docs don't cover it. Humans ask in-app; agents ground the same way through the connector. Managed AI on the hosted instance is metered by your plan's monthly allowance.`
- `Do I need to run an embedder?` → `Only on self-host: semantic/vector search needs an Ollama-compatible embedder (the live instance uses qwen3-embedding:0.6b), and ask-your-docs also needs an LLM. Keyword full-text needs nothing extra.`
- `Is it really markdown, with all that block editing?` → `Yes. The editor is full block editing — drag, slash menu, turn-into, tables, diagrams — but pages.body is plain markdown. There is no block table; reordering a block reorders markdown lines. Import a directory, export anytime.`
- `Can I edit in my own editor — Obsidian, VS Code?` → `Yes. Mount a space as a local folder over WebDAV and sync it with rclone, then edit in any editor. Pages round-trip as plain markdown and non-markdown files (images, PDFs, diagrams) sync too — local folder and tela stay in step both ways.`
- `How do agents authenticate?` → `OAuth 2.1 for the Claude/ChatGPT connectors (one sign-in, no token to paste), or a scoped personal access token (tela_pat_…) for code agents. Keys are read/write/admin, expirable, pinnable to one space, and audited.`
- `Is my team's data access-controlled?` → `Yes — SSO, organizations and groups, and per-space roles (owner/editor/viewer) with hard invariants. Keys are scoped and audited. The access model is documented and open.`
- `Can I self-host it?` → `Yes. The Community edition is the whole product, free and open (AGPL), with Docker Compose — your data on your disk, your markdown exportable. Self-host is bring-your-own-AI: point it at your own LLM + embedder. Enterprise adds SSO, audit and governance via a license key. Self-host is the option, not the requirement — the hosted instance is ready to use now, with managed AI included.`

### 11. Final CTA  — Tier 1  (close)
- **Headline:** `Give your agents a wiki they can reason over.`
- **Subhead:** `Start free on the hosted instance, then connect it in Claude or ChatGPT.`
- **Primary CTA:** `Get started` → https://telawiki.com.  **Secondary:** `Add to Claude or ChatGPT` → `#agents` / `/mcp`.
- **Friction microcopy:** `Free to start · markdown you own · self-host whenever you want.`

### 12. Footer  — Tier 4  (junk drawer)
- Wordmark + one-line descriptor: `tela — the team wiki that documents itself and your AI reasons over.`
- Links: GitHub · MCP / connector · Docs · Live instance (telawiki.com) · Privacy · License.
- Optional, sparing: `tela — Latin for the woven cloth. A grid you build on.` (use once or not at all.)

- **Primary CTA (one, repeated):** `Get started` → https://telawiki.com · friction: `Free to start · markdown you own · self-host whenever you want.`
- **Secondary CTA (repeated):** `Add to Claude or ChatGPT` → `#agents` / `/mcp`.

## SEO & accessibility

- **`<title>`:** `tela — the team wiki that documents itself and your AI reasons over`
- **Meta description:** `A markdown team wiki that documents itself: point Atlas at a git repo or Jira project and it writes a cited, coverage-checked wiki and keeps it fresh. Your AI agents search it by meaning and read, write, and cite it inside Claude and ChatGPT. Real-time multiplayer, SSO, scoped access. Hosted or self-hosted.`
- **5-second clarity test (visitor must grasp):** *tela is a markdown team wiki, and its standout is one loop — it documents itself from your sources (Atlas, coverage-checked) and your AI reasons over it (semantic retrieval + a real MCP connector inside Claude and ChatGPT).*
- **Headings:** one H1 (the hero promise); section H2s phrased as real queries where natural ("What can an agent actually do with tela?", "How does search work?"). Descriptive anchor text (never "click here"). Meaningful alt on the editor visual and the hero signature moment. Plain-language reading level (~8th grade) despite the technical audience.
- **JSON-LD:** `SoftwareApplication` (name, description covering Atlas doc-generation + MCP + semantic-search + markdown, `applicationCategory`, `operatingSystem`, open-source), plus `SoftwareSourceCode`, `Organization`/`WebSite`. **No FAQPage** schema (FAQ rich results removed 2026) — answer-first prose under question H2s is the durable play. **Remove "SQLite" from the structured-data description.**

## VoC swipe file  (verbatim language to use in copy)

- "the wiki that documents itself"
- "point it at a repo, get a wiki that stays current"
- "measurably complete, not 'the AI wrote something'"
- "a coverage score, with the gaps named"
- "it watches the source and regenerates when it moves ahead"
- "two sources today — git and Jira; more coming"
- "the wiki your agents reason over"
- "search your docs by meaning, not just keywords"
- "use it inside Claude and ChatGPT"
- "the right section, with a citation"
- "stops starting from zero every session"
- "edits like a block editor, saves like a markdown file"
- "no block table — reordering a block reorders markdown lines"
- "markdown you own / take it back / leaving means copying files"
- "SSO, roles, scoped and audited keys"
- "submitted to the Claude and ChatGPT connector directories"
- "hosted for you — self-host if you'd rather"
- "read exactly what runs before you run it"
- "use it before you commit"
