package api

import (
	"context"
	"fmt"
	"image/color"
	"net/http"
	"strings"

	"github.com/zcag/tela/backend/internal/auth"
	"github.com/zcag/tela/backend/internal/mailer"
)

// email_brand.go resolves the per-org branding a transactional email should
// carry. Branding applies ONLY where the org has claimed a custom domain — the
// same white-label condition as the app chrome, OG cards, and the share/public
// readers — so a generic tela instance still sends plain tela mail. The accent
// is reduced to an email-safe hex (clients don't render oklch), and the logo URL
// is absolutized so it loads in an inbox with no app session.

// emailBrandForRequest brands an email triggered by a request that arrived on an
// org custom domain (verify/reset). Zero Brand on the canonical host.
func (s *Server) emailBrandForRequest(r *http.Request) mailer.Brand {
	oc, ok := auth.OrgContextFromContext(r.Context())
	if !ok || oc.OrgID <= 0 {
		return s.instanceEmailBrand(s.linkOrigin(r))
	}
	return s.emailBrand(r.Context(), oc.OrgID, s.linkOrigin(r))
}

// emailBrandForSpace brands a notification email from the space's owning org,
// but only when that org has an active custom domain (else zero Brand → tela).
// The logo loads from that domain's origin.
func (s *Server) emailBrandForSpace(ctx context.Context, spaceID *int64) mailer.Brand {
	if spaceID == nil {
		return s.instanceEmailBrand(canonicalBaseURL())
	}
	host, ok := s.spaceOrgPrimaryHost(ctx, *spaceID)
	if !ok {
		return s.instanceEmailBrand(canonicalBaseURL())
	}
	return s.emailBrand(ctx, s.spaceOwnerOrg(ctx, *spaceID), "https://"+host)
}

// emailBrand builds the brand for orgID: its name, the logo absolutized against
// origin, and an email-safe hex accent. A blank name (missing org) or no stored
// branding yields a name-only / zero brand, which renders as tela downstream.
func (s *Server) emailBrand(ctx context.Context, orgID int64, origin string) mailer.Brand {
	if orgID <= 0 {
		return mailer.Brand{}
	}
	var name string
	if s.DB.QueryRowContext(ctx, `SELECT name FROM orgs WHERE id = $1`, orgID).Scan(&name) != nil {
		return mailer.Brand{}
	}
	logoURL, accent := s.orgBranding(ctx, orgID)
	b := mailer.Brand{Name: name}
	if logoURL != "" {
		b.LogoURL = absEmailURL(origin, logoURL)
	}
	if c, ok := parseAccentRGBA(accent); ok {
		b.Accent = hexColor(c)
	}
	return b
}

// instanceEmailBrand is the email brand where no org applies: the instance's
// own brand when it is customized, else zero (tela). The logo is absolutized
// against origin so it loads in an inbox.
func (s *Server) instanceEmailBrand(origin string) mailer.Brand {
	b := s.instanceBrand()
	if !b.custom() {
		return mailer.Brand{}
	}
	out := mailer.Brand{Name: b.Name}
	if u := b.logoURL(); u != "" && origin != "" {
		out.LogoURL = absEmailURL(origin, u)
	}
	if c, ok := parseAccentRGBA(b.Accent); ok {
		out.Accent = hexColor(c)
	}
	return out
}

// absEmailURL makes the stored logo URL absolute: the in-tela serve route is a
// root-relative path that must be prefixed with the org's origin to load in an
// inbox; a legacy external URL is already absolute.
func absEmailURL(origin, logoURL string) string {
	if strings.HasPrefix(logoURL, "http://") || strings.HasPrefix(logoURL, "https://") {
		return logoURL
	}
	return strings.TrimRight(origin, "/") + logoURL
}

// hexColor renders an opaque RGBA as #rrggbb — the one color form every email
// client renders (unlike oklch / CSS vars).
func hexColor(c color.RGBA) string {
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}
