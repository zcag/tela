package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/zcag/tela/backend/internal/models"
)

// Deck render. A "deck" page's body IS Slidev markdown; the deck render sidecar
// (deck/) renders it. The look lives entirely in the slidev-theme-tahta package;
// tela passes only a per-deck visual config (variant/accent/lang, from page
// props). Mirrors pdf_export.go's gotenberg proxy: the backend never renders
// markdown itself, it proxies the sidecar.
//
// Present is the live Slidev SPA (real presenter/overview/drawing), built by the
// sidecar and served page-scoped + membership-gated. PNG/PDF/PPTX stay for export
// + the MCP preview_deck tool + thumbnails.
//
//   GET  /api/pages/{id}/deck/spa/{path...} (gated)  — live interactive SPA (Present)
//   GET  /api/pages/{id}/deck/outline       (gated)  — structure, no render
//   POST /api/pages/{id}/deck/parse         (gated)  — parse a draft (editor outline)
//   POST /api/pages/{id}/deck/lint          (gated)  — validate a draft (editor advisory)
//   GET  /api/pages/{id}/deck.pdf|.pptx      (gated)  — export
//   GET  /api/deck/d/{renderId}/{file}      (public) — a rendered PNG (content-addressed)

const deckRenderTO = 180 * time.Second

// deckBaseURL is the internal address of the deck render sidecar.
func deckBaseURL() string {
	if v := os.Getenv("TELA_DECK_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://deck:3344"
}

// deckConfig is tela's per-deck visual config — the only inputs to the theme.
// The whole look lives in the slidev-theme-tahta package; tela just declares
// which variant (and optional accent/lang/logo).
type deckConfig struct {
	Variant    string
	Accent     string
	Lang       string
	Logo       string // tahta themeConfig.logo — brand mark (hero on openers, footer mark)
	LogoInvert bool   // tahta themeConfig.logoInvert — flip a monochrome mark for the scheme
}

// deckThemeConfig reads the per-deck visual config: explicit page props first
// (the editor's selector / an agent write them), then the page's owning org's
// brand IDENTITY (logo + accent) fills any unset values — so org decks carry the
// brand mark + color automatically.
//
// The VARIANT is deliberately NOT inherited or defaulted here. The variant is the
// biggest visual decision (typeface/scheme/texture) and must be a conscious
// per-deck choice, never a silent default that lets the author (human or agent)
// coast — the org's preferred variant is only a non-binding UI recommendation, not
// an applied value. An unset variant falls back to the theme's own default at
// render time (in the sidecar) purely as a don't-crash safety net, not a choice we
// make for the deck.
func (s *Server) deckThemeConfig(ctx context.Context, p models.Page) deckConfig {
	str := func(k string) string {
		if v, ok := p.Props[k].(string); ok {
			return v
		}
		return ""
	}
	cfg := deckConfig{Variant: str("variant"), Accent: str("accent"), Lang: str("lang"), Logo: str("logo")}
	if v, ok := p.Props["logoInvert"].(bool); ok {
		cfg.LogoInvert = v
	}
	// Inherit brand identity (logo + accent only — NOT variant) from the owning org.
	if cfg.Accent == "" || cfg.Logo == "" {
		logo, accent := s.deckOrgBrand(ctx, p.SpaceID)
		if cfg.Accent == "" {
			cfg.Accent = accent
		}
		if cfg.Logo == "" {
			cfg.Logo = logo
		}
	}
	// A logo stored as a tela attachment is a root-relative URL the sidecar
	// renderer can't resolve — make it absolute (external https logos pass through).
	cfg.Logo = absolutizeAsset(cfg.Logo)
	return cfg
}

// deckOrgBrand resolves a space's owning org's brand identity (logo + accent) in
// one lookup. Both empty for a personal space (org_id NULL) or an org with no
// branding row. The org's preferred deck VARIANT is intentionally not read here —
// it's a UI recommendation only, never applied as a default (see deckThemeConfig).
func (s *Server) deckOrgBrand(ctx context.Context, spaceID int64) (logo, accent string) {
	_ = s.DB.QueryRowContext(ctx, `
		SELECT COALESCE(ob.logo_url, ''), COALESCE(ob.accent, '')
		  FROM spaces s
		  LEFT JOIN org_branding ob ON ob.org_id = s.org_id
		 WHERE s.id = $1`, spaceID).Scan(&logo, &accent)
	return
}

// deckManifest is the sidecar /render result — static frames for export + the MCP
// preview_deck tool.
type deckManifest struct {
	ID      string   `json:"id"`
	Count   int      `json:"count"`
	Variant string   `json:"variant"`
	Slides  []string `json:"slides"`
}

// requirePageRead is the single gate for page-scoped read routes (deck spa /
// outline / parse / export): resolve {id}, collapse not-found to 403 (no
// enumeration), and require read membership of the page's space. On any failure
// it writes the response and returns ok=false.
func (s *Server) requirePageRead(w http.ResponseWriter, r *http.Request) (models.Page, bool) {
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return models.Page{}, false
	}
	p, err := selectPageByID(r.Context(), s.DB, id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusForbidden, "forbidden", "not a member")
		return models.Page{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "fetch page failed")
		return models.Page{}, false
	}
	if _, ok := s.requireMembership(w, r, p.SpaceID); !ok {
		return models.Page{}, false
	}
	return p, true
}

