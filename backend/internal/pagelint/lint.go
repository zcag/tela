// Package pagelint finds places where tela's READER renders a page differently
// from what its markdown source implies.
//
// WHY THIS IS NOT A STYLE LINTER: agents write a large share of tela's pages and
// cannot see the reader, so any transformation the renderer performs that the
// markdown doesn't spell out is undetectable from the writing side — by
// construction, re-reading your own source can never catch it. The `$…$`-eats-
// prose defect was exactly that shape: the source looked perfect, and two `$`
// signs in one paragraph silently swallowed the sentence between them. Every
// rule here targets that class — a real divergence between source and render —
// and nothing here is about taste, length, or heading hygiene. If a rule can't
// name what the reader will visibly do differently, it doesn't belong.
//
// The rules are deliberately line-oriented rather than a second mdast parse: a
// Go reimplementation of the frontend's remark stack would drift from it exactly
// where this package is supposed to be authoritative. What the reader recognizes
// (directive names, callout types) is INJECTED via Vocab from the generated
// blocks manifest, so the one thing that genuinely must agree with the frontend
// is generated and gate-checked (`make blocks-gate`), not hand-copied.
package pagelint

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Issue levels. `error` means content is lost or restructured — the render is
// missing something the author wrote. `warning` means the content survives but
// the block's chrome doesn't (a callout degrades to a plain quote, a directive
// unwraps to bare children).
const (
	LevelError   = "error"
	LevelWarning = "warning"
)

// maxIssues caps a single report. A body that trips hundreds of rules is broken
// in one systemic way; the first few say so and the rest are noise in a tool
// result an agent has to read.
const maxIssues = 60

// Issue is one divergence. Line is 1-based (0 when the issue isn't anchored to
// a line). Rule is a stable machine id so callers can filter without matching
// on prose.
type Issue struct {
	Line    int    `json:"line"`
	Level   string `json:"level"`
	Rule    string `json:"rule"`
	Message string `json:"message"`
}

// Vocab is what the reader actually recognizes, supplied by the caller from the
// generated manifest. Empty lists disable the corresponding unknown-name rules
// rather than flagging everything — a missing vocab must not turn every valid
// page into a wall of false positives.
type Vocab struct {
	Directives   []string
	CalloutTypes []string
	// DirectiveAliases maps a name an author plausibly writes instead of the
	// real one (a block's id or label) to the directive that actually renders,
	// so an unknown-directive report can name the fix instead of just the fault.
	DirectiveAliases map[string]string
}

// Directives whose body is projected through `###` headings — an empty one
// renders as literally nothing, which looks like a save that didn't take.
var headingSectioned = map[string]string{
	"tabs":   "tab",
	"kanban": "column",
	"stats":  "stat",
}

// HTML tags the reader honors. Everything else is dropped on the floor by
// MarkdownView's `case 'html': return null` — the tag AND its styling vanish
// while any text between the tags survives as plain prose, so the page silently
// loses emphasis or images with no trace in the source.
var renderedHTMLTags = map[string]bool{"details": true, "summary": true}

// `<br />` is written by TELA ITSELF: the editor's commonmark preset serializes
// every empty paragraph as `<br />` (`remarkPreserveEmptyLinePlugin`, see the
// note in milkdown-editor.tsx). So a page someone typed in the browser, adding
// no markup whatsoever, comes back full of them — and reporting that is the
// product blaming the author for its own output. Nothing visible goes wrong
// either (markdown already flows a single newline as a space), so there is no
// finding here at all, only noise on top of noise.
var cosmeticHTMLTags = map[string]bool{"br": true, "wbr": true}

// Real HTML element names authors actually reach for. Anything OUTSIDE this set
// that carries no attributes reads as a prose placeholder — `<org>`, `<commit>`,
// `<all>` — and CommonMark parses it as a tag just the same, so the word is
// deleted from the rendered page outright. That's a different (and worse)
// problem from `<span style>` losing its styling, and it needs different advice:
// wrap it in backticks. Same rule, two messages.
var htmlElements = map[string]bool{
	"a": true, "abbr": true, "article": true, "aside": true, "audio": true,
	"b": true, "blockquote": true, "button": true, "canvas": true, "caption": true,
	"center": true, "code": true, "col": true, "colgroup": true, "dd": true,
	"del": true, "div": true, "dl": true, "dt": true, "em": true, "embed": true,
	"fieldset": true, "figcaption": true, "figure": true, "font": true,
	"footer": true, "form": true, "h1": true, "h2": true, "h3": true, "h4": true,
	"h5": true, "h6": true, "header": true, "hr": true, "i": true, "iframe": true,
	"img": true, "input": true, "ins": true, "kbd": true, "label": true,
	"legend": true, "li": true, "main": true, "mark": true, "nav": true,
	"object": true, "ol": true, "option": true, "p": true, "picture": true,
	"pre": true, "q": true, "s": true, "samp": true, "script": true,
	"section": true, "select": true, "small": true, "source": true, "span": true,
	"strike": true, "strong": true, "style": true, "sub": true, "sup": true,
	"svg": true, "table": true, "tbody": true, "td": true, "textarea": true,
	"tfoot": true, "th": true, "thead": true, "time": true, "tr": true,
	"track": true, "u": true, "ul": true, "var": true, "video": true,
}

