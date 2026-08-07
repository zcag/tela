package engine

import (
	"fmt"
	"strings"
	"testing"

	"github.com/zcag/tela/backend/internal/atlas/core"
)

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
	batches := spineBatches(items, refListBudget)

	total := 0
	for i, b := range batches {
		list, query := renderSpineList(b)
		total += len(b)
		if len(list) > refListBudget {
			t.Fatalf("batch %d rendered %d chars, over the %d budget", i, len(list), refListBudget)
		}
		if query == "" && len(b) > 0 {
			t.Fatalf("batch %d has items but an empty retrieval query", i)
		}
	}
	if total != len(items) {
		t.Fatalf("batching lost items: %d of %d survived — the list is contractually COMPLETE, "+
			"so a dropped item becomes a page that lies about its coverage", total, len(items))
	}
	if len(batches) < 2 {
		t.Fatalf("4,503 items fitted in %d batch(es) — the budget is not being applied", len(batches))
	}
	unbounded, _ := renderSpineList(items)
	t.Logf("unbatched list would be %d chars; %d batches, largest %d", len(unbounded), len(batches), refListBudget)
}

// TestSpineBatches_SmallAndEmpty pins that ordinary pages are untouched: a
// surface that fits stays a single call (identical prompt to before), and a page
// with no extracted surface still gets drafted rather than skipped.
func TestSpineBatches_SmallAndEmpty(t *testing.T) {
	if got := len(spineBatches(spineFixture(50), refListBudget)); got != 1 {
		t.Fatalf("50 items produced %d batches, want 1 — small pages must be unaffected", got)
	}
	b := spineBatches(nil, refListBudget)
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
	for _, b := range spineBatches(items, refListBudget) {
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
	single := refUser("API", "- [export] a  (a.ts:1)\n", "ctx", 0, 1)
	if !strings.Contains(single, "An H1 title") || !strings.Contains(single, "SUMMARY:") {
		t.Fatal("single-part prompt lost the H1/summary instructions")
	}
	if strings.Contains(single, "PART 1 OF") {
		t.Fatal("single-part prompt should not mention parts at all")
	}

	first := refUser("API", "- [export] a  (a.ts:1)\n", "ctx", 0, 3)
	if !strings.Contains(first, "PART 1 OF 3") || !strings.Contains(first, "An H1 title") || !strings.Contains(first, "SUMMARY:") {
		t.Fatalf("part 1 must open the page and emit the summary: %.400s", first)
	}

	later := refUser("API", "- [export] b  (b.ts:2)\n", "ctx", 2, 3)
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
