// Synthetic inventory only. The UI that renders this data comes from Steward.
export const connectionID = 'demo-alicloud';
export const regionID = 'ap-southeast-1';
export const vpcID = 'vpc-demo-production';
export const initialPath = `/panorama/regions/${regionID}/vpcs/${vpcID}`;
const timestamp = '2026-09-06T08:30:00Z';
const cleanup = { selectable: true, potential_blockers: 0 };
const kindDefinitions = [
  ['ACS::VPC::VPC', 'VPC', '专有网络', 'network.vpc', 'acs-vpc-vpc.svg'],
  ['ACS::VPC::VSwitch', 'VSwitch', '交换机', 'network.subnet', 'acs-vpc-vswitch.svg'],
  ['ACS::ECS::Instance', 'ECS Instance', 'ECS 实例', 'compute.instance', 'acs-ecs-instance.svg'],
  ['ACS::SLB::LoadBalancer', 'CLB Instance', 'CLB 实例', 'network.load_balancer', 'acs-slb-loadbalancer.svg'],
  ['ACS::RDS::DBInstance', 'RDS Instance', 'RDS 实例', 'database.instance', 'acs-rds-dbinstance.png'],
  ['ACS::ECS::SecurityGroup', 'Security Group', '安全组', 'network.security_group', 'acs-ecs-securitygroup.png'],
];

export const kinds = kindDefinitions.map(([native_type, en, zh, resourceClass, icon]) => ({
  id: `alicloud:${native_type}`, provider: 'alicloud', native_type,
  display_name: en, display_names: { 'en-US': en, 'zh-CN': zh },
  class: resourceClass, icon: `/demo/icons/alicloud/${icon}`,
  capabilities: ['indexed', 'discovered', 'deletable'], scope_kinds: ['region'],
  bundle_revision: 'demo-1',
}));
export const catalog = [{
  provider: 'alicloud', revision: 'demo-1', hash: 'demo-1', kinds_revision: 'demo-1', kinds,
  specs: kinds.map(resource_kind => ({
    resource_kind, revision: 'demo-1', hash: resource_kind.id,
    definition: {
      metadata: { provider: 'alicloud', nativeType: resource_kind.native_type, class: resource_kind.class },
      scope: { kind: 'region' }, discovery: { source: 'product-api' }, actions: { delete: {} },
    },
  })),
}];

export const connection = {
  id: connectionID, name: 'Production · Demo', provider: 'alicloud', site: 'intl',
  partition: 'public', principal: 'demo-workspace', status: 'active',
  credential: { type: 'access_key', updated_at: timestamp },
  active_region_count: 1, retired_region_count: 0, excluded_region_count: 0,
  enabled_capabilities: ['indexed', 'discovered', 'deletable'],
  created_at: timestamp, updated_at: timestamp,
};
export const regions = [{
  id: 'demo-region', connection_id: connectionID, region_id: regionID,
  name: 'Singapore', origin: 'api', lifecycle: 'active', created_at: timestamp, updated_at: timestamp,
}];
export const scopes = [
  { id: 'scope-account', kind: 'account', name: 'Production · Demo', native_id: 'demo-account' },
  { id: 'scope-region', kind: 'region', name: 'Singapore', native_id: regionID, parent_id: 'scope-account', location: regionID },
].map(scope => ({ ...scope, connection_id: connectionID, created_at: timestamp, updated_at: timestamp }));

function asset(id, type, name, nativeID, properties = {}) {
  return {
    id, name, resource_kind_id: `alicloud:${type}`, scope_id: 'scope-region',
    identity: { provider: 'alicloud', partition: 'public', connection_id: connectionID, native_type: type, native_id: nativeID, scope_key: `region:${regionID}` },
    state: 'running', location: regionID, capabilities: ['indexed', 'discovered', 'deletable'],
    tags: { environment: 'production', project: 'storefront' },
    normalized: { vpc_id: vpcID, region_id: regionID, ...properties },
    first_seen_at: timestamp, last_seen_at: timestamp,
  };
}

