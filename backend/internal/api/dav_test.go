package api

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zcag/tela/backend/internal/models"
)

// dav_test.go exercises the WebDAV sync surface end-to-end through the wired
// server (auth.Middleware bypass + the real webdav.Handler over davFS), using a
// PAT as the Basic-auth password the way stock clients (rclone, Finder) do.

// davClient is an http.Client that does NOT auto-follow redirects, so a PUT
// that would 301 (e.g. a missing trailing slash) surfaces instead of silently
// turning into a GET.
var davClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func davDo(t *testing.T, ts *httptest.Server, token, method, p, body string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+p, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request %s %s: %v", method, p, err)
	}
	if token != "" {
		req.SetBasicAuth("anyuser", token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := davClient.Do(req)
	if err != nil {
		t.Fatalf("do %s %s: %v", method, p, err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(data)
}

func countLivePages(t *testing.T, d *sql.DB, spaceID int64) int {
	t.Helper()
	var n int
	if err := d.QueryRowContext(context.Background(),
		`SELECT count(*) FROM pages WHERE space_id = $1 AND deleted_at IS NULL`, spaceID).Scan(&n); err != nil {
		t.Fatalf("count pages: %v", err)
	}
	return n
}

func pageByTitle(t *testing.T, d *sql.DB, spaceID int64, title string) (models.Page, bool) {
	t.Helper()
	row := d.QueryRowContext(context.Background(),
		`SELECT id, space_id, parent_id, title, body, position, props, created_at, updated_at, filename
		   FROM pages WHERE space_id = $1 AND title = $2 AND deleted_at IS NULL`, spaceID, title)
	p, err := scanPageFromRow(row)
	if err == sql.ErrNoRows {
		return models.Page{}, false
	}
	if err != nil {
		t.Fatalf("page by title %q: %v", title, err)
	}
	return p, true
}

// davFixture seeds one owner + one space + a write-scope PAT, returns the wired
// server, db, space id, the space folder name, and the raw token.
func davFixture(t *testing.T) (*httptest.Server, *sql.DB, int64, string, string) {
	t.Helper()
	ts, d := newWiredServer(t)
	uid := seedUser(t, d, "owner", "pw-owner-123", false)
	spaceID := seedSpace(t, d, "Engineering", "eng", uid)
	token, _ := seedAPIKeyForUser(t, d, uid, "write", nil)
	return ts, d, spaceID, "eng", token
}

func TestDAV_AuthRequired(t *testing.T) {
	ts, _, _, folder, _ := davFixture(t)
	resp, _ := davDo(t, ts, "", "PROPFIND", "/dav/"+folder+"/", "", map[string]string{"Depth": "1"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-cred PROPFIND status = %d, want 401", resp.StatusCode)
	}
	if got := resp.Header.Get("WWW-Authenticate"); !strings.Contains(got, "Basic") {
		t.Fatalf("WWW-Authenticate = %q, want Basic challenge", got)
	}
}

func TestDAV_PutCreatesAndGetRoundTrips(t *testing.T) {
	ts, d, spaceID, folder, token := davFixture(t)

	// No H1 / no frontmatter title → the title (and thus the slug/filename) comes
	// from the filename, so note.md round-trips to /dav/eng/note.md.
	body := "Hello from rclone.\n"
	resp, _ := davDo(t, ts, token, "PUT", "/dav/"+folder+"/note.md", body, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT create status = %d, want 201", resp.StatusCode)
	}
	if countLivePages(t, d, spaceID) != 1 {
		t.Fatalf("after create: %d live pages, want 1", countLivePages(t, d, spaceID))
	}
	p, ok := pageByTitle(t, d, spaceID, "note")
	if !ok {
		t.Fatal("page 'note' not created")
	}
	if p.Body != "Hello from rclone.\n" {
		t.Fatalf("stored body = %q, want %q", p.Body, "Hello from rclone.\n")
	}

	// GET returns canonical markdown: frontmatter (carrying the assigned id) + body.
	resp, got := davDo(t, ts, token, "GET", "/dav/"+folder+"/note.md", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(got, "Hello from rclone.") {
		t.Fatalf("GET body missing content:\n%s", got)
	}
	if !strings.Contains(got, "id:") || !strings.Contains(got, "title: note") {
		t.Fatalf("GET body missing frontmatter id/title:\n%s", got)
	}

	// Re-PUT the exact bytes we just read (the rclone steady state): binds by the
	// frontmatter id, nothing differs → idempotent no-op, NO duplicate page.
	resp, _ = davDo(t, ts, token, "PUT", "/dav/"+folder+"/note.md", got, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("re-PUT status = %d, want 201", resp.StatusCode)
	}
	if n := countLivePages(t, d, spaceID); n != 1 {
		t.Fatalf("after re-PUT: %d live pages, want 1 (no ping-pong duplicate)", n)
	}
}

func TestDAV_PropfindListsTree(t *testing.T) {
	ts, _, _, folder, token := davFixture(t)
	// Build a small tree over WebDAV: a folder page "guide" with one child.
	if resp, _ := davDo(t, ts, token, "MKCOL", "/dav/"+folder+"/guide", "", nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("MKCOL guide status = %d, want 201", resp.StatusCode)
	}
	if resp, _ := davDo(t, ts, token, "PUT", "/dav/"+folder+"/guide/setup.md", "Install.\n", nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT child status = %d, want 201", resp.StatusCode)
	}

	// Depth-1 PROPFIND on the space folder lists the root page as both a file and
	// (because it has a child) a folder.
	resp, multi := davDo(t, ts, token, "PROPFIND", "/dav/"+folder+"/", "", map[string]string{"Depth": "1"})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND status = %d, want 207\n%s", resp.StatusCode, multi)
	}
	if !strings.Contains(multi, "guide.md") {
		t.Fatalf("space PROPFIND missing guide.md:\n%s", multi)
	}

	// Depth-1 PROPFIND on the page folder lists its child.
	resp, multi = davDo(t, ts, token, "PROPFIND", "/dav/"+folder+"/guide/", "", map[string]string{"Depth": "1"})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("child PROPFIND status = %d, want 207\n%s", resp.StatusCode, multi)
	}
	if !strings.Contains(multi, "setup.md") {
		t.Fatalf("page-folder PROPFIND missing setup.md:\n%s", multi)
	}
}

func TestDAV_MkcolThenIndexPutBindsNoDuplicate(t *testing.T) {
	ts, d, spaceID, folder, token := davFixture(t)

	// rclone-style upload of a folder page: MKCOL the container, PUT a child, then
	// PUT the container's own index file. The index PUT must bind to the MKCOL'd
	// page (path-fallback), not mint a second "guide".
	resp, _ := davDo(t, ts, token, "MKCOL", "/dav/"+folder+"/guide", "", nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("MKCOL status = %d, want 201", resp.StatusCode)
	}
	resp, _ = davDo(t, ts, token, "PUT", "/dav/"+folder+"/guide/setup.md", "Install steps.\n", nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("child PUT status = %d, want 201", resp.StatusCode)
	}
	resp, _ = davDo(t, ts, token, "PUT", "/dav/"+folder+"/guide.md", "The guide body.\n", nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("index PUT status = %d, want 201", resp.StatusCode)
	}

	if n := countLivePages(t, d, spaceID); n != 2 {
		t.Fatalf("got %d live pages, want 2 (guide + setup, no duplicate guide)", n)
	}
	guide, ok := pageByTitle(t, d, spaceID, "guide")
	if !ok {
		t.Fatal("guide page missing")
	}
	if guide.Body != "The guide body.\n" {
		t.Fatalf("guide body = %q, want index PUT to have filled it in", guide.Body)
	}
}

func TestDAV_DeleteSoftDeletes(t *testing.T) {
	ts, d, spaceID, folder, token := davFixture(t)
	davDo(t, ts, token, "PUT", "/dav/"+folder+"/note.md", "doomed\n", nil)
	if countLivePages(t, d, spaceID) != 1 {
		t.Fatal("setup: expected 1 page")
	}

	resp, _ := davDo(t, ts, token, "DELETE", "/dav/"+folder+"/note.md", "", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", resp.StatusCode)
	}
	if n := countLivePages(t, d, spaceID); n != 0 {
		t.Fatalf("after DELETE: %d live pages, want 0", n)
	}
	resp, _ = davDo(t, ts, token, "GET", "/dav/"+folder+"/note.md", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after DELETE status = %d, want 404", resp.StatusCode)
	}
}

func TestDAV_ReadScopeCannotWrite(t *testing.T) {
	ts, d, _, folder, _ := davFixture(t)
	uid := seedUser(t, d, "reader", "pw-reader-123", false)
	// reader is a member of the eng space (so reads resolve) but holds a read PAT.
	var spaceID int64
	d.QueryRowContext(context.Background(), `SELECT id FROM spaces WHERE slug = 'eng'`).Scan(&spaceID)
	seedMember(t, d, spaceID, uid, "viewer")
	roTok, _ := seedAPIKeyForUser(t, d, uid, "read", nil)

	resp, _ := davDo(t, ts, roTok, "PROPFIND", "/dav/"+folder+"/", "", map[string]string{"Depth": "1"})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("read-scope PROPFIND status = %d, want 207", resp.StatusCode)
	}
	resp, _ = davDo(t, ts, roTok, "PUT", "/dav/"+folder+"/x.md", "nope\n", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("read-scope PUT status = %d, want 403", resp.StatusCode)
	}
}

func TestDAV_SpacePinnedPATHidesOtherSpaces(t *testing.T) {
	ts, d, engID, _, _ := davFixture(t)
	// owner also owns a second space; a PAT pinned to eng must not expose it.
	var ownerID int64
	d.QueryRowContext(context.Background(), `SELECT user_id FROM space_members WHERE space_id = $1 AND role='owner'`, engID).Scan(&ownerID)
	otherID := seedSpace(t, d, "Personal", "personal", ownerID)
	_ = otherID
	pinned, _ := seedAPIKeyForUser(t, d, ownerID, "write", &engID)

	resp, multi := davDo(t, ts, pinned, "PROPFIND", "/dav/", "", map[string]string{"Depth": "1"})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("root PROPFIND status = %d, want 207", resp.StatusCode)
	}
	if !strings.Contains(multi, "eng") {
		t.Fatalf("pinned root missing its own space:\n%s", multi)
	}
	if strings.Contains(multi, "personal") {
		t.Fatalf("pinned PAT leaked the other space:\n%s", multi)
	}
	// Direct access to the other space is also denied (not in the listing → 404).
	resp, _ = davDo(t, ts, pinned, "PROPFIND", "/dav/personal/", "", map[string]string{"Depth": "1"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("pinned access to other space status = %d, want 404", resp.StatusCode)
	}
}

// TestDAV_ThreeWayMergeCombinesEdits is the Phase-4 keystone end-to-end: a PUT
// establishes the client's base, the page is edited out-of-band in the DB (as if
// in the app), the client edits a DIFFERENT line locally and PUTs — the server
// must MERGE both, not clobber the app edit (which last-write-wins would).
func TestDAV_ThreeWayMergeCombinesEdits(t *testing.T) {
	ts, d, spaceID, folder, token := davFixture(t)
	ctx := context.Background()

	// 1. Client uploads the original → creates the page + records its base.
	original := "line1\nline2\nline3\n"
	if resp, _ := davDo(t, ts, token, "PUT", "/dav/"+folder+"/note.md", original, nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("initial PUT = %d, want 201", resp.StatusCode)
	}
	page, ok := pageByTitle(t, d, spaceID, "note")
	if !ok {
		t.Fatal("note page not created")
	}

	// 2. Out-of-band DB edit (someone edits line 2 in the app).
	if _, err := d.ExecContext(ctx,
		`UPDATE pages SET body = $1, updated_at = tela_now() WHERE id = $2`,
		"line1\nline2-APP\nline3\n", page.ID); err != nil {
		t.Fatalf("simulate app edit: %v", err)
	}

	// 3. Client edits a DIFFERENT line locally (line 3) against its base and PUTs.
	if resp, _ := davDo(t, ts, token, "PUT", "/dav/"+folder+"/note.md", "line1\nline2\nline3-LOCAL\n", nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("merge PUT = %d, want 201", resp.StatusCode)
	}

	// Both edits must survive — line2 from the app, line3 from the client.
	merged, _ := pageByTitle(t, d, spaceID, "note")
	if merged.Body != "line1\nline2-APP\nline3-LOCAL\n" {
		t.Fatalf("merged body = %q, want both edits combined", merged.Body)
	}
	var conflictAt sql.NullString
	d.QueryRowContext(ctx, `SELECT sync_conflict_at FROM pages WHERE id = $1`, page.ID).Scan(&conflictAt)
	if conflictAt.Valid {
		t.Fatalf("clean non-overlapping merge should NOT flag a conflict, got %q", conflictAt.String)
	}
}

// TestDAV_ThreeWayMergeConflict: both sides edit the SAME line → the local edit
// wins (default), the page is flagged, and the overridden DB side is kept as a
// recoverable `sync-conflict` revision.
func TestDAV_ThreeWayMergeConflict(t *testing.T) {
	ts, d, spaceID, folder, token := davFixture(t)
	ctx := context.Background()

	if resp, _ := davDo(t, ts, token, "PUT", "/dav/"+folder+"/note.md", "a\nb\nc\n", nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("initial PUT = %d, want 201", resp.StatusCode)
	}
	page, _ := pageByTitle(t, d, spaceID, "note")

	// App edits line 2 → B-APP; client edits the same line 2 → B-LOCAL.
	if _, err := d.ExecContext(ctx,
		`UPDATE pages SET body = $1, updated_at = tela_now() WHERE id = $2`, "a\nB-APP\nc\n", page.ID); err != nil {
		t.Fatalf("simulate app edit: %v", err)
	}
	if resp, _ := davDo(t, ts, token, "PUT", "/dav/"+folder+"/note.md", "a\nB-LOCAL\nc\n", nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("conflict PUT = %d, want 201", resp.StatusCode)
	}

	merged, _ := pageByTitle(t, d, spaceID, "note")
	if merged.Body != "a\nB-LOCAL\nc\n" {
		t.Fatalf("conflict body = %q, want incoming (local) to win", merged.Body)
	}
	var conflictAt sql.NullString
	d.QueryRowContext(ctx, `SELECT sync_conflict_at FROM pages WHERE id = $1`, page.ID).Scan(&conflictAt)
	if !conflictAt.Valid {
		t.Fatal("conflict should set sync_conflict_at")
	}
	// The overridden DB side (B-APP) must be recoverable as a revision.
	var n int
	d.QueryRowContext(ctx,
		`SELECT count(*) FROM page_revisions WHERE page_id = $1 AND source = 'sync-conflict' AND body LIKE '%B-APP%'`,
		page.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("want 1 sync-conflict revision preserving the DB side, got %d", n)
	}
}

// TestDAV_BaseGapSeededOnRead: a page created in the app (never PUT by this
// client) has no merge base. The FIRST download (GET) seeds it (insert-if-
// absent), so a subsequent local edit 3-way-merges instead of clobbering — and a
// later probe GET must NOT overwrite that base.
func TestDAV_BaseGapSeededOnRead(t *testing.T) {
	ts, d, spaceID, folder, token := davFixture(t)
	ctx := context.Background()

	// Created in-app (direct insert → no sync_base for the client).
	var pid int64
	if err := d.QueryRowContext(ctx,
		`INSERT INTO pages (space_id, title, body, props) VALUES ($1, 'note', $2, '{}'::jsonb) RETURNING id`,
		spaceID, "L1\nL2\nL3\n").Scan(&pid); err != nil {
		t.Fatalf("seed page: %v", err)
	}

	// First download seeds the base.
	if resp, _ := davDo(t, ts, token, "GET", "/dav/"+folder+"/note.md", "", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d, want 200", resp.StatusCode)
	}
	var base string
	if err := d.QueryRowContext(ctx, `SELECT base_body FROM sync_base WHERE page_id = $1`, pid).Scan(&base); err != nil {
		t.Fatalf("base not seeded on first read: %v", err)
	}
	if base != "L1\nL2\nL3\n" {
		t.Fatalf("seeded base = %q, want the downloaded body", base)
	}

	// Out-of-band app edit (line 1), then a probe GET — base must stay put.
	d.ExecContext(ctx, `UPDATE pages SET body = $1, updated_at = tela_now() WHERE id = $2`, "L1-APP\nL2\nL3\n", pid)
	davDo(t, ts, token, "GET", "/dav/"+folder+"/note.md", "", nil)
	d.QueryRowContext(ctx, `SELECT base_body FROM sync_base WHERE page_id = $1`, pid).Scan(&base)
	if base != "L1\nL2\nL3\n" {
		t.Fatalf("base overwritten on read (insert-if-absent violated): %q", base)
	}

	// Client edits line 3 locally and PUTs → both edits survive (merge, not LWW).
	if resp, _ := davDo(t, ts, token, "PUT", "/dav/"+folder+"/note.md", "L1\nL2\nL3-LOCAL\n", nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT = %d, want 201", resp.StatusCode)
	}
	merged, _ := pageByTitle(t, d, spaceID, "note")
	if merged.Body != "L1-APP\nL2\nL3-LOCAL\n" {
		t.Fatalf("base-gap merge body = %q, want both edits", merged.Body)
	}
}

// TestDAV_DeleteRequiresPriorSync: the cursor gate (sync §6). A client may only
// delete a page it has previously synced (has a base for) — a page created in
// the app that this client never pulled must NOT be deletable, so a partial /
// fresh client can't wipe pages it never had. After a GET seeds the base, the
// same delete is honoured.
func TestDAV_DeleteRequiresPriorSync(t *testing.T) {
	ts, d, spaceID, folder, token := davFixture(t)
	ctx := context.Background()

	var pid int64
	if err := d.QueryRowContext(ctx,
		`INSERT INTO pages (space_id, title, body, props) VALUES ($1, 'note', 'x', '{}'::jsonb) RETURNING id`,
		spaceID).Scan(&pid); err != nil {
		t.Fatalf("seed page: %v", err)
	}

	// Never synced by this client → refused; page stays live.
	if resp, _ := davDo(t, ts, token, "DELETE", "/dav/"+folder+"/note.md", "", nil); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE of never-synced page = %d, want 405 (refused)", resp.StatusCode)
	}
	if countLivePages(t, d, spaceID) != 1 {
		t.Fatal("page must remain after a refused delete")
	}

	// A download seeds the base → the client now "had" it → delete is honoured.
	davDo(t, ts, token, "GET", "/dav/"+folder+"/note.md", "", nil)
	if resp, _ := davDo(t, ts, token, "DELETE", "/dav/"+folder+"/note.md", "", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE after sync = %d, want 204", resp.StatusCode)
	}
	if countLivePages(t, d, spaceID) != 0 {
		t.Fatal("page should be soft-deleted after an allowed delete")
	}
}

// TestDAV_MassDeleteGuard: the brake trips once an anomalous fraction of the
// space vanishes in a window. With a floor of 2 and fraction 0.5 over 6 pages,
// the limit is max(2, 3) = 3 → only 3 deletes are honoured.
func TestDAV_MassDeleteGuard(t *testing.T) {
	t.Setenv("TELA_WEBDAV_DELETE_FLOOR", "2") // before the server builds its guard
	ts, d, spaceID, folder, token := davFixture(t)

	for i := 0; i < 6; i++ {
		name := fmt.Sprintf("/dav/%s/note%d.md", folder, i)
		if resp, _ := davDo(t, ts, token, "PUT", name, fmt.Sprintf("body %d\n", i), nil); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT note%d = %d", i, resp.StatusCode)
		}
	}

	allowed := 0
	for i := 0; i < 6; i++ {
		resp, _ := davDo(t, ts, token, "DELETE", fmt.Sprintf("/dav/%s/note%d.md", folder, i), "", nil)
		switch resp.StatusCode {
		case http.StatusNoContent:
			allowed++
		case http.StatusMethodNotAllowed:
			// guard tripped — expected past the limit
		default:
			t.Fatalf("DELETE note%d unexpected status %d", i, resp.StatusCode)
		}
	}
	if allowed != 3 {
		t.Fatalf("mass-delete guard allowed %d deletes, want 3 (limit max(2, 0.5*6))", allowed)
	}
	if n := countLivePages(t, d, spaceID); n != 3 {
		t.Fatalf("%d live pages after guarded run, want 3", n)
	}
}

