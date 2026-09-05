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

func deletePage(t *testing.T, c *http.Client, base string, id int64) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, base+"/api/pages/"+itoa(id), nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("delete page %d: %v", id, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete page %d status=%d want 204", id, resp.StatusCode)
	}
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

	deletePage(t, c, ts.URL, parent)

	got := getTrash(t, c, ts.URL, space)
	if len(got) != 1 {
		t.Fatalf("bin should list the deleted parent only, got %+v", got)
	}
	if got[0].ID != parent || got[0].Title != "parent" || got[0].SubPages != 2 {
		t.Fatalf("entry = %+v, want parent id=%d with 2 sub-pages", got[0], parent)
	}

	resp, err := c.Post(ts.URL+"/api/pages/"+itoa(parent)+"/restore", "application/json", nil)
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

	deletePage(t, c, ts.URL, parent)

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
		deletePage(t, c, ts.URL, id)
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

// TestTrash_ViewerSeesNothingAndCannotRestore: a read-only member has no
// deletes of their own and does not own the space, so the bin is empty for them
// — the fact that someone removed a page is not everyone's business — and
// restoring is refused.
func TestTrash_ViewerSeesNothingAndCannotRestore(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	bob := seedUser(t, d, "bob", "bobpw1234", false)
	space := seedSpace(t, d, "Alice Space", "alice-space", alice)
	page := seedPageInSpace(t, d, space, nil, "page", "p\n")
	seedMember(t, d, space, bob, "viewer")

	alicec := loginClient(t, ts, "alice", "alicepw12")
	deletePage(t, alicec, ts.URL, page)

	bobc := loginClient(t, ts, "bob", "bobpw1234")
	if got := getTrash(t, bobc, ts.URL, space); len(got) != 0 {
		t.Fatalf("a viewer should see none of someone else's deletes, got %+v", got)
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

// TestTrash_ScopedByWhoDeleted: in a shared space you see your own deletes; the
// space owner sees everyone's, labelled. Deleting a page was always visible to
// every member (it vanishes from the space), but who removed it and the power to
// put it back are not.
func TestTrash_ScopedByWhoDeleted(t *testing.T) {
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

	mine := getTrash(t, bobc, ts.URL, space)
	if len(mine) != 1 || mine[0].ID != byBob {
		t.Fatalf("an editor should see only their own delete, got %+v", mine)
	}
	if !mine[0].DeletedByYou || mine[0].DeletedVia != deleteViaManual {
		t.Fatalf("own row = %+v, want deleted_by_you with via=manual", mine[0])
	}

	all := getTrash(t, alicec, ts.URL, space)
	if len(all) != 2 {
		t.Fatalf("the owner should see the whole bin, got %+v", all)
	}
	for _, e := range all {
		switch e.ID {
		case byAlice:
			if !e.DeletedByYou || e.DeletedBy != "alice" {
				t.Errorf("alice's own row = %+v, want deleted_by_you and her name", e)
			}
		case byBob:
			if e.DeletedByYou || e.DeletedBy != "bob" {
				t.Errorf("bob's row as seen by the owner = %+v, want attributed to bob", e)
			}
		}
	}

	// And bob cannot undo alice's delete — it reads as not-there, so ids can't be
	// probed for deletes you aren't allowed to see.
	resp, err := bobc.Post(ts.URL+"/api/pages/"+itoa(byAlice)+"/restore", "application/json", nil)
	if err != nil {
		t.Fatalf("bob restore alice's page: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("restoring someone else's delete status=%d want 404", resp.StatusCode)
	}
	if !deleted(t, d, byAlice) {
		t.Fatal("refused restore must leave the page deleted")
	}
}
