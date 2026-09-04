package api

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func getTrash(t *testing.T, c *http.Client, base string, spaceID int64) []trashEntry {
	t.Helper()
	resp, err := c.Get(base + "/api/spaces/" + itoa(spaceID) + "/trash")
	if err != nil {
		t.Fatalf("get trash: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("trash status=%d body=%s", resp.StatusCode, b)
	}
	var out struct {
		Pages []trashEntry `json:"pages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode trash: %v", err)
	}
	return out.Pages
}

func deleted(t *testing.T, d *sql.DB, id int64) bool {
	t.Helper()
	var at sql.NullString
	if err := d.QueryRow(`SELECT deleted_at FROM pages WHERE id = $1`, id).Scan(&at); err != nil {
		t.Fatalf("read deleted_at for %d: %v", id, err)
	}
	return at.Valid
}

// TestTrash_ListsRootsAndRestoresSubtree: deleting a parent takes its children,
// so the bin offers the PARENT — one row, "2 sub-pages" — and restoring it puts
// the whole set back. Listing the children as their own rows would offer
// restores that are already implied by the parent's.
func TestTrash_ListsRootsAndRestoresSubtree(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	space := seedSpace(t, d, "Alice Space", "alice-space", alice)
	parent := seedPageInSpace(t, d, space, nil, "parent", "p\n")
	kidA := seedPageInSpace(t, d, space, &parent, "kid-a", "a\n")
	kidB := seedPageInSpace(t, d, space, &parent, "kid-b", "b\n")
	c := loginClient(t, ts, "alice", "alicepw12")

	if got := getTrash(t, c, ts.URL, space); len(got) != 0 {
		t.Fatalf("fresh space should have an empty bin, got %+v", got)
	}

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/pages/"+itoa(parent), nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("delete parent: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status=%d want 204", resp.StatusCode)
	}

	got := getTrash(t, c, ts.URL, space)
	if len(got) != 1 {
		t.Fatalf("bin should list the deleted parent only, got %+v", got)
	}
	if got[0].ID != parent || got[0].Title != "parent" || got[0].SubPages != 2 {
		t.Fatalf("entry = %+v, want parent id=%d with 2 sub-pages", got[0], parent)
	}

	resp, err = c.Post(ts.URL+"/api/pages/"+itoa(parent)+"/restore", "application/json", nil)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("restore status=%d body=%s", resp.StatusCode, b)
	}
	for _, id := range []int64{parent, kidA, kidB} {
		if deleted(t, d, id) {
			t.Fatalf("page %d still deleted after restoring the parent", id)
		}
	}
	if got := getTrash(t, c, ts.URL, space); len(got) != 0 {
		t.Fatalf("bin should be empty after the restore, got %+v", got)
	}
}

// TestTrash_RestoreRefusesOrphanedChild: a child deleted along with its parent
// is not offered on its own, and asking for it directly is refused — restoring
// it under a still-trashed parent would bring it back invisible.
func TestTrash_RestoreRefusesOrphanedChild(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	space := seedSpace(t, d, "Alice Space", "alice-space", alice)
	parent := seedPageInSpace(t, d, space, nil, "parent", "p\n")
	kid := seedPageInSpace(t, d, space, &parent, "kid", "k\n")
	c := loginClient(t, ts, "alice", "alicepw12")

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/pages/"+itoa(parent), nil)
	resp, _ := c.Do(req)
	resp.Body.Close()

	resp, err := c.Post(ts.URL+"/api/pages/"+itoa(kid)+"/restore", "application/json", nil)
	if err != nil {
		t.Fatalf("restore kid: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("restore of an orphaned child status=%d want 409", resp.StatusCode)
	}
	if !deleted(t, d, kid) {
		t.Fatal("refused restore must leave the child deleted")
	}
}

// TestTrash_RestoreLeavesSeparateDeletesAlone: a sub-page deleted on its own,
// before its parent went, carries a different cascade root — restoring the
// parent must not quietly undo that deliberate delete.
func TestTrash_RestoreLeavesSeparateDeletesAlone(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	space := seedSpace(t, d, "Alice Space", "alice-space", alice)
	parent := seedPageInSpace(t, d, space, nil, "parent", "p\n")
	gone := seedPageInSpace(t, d, space, &parent, "gone", "g\n")
	c := loginClient(t, ts, "alice", "alicepw12")

	for _, id := range []int64{gone, parent} {
		req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/pages/"+itoa(id), nil)
		resp, _ := c.Do(req)
		resp.Body.Close()
	}
	resp, err := c.Post(ts.URL+"/api/pages/"+itoa(parent)+"/restore", "application/json", nil)
	if err != nil {
		t.Fatalf("restore parent: %v", err)
	}
	resp.Body.Close()
	if deleted(t, d, parent) {
		t.Fatal("parent should be back")
	}
	if !deleted(t, d, gone) {
		t.Fatal("a sub-page deleted on its own must stay deleted")
	}
	// …and it is now a root of its own, so the bin offers it separately.
	got := getTrash(t, c, ts.URL, space)
	if len(got) != 1 || got[0].ID != gone || got[0].ParentTitle != "parent" {
		t.Fatalf("bin = %+v, want just %q under parent", got, "gone")
	}
}

// TestTrash_ReaderCannotRestore: looking in the bin needs read access, putting
// something back needs edit — same split as every other write.
func TestTrash_ReaderCannotRestore(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	bob := seedUser(t, d, "bob", "bobpw1234", false)
	space := seedSpace(t, d, "Alice Space", "alice-space", alice)
	page := seedPageInSpace(t, d, space, nil, "page", "p\n")
	if _, err := d.Exec(
		`INSERT INTO space_members (space_id, user_id, role) VALUES ($1, $2, 'viewer')`, space, bob); err != nil {
		t.Fatalf("add viewer: %v", err)
	}

	alicec := loginClient(t, ts, "alice", "alicepw12")
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/pages/"+itoa(page), nil)
	resp, _ := alicec.Do(req)
	resp.Body.Close()

	bobc := loginClient(t, ts, "bob", "bobpw1234")
	if got := getTrash(t, bobc, ts.URL, space); len(got) != 1 {
		t.Fatalf("a viewer should be able to see the bin, got %+v", got)
	}
	resp, err := bobc.Post(ts.URL+"/api/pages/"+itoa(page)+"/restore", "application/json", nil)
	if err != nil {
		t.Fatalf("bob restore: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer restore status=%d want 403", resp.StatusCode)
	}
}