// TestDAV_SyncPreservesOverwrittenContent: even a last-write-wins overwrite (no
// base → no merge) must keep what it replaced in revision history, so a sync can
// never silently lose server content. The overwritten state lands as a
// `sync-prior` revision, in the same tx as the write.
func TestDAV_SyncPreservesOverwrittenContent(t *testing.T) {
	ts, d, spaceID, folder, token := davFixture(t)
	ctx := context.Background()

	// In-app page (no base for this client → the PUT is last-write-wins).
	var pid int64
	if err := d.QueryRowContext(ctx,
		`INSERT INTO pages (space_id, title, body, props) VALUES ($1, 'note', $2, '{}'::jsonb) RETURNING id`,
		spaceID, "original server content\n").Scan(&pid); err != nil {
		t.Fatalf("seed page: %v", err)
	}

	if resp, _ := davDo(t, ts, token, "PUT", "/dav/"+folder+"/note.md", "replaced by sync\n", nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT = %d, want 201", resp.StatusCode)
	}

	// Live page is the incoming (LWW), and the overwritten content survives.
	p, _ := pageByTitle(t, d, spaceID, "note")
	if p.Body != "replaced by sync\n" {
		t.Fatalf("live body = %q, want the incoming (LWW)", p.Body)
	}
	var n int
	d.QueryRowContext(ctx,
		`SELECT count(*) FROM page_revisions WHERE page_id = $1 AND source = 'sync-prior' AND body LIKE '%original server content%'`,
		pid).Scan(&n)
	if n != 1 {
		t.Fatalf("want 1 sync-prior revision preserving the overwritten content, got %d", n)
	}
}

