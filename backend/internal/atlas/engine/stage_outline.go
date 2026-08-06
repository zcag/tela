package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/zcag/tela/backend/internal/atlas/core"
)

// outlineStage plans the wiki. An LLM designs the narrative pages (seeded with a
// condensed repo overview + the extracted surface), then deterministic reference
// pages are appended for every surface kind present — so routes/flags/env/etc.
// are GUARANTEED a home regardless of what the model's outline chose. This is the
// fix for "the docs only covered the obvious subsystems".
type outlineStage struct{}

func (outlineStage) Name() core.StageName { return core.StageOutline }

func (outlineStage) Run(ctx context.Context, rc *RunContext) error {
	jira := rc.Source != nil && rc.Source.Type == core.SourceJira
	rc.Info("planning pages from %d files / %d surface items", len(rc.Art.Files), len(rc.Art.Spine))

	// The page plan is source-type-aware. A repo is documented as a static
	// architecture synthesis; a Jira project is documented as a *tracker* — the
	// headline is its current state/progress, not an architecture overview — so it
	// gets a tracker-shaped overview brief and outline prompt.
	sys, user := outlineSystem, outlineUser(buildOverview(rc))
	if jira {
		sys, user = outlineJiraSystem, outlineJiraUser(buildJiraOverview(rc))
	}

	// atlas's outline is a single LLM call; an empty/truncated response (the model
	// occasionally returns one, esp. on a large surface) would otherwise fail the
	// whole run on the JSON parse. Retry a few times — temperature 0.3 varies the
	// output, so a transient bad response usually clears on the next attempt.
	const outlineAttempts = 3
	var planned []plannedPage
	var perr error
	for attempt := 1; attempt <= outlineAttempts; attempt++ {
		raw, cerr := rc.LLM.Chat(ctx, sys, user, 0.3)
		if cerr != nil {
			return cerr
		}
		planned, perr = parseOutline(raw)
		if perr == nil && len(planned) > 0 {
			break
		}
		rc.Warn("outline attempt %d/%d produced no usable plan (%v); retrying", attempt, outlineAttempts, perr)
	}
	if perr != nil {
		return fmt.Errorf("parse outline (after %d attempts): %w", outlineAttempts, perr)
	}
	if len(planned) == 0 {
		return fmt.Errorf("parse outline: model returned an empty plan after %d attempts", outlineAttempts)
	}

	var pages []core.Page
	order := 0
	for _, p := range planned {
		order++
		pages = append(pages, core.Page{Order: order, Kind: core.PageNarrative,
			Title: p.Title, Slug: slugify(p.Title), Summary: p.Summary, Topics: p.Topics})
	}
	// deterministic reference pages — only for surface kinds that exist. The set is
	// source-type-aware: a tracker leads with a Status Board (the state surface),
	// then its schema reference; a repo gets the code-shaped reference pages.
	refPages := []struct {
		title string
		kinds []core.SpineKind
	}{
		{"API & Routes", []core.SpineKind{core.KindRoute}},
		{"Components & Exported Types", []core.SpineKind{core.KindExport}},
		{"Entry Points, Flags & Environment", []core.SpineKind{core.KindEntrypoint, core.KindFlag, core.KindEnv}},
		{"Data & Persistence", []core.SpineKind{core.KindDBModel}},
		{"External Calls & Integrations", []core.SpineKind{core.KindOutbound}},
	}
	if jira {
		refPages = []struct {
			title string
			kinds []core.SpineKind
		}{
			{"Status Board", []core.SpineKind{core.KindState}},
			{"Schema: Types, Statuses & Components", []core.SpineKind{core.KindExport, core.KindConfig, core.KindDBModel}},
		}
	}
	for _, rp := range refPages {
		if len(rc.Art.SpineByKind(rp.kinds...)) == 0 {
			continue
		}
		order++
		pages = append(pages, core.Page{Order: order, Kind: core.PageReference,
			Title: rp.title, Slug: slugify(rp.title), SpineKinds: rp.kinds,
			Summary: "Complete reference, anchored to the extracted surface."})
	}

	rc.Art.Pages = pages
	if err := rc.Store.SavePages(rc.Run.ID, pages); err != nil {
		return err
	}
	rc.Info("planned %d pages (%d narrative + %d reference)", len(pages), len(planned), len(pages)-len(planned))
	return nil
}

type plannedPage struct {
	Title   string   `json:"title"`
	Summary string   `json:"summary"`
	Topics  []string `json:"topics"`
}

func parseOutline(raw string) ([]plannedPage, error) {
	js := extractJSONArray(raw)
	if js == "" {
		return nil, fmt.Errorf("no JSON array in model output")
	}
	var pages []plannedPage
	if err := json.Unmarshal([]byte(js), &pages); err != nil {
		return nil, err
	}
	// keep only well-formed entries
	out := pages[:0]
	for _, p := range pages {
		if strings.TrimSpace(p.Title) != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("outline had no titled pages")
	}
	return out, nil
}

