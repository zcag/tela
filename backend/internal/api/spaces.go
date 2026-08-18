package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/zcag/tela/backend/internal/auth"
	"github.com/zcag/tela/backend/internal/models"
)

const (
	maxSpaceNameLen = 200
	maxSpaceSlugLen = 100

	// pgUniqueViolation — Postgres SQLSTATE for a UNIQUE / PRIMARY KEY
	// violation. 23505 covers both standalone unique indexes and composite-PK
	// duplicates (e.g. space_members), which on SQLite were two distinct codes.
	pgUniqueViolation = "23505"
)

var (
	slugValidRe     = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	slugNormalizeRe = regexp.MustCompile(`[^a-z0-9]+`)
)

// spacePrincipal is one org/group a space is shared with, for the sidebar
// access summary.
type spacePrincipal struct {
	Kind string `json:"kind"` // "org" | "group"
	Name string `json:"name"`
}

// spaceOwnerOrg is the org that *owns* a space (spaces.org_id), distinct from
// the orgs it's merely shared with. nil for a personally-owned space. Lets the
// UI say "Owned by <Org>" unambiguously — an owning org also appears in
// Principals (auto-granted editor on transfer), so the two can't be told apart
// from principals alone.
type spaceOwnerOrg struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// spaceListItem is the sidebar row shape: a space plus a compact access summary
// so the row can show who/what can reach it at a glance. MemberCount is the
// effective distinct-user count (direct ∪ via org/group); IsPersonal flags the
// auto-provisioned personal home; Principals lists the orgs/groups it's shared
// with.
type spaceListItem struct {
	models.Space
	MemberCount int              `json:"member_count"`
	IsPersonal  bool             `json:"is_personal"`
	Principals  []spacePrincipal `json:"principals"`
	// OwnerOrg is set when the space is owned by an org (spaces.org_id); nil for
	// a personally-owned space. Surfaced as the "Owned by <Org>" indicator.
	OwnerOrg *spaceOwnerOrg `json:"owner_org,omitempty"`
}

type spaceCreateRequest struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
	// OrgID, when set, makes the new space owned by that org (the caller must be a
	// member): the org's plan governs its quotas and org members get editor
	// access. Omit/null for a personal space owned by the caller.
	OrgID *int64 `json:"org_id"`
}

type spaceUpdateRequest struct {
	Name *string `json:"name"`
	Slug *string `json:"slug"`
	// Visibility flips the space between 'private' and 'public'. Owner-only
	// (stricter than name/slug, which is editor+) — publishing a whole space is
	// an owner decision.
	Visibility *string `json:"visibility"`
	// Description is the public blog standfirst. Editor+ (curation, not a publish
	// decision). Empty string clears it.
	Description *string `json:"description"`
}

const maxSpaceDescriptionLen = 280

func (s *Server) ListSpaces(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	spaces, ae := s.listSpacesCore(r.Context(), u)
	if ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"spaces": spaces})
}

