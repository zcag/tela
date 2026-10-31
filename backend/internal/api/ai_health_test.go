package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

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
