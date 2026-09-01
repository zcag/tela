package engine

import (
	"fmt"
	"strings"
	"testing"

	"github.com/zcag/tela/backend/internal/atlas/core"
)

// plainRender is the item renderer with no evidence attached — the shape the
// budget test cares about (evidence length is exercised in the budget tests).
func plainRender(it core.SpineItem) string { return renderSpineItem(it, "") }

// batchAll is how the draft stage cuts a surface into parts: repeated nextBatch
// over a cursor. The production loop (renderReferenceBody) does exactly this,
// re-cutting the remainder when a part comes back truncated.
func batchAll(items []core.SpineItem, budget, maxItems int) [][]core.SpineItem {
	if len(items) == 0 {
		return [][]core.SpineItem{nil}
	}
	var out [][]core.SpineItem
	for i := 0; i < len(items); {
		n := nextBatch(items[i:], budget, maxItems, plainRender)
		if n == 0 {
			n = 1
		}
		out = append(out, items[i:i+n])
		i += n
	}
	return out
}

func spineFixture(n int) []core.SpineItem {
	items := make([]core.SpineItem, n)
	for i := range items {
		items[i] = core.SpineItem{
			Kind: core.KindExport,
			Name: fmt.Sprintf("SomeExportedSymbolNumber%04d", i),
			File: fmt.Sprintf("src/pkg/area%02d/file_%04d.ts", i%40, i),
			Line: i + 1,
		}
	}
	return items
}

// TestSpineBatches_BoundsThePrompt is the regression guard for the crash: the
// reference-page item list was unbounded, so a 4,503-item surface produced a
// ~406 KB prompt that OOM-killed the local model and took every in-flight
// request down with it. No batch's rendered list may exceed the budget.
func TestSpineBatches_BoundsThePrompt(t *testing.T) {
	items := spineFixture(4503) // the surface that actually crashed it
	perBatch, _ := refBatchPlan(len(items), 4096)
	batches := batchAll(items, refListBudget, perBatch)

	total := 0
	for i, b := range batches {
		list := renderSpineList(b, plainRender)
		total += len(b)
		if len(list) > refListBudget {
			t.Fatalf("batch %d rendered %d chars, over the %d budget", i, len(list), refListBudget)
		}
		if len(b) > perBatch {
			t.Fatalf("batch %d carries %d items, over the %d the model can answer", i, len(b), perBatch)
		}
	}
	if total != len(items) {
		t.Fatalf("batching lost items: %d of %d survived — the list is contractually COMPLETE, "+
			"so a dropped item becomes a page that lies about its coverage", total, len(items))
	}
	if len(batches) < 2 {
		t.Fatalf("4,503 items fitted in %d batch(es) — the budget is not being applied", len(batches))
	}
	unbounded := renderSpineList(items, plainRender)
	t.Logf("unbatched list would be %d chars; %d batches, largest %d", len(unbounded), len(batches), refListBudget)
}

// TestSpineBatches_SmallAndEmpty pins that ordinary pages are untouched: a
// surface that fits stays a single call (identical prompt to before), and a page
// with no extracted surface still gets drafted rather than skipped.
func TestSpineBatches_SmallAndEmpty(t *testing.T) {
	// 35 items is the compass baseline's must-cover surface — the calibrated case.
	perBatch, compact := refBatchPlan(35, 4096)
	if compact {
		t.Fatal("a 35-item baseline surface must stay in the rich (section-per-item) form")
	}
	if got := len(batchAll(spineFixture(35), refListBudget, perBatch)); got != 1 {
		t.Fatalf("35 items produced %d batches, want 1 — the calibrated baselines must be unaffected", got)
	}
	b := batchAll(nil, refListBudget, perBatch)
	if len(b) != 1 || len(b[0]) != 0 {
		t.Fatalf("empty surface produced %v, want exactly one empty batch", b)
	}
}

// TestSpineBatches_OversizedItemKept proves a single item larger than the whole
// budget is still emitted (in a batch of its own) rather than silently dropped.
func TestSpineBatches_OversizedItemKept(t *testing.T) {
	items := []core.SpineItem{
		{Kind: core.KindExport, Name: "small", File: "a.ts", Line: 1},
		{Kind: core.KindExport, Name: strings.Repeat("X", refListBudget*2), File: "b.ts", Line: 2},
		{Kind: core.KindExport, Name: "also-small", File: "c.ts", Line: 3},
	}
	got := 0
	for _, b := range batchAll(items, refListBudget, 50) {
		got += len(b)
	}
	if got != 3 {
		t.Fatalf("kept %d of 3 items — coverage beats prompt tidiness", got)
	}
}

// TestRefUser_PartsDoNotRepeatThePage checks the multi-part prompts: only part 1
// opens the page and asks for a summary marker, or the published body would carry
// an H1 and a standfirst per part. A single-part page must get the original
// prompt unchanged.
func TestRefUser_PartsDoNotRepeatThePage(t *testing.T) {
	single := refUser(refPrompt{title: "API", itemList: "- [export] a  (a.ts:1)\n", part: 0, total: 1, evidence: true})
	if !strings.Contains(single, "An H1 title") || !strings.Contains(single, "SUMMARY:") {
		t.Fatal("single-part prompt lost the H1/summary instructions")
	}
	if strings.Contains(single, "PART 1 OF") {
		t.Fatal("single-part prompt should not mention parts at all")
	}

	first := refUser(refPrompt{title: "API", itemList: "- [export] a  (a.ts:1)\n", part: 0, total: 3, evidence: true})
	if !strings.Contains(first, "PART 1 OF 3") || !strings.Contains(first, "An H1 title") || !strings.Contains(first, "SUMMARY:") {
		t.Fatalf("part 1 must open the page and emit the summary: %.400s", first)
	}

	later := refUser(refPrompt{title: "API", itemList: "- [export] b  (b.ts:2)\n", part: 2, total: 3, evidence: true})
	if !strings.Contains(later, "PART 3 OF 3") {
		t.Fatal("later part lost its part marker")
	}
	if strings.Contains(later, "SUMMARY:") {
		t.Fatal("later parts must NOT emit a summary marker — one page, one standfirst")
	}
	if !strings.Contains(later, "Do NOT write an H1") {
		t.Fatal("later parts must be told not to restate the title")
	}
}

// TestRefUser_TrackerPathKeepsItsExcerpts pins that the grounding swap is
// code-path only: a source whose spine items have no real file:line (jira) must
// still receive the retrieved-excerpt block it has always been given, or the
// tracker's reference pages lose their only source of description.
func TestRefUser_TrackerPathKeepsItsExcerpts(t *testing.T) {
	tracker := refUser(refPrompt{title: "Status Board", itemList: "- [state] In Progress  (status.md:3)\n",
		context: "### status.md:1-9\nIn Progress: 12 issues\n", part: 0, total: 1, evidence: false})
	if !strings.Contains(tracker, "In Progress: 12 issues") {
		t.Fatal("the tracker prompt lost its retrieved excerpts")
	}
	if !strings.Contains(tracker, "(from the excerpts)") {
		t.Fatal("the tracker prompt should still point the model at the excerpts")
	}

	code := refUser(refPrompt{title: "Flags", itemList: "- [cli_flag] --dev  (cli.py:9)\n    9| add_argument\n",
		part: 0, total: 1, evidence: true})
	if strings.Contains(code, "Relevant source excerpts") {
		t.Fatal("the code path must not append an empty excerpt block")
	}
	if !strings.Contains(code, "source lines shown under it") {
		t.Fatal("the code path should point the model at each item's own lines")
	}
}
