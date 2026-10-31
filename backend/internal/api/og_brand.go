package api

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	_ "image/gif" // register GIF decoder for org logos
	"net/http"
	"strconv"
	"strings"

	_ "golang.org/x/image/webp" // register WebP decoder for org logos

	"github.com/zcag/tela/backend/internal/auth"
)

// og_brand.go resolves the per-org branding a crawler OG card should carry, so a
// link shared on an org's custom domain (e.g. ngss.io) unfurls with the org's
// logo, accent, and name instead of the generic "tela" card. The pure rendering
// of that brand lives in og_image.go; this file is the DB + request half.
//
// Which org's brand wins mirrors ogOriginForPage: the request's OWN
// custom-domain org first (the host actually in the shared URL), else the
// page/space's owning org. So a member's deep link copied from a white-label
// domain stays branded even when the space itself carries no org_id, and a
// crawler that fetches the image off the canonical host still gets the owning
// org's brand.

// ogBrand is the resolved branding for a crawler card. The zero value (no name,
// no accent, no logo) renders the default tela card, so every caller can fall
// back by passing ogBrand{}.
type ogBrand struct {
	name      string      // org display name; "" → "tela"
	accent    color.RGBA  // brand accent (opaque); only meaningful when hasAccent
	hasAccent bool        // accent parsed to RGBA (oklch() values don't, and fall back)
	logo      image.Image // decoded org logo, nil when none/undecodable
	sig       string      // short identity hash (name|accent|logo) for cache-busting
}

// ogBrandOrgID picks which org's brand a card carries. ownerOrgID is the
// page/space owning org (0 when none/unknown, e.g. a user home or personal
// space). The request's custom-domain org takes precedence.
func (s *Server) ogBrandOrgID(r *http.Request, ownerOrgID int64) int64 {
	if oc, ok := auth.OrgContextFromContext(r.Context()); ok && oc.OrgID > 0 {
		return oc.OrgID
	}
	return ownerOrgID
}

// resolveOGBrand loads the full brand (name + accent + decoded logo) for the org
// that should brand this request's card. Returns the zero brand when no org
// applies or its row is blank — never errors the caller (crawler path).
func (s *Server) resolveOGBrand(r *http.Request, ownerOrgID int64) ogBrand {
	return s.loadOGBrand(r.Context(), s.ogBrandOrgID(r, ownerOrgID))
}

func (s *Server) loadOGBrand(ctx context.Context, orgID int64) ogBrand {
	if orgID <= 0 {
		return s.instanceOGBrand(ctx)
	}
	var (
		name, accent, logoMime, logoHash string
		logoData                         []byte
	)
	err := s.DB.QueryRowContext(ctx, `
		SELECT o.name, COALESCE(b.accent, ''), COALESCE(b.logo_mime, ''),
		       COALESCE(b.logo_hash, ''), b.logo_data
		  FROM orgs o
		  LEFT JOIN org_branding b ON b.org_id = o.id
		 WHERE o.id = $1`, orgID).Scan(&name, &accent, &logoMime, &logoHash, &logoData)
	if err != nil {
		return ogBrand{}
	}
	br := ogBrand{name: name}
	if c, ok := parseAccentRGBA(accent); ok {
		br.accent, br.hasAccent = c, true
	}
	if len(logoData) > 0 {
		if img, _, derr := image.Decode(bytes.NewReader(logoData)); derr == nil {
			br.logo = img
		}
	}
	sum := sha256.Sum256([]byte(name + "|" + accent + "|" + logoHash))
	br.sig = hex.EncodeToString(sum[:])[:12]
	return br
}

// instanceOGBrand is the card brand where no org applies: the instance's own
// brand when it is customized, else the zero (tela) brand.
func (s *Server) instanceOGBrand(ctx context.Context) ogBrand {
	b := s.instanceBrand()
	if !b.custom() && b.Accent == "" {
		return ogBrand{}
	}
	br := ogBrand{name: b.Name}
	if c, ok := parseAccentRGBA(b.Accent); ok {
		br.accent, br.hasAccent = c, true
	}
	if b.LogoHash != "" {
		var logoData []byte
		if s.DB.QueryRowContext(ctx, `SELECT logo_data FROM instance_branding WHERE id = 1`).Scan(&logoData) == nil && len(logoData) > 0 {
			if img, _, err := image.Decode(bytes.NewReader(logoData)); err == nil {
				br.logo = img
			}
		}
	}
	sum := sha256.Sum256([]byte("instance|" + b.Name + "|" + b.Accent + "|" + b.LogoHash))
	br.sig = hex.EncodeToString(sum[:])[:12]
	return br
}

