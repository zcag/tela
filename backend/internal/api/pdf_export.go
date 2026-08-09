package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// PDF export (#3). A page is rendered to PDF by pointing gotenberg's headless
// Chromium at the SAME reading-mode UI a human sees, then streaming the result
// back. No server-side markdown→HTML pipeline: the PDF *is* the reader, so
// callouts, code, tables, mermaid/katex and excalidraw all render with zero
// drift. Two entry surfaces share renderPDF:
//   - GET /api/pages/{id}/pdf            (session-authed) — own page; mints a
//     short-lived signed token so gotenberg can read just that one page.
//   - GET /api/share/{token}/pdf[?p=ID]  (public)         — the ".pdf on a share
//     URL" trick; the share token already authorizes public read.
//
// Gotenberg renders via the INTERNAL proxy origin (TELA_PDF_RENDER_BASE_URL,
// default http://proxy) — never the public CF-fronted URL — so we sidestep both
// CDN caching of the .pdf and any headless-browser edge challenge. Every PDF
// response is no-store (browser + CDN) so an edit is never served stale.

const (
	printTokenTTL = 5 * time.Minute
	gotenbergTO   = 60 * time.Second
)

// gotenbergBaseURL is the internal address of the gotenberg HTTP API.
func gotenbergBaseURL() string {
	if v := os.Getenv("TELA_GOTENBERG_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://gotenberg:3000"
}

// pdfRenderBaseURL is the origin gotenberg's Chromium navigates to in order to
// load the reader. Internal (the compose proxy) by default so rendering never
// round-trips through Cloudflare.
func pdfRenderBaseURL() string {
	if v := os.Getenv("TELA_PDF_RENDER_BASE_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://proxy"
}

// ── short-lived, stateless print token ──────────────────────────────────────
// HMAC over "pageID.exp" with a key derived from the share secret. Least-
// privilege: grants read of one page's title+body for ~5 minutes, nothing else.
// Stateless → no table, no migration.

func (s *Server) printTokenKey() []byte {
	h := hmac.New(sha256.New, s.shareSecret)
	h.Write([]byte("tela-pdf-export-v1"))
	return h.Sum(nil)
}

func (s *Server) mintPrintToken(pageID int64) string {
	payload := fmt.Sprintf("%d.%d", pageID, time.Now().Add(printTokenTTL).Unix())
	mac := hmac.New(sha256.New, s.printTokenKey())
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) verifyPrintToken(tok string) (int64, bool) {
	parts := strings.SplitN(tok, ".", 2)
	if len(parts) != 2 {
		return 0, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return 0, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, false
	}
	mac := hmac.New(sha256.New, s.printTokenKey())
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return 0, false
	}
	var pageID, exp int64
	if _, err := fmt.Sscanf(string(payload), "%d.%d", &pageID, &exp); err != nil {
		return 0, false
	}
	if time.Now().Unix() > exp {
		return 0, false
	}
	return pageID, true
}

// ── handlers ────────────────────────────────────────────────────────────────

// ExportPagePDF (GET /api/pages/{id}/pdf): session-authed. Renders the caller's
// own page to PDF via a short-lived print token.
func (s *Server) ExportPagePDF(w http.ResponseWriter, r *http.Request) {
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
	// A deck's body is Slidev markdown, not prose — render it as real per-slide
	// frames (Slidev sidecar) instead of feeding the raw source to gotenberg,
	// which would dump headmatter + `---` separators as a wall of text.
	if isDeckBag(p.Props) {
		s.streamDeckPDF(w, r, p)
		return
	}
	url := pdfRenderBaseURL() + "/print/" + s.mintPrintToken(id) + themeQuery(r)
	s.streamPDF(w, r, url, p.Title)
}

// GetPrintPage (GET /api/print/{token}): PUBLIC (auth.IsPublicPath). Verifies
// the print token and returns one page's title/body — the data the /print SPA
// route renders for gotenberg. Least-privilege; no session.
func (s *Server) GetPrintPage(w http.ResponseWriter, r *http.Request) {
	pageID, ok := s.verifyPrintToken(r.PathValue("token"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "invalid or expired token")
		return
	}
	p, err := selectPageByID(r.Context(), s.DB, pageID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "page not found")
		return
	}
	noStore(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"page": map[string]any{
			"id":         p.ID,
			"title":      p.Title,
			"body":       p.Body,
			"updated_at": p.UpdatedAt,
			"source_url": canonicalBaseURL(),
		},
	})
}

