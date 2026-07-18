export interface Page<T> {
  items: T[];
  next_cursor?: string;
}

export interface TopologyCleanupSummary {
  selectable: boolean;
  selector_kind?: CleanupSelector["kind"];
  selector_key?: string;
  confirmation?: "type_name" | "confirm" | string;
  potential_blockers: number;
}

export interface TopologyEntrySummary {
  key: string;
  asset_id?: string;
  dirty?: boolean;
  name: string;
  native_id?: string;
  resource_count: number;
  cleanup: TopologyCleanupSummary;
}

export interface TopologyViewContext {
  key: string;
  name: string;
  native_id?: string;
}

export interface AccountTopologyView {
  kind: "account";
  global_resources?: TopologyEntrySummary;
  regions: TopologyEntrySummary[];
}

export interface RegionTopologyView {
  kind: "region";
  region: TopologyViewContext;
  public_resources: TopologyEntrySummary;
  vpcs: TopologyEntrySummary[];
}

export type TopologyResourceDomain =
  "network" | "compute" | "storage" | "unknown";

export type TopologyResourceEdgeKind = "relationship" | "lifecycle";

export interface TopologyExternalRelation {
  key: string;
  kind: TopologyResourceEdgeKind;
  relation: string;
  direction: string;
  target_id: string;
  target_name: string;
  target_type: string;
  target_resource_kind_id?: string;
  target_class?: string;
}

export interface TopologyResource {
  key: string;
  asset_id: string;
  dirty?: boolean;
  resource_kind_id: string;
  name: string;
  native_id: string;
  type_name: string;
  type_names?: Record<string, string>;
  icon?: string;
  class?: string;
  console_link_values?: Record<string, string>;
  domain: TopologyResourceDomain;
  state?: string;
  finding_count: number;
  actionable: boolean;
  membership_unknown?: boolean;
  external_relations?: TopologyExternalRelation[];
  cleanup: TopologyCleanupSummary;
}

export interface TopologyResourceEdge {
  key: string;
  source_key: string;
  target_key: string;
  kind: TopologyResourceEdgeKind;
  relation: string;
  metadata?: Record<string, unknown>;
}

export interface TopologyVSwitch {
  key: string;
  asset_id?: string;
  dirty?: boolean;
  name: string;
  native_id: string;
  zone?: string;
  resource_count: number;
  resource_keys: string[];
}

export interface ResourceGraphTopologyView {
  kind: "resource_graph";
  context: TopologyViewContext;
  ancestors?: TopologyViewContext[];
  resources: TopologyResource[];
  edges: TopologyResourceEdge[];
}

export interface VPCTopologyView {
  kind: "vpc";
  region: TopologyViewContext;
  vpc: TopologyViewContext;
  public_resource_keys: string[];
  vswitches: TopologyVSwitch[];
  resources: TopologyResource[];
  edges: TopologyResourceEdge[];
}

export type TopologyView =
  | AccountTopologyView
  | RegionTopologyView
  | ResourceGraphTopologyView
  | VPCTopologyView;

export interface TopologyProjectionWarning {
  code: string;
  relation_key?: string;
  visible_asset_id?: string;
  message: string;
}

export interface TopologyRevision {
  inventory: string;
  graph: string;
  spec_bundle: string;
  projected_at: string;
}

export interface TopologyCoverage {
  status: string;
  last_complete_scan_at?: string;
  failed_shards: number;
}

export interface TopologyResponse {
  revision: TopologyRevision;
  coverage: TopologyCoverage;
  view: TopologyView;
  warnings?: TopologyProjectionWarning[];
  next_cursor?: string;
  truncated: boolean;
}

export interface TopologyQuery {
  focus_key?: string;
  cursor?: string;
  limit?: number;
  resource_class?: string;
  resource_kind_id?: string[];
  risk?: string;
  resource_query?: string;
}

export interface ResourceProperty {
  path: string;
  type:
    | "any"
    | "string"
    | "number"
    | "integer"
    | "boolean"
    | "datetime"
    | "object"
    | "array"
    | string;
  display_names?: Record<string, string>;
  enum?: unknown[];
  operators?: string[];
}

