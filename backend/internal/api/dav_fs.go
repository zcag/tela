package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/zcag/tela/backend/internal/models"
)

// dav_fs.go — the webdav.FileSystem over tela pages. The tree is virtual: a
// space is a top-level folder, a page is `<slug>.md`, and a page with children
// is ALSO a `<slug>/` folder (the sibling-folder layout in pagetree.go, shared
// with export.zip). Identity is never the path: writes flow through
// ApplyFileSync, which binds by frontmatter `id` (path is only placement).
//
// All per-op state (the authed principal + a memoised per-space tree cache) rides
// the request context as a *davReqState, so a single PROPFIND of a space is one
// indexed query, not one-per-node (the perf bar). The davFS value itself is
// stateless and shared across requests.

type davFS struct{ s *Server }

var (
	_ os.FileInfo = (*davInfo)(nil)
)

// davSplit cleans a prefix-stripped webdav name into path segments, rejecting
// traversal and empty interior segments. The root ("/" or "") yields nil, true.
func davSplit(name string) (segs []string, ok bool) {
	name = strings.Trim(name, "/")
	if name == "" {
		return nil, true
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return nil, false
		}
		segs = append(segs, seg)
	}
	return segs, true
}

func isMarkdownName(name string) bool {
	l := strings.ToLower(name)
	return strings.HasSuffix(l, ".md") || strings.HasSuffix(l, ".markdown")
}

// davTitleFromName strips a markdown extension to recover the human title a file
// rename implies. Used only by Rename (a `.md` rename = retitle).
func davTitleFromName(name string) string {
	l := strings.ToLower(name)
	for _, ext := range []string{".md", ".markdown"} {
		if strings.HasSuffix(l, ext) {
			return name[:len(name)-len(ext)]
		}
	}
	return name
}

func parentKey(parentID *int64) int64 {
	if parentID == nil {
		return rootParentKey
	}
	return *parentID
}

// fileDirParent resolves the DIRECTORY part of a non-markdown path (segs below
// the space, minus the filename) to the folder page a space_file would nest
// under: nil = the space root, else the page id of the containing folder. ok is
// false when the directory doesn't resolve to a page-folder (a missing or
// file-form interior segment) — i.e. there's no such location.
func fileDirParent(t *spaceTree, segs []string) (parentID *int64, ok bool) {
	dir := segs[1 : len(segs)-1] // drop the space folder and the filename
	if len(dir) == 0 {
		return nil, true // space root
	}
	p, isFile, found := t.resolve(dir)
	if !found || isFile {
		return nil, false
	}
	return &p.ID, true
}

// davMapErr turns a page-core *apiErr into the error shape webdav maps to a
// status: not-found → 404/409, forbidden/unauthorized → permission, else a
// generic 500. nil passes through.
func davMapErr(ae *apiErr) error {
	if ae == nil {
		return nil
	}
	switch ae.Status {
	case http.StatusNotFound:
		return os.ErrNotExist
	case http.StatusForbidden, http.StatusUnauthorized:
		return os.ErrPermission
	default:
		return errors.New(ae.Message)
	}
}

// space resolves a top-level folder name to a space the principal can reach.
func (fs *davFS) space(ctx context.Context, folder string) (davSpace, bool, error) {
	spaces, err := davStateFrom(ctx).spaces(ctx, fs.s.DB)
	if err != nil {
		return davSpace{}, false, err
	}
	for _, s := range spaces {
		if s.folder == folder {
			return s, true, nil
		}
	}
	return davSpace{}, false, nil
}

