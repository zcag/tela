package rag

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"

	"github.com/zcag/tela/backend/internal/sheetproj"
)

// chunkHash keys a chunk's embedding by (model, embed-text) so a reindex can
// reuse a stored vector when nothing relevant changed. Folding the model in
// means switching embedders invalidates every cached vector automatically.
func chunkHash(model, embedText string) string {
	h := sha256.Sum256([]byte(model + "\x00" + embedText))
	return hex.EncodeToString(h[:])
}

// embedBatchSize is how many uncached chunks go upstream per request. The
// embedder is a shared GPU, so the cost that matters is REQUESTS, not chunks:
// at 32 a 829-chunk page is 26 round trips instead of 829. Sized to keep one
// request's payload small enough that a batch failure (which degrades to
// per-item inside embedMany) is cheap to redo.
const embedBatchSize = 32

// chunkTable names one of the two interchangeable chunk stores. page_chunks and
// file_chunks have the same shape and the same lifecycle, so the reindex body
// below is written once against this rather than twice against the two tables.
// Both fields are compile-time constants — never interpolate anything else.
type chunkTable struct{ table, idCol, label string }

var (
	pageChunks = chunkTable{"page_chunks", "page_id", "page"}
	fileChunks = chunkTable{"file_chunks", "space_file_id", "file"}
)

// indexRow is one chunk ready to be written. emb is a pgvector literal "[...]",
// or "" for a chunk whose vector hasn't been computed yet.
type indexRow struct {
	ord                    int
	hp, content, hash, emb string
}

// planRows pairs each chunk with the vector already cached for its (model, text)
// hash. A row with emb == "" still needs the embedder.
func planRows(chunks []Chunk, cached map[string]string, model string) []indexRow {
	rows := make([]indexRow, len(chunks))
	for i, c := range chunks {
		hash := chunkHash(model, c.EmbedText)
		rows[i] = indexRow{c.Ord, c.HeadingPath, c.Content, hash, cached[hash]}
	}
	return rows
}

// writeChunks replaces every chunk row for one page/file in a single
// transaction, storing whatever vectors we already have and leaving the rest
// NULL for fillEmbeddings to complete. `embedding` is nullable precisely so a
// chunk can exist before it is embedded (migration 0002) — this is the first
// caller to use that.
//
// Writing the rows BEFORE embedding is what makes a reindex resumable, and that
// is the fix for the runaway: embedding used to happen entirely in memory and
// land only in a closing transaction, so a reindex that ran out of time wrote
// NOTHING and its successor re-embedded every chunk from scratch. A page too big
// to finish in one window could therefore never index — not once, ever — while
// re-embedding itself around the clock. Now each completed batch is durable, so
// every attempt starts where the last one stopped and the page converges.
//
// It also puts the chunk TEXT in the lexical index immediately, so keyword
// search over new content no longer waits on the whole embed pass.
func (s *Service) writeChunks(ctx context.Context, ct chunkTable, id int64, rows []indexRow, model string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM `+ct.table+` WHERE `+ct.idCol+` = $1`, id); err != nil {
		return err
	}
	for _, r := range rows {
		var emb any // NULL, not "", for a chunk we haven't embedded yet
		if r.emb != "" {
			emb = r.emb
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO `+ct.table+`
			  (`+ct.idCol+`, ord, heading_path, content, content_hash, embedding, embed_model)
			VALUES ($1, $2, $3, $4, $5, $6::vector, $7)`,
			id, r.ord, r.hp, r.content, r.hash, emb, model,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// fillEmbeddings embeds every row still missing a vector, in batches, COMMITTING
// each batch before starting the next. It returns how many chunks it embedded —
// including on failure, so the caller can tell a run that made progress from one
// that got nowhere (see reindexLoop: progress means retry promptly rather than
// backing off, since the work left shrinks every time).
func (s *Service) fillEmbeddings(ctx context.Context, ct chunkTable, id int64, chunks []Chunk, rows []indexRow) (int, error) {
	var todo []int // indexes into rows/chunks that still need a vector
	for i := range rows {
		if rows[i].emb == "" {
			todo = append(todo, i)
		}
	}

	done := 0
	for start := 0; start < len(todo); start += embedBatchSize {
		batch := todo[start:min(start+embedBatchSize, len(todo))]
		inputs := make([]string, len(batch))
		for j, i := range batch {
			inputs[j] = chunks[i].EmbedText
		}
		vecs, _, err := s.EmbedMany(ctx, inputs)
		if err != nil {
			return done, fmt.Errorf("embed chunk %d of %s %d: %w", chunks[batch[0]].Ord, ct.label, id, err)
		}
		if err := s.storeVectors(ctx, ct, id, batch, rows, vecs); err != nil {
			return done, err
		}
		done += len(batch)
	}
	return done, nil
}

// storeVectors commits one batch's vectors onto their already-written rows.
func (s *Service) storeVectors(ctx context.Context, ct chunkTable, id int64, batch []int, rows []indexRow, vecs [][]float32) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for j, i := range batch {
		lit := vecLiteral(vecs[j])
		if _, err := tx.ExecContext(ctx,
			`UPDATE `+ct.table+` SET embedding = $1::vector, updated_at = tela_now()
			 WHERE `+ct.idCol+` = $2 AND ord = $3`, lit, id, rows[i].ord); err != nil {
			return err
		}
		rows[i].emb = lit
	}
	return tx.Commit()
}

