// Package engine is atlas's pipeline core: an ordered list of stages run over a
// source, emitting progress as it goes. It's a plain library — the CLI drives it
// today, an HTTP server will drive it later, neither is imported here.
package engine

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/zcag/tela/backend/internal/atlas/core"
	"github.com/zcag/tela/backend/internal/atlas/llm"
	"github.com/zcag/tela/backend/internal/atlas/source"
	gitsrc "github.com/zcag/tela/backend/internal/atlas/source/git"
	jirasrc "github.com/zcag/tela/backend/internal/atlas/source/jira"
)

// connectors is the registry of source-type → connector. The source-front
// stages (acquire/inventory/spine) select by rc.Source.Type; a new source type
// is a new entry here, not edits across the stages (git + jira today).
var connectors = map[string]source.Connector{
	gitsrc.New().Type():  gitsrc.New(),
	jirasrc.New().Type(): jirasrc.New(),
}

// connectorFor returns the connector for a source type, or an error naming the
// unsupported type (matching today's acquire-stage guard).
func connectorFor(t core.SourceType) (source.Connector, error) {
	if c, ok := connectors[string(t)]; ok {
		return c, nil
	}
	return nil, fmt.Errorf("unsupported source type %q", t)
}

// DetectDelta acquires src fresh into workdir and diffs fromRef → the freshly
// pinned ref, returning the in-scope ChangeSet and the new ref. It selects the
// connector by Source.Type, so the delta entry path stays source-agnostic. Used
// by the server's sync path to decide whether a delta run is worth starting.
func DetectDelta(ctx context.Context, src core.Source, workdir, fromRef string) (source.ChangeSet, string, error) {
	conn, err := connectorFor(src.Type)
	if err != nil {
		return source.ChangeSet{}, "", err
	}
	snap, err := conn.Acquire(ctx, src, workdir)
	if err != nil {
		return source.ChangeSet{}, "", err
	}
	cs, err := conn.Delta(ctx, snap, src, fromRef, snap.Ref)
	if err != nil {
		return source.ChangeSet{}, snap.Ref, err
	}
	return cs, snap.Ref, nil
}

// HasChanges is the cheap, no-materialize freshness probe: it selects the
// connector by Source.Type and asks whether the source has anything newer than
// fromRef (git: ls-remote HEAD vs fromRef; jira: a count of issues updated since
// fromRef). The refresh poller uses it to avoid cloning/fetching when nothing
// changed. src must carry its resolved secret (SecretValue/SecretMeta) so the
// probe can authenticate.
func HasChanges(ctx context.Context, src core.Source, fromRef string) (bool, error) {
	conn, err := connectorFor(src.Type)
	if err != nil {
		return false, err
	}
	return conn.HasChanges(ctx, src, fromRef)
}

// Stage is one step of the pipeline. Implementations live in stages_*.go and
// receive a RunContext to read inputs, emit progress, and write outputs.
type Stage interface {
	Name() core.StageName
	Run(ctx context.Context, rc *RunContext) error
}

// Emitter receives every progress event. The CLI uses a console emitter; the
// server will fan events out over SSE. Events are also persisted by RunContext.
type Emitter func(core.Event)

// RunContext carries everything a stage needs for one run.
type RunContext struct {
	Project   *core.Project
	Source    *core.Source
	Run       *core.Run
	Workspace string          // run workdir; the connector materializes the source under it
	Snapshot  source.Snapshot // acquired source at a pinned ref (set by acquire)
	Store     EngineStore
	LLM       *llm.Client
	Publisher Publisher      // delivers finished pages into the bound space (nil = no delivery)
	Art       core.Artifacts // shared state threaded through stages
	Retriever *Retriever     // built by the index stage, used by generation
	Coverage  core.Coverage  // computed by validate, refined by repair, written by publish

	// MaxFiles refuses a source larger than the owner's plan allows, checked right
	// after inventory — before chunking or embedding, so an oversized repo costs
	// nothing but the clone. 0 = unlimited. It is deliberately a hard refusal
	// rather than a truncation: indexing the first N files would produce docs that
	// silently claim to cover a whole repo, and the coverage audit would agree.
	MaxFiles int

	// OnFinish, if set, is called once when the run reaches a terminal state
	// (done/failed). It is the pluggable seam for run-finish notifications —
	// standalone atlas hard-wired internal/notify here; inside tela the executor
	// injects a hook (reuse tela's mailer, etc.). Non-blocking + non-fatal is the
	// caller's responsibility (run it in a goroutine; swallow errors).
	OnFinish func(rc *RunContext, status core.RunStatus, runErr error)

	stage    core.StageName
	emit     Emitter
	progress atomic.Int64 // monotonic done-counter for parallel stages (see StepDone)
}

