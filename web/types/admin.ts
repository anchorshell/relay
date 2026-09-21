export type ScopeType = 'global' | 'provider' | 'endpoint' | 'lane' | 'user' | 'user_model' | 'user_provider'
export type Metric = 'requests' | 'tokens' | 'spend' | 'concurrency'
export type Period = 'second' | 'minute' | 'hour' | 'day' | 'month'
export type UsageWindow = 'hour' | 'day' | 'week' | 'month'
export type UsageAnalyticsBucket = 'minute' | 'hour' | 'day' | 'week' | 'month'
export type RouteKind = 'chat' | 'responses' | 'embeddings' | 'multi'
export type HealthStatus = 'healthy' | 'rate_limited' | 'cooling_down' | 'unhealthy'

export interface CharacterizationScore {
  label: string
  score: number
}

export interface RequestCharacterization {
  version: string
  taxonomy_version: string
  model_version: string
  primary_action: string
  secondary_actions: string[]
  target_objects: string[]
  output_objects: string[]
  domains: string[]
  observed_flags: string[]
  required_capabilities: string[]
  context_burden: { tier: string, score: number, input_tokens: number, evidence_ids: string[], components: CharacterizationScore[] }
  reasoning_complexity: { tier: string, score: number, evidence_ids: string[], components: CharacterizationScore[] }
  harness: { profile: string, confidence: number, evidence_ids: string[] }
  action_scores: CharacterizationScore[]
  object_scores: CharacterizationScore[]
  domain_scores: CharacterizationScore[]
  confidence: number
  classification_duration_ms?: number
  classification_background?: boolean
  evidence_ids: string[]
  classifier_status: string
}

export interface Provider {
  id: string
  name: string
  slug: string
  base_url: string
  max_latency_ms: number
  auth_mode: string
  auth_header_name: string
  enabled: boolean
  notes: string
  health_status: HealthStatus
  created_at: string
  updated_at: string
}

export interface Credential {
  id: string
  provider_id: string
  name: string
  has_secret: boolean
  enabled: boolean
  created_at: string
  updated_at: string
}

export interface Endpoint {
  id: string
  provider_id: string
  credential_id: string
  name: string
  slug: string
  upstream_model: string
  route_kind: RouteKind
  enabled: boolean
  manual_rank: number
  suggested_rank: number
  suggested_score: number
  quality_score: number
  context_window: number
  param_size_b: number
  modalities: string[]
  descriptor_tags: string[]
  supports_streaming: boolean
  supports_tools: boolean
  supports_vision: boolean
  reasoning_control_kind?: string
  allowed_reasoning_efforts?: string[]
  default_reasoning_effort?: string
  maximum_reasoning_effort?: string
  pacing?: boolean | null
  health_status: HealthStatus
  cooldown_until?: string | null
  cooldown_reason?: string
  cooldown_status_code?: number
  max_latency_ms: number
  notes: string
  created_at: string
  updated_at: string
}

export interface RoutingLane {
  id: string
  name: string
  slug: string
  description: string
  hidden?: boolean
  enabled: boolean
  default_max_wait_ms: number
  allow_fallback: boolean
  default_priority: number
  created_at: string
  updated_at: string
}

export interface LaneMembership {
  id: string
  lane_id: string
  endpoint_id: string
  manual_rank: number
  enabled: boolean
  created_at: string
  updated_at: string
}

export interface LimitPolicy {
  id: string
  scope_type: ScopeType
  scope_id: string
  metric: Metric
  period: Period
  limit_value: number
  enabled: boolean
  source: string
  created_at: string
  updated_at: string
}

export interface ObservedLimit {
  id: string
  scope_type: ScopeType
  scope_id: string
  metric: Metric
  period: Period
  observed_value: number
  source_header: string
  observed_at: string
  expires_at?: string | null
  enabled: boolean
  created_at: string
  updated_at: string
}

export interface PricingPolicy {
  id: string
  endpoint_id: string
  currency: string
  input_cost_micros_per_1m_tokens: number
  output_cost_micros_per_1m_tokens: number
  cached_input_cost_micros_per_1m_tokens: number
  flat_request_cost_micros: number
  created_at: string
  updated_at: string
}

