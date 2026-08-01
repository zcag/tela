package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/zcag/tela/backend/internal/auth"
	"github.com/zcag/tela/backend/internal/mailer"
)

// Sharing a space by email — including with people who don't have a tela account
// yet. One owner action, two outcomes:
//
//   - the address already belongs to a tela user → they get the space_members row
//     immediately (identical to the username-based AddSpaceMember) plus the
//     standard space_added notification (in-app + email). Nothing to accept.
//   - nobody owns that address yet → a pending, email-targeted space_invites row
//     and a branded invitation email. The invitee lands on /invite/{token}; when
//     they sign up and verify that address they get the space automatically
//     (applyPendingInvites), and a logged-in matching user can accept directly.
//
// The token mint, the public /api/invites/{token} lookup and the accept flow are
// shared with org invites — see invites.go.

type spaceInviteDTO struct {
	ID        int64  `json:"id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
}

// spaceShareResult is the outcome of sharing a space with an address: exactly
// one of Member (granted immediately) / Invite (invitation emailed) is set.
type spaceShareResult struct {
	Member *spaceMemberDTO
	Invite *spaceInviteDTO
}

// shareSpaceByEmailCore is the transport-agnostic core behind "share this space
// with this email" — used by the REST handler and the MCP invite_to_space tool
// so the gate, both branches, the mail and the audit trail are identical.
// origin is the browser-facing origin the invite link should point at.
func (s *Server) shareSpaceByEmailCore(ctx context.Context, u *auth.User, k *auth.APIKey,
	spaceID int64, rawEmail, rawRole, origin string) (spaceShareResult, *apiErr) {
	if ae := s.requireSpaceOwnerCore(ctx, u, k, spaceID); ae != nil {
		return spaceShareResult{}, ae
	}
	email := normalizeEmail(rawEmail)
	if !validEmail(email) {
		return spaceShareResult{}, &apiErr{http.StatusBadRequest, "bad_request", "a valid email is required"}
	}
	role := rawRole
	if role == "" {
		role = roleViewer
	}
	if !isValidRole(role) {
		return spaceShareResult{}, &apiErr{http.StatusBadRequest, "bad_request", "role must be one of owner, editor, viewer"}
	}

	// Branch 1 — an active account already owns this (verified) address: grant
	// access now. An unverified account falls through to the invite branch; the
	// invite is applied the moment they verify.
	var targetID int64
	err := s.DB.QueryRowContext(ctx, `
		SELECT id FROM users
		 WHERE lower(email) = $1 AND is_active = 1 AND email_verified_at IS NOT NULL`,
		email).Scan(&targetID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return spaceShareResult{}, &apiErr{http.StatusInternalServerError, "internal", "lookup user failed"}
	}
	if err == nil {
		if _, err := s.DB.ExecContext(ctx,
			`INSERT INTO space_members (space_id, user_id, role) VALUES ($1, $2, $3)`,
			spaceID, targetID, role); err != nil {
			if isUniqueConstraintErr(err) {
				return spaceShareResult{}, &apiErr{http.StatusConflict, "already_member", "that person already has access to this space"}
			}
			return spaceShareResult{}, &apiErr{http.StatusInternalServerError, "internal", "add member failed"}
		}
		// In-app + email ("X added you to <space>") through the standard pipeline.
		s.notifySpaceAdded(ctx, u, targetID, spaceID)
		writeAudit(ctx, s.DB, &u.ID, "space_invite.grant", "space", spaceID, email)
		dto, err := selectSpaceMember(ctx, s.DB, spaceID, targetID)
		if err != nil {
			return spaceShareResult{}, &apiErr{http.StatusInternalServerError, "internal", "fetch added member failed"}
		}
		return spaceShareResult{Member: &dto}, nil
	}

	// Branch 2 — nobody owns the address yet: park a token-based invitation.
	var spaceName string
	if err := s.DB.QueryRowContext(ctx, `SELECT name FROM spaces WHERE id = $1`, spaceID).Scan(&spaceName); err != nil {
		return spaceShareResult{}, &apiErr{http.StatusNotFound, "not_found", "space not found"}
	}
	raw, hash, err := newEmailToken()
	if err != nil {
		return spaceShareResult{}, &apiErr{http.StatusInternalServerError, "internal", "token generation failed"}
	}
	expires := inviteExpiry()

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return spaceShareResult{}, &apiErr{http.StatusInternalServerError, "internal", "begin tx failed"}
	}
	defer tx.Rollback()
	inviteID, err := mintInvite(ctx, tx, "space_invites", "space_id", "role", spaceID, email, role, hash, u.ID, expires)
	if err != nil {
		return spaceShareResult{}, &apiErr{http.StatusInternalServerError, "internal", "create invite failed"}
	}
	if err := tx.Commit(); err != nil {
		return spaceShareResult{}, &apiErr{http.StatusInternalServerError, "internal", "commit failed"}
	}

	// Best-effort send — the invite exists regardless; with mail logging-only the
	// link is in the logs, same as verify/reset.
	inviteURL := origin + "/invite/" + raw
	if err := s.Mailer.Send(ctx, mailer.SpaceInvite(email, spaceName, u.Username, inviteURL, s.emailBrandForSpace(ctx, &spaceID))); err != nil {
		writeAudit(ctx, s.DB, &u.ID, "space_invite.mail_failed", "space", spaceID, email)
	}
	writeAudit(ctx, s.DB, &u.ID, "space_invite.create", "space", spaceID, email)
	return spaceShareResult{Invite: &spaceInviteDTO{
		ID: inviteID, Email: email, Role: role, ExpiresAt: expires,
	}}, nil
}

// CreateSpaceInvite shares a space with an email address. Owner only (the same
// gate as AddSpaceMember). Responds 201 with either {"member": …} (the address
// already had an account — access granted now) or {"invite": …} (invitation
// emailed). Re-inviting a pending address refreshes the invite.
func (s *Server) CreateSpaceInvite(w http.ResponseWriter, r *http.Request) {
	spaceID, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	var req struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}
	ctx := r.Context()
	k, _ := auth.APIKeyFromContext(ctx)
	res, ae := s.shareSpaceByEmailCore(ctx, u, k, spaceID, req.Email, req.Role, s.linkOrigin(r))
	if ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	if res.Member != nil {
		writeJSON(w, http.StatusCreated, map[string]any{"member": *res.Member})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"invite": *res.Invite})
}

// ListSpaceInvites returns the space's pending (unaccepted, unexpired) invites.
// Owner only — the pending list exposes addresses that aren't members yet.
func (s *Server) ListSpaceInvites(w http.ResponseWriter, r *http.Request) {
	spaceID, ok := s.requireSpaceOwner(w, r)
	if !ok {
		return
	}
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT id, email, role, created_at, expires_at
		  FROM space_invites
		 WHERE space_id = $1 AND accepted_at IS NULL AND expires_at > tela_now()
		 ORDER BY created_at DESC`, spaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "list invites failed")
		return
	}
	defer rows.Close()
	out := []spaceInviteDTO{}
	for rows.Next() {
		var d spaceInviteDTO
		if err := rows.Scan(&d.ID, &d.Email, &d.Role, &d.CreatedAt, &d.ExpiresAt); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "scan invite failed")
			return
		}
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, map[string]any{"invites": out})
}