// ogSiteName resolves only the og:site_name for the HTML crawler envelope (no
// logo decode). The org name on a branded surface, else "tela".
func (s *Server) ogSiteName(r *http.Request, ownerOrgID int64) string {
	orgID := s.ogBrandOrgID(r, ownerOrgID)
	if orgID <= 0 {
		return cmp.Or(s.instanceBrand().Name, "tela")
	}
	var name string
	if s.DB.QueryRowContext(r.Context(),
		`SELECT name FROM orgs WHERE id = $1`, orgID).Scan(&name) == nil && strings.TrimSpace(name) != "" {
		return name
	}
	return "tela"
}

// pageOwnerOrg returns the org that owns a page's space (0 when the page is
// missing or its space has no org). Used as the brand fallback in the HTML
// crawler path, where the owning org isn't already in hand.
func (s *Server) pageOwnerOrg(ctx context.Context, pageID int64) int64 {
	var orgID int64 // NULL org_id scans as 0 via COALESCE
	_ = s.DB.QueryRowContext(ctx,
		`SELECT COALESCE(sp.org_id, 0) FROM pages p JOIN spaces sp ON sp.id = p.space_id WHERE p.id = $1`,
		pageID).Scan(&orgID)
	return orgID
}

// spaceOwnerOrg returns the org that owns a space (0 when none/personal).
func (s *Server) spaceOwnerOrg(ctx context.Context, spaceID int64) int64 {
	var orgID int64
	_ = s.DB.QueryRowContext(ctx,
		`SELECT COALESCE(org_id, 0) FROM spaces WHERE id = $1`, spaceID).Scan(&orgID)
	return orgID
}

// parseAccentRGBA parses a stored accent (org_branding.accent) into an opaque
// RGBA. Handles hex (#rgb/#rgba/#rrggbb/#rrggbbaa) and rgb()/rgba() with 0–255
// channels. oklch() and anything unrecognized return ok=false so the renderer
// falls back to its default accent — converting oklch to sRGB isn't worth a
// color-space dependency for a link-preview card.
func parseAccentRGBA(s string) (color.RGBA, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return color.RGBA{}, false
	}
	if strings.HasPrefix(s, "#") {
		return parseHexColor(s[1:])
	}
	if strings.HasPrefix(s, "rgb(") || strings.HasPrefix(s, "rgba(") {
		inner := s[strings.IndexByte(s, '(')+1 : len(s)-1]
		inner = strings.ReplaceAll(inner, "/", ",")
		parts := strings.FieldsFunc(inner, func(r rune) bool { return r == ',' || r == ' ' })
		if len(parts) < 3 {
			return color.RGBA{}, false
		}
		ch := func(p string) (uint8, bool) {
			n, err := strconv.Atoi(strings.TrimSpace(p))
			if err != nil || n < 0 || n > 255 {
				return 0, false
			}
			return uint8(n), true
		}
		rr, okR := ch(parts[0])
		gg, okG := ch(parts[1])
		bb, okB := ch(parts[2])
		if !okR || !okG || !okB {
			return color.RGBA{}, false
		}
		return color.RGBA{R: rr, G: gg, B: bb, A: 0xff}, true
	}
	return color.RGBA{}, false
}

func parseHexColor(h string) (color.RGBA, bool) {
	// Expand shorthand #rgb / #rgba to full form.
	if len(h) == 3 || len(h) == 4 {
		var sb strings.Builder
		for _, c := range h {
			sb.WriteRune(c)
			sb.WriteRune(c)
		}
		h = sb.String()
	}
	if len(h) != 6 && len(h) != 8 {
		return color.RGBA{}, false
	}
	b, err := hex.DecodeString(h)
	if err != nil {
		return color.RGBA{}, false
	}
	return color.RGBA{R: b[0], G: b[1], B: b[2], A: 0xff}, true // alpha forced opaque
}
