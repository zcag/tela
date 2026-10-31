package api

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// AI-health probing makes the host-context `ai_available` flag reflect the
// backing AI services' ACTUAL reachability, not just whether they're configured.
// A background prober pings the embedder and (when configured) the chat model on
// an interval (TELA_AI_PROBE_INTERVAL, default 30s) and caches a PER-SERVICE
// verdict; the user-facing "AI unavailable" header flips automatically when
// either is down and clears on its own when it returns — no admin kill-switch
// toggle needed.
//
// Why a cached background probe and not a per-request or /health-time ping:
// running it on a timer (not a hot path) keeps the cost to one probe per service
// per interval regardless of traffic. This is the proactive complement to the
// per-request degrade in the ask UI.
//
// The embed probe does a REAL minimal end-to-end embed (rag.ProbeEmbed), not a
// liveness ping: a liveness check only proves the proxy/host answers, so a
// reachable-but-unusable embedder — e.g. a congested/lossy link that times out on
// an actual embed while the proxy stays up — reads as healthy and the outage goes
// silent (exactly what bit us). Exercising the true path is the only signal that
// tracks what ask/search actually hit; the side effect is it keeps the embed
// model warm, which a responsive RAG path wants anyway. The chat probe stays a
// cheap liveness ping (a real completion each interval is too costly).
//
// The per-service detail (latency, last-ok, time-in-state) feeds two surfaces:
// the Prometheus gauges aiUp / aiProbeLatency (scraped → Grafana + alerts) and
// the in-app admin breakdown (GET /api/admin/ai-endpoints). Per-backend and
// fallback detail — which relief endpoint served, how often the pool failed
// over — lives in the LiteLLM proxy's own /metrics, not here.

const (
	aiProbeIntervalDefault = 30 * time.Second
	aiProbeTimeout         = 5 * time.Second
)

// aiProbeInterval reads TELA_AI_PROBE_INTERVAL (a Go duration). 0 turns the
// prober off; unset or invalid keeps the 30s default; anything shorter than
// aiProbeTimeout is raised to it, so a typo like 1ms can't probe back to back.
func aiProbeInterval() time.Duration {
	v := strings.TrimSpace(os.Getenv("TELA_AI_PROBE_INTERVAL"))
	if v == "" {
		return aiProbeIntervalDefault
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		slog.Warn("ai health: ignoring invalid TELA_AI_PROBE_INTERVAL, probing every 30s", "value", v)
		return aiProbeIntervalDefault
	}
	if d > 0 && d < aiProbeTimeout {
		slog.Warn("ai health: TELA_AI_PROBE_INTERVAL below the minimum, probing every 5s", "value", v)
		return aiProbeTimeout
	}
	return d
}

// aiServiceHealth is the cached probe outcome for one service (embed or chat).
type aiServiceHealth struct {
	configured bool          // service is wired up (so absence vs down is distinguishable)
	healthy    bool          // last probe reached it
	reason     string        // non-empty when unhealthy
	latency    time.Duration // duration of the last probe
	lastOK     time.Time     // last time it probed healthy (zero = never)
	since      time.Time     // when the current up/down state began
	probedAt   time.Time     // when the last probe ran
}

// aiHealthState caches the latest per-service probe outcome. checked stays false
// until the first probe completes, so aiHealthy() can report optimistically at
// boot (and in tests, which never start the prober) instead of flapping off.
type aiHealthState struct {
	mu       sync.RWMutex
	checked  bool
	disabled bool // TELA_AI_PROBE_INTERVAL=0: no prober, so checked never turns true
	embed    aiServiceHealth
	chat     aiServiceHealth
}

// aiHealthy reports whether AI should be advertised as available to clients: AI
// enabled (configured + not admin-disabled) AND, once probed, every configured
// service reachable. Before the first probe — or with no prober (tests) — it
// trusts aiEnabled(), matching the pre-probe world.
func (s *Server) aiHealthy() bool {
	if !s.aiEnabled() {
		return false
	}
	s.aiHealth.mu.RLock()
	defer s.aiHealth.mu.RUnlock()
	if !s.aiHealth.checked {
		return true
	}
	if !s.aiHealth.embed.healthy {
		return false
	}
	if s.aiHealth.chat.configured && !s.aiHealth.chat.healthy {
		return false
	}
	return true
}

// aiHealthReason returns the first unhealthy service's reason for the admin
// header / stats, or "" when healthy. Caller need not hold the lock.
func (s *Server) aiHealthReason() string {
	if !s.aiEnabled() {
		return "ai disabled"
	}
	s.aiHealth.mu.RLock()
	defer s.aiHealth.mu.RUnlock()
	if !s.aiHealth.checked {
		return ""
	}
	if !s.aiHealth.embed.healthy {
		return "embedder: " + s.aiHealth.embed.reason
	}
	if s.aiHealth.chat.configured && !s.aiHealth.chat.healthy {
		return "chat: " + s.aiHealth.chat.reason
	}
	return ""
}

