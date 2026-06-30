import { useQuery } from '@tanstack/react-query'
import { api } from '../api'

// AI endpoints & reliability (GET /api/admin/ai-endpoints). Per-service live
// health + the relief-proxy topology — the in-app failover breakdown. Deep
// per-backend/fallback detail lives in Grafana (grafana_url). See backend
// ai_endpoints.go.

export interface AIEndpointHealth {
  service: 'embed' | 'chat'
  configured: boolean
  healthy: boolean
  reason?: string
  endpoint: string // redacted scheme://host
  model: string
  latency_ms: number
  last_ok?: string // sqlite-format ts
  since?: string
}

// Foreground (ask/assist) concurrency gate snapshot — present only when a gate
// is configured. limit = cap, in_flight = live slots held, spills = cumulative
// overload spills to the relief layer, overflow = a spill target is configured.
export interface AIGate {
  limit: number
  in_flight: number
  spills: number
  overflow: boolean
}

export interface AIEndpoints {
  enabled: boolean
  probed: boolean
  healthy: boolean
  relief_proxy: boolean // a failover proxy fronts the endpoints (TELA_AI_RELIEF)
  services: AIEndpointHealth[]
  grafana_url?: string
  gate?: AIGate
}

export const aiEndpointsKeys = { endpoints: ['admin-ai-endpoints'] as const }

export function useAIEndpoints() {
  return useQuery({
    queryKey: aiEndpointsKeys.endpoints,
    queryFn: () => api<AIEndpoints>('/api/admin/ai-endpoints'),
    // Reliability view — refresh on the prober's cadence so it stays live.
    staleTime: 20_000,
    refetchInterval: 30_000,
  })
}
