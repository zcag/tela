package api

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"time"

	"github.com/zcag/tela/backend/internal/models"
	"github.com/zcag/tela/backend/internal/pagemd"
)

// dav_mtime.go — modtime write support for the WebDAV surface (migration 0079).
//
// THE PROBLEM. rclone decides what to transfer with equal(): size, then
// modtime. tela transforms markdown on write (renders frontmatter, may merge),
// so the stored bytes are not the uploaded bytes and the recipe must pass
// --ignore-size or every upload is rolled back as "corrupted on transfer". That
// leaves modtime as the only signal left — and a WebDAV server that cannot SET
// one reports Precision=ModTimeNotSupported, at which point rclone's equal()
// returns early on "Sizes identical" and calls EVERY already-existing file
// unchanged. Uploads of new files worked; every edit to a synced page was
// silently skipped (bisync queued the copy, the delegated copy refused it, the
// run exited 0 and advanced its listings, so it was never retried).
//
// THE FIX. Honour `X-OC-Mtime` on PUT — the owncloud header rclone sends when
// the remote is configured with vendor=rclone (also owncloud/nextcloud) — and
// serve it back as getlastmodified. Then rclone has a real modtime on both
// sides and its comparison is correct again. The vendor setting is load-bearing
// on the client: with vendor=other rclone sends no mtime and ignores ours.
//
// WHEN THE CLIENT'S MTIME IS ACCEPTED. Only when the client's file is a
// faithful copy of what tela now serves, i.e. either the served bytes are
// byte-identical to what was uploaded, or the upload changed nothing and the
// file already carries the page's `id:`. Otherwise the page's own updated_at
// stands, which is NEWER than the local file — so the client pulls the
// canonical rendering down on its next pass. That is what returns a
// server-assigned `id:` (identity, and therefore rename-safety) to a
// locally-created file, and what lands a merged body back on the machine that
// wrote half of it.

// davMtimeHeader is owncloud's de-facto "set the modification time to this"
// upload header (unix seconds). rclone sends it for vendor=rclone/owncloud/
// nextcloud/infinitescale/fastmail, and reads back `X-OC-Mtime: accepted` as
// confirmation that no PROPPATCH fallback is needed.
const davMtimeHeader = "X-OC-Mtime"

// davClientMtime parses X-OC-Mtime off a request. Zero time = not supplied (or
// unparseable, or absurd — a bogus stamp is worse than none, so the range is
// clamped to plausible file dates).
func davClientMtime(r *http.Request) time.Time {
	raw := r.Header.Get(davMtimeHeader)
	if raw == "" {
		return time.Time{}
	}
	secs, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || secs <= 0 || secs > 1<<40 {
		return time.Time{}
	}
	return time.Unix(secs, 0).UTC()
}

// davMtimeStamp renders a client mtime in tela's canonical TEXT datetime format
// (the same 'YYYY-MM-DD HH:MM:SS' UTC the timestamp columns use, so a stamped
// value is directly comparable to updated_at).
func davMtimeStamp(t time.Time) string { return t.UTC().Format(davTimeLayout) }

// davAcceptClientMtime reports whether the file the client just PUT is exactly
// what tela would now serve at that path — the condition for letting the
// client's mtime stand (see the file header). action/`p` are ApplyFileSync's
// result; raw is the uploaded body.
func davAcceptClientMtime(raw []byte, p models.Page, action syncAction) bool {
	if bytes.Equal(pagemd.Encode(p, canonicalBaseURL()), raw) {
		return true // the local file IS the canonical rendering
	}
	if action != syncUnchanged {
		return false // created, merged, moved or renamed → the client is behind
	}
	// Nothing changed server-side, but the byte view differs (a stale `updated:`,
	// an H1 the title was lifted from, CRLF…). Harmless — as long as the file
	// carries the page's identity, so a later rename rebinds instead of forking.
	d := pagemd.DecodeDoc(pagemd.NormalizeText(string(raw)))
	return d.ID != nil && *d.ID == p.ID
}

// stampPageSyncMtime records the client's mtime for a page, guarded on the
// updated_at it was observed against: a concurrent write that already moved the
// row makes this a no-op rather than masking that write from every sync client.
// Best-effort — a failed stamp only costs one extra transfer next cycle.
func stampPageSyncMtime(ctx context.Context, db *sql.DB, pageID int64, mtime, updatedAt string) {
	_, _ = db.ExecContext(ctx,
		`UPDATE pages SET sync_mtime = $1, sync_mtime_at = $2 WHERE id = $3 AND updated_at = $2`,
		mtime, updatedAt, pageID)
}

// stampFileSyncMtime is the space_files analogue. A stored file round-trips
// byte-for-byte, so there is no "is the client current" question — whatever it
// just uploaded IS what we serve.
func stampFileSyncMtime(ctx context.Context, db *sql.DB, fileID int64, mtime, updatedAt string) {
	_, _ = db.ExecContext(ctx,
		`UPDATE space_files SET sync_mtime = $1, sync_mtime_at = $2 WHERE id = $3 AND updated_at = $2`,
		mtime, updatedAt, fileID)
}

// loadSyncMtimes reads the still-valid page mtime stamps for a space (page id →
// stamp). Rows whose stamp predates the current updated_at are left out, so a
// caller can treat a hit as authoritative. Loaded once per request per space,
// like the page tree and the file set.
func loadSyncMtimes(ctx context.Context, db *sql.DB, spaceID int64) (map[int64]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, sync_mtime FROM pages
		 WHERE space_id = $1 AND deleted_at IS NULL
		   AND sync_mtime IS NOT NULL AND sync_mtime_at = updated_at`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var (
			id    int64
			stamp string
		)
		if err := rows.Scan(&id, &stamp); err != nil {
			return nil, err
		}
		out[id] = stamp
	}
	return out, rows.Err()
}
