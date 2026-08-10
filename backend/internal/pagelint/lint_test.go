package pagelint

import (
	"fmt"
	"strings"
	"testing"
)

// The real vocabulary, mirroring what the generated manifest supplies.
var vocab = Vocab{
	Directives:   []string{"quote", "tabs", "kanban", "stats", "embed", "file", "calendar", "timeline", "poll"},
	CalloutTypes: []string{"note", "tip", "important", "warning", "caution"},
}

func rules(issues []Issue) []string {
	out := make([]string, len(issues))
	for i, is := range issues {
		out[i] = is.Rule
	}
	return out
}

// wants asserts the exact set of rules fired, in order.
func wants(t *testing.T, body string, want ...string) []Issue {
	t.Helper()
	got := Lint(body, vocab)
	if strings.Join(rules(got), ",") != strings.Join(want, ",") {
		t.Fatalf("rules = %v, want %v\nissues: %+v", rules(got), want, got)
	}
	return got
}

func TestUnclosedConstructs(t *testing.T) {
	wants(t, "text\n```go\nfunc main() {}\n", "unclosed-code-fence")
	wants(t, "before\n$$\nE = mc^2\n", "unclosed-math")
	wants(t, ":::tabs\n### One\nbody\n", "unclosed-directive")
}

func TestClosedConstructsAreClean(t *testing.T) {
	body := "para\n\n```go\nfunc main() {}\n```\n\n$$\nE = mc^2\n$$\n\n:::tabs\n### One\nbody\n:::\n"
	wants(t, body)
}

// A tilde fence must not be closed by a backtick one, and a longer opener needs
// a matching-length closer.
func TestFenceMatching(t *testing.T) {
	wants(t, "~~~\ncode\n```\nstill code\n", "unclosed-code-fence")
	wants(t, "````\n```\nnested sample\n```\n````\n")
}

func TestUnknownDirectiveAndCallout(t *testing.T) {
	is := wants(t, ":::callout\nbody\n:::\n", "unknown-directive")
	if !strings.Contains(is[0].Message, "renders as its bare contents") {
		t.Errorf("message should say what the reader does: %q", is[0].Message)
	}
	wants(t, "> [!INFO]\n> body\n", "unknown-callout")
	wants(t, "> [!note]\n> lowercase is accepted by the renderer\n")
}

// An empty vocab must disable the unknown-name rules rather than flag everything.
func TestEmptyVocabDisablesNameRules(t *testing.T) {
	got := Lint(":::whatever\nbody\n:::\n\n> [!NOPE]\n> x\n", Vocab{})
	if len(got) != 0 {
		t.Fatalf("empty vocab should flag nothing, got %+v", got)
	}
}

func TestHeadingSectionedBlocks(t *testing.T) {
	wants(t, ":::tabs\njust prose, no headings\n:::\n", "empty-block")
	wants(t, ":::kanban\n### To do\n- [ ] card\n:::\n")
	// A `###` inside a nested fence must not count as a section.
	wants(t, ":::tabs\n```\n### not a tab\n```\n:::\n", "empty-block")
}

func TestCollapsibles(t *testing.T) {
	// Everything on one HTML block — the reader drops the whole thing.
	wants(t, "<details><summary>S</summary>\nbody\n</details>\n", "collapsible-not-split")
	// Body absorbed into the opener; the collapsible renders empty.
	wants(t, "<details><summary>S</summary>\nbody\n\n</details>\n", "collapsible-body-absorbed")
	// Closer joined to the body paragraph: this WORKS. `</details>` is a
	// type-6 HTML block, which may interrupt a paragraph, so it still becomes
	// its own node — verified in the reader, summary and body both render.
	wants(t, "<details><summary>S</summary>\n\nbody\n</details>\n")
	// Never closed.
	wants(t, "<details><summary>S</summary>\n\nbody\n", "unclosed-collapsible")
	// The documented, correct form.
	wants(t, "<details><summary>S</summary>\n\nbody\n\n</details>\n")
	wants(t, "<details open><summary>S</summary>\n\nbody\n\n</details>\n")
}

func TestDroppedHTML(t *testing.T) {
	is := wants(t, "a <div>styled</div> word\n", "dropped-html")
	if !strings.Contains(is[0].Message, "<div>") {
		t.Errorf("should name the tag: %q", is[0].Message)
	}
}

// A backslash escape makes the character literal, so `\<all>` renders as
// visible text and is not a tag. tela's OWN editor emits these escapes when it
// serializes, so scanning the raw text meant reporting the editor's correct
// output as a defect on every page a person had typed in the browser.
func TestEscapedPunctuationIsNotSyntax(t *testing.T) {
	wants(t, "cap: \\[‘netconf \\<all>‘]\n")
	wants(t, "the \\<org> placeholder is escaped\n")
	wants(t, "a claim.\\[^1]\n")
	// The unescaped form is still a real defect — that text does vanish.
	wants(t, "the <org> placeholder is bare\n", "dropped-html")
	// `\\` is an escaped BACKSLASH, so the tag after it is real.
	wants(t, "a literal \\\\<div>x</div>\n", "dropped-html")
}

// `<br>` is dropped like any other raw HTML, but nothing visible goes wrong —
// markdown already flows a single newline as a space — so reporting it was pure
// noise that buried the findings that matter.
func TestBrIsNotReported(t *testing.T) {
	wants(t, "line one<br>line two\n")
	wants(t, "cell<br />more\n")
	wants(t, "a<wbr>b\n")
}