// ServePageDeckSPA (GET /api/pages/{id}/deck/spa/{path...}): membership-gated.
// Serves one file of the deck's live interactive Slidev SPA (real presenter /
// overview / drawing). The SPA is built lazily + cached in the sidecar; the
// backend forwards the deck body + the base path it serves under + the requested
// file, and streams the sidecar's response (content-type + cache-control). Every
// asset request passes requirePageRead, so a leaked path can't expose another
// space's deck or its speaker notes.
func (s *Server) ServePageDeckSPA(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requirePageRead(w, r)
	if !ok {
		return
	}
	s.streamDeckSPA(w, r, p, fmt.Sprintf("/api/pages/%d/deck/spa/", p.ID))
}

// streamDeckSPA fetches one file of the deck's built SPA from the sidecar (built
// under `base` so asset URLs resolve) and streams it. Shared by the membership-
// gated and the public (public-space) Present routes — the only difference is the
// gate the caller applied and the base path the SPA is served under.
func (s *Server) streamDeckSPA(w http.ResponseWriter, r *http.Request, p models.Page, base string) {
	file := r.PathValue("path") // "" → the sidecar serves index.html
	resp, err := deckSPA(r.Context(), p.Body, s.deckThemeConfig(r.Context(), p), base, file)
	if err != nil {
		writeError(w, http.StatusBadGateway, "deck_unavailable", "deck service unavailable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeDeckSPAError(w, resp.StatusCode)
		return
	}
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	if cc := resp.Header.Get("Cache-Control"); cc != "" {
		w.Header().Set("Cache-Control", cc)
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, resp.Body)
}

// writeDeckSPAError turns a non-200 from the sidecar's SPA build into a
// meaningful client error instead of a bare 404 (which read as "page missing"
// and hid the real cause — a structurally broken deck that the build choked on).
// A sidecar 404 is a genuinely missing asset file; a 400 is a deck it couldn't
// parse; anything else is a build failure. Both deck cases point the author at
// lint_deck — the structural error is exactly what it reports.
func writeDeckSPAError(w http.ResponseWriter, sidecarStatus int) {
	switch sidecarStatus {
	case http.StatusNotFound:
		writeError(w, http.StatusNotFound, "not_found", "not found")
	case http.StatusBadRequest:
		writeError(w, http.StatusUnprocessableEntity, "deck_invalid", "this deck couldn't be parsed — it has a structural error (run lint_deck to find it)")
	default:
		writeError(w, http.StatusBadGateway, "deck_build_failed", "this deck failed to build — it likely has a structural error (run lint_deck to find it)")
	}
}

// ServePublicDeckSPA (GET /api/public/spaces/{id}/pages/{page_id}/deck/spa/{path...}):
// PUBLIC, self-authenticating on space visibility=public (publicSpacePage). Makes a
// public space's decks presentable to logged-out visitors — "public means public".
func (s *Server) ServePublicDeckSPA(w http.ResponseWriter, r *http.Request) {
	p, ok := s.publicSpacePage(w, r)
	if !ok {
		return
	}
	if !isDeckBag(p.Props) {
		writeError(w, http.StatusNotFound, "not_found", "not a deck")
		return
	}
	s.streamDeckSPA(w, r, p, fmt.Sprintf("/api/public/spaces/%d/pages/%d/deck/spa/", p.SpaceID, p.ID))
}

// deckCoverResult mirrors the sidecar /cover response.
type deckCoverResult struct {
	URL   string `json:"url"` // sidecar-relative, e.g. /d/<id>/1.png — served at /api/deck<url>
	Count int    `json:"count"`
}

// deckCover renders (cached) the deck's first slide via the sidecar and returns the
// sidecar-relative asset URL + slide count. The asset is public + content-addressed
// (ServeDeckAsset). Cheaper than a full render — one frame.
func deckCover(ctx context.Context, body string, cfg deckConfig) (deckCoverResult, error) {
	var out deckCoverResult
	resp, err := deckPost(ctx, "/cover", body, cfg)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, deckErr(resp)
	}
	err = json.NewDecoder(resp.Body).Decode(&out)
	return out, err
}

