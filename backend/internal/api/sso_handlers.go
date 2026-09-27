package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/zcag/tela/backend/internal/auth"
)

// SSO login endpoints, all under /api/auth/ (already on auth.IsPublicPath, so
// they self-authenticate). Flow: /start sets a signed state cookie and bounces
// to the IdP; the IdP returns to /callback, which validates state + nonce,
// exchanges the code, resolves the identity, and signs the user in via
// signInSSO. The 'org' provider segment is shared by every per-org connection
// (the org id rides inside the signed state).

const ssoStateCookie = "tela_sso_state"
const ssoStateTTL = 10 * time.Minute

// ssoState is the signed, self-contained login state. It lives only in the
// browser (HMAC-signed cookie) — no server-side store — so a callback can
// recover which provider/org, the OIDC nonce, the CSRF token, and the return
// path without shared state across instances.
type ssoState struct {
	Provider         string `json:"p"`           // route segment: google|microsoft|github|org
	IdentityProvider string `json:"ip"`          // durable key, e.g. 'org:5' or 'google'
	OrgID            int64  `json:"o,omitempty"` // set for org connections
	Nonce            string `json:"n"`           // OIDC nonce (replay defense)
	Token            string `json:"t"`           // CSRF token == OAuth state param
	Next             string `json:"x"`           // sanitized same-origin return path
	Exp              int64  `json:"e"`           // unix expiry
}

type ssoProviderDTO struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}

