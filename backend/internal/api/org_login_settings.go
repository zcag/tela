package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/zcag/tela/backend/internal/auth"
)

// org_login_settings.go: per-org control over which sign-in methods appear on
// that org's custom-domain login screen, plus the public host-context endpoint
// the SPA bootstraps from to brand the login screen + app shell.

// orgLoginSettings returns an org's sign-in toggles, defaulting both to enabled
// when no row exists (the instance default).
func (s *Server) orgLoginSettings(ctx context.Context, orgID int64) (passwordEnabled, socialEnabled bool) {
	passwordEnabled, socialEnabled = true, true
	var p, so int
	if err := s.DB.QueryRowContext(ctx,
		`SELECT password_enabled, social_enabled FROM org_login_settings WHERE org_id = $1`, orgID).
		Scan(&p, &so); err == nil {
		passwordEnabled, socialEnabled = p == 1, so == 1
	}
	return
}

// orgHasSSO reports whether the org has an SSO connection configured.
func (s *Server) orgHasSSO(ctx context.Context, orgID int64) bool {
	var x int
	return s.DB.QueryRowContext(ctx,
		`SELECT 1 FROM org_sso WHERE org_id = $1`, orgID).Scan(&x) == nil
}

type hostOrgDTO struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Slug    string `json:"slug"`
	LogoURL string `json:"logo_url"`
	Accent  string `json:"accent"`
}

type hostLoginDTO struct {
	PasswordEnabled bool `json:"password_enabled"`
	SocialEnabled   bool `json:"social_enabled"`
	OrgSSOAvailable bool `json:"org_sso_available"`
}

// maintenanceDTO is an admin-set instance notice (e.g. "AI is paused for
// maintenance"). nil when no notice is set. Level drives the banner styling.
type maintenanceDTO struct {
	Notice string `json:"notice"`
	Level  string `json:"level"` // "info" | "warning"
}

type hostContextDTO struct {
	Org   *hostOrgDTO  `json:"org"`
	Login hostLoginDTO `json:"login"`
	// The instance's one true origin (TELA_PUBLIC_BASE_URL). On an org custom
	// domain the SPA points tela-brand links here instead of the current host
	// (a relative "/" would land on the org domain's root). '' in dev.
	CanonicalBase string `json:"canonical_base"`
	// Maintenance notice for the app-wide banner; nil = none.
	Maintenance *maintenanceDTO `json:"maintenance,omitempty"`
	// Whether managed AI (ask / semantic search) is serving — false when the
	// embedder is unconfigured, an admin flipped the ai.disabled kill-switch, OR
	// the background health prober found the embedder/chat model unreachable (so
	// the client shows "AI unavailable" automatically during an outage).
	AIAvailable bool `json:"ai_available"`
	// The instance's own brand on the canonical host; nil when it isn't
	// rebranded or an org custom-domain brand applies instead.
	Brand *hostBrandDTO `json:"brand"`
}

type hostBrandDTO struct {
	Name    string `json:"name"`
	LogoURL string `json:"logo_url"`
	Accent  string `json:"accent"`
}

// aiEnabled reports whether managed AI should serve. False when the embedder/LLM
// is unconfigured, OR an instance admin has set the ai.disabled kill-switch
// (pause AI while its backing service is under maintenance without erroring
// loudly). The user-facing AI endpoints + host-context gate on this.
func (s *Server) aiEnabled() bool {
	if v, ok := s.settings.Get("ai.disabled"); ok && v == "1" {
		return false
	}
	return s.rag != nil && s.rag.Enabled()
}

// maintenanceNotice returns the active admin notice, or nil when unset.
func (s *Server) maintenanceNotice() *maintenanceDTO {
	notice, _ := s.settings.Get("maintenance.notice")
	notice = strings.TrimSpace(notice)
	if notice == "" {
		return nil
	}
	level, _ := s.settings.Get("maintenance.level")
	if level != "warning" {
		level = "info"
	}
	return &maintenanceDTO{Notice: notice, Level: level}
}

