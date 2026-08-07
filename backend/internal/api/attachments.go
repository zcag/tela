package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/zcag/tela/backend/internal/auth"
)

// attachments.go — the page-attachments surface over the unified space_files
// blob store (migration 0015). A page's attachments are the space_files parented
// to it (parent_page_id), which is exactly where a file rclone-synced into the
// page's folder lands — so synced files show up as attachments with no body edit.
//
// Two routes:
//
//   - GET /api/pages/{id}/attachments — session-authed (any space role reads).
//     Lists the parented files with a stable, content-addressed serve URL and an
//     `embedded` flag (does the body already reference this file's hash).
//
//   - GET /api/files/{space_id}/{file} — PUBLIC, no session (reachable from
//     share/public readers). Content-addressed by hash → immutable cache. Images
//     (raster) serve inline so `![](url)` renders; everything else is forced to
//     download (Content-Disposition: attachment) + nosniff, so an embedded
//     .html/.svg can't run as stored-XSS from our origin. /api/files/ is on
//     auth.IsPublicPath, same shape as /api/images/ and /api/diagrams/.

// inlineServeMimes are the only types served inline from /api/files; everything
// else downloads. Raster images only — SVG (image/svg+xml) is deliberately
// excluded (it can carry scripts).
var inlineServeMimes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
}

type attachmentOut struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Mime     string `json:"mime"`
	ByteSize int64  `json:"byte_size"`
	Hash     string `json:"hash"`
	URL      string `json:"url"`
	Embedded bool   `json:"embedded"`          // body already references this file's hash
	Summary  string `json:"summary,omitempty"` // machine-generated, "" until the worker runs (or non-text)
}

// spaceFileServeURL is the stable, rename-proof URL for a stored file: keyed by
// space + content hash (not path), so a sync rename/move never breaks a body
// embed. The extension is cosmetic (the stored mime is authoritative on serve).
func spaceFileServeURL(spaceID int64, name, hash string) string {
	ext := strings.ToLower(path.Ext(name)) // includes the dot, or ""
	return fmt.Sprintf("/api/files/%d/%s%s", spaceID, hash, ext)
}

// ---- transport-agnostic cores (shared by the REST handlers + the MCP
// list_attachments / upload_attachment / delete_attachment tools) ----
//
// A missing page resolves to the same 403 "not a member" the membership check
// returns, so the routes don't become a page-existence enumeration oracle.