var (
	// The second group is the WHOLE rest of the line, not just the language: a
	// backtick fence's info string may not contain a backtick, which is what
	// separates a real fence from a wide code span like ```` ```lang ````.
	fenceRE     = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})(.*)$")
	directiveRE = regexp.MustCompile(`^ {0,3}(:{3,})[ \t]*([A-Za-z][A-Za-z0-9_-]*)?`)
	calloutRE   = regexp.MustCompile(`^ {0,3}>[ \t]?\[!([A-Za-z]+)\]`)
	sectionRE   = regexp.MustCompile(`^ {0,3}#{1,6}[ \t]`)

	detailsOpenRE  = regexp.MustCompile(`(?i)^[ \t]*<details(\s[^>]*)?>`)
	detailsCloseRE = regexp.MustCompile(`(?i)</details\s*>`)
	summaryRE      = regexp.MustCompile(`(?i)<summary[^>]*>.*?</summary\s*>`)

	// CommonMark's raw-tag grammar, not a loose `<word` match: only a syntactically
	// complete tag becomes an html node, and only an html node gets dropped. A bare
	// `a <b` or an autolink `<https://…>` stays prose and must not be reported.
	htmlTagRE = regexp.MustCompile(
		`</?[a-zA-Z][a-zA-Z0-9-]*(?:\s+[a-zA-Z_:][a-zA-Z0-9_.:-]*(?:\s*=\s*(?:[^\s"'=<>` + "`" + `]+|'[^']*'|"[^"]*"))?)*\s*/?>`)
	htmlNameRE   = regexp.MustCompile(`^</?([a-zA-Z][a-zA-Z0-9-]*)`)
	inlineCodeRE = regexp.MustCompile("`+[^`\n]*`+")

	fnRefRE = regexp.MustCompile(`\[\^([^\]\s]+)\]`)
	fnDefRE = regexp.MustCompile(`^ {0,3}\[\^([^\]\s]+)\]:`)

	inlineMathRE = regexp.MustCompile(`\$\$([^$\n]+)\$\$`)
	// Any of these inside `$$…$$` means it plausibly IS math; their absence in a
	// multi-word span is what makes it read as prose the reader will mangle.
	mathishRE = regexp.MustCompile(`[\\^_{}=+*/<>|~]|\\frac|\d`)

	tableDelimRE = regexp.MustCompile(`^ {0,3}\|?[ \t]*:?-+:?[ \t]*(\|[ \t]*:?-+:?[ \t]*)*\|?[ \t]*$`)

	// A CommonMark backslash escape makes the next punctuation character
	// LITERAL, so `\<all>` renders as visible text and is not a tag at all.
	// tela's own editor emits these escapes when it serializes, which means
	// anything a person typed in the browser arrives here already escaped —
	// scanning the raw text reported the editor's correct output as a defect.
	// Matching `\\` as one unit is what keeps `\\<div>` a real tag.
	escapedPunctRE = regexp.MustCompile(`\\[!"#$%&'()*+,\-./:;<=>?@\[\\\]^_` + "`" + `{|}~]`)
)

// Lint reports every divergence found in a page body, in source order.
func Lint(body string, v Vocab) []Issue {
	l := &linter{
		lines:    splitLines(body),
		known:    toSet(v.Directives),
		callouts: toSet(v.CalloutTypes),
		aliases:  v.DirectiveAliases,
	}
	l.scan()
	l.buildMasked()
	l.checkInline()
	l.checkFootnotes()
	// The passes run in rule order, not source order; the report reads top-down.
	sort.SliceStable(l.issues, func(a, b int) bool { return l.issues[a].Line < l.issues[b].Line })
	if len(l.issues) > maxIssues {
		l.issues = l.issues[:maxIssues]
	}
	return l.issues
}

