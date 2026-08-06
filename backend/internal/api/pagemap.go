package api

import (
	"fmt"
	"regexp"
	"strings"
)

// Surgical sub-document editing (T1): instead of an agent re-sending a whole page
// to change one part, get_page format:"map" returns just the heading outline, and
// patch_page edits ONE section by its heading path. Both rest on pageOutline, a
// fence-aware markdown section parser — a '#' inside a ``` block is not a heading.
// patch_page reassembles the body and writes it through the normal update path, so
// revisions / reindex / agreement / provenance all happen exactly as for any edit.

// A heading line, optionally inside a blockquote — `> ### Title` is how the
// authoring guide tells people to head a callout, and the "do this now" callout
// is often the most-edited block on a page, so it has to be patchable too.
var mapHeadingRE = regexp.MustCompile(`^((?:\s{0,3}>\s?)*)(#{1,6})\s+(.*\S)\s*$`)

// pageSection is one heading and the span of lines it governs: the heading line
// through the line before the next heading of the SAME OR HIGHER level — i.e. the
// whole section, subsections included. A quoted heading is additionally clamped
// to the end of its blockquote.
type pageSection struct {
	Level   int    `json:"level"`
	Heading string `json:"heading"`
	Path    string `json:"path"`              // "Parent > Child" breadcrumb — the patch_page target
	Preview string `json:"preview,omitempty"` // a short prose snippet of the section's own content
	InQuote bool   `json:"in_quote,omitempty"`

	headingLine int    // line index of the heading
	bodyStart   int    // first line after the heading
	end         int    // exclusive: next same-or-higher heading (or end of quote), or EOF
	quote       string // blockquote prefix of the heading line ("" outside a quote)
}

// isQuoteLine reports whether a line is still inside a blockquote.
func isQuoteLine(ln string) bool {
	return strings.HasPrefix(strings.TrimSpace(ln), ">")
}

// pageOutline parses a markdown body into its heading sections in document order,
// fence-aware. Line spans are half-open [headingLine,end).
func pageOutline(body string) []pageSection {
	lines := strings.Split(body, "\n")
	type hd struct {
		idx, level           int
		heading, path, quote string
	}
	var heads []hd
	var stack []string // heading text per level (index = level-1)
	inFence := false
	fenceTok := ""
	for i, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			tok := trimmed[:3]
			if !inFence {
				inFence, fenceTok = true, tok
			} else if tok == fenceTok {
				inFence = false
			}
			continue
		}
		if inFence {
			continue
		}
		m := mapHeadingRE.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		quote, level, title := m[1], len(m[2]), strings.TrimSpace(m[3])
		if len(stack) >= level {
			stack = stack[:level-1]
		} else {
			for len(stack) < level-1 {
				stack = append(stack, "")
			}
		}
		stack = append(stack, title)
		parts := make([]string, 0, len(stack))
		for _, s := range stack {
			if s != "" {
				parts = append(parts, s)
			}
		}
		heads = append(heads, hd{idx: i, level: level, heading: title, path: strings.Join(parts, " > "), quote: quote})
	}
	out := make([]pageSection, len(heads))
	for j, h := range heads {
		end := len(lines)
		for k := j + 1; k < len(heads); k++ {
			if heads[k].level <= h.level {
				end = heads[k].idx
				break
			}
		}
		// A heading inside a blockquote governs only the rest of that quote: the
		// first line that leaves it ends the section, whatever the heading levels
		// around it say.
		if h.quote != "" {
			for k := h.idx + 1; k < len(lines); k++ {
				if !isQuoteLine(lines[k]) {
					if k < end {
						end = k
					}
					break
				}
			}
		}
		// Preview spans only this section's OWN content — up to the very next
		// heading of any level — so a parent's preview isn't its whole subtree.
		ownEnd := end
		if j+1 < len(heads) && heads[j+1].idx < ownEnd {
			ownEnd = heads[j+1].idx
		}
		out[j] = pageSection{
			Level: h.level, Heading: h.heading, Path: h.path, InQuote: h.quote != "",
			headingLine: h.idx, bodyStart: h.idx + 1, end: end, quote: h.quote,
			Preview: sectionPreview(lines, h.idx+1, ownEnd),
		}
	}
	return out
}

var mdLinkRE = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
var mdStripper = strings.NewReplacer("**", "", "__", "", "*", "", "`", "", "~~", "")

