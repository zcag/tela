package rag

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/zcag/tela/backend/internal/extract"
)

// index_file.go — the file half of the document index, sibling to index.go's
// ReindexPage. A page's source text is its body; a file's is its EXTRACTED text
// (PDF/plaintext). Everything else — heading-aware chunking, the per-chunk
// vector cache keyed by (model, text), the replace-in-one-tx write — is shared.

// ReindexFile rebuilds file_chunks for one attachment: load + extract → chunk →
// (reuse cached vector or embed) → replace rows in a transaction. Files whose
// bytes aren't text-extractable (images, binaries, scanned PDFs) index to zero
// chunks — benign, and any stale chunks are cleared. Returns chunks written.
func (s *Service) ReindexFile(ctx context.Context, fileID int64) (int, error) {
	if !s.Enabled() {
		return 0, fmt.Errorf("rag: embedder not configured")
	}

	var name, mime string
	var data []byte
	var deletedAt sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT name, mime, data, deleted_at FROM space_files WHERE id = $1`, fileID,
	).Scan(&name, &mime, &data, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil // gone between enqueue and reindex; chunks cascade-deleted
	}
	if err != nil {
		return 0, err
	}

	// Soft-deleted or not text-extractable → ensure no stale chunks, done.
	if deletedAt.Valid {
		return 0, s.clearFileChunks(ctx, fileID)
	}
	text, ok := extract.Text(mime, name, data)
	if !ok || text == "" {
		return 0, s.clearFileChunks(ctx, fileID)
	}

	chunks := ChunkMarkdown(name, text)
	cached, err := s.cachedFileVectors(ctx, fileID)
	if err != nil {
		return 0, err
	}

	model := s.emb.Model()
	rows := planRows(chunks, cached, model)
	if err := s.writeChunks(ctx, fileChunks, fileID, rows, model); err != nil {
		return 0, err
	}
	if embedded, err := s.fillEmbeddings(ctx, fileChunks, fileID, chunks, rows); err != nil {
		return embedded, err
	}
	return len(rows), nil
}

func (s *Service) clearFileChunks(ctx context.Context, fileID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM file_chunks WHERE space_file_id = $1`, fileID)
	return err
}

// cachedFileVectors mirrors cachedVectors for files: (content_hash → embedding
// literal) already stored, so an unchanged chunk skips the embedder on reindex.
func (s *Service) cachedFileVectors(ctx context.Context, fileID int64) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT content_hash, embedding::text FROM file_chunks WHERE space_file_id = $1 AND embedding IS NOT NULL`, fileID)
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
