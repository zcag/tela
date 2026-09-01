package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zcag/tela/backend/internal/auth"
)

const (
	minPasswordLen = 8
	maxUsernameLen = 64
)

// adminUserDTO is the wire shape for admin user listings + writes. Mirrors
// the users row except password_hash (never exposed).
type adminUserDTO struct {
	ID              int64   `json:"id"`
	Username        string  `json:"username"`
	DisplayName     string  `json:"display_name"`
	Email           *string `json:"email"`
	EmailVerified   bool    `json:"email_verified"`
	IsInstanceAdmin bool    `json:"is_instance_admin"`
	IsActive        bool    `json:"is_active"`
	PlanKey         string  `json:"plan_key"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
	// Populated only by the list endpoint (omitted on create/patch responses).
	LastActiveAt *string         `json:"last_active_at,omitempty"`
	Usage        *adminUserUsage `json:"usage,omitempty"`
	Orgs         int             `json:"orgs,omitempty"`             // org memberships
	HasAPIKey    bool            `json:"has_api_key,omitempty"`      // ≥1 non-revoked PAT
	UsedMCP      bool            `json:"used_mcp,omitempty"`         // connected MCP (PAT-via-/api/mcp OR any MCP request, incl. OAuth)
	McpLastSeen  *string         `json:"mcp_last_seen_at,omitempty"` // last authenticated MCP request, any credential
	// Activity inside the requested ?window= (admin_user_metrics.go). Always set
	// on list rows, including for accounts that did nothing (all-zero).
	Metrics *adminUserMetrics `json:"metrics,omitempty"`
	// Lifecycle label (admin_user_segments.go): power|regular|dabbler|churned|never.
	// Always computed from the last 30 days, never from the selected window.
	Segment string `json:"segment,omitempty"`
}

// adminUserUsage is the per-user resource snapshot the admin list shows: current
// usage beside the account's plan limits. Built from buildUsage — the same path
// limits.go enforces against — so these figures never drift from the real quota.
// A nil max means unlimited.
type adminUserUsage struct {
	Spaces          int64  `json:"spaces"`
	StorageBytes    int64  `json:"storage_bytes"`
	MaxSpaces       *int64 `json:"max_spaces"`
	MaxStorageBytes *int64 `json:"max_storage_bytes"`
}

type adminUserCreateRequest struct {
	Username        string `json:"username"`
	Email           string `json:"email"`
	Password        string `json:"password"`
	IsInstanceAdmin *bool  `json:"is_instance_admin"`
}

type adminUserPatchRequest struct {
	IsActive        *bool   `json:"is_active"`
	IsInstanceAdmin *bool   `json:"is_instance_admin"`
	Password        *string `json:"password"`
}

// ListAdminUsers returns every user row, including inactive ones, newest account
// first (most recent signup on top), each enriched with its activity inside
// ?window=1m|3m|all. Instance-admin only.
//
// The whole population ships in one payload and the table sorts client-side:
// this list is admin-only and bounded, so paging + server-side sort keys would
// buy nothing but two more things to keep in sync.
func (s *Server) ListAdminUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireInstanceAdmin(w, r); !ok {
		return
	}
	now := time.Now().UTC()
	win := parseAdminUserWindow(r.URL.Query().Get("window"), now)
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT id, username, display_name, email, email_verified_at, is_instance_admin, is_active, plan_key, created_at, updated_at, mcp_last_seen_at
		  FROM users
		 ORDER BY created_at DESC, id DESC`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "list users failed")
		return
	}
	defer rows.Close()

	users := []adminUserDTO{}
	for rows.Next() {
		u, err := scanAdminUserRow(rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "scan user row failed")
			return
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "iterate users failed")
		return
	}

	// Enrich each row with last-active + a usage-vs-limit snapshot. last-active is
	// the most recent session touch (sessions.last_seen_at, stamped per request);
	// usage reuses buildUsage so it matches what limits.go enforces. One batched
	// query for last-seen, then per-user usage — this list is admin-only and small,
	// so the N+1 is acceptable in exchange for not duplicating the quota counters.
	ctx := r.Context()
	lastSeen := map[int64]string{}
	if lsRows, err := s.DB.QueryContext(ctx,
		`SELECT user_id, MAX(last_seen_at) FROM sessions GROUP BY user_id`); err == nil {
		defer lsRows.Close()
		for lsRows.Next() {
			var uid int64
			var seen sql.NullString
			if err := lsRows.Scan(&uid, &seen); err == nil && seen.Valid {
				lastSeen[uid] = seen.String
			}
		}
	}

	// Org-membership counts.
	orgCount := scanInt64Map(ctx, s.DB, `SELECT user_id, COUNT(*) FROM org_members GROUP BY user_id`)
	// Windowed activity: edits, pages created, views, asks, sign-ins, days active,
	// AI calls — one batched aggregate each.
	metrics := loadAdminUserMetrics(ctx, s.DB, win)
	// MCP signal per user: has a live PAT, and/or has actually hit /api/mcp with one.
	hasKey, usedMCP := map[int64]bool{}, map[int64]bool{}
	if kr, err := s.DB.QueryContext(ctx, `
		SELECT k.user_id,
		       bool_or(k.revoked_at IS NULL),
		       bool_or(EXISTS (SELECT 1 FROM api_key_audit a
		                        WHERE a.api_key_id = k.id AND a.path LIKE '/api/mcp%'))
		  FROM api_keys k GROUP BY k.user_id`); err == nil {
		defer kr.Close()
		for kr.Next() {
			var uid int64
			var hk, um bool
			if err := kr.Scan(&uid, &hk, &um); err == nil {
				hasKey[uid], usedMCP[uid] = hk, um
			}
		}
	}

	for i := range users {
		id := users[i].ID
		if ls, ok := lastSeen[id]; ok {
			users[i].LastActiveAt = &ls
		}
		users[i].Orgs = int(orgCount[id])
		if m, ok := metrics[id]; ok {
			users[i].Metrics = m
		} else {
			users[i].Metrics = &adminUserMetrics{Weeks: make([]int64, adminUserWeeks)}
		}
		users[i].Segment = classifySegment(users[i].Metrics, users[i].LastActiveAt, now)
		users[i].HasAPIKey = hasKey[id]
		users[i].UsedMCP = users[i].UsedMCP || usedMCP[id] // scan set it from mcp_last_seen_at (covers OAuth)
		u, err := s.buildUsage(ctx, account{Kind: accountUser, ID: id})
		if err != nil {
			continue // usage stays nil for this row; the rest of the list still renders
		}
		users[i].Usage = &adminUserUsage{
			Spaces:          u.Usage.Spaces,
			StorageBytes:    u.Usage.StorageBytes,
			MaxSpaces:       u.Plan.MaxSpaces,
			MaxStorageBytes: u.Plan.MaxStorageBytes,
		}
	}
	weeks, _ := weekAxis(now)
	writeJSON(w, http.StatusOK, map[string]any{
		"users":  users,
		"window": win.Key,
		// Week-start dates (Monday, oldest→newest) that every row's `weeks`
		// series is aligned to — the x-axis for the sparklines and the cohort grid.
		"weeks": weeks,
		// The retention horizon behind the events-derived columns (views,
		// sign-ins, days active) — they cannot see further back than this.
		"events_since": eventsHorizon(ctx, s.DB),
	})
}

