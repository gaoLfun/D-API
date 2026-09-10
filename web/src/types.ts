export interface Upstream {
  id: number
  name: string
  kind: 'newapi' | 'sub2api'
  base_url: string
  user_agent?: string
  enabled: boolean
  balance_protection_enabled?: boolean
  balance_suspended?: boolean
  zero_balance_checks?: number
  priority: number
  protocols: string[]
  models: string[]
  models_locked?: boolean
  pricing_profile_id?: number
  model_aliases: Record<string, string>
  connect_timeout_ms?: number
  first_byte_timeout_ms?: number
  idle_timeout_ms?: number
  failure_threshold: number
  cooldown_seconds?: number
  health_status: string
  consecutive_failures?: number
  circuit_open_until?: string
  last_check_at?: string
  last_error?: string
  today_requests?: number
  today_tokens?: number
  today_cost_usd?: number
  today_cost_coverage?: number
  lifetime_requests?: number
  lifetime_cost_usd?: number
  lifetime_cost_coverage?: number
  balance?: { status?: string; available?: number; used?: number; currency?: string; unlimited?: boolean; updated_at?: string; last_success_at?: string }
}

export interface UpstreamGroup {
  key: string
  base_url: string
  items: Upstream[]
  priority: number
  total: number
  healthy: number
  enabled: number
  available: number
  balance_suspended: number
  circuit_open: number
  protocols: string[]
  models: string[]
  today_requests: number
  today_tokens: number
  last_check_at?: string
}

export interface ClientKey {
  id: number
  name: string
  prefix?: string
  key_prefix?: string
  enabled: boolean
  protocols: string[]
  models: string[]
  last_used_at?: string
  created_at: string
  group_id: number
  group_name?: string
}

export interface Group {
  id: number
  name: string
  enabled: boolean
  upstream_ids: number[]
  key_count: number
  created_at: string
  updated_at: string
}

export interface RequestLog {
  id?: number
  request_id: string
  upstream_id?: number
  upstream_name?: string
  group_name?: string
  api_key_name?: string
  protocol: string
  model: string
  status_code: number
  duration_ms: number
  ttfb_ms?: number | null
  ttft_ms?: number | null
  attempts: Array<{ upstream_id?: number; upstream_name?: string; status_code?: number; error?: string; duration_ms?: number; ttfb_ms?: number | null; ttft_ms?: number | null }>
  usage?: {
    input_tokens?: number
    output_tokens?: number
    cached_input_tokens?: number
    cache_creation_input_tokens?: number
    uncached_input_tokens?: number
  }
  error_code?: string
  cost_usd?: number
  created_at: string
}

export interface Channel {
  id: number
  name: string
  kind: 'email' | 'webhook'
  enabled: boolean
  created_at?: string
}

export interface AlertRule {
  id: number
  event: string
  upstream_id?: number
  threshold: number
  window_seconds: number
  cooldown_seconds: number
  max_notifications: number
  enabled: boolean
}

export interface ModelProtocolResult {
  protocol: string
  status: 'success' | 'degraded' | 'failed'
  status_code?: number
  latency_ms: number
  ping_latency_ms?: number
  error?: string
}

export interface ModelTestReport {
  model: string
  status: 'available' | 'partial' | 'unavailable'
  results: ModelProtocolResult[]
  error?: string
}


export interface UsageRow {
 upstream_id?: number; api_key_id?: number; group_id?: number
 usage_requests?: number; cache_hit_requests?: number; cached_requests?: number; token_hit_rate?: number | null; cache_request_hit_rate?: number | null
 hour?: string; day?: string; date?: string; label?: string; dimension_label?: string
 upstream_name?: string; api_key_name?: string; group_name?: string; protocol?: string; model?: string
 requests?: number; successes?: number; tokens?: number; total_tokens?: number
 input_tokens?: number; output_tokens?: number; cached_input_tokens?: number; cache_creation_input_tokens?: number
 uncached_input_tokens?: number; cache_read_tokens?: number; cache_write_tokens?: number
 cost_usd?: number; cost_known_requests?: number; cost_coverage?: number | null
 cache_hit_rate?: number | null; request_hit_rate?: number | null
 avg_duration_ms?: number | null; average_duration_ms?: number | null; p95_duration_ms?: number | null; p95_ms?: number | null
 duration_ms?: number; total_duration_ms?: number
}
export interface UsageReport {
 daily?: UsageRow[]; trend?: UsageRow[]; items?: UsageRow[]; data?: UsageRow[]
 totals?: UsageRow; summary?: UsageRow
}
export interface Dashboard {
 active_alerts?: number; usage_requests_24h?: number; input_tokens_24h?: number; output_tokens_24h?: number; cached_input_tokens_24h?: number; cache_hit_requests_24h?: number
 daily?: UsageRow[]; hourly?: UsageRow[]; trend?: UsageRow[]
 cost_usd_24h?: number; cost_coverage?: number | null; cache_hit_rate?: number | null; request_hit_rate?: number | null
 upstreams_total?: number; total_upstreams?: number; upstreams_healthy?: number; healthy_upstreams?: number
 requests_24h?: number; request_count?: number; success_rate?: number; avg_latency_ms?: number; average_latency_ms?: number
 stats?: Dashboard; summary?: Dashboard
}
export interface ModelPrice { model: string; input_usd_per_million: number; output_usd_per_million: number; cache_read_usd_per_million: number; cache_write_usd_per_million: number }
export interface PricingProfile { id: number; name: string; provider: string; source_url: string; source_version: string; last_refreshed_at?: string; model_count?: number; prices?: ModelPrice[] }
export interface PricingData { profiles?: PricingProfile[]; usd_cny_rate?: number }
export interface KeySecret { id?: number; key?: string; api_key?: string; secret?: string }
export interface ProbeResult { status?: string; error?: string; models?: string[]; latency_ms?: number }