// deckCoverPNG fetches the deck's first-slide PNG bytes (for the OG share image,
// which proxies bytes rather than redirecting — crawlers don't always follow 302s).
// Time-bounded so a cold render can't hang a crawler; returns ok=false to let the
// caller fall back. cfg is built from page props directly (no models.Page needed).
func (s *Server) deckCoverPNG(ctx context.Context, body string, props map[string]any, spaceID int64) ([]byte, string, bool) {
	cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	cfg := s.deckThemeConfig(cctx, models.Page{Props: props, SpaceID: spaceID})
	cov, err := deckCover(cctx, body, cfg)
	if err != nil || cov.URL == "" {
		return nil, "", false
	}
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, deckBaseURL()+cov.URL, nil)
	if err != nil {
		return nil, "", false
	}
	resp, err := (&http.Client{Timeout: 12 * time.Second}).Do(req)
	if err != nil {
		return nil, "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", false
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", false
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "image/png"
	}
	return b, ct, true
}

// serveDeckCover redirects to the deck's first-slide image — the public, immutable,
// content-addressed asset. Shared by the gated + public cover endpoints; 404 for a
// non-deck page.
func (s *Server) serveDeckCover(w http.ResponseWriter, r *http.Request, p models.Page) {
	if !isDeckBag(p.Props) {
		writeError(w, http.StatusNotFound, "not_found", "not a deck")
		return
	}
	cov, err := deckCover(r.Context(), p.Body, s.deckThemeConfig(r.Context(), p))
	if err != nil || cov.URL == "" {
		writeError(w, http.StatusBadGateway, "deck_unavailable", "deck cover unavailable")
		return
	}
	// The redirect target is content-addressed + immutable, but the redirect
	// itself is keyed only by page — cache it briefly so a warm cover isn't
	// re-resolved through the sidecar on every view. Short enough that an edit's
	// new render is picked up promptly.
	w.Header().Set("Cache-Control", "private, max-age=60")
	http.Redirect(w, r, "/api/deck"+cov.URL, http.StatusFound)
}

// ServePageDeckCover (GET /api/pages/{id}/deck/cover): membership-gated. The deck's
// first-slide thumbnail for the in-app deck view.
func (s *Server) ServePageDeckCover(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requirePageRead(w, r)
	if !ok {
		return
	}
	s.serveDeckCover(w, r, p)
}