// Emit reports progress. total=0 means "just a log line"; total>0 drives a bar.
func (rc *RunContext) Emit(level core.EventLevel, cur, total int, format string, a ...any) {
	e := core.Event{
		RunID: rc.Run.ID, Stage: rc.stage, Level: level,
		Msg: fmt.Sprintf(format, a...), Cur: cur, Total: total, At: time.Now(),
	}
	if rc.Store != nil {
		_ = rc.Store.AppendEvent(e)
	}
	if rc.emit != nil {
		rc.emit(e)
	}
}

func (rc *RunContext) Info(format string, a ...any) { rc.Emit(core.LevelInfo, 0, 0, format, a...) }
func (rc *RunContext) Warn(format string, a ...any) { rc.Emit(core.LevelWarn, 0, 0, format, a...) }
func (rc *RunContext) Step(cur, total int, format string, a ...any) {
	rc.Emit(core.LevelInfo, cur, total, format, a...)
}

// resetProgress zeroes the parallel done-counter. Called at the top of a stage
// that fans out, so StepDone reports 1..total within that stage.
func (rc *RunContext) resetProgress() { rc.progress.Store(0) }

// StepDone is the concurrency-safe progress reporter for parallel stages: it
// atomically increments the done-counter and emits the new (monotonic) count.
// Workers finish out of order, so cur reflects "how many are done", not which
// index just completed — the bar still advances 1..total exactly once each.
func (rc *RunContext) StepDone(total int, format string, a ...any) {
	cur := int(rc.progress.Add(1))
	rc.Emit(core.LevelInfo, cur, total, format, a...)
}

// Pipeline is the ordered set of stages.
type Pipeline struct{ Stages []Stage }

// Default is the full atlas pipeline. Stages 2–13 are stubs in Phase 1; each is
// promoted to a real implementation as we build it. Acquire is already real.
func Default() *Pipeline {
	return &Pipeline{Stages: []Stage{
		&acquireStage{},
		&inventoryStage{},
		&spineStage{},
		&chunkStage{},
		&embedStage{},
		&indexStage{},
		&outlineStage{},
		&draftStage{},
		&refineStage{},
		&validateStage{},
		&repairStage{},
		&publishStage{},
	}}
}

// Run executes the pipeline, updating run status and emitting stage boundaries.
// Run-to-completion: it does not bail early; a failed stage fails the run.
func (p *Pipeline) Run(ctx context.Context, rc *RunContext, emit Emitter) error {
	rc.emit = emit
	rc.Run.Status = core.RunRunning
	_ = rc.Store.UpdateRun(rc.Run)

	for _, st := range p.Stages {
		if err := ctx.Err(); err != nil {
			return p.finish(rc, core.RunCanceled, err)
		}
		rc.stage = st.Name()
		rc.Run.Stage = st.Name()
		_ = rc.Store.UpdateRun(rc.Run)
		rc.Emit(core.LevelInfo, 0, 0, "▶ stage start")
		started := time.Now()
		if err := st.Run(ctx, rc); err != nil {
			rc.Emit(core.LevelError, 0, 0, "✗ %v", err)
			return p.finish(rc, core.RunFailed, err)
		}
		rc.Emit(core.LevelInfo, 0, 0, "✓ done in %s", time.Since(started).Round(time.Millisecond))
	}
	return p.finish(rc, core.RunDone, nil)
}

func (p *Pipeline) finish(rc *RunContext, status core.RunStatus, err error) error {
	rc.Run.Status = status
	rc.Run.FinishedAt = time.Now()
	if err != nil {
		rc.Run.Err = err.Error()
	}
	_ = rc.Store.UpdateRun(rc.Run)
	// Fire the pluggable run-finish hook on a terminal status. Standalone atlas
	// hard-wired internal/notify here; inside tela the executor injects OnFinish
	// (and is responsible for keeping it non-blocking + non-fatal).
	if (status == core.RunDone || status == core.RunFailed) && rc.OnFinish != nil {
		rc.OnFinish(rc, status, err)
	}
	return err
}
