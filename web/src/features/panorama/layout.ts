import type {
  ResourceGraphTopologyView,
  TopologyResource,
  TopologyResourceEdge,
  TopologyVSwitch,
  VPCTopologyView,
} from "@/api/types";

export interface TopologyLayoutPoint {
  x: number;
  y: number;
}

export interface TopologyLayoutSize {
  width: number;
  height: number;
}

interface TopologyLayoutNodeBase {
  key: string;
  position: TopologyLayoutPoint;
  size: TopologyLayoutSize;
  parentKey?: string;
}

export interface TopologyResourceLayoutNode extends TopologyLayoutNodeBase {
  kind: "resource";
  resource: TopologyResource;
  band: number;
}

export interface TopologyVSwitchLayoutNode extends TopologyLayoutNodeBase {
  kind: "vswitch";
  vSwitch: TopologyVSwitch;
}

export interface TopologyStackLayoutNode extends TopologyLayoutNodeBase {
  kind: "stack";
  resourceKindID: string;
  typeName: string;
  typeNames?: Record<string, string>;
  icon?: string;
  count: number;
  memberKeys: string[];
  band: number;
}

export interface TopologyExpandedStackLayoutNode extends TopologyLayoutNodeBase {
  kind: "expandedStack";
  resourceKindID: string;
  typeName: string;
  typeNames?: Record<string, string>;
  icon?: string;
  count: number;
  memberKeys: string[];
  band: number;
}

export interface TopologyResourceGroupTypeSummary {
  resourceKindID: string;
  typeName: string;
  typeNames?: Record<string, string>;
  count: number;
}

interface TopologyResourceGroupLayoutNodeBase extends TopologyLayoutNodeBase {
  resource: TopologyResource;
  childKeys: string[];
  directChildCount: number;
  childTypeSummaries: TopologyResourceGroupTypeSummary[];
  childFindingCount: number;
  band: number;
}

export interface TopologyResourceGroupLayoutNode extends TopologyResourceGroupLayoutNodeBase {
  kind: "resourceGroup";
}

export interface TopologyExpandedResourceGroupLayoutNode extends TopologyResourceGroupLayoutNodeBase {
  kind: "expandedResourceGroup";
}

export interface TopologyVSwitchStackLayoutNode extends TopologyLayoutNodeBase {
  kind: "vSwitchStack";
  count: number;
  vSwitches: TopologyVSwitch[];
}

export interface TopologyExpandedVSwitchStackLayoutNode extends TopologyLayoutNodeBase {
  kind: "expandedVSwitchStack";
  count: number;
  vSwitches: TopologyVSwitch[];
}

export type TopologyLayoutNode =
  | TopologyResourceLayoutNode
  | TopologyVSwitchLayoutNode
  | TopologyStackLayoutNode
  | TopologyExpandedStackLayoutNode
  | TopologyResourceGroupLayoutNode
  | TopologyExpandedResourceGroupLayoutNode
  | TopologyVSwitchStackLayoutNode
  | TopologyExpandedVSwitchStackLayoutNode;

export interface TopologyLayoutDiagnostics {
  resourceVisits: number;
  membershipVisits: number;
  edgeVisits: number;
  groupingVisits: number;
  placementVisits: number;
  materializationVisits: number;
  gridVisits: number;
  sortComparisons: number;
}

export interface TopologyLayout {
  nodes: TopologyLayoutNode[];
  edges: readonly TopologyResourceEdge[];
  size: TopologyLayoutSize;
  diagnostics: TopologyLayoutDiagnostics;
}

export interface TopologyEdgeHandles {
  sourceHandle: string;
  targetHandle: string;
}

interface StackGroup {
  key: string;
  displayDomainKey: string;
  resourceKindID: string;
  typeName: string;
  typeNames?: Record<string, string>;
  icon?: string;
  members: TopologyResource[];
  band: number;
  componentKey: string;
}

interface ResourceGroup {
  parent: TopologyResource;
  children: TopologyResource[];
  descendantKeys: string[];
  childTypeSummaries: TopologyResourceGroupTypeSummary[];
  childFindingCount: number;
}

interface ResourceGroupChildLayout {
  resource: TopologyResource;
  position: TopologyLayoutPoint;
  size: TopologyLayoutSize;
  groupPlan?: ResourceGroupLayoutPlan;
}

interface ResourceGroupLayoutPlan {
  group: ResourceGroup;
  expanded: boolean;
  size: TopologyLayoutSize;
  children: ResourceGroupChildLayout[];
}

interface LayoutItem {
  key: string;
  band: number;
  componentKey: string;
  resourceKindID: string;
  name: string;
  size: TopologyLayoutSize;
  expandedColumns?: number;
  resource?: TopologyResource;
  stack?: StackGroup;
  resourceGroup?: ResourceGroup;
  resourceGroupPlan?: ResourceGroupLayoutPlan;
  expanded: boolean;
}

interface PlacedItem {
  item: LayoutItem;
  position: TopologyLayoutPoint;
}

interface ItemLayout {
  items: PlacedItem[];
  size: TopologyLayoutSize;
}

interface BuildIndex {
  resources: TopologyResource[];
  resourceByKey: Map<string, TopologyResource>;
  degreeByKey: Map<string, number>;
  componentByKey: Map<string, string>;
  resourceToVSwitch: Map<string, string>;
  stackByResource: Map<string, StackGroup>;
  connectedStackByResource: Map<string, StackGroup>;
  resourceGroupByParent: Map<string, ResourceGroup>;
  resourceGroupByChild: Map<string, ResourceGroup>;
  diagnostics: TopologyLayoutDiagnostics;
}

const RESOURCE_SIZE = { width: 168, height: 60 };
const STACK_SIZE = { width: 168, height: 60 };
const RESOURCE_GROUP_SIZE = { width: 196, height: 88 };
const ITEM_GAP_X = 28;
const ITEM_GAP_Y = 28;
const BAND_GAP_Y = 36;
const LAYOUT_TARGET_ASPECT = 2.7;
const MIN_ROW_ITEM_COUNT = 4;
const VPC_PUBLIC_MIN_ROW_ITEM_COUNT = 3;
const VSWITCH_PADDING_X = 28;
const VSWITCH_PADDING_BOTTOM = 28;
const VSWITCH_HEADER_HEIGHT = 56;
const VSWITCH_MIN_SIZE = { width: 320, height: 208 };
const VSWITCH_GRID_GAP_X = 72;
const VSWITCH_GRID_GAP_Y = 72;
const PUBLIC_TO_VSWITCH_GAP = 88;
const EXPANDED_PADDING_X = 24;
const EXPANDED_PADDING_BOTTOM = 24;
const EXPANDED_STACK_LABEL_HEIGHT = 40;
const EXPANDED_RESOURCE_GROUP_HEADER_HEIGHT = EXPANDED_STACK_LABEL_HEIGHT;
const EXPANDED_VSWITCH_STACK_HEADER_HEIGHT = 40;
const EXPANDED_VSWITCH_STACK_CONTENT_INSET = 48;
const EXPANDED_VSWITCH_STACK_CONTENT_PADDING_Y = 24;
const EXPANDED_VSWITCH_STACK_GAP_X = 20;
const EXPANDED_VSWITCH_STACK_GAP_Y = 20;
const EXPANDED_GAP = 20;
const VSWITCH_STACK_SIZE = STACK_SIZE;
const VSWITCH_STACK_MEMBER_SIZE = { width: 240, height: 72 };
const VSWITCH_STACK_COLUMNS = 4;
const DEFAULT_VIEWPORT = { width: 1200, height: 800 };
const MAX_FIT_ZOOM = 1.8;

interface TopologyLayoutOptions {
  complete: boolean;
  viewport?: TopologyLayoutSize;
}

export function buildTopologyLayout(
  view: ResourceGraphTopologyView | VPCTopologyView,
  expandedStackKeys: ReadonlySet<string>,
  options: TopologyLayoutOptions,
): TopologyLayout {
  const diagnostics: TopologyLayoutDiagnostics = {
    resourceVisits: 0,
    membershipVisits: 0,
    edgeVisits: 0,
    groupingVisits: 0,
    placementVisits: 0,
    materializationVisits: 0,
    gridVisits: 0,
    sortComparisons: 0,
  };
  const index = buildIndex(view, options.complete, diagnostics);
  const viewport = validViewport(options.viewport);

  if (view.kind === "resource_graph") {
    const items = buildItems(
      index.resources,
      index,
      expandedStackKeys,
      viewport,
      diagnostics,
    );
    const placed = placeItems(items, 0, 0, diagnostics);
    return {
      nodes: materializeItems(placed.items, diagnostics),
      edges: displayEdges(
        view.edges,
        index.resourceByKey,
        diagnostics,
        index.resourceGroupByChild,
        index.connectedStackByResource,
        expandedStackKeys,
      ),
      size: placed.size,
      diagnostics,
    };
  }

  return buildVPCLayout(
    view,
    index,
    expandedStackKeys,
    options.complete,
    viewport,
    diagnostics,
  );
}

