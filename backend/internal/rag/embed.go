package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// OllamaEmbedder calls an Ollama server's /api/embed endpoint — typically a
// remote Ollama on your network (e.g. http://ollama-host:11434), 1024-d model.
// Swapping to a hosted OpenAI-compatible provider is a sibling type implementing
// Embedder, not a change here.
type OllamaEmbedder struct {
	base     string
	model    string
	token    string // optional bearer; set when the embed URL is tela's managed endpoint
	instruct string // query-side instruction task (asymmetric retrieval); blank disables the prefix
	client   *http.Client
}

// defaultQueryInstruct is the task description prepended to QUERY embeddings only
// — documents (passages) are embedded raw. Qwen3-Embedding (and most modern
// instruction-aware embedders) are trained for this asymmetric shape: an
// instructed query against bare passages. Omitting the query instruction costs
// ~1–5% retrieval quality on Qwen3. Wrapped as "Instruct: {task}\nQuery:{q}" per
// the model card. Override per-deployment with TELA_RAG_QUERY_INSTRUCT; set it to
// a single space to disable (e.g. for a non-instruction model like mxbai).
const defaultQueryInstruct = "Given a question, retrieve passages from the knowledge base that answer it"

// NewOllamaEmbedder builds an embedder for an Ollama-compatible /api/embed
// endpoint. token is optional: empty for a direct Ollama, or a tela
// PAT when base points at tela cloud's managed embed proxy (/api/cloud/ollama).
// The managed endpoint speaks the same Ollama shape, so this one client serves
// both BYO and cloud-backed — no separate cloud embedder type.
func NewOllamaEmbedder(base, model, token string) *OllamaEmbedder {
	return &OllamaEmbedder{
		base:     strings.TrimRight(base, "/"),
		model:    model,
		token:    strings.TrimSpace(token),
		instruct: strings.TrimSpace(getenv("TELA_RAG_QUERY_INSTRUCT", defaultQueryInstruct)),
		client:   &http.Client{Timeout: 60 * time.Second},
	}
}

func (e *OllamaEmbedder) Model() string { return e.model }

// Live is a cheap liveness probe for the AI-health prober: GET {base}/api/tags
// (Ollama's installed-models list) just to confirm the embedder host answers. It
// runs NO inference, so a cold model is never woken and nothing is metered. Any
// HTTP response < 500 counts as reachable; a transport error (connection
// refused / DNS / timeout) or a 5xx means down. The managed cloud endpoint has
// no /api/tags and returns 404 — still a response, so still "reachable", which
// is exactly the up/down signal we want.
func (e *OllamaEmbedder) Live(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.base+"/api/tags", nil)
	if err != nil {
		return err
	}
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("ollama tags: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode >= 500 {
		return fmt.Errorf("ollama tags: status %d", resp.StatusCode)
	}
	return nil
}

// EmbedQuery embeds a SEARCH QUERY with the asymmetric instruction prefix, the
// counterpart to Embed (which embeds passages raw). Search calls this for the
// query side only; the corpus is never re-embedded with the prefix. With no
// instruction configured it degrades to a plain Embed.
func (e *OllamaEmbedder) EmbedQuery(ctx context.Context, query string) ([]float32, error) {
	return e.Embed(ctx, queryInstruct(e.instruct, query))
}

// maxEmbedChars is the initial rune cap on embed input — a first-pass guard so
// most chunks embed in one shot. It is intentionally loose because token density
// varies wildly (symbol-heavy markdown can be <2 chars/token), so a char cap
// can't reliably predict the model's 512-token limit. The real guarantee is the
// shrink-retry loop in Embed.
const maxEmbedChars = 1600

// embedMinChars is the floor below which we stop shrinking and surface the error
// rather than embed a near-empty fragment.
const embedMinChars = 64

// maxEmbedResponseBytes caps how much we read from the embedder's response — a
// 1024-d float vector is a few KB; 8 MiB is a generous ceiling that still
// guards against an unbounded/malicious upstream body.
const maxEmbedResponseBytes = 8 << 20

func clampRunes(text string, n int) string {
	r := []rune(text)
	if len(r) <= n {
		return text
	}
	return string(r[:n])
}

// Embed returns the embedding for text via POST /api/embed {model, input}. The
// Ollama API returns {"embeddings": [[...]]}; we send one string and take row 0.
//
// mxbai-embed-large rejects (HTTP 400 — it does NOT silently truncate) input
// past its 512-token context, and token density isn't predictable from char
// count. So on a context-overflow 400 we shrink the input 25% and retry until it
// fits (down to embedMinChars). Only the embedded text shrinks; the stored chunk
// content + lexical index keep the full text, and the contextual prefix sits at
// the head, so we only ever drop the tail of an over-long section.
func (e *OllamaEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	return shrinkToFit(text, func(in string) ([]float32, bool, error) {
		return e.embedOnce(ctx, in)
	})
}

// shrinkToFit runs once(input) and, on a context-overflow rejection (overflow
// true), shrinks the input 25% and retries down to embedMinChars. Shared by
// every Embedder (Ollama + OpenAI) so the overflow handling lives in one place;
// only the embedded text shrinks, never the stored chunk. once returns
// (vec, overflow, err).
func shrinkToFit(text string, once func(string) ([]float32, bool, error)) ([]float32, error) {
	input := clampRunes(text, maxEmbedChars)
	for {
		vec, overflow, err := once(input)
		if err == nil {
			return vec, nil
		}
		n := len([]rune(input))
		if !overflow || n <= embedMinChars {
			return nil, err
		}
		input = clampRunes(input, n*3/4)
	}
}