// ServePublicDeckCover (GET /api/public/spaces/{id}/pages/{page_id}/deck/cover):
// PUBLIC (public-space gate). The first-slide cover for the public index card +
// reader hero.
func (s *Server) ServePublicDeckCover(w http.ResponseWriter, r *http.Request) {
	p, ok := s.publicSpacePage(w, r)
	if !ok {
		return
	}
	s.serveDeckCover(w, r, p)
}

// ServeDeckAsset (GET /api/deck/d/{renderId}/{file}): PUBLIC (auth.IsPublicPath).
// Proxies a rendered slide image / PDF from the sidecar. Content-addressed +
// immutable — renderId is an unguessable content hash (the /api/diagrams posture).
func (s *Server) ServeDeckAsset(w http.ResponseWriter, r *http.Request) {
	renderID := r.PathValue("renderId")
	file := r.PathValue("file")
	if !deckSafeSeg(renderID) || !deckSafeSeg(file) {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid asset path")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet,
		deckBaseURL()+"/d/"+renderID+"/"+file, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "request build failed")
		return
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "deck_unavailable", "deck service unavailable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeError(w, http.StatusNotFound, "not_found", "asset not found")
		return
	}
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, resp.Body)
}

// ExportPageDeckPDF (GET /api/pages/{id}/deck.pdf): session-authed. Exports the
// deck to a downloadable PDF via the sidecar.
func (s *Server) ExportPageDeckPDF(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requirePageRead(w, r)
	if !ok {
		return
	}
	s.streamDeckPDF(w, r, p)
}

// streamDeckPDF renders a deck page to a per-slide PDF via the Slidev sidecar and
// writes it as a download. Shared by the explicit /deck.pdf route and the generic
// /pdf route's deck branch (so /pdf produces the real slide PDF, not a gotenberg
// wall-of-text of the Slidev source). Caller has already gated read access.
func (s *Server) streamDeckPDF(w http.ResponseWriter, r *http.Request, p models.Page) {
	pdf, err := deckExport(r.Context(), p.Body, s.deckThemeConfig(r.Context(), p), "pdf")
	if err != nil {
		writeError(w, http.StatusBadGateway, "deck_render_failed", "could not export deck")
		return
	}
	noStore(w)
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", pdfFilename(p.Title)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdf)
}

// ExportPageDeckPPTX (GET /api/pages/{id}/deck.pptx): session-authed. Exports
// the deck to a downloadable PowerPoint via the sidecar.
func (s *Server) ExportPageDeckPPTX(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requirePageRead(w, r)
	if !ok {
		return
	}
	pptx, err := deckExport(r.Context(), p.Body, s.deckThemeConfig(r.Context(), p), "pptx")
	if err != nil {
		writeError(w, http.StatusBadGateway, "deck_render_failed", "could not export deck")
		return
	}
	noStore(w)
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.presentationml.presentation")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", pageFileSlug(p.Title)+".pptx"))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pptx)
}

