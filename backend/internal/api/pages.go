package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/zcag/tela/backend/internal/auth"
	"github.com/zcag/tela/backend/internal/models"
	"github.com/zcag/tela/backend/internal/pagemd"
)

const maxPageTitleLen = 500

type pageCreateRequest struct {
	SpaceID  int64          `json:"space_id"`
	ParentID *int64         `json:"parent_id"`
	Title    string         `json:"title"`
	Body     string         `json:"body"`
	Props    map[string]any `json:"props"`
	// Filename pins the page's stable /dav/ on-disk name (sync-create only; the
	// REST/MCP create paths leave it nil → name falls back to slugify(title)).
	Filename *string `json:"-"`
}

// pageUpdateRequest patches a page. A nil field is left unchanged; a non-nil
// Props (including an explicit {}) replaces the whole bag (Replace/PUT semantics
// — see docs/page-properties.md "Update semantics").
type pageUpdateRequest struct {
	Title *string        `json:"title"`
	Body  *string        `json:"body"`
	Props map[string]any `json:"props"`
}

// propsJSON marshals a props bag to a JSON string for binding into a JSONB
// column (with a ::jsonb cast at the call site). Empty/nil → "{}".
func propsJSON(props map[string]any) string {
	if len(props) == 0 {
		return "{}"
	}
	b, err := json.Marshal(props)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// pageNode mirrors models.Page (promoted via embedding) plus a children slice
// so the tree endpoint can return a nested structure. Exposure is the resolved
// public-link visibility (see exposure.go) so the sidebar can render markers.
type pageNode struct {
	models.Page
	Exposure *pageExposure `json:"exposure"`
	Children []*pageNode   `json:"children"`
}

// pageWithExposure is the flat-list row enriched with resolved exposure, used
// by the non-tree ListPages branch so lazy-loaded children carry their state.
type pageWithExposure struct {
	models.Page
	Exposure *pageExposure `json:"exposure"`
}

// pageListItem is the flat cross-space row returned by ListAllPages — no
// body, no parent_id, no timestamps; just enough for the wikilink picker
// (id, space hint, title, breadcrumb).
type pageListItem struct {
	ID         int64    `json:"id"`
	SpaceID    int64    `json:"space_id"`
	SpaceName  string   `json:"space_name"`
	Title      string   `json:"title"`
	Breadcrumb []string `json:"breadcrumb"`
}

func (s *Server) ListPages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	spaceIDStr := q.Get("space_id")
	if spaceIDStr == "" {
		writeError(w, http.StatusBadRequest, "invalid_query", "space_id is required")
		return
	}
	spaceID, err := strconv.ParseInt(spaceIDStr, 10, 64)
	if err != nil || spaceID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_query", "space_id must be a positive integer")
		return
	}

	if err := verifySpaceExists(r.Context(), s.DB, spaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "space_not_found", "space not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "lookup space failed")
		return
	}
	if _, ok := s.requireMembership(w, r, spaceID); !ok {
		return
	}

	if q.Get("tree") == "1" {
		tree, err := buildPageTree(r.Context(), s.DB, spaceID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "build page tree failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"pages": tree})
		return
	}

	parentIDStr := q.Get("parent_id")
	parentIDPresent := q.Has("parent_id")

	var parentID *int64
	if parentIDPresent && parentIDStr != "" && parentIDStr != "null" {
		pid, perr := strconv.ParseInt(parentIDStr, 10, 64)
		if perr != nil || pid <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_query", "parent_id must be a positive integer or 'null'")
			return
		}
		parentID = &pid
	}

	out, err := listPagesFlat(r.Context(), s.DB, spaceID, parentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "list pages failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pages": out})
}

// listPagesFlat returns the direct children of parentID in spaceID (roots when
// parentID is nil), each enriched with resolved exposure. Auth-free shared core
// behind the flat branch of GET /api/pages and the MCP list_pages tool — the
// caller must do space-exists + membership gating first (listPagesCore does for
// the MCP path; ListPages does inline for both its tree and flat branches).
func listPagesFlat(ctx context.Context, db *sql.DB, spaceID int64, parentID *int64) ([]pageWithExposure, error) {
	var rows *sql.Rows
	var err error
	if parentID == nil {
		rows, err = db.QueryContext(ctx,
			`SELECT id, space_id, parent_id, title, body, position, props, created_at, updated_at, filename
			 FROM pages WHERE space_id = $1 AND parent_id IS NULL AND deleted_at IS NULL
			 ORDER BY position ASC, id ASC`, spaceID)
	} else {
		rows, err = db.QueryContext(ctx,
			`SELECT id, space_id, parent_id, title, body, position, props, created_at, updated_at, filename
			 FROM pages WHERE space_id = $1 AND parent_id = $2 AND deleted_at IS NULL
			 ORDER BY position ASC, id ASC`, spaceID, *parentID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	pages := []models.Page{}
	for rows.Next() {
		p, err := scanPageFromRows(rows)
		if err != nil {
			return nil, err
		}
		pages = append(pages, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	exposures, err := resolveSpaceExposures(ctx, db, spaceID)
	if err != nil {
		return nil, err
	}
	out := make([]pageWithExposure, 0, len(pages))
	for _, p := range pages {
		e := exposures[p.ID]
		out = append(out, pageWithExposure{Page: p, Exposure: &e})
	}
	return out, nil
}

// listPagesCore is the transport-agnostic core behind the MCP list_pages tool:
// verify the space exists, gate on membership, then return the flat child list.
// Mirrors the flat branch of ListPages with the same checks in the same order.
func (s *Server) listPagesCore(ctx context.Context, u *auth.User, k *auth.APIKey, spaceID int64, parentID *int64) ([]pageWithExposure, *apiErr) {
	if err := verifySpaceExists(ctx, s.DB, spaceID); errors.Is(err, sql.ErrNoRows) {
		return nil, &apiErr{http.StatusNotFound, "space_not_found", "space not found"}
	} else if err != nil {
		return nil, &apiErr{http.StatusInternalServerError, "internal", "lookup space failed"}
	}
	if _, ae := s.membershipCore(ctx, u, k, spaceID); ae != nil {
		return nil, ae
	}
	out, err := listPagesFlat(ctx, s.DB, spaceID, parentID)
	if err != nil {
		return nil, &apiErr{http.StatusInternalServerError, "internal", "list pages failed"}
	}
	return out, nil
}

// ListAllPages returns every page in every space the caller is a member of
// as a flat list, ordered by space_name then title. Powers the cross-space
// `[[Page]]` picker. No pagination — single-user, <100 pages assumed (same
// bound as the orama tier-1 index).
func (s *Server) ListAllPages(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	// Bearer-mode with a space_id restriction narrows the cross-space listing
	// to just that one space. Without the filter the wikilink picker would
	// surface page ids the key isn't allowed to open (the actual GET would
	// then 403 — confusing UX, defends against accidental id leakage).
	var (
		rows *sql.Rows
		err  error
	)
	if k, isBearer := auth.APIKeyFromContext(r.Context()); isBearer && k.SpaceID != nil {
		rows, err = s.DB.QueryContext(r.Context(), `
			SELECT p.id, p.space_id, s.name, p.title
			  FROM pages p
			  JOIN spaces s ON s.id = p.space_id
			  JOIN (SELECT DISTINCT space_id FROM space_access WHERE user_id = $1) sm ON sm.space_id = p.space_id
			 WHERE p.space_id = $2 AND p.deleted_at IS NULL
			 ORDER BY s.name ASC, p.title ASC`, u.ID, *k.SpaceID)
	} else {
		rows, err = s.DB.QueryContext(r.Context(), `
			SELECT p.id, p.space_id, s.name, p.title
			  FROM pages p
			  JOIN spaces s ON s.id = p.space_id
			  JOIN (SELECT DISTINCT space_id FROM space_access WHERE user_id = $1) sm ON sm.space_id = p.space_id
			 WHERE p.deleted_at IS NULL
			 ORDER BY s.name ASC, p.title ASC`, u.ID)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "list pages failed")
		return
	}
	defer rows.Close()

	type rowItem struct {
		ID, SpaceID      int64
		SpaceName, Title string
	}
	items := []rowItem{}
	for rows.Next() {
		var it rowItem
		if err := rows.Scan(&it.ID, &it.SpaceID, &it.SpaceName, &it.Title); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "scan page row failed")
			return
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "iterate pages failed")
		return
	}

	out := make([]pageListItem, 0, len(items))
	for _, it := range items {
		bc, err := pageBreadcrumb(r.Context(), s.DB, it.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "build breadcrumb failed")
			return
		}
		out = append(out, pageListItem{
			ID:         it.ID,
			SpaceID:    it.SpaceID,
			SpaceName:  it.SpaceName,
			Title:      it.Title,
			Breadcrumb: bc,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"pages": out})
}

func (s *Server) CreatePage(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	var req pageCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	page, ae := s.createPageCore(r.Context(), u, k, req, true)
	if ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"page": page})
}

