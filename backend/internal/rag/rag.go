// Package rag is tela's self-contained semantic-retrieval layer: heading-aware
// markdown chunking, pluggable embeddings (Ollama by default), and hybrid
// (lexical + vector, RRF-fused) chunk search over the page_chunks table.
//
// Storage is Postgres + pgvector (migrations 0002/0003). Two sticky invariants
// shape every query here:
//
//   - The index is a DISPOSABLE, derived cache of `pages`. page_chunks is fully
//     rebuildable via ReindexPage/ReindexSpace; backend choices stay reversible.
//   - Authorize through the LIVE page row, in-query. Every retrieval joins
//     page_chunks → pages → space_access; the chunk carries NO permission copy
//     (there is deliberately no space_id column on page_chunks). This is the
//     anti-leak invariant — a chunk can never out-live or out-scope its page.
//
// Wire-in is one field on api.Server plus the routes in api/rag.go. When
// TELA_RAG_EMBED_URL is unset the feature no-ops (Enabled()==false; handlers
// 503), so it ships dark until explicitly configured.
package rag

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strconv"
	"sync"
	"time"
)

// Config is the env-driven configuration. EmbedURL empty => feature disabled.
type Config struct {
	EmbedURL   string // Ollama base, e.g. http://ollama-host:11435, or tela cloud's /api/cloud/ollama
	EmbedModel string // default qwen3-embedding:0.6b (1024-d)
	EmbedToken string // optional bearer (a tela PAT) when EmbedURL is the managed cloud endpoint
	Dim        int    // expected embedding dimension (advisory; column is vector(1024))

	// Reranking (optional second-stage precision). Empty RerankURL => disabled
	// (hybrid+RRF order is returned as-is). When set, the top fused candidates are
	// re-scored by a cross-encoder before the final trim.
	RerankURL   string // full /rerank endpoint (Cohere/Jina-compatible shape)
	RerankModel string
	RerankToken string
}

// ConfigFromEnv reads the TELA_RAG_* env. The default embed model tracks the prod
// instance (qwen3-embedding:0.6b, 1024-d). Reranking is off unless
// TELA_RAG_RERANK_URL is set.
func ConfigFromEnv() Config {
	return Config{
		EmbedURL:    os.Getenv("TELA_RAG_EMBED_URL"),
		EmbedModel:  getenv("TELA_RAG_EMBED_MODEL", "qwen3-embedding:0.6b"),
		EmbedToken:  os.Getenv("TELA_RAG_EMBED_TOKEN"),
		Dim:         atoiDefault(os.Getenv("TELA_RAG_EMBED_DIM"), 1024),
		RerankURL:   os.Getenv("TELA_RAG_RERANK_URL"),
		RerankModel: os.Getenv("TELA_RAG_RERANK_MODEL"),
		RerankToken: os.Getenv("TELA_RAG_RERANK_TOKEN"),
	}
}

// Embedder turns text into a vector. One method (plus Model for cache-keying),
// so swapping Ollama for a hosted OpenAI-compatible endpoint later is a new
// file implementing this interface, not a refactor.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	Model() string
}

// Service bundles the DB handle, config, the active embedder, and an optional
// reranker. A nil embedder means the feature is disabled; a nil reranker means
// the fused order is returned as-is.
type Service struct {
	db  *sql.DB
	cfg Config
	emb Embedder
	rr  Reranker

	// Auto-reindex queue (see autoreindex.go). pending maps page id → debounce
	// deadline; nil until StartAutoReindex runs. attempts tracks consecutive
	// failures per page to drive exponential retry backoff (cleared on success).
	// pendingFiles/fileAttempts are the identical machinery for space_file ids
	// (the file half of the document index). All guarded by queueMu.
	queueMu      sync.Mutex
	pending      map[int64]time.Time
	attempts     map[int64]int
	pendingFiles map[int64]time.Time
	fileAttempts map[int64]int

	// paused, when set and true, halts background semantic backfilling (the
	// auto-reindex worker + stale sweep). Wired to the admin AI kill-switch so
	// indexing doesn't hammer the embedder while it's under maintenance; the
	// corpus is the source of truth, so the sweep backfills once it clears.
	paused func() bool
}

