package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zcag/tela/backend/internal/auth"
	"github.com/zcag/tela/backend/internal/rag"
)

// bearerRoundTripper injects a tela PAT on every request the MCP client makes,
// so the Streamable-HTTP transport authenticates against /api/mcp.
type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (b bearerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

// TestMCP_SpikeListSpaces is the Phase-0 spike: it drives the real MCP Go SDK
// client over Streamable HTTP against the wired backend, authenticates with a
// tela PAT, and asserts the list_spaces tool returns the caller's spaces as
// structured output. This proves transport + bearer-verifier + identity
// threading + typed output end-to-end.
func TestMCP_SpikeListSpaces(t *testing.T) {
	ts, d := newWiredServer(t)

	alice := seedUser(t, d, "alice", "alicepw12", false)
	bob := seedUser(t, d, "bob", "bobpw1234", false)
	seedSpace(t, d, "Alice Space", "alice-space", alice)
	seedSpace(t, d, "Bob Space", "bob-space", bob) // not alice's — must not leak

	// Mint an unrestricted read PAT for alice.
	raw, prefix, _, err := auth.NewAPIKey(auth.LoadAPIKeySecret())
	if err != nil {
		t.Fatalf("new api key: %v", err)
	}
	hmacHex := auth.HMACAPIKey(auth.LoadAPIKeySecret(), raw)
	if _, err := d.ExecContext(context.Background(), `
		INSERT INTO api_keys (user_id, name, key_prefix, key_hmac, scope, space_id)
		VALUES ($1, 'mcp', $2, $3, $4, NULL)`,
		alice, prefix, hmacHex, auth.ScopeRead); err != nil {
		t.Fatalf("seed key: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	transport := &mcp.StreamableClientTransport{
		Endpoint: ts.URL + "/api/mcp",
		HTTPClient: &http.Client{
			Transport: bearerRoundTripper{token: raw, base: http.DefaultTransport},
		},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "spike-test", Version: "0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	// tools/list advertises list_spaces with an output schema.
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	var found *mcp.Tool
	for _, tl := range tools.Tools {
		if tl.Name == "list_spaces" {
			found = tl
		}
	}
	if found == nil {
		t.Fatalf("list_spaces tool not advertised; got %d tools", len(tools.Tools))
	}
	if found.OutputSchema == nil {
		t.Errorf("list_spaces has no output schema")
	}
	// Every tool needs a Title (Claude directory eligibility) and annotations.
	// openWorldHint MUST be set explicitly (the SDK omitempty default is "true",
	// i.e. open-world — wrong for tela's closed DB, which is open-world-free).
	// Both directories reject hints that don't match behavior, so guard them.
	openWorld := map[string]bool{}
	for _, tl := range tools.Tools {
		if tl.Title == "" {
			t.Errorf("tool %q has no Title", tl.Name)
		}
		if tl.Annotations == nil {
			t.Errorf("tool %q has no Annotations", tl.Name)
			continue
		}
		if tl.Annotations.OpenWorldHint == nil {
			t.Errorf("tool %q: OpenWorldHint unset (defaults to open-world)", tl.Name)
		} else if *tl.Annotations.OpenWorldHint != openWorld[tl.Name] {
			t.Errorf("tool %q: OpenWorldHint=%v, want %v", tl.Name, *tl.Annotations.OpenWorldHint, openWorld[tl.Name])
		}
	}
	// Drift guard: the full expected tool roster is advertised (catches an
	// accidentally-dropped or renamed tool).
	got := map[string]bool{}
	for _, tl := range tools.Tools {
		got[tl.Name] = true
	}
	for _, want := range []string{
		"list_spaces", "get_space", "list_pages", "get_page", "list_backlinks",
		"search", "research", "read_chunk", "fetch",
		"create_page", "update_page", "delete_page", "move_page", "add_comment",
		"create_space", "update_space", "delete_space", "invite_to_space",
		"submit_feedback",
		"list_attachments", "upload_attachment", "delete_attachment",
		"request_attachment_upload", "confirm_attachment_upload",
	} {
		if !got[want] {
			t.Errorf("tool %q not advertised", want)
		}
	}

	// tools/call returns alice's space (and only hers) as structured output.
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_spaces"})
	if err != nil {
		t.Fatalf("call list_spaces: %v", err)
	}
	if res.IsError {
		t.Fatalf("list_spaces returned tool error: %v", res.Content)
	}

	var out listSpacesOut
	raw2, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw2, &out); err != nil {
		t.Fatalf("decode structured content %s: %v", raw2, err)
	}
	if len(out.Spaces) != 1 {
		t.Fatalf("want exactly alice's 1 space, got %d: %+v", len(out.Spaces), out.Spaces)
	}
	if out.Spaces[0].Name != "Alice Space" || out.Spaces[0].Slug != "alice-space" {
		t.Errorf("unexpected space: %+v", out.Spaces[0])
	}
	_ = bob
}

// mcpSession opens an authenticated MCP client session against ts using token.
func mcpSession(t *testing.T, ctx context.Context, ts *httptest.Server, token string) *mcp.ClientSession {
	t.Helper()
	transport := &mcp.StreamableClientTransport{
		Endpoint: ts.URL + "/api/mcp",
		HTTPClient: &http.Client{
			Transport: bearerRoundTripper{token: token, base: http.DefaultTransport},
		},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func seedReadKey(t *testing.T, d *sql.DB, uid int64, scope string) string {
	t.Helper()
	raw, prefix, _, err := auth.NewAPIKey(auth.LoadAPIKeySecret())
	if err != nil {
		t.Fatalf("new api key: %v", err)
	}
	hmacHex := auth.HMACAPIKey(auth.LoadAPIKeySecret(), raw)
	if _, err := d.ExecContext(context.Background(), `
		INSERT INTO api_keys (user_id, name, key_prefix, key_hmac, scope, space_id)
		VALUES ($1, 'mcp', $2, $3, $4, NULL)`, uid, prefix, hmacHex, scope); err != nil {
		t.Fatalf("seed key: %v", err)
	}
	return raw
}

func mcpCallJSON(t *testing.T, ctx context.Context, sess *mcp.ClientSession, name string, args map[string]any, out any) {
	t.Helper()
	raw, _ := mcpCallRawJSON(t, ctx, sess, name, args)
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decode %s output %s: %v", name, raw, err)
	}
}

// mcpCallRawJSON calls a tool, fails on a tool/transport error, and returns the
// raw JSON of its structured content (for asserting the exact wire shape).
func mcpCallRawJSON(t *testing.T, ctx context.Context, sess *mcp.ClientSession, name string, args map[string]any) ([]byte, *mcp.CallToolResult) {
	t.Helper()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s returned tool error: %v", name, res.Content)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	return raw, res
}

// TestMCP_ReadTools exercises the Phase-1 read surface end-to-end over the MCP
// transport: list_pages, get_page, search, list_backlinks. It asserts results
// are space-scoped and that the typed structured output decodes.
func TestMCP_ReadTools(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	space := seedSpace(t, d, "Docs", "docs", alice)
	other := seedUser(t, d, "bob", "bobpw1234", false)
	otherSpace := seedSpace(t, d, "Bob", "bob", other)

	// alice's pages: a parent "Alpha" with body, and a child "Beta" linking to it.
	var alphaID int64
	if err := d.QueryRowContext(context.Background(),
		`INSERT INTO pages (space_id, parent_id, title, body, position) VALUES ($1, NULL, 'Alpha', 'the quick brown fox', 0) RETURNING id`,
		space).Scan(&alphaID); err != nil {
		t.Fatalf("insert alpha: %v", err)
	}
	betaBody := "see [Alpha](tela://page/" + strconv.FormatInt(alphaID, 10) + ") for context"
	var betaID int64
	if err := d.QueryRowContext(context.Background(),
		`INSERT INTO pages (space_id, parent_id, title, body, position) VALUES ($1, $2, 'Beta', $3, 0) RETURNING id`,
		space, alphaID, betaBody).Scan(&betaID); err != nil {
		t.Fatalf("insert beta: %v", err)
	}
	// page_links row so backlinks resolve (mirrors syncPageLinks on save).
	if _, err := d.ExecContext(context.Background(),
		`INSERT INTO page_links (source_id, target_id, last_known_title) VALUES ($1, $2, 'Alpha')`,
		betaID, alphaID); err != nil {
		t.Fatalf("insert page_link: %v", err)
	}
	_ = otherSpace

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sess := mcpSession(t, ctx, ts, seedReadKey(t, d, alice, auth.ScopeRead))

	// list_pages top-level → just Alpha.
	var lp listPagesOut
	mcpCallJSON(t, ctx, sess, "list_pages", map[string]any{"space_id": space}, &lp)
	if len(lp.Pages) != 1 || lp.Pages[0].Title != "Alpha" {
		t.Fatalf("list_pages top-level: %+v", lp.Pages)
	}
	if lp.Pages[0].URL == "" {
		t.Errorf("list_pages item missing url")
	}

	// list_pages with parent_id → just Beta.
	var lpc listPagesOut
	mcpCallJSON(t, ctx, sess, "list_pages", map[string]any{"space_id": space, "parent_id": alphaID}, &lpc)
	if len(lpc.Pages) != 1 || lpc.Pages[0].Title != "Beta" {
		t.Fatalf("list_pages child: %+v", lpc.Pages)
	}

	// get_page → full body + url.
	var gp getPageOut
	mcpCallJSON(t, ctx, sess, "get_page", map[string]any{"id": alphaID}, &gp)
	if gp.Page.Body != "the quick brown fox" || gp.Page.URL == "" {
		t.Fatalf("get_page: %+v", gp.Page)
	}

	// search → Alpha matches "fox".
	var sr searchOut
	mcpCallJSON(t, ctx, sess, "search", map[string]any{"query": "fox"}, &sr)
	if len(sr.Results) != 1 || sr.Results[0].PageID != alphaID {
		t.Fatalf("search fox: %+v", sr.Results)
	}
	// ChatGPT Deep-Research aliases present on each hit (id=string page id, text=snippet, url).
	if sr.Results[0].ID != strconv.FormatInt(alphaID, 10) || sr.Results[0].Text == "" || sr.Results[0].URL == "" {
		t.Fatalf("search hit missing ChatGPT id/text/url: %+v", sr.Results[0])
	}

	// fetch (Deep Research) → full page text by the search result's id.
	var fo fetchOut
	mcpCallJSON(t, ctx, sess, "fetch", map[string]any{"id": sr.Results[0].ID}, &fo)
	if fo.Text != "the quick brown fox" || fo.Title != "Alpha" || fo.URL == "" {
		t.Fatalf("fetch: %+v", fo)
	}

	// list_backlinks → Beta links to Alpha.
	var bl listBacklinksOut
	mcpCallJSON(t, ctx, sess, "list_backlinks", map[string]any{"page_id": alphaID}, &bl)
	if len(bl.Backlinks) != 1 || bl.Backlinks[0].PageID != betaID {
		t.Fatalf("list_backlinks: %+v", bl.Backlinks)
	}
}

// TestMCP_ReadToolCrossSpaceDenied asserts a read key cannot reach a space the
// user isn't a member of (get_page collapses to the 403 "not a member").
func TestMCP_ReadToolCrossSpaceDenied(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	seedSpace(t, d, "Alice", "alice", alice)
	bob := seedUser(t, d, "bob", "bobpw1234", false)
	bobSpace := seedSpace(t, d, "Bob", "bob", bob)
	var bobPage int64
	if err := d.QueryRowContext(context.Background(),
		`INSERT INTO pages (space_id, parent_id, title, body, position) VALUES ($1, NULL, 'Secret', 'x', 0) RETURNING id`,
		bobSpace).Scan(&bobPage); err != nil {
		t.Fatalf("insert: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sess := mcpSession(t, ctx, ts, seedReadKey(t, d, alice, auth.ScopeRead))

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "get_page", Arguments: map[string]any{"id": bobPage}})
	if err != nil {
		t.Fatalf("call get_page: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected get_page on another user's space to be a tool error")
	}
}

// TestMCP_FetchBadIDCleanError — fetch with a non-numeric id must come back as a
// clean tool error, not a tool-output schema-validation failure. The error path
// returned a zero fetchOut whose nil `metadata` map serialized to null and failed
// the output schema (object), masking the real error — see mcpFetch.
func TestMCP_FetchBadIDCleanError(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sess := mcpSession(t, ctx, ts, seedReadKey(t, d, alice, auth.ScopeRead))

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "fetch", Arguments: map[string]any{"id": "not-a-number"}})
	if err != nil {
		t.Fatalf("fetch call failed at the transport/validation layer: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected fetch with a non-numeric id to be a tool error")
	}
}

// TestMCP_DeckAuthoringGuideTool — the deck guide is reachable as a TOOL (not only
// as a tela:// resource), for hosts that can't read resources. With the sidecar
// down in tests it returns the static fallback; either way it's non-empty markdown.
func TestMCP_DeckAuthoringGuideTool(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sess := mcpSession(t, ctx, ts, seedReadKey(t, d, alice, auth.ScopeRead))

	raw, _ := mcpCallRawJSON(t, ctx, sess, "deck_authoring_guide", nil)
	var out struct {
		Guide string `json:"guide"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode deck_authoring_guide: %v", err)
	}
	if !strings.Contains(out.Guide, "deck") {
		t.Fatalf("deck_authoring_guide returned unexpected content: %q", out.Guide)
	}
}

// TestMCP_ImportGuide — the bulk-import recipe is reachable both as a TOOL and as
// the tela://import-guide resource, and both carry the same non-empty guide. This
// is the disclosure that makes the (REST) import endpoint discoverable to agents.
func TestMCP_ImportGuide(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sess := mcpSession(t, ctx, ts, seedReadKey(t, d, alice, auth.ScopeRead))

	raw, _ := mcpCallRawJSON(t, ctx, sess, "import_guide", nil)
	var out struct {
		Guide string `json:"guide"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode import_guide: %v", err)
	}
	// The guide must name the endpoint and the sheet-conversion trick — the two
	// things an agent can't guess.
	for _, must := range []string{"/import", "sheet: true", "dry_run"} {
		if !strings.Contains(out.Guide, must) {
			t.Fatalf("import_guide missing %q; got: %q", must, out.Guide)
		}
	}

	// Same content via the resource.
	rr, err := sess.ReadResource(ctx, &mcp.ReadResourceParams{URI: "tela://import-guide"})
	if err != nil {
		t.Fatalf("read import-guide resource: %v", err)
	}
	if len(rr.Contents) != 1 || rr.Contents[0].Text != out.Guide {
		t.Fatalf("import-guide resource != tool content")
	}
}

// TestMCP_WriteTools exercises the Phase-1 write surface end-to-end: create_space,
// create_page, update_page, add_comment, delete_page, delete_space, plus the
// read-scope rejection (mcpRequireWrite) and submit_feedback's any-scope allowance.
func TestMCP_WriteTools(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sess := mcpSession(t, ctx, ts, seedReadKey(t, d, alice, auth.ScopeWrite))

	// create_space → alice owns it.
	var sp spaceOut
	mcpCallJSON(t, ctx, sess, "create_space", map[string]any{"name": "Engineering"}, &sp)
	if sp.Space.ID == 0 || sp.Space.Slug != "engineering" {
		t.Fatalf("create_space: %+v", sp.Space)
	}
	spaceID := sp.Space.ID

	// create_page in it. The result carries id/title/url but deliberately NOT
	// the body the caller just sent (createPageOut omits it — no payload echo).
	var pg createPageOut
	mcpCallJSON(t, ctx, sess, "create_page", map[string]any{
		"space_id": spaceID, "title": "Runbook", "body": "step one",
	}, &pg)
	if pg.Page.Title != "Runbook" || pg.Page.URL == "" {
		t.Fatalf("create_page: %+v", pg.Page)
	}
	// The raw structured content must not contain a body field at all.
	if raw, _ := mcpCallRawJSON(t, ctx, sess, "create_page", map[string]any{
		"space_id": spaceID, "title": "Echo check", "body": "secret body text",
	}); strings.Contains(string(raw), "secret body text") || strings.Contains(string(raw), `"body"`) {
		t.Fatalf("create_page echoed the body: %s", raw)
	}
	pageID := pg.Page.ID

	// update_page body → snapshot + new body.
	var up getPageOut
	mcpCallJSON(t, ctx, sess, "update_page", map[string]any{"id": pageID, "body": "step one\nstep two"}, &up)
	if up.Page.Body != "step one\nstep two" {
		t.Fatalf("update_page: %+v", up.Page)
	}

	// create a second page, then move_page it under the first (reparent).
	var pg2 getPageOut
	mcpCallJSON(t, ctx, sess, "create_page", map[string]any{"space_id": spaceID, "title": "Child", "body": "c"}, &pg2)
	var mv getPageOut
	mcpCallJSON(t, ctx, sess, "move_page", map[string]any{"id": pg2.Page.ID, "parent_id": pageID}, &mv)
	if mv.Page.ParentID == nil || *mv.Page.ParentID != pageID {
		t.Fatalf("move_page reparent: %+v", mv.Page)
	}
	// move_page detach back to root.
	var mv2 getPageOut
	mcpCallJSON(t, ctx, sess, "move_page", map[string]any{"id": pg2.Page.ID, "make_root": true}, &mv2)
	if mv2.Page.ParentID != nil {
		t.Fatalf("move_page make_root: %+v", mv2.Page)
	}
	// parent_id + make_root together is rejected.
	if res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "move_page", Arguments: map[string]any{
		"id": pg2.Page.ID, "parent_id": pageID, "make_root": true,
	}}); err != nil {
		t.Fatalf("move_page conflict call: %v", err)
	} else if !res.IsError {
		t.Fatalf("expected parent_id+make_root to be rejected")
	}

	// add_comment (root, anchored).
	var cm commentOut
	mcpCallJSON(t, ctx, sess, "add_comment", map[string]any{
		"page_id": pageID,
		"anchor":  map[string]any{"prefix": "step ", "exact": "one", "suffix": "\nstep"},
		"body":    "is this still accurate?",
	}, &cm)
	if cm.Comment.ID == 0 || cm.Comment.Body != "is this still accurate?" {
		t.Fatalf("add_comment: %+v", cm.Comment)
	}

	// submit_feedback works on a write key too.
	var fb submitFeedbackOut
	mcpCallJSON(t, ctx, sess, "submit_feedback", map[string]any{"subject": "nice", "body": "the mcp is great"}, &fb)
	if fb.Feedback.ID == 0 {
		t.Fatalf("submit_feedback: %+v", fb.Feedback)
	}

	// delete_page → ok.
	var del okOut
	mcpCallJSON(t, ctx, sess, "delete_page", map[string]any{"id": pageID}, &del)
	if !del.OK {
		t.Fatalf("delete_page: %+v", del)
	}

	// delete_space → ok.
	var dels okOut
	mcpCallJSON(t, ctx, sess, "delete_space", map[string]any{"id": spaceID}, &dels)
	if !dels.OK {
		t.Fatalf("delete_space: %+v", dels)
	}
}

