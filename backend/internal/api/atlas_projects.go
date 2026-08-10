package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/zcag/tela/backend/internal/atlas/core"
	"github.com/zcag/tela/backend/internal/auth"
)

// atlasProject is the persisted control-plane row: what to document, where it
// lands (output space + optional top-dir), on what schedule, who owns it.
// OutputSpaceID is nil until the first run materializes the space.
type atlasProject struct {
	ID                 int64
	Name               string
	OwnerKind          string
	OwnerID            int64
	OutputSpaceID      *int64
	OutputParentPageID *int64
	Cadence            string
	AutoUpdate         bool
	LastRefreshAt      string
	CreatedAt          string
}

var atlasCadences = map[string]bool{"": true, "hourly": true, "daily": true, "weekly": true, "monthly": true}

// loadProject reads a project row by id.
func (m *atlasManager) loadProject(ctx context.Context, projectID int64) (atlasProject, error) {
	return loadAtlasProject(ctx, m.s.DB, projectID)
}

func loadAtlasProject(ctx context.Context, db *sql.DB, projectID int64) (atlasProject, error) {
	var p atlasProject
	var space, parent sql.NullInt64
	var auto int
	err := db.QueryRowContext(ctx, `
		SELECT id, name, owner_kind, owner_id, output_space_id, output_parent_page_id, cadence, auto_update, last_refresh_at, created_at
		  FROM atlas_projects WHERE id = $1`, projectID).
		Scan(&p.ID, &p.Name, &p.OwnerKind, &p.OwnerID, &space, &parent, &p.Cadence, &auto, &p.LastRefreshAt, &p.CreatedAt)
	if err != nil {
		return atlasProject{}, err
	}
	if space.Valid {
		p.OutputSpaceID = &space.Int64
	}
	if parent.Valid {
		p.OutputParentPageID = &parent.Int64
	}
	p.AutoUpdate = auto != 0
	return p, nil
}

// ensureOutputSpace resolves a project's output space, CREATING it on the first
// run if it doesn't exist yet (atlas's create-if-missing) and persisting the new
// id on the project. Returns the output space id + the optional top-dir parent
// page generated subtrees nest under.
func (s *Server) ensureOutputSpace(ctx context.Context, p atlasProject) (int64, *int64, *apiErr) {
	if p.OutputSpaceID != nil {
		return *p.OutputSpaceID, p.OutputParentPageID, nil
	}
	spaceID, ae := s.createProjectOutputSpace(ctx, p.OwnerKind, p.OwnerID, p.Name)
	if ae != nil {
		return 0, nil, ae
	}
	if _, err := s.DB.ExecContext(ctx,
		`UPDATE atlas_projects SET output_space_id = $1 WHERE id = $2`, spaceID, p.ID); err != nil {
		return 0, nil, &apiErr{http.StatusInternalServerError, "internal", "persist output space failed"}
	}
	return spaceID, p.OutputParentPageID, nil
}

// createProjectOutputSpace creates a fresh space named after the project, owned
// by the project's owner: an org space (shared with the org as editors, an org
// admin seeded as the human owner) when the owner is an org, else a personal
// space owned by the owner user. Reuses createSpaceCore so quota, slug
// derivation, and org grants stay identical to a hand-made space.
func (s *Server) createProjectOutputSpace(ctx context.Context, ownerKind string, ownerID int64, name string) (int64, *apiErr) {
	var actingUser *auth.User
	var orgIDPtr *int64
	switch ownerKind {
	case accountOrg:
		// Seed an org admin (else any member) as the human owner — createSpaceCore
		// requires a real user in space_members, and the org grant makes it shared.
		var uid int64
		err := s.DB.QueryRowContext(ctx,
			`SELECT user_id FROM org_members WHERE org_id=$1 AND org_role='admin' ORDER BY user_id LIMIT 1`, ownerID).Scan(&uid)
		if err == sql.ErrNoRows {
			err = s.DB.QueryRowContext(ctx,
				`SELECT user_id FROM org_members WHERE org_id=$1 ORDER BY user_id LIMIT 1`, ownerID).Scan(&uid)
		}
		if err != nil {
			return 0, &apiErr{http.StatusInternalServerError, "internal", "org has no member to own the output space"}
		}
		actingUser = &auth.User{ID: uid}
		orgIDPtr = &ownerID
	case accountUser:
		actingUser = &auth.User{ID: ownerID}
	default:
		return 0, &apiErr{http.StatusBadRequest, "invalid_owner", "owner_kind must be 'user' or 'org'"}
	}
	// Pre-derive a unique slug so a create-on-first-run never trips the slug UNIQUE.
	slug, err := uniqueSlug(ctx, s.DB, name, "atlas")
	if err != nil {
		return 0, &apiErr{http.StatusInternalServerError, "internal", "derive output space slug failed"}
	}
	sp, ae := s.createSpaceCore(ctx, actingUser, name, slug, orgIDPtr)
	if ae != nil {
		return 0, ae
	}
	return sp.ID, nil
}

