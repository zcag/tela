package summarize

import (
	"context"
	"log/slog"
	"time"

	"github.com/zcag/tela/backend/internal/extract"
)

// Auto-summarize: a debounced, coalescing background worker that keeps
// props.summary fresh as pages are written, WITHOUT blocking the write path
// (generation is a network op to the LLM). Handlers call Queue(pageID) after a
// committed write — the same sites that call rag.QueueReindex — and the worker
// summarizes each page once its edits settle, so an editing burst collapses to
// one generation.
//
// Self-healing by design: a failed generation is recorded in page_summaries (so
// the status view reads failed), and an independent stale sweep periodically
// re-queues pages whose summary is missing, out of date, or failed and due a
// retry (retryDueExpr), recovering both LLM outages and edits made while the
// process was down. Retries live in the database, not in this queue, so a
// restart neither loses nor resets them.

// Tunable (var, not const) so tests can shrink the windows. Production values:
var (
	// summarizeDebounce is how long after the last edit to a page we wait before
	// summarizing it — long enough to coalesce an active editing burst.
	summarizeDebounce = 5 * time.Second
	// summarizeTick is how often the worker checks for pages whose debounce elapsed.
	summarizeTick = 1 * time.Second
	// summarizeTimeout caps a single page's generation round-trip.
	summarizeTimeout     = 2 * time.Minute
	recordFailureTimeout = 10 * time.Second
	// summarizeRetryBase / summarizeRetryMax / summarizeMaxAttempts are the stale
	// sweep's retry policy for a failed summary (retryDueExpr): wait
	// base * 2^attempts (capped at max) after the failure, and stop after
	// summarizeMaxAttempts failures of the same source until it changes.
	summarizeRetryBase   = 30 * time.Second
	summarizeRetryMax    = 6 * time.Hour
	summarizeMaxAttempts = 5
	// staleSweepInterval is how often the background sweep re-queues stale/missing
	// summaries. staleSweepInitialDelay defers the first sweep past boot;
	// staleSweepBatch caps how many pages one sweep enqueues.
	staleSweepInterval     = 5 * time.Minute
	staleSweepInitialDelay = 30 * time.Second
	staleSweepBatch        = 500
)

// Queue schedules pageID to be summarized after the debounce window. Safe to
// call on every write; repeated calls for the same page coalesce and push the
// deadline forward. No-op when the LLM is unconfigured or the worker isn't running.
func (s *Service) Queue(pageID int64) {
	if !s.Enabled() {
		return
	}
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	if s.pending == nil {
		return // worker not started
	}
	s.pending[pageID] = time.Now().Add(summarizeDebounce)
}

// Start launches the background summarize worker plus the stale-sweep loop.
// Idempotent; no-op when disabled. Both stop when ctx is cancelled. Call once
// from api.New.
func (s *Service) Start(ctx context.Context) {
	if !s.Enabled() {
		return
	}
	s.queueMu.Lock()
	if s.pending != nil {
		s.queueMu.Unlock()
		return // already started
	}
	s.pending = make(map[int64]time.Time)
	s.pendingFiles = make(map[int64]time.Time)
	s.queueMu.Unlock()
	go s.summarizeLoop(ctx)
	go s.staleSweepLoop(ctx)
}

// QueueFile is Queue for a space_file (the file half of auto-summary) — the
// trigger every upload path fires after a content change, alongside
// rag.QueueReindexFile. Same debounce/coalesce machinery, keyed on file id.
func (s *Service) QueueFile(fileID int64) {
	if !s.Enabled() {
		return
	}
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	if s.pendingFiles == nil {
		return // worker not started
	}
	s.pendingFiles[fileID] = time.Now().Add(summarizeDebounce)
}

func (s *Service) summarizeLoop(ctx context.Context) {
	t := time.NewTicker(summarizeTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if s.isPaused() {
				continue // AI kill-switch on — leave the queue intact, resume later
			}
			for _, id := range s.dueSummaries() {
				sctx, cancel := context.WithTimeout(ctx, summarizeTimeout)
				if _, err := s.SummarizePage(sctx, id, false); err != nil {
					slog.Warn("summarize: generation failed", "page_id", id, "err", err)
				}
				cancel()
			}
			for _, id := range s.dueFileSummaries() {
				sctx, cancel := context.WithTimeout(ctx, summarizeTimeout)
				if _, err := s.SummarizeFile(sctx, id, false); err != nil {
					slog.Warn("summarize: file generation failed", "file_id", id, "err", err)
				}
				cancel()
			}
		}
	}
}

// dueFileSummaries is dueSummaries for the file queue.
func (s *Service) dueFileSummaries() []int64 {
	now := time.Now()
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	var due []int64
	for id, deadline := range s.pendingFiles {
		if !now.Before(deadline) {
			due = append(due, id)
			delete(s.pendingFiles, id)
		}
	}
	return due
}

// dueSummaries removes and returns the page IDs whose debounce window has
// elapsed. Pages still settling (or backing off after a failure) stay queued.
func (s *Service) dueSummaries() []int64 {
	now := time.Now()
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	var due []int64
	for id, deadline := range s.pending {
		if !now.Before(deadline) {
			due = append(due, id)
			delete(s.pending, id)
		}
	}
	return due
}

// staleSweepLoop periodically re-queues every page whose summary is missing or
// out of date — the safety net that heals a backlog after an LLM outage or a
// process restart (which loses the in-memory queue but not the corpus). Also
// logs a coverage summary each cycle so freshness is observable in the logs.
func (s *Service) staleSweepLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(staleSweepInitialDelay):
	}
	t := time.NewTicker(staleSweepInterval)
	defer t.Stop()
	for {
		s.sweepStale(ctx)
		s.sweepStaleFiles(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// sweepStale enqueues up to staleSweepBatch stale/missing pages and logs a
// health line. Best-effort: query errors are logged, never fatal.
func (s *Service) sweepStale(ctx context.Context) {
	if s.isPaused() {
		return // AI kill-switch on — don't backfill while the LLM is paused
	}
	h, err := s.SummaryHealth(ctx)
	if err != nil {
		slog.Error("summarize: health query", "err", err)
		return
	}
	slog.Info("summarize: coverage",
		"content_pages", h.ContentPages, "summarized", h.Summarized, "stale", h.Stale, "failed", h.Failed)
	if h.Stale == 0 && h.Failed == 0 {
		return
	}
	ids, err := s.stalePageIDs(ctx, staleSweepBatch)
	if err != nil {
		slog.Error("summarize: stale-sweep query", "err", err)
		return
	}
	for _, id := range ids {
		s.Queue(id)
	}
	slog.Info("summarize: stale-sweep enqueued", "pages", len(ids))
}

// sweepStaleFiles is the file half of the stale sweep: enqueue text-extractable
// attachments whose summary is missing, failed, or generated from older content
// — the back-fill that summarizes files uploaded before the feature (or while the
// LLM was down). Obviously-binary files are filtered out in Go (extract.Extractable)
// so the sweep doesn't churn on images. Called from staleSweepLoop alongside
// sweepStale. Best-effort.
func (s *Service) sweepStaleFiles(ctx context.Context) {
	if s.isPaused() {
		return
	}
	files, err := s.staleFileIDs(ctx, staleSweepBatch)
	if err != nil {
		slog.Error("summarize: file stale-sweep query", "err", err)
		return
	}
	n := 0
	for _, f := range files {
		if !extract.Extractable(f.mime, f.name) {
			continue
		}
		s.QueueFile(f.id)
		n++
	}
	if n > 0 {
		slog.Info("summarize: file stale-sweep enqueued", "files", n)
	}
}