type linter struct {
	lines    []string
	known    map[string]bool
	callouts map[string]bool
	aliases  map[string]string
	issues   []Issue

	// prose holds the indexes of lines that are neither fenced code nor block
	// math — the only lines whose inline syntax the reader interprets. masked is
	// the whole body, line-aligned, with code blanked out.
	prose  []int
	masked []string
}

// frame is one open `:::name` container.
type frame struct {
	name     string
	line     int
	sections int
}

func (l *linter) add(line int, level, rule, msg string) {
	l.issues = append(l.issues, Issue{Line: line, Level: level, Rule: rule, Message: msg})
}

func (l *linter) scan() {
	var (
		fenceChar byte
		fenceLen  int
		fenceInfo string
		fenceLine int
		fenceBody []string

		mathOpen bool
		mathLine int

		stack []frame
	)

	for i, raw := range l.lines {
		n := i + 1
		line := strings.TrimRight(raw, " \t")

		// ── fenced code ────────────────────────────────────────────────────
		if fenceLen > 0 {
			if m := fenceRE.FindStringSubmatch(line); m != nil &&
				m[1][0] == fenceChar && len(m[1]) >= fenceLen && strings.TrimSpace(m[2]) == "" {
				if fenceInfo == "chart" {
					l.checkChart(fenceLine, fenceBody)
				}
				fenceLen, fenceInfo, fenceBody = 0, "", nil
				continue
			}
			if fenceInfo == "chart" {
				fenceBody = append(fenceBody, raw)
			}
			continue
		}
		if m := fenceRE.FindStringSubmatch(line); m != nil && !(m[1][0] == '`' && strings.Contains(m[2], "`")) {
			fenceChar, fenceLen, fenceLine = m[1][0], len(m[1]), n
			fenceInfo = strings.ToLower(firstField(m[2]))
			continue
		}

		// ── block math ($$ on its own line) ────────────────────────────────
		if strings.TrimSpace(line) == "$$" {
			if mathOpen {
				mathOpen = false
			} else {
				mathOpen, mathLine = true, n
			}
			continue
		}
		if mathOpen {
			continue
		}

		l.prose = append(l.prose, i)

		// ── directives ─────────────────────────────────────────────────────
		if m := directiveRE.FindStringSubmatch(line); m != nil {
			if m[2] == "" { // bare `:::` closes the innermost container
				if len(stack) > 0 {
					l.closeFrame(stack[len(stack)-1])
					stack = stack[:len(stack)-1]
				}
				continue
			}
			name := m[2]
			if len(l.known) > 0 && !l.known[name] {
				if real := l.aliases[strings.ToLower(name)]; real != "" {
					l.add(n, LevelWarning, "unknown-directive", fmt.Sprintf(
						":::%s renders as its bare contents — %q is the block's name in the palette, not the directive you write. Use `:::%s`.",
						name, name, real))
				} else {
					l.add(n, LevelWarning, "unknown-directive", fmt.Sprintf(
						":::%s isn't a block the reader knows, so it renders as its bare contents — the block's frame, labels and any attributes are dropped. Known directives: %s.",
						name, l.knownList()))
				}
			}
			stack = append(stack, frame{name: name, line: n})
			continue
		}
		if len(stack) > 0 && sectionRE.MatchString(line) {
			stack[len(stack)-1].sections++
		}

		// ── callouts ───────────────────────────────────────────────────────
		if m := calloutRE.FindStringSubmatch(line); m != nil {
			t := strings.ToLower(m[1])
			if len(l.callouts) > 0 && !l.callouts[t] {
				l.add(n, LevelWarning, "unknown-callout", fmt.Sprintf(
					"[!%s] isn't a callout type, so this renders as an ordinary quote with the literal text \"[!%s]\" in it. Use one of: %s.",
					m[1], m[1], l.calloutList()))
			}
			continue
		}

		// ── collapsibles ───────────────────────────────────────────────────
		if detailsOpenRE.MatchString(line) {
			l.checkDetails(i)
		}
	}

	// ── unterminated at EOF ────────────────────────────────────────────────
	if fenceLen > 0 {
		l.add(fenceLine, LevelError, "unclosed-code-fence",
			"this code fence is never closed, so every line after it renders as code instead of prose. Close it with a matching fence.")
	}
	if mathOpen {
		l.add(mathLine, LevelError, "unclosed-math",
			"this `$$` math block is never closed, so the rest of the page renders as one formula. Close it with a `$$` line.")
	}
	for _, f := range stack {
		l.add(f.line, LevelError, "unclosed-directive", fmt.Sprintf(
			":::%s is never closed, so it swallows everything below it into the block. Close it with a `:::` line.", f.name))
	}
}

