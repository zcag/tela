package api

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zcag/tela/backend/internal/auth"
)

// TestMCP_PageRevisions: list_page_revisions / get_page_revision read history
// newest first with is_current, page through a cursor, keep working once the
// page is trashed (the Atlas-prune case), and stay behind the editor bar.
func TestMCP_PageRevisions(t *testing.T) {
	ts, d := newWiredServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	alice := seedUser(t, d, "alice", "alicepw12", false)
	viewer := seedUser(t, d, "vic", "vicpw1234", false)
	space := seedSpace(t, d, "Docs", "docs", alice)
	seedMember(t, d, space, viewer, roleViewer)

	var pageID int64
	if err := d.QueryRowContext(ctx,
		`INSERT INTO pages (space_id, parent_id, title, body, position) VALUES ($1, NULL, 'Doc', 'v3', 0) RETURNING id`,
		space).Scan(&pageID); err != nil {
		t.Fatalf("insert page: %v", err)
	}
	revs := make([]int64, 3)
	for i, body := range []string{"v1", "v2", "v3"} {
		id, err := insertPageRevision(ctx, d, pageID, body, "Doc", map[string]any{"commit": body}, &alice, "agent")
		if err != nil {
			t.Fatalf("seed revision: %v", err)
		}
		revs[i] = id
	}

	sess := mcpSession(t, ctx, ts, seedReadKey(t, d, alice, auth.ScopeRead))

	var first listPageRevisionsOut
	mcpCallJSON(t, ctx, sess, "list_page_revisions", map[string]any{"page_id": pageID, "limit": 2}, &first)
	if len(first.Revisions) != 2 || first.Revisions[0].ID != revs[2] || first.Revisions[1].ID != revs[1] {
		t.Fatalf("first page = %+v, want revs %d,%d", first.Revisions, revs[2], revs[1])
	}
	if !first.Revisions[0].IsCurrent || first.Revisions[1].IsCurrent {
		t.Errorf("is_current = %v,%v, want true,false", first.Revisions[0].IsCurrent, first.Revisions[1].IsCurrent)
	}
	if first.Revisions[0].Body != "" {
		t.Error("list leaked a body")
	}
	if first.NextCursor == nil || *first.NextCursor != revs[1] {
		t.Fatalf("next_cursor = %v, want %d", first.NextCursor, revs[1])
	}
	var rest listPageRevisionsOut
	mcpCallJSON(t, ctx, sess, "list_page_revisions", map[string]any{"page_id": pageID, "limit": 2, "cursor": *first.NextCursor}, &rest)
	if len(rest.Revisions) != 1 || rest.Revisions[0].ID != revs[0] || rest.NextCursor != nil {
		t.Fatalf("second page = %+v cursor=%v, want just rev %d and no cursor", rest.Revisions, rest.NextCursor, revs[0])
	}

	var got getPageRevisionOut
	mcpCallJSON(t, ctx, sess, "get_page_revision", map[string]any{"page_id": pageID, "revision_id": revs[0]}, &got)
	if got.Revision.Body != "v1" || got.Revision.Props["commit"] != "v1" || got.Revision.IsCurrent {
		t.Errorf("get_page_revision = %+v, want body/commit v1, not current", got.Revision)
	}

	// Trashed: history stays readable, nothing is current any more.
	if _, err := d.ExecContext(ctx, `UPDATE pages SET deleted_at = tela_now() WHERE id = $1`, pageID); err != nil {
		t.Fatalf("trash page: %v", err)
	}
	var trashed listPageRevisionsOut
	mcpCallJSON(t, ctx, sess, "list_page_revisions", map[string]any{"page_id": pageID}, &trashed)
	if trashed.Page.DeletedAt == nil || len(trashed.Revisions) != 3 || trashed.Revisions[0].IsCurrent {
		t.Errorf("trashed page: deleted_at=%v revs=%d current=%v", trashed.Page.DeletedAt, len(trashed.Revisions), trashed.Revisions[0].IsCurrent)
	}
	mcpCallJSON(t, ctx, sess, "get_page_revision", map[string]any{"page_id": pageID, "revision_id": revs[2]}, &got)
	if got.Revision.Body != "v3" {
		t.Errorf("trashed get body = %q, want v3", got.Revision.Body)
	}

	// A viewer is refused, as in the app.
	vsess := mcpSession(t, ctx, ts, seedReadKey(t, d, viewer, auth.ScopeRead))
	res, err := vsess.CallTool(ctx, &mcp.CallToolParams{Name: "list_page_revisions", Arguments: map[string]any{"page_id": pageID}})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError {
		t.Error("viewer listed revisions, want a tool error")
	}
}