export type GuardrailStage = 'pre_dispatch' | 'post_response'
export type GuardrailDecision = 'allow' | 'log_only' | 'block' | 'replace_response' | 'error'

export interface GuardrailRule {
  id: string
  label: string
  source: 'json_body' | 'http_status' | 'response_header'
  path: string
  header?: string
  operator: string
  value?: any
}

export interface GuardrailRuleSet {
  match_mode: 'any' | 'all'
  rules: GuardrailRule[]
  missing_path: 'error' | 'no_match'
  on_match: Record<string, any>
  on_no_match: Record<string, any>
}

export interface Guardrail {
  id: string
  name: string
  slug: string
  description: string
  enabled: boolean
  preset_slug: string
  priority: number
  http_method: 'POST' | 'PUT' | 'PATCH'
  base_url: string
  auth_mode: 'none' | 'bearer' | 'api_key_header' | 'basic' | 'custom_secret_header'
  credential_id?: string | null
  secret_header_name: string
  request_headers_json: string
  timeout_ms: number
  max_request_bytes: number
  max_response_bytes: number
  follow_redirects: boolean
  network_access_mode: 'public_https' | 'private_network' | 'loopback'
  pre_dispatch_enabled: boolean
  post_response_enabled: boolean
  pre_request_template_json: string
  pre_response_rules_json: string
  pre_failure_policy_json: string
  post_request_template_json: string
  post_response_rules_json: string
  post_failure_policy_json: string
  sample_response_json: string
  last_test_status: string
  last_tested_at?: string | null
  last_test_latency_ms: number
  last_test_error_code: string
  has_credential?: boolean
  binding_count?: number
  created_at: string
  updated_at: string
}

export interface GuardrailBinding {
  id: string
  guardrail_id: string
  routing_lane_id?: string | null
  provider_id?: string | null
  endpoint_id?: string | null
  enabled: boolean
}

export interface GuardrailPreset {
  slug: string
  name: string
  description: string
  category: string
  official_documentation_reference: string
  contract_verified_at: string
  pricing_note: string
  default_url: string
  http_method: string
  auth_mode: string
  secret_header_name?: string
  required_setup_fields: string[]
  pre_request_template_json?: string
  post_request_template_json?: string
  sample_response?: string
  suggested_response_rules: GuardrailRuleSet
  starter_policies?: Array<{ slug: string, name: string, description: string, response_rules: GuardrailRuleSet }>
  supported_stages: GuardrailStage[]
  known_input_limits: string
  warning_text: string
  ready: boolean
}

export interface AppSetting {
  key: string
  value_json: string
  updated_at: string
}

export interface UsageSummary {
  id: string
  scope_type: ScopeType
  scope_id: string
  metric: Metric
  period: Period
  window_start: string
  used_value: number
  updated_at: string
}

export type CapacityState = 'healthy' | 'rate-limited' | 'cooling-down' | 'unhealthy' | 'unavailable'

export interface CapacityLimitRow {
  key: string
  label: string
  scope_type: ScopeType
  scope_id: string
  target_type?: 'global' | 'model' | 'provider' | string
  target_key?: string
  actor_id?: string
  metric: Metric
  period: Period
  configured: number
  effective: number
  used: number
  reserved: number
  remaining: number
  percent: number
  window_start: string
  reset_at?: string
  source: string
  user_scoped?: boolean
  blocked?: boolean
  blocked_until?: string | null
  blocked_reason?: string
}

export interface ModelCapacitySnapshot {
  endpoint_id: string
  provider_id: string
  capacity_state: CapacityState
  health_status: HealthStatus
  cooldown_until?: string | null
  cooldown_reason?: string
  cooldown_status_code?: number
  limit_rows: CapacityLimitRow[]
}

export interface CapacitySnapshot {
  generated_at: string
  sequence: number
  external_rows_status?: 'ok' | 'context_unavailable' | 'error' | string
  queue: QueueSnapshot
  queue_items?: QueueItem[]
  models: ModelCapacitySnapshot[]
  visible_queue_limit?: number
  preview_session_id?: string
  preview_generation?: number
}

