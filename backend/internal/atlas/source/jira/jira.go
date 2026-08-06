// Package jira is the source connector for Jira projects — the second source
// type, proving the source.Connector abstraction holds with zero edits to the
// shared chunk→publish pipeline. It mirrors git's shape (acquire materializes the
// source on disk, inventory enumerates units, spine extracts the surface, delta
// reports changes) but the "source" is a Jira project's issues + schema fetched
// over the REST API instead of a cloned repo.
//
// Auth is HTTP Basic base64(email:token): the token is the resolved secret
// (src.SecretValue), the email is src.SecretMeta["email"]. The base URL is
// src.Location (e.g. https://x.atlassian.net) and the project key is src.Subpath.
// The base URL is injectable (via New) so tests can point at an httptest server.
package jira

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zcag/tela/backend/internal/atlas/core"
	"github.com/zcag/tela/backend/internal/atlas/source"
)

// Connector implements source.Connector for Jira projects. baseOverride, when
// set, replaces src.Location as the API base — tests inject an httptest URL here
// so no real Jira instance is needed.
type Connector struct {
	baseOverride string
}

// New returns a Jira source connector that talks to the source's own Location.
func New() *Connector { return &Connector{} }

// NewWithBase returns a connector whose API base is fixed to base, ignoring
// src.Location. Tests use it to target an httptest server.
func NewWithBase(base string) *Connector { return &Connector{baseOverride: base} }

func (*Connector) Type() string { return string(core.SourceJira) }

// clientFor builds an authed jiraClient for a source: base URL from the override
// or src.Location, Basic auth from the resolved secret (email in Meta, token in
// Value). A missing token still produces a client (public/anonymous Jira), the
// request just goes unauthenticated.
func (c *Connector) clientFor(src core.Source) *jiraClient {
	base := c.baseOverride
	if base == "" {
		base = src.Location
	}
	email := ""
	if src.SecretMeta != nil {
		email = src.SecretMeta["email"]
	}
	return newClient(base, email, src.SecretValue)
}

// projectKey returns the Jira project key for a source (its Subpath, uppercased).
func projectKey(src core.Source) string {
	return strings.ToUpper(strings.Trim(src.Subpath, "/"))
}

// Acquire fetches the project's issues + schema metadata over REST and renders
// them under workdir/jira: one <KEY-123>.md per issue and a schema.md describing
// the project's surface (issue types, statuses, components, versions, fields).
// Snapshot.Ref is the latest issue `updated` timestamp (RFC3339), so a later
// Delta can ask "what changed since this ref".
func (c *Connector) Acquire(ctx context.Context, src core.Source, workdir string) (source.Snapshot, error) {
	key := projectKey(src)
	if key == "" {
		return source.Snapshot{}, fmt.Errorf("jira: project key required (set source subpath)")
	}
	cl := c.clientFor(src)

	dir := filepath.Join(workdir, "jira")
	issuesDir := filepath.Join(dir, "issues")
	if err := os.MkdirAll(issuesDir, 0o755); err != nil {
		return source.Snapshot{}, err
	}

	issues, err := cl.searchAll(ctx, fmt.Sprintf("project=%s ORDER BY updated DESC", key))
	if err != nil {
		return source.Snapshot{}, err
	}

	latest := ""
	for _, is := range issues {
		body := renderIssue(is)
		if err := os.WriteFile(filepath.Join(issuesDir, is.Key+".md"), []byte(body), 0o644); err != nil {
			return source.Snapshot{}, err
		}
		if u := is.Fields.Updated; u > latest {
			latest = u
		}
	}

	meta, err := cl.projectMeta(ctx, key)
	if err != nil {
		return source.Snapshot{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "schema.md"), []byte(renderSchema(key, meta)), 0o644); err != nil {
		return source.Snapshot{}, err
	}

	// status.md captures the project's *current state* (status distribution, epic
	// progress, blocked/in-flight, recency) — what a tracker is actually about.
	// It's a real, citeable source: inventoried, chunked + embedded like any other
	// markdown, and its surface is must-cover via the spine (parseStatusSpine).
	status := computeStatus(issues, time.Now().UTC())
	if err := os.WriteFile(filepath.Join(dir, "status.md"), []byte(renderStatus(key, status)), 0o644); err != nil {
		return source.Snapshot{}, err
	}

	// Normalize the ref to RFC3339 so Delta's JQL comparison is stable. Fall back
	// to now if the project has no issues (empty project still acquires cleanly).
	ref := normalizeRef(latest)
	if ref == "" {
		ref = time.Now().UTC().Format(time.RFC3339)
	}
	return source.Snapshot{Dir: dir, Ref: ref}, nil
}

