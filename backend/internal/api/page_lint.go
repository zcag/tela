package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/zcag/tela/backend/internal/models"
	"github.com/zcag/tela/backend/internal/pagelint"
)

// Page lint — the ordinary-page analogue of lint_deck.
//
// A deck that's structurally broken fails loudly (Present 502s). A prose page
// never fails: the reader renders SOMETHING for any input, so a page whose
// markdown says one thing and whose render says another just sits there looking
// fine. Agents write a lot of pages and can't see the reader, so that gap is
// invisible from the writing side — this is the tool that closes it.
//
// Two halves, deliberately in different places. The structural rules are pure
// text and live in internal/pagelint (unit-testable, no DB, reusable). The
// resolution rules — does this [[wikilink]] actually point at a page, does this
// attachment still exist — need the database and can only live here. Both land
// in one report so a caller gets one answer to "is this page right?".

// pageLintIssue mirrors pagelint.Issue on the wire and adds the resolution
// findings, so the MCP tool, the REST route and the write-time advisory all
// speak one shape.
type pageLintIssue struct {
	Line    int    `json:"line"`
	Level   string `json:"level"`
	Rule    string `json:"rule"`
	Message string `json:"message"`
}

type pageLintOut struct {
	OK       bool            `json:"ok"`
	Errors   int             `json:"errors"`
	Warnings int             `json:"warnings"`
	Issues   []pageLintIssue `json:"issues"`
	Hint     string          `json:"hint,omitempty"`
}

// pageLintVocab is what the reader recognizes, read from the generated block
// manifest. Generated + gate-checked (`make blocks-gate`) rather than hand-
// copied, because a stale list here means the lint confidently reports a valid
// block as unknown.
var pageLintVocab = pagelint.Vocab{
	Directives:   manifestDirectives,
	CalloutTypes: manifestCalloutTypes,
}

// Body references the reader resolves at render time. Both forms are checked;
// `/p/{id}` is the short link people paste out of the URL bar.
var (
	pageRefRE = regexp.MustCompile(`tela://page/([0-9]+)|/p/([0-9]+)\b`)
	fileRefRE = regexp.MustCompile(`/api/files/([0-9]+)/([A-Za-z0-9._-]+)`)
)

// wrongBodyKind rejects a page whose body isn't prose. A deck's body is Slidev
// markdown and a sheet's is a Defter grid; the prose rules are meaningless there
// and actively wrong — a deck legitimately contains tahta components like
// `<callout>` and `<stat>`, which the raw-HTML rule would report as dropped,
// dozens of times, on a page that is perfectly fine. Each kind has its own
// validator, so point at it instead of guessing.
func wrongBodyKind(p models.Page) *apiErr {
	switch {
	case isDeckBag(p.Props):
		return &apiErr{http.StatusBadRequest, "wrong_tool",
			"this page is a deck — its body is Slidev markdown, not prose. Call lint_deck / preview_deck instead."}
	case isSheetBag(p.Props):
		return &apiErr{http.StatusBadRequest, "wrong_tool",
			"this page is a sheet — its body is a Defter grid, not prose. Call sheet_authoring_guide for its rules; edit_sheet validates on write."}
	}
	return nil
}

// lintPage runs every check over a page body and returns one merged report.
// `body` is passed separately from `p` so the editor can lint an unsaved draft.
// Callers must reject non-prose bodies with wrongBodyKind first.
func (s *Server) lintPage(ctx context.Context, p models.Page, body string) pageLintOut {
	var issues []pageLintIssue
	for _, is := range pagelint.Lint(body, pageLintVocab) {
		issues = append(issues, pageLintIssue{is.Line, is.Level, is.Rule, is.Message})
	}
	issues = append(issues, s.lintPageRefs(ctx, p, body)...)
	// The two halves are produced independently; the report reads top-down.
	sort.SliceStable(issues, func(a, b int) bool { return issues[a].Line < issues[b].Line })

	out := pageLintOut{Issues: issues}
	for _, is := range issues {
		if is.Level == pagelint.LevelError {
			out.Errors++
		} else {
			out.Warnings++
		}
	}
	out.OK = out.Errors == 0 && out.Warnings == 0
	if !out.OK {
		out.Hint = "These are differences between what the markdown says and what the reader shows. Call preview_page to read the page as it actually renders."
	}
	return out
}

// lintPageRefs reports references that resolve to nothing. Masked against code
// so a `[[Page Title]]` in a syntax example isn't reported as broken.
func (s *Server) lintPageRefs(ctx context.Context, p models.Page, body string) []pageLintIssue {
	masked := pagelint.MaskCode(body)
	var issues []pageLintIssue

	// ── [[wikilinks]] ───────────────────────────────────────────────────────
	// A wikilink to a title that doesn't exist in the space renders as plain
	// unlinked text — the page looks fine and the link is simply dead. This is
	// the standing trap when an agent writes cross-references before creating
	// the pages they point at.
	if refs := parseWikiTitleRefs(masked); len(refs) > 0 {
		bySlug, err := spaceTitleSlugIndex(ctx, s.DB, p.ID)
		if err == nil {
			seen := map[string]bool{}
			for _, r := range refs {
				if bySlug[r.Slug] != 0 || seen[r.Slug] {
					continue
				}
				seen[r.Slug] = true
				issues = append(issues, pageLintIssue{r.Line, pagelint.LevelWarning, "dangling-wikilink", fmt.Sprintf(
					"[[%s]] matches no page title in this space, so it renders as plain text instead of a link. Create the page first, or link an existing one by its exact title.", r.Raw)})
			}
		}
	}

	// ── page links ──────────────────────────────────────────────────────────
	if ids, lines := scanRefs(masked, pageRefRE); len(ids) > 0 {
		missing, err := s.missingPageIDs(ctx, ids)
		if err == nil {
			for _, id := range missing {
				issues = append(issues, pageLintIssue{lines[id], pagelint.LevelWarning, "broken-page-link", fmt.Sprintf(
					"page %d doesn't exist (or was deleted), so this link goes nowhere.", id)})
			}
		}
	}

	// ── attachments ─────────────────────────────────────────────────────────
	// A missing attachment is a broken image or a dead download — visible to
	// every reader, invisible in the markdown.
	for _, m := range fileRefRE.FindAllStringSubmatchIndex(masked, -1) {
		name := masked[m[4]:m[5]]
		if s.spaceFileExists(ctx, name) {
			continue
		}
		issues = append(issues, pageLintIssue{lineAt(masked, m[0]), pagelint.LevelWarning, "missing-attachment", fmt.Sprintf(
			"the attachment %s isn't in this space any more, so this renders as a broken image or a dead link.", name)})
	}
	return issues
}