// closeFrame runs the checks that only make sense once a container's contents
// are known.
func (l *linter) closeFrame(f frame) {
	if unit, ok := headingSectioned[f.name]; ok && f.sections == 0 {
		l.add(f.line, LevelWarning, "empty-block", fmt.Sprintf(
			":::%s has no `###` headings, and each `###` inside it is one %s — so this renders as an empty block. Add a `### Label` per %s.",
			f.name, unit, unit))
	}
}

// checkDetails validates one `<details>` collapsible against how the reader
// actually assembles it. `collapsiblesRemark` pairs an OPENER html node with a
// separate CLOSER html node, which only happens when blank lines split them into
// separate blocks — remark otherwise merges consecutive lines into one raw html
// node, and raw html that isn't a recognized collapsible is dropped entirely.
// The failure is total and silent, which is why the missing blank line is an
// error rather than a formatting nit.
func (l *linter) checkDetails(start int) {
	// The opener's html block runs to the next blank line.
	end := start
	for end+1 < len(l.lines) && strings.TrimSpace(l.lines[end+1]) != "" {
		end++
	}
	chunk := strings.Join(l.lines[start:end+1], "\n")

	if detailsCloseRE.MatchString(chunk) {
		l.add(start+1, LevelError, "collapsible-not-split",
			"this whole collapsible is one unbroken HTML block, which the reader drops — nothing inside it appears on the page. Put a blank line after `</summary>` and another before `</details>`.")
		return
	}
	// Whatever remains after the opening tag and its summary is body text that
	// got absorbed into the opener node — the collapsible renders, but empty.
	rest := strings.TrimSpace(summaryRE.ReplaceAllString(
		detailsOpenRE.ReplaceAllString(chunk, ""), ""))
	if rest != "" {
		l.add(start+1, LevelError, "collapsible-body-absorbed",
			"this collapsible's body is on the same block as `<details>`, so it's absorbed into the opening tag and the collapsible renders empty. Put a blank line after `</summary>`.")
		return
	}
	// A closer anywhere below is enough. It does NOT need a blank line before it:
	// `</details>` is a CommonMark type-6 HTML block, which may interrupt a
	// paragraph, so it always becomes its own node and the collapsible still
	// pairs up. This package used to warn about that and was wrong 66 times
	// across the live wiki — measured in the reader: summary AND body both
	// render. Only the two cases above lose content.
	for i := end + 1; i < len(l.lines); i++ {
		if detailsCloseRE.MatchString(l.lines[i]) {
			return
		}
	}
	l.add(start+1, LevelError, "unclosed-collapsible",
		"this `<details>` is never closed, so the reader drops the opening tag and the body renders as loose text with no toggle.")
}

// checkChart validates a ```chart block's YAML. The reader renders a chart from
// this payload; malformed YAML doesn't fall back to showing the source, it just
// leaves a dead block on the page.
func (l *linter) checkChart(line int, body []string) {
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(strings.Join(body, "\n")), &doc); err != nil {
		l.add(line, LevelError, "chart-invalid", fmt.Sprintf(
			"this chart's YAML doesn't parse (%s), so the block renders empty instead of a chart.", firstLine(err.Error())))
		return
	}
	if doc == nil {
		l.add(line, LevelError, "chart-invalid", "this chart block is empty, so it renders as a blank frame.")
		return
	}
	if _, ok := doc["type"]; !ok {
		l.add(line, LevelError, "chart-invalid",
			"this chart has no `type:` (bar, line, pie, …), so the reader has nothing to draw.")
	}
}

// checkFootnotes reports references with no definition. remark leaves them as
// literal `[^1]` text in the prose — visible, wrong, and easy to miss when the
// definition was dropped by an edit somewhere else on the page.
func (l *linter) checkFootnotes() {
	defs := map[string]bool{}
	type ref struct {
		id   string
		line int
	}
	var refs []ref
	for _, i := range l.prose {
		line := l.lines[i]
		if m := fnDefRE.FindStringSubmatch(line); m != nil {
			defs[m[1]] = true
			continue
		}
		for _, m := range fnRefRE.FindAllStringSubmatch(l.masked[i], -1) {
			refs = append(refs, ref{m[1], i + 1})
		}
	}
	seen := map[string]bool{}
	for _, r := range refs {
		if defs[r.id] || seen[r.id] {
			continue
		}
		seen[r.id] = true
		l.add(r.line, LevelWarning, "undefined-footnote", fmt.Sprintf(
			"footnote [^%s] has no definition on this page, so it renders as the literal text \"[^%s]\" instead of a link. Add a `[^%s]: …` line.",
			r.id, r.id, r.id))
	}
}

