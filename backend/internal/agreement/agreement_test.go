package agreement

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/zcag/tela/backend/internal/testdb"
)

func TestParsePairVerdict(t *testing.T) {
	cases := []struct{ name, out, want string }{
		{"plain", "contradict|report service port|8090|2480|two ports for one service", "contradict"},
		{"header echoed back", "verdict|contradict|port|8090|2480|two ports", "contradict"},
		{"preamble and a fence", "Here is my answer:\n```\nagree|auth backend|||same LDAP server\n```", "agree"},
		{"empty value fields", "neutral|kafka setup|||different components", "neutral"},
		{"unreadable", "I could not compare these passages.", ""},
	}
	for _, c := range cases {
		if got := parsePairVerdict(c.out).Verdict; got != c.want {
			t.Errorf("%s: verdict = %q, want %q", c.name, got, c.want)
		}
	}

	v := parsePairVerdict("contradict|report service port|8090|2480|two ports for one service")
	if v.ValueA != "8090" || v.ValueB != "2480" || v.Subject != "report service port" {
		t.Fatalf("fields not split: %+v", v)
	}
	if want := "report service port: 8090 vs 2480 — two ports for one service"; v.Reason() != want {
		t.Fatalf("Reason() = %q, want %q", v.Reason(), want)
	}
}

// The model is ordered to quote two conflicting values, so it produces a pair
// whether or not it has one. Each case here is a shape seen on the live corpus.
func TestUnverifiedPair(t *testing.T) {
	const a = "Report service on port 8090. FND-1671 stamped 2026-09-04T06:15:32Z. Scope: Solution. A 13 track list."
	const b = "Report service port 2480. Booking call on 2017-09-09. Scope: Evidence-based solution. A 16 track list."

	drop := []struct {
		name string
		v    pairVerdict
	}{
		{"no values quoted", pairVerdict{Subject: "port", ValueA: "", ValueB: "2480"}},
		{"same value both sides", pairVerdict{Subject: "date", ValueA: "2017-09-09", ValueB: "2017-09-09"}},
		{"same value, different wording", pairVerdict{Subject: "duration", ValueA: "three hours per day", ValueB: "three hours/day"}},
		{"a refinement, not a conflict", pairVerdict{Subject: "scope", ValueA: "Solution", ValueB: "Evidence-based solution"}},
		{"value invented for A", pairVerdict{Subject: "port", ValueA: "9999", ValueB: "2480"}},
		{"value invented for B", pairVerdict{Subject: "port", ValueA: "8090", ValueB: "7777"}},
		{"a truncated value", pairVerdict{Subject: "timestamp", ValueA: "202", ValueB: "2026-09-04T06:15:32Z"}},
	}
	for _, c := range drop {
		if why := unverifiedPair(c.v, a, b); why == "" {
			t.Errorf("%s: should have been dropped, was kept: %+v", c.name, c.v)
		}
	}

	keep := []struct {
		name string
		v    pairVerdict
	}{
		{"two real values", pairVerdict{Subject: "report service port", ValueA: "8090", ValueB: "2480"}},
		{"same words, different number", pairVerdict{Subject: "tracks", ValueA: "13 track list", ValueB: "16 track list"}},
		{"quoted long spans", pairVerdict{Subject: "date", ValueA: "FND-1671 stamped 2026-09-04T06:15:32Z", ValueB: "Booking call on 2017-09-09"}},
	}
	for _, c := range keep {
		if why := unverifiedPair(c.v, a, b); why != "" {
			t.Errorf("%s: should have been kept, dropped as %q", c.name, why)
		}
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

// A field that identifies the page — a project code, a media id — is not a shared
// fact, and every page having its own is not a disagreement. The tell is one page
// raising the same subject against several neighbours with its own value constant.
func TestDropIdentityFields(t *testing.T) {
	mk := func(nb int64, subject, a, b string) candidate {
		v := pairVerdict{Subject: subject, ValueA: a, ValueB: b}
		return candidate{Dispute{PageID: nb, Title: "n", Reason: v.Reason()}, v}
	}
	got := dropIdentityFields([]candidate{
		// An identity field: one subject, my value fixed, three different theirs.
		mk(11, "Mã CSE", "HK261-DAGD1-369", "HK261-DAGD1-365"),
		mk(12, "Mã CSE", "HK261-DAGD1-369", "HK261-DAGD1-234"),
		mk(13, "Mã CSE", "HK261-DAGD1-369", "HK261-DAGD1-233"),
		// Two real conflicts from one page, under different subjects.
		mk(14, "payroll document-holder", "Jutta A. Groß-Holler", "Bianca Kummrow"),
		mk(15, "payroll accountant", "Jutta A. Groß-Holler", "Steffen Haja"),
		// One subject twice, but this page states a different value each time —
		// a real clash, not an identity.
		mk(16, "service port", "8485", "8484"),
		mk(17, "service port", "8484", "8480"),
	}, 1)
	kept := map[int64]bool{}
	for _, d := range got {
		kept[d.PageID] = true
	}
	for _, id := range []int64{11, 12, 13} {
		if kept[id] {
			t.Errorf("identity-field conflict against %d should have been dropped", id)
		}
	}
	for _, id := range []int64{14, 15, 16, 17} {
		if !kept[id] {
			t.Errorf("real conflict against %d should have been kept", id)
		}
	}
	if len(got) != 4 {
		t.Fatalf("kept %d conflicts, want 4", len(got))
	}
}