// ExportSharePDF (GET /api/share/{token}/pdf[?p=ID]): PUBLIC. Powers the
// `/share/<token>.pdf` trick (Caddy rewrites the pretty URL here). Renders the
// public share reader to PDF. Password-protected shares are not supported (the
// gotenberg render context can't carry the unlock cookie).
func (s *Server) ExportSharePDF(w http.ResponseWriter, r *http.Request) {
	share, ok := s.publicShareLookup(w, r)
	if !ok {
		return
	}
	if share.PasswordHash.Valid {
		writeError(w, http.StatusBadRequest, "pdf_unavailable",
			"PDF export is not available for password-protected shares")
		return
	}

	target := share.PageID
	if pStr := r.URL.Query().Get("p"); pStr != "" {
		pid, err := strconv.ParseInt(pStr, 10, 64)
		if err != nil || pid <= 0 {
			writeError(w, http.StatusNotFound, "not_found", "page not in share scope")
			return
		}
		if pid != share.PageID {
			if !share.IncludeDescendants {
				writeError(w, http.StatusNotFound, "not_found", "page not in share scope")
				return
			}
			inScope, err := pageInShareSubtree(r.Context(), s.DB, share.PageID, pid)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "scope check failed")
				return
			}
			if !inScope {
				writeError(w, http.StatusNotFound, "not_found", "page not in share scope")
				return
			}
		}
		target = pid
	}

	// A shared deck exports as real per-slide frames too (same reasoning as the
	// authed /pdf route) — the share token already gated scope above.
	p, err := selectPageByID(r.Context(), s.DB, target)
	if err == nil && isDeckBag(p.Props) {
		s.streamDeckPDF(w, r, p)
		return
	}

	url := pdfRenderBaseURL() + "/share/" + share.Token
	if target != share.PageID {
		url += "/p/" + strconv.FormatInt(target, 10)
	}
	url += themeQuery(r)
	title := ""
	if err == nil {
		title = p.Title
	}
	s.streamPDF(w, r, url, title)
}

// ── rendering core ──────────────────────────────────────────────────────────

// streamPDF renders pageURL via gotenberg and writes the PDF as a no-store
// attachment. Centralises the gotenberg error → 502 mapping and the caching
// headers (so neither browser nor Cloudflare ever serves a stale render).
func (s *Server) streamPDF(w http.ResponseWriter, r *http.Request, pageURL, title string) {
	pdf, err := renderPDF(r.Context(), pageURL, title)
	if err != nil {
		writeError(w, http.StatusBadGateway, "pdf_render_failed", "could not render PDF")
		return
	}
	noStore(w)
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", pdfFilename(title)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdf)
}

// renderPDF POSTs to gotenberg's Chromium URL→PDF route. Plain client (internal
// trusted service, so no SSRF guard needed). Waits on the reader's
// window.__telaPdfReady flag so async content (mermaid/katex/diagrams/fonts) is
// painted before capture, and uploads a footer template (page title + N of M).
func renderPDF(ctx context.Context, pageURL, title string) ([]byte, error) {
	return gotenbergRender(ctx, "/forms/chromium/convert/url", map[string]string{
		"url":               pageURL,
		"emulatedMediaType": "print",
		"printBackground":   "true",
		"waitForExpression": pdfReadyExpr,
		"marginTop":         "0.55",
		"marginBottom":      "0.7",
		"marginLeft":        "0.6",
		"marginRight":       "0.6",
	}, map[string][]byte{"footer.html": []byte(footerHTML(title))})
}

