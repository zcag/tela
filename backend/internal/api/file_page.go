package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// file_page.go — /f/{hash}/{name}: the human-facing URL for a stored file.
//
// /api/files/{space}/{64-hex}.pdf is the BLOB. It forces a download (see
// attachments.go — anything but a raster image must not render from our origin),
// carries no title, and unfurls as nothing anywhere it is pasted. This is the
// link to actually share: a short content hash plus the real filename, resolving
// to a page that PREVIEWS the file (pdf.js / images) in tela chrome, with a
// crawler card behind it.
//
// Bot-gated exactly like /share and /public: Caddy routes crawler UAs here and
// humans fall through to the SPA at the same path (routes/file.tsx). A non-bot
// reaching this handler means the gate is missing, and 404 is the safe answer —
// better a visible 404 than serving the OG envelope in place of the app.
//
// Disclosure: the blob is already public-by-URL (an unguessable capability URL),
// so filename + type + size add nothing new. The parent PAGE is named only when
// its space is public — otherwise the card would hand a private page's title,
// and the file's auto-summary, to every unfurler that ever sees the link.

// fileHashShortLen is how much of the content hash the pretty URL carries. 12
// hex (48 bits) is git-short-sha territory: unambiguous in any realistic store,
// short enough to read. Mirrored by fileShareUrl() in the frontend.
const fileHashShortLen = 12

// sharedFile is one file resolved for the share surfaces (the /f page, its card,
// and the /api/files crawler retrofit).
type sharedFile struct {
	hash        string
	name        string
	mime        string
	size        int64
	spaceID     int64
	spaceName   string
	spacePublic bool
	ownerOrgID  int64
	pageID      int64
	pageTitle   string
	summary     string
}

func (f sharedFile) short() string {
	if len(f.hash) <= fileHashShortLen {
		return f.hash
	}
	return f.hash[:fileHashShortLen]
}

// pagePath is the canonical shareable path. The name is decorative — the hash
// prefix resolves the file — so a re-uploaded/renamed copy never breaks a link
// that is already out in the world.
func (f sharedFile) pagePath() string { return fileSharePath(f.hash, f.name) }

// fileSharePath builds /f/{hash12}/{name} from a full content hash. The ONE
// definition of the shareable path: attachment payloads (REST + MCP) carry it
// server-computed, so no client re-derives it — the same rule public_path
// follows for pages.
func fileSharePath(hash, name string) string {
	if len(hash) > fileHashShortLen {
		hash = hash[:fileHashShortLen]
	}
	p := "/f/" + hash
	if name != "" {
		p += "/" + url.PathEscape(name)
	}
	return p
}

func (f sharedFile) blobPath() string { return spaceFileServeURL(f.spaceID, f.name, f.hash) }

// kind is the short human label for the file type: the extension when there is
// one ("PDF", "XLSX"), else the mime's subtype. Used as the card kicker.
func (f sharedFile) kind() string {
	if ext := strings.TrimPrefix(path.Ext(f.name), "."); ext != "" {
		return strings.ToUpper(ext)
	}
	if _, sub, ok := strings.Cut(f.mime, "/"); ok && sub != "" {
		return strings.ToUpper(sub)
	}
	return "File"
}

// meta is the one-line "PDF · 46 KB" descriptor.
func (f sharedFile) meta() string { return f.kind() + " · " + humanBytes(f.size) }

// humanBytes formats a byte count for a card/description. Mirrors the
// frontend's prettySize so a file reads the same in the app and in an unfurl.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	kb := float64(n) / unit
	if kb < unit {
		if kb < 10 {
			return fmt.Sprintf("%.1f KB", kb)
		}
		return fmt.Sprintf("%.0f KB", kb)
	}
	mb := kb / unit
	if mb < 10 {
		return fmt.Sprintf("%.1f MB", mb)
	}
	return fmt.Sprintf("%.0f MB", mb)
}

// lookupSharedFile resolves a content-hash PREFIX (12–64 lowercase hex) to a live
// file. sql.ErrNoRows when nothing matches or the prefix is malformed.
//
// The prefix is expressed as a RANGE rather than LIKE so it uses the
// text_pattern_ops index (migration 0082) on any collation; 'g' is the upper
// bound because hex digits stop at 'f'.
func lookupSharedFile(ctx context.Context, db *sql.DB, prefix string) (sharedFile, error) {
	prefix = strings.ToLower(prefix)
	// At least the shareable prefix (fileHashShortLen, what every /f link
	// carries): this resolves files in PRIVATE spaces too and hands back the full
	// hash, which is the blob's capability, so a shorter prefix would let a guess
	// over a smaller space unlock a private file.
	if len(prefix) < fileHashShortLen || len(prefix) > 64 || strings.Trim(prefix, "0123456789abcdef") != "" {
		return sharedFile{}, sql.ErrNoRows
	}
	var f sharedFile
	var isPublic int
	err := db.QueryRowContext(ctx, `
		SELECT f.content_hash, f.name, f.mime, f.byte_size, f.space_id, sp.name,
		       CASE WHEN sp.visibility = 'public' AND COALESCE(p.status, 'published') = 'published' THEN 1 ELSE 0 END,
		       COALESCE(sp.org_id, 0), COALESCE(f.parent_page_id, 0),
		       COALESCE(p.title, ''), COALESCE(f.summary, '')
		  FROM space_files f
		  JOIN spaces sp ON sp.id = f.space_id
		  LEFT JOIN pages p ON p.id = f.parent_page_id AND p.deleted_at IS NULL
		 WHERE f.deleted_at IS NULL
		   AND f.content_hash >= $1 AND f.content_hash < $2
		 ORDER BY f.id ASC
		 LIMIT 1`, prefix, prefix+"g",
	).Scan(&f.hash, &f.name, &f.mime, &f.size, &f.spaceID, &f.spaceName,
		&isPublic, &f.ownerOrgID, &f.pageID, &f.pageTitle, &f.summary)
	if err != nil {
		return sharedFile{}, err
	}
	f.spacePublic = isPublic == 1
	return f, nil
}

