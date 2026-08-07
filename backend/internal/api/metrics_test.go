package api

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestMetrics_Gating asserts /metrics is instance-admin only: anonymous → 401,
// a non-admin session → 403, an admin session → 200 with the Prometheus
// exposition (including our HTTP request metric and the Go runtime collectors).
func TestMetrics_Gating(t *testing.T) {
	ts, d := newWiredServer(t)
	seedUser(t, d, "alice", "alicepw12", false)
	seedUser(t, d, "root", "rootpw1234", true)

	// Anonymous: middleware 401s before the handler runs.
	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("anon GET /metrics: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anon /metrics: status=%d, want 401", resp.StatusCode)
	}

	// Non-admin session: 403 from requireInstanceAdmin.
	alice := loginClient(t, ts, "alice", "alicepw12")
	resp, err = alice.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("alice GET /metrics: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin /metrics: status=%d, want 403", resp.StatusCode)
	}

	// Admin session: 200 + exposition. Hit a real route first so the request
	// counter has at least one observation to emit.
	admin := loginClient(t, ts, "root", "rootpw1234")
	if r, err := admin.Get(ts.URL + "/api/spaces"); err == nil {
		r.Body.Close()
	}
	resp, err = admin.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("admin GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin /metrics: status=%d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{
		"tela_http_requests_total",
		"tela_http_request_duration_seconds",
		"go_goroutines",              // Go runtime collector
		"process_start_time_seconds", // process collector
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("/metrics body missing %q", want)
		}
	}
	// Route label must be the matched pattern, not the raw path (low cardinality).
	if !strings.Contains(string(body), `route="GET /api/spaces"`) {
		t.Errorf("/metrics missing route-pattern label for GET /api/spaces; body:\n%s", body)
	}
}