// listSpacesCore is the transport-agnostic core behind GET /api/spaces and the
// MCP `list_spaces` tool: every space the user can reach, with the sidebar
// access summary. Returns *apiErr instead of writing to a ResponseWriter so the
// HTTP route and the MCP tool can share one implementation.
func (s *Server) listSpacesCore(ctx context.Context, u *auth.User) ([]spaceListItem, *apiErr) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT s.id, s.name, s.slug, s.visibility, s.description, s.created_at, s.updated_at,
		       (SELECT COUNT(DISTINCT user_id) FROM space_access a WHERE a.space_id = s.id) AS member_count,
		       CASE WHEN s.personal_user_id IS NOT NULL THEN 1 ELSE 0 END AS is_personal,
		       o.id, o.name
		  FROM spaces s
		  JOIN (SELECT DISTINCT space_id FROM space_access WHERE user_id = $1) sa ON sa.space_id = s.id
		  LEFT JOIN orgs o ON o.id = s.org_id
		 ORDER BY s.name ASC`, u.ID)
	if err != nil {
		return nil, &apiErr{http.StatusInternalServerError, "internal", "list spaces failed"}
	}
	defer rows.Close()

	spaces := []spaceListItem{}
	byID := map[int64]*spaceListItem{}
	for rows.Next() {
		var (
			it       spaceListItem
			personal int
			orgID    sql.NullInt64
			orgName  sql.NullString
		)
		if err := rows.Scan(&it.ID, &it.Name, &it.Slug, &it.Visibility, &it.Description, &it.CreatedAt, &it.UpdatedAt, &it.MemberCount, &personal, &orgID, &orgName); err != nil {
			return nil, &apiErr{http.StatusInternalServerError, "internal", "scan space row failed"}
		}
		it.IsPersonal = personal == 1
		it.Principals = []spacePrincipal{}
		if orgID.Valid && orgName.Valid {
			it.OwnerOrg = &spaceOwnerOrg{ID: orgID.Int64, Name: orgName.String}
		}
		spaces = append(spaces, it)
	}
	if err := rows.Err(); err != nil {
		return nil, &apiErr{http.StatusInternalServerError, "internal", "iterate spaces failed"}
	}
	for i := range spaces {
		byID[spaces[i].ID] = &spaces[i]
	}

	// Attach the org/group grants for these spaces in one query (no N+1).
	if err := attachSpacePrincipals(ctx, s.DB, byID); err != nil {
		return nil, &apiErr{http.StatusInternalServerError, "internal", "load space sharing failed"}
	}
	return spaces, nil
}

func (s *Server) CreateSpace(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	var req spaceCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}
	sp, ae := s.createSpaceCore(r.Context(), u, req.Name, req.Slug, req.OrgID)
	if ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	// Best-effort: seed a starter page so the new space isn't an empty void.
	// Never blocks creation — a failed seed is logged and the space still ships.
	// Done in the HTTP handler (not createSpaceCore) so the MCP create_space
	// tool and direct-core callers stay seed-free.
	s.seedWelcomePage(r.Context(), u, sp)
	writeJSON(w, http.StatusCreated, map[string]any{"space": sp})
}

// seedWelcomePage drops a starter "Welcome" page into a just-created space when
// seeding is enabled (see Server.seedWelcome). Best-effort: it reuses
// createPageCore (so the page is indexed and link-synced like any other), and a
// failure is logged, never surfaced — the space is already committed.
func (s *Server) seedWelcomePage(ctx context.Context, u *auth.User, sp models.Space) {
	if !s.seedWelcome {
		return
	}
	if _, ae := s.createPageCore(ctx, u, nil, pageCreateRequest{
		SpaceID: sp.ID,
		Title:   "Welcome to " + sp.Name,
		Body:    welcomePageBody(sp.Name),
	}, false); ae != nil {
		slog.Error("seed welcome page for space", "space_id", sp.ID, "err", ae.Message)
	}
}

// welcomePageBody is the starter page's markdown. Intentionally short and
// practical — a few orienting lines plus how to invite the team — so it reads as
// a helpful nudge, not filler to delete.
func welcomePageBody(spaceName string) string {
	return "This is the home of **" + spaceName + "**. A space is a tree of markdown pages your team writes and edits together.\n\n" +
		"## Getting started\n\n" +
		"- Press **Ctrl/⌘ N** to create a page, or use the **New page** button in the sidebar.\n" +
		"- Link pages with `[[Page Title]]` — backlinks and the graph build themselves.\n" +
		"- Star a page to pin it to your sidebar and home dashboard.\n" +
		"- Share a page publicly from its visibility menu when you need a link.\n\n" +
		"## Invite your team\n\n" +
		"Open a space's **members** to add teammates, or share the whole space with an organization so everyone joins at once.\n\n" +
		"Delete this page whenever you're ready — it won't be missed.\n"
}

// seedPersonalWelcomePage drops a starter page into a just-provisioned personal
// space, so the one space every account is guaranteed to land in isn't the only
// space that never gets one. EnsurePersonalSpace is a package-level func over
// *sql.DB with no page concern, so the seed hangs off the signup handlers that
// call it rather than off the provisioning itself. Best-effort and idempotent:
// it no-ops when the space already holds a page — deliberately counting
// soft-deleted rows too, so a user who deletes the starter page never sees it
// come back.
func (s *Server) seedPersonalWelcomePage(ctx context.Context, userID int64, username string, spaceID int64) {
	if !s.seedWelcome {
		return
	}
	var n int
	if err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pages WHERE space_id = $1`, spaceID).Scan(&n); err != nil || n > 0 {
		return
	}
	u := &auth.User{ID: userID, Username: username}
	if _, ae := s.createPageCore(ctx, u, nil, pageCreateRequest{
		SpaceID: spaceID,
		Title:   "Welcome to tela",
		Body:    personalWelcomeBody(),
	}, false); ae != nil {
		slog.Error("seed welcome page for personal space", "space_id", spaceID, "err", ae.Message)
	}
}