// sectionPreview builds a short, prose-ish snippet from a section's content lines
// (fence-aware), stripping the loudest markdown so an agent can tell what a
// section is about without reading the body. Clamped to ~140 runes.
func sectionPreview(lines []string, start, end int) string {
	const cap = 140
	var b strings.Builder
	inFence := false
	for i := start; i < end && i < len(lines); i++ {
		ln := strings.TrimSpace(lines[i])
		if strings.HasPrefix(ln, "```") || strings.HasPrefix(ln, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || ln == "" {
			continue
		}
		ln = strings.TrimLeft(ln, "#>-*+| \t")
		ln = mdLinkRE.ReplaceAllString(ln, "$1")
		ln = strings.TrimSpace(mdStripper.Replace(ln))
		if ln == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(ln)
		if len([]rune(b.String())) >= cap {
			break
		}
	}
	r := []rune(b.String())
	if len(r) > cap {
		return strings.TrimRight(string(r[:cap]), " ") + "…"
	}
	return string(r)
}

// matchSection resolves a patch target to a section: exact path, then exact
// heading text, then a path suffix ("Production" matches "Deploy > Production").
// nil when nothing matches.
func matchSection(sections []pageSection, target string) *pageSection {
	t := strings.ToLower(strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(target), "#")))
	for i := range sections {
		if strings.ToLower(sections[i].Path) == t {
			return &sections[i]
		}
	}
	for i := range sections {
		if strings.ToLower(sections[i].Heading) == t {
			return &sections[i]
		}
	}
	for i := range sections {
		if strings.HasSuffix(strings.ToLower(sections[i].Path), "> "+t) {
			return &sections[i]
		}
	}
	return nil
}

func sectionPaths(sections []pageSection) string {
	ps := make([]string, len(sections))
	for i, s := range sections {
		ps[i] = s.Path
	}
	return strings.Join(ps, ", ")
}

// patchResult is one applied patch: the new body, the section it hit, and the
// sub-sections that went with it. Removed is what makes the destructive edge of
// `replace`/`delete` visible — a section spans its whole subtree, so replacing a
// `##` also drops every `###` under it. Silently, before this was reported.
type patchResult struct {
	Body    string
	Section *pageSection
	Removed []string // paths of nested sub-sections the op deleted
}

// applyPatch returns body with op applied at the section named by target. ops:
// append (after the section body), prepend (right under the heading), replace
// (swap the section body, heading kept), delete (remove heading + body). Content
// patched into a section that lives inside a blockquote (a callout heading) is
// re-prefixed so it stays in the quote instead of breaking out of it.
func applyPatch(body, target, op, content string) (patchResult, error) {
	sections := pageOutline(body)
	sec := matchSection(sections, target)
	if sec == nil {
		if len(sections) == 0 {
			return patchResult{}, fmt.Errorf("page has no headings to target; sections: (none)")
		}
		return patchResult{}, fmt.Errorf("section %q not found; sections: %s", target, sectionPaths(sections))
	}
	lines := strings.Split(body, "\n")
	add := strings.Split(strings.Trim(content, "\n"), "\n")
	blank := ""
	if sec.quote != "" {
		for i, ln := range add {
			add[i] = sec.quote + ln
		}
		blank = strings.TrimRight(sec.quote, " ")
	}

	var out []string
	switch op {
	case "append":
		out = append(out, lines[:sec.end]...)
		out = append(out, blank)
		out = append(out, add...)
		out = append(out, lines[sec.end:]...)
	case "prepend":
		out = append(out, lines[:sec.bodyStart]...)
		out = append(out, blank)
		out = append(out, add...)
		out = append(out, lines[sec.bodyStart:]...)
	case "replace":
		out = append(out, lines[:sec.bodyStart]...)
		out = append(out, blank)
		out = append(out, add...)
		out = append(out, blank)
		out = append(out, lines[sec.end:]...)
	case "delete":
		out = append(out, lines[:sec.headingLine]...)
		out = append(out, lines[sec.end:]...)
	default:
		return patchResult{}, fmt.Errorf("unknown operation %q (use append, prepend, replace, or delete)", op)
	}
	res := patchResult{Section: sec}
	if op == "replace" || op == "delete" {
		res.Removed = nestedPaths(sections, sec)
	}
	// Collapse any run of 3+ blank lines the splice may have introduced down to one.
	res.Body = collapseBlankRuns(strings.Join(out, "\n"))
	return res, nil
}

// nestedPaths lists the sub-sections that live inside sec's span — the ones an
// op over the whole section takes with it.
func nestedPaths(sections []pageSection, sec *pageSection) []string {
	var out []string
	for i := range sections {
		if s := &sections[i]; s.headingLine > sec.headingLine && s.headingLine < sec.end {
			out = append(out, s.Path)
		}
	}
	return out
}

var blankRunRE = regexp.MustCompile(`\n{3,}`)

func collapseBlankRuns(s string) string {
	return strings.Trim(blankRunRE.ReplaceAllString(s, "\n\n"), "\n") + "\n"
}
