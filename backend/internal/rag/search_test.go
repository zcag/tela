package rag

import (
	"context"
	"database/sql"
	"errors"
	"hash/fnv"
	"math"
	"strings"
	"testing"

	"github.com/zcag/tela/backend/internal/testdb"
)

// fakeEmbedder maps text to a deterministic L2-normalised 1024-d bag-of-words
// vector, so cosine similarity tracks word overlap. Enough to exercise the
// vector-rank plumbing + RRF fusion deterministically (real semantic quality is
// verified live against Ollama, not here). embedDim matches the page_chunks
// vector(1024) column. calls counts Embed invocations for the cache-reuse test.
type fakeEmbedder struct{ calls int }

func (f *fakeEmbedder) Model() string { return "fake-test" }

func (f *fakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	f.calls++
	v := make([]float32, 1024)
	for _, w := range strings.Fields(strings.ToLower(text)) {
		h := fnv.New32a()
		h.Write([]byte(w))
		v[h.Sum32()%1024]++
	}
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	if norm > 0 {
		n := float32(math.Sqrt(norm))
		for i := range v {
			v[i] /= n
		}
	}
	return v, nil
}

func newUser(t *testing.T, d *sql.DB, name string) int64 {
	t.Helper()
	var id int64
	if err := d.QueryRow(
		`INSERT INTO users (username, password_hash) VALUES ($1, 'x') RETURNING id`, name,
	).Scan(&id); err != nil {
		t.Fatalf("insert user %s: %v", name, err)
	}
	return id
}

func newSpace(t *testing.T, d *sql.DB, slug string, owner int64) int64 {
	t.Helper()
	var id int64
	if err := d.QueryRow(
		`INSERT INTO spaces (name, slug) VALUES ($1, $1) RETURNING id`, slug,
	).Scan(&id); err != nil {
		t.Fatalf("insert space %s: %v", slug, err)
	}
	if _, err := d.Exec(
		`INSERT INTO space_members (space_id, user_id, role) VALUES ($1, $2, 'owner')`, id, owner,
	); err != nil {
		t.Fatalf("add member: %v", err)
	}
	return id
}

// addAccess grants an existing user membership of an existing space (editor), so
// a test can build the "shared space" shape without a second owner.
func addAccess(t *testing.T, d *sql.DB, spaceID, userID int64) {
	t.Helper()
	if _, err := d.Exec(
		`INSERT INTO space_members (space_id, user_id, role) VALUES ($1, $2, 'editor')`, spaceID, userID,
	); err != nil {
		t.Fatalf("add access: %v", err)
	}
}

func newPage(t *testing.T, d *sql.DB, spaceID int64, title, body string) int64 {
	t.Helper()
	var id int64
	if err := d.QueryRow(
		`INSERT INTO pages (space_id, title, body) VALUES ($1, $2, $3) RETURNING id`, spaceID, title, body,
	).Scan(&id); err != nil {
		t.Fatalf("insert page %q: %v", title, err)
	}
	return id
}

func TestSearch_HybridAndScoping(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()

	alice := newUser(t, d, "alice")
	bob := newUser(t, d, "bob")
	s1 := newSpace(t, d, "alpha", alice)
	s2 := newSpace(t, d, "bravo", bob) // alice is NOT a member of s2

	newPage(t, d, s1, "Deployment Runbook",
		"## Shipping\nRun make deploy to push the release to production. The health gate polls the version endpoint.")
	newPage(t, d, s1, "Onboarding",
		"## Welcome\nSet up your laptop, clone the repo, and read the style guide.")
	// A page in bob's space that should NEVER surface for alice.
	secret := newPage(t, d, s2, "Secret Roadmap",
		"## Deploy plans\nThe deployment roadmap and release schedule for next quarter.")

	emb := &fakeEmbedder{}
	svc := NewServiceWithEmbedder(d, emb)

	if _, _, err := svc.ReindexSpace(ctx, s1); err != nil {
		t.Fatalf("reindex s1: %v", err)
	}
	if _, _, err := svc.ReindexSpace(ctx, s2); err != nil {
		t.Fatalf("reindex s2: %v", err)
	}

	// Hybrid search as alice for "deploy release" must find the runbook and
	// must NOT leak bob's secret page (permission scope via the live page row).
	hits, err := svc.Search(ctx, alice, "deploy release", nil, 10, "hybrid")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hits, got none")
	}
	for _, h := range hits {
		if h.PageID == secret {
			t.Fatalf("LEAK: alice retrieved bob's page %d", secret)
		}
		if h.SpaceID != s1 {
			t.Errorf("hit from unexpected space %d", h.SpaceID)
		}
	}
	if top := hits[0]; !strings.Contains(top.Title, "Deployment") {
		t.Errorf("top hit = %q, expected the Deployment Runbook", top.Title)
	}
	if hits[0].UpdatedAt == "" {
		t.Error("hit missing updated_at (freshness signal)")
	}

	// Bob CAN see his own page.
	bobHits, err := svc.Search(ctx, bob, "deployment roadmap", nil, 10, "hybrid")
	if err != nil {
		t.Fatalf("bob search: %v", err)
	}
	foundSecret := false
	for _, h := range bobHits {
		if h.PageID == secret {
			foundSecret = true
		}
	}
	if !foundSecret {
		t.Error("bob should retrieve his own page")
	}
}

