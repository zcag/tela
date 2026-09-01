package engine

import "fmt"

// Generation prompts. The draft prompt is deliberately strict — ground only in
// the provided code, cite file:line, diagram with Mermaid — which is the
// retrieve-then-write pattern that holds up on a local model.

const outlineSystem = `You are a principal engineer planning the technical documentation for a software repository.
Design a concise, NON-OVERLAPPING set of wiki pages that together explain the whole system to a new engineer:
architecture and how the pieces fit, each core subsystem, the key end-to-end flows, the data model, and how it's configured and run.`

func outlineUser(overview string) string {
	return overview + `

Return ONLY a JSON array (no prose, no code fences) of 5 to 12 pages, ordered so a reader can follow them top to bottom:
[
  {"title": "Architecture Overview", "summary": "one sentence on what this page covers", "topics": ["keywords","for","retrieval"]},
  ...
]
Rules:
- Start with a high-level architecture/overview page.
- One page per real subsystem or major flow — name them after what's actually in the code above.
- Do NOT create pages that merely list endpoints, CLI flags, or environment variables — those reference pages are generated automatically.
- "topics" are search keywords (symbols, file names, concepts) used to retrieve the right source for that page.`
}

const outlineJiraSystem = `You are a delivery lead writing the living documentation for a Jira project — an issue TRACKER, not a codebase.
A tracker's documentation is about its CURRENT STATE and PROGRESS: what is in each status, how far each epic has come, what is blocked or in flight, and what moved recently — NOT a static architecture overview.
Plan a TIGHT, NON-OVERLAPPING set of AT MOST TWO narrative pages that do not re-derive the same state. Be lean: no filler, no overlapping pages.`

func outlineJiraUser(overview string) string {
	return overview + `

Return ONLY a JSON array (no prose, no code fences) of AT MOST 2 pages, in this order:
[
  {"title": "Current State & Progress", "summary": "the dashboard — status distribution, epic completion, in-flight/blocked, recent activity", "topics": ["status","progress","blocked","in progress","recent"]},
  {"title": "Epics & Roadmap", "summary": "per-epic breakdown and what each epic is for", "topics": ["epic","roadmap"]}
]
Rules:
- Plan AT MOST 2 narrative pages, and they MUST be these two (or near-identical titles):
  1. "Current State & Progress" — the HEADLINE dashboard, built from status.md: per-status counts, the epic-completion table, what's in-flight and blocked, and recent activity. This single page folds in workflow/statuses, work-areas, and momentum — do NOT split those out.
  2. "Epics & Roadmap" — a per-epic breakdown (what each epic covers and how far it has come).
- Do NOT plan separate "Workflow & Statuses", "Work Areas & In-Flight", or "Recent Activity & Momentum" pages — that content belongs INSIDE the dashboard.
- Do NOT create pages that merely list the status board, issue types, or components — those reference pages are generated automatically.
- The two pages MUST cover DISTINCT ground: the status-count/blocked/in-flight state lives only on the dashboard; the per-epic detail lives only on Epics & Roadmap. Never repeat the same status counts or epic table across both.
- "topics" are search keywords (status names, epic keys/names, component names, concepts) used to retrieve the right source for that page.`
}

const draftSystem = `You are an expert technical writer and software architect documenting a codebase.
You write each wiki page GROUNDED ONLY in the source excerpts you are given — never invent files, functions, routes, or behavior that is not in the provided code. If something isn't in the excerpts, say so rather than guessing.`

// mermaidLabelRules is the one place that tells the model how to write a Mermaid
// diagram that mermaid's parser actually accepts. ALWAYS-quote node labels: the
// parser breaks on a raw ':' '(' ')' or any non-ASCII char in an UNQUOTED label
// (the exact failure QA saw — labels like Schema: Types, Güven, Tür). Shared by
// every draft/refine/repair prompt so the rule can't drift between paths.
const mermaidLabelRules = "At least one Mermaid diagram (use vertical 'graph TD' / 'sequenceDiagram' / 'classDiagram'); wrap it in a ```mermaid fence. " +
	`ALWAYS quote node labels, e.g. A["Schema: Types"] — never write a label unquoted. ` +
	"An UNQUOTED label MUST NOT contain ':' '(' ')' or any non-ASCII character (e.g. ü, ç, ş, İ); mermaid's parser breaks on them. Put any such label inside double quotes."

