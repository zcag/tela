// Package llm is a thin OpenAI-compatible client: chat + embeddings, with retry
// and an oversized-input fallback. No SDK — atlas talks to any /v1 endpoint
// (tardis/Ollama/OpenAI) over plain HTTP.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zcag/tela/backend/internal/atlas/core"
)

// DefaultConcurrency bounds total in-flight LLM requests when nothing overrides
// it. Kept low on purpose: the default provider is ollama on a single box, so a
// generous fan-out upstream (parallel stages + parallel sources) must still land
// as a small, steady number of concurrent requests here.
const DefaultConcurrency = 6

type Client struct {
	cfg  core.ModelCfg
	http *http.Client
	dim  int // embedding dimension, learned from first success (for zero fallback)

	// sem is the global concurrency gate: every chat + embed request acquires a
	// slot before hitting the provider and releases it after, so total in-flight
	// LLM work stays bounded no matter how many stages/sources run in parallel.
	sem chan struct{}

	// EmbedFunc, when set, replaces the built-in OpenAI-style embed transport.
	// Inside tela the executor injects a delegate to tela's rag.Embedder (the
	// Ollama-native /api/embed path) so generation embeds via the SAME instance
	// embedder + model tela's own RAG uses — identical vectors, no second
	// endpoint to configure. Contract: one vector per input, how many inputs fell
	// back to a zero vector, and how many upstream REQUESTS it cost — the
	// delegate is free to batch, so the caller can't infer that. nil ⇒ the
	// original /embeddings HTTP path (preserved verbatim for standalone use).
	//
	// tokens is what the provider actually charged. It must NOT be derived from
	// input length: the delegate clamps each input before sending, so a length
	// estimate counts text that was never embedded — it overstated a single large
	// chunk by ~400x, and that inflated figure is what the per-plan monthly embed
	// budget meters.
	EmbedFunc func(ctx context.Context, inputs []string) (vecs [][]float32, zeroed int, requests int, tokens int, err error)

	mu    sync.Mutex
	usage core.Usage // token + call tally, accumulated across this run
}

func New(cfg core.ModelCfg) *Client {
	return &Client{
		cfg:  cfg,
		http: &http.Client{}, // per-request deadlines via context
		sem:  make(chan struct{}, resolveConcurrency(cfg)),
	}
}

// resolveConcurrency picks the in-flight cap: explicit model cfg wins, else the
// ATLAS_LLM_CONCURRENCY env, else DefaultConcurrency. Non-positive values fall
// back to the default (a zero-capacity channel would deadlock every request).
func resolveConcurrency(cfg core.ModelCfg) int {
	if cfg.Concurrency > 0 {
		return cfg.Concurrency
	}
	if v := os.Getenv("ATLAS_LLM_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return DefaultConcurrency
}

// acquire blocks until a concurrency slot is free or ctx is canceled; the
// returned release frees the slot. A canceled acquisition returns a no-op
// release and ctx.Err(), so callers never release a slot they didn't take.
func (c *Client) acquire(ctx context.Context) (release func(), err error) {
	select {
	case c.sem <- struct{}{}:
		return func() { <-c.sem }, nil
	case <-ctx.Done():
		return func() {}, ctx.Err()
	}
}

// Usage returns the accumulated token tally for this client (one per run).
func (c *Client) Usage() core.Usage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.usage
}

func (c *Client) addChat(prompt, completion int) {
	c.mu.Lock()
	c.usage.ChatCalls++
	c.usage.PromptTokens += int64(prompt)
	c.usage.CompletionTokens += int64(completion)
	c.mu.Unlock()
}

// addEmbed tallies one embed step. requests is how many UPSTREAM calls it cost,
// not how many batches the caller made — EmbedCalls is a load figure, and
// reporting batches understated a repo ingest by the batch size (16×).
func (c *Client) addEmbed(tokens, requests int) {
	c.mu.Lock()
	c.usage.EmbedCalls += requests
	c.usage.EmbedTokens += int64(tokens)
	c.mu.Unlock()
}