// CreateAdminUser inserts a new user. 409 on duplicate username, 400 on
// validation failure, 201 with the new row otherwise. Instance-admin only.
func (s *Server) CreateAdminUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireInstanceAdmin(w, r); !ok {
		return
	}

	var req adminUserCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}
	username := strings.TrimSpace(req.Username)
	if username == "" || len(username) > maxUsernameLen {
		writeError(w, http.StatusBadRequest, "bad_request", "username must be 1-64 characters")
		return
	}
	if len(req.Password) < minPasswordLen {
		writeError(w, http.StatusBadRequest, "bad_request", "password must be at least 8 characters")
		return
	}
	// Email is optional for admin-created users. When present it must be valid;
	// admin-created accounts are treated as pre-confirmed (no verify email).
	email := normalizeEmail(req.Email)
	if email != "" && !validEmail(email) {
		writeError(w, http.StatusBadRequest, "bad_request", "email must be a valid address")
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "hash password failed")
		return
	}

	ctx := r.Context()
	// No trial: this account was provisioned by an operator, not created by the
	// person using it — and on a self-hosted instance a trial banner on every
	// admin-made account would be pure noise. See users_create.go.
	id, err := insertUser(ctx, s.DB, newUser{
		Username:     username,
		Email:        email,
		Verified:     email != "",
		PasswordHash: hash,
		IsAdmin:      req.IsInstanceAdmin != nil && *req.IsInstanceAdmin,
	})
	if err != nil {
		if isUniqueConstraintErr(err) {
			writeError(w, http.StatusConflict, "conflict", "username or email already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "create user failed")
		return
	}
	// Provision the new user's personal space (best-effort: the user already
	// exists, so a provisioning hiccup shouldn't fail the create — the startup
	// backfill will catch it on next boot).
	if _, err := EnsurePersonalSpace(ctx, s.DB, id, username); err != nil {
		slog.Error("personal space for new user", "user_id", id, "username", username, "err", err)
	}
	dto, err := selectAdminUserByID(ctx, s.DB, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "fetch created user failed")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": dto})
}

// PatchAdminUser updates is_active, is_instance_admin, and/or password on a
// target user. Safeguards: caller can't self-target, can't demote/deactivate
// the last instance-admin. Password reset + is_active=false both clear all
// sessions for the target user in the same tx.
func (s *Server) PatchAdminUser(w http.ResponseWriter, r *http.Request) {
	caller, ok := requireInstanceAdmin(w, r)
	if !ok {
		return
	}
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}

	var req adminUserPatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}
	if req.IsActive == nil && req.IsInstanceAdmin == nil && req.Password == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "at least one of is_active, is_instance_admin, password must be provided")
		return
	}
	if id == caller.ID {
		writeError(w, http.StatusBadRequest, "bad_request", "cannot modify self via admin endpoint")
		return
	}

	var newHash string
	if req.Password != nil {
		if len(*req.Password) < minPasswordLen {
			writeError(w, http.StatusBadRequest, "bad_request", "password must be at least 8 characters")
			return
		}
		h, err := auth.HashPassword(*req.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "hash password failed")
			return
		}
		newHash = h
	}

	ctx := r.Context()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "begin tx failed")
		return
	}
	defer tx.Rollback()

	var (
		existingActive  int
		existingIsAdmin int
	)
	err = tx.QueryRowContext(ctx,
		`SELECT is_active, is_instance_admin FROM users WHERE id = $1`, id).
		Scan(&existingActive, &existingIsAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "user not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "lookup user failed")
		return
	}

	demotingAdmin := existingIsAdmin == 1 && req.IsInstanceAdmin != nil && !*req.IsInstanceAdmin
	deactivatingAdmin := existingIsAdmin == 1 && existingActive == 1 && req.IsActive != nil && !*req.IsActive
	if demotingAdmin || deactivatingAdmin {
		if last, err := wouldLeaveZeroAdminsTx(ctx, tx, id); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "count admins failed")
			return
		} else if last {
			writeError(w, http.StatusBadRequest, "last_admin", "cannot demote or deactivate the last instance admin")
			return
		}
	}

	sets := make([]string, 0, 4)
	args := make([]any, 0, 4)
	if req.IsActive != nil {
		v := 0
		if *req.IsActive {
			v = 1
		}
		sets = append(sets, "is_active = $"+strconv.Itoa(len(args)+1))
		args = append(args, v)
	}
	if req.IsInstanceAdmin != nil {
		v := 0
		if *req.IsInstanceAdmin {
			v = 1
		}
		sets = append(sets, "is_instance_admin = $"+strconv.Itoa(len(args)+1))
		args = append(args, v)
	}
	if req.Password != nil {
		sets = append(sets, "password_hash = $"+strconv.Itoa(len(args)+1))
		args = append(args, newHash)
	}
	sets = append(sets, "updated_at = tela_now()")
	stmt := "UPDATE users SET " + strings.Join(sets, ", ") + " WHERE id = $" + strconv.Itoa(len(args)+1)
	args = append(args, id)
	if _, err := tx.ExecContext(ctx, stmt, args...); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "update user failed")
		return
	}

	// Kill all sessions if password reset or deactivation took effect.
	wipeSessions := req.Password != nil ||
		(req.IsActive != nil && !*req.IsActive && existingActive == 1)
	if wipeSessions {
		if err := auth.DeleteUserSessions(ctx, tx, id); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "clear sessions failed")
			return
		}
	}

	dto, err := selectAdminUserByIDTx(ctx, tx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "fetch updated user failed")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "commit failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": dto})
}

