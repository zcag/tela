package api

import (
	"cmp"
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	githuboauth "golang.org/x/oauth2/github"
)

// Federated sign-in. Three instance-wide social providers (Google, Microsoft,
// GitHub) plus per-org OIDC SSO. The whole layer is additive: a provider
// resolves an external identity, then resolveSSOUser hands off to the same
// provisioning chain (EnsurePersonalSpace → applyAutoJoin → CreateSession) the
// email-verify flow already uses. See docs and migration 0016_sso.sql.

// ssoProvider is one configured login source. OIDC providers (Google,
// Microsoft, per-org connections) carry an id_token verifier; GitHub is plain
// OAuth2 and resolves identity from its REST API instead.
type ssoProvider struct {
	name        string                // 'google' | 'microsoft' | 'github' | 'org'
	label       string                // button label on the login screen
	oauth2      *oauth2.Config        // authorize/token + redirect + scopes
	verifier    *oidc.IDTokenVerifier // nil → OAuth2-only (GitHub)
	userInfoURL string                // GitHub: identity endpoint
	emailsURL   string                // GitHub: verified-emails endpoint
	issuerOK    func(string) bool     // optional manual iss check (Microsoft 'common')
	trustEmail  bool                  // Microsoft: no email_verified; trust the email only with xms_edov (see identityFromOIDC)
}

func (p *ssoProvider) isOIDC() bool { return p.verifier != nil }

// ssoRegistry holds the instance-wide social providers built from env at boot.
// Per-org OIDC connections are built on demand from the org_sso row (buildOrg-
// Provider), so they don't live here. Never nil — an instance with no TELA_SSO_*
// env just has an empty map (every social button hidden), the same no-op-until-
// configured posture as the RAG embedder and MCP OAuth.
type ssoRegistry struct {
	social map[string]*ssoProvider
}

func (r *ssoRegistry) lookup(name string) (*ssoProvider, bool) {
	p, ok := r.social[name]
	return p, ok
}

// ssoCallbackURL is the registered redirect URI for a provider segment, on a
// given origin. Social providers use their own name ('google') and a fixed
// canonical origin (one registered redirect URI per instance). Org connections
// share the 'org' segment (the org id rides in the signed state) but follow the
// request's origin — an org on a custom domain registers
// https://tela.ngss.io/api/auth/sso/org/callback at its IdP. OIDC requires the
// redirect_uri at authorize and token-exchange to byte-match the registration,
// so the org callback must use the same origin at both ends (it does — the IdP
// returns to that host, and providerForCallback rebuilds from the same request).
func ssoCallbackURL(origin, provider string) string {
	return origin + "/api/auth/sso/" + provider + "/callback"
}

// loadSSOProviders builds the social registry from env. Each provider is skipped
// (with a log line) when its client id/secret is unset or discovery fails, so a
// misconfigured provider degrades to "button hidden", never a boot failure.
func loadSSOProviders(ctx context.Context) *ssoRegistry {
	reg := &ssoRegistry{social: map[string]*ssoProvider{}}

	// Social providers register a single canonical redirect URI per instance —
	// they are NOT offered on org custom-domain login screens (org SSO is), so
	// they never need a per-host callback. See custom_domains.go / docs.
	origin := cmp.Or(canonicalBaseURL(), devBaseURL)

	if id, sec := os.Getenv("TELA_SSO_GOOGLE_CLIENT_ID"), os.Getenv("TELA_SSO_GOOGLE_CLIENT_SECRET"); id != "" && sec != "" {
		if p, err := buildOIDCProvider(ctx, origin, "google", "Google", "https://accounts.google.com",
			id, sec, []string{oidc.ScopeOpenID, "email", "profile"}, false, nil); err != nil {
			slog.Warn("sso: google disabled", "err", err)
		} else {
			reg.social["google"] = p
		}
	}

	if id, sec := os.Getenv("TELA_SSO_MICROSOFT_CLIENT_ID"), os.Getenv("TELA_SSO_MICROSOFT_CLIENT_SECRET"); id != "" && sec != "" {
		// The multi-tenant 'common' endpoint advertises a templated issuer
		// (.../{tenantid}/v2.0) that won't match any single string, so discovery
		// runs under InsecureIssuerURLContext and the verifier skips the built-in
		// issuer check; issuerOK re-imposes a strict host+suffix check on the real
		// per-tenant `iss` at verify time so we don't blindly trust any issuer.
		const msIssuer = "https://login.microsoftonline.com/common/v2.0"
		issuerOK := func(iss string) bool {
			return strings.HasPrefix(iss, "https://login.microsoftonline.com/") && strings.HasSuffix(iss, "/v2.0")
		}
		if p, err := buildOIDCProvider(oidc.InsecureIssuerURLContext(ctx, msIssuer), origin, "microsoft", "Microsoft", msIssuer,
			id, sec, []string{oidc.ScopeOpenID, "email", "profile"}, true, issuerOK); err != nil {
			slog.Warn("sso: microsoft disabled", "err", err)
		} else {
			p.trustEmail = true
			reg.social["microsoft"] = p
		}
	}

	if id, sec := os.Getenv("TELA_SSO_GITHUB_CLIENT_ID"), os.Getenv("TELA_SSO_GITHUB_CLIENT_SECRET"); id != "" && sec != "" {
		reg.social["github"] = &ssoProvider{
			name:  "github",
			label: "GitHub",
			oauth2: &oauth2.Config{
				ClientID:     id,
				ClientSecret: sec,
				Endpoint:     githuboauth.Endpoint,
				RedirectURL:  ssoCallbackURL(origin, "github"),
				Scopes:       []string{"read:user", "user:email"},
			},
			userInfoURL: "https://api.github.com/user",
			emailsURL:   "https://api.github.com/user/emails",
		}
	}

	if n := len(reg.social); n > 0 {
		slog.Info("sso: social providers enabled", "count", n)
	}
	return reg
}

// buildOIDCProvider runs OIDC discovery against issuer and wires the oauth2
// config + id_token verifier. discoveryCtx is the ctx passed to NewProvider (the
// Microsoft path overrides it via InsecureIssuerURLContext). skipIssuerCheck +
// issuerOK cover multi-tenant issuers; for normal single-issuer providers both
// are zero-valued.
func buildOIDCProvider(discoveryCtx context.Context, origin, name, label, issuer, clientID, clientSecret string, scopes []string, skipIssuerCheck bool, issuerOK func(string) bool) (*ssoProvider, error) {
	provider, err := oidc.NewProvider(discoveryCtx, issuer)
	if err != nil {
		return nil, err
	}
	return &ssoProvider{
		name:  name,
		label: label,
		oauth2: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  ssoCallbackURL(origin, name),
			Scopes:       scopes,
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: clientID, SkipIssuerCheck: skipIssuerCheck}),
		issuerOK: issuerOK,
	}, nil
}

// buildOrgProvider constructs a per-org OIDC provider from its org_sso row at
// request time (org connections are dynamic, so they aren't held in the
// registry). origin is the request's origin so the 'org' redirect URI follows
// the org's custom domain when the login happens there; the authorize and
// callback legs must pass the same origin (they do — same host round-trip).
func buildOrgProvider(ctx context.Context, origin, issuer, clientID, clientSecret string) (*ssoProvider, error) {
	return buildOIDCProvider(ctx, origin, "org", "SSO", issuer, clientID, clientSecret,
		[]string{oidc.ScopeOpenID, "email", "profile"}, false, nil)
}