// SetPaused installs the predicate the background indexer consults each tick.
// Call before StartAutoReindex.
func (s *Service) SetPaused(fn func() bool) { s.paused = fn }

// SetUsageRecorder wraps the active embedder so EVERY embed call (search,
// reindex, the cloud proxy — all go through s.emb) is metered with a length-based
// token estimate. Injected by the api layer; no-op if disabled. Call once at
// wiring, before the background indexer starts.
func (s *Service) SetUsageRecorder(rec func(model string, inputTokens int)) {
	if rec == nil || s.emb == nil {
		return
	}
	s.emb = recordingEmbedder{inner: s.emb, rec: rec}
}

// recordingEmbedder is a metering decorator over an Embedder.
type recordingEmbedder struct {
	inner Embedder
	rec   func(model string, inputTokens int)
}

func (e recordingEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	v, err := e.inner.Embed(ctx, text)
	if err == nil {
		e.rec(e.inner.Model(), (len(text)+3)/4)
	}
	return v, err
}

// EmbedMany keeps the batch path visible THROUGH the decorator. Without it the
// batchEmbedder assertion in Service.EmbedMany would fail on the wrapper and
// every batch would silently degrade to one request per item — which is exactly
// the bug this seam existed to hide.
func (e recordingEmbedder) EmbedMany(ctx context.Context, texts []string) ([][]float32, EmbedStats, error) {
	inner, ok := e.inner.(batchEmbedder)
	if !ok {
		return embedMany(texts, nil, func(t string) ([]float32, error) { return e.Embed(ctx, t) })
	}
	vecs, st, err := inner.EmbedMany(ctx, texts)
	if err == nil {
		// Record the PROVIDER's token count for the batch, not a per-text estimate
		// of the raw input: the input is clamped before sending, so the estimate
		// bills text that was never embedded.
		e.rec(e.inner.Model(), st.Tokens)
	}
	return vecs, st, err
}

func (e recordingEmbedder) Model() string { return e.inner.Model() }

func (s *Service) isPaused() bool { return s.paused != nil && s.paused() }

// NewService builds the service from config. It never fails: with no EmbedURL
// the service is constructed disabled so api.Server can hold a non-nil handle
// unconditionally.
func NewService(db *sql.DB, cfg Config) *Service {
	s := &Service{db: db, cfg: cfg}
	if cfg.EmbedURL != "" {
		// A /v1 base speaks the OpenAI /embeddings shape (e.g. a LiteLLM proxy
		// fronting a primary+relief pool); anything else is native Ollama.
		if isOpenAIBase(cfg.EmbedURL) {
			s.emb = NewOpenAIEmbedder(cfg.EmbedURL, cfg.EmbedModel, cfg.EmbedToken)
		} else {
			s.emb = NewOllamaEmbedder(cfg.EmbedURL, cfg.EmbedModel, cfg.EmbedToken)
		}
	}
	if cfg.RerankURL != "" {
		s.rr = NewHTTPReranker(cfg.RerankURL, cfg.RerankModel, cfg.RerankToken)
	}
	return s
}

// Embed exposes the active embedder for the managed cloud embed proxy
// (api.CloudEmbed), so a connected self-hoster's embeddings are produced by the
// SAME embedder the provider uses in-process — no second embedding path.
func (s *Service) Embed(ctx context.Context, text string) ([]float32, error) {
	if !s.Enabled() {
		return nil, errEmbedderDisabled
	}
	return s.emb.Embed(ctx, text)
}

// batchEmbedder is the optional batch transport an Embedder can expose: one
// upstream request for N inputs instead of N requests. Both built-in embedders
// implement it; a fake or a future provider that doesn't just falls back to
// per-item, so it stays out of the required Embedder contract.
type batchEmbedder interface {
	EmbedMany(ctx context.Context, texts []string) (vecs [][]float32, st EmbedStats, err error)
}

