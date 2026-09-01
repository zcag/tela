package engine

import (
	"context"
	"os"
	"path/filepath"

	"github.com/zcag/tela/backend/internal/atlas/core"
)

// Acquire re-materializes a run's source into its workspace, pinning the snapshot
// and setting rc.Snapshot + rc.Art.RepoDir — the inputs the file-reading stages
// (chunk/outline/draft) need. It reuses the acquire stage's logic (secret resolve
// + connector clone + ref persist), so there's no duplicate acquire path. The
// workspace clone target is wiped first so a re-acquire over a stale workdir (left
// by the interrupted run) doesn't fail on an existing clone dir.
//
// The resume path calls this before RunFrom even when resuming past acquire, so a
// run whose workdir was lost on the restarted box (ephemeral storage, a fresh
// container) still has its source on disk for the stages that read files.
func Acquire(ctx context.Context, rc *RunContext) error {
	_ = os.RemoveAll(filepath.Join(rc.Workspace, "repo"))
	if err := (acquireStage{}).Run(ctx, rc); err != nil {
		return err
	}
	rc.Art.RepoDir = rc.Snapshot.Dir
	return nil
}

// Rehydrate reloads a run's persisted artifacts back into rc.Art and rebuilds the
// in-memory Retriever, so the pipeline can be resumed from a later stage after a
// process restart. Every stage persists its output by end-of-stage (files, spine,
// chunks+vectors, pages+bodies, coverage), so the only state lost on a kill is
// rc.Art (in-memory) and rc.Retriever — both reconstructable from the DB here.
//
// rc.Art.RepoDir is NOT set here: it comes from re-acquiring the snapshot (the
// resume caller sets rc.Art.RepoDir = rc.Snapshot.Dir after Acquire), so the
// file-reading stages (chunk/outline/draft on Jira state) keep working.
//
// The Retriever is rebuilt over exactly the chunks the index stage would have
// indexed (BuildRetriever — the same build path the index stage uses), so resume
// from any post-index stage retrieves identically.
func Rehydrate(ctx context.Context, rc *RunContext) error {
	st := rc.Store
	runID := rc.Run.ID

	files, err := st.RunFiles(runID)
	if err != nil {
		return err
	}
	spine, err := st.RunSpine(runID)
	if err != nil {
		return err
	}
	chunks, err := st.RunChunksWithVectors(runID)
	if err != nil {
		return err
	}
	pages, err := st.RunPagesFull(runID)
	if err != nil {
		return err
	}

	rc.Art.Files = files
	rc.Art.Spine = spine
	rc.Art.Chunks = chunks
	rc.Art.Pages = pages

	// Coverage was loaded onto rc.Run by GetRun (scanRun decodes coverage_json);
	// carry it onto rc.Coverage so a resume into repair/publish sees the last audit.
	if rc.Run.Coverage != nil {
		rc.Coverage = *rc.Run.Coverage
	}

	// Rebuild the hybrid retriever over the rehydrated chunks — the same build the
	// index stage runs. Skipped when there are no chunks yet (resume before chunk).
	if len(chunks) > 0 {
		rc.Retriever = BuildRetriever(rc.Art.Chunks)
		rc.Evidence = BuildEvidenceIndex(rc.Art.Chunks)
	}
	return nil
}

// stageIndexByName maps a stage name to its position in the Default pipeline, or
// -1 if unknown. Used by RunFrom to locate the resume point.
func (p *Pipeline) stageIndexByName(name core.StageName) int {
	for i, st := range p.Stages {
		if st.Name() == name {
			return i
		}
	}
	return -1
}

// RunFrom resumes the pipeline from fromStage: stages before it are skipped
// (their outputs were rehydrated by Rehydrate), and fromStage→end execute
// normally. Re-running fromStage must be idempotent — embed skips already-vectored
// chunks, draft/refine skip pages that already have a body, and repair is bounded
// — so resuming into a half-finished stage redoes only the unfinished work.
//
// An unknown/empty fromStage runs the whole pipeline (equivalent to Run), so a
// caller that can't determine a resume point cleanly restarts from the top.
func (p *Pipeline) RunFrom(ctx context.Context, rc *RunContext, fromStage core.StageName, emit Emitter) error {
	from := p.stageIndexByName(fromStage)
	if from < 0 {
		from = 0
	}
	sub := &Pipeline{Stages: p.Stages[from:]}
	return sub.Run(ctx, rc, emit)
}
