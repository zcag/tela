package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestProbeReason(t *testing.T) {
	page := "ollama embed: status 501: <!DOCTYPE HTML>\n<html lang=\"en\">\n    <head>\n        <meta charset=\"utf-8\">\n" +
		"        <style type=\"text/css\">\n            :root {\n                color-scheme: light dark;\n            }\n        </style>\n" +
		"        <title>Error response</title>\n    </head>\n    <body>\n        <h1>Error response</h1>\n        <p>Error code: 501</p>\n" +
		"        <p>Message: Unsupported method (&#x27;POST&#x27;).</p>\n    </body>\n</html>"
	cases := []struct{ in, want string }{
		{page, "ollama embed: status 501: Error response Error response Error code: 501 Message: Unsupported method ('POST')."},
		{`llm chat: status 404: {"error":"model \"qwen\" not found"}`, `llm chat: status 404: {"error":"model \"qwen\" not found"}`},
		{"ollama embed: status 400: input <prompt> too long", "ollama embed: status 400: input <prompt> too long"},
		{"ollama embed: dial tcp 127.0.0.1:9: connect: connection refused", "ollama embed: dial tcp 127.0.0.1:9: connect: connection refused"},
	}
	for _, c := range cases {
		if got := probeReason(errors.New(c.in)); got != c.want {
			t.Errorf("probeReason(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
	if got := probeReason(errors.New("status 500: " + strings.Repeat("x ", 400))); len([]rune(got)) > 301 {
		t.Errorf("long reason not clipped: %d runes", len([]rune(got)))
	}
}

func TestAIProbeInterval(t *testing.T) {
	cases := []struct {
		env  string
		want time.Duration
	}{
		{"", aiProbeIntervalDefault},
		{"5m", 5 * time.Minute},
		{" 90s ", 90 * time.Second},
		{"0", 0},
		{"1ms", aiProbeTimeout},
		{"-1m", aiProbeIntervalDefault},
		{"often", aiProbeIntervalDefault},
		{"30", aiProbeIntervalDefault},
	}
	for _, c := range cases {
		t.Setenv("TELA_AI_PROBE_INTERVAL", c.env)
		if got := aiProbeInterval(); got != c.want {
			t.Errorf("TELA_AI_PROBE_INTERVAL=%q: got %v, want %v", c.env, got, c.want)
		}
	}
}

func TestAdminAIEndpoints_ProbeDisabled(t *testing.T) {
	ts, d, srv := newWiredServerOnDiskWithSrv(t)
	seedUser(t, d, "root", "rootpw1234", true)
	admin := loginClient(t, ts, "root", "rootpw1234")

	probeDisabled := func() bool {
		t.Helper()
		resp, err := admin.Get(ts.URL + "/api/admin/ai-endpoints")
		if err != nil {
			t.Fatalf("GET ai-endpoints: %v", err)
		}
		defer resp.Body.Close()
		var out aiEndpointsOut
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out.ProbeDisabled
	}

	if probeDisabled() {
		t.Fatal("probe_disabled before the prober was configured off")
	}
	t.Setenv("TELA_AI_PROBE_INTERVAL", "0")
	srv.StartAIHealthProbe(context.Background())
	if !probeDisabled() {
		t.Fatal("probe_disabled false with TELA_AI_PROBE_INTERVAL=0")
	}
}
