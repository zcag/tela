package rag

import (
	"context"
	"log/slog"
	"time"
)

// Auto-reindex: a debounced, coalescing background worker that keeps page_chunks
// fresh as pages are written, WITHOUT blocking the write path (embedding is a
// network op to the embedder). Handlers call QueueReindex(pageID) after a
// committed write; the worker reindexes each page once its edits settle, so a
// burst of saves on one page collapses to a single reindex.
//
// Self-healing by design: the index is a disposable cache. A reindex that fails
// (embedder down, transient network error) is RE-ENQUEUED with exponential
// backoff instead of dropped, and an independent stale-sweep periodically scans
// the whole corpus for pages whose index is missing or out of date and re-queues
// them. So an embedder outage degrades to "stale until the embedder returns and
// the next sweep/retry fires", never a permanent silent gap, and never a hard
// error on the user's save. The sweep also recovers edits made while the process
// was down (the in-memory queue doesn't survive a restart, the corpus does).

// Tunable (var, not const) so tests can shrink the windows. Production values:
var (
	// reindexDebounce is how long after the last edit to a page we wait before
	// reindexing it — long enough to coalesce an active editing burst.
	reindexDebounce = 3 * time.Second
	// reindexTick is how often the worker checks for pages whose debounce elapsed.
	reindexTick = 1 * time.Second
	// reindexTimeout caps a single page's reindex (chunk + embed round-trips).
	// It bounds how much work ONE attempt does, not how much a page needs: a
	// reindex commits each embedded batch as it goes, so a page too big to finish
	// in one window resumes from where it stopped instead of starting over. This
	// is why the cap can stay flat and modest — before that, it was the line past
	// which a page could never index at all, however many times it retried.
	reindexTimeout = 2 * time.Minute
	// reindexRetryBase / reindexRetryMax bound the exponential backoff applied to
	// a page whose reindex just failed: base * 2^(attempts-1), capped at max. A
	// failing embedder thus retries roughly every 30s → 1m → 2m → … up to 10m,
	// rather than hot-looping or giving up.
	reindexRetryBase = 30 * time.Second
	reindexRetryMax  = 10 * time.Minute
	// staleSweepInterval is how often the background sweep re-queues stale/unindexed
	// pages. staleSweepInitialDelay defers the first sweep past boot so a fresh
	// process isn't hammered. staleSweepBatch caps how many pages one sweep enqueues.
	staleSweepInterval     = 5 * time.Minute
	staleSweepInitialDelay = 30 * time.Second
	staleSweepBatch        = 500
)

// QueueReindex schedules pageID to be reindexed after the debounce window. Safe
// to call on every write; repeated calls for the same page coalesce and push the
// deadline forward. A fresh edit clears any accumulated retry backoff — new
// content deserves a prompt attempt. No-op when the embedder is unconfigured or
// the worker isn't running.
func (s *Service) QueueReindex(pageID int64) {
	if !s.Enabled() {
		return
	}
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	if s.pending == nil {
		return // worker not started
	}
	s.pending[pageID] = time.Now().Add(reindexDebounce)
	delete(s.attempts, pageID)
}

// queueReindexStale is QueueReindex for a page the SWEEP found stale, rather
// than one a user just edited. It must not touch a page that is already queued:
// clearing the retry counter and pulling the deadline forward is only right for
// new content, and doing it every sweep defeats the backoff entirely — a page
// that can never index (one too large to finish inside reindexTimeout) went
// 30s → 1m → *sweep* → 30s → 1m → … forever instead of decaying to 10m, and
// re-embedded itself around the clock for as long as it stayed stale.
func (s *Service) queueReindexStale(pageID int64) {
	if !s.Enabled() {
		return
	}
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	if s.pending == nil {
		return // worker not started
	}
	if _, queued := s.pending[pageID]; queued {
		return // already waiting (possibly backing off) — leave its deadline alone
	}
	if s.attempts[pageID] > 0 {
		return // failing page mid-attempt; its retry is the backoff's to schedule
	}
	s.pending[pageID] = time.Now().Add(reindexDebounce)
}

// StartAutoReindex launches the background reindex worker plus the stale-sweep
// loop. Idempotent; no-op when disabled. Both stop when ctx is cancelled. Call
// once from api.New.
func (s *Service) StartAutoReindex(ctx context.Context) {
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
	go s.reindexLoop(ctx)
	go s.staleSweepLoop(ctx)
}

// QueueReindexFile is QueueReindex for a space_file (the file half of the
// document index) — the trigger every upload path fires after a content change,
// and a delete fires to clear chunks. Same debounce/coalesce/backoff machinery
// as pages, keyed on file id.
func (s *Service) QueueReindexFile(fileID int64) {
	if !s.Enabled() {
		return
	}
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	if s.pendingFiles == nil {
		return // worker not started
	}
	s.pendingFiles[fileID] = time.Now().Add(reindexDebounce)
	delete(s.fileAttempts, fileID)
}

func (s *Service) reindexLoop(ctx context.Context) {
	t := time.NewTicker(reindexTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// Admin AI kill-switch: skip the tick entirely (don't drain the queue)
			// so backfilling resumes where it left off once AI is re-enabled.
			if s.isPaused() {
				continue
			}
			for _, id := range s.dueReindexes() {
				rctx, cancel := context.WithTimeout(ctx, reindexTimeout)
				embedded, err := s.ReindexPage(rctx, id)
				cancel()
				if err != nil {
					s.requeueAfterFailure(id, embedded, err)
				} else {
					s.clearAttempts(id)
				}
			}
			for _, id := range s.dueFileReindexes() {
				rctx, cancel := context.WithTimeout(ctx, reindexTimeout)
				embedded, err := s.ReindexFile(rctx, id)
				cancel()
				if err != nil {
					s.requeueFileAfterFailure(id, embedded, err)
				} else {
					s.clearFileAttempts(id)
				}
			}
		}
	}
}

