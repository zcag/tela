package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// org_branding.go: per-org visual branding (logo + accent + preferred deck
// variant). Not coupled to the custom-domain surface — it themes the white-label
// login/app shell AND is inherited by the org's slide decks (logo + accent). Its
// own org-settings section; managed by org admins.
//
// The logo is always stored IN tela and served from tela's own origin (a logo on
// an arbitrary external host can't be fetched by the server-side deck renderer).
// Two import paths fill the same columns: a direct upload, or import-from-URL
// (tela fetches it once). logo_url then holds the internal, content-addressed
// serve route; a legacy external URL is left until the org re-uploads/imports.

// accentPattern bounds what we store as an --accent value: a hex color or an
// oklch()/rgb()/rgba() function. The SPA injects this via setProperty (which
// rejects invalid values) — this is data hygiene, not the security boundary.
var accentPattern = regexp.MustCompile(`^(#[0-9a-fA-F]{3,8}|(oklch|rgb|rgba)\([0-9a-zA-Z%.,/\s-]+\))$`)

const orgLogoMaxBytes = 2 << 20 // 2 MB — a brand logo, not a hero image

// allowedLogoMime is the raster set http.DetectContentType reliably sniffs. SVG is
// excluded on purpose: served logo bytes are public, and an SVG can carry script
// that runs on direct navigation — raster sidesteps that surface entirely.
var allowedLogoMime = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/webp": true, "image/gif": true,
}

type orgBrandingDTO struct {
	LogoURL string `json:"logo_url"` // effective URL the SPA/deck use (tela serve route, or legacy external)
	HasLogo bool   `json:"has_logo"` // a logo is stored in tela (vs. unset or legacy-external)
	Accent  string `json:"accent"`   // brand color (hue-matched into deck variants)
}

// orgBranding returns an org's effective logo URL + accent (empty when unset).
// Used by host-context (login/app shell) and deck rendering (logo + accent
// inheritance) — both get a tela-origin serve route once a logo is stored.
func (s *Server) orgBranding(ctx context.Context, orgID int64) (logoURL, accent string) {
	_ = s.DB.QueryRowContext(ctx,
		`SELECT logo_url, accent FROM org_branding WHERE org_id = $1`, orgID).Scan(&logoURL, &accent)
	return
}

// GetOrgBranding — GET /api/orgs/{id}/branding. Org-admin.
func (s *Server) GetOrgBranding(w http.ResponseWriter, r *http.Request) {
	orgID, ok := parseOrgID(w, r)
	if !ok {
		return
	}
	if !s.requireOrgAdmin(w, r, orgID) {
		return
	}
	var dto orgBrandingDTO
	_ = s.DB.QueryRowContext(r.Context(),
		`SELECT logo_url, accent, (logo_data IS NOT NULL)
		   FROM org_branding WHERE org_id = $1`, orgID).
		Scan(&dto.LogoURL, &dto.Accent, &dto.HasLogo)
	writeJSON(w, http.StatusOK, dto)
}

type putBrandingReq struct {
	Accent        string `json:"accent"`
	LogoImportURL string `json:"logo_import_url"` // optional: fetch this once and store it as the org logo
}

// PutOrgBranding — PUT /api/orgs/{id}/branding. Org-admin. Sets the brand accent;
// optionally imports a logo from a URL. The logo bytes themselves are otherwise
// managed by the upload/delete endpoints — PUT never touches a stored logo unless
// logo_import_url is given.
func (s *Server) PutOrgBranding(w http.ResponseWriter, r *http.Request) {
	orgID, ok := parseOrgID(w, r)
	if !ok {
		return
	}
	if !s.requireOrgAdmin(w, r, orgID) {
		return
	}
	var req putBrandingReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}
	req.Accent = strings.TrimSpace(req.Accent)
	req.LogoImportURL = strings.TrimSpace(req.LogoImportURL)
	if req.Accent != "" && !accentPattern.MatchString(req.Accent) {
		writeError(w, http.StatusBadRequest, "bad_request", "accent must be a hex color or oklch()/rgb() value")
		return
	}
	if _, err := s.DB.ExecContext(r.Context(), `
		INSERT INTO org_branding (org_id, accent)
		VALUES ($1, $2)
		ON CONFLICT (org_id) DO UPDATE
		   SET accent = EXCLUDED.accent, updated_at = tela_now()`,
		orgID, req.Accent); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "save branding failed")
		return
	}
	if req.LogoImportURL != "" {
		if ae := s.importOrgLogo(r.Context(), orgID, req.LogoImportURL); ae != nil {
			writeError(w, ae.Status, ae.Code, ae.Message)
			return
		}
	}
	s.audit(r.Context(), r, "org_branding.set", "org", orgID, "")
	s.GetOrgBranding(w, r)
}

// UploadOrgLogo — POST /api/orgs/{id}/branding/logo. Org-admin. Raw image bytes
// in the body (Content-Type from the request); the file picker's File is sent
// directly. Stores it as the org logo and returns the updated branding.
func (s *Server) UploadOrgLogo(w http.ResponseWriter, r *http.Request) {
	orgID, ok := parseOrgID(w, r)
	if !ok {
		return
	}
	if !s.requireOrgAdmin(w, r, orgID) {
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, orgLogoMaxBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not read upload")
		return
	}
	if ae := s.storeOrgLogo(r.Context(), orgID, data); ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	s.audit(r.Context(), r, "org_branding.logo.upload", "org", orgID, "")
	s.GetOrgBranding(w, r)
}