// Inventory walks the acquired snapshot for *.md files and returns them as
// markdown core.File units (issues + schema.md). Paths are snapshot-relative so
// they read naturally downstream (e.g. "issues/KEY-1.md", "schema.md").
func (c *Connector) Inventory(ctx context.Context, snap source.Snapshot, src core.Source) ([]core.File, error) {
	files, _, err := c.InventoryWithProgress(ctx, snap, src, nil, nil)
	return files, err
}

// InventoryWithProgress is Inventory plus the optional scan-announce / per-file
// progress hooks and the skip report, implementing source.ProgressConnector so
// the engine drives the same progress UX as git.
func (*Connector) InventoryWithProgress(ctx context.Context, snap source.Snapshot, src core.Source, onScan func(tracked int), onUnit source.Progress) ([]core.File, source.InventoryReport, error) {
	var paths []string
	err := filepath.Walk(snap.Dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(p, ".md") {
			rel, _ := filepath.Rel(snap.Dir, p)
			paths = append(paths, rel)
		}
		return nil
	})
	if err != nil {
		return nil, source.InventoryReport{}, err
	}
	sort.Strings(paths)
	rep := source.InventoryReport{Tracked: len(paths)}
	if onScan != nil {
		onScan(len(paths))
	}

	var files []core.File
	for i, rel := range paths {
		if err := ctx.Err(); err != nil {
			return nil, source.InventoryReport{}, err
		}
		data, err := os.ReadFile(filepath.Join(snap.Dir, rel))
		if err != nil {
			continue
		}
		if len(strings.TrimSpace(string(data))) == 0 {
			rep.Empty++
			continue
		}
		files = append(files, core.File{
			Path:  rel,
			Lang:  core.LangMarkdown,
			Size:  len(data),
			Lines: strings.Count(string(data), "\n") + 1,
		})
		if onUnit != nil {
			onUnit(i+1, len(paths))
		}
	}
	if len(files) > 0 {
		rep.Langs = 1 // markdown only
	}
	return files, rep, nil
}

// Spine extracts the Jira surface — the must-cover checklist — from the fetched
// schema (schema.md). Issue types + components → KindExport; statuses + custom
// fields + versions → KindConfig; epics → KindDBModel. All items point at
// schema.md (the deterministic surface lives there), deduped + ordered.
func (c *Connector) Spine(ctx context.Context, snap source.Snapshot, files []core.File) ([]core.SpineItem, error) {
	return c.SpineWithProgress(ctx, snap, files, nil)
}

// SpineWithProgress is Spine plus a per-line progress tick, implementing
// source.ProgressConnector.
func (*Connector) SpineWithProgress(ctx context.Context, snap source.Snapshot, files []core.File, onUnit source.Progress) ([]core.SpineItem, error) {
	data, err := os.ReadFile(filepath.Join(snap.Dir, "schema.md"))
	if err != nil {
		return nil, fmt.Errorf("jira spine: read schema.md: %w", err)
	}
	items := parseSchemaSpine(core.SourceText(data))

	// The current-state surface (per-status + per-epic progress) is a must-cover
	// part of the spine for a tracker, read back out of status.md the same way the
	// schema surface is read out of schema.md. status.md may be absent for very old
	// snapshots (acquired before this connector wrote it), so a missing file is not
	// fatal — the schema surface still stands.
	if sd, err := os.ReadFile(filepath.Join(snap.Dir, "status.md")); err == nil {
		items = append(items, parseStatusSpine(core.SourceText(sd))...)
	}

	if onUnit != nil {
		for i := range items {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			onUnit(i+1, len(items))
		}
	}
	return dedupeSpine(items), nil
}