// createPageCore is the transport-agnostic core behind POST /api/pages and the
// MCP create_page tool: validate, gate on editor+ membership (bearer space-scope
// first), then insert the page and sync its outgoing wikilinks in one tx.
// leadingTitleH1RE matches a leading ATX H1 at the very top of a body (only
// whitespace may precede it), capturing the heading text.
var leadingTitleH1RE = regexp.MustCompile(`\A\s*#[ \t]+([^\r\n]+?)[ \t]*(\r?\n|\z)`)

// agentWriteCtxKey marks a context as originating from an agent (MCP) write so
// the page cores tag the revision source 'agent' — what the "changes by your AI"
// feed filters on. updatePageCore already gets an agentWrite param; createPageCore
// is called by 9 sites, so the agent signal rides the context instead (set by the
// one MCP create_page handler) to avoid churning every caller.
type agentWriteCtxKey struct{}

func withAgentWrite(ctx context.Context) context.Context {
	return context.WithValue(ctx, agentWriteCtxKey{}, true)
}

func isAgentWrite(ctx context.Context) bool {
	v, _ := ctx.Value(agentWriteCtxKey{}).(bool)
	return v
}

// stripLeadingTitleH1 drops a leading `# Heading` from body when its text equals
// title (case-insensitive). Title and body are separate columns, but MCP/agent
// clients habitually open the markdown with `# Same Title`, which then renders
// twice — once as the page title, once as a body heading. A leading H1 that
// differs from the title is left untouched.
func stripLeadingTitleH1(body, title string) string {
	m := leadingTitleH1RE.FindStringSubmatchIndex(body)
	if m == nil {
		return body
	}
	heading := strings.TrimSpace(body[m[2]:m[3]])
	if !strings.EqualFold(heading, strings.TrimSpace(title)) {
		return body
	}
	return strings.TrimLeft(body[m[1]:], "\r\n")
}

// isDeckBag reports whether a props bag marks a deck. A deck's body is Slidev
// markdown, so its leading frontmatter is the deck headmatter (first slide) and
// must NOT be stripped into props.
func isDeckBag(props map[string]any) bool {
	b, _ := props["deck"].(bool)
	return b
}

// pageIsDeckTx reads the stored deck flag for a page (for body-only updates that
// don't carry props). props is jsonb; props->>'deck' is the text 'true'/'false'.
func pageIsDeckTx(ctx context.Context, tx *sql.Tx, id int64) bool {
	var v sql.NullString
	_ = tx.QueryRowContext(ctx, `SELECT props->>'deck' FROM pages WHERE id = $1`, id).Scan(&v)
	return v.Valid && v.String == "true"
}

// isSheetBag reports whether a props bag marks a spreadsheet ("sheet"). A sheet's
// body is Defter markdown (compact GFM tables + a fenced defter-style block), so —
// exactly like a deck — it is a verbatim-body doc-type: nothing in the body is page
// frontmatter and it must never be run through the prose strip/normalize path.
func isSheetBag(props map[string]any) bool {
	b, _ := props["sheet"].(bool)
	return b
}

// pageIsSheetTx reads the stored sheet flag for a page (body-only updates that
// don't carry props). props->>'sheet' is the text 'true'/'false'.
func pageIsSheetTx(ctx context.Context, tx *sql.Tx, id int64) bool {
	var v sql.NullString
	_ = tx.QueryRowContext(ctx, `SELECT props->>'sheet' FROM pages WHERE id = $1`, id).Scan(&v)
	return v.Valid && v.String == "true"
}