// HandleFilePage — GET /f/{hash} and GET /f/{hash}/{name}. Crawler UAs get the
// card; anything else 404s (Caddy serves humans the SPA at this path).
func (s *Server) HandleFilePage(w http.ResponseWriter, r *http.Request) {
	f, err := lookupSharedFile(r.Context(), s.DB, r.PathValue("hash"))
	if errors.Is(err, sql.ErrNoRows) {
		writeNotFoundHTML(w)
		return
	}
	if err != nil {
		writeInternalHTML(w)
		return
	}
	if !isBotUA(r.Header.Get("User-Agent")) {
		writeNotFoundHTML(w)
		return
	}
	s.writeFileOG(w, r, f)
}

// writeFileOG emits the crawler envelope for a file. Shared by /f/{hash} and the
// /api/files retrofit so a link unfurls identically in either shape.
func (s *Server) writeFileOG(w http.ResponseWriter, r *http.Request, f sharedFile) {
	origin := s.originFor(r)
	if origin == "" {
		origin = canonicalBaseURL()
	}
	site := s.ogSiteName(r, f.ownerOrgID)
	desc := f.meta()
	switch {
	case f.spacePublic && strings.TrimSpace(f.summary) != "":
		desc += " — " + runeTruncate(stripMarkdownToText(f.summary), 160)
	case f.spacePublic && f.pageTitle != "":
		desc += " — attached to " + f.pageTitle + " in " + f.spaceName
	default:
		// Private space: the file itself, nothing about where it lives.
		desc += " · shared from " + site
	}
	writeOGDoc(w, ogDoc{
		Title:        runeTruncate(f.name, 110),
		Description:  runeTruncate(desc, 200),
		CanonicalURL: origin + f.pagePath(),
		ImageURL:     origin + "/f/" + f.short() + "/og.png",
		OGType:       "website",
		SiteName:     site,
	})
}

// HandleFileOGImage — GET /f/{hash}/og.png. Served to ALL UAs (link-preview
// image fetchers carry arbitrary ones), like every other og.png here.
func (s *Server) HandleFileOGImage(w http.ResponseWriter, r *http.Request) {
	f, err := lookupSharedFile(r.Context(), s.DB, r.PathValue("hash"))
	if err != nil {
		writeNotFoundHTML(w)
		return
	}
	subtitle := humanBytes(f.size)
	if f.spacePublic && f.pageTitle != "" {
		subtitle += " · " + f.pageTitle
	}
	png, err := renderOGCardOpts(ogCardOpts{
		kicker:        f.kind(),
		title:         f.name,
		subtitle:      subtitle,
		maxTitleLines: 2,
		accentLabel:   s.ogHost(r),
		brand:         s.resolveOGBrand(r, f.ownerOrgID),
	})
	if err != nil {
		writeInternalHTML(w)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("Content-Length", strconv.Itoa(len(png)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(png)
}

type publicFilePageOut struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Path      string `json:"path"`
	SpaceName string `json:"space_name"`
}

type publicFileOut struct {
	Hash     string             `json:"hash"`
	Short    string             `json:"short"`
	Name     string             `json:"name"`
	Mime     string             `json:"mime"`
	Kind     string             `json:"kind"`
	ByteSize int64              `json:"byte_size"`
	URL      string             `json:"url"`  // the blob (download / pdf.js source)
	Path     string             `json:"path"` // canonical /f/… path
	Page     *publicFilePageOut `json:"page,omitempty"`
}

// GetPublicFile — GET /api/public/files/{hash}. The metadata the file page needs
// to render: name, type, size and the blob URL. Self-authenticating like every
// /api/public/ route, and it hands back exactly what the blob's own
// Content-Disposition already discloses — the parent page only when its space is
// public.
func (s *Server) GetPublicFile(w http.ResponseWriter, r *http.Request) {
	f, err := lookupSharedFile(r.Context(), s.DB, r.PathValue("hash"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "file not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "lookup file failed")
		return
	}
	out := publicFileOut{
		Hash: f.hash, Short: f.short(), Name: f.name, Mime: f.mime, Kind: f.kind(),
		ByteSize: f.size, URL: f.blobPath(), Path: f.pagePath(),
	}
	if f.spacePublic && f.pageID > 0 {
		out.Page = &publicFilePageOut{
			ID:        f.pageID,
			Title:     f.pageTitle,
			Path:      s.canonicalPagePath(r.Context(), f.spaceID, f.pageID, f.pageTitle),
			SpaceName: f.spaceName,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"file": out})
}
