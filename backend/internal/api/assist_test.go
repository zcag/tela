package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/zcag/tela/backend/internal/agreement"
	"github.com/zcag/tela/backend/internal/llm"
	"github.com/zcag/tela/backend/internal/rag"
)

func TestRAGDraft_Grounded(t *testing.T) {
	ts, d, srv := newRagServer(t)
	srv.llm = llm.NewServiceWithCompleter(&fakeCompleter{answer: "# Deploying\n\nRun make deploy."})
	alice := seedUser(t, d, "alice", "alicepw12", false)
	sp := seedSpace(t, d, "Alpha", "alpha", alice)
	mustPage(t, d, sp, "Deploy Guide", "## Shipping\nrun make deploy to push the release to production")
	if _, _, err := rag.NewServiceWithEmbedder(d, fakeEmb{}).ReindexSpace(context.Background(), sp); err != nil {
		t.Fatalf("index: %v", err)
	}
	c := loginClient(t, ts, "alice", "alicepw12")
	bodyReq := `{"topic":"how we deploy","space_id":` + strconv.FormatInt(sp, 10) + `}`

	// Grounded draft + sources.
	resp, err := c.Post(ts.URL+"/api/rag/draft", "application/json", strings.NewReader(bodyReq))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("draft = %d body=%q", resp.StatusCode, rb)
	}
	var out struct {
		Draft   string    `json:"draft"`
		Sources []rag.Hit `json:"sources"`
	}
	if err := json.Unmarshal(rb, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Draft, "make deploy") {
		t.Errorf("draft missing canned content: %q", out.Draft)
	}
	if len(out.Sources) == 0 {
		t.Error("draft has no sources")
	}
}