function buildIndex(
  view: ResourceGraphTopologyView | VPCTopologyView,
  complete: boolean,
  diagnostics: TopologyLayoutDiagnostics,
): BuildIndex {
  const resources = [...view.resources];
  const resourceByKey = new Map<string, TopologyResource>();
  for (const resource of resources) {
    diagnostics.resourceVisits += 1;
    resourceByKey.set(resource.key, resource);
  }

  const degreeByKey = new Map<string, number>();
  const boundSnapshotKeys = new Set<string>();
  const adjacency = new Map<string, string[]>();
  for (const resource of resources) {
    diagnostics.resourceVisits += 1;
    degreeByKey.set(resource.key, 0);
    adjacency.set(resource.key, []);
    if (
      isSnapshotResource(resource) &&
      resource.external_relations?.some((relation) =>
        isSnapshotBindingTarget(
          relation.target_class,
          relation.target_resource_kind_id,
        ),
      )
    ) {
      boundSnapshotKeys.add(resource.key);
    }
  }
  for (const edge of view.edges) {
    diagnostics.edgeVisits += 1;
    if (
      !resourceByKey.has(edge.source_key) ||
      !resourceByKey.has(edge.target_key)
    ) {
      continue;
    }
    degreeByKey.set(
      edge.source_key,
      (degreeByKey.get(edge.source_key) ?? 0) + 1,
    );
    degreeByKey.set(
      edge.target_key,
      (degreeByKey.get(edge.target_key) ?? 0) + 1,
    );
    adjacency.get(edge.source_key)?.push(edge.target_key);
    adjacency.get(edge.target_key)?.push(edge.source_key);
    const source = resourceByKey.get(edge.source_key);
    const target = resourceByKey.get(edge.target_key);
    if (source && target) {
      if (isSnapshotResource(source) && isSnapshotBindingResource(target)) {
        boundSnapshotKeys.add(source.key);
      }
      if (isSnapshotResource(target) && isSnapshotBindingResource(source)) {
        boundSnapshotKeys.add(target.key);
      }
    }
  }

  const componentByKey = connectedComponents(resources, adjacency, diagnostics);
  const resourceToVSwitch = new Map<string, string>();
  if (view.kind === "vpc") {
    const publicKeys = new Set(view.public_resource_keys);
    for (const vSwitch of stableVSwitches(view.vswitches, diagnostics)) {
      for (const resourceKey of vSwitch.resource_keys) {
        if (
          publicKeys.has(resourceKey) ||
          !resourceByKey.has(resourceKey) ||
          resourceToVSwitch.has(resourceKey)
        ) {
          continue;
        }
        diagnostics.membershipVisits += 1;
        resourceToVSwitch.set(resourceKey, vSwitch.key);
      }
    }
  }

  const { resourceGroupByParent, resourceGroupByChild } =
    complete && view.kind === "resource_graph"
      ? resourceGroups(view, resources, resourceByKey, diagnostics)
      : {
          resourceGroupByParent: new Map<string, ResourceGroup>(),
          resourceGroupByChild: new Map<string, ResourceGroup>(),
        };
  const stackByResource = complete
    ? isolatedStackGroups(
        view,
        resources,
        resourceToVSwitch,
        degreeByKey,
        boundSnapshotKeys,
        componentByKey,
        resourceGroupByParent,
        resourceGroupByChild,
        diagnostics,
      )
    : new Map<string, StackGroup>();
  const connectedStackByResource = new Map<string, StackGroup>();
  for (const [resourceKey, stack] of stackByResource) {
    diagnostics.groupingVisits += 1;
    if ((degreeByKey.get(resourceKey) ?? 0) > 0) {
      connectedStackByResource.set(resourceKey, stack);
    }
  }

  return {
    resources,
    resourceByKey,
    degreeByKey,
    componentByKey,
    resourceToVSwitch,
    stackByResource,
    connectedStackByResource,
    resourceGroupByParent,
    resourceGroupByChild,
    diagnostics,
  };
}

function resourceGroups(
  view: ResourceGraphTopologyView,
  resources: readonly TopologyResource[],
  resourceByKey: ReadonlyMap<string, TopologyResource>,
  diagnostics: TopologyLayoutDiagnostics,
): {
  resourceGroupByParent: Map<string, ResourceGroup>;
  resourceGroupByChild: Map<string, ResourceGroup>;
} {
  const containerKeysByChild = new Map<string, Set<string>>();
  for (const edge of view.edges) {
    diagnostics.edgeVisits += 1;
    const source = resourceByKey.get(edge.source_key);
    const target = resourceByKey.get(edge.target_key);
    if (
      edge.kind !== "relationship" ||
      edge.source_key === edge.target_key ||
      !source ||
      !target ||
      !isParentChildRelationship(edge, source, target)
    ) {
      continue;
    }
    const containerKeys = containerKeysByChild.get(edge.source_key);
    if (containerKeys) {
      containerKeys.add(edge.target_key);
    } else {
      containerKeysByChild.set(edge.source_key, new Set([edge.target_key]));
    }
  }

  const containerKeyByChild = new Map<string, string>();
  for (const [childKey, containerKeys] of containerKeysByChild) {
    diagnostics.groupingVisits += 1;
    const parentKey = mostSpecificContainerKey(
      childKey,
      containerKeys,
      containerKeysByChild,
      diagnostics,
    );
    if (parentKey) containerKeyByChild.set(childKey, parentKey);
  }
  const cyclicKeys = new Set<string>();
  for (const start of containerKeyByChild.keys()) {
    const path: string[] = [];
    const pathIndex = new Map<string, number>();
    let current: string | undefined = start;
    while (current && containerKeyByChild.has(current)) {
      diagnostics.groupingVisits += 1;
      const cycleStart = pathIndex.get(current);
      if (cycleStart !== undefined) {
        for (const key of path.slice(cycleStart)) cyclicKeys.add(key);
        break;
      }
      pathIndex.set(current, path.length);
      path.push(current);
      current = containerKeyByChild.get(current);
    }
  }
  for (const key of cyclicKeys) containerKeyByChild.delete(key);

  const childrenByParent = new Map<string, TopologyResource[]>();
  for (const resource of resources) {
    diagnostics.groupingVisits += 1;
    const parentKey = containerKeyByChild.get(resource.key);
    if (!parentKey) continue;
    const children = childrenByParent.get(parentKey);
    if (children) {
      children.push(resource);
    } else {
      childrenByParent.set(parentKey, [resource]);
    }
  }

  const resourceGroupByParent = new Map<string, ResourceGroup>();
  const resourceGroupByChild = new Map<string, ResourceGroup>();
  for (const [parentKey, unsortedChildren] of childrenByParent) {
    diagnostics.groupingVisits += 1;
    if (unsortedChildren.length < 2 && !containerKeyByChild.has(parentKey)) {
      continue;
    }
    const parent = resourceByKey.get(parentKey);
    if (!parent) continue;
    const children = [...unsortedChildren].sort(
      countedComparator(diagnostics, compareResources),
    );
    const typeSummaryByKind = new Map<
      string,
      TopologyResourceGroupTypeSummary
    >();
    let childFindingCount = 0;
    for (const child of children) {
      diagnostics.groupingVisits += 1;
      childFindingCount += child.finding_count;
      const existing = typeSummaryByKind.get(child.resource_kind_id);
      if (existing) {
        existing.count += 1;
      } else {
        typeSummaryByKind.set(child.resource_kind_id, {
          resourceKindID: child.resource_kind_id,
          typeName: child.type_name,
          typeNames: child.type_names,
          count: 1,
        });
      }
    }
    const childTypeSummaries = [...typeSummaryByKind.values()].sort(
      countedComparator(
        diagnostics,
        (left, right) =>
          right.count - left.count ||
          compareText(left.resourceKindID, right.resourceKindID),
      ),
    );
    const group: ResourceGroup = {
      parent,
      children,
      descendantKeys: [],
      childTypeSummaries,
      childFindingCount,
    };
    resourceGroupByParent.set(parent.key, group);
    for (const child of children) {
      resourceGroupByChild.set(child.key, group);
    }
  }

  const descendantKeysByParent = new Map<string, string[]>();
  const collectDescendantKeys = (
    group: ResourceGroup,
    ancestors: ReadonlySet<string>,
  ): string[] => {
    const cached = descendantKeysByParent.get(group.parent.key);
    if (cached) return cached;
    if (ancestors.has(group.parent.key)) return [];
    const nextAncestors = new Set(ancestors);
    nextAncestors.add(group.parent.key);
    const descendants: string[] = [];
    for (const child of group.children) {
      diagnostics.groupingVisits += 1;
      descendants.push(child.key);
      const nested = resourceGroupByParent.get(child.key);
      if (nested) {
        descendants.push(...collectDescendantKeys(nested, nextAncestors));
      }
    }
    const unique = [...new Set(descendants)];
    descendantKeysByParent.set(group.parent.key, unique);
    return unique;
  };
  for (const group of resourceGroupByParent.values()) {
    group.descendantKeys = collectDescendantKeys(group, new Set());
  }
  return { resourceGroupByParent, resourceGroupByChild };
}

