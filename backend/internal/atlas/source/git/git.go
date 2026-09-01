// Package git is the source connector for git repositories — today's
// acquire/inventory/spine logic, moved behind source.Connector with no
// behaviour change. It is pure source logic: clone+pin, file enumeration via
// `git ls-files`, language classification, and the deterministic surface
// (spine) extraction (Go via go/ast, others via regex packs).
package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/zcag/tela/backend/internal/atlas/core"
	"github.com/zcag/tela/backend/internal/atlas/source"
)

// Connector implements source.Connector for git repositories.
type Connector struct{}

// New returns a git source connector.
func New() *Connector { return &Connector{} }

func (*Connector) Type() string { return string(core.SourceGit) }

// authURL injects the resolved credential into an https(s) git URL's userinfo
// for the duration of a single git command — so the token NEVER lands on
// core.Source.Location (which is printed in the overview page, logged, and
// emitted in run events). A username (GitHub "x-access-token", GitLab "oauth2",
// a real login) becomes user:token; with no username the token alone becomes
// the userinfo. No secret, or a non-http scheme (ssh/local), returns Location
// unchanged.
//
// When the credential carries no username the HOST supplies one — see
// defaultGitUser. That default is why a credential with an empty meta_json can
// still authenticate.
func authURL(src core.Source) string {
	if src.SecretValue == "" {
		return src.Location
	}
	u, err := url.Parse(src.Location)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return src.Location
	}
	user := src.SecretMeta["username"]
	if user == "" {
		user = defaultGitUser(u.Hostname())
	}
	if user != "" {
		u.User = url.UserPassword(user, src.SecretValue)
	} else {
		u.User = url.User(src.SecretValue)
	}
	return u.String()
}

// defaultGitUser is the git auth username to use when the credential carries
// none. The no-username form (token as the whole userinfo) is accepted by GitHub
// for CLASSIC ghp_ tokens but REJECTED for fine-grained github_pat_ ones — git
// then prompts for the missing password half and, with no tty, dies with
// "could not read Password ... No such device or address". That reads like an
// expired token, so the failure gets misattributed and the token re-issued,
// which doesn't help. Supplying the username the host expects is the fix.
//
// Deliberately narrow: only hosts where the no-username form is known to fail.
// An unknown host keeps the previous behaviour, because a wrong guess here
// breaks a working self-hosted remote and stays invisible until someone reads
// probe_error. Everything else sets meta.username explicitly (the UI offers it).
func defaultGitUser(host string) string {
	host = strings.ToLower(host)
	switch {
	case host == "github.com" || strings.HasSuffix(host, ".github.com"):
		// What actions/checkout uses; valid for classic AND fine-grained tokens.
		return "x-access-token"
	case host == "gitlab.com" || strings.HasSuffix(host, ".gitlab.com"):
		return "oauth2"
	}
	return ""
}

// redactSecret blanks the token in a string (git command output can echo the
// auth'd URL on failure) so it never surfaces in a run error / log / event.
// Both the raw token and its URL-percent-encoded form are replaced: authURL
// passes the token through url.User / url.UserPassword, which percent-encodes
// special characters (e.g. "@" → "%40"), so git may echo the encoded form.
//
// The encoded form is produced the way authURL produces it — USERINFO escaping,
// via url.User(...).String() — not url.PathEscape, which was the previous
// attempt at the same thing. They disagree on "@": PathEscape leaves it intact
// while userinfo escaping turns it into %40, so a token containing one used to
// slip through into run events and logs. Go escapes the username and password
// halves identically, so one form covers both positions.
func redactSecret(s, secret string) string {
	if secret == "" {
		return s
	}
	s = strings.ReplaceAll(s, secret, "***")
	if enc := url.User(secret).String(); enc != secret {
		s = strings.ReplaceAll(s, enc, "***")
	}
	return s
}

// Acquire clones the repo into workdir/repo and pins HEAD. Local paths and
// remote URLs are both cloned fresh (full clone — real line history for
// citations). Source.Branch, if set, selects a single branch.
func (*Connector) Acquire(ctx context.Context, src core.Source, workdir string) (source.Snapshot, error) {
	dst := filepath.Join(workdir, "repo")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return source.Snapshot{}, err
	}
	args := []string{"clone", "--quiet"}
	if src.Branch != "" {
		args = append(args, "--branch", src.Branch, "--single-branch")
	}
	args = append(args, authURL(src), dst)
	cmd := exec.CommandContext(ctx, "git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		// git can echo the (auth'd) clone URL on failure — redact the token so it
		// never reaches the run error / logs / events.
		return source.Snapshot{}, fmt.Errorf("git clone: %v: %s", err, redactSecret(strings.TrimSpace(string(out)), src.SecretValue))
	}
	sha, err := gitHead(ctx, dst)
	if err != nil {
		return source.Snapshot{}, err
	}
	return source.Snapshot{Dir: dst, Ref: sha}, nil
}