// --- pure unit tests (no DB) for the path + slug layer ---

func TestDavSplit(t *testing.T) {
	cases := []struct {
		in   string
		segs []string
		ok   bool
	}{
		{"/", nil, true},
		{"", nil, true},
		{"/eng", []string{"eng"}, true},
		{"/eng/Notes.md", []string{"eng", "Notes.md"}, true},
		{"/eng/a/b.md", []string{"eng", "a", "b.md"}, true},
		{"/eng/../secret", nil, false},
		{"/eng//x", nil, false},
		{"/eng/./x", nil, false},
	}
	for _, c := range cases {
		segs, ok := davSplit(c.in)
		if ok != c.ok {
			t.Fatalf("davSplit(%q) ok = %v, want %v", c.in, ok, c.ok)
		}
		if ok && strings.Join(segs, "/") != strings.Join(c.segs, "/") {
			t.Fatalf("davSplit(%q) = %v, want %v", c.in, segs, c.segs)
		}
	}
}

func TestSiblingSlugsDedup(t *testing.T) {
	sibs := []models.Page{
		{ID: 1, Title: "Notes"},
		{ID: 2, Title: "Notes"}, // same slug → -2
		{ID: 3, Title: "Other"},
	}
	got := siblingSlugs(sibs)
	if got[1] != "notes" || got[2] != "notes-2" || got[3] != "other" {
		t.Fatalf("siblingSlugs = %v, want notes/notes-2/other", got)
	}
}