// DeleteAdminUser soft-deletes a user (is_active=0) and wipes their sessions.
// Idempotent on already-inactive users. Same safeguards as PATCH:
// no self-target, can't deactivate the last instance admin.
func (s *Server) DeleteAdminUser(w http.ResponseWriter, r *http.Request) {
	caller, ok := requireInstanceAdmin(w, r)
	if !ok {
		return
	}
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	if id == caller.ID {
		writeError(w, http.StatusBadRequest, "bad_request", "cannot modify self via admin endpoint")
		return
	}

	ctx := r.Context()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "begin tx failed")
		return
	}
	defer tx.Rollback()

	var (
		existingActive  int
		existingIsAdmin int
	)
	err = tx.QueryRowContext(ctx,
		`SELECT is_active, is_instance_admin FROM users WHERE id = $1`, id).
		Scan(&existingActive, &existingIsAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "user not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "lookup user failed")
		return
	}

	if existingIsAdmin == 1 && existingActive == 1 {
		if last, err := wouldLeaveZeroAdminsTx(ctx, tx, id); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "count admins failed")
			return
		} else if last {
			writeError(w, http.StatusBadRequest, "last_admin", "cannot deactivate the last instance admin")
			return
		}
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET is_active = 0, updated_at = tela_now() WHERE id = $1`, id); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "deactivate user failed")
		return
	}
	if err := auth.DeleteUserSessions(ctx, tx, id); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "clear sessions failed")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "commit failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// wouldLeaveZeroAdminsTx returns true if removing or demoting excludeID from
// the active instance-admin set would drop the count to zero. Counted inside
// the same tx as the mutation so a concurrent demote can't race.
func wouldLeaveZeroAdminsTx(ctx context.Context, tx *sql.Tx, excludeID int64) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM users
		 WHERE is_active = 1 AND is_instance_admin = 1 AND id != $1`, excludeID).Scan(&n)
	if err != nil {
		return false, err
	}
	return n == 0, nil
}