export const initialAssets = [
  asset('demo-vpc', 'ACS::VPC::VPC', 'production', vpcID, { cidr_block: '10.0.0.0/16' }),
  asset('demo-app-subnet', 'ACS::VPC::VSwitch', 'application', 'vsw-demo-application', { cidr_block: '10.0.1.0/24', zone_id: 'ap-southeast-1a' }),
  asset('demo-data-subnet', 'ACS::VPC::VSwitch', 'data', 'vsw-demo-data', { cidr_block: '10.0.2.0/24', zone_id: 'ap-southeast-1b' }),
  asset('demo-gateway', 'ACS::SLB::LoadBalancer', 'public-gateway', 'lb-demo-gateway', { vswitch_id: 'vsw-demo-application', address: '203.0.113.24', address_type: 'internet', bandwidth: 100 }),
  asset('demo-api-01', 'ACS::ECS::Instance', 'api-01', 'i-demo-api01', { vswitch_id: 'vsw-demo-application', instance_type: 'ecs.g7.large', cpu: 2, memory: 8192, private_ip_addresses: ['10.0.1.11'], security_group_ids: ['sg-demo-application'] }),
  asset('demo-api-02', 'ACS::ECS::Instance', 'api-02', 'i-demo-api02', { vswitch_id: 'vsw-demo-application', instance_type: 'ecs.g7.large', cpu: 2, memory: 8192, private_ip_addresses: ['10.0.1.12'], security_group_ids: ['sg-demo-application'] }),
  asset('demo-postgres', 'ACS::RDS::DBInstance', 'postgres-primary', 'pg-demo-primary', { vswitch_id: 'vsw-demo-data', engine: 'PostgreSQL', engine_version: '16.0', db_instance_storage: 100, connection_string: 'postgres.internal.example' }),
  asset('demo-analytics', 'ACS::RDS::DBInstance', 'analytics', 'pg-demo-analytics', { vswitch_id: 'vsw-demo-data', engine: 'PostgreSQL', engine_version: '16.0', db_instance_storage: 50 }),
  asset('demo-security', 'ACS::ECS::SecurityGroup', 'application-policy', 'sg-demo-application', { description: 'Application access policy' }),
];

export const graphEdges = [
  ['demo-gateway', 'demo-api-01', 'routes_to'],
  ['demo-gateway', 'demo-api-02', 'routes_to'],
  ['demo-api-01', 'demo-security', 'uses'],
  ['demo-api-02', 'demo-security', 'uses'],
].map(([source_key, target_key, relation], index) => ({ key: `demo-edge-${index}`, source_key, target_key, relation, kind: 'relationship' }));

export function resourceFromAsset(value) {
  const kind = kinds.find(kind => kind.id === value.resource_kind_id);
  return {
    key: value.id, asset_id: value.id, name: value.name, native_id: value.identity.native_id,
    resource_kind_id: kind.id, type_name: kind.display_name, type_names: kind.display_names,
    class: kind.class, icon: kind.icon, state: value.state, dirty: value.dirty ?? false,
    domain: kind.class.startsWith('network') ? 'network' : kind.class.startsWith('database') ? 'storage' : 'compute',
    finding_count: 0, actionable: true, cleanup,
  };
}

