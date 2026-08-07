package api

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/zcag/tela/backend/internal/atlas/core"
	"github.com/zcag/tela/backend/internal/atlas/engine"
)

// atlasSourceRow is the persisted binding the executor needs to drive a run: the
// source's scope + the project it belongs to (which resolves the output space)
// and the optional reusable credential it authenticates with.
type atlasSourceRow struct {
	ID        int64
	ProjectID int64
	Type      string
	Location  string
	Name      string
	Ref       string
	Branch    string
	Subpath   string
	Include   string
	Exclude   string
	CredID    *int64
}

const atlasSourceCols = `id, project_id, type, location, name, ref, branch, subpath, include, exclude, cred_id`

func scanAtlasSource(sc interface{ Scan(...any) error }) (atlasSourceRow, error) {
	var r atlasSourceRow
	var cred sql.NullInt64
	err := sc.Scan(&r.ID, &r.ProjectID, &r.Type, &r.Location, &r.Name, &r.Ref, &r.Branch, &r.Subpath, &r.Include, &r.Exclude, &cred)
	if cred.Valid {
		r.CredID = &cred.Int64
	}
	return r, err
}

func (m *atlasManager) loadSource(ctx context.Context, sourceID int64) (atlasSourceRow, error) {
	row := m.s.DB.QueryRowContext(ctx, `SELECT `+atlasSourceCols+` FROM atlas_sources WHERE id = $1`, sourceID)
	return scanAtlasSource(row)
}

// StartRun creates a full run for a source and drives it in the background.
// Authorization is the caller's responsibility (gate the source's project with
// atlasProjectManageErr before calling). One active run per source: a second
// start is rejected.
func (m *atlasManager) StartRun(ctx context.Context, sourceID int64) (int64, *apiErr) {
	if !m.atlasEnabled() {
		return 0, &apiErr{http.StatusServiceUnavailable, "ai_unavailable", "atlas needs both an embedder (TELA_RAG_EMBED_URL) and a chat model (TELA_LLM_URL)"}
	}
	if m.sourceHasNonTerminalRun(ctx, sourceID) {
		return 0, &apiErr{http.StatusConflict, "run_active", "a run is already queued or in progress for this source"}
	}
	src, err := m.loadSource(ctx, sourceID)
	if err == sql.ErrNoRows {
		return 0, &apiErr{http.StatusNotFound, "not_found", "source not found"}
	} else if err != nil {
		return 0, &apiErr{http.StatusInternalServerError, "internal", "lookup source failed"}
	}

	// Monthly run + embed-token budget for the owning account. Refused BEFORE the
	// run row exists, so a blocked attempt leaves no failed run in the history and
	// costs the user nothing. The per-run file cap can't be judged yet — the repo
	// isn't cloned — so the engine enforces that one after inventory.
	if acct, ae := m.ownerAccount(ctx, src.ProjectID); ae != nil {
		return 0, ae
	} else if ae := m.s.checkAtlasRunQuota(ctx, acct); ae != nil {
		return 0, ae
	}

	var runID int64
	if err := m.s.DB.QueryRowContext(ctx,
		`INSERT INTO atlas_runs (source_id, kind, status) VALUES ($1, 'full', 'pending') RETURNING id`,
		sourceID).Scan(&runID); err != nil {
		return 0, &apiErr{http.StatusInternalServerError, "internal", "create run failed"}
	}

	m.signalDispatch() // enqueue — the dispatcher starts it when a run slot is free
	return runID, nil
}

