package summarize

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zcag/tela/backend/internal/llm"
	"github.com/zcag/tela/backend/internal/testdb"
)

// fakeLLM is a canned llm.Completer: returns out (after failing the first
// `failures` calls) and counts invocations so tests can assert the hash-skip.
type fakeLLM struct {
	mu       sync.Mutex
	out      string
	failures int
	calls    int
	hang     bool
}

func (f *fakeLLM) Model() string { return "fake-llm" }
func (f *fakeLLM) Complete(ctx context.Context, _, _ string) (string, error) {
	f.mu.Lock()
	f.calls++
	hang := f.hang
	f.mu.Unlock()
	if hang {
		<-ctx.Done()
		return "", ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failures > 0 {
		f.failures--
		return "", errors.New("llm boom")
	}
	return f.out, nil
}

func (f *fakeLLM) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func newSvc(d *sql.DB, fake *fakeLLM) *Service {
	return NewService(d, llm.NewServiceWithCompleter(fake))
}

func newUser(t *testing.T, d *sql.DB, name string) int64 {
	t.Helper()
	var id int64
	if err := d.QueryRow(
		`INSERT INTO users (username, password_hash) VALUES ($1, 'x') RETURNING id`, name,
	).Scan(&id); err != nil {
		t.Fatalf("insert user %s: %v", name, err)
	}
	return id
}

func newSpace(t *testing.T, d *sql.DB, slug string, owner int64) int64 {
	t.Helper()
	var id int64
	if err := d.QueryRow(
		`INSERT INTO spaces (name, slug) VALUES ($1, $1) RETURNING id`, slug,
	).Scan(&id); err != nil {
		t.Fatalf("insert space %s: %v", slug, err)
	}
	if _, err := d.Exec(
		`INSERT INTO space_members (space_id, user_id, role) VALUES ($1, $2, 'owner')`, id, owner,
	); err != nil {
		t.Fatalf("add member: %v", err)
	}
	return id
}

func newPage(t *testing.T, d *sql.DB, spaceID int64, title, body string) int64 {
	t.Helper()
	var id int64
	if err := d.QueryRow(
		`INSERT INTO pages (space_id, title, body) VALUES ($1, $2, $3) RETURNING id`, spaceID, title, body,
	).Scan(&id); err != nil {
		t.Fatalf("insert page %q: %v", title, err)
	}
	return id
}

func pageSummary(t *testing.T, d *sql.DB, id int64) string {
	t.Helper()
	var s string
	if err := d.QueryRow(`SELECT coalesce(props->>'summary', '') FROM pages WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatalf("read summary: %v", err)
	}
	return s
}

func TestSummarizePage_GeneratesWithoutTouchingPage(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	u := newUser(t, d, "alice")
	sp := newSpace(t, d, "alpha", u)
	page := newPage(t, d, sp, "Deploys", "## Shipping\nrun make deploy to push a release")

	// Pin updated_at to a distinctive past value so a same-second tela_now()
	// rewrite can't mask an accidental bump.
	const updatedBefore = "2000-01-01 00:00:00"
	if _, err := d.Exec(`UPDATE pages SET updated_at = $1 WHERE id = $2`, updatedBefore, page); err != nil {
		t.Fatalf("pin updated_at: %v", err)
	}

	fake := &fakeLLM{out: "  “Releases ship via make deploy.”  "}
	svc := newSvc(d, fake)
	res, err := svc.SummarizePage(ctx, page, false)
	if err != nil || res != Generated {
		t.Fatalf("summarize: res=%q err=%v, want generated", res, err)
	}

	// Summary landed in props, sanitized (single line, wrapping quotes stripped).
	if got := pageSummary(t, d, page); got != "Releases ship via make deploy." {
		t.Errorf("props.summary = %q", got)
	}
	// Bookkeeping row: hash of the live body, model, clean failure state.
	var hash, model, genAt, lastErr string
	var attempts int
	if err := d.QueryRow(`SELECT src_hash, model, generated_at, last_error, attempts FROM page_summaries WHERE page_id = $1`, page).
		Scan(&hash, &model, &genAt, &lastErr, &attempts); err != nil {
		t.Fatalf("read page_summaries: %v", err)
	}
	var body string
	_ = d.QueryRow(`SELECT body FROM pages WHERE id = $1`, page).Scan(&body)
	if hash != srcHash(body) || model != "fake-llm" || genAt == "" || lastErr != "" || attempts != 0 {
		t.Errorf("row = hash-match:%v model:%q gen:%q err:%q attempts:%d", hash == srcHash(body), model, genAt, lastErr, attempts)
	}

	// The write must NOT look like a user edit: updated_at untouched, no revision.
	var updatedAfter string
	_ = d.QueryRow(`SELECT updated_at FROM pages WHERE id = $1`, page).Scan(&updatedAfter)
	if updatedAfter != updatedBefore {
		t.Errorf("updated_at bumped by summary write: %q → %q", updatedBefore, updatedAfter)
	}
	var revs int
	_ = d.QueryRow(`SELECT count(*) FROM page_revisions WHERE page_id = $1`, page).Scan(&revs)
	if revs != 0 {
		t.Errorf("summary write created %d revision(s), want 0", revs)
	}

	// Re-run: hash matches + summary present → skipped, no second LLM call.
	res, err = svc.SummarizePage(ctx, page, false)
	if err != nil || res != SkippedFresh {
		t.Fatalf("re-run: res=%q err=%v, want fresh skip", res, err)
	}
	if fake.callCount() != 1 {
		t.Errorf("llm calls = %d, want 1 (hash-skip)", fake.callCount())
	}
	// --force bypasses the skip.
	if res, err = svc.SummarizePage(ctx, page, true); err != nil || res != Generated {
		t.Fatalf("force: res=%q err=%v", res, err)
	}
}

func TestSummarizePage_LockAndEmptySkipped(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	u := newUser(t, d, "alice")
	sp := newSpace(t, d, "alpha", u)
	locked := newPage(t, d, sp, "Locked", "real content the llm must not touch")
	if _, err := d.Exec(`UPDATE pages SET props = '{"summary_lock": true, "summary": "hand-written"}' WHERE id = $1`, locked); err != nil {
		t.Fatalf("lock: %v", err)
	}
	empty := newPage(t, d, sp, "Empty", "   \n")

	fake := &fakeLLM{out: "machine summary"}
	svc := newSvc(d, fake)
	if res, err := svc.SummarizePage(ctx, locked, true); err != nil || res != SkippedLocked {
		t.Fatalf("locked: res=%q err=%v", res, err)
	}
	if res, err := svc.SummarizePage(ctx, empty, true); err != nil || res != SkippedEmpty {
		t.Fatalf("empty: res=%q err=%v", res, err)
	}
	if fake.callCount() != 0 {
		t.Errorf("llm called %d times for skipped pages", fake.callCount())
	}
	if got := pageSummary(t, d, locked); got != "hand-written" {
		t.Errorf("locked summary overwritten: %q", got)
	}
	// Skips record nothing.
	var n int
	_ = d.QueryRow(`SELECT count(*) FROM page_summaries`).Scan(&n)
	if n != 0 {
		t.Errorf("page_summaries rows = %d, want 0", n)
	}
}

// A model that abstains (NONE) on a body with nothing to faithfully summarize
// must not persist "NONE": it clears any stale summary, records the row fresh
// (so the same body isn't retried), and doesn't count as a failure.
func TestSummarizePage_NoneClearsAndRecordsFresh(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	u := newUser(t, d, "alice")
	sp := newSpace(t, d, "alpha", u)
	// A diagram-only body (non-prose) plus a pre-existing stale/wrong summary.
	page := newPage(t, d, sp, "Gulden - Cagdas", "```excalidraw\n{\"elements\":[]}\n```")
	if _, err := d.Exec(`UPDATE pages SET props = '{"summary":"Gulden is a financial institution"}' WHERE id = $1`, page); err != nil {
		t.Fatalf("seed stale summary: %v", err)
	}

	fake := &fakeLLM{out: "  NONE.  "} // sanitize-then-isNone must still match
	svc := newSvc(d, fake)
	if res, err := svc.SummarizePage(ctx, page, false); err != nil || res != SkippedNoneBody {
		t.Fatalf("none: res=%q err=%v, want no_content", res, err)
	}
	// The stale summary is gone — not replaced with the literal "NONE".
	if got := pageSummary(t, d, page); got != "" {
		t.Errorf("summary after NONE = %q, want empty", got)
	}
	// Row recorded fresh (hash of body, clean error state) so it reads done.
	var hash, lastErr string
	var attempts int
	if err := d.QueryRow(`SELECT src_hash, last_error, attempts FROM page_summaries WHERE page_id = $1`, page).
		Scan(&hash, &lastErr, &attempts); err != nil {
		t.Fatalf("read page_summaries: %v", err)
	}
	var body string
	_ = d.QueryRow(`SELECT body FROM pages WHERE id = $1`, page).Scan(&body)
	if hash != srcHash(body) || lastErr != "" || attempts != 0 {
		t.Errorf("row = hash-match:%v err:%q attempts:%d, want fresh", hash == srcHash(body), lastErr, attempts)
	}
	// Re-run on the unchanged body makes no second LLM call (recorded fresh).
	if res, err := svc.SummarizePage(ctx, page, false); err != nil || res != SkippedFresh {
		t.Fatalf("re-run: res=%q err=%v, want fresh skip", res, err)
	}
	if fake.callCount() != 1 {
		t.Errorf("llm calls = %d, want 1", fake.callCount())
	}
	// The status endpoint must agree it's done: an abstained page reads fresh,
	// not stale, so the staleness dot clears. Regression guard — an empty
	// props.summary alone used to force 'stale' here (and re-queue) forever.
	if st := pageStatus(t, svc, u, sp, page); st != "fresh" {
		t.Errorf("status after NONE = %q, want fresh", st)
	}
}

func TestSummarizePage_BodyChangeGoesStaleThenRegenerates(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	u := newUser(t, d, "alice")
	sp := newSpace(t, d, "alpha", u)
	page := newPage(t, d, sp, "Doc", "version one of the body")

	fake := &fakeLLM{out: "Summary of version one."}
	svc := newSvc(d, fake)
	if _, err := svc.SummarizePage(ctx, page, false); err != nil {
		t.Fatalf("first generation: %v", err)
	}

	if _, err := d.Exec(`UPDATE pages SET body = 'version two, rather different' WHERE id = $1`, page); err != nil {
		t.Fatalf("edit body: %v", err)
	}
	if st := pageStatus(t, svc, u, sp, page); st != "stale" {
		t.Fatalf("status after edit = %q, want stale", st)
	}

	fake.mu.Lock()
	fake.out = "Summary of version two."
	fake.mu.Unlock()
	if res, err := svc.SummarizePage(ctx, page, false); err != nil || res != Generated {
		t.Fatalf("regenerate: res=%q err=%v", res, err)
	}
	if got := pageSummary(t, d, page); got != "Summary of version two." {
		t.Errorf("summary = %q", got)
	}
	if st := pageStatus(t, svc, u, sp, page); st != "fresh" {
		t.Errorf("status after regenerate = %q, want fresh", st)
	}
}

func TestSummarizePage_TimeoutRecordsFailure(t *testing.T) {
	d := testdb.New(t)
	u := newUser(t, d, "alice")
	sp := newSpace(t, d, "eng", u)
	page := newPage(t, d, sp, "Slow", "A body the model never finishes.")
	svc := newSvc(d, &fakeLLM{hang: true})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := svc.SummarizePage(ctx, page, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	var lastErr string
	var attempts int
	if err := d.QueryRow(`SELECT last_error, attempts FROM page_summaries WHERE page_id = $1`, page).Scan(&lastErr, &attempts); err != nil {
		t.Fatalf("no failure row: %v", err)
	}
	if attempts != 1 || !strings.Contains(lastErr, "deadline exceeded") {
		t.Errorf("last_error=%q attempts=%d, want the deadline recorded once", lastErr, attempts)
	}
}

func TestSummarizePage_FailureRecordedThenRecovers(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	u := newUser(t, d, "alice")
	sp := newSpace(t, d, "alpha", u)
	page := newPage(t, d, sp, "Doc", "content the llm will choke on, twice")

	fake := &fakeLLM{out: "Eventually fine.", failures: 2}
	svc := newSvc(d, fake)

	for want := 1; want <= 2; want++ {
		if _, err := svc.SummarizePage(ctx, page, false); err == nil {
			t.Fatalf("attempt %d: expected error", want)
		}
		var lastErr string
		var attempts int
		if err := d.QueryRow(`SELECT last_error, attempts FROM page_summaries WHERE page_id = $1`, page).Scan(&lastErr, &attempts); err != nil {
			t.Fatalf("read failure row: %v", err)
		}
		if !strings.Contains(lastErr, "llm boom") || attempts != want {
			t.Fatalf("attempt %d: last_error=%q attempts=%d", want, lastErr, attempts)
		}
		if st := pageStatus(t, svc, u, sp, page); st != "failed" {
			t.Fatalf("attempt %d: status = %q, want failed", want, st)
		}
	}

	// A failed page stays eligible (the fresh-skip must not mask it).
	if res, err := svc.SummarizePage(ctx, page, false); err != nil || res != Generated {
		t.Fatalf("recovery: res=%q err=%v", res, err)
	}
	var lastErr string
	var attempts int
	_ = d.QueryRow(`SELECT last_error, attempts FROM page_summaries WHERE page_id = $1`, page).Scan(&lastErr, &attempts)
	if lastErr != "" || attempts != 0 {
		t.Errorf("failure state not cleared: err=%q attempts=%d", lastErr, attempts)
	}
}

func pageStatus(t *testing.T, svc *Service, userID, spaceID, pageID int64) string {
	t.Helper()
	pages, err := svc.SpacePageSummaries(context.Background(), userID, spaceID)
	if err != nil {
		t.Fatalf("page summaries: %v", err)
	}
	for _, p := range pages {
		if p.PageID == pageID {
			return p.Status
		}
	}
	t.Fatalf("page %d not in space %d listing", pageID, spaceID)
	return ""
}

func TestStatus_RollupAndPerPage(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	alice := newUser(t, d, "alice")
	bob := newUser(t, d, "bob")
	sp := newSpace(t, d, "alpha", alice)
	bobSpace := newSpace(t, d, "bravo", bob)

	fresh := newPage(t, d, sp, "Fresh", "summarized and untouched since")
	stale := newPage(t, d, sp, "Stale", "summarized, then edited")
	missing := newPage(t, d, sp, "Missing", "never summarized, no record")
	failed := newPage(t, d, sp, "Failed", "generation errored here")
	locked := newPage(t, d, sp, "Locked", "author-owned summary")
	empty := newPage(t, d, sp, "Empty", "  ")
	if _, err := d.Exec(`UPDATE pages SET props = '{"summary_lock": true}' WHERE id = $1`, locked); err != nil {
		t.Fatalf("lock: %v", err)
	}

	svc := newSvc(d, &fakeLLM{out: "A summary."})
	for _, id := range []int64{fresh, stale} {
		if _, err := svc.SummarizePage(ctx, id, false); err != nil {
			t.Fatalf("seed page %d: %v", id, err)
		}
	}
	if _, err := d.Exec(`UPDATE pages SET body = 'edited after generation' WHERE id = $1`, stale); err != nil {
		t.Fatalf("edit stale: %v", err)
	}
	if _, err := newSvc(d, &fakeLLM{failures: 99}).SummarizePage(ctx, failed, false); err == nil {
		t.Fatal("expected failure")
	}

	// Per-page statuses, including the missing-vs-stale distinction.
	got := map[int64]string{}
	pages, err := svc.SpacePageSummaries(ctx, alice, sp)
	if err != nil {
		t.Fatalf("page summaries: %v", err)
	}
	for _, p := range pages {
		got[p.PageID] = p.Status
	}
	want := map[int64]string{
		fresh: "fresh", stale: "stale", missing: "missing", failed: "failed", locked: "locked", empty: "empty",
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("page %d status = %q, want %q", id, got[id], w)
		}
	}
	// Contract details: generated_at/model "" when never generated; last_error
	// only on failed rows.
	for _, p := range pages {
		switch p.PageID {
		case fresh:
			if p.GeneratedAt == "" || p.Model != "fake-llm" || p.LastError != "" {
				t.Errorf("fresh row: %+v", p)
			}
		case missing, failed:
			if p.GeneratedAt != "" || p.Model != "" {
				t.Errorf("never-generated row leaks provenance: %+v", p)
			}
			if p.PageID == failed && p.LastError == "" {
				t.Errorf("failed row missing last_error: %+v", p)
			}
		}
		if p.UpdatedAt == "" {
			t.Errorf("page %d missing updated_at", p.PageID)
		}
	}

	// Space rollup: counts over non-empty pages; missing folds into stale.
	spaces, err := svc.SpaceRollup(ctx, alice)
	if err != nil {
		t.Fatalf("rollup: %v", err)
	}
	if len(spaces) != 1 {
		t.Fatalf("alice sees %d spaces, want 1 (bob's must not leak)", len(spaces))
	}
	f := spaces[0]
	if f.SpaceID != sp || f.Pages != 5 || f.Summarized != 2 || f.Stale != 2 || f.Failed != 1 {
		t.Errorf("rollup = %+v, want pages=5 summarized=2 stale=2 failed=1", f)
	}
	if f.LastGenerated == "" {
		t.Error("last_generated empty after generations")
	}

	// Access scoping mirrors freshness: bob's space per-page is empty for alice.
	leak, err := svc.SpacePageSummaries(ctx, alice, bobSpace)
	if err != nil {
		t.Fatalf("cross-space query: %v", err)
	}
	if len(leak) != 0 {
		t.Fatalf("LEAK: alice got %d pages of bob's space", len(leak))
	}

	// QueueStaleSpace targets exactly the needs-work set (stale+missing+failed).
	svc.pending = map[int64]time.Time{} // arm the queue without starting goroutines
	svc.attempts = map[int64]int{}
	n, err := svc.QueueStaleSpace(ctx, sp)
	if err != nil {
		t.Fatalf("queue stale: %v", err)
	}
	if n != 3 || len(svc.pending) != 3 {
		t.Errorf("queued %d (pending %d), want 3", n, len(svc.pending))
	}
}

func TestWorker_DebouncedGenerationAndRetry(t *testing.T) {
	// Shrink the windows so the worker fires in milliseconds, not seconds.
	origDebounce, origTick, origRetry := summarizeDebounce, summarizeTick, summarizeRetryBase
	summarizeDebounce, summarizeTick, summarizeRetryBase = 15*time.Millisecond, 5*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { summarizeDebounce, summarizeTick, summarizeRetryBase = origDebounce, origTick, origRetry })

	d := testdb.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	u := newUser(t, d, "carol")
	sp := newSpace(t, d, "charlie", u)
	page := newPage(t, d, sp, "Doc", "## A\ncontent to be auto-summarized")

	// First attempt fails → the worker must back off and retry to success.
	svc := newSvc(d, &fakeLLM{out: "Auto-generated summary.", failures: 1})
	svc.Start(ctx)
	svc.Queue(page)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pageSummary(t, d, page) == "Auto-generated summary." {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("worker did not produce a summary for page %d within deadline (summary=%q)",
		page, pageSummary(t, d, page))
}
