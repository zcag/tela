package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/zcag/tela/backend/internal/auth"
	"github.com/zcag/tela/backend/internal/mailer"
)

// M17.A.1 Feedback. Meta-feedback channel for Tela + tela-mcp themselves —
// bugs, friction, suggestions to the developers. DISTINCT from page-content
// comments (use /api/pages/{id}/comments for those). v0 is write-only: no
// GET endpoint, no admin UI; rows accumulate and PO inspects via psql.
//
// Auth: any authenticated caller (session OR bearer token). Bearer scope
// gating is relaxed at the middleware level for POST /api/feedback so that
// read-scope keys — the MCP `submit_feedback` tool's design scope — can
// submit. See auth.scopeAllowsRequest for the carve-out.

const (
	feedbackMaxSubjectLen = 200
	feedbackMaxBodyLen    = 8000
)

// feedbackKinds is the closed set of optional user-chosen types. Anything else
// (including "") is stored as NULL kind — the widget's chips are optional.
var feedbackKinds = map[string]bool{"idea": true, "bug": true, "other": true}

// feedbackDTO is the wire shape returned by CreateFeedback. Mirrors the
// columns 1:1; provenance pointers are nil when the row was inserted by a
// session caller (created_by_api_key_id) or after the underlying user/key
// has been deleted (ON DELETE SET NULL).
type feedbackDTO struct {
	ID                int64   `json:"id"`
	CreatedAt         string  `json:"created_at"`
	CreatedByUserID   *int64  `json:"created_by_user_id"`
	CreatedByAPIKeyID *int64  `json:"created_by_api_key_id"`
	Subject           string  `json:"subject"`
	Body              string  `json:"body"`
	Kind              *string `json:"kind"`
	Source            string  `json:"source"`
	// Context is a free-form bag (map, not json.RawMessage, so the MCP output
	// schema types it as an object rather than a byte array — cf. Page.Props).
	Context map[string]any `json:"context"`
}

type feedbackCreateRequest struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
	// Kind is the optional user-chosen type (idea | bug | other); unknown
	// values fall back to NULL. Context is the free-form client bag (route,
	// page_id, space_id, …) the backend augments with source/app/UA metadata.
	Kind    string          `json:"kind"`
	Context json.RawMessage `json:"context"`
}

// CreateFeedback — POST /api/feedback. Validates trimmed subject + body
// against the migration's CHECK lengths, stamps whichever auth context
// applies (session → user_id only; bearer → user_id + api_key_id), and
// returns the inserted row.
func (s *Server) CreateFeedback(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	var req feedbackCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not parse request body")
		return
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	// Source: a session caller is the in-app web widget; a bearer key hitting
	// the REST endpoint directly is 'api'. The MCP tool sets 'mcp' itself.
	source := "web"
	if k != nil {
		source = "api"
	}
	dto, ae := s.feedbackCore(r.Context(), u, k, req, source, r.UserAgent())
	if ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"feedback": dto})
}