// notify controls the social create notifications (mentions + page_created):
// true for interactive creates (REST / MCP / assist), false for bulk ingestion
// (file sync, conflict-resolve, seeded pages) so a vault sync can't storm space
// followers. Auto-follow (autowatch) runs regardless of notify, gated only by
// the user's autowatch preference.
func (s *Server) createPageCore(ctx context.Context, u *auth.User, k *auth.APIKey, req pageCreateRequest, notify bool) (models.Page, *apiErr) {
	title := strings.TrimSpace(req.Title)
	if title == "" {
		return models.Page{}, &apiErr{http.StatusBadRequest, "invalid_title", "title is required"}
	}
	// Body invariant: frontmatter never lives in pages.body, at any ingress —
	// EXCEPT the verbatim-body doc-types (deck, sheet). A deck's body is Slidev
	// markdown (leading `---...---` is the deck headmatter / first slide); a sheet's
	// body is Defter markdown (compact tables + a defter-style block). Neither is
	// prose, so their bodies are stored byte-for-byte — never strip/normalize. For a
	// normal page strip leading frontmatter into props.
	props := pagemd.FilterReserved(req.Props)
	stripped, _, bodyProps := pagemd.Decode(req.Body)
	isDeck := isDeckBag(props) || isDeckBag(bodyProps)
	isSheet := isSheetBag(props) || isSheetBag(bodyProps)
	var body string
	if isDeck || isSheet {
		body = req.Body
		if props == nil {
			props = map[string]any{}
		}
		if isDeck {
			props["deck"] = true
		}
		if isSheet {
			props["sheet"] = true
		}
	} else {
		body = stripLeadingTitleH1(stripped, title)
		if props == nil {
			props = bodyProps
		}
	}
	if props == nil {
		props = map[string]any{}
	}
	if len(title) > maxPageTitleLen {
		return models.Page{}, &apiErr{http.StatusBadRequest, "invalid_title", "title exceeds 500 characters"}
	}
	if req.SpaceID <= 0 {
		return models.Page{}, &apiErr{http.StatusBadRequest, "invalid_space_id", "space_id is required"}
	}
	if req.ParentID != nil && *req.ParentID <= 0 {
		return models.Page{}, &apiErr{http.StatusBadRequest, "invalid_parent_id", "parent_id must be a positive integer or null"}
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "begin tx failed"}
	}
	defer tx.Rollback()

	if ae := apiKeySpaceScopeErr(k, req.SpaceID); ae != nil {
		return models.Page{}, ae
	}
	role, err := spaceRoleTx(ctx, tx, u.ID, req.SpaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return models.Page{}, &apiErr{http.StatusForbidden, "forbidden", "not a member"}
	}
	if err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "lookup membership failed"}
	}
	if !canEdit(role) {
		return models.Page{}, &apiErr{http.StatusForbidden, "forbidden", "editor or owner role required"}
	}

	// Agent-authored decks are lint-gated so a structurally broken deck (one that
	// 404s on Present) can't be saved silently — the agent gets the issues back to
	// fix. Humans editing in the FE autosave through transient states and are not
	// gated; fail-open on a sidecar outage (see deckWriteGate).
	if isAgentWrite(ctx) && isDeckBag(props) {
		if ae := s.deckWriteGate(ctx, body); ae != nil {
			return models.Page{}, ae
		}
	}
	// Agent-authored sheets are structurally lint-gated (in-process defterparse)
	// so a broken Defter body can't be saved silently — the agent gets the issues
	// back to fix. A sheet stores its body verbatim, so `body` is what persists.
	if isAgentWrite(ctx) && isSheetBag(props) {
		if ae := s.sheetWriteGate(body); ae != nil {
			return models.Page{}, ae
		}
	}

	if err := verifySpaceExistsTx(ctx, tx, req.SpaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return models.Page{}, &apiErr{http.StatusBadRequest, "space_not_found", "space does not exist"}
		}
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "lookup space failed"}
	}

	if ae := s.checkPageQuota(ctx, req.SpaceID); ae != nil {
		return models.Page{}, ae
	}

	if req.ParentID != nil {
		var parentSpaceID int64
		err := tx.QueryRowContext(ctx,
			`SELECT space_id FROM pages WHERE id = $1 AND deleted_at IS NULL`, *req.ParentID).Scan(&parentSpaceID)
		if errors.Is(err, sql.ErrNoRows) {
			return models.Page{}, &apiErr{http.StatusBadRequest, "parent_not_found", "parent page does not exist"}
		}
		if err != nil {
			return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "lookup parent failed"}
		}
		if parentSpaceID != req.SpaceID {
			return models.Page{}, &apiErr{http.StatusBadRequest, "parent_space_mismatch", "parent page is in a different space"}
		}
	}

	var maxPos sql.NullInt64
	if req.ParentID == nil {
		err = tx.QueryRowContext(ctx,
			`SELECT MAX(position) FROM pages WHERE space_id = $1 AND parent_id IS NULL AND deleted_at IS NULL`, req.SpaceID).Scan(&maxPos)
	} else {
		err = tx.QueryRowContext(ctx,
			`SELECT MAX(position) FROM pages WHERE space_id = $1 AND parent_id = $2 AND deleted_at IS NULL`, req.SpaceID, *req.ParentID).Scan(&maxPos)
	}
	if err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "compute position failed"}
	}
	var position int64
	if maxPos.Valid {
		position = maxPos.Int64 + 1
	}

	var id int64
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO pages(space_id, parent_id, title, body, position, props, filename) VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7) RETURNING id`,
		req.SpaceID, nullableInt64(req.ParentID), title, body, position, propsJSON(props), nullableFilename(req.Filename)).Scan(&id); err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "create page failed"}
	}
	if err := syncPageLinks(ctx, tx, id, body); err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "sync page_links failed"}
	}
	if err := appendChangeLog(ctx, tx, req.SpaceID, id, changeCreated); err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "append change_log failed"}
	}
	page, err := selectPageByIDTx(ctx, tx, id)
	if err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "fetch created page failed"}
	}
	if err := tx.Commit(); err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "commit failed"}
	}
	// Snapshot the as-created state as the first revision (source='create'): gives
	// history a baseline to diff/restore against, and surfaces the new page in the
	// recent-changes / my-edits feeds immediately — creation IS recent activity,
	// not just later edits. Post-commit + best-effort, like the edit snapshot.
	createAuthor := u.ID
	createSource := "create"
	if isAgentWrite(ctx) {
		createSource = "agent"
	}
	if _, err := insertPageRevision(ctx, s.DB, id, page.Body, page.Title, props, &createAuthor, createSource); err != nil {
		slog.Error("page create revision failed", "page_id", id, "err", err)
	}
	recordEvent(ctx, s.DB, eventInput{
		Type: evtPageCreate, ActorUserID: &createAuthor, TargetKind: "page",
		TargetID: &id, TargetLabel: page.Title, Detail: createSource,
	})
	// Index + summarize the new page's content (debounced, async; each a no-op
	// when its service is off). Lives in the core so both POST /api/pages and
	// the MCP create_page tool enqueue them.
	s.rag.QueueReindex(id)
	s.summarize.Queue(id)
	s.agreement.Queue(id)
	// A new deck (created via UI/MCP/sync): pre-warm its Present build now so the
	// first present is instant rather than paying the cold slidev build.
	if isDeckBag(props) {
		s.deckWarm.schedule(id)
	}
	// Social notifications fire only for interactive creates, never bulk sync
	// ingestion (which would storm @-mentioned users and space followers).
	if notify {
		s.notifyPageMentions(ctx, u, id, req.SpaceID, page.Title, page.Body)
		s.notifyPageCreated(ctx, u, id, req.SpaceID, page.Title)
	}
	// The author auto-follows their new page (autowatch), so they hear about
	// others' edits without an explicit follow.
	s.autoFollow(ctx, u.ID, id)
	return page, nil
}

func (s *Server) GetPage(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	p, ae := s.getPageCore(r.Context(), u, k, id)
	if ae != nil {
		// A denial on published content carries where to read it instead; the
		// SPA (PageView) redirects there rather than showing a permission wall.
		if path := s.publicReaderFallbackPath(r.Context(), ae, id); path != "" {
			writeJSON(w, http.StatusForbidden, errorBody{
				Error:      "not a member; this page is published and readable without an account",
				Code:       "forbidden_public",
				PublicPath: path,
			})
			return
		}
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	exp, err := resolvePageExposure(r.Context(), s.DB, p.ID, p.SpaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "resolve exposure failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"page": p, "exposure": exp})
}

// RecordPageView logs a logged-in page view into the unified Events feed —
// POST /api/pages/{id}/view. A fire-and-forget beacon the SPA fires when a page
// is actually displayed (not on hover-prefetch, which is why this is a separate
// call from GetPage). Gated on the same page access as GetPage; always 204, and
// a failed access check is swallowed so the beacon never affects the UI or leaks
// page existence.
func (s *Server) RecordPageView(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	p, ae := s.getPageCore(r.Context(), u, k, id)
	if ae == nil {
		pid := p.ID
		s.recordRequestEvent(r, eventInput{
			Type: evtPageView, ActorUserID: &u.ID, ActorLabel: u.Username,
			TargetKind: "page", TargetID: &pid, TargetLabel: p.Title,
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

// getPageCore is the transport-agnostic core behind GET /api/pages/{id} and the
// MCP get_page tool: fetch the page and gate on space membership. Missing page
// collapses to the same 403 a non-member sees so ids can't be enumerated across
// spaces. The REST route additionally resolves exposure (the MCP tool doesn't
// need it).
func (s *Server) getPageCore(ctx context.Context, u *auth.User, k *auth.APIKey, id int64) (models.Page, *apiErr) {
	p, err := selectPageByID(ctx, s.DB, id)
	if errors.Is(err, sql.ErrNoRows) {
		return models.Page{}, &apiErr{http.StatusForbidden, "forbidden", "not a member"}
	}
	if err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "fetch page failed"}
	}
	if _, ae := s.membershipCore(ctx, u, k, p.SpaceID); ae != nil {
		return models.Page{}, ae
	}
	return p, nil
}

// publicReaderFallbackPath returns the no-login reader route for pageID when a
// membership denial should redirect rather than dead-end: the caller holds no
// space_access, but the space is published public, so the identical content
// reads without an account.
//
// Same rule /p/{id} applies (HandlePublicShare) — a public space's pages are
// already world-readable, surfaced to non-members by ranked search
// (searchAccessSQL) and indexed for SEO — so a 403 on the authed URL was an
// inconsistency, not a boundary. Returns "" (caller keeps the plain denial) for
// anything else: only a bare `forbidden` is eligible, since an
// api_key_space_scope denial is a real ceiling. A missing page also yields ""
// via selectPageByID, preserving getPageCore's deliberate collapse of
// not-found into not-a-member (no cross-space id enumeration).
func (s *Server) publicReaderFallbackPath(ctx context.Context, ae *apiErr, pageID int64) string {
	if ae.Code != "forbidden" {
		return ""
	}
	p, err := selectPageByID(ctx, s.DB, pageID)
	if err != nil {
		return ""
	}
	sp, err := selectSpaceByID(ctx, s.DB, p.SpaceID)
	if err != nil || sp.Visibility != spaceVisibilityPublic {
		return ""
	}
	return s.canonicalPagePath(ctx, p.SpaceID, p.ID, p.Title)
}

func (s *Server) UpdatePage(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	var req pageUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	// agentWrite=false: a REST save is the editor's own collab-synced write (or a
	// generic client); it must NOT drop the Yjs overlay it is in sync with.
	p, ae := s.updatePageCore(r.Context(), u, k, id, req, false)
	if ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"page": p})
}

// validateUpdateReq checks a page patch's fields without touching the DB, so
// the cheap 400s (no_fields / invalid_title) surface before any tx or auth —
// preserving error precedence across every caller of applyUpdateTx.
func validateUpdateReq(req pageUpdateRequest) *apiErr {
	if req.Title == nil && req.Body == nil && req.Props == nil {
		return &apiErr{http.StatusBadRequest, "no_fields", "at least one of title, body, props must be provided"}
	}
	if req.Title != nil {
		title := strings.TrimSpace(*req.Title)
		if title == "" {
			return &apiErr{http.StatusBadRequest, "invalid_title", "title cannot be empty"}
		}
		if len(title) > maxPageTitleLen {
			return &apiErr{http.StatusBadRequest, "invalid_title", "title exceeds 500 characters"}
		}
	}
	return nil
}

// requireEditTx authorizes u (optionally scoped by api key k) as an editor+ of
// spaceID within an open tx: api-key space scope, membership, then edit role.
// The shared source-auth gate for in-tx page mutations (update / move / sync).
func (s *Server) requireEditTx(ctx context.Context, tx *sql.Tx, u *auth.User, k *auth.APIKey, spaceID int64) *apiErr {
	if ae := apiKeySpaceScopeErr(k, spaceID); ae != nil {
		return ae
	}
	role, err := spaceRoleTx(ctx, tx, u.ID, spaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return &apiErr{http.StatusForbidden, "forbidden", "not a member"}
	}
	if err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "lookup membership failed"}
	}
	if !canEdit(role) {
		return &apiErr{http.StatusForbidden, "forbidden", "editor or owner role required"}
	}
	return nil
}

// applyUpdateTx writes a validated page patch within an open tx and returns the
// re-fetched page. It updates only the provided columns, enforces the body
// invariant (frontmatter stripped → props), and re-syncs wikilinks when the body
// changes. No validation (caller ran validateUpdateReq), no auth (caller ran
// requireEditTx), no commit, and no after-commit effects (revision / RAG / Yjs)
// — those are afterPageWrite's, run by the caller post-commit. Shared by
// updatePageCore and the sync ingress so the write path is one mechanism.
func applyUpdateTx(ctx context.Context, tx *sql.Tx, id int64, req pageUpdateRequest) (models.Page, *apiErr) {
	sets := make([]string, 0, 4)
	args := make([]any, 0, 4)

	if req.Title != nil {
		args = append(args, strings.TrimSpace(*req.Title))
		sets = append(sets, "title = $"+strconv.Itoa(len(args)))
	}
	// Body invariant: strip leading frontmatter into props (bodyStripped is what
	// gets stored + link-synced) — EXCEPT the verbatim-body doc-types (deck, sheet),
	// whose bodies are Slidev / Defter markdown and must be kept byte-for-byte.
	// Type: an explicit props flag wins; otherwise the stored page.
	var bodyStripped string
	var bodyProps map[string]any
	if req.Body != nil {
		verbatim := false
		if req.Props != nil {
			fp := pagemd.FilterReserved(req.Props)
			verbatim = isDeckBag(fp) || isSheetBag(fp)
		} else {
			verbatim = pageIsDeckTx(ctx, tx, id) || pageIsSheetTx(ctx, tx, id)
		}
		stripped, _, bp := pagemd.Decode(*req.Body)
		if verbatim || isDeckBag(bp) || isSheetBag(bp) {
			bodyStripped = *req.Body // verbatim — preserve deck headmatter / defter tables
		} else {
			bodyStripped = stripped
			bodyProps = bp
		}
		args = append(args, bodyStripped)
		sets = append(sets, "body = $"+strconv.Itoa(len(args)))
	}
	// Props: Replace semantics. An explicit props field wins over frontmatter
	// found in body; a nil field leaves the bag unchanged.
	if req.Props != nil {
		args = append(args, propsJSON(pagemd.FilterReserved(req.Props)))
		sets = append(sets, "props = $"+strconv.Itoa(len(args))+"::jsonb")
	} else if bodyProps != nil {
		args = append(args, propsJSON(bodyProps))
		sets = append(sets, "props = $"+strconv.Itoa(len(args))+"::jsonb")
	}
	sets = append(sets, "updated_at = tela_now()")
	args = append(args, id)

	stmt := "UPDATE pages SET " + strings.Join(sets, ", ") + " WHERE id = $" + strconv.Itoa(len(args))
	res, err := tx.ExecContext(ctx, stmt, args...)
	if err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "update page failed"}
	}
	n, err := res.RowsAffected()
	if err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "update page: rows affected failed"}
	}
	if n == 0 {
		return models.Page{}, &apiErr{http.StatusNotFound, "not_found", "page not found"}
	}
	if req.Body != nil {
		if err := syncPageLinks(ctx, tx, id, bodyStripped); err != nil {
			return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "sync page_links failed"}
		}
	}
	p, err := selectPageByIDTx(ctx, tx, id)
	if err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "fetch updated page failed"}
	}
	if err := appendChangeLog(ctx, tx, p.SpaceID, p.ID, changeUpdated); err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "append change_log failed"}
	}
	return p, nil
}

// afterPageWrite runs the post-commit side effects of a page write: snapshot a
// revision and queue a RAG reindex when body or title actually changed, and (for
// out-of-band/agent writes) reset the live Yjs overlay so editors re-seed from
// the new body. Runs after commit so a failure here can't roll the save back —
// it logs and proceeds. Shared by updatePageCore and the sync ingress.
func (s *Server) afterPageWrite(ctx context.Context, existing, p models.Page, bodyProvided, agentWrite bool, authorID int64, source string) {
	changed := p.Body != existing.Body || p.Title != existing.Title
	if changed {
		// Title is folded into each chunk's embed text and body is the source,
		// so reindex on either change (debounced, async; no-op when RAG is off).
		if _, err := insertPageRevision(ctx, s.DB, p.ID, p.Body, p.Title, p.Props, &authorID, source); err != nil {
			slog.Error("page snapshot revision failed", "page_id", p.ID, "err", err)
		}
		pid := p.ID
		recordEvent(ctx, s.DB, eventInput{
			Type: evtPageEdit, ActorUserID: &authorID, TargetKind: "page",
			TargetID: &pid, TargetLabel: p.Title, Detail: source,
		})
		s.rag.QueueReindex(p.ID)
		s.summarize.Queue(p.ID)
		s.agreement.Queue(p.ID)
	}
	// When the body is rewritten out-of-band (MCP agent, file sync), drop the Yjs
	// collab overlay so live + next editors re-seed from the new body instead of
	// masking it with stale CRDT state. DB-wins, per the agent-backend sync design.
	if agentWrite && bodyProvided && p.Body != existing.Body {
		if err := s.rooms.resetPage(ctx, s.DB, p.ID); err != nil {
			slog.Error("page collab overlay reset failed", "page_id", p.ID, "err", err)
		}
	}
	// A deck whose source changed: pre-warm its Present build so the next present
	// is instant. afterPageWrite is the universal save hook — every write path
	// funnels through it (UI, MCP, WebDAV/rsync, sync) — so all of them warm.
	if p.Body != existing.Body && isDeckBag(p.Props) {
		s.deckWarm.schedule(p.ID)
	}
}

// updatePageCore is the transport-agnostic core behind PATCH /api/pages/{id} and
// the MCP update_page tool: patch title and/or body under editor+ membership,
// re-sync wikilinks when the body changes, and snapshot a revision after commit.
func (s *Server) updatePageCore(ctx context.Context, u *auth.User, k *auth.APIKey, id int64, req pageUpdateRequest, agentWrite bool) (models.Page, *apiErr) {
	if ae := validateUpdateReq(req); ae != nil {
		return models.Page{}, ae
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "begin tx failed"}
	}
	defer tx.Rollback()

	existing, err := selectPageByIDTx(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return models.Page{}, &apiErr{http.StatusForbidden, "forbidden", "not a member"}
	}
	if err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "lookup page failed"}
	}
	if ae := s.requireEditTx(ctx, tx, u, k, existing.SpaceID); ae != nil {
		return models.Page{}, ae
	}
	// Lint-gate agent rewrites of a deck body (see createPageCore). A deck stores
	// its body verbatim, so *req.Body is exactly what would be persisted.
	if agentWrite && req.Body != nil && (isDeckBag(existing.Props) || isDeckBag(req.Props)) {
		if ae := s.deckWriteGate(ctx, *req.Body); ae != nil {
			return models.Page{}, ae
		}
	}
	// Lint-gate agent rewrites of a sheet body (see createPageCore). Verbatim body,
	// so *req.Body is exactly what would persist. Sheet-ness: explicit props wins,
	// else the stored page.
	if agentWrite && req.Body != nil && (isSheetBag(existing.Props) || isSheetBag(req.Props)) {
		if ae := s.sheetWriteGate(*req.Body); ae != nil {
			return models.Page{}, ae
		}
	}
	p, ae := applyUpdateTx(ctx, tx, id, req)
	if ae != nil {
		return models.Page{}, ae
	}
	if err := tx.Commit(); err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "commit failed"}
	}
	// Snapshot-on-save (revision + RAG reindex), and — for agent/out-of-band
	// writes — reset the Yjs overlay so live editors re-seed (DB-wins). Shared
	// with the file-sync path via afterPageWrite. The revision source carries the
	// agent-vs-manual tag the "Changes by your AI" feed keys on.
	editSource := "manual"
	if agentWrite {
		editSource = "agent"
	}
	s.afterPageWrite(ctx, existing, p, req.Body != nil, agentWrite, u.ID, editSource)
	// Editing is an "I care about this page" signal — auto-follow it (autowatch).
	// This is the interactive edit path (REST/MCP); sync edits go through
	// applyUpdateTx directly and never reach here, so a vault sync won't subscribe.
	s.autoFollow(ctx, u.ID, id)
	// Notifications fire only on the interactive REST/MCP edit path (not file
	// sync): mention anyone newly @-mentioned, and notify followers of the page /
	// its space. Same body/title-changed gate afterPageWrite uses internally.
	if p.Body != existing.Body || p.Title != existing.Title {
		s.notifyPageMentions(ctx, u, id, existing.SpaceID, p.Title, p.Body)
		s.notifyPageUpdate(ctx, u, id, existing.SpaceID, p.Title, existing.Body, p.Body)
	}
	return p, nil
}

func (s *Server) DeletePage(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	if ae := s.deletePageCore(r.Context(), u, k, id); ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deletePageCore is the transport-agnostic core behind DELETE /api/pages/{id}
// and the MCP delete_page tool: editor+ gated; caches the live title onto
// incoming backlinks before deleting, and clears the page's outgoing links.
func (s *Server) deletePageCore(ctx context.Context, u *auth.User, k *auth.APIKey, id int64) *apiErr {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "begin tx failed"}
	}
	defer tx.Rollback()

	existing, err := selectPageByIDTx(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return &apiErr{http.StatusForbidden, "forbidden", "not a member"}
	}
	if err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "lookup page failed"}
	}
	if ae := apiKeySpaceScopeErr(k, existing.SpaceID); ae != nil {
		return ae
	}
	role, err := spaceRoleTx(ctx, tx, u.ID, existing.SpaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return &apiErr{http.StatusForbidden, "forbidden", "not a member"}
	}
	if err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "lookup membership failed"}
	}
	if !canEdit(role) {
		return &apiErr{http.StatusForbidden, "forbidden", "editor or owner role required"}
	}

	// Cache the live title onto any incoming page_links rows so backlinks
	// from other pages still render with a usable label after deletion.
	if _, err := tx.ExecContext(ctx,
		`UPDATE page_links
		   SET last_known_title = COALESCE((SELECT title FROM pages WHERE id = $1), last_known_title)
		 WHERE target_id = $2`, id, id); err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "cache page_links titles failed"}
	}

	// Soft-delete: stamp deleted_at on the page AND its whole live subtree
	// (the old hard DELETE relied on ON DELETE CASCADE to take the descendants;
	// soft-delete must walk them itself). Trashed rows are then invisible to
	// every read (all filter deleted_at IS NULL) and recoverable — a sync glitch
	// can't destroy content, and a re-synced file can resurrect by id (sync §6).
	// subtreeCTE re-walks parent_id (unaffected by deleted_at), so it resolves
	// the same set before and after the stamp; reused for the cleanups below.
	const subtreeCTE = `
		WITH RECURSIVE subtree(id) AS (
			SELECT id FROM pages WHERE id = $1
			UNION ALL
			SELECT p.id FROM pages p JOIN subtree s ON p.parent_id = s.id
		)`
	rows, err := tx.QueryContext(ctx, subtreeCTE+`
		UPDATE pages SET deleted_at = tela_now()
		 WHERE id IN (SELECT id FROM subtree) AND deleted_at IS NULL
		 RETURNING id`, id)
	if err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "delete page failed"}
	}
	var deletedIDs []int64
	for rows.Next() {
		var did int64
		if err := rows.Scan(&did); err != nil {
			rows.Close()
			return &apiErr{http.StatusInternalServerError, "internal", "scan deleted id failed"}
		}
		deletedIDs = append(deletedIDs, did)
	}
	rerr := rows.Err()
	rows.Close() // must close before the next Exec on this tx (single-conn cursor)
	if rerr != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "delete page: read ids failed"}
	}
	if len(deletedIDs) == 0 {
		return &apiErr{http.StatusNotFound, "not_found", "page not found"}
	}

	// Hard-remove the subtree's dependents that must NOT survive a delete — the
	// behaviour the old ON DELETE CASCADE gave us, now explicit:
	//   - page_links: trashed pages stop contributing backlinks (resurrect
	//     rebuilds them from the body via syncPageLinks).
	//   - share_links: a deleted page must not stay publicly reachable.
	//   - page_diagrams: derived render blobs served from a public path.
	for _, stmt := range []string{
		`DELETE FROM page_links WHERE source_id IN (SELECT id FROM subtree)`,
		`DELETE FROM share_links WHERE page_id IN (SELECT id FROM subtree)`,
		`DELETE FROM page_diagrams WHERE page_id IN (SELECT id FROM subtree)`,
	} {
		if _, err := tx.ExecContext(ctx, subtreeCTE+stmt, id); err != nil {
			return &apiErr{http.StatusInternalServerError, "internal", "delete page dependents failed"}
		}
	}

	// Attachments parented to the trashed subtree: SOFT-delete the file rows
	// (recoverable, same promise as the pages — a re-sync resurrects + re-indexes
	// them) but HARD-clear their disposable RAG index (file_chunks) and summary
	// bookkeeping (file_summaries). Without this a deleted page's files stay live
	// and keep surfacing in research / Ask, citing a now-deleted page, and
	// the summarize stale-sweep keeps re-processing them. Mirrors the per-file
	// delete path (soft-delete + clear chunks) across the whole subtree at once.
	const filesUnderSubtree = ` (SELECT id FROM space_files WHERE parent_page_id IN (SELECT id FROM subtree))`
	for _, stmt := range []string{
		`DELETE FROM file_chunks WHERE space_file_id IN` + filesUnderSubtree,
		`DELETE FROM file_summaries WHERE space_file_id IN` + filesUnderSubtree,
		`UPDATE space_files SET deleted_at = tela_now()
		   WHERE parent_page_id IN (SELECT id FROM subtree) AND deleted_at IS NULL`,
	} {
		if _, err := tx.ExecContext(ctx, subtreeCTE+stmt, id); err != nil {
			return &apiErr{http.StatusInternalServerError, "internal", "delete page attachments failed"}
		}
	}

	// Feed every soft-deleted page (whole subtree) so a polling client learns of
	// the deletions, not just the root (sync §4). Same space for the whole subtree.
	for _, did := range deletedIDs {
		if err := appendChangeLog(ctx, tx, existing.SpaceID, did, changeDeleted); err != nil {
			return &apiErr{http.StatusInternalServerError, "internal", "append change_log failed"}
		}
	}
	// Subscriptions + notifications are polymorphic (no FK on subject_id), so a
	// page delete must clear its own — else orphaned follows / dead inbox rows
	// pointing at a gone page survive. (favorites cascades via its FK.)
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM subscriptions WHERE subject_kind = 'page' AND subject_id = $1`, id); err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "delete page subscriptions failed"}
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM notifications WHERE subject_kind = 'page' AND subject_id = $1`, id); err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "delete page notifications failed"}
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM notification_email_throttle WHERE subject_id = $1`, id); err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "delete page email throttle failed"}
	}

	if err := tx.Commit(); err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "commit failed"}
	}
	return nil
}

