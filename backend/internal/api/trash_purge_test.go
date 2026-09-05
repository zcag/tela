package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func purge(t *testing.T, c *http.Client, base string, id int64) *http.Response {
	t.Helper()
	resp, err := c.Post(base+"/api/pages/"+itoa(id)+"/purge", "application/json", nil)
	if err != nil {
		t.Fatalf("purge %d: %v", id, err)
	}
	return resp
}

func pageExists(t *testing.T, d *sql.DB, id int64) bool {
	t.Helper()
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM pages WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatalf("count page %d: %v", id, err)
	}
	return n > 0
}

// TestTrash_PurgeDestroysSubtree: purge is the one irreversible delete. It takes
// the page's sub-pages with it — a child cannot outlive its parent — and leaves
// no row behind, which is what makes it the answer to "this page should never
// have existed".
func TestTrash_PurgeDestroysSubtree(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	space := seedSpace(t, d, "Alice Space", "alice-space", alice)
	parent := seedPageInSpace(t, d, space, nil, "parent", "p\n")
	kidA := seedPageInSpace(t, d, space, &parent, "kid-a", "a\n")
	kidB := seedPageInSpace(t, d, space, &parent, "kid-b", "b\n")
	c := loginClient(t, ts, "alice", "alicepw12")

	deletePage(t, c, ts.URL, parent)
	resp := purge(t, c, ts.URL, parent)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("purge status=%d body=%s, want 204", resp.StatusCode, b)
	}
	for _, id := range []int64{parent, kidA, kidB} {
		if pageExists(t, d, id) {
			t.Fatalf("page %d survived the purge", id)
		}
	}
	if got := getTrash(t, c, ts.URL, space); len(got) != 0 {
		t.Fatalf("bin should be empty after the purge, got %+v", got)
	}
	// Revisions hang off pages with ON DELETE CASCADE, so nothing is orphaned.
	var revs int
	d.QueryRow(`SELECT count(*) FROM page_revisions WHERE page_id = $1`, parent).Scan(&revs)
	if revs != 0 {
		t.Fatalf("page_revisions survived the purge: %d", revs)
	}
	// The event is the only remaining trace, and it carries the title.
	var label string
	if err := d.QueryRow(
		`SELECT target_label FROM events WHERE type = $1 AND target_id = $2`, evtPagePurge, parent).Scan(&label); err != nil {
		t.Fatalf("no page.purge event recorded: %v", err)
	}
	if label != "parent" {
		t.Fatalf("purge event label = %q, want the page title", label)
	}
}

// TestTrash_PurgeScopedLikeRestore: purge gates on exactly what the bin shows —
// your own deletes, or anything if you own the space.
func TestTrash_PurgeScopedLikeRestore(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	bob := seedUser(t, d, "bob", "bobpw1234", false)
	space := seedSpace(t, d, "Shared", "shared", alice)
	seedMember(t, d, space, bob, "editor")
	byAlice := seedPageInSpace(t, d, space, nil, "alice page", "a\n")
	byBob := seedPageInSpace(t, d, space, nil, "bob page", "b\n")

	alicec := loginClient(t, ts, "alice", "alicepw12")
	bobc := loginClient(t, ts, "bob", "bobpw1234")
	deletePage(t, alicec, ts.URL, byAlice)
	deletePage(t, bobc, ts.URL, byBob)

	resp := purge(t, bobc, ts.URL, byAlice)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("editor purging someone else's delete = %d, want 404", resp.StatusCode)
	}
	if !pageExists(t, d, byAlice) {
		t.Fatal("refused purge must leave the page intact")
	}
	resp = purge(t, bobc, ts.URL, byBob)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("purging your own delete = %d, want 204", resp.StatusCode)
	}
	resp = purge(t, alicec, ts.URL, byAlice)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("owner purging their own delete = %d, want 204", resp.StatusCode)
	}
}

// TestTrash_EmptyTrashScoped: emptying the bin clears what you can see and
// nothing else — a member's "empty" must not take the rest of the team's.
func TestTrash_EmptyTrashScoped(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	bob := seedUser(t, d, "bob", "bobpw1234", false)
	space := seedSpace(t, d, "Shared", "shared", alice)
	seedMember(t, d, space, bob, "editor")
	byAlice := seedPageInSpace(t, d, space, nil, "alice page", "a\n")
	byBob := seedPageInSpace(t, d, space, nil, "bob page", "b\n")

	alicec := loginClient(t, ts, "alice", "alicepw12")
	bobc := loginClient(t, ts, "bob", "bobpw1234")
	deletePage(t, alicec, ts.URL, byAlice)
	deletePage(t, bobc, ts.URL, byBob)

	empty := func(c *http.Client) int64 {
		t.Helper()
		req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/spaces/"+itoa(space)+"/trash", nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("empty trash: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("empty trash status=%d body=%s", resp.StatusCode, b)
		}
		var out struct {
			Purged int64 `json:"purged"`
		}
		json.NewDecoder(resp.Body).Decode(&out)
		return out.Purged
	}

	if n := empty(bobc); n != 1 {
		t.Fatalf("bob emptied %d rows, want just his own", n)
	}
	if pageExists(t, d, byBob) {
		t.Fatal("bob's page should be gone")
	}
	if !pageExists(t, d, byAlice) {
		t.Fatal("a member emptying the bin must not take someone else's deletes")
	}
	if n := empty(alicec); n != 1 {
		t.Fatalf("the owner emptied %d rows, want the remaining one", n)
	}
	if pageExists(t, d, byAlice) {
		t.Fatal("the owner's empty should clear the whole bin")
	}
}

// TestTrash_RetentionSweep: off unless a deploy asks for it, and when on it only
// takes pages that have sat in the bin past the window.
func TestTrash_RetentionSweep(t *testing.T) {
	_, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	space := seedSpace(t, d, "Alice Space", "alice-space", alice)
	old := seedPageInSpace(t, d, space, nil, "old", "o\n")
	recent := seedPageInSpace(t, d, space, nil, "recent", "r\n")
	ctx := context.Background()

	for _, p := range []struct {
		id   int64
		when string
	}{{old, "2020-01-01 00:00:00"}, {recent, "2999-01-01 00:00:00"}} {
		if _, err := d.ExecContext(ctx,
			`UPDATE pages SET deleted_at = $1, deleted_by = $2 WHERE id = $3`, p.when, alice, p.id); err != nil {
			t.Fatalf("age page %d: %v", p.id, err)
		}
	}

	if err := purgeTrashOlderThan(ctx, d, 0); err != nil {
		t.Fatalf("disabled sweep: %v", err)
	}
	if !pageExists(t, d, old) {
		t.Fatal("a sweep with retention 0 must do nothing — that is the default")
	}

	if err := purgeTrashOlderThan(ctx, d, 30); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if pageExists(t, d, old) {
		t.Fatal("a page trashed years ago should be swept at 30 days")
	}
	if !pageExists(t, d, recent) {
		t.Fatal("a page inside the window must survive the sweep")
	}
}