// TestMCP_IdempotentCreate asserts a create_page replayed with the same
// idempotency_key returns the ORIGINAL page instead of creating a duplicate —
// the safe-retry guarantee for dropped connections. Also checks that a key
// reused on a different tool is rejected, and that omitting the key leaves the
// old (always-create) behavior intact.
func TestMCP_IdempotentCreate(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	space := seedSpace(t, d, "Docs", "docs", alice)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sess := mcpSession(t, ctx, ts, seedReadKey(t, d, alice, auth.ScopeWrite))

	args := map[string]any{"space_id": space, "title": "Once", "body": "b", "idempotency_key": "k-abc"}

	var first createPageOut
	mcpCallJSON(t, ctx, sess, "create_page", args, &first)
	if first.Page.ID == 0 {
		t.Fatalf("first create: %+v", first.Page)
	}

	// Replay with the same key → SAME page id, no second row.
	var replay createPageOut
	mcpCallJSON(t, ctx, sess, "create_page", args, &replay)
	if replay.Page.ID != first.Page.ID {
		t.Fatalf("replay made a new page: first=%d replay=%d", first.Page.ID, replay.Page.ID)
	}

	var count int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM pages WHERE space_id=$1 AND title='Once'`, space).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 page, got %d", count)
	}

	// A different key creates a distinct page.
	var other createPageOut
	mcpCallJSON(t, ctx, sess, "create_page", map[string]any{
		"space_id": space, "title": "Twice", "body": "b", "idempotency_key": "k-xyz",
	}, &other)
	if other.Page.ID == first.Page.ID {
		t.Fatalf("different key reused the same page")
	}

	// Reusing a key on a different tool is rejected.
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "create_space", Arguments: map[string]any{
		"name": "Nope", "idempotency_key": "k-abc",
	}})
	if err != nil {
		t.Fatalf("call create_space: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected a reused key on a different tool to be rejected")
	}
}

// TestMCP_WriteToolReadKeyDenied asserts a read-scope key is refused at a write
// tool (mcpRequireWrite → api_key_scope) but allowed at submit_feedback.
func TestMCP_WriteToolReadKeyDenied(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	space := seedSpace(t, d, "Docs", "docs", alice)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sess := mcpSession(t, ctx, ts, seedReadKey(t, d, alice, auth.ScopeRead))

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "create_page", Arguments: map[string]any{
		"space_id": space, "title": "X", "body": "y",
	}})
	if err != nil {
		t.Fatalf("call create_page: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected read key to be denied at create_page")
	}

	// submit_feedback is the read-scope carve-out → must succeed.
	var fb submitFeedbackOut
	mcpCallJSON(t, ctx, sess, "submit_feedback", map[string]any{"subject": "s", "body": "b"}, &fb)
	if fb.Feedback.ID == 0 {
		t.Fatalf("submit_feedback under read key should succeed: %+v", fb.Feedback)
	}
}

// TestMCP_ShareTools exercises the share-link lifecycle over MCP: share_page
// mints a short-token public URL, list_shares sees it, revoke_share disables it
// (idempotently), and a read-scope key is refused at all three.
func TestMCP_ShareTools(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	space := seedSpace(t, d, "Docs", "docs", alice)
	page := seedPage(t, d, space, "Public Note")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sess := mcpSession(t, ctx, ts, seedReadKey(t, d, alice, auth.ScopeWrite))

	// share_page → public URL with a short (11-char) token.
	var sp sharePageOut
	mcpCallJSON(t, ctx, sess, "share_page", map[string]any{
		"page_id": page, "include_descendants": true,
	}, &sp)
	if sp.Share.ID == 0 || len(sp.Share.Token) != 11 || sp.Share.URL == "" || !sp.Share.IncludeDescendants {
		t.Fatalf("share_page: %+v", sp.Share)
	}

	// list_shares sees the active link.
	var ls listSharesOut
	mcpCallJSON(t, ctx, sess, "list_shares", map[string]any{"page_id": page}, &ls)
	if len(ls.Shares) != 1 || ls.Shares[0].ID != sp.Share.ID {
		t.Fatalf("list_shares: %+v", ls.Shares)
	}

	// revoke_share disables it; list_shares (active only) is now empty.
	var rv okOut
	mcpCallJSON(t, ctx, sess, "revoke_share", map[string]any{"share_id": sp.Share.ID}, &rv)
	if !rv.OK {
		t.Fatalf("revoke_share: %+v", rv)
	}
	mcpCallJSON(t, ctx, sess, "list_shares", map[string]any{"page_id": page}, &ls)
	if len(ls.Shares) != 0 {
		t.Fatalf("expected no active shares after revoke: %+v", ls.Shares)
	}
	// include_revoked surfaces the revoked row.
	mcpCallJSON(t, ctx, sess, "list_shares", map[string]any{"page_id": page, "include_revoked": true}, &ls)
	if len(ls.Shares) != 1 || ls.Shares[0].RevokedAt == nil {
		t.Fatalf("include_revoked: %+v", ls.Shares)
	}
	// revoke again → idempotent no-op.
	mcpCallJSON(t, ctx, sess, "revoke_share", map[string]any{"share_id": sp.Share.ID}, &rv)
	if !rv.OK {
		t.Fatalf("idempotent revoke_share: %+v", rv)
	}

	// A read-scope key is refused at every share tool (tokens are secrets).
	rsess := mcpSession(t, ctx, ts, seedReadKey(t, d, alice, auth.ScopeRead))
	for _, name := range []string{"share_page", "list_shares", "revoke_share"} {
		res, err := rsess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{
			"page_id": page, "share_id": sp.Share.ID,
		}})
		if err != nil {
			t.Fatalf("call %s: %v", name, err)
		}
		if !res.IsError {
			t.Fatalf("expected read key to be denied at %s", name)
		}
	}
}

// TestMCP_Resources exercises Phase 2: the tela://page/{id} resource template
// (list + read, with cross-space denial) and resource links in tool results.
func TestMCP_Resources(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	space := seedSpace(t, d, "Docs", "docs", alice)
	bob := seedUser(t, d, "bob", "bobpw1234", false)
	bobSpace := seedSpace(t, d, "Bob", "bob", bob)

	var pageID int64
	if err := d.QueryRowContext(context.Background(),
		`INSERT INTO pages (space_id, parent_id, title, body, position) VALUES ($1, NULL, 'Alpha', 'hello world', 0) RETURNING id`,
		space).Scan(&pageID); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var bobPage int64
	if err := d.QueryRowContext(context.Background(),
		`INSERT INTO pages (space_id, parent_id, title, body, position) VALUES ($1, NULL, 'Secret', 'x', 0) RETURNING id`,
		bobSpace).Scan(&bobPage); err != nil {
		t.Fatalf("insert: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sess := mcpSession(t, ctx, ts, seedReadKey(t, d, alice, auth.ScopeRead))

	// The page template is advertised.
	tmpls, err := sess.ListResourceTemplates(ctx, nil)
	if err != nil {
		t.Fatalf("list templates: %v", err)
	}
	var hasPage bool
	for _, tm := range tmpls.ResourceTemplates {
		if tm.URITemplate == "tela://page/{id}" {
			hasPage = true
		}
	}
	if !hasPage {
		t.Fatalf("tela://page/{id} template not advertised: %+v", tmpls.ResourceTemplates)
	}

	// Read alice's page → markdown with title + body.
	rr, err := sess.ReadResource(ctx, &mcp.ReadResourceParams{URI: "tela://page/" + strconv.FormatInt(pageID, 10)})
	if err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if len(rr.Contents) != 1 || rr.Contents[0].Text != "# Alpha\n\nhello world" {
		t.Fatalf("resource contents: %+v", rr.Contents)
	}

	// Reading bob's page → not found (membership-gated, collapsed).
	if _, err := sess.ReadResource(ctx, &mcp.ReadResourceParams{URI: "tela://page/" + strconv.FormatInt(bobPage, 10)}); err == nil {
		t.Fatalf("expected cross-space resource read to fail")
	}

	// get_page tool result carries no ResourceLink content blocks — hosts (Claude)
	// surface them as "Resource links are not currently supported" noise, and the
	// data is already in structuredContent. The tela://page/{id} resource template
	// (asserted above) remains the click-through / @-mention path.
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "get_page", Arguments: map[string]any{"id": pageID}})
	if err != nil {
		t.Fatalf("get_page: %v", err)
	}
	for _, c := range res.Content {
		if _, ok := c.(*mcp.ResourceLink); ok {
			t.Fatalf("get_page result should not carry ResourceLink blocks: %+v", res.Content)
		}
	}

	// get_space tool → metadata for the space.
	var gs spaceOut
	mcpCallJSON(t, ctx, sess, "get_space", map[string]any{"id": space}, &gs)
	if gs.Space.ID != space || gs.Space.Slug != "docs" {
		t.Fatalf("get_space: %+v", gs.Space)
	}

	// tela://space/{id} resource → markdown index linking the page.
	sr, err := sess.ReadResource(ctx, &mcp.ReadResourceParams{URI: "tela://space/" + strconv.FormatInt(space, 10)})
	if err != nil {
		t.Fatalf("read space resource: %v", err)
	}
	wantLink := "[Alpha](tela://page/" + strconv.FormatInt(pageID, 10) + ")"
	if len(sr.Contents) != 1 || !strings.Contains(sr.Contents[0].Text, "# Docs") || !strings.Contains(sr.Contents[0].Text, wantLink) {
		t.Fatalf("space resource contents: %q", sr.Contents[0].Text)
	}

	// Cross-space space resource read → denied.
	if _, err := sess.ReadResource(ctx, &mcp.ReadResourceParams{URI: "tela://space/" + strconv.FormatInt(bobSpace, 10)}); err == nil {
		t.Fatalf("expected cross-space space-resource read to fail")
	}
}

// TestMCP_SpikeRejectsNoToken asserts the transport refuses an unauthenticated
// connection (the bearer verifier 401s with WWW-Authenticate).
func TestMCP_SpikeRejectsNoToken(t *testing.T) {
	ts, _ := newWiredServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	transport := &mcp.StreamableClientTransport{Endpoint: ts.URL + "/api/mcp"}
	client := mcp.NewClient(&mcp.Implementation{Name: "spike-test", Version: "0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err == nil {
		session.Close()
		t.Fatalf("expected connect to fail without a token")
	}
}

// TestMCP_Widgets verifies the MCP Apps widget surface: the ui:// resources are
// advertised + serve HTML (both MIME variants) with the host bridge inlined (no
// external esm.sh import — that left a blank iframe in Claude), and the
// get_page/search tools carry the _meta that links them to their widget.
func TestMCP_Widgets(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sess := mcpSession(t, ctx, ts, seedReadKey(t, d, alice, auth.ScopeRead))

	// All four widget resources advertised.
	res, err := sess.ListResources(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"ui://tela/page-reader/openai": false, "ui://tela/page-reader/mcp": false,
		"ui://tela/search-results/openai": false, "ui://tela/search-results/mcp": false,
	}
	for _, r := range res.Resources {
		if _, ok := want[r.URI]; ok {
			want[r.URI] = true
		}
	}
	for uri, found := range want {
		if !found {
			t.Errorf("widget resource %s not advertised", uri)
		}
	}

	// Reading a widget resource returns the HTML bundle with the right MIME and
	// the bridge inlined: the ChatGPT (window.openai) + MCP Apps (ui/initialize)
	// branches are present, the injection marker is consumed, and there's no
	// external esm.sh import (the cause of Claude's blank iframe).
	rr, err := sess.ReadResource(ctx, &mcp.ReadResourceParams{URI: "ui://tela/page-reader/openai"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rr.Contents) != 1 {
		t.Fatalf("widget html unexpected: %+v", rr.Contents)
	}
	html := rr.Contents[0].Text
	for _, must := range []string{"window.openai", "ui/initialize", "window.__telaWidget"} {
		if !strings.Contains(html, must) {
			t.Errorf("widget html missing %q (bridge not inlined?)", must)
		}
	}
	for _, mustNot := range []string{"esm.sh", "<!--TELA_BRIDGE-->"} {
		if strings.Contains(html, mustNot) {
			t.Errorf("widget html still contains %q", mustNot)
		}
	}
	if rr.Contents[0].MIMEType != "text/html+skybridge" {
		t.Errorf("widget mime: %q", rr.Contents[0].MIMEType)
	}

	// get_page + search advertise the widget link _meta.
	tools, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantTemplate := map[string]string{
		"get_page": "ui://tela/page-reader/openai",
		"search":   "ui://tela/search-results/openai",
	}
	seen := map[string]bool{}
	for _, tl := range tools.Tools {
		if want, ok := wantTemplate[tl.Name]; ok {
			seen[tl.Name] = true
			if tl.Meta["openai/outputTemplate"] != want {
				t.Errorf("%s _meta outputTemplate = %v, want %q", tl.Name, tl.Meta["openai/outputTemplate"], want)
			}
		}
	}
	for name := range wantTemplate {
		if !seen[name] {
			t.Errorf("tool %q not advertised", name)
		}
	}
}

// TestMCP_ResearchFiles proves the MCP retrieval surface covers attachments:
// a reindexed file surfaces in research's sources with a file citation
// (source_kind, file_name, parent page_id, download_url) and reads back through
// read_chunk.
func TestMCP_ResearchFiles(t *testing.T) {
	ts, d, srv := newRagServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	aSpace := seedSpace(t, d, "Alpha", "alpha", alice)
	parent := mustPage(t, d, aSpace, "Vendor", "## Notes\nvendor context here")
	fileID := mustFile(t, d, aSpace, parent, "msa.md", "text/markdown",
		"# Master Service Agreement\n\nThe indemnification liability cap is two million dollars.")
	if _, err := srv.rag.ReindexFile(context.Background(), fileID); err != nil {
		t.Fatalf("reindex file: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sess := mcpSession(t, ctx, ts, seedReadKey(t, d, alice, auth.ScopeRead))

	var sout researchOut
	mcpCallJSON(t, ctx, sess, "research", map[string]any{"question": "what is the indemnification liability cap?"}, &sout)
	if sout.Context == "" {
		t.Errorf("research returned empty context for a question with a file hit")
	}
	var fileHit *rag.Hit
	for i := range sout.Sources {
		if sout.Sources[i].SourceKind == "file" {
			fileHit = &sout.Sources[i]
		}
	}
	if fileHit == nil {
		t.Fatalf("no file hit from research: %+v", sout.Sources)
	}
	if fileHit.FileID != fileID || fileHit.FileName != "msa.md" || fileHit.PageID != parent {
		t.Errorf("file citation wrong: %+v", *fileHit)
	}
	if fileHit.DownloadURL == "" {
		t.Errorf("file hit missing download_url")
	}

	var rout readChunkOut
	mcpCallJSON(t, ctx, sess, "read_chunk", map[string]any{"chunk_id": fileHit.ChunkID}, &rout)
	if rout.Chunk.SourceKind != "file" || rout.Chunk.FileID != fileID || rout.Chunk.Content == "" {
		t.Errorf("read_chunk file result wrong: %+v", rout.Chunk)
	}
	if rout.Chunk.DownloadURL == "" {
		t.Errorf("read_chunk file result missing download_url")
	}
}

// TestMCP_InviteToSpace exercises invite_to_space over the transport: both
// branches (an address that already has an account vs one that doesn't), the
// owner gate, and the write-scope gate.
func TestMCP_InviteToSpace(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	space := seedSpace(t, d, "Docs", "docs", alice)
	bob := seedUser(t, d, "bob", "bobpw1234", false)
	setUserEmail(t, d, bob, "bob@acme.com")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sess := mcpSession(t, ctx, ts, seedReadKey(t, d, alice, auth.ScopeWrite))

	// Existing account → immediate grant, {member} envelope, no invite row.
	var got inviteToSpaceOut
	raw, _ := mcpCallRawJSON(t, ctx, sess, "invite_to_space", map[string]any{
		"space_id": space, "email": "bob@acme.com", "role": "editor",
	})
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode invite_to_space output %s: %v", raw, err)
	}
	if got.Member == nil || got.Invite != nil || got.Member.Username != "bob" || got.Member.Role != "editor" {
		t.Fatalf("existing-account branch: %s", raw)
	}
	var members int
	mustQueryRow(t, d, `SELECT COUNT(*) FROM space_members WHERE space_id=$1 AND user_id=$2`, &members, space, bob)
	if members != 1 {
		t.Fatalf("bob should be a member, got %d", members)
	}

	// Unknown address → pending invite + emailed invitation, {invite} envelope.
	got = inviteToSpaceOut{}
	raw, _ = mcpCallRawJSON(t, ctx, sess, "invite_to_space", map[string]any{
		"space_id": space, "email": "stranger@acme.com",
	})
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode invite_to_space output %s: %v", raw, err)
	}
	if got.Invite == nil || got.Member != nil || got.Invite.Email != "stranger@acme.com" || got.Invite.Role != "viewer" {
		t.Fatalf("unknown-address branch: %s", raw)
	}
	var pending int
	mustQueryRow(t, d, `SELECT COUNT(*) FROM space_invites WHERE space_id=$1 AND accepted_at IS NULL`, &pending, space)
	if pending != 1 {
		t.Fatalf("expected 1 pending invite, got %d", pending)
	}
	// Auditable: inviting a third party by mail leaves a trail.
	var audits int
	mustQueryRow(t, d, `SELECT COUNT(*) FROM access_audit WHERE action='space_invite.create' AND target_id=$1`, &audits, space)
	if audits != 1 {
		t.Fatalf("expected an audit row for the invite, got %d", audits)
	}

	// A non-owner (editor) with a write key can't invite.
	carol := seedUser(t, d, "carol", "carolpw12", false)
	seedMember(t, d, space, carol, "editor")
	csess := mcpSession(t, ctx, ts, seedReadKey(t, d, carol, auth.ScopeWrite))
	if res, err := csess.CallTool(ctx, &mcp.CallToolParams{Name: "invite_to_space", Arguments: map[string]any{
		"space_id": space, "email": "someone@acme.com",
	}}); err != nil {
		t.Fatalf("call invite_to_space as editor: %v", err)
	} else if !res.IsError {
		t.Fatalf("expected a non-owner to be refused at invite_to_space")
	}

	// A read-scope key of the owner is refused too (mcpRequireWrite).
	rsess := mcpSession(t, ctx, ts, seedReadKey(t, d, alice, auth.ScopeRead))
	if res, err := rsess.CallTool(ctx, &mcp.CallToolParams{Name: "invite_to_space", Arguments: map[string]any{
		"space_id": space, "email": "someone@acme.com",
	}}); err != nil {
		t.Fatalf("call invite_to_space with read key: %v", err)
	} else if !res.IsError {
		t.Fatalf("expected a read-scope key to be denied at invite_to_space")
	}
}
