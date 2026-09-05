package api

import (
	"context"
	"database/sql"
	"testing"
)

// soft-delete: a deleted page is invisible to the live reads but the row (and
// its revisions) survive, so sync can resurrect it by id.

func deletedAt(t *testing.T, d *sql.DB, id int64) sql.NullString {
	t.Helper()
	var da sql.NullString
	if err := d.QueryRowContext(context.Background(),
		`SELECT deleted_at FROM pages WHERE id = $1`, id).Scan(&da); err != nil {
		t.Fatalf("read deleted_at: %v", err)
	}
	return da
}

func TestDeletePage_SoftDeletesAndHides(t *testing.T) {
	s, u, space := syncFixture(t)
	ctx := context.Background()

	p, _, ae := s.ApplyFileSync(ctx, u, nil, space, nil, "Doc.md", []byte("findme unique-token"))
	if ae != nil {
		t.Fatalf("create: %+v", ae)
	}

	if ae := s.deletePageCore(ctx, u, nil, p.ID, deleteViaManual); ae != nil {
		t.Fatalf("delete: %+v", ae)
	}

	// Row survives, stamped (not hard-deleted).
	if !deletedAt(t, s.DB, p.ID).Valid {
		t.Fatalf("page %d not soft-deleted (deleted_at NULL or row gone)", p.ID)
	}

	// Invisible to get / list / search.
	if _, err := selectPageByID(ctx, s.DB, p.ID); err != sql.ErrNoRows {
		t.Errorf("selectPageByID on trashed page: err=%v, want ErrNoRows", err)
	}
	if pages, err := listPagesFlat(ctx, s.DB, space, nil); err != nil || len(pages) != 0 {
		t.Errorf("listPagesFlat returned trashed page: n=%d err=%v", len(pages), err)
	}
	hits, ae := s.searchCore(ctx, u, nil, "unique-token", nil, 10)
	if ae != nil {
		t.Fatalf("search: %+v", ae)
	}
	if len(hits) != 0 {
		t.Errorf("search returned %d hits for a trashed page, want 0", len(hits))
	}
}

func TestDeletePage_CascadesSubtreeSoftly(t *testing.T) {
	s, u, space := syncFixture(t)
	ctx := context.Background()

	parent, _, _ := s.ApplyFileSync(ctx, u, nil, space, nil, "Parent.md", []byte("p"))
	child, _, _ := s.ApplyFileSync(ctx, u, nil, space, &parent.ID, "Child.md", []byte("c"))

	if ae := s.deletePageCore(ctx, u, nil, parent.ID, deleteViaManual); ae != nil {
		t.Fatalf("delete parent: %+v", ae)
	}
	// Both parent and the descendant are stamped — the whole subtree is trashed.
	if !deletedAt(t, s.DB, parent.ID).Valid {
		t.Errorf("parent not soft-deleted")
	}
	if !deletedAt(t, s.DB, child.ID).Valid {
		t.Errorf("child not soft-deleted with the subtree")
	}
}

func TestApplyFileSync_ResurrectsTrashedPage(t *testing.T) {
	s, u, space := syncFixture(t)
	ctx := context.Background()

	p, _, ae := s.ApplyFileSync(ctx, u, nil, space, nil, "Doc.md", []byte("v1"))
	if ae != nil {
		t.Fatalf("create: %+v", ae)
	}
	if ae := s.deletePageCore(ctx, u, nil, p.ID, deleteViaManual); ae != nil {
		t.Fatalf("delete: %+v", ae)
	}

	// Re-sync the same file (still carrying id=p.ID) with edited content → the
	// trashed page comes back by id, not a duplicate.
	p.Body = "v2 after resurrect"
	back, act, ae := s.ApplyFileSync(ctx, u, nil, space, nil, "Doc.md", emit(p))
	if ae != nil {
		t.Fatalf("resurrect: %+v", ae)
	}
	if act != syncResurrected {
		t.Fatalf("action = %q, want resurrected", act)
	}
	if back.ID != p.ID {
		t.Fatalf("resurrected as new id %d, want %d (no duplicate)", back.ID, p.ID)
	}
	if back.Body != "v2 after resurrect" {
		t.Fatalf("resurrected body = %q", back.Body)
	}
	if deletedAt(t, s.DB, p.ID).Valid {
		t.Errorf("page still trashed after resurrect")
	}
	if n := countPagesInSpace(t, s.DB, space); n != 1 {
		t.Fatalf("resurrect left %d pages, want 1 (no duplicate)", n)
	}
}

// A trashed page must not block creating a fresh page from a no-id file in the
// same spot (the deleted row is invisible to placement/sibling computation).
func TestApplyFileSync_CreateAfterDeleteNoCollision(t *testing.T) {
	s, u, space := syncFixture(t)
	ctx := context.Background()

	p, _, _ := s.ApplyFileSync(ctx, u, nil, space, nil, "Note.md", []byte("a"))
	if ae := s.deletePageCore(ctx, u, nil, p.ID, deleteViaManual); ae != nil {
		t.Fatalf("delete: %+v", ae)
	}
	// A brand-new file (no id), same title/location.
	fresh, act, ae := s.ApplyFileSync(ctx, u, nil, space, nil, "Note.md", []byte("b"))
	if ae != nil {
		t.Fatalf("create after delete: %+v", ae)
	}
	if act != syncCreated || fresh.ID == p.ID {
		t.Fatalf("expected a fresh create, got act=%q id=%d (old %d)", act, fresh.ID, p.ID)
	}
}
