package rag

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/zcag/tela/backend/internal/testdb"
)

// bigBody is a page long enough to chunk well past one embed batch. Every
// section is DISTINCT: identical chunks share a content hash, so a repeated body
// would collapse to one cached vector and hide per-chunk behaviour.
func bigBody() string {
	var b strings.Builder
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&b, "## Section %d\n\nStage %d of the ingest pipeline buffers events before the sink flushes them. ", i, i)
		for j := 0; j < 12; j++ {
			fmt.Fprintf(&b, "Latency budget %d-%d differs per stage. ", i, j)
		}
		b.WriteString("\n\n")
	}
	return b.String()
}

// TestReindexPage_BatchesEmbeds is the regression guard for the runaway that put
// a real page in a permanent reindex loop: ReindexPage embedded one chunk per
// HTTP request, so a big page needed more serial round trips than reindexTimeout
// allowed, timed out, wrote nothing, and started over — forever, never indexing
// once. The fix is that uncached chunks go upstream in batches, so what matters
// is REQUESTS per reindex, not chunks.
func TestReindexPage_BatchesEmbeds(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	owner := newUser(t, d, "batcher")
	spaceID := newSpace(t, d, "batch", owner)

	pageID := newPage(t, d, spaceID, "Big Reference", bigBody())

	bs := newBatchServer(t, false)
	bs.dims = 1024 // page_chunks.embedding is vector(1024)
	svc := NewServiceWithEmbedder(d, NewOpenAIEmbedder(bs.URL, "m", ""))

	n, err := svc.ReindexPage(ctx, pageID)
	if err != nil {
		t.Fatalf("ReindexPage: %v", err)
	}
	if n <= embedBatchSize {
		t.Fatalf("test page produced only %d chunks; need more than one batch (%d)", n, embedBatchSize)
	}

	wantReqs := (n + embedBatchSize - 1) / embedBatchSize
	if bs.requests != wantReqs {
		t.Fatalf("%d chunks cost %d upstream requests, want %d (one per batch of %d)",
			n, bs.requests, wantReqs, embedBatchSize)
	}
	for i, size := range bs.batchSize {
		if size > embedBatchSize {
			t.Fatalf("request %d carried %d inputs, over the %d cap", i, size, embedBatchSize)
		}
	}

	// Every chunk still gets its own row and its own vector — batching must not
	// drop or misalign anything.
	var rows, withVec int
	d.QueryRow(`SELECT count(*) FROM page_chunks WHERE page_id=$1`, pageID).Scan(&rows)
	d.QueryRow(`SELECT count(*) FROM page_chunks WHERE page_id=$1 AND embedding IS NOT NULL`, pageID).Scan(&withVec)
	if rows != n || withVec != n {
		t.Fatalf("rows=%d vec=%d want %d each", rows, withVec, n)
	}

	// A second reindex reuses cached vectors and must not hit the embedder at all.
	before := bs.requests
	if _, err := svc.ReindexPage(ctx, pageID); err != nil {
		t.Fatal(err)
	}
	if bs.requests != before {
		t.Fatalf("second reindex made %d requests; cache not reused", bs.requests-before)
	}
}

// TestReindexPage_ResumesAfterFailure is the root-cause guard. A reindex used to
// hold every vector in memory and write them only in a closing transaction, so a
// run that ran out of time persisted NOTHING and its successor re-embedded the
// whole page from scratch — a page too big for one window could never index, at
// any timeout, however many times it retried. Work must now survive a failure:
// the second attempt embeds only what the first one didn't reach.
func TestReindexPage_ResumesAfterFailure(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	owner := newUser(t, d, "resumer")
	spaceID := newSpace(t, d, "resume", owner)
	pageID := newPage(t, d, spaceID, "Big Reference", bigBody())

	// Die partway through, as a timeout would.
	emb := &countingEmbedder{failAfter: 3 * embedBatchSize}
	svc := NewServiceWithEmbedder(d, emb)
	if _, err := svc.ReindexPage(ctx, pageID); err == nil {
		t.Fatal("expected the reindex to fail partway")
	}

	var total, embedded int
	d.QueryRow(`SELECT count(*) FROM page_chunks WHERE page_id=$1`, pageID).Scan(&total)
	d.QueryRow(`SELECT count(*) FROM page_chunks WHERE page_id=$1 AND embedding IS NOT NULL`, pageID).Scan(&embedded)
	if total == 0 {
		t.Fatal("a failed reindex persisted nothing — every retry will start from zero")
	}
	if embedded == 0 || embedded >= total {
		t.Fatalf("want a partially embedded page, got %d/%d embedded", embedded, total)
	}

	// A page holding unembedded chunks must stay on the sweep's work list, or the
	// gap becomes permanent and silent.
	ids, err := svc.stalePageIDs(ctx, 50)
	if err != nil {
		t.Fatalf("stalePageIDs: %v", err)
	}
	queued := false
	for _, id := range ids {
		queued = queued || id == pageID
	}
	if !queued {
		t.Fatal("half-embedded page dropped off the sweep — it would never finish indexing")
	}

	// Second attempt: only the chunks the first one never reached.
	emb.failAfter = 0
	before := emb.embedded
	if _, err := svc.ReindexPage(ctx, pageID); err != nil {
		t.Fatalf("second ReindexPage: %v", err)
	}
	redone := emb.embedded - before
	if redone != total-embedded {
		t.Fatalf("resume embedded %d chunks, want exactly the %d left unfinished", redone, total-embedded)
	}
	d.QueryRow(`SELECT count(*) FROM page_chunks WHERE page_id=$1 AND embedding IS NOT NULL`, pageID).Scan(&embedded)
	if embedded != total {
		t.Fatalf("page still %d/%d embedded after resume", embedded, total)
	}
}

