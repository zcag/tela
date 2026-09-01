package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/zcag/tela/backend/internal/models"
	"github.com/zcag/tela/backend/internal/pagemd"
	"golang.org/x/net/webdav"
)

// dav_file.go: the webdav.File and os.FileInfo implementations the davFS hands
// back. Reads serve pagemd.Encode bytes; writes buffer and flush through
// ApplyFileSync on Close/Stat; directories enumerate the page tree.

// davTimeLayout is tela's canonical TEXT datetime ('YYYY-MM-DD HH:MM:SS' UTC,
// the SQLite-era format kept across the Postgres move). Parsed for getlastmodified.
const davTimeLayout = "2006-01-02 15:04:05"

func davModTime(ts string) time.Time {
	t, err := time.Parse(davTimeLayout, ts)
	if err != nil {
		return time.Unix(0, 0).UTC()
	}
	return t.UTC()
}

// davInfo is the os.FileInfo (plus webdav.ETager) for a node. For a page file it
// carries the page so Size/ETag/ModTime derive from the canonical Encode output;
// for directories and for the lightweight entries Readdir emits, page is nil and
// only Name/IsDir are meaningful (walkFS re-Stats each child for the real props).
type davInfo struct {
	name string
	dir  bool
	mod  time.Time
	page *models.Page
	enc  []byte     // memoised Encode(page) for Size; nil until first needed
	file *spaceFile // set for a non-markdown space_file node (mutually exclusive with page)
	// sync is the client-supplied mtime that still stands for this node (see
	// dav_mtime.go); "" → the row's own updated_at is reported.
	sync string
}

func (i *davInfo) Name() string { return i.name }
func (i *davInfo) IsDir() bool  { return i.dir }
func (i *davInfo) Sys() any     { return nil }

func (i *davInfo) Mode() os.FileMode {
	if i.dir {
		return os.ModeDir | 0o755
	}
	return 0o644
}

func (i *davInfo) ModTime() time.Time {
	if i.sync != "" {
		return davModTime(i.sync)
	}
	if i.page != nil {
		return davModTime(i.page.UpdatedAt)
	}
	if i.file != nil {
		return davModTime(i.file.updatedAt)
	}
	return i.mod
}

func (i *davInfo) Size() int64 {
	if i.file != nil {
		return i.file.size
	}
	if i.page == nil {
		return 0
	}
	if i.enc == nil {
		i.enc = pagemd.Encode(*i.page, canonicalBaseURL())
	}
	return int64(len(i.enc))
}

// ETag is a strong validator: the page id paired with its updated_at. Any write
// (body, title, props, move) bumps updated_at, and the served bytes are a pure
// function of the page, so this changes iff the content changes — letting rclone
// skip unchanged transfers without re-hashing. Non-page nodes fall back to
// webdav's ModTime/Size etag.
func (i *davInfo) ETag(_ context.Context) (string, error) {
	if i.file != nil {
		// Content-addressed: the bytes' sha256 changes iff the file changes, so
		// rclone skips unchanged transfers without re-hashing.
		return `"` + i.file.hash + `"`, nil
	}
	if i.page == nil {
		return "", webdav.ErrNotImplemented
	}
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, i.page.UpdatedAt)
	return `"p` + strconv.FormatInt(i.page.ID, 10) + `-` + digits + `"`, nil
}

// pageFileInfo builds a file FileInfo for a page under the given on-disk name
// (the deduped `<slug>.md` the resolver already computed for that sibling group).
func pageFileInfo(name string, p models.Page, syncMtime string) *davInfo {
	cp := p
	return &davInfo{name: name, page: &cp, sync: syncMtime}
}

func dirInfo(name string, mod time.Time) *davInfo {
	return &davInfo{name: name, dir: true, mod: mod}
}

// davReadFile serves a page's canonical markdown for GET. The bytes are the
// pagemd.Encode output (frontmatter view + pure body); a bytes.Reader gives the
// io.Seeker http.ServeContent needs for Range requests.
type davReadFile struct {
	info *davInfo
	rd   *bytes.Reader
}

func newDavReadFile(name string, p models.Page, syncMtime string) *davReadFile {
	enc := pagemd.Encode(p, canonicalBaseURL())
	cp := p
	info := &davInfo{name: name, page: &cp, enc: enc, sync: syncMtime}
	return &davReadFile{info: info, rd: bytes.NewReader(enc)}
}

// spaceFileInfo is the os.FileInfo for a stored non-markdown file (listing/Stat).
func spaceFileInfo(f spaceFile) *davInfo {
	cf := f
	return &davInfo{name: f.name, file: &cf, sync: f.syncMtime}
}

// newDavBlobFile serves a stored file's raw bytes for GET. Like davReadFile but
// the body is the blob, not encoded markdown.
func newDavBlobFile(f spaceFile, data []byte) *davReadFile {
	cf := f
	return &davReadFile{info: &davInfo{name: f.name, file: &cf, sync: f.syncMtime}, rd: bytes.NewReader(data)}
}

