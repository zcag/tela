package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// A page in a PUBLIC space is world-readable — ranked search surfaces it to
// non-members and crawlers index it — so the authed route denying it with a
// bare 403 was an inconsistency, not a permission boundary. These tests pin the
// two halves of the fix: the denial says where to read it instead, and search
// links such a hit at the public reader in the first place.

func TestGetPage_PublicSpaceNonMember_RedirectsInsteadOf403(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	seedUser(t, d, "bob", "bobpw12345", false)

	pub := seedSpace(t, d, "Claude Notes", "claude-notes", alice)
	var pageID int64
	if err := d.QueryRow(
		`INSERT INTO pages (space_id, parent_id, title, body, position)
		 VALUES ($1, NULL, 'Claude 101', 'how to use claude', 0) RETURNING id`, pub).Scan(&pageID); err != nil {
		t.Fatalf("seed page: %v", err)
	}
	mustExec(t, d, `UPDATE spaces SET visibility = 'public' WHERE id = $1`, pub)

	bobC := loginClient(t, ts, "bob", "bobpw12345")
	resp, err := bobC.Get(fmt.Sprintf("%s/api/pages/%d", ts.URL, pageID))
	if err != nil {
		t.Fatalf("get page: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d want 403 (the route stays gated; only the payload changes)", resp.StatusCode)
	}
	var body struct {
		Code       string `json:"code"`
		PublicPath string `json:"public_path"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Code != "forbidden_public" {
		t.Fatalf("code=%q want forbidden_public", body.Code)
	}
	// The canonical pretty form — what a denied non-member should be sent to.
	want := fmt.Sprintf("/alice/claude-notes/%d/claude-101", pageID)
	if body.PublicPath != want {
		t.Fatalf("public_path=%q want %q", body.PublicPath, want)
	}
}

// The fallback must NOT fire for genuinely private content: a non-member gets
// the same opaque denial as before, with nothing disclosing that the page (or
// its space) exists.
func TestGetPage_PrivateSpaceNonMember_StaysOpaque403(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	seedUser(t, d, "bob", "bobpw12345", false)

	priv := seedSpace(t, d, "Alice Secret", "alice-secret", alice)
	var pageID int64
	if err := d.QueryRow(
		`INSERT INTO pages (space_id, parent_id, title, body, position)
		 VALUES ($1, NULL, 'Secret', 'x', 0) RETURNING id`, priv).Scan(&pageID); err != nil {
		t.Fatalf("seed page: %v", err)
	}

	bobC := loginClient(t, ts, "bob", "bobpw12345")
	resp, err := bobC.Get(fmt.Sprintf("%s/api/pages/%d", ts.URL, pageID))
	if err != nil {
		t.Fatalf("get page: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d want 403", resp.StatusCode)
	}
	var body struct {
		Code       string `json:"code"`
		PublicPath string `json:"public_path"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Code != "forbidden" || body.PublicPath != "" {
		t.Fatalf("code=%q public_path=%q want forbidden with no path", body.Code, body.PublicPath)
	}
}

// A search hit the caller can only see because the space is published must be
// flagged and linked at the no-login reader. Linking it at the authed route is
// what sent a real user into a 403 on content that was deliberately published —
// and the same `url` is what the MCP search tool hands to agents.
func TestSearch_PublicNonMemberHit_LinksToReader(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	bob := seedUser(t, d, "bob", "bobpw12345", false)

	pub := seedSpace(t, d, "Claude Notes", "claude-notes", alice)
	mustExec(t, d, `INSERT INTO pages (space_id, parent_id, title, body, position)
	                VALUES ($1, NULL, 'Claude 101', 'zqxwomble primer', 0)`, pub)
	mustExec(t, d, `UPDATE spaces SET visibility = 'public' WHERE id = $1`, pub)

	// Bob's own space, matching the same term, to prove membership still wins
	// the authed route for content he actually holds access to.
	own := seedSpace(t, d, "Bob Space", "bob-space", bob)
	mustExec(t, d, `INSERT INTO pages (space_id, parent_id, title, body, position)
	                VALUES ($1, NULL, 'Bob Notes', 'zqxwomble too', 0)`, own)

	bobC := loginClient(t, ts, "bob", "bobpw12345")
	var out struct {
		Results []struct {
			SpaceID int64  `json:"space_id"`
			Title   string `json:"title"`
			Public  bool   `json:"public"`
			URL     string `json:"url"`
		} `json:"results"`
	}
	getJSON(t, bobC, ts.URL+"/api/search?q=zqxwomble", &out)

	var sawPublic, sawOwn bool
	for _, r := range out.Results {
		switch r.SpaceID {
		case pub:
			sawPublic = true
			if !r.Public {
				t.Errorf("public-space hit %q: public=false, want true", r.Title)
			}
			// Canonical pretty reader URL, not the id form — this `url` is what
			// MCP/agent callers follow, and the id form canonicalizes away.
			if !strings.Contains(r.URL, "/alice/claude-notes/") {
				t.Errorf("public-space hit url=%q want the canonical reader URL", r.URL)
			}
		case own:
			sawOwn = true
			if r.Public {
				t.Errorf("own-space hit %q: public=true, want false", r.Title)
			}
			if !strings.Contains(r.URL, fmt.Sprintf("/spaces/%d/pages/", own)) {
				t.Errorf("own-space hit url=%q want the authed route", r.URL)
			}
		}
	}
	if !sawPublic || !sawOwn {
		t.Fatalf("results=%+v want both a public-space and an own-space hit", out.Results)
	}
}
