// Package llm is tela's env-gated chat-completion client — the sibling to
// internal/rag for text generation. It speaks the OpenAI-compatible
// /v1/chat/completions shape (which Ollama also serves), so one client covers
// both a BYO endpoint and tela cloud's managed proxy.
//
// Wire-in mirrors rag exactly: one field on api.Server (s.llm), constructed from
// env. When TELA_LLM_URL is unset the service is disabled (Enabled()==false;
// handlers 503), so it ships dark until explicitly configured. The single client
// is consumed in-process by /api/rag/ask AND over HTTP by self-hosters via the
// managed cloud proxy (api.CloudChat) — there is no second generation path.
package llm

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Config is the env-driven configuration. URL empty => feature disabled.
type Config struct {
	URL       string // OpenAI-compatible base, e.g. http://ollama-host:11434/v1, or tela cloud's /api/cloud/llm/v1
	Model     string // chat model, e.g. qwen2.5:7b
	Token     string // optional bearer (a tela PAT) when URL is the managed cloud endpoint
	MaxTokens int    // completion length cap (0 => provider default)
	// MaxInflight bounds concurrent FOREGROUND (ask/assist) completions hitting the
	// primary endpoint. The N+1th waits OverflowWait for a slot, then spills to
	// OverflowModel (if set) instead of piling on — the primary degrades gracefully
	// under load until genuine saturation, at which point the overflow is real
	// failover, not load-balancing. 0 => no gate. Background callers (summarize /
	// agreement, marked via WithBackground) bypass the gate entirely.
	MaxInflight   int
	OverflowModel string        // model name for the overload spill (the relief proxy's L2 alias); "" => no spill
	OverflowWait  time.Duration // how long a foreground call waits for a primary slot before spilling
}

// ConfigFromEnv reads TELA_LLM_URL / _MODEL / _TOKEN / _MAX_TOKENS. _TOKEN is set
// only when pointing URL at tela cloud's managed LLM endpoint. _MAX_TOKENS caps
// answer length (default 1024) so a slow local model can't generate for minutes
// and trip the request timeout; set 0 to disable the cap.
//
// _MAX_INFLIGHT / _OVERFLOW_MODEL / _OVERFLOW_WAIT_MS configure the foreground
// concurrency gate (see Config.MaxInflight). The default gate (20) is sized to the
// primary's healthy batch capacity; with _OVERFLOW_MODEL unset the gate still
// bounds concurrency but a full gate just queues on the primary (no spill).
func ConfigFromEnv() Config {
	return Config{
		URL:           os.Getenv("TELA_LLM_URL"),
		Model:         getenv("TELA_LLM_MODEL", "qwen2.5:7b"),
		Token:         os.Getenv("TELA_LLM_TOKEN"),
		MaxTokens:     atoiDefault(os.Getenv("TELA_LLM_MAX_TOKENS"), 1024),
		MaxInflight:   atoiDefault(os.Getenv("TELA_LLM_MAX_INFLIGHT"), 20),
		OverflowModel: os.Getenv("TELA_LLM_OVERFLOW_MODEL"),
		OverflowWait:  time.Duration(atoiDefault(os.Getenv("TELA_LLM_OVERFLOW_WAIT_MS"), 12000)) * time.Millisecond,
	}
}

// Completer turns a system+user prompt into a single completion. One method, so
// the test fake and a future hosted provider are sibling types implementing this
// interface — not a refactor.
type Completer interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error)
	Model() string
}

// StreamCompleter is the optional streaming counterpart to Completer. A client
// that implements it (the OpenAI client) streams token deltas; one that doesn't
// (test fakes, a future provider) is handled by Service.CompleteStream's
// blocking fallback, so callers can always stream without every client growing
// a streaming path.
type StreamCompleter interface {
	CompleteStream(ctx context.Context, systemPrompt, userPrompt string, onToken func(string) error) error
}

// UsageRecorder is called once per completion with the active model and
// length-based token estimates. Injected by the api layer (which owns the DB);
// nil = no metering. See api/ai_usage.go.
type UsageRecorder func(model string, inputTokens, outputTokens int)

// EstimateTokens is the shared rough heuristic (~4 chars/token) used to meter AI
// usage without a real tokenizer — adequate for cost estimation, not billing.
func EstimateTokens(s string) int { return (len(s) + 3) / 4 }

