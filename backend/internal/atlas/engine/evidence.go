package engine

import (
	"fmt"
	"strings"

	"github.com/zcag/tela/backend/internal/atlas/core"
)

// EXPERIMENT 2026-09-01 (atlas output-budget pack) — docs/atlas-output-budget-experiment.md
//
// EvidenceIndex answers "what does the source look like AT this spine item?"
// deterministically, by the item's own file:line.
//
// WHY IT EXISTS. Reference pages used to get their grounding from the same
// hybrid retriever the narrative pages use, with a query built by concatenating
// EVERY item name in the batch (renderSpineList) into one string. Embedding a
// bag of 160 flag names produces a vector that describes none of them, and the
// K=18 chunks it returned then had to ground all 160 items — so the model had
// each item's NAME and no code, and wrote the only thing it could:
// "--dev — Type: CLI flag. Description: Enables dev mode." Filler that restates
// the name, on every item, indistinguishable to the coverage audit from a real
// description.
//
// But a spine item already carries its exact file:line, and every chunk carries
// file/start_line/end_line — so the item→evidence join is exact and free. This
// looks it up instead of guessing at it, which is both cheaper (no embedding
// call per batch) and strictly better grounded.
type EvidenceIndex struct{ byFile map[string][]core.Chunk }

// BuildEvidenceIndex groups chunks by file. Built alongside the Retriever (index
// stage + resume) from the same chunk set, so it is available wherever the
// retriever is.
func BuildEvidenceIndex(chunks []core.Chunk) *EvidenceIndex {
	ix := &EvidenceIndex{byFile: make(map[string][]core.Chunk, 64)}
	for _, c := range chunks {
		ix.byFile[c.File] = append(ix.byFile[c.File], c)
	}
	return ix
}

const maxEvidenceLineChars = 200 // a minified/generated line must not eat the budget

// Snippet returns up to maxLines of source starting at line, each prefixed with
// its real line number, or "" when the line isn't covered by any chunk. A nil
// index returns "" so callers never have to guard (a run that somehow reaches
// drafting without an index degrades to the old name-only behaviour rather than
// panicking).
func (ix *EvidenceIndex) Snippet(file string, line, maxLines int) string {
	if ix == nil || line < 1 || maxLines < 1 {
		return ""
	}
	c, ok := ix.containing(file, line)
	if !ok {
		return ""
	}
	lines := strings.Split(c.Text, "\n")
	start := line - c.StartLine // Text is exactly lines[StartLine-1:EndLine] of the file
	if start < 0 || start >= len(lines) {
		return ""
	}
	end := start + maxLines
	if end > len(lines) {
		end = len(lines)
	}
	var b strings.Builder
	for i := start; i < end; i++ {
		txt := strings.TrimRight(lines[i], " \t\r")
		if len(txt) > maxEvidenceLineChars {
			txt = txt[:maxEvidenceLineChars] + "…"
		}
		fmt.Fprintf(&b, "    %d| %s\n", c.StartLine+i, txt)
	}
	return b.String()
}

// containing returns the SMALLEST chunk whose line range covers line. Chunk
// windows overlap (windowOverlap) and a small file is also emitted as one
// whole-file chunk, so several can match; the tightest one is the declaration
// the item actually belongs to rather than the file it happens to sit in.
func (ix *EvidenceIndex) containing(file string, line int) (core.Chunk, bool) {
	var best core.Chunk
	found := false
	for _, c := range ix.byFile[file] {
		if line < c.StartLine || line > c.EndLine {
			continue
		}
		if !found || (c.EndLine-c.StartLine) < (best.EndLine-best.StartLine) {
			best, found = c, true
		}
	}
	return best, found
}
