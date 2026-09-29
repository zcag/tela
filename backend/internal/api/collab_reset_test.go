package api

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zcag/tela/backend/internal/auth"
)

func countYjs(t *testing.T, d *sql.DB, table string, pageID int64) int {
	t.Helper()
	var n int
	if err := d.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE page_id = $1", pageID).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// Simulate a live editor having built a Yjs overlay (updates + a snapshot) for a page.
func seedYjsOverlay(t *testing.T, d *sql.DB, pageID int64) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO page_yjs_updates(page_id, seq, payload) VALUES ($1, 1, $2)`, pageID, []byte{0x01, 0x02}); err != nil {
		t.Fatalf("seed update: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO page_yjs_snapshots(page_id, seq, state) VALUES ($1, 1, $2)`, pageID, []byte{0x03, 0x04}); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
}

// An agent (MCP update_page) rewriting the body must DROP the Yjs overlay so a
// live/next editor re-seeds from the new body — while the editor's own REST save
// must PRESERVE the overlay it is in sync with.
func TestMCP_AgentWriteResetsCollabOverlay(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sess := mcpSession(t, ctx, ts, seedReadKey(t, d, alice, auth.ScopeWrite))

	var sp spaceOut
	mcpCallJSON(t, ctx, sess, "create_space", map[string]any{"name": "Eng"}, &sp)
	var pg getPageOut
	mcpCallJSON(t, ctx, sess, "create_page", map[string]any{"space_id": sp.Space.ID, "title": "Doc", "body": "v1"}, &pg)
	pageID := pg.Page.ID

	// --- agent (MCP) write clears the overlay ---
	seedYjsOverlay(t, d, pageID)
	if countYjs(t, d, "page_yjs_updates", pageID) == 0 || countYjs(t, d, "page_yjs_snapshots", pageID) == 0 {
		t.Fatal("precondition: overlay rows should exist")
	}
	var up getPageOut
	mcpCallJSON(t, ctx, sess, "update_page", map[string]any{"id": pageID, "body": "v2-from-agent"}, &up)
	if up.Page.Body != "v2-from-agent" {
		t.Fatalf("body not updated: %q", up.Page.Body)
	}
	if n := countYjs(t, d, "page_yjs_updates", pageID); n != 0 {
		t.Fatalf("agent write should clear page_yjs_updates, have %d", n)
	}
	if n := countYjs(t, d, "page_yjs_snapshots", pageID); n != 0 {
		t.Fatalf("agent write should clear page_yjs_snapshots, have %d", n)
	}

	// --- editor REST save preserves the overlay ---
	seedYjsOverlay(t, d, pageID)
	cli := loginClient(t, ts, "alice", "alicepw12")
	req, _ := http.NewRequest(http.MethodPatch, ts.URL+"/api/pages/"+strconv.FormatInt(pageID, 10), strings.NewReader(`{"body":"v3-from-editor"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := cli.Do(req)
	if err != nil {
		t.Fatalf("rest patch: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rest patch status %d", resp.StatusCode)
	}
	if n := countYjs(t, d, "page_yjs_updates", pageID); n != 1 {
		t.Fatalf("editor REST save must preserve the overlay, have %d page_yjs_updates", n)
	}

	// --- markdown source-mode save (reset_collab) clears it, but stays a human edit ---
	req, _ = http.NewRequest(http.MethodPatch, ts.URL+"/api/pages/"+strconv.FormatInt(pageID, 10), strings.NewReader(`{"body":"v4-from-source","reset_collab":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err = cli.Do(req)
	if err != nil {
		t.Fatalf("rest patch: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rest patch status %d", resp.StatusCode)
	}
	if n := countYjs(t, d, "page_yjs_updates", pageID) + countYjs(t, d, "page_yjs_snapshots", pageID); n != 0 {
		t.Fatalf("reset_collab save should clear the overlay, have %d rows", n)
	}
	var source string
	if err := d.QueryRow(`SELECT source FROM page_revisions WHERE page_id = $1 AND body = 'v4-from-source'`, pageID).Scan(&source); err != nil {
		t.Fatalf("revision: %v", err)
	}
	if source != "manual" {
		t.Fatalf("source-mode save must be recorded as a manual edit, got %q", source)
	}
}