// personalWelcomeBody is the personal space's starter page: welcomePageBody's
// counterpart in the same voice, solo instead of team, naming the two things
// nobody discovers on their own (Ask and MCP). The docs link is the canonical
// public one — frontend/src/lib/docs.ts holds the same URL for the UI.
func personalWelcomeBody() string {
	return "This is your personal space — private, only you can see it. Everything in tela is a markdown page in a tree, so start anywhere and move it later.\n\n" +
		"## Three things worth two minutes\n\n" +
		"- Press **Ctrl/⌘ N** to create a page. Link pages with `[[Page Title]]` — backlinks and the graph build themselves.\n" +
		"- **Ask** answers questions across everything you've written, with citations back to the page. Try it once you have a few pages in here.\n" +
		"- Point your assistant at tela over **MCP** and Claude or ChatGPT can read and write these pages directly — [setup here](https://telawiki.com/tela/docs/211/agents-mcp).\n\n" +
		"## When it stops being just notes\n\n" +
		"Make a space for a project or a team and invite people. Publish any page to a public link. Point **Atlas** at a repo and it keeps a set of pages current on its own.\n\n" +
		"Delete this page whenever you're ready — it won't be missed.\n"
}

// createSpaceCore is the transport-agnostic core behind POST /api/spaces and the
// MCP create_space tool: validate name + (derived-or-given) slug, then insert
// the space and the creator's owner-membership row in one tx so a crash can't
// lock the creator out (M6.1 auto-own).
func (s *Server) createSpaceCore(ctx context.Context, u *auth.User, rawName, rawSlug string, ownerOrgID *int64) (models.Space, *apiErr) {
	name := strings.TrimSpace(rawName)
	if name == "" {
		return models.Space{}, &apiErr{http.StatusBadRequest, "invalid_name", "name is required"}
	}
	if len(name) > maxSpaceNameLen {
		return models.Space{}, &apiErr{http.StatusBadRequest, "invalid_name", "name exceeds 200 characters"}
	}

	slug := strings.TrimSpace(rawSlug)
	if slug == "" {
		slug = normalizeSlug(name)
		if slug == "" {
			return models.Space{}, &apiErr{http.StatusBadRequest, "invalid_name", "cannot derive a slug from the given name"}
		}
		if len(slug) > maxSpaceSlugLen {
			slug = slug[:maxSpaceSlugLen]
			slug = strings.TrimRight(slug, "-")
		}
	} else {
		if len(slug) > maxSpaceSlugLen {
			return models.Space{}, &apiErr{http.StatusBadRequest, "invalid_slug", "slug exceeds 100 characters"}
		}
		if !slugValidRe.MatchString(slug) {
			return models.Space{}, &apiErr{http.StatusBadRequest, "invalid_slug", "slug must be lowercase alphanumeric segments joined by '-'"}
		}
	}

	// Resolve the owning account (org if requested + caller is a member, else the
	// caller's personal account) and gate on its space quota before inserting.
	owner := account{Kind: accountUser, ID: u.ID}
	if ownerOrgID != nil {
		if !u.IsInstanceAdmin {
			if _, err := orgRole(ctx, s.DB, u.ID, *ownerOrgID); errors.Is(err, sql.ErrNoRows) {
				return models.Space{}, &apiErr{http.StatusForbidden, "forbidden", "not a member of this org"}
			} else if err != nil {
				return models.Space{}, &apiErr{http.StatusInternalServerError, "internal", "lookup org membership failed"}
			}
		}
		owner = account{Kind: accountOrg, ID: *ownerOrgID}
	}
	if ae := s.checkSpaceQuota(ctx, owner); ae != nil {
		return models.Space{}, ae
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return models.Space{}, &apiErr{http.StatusInternalServerError, "internal", "begin tx failed"}
	}
	defer tx.Rollback()

	var id int64
	err = tx.QueryRowContext(ctx,
		`INSERT INTO spaces(name, slug, org_id) VALUES ($1, $2, $3) RETURNING id`, name, slug, ownerOrgID).Scan(&id)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return models.Space{}, &apiErr{http.StatusConflict, "slug_conflict", "a space with that slug already exists"}
		}
		return models.Space{}, &apiErr{http.StatusInternalServerError, "internal", "create space failed"}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO space_members(space_id, user_id, role) VALUES ($1, $2, 'owner')`,
		id, u.ID); err != nil {
		return models.Space{}, &apiErr{http.StatusInternalServerError, "internal", "assign space owner failed"}
	}
	// An org-owned space is shared with the whole org as editors (owner stays the
	// human creator — the no-principal-owner trigger forbids org owners).
	if ownerOrgID != nil {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO space_grants(space_id, principal_kind, principal_id, role) VALUES ($1, 'org', $2, 'editor')`,
			id, *ownerOrgID); err != nil {
			return models.Space{}, &apiErr{http.StatusInternalServerError, "internal", "share space with org failed"}
		}
	}
	sp, err := selectSpaceByIDTx(ctx, tx, id)
	if err != nil {
		return models.Space{}, &apiErr{http.StatusInternalServerError, "internal", "fetch created space failed"}
	}
	if err := tx.Commit(); err != nil {
		return models.Space{}, &apiErr{http.StatusInternalServerError, "internal", "commit failed"}
	}
	sp.MyRole = roleOwner // creator is always seeded as the direct owner above
	return sp, nil
}