func (fs *davFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	segs, ok := davSplit(name)
	if !ok {
		return nil, os.ErrNotExist
	}
	if len(segs) == 0 {
		return dirInfo("/", time.Unix(0, 0).UTC()), nil
	}
	sp, found, err := fs.space(ctx, segs[0])
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, os.ErrNotExist
	}
	if len(segs) == 1 {
		return dirInfo(sp.folder, davModTime(sp.updatedAt)), nil
	}
	st := davStateFrom(ctx)
	t, err := st.tree(ctx, fs.s.DB, sp.id)
	if err != nil {
		return nil, err
	}
	leaf := segs[len(segs)-1]
	if p, isFile, found := t.resolve(segs[1:]); found {
		if isFile {
			return pageFileInfo(leaf, p, st.syncMtimes(ctx, fs.s.DB, sp.id)[p.ID]), nil
		}
		// Directory form: a page-as-collection is statable for ANY page (so MKCOL /
		// PUT into a leaf that is gaining its first child resolves), independent of
		// whether it currently has children.
		return dirInfo(leaf, davModTime(p.UpdatedAt)), nil
	}
	// Not a page — a stored non-markdown file at this location?
	if parentID, ok := fileDirParent(t, segs); ok {
		set, err := st.files(ctx, fs.s.DB, sp.id)
		if err != nil {
			return nil, err
		}
		if f, found := lookupInSet(set, parentID, leaf); found {
			return spaceFileInfo(f), nil
		}
	}
	return nil, os.ErrNotExist
}

func (fs *davFS) OpenFile(ctx context.Context, name string, flag int, _ os.FileMode) (webdavFile, error) {
	segs, ok := davSplit(name)
	if !ok {
		return nil, os.ErrNotExist
	}
	if flag&(os.O_WRONLY|os.O_RDWR) != 0 {
		return fs.openWrite(ctx, segs)
	}
	return fs.openRead(ctx, segs)
}

func (fs *davFS) openRead(ctx context.Context, segs []string) (webdavFile, error) {
	st := davStateFrom(ctx)
	if len(segs) == 0 { // root: spaces as folders
		spaces, err := st.spaces(ctx, fs.s.DB)
		if err != nil {
			return nil, err
		}
		kids := make([]os.FileInfo, 0, len(spaces))
		for _, s := range spaces {
			kids = append(kids, dirInfo(s.folder, davModTime(s.updatedAt)))
		}
		return &davDir{info: dirInfo("/", time.Unix(0, 0).UTC()), children: kids}, nil
	}
	sp, found, err := fs.space(ctx, segs[0])
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, os.ErrNotExist
	}
	t, err := st.tree(ctx, fs.s.DB, sp.id)
	if err != nil {
		return nil, err
	}
	fileSet, err := st.files(ctx, fs.s.DB, sp.id)
	if err != nil {
		return nil, err
	}
	if len(segs) == 1 { // space folder: its root pages + root files
		return fs.dirFromChildren(sp.folder, davModTime(sp.updatedAt), t, rootParentKey, fileSet), nil
	}
	leaf := segs[len(segs)-1]
	p, isFile, found := t.resolve(segs[1:])
	if !found {
		// Not a page — try a stored non-markdown file at this location.
		if parentID, ok := fileDirParent(t, segs); ok {
			if f, ok := lookupInSet(fileSet, parentID, leaf); ok {
				data, err := readSpaceFileData(ctx, fs.s.DB, f.id)
				if err != nil {
					return nil, err
				}
				return newDavBlobFile(f, data), nil
			}
		}
		return nil, os.ErrNotExist
	}
	if isFile {
		// Establish the merge base on FIRST download only (insert-if-absent), so a
		// page created in the app and edited locally before this client ever
		// uploaded it still merges instead of last-write-wins. We never OVERWRITE an
		// existing base on a read — stock clients issue HEAD/GET probes during their
		// bookkeeping, and overwriting would set base==current right before a PUT and
		// collapse the merge to LWW. Writes (PUT) own base updates thereafter.
		if pr := davPrincipalFrom(ctx); pr.k != nil {
			if err := insertSyncBaseIfAbsent(ctx, fs.s.DB, pr.k.ID, p.ID, p.Title, p.Body, p.Props); err != nil {
				slog.Error("dav: sync base seed on read", "page_id", p.ID, "err", err)
			}
		}
		return newDavReadFile(leaf, p, st.syncMtimes(ctx, fs.s.DB, sp.id)[p.ID]), nil
	}
	return fs.dirFromChildren(leaf, davModTime(p.UpdatedAt), t, p.ID, fileSet), nil
}