export interface UsageSeriesPoint {
  label: string
  bucket_start: string
  bucket_end: string
  value: number
  requests?: number
  tokens_up?: number
  tokens_down?: number
  cost_micros?: number
}

export interface UsageSeriesResponse {
  metric: 'requests' | 'tokens' | 'spend'
  window: UsageWindow
  points: UsageSeriesPoint[]
}

export interface UsageAnalyticsTotals {
  requests: number
  tokens_in: number
  tokens_down: number
  cost_micros: number
}

export interface UsageAnalyticsRow {
  provider_id: string
  endpoint_id: string
  provider_name: string
  model_name: string
  requests: number
  tokens_in: number
  tokens_down: number
  cost_micros: number
}

export interface UsageAnalyticsResponse {
  start_date: string
  end_date: string
  metric: 'requests' | 'tokens' | 'spend'
  bucket: UsageAnalyticsBucket
  timezone_offset_minutes: number
  series: UsageSeriesPoint[]
  totals: UsageAnalyticsTotals
  rows: UsageAnalyticsRow[]
}

export interface CharacterizationIntentAnalyticsItem {
  primary_action: string
  value: number
  percentage: number
}

export interface CharacterizationIntentAnalyticsResponse {
  start_date: string
  end_date: string
  metric: 'requests' | 'tokens' | 'spend'
  timezone_offset_minutes: number
  total: number
  items: CharacterizationIntentAnalyticsItem[]
}

export interface RequestLog {
  id: string
  request_id: string
  actor_id?: string
  api_key_uuid?: string
  parent_request_id: string
  lane_id?: string | null
  endpoint_id?: string | null
  provider_id?: string | null
  route_kind: RouteKind
  incoming_model: string
  selected_upstream_model: string
  status_code: number
  task_state: string
  queued_at?: string | null
  started_at?: string | null
  finished_at?: string | null
  wait_ms: number
  latency_ms: number
  streaming: boolean
  priority: number
  fallback_count: number
  estimated_input_tokens: number
  estimated_output_tokens: number
  actual_input_tokens: number
  actual_output_tokens: number
  actual_total_tokens: number
  estimated_cost_micros: number
  actual_cost_micros: number
  error_text: string
  candidate_trace_json?: string
  limit_impact_json?: string
  applied_overrides_json?: string
  upstream_headers_json?: string
  request_body_json?: string
  upstream_request_json?: string
  response_body_json?: string
  request_bodies_stored?: boolean
  primary_action?: string | null
  action_confidence?: number | null
  characterization_version?: string | null
  characterization_json?: string | null
  characterization_duration_ms?: number
  guardrail_status?: string
  guardrail_duration_ms?: number
  guardrail_pre_duration_ms?: number
  guardrail_post_duration_ms?: number
  provider_latency_ms?: number
  total_time_ms?: number
  guardrail_results_json?: string
  guardrail_response_bodies_json?: string
  created_at: string
  updated_at: string
}

export interface RequestLogPage {
  items: RequestLog[]
  total: number
  limit: number
  offset: number
  next_offset: number
  has_more: boolean
}

export interface RecentFlowRequest {
  request_id: string
  actor_id?: string
  api_key_uuid?: string
  lane_id?: string | null
  endpoint_id?: string | null
  provider_id?: string | null
  incoming_model: string
  selected_upstream_model: string
  status_code: number
  task_state: string
  queued_at?: string | null
  started_at?: string | null
  finished_at?: string | null
  wait_ms: number
  latency_ms: number
  characterization_duration_ms?: number
  guardrail_status?: string
  guardrail_pre_duration_ms?: number
  provider_latency_ms?: number
  guardrail_post_duration_ms?: number
  total_time_ms?: number
  fallback_count: number
  estimated_input_tokens: number
  estimated_output_tokens: number
  actual_input_tokens: number
  actual_output_tokens: number
  actual_total_tokens: number
  estimated_cost_micros: number
  actual_cost_micros: number
  error_text?: string
  created_at: string
  updated_at: string
}

export interface RecentFlowActivityResponse {
  requests: RecentFlowRequest[]
}

export interface RecentModelUsage {
  key: string
  lane_id: string
  lane_name: string
  endpoint_id: string
  provider_id: string
  provider_name: string
  model_name: string
  last_used_at: string
  request_count: number
}

