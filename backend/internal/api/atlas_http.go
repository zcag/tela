package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/zcag/tela/backend/internal/atlas/core"
)

// ── request / response shapes ───────────────────────────────────────────────

type atlasSourceCreateReq struct {
	Type     string `json:"type"` // "git" | "jira"
	Location string `json:"location"`
	Name     string `json:"name"`
	Branch   string `json:"branch"`
	Subpath  string `json:"subpath"`
	Include  string `json:"include"`
	Exclude  string `json:"exclude"`
	CredID   *int64 `json:"cred_id"` // optional reusable credential (owner-scoped)
}

type atlasSourcePatchReq struct {
	Location *string `json:"location"`
	Name     *string `json:"name"`
	Branch   *string `json:"branch"`
	Subpath  *string `json:"subpath"`
	Include  *string `json:"include"`
	Exclude  *string `json:"exclude"`
	CredID   *int64  `json:"cred_id"`
}

type atlasSourceDTO struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"project_id"`
	CredID    *int64 `json:"cred_id,omitempty"`
	Type      string `json:"type"`
	Location  string `json:"location"`
	Name      string `json:"name"`
	Ref       string `json:"ref"`
	Branch    string `json:"branch,omitempty"`
	Subpath   string `json:"subpath,omitempty"`
	Include   string `json:"include,omitempty"`
	Exclude   string `json:"exclude,omitempty"`
	CreatedAt string `json:"created_at"`
	// Drift: set (a timestamp) when detection has seen upstream move past `ref`
	// since the last generation; '' when the docs match upstream.
	StaleSince string `json:"stale_since,omitempty"`
	// Why the last drift probe failed; '' when it succeeded. Set means upstream is
	// unreachable (deleted repo, dead credential), which the UI must show ABOVE
	// staleness — a source that can't be reached has an unknown, not fresh, state.
	ProbeError string `json:"probe_error,omitempty"`
	// Latest-run summary (nil when never run), for the sources list.
	LastRunID       *int64   `json:"last_run_id,omitempty"`
	LastRunStatus   string   `json:"last_run_status,omitempty"`
	LastMustRate    *float64 `json:"last_must_rate,omitempty"`
	LastSurfaceRate *float64 `json:"last_surface_rate,omitempty"`
	LastPages       *int     `json:"last_pages,omitempty"`
	LastGeneratedAt string   `json:"last_generated_at,omitempty"` // last successful run's finish time
}

const atlasSourceDTOSelect = `
	SELECT s.id, s.project_id, s.cred_id, s.type, s.location, s.name, s.ref, s.branch, s.subpath,
	       s.include, s.exclude, s.created_at, s.stale_since, s.probe_error,
	       lr.id, lr.status, lr.coverage_json, lr.stats_json, lr.finished_at
	  FROM atlas_sources s
	  LEFT JOIN LATERAL (
	        SELECT id, status, coverage_json, stats_json, finished_at FROM atlas_runs WHERE source_id = s.id ORDER BY id DESC LIMIT 1
	  ) lr ON true`

func scanSourceDTO(sc interface{ Scan(...any) error }) (atlasSourceDTO, error) {
	var d atlasSourceDTO
	var cred, lastRunID sql.NullInt64
	var lastStatus, covJSON, statsJSON, finishedAt sql.NullString
	if err := sc.Scan(&d.ID, &d.ProjectID, &cred, &d.Type, &d.Location, &d.Name, &d.Ref, &d.Branch,
		&d.Subpath, &d.Include, &d.Exclude, &d.CreatedAt, &d.StaleSince, &d.ProbeError,
		&lastRunID, &lastStatus, &covJSON, &statsJSON, &finishedAt); err != nil {
		return d, err
	}
	if cred.Valid {
		d.CredID = &cred.Int64
	}
	if lastRunID.Valid {
		d.LastRunID = &lastRunID.Int64
		d.LastRunStatus = lastStatus.String
		d.LastGeneratedAt = finishedAt.String
		if covJSON.Valid && covJSON.String != "" {
			var cov core.Coverage
			if json.Unmarshal([]byte(covJSON.String), &cov) == nil {
				mr := cov.MustRate()
				sr := cov.Rate()
				d.LastMustRate = &mr
				d.LastSurfaceRate = &sr
			}
		}
		if statsJSON.Valid && statsJSON.String != "" {
			var st core.RunStats
			if json.Unmarshal([]byte(statsJSON.String), &st) == nil && st.Pages > 0 {
				p := st.Pages
				d.LastPages = &p
			}
		}
	}
	return d, nil
}

func (s *Server) listProjectSources(ctx context.Context, projectID int64) ([]atlasSourceDTO, error) {
	rows, err := s.DB.QueryContext(ctx, atlasSourceDTOSelect+` WHERE s.project_id = $1 ORDER BY s.id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []atlasSourceDTO{}
	for rows.Next() {
		d, err := scanSourceDTO(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ── source CRUD (under a project) ───────────────────────────────────────────

// CreateAtlasSource adds a source to a project. POST /api/atlas/projects/{id}/sources — manage.
func (s *Server) CreateAtlasSource(w http.ResponseWriter, r *http.Request) {
	projectID, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	ownerKind, ownerID, err := s.atlasProjectOwner(r.Context(), projectID)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "lookup project failed")
		return
	}
	if ae := s.atlasOwnerManageErr(r.Context(), u, ownerKind, ownerID); ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	var req atlasSourceCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}
	if req.Type == "" {
		req.Type = string(core.SourceGit)
	}
	if req.Type != string(core.SourceGit) && req.Type != string(core.SourceJira) {
		writeError(w, http.StatusBadRequest, "unsupported_type", "type must be 'git' or 'jira'")
		return
	}
	if req.Location == "" {
		writeError(w, http.StatusBadRequest, "invalid_source", "location is required")
		return
	}
	// jira ingests a single project's issues + schema; the project key rides in
	// subpath and the credential (email + API token) authenticates the REST calls.
	if req.Type == string(core.SourceJira) {
		if req.Subpath == "" {
			writeError(w, http.StatusBadRequest, "invalid_source", "jira sources require subpath (the project key)")
			return
		}
		if req.CredID == nil {
			writeError(w, http.StatusBadRequest, "invalid_source", "jira sources require a credential (email + API token)")
			return
		}
	}
	// A bound credential must belong to the project's owner OR to the acting user
	// (their personal token, lent to this source without entering the org pool).
	if req.CredID != nil {
		if ae := s.atlasCredBindable(r.Context(), *req.CredID, ownerKind, ownerID, u.ID); ae != nil {
			writeError(w, ae.Status, ae.Code, ae.Message)
			return
		}
	}
	// Per-tier Atlas source cap (plans.max_atlas_sources), counted across all the
	// owning account's projects.
	if ae := s.checkAtlasSourceQuota(r.Context(), account{Kind: ownerKind, ID: ownerID}); ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	var id int64
	var createdAt string
	err = s.DB.QueryRowContext(r.Context(), `
		INSERT INTO atlas_sources (project_id, cred_id, type, location, name, branch, subpath, include, exclude)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id, created_at`,
		projectID, nullableInt64(req.CredID), req.Type, req.Location, req.Name, req.Branch, req.Subpath, req.Include, req.Exclude).
		Scan(&id, &createdAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "create source failed")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"source": atlasSourceDTO{
		ID: id, ProjectID: projectID, CredID: req.CredID, Type: req.Type, Location: req.Location,
		Name: req.Name, Branch: req.Branch, Subpath: req.Subpath, Include: req.Include, Exclude: req.Exclude, CreatedAt: createdAt,
	}})
}

