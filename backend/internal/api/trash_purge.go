package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/zcag/tela/backend/internal/auth"
)

// trash_purge.go — the only irreversible delete tela has for a page. Everything
// else about deleting is recoverable by design (trash.go); this is the escape
// hatch for when "recoverable" is the problem: a page whose very title was a
// mistake, or a bin that has grown into a record of everything you ever removed.
//
// Scoped exactly like restore: your own deletes, or anything in the space if you
// own it. A purge cascades to the page's sub-pages through the pages.parent_id
// FK — a child cannot outlive its parent — and every dependent table hangs off
// pages with ON DELETE CASCADE (revisions, chunks, comments, favourites, share
// links, Yjs state, sync_base, the Atlas map, attachments whose bytes live in
// space_files.data), so there is nothing left behind and no blob to collect.

// subtreeOf resolves a page and its descendants. parent_id is walked regardless
// of deleted_at, so it names the same set before and after a soft delete.
const subtreeOf = `
	WITH RECURSIVE subtree(id) AS (
		SELECT id FROM pages WHERE id = $1
		UNION ALL
		SELECT p.id FROM pages p JOIN subtree s ON p.parent_id = s.id
	)`

// PurgePage handles POST /api/pages/{id}/purge — destroy one trashed page for good.
func (s *Server) PurgePage(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	if ae := s.purgePageCore(r.Context(), u, k, id); ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) purgePageCore(ctx context.Context, u *auth.User, k *auth.APIKey, id int64) *apiErr {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "begin tx failed"}
	}
	defer tx.Rollback()

	var spaceID int64
	var title string
	var deletedBy sql.NullInt64
	err = tx.QueryRowContext(ctx,
		`SELECT space_id, title, deleted_by FROM pages WHERE id = $1 AND deleted_at IS NOT NULL`,
		id).Scan(&spaceID, &title, &deletedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return &apiErr{http.StatusNotFound, "not_found", "no deleted page with that id"}
	}
	if err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "lookup trashed page failed"}
	}
	if ae := s.requireEditTx(ctx, tx, u, k, spaceID); ae != nil {
		return ae
	}
	ae := s.mayActOnTrashedTx(ctx, tx, u, spaceID, deletedBy)
	if ae != nil {
		return ae
	}
	// The FK cascade would take a LIVE descendant with it without a word. That
	// state should be impossible — a soft delete takes the whole subtree, and
	// restoring a page under a trashed parent is refused — so this is a rail
	// against a future bug destroying content the caller never saw, not a case
	// anyone is expected to hit.
	var live int
	if err := tx.QueryRowContext(ctx, subtreeOf+`
		SELECT count(*) FROM pages
		 WHERE id IN (SELECT id FROM subtree) AND id <> $1 AND deleted_at IS NULL`, id).Scan(&live); err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "check live sub-pages failed"}
	}
	if live > 0 {
		return &apiErr{http.StatusConflict, "live_subpages", "this page still has live sub-pages — delete them first"}
	}

	if _, err := tx.ExecContext(ctx, subtreeOf+`
		DELETE FROM pages WHERE id IN (SELECT id FROM subtree)`, id); err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "purge page failed"}
	}
	if err := tx.Commit(); err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "commit failed"}
	}
	// The page row is gone, so this event is the only remaining trace — hence the
	// title is denormalised onto it, as eventInput does for every target.
	pid := id
	recordEvent(ctx, s.DB, eventInput{
		Type: evtPagePurge, ActorUserID: &u.ID, TargetKind: "page",
		TargetID: &pid, TargetLabel: title,
	})
	return nil
}