// ── DTOs ────────────────────────────────────────────────────────────────────

type atlasOwnerRef struct {
	Kind string `json:"kind"`
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type atlasSpaceRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type atlasLastRun struct {
	ID       int64    `json:"id"`
	Status   string   `json:"status"`
	MustRate *float64 `json:"must_rate,omitempty"`
}

type atlasProjectDTO struct {
	ID                 int64          `json:"id"`
	Name               string         `json:"name"`
	Owner              atlasOwnerRef  `json:"owner"`
	OutputSpace        *atlasSpaceRef `json:"output_space"`
	OutputParentPageID *int64         `json:"output_parent_page_id,omitempty"`
	Cadence            string         `json:"cadence"`
	AutoUpdate         bool           `json:"auto_update"`
	LastRefreshAt      string         `json:"last_refresh_at,omitempty"`
	NextDue            string         `json:"next_due,omitempty"`
	SourcesCount       int            `json:"sources_count"`
	StaleSources       int            `json:"stale_sources"` // generated sources now behind upstream
	LastRun            *atlasLastRun  `json:"last_run"`
	CreatedAt          string         `json:"created_at"`
	CanManage          bool           `json:"can_manage"`
	// ScheduledAllowed reports whether the owning account's plan includes scheduled
	// Atlas auto-refresh (paid/trial). When false, auto_update is a no-op server-side
	// (the scheduler skips it) — the UI shows manual-only + an upgrade affordance.
	ScheduledAllowed bool `json:"scheduled_allowed"`
}

// atlasProjectListCols is the SELECT list shared by the list + single-project
// reads, projecting each project + its owner name, output-space name, sources
// count, and latest run across all its sources.
const atlasProjectSelect = `
	SELECT p.id, p.name, p.owner_kind, p.owner_id,
	       CASE WHEN p.owner_kind = 'user' THEN (SELECT username FROM users WHERE id = p.owner_id)
	            ELSE (SELECT name FROM orgs WHERE id = p.owner_id) END,
	       p.output_space_id, sp.name,
	       p.output_parent_page_id, p.cadence, p.auto_update, p.last_refresh_at, p.created_at,
	       (SELECT count(*) FROM atlas_sources s WHERE s.project_id = p.id),
	       (SELECT count(*) FROM atlas_sources s WHERE s.project_id = p.id AND s.stale_since <> '' AND s.ref <> ''),
	       lr.id, lr.status, lr.coverage_json
	  FROM atlas_projects p
	  LEFT JOIN spaces sp ON sp.id = p.output_space_id
	  LEFT JOIN LATERAL (
	        SELECT r.id, r.status, r.coverage_json
	          FROM atlas_runs r JOIN atlas_sources s ON s.id = r.source_id
	         WHERE s.project_id = p.id ORDER BY r.id DESC LIMIT 1
	  ) lr ON true`

func scanProjectDTO(sc interface{ Scan(...any) error }) (atlasProjectDTO, error) {
	var d atlasProjectDTO
	var ownerName, spaceName sql.NullString
	var spaceID, parent, lastRunID sql.NullInt64
	var auto int
	var lastStatus, covJSON sql.NullString
	var cadence, lastRefresh string
	if err := sc.Scan(&d.ID, &d.Name, &d.Owner.Kind, &d.Owner.ID, &ownerName,
		&spaceID, &spaceName, &parent, &cadence, &auto, &lastRefresh, &d.CreatedAt,
		&d.SourcesCount, &d.StaleSources, &lastRunID, &lastStatus, &covJSON); err != nil {
		return d, err
	}
	d.Owner.Name = ownerName.String
	if spaceID.Valid {
		d.OutputSpace = &atlasSpaceRef{ID: spaceID.Int64, Name: spaceName.String}
	}
	if parent.Valid {
		d.OutputParentPageID = &parent.Int64
	}
	d.Cadence = cadence
	d.AutoUpdate = auto != 0
	d.LastRefreshAt = lastRefresh
	d.NextDue = atlasNextDueStr(d.AutoUpdate, cadence, lastRefresh)
	if lastRunID.Valid {
		lr := &atlasLastRun{ID: lastRunID.Int64, Status: lastStatus.String}
		if covJSON.Valid && covJSON.String != "" {
			var cov core.Coverage
			if json.Unmarshal([]byte(covJSON.String), &cov) == nil {
				mr := cov.MustRate()
				lr.MustRate = &mr
			}
		}
		d.LastRun = lr
	}
	return d, nil
}

// ── handlers: project CRUD ──────────────────────────────────────────────────

// listAtlasProjectsFor returns the projects a user can see (personal ∪ the
// projects of every org they belong to), each with CanManage resolved. Shared by
// the REST list handler and the atlas_list_projects MCP tool.
func (s *Server) listAtlasProjectsFor(ctx context.Context, u *auth.User) ([]atlasProjectDTO, error) {
	rows, err := s.DB.QueryContext(ctx, atlasProjectSelect+`
		 WHERE (p.owner_kind = 'user' AND p.owner_id = $1)
		    OR (p.owner_kind = 'org'  AND p.owner_id IN (SELECT org_id FROM org_members WHERE user_id = $1))
		 ORDER BY p.id`, u.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []atlasProjectDTO{}
	for rows.Next() {
		d, err := scanProjectDTO(rows)
		if err != nil {
			return nil, err
		}
		d.CanManage = s.atlasOwnerManageErr(ctx, u, d.Owner.Kind, d.Owner.ID) == nil
		d.ScheduledAllowed = s.scheduledAtlasAllowed(ctx, account{Kind: d.Owner.Kind, ID: d.Owner.ID})
		out = append(out, d)
	}
	return out, rows.Err()
}

// ListAtlasProjects lists projects the caller can see: their personal ones plus
// the projects of every org they belong to. GET /api/atlas/projects.
func (s *Server) ListAtlasProjects(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	out, err := s.listAtlasProjectsFor(r.Context(), u)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "list projects failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out})
}

type atlasProjectOutputReq struct {
	SpaceID      *int64 `json:"space_id"`
	NewSpaceName string `json:"new_space_name"`
	ParentPageID *int64 `json:"parent_page_id"`
}

type atlasProjectCreateReq struct {
	Name       string                `json:"name"`
	OwnerKind  string                `json:"owner_kind"`
	OwnerID    int64                 `json:"owner_id"`
	Output     atlasProjectOutputReq `json:"output"`
	Cadence    string                `json:"cadence"`
	AutoUpdate *bool                 `json:"auto_update"`
}

// CreateAtlasProject creates a project. The output is either an existing space
// the caller may write to, a brand-new space (created now), or deferred (created
// on the first run, named after the project). POST /api/atlas/projects.
func (s *Server) CreateAtlasProject(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	var req atlasProjectCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}
	if ae := s.atlasOwnerManageErr(r.Context(), u, req.OwnerKind, req.OwnerID); ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "invalid_name", "name is required")
		return
	}
	if !atlasCadences[req.Cadence] {
		writeError(w, http.StatusBadRequest, "invalid_cadence", "cadence must be hourly|daily|weekly|monthly or empty")
		return
	}
	autoUpdate := req.AutoUpdate == nil || *req.AutoUpdate
	cadence := req.Cadence
	if cadence == "" && autoUpdate {
		cadence = "daily"
	}
	if ae := s.checkCadenceAffordable(r.Context(), account{Kind: req.OwnerKind, ID: req.OwnerID}, cadence); ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}

	// Resolve the output space. An explicit space must be writable by the caller; a
	// new_space_name is materialized now; neither defers to the first run.
	var outputSpaceID *int64
	var parentPageID *int64
	switch {
	case req.Output.SpaceID != nil:
		if ae := s.atlasOutputSpaceWritable(r.Context(), u, *req.Output.SpaceID); ae != nil {
			writeError(w, ae.Status, ae.Code, ae.Message)
			return
		}
		outputSpaceID = req.Output.SpaceID
		if req.Output.ParentPageID != nil {
			if !s.pageInSpace(r.Context(), *req.Output.ParentPageID, *outputSpaceID) {
				writeError(w, http.StatusBadRequest, "invalid_parent", "parent_page_id must be a page in the output space")
				return
			}
			parentPageID = req.Output.ParentPageID
		}
		// Namespace the project under its own folder in the (often shared) space, so
		// the tree reads Space → [topdir] → Project → Source and multiple projects in
		// one space never collide at the root. The folder becomes the project's
		// output parent; sources publish beneath it. (A brand-new space, below, is
		// already named after the project — it IS the namespace, so no folder there.)
		folderID, ae := s.createProjectFolder(r.Context(), *outputSpaceID, parentPageID, req.Name)
		if ae != nil {
			writeError(w, ae.Status, ae.Code, ae.Message)
			return
		}
		parentPageID = &folderID
	case req.Output.NewSpaceName != "":
		id, ae := s.createProjectOutputSpace(r.Context(), req.OwnerKind, req.OwnerID, req.Output.NewSpaceName)
		if ae != nil {
			writeError(w, ae.Status, ae.Code, ae.Message)
			return
		}
		outputSpaceID = &id
	}

	var id int64
	var createdAt string
	err := s.DB.QueryRowContext(r.Context(), `
		INSERT INTO atlas_projects (name, owner_kind, owner_id, output_space_id, output_parent_page_id, cadence, auto_update)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id, created_at`,
		req.Name, req.OwnerKind, req.OwnerID, nullableInt64(outputSpaceID), nullableInt64(parentPageID), cadence, boolToInt(autoUpdate)).
		Scan(&id, &createdAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "create project failed")
		return
	}
	d, err := s.loadProjectDTO(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "load created project failed")
		return
	}
	d.CanManage = true
	d.ScheduledAllowed = s.scheduledAtlasAllowed(r.Context(), account{Kind: d.Owner.Kind, ID: d.Owner.ID})
	writeJSON(w, http.StatusCreated, map[string]any{"project": d})
}

