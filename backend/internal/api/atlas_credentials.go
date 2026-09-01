package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/zcag/tela/backend/internal/atlas/core"
	"github.com/zcag/tela/backend/internal/secretbox"
)

// ── request / response shapes ───────────────────────────────────────────────

type atlasCredCreateReq struct {
	OwnerKind string            `json:"owner_kind"`     // "user" | "org"
	OwnerID   int64             `json:"owner_id"`       // the owning user or org id
	Name      string            `json:"name"`           // unique within (owner_kind, owner_id)
	Kind      string            `json:"kind"`           // "git" | "jira"
	Value     string            `json:"value"`          // the token (write-only)
	Meta      map[string]string `json:"meta,omitempty"` // git: {username}; jira: {email, base_url}
}

// atlasCredDTO is the read shape: the token value is NEVER returned (write-only),
// only the non-secret name/kind/meta/owner so a project owner can pick a cred.
type atlasCredDTO struct {
	ID        int64             `json:"id"`
	OwnerKind string            `json:"owner_kind"`
	OwnerID   int64             `json:"owner_id"`
	Name      string            `json:"name"`
	Kind      string            `json:"kind"`
	Meta      map[string]string `json:"meta,omitempty"`
	CreatedAt string            `json:"created_at"`
}

var atlasCredKinds = map[string]bool{string(core.SourceGit): true, string(core.SourceJira): true}

// ── owner-scoped: credential CRUD ───────────────────────────────────────────