// Delta reports the in-scope issues that changed since fromRef using JQL
// `project=KEY AND updated >= <fromRef>`. Every changed issue is Modified;
// those whose `created` is also >= fromRef are classified Added (new since the
// baseline). Paths match Inventory's ("issues/KEY-123.md") so re-ingestion +
// publish-prune line up. toRef is unused (the ref is the issue clock, not a tree
// id) but kept for interface symmetry.
func (c *Connector) Delta(ctx context.Context, snap source.Snapshot, src core.Source, fromRef, toRef string) (source.ChangeSet, error) {
	key := projectKey(src)
	cl := c.clientFor(src)
	jql := fmt.Sprintf("project=%s AND updated >= %q", key, jqlTime(fromRef))
	issues, err := cl.searchAll(ctx, jql)
	if err != nil {
		return source.ChangeSet{}, err
	}
	var cs source.ChangeSet
	for _, is := range issues {
		// The JQL boundary is timezone-approximate (JQL reads the literal in the
		// instance zone), so it over-fetches; trim to issues actually updated AFTER
		// the ref via the UTC-normalized compare. This makes the changeset exact and
		// drops the ref's own boundary issue (updated == ref).
		if normalizeRef(is.Fields.Updated) <= normalizeRef(fromRef) {
			continue
		}
		path := "issues/" + is.Key + ".md"
		if is.Fields.Created != "" && normalizeRef(is.Fields.Created) >= normalizeRef(fromRef) {
			cs.Added = append(cs.Added, path)
		} else {
			cs.Modified = append(cs.Modified, path)
		}
	}
	sort.Strings(cs.Added)
	sort.Strings(cs.Modified)
	return cs, nil
}

// HasChanges cheaply probes whether any issue changed since fromRef via a single
// small `project=KEY AND updated >= <fromRef>` search (maxResults=1): if even one
// issue comes back, there's something to re-ingest. An empty fromRef (no
// baseline) is always "changed". No on-disk materialize — just one count query
// against the live API, authed from the resolved secret on src.
func (c *Connector) HasChanges(ctx context.Context, src core.Source, fromRef string) (bool, error) {
	if fromRef == "" {
		return true, nil
	}
	key := projectKey(src)
	if key == "" {
		return false, fmt.Errorf("jira: project key required (set source subpath)")
	}
	// Compare instants in Go, not via a JQL `updated >= <literal>` probe: JQL reads
	// datetime literals in the instance's timezone, but the ref is UTC, so the probe
	// landed UTC-offset hours early and the ref's own boundary issue re-matched
	// forever (perpetual false "stale"). Fetch the most-recently-updated issue and
	// compare normalized (UTC RFC3339, lexically sortable) timestamps instead.
	latest, err := c.clientFor(src).latestUpdated(ctx, fmt.Sprintf("project=%s ORDER BY updated DESC", key))
	if err != nil {
		return false, err
	}
	if latest == "" {
		return false, nil // no issues — nothing to be stale against
	}
	return normalizeRef(latest) > normalizeRef(fromRef), nil
}

// --- spine extraction ------------------------------------------------------