// requeueAfterFailure re-enqueues a page whose reindex failed, with exponential
// backoff keyed on its consecutive-failure count, and logs the failure with the
// computed next-retry delay so an outage is visible without being noisy per-tick.
//
// embedded is how many chunks the failed run got through. Backoff is for a run
// that achieved NOTHING (embedder down, bad page); a run that embedded some of
// its chunks left them committed, so the next attempt has strictly less to do
// and should start soon rather than wait out a doubling delay. Without this a
// page needing several windows would crawl through the 30s→10m schedule even
// though it was converging the whole time.
func (s *Service) requeueAfterFailure(pageID int64, embedded int, cause error) {
	s.queueMu.Lock()
	if s.pending == nil { // worker stopped
		s.queueMu.Unlock()
		return
	}
	if embedded > 0 {
		s.pending[pageID] = time.Now().Add(reindexRetryBase)
		delete(s.attempts, pageID)
		s.queueMu.Unlock()
		slog.Info("rag: auto-reindex incomplete, resuming", "page_id", pageID, "embedded", embedded,
			"retry_in", reindexRetryBase, "err", cause)
		return
	}
	s.attempts[pageID]++
	n := s.attempts[pageID]
	shift := n - 1
	if shift > 16 { // cap before the shift so reindexRetryBase<<shift can't overflow int64
		shift = 16
	}
	backoff := reindexRetryBase << uint(shift)
	if backoff > reindexRetryMax || backoff <= 0 { // clamp to the ceiling (and belt-and-braces on overflow)
		backoff = reindexRetryMax
	}
	s.pending[pageID] = time.Now().Add(backoff)
	s.queueMu.Unlock()
	slog.Warn("rag: auto-reindex failed, will retry", "page_id", pageID, "attempt", n, "retry_in", backoff, "err", cause)
}

func (s *Service) clearAttempts(pageID int64) {
	s.queueMu.Lock()
	delete(s.attempts, pageID)
	s.queueMu.Unlock()
}

// dueFileReindexes / requeueFileAfterFailure / clearFileAttempts mirror the page
// trio for the file queue. Same debounce + exponential backoff, keyed on file id.

func (s *Service) dueFileReindexes() []int64 {
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

func (s *Service) requeueFileAfterFailure(fileID int64, embedded int, cause error) {
	s.queueMu.Lock()
	if s.pendingFiles == nil {
		s.queueMu.Unlock()
		return
	}
	if embedded > 0 { // made progress — resume soon instead of backing off
		s.pendingFiles[fileID] = time.Now().Add(reindexRetryBase)
		delete(s.fileAttempts, fileID)
		s.queueMu.Unlock()
		slog.Info("rag: file auto-reindex incomplete, resuming", "file_id", fileID, "embedded", embedded,
			"retry_in", reindexRetryBase, "err", cause)
		return
	}
	s.fileAttempts[fileID]++
	n := s.fileAttempts[fileID]
	shift := n - 1
	if shift > 16 {
		shift = 16
	}
	backoff := reindexRetryBase << uint(shift)
	if backoff > reindexRetryMax || backoff <= 0 {
		backoff = reindexRetryMax
	}
	s.pendingFiles[fileID] = time.Now().Add(backoff)
	s.queueMu.Unlock()
	slog.Warn("rag: file auto-reindex failed, will retry", "file_id", fileID, "attempt", n, "retry_in", backoff, "err", cause)
}

func (s *Service) clearFileAttempts(fileID int64) {
	s.queueMu.Lock()
	delete(s.fileAttempts, fileID)
	s.queueMu.Unlock()
}

// dueReindexes removes and returns the page IDs whose debounce window has
// elapsed. Pages still settling (or backing off after a failure) stay queued.
func (s *Service) dueReindexes() []int64 {
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

// staleSweepLoop periodically re-queues every page whose index is missing or out
// of date — the safety net that heals a stale backlog after an embedder outage
// or a process restart (which loses the in-memory queue but not the corpus). It
// also logs an index-health summary each cycle so the corpus's freshness is
// observable in the logs (scrapeable by the ops stack) without anyone querying
// the freshness API.
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
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// sweepStale enqueues up to staleSweepBatch stale/unindexed pages and logs a
// health line. Best-effort: query errors are logged, never fatal.
func (s *Service) sweepStale(ctx context.Context) {
	if s.isPaused() {
		return // AI kill-switch on — don't backfill while the embedder is paused
	}
	h, err := s.IndexHealth(ctx)
	if err != nil {
		slog.Error("rag: index-health query", "err", err)
		return
	}
	slog.Info("rag: index health",
		"content_pages", h.ContentPages, "indexed_pages", h.IndexedPages,
		"stale_pages", h.StalePages, "chunks", h.Chunks, "model_drift_chunks", h.ModelDriftChunks)
	if h.StalePages == 0 {
		return
	}
	ids, err := s.stalePageIDs(ctx, staleSweepBatch)
	if err != nil {
		slog.Error("rag: stale-sweep query", "err", err)
		return
	}
	for _, id := range ids {
		s.queueReindexStale(id)
	}
	slog.Info("rag: stale-sweep enqueued", "pages", len(ids), "stale_total", h.StalePages)
}