// checkInline runs the per-line checks over prose lines: dropped raw HTML,
// prose captured as inline math, and ragged tables. It needs the prose index
// scan() builds, so it runs after it.
func (l *linter) checkInline() {
	inTable := false
	// Collected page-wide, not per line: the same tag repeated down a page is ONE
	// thing to fix, and emitting a row per occurrence buried the other rules under
	// a wall of identical warnings.
	html := map[string]*htmlHit{}
	for idx, i := range l.prose {
		line := l.lines[i]
		n := i + 1
		masked := l.masked[i]

		// Raw HTML the reader drops.
		for _, m := range htmlTagRE.FindAllString(masked, -1) {
			tag := strings.ToLower(htmlNameRE.FindStringSubmatch(m)[1])
			if renderedHTMLTags[tag] || cosmeticHTMLTags[tag] {
				continue
			}
			hit := html[tag]
			if hit == nil {
				hit = &htmlHit{line: n, placeholder: !htmlElements[tag] && !strings.ContainsAny(m, " \t")}
				html[tag] = hit
			}
			// Count opening tags only — `<span>x</span>` is one mistake, not two —
			// while still registering a lone stray closer.
			if !strings.HasPrefix(m, "</") {
				hit.count++
			}
		}

		// Prose captured inside `$$…$$`.
		for _, m := range inlineMathRE.FindAllStringSubmatch(masked, -1) {
			inner := strings.TrimSpace(m[1])
			if strings.Count(inner, " ") >= 1 && !mathishRE.MatchString(inner) {
				l.add(n, LevelWarning, "prose-as-math", fmt.Sprintf(
					"`$$%s$$` renders as a math formula, not text — `$$…$$` is tela's inline math delimiter. Remove the `$$` if this is prose.", inner))
			}
		}

		// Ragged GFM table: a row whose cell count differs from the header's.
		// Extra cells are dropped and missing ones filled blank, both silently.
		if !inTable && strings.Contains(line, "|") && idx+1 < len(l.prose) {
			next := l.lines[l.prose[idx+1]]
			if tableDelimRE.MatchString(next) && strings.Contains(next, "|") {
				inTable = true
				l.checkTable(idx)
			}
			continue
		}
		if inTable && strings.TrimSpace(line) == "" {
			inTable = false
		}
	}
	l.reportHTML(html)
}

// htmlHit is one raw tag name seen on a page: where it first appeared, how often,
// and whether it reads as a prose placeholder rather than a real element.
type htmlHit struct {
	line        int
	count       int
	placeholder bool
}

// reportHTML emits one issue per tag name, anchored at its first occurrence.
func (l *linter) reportHTML(html map[string]*htmlHit) {
	tags := make([]string, 0, len(html))
	for tag := range html {
		tags = append(tags, tag)
	}
	sort.Strings(tags) // map order is random; the report must not be
	for _, tag := range tags {
		hit := html[tag]
		times := ""
		if hit.count > 1 {
			times = fmt.Sprintf(" It appears %d times on this page.", hit.count)
		}
		if hit.placeholder {
			l.add(hit.line, LevelWarning, "dropped-html", fmt.Sprintf(
				"`<%s>` is read as an HTML tag, so it is deleted from the page — the reader shows nothing where you wrote it. Put it in backticks (`` `<%s>` ``) to keep it visible.%s",
				tag, tag, times))
			continue
		}
		l.add(hit.line, LevelWarning, "dropped-html", fmt.Sprintf(
			"<%s> is raw HTML, which the reader drops — the tag and anything it styles disappear, though text between the tags survives as plain prose. Use markdown or a tela block instead.%s",
			tag, times))
	}
}