type usageBlock struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// per-request deadlines: embeddings are quick (but allow a cold model load);
// chat generation is slow. A hung endpoint fails at the deadline and retries
// instead of blocking the whole run.
const (
	embedTimeout = 120 * time.Second
	chatTimeout  = 360 * time.Second
)

// --- embeddings ---

type embedReq struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}
type embedResp struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Usage usageBlock `json:"usage"`
	Error *apiError  `json:"error,omitempty"`
}

// Embed returns one vector per input. Token-density (not char count) is what
// blows past an embedder's window, so on a context-overflow it falls back from
// the fast batch path to per-item with progressively harder truncation, and as a
// last resort a zero vector — so a single pathological chunk never fails a run
// (it just won't be dense-retrievable). Real transport errors still propagate.
// The returned int is how many inputs hit the zero-vector fallback.
func (c *Client) Embed(ctx context.Context, inputs []string) ([][]float32, int, error) {
	// The gate lives HERE, not in embedOnce, so it covers the EmbedFunc delegate
	// too. It used to sit in embedOnce only — which the delegate path never
	// reaches — so inside tela (where EmbedFunc is always wired) the concurrency
	// cap was dead code and nothing bounded embed load on the shared GPU.
	release, err := c.acquire(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer release()

	if c.EmbedFunc != nil {
		vecs, zeroed, requests, tokens, err := c.EmbedFunc(ctx, inputs)
		if err != nil {
			return nil, 0, err
		}
		c.rememberDim(vecs)
		c.addEmbed(tokens, requests)
		return vecs, zeroed, nil
	}
	trimmed := make([]string, len(inputs))
	for i, s := range inputs {
		trimmed[i] = truncate(s, embedMaxChars)
	}
	if vecs, err := c.embedOnce(ctx, trimmed); err == nil {
		c.rememberDim(vecs)
		return vecs, 0, nil
	} else if !isContextOverflow(err) {
		return nil, 0, err // transport/other — don't mask it
	}
	out := make([][]float32, len(inputs))
	zeroed := 0
	for i, s := range inputs {
		v, fell, err := c.embedItem(ctx, s)
		if err != nil {
			return nil, 0, fmt.Errorf("embed item %d: %w", i, err)
		}
		out[i] = v
		if fell {
			zeroed++
		}
	}
	return out, zeroed, nil
}

// embedItem embeds one input, truncating harder on overflow and finally zeroing.
func (c *Client) embedItem(ctx context.Context, s string) (vec []float32, zeroed bool, err error) {
	for _, lim := range []int{embedMaxChars, 3000, 1500, 700} {
		vecs, e := c.embedOnce(ctx, []string{truncate(s, lim)})
		if e == nil {
			c.rememberDim(vecs)
			return vecs[0], false, nil
		}
		if !isContextOverflow(e) {
			return nil, false, e
		}
	}
	return make([]float32, c.dimLen()), true, nil // last resort: zero vector
}

// rememberDim / dimLen guard c.dim, learned from the first successful embedding.
// Concurrent Embed calls (parallel stages) race on it otherwise, so both go
// through the usage mutex.
func (c *Client) rememberDim(vecs [][]float32) {
	if len(vecs) == 0 {
		return
	}
	c.mu.Lock()
	if c.dim == 0 {
		c.dim = len(vecs[0])
	}
	c.mu.Unlock()
}

func (c *Client) dimLen() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dim
}

func isContextOverflow(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "context length")
}

const embedMaxChars = 6000

func (c *Client) embedOnce(ctx context.Context, inputs []string) ([][]float32, error) {
	// No gate here: Embed already holds a slot for the whole call (including the
	// per-item embedItem fallback loop, which would otherwise re-acquire a slot
	// it already owns and deadlock against its own fanout).
	var resp embedResp
	if err := c.post(ctx, c.cfg.EmbedBase(), "/embeddings", embedReq{Model: c.cfg.EmbedModel, Input: inputs}, &resp, embedTimeout); err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("embed: %s", resp.Error.Message)
	}
	if len(resp.Data) != len(inputs) {
		return nil, fmt.Errorf("embed: got %d vectors for %d inputs", len(resp.Data), len(inputs))
	}
	out := make([][]float32, len(resp.Data))
	for i := range resp.Data {
		out[i] = resp.Data[i].Embedding
	}
	tok := resp.Usage.TotalTokens
	if tok == 0 {
		tok = resp.Usage.PromptTokens
	}
	c.addEmbed(tok, 1) // built-in transport: this func is exactly one request
	return out, nil
}