// codeDensityRules forbid the same generic filler the jira path forbids, applied
// to the git/code draft so code docs stay dense + factual (no rationale-essay
// sections that say nothing the code doesn't).
const codeDensityRules = `

Keep it dense and factual — no generic filler:
- NO "Why This Architecture?", "Why This Matters", "Key Takeaways", management-speak, or "ultimately…/this enables…" rationale/justification sections. Explain the real "why" inline where the code supports it; do not pad with essay sections.`

func draftUser(title, summary, context string) string {
	return `# Page to write: ` + title + `
` + summary + `

You are given relevant source excerpts below. Each is headed by its file path and line range.

` + context + `

Write the page in Markdown:
1. Begin with a <details> block titled "Sources" listing the file paths you used.
2. An H1 title, then a 1-2 paragraph introduction.
3. Logical H2/H3 sections explaining architecture, components, control/data flow, and the "why" — derived ONLY from the excerpts.
4. ` + mermaidLabelRules + `
5. For every significant claim, cite the source inline as ` + "`Sources: [path:start-end]`" + ` using the real line ranges shown.
6. Use tables where they clarify. Keep it accurate and concrete — name the real symbols.
` + summaryDirective + `
Do not include any other text outside the page itself.`
}

// summaryDirective asks the drafting model to emit a one-line, user-facing
// standfirst for the page it just wrote, on the very last line, wrapped in an
// HTML comment so it's trivially extractable and never renders. Atlas strips it
// from the body and stamps it as the page's summary (locked), so the auto-
// summarizer never has to re-derive it. Shared by the narrative + reference
// draft prompts.
const summaryDirective = `7. On the VERY LAST line, output a one-sentence plain-text summary of THIS page for someone browsing a list of pages — grounded only in what the page says, no markdown, and stating the substance directly (do NOT open with "This page…" / "This document…") — wrapped exactly as: <!-- SUMMARY: your sentence -->`

// draftUserCode is the git/code draft prompt: the base draft + the code-density
// rule (no rationale-essay filler). The jira path uses draftUserJira instead.
func draftUserCode(title, summary, context string) string {
	return draftUser(title, summary, context) + codeDensityRules
}

// jiraDensityRules are appended to the draft/refine prompts ONLY on the Jira
// (tracker) path. They keep tracker pages dense and factual — no management-speak,
// no speculation beyond the tickets, no repeated state across pages. The git/code
// prompts never see these, so code docs keep their architecture/why narrative.
const jiraDensityRules = `

This is a TRACKER page (a Jira project's living state), so write it dense and factual:
- Prefer TABLES for state (status counts, epic progress, in-flight/blocked) over prose paragraphs.
- NO "Why This Matters", "Key Takeaways", management-speak, or "ultimately…/this enables…" filler sections. State the facts; stop.
- NO speculation beyond the tickets — do NOT invent process or workflow that isn't in the data (e.g. don't say work merges "typically via a pull request"). If it's not in the tickets/status, don't claim it.
- Cover DISTINCT ground: do NOT repeat the same status counts or epic table that another page owns. The dashboard owns status distribution + blocked/in-flight; the roadmap owns per-epic detail.`

func draftUserJira(title, summary, context string) string {
	return draftUser(title, summary, context) + jiraDensityRules
}

func refineUserJira(title, draft, context string) string {
	return refineUser(title, draft, context) + jiraDensityRules
}

const refineSystem = `You are a senior reviewer improving a draft documentation page. You make it more accurate, complete, and clear —
strictly grounded in the provided source. You remove or correct any claim the source doesn't support, deepen thin sections,
and ensure diagrams and citations are valid. You never invent. Return ONLY the improved page.`

func refineUser(title, draft, context string) string {
	return `Improve the documentation page titled "` + title + `".

<existing_page>
` + draft + `
</existing_page>

<source_excerpts>
` + context + `
</source_excerpts>
(The source excerpts are the ONLY ground truth — each headed by file:line.)

Output ONLY the final improved page markdown, beginning with its <details> Sources block.
Do NOT echo these instructions, the tags, or any header like "existing page". Improving it means:
- Fix or delete any statement not supported by the excerpts.
- Expand sections that are thin; add concrete detail (real symbol/file names) where the source supports it.
- Keep the <details> Sources block, the Mermaid diagram(s), and inline ` + "`Sources: [path:start-end]`" + ` citations — correct any wrong line ranges. ` + mermaidLabelRules + `
- Keep it well-structured Markdown. Return only the page, no commentary.`
}