// checkTable walks one table from its header row and reports rows whose cell
// count doesn't match.
func (l *linter) checkTable(headerIdx int) {
	header := countCells(l.lines[l.prose[headerIdx]])
	for j := headerIdx + 2; j < len(l.prose); j++ {
		line := l.lines[l.prose[j]]
		if strings.TrimSpace(line) == "" || !strings.Contains(line, "|") {
			return
		}
		if got := countCells(line); got != header {
			// Verified against the reader: the extra cell is NOT dropped, it
			// renders — as a stray column the header never declared, which is
			// why the table comes out misaligned rather than merely truncated.
			effect := "so it renders a stray column the header doesn't cover"
			if got < header {
				effect = "so it renders short and the columns stop lining up"
			}
			l.add(l.prose[j]+1, LevelWarning, "ragged-table", fmt.Sprintf(
				"this row has %d cells but the header has %d, %s. Match the header's column count.", got, header, effect))
			return // one report per table is enough to send the author back to it
		}
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

func splitLines(body string) []string {
	lines := strings.Split(body, "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	return lines
}

func toSet(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	m := make(map[string]bool, len(items))
	for _, s := range items {
		m[strings.ToLower(s)] = true
	}
	return m
}

func (l *linter) knownList() string   { return sortedList(l.known) }
func (l *linter) calloutList() string { return sortedList(l.callouts) }

func sortedList(m map[string]bool) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Stable order without pulling in sort for a handful of names.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return strings.Join(out, ", ")
}

// MaskCode returns body with every fenced code block and inline code span
// blanked to spaces, keeping the line structure (and therefore line numbers)
// intact. Anything that scans a body for syntax the reader interprets should
// run over this rather than the raw text, or a `<div>` or `[[Page]]` shown as
// an EXAMPLE gets reported as a defect. Exported because the resolution checks
// in internal/api need exactly the same treatment — one masking implementation,
// not two that disagree about what counts as code.
func MaskCode(body string) string {
	lines := splitLines(body)
	var fenceChar byte
	var fenceLen int
	for i, raw := range lines {
		line := strings.TrimRight(raw, " \t")
		m := fenceRE.FindStringSubmatch(line)
		if fenceLen > 0 {
			closing := m != nil && m[1][0] == fenceChar && len(m[1]) >= fenceLen && strings.TrimSpace(m[2]) == ""
			lines[i] = blank(raw)
			if closing {
				fenceLen = 0
			}
			continue
		}
		if m != nil && !(m[1][0] == '`' && strings.Contains(m[2], "`")) {
			fenceChar, fenceLen = m[1][0], len(m[1])
			lines[i] = blank(raw)
		}
	}
	// Code spans only after fences are gone, so a stray backtick inside a code
	// block can't open a span that swallows real prose below it.
	//
	// Escapes are deliberately NOT blanked here. This is the mask the wikilink
	// resolver in internal/api runs over too, and `[[Page\|alias]]` — the form
	// the editor writes inside a table — carries an escaped pipe that IS the
	// alias separator. Blanking it turned every aliased wikilink in a table into
	// a bogus "matches no page" report. Escapes are masked per-rule instead, in
	// buildMasked.
	return maskCodeSpans(strings.Join(lines, "\n"))
}

func blank(s string) string { return strings.Repeat(" ", len([]rune(s))) }

// buildMasked caches the masked body per line for the inline checks: code
// blanked, plus backslash escapes, since `\<all>` is literal text to those
// rules. Only the inline rules get the escape pass — see MaskCode.
func (l *linter) buildMasked() {
	masked := escapedPunctRE.ReplaceAllString(MaskCode(strings.Join(l.lines, "\n")), "  ")
	l.masked = strings.Split(masked, "\n")
}

// maskCodeSpans blanks every `…` span, honoring CommonMark's rule that a span
// opened by a run of N backticks closes only on a run of exactly N. An unclosed
// run is literal text and is left alone.
func maskCodeSpans(s string) string {
	b := []byte(s)
	out := append([]byte(nil), b...)
	runAt := func(i int) int {
		j := i
		for j < len(b) && b[j] == '`' {
			j++
		}
		return j
	}
	for i := 0; i < len(b); {
		if b[i] != '`' {
			i++
			continue
		}
		start := i
		i = runAt(i)
		n := i - start
		j := i
		for j < len(b) {
			if b[j] != '`' {
				j++
				continue
			}
			cs := j
			j = runAt(j)
			if j-cs == n {
				for k := start; k < j; k++ {
					if out[k] != '\n' {
						out[k] = ' '
					}
				}
				break
			}
		}
		i = j
	}
	return string(out)
}

func firstField(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

// countCells counts a GFM table row's cells, honoring `\|` escapes.
func countCells(line string) int {
	s := strings.TrimSpace(line)
	s = strings.TrimPrefix(s, "|")
	s = strings.TrimSuffix(s, "|")
	n := 1
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == '|' {
			n++
		}
	}
	return n
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
