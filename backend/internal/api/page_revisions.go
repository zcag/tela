package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/zcag/tela/backend/internal/auth"
	"github.com/zcag/tela/backend/internal/models"
)

const (
	pageRevisionsDefaultLimit = 50
	pageRevisionsMaxLimit     = 200
)

// pageRevisionListColumns is the SELECT used by the list endpoint — body
// is intentionally excluded so the per-row payload stays small.
const pageRevisionListColumns = `
	SELECT r.id, r.page_id, r.title, r.author_id, u.username,
	       r.source, r.byte_size, r.created_at`

// pageRevisionFullColumns is the SELECT used by the single-revision endpoint
// — adds the body column for the soft-draft / diff payload.
const pageRevisionFullColumns = `
	SELECT r.id, r.page_id, r.title, r.body, r.props, r.author_id, u.username,
	       r.source, r.byte_size, r.created_at`

// revisionQuerier is satisfied by both *sql.DB and *sql.Tx, so a revision can be
// snapshotted post-commit (the usual case) OR atomically inside a write tx — the
// latter for the pre-sync-overwrite snapshot that must be guaranteed never to be
// lost (see applySyncBound).
type revisionQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// insertPageRevision writes a new page_revisions row for pageID. byte_size is
// derived from len(body); created_at is set by tela_now() so the
// wire format matches the rest of the API. authorID is nullable; pass nil when
// the writer's user record is unavailable. Usually called from the
// snapshot-on-save hook AFTER the pages UPDATE has committed (pass s.DB) so a
// failure cannot roll the user's save back; the sync path passes its tx to
// snapshot the overwritten state atomically.
func insertPageRevision(ctx context.Context, db revisionQuerier, pageID int64, body, title string, props map[string]any, authorID *int64, source string) (int64, error) {
	var id int64
	err := db.QueryRowContext(ctx, `
		INSERT INTO page_revisions
		  (page_id, body, title, props, author_id, source, byte_size, created_at)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, tela_now()) RETURNING id`,
		pageID, body, title, propsJSON(props), nullableInt64(authorID), source, int64(len(body))).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// revisionPage is the page a revision read is scoped to. Unlike selectPageByID
// it includes TRASHED pages: a soft-deleted page keeps its revisions (only a
// purge cascades them away), and "what did this page say before it was removed"
// is exactly what history is for. Atlas prunes a page whose slug leaves the
// outline, and the only record of its generated text is here.
type revisionPage struct {
	ID                int64   `json:"id"`
	SpaceID           int64   `json:"space_id"`
	Title             string  `json:"title"`
	DeletedAt         *string `json:"deleted_at"`
	CurrentRevisionID *int64  `json:"current_revision_id"`
}

// revisionPageCore resolves pageID for a revision read and gates it: editor+ on
// the page's space (history exposes every past body, so it stays behind the same
// bar as the in-app history view). A missing page collapses to the same 403 a
// non-member sees, so ids can't be probed.
func (s *Server) revisionPageCore(ctx context.Context, u *auth.User, k *auth.APIKey, pageID int64) (revisionPage, *apiErr) {
	var (
		p       revisionPage
		deleted sql.NullString
		cur     sql.NullInt64
	)
	err := s.DB.QueryRowContext(ctx, `
		SELECT p.id, p.space_id, p.title, p.deleted_at,
		       (SELECT MAX(r.id) FROM page_revisions r WHERE r.page_id = p.id)
		  FROM pages p WHERE p.id = $1`, pageID).Scan(&p.ID, &p.SpaceID, &p.Title, &deleted, &cur)
	if errors.Is(err, sql.ErrNoRows) {
		return p, &apiErr{http.StatusForbidden, "forbidden", "not a member"}
	}
	if err != nil {
		return p, &apiErr{http.StatusInternalServerError, "internal", "lookup page failed"}
	}
	role, ae := s.membershipCore(ctx, u, k, p.SpaceID)
	if ae != nil {
		return p, ae
	}
	if !canEdit(role) {
		return p, &apiErr{http.StatusForbidden, "viewer_no_write", "editor or owner role required"}
	}
	if deleted.Valid {
		p.DeletedAt = &deleted.String
	}
	if cur.Valid {
		p.CurrentRevisionID = &cur.Int64
	}
	return p, nil
}

// revisionLimit applies the default (limit <= 0) and the max.
func revisionLimit(limit int64) int64 {
	if limit <= 0 {
		return pageRevisionsDefaultLimit
	}
	return min(limit, pageRevisionsMaxLimit)
}

// listPageRevisionsCore backs GET /api/pages/{id}/revisions and the MCP
// list_page_revisions tool: revisions newest first (id DESC), body excluded.
// cursor=0 starts from the latest; otherwise rows with id < cursor.
func (s *Server) listPageRevisionsCore(ctx context.Context, u *auth.User, k *auth.APIKey, pageID, cursor, limit int64) (revisionPage, []models.PageRevision, *apiErr) {
	page, ae := s.revisionPageCore(ctx, u, k, pageID)
	if ae != nil {
		return page, nil, ae
	}
	limit = revisionLimit(limit)
	rows, err := s.DB.QueryContext(ctx, pageRevisionListColumns+`
		  FROM page_revisions r
		  LEFT JOIN users u ON u.id = r.author_id
		 WHERE r.page_id = $1 AND ($2 = 0 OR r.id < $2)
		 ORDER BY r.id DESC
		 LIMIT $3`, pageID, cursor, limit)
	if err != nil {
		return page, nil, &apiErr{http.StatusInternalServerError, "internal", "list revisions failed"}
	}
	defer rows.Close()
	out := []models.PageRevision{}
	for rows.Next() {
		rev, err := scanPageRevisionList(rows)
		if err != nil {
			return page, nil, &apiErr{http.StatusInternalServerError, "internal", "scan revision row failed"}
		}
		out = append(out, rev)
	}
	if err := rows.Err(); err != nil {
		return page, nil, &apiErr{http.StatusInternalServerError, "internal", "iterate revisions failed"}
	}
	return page, out, nil
}

// getPageRevisionCore backs GET /api/pages/{id}/revisions/{rev_id} and the MCP
// get_page_revision tool: one revision's full title/body/props. A revision of a
// different page than pageID is not-found (don't leak existence).
func (s *Server) getPageRevisionCore(ctx context.Context, u *auth.User, k *auth.APIKey, pageID, revID int64) (revisionPage, models.PageRevision, *apiErr) {
	page, ae := s.revisionPageCore(ctx, u, k, pageID)
	if ae != nil {
		return page, models.PageRevision{}, ae
	}
	rev, err := scanPageRevisionFull(s.DB.QueryRowContext(ctx, pageRevisionFullColumns+`
		  FROM page_revisions r
		  LEFT JOIN users u ON u.id = r.author_id
		 WHERE r.id = $1 AND r.page_id = $2`, revID, pageID))
	if errors.Is(err, sql.ErrNoRows) {
		return page, rev, &apiErr{http.StatusNotFound, "revision_not_found", "revision not found"}
	}
	if err != nil {
		return page, rev, &apiErr{http.StatusInternalServerError, "internal", "fetch revision failed"}
	}
	return page, rev, nil
}

// ListPageRevisions returns a paginated list of revisions for a page,
// ordered by id DESC (newest first). Editor+ on the page's space.
func (s *Server) ListPageRevisions(w http.ResponseWriter, r *http.Request) {
	pageID, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	var cursor, limit int64
	if raw := q.Get("cursor"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "cursor must be a non-negative integer")
			return
		}
		cursor = v
	}
	if raw := q.Get("limit"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "limit must be a positive integer")
			return
		}
		limit = v
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	_, out, ae := s.listPageRevisionsCore(r.Context(), u, k, pageID, cursor, limit)
	if ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revisions": out})
}

