package store

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/zcag/tela/backend/internal/atlas/core"
	"github.com/zcag/tela/backend/internal/testdb"
)

// latin1Line is a real-world CP1252/Latin-1 fragment: `0xd3 0xeb` is the byte
// pair that killed every run of atlas source 19 (laravel/framework) for three
// weeks. It has no NUL byte, so the binary filter passes it through as ordinary
// text and it reaches Postgres as-is.
var latin1Line = []byte("// Se\xd3\xeb or: \xe9\xe8\xf1 caf\xe9\n")

// TestSaveChunks_RejectsRawLatin1 pins the failure this guards against: handing
// Postgres non-UTF-8 bytes fails the whole INSERT (SQLSTATE 22021), which is why
// one bad file in a 3,500-file repo failed the entire run — and, since a run
// re-clones and re-chunks from scratch, failed it identically on every retry
// forever. If this ever stops failing, the sanitiser below is no longer the
// thing standing between a repo and a permanently broken source.
func TestSaveChunks_RejectsRawLatin1(t *testing.T) {
	d := testdb.New(t)
	st := New(d)
	_, runID := fixture(t, d)

	err := st.SaveChunks(runID, []core.Chunk{
		{File: "bad.php", StartLine: 1, EndLine: 1, Kind: core.ChunkFile, Text: string(latin1Line)},
	})
	if err == nil {
		t.Fatal("expected Postgres to reject invalid UTF-8; it did not — re-check whether core.SourceText is still load-bearing")
	}
	if !strings.Contains(err.Error(), "invalid byte sequence") {
		t.Fatalf("expected an encoding error, got: %v", err)
	}
}

// TestSaveChunks_SourceTextMakesLatin1Storable is the fix: the same bytes routed
// through core.SourceText store cleanly, keeping the file's readable content
// instead of dropping the file or failing the run.
func TestSaveChunks_SourceTextMakesLatin1Storable(t *testing.T) {
	d := testdb.New(t)
	st := New(d)
	_, runID := fixture(t, d)

	text := core.SourceText(latin1Line)
	if !utf8.ValidString(text) {
		t.Fatalf("SourceText returned invalid UTF-8: %q", text)
	}
	if err := st.SaveChunks(runID, []core.Chunk{
		{File: "bad.php", StartLine: 1, EndLine: 1, Kind: core.ChunkFile, Text: text},
	}); err != nil {
		t.Fatalf("SaveChunks with sanitised text: %v", err)
	}

	got, err := st.RunChunksWithVectors(runID)
	if err != nil {
		t.Fatalf("RunChunksWithVectors: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk back, got %d", len(got))
	}
	// The surrounding text survives — only the undecodable bytes are replaced.
	if !strings.Contains(got[0].Text, "// Se") || !strings.Contains(got[0].Text, " caf") {
		t.Fatalf("readable content did not survive sanitising: %q", got[0].Text)
	}
}