function isParentChildRelationship(
  edge: TopologyResourceEdge,
  source: TopologyResource,
  target: TopologyResource,
): boolean {
  if (edge.relation === "member_of") return true;
  return (
    edge.relation === "uses" &&
    source.class === "network.vpn_connection" &&
    target.class === "network.customer_gateway"
  );
}

function mostSpecificContainerKey(
  childKey: string,
  containerKeys: ReadonlySet<string>,
  containerKeysByChild: ReadonlyMap<string, ReadonlySet<string>>,
  diagnostics: TopologyLayoutDiagnostics,
): string | undefined {
  const candidates = [...containerKeys].filter((key) => key !== childKey);
  if (candidates.length === 1) return candidates[0];
  const reaches = (start: string, target: string): boolean => {
    const pending = [start];
    const visited = new Set<string>();
    while (pending.length > 0) {
      const current = pending.pop();
      if (!current || visited.has(current)) continue;
      visited.add(current);
      for (const parent of containerKeysByChild.get(current) ?? []) {
        diagnostics.groupingVisits += 1;
        if (parent === target) return true;
        if (!visited.has(parent)) pending.push(parent);
      }
    }
    return false;
  };
  const mostSpecific = candidates.filter((candidate) =>
    candidates.every(
      (other) => other === candidate || reaches(candidate, other),
    ),
  );
  return mostSpecific.length === 1 ? mostSpecific[0] : undefined;
}

function connectedComponents(
  resources: readonly TopologyResource[],
  adjacency: ReadonlyMap<string, readonly string[]>,
  diagnostics: TopologyLayoutDiagnostics,
): Map<string, string> {
  const componentByKey = new Map<string, string>();
  const sortedKeys: string[] = [];
  for (const resource of resources) {
    diagnostics.resourceVisits += 1;
    sortedKeys.push(resource.key);
  }
  sortedKeys.sort(countedComparator(diagnostics, compareText));

  for (const startKey of sortedKeys) {
    if (componentByKey.has(startKey)) continue;
    const pending = [startKey];
    const members: string[] = [];
    let minimumKey = startKey;
    componentByKey.set(startKey, startKey);

    while (pending.length > 0) {
      const key = pending.pop();
      if (!key) continue;
      diagnostics.resourceVisits += 1;
      members.push(key);
      if (compareText(key, minimumKey) < 0) minimumKey = key;
      for (const neighborKey of adjacency.get(key) ?? []) {
        diagnostics.edgeVisits += 1;
        if (componentByKey.has(neighborKey)) continue;
        componentByKey.set(neighborKey, startKey);
        pending.push(neighborKey);
      }
    }

    for (const key of members) componentByKey.set(key, minimumKey);
  }
  return componentByKey;
}

function isolatedStackGroups(
  view: ResourceGraphTopologyView | VPCTopologyView,
  resources: readonly TopologyResource[],
  resourceToVSwitch: ReadonlyMap<string, string>,
  degreeByKey: ReadonlyMap<string, number>,
  boundSnapshotKeys: ReadonlySet<string>,
  componentByKey: ReadonlyMap<string, string>,
  resourceGroupByParent: ReadonlyMap<string, ResourceGroup>,
  resourceGroupByChild: ReadonlyMap<string, ResourceGroup>,
  diagnostics: TopologyLayoutDiagnostics,
): Map<string, StackGroup> {
  const candidates = new Map<string, TopologyResource[]>();
  for (const resource of resources) {
    diagnostics.resourceVisits += 1;
    diagnostics.groupingVisits += 1;
    const independentSnapshot =
      isSnapshotResource(resource) && !boundSnapshotKeys.has(resource.key);
    if (
      resource.membership_unknown ||
      resourceGroupByParent.has(resource.key) ||
      resourceGroupByChild.has(resource.key) ||
      (!independentSnapshot &&
        ((degreeByKey.get(resource.key) ?? 0) !== 0 ||
          (resource.external_relations?.length ?? 0) !== 0))
    ) {
      continue;
    }
    const displayDomainKey = stackDisplayDomainKey(
      view,
      resource.key,
      resourceToVSwitch,
    );
    const groupKey = `${displayDomainKey}\u0000${resource.resource_kind_id}`;
    const values = candidates.get(groupKey);
    if (values) {
      values.push(resource);
    } else {
      candidates.set(groupKey, [resource]);
    }
  }

  const stackByResource = new Map<string, StackGroup>();
  for (const members of candidates.values()) {
    diagnostics.groupingVisits += 1;
    if (members.length < 2) continue;
    members.sort(
      countedComparator(diagnostics, (left, right) =>
        compareLayoutResources(left, right, componentByKey),
      ),
    );
    const first = members[0];
    if (!first) continue;
    const displayDomainKey = stackDisplayDomainKey(
      view,
      first.key,
      resourceToVSwitch,
    );
    let minimumMemberKey = first.key;
    for (const member of members) {
      diagnostics.groupingVisits += 1;
      if (compareText(member.key, minimumMemberKey) < 0) {
        minimumMemberKey = member.key;
      }
    }
    const group: StackGroup = {
      key: stackLayoutKey(displayDomainKey, first.resource_kind_id),
      displayDomainKey,
      resourceKindID: first.resource_kind_id,
      typeName: first.type_name.trim() || first.resource_kind_id,
      typeNames: first.type_names,
      icon: first.icon,
      members,
      band: resourceBand(first),
      componentKey: minimumMemberKey,
    };
    for (const member of members) {
      diagnostics.groupingVisits += 1;
      stackByResource.set(member.key, group);
    }
  }
  return stackByResource;
}

function isSnapshotResource(resource: TopologyResource): boolean {
  return (
    normalizedResourceClass(resource.class) === "storage.snapshot" ||
    matchesNativeResourceKind(resource.resource_kind_id, "acs::ecs::snapshot")
  );
}

function isSnapshotBindingResource(resource: TopologyResource): boolean {
  return isSnapshotBindingTarget(resource.class, resource.resource_kind_id);
}

function isSnapshotBindingTarget(
  className: string | undefined,
  resourceKindID: string | undefined,
): boolean {
  const normalizedClass = normalizedResourceClass(className);
  if (
    normalizedClass === "compute.image" ||
    normalizedClass === "storage.block"
  ) {
    return true;
  }
  return (
    matchesNativeResourceKind(resourceKindID, "acs::ecs::image") ||
    matchesNativeResourceKind(resourceKindID, "acs::ecs::disk")
  );
}

function normalizedResourceClass(value: string | undefined): string {
  return value?.trim().toLowerCase() ?? "";
}

function matchesNativeResourceKind(
  resourceKindID: string | undefined,
  nativeType: string,
): boolean {
  const normalizedKindID = resourceKindID?.trim().toLowerCase() ?? "";
  return (
    normalizedKindID === nativeType ||
    normalizedKindID.endsWith(`:${nativeType}`)
  );
}

function stackDisplayDomainKey(
  view: ResourceGraphTopologyView | VPCTopologyView,
  resourceKey: string,
  resourceToVSwitch: ReadonlyMap<string, string>,
): string {
  if (view.kind === "resource_graph") {
    return `region-public:${view.context.key}`;
  }
  const vSwitchKey = resourceToVSwitch.get(resourceKey);
  return vSwitchKey ? `vswitch:${vSwitchKey}` : `vpc-public:${view.vpc.key}`;
}