// buildOverview condenses the repo into a planning brief: languages, the file
// tree clustered to top dirs, the surface summary with samples, and the README.
func buildOverview(rc *RunContext) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Repository: %s\n\n", filepath.Base(rc.Source.Location))

	// languages
	langCount := map[core.Lang]int{}
	for _, f := range rc.Art.Files {
		langCount[f.Lang]++
	}
	b.WriteString("## Languages: ")
	b.WriteString(topCounts(langCount, 8))
	b.WriteString("\n\n")

	// clustered tree (top-level dirs + file counts)
	dir := map[string]int{}
	for _, f := range rc.Art.Files {
		parts := strings.SplitN(f.Path, "/", 3)
		key := parts[0]
		if len(parts) > 1 {
			key = parts[0] + "/" + parts[1]
		}
		dir[key]++
	}
	b.WriteString("## Layout (path → files)\n")
	for _, kv := range sortedByVal(dir, 40) {
		fmt.Fprintf(&b, "- %s (%d)\n", kv.k, kv.v)
	}
	b.WriteString("\n")

	// surface summary + samples
	b.WriteString("## Extracted surface\n")
	b.WriteString(spineSummary(rc.Art.Spine))
	b.WriteString("\n")
	for _, k := range []core.SpineKind{core.KindRoute, core.KindEntrypoint, core.KindExport} {
		items := rc.Art.SpineByKind(k)
		if len(items) == 0 {
			continue
		}
		names := make([]string, 0, len(items))
		for _, it := range items {
			names = append(names, it.Name)
		}
		fmt.Fprintf(&b, "- %s: %s\n", k, sampleJoin(names, 25))
	}
	b.WriteString("\n")

	// README
	if rd := findReadme(rc); rd != "" {
		b.WriteString("## README (excerpt)\n")
		b.WriteString(truncStr(rd, 3000))
		b.WriteString("\n")
	}
	return b.String()
}

// buildJiraOverview condenses a Jira project into a tracker-planning brief: the
// current state (status.md verbatim — counts, epic progress, blocked/in-flight,
// recency) leading, then the project schema surface (types, statuses, components,
// epics) from schema.md. Parallel to buildOverview, but state-first: the plan it
// seeds is about *where the project stands*, not a code architecture.
func buildJiraOverview(rc *RunContext) string {
	var b strings.Builder
	key := strings.ToUpper(strings.Trim(rc.Source.Subpath, "/"))
	fmt.Fprintf(&b, "# Jira Project: %s (%d issues, %d surface items)\n\n", key, len(rc.Art.Files), len(rc.Art.Spine))

	if s := readSnapshotFile(rc, "status.md"); s != "" {
		b.WriteString("## Current state (status.md — the headline)\n")
		b.WriteString(truncStr(s, 6000))
		b.WriteString("\n\n")
	}
	if s := readSnapshotFile(rc, "schema.md"); s != "" {
		b.WriteString("## Project schema (schema.md — types, statuses, components, epics)\n")
		b.WriteString(truncStr(s, 4000))
		b.WriteString("\n\n")
	}

	// surface summary so the planner sees the must-cover state items.
	b.WriteString("## Extracted surface\n")
	b.WriteString(spineSummary(rc.Art.Spine))
	b.WriteString("\n")
	if items := rc.Art.SpineByKind(core.KindState); len(items) > 0 {
		names := make([]string, 0, len(items))
		for _, it := range items {
			names = append(names, it.Name)
		}
		fmt.Fprintf(&b, "- current-state items: %s\n", sampleJoin(names, 30))
	}
	return b.String()
}

// readSnapshotFile reads one acquired source file (status.md / schema.md) from the
// snapshot dir, "" if absent.
func readSnapshotFile(rc *RunContext, name string) string {
	data, err := os.ReadFile(filepath.Join(rc.Art.RepoDir, name))
	if err != nil {
		return ""
	}
	return core.SourceText(data)
}

func findReadme(rc *RunContext) string {
	for _, f := range rc.Art.Files {
		base := strings.ToLower(filepath.Base(f.Path))
		if strings.HasPrefix(base, "readme") {
			data, err := os.ReadFile(filepath.Join(rc.Art.RepoDir, f.Path))
			if err == nil {
				return core.SourceText(data)
			}
		}
	}
	return ""
}

// --- small helpers ---

var jsonArrayRe = regexp.MustCompile(`(?s)\[.*\]`)

func extractJSONArray(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	return jsonArrayRe.FindString(s)
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(s)
	s = slugRe.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

type kv struct {
	k string
	v int
}

func sortedByVal[K comparable](m map[K]int, limit int) []struct {
	k K
	v int
} {
	type p = struct {
		k K
		v int
	}
	out := make([]p, 0, len(m))
	for k, v := range m {
		out = append(out, p{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].v > out[j].v })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func topCounts(m map[core.Lang]int, limit int) string {
	out := sortedByVal(m, limit)
	parts := make([]string, len(out))
	for i, kv := range out {
		parts[i] = fmt.Sprintf("%s(%d)", kv.k, kv.v)
	}
	return strings.Join(parts, " ")
}

func sampleJoin(names []string, n int) string {
	if len(names) > n {
		return strings.Join(names[:n], ", ") + fmt.Sprintf(", … (+%d more)", len(names)-n)
	}
	return strings.Join(names, ", ")
}

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