// SSOProviders lists the social buttons the login screen should render plus
// whether org SSO is configured anywhere (so the UI can show the "Sign in with
// SSO" affordance). Public, unauthenticated.
func (s *Server) SSOProviders(w http.ResponseWriter, r *http.Request) {
	out := []ssoProviderDTO{}
	for name, p := range s.sso.social {
		out = append(out, ssoProviderDTO{Name: name, Label: p.label})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	var orgSSO bool
	_ = s.DB.QueryRowContext(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM org_sso)`).Scan(&orgSSO)

	writeJSON(w, http.StatusOK, map[string]any{"providers": out, "org_sso": orgSSO})
}

// SSOStart builds the authorize URL for {provider}, sets the signed state
// cookie, and redirects to the IdP. For 'org' it resolves the connection from
// ?domain= (or ?email=) via the org's auto-join domain mapping.
func (s *Server) SSOStart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	provider := r.PathValue("provider")
	// r.FormValue reads query (GET: social buttons, custom-domain direct) OR POST
	// body. The org email-prompt POSTs so the work email never lands in a GET URL
	// — a login URL carrying an email + a next-to-login redirect is the textbook
	// phishing shape that gets the domain flagged by Safe Browsing.
	next := sanitizeNext(r.FormValue("next"))

	var (
		p                *ssoProvider
		identityProvider string
		orgID            int64
	)
	if provider == "org" {
		var (
			conn orgSSOConn
			ok   bool
			err  error
		)
		// On the org's own custom domain we already know which org this is, so
		// the SSO flow needs no email/domain prompt — resolve straight from the
		// host context. Off a custom domain, fall back to email/domain matching.
		if oc, hasOrg := auth.OrgContextFromContext(ctx); hasOrg {
			conn, ok, err = s.orgSSOByID(ctx, oc.OrgID)
		} else {
			domain := normalizeDomain(r.FormValue("domain"))
			if domain == "" {
				domain = emailDomain(normalizeEmail(r.FormValue("email")))
			}
			if domain == "" {
				writeError(w, http.StatusBadRequest, "bad_request", "a domain or email is required")
				return
			}
			conn, ok, err = s.orgSSOByDomain(ctx, domain)
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "lookup sso failed")
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "no SSO is configured for that domain")
			return
		}
		// Usage-time entitlement gate: an org that lost its Enterprise entitlement
		// (downgrade, lapsed license) can't be used to start an SSO login even if a
		// connection row survives. Same not-configured response — don't leak plan
		// state at the login screen.
		if !s.entitled(ctx, account{Kind: accountOrg, ID: conn.OrgID}, "sso") {
			writeError(w, http.StatusNotFound, "not_found", "no SSO is configured for that domain")
			return
		}
		pp, err := buildOrgProvider(ctx, s.linkOrigin(r), conn.Issuer, conn.ClientID, conn.ClientSecret)
		if err != nil {
			writeError(w, http.StatusBadGateway, "sso_unavailable", "could not reach the SSO provider")
			return
		}
		p, orgID, identityProvider = pp, conn.OrgID, fmt.Sprintf("org:%d", conn.OrgID)
	} else {
		pp, ok := s.sso.lookup(provider)
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "unknown sso provider")
			return
		}
		p, identityProvider = pp, provider
	}

	st := ssoState{
		Provider:         provider,
		IdentityProvider: identityProvider,
		OrgID:            orgID,
		Nonce:            randomSecret(),
		Token:            randomSecret(),
		Next:             next,
		Exp:              time.Now().Add(ssoStateTTL).Unix(),
	}
	s.setSSOStateCookie(w, st)

	opts := []oauth2.AuthCodeOption{}
	if p.isOIDC() {
		opts = append(opts, oidc.Nonce(st.Nonce))
	}
	http.Redirect(w, r, p.oauth2.AuthCodeURL(st.Token, opts...), http.StatusFound)
}

// SSOCallback validates the returned state, exchanges the code, resolves the
// external identity, and signs the user in. Any failure bounces back to the
// login screen with ?sso_error= rather than dumping a raw error.
func (s *Server) SSOCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	provider := r.PathValue("provider")

	st, ok := s.readSSOStateCookie(r)
	s.clearSSOStateCookie(w)
	if !ok || st.Provider != provider || time.Now().Unix() > st.Exp {
		ssoFail(w, r, "your sign-in session expired; please try again")
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(st.Token)) != 1 {
		ssoFail(w, r, "invalid sign-in state")
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		ssoFail(w, r, "the provider denied the sign-in")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		ssoFail(w, r, "the provider returned no authorization code")
		return
	}

	p, err := s.providerForCallback(ctx, s.linkOrigin(r), provider, st.OrgID)
	if err != nil {
		ssoFail(w, r, "the SSO provider is unavailable")
		return
	}

	tok, err := p.oauth2.Exchange(ctx, code)
	if err != nil {
		ssoFail(w, r, "could not complete the token exchange")
		return
	}

	var id ssoIdentity
	if p.isOIDC() {
		id, err = p.identityFromOIDC(ctx, tok, st.Nonce)
	} else {
		id, err = p.identityFromGitHub(ctx, tok)
	}
	if err != nil || id.subject == "" {
		ssoFail(w, r, "could not read your profile from the provider")
		return
	}
	id.provider = st.IdentityProvider
	id.email = normalizeEmail(id.email)

	if id.email == "" {
		ssoFail(w, r, "your account has no usable email address")
		return
	}
	if provider == "org" {
		// An org IdP is authoritative only for its own domains: only auto-link
		// to an existing tela account when the email domain belongs to this org.
		id.linkTrusted = s.orgOwnsEmailDomain(ctx, st.OrgID, id.email)
	} else if !id.linkTrusted && !s.ssoIdentityLinked(ctx, id) {
		// Social providers must vouch for the email (email_verified) before we
		// either adopt an existing account or mint one on it. A returning user is
		// matched by (provider, subject), never by email, so they don't need to:
		// resolveSSOUser takes its step (1) and the email is never consulted.
		ssoFail(w, r, "your email address is not verified with the provider")
		return
	}

	if _, err := s.signInSSO(w, r, id); err != nil {
		if errors.Is(err, errSSOEmailTaken) {
			ssoFail(w, r, "an account already exists for this email; sign in with your original method")
			return
		}
		slog.Error("sso: sign-in failed", "provider", id.provider, "err", err)
		ssoFail(w, r, "could not complete sign-in")
		return
	}
	http.Redirect(w, r, st.Next, http.StatusFound)
}

// providerForCallback re-materializes the provider for a callback: the social
// registry for a named provider, or a freshly built org provider from the
// org_sso row carried in the state.
func (s *Server) providerForCallback(ctx context.Context, origin, provider string, orgID int64) (*ssoProvider, error) {
	if provider != "org" {
		p, ok := s.sso.lookup(provider)
		if !ok {
			return nil, errors.New("unknown provider")
		}
		return p, nil
	}
	conn, ok, err := s.orgSSOByID(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("org sso not found")
	}
	// origin must match SSOStart's so the redirect_uri byte-matches at token
	// exchange (OIDC requirement). Same host round-trips through the IdP.
	return buildOrgProvider(ctx, origin, conn.Issuer, conn.ClientID, conn.ClientSecret)
}

// identityFromOIDC verifies the id_token (signature, audience, nonce, and the
// optional manual issuer check) and pulls the standard claims.
func (p *ssoProvider) identityFromOIDC(ctx context.Context, tok *oauth2.Token, nonce string) (ssoIdentity, error) {
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return ssoIdentity{}, errors.New("no id_token in response")
	}
	idt, err := p.verifier.Verify(ctx, raw)
	if err != nil {
		return ssoIdentity{}, err
	}
	if idt.Nonce != nonce {
		return ssoIdentity{}, errors.New("nonce mismatch")
	}
	if p.issuerOK != nil && !p.issuerOK(idt.Issuer) {
		return ssoIdentity{}, errors.New("issuer not allowed")
	}
	var c struct {
		Email             string `json:"email"`
		EmailVerified     bool   `json:"email_verified"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
		EDOV              any    `json:"xms_edov"`
	}
	if err := idt.Claims(&c); err != nil {
		return ssoIdentity{}, err
	}
	name := c.Name
	if name == "" {
		name = c.PreferredUsername
	}
	return ssoIdentity{
		subject:     idt.Subject,
		email:       c.Email,
		displayName: name,
		linkTrusted: c.EmailVerified || (p.trustEmail && c.Email != "" && claimTrue(c.EDOV)),
	}, nil
}

