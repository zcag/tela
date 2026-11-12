package summarize

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zcag/tela/backend/internal/testdb"
)

func newSpaceFile(t *testing.T, d *sql.DB, spaceID int64, name, mime string, data []byte) int64 {
	t.Helper()
	var id int64
	if err := d.QueryRow(`INSERT INTO space_files
		(space_id, parent_page_id, name, content_hash, mime, data, byte_size)
		VALUES ($1, NULL, $2, $3, $4, $5, $6) RETURNING id`,
		spaceID, name, name+"hash", mime, data, len(data)).Scan(&id); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	return id
}

func fileSummary(t *testing.T, d *sql.DB, id int64) string {
	t.Helper()
	var s string
	if err := d.QueryRow(`SELECT summary FROM space_files WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatalf("read file summary: %v", err)
	}
	return s
}

func TestSummarizeFile_GeneratesAndIsIdempotent(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	u := newUser(t, d, "alice")
	sp := newSpace(t, d, "alpha", u)
	fileID := newSpaceFile(t, d, sp, "notes.md", "text/markdown",
		[]byte("# Vendor MSA\n\nThe agreement caps liability at two million dollars."))

	fake := &fakeLLM{out: "  \"A vendor MSA capping liability at $2M.\"  "}
	svc := newSvc(d, fake)

	res, err := svc.SummarizeFile(ctx, fileID, false)
	if err != nil {
		t.Fatalf("SummarizeFile: %v", err)
	}
	if res != Generated {
		t.Fatalf("result = %q, want generated", res)
	}
	if got := fileSummary(t, d, fileID); got != "A vendor MSA capping liability at $2M." {
		t.Errorf("summary = %q (quotes/whitespace not sanitized?)", got)
	}

	// Idempotent: a second pass with unchanged content skips the LLM.
	before := fake.callCount()
	if res, err := svc.SummarizeFile(ctx, fileID, false); err != nil || res != SkippedFresh {
		t.Fatalf("second pass res=%q err=%v, want fresh,nil", res, err)
	}
	if fake.callCount() != before {
		t.Errorf("fresh file re-called the LLM (%d new calls)", fake.callCount()-before)
	}
}

func TestSummarizeFile_NonTextRecordsEmptyAndDoesntRetry(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	u := newUser(t, d, "bob")
	sp := newSpace(t, d, "bravo", u)
	imgID := newSpaceFile(t, d, sp, "pic.png", "image/png", []byte("\x89PNG not text"))

	fake := &fakeLLM{out: "should never be used"}
	svc := newSvc(d, fake)

	res, err := svc.SummarizeFile(ctx, imgID, false)
	if err != nil || res != SkippedEmpty {
		t.Fatalf("non-text res=%q err=%v, want empty,nil", res, err)
	}
	if fake.callCount() != 0 {
		t.Errorf("non-text file called the LLM %d times", fake.callCount())
	}
	if got := fileSummary(t, d, imgID); got != "" {
		t.Errorf("non-text summary = %q, want empty", got)
	}
	// A sentinel row was recorded so the stale sweep won't re-queue it.
	stale, err := svc.staleFileIDs(ctx, 100)
	if err != nil {
		t.Fatalf("staleFileIDs: %v", err)
	}
	for _, f := range stale {
		if f.id == imgID {
			t.Errorf("non-text file still appears stale (would re-queue forever)")
		}
	}
}

func TestSummarizeFile_TimeoutRecordsFailure(t *testing.T) {
	d := testdb.New(t)
	u := newUser(t, d, "alice")
	sp := newSpace(t, d, "eng", u)
	file := newSpaceFile(t, d, sp, "notes.md", "text/markdown", []byte("# Notes\n\nA document the model never finishes."))
	svc := newSvc(d, &fakeLLM{hang: true})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := svc.SummarizeFile(ctx, file, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	var lastErr string
	var attempts int
	if err := d.QueryRow(`SELECT last_error, attempts FROM file_summaries WHERE space_file_id = $1`, file).Scan(&lastErr, &attempts); err != nil {
		t.Fatalf("no failure row: %v", err)
	}
	if attempts != 1 || !strings.Contains(lastErr, "deadline exceeded") {
		t.Errorf("last_error=%q attempts=%d, want the deadline recorded once", lastErr, attempts)
	}
}

func TestSummarizeFile_ContentChangeRegenerates(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	u := newUser(t, d, "carol")
	sp := newSpace(t, d, "charlie", u)
	fileID := newSpaceFile(t, d, sp, "doc.txt", "text/plain", []byte("first version of the document"))

	svc := newSvc(d, &fakeLLM{out: "Summary v1."})
	if _, err := svc.SummarizeFile(ctx, fileID, false); err != nil {
		t.Fatal(err)
	}

	// Simulate a re-upload: new bytes → new content_hash.
	if _, err := d.Exec(`UPDATE space_files SET content_hash = 'changed', data = $2 WHERE id = $1`,
		fileID, []byte("second, very different version")); err != nil {
		t.Fatal(err)
	}
	stale, _ := svc.staleFileIDs(ctx, 100)
	found := false
	for _, f := range stale {
		if f.id == fileID {
			found = true
		}
	}
	if !found {
		t.Fatal("content change did not mark the file stale")
	}
	svc2 := newSvc(d, &fakeLLM{out: "Summary v2."})
	if _, err := svc2.SummarizeFile(ctx, fileID, false); err != nil {
		t.Fatal(err)
	}
	if got := fileSummary(t, d, fileID); got != "Summary v2." {
		t.Errorf("summary = %q, want regenerated v2", got)
	}
}

func TestWorker_QueueFileGenerates(t *testing.T) {
	origDebounce, origTick := summarizeDebounce, summarizeTick
	summarizeDebounce, summarizeTick = 15*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { summarizeDebounce, summarizeTick = origDebounce, origTick })

	d := testdb.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	u := newUser(t, d, "dave")
	sp := newSpace(t, d, "delta", u)
	fileID := newSpaceFile(t, d, sp, "auto.md", "text/markdown", []byte("# Auto\n\nIndex me and summarize me."))

	svc := newSvc(d, &fakeLLM{out: "Auto file summary."})
	svc.Start(ctx)
	svc.QueueFile(fileID)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fileSummary(t, d, fileID) == "Auto file summary." {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("worker did not summarize file %d (summary=%q)", fileID, fileSummary(t, d, fileID))
}

func TestFileSweep_BacksOffGivesUpAndChangeResets(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	u := newUser(t, d, "frank")
	sp := newSpace(t, d, "foxtrot", u)
	fileID := newSpaceFile(t, d, sp, "doc.md", "text/markdown", []byte("# Doc\n\nThe model keeps failing on this."))
	svc := newSvc(d, &fakeLLM{failures: 100})
	sweptFile := func() bool {
		files, err := svc.staleFileIDs(ctx, 1000)
		if err != nil {
			t.Fatal(err)
		}
		return slices.ContainsFunc(files, func(f fileRef) bool { return f.id == fileID })
	}
	age := func(ago time.Duration) {
		if _, err := d.Exec(`UPDATE file_summaries SET failed_at = $2 WHERE space_file_id = $1`,
			fileID, time.Now().UTC().Add(-ago).Format(time.DateTime)); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := svc.SummarizeFile(ctx, fileID, false); err == nil {
		t.Fatal("expected failure")
	}
	if sweptFile() {
		t.Error("a just-failed file was swept before its backoff")
	}
	age(time.Hour)
	if !sweptFile() {
		t.Error("not swept after its backoff elapsed")
	}
	for i := 2; i <= summarizeMaxAttempts; i++ {
		if _, err := svc.SummarizeFile(ctx, fileID, false); err == nil {
			t.Fatal("expected failure")
		}
	}
	age(24 * time.Hour)
	if sweptFile() {
		t.Error("swept after reaching the attempt limit")
	}
	if _, err := d.Exec(`UPDATE space_files SET content_hash = 'changed' WHERE id = $1`, fileID); err != nil {
		t.Fatal(err)
	}
	if !sweptFile() {
		t.Error("new content did not make a given-up file eligible again")
	}
}
