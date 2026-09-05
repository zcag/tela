package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/zcag/tela/backend/internal/auth"
	"github.com/zcag/tela/backend/internal/models"
)

// trash.go — the recoverable side of delete. Deleting a page soft-deletes it and
// its whole subtree (pages.go), which had always been reversible in principle
// and unreachable in practice: nothing listed a trashed page and nothing
// restored one but the sync resurrect path. That gap is why two sync bugs cost
// ten pages before anyone noticed — see docs/webdav-sync.md.
//
// Who sees what: your own deletes are yours to see and undo, and a space OWNER
// sees the whole bin. A shared space is the reason — the page you deleted was
// readable by every member (tela has no per-page permissions), but the *fact*
// that you deleted it, and the power to reverse it, should not be everyone's.
// Pages trashed before migration 0081 have no recorded actor, so they are
// visible to owners only.

// trashEntry is one deleted page offered for restore. Only the ROOT of each
// delete is listed — restoring it brings its subtree along, so listing the
// sub-pages separately would offer restores that are already implied.
type trashEntry struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	DeletedAt string `json:"deleted_at"`
	// Sub-pages this restore will bring back with it.
	SubPages int    `json:"sub_pages"`
	ParentID *int64 `json:"parent_id,omitempty"`
	// Where it used to hang, for telling two same-named pages apart. Empty for a
	// top-level page, and for one whose parent is itself gone for good.
	ParentTitle string `json:"parent_title,omitempty"`
	// Who removed it and through what — empty/unknown for pages trashed before
	// migration 0081. DeletedByYou lets the UI say "you" without the client
	// having to know its own user id.
	DeletedBy    string `json:"deleted_by,omitempty"`
	DeletedByYou bool   `json:"deleted_by_you"`
	DeletedVia   string `json:"deleted_via,omitempty"`
}

const trashListLimit = 200

// ListSpaceTrash handles GET /api/spaces/{id}/trash — deleted pages, newest
// first: your own, or all of them for a space owner. Restoring needs edit.
//
// A row is a root when its parent is NOT also in the bin. That reads the tree
// rather than deleted_root_id on purpose: pages trashed before migration 0080
// carry no root id, and they must still list correctly.
func (s *Server) ListSpaceTrash(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	role, ae := s.membershipCore(r.Context(), u, k, id)
	if ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}

	rows, err := s.DB.QueryContext(r.Context(), `
		WITH RECURSIVE trashed AS (
			SELECT id, parent_id, title, deleted_at, deleted_root_id, deleted_by, deleted_via
			  FROM pages WHERE space_id = $1 AND deleted_at IS NOT NULL
		),
		roots AS (
			SELECT t.* FROM trashed t
			 WHERE NOT EXISTS (SELECT 1 FROM trashed p WHERE p.id = t.parent_id)
		),
		fell_with(root, id) AS (
			SELECT r.id, r.id FROM roots r
			UNION ALL
			SELECT f.root, t.id FROM fell_with f JOIN trashed t ON t.parent_id = f.id
		)
		SELECT r.id, r.title, r.deleted_at, r.parent_id,
		       (SELECT count(*) FROM fell_with f JOIN trashed d ON d.id = f.id
		         WHERE f.root = r.id AND d.id <> r.id
		           AND (d.deleted_root_id = r.id
		                OR (d.deleted_root_id IS NULL AND d.deleted_at = r.deleted_at))),
		       COALESCE((SELECT par.title FROM pages par WHERE par.id = r.parent_id), ''),
		       COALESCE((SELECT COALESCE(NULLIF(us.display_name, ''), us.username)
		                   FROM users us WHERE us.id = r.deleted_by), ''),
		       COALESCE(r.deleted_by = $3, false),
		       COALESCE(r.deleted_via, '')
		  FROM roots r
		 WHERE $4 OR r.deleted_by = $3
		 ORDER BY r.deleted_at DESC, r.id DESC
		 LIMIT $2`, id, trashListLimit, u.ID, role == roleOwner)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "list trash failed")
		return
	}
	defer rows.Close()

	out := []trashEntry{}
	for rows.Next() {
		var e trashEntry
		var parent sql.NullInt64
		if err := rows.Scan(&e.ID, &e.Title, &e.DeletedAt, &parent, &e.SubPages,
			&e.ParentTitle, &e.DeletedBy, &e.DeletedByYou, &e.DeletedVia); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "scan trash row failed")
			return
		}
		if parent.Valid {
			pid := parent.Int64
			e.ParentID = &pid
		}
		out = append(out, e)
	}
	if rows.Err() != nil {
		writeError(w, http.StatusInternalServerError, "internal", "read trash failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pages": out})
}

// RestorePage handles POST /api/pages/{id}/restore.
func (s *Server) RestorePage(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	p, ae := s.restorePageCore(r.Context(), u, k, id)
	if ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"page": p})
}

