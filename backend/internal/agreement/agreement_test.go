package agreement

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/zcag/tela/backend/internal/rag"
	"github.com/zcag/tela/backend/internal/testdb"
)

func TestParseVerdicts(t *testing.T) {
	neighbors := []rag.Neighbor{
		{PageID: 10, Title: "Deploy runbook"},
		{PageID: 11, Title: "Old deploy notes"},
		{PageID: 12, Title: "Unrelated thing"},
		{PageID: 13, Title: "Backup policy"},
	}
	// Mixed, slightly messy output: a bracketed index, varied casing, a stray
	// preamble line, and a verdict the parser must ignore (unrelated).
	out := "Here are my classifications:\n" +
		"1|corroborate|both say deploy via make deploy\n" +
		"[2] | Contradict | says the old port 8080, target says 8780\n" +
		"3|unrelated|\n" +
		"4|CORROBORATE|backup cadence matches\n" +
		"7|contradict|out of range — must be ignored"

	corr, disp, disputes := parseVerdicts(out, neighbors)
	if corr != 2 {
		t.Fatalf("corroborate = %d, want 2", corr)
	}
	if disp != 1 {
		t.Fatalf("dispute = %d, want 1", disp)
	}
	if len(disputes) != 1 || disputes[0].PageID != 11 {
		t.Fatalf("disputes = %+v, want one for page 11", disputes)
	}
	if disputes[0].Reason == "" {
		t.Fatalf("dispute reason should be captured, got empty")
	}
}

func TestDisputesFor(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	var spaceID int64
	if err := d.QueryRow(`INSERT INTO spaces (name, slug) VALUES ('s','s') RETURNING id`).Scan(&spaceID); err != nil {
		t.Fatalf("space: %v", err)
	}
	mk := func(title string) int64 {
		var id int64
		if err := d.QueryRow(`INSERT INTO pages (space_id, title, body) VALUES ($1,$2,'x') RETURNING id`, spaceID, title).Scan(&id); err != nil {
			t.Fatalf("page %s: %v", title, err)
		}
		return id
	}
	a, b, c := mk("A"), mk("B"), mk("C")
	// A has a clean dispute against B; C has a FAILED row (must be excluded).
	if _, err := d.Exec(`INSERT INTO page_agreement (page_id, src_hash, model, dispute, disputes, last_error)
		VALUES ($1,'h','m',1,$2,'')`, a, fmt.Sprintf(`[{"page_id":%d,"title":"B","reason":"port 1 vs 2"}]`, b)); err != nil {
		t.Fatalf("seed A: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO page_agreement (page_id, src_hash, model, dispute, disputes, last_error)
		VALUES ($1,'','m',0,'[]','boom')`, c); err != nil {
		t.Fatalf("seed C: %v", err)
	}

	got, err := (&Service{db: d}).DisputesFor(ctx, []int64{a, b, c})
	if err != nil {
		t.Fatalf("DisputesFor: %v", err)
	}
	if len(got[a]) != 1 || got[a][0].PageID != b || got[a][0].Reason != "port 1 vs 2" {
		t.Fatalf("A's disputes = %+v, want one for B", got[a])
	}
	if _, ok := got[c]; ok {
		t.Errorf("failed row (last_error set) must be excluded, got %+v", got[c])
	}
	if _, ok := got[b]; ok {
		t.Errorf("B has no row, must be absent, got %+v", got[b])
	}
}

func TestParseVerdictsEmpty(t *testing.T) {
	corr, disp, disputes := parseVerdicts("", []rag.Neighbor{{PageID: 1}})
	if corr != 0 || disp != 0 || len(disputes) != 0 {
		t.Fatalf("empty output should yield zero verdicts, got %d/%d/%d", corr, disp, len(disputes))
	}
}

// A clamped excerpt must never end in a bare mid-token prefix: that is what made
// a page stating "…2026-09-04T06:15:32Z" near the budget read as stating "202",
// which the model then named as a conflicting value against a page saying the
// same thing.
func TestClampMarksTruncationAtLineBoundary(t *testing.T) {
	body := "Cursor registry\n\nFND-1671 stamped 2026-09-04T06:15:32Z, concluded NO WRITE.\n"
	got := clamp(body, 40) // lands mid-timestamp
	if strings.Contains(got, "2026-09-04T06") || strings.HasSuffix(strings.TrimSuffix(got, truncMarker), "202") {
		t.Fatalf("clamp kept a bisected value: %q", got)
	}
	if !strings.HasSuffix(got, truncMarker) {
		t.Fatalf("clamp did not mark the cut: %q", got)
	}
	if !strings.HasPrefix(got, "Cursor registry") {
		t.Fatalf("clamp dropped the head of the body: %q", got)
	}

	// A single line longer than the budget has no break to fall back to: hard cut,
	// still marked.
	long := clamp(strings.Repeat("x", 200), 50)
	if !strings.HasSuffix(long, truncMarker) {
		t.Fatalf("unbroken line lost its marker: %q", long)
	}
	if n := len([]rune(strings.TrimSuffix(long, truncMarker))); n != 50 {
		t.Fatalf("unbroken line cut to %d runes, want 50", n)
	}

	// Under budget: untouched, no marker.
	if got := clamp("short body", 500); got != "short body" {
		t.Fatalf("clamp altered an under-budget body: %q", got)
	}
}