// buildRunContext assembles the engine inputs for a run. It resolves the
// source's project → output destination (creating the output space on the first
// run if it doesn't exist yet), binds the publisher to that space + top-dir, and
// pins the instance chat/embed model. core.Project.ID carries the tela project id
// (the notify path resolves the project's owner/managers from it).
func (m *atlasManager) buildRunContext(ctx context.Context, src atlasSourceRow, run *core.Run, workspace string) (*engine.RunContext, error) {
	proj, err := m.loadProject(ctx, src.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("load project %d: %w", src.ProjectID, err)
	}
	spaceID, parentPageID, ae := m.s.ensureOutputSpace(ctx, proj)
	if ae != nil {
		return nil, fmt.Errorf("ensure output space: %s", ae.Message)
	}
	client := m.newLLMClient()
	coreProj := &core.Project{ID: proj.ID, Name: proj.Name, Model: atlasModelCfg(m.s.rag.EmbedModel())}
	// Resolve the bound credential into the transient secret fields (Location stays
	// clean; the git connector injects auth at command time). jira reads
	// SecretValue + SecretMeta["email"]; git reads SecretValue + SecretMeta["username"].
	coreSrc := m.resolveCoreSource(ctx, src)
	return &engine.RunContext{
		MaxFiles:  m.maxFilesFor(ctx, account{Kind: proj.OwnerKind, ID: proj.OwnerID}),
		Project:   coreProj,
		Source:    &coreSrc,
		Run:       run,
		Workspace: workspace,
		Store:     m.store,
		LLM:       client,
		Publisher: newAtlasPublisher(m.s.DB, m.s.rag.QueueReindex, spaceID, parentPageID),
		OnFinish:  m.onFinish,
	}, nil
}

// spawn drives one run to completion in its own goroutine. fromStage == "" runs
// the full pipeline; a non-empty fromStage resumes (re-acquire + rehydrate +
// RunFrom), used by ResumeDangling after a restart.
func (m *atlasManager) spawn(src atlasSourceRow, runID int64, fromStage core.StageName) {
	runCtx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.active[src.ID] = cancel
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			delete(m.active, src.ID)
			m.mu.Unlock()
			cancel()
			if r := recover(); r != nil {
				slog.Error("atlas: run panicked", "run", runID, "panic", r)
				_, _ = m.s.DB.Exec(`UPDATE atlas_runs SET status='failed', err=$2, finished_at=tela_now() WHERE id=$1`,
					runID, fmt.Sprintf("panic: %v", r))
			}
			m.signalDispatch() // slot freed — let the dispatcher start the next queued run
		}()

		workspace, err := os.MkdirTemp(atlasWorkRoot(), fmt.Sprintf("atlas-run-%d-", runID))
		if err != nil {
			slog.Error("atlas: workspace", "run", runID, "err", err)
			_, _ = m.s.DB.Exec(`UPDATE atlas_runs SET status='failed', err=$2, finished_at=tela_now() WHERE id=$1`, runID, err.Error())
			return
		}
		defer os.RemoveAll(workspace)

		run, err := m.store.GetRun(runID)
		if err != nil || run == nil {
			slog.Error("atlas: load run", "run", runID, "err", err)
			return
		}
		rc, err := m.buildRunContext(runCtx, src, run, workspace)
		if err != nil {
			slog.Error("atlas: build run context", "run", runID, "err", err)
			_, _ = m.s.DB.Exec(`UPDATE atlas_runs SET status='failed', err=$2, finished_at=tela_now() WHERE id=$1`, runID, err.Error())
			return
		}
		emit := func(e core.Event) { m.hub.publish(e) }
		// Publish a terminal marker so live SSE subscribers close cleanly when the
		// run ends (the engine emits no terminal event of its own).
		defer m.hub.publish(core.Event{RunID: runID, Stage: atlasEndStage, Level: core.LevelInfo, Msg: "finished"})

		if fromStage == "" {
			_ = engine.Default().Run(runCtx, rc, emit)
			return
		}
		// Resume: re-materialize the source on disk, rehydrate persisted artifacts
		// + the retriever, then continue from the persisted stage.
		if err := engine.Acquire(runCtx, rc); err != nil {
			slog.Error("atlas: resume acquire", "run", runID, "err", err)
			return
		}
		if err := engine.Rehydrate(runCtx, rc); err != nil {
			slog.Error("atlas: resume rehydrate", "run", runID, "err", err)
			return
		}
		_ = engine.Default().RunFrom(runCtx, rc, fromStage, emit)
	}()
}

