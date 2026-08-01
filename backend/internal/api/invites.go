package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// Token-based email invitations, shared across the two things you can be
// invited to: an ORGANIZATION (org_invites.go) and a SPACE (space_invites.go).
// One link shape (/invite/{token}), one public lookup, one accept endpoint, one
// auto-apply-on-verify hook — the kind is a discriminator resolved from whichever
// table holds the token hash. The scope-specific create/list/revoke handlers live
// in their own files; everything below is the common core.
//
// The raw token lives only in the emailed link; we persist its SHA-256 hash
// (mirroring email_tokens). Invites are email-targeted, so a forwarded link
// can't enrol the wrong person.

const inviteTokenTTL = 14 * 24 * time.Hour

const (
	inviteKindOrg   = "org"
	inviteKindSpace = "space"
)

// inviteTarget is a resolved invite, whichever table it came from.
type inviteTarget struct {
	Kind    string // inviteKindOrg | inviteKindSpace
	ID      int64  // the invite row id
	ScopeID int64  // org id or space id
	Name    string // org or space name (safe to show the token holder)
	Email   string // the invited address
	Role    string // org_role / space role
	Inviter string // who sent it (display name, may be "")
}

// inviteExpiry is the wire/DB timestamp an invite minted now expires at.
func inviteExpiry() string {
	return time.Now().UTC().Add(inviteTokenTTL).Format("2006-01-02 15:04:05")
}

