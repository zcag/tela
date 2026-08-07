package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/zcag/tela/backend/internal/models"
)

// Crawler-facing surfaces for the public blog. Caddy routes BOT user-agents on
// /public/* and /u/* to these handlers (humans get the SPA); they emit a small
// HTML document with title / description / canonical / OpenGraph / Twitter +
// JSON-LD so the public space front page, reader pages, and author home index
// and unfurl. Read-only; each self-authenticates by requiring the space (or the
// user's spaces) be public. Mirrors the /share OG bot-gate pattern.
//
// Public-space pages carry a RICH body excerpt in og:description — unlike the
// /p/{id} permalink (title-only for privacy), the body here is genuinely public.

// ogDoc is the rendered crawler document. JSONLD is pre-marshalled JSON embedded
// verbatim in a <script type="application/ld+json"> block (json.Marshal escapes
// <,>,& so it is safe inside the element).
type ogDoc struct {
	Title        string
	Description  string
	CanonicalURL string
	ImageURL     string
	OGType       string        // website | article | profile
	FeedURL      string        // optional rss alternate
	JSONLD       string        // optional ld+json
	SiteName     string        // og:site_name — org brand on a white-label domain, else "tela"
	Heading      string        // optional <h1> for the crawler body (page title)
	BodyHTML     template.HTML // optional rendered page body (crawler-visible content)
}