// A tag repeated down a page is ONE thing to fix. Reporting each occurrence
// turned a single mistake into a wall of identical warnings.
func TestDroppedHTMLIsReportedOncePerTag(t *testing.T) {
	body := "<span>a</span> and <span>b</span>\n\nmore <span>c</span> here\n\n<div>x</div>\n"
	got := Lint(body, vocab)
	if len(got) != 2 {
		t.Fatalf("want one issue per tag (span, div), got %d: %+v", len(got), got)
	}
	if got[0].Line != 1 || !strings.Contains(got[0].Message, "appears 3 times") {
		t.Errorf("span should anchor at its first line and carry a count: %+v", got[0])
	}
	if strings.Contains(got[1].Message, "appears") {
		t.Errorf("a single occurrence should not carry a count: %q", got[1].Message)
	}
}

// A placeholder like <org>/<commit>/<all> is DELETED from the page — there's no
// inner text to survive — so it gets the "this vanishes, use backticks" message
// rather than the styling one.
func TestPlaceholderTagsGetTheirOwnMessage(t *testing.T) {
	is := wants(t, "run it against <all> repos\n", "dropped-html")
	if !strings.Contains(is[0].Message, "deleted from the page") ||
		!strings.Contains(is[0].Message, "backticks") {
		t.Errorf("placeholder message should say it vanishes + how to fix: %q", is[0].Message)
	}
	// A real element with attributes keeps the styling message.
	is = wants(t, `a <span style="color:red">word</span>`+"\n", "dropped-html")
	if !strings.Contains(is[0].Message, "text between the tags survives") {
		t.Errorf("element message should say inner text survives: %q", is[0].Message)
	}
}

// The rules must not fire on code samples, autolinks, or comparisons — the
// false-positive surface is what makes a lint trustworthy enough to act on.
func TestNoFalsePositives(t *testing.T) {
	wants(t, "use `<div>` for a block\n")
	wants(t, "```html\n<div class=\"x\">sample</div>\n```\n")
	wants(t, "see <https://example.com> for more\n")
	wants(t, "if a < b and c > d then\n")
	wants(t, "the threshold is <b in the worst case\n") // no closing `>`, so not a tag
	wants(t, "```\n:::tabs\n> [!NOPE]\n<div>\n[^9]\n```\n")
	wants(t, "an <!-- html comment --> is deliberate\n")
	// A wide code span showing a fence is not itself a fence: a backtick fence's
	// info string may not contain backticks.
	wants(t, "the ```` ```excalidraw ```` fence holds the scene\n\nprose after\n")
	// A code span may wrap across lines; the placeholder inside it is still code.
	wants(t, "run `docker compose up\nwith :<commit> pinned` to deploy\n")
}

func TestFootnotes(t *testing.T) {
	wants(t, "a claim.[^1]\n\n[^1]: the source\n")
	wants(t, "a claim.[^1]\n", "undefined-footnote")
	// Repeated references to the same missing note report once.
	wants(t, "one[^x] two[^x]\n", "undefined-footnote")
}

func TestProseAsMath(t *testing.T) {
	is := wants(t, "the $$quick brown fox$$ jumps\n", "prose-as-math")
	if !strings.Contains(is[0].Message, "renders as a math formula") {
		t.Errorf("unexpected message %q", is[0].Message)
	}
	// Real inline math is left alone.
	wants(t, "Euler's identity $$e^{i\\pi}+1=0$$ is elegant.\n")
	wants(t, "prices are $3,500 and $2,500 — no longer math\n")
	wants(t, "a $$x$$ single token isn't prose\n")
}

func TestRaggedTable(t *testing.T) {
	wants(t, "| a | b |\n| --- | --- |\n| 1 | 2 |\n")
	wants(t, "| a | b |\n| --- | --- |\n| 1 | 2 | 3 |\n", "ragged-table")
	wants(t, "| a | b |\n| --- | --- |\n| 1 |\n", "ragged-table")
	// An escaped pipe is content, not a cell boundary.
	wants(t, "| a | b |\n| --- | --- |\n| x \\| y | 2 |\n")
}

func TestChart(t *testing.T) {
	wants(t, "```chart\ntype: bar\nx: [Q1, Q2]\n```\n")
	wants(t, "```chart\ntype: bar\n  bad: [indent\n```\n", "chart-invalid")
	wants(t, "```chart\nx: [Q1]\n```\n", "chart-invalid")
	wants(t, "```chart\n```\n", "chart-invalid")
}

func TestIssuesAreOrderedByLine(t *testing.T) {
	body := "<div>a</div>\n\n> [!NOPE]\n> x\n\n:::tabs\n### one\n"
	got := Lint(body, vocab)
	for i := 1; i < len(got); i++ {
		if got[i].Line < got[i-1].Line {
			t.Fatalf("issues out of order: %+v", got)
		}
	}
}

func TestIssueCap(t *testing.T) {
	var b strings.Builder
	for i := 0; i < maxIssues+20; i++ {
		fmt.Fprintf(&b, "a claim.[^%d]\n\n", i)
	}
	if got := len(Lint(b.String(), vocab)); got != maxIssues {
		t.Fatalf("len = %d, want the cap %d", got, maxIssues)
	}
}

func TestEmptyBody(t *testing.T) {
	wants(t, "")
}
