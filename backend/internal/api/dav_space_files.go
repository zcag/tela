package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// dav_space_files.go — the blob store behind non-markdown files in the WebDAV
// sync tree (migration 0015). Markdown is a page (id-bound, 3-way-merged);
// everything else that isn't editor/OS junk is a space_file: identity is its
// LOCATION (space + parent-folder page + name), conflicts are last-write-wins by
// content, and it round-trips through /dav so a vault's PDFs/images/etc. reach
// every machine instead of being silently dropped. The davFS (dav_fs.go) calls
// these; serving/listing detail lives there.

// davFileMaxBytesDefault caps one synced file. Generous for attachments, bounded
// so a stray huge upload can't bloat the Postgres blob store unchecked. Override
// with TELA_WEBDAV_FILE_MAX_BYTES (bytes).
const davFileMaxBytesDefault int64 = 50 << 20

func davFileMaxBytes() int64 {
	if v := os.Getenv("TELA_WEBDAV_FILE_MAX_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return davFileMaxBytesDefault
}

// spaceFile is one stored non-markdown file's metadata (the bytes are loaded
// separately, on GET only, so a PROPFIND never pulls blob data). parentID is the
// folder page it lives under; nil = the space root.
type spaceFile struct {
	id        int64
	spaceID   int64
	parentID  *int64
	name      string
	hash      string
	mime      string
	size      int64
	updatedAt string
	// syncMtime is the client-supplied mtime still standing for this file (see
	// dav_mtime.go); "" → updatedAt is reported as getlastmodified.
	syncMtime string
}

// isSyncJunkName matches the OS/editor cruft we accept-and-drop rather than
// persist (the same set the rclone exclude list filters client-side). A real
// non-md file is stored; this junk is swallowed so native clients don't error.
func isSyncJunkName(name string) bool {
	if name == ".DS_Store" || name == "Thumbs.db" {
		return true
	}
	if strings.HasPrefix(name, "._") || strings.HasPrefix(name, "~$") {
		return true
	}
	l := strings.ToLower(name)
	return strings.HasSuffix(l, ".swp") || strings.HasSuffix(l, ".tmp")
}

// detectFileMime picks a Content-Type for an arbitrary synced file: extension
// first (covers pdf/zip/text/etc. precisely), then sniffing the bytes, then a
// generic binary fallback. Note: these files ARE served publicly via
// /api/files/{space_id}/{hash}{ext} — serve-side controls (Content-Disposition:
// attachment + X-Content-Type-Options: nosniff, raster-only inline) prevent
// browser XSS. Do not remove those serve-side controls; they are the safety net
// for arbitrary content types accepted here.
func detectFileMime(name string, data []byte) string {
	if ext := name[strings.LastIndexByte(name, '.')+1:]; ext != "" && ext != name {
		if m := mime.TypeByExtension("." + strings.ToLower(ext)); m != "" {
			return m
		}
	}
	if len(data) > 0 {
		return http.DetectContentType(data)
	}
	return "application/octet-stream"
}

// loadSpaceFiles reads a space's live files grouped by parent (parentKey:
// rootParentKey for space-root files, else the folder page id) — the per-space,
// once-per-request load that keeps a PROPFIND a single indexed query, mirroring
// the page tree. Blob data is excluded.
func loadSpaceFiles(ctx context.Context, db *sql.DB, spaceID int64) (map[int64][]spaceFile, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, parent_page_id, name, content_hash, mime, byte_size, updated_at,
		       CASE WHEN sync_mtime_at = updated_at THEN sync_mtime ELSE NULL END
		  FROM space_files
		 WHERE space_id = $1 AND deleted_at IS NULL
		 ORDER BY name ASC, id ASC`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]spaceFile{}
	for rows.Next() {
		f := spaceFile{spaceID: spaceID}
		var (
			parent sql.NullInt64
			mtime  sql.NullString
		)
		if err := rows.Scan(&f.id, &parent, &f.name, &f.hash, &f.mime, &f.size, &f.updatedAt, &mtime); err != nil {
			return nil, err
		}
		f.syncMtime = mtime.String
		if parent.Valid {
			pid := parent.Int64
			f.parentID = &pid
		}
		out[parentKey(f.parentID)] = append(out[parentKey(f.parentID)], f)
	}
	return out, rows.Err()
}

// lookupInSet finds a live file by name within a parent group of a loaded set.
func lookupInSet(set map[int64][]spaceFile, parentID *int64, name string) (spaceFile, bool) {
	for _, f := range set[parentKey(parentID)] {
		if f.name == name {
			return f, true
		}
	}
	return spaceFile{}, false
}

// readSpaceFileData fetches one live file's bytes (GET only).
func readSpaceFileData(ctx context.Context, db *sql.DB, id int64) ([]byte, error) {
	var data []byte
	err := db.QueryRowContext(ctx,
		`SELECT data FROM space_files WHERE id = $1 AND deleted_at IS NULL`, id).Scan(&data)
	return data, err
}

// upsertSpaceFile stores a PUT's bytes at (space, parent, name): a no-op when the
// live row already holds these exact bytes (no updated_at churn → rclone skips
// it next cycle), an in-place content replace when it differs, else a fresh row.
// Returns the resulting file metadata.
func upsertSpaceFile(ctx context.Context, db *sql.DB, spaceID int64, parentID *int64, name string, data []byte) (spaceFile, error) {
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	mimeType := detectFileMime(name, data)
	size := int64(len(data))

	var (
		curID   int64
		curHash string
		curMime string
		curUpd  string
	)
	err := db.QueryRowContext(ctx, `
		SELECT id, content_hash, mime, updated_at FROM space_files
		 WHERE space_id = $1 AND COALESCE(parent_page_id, 0) = $2 AND name = $3 AND deleted_at IS NULL`,
		spaceID, parentKey(parentID), name).Scan(&curID, &curHash, &curMime, &curUpd)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// New file.
		var f spaceFile
		err = db.QueryRowContext(ctx, `
			INSERT INTO space_files (space_id, parent_page_id, name, content_hash, mime, data, byte_size)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			RETURNING id, updated_at`,
			spaceID, parentID, name, hash, mimeType, data, size).Scan(&f.id, &f.updatedAt)
		if err != nil {
			return spaceFile{}, err
		}
		f.spaceID, f.parentID, f.name, f.hash, f.mime, f.size = spaceID, parentID, name, hash, mimeType, size
		return f, nil
	case err != nil:
		return spaceFile{}, err
	}
	// Existing live row.
	if curHash == hash {
		// Idempotent re-PUT — keep the row untouched so the ETag/modtime are stable.
		return spaceFile{id: curID, spaceID: spaceID, parentID: parentID, name: name, hash: hash, mime: curMime, size: size, updatedAt: curUpd}, nil
	}
	var upd string
	if err := db.QueryRowContext(ctx, `
		UPDATE space_files
		   SET content_hash = $2, mime = $3, data = $4, byte_size = $5, updated_at = tela_now()
		 WHERE id = $1
		 RETURNING updated_at`,
		curID, hash, mimeType, data, size).Scan(&upd); err != nil {
		return spaceFile{}, err
	}
	return spaceFile{id: curID, spaceID: spaceID, parentID: parentID, name: name, hash: hash, mime: mimeType, size: size, updatedAt: upd}, nil
}

// softDeleteSpaceFile marks a file deleted (recoverable). It frees the location
// for the partial unique index so a later PUT of the same name creates cleanly.
func softDeleteSpaceFile(ctx context.Context, db *sql.DB, id int64) error {
	_, err := db.ExecContext(ctx,
		`UPDATE space_files SET deleted_at = tela_now() WHERE id = $1 AND deleted_at IS NULL`, id)
	return err
}

// renameSpaceFileToSpace moves a file to a new (space, parent, name) — a WebDAV
// MOVE of a raw file, which can also relocate it across spaces.
func renameSpaceFileToSpace(ctx context.Context, db *sql.DB, id, spaceID int64, newParentID *int64, newName string) error {
	_, err := db.ExecContext(ctx, `
		UPDATE space_files
		   SET space_id = $2, parent_page_id = $3, name = $4, updated_at = tela_now()
		 WHERE id = $1 AND deleted_at IS NULL`,
		id, spaceID, newParentID, newName)
	return err
}

// countLiveSpaceFiles is the denominator the mass-delete brake measures a
// space's vanishing fraction against (the file analogue of countLiveSpacePages).
func countLiveSpaceFiles(ctx context.Context, db *sql.DB, spaceID int64) (int64, error) {
	var n int64
	err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM space_files WHERE space_id = $1 AND deleted_at IS NULL`, spaceID).Scan(&n)
	return n, err
}
