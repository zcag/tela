// Data for the /compare/<slug> pages. One entry per competitor; the page
// template (src/pages/compare/[slug].astro) and the sitemap read from here.
//
// Voice: plain, capability-first, honest — matches Compare.astro. Each page
// concedes what the competitor still does better (the "when X is better" line).
// FACTS (verified mid-2026): tela is open-source (AGPL), self-hostable,
// markdown-native (canonical markdown, no block table), with a built-in MCP
// server (39 tools) and Atlas (a cited, coverage-checked wiki generated from
// git + Jira). The real differentiator is ATLAS + open-source/self-host/markdown
// ownership — NOT "they have no MCP": Notion, Confluence, GitBook, Docmost,
// Slite, Nuclino, Coda, Mintlify, AFFiNE and Backstage all ship one. Only ever
// say a competitor has no OFFICIAL MCP server, only when it was actually
// checked, and say what does exist (community projects, or an official server
// that covers something other than the docs — Backstage's exposes the catalog,
// GitHub's ships no wiki tools).
// Keep compare pages price-agnostic on purpose (durability): say "self-host free
// · free cloud tier", not concrete numbers — so a pricing change never stales
// these. Canonical prices live in docs/editions-and-pricing.md + the landing.

export interface CompareRow {
  /** Comparison dimension. */
  feature: string;
  /** tela's value. */
  tela: string;
  /** the competitor's value. */
  them: string;
}

export interface Competitor {
  slug: string;
  /** Proper display name, e.g. "Notion". */
  name: string;
  seoTitle: string;
  metaDescription: string;
  /** The H1 / page heading. */
  heading: string;
  /** Lead paragraph (plain text). */
  lead: string;
  rows: CompareRow[];
  /** "Why teams switch" bullets. */
  whySwitch: string[];
  /** Honest "when <competitor> is the better choice". */
  whenBetter: string;
  /** Short source note (where the competitor facts came from). */
  source: string;
  /**
   * Human-readable last-verified date for THIS entry, e.g. "September 9, 2026".
   * Optional: entries omit it and fall back to COMPARE_UPDATED, so a page only
   * claims a fresher date when its facts were actually re-checked.
   */
  updated?: string;
}

/** Fallback "last updated" for entries with no `updated` of their own. */
export const COMPARE_UPDATED = 'June 30, 2026';

const TELA_LICENSE = 'Open source (AGPL-3.0)';
const TELA_SELFHOST = 'Yes — self-host free, plus a free cloud tier';
const TELA_STORAGE = 'Canonical markdown you own';
const TELA_ASK = 'Built in — semantic + full-text, answers with citations';
const TELA_MCP = 'Built in — agents read & write (39 scoped tools)';
const TELA_ATLAS = 'Yes — Atlas builds a cited, coverage-checked wiki from git + Jira';

