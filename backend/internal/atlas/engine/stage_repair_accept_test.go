package engine

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/zcag/tela/backend/internal/atlas/core"
)

// EXPERIMENT 2026-09-01 (atlas output-budget pack) — docs/atlas-output-budget-experiment.md

// fakeStore records page-body writes and no-ops everything else the engine
// touches. Only UpdatePageBody matters here: the rollback has to reach the
// STORE, not just the in-memory page, or the run publishes the regressed body.
type fakeStore struct {
	mu     sync.Mutex
	writes map[int64][]string
}

func newFakeStore() *fakeStore { return &fakeStore{writes: map[int64][]string{}} }

func (f *fakeStore) UpdatePageBody(pageID int64, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes[pageID] = append(f.writes[pageID], body)
	return nil
}
func (f *fakeStore) lastWrite(pageID int64) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	w := f.writes[pageID]
	if len(w) == 0 {
		return ""
	}
	return w[len(w)-1]
}

func (f *fakeStore) AppendEvent(core.Event) error                        { return nil }
func (f *fakeStore) UpdateRun(*core.Run) error                           { return nil }
func (f *fakeStore) GetRun(int64) (*core.Run, error)                     { return nil, nil }
func (f *fakeStore) SetSourceRef(int64, string) error                    { return nil }
func (f *fakeStore) SaveFiles(int64, []core.File) error                  { return nil }
func (f *fakeStore) SaveSpine(int64, []core.SpineItem) error             { return nil }
func (f *fakeStore) SaveChunks(int64, []core.Chunk) error                { return nil }
func (f *fakeStore) SaveVectors([]core.Chunk) error                      { return nil }
func (f *fakeStore) SavePages(int64, []core.Page) error                  { return nil }
func (f *fakeStore) SaveRunCoverage(int64, core.Coverage) error          { return nil }
func (f *fakeStore) SaveRunStats(int64, core.RunStats) error             { return nil }
func (f *fakeStore) CopyChunksToRun(int64, int64, []string) (int, error) { return 0, nil }
func (f *fakeStore) RunFiles(int64) ([]core.File, error)                 { return nil, nil }
func (f *fakeStore) RunSpine(int64) ([]core.SpineItem, error)            { return nil, nil }
func (f *fakeStore) RunChunksWithVectors(int64) ([]core.Chunk, error)    { return nil, nil }
func (f *fakeStore) RunPagesFull(int64) ([]core.Page, error)             { return nil, nil }

const goodBody = "# Flags\n\nThis reference documents --alpha, --beta and --gamma in enough words to clear the body floor.\n"

// repairFixture: a 4-flag must-cover surface with a page covering 3 of them
// (75% — under repairThreshold, so the loop engages).
func repairFixture(t *testing.T, answer func(call int) string) (*RunContext, *fakeStore, *stubLLM) {
	t.Helper()
	spine := []core.SpineItem{
		{Kind: core.KindFlag, Name: "--alpha", File: "cli.py", Line: 1},
		{Kind: core.KindFlag, Name: "--beta", File: "cli.py", Line: 2},
		{Kind: core.KindFlag, Name: "--gamma", File: "cli.py", Line: 3},
		{Kind: core.KindFlag, Name: "--delta", File: "cli.py", Line: 4},
	}
	st := newFakeStore()
	stub := newStubLLM(t, answer)
	rc := &RunContext{
		Run: &core.Run{ID: 1}, Store: st, LLM: stub.client(),
		Art: core.Artifacts{Spine: spine, Pages: []core.Page{{
			ID: 7, Kind: core.PageReference, Title: "Flags", Slug: "flags",
			SpineKinds: []core.SpineKind{core.KindFlag}, Body: goodBody,
		}}},
	}
	return rc, st, stub
}

// TestRepair_RollsBackAPassThatLosesCoverage is the ratchet. Repair regenerates
// a page from scratch and does not see its previous body, so a pass can come
// back covering FEWER items — measured in production at -72 and -41 must-cover
// items. The loop must end no worse than it started, in memory AND in the store.
func TestRepair_RollsBackAPassThatLosesCoverage(t *testing.T) {
	worse := "# Flags\n\nThis rewrite mentions only --alpha and forgets the rest of the surface entirely.\n"
	rc, st, stub := repairFixture(t, func(int) string { return worse })

	if err := repairMustCover(context.Background(), rc); err != nil {
		t.Fatalf("repair failed: %v", err)
	}
	if got := rc.Art.Pages[0].Body; got != goodBody {
		t.Fatalf("in-memory body was not rolled back:\n%q", got)
	}
	if got := st.lastWrite(7); got != goodBody {
		t.Fatalf("the STORE kept the regressed body — publish would ship it:\n%q", got)
	}
	if rc.Coverage.MustCovered != 3 {
		t.Fatalf("coverage after rollback = %d, want the pre-pass 3", rc.Coverage.MustCovered)
	}
	if n := stub.calls.Load(); n != 1 {
		t.Fatalf("%d calls — a losing pass must stop the loop, not run all %d passes", n, repairMaxIter)
	}
}

// TestRepair_StopsWhenAPassChangesNothing: repairThreshold (0.95) is unreachable
// from most real starting points, so the loop used to burn all repairMaxIter
// passes — a full re-draft of every responsible page each time — to arrive back
// where it began. In production this cost 6m10s to achieve nothing.
func TestRepair_StopsWhenAPassChangesNothing(t *testing.T) {
	rc, _, stub := repairFixture(t, func(int) string { return goodBody })

	if err := repairMustCover(context.Background(), rc); err != nil {
		t.Fatalf("repair failed: %v", err)
	}
	if n := stub.calls.Load(); n != 1 {
		t.Fatalf("%d calls for a no-op pass, want 1 — the loop must not keep paying", n)
	}
}

// TestRepair_KeepsAPassThatImproves proves the ratchet doesn't block real
// repairs: a pass that adds the missing item is accepted and persisted.
func TestRepair_KeepsAPassThatImproves(t *testing.T) {
	better := "# Flags\n\nThis rewrite documents --alpha, --beta, --gamma and also the missing --delta flag.\n"
	rc, st, _ := repairFixture(t, func(int) string { return better })

	if err := repairMustCover(context.Background(), rc); err != nil {
		t.Fatalf("repair failed: %v", err)
	}
	if !strings.Contains(rc.Art.Pages[0].Body, "--delta") {
		t.Fatalf("an improving pass was discarded: %q", rc.Art.Pages[0].Body)
	}
	if !strings.Contains(st.lastWrite(7), "--delta") {
		t.Fatal("an improving pass was not persisted")
	}
	if rc.Coverage.MustCovered != 4 {
		t.Fatalf("coverage = %d, want 4 after a successful repair", rc.Coverage.MustCovered)
	}
}
