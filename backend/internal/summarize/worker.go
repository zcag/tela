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
// Self-healing by design, mirroring rag/autoreindex.go: a failed generation is
// re-enqueued with exponential backoff (and recorded in page_summaries so the
// status view reads failed), and an independent stale sweep periodically
// re-queues pages whose summary is missing or out of date — recovering both
// LLM outages and edits made while the process was down.

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
	// summarizeRetryBase / summarizeRetryMax bound the exponential backoff applied
	// to a page whose generation just failed: base * 2^(attempts-1), capped at max.
	summarizeRetryBase = 30 * time.Second
	summarizeRetryMax  = 10 * time.Minute
	// staleSweepInterval is how often the background sweep re-queues stale/missing
	// summaries. staleSweepInitialDelay defers the first sweep past boot;
	// staleSweepBatch caps how many pages one sweep enqueues.
	staleSweepInterval     = 5 * time.Minute
	staleSweepInitialDelay = 30 * time.Second
	staleSweepBatch        = 500
)

// Queue schedules pageID to be summarized after the debounce window. Safe to
// call on every write; repeated calls for the same page coalesce and push the
// deadline forward. A fresh edit clears any accumulated retry backoff. No-op
// when the LLM is unconfigured or the worker isn't running.
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
	delete(s.attempts, pageID)
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
	s.attempts = make(map[int64]int)
	s.pendingFiles = make(map[int64]time.Time)
	s.fileAttempts = make(map[int64]int)
	s.queueMu.Unlock()
	go s.summarizeLoop(ctx)
	go s.staleSweepLoop(ctx)
}

// QueueFile is Queue for a space_file (the file half of auto-summary) — the
// trigger every upload path fires after a content change, alongside
// rag.QueueReindexFile. Same debounce/coalesce/backoff machinery, keyed on file id.
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
	delete(s.fileAttempts, fileID)
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
				_, err := s.SummarizePage(sctx, id, false)
				cancel()
				if err != nil {
					s.requeueAfterFailure(id)
				} else {
					s.clearAttempts(id)
				}
			}
			for _, id := range s.dueFileSummaries() {
				sctx, cancel := context.WithTimeout(ctx, summarizeTimeout)
				_, err := s.SummarizeFile(sctx, id, false)
				cancel()
				if err != nil {
					s.requeueFileAfterFailure(id)
				} else {
					s.clearFileAttempts(id)
				}
			}
		}
	}
}

// requeueAfterFailure re-enqueues a page whose generation failed, with
// exponential backoff keyed on its consecutive-failure count.
func (s *Service) requeueAfterFailure(pageID int64) {
	s.queueMu.Lock()
	if s.pending == nil { // worker stopped
		s.queueMu.Unlock()
		return
	}
	s.attempts[pageID]++
	n := s.attempts[pageID]
	shift := n - 1
	if shift > 16 { // cap before the shift so summarizeRetryBase<<shift can't overflow int64
		shift = 16
	}
	backoff := summarizeRetryBase << uint(shift)
	if backoff > summarizeRetryMax || backoff <= 0 { // clamp to the ceiling (and belt-and-braces on overflow)
		backoff = summarizeRetryMax
	}
	s.pending[pageID] = time.Now().Add(backoff)
	s.queueMu.Unlock()
	slog.Warn("summarize: generation failed, will retry", "page_id", pageID, "attempt", n, "retry_in", backoff)
}

func (s *Service) clearAttempts(pageID int64) {
	s.queueMu.Lock()
	delete(s.attempts, pageID)
	s.queueMu.Unlock()
}

// dueFileSummaries / requeueFileAfterFailure / clearFileAttempts mirror the page
// trio for the file queue — same debounce + exponential backoff, keyed on file id.

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

func (s *Service) requeueFileAfterFailure(fileID int64) {
	s.queueMu.Lock()
	if s.pendingFiles == nil {
		s.queueMu.Unlock()
		return
	}
	s.fileAttempts[fileID]++
	n := s.fileAttempts[fileID]
	shift := n - 1
	if shift > 16 {
		shift = 16
	}
	backoff := summarizeRetryBase << uint(shift)
	if backoff > summarizeRetryMax || backoff <= 0 {
		backoff = summarizeRetryMax
	}
	s.pendingFiles[fileID] = time.Now().Add(backoff)
	s.queueMu.Unlock()
	slog.Warn("summarize: file generation failed, will retry", "file_id", fileID, "attempt", n, "retry_in", backoff)
}

func (s *Service) clearFileAttempts(fileID int64) {
	s.queueMu.Lock()
	delete(s.fileAttempts, fileID)
	s.queueMu.Unlock()
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