func TestSpaceTreeResolve(t *testing.T) {
	pid := int64(1)
	t0 := &spaceTree{
		children: map[int64][]models.Page{
			rootParentKey: {{ID: 1, Title: "Guide"}},
			1:             {{ID: 2, Title: "Setup", ParentID: &pid}},
		},
		slug: map[int64]string{1: "guide", 2: "setup"},
	}
	// file form
	if p, isFile, ok := t0.resolve([]string{"guide.md"}); !ok || !isFile || p.ID != 1 {
		t.Fatalf("resolve guide.md = (%d,%v,%v), want (1,true,true)", p.ID, isFile, ok)
	}
	// folder form
	if p, isFile, ok := t0.resolve([]string{"guide"}); !ok || isFile || p.ID != 1 {
		t.Fatalf("resolve guide = (%d,%v,%v), want (1,false,true)", p.ID, isFile, ok)
	}
	// nested file
	if p, isFile, ok := t0.resolve([]string{"guide", "setup.md"}); !ok || !isFile || p.ID != 2 {
		t.Fatalf("resolve guide/setup.md = (%d,%v,%v), want (2,true,true)", p.ID, isFile, ok)
	}
	// .md mid-path is malformed
	if _, _, ok := t0.resolve([]string{"guide.md", "setup.md"}); ok {
		t.Fatal("resolve with .md mid-path should fail")
	}
	// unknown
	if _, _, ok := t0.resolve([]string{"missing.md"}); ok {
		t.Fatal("resolve unknown should fail")
	}
}

