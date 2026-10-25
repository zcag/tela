package api

import (
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
