package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/zcag/tela/backend/internal/secretbox"
)

// Per-org OIDC SSO connection: stored config + the read paths the login flow
// uses. Configuration is instance-admin only (mirrors org_email_domains), the
// same posture as the rest of org administration.

// orgSSOConn is the stored connection. ClientSecret is never serialized to the
// read API — see orgSSODTO.
type orgSSOConn struct {
	OrgID        int64
	Issuer       string
	ClientID     string
	ClientSecret string
	Enforced     bool
}

type orgSSODTO struct {
	Configured bool   `json:"configured"`
	Issuer     string `json:"issuer"`
	ClientID   string `json:"client_id"`
	Enforced   bool   `json:"enforced"`
}

type orgSSOPutRequest struct {
	Issuer       string `json:"issuer"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Enforced     bool   `json:"enforced"`
}

// GetOrgSSO returns an org's SSO connection minus the secret. Instance-admin.
func (s *Server) GetOrgSSO(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireInstanceAdmin(w, r); !ok {
		return
	}
	orgID, ok := parseOrgID(w, r)
	if !ok {
		return
	}
	conn, found, err := s.orgSSOByID(r.Context(), orgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "load sso failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sso": orgSSODTO{
		Configured: found,
		Issuer:     conn.Issuer,
		ClientID:   conn.ClientID,
		Enforced:   conn.Enforced,
	}})
}

// PutOrgSSO upserts an org's OIDC connection. Instance-admin. The issuer is
// probed with OIDC discovery before saving, so a typo'd issuer is rejected up
// front rather than only failing at first login.
func (s *Server) PutOrgSSO(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireInstanceAdmin(w, r); !ok {
		return
	}
	orgID, ok := parseOrgID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if exists, err := orgExists(ctx, s.DB, orgID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "lookup org failed")
		return
	} else if !exists {
		writeError(w, http.StatusNotFound, "not_found", "org not found")
		return
	}
	// SSO is an Enterprise feature — don't let it be configured for an org whose
	// plan (or license) doesn't grant it. The usage-time gate is in SSOStart; this
	// is the config-time gate so the admin gets a clear reason up front.
	if !s.entitled(ctx, account{Kind: accountOrg, ID: orgID}, "sso") {
		writeError(w, http.StatusPaymentRequired, "upgrade_required", "single sign-on is an Enterprise feature — move this org to the Enterprise plan to configure it")
		return
	}

	var req orgSSOPutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}
	req.Issuer = strings.TrimRight(strings.TrimSpace(req.Issuer), "/")
	req.ClientID = strings.TrimSpace(req.ClientID)
	req.ClientSecret = strings.TrimSpace(req.ClientSecret)
	if req.Issuer == "" || req.ClientID == "" || req.ClientSecret == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "issuer, client_id and client_secret are required")
		return
	}
	if !strings.HasPrefix(req.Issuer, "https://") {
		writeError(w, http.StatusBadRequest, "bad_request", "issuer must be an https URL")
		return
	}
	if _, err := oidc.NewProvider(ctx, req.Issuer); err != nil {
		writeError(w, http.StatusBadRequest, "issuer_unreachable", "could not run OIDC discovery against that issuer")
		return
	}

	enforced := 0
	if req.Enforced {
		enforced = 1
	}
	// Encrypted at rest (internal/secretbox) — the client secret is replayed to the
	// IdP on every code exchange, so it can't be hashed. Issuer/client_id stay clear.
	sealed, err := secretbox.Seal(req.ClientSecret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "encrypt client secret failed")
		return
	}
	if _, err := s.DB.ExecContext(ctx, `
		INSERT INTO org_sso (org_id, issuer, client_id, client_secret, enforced)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (org_id) DO UPDATE SET
			issuer = EXCLUDED.issuer, client_id = EXCLUDED.client_id,
			client_secret = EXCLUDED.client_secret, enforced = EXCLUDED.enforced,
			updated_at = tela_now()`,
		orgID, req.Issuer, req.ClientID, sealed, enforced); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "save sso failed")
		return
	}
	s.audit(ctx, r, "org_sso.set", "org", orgID, req.Issuer)
	writeJSON(w, http.StatusOK, map[string]any{"sso": orgSSODTO{
		Configured: true, Issuer: req.Issuer, ClientID: req.ClientID, Enforced: req.Enforced,
	}})
}

// DeleteOrgSSO removes an org's SSO connection. Instance-admin. Existing linked
// identities survive (they just can't be used to start a new SSO login).
func (s *Server) DeleteOrgSSO(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireInstanceAdmin(w, r); !ok {
		return
	}
	orgID, ok := parseOrgID(w, r)
	if !ok {
		return
	}
	res, err := s.DB.ExecContext(r.Context(), `DELETE FROM org_sso WHERE org_id = $1`, orgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "delete sso failed")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "no sso configured for that org")
		return
	}
	s.audit(r.Context(), r, "org_sso.delete", "org", orgID, "")
	w.WriteHeader(http.StatusNoContent)
}

// --- read paths used by the login flow ---

const orgSSOSelect = `SELECT org_id, issuer, client_id, client_secret, enforced FROM org_sso`

// scanOrgSSO is the ONLY place a stored client secret is unwrapped — every read
// path funnels through it, so encryption can't be bypassed by adding a query.
func scanOrgSSO(row *sql.Row) (orgSSOConn, bool, error) {
	var c orgSSOConn
	var enforced int
	err := row.Scan(&c.OrgID, &c.Issuer, &c.ClientID, &c.ClientSecret, &enforced)
	if errors.Is(err, sql.ErrNoRows) {
		return orgSSOConn{}, false, nil
	}
	if err != nil {
		return orgSSOConn{}, false, err
	}
	// A row written before encryption landed carries no version prefix and comes
	// back verbatim; a decrypt failure fails the login rather than trying to
	// authenticate to the IdP with ciphertext.
	if c.ClientSecret, err = secretbox.Open(c.ClientSecret); err != nil {
		return orgSSOConn{}, false, err
	}
	c.Enforced = enforced == 1
	return c, true, nil
}

func (s *Server) orgSSOByID(ctx context.Context, orgID int64) (orgSSOConn, bool, error) {
	return scanOrgSSO(s.DB.QueryRowContext(ctx, orgSSOSelect+` WHERE org_id = $1`, orgID))
}

// orgSSOByDomain resolves the SSO connection for an email domain via the org's
// auto-join domain mapping (org_email_domains → org_sso).
func (s *Server) orgSSOByDomain(ctx context.Context, domain string) (orgSSOConn, bool, error) {
	return scanOrgSSO(s.DB.QueryRowContext(ctx, `
		SELECT so.org_id, so.issuer, so.client_id, so.client_secret, so.enforced
		  FROM org_email_domains d
		  JOIN org_sso so ON so.org_id = d.org_id
		 WHERE d.domain = $1`, domain))
}

// orgOwnsEmailDomain reports whether email's domain is an auto-join domain of
// orgID — the guard that lets an org IdP auto-link only its own users.
func (s *Server) orgOwnsEmailDomain(ctx context.Context, orgID int64, email string) bool {
	domain := emailDomain(email)
	if domain == "" {
		return false
	}
	var mapped int64
	err := s.DB.QueryRowContext(ctx,
		`SELECT org_id FROM org_email_domains WHERE domain = $1`, domain).Scan(&mapped)
	return err == nil && mapped == orgID
}

// passwordLoginBlocked reports whether email's domain belongs to an org that
// enforces SSO — in which case password login is refused (the user must use the
// SSO button). Best-effort: a DB hiccup never blocks login.
func (s *Server) passwordLoginBlocked(ctx context.Context, email string) bool {
	domain := emailDomain(email)
	if domain == "" {
		return false
	}
	var enforced int
	var orgID int64
	err := s.DB.QueryRowContext(ctx, `
		SELECT so.org_id, so.enforced
		  FROM org_email_domains d
		  JOIN org_sso so ON so.org_id = d.org_id
		 WHERE d.domain = $1`, domain).Scan(&orgID, &enforced)
	if err != nil || enforced != 1 {
		return false
	}
	// Don't strand users: only enforce SSO-only login while the org is actually
	// entitled to SSO. If entitlement lapsed (downgrade / lapsed license), SSO
	// login is gated off (SSOStart) — so password login must keep working, else
	// the org's users can't sign in at all.
	return s.entitled(ctx, account{Kind: accountOrg, ID: orgID}, "sso")
}

// parseOrgID reads the {id} path value as an org id, writing a 400 on a bad value.
func parseOrgID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid org id")
		return 0, false
	}
	return id, true
}