func (f *davReadFile) Read(p []byte) (int, error)                { return f.rd.Read(p) }
func (f *davReadFile) Seek(off int64, whence int) (int64, error) { return f.rd.Seek(off, whence) }
func (f *davReadFile) Stat() (os.FileInfo, error)                { return f.info, nil }
func (f *davReadFile) Write([]byte) (int, error)                 { return 0, os.ErrInvalid }
func (f *davReadFile) Close() error                              { return nil }
func (f *davReadFile) Readdir(int) ([]os.FileInfo, error) {
	return nil, errors.New("not a directory")
}

// davDir is a collection node (root, a space folder, or a page-as-folder). Its
// children are precomputed at open time; Readdir just hands them out.
type davDir struct {
	info     *davInfo
	children []os.FileInfo
	off      int
}

func (d *davDir) Read([]byte) (int, error)       { return 0, os.ErrInvalid }
func (d *davDir) Seek(int64, int) (int64, error) { return 0, os.ErrInvalid }
func (d *davDir) Write([]byte) (int, error)      { return 0, os.ErrInvalid }
func (d *davDir) Close() error                   { return nil }
func (d *davDir) Stat() (os.FileInfo, error)     { return d.info, nil }

// Readdir mirrors os.File: count<=0 returns every remaining entry; count>0
// returns the next batch and io.EOF once exhausted. walkFS calls Readdir(0).
func (d *davDir) Readdir(count int) ([]os.FileInfo, error) {
	if count <= 0 {
		rest := d.children[d.off:]
		d.off = len(d.children)
		return rest, nil
	}
	if d.off >= len(d.children) {
		return nil, io.EOF
	}
	end := d.off + count
	if end > len(d.children) {
		end = len(d.children)
	}
	batch := d.children[d.off:end]
	d.off = end
	return batch, nil
}

// davWriteFile buffers a PUT body and resolves it through ApplyFileSync — the
// shared id-binding sync kernel — when the handler finishes. handlePut calls
// Stat() BEFORE Close(), so the flush is triggered by whichever runs first and
// memoised: Stat returns the resulting page's info (correct ETag for the PUT
// response), Close is then a no-op. A flush error surfaces as the PUT failing.
type davWriteFile struct {
	fs       *davFS
	ctx      context.Context
	spaceID  int64
	parentID *int64
	filename string

	buf     bytes.Buffer
	tooBig  bool
	flushed bool
	result  models.Page
	mtime   string // the client mtime we accepted for the page, "" if none
	err     error
}

func (f *davWriteFile) Write(p []byte) (int, error) {
	if f.tooBig {
		return 0, os.ErrInvalid
	}
	if int64(f.buf.Len()+len(p)) > davFileMaxBytes() {
		f.tooBig = true
		f.err = errors.New("markdown file exceeds size limit")
		return 0, f.err
	}
	return f.buf.Write(p)
}

func (f *davWriteFile) flush() error {
	if f.flushed {
		return f.err
	}
	if f.tooBig {
		return f.err
	}
	f.flushed = true
	pr := davPrincipalFrom(f.ctx)
	p, action, ae := f.fs.s.ApplyFileSync(f.ctx, pr.u, pr.k, f.spaceID, f.parentID, f.filename, f.buf.Bytes())
	if ae != nil {
		f.err = ae
		return ae
	}
	f.result = p
	// Modtime write support: keep the client's mtime when its file is a faithful
	// copy of what we now serve, else let the page's updated_at stand so the
	// client pulls the canonical rendering (with its `id:`) down. dav_mtime.go.
	if st := davStateFrom(f.ctx); st != nil && !st.clientMtime.IsZero() &&
		davAcceptClientMtime(f.buf.Bytes(), p, action) {
		f.mtime = davMtimeStamp(st.clientMtime)
		stampPageSyncMtime(f.ctx, f.fs.s.DB, p.ID, f.mtime, p.UpdatedAt)
		st.respHeader.Set(davMtimeHeader, "accepted")
	}
	return nil
}

func (f *davWriteFile) Stat() (os.FileInfo, error) {
	if err := f.flush(); err != nil {
		return nil, err
	}
	// Name is unused by handlePut (it only reads the ETag); the page's own slug
	// is the best-effort display name.
	return pageFileInfo(mdSlugOr(f.result.Title, "page")+".md", f.result, f.mtime), nil
}

func (f *davWriteFile) Close() error                       { return f.flush() }
func (f *davWriteFile) Read([]byte) (int, error)           { return 0, os.ErrInvalid }
func (f *davWriteFile) Seek(int64, int) (int64, error)     { return 0, os.ErrInvalid }
func (f *davWriteFile) Readdir(int) ([]os.FileInfo, error) { return nil, os.ErrInvalid }

