package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zcag/tela/backend/internal/auth"
	"github.com/zcag/tela/backend/internal/models"
)

// TestPageVersion covers GitHub issue #18's lost-edit half: the version
// advances on every content change (whatever the write path), a write with a
// stale base_version is refused with 409 version_conflict instead of
// overwriting, and patch_page never undoes an edit made while it ran.
func TestPageVersion(t *testing.T) {
	ts, d := newWiredServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	seedUser(t, d, "alice", "alicepw12", false)
	var alice int64
	mustQueryRow(t, d, `SELECT id FROM users WHERE username = 'alice'`, &alice)
	space := seedSpace(t, d, "Docs", "docs", alice)
	client := loginClient(t, ts, "alice", "alicepw12")

	patch := func(id int64, body string) (int, models.Page, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPatch, fmt.Sprintf("%s/api/pages/%d", ts.URL, id), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var out struct {
			Page models.Page `json:"page"`
		}
		_ = json.Unmarshal(raw, &out)
		return resp.StatusCode, out.Page, string(raw)
	}

	id := seedPageInSpace(t, d, space, nil, "Runbook", "## Setup\n\nstep one\n\n## Deploy\n\nship it\n")
	st, p, raw := patch(id, `{"body":"## Setup\n\nstep one, two\n\n## Deploy\n\nship it\n"}`)
	if st != http.StatusOK || p.Version != 2 || p.Status != "published" {
		t.Fatalf("plain write: %d version=%d status=%q %s", st, p.Version, p.Status, raw)
	}
	// A write from any path, here raw SQL like sync/Atlas/collab persistence, bumps it too.
	mustExec(t, d, `UPDATE pages SET body = body || 'x' WHERE id = $1`, id)
	st, p, _ = patch(id, `{"title":"Runbook","base_version":3}`)
	if st != http.StatusOK || p.Version != 3 {
		t.Fatalf("title-only same value at current base: %d version=%d (unchanged content must not bump)", st, p.Version)
	}
	// Stale base: refused, page untouched.
	st, _, raw = patch(id, `{"body":"clobber","base_version":2}`)
	if st != http.StatusConflict || !strings.Contains(raw, "version_conflict") || !strings.Contains(raw, "now version 3") {
		t.Fatalf("stale base_version: %d %s, want 409 version_conflict naming the current version", st, raw)
	}
	var body string
	mustQueryRow(t, d, `SELECT body FROM pages WHERE id = $1`, &body, id)
	if body == "clobber" {
		t.Fatal("stale write went through")
	}

	// Two writers racing from the same base: exactly one wins.
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i], _, _ = patch(id, fmt.Sprintf(`{"body":"writer %d","base_version":3}`, i))
		}(i)
	}
	wg.Wait()
	if !(codes[0] == 200 && codes[1] == 409 || codes[0] == 409 && codes[1] == 200) {
		t.Fatalf("racing writers from one base got %v, want one 200 and one 409", codes)
	}

	// MCP: update_page honours base_version; patch_page refuses a stale one.
	sess := mcpSession(t, ctx, ts, seedReadKey(t, d, alice, auth.ScopeWrite))
	var cur int64
	mustQueryRow(t, d, `SELECT version FROM pages WHERE id = $1`, &cur, id)
	for _, call := range []struct {
		tool string
		args map[string]any
	}{
		{"update_page", map[string]any{"id": id, "body": "x", "base_version": cur - 1}},
		{"patch_page", map[string]any{"id": id, "target": "Setup", "operation": "append", "content": "y", "base_version": cur - 1}},
	} {
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: call.tool, Arguments: call.args})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError || !strings.Contains(mcpErrText(res), "version_conflict") {
			t.Errorf("%s with stale base_version: want version_conflict, got %v", call.tool, res.Content)
		}
	}
	var gp getPageOut
	mcpCallJSON(t, ctx, sess, "get_page", map[string]any{"id": id}, &gp)
	if gp.Page.Version != cur {
		t.Errorf("get_page version = %d, want %d", gp.Page.Version, cur)
	}
	var up writePageOut
	mcpCallJSON(t, ctx, sess, "update_page", map[string]any{"id": id, "body": "fresh", "base_version": cur}, &up)
	if up.Page.Version != cur+1 {
		t.Errorf("update_page at current base: version %d, want %d", up.Page.Version, cur+1)
	}
}