export const competitors: Competitor[] = [
  {
    slug: 'notion',
    name: 'Notion',
    seoTitle: 'Notion alternative — open-source, self-hosted, agent-native | tela',
    metaDescription:
      'An open-source, self-hostable Notion alternative. tela keeps canonical markdown you own, answers questions over your docs with citations, and generates a cited wiki from your code with Atlas.',
    heading: 'The open-source, self-hostable Notion alternative',
    lead: 'Notion is a strong all-round workspace, but your pages live in a proprietary block database, it is cloud-only, and nothing in it writes your docs from your code. tela is markdown-native, self-hostable, and agent-native — and Atlas generates a cited wiki straight from your git repos and Jira.',
    rows: [
      { feature: 'Storage', tela: TELA_STORAGE, them: 'Proprietary block database' },
      { feature: 'Self-hostable', tela: TELA_SELFHOST, them: 'No — cloud only' },
      { feature: 'Ask your docs (AI)', tela: TELA_ASK, them: '"Ask Notion" — on the Business tier' },
      { feature: 'Agents read & write (MCP)', tela: TELA_MCP, them: 'Official MCP server (behind paid AI)' },
      { feature: 'Generate docs from your code', tela: TELA_ATLAS, them: 'No' },
    ],
    whySwitch: [
      'Your docs write themselves — point Atlas at a repo or Jira project and it generates a cited, coverage-checked wiki.',
      'Own your content as portable markdown — export is a no-op, not a lossy converter out of a block store.',
      'Self-host on your own infrastructure, or use the free cloud tier.',
    ],
    whenBetter:
      'Notion is years ahead on databases, templates, and all-round polish. If you want a relational workspace — trackers, project boards, lightweight apps — rather than a wiki, Notion is the better tool.',
    source: 'Notion pricing + Notion MCP server (verified 2026).',
  },
  {
    slug: 'confluence',
    name: 'Confluence',
    seoTitle: 'Confluence alternative — fast, self-hosted, AI-native | tela',
    metaDescription:
      'A lightweight, self-hostable, AI-native Confluence alternative. tela is markdown-native, generates a cited wiki from your code with Atlas, and meters nothing to ask your own docs.',
    heading: 'A Confluence alternative your engineers will actually trust',
    lead: 'Confluence is heavy and its AI (Rovo) is metered in credits, with the better AI on higher tiers. And like every incumbent, it cannot write your docs from your source. tela is the lightweight, markdown-native, AI-native opposite — and Atlas keeps the wiki generated and current from your git repos and Jira.',
    rows: [
      { feature: 'Feel', tela: 'Fast, markdown-native, clean editor', them: 'Heavy; proprietary editor' },
      { feature: 'Self-hostable', tela: TELA_SELFHOST, them: 'Data Center (enterprise) or cloud' },
      { feature: 'Ask your docs (AI)', tela: TELA_ASK, them: 'Rovo — metered in credits' },
      { feature: 'Agents read & write (MCP)', tela: TELA_MCP, them: 'Rovo MCP (behind a paid plan)' },
      { feature: 'Generate docs from your code', tela: TELA_ATLAS, them: 'No' },
    ],
    whySwitch: [
      'It stays current by itself — Atlas regenerates from the code and flags drift; the usual reason Confluence spaces rot is that nobody updates them.',
      'No credit accounting just to ask your own wiki a question.',
      'Lightweight and ownable — a self-hostable wiki with plain-markdown portability, not a sprawling enterprise install.',
    ],
    whenBetter:
      'If your organization is deep in the Atlassian stack — Jira workflows, enterprise SSO and governance, thousand-user scale — Confluence\'s integration depth and existing investment are real reasons to stay. tela documents from Jira; it does not drive Jira.',
    source: 'Atlassian Rovo pricing + Rovo MCP (verified 2026).',
  },
  {
    slug: 'outline',
    name: 'Outline',
    seoTitle: 'Outline alternative — open-source (AGPL), agent-native wiki | tela',
    metaDescription:
      'tela vs Outline: both self-hostable markdown wikis. tela is AGPL (Outline is BSL-1.1 source-available), has a free cloud tier, a built-in MCP server, and Atlas — a cited wiki generated from your code.',
    heading: 'tela vs Outline — the AI-native, fully open-source option',
    lead: 'Outline is genuinely good and the closest comparison — a polished, self-hostable markdown wiki. The differences are three: license, pricing model, and the entire AI layer. Outline is BSL-1.1 (source-available, not OSI open source) with no free cloud and no first-class agent/auto-doc layer; tela is AGPL with a free cloud tier, a built-in MCP server, and Atlas.',
    rows: [
      { feature: 'License', tela: TELA_LICENSE, them: 'BSL 1.1 — source-available, not OSI open source' },
      { feature: 'Self-hostable', tela: TELA_SELFHOST, them: 'Yes (no free cloud)' },
      { feature: 'Ask your docs (AI)', tela: TELA_ASK, them: 'Self-host + your own OpenAI key' },
      { feature: 'Agents read & write (MCP)', tela: TELA_MCP, them: 'No official server (third-party only)' },
      { feature: 'Generate docs from your code', tela: TELA_ATLAS, them: 'No' },
    ],
    whySwitch: [
      'The axis Outline never built — Atlas generates a cited wiki from your code, and a built-in MCP server makes agents first-class authors.',
      'A cleaner open-source story — AGPL (real OSI open source) versus BSL\'s source-available restrictions.',
      'A free cloud tier to evaluate, plus self-host whenever you want.',
    ],
    whenBetter:
      'Outline is a mature, beautifully polished self-hosted wiki with a strong community. If you want a great self-hosted wiki today and do not need AI generation or agents, Outline is an excellent, stable choice.',
    source: 'Outline pricing + BSL-1.1 repo license (verified 2026).',
  },
  {
    slug: 'gitbook',
    name: 'GitBook',
    seoTitle: 'GitBook alternative — self-hosted AI wiki you own | tela',
    metaDescription:
      'An open-source, self-hostable GitBook alternative for internal team knowledge. tela generates a cited wiki from your repo with Atlas, and agents can write it — not just read it.',
    heading: 'The open-source GitBook alternative that documents itself',
    lead: 'GitBook is polished for public product docs, but it is proprietary SaaS you cannot self-host, priced per published site, and its Git Sync only mirrors markdown you already wrote. tela is the switch for internal team knowledge you own — markdown-native, self-hostable, and generated from your actual source.',
    rows: [
      { feature: 'License', tela: TELA_LICENSE, them: 'Proprietary SaaS' },
      { feature: 'Self-hostable', tela: TELA_SELFHOST, them: 'No — cloud only' },
      { feature: 'Ask your docs (AI)', tela: TELA_ASK, them: 'AI on its top tier' },
      { feature: 'Agents read & write (MCP)', tela: TELA_MCP, them: 'MCP, but read-only (published docs)' },
      { feature: 'Generate docs from your code', tela: TELA_ATLAS, them: 'No — Git Sync mirrors existing markdown' },
    ],
    whySwitch: [
      'Atlas writes the first draft from your code; GitBook\'s Git Sync only mirrors markdown you authored by hand.',
      'Agents are full citizens — GitBook\'s MCP is read-only and exposes only published docs; tela\'s agents search and write your live wiki.',
      'Self-host under AGPL and keep portable markdown, instead of renting per published site.',
    ],
    whenBetter:
      'If your job is beautiful public-facing developer documentation — versioned API references, multi-version docs for an open-source library, a branded docs site — GitBook is excellent and hard to beat. tela is a team wiki, not a public docs-publishing platform.',
    source: 'GitBook pricing + published-docs MCP (verified 2026).',
  },
  {
    slug: 'bookstack',
    name: 'BookStack',
    seoTitle: 'BookStack alternative — AI-native, agent-ready self-hosted wiki | tela',
    metaDescription:
      'A self-hosted BookStack alternative with built-in AI and a native MCP server. tela keeps canonical markdown, answers questions over your docs, and generates a wiki from your repo with Atlas.',
    heading: 'The AI-native, open-source BookStack alternative',
    lead: 'BookStack is a rock-solid, MIT-licensed self-hosted wiki — and if you just need shelves, books, and pages, it is a great free choice. But it has no built-in AI, no official MCP server, stores content as HTML rather than markdown, and will not generate docs from your code. tela adds all four.',
    rows: [
      { feature: 'License', tela: TELA_LICENSE, them: 'Open source (MIT)' },
      { feature: 'Storage', tela: TELA_STORAGE, them: 'HTML-primary' },
      { feature: 'Ask your docs (AI)', tela: TELA_ASK, them: 'None built in' },
      { feature: 'Agents read & write (MCP)', tela: TELA_MCP, them: 'No official server (community only)' },
      { feature: 'Generate docs from your code', tela: TELA_ATLAS, them: 'No' },
      { feature: 'Live collaboration', tela: 'Yes — real-time multiplayer', them: 'No real-time co-editing' },
    ],
    whySwitch: [
      'Ask your docs, do not just keyword-search them — semantic answers with citations, out of the box.',
      'Agents are first-class via a built-in MCP server; BookStack has only community API wrappers.',
      'Atlas turns a repo into a cited wiki; BookStack content is entirely hand-authored.',
    ],
    whenBetter:
      'BookStack is more mature, dead-simple to run, genuinely zero-cost, and MIT-licensed with no copyleft to reason about. For a no-frills, permissively-licensed documentation wiki with no AI ambitions, it is a fantastic, lighter choice.',
    source: 'BookStack docs + content-storage model (verified 2026).',
  },
  {
    slug: 'docmost',
    name: 'Docmost',
    seoTitle: 'Docmost alternative — markdown-native, un-gated AI & agents | tela',
    metaDescription:
      'A markdown-native Docmost alternative. tela keeps canonical markdown (not ProseMirror JSON), ships AI and agent access without an Enterprise gate, and generates a cited wiki from your code with Atlas.',
    heading: 'The markdown-native, self-hosted Docmost alternative',
    lead: 'Docmost is the closest tool to tela here — both are AGPL, both self-host, both do live collaboration, and both ship an MCP server. The real differences are three: Docmost stores ProseMirror JSON rather than markdown, its AI and MCP server sit behind a paid Enterprise license, and it has no way to generate docs from your code.',
    rows: [
      { feature: 'License', tela: TELA_LICENSE, them: 'AGPL core + commercial Enterprise license' },
      { feature: 'Storage', tela: TELA_STORAGE, them: 'ProseMirror JSON (markdown = import/export)' },
      { feature: 'Ask your docs (AI)', tela: TELA_ASK, them: 'Built in — Enterprise license only' },
      { feature: 'Agents read & write (MCP)', tela: TELA_MCP, them: 'First-party MCP — Enterprise license only' },
      { feature: 'Generate docs from your code', tela: TELA_ATLAS, them: 'No' },
    ],
    whySwitch: [
      'Markdown is the source of truth — grep it, diff it, own it forever; Docmost stores ProseMirror JSON with markdown only as import/export.',
      'AI and agent access are in the box, not behind an Enterprise license wall.',
      'Atlas closes the loop Docmost does not — a cited wiki generated from your repo and Jira.',
    ],
    whenBetter:
      'Docmost is mature and well-rounded with a clear paid-support path — a polished block editor, a Confluence importer, SSO/SCIM, and audit logs. If you want a Notion-style block editor, a turnkey Confluence migration, or a vendor to buy a support contract from today, it is a strong pick.',
    source: 'Docmost editions, AI and MCP docs (verified 2026).',
  },
  {
    slug: 'slab',
    name: 'Slab',
    seoTitle: 'Slab alternative — self-hosted, markdown-native, agent-ready | tela',
    metaDescription:
      'An open-source, self-hostable Slab alternative. tela keeps your content as markdown you own, ships a read/write MCP server, and generates a cited wiki from your repo with Atlas.',
    heading: 'The self-hosted, open-source Slab alternative',
    lead: 'Slab is a clean, well-designed team knowledge base — but it is proprietary, cloud-only SaaS with no self-hosting, content lives in a proprietary rich-text format, and its AI "Ask" is gated to a higher plan. tela gives you the same organized team wiki — self-hostable, markdown-native, with AI and agents built in.',
    rows: [
      { feature: 'License', tela: TELA_LICENSE, them: 'Proprietary SaaS' },
      { feature: 'Self-hostable', tela: TELA_SELFHOST, them: 'No — cloud only' },
      { feature: 'Storage', tela: TELA_STORAGE, them: 'Proprietary rich-text "Posts"' },
      { feature: 'Ask your docs (AI)', tela: TELA_ASK, them: 'AI "Ask" — on a higher plan' },
      { feature: 'Agents read & write (MCP)', tela: TELA_MCP, them: 'No official server (community only)' },
      { feature: 'Generate docs from your code', tela: TELA_ATLAS, them: 'No' },
    ],
    whySwitch: [
      'Own your knowledge base and your data — self-host under AGPL; Slab is cloud-only.',
      'Markdown you can export and version, not a proprietary post format.',
      'Atlas generates a cited wiki from your sources; Slab\'s repo integration only mirrors existing markdown.',
    ],
    whenBetter:
      'Slab\'s editing experience and integration breadth are strong — a refined writing UI and a unified search that federates across Slack, Drive, GitHub, Linear, Jira and more. For a fully-managed, no-ops SaaS that ties a stack of existing tools together, Slab is a polished choice.',
    source: 'Slab pricing + unified search docs (verified 2026).',
  },
  {
    slug: 'wikijs',
    name: 'Wiki.js',
    seoTitle: 'Wiki.js alternative — open-source AI wiki that documents itself | tela',
    metaDescription:
      'An open-source Wiki.js alternative (AGPL, self-hosted). tela adds semantic ask-your-docs, a built-in MCP server, and Atlas — which generates a cited wiki from your code repos.',
    heading: 'The open-source Wiki.js alternative that writes its own docs',
    lead: 'Wiki.js is a deservedly popular self-hosted wiki — AGPL, markdown-native, free to run. But its shipping line has no built-in AI, no first-class agent integration, and its Git module only syncs your wiki to a repo; it never generates docs from your code. tela keeps the same ownership and adds the parts Wiki.js leaves to you.',
    rows: [
      { feature: 'License', tela: TELA_LICENSE, them: 'Open source (AGPL-3.0)' },
      { feature: 'Ask your docs (AI)', tela: TELA_ASK, them: 'None built in (keyword search)' },
      { feature: 'Agents read & write (MCP)', tela: TELA_MCP, them: 'No official server (community bridges)' },
      { feature: 'Generate docs from your code', tela: TELA_ATLAS, them: 'No — Git module syncs content' },
      { feature: 'Live collaboration', tela: 'Yes — real-time multiplayer', them: 'No real-time co-editing' },
    ],
    whySwitch: [
      'Your docs write themselves — Atlas generates a cited wiki from a repo or Jira project and flags coverage gaps.',
      'Agents are first-class via a built-in MCP server, not community wrappers over its API.',
      'Ask your docs by meaning, with citations — not just keyword search.',
    ],
    whenBetter:
      'Wiki.js v2 is mature, has a large module and theme ecosystem, and broad database-backend flexibility. If you want a proven, lightweight wiki and do not need AI, agents, or repo-to-doc generation, it is an excellent no-cost option. (Its v3 rewrite is still pre-release, so the stable choice is v2.)',
    source: 'js.wiki — license, Git sync, editors (verified 2026).',
  },
  {
    slug: 'slite',
    name: 'Slite',
    seoTitle: 'Slite alternative — self-hosted, open-source AI knowledge base | tela',
    metaDescription:
      'A self-hosted, open-source Slite alternative. tela is AGPL markdown you own — with ask-your-docs, a built-in MCP server, and Atlas to generate a cited wiki from your code.',
    heading: 'The self-hosted, open-source Slite alternative',
    lead: 'Slite is a polished cloud knowledge base with a genuinely good AI layer and an official MCP server, so the honest difference is not "Slite has no AI" — it is ownership and lock-in. Slite is proprietary, cloud-only, per-seat, with no permanent free tier. tela matches its AI-native posture but is open-source, self-hostable, and stores canonical markdown you own — and Atlas generates docs from your code, which Slite does not.',
    rows: [
      { feature: 'License', tela: TELA_LICENSE, them: 'Proprietary SaaS' },
      { feature: 'Self-hostable', tela: TELA_SELFHOST, them: 'No — cloud only' },
      { feature: 'Storage', tela: TELA_STORAGE, them: 'Block editor (markdown = import/export)' },
      { feature: 'Ask your docs (AI)', tela: TELA_ASK, them: 'AI "Ask" with citations (metered)' },
      { feature: 'Agents read & write (MCP)', tela: TELA_MCP, them: 'Yes — official remote MCP server' },
      { feature: 'Generate docs from your code', tela: TELA_ATLAS, them: 'No (AI detects drift across SaaS tools)' },
    ],
    whySwitch: [
      'No per-seat cloud bill and no lock-in — self-host under AGPL or use the free cloud tier.',
      'Own the markdown; Slite\'s content lives in its proprietary format and its cloud.',
      'Atlas turns repos and Jira into a cited wiki; Slite surfaces drift but does not author from your source.',
    ],
    whenBetter:
      'For a turnkey, beautifully designed hosted product with zero ops, deep Slack integration that auto-answers in channels, and an AI agent watching dozens of connected SaaS tools for documentation drift, Slite is excellent and faster to adopt.',
    source: 'slite.com/pricing + Slite changelog, official MCP (verified 2026).',
  },
  {
    slug: 'nuclino',
    name: 'Nuclino',
    seoTitle: 'Nuclino alternative — self-hosted, open-source team wiki | tela',
    metaDescription:
      'A self-hosted, open-source Nuclino alternative (AGPL). tela gives you ask-your-docs, a built-in MCP server, and Atlas — which generates a cited wiki from your code repos.',
    heading: 'The self-hosted, open-source Nuclino alternative',
    lead: 'Nuclino is fast and has a capable AI assistant with citations and an official MCP server, so the contrast with tela is not about AI existing — it is where your knowledge lives and how far the automation goes. Nuclino is proprietary and cloud-only, with the full assistant gated to its top tier. tela is open-source, self-hostable, stores markdown you own, and Atlas generates docs from your code.',
    rows: [
      { feature: 'License', tela: TELA_LICENSE, them: 'Proprietary SaaS' },
      { feature: 'Self-hostable', tela: TELA_SELFHOST, them: 'No — cloud only' },
      { feature: 'Ask your docs (AI)', tela: TELA_ASK, them: '"Sidekick" — full version on the top tier' },
      { feature: 'Agents read & write (MCP)', tela: TELA_MCP, them: 'Yes — official MCP server' },
      { feature: 'Generate docs from your code', tela: TELA_ATLAS, them: 'No' },
    ],
    whySwitch: [
      'Self-host and own your data — Nuclino is cloud-only with no on-prem option.',
      'AI is not paywalled to the top tier — semantic retrieval is built in.',
      'Atlas generates a cited wiki from repos and Jira; Nuclino is a manual wiki.',
    ],
    whenBetter:
      'Nuclino is exceptionally fast and simple, with a lovely lightweight UX, instant graph/board/canvas views, and zero setup. For a frictionless hosted team wiki where speed and minimalism matter more than self-hosting and repo-to-doc generation, it is a delightful choice.',
    source: 'nuclino.com/pricing + help docs, official MCP (verified 2026).',
  },
  {
    slug: 'coda',
    name: 'Coda',
    seoTitle: 'Coda alternative — open-source, self-hosted, markdown you own | tela',
    metaDescription:
      'An open-source, self-hosted Coda alternative. tela is AGPL canonical markdown — not a proprietary block-and-formula canvas — with ask-your-docs, a built-in MCP server, and Atlas.',
    heading: 'The open-source, self-hosted Coda alternative for docs you own',
    lead: 'Coda is a powerful doc-meets-app canvas — tables, formulas, Packs, an official MCP server, in-doc AI. It is also proprietary, cloud-only, priced per Doc Maker, and stores content in its own format. tela is a leaner proposition: an open-source, self-hostable, markdown-native team wiki where knowledge stays as markdown you own. If you reached for Coda to document a team and do not need its spreadsheet-database machinery, tela is the ownable alternative.',
    rows: [
      { feature: 'License', tela: TELA_LICENSE, them: 'Proprietary SaaS' },
      { feature: 'Self-hostable', tela: TELA_SELFHOST, them: 'No — cloud only' },
      { feature: 'Storage', tela: TELA_STORAGE, them: 'Proprietary block/canvas format' },
      { feature: 'Ask your docs (AI)', tela: TELA_ASK, them: 'Coda AI — credit-metered' },
      { feature: 'Agents read & write (MCP)', tela: TELA_MCP, them: 'Yes — official MCP server' },
      { feature: 'Generate docs from your code', tela: TELA_ATLAS, them: 'No' },
    ],
    whySwitch: [
      'Own your content as markdown — Coda locks docs into a proprietary format and its cloud.',
      'Open-source and self-hostable under AGPL, no per-Doc-Maker bill.',
      'Atlas generates docs from code; Coda has no repo ingestion.',
    ],
    whenBetter:
      'Coda\'s superpower is being a doc and a relational app at once — tables, formulas, buttons, and Packs that integrate dozens of services. If you want to build interactive workflows or lightweight internal tools rather than write and read documentation, Coda is in a more capable class for that job.',
    source: 'coda.io/pricing + Coda MCP guide (verified 2026).',
  },
  {
    slug: 'mediawiki',
    name: 'MediaWiki',
    seoTitle: 'MediaWiki alternative — modern, markdown-native, AI team wiki | tela',
    metaDescription:
      'A modern, markdown-native MediaWiki alternative. tela is open-source (AGPL), self-hostable — no wikitext, no heavy ops — with ask-your-docs, a built-in MCP server, and Atlas.',
    heading: 'The modern, markdown-native MediaWiki alternative',
    lead: 'MediaWiki is the engine behind Wikipedia — GPL, infinitely extensible, unmatched at encyclopedic public-scale wikis. For a team wiki it is a heavy lift: it uses wikitext rather than markdown, carries a steep learning curve, ships no built-in AI, and has no MCP server in core. tela keeps what is good — open-source, self-hostable, your data on your server — and drops the friction.',
    rows: [
      { feature: 'License', tela: TELA_LICENSE, them: 'Open source (GPL-2.0+)' },
      { feature: 'Markup', tela: 'Canonical markdown', them: 'Wikitext (not markdown)' },
      { feature: 'Ask your docs (AI)', tela: TELA_ASK, them: 'None in core (extensions only)' },
      { feature: 'Agents read & write (MCP)', tela: TELA_MCP, them: 'No core server (community wrappers)' },
      { feature: 'Generate docs from your code', tela: TELA_ATLAS, them: 'No' },
      { feature: 'Ops', tela: 'Lightweight modern stack', them: 'Heavyweight (Wikipedia-scale)' },
    ],
    whySwitch: [
      'Markdown, not wikitext — no template or parser-function learning curve.',
      'AI- and agent-native out of the box; MediaWiki needs bolt-on extensions and has no MCP in core.',
      'Atlas generates docs from code, and the stack is far lighter to run.',
    ],
    whenBetter:
      'For a massive, public, multilingual encyclopedia — thousands of contributors, deep template and transclusion systems, structured data via Semantic MediaWiki, and a vast extension ecosystem refined over two decades — MediaWiki is the proven, purpose-built engine, and nothing else matches it at that scale.',
    source: 'mediawiki.org — install requirements + copyright (verified 2026).',
  },
  {
    slug: 'obsidian',
    name: 'Obsidian',
    seoTitle: 'Obsidian alternative for teams — open-source, self-hosted, live collaboration | tela',
    metaDescription:
      'A team-ready, self-hosted Obsidian alternative. tela keeps your markdown but adds real-time multiplayer, SSO, ask-your-docs, a built-in MCP server, and Atlas — a cited wiki generated from your code.',
    heading: 'The open-source, self-hosted Obsidian alternative built for teams',
    lead: 'Obsidian is a beloved local-first markdown app — your notes are plain files you own, with an unrivaled plugin ecosystem and graph view. But it is built for one person: closed-source, no real-time multiplayer, no built-in AI or MCP, and Obsidian Publish is a hosted service you cannot self-host. tela keeps Obsidian\'s best idea — knowledge as portable markdown you own — and makes it a real team platform.',
    rows: [
      { feature: 'License', tela: TELA_LICENSE, them: 'Proprietary / closed-source' },
      { feature: 'Self-hostable', tela: TELA_SELFHOST, them: 'Local app; Publish is hosted, not self-hostable' },
      { feature: 'Real-time collaboration', tela: 'Yes — multiplayer editing', them: 'No — single-user; async vault sync' },
      { feature: 'Team controls (SSO, roles)', tela: 'Yes', them: 'No — single-user product' },
      { feature: 'Ask your docs (AI) & MCP', tela: TELA_ASK, them: 'None official (community plugins)' },
      { feature: 'Generate docs from your code', tela: TELA_ATLAS, them: 'No' },
    ],
    whySwitch: [
      'Real-time multiplayer with SSO and roles; Obsidian is single-player with async vault sync.',
      'Open-source and self-hostable — including the published surface; Obsidian Publish is a paid hosted service you cannot run yourself.',
      'AI and agents are built in, not assembled from community plugins of varying license and maintenance — and Atlas generates docs from code.',
    ],
    whenBetter:
      'For a single user\'s personal knowledge base, Obsidian is hard to beat: local-first and offline by default, an enormous plugin library, the graph view, and total control over a folder of files on your disk. For solo PKM or a personal digital garden, stay with Obsidian.',
    source: 'obsidian.md — pricing + license (verified 2026).',
  },
  {
    slug: 'docusaurus',
    name: 'Docusaurus',
    seoTitle: 'Docusaurus alternative — a team wiki with no build pipeline | tela',
    metaDescription:
      'A Docusaurus alternative for internal team knowledge. tela publishes on save with no build or deploy step, lets anyone edit in the browser, and agents read and write it through a built-in MCP server.',
    heading: 'The Docusaurus alternative with no build pipeline',
    lead: 'Docusaurus is an excellent static site generator for public developer docs — MIT-licensed, versioned, React all the way down. It is also a repo: every page is a markdown file, every edit needs a build and a deploy, there is no official editing UI, no search in the box, and no official MCP server. tela is the other shape — a wiki where saving publishes, anyone on the team can write, and Atlas drafts the pages from your code.',
    rows: [
      { feature: 'Publishing', tela: 'Save — the page is live', them: 'Build + deploy pipeline (CI → Pages, Netlify, Vercel…)' },
      { feature: 'Who can edit', tela: 'Anyone — a WYSIWYG markdown editor in the browser', them: 'Markdown files in a git repo; no official editing UI' },
      { feature: 'Search & AI answers', tela: TELA_ASK, them: 'Algolia DocSearch (and its Ask AI) — a third-party index; nothing built in' },
      { feature: 'Agents read & write (MCP)', tela: TELA_MCP, them: 'No official server (community plugins only)' },
      { feature: 'Generate docs from your code', tela: TELA_ATLAS, them: 'No — it renders the markdown you already wrote' },
    ],
    whySwitch: [
      'Nothing stands between writing and published — tela saves the page live; in Docusaurus every edit is a rebuild and a redeploy.',
      'The people who know the answer can write it down, in a browser, without a pull request against a repo.',
      'Atlas drafts pages from your repos and Jira, and a built-in MCP server lets agents keep writing them.',
    ],
    whenBetter:
      'For a public, versioned documentation site, Docusaurus is the better tool: docs versioning and i18n out of the box, MDX with React components, free hosting on GitHub Pages, and a large plugin ecosystem. Keeping docs in the repo also means they are reviewed in the same pull request as the code they describe — a discipline a wiki does not give you. tela is for internal team knowledge, not for shipping a branded docs site.',
    source: 'docusaurus.io — deployment, search and the 3.9/3.10 release notes; MIT license on facebook/docusaurus (verified 2026).',
    updated: 'September 9, 2026',
  },
  {
    slug: 'mkdocs',
    name: 'MkDocs',
    seoTitle: 'MkDocs alternative — a team wiki with no build step | tela',
    metaDescription:
      'An MkDocs and Material for MkDocs alternative. tela needs no build or deploy step, lets anyone edit in the browser, answers questions over your docs with citations, and lets agents write through a built-in MCP server.',
    heading: 'The MkDocs alternative that needs no build step',
    lead: 'MkDocs — in practice Material for MkDocs — is the standard way to turn a docs/ folder into a documentation site, and it is very good at it. It is also markdown in a repo, with a build and a deploy behind every change, client-side keyword search, and no official MCP server. In November 2025 Material announced it was entering maintenance mode, with new development moving to a successor project. tela is a wiki instead: save and it is live, anyone can edit, and agents read and write it.',
    rows: [
      { feature: 'Publishing', tela: 'Save — the page is live', them: 'mkdocs build + deploy (gh-deploy, Read the Docs, CI)' },
      { feature: 'Who can edit', tela: 'Anyone — a WYSIWYG markdown editor in the browser', them: 'Markdown files in a git repo; no official editing UI' },
      { feature: 'Search & AI answers', tela: TELA_ASK, them: 'Client-side keyword search (lunr); no AI answers documented' },
      { feature: 'Agents read & write (MCP)', tela: TELA_MCP, them: 'No official server (community plugins only)' },
      { feature: 'Generate docs from your code', tela: TELA_ATLAS, them: 'No — renders markdown you wrote (mkdocstrings pulls API reference from docstrings)' },
      { feature: 'Roadmap', tela: 'Actively developed', them: 'Material for MkDocs is in maintenance mode since Nov 2025 — fixes only, features moved to its successor' },
    ],
    whySwitch: [
      'No build, no deploy, no CI job — a page is live the moment you save it.',
      'Ask your docs a question and get an answer with citations, instead of client-side keyword search.',
      'Agents are first-class through a built-in MCP server, and Atlas drafts pages straight from your repos.',
    ],
    whenBetter:
      'Material for MkDocs is beautiful, Python-native, trivial to run, and — since 9.7.0 folded every previously sponsor-only feature into the free MIT release — more capable at zero cost than it has ever been. For a static, offline-capable docs site built from a docs/ folder, with mkdocstrings pulling API reference out of your docstrings, it is still a great choice, and MkDocs core is maintained separately.',
    source: 'mkdocs.org deployment docs; squidfunk.github.io/mkdocs-material — search setup, MIT license and the 2025-11-11 maintenance-mode announcement (verified 2026).',
    updated: 'September 9, 2026',
  },
  {
    slug: 'backstage-techdocs',
    name: 'Backstage TechDocs',
    seoTitle: 'Backstage TechDocs alternative — docs generated from your code | tela',
    metaDescription:
      'A Backstage TechDocs alternative. tela needs no CI job or object store, lets anyone edit in the browser, answers with citations, and Atlas generates a cited, coverage-checked wiki from your repos.',
    heading: 'tela vs Backstage TechDocs — generating docs, not just building them',
    lead: 'TechDocs is the docs-like-code standard for platform teams: markdown beside your code, built by CI, attached to the component that owns it in the Backstage catalog. That ownership model is genuinely good, and tela does not replace a service catalog. What TechDocs does not do is write anything — it builds markdown a human already wrote, has no AI answer layer in core, and its official MCP exposes catalog and scaffolder actions rather than your docs. tela is the other half of the problem.',
    rows: [
      { feature: 'Where the docs come from', tela: TELA_ATLAS, them: 'Builds and hosts the markdown you wrote' },
      { feature: 'Who can edit', tela: 'Anyone — a WYSIWYG markdown editor in the browser', them: 'Markdown in each repo; "Edit this page" links back out to the repo' },
      { feature: 'Ask your docs (AI)', tela: TELA_ASK, them: 'None in core — Lunr, Postgres or Elasticsearch search' },
      { feature: 'Agents read & write (MCP)', tela: TELA_MCP, them: 'Official MCP for catalog/scaffolder actions; no TechDocs actions' },
      { feature: 'Ownership & discovery', tela: 'Spaces, backlinks, semantic search', them: 'Stronger — docs hang off a catalog entity and inherit its owner' },
      { feature: 'To run it', tela: 'A docker-compose stack, or the free cloud tier', them: 'A Backstage monorepo you own, a CI job per repo, plus S3/GCS for output' },
    ],
    whySwitch: [
      'Atlas writes the first draft from the repo and flags what is not covered; TechDocs only builds pages somebody already wrote.',
      'Answers with citations over the whole wiki, instead of Lunr or an Elasticsearch cluster you operate yourself.',
      'Non-engineers can contribute — editing is a browser, not a pull request into each service repo.',
    ],
    whenBetter:
      'If you already run Backstage, TechDocs is the right home for service documentation, and tela does not try to replace the software catalog. Docs living in the repo means they are versioned with the code, reviewed in the same pull request, and inherit an owner — so a stale or unowned doc shows up as a catalog problem rather than going quietly unnoticed. That coupling is precisely what a standalone wiki gives up.',
    source: 'backstage.io — TechDocs architecture and FAQ, search engines, MCP actions backend; Apache-2.0, CNCF incubating (verified 2026).',
    updated: 'September 9, 2026',
  },
  {
    slug: 'mintlify',
    name: 'Mintlify',
    seoTitle: 'Mintlify alternative — open-source, self-hosted docs you own | tela',
    metaDescription:
      'An open-source, self-hostable Mintlify alternative for internal team knowledge. tela is AGPL markdown you own, with un-metered ask-your-docs, a built-in read/write MCP server, and Atlas.',
    heading: 'The open-source Mintlify alternative for docs you own',
    lead: 'Mintlify is a genuinely strong AI documentation platform — a real browser editor with live cursors, an assistant that cites its sources, automatic llms.txt, and an admin MCP that lets agents edit your pages. So the honest contrast is not "they have no AI". It is ownership and purpose: Mintlify is proprietary, self-hosting starts at Enterprise as a scoped engagement with their team, its AI is billed from a credit balance, and what it produces is a public docs site. tela is AGPL, self-hostable for free, and is the internal wiki your team works in.',
    rows: [
      { feature: 'License', tela: TELA_LICENSE, them: 'Proprietary SaaS' },
      { feature: 'Self-hostable', tela: TELA_SELFHOST, them: 'Enterprise only — a scoped deployment with their account team' },
      { feature: 'Ask your docs (AI)', tela: TELA_ASK, them: 'Assistant with citations — Pro and above, billed in credits' },
      { feature: 'Agents read & write (MCP)', tela: TELA_MCP, them: 'Admin MCP writes; the docs MCP your readers get is read-only' },
      { feature: 'Generate docs from your code', tela: TELA_ATLAS, them: 'Agent drafts pull requests from repos and PRs (Pro and above)' },
      { feature: 'Built for', tela: 'An internal team wiki', them: 'Public, branded product documentation' },
    ],
    whySwitch: [
      'Self-host for free under AGPL — Mintlify self-hosting begins at Enterprise and runs through their account team.',
      'Asking your own docs a question is not metered; Mintlify draws the assistant and the agent from a shared credit balance.',
      'It is a wiki for the whole team’s knowledge, not a publishing pipeline for a customer-facing docs site.',
    ],
    whenBetter:
      'For public product and API documentation, Mintlify is excellent and ahead of tela: a polished branded docs site, OpenAPI-driven API playgrounds, preview deployments on every pull request, real-time collaborative editing with live cursors, and an AI agent that opens documentation PRs off your commits. If your job is shipping developer docs to customers rather than running an internal wiki, Mintlify is the better tool.',
    source: 'mintlify.com/docs — self-host, editor, assistant, agent and MCP pages; mintlify.com/pricing (verified 2026).',
    updated: 'September 9, 2026',
  },
  {
    slug: 'logseq',
    name: 'Logseq',
    seoTitle: 'Logseq alternative for teams — open-source, self-hosted wiki | tela',
    metaDescription:
      'A team-ready Logseq alternative. tela is an open-source (AGPL), self-hostable wiki with canonical markdown, real-time multiplayer, roles, ask-your-docs with citations, and a built-in MCP server.',
    heading: 'The open-source Logseq alternative built for a team',
    lead: 'Logseq is a superb personal outliner — local-first, AGPL, block references, daily journals. It is not a team wiki, and the ground beneath it has shifted: the markdown-files-on-disk version is now "Logseq OG" on maintenance-only support, while the actively developed 2.0 keeps your graph in SQLite and is still labelled beta. tela is the other shape — a shared, permissioned team wiki whose canonical format stays markdown.',
    rows: [
      { feature: 'Built for', tela: 'A team — shared spaces, roles, permissions', them: 'One person’s graph' },
      { feature: 'Storage', tela: TELA_STORAGE, them: 'Markdown/Org files in "Logseq OG"; SQLite in the 2.0 DB version' },
      { feature: 'Real-time collaboration', tela: 'Yes — multiplayer editing', them: 'RTC in the 2.0 beta — alpha, invite-only, paid' },
      { feature: 'Ask your docs (AI)', tela: TELA_ASK, them: 'None official' },
      { feature: 'Agents read & write (MCP)', tela: TELA_MCP, them: 'No official server (community, via the desktop app’s local API)' },
      { feature: 'Generate docs from your code', tela: TELA_ATLAS, them: 'No' },
    ],
    whySwitch: [
      'A real team surface — shared spaces, roles and permissions, rather than one person’s graph with sync added on.',
      'Markdown stays canonical and server-side; Logseq’s file-based line is now maintenance-only, and 2.0’s markdown export is documented as lossy.',
      'Ask your docs with citations, and let agents write pages through a built-in MCP server.',
    ],
    whenBetter:
      'For personal knowledge work Logseq is excellent and tela does not try to compete: an outliner where every block is addressable, block references and embeds, daily journals as the capture surface, a real query language, flashcards, PDF annotation — and, in the OG line, plain files on your own disk that any editor can open. If you are one person thinking in an outline, stay with Logseq.',
    source: 'github.com/logseq — README, 2.0.1 release notes and db-version docs; the Logseq OG split announcement (verified 2026).',
    updated: 'September 9, 2026',
  },
  {
    slug: 'github-wiki',
    name: 'GitHub Wiki',
    seoTitle: 'GitHub Wiki alternative — for when you outgrow the repo wiki | tela',
    metaDescription:
      'Outgrown your GitHub Wiki? tela is an open-source (AGPL), self-hostable team wiki: nested spaces, real permissions, semantic search with citations, a built-in MCP server, and docs generated from your code.',
    heading: 'The GitHub Wiki alternative for when the repo wiki runs out',
    lead: 'A repository wiki is the right first move — free, already there, and a real git repo you can clone. Teams outgrow it in predictable ways, and GitHub’s own documentation names most of them: no page hierarchy beyond a hand-written sidebar, no pull requests or review on an edit, permissions welded to the repository, a soft limit of 5,000 files, and search engines that only index a wiki if the repo has 500 or more stars and public editing is turned off. tela is where that knowledge goes next.',
    rows: [
      { feature: 'Structure', tela: 'Nested spaces and pages, backlinks', them: 'A flat page list plus a hand-maintained _Sidebar' },
      { feature: 'Permissions', tela: 'Per-space roles and sharing', them: 'Tied to the repo — public, or collaborators only' },
      { feature: 'Search', tela: TELA_ASK, them: 'Its own siloed wiki search; wikis are not in code search' },
      { feature: 'Found by search engines', tela: 'Yes — public spaces are indexable, with a sitemap', them: 'Only for repos with 500+ stars and public editing disabled' },
      { feature: 'Agents read & write (MCP)', tela: TELA_MCP, them: 'GitHub’s official MCP server ships no wiki tools' },
      { feature: 'Generate docs from your code', tela: TELA_ATLAS, them: 'No' },
    ],
    whySwitch: [
      'Real structure and real permissions — nested spaces and per-space roles, instead of a flat page list whose access is whatever the repo’s happens to be.',
      'Findable: ask a question and get an answer with citations, and publish a space search engines will actually index.',
      'Agents write it — GitHub’s own MCP server exposes issues, pull requests and code, but no wiki tools at all.',
    ],
    whenBetter:
      'For a small project the repo wiki is hard to argue with: nothing to host, nothing to pay for, it sits beside the code with the same access list, it renders AsciiDoc and reStructuredText as happily as markdown, and it is a git repository you can clone and edit locally with full history. If a handful of pages next to one repo is genuinely all you need, stay there.',
    source: 'GitHub Docs — About wikis (indexing + 5,000-file notes), wiki permissions and wiki search; github/github-mcp-server toolsets (verified 2026).',
    updated: 'September 9, 2026',
  },
  {
    slug: 'appflowy',
    name: 'AppFlowy',
    seoTitle: 'AppFlowy alternative — fully open-source, self-hosted team wiki | tela',
    metaDescription:
      'An AppFlowy alternative that is open source all the way down. tela self-hosts with no seat cap, keeps canonical markdown, answers with citations, and ships a built-in MCP server so agents read and write.',
    heading: 'The AppFlowy alternative that is open source all the way down',
    lead: 'AppFlowy is a well-built open-source Notion alternative with a genuinely good native app. If you are self-hosting it, though, two details matter: the server that AppFlowy actually maintains for self-host is a closed-source commercial fork of its AGPL core, and its free self-host tier is one user seat per instance. tela is AGPL end to end, self-hosts for a team without a seat ceiling, and keeps markdown as the canonical format.',
    rows: [
      { feature: 'License', tela: TELA_LICENSE, them: 'AGPL clients; the maintained self-host server is a closed-source commercial fork' },
      { feature: 'Self-hostable', tela: TELA_SELFHOST, them: 'Yes — but the free tier is one user seat per instance' },
      { feature: 'Storage', tela: TELA_STORAGE, them: 'CRDT documents (Yrs); markdown is import/export' },
      { feature: 'Ask your docs (AI)', tela: TELA_ASK, them: 'AI search — metered on cloud; self-host is bring-your-own model' },
      { feature: 'Agents read & write (MCP)', tela: TELA_MCP, them: 'No official server we could find (community projects only)' },
      { feature: 'Generate docs from your code', tela: TELA_ATLAS, them: 'No' },
    ],
    whySwitch: [
      'The stack you self-host is the open-source one — AppFlowy’s maintained self-host server is a closed-source fork of its AGPL core.',
      'Self-host for an actual team: AppFlowy’s free self-hosted tier is a single user seat per instance.',
      'Markdown stays canonical, Atlas drafts pages from your repos, and a built-in MCP server lets agents keep writing them.',
    ],
    whenBetter:
      'AppFlowy is the better tool if you want Notion’s structure rather than a wiki: grids, boards and calendars with relations and rollups, all deepening release over release. It also has something tela does not — a real native Flutter app on desktop and mobile with true offline editing, and a free local-AI path that runs models on your own hardware through Ollama.',
    source: 'github.com/AppFlowy-IO — AGPL LICENSE, the AppFlowy-Cloud open-core notice and its self-host tier README; appflowy.com/pricing (verified 2026).',
    updated: 'September 9, 2026',
  },
  {
    slug: 'affine',
    name: 'AFFiNE',
    seoTitle: 'AFFiNE alternative — AGPL end to end, no self-host seat cap | tela',
    metaDescription:
      'An AFFiNE alternative for team knowledge. tela is AGPL end to end with no self-hosted seat cap, canonical markdown you own, ask-your-docs with citations, and a built-in read/write MCP server.',
    heading: 'The AFFiNE alternative for a team wiki you fully own',
    lead: 'AFFiNE is an ambitious open-source workspace, and its bet is the edgeless canvas — the same document as a page or an infinite whiteboard. It also ships a first-party MCP server, so this is not a comparison about who has agents. It is about licensing and shape: AFFiNE’s client is MIT but its self-host backend sits under a separate Enterprise Edition license, and a self-hosted workspace is capped at 10 seats without a Team license. tela is AGPL end to end, uncapped, and markdown-canonical.',
    rows: [
      { feature: 'License', tela: TELA_LICENSE, them: 'MIT client; the self-host backend is under its source-available Enterprise Edition license' },
      { feature: 'Self-hostable', tela: TELA_SELFHOST, them: 'Yes — a workspace is capped at 10 seats without a Team license' },
      { feature: 'Storage', tela: TELA_STORAGE, them: 'BlockSuite/Yjs documents; markdown is import/export' },
      { feature: 'Ask your docs (AI)', tela: TELA_ASK, them: 'Yes — self-host requires bring-your-own API keys' },
      { feature: 'Agents read & write (MCP)', tela: TELA_MCP, them: 'Built-in MCP — read tools by default, write tools rolling out' },
      { feature: 'Generate docs from your code', tela: TELA_ATLAS, them: 'No' },
    ],
    whySwitch: [
      'One license for the whole product — AGPL end to end, including the server you run.',
      'No seat ceiling on a self-hosted install; AFFiNE caps a self-hosted workspace at 10 seats without a Team license.',
      'Atlas generates a cited wiki from your repos and Jira — AFFiNE gives you a surface to write on, not one that drafts from your code.',
    ],
    whenBetter:
      'AFFiNE’s edgeless canvas is a real differentiator and tela has nothing like it: one document you can flip between a page and an infinite whiteboard, plus mind maps, presentation mode, and search that indexes text on the canvas. It is also local-first — unlimited local workspaces, free forever, no account — ships native mobile apps, and its docs actively support free self-hosting with prebuilt images. If your team thinks visually and wants diagramming and writing in one surface, AFFiNE is the more interesting tool.',
    source: 'github.com/toeverything/AFFiNE — root LICENSE and the backend EE license; docs.affine.pro self-host (seat quota, BYOK AI); affine.pro/mcp and /pricing (verified 2026).',
    updated: 'September 9, 2026',
  },
];

export const competitorSlugs = competitors.map((c) => c.slug);
