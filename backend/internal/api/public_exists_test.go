package api

import (
	"io"
	"net/http"
	"testing"
)

// exists probes the edge's existence endpoint and asserts the response carries
// no body — the status IS the answer.
func exists(t *testing.T, base, path string) int {
	t.Helper()
	resp, err := http.Get(base + "/api/public/exists" + path)
	if err != nil {
		t.Fatalf("GET exists%s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if len(b) != 0 {
		t.Fatalf("exists%s returned a body (%q) — it must be status-only", path, b)
	}
	return resp.StatusCode
}

// TestPublicExists_Shapes: the three handle-URL shapes each answer 204 when the
// thing is publicly there and 404 when it definitely isn't. nginx turns that
// 404 into the real 404 that replaces the soft-404 app shell, so a false
// negative here is a live page going dark.
func TestPublicExists_Shapes(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	pub := seedPublicSpace(t, d, "Alice Blog", "alice-blog", alice)
	priv := seedSpace(t, d, "Alice Secret", "alice-secret", alice)
	mustExec(t, d, `INSERT INTO pages (space_id, parent_id, title, body, position) VALUES ($1, NULL, 'Post', 'b', 0)`, pub)
	mustExec(t, d, `INSERT INTO pages (space_id, parent_id, title, body, position) VALUES ($1, NULL, 'Hidden', 'b', 0)`, priv)

	var pageID, privPageID int64
	mustQueryRow(t, d, `SELECT id FROM pages WHERE space_id = $1`, &pageID, pub)
	mustQueryRow(t, d, `SELECT id FROM pages WHERE space_id = $1`, &privPageID, priv)

	cases := []struct {
		path string
		want int
	}{
		// Handle home: exists iff the account has ≥1 public space.
		{"/alice", 204},
		{"/nobody-xyz", 404},
		// A machine path that happens to be handle-shaped — this is the class
		// that used to answer 200 with the app shell (/openapi.json, /ai.txt).
		{"/ai.txt", 404},
		// Space by handle+slug.
		{"/alice/alice-blog", 204},
		{"/alice/no-such-space", 404},
		{"/alice/alice-secret", 404}, // private → never confirmed
		{"/nobody-xyz/alice-blog", 404},
		// Page by handle+slug+id.
		{"/alice/alice-blog/" + itoa(pageID), 204},
		{"/alice/alice-blog/999999", 404},
		{"/alice/alice-blog/" + itoa(privPageID), 404}, // page of another space
		{"/alice/alice-blog/notanumber", 404},
	}
	for _, c := range cases {
		if got := exists(t, ts.URL, c.path); got != c.want {
			t.Errorf("exists%s = %d, want %d", c.path, got, c.want)
		}
	}
}

// TestPublicExists_PrivateHandleHome: a user with no public space is reported
// missing, exactly like an unknown handle — the probe must not become a way to
// enumerate accounts.
func TestPublicExists_PrivateHandleHome(t *testing.T) {
	ts, d := newWiredServer(t)
	bob := seedUser(t, d, "bob", "bobpw1234", false)
	seedSpace(t, d, "Bob Private", "bob-private", bob)

	if got := exists(t, ts.URL, "/bob"); got != http.StatusNotFound {
		t.Fatalf("private-only handle = %d, want 404", got)
	}
	// Flipping the space public makes the home exist — the probe follows the
	// same predicate the home page itself uses.
	mustExec(t, d, `UPDATE spaces SET visibility = 'public' WHERE slug = 'bob-private'`)
	if got := exists(t, ts.URL, "/bob"); got != http.StatusNoContent {
		t.Fatalf("published handle = %d, want 204", got)
	}
}

// TestPublicExists_OrgHandle: an org handle resolves through the same path, and
// a public ORG space is NOT reachable under its creator's personal handle (the
// attribution bug) — so the edge agrees with the reader on both.
func TestPublicExists_OrgHandle(t *testing.T) {
	ts, d := newWiredServer(t)
	erin := seedUser(t, d, "erin", "erinpw123", false)
	org := seedOrg(t, d, "Widget Co", "widgetco")
	orgSpace := seedOrgSpace(t, d, "Widget Docs", "widget-docs", org)
	seedMember(t, d, orgSpace, erin, "owner")
	mustExec(t, d, `UPDATE spaces SET visibility = 'public' WHERE id = $1`, orgSpace)

	if got := exists(t, ts.URL, "/widgetco/widget-docs"); got != http.StatusNoContent {
		t.Fatalf("org space = %d, want 204", got)
	}
	if got := exists(t, ts.URL, "/erin/widget-docs"); got != http.StatusNotFound {
		t.Fatalf("org space under creator handle = %d, want 404", got)
	}
	if got := exists(t, ts.URL, "/erin"); got != http.StatusNotFound {
		t.Fatalf("creator home = %d, want 404 (no space of her own)", got)
	}
}

// TestPublicExists_DeletedPage: a trashed page stops existing at its URL (the
// reader 404s it), so the edge must 404 it too.
func TestPublicExists_DeletedPage(t *testing.T) {
	ts, d := newWiredServer(t)
	carl := seedUser(t, d, "carl", "carlpw123", false)
	sp := seedPublicSpace(t, d, "Carl Blog", "carl-blog", carl)
	mustExec(t, d, `INSERT INTO pages (space_id, parent_id, title, body, position) VALUES ($1, NULL, 'Gone', 'b', 0)`, sp)
	var pageID int64
	mustQueryRow(t, d, `SELECT id FROM pages WHERE space_id = $1`, &pageID, sp)

	if got := exists(t, ts.URL, "/carl/carl-blog/"+itoa(pageID)); got != http.StatusNoContent {
		t.Fatalf("live page = %d, want 204", got)
	}
	mustExec(t, d, `UPDATE pages SET deleted_at = tela_now() WHERE id = $1`, pageID)
	if got := exists(t, ts.URL, "/carl/carl-blog/"+itoa(pageID)); got != http.StatusNotFound {
		t.Fatalf("deleted page = %d, want 404", got)
	}
}