// dirFromChildren builds a collection whose entries are the page tree's children
// of parentKey: a `<slug>.md` file per child, plus a `<slug>/` folder for each
// child that itself has children. The file entries are lightweight (walkFS
// re-Stats every node for the authoritative props).
func (fs *davFS) dirFromChildren(name string, mod time.Time, t *spaceTree, parentKey int64, fileSet map[int64][]spaceFile) *davDir {
	group := t.children[parentKey]
	files := fileSet[parentKey]
	kids := make([]os.FileInfo, 0, len(group)+len(files))
	for _, c := range group {
		slug := t.slug[c.ID]
		kids = append(kids, &davInfo{name: slug + ".md"})
		// Present a page as a `<slug>/` collection when it holds child pages OR
		// stored files — else a folder page whose only contents are files would be
		// undiscoverable by a recursive tree walk (the client never descends into
		// it), so its files would never sync down.
		if t.hasChildren(c.ID) || len(fileSet[c.ID]) > 0 {
			kids = append(kids, &davInfo{name: slug, dir: true, mod: davModTime(c.UpdatedAt)})
		}
	}
	for _, f := range files { // stored non-markdown files at this level
		kids = append(kids, spaceFileInfo(f))
	}
	return &davDir{info: dirInfo(name, mod), children: kids}
}

func (fs *davFS) openWrite(ctx context.Context, segs []string) (webdavFile, error) {
	// A page must live inside a space (≥2 segments: space + filename). A write
	// above that is junk we swallow rather than error on.
	if len(segs) < 2 {
		return &davDiscardFile{name: lastSeg(segs)}, nil
	}
	filename := segs[len(segs)-1]
	if isSyncJunkName(filename) {
		// .DS_Store, ._*, *.swp, editor temp — accept-and-drop (sync §14). Checked
		// before the markdown split so AppleDouble shadows (._note.md) don't mint a
		// page either.
		return &davDiscardFile{name: filename}, nil
	}
	isMD := isMarkdownName(filename)
	sp, found, err := fs.space(ctx, segs[0])
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, os.ErrNotExist
	}
	var parentID *int64
	if len(segs) > 2 {
		t, err := davStateFrom(ctx).tree(ctx, fs.s.DB, sp.id)
		if err != nil {
			return nil, err
		}
		parent, isFile, ok := t.resolve(segs[1 : len(segs)-1])
		if !ok || isFile { // missing or non-collection parent → 409 conflict
			return nil, os.ErrNotExist
		}
		parentID = &parent.ID
	}
	if !isMD {
		// A real non-markdown file — persist it as a space_file (markdown stays a
		// page). This is the path that used to discard arbitrary files.
		return &davSpaceWriteFile{fs: fs, ctx: ctx, spaceID: sp.id, parentID: parentID, name: filename}, nil
	}
	return &davWriteFile{fs: fs, ctx: ctx, spaceID: sp.id, parentID: parentID, filename: filename}, nil
}