// Service bundles the config and the active client. A nil client means disabled.
type Service struct {
	cfg Config
	cl  Completer
	// overflow is the overload spill target (the relief proxy's L2 alias) used when
	// the foreground gate stays full past cfg.OverflowWait. nil => spill disabled.
	overflow Completer
	// sem gates concurrent FOREGROUND completions on the primary; nil => no gate.
	// wait is how long a foreground call waits for a slot before spilling.
	sem  chan struct{}
	wait time.Duration
	// spills counts foreground overload spills to the overflow target (atomic);
	// onSpill, when set, fires on each spill (the api layer's Prometheus counter).
	spills  int64
	onSpill func()
	// usage, when set, records token estimates for every completion — the single
	// chokepoint for all chat usage regardless of which package calls Complete.
	usage UsageRecorder
}

// GateStats is a point-in-time snapshot of the foreground concurrency gate, for
// the admin reliability card and metrics.
type GateStats struct {
	Limit    int   // gate size (MaxInflight); 0 = no gate
	InFlight int   // foreground completions currently holding a slot
	Spills   int64 // cumulative overload spills to the overflow target
	Overflow bool  // an overflow target is configured (a spill is possible)
}

// Stats returns the current gate snapshot. Safe on a nil/disabled service.
func (s *Service) Stats() GateStats {
	if s == nil {
		return GateStats{}
	}
	g := GateStats{Overflow: s.overflow != nil, Spills: atomic.LoadInt64(&s.spills)}
	if s.sem != nil {
		g.Limit = cap(s.sem)
		g.InFlight = len(s.sem)
	}
	return g
}

// SetSpillRecorder installs a hook fired once each time a foreground call spills
// to the overflow target (genuine overload). Call once at wiring.
func (s *Service) SetSpillRecorder(f func()) {
	if s != nil {
		s.onSpill = f
	}
}

// bgKey marks a context as a low-priority background completion.
type bgKey struct{}

// WithBackground marks ctx as a background completion (summarize, agreement): it
// bypasses the foreground concurrency gate and never spills to the overflow
// target. Background work rides the primary endpoint down rather than burning the
// (often paid) relief layer, and shouldn't compete with live ask/assist for gate
// slots. Foreground ask/assist calls omit it.
func WithBackground(ctx context.Context) context.Context {
	return context.WithValue(ctx, bgKey{}, true)
}

func isBackground(ctx context.Context) bool { v, _ := ctx.Value(bgKey{}).(bool); return v }

// SetUsageRecorder installs the per-completion usage hook. Call once at wiring.
func (s *Service) SetUsageRecorder(r UsageRecorder) {
	if s != nil {
		s.usage = r
	}
}

func (s *Service) recordModel(model, systemPrompt, userPrompt, output string) {
	if s.usage == nil {
		return
	}
	s.usage(model, EstimateTokens(systemPrompt)+EstimateTokens(userPrompt), EstimateTokens(output))
}

// NewService builds the service from config. It never fails: with no URL the
// service is constructed disabled so api.Server can hold a non-nil handle
// unconditionally.
func NewService(cfg Config) *Service {
	s := &Service{cfg: cfg, wait: cfg.OverflowWait}
	if cfg.URL != "" {
		s.cl = NewOpenAIClient(cfg.URL, cfg.Model, cfg.Token, cfg.MaxTokens)
		// The overflow client shares the proxy URL/token; only the model alias
		// differs, so the proxy routes the spill to L2 (and on past it). A model
		// equal to the primary would be a no-op spill, so guard against it.
		if cfg.OverflowModel != "" && cfg.OverflowModel != cfg.Model {
			s.overflow = NewOpenAIClient(cfg.URL, cfg.OverflowModel, cfg.Token, cfg.MaxTokens)
		}
	}
	if cfg.MaxInflight > 0 {
		s.sem = make(chan struct{}, cfg.MaxInflight)
	}
	return s
}

