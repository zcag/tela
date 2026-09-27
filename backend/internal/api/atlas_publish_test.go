package api

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/zcag/tela/backend/internal/atlas/core"
	"github.com/zcag/tela/backend/internal/atlas/engine"
	"github.com/zcag/tela/backend/internal/testdb"
)

// captureReindex is a queueReindex hook that records every page id queued.
type captureReindex struct {
	mu  sync.Mutex
	ids []int64
}

func (c *captureReindex) fn(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ids = append(c.ids, id)
}

func (c *captureReindex) snapshot() []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := append([]int64(nil), c.ids...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (c *captureReindex) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ids = nil
}

// seedAtlasFixtures inserts a user, space, atlas_sources, and atlas_runs row,
// returning the source id and run id.
func seedAtlasFixtures(t *testing.T, d *sql.DB) (spaceID, sourceID, runID int64) {
	t.Helper()
	ctx := context.Background()
	uid := seedUser(t, d, "atlas-pub", "pw", false)
	spaceID = seedSpace(t, d, "Atlas", "atlas", uid)
	pid := seedAtlasProject(t, d, "Atlas", accountUser, uid, spaceID, 0)
	if err := d.QueryRowContext(ctx,
		`INSERT INTO atlas_sources (project_id, type, location, name)
		 VALUES ($1, 'git', 'https://example.com/acme/widget.git', '') RETURNING id`,
		pid).Scan(&sourceID); err != nil {
		t.Fatalf("insert atlas_sources: %v", err)
	}
	if err := d.QueryRowContext(ctx,
		`INSERT INTO atlas_runs (source_id, status) VALUES ($1, 'running') RETURNING id`,
		sourceID).Scan(&runID); err != nil {
		t.Fatalf("insert atlas_runs: %v", err)
	}
	return spaceID, sourceID, runID
}

func runContext(sourceID, runID int64, pages []core.Page) *engine.RunContext {
	return &engine.RunContext{
		Source: &core.Source{ID: sourceID, Type: core.SourceGit,
			Location: "https://example.com/acme/widget.git", Ref: "abcdef1234567890"},
		Run: &core.Run{ID: runID, SourceID: sourceID, Status: core.RunDone,
			Stats: &core.RunStats{Files: 12, Surface: 30, Chunks: 80, Pages: len(pages),
				DurationSec: 95, ChatModel: "qwen", EmbedModel: "qwen-embed",
				Usage: core.Usage{ChatCalls: 5, EmbedCalls: 2, PromptTokens: 12345, CompletionTokens: 6789, EmbedTokens: 4321}}},
		Coverage: core.Coverage{Total: 30, Covered: 27, MustTotal: 10, MustCovered: 9, Citations: 14, Mermaid: 2},
		Art: core.Artifacts{
			Spine: []core.SpineItem{
				{Kind: core.KindRoute, Name: "GET /x", File: "x.go", Line: 1},
				{Kind: core.KindEntrypoint, Name: "main", File: "main.go", Line: 1},
			},
			Pages: pages,
		},
	}
}

func livePageByMap(t *testing.T, d *sql.DB, sourceID int64, slug string) (id int64, parent sql.NullInt64, title, body string, deleted sql.NullString, props string, ok bool) {
	t.Helper()
	err := d.QueryRowContext(context.Background(),
		`SELECT p.id, p.parent_id, p.title, p.body, p.deleted_at, p.props::text
		   FROM atlas_page_map m JOIN pages p ON p.id = m.page_id
		  WHERE m.source_id = $1 AND m.slug = $2`, sourceID, slug).
		Scan(&id, &parent, &title, &body, &deleted, &props)
	if err == sql.ErrNoRows {
		return 0, parent, "", "", deleted, "", false
	}
	if err != nil {
		t.Fatalf("livePageByMap %s: %v", slug, err)
	}
	return id, parent, title, body, deleted, props, true
}