function stackLayoutKey(
  displayDomainKey: string,
  resourceKindID: string,
): string {
  return `stack:${encodeURIComponent(displayDomainKey)}:${encodeURIComponent(resourceKindID)}`;
}

function buildVPCLayout(
  view: VPCTopologyView,
  index: BuildIndex,
  expandedStackKeys: ReadonlySet<string>,
  complete: boolean,
  viewport: TopologyLayoutSize,
  diagnostics: TopologyLayoutDiagnostics,
): TopologyLayout {
  const publicResources: TopologyResource[] = [];
  const resourcesByVSwitch = new Map<string, TopologyResource[]>();
  for (const resource of index.resources) {
    diagnostics.resourceVisits += 1;
    const vSwitchKey = index.resourceToVSwitch.get(resource.key);
    if (!vSwitchKey) {
      publicResources.push(resource);
      continue;
    }
    const members = resourcesByVSwitch.get(vSwitchKey);
    if (members) {
      members.push(resource);
    } else {
      resourcesByVSwitch.set(vSwitchKey, [resource]);
    }
  }

  const publicItems = buildItems(
    publicResources,
    index,
    expandedStackKeys,
    viewport,
    diagnostics,
  );
  const publicLayout = placeItems(
    publicItems,
    0,
    0,
    diagnostics,
    LAYOUT_TARGET_ASPECT,
    VPC_PUBLIC_MIN_ROW_ITEM_COUNT,
    compareVPCPublicItems,
  );
  centerItemRows(publicLayout, 0);
  const nodes = materializeItems(publicLayout.items, diagnostics);

  const stableVSwitchesForLayout = stableVSwitches(view.vswitches, diagnostics);
  const emptyVSwitches = stableVSwitchesForLayout
    .filter(
      (vSwitch) =>
        vSwitch.resource_count === 0 && vSwitch.resource_keys.length === 0,
    )
    .sort(
      countedComparator(
        diagnostics,
        (left, right) =>
          compareText(left.name, right.name) ||
          compareText(left.key, right.key),
      ),
    );
  const hasVSwitchStack = complete && emptyVSwitches.length >= 2;
  const visibleVSwitches = hasVSwitchStack
    ? stableVSwitchesForLayout.filter(
        (vSwitch) =>
          vSwitch.resource_count !== 0 || vSwitch.resource_keys.length !== 0,
      )
    : stableVSwitchesForLayout;
  const vSwitchLayouts: VSwitchGridLayout[] = visibleVSwitches.map(
    (vSwitch) => {
      diagnostics.gridVisits += 1;
      const items = buildItems(
        resourcesByVSwitch.get(vSwitch.key) ?? [],
        index,
        expandedStackKeys,
        viewport,
        diagnostics,
      );
      const content = placeItems(
        items,
        VSWITCH_PADDING_X,
        VSWITCH_HEADER_HEIGHT,
        diagnostics,
      );
      const size = vSwitchSize(content.size);
      centerVSwitchContent(content, size.width);
      return { kind: "vswitch", vSwitch, content, size };
    },
  );
  if (hasVSwitchStack) {
    const expanded = expandedStackKeys.has(`vswitch-stack:${view.vpc.key}`);
    vSwitchLayouts.push({
      kind: "vSwitchStack",
      key: `vswitch-stack:${view.vpc.key}`,
      vSwitches: emptyVSwitches,
      size: expanded
        ? expandedVSwitchStackSize(emptyVSwitches.length)
        : VSWITCH_STACK_SIZE,
      expanded,
    });
  }

  const gridTop =
    publicLayout.items.length > 0
      ? publicLayout.size.height + PUBLIC_TO_VSWITCH_GAP
      : 0;
  const placementLayouts = vSwitchLayouts.map((layout) =>
    layout.kind === "vSwitchStack" && layout.expanded
      ? { ...layout, size: VSWITCH_STACK_SIZE }
      : layout,
  );
  const columns = vSwitchColumnCount(
    placementLayouts,
    Math.max(publicLayout.size.width, viewport.width),
  );
  const {
    columnWidths,
    rowHeights,
    width: gridWidth,
  } = vSwitchGridTracks(vSwitchLayouts, columns);
  diagnostics.gridVisits += vSwitchLayouts.length;
  const columnOffsets = cumulativeOffsets(
    columnWidths,
    VSWITCH_GRID_GAP_X,
    diagnostics,
  );
  const rowOffsets = cumulativeOffsets(
    rowHeights,
    VSWITCH_GRID_GAP_Y,
    diagnostics,
  );

  const width = Math.max(publicLayout.size.width, gridWidth);
  const gridOffsetX = Math.max(0, (width - gridWidth) / 2);
  const publicOffsetX = publicLayoutOffsetX(
    publicLayout.size.width,
    width,
    vSwitchLayouts,
    columns,
    columnOffsets,
    gridOffsetX,
  );
  for (const node of nodes) {
    node.position.x += publicOffsetX;
  }
  let height = publicLayout.size.height;
  for (let index = 0; index < vSwitchLayouts.length; index += 1) {
    diagnostics.gridVisits += 1;
    const layout = vSwitchLayouts[index];
    if (!layout) continue;
    const column = index % columns;
    const row = Math.floor(index / columns);
    const position = {
      x: gridOffsetX + (columnOffsets[column] ?? 0),
      y: gridTop + (rowOffsets[row] ?? 0),
    };
    if (layout.kind === "vswitch") {
      nodes.push({
        kind: "vswitch",
        key: layout.vSwitch.key,
        vSwitch: layout.vSwitch,
        position,
        size: layout.size,
      });
      nodes.push(
        ...materializeItems(
          layout.content.items,
          diagnostics,
          layout.vSwitch.key,
        ),
      );
    } else {
      const common = {
        key: layout.key,
        count: layout.vSwitches.length,
        vSwitches: layout.vSwitches,
        position,
        size: layout.size,
      };
      nodes.push(
        layout.expanded
          ? { kind: "expandedVSwitchStack", ...common }
          : { kind: "vSwitchStack", ...common },
      );
    }
    height = Math.max(height, position.y + layout.size.height);
  }

  return {
    nodes,
    edges: displayEdges(
      view.edges,
      index.resourceByKey,
      diagnostics,
      new Map(),
      index.connectedStackByResource,
      expandedStackKeys,
    ),
    size: { width, height },
    diagnostics,
  };
}

type VSwitchGridLayout =
  | {
      kind: "vswitch";
      vSwitch: TopologyVSwitch;
      content: ItemLayout;
      size: TopologyLayoutSize;
    }
  | {
      kind: "vSwitchStack";
      key: string;
      vSwitches: TopologyVSwitch[];
      size: TopologyLayoutSize;
      expanded: boolean;
    };

function buildItems(
  resources: readonly TopologyResource[],
  index: BuildIndex,
  expandedStackKeys: ReadonlySet<string>,
  viewport: TopologyLayoutSize,
  diagnostics: TopologyLayoutDiagnostics,
): LayoutItem[] {
  const items: LayoutItem[] = [];
  const emittedStacks = new Set<string>();
  const sortedResources = [...resources].sort(
    countedComparator(diagnostics, compareResources),
  );

  for (const resource of sortedResources) {
    diagnostics.resourceVisits += 1;
    diagnostics.groupingVisits += 1;
    if (index.resourceGroupByChild.has(resource.key)) continue;
    const resourceGroup = index.resourceGroupByParent.get(resource.key);
    if (resourceGroup) {
      const resourceGroupPlan = buildResourceGroupLayoutPlan(
        resourceGroup,
        index.resourceGroupByParent,
        expandedStackKeys,
        viewport,
        diagnostics,
      );
      items.push({
        key: resource.key,
        band: resourceBand(resource),
        componentKey: index.componentByKey.get(resource.key) ?? resource.key,
        resourceKindID: resource.resource_kind_id,
        name: resource.name,
        size: resourceGroupPlan.size,
        resourceGroup,
        resourceGroupPlan,
        expanded: resourceGroupPlan.expanded,
      });
      continue;
    }
    const stack = index.stackByResource.get(resource.key);
    if (!stack) {
      items.push({
        key: resource.key,
        band: resourceBand(resource),
        componentKey: index.componentByKey.get(resource.key) ?? resource.key,
        resourceKindID: resource.resource_kind_id,
        name: resource.name,
        size: RESOURCE_SIZE,
        resource,
        expanded: false,
      });
      continue;
    }
    if (emittedStacks.has(stack.key)) continue;
    emittedStacks.add(stack.key);
    const expanded = expandedStackKeys.has(stack.key);
    const expandedColumns = expanded
      ? expandedStackColumnCount(stack.members.length, viewport)
      : undefined;
    items.push({
      key: stack.key,
      band: stack.band,
      componentKey: stack.componentKey,
      resourceKindID: stack.resourceKindID,
      name: stack.members[0]?.name ?? stack.key,
      size: expanded
        ? expandedStackSize(stack.members.length, expandedColumns)
        : STACK_SIZE,
      expandedColumns,
      stack,
      expanded,
    });
  }
  return items.sort(countedComparator(diagnostics, compareItems));
}