// EmbedMany embeds a slice of passages, using the embedder's batch transport
// when it has one. This is the bulk-ingest path (atlas runs a repo through it a
// batch at a time): a 20k-chunk source is ~1.2k requests here versus ~20k
// one-at-a-time, and the shared embed GPU is the scarce resource — flooding it
// starves the chat model that lives on the same card. Returns one vector per
// text in order, plus how many upstream requests it actually cost.
func (s *Service) EmbedMany(ctx context.Context, texts []string) (vecs [][]float32, st EmbedStats, err error) {
	if !s.Enabled() {
		return nil, EmbedStats{}, errEmbedderDisabled
	}
	if b, ok := s.emb.(batchEmbedder); ok {
		return b.EmbedMany(ctx, texts)
	}
	return embedMany(texts, nil, func(t string) ([]float32, error) { return s.emb.Embed(ctx, t) })
}

// EmbedModel returns the active model name (for the managed proxy response).
func (s *Service) EmbedModel() string {
	if s.emb == nil {
		return ""
	}
	return s.emb.Model()
}

// EmbedEndpoint reports the configured embed base URL, model, and whether it's
// routed through an OpenAI /v1 proxy (a LiteLLM relief pool, vs native Ollama) —
// for the admin AI-endpoints breakdown. Empty url when disabled.
func (s *Service) EmbedEndpoint() (url, model string, proxied bool) {
	if s == nil || s.emb == nil {
		return "", "", false
	}
	return s.cfg.EmbedURL, s.emb.Model(), isOpenAIBase(s.cfg.EmbedURL)
}

var errEmbedderDisabled = errors.New("rag: embedder disabled")

// NewServiceWithEmbedder injects an embedder directly — used by tests with a
// deterministic fake, bypassing the env/Ollama path.
func NewServiceWithEmbedder(db *sql.DB, emb Embedder) *Service {
	return &Service{db: db, emb: emb}
}

// Enabled reports whether an embedder is configured.
func (s *Service) Enabled() bool { return s != nil && s.emb != nil }

// liveChecker is the optional liveness probe an embedder can expose so the
// background AI-health prober can cheaply check reachability without running a
// (metered) embed. Embedders that don't implement it are assumed reachable.
type liveChecker interface {
	Live(ctx context.Context) error
}

// Ping checks the embedder is reachable, for the AI-health prober. It unwraps the
// usage-metering decorator so probe traffic is never counted as user usage, then
// defers to the embedder's Live check; an embedder without one (e.g. a test fake)
// is treated as reachable. A disabled service returns errEmbedderDisabled.
func (s *Service) Ping(ctx context.Context) error {
	if !s.Enabled() {
		return errEmbedderDisabled
	}
	emb := s.emb
	if rec, ok := emb.(recordingEmbedder); ok {
		emb = rec.inner
	}
	if lc, ok := emb.(liveChecker); ok {
		return lc.Live(ctx)
	}
	return nil
}

// probeEmbedInput is the tiny fixed text ProbeEmbed embeds. Short so the probe is
// cheap and the warm model answers instantly on a healthy link.
const probeEmbedInput = "healthcheck"

// ProbeEmbed embeds probeEmbedInput end-to-end for the AI-health prober. Unlike
// Ping — a cheap liveness check that only proves the proxy/host ANSWERS — this
// exercises the REAL path (proxy → remote model), the same one ask/search hit. A
// reachable-but-unusable embedder (e.g. a lossy/bufferbloated link that times out
// on an actual embed while the proxy's liveness endpoint stays up) is caught here
// but invisible to Ping — which is exactly how a degraded link went undetected.
// It unwraps the usage-metering decorator so probe traffic is never counted as
// user usage. The caller scopes the timeout (see aiProbeTimeout).
func (s *Service) ProbeEmbed(ctx context.Context) error {
	if !s.Enabled() {
		return errEmbedderDisabled
	}
	emb := s.emb
	if rec, ok := emb.(recordingEmbedder); ok {
		emb = rec.inner
	}
	_, err := emb.Embed(ctx, probeEmbedInput)
	return err
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return def
}