// renderScreenshot captures the same reader page as a full-height JPEG, for
// preview_page's image mode. Screen media (not print) so what comes back is the
// page as a reader sees it, and JPEG at a modest width because a long page's
// screenshot is base64'd into a tool result.
func renderScreenshot(ctx context.Context, pageURL string) ([]byte, error) {
	return gotenbergRender(ctx, "/forms/chromium/screenshot/url", map[string]string{
		"url":               pageURL,
		"format":            "jpeg",
		"quality":           "70",
		"width":             "1100",
		"waitForExpression": pdfReadyExpr,
	}, nil)
}

// pdfReadyExpr is the reader's own "async content is painted" flag — mermaid,
// katex, diagrams and fonts all settle before it flips, so a render never
// captures a half-drawn page.
const pdfReadyExpr = "window.__telaPdfReady === true"

// gotenbergRender POSTs a multipart form to one of gotenberg's chromium routes
// and returns the rendered bytes. Plain client (internal trusted service, so no
// SSRF guard needed).
func gotenbergRender(ctx context.Context, route string, fields map[string]string, files map[string][]byte) ([]byte, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return nil, err
		}
	}
	for name, data := range files {
		fw, err := mw.CreateFormFile("files", name)
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write(data); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, gotenbergBaseURL()+route, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := (&http.Client{Timeout: gotenbergTO}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("gotenberg %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return io.ReadAll(resp.Body)
}

// footerHTML is gotenberg's per-page footer template: page title on the left,
// "Page N of M" on the right (gotenberg substitutes its .pageNumber/.totalPages
// spans). Title is HTML-escaped — it's user content.
func footerHTML(title string) string {
	t := html.EscapeString(strings.TrimSpace(title))
	if t == "" {
		t = "Untitled"
	}
	// Brand is the "tela" wordmark in brand indigo, centered. The folded-paper
	// mark is deliberately NOT an inline SVG/image here: gotenberg's headless-
	// Chrome footer template renders in a restricted context where SVG/external
	// images are unreliable, so the wordmark is the robust choice.
	return `<html><head><meta charset="utf-8"><style>
  body{margin:0;font-family:-apple-system,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;
       font-size:8px;color:#9a9aa6;width:100%;-webkit-print-color-adjust:exact;}
  .f{display:flex;justify-content:space-between;align-items:center;
     width:100%;box-sizing:border-box;padding:0 1.5cm;}
  .t{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:55%;}
  .b{font-weight:700;letter-spacing:-0.02em;color:#6366f1;}
</style></head><body>
  <div class="f"><span class="t">` + t + `</span>
  <span class="b">tela</span>
  <span>Page <span class="pageNumber"></span> of <span class="totalPages"></span></span></div>
</body></html>`
}

// pdfFilename turns a page title into a safe download filename.
func pdfFilename(title string) string { return pageFileSlug(title) + ".pdf" }

// pageFileSlug turns a page title into a safe download-filename stem (no ext).
func pageFileSlug(title string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "page"
	}
	if len(slug) > 80 {
		slug = strings.Trim(slug[:80], "-")
	}
	return slug
}

// themeQuery validates a caller-supplied ?theme= and returns it as a query
// string to append to the reader URL gotenberg loads (so the PDF renders in the
// chosen theme). Empty/unknown → "" (the reader's default light). Only the three
// real themes are allowed — the value is reflected into a URL, so it's bounded.
func themeQuery(r *http.Request) string {
	switch r.URL.Query().Get("theme") {
	case "light", "dark", "warm":
		return "?theme=" + r.URL.Query().Get("theme")
	}
	return ""
}

// noStore marks a response uncacheable by both the browser and Cloudflare so an
// edit is never served stale (the .pdf URL has a CDN-cacheable extension).
func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("CDN-Cache-Control", "no-store")
}