// ReindexPage rebuilds page_chunks for one page: chunk → (reuse cached vector or
// embed) → replace rows in a single transaction. Idempotent; unchanged chunks
// reuse their stored vector and never re-hit the embedder. Returns the number of
// chunks written.
func (s *Service) ReindexPage(ctx context.Context, pageID int64) (int, error) {
	return s.reindexPage(ctx, pageID, false)
}

// reindexPage is ReindexPage with an explicit force flag. force=true bypasses the
// per-chunk vector cache so every chunk is re-embedded against the CURRENT
// embedder — the clean way to force a full re-embed after an embedder setup
// change that the model-name-keyed cache can't see (replaces a manual TRUNCATE).
func (s *Service) reindexPage(ctx context.Context, pageID int64, force bool) (int, error) {
	if !s.Enabled() {
		return 0, fmt.Errorf("rag: embedder not configured")
	}

	var title, body string
	var isSheet sql.NullString
	if err := s.db.QueryRowContext(ctx,
		`SELECT title, body, props->>'sheet' FROM pages WHERE id = $1`, pageID,
	).Scan(&title, &body, &isSheet); err != nil {
		// Page deleted between enqueue and reindex — benign; its chunks were
		// already removed by ON DELETE CASCADE. Nothing to index.
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}

	// A sheet's body is Defter markdown (compact tables + a defter-style block).
	// Embedding it raw buries the data under style noise and table syntax, so
	// project it to self-describing prose ("Sheet — Header: value, …") first —
	// this strips the presentation block and materializes literal cell values.
	// (Formula-*computed* values still need the TS engine; that's a follow-up via
	// a node projection step.)
	embedBody := StripExcalidrawFences(body)
	if isSheet.Valid && isSheet.String == "true" {
		embedBody = sheetproj.Project(ctx, body)
	}
	chunks := ChunkMarkdown(title, embedBody)
	cached := map[string]string{}
	if !force {
		var err error
		if cached, err = s.cachedVectors(ctx, pageID); err != nil {
			return 0, err
		}
	}

	model := s.emb.Model()
	rows := planRows(chunks, cached, model)
	if err := s.writeChunks(ctx, pageChunks, pageID, rows, model); err != nil {
		return 0, err
	}
	if embedded, err := s.fillEmbeddings(ctx, pageChunks, pageID, chunks, rows); err != nil {
		return embedded, err
	}

	// Stamp the page as indexed. This is the ONLY signal that a reindex ran, and
	// it has to be recorded rather than inferred: a page whose body is nothing
	// but a drawing chunks to zero rows, and "no rows" is indistinguishable from
	// "never indexed" — which left such pages permanently stale and re-swept
	// forever (migration 0076). Deliberately does NOT touch updated_at: that is
	// the user's edit clock, and bumping it here would re-stale the page against
	// its own index on every pass.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE pages SET indexed_at = tela_now() WHERE id = $1`, pageID); err != nil {
		return len(rows), err
	}
	return len(rows), nil
}