function placeItems(
  items: readonly LayoutItem[],
  offsetX: number,
  offsetY: number,
  diagnostics: TopologyLayoutDiagnostics,
  targetAspect = LAYOUT_TARGET_ASPECT,
  minRowItemCount = MIN_ROW_ITEM_COUNT,
  compare = compareItems,
): ItemLayout {
  if (items.length === 0) {
    return {
      items: [],
      size: { width: offsetX, height: offsetY },
    };
  }

  const placed: PlacedItem[] = [];
  const allBands = groupItemsByBand(items, diagnostics);
  const targetWidth = layoutTargetWidth(items, targetAspect, minRowItemCount);
  let currentY = offsetY;
  let maxRight = offsetX;
  for (const [, unsortedBandItems] of [...allBands.entries()].sort(
    countedComparator(diagnostics, ([left], [right]) => left - right),
  )) {
    const bandItems = [...unsortedBandItems].sort(
      countedComparator(diagnostics, compare),
    );
    let rowX = offsetX;
    let rowY = currentY;
    let rowHeight = 0;
    for (const item of bandItems) {
      diagnostics.placementVisits += 1;
      const planningSize = itemPlanningSize(item);
      if (rowX > offsetX && rowX + planningSize.width > offsetX + targetWidth) {
        rowX = offsetX;
        rowY += rowHeight + ITEM_GAP_Y;
        rowHeight = 0;
      }
      placed.push({ item, position: { x: rowX, y: rowY } });
      maxRight = Math.max(maxRight, rowX + item.size.width);
      rowHeight = Math.max(rowHeight, item.size.height);
      rowX += item.size.width + ITEM_GAP_X;
    }
    currentY = rowY + rowHeight + BAND_GAP_Y;
  }

  return {
    items: placed,
    size: {
      width: maxRight,
      height: Math.max(offsetY, currentY - BAND_GAP_Y),
    },
  };
}

function layoutTargetWidth(
  items: readonly LayoutItem[],
  targetAspect: number,
  minRowItemCount: number,
): number {
  let occupiedArea = 0;
  let widestItem = 0;
  for (const item of items) {
    const planningSize = itemPlanningSize(item);
    occupiedArea +=
      (planningSize.width + ITEM_GAP_X) * (planningSize.height + ITEM_GAP_Y);
    widestItem = Math.max(widestItem, planningSize.width);
  }
  return Math.max(
    widestItem,
    minRowItemCount * RESOURCE_SIZE.width + (minRowItemCount - 1) * ITEM_GAP_X,
    Math.sqrt(occupiedArea * targetAspect),
  );
}

function centerItemRows(layout: ItemLayout, offsetX: number): void {
  const rows = new Map<number, PlacedItem[]>();
  for (const placed of layout.items) {
    const row = rows.get(placed.position.y);
    if (row) {
      row.push(placed);
    } else {
      rows.set(placed.position.y, [placed]);
    }
  }
  for (const row of rows.values()) {
    const left = Math.min(...row.map((placed) => placed.position.x));
    const right = Math.max(
      ...row.map((placed) => placed.position.x + placed.item.size.width),
    );
    const availableWidth = layout.size.width - offsetX;
    const rowWidth = right - left;
    const shift = offsetX + (availableWidth - rowWidth) / 2 - left;
    for (const placed of row) placed.position.x += shift;
  }
}

function itemPlanningSize(item: LayoutItem): TopologyLayoutSize {
  return item.stack && item.expanded ? STACK_SIZE : item.size;
}

function materializeItems(
  placedItems: readonly PlacedItem[],
  diagnostics: TopologyLayoutDiagnostics,
  parentKey?: string,
): TopologyLayoutNode[] {
  const nodes: TopologyLayoutNode[] = [];
  for (const { item, position } of placedItems) {
    diagnostics.materializationVisits += 1;
    const resourceGroup = item.resourceGroup;
    if (resourceGroup) {
      const plan =
        item.resourceGroupPlan ??
        ({
          group: resourceGroup,
          expanded: false,
          size: item.size,
          children: [],
        } satisfies ResourceGroupLayoutPlan);
      nodes.push(
        ...materializeResourceGroupPlan(plan, position, diagnostics, parentKey),
      );
      continue;
    }
    if (item.resource) {
      nodes.push({
        kind: "resource",
        key: item.resource.key,
        resource: item.resource,
        band: item.band,
        position,
        size: RESOURCE_SIZE,
        parentKey,
      });
      continue;
    }
    const stack = item.stack;
    if (!stack) continue;
    const memberKeys = stack.members.map((member) => {
      diagnostics.materializationVisits += 1;
      return member.key;
    });
    const common = {
      key: stack.key,
      resourceKindID: stack.resourceKindID,
      typeName: stack.typeName,
      typeNames: stack.typeNames,
      icon: stack.icon,
      count: stack.members.length,
      memberKeys,
      band: stack.band,
      position,
      size: item.size,
      parentKey,
    };
    if (!item.expanded) {
      nodes.push({ kind: "stack", ...common });
      continue;
    }
    nodes.push({ kind: "expandedStack", ...common });
    const columns = Math.max(1, item.expandedColumns ?? 1);
    for (let index = 0; index < stack.members.length; index += 1) {
      diagnostics.materializationVisits += 1;
      const member = stack.members[index];
      if (!member) continue;
      const column = index % columns;
      const row = Math.floor(index / columns);
      nodes.push({
        kind: "resource",
        key: member.key,
        resource: member,
        band: stack.band,
        parentKey: stack.key,
        position: {
          x: EXPANDED_PADDING_X + column * (RESOURCE_SIZE.width + EXPANDED_GAP),
          y:
            EXPANDED_STACK_LABEL_HEIGHT +
            row * (RESOURCE_SIZE.height + EXPANDED_GAP),
        },
        size: RESOURCE_SIZE,
      });
    }
  }
  return nodes;
}

function materializeResourceGroupPlan(
  plan: ResourceGroupLayoutPlan,
  position: TopologyLayoutPoint,
  diagnostics: TopologyLayoutDiagnostics,
  parentKey?: string,
): TopologyLayoutNode[] {
  diagnostics.materializationVisits += 1;
  const { group } = plan;
  const common = {
    key: group.parent.key,
    resource: group.parent,
    childKeys: group.descendantKeys,
    directChildCount: group.children.length,
    childTypeSummaries: group.childTypeSummaries,
    childFindingCount: group.childFindingCount,
    band: resourceBand(group.parent),
    position,
    size: plan.size,
    parentKey,
  };
  if (!plan.expanded) {
    return [{ kind: "resourceGroup", ...common }];
  }
  const nodes: TopologyLayoutNode[] = [
    { kind: "expandedResourceGroup", ...common },
  ];
  for (const child of plan.children) {
    diagnostics.materializationVisits += 1;
    if (child.groupPlan) {
      nodes.push(
        ...materializeResourceGroupPlan(
          child.groupPlan,
          child.position,
          diagnostics,
          group.parent.key,
        ),
      );
      continue;
    }
    nodes.push({
      kind: "resource",
      key: child.resource.key,
      resource: child.resource,
      band: resourceBand(child.resource),
      parentKey: group.parent.key,
      position: child.position,
      size: child.size,
    });
  }
  return nodes;
}

function groupItemsByBand(
  items: readonly LayoutItem[],
  diagnostics: TopologyLayoutDiagnostics,
): Map<number, LayoutItem[]> {
  const result = new Map<number, LayoutItem[]>();
  for (const item of items) {
    diagnostics.placementVisits += 1;
    const values = result.get(item.band);
    if (values) {
      values.push(item);
    } else {
      result.set(item.band, [item]);
    }
  }
  return result;
}

function vSwitchSize(content: TopologyLayoutSize): TopologyLayoutSize {
  return {
    width: Math.max(VSWITCH_MIN_SIZE.width, content.width + VSWITCH_PADDING_X),
    height: Math.max(
      VSWITCH_MIN_SIZE.height,
      content.height + VSWITCH_PADDING_BOTTOM,
    ),
  };
}