// TestDAV_ThreeWayMergeSurvivesDeleteThenRepush: rclone bisync resolves a
// both-sides edit as DELETE(loser) + PUT(winner) (`--conflict-loser delete`),
// so the write lands on the RESURRECT path rather than the bound one. It must
// merge exactly the same — applying the incoming file blind there made the
// documented "your edits combine" contract silently last-write-wins.
func TestDAV_ThreeWayMergeSurvivesDeleteThenRepush(t *testing.T) {
	ts, d, spaceID, folder, token := davFixture(t)
	ctx := context.Background()
	path := "/dav/" + folder + "/note.md"

	if resp, _ := davDo(t, ts, token, "PUT", path, "line1\nline2\nline3\n", nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("initial PUT = %d, want 201", resp.StatusCode)
	}
	page, ok := pageByTitle(t, d, spaceID, "note")
	if !ok {
		t.Fatal("note page not created")
	}
	// What the client holds on disk: the canonical file, id and all.
	_, canonical := davDo(t, ts, token, "GET", path, "", nil)

	// The app edits line 2 while the client edits line 3.
	if _, err := d.ExecContext(ctx,
		`UPDATE pages SET body = $1, updated_at = tela_now() WHERE id = $2`,
		"line1\nline2-APP\nline3\n", page.ID); err != nil {
		t.Fatalf("simulate app edit: %v", err)
	}
	local := strings.Replace(canonical, "line3", "line3-LOCAL", 1)

	// The conflict resolution: delete the losing (remote) copy, re-push the winner.
	if resp, _ := davDo(t, ts, token, "DELETE", path, "", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE = %d, want 204", resp.StatusCode)
	}
	if resp, _ := davDo(t, ts, token, "PUT", path, local, nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("re-push PUT = %d, want 201", resp.StatusCode)
	}

	merged, ok := pageByTitle(t, d, spaceID, "note")
	if !ok {
		t.Fatal("page not resurrected")
	}
	if merged.ID != page.ID {
		t.Fatalf("resurrect forked a new page: id %d, want %d", merged.ID, page.ID)
	}
	if merged.Body != "line1\nline2-APP\nline3-LOCAL\n" {
		t.Fatalf("merged body = %q, want both edits combined across the delete", merged.Body)
	}
	if n := countLivePages(t, d, spaceID); n != 1 {
		t.Fatalf("%d live pages, want 1", n)
	}
}
