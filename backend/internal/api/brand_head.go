package api

import (
	"cmp"
	"html"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/zcag/tela/backend/internal/auth"
)

// brand_head.go serves the branded part of the SPA shell's <head>. index.html
// is static, so its title, favicon and boot splash were always tela's, and a
// rebranded instance (or an org custom domain) flashed tela before React loaded
// host-context. frontend/nginx.conf pulls this fragment into the shell with an
// SSI include; an EMPTY body makes nginx keep the tela defaults (the include's
// stub), so an unbranded host costs nothing.

type shellBrand struct {
	name, logoURL, accent string
}

// shellBrandFor resolves the brand this request's host wears: the org's on its
// custom domain, else the instance brand. Zero when neither is customized.
func (s *Server) shellBrandFor(r *http.Request) shellBrand {
	if oc, ok := auth.OrgContextFromContext(r.Context()); ok && oc.OrgID > 0 {
		var name string
		_ = s.DB.QueryRowContext(r.Context(), `SELECT name FROM orgs WHERE id = $1`, oc.OrgID).Scan(&name)
		logoURL, accent := s.orgBranding(r.Context(), oc.OrgID)
		if !strings.HasPrefix(logoURL, "/api/public/orgs/") {
			logoURL = ""
		}
		return shellBrand{name: name, logoURL: logoURL, accent: accent}
	}
	b := s.instanceBrand()
	if !b.custom() {
		return shellBrand{}
	}
	return shellBrand{name: b.Name, logoURL: b.logoURL(), accent: b.Accent}
}

// BrandHead: GET /api/public/brand/head. Public (the shell is served before
// login). The logo URL is our own content-addressed route and the accent passed
// accentPattern on write, so neither can break out of the attribute or <style>.
func (s *Server) BrandHead(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	b := s.shellBrandFor(r)
	if b.name == "" && b.logoURL == "" {
		w.WriteHeader(http.StatusOK)
		return
	}
	icon := b.logoURL
	if icon == "" {
		icon = initialIcon(b.name, b.accent)
	}
	var sb strings.Builder
	sb.WriteString("<title>" + html.EscapeString(cmp.Or(b.name, "tela")) + "</title>")
	sb.WriteString(`<link rel="icon" href="` + html.EscapeString(icon) + `">`)
	sb.WriteString(`<link rel="apple-touch-icon" href="` + html.EscapeString(icon) + `">`)
	sb.WriteString(`<style>#boot svg{display:none}#boot .boot-logo{display:block;background-image:url("` + icon + `")}</style>`)
	_, _ = w.Write([]byte(sb.String()))
}

// initialIcon is a rounded accent tile holding the name's first letter, as a
// data URI: the favicon and splash for a brand with a name but no logo (the OG
// card's no-logo lockup draws the same tile).
func initialIcon(name, accent string) string {
	r, _ := utf8.DecodeRuneInString(strings.TrimSpace(name))
	letter := strings.ToUpper(string(r))
	if accent == "" {
		accent = "#4f46e5"
	}
	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">` +
		`<rect width="64" height="64" rx="14" fill="` + html.EscapeString(accent) + `"/>` +
		`<text x="32" y="44" font-family="system-ui,sans-serif" font-size="34" font-weight="700" fill="#fff" text-anchor="middle">` +
		html.EscapeString(letter) + `</text></svg>`
	return "data:image/svg+xml," + url.PathEscape(svg)
}