// scanRefs pulls every page id out of body and remembers the first line each
// was seen on.
func scanRefs(body string, re *regexp.Regexp) ([]int64, map[int64]int) {
	var ids []int64
	lines := map[int64]int{}
	for _, m := range re.FindAllStringSubmatchIndex(body, -1) {
		raw := ""
		for g := 1; g*2+1 < len(m); g++ {
			if m[g*2] >= 0 {
				raw = body[m[g*2]:m[g*2+1]]
				break
			}
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		if _, ok := lines[id]; ok {
			continue
		}
		lines[id] = lineAt(body, m[0])
		ids = append(ids, id)
	}
	return ids, lines
}

func lineAt(body string, offset int) int {
	return 1 + strings.Count(body[:offset], "\n")
}

// missingPageIDs returns the subset of ids with no live page row. Deliberately
// NOT membership-filtered: a link to a page in another space is a legitimate
// link the reader may or may not be allowed to follow, and reporting it as
// broken would leak the existence of pages the caller can't see.
func (s *Server) missingPageIDs(ctx context.Context, ids []int64) ([]int64, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id FROM pages WHERE id = ANY($1) AND deleted_at IS NULL`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	live := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		live[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var missing []int64
	for _, id := range ids {
		if !live[id] {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

// spaceFileExists reports whether an /api/files/{space}/{hash}{ext} reference
// still resolves. It mirrors ServeSpaceFile's lookup exactly — content-addressed
// and NOT space-filtered, because that route deliberately falls back to any
// space holding the hash so embeds survive a cross-space page move. Filtering by
// the space in the URL here would report working attachments as broken.
//
// Only a definitive "no row" counts as missing: on any other error it answers
// true, since a lint that invents defects when the database hiccups is worse
// than one that misses them.
func (s *Server) spaceFileExists(ctx context.Context, name string) bool {
	hash := name
	if i := strings.IndexByte(hash, '.'); i > 0 {
		hash = hash[:i]
	}
	var n int
	err := s.DB.QueryRowContext(ctx,
		`SELECT 1 FROM space_files WHERE content_hash = $1 AND deleted_at IS NULL LIMIT 1`,
		hash).Scan(&n)
	return !errors.Is(err, sql.ErrNoRows)
}

// writeAdvisory is the lint note attached to an AGENT's create/update result.
//
// Nothing is blocked: a page is prose, the markdown is stored verbatim whatever
// the lint says, and refusing a save over an unknown callout type would fight
// the author for no gain — unlike a broken deck, which builds nothing at all.
// But the deck work established that agents don't call a lint tool on their own
// initiative, so the finding has to arrive unasked-for, in the result of the
// write that caused it, while fixing it is still one edit away.
//
// Best-effort: a lint failure must never turn a successful write into an error.
func (s *Server) writeAdvisory(ctx context.Context, p models.Page) string {
	if wrongBodyKind(p) != nil {
		return "" // deck/sheet — both have their own gate/lint on the way in
	}
	return lintAdvisory(s.lintPage(ctx, p, p.Body))
}

// lintAdvisory renders a report into the one-line-per-issue note attached to an
// agent's write. Capped: the point is to make the agent look, not to reproduce
// the whole report in a result it didn't ask for.
func lintAdvisory(out pageLintOut) string {
	if out.OK {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "This page renders differently from what the markdown implies — %d issue(s). Call lint_page for the full report, preview_page to see the rendered result:", len(out.Issues))
	for i, is := range out.Issues {
		if i >= 5 {
			fmt.Fprintf(&b, "\n  - …and %d more (lint_page)", len(out.Issues)-i)
			break
		}
		fmt.Fprintf(&b, "\n  - line %d: %s", is.Line, is.Message)
	}
	return b.String()
}

// PostPageLint (POST /api/pages/{id}/lint): session-authed. Lints the DRAFT
// editor buffer — the same report lint_page returns to an agent.
//
// Advisory ONLY, and not gated even for agents: a page is prose, and refusing a
// save because a callout type is unknown would fight the author for no gain
// (nothing is unrecoverable — the markdown is stored verbatim either way). The
// deck route learned the other half of this lesson: not surfacing it at all
// meant a human authoring in the browser got no signal whatsoever.
func (s *Server) PostPageLint(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requirePageRead(w, r)
	if !ok {
		return
	}
	if ae := wrongBodyKind(p); ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	draft, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not read body")
		return
	}
	body := p.Body
	if len(draft) > 0 {
		body = string(draft)
	}
	noStore(w)
	writeJSON(w, http.StatusOK, s.lintPage(r.Context(), p, body))
}