// davSpaceWriteFile buffers a PUT body for a non-markdown file and stores it as a
// space_file (by location) on flush. A size cap is enforced as bytes arrive so a
// huge upload can't pin memory or bloat the blob store: past it Write fails, the
// flush surfaces the error, and the PUT fails (the file is NOT silently dropped —
// that's the footgun this feature fixes). Stat-before-Close means flush runs once.
type davSpaceWriteFile struct {
	fs       *davFS
	ctx      context.Context
	spaceID  int64
	parentID *int64
	name     string

	buf     bytes.Buffer
	tooBig  bool
	flushed bool
	result  spaceFile
	err     error
}

func (f *davSpaceWriteFile) Write(p []byte) (int, error) {
	if f.tooBig {
		return 0, os.ErrInvalid
	}
	if int64(f.buf.Len()+len(p)) > davFileMaxBytes() {
		f.tooBig = true
		f.err = errors.New("file exceeds TELA_WEBDAV_FILE_MAX_BYTES")
		return 0, f.err
	}
	return f.buf.Write(p)
}

func (f *davSpaceWriteFile) flush() error {
	if f.flushed {
		return f.err
	}
	f.flushed = true
	if f.err != nil { // a size-cap failure recorded during Write
		return f.err
	}
	// Storage quota: charge only the net new bytes vs whatever lives at this
	// location, so an idempotent re-PUT (rclone) at the cap isn't blocked.
	var oldSize int64
	_ = f.fs.s.DB.QueryRowContext(f.ctx,
		`SELECT byte_size FROM space_files WHERE space_id = $1 AND COALESCE(parent_page_id, 0) = $2 AND name = $3 AND deleted_at IS NULL`,
		f.spaceID, parentKey(f.parentID), f.name).Scan(&oldSize)
	if ae := f.fs.s.checkStorageQuota(f.ctx, f.spaceID, int64(f.buf.Len())-oldSize); ae != nil {
		f.err = errors.New(ae.Message)
		return f.err
	}
	sf, err := upsertSpaceFile(f.ctx, f.fs.s.DB, f.spaceID, f.parentID, f.name, f.buf.Bytes())
	if err != nil {
		f.err = err
		return err
	}
	f.result = sf
	// A stored file round-trips byte-for-byte, so the client's copy is current by
	// construction — its mtime always stands (dav_mtime.go).
	if st := davStateFrom(f.ctx); st != nil && !st.clientMtime.IsZero() {
		f.result.syncMtime = davMtimeStamp(st.clientMtime)
		stampFileSyncMtime(f.ctx, f.fs.s.DB, sf.id, f.result.syncMtime, sf.updatedAt)
		st.respHeader.Set(davMtimeHeader, "accepted")
	}
	// Store-and-announce: a synced file is the fourth ingress (after editor, MCP
	// base64, handshake PUT) — enqueue indexing too. Unconditional: a no-op
	// re-sync reindexes cheaply (the per-chunk vector cache skips the embedder),
	// and the debounce coalesces rapid syncs.
	f.fs.s.rag.QueueReindexFile(sf.id)
	f.fs.s.summarize.QueueFile(sf.id)
	return nil
}

func (f *davSpaceWriteFile) Stat() (os.FileInfo, error) {
	if err := f.flush(); err != nil {
		return nil, err
	}
	return spaceFileInfo(f.result), nil
}

func (f *davSpaceWriteFile) Close() error                       { return f.flush() }
func (f *davSpaceWriteFile) Read([]byte) (int, error)           { return 0, os.ErrInvalid }
func (f *davSpaceWriteFile) Seek(int64, int) (int64, error)     { return 0, os.ErrInvalid }
func (f *davSpaceWriteFile) Readdir(int) ([]os.FileInfo, error) { return nil, os.ErrInvalid }

// davDiscardFile swallows a write to a path tela does not persist (a non-.md
// junk file — .DS_Store, ._*, *.swp, editor temp). Accepting it keeps OS-native
// clients from erroring, while creating no junk page (sync §14 hygiene). rclone
// should still exclude these via filters; this is the defensive backstop.
type davDiscardFile struct {
	name   string
	n      int64
	tooBig bool
}

func (f *davDiscardFile) Write(p []byte) (int, error) {
	if f.tooBig {
		return 0, os.ErrInvalid
	}
	if f.n+int64(len(p)) > davFileMaxBytes() {
		f.tooBig = true
		return 0, errors.New("junk file exceeds size limit")
	}
	f.n += int64(len(p))
	return len(p), nil
}
func (f *davDiscardFile) Close() error             { return nil }
func (f *davDiscardFile) Read([]byte) (int, error) { return 0, os.ErrInvalid }
func (f *davDiscardFile) Seek(int64, int) (int64, error) {
	return 0, os.ErrInvalid
}
func (f *davDiscardFile) Readdir(int) ([]os.FileInfo, error) { return nil, os.ErrInvalid }
func (f *davDiscardFile) Stat() (os.FileInfo, error) {
	return &davInfo{name: f.name, mod: time.Unix(0, 0).UTC()}, nil
}