// ListAtlasSources lists a project's sources (+ latest-run summary).
// GET /api/atlas/projects/{id}/sources — view.
func (s *Server) ListAtlasSources(w http.ResponseWriter, r *http.Request) {
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
	out, err := s.listProjectSources(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "list sources failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": out})
}

// PatchAtlasSource edits a source's scope / credential. PATCH /api/atlas/sources/{id} — manage.
func (s *Server) PatchAtlasSource(w http.ResponseWriter, r *http.Request) {
	sourceID, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	ownerKind, ownerID, ok := s.atlasManageSource(w, r, sourceID)
	if !ok {
		return
	}
	var req atlasSourcePatchReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}
	if req.CredID != nil {
		u, ok := requireUser(w, r)
		if !ok {
			return
		}
		if ae := s.atlasCredBindable(r.Context(), *req.CredID, ownerKind, ownerID, u.ID); ae != nil {
			writeError(w, ae.Status, ae.Code, ae.Message)
			return
		}
	}
	set := []string{}
	args := []any{}
	add := func(col string, v any) {
		args = append(args, v)
		set = append(set, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if req.Location != nil {
		add("location", *req.Location)
	}
	if req.Name != nil {
		add("name", *req.Name)
	}
	if req.Branch != nil {
		add("branch", *req.Branch)
	}
	if req.Subpath != nil {
		add("subpath", *req.Subpath)
	}
	if req.Include != nil {
		add("include", *req.Include)
	}
	if req.Exclude != nil {
		add("exclude", *req.Exclude)
	}
	if req.CredID != nil {
		add("cred_id", *req.CredID)
	}
	if len(set) == 0 {
		writeError(w, http.StatusBadRequest, "no_fields", "no updatable fields supplied")
		return
	}
	args = append(args, sourceID)
	q := "UPDATE atlas_sources SET " + strings.Join(set, ", ") + fmt.Sprintf(" WHERE id = $%d", len(args))
	if _, err := s.DB.ExecContext(r.Context(), q, args...); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "update source failed")
		return
	}
	d, err := scanSourceDTO(s.DB.QueryRowContext(r.Context(), atlasSourceDTOSelect+` WHERE s.id = $1`, sourceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "load source failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"source": d})
}

// DeleteAtlasSource removes a source (CASCADEs its runs + ingestion artifacts;
// generated pages are left in place). DELETE /api/atlas/sources/{id} — manage.
func (s *Server) DeleteAtlasSource(w http.ResponseWriter, r *http.Request) {
	sourceID, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	if _, _, ok := s.atlasManageSource(w, r, sourceID); !ok {
		return
	}
	if _, err := s.DB.ExecContext(r.Context(), `DELETE FROM atlas_sources WHERE id=$1`, sourceID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "delete source failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RunAtlasSource triggers a full run for one source. POST /api/atlas/sources/{id}/run — manage.
func (s *Server) RunAtlasSource(w http.ResponseWriter, r *http.Request) {
	sourceID, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	if _, _, ok := s.atlasManageSource(w, r, sourceID); !ok {
		return
	}
	runID, ae := s.atlas.StartRun(r.Context(), sourceID)
	if ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run_id": runID})
}

// ListAtlasSourceRuns lists a source's runs. GET /api/atlas/sources/{id}/runs — view.
func (s *Server) ListAtlasSourceRuns(w http.ResponseWriter, r *http.Request) {
	sourceID, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	if !s.atlasViewSource(w, r, sourceID) {
		return
	}
	rows, err := s.DB.QueryContext(r.Context(),
		`SELECT id, kind, status, stage, err, coverage_json, stats_json, started_at, finished_at
		   FROM atlas_runs WHERE source_id=$1 ORDER BY id DESC LIMIT 50`, sourceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "list runs failed")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var kind, status, stage, errStr, covJSON, statsJSON, started, finished string
		if err := rows.Scan(&id, &kind, &status, &stage, &errStr, &covJSON, &statsJSON, &started, &finished); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "scan run failed")
			return
		}
		m := map[string]any{"id": id, "kind": kind, "status": status, "stage": stage, "started_at": started}
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
	writeJSON(w, http.StatusOK, map[string]any{"runs": out})
}

// GetAtlasRun returns a run's status + coverage + stats. GET /api/atlas/runs/{id} — view.
func (s *Server) GetAtlasRun(w http.ResponseWriter, r *http.Request) {
	runID, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	if !s.atlasViewRun(w, r, runID) {
		return
	}
	run, err := s.atlas.store.GetRun(runID)
	if err != nil || run == nil {
		writeError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	// project_id lets the run screen link back to its project (a run belongs to a
	// project via its source); best-effort, 0 if the source/project is gone.
	var projectID int64
	_ = s.DB.QueryRowContext(r.Context(),
		`SELECT s.project_id FROM atlas_runs r JOIN atlas_sources s ON s.id=r.source_id WHERE r.id=$1`, runID).Scan(&projectID)
	writeJSON(w, http.StatusOK, map[string]any{"run": run, "project_id": projectID})
}

// StreamAtlasRun streams a run's live progress over SSE: persisted events replay
// first, then live tail. GET /api/atlas/runs/{id}/stream — view.
func (s *Server) StreamAtlasRun(w http.ResponseWriter, r *http.Request) {
	runID, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	if !s.atlasViewRun(w, r, runID) {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Subscribe BEFORE replay so an event fired during replay isn't lost.
	ch, unsub := s.atlas.hub.subscribe(runID)
	defer unsub()

	send := func(e core.Event) {
		b, _ := json.Marshal(e)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	// Replay persisted events.
	rows, err := s.DB.QueryContext(r.Context(),
		`SELECT stage, level, msg, cur, total, at FROM atlas_run_events WHERE run_id=$1 ORDER BY id`, runID)
	if err == nil {
		for rows.Next() {
			var e core.Event
			var stage, level, at string
			if rows.Scan(&stage, &level, &e.Msg, &e.Cur, &e.Total, &at) == nil {
				e.RunID, e.Stage, e.Level = runID, core.StageName(stage), core.EventLevel(level)
				if t, perr := time.Parse("2006-01-02 15:04:05", at); perr == nil {
					e.At = t
				}
				send(e)
			}
		}
		rows.Close()
	}

	// If the run already finished, send a terminal marker and stop — don't hang.
	var status string
	_ = s.DB.QueryRowContext(r.Context(), `SELECT status FROM atlas_runs WHERE id=$1`, runID).Scan(&status)
	if status == string(core.RunDone) || status == string(core.RunFailed) || status == string(core.RunCanceled) {
		send(core.Event{RunID: runID, Stage: atlasEndStage, Level: core.LevelInfo, Msg: status})
		return
	}

	// Live tail until the run ends, the client disconnects, or we go idle.
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, open := <-ch:
			if !open {
				return
			}
			send(e)
			if e.Stage == atlasEndStage {
				return
			}
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// atlasEndStage is the synthetic terminal marker published to the hub when a run
// reaches a terminal state, so live SSE subscribers know to close.
const atlasEndStage core.StageName = "__end__"

// ── helpers: resolve the governing project + gate ───────────────────────────

// atlasManageSource resolves a source's project and enforces management rights,
// writing the error envelope on failure. Returns (ownerKind, ownerID, true) when
// the caller may manage.
func (s *Server) atlasManageSource(w http.ResponseWriter, r *http.Request, sourceID int64) (string, int64, bool) {
	projectID, err := s.atlasSourceProject(r.Context(), sourceID)
	if err != nil {
		s.atlasResolveErr(w, err)
		return "", 0, false
	}
	u, ok := requireUser(w, r)
	if !ok {
		return "", 0, false
	}
	ownerKind, ownerID, err := s.atlasProjectOwner(r.Context(), projectID)
	if err != nil {
		s.atlasResolveErr(w, err)
		return "", 0, false
	}
	if ae := s.atlasOwnerManageErr(r.Context(), u, ownerKind, ownerID); ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return "", 0, false
	}
	return ownerKind, ownerID, true
}

func (s *Server) atlasViewSource(w http.ResponseWriter, r *http.Request, sourceID int64) bool {
	projectID, err := s.atlasSourceProject(r.Context(), sourceID)
	if err != nil {
		s.atlasResolveErr(w, err)
		return false
	}
	u, ok := requireUser(w, r)
	if !ok {
		return false
	}
	if ae := s.atlasProjectViewErr(r.Context(), u, projectID); ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return false
	}
	return true
}

func (s *Server) atlasViewRun(w http.ResponseWriter, r *http.Request, runID int64) bool {
	projectID, err := s.atlasRunProject(r.Context(), runID)
	if err != nil {
		s.atlasResolveErr(w, err)
		return false
	}
	u, ok := requireUser(w, r)
	if !ok {
		return false
	}
	if ae := s.atlasProjectViewErr(r.Context(), u, projectID); ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return false
	}
	return true
}

func (s *Server) atlasSourceProject(ctx context.Context, sourceID int64) (int64, error) {
	var projectID int64
	err := s.DB.QueryRowContext(ctx, `SELECT project_id FROM atlas_sources WHERE id=$1`, sourceID).Scan(&projectID)
	return projectID, err
}

func (s *Server) atlasRunProject(ctx context.Context, runID int64) (int64, error) {
	var projectID int64
	err := s.DB.QueryRowContext(ctx,
		`SELECT s.project_id FROM atlas_runs r JOIN atlas_sources s ON s.id=r.source_id WHERE r.id=$1`, runID).Scan(&projectID)
	return projectID, err
}

// atlasCredBindable verifies a credential may be bound to a source in a project
// owned by (projKind, projID). It's allowed when the credential is owned by
// EITHER the project's owner OR the acting user as a PERSONAL credential — so a
// user can lend their own private token to an org project's source without
// publishing it to the org's reusable credential pool. The privacy model holds:
// other org admins can RUN the project (a run uses the token to clone) but never
// see its value (write-only) and can't reuse it elsewhere (it's not in their
// pool). The acting user is already gated to project management at the call site.
func (s *Server) atlasCredBindable(ctx context.Context, credID int64, projKind string, projID, actingUserID int64) *apiErr {
	var ok bool
	err := s.DB.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM atlas_credentials WHERE id=$1 AND (
			(owner_kind=$2 AND owner_id=$3) OR (owner_kind=$4 AND owner_id=$5)
		))`,
		credID, projKind, projID, accountUser, actingUserID).Scan(&ok)
	if err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "lookup credential failed"}
	}
	if !ok {
		return &apiErr{http.StatusBadRequest, "invalid_credential", "cred_id must be a credential owned by this project's owner or by you"}
	}
	return nil
}

func (s *Server) atlasResolveErr(w http.ResponseWriter, err error) {
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "internal", "lookup failed")
}
