package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/zcag/tela/backend/internal/auth"
	"github.com/zcag/tela/backend/internal/models"
)

const maxCommentBodyLen = 10_000

// commentThread bundles a root comment with its replies in created_at ASC
// order. GET /api/pages/{id}/comments returns []commentThread.
//
// The trailing fields belong to the SPACE-scoped listing (list_comments with
// space_id), which returns roots only — it is an inbox, not a transcript — so
// it reports how many replies a thread has instead of carrying them, plus the
// page context a caller has no other way to resolve. All omitempty, so the
// page-scoped REST shape is byte-identical to what it has always been.
type commentThread struct {
	Root       models.Comment   `json:"root"`
	Replies    []models.Comment `json:"replies"`
	ReplyCount *int             `json:"reply_count,omitempty"`
	PageTitle  string           `json:"page_title,omitempty"`
	SpaceID    int64            `json:"space_id,omitempty"`
}

// commentListOpts selects threads for listCommentsCore. Exactly one of PageID
// and SpaceID must be set.
type commentListOpts struct {
	PageID  int64
	SpaceID int64
	Status  string // "" / "open" (default) | "resolved" | "all"
	Since   string // opaque cursor from a previous call
	Limit   int    // threads (not comments), clamped to maxCommentListLimit
}

const (
	defaultCommentListLimit = 50
	maxCommentListLimit     = 200
)

type commentCreateRequest struct {
	Body         string  `json:"body"`
	ParentID     *int64  `json:"parent_id"`
	AnchorPrefix *string `json:"anchor_prefix"`
	AnchorExact  *string `json:"anchor_exact"`
	AnchorSuffix *string `json:"anchor_suffix"`
}

// commentPatchRequest is mutually exclusive: exactly one of Body / Resolved
// may be set. The handler 400s when both fields are present.
type commentPatchRequest struct {
	Body     *string `json:"body"`
	Resolved *bool   `json:"resolved"`
}

// ListComments returns all threads for a page. Viewers get 403 (the comments
// surface does not exist for viewers, per the M8 doctrine — not an empty
// array). Resolved threads are included only when ?include_resolved=true.
func (s *Server) ListComments(w http.ResponseWriter, r *http.Request) {
	pageID, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	status := "open"
	if r.URL.Query().Get("include_resolved") == "true" {
		status = "all"
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	threads, _, ae := s.listCommentsCore(r.Context(), u, k, commentListOpts{
		PageID: pageID, Status: status, Limit: maxCommentListLimit,
	})
	if ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"threads": threads})
}

// commentActivityKey is the sortable "<updated_at>|<zero-padded id>" token
// behind the opaque `since` cursor. updated_at is fixed-width UTC text
// ('YYYY-MM-DD HH:MM:SS'), so plain string ordering is chronological, and
// folding the id in keeps the tiebreak unambiguous when two comments land in
// the same second. Keying on updated_at rather than on the id alone is
// deliberate: an EDITED comment has to come back to a polling agent, and an
// id-only cursor can never see one.
func commentActivityKey(updatedAt string, id int64) string {
	return fmt.Sprintf("%s|%012d", updatedAt, id)
}

// sqlCommentActivity is commentActivityKey in SQL, for the space-scoped query.
// GREATEST folds the newest reply's key in, so answering a thread makes it
// resurface for a poller even though the root row never changed.
const sqlCommentActivity = `GREATEST(c.updated_at || '|' || lpad(c.id::text, 12, '0'), COALESCE(r.activity, ''))`

// threadActivity is the newest activity key anywhere in a thread.
func threadActivity(t commentThread) string {
	key := commentActivityKey(t.Root.UpdatedAt, t.Root.ID)
	for _, rep := range t.Replies {
		if k := commentActivityKey(rep.UpdatedAt, rep.ID); k > key {
			key = k
		}
	}
	return key
}

// commentStatusClause maps a validated status to its SQL predicate on a root.
func commentStatusClause(status string) string {
	switch status {
	case "open":
		return "c.resolved = 0"
	case "resolved":
		return "c.resolved = 1"
	default:
		return "TRUE"
	}
}