function centerVSwitchContent(content: ItemLayout, frameWidth: number): void {
  if (content.items.some(({ item }) => item.stack)) return;
  const contentWidth = Math.max(0, content.size.width - VSWITCH_PADDING_X);
  const availableWidth = Math.max(0, frameWidth - VSWITCH_PADDING_X * 2);
  const offsetX = Math.max(0, (availableWidth - contentWidth) / 2);
  if (offsetX === 0) return;
  for (const placed of content.items) {
    placed.position.x += offsetX;
  }
}

function expandedStackSize(
  memberCount: number,
  columns = Math.max(1, memberCount),
): TopologyLayoutSize {
  const rows = Math.ceil(memberCount / columns);
  return {
    width:
      EXPANDED_PADDING_X * 2 +
      columns * RESOURCE_SIZE.width +
      Math.max(0, columns - 1) * EXPANDED_GAP,
    height:
      EXPANDED_STACK_LABEL_HEIGHT +
      rows * RESOURCE_SIZE.height +
      Math.max(0, rows - 1) * EXPANDED_GAP +
      EXPANDED_PADDING_BOTTOM,
  };
}

function buildResourceGroupLayoutPlan(
  group: ResourceGroup,
  resourceGroupByParent: ReadonlyMap<string, ResourceGroup>,
  expandedGroupKeys: ReadonlySet<string>,
  viewport: TopologyLayoutSize,
  diagnostics: TopologyLayoutDiagnostics,
  ancestors: ReadonlySet<string> = new Set(),
): ResourceGroupLayoutPlan {
  if (!expandedGroupKeys.has(group.parent.key)) {
    return {
      group,
      expanded: false,
      size: RESOURCE_GROUP_SIZE,
      children: [],
    };
  }
  if (ancestors.has(group.parent.key)) {
    return {
      group,
      expanded: false,
      size: RESOURCE_GROUP_SIZE,
      children: [],
    };
  }
  const nextAncestors = new Set(ancestors);
  nextAncestors.add(group.parent.key);
  const children = group.children.map((resource) => {
    diagnostics.groupingVisits += 1;
    const nested = resourceGroupByParent.get(resource.key);
    const groupPlan = nested
      ? buildResourceGroupLayoutPlan(
          nested,
          resourceGroupByParent,
          expandedGroupKeys,
          viewport,
          diagnostics,
          nextAncestors,
        )
      : undefined;
    return {
      resource,
      position: { x: 0, y: 0 },
      size: groupPlan?.size ?? RESOURCE_SIZE,
      ...(groupPlan ? { groupPlan } : {}),
    } satisfies ResourceGroupChildLayout;
  });
  let best = resourceGroupChildGrid(children, 1, diagnostics);
  let bestOccupiedArea = projectedOccupiedArea(best.size, viewport);
  for (let columns = 2; columns <= children.length; columns += 1) {
    const candidate = resourceGroupChildGrid(children, columns, diagnostics);
    const occupiedArea = projectedOccupiedArea(candidate.size, viewport);
    if (occupiedArea > bestOccupiedArea) {
      best = candidate;
      bestOccupiedArea = occupiedArea;
    }
  }
  return {
    group,
    expanded: true,
    size: best.size,
    children: best.children,
  };
}

function resourceGroupChildGrid(
  children: readonly ResourceGroupChildLayout[],
  columns: number,
  diagnostics: TopologyLayoutDiagnostics,
): {
  size: TopologyLayoutSize;
  children: ResourceGroupChildLayout[];
} {
  const safeColumns = Math.max(1, Math.min(columns, children.length));
  const rows = Math.ceil(children.length / safeColumns);
  const columnWidths = Array.from({ length: safeColumns }, () => 0);
  const rowHeights = Array.from({ length: rows }, () => 0);
  for (let index = 0; index < children.length; index += 1) {
    diagnostics.gridVisits += 1;
    const child = children[index];
    if (!child) continue;
    const column = index % safeColumns;
    const row = Math.floor(index / safeColumns);
    columnWidths[column] = Math.max(
      columnWidths[column] ?? 0,
      child.size.width,
    );
    rowHeights[row] = Math.max(rowHeights[row] ?? 0, child.size.height);
  }
  const columnOffsets: number[] = [];
  let x = EXPANDED_PADDING_X;
  for (const width of columnWidths) {
    columnOffsets.push(x);
    x += width + EXPANDED_GAP;
  }
  const rowOffsets: number[] = [];
  let y = EXPANDED_RESOURCE_GROUP_HEADER_HEIGHT;
  for (const height of rowHeights) {
    rowOffsets.push(y);
    y += height + EXPANDED_GAP;
  }
  const positioned = children.map((child, index) => ({
    ...child,
    position: {
      x: columnOffsets[index % safeColumns] ?? EXPANDED_PADDING_X,
      y:
        rowOffsets[Math.floor(index / safeColumns)] ??
        EXPANDED_RESOURCE_GROUP_HEADER_HEIGHT,
    },
  }));
  return {
    size: {
      width: Math.max(
        RESOURCE_GROUP_SIZE.width,
        EXPANDED_PADDING_X * 2 +
          columnWidths.reduce((total, width) => total + width, 0) +
          Math.max(0, safeColumns - 1) * EXPANDED_GAP,
      ),
      height:
        EXPANDED_RESOURCE_GROUP_HEADER_HEIGHT +
        rowHeights.reduce((total, height) => total + height, 0) +
        Math.max(0, rows - 1) * EXPANDED_GAP +
        EXPANDED_PADDING_BOTTOM,
    },
    children: positioned,
  };
}

function expandedStackColumnCount(
  memberCount: number,
  viewport: TopologyLayoutSize,
): number {
  if (memberCount <= 1) return 1;
  let bestColumns = 1;
  let bestOccupiedArea = -1;
  for (let columns = 1; columns <= memberCount; columns += 1) {
    const size = expandedStackSize(memberCount, columns);
    const occupiedArea = projectedOccupiedArea(size, viewport);
    if (occupiedArea > bestOccupiedArea) {
      bestColumns = columns;
      bestOccupiedArea = occupiedArea;
    }
  }
  return bestColumns;
}

function vSwitchColumnCount(
  layouts: readonly VSwitchGridLayout[],
  availableWidth: number,
): number {
  if (layouts.length <= 1) return 1;
  const candidates =
    layouts.length <= 64
      ? Array.from({ length: layouts.length }, (_, index) => index + 1)
      : boundedGridColumnCandidates(layouts, availableWidth);
  let bestColumns = 1;
  for (const columns of candidates) {
    const tracks = vSwitchGridTracks(layouts, columns);
    if (tracks.width <= availableWidth) bestColumns = columns;
  }
  return bestColumns;
}

function boundedGridColumnCandidates(
  layouts: readonly VSwitchGridLayout[],
  availableWidth: number,
): number[] {
  const averageWidth =
    layouts.reduce((total, layout) => total + layout.size.width, 0) /
    layouts.length;
  const ideal = Math.min(
    layouts.length,
    Math.max(
      1,
      (availableWidth + VSWITCH_GRID_GAP_X) /
        (averageWidth + VSWITCH_GRID_GAP_X),
    ),
  );
  const candidates = new Set<number>([1, layouts.length]);
  for (
    let columns = Math.floor(ideal) - 3;
    columns <= Math.ceil(ideal) + 3;
    columns += 1
  ) {
    candidates.add(Math.min(layouts.length, Math.max(1, columns)));
  }
  return [...candidates].sort((left, right) => left - right);
}

function vSwitchGridTracks(
  layouts: readonly VSwitchGridLayout[],
  columns: number,
): {
  columnWidths: number[];
  rowHeights: number[];
  width: number;
  height: number;
} {
  const columnWidths = Array.from({ length: columns }, () => 0);
  const rowHeights = Array.from(
    { length: Math.ceil(layouts.length / columns) },
    () => 0,
  );
  for (let index = 0; index < layouts.length; index += 1) {
    const layout = layouts[index];
    if (!layout) continue;
    const column = index % columns;
    const row = Math.floor(index / columns);
    columnWidths[column] = Math.max(
      columnWidths[column] ?? 0,
      layout.size.width,
    );
    rowHeights[row] = Math.max(rowHeights[row] ?? 0, layout.size.height);
  }
  return {
    columnWidths,
    rowHeights,
    width:
      columnWidths.reduce((total, value) => total + value, 0) +
      Math.max(0, columns - 1) * VSWITCH_GRID_GAP_X,
    height:
      rowHeights.reduce((total, value) => total + value, 0) +
      Math.max(0, rowHeights.length - 1) * VSWITCH_GRID_GAP_Y,
  };
}