// ExportPageDeckMarkdown (GET /api/pages/{id}/deck.md): session-authed. Returns
// the deck's raw Slidev source — the page body verbatim (headmatter and all),
// tela asset URLs absolutized so images resolve outside tela. Unlike the generic
// /md export (which prepends a tela frontmatter block for round-trip), this is
// directly usable with `slidev slides.md`; the reader still supplies the
// slidev-theme-tahta look (npm) itself. The runnable-project bundle is a
// deliberate non-goal here (see docs/deck.md).
func (s *Server) ExportPageDeckMarkdown(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requirePageRead(w, r)
	if !ok {
		return
	}
	if !isDeckBag(p.Props) {
		writeError(w, http.StatusBadRequest, "not_a_deck", "page is not a deck")
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", pageFileSlug(p.Title)+".md"))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(absolutizeDeckAssets(p.Body)))
}

// GetPageDeckOutline (GET /api/pages/{id}/deck/outline): session-authed. Returns
// the deck's structure (slide count, titles, layouts, speaker notes, detected
// features) via the sidecar's /parse — no render, no Chromium. Powers the deck's
// default-view identity and the editor outline.
func (s *Server) GetPageDeckOutline(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requirePageRead(w, r)
	if !ok {
		return
	}
	resp, err := deckPost(r.Context(), "/parse", p.Body, deckConfig{})
	if err != nil {
		writeError(w, http.StatusBadGateway, "deck_unavailable", "deck service unavailable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeError(w, http.StatusBadGateway, "deck_parse_failed", "could not parse deck")
		return
	}
	noStore(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, resp.Body)
}

// PostPageDeckParse (POST /api/pages/{id}/deck/parse): session-authed. Parses
// the DRAFT markdown in the request body (the live editor buffer, not the saved
// page) into deck structure via the sidecar's /parse — no render. Powers the
// live editor outline. Page-scoped so it isn't an open parser proxy; the body is
// the unsaved text, so it can't reuse the saved-body /outline route.
func (s *Server) PostPageDeckParse(w http.ResponseWriter, r *http.Request) {
	body, ok := s.readDeckDraft(w, r)
	if !ok {
		return
	}
	resp, err := deckPost(r.Context(), "/parse", body, deckConfig{})
	if err != nil {
		writeError(w, http.StatusBadGateway, "deck_unavailable", "deck service unavailable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeError(w, http.StatusBadGateway, "deck_parse_failed", "could not parse deck")
		return
	}
	noStore(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, resp.Body)
}

// readDeckDraft gates on page read access and returns the DRAFT markdown from the
// request body — the shared front half of the two editor-buffer routes (/parse,
// /lint). Page-scoped so neither is an open proxy to the sidecar.
func (s *Server) readDeckDraft(w http.ResponseWriter, r *http.Request) (string, bool) {
	if _, ok := s.requirePageRead(w, r); !ok {
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20)) // decks are markdown; 4MB is ample
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not read body")
		return "", false
	}
	return string(body), true
}

// PostPageDeckLint (POST /api/pages/{id}/deck/lint): session-authed. Validates the
// DRAFT editor buffer against the theme contract — the same tahta-lint report the
// agent-facing lint_deck tool returns and deckWriteGate blocks agent writes on.
//
// Advisory ONLY. Agent writes are gated because an agent ships and walks away; a
// human autosaves keystroke-by-keystroke through transiently-broken states, so
// gating them would fight the editor. But not gating had meant no signal at all:
// a structural error (e.g. a raw `"` inside a `:prop="…"` binding, which fails the
// whole slidev build) stayed invisible until Present 502'd. This route makes it
// visible while they type without ever blocking a save.
func (s *Server) PostPageDeckLint(w http.ResponseWriter, r *http.Request) {
	body, ok := s.readDeckDraft(w, r)
	if !ok {
		return
	}
	out, err := s.deckLint(r.Context(), body)
	if err != nil {
		writeError(w, http.StatusBadGateway, "deck_unavailable", "deck service unavailable")
		return
	}
	noStore(w)
	writeJSON(w, http.StatusOK, out)
}

// ServeDeckThemes (GET /api/deck/themes): PUBLIC. Proxies the sidecar's theme
// list for the editor's theme selector.
func (s *Server) ServeDeckThemes(w http.ResponseWriter, r *http.Request) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, deckBaseURL()+"/themes", nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "request build failed")
		return
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "deck_unavailable", "deck service unavailable")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// ── proxy core ──────────────────────────────────────────────────────────────

func deckRender(ctx context.Context, body string, cfg deckConfig) (*deckManifest, error) {
	resp, err := deckPost(ctx, "/render", body, cfg)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, deckErr(resp)
	}
	var m deckManifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

func deckExport(ctx context.Context, body string, cfg deckConfig, format string) ([]byte, error) {
	resp, err := deckPost(ctx, "/export/"+format, body, cfg)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, deckErr(resp)
	}
	return io.ReadAll(resp.Body)
}