func (s *Server) GetSpace(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	sp, ae := s.getSpaceCore(r.Context(), u, k, id)
	if ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"space": sp})
}

// getSpaceCore is the transport-agnostic core behind GET /api/spaces/{id} and
// the MCP get_space tool: membership-gated fetch of one space by id. The
// caller's effective role (direct ∪ org ∪ group, already resolved by the
// membership gate) is surfaced as my_role so clients can role-gate UI without
// re-deriving it from the direct members list.
func (s *Server) getSpaceCore(ctx context.Context, u *auth.User, k *auth.APIKey, id int64) (models.Space, *apiErr) {
	role, ae := s.membershipCore(ctx, u, k, id)
	if ae != nil {
		return models.Space{}, ae
	}
	sp, err := selectSpaceByID(ctx, s.DB, id)
	if errors.Is(err, sql.ErrNoRows) {
		return models.Space{}, &apiErr{http.StatusNotFound, "not_found", "space not found"}
	}
	if err != nil {
		return models.Space{}, &apiErr{http.StatusInternalServerError, "internal", "fetch space failed"}
	}
	sp.MyRole = role
	// Canonical public path, so the share dialog hands out the pretty handle URL
	// rather than the id form (and never re-derives the handle rule client-side).
	if sp.Visibility == spaceVisibilityPublic {
		sp.PublicPath = s.spaceHandlePath(ctx, sp.ID, sp.Slug)
	}
	return sp, nil
}

