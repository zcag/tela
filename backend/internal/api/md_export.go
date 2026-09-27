package api

import (
	"archive/zip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"github.com/zcag/tela/backend/internal/models"
	"github.com/zcag/tela/backend/internal/pagemd"
)

// ExportPageMarkdown (GET /api/pages/{id}/md): session-authed. Returns the
// page's full canonical markdown — synthesized frontmatter (system fields + the
// props bag) followed by the body — as a download. The round-trip "out" side of
// the page-properties contract (docs/page-properties.md).
func (s *Server) ExportPageMarkdown(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	p, err := selectPageByID(r.Context(), s.DB, id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusForbidden, "forbidden", "not a member")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "fetch page failed")
		return
	}
	if _, ok := s.requireMembership(w, r, p.SpaceID); !ok {
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", mdSlugOr(p.Title, "page")+".md"))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(pagemd.Encode(p, canonicalBaseURL()))
}

// ExportSpaceMarkdownZip (GET /api/spaces/{id}/export.zip): session-authed.
// Streams the whole space as a folder of .md files (one per page, full canonical
// markdown), the page tree preserved as directories. Uploaded attachments are
// placed under `<page-slug>/_attachments/<filename>` alongside the .md. A page
// with children becomes `<slug>.md` plus a sibling `<slug>/` folder holding its
// descendants (the Obsidian note-plus-folder layout).
func (s *Server) ExportSpaceMarkdownZip(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	if _, ok := s.requireMembership(w, r, id); !ok {
		return
	}
	var spaceName string
	if err := s.DB.QueryRowContext(r.Context(),
		`SELECT name FROM spaces WHERE id = $1`, id).Scan(&spaceName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "space_not_found", "space not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "lookup space failed")
		return
	}
	pages, err := loadSpacePages(r.Context(), s.DB, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "load pages failed")
		return
	}
	attachments, err := loadSpaceAttachments(r.Context(), s.DB, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "load attachments failed")
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", mdSlugOr(spaceName, "space")+".zip"))
	w.Header().Set("X-Content-Type-Options", "nosniff")

	zw := zip.NewWriter(w)
	defer zw.Close()
	writeSpaceZip(zw, pages, attachments)
}

// exportAttachment is a single uploaded file for the zip export — name + raw bytes.
type exportAttachment struct {
	name string
	data []byte
}

// loadSpaceAttachments loads all live uploaded files for a space, keyed by the
// parent page id. Attachment data (blob) is included so the zip export is
// self-contained without any additional HTTP round-trip.
func loadSpaceAttachments(ctx context.Context, db *sql.DB, spaceID int64) (map[int64][]exportAttachment, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT parent_page_id, name, data FROM space_files
		  WHERE space_id = $1 AND parent_page_id IS NOT NULL AND deleted_at IS NULL
		  ORDER BY parent_page_id, id`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]exportAttachment{}
	for rows.Next() {
		var pageID int64
		var name string
		var data []byte
		if err := rows.Scan(&pageID, &name, &data); err != nil {
			return nil, err
		}
		out[pageID] = append(out[pageID], exportAttachment{name: name, data: data})
	}
	return out, rows.Err()
}

// loadSpacePages returns every page in a space, ordered by position then id so
// the zip tree walk is deterministic.
func loadSpacePages(ctx context.Context, db *sql.DB, spaceID int64) ([]models.Page, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT `+pageCols+`
		   FROM pages WHERE space_id = $1 AND deleted_at IS NULL ORDER BY position ASC, id ASC`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Page
	for rows.Next() {
		p, err := scanPageFromRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// writeSpaceZip lays the pages out as a directory tree and writes one .md per
// page. Sibling filename collisions (same slug) are disambiguated with -2, -3…;
// a page's children nest under a folder of the same (deduped) slug. The layout
// (slug derivation + dedup) is the shared sibling-folder model in pagetree.go,
// so the zip and the live WebDAV surface emit byte-identical file trees.
// Attachments land at `<slug>/_attachments/<filename>` alongside their page.
func writeSpaceZip(zw *zip.Writer, pages []models.Page, attachments map[int64][]exportAttachment) {
	children := map[int64][]models.Page{}
	var roots []models.Page
	for _, p := range pages {
		if p.ParentID == nil {
			roots = append(roots, p)
		} else {
			children[*p.ParentID] = append(children[*p.ParentID], p)
		}
	}

	var walk func(nodes []models.Page, prefix string)
	walk = func(nodes []models.Page, prefix string) {
		slugs := siblingSlugs(nodes)
		for _, p := range nodes {
			slug := slugs[p.ID]
			if fw, err := zw.Create(prefix + slug + ".md"); err == nil {
				_, _ = fw.Write(pagemd.Encode(p, canonicalBaseURL()))
			}
			for _, a := range attachments[p.ID] {
				if fw, err := zw.Create(prefix + slug + "/_attachments/" + a.name); err == nil {
					_, _ = fw.Write(a.data)
				}
			}
			if kids := children[p.ID]; len(kids) > 0 {
				walk(kids, prefix+slug+"/")
			}
		}
	}
	walk(roots, "")
}

// mdSlugOr returns a filename-safe slug for title, or fallback when the title
// yields no slug. Reuses the same pageSlug derivation emitted as the `slug`
// frontmatter key, so filenames match the round-tripped metadata.
func mdSlugOr(title, fallback string) string {
	if s := pageSlug(title); s != "" {
		return s
	}
	return fallback
}
