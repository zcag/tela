package engine

import (
	"context"

	"github.com/zcag/tela/backend/internal/atlas/core"
)

const (
	repairThreshold = 0.95 // must-cover rate at which we stop
	repairMaxIter   = 3
)

// repairStage closes coverage gaps. While the must-cover surface (routes,
// entrypoints, flags, env, models) isn't fully documented, it regenerates the
// reference page responsible for the missing kind, explicitly listing what was
// dropped — then re-audits. Bounded iterations keep it from looping forever.
type repairStage struct{}

func (repairStage) Name() core.StageName { return core.StageRepair }

func (repairStage) Run(ctx context.Context, rc *RunContext) error {
	if err := repairMustCover(ctx, rc); err != nil {
		return err
	}
	if err := repairMermaid(ctx, rc); err != nil {
		return err
	}
	rc.Coverage = computeCoverage(rc)
	_ = rc.Store.SaveRunCoverage(rc.Run.ID, rc.Coverage)
	if g := mustCoverGaps(rc.Coverage.Gaps); len(g) > 0 {
		rc.Warn("residual gaps after repair: %s", gapSample(g, 12))
	}
	if rc.Coverage.MermaidInvalid > 0 {
		rc.Warn("residual mermaid issues after repair: %d invalid", rc.Coverage.MermaidInvalid)
	}
	return nil
}

// repairMustCover closes must-cover surface gaps by re-drafting the responsible
// reference pages, explicitly listing the dropped items. Bounded iterations.
func repairMustCover(ctx context.Context, rc *RunContext) error {
	for iter := 1; iter <= repairMaxIter; iter++ {
		rc.Coverage = computeCoverage(rc)
		if rc.Coverage.MustRate() >= repairThreshold {
			rc.Info("coverage sufficient: must-cover %.0f%%", 100*rc.Coverage.MustRate())
			return nil
		}
		missing := mustCoverGaps(rc.Coverage.Gaps)
		if len(missing) == 0 {
			return nil
		}
		rc.Step(iter, repairMaxIter, "repair pass %d: %d gap(s) in must-cover surface", iter, len(missing))

		// Collect the reference pages responsible for a missing kind, then
		// re-draft them concurrently (the outer iteration stays sequential — it
		// re-audits coverage between passes). Each worker writes only its own page.
		type target struct {
			i    int
			miss []core.Gap
		}
		var targets []target
		for i := range rc.Art.Pages {
			p := &rc.Art.Pages[i]
			if p.Kind != core.PageReference {
				continue
			}
			if miss := gapsForKinds(missing, p.SpineKinds); len(miss) > 0 {
				targets = append(targets, target{i, miss})
			}
		}
		if len(targets) == 0 {
			break // nothing actionable (gaps in kinds without a reference page)
		}
		// EXPERIMENT 2026-09-01 (atlas output-budget pack) — accept the pass only
		// if it actually helped. docs/atlas-output-budget-experiment.md
		//
		// A repair pass REGENERATES the whole page from the item list; it does not
		// receive the previous body. So every item the page already covered is
		// re-rolled on every pass, and the pass is only worth keeping if the audit
		// says it came out ahead. It frequently did not: measured across the four
		// production runs where repair moved the number at all, the must-covered
		// deltas were +27, +18, -72 and -41. Without an acceptance test the loop
		// publishes whichever sample it happened to draw last.
		//
		// refineStage has had exactly this instinct all along (its shrink guard
		// refuses a degenerate rewrite); repair overwrote unconditionally. Note
		// this is a RATCHET, not a fix for why a pass loses items — that is the
		// truncation the batching change addresses. It guarantees the loop can no
		// longer end below where it started.
		before := rc.Coverage.MustCovered
		prev := make([]string, len(targets))
		for t, tg := range targets {
			prev[t] = rc.Art.Pages[tg.i].Body
		}
		err := parallelN(ctx, pageFanout, len(targets), func(ctx context.Context, t int) error {
			p := &rc.Art.Pages[targets[t].i]
			body, err := redraftReference(ctx, rc, p, targets[t].miss)
			if err != nil {
				return err
			}
			p.Body = body
			return rc.Store.UpdatePageBody(p.ID, body)
		})
		if err != nil {
			return err
		}
		after := computeCoverage(rc)
		if after.MustCovered < before {
			for t, tg := range targets {
				rc.Art.Pages[tg.i].Body = prev[t]
				if uerr := rc.Store.UpdatePageBody(rc.Art.Pages[tg.i].ID, prev[t]); uerr != nil {
					return uerr
				}
			}
			rc.Warn("repair pass %d lost coverage (%d → %d must-cover items) — rolled back and stopping", iter, before, after.MustCovered)
			rc.Coverage = computeCoverage(rc)
			return nil
		}
		rc.Coverage = after
		if after.MustCovered == before {
			// The pass cost a full re-draft of every responsible page and moved
			// nothing. repairThreshold (0.95) is unreachable from most real
			// starting points, so without this the loop always burns all
			// repairMaxIter passes to arrive back where it began.
			rc.Info("repair pass %d changed no coverage (%d must-cover items) — stopping", iter, after.MustCovered)
			return nil
		}
		rc.Info("repair pass %d: must-cover %d → %d items", iter, before, after.MustCovered)
	}
	return nil
}

