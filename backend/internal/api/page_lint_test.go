package api

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/zcag/tela/backend/internal/models"
	"github.com/zcag/tela/backend/internal/testdb"
)

// seedLintPage creates a space with one page and returns the server + page.
func seedLintPage(t *testing.T, title string) (*Server, models.Page) {
	t.Helper()
	ctx := context.Background()
	d := testdb.New(t)
	var spaceID int64
	if err := d.QueryRowContext(ctx,
		`INSERT INTO spaces(name, slug) VALUES ('General','general') RETURNING id`).Scan(&spaceID); err != nil {
		t.Fatalf("seed space: %v", err)
	}
	var id int64
	if err := d.QueryRowContext(ctx,
		`INSERT INTO pages(space_id, parent_id, title, body, position) VALUES ($1, NULL, $2, '', 0) RETURNING id`,
		spaceID, title).Scan(&id); err != nil {
		t.Fatalf("insert page: %v", err)
	}
	return &Server{DB: d}, models.Page{ID: id, SpaceID: spaceID, Title: title}
}

func lintRules(out pageLintOut) []string {
	rules := make([]string, len(out.Issues))
	for i, is := range out.Issues {
		rules[i] = is.Rule
	}
	return rules
}

func TestLintPage_DanglingWikilink(t *testing.T) {
	s, p := seedLintPage(t, "Home")

	out := s.lintPage(context.Background(), p, "See [[Nowhere At All]] for detail.\n")
	if got := lintRules(out); len(got) != 1 || got[0] != "dangling-wikilink" {
		t.Fatalf("rules = %v, want [dangling-wikilink]; issues %+v", got, out.Issues)
	}
	if out.OK || out.Warnings != 1 {
		t.Fatalf("out = %+v, want one warning", out)
	}

	// A link to a page that DOES exist in the space resolves and is silent —
	// including through a `|alias` and a `#heading` suffix.
	if out := s.lintPage(context.Background(), p, "See [[Home|the top]] and [[Home#Intro]].\n"); !out.OK {
		t.Fatalf("resolvable wikilinks should be clean, got %+v", out.Issues)
	}
}

// A wikilink shown as an EXAMPLE inside code is documentation, not a broken
// link — this is the false positive that would make the lint untrustworthy on
// tela's own docs pages, which are full of syntax samples.
func TestLintPage_WikilinkInCodeIsNotDangling(t *testing.T) {
	s, p := seedLintPage(t, "Home")
	body := "Write `[[Page Title]]` to link.\n\n```\n[[Another Missing Page]]\n```\n"
	if out := s.lintPage(context.Background(), p, body); !out.OK {
		t.Fatalf("code samples should be clean, got %+v", out.Issues)
	}
}

func TestLintPage_BrokenPageLink(t *testing.T) {
	s, p := seedLintPage(t, "Home")
	ctx := context.Background()

	// A link to the page itself resolves; a link to a made-up id does not.
	body := fmt.Sprintf("alive: tela://page/%d\n\ndead: /p/%d\n", p.ID, p.ID+9999)
	out := s.lintPage(ctx, p, body)
	if got := lintRules(out); len(got) != 1 || got[0] != "broken-page-link" {
		t.Fatalf("rules = %v, want [broken-page-link]; issues %+v", got, out.Issues)
	}
	if out.Issues[0].Line != 3 {
		t.Errorf("line = %d, want 3 (the dead link)", out.Issues[0].Line)
	}
}

func TestLintPage_MissingAttachment(t *testing.T) {
	s, p := seedLintPage(t, "Home")
	ctx := context.Background()
	const hash = "aa11bb22cc33dd44ee55ff6677889900aa11bb22cc33dd44ee55ff6677889900"

	body := fmt.Sprintf("![chart](/api/files/%d/%s.png)\n", p.SpaceID, hash)
	out := s.lintPage(ctx, p, body)
	if got := lintRules(out); len(got) != 1 || got[0] != "missing-attachment" {
		t.Fatalf("rules = %v, want [missing-attachment]; issues %+v", got, out.Issues)
	}

	// Once the file exists the reference resolves and the lint goes quiet. The
	// row is seeded in ANOTHER space on purpose: serving is content-addressed and
	// falls back across spaces, so a space mismatch must NOT read as broken.
	var otherSpace int64
	if err := s.DB.QueryRowContext(ctx,
		`INSERT INTO spaces(name, slug) VALUES ('Other','other') RETURNING id`).Scan(&otherSpace); err != nil {
		t.Fatalf("seed other space: %v", err)
	}
	if _, err := s.DB.ExecContext(ctx,
		`INSERT INTO space_files(space_id, name, content_hash, mime, data, byte_size)
		 VALUES ($1, 'chart.png', $2, 'image/png', '\x00', 1)`, otherSpace, hash); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if out := s.lintPage(ctx, p, body); !out.OK {
		t.Fatalf("existing attachment reported as missing: %+v", out.Issues)
	}
}

// The structural and resolution halves must arrive as one report, ordered by
// line — a caller shouldn't have to know there are two sources.
func TestLintPage_MergesBothHalves(t *testing.T) {
	s, p := seedLintPage(t, "Home")
	body := "> [!INFO]\n> unknown callout type\n\nand a [[Missing Page]] link\n"
	out := s.lintPage(context.Background(), p, body)
	if got := lintRules(out); strings.Join(got, ",") != "unknown-callout,dangling-wikilink" {
		t.Fatalf("rules = %v, want [unknown-callout dangling-wikilink]", got)
	}
	if out.Hint == "" {
		t.Error("a report with issues should point at preview_page")
	}
}

func TestLintPage_CleanBodyIsOK(t *testing.T) {
	s, p := seedLintPage(t, "Home")
	body := "# Title\n\n> [!NOTE]\n> A real callout.\n\n:::tabs\n### One\ncontent\n:::\n\n| a | b |\n| --- | --- |\n| 1 | 2 |\n"
	out := s.lintPage(context.Background(), p, body)
	if !out.OK {
		t.Fatalf("clean body reported %+v", out.Issues)
	}
}

// The advisory is what actually reaches an agent, so it must appear for a bad
// body, stay empty for a good one, and never be produced for deck/sheet pages
// (which have their own validation on the way in).
func TestWriteAdvisory(t *testing.T) {
	s, p := seedLintPage(t, "Home")
	ctx := context.Background()

	p.Body = "a [[Missing Page]] link\n"
	if got := s.writeAdvisory(ctx, p); !strings.Contains(got, "Missing Page") {
		t.Fatalf("advisory = %q, want it to name the broken link", got)
	}

	p.Body = "just prose\n"
	if got := s.writeAdvisory(ctx, p); got != "" {
		t.Fatalf("clean body advisory = %q, want empty", got)
	}

	p.Body = "a [[Missing Page]] link\n"
	p.Props = map[string]any{"deck": true}
	if got := s.writeAdvisory(ctx, p); got != "" {
		t.Fatalf("deck advisory = %q, want empty (lint_deck owns decks)", got)
	}
}
