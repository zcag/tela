package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestOpenAPISpecIsWellFormed guards the embedded document itself: it must
// parse, and it must still contain the exact servers[0].url literal that
// ServeOpenAPI substitutes. Without the second check a reformat of the JSON
// would make the substitution a silent no-op and every self-hosted instance
// would advertise telawiki.com as its API server.
func TestOpenAPISpecIsWellFormed(t *testing.T) {
	var doc struct {
		OpenAPI string                                `json:"openapi"`
		Info    struct{ Title, Description string }   `json:"info"`
		Paths   map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(openAPISpec, &doc); err != nil {
		t.Fatalf("openapi.json does not parse: %v", err)
	}
	if !strings.HasPrefix(doc.OpenAPI, "3.1") {
		t.Errorf("openapi version = %q, want 3.1.x", doc.OpenAPI)
	}
	if len(doc.Paths) == 0 {
		t.Fatal("openapi.json declares no paths")
	}
	if n := bytes.Count(openAPISpec, []byte(openAPICanonicalServer)); n != 1 {
		t.Fatalf("servers[0].url literal %q appears %d times, want exactly 1 — "+
			"ServeOpenAPI rewrites it for self-hosted instances", openAPICanonicalServer, n)
	}
}

// TestOpenAPIPathsExist is the anti-drift check: every /api path the spec
// describes must be a route the mux actually registers. A spec that documents
// a route tela removed is worse than no spec — an integrator builds against it
// and gets a 404. (The reverse is fine: the spec deliberately covers a subset.)
func TestOpenAPIPathsExist(t *testing.T) {
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(openAPISpec, &doc); err != nil {
		t.Fatalf("openapi.json does not parse: %v", err)
	}

	// Register the real routes on a bare mux and ask it to resolve each
	// documented path. mux.Handler reports the matched pattern, which is ""
	// only when nothing is registered for it. registerRoutes builds a few
	// handlers eagerly (MCP, WebDAV, metrics), so it needs a real Server —
	// hence the throwaway database rather than a zero value.
	mux := http.NewServeMux()
	registerRoutes(New(newAPITestDB(t)), mux)

	for p, ops := range doc.Paths {
		for method := range ops {
			m := strings.ToUpper(method)
			if m == "PARAMETERS" {
				continue
			}
			// Substitute plausible values for the templated segments; the mux
			// only needs a concrete path to match a pattern.
			concrete := templateReplacer.Replace(p)
			req := httptest.NewRequest(m, concrete, nil)
			if _, pattern := mux.Handler(req); pattern == "" {
				t.Errorf("openapi.json documents %s %s (%s) but no route is registered for it", m, p, concrete)
			}
		}
	}
}

var templateReplacer = strings.NewReplacer(
	"{id}", "1",
	"{rev_id}", "1",
	"{file_id}", "1",
	"{page_id}", "1",
	"{share_id}", "1",
	"{token}", "tok",
	"{handle}", "someone",
	"{slug}", "a-space",
	"{space_id}", "1",
	"{file}", strings.Repeat("a", 64)+".png",
	"{hash}", strings.Repeat("a", 64),
)

// TestServeOpenAPI checks the wire contract: JSON content type (the bug this
// endpoint exists to fix was /openapi.json answering 200 with the SPA's HTML)
// and the server URL rewritten to the requesting origin when no canonical base
// URL is configured — which is the self-hosted default.
func TestServeOpenAPI(t *testing.T) {
	t.Setenv("TELA_PUBLIC_BASE_URL", "")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://wiki.example.test/api/public/openapi.json", nil)
	(&Server{}).ServeOpenAPI(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var doc struct {
		Servers []struct{ URL string } `json:"servers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("response does not parse: %v", err)
	}
	if len(doc.Servers) == 0 || doc.Servers[0].URL != "http://wiki.example.test" {
		t.Errorf("servers[0].url = %+v, want http://wiki.example.test", doc.Servers)
	}
}