// feedbackCore is the transport-agnostic core behind POST /api/feedback and the
// MCP submit_feedback tool: validate subject + body, stamp the provenance
// (user always; api key when bearer-authed) plus source/kind/context for triage,
// insert, and return the row. Allowed for any scope, including read (the carve-out
// lives in scopeAllowsRequest / mcpRequireWrite is intentionally NOT called).
func (s *Server) feedbackCore(ctx context.Context, u *auth.User, k *auth.APIKey, req feedbackCreateRequest, source, userAgent string) (feedbackDTO, *apiErr) {
	body := strings.TrimSpace(req.Body)
	if body == "" || len(body) > feedbackMaxBodyLen {
		return feedbackDTO{}, &apiErr{http.StatusBadRequest, "bad_request", "body must be 1-8000 characters"}
	}
	// Subject is derived from the first line when the caller omits it (the
	// single-textarea widget), so the user never fills a second field.
	subject := strings.TrimSpace(req.Subject)
	if subject == "" {
		subject = feedbackSubjectFromBody(body)
	}
	if subject == "" || len(subject) > feedbackMaxSubjectLen {
		return feedbackDTO{}, &apiErr{http.StatusBadRequest, "bad_request", "subject must be 1-200 characters"}
	}

	// Kind is optional; only the closed set persists, everything else → NULL.
	var kindArg any = nil
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	if feedbackKinds[kind] {
		kindArg = kind
	} else {
		kind = ""
	}

	// Build the context bag: start from the client's, then stamp server-known
	// metadata it shouldn't have to send (build version + the request UA).
	ctxBag := feedbackMergeContext(req.Context, source, userAgent)

	// OAuth-authed MCP callers (claude.ai/cowork) get a synthetic APIKey with no
	// persisted row (ID 0) — see verifyWorkOSToken. Stamping that into the
	// created_by_api_key_id FK violates the api_keys reference and 500s the insert,
	// so only record a real (persisted) key id; OAuth callers record user only.
	var apiKeyArg any = nil
	if k != nil && k.ID != 0 {
		apiKeyArg = k.ID
	}

	var id int64
	err := s.DB.QueryRowContext(ctx, `
		INSERT INTO feedback (created_by_user_id, created_by_api_key_id, subject, body, source, kind, context)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb) RETURNING id`,
		u.ID, apiKeyArg, subject, body, source, kindArg, string(ctxBag)).Scan(&id)
	if err != nil {
		return feedbackDTO{}, &apiErr{http.StatusInternalServerError, "internal", "insert feedback failed"}
	}
	dto, err := selectFeedbackByID(ctx, s.DB, id)
	if err != nil {
		return feedbackDTO{}, &apiErr{http.StatusInternalServerError, "internal", "fetch created feedback failed"}
	}
	s.notifyNewFeedback(ctx, u, subject, body, kind, source, ctxBag)
	return dto, nil
}

// feedbackSubjectFromBody derives a one-line subject from the body's first
// non-empty line, clamped to the column length (rune-safe).
func feedbackSubjectFromBody(body string) string {
	line := body
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		line = body[:i]
	}
	line = strings.TrimSpace(line)
	if r := []rune(line); len(r) > 120 {
		line = strings.TrimSpace(string(r[:120])) + "…"
	}
	return line
}