// cachedVectors returns the (content_hash -> embedding literal) map already
// stored for a page, used to skip re-embedding unchanged chunks across reindex
// runs. embedding::text renders the pgvector value back as "[...]" so it can be
// re-inserted via a ::vector cast without re-embedding.
func (s *Service) cachedVectors(ctx context.Context, pageID int64) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT content_hash, embedding::text FROM page_chunks WHERE page_id = $1 AND embedding IS NOT NULL`, pageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var h, e string
		if err := rows.Scan(&h, &e); err != nil {
			return nil, err
		}
		m[h] = e
	}
	return m, rows.Err()
}

// ReindexSpace reindexes every page in a space, page by page. Returns the number
// of pages processed and chunks written. Resilient: a single page that fails to
// embed is logged and skipped, not fatal — one bad page never aborts the run.
// err is returned only for an infrastructure failure (listing the pages).
func (s *Service) ReindexSpace(ctx context.Context, spaceID int64) (pages, chunks int, err error) {
	pages, chunks, _, err = s.reindexSpace(ctx, spaceID, false)
	return pages, chunks, err
}

// reindexSpace is ReindexSpace with a force flag and a failed-page count. ctx
// cancellation aborts the run (returns ctx.Err()); per-page embed failures are
// counted and skipped.
func (s *Service) reindexSpace(ctx context.Context, spaceID int64, force bool) (pages, chunks, failed int, err error) {
	if !s.Enabled() {
		return 0, 0, 0, fmt.Errorf("rag: embedder not configured")
	}
	ids, err := s.pageIDs(ctx, spaceID)
	if err != nil {
		return 0, 0, 0, err
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return pages, chunks, failed, err
		}
		n, err := s.reindexPage(ctx, id, force)
		if err != nil {
			slog.Error("rag: reindex page failed (skipping)", "space_id", spaceID, "page_id", id, "err", err)
			failed++
			continue
		}
		pages++
		chunks += n
	}
	return pages, chunks, failed, nil
}

// ReindexSummary is the result of a whole-corpus reindex (the reindex-all CLI).
type ReindexSummary struct {
	Spaces, Pages, Chunks, Failed int
	Files, FileChunks             int // the file half (attachments → file_chunks)
}

// ReindexAll re-embeds every page in every space against the current embedder,
// logging per-space progress. force=true bypasses the per-chunk cache (full
// re-embed). Resilient: a failing page is skipped and counted, never aborting
// the run; only an infrastructure failure (listing spaces/pages) returns err.
func (s *Service) ReindexAll(ctx context.Context, force bool) (ReindexSummary, error) {
	var sum ReindexSummary
	if !s.Enabled() {
		return sum, fmt.Errorf("rag: embedder not configured")
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

	slog.Info("reindex-all: starting", "spaces", len(spaces), "model", s.emb.Model(), "force", force)
	for i, sp := range spaces {
		pages, chunks, failed, err := s.reindexSpace(ctx, sp.id, force)
		if err != nil {
			return sum, fmt.Errorf("space %d (%s): %w", sp.id, sp.name, err)
		}
		sum.Spaces++
		sum.Pages += pages
		sum.Chunks += chunks
		sum.Failed += failed
		slog.Info("reindex-all: space done",
			"progress", i+1, "total", len(spaces), "space_id", sp.id, "name", sp.name,
			"pages", pages, "chunks", chunks, "failed", failed)
	}
	// The file half: walk every live attachment and (re)index its extracted text.
	// ReindexFile is idempotent (the per-chunk vector cache skips unchanged text;
	// non-text files index to zero chunks), so this back-fills attachments uploaded
	// before the feature AND re-embeds them on a model change — the same "reindex
	// everything" contract the name promises. Failures are counted, never fatal.
	fileIDs, err := s.allFileIDs(ctx)
	if err != nil {
		return sum, fmt.Errorf("list files: %w", err)
	}
	for _, fid := range fileIDs {
		n, err := s.ReindexFile(ctx, fid)
		if err != nil {
			sum.Failed++
			slog.Warn("reindex-all: file failed", "file_id", fid, "err", err)
			continue
		}
		sum.Files++
		sum.FileChunks += n
	}

	slog.Info("reindex-all: DONE",
		"spaces", sum.Spaces, "pages", sum.Pages, "chunks", sum.Chunks,
		"files", sum.Files, "file_chunks", sum.FileChunks, "failed", sum.Failed, "model", s.emb.Model())
	return sum, nil
}

// allFileIDs returns every live attachment id (corpus-wide), for ReindexAll's
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

func (s *Service) pageIDs(ctx context.Context, spaceID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM pages WHERE space_id = $1 AND deleted_at IS NULL ORDER BY id`, spaceID)
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