// TestReindexPage_ZeroChunkPageConverges covers the other way a page could never
// become fresh. A drawing-only page is a title plus an ```excalidraw fence; the
// indexer strips the fence, so it chunks to NOTHING — while the SQL staleness
// predicate saw the leftover heading and called it stale. With "indexed" inferred
// from chunk rows there was no way to record that the page HAD been indexed, so
// the sweep re-queued it every cycle forever. One page had looped since
// 2026-07-27, which kept stale_pages permanently above zero and hid a genuinely
// stuck page inside the count.
func TestReindexPage_ZeroChunkPageConverges(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	owner := newUser(t, d, "drawer")
	spaceID := newSpace(t, d, "draw", owner)
	pageID := newPage(t, d, spaceID, "Muhas",
		"# Muhas\n\n```excalidraw\n{\"elements\":[{\"id\":\"abc\",\"type\":\"rectangle\"}]}\n```\n")

	svc := NewServiceWithEmbedder(d, &fakeEmbedder{})
	n, err := svc.ReindexPage(ctx, pageID)
	if err != nil {
		t.Fatalf("ReindexPage: %v", err)
	}
	if n != 0 {
		t.Fatalf("a drawing-only page should chunk to nothing, got %d chunks", n)
	}

	// Indexed, therefore not stale — the sweep must stop re-queueing it.
	pages, err := svc.SpacePageFreshness(ctx, owner, spaceID)
	if err != nil {
		t.Fatalf("freshness: %v", err)
	}
	for _, p := range pages {
		if p.PageID == pageID && p.Status == "stale" {
			t.Fatal("a page that indexed to zero chunks still reads stale — it will be re-swept forever")
		}
	}
	ids, err := svc.stalePageIDs(ctx, 50)
	if err != nil {
		t.Fatalf("stalePageIDs: %v", err)
	}
	for _, id := range ids {
		if id == pageID {
			t.Fatal("zero-chunk page is still on the sweep's work list after indexing")
		}
	}

	// An edit after indexing must still make it stale — the stamp records when
	// the indexer ran, it doesn't excuse the page from ever being reindexed.
	if _, err := d.Exec(`UPDATE pages SET body = $1, updated_at = '2099-01-01 00:00:00' WHERE id = $2`,
		"# Muhas\n\nNow it has real prose worth embedding.\n", pageID); err != nil {
		t.Fatal(err)
	}
	ids, _ = svc.stalePageIDs(ctx, 50)
	found := false
	for _, id := range ids {
		found = found || id == pageID
	}
	if !found {
		t.Fatal("page edited after indexing is not stale — edits would never reindex")
	}
}

// countingEmbedder embeds normally but can be told to die after N chunks, the
// way a context deadline lands mid-page.
type countingEmbedder struct {
	fakeEmbedder
	embedded  int
	failAfter int // 0 = never fail
}

func (c *countingEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if c.failAfter > 0 && c.embedded >= c.failAfter {
		return nil, errors.New("context deadline exceeded")
	}
	c.embedded++
	return c.fakeEmbedder.Embed(ctx, text)
}

// TestStaleSweep_PreservesBackoff proves the stale sweep can no longer reset a
// failing page's retry backoff. It used to re-enqueue through QueueReindex,
// which clears the attempt counter and pulls the deadline forward — so a page
// that could never index retried every ~30s indefinitely instead of decaying to
// reindexRetryMax, and hammered the shared embedder around the clock.
func TestStaleSweep_PreservesBackoff(t *testing.T) {
	svc := NewServiceWithEmbedder(nil, &fakeEmbedder{})
	svc.pending = map[int64]time.Time{}
	svc.attempts = map[int64]int{}

	cause := errors.New("context deadline exceeded")
	for i := 0; i < 6; i++ {
		svc.requeueAfterFailure(7, 0, cause)
	}
	deadline, attempts := svc.pending[7], svc.attempts[7]
	if attempts != 6 {
		t.Fatalf("attempts=%d want 6", attempts)
	}
	if until := time.Until(deadline); until < reindexRetryMax/2 {
		t.Fatalf("backoff only %v after 6 failures; want it climbing toward %v", until, reindexRetryMax)
	}

	svc.queueReindexStale(7)
	if svc.attempts[7] != attempts {
		t.Fatalf("sweep reset attempts to %d (was %d)", svc.attempts[7], attempts)
	}
	if !svc.pending[7].Equal(deadline) {
		t.Fatalf("sweep pulled the retry deadline from %v to %v", deadline, svc.pending[7])
	}

	// A real edit still clears the backoff — new content deserves a prompt try.
	svc.QueueReindex(7)
	if svc.attempts[7] != 0 {
		t.Fatalf("a fresh edit must clear the retry counter, got %d", svc.attempts[7])
	}
	if time.Until(svc.pending[7]) > reindexDebounce {
		t.Fatalf("a fresh edit must reindex after the debounce, got %v", time.Until(svc.pending[7]))
	}

	// A page the sweep finds stale with no failure history is queued normally.
	svc.queueReindexStale(9)
	if _, ok := svc.pending[9]; !ok {
		t.Fatal("sweep should enqueue a stale page that isn't already queued")
	}

	// A run that failed but DID embed some chunks left them committed, so the
	// next attempt has less to do — that's progress, not a failure to back off.
	for i := 0; i < 4; i++ {
		svc.requeueAfterFailure(11, 0, cause)
	}
	svc.requeueAfterFailure(11, 64, cause)
	if svc.attempts[11] != 0 {
		t.Fatalf("a progressing run must clear the failure count, got %d", svc.attempts[11])
	}
	if until := time.Until(svc.pending[11]); until > reindexRetryBase {
		t.Fatalf("a progressing run retries in %v; want ≤%v", until, reindexRetryBase)
	}
}
