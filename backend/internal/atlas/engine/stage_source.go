package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/zcag/tela/backend/internal/atlas/core"
	"github.com/zcag/tela/backend/internal/atlas/source"
)

// This file holds the source-front stage wrappers (inventory, spine). Each is a
// thin adapter: it selects the connector by Source.Type, delegates the
// source-specific work, and keeps the RunContext concerns — stash results in
// Art, persist via the store, emit progress. Acquire's wrapper lives in
// stages.go alongside the stub stages.

// --- inventory -------------------------------------------------------------

// inventoryStage discovers the source's units (for git: tracked, in-scope,
// non-binary files, classified by language) via the connector. Nothing is
// silently skipped — skips are reported by the connector and logged here.
type inventoryStage struct{}

func (inventoryStage) Name() core.StageName { return core.StageInventory }

func (inventoryStage) Run(ctx context.Context, rc *RunContext) error {
	conn, err := connectorFor(rc.Source.Type)
	if err != nil {
		return err
	}
	rc.Art.RepoDir = rc.Snapshot.Dir

	var files []core.File
	if pc, ok := conn.(source.ProgressConnector); ok {
		var rep source.InventoryReport
		files, rep, err = pc.InventoryWithProgress(ctx, rc.Snapshot, *rc.Source,
			func(tracked int) { rc.Info("scanning %d tracked files", tracked) },
			func(i1, total int) {
				if i1%50 == 0 || i1 == total {
					rc.Step(i1, total, "classifying files")
				}
			})
		if err != nil {
			return err
		}
		if err := saveFiles(rc, files); err != nil {
			return err
		}
		if rep.Scoped > 0 {
			rc.Info("scoped out %d files (subpath/include/exclude)", rep.Scoped)
		}
		rc.Info("kept %d files (%d binary, %d empty skipped) across %d languages",
			len(files), rep.Binary, rep.Empty, rep.Langs)
		return nil
	}

	// Fallback for connectors without the progress upgrade.
	if files, err = conn.Inventory(ctx, rc.Snapshot, *rc.Source); err != nil {
		return err
	}
	if rc.MaxFiles > 0 && len(files) > rc.MaxFiles {
		return fmt.Errorf("source has %d files, over this plan's limit of %d per run — narrow it with the source's subpath/include/exclude filters, or upgrade",
			len(files), rc.MaxFiles)
	}
	if err := saveFiles(rc, files); err != nil {
		return err
	}
	rc.Info("kept %d files", len(files))
	return nil
}

func saveFiles(rc *RunContext, files []core.File) error {
	rc.Art.Files = files
	return rc.Store.SaveFiles(rc.Run.ID, rc.Art.Files)
}

// --- spine -----------------------------------------------------------------

// spineStage builds the deterministic surface inventory — the checklist the
// generated docs are later audited against — via the connector (git parses Go
// with go/ast and other languages with tuned regex packs).
type spineStage struct{}

func (spineStage) Name() core.StageName { return core.StageSpine }

func (spineStage) Run(ctx context.Context, rc *RunContext) error {
	conn, err := connectorFor(rc.Source.Type)
	if err != nil {
		return err
	}

	var items []core.SpineItem
	if pc, ok := conn.(source.ProgressConnector); ok {
		items, err = pc.SpineWithProgress(ctx, rc.Snapshot, rc.Art.Files, func(i1, total int) {
			if i1%50 == 0 || i1 == total {
				rc.Step(i1, total, "extracting surface")
			}
		})
	} else {
		items, err = conn.Spine(ctx, rc.Snapshot, rc.Art.Files)
	}
	if err != nil {
		return err
	}

	rc.Art.Spine = items
	if err := rc.Store.SaveSpine(rc.Run.ID, items); err != nil {
		return err
	}
	rc.Info("surface: %s", spineSummary(items))
	return nil
}

// spineSummary renders the per-kind counts of a spine. It stays engine-side
// (also used by the outline stage): it summarizes the source-agnostic spine
// model, not source specifics.
func spineSummary(items []core.SpineItem) string {
	c := map[core.SpineKind]int{}
	for _, it := range items {
		c[it.Kind]++
	}
	order := []core.SpineKind{core.KindEntrypoint, core.KindRoute, core.KindExport, core.KindFlag, core.KindEnv, core.KindConfig, core.KindOutbound, core.KindDBModel}
	var parts []string
	for _, k := range order {
		if c[k] > 0 {
			parts = append(parts, string(k)+"="+itoa(c[k]))
		}
	}
	return strings.Join(parts, " ")
}

// lineAt returns the 1-based line number at byte offset off (used by the chunk
// stage). A small, source-agnostic helper that stays engine-side.
func lineAt(src string, off int) int { return strings.Count(src[:off], "\n") + 1 }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