// RevokeSpaceInvite deletes a pending invite. Owner only.
func (s *Server) RevokeSpaceInvite(w http.ResponseWriter, r *http.Request) {
	spaceID, ok := s.requireSpaceOwner(w, r)
	if !ok {
		return
	}
	inviteID, err := strconv.ParseInt(r.PathValue("inviteId"), 10, 64)
	if err != nil || inviteID <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid invite id")
		return
	}
	res, err := s.DB.ExecContext(r.Context(),
		`DELETE FROM space_invites WHERE id = $1 AND space_id = $2 AND accepted_at IS NULL`, inviteID, spaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "revoke invite failed")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "no pending invite with that id")
		return
	}
	s.audit(r.Context(), r, "space_invite.revoke", "space", spaceID, "")
	w.WriteHeader(http.StatusNoContent)
}

// acceptSpaceInvite adds userID to the invite's space and marks it consumed, in
// one tx. ON CONFLICT keeps it idempotent when access arrived some other way
// (an org grant, a manual add) between the invite and the click.
func (s *Server) acceptSpaceInvite(ctx context.Context, userID int64, t inviteTarget) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO space_members (space_id, user_id, role) VALUES ($1, $2, $3)
		 ON CONFLICT (space_id, user_id) DO NOTHING`, t.ScopeID, userID, t.Role); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE space_invites SET accepted_at = tela_now() WHERE id = $1`, t.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// applyPendingSpaceInvites grants userID every space with a pending invite for
// their just-verified email — so someone invited before they had an account
// finds the space waiting when they finish signing up.
func (s *Server) applyPendingSpaceInvites(ctx context.Context, userID int64, email string) {
	for _, t := range s.pendingInvitesFor(ctx, inviteKindSpace, email) {
		if err := s.acceptSpaceInvite(ctx, userID, t); err != nil {
			slog.Error("pending space invite join", "user_id", userID, "space_id", t.ScopeID, "err", err)
			continue
		}
		writeAudit(ctx, s.DB, &userID, "space_member.invite_accept", "space", t.ScopeID, email)
	}
}

// requireSpaceOwnerCore is the space-owner gate every invite entry point shares
// (the same gate AddSpaceMember applies), transport-agnostic: it goes through
// membershipCore, so a bearer key's space-scope ceiling is enforced first.
func (s *Server) requireSpaceOwnerCore(ctx context.Context, u *auth.User, k *auth.APIKey, spaceID int64) *apiErr {
	role, ae := s.membershipCore(ctx, u, k, spaceID)
	if ae != nil {
		return ae
	}
	if role != roleOwner {
		return &apiErr{http.StatusForbidden, "forbidden", "owner role required"}
	}
	return nil
}

// requireSpaceOwner is requireSpaceOwnerCore for the HTTP handlers: resolves
// {id} and writes the error response itself.
func (s *Server) requireSpaceOwner(w http.ResponseWriter, r *http.Request) (int64, bool) {
	spaceID, ok := parseIDParam(w, r, "id")
	if !ok {
		return 0, false
	}
	u, ok := requireUser(w, r)
	if !ok {
		return 0, false
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	if ae := s.requireSpaceOwnerCore(r.Context(), u, k, spaceID); ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return 0, false
	}
	return spaceID, true
}