const refSystem = `You are documenting a software system's interface surface. You are given the COMPLETE, authoritative,
machine-extracted list of items — your job is to document EVERY item, grounded in the source you are given. Do not omit any item, and do not invent items not in the list.
Never pad an item by restating its own name back as its description.`

// refPrompt is the input to one reference-page call. It is a struct because the
// shape depends on several independent facts (which part, how dense, where the
// grounding comes from) and a six-argument call reads as noise at the call site.
type refPrompt struct {
	title    string
	itemList string
	// context is the retrieved-excerpt block, used ONLY where per-item evidence
	// is unavailable (see evidence).
	context string
	part    int
	total   int
	// compact writes the items as table rows instead of a section each, for a
	// surface too large to document richly inside one answer.
	compact bool
	// evidence reports that each item in itemList is followed by its own source
	// lines, so the model is told to read them instead of being handed a block of
	// excerpts retrieved for the batch as a whole.
	evidence bool
}

// refUser builds the reference-page prompt. A page whose surface is too large
// for one call is written in PARTS (see refBatchPlan): part 0 opens the page,
// and later parts continue it — they must not restate an H1, re-introduce the
// page, or emit a second summary marker, or the published body ends up with a
// title and standfirst per part. total==1 produces exactly the single-call
// prompt this used to send, so pages whose surface fits one call — which is
// every calibrated baseline — are unaffected.
func refUser(rp refPrompt) string {
	scope := "This is the COMPLETE list of items to document (extracted directly from the source — do not add or drop any):"
	if rp.total > 1 {
		scope = fmt.Sprintf(
			"This surface is too large for one pass, so you are writing PART %d OF %d. Below is the complete list of items for THIS PART only (extracted directly from the source — do not add or drop any):",
			rp.part+1, rp.total)
	}

	// Where the description comes from. With per-item evidence the model reads
	// the lines printed under each item; without it, the retrieved excerpt block
	// is all there is (the tracker path, whose items have no real file:line).
	from, excerpts := "(from the excerpts)", "\nRelevant source excerpts for context:\n\n"+rp.context+"\n"
	if rp.evidence {
		from, excerpts = "(from the source lines shown under it)", ""
	}

	// item/group carry no list number: the opening and continuation shapes number
	// them differently, and the original prompts did too.
	item := "Document EVERY item in the list — a table or a section each, with what it is, where it lives (`path:line`), and " + from + " what it does."
	group := "Group sensibly (e.g. by prefix or area) if there are many."
	if rp.compact {
		item = "Document EVERY item in the list as ONE ROW of a Markdown table with columns | Item | Location | Description |. Do NOT give each item its own heading — this surface is far too large for that. Location is `path:line`; Description is what the item actually does " + from + "."
		group = "Group into a few tables by prefix or area if that helps."
	}

	var shape string
	switch {
	case rp.total == 1:
		shape = "Write a Markdown reference page:\n1. An H1 title and a short intro explaining what this surface is.\n2. " +
			item + "\n3. " + group + "\n" + summaryDirective
	case rp.part == 0:
		shape = "Write the OPENING of a Markdown reference page:\n1. An H1 title and a short intro explaining what this surface is.\n2. " +
			item + "\n3. " + group + " Later parts will continue this page, so do not write a conclusion.\n" + summaryDirective
	default:
		shape = "CONTINUE the reference page — its title, introduction and earlier items are already written:\n" +
			"1. Do NOT write an H1, do NOT re-introduce the page, and do NOT write a conclusion or a summary line.\n" +
			"2. Start directly with the content for this part's items.\n3. " + item + "\n4. " + group
	}

	return `# Reference page: ` + rp.title + `

` + scope + `

` + rp.itemList + `
` + excerpts + `
` + shape + `
Be exhaustive: every listed item must appear. If an item's behaviour is not visible in the source you were given, document its location and signature anyway.`
}