// spaceOwnerHandles returns the personal-owner username for every space the user
// can access, in one query (no N+1). Org-owned spaces are simply absent from the map.
func (s *Server) spaceOwnerHandles(ctx context.Context, userID int64) map[int64]string {
	out := map[int64]string{}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT m.space_id, u.username
		   FROM space_members m JOIN users u ON u.id = m.user_id
		  WHERE m.role = 'owner'
		    AND m.space_id IN (SELECT DISTINCT space_id FROM space_access WHERE user_id = $1)`, userID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var h string
		if rows.Scan(&id, &h) == nil {
			if _, seen := out[id]; !seen {
				out[id] = h
			}
		}
	}
	return out
}

// spaceOwnerOrgName returns the owning org's name when the space is org-owned, else "".
func (s *Server) spaceOwnerOrgName(ctx context.Context, spaceID int64) string {
	var n string
	_ = s.DB.QueryRowContext(ctx,
		`SELECT o.name FROM spaces s JOIN orgs o ON o.id = s.org_id WHERE s.id = $1`, spaceID).Scan(&n)
	return n
}

// spaceOrgGrantNames returns the names of orgs granted access to the space
// (space_grants, principal_kind='org'). An org here may own or just share the
// space — resolveOwnerOrg applies the same "sole org → owner" heuristic the UI uses.
func (s *Server) spaceOrgGrantNames(ctx context.Context, spaceID int64) []string {
	var names []string
	rows, err := s.DB.QueryContext(ctx,
		`SELECT o.name FROM space_grants sg JOIN orgs o ON o.id = sg.principal_id
		  WHERE sg.space_id = $1 AND sg.principal_kind = 'org'`, spaceID)
	if err != nil {
		return names
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			names = append(names, n)
		}
	}
	return names
}

// spaceIsPersonal reports whether the space is an auto-provisioned personal home.
func (s *Server) spaceIsPersonal(ctx context.Context, spaceID int64) bool {
	var personal bool
	_ = s.DB.QueryRowContext(ctx,
		`SELECT personal_user_id IS NOT NULL FROM spaces WHERE id = $1`, spaceID).Scan(&personal)
	return personal
}

// resolveOwnerOrg mirrors the frontend's spaceOwnership() (lib/space-owner.ts):
// the explicit org_id owner wins; otherwise a non-personal space with exactly one
// org grant is treated as owned by that org. Empty → a personal/"you" space.
func resolveOwnerOrg(orgIDName string, isPersonal bool, orgGrantNames []string) string {
	if orgIDName != "" {
		return orgIDName
	}
	if !isPersonal && len(orgGrantNames) == 1 {
		return orgGrantNames[0]
	}
	return ""
}

func (s *Server) UpdateSpace(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	var req spaceUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	sp, ae := s.updateSpaceCore(r.Context(), u, k, id, req)
	if ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"space": sp})
}

// updateSpaceCore is the transport-agnostic core behind PATCH /api/spaces/{id}
// and the MCP update_space tool: editor+ gated patch of name and/or slug.
func (s *Server) updateSpaceCore(ctx context.Context, u *auth.User, k *auth.APIKey, id int64, req spaceUpdateRequest) (models.Space, *apiErr) {
	role, ae := s.membershipCore(ctx, u, k, id)
	if ae != nil {
		return models.Space{}, ae
	}
	if !canEdit(role) {
		return models.Space{}, &apiErr{http.StatusForbidden, "forbidden", "editor or owner role required"}
	}
	// Publishing a whole space (visibility) is an owner-only decision, stricter
	// than the editor+ gate that covers name/slug.
	if req.Visibility != nil && role != roleOwner {
		return models.Space{}, &apiErr{http.StatusForbidden, "forbidden", "owner role required to change visibility"}
	}
	// Cloud publishing entitlement: flipping a space PUBLIC can be gated on the
	// owning account's plan granting the `publishing` feature. This is the single
	// place the gate lives (PATCH /api/spaces and the MCP update_space tool both
	// route through updateSpaceCore). It is OPT-IN via the instance setting
	// require_publishing_entitlement (default off) so self-hosters are unaffected:
	// the cloud main instance sets it on, while a self-host comp/unlimited tier —
	// which already carries publishing=true — keeps publishing even when on. We
	// only gate the private→public transition, never un-publishing.
	if req.Visibility != nil && strings.TrimSpace(*req.Visibility) == spaceVisibilityPublic && s.publishingEntitlementRequired() {
		owner, err := spaceOwner(ctx, s.DB, id)
		if err != nil {
			return models.Space{}, &apiErr{http.StatusInternalServerError, "internal", "resolve space owner failed"}
		}
		if !s.entitled(ctx, owner, "publishing") {
			return models.Space{}, &apiErr{http.StatusPaymentRequired, "upgrade_required", "publishing public spaces requires a plan that includes publishing"}
		}
	}
	if req.Name == nil && req.Slug == nil && req.Visibility == nil && req.Description == nil {
		return models.Space{}, &apiErr{http.StatusBadRequest, "no_fields", "at least one of name, slug, visibility, description must be provided"}
	}

	sets := make([]string, 0, 3)
	args := make([]any, 0, 4)
	argN := 0

	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			return models.Space{}, &apiErr{http.StatusBadRequest, "invalid_name", "name cannot be empty"}
		}
		if len(name) > maxSpaceNameLen {
			return models.Space{}, &apiErr{http.StatusBadRequest, "invalid_name", "name exceeds 200 characters"}
		}
		argN++
		sets = append(sets, "name = $"+strconv.Itoa(argN))
		args = append(args, name)
	}
	if req.Slug != nil {
		slug := strings.TrimSpace(*req.Slug)
		if slug == "" {
			return models.Space{}, &apiErr{http.StatusBadRequest, "invalid_slug", "slug cannot be empty"}
		}
		if len(slug) > maxSpaceSlugLen {
			return models.Space{}, &apiErr{http.StatusBadRequest, "invalid_slug", "slug exceeds 100 characters"}
		}
		if !slugValidRe.MatchString(slug) {
			return models.Space{}, &apiErr{http.StatusBadRequest, "invalid_slug", "slug must be lowercase alphanumeric segments joined by '-'"}
		}
		argN++
		sets = append(sets, "slug = $"+strconv.Itoa(argN))
		args = append(args, slug)
	}
	if req.Visibility != nil {
		vis := strings.TrimSpace(*req.Visibility)
		if vis != spaceVisibilityPrivate && vis != spaceVisibilityPublic {
			return models.Space{}, &apiErr{http.StatusBadRequest, "invalid_visibility", "visibility must be 'private' or 'public'"}
		}
		argN++
		sets = append(sets, "visibility = $"+strconv.Itoa(argN))
		args = append(args, vis)
	}
	if req.Description != nil {
		desc := strings.TrimSpace(*req.Description)
		if len(desc) > maxSpaceDescriptionLen {
			return models.Space{}, &apiErr{http.StatusBadRequest, "invalid_description", "description exceeds 280 characters"}
		}
		argN++
		sets = append(sets, "description = $"+strconv.Itoa(argN))
		args = append(args, desc)
	}
	sets = append(sets, "updated_at = tela_now()")
	argN++
	args = append(args, id)

	stmt := "UPDATE spaces SET " + strings.Join(sets, ", ") + " WHERE id = $" + strconv.Itoa(argN)
	res, err := s.DB.ExecContext(ctx, stmt, args...)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return models.Space{}, &apiErr{http.StatusConflict, "slug_conflict", "a space with that slug already exists"}
		}
		return models.Space{}, &apiErr{http.StatusInternalServerError, "internal", "update space failed"}
	}
	n, err := res.RowsAffected()
	if err != nil {
		return models.Space{}, &apiErr{http.StatusInternalServerError, "internal", "update space: rows affected failed"}
	}
	if n == 0 {
		return models.Space{}, &apiErr{http.StatusNotFound, "not_found", "space not found"}
	}
	sp, err := selectSpaceByID(ctx, s.DB, id)
	if err != nil {
		return models.Space{}, &apiErr{http.StatusInternalServerError, "internal", "fetch updated space failed"}
	}
	sp.MyRole = role
	// Canonical public path, so the share dialog hands out the pretty handle URL
	// rather than the id form (and never re-derives the handle rule client-side).
	if sp.Visibility == spaceVisibilityPublic {
		sp.PublicPath = s.spaceHandlePath(ctx, sp.ID, sp.Slug)
	}
	return sp, nil
}

func (s *Server) DeleteSpace(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	k, _ := auth.APIKeyFromContext(r.Context())
	if ae := s.deleteSpaceCore(r.Context(), u, k, id); ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteSpaceCore is the transport-agnostic core behind DELETE /api/spaces/{id}
// and the MCP delete_space tool: owner-only; cascades to all pages, comments,
// revisions and share links via FKs.
func (s *Server) deleteSpaceCore(ctx context.Context, u *auth.User, k *auth.APIKey, id int64) *apiErr {
	role, ae := s.membershipCore(ctx, u, k, id)
	if ae != nil {
		return ae
	}
	if role != roleOwner {
		return &apiErr{http.StatusForbidden, "forbidden", "owner role required"}
	}
	// Clear polymorphic follows for the space + its pages before the cascade —
	// subscriptions have no FK, so the space delete won't reach them (it does
	// reach notifications, which carry space_id). Done first while the pages
	// still exist.
	if _, err := s.DB.ExecContext(ctx, `
		DELETE FROM subscriptions
		 WHERE (subject_kind = 'space' AND subject_id = $1)
		    OR (subject_kind = 'page'  AND subject_id IN (SELECT id FROM pages WHERE space_id = $1))`, id); err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "delete space subscriptions failed"}
	}
	res, err := s.DB.ExecContext(ctx, `DELETE FROM spaces WHERE id = $1`, id)
	if err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "delete space failed"}
	}
	n, err := res.RowsAffected()
	if err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "delete space: rows affected failed"}
	}
	if n == 0 {
		return &apiErr{http.StatusNotFound, "not_found", "space not found"}
	}
	return nil
}

// attachSpacePrincipals fills each space's Principals with the orgs/groups it's
// shared with, in one query over all the listed space ids.
func attachSpacePrincipals(ctx context.Context, db *sql.DB, byID map[int64]*spaceListItem) error {
	if len(byID) == 0 {
		return nil
	}
	ids := make([]any, 0, len(byID))
	ph := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
		ph = append(ph, "$"+strconv.Itoa(len(ph)+1))
	}
	rows, err := db.QueryContext(ctx, `
		SELECT sg.space_id, sg.principal_kind, COALESCE(o.name, g.name)
		  FROM space_grants sg
		  LEFT JOIN orgs o   ON sg.principal_kind = 'org'   AND o.id = sg.principal_id
		  LEFT JOIN groups g ON sg.principal_kind = 'group' AND g.id = sg.principal_id
		 WHERE sg.space_id IN (`+strings.Join(ph, ",")+`)
		 ORDER BY sg.principal_kind ASC, COALESCE(o.name, g.name) ASC`, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			spaceID int64
			kind    string
			name    sql.NullString
		)
		if err := rows.Scan(&spaceID, &kind, &name); err != nil {
			return err
		}
		if it := byID[spaceID]; it != nil && name.Valid {
			it.Principals = append(it.Principals, spacePrincipal{Kind: kind, Name: name.String})
		}
	}
	return rows.Err()
}

func selectSpaceByID(ctx context.Context, db *sql.DB, id int64) (models.Space, error) {
	var sp models.Space
	err := db.QueryRowContext(ctx,
		`SELECT id, name, slug, visibility, description, created_at, updated_at FROM spaces WHERE id = $1`, id,
	).Scan(&sp.ID, &sp.Name, &sp.Slug, &sp.Visibility, &sp.Description, &sp.CreatedAt, &sp.UpdatedAt)
	return sp, err
}

func selectSpaceByIDTx(ctx context.Context, tx *sql.Tx, id int64) (models.Space, error) {
	var sp models.Space
	err := tx.QueryRowContext(ctx,
		`SELECT id, name, slug, visibility, description, created_at, updated_at FROM spaces WHERE id = $1`, id,
	).Scan(&sp.ID, &sp.Name, &sp.Slug, &sp.Visibility, &sp.Description, &sp.CreatedAt, &sp.UpdatedAt)
	return sp, err
}

// normalizeSlug lowercases the input, replaces runs of non-alphanumeric
// characters with a single '-', and trims leading/trailing '-'.
func normalizeSlug(s string) string {
	lower := strings.ToLower(s)
	collapsed := slugNormalizeRe.ReplaceAllString(lower, "-")
	return strings.Trim(collapsed, "-")
}

func isUniqueConstraintErr(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}