// claimTrue reads a boolean-ish optional claim. Microsoft's email claim is
// unverified and set by whoever administers the issuing tenant, and 'common'
// accepts any tenant, so trusting it let anyone with their own tenant claim a
// victim's address and be linked into their account (nOAuth). xms_edov ("email
// domain owner verified") is the claim that proves the tenant owns the email's
// domain; it's optional and must be enabled on the app registration. Read
// defensively: a bool, or its string/number spelling.
func claimTrue(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "1"
	case float64:
		return x == 1
	}
	return false
}

// ssoIdentityLinked reports whether this (provider, subject) already belongs to
// an active account, i.e. this is a returning user rather than a first login.
func (s *Server) ssoIdentityLinked(ctx context.Context, id ssoIdentity) bool {
	var ok bool
	err := s.DB.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM sso_identities si JOIN users u ON u.id = si.user_id
		               WHERE si.provider = $1 AND si.subject = $2 AND u.is_active = 1)`,
		id.provider, id.subject).Scan(&ok)
	return err == nil && ok
}

// identityFromGitHub resolves identity from GitHub's REST API (it isn't OIDC):
// the numeric id as subject, and the primary verified email from /user/emails.
func (p *ssoProvider) identityFromGitHub(ctx context.Context, tok *oauth2.Token) (ssoIdentity, error) {
	client := p.oauth2.Client(ctx, tok)

	var u struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := githubGetJSON(ctx, client, p.userInfoURL, &u); err != nil {
		return ssoIdentity{}, err
	}
	if u.ID == 0 {
		return ssoIdentity{}, errors.New("github: empty user id")
	}

	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := githubGetJSON(ctx, client, p.emailsURL, &emails); err != nil {
		return ssoIdentity{}, err
	}
	email, verified := "", false
	for _, e := range emails {
		if e.Primary && e.Verified {
			email, verified = e.Email, true
			break
		}
	}
	if email == "" {
		for _, e := range emails {
			if e.Verified {
				email, verified = e.Email, true
				break
			}
		}
	}
	name := u.Name
	if name == "" {
		name = u.Login
	}
	return ssoIdentity{
		subject:     strconv.FormatInt(u.ID, 10),
		email:       email,
		displayName: name,
		linkTrusted: verified,
	}, nil
}

// githubGetJSON GETs url with the oauth2 client and decodes JSON. GitHub rejects
// requests without a User-Agent, so set one.
func githubGetJSON(ctx context.Context, client *http.Client, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "tela")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return fmt.Errorf("github %s: status %d: %s", url, resp.StatusCode, b)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// --- state cookie (HMAC-signed, browser-only) ---

func (s *Server) setSSOStateCookie(w http.ResponseWriter, st ssoState) {
	http.SetCookie(w, &http.Cookie{
		Name:     ssoStateCookie,
		Value:    s.signSSOState(st),
		Path:     "/api/auth/sso",
		HttpOnly: true,
		// Lax (not Strict) so the cookie survives the top-level GET redirect back
		// from the IdP — Strict would drop it on the cross-site return.
		SameSite: http.SameSiteLaxMode,
		Secure:   auth.CookieSecure(),
		MaxAge:   int(ssoStateTTL.Seconds()),
	})
}

func (s *Server) clearSSOStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: ssoStateCookie, Value: "", Path: "/api/auth/sso",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: auth.CookieSecure(), MaxAge: -1,
	})
}

func (s *Server) readSSOStateCookie(r *http.Request) (ssoState, bool) {
	c, err := r.Cookie(ssoStateCookie)
	if err != nil || c.Value == "" {
		return ssoState{}, false
	}
	return s.verifySSOState(c.Value)
}

// signSSOState returns base64(json).base64(hmac). The HMAC over the share secret
// makes the cookie tamper-evident, so the callback can trust the provider/org/
// nonce/next it carries.
func (s *Server) signSSOState(st ssoState) string {
	payload, _ := json.Marshal(st)
	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + base64.RawURLEncoding.EncodeToString(s.ssoStateMAC(body))
}

func (s *Server) verifySSOState(v string) (ssoState, bool) {
	body, sig, ok := strings.Cut(v, ".")
	if !ok {
		return ssoState{}, false
	}
	want, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(want, s.ssoStateMAC(body)) {
		return ssoState{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return ssoState{}, false
	}
	var st ssoState
	if err := json.Unmarshal(payload, &st); err != nil {
		return ssoState{}, false
	}
	return st, true
}

func (s *Server) ssoStateMAC(body string) []byte {
	mac := hmac.New(sha256.New, s.shareSecret)
	mac.Write([]byte("sso-state\x00" + body))
	return mac.Sum(nil)
}

// sanitizeNext keeps only a safe same-origin path (must start with a single
// '/'), defaulting to the app root. Blocks open redirects via absolute URLs or
// protocol-relative '//evil.com'.
func sanitizeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	if u, err := url.Parse(next); err != nil || u.Host != "" || u.Scheme != "" {
		return "/"
	}
	return next
}

// ssoFail bounces back to the login screen with a short, user-facing reason in
// the query string (the SPA surfaces it).
func ssoFail(w http.ResponseWriter, r *http.Request, reason string) {
	http.Redirect(w, r, "/login?sso_error="+url.QueryEscape(reason), http.StatusFound)
}