// embedMany is the shared batch path for every Embedder: it sends the whole
// slice as ONE upstream request and, if that request fails for ANY reason, falls
// back to embedding each item on its own. The fallback is what makes batching
// safe to attempt blindly — a batch 400 can't say WHICH input overflowed, and an
// endpoint that only speaks the single-input shape (tela cloud's managed embed
// proxy) rejects the array outright. Per-item retry re-runs shrinkToFit, so the
// slow path behaves exactly as it did before batching existed.
//
// Callers get one vector per text, in order. batch receives inputs pre-clamped
// to maxEmbedChars (the same first-pass guard shrinkToFit applies) so a routine
// over-long chunk doesn't cost a whole wasted batch round trip. A nil batch
// (embedder with no batch transport) goes straight to the per-item path.
//
// requests is how many upstream calls this actually cost, so metering reports
// real load on the embedder rather than the caller's batch count. A shrink-retry
// inside one() is not counted — it's rare, and counting it would mean threading
// a tally through shrinkToFit for a number nobody acts on.
func embedMany(texts []string, batch func([]string) ([][]float32, error), one func(string) ([]float32, error)) (vecs [][]float32, requests int, err error) {
	if len(texts) == 0 {
		return nil, 0, nil
	}
	if len(texts) > 1 && batch != nil {
		clamped := make([]string, len(texts))
		for i, t := range texts {
			clamped[i] = clampRunes(t, maxEmbedChars)
		}
		if v, err := batch(clamped); err == nil {
			return v, 1, nil
		}
		requests = 1 // the failed attempt still hit the embedder
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, err := one(t)
		requests++
		if err != nil {
			return nil, requests, err
		}
		out[i] = v
	}
	return out, requests, nil
}

// queryInstruct wraps a search query with the asymmetric instruction prefix per
// the Qwen3-Embedding model card ("Instruct: {task}\nQuery:{q}"); a blank
// instruction returns the query unchanged. Shared by both embedders' EmbedQuery.
func queryInstruct(instruct, query string) string {
	if instruct == "" {
		return query
	}
	return "Instruct: " + instruct + "\nQuery:" + query
}

// EmbedMany embeds a whole batch in ONE upstream request (Ollama's /api/embed
// takes an `input` array and answers with one row per input). See embedMany for
// the fallback contract.
func (e *OllamaEmbedder) EmbedMany(ctx context.Context, texts []string) ([][]float32, int, error) {
	return embedMany(texts,
		func(in []string) ([][]float32, error) {
			rows, _, err := e.post(ctx, in, len(in))
			return rows, err
		},
		func(text string) ([]float32, error) { return e.Embed(ctx, text) },
	)
}

// embedOnce does a single embed request. overflow is true when the model
// rejected the input for exceeding its context window (the retryable case).
func (e *OllamaEmbedder) embedOnce(ctx context.Context, input string) (vec []float32, overflow bool, err error) {
	// A bare string, not a 1-element array: tela cloud's managed embed proxy
	// (api.CloudEmbed) decodes `input` as a string, so the single-item shape
	// must stay exactly as it was. Batching is the array shape below.
	rows, over, err := e.post(ctx, input, 1)
	if err != nil {
		return nil, over, err
	}
	return rows[0], false, nil
}

// post sends {model, input} to /api/embed and returns one vector row per input.
// input is either a single string or a []string batch; want is how many rows the
// caller expects. overflow is true when the model rejected the input for
// exceeding its context window (the retryable case).
func (e *OllamaEmbedder) post(ctx context.Context, input any, want int) (rows [][]float32, overflow bool, err error) {
	body, _ := json.Marshal(map[string]any{"model": e.model, "input": input})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.base+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("ollama embed: %w", err)
	}
	defer resp.Body.Close()

	// Cap the upstream read: a malicious or buggy embedder (or, in the BYO
	// case, an attacker-controlled TELA_RAG_EMBED_URL) could otherwise return a
	// huge body and OOM the process. An embedding response is tiny.
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxEmbedResponseBytes))
	if resp.StatusCode != http.StatusOK {
		over := resp.StatusCode == http.StatusBadRequest && strings.Contains(string(raw), "context length")
		return nil, over, fmt.Errorf("ollama embed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
		Error      string      `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false, fmt.Errorf("ollama decode: %w", err)
	}
	if out.Error != "" {
		return nil, false, fmt.Errorf("ollama embed: %s", out.Error)
	}
	if len(out.Embeddings) != want || len(out.Embeddings[0]) == 0 {
		return nil, false, fmt.Errorf("ollama embed: got %d vectors for %d inputs (model %q)", len(out.Embeddings), want, e.model)
	}
	return out.Embeddings, false, nil
}

// vecLiteral formats a float32 slice as a pgvector text literal ("[0.1,0.2]").
// pgvector parses this on a ::vector cast, so we never need a driver-level type
// or an extra dependency — the value crosses database/sql as a plain string.
func vecLiteral(v []float32) string {
	var b strings.Builder
	b.Grow(len(v) * 8)
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}
