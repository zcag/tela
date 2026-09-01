package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zcag/tela/backend/internal/atlas/core"
	"github.com/zcag/tela/backend/internal/atlas/llm"
)

// EXPERIMENT 2026-09-01 (atlas output-budget pack) — docs/atlas-output-budget-experiment.md

// TestRefBatchPlan_LeavesCalibratedBaselinesAlone is the fidelity guard. The
// pipeline's constants were tuned against small surfaces (compass 35 must-cover
// items, udn 17, COM 15, nest 20) and those runs must keep producing the exact
// single-call prompt they always did — total==1 is what makes refUser emit it.
func TestRefBatchPlan_LeavesCalibratedBaselinesAlone(t *testing.T) {
	for _, n := range []int{1, 15, 17, 20, 35, 50} {
		perBatch, compact := refBatchPlan(n, 4096)
		if compact {
			t.Errorf("%d items went compact — the baselines must stay in section form", n)
		}
		if n > perBatch {
			t.Errorf("%d items would split into parts (perBatch=%d); baselines must stay one call", n, perBatch)
		}
	}
}

// TestRefBatchPlan_SplitsTheSurfacesThatTruncated pins the regime the constants
// were never exercised in. SerpApi's 160-item flag+env page came back cut off
// mid-word, losing 19 flags and all 36 environment variables; repowise's 3,817
// exports lost ~75% of the list.
func TestRefBatchPlan_SplitsTheSurfacesThatTruncated(t *testing.T) {
	perBatch, compact := refBatchPlan(160, 4096)
	if compact {
		t.Error("160 items should still be rich — compact is for surfaces an order of magnitude larger")
	}
	if parts := (160 + perBatch - 1) / perBatch; parts < 2 {
		t.Fatalf("160 items still planned as %d part(s) — this is the page that truncated", parts)
	}

	perBatch, compact = refBatchPlan(3817, 4096)
	if !compact {
		t.Fatal("3,817 items must switch to the table form or the page costs ~75 calls")
	}
	parts := (3817 + perBatch - 1) / perBatch
	if parts > 40 {
		t.Fatalf("3,817 items planned as %d parts — too expensive; compact mode is meant to bound this", parts)
	}
}

// TestRefBatchPlan_UnknownCapIsSafe: an unset max_tokens must not produce an
// unbounded batch. Guessing low costs extra calls; guessing high truncates.
func TestRefBatchPlan_UnknownCapIsSafe(t *testing.T) {
	got, _ := refBatchPlan(500, 0)
	want, _ := refBatchPlan(500, refAssumedMaxTokens)
	if got != want {
		t.Fatalf("unknown cap planned %d items/call, want the %d assumed-cap plan", got, want)
	}
}

// TestEvidenceIndex_ResolvesItemToItsOwnSource is the point of the whole join:
// a spine item's file:line must yield the source AT that line. The old path
// embedded every item name in the batch as one query and hoped; this is exact.
func TestEvidenceIndex_ResolvesItemToItsOwnSource(t *testing.T) {
	// A chunk covering lines 10-14 of the file.
	ix := BuildEvidenceIndex([]core.Chunk{{
		File: "cli.py", StartLine: 10, EndLine: 14,
		Text: "line10\nline11\nline12\nline13\nline14",
	}})
	got := ix.Snippet("cli.py", 12, 2)
	want := "    12| line12\n    13| line13\n"
	if got != want {
		t.Fatalf("snippet line math is off:\n got %q\nwant %q", got, want)
	}
	if s := ix.Snippet("cli.py", 14, 6); s != "    14| line14\n" {
		t.Fatalf("a line at the end of a chunk must not over-read: %q", s)
	}
	if s := ix.Snippet("cli.py", 99, 2); s != "" {
		t.Fatalf("a line outside every chunk must yield nothing, got %q", s)
	}
	if s := ix.Snippet("nope.py", 10, 2); s != "" {
		t.Fatalf("an unknown file must yield nothing, got %q", s)
	}
	var nilIx *EvidenceIndex
	if s := nilIx.Snippet("cli.py", 10, 2); s != "" {
		t.Fatalf("a nil index must degrade to no evidence, got %q", s)
	}
}

// TestEvidenceIndex_PrefersTheTightestChunk: chunk windows overlap and a small
// file is also emitted whole, so several chunks can cover a line. The narrowest
// is the declaration the item belongs to, not the file it happens to sit in.
func TestEvidenceIndex_PrefersTheTightestChunk(t *testing.T) {
	ix := BuildEvidenceIndex([]core.Chunk{
		{File: "a.go", StartLine: 1, EndLine: 3, Text: "whole1\nwhole2\nwhole3"},
		{File: "a.go", StartLine: 2, EndLine: 3, Text: "decl2\ndecl3"},
	})
	if got := ix.Snippet("a.go", 2, 1); got != "    2| decl2\n" {
		t.Fatalf("want the tightest chunk's text, got %q", got)
	}
}

