package summarize

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/zcag/tela/backend/internal/extract"
	"github.com/zcag/tela/backend/internal/llm"
)

// summarize_file.go — the file half of auto-summary, sibling to summarize.go's
// SummarizePage (the same shape rag uses: index_file.go beside index.go). A
// page's source text is its body; a file's is its EXTRACTED text (PDF/plaintext).
// The summary string lives in space_files.summary (files have no props bag) and
// file_summaries records the provenance, keyed by the file's content_hash so
// freshness is decided without re-extracting.

// SummarizeFile generates and persists the summary for one attachment. Idempotent:
// unless force, a file whose file_summaries.src_hash already matches its current
// content_hash (and last attempt didn't fail) is skipped without extraction or an
// LLM call. A file whose bytes aren't text-extractable (image, scanned PDF, binary)
// records a fresh row with an EMPTY summary — so the stale sweep marks it done and
// never re-queues it — and returns SkippedEmpty. On LLM failure the error is
// recorded (attempts++) and returned for the caller's retry policy.
func (s *Service) SummarizeFile(ctx context.Context, fileID int64, force bool) (Result, error) {
	if !s.Enabled() {
		return "", fmt.Errorf("summarize: llm not configured")
	}

	// Metadata only first (no bytea) — the fresh path must be cheap.
	var name, mime, contentHash string
	var deletedAt sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT name, mime, content_hash, deleted_at FROM space_files WHERE id = $1`, fileID,
	).Scan(&name, &mime, &contentHash, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SkippedGone, nil // deleted while queued — file_summaries cascade-cleared
	}
	if err != nil {
		return "", fmt.Errorf("summarize: load file %d: %w", fileID, err)
	}
	if deletedAt.Valid {
		return SkippedGone, nil
	}

	if !force {
		var have string
		err := s.db.QueryRowContext(ctx,
			`SELECT src_hash FROM file_summaries WHERE space_file_id = $1 AND last_error = ''`, fileID).Scan(&have)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("summarize: check file hash: %w", err)
		}
		if err == nil && have == contentHash { // fresh — empty summary for a non-text file is still "done"
			return SkippedFresh, nil
		}
	}

	// Not fresh → load the bytes and extract.
	var data []byte
	if err := s.db.QueryRowContext(ctx, `SELECT data FROM space_files WHERE id = $1`, fileID).Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SkippedGone, nil
		}
		return "", fmt.Errorf("summarize: load file bytes %d: %w", fileID, err)
	}

	text, ok := extract.Text(mime, name, data)
	if !ok || text == "" {
		// Not text-extractable: record a fresh, empty-summary row so the sweep
		// stops re-queuing it (content_hash matches, no error).
		if err := s.persistFileSummary(ctx, fileID, "", contentHash); err != nil {
			return "", err
		}
		return SkippedEmpty, nil
	}

	user := "Filename: " + name + "\n\n" + truncate(text, summarizeMaxBodyChars)
	// Background work: bypass the foreground gate and never spill to the relief layer.
	out, err := s.llm.Complete(llm.WithBackground(ctx), summaryFileSystem, user)
	if err == nil {
		if out = sanitize(out); out == "" {
			err = errors.New("llm returned an empty summary")
		}
	}
	if err != nil {
		s.recordFileFailure(ctx, fileID, err)
		return "", fmt.Errorf("summarize file %d: %w", fileID, err)
	}
	if err := s.persistFileSummary(ctx, fileID, out, contentHash); err != nil {
		return "", err
	}
	return Generated, nil
}

// summaryFileSystem is the file variant of summarySystem: a standfirst for a
// document/attachment rather than a wiki page.
const summaryFileSystem = "You write one-line descriptions of attached documents. Reply with a 1-2 sentence " +
	"factual summary of the document's contents and purpose, at most 50 words. Plain text only: no markdown, " +
	"no surrounding quotes, and no boilerplate openers like \"This document\" — state the substance directly."

// persistFileSummary writes the summary column + the file_summaries provenance in
// one tx. summary may be "" (a non-text file) — the row still records that the
// file was processed at this content_hash so the sweep won't re-queue it. The
// WHERE re-checks liveness so a delete mid-generation wins.
func (s *Service) persistFileSummary(ctx context.Context, fileID int64, summary, contentHash string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("summarize: begin file tx: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`UPDATE space_files SET summary = $2 WHERE id = $1 AND deleted_at IS NULL`, fileID, summary)
	if err != nil {
		return fmt.Errorf("summarize: write file summary: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil // deleted mid-flight
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO file_summaries (space_file_id, src_hash, model, generated_at, last_error, attempts)
		VALUES ($1, $2, $3, tela_now(), '', 0)
		ON CONFLICT (space_file_id) DO UPDATE
		   SET src_hash = EXCLUDED.src_hash, model = EXCLUDED.model,
		       generated_at = tela_now(), last_error = '', attempts = 0`,
		fileID, contentHash, s.llm.Model()); err != nil {
		return fmt.Errorf("summarize: upsert file_summaries: %w", err)
	}
	return tx.Commit()
}

// recordFileFailure mirrors recordFailure for files: upsert the failure state so
// the stale sweep keeps retrying. Best-effort — bookkeeping errors are logged.
func (s *Service) recordFileFailure(ctx context.Context, fileID int64, cause error) {
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO file_summaries (space_file_id, src_hash, model, last_error, attempts)
		VALUES ($1, '', '', $2, 1)
		ON CONFLICT (space_file_id) DO UPDATE
		   SET last_error = EXCLUDED.last_error, attempts = file_summaries.attempts + 1`,
		fileID, cause.Error()); err != nil {
		slog.Error("summarize: record file failure", "file_id", fileID, "err", err)
	}
}

// staleFileIDs returns up to limit live, text-extractable file ids that need a
// (re)summary — no fresh file_summaries row at the current content_hash, or a
// recorded failure. The mime/name pre-filter keeps obviously-binary files
// (images) out of the sweep entirely (extract.Extractable mirrored in SQL would
// drift, so the worker filters in Go after this cheap query). Most-recent first.
func (s *Service) staleFileIDs(ctx context.Context, limit int) ([]fileRef, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT sf.id, sf.mime, sf.name
		  FROM space_files sf
		  LEFT JOIN file_summaries fs ON fs.space_file_id = sf.id
		 WHERE sf.deleted_at IS NULL
		   AND (fs.space_file_id IS NULL OR fs.src_hash <> sf.content_hash OR coalesce(fs.last_error, '') <> '')
		 ORDER BY sf.updated_at DESC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []fileRef
	for rows.Next() {
		var f fileRef
		if err := rows.Scan(&f.id, &f.mime, &f.name); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

type fileRef struct {
	id         int64
	mime, name string
}
