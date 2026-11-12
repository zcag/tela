// Package summarize keeps machine-generated page summaries fresh as bodies
// change — the generation sibling to internal/rag's auto-reindex. The summary
// string lives in pages.props under 'summary' (blog excerpts, public meta
// descriptions and the title hover hint already read it); page_summaries
// (migration 0030) records the provenance: sha256(body) at generation time,
// model, timestamp, and failure state for the retry loop.
//
// Two hard rules shape the write path:
//   - props.summary_lock == true → the page's summary is never touched.
//   - persisting a summary is NOT a user edit: it must not bump
//     pages.updated_at and must not snapshot a revision, so machine
//     bookkeeping never pollutes history, recency feeds, or sync cursors.
//
// Wire-in mirrors rag exactly: one field on api.Server (s.summarize),
// constructed from the db + the existing llm service (TELA_LLM_URL). Disabled
// — but never nil — when the LLM is unconfigured, so the feature ships dark.
package summarize

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/zcag/tela/backend/internal/llm"
	"github.com/zcag/tela/backend/internal/sheetproj"
)

// Service bundles the DB handle and the chat-completion client, plus the
// debounced work queue (see worker.go). llm disabled ⇒ the whole feature
// no-ops.
type Service struct {
	db  *sql.DB
	llm *llm.Service

	// Debounce queue (see worker.go). pending maps page id → debounce deadline;
	// nil until Start runs. pendingFiles is the same for space_file ids. A
	// failure is not retried from here: the stale sweep owns retries, backing
	// off on the recorded failure. All under queueMu.
	queueMu      sync.Mutex
	pending      map[int64]time.Time
	pendingFiles map[int64]time.Time

	// paused, when set and true, halts background generation — wired to the admin
	// AI kill-switch so it doesn't call the LLM while it's under maintenance.
	paused func() bool
}

// SetPaused installs the predicate the worker consults each tick. Call before Start.
func (s *Service) SetPaused(fn func() bool) { s.paused = fn }

func (s *Service) isPaused() bool { return s.paused != nil && s.paused() }

// NewService builds the service. Never fails; with a disabled llm the service
// is constructed disabled so api.Server can hold a non-nil handle.
func NewService(db *sql.DB, l *llm.Service) *Service {
	return &Service{db: db, llm: l}
}

// Enabled reports whether a chat client is configured (TELA_LLM_URL set, or a
// fake injected in tests).
func (s *Service) Enabled() bool { return s.llm.Enabled() }

// Model returns the active chat model name ("" when disabled).
func (s *Service) Model() string { return s.llm.Model() }

// srcHash keys a generated summary to the exact body it was written from. The
// Go side and bodyHashExpr (SQL) must agree byte-for-byte: both are
// sha256 over the UTF-8 body, hex-encoded.
func srcHash(body string) string {
	h := sha256.Sum256([]byte(body))
	return hex.EncodeToString(h[:])
}

// bodyHashExpr is srcHash in SQL, for the status/stale queries.
const bodyHashExpr = `encode(sha256(convert_to(p.body, 'UTF8')), 'hex')`

// summarySystem is the generation prompt: a standfirst, not a "This page…"
// table of contents. It is deliberately extractive — summarize only what the
// body states, never infer from the title or outside knowledge — with an
// abstention hatch: a body with no readable prose (a bare diagram, a file
// embed) yields NONE rather than a confabulated standfirst. The abstention is
// load-bearing: a soft "grounded" instruction alone still lets the model invent
// a subject from a name in the title (see summarizeNone handling).
const summarySystem = "You write standfirsts for wiki pages. Summarize ONLY what the body actually states, " +
	"in 1-2 sentences (at most 50 words); never add facts that are not in the body. If the body has no " +
	"readable text to summarize, reply with exactly: NONE. Plain text only: no markdown, no surrounding " +
	"quotes, no openers like \"This page describes\"."

// summaryNone is the sentinel the model returns when the body carries nothing it
// can faithfully summarize. Matched case-insensitively, trailing period tolerated.
const summaryNone = "NONE"

// isNone reports whether a sanitized completion is the abstention sentinel.
func isNone(out string) bool {
	return strings.EqualFold(strings.TrimRight(out, "."), summaryNone)
}

