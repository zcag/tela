package rag

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zcag/tela/backend/internal/testdb"
)

// --- test embedders -------------------------------------------------------

// namedEmbedder reports a configurable model name (for model-drift tests).
type namedEmbedder struct {
	fakeEmbedder
	model string
}

func (n *namedEmbedder) Model() string { return n.model }

// flakyEmbedder fails its first failUntil Embed calls, then succeeds — models a
// transient embedder outage for the retry/self-heal path.
type flakyEmbedder struct {
	fakeEmbedder
	mu        sync.Mutex
	attempts  int
	failUntil int
}

func (f *flakyEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	f.mu.Lock()
	f.attempts++
	n := f.attempts
	f.mu.Unlock()
	if n <= f.failUntil {
		return nil, errors.New("embedder down")
	}
	return f.fakeEmbedder.Embed(ctx, text)
}

func (f *flakyEmbedder) attemptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

// pickyEmbedder fails on any text containing "BOOM" — models one un-embeddable
// page among many (resilience test).
type pickyEmbedder struct{ fakeEmbedder }

func (p *pickyEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if strings.Contains(text, "BOOM") {
		return nil, errors.New("boom")
	}
	return p.fakeEmbedder.Embed(ctx, text)
}

// --- self-healing: retry after embedder failure --------------------------

func TestAutoReindex_RetriesAfterFailure(t *testing.T) {
	withFastWindows(t)
	d := testdb.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	u := newUser(t, d, "carol")
	sp := newSpace(t, d, "charlie", u)
	page := newPage(t, d, sp, "Doc", "## A\ncontent that fails once then indexes")

	emb := &flakyEmbedder{failUntil: 1} // first embed attempt fails, retry succeeds
	svc := NewServiceWithEmbedder(d, emb)
	svc.StartAutoReindex(ctx)
	svc.QueueReindex(page)

	if !waitForChunks(t, d, page, 2*time.Second) {
		t.Fatalf("page never indexed after retry (attempts=%d)", emb.attemptCount())
	}
	if emb.attemptCount() < 2 {
		t.Fatalf("expected ≥2 embed attempts (fail + retry), got %d", emb.attemptCount())
	}
}

// --- self-healing: stale sweep recovers an unqueued page -----------------

func TestStaleSweep_RecoversUnindexedPage(t *testing.T) {
	withFastWindows(t)
	origDelay, origInterval := staleSweepInitialDelay, staleSweepInterval
	staleSweepInitialDelay, staleSweepInterval = 10*time.Millisecond, 30*time.Millisecond
	t.Cleanup(func() { staleSweepInitialDelay, staleSweepInterval = origDelay, origInterval })

	d := testdb.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	u := newUser(t, d, "dave")
	sp := newSpace(t, d, "delta", u)
	// Created but deliberately NOT queued — only the sweep can find it.
	page := newPage(t, d, sp, "Orphan", "## A\nnever explicitly queued for indexing")

	svc := NewServiceWithEmbedder(d, &fakeEmbedder{})
	svc.StartAutoReindex(ctx)

	if !waitForChunks(t, d, page, 2*time.Second) {
		t.Fatal("stale sweep did not recover the unindexed page")
	}
}

// --- resilience: one bad page does not abort the space -------------------