type adminUserScanner interface {
	Scan(dest ...any) error
}

func scanAdminUserRow(s adminUserScanner) (adminUserDTO, error) {
	var (
		dto             adminUserDTO
		email, verified sql.NullString
		mcpSeen         sql.NullString
		isAdmin, active int
	)
	if err := s.Scan(&dto.ID, &dto.Username, &dto.DisplayName, &email, &verified, &isAdmin, &active, &dto.PlanKey, &dto.CreatedAt, &dto.UpdatedAt, &mcpSeen); err != nil {
		return adminUserDTO{}, err
	}
	dto.Email = nullableString(email)
	dto.EmailVerified = verified.Valid
	dto.IsInstanceAdmin = isAdmin == 1
	dto.IsActive = active == 1
	dto.McpLastSeen = nullableString(mcpSeen)
	if mcpSeen.Valid {
		dto.UsedMCP = true // any MCP request (PAT or OAuth) counts as connected
	}
	return dto, nil
}

// scanInt64Map runs a two-column (id, count) query and returns it as a map.
// Best-effort: a query error yields an empty map (the caller's rows degrade to
// zero, never failing the whole list). Used for the admin-list enrichments.
func scanInt64Map(ctx context.Context, d *sql.DB, query string, args ...any) map[int64]int64 {
	out := map[int64]int64{}
	rows, err := d.QueryContext(ctx, query, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k, v int64
		if err := rows.Scan(&k, &v); err == nil {
			out[k] = v
		}
	}
	return out
}

func selectAdminUserByID(ctx context.Context, d *sql.DB, id int64) (adminUserDTO, error) {
	row := d.QueryRowContext(ctx, `
		SELECT id, username, display_name, email, email_verified_at, is_instance_admin, is_active, plan_key, created_at, updated_at, mcp_last_seen_at
		  FROM users WHERE id = $1`, id)
	return scanAdminUserRow(row)
}

func selectAdminUserByIDTx(ctx context.Context, tx *sql.Tx, id int64) (adminUserDTO, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, username, display_name, email, email_verified_at, is_instance_admin, is_active, plan_key, created_at, updated_at, mcp_last_seen_at
		  FROM users WHERE id = $1`, id)
	return scanAdminUserRow(row)
}