// summarizeMaxBodyChars caps how much body is sent to the LLM (prompt-size
// bound; the opening of a wiki page carries the gist).
const summarizeMaxBodyChars = 12000

// Result says what SummarizePage did with a page, for CLI progress logs and
// the worker's skip-vs-work accounting.
type Result string

const (
	Generated       Result = "generated"
	SkippedFresh    Result = "fresh"      // stored hash matches body and summary present
	SkippedLocked   Result = "locked"     // props.summary_lock — never touched
	SkippedEmpty    Result = "empty"      // blank body — nothing to summarize
	SkippedNoneBody Result = "no_content" // body present but nothing faithfully summarizable (model → NONE)
	SkippedGone     Result = "gone"       // page deleted (or locked mid-flight)
)

// SummarizePage generates and persists the summary for one page. Idempotent:
// unless force, a page whose stored src_hash matches the current body (and
// whose props.summary is non-empty, with no pending failure) is skipped
// without an LLM call. Locked and blank pages are skipped recording nothing.
// On LLM failure the error is recorded in page_summaries (last_error,
// attempts++) so the status view reads failed, and the error is returned for
// the caller's retry policy.
func (s *Service) SummarizePage(ctx context.Context, pageID int64, force bool) (Result, error) {
	if !s.Enabled() {
		return "", fmt.Errorf("summarize: llm not configured")
	}

	var title, body string
	var propsRaw []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT title, body, props FROM pages WHERE id = $1 AND deleted_at IS NULL`, pageID,
	).Scan(&title, &body, &propsRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return SkippedGone, nil // deleted while queued — nothing to do
	}
	if err != nil {
		return "", fmt.Errorf("summarize: load page %d: %w", pageID, err)
	}
	props := map[string]any{}
	if len(propsRaw) > 0 {
		_ = json.Unmarshal(propsRaw, &props)
	}
	if locked, _ := props["summary_lock"].(bool); locked {
		return SkippedLocked, nil
	}
	if strings.TrimSpace(body) == "" {
		return SkippedEmpty, nil
	}

	hash := srcHash(body)
	if !force {
		// Fresh = a clean row (no pending failure) whose stored hash matches the
		// live body. A matching clean row means this exact body was already taken
		// to a terminal state — either a summary was written, or the model
		// abstained (NONE) and we deliberately left props.summary empty. Both are
		// fresh; only a hash mismatch (edited body) or a failed row re-runs. (The
		// Generated write is one tx, so a clean matching row can't have lost its
		// summary except via a deliberate NONE.)
		var have string
		err := s.db.QueryRowContext(ctx,
			`SELECT src_hash FROM page_summaries WHERE page_id = $1 AND last_error = ''`, pageID).Scan(&have)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("summarize: check hash: %w", err)
		}
		if err == nil && have == hash {
			return SkippedFresh, nil
		}
	}

	// A sheet's raw body is Defter markdown (formulas, not answers) — summarizing
	// it makes the model guess numbers and get them wrong. Feed the COMPUTED
	// projection instead so the standfirst quotes real totals. The staleness hash
	// stays keyed on the raw body above, so an edit still re-summarizes.
	promptBody := body
	if isSheet, _ := props["sheet"].(bool); isSheet {
		promptBody = sheetproj.Project(ctx, body)
	}
	user := "Title: " + title + "\n\n" + truncate(promptBody, summarizeMaxBodyChars)
	// Background work: bypass the foreground gate and never spill to the relief layer.
	out, err := s.llm.Complete(llm.WithBackground(ctx), summarySystem, user)
	if err == nil {
		if out = sanitize(out); out == "" {
			err = errors.New("llm returned an empty summary")
		}
	}
	if err != nil {
		s.recordFailure(ctx, pageID, hash, err)
		return "", fmt.Errorf("summarize page %d: %w", pageID, err)
	}

	// Abstention: the model judged the body has nothing to faithfully summarize
	// (a bare diagram, a file embed, a name-only stub). Persist NO summary —
	// clear any stale one — and record the row fresh so this body isn't retried.
	// Same tx discipline as the write path (lock re-check, no updated_at bump).
	if isNone(out) {
		if err := s.clearSummary(ctx, pageID, hash); err != nil {
			return "", err
		}
		return SkippedNoneBody, nil
	}

	// Persist in ONE tx: set ONLY props.summary — deliberately not the save path
	// (applyUpdateTx), so updated_at stays put and no revision is snapshotted.
	// The WHERE re-checks lock + liveness so a flip mid-generation wins.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("summarize: begin tx: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
		UPDATE pages SET props = jsonb_set(props, '{summary}', to_jsonb($2::text))
		 WHERE id = $1 AND deleted_at IS NULL
		   AND coalesce(props->>'summary_lock', '') <> 'true'`, pageID, out)
	if err != nil {
		return "", fmt.Errorf("summarize: write props: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return SkippedGone, nil
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO page_summaries (page_id, src_hash, model, generated_at, last_error, attempts)
		VALUES ($1, $2, $3, tela_now(), '', 0)
		ON CONFLICT (page_id) DO UPDATE
		   SET src_hash = EXCLUDED.src_hash, model = EXCLUDED.model,
		       generated_at = tela_now(), last_error = '', attempts = 0, failed_at = '', failed_hash = ''`,
		pageID, hash, s.llm.Model()); err != nil {
		return "", fmt.Errorf("summarize: upsert page_summaries: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("summarize: commit: %w", err)
	}
	return Generated, nil
}

// clearSummary handles the abstention (NONE) outcome: drop any existing
// props.summary and record the summary row as fresh for this body hash, so the
// page reads "done, no summary" and the worker won't re-attempt it until the
// body changes. Mirrors the write path — respects summary_lock, doesn't bump
// updated_at, snapshots no revision.
func (s *Service) clearSummary(ctx context.Context, pageID int64, hash string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("summarize: begin tx: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE pages SET props = props - 'summary'
		 WHERE id = $1 AND deleted_at IS NULL
		   AND coalesce(props->>'summary_lock', '') <> 'true'`, pageID); err != nil {
		return fmt.Errorf("summarize: clear summary: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO page_summaries (page_id, src_hash, model, generated_at, last_error, attempts)
		VALUES ($1, $2, $3, tela_now(), '', 0)
		ON CONFLICT (page_id) DO UPDATE
		   SET src_hash = EXCLUDED.src_hash, model = EXCLUDED.model,
		       generated_at = tela_now(), last_error = '', attempts = 0, failed_at = '', failed_hash = ''`,
		pageID, hash, s.llm.Model()); err != nil {
		return fmt.Errorf("summarize: upsert page_summaries (none): %w", err)
	}
	return tx.Commit()
}

// recordFailure upserts the failure state so the status view reads failed. A
// first-ever failure keeps src_hash/model ” (failed-never-generated); a
// failure after a success keeps the last good hash/model/generated_at.
// attempts counts failures of the same body (hash); a new body starts at 1.
// Best-effort: a bookkeeping error is logged, never surfaced over the LLM one.
func (s *Service) recordFailure(ctx context.Context, pageID int64, hash string, cause error) {
	ctx, cancel := bookkeepingContext(ctx)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO page_summaries (page_id, src_hash, model, last_error, attempts, failed_at, failed_hash)
		VALUES ($1, '', '', $2, 1, tela_now(), $3)
		ON CONFLICT (page_id) DO UPDATE
		   SET last_error = EXCLUDED.last_error, failed_at = EXCLUDED.failed_at, failed_hash = EXCLUDED.failed_hash,
		       attempts = CASE WHEN page_summaries.failed_hash = EXCLUDED.failed_hash
		                       THEN page_summaries.attempts + 1 ELSE 1 END`,
		pageID, cause.Error(), hash); err != nil {
		slog.Error("summarize: record failure", "page_id", pageID, "err", err)
	}
}

// bookkeepingContext detaches failure bookkeeping from the generation's
// deadline: the failure being recorded is often that deadline expiring, and a
// write on the expired context fails too, leaving the page unmarked and retried
// forever by the stale sweep.
func bookkeepingContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), recordFailureTimeout)
}

