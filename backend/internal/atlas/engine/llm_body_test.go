package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/zcag/tela/backend/internal/atlas/core"
	"github.com/zcag/tela/backend/internal/atlas/llm"
)

func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// rcServing builds a RunContext whose LLM talks to a stub that answers 200 OK
// with `content` — the exact shape a broken-but-responsive provider produces.
func rcServing(t *testing.T, content string) *RunContext {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": content},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 8},
		})
	}))
	t.Cleanup(srv.Close)
	return &RunContext{LLM: llm.New(core.ModelCfg{BaseURL: srv.URL, ChatModel: "m", MaxTokens: 4096})}
}

// TestChatBody_RejectsProviderErrorAsContent is the regression guard for the
// incident: tardis's L2 relief answered 200 OK with its auth error as the
// assistant message, and those 33 characters were published to a live wiki as
// seven pages. A 200 is not consent to publish.
func TestChatBody_RejectsProviderErrorAsContent(t *testing.T) {
	rc := rcServing(t, "Not logged in · Please run /login")
	body, _, err := chatBody(context.Background(), rc, "sys", "user", 0.2)
	if err == nil {
		t.Fatalf("provider error was accepted as a page body: %q", body)
	}
	if body != "" {
		t.Fatalf("rejected call still returned a body: %q", body)
	}
	// The error must carry the offending text, or whoever reads the failed run
	// has to go and re-probe the provider to find out what happened.
	if !strings.Contains(err.Error(), "Not logged in") {
		t.Fatalf("error hides the provider's answer: %v", err)
	}
}

// TestChatBody_RejectsWordierProviderErrors pins that the guard is structural,
// not a match on that one sentence — the next broken provider words its error
// differently, and a length-only floor would wave a chattier one straight
// through.
func TestChatBody_RejectsWordierProviderErrors(t *testing.T) {
	long := "Your subscription has lapsed and this request could not be completed. " +
		"Please visit the billing portal to reactivate your plan before retrying this operation."
	if len(long) <= minBodyChars {
		t.Fatalf("fixture must be longer than the %d-char floor to test the structural rule", minBodyChars)
	}
	if _, _, err := chatBody(context.Background(), rcServing(t, long), "sys", "user", 0.2); err == nil {
		t.Fatal("a long single-line provider error was accepted as a page body")
	}
}

// TestChatBody_AcceptsRealPage proves the guard doesn't reject ordinary output.
func TestChatBody_AcceptsRealPage(t *testing.T) {
	page := "# API & Routes\n\nSUMMARY: how requests are routed.\n\n" +
		"## Handlers\n\n- `GET /v1/models` — lists the pinned model.\n- `POST /v1/chat/completions` — chat.\n"
	body, truncated, err := chatBody(context.Background(), rcServing(t, page), "sys", "user", 0.2)
	if truncated {
		t.Fatal("a finish_reason=stop answer was reported as truncated")
	}
	if err != nil {
		t.Fatalf("real page rejected: %v", err)
	}
	if body != page {
		t.Fatalf("body was altered by the guard:\n got %q\nwant %q", body, page)
	}
}

// TestChatBody_GuardsEveryPageBodyCallSite is the anti-regression check that
// matters most in practice: the incident published a 305 KB page with error
// strings spliced between real sections, because a multi-part reference page
// concatenates parts and only SOME parts failed. Any page-body call that still
// goes through rc.LLM.Chat directly re-opens exactly that hole, and the
// assembled-length check that everyone reaches for first would not see it.
func TestChatBody_GuardsEveryPageBodyCallSite(t *testing.T) {
	for _, f := range []string{"stage_draft.go", "stage_refine.go", "stage_repair.go"} {
		src := readSource(t, f)
		if strings.Contains(src, "rc.LLM.Chat(") {
			t.Errorf("%s still calls rc.LLM.Chat directly for a page body — route it through chatBody", f)
		}
	}
	// stage_outline.go is deliberately exempt: its answer is JSON that gets
	// parsed (and can legitimately be a single line), so the multi-line page
	// rule must not apply to it.
	if !strings.Contains(readSource(t, "stage_outline.go"), "rc.LLM.Chat(") {
		t.Error("stage_outline.go no longer calls rc.LLM.Chat — if it was routed through " +
			"chatBody, single-line JSON outlines will now fail the run")
	}
}