// pick selects the client for one foreground completion and returns a release for
// any gate slot it holds. Background calls and a no-gate config use the primary
// directly. Otherwise it waits up to s.wait for a slot: acquired => primary (the
// returned release frees the slot); timed out => the overflow client if one is
// configured (no slot held — it targets a different layer), else the primary
// best-effort, so a missing relief layer degrades to "queue on the primary"
// rather than failing the request.
func (s *Service) pick(ctx context.Context) (Completer, func()) {
	noop := func() {}
	if s.sem == nil || isBackground(ctx) {
		return s.cl, noop
	}
	t := time.NewTimer(s.wait)
	defer t.Stop()
	select {
	case s.sem <- struct{}{}:
		return s.cl, func() { <-s.sem }
	case <-ctx.Done():
		return s.cl, noop // the completion below observes ctx.Err()
	case <-t.C:
		if s.overflow != nil {
			atomic.AddInt64(&s.spills, 1)
			if s.onSpill != nil {
				s.onSpill()
			}
			slog.Warn("llm: foreground gate saturated, spilling to overflow",
				"overflow_model", s.overflow.Model(), "gate", cap(s.sem), "waited", s.wait)
			return s.overflow, noop
		}
		return s.cl, noop
	}
}

// NewServiceWithCompleter injects a completer directly — used by tests with a
// canned fake and by the managed proxy, bypassing the env/HTTP path.
func NewServiceWithCompleter(cl Completer) *Service { return &Service{cl: cl} }

// Enabled reports whether a chat client is configured.
func (s *Service) Enabled() bool { return s != nil && s.cl != nil }

// liveChecker is the optional liveness probe a chat client can expose so the
// background AI-health prober can confirm reachability without a completion. A
// client without one (a test fake) is treated as reachable.
type liveChecker interface {
	Live(ctx context.Context) error
}

// Ping checks the chat backend is reachable, for the AI-health prober. Defers to
// the client's Live check; a client without one is treated as reachable. A
// disabled service returns errLLMDisabled.
func (s *Service) Ping(ctx context.Context) error {
	if !s.Enabled() {
		return errLLMDisabled
	}
	if lc, ok := s.cl.(liveChecker); ok {
		return lc.Live(ctx)
	}
	return nil
}

var errLLMDisabled = errors.New("llm: client disabled")

// Complete runs one non-streaming chat completion. Surfaces the disabled error
// so callers (and the cloud proxy) can 503 uniformly.
func (s *Service) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	if !s.Enabled() {
		return "", errLLMDisabled
	}
	cl, release := s.pick(ctx)
	defer release()
	out, err := cl.Complete(ctx, systemPrompt, userPrompt)
	if err == nil {
		s.recordModel(cl.Model(), systemPrompt, userPrompt, out)
	}
	return out, err
}

// CompleteStream streams the completion token-by-token via onToken. If the active
// client doesn't implement StreamCompleter, it falls back to a blocking Complete
// and delivers the whole answer as a single onToken call — so the streaming ask
// path works against any provider (and the test fakes) unchanged.
func (s *Service) CompleteStream(ctx context.Context, systemPrompt, userPrompt string, onToken func(string) error) error {
	if !s.Enabled() {
		return errLLMDisabled
	}
	cl, release := s.pick(ctx)
	defer release()
	// Count streamed output for the usage estimate without buffering the whole
	// answer: tally bytes as they flow, record on success.
	var outLen int
	tally := func(tok string) error {
		outLen += len(tok)
		return onToken(tok)
	}
	if sc, ok := cl.(StreamCompleter); ok {
		err := sc.CompleteStream(ctx, systemPrompt, userPrompt, tally)
		if err == nil && s.usage != nil {
			s.usage(cl.Model(), EstimateTokens(systemPrompt)+EstimateTokens(userPrompt), (outLen+3)/4)
		}
		return err
	}
	out, err := cl.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		return err
	}
	s.recordModel(cl.Model(), systemPrompt, userPrompt, out)
	if out == "" {
		return nil
	}
	return onToken(out)
}

// Model returns the active model name (for the managed proxy default).
func (s *Service) Model() string {
	if s.cl == nil {
		return ""
	}
	return s.cl.Model()
}

// Endpoint reports the configured chat base URL and model for the admin
// AI-endpoints breakdown. Empty url when disabled.
func (s *Service) Endpoint() (url, model string) {
	if !s.Enabled() {
		return "", ""
	}
	return s.cfg.URL, s.Model()
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// atoiDefault parses s as an int, falling back to def on empty/invalid. A
// negative value (e.g. "-1") is treated as "no cap" → 0.
func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	if n < 0 {
		return 0
	}
	return n
}