func gitHead(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Inventory enumerates the repo's tracked, in-scope, non-binary files via
// `git ls-files` (so .gitignore is honored for free), classifying each by
// language. Skips are counted, never silent — see InventoryWithProgress.
func (c *Connector) Inventory(ctx context.Context, snap source.Snapshot, src core.Source) ([]core.File, error) {
	files, _, err := c.InventoryWithProgress(ctx, snap, src, nil, nil)
	return files, err
}

// InventoryWithProgress is Inventory plus the optional progress hooks (scan
// announce + per-surviving-file tick) and the skip report. It implements
// source.ProgressConnector so the engine can reproduce today's progress/logs.
func (*Connector) InventoryWithProgress(ctx context.Context, snap source.Snapshot, src core.Source, onScan func(tracked int), onUnit source.Progress) ([]core.File, source.InventoryReport, error) {
	repo := snap.Dir
	out, err := exec.CommandContext(ctx, "git", "-C", repo, "ls-files", "-z").Output()
	if err != nil {
		return nil, source.InventoryReport{}, err
	}
	paths := splitZ(out)
	rep := source.InventoryReport{Tracked: len(paths)}
	if onScan != nil {
		onScan(len(paths))
	}

	sub, inc, exc := scopeFilter(src)
	var files []core.File
	for i, p := range paths {
		if err := ctx.Err(); err != nil {
			return nil, source.InventoryReport{}, err
		}
		if !scopeAllows(p, sub, inc, exc) {
			rep.Scoped++
			continue
		}
		abs := filepath.Join(repo, p)
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		if isBinary(data) {
			rep.Binary++
			continue
		}
		if len(bytes.TrimSpace(data)) == 0 {
			rep.Empty++
			continue
		}
		sum := sha256.Sum256(data)
		files = append(files, core.File{
			Path:  p,
			Lang:  classify(p, data),
			Size:  len(data),
			Lines: bytes.Count(data, []byte{'\n'}) + 1,
			Hash:  hex.EncodeToString(sum[:]),
		})
		if onUnit != nil {
			onUnit(i+1, len(paths))
		}
	}
	rep.Langs = countLangs(files)
	return files, rep, nil
}

// Spine builds the deterministic surface inventory from the inventoried files.
// Go is parsed for real (go/ast); other languages use tuned regex packs. Test
// files are excluded from the surface (kept for retrieval, not spine).
func (c *Connector) Spine(ctx context.Context, snap source.Snapshot, files []core.File) ([]core.SpineItem, error) {
	return c.SpineWithProgress(ctx, snap, files, nil)
}

// SpineWithProgress is Spine plus a per-file progress tick (index1, total over
// all files). It implements source.ProgressConnector.
func (*Connector) SpineWithProgress(ctx context.Context, snap source.Snapshot, files []core.File, onUnit source.Progress) ([]core.SpineItem, error) {
	var items []core.SpineItem
	for i, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if isTestFile(f.Path) {
			continue
		}
		src, err := os.ReadFile(filepath.Join(snap.Dir, f.Path))
		if err != nil {
			continue
		}
		switch f.Lang {
		case core.LangGo:
			items = append(items, extractGo(f.Path, src)...)
		default:
			items = append(items, extractRegex(f.Path, core.SourceText(src), f.Lang)...)
		}
		if onUnit != nil {
			onUnit(i+1, len(files))
		}
	}
	items = dedupeSpine(items)
	return items, nil
}

