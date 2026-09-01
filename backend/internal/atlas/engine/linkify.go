package engine

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/zcag/tela/backend/internal/atlas/core"
	"github.com/zcag/tela/backend/internal/atlas/source"
)

// linkifyCitations rewrites resolvable `path:line` (or `path:l1-l2`) citations in
// a page body into clickable markdown links, using source.CiteURL for the target.
// It is publish-time only — the audit still operates on the raw `path:line` form.
//
// Rules:
//   - Only citations whose path resolves to a real corpus file (same suffix match
//     the audit uses) are linked; unresolvable ones (the BadCitations) are left
//     untouched as plain text.
//   - Matches inside fenced ``` code blocks are skipped (don't linkify code/log
//     lines), as are citations already wrapped in a markdown link.
//   - When CiteURL returns "" (unsupported host / non-issue jira path) the
//     citation is left as plain text.
func linkifyCitations(src core.Source, files []core.File, body string) string {
	lines := strings.Split(body, "\n")
	inFence := false
	for i, line := range lines {
		if isFence(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		lines[i] = linkifyLine(src, files, unwrapCitationCodeSpans(line))
	}
	return strings.Join(lines, "\n")
}

// isFence reports whether a line opens or closes a ``` / ~~~ code fence.
func isFence(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")
}

// linkifyLine replaces resolvable citations on a single (non-fenced) line with a
// single clean clickable link whose text is a code span: [`<cite>`](url). Any
// backticks and/or square brackets immediately wrapping the citation are consumed
// so the output has no stray delimiters — markdown would otherwise render
// `[cite](url)` as literal code, or [[cite](url)] double-bracketed.
func linkifyLine(src core.Source, files []core.File, line string) string {
	ms := citeRe.FindAllStringSubmatchIndex(line, -1)
	if ms == nil {
		return line
	}
	spans := inlineCodeSpans(line)
	var b strings.Builder
	last := 0
	for _, m := range ms {
		start, end := m[0], m[1]
		// already inside a markdown link target/label → leave it.
		if alreadyLinked(line, start, end) {
			continue
		}
		// Inside an inline code span → leave it. Writing a markdown link into a
		// code span produces markup that can never render: the reader sees the
		// literal [`path`](https://…) including the whole URL. Fenced blocks were
		// always skipped (above); inline spans were not, which is how every run
		// since at least #108 published broken citations.
		if inSpan(spans, start, end) {
			continue
		}
		cite := line[start:end]
		path := line[m[2]:m[3]]
		l1, _ := strconv.Atoi(line[m[4]:m[5]])
		l2 := l1
		if m[6] >= 0 {
			l2, _ = strconv.Atoi(line[m[6]:m[7]])
		}
		realPath, _, ok := resolveCite(files, path)
		if !ok {
			continue // unresolvable: a BadCitation, leave as-is
		}
		url := source.CiteURL(src, realPath, l1, l2)
		if url == "" {
			continue // no clickable target for this source/host
		}
		// consume any wrapping delimiters: backticks (Sources list / inline code
		// spans) and/or [ ] (the inline "Sources: [path:line]" form).
		ws, we := absorbDelims(line, start, end)
		if ws < last { // overlaps an already-emitted span
			continue
		}
		b.WriteString(line[last:ws])
		b.WriteString("[`")
		b.WriteString(cite)
		b.WriteString("`](")
		b.WriteString(url)
		b.WriteString(")")
		last = we
	}
	b.WriteString(line[last:])
	return b.String()
}

// absorbDelims widens [start,end) to swallow a single pair of backticks and/or a
// single pair of square brackets immediately wrapping the citation, in either
// nesting order (`[cite]`, [`cite`], `cite`, [cite]). Returns the widened bounds.
func absorbDelims(line string, start, end int) (int, int) {
	for {
		grew := false
		if start >= 1 && end < len(line) && line[start-1] == '`' && line[end] == '`' {
			start, end, grew = start-1, end+1, true
		}
		if start >= 1 && end < len(line) && line[start-1] == '[' && line[end] == ']' {
			start, end, grew = start-1, end+1, true
		}
		if !grew {
			return start, end
		}
	}
}

// alreadyLinked reports whether the match at [start,end) is already part of a
// markdown link — either its label (preceded by '[', followed by "](") or its
// target (immediately preceded by "](").
func alreadyLinked(line string, start, end int) bool {
	if start >= 1 && line[start-1] == '[' && strings.HasPrefix(line[end:], "](") {
		return true
	}
	if start >= 2 && line[start-2] == ']' && line[start-1] == '(' {
		return true
	}
	return false
}

// --- inline code spans -----------------------------------------------------

// sourcesLabelRE is the "Sources:" label the draft prompt asks for inline. The
// prompt shows the format inside backticks — `Sources: [path:start-end]` — and
// the model copies those backticks into its answer often enough to matter.
var sourcesLabelRE = regexp.MustCompile(`(?i)^sources?\s*:\s*`)

// citeFillerRE is everything allowed to surround citations inside such a span.
var citeFillerRE = regexp.MustCompile(`[\[\](),;\s]+`)

// unwrapCitationCodeSpans drops the backticks around a code span that holds
// nothing but citations (optionally labelled "Sources:"), so the citation can be
// linked instead of frozen as literal code.
//
// It only unwraps spans that are ENTIRELY citations: a span mentioning a
// path:line amid real code (`open("a.py:12")`) keeps its backticks and is then
// skipped by the inSpan guard, which is the behaviour code deserves.
func unwrapCitationCodeSpans(line string) string {
	return codeSpanRE.ReplaceAllStringFunc(line, func(span string) string {
		inner := span[1 : len(span)-1]
		rest := sourcesLabelRE.ReplaceAllString(inner, "")
		if !citeRe.MatchString(rest) {
			return span // no citation in here — leave the code span alone
		}
		if citeFillerRE.ReplaceAllString(citeRe.ReplaceAllString(rest, ""), "") != "" {
			return span // there is real content besides citations — keep it code
		}
		return inner
	})
}

// codeSpanRE matches a single-backtick inline code span.
var codeSpanRE = regexp.MustCompile("`[^`]*`")

// inlineCodeSpans returns the [start,end) ranges of the line's code spans.
func inlineCodeSpans(line string) [][]int { return codeSpanRE.FindAllStringIndex(line, -1) }

// inSpan reports whether [start,end) falls inside any of the given ranges.
func inSpan(spans [][]int, start, end int) bool {
	for _, s := range spans {
		if start >= s[0] && end <= s[1] {
			return true
		}
	}
	return false
}