// EmptySpaceTrash handles DELETE /api/spaces/{id}/trash — purge everything the
// caller can see in the bin: their own deletes, or all of them for an owner.
func (s *Server) EmptySpaceTrash(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	k, _ := auth.APIKeyFromContext(r.Context())

	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "begin tx failed")
		return
	}
	defer tx.Rollback()

	if ae := s.requireEditTx(r.Context(), tx, u, k, id); ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	role, err := spaceRoleTx(r.Context(), tx, u.ID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "lookup membership failed")
		return
	}
	// Delete the ROOTS the caller can see; pages.parent_id ON DELETE CASCADE takes
	// each subtree. A sub-page someone else deleted separately goes with its
	// parent — a child cannot outlive the page it hangs off.
	res, err := tx.ExecContext(r.Context(), `
		WITH RECURSIVE trashed AS (
			SELECT id, parent_id, deleted_by FROM pages
			 WHERE space_id = $1 AND deleted_at IS NOT NULL
		),
		roots AS (
			SELECT t.* FROM trashed t
			 WHERE NOT EXISTS (SELECT 1 FROM trashed p WHERE p.id = t.parent_id)
		)
		DELETE FROM pages
		 WHERE id IN (SELECT id FROM roots WHERE $3 OR deleted_by = $2)`,
		id, u.ID, role == roleOwner)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "empty trash failed")
		return
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "commit failed")
		return
	}
	sid := id
	recordEvent(r.Context(), s.DB, eventInput{
		Type: evtPagePurge, ActorUserID: &u.ID, TargetKind: "space",
		TargetID: &sid, Detail: "empty_trash",
	})
	slog.Info("trash emptied", "space_id", id, "user_id", u.ID, "roots", n)
	writeJSON(w, http.StatusOK, map[string]any{"purged": n})
}

// mayActOnTrashedTx gates restore and purge identically: your own delete, or
// anything if you own the space. A page trashed before migration 0081 has no
// recorded actor, so only an owner can act on it. The refusal is a 404, not a
// 403 — a delete you are not allowed to see should not be confirmable by
// probing ids.
func (s *Server) mayActOnTrashedTx(ctx context.Context, tx *sql.Tx, u *auth.User, spaceID int64, deletedBy sql.NullInt64) *apiErr {
	role, err := spaceRoleTx(ctx, tx, u.ID, spaceID)
	if err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "lookup membership failed"}
	}
	if role != roleOwner && (!deletedBy.Valid || deletedBy.Int64 != u.ID) {
		return &apiErr{http.StatusNotFound, "not_found", "no deleted page with that id"}
	}
	return nil
}

// ── Optional retention sweep ───────────────────────────────────────────────
// OFF by default. A wiki's bin is a safety net, and a net that empties itself
// on a timer is how you lose the page you meant to come back for — so tela only
// ages the trash out when a deploy explicitly asks for it.

const trashGCInterval = 6 * time.Hour

// StartTrashGC launches the trash retention sweep when TELA_TRASH_RETENTION_DAYS
// is set to a positive integer; otherwise it logs that the bin is kept forever
// and returns. Mirrors StartEventsGC.
func StartTrashGC(ctx context.Context, d *sql.DB) {
	days := trashRetentionDays()
	if days <= 0 {
		slog.Info("trash: retention GC disabled — deleted pages are kept until purged by hand (set TELA_TRASH_RETENTION_DAYS to age them out)")
		return
	}
	slog.Info("trash: retention GC", "retention_days", days, "sweep_interval", trashGCInterval)
	go func() {
		sweep := func() {
			if err := purgeTrashOlderThan(ctx, d, days); err != nil {
				slog.Error("trash: GC sweep failed", "err", err)
			}
		}
		sweep()
		t := time.NewTicker(trashGCInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sweep()
			}
		}
	}()
}

func trashRetentionDays() int {
	v := os.Getenv("TELA_TRASH_RETENTION_DAYS")
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		slog.Warn("trash: ignoring invalid TELA_TRASH_RETENTION_DAYS, keeping the bin forever", "value", v)
		return 0
	}
	return n
}

// purgeTrashOlderThan destroys pages soft-deleted longer than days ago. Only
// ROOTS are matched — a sub-page's own deleted_at can be older than the delete
// that actually took it, and the FK cascade removes the subtree anyway.
func purgeTrashOlderThan(ctx context.Context, d *sql.DB, days int) error {
	if days <= 0 {
		return nil
	}
	res, err := d.ExecContext(ctx, `
		WITH RECURSIVE trashed AS (
			SELECT id, parent_id, deleted_at FROM pages WHERE deleted_at IS NOT NULL
		),
		roots AS (
			SELECT t.* FROM trashed t
			 WHERE NOT EXISTS (SELECT 1 FROM trashed p WHERE p.id = t.parent_id)
		)
		DELETE FROM pages
		 WHERE id IN (SELECT id FROM roots
		               WHERE deleted_at < to_char(now() at time zone 'utc' - make_interval(days => $1),
		                                          'YYYY-MM-DD HH24:MI:SS'))`, days)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("trash: retention sweep purged pages", "roots", n, "retention_days", days)
	}
	return nil
}
