package rag

import (
	"context"
	"strings"
	"testing"

	"github.com/zcag/tela/backend/internal/testdb"
)

func TestKnowledgeGaps_SurfacesUnanswered(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	u := newUser(t, d, "alice")
	sp := newSpace(t, d, "alpha", u)
	svc := NewServiceWithEmbedder(d, &fakeEmbedder{})

	// "how do I configure SSO" asked 3×, never answered → a gap.
	for i := 0; i < 3; i++ {
		if err := svc.LogAsk(ctx, u, &sp, "How do I configure SSO?", 0, 0); err != nil {
			t.Fatalf("logask: %v", err)
		}
	}
	// "deploy" asked once, answered → not a gap.
	if err := svc.LogAsk(ctx, u, &sp, "How to deploy?", 4, 0.8); err != nil {
		t.Fatalf("logask: %v", err)
	}

	gaps, err := svc.KnowledgeGaps(ctx, GapScope{Instance: true}, 0, 50)
	if err != nil {
		t.Fatalf("gaps: %v", err)
	}
	if len(gaps) != 1 {
		t.Fatalf("gaps = %d, want 1 (only the unanswered SSO question)", len(gaps))
	}
	if gaps[0].Asks != 3 || gaps[0].Answered != 0 {
		t.Errorf("gap = %+v, want asks=3 answered=0", gaps[0])
	}
	if !strings.Contains(strings.ToLower(gaps[0].Question), "sso") {
		t.Errorf("gap question = %q, want the SSO one", gaps[0].Question)
	}
}

// The de-gated view must not leak one user's questions to another: a caller sees
// their own asks plus asks inside spaces they're a member of, and nothing else.
func TestKnowledgeGaps_ScopedToCaller(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	alice, bob := newUser(t, d, "alice"), newUser(t, d, "bob")
	shared := newSpace(t, d, "shared", alice)
	private := newSpace(t, d, "bobs-own", bob)
	addAccess(t, d, shared, bob) // bob joins alice's space
	svc := NewServiceWithEmbedder(d, &fakeEmbedder{})

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("logask: %v", err)
		}
	}
	// Bob asks twice in the shared space (alice is a member → she sees it) and
	// twice in his own space (she is not → she must not), plus one unscoped ask.
	for i := 0; i < 2; i++ {
		must(svc.LogAsk(ctx, bob, &shared, "What is the shared thing?", 0, 0))
		must(svc.LogAsk(ctx, bob, &private, "What is bobs secret?", 0, 0))
		must(svc.LogAsk(ctx, bob, nil, "What is everywhere?", 0, 0))
	}

	seen := func(scope GapScope) map[string]bool {
		t.Helper()
		gaps, err := svc.KnowledgeGaps(ctx, scope, 0, 50)
		if err != nil {
			t.Fatalf("gaps: %v", err)
		}
		out := map[string]bool{}
		for _, g := range gaps {
			out[g.Question] = true
		}
		return out
	}

	got := seen(GapScope{UserID: alice})
	if !got["What is the shared thing?"] {
		t.Error("alice should see asks in a space she is a member of")
	}
	if got["What is bobs secret?"] {
		t.Error("LEAK: alice saw an ask in a space she has no access to")
	}
	if got["What is everywhere?"] {
		t.Error("LEAK: alice saw another user's unscoped (NULL space) ask")
	}

	// Bob sees all three of his own; the instance view sees everything.
	if got := seen(GapScope{UserID: bob}); len(got) != 3 {
		t.Errorf("bob sees %d of his own gaps, want 3: %v", len(got), got)
	}
	if got := seen(GapScope{Instance: true}); len(got) != 3 {
		t.Errorf("instance view sees %d gaps, want 3: %v", len(got), got)
	}
	// space_id narrows without granting: alice pinning bob's private space gets
	// nothing rather than his questions.
	if got := seen(GapScope{UserID: alice, SpaceID: &private}); len(got) != 0 {
		t.Errorf("alice pinned a space she cannot see and got %v", got)
	}
	if got := seen(GapScope{UserID: alice, SpaceID: &shared}); !got["What is the shared thing?"] || len(got) != 1 {
		t.Errorf("space-pinned view = %v, want only the shared-space gap", got)
	}
}