func TestAtlasPublish(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	spaceID, sourceID, runID := seedAtlasFixtures(t, d)

	capt := &captureReindex{}
	pub := newAtlasPublisher(d, capt.fn, spaceID, nil)

	pages := []core.Page{
		{Order: 0, Kind: core.PageNarrative, Title: "Overview", Slug: "overview", Body: "# Overview\n\nbody A"},
		{Order: 1, Kind: core.PageReference, Title: "Routes", Slug: "routes", Body: "# Routes\n\nbody B"},
		{Order: 2, Kind: core.PageNarrative, Title: "Config", Slug: "config", Body: "# Config\n\nbody C"},
	}

	// --- first publish: creates root + 3 docs ---
	if err := pub.Publish(ctx, runContext(sourceID, runID, pages)); err != nil {
		t.Fatalf("first publish: %v", err)
	}

	rootID, rootParent, rootTitle, rootBody, rootDel, rootProps, ok := livePageByMap(t, d, sourceID, "__root__")
	if !ok {
		t.Fatal("root page not created")
	}
	if rootParent.Valid {
		t.Errorf("root parent_id = %v, want NULL (space root)", rootParent.Int64)
	}
	if rootDel.Valid {
		t.Error("root page is soft-deleted")
	}
	if rootTitle != "widget - repo" {
		t.Errorf("root title = %q, want %q (repo base name + kind tag)", rootTitle, "widget - repo")
	}
	if !strings.Contains(rootProps, `"generator": "atlas"`) {
		t.Errorf("root props missing generator=atlas: %s", rootProps)
	}
	if !strings.Contains(rootBody, "# widget — documentation") || !strings.Contains(rootBody, "$0 (local)") {
		t.Errorf("root body not the rendered overview:\n%s", rootBody)
	}

	created := map[string]int64{"__root__": rootID}
	for _, pg := range pages {
		id, parent, title, body, del, props, ok := livePageByMap(t, d, sourceID, pg.Slug)
		if !ok {
			t.Fatalf("page %q not created", pg.Slug)
		}
		if !parent.Valid || parent.Int64 != rootID {
			t.Errorf("page %q parent_id = %v, want root %d", pg.Slug, parent, rootID)
		}
		if title != pg.Title || body != pg.Body {
			t.Errorf("page %q title/body mismatch: got %q/%q", pg.Slug, title, body)
		}
		if del.Valid {
			t.Errorf("page %q soft-deleted after create", pg.Slug)
		}
		if !strings.Contains(props, `"source": "widget"`) {
			t.Errorf("page %q props missing source: %s", pg.Slug, props)
		}
		created[pg.Slug] = id
	}

	// reindex queued for every created page (root + 3)
	wantIDs := sortedVals(created)
	if got := capt.snapshot(); !equalInt64(got, wantIDs) {
		t.Errorf("reindex ids after create = %v, want %v", got, wantIDs)
	}

	// every created page got its first revision, authored by no one, source agent
	for slug, id := range created {
		if n := revCount(t, d, id); n != 1 {
			t.Errorf("page %q revisions after create = %d, want 1", slug, n)
		}
	}
	var src string
	var author sql.NullInt64
	if err := d.QueryRowContext(ctx, `SELECT source, author_id FROM page_revisions WHERE page_id = $1`, created["overview"]).Scan(&src, &author); err != nil {
		t.Fatalf("load revision: %v", err)
	}
	if src != "agent" || author.Valid {
		t.Errorf("atlas revision source/author = %q/%v, want agent/NULL", src, author)
	}

	// page count under the space = root + 3 docs
	if n := liveCount(t, d, spaceID); n != 4 {
		t.Fatalf("live page count = %d, want 4", n)
	}

	// --- second publish, unchanged: no churn, no reindex ---
	capt.reset()
	upBefore := updatedAtSnapshot(t, d, spaceID)
	if err := pub.Publish(ctx, runContext(sourceID, runID, pages)); err != nil {
		t.Fatalf("second publish: %v", err)
	}
	if n := liveCount(t, d, spaceID); n != 4 {
		t.Errorf("live page count after no-op republish = %d, want 4 (no dupes)", n)
	}
	if got := capt.snapshot(); len(got) != 0 {
		t.Errorf("reindex queued on no-op republish: %v", got)
	}
	for slug, id := range created {
		if n := revCount(t, d, id); n != 1 {
			t.Errorf("page %q revisions after no-op republish = %d, want 1", slug, n)
		}
	}
	upAfter := updatedAtSnapshot(t, d, spaceID)
	for id, before := range upBefore {
		if upAfter[id] != before {
			t.Errorf("page %d updated_at churned on no-op republish: %q -> %q", id, before, upAfter[id])
		}
	}

	// --- third publish with "config" removed: it is pruned, others intact ---
	capt.reset()
	trimmed := pages[:2] // drop "config"
	if err := pub.Publish(ctx, runContext(sourceID, runID, trimmed)); err != nil {
		t.Fatalf("third publish: %v", err)
	}
	// config's page is soft-deleted and its map row gone
	var deletedAt sql.NullString
	if err := d.QueryRowContext(ctx,
		`SELECT deleted_at FROM pages WHERE id = $1`, created["config"]).Scan(&deletedAt); err != nil {
		t.Fatalf("load pruned page: %v", err)
	}
	if !deletedAt.Valid {
		t.Error("pruned page 'config' not soft-deleted")
	}
	var mapRows int
	if err := d.QueryRowContext(ctx,
		`SELECT count(*) FROM atlas_page_map WHERE source_id = $1 AND slug = 'config'`, sourceID).Scan(&mapRows); err != nil {
		t.Fatalf("count map: %v", err)
	}
	if mapRows != 0 {
		t.Error("pruned page 'config' map row not removed")
	}
	// survivors intact
	for _, slug := range []string{"__root__", "overview", "routes"} {
		if _, _, _, _, del, _, ok := livePageByMap(t, d, sourceID, slug); !ok || del.Valid {
			t.Errorf("survivor %q gone/deleted after prune (ok=%v del=%v)", slug, ok, del.Valid)
		}
	}
	if n := liveCount(t, d, spaceID); n != 3 {
		t.Errorf("live page count after prune = %d, want 3", n)
	}
	// Only the root reindexes: its overview body changed (the Pages list lost
	// "Config" and the stats page-count dropped 3→2). The pruned page and the
	// unchanged survivors must NOT reindex.
	if got := capt.snapshot(); !equalInt64(got, []int64{rootID}) {
		t.Errorf("reindex after prune = %v, want just root %d", got, rootID)
	}
	// The changed root snapshots again; the pruned page keeps its history.
	if n := revCount(t, d, rootID); n != 2 {
		t.Errorf("root revisions after prune = %d, want 2", n)
	}
	if n := revCount(t, d, created["config"]); n != 1 {
		t.Errorf("pruned page revisions = %d, want 1 (kept in trash)", n)
	}
}