// pageMoveParams is the normalized move request shared by the REST MovePage
// handler and the MCP move_page tool. Each "Set" flag distinguishes "field
// omitted" from "field provided" so parent_id can be set to null (detach to
// root) distinctly from "leave parent unchanged".
type pageMoveParams struct {
	SpaceIDSet bool
	NewSpaceID int64

	ParentIDSet    bool
	ParentIDIsNull bool
	NewParentID    int64

	PositionSet bool
	NewPosition int64
}

func (s *Server) MovePage(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}

	var rawMap map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&rawMap); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}

	var mv pageMoveParams
	if v, ok := rawMap["space_id"]; ok {
		mv.SpaceIDSet = true
		if err := json.Unmarshal(v, &mv.NewSpaceID); err != nil || mv.NewSpaceID <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_space_id", "space_id must be a positive integer")
			return
		}
	}
	if v, ok := rawMap["parent_id"]; ok {
		mv.ParentIDSet = true
		if string(v) == "null" {
			mv.ParentIDIsNull = true
		} else if err := json.Unmarshal(v, &mv.NewParentID); err != nil || mv.NewParentID <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_parent_id", "parent_id must be a positive integer or null")
			return
		}
	}
	if v, ok := rawMap["position"]; ok {
		mv.PositionSet = true
		if err := json.Unmarshal(v, &mv.NewPosition); err != nil || mv.NewPosition < 0 {
			writeError(w, http.StatusBadRequest, "invalid_position", "position must be a non-negative integer")
			return
		}
	}

	k, _ := auth.APIKeyFromContext(r.Context())
	moved, ae := s.movePageCore(r.Context(), u, k, id, mv)
	if ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"page": moved})
}