func (fs *davFS) Mkdir(ctx context.Context, name string, _ os.FileMode) error {
	segs, ok := davSplit(name)
	if !ok {
		return os.ErrNotExist
	}
	if len(segs) == 0 {
		return os.ErrExist // root
	}
	sp, found, err := fs.space(ctx, segs[0])
	if err != nil {
		return err
	}
	if !found {
		if len(segs) == 1 {
			// A new top-level folder mints a new space (mkdir ~/tela/foo). Guarded:
			// disabled by config, or with a space-pinned key, this stays refused.
			return fs.createSpaceFromMkcol(ctx, segs[0])
		}
		return os.ErrNotExist // nesting under a space that doesn't exist → 409
	}
	if len(segs) == 1 {
		return os.ErrExist // the space folder already exists
	}
	st := davStateFrom(ctx)
	t, err := st.tree(ctx, fs.s.DB, sp.id)
	if err != nil {
		return err
	}
	var parentID *int64
	if len(segs) > 2 {
		parent, isFile, ok := t.resolve(segs[1 : len(segs)-1])
		if !ok || isFile {
			return os.ErrNotExist
		}
		parentID = &parent.ID
	}
	target := segs[len(segs)-1]
	// If a page already occupies this folder name, MKCOL is a no-op success — the
	// page simply becomes (or already is) a child container. Otherwise mint an
	// empty container page; a later PUT of its `<name>.md` index binds to it (via
	// ApplyFileSync's path-fallback) and fills in the body/title.
	if _, exists := t.childBySlug(parentKey(parentID), target); exists {
		return nil
	}
	pr := davPrincipalFrom(ctx)
	_, ae := fs.s.createPageCore(ctx, pr.u, pr.k, pageCreateRequest{
		SpaceID: sp.id, ParentID: parentID, Title: target,
	}, false)
	return davMapErr(ae)
}

// createSpaceFromMkcol mints a new space from a root-level MKCOL (mkdir
// ~/tela/<folder>), owned by the caller. It uses the folder name as the slug
// verbatim when that's already slug-valid (so the folder round-trips with the
// exact name the client made), else lets createSpaceCore derive one. Guards:
// disabled via TELA_WEBDAV_CREATE_SPACES=0, and a space-pinned key (scoped to one
// space) can never mint others. A slug already taken by a space the caller can't
// see surfaces as "already exists". Space DELETE stays refused (RemoveAll), so
// this is intentionally asymmetric — create by mkdir, but never rm a space.
func (fs *davFS) createSpaceFromMkcol(ctx context.Context, folder string) error {
	if !davCreateSpacesEnabled() {
		return os.ErrPermission
	}
	pr := davPrincipalFrom(ctx)
	if pr.k != nil && pr.k.SpaceID != nil {
		return os.ErrPermission
	}
	slug := ""
	if slugValidRe.MatchString(folder) {
		slug = folder // round-trips at the exact folder name the client created
	}
	_, ae := fs.s.createSpaceCore(ctx, pr.u, folder, slug, nil)
	if ae != nil {
		if ae.Status == http.StatusConflict {
			return os.ErrExist // slug taken (possibly by a space the caller can't see)
		}
		return davMapErr(ae)
	}
	return nil
}

func (fs *davFS) RemoveAll(ctx context.Context, name string) error {
	segs, ok := davSplit(name)
	if !ok {
		return os.ErrNotExist
	}
	if len(segs) <= 1 {
		return os.ErrPermission // never delete the root or a whole space via WebDAV
	}
	sp, found, err := fs.space(ctx, segs[0])
	if err != nil {
		return err
	}
	if !found {
		return os.ErrNotExist
	}
	st := davStateFrom(ctx)
	t, err := st.tree(ctx, fs.s.DB, sp.id)
	if err != nil {
		return err
	}
	pr := davPrincipalFrom(ctx)
	p, _, ok := t.resolve(segs[1:]) // file or folder form both name the page
	if !ok {
		// Not a page — a stored non-markdown file? Soft-delete it (recoverable),
		// gated by the same mass-delete brake so a wiped local vault can't wipe a
		// space's files in one run.
		return fs.removeSpaceFile(ctx, st, sp, segs)
	}
	// Delete-safety (sync §6), WebDAV path only — an interactive app/MCP delete is
	// always honoured. (1) Cursor gate: a sync client may only delete a page it
	// has previously synced (a sync_base row proves it had the page); a page it
	// never had isn't a real removal, so refusing stops a partial clone / fresh
	// client from soft-deleting pages it never pulled. (2) Mass-delete brake:
	// refuse once an anomalous fraction of the space would vanish in a window.
	if pr.k != nil {
		had, err := hasSyncBase(ctx, fs.s.DB, pr.k.ID, p.ID)
		if err != nil {
			return err
		}
		if !had {
			slog.Warn("dav: refused DELETE of page — key never synced it", "page_id", p.ID, "key_id", pr.k.ID)
			return os.ErrPermission
		}
		live, err := countLiveSpacePages(ctx, fs.s.DB, sp.id)
		if err != nil {
			return err
		}
		if !fs.s.davDeletes.allow(pr.k.ID, sp.id, live) {
			slog.Warn("dav: refused DELETE of page — mass-delete guard tripped", "page_id", p.ID, "key_id", pr.k.ID, "space_id", sp.id)
			return os.ErrPermission
		}
	}
	return davMapErr(fs.s.deletePageCore(ctx, pr.u, pr.k, p.ID))
}