// DeleteOrgLogo — DELETE /api/orgs/{id}/branding/logo. Org-admin. Clears the
// stored logo (and any legacy external URL); accent + deck variant are untouched.
func (s *Server) DeleteOrgLogo(w http.ResponseWriter, r *http.Request) {
	orgID, ok := parseOrgID(w, r)
	if !ok {
		return
	}
	if !s.requireOrgAdmin(w, r, orgID) {
		return
	}
	if _, err := s.DB.ExecContext(r.Context(),
		`UPDATE org_branding SET logo_data = NULL, logo_mime = '', logo_hash = '', logo_url = '', updated_at = tela_now()
		  WHERE org_id = $1`, orgID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "clear logo failed")
		return
	}
	s.audit(r.Context(), r, "org_branding.logo.delete", "org", orgID, "")
	s.GetOrgBranding(w, r)
}

// ServeOrgLogo — GET /api/public/orgs/{id}/logo. PUBLIC (a brand asset, shown on
// the pre-auth login screen) and content-addressed (?v=<hash>); served from
// tela's own origin so the deck renderer can always fetch it.
func (s *Server) ServeOrgLogo(w http.ResponseWriter, r *http.Request) {
	orgID, ok := parseOrgID(w, r)
	if !ok {
		return
	}
	var data []byte
	var mime, hash string
	err := s.DB.QueryRowContext(r.Context(),
		`SELECT logo_data, logo_mime, logo_hash FROM org_branding WHERE org_id = $1`, orgID).
		Scan(&data, &mime, &hash)
	if errors.Is(err, sql.ErrNoRows) || len(data) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "no logo")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "load logo failed")
		return
	}
	serveLogo(w, r, data, mime, hash)
}

// serveLogo writes stored logo bytes with a content-hash ETag. Shared by the org
// and instance logo routes.
func serveLogo(w http.ResponseWriter, r *http.Request, data []byte, mime, hash string) {
	etag := `"` + hash + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

// importOrgLogo fetches a URL once and stores the bytes as the org logo.
func (s *Server) importOrgLogo(ctx context.Context, orgID int64, raw string) *apiErr {
	data, ae := fetchLogo(ctx, raw)
	if ae != nil {
		return ae
	}
	return s.storeOrgLogo(ctx, orgID, data)
}

// fetchLogo fetches a logo URL once (SSRF-guarded, no redirects: the same dial
// guard as the unfurl title fetch).
func fetchLogo(ctx context.Context, raw string) ([]byte, *apiErr) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, &apiErr{http.StatusBadRequest, "bad_request", "logo URL must be an http(s):// URL"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, &apiErr{http.StatusBadRequest, "bad_request", "invalid logo URL"}
	}
	resp, err := newUnfurlClient().Do(req)
	if err != nil {
		return nil, &apiErr{http.StatusBadGateway, "fetch_failed", "could not fetch the logo URL: " + err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &apiErr{http.StatusBadGateway, "fetch_failed", fmt.Sprintf("logo URL returned %d", resp.StatusCode)}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, orgLogoMaxBytes+1))
	if err != nil {
		return nil, &apiErr{http.StatusBadGateway, "fetch_failed", "could not read the logo"}
	}
	return data, nil
}

// validateLogo checks raster image bytes and returns their sniffed MIME type and
// a short content hash for cache-busting.
func validateLogo(data []byte) (mime, hash string, ae *apiErr) {
	if len(data) == 0 {
		return "", "", &apiErr{http.StatusBadRequest, "bad_request", "logo is empty"}
	}
	if len(data) > orgLogoMaxBytes {
		return "", "", &apiErr{http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("logo exceeds %d MB", orgLogoMaxBytes>>20)}
	}
	mime = http.DetectContentType(data)
	if !allowedLogoMime[mime] {
		return "", "", &apiErr{http.StatusBadRequest, "bad_request", "logo must be a PNG, JPEG, WebP or GIF image"}
	}
	sum := sha256.Sum256(data)
	return mime, hex.EncodeToString(sum[:])[:16], nil
}

// storeOrgLogo validates raster image bytes, content-addresses them, and writes
// them to org_branding with logo_url pointing at the tela serve route.
func (s *Server) storeOrgLogo(ctx context.Context, orgID int64, data []byte) *apiErr {
	mime, hash, ae := validateLogo(data)
	if ae != nil {
		return ae
	}
	serveURL := fmt.Sprintf("/api/public/orgs/%d/logo?v=%s", orgID, hash)
	if _, err := s.DB.ExecContext(ctx, `
		INSERT INTO org_branding (org_id, logo_url, logo_data, logo_mime, logo_hash)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (org_id) DO UPDATE
		   SET logo_url = EXCLUDED.logo_url, logo_data = EXCLUDED.logo_data,
		       logo_mime = EXCLUDED.logo_mime, logo_hash = EXCLUDED.logo_hash, updated_at = tela_now()`,
		orgID, serveURL, data, mime, hash); err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "store logo failed"}
	}
	return nil
}