// --- chat ---

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type chatReq struct {
	Model       string    `json:"model"`
	Messages    []chatMsg `json:"messages"`
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream"`
}
type chatResp struct {
	Choices []struct {
		Message chatMsg `json:"message"`
		// FinishReason is why the model stopped. "length" means the answer was
		// CUT OFF at max_tokens — the provider reports this on every call and
		// atlas used to discard it, so a reference page truncated mid-word was
		// indistinguishable from one the model simply chose to end. See
		// docs/atlas-output-budget-experiment.md.
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage usageBlock `json:"usage"`
	Error *apiError  `json:"error,omitempty"`
}
type apiError struct {
	Message string `json:"message"`
}

// MaxTokens is the configured output cap per chat call (0 = unset, i.e. the
// endpoint's own default). Generation stages need it to size a request to what
// the provider can actually answer, so it is read here rather than re-derived
// from env in the engine.
func (c *Client) MaxTokens() int { return c.cfg.MaxTokens }

// Chat sends a system+user turn and returns the assistant text, discarding
// whether the answer was truncated. Callers that build a page body should use
// ChatFull instead.
func (c *Client) Chat(ctx context.Context, system, user string, temperature float64) (string, error) {
	body, _, err := c.ChatFull(ctx, system, user, temperature)
	return body, err
}

// ChatFull is Chat plus the truncation flag: truncated reports that the provider
// stopped at max_tokens rather than at a natural end, so the text is an
// incomplete answer that happens to be well-formed HTTP.
func (c *Client) ChatFull(ctx context.Context, system, user string, temperature float64) (body string, truncated bool, err error) {
	// One chat call = one slot of the global gate.
	release, err := c.acquire(ctx)
	if err != nil {
		return "", false, err
	}
	defer release()
	var resp chatResp
	req := chatReq{Model: c.cfg.ChatModel, Temperature: temperature, MaxTokens: c.cfg.MaxTokens, Messages: []chatMsg{
		{Role: "system", Content: system}, {Role: "user", Content: user},
	}}
	if err := c.post(ctx, c.cfg.BaseURL, "/chat/completions", req, &resp, chatTimeout); err != nil {
		return "", false, err
	}
	if resp.Error != nil {
		return "", false, fmt.Errorf("chat: %s", resp.Error.Message)
	}
	if len(resp.Choices) == 0 {
		return "", false, fmt.Errorf("chat: empty response")
	}
	c.addChat(resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
	return resp.Choices[0].Message.Content, resp.Choices[0].FinishReason == "length", nil
}

// --- transport (with retry/backoff) ---

func (c *Client) post(ctx context.Context, base, path string, body, out any, timeout time.Duration) error {
	buf, _ := json.Marshal(body)
	url := strings.TrimRight(base, "/") + path
	var lastErr error
	// Retry 5xx/429/transport errors with capped exponential backoff. The window
	// is generous (≈30s over the attempts) so a brief endpoint hiccup — an MLX
	// server that drops a connection and needs a moment to recover — doesn't fail
	// the whole multi-minute run on a single page.
	const maxAttempts = 6
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(math.Pow(2, float64(attempt))) * 500 * time.Millisecond
			if backoff > 20*time.Second {
				backoff = 20 * time.Second
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}
		reqCtx, cancel := context.WithTimeout(ctx, timeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(buf))
		if err != nil {
			cancel()
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if c.cfg.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		if resp.StatusCode >= 500 || resp.StatusCode == 429 {
			lastErr = fmt.Errorf("http %d: %s", resp.StatusCode, snippet(data))
			continue // transient — retry
		}
		if resp.StatusCode >= 400 {
			return fmt.Errorf("http %d: %s", resp.StatusCode, snippet(data)) // client error — don't retry
		}
		return json.Unmarshal(data, out)
	}
	return lastErr
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