// listCommentsCore is the transport-agnostic core behind GET
// /api/pages/{id}/comments and the MCP list_comments tool. Editor+ on the
// space is required either way — comments do not exist for viewers. Returns
// the threads plus the cursor a caller passes back as Since to see only what
// has changed since.
func (s *Server) listCommentsCore(ctx context.Context, u *auth.User, k *auth.APIKey, opts commentListOpts) ([]commentThread, string, *apiErr) {
	status := opts.Status
	if status == "" {
		status = "open"
	}
	if status != "open" && status != "resolved" && status != "all" {
		return nil, "", &apiErr{http.StatusBadRequest, "bad_request", "status must be one of open, resolved, all"}
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultCommentListLimit
	}
	if limit > maxCommentListLimit {
		limit = maxCommentListLimit
	}
	if (opts.PageID == 0) == (opts.SpaceID == 0) {
		return nil, "", &apiErr{http.StatusBadRequest, "bad_request", "exactly one of page_id, space_id is required"}
	}
	if opts.PageID != 0 {
		return s.listPageComments(ctx, u, k, opts.PageID, status, opts.Since, limit)
	}
	return s.listSpaceComments(ctx, u, k, opts.SpaceID, status, opts.Since, limit)
}

// commentSpaceAccess is the comment surface's access rule, shared by every
// comment read: api-key space scope, membership, and editor+.
func (s *Server) commentSpaceAccess(ctx context.Context, u *auth.User, k *auth.APIKey, spaceID int64) *apiErr {
	if ae := apiKeySpaceScopeErr(k, spaceID); ae != nil {
		return ae
	}
	role, err := spaceRole(ctx, s.DB, u.ID, spaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return &apiErr{http.StatusForbidden, "forbidden", "not a member"}
	}
	if err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "lookup membership failed"}
	}
	if !canEdit(role) {
		return &apiErr{http.StatusForbidden, "forbidden", "editor or owner role required"}
	}
	return nil
}

// listPageComments returns one page's full threads (root + replies).
func (s *Server) listPageComments(ctx context.Context, u *auth.User, k *auth.APIKey, pageID int64, status, since string, limit int) ([]commentThread, string, *apiErr) {
	page, err := selectPageByID(ctx, s.DB, pageID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", &apiErr{http.StatusForbidden, "forbidden", "not a member"}
	}
	if err != nil {
		return nil, "", &apiErr{http.StatusInternalServerError, "internal", "lookup page failed"}
	}
	if ae := s.commentSpaceAccess(ctx, u, k, page.SpaceID); ae != nil {
		return nil, "", ae
	}

	rows, err := s.DB.QueryContext(ctx, commentSelectColumns+commentSelectFrom+`
		 WHERE c.page_id = $1 AND c.deleted_at IS NULL
		 ORDER BY c.created_at ASC, c.id ASC`, pageID)
	if err != nil {
		return nil, "", &apiErr{http.StatusInternalServerError, "internal", "list comments failed"}
	}
	defer rows.Close()

	all := []models.Comment{}
	for rows.Next() {
		c, err := scanCommentFromRows(rows)
		if err != nil {
			return nil, "", &apiErr{http.StatusInternalServerError, "internal", "scan comment row failed"}
		}
		all = append(all, c)
	}
	if err := rows.Err(); err != nil {
		return nil, "", &apiErr{http.StatusInternalServerError, "internal", "iterate comments failed"}
	}

	// Bucket replies onto their root. Replies whose root was soft-deleted fall
	// out (root row excluded by the WHERE above, so the lookup misses).
	// byRoot holds INDICES, not pointers: `threads` grows in this loop, and a
	// pointer taken before a reallocation writes into the discarded backing
	// array — which silently dropped the replies of every thread but the last.
	byRoot := map[int64]int{}
	threads := []commentThread{}
	for _, c := range all {
		if c.ParentID == nil {
			threads = append(threads, commentThread{Root: c, Replies: []models.Comment{}})
			byRoot[c.ID] = len(threads) - 1
		}
	}
	for _, c := range all {
		if c.ParentID == nil {
			continue
		}
		i, ok := byRoot[*c.ParentID]
		if !ok {
			continue
		}
		threads[i].Replies = append(threads[i].Replies, c)
	}

	// Status and cursor apply AFTER bucketing: a thread's activity includes its
	// replies, so a thread someone just answered comes back to a poller even
	// though its root row is untouched.
	out := []commentThread{}
	cursor := since
	for _, t := range threads {
		if (status == "open" && t.Root.Resolved) || (status == "resolved" && !t.Root.Resolved) {
			continue
		}
		act := threadActivity(t)
		if since != "" && act <= since {
			continue
		}
		if act > cursor {
			cursor = act
		}
		out = append(out, t)
		if len(out) >= limit {
			break
		}
	}
	return out, cursor, nil
}

