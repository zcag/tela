package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/zcag/tela/backend/internal/auth"
	"golang.org/x/net/webdav"
)

// dav.go — the HTTP edge of the WebDAV sync surface (sync spec §7–9): PAT auth
// (stock clients send Basic, password = PAT), scope gating, and the per-request
// state the davFS reads off the context. The file tree itself lives in
// dav_fs.go; this file is the front door.
//
// The endpoint is on auth.IsPublicPath, so it bypasses the session/method-scope
// Middleware and self-authenticates here — mirroring /api/mcp. A WebDAV transport
// carries read AND write verbs (PROPFIND vs PUT/MOVE), and Basic-not-Bearer, so
// the generic Middleware can't gate it; we do scope here against the PAT.

const davPrefix = "/dav"

// webdavFile is the file type the FileSystem returns (http.File + io.Writer).
type webdavFile = webdav.File

// davPrincipal is the authenticated caller for one WebDAV request.
type davPrincipal struct {
	u *auth.User
	k *auth.APIKey
}

type davCtxKey struct{}

func withDavState(ctx context.Context, st *davReqState) context.Context {
	return context.WithValue(ctx, davCtxKey{}, st)
}

func davStateFrom(ctx context.Context) *davReqState {
	st, _ := ctx.Value(davCtxKey{}).(*davReqState)
	return st
}

func davPrincipalFrom(ctx context.Context) davPrincipal {
	if st := davStateFrom(ctx); st != nil {
		return st.pr
	}
	return davPrincipal{}
}

// davSpace is one space as a top-level WebDAV folder. folder is the deduped
// on-disk name (the space slug, uniquified across the caller's spaces).
type davSpace struct {
	id        int64
	folder    string
	updatedAt string
}

// davReqState is the per-request scratch the davFS shares across the many
// Stat/OpenFile calls one PROPFIND fans out into: the principal, the caller's
// accessible spaces (loaded once), and a per-space tree cache (each space loaded
// at most once). This is what keeps a deep PROPFIND a single query per space
// instead of one per node.
type davReqState struct {
	pr davPrincipal

	spacesCache []davSpace
	spacesErr   error
	spacesDone  bool

	trees    map[int64]*spaceTree
	fileSets map[int64]map[int64][]spaceFile
	mtimes   map[int64]map[int64]string

	// clientMtime is the X-OC-Mtime the write carried (zero when absent), and
	// respHeader is the response's header map so the write path can confirm the
	// stamp with `X-OC-Mtime: accepted`. See dav_mtime.go.
	clientMtime time.Time
	respHeader  http.Header
}

func (st *davReqState) spaces(ctx context.Context, db *sql.DB) ([]davSpace, error) {
	if !st.spacesDone {
		st.spacesDone = true
		st.spacesCache, st.spacesErr = loadDavSpaces(ctx, db, st.pr)
	}
	return st.spacesCache, st.spacesErr
}

func (st *davReqState) tree(ctx context.Context, db *sql.DB, spaceID int64) (*spaceTree, error) {
	if t, ok := st.trees[spaceID]; ok {
		return t, nil
	}
	t, err := newSpaceTree(ctx, db, spaceID)
	if err != nil {
		return nil, err
	}
	st.trees[spaceID] = t
	return t, nil
}

// files returns the space's live non-markdown files grouped by parent, loaded
// at most once per request (same once-per-space discipline as tree).
func (st *davReqState) files(ctx context.Context, db *sql.DB, spaceID int64) (map[int64][]spaceFile, error) {
	if set, ok := st.fileSets[spaceID]; ok {
		return set, nil
	}
	set, err := loadSpaceFiles(ctx, db, spaceID)
	if err != nil {
		return nil, err
	}
	st.fileSets[spaceID] = set
	return set, nil
}

// syncMtimes returns the space's live client-supplied mtime stamps (page id →
// stamp), loaded at most once per request like tree/files.
func (st *davReqState) syncMtimes(ctx context.Context, db *sql.DB, spaceID int64) map[int64]string {
	if m, ok := st.mtimes[spaceID]; ok {
		return m
	}
	m, err := loadSyncMtimes(ctx, db, spaceID)
	if err != nil {
		// A missing stamp only costs an extra transfer; never fail a listing on it.
		slog.Error("dav: load sync mtimes", "space_id", spaceID, "err", err)
		m = map[int64]string{}
	}
	st.mtimes[spaceID] = m
	return m
}