// movePageCore is the transport-agnostic core behind POST /api/pages/{id}/move
// and the MCP move_page tool: reparent / reorder / relocate a page (and its
// subtree) under editor+ membership in both the source and target space, with
// cycle detection and sibling renumbering.
func (s *Server) movePageCore(ctx context.Context, u *auth.User, k *auth.APIKey, id int64, mv pageMoveParams) (models.Page, *apiErr) {
	if !mv.SpaceIDSet && !mv.ParentIDSet && !mv.PositionSet {
		return models.Page{}, &apiErr{http.StatusBadRequest, "no_fields", "at least one of space_id, parent_id, position must be provided"}
	}
	// Guard here (not just in the REST parser) so the MCP path can't reach the
	// slice math below with a negative position and panic (newSiblingIDs[:insertAt]).
	if mv.PositionSet && mv.NewPosition < 0 {
		return models.Page{}, &apiErr{http.StatusBadRequest, "invalid_position", "position must be a non-negative integer"}
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "begin tx failed"}
	}
	defer tx.Rollback()

	page, err := selectPageByIDTx(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return models.Page{}, &apiErr{http.StatusForbidden, "forbidden", "not a member"}
	}
	if err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "lookup page failed"}
	}
	if ae := s.requireEditTx(ctx, tx, u, k, page.SpaceID); ae != nil {
		return models.Page{}, ae
	}

	moved, ae := s.applyMoveTx(ctx, tx, u, k, page, mv)
	if ae != nil {
		return models.Page{}, ae
	}
	if err := tx.Commit(); err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "commit failed"}
	}
	return moved, nil
}

