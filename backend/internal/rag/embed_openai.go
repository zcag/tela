package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAIEmbedder calls an OpenAI-compatible POST {base}/embeddings endpoint
// (base includes the /v1 prefix) — the sibling to OllamaEmbedder for providers
// that speak the OpenAI shape instead of Ollama's native /api/embed. Its reason
// to exist is failover: a LiteLLM proxy fronting a primary+relief embedding pool
// is addressed as http://litellm:4000/v1, so routing embeddings through it gives
// the SAME relief-on-overload behaviour the chat path already gets — but LiteLLM
// only speaks OpenAI, not Ollama's /api/embed. Selected automatically when
// TELA_RAG_EMBED_URL ends in /v1 (isOpenAIBase), mirroring the TELA_LLM_URL
// convention; otherwise NewOllamaEmbedder is used. Same Embedder/queryEmbedder/
// liveChecker contract, so nothing downstream changes.
type OpenAIEmbedder struct {
	base     string
	model    string
	token    string // optional bearer (LiteLLM virtual key, or a tela PAT for the managed proxy)
	instruct string // query-side asymmetric instruction; blank disables the prefix
	client   *http.Client
}

// NewOpenAIEmbedder builds an embedder for an OpenAI-compatible /embeddings
// endpoint. token is optional (a LiteLLM key / provider key / tela PAT).
func NewOpenAIEmbedder(base, model, token string) *OpenAIEmbedder {
	return &OpenAIEmbedder{
		base:     strings.TrimRight(base, "/"),
		model:    model,
		token:    strings.TrimSpace(token),
		instruct: strings.TrimSpace(getenv("TELA_RAG_QUERY_INSTRUCT", defaultQueryInstruct)),
		client:   &http.Client{Timeout: 60 * time.Second},
	}
}

func (e *OpenAIEmbedder) Model() string { return e.model }

// isOpenAIBase reports whether an embed base URL speaks the OpenAI /v1 shape
// (path ends in /v1) rather than Ollama's native /api/embed — the same
// convention TELA_LLM_URL uses. A LiteLLM proxy is http://litellm:4000/v1.
func isOpenAIBase(u string) bool {
	return strings.HasSuffix(strings.TrimRight(u, "/"), "/v1")
}

// Live is the liveness probe for the AI-health prober: GET {base}/models — the
// OpenAI model list, no inference, so a cold model is never woken. Any response
// < 500 is reachable (a proxy returning 401/404 still proves it's up); a
// transport error or 5xx is down. Mirrors OllamaEmbedder.Live + llm.Live.
func (e *OpenAIEmbedder) Live(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.base+"/models", nil)
	if err != nil {
		return err
	}
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("openai models: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode >= 500 {
		return fmt.Errorf("openai models: status %d", resp.StatusCode)
	}
	return nil
}

// EmbedQuery embeds a SEARCH QUERY with the asymmetric instruction prefix, the
// counterpart to Embed (passages, raw). Shares queryInstruct with the Ollama
// embedder; with no instruction configured it degrades to a plain Embed.
func (e *OpenAIEmbedder) EmbedQuery(ctx context.Context, query string) ([]float32, error) {
	return e.Embed(ctx, queryInstruct(e.instruct, query))
}

// Embed returns the embedding for text via POST {base}/embeddings, reusing the
// shared shrink-on-overflow loop so an over-long chunk is trimmed and retried
// exactly as the Ollama path does.
func (e *OpenAIEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	return shrinkToFit(text, func(in string) ([]float32, bool, error) {
		return e.embedOnce(ctx, in)
	})
}

// EmbedMany embeds a whole batch in ONE upstream request — the OpenAI
// /embeddings shape takes an `input` array and answers with one data row per
// input, and LiteLLM passes that through. See embedMany for the fallback
// contract.
func (e *OpenAIEmbedder) EmbedMany(ctx context.Context, texts []string) ([][]float32, int, error) {
	return embedMany(texts,
		func(in []string) ([][]float32, error) {
			rows, _, err := e.post(ctx, in, len(in))
			return rows, err
		},
		func(text string) ([]float32, error) { return e.Embed(ctx, text) },
	)
}

// embedOnce does a single embed request. overflow is true when the upstream
// rejected the input for exceeding the model's context window (the retryable
// case). The OpenAI embeddings response is {"data":[{"embedding":[...]}]}.
func (e *OpenAIEmbedder) embedOnce(ctx context.Context, input string) (vec []float32, overflow bool, err error) {
	rows, over, err := e.post(ctx, input, 1)
	if err != nil {
		return nil, over, err
	}
	return rows[0], false, nil
}

// post sends {model, input} to /embeddings and returns one vector row per input.
// input is either a single string or a []string batch; want is how many rows the
// caller expects.
func (e *OpenAIEmbedder) post(ctx context.Context, input any, want int) (rows [][]float32, overflow bool, err error) {
	body, _ := json.Marshal(map[string]any{"model": e.model, "input": input})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.base+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("openai embed: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxEmbedResponseBytes))
	if resp.StatusCode != http.StatusOK {
		// An over-long input comes back as 400 with a message naming the context/
		// token limit; phrasing varies by provider (and LiteLLM passes it through),
		// so match a few known forms — only on a 400, so a real error never loops.
		low := strings.ToLower(string(raw))
		over := resp.StatusCode == http.StatusBadRequest &&
			(strings.Contains(low, "context length") ||
				strings.Contains(low, "maximum context") ||
				strings.Contains(low, "context window") ||
				strings.Contains(low, "too long") ||
				strings.Contains(low, "too many tokens"))
		return nil, over, fmt.Errorf("openai embed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false, fmt.Errorf("openai embed decode: %w", err)
	}
	if len(out.Data) != want || len(out.Data[0].Embedding) == 0 {
		return nil, false, fmt.Errorf("openai embed: got %d vectors for %d inputs (model %q)", len(out.Data), want, e.model)
	}
	rows = make([][]float32, len(out.Data))
	for i := range out.Data {
		rows[i] = out.Data[i].Embedding
	}
	return rows, false, nil
}