const mermaidRepairMaxIter = 2 // bound on the mermaid re-draft loop

// repairMermaid re-drafts pages whose mermaid blocks failed the structural check,
// instructing a valid, simple diagram. Bounded (≤2 iters); stops when no gaps.
func repairMermaid(ctx context.Context, rc *RunContext) error {
	for iter := 1; iter <= mermaidRepairMaxIter; iter++ {
		rc.Coverage = computeCoverage(rc)
		gaps := rc.Coverage.MermaidGaps
		if len(gaps) == 0 {
			return nil
		}
		// dedupe pages with at least one bad mermaid block
		badPages := map[string]bool{}
		for _, g := range gaps {
			badPages[g.Page] = true
		}
		rc.Step(iter, mermaidRepairMaxIter, "mermaid repair pass %d: %d invalid block(s) across %d page(s)", iter, len(gaps), len(badPages))

		// Re-draft each page with a bad mermaid block concurrently (outer loop
		// stays sequential — it re-audits between passes). Each worker writes its
		// own page.
		var targets []int
		for i := range rc.Art.Pages {
			if badPages[rc.Art.Pages[i].Slug] {
				targets = append(targets, i)
			}
		}
		if len(targets) == 0 {
			break
		}
		err := parallelN(ctx, pageFanout, len(targets), func(ctx context.Context, t int) error {
			p := &rc.Art.Pages[targets[t]]
			body, err := redraftMermaid(ctx, rc, p)
			if err != nil {
				return err
			}
			p.Body = body
			return rc.Store.UpdatePageBody(p.ID, body)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// mermaidRepairSystem / mermaidRepairUser keep the repair prompt local to this
// file (prompts.go is owned by another change). The instruction forces a valid,
// simple diagram so the cheap structural check passes on the next audit.
const mermaidRepairSystem = `You are fixing broken Mermaid diagrams in an existing technical documentation page.
Return the COMPLETE page markdown, unchanged except that every ` + "```mermaid```" + ` block is a VALID, SIMPLE Mermaid diagram.
Rules for each diagram:
- Start with one diagram-type header (graph, flowchart, sequenceDiagram, classDiagram, stateDiagram, erDiagram, gantt, journey, pie, mindmap, or timeline).
- Use exactly one diagram type per block.
- ALWAYS quote every node label, e.g. A["Schema: Types"] — never leave a label unquoted.
- An UNQUOTED label MUST NOT contain ':' '(' ')' or any non-ASCII character (e.g. ü, ç, ş, İ) — mermaid's parser breaks on them. Put any such label in double quotes.
- Keep brackets ([] () {}) balanced.
Do not add commentary, headings, or code fences around your answer — output only the page markdown.`

// redraftMermaid asks the model to rewrite a page, repairing its mermaid blocks.
func redraftMermaid(ctx context.Context, rc *RunContext, p *core.Page) (string, error) {
	user := "PAGE TITLE: " + p.Title + "\n\nREWRITE THIS PAGE, fixing only its Mermaid diagrams per the rules:\n\n" + p.Body
	body, truncated, err := chatBody(ctx, rc, mermaidRepairSystem, user, 0.2)
	if err == nil && truncated {
		// Same hazard as refine: this rewrites a complete page, so a reply cut off
		// at max_tokens would replace it with a prefix of itself.
		rc.Warn("mermaid repair of %q hit the model's output cap — keeping the existing page", p.Title)
		return p.Body, nil
	}
	return sanitizePage(body), err
}

// redraftReference regenerates a reference page, forcing the previously-missing
// items to appear. Batching, evidence and truncation handling are the draft
// stage's (renderReferenceBody) — repair only supplies the omissions, so the two
// paths cannot drift apart the way they did when each batched for itself.
func redraftReference(ctx context.Context, rc *RunContext, p *core.Page, miss []core.Gap) (string, error) {
	missBy := make(map[string]core.Gap, len(miss))
	for _, g := range miss {
		missBy[g.Name] = g
	}
	// The summary is deliberately dropped: repair rewrites the body, and a page's
	// standfirst is owned by the draft stage.
	body, _, err := renderReferenceBody(ctx, rc, p, missBy)
	return body, err
}

func mustCoverGaps(gaps []core.Gap) []core.Gap {
	var out []core.Gap
	for _, g := range gaps {
		if core.MustCoverKinds[g.Kind] {
			out = append(out, g)
		}
	}
	return out
}

func gapsForKinds(gaps []core.Gap, kinds []core.SpineKind) []core.Gap {
	want := map[core.SpineKind]bool{}
	for _, k := range kinds {
		want[k] = true
	}
	var out []core.Gap
	for _, g := range gaps {
		if want[g.Kind] {
			out = append(out, g)
		}
	}
	return out
}