// restorePageCore brings a trashed page back through the same cascade the sync
// resurrect path uses, so the two can't drift on what "restore" means.
func (s *Server) restorePageCore(ctx context.Context, u *auth.User, k *auth.APIKey, id int64) (models.Page, *apiErr) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "begin tx failed"}
	}
	defer tx.Rollback()

	var spaceID int64
	var deletedAt string
	var parentID, deletedBy sql.NullInt64
	err = tx.QueryRowContext(ctx,
		`SELECT space_id, deleted_at, parent_id, deleted_by FROM pages
		  WHERE id = $1 AND deleted_at IS NOT NULL`,
		id).Scan(&spaceID, &deletedAt, &parentID, &deletedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return models.Page{}, &apiErr{http.StatusNotFound, "not_found", "no deleted page with that id"}
	}
	if err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "lookup trashed page failed"}
	}
	if ae := s.requireEditTx(ctx, tx, u, k, spaceID); ae != nil {
		return models.Page{}, ae
	}
	// Mirror what the bin shows you: undo your own deletes, or anything at all if
	// you own the space. Shared with purge so the two can't gate differently.
	if ae := s.mayActOnTrashedTx(ctx, tx, u, spaceID, deletedBy); ae != nil {
		return models.Page{}, ae
	}
	// A page under a still-trashed parent would come back invisible — the tree
	// only walks live rows — so send the caller at the parent instead. This is
	// also why ListSpaceTrash offers roots only.
	if parentID.Valid {
		var parentTrashed bool
		if err := tx.QueryRowContext(ctx,
			`SELECT deleted_at IS NOT NULL FROM pages WHERE id = $1`, parentID.Int64).Scan(&parentTrashed); err == nil && parentTrashed {
			return models.Page{}, &apiErr{http.StatusConflict, "parent_deleted", "restore the parent page first — it will bring this one back with it"}
		}
	}

	if ae := resurrectCascadeTx(ctx, tx, id, deletedAt, spaceID); ae != nil {
		return models.Page{}, ae
	}
	p, err := selectPageByIDTx(ctx, tx, id)
	if err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "fetch restored page failed"}
	}
	if err := appendChangeLog(ctx, tx, spaceID, id, changeUpdated); err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "append change_log failed"}
	}
	if err := tx.Commit(); err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "commit failed"}
	}
	// The other half of the audit trail page.delete writes.
	pid := p.ID
	recordEvent(ctx, s.DB, eventInput{
		Type: evtPageRestore, ActorUserID: &u.ID, TargetKind: "page",
		TargetID: &pid, TargetLabel: p.Title,
	})
	return p, nil
}

// resurrectCascadeTx un-trashes the page AND every descendant that went down
// with it. deletePageCore stamps deleted_root_id on every row one delete takes,
// so that column names exactly the set to bring back — a descendant trashed
// separately, by its own delete, carries a different root and stays trashed.
// (deleted_at cannot stand in for this: tela_now() has second resolution, so two
// deletes in the same second are indistinguishable by timestamp. It is used only
// as the fallback for rows trashed before migration 0080, which have no root
// recorded and are otherwise unreachable — there the whole subtree really was
// stamped in one statement.)
//
// This is also what makes rclone bisync's conflict shape non-destructive. bisync
// resolves a both-sides edit as DELETE(loser) + PUT(winner) on `<page>.md`, and
// in tela's WebDAV projection a page with children is `<page>.md` NEXT TO a
// `<page>/` directory — so the DELETE cascades to sub-pages that are separate
// files the client never touched, while the PUT brought back only the page
// itself. Every sync conflict on a page with children silently trashed the whole
// subtree, and with no trash surface there was nothing to notice it in.
func resurrectCascadeTx(ctx context.Context, tx *sql.Tx, id int64, deletedAt string, spaceID int64) *apiErr {
	const subtreeCTE = `
		WITH RECURSIVE subtree(id) AS (
			SELECT id FROM pages WHERE id = $1
			UNION ALL
			SELECT p.id FROM pages p JOIN subtree s ON p.parent_id = s.id
		)`
	fail := func(msg string) *apiErr { return &apiErr{http.StatusInternalServerError, "internal", msg} }

	rows, err := tx.QueryContext(ctx, subtreeCTE+`
		UPDATE pages
		   SET deleted_at = NULL, deleted_root_id = NULL, deleted_by = NULL,
		       deleted_via = NULL, updated_at = tela_now()
		 WHERE id IN (SELECT id FROM subtree)
		   AND (id = $1 OR deleted_root_id = $1
		        OR (deleted_root_id IS NULL AND deleted_at = $2))
		 RETURNING id, body`, id, deletedAt)
	if err != nil {
		return fail("resurrect failed")
	}
	type restored struct {
		id   int64
		body string
	}
	var back []restored
	for rows.Next() {
		var r restored
		if err := rows.Scan(&r.id, &r.body); err != nil {
			rows.Close()
			return fail("resurrect: scan restored id failed")
		}
		back = append(back, r)
	}
	rerr := rows.Err()
	rows.Close() // must close before the next Exec on this tx (single-conn cursor)
	if rerr != nil {
		return fail("resurrect: read restored ids failed")
	}

	for _, r := range back {
		// The cascade HARD-deleted page_links for the whole subtree (that is the
		// documented deal: a trashed page stops contributing backlinks, and a
		// resurrect rebuilds them from the body). Rebuild every restored page's,
		// not just the root's, or the sub-pages come back with their outgoing
		// links gone.
		if err := syncPageLinks(ctx, tx, r.id, r.body); err != nil {
			return fail("resurrect: rebuild page_links failed")
		}
		// Feed the sub-pages so a polling sync client learns they are back — the
		// delete fed every one of them. The root is left to the caller, which
		// writes its own entry on the path that brought it here.
		if r.id != id {
			if err := appendChangeLog(ctx, tx, spaceID, r.id, changeUpdated); err != nil {
				return fail("resurrect: append change_log failed")
			}
		}
	}
	// Attachments were soft-deleted in the same instant; bring back that set too.
	if _, err := tx.ExecContext(ctx, subtreeCTE+`
		UPDATE space_files SET deleted_at = NULL
		 WHERE parent_page_id IN (SELECT id FROM subtree) AND deleted_at = $2`, id, deletedAt); err != nil {
		return fail("resurrect: restore attachments failed")
	}
	return nil
}