export interface Principal {
  subject: string;
  roles: string[];
}

export interface ResourceKind {
  id: string;
  provider: string;
  native_type: string;
  class?: string;
  scope_kinds?: string[];
  capabilities: string[];
  display_name?: string;
  display_names?: Record<string, string>;
  field_display_names?: Record<string, Record<string, string>>;
  properties?: ResourceProperty[];
  icon?: string;
  console_link_template?: string;
  summary_fields?: string[];
  bundle_revision: string;
}

export interface CompiledSpec {
  resource_kind: ResourceKind;
  revision: string;
  hash: string;
  definition: {
    metadata: { provider: string; nativeType: string; class?: string };
    scope: { kind: string };
    discovery: { source: string };
    actions?: Record<string, unknown>;
  };
}

export interface ProviderBundle {
  provider: string;
  specs: CompiledSpec[];
  revision: string;
  hash: string;
  kinds?: ResourceKind[];
  kinds_revision?: string;
}

export interface ProviderCatalogBundle extends ProviderBundle {
  kinds: ResourceKind[];
  kinds_revision: string;
}

export interface InventorySource {
  name: string;
  root_scope_kinds: string[];
  authoritative_default: boolean;
}

export interface ProviderSite {
  value: "cn" | "intl" | string;
  label_key: string;
}

export interface ProviderDescriptor {
  provider: string;
  sites: ProviderSite[];
  inventory_sources: InventorySource[];
  credential_schemas: CredentialSchema[];
}

export interface CredentialField {
  key: string;
  label_key: string;
  input_type: string;
  secret: boolean;
  required: boolean;
}

export interface CredentialSchema {
  type: string;
  label_key: string;
  flow?: "browser_oauth" | string;
  fields: CredentialField[];
}

export type OAuthFlowStatus =
  "pending" | "authorized" | "failed" | "expired" | "consumed";

export interface OAuthFlow {
  id: string;
  status: OAuthFlowStatus;
  authorization_url?: string;
  expires_at: string;
  error_code?: string;
}

export interface CredentialSummary {
  type: string;
  expires_at?: string;
  updated_at: string;
}

export type ConnectionStatus = "unverified" | "active" | "invalid" | "deleted";

export interface CloudConnection {
  id: string;
  name: string;
  provider: string;
  site?: string;
  partition: string;
  tenant_id?: string;
  principal: string;
  status: ConnectionStatus;
  credential: CredentialSummary;
  enabled_capabilities?: string[];
  active_region_count?: number;
  retired_region_count?: number;
  excluded_region_count?: number;
  last_region_refresh_at?: string;
  last_region_refresh_status?: string;
  region_refresh?: { job_id?: string; status: string; error_code?: string };
  created_at: string;
  updated_at: string;
}

export type RegionLifecycle = "active" | "retired" | "excluded";

export interface ConnectionRegion {
  id: string;
  connection_id: string;
  region_id: string;
  name: string;
  discovered_name?: string;
  name_override?: string;
  origin: "api" | "manual";
  lifecycle: RegionLifecycle;
  first_seen_at?: string;
  last_seen_at?: string;
  created_at: string;
  updated_at: string;
}

export interface CreateConnectionInput {
  name: string;
  provider: string;
  site?: string;
  credential: {
    type: string;
    values: Record<string, string>;
    expires_at?: string;
  };
}

export interface Scope {
  id: string;
  connection_id: string;
  parent_id?: string;
  kind: string;
  native_id: string;
  name: string;
  location?: string;
  created_at: string;
  updated_at: string;
}

export interface Identity {
  provider: string;
  partition: string;
  connection_id: string;
  native_type: string;
  native_id: string;
  scope_key?: string;
}

export interface Asset {
  id: string;
  identity: Identity;
  scope_id: string;
  resource_kind_id: string;
  current_observation_id?: string;
  name?: string;
  state?: string;
  location?: string;
  dirty?: boolean;
  tags?: Record<string, string>;
  capabilities: string[];
  normalized?: Record<string, unknown>;
  first_seen_at: string;
  last_seen_at: string;
  closed_at?: string;
  deleted_at?: string;
}

