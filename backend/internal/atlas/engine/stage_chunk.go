package engine

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zcag/tela/backend/internal/atlas/core"
)

const (
	smallFileLines = 120 // below this, one whole-file chunk
	maxChunkLines  = 160 // larger spans get window-split
	windowOverlap  = 20
)

// chunkStage turns files into retrievable chunks, symbol-aware where possible so
// a chunk is a coherent unit (a function, a type, a doc section) and carries the
// exact file+line range it came from — the basis for verifiable citations.
type chunkStage struct{}

func (chunkStage) Name() core.StageName { return core.StageChunk }

func (chunkStage) Run(ctx context.Context, rc *RunContext) error {
	// Delta path: reuse the baseline's chunks+vectors for unchanged files and
	// re-chunk only the changed ones. Guarded — on any problem (or an embed-model
	// change that makes copied vectors incompatible) it falls through to a full
	// re-chunk, since correctness beats speed.
	if reused, ok := chunkDelta(ctx, rc); ok {
		return reused
	}

	chunks, err := chunkFiles(ctx, rc, rc.Art.Files)
	if err != nil {
		return err
	}
	rc.Art.Chunks = chunks
	if err := rc.Store.SaveChunks(rc.Run.ID, chunks); err != nil {
		return err
	}
	rc.Info("produced %d chunks (avg %d lines)", len(chunks), avgLines(chunks))
	return nil
}

// chunkFiles chunks the given files (reading each from the run's repo dir),
// emitting periodic progress. Shared by the full path and the delta path's
// changed-files subset.
func chunkFiles(ctx context.Context, rc *RunContext, files []core.File) ([]core.Chunk, error) {
	var chunks []core.Chunk
	for i, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		src, err := os.ReadFile(filepath.Join(rc.Art.RepoDir, f.Path))
		if err != nil {
			continue
		}
		chunks = append(chunks, chunkFile(f, core.SourceText(src))...)
		if (i+1)%50 == 0 || i+1 == len(files) {
			rc.Step(i+1, len(files), "chunking")
		}
	}
	return chunks, nil
}

// chunkDelta is the cheap delta path. It returns (err, true) when it handled the
// stage (reuse taken), or (_, false) to signal the caller to do a full re-chunk
// (delta not applicable, or guarded off). It only engages for a delta run with a
// baseline + changeset, and bails to full when the baseline's embed model
// differs (copied vectors would be dimension/space-incompatible).
func chunkDelta(ctx context.Context, rc *RunContext) (error, bool) {
	r := rc.Run
	if r.Kind != core.RunDelta || r.BaselineID == 0 || r.ChangeSet == nil {
		return nil, false
	}

	// Model-change guard: copied vectors are only valid under the same embedder.
	base, err := rc.Store.GetRun(r.BaselineID)
	if err != nil || base == nil || base.Stats == nil {
		rc.Info("delta reuse skipped (baseline %d stats unavailable) — full re-chunk", r.BaselineID)
		return nil, false
	}
	curModel := rc.Project.Model.EmbedModel
	if base.Stats.EmbedModel != curModel {
		rc.Info("delta reuse skipped (embed model changed %q → %q) — full re-chunk",
			base.Stats.EmbedModel, curModel)
		return nil, false
	}

	// Files touched since the baseline (re-chunk these); everything else is copied.
	cs := r.ChangeSet
	changed := map[string]bool{}
	for _, f := range cs.Added {
		changed[f] = true
	}
	for _, f := range cs.Modified {
		changed[f] = true
	}
	for _, f := range cs.Deleted {
		changed[f] = true
	}

	var reuseFiles []string
	var rechunk []core.File
	for _, f := range rc.Art.Files {
		if changed[f.Path] {
			rechunk = append(rechunk, f)
		} else {
			reuseFiles = append(reuseFiles, f.Path)
		}
	}

	// Copy-forward unchanged files' chunks+vectors from the baseline run.
	copied, err := rc.Store.CopyChunksToRun(r.BaselineID, r.ID, reuseFiles)
	if err != nil {
		rc.Warn("delta copy-forward failed (%v) — full re-chunk", err)
		return nil, false
	}
	// Reload the copied rows so rc.Art.Chunks carries their new run-scoped IDs
	// (vectors intact) — the embed stage skips these, index builds over them.
	reused, err := rc.Store.RunChunksWithVectors(r.ID)
	if err != nil {
		rc.Warn("delta reload failed (%v) — full re-chunk", err)
		return nil, false
	}

	// Re-chunk only the changed files; persist just the new (vector-less) rows.
	fresh, err := chunkFiles(ctx, rc, rechunk)
	if err != nil {
		return err, true
	}
	if err := rc.Store.SaveChunks(r.ID, fresh); err != nil {
		return err, true
	}

	rc.Art.Chunks = append(reused, fresh...)
	rc.Info("reused %d chunks from baseline · re-chunked %d files (%d new chunks)",
		copied, len(rechunk), len(fresh))
	return nil, true
}

func chunkFile(f core.File, src string) []core.Chunk {
	if f.Lines <= smallFileLines {
		return []core.Chunk{{File: f.Path, StartLine: 1, EndLine: f.Lines, Kind: core.ChunkFile, Text: src}}
	}
	switch f.Lang {
	case core.LangGo:
		return chunkGo(f, src)
	case core.LangMarkdown:
		return chunkDoc(f, src)
	default:
		if re := declRe[f.Lang]; re != nil {
			return chunkByBoundaries(f, src, re)
		}
		return chunkWindow(f, src, 1)
	}
}