// --- reference rendering against a stub provider ---------------------------

type stubLLM struct {
	srv    *httptest.Server
	calls  atomic.Int64
	cutoff atomic.Bool // when set, every answer reports finish_reason=length
	body   func(call int) string
}

func newStubLLM(t *testing.T, body func(call int) string) *stubLLM {
	t.Helper()
	s := &stubLLM{body: body}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(s.calls.Add(1))
		reason := "stop"
		if s.cutoff.Load() {
			reason = "length"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": s.body(n)},
				"finish_reason": reason,
			}},
			"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 8},
		})
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stubLLM) client() *llm.Client {
	return llm.New(core.ModelCfg{BaseURL: s.srv.URL, ChatModel: "m", MaxTokens: 4096})
}

func refPageFixture(items int) (*RunContext, *core.Page) {
	spine := make([]core.SpineItem, items)
	for i := range spine {
		spine[i] = core.SpineItem{Kind: core.KindFlag, Name: fmt.Sprintf("--flag%03d", i), File: "cli.py", Line: i + 1}
	}
	p := &core.Page{Kind: core.PageReference, Title: "Entry Points, Flags & Environment",
		Slug: "flags", SpineKinds: []core.SpineKind{core.KindFlag}}
	rc := &RunContext{Run: &core.Run{ID: 1}, Art: core.Artifacts{Spine: spine, Pages: []core.Page{*p}}}
	return rc, p
}

// TestRenderReferenceBody_TruncationRetryTerminates is the safety property of
// the halve-and-retry: a provider that ALWAYS reports truncation must not loop.
// It must give up after a bounded number of attempts, keep the text it got, and
// still emit every part — a wedged retry loop would be far worse than the
// truncation it is trying to fix.
func TestRenderReferenceBody_TruncationRetryTerminates(t *testing.T) {
	stub := newStubLLM(t, func(int) string {
		return "# Flags\n\nsome real looking page body that is comfortably over the minimum length floor\n"
	})
	stub.cutoff.Store(true)
	rc, p := refPageFixture(40)
	rc.LLM = stub.client()

	body, _, err := renderReferenceBody(context.Background(), rc, p, nil)
	if err != nil {
		t.Fatalf("always-truncating provider failed the page: %v", err)
	}
	if strings.TrimSpace(body) == "" {
		t.Fatal("gave up and produced nothing; a truncated part is still content")
	}
	// 40 items at the planned batch size, each part retried refTruncRetries times.
	if n := stub.calls.Load(); n > 40 {
		t.Fatalf("%d provider calls for a 40-item page — the retry is not bounded", n)
	}
}

// TestRenderReferenceBody_SinglePartIsOneCall pins that a page whose surface
// fits keeps costing exactly one call, opens with an H1, and never mentions
// parts — the calibrated behaviour.
func TestRenderReferenceBody_SinglePartIsOneCall(t *testing.T) {
	stub := newStubLLM(t, func(int) string {
		return "# Flags\n\nA reference page body that is comfortably longer than the minimum page-body floor.\n"
	})
	rc, p := refPageFixture(20)
	rc.LLM = stub.client()

	if _, _, err := renderReferenceBody(context.Background(), rc, p, nil); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	if n := stub.calls.Load(); n != 1 {
		t.Fatalf("a 20-item reference page took %d calls, want 1", n)
	}
}

// TestRenderReferenceBody_EmptySurfaceStillDrafts: outline only plans a
// reference page when its surface is non-empty, but the single-batch path used
// to draft an empty one anyway and dropping that silently would turn the page
// into an empty body — which the draft stage then discards as a failure.
func TestRenderReferenceBody_EmptySurfaceStillDrafts(t *testing.T) {
	stub := newStubLLM(t, func(int) string {
		return "# Flags\n\nNothing was extracted for this surface, but the page itself still exists here.\n"
	})
	rc, p := refPageFixture(0)
	rc.LLM = stub.client()

	body, _, err := renderReferenceBody(context.Background(), rc, p, nil)
	if err != nil {
		t.Fatalf("empty surface failed: %v", err)
	}
	if strings.TrimSpace(body) == "" {
		t.Fatal("empty surface produced no body — the page would be dropped")
	}
	if n := stub.calls.Load(); n != 1 {
		t.Fatalf("empty surface took %d calls, want exactly 1", n)
	}
}