export interface RecentModelUsageResponse {
  items: RecentModelUsage[]
}

export interface EffectiveLimit {
  metric: Metric
  period: Period
  configured?: number | null
  observed?: number | null
  effective?: number | null
  scope_type: ScopeType
  scope_id: string
  source_header?: string
  used?: number
  reserved?: number
  next_available_at?: string
}

export interface CandidateTrace {
  endpoint_id: string
  endpoint_name: string
  provider_id: string
  upstream_model: string
  rank: number
  fallback_count: number
  eligible_at?: string
  predicted_wait_ms: number
  decision: string
  reason: string
  effective_limits?: EffectiveLimit[]
}

export interface QueueSnapshot {
  queue_depth_global: number
  queue_depth_by_endpoint: Record<string, number>
  in_flight_by_endpoint: Record<string, number>
  states: Record<string, number>
}

export interface QueueItem {
  task_id: string
  request_id: string
  lane: string
  incoming_model: string
  endpoint_id: string
  endpoint_name: string
  provider_id: string
  actor_id?: string
  api_key_uuid?: string
  state: string
  priority: number
  queued_at: string
  started_at?: string | null
  wait_ms: number
  predicted_eligible_at: string
  resource_eligible_at?: string | null
  user_eligible_at?: string | null
  user_limit_reason?: string
  defer_scope?: string
  defer_reason?: string
  delay_reason?: string
  substatus?: string
  uploaded_tokens?: number
  downloaded_tokens?: number
  estimated_cost_micros: number
  fallback_count: number
  candidate_trace: CandidateTrace[]
}

export interface TelemetryEvent {
  type: string
  timestamp: string
  payload: Record<string, any>
  stream_id?: string
  sequence?: number
}

export interface SystemInfo {
  laya?: { configured: boolean, ready: boolean }
  http_addr: string
  db_path: string
  temp_dir: string
  log_level: string
  insecure_dev: boolean
  admin_token_configured: boolean
  master_key_configured: boolean
}

export interface LaneSimulationResult {
  selected?: CandidateTrace | null
  candidates: CandidateTrace[]
}

export interface LiveFlowSimulationRequest {
  request_id: string
  sequence: number
  incoming_model: string
  queued_at: string
  selected_at?: string | null
  wait_ms: number
  status: string
  state: string
  endpoint_id: string
  endpoint_name: string
  provider_id: string
  actor_id?: string
  predicted_eligible_at: string
  resource_eligible_at?: string | null
  user_eligible_at?: string | null
  user_limit_reason?: string
  defer_scope?: string
  defer_reason?: string
  delay_reason?: string
  selected?: CandidateTrace | null
  candidates: CandidateTrace[]
  estimated_input_tokens: number
  estimated_output_tokens: number
  estimated_cost_micros: number
  fallback_count: number
}

export interface LiveFlowSimulationResult {
  started_at: string
  request_count: number
  arrival_interval_ms: number
  preview_session_id?: string
  requests: LiveFlowSimulationRequest[]
}

export type AdminPageKey =
  | 'dashboard'
  | 'providers'
  | 'groups'
  | 'limits'
  | 'usage'
  | 'requests'
  | 'queue'
  | 'playground'
  | 'realtime'
  | 'settings'
  | 'setup'

export interface AdminCatalogPayload {
  providers?: Provider[]
  credentials?: Credential[]
  endpoints?: Endpoint[]
  lanes?: RoutingLane[]
  memberships?: LaneMembership[]
  limit_policies?: LimitPolicy[]
  observed_limits?: ObservedLimit[]
  pricing_policies?: PricingPolicy[]
  guardrails?: Guardrail[]
  guardrail_bindings?: GuardrailBinding[]
}

export interface AdminMetricsPayload {
  summary?: Record<string, any>
  queue?: QueueSnapshot
  usage?: UsageSummary[]
  spend?: UsageSummary[]
}

export interface AdminPagePayload {
  page: AdminPageKey | string
  catalog?: AdminCatalogPayload
  queue_items?: QueueItem[]
  settings?: AppSetting[]
  system?: SystemInfo
}
