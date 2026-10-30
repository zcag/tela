package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// instance_branding.go: the instance's own brand (name, accent, logo). Org
// branding only applies on an org's custom domain, so a self-hosted wiki served
// on its canonical host had no way to stop calling itself tela. This brand is
// the canonical-host counterpart: the shell, tab titles, OG cards and emails use
// it whenever no org custom-domain brand applies. Instance-admin managed; seeded
// once from TELA_BRAND_NAME / TELA_BRAND_ACCENT / TELA_BRAND_LOGO_URL on a fresh
// instance, after which Settings is authoritative.

const instanceBrandNameMax = 60

type instanceBrand struct {
	Name     string
	Accent   string
	LogoHash string
}

// custom reports whether the instance is rebranded (a name or logo). An accent
// alone recolors tela but doesn't replace it.
func (b instanceBrand) custom() bool { return b.Name != "" || b.LogoHash != "" }

func (b instanceBrand) logoURL() string {
	if b.LogoHash == "" {
		return ""
	}
	return "/api/public/instance/logo?v=" + b.LogoHash
}

func (s *Server) instanceBrand() instanceBrand {
	if b := s.brand.Load(); b != nil {
		return *b
	}
	return instanceBrand{}
}

func (s *Server) loadInstanceBrand(ctx context.Context) {
	var b instanceBrand
	err := s.DB.QueryRowContext(ctx,
		`SELECT name, accent, logo_hash FROM instance_branding WHERE id = 1`).
		Scan(&b.Name, &b.Accent, &b.LogoHash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		slog.Warn("instance brand: load", "err", err)
	}
	s.brand.Store(&b)
}

func (s *Server) initInstanceBrand(ctx context.Context) {
	s.seedInstanceBrandFromEnv(ctx)
	s.loadInstanceBrand(ctx)
}