func TestReindexSpace_SkipsFailingPage(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	u := newUser(t, d, "erin")
	sp := newSpace(t, d, "echo", u)
	good1 := newPage(t, d, sp, "Good one", "## A\nperfectly fine content")
	newPage(t, d, sp, "Bad", "## B\nthis one will BOOM on embed")
	good2 := newPage(t, d, sp, "Good two", "## C\nalso fine content")

	svc := NewServiceWithEmbedder(d, &pickyEmbedder{})
	pages, _, failed, err := svc.reindexSpace(ctx, sp, false)
	if err != nil {
		t.Fatalf("reindexSpace returned infra error: %v", err)
	}
	if failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}
	if pages != 2 {
		t.Errorf("indexed pages = %d, want 2 (the two good ones)", pages)
	}
	// The good pages really got chunks despite the bad one in between.
	for _, id := range []int64{good1, good2} {
		var n int
		if err := d.QueryRow(`SELECT count(*) FROM page_chunks WHERE page_id=$1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Errorf("good page %d got no chunks", id)
		}
	}
}

// --- force re-embed bypasses the vector cache ----------------------------

func TestReindex_ForceBypassesCache(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	u := newUser(t, d, "frank")
	sp := newSpace(t, d, "foxtrot", u)
	newPage(t, d, sp, "Doc", "## A\nfirst chunk\n\n## B\nsecond chunk")

	emb := &fakeEmbedder{}
	svc := NewServiceWithEmbedder(d, emb)

	_, chunks, _, err := svc.reindexSpace(ctx, sp, false)
	if err != nil {
		t.Fatalf("initial: %v", err)
	}
	afterFirst := emb.calls
	if afterFirst != chunks || chunks == 0 {
		t.Fatalf("expected %d embed calls, got %d", chunks, afterFirst)
	}

	// Normal reindex reuses cached vectors — no new embed calls.
	if _, _, _, err := svc.reindexSpace(ctx, sp, false); err != nil {
		t.Fatalf("cached: %v", err)
	}
	if emb.calls != afterFirst {
		t.Errorf("non-force reindex re-embedded: calls %d → %d", afterFirst, emb.calls)
	}

	// Forced reindex re-embeds every chunk.
	if _, _, _, err := svc.reindexSpace(ctx, sp, true); err != nil {
		t.Fatalf("force: %v", err)
	}
	if emb.calls != afterFirst+chunks {
		t.Errorf("force reindex did not re-embed all chunks: calls %d, want %d", emb.calls, afterFirst+chunks)
	}
}

// --- index health + model drift ------------------------------------------

func TestIndexHealth_CountsAndModelDrift(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	u := newUser(t, d, "grace")
	sp := newSpace(t, d, "golf", u)
	newPage(t, d, sp, "Indexed", "## A\nreal content to chunk")
	newPage(t, d, sp, "Empty", "   ")

	// Index everything with model "old-model".
	old := NewServiceWithEmbedder(d, &namedEmbedder{model: "old-model"})
	if _, _, err := old.ReindexSpace(ctx, sp); err != nil {
		t.Fatalf("index: %v", err)
	}

	// A service on a DIFFERENT current model sees every stamped chunk as drift.
	cur := NewServiceWithEmbedder(d, &namedEmbedder{model: "new-model"})
	h, err := cur.IndexHealth(ctx)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if h.ContentPages != 1 {
		t.Errorf("content_pages = %d, want 1 (empty page excluded)", h.ContentPages)
	}
	if h.IndexedPages != 1 {
		t.Errorf("indexed_pages = %d, want 1", h.IndexedPages)
	}
	if h.StalePages != 0 {
		t.Errorf("stale_pages = %d, want 0", h.StalePages)
	}
	if h.Chunks == 0 || h.ModelDriftChunks != h.Chunks {
		t.Errorf("model_drift = %d, chunks = %d; want all chunks counted as drift", h.ModelDriftChunks, h.Chunks)
	}

	// Same model → no drift.
	same := NewServiceWithEmbedder(d, &namedEmbedder{model: "old-model"})
	h2, _ := same.IndexHealth(ctx)
	if h2.ModelDriftChunks != 0 {
		t.Errorf("same-model drift = %d, want 0", h2.ModelDriftChunks)
	}
}

// --- helpers --------------------------------------------------------------

// withFastWindows shrinks the auto-reindex timers so worker tests run in ms.
func withFastWindows(t *testing.T) {
	od, ot, orb, orm := reindexDebounce, reindexTick, reindexRetryBase, reindexRetryMax
	reindexDebounce, reindexTick = 10*time.Millisecond, 5*time.Millisecond
	reindexRetryBase, reindexRetryMax = 15*time.Millisecond, 60*time.Millisecond
	t.Cleanup(func() {
		reindexDebounce, reindexTick, reindexRetryBase, reindexRetryMax = od, ot, orb, orm
	})
}

// waitForChunks polls until the page has ≥1 EMBEDDED chunk or the deadline
// passes. The embedding filter is load-bearing: a reindex writes its chunk rows
// before it embeds them (so the work survives a failure), so a bare row count
// goes true the instant a reindex starts — including one that then fails — and
// every test built on it would pass without an embedder ever succeeding.
func waitForChunks(t *testing.T, d *sql.DB, page int64, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		var n int
		if err := d.QueryRow(
			`SELECT count(*) FROM page_chunks WHERE page_id=$1 AND embedding IS NOT NULL`, page).Scan(&n); err != nil {
			t.Fatalf("count chunks: %v", err)
		}
		if n > 0 {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