func chunkGo(f core.File, src string) []core.Chunk {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, f.Path, src, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		return chunkWindow(f, src, 1)
	}
	lines := strings.Split(src, "\n")
	var out []core.Chunk
	prevEnd := 0
	for _, d := range file.Decls {
		s := fset.Position(d.Pos()).Line
		e := fset.Position(d.End()).Line
		if s > prevEnd+1 { // header / inter-decl gap (package, imports, comments)
			out = append(out, span(f, lines, prevEnd+1, s-1, core.ChunkFile, ""))
		}
		out = appendSpan(out, f, lines, s, e, declName(src, s))
		prevEnd = e
	}
	if prevEnd < len(lines) {
		out = append(out, span(f, lines, prevEnd+1, len(lines), core.ChunkFile, ""))
	}
	return compact(out)
}

var declRe = map[core.Lang]*regexp.Regexp{
	core.LangPython: regexp.MustCompile(`(?m)^(?:async\s+)?(?:def|class)\s`),
	core.LangJava:   regexp.MustCompile(`(?m)^\s*(?:public|private|protected)\s+.*?(?:class|interface|enum|void|[A-Z]\w+)\s`),
	core.LangJS:     regexp.MustCompile(`(?m)^(?:export\s+)?(?:async\s+)?(?:function|class|const|let)\s`),
	core.LangTS:     regexp.MustCompile(`(?m)^(?:export\s+)?(?:async\s+)?(?:function|class|const|let|interface|type)\s`),
	core.LangRust:   regexp.MustCompile(`(?m)^\s*(?:pub\s+)?(?:fn|struct|enum|trait|impl|mod)\s`),
	core.LangRuby:   regexp.MustCompile(`(?m)^\s*(?:def|class|module)\s`),
}

// chunkByBoundaries splits at top-level declaration starts found by re.
func chunkByBoundaries(f core.File, src string, re *regexp.Regexp) []core.Chunk {
	lines := strings.Split(src, "\n")
	starts := []int{1}
	for _, loc := range re.FindAllStringIndex(src, -1) {
		ln := lineAt(src, loc[0])
		if ln > 1 {
			starts = append(starts, ln)
		}
	}
	starts = uniqSortedInts(starts)
	var out []core.Chunk
	for i, s := range starts {
		e := len(lines)
		if i+1 < len(starts) {
			e = starts[i+1] - 1
		}
		out = appendSpan(out, f, lines, s, e, "")
	}
	return compact(out)
}

// chunkDoc splits markdown at top-level/section headings.
func chunkDoc(f core.File, src string) []core.Chunk {
	lines := strings.Split(src, "\n")
	var starts []int
	for i, ln := range lines {
		if strings.HasPrefix(ln, "# ") || strings.HasPrefix(ln, "## ") {
			starts = append(starts, i+1)
		}
	}
	if len(starts) == 0 || starts[0] != 1 {
		starts = append([]int{1}, starts...)
	}
	starts = uniqSortedInts(starts)
	var out []core.Chunk
	for i, s := range starts {
		e := len(lines)
		if i+1 < len(starts) {
			e = starts[i+1] - 1
		}
		out = appendSpan(out, f, lines, s, e, "")
	}
	for i := range out {
		out[i].Kind = core.ChunkDoc
	}
	return compact(out)
}

// chunkWindow is the fallback: fixed line windows with overlap.
func chunkWindow(f core.File, src string, _ int) []core.Chunk {
	lines := strings.Split(src, "\n")
	var out []core.Chunk
	for s := 1; s <= len(lines); s += maxChunkLines - windowOverlap {
		e := min(s+maxChunkLines-1, len(lines))
		out = append(out, span(f, lines, s, e, core.ChunkFile, ""))
		if e == len(lines) {
			break
		}
	}
	return out
}

// appendSpan adds a span, window-splitting it if it exceeds maxChunkLines.
func appendSpan(out []core.Chunk, f core.File, lines []string, s, e int, sym string) []core.Chunk {
	if e < s {
		return out
	}
	if e-s+1 <= maxChunkLines {
		return append(out, span(f, lines, s, e, core.ChunkDecl, sym))
	}
	for ws := s; ws <= e; ws += maxChunkLines - windowOverlap {
		we := min(ws+maxChunkLines-1, e)
		out = append(out, span(f, lines, ws, we, core.ChunkDecl, sym))
		if we == e {
			break
		}
	}
	return out
}

func span(f core.File, lines []string, s, e int, kind core.ChunkKind, sym string) core.Chunk {
	if s < 1 {
		s = 1
	}
	if e > len(lines) {
		e = len(lines)
	}
	return core.Chunk{File: f.Path, StartLine: s, EndLine: e, Kind: kind, Symbol: sym,
		Text: strings.Join(lines[s-1:e], "\n")}
}

// compact drops chunks that are only whitespace.
func compact(in []core.Chunk) []core.Chunk {
	out := in[:0]
	for _, c := range in {
		if strings.TrimSpace(c.Text) != "" {
			out = append(out, c)
		}
	}
	return out
}

var goDeclName = regexp.MustCompile(`^(?:func(?:\s*\([^)]*\))?\s+|type\s+|var\s+|const\s+)(\w+)`)

func declName(src string, line int) string {
	lines := strings.Split(src, "\n")
	if line-1 < len(lines) {
		if m := goDeclName.FindStringSubmatch(strings.TrimSpace(lines[line-1])); m != nil {
			return m[1]
		}
	}
	return ""
}

func uniqSortedInts(in []int) []int {
	seen := map[int]bool{}
	var out []int
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for i := 1; i < len(out); i++ { // insertion sort (small slices)
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

func avgLines(cs []core.Chunk) int {
	if len(cs) == 0 {
		return 0
	}
	t := 0
	for _, c := range cs {
		t += c.EndLine - c.StartLine + 1
	}
	return t / len(cs)
}