// applyMoveTx reparents / relocates / reorders page within an open tx and
// returns the re-fetched page. It does the move-specific authorization (target
// space membership + edit role when crossing spaces), parent + cycle validation,
// the move write, descendant space-id propagation, and sibling renumbering on
// both ends. Assumes the caller already authorized edit on the SOURCE space
// (requireEditTx) and owns the tx lifecycle (no commit here). Shared by
// movePageCore and the sync ingress so a move is one mechanism.
func (s *Server) applyMoveTx(ctx context.Context, tx *sql.Tx, u *auth.User, k *auth.APIKey, page models.Page, mv pageMoveParams) (models.Page, *apiErr) {
	id := page.ID
	targetSpaceID := page.SpaceID
	if mv.SpaceIDSet {
		targetSpaceID = mv.NewSpaceID
		if err := verifySpaceExistsTx(ctx, tx, targetSpaceID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return models.Page{}, &apiErr{http.StatusBadRequest, "space_not_found", "target space does not exist"}
			}
			return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "lookup target space failed"}
		}
		if targetSpaceID != page.SpaceID {
			if ae := apiKeySpaceScopeErr(k, targetSpaceID); ae != nil {
				return models.Page{}, ae
			}
			targetRole, err := spaceRoleTx(ctx, tx, u.ID, targetSpaceID)
			if errors.Is(err, sql.ErrNoRows) {
				return models.Page{}, &apiErr{http.StatusForbidden, "forbidden", "not a member of target space"}
			}
			if err != nil {
				return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "lookup target membership failed"}
			}
			if !canEdit(targetRole) {
				return models.Page{}, &apiErr{http.StatusForbidden, "forbidden", "editor or owner role required in target space"}
			}
			// Quota: the page and all its descendants become new pages in the
			// target space, so gate on the target owner's per-space page limit.
			var subtreeN int64
			if err := tx.QueryRowContext(ctx, `
				WITH RECURSIVE sub AS (
				  SELECT id FROM pages WHERE id = $1 AND deleted_at IS NULL
				  UNION ALL
				  SELECT p.id FROM pages p JOIN sub ON p.parent_id = sub.id WHERE p.deleted_at IS NULL
				) SELECT count(*) FROM sub`, id).Scan(&subtreeN); err != nil {
				return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "count moved subtree failed"}
			}
			if ae := s.checkPageQuotaN(ctx, targetSpaceID, subtreeN); ae != nil {
				return models.Page{}, ae
			}
		}
	}

	var targetParentID *int64
	if mv.ParentIDSet {
		if !mv.ParentIDIsNull {
			parent := mv.NewParentID
			targetParentID = &parent
		}
	} else {
		targetParentID = page.ParentID
	}

	if targetParentID != nil {
		var parentSpaceID int64
		err := tx.QueryRowContext(ctx,
			`SELECT space_id FROM pages WHERE id = $1 AND deleted_at IS NULL`, *targetParentID).Scan(&parentSpaceID)
		if errors.Is(err, sql.ErrNoRows) {
			return models.Page{}, &apiErr{http.StatusBadRequest, "parent_not_found", "parent page does not exist"}
		}
		if err != nil {
			return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "lookup parent failed"}
		}
		if parentSpaceID != targetSpaceID {
			return models.Page{}, &apiErr{http.StatusBadRequest, "parent_space_mismatch", "parent page is in a different space"}
		}
		if cyclic, err := wouldCreateCycle(ctx, tx, id, *targetParentID); err != nil {
			return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "cycle check failed"}
		} else if cyclic {
			return models.Page{}, &apiErr{http.StatusBadRequest, "cycle", "move would create a cycle"}
		}
	}

	sameGroup := page.SpaceID == targetSpaceID && parentIDPtrEqual(page.ParentID, targetParentID)

	newSiblingIDs, err := siblingIDsExcluding(ctx, tx, targetSpaceID, targetParentID, id)
	if err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "list new siblings failed"}
	}

	insertAt := int64(len(newSiblingIDs))
	if mv.PositionSet && mv.NewPosition < insertAt {
		insertAt = mv.NewPosition
	}

	finalList := make([]int64, 0, len(newSiblingIDs)+1)
	finalList = append(finalList, newSiblingIDs[:insertAt]...)
	finalList = append(finalList, id)
	finalList = append(finalList, newSiblingIDs[insertAt:]...)

	if _, err := tx.ExecContext(ctx,
		`UPDATE pages SET space_id = $1, parent_id = $2, updated_at = tela_now() WHERE id = $3`,
		targetSpaceID, nullableInt64(targetParentID), id); err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "move page failed"}
	}

	if page.SpaceID != targetSpaceID {
		if err := updateDescendantsSpaceID(ctx, tx, id, targetSpaceID); err != nil {
			return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "propagate space_id to descendants failed"}
		}
		// Files (uploads + rclone-synced blobs) live in space_files keyed by
		// space, so carry the whole moved subtree's files into the target space
		// too — otherwise they strand in the old space: counted against the wrong
		// quota, surfaced in the wrong /dav tree, and lost if the old space is
		// later deleted (ON DELETE CASCADE). Body embeds keep resolving because
		// ServeSpaceFile is content-addressed (serves by hash regardless of the
		// space in the URL), so no body rewrite is needed. No deleted_at filter:
		// trashed pages/files ride along so a later resurrect lands in the new
		// space, mirroring updateDescendantsSpaceID.
		if _, err := tx.ExecContext(ctx, `
			WITH RECURSIVE sub AS (
			  SELECT id FROM pages WHERE id = $1
			  UNION ALL
			  SELECT p.id FROM pages p JOIN sub ON p.parent_id = sub.id
			)
			UPDATE space_files SET space_id = $2, updated_at = tela_now()
			 WHERE parent_page_id IN (SELECT id FROM sub)`, id, targetSpaceID); err != nil {
			return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "migrate subtree files failed"}
		}
	}

	for i, sid := range finalList {
		if _, err := tx.ExecContext(ctx,
			`UPDATE pages SET position = $1 WHERE id = $2`, int64(i), sid); err != nil {
			return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "renumber new siblings failed"}
		}
	}

	if !sameGroup {
		oldSiblingIDs, err := siblingIDsExcluding(ctx, tx, page.SpaceID, page.ParentID, id)
		if err != nil {
			return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "list old siblings failed"}
		}
		for i, sid := range oldSiblingIDs {
			if _, err := tx.ExecContext(ctx,
				`UPDATE pages SET position = $1 WHERE id = $2`, int64(i), sid); err != nil {
				return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "renumber old siblings failed"}
			}
		}
	}

	moved, err := selectPageByIDTx(ctx, tx, id)
	if err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "fetch moved page failed"}
	}
	if err := appendChangeLog(ctx, tx, moved.SpaceID, moved.ID, changeMoved); err != nil {
		return models.Page{}, &apiErr{http.StatusInternalServerError, "internal", "append change_log failed"}
	}
	return moved, nil
}