// GetAtlasProject returns a project with its sources and recent runs.
// GET /api/atlas/projects/{id} — view-gated.
func (s *Server) GetAtlasProject(w http.ResponseWriter, r *http.Request) {
	projectID, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	if ae := s.atlasProjectViewErr(r.Context(), u, projectID); ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	d, err := s.loadProjectDTO(r.Context(), projectID)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "load project failed")
		return
	}
	d.CanManage = s.atlasOwnerManageErr(r.Context(), u, d.Owner.Kind, d.Owner.ID) == nil
	d.ScheduledAllowed = s.scheduledAtlasAllowed(r.Context(), account{Kind: d.Owner.Kind, ID: d.Owner.ID})

	sources, err := s.listProjectSources(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "list sources failed")
		return
	}
	runs, err := s.listProjectRuns(r.Context(), projectID, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "list runs failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": d, "sources": sources, "runs": runs})
}

type atlasProjectPatchReq struct {
	Name       *string                `json:"name"`
	Cadence    *string                `json:"cadence"`
	AutoUpdate *bool                  `json:"auto_update"`
	Output     *atlasProjectOutputReq `json:"output"`
}

// PatchAtlasProject updates a project's name / schedule / output destination.
// PATCH /api/atlas/projects/{id} — management-gated.
func (s *Server) PatchAtlasProject(w http.ResponseWriter, r *http.Request) {
	projectID, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	if ae := s.atlasProjectManageErr(r.Context(), u, projectID); ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	var req atlasProjectPatchReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}
	if req.Name != nil {
		if *req.Name == "" {
			writeError(w, http.StatusBadRequest, "invalid_name", "name cannot be empty")
			return
		}
		if _, err := s.DB.ExecContext(r.Context(), `UPDATE atlas_projects SET name=$1 WHERE id=$2`, *req.Name, projectID); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "update project failed")
			return
		}
	}
	if req.Cadence != nil {
		if !atlasCadences[*req.Cadence] {
			writeError(w, http.StatusBadRequest, "invalid_cadence", "cadence must be hourly|daily|weekly|monthly or empty")
			return
		}
		// A cadence the plan can never pay for is refused here rather than
		// discovered as a 402 once the month's allowance is gone.
		acct, ae := s.atlas.ownerAccount(r.Context(), projectID)
		if ae != nil {
			writeError(w, ae.Status, ae.Code, ae.Message)
			return
		}
		if ae := s.checkCadenceAffordable(r.Context(), acct, *req.Cadence); ae != nil {
			writeError(w, ae.Status, ae.Code, ae.Message)
			return
		}
		if _, err := s.DB.ExecContext(r.Context(), `UPDATE atlas_projects SET cadence=$1 WHERE id=$2`, *req.Cadence, projectID); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "update project failed")
			return
		}
	}
	if req.AutoUpdate != nil {
		if _, err := s.DB.ExecContext(r.Context(), `UPDATE atlas_projects SET auto_update=$1 WHERE id=$2`, boolToInt(*req.AutoUpdate), projectID); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "update project failed")
			return
		}
	}
	if req.Output != nil && req.Output.SpaceID != nil {
		if ae := s.atlasOutputSpaceWritable(r.Context(), u, *req.Output.SpaceID); ae != nil {
			writeError(w, ae.Status, ae.Code, ae.Message)
			return
		}
		var parentPageID *int64
		if req.Output.ParentPageID != nil {
			if !s.pageInSpace(r.Context(), *req.Output.ParentPageID, *req.Output.SpaceID) {
				writeError(w, http.StatusBadRequest, "invalid_parent", "parent_page_id must be a page in the output space")
				return
			}
			parentPageID = req.Output.ParentPageID
		}
		if _, err := s.DB.ExecContext(r.Context(),
			`UPDATE atlas_projects SET output_space_id=$1, output_parent_page_id=$2 WHERE id=$3`,
			*req.Output.SpaceID, nullableInt64(parentPageID), projectID); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "update project failed")
			return
		}
	}
	d, err := s.loadProjectDTO(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "load project failed")
		return
	}
	d.CanManage = true
	d.ScheduledAllowed = s.scheduledAtlasAllowed(r.Context(), account{Kind: d.Owner.Kind, ID: d.Owner.ID})
	writeJSON(w, http.StatusOK, map[string]any{"project": d})
}

