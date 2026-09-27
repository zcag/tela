package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zcag/tela/backend/internal/auth"
)

// TestPageDraftStatus covers GitHub issue #18's draft half: status defaults to
// published (nothing changes for existing writers), and a draft is kept off
// every public surface (public space tree/page/feed/sitemap/exists, the /p
// card, share links, a file page's parent) until it's published, while its
// published children stay reachable.
func TestPageDraftStatus(t *testing.T) {
	ts, d := newWiredServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	owner := seedUser(t, d, "owner", "ownerpw12", false)
	space := seedPublicSpace(t, d, "Blog", "blog", owner)
	client := loginClient(t, ts, "owner", "ownerpw12")

	do := func(method, path, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	anon := func(path, ua string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		if ua != "" {
			req.Header.Set("User-Agent", ua)
		}
		resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	create := func(title, extra string) int64 {
		t.Helper()
		st, b := do(http.MethodPost, "/api/pages", fmt.Sprintf(`{"space_id":%d,"title":%q,"body":"text"%s}`, space, title, extra))
		var out struct {
			Page struct {
				ID     int64  `json:"id"`
				Status string `json:"status"`
			} `json:"page"`
		}
		_ = json.Unmarshal([]byte(b), &out)
		if st/100 != 2 || out.Page.ID == 0 {
			t.Fatalf("create %q: %d %s", title, st, b)
		}
		return out.Page.ID
	}

	pub := create("Hello world", "")
	draft := create("Secret plans", `,"status":"draft"`)
	var status string
	mustQueryRow(t, d, `SELECT status FROM pages WHERE id = $1`, &status, pub)
	if status != "published" {
		t.Fatalf("default status = %q, want published", status)
	}
	// A published child under the draft stays public, re-parented past it.
	var child int64
	mustQueryRow(t, d, `INSERT INTO pages (space_id, parent_id, title, body, position) VALUES ($1, $2, 'Child', 'c', 0) RETURNING id`, &child, space, draft)
	if st, b := do(http.MethodPost, "/api/pages", fmt.Sprintf(`{"space_id":%d,"title":"x","body":"y","status":"bogus"}`, space)); st != http.StatusBadRequest || !strings.Contains(b, "invalid_status") {
		t.Errorf("bogus status on create: %d %s, want 400 invalid_status", st, b)
	}

	hidden := func(when string) {
		t.Helper()
		_, tree := anon(fmt.Sprintf("/api/public/spaces/%d/tree", space), "")
		if strings.Contains(tree, "Secret plans") || !strings.Contains(tree, "Hello world") {
			t.Errorf("%s: public tree shows draft or lost published page: %s", when, tree)
		}
		var tr struct {
			Pages []publicTreeNode `json:"pages"`
		}
		_ = json.Unmarshal([]byte(tree), &tr)
		for _, n := range tr.Pages {
			if n.ID == child && n.ParentID != nil {
				t.Errorf("%s: child of a draft still points at it (parent %d)", when, *n.ParentID)
			}
		}
		if st, _ := anon(fmt.Sprintf("/api/public/spaces/%d/pages/%d", space, draft), ""); st != http.StatusNotFound {
			t.Errorf("%s: public page read of a draft = %d, want 404", when, st)
		}
		if st, _ := anon(fmt.Sprintf("/api/public/spaces/%d/pages/%d", space, child), ""); st != http.StatusOK {
			t.Errorf("%s: published child of a draft = %d, want 200", when, st)
		}
		for _, path := range []string{fmt.Sprintf("/api/public/spaces/%d/feed.xml", space), "/api/public/sitemap.xml"} {
			if _, b := anon(path, ""); strings.Contains(b, "Secret plans") || strings.Contains(b, fmt.Sprintf("/%d/", draft)) {
				t.Errorf("%s: %s lists the draft", when, path)
			}
		}
		if _, b := anon(fmt.Sprintf("/p/%d", draft), "Slackbot-LinkExpanding 1.0"); strings.Contains(b, "Secret plans") {
			t.Errorf("%s: bare /p card of a draft in a public space shows its title", when)
		}
		if st, loc := anon(fmt.Sprintf("/p/%d", draft), "Mozilla/5.0"); st != http.StatusFound || strings.Contains(loc, "/public/") {
			t.Errorf("%s: browser /p of a draft should go to the app, got %d %s", when, st, loc)
		}
	}
	hidden("draft")

	// Share links: a draft root resolves as not found; a draft under a shared
	// root drops out of scope with its subtree.
	seedShareRow(t, d, draft, owner, false, false, "", "")
	var draftToken string
	mustQueryRow(t, d, `SELECT token FROM share_links WHERE page_id = $1`, &draftToken, draft)
	if st, _ := anon("/api/share/"+draftToken, ""); st != http.StatusNotFound {
		t.Errorf("share of a draft = %d, want 404", st)
	}
	seedShareRow(t, d, pub, owner, true, false, "", "")
	var pubToken string
	mustQueryRow(t, d, `SELECT token FROM share_links WHERE page_id = $1`, &pubToken, pub)
	mustExec(t, d, `UPDATE pages SET parent_id = $1 WHERE id = $2`, pub, draft)
	if _, b := anon("/api/share/"+pubToken+"/tree", ""); strings.Contains(b, "Secret plans") || strings.Contains(b, `"Child"`) {
		t.Errorf("shared tree includes a draft or its subtree: %s", b)
	}
	if st, _ := anon(fmt.Sprintf("/api/share/%s/page/%d", pubToken, draft), ""); st != http.StatusNotFound {
		t.Errorf("draft page via a parent's share = %d, want 404", st)
	}

	// MCP: status shows, and filters.
	sess := mcpSession(t, ctx, ts, seedReadKey(t, d, owner, auth.ScopeRead))
	var lp listPagesOut
	mcpCallJSON(t, ctx, sess, "list_pages", map[string]any{"space_id": space, "parent_id": pub, "status": "draft"}, &lp)
	if len(lp.Pages) != 1 || lp.Pages[0].ID != draft || lp.Pages[0].Status != "draft" {
		t.Errorf("list_pages status=draft = %+v, want just the draft", lp.Pages)
	}

	// Publishing makes it public everywhere.
	if st, b := do(http.MethodPatch, fmt.Sprintf("/api/pages/%d", draft), `{"status":"published"}`); st != http.StatusOK {
		t.Fatalf("publish: %d %s", st, b)
	}
	if st, _ := anon(fmt.Sprintf("/api/public/spaces/%d/pages/%d", space, draft), ""); st != http.StatusOK {
		t.Errorf("published page = %d, want 200", st)
	}
	if st, _ := anon("/api/share/"+draftToken, ""); st != http.StatusOK {
		t.Errorf("share of a now-published page = %d, want 200", st)
	}
	if _, b := anon("/api/share/"+pubToken+"/tree", ""); !strings.Contains(b, "Secret plans") {
		t.Errorf("published page missing from its parent's share tree: %s", b)
	}
	if st, b := do(http.MethodPatch, fmt.Sprintf("/api/pages/%d", draft), `{"status":"archived"}`); st != http.StatusBadRequest || !strings.Contains(b, "invalid_status") {
		t.Errorf("bogus status on update: %d %s, want 400 invalid_status", st, b)
	}
}
