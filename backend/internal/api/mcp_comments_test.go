package api

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zcag/tela/backend/internal/auth"
)

// TestMCP_CommentTools exercises the agent-facing comment loop end to end:
// read what people wrote (page threads and the space inbox), poll for what
// changed with the cursor, answer in-thread, and close it out.
func TestMCP_CommentTools(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	bob := seedUser(t, d, "bob", "bobpw1234", false)
	space := seedSpace(t, d, "Docs", "docs", alice)
	seedMember(t, d, space, bob, "editor")
	page := mustPage(t, d, space, "Runbook", "step one\nstep two\nstep three")
	notes := mustPage(t, d, space, "Notes", "background reading")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sess := mcpSession(t, ctx, ts, seedReadKey(t, d, alice, auth.ScopeWrite))

	// Three root comments on one page. Three matters: the reply bucketing used
	// to key a growing slice by POINTER, so every thread but the last silently
	// lost its replies once the backing array reallocated.
	var roots []int64
	for _, span := range []string{"one", "two", "three"} {
		var c commentOut
		mcpCallJSON(t, ctx, sess, "add_comment", map[string]any{
			"page_id": page,
			"anchor":  map[string]any{"prefix": "step ", "exact": span, "suffix": "\nstep"},
			"body":    "is " + span + " still right?",
		}, &c)
		if c.Comment.ID == 0 {
			t.Fatalf("add_comment %s: %+v", span, c.Comment)
		}
		roots = append(roots, c.Comment.ID)
	}
	var onNotes commentOut
	mcpCallJSON(t, ctx, sess, "add_comment", map[string]any{
		"page_id": notes,
		"anchor":  map[string]any{"prefix": "", "exact": "background", "suffix": " reading"},
		"body":    "stale?",
	}, &onNotes)

	// Reply to the FIRST thread — the one the pointer bug used to drop.
	var reply commentOut
	mcpCallJSON(t, ctx, sess, "add_comment", map[string]any{
		"page_id": page, "parent_id": roots[0], "body": "yes, verified today",
	}, &reply)
	if reply.Comment.ParentID == nil || *reply.Comment.ParentID != roots[0] {
		t.Fatalf("reply not attached to root: %+v", reply.Comment)
	}
	if reply.Comment.AnchorExact != nil {
		t.Errorf("reply should carry no anchor: %+v", reply.Comment)
	}

	// Page scope: full threads, replies bucketed onto the right root.
	var pageList listCommentsOut
	mcpCallJSON(t, ctx, sess, "list_comments", map[string]any{"page_id": page}, &pageList)
	if len(pageList.Threads) != 3 {
		t.Fatalf("want 3 threads, got %d", len(pageList.Threads))
	}
	if got := len(pageList.Threads[0].Replies); got != 1 {
		t.Fatalf("first thread lost its reply (bucketing regression): %d replies", got)
	}
	if pageList.Cursor == "" {
		t.Fatal("page listing returned no cursor")
	}
	cursor := pageList.Cursor

	// Space scope: an inbox of roots across every page, with reply counts and
	// page context, and no replies inlined.
	var spaceList listCommentsOut
	mcpCallJSON(t, ctx, sess, "list_comments", map[string]any{"space_id": space}, &spaceList)
	if len(spaceList.Threads) != 4 {
		t.Fatalf("want 4 space threads, got %d", len(spaceList.Threads))
	}
	var first *commentThread
	for i := range spaceList.Threads {
		if spaceList.Threads[i].Root.ID == roots[0] {
			first = &spaceList.Threads[i]
		}
	}
	if first == nil {
		t.Fatal("space inbox missing the first thread")
	}
	if first.ReplyCount == nil || *first.ReplyCount != 1 {
		t.Errorf("reply_count = %v, want 1", first.ReplyCount)
	}
	if first.PageTitle != "Runbook" || first.SpaceID != space {
		t.Errorf("page context = %q/%d", first.PageTitle, first.SpaceID)
	}
	if len(first.Replies) != 0 {
		t.Errorf("space inbox should not inline replies, got %d", len(first.Replies))
	}

	// Cursor: nothing has changed, so a poll comes back empty.
	var poll listCommentsOut
	mcpCallJSON(t, ctx, sess, "list_comments", map[string]any{"page_id": page, "since": cursor}, &poll)
	if len(poll.Threads) != 0 {
		t.Fatalf("poll with an up-to-date cursor returned %d threads", len(poll.Threads))
	}

	// A reply resurfaces its thread even though the ROOT row never changed —
	// which an id-only or created_at cursor could not do.
	var reply2 commentOut
	mcpCallJSON(t, ctx, sess, "add_comment", map[string]any{
		"page_id": page, "parent_id": roots[1], "body": "checking",
	}, &reply2)
	mcpCallJSON(t, ctx, sess, "list_comments", map[string]any{"page_id": page, "since": cursor}, &poll)
	if len(poll.Threads) != 1 || poll.Threads[0].Root.ID != roots[1] {
		t.Fatalf("poll after a reply: want just thread %d, got %+v", roots[1], poll.Threads)
	}

	// Resolve, then resolve again: idempotent over MCP (409 in REST).
	var res commentOut
	mcpCallJSON(t, ctx, sess, "update_comment", map[string]any{"comment_id": roots[2], "resolved": true}, &res)
	if !res.Comment.Resolved {
		t.Fatalf("resolve did not take: %+v", res.Comment)
	}
	var again commentOut
	mcpCallJSON(t, ctx, sess, "update_comment", map[string]any{"comment_id": roots[2], "resolved": true}, &again)
	if !again.Comment.Resolved {
		t.Fatalf("re-resolve should report the comment as resolved: %+v", again.Comment)
	}

	// status filters the roots.
	for _, tc := range []struct {
		status string
		want   int
	}{
		{"open", 2}, {"resolved", 1}, {"all", 3},
	} {
		var got listCommentsOut
		mcpCallJSON(t, ctx, sess, "list_comments", map[string]any{"page_id": page, "status": tc.status}, &got)
		if len(got.Threads) != tc.want {
			t.Errorf("status=%s: want %d threads, got %d", tc.status, tc.want, len(got.Threads))
		}
	}

	// Editing a body is author-only: bob may resolve alice's thread but not
	// rewrite what she said.
	bsess := mcpSession(t, ctx, ts, seedReadKey(t, d, bob, auth.ScopeWrite))
	if r, err := bsess.CallTool(ctx, &mcp.CallToolParams{Name: "update_comment", Arguments: map[string]any{
		"comment_id": roots[0], "body": "rewritten",
	}}); err != nil {
		t.Fatalf("call update_comment as bob: %v", err)
	} else if !r.IsError {
		t.Error("editing another author's comment should be refused")
	}

	// A read-scope key reads comments but cannot mutate them.
	rsess := mcpSession(t, ctx, ts, seedReadKey(t, d, alice, auth.ScopeRead))
	var readOnly listCommentsOut
	mcpCallJSON(t, ctx, rsess, "list_comments", map[string]any{"page_id": page}, &readOnly)
	if len(readOnly.Threads) == 0 {
		t.Error("read-scope key should be able to list comments")
	}
	if r, err := rsess.CallTool(ctx, &mcp.CallToolParams{Name: "update_comment", Arguments: map[string]any{
		"comment_id": roots[0], "resolved": true,
	}}); err != nil {
		t.Fatalf("call update_comment with a read key: %v", err)
	} else if !r.IsError {
		t.Error("read-scope key should not be able to resolve")
	}

	// Scope is exactly one of page_id / space_id.
	if r, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "list_comments", Arguments: map[string]any{
		"page_id": page, "space_id": space,
	}}); err != nil {
		t.Fatalf("call list_comments with both scopes: %v", err)
	} else if !r.IsError {
		t.Error("page_id + space_id together should be rejected")
	}
}
