package rag

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// batchServer answers both embed shapes and records how many HTTP requests it
// took and how many inputs each carried. `input` is decoded as json.RawMessage
// first so a bare string and an array can be told apart — which is the whole
// point of these tests.
type batchServer struct {
	*httptest.Server
	requests  int
	batchSize []int // inputs per request, in order
}

func newBatchServer(t *testing.T, ollamaShape bool) *batchServer {
	t.Helper()
	bs := &batchServer{}
	bs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Input json.RawMessage `json:"input"`
		}
		_ = json.Unmarshal(body, &req)
		var many []string
		if err := json.Unmarshal(req.Input, &many); err != nil {
			var one string
			_ = json.Unmarshal(req.Input, &one)
			many = []string{one}
		}
		bs.requests++
		bs.batchSize = append(bs.batchSize, len(many))

		rows := make([][]float32, len(many))
		for i := range many {
			rows[i] = []float32{float32(i), 0.5}
		}
		if ollamaShape {
			_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": rows})
			return
		}
		data := make([]map[string]any, len(rows))
		for i, v := range rows {
			data[i] = map[string]any{"embedding": v}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(bs.Close)
	return bs
}

// TestEmbedMany_OneRequestPerBatch is the regression guard for the fan-out that
// melted the shared embed GPU: atlas hands down batches of 16, and every input
// went out as its own HTTP request. Both embedders must spend exactly ONE
// upstream request on a batch, and report it as one.
func TestEmbedMany_OneRequestPerBatch(t *testing.T) {
	texts := []string{"alpha", "beta", "gamma", "delta"}

	for _, tc := range []struct {
		name string
		emb  func(base string) Embedder
		srv  func() *batchServer
	}{
		{
			name: "ollama",
			srv:  func() *batchServer { return newBatchServer(t, true) },
			emb:  func(base string) Embedder { return NewOllamaEmbedder(base, "m", "") },
		},
		{
			name: "openai",
			srv:  func() *batchServer { return newBatchServer(t, false) },
			emb:  func(base string) Embedder { return NewOpenAIEmbedder(base+"/v1", "m", "") },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := tc.srv()
			b, ok := tc.emb(srv.URL).(batchEmbedder)
			if !ok {
				t.Fatalf("%s embedder does not implement batchEmbedder", tc.name)
			}
			vecs, st, err := b.EmbedMany(context.Background(), texts)
			if err != nil {
				t.Fatalf("EmbedMany: %v", err)
			}
			if len(vecs) != len(texts) {
				t.Fatalf("got %d vectors for %d inputs", len(vecs), len(texts))
			}
			if srv.requests != 1 {
				t.Fatalf("expected 1 upstream request, got %d (sizes %v)", srv.requests, srv.batchSize)
			}
			if st.Requests != 1 {
				t.Fatalf("reported %d requests, server saw 1", st.Requests)
			}
			if srv.batchSize[0] != len(texts) {
				t.Fatalf("expected all %d inputs in one request, got %d", len(texts), srv.batchSize[0])
			}
		})
	}
}

// TestEmbedMany_FallsBackPerItem proves an endpoint that refuses the array shape
// (tela cloud's managed embed proxy decodes `input` as a string) still gets
// correct vectors — one request each — and that the reported request count tells
// the truth about that cost rather than claiming a single cheap batch.
func TestEmbedMany_FallsBackPerItem(t *testing.T) {
	var requests, arrayRejects int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Input json.RawMessage `json:"input"`
		}
		_ = json.Unmarshal(body, &req)
		requests++
		var one string
		if err := json.Unmarshal(req.Input, &one); err != nil {
			arrayRejects++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"input must be a string"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{1, 2}}},
		})
	}))
	defer srv.Close()

	emb := NewOpenAIEmbedder(srv.URL+"/v1", "m", "")
	texts := []string{"a", "b", "c"}
	vecs, reported, err := emb.EmbedMany(context.Background(), texts)
	if err != nil {
		t.Fatalf("EmbedMany: %v", err)
	}
	if len(vecs) != len(texts) || len(vecs[2]) != 2 {
		t.Fatalf("bad vectors %v", vecs)
	}
	if arrayRejects != 1 {
		t.Fatalf("expected exactly one rejected batch attempt, got %d", arrayRejects)
	}
	// 1 failed batch + one per item; all of them really hit the embedder.
	if want := 1 + len(texts); reported.Requests != want || requests != want {
		t.Fatalf("reported %d requests, server saw %d, want %d", reported.Requests, requests, want)
	}
}

// TestRecordingEmbedder_KeepsBatching is the guard for the subtler half of the
// bug: the usage-metering decorator wraps the embedder, so if it doesn't carry
// EmbedMany through, every batch silently degrades to one request per item and
// nothing in the app looks broken. It must batch AND meter every input.
func TestRecordingEmbedder_KeepsBatching(t *testing.T) {
	srv := newBatchServer(t, true)
	var meterCalls, meteredTokens int
	s := &Service{emb: NewOllamaEmbedder(srv.URL, "m", "")}
	s.SetUsageRecorder(func(model string, tokens int) { meterCalls++; meteredTokens += tokens })

	texts := []string{"one", "two", "three"}
	vecs, st, err := s.EmbedMany(context.Background(), texts)
	if err != nil {
		t.Fatalf("EmbedMany: %v", err)
	}
	if len(vecs) != len(texts) {
		t.Fatalf("got %d vectors for %d inputs", len(vecs), len(texts))
	}
	if srv.requests != 1 || st.Requests != 1 {
		t.Fatalf("decorator broke batching: server saw %d requests, reported %d", srv.requests, st.Requests)
	}
	// Metering is per BATCH, not per input: the recorder gets the provider's
	// token count for the whole call (a per-text estimate would bill text the
	// clamp dropped). One call, covering every input's tokens.
	if meterCalls != 1 {
		t.Fatalf("metered %d times, want 1 per batch", meterCalls)
	}
	if want := estimateTokens(texts); meteredTokens != want {
		t.Fatalf("metered %d tokens, want %d (all %d inputs)", meteredTokens, want, len(texts))
	}
}