export interface Coverage {
  source: string;
  scope_id: string;
  resource_kind_id?: string;
  authoritative: boolean;
  complete: boolean;
  item_count: number;
  fresh_at?: string;
  skip_reason?: "product_unsupported" | "provider_region_unavailable";
  failure_reason?: string;
}

export type ScanScopeMode =
  "all_active_regions" | "selected_regions" | "selected_networks";

export type ScanTargetKind = "region" | "global" | "vpc" | "vswitch";

export interface ScanTarget {
  key: string;
  kind: ScanTargetKind;
  region_id: string;
  region_name?: string;
  native_id?: string;
  name?: string;
  parent_native_id?: string;
}

export interface ScanTargetProgress extends ScanTarget {
  status: string;
  completed: number;
  total: number;
  resource_count: number;
  error_count: number;
  summary: string;
}

export interface ScanTask {
  id: string;
  connection_id: string;
  status: string;
  scope_mode: ScanScopeMode;
  requested_by: string;
  targets: ScanTarget[];
  resource_kind_ids?: string[];
  retry_generation: number;
  retry_count: number;
  control_version: number;
  created_at: string;
  updated_at?: string;
  duration_ms?: number;
  started_at?: string;
  finished_at?: string;
  paused_at?: string;
  canceled_at?: string;
  target_progress: ScanTargetProgress[];
  progress: {
    completed: number;
    total: number;
    running: number;
    failed: number;
    resource_count: number;
  };
  allowed_actions: Array<"pause" | "resume" | "cancel" | "retry">;
}

export interface NetworkTargetOption {
  kind: "vpc" | "vswitch";
  region_id: string;
  native_id: string;
  name?: string;
  parent_native_id?: string;
}

export interface NetworkTargetPage {
  items: NetworkTargetOption[];
  next_cursor?: string;
  request_id?: string;
}

export interface CreateScanInput {
  scope_mode: ScanScopeMode;
  region_ids?: string[];
  network_targets?: Array<{
    kind: "vpc" | "vswitch";
    region_id: string;
    native_id: string;
    name?: string;
    parent_native_id?: string;
  }>;
  resource_kind_ids?: string[];
}

export interface Relationship {
  id: string;
  source_asset_id: string;
  target_asset_id: string;
  type: string;
  source: string;
  evidence?: Record<string, unknown>;
  confidence: number;
  graph_revision: string;
  observed_at: string;
  closed_at?: string;
}

export interface LifecycleBinding {
  id?: string;
  controller_asset_id: string;
  managed_asset_id: string;
  authority: string;
  ownership: string;
  cleanup_policy: string;
  direct_cleanup_allowed?: boolean;
  evidence_source?: string;
  evidence?: Record<string, unknown>;
  confidence: number;
  graph_revision?: string;
  observed_at?: string;
}

export interface Finding {
  id: string;
  asset_id: string;
  rule_id: string;
  title?: string;
  description?: string;
  status: string;
  severity: string;
  evidence?: Record<string, unknown>;
  spec_bundle_revision?: string;
  first_seen_at: string;
  last_seen_at: string;
  closed_at?: string;
}

export interface RevisionBinding {
  inventory_revision: string;
  graph_revision: string;
  spec_bundle_revision: string;
  spec_hash: string;
}

export interface CleanupBlocker {
  code: string;
  asset_id?: string;
  controller_id?: string;
  message: string;
  evidence?: Record<string, unknown>;
}

export interface CleanupWarning {
  code: string;
  asset_id?: string;
  controller_id?: string;
  message: string;
  evidence?: Record<string, unknown>;
}

export type CleanupSelector =
  | {
      kind: "connection";
      connection_id: string;
      display_name?: string;
    }
  | {
      kind: "scope";
      connection_id?: string;
      scope_id: string;
      scope_kind?: string;
      descendants?: boolean;
      display_name?: string;
    }
  | {
      kind: "group";
      connection_id?: string;
      scope_id?: string;
      group_key: string;
      display_name?: string;
    }
  | {
      kind: "asset";
      asset_id: string;
      display_name?: string;
    };

