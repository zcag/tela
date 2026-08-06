package core

import (
	"strings"
	"unicode/utf8"
)

// Lang is a source language id, derived from extension (+ shebang for scripts).
type Lang string

const (
	LangGo       Lang = "go"
	LangPython   Lang = "python"
	LangJava     Lang = "java"
	LangJS       Lang = "javascript"
	LangTS       Lang = "typescript"
	LangRust     Lang = "rust"
	LangRuby     Lang = "ruby"
	LangMarkdown Lang = "markdown"
	LangYAML     Lang = "yaml"
	LangJSON     Lang = "json"
	LangSQL      Lang = "sql"
	LangShell    Lang = "shell"
	LangText     Lang = "text"
	LangOther    Lang = "other"
)

// File is one ingested file (git-tracked, non-binary).
type File struct {
	ID    int64
	Path  string // relative to repo root
	Lang  Lang
	Size  int
	Lines int
	Hash  string // content SHA256 (hex), computed at inventory; delta-reuse safety check
}

// SpineKind classifies a surface item — the things a complete doc must cover.
type SpineKind string

const (
	KindEntrypoint SpineKind = "entrypoint" // main(), service bootstrap
	KindRoute      SpineKind = "route"      // HTTP route (method + path)
	KindExport     SpineKind = "export"     // public/exported symbol
	KindFlag       SpineKind = "cli_flag"   // CLI flag
	KindEnv        SpineKind = "env_var"    // environment variable read
	KindConfig     SpineKind = "config"     // config file / key
	KindOutbound   SpineKind = "outbound"   // outbound network call / external service
	KindDBModel    SpineKind = "db_model"   // persisted model / table / repository
	KindState      SpineKind = "state"      // tracker current-state surface (status counts, epic progress)
)

// KindState is a must-cover surface: for a tracker the current state (what's in
// each status, epic progress) is the headline a complete doc has to surface, the
// same way routes/entrypoints are for a service. Registered here (not in the
// MustCoverKinds literal) so the state kind and its must-cover policy live with
// the rest of the ingest model.
func init() { MustCoverKinds[KindState] = true }

// SpineItem is one element of the deterministic surface inventory. The set of
// these is the completeness checklist the generated docs are audited against.
type SpineItem struct {
	ID     int64     `json:"id"`
	Kind   SpineKind `json:"kind"`
	Name   string    `json:"name"` // canonical id, e.g. "GET /v1/render", "func Render"
	File   string    `json:"file"`
	Line   int       `json:"line"`
	Detail string    `json:"detail"`
}

// ChunkKind tags what a chunk represents.
type ChunkKind string

const (
	ChunkDecl ChunkKind = "decl" // a top-level declaration (func/class/type)
	ChunkFile ChunkKind = "file" // a whole small file
	ChunkDoc  ChunkKind = "doc"  // a section of prose/config
)

// Chunk is a retrievable unit of the repo, with the exact line range it came
// from so citations are verifiable.
type Chunk struct {
	ID        int64
	File      string
	StartLine int
	EndLine   int
	Kind      ChunkKind
	Symbol    string // associated symbol, if any
	Text      string
	Vector    []float32 // filled at embed stage
}

// Artifacts is the in-memory state threaded through a run's stages. Each stage
// reads what it needs and appends its output. Durable copies live in SQLite.
type Artifacts struct {
	RepoDir string // absolute path to the checked-out repo
	Files   []File
	Spine   []SpineItem
	Chunks  []Chunk
	Pages   []Page
}

// SpineByKind returns the surface items of a given kind (used by reference pages
// to guarantee they enumerate the whole surface).
func (a *Artifacts) SpineByKind(kinds ...SpineKind) []SpineItem {
	want := map[SpineKind]bool{}
	for _, k := range kinds {
		want[k] = true
	}
	var out []SpineItem
	for _, it := range a.Spine {
		if want[it.Kind] {
			out = append(out, it)
		}
	}
	return out
}

// SourceText decodes raw source-file bytes into text that is safe to store.
//
// A repo file is not guaranteed to be UTF-8 — a Latin-1/CP1252 source file is
// ordinary text with no NUL bytes, so it sails past the binary filter and then
// kills the run at the DB write: Postgres rejects the whole INSERT with
// `invalid byte sequence for encoding "UTF8"`, and since the run re-clones and
// re-chunks from scratch every time, it fails identically forever (source 19,
// laravel/framework: 46 consecutive failed runs before this was found).
//
// Invalid bytes become U+FFFD rather than dropping the file: the rest of it is
// still perfectly good retrievable text, and losing a file silently is worse
// than a few replacement runes in one line. Valid UTF-8 (the overwhelming
// majority) is returned unchanged with no allocation.
func SourceText(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return strings.ToValidUTF8(string(b), "�")
}