// feedbackMergeContext folds server-known fields into the client's context bag
// (without overwriting what the client set) and stamps the source, so a row's
// JSONB is self-describing. Malformed client context is dropped, not fatal.
func feedbackMergeContext(client json.RawMessage, source, userAgent string) json.RawMessage {
	m := map[string]any{}
	if len(client) > 0 {
		_ = json.Unmarshal(client, &m)
	}
	m["source"] = source
	if userAgent != "" {
		if _, ok := m["user_agent"]; !ok {
			m["user_agent"] = userAgent
		}
	}
	if _, ok := m["app_version"]; !ok {
		m["app_version"] = Version
	}
	if _, ok := m["app_commit"]; !ok {
		m["app_commit"] = Commit
	}
	b, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// notifyNewFeedback emails the instance admins that new feedback arrived — the
// email companion to the in-app unread badge. Best-effort: the recipient lookup
// is synchronous (ctx is still live) but the SMTP sends run detached so relay
// latency never slows the submit, and any failure is logged, never surfaced. A
// missing relay (LogMailer) just logs.
//
// Every instance admin is notified — including the submitter. On a single-admin
// instance the admin IS the main user, so self-submitted feedback would
// otherwise never email anyone; the receipt is wanted, not noise.
func (s *Server) notifyNewFeedback(ctx context.Context, submitter *auth.User, subject, body, kind, source string, ctxBag json.RawMessage) {
	emails, err := s.feedbackAdminRecipients(ctx, 0) // 0 matches no user → exclude nobody
	if err != nil {
		slog.Error("feedback: admin lookup failed", "err", err)
		return
	}
	if len(emails) == 0 {
		return
	}
	who := submitter.Username
	page := feedbackPageLabel(ctxBag)
	inbox := canonicalBaseURL() + "/settings?tab=feedback"
	go func() {
		for _, e := range emails {
			if err := s.Mailer.Send(context.Background(), mailer.FeedbackNotice(e, who, subject, body, kind, source, page, inbox)); err != nil {
				slog.Error("feedback: notify send failed", "to", e, "err", err)
			}
		}
	}()
}

// feedbackPageLabel pulls a human page hint ("“Title”") out of the context bag
// for the notification email, so an admin sees what a report is about without
// opening the inbox. Empty when no page is in context.
func feedbackPageLabel(ctxBag json.RawMessage) string {
	var c struct {
		PageTitle string `json:"page_title"`
		PageID    int64  `json:"page_id"`
	}
	if len(ctxBag) > 0 {
		_ = json.Unmarshal(ctxBag, &c)
	}
	if t := strings.TrimSpace(c.PageTitle); t != "" {
		return "“" + t + "”"
	}
	if c.PageID > 0 {
		return fmt.Sprintf("page #%d", c.PageID)
	}
	return ""
}

// feedbackAdminRecipients returns the emails to notify of new feedback: every
// instance admin with a non-empty email, excluding excludeID (0 = exclude none).
func (s *Server) feedbackAdminRecipients(ctx context.Context, excludeID int64) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT email FROM users
		  WHERE is_instance_admin = 1 AND email IS NOT NULL AND email <> '' AND id <> $1
		  ORDER BY email`, excludeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var emails []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err == nil {
			emails = append(emails, e)
		}
	}
	return emails, rows.Err()
}

// feedbackAdminEntry is the read shape for the instance-admin inbox: the row plus
// the submitter's username (joined; nil for a deleted/anonymous user) and a flag
// for whether it came through an API key / agent.
type feedbackAdminEntry struct {
	ID         int64          `json:"id"`
	CreatedAt  string         `json:"created_at"`
	Subject    string         `json:"subject"`
	Body       string         `json:"body"`
	Kind       *string        `json:"kind"`
	Source     string         `json:"source"`
	UserID     *int64         `json:"user_id"`
	Username   *string        `json:"username"`
	ViaAPIKey  bool           `json:"via_api_key"`
	Context    map[string]any `json:"context"`
	Status     string         `json:"status"`
	ResolvedAt *string        `json:"resolved_at"`
}

// feedbackStatuses is the closed set of triage states (migration 0071).
// Deliberately three: an entry is waiting, handled, or declined. Anything
// richer (assignees, threads, priorities) is a tracker — tela doesn't have one
// and this inbox doesn't have the volume to want one.
var feedbackStatuses = map[string]bool{"open": true, "done": true, "wontfix": true}

// ListFeedback returns the most recent feedback across the instance, newest first.
// Instance-admin only — feedback is global (about tela itself), not org-scoped.
func (s *Server) ListFeedback(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireInstanceAdmin(w, r); !ok {
		return
	}
	limit := clampLimit(r.URL.Query().Get("limit"), 100, 200)
	// ?status=open|done|wontfix narrows the inbox; anything else (including the
	// absent param) lists everything, so the filter can never silently hide rows
	// on a typo'd value.
	statusFilter := r.URL.Query().Get("status")
	if !feedbackStatuses[statusFilter] {
		statusFilter = ""
	}
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT f.id, f.created_at, f.subject, f.body, f.kind, f.source, f.context,
		       f.created_by_user_id, u.username,
		       CASE WHEN f.created_by_api_key_id IS NOT NULL THEN 1 ELSE 0 END,
		       f.status, f.resolved_at
		  FROM feedback f
		  LEFT JOIN users u ON u.id = f.created_by_user_id
		 WHERE ($2 = '' OR f.status = $2)
		 ORDER BY f.id DESC
		 LIMIT $1`, limit, statusFilter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "list feedback failed")
		return
	}
	defer rows.Close()
	entries := []feedbackAdminEntry{}
	for rows.Next() {
		var (
			e          feedbackAdminEntry
			kind       sql.NullString
			ctxRaw     []byte
			userID     sql.NullInt64
			username   sql.NullString
			viaKey     int
			resolvedAt sql.NullString
		)
		if err := rows.Scan(&e.ID, &e.CreatedAt, &e.Subject, &e.Body, &kind, &e.Source, &ctxRaw, &userID, &username, &viaKey, &e.Status, &resolvedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "scan feedback row failed")
			return
		}
		e.ResolvedAt = nullableString(resolvedAt)
		e.Kind = nullableString(kind)
		if len(ctxRaw) > 0 {
			_ = json.Unmarshal(ctxRaw, &e.Context)
		}
		if userID.Valid {
			e.UserID = &userID.Int64
		}
		e.Username = nullableString(username)
		e.ViaAPIKey = viaKey == 1
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "iterate feedback failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"feedback": entries})
}