func TestSearch_Modes(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	u := newUser(t, d, "carol")
	sp := newSpace(t, d, "charlie", u)
	newPage(t, d, sp, "Kubernetes Notes", "## Pods\nA pod is the smallest deployable unit in kubernetes.")

	svc := NewServiceWithEmbedder(d, &fakeEmbedder{})
	if _, _, err := svc.ReindexSpace(ctx, sp); err != nil {
		t.Fatalf("reindex: %v", err)
	}

	for _, mode := range []string{"hybrid", "lexical", "semantic"} {
		hits, err := svc.Search(ctx, u, "kubernetes pod", nil, 5, mode)
		if err != nil {
			t.Fatalf("%s search: %v", mode, err)
		}
		if len(hits) == 0 {
			t.Errorf("%s search returned no hits", mode)
		}
	}
}

func TestReadChunk_FoundAndScoped(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	alice := newUser(t, d, "alice")
	bob := newUser(t, d, "bob")
	s1 := newSpace(t, d, "alpha", alice)
	s2 := newSpace(t, d, "bravo", bob) // alice not a member

	newPage(t, d, s1, "Runbook", "## Deploy\nrun make deploy to ship to production")
	newPage(t, d, s2, "Secret", "## Plans\nthe secret roadmap")

	svc := NewServiceWithEmbedder(d, &fakeEmbedder{})
	if _, _, err := svc.ReindexSpace(ctx, s1); err != nil {
		t.Fatalf("reindex s1: %v", err)
	}
	if _, _, err := svc.ReindexSpace(ctx, s2); err != nil {
		t.Fatalf("reindex s2: %v", err)
	}

	// Find a chunk alice can see, then read it back in full.
	hits, err := svc.Search(ctx, alice, "deploy", nil, 5, "hybrid")
	if err != nil || len(hits) == 0 {
		t.Fatalf("search: %v (hits=%d)", err, len(hits))
	}
	got, err := svc.ReadChunk(ctx, alice, hits[0].ChunkID, nil)
	if err != nil {
		t.Fatalf("read own chunk: %v", err)
	}
	if got.PageID != hits[0].PageID || got.Content == "" {
		t.Errorf("read chunk mismatch: %+v", got)
	}
	if got.SpaceID != s1 {
		t.Errorf("chunk space = %d, want %d", got.SpaceID, s1)
	}

	// Find bob's chunk id (as bob), then confirm alice CANNOT read it.
	bobHits, err := svc.Search(ctx, bob, "secret roadmap", nil, 5, "hybrid")
	if err != nil || len(bobHits) == 0 {
		t.Fatalf("bob search: %v", err)
	}
	if _, err := svc.ReadChunk(ctx, alice, bobHits[0].ChunkID, nil); !errors.Is(err, ErrChunkNotFound) {
		t.Fatalf("LEAK: alice read bob's chunk %d (err=%v)", bobHits[0].ChunkID, err)
	}

	// Missing id → not found, not a different error.
	if _, err := svc.ReadChunk(ctx, alice, 999999, nil); !errors.Is(err, ErrChunkNotFound) {
		t.Errorf("missing chunk: err=%v, want ErrChunkNotFound", err)
	}
}

// TestLexicalRank_ORRecall pins the recall fix: a doc must surface lexically when
// the query shares SOME terms with it, even though other query terms are absent.
// AND-matching (the old plainto_tsquery) returned nothing here, starving the
// candidate pool; OR-matching retrieves it on the overlapping terms.
func TestLexicalRank_ORRecall(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	u := newUser(t, d, "alice")
	sp := newSpace(t, d, "alpha", u)
	page := newPage(t, d, sp, "Orbital Mesh SLA",
		"## Terms\nSeverity 1 first response within 15 minutes. Egress billed per gigabyte.")

	svc := NewServiceWithEmbedder(d, &fakeEmbedder{})
	if _, _, err := svc.ReindexSpace(ctx, sp); err != nil {
		t.Fatalf("index: %v", err)
	}

	// Query mixes doc terms (severity, egress, response) with words ABSENT from the
	// doc (whats, the, time, price). AND-matching needs all → zero rows; OR-matching
	// retrieves the chunk on the shared terms.
	ids, err := svc.lexicalRank(ctx, u, "whats the severity 1 response time and egress price", nil, 40)
	if err != nil {
		t.Fatalf("lexicalRank: %v", err)
	}
	if len(ids) == 0 {
		t.Fatal("OR-recall failed: no lexical candidates for a partially-overlapping query")
	}
	var chunk int64
	d.QueryRow(`SELECT id FROM page_chunks WHERE page_id = $1 LIMIT 1`, page).Scan(&chunk)
	found := false
	for _, id := range ids {
		if id == chunk {
			found = true
		}
	}
	if !found {
		t.Errorf("OR-recall: relevant chunk %d not among lexical candidates %v", chunk, ids)
	}
}

func TestReindex_ReusesCachedVectors(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	u := newUser(t, d, "dave")
	sp := newSpace(t, d, "delta", u)
	newPage(t, d, sp, "Doc", "## A\nalpha content\n\n## B\nbravo content")

	emb := &fakeEmbedder{}
	svc := NewServiceWithEmbedder(d, emb)

	if _, _, err := svc.ReindexSpace(ctx, sp); err != nil {
		t.Fatalf("first reindex: %v", err)
	}
	first := emb.calls
	if first == 0 {
		t.Fatal("expected embed calls on first index")
	}

	// Re-indexing unchanged content must reuse every cached vector → 0 new
	// embed calls (content_hash hit path).
	if _, _, err := svc.ReindexSpace(ctx, sp); err != nil {
		t.Fatalf("second reindex: %v", err)
	}
	if emb.calls != first {
		t.Errorf("re-embedded on unchanged content: %d new calls (want 0)", emb.calls-first)
	}
}