function publicLayoutOffsetX(
  publicWidth: number,
  layoutWidth: number,
  layouts: readonly VSwitchGridLayout[],
  columns: number,
  columnOffsets: readonly number[],
  gridOffsetX: number,
): number {
  let populatedLeft = Number.POSITIVE_INFINITY;
  let populatedRight = Number.NEGATIVE_INFINITY;
  for (let index = 0; index < layouts.length; index += 1) {
    const layout = layouts[index];
    if (!layout || layout.kind !== "vswitch") continue;
    const column = index % columns;
    const left = gridOffsetX + (columnOffsets[column] ?? 0);
    populatedLeft = Math.min(populatedLeft, left);
    populatedRight = Math.max(populatedRight, left + layout.size.width);
  }
  const centered = (layoutWidth - publicWidth) / 2;
  if (!Number.isFinite(populatedLeft) || !Number.isFinite(populatedRight)) {
    return Math.max(0, centered);
  }
  const populatedCenter = (populatedLeft + populatedRight) / 2;
  return Math.min(
    Math.max(0, populatedCenter - publicWidth / 2),
    Math.max(0, layoutWidth - publicWidth),
  );
}

function projectedOccupiedArea(
  content: TopologyLayoutSize,
  viewport: TopologyLayoutSize,
): number {
  const scale = Math.min(
    MAX_FIT_ZOOM,
    viewport.width / Math.max(1, content.width),
    viewport.height / Math.max(1, content.height),
  );
  return content.width * content.height * scale * scale;
}

function validViewport(
  viewport: TopologyLayoutSize | undefined,
): TopologyLayoutSize {
  if (!viewport || viewport.width <= 0 || viewport.height <= 0) {
    return DEFAULT_VIEWPORT;
  }
  return viewport;
}

function expandedVSwitchStackSize(memberCount: number): TopologyLayoutSize {
  const columns = Math.min(VSWITCH_STACK_COLUMNS, memberCount);
  const rows = Math.ceil(memberCount / VSWITCH_STACK_COLUMNS);
  return {
    width:
      EXPANDED_VSWITCH_STACK_CONTENT_INSET +
      columns * VSWITCH_STACK_MEMBER_SIZE.width +
      Math.max(0, columns - 1) * EXPANDED_VSWITCH_STACK_GAP_X,
    height:
      EXPANDED_VSWITCH_STACK_HEADER_HEIGHT +
      EXPANDED_VSWITCH_STACK_CONTENT_PADDING_Y +
      rows * VSWITCH_STACK_MEMBER_SIZE.height +
      Math.max(0, rows - 1) * EXPANDED_VSWITCH_STACK_GAP_Y,
  };
}

function stableVSwitches(
  values: readonly TopologyVSwitch[],
  diagnostics: TopologyLayoutDiagnostics,
): TopologyVSwitch[] {
  return [...values].sort(
    countedComparator(diagnostics, (left, right) => {
      return (
        compareText(left.zone ?? "", right.zone ?? "") ||
        compareText(left.name, right.name) ||
        compareText(left.key, right.key)
      );
    }),
  );
}

function displayEdges(
  values: readonly TopologyResourceEdge[],
  resourceByKey: ReadonlyMap<string, TopologyResource>,
  diagnostics: TopologyLayoutDiagnostics,
  resourceGroupByChild: ReadonlyMap<string, ResourceGroup> = new Map(),
  stackByResource: ReadonlyMap<string, StackGroup> = new Map(),
  expandedGroupKeys: ReadonlySet<string> = new Set(),
): readonly TopologyResourceEdge[] {
  const usesTargetsBySource = new Map<string, Set<string>>();
  const relationshipPairs = new Set<string>();
  const connectedPairs = new Set<string>();
  const privateLinkLifecyclePairs = new Set<string>();
  for (const edge of values) {
    diagnostics.edgeVisits += 1;
    const pairKey = unorderedEdgePairKey(edge);
    if (edge.kind === "relationship") {
      relationshipPairs.add(pairKey);
      if (edge.relation === "connected_to") connectedPairs.add(pairKey);
    } else if (isPrivateLinkEndpointENIPair(edge, resourceByKey)) {
      privateLinkLifecyclePairs.add(pairKey);
    }
    if (edge.kind !== "relationship" || edge.relation !== "uses") continue;
    const targets = usesTargetsBySource.get(edge.source_key);
    if (targets) {
      targets.add(edge.target_key);
    } else {
      usesTargetsBySource.set(edge.source_key, new Set([edge.target_key]));
    }
  }
  const visible: TopologyResourceEdge[] = values
    .filter((edge) => {
      const pairKey = unorderedEdgePairKey(edge);
      if (edge.kind === "lifecycle") {
        if (privateLinkLifecyclePairs.has(pairKey)) {
          return !connectedPairs.has(pairKey);
        }
        return !relationshipPairs.has(pairKey);
      }
      if (
        edge.relation === "member_of" &&
        privateLinkLifecyclePairs.has(pairKey)
      ) {
        return false;
      }
      if (edge.relation !== "uses") return true;
      const source = resourceByKey.get(edge.source_key);
      const target = resourceByKey.get(edge.target_key);
      if (
        !source ||
        !target ||
        !isComputeInstance(source) ||
        !isSecurityGroup(target)
      ) {
        return true;
      }
      for (const intermediateKey of usesTargetsBySource.get(source.key) ?? []) {
        const intermediate = resourceByKey.get(intermediateKey);
        if (
          intermediate &&
          isNetworkInterface(intermediate) &&
          usesTargetsBySource.get(intermediateKey)?.has(target.key)
        ) {
          return false;
        }
      }
      return true;
    })
    .map((edge) =>
      edge.kind === "lifecycle" &&
      privateLinkLifecyclePairs.has(unorderedEdgePairKey(edge))
        ? { ...edge, kind: "relationship", relation: "connected_to" }
        : edge,
    );
  const sorted = visible.sort(
    countedComparator(diagnostics, (left, right) =>
      compareText(left.key, right.key),
    ),
  );
  if (resourceGroupByChild.size === 0 && stackByResource.size === 0) {
    return sorted;
  }

  const projected = new Map<
    string,
    { edge: TopologyResourceEdge; edgeKeys: string[] }
  >();
  for (const edge of sorted) {
    diagnostics.edgeVisits += 1;
    const sourceGroup = resourceGroupByChild.get(edge.source_key);
    const source = resourceByKey.get(edge.source_key);
    const target = resourceByKey.get(edge.target_key);
    if (
      edge.kind === "relationship" &&
      source &&
      target &&
      isParentChildRelationship(edge, source, target) &&
      sourceGroup &&
      (sourceGroup.parent.key === edge.target_key ||
        isResourceGroupAncestor(
          sourceGroup.parent.key,
          edge.target_key,
          resourceGroupByChild,
        ))
    ) {
      continue;
    }
    const sourceKey = visibleResourceStackKey(
      visibleResourceGroupKey(
        edge.source_key,
        resourceGroupByChild,
        expandedGroupKeys,
      ),
      stackByResource,
      expandedGroupKeys,
    );
    const targetKey = visibleResourceStackKey(
      visibleResourceGroupKey(
        edge.target_key,
        resourceGroupByChild,
        expandedGroupKeys,
      ),
      stackByResource,
      expandedGroupKeys,
    );
    if (sourceKey === targetKey) continue;
    const aggregateKey = [sourceKey, targetKey, edge.kind, edge.relation].join(
      "\u0000",
    );
    const existing = projected.get(aggregateKey);
    if (existing) {
      existing.edgeKeys.push(edge.key);
      continue;
    }
    const remapped =
      sourceKey !== edge.source_key || targetKey !== edge.target_key;
    projected.set(aggregateKey, {
      edge: remapped
        ? {
            ...edge,
            key: `group-edge:${encodeURIComponent(aggregateKey)}`,
            source_key: sourceKey,
            target_key: targetKey,
          }
        : edge,
      edgeKeys: [edge.key],
    });
  }
  return [...projected.values()]
    .map(({ edge, edgeKeys }) =>
      edgeKeys.length > 1
        ? {
            ...edge,
            metadata: {
              ...edge.metadata,
              aggregate_count: edgeKeys.length,
            },
          }
        : edge,
    )
    .sort(
      countedComparator(diagnostics, (left, right) =>
        compareText(left.key, right.key),
      ),
    );
}

