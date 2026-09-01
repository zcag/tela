package engine

import (
	"context"
	"strings"

	"github.com/zcag/tela/backend/internal/atlas/core"
)

// refineStage runs a grounded improvement pass over each narrative page —
// re-retrieving the relevant source and asking the model to fix unsupported
// claims, deepen thin sections, and validate diagrams/citations. Reference pages
// are skipped (they're spine-anchored and handled by repair).
//
// Resume idempotency: refine rewrites a page's body in place, so (unlike draft)
// "has a body" can't distinguish a draft-only body from an already-refined one —
// there's no per-stage marker to skip on. Instead refine is idempotent *by
// effect*: it's improvement-only and shrink-guarded (a re-refine never shortens a
// page below half its length, so it can't degrade or lose content), the same
// safety class as the bounded repair loop. Resuming into refine therefore re-runs
// it over all bodied pages — at worst extra LLM calls, never corruption.
type refineStage struct{}

func (refineStage) Name() core.StageName { return core.StageRefine }

func (refineStage) Run(ctx context.Context, rc *RunContext) error {
	// Per-page refines are independent: each worker re-retrieves and rewrites only
	// its own Pages[i] (reading + writing that one page's Body, persisting its own
	// row). Non-narrative/empty pages are no-ops but still tick progress so the
	// bar matches the page count, as the serial version did.
	rc.resetProgress()
	n := len(rc.Art.Pages)
	return parallelN(ctx, pageFanout, n, func(ctx context.Context, i int) error {
		p := &rc.Art.Pages[i]
		if p.Kind != core.PageNarrative || strings.TrimSpace(p.Body) == "" {
			rc.StepDone(n, "refining: %s", p.Title)
			return nil
		}
		chunks, err := narrativeChunks(ctx, rc, p)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			rc.Warn("refine %q: retrieval failed, keeping draft: %v", p.Title, err)
			rc.StepDone(n, "refining: %s (kept draft)", p.Title)
			return nil
		}
		user := refineUser(p.Title, p.Body, assembleContext(chunks))
		if rc.Source != nil && rc.Source.Type == core.SourceJira {
			user = refineUserJira(p.Title, p.Body, assembleContext(chunks))
		}
		improved, truncated, err := chatBody(ctx, rc, refineSystem, user, 0.3)
		if err == nil && truncated {
			// Refine REPLACES a complete draft. A reply cut off at max_tokens is a
			// half-written page, and the shrink guard below does not catch it — a
			// page truncated at 60% clears "at least half the length" comfortably.
			// Keeping the draft is strictly better than replacing it with a
			// prefix of itself.
			rc.Warn("refine %q hit the model's output cap — keeping the complete draft", p.Title)
			rc.StepDone(n, "refining: %s (kept draft)", p.Title)
			return nil
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Refine is improvement-only — keep the existing draft on a transient
			// failure rather than failing the whole run on one page.
			rc.Warn("refine %q failed, keeping draft: %v", p.Title, err)
			rc.StepDone(n, "refining: %s (kept draft)", p.Title)
			return nil
		}
		improved = sanitizePage(improved)
		if len(strings.TrimSpace(improved)) >= len(strings.TrimSpace(p.Body))/2 { // guard against a degenerate shrink
			p.Body = improved
			if err := rc.Store.UpdatePageBody(p.ID, improved); err != nil {
				return err
			}
		}
		rc.StepDone(n, "refining: %s", p.Title)
		return nil
	})
}
