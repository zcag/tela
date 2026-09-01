package engine

import (
	"strings"
	"testing"

	"github.com/zcag/tela/backend/internal/atlas/core"
)

// The real string run 134 published, and the shape every run since at least #108
// has been publishing: the draft prompt shows the citation format inside
// backticks (`Sources: [path:start-end]`) and the model copies them. linkify
// then absorbed the inner [ ] but could not see the outer backticks, so it wrote
// a markdown link INSIDE a code span — which renders as the literal
// [`path`](https://…), whole URL visible and unclickable.
func TestLinkify_CitationInsideCodeSpan(t *testing.T) {
	src := core.Source{Type: core.SourceGit, Location: "https://github.com/serpapi/SerpApi", Ref: "abc123"}
	files := []core.File{{Path: "config/locales/en.yml", Lines: 400}}

	got := linkifyCitations(src, files,
		"...search requests. `Sources: [config/locales/en.yml:205-215]`")

	// The wrapping code span must be gone: a leftover backtick before "Sources"
	// (or a stray trailing one) is what froze the link as literal text.
	if strings.Contains(got, "`Sources") || strings.HasSuffix(got, "`") {
		t.Fatalf("link markup is still inside a code span:\n%s", got)
	}
	if strings.Count(got, "`")%2 != 0 {
		t.Fatalf("unbalanced backticks — a code span is still open:\n%s", got)
	}
	if !strings.Contains(got, "](https://github.com/serpapi/SerpApi/blob/abc123/config/locales/en.yml#L205-L215)") {
		t.Fatalf("citation did not become a real link:\n%s", got)
	}
	// The label the reader sees must be the short path, not the URL.
	if !strings.Contains(got, "[`config/locales/en.yml:205-215`](") {
		t.Fatalf("link label is not the citation:\n%s", got)
	}
}

// A code span that is real CODE which merely mentions a path:line must keep its
// backticks and must NOT be rewritten — linkifying it would corrupt the code.
func TestLinkify_LeavesRealCodeAlone(t *testing.T) {
	src := core.Source{Type: core.SourceGit, Location: "https://github.com/serpapi/SerpApi", Ref: "abc123"}
	files := []core.File{{Path: "config/locales/en.yml", Lines: 400}}

	in := "call `open(\"config/locales/en.yml:205\")` to load it"
	if got := linkifyCitations(src, files, in); got != in {
		t.Fatalf("rewrote a real code span:\n got %s\nwant %s", got, in)
	}
}

// The ordinary inline form (no backticks) must keep working exactly as before.
func TestLinkify_PlainInlineCitationUnchanged(t *testing.T) {
	src := core.Source{Type: core.SourceGit, Location: "https://github.com/serpapi/SerpApi", Ref: "abc123"}
	files := []core.File{{Path: "app/models/search.rb", Lines: 90}}

	got := linkifyCitations(src, files, "the parser runs here. Sources: [app/models/search.rb:10-20]")
	if !strings.Contains(got, "[`app/models/search.rb:10-20`](https://github.com/serpapi/SerpApi/blob/abc123/app/models/search.rb#L10-L20)") {
		t.Fatalf("plain inline citation regressed:\n%s", got)
	}
}

// Fenced code must still be skipped entirely.
func TestLinkify_FencedCodeStillSkipped(t *testing.T) {
	src := core.Source{Type: core.SourceGit, Location: "https://github.com/serpapi/SerpApi", Ref: "abc123"}
	files := []core.File{{Path: "app/models/search.rb", Lines: 90}}

	in := "```\nsee app/models/search.rb:10\n```"
	if got := linkifyCitations(src, files, in); got != in {
		t.Fatalf("fenced code was linkified:\n%s", got)
	}
}