function unorderedEdgePairKey(edge: TopologyResourceEdge): string {
  return edge.source_key < edge.target_key
    ? `${edge.source_key}\u0000${edge.target_key}`
    : `${edge.target_key}\u0000${edge.source_key}`;
}

function isPrivateLinkEndpointENIPair(
  edge: TopologyResourceEdge,
  resourceByKey: ReadonlyMap<string, TopologyResource>,
): boolean {
  const source = resourceByKey.get(edge.source_key);
  const target = resourceByKey.get(edge.target_key);
  const sourceClass = source?.class?.trim().toLowerCase();
  const targetClass = target?.class?.trim().toLowerCase();
  return (
    (sourceClass === "network.endpoint" &&
      target !== undefined &&
      isNetworkInterface(target)) ||
    (targetClass === "network.endpoint" &&
      source !== undefined &&
      isNetworkInterface(source))
  );
}

function isResourceGroupAncestor(
  resourceKey: string,
  possibleAncestorKey: string,
  resourceGroupByChild: ReadonlyMap<string, ResourceGroup>,
): boolean {
  let currentKey = resourceKey;
  const visited = new Set<string>();
  while (!visited.has(currentKey)) {
    visited.add(currentKey);
    const group = resourceGroupByChild.get(currentKey);
    if (!group) return false;
    if (group.parent.key === possibleAncestorKey) return true;
    currentKey = group.parent.key;
  }
  return false;
}

function visibleResourceGroupKey(
  resourceKey: string,
  resourceGroupByChild: ReadonlyMap<string, ResourceGroup>,
  expandedGroupKeys: ReadonlySet<string>,
): string {
  let visibleKey = resourceKey;
  let currentKey = resourceKey;
  const visited = new Set<string>();
  while (!visited.has(currentKey)) {
    visited.add(currentKey);
    const group = resourceGroupByChild.get(currentKey);
    if (!group) break;
    if (!expandedGroupKeys.has(group.parent.key)) {
      visibleKey = group.parent.key;
    }
    currentKey = group.parent.key;
  }
  return visibleKey;
}

function visibleResourceStackKey(
  resourceKey: string,
  stackByResource: ReadonlyMap<string, StackGroup>,
  expandedGroupKeys: ReadonlySet<string>,
): string {
  const stack = stackByResource.get(resourceKey);
  return stack && !expandedGroupKeys.has(stack.key) ? stack.key : resourceKey;
}

function isComputeInstance(resource: TopologyResource): boolean {
  const className = resource.class?.trim().toLowerCase() ?? "";
  const kind = resource.resource_kind_id.toLowerCase();
  return (
    className === "compute.instance" ||
    kind === "ecs" ||
    kind.endsWith("::ecs::instance")
  );
}

function isNetworkInterface(resource: TopologyResource): boolean {
  const className = resource.class?.trim().toLowerCase() ?? "";
  const kind = resource.resource_kind_id.toLowerCase();
  return (
    className === "network.eni" ||
    className === "network.interface" ||
    kind === "eni" ||
    kind.endsWith("::ecs::networkinterface")
  );
}

function isSecurityGroup(resource: TopologyResource): boolean {
  const className = resource.class?.trim().toLowerCase() ?? "";
  const kind = resource.resource_kind_id.toLowerCase();
  return (
    className === "network.security_group" ||
    kind === "security-group" ||
    kind.endsWith("::ecs::securitygroup")
  );
}

export function topologyEdgeHandles(
  nodes: readonly TopologyLayoutNode[],
  edges: readonly TopologyResourceEdge[],
): ReadonlyMap<string, TopologyEdgeHandles> {
  const nodeByKey = new Map(nodes.map((node) => [node.key, node]));
  const absolutePositionByKey = new Map<string, TopologyLayoutPoint>();

  const absolutePosition = (key: string): TopologyLayoutPoint | undefined => {
    const cached = absolutePositionByKey.get(key);
    if (cached) return cached;
    const node = nodeByKey.get(key);
    if (!node) return undefined;
    const parentPosition = node.parentKey
      ? absolutePosition(node.parentKey)
      : undefined;
    const position = parentPosition
      ? {
          x: parentPosition.x + node.position.x,
          y: parentPosition.y + node.position.y,
        }
      : node.position;
    absolutePositionByKey.set(key, position);
    return position;
  };

  const result = new Map<string, TopologyEdgeHandles>();
  for (const edge of edges) {
    const source = nodeByKey.get(edge.source_key);
    const target = nodeByKey.get(edge.target_key);
    const sourcePosition = absolutePosition(edge.source_key);
    const targetPosition = absolutePosition(edge.target_key);
    if (!source || !target || !sourcePosition || !targetPosition) continue;
    const deltaX =
      targetPosition.x +
      target.size.width / 2 -
      (sourcePosition.x + source.size.width / 2);
    const deltaY =
      targetPosition.y +
      target.size.height / 2 -
      (sourcePosition.y + source.size.height / 2);
    if (Math.abs(deltaY) >= Math.abs(deltaX)) {
      result.set(edge.key, {
        sourceHandle: deltaY >= 0 ? "source-bottom" : "source-top",
        targetHandle: deltaY >= 0 ? "target-top" : "target-bottom",
      });
    } else {
      result.set(edge.key, {
        sourceHandle: deltaX >= 0 ? "source-right" : "source-left",
        targetHandle: deltaX >= 0 ? "target-left" : "target-right",
      });
    }
  }
  return result;
}

function compareItems(left: LayoutItem, right: LayoutItem): number {
  return (
    left.band - right.band ||
    compareText(left.componentKey, right.componentKey) ||
    compareText(left.resourceKindID, right.resourceKindID) ||
    compareText(left.name, right.name) ||
    compareText(left.key, right.key)
  );
}

function compareVPCPublicItems(left: LayoutItem, right: LayoutItem): number {
  return (
    vpcPublicItemPriority(left) - vpcPublicItemPriority(right) ||
    compareItems(left, right)
  );
}

function vpcPublicItemPriority(item: LayoutItem): number {
  const resource =
    item.resource ?? item.resourceGroup?.parent ?? item.stack?.members[0];
  return resource?.class?.trim().toLowerCase() === "network.endpoint" ? 1 : 0;
}

function compareResources(
  left: TopologyResource,
  right: TopologyResource,
): number {
  return (
    resourceBand(left) - resourceBand(right) ||
    compareText(left.resource_kind_id, right.resource_kind_id) ||
    compareText(left.name, right.name) ||
    compareText(left.key, right.key)
  );
}

function compareLayoutResources(
  left: TopologyResource,
  right: TopologyResource,
  componentByKey: ReadonlyMap<string, string>,
): number {
  return (
    resourceBand(left) - resourceBand(right) ||
    compareText(
      componentByKey.get(left.key) ?? left.key,
      componentByKey.get(right.key) ?? right.key,
    ) ||
    compareText(left.resource_kind_id, right.resource_kind_id) ||
    compareText(left.name, right.name) ||
    compareText(left.key, right.key)
  );
}

function compareText(left: string, right: string): number {
  if (left < right) return -1;
  if (left > right) return 1;
  return 0;
}

function countedComparator<T>(
  diagnostics: TopologyLayoutDiagnostics,
  compare: (left: T, right: T) => number,
): (left: T, right: T) => number {
  return (left, right) => {
    diagnostics.sortComparisons += 1;
    return compare(left, right);
  };
}

function resourceBand(resource: TopologyResource): number {
  const className = resource.class?.trim().toLowerCase();
  if (className) {
    if (className === "network" || className.startsWith("network.")) return 0;
    if (
      className === "compute" ||
      className.startsWith("compute.") ||
      className === "container" ||
      className.startsWith("container.")
    ) {
      return 1;
    }
    if (className === "storage" || className.startsWith("storage.")) return 2;
    return 3;
  }
  switch (resource.domain) {
    case "network":
      return 0;
    case "compute":
      return 1;
    case "storage":
      return 2;
    case "unknown":
      return 3;
  }
}

function cumulativeOffsets(
  values: readonly number[],
  gap: number,
  diagnostics: TopologyLayoutDiagnostics,
): number[] {
  const offsets: number[] = [];
  let current = 0;
  for (const value of values) {
    diagnostics.gridVisits += 1;
    offsets.push(current);
    current += value + gap;
  }
  return offsets;
}