func writeOGDoc(w http.ResponseWriter, d ogDoc) {
	if d.SiteName == "" {
		d.SiteName = "tela"
	}
	feed := ""
	if d.FeedURL != "" {
		feed = fmt.Sprintf("\n  <link rel=\"alternate\" type=\"application/rss+xml\" href=%q>", html.EscapeString(d.FeedURL))
	}
	jsonld := ""
	if d.JSONLD != "" {
		jsonld = "\n  <script type=\"application/ld+json\">" + d.JSONLD + "</script>"
	}
	// Crawler-visible body: an <article> with the page heading + rendered markdown.
	// Empty (SPA shell) when the caller supplies no body — the human path is a
	// client-rendered SPA regardless; this content is for bots/indexing only.
	body := ""
	if d.BodyHTML != "" {
		heading := ""
		if d.Heading != "" {
			heading = "<h1>" + html.EscapeString(d.Heading) + "</h1>\n"
		}
		body = "<article>" + heading + string(d.BodyHTML) + "</article>"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>%s</title>
  <meta name="description" content="%s">
  <link rel="canonical" href="%s">
  <meta property="og:site_name" content="%s">
  <meta property="og:title" content="%s">
  <meta property="og:description" content="%s">
  <meta property="og:image" content="%s">
  <meta property="og:image:width" content="1200">
  <meta property="og:image:height" content="630">
  <meta property="og:url" content="%s">
  <meta property="og:type" content="%s">
  <meta name="twitter:card" content="summary_large_image">
  <meta name="twitter:title" content="%s">
  <meta name="twitter:description" content="%s">
  <meta name="twitter:image" content="%s">%s%s
</head>
<body>%s</body>
</html>`,
		html.EscapeString(d.Title), html.EscapeString(d.Description), html.EscapeString(d.CanonicalURL),
		html.EscapeString(d.SiteName),
		html.EscapeString(d.Title), html.EscapeString(d.Description), html.EscapeString(d.ImageURL),
		html.EscapeString(d.CanonicalURL), html.EscapeString(d.OGType),
		html.EscapeString(d.Title), html.EscapeString(d.Description), html.EscapeString(d.ImageURL),
		feed, jsonld, body,
	)
}

func jsonLD(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// telaTimeToRFC3339 converts a tela TEXT timestamp to RFC3339 (for schema.org
// date fields). Empty on parse failure.
func telaTimeToRFC3339(s string) string {
	t, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

type ogPostRow struct {
	id      int64
	title   string
	created string
}

func (s *Server) topLevelPosts(r *http.Request, spaceID int64, limit int) []ogPostRow {
	rows, err := s.DB.QueryContext(r.Context(),
		`SELECT id, title, created_at
		   FROM pages
		  WHERE space_id = $1 AND parent_id IS NULL AND deleted_at IS NULL
		  ORDER BY created_at DESC, id DESC
		  LIMIT $2`, spaceID, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ogPostRow
	for rows.Next() {
		var p ogPostRow
		if rows.Scan(&p.id, &p.title, &p.created) == nil {
			out = append(out, p)
		}
	}
	return out
}

// spaceOwnerHandle returns the username of a space's personal owner (its
// owner-role member). Empty when the space is org-owned (no personal owner) or
// has none. Best-effort — never errors the caller.
func (s *Server) spaceOwnerHandle(ctx context.Context, spaceID int64) string {
	var h string
	_ = s.DB.QueryRowContext(ctx,
		`SELECT u.username FROM space_members m JOIN users u ON u.id = m.user_id
		  WHERE m.space_id = $1 AND m.role = 'owner' ORDER BY m.user_id ASC LIMIT 1`, spaceID).Scan(&h)
	return h
}

// canonicalSpacePath is the canonical path for a public space: the pretty
// /{handle}/{space-slug}, falling back to the id form when no handle resolves.
// One definition so the OG canonical and the sitemap can never disagree — a
// sitemap entry whose page canonicalizes elsewhere is a self-inflicted
// deindexing.
func (s *Server) canonicalSpacePath(ctx context.Context, sp models.Space) string {
	if p := s.spaceHandlePath(ctx, sp.ID, sp.Slug); p != "" {
		return p
	}
	return publicSpacePath(sp.ID)
}

// loadPublicSpaceForOG loads a space only when it is public, writing an HTML 404
// otherwise (crawler-friendly — no JSON envelope).
func (s *Server) loadPublicSpaceForOG(w http.ResponseWriter, r *http.Request) (models.Space, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeNotFoundHTML(w)
		return models.Space{}, false
	}
	sp, err := selectSpaceByID(r.Context(), s.DB, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && sp.Visibility != spaceVisibilityPublic) {
		writeNotFoundHTML(w)
		return models.Space{}, false
	}
	if err != nil {
		writeInternalHTML(w)
		return models.Space{}, false
	}
	return sp, true
}

// HandlePublicSpaceOG — GET /public/spaces/{id} (bot UAs). Blog front-page card.
func (s *Server) HandlePublicSpaceOG(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.loadPublicSpaceForOG(w, r)
	if !ok {
		return
	}
	s.renderSpaceOG(w, r, sp)
}

// renderSpaceOG writes the blog front-page card for a public space. Shared by
// the id route (/public/spaces/{id}) and the handle route (/{handle}/{slug}) so
// BOTH URL shapes unfurl — the pretty handle form is the one the docs tell people
// to share, and it produced no card at all until it routed here. Both emit the
// PRETTY path as canonical, so the two shapes consolidate onto one document.
func (s *Server) renderSpaceOG(w http.ResponseWriter, r *http.Request, sp models.Space) {
	base := canonicalBaseURL()
	// Canonical is the PRETTY handle form — that's the URL people share and the
	// one the docs name, so it's the one search results should show. The id form
	// stays live and points here, consolidating both shapes onto one document.
	canonical := base + s.canonicalSpacePath(r.Context(), sp)
	owner := s.spaceOwnerHandle(r.Context(), sp.ID)
	siteName := s.ogSiteName(r, s.spaceOwnerOrg(r.Context(), sp.ID))
	desc := sp.Description
	if desc == "" {
		desc = "A blog on " + siteName + "."
	}

	ld := map[string]any{
		"@context": "https://schema.org", "@type": "Blog",
		"name": sp.Name, "description": sp.Description, "url": canonical,
	}
	if owner != "" {
		ld["author"] = map[string]any{"@type": "Person", "name": owner, "url": base + "/" + url.PathEscape(owner)}
	}
	// One post list feeds both the JSON-LD blogPost array AND a crawler-visible
	// linked index — so bots reach every public page through internal <a> links
	// (stronger than sitemap-only discovery), not just structured-data URLs.
	var body template.HTML
	if posts := s.topLevelPosts(r, sp.ID, 100); len(posts) > 0 {
		bp := make([]map[string]any, 0, len(posts))
		var list string
		for _, p := range posts {
			path := publicReaderPath(sp.ID, p.id, p.title)
			bp = append(bp, map[string]any{
				"@type": "BlogPosting", "headline": p.title,
				"url":           base + path,
				"datePublished": telaTimeToRFC3339(p.created),
			})
			list += "<li><a href=\"" + html.EscapeString(path) + "\">" + html.EscapeString(p.title) + "</a></li>"
		}
		ld["blogPost"] = bp
		body = template.HTML("<ul>" + list + "</ul>") //nolint:gosec // titles+paths escaped above
	}

	writeOGDoc(w, ogDoc{
		Title:        sp.Name + " — " + siteName,
		Description:  runeTruncate(desc, 200),
		CanonicalURL: canonical,
		ImageURL:     base + "/api/public/spaces/" + strconv.FormatInt(sp.ID, 10) + "/og.png",
		OGType:       "website",
		FeedURL:      base + "/api/public/spaces/" + strconv.FormatInt(sp.ID, 10) + "/feed.xml",
		JSONLD:       jsonLD(ld),
		SiteName:     siteName,
		Heading:      sp.Name,
		BodyHTML:     body,
	})
}

// HandlePublicReaderOG — GET /public/spaces/{id}/pages/{page_id}[/{slug}] (bots).
func (s *Server) HandlePublicReaderOG(w http.ResponseWriter, r *http.Request) {
	sp, ok := s.loadPublicSpaceForOG(w, r)
	if !ok {
		return
	}
	pageID, err := strconv.ParseInt(r.PathValue("page_id"), 10, 64)
	if err != nil {
		writeNotFoundHTML(w)
		return
	}
	page, err := selectPageByID(r.Context(), s.DB, pageID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && page.SpaceID != sp.ID) {
		writeNotFoundHTML(w)
		return
	}
	if err != nil {
		writeInternalHTML(w)
		return
	}

	base := canonicalBaseURL()
	canonical := base + publicReaderPath(sp.ID, pageID, page.Title)
	imageURL := base + fmt.Sprintf("/p/%d/og.png", pageID)
	desc := postExcerpt(page.Body, page.Props, 200)
	// Crawler-visible author should match the human-visible byline: the page's
	// own author (first revision), not the space owner. Falls back to the owner
	// for legacy pages with no recorded author — single-author blogs, where the
	// two coincide anyway. Keeps the schema.org author in sync with PublicReader.
	author, _ := pageAuthorAndEditor(r.Context(), s.DB, pageID)
	if author == "" {
		author = s.spaceOwnerHandle(r.Context(), sp.ID)
	}

	ld := map[string]any{
		"@context": "https://schema.org", "@type": "BlogPosting",
		"headline": page.Title, "description": desc, "url": canonical,
		"mainEntityOfPage": canonical, "image": imageURL,
		"datePublished": telaTimeToRFC3339(page.CreatedAt),
		"dateModified":  telaTimeToRFC3339(page.UpdatedAt),
		"isPartOf":      map[string]any{"@type": "Blog", "name": sp.Name, "url": base + publicSpacePath(sp.ID)},
	}
	if author != "" {
		ld["author"] = map[string]any{"@type": "Person", "name": author, "url": base + "/" + url.PathEscape(author)}
	}

	writeOGDoc(w, ogDoc{
		Title:        page.Title + " — " + sp.Name,
		Description:  runeTruncate(desc, 200),
		CanonicalURL: canonical,
		ImageURL:     imageURL,
		OGType:       "article",
		JSONLD:       jsonLD(ld),
		SiteName:     s.ogSiteName(r, s.spaceOwnerOrg(r.Context(), sp.ID)),
		Heading:      page.Title,
		BodyHTML:     renderPublicBodyHTML(page.Body),
	})
}

// HandlePublicUserOG — GET /u/{username} (bots). Author home card. 404 unless the
// user exists and has at least one public space.
func (s *Server) HandlePublicUserOG(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	if username == "" {
		writeNotFoundHTML(w)
		return
	}
	var name, bio string
	err := s.DB.QueryRowContext(r.Context(),
		`SELECT username, bio FROM users WHERE LOWER(username) = LOWER($1)`, username).Scan(&name, &bio)
	if errors.Is(err, sql.ErrNoRows) {
		writeNotFoundHTML(w)
		return
	}
	if err != nil {
		writeInternalHTML(w)
		return
	}
	// Same gate as the handle home (handleOwnerWhere): an owner row on a public
	// ORG space does NOT give the user a home, so it must not render a card.
	_, uid, _, _, resolved := s.resolveHandle(r.Context(), name)
	hasPublic := resolved && s.handleHasPublicSpace(r.Context(), handleKindUser, uid)
	if !hasPublic {
		writeNotFoundHTML(w)
		return
	}

	base := canonicalBaseURL()
	// /u/{name} is the LEGACY form and redirects to /{name}; canonical points at
	// the unified handle URL so both shapes consolidate there.
	canonical := base + "/" + url.PathEscape(name)
	// A user home isn't org-scoped, so branding comes only from the request's
	// custom-domain host (ogSiteName with no owning org) — else "tela".
	siteName := s.ogSiteName(r, 0)
	desc := bio
	if desc == "" {
		desc = name + " on " + siteName
	}
	ld := map[string]any{
		"@context": "https://schema.org", "@type": "ProfilePage", "url": canonical,
		"mainEntity": map[string]any{"@type": "Person", "name": name, "url": canonical, "description": bio},
	}
	writeOGDoc(w, ogDoc{
		Title:        name + " — " + siteName,
		Description:  runeTruncate(desc, 200),
		CanonicalURL: canonical,
		ImageURL:     base + "/api/public/users/" + url.PathEscape(name) + "/og.png",
		OGType:       "profile",
		JSONLD:       jsonLD(ld),
		SiteName:     siteName,
	})
}

// HandleHandleHomeOG — GET /{handle} (bot UAs). The unified handle home card,
// for a USER or an ORG. Gated on handleHasPublicSpace — the same predicate
// GetPublicByHandle uses — so a card is never served for a home that 404s.
func (s *Server) HandleHandleHomeOG(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	kind, ownerID, name, bio, ok := s.resolveHandle(r.Context(), handle)
	if !ok || !s.handleHasPublicSpace(r.Context(), kind, ownerID) {
		writeNotFoundHTML(w)
		return
	}

	base := canonicalBaseURL()
	canonical := base + "/" + url.PathEscape(handle)
	// An org home IS org-scoped, so it brands from that org; a user home brands
	// only from the request's custom-domain host.
	orgID := int64(0)
	if kind == handleKindOrg {
		orgID = ownerID
	}
	siteName := s.ogSiteName(r, orgID)
	desc := bio
	if desc == "" {
		desc = name + " on " + siteName
	}

	entityType := "Person"
	ogType := "profile"
	if kind == handleKindOrg {
		entityType, ogType = "Organization", "website"
	}
	ld := map[string]any{
		"@context": "https://schema.org", "@type": "ProfilePage", "url": canonical,
		"mainEntity": map[string]any{"@type": entityType, "name": name, "url": canonical, "description": bio},
	}
	// Crawler-visible links to the handle's public spaces, so bots reach them
	// through internal <a> links rather than the sitemap alone.
	var body template.HTML
	if spaces, err := s.publicSpacesForHandle(r, kind, ownerID); err == nil && len(spaces) > 0 {
		var list string
		for _, sp := range spaces {
			path := "/" + url.PathEscape(handle) + "/" + url.PathEscape(sp.Slug)
			list += "<li><a href=\"" + html.EscapeString(path) + "\">" + html.EscapeString(sp.Name) + "</a></li>"
		}
		body = template.HTML("<ul>" + list + "</ul>") //nolint:gosec // names+paths escaped above
	}

	// "name — siteName" reads as "Çağdaş Salur — tela" for a user, but an ORG is
	// its own site, so the same formula gives "tela — tela". Title is just the
	// name there; og:site_name still carries the brand.
	title := name + " — " + siteName
	if kind == handleKindOrg || name == siteName {
		title = name
	}
	writeOGDoc(w, ogDoc{
		Title:        title,
		Description:  runeTruncate(desc, 200),
		CanonicalURL: canonical,
		ImageURL:     base + "/api/public/handles/" + url.PathEscape(handle) + "/og.png",
		OGType:       ogType,
		JSONLD:       jsonLD(ld),
		SiteName:     siteName,
		Heading:      name,
		BodyHTML:     body,
	})
}

// HandleHandleSpaceOG — GET /{handle}/{spaceSlug} (bot UAs). Resolves the pretty
// URL to its space and renders the same card as the id route.
func (s *Server) HandleHandleSpaceOG(w http.ResponseWriter, r *http.Request) {
	kind, ownerID, _, _, ok := s.resolveHandle(r.Context(), r.PathValue("handle"))
	if !ok {
		writeNotFoundHTML(w)
		return
	}
	id, _, err := s.publicSpaceIDForHandle(r, kind, ownerID, r.PathValue("spaceSlug"))
	if errors.Is(err, sql.ErrNoRows) {
		writeNotFoundHTML(w)
		return
	}
	if err != nil {
		writeInternalHTML(w)
		return
	}
	sp, err := selectSpaceByID(r.Context(), s.DB, id)
	if err != nil || sp.Visibility != spaceVisibilityPublic {
		writeNotFoundHTML(w)
		return
	}
	s.renderSpaceOG(w, r, sp)
}

// HandleHandleOGImage — GET /api/public/handles/{handle}/og.png. The handle-home
// card image, for a user OR an org (the older /api/public/users/{name}/og.png
// stays for the legacy /u/ route).
func (s *Server) HandleHandleOGImage(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	kind, ownerID, name, bio, ok := s.resolveHandle(r.Context(), handle)
	if !ok || !s.handleHasPublicSpace(r.Context(), kind, ownerID) {
		http.NotFound(w, r)
		return
	}
	sub := bio
	if sub == "" {
		sub = "on " + s.ogSiteName(r, 0)
	}
	orgID := int64(0)
	if kind == handleKindOrg {
		orgID = ownerID
	}
	writeOGImagePNG(w, name, runeTruncate(sub, 70), s.resolveOGBrand(r, orgID))
}

// HandlePublicSpaceOGImage — GET /api/public/spaces/{id}/og.png. A title card for
// the blog front page (reuses the share/permalink OG renderer).
func (s *Server) HandlePublicSpaceOGImage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sp, err := selectSpaceByID(r.Context(), s.DB, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && sp.Visibility != spaceVisibilityPublic) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "error", http.StatusInternalServerError)
		return
	}
	sub := sp.Description
	if sub == "" {
		sub = "A blog on tela"
	}
	brand := s.resolveOGBrand(r, s.spaceOwnerOrg(r.Context(), sp.ID))
	writeOGImagePNG(w, sp.Name, runeTruncate(sub, 70), brand)
}

// HandlePublicUserOGImage — GET /api/public/users/{username}/og.png.
// 404 unless the user exists and has at least one public space (mirrors HandlePublicUserOG).
func (s *Server) HandlePublicUserOGImage(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	var name, bio string
	err := s.DB.QueryRowContext(r.Context(),
		`SELECT username, bio FROM users WHERE LOWER(username) = LOWER($1)`, username).Scan(&name, &bio)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "error", http.StatusInternalServerError)
		return
	}
	// Same gate as the handle home (handleOwnerWhere): an owner row on a public
	// ORG space does NOT give the user a home, so it must not render a card.
	_, uid, _, _, resolved := s.resolveHandle(r.Context(), name)
	hasPublic := resolved && s.handleHasPublicSpace(r.Context(), handleKindUser, uid)
	if !hasPublic {
		http.NotFound(w, r)
		return
	}
	sub := bio
	if sub == "" {
		sub = "on tela"
	}
	// A user home isn't org-scoped; brand only from the request's custom domain.
	writeOGImagePNG(w, name, runeTruncate(sub, 70), s.resolveOGBrand(r, 0))
}

func writeOGImagePNG(w http.ResponseWriter, title, subtitle string, brand ogBrand) {
	png, err := renderOGCard(title, subtitle, brand)
	if err != nil {
		http.Error(w, "render failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(png)
}

// HandlePublicSitemap — GET /api/public/sitemap.xml (Caddy also serves it at
// /sitemap-public.xml). Lists every public space front page, every public page's
// reader URL, and the author home of every public-space owner.
func (s *Server) HandlePublicSitemap(w http.ResponseWriter, r *http.Request) {
	type urlEntry struct {
		Loc     string `xml:"loc"`
		LastMod string `xml:"lastmod,omitempty"`
	}
	type urlset struct {
		XMLName xml.Name   `xml:"urlset"`
		NS      string     `xml:"xmlns,attr"`
		URLs    []urlEntry `xml:"url"`
	}
	base := canonicalBaseURL()
	set := urlset{NS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	ctx := r.Context()

	// Public space front pages, at their CANONICAL pretty path — the sitemap must
	// list what each page canonicalizes to, or we'd be submitting URLs that point
	// search engines somewhere else.
	if rows, err := s.DB.QueryContext(ctx,
		`SELECT s.id, s.slug, s.updated_at, `+spaceHandleExpr+`
		   FROM spaces s LEFT JOIN orgs o ON o.id = s.org_id
		  WHERE s.visibility = 'public' ORDER BY s.id`); err == nil {
		for rows.Next() {
			var id int64
			var slug, upd, handle string
			if rows.Scan(&id, &slug, &upd, &handle) == nil {
				path := handleSpacePath(handle, slug)
				if path == "" {
					path = publicSpacePath(id)
				}
				set.URLs = append(set.URLs, urlEntry{Loc: base + path, LastMod: sitemapDate(upd)})
			}
		}
		rows.Close()
	}
	// Pages in public spaces (reader URLs).
	if rows, err := s.DB.QueryContext(ctx,
		`SELECT p.id, p.title, p.space_id, p.updated_at
		   FROM pages p JOIN spaces sp ON sp.id = p.space_id
		  WHERE sp.visibility = 'public' AND p.deleted_at IS NULL
		  ORDER BY p.space_id, p.id`); err == nil {
		for rows.Next() {
			var pid, sid int64
			var title, upd string
			if rows.Scan(&pid, &title, &sid, &upd) == nil {
				set.URLs = append(set.URLs, urlEntry{Loc: base + publicReaderPath(sid, pid, title), LastMod: sitemapDate(upd)})
			}
		}
		rows.Close()
	}
	// Author homes for owners of public spaces. The org_id IS NULL guard mirrors
	// handleOwnerWhere's user branch — an owner row on a public ORG space does
	// NOT give the user a handle home, so listing them here advertised URLs that
	// 404 (their spaces live on the ORG handle).
	if rows, err := s.DB.QueryContext(ctx,
		`SELECT DISTINCT u.username
		   FROM users u JOIN space_members m ON m.user_id = u.id
		   JOIN spaces sp ON sp.id = m.space_id
		  WHERE sp.visibility = 'public' AND m.role = 'owner' AND sp.org_id IS NULL
		  ORDER BY u.username`); err == nil {
		for rows.Next() {
			var h string
			if rows.Scan(&h) == nil {
				set.URLs = append(set.URLs, urlEntry{Loc: base + "/" + url.PathEscape(h)})
			}
		}
		rows.Close()
	}
	// Org homes. These are real public pages at /{org-slug} and were missing
	// entirely — an org's public spaces were reachable but its home never
	// advertised.
	if rows, err := s.DB.QueryContext(ctx,
		`SELECT DISTINCT o.slug
		   FROM orgs o JOIN spaces sp ON sp.org_id = o.id
		  WHERE sp.visibility = 'public'
		  ORDER BY o.slug`); err == nil {
		for rows.Next() {
			var h string
			if rows.Scan(&h) == nil {
				set.URLs = append(set.URLs, urlEntry{Loc: base + "/" + h})
			}
		}
		rows.Close()
	}

	out, err := xml.MarshalIndent(set, "", "  ")
	if err != nil {
		http.Error(w, "encode failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=900")
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(out)
}

// sitemapDate reduces a tela timestamp to a W3C date (YYYY-MM-DD) for <lastmod>.
func sitemapDate(telaTs string) string {
	if len(telaTs) >= 10 {
		return telaTs[:10]
	}
	return ""
}
