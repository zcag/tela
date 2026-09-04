package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// withID is the file as the sync client re-uploads it: tela renders the page's
// identity into the frontmatter on every download, and that `id:` is what binds
// the PUT back to the page (and, after a DELETE, resurrects it) instead of
// minting a duplicate.
func withID(id int64, title, body string) string {
	return fmt.Sprintf("---\nid: %d\ntitle: %s\n---\n%s", id, title, body)
}

// TestDAV_ConflictDeletePutKeepsSubPages: rclone bisync resolves a both-sides
// edit as DELETE(loser) + PUT(winner) on the SAME path. For a page that has
// sub-pages, the DELETE cascades to the whole subtree — the sub-pages are
// separate files under `<page>/` that the client never touched — so the PUT has
// to bring the subtree back with it, or every sync conflict on a parent page
// silently trashes its children. (It did, twice, before this: nine pages on
// 2026-09-02 and one on 2026-09-04, invisible because there is no trash view.)
func TestDAV_ConflictDeletePutKeepsSubPages(t *testing.T) {
	ts, d, spaceID, folder, token := davFixture(t)
	ctx := context.Background()

	if resp, _ := davDo(t, ts, token, "PUT", "/dav/"+folder+"/parent.md", "a\nb\nc\n", nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("parent PUT = %d, want 201", resp.StatusCode)
	}
	if resp, _ := davDo(t, ts, token, "PUT", "/dav/"+folder+"/parent/kid.md", "kid body\n", nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("child PUT = %d, want 201", resp.StatusCode)
	}
	parent, _ := pageByTitle(t, d, spaceID, "parent")
	kid, _ := pageByTitle(t, d, spaceID, "kid")
	if kid.ParentID == nil || *kid.ParentID != parent.ID {
		t.Fatalf("child should hang off the parent page, got parent_id %v", kid.ParentID)
	}

	// The app edits the parent too, so the next sync is a genuine conflict.
	if _, err := d.ExecContext(ctx,
		`UPDATE pages SET body = $1, updated_at = tela_now() WHERE id = $2`, "a\nB-APP\nc\n", parent.ID); err != nil {
		t.Fatalf("simulate app edit: %v", err)
	}
	// …which bisync sends as DELETE then PUT of the parent's own file.
	if resp, _ := davDo(t, ts, token, "DELETE", "/dav/"+folder+"/parent.md", "", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("conflict DELETE = %d, want 204", resp.StatusCode)
	}
	if resp, _ := davDo(t, ts, token, "PUT", "/dav/"+folder+"/parent.md", withID(parent.ID, "parent", "a\nB-LOCAL\nc\n"), nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("conflict PUT = %d, want 201", resp.StatusCode)
	}

	var deletedAt sql.NullString
	if err := d.QueryRowContext(ctx, `SELECT deleted_at FROM pages WHERE id = $1`, kid.ID).Scan(&deletedAt); err != nil {
		t.Fatalf("re-read child: %v", err)
	}
	if deletedAt.Valid {
		t.Fatalf("child was left trashed by the parent's DELETE+PUT (deleted_at = %q)", deletedAt.String)
	}
	// And it comes back whole: still reachable on the sync surface, links rebuilt.
	resp, body := davDo(t, ts, token, "GET", "/dav/"+folder+"/parent/kid.md", "", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "kid body") {
		t.Fatalf("child not served after resurrect: %d %q", resp.StatusCode, body)
	}
}

// TestDAV_ResurrectLeavesEarlierDeletesTrashed: the cascade restore is scoped to
// the one delete that took the page — a sub-page trashed earlier, on its own,
// stays trashed. Otherwise a later conflict on the parent would quietly undo a
// deliberate delete.
func TestDAV_ResurrectLeavesEarlierDeletesTrashed(t *testing.T) {
	ts, d, spaceID, folder, token := davFixture(t)
	ctx := context.Background()

	davDo(t, ts, token, "PUT", "/dav/"+folder+"/parent.md", "a\n", nil)
	davDo(t, ts, token, "PUT", "/dav/"+folder+"/parent/gone.md", "gone\n", nil)
	parent, _ := pageByTitle(t, d, spaceID, "parent")
	gone, _ := pageByTitle(t, d, spaceID, "gone")

	// Deliberately delete the sub-page first, on its own.
	if resp, _ := davDo(t, ts, token, "DELETE", "/dav/"+folder+"/parent/gone.md", "", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("child DELETE = %d, want 204", resp.StatusCode)
	}
	if _, err := d.ExecContext(ctx,
		`UPDATE pages SET body = $1, updated_at = tela_now() WHERE id = $2`, "A-APP\n", parent.ID); err != nil {
		t.Fatalf("simulate app edit: %v", err)
	}
	davDo(t, ts, token, "DELETE", "/dav/"+folder+"/parent.md", "", nil)
	davDo(t, ts, token, "PUT", "/dav/"+folder+"/parent.md", withID(parent.ID, "parent", "A-LOCAL\n"), nil)

	var deletedAt sql.NullString
	d.QueryRowContext(ctx, `SELECT deleted_at FROM pages WHERE id = $1`, gone.ID).Scan(&deletedAt)
	if !deletedAt.Valid {
		t.Fatal("a sub-page deleted on its own must stay deleted through the parent's resurrect")
	}
}