// aiHealthSnapshot returns a copy of the per-service health for the admin
// breakdown, without exposing the lock. checked is false before the first probe;
// disabled is true when the prober is off, so checked never will be.
func (s *Server) aiHealthSnapshot() (checked, disabled bool, embed, chat aiServiceHealth) {
	s.aiHealth.mu.RLock()
	defer s.aiHealth.mu.RUnlock()
	return s.aiHealth.checked, s.aiHealth.disabled, s.aiHealth.embed, s.aiHealth.chat
}

// StartAIHealthProbe launches the background prober. Called once from the real
// entrypoint (cmd/tela), NOT from New(), so test servers never spawn it or hit
// the network — they fall through to aiHealthy()'s optimistic default.
func (s *Server) StartAIHealthProbe(ctx context.Context) {
	interval := aiProbeInterval()
	if interval == 0 {
		s.aiHealth.mu.Lock()
		s.aiHealth.disabled = true
		s.aiHealth.mu.Unlock()
		slog.Info("ai health: prober disabled (TELA_AI_PROBE_INTERVAL=0) — AI availability follows configuration only")
		return
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		s.probeAI(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.probeAI(ctx)
			}
		}
	}()
}

// probeAI runs one round. When AI is off (unconfigured or admin kill-switch) it
// skips the network and clears the gauges. Otherwise it probes BOTH services
// (embed always; chat when configured) — independently, so the admin screen and
// Grafana show each one's true state even when the other is down.
func (s *Server) probeAI(ctx context.Context) {
	if !s.aiEnabled() {
		s.mergeAIHealth(aiServiceHealth{}, aiServiceHealth{}, false)
		aiUp.DeleteLabelValues("embed")
		aiUp.DeleteLabelValues("chat")
		aiProbeLatency.DeleteLabelValues("embed")
		aiProbeLatency.DeleteLabelValues("chat")
		return
	}

	embed := s.probeOne(ctx, "embed", true, s.rag.ProbeEmbed)

	chatEnabled := s.llm.Enabled()
	var chat aiServiceHealth
	if chatEnabled {
		chat = s.probeOne(ctx, "chat", true, s.llm.Ping)
	} else {
		aiUp.DeleteLabelValues("chat")
		aiProbeLatency.DeleteLabelValues("chat")
	}

	s.mergeAIHealth(embed, chat, chatEnabled)
}

// probeOne times a single liveness ping, publishes the Prometheus gauges, and
// returns the fresh per-service health (carrying lastOK/since forward).
func (s *Server) probeOne(ctx context.Context, service string, configured bool, ping func(context.Context) error) aiServiceHealth {
	pctx, cancel := context.WithTimeout(ctx, aiProbeTimeout)
	defer cancel()
	start := time.Now()
	err := ping(pctx)
	lat := time.Since(start)

	up := 0.0
	if err == nil {
		up = 1.0
	}
	aiUp.WithLabelValues(service).Set(up)
	aiProbeLatency.WithLabelValues(service).Set(lat.Seconds())

	h := aiServiceHealth{configured: configured, healthy: err == nil, latency: lat, probedAt: start}
	if err != nil {
		h.reason = err.Error()
	}
	return h
}

// mergeAIHealth installs the new per-service outcomes, carrying lastOK/since
// across rounds (they describe history the fresh probe doesn't know), and logs
// only on a state change so the log shows AI going down/up without per-tick noise.
func (s *Server) mergeAIHealth(embed, chat aiServiceHealth, chatConfigured bool) {
	chat.configured = chatConfigured

	s.aiHealth.mu.Lock()
	beforeHealthy := s.aiHealth.checked && s.aiHealth.embed.healthy &&
		(!s.aiHealth.chat.configured || s.aiHealth.chat.healthy)
	carry(&s.aiHealth.embed, &embed, embed.probedAt)
	carry(&s.aiHealth.chat, &chat, chat.probedAt)
	s.aiHealth.checked = true
	s.aiHealth.embed = embed
	s.aiHealth.chat = chat
	afterHealthy := embed.healthy && (!chat.configured || chat.healthy)
	s.aiHealth.mu.Unlock()

	if beforeHealthy == afterHealthy {
		return
	}
	if afterHealthy {
		slog.Info("ai health: available")
	} else {
		slog.Warn("ai health: unavailable", "reason", s.aiHealthReason())
	}
}

// carry copies the prior lastOK/since history onto a fresh probe result: lastOK
// advances to now when this probe was healthy, and since holds the timestamp the
// current up/down state began (reset only when the state flips).
func carry(prev, next *aiServiceHealth, now time.Time) {
	next.lastOK = prev.lastOK
	if next.healthy {
		next.lastOK = now
	}
	if prev.probedAt.IsZero() || prev.healthy != next.healthy {
		next.since = now
	} else {
		next.since = prev.since
	}
}