export function topology(assets, params) {
  const count = assets.length;
  const region = { key: 'region:ap-southeast-1', name: 'Singapore', native_id: regionID };
  const vpc = { key: 'demo-vpc', asset_id: 'demo-vpc', name: 'production', native_id: vpcID };
  const focus = params.get('focus_key') || '';
  let view;
  if (!focus) {
    view = { kind: 'account', regions: [{ ...region, resource_count: count, cleanup }] };
  } else if (focus.startsWith('region:')) {
    view = { kind: 'region', region, public_resources: { key: 'public', name: 'Region public', resource_count: 0, cleanup }, vpcs: [{ ...vpc, resource_count: count, cleanup }] };
  } else if (focus === 'account-global' || focus.startsWith('region-public:')) {
    view = { kind: 'resource_graph', context: { key: focus, name: 'Region public', native_id: regionID }, resources: [], edges: [] };
  } else {
    const selectedKinds = params.getAll('resource_kind_id');
    const risk = params.get('risk');
    const query = params.get('resource_query')?.toLowerCase() ?? '';
    const filtered = assets.filter(value => !['demo-vpc', 'demo-app-subnet', 'demo-data-subnet'].includes(value.id))
      .filter(value => !selectedKinds.length || selectedKinds.includes(value.resource_kind_id))
      .filter(() => !risk || risk === 'actionable')
      .filter(value => !query || `${value.name} ${value.identity.native_id}`.toLowerCase().includes(query));
    const visible = new Set(filtered.map(value => value.id));
    view = {
      kind: 'vpc', region, vpc,
      public_resource_keys: filtered.filter(value => !value.normalized.vswitch_id).map(value => value.id),
      vswitches: [
        { key: 'demo-app-subnet', asset_id: 'demo-app-subnet', name: 'application', native_id: 'vsw-demo-application', zone: 'ap-southeast-1a' },
        { key: 'demo-data-subnet', asset_id: 'demo-data-subnet', name: 'data', native_id: 'vsw-demo-data', zone: 'ap-southeast-1b' },
      ].map(subnet => {
        const resource_keys = filtered.filter(value => value.normalized.vswitch_id === subnet.native_id).map(value => value.id);
        return { ...subnet, resource_keys, resource_count: resource_keys.length };
      }),
      resources: filtered.map(resourceFromAsset),
      edges: graphEdges.filter(edge => visible.has(edge.source_key) && visible.has(edge.target_key)),
    };
  }
  return { view, revision: { inventory: 'demo-1', graph: 'demo-1', spec_bundle: 'demo-1', projected_at: timestamp }, coverage: { status: 'complete', failed_shards: 0 }, warnings: [], truncated: false };
}

export const scan = {
  id: 'demo-scan', connection_id: connectionID, status: 'succeeded', scope_mode: 'selected_regions',
  requested_by: 'demo-workspace', retry_generation: 0, retry_count: 0, control_version: 1,
  targets: [{ kind: 'region', region_id: regionID }], resource_kind_ids: kinds.map(kind => kind.id),
  created_at: timestamp, started_at: timestamp, finished_at: '2026-09-06T08:30:12Z', duration_ms: 12000,
  target_progress: [{ kind: 'region', region_id: regionID, status: 'succeeded', completed: 6, total: 6, resource_count: initialAssets.length, error_count: 0, summary: '' }],
  progress: { completed: 6, total: 6, running: 0, failed: 0, resource_count: initialAssets.length }, allowed_actions: [],
};

export const audits = [{
  id: 'demo-audit', connection_id: connectionID, actor: 'demo-workspace', action: 'scan.create',
  target_type: 'scan_task', target_id: scan.id, result: 'succeeded', created_at: timestamp,
}];

// A saved review, not an executable cleanup. The selected vSwitch still has
// resources outside the cleanup selection, which the original UI flags.
export const cleanupReview = {
  task: {
    id: 'demo-cleanup', connection_id: connectionID, status: 'draft',
    selectors: ['demo-api-01', 'demo-gateway', 'demo-app-subnet'].map(asset_id => ({ kind: 'asset', asset_id })),
    resolved_asset_ids: ['demo-api-01', 'demo-gateway', 'demo-app-subnet'],
    selector_asset_ids: [['demo-api-01'], ['demo-gateway'], ['demo-app-subnet']],
    revision: { inventory_revision: 'demo-1', graph_revision: 'demo-1', spec_bundle_revision: 'demo-1', spec_hash: 'demo-1' },
    scan_coverage: { status: 'complete' }, snapshot_hash: 'demo-review',
    blockers: [{ code: 'cross_scope_dependency', asset_id: 'demo-app-subnet', message: 'api-02 still depends on this vSwitch.', evidence: { dependent_asset_id: 'demo-api-02', relationship_type: 'member_of' } }],
    warnings: [], created_by: 'demo-workspace', created_at: timestamp,
  },
  steps: ['demo-api-01', 'demo-gateway'].map(asset_id => ({ id: `step-${asset_id}`, cleanup_task_id: 'demo-cleanup', asset_id, kind: 'direct', action: 'delete', depends_on: [] })),
  impact_items: [],
};