// parseSchemaSpine reads the deterministic surface back out of schema.md. The
// schema is written by renderSchema with one fenced list per surface category;
// this reads those lists so the spine is a pure function of the acquired schema
// (no second network call). Line numbers point into schema.md.
func parseSchemaSpine(md string) []core.SpineItem {
	var items []core.SpineItem
	lines := strings.Split(md, "\n")
	section := ""
	for i, ln := range lines {
		if h := strings.TrimPrefix(ln, "## "); h != ln {
			section = strings.TrimSpace(h)
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(ln, "- "))
		if ln == name || name == "" { // not a list item
			continue
		}
		var kind core.SpineKind
		var detail string
		switch section {
		case "Issue Types":
			kind, detail = core.KindExport, "issue type"
		case "Components":
			kind, detail = core.KindExport, "component"
		case "Statuses":
			kind, detail = core.KindConfig, "status"
		case "Custom Fields":
			kind, detail = core.KindConfig, "custom field"
		case "Versions":
			kind, detail = core.KindConfig, "version"
		case "Epics":
			kind, detail = core.KindDBModel, "epic"
		default:
			continue
		}
		items = append(items, core.SpineItem{Kind: kind, Name: name, File: "schema.md", Line: i + 1, Detail: detail})
	}
	return items
}

// parseStatusSpine reads the current-state surface back out of status.md. Each
// `- status:<Name> (<n>) — …` line under "## Current state" and each
// `- epic:<Key> (<pct>%) — …` line under "## Epics & progress" becomes a
// KindState spine item whose Name is the leading token before the em-dash —
// `status:In Progress (8)` / `epic:ATL-3 (40%)`. That token is what the progress
// page is asked to echo, so coverage (which matches the item Name literally for
// non-route/export kinds) resolves against the prose. Line numbers point into
// status.md so the state surface is a citeable source.
func parseStatusSpine(md string) []core.SpineItem {
	var items []core.SpineItem
	lines := strings.Split(md, "\n")
	section := ""
	for i, ln := range lines {
		if h := strings.TrimPrefix(ln, "## "); h != ln {
			section = strings.TrimSpace(h)
			continue
		}
		item := strings.TrimSpace(strings.TrimPrefix(ln, "- "))
		if ln == item || item == "" { // not a list item
			continue
		}
		// the spine Name is the token before the " — " detail separator.
		name, detail := item, ""
		if k := strings.Index(item, " — "); k >= 0 {
			name, detail = strings.TrimSpace(item[:k]), strings.TrimSpace(item[k+len(" — "):])
		}
		var kind core.SpineKind
		var detailTag string
		switch section {
		case "Current state":
			if !strings.HasPrefix(name, "status:") {
				continue
			}
			kind, detailTag = core.KindState, "status"
		case "Epics & progress":
			if !strings.HasPrefix(name, "epic:") {
				continue
			}
			kind, detailTag = core.KindState, "epic progress"
		default:
			continue
		}
		if detail != "" {
			detailTag = detailTag + ": " + detail
		}
		items = append(items, core.SpineItem{Kind: kind, Name: name, File: "status.md", Line: i + 1, Detail: detailTag})
	}
	return items
}

func dedupeSpine(items []core.SpineItem) []core.SpineItem {
	seen := map[string]bool{}
	out := items[:0]
	for _, it := range items {
		k := string(it.Kind) + "|" + it.Name
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, it)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// --- ref helpers -----------------------------------------------------------

// normalizeRef parses a Jira timestamp (RFC3339 or Jira's "2006-01-02T15:04:05.000-0700")
// and re-emits RFC3339, so refs compare as plain strings. Unparseable input is
// returned unchanged.
func normalizeRef(s string) string {
	if s == "" {
		return ""
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05.000-0700", "2006-01-02T15:04:05.000Z0700"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return s
}

// jqlTime formats a ref for a JQL `updated >=` comparison: Jira expects
// "yyyy-MM-dd HH:mm" (no seconds, space-separated, no zone), so we floor to the
// minute in UTC. Unparseable refs fall back to a date-only form.
func jqlTime(ref string) string {
	r := normalizeRef(ref)
	if t, err := time.Parse(time.RFC3339, r); err == nil {
		return t.UTC().Format("2006-01-02 15:04")
	}
	if len(ref) >= 10 {
		return ref[:10]
	}
	return ref
}

