package engine

import (
	"context"

	"github.com/zcag/tela/backend/internal/atlas/core"
)

// indexStage builds the in-memory hybrid retriever (dense + sparse) over the
// embedded chunks and stashes it on the run context for the generation stages.
type indexStage struct{}

func (indexStage) Name() core.StageName { return core.StageIndex }

func (indexStage) Run(ctx context.Context, rc *RunContext) error {
	rc.Retriever = BuildRetriever(rc.Art.Chunks)
	rc.Evidence = BuildEvidenceIndex(rc.Art.Chunks)
	rc.Info("indexed %d chunks for hybrid retrieval (dense + BM25)", len(rc.Art.Chunks))
	return nil
}