// HostContext — GET /api/host-context. Public (host-derived, pre-login). The
// SPA calls it on first paint to discover whether it's on an org custom domain
// (→ brand the login screen + shell with the org, and show only that org's
// enabled sign-in methods). On the canonical host it returns org=null and the
// full default method set; the SPA then uses the instance-wide providers list.
func (s *Server) HostContext(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := hostContextDTO{
		Login:         hostLoginDTO{PasswordEnabled: true, SocialEnabled: true},
		CanonicalBase: canonicalBaseURL(),
		Maintenance:   s.maintenanceNotice(),
		AIAvailable:   s.aiHealthy(),
	}

	oc, ok := auth.OrgContextFromContext(ctx)
	if !ok {
		if b := s.instanceBrand(); b.custom() || b.Accent != "" {
			out.Brand = &hostBrandDTO{Name: b.Name, LogoURL: b.logoURL(), Accent: b.Accent}
		}
		writeJSON(w, http.StatusOK, out)
		return
	}

	var name, slug string
	if err := s.DB.QueryRowContext(ctx, `SELECT name, slug FROM orgs WHERE id = $1`, oc.OrgID).
		Scan(&name, &slug); err != nil {
		// Active hostname with a missing org is a data inconsistency — degrade to
		// the canonical default rather than failing the login screen.
		writeJSON(w, http.StatusOK, out)
		return
	}
	pw, social := s.orgLoginSettings(ctx, oc.OrgID)
	logoURL, accent := s.orgBranding(ctx, oc.OrgID)
	out.Org = &hostOrgDTO{ID: oc.OrgID, Name: name, Slug: slug, LogoURL: logoURL, Accent: accent}
	out.Login = hostLoginDTO{PasswordEnabled: pw, SocialEnabled: social, OrgSSOAvailable: s.orgHasSSO(ctx, oc.OrgID)}
	writeJSON(w, http.StatusOK, out)
}

type orgLoginSettingsDTO struct {
	PasswordEnabled bool `json:"password_enabled"`
	SocialEnabled   bool `json:"social_enabled"`
}

// GetOrgLoginSettings — GET /api/orgs/{id}/login-settings. Org-admin.
func (s *Server) GetOrgLoginSettings(w http.ResponseWriter, r *http.Request) {
	orgID, ok := parseOrgID(w, r)
	if !ok {
		return
	}
	if !s.requireOrgAdmin(w, r, orgID) {
		return
	}
	pw, social := s.orgLoginSettings(r.Context(), orgID)
	writeJSON(w, http.StatusOK, orgLoginSettingsDTO{PasswordEnabled: pw, SocialEnabled: social})
}

// PutOrgLoginSettings — PUT /api/orgs/{id}/login-settings. Org-admin. Upserts
// the toggles. Rejecting all methods would lock the org out, so at least one of
// password / social / configured SSO must remain available.
func (s *Server) PutOrgLoginSettings(w http.ResponseWriter, r *http.Request) {
	orgID, ok := parseOrgID(w, r)
	if !ok {
		return
	}
	if !s.requireOrgAdmin(w, r, orgID) {
		return
	}
	var req orgLoginSettingsDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}
	if !req.PasswordEnabled && !req.SocialEnabled && !s.orgHasSSO(r.Context(), orgID) {
		writeError(w, http.StatusBadRequest, "bad_request",
			"at least one sign-in method must stay enabled (configure SSO first to disable both password and social)")
		return
	}
	if _, err := s.DB.ExecContext(r.Context(), `
		INSERT INTO org_login_settings (org_id, password_enabled, social_enabled)
		VALUES ($1, $2, $3)
		ON CONFLICT (org_id) DO UPDATE
		   SET password_enabled = EXCLUDED.password_enabled,
		       social_enabled   = EXCLUDED.social_enabled,
		       updated_at       = tela_now()`,
		orgID, boolToInt(req.PasswordEnabled), boolToInt(req.SocialEnabled)); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "save login settings failed")
		return
	}
	s.audit(r.Context(), r, "org_login_settings.set", "org", orgID, "")
	writeJSON(w, http.StatusOK, req)
}

// passwordLoginBlockedByHost reports whether the request arrived on an org
// custom domain that has disabled password sign-in — enforced server-side so
// hiding the form in the SPA isn't the only guard. Returns false on the
// canonical host (no org context) and for orgs with password enabled.
func (s *Server) passwordLoginBlockedByHost(r *http.Request) bool {
	oc, ok := auth.OrgContextFromContext(r.Context())
	if !ok {
		return false
	}
	pw, _ := s.orgLoginSettings(r.Context(), oc.OrgID)
	return !pw
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
