package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// TestPublicReaderOG_AuthorIsPageAuthor — the crawler JSON-LD credits the page's
// own author (first revision), not the space owner, so the machine-readable
// author matches the human-visible byline. Falls back to the owner only when the
// page has no recorded author.
func TestPublicReaderOG_AuthorIsPageAuthor(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	ctx := context.Background()

	owner := seedUser(t, d, "owner", "ownerpw12345", false)
	writer := seedUser(t, d, "writer", "writerpw1234", false)
	spaceID := seedSpace(t, d, "Docs", "docs", owner)
	if _, err := d.ExecContext(ctx, `UPDATE spaces SET visibility = 'public' WHERE id = $1`, spaceID); err != nil {
		t.Fatalf("publish: %v", err)
	}
	pageID := seedPage(t, d, spaceID, "Guide")
	if _, err := insertPageRevision(ctx, d, pageID, "body", "Guide", nil, &writer, "create"); err != nil {
		t.Fatalf("rev: %v", err)
	}

	rec := routedRecorder("GET /public/spaces/{id}/pages/{page_id}",
		srv.HandlePublicReaderOG,
		userRequest(http.MethodGet, "/public/spaces/1/pages/"+strconv.FormatInt(pageID, 10), "", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Author should be the writer, linked to their unified handle home (/{handle},
	// not the legacy /u/ form) — and not the owner.
	if !strings.Contains(body, `"name":"writer"`) || !strings.Contains(body, `"url":"/writer"`) {
		t.Fatalf("JSON-LD author isn't the page author:\n%s", body)
	}
	if strings.Contains(body, `"name":"owner"`) {
		t.Fatalf("JSON-LD still credits the space owner:\n%s", body)
	}
}

// TestPublicReaderOG_FallsBackToOwner — a legacy page with no revision trail
// credits the space owner (single-author blogs, where they coincide).
func TestPublicReaderOG_FallsBackToOwner(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	ctx := context.Background()

	owner := seedUser(t, d, "owner", "ownerpw12345", false)
	spaceID := seedSpace(t, d, "Docs", "docs", owner)
	if _, err := d.ExecContext(ctx, `UPDATE spaces SET visibility = 'public' WHERE id = $1`, spaceID); err != nil {
		t.Fatalf("publish: %v", err)
	}
	pageID := seedPage(t, d, spaceID, "Legacy") // no page_revision

	rec := routedRecorder("GET /public/spaces/{id}/pages/{page_id}",
		srv.HandlePublicReaderOG,
		userRequest(http.MethodGet, "/public/spaces/1/pages/"+strconv.FormatInt(pageID, 10), "", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"name":"owner"`) {
		t.Fatalf("expected owner fallback:\n%s", rec.Body.String())
	}
}