// countLiveSpacePages returns the number of live (non-deleted) pages in a space
// — the denominator the mass-delete brake measures the vanishing fraction against.
func countLiveSpacePages(ctx context.Context, db *sql.DB, spaceID int64) (int64, error) {
	var n int64
	err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM pages WHERE space_id = $1 AND deleted_at IS NULL`, spaceID).Scan(&n)
	return n, err
}

// removeSpaceFile soft-deletes the non-markdown file a DELETE path names (when it
// isn't a page). The mass-delete brake applies — sharing the page guard's window,
// measured against the live file count — so a wiped local vault can't wipe a
// space's files in one run. Deletes are soft, so they're recoverable regardless.
func (fs *davFS) removeSpaceFile(ctx context.Context, st *davReqState, sp davSpace, segs []string) error {
	t, err := st.tree(ctx, fs.s.DB, sp.id)
	if err != nil {
		return err
	}
	parentID, ok := fileDirParent(t, segs)
	if !ok {
		return os.ErrNotExist
	}
	set, err := st.files(ctx, fs.s.DB, sp.id)
	if err != nil {
		return err
	}
	f, ok := lookupInSet(set, parentID, segs[len(segs)-1])
	if !ok {
		return os.ErrNotExist
	}
	if pr := davPrincipalFrom(ctx); pr.k != nil {
		live, err := countLiveSpaceFiles(ctx, fs.s.DB, sp.id)
		if err != nil {
			return err
		}
		if !fs.s.davDeletes.allow(pr.k.ID, sp.id, live) {
			slog.Warn("dav: refused DELETE of file — mass-delete guard tripped", "file_id", f.id, "key_id", pr.k.ID, "space_id", sp.id)
			return os.ErrPermission
		}
	}
	return softDeleteSpaceFile(ctx, fs.s.DB, f.id)
}

func (fs *davFS) Rename(ctx context.Context, oldName, newName string) error {
	oseg, ok := davSplit(oldName)
	if !ok || len(oseg) <= 1 {
		return os.ErrPermission // can't move the root or a space
	}
	nseg, ok := davSplit(newName)
	if !ok || len(nseg) <= 1 {
		return os.ErrPermission
	}
	st := davStateFrom(ctx)
	osp, found, err := fs.space(ctx, oseg[0])
	if err != nil {
		return err
	}
	if !found {
		return os.ErrNotExist
	}
	ot, err := st.tree(ctx, fs.s.DB, osp.id)
	if err != nil {
		return err
	}
	nsp, found, err := fs.space(ctx, nseg[0])
	if err != nil {
		return err
	}
	if !found {
		return os.ErrNotExist
	}
	page, _, ok := ot.resolve(oseg[1:])
	if !ok {
		// Not a page — a stored non-markdown file move/rename (path-keyed, no title).
		return fs.renameSpaceFileAt(ctx, st, osp, oseg, nsp, nseg)
	}
	var newParentID *int64
	if len(nseg) > 2 {
		nt := ot
		if nsp.id != osp.id {
			if nt, err = st.tree(ctx, fs.s.DB, nsp.id); err != nil {
				return err
			}
		}
		parent, isFile, ok := nt.resolve(nseg[1 : len(nseg)-1])
		if !ok || isFile {
			return os.ErrNotExist
		}
		newParentID = &parent.ID
	}
	// Only a file-form rename (`<old>.md` → `<new>.md`) retitles; a folder rename
	// reparents/relocates without clobbering the title with a slug.
	newTitle := page.Title
	if isMarkdownName(oseg[len(oseg)-1]) && isMarkdownName(nseg[len(nseg)-1]) {
		newTitle = davTitleFromName(nseg[len(nseg)-1])
	}
	return fs.renameApply(ctx, page, nsp.id, newParentID, newTitle)
}

// renameApply commits a WebDAV MOVE as a retitle and/or reparent in one tx,
// reusing the shared in-tx page primitives (the same ones sync + REST use).
func (fs *davFS) renameApply(ctx context.Context, page models.Page, spaceID int64, parentID *int64, newTitle string) error {
	titleChanged := strings.TrimSpace(newTitle) != "" && newTitle != page.Title
	placementChanged := page.SpaceID != spaceID || !sameParent(page.ParentID, parentID)
	if !titleChanged && !placementChanged {
		return nil
	}
	pr := davPrincipalFrom(ctx)
	tx, err := fs.s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cur, err := selectPageByIDTx(ctx, tx, page.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return os.ErrNotExist
	}
	if err != nil {
		return err
	}
	if ae := fs.s.requireEditTx(ctx, tx, pr.u, pr.k, cur.SpaceID); ae != nil {
		return davMapErr(ae)
	}
	p := cur
	if titleChanged {
		up, ae := applyUpdateTx(ctx, tx, cur.ID, pageUpdateRequest{Title: &newTitle})
		if ae != nil {
			return davMapErr(ae)
		}
		p = up
	}
	if placementChanged {
		mp, ae := fs.s.applyMoveTx(ctx, tx, pr.u, pr.k, p, syncMoveParams(cur, spaceID, parentID))
		if ae != nil {
			return davMapErr(ae)
		}
		p = mp
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	fs.s.afterPageWrite(ctx, cur, p, false, true, pr.u.ID, "sync")
	return nil
}

// renameSpaceFileAt commits a WebDAV MOVE of a stored non-markdown file: relocate
// it to the new parent folder and/or rename it. A file can't cross into the page
// namespace, so the destination must also be non-markdown.
func (fs *davFS) renameSpaceFileAt(ctx context.Context, st *davReqState, osp davSpace, oseg []string, nsp davSpace, nseg []string) error {
	newName := nseg[len(nseg)-1]
	if isMarkdownName(newName) {
		return os.ErrPermission // a raw file can't become a page
	}
	ot, err := st.tree(ctx, fs.s.DB, osp.id)
	if err != nil {
		return err
	}
	oldParentID, ok := fileDirParent(ot, oseg)
	if !ok {
		return os.ErrNotExist
	}
	set, err := st.files(ctx, fs.s.DB, osp.id)
	if err != nil {
		return err
	}
	f, ok := lookupInSet(set, oldParentID, oseg[len(oseg)-1])
	if !ok {
		return os.ErrNotExist
	}
	nt := ot
	if nsp.id != osp.id {
		if nt, err = st.tree(ctx, fs.s.DB, nsp.id); err != nil {
			return err
		}
	}
	newParentID, ok := fileDirParent(nt, nseg)
	if !ok {
		return os.ErrNotExist
	}
	// The destination space is the new parent's space (or the space root's).
	return renameSpaceFileToSpace(ctx, fs.s.DB, f.id, nsp.id, newParentID, newName)
}

func lastSeg(segs []string) string {
	if len(segs) == 0 {
		return ""
	}
	return segs[len(segs)-1]
}