// listPageAttachmentsCore lists a page's live space_files (uploads AND files
// rclone-synced into its folder) with stable serve URLs + an embedded flag. Any
// space member may read.
func (s *Server) listPageAttachmentsCore(ctx context.Context, u *auth.User, k *auth.APIKey, pageID int64) ([]attachmentOut, *apiErr) {
	page, err := selectPageByID(ctx, s.DB, pageID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &apiErr{http.StatusForbidden, "forbidden", "not a member"}
	}
	if err != nil {
		return nil, &apiErr{http.StatusInternalServerError, "internal", "lookup page failed"}
	}
	if _, ae := s.membershipCore(ctx, u, k, page.SpaceID); ae != nil {
		return nil, ae
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, name, mime, byte_size, content_hash, summary
		  FROM space_files
		 WHERE parent_page_id = $1 AND deleted_at IS NULL
		 ORDER BY name ASC, id ASC`, pageID)
	if err != nil {
		return nil, &apiErr{http.StatusInternalServerError, "internal", "list attachments failed"}
	}
	defer rows.Close()
	out := []attachmentOut{}
	for rows.Next() {
		var a attachmentOut
		if err := rows.Scan(&a.ID, &a.Name, &a.Mime, &a.ByteSize, &a.Hash, &a.Summary); err != nil {
			return nil, &apiErr{http.StatusInternalServerError, "internal", "scan attachment failed"}
		}
		a.URL = spaceFileServeURL(page.SpaceID, a.Name, a.Hash)
		a.Embedded = strings.Contains(page.Body, a.Hash)
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, &apiErr{http.StatusInternalServerError, "internal", "list attachments failed"}
	}
	return out, nil
}

// uploadPageAttachmentCore stores data as a space_file parented to the page and
// returns its metadata + serve URL (editor+). The unified path for BOTH inline
// images and other attachments. Callers pass the already-read bytes (the REST
// handler streams the multipart under a MaxBytesReader; the MCP tool decodes
// base64), so the size cap is enforced here too.
func (s *Server) uploadPageAttachmentCore(ctx context.Context, u *auth.User, k *auth.APIKey, pageID int64, filename string, data []byte) (attachmentOut, *apiErr) {
	if len(data) == 0 {
		return attachmentOut{}, &apiErr{http.StatusBadRequest, "bad_request", "uploaded file is empty"}
	}
	if int64(len(data)) > davFileMaxBytes() {
		return attachmentOut{}, &apiErr{http.StatusRequestEntityTooLarge, "too_large", "file exceeds the size limit"}
	}
	page, err := selectPageByID(ctx, s.DB, pageID)
	if errors.Is(err, sql.ErrNoRows) {
		return attachmentOut{}, &apiErr{http.StatusForbidden, "forbidden", "not a member"}
	}
	if err != nil {
		return attachmentOut{}, &apiErr{http.StatusInternalServerError, "internal", "lookup page failed"}
	}
	role, ae := s.membershipCore(ctx, u, k, page.SpaceID)
	if ae != nil {
		return attachmentOut{}, ae
	}
	if !canEdit(role) {
		return attachmentOut{}, &apiErr{http.StatusForbidden, "viewer_no_write", "editor or owner role required"}
	}
	if ae := s.checkStorageQuota(ctx, page.SpaceID, int64(len(data))); ae != nil {
		return attachmentOut{}, ae
	}
	sf, err := s.createPageUploadFile(ctx, page.SpaceID, pageID, sanitizeUploadName(filename), data)
	if err != nil {
		return attachmentOut{}, &apiErr{http.StatusInternalServerError, "internal", "store attachment failed"}
	}
	return attachmentOut{
		ID: sf.id, Name: sf.name, Mime: sf.mime, ByteSize: sf.size, Hash: sf.hash,
		URL:      spaceFileServeURL(page.SpaceID, sf.name, sf.hash),
		Embedded: strings.Contains(page.Body, sf.hash),
	}, nil
}

// deletePageAttachmentCore soft-deletes a space_file parented to the page (editor+).
func (s *Server) deletePageAttachmentCore(ctx context.Context, u *auth.User, k *auth.APIKey, pageID, fileID int64) *apiErr {
	page, err := selectPageByID(ctx, s.DB, pageID)
	if errors.Is(err, sql.ErrNoRows) {
		return &apiErr{http.StatusForbidden, "forbidden", "not a member"}
	}
	if err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "lookup page failed"}
	}
	role, ae := s.membershipCore(ctx, u, k, page.SpaceID)
	if ae != nil {
		return ae
	}
	if !canEdit(role) {
		return &apiErr{http.StatusForbidden, "viewer_no_write", "editor or owner role required"}
	}
	res, err := s.DB.ExecContext(ctx, `
		UPDATE space_files SET deleted_at = tela_now()
		 WHERE id = $1 AND parent_page_id = $2 AND deleted_at IS NULL`, fileID, pageID)
	if err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "delete attachment failed"}
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return &apiErr{http.StatusNotFound, "not_found", "attachment not found on this page"}
	}
	// Soft-delete → ReindexFile sees deleted_at and clears the file's chunks.
	s.rag.QueueReindexFile(fileID)
	return nil
}

// ListPageAttachments handles GET /api/pages/{id}/attachments.
func (s *Server) ListPageAttachments(w http.ResponseWriter, r *http.Request) {
	pageID, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	atts, ae := s.listPageAttachmentsCore(r.Context(), u, k, pageID)
	if ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"attachments": atts})
}

// UploadPageAttachment handles POST /api/pages/{id}/attachments (multipart,
// field "file"). Editor+ on the page's space. This is the unified upload path
// the editor uses for BOTH inline images and other attachments.
func (s *Server) UploadPageAttachment(w http.ResponseWriter, r *http.Request) {
	pageID, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, davFileMaxBytes())
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "expected a multipart 'file' part")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not read uploaded file (too large?)")
		return
	}
	a, ae := s.uploadPageAttachmentCore(r.Context(), u, k, pageID, hdr.Filename, data)
	if ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"attachment": a})
}

// DeletePageAttachment handles DELETE /api/pages/{id}/attachments/{file_id}.
func (s *Server) DeletePageAttachment(w http.ResponseWriter, r *http.Request) {
	pageID, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	fileID, ok := parseIDParam(w, r, "file_id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	if ae := s.deletePageAttachmentCore(r.Context(), u, k, pageID, fileID); ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// sanitizeUploadName reduces a client-supplied filename to a safe basename (no
// path, no traversal), falling back to "file" when empty.
func sanitizeUploadName(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "file"
	}
	return name
}

// createPageUploadFile stores an uploaded file parented to a page, content-aware
// so distinct uploads never clobber each other: identical bytes at the same name
// dedupe (idempotent), but DIFFERENT bytes that would collide on the name (e.g.
// two pasted "image.png") get a `-<hash8>` suffix so the first embed keeps
// working. The mime for a recognised raster image is taken from magic bytes (so
// the inline-serve path is trustworthy), else inferred from name/sniff.
func (s *Server) createPageUploadFile(ctx context.Context, spaceID, pageID int64, name string, data []byte) (spaceFile, error) {
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	mimeType := detectImageMime(data)
	if mimeType == "" {
		mimeType = detectFileMime(name, data)
	}
	finalName := name
	if h, found, err := liveFileHashAt(ctx, s.DB, spaceID, pageID, finalName); err != nil {
		return spaceFile{}, err
	} else if found {
		if h == hash {
			// Identical bytes already stored → idempotent, already indexed.
			return spaceFile{spaceID: spaceID, parentID: &pageID, name: finalName, hash: hash, mime: mimeType, size: int64(len(data))}, nil
		}
		ext := path.Ext(name)
		finalName = strings.TrimSuffix(name, ext) + "-" + hash[:8] + ext
		if h2, f2, err := liveFileHashAt(ctx, s.DB, spaceID, pageID, finalName); err != nil {
			return spaceFile{}, err
		} else if f2 && h2 == hash {
			return spaceFile{spaceID: spaceID, parentID: &pageID, name: finalName, hash: hash, mime: mimeType, size: int64(len(data))}, nil
		}
	}
	var sf spaceFile
	err := s.DB.QueryRowContext(ctx, `
		INSERT INTO space_files (space_id, parent_page_id, name, content_hash, mime, data, byte_size)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id`,
		spaceID, pageID, finalName, hash, mimeType, data, int64(len(data))).Scan(&sf.id)
	if err != nil {
		return spaceFile{}, err
	}
	sf.spaceID, sf.parentID, sf.name, sf.hash, sf.mime, sf.size = spaceID, &pageID, finalName, hash, mimeType, int64(len(data))
	// Store-and-announce: a new blob → enqueue text extraction + indexing (the
	// file half of the RAG index) and an auto-summary. Both no-op when their
	// service is unconfigured.
	s.rag.QueueReindexFile(sf.id)
	s.summarize.QueueFile(sf.id)
	return sf, nil
}

// liveFileHashAt returns the content hash of the live file at (space, parent, name).
func liveFileHashAt(ctx context.Context, db *sql.DB, spaceID, pageID int64, name string) (hash string, found bool, err error) {
	err = db.QueryRowContext(ctx, `
		SELECT content_hash FROM space_files
		 WHERE space_id = $1 AND COALESCE(parent_page_id, 0) = $2 AND name = $3 AND deleted_at IS NULL`,
		spaceID, pageID, name).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return hash, true, nil
}

// ServeSpaceFile handles GET /api/files/{space_id}/{file}.
//
// Public — no session. Path values are validated before touching SQL; mismatches
// return 404 (not 400) so the route is not an enumeration oracle.
func (s *Server) ServeSpaceFile(w http.ResponseWriter, r *http.Request) {
	spaceID, err := strconv.ParseInt(r.PathValue("space_id"), 10, 64)
	if err != nil || spaceID <= 0 {
		writeError(w, http.StatusNotFound, "not_found", "file not found")
		return
	}
	// Go 1.22+ mux wildcards must end a segment, so the extension is part of
	// {file}. Strip from the first dot to recover the hash.
	file := r.PathValue("file")
	hash := file
	if dot := strings.IndexByte(file, '.'); dot >= 0 {
		hash = file[:dot]
	}
	if !pageImageHashRE.MatchString(hash) { // 64 lowercase hex — shared validator
		writeError(w, http.StatusNotFound, "not_found", "file not found")
		return
	}

	etag := `"` + hash + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.WriteHeader(http.StatusNotModified)
		return
	}

	var (
		data []byte
		mime string
		name string
	)
	// Content-addressed: any live row with these bytes serves them — identical
	// content may exist at several locations, but the bytes (and thus the
	// response) are the same. We prefer a row in the URL's space, but fall back
	// to any space holding the hash so embeds authored before a cross-space page
	// move (their URL still hard-codes the old space) keep resolving.
	err = s.DB.QueryRowContext(r.Context(), `
		SELECT data, mime, name FROM space_files
		 WHERE content_hash = $2 AND deleted_at IS NULL
		 ORDER BY (space_id = $1) DESC, id ASC
		 LIMIT 1`, spaceID, hash).Scan(&data, &mime, &name)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "file not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "fetch file failed")
		return
	}

	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("ETag", etag)
	// Inline only for safe raster images (so `![](url)` renders); force download
	// for everything else so arbitrary content can't execute from our origin.
	if inlineServeMimes[mime] {
		w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", name))
	} else {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