// Delta reports the in-scope files that changed between fromRef and toRef using
// `git diff --name-only --diff-filter=…`, applying the same subpath/include/
// exclude scoping as Inventory so only changes that would be ingested surface.
// fromRef is the prior run's commit; toRef the freshly-pinned one (snap.Ref).
func (*Connector) Delta(ctx context.Context, snap source.Snapshot, src core.Source, fromRef, toRef string) (source.ChangeSet, error) {
	sub, inc, exc := scopeFilter(src)
	var cs source.ChangeSet
	for _, f := range []struct {
		flag string
		dst  *[]string
	}{{"A", &cs.Added}, {"M", &cs.Modified}, {"D", &cs.Deleted}} {
		// --no-renames so a rename surfaces as delete-old + add-new — the right
		// shape for re-ingestion and publish-prune (the old path's page is an
		// orphan; the new path is fresh content).
		out, err := exec.CommandContext(ctx, "git", "-C", snap.Dir,
			"diff", "--name-only", "-z", "--no-renames", "--diff-filter="+f.flag, fromRef, toRef).Output()
		if err != nil {
			return source.ChangeSet{}, fmt.Errorf("git diff %s %s..%s: %w", f.flag, fromRef, toRef, err)
		}
		for _, p := range splitZ(out) {
			if scopeAllows(p, sub, inc, exc) {
				*f.dst = append(*f.dst, p)
			}
		}
	}
	return cs, nil
}

// HasChanges cheaply probes whether the remote HEAD differs from fromRef using
// `git ls-remote <Location> HEAD` — no clone. An empty fromRef (no baseline) is
// always "changed". Auth for private repos is injected into the command URL only
// (authURL) — never onto Source.Location. Branch, if set, scopes the probe.
func (*Connector) HasChanges(ctx context.Context, src core.Source, fromRef string) (bool, error) {
	if fromRef == "" {
		return true, nil
	}
	ref := "HEAD"
	if src.Branch != "" {
		ref = src.Branch
	}
	cmd := exec.CommandContext(ctx, "git", "ls-remote", authURL(src), ref)
	out, err := cmd.Output()
	if err != nil {
		// Mirror the Acquire pattern: use CombinedOutput-equivalent stderr scrub so
		// a git server that echoes the auth URL in its error output can't leak the token.
		var exitErr *exec.ExitError
		stderr := ""
		if errors.As(err, &exitErr) {
			stderr = ": " + redactSecret(strings.TrimSpace(string(exitErr.Stderr)), src.SecretValue)
		}
		return false, fmt.Errorf("git ls-remote: %v%s", err, stderr)
	}
	sha, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\t")
	if sha == "" {
		return false, fmt.Errorf("git ls-remote %s %s: no ref", src.Location, ref)
	}
	return sha != fromRef, nil
}

// --- inventory helpers -----------------------------------------------------

// scopeFilter compiles a source's subpath + include/exclude globs once.
func scopeFilter(src core.Source) (sub string, inc, exc []*regexp.Regexp) {
	return strings.Trim(src.Subpath, "/"), compileGlobs(src.Include), compileGlobs(src.Exclude)
}

// scopeAllows reports whether a path survives the subpath + include/exclude scoping.
func scopeAllows(p, sub string, inc, exc []*regexp.Regexp) bool {
	if sub != "" && p != sub && !strings.HasPrefix(p, sub+"/") {
		return false
	}
	if len(inc) > 0 && !anyMatch(inc, p) {
		return false
	}
	if anyMatch(exc, p) {
		return false
	}
	return true
}

func compileGlobs(csv string) []*regexp.Regexp {
	var out []*regexp.Regexp
	for _, pat := range strings.Split(csv, ",") {
		if pat = strings.TrimSpace(pat); pat != "" {
			out = append(out, globToRe(pat))
		}
	}
	return out
}

func anyMatch(res []*regexp.Regexp, p string) bool {
	for _, re := range res {
		if re.MatchString(p) {
			return true
		}
	}
	return false
}

// globToRe compiles a glob (with * = within a path segment, ** = across
// segments) to an anchored regexp.
func globToRe(pat string) *regexp.Regexp {
	var b strings.Builder
	b.WriteByte('^')
	for i := 0; i < len(pat); i++ {
		c := pat[i]
		switch {
		case c == '*' && i+1 < len(pat) && pat[i+1] == '*':
			if i+2 < len(pat) && pat[i+2] == '/' {
				b.WriteString("(?:.*/)?") // **/ matches zero or more dirs
				i += 2
			} else {
				b.WriteString(".*")
				i++
			}
		case c == '*':
			b.WriteString("[^/]*")
		case strings.IndexByte(`.+()|^$[]{}\?`, c) >= 0:
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('$')
	return regexp.MustCompile(b.String())
}

func splitZ(b []byte) []string {
	b = bytes.TrimRight(b, "\x00")
	if len(b) == 0 {
		return nil
	}
	parts := bytes.Split(b, []byte{0})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) > 0 {
			out = append(out, string(p))
		}
	}
	return out
}