// truncate clips s to at most n bytes without splitting a UTF-8 rune.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 { // don't cut mid-rune
		n--
	}
	return s[:n]
}

// sanitize collapses the completion to a single trimmed line and strips one
// layer of wrapping quotes (models love quoting their own standfirst).
func sanitize(out string) string {
	out = strings.Join(strings.Fields(out), " ")
	for _, q := range [][2]string{{`"`, `"`}, {"'", "'"}, {"“", "”"}, {"‘", "’"}} {
		if strings.HasPrefix(out, q[0]) && strings.HasSuffix(out, q[1]) && len(out) > len(q[0])+len(q[1]) {
			out = strings.TrimSpace(out[len(q[0]) : len(out)-len(q[1])])
		}
	}
	return out
}

// RunSummary is SummarizeAll's tally, for the CLI exit report.
type RunSummary struct {
	Spaces, Pages, Generated, Skipped, Failed int
	Files                                     int // attachments processed (the file half)
}

// SummarizeAll walks every live page in every space serially and summarizes
// it. Resumable by virtue of the hash-skip (unless force, which regenerates
// everything); per-page LLM failures are recorded + counted, never abort the
// run. Backs the `tela summarize-all` subcommand, mirroring rag.ReindexAll.
func (s *Service) SummarizeAll(ctx context.Context, force bool) (RunSummary, error) {
	var sum RunSummary
	if !s.Enabled() {
		return sum, fmt.Errorf("summarize: llm not configured")
	}
	type spaceRef struct {
		id   int64
		name string
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name FROM spaces ORDER BY id`)
	if err != nil {
		return sum, fmt.Errorf("list spaces: %w", err)
	}
	var spaces []spaceRef
	for rows.Next() {
		var sp spaceRef
		if err := rows.Scan(&sp.id, &sp.name); err != nil {
			rows.Close()
			return sum, err
		}
		spaces = append(spaces, sp)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return sum, err
	}

	slog.Info("summarize-all: starting", "spaces", len(spaces), "model", s.llm.Model(), "force", force)
	for i, sp := range spaces {
		ids, err := s.spacePageIDs(ctx, sp.id)
		if err != nil {
			return sum, fmt.Errorf("space %d (%s): %w", sp.id, sp.name, err)
		}
		var generated, skipped, failed int
		for _, id := range ids {
			res, err := s.SummarizePage(ctx, id, force)
			switch {
			case err != nil:
				failed++
				slog.Warn("summarize-all: page failed", "page_id", id, "err", err)
			case res == Generated:
				generated++
			default:
				skipped++
			}
		}
		sum.Spaces++
		sum.Pages += len(ids)
		sum.Generated += generated
		sum.Skipped += skipped
		sum.Failed += failed
		slog.Info("summarize-all: space done",
			"progress", i+1, "total", len(spaces), "space_id", sp.id, "name", sp.name,
			"pages", len(ids), "generated", generated, "skipped", skipped, "failed", failed)
	}
	// The file half: summarize every live attachment (text-extractable ones get a
	// standfirst; non-text files record an empty, fresh row so they're not retried).
	// Idempotent via the content_hash skip, exactly like pages. Failures counted.
	fileIDs, err := s.allFileIDs(ctx)
	if err != nil {
		return sum, fmt.Errorf("list files: %w", err)
	}
	for _, fid := range fileIDs {
		res, err := s.SummarizeFile(ctx, fid, force)
		switch {
		case err != nil:
			sum.Failed++
			slog.Warn("summarize-all: file failed", "file_id", fid, "err", err)
		case res == Generated:
			sum.Files++
			sum.Generated++
		default:
			sum.Skipped++
		}
	}

	slog.Info("summarize-all: DONE",
		"spaces", sum.Spaces, "pages", sum.Pages, "generated", sum.Generated,
		"skipped", sum.Skipped, "failed", sum.Failed, "files", sum.Files, "model", s.llm.Model())
	return sum, nil
}

// allFileIDs returns every live attachment id (corpus-wide), for SummarizeAll's
// file pass. Ordered by id for stable, resumable progress.
func (s *Service) allFileIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM space_files WHERE deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Service) spacePageIDs(ctx context.Context, spaceID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM pages WHERE space_id = $1 AND deleted_at IS NULL ORDER BY id`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
