package api

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// metricsRegistry is the process-wide Prometheus registry. It is NOT the
// default global registry: a private registry keeps the exported surface to
// exactly what we register here (HTTP request metrics + the standard Go runtime
// and process collectors), and avoids picking up stray metrics any imported
// dependency might register on the global default.
var (
	metricsRegistry = prometheus.NewRegistry()

	// httpRequests counts HTTP requests by method, the matched route pattern,
	// and response status. The route pattern (e.g. "GET /api/pages/{id}") is
	// used instead of the raw path so per-id paths collapse to one low-
	// cardinality series; unmatched paths fall back to "other" (see
	// routePattern).
	httpRequests = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tela_http_requests_total",
			Help: "Total HTTP requests by method, route pattern, and status code.",
		},
		[]string{"method", "route", "status"},
	)

	// httpDuration observes request latency in seconds, labelled the same way
	// (minus status) so it stays low-cardinality.
	httpDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "tela_http_request_duration_seconds",
			Help:    "HTTP request latency in seconds by method and route pattern.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "route"},
	)

	// clientErrors counts browser-side error reports beaconed to
	// /api/client-errors, by kind (error | unhandledrejection | react | collab).
	// The kind label is bounded by the client + the clientErrMaxKind truncation,
	// so cardinality stays low; this is the alertable signal for "users are
	// hitting crashes we never see server-side".
	clientErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tela_client_errors_total",
			Help: "Total browser-side error reports by kind.",
		},
		[]string{"kind"},
	)

	// atlasKills counts Atlas runs killed by the stuck-run watchdog (running for
	// more than atlasRunTimeout = 4h). Any increment is a signal that a run hung
	// badly enough to hit the watchdog; a spike means something is systematically
	// wrong with the LLM or the clone path.
	atlasKills = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "tela_atlas_stuck_runs_killed_total",
		Help: "Atlas runs killed by the 4h stuck-run watchdog.",
	})

	// polarLastWebhook tracks the Unix timestamp of the last successfully
	// processed Polar webhook. Alertable when it's been silent for >24h while
	// active subscriptions exist (possible Polar delivery outage or misconfigured
	// endpoint). Set to 0 on boot (never received); the alert rule must account
	// for this with a noDataState=OK posture.
	polarLastWebhook = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "tela_polar_last_webhook_timestamp_seconds",
		Help: "Unix timestamp of the last successfully processed Polar webhook. 0 = never received.",
	})

	// aiTokens counts estimated token consumption at the LLM service chokepoints
	// (chat, embed, image). Input + output tokens are summed for each call so
	// increase() over a window gives the total token rate — the alertable signal
	// for a runaway Atlas run or Ask loop exhausting the LLM budget.
	aiTokens = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tela_ai_tokens_total",
			Help: "Estimated AI tokens consumed by kind (chat|embed|image).",
		},
		[]string{"kind"},
	)

	// aiForegroundSpills counts foreground (ask/assist) completions spilled to the
	// overflow target because the primary concurrency gate was saturated — i.e. L1
	// is healthy but overloaded. Distinct from a down-failover (which LiteLLM
	// handles and exports separately): any sustained increase here means live
	// traffic is exceeding L1's capacity and bleeding onto the (often paid) relief
	// layer — the alertable "lower the gate or add capacity" signal.
	aiForegroundSpills = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "tela_llm_foreground_spill_total",
		Help: "Foreground (ask/assist) completions spilled to the overflow target due to a saturated primary concurrency gate.",
	})

	// aiUp reports each backing AI service's reachability as seen by the
	// background health prober: 1 = reachable, 0 = down, absent = not configured.
	// service is "embed" or "chat". This is the alertable up/down signal (a
	// LiteLLM relief pool can mask single-backend outages, so this only fires
	// when the WHOLE service — proxy included — is unreachable). Per-backend /
	// fallback detail comes from LiteLLM's own /metrics, not here.
	aiUp = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "tela_ai_service_up",
			Help: "AI service reachability from the health prober (1=up,0=down) by service (embed|chat).",
		},
		[]string{"service"},
	)

	// aiProbeLatency observes how long the last liveness probe took per service —
	// a slow-but-up backend (the early sign of clogging, before it fails outright)
	// shows here as rising latency while aiUp is still 1.
	aiProbeLatency = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "tela_ai_probe_latency_seconds",
			Help: "Duration of the last AI liveness probe in seconds by service (embed|chat).",
		},
		[]string{"service"},
	)
)

func init() {
	metricsRegistry.MustRegister(
		httpRequests,
		httpDuration,
		clientErrors,
		aiTokens,
		aiForegroundSpills,
		aiUp,
		aiProbeLatency,
		atlasKills,
		polarLastWebhook,
		// Go runtime + process collectors (goroutines, GC, memory, open FDs,
		// CPU, etc.).
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
}

// metricsHandler serves the Prometheus exposition format from our private
// registry. It is gated to instance-admins (session cookie OR an admin-scoped
// PAT as a bearer token) by requireInstanceAdmin — auth.Middleware has already
// resolved the caller onto the request context by the time this runs, since
// /metrics is NOT on auth.IsPublicPath. A Prometheus scraper authenticates with
//
//	Authorization: Bearer tela_pat_<admin-scoped-key>
func (s *Server) metricsHandler() http.Handler {
	promh := promhttp.HandlerFor(metricsRegistry, promhttp.HandlerOpts{})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireInstanceAdmin(w, r); !ok {
			return
		}
		promh.ServeHTTP(w, r)
	})
}

// routePattern returns a low-cardinality label for the request: the Go 1.22+
// ServeMux pattern that matched (e.g. "GET /api/pages/{id}"), so per-id paths
// collapse to a single series. mux.Handler(r) resolves the matched pattern
// without dispatching. Unmatched requests (404) report "other" so a path-
// scanning probe can't blow up label cardinality.
func routePattern(mux *http.ServeMux, r *http.Request) string {
	if _, pattern := mux.Handler(r); pattern != "" {
		return pattern
	}
	return "other"
}