func buildPageTree(ctx context.Context, db *sql.DB, spaceID int64) ([]*pageNode, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, space_id, parent_id, title, body, position, props, created_at, updated_at, filename
		 FROM pages WHERE space_id = $1 AND deleted_at IS NULL
		 ORDER BY position ASC, id ASC`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	nodes := map[int64]*pageNode{}
	all := []*pageNode{}
	for rows.Next() {
		p, err := scanPageFromRows(rows)
		if err != nil {
			return nil, err
		}
		n := &pageNode{Page: p, Children: []*pageNode{}}
		nodes[p.ID] = n
		all = append(all, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Resolve exposure off the already-loaded nodes (build the parent map in
	// memory; only the active-share lookup hits the DB).
	parentMap := make(map[int64]*int64, len(all))
	for _, n := range all {
		parentMap[n.ID] = n.ParentID
	}
	shares, err := loadActiveShareFacts(ctx, db, spaceID)
	if err != nil {
		return nil, err
	}
	exposures := resolveExposures(parentMap, shares)
	for _, n := range all {
		e := exposures[n.ID]
		n.Exposure = &e
	}

	roots := []*pageNode{}
	for _, n := range all {
		if n.ParentID == nil {
			roots = append(roots, n)
			continue
		}
		if parent, ok := nodes[*n.ParentID]; ok {
			parent.Children = append(parent.Children, n)
		}
	}
	return roots, nil
}

// selectPageByID / selectPageByIDTx fetch a LIVE page (soft-deleted rows are
// invisible — a trashed id reads as sql.ErrNoRows, i.e. not-found / 403). Use
// selectPageByIDIncludingDeleted for the resurrect path.
func selectPageByID(ctx context.Context, db *sql.DB, id int64) (models.Page, error) {
	row := db.QueryRowContext(ctx,
		`SELECT id, space_id, parent_id, title, body, position, props, created_at, updated_at, filename
		 FROM pages WHERE id = $1 AND deleted_at IS NULL`, id)
	return scanPageFromRow(row)
}

func selectPageByIDTx(ctx context.Context, tx *sql.Tx, id int64) (models.Page, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT id, space_id, parent_id, title, body, position, props, created_at, updated_at, filename
		 FROM pages WHERE id = $1 AND deleted_at IS NULL`, id)
	return scanPageFromRow(row)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPageFromRow(row *sql.Row) (models.Page, error) {
	return scanPageInto(row)
}

func scanPageFromRows(rows *sql.Rows) (models.Page, error) {
	return scanPageInto(rows)
}

func scanPageInto(r rowScanner) (models.Page, error) {
	var p models.Page
	var parentID sql.NullInt64
	var propsRaw []byte
	var filename sql.NullString
	if err := r.Scan(&p.ID, &p.SpaceID, &parentID, &p.Title, &p.Body, &p.Position, &propsRaw, &p.CreatedAt, &p.UpdatedAt, &filename); err != nil {
		return p, err
	}
	if parentID.Valid {
		v := parentID.Int64
		p.ParentID = &v
	}
	if filename.Valid {
		p.Filename = &filename.String
	}
	p.Props = map[string]any{}
	if len(propsRaw) > 0 {
		if err := json.Unmarshal(propsRaw, &p.Props); err != nil {
			return p, fmt.Errorf("scan page props: %w", err)
		}
	}
	return p, nil
}

func verifySpaceExists(ctx context.Context, db *sql.DB, id int64) error {
	var x int
	return db.QueryRowContext(ctx, `SELECT 1 FROM spaces WHERE id = $1`, id).Scan(&x)
}

func verifySpaceExistsTx(ctx context.Context, tx *sql.Tx, id int64) error {
	var x int
	return tx.QueryRowContext(ctx, `SELECT 1 FROM spaces WHERE id = $1`, id).Scan(&x)
}