// DeleteAtlasProject removes a project (CASCADEs its sources + runs + ingestion
// artifacts; the output space and generated pages are left in place).
// DELETE /api/atlas/projects/{id} — management-gated.
func (s *Server) DeleteAtlasProject(w http.ResponseWriter, r *http.Request) {
	projectID, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	if ae := s.atlasProjectManageErr(r.Context(), u, projectID); ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	if _, err := s.DB.ExecContext(r.Context(), `DELETE FROM atlas_projects WHERE id=$1`, projectID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "delete project failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RunAtlasProject triggers a full run for every source in the project.
// POST /api/atlas/projects/{id}/run — management-gated.
func (s *Server) RunAtlasProject(w http.ResponseWriter, r *http.Request) {
	projectID, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	if ae := s.atlasProjectManageErr(r.Context(), u, projectID); ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	runIDs, ae := s.startProjectRuns(r.Context(), projectID)
	if ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run_ids": runIDs})
}

// startProjectRuns triggers a full run for every source in a project. The first
// hard failure (e.g. ai_unavailable) when nothing has started yet is returned as
// an *apiErr; a busy source (run_active) once others have started is skipped.
// Authorization is the caller's responsibility (gate before calling).
func (s *Server) startProjectRuns(ctx context.Context, projectID int64) ([]int64, *apiErr) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM atlas_sources WHERE project_id=$1 ORDER BY id`, projectID)
	if err != nil {
		return nil, &apiErr{http.StatusInternalServerError, "internal", "list sources failed"}
	}
	defer rows.Close()
	var sourceIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, &apiErr{http.StatusInternalServerError, "internal", "scan source failed"}
		}
		sourceIDs = append(sourceIDs, id)
	}
	if len(sourceIDs) == 0 {
		return nil, &apiErr{http.StatusBadRequest, "no_sources", "the project has no sources to run"}
	}
	runIDs := []int64{}
	for _, sid := range sourceIDs {
		runID, ae := s.atlas.StartRun(ctx, sid)
		if ae != nil {
			if len(runIDs) == 0 {
				return nil, ae
			}
			continue
		}
		runIDs = append(runIDs, runID)
	}
	return runIDs, nil
}

// ── shared loaders ──────────────────────────────────────────────────────────

func (s *Server) loadProjectDTO(ctx context.Context, projectID int64) (atlasProjectDTO, error) {
	row := s.DB.QueryRowContext(ctx, atlasProjectSelect+` WHERE p.id = $1`, projectID)
	return scanProjectDTO(row)
}

func (s *Server) listProjectRuns(ctx context.Context, projectID int64, limit int) ([]map[string]any, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT r.id, r.source_id, r.kind, r.status, r.stage, r.err, r.coverage_json, r.started_at, r.finished_at
		  FROM atlas_runs r JOIN atlas_sources s ON s.id = r.source_id
		 WHERE s.project_id = $1 ORDER BY r.id DESC LIMIT $2`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, sourceID int64
		var kind, status, stage, errStr, covJSON, started, finished string
		if err := rows.Scan(&id, &sourceID, &kind, &status, &stage, &errStr, &covJSON, &started, &finished); err != nil {
			return nil, err
		}
		m := map[string]any{"id": id, "source_id": sourceID, "kind": kind, "status": status, "stage": stage, "started_at": started}
		if errStr != "" {
			m["err"] = errStr
		}
		if finished != "" {
			m["finished_at"] = finished
		}
		if covJSON != "" {
			var cov core.Coverage
			if json.Unmarshal([]byte(covJSON), &cov) == nil {
				m["coverage"] = cov
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// atlasOutputSpaceWritable verifies the caller may write into an existing output
// space (editor+ effective role, or instance admin). The api-key space scope
// ceiling applies.
func (s *Server) atlasOutputSpaceWritable(ctx context.Context, u *auth.User, spaceID int64) *apiErr {
	if u.IsInstanceAdmin {
		return nil
	}
	role, err := spaceRole(ctx, s.DB, u.ID, spaceID)
	if err == sql.ErrNoRows {
		return &apiErr{http.StatusForbidden, "forbidden", "no write access to the output space"}
	}
	if err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "lookup output space access failed"}
	}
	if !canEdit(role) {
		return &apiErr{http.StatusForbidden, "forbidden", "no write access to the output space"}
	}
	return nil
}

func (s *Server) pageInSpace(ctx context.Context, pageID, spaceID int64) bool {
	var ok bool
	err := s.DB.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM pages WHERE id=$1 AND space_id=$2 AND deleted_at IS NULL)`,
		pageID, spaceID).Scan(&ok)
	return err == nil && ok
}

// createProjectFolder makes the project's namespace page in its output space —
// the container sources publish beneath, so the tree reads Space → [topdir] →
// Project → Source. parentID is the (optional) user-chosen top-dir; nil roots it
// at the space. Positioned after existing siblings so it appends, not displaces.
func (s *Server) createProjectFolder(ctx context.Context, spaceID int64, parentID *int64, name string) (int64, *apiErr) {
	var maxPos sql.NullInt64
	if err := s.DB.QueryRowContext(ctx,
		`SELECT MAX(position) FROM pages WHERE space_id=$1 AND deleted_at IS NULL AND parent_id IS NOT DISTINCT FROM $2`,
		spaceID, nullableInt64(parentID)).Scan(&maxPos); err != nil {
		return 0, &apiErr{http.StatusInternalServerError, "internal", "compute folder position"}
	}
	pos := 0
	if maxPos.Valid {
		pos = int(maxPos.Int64) + 1
	}
	body := fmt.Sprintf("Documentation for **%s**, generated and maintained by atlas. Each source publishes under its own page below.\n", name)
	// The index page's summary is deterministic (it's a template, not agent-drafted),
	// so stamp it directly and lock it — the auto-summarizer must never touch it (a
	// bare one-liner is exactly what it mis-summarizes or abstains on).
	props := propsJSON(map[string]any{
		"summary":      fmt.Sprintf("Documentation for %s, generated and maintained by atlas; each source is documented on its own page.", name),
		"summary_lock": true,
	})
	var id int64
	if err := s.DB.QueryRowContext(ctx,
		`INSERT INTO pages (space_id, parent_id, title, body, position, props) VALUES ($1,$2,$3,$4,$5,$6::jsonb) RETURNING id`,
		spaceID, nullableInt64(parentID), name, body, pos, props).Scan(&id); err != nil {
		return 0, &apiErr{http.StatusInternalServerError, "internal", "create project folder"}
	}
	return id, nil
}