// isBinary flags content with a NUL in the first 8KB (the standard heuristic).
func isBinary(b []byte) bool {
	n := min(len(b), 8192)
	return bytes.IndexByte(b[:n], 0) >= 0
}

var extLang = map[string]core.Lang{
	".go": core.LangGo, ".py": core.LangPython, ".java": core.LangJava,
	".js": core.LangJS, ".mjs": core.LangJS, ".cjs": core.LangJS, ".jsx": core.LangJS,
	".ts": core.LangTS, ".tsx": core.LangTS, ".rs": core.LangRust, ".rb": core.LangRuby,
	".md": core.LangMarkdown, ".mdx": core.LangMarkdown, ".rst": core.LangMarkdown,
	".yaml": core.LangYAML, ".yml": core.LangYAML, ".json": core.LangJSON,
	".sql": core.LangSQL, ".sh": core.LangShell, ".bash": core.LangShell,
	".txt": core.LangText, ".cfg": core.LangText, ".ini": core.LangText,
	".toml": core.LangText, ".properties": core.LangText, ".xml": core.LangText,
	".html": core.LangText, ".css": core.LangText, ".scss": core.LangText,
	".astro": core.LangJS, ".vue": core.LangJS, ".svelte": core.LangJS,
}

func classify(path string, data []byte) core.Lang {
	if l, ok := extLang[strings.ToLower(filepath.Ext(path))]; ok {
		return l
	}
	// shebang for extensionless scripts
	if bytes.HasPrefix(data, []byte("#!")) {
		first := data
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			first = data[:i]
		}
		switch {
		case bytes.Contains(first, []byte("python")):
			return core.LangPython
		case bytes.Contains(first, []byte("sh")):
			return core.LangShell
		case bytes.Contains(first, []byte("ruby")):
			return core.LangRuby
		case bytes.Contains(first, []byte("node")):
			return core.LangJS
		}
	}
	base := strings.ToLower(filepath.Base(path))
	if base == "dockerfile" || strings.HasPrefix(base, "makefile") {
		return core.LangText
	}
	return core.LangOther
}

func countLangs(fs []core.File) int {
	seen := map[core.Lang]bool{}
	for _, f := range fs {
		seen[f.Lang] = true
	}
	return len(seen)
}

// --- spine: Go (AST) -------------------------------------------------------

func extractGo(path string, src []byte) []core.SpineItem {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		return extractRegex(path, core.SourceText(src), core.LangGo) // fall back on parse error
	}
	var out []core.SpineItem
	add := func(k core.SpineKind, name string, pos token.Pos, detail string) {
		out = append(out, core.SpineItem{Kind: k, Name: name, File: path, Line: fset.Position(pos).Line, Detail: detail})
	}
	for _, d := range file.Decls {
		switch n := d.(type) {
		case *ast.FuncDecl:
			if n.Recv == nil && n.Name.Name == "main" {
				add(core.KindEntrypoint, "main()", n.Pos(), "package "+file.Name.Name)
			} else if n.Recv == nil && ast.IsExported(n.Name.Name) {
				add(core.KindExport, "func "+n.Name.Name, n.Pos(), "")
			}
		case *ast.GenDecl:
			if n.Tok == token.TYPE {
				for _, s := range n.Specs {
					ts := s.(*ast.TypeSpec)
					if ast.IsExported(ts.Name.Name) {
						add(core.KindExport, "type "+ts.Name.Name, ts.Pos(), "")
						if st, ok := ts.Type.(*ast.StructType); ok && hasDBTag(st) {
							add(core.KindDBModel, ts.Name.Name, ts.Pos(), "struct with db/gorm tags")
						}
					}
				}
			}
		}
	}
	// call-expression sweep: routes, flags, env, outbound
	ast.Inspect(file, func(node ast.Node) bool {
		ce, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := ce.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		name := sel.Sel.Name
		pkg := ""
		if id, ok := sel.X.(*ast.Ident); ok {
			pkg = id.Name
		}
		switch {
		case name == "Handle" || name == "HandleFunc":
			if s := firstStrLit(ce.Args); s != "" {
				add(core.KindRoute, normRoute(s), ce.Pos(), "mux."+name)
			}
		case isHTTPVerb(name) && len(ce.Args) >= 1:
			if s := strLit(ce.Args[0]); s != "" && strings.HasPrefix(s, "/") {
				add(core.KindRoute, strings.ToUpper(name)+" "+s, ce.Pos(), "router")
			}
		case pkg == "flag" && (name == "String" || name == "Int" || name == "Bool" || name == "Duration" || name == "Int64" || name == "Float64"):
			if s := firstStrLit(ce.Args); s != "" {
				add(core.KindFlag, "--"+s, ce.Pos(), "flag."+name)
			}
		case pkg == "os" && name == "Getenv":
			if s := firstStrLit(ce.Args); s != "" {
				add(core.KindEnv, s, ce.Pos(), "os.Getenv")
			}
		case pkg == "http" && (name == "Get" || name == "Post" || name == "NewRequest" || name == "NewRequestWithContext"):
			add(core.KindOutbound, "http."+name, ce.Pos(), "outbound HTTP")
		case name == "Do" && pkg != "":
			add(core.KindOutbound, pkg+".Do", ce.Pos(), "outbound HTTP client")
		}
		return true
	})
	return out
}