// mintInvite refreshes the outstanding invite for (scope, email) and inserts a
// fresh one, returning its id. Deleting first is cleaner than an ON CONFLICT
// against the partial unique index. table/scopeCol/roleCol are compile-time
// constants from the two callers — never request data.
func mintInvite(ctx context.Context, tx *sql.Tx, table, scopeCol, roleCol string,
	scopeID int64, email, role, hash string, invitedBy int64, expires string) (int64, error) {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM `+table+` WHERE `+scopeCol+` = $1 AND lower(email) = $2 AND accepted_at IS NULL`,
		scopeID, email); err != nil {
		return 0, err
	}
	var id int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO `+table+` (`+scopeCol+`, email, `+roleCol+`, token_hash, invited_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		scopeID, email, role, hash, invitedBy, expires).Scan(&id)
	return id, err
}

// lookupInvite resolves a token hash to its pending invite. Org invites are
// checked first (they predate space invites); token hashes are unique across
// both tables in practice — a 32-byte random can't collide.
func (s *Server) lookupInvite(ctx context.Context, hash string) (inviteTarget, error) {
	t := inviteTarget{Kind: inviteKindOrg}
	var inviter sql.NullString
	err := s.DB.QueryRowContext(ctx, `
		SELECT i.id, i.org_id, o.name, i.email, i.org_role,
		       COALESCE(NULLIF(iu.display_name, ''), iu.username)
		  FROM org_invites i
		  JOIN orgs o ON o.id = i.org_id
		  LEFT JOIN users iu ON iu.id = i.invited_by
		 WHERE i.token_hash = $1 AND i.accepted_at IS NULL AND i.expires_at > tela_now()`,
		hash).Scan(&t.ID, &t.ScopeID, &t.Name, &t.Email, &t.Role, &inviter)
	if err == nil {
		t.Inviter = inviter.String
		return t, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return inviteTarget{}, err
	}

	t = inviteTarget{Kind: inviteKindSpace}
	err = s.DB.QueryRowContext(ctx, `
		SELECT i.id, i.space_id, sp.name, i.email, i.role,
		       COALESCE(NULLIF(iu.display_name, ''), iu.username)
		  FROM space_invites i
		  JOIN spaces sp ON sp.id = i.space_id
		  LEFT JOIN users iu ON iu.id = i.invited_by
		 WHERE i.token_hash = $1 AND i.accepted_at IS NULL AND i.expires_at > tela_now()`,
		hash).Scan(&t.ID, &t.ScopeID, &t.Name, &t.Email, &t.Role, &inviter)
	if err != nil {
		return inviteTarget{}, err
	}
	t.Inviter = inviter.String
	return t, nil
}

// GetInvite returns an invite's target + email for the accept page. PUBLIC
// (under /api/invites/, bypasses session middleware) — it self-authenticates via
// the unguessable token, so a logged-out invitee can render the page before
// signing up. Never reveals anything the token holder doesn't already know: the
// org/space name and who invited them are exactly what the email said.
func (s *Server) GetInvite(w http.ResponseWriter, r *http.Request) {
	t, err := s.lookupInvite(r.Context(), hashEmailToken(r.PathValue("token")))
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{"valid": false})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "load invite failed")
		return
	}
	out := map[string]any{
		"valid":   true,
		"kind":    t.Kind,
		"email":   t.Email,
		"role":    t.Role,
		"inviter": t.Inviter,
	}
	if t.Kind == inviteKindSpace {
		out["space_name"] = t.Name
	} else {
		out["org_name"] = t.Name
	}
	writeJSON(w, http.StatusOK, out)
}

// AcceptInvite joins the logged-in user to whatever the token names — an org or
// a space. The caller's verified email must match the invite (email-targeted, so
// a forwarded link can't enrol the wrong person).
func (s *Server) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}
	ctx := r.Context()

	t, err := s.lookupInvite(ctx, hashEmailToken(req.Token))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusBadRequest, "invalid_token", "this invitation is invalid or has expired")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "load invite failed")
		return
	}
	if normalizeEmail(u.Email) != normalizeEmail(t.Email) {
		writeError(w, http.StatusForbidden, "email_mismatch", "this invitation is for "+t.Email+" — sign in with that address to accept it")
		return
	}

	if t.Kind == inviteKindSpace {
		if err := s.acceptSpaceInvite(ctx, u.ID, t); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "join space failed")
			return
		}
		writeAudit(ctx, s.DB, &u.ID, "space_member.invite_accept", "space", t.ScopeID, t.Email)
		space, err := selectSpaceByID(ctx, s.DB, t.ScopeID)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"space": space})
		return
	}

	if ae := s.checkSeatQuota(ctx, t.ScopeID); ae != nil {
		writeError(w, ae.Status, ae.Code, "this organization is at its seat limit — ask an admin to add a seat")
		return
	}
	if err := s.acceptOrgInvite(ctx, u.ID, t); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "join org failed")
		return
	}
	writeAudit(ctx, s.DB, &u.ID, "org_member.invite_accept", "org", t.ScopeID, t.Email)
	org, err := selectOrgByID(ctx, s.DB, t.ScopeID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"org": org})
}

// applyPendingInvites enrols userID into every org AND space with a pending
// invite for their just-verified email — the auto-join twin of applyAutoJoin,
// called from VerifyEmail so a fresh signup that arrived via an invite link
// lands where it was invited without a second click.
func (s *Server) applyPendingInvites(ctx context.Context, userID int64, email string) {
	email = normalizeEmail(email)
	if email == "" {
		return
	}
	s.applyPendingOrgInvites(ctx, userID, email)
	s.applyPendingSpaceInvites(ctx, userID, email)
}

// pendingInvitesFor loads the caller's pending invites of one kind for email.
func (s *Server) pendingInvitesFor(ctx context.Context, kind, email string) []inviteTarget {
	q := `SELECT id, org_id, org_role FROM org_invites
	       WHERE lower(email) = $1 AND accepted_at IS NULL AND expires_at > tela_now()`
	if kind == inviteKindSpace {
		q = `SELECT id, space_id, role FROM space_invites
		      WHERE lower(email) = $1 AND accepted_at IS NULL AND expires_at > tela_now()`
	}
	rows, err := s.DB.QueryContext(ctx, q, email)
	if err != nil {
		slog.Error("pending invites lookup", "kind", kind, "err", err)
		return nil
	}
	defer rows.Close()
	var out []inviteTarget
	for rows.Next() {
		t := inviteTarget{Kind: kind, Email: email}
		if err := rows.Scan(&t.ID, &t.ScopeID, &t.Role); err == nil {
			out = append(out, t)
		}
	}
	return out
}