// deckSPA fetches one file of the deck's built interactive SPA from the sidecar
// (build-if-needed, cached). `base` is the page-scoped path the SPA is served
// under (baked into the build so asset URLs resolve); `file` is the requested
// asset ("" → index.html).
func deckSPA(ctx context.Context, body string, cfg deckConfig, base, file string) (*http.Response, error) {
	q := deckQuery(cfg)
	q.Set("base", base)
	q.Set("file", file)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, deckBaseURL()+"/spa?"+q.Encode(), strings.NewReader(absolutizeDeckAssets(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/markdown")
	return (&http.Client{Timeout: deckRenderTO}).Do(req)
}

// deckQuery turns the per-deck visual config into the sidecar's query params.
func deckQuery(cfg deckConfig) url.Values {
	q := url.Values{}
	if cfg.Variant != "" {
		q.Set("variant", cfg.Variant)
	}
	if cfg.Accent != "" {
		q.Set("accent", cfg.Accent)
	}
	if cfg.Lang != "" {
		q.Set("lang", cfg.Lang)
	}
	if cfg.Logo != "" {
		q.Set("logo", cfg.Logo)
	}
	if cfg.LogoInvert {
		q.Set("logoInvert", "1")
	}
	return q
}

// deckRelAssetRe matches a root-relative tela asset URL (attachment/diagram/
// public file) used as a markdown/HTML/frontmatter reference — captured by its
// leading delimiter so an already-absolute URL (…://host/api/files/…) is left
// alone. The deck render sidecar fetches images from ITS OWN origin, so a
// root-relative /api/files/… 404s in the Chromium export (and the built SPA);
// absolutizing against the canonical base makes tela-hosted deck images (uploaded
// or treated) and tela-hosted org logos render everywhere, not just same-origin.
var deckRelAssetRe = regexp.MustCompile(`(^|[\s"'(\[])(/api/(?:files|diagrams|public)/)`)

// absolutizeDeckAssets rewrites root-relative tela asset URLs in deck markdown to
// absolute (canonical base). No-op when TELA_PUBLIC_BASE_URL is unset.
func absolutizeDeckAssets(body string) string {
	base := canonicalBaseURL()
	if base == "" {
		return body
	}
	return deckRelAssetRe.ReplaceAllString(body, "${1}"+base+"${2}")
}

// absolutizeAsset makes a single root-relative URL (e.g. an org logo stored as a
// tela attachment) absolute against the canonical base. External https URLs and
// already-absolute URLs pass through unchanged.
func absolutizeAsset(u string) string {
	if strings.HasPrefix(u, "/") {
		if base := canonicalBaseURL(); base != "" {
			return base + u
		}
	}
	return u
}

func deckPost(ctx context.Context, path, body string, cfg deckConfig) (*http.Response, error) {
	u := deckBaseURL() + path
	if q := deckQuery(cfg); len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(absolutizeDeckAssets(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/markdown")
	return (&http.Client{Timeout: deckRenderTO}).Do(req)
}

// deckTreat runs raw image bytes through the sidecar's tahta-imagine treat step
// (crop → scheme-aware duotone/none → grain → optional scrim) for the variant,
// returning the treated JPEG. Deterministic + local to the sidecar — no model.
func deckTreat(ctx context.Context, img []byte, variant, mode, scrim string) ([]byte, error) {
	q := url.Values{}
	if variant != "" {
		q.Set("variant", variant)
	}
	if mode != "" {
		q.Set("mode", mode)
	}
	if scrim != "" {
		q.Set("scrim", scrim)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, deckBaseURL()+"/treat?"+q.Encode(), bytes.NewReader(img))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := (&http.Client{Timeout: deckRenderTO}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, deckErr(resp)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}

func deckErr(resp *http.Response) error {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("deck %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
}

// deckSafeSeg bounds a proxied path segment (the sidecar also guards traversal).
func deckSafeSeg(s string) bool {
	if s == "" || len(s) > 64 || strings.Contains(s, "..") {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