func siblingIDsExcluding(ctx context.Context, tx *sql.Tx, spaceID int64, parentID *int64, excludeID int64) ([]int64, error) {
	var rows *sql.Rows
	var err error
	if parentID == nil {
		rows, err = tx.QueryContext(ctx,
			`SELECT id FROM pages WHERE space_id = $1 AND parent_id IS NULL AND id != $2 AND deleted_at IS NULL
			 ORDER BY position ASC, id ASC`, spaceID, excludeID)
	} else {
		rows, err = tx.QueryContext(ctx,
			`SELECT id FROM pages WHERE space_id = $1 AND parent_id = $2 AND id != $3 AND deleted_at IS NULL
			 ORDER BY position ASC, id ASC`, spaceID, *parentID, excludeID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := []int64{}
	for rows.Next() {
		var sid int64
		if err := rows.Scan(&sid); err != nil {
			return nil, err
		}
		ids = append(ids, sid)
	}
	return ids, rows.Err()
}

// wouldCreateCycle returns true if making movingID a child of newParentID
// would put movingID in its own ancestor chain.
func wouldCreateCycle(ctx context.Context, tx *sql.Tx, movingID, newParentID int64) (bool, error) {
	cursor := newParentID
	for {
		if cursor == movingID {
			return true, nil
		}
		var pid sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT parent_id FROM pages WHERE id = $1 AND deleted_at IS NULL`, cursor).Scan(&pid)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !pid.Valid {
			return false, nil
		}
		cursor = pid.Int64
	}
}

// updateDescendantsSpaceID updates space_id for all descendants of rootID
// (rootID itself is NOT updated — caller is expected to have already done so).
func updateDescendantsSpaceID(ctx context.Context, tx *sql.Tx, rootID, newSpaceID int64) error {
	frontier := []int64{rootID}
	for len(frontier) > 0 {
		placeholders := make([]string, len(frontier))
		args := make([]any, len(frontier))
		for i, fid := range frontier {
			placeholders[i] = "$" + strconv.Itoa(i+1)
			args[i] = fid
		}
		// No deleted_at filter on purpose: a trashed descendant must still ride
		// the space move so a later resurrect lands it in the new space, not the old.
		q := fmt.Sprintf(`SELECT id FROM pages WHERE parent_id IN (%s)`, strings.Join(placeholders, ","))
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		next := []int64{}
		for rows.Next() {
			var cid int64
			if err := rows.Scan(&cid); err != nil {
				rows.Close()
				return err
			}
			next = append(next, cid)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(next) == 0 {
			return nil
		}
		updPlaceholders := make([]string, len(next))
		updArgs := make([]any, 0, len(next)+1)
		updArgs = append(updArgs, newSpaceID)
		for i, nid := range next {
			updPlaceholders[i] = "$" + strconv.Itoa(i+2)
			updArgs = append(updArgs, nid)
		}
		upd := fmt.Sprintf(`UPDATE pages SET space_id = $1 WHERE id IN (%s)`, strings.Join(updPlaceholders, ","))
		if _, err := tx.ExecContext(ctx, upd, updArgs...); err != nil {
			return err
		}
		frontier = next
	}
	return nil
}

func parentIDPtrEqual(a, b *int64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func nullableInt64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// nullableFilename binds the sync filename stamp: NULL for an unset/empty value
// (the page then falls back to slugify(title) for its /dav/ name).
func nullableFilename(p *string) any {
	if p == nil || *p == "" {
		return nil
	}
	return *p
}

// wikiLinkRE matches the canonical wikilink URL form serialised by the
// Milkdown mention node. The URL is the source of truth — we don't parse
// the surrounding node syntax.
var wikiLinkRE = regexp.MustCompile(`tela://page/([0-9]+)`)

// parseWikiLinks extracts unique target page ids referenced by body via
// `tela://page/{N}` URLs. Order of returned ids is not guaranteed.
func parseWikiLinks(body string) []int64 {
	matches := wikiLinkRE.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(matches))
	ids := make([]int64, 0, len(matches))
	for _, m := range matches {
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || n <= 0 {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		ids = append(ids, n)
	}
	return ids
}

// wikiBracketRE matches Obsidian-style `[[Name]]` wikilinks (Name has no nested
// brackets). `[[Name|alias]]` and `[[Name#heading]]` are supported — the alias /
// heading suffix is trimmed before resolution.
var wikiBracketRE = regexp.MustCompile(`\[\[([^\[\]]+?)\]\]`)

// wikiRef is one `[[Name]]` occurrence: the name as written (for messages that
// quote the author's own text back), its resolution slug, and the 1-based line
// it sits on.
type wikiRef struct {
	Raw  string
	Slug string
	Line int
}

// parseWikiTitleRefs extracts every `[[Name]]` occurrence from body, reducing
// each to a page slug so `[[Route Analyze]]`, `[[route-analyze]]` and
// `[[route-analyze|alias]]` all normalise to "route-analyze". Occurrences are
// returned in source order WITH duplicates — callers that only want the link
// targets dedupe (parseWikiTitleSlugs), callers that report to a human need
// each site.
func parseWikiTitleRefs(body string) []wikiRef {
	idx := wikiBracketRE.FindAllStringSubmatchIndex(body, -1)
	if len(idx) == 0 {
		return nil
	}
	out := make([]wikiRef, 0, len(idx))
	line, scanned := 1, 0
	for _, m := range idx {
		line += strings.Count(body[scanned:m[0]], "\n")
		scanned = m[0]
		name := body[m[2]:m[3]]
		if i := strings.IndexAny(name, "|#"); i >= 0 {
			name = name[:i]
		}
		s := pageSlug(name)
		if s == "" {
			continue
		}
		out = append(out, wikiRef{Raw: strings.TrimSpace(name), Slug: s, Line: line})
	}
	return out
}

// parseWikiTitleSlugs returns the unique, non-empty slugs referenced by body's
// `[[Name]]` wikilinks. The canonical `tela://page/{id}` links are parsed
// separately by parseWikiLinks.
func parseWikiTitleSlugs(body string) []string {
	refs := parseWikiTitleRefs(body)
	seen := make(map[string]struct{}, len(refs))
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		if _, ok := seen[r.Slug]; ok {
			continue
		}
		seen[r.Slug] = struct{}{}
		out = append(out, r.Slug)
	}
	return out
}

// spaceTitleSlugIndex maps every live page title in sourceID's space to its id,
// slug-normalised (lowest id wins on a title clash). Scoped to the space so a
// name can never resolve across a membership boundary. Shared by link syncing
// and by the lint, so "what does [[Name]] resolve to" has exactly one answer.
func spaceTitleSlugIndex(ctx context.Context, q queryer, sourceID int64) (map[string]int64, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, title FROM pages
		WHERE space_id = (SELECT space_id FROM pages WHERE id = $1) AND deleted_at IS NULL
		ORDER BY id ASC`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("load space pages for wikilink resolution: %w", err)
	}
	defer rows.Close()
	bySlug := make(map[string]int64)
	for rows.Next() {
		var id int64
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			return nil, fmt.Errorf("scan page for wikilink resolution: %w", err)
		}
		if s := pageSlug(title); s != "" {
			if _, ok := bySlug[s]; !ok { // ORDER BY id ASC → lowest id wins
				bySlug[s] = id
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pages for wikilink resolution: %w", err)
	}
	return bySlug, nil
}

// resolveWikiTitleSlugs maps each `[[Name]]` slug to a target page id within
// sourceID's space. Names that match no page are dropped (nothing to link).
func resolveWikiTitleSlugs(ctx context.Context, tx *sql.Tx, sourceID int64, slugs []string) ([]int64, error) {
	bySlug, err := spaceTitleSlugIndex(ctx, tx, sourceID)
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(slugs))
	for _, s := range slugs {
		if id, ok := bySlug[s]; ok {
			out = append(out, id)
		}
	}
	return out, nil
}

// syncPageLinks rebuilds the outgoing page_links rows for sourceID from
// body: deletes existing rows, then inserts one row per unique wikilink
// target. Targets come from canonical `tela://page/{id}` links (parseWikiLinks)
// plus Obsidian-style `[[Name]]` links resolved by title within the same space.
// last_known_title is the live target title, or an empty string when the target
// does not exist — that's how a freshly broken link is recorded.
func syncPageLinks(ctx context.Context, tx *sql.Tx, sourceID int64, body string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM page_links WHERE source_id = $1`, sourceID); err != nil {
		return fmt.Errorf("delete outgoing page_links: %w", err)
	}
	targets := parseWikiLinks(body)
	if slugs := parseWikiTitleSlugs(body); len(slugs) > 0 {
		resolved, err := resolveWikiTitleSlugs(ctx, tx, sourceID, slugs)
		if err != nil {
			return err
		}
		seen := make(map[int64]struct{}, len(targets))
		for _, id := range targets {
			seen[id] = struct{}{}
		}
		for _, id := range resolved {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				targets = append(targets, id)
			}
		}
	}
	if len(targets) == 0 {
		return nil
	}
	placeholders := make([]string, 0, len(targets))
	args := make([]any, 0, len(targets)*3)
	for _, tid := range targets {
		if tid == sourceID {
			continue
		}
		var title sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT title FROM pages WHERE id = $1 AND deleted_at IS NULL`, tid).Scan(&title)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("lookup target title: %w", err)
		}
		n := len(args)
		placeholders = append(placeholders, fmt.Sprintf("($%d, $%d, $%d)", n+1, n+2, n+3))
		args = append(args, sourceID, tid, title.String)
	}
	if len(placeholders) == 0 {
		return nil
	}
	stmt := `INSERT INTO page_links(source_id, target_id, last_known_title) VALUES ` + strings.Join(placeholders, ", ")
	if _, err := tx.ExecContext(ctx, stmt, args...); err != nil {
		return fmt.Errorf("insert page_links: %w", err)
	}
	return nil
}