// seedInstanceBrandFromEnv writes the env brand on first boot only (no row yet),
// the same env-then-settings precedence as TELA_REGISTRATION_OPEN. The logo URL
// is fetched in the background so a slow or dead host never delays boot; a
// failure is logged and the logo can be uploaded in Settings instead.
func (s *Server) seedInstanceBrandFromEnv(ctx context.Context) {
	name := strings.TrimSpace(os.Getenv("TELA_BRAND_NAME"))
	accent := strings.TrimSpace(os.Getenv("TELA_BRAND_ACCENT"))
	logoURL := strings.TrimSpace(os.Getenv("TELA_BRAND_LOGO_URL"))
	if name == "" && accent == "" && logoURL == "" {
		return
	}
	var exists bool
	if err := s.DB.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM instance_branding WHERE id = 1)`).Scan(&exists); err != nil || exists {
		return
	}
	name, err := normalizeBrandName(name)
	if err != nil {
		slog.Warn("instance brand: ignoring TELA_BRAND_NAME", "err", err)
		name = ""
	}
	if accent != "" && !accentPattern.MatchString(accent) {
		slog.Warn("instance brand: ignoring TELA_BRAND_ACCENT, not a hex/oklch()/rgb() color", "value", accent)
		accent = ""
	}
	if _, err := s.DB.ExecContext(ctx,
		`INSERT INTO instance_branding (id, name, accent) VALUES (1, $1, $2) ON CONFLICT (id) DO NOTHING`,
		name, accent); err != nil {
		slog.Warn("instance brand: seed from env", "err", err)
		return
	}
	if logoURL == "" {
		return
	}
	go func() {
		fctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		data, ae := fetchLogo(fctx, logoURL)
		if ae == nil {
			ae = s.storeInstanceLogo(fctx, data)
		}
		if ae != nil {
			slog.Warn("instance brand: TELA_BRAND_LOGO_URL not imported; upload the logo in Settings > Instance", "err", ae.Message)
			return
		}
		s.loadInstanceBrand(fctx)
	}()
}

// normalizeBrandName trims a brand name and rejects control characters and
// overlong values. "tela" (any case) normalizes to "" = the default brand.
func normalizeBrandName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > instanceBrandNameMax {
		return "", errors.New("brand name is too long")
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", errors.New("brand name has control characters")
	}
	if strings.EqualFold(name, "tela") {
		return "", nil
	}
	return name, nil
}

type instanceBrandingDTO struct {
	Name    string `json:"name"`
	Accent  string `json:"accent"`
	LogoURL string `json:"logo_url"`
	HasLogo bool   `json:"has_logo"`
}

// GetInstanceBranding: GET /api/admin/branding. Instance-admin.
func (s *Server) GetInstanceBranding(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireInstanceAdmin(w, r); !ok {
		return
	}
	b := s.instanceBrand()
	writeJSON(w, http.StatusOK, instanceBrandingDTO{Name: b.Name, Accent: b.Accent, LogoURL: b.logoURL(), HasLogo: b.LogoHash != ""})
}

type putInstanceBrandingReq struct {
	Name          string `json:"name"`
	Accent        string `json:"accent"`
	LogoImportURL string `json:"logo_import_url"`
}

// PutInstanceBranding: PUT /api/admin/branding. Instance-admin. Sets name and
// accent; optionally imports a logo from a URL. A stored logo is otherwise only
// changed by the upload/delete endpoints.
func (s *Server) PutInstanceBranding(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireInstanceAdmin(w, r); !ok {
		return
	}
	var req putInstanceBrandingReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}
	name, err := normalizeBrandName(req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	accent := strings.TrimSpace(req.Accent)
	if accent != "" && !accentPattern.MatchString(accent) {
		writeError(w, http.StatusBadRequest, "bad_request", "accent must be a hex color or oklch()/rgb() value")
		return
	}
	if _, err := s.DB.ExecContext(r.Context(), `
		INSERT INTO instance_branding (id, name, accent) VALUES (1, $1, $2)
		ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, accent = EXCLUDED.accent, updated_at = tela_now()`,
		name, accent); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "save branding failed")
		return
	}
	if u := strings.TrimSpace(req.LogoImportURL); u != "" {
		data, ae := fetchLogo(r.Context(), u)
		if ae == nil {
			ae = s.storeInstanceLogo(r.Context(), data)
		}
		if ae != nil {
			s.loadInstanceBrand(r.Context())
			writeError(w, ae.Status, ae.Code, ae.Message)
			return
		}
	}
	s.loadInstanceBrand(r.Context())
	s.audit(r.Context(), r, "instance_branding.set", "instance", 0, "")
	s.GetInstanceBranding(w, r)
}

// UploadInstanceLogo: POST /api/admin/branding/logo. Instance-admin. Raw image
// bytes in the body.
func (s *Server) UploadInstanceLogo(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireInstanceAdmin(w, r); !ok {
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, orgLogoMaxBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not read upload")
		return
	}
	if ae := s.storeInstanceLogo(r.Context(), data); ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	s.loadInstanceBrand(r.Context())
	s.audit(r.Context(), r, "instance_branding.logo.upload", "instance", 0, "")
	s.GetInstanceBranding(w, r)
}

// DeleteInstanceLogo: DELETE /api/admin/branding/logo. Instance-admin.
func (s *Server) DeleteInstanceLogo(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireInstanceAdmin(w, r); !ok {
		return
	}
	if _, err := s.DB.ExecContext(r.Context(),
		`UPDATE instance_branding SET logo_data = NULL, logo_mime = '', logo_hash = '', updated_at = tela_now() WHERE id = 1`); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "clear logo failed")
		return
	}
	s.loadInstanceBrand(r.Context())
	s.audit(r.Context(), r, "instance_branding.logo.delete", "instance", 0, "")
	s.GetInstanceBranding(w, r)
}

// ServeInstanceLogo: GET /api/public/instance/logo. Public: it brands the
// pre-auth login screen, the boot splash and the favicon.
func (s *Server) ServeInstanceLogo(w http.ResponseWriter, r *http.Request) {
	var data []byte
	var mime, hash string
	err := s.DB.QueryRowContext(r.Context(),
		`SELECT logo_data, logo_mime, logo_hash FROM instance_branding WHERE id = 1`).Scan(&data, &mime, &hash)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && len(data) == 0) {
		writeError(w, http.StatusNotFound, "not_found", "no logo")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "load logo failed")
		return
	}
	serveLogo(w, r, data, mime, hash)
}

func (s *Server) storeInstanceLogo(ctx context.Context, data []byte) *apiErr {
	mime, hash, ae := validateLogo(data)
	if ae != nil {
		return ae
	}
	if _, err := s.DB.ExecContext(ctx, `
		INSERT INTO instance_branding (id, logo_data, logo_mime, logo_hash) VALUES (1, $1, $2, $3)
		ON CONFLICT (id) DO UPDATE SET logo_data = EXCLUDED.logo_data, logo_mime = EXCLUDED.logo_mime,
		       logo_hash = EXCLUDED.logo_hash, updated_at = tela_now()`,
		data, mime, hash); err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "store logo failed"}
	}
	return nil
}
