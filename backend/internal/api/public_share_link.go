package api

import (
	"database/sql"
	"errors"
	"fmt"
	"html"
	"net/http"
)

// M15.5 PublicShare OG bot-gate. Mirrors the M11.0 /p/{id} pattern for share
// URLs so Slack / Twitter / Discord etc. unfurl shared pages with a real OG
// card instead of an empty SPA shell.
//
// Routing model: Caddy's /share/* block matches a bot UA regexp and routes
// bots here; everything else hits the frontend SPA (M15.1). The handlers below
// keep the UA guard as defense-in-depth — if the Caddy block is misconfigured,
// a real browser still gets a 404 from the backend rather than the OG envelope
// rendering instead of the SPA.

// HandlePublicShareLink — GET /share/{token}. Bot UA → OG HTML for the share's
// ROOT page; non-bot UA → 404 (Caddy is the real branch in prod). Token must
// exist + be active (not revoked, not expired) — missing / revoked / expired
// all collapse to an identical 404 to avoid a token-enumeration oracle.
func (s *Server) HandlePublicShareLink(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		writeNotFoundHTML(w)
		return
	}
	share, err := selectShareLinkByToken(r.Context(), s.DB, token)
	if errors.Is(err, sql.ErrNoRows) {
		writeNotFoundHTML(w)
		return
	}
	if err != nil {
		writeInternalHTML(w)
		return
	}
	if !shareLinkActive(&share) {
		writeNotFoundHTML(w)
		return
	}
	if !isBotUA(r.Header.Get("User-Agent")) {
		writeNotFoundHTML(w)
		return
	}
	origin := s.shareOriginForPage(r.Context(), share.PageID)
	if share.PasswordHash.Valid {
		writeLockedShareOGHTML(w, origin, share.Token, s.ogSiteName(r, s.pageOwnerOrg(r.Context(), share.PageID)))
		return
	}
	s.writeShareOGHTML(r, w, &share, share.PageID, shareRootURL(origin, share.Token), origin, true)
}

// HandlePublicShareLinkPage — GET /share/{token}/p/{page_id}. Same gate as the
// root handler plus a subtree check: descendant page must be in the share's
// scope (include_descendants=true AND page is a transitive descendant) or
// equal the root.
func (s *Server) HandlePublicShareLinkPage(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		writeNotFoundHTML(w)
		return
	}
	share, err := selectShareLinkByToken(r.Context(), s.DB, token)
	if errors.Is(err, sql.ErrNoRows) {
		writeNotFoundHTML(w)
		return
	}
	if err != nil {
		writeInternalHTML(w)
		return
	}
	if !shareLinkActive(&share) {
		writeNotFoundHTML(w)
		return
	}
	pageID, ok := parseShareLinkPageID(r)
	if !ok {
		writeNotFoundHTML(w)
		return
	}
	if pageID != share.PageID {
		if !share.IncludeDescendants {
			writeNotFoundHTML(w)
			return
		}
		inScope, err := pageInShareSubtree(r.Context(), s.DB, share.PageID, pageID)
		if err != nil {
			writeInternalHTML(w)
			return
		}
		if !inScope {
			writeNotFoundHTML(w)
			return
		}
	}
	if !isBotUA(r.Header.Get("User-Agent")) {
		writeNotFoundHTML(w)
		return
	}
	origin := s.shareOriginForPage(r.Context(), pageID)
	if share.PasswordHash.Valid {
		writeLockedShareOGHTML(w, origin, share.Token, s.ogSiteName(r, s.pageOwnerOrg(r.Context(), pageID)))
		return
	}
	s.writeShareOGHTML(r, w, &share, pageID, shareDescendantURL(origin, share.Token, pageID), origin, false)
}