// UpdateFeedbackStatus moves one entry between open/done/wontfix —
// PATCH /api/admin/feedback/{id}. Instance-admin only.
//
// resolved_at is derived, never client-supplied: stamped on the move off 'open'
// and cleared on the move back, so it can't drift out of agreement with status.
func (s *Server) UpdateFeedbackStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireInstanceAdmin(w, r); !ok {
		return
	}
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not parse request body")
		return
	}
	if !feedbackStatuses[req.Status] {
		writeError(w, http.StatusBadRequest, "bad_request", "status must be one of open, done, wontfix")
		return
	}
	res, err := s.DB.ExecContext(r.Context(),
		`UPDATE feedback
		    SET status = $1,
		        resolved_at = CASE WHEN $1 = 'open' THEN NULL ELSE tela_now() END
		  WHERE id = $2`, req.Status, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "update feedback failed")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "no such feedback")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// MarkFeedbackSeen stamps the caller's feedback_seen_at to now, clearing the
// unread badge. Instance-admin only (the inbox is admin-gated).
func (s *Server) MarkFeedbackSeen(w http.ResponseWriter, r *http.Request) {
	u, ok := requireInstanceAdmin(w, r)
	if !ok {
		return
	}
	if _, err := s.DB.ExecContext(r.Context(),
		`UPDATE users SET feedback_seen_at = tela_now() WHERE id = $1`, u.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "mark seen failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func selectFeedbackByID(ctx context.Context, q *sql.DB, id int64) (feedbackDTO, error) {
	row := q.QueryRowContext(ctx, `
		SELECT id, created_at, created_by_user_id, created_by_api_key_id, subject, body, kind, source, context
		  FROM feedback WHERE id = $1`, id)
	return scanFeedback(row)
}

func scanFeedback(r rowScanner) (feedbackDTO, error) {
	var (
		dto      feedbackDTO
		userID   sql.NullInt64
		apiKeyID sql.NullInt64
		kind     sql.NullString
		ctxRaw   []byte
	)
	if err := r.Scan(&dto.ID, &dto.CreatedAt, &userID, &apiKeyID, &dto.Subject, &dto.Body, &kind, &dto.Source, &ctxRaw); err != nil {
		return feedbackDTO{}, err
	}
	if userID.Valid {
		v := userID.Int64
		dto.CreatedByUserID = &v
	}
	if apiKeyID.Valid {
		v := apiKeyID.Int64
		dto.CreatedByAPIKeyID = &v
	}
	dto.Kind = nullableString(kind)
	if len(ctxRaw) > 0 {
		_ = json.Unmarshal(ctxRaw, &dto.Context)
	}
	return dto, nil
}