// ListAtlasCredentials lists the credentials the caller may use: their personal
// ones plus the orgs they administer. The token value is BLANKED everywhere.
// GET /api/atlas/credentials.
func (s *Server) ListAtlasCredentials(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT id, owner_kind, owner_id, name, kind, meta_json, created_at
		  FROM atlas_credentials
		 WHERE (owner_kind = 'user' AND owner_id = $1)
		    OR (owner_kind = 'org'  AND owner_id IN (
		          SELECT org_id FROM org_members WHERE user_id = $1 AND org_role = 'admin'))
		 ORDER BY id`, u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "list credentials failed")
		return
	}
	defer rows.Close()
	out := []atlasCredDTO{}
	for rows.Next() {
		var d atlasCredDTO
		var metaJSON string
		if err := rows.Scan(&d.ID, &d.OwnerKind, &d.OwnerID, &d.Name, &d.Kind, &metaJSON, &d.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "scan credential failed")
			return
		}
		d.Meta = parseCredMeta(metaJSON)
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, map[string]any{"credentials": out})
}

// CreateAtlasCredential stores an owner-scoped, reusable source credential. The
// value (token) is write-only — it lands here on create and is blanked on read.
// POST /api/atlas/credentials — owner-management-gated.
func (s *Server) CreateAtlasCredential(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	var req atlasCredCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}
	if ae := s.atlasOwnerManageErr(r.Context(), u, req.OwnerKind, req.OwnerID); ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "invalid_credential", "name is required")
		return
	}
	if !atlasCredKinds[req.Kind] {
		writeError(w, http.StatusBadRequest, "invalid_kind", "kind must be 'git' or 'jira'")
		return
	}
	if req.Value == "" {
		writeError(w, http.StatusBadRequest, "invalid_credential", "value (token) is required")
		return
	}
	// jira authenticates as Basic base64(email:token) — the email is non-secret and
	// must travel in meta so the connector can build the header.
	if req.Kind == string(core.SourceJira) && (req.Meta == nil || req.Meta["email"] == "") {
		writeError(w, http.StatusBadRequest, "invalid_credential", "jira credential requires meta.email")
		return
	}
	metaJSON := ""
	if len(req.Meta) > 0 {
		b, _ := json.Marshal(req.Meta)
		metaJSON = string(b)
	}
	// The token is encrypted at rest (internal/secretbox) — it has to be replayed
	// outbound to git/jira, so hashing is not an option. meta_json is deliberately
	// left in the clear: it holds only non-secret adornments (jira email/base URL,
	// git username), and keeping it readable keeps it greppable in a dump.
	sealed, err := secretbox.Seal(req.Value)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "encrypt credential failed")
		return
	}
	var id int64
	var createdAt string
	err = s.DB.QueryRowContext(r.Context(), `
		INSERT INTO atlas_credentials (owner_kind, owner_id, name, kind, value, meta_json)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING id, created_at`,
		req.OwnerKind, req.OwnerID, req.Name, req.Kind, sealed, metaJSON).Scan(&id, &createdAt)
	if err != nil {
		if isUniqueConstraintErr(err) {
			writeError(w, http.StatusConflict, "duplicate", "a credential with this name already exists for this owner")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "create credential failed")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"credential": atlasCredDTO{
		ID: id, OwnerKind: req.OwnerKind, OwnerID: req.OwnerID, Name: req.Name, Kind: req.Kind, Meta: req.Meta, CreatedAt: createdAt,
	}})
}

// DeleteAtlasCredential removes a credential (its FK on atlas_sources nulls —
// referencing sources fall back to public/no-auth). DELETE /api/atlas/credentials/{id}.
func (s *Server) DeleteAtlasCredential(w http.ResponseWriter, r *http.Request) {
	credID, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	kind, ownerID, err := s.atlasCredOwner(r.Context(), credID)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "credential not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "lookup credential failed")
		return
	}
	if ae := s.atlasOwnerManageErr(r.Context(), u, kind, ownerID); ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	if _, err := s.DB.ExecContext(r.Context(), `DELETE FROM atlas_credentials WHERE id=$1`, credID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "delete credential failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// atlasCredOwner resolves a credential's owner scope (kind + id).
func (s *Server) atlasCredOwner(ctx context.Context, credID int64) (kind string, ownerID int64, err error) {
	err = s.DB.QueryRowContext(ctx,
		`SELECT owner_kind, owner_id FROM atlas_credentials WHERE id=$1`, credID).Scan(&kind, &ownerID)
	return kind, ownerID, err
}

// ── resolution (executor side) ──────────────────────────────────────────────

// loadAtlasCredential reads a credential's token + parsed meta by id. The value
// is the access token; meta carries the non-secret adornments (jira email/base,
// git username).
func loadAtlasCredential(ctx context.Context, db *sql.DB, id int64) (value string, meta map[string]string, err error) {
	var stored, metaJSON string
	err = db.QueryRowContext(ctx, `SELECT value, meta_json FROM atlas_credentials WHERE id=$1`, id).Scan(&stored, &metaJSON)
	if err != nil {
		return "", nil, err
	}
	// Unwraps a sealed value; a row written before encryption landed has no version
	// prefix and comes back verbatim, so old installs keep running until the row is
	// next saved. A decrypt failure fails the run rather than handing the connector
	// ciphertext to authenticate with.
	value, err = secretbox.Open(stored)
	if err != nil {
		return "", nil, err
	}
	return value, parseCredMeta(metaJSON), nil
}

// applyAtlasCred stuffs a resolved credential onto a run's transient core.Source
// so the connector authenticates without the Connector interface ever carrying a
// credential. jira reads SecretValue (token) + SecretMeta["email"] + Location
// (base) directly. git authenticates via the clone URL, not SecretValue, so the
// token is injected into Location here (a no-op for an empty value or a non-http
// location, e.g. a local path).
func applyAtlasCred(src *core.Source, value string, meta map[string]string) {
	src.SecretValue = value
	src.SecretMeta = meta
	// Git auth is injected at git-command time (the git connector's authURL),
	// NEVER onto Location — that keeps the token out of the overview page, the
	// server logs, and the run events. The connector reads SecretValue +
	// SecretMeta["username"]; jira reads SecretValue + SecretMeta["email"].
}

func parseCredMeta(metaJSON string) map[string]string {
	if metaJSON == "" {
		return nil
	}
	var m map[string]string
	if json.Unmarshal([]byte(metaJSON), &m) != nil {
		return nil
	}
	return m
}