// loadDavSpaces lists the spaces the principal can reach as deduped folders,
// honouring a space-pinned PAT (it then exposes exactly that one space).
func loadDavSpaces(ctx context.Context, db *sql.DB, pr davPrincipal) ([]davSpace, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT s.id, s.slug, s.updated_at
		  FROM spaces s
		  JOIN (SELECT DISTINCT space_id FROM space_access WHERE user_id = $1) sa ON sa.space_id = s.id
		 ORDER BY s.slug ASC, s.id ASC`, pr.u.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pinned := pr.k != nil && pr.k.SpaceID != nil
	used := map[string]bool{}
	var out []davSpace
	for rows.Next() {
		var (
			id      int64
			slug    sql.NullString
			updated string
		)
		if err := rows.Scan(&id, &slug, &updated); err != nil {
			return nil, err
		}
		if pinned && *pr.k.SpaceID != id {
			continue
		}
		base := strings.TrimSpace(slug.String)
		if base == "" {
			base = "space-" + strconv.FormatInt(id, 10)
		}
		folder := base
		for n := 2; used[folder]; n++ {
			folder = base + "-" + strconv.Itoa(n)
		}
		used[folder] = true
		out = append(out, davSpace{id: id, folder: folder, updatedAt: updated})
	}
	return out, rows.Err()
}

// davEnabled gates the surface on TELA_WEBDAV_ENABLED. Default ON (the endpoint
// is PAT-gated + scope-checked); set it to 0/false/off/no to disable.
func davEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("TELA_WEBDAV_ENABLED"))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// davCreateSpacesEnabled gates whether a root-level MKCOL mints a new space
// (mkdir ~/tela/foo). Default ON; set TELA_WEBDAV_CREATE_SPACES to 0/false/off/no
// to keep space creation in-app only.
func davCreateSpacesEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("TELA_WEBDAV_CREATE_SPACES"))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// davScopeAllows maps the PAT scope to the WebDAV verb. read = the safe,
// non-mutating verbs only; write/admin = everything.
func davScopeAllows(scope, method string) bool {
	switch scope {
	case auth.ScopeWrite, auth.ScopeAdmin:
		return true
	case auth.ScopeRead:
		switch method {
		case http.MethodGet, http.MethodHead, http.MethodOptions, "PROPFIND":
			return true
		}
	}
	return false
}

// davToken pulls the PAT from a request: Basic-auth password (stock WebDAV
// clients — rclone, Finder, Windows) or an explicit Bearer header.
func davToken(r *http.Request) string {
	if _, pass, ok := r.BasicAuth(); ok && pass != "" {
		return pass
	}
	const bearer = "Bearer "
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, bearer) {
		return strings.TrimSpace(h[len(bearer):])
	}
	return ""
}

func davChallenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="tela webdav"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

func (s *Server) davAuthenticate(w http.ResponseWriter, r *http.Request) (*auth.User, *auth.APIKey, bool) {
	tok := davToken(r)
	if tok == "" {
		davChallenge(w)
		return nil, nil, false
	}
	k, err := auth.LookupAPIKey(r.Context(), s.DB, auth.LoadAPIKeySecret(), tok)
	if err != nil {
		davChallenge(w)
		return nil, nil, false
	}
	u, err := auth.UserForAPIKey(r.Context(), s.DB, k.UserID)
	if err != nil {
		davChallenge(w)
		return nil, nil, false
	}
	return u, k, true
}

// DAVHandler builds the WebDAV endpoint mounted at /dav. The x/net/webdav
// Handler drives the protocol (PROPFIND/MOVE/COPY/LOCK XML, depth, conditional
// headers) on top of our davFS + an in-memory lock system; we wrap it with PAT
// auth and scope gating and seed the per-request state.
func (s *Server) DAVHandler() http.Handler {
	fsys := &davFS{s: s}
	h := &webdav.Handler{
		Prefix:     davPrefix,
		FileSystem: fsys,
		LockSystem: webdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			// os.ErrNotExist is the normal "probe before create" 404 every client
			// does — not worth logging. Everything else is a real surprise.
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				slog.Error("webdav", "method", r.Method, "path", r.URL.Path, "err", err)
			}
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !davEnabled() {
			http.NotFound(w, r)
			return
		}
		u, k, ok := s.davAuthenticate(w, r)
		if !ok {
			return
		}
		if !davScopeAllows(k.Scope, r.Method) {
			http.Error(w, "insufficient api key scope", http.StatusForbidden)
			return
		}
		st := &davReqState{
			pr:          davPrincipal{u: u, k: k},
			trees:       map[int64]*spaceTree{},
			fileSets:    map[int64]map[int64][]spaceFile{},
			mtimes:      map[int64]map[int64]string{},
			clientMtime: davClientMtime(r),
			respHeader:  w.Header(),
		}
		r = r.WithContext(withDavState(r.Context(), st))
		// A DELETE of a path that is already gone is a success, not a 404. A
		// rename over rclone bisync arrives as PUT(new name) + DELETE(old name),
		// and the PUT — bound by the file's `id:` — has by then moved the page to
		// its new name, so the old path resolves to nothing. x/net/webdav would
		// 404 it, rclone counts that as a failed delete, and bisync aborts the
		// whole run with "critical error … must run --resync": a plain
		// `mv note.md journal.md` broke the vault until manual recovery. Deleting
		// nothing is idempotent; every delete guard still applies to a path that
		// DOES resolve.
		if r.Method == http.MethodDelete {
			if _, err := fsys.Stat(r.Context(), strings.TrimPrefix(r.URL.Path, davPrefix)); errors.Is(err, os.ErrNotExist) {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}