func TestRAGAnswerToPage_CreatesCitedPage(t *testing.T) {
	ts, d, srv := newRagServer(t)
	srv.llm = llm.NewServiceWithCompleter(&fakeCompleter{answer: "You ship with make deploy."})
	alice := seedUser(t, d, "alice", "alicepw12", false)
	sp := seedSpace(t, d, "Alpha", "alpha", alice)
	src := mustPage(t, d, sp, "Deploy Guide", "## Shipping\nrun make deploy to push the release to production")
	if _, _, err := rag.NewServiceWithEmbedder(d, fakeEmb{}).ReindexSpace(context.Background(), sp); err != nil {
		t.Fatalf("index: %v", err)
	}
	c := loginClient(t, ts, "alice", "alicepw12")
	resp, err := c.Post(ts.URL+"/api/rag/answer-to-page", "application/json",
		strings.NewReader(`{"question":"how do I deploy","space_id":`+strconv.FormatInt(sp, 10)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("answer-to-page = %d body=%q", resp.StatusCode, rb)
	}
	var out struct {
		Page struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
		} `json:"page"`
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal(rb, &out); err != nil {
		t.Fatal(err)
	}
	if out.Page.ID == 0 {
		t.Fatal("no page created")
	}
	// The created page exists and carries the answer + a Sources section citing the source page.
	var body string
	if err := d.QueryRow(`SELECT body FROM pages WHERE id=$1`, out.Page.ID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "make deploy") || !strings.Contains(body, "## Sources") {
		t.Errorf("saved page body missing answer or sources: %q", body)
	}
	if !strings.Contains(body, "tela://page/"+strconv.FormatInt(src, 10)) {
		t.Errorf("saved page doesn't cite the source page %d: %q", src, body)
	}
}

func TestBuildAskContext_ExpandsHubAndFallsBack(t *testing.T) {
	// 6 pages in rank order. Pages 1–4 expand by rank; page 5 (rank 4, single
	// chunk) must fall back to its chunk; page 6 (LAST by rank but a dense hub —
	// the "kafka registry" shape) must still expand via the density rule.
	pageIDs := []int64{1, 2, 3, 4, 5, 6}
	order := make([]string, 0, len(pageIDs))
	best := map[string]rag.Hit{}
	bodies := map[int64]string{}
	contents := map[int64]string{}
	count := map[string]int{}
	for _, pid := range pageIDs {
		k := "p" + strconv.FormatInt(pid, 10)
		order = append(order, k)
		best[k] = rag.Hit{SourceKind: "page", PageID: pid, ChunkID: pid * 10, Title: "Page" + strconv.FormatInt(pid, 10),
			HeadingPath: "Sec" + strconv.FormatInt(pid, 10), Snippet: "snip"}
		bodies[pid] = "FULLBODY" + strconv.FormatInt(pid, 10) + " whole page text"
		contents[pid*10] = "CHUNK" + strconv.FormatInt(pid, 10) + " fragment"
		count[k] = 1
	}
	count["p6"] = askDenseChunks // page 6 is the dense hub despite ranking last

	block, pageHits := buildAskContext(order, best, count, bodies, contents, nil, askMaxPages)

	// Top-ranked pages expanded to full body.
	if !strings.Contains(block, "FULLBODY1") {
		t.Errorf("top page not expanded: %q", block)
	}
	// Page 5: not top-rank, not dense → chunk fallback, with heading path in header.
	if strings.Contains(block, "FULLBODY5") || !strings.Contains(block, "CHUNK5") {
		t.Errorf("page 5 should fall back to its chunk, got: %q", block)
	}
	if !strings.Contains(block, "Page5 — Sec5") {
		t.Errorf("chunk-fallback header should carry heading path: %q", block)
	}
	// Page 6: the density rescue — expanded to full body even though it ranks last.
	if !strings.Contains(block, "FULLBODY6") {
		t.Errorf("dense hub page (rank last) was not expanded — the table-rescue case: %q", block)
	}
	// Per-page hits align with the [n] numbering (one per page, in order).
	if len(pageHits) != 6 {
		t.Fatalf("want 6 page hits, got %d", len(pageHits))
	}
	for i, h := range pageHits {
		if h.PageID != pageIDs[i] {
			t.Errorf("pageHits[%d] = page %d, want %d", i, h.PageID, pageIDs[i])
		}
	}
}

func TestBoundSegments(t *testing.T) {
	cases := []struct {
		name string
		segs []string
		want string
	}{
		{"empty", nil, ""},
		{"single space", []string{"Eng"}, "Eng"},
		{"under cap", []string{"Eng", "Runbooks", "Deploy"}, "Eng › Runbooks › Deploy"},
		{"at cap", []string{"Eng", "A", "B", "C"}, "Eng › A › B › C"},
		{"over cap elides middle", []string{"Eng", "A", "B", "C", "D"}, "Eng › A › … › D"},
	}
	for _, c := range cases {
		if got := boundSegments(c.segs, askPathMaxSegments); got != c.want {
			t.Errorf("%s: boundSegments(%v) = %q, want %q", c.name, c.segs, got, c.want)
		}
	}
}

func TestBuildAskContext_LocationPrefix(t *testing.T) {
	// A page source and a file source, each with a "Space › path" location: the
	// label must read "loc › Title", and the file keeps its (file) marker after it.
	order := []string{"p1", "f2"}
	best := map[string]rag.Hit{
		"p1": {SourceKind: "page", PageID: 1, ChunkID: 10, Title: "Deploy", Snippet: "snip"},
		"f2": {SourceKind: "file", FileID: 2, ChunkID: 20, Title: "runbook.pdf", Snippet: "fsnip"},
	}
	count := map[string]int{"p1": 1, "f2": 1}
	contents := map[int64]string{10: "chunk text", 20: "file chunk text"}
	locations := map[string]string{"p1": "Eng › Runbooks", "f2": "Eng"}

	block, _ := buildAskContext(order, best, count, map[int64]string{}, contents, locations, askMaxPages)

	if !strings.Contains(block, "[1] Eng › Runbooks › Deploy") {
		t.Errorf("page label missing location prefix: %q", block)
	}
	if !strings.Contains(block, "[2] Eng › runbook.pdf (file)") {
		t.Errorf("file label missing location prefix or (file) marker: %q", block)
	}
	// A missing location degrades to the bare title (nil-safe).
	plain, _ := buildAskContext([]string{"p1"}, best, count, map[int64]string{}, contents, nil, askMaxPages)
	if !strings.Contains(plain, "[1] Deploy") || strings.Contains(plain, "›") {
		t.Errorf("nil locations should yield a bare title: %q", plain)
	}
}

func TestFormatConflicts(t *testing.T) {
	// Cited sources [1]=page10, [2]=page20, [3]=page30. A symmetric conflict between
	// 10 and 20 is recorded on BOTH rows; a one-sided conflict on 30 points at an
	// uncited page 99; a reasonless entry must be dropped.
	pageHits := []rag.Hit{
		{PageID: 10, Title: "Report Helper"},
		{PageID: 20, Title: "Domain Context"},
		{PageID: 30, Title: "Connector"},
	}
	byPage := map[int64][]agreement.Dispute{
		10: {{PageID: 20, Title: "Domain Context", Reason: "report port: 2480 vs 8444"}},
		20: {{PageID: 10, Title: "Report Helper", Reason: "report port: 8444 vs 2480"}}, // symmetric dup
		30: {
			{PageID: 99, Title: "Legacy Connector", Reason: "host: a vs b"}, // one-sided (99 not cited)
			{PageID: 12, Title: "No Reason Page", Reason: "  "},             // dropped: blank reason
		},
	}

	out := formatConflicts(pageHits, byPage)

	// The symmetric 10<->20 conflict appears exactly once, keyed to the first [n].
	if strings.Count(out, "report port") != 1 {
		t.Errorf("symmetric conflict should be de-duped to one line:\n%s", out)
	}
	if !strings.Contains(out, "[1] \"Report Helper\" may conflict with \"Domain Context\"") {
		t.Errorf("missing/mis-numbered symmetric conflict line:\n%s", out)
	}
	// The one-sided conflict (other page not retrieved) is still surfaced.
	if !strings.Contains(out, "[3] \"Connector\" may conflict with \"Legacy Connector\" — host: a vs b") {
		t.Errorf("one-sided conflict should be surfaced:\n%s", out)
	}
	// The reasonless entry is dropped.
	if strings.Contains(out, "No Reason Page") {
		t.Errorf("reasonless conflict should be dropped:\n%s", out)
	}

	// No disputes among cited pages → empty string (no header).
	if got := formatConflicts(pageHits, map[int64][]agreement.Dispute{}); got != "" {
		t.Errorf("want empty string when no conflicts, got %q", got)
	}
}

func TestLowConfidence(t *testing.T) {
	cases := []struct {
		name     string
		rerankOn bool
		top      float64
		want     bool
	}{
		{"strong query, rerank on", true, 3.3, false},
		{"answerable aggregate, rerank on", true, -0.2, false},
		{"out-of-scope, rerank on", true, -6.6, true},
		{"just over threshold", true, -4.1, true},
		{"just under threshold", true, -3.9, false},
		{"rerank off never flags (RRF scale differs)", false, -6.6, false},
	}
	for _, c := range cases {
		if got := lowConfidence(c.rerankOn, c.top); got != c.want {
			t.Errorf("%s: lowConfidence(%v, %.1f) = %v, want %v", c.name, c.rerankOn, c.top, got, c.want)
		}
	}
}

func TestRAGAsk_Followups(t *testing.T) {
	ts, d, srv := newRagServer(t)
	srv.llm = llm.NewServiceWithCompleter(&fakeCompleter{answer: "Use make deploy to ship."})
	alice := seedUser(t, d, "alice", "alicepw12", false)
	sp := seedSpace(t, d, "Alpha", "alpha", alice)
	mustPage(t, d, sp, "Deploy Guide", "## Shipping\nrun make deploy to push the release to production")
	if _, _, err := rag.NewServiceWithEmbedder(d, fakeEmb{}).ReindexSpace(context.Background(), sp); err != nil {
		t.Fatalf("index: %v", err)
	}
	c := loginClient(t, ts, "alice", "alicepw12")
	resp, err := c.Post(ts.URL+"/api/rag/ask", "application/json",
		strings.NewReader(`{"question":"how do I deploy","space_id":`+strconv.FormatInt(sp, 10)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	var out struct {
		Answer    string   `json:"answer"`
		Followups []string `json:"followups"`
	}
	if err := json.Unmarshal(rb, &out); err != nil {
		t.Fatal(err)
	}
	if out.Answer == "" {
		t.Fatal("no answer")
	}
	if len(out.Followups) == 0 {
		t.Error("expected follow-up questions")
	}
}

func TestFrontHubs(t *testing.T) {
	// p5 is content-dense (4 retrieved chunks) but not title-matched — the
	// "Architecture Overview" case. It must jump ahead of the lower-value pages
	// p1..p4 so it expands before the budget is spent. p2 is a title hub.
	order := []string{"p1", "p2", "p3", "p4", "p5"}
	count := map[string]int{"p1": 1, "p2": 1, "p3": 1, "p4": 1, "p5": 4}
	got := frontHubs(order, count, map[string]bool{"p2": true}, 3)
	want := []string{"p2", "p5", "p1", "p3", "p4"} // hubs first (stable), then the rest (stable)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("frontHubs = %v, want %v", got, want)
	}
	// No hubs → order unchanged.
	plain := frontHubs([]string{"p1", "p2"}, map[string]int{"p1": 1, "p2": 1}, nil, 3)
	if strings.Join(plain, ",") != "p1,p2" {
		t.Errorf("frontHubs (no hubs) = %v, want [p1 p2]", plain)
	}
}

// The render cap is a per-call number, not a hard 12. Before this, a caller's
// `limit` only deepened chunk retrieval and the cap silently clipped the render
// back to askMaxPages — which is why 79% of logged asks returned exactly 12.
func TestBuildAskContext_SourceCapIsPerCall(t *testing.T) {
	const sources = 20
	order := make([]string, 0, sources)
	best := map[string]rag.Hit{}
	contents := map[int64]string{}
	count := map[string]int{}
	for i := 1; i <= sources; i++ {
		k := "p" + strconv.Itoa(i)
		order = append(order, k)
		best[k] = rag.Hit{SourceKind: "page", PageID: int64(i), ChunkID: int64(i) * 10, Title: "Page" + strconv.Itoa(i)}
		contents[int64(i)*10] = "CHUNK" + strconv.Itoa(i)
		count[k] = 1
	}

	_, atDefault := buildAskContext(order, best, count, nil, contents, nil, askMaxPages)
	if len(atDefault) != askMaxPages {
		t.Fatalf("default render = %d sources, want %d", len(atDefault), askMaxPages)
	}
	if len(order) <= len(atDefault) {
		t.Fatal("test needs more retrieved sources than the cap renders")
	}

	block, raised := buildAskContext(order, best, count, nil, contents, nil, sources)
	if len(raised) != sources {
		t.Errorf("raised cap rendered %d sources, want %d", len(raised), sources)
	}
	// The [n] numbering must run to the raised count, not stop at the default.
	if !strings.Contains(block, "[20] Page20") {
		t.Errorf("20th source missing from the excerpt block: %q", block)
	}
}

// askResult.Truncated is the signal itself: it must fire exactly when retrieval
// found more than was rendered, and stay off when the corpus really did run out.
func TestAskResult_Truncated(t *testing.T) {
	hits := make([]rag.Hit, 12)
	for _, tc := range []struct {
		name       string
		considered int
		want       bool
	}{
		{"clipped by the cap", 24, true},
		{"corpus ran out", 12, false},
		{"nothing retrieved", 0, false},
	} {
		h := hits
		if tc.considered == 0 {
			h = nil
		}
		got := askResult{Hits: h, Considered: tc.considered}.Truncated()
		if got != tc.want {
			t.Errorf("%s: Truncated() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
