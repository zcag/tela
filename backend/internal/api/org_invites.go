package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/zcag/tela/backend/internal/mailer"
)

// Email invitations to an organization — the self-serve onboarding path. An org
// admin invites a teammate by email; the invitee joins by accepting while logged
// in with the matching verified email, or auto-joins when they verify a fresh
// signup (applyPendingInvites, beside applyAutoJoin). The token mint/lookup,
// public accept page endpoint and accept flow are shared with space invites —
// see invites.go.

type orgInviteDTO struct {
	ID        int64  `json:"id"`
	Email     string `json:"email"`
	OrgRole   string `json:"org_role"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
}

// CreateOrgInvite invites an email to the org (org admin). Re-inviting refreshes
// the pending invite. The seat limit is enforced at accept time, not here, so an
// admin can queue invites before upgrading.
func (s *Server) CreateOrgInvite(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	orgID, ok := parseOrgID(w, r)
	if !ok {
		return
	}
	if !s.requireOrgAdmin(w, r, orgID) {
		return
	}
	var req struct {
		Email   string `json:"email"`
		OrgRole string `json:"org_role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}
	email := normalizeEmail(req.Email)
	if !validEmail(email) {
		writeError(w, http.StatusBadRequest, "bad_request", "a valid email is required")
		return
	}
	role := req.OrgRole
	if role == "" {
		role = orgRoleMember
	}
	if role != orgRoleAdmin && role != orgRoleMember {
		writeError(w, http.StatusBadRequest, "bad_request", "org_role must be 'admin' or 'member'")
		return
	}
	ctx := r.Context()

	// Already a member? Then there's nothing to invite.
	var dummy int64
	memberErr := s.DB.QueryRowContext(ctx, `
		SELECT u.id FROM users u
		  JOIN org_members m ON m.user_id = u.id
		 WHERE m.org_id = $1 AND lower(u.email) = $2`, orgID, email).Scan(&dummy)
	if memberErr == nil {
		writeError(w, http.StatusConflict, "already_member", "that person is already in this organization")
		return
	}
	if !errors.Is(memberErr, sql.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "internal", "membership check failed")
		return
	}

	var orgName string
	if err := s.DB.QueryRowContext(ctx, `SELECT name FROM orgs WHERE id = $1`, orgID).Scan(&orgName); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "org not found")
		return
	}

	raw, hash, err := newEmailToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "token generation failed")
		return
	}
	expires := inviteExpiry()

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "begin tx failed")
		return
	}
	defer tx.Rollback()
	inviteID, err := mintInvite(ctx, tx, "org_invites", "org_id", "org_role", orgID, email, role, hash, u.ID, expires)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "create invite failed")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "commit failed")
		return
	}

	// Send the branded invitation (best-effort — the invite exists regardless; if
	// mail is logging-only the link is in the logs, same as verify/reset).
	inviteURL := s.linkOrigin(r) + "/invite/" + raw
	inviter := u.Username
	if err := s.Mailer.Send(ctx, mailer.OrgInvite(email, orgName, inviter, inviteURL, s.emailBrandForRequest(r))); err != nil {
		// Don't fail the request — surface it in logs; the admin can re-send.
		writeAudit(ctx, s.DB, &u.ID, "org_invite.mail_failed", "org", orgID, email)
	}
	s.audit(ctx, r, "org_invite.create", "org", orgID, email)
	writeJSON(w, http.StatusCreated, map[string]any{"invite": orgInviteDTO{
		ID: inviteID, Email: email, OrgRole: role, ExpiresAt: expires,
	}})
}

// ListOrgInvites returns the org's pending (unaccepted, unexpired) invites. Org admin.
func (s *Server) ListOrgInvites(w http.ResponseWriter, r *http.Request) {
	orgID, ok := parseOrgID(w, r)
	if !ok {
		return
	}
	if !s.requireOrgAdmin(w, r, orgID) {
		return
	}
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT id, email, org_role, created_at, expires_at
		  FROM org_invites
		 WHERE org_id = $1 AND accepted_at IS NULL AND expires_at > tela_now()
		 ORDER BY created_at DESC`, orgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "list invites failed")
		return
	}
	defer rows.Close()
	out := []orgInviteDTO{}
	for rows.Next() {
		var d orgInviteDTO
		if err := rows.Scan(&d.ID, &d.Email, &d.OrgRole, &d.CreatedAt, &d.ExpiresAt); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "scan invite failed")
			return
		}
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, map[string]any{"invites": out})
}

// RevokeOrgInvite deletes a pending invite. Org admin.
func (s *Server) RevokeOrgInvite(w http.ResponseWriter, r *http.Request) {
	orgID, ok := parseOrgID(w, r)
	if !ok {
		return
	}
	if !s.requireOrgAdmin(w, r, orgID) {
		return
	}
	inviteID, err := strconv.ParseInt(r.PathValue("inviteId"), 10, 64)
	if err != nil || inviteID <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid invite id")
		return
	}
	res, err := s.DB.ExecContext(r.Context(),
		`DELETE FROM org_invites WHERE id = $1 AND org_id = $2 AND accepted_at IS NULL`, inviteID, orgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "revoke invite failed")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "no pending invite with that id")
		return
	}
	s.audit(r.Context(), r, "org_invite.revoke", "org", orgID, "")
	w.WriteHeader(http.StatusNoContent)
}

// acceptOrgInvite joins userID to the invite's org and marks it consumed, in one
// tx. Seat quota is checked by the caller.
func (s *Server) acceptOrgInvite(ctx context.Context, userID int64, t inviteTarget) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO org_members (org_id, user_id, org_role) VALUES ($1, $2, $3)
		 ON CONFLICT (org_id, user_id) DO NOTHING`, t.ScopeID, userID, t.Role); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE org_invites SET accepted_at = tela_now() WHERE id = $1`, t.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// applyPendingOrgInvites enrols userID into every org with a pending invite for
// their just-verified email. Seat-blocked invites are left pending (the invitee
// can still accept later once the org frees a seat).
func (s *Server) applyPendingOrgInvites(ctx context.Context, userID int64, email string) {
	for _, t := range s.pendingInvitesFor(ctx, inviteKindOrg, email) {
		if ae := s.checkSeatQuota(ctx, t.ScopeID); ae != nil {
			continue // org full — leave the invite pending for a later manual accept
		}
		if err := s.acceptOrgInvite(ctx, userID, t); err != nil {
			slog.Error("pending invite join", "user_id", userID, "org_id", t.ScopeID, "err", err)
			continue
		}
		writeAudit(ctx, s.DB, &userID, "org_member.invite_accept", "org", t.ScopeID, email)
	}
}
