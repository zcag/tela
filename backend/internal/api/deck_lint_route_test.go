package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/zcag/tela/backend/internal/auth"
)

// POST /api/pages/{id}/deck/lint is the editor's advisory validator: page-scoped
// (so it isn't an open proxy to the sidecar), and purely informational — it
// reports the same tahta-lint issues the agent write gate blocks on, but a human
// autosaving through a broken state must still be able to save.
func TestDeckLintRoute(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	ctx := context.Background()

	owner := seedUser(t, d, "owner", "ownerpw123", false)
	outsider := seedUser(t, d, "outsider", "outsiderpw123", false)
	sp := seedSpace(t, d, "Deck Space", "deck-space", owner)
	p, ae := srv.createPageCore(ctx, authUser(owner, "owner", false), nil, pageCreateRequest{
		SpaceID: sp, Title: "D", Body: "---\nlayout: cover\ntitle: X\n---\n", Props: map[string]any{"deck": true},
	}, true)
	if ae != nil {
		t.Fatalf("create deck: %v", ae)
	}
	path := "/api/pages/" + strconv.FormatInt(p.ID, 10) + "/deck/lint"
	const route = "POST /api/pages/{id}/deck/lint"

	hit := func(body string, u *auth.User) *http.Response {
		return routedRecorder(route, srv.PostPageDeckLint, userRequest(http.MethodPost, path, body, u)).Result()
	}

	t.Run("non-member forbidden", func(t *testing.T) {
		fakeDeckLint(t)
		if c := hit("x", authUser(outsider, "outsider", false)).StatusCode; c != http.StatusForbidden {
			t.Fatalf("outsider: want 403 got %d", c)
		}
	})

	t.Run("reports issues without blocking", func(t *testing.T) {
		fakeDeckLint(t)
		resp := hit("---\nlayout: cover\ntitle: BROKENDECK\n---\n", authUser(owner, "owner", false))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("want 200 (advisory, never an error status) got %d", resp.StatusCode)
		}
		var out lintDeckOut
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out.Errors != 1 || len(out.Issues) != 1 || out.Issues[0].Slide != 2 {
			t.Fatalf("want the sidecar's one issue on slide 2, got %+v", out)
		}
		// The same body must still save through the interactive (non-agent) path —
		// agentWrite=false is the FE autosave. Advising is not gating.
		bad := "---\nlayout: cover\ntitle: BROKENDECK\n---\n"
		if _, ae := srv.updatePageCore(ctx, authUser(owner, "owner", false), nil, p.ID, pageUpdateRequest{Body: &bad}, false); ae != nil {
			t.Fatalf("advisory lint must not block an interactive save: %+v", ae)
		}
	})

	t.Run("clean draft", func(t *testing.T) {
		fakeDeckLint(t)
		resp := hit("---\nlayout: cover\ntitle: fine\n---\n", authUser(owner, "owner", false))
		var out lintDeckOut
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if !out.OK || out.Errors != 0 {
			t.Fatalf("want clean, got %+v", out)
		}
	})

	t.Run("sidecar down", func(t *testing.T) {
		t.Setenv("TELA_DECK_URL", "http://127.0.0.1:1") // connection refused
		if c := hit("x", authUser(owner, "owner", false)).StatusCode; c != http.StatusBadGateway {
			t.Fatalf("want 502 got %d", c)
		}
	})
}