func hasDBTag(st *ast.StructType) bool {
	for _, f := range st.Fields.List {
		if f.Tag != nil && (strings.Contains(f.Tag.Value, "gorm:") || strings.Contains(f.Tag.Value, "db:")) {
			return true
		}
	}
	return false
}

func firstStrLit(args []ast.Expr) string {
	for _, a := range args {
		if s := strLit(a); s != "" {
			return s
		}
	}
	return ""
}

func strLit(e ast.Expr) string {
	if bl, ok := e.(*ast.BasicLit); ok && bl.Kind == token.STRING {
		return strings.Trim(bl.Value, "`\"")
	}
	return ""
}

func isHTTPVerb(n string) bool {
	switch n {
	case "Get", "Post", "Put", "Delete", "Patch", "Head", "Options":
		return true
	}
	return false
}

// normRoute turns a Go 1.22 "GET /x" pattern or a bare "/x" into a canonical id.
func normRoute(s string) string {
	if strings.Contains(s, " ") {
		return s
	}
	return s
}

// --- spine: regex packs (non-Go) -------------------------------------------

type rule struct {
	kind core.SpineKind
	re   *regexp.Regexp
	fmtf func(m []string) string
}

var rulesByLang = map[core.Lang][]rule{
	core.LangPython: {
		{core.KindRoute, regexp.MustCompile(`@\w+\.(get|post|put|delete|patch|route)\(\s*["']([^"']+)["']`), func(m []string) string {
			v := strings.ToUpper(m[1])
			if v == "ROUTE" {
				v = "ANY"
			}
			return v + " " + m[2]
		}},
		{core.KindExport, regexp.MustCompile(`(?m)^class\s+([A-Za-z_]\w*)`), func(m []string) string { return "class " + m[1] }},
		{core.KindExport, regexp.MustCompile(`(?m)^def\s+([a-zA-Z]\w*)`), func(m []string) string { return "def " + m[1] }},
		{core.KindEnv, regexp.MustCompile(`os\.environ(?:\.get)?\(?\s*["']([A-Z0-9_]+)["']`), func(m []string) string { return m[1] }},
		{core.KindEnv, regexp.MustCompile(`os\.getenv\(\s*["']([A-Z0-9_]+)["']`), func(m []string) string { return m[1] }},
		{core.KindFlag, regexp.MustCompile(`add_argument\(\s*["'](--[\w-]+)["']`), func(m []string) string { return m[1] }},
		{core.KindFlag, regexp.MustCompile(`@click\.option\(\s*["'](--[\w-]+)["']`), func(m []string) string { return m[1] }},
		{core.KindOutbound, regexp.MustCompile(`\b(requests|httpx)\.(get|post|put|delete|request)\b`), func(m []string) string { return m[1] + "." + m[2] }},
		{core.KindDBModel, regexp.MustCompile(`(?m)^class\s+(\w+)\((?:[\w.]*Base|models\.Model)`), func(m []string) string { return m[1] }},
	},
	core.LangJava: {
		{core.KindRoute, regexp.MustCompile(`@(Get|Post|Put|Delete|Patch|Request)Mapping\(\s*(?:value\s*=\s*)?[{\s]*["']([^"']+)["']`), func(m []string) string {
			v := strings.ToUpper(m[1])
			if v == "REQUEST" {
				v = "ANY"
			}
			return v + " " + m[2]
		}},
		{core.KindEntrypoint, regexp.MustCompile(`public\s+static\s+void\s+main\b`), func(m []string) string { return "main()" }},
		{core.KindExport, regexp.MustCompile(`public\s+(?:final\s+|abstract\s+)?(class|interface|enum|record)\s+(\w+)`), func(m []string) string { return m[1] + " " + m[2] }},
		{core.KindEnv, regexp.MustCompile(`System\.getenv\(\s*["']([A-Z0-9_]+)["']`), func(m []string) string { return m[1] }},
		{core.KindEnv, regexp.MustCompile(`@Value\(\s*["']\$\{([\w.]+)`), func(m []string) string { return m[1] }},
		{core.KindOutbound, regexp.MustCompile(`\b(RestTemplate|WebClient|HttpClient|RestClient)\b`), func(m []string) string { return m[1] }},
		{core.KindDBModel, regexp.MustCompile(`interface\s+(\w+)\s+extends\s+\w*Repository`), func(m []string) string { return m[1] }},
		{core.KindDBModel, regexp.MustCompile(`@Entity[\s\S]{0,80}?class\s+(\w+)`), func(m []string) string { return m[1] }},
	},
	core.LangSQL: {
		{core.KindDBModel, regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?["'` + "`" + `]?(\w+)`), func(m []string) string { return "table " + m[1] }},
	},
}

// JS/TS share a pack.
var jsRules = []rule{
	{core.KindRoute, regexp.MustCompile(`\b(?:app|router)\.(get|post|put|delete|patch|use)\(\s*["'` + "`" + `]([^"'` + "`" + `]+)`), func(m []string) string {
		return strings.ToUpper(m[1]) + " " + m[2]
	}},
	{core.KindExport, regexp.MustCompile(`export\s+(?:default\s+)?(?:async\s+)?(function|class|const|let)\s+(\w+)`), func(m []string) string { return m[1] + " " + m[2] }},
	{core.KindEnv, regexp.MustCompile(`process\.env\.([A-Z0-9_]+)`), func(m []string) string { return m[1] }},
	{core.KindEnv, regexp.MustCompile(`process\.env\[\s*["']([A-Z0-9_]+)["']`), func(m []string) string { return m[1] }},
	{core.KindOutbound, regexp.MustCompile(`\b(fetch|axios)\b`), func(m []string) string { return m[1] }},
}

func extractRegex(path, src string, lang core.Lang) []core.SpineItem {
	rules := rulesByLang[lang]
	if lang == core.LangJS || lang == core.LangTS {
		rules = jsRules
	}
	var out []core.SpineItem
	for _, r := range rules {
		for _, loc := range r.re.FindAllStringSubmatchIndex(src, -1) {
			m := groups(src, loc)
			name := r.fmtf(m)
			if name == "" {
				continue
			}
			out = append(out, core.SpineItem{Kind: r.kind, Name: name, File: path, Line: lineAt(src, loc[0]), Detail: lang2detail(lang)})
		}
	}
	return out
}

func groups(src string, loc []int) []string {
	m := make([]string, len(loc)/2)
	for i := 0; i < len(loc); i += 2 {
		if loc[i] >= 0 {
			m[i/2] = src[loc[i]:loc[i+1]]
		}
	}
	return m
}

func lineAt(src string, off int) int { return strings.Count(src[:off], "\n") + 1 }
func lang2detail(l core.Lang) string { return string(l) }

// isTestFile reports whether a path is test code (excluded from the surface
// inventory but kept as retrieval context).
func isTestFile(p string) bool {
	lp := strings.ToLower(p)
	for _, seg := range []string{"/test/", "/tests/", "/__tests__/", "/testdata/", "/e2e/", "/fixtures/"} {
		if strings.Contains(lp, seg) {
			return true
		}
	}
	base := filepath.Base(lp)
	switch {
	case strings.HasSuffix(base, "_test.go"),
		strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py"),
		strings.HasSuffix(base, "_test.py"),
		strings.HasSuffix(base, "test.java"), strings.HasSuffix(base, "tests.java"),
		strings.Contains(base, ".test."), strings.Contains(base, ".spec."):
		return true
	}
	return false
}

func dedupeSpine(items []core.SpineItem) []core.SpineItem {
	seen := map[string]bool{}
	out := items[:0]
	for _, it := range items {
		k := string(it.Kind) + "|" + it.Name + "|" + it.File
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