// listSpaceComments returns an inbox of root comments across a whole space:
// roots only, each carrying its reply count and page title. Threads are not
// expanded — a space-wide call that inlined every reply would be enormous, and
// the caller can drill into one page once it knows which thread it wants.
func (s *Server) listSpaceComments(ctx context.Context, u *auth.User, k *auth.APIKey, spaceID int64, status, since string, limit int) ([]commentThread, string, *apiErr) {
	if ae := s.commentSpaceAccess(ctx, u, k, spaceID); ae != nil {
		return nil, "", ae
	}

	order := "c.created_at ASC, c.id ASC"
	if since != "" {
		order = "activity ASC"
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT c.id, p.title, COALESCE(r.reply_count, 0), `+sqlCommentActivity+` AS activity
		  FROM comments c
		  JOIN pages p ON p.id = c.page_id
		  LEFT JOIN LATERAL (
		    SELECT COUNT(*) AS reply_count,
		           MAX(rc.updated_at || '|' || lpad(rc.id::text, 12, '0')) AS activity
		      FROM comments rc
		     WHERE rc.parent_id = c.id AND rc.deleted_at IS NULL
		  ) r ON TRUE
		 WHERE p.space_id = $1 AND c.parent_id IS NULL
		   AND c.deleted_at IS NULL AND p.deleted_at IS NULL
		   AND `+commentStatusClause(status)+`
		   AND ($2 = '' OR `+sqlCommentActivity+` > $2)
		 ORDER BY `+order+`
		 LIMIT $3`, spaceID, since, limit)
	if err != nil {
		return nil, "", &apiErr{http.StatusInternalServerError, "internal", "list space comments failed"}
	}
	defer rows.Close()

	type rootMeta struct {
		id         int64
		title      string
		replyCount int
		activity   string
	}
	var metas []rootMeta
	cursor := since
	for rows.Next() {
		var m rootMeta
		if err := rows.Scan(&m.id, &m.title, &m.replyCount, &m.activity); err != nil {
			return nil, "", &apiErr{http.StatusInternalServerError, "internal", "scan space comment row failed"}
		}
		if m.activity > cursor {
			cursor = m.activity
		}
		metas = append(metas, m)
	}
	if err := rows.Err(); err != nil {
		return nil, "", &apiErr{http.StatusInternalServerError, "internal", "iterate space comments failed"}
	}
	if len(metas) == 0 {
		return []commentThread{}, cursor, nil
	}

	// Second pass for the full rows, so the comment scan stays single-sourced
	// through commentSelectColumns/scanCommentInto instead of being duplicated.
	ph := make([]string, len(metas))
	args := make([]any, len(metas))
	for i, m := range metas {
		ph[i] = fmt.Sprintf("$%d", i+1)
		args[i] = m.id
	}
	crows, err := s.DB.QueryContext(ctx, commentSelectColumns+commentSelectFrom+`
		 WHERE c.id IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return nil, "", &apiErr{http.StatusInternalServerError, "internal", "load space comments failed"}
	}
	defer crows.Close()
	byID := map[int64]models.Comment{}
	for crows.Next() {
		c, err := scanCommentFromRows(crows)
		if err != nil {
			return nil, "", &apiErr{http.StatusInternalServerError, "internal", "scan comment row failed"}
		}
		byID[c.ID] = c
	}
	if err := crows.Err(); err != nil {
		return nil, "", &apiErr{http.StatusInternalServerError, "internal", "iterate comments failed"}
	}

	out := make([]commentThread, 0, len(metas))
	for _, m := range metas {
		c, ok := byID[m.id]
		if !ok {
			continue
		}
		count := m.replyCount
		out = append(out, commentThread{
			Root:       c,
			Replies:    []models.Comment{},
			ReplyCount: &count,
			PageTitle:  m.title,
			SpaceID:    spaceID,
		})
	}
	return out, cursor, nil
}