// parseShareLinkPageID parses the {page_id} path value as a positive int64,
// preserving the writeNotFoundHTML contract (HTML, no JSON oracle).
func parseShareLinkPageID(r *http.Request) (int64, bool) {
	raw := r.PathValue("page_id")
	if raw == "" {
		return 0, false
	}
	var id int64
	if _, err := fmt.Sscanf(raw, "%d", &id); err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// shareRootURL builds the absolute (or path-only when origin is empty) URL
// Slack / Twitter / etc. should display for the share root. origin is the
// share's effective origin (org custom domain when applicable, else canonical).
func shareRootURL(origin, token string) string {
	return fmt.Sprintf("%s/share/%s", origin, token)
}

// shareDescendantURL builds the absolute (or path-only) URL for a descendant
// page within a share's subtree.
func shareDescendantURL(origin, token string, pageID int64) string {
	return fmt.Sprintf("%s/share/%s/p/%d", origin, token, pageID)
}

// writeShareOGHTML emits the OG envelope for an unlocked share. The og:image
// reuses the existing /p/{page_id}/og.png renderer (already public, no auth);
// only the og:url differs from the /p/{id} flavour, so this routes through
// writeOGHTMLWithURL.
func (s *Server) writeShareOGHTML(r *http.Request, w http.ResponseWriter, _ *shareLink, pageID int64, pageURL, origin string, withSlug bool) {
	var (
		title      string
		body       string
		spaceName  string
		ownerOrgID int64 // NULL space.org_id scans as 0 via COALESCE
	)
	err := s.DB.QueryRowContext(r.Context(),
		`SELECT p.title, p.body, sp.name, COALESCE(sp.org_id, 0)
		   FROM pages p
		   JOIN spaces sp ON sp.id = p.space_id
		  WHERE p.id = $1 AND p.deleted_at IS NULL`, pageID,
	).Scan(&title, &body, &spaceName, &ownerOrgID)
	if errors.Is(err, sql.ErrNoRows) {
		writeNotFoundHTML(w)
		return
	}
	if err != nil {
		writeInternalHTML(w)
		return
	}
	// Append the cosmetic slug to the share-root URL (/share/{token}/{slug}).
	// Only the root carries it — the descendant URL is already id-scoped.
	if withSlug {
		if sl := pageSlug(title); sl != "" {
			pageURL += "/" + sl
		}
	}
	writeOGHTMLWithURL(w, title, body, spaceName, pageURL, s.ogImageURL(origin, pageID), s.ogSiteName(r, ownerOrgID))
}

// writeOGHTMLWithURL is the URL-overridable variant of writeOGHTML. The /p/{id}
// flavour builds og:url from <origin>+/p/{id}; shares need /share/{token} or
// /share/{token}/p/{id}. og:image is /p/{page_id}/og.png on the SAME origin as
// og:url — the page's org custom domain when it has one, else canonical (or
// path-only in dev). The renderer is public and keyed only on page id, so it
// resolves on either host. siteName brands og:site_name (the owning/host org's
// name on a white-label domain, else "tela").
func writeOGHTMLWithURL(w http.ResponseWriter, title, body, spaceName, pageURL, imageURL, siteName string) {
	if siteName == "" {
		siteName = "tela"
	}
	ogTitle := runeTruncate(title+" — "+spaceName, 100)
	// Interim privacy fix (docs/visibility-model.md): /p/{id} emits this OG
	// envelope for ANY page to crawlers — no auth, no share link required — so a
	// body excerpt in og:description leaks private page content. Use the title
	// as the description instead (matches the title-only OG image card).
	// TODO: restore the rich body excerpt, but only for pages that have an
	// active public share link.
	//   plain := stripMarkdownToText(body)
	//   ogDesc := runeTruncate(plain, 200)
	ogDesc := runeTruncate(title, 200)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>%s</title>
  <meta property="og:site_name" content="%s">
  <meta property="og:title" content="%s">
  <meta property="og:description" content="%s">
  <meta property="og:image" content="%s">
  <meta property="og:image:width" content="1200">
  <meta property="og:image:height" content="630">
  <meta property="og:image:alt" content="%s">
  <meta property="og:url" content="%s">
  <meta property="og:type" content="article">
  <meta name="twitter:card" content="summary_large_image">
  <meta name="twitter:title" content="%s">
  <meta name="twitter:description" content="%s">
  <meta name="twitter:image" content="%s">
</head>
<body></body>
</html>`,
		html.EscapeString(ogTitle),
		html.EscapeString(siteName),
		html.EscapeString(ogTitle),
		html.EscapeString(ogDesc),
		html.EscapeString(imageURL),
		html.EscapeString(ogTitle),
		html.EscapeString(pageURL),
		html.EscapeString(ogTitle),
		html.EscapeString(ogDesc),
		html.EscapeString(imageURL),
	)
}

// writeLockedShareOGHTML emits a generic OG envelope for a password-protected
// share so crawlers don't leak the real title / description / image. og:image
// is intentionally absent — sharing the page's image card on a locked share
// would defeat the password.
func writeLockedShareOGHTML(w http.ResponseWriter, origin, token, siteName string) {
	writeGenericOGHTML(w, "Protected page on Tela",
		"This page is password-protected. Open the link to enter the password.",
		shareRootURL(origin, token), siteName)
}

// writeGenericOGHTML emits a content-free OG envelope: a fixed title and
// description, no image. For links whose page must not be described to a
// crawler (a locked share, a private page reached without its title).
func writeGenericOGHTML(w http.ResponseWriter, title, desc, pageURL, siteName string) {
	if siteName == "" {
		siteName = "tela"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>%s</title>
  <meta property="og:site_name" content="%s">
  <meta property="og:title" content="%s">
  <meta property="og:description" content="%s">
  <meta property="og:url" content="%s">
  <meta property="og:type" content="article">
  <meta name="twitter:card" content="summary">
  <meta name="twitter:title" content="%s">
  <meta name="twitter:description" content="%s">
</head>
<body></body>
</html>`,
		html.EscapeString(title),
		html.EscapeString(siteName),
		html.EscapeString(title),
		html.EscapeString(desc),
		html.EscapeString(pageURL),
		html.EscapeString(title),
		html.EscapeString(desc),
	)
}