func revCount(t *testing.T, d *sql.DB, pageID int64) int {
	t.Helper()
	var n int
	if err := d.QueryRowContext(context.Background(),
		`SELECT count(*) FROM page_revisions WHERE page_id = $1`, pageID).Scan(&n); err != nil {
		t.Fatalf("revCount: %v", err)
	}
	return n
}

// --- small test helpers ---

func liveCount(t *testing.T, d *sql.DB, spaceID int64) int {
	t.Helper()
	var n int
	if err := d.QueryRowContext(context.Background(),
		`SELECT count(*) FROM pages WHERE space_id = $1 AND deleted_at IS NULL`, spaceID).Scan(&n); err != nil {
		t.Fatalf("liveCount: %v", err)
	}
	return n
}

func updatedAtSnapshot(t *testing.T, d *sql.DB, spaceID int64) map[int64]string {
	t.Helper()
	rows, err := d.QueryContext(context.Background(),
		`SELECT id, updated_at FROM pages WHERE space_id = $1 AND deleted_at IS NULL`, spaceID)
	if err != nil {
		t.Fatalf("updatedAtSnapshot: %v", err)
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var ts string
		if err := rows.Scan(&id, &ts); err != nil {
			t.Fatalf("scan updated_at: %v", err)
		}
		out[id] = ts
	}
	return out
}

func sortedVals(m map[string]int64) []int64 {
	out := make([]int64, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func equalInt64(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