// CreateComment inserts either a root (parent_id null, all three anchor_*
// required) or a reply (parent_id of a root in the same page, anchor_*
// ignored). Editor+ on the space required.
func (s *Server) CreateComment(w http.ResponseWriter, r *http.Request) {
	pageID, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	var req commentCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not parse request body")
		return
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	c, ae := s.createCommentCore(r.Context(), u, k, pageID, req)
	if ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"comment": c})
}

// createCommentCore is the transport-agnostic core behind POST
// /api/pages/{id}/comments and the MCP add_comment tool: inserts a root (all
// three anchor_* required) or a reply (parent_id of a root on the same page).
// Editor+ on the page's space required. The MCP tool only creates roots.
func (s *Server) createCommentCore(ctx context.Context, u *auth.User, k *auth.APIKey, pageID int64, req commentCreateRequest) (models.Comment, *apiErr) {
	body := strings.TrimSpace(req.Body)
	if body == "" {
		return models.Comment{}, &apiErr{http.StatusBadRequest, "bad_request", "body is required"}
	}
	if len(body) > maxCommentBodyLen {
		return models.Comment{}, &apiErr{http.StatusBadRequest, "bad_request", "body exceeds 10000 characters"}
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return models.Comment{}, &apiErr{http.StatusInternalServerError, "internal", "begin tx failed"}
	}
	defer tx.Rollback()

	page, err := selectPageByIDTx(ctx, tx, pageID)
	if errors.Is(err, sql.ErrNoRows) {
		return models.Comment{}, &apiErr{http.StatusForbidden, "forbidden", "not a member"}
	}
	if err != nil {
		return models.Comment{}, &apiErr{http.StatusInternalServerError, "internal", "lookup page failed"}
	}
	if ae := apiKeySpaceScopeErr(k, page.SpaceID); ae != nil {
		return models.Comment{}, ae
	}
	role, err := spaceRoleTx(ctx, tx, u.ID, page.SpaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return models.Comment{}, &apiErr{http.StatusForbidden, "forbidden", "not a member"}
	}
	if err != nil {
		return models.Comment{}, &apiErr{http.StatusInternalServerError, "internal", "lookup membership failed"}
	}
	if !canEdit(role) {
		return models.Comment{}, &apiErr{http.StatusForbidden, "forbidden", "editor or owner role required"}
	}

	isReply := req.ParentID != nil
	var (
		anchorPrefix, anchorExact, anchorSuffix any // sql NULL when reply
		parentAuthorID                          int64
	)

	if isReply {
		if *req.ParentID <= 0 {
			return models.Comment{}, &apiErr{http.StatusBadRequest, "bad_request", "parent_id must be a positive integer"}
		}
		var parentPageID int64
		var parentParentID sql.NullInt64
		var parentDeleted sql.NullString
		err := tx.QueryRowContext(ctx,
			`SELECT page_id, parent_id, deleted_at, author_id FROM comments WHERE id = $1`, *req.ParentID).
			Scan(&parentPageID, &parentParentID, &parentDeleted, &parentAuthorID)
		if errors.Is(err, sql.ErrNoRows) {
			return models.Comment{}, &apiErr{http.StatusNotFound, "comment_not_found", "parent comment not found"}
		}
		if err != nil {
			return models.Comment{}, &apiErr{http.StatusInternalServerError, "internal", "lookup parent comment failed"}
		}
		if parentDeleted.Valid {
			return models.Comment{}, &apiErr{http.StatusNotFound, "comment_not_found", "parent comment not found"}
		}
		if parentPageID != pageID {
			return models.Comment{}, &apiErr{http.StatusBadRequest, "bad_request", "parent comment belongs to a different page"}
		}
		if parentParentID.Valid {
			return models.Comment{}, &apiErr{http.StatusBadRequest, "comment_reply_to_reply", "replies must target a root comment"}
		}
	} else {
		if !anchorTriplePopulated(req.AnchorPrefix, req.AnchorExact, req.AnchorSuffix) {
			return models.Comment{}, &apiErr{http.StatusBadRequest, "comment_no_anchor", "root comments require anchor_prefix, anchor_exact, anchor_suffix"}
		}
		anchorPrefix = *req.AnchorPrefix
		anchorExact = *req.AnchorExact
		anchorSuffix = *req.AnchorSuffix
	}

	parentArg := any(nil)
	if isReply {
		parentArg = *req.ParentID
	}

	var id int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO comments
		  (page_id, parent_id, author_id, body,
		   anchor_prefix, anchor_exact, anchor_suffix,
		   resolved, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 0, tela_now(), tela_now()) RETURNING id`,
		pageID, parentArg, u.ID, body, anchorPrefix, anchorExact, anchorSuffix).Scan(&id)
	if err != nil {
		return models.Comment{}, &apiErr{http.StatusInternalServerError, "internal", "create comment failed"}
	}
	c, err := selectCommentByIDTx(ctx, tx, id)
	if err != nil {
		return models.Comment{}, &apiErr{http.StatusInternalServerError, "internal", "fetch created comment failed"}
	}
	if err := tx.Commit(); err != nil {
		return models.Comment{}, &apiErr{http.StatusInternalServerError, "internal", "commit failed"}
	}
	// Commenting is a strong "I care about this page" signal — auto-follow it so
	// the commenter hears about later changes (Confluence-style autowatch).
	s.autoFollow(ctx, u.ID, pageID)
	// Notify the root comment's author that someone replied (best-effort).
	if isReply {
		s.notifyCommentReply(ctx, u, pageID, parentAuthorID, body)
	}
	return c, nil
}

// PatchComment handles two mutually-exclusive operations on a comment:
//
//  1. {body: "..."} — author-only edit of the comment text.
//  2. {resolved: bool} — editor+ on the page's space toggles the resolved
//     flag. Only valid on root comments; flipping the same value twice
//     returns 409 comment_already_resolved.
//
// Sending both fields in one request returns 400 bad_request.
func (s *Server) PatchComment(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	var req commentPatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not parse request body")
		return
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	c, ae := s.patchCommentCore(r.Context(), u, k, id, req)
	if ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"comment": c})
}

// patchCommentCore is the transport-agnostic core behind PATCH
// /api/comments/{id} and the MCP update_comment tool. Exactly one of
// req.Body / req.Resolved must be set.
func (s *Server) patchCommentCore(ctx context.Context, u *auth.User, k *auth.APIKey, id int64, req commentPatchRequest) (models.Comment, *apiErr) {
	switch {
	case req.Body != nil && req.Resolved != nil:
		return models.Comment{}, &apiErr{http.StatusBadRequest, "bad_request", "body and resolved cannot be set in the same request"}
	case req.Body == nil && req.Resolved == nil:
		return models.Comment{}, &apiErr{http.StatusBadRequest, "bad_request", "one of body, resolved must be provided"}
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return models.Comment{}, &apiErr{http.StatusInternalServerError, "internal", "begin tx failed"}
	}
	defer tx.Rollback()

	var (
		pageID    int64
		authorID  int64
		parentID  sql.NullInt64
		resolved  int
		deletedAt sql.NullString
	)
	err = tx.QueryRowContext(ctx,
		`SELECT page_id, author_id, parent_id, resolved, deleted_at FROM comments WHERE id = $1`, id).
		Scan(&pageID, &authorID, &parentID, &resolved, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return models.Comment{}, &apiErr{http.StatusNotFound, "comment_not_found", "comment not found"}
	}
	if err != nil {
		return models.Comment{}, &apiErr{http.StatusInternalServerError, "internal", "lookup comment failed"}
	}
	if deletedAt.Valid {
		return models.Comment{}, &apiErr{http.StatusNotFound, "comment_not_found", "comment not found"}
	}

	page, err := selectPageByIDTx(ctx, tx, pageID)
	if err != nil {
		return models.Comment{}, &apiErr{http.StatusInternalServerError, "internal", "lookup parent page failed"}
	}
	if ae := apiKeySpaceScopeErr(k, page.SpaceID); ae != nil {
		return models.Comment{}, ae
	}
	role, err := spaceRoleTx(ctx, tx, u.ID, page.SpaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return models.Comment{}, &apiErr{http.StatusForbidden, "forbidden", "not a member"}
	}
	if err != nil {
		return models.Comment{}, &apiErr{http.StatusInternalServerError, "internal", "lookup membership failed"}
	}
	if !canEdit(role) {
		return models.Comment{}, &apiErr{http.StatusForbidden, "forbidden", "editor or owner role required"}
	}

	switch {
	case req.Body != nil:
		if authorID != u.ID {
			return models.Comment{}, &apiErr{http.StatusForbidden, "forbidden", "only the author can edit a comment"}
		}
		body := strings.TrimSpace(*req.Body)
		if body == "" {
			return models.Comment{}, &apiErr{http.StatusBadRequest, "bad_request", "body is required"}
		}
		if len(body) > maxCommentBodyLen {
			return models.Comment{}, &apiErr{http.StatusBadRequest, "bad_request", "body exceeds 10000 characters"}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE comments SET body = $1, updated_at = tela_now() WHERE id = $2`,
			body, id); err != nil {
			return models.Comment{}, &apiErr{http.StatusInternalServerError, "internal", "update comment failed"}
		}
	case req.Resolved != nil:
		if parentID.Valid {
			return models.Comment{}, &apiErr{http.StatusBadRequest, "bad_request", "resolve can only be set on root comments"}
		}
		desired := 0
		if *req.Resolved {
			desired = 1
		}
		if resolved == desired {
			if desired == 1 {
				return models.Comment{}, &apiErr{http.StatusConflict, "comment_already_resolved", "comment is already resolved"}
			}
			return models.Comment{}, &apiErr{http.StatusConflict, "comment_already_resolved", "comment is already open"}
		}
		if desired == 1 {
			if _, err := tx.ExecContext(ctx, `
				UPDATE comments
				   SET resolved = 1, resolved_at = tela_now(), resolved_by = $1,
				       updated_at = tela_now()
				 WHERE id = $2`, u.ID, id); err != nil {
				return models.Comment{}, &apiErr{http.StatusInternalServerError, "internal", "resolve comment failed"}
			}
		} else {
			if _, err := tx.ExecContext(ctx, `
				UPDATE comments
				   SET resolved = 0, resolved_at = NULL, resolved_by = NULL,
				       updated_at = tela_now()
				 WHERE id = $1`, id); err != nil {
				return models.Comment{}, &apiErr{http.StatusInternalServerError, "internal", "reopen comment failed"}
			}
		}
	}

	c, err := selectCommentByIDTx(ctx, tx, id)
	if err != nil {
		return models.Comment{}, &apiErr{http.StatusInternalServerError, "internal", "fetch updated comment failed"}
	}
	if err := tx.Commit(); err != nil {
		return models.Comment{}, &apiErr{http.StatusInternalServerError, "internal", "commit failed"}
	}
	return c, nil
}

