# Knowledge intelligence — tela's gem

> The thesis: a documentation tool should not be a filing cabinet you maintain.
> It should be a **knowledge organism the system maintains for you** — connecting,
> deduplicating, surfacing gaps, answering, and keeping itself fresh. tela's
> embedding/retrieval layer (see [`rag.md`](rag.md)) is the engine; this doc is
> what we build *on* it, and why it's the product's centerpiece.

## The reframe

Most wikis are **write-only graveyards**: people file pages, the pages rot,
nobody finds them, and the same topic gets re-documented in three places that
drift apart. Every "knowledge base" feature that matters is really fighting one
of four failures:

| Failure | Old answer | tela's answer |
| --- | --- | --- |
| Knowledge fragments into orphans | manual `[[links]]` (nobody maintains) | **connections emerge from content** — related pages + write-time link suggestions |
| The same thing documented N times | hope someone notices | **overlap detection** finds near-duplicates to merge |
| Docs go stale / silently wrong | "review by" dates | **freshness + gaps** surface what's rotten and what's missing |
| You can't find / synthesize | keyword search | **hybrid retrieval + ask-your-docs** with citations |

The industry is converging here — Reflect/Capacities/Atlas pitch *"AI builds
connections automatically from content rather than asking you to declare them"*;
Obsidian's Smart Connections *"suggests backlinks even when you didn't tag
them"*; Notion AI *"suggests links to related pages"*; Guru and Slite lead with
**verification / stale-content flagging**; Glean builds an enterprise **knowledge
graph** with cited answers. tela does the same primitives, but with a sharper
wedge:

- **Markdown-native & self-hostable** — your knowledge stays in plain `pages.body`
  in your own Postgres, not a proprietary store.
- **Agent-first** — every capability below is an MCP tool, so agents are
  first-class authors and researchers, not bolted-on chat.
- **One engine** — hybrid search, vectors, and these features are all Postgres +
  pgvector. No second system to operate.

## Capability catalogue

### Rollout

The knowledge-intelligence surface is always on (subject only to its runtime
dependencies — the embedder for retrieval, the LLM for generation, which 503
when unconfigured). It was originally shipped dark behind a `feature.knowledge` /
`feature.ask` instance flag; that experimental-flag layer has been removed in
favour of doing per-user feature gating properly later. New views (a topic map,
the health dashboard) are **additive** — they sit beside folders and the page
tree, never replacing them.

### Built (on this branch)

| Capability | What it does | Surfaces |
| --- | --- | --- |
| **Related pages** | semantic "see also" for any page (centroid → nearest pages) | `GET /api/pages/{id}/related`, MCP `related_pages` |
| **Link suggestions** | existing pages a *draft* should link to (assisted authoring) | `POST /api/rag/suggest-links`, MCP `suggest_links` |
| **Overlap detection** | near-duplicate page pairs to merge/redirect (hygiene) | `GET /api/rag/overlaps`, MCP `find_overlaps` |
| **Knowledge gaps** | most-asked questions the corpus *couldn't* answer → content roadmap | `GET /api/rag/gaps[?space_id=]`, MCP `knowledge_gaps` |
| **Ask your docs** | cited answers grounded on full chunks (+ follow-up questions) | `POST /api/rag/ask` |
| **Ask-first authoring** | a grounded markdown *draft* for a new page from a topic | `POST /api/rag/draft` |
| **Answer → page** ⭐ | answer a question AND save it as a cited page — closes ask→gap→write | `POST /api/rag/answer-to-page` |
| **Page questions** | "what does this page answer?" — seeds for ask-first nav | `GET /api/pages/{id}/questions` |
| **Reranking** | optional cross-encoder second stage for top-k precision | env `TELA_RAG_RERANK_URL` |

The generative features (draft / answer-to-page / page-questions / follow-ups)
share one seam — `askContext` (retrieve + cite) and `askComplete` (caps + LLM).
They're REST-first; agents compose the same outcomes from `search` +
`create_page`, so there's no parallel LLM-cap path on MCP.
| **Self-healing index + eval** | the index keeps itself fresh; `rag-eval` measures quality | see [`rag.md`](rag.md) |

All read capabilities are **access-scoped through the live page row** (the
anti-leak invariant) and need only the stored vectors — `related_pages` /
`find_overlaps` work even while the embedder is offline.

### Designed (next, in priority order)

1. **Write-time intelligence panel.** As you write a page, inline: *suggested
   links* (built), *"3 pages already cover this — merge?"* (overlap on the draft),
   and *contradiction hints* (a related page that disagrees). Turns the blank page
   into an assisted, connected, de-duplicated one. The agent equivalent: a
   `create_page` that runs the dedup/link checks before committing.
2. **Knowledge-health dashboard.** One surface for the wiki's vital signs:
   stale pages, orphans, overlaps, gaps, unindexed. Documenting becomes gardening
   with a map. (Backend signals mostly exist — freshness, overlaps, gaps.)
3. **Auto-summaries / contextual retrieval.** An LLM-generated 1–2 sentence
   situating blurb per page, kept fresh — doubles as the contextual-retrieval
   quality lift (−49% retrieval failures) AND powers richer related/ask cards.
4. **Topic map / clustering.** Cluster pages by embedding into emergent topics —
   a self-organizing map of the knowledge base, instead of manual folders.
5. **Verification & decay.** Guru/Slite's headline: flag pages whose facts are
   likely stale (age + contradicted by newer related pages), prompt a re-verify.
6. **Ask-first navigation.** Make the question box the front door — answer with
   citations, then offer follow-up questions (Dash-style distillation) and the
   related pages to drill into.
7. **Expert detection.** "Who knows about X" from authorship/edits of the pages
   most related to a query (Glean's people-graph, scoped to tela).

## Why this composes

Each feature feeds the others: asks generate **gaps** → gaps tell you what to
write → writing triggers **link suggestions** + **overlap** checks → new pages
become **related** neighbours → the **self-healing index** keeps them retrievable
→ **ask** answers from them with citations → repeat. The wiki improves itself,
and an agent can drive the whole loop through MCP. That loop — not any single
feature — is the gem.

## Privacy & control

`ask_log` records questions to power gaps. Reading it is open to every signed-in
user, scoped by `rag.GapScope` to **their own asks plus asks made inside spaces
they are a member of** — membership, not readability, so publishing a space does
not hand strangers its members' questions, and an ask with a NULL `space_id`
(asked across everything) is visible only to its asker. Instance admins see the
whole instance. Logging can be disabled instance-wide with `TELA_RAG_LOG_ASKS=0`.
Everything else reads only content the caller can already access.