// onFinish meters the run's chat token usage into tela's AI-usage ledger and
// notifies the space's managers that the run finished. Embeddings are already
// metered per-call by tela's rag recorder (s.rag.Embed), so only chat is
// recorded here to avoid double counting. Both steps are best-effort: a metering
// or notify failure must never affect the run (it's already terminal).
func (m *atlasManager) onFinish(rc *engine.RunContext, status core.RunStatus, runErr error) {
	if rc.Run != nil && rc.Run.Stats != nil {
		u := rc.Run.Stats.Usage
		m.s.recordAIUsage("chat", rc.Project.Model.ChatModel, int(u.PromptTokens), int(u.CompletionTokens), 0)
	}
	m.notifyAtlasRunFinish(context.Background(), rc, status, runErr)
}

// signalDispatch nudges the queue dispatcher (non-blocking; coalesces repeated
// signals into a single pending wake-up).
func (m *atlasManager) signalDispatch() {
	select {
	case m.dispatch <- struct{}{}:
	default:
	}
}

// sourceHasNonTerminalRun is the one-run-per-source guard, DB-backed: a queued
// run isn't in the in-memory active set, so the old map check would miss it.
func (m *atlasManager) sourceHasNonTerminalRun(ctx context.Context, sourceID int64) bool {
	var n int
	_ = m.s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM atlas_runs WHERE source_id=$1 AND status IN ('pending','running')`,
		sourceID).Scan(&n)
	return n > 0
}

// runDispatcher is the durable run queue. It keeps at most maxRuns runs executing,
// claiming the oldest pending run from the DB whenever a slot frees. The queue is
// the set of status='pending' rows (not in-memory goroutines), so a restart or
// redeploy never strands a waiting run — on boot the dispatcher picks the pending
// rows back up (ResumeDangling separately recovers runs that were mid-execution).
// Signalled on enqueue and on each run finish; a slow ticker is a safety backstop.
func (m *atlasManager) runDispatcher(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		m.dispatchPending(ctx)
		select {
		case <-ctx.Done():
			return
		case <-m.dispatch:
		case <-t.C:
		}
	}
}

// dispatchPending starts queued runs (oldest first) until the global slot cap is
// reached. Only runDispatcher calls it (single goroutine), so the slot accounting
// against the active set is race-free.
func (m *atlasManager) dispatchPending(ctx context.Context) {
	if !m.atlasEnabled() {
		return
	}
	for {
		m.mu.Lock()
		free := len(m.active) < m.maxRuns
		m.mu.Unlock()
		if !free {
			return
		}
		src, runID, ok := m.claimNextPending(ctx)
		if !ok {
			return
		}
		m.spawn(src, runID, "")
	}
}

// claimNextPending atomically claims the oldest pending run (pending→running) and
// returns its source. One-run-per-source is enforced at enqueue, so a pending
// run's source is never already executing. ok=false when the queue is empty.
func (m *atlasManager) claimNextPending(ctx context.Context) (atlasSourceRow, int64, bool) {
	var runID int64
	var src atlasSourceRow
	var cred sql.NullInt64
	err := m.s.DB.QueryRowContext(ctx,
		`SELECT r.id, `+atlasSourceColsPrefixed("s")+`
		   FROM atlas_runs r JOIN atlas_sources s ON s.id = r.source_id
		  WHERE r.status='pending' ORDER BY r.id LIMIT 1`).
		Scan(&runID, &src.ID, &src.ProjectID, &src.Type, &src.Location, &src.Name, &src.Ref, &src.Branch, &src.Subpath, &src.Include, &src.Exclude, &cred)
	if err != nil {
		return src, 0, false // sql.ErrNoRows ⇒ empty queue
	}
	if cred.Valid {
		src.CredID = &cred.Int64
	}
	// Claim the run only if fewer than maxRuns are already executing. The cap is
	// enforced HERE, in the DB, not via the in-memory active map: that map can
	// undercount (a spawn/accounting race, or a second manager), which silently
	// lifts the cap and lets a project's sources all run at once — the exact
	// overload we hit. The DB count is authoritative and holds across the map,
	// races, multiple instances, and restarts. A pending row isn't 'running', so
	// it doesn't count itself.
	res, err := m.s.DB.ExecContext(ctx,
		`UPDATE atlas_runs SET status='running'
		  WHERE id=$1 AND status='pending'
		    AND (SELECT count(*) FROM atlas_runs WHERE status='running') < $2`, runID, m.maxRuns)
	if err != nil {
		return src, 0, false
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return src, 0, false // cap reached, or already claimed (multi-instance) — the ticker retries
	}
	return src, runID, true
}

// ResumeDangling picks up runs left 'running' by a previous process and continues
// them from their persisted stage. Called once at boot (mirrors atlas's
// ResumeDangling). Best-effort: a run that can't be resumed is left as-is.
func (m *atlasManager) ResumeDangling(ctx context.Context) {
	rows, err := m.s.DB.QueryContext(ctx,
		`SELECT r.id, r.stage, `+atlasSourceColsPrefixed("s")+`
		   FROM atlas_runs r JOIN atlas_sources s ON s.id = r.source_id
		  WHERE r.status = 'running'`)
	if err != nil {
		slog.Error("atlas: resume scan", "err", err)
		return
	}
	defer rows.Close()
	type pending struct {
		src   atlasSourceRow
		runID int64
		stage core.StageName
	}
	var todo []pending
	for rows.Next() {
		var runID int64
		var stage string
		var src atlasSourceRow
		var cred sql.NullInt64
		if err := rows.Scan(&runID, &stage, &src.ID, &src.ProjectID, &src.Type, &src.Location, &src.Name, &src.Ref, &src.Branch, &src.Subpath, &src.Include, &src.Exclude, &cred); err != nil {
			slog.Error("atlas: resume row", "err", err)
			continue
		}
		if cred.Valid {
			src.CredID = &cred.Int64
		}
		todo = append(todo, pending{src, runID, core.StageName(stage)})
	}
	for _, p := range todo {
		if !m.atlasEnabled() {
			slog.Warn("atlas: cannot resume run (AI unconfigured)", "run", p.runID)
			continue
		}
		slog.Info("atlas: resuming dangling run", "run", p.runID, "from", p.stage)
		m.spawn(p.src, p.runID, p.stage)
	}
	m.signalDispatch() // fill any remaining slots with queued (pending) runs
}

// atlasSourceColsPrefixed returns the source columns qualified with a table
// alias, for the resume join (keeps column order in sync with scanAtlasSource).
func atlasSourceColsPrefixed(alias string) string {
	return alias + ".id, " + alias + ".project_id, " + alias + ".type, " +
		alias + ".location, " + alias + ".name, " + alias + ".ref, " + alias + ".branch, " +
		alias + ".subpath, " + alias + ".include, " + alias + ".exclude, " + alias + ".cred_id"
}

// atlasWorkRoot is where run workspaces (repo clones) are materialized. Override
// with TELA_ATLAS_WORKDIR; defaults to the OS temp dir.
func atlasWorkRoot() string {
	if v := os.Getenv("TELA_ATLAS_WORKDIR"); v != "" {
		return v
	}
	return os.TempDir()
}

// ownerAccount resolves the billable account behind an atlas project — the scope
// every atlas quota is metered against.
func (m *atlasManager) ownerAccount(ctx context.Context, projectID int64) (account, *apiErr) {
	kind, id, err := m.s.atlasProjectOwner(ctx, projectID)
	if err == sql.ErrNoRows {
		return account{}, &apiErr{http.StatusNotFound, "not_found", "project not found"}
	} else if err != nil {
		return account{}, &apiErr{http.StatusInternalServerError, "internal", "lookup project failed"}
	}
	return account{Kind: kind, ID: id}, nil
}

// maxFilesFor resolves the owning plan's per-run file cap. 0 = unlimited, which
// is also what a lookup failure yields: a quota check that can't read the plan
// must not silently block a paying customer's run — the monthly gates at
// StartRun already fail closed, and this one is the coarse backstop.
func (m *atlasManager) maxFilesFor(ctx context.Context, acct account) int {
	p, err := planFor(ctx, m.s.DB, acct)
	if err != nil || p.MaxAtlasFilesPerRun == nil {
		return 0
	}
	return int(*p.MaxAtlasFilesPerRun)
}