// GetPageRevision returns a single revision's full body+title. Editor+ on the
// page's space required.
func (s *Server) GetPageRevision(w http.ResponseWriter, r *http.Request) {
	pageID, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	revID, ok := parseIDParam(w, r, "rev_id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	_, rev, ae := s.getPageRevisionCore(r.Context(), u, k, pageID, revID)
	if ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": rev})
}

func scanPageRevisionList(rows *sql.Rows) (models.PageRevision, error) {
	var rev models.PageRevision
	var (
		authorID   sql.NullInt64
		authorName sql.NullString
	)
	if err := rows.Scan(
		&rev.ID, &rev.PageID, &rev.Title, &authorID, &authorName,
		&rev.Source, &rev.ByteSize, &rev.CreatedAt,
	); err != nil {
		return rev, err
	}
	if authorID.Valid {
		v := authorID.Int64
		rev.AuthorID = &v
	}
	if authorName.Valid {
		v := authorName.String
		rev.AuthorUsername = &v
	}
	return rev, nil
}

func scanPageRevisionFull(row *sql.Row) (models.PageRevision, error) {
	var rev models.PageRevision
	var (
		authorID   sql.NullInt64
		authorName sql.NullString
		propsRaw   []byte
	)
	if err := row.Scan(
		&rev.ID, &rev.PageID, &rev.Title, &rev.Body, &propsRaw, &authorID, &authorName,
		&rev.Source, &rev.ByteSize, &rev.CreatedAt,
	); err != nil {
		return rev, err
	}
	rev.Props = map[string]any{}
	if len(propsRaw) > 0 {
		if err := json.Unmarshal(propsRaw, &rev.Props); err != nil {
			return rev, fmt.Errorf("scan revision props: %w", err)
		}
	}
	if authorID.Valid {
		v := authorID.Int64
		rev.AuthorID = &v
	}
	if authorName.Valid {
		v := authorName.String
		rev.AuthorUsername = &v
	}
	return rev, nil
}