// DeleteComment soft-deletes a comment (sets deleted_at). The author may
// always delete their own; a space owner may delete any. Other editors of
// the space cannot delete comments authored by someone else.
func (s *Server) DeleteComment(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "begin tx failed")
		return
	}
	defer tx.Rollback()

	var (
		pageID    int64
		authorID  int64
		deletedAt sql.NullString
	)
	err = tx.QueryRowContext(ctx,
		`SELECT page_id, author_id, deleted_at FROM comments WHERE id = $1`, id).
		Scan(&pageID, &authorID, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "comment_not_found", "comment not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "lookup comment failed")
		return
	}
	if deletedAt.Valid {
		writeError(w, http.StatusNotFound, "comment_not_found", "comment not found")
		return
	}

	page, err := selectPageByIDTx(ctx, tx, pageID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "lookup parent page failed")
		return
	}
	if !enforceAPIKeySpaceScope(w, r, page.SpaceID) {
		return
	}
	role, err := spaceRoleTx(ctx, tx, u.ID, page.SpaceID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusForbidden, "forbidden", "not a member")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "lookup membership failed")
		return
	}
	if !canEdit(role) {
		writeError(w, http.StatusForbidden, "forbidden", "editor or owner role required")
		return
	}
	// Author always allowed; otherwise only space owners.
	if authorID != u.ID && role != roleOwner {
		writeError(w, http.StatusForbidden, "forbidden", "only the author or a space owner can delete a comment")
		return
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE comments SET deleted_at = tela_now(), updated_at = tela_now() WHERE id = $1`,
		id); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "delete comment failed")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "commit failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// commentSelectColumns is the shared SELECT prefix used by every comment read
// path so scanCommentInto's column order is single-sourced; commentSelectFrom
// is the joins those columns need, so the two can't drift apart either.
const commentSelectColumns = `
	SELECT c.id, c.page_id, c.parent_id, c.author_id, author.username,
	       c.body, c.anchor_prefix, c.anchor_exact, c.anchor_suffix,
	       c.resolved, c.resolved_at, c.resolved_by, resolver.username,
	       c.created_at, c.updated_at`

const commentSelectFrom = `
	  FROM comments c
	  JOIN users author ON author.id = c.author_id
	  LEFT JOIN users resolver ON resolver.id = c.resolved_by`

func selectCommentByIDTx(ctx context.Context, tx *sql.Tx, id int64) (models.Comment, error) {
	row := tx.QueryRowContext(ctx, commentSelectColumns+commentSelectFrom+`
		 WHERE c.id = $1`, id)
	return scanCommentFromRow(row)
}

func selectCommentByID(ctx context.Context, db *sql.DB, id int64) (models.Comment, error) {
	row := db.QueryRowContext(ctx, commentSelectColumns+commentSelectFrom+`
		 WHERE c.id = $1`, id)
	return scanCommentFromRow(row)
}

func scanCommentFromRow(row *sql.Row) (models.Comment, error) {
	return scanCommentInto(row)
}

func scanCommentFromRows(rows *sql.Rows) (models.Comment, error) {
	return scanCommentInto(rows)
}

func scanCommentInto(r rowScanner) (models.Comment, error) {
	var c models.Comment
	var (
		parentID     sql.NullInt64
		anchorPrefix sql.NullString
		anchorExact  sql.NullString
		anchorSuffix sql.NullString
		resolvedInt  int
		resolvedAt   sql.NullString
		resolvedBy   sql.NullInt64
		resolverName sql.NullString
	)
	if err := r.Scan(
		&c.ID, &c.PageID, &parentID, &c.AuthorID, &c.AuthorName,
		&c.Body, &anchorPrefix, &anchorExact, &anchorSuffix,
		&resolvedInt, &resolvedAt, &resolvedBy, &resolverName,
		&c.CreatedAt, &c.UpdatedAt,
	); err != nil {
		return c, err
	}
	if parentID.Valid {
		v := parentID.Int64
		c.ParentID = &v
	}
	if anchorPrefix.Valid {
		v := anchorPrefix.String
		c.AnchorPrefix = &v
	}
	if anchorExact.Valid {
		v := anchorExact.String
		c.AnchorExact = &v
	}
	if anchorSuffix.Valid {
		v := anchorSuffix.String
		c.AnchorSuffix = &v
	}
	c.Resolved = resolvedInt != 0
	if resolvedAt.Valid {
		v := resolvedAt.String
		c.ResolvedAt = &v
	}
	if resolvedBy.Valid {
		v := resolvedBy.Int64
		c.ResolvedBy = &v
	}
	if resolverName.Valid {
		v := resolverName.String
		c.ResolvedName = &v
	}
	return c, nil
}

// anchorTriplePopulated returns true when all three pointers are non-nil and
// the exact slice is non-empty. Empty exact would be a zero-length selection
// — the FE must guard, but the backend rejects it defensively here.
func anchorTriplePopulated(prefix, exact, suffix *string) bool {
	return prefix != nil && exact != nil && suffix != nil && *exact != ""
}