export interface ConnectionCoverage {
  connection_id: string;
  status: string;
  failed_shards: number;
  last_complete_scan_at?: string;
}

export interface CleanupScanCoverage {
  status: string;
  connections?: ConnectionCoverage[];
}

export interface CleanupTask {
  id: string;
  connection_id: string;
  status: string;
  selectors: CleanupSelector[];
  resolved_asset_ids: string[];
  selector_asset_ids?: string[][];
  request_options?: Record<string, Record<string, unknown>>;
  revision: RevisionBinding;
  scan_coverage: CleanupScanCoverage;
  snapshot_hash: string;
  blockers?: CleanupBlocker[];
  warnings?: CleanupWarning[];
  invalidation_reason?: string;
  created_by: string;
  created_at: string;
  updated_at?: string;
}

export interface CleanupTaskStep {
  id: string;
  cleanup_task_id: string;
  asset_id: string;
  kind: string;
  action: string;
  depends_on?: string[];
  request_options?: Record<string, unknown>;
  evidence?: Record<string, unknown>;
}

export interface ImpactItem {
  id: string;
  cleanup_task_id: string;
  asset_id: string;
  controller_id: string;
  delegated_to: string;
  ownership?: string;
  cleanup_policy?: string;
  evidence?: Record<string, unknown>;
  expected: string;
  result?: string;
  may_continue_billing: boolean;
}

export interface CleanupTaskAggregate {
  task: CleanupTask;
  steps: CleanupTaskStep[];
  impact_items: ImpactItem[];
}

export interface ExecutionConfirmation {
  typed_names?: Record<string, string>;
  acknowledged?: boolean;
}

export interface ExecutionAttempt {
  id: string;
  connection_id: string;
  cleanup_task_id: string;
  status: string;
  concurrency?: number;
  requested_by: string;
  idempotency_key: string;
  continue_idempotency_key?: string;
  continue_count?: number;
  failure_reason?: string;
  created_at: string;
  updated_at?: string;
  duration_ms?: number;
  started_at?: string;
  finished_at?: string;
}

export interface ActionAttempt {
  id: string;
  execution_id: string;
  cleanup_task_step_id: string;
  asset_id: string;
  action: string;
  status: string;
  idempotency_key: string;
  spec_bundle_revision: string;
  spec_hash: string;
  request?: Record<string, unknown>;
  preflight_evidence?: Record<string, unknown>;
  provider_request_id?: string;
  provider_operation_id?: string;
  provider_result?: Record<string, unknown>;
  readback?: Record<string, unknown>;
  provider_error?: {
    category: string;
    code?: string;
    message: string;
    request_id?: string;
    summary?: Record<string, unknown>;
  };
  deletion_check_started_at?: string;
  skip_reason?: string;
  failed_from?: string;
  resume_status?: string;
  created_at: string;
  updated_at: string;
  finished_at?: string;
}

export interface Job {
  id: string;
  connection_id: string;
  type: string;
  status: string;
  payload: Record<string, unknown>;
  run_at: string;
  attempts: number;
  last_error?: string;
  created_at: string;
  updated_at: string;
  finished_at?: string;
}

export interface JobLog {
  id: string;
  job_id: string;
  aggregate_type?: string;
  aggregate_id?: string;
  target_key?: string;
  retry_generation?: number;
  sequence: number;
  kind: "text" | "cloud_api_request" | "cloud_api_response";
  level: string;
  message: string;
  payload?: Record<string, unknown>;
  created_at: string;
}

export interface ScanLogPage {
  items: JobLog[];
  next_cursor?: string;
  live_cursor?: string;
}

export interface AuditEvent {
  id: string;
  connection_id: string;
  actor: string;
  action: string;
  target_type: string;
  target_id: string;
  result: string;
  evidence?: Record<string, unknown>;
  request_id?: string;
  created_at: string;
}
