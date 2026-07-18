import type {
  Asset,
  CleanupSelector,
  TopologyEntrySummary,
  TopologyResource,
  TopologyVSwitch,
} from "@/api/types";
import type {
  CleanupTarget,
  CleanupTargetLocationContext,
} from "./cleanupSelection";

function displayName(resource: TopologyResource): string {
  return (
    resource.name.trim() ||
    resource.native_id.trim() ||
    resource.asset_id.trim() ||
    resource.key
  );
}

function assetDisplayName(resource: Asset): string {
  return (
    resource.name?.trim() ||
    resource.identity.native_id.trim() ||
    resource.id.trim()
  );
}

function ancestryPath(
  ancestryKeys: readonly string[],
  targetKey: string,
): string[] {
  return ancestryKeys.at(-1) === targetKey
    ? [...ancestryKeys]
    : [...ancestryKeys, targetKey];
}

function cloneLocationContext(
  context: CleanupTargetLocationContext | undefined,
): CleanupTargetLocationContext | undefined {
  if (!context) return undefined;
  return {
    region: context.region && { ...context.region },
    vpc: context.vpc && { ...context.vpc },
    scope: context.scope && { ...context.scope },
  };
}

function selectorForEntry(
  connectionId: string,
  entry: TopologyEntrySummary,
  kind: "region" | "vpc",
): CleanupSelector {
  const key = entry.cleanup.selector_key?.trim();
  if (entry.cleanup.selector_kind === "scope" && key) {
    return {
      kind: "scope",
      connection_id: connectionId,
      scope_id: key,
      scope_kind: kind === "region" ? "region" : undefined,
      descendants: true,
      display_name: entry.name,
    };
  }
  return {
    kind: "group",
    connection_id: connectionId,
    group_key: key || entry.key,
    display_name: entry.name,
  };
}

export function resourceCleanupTarget(input: {
  connectionId: string;
  resource: TopologyResource;
  ancestryKeys: readonly string[];
  locationContext?: CleanupTargetLocationContext;
}): CleanupTarget {
  const name = displayName(input.resource);
  const assetId = input.resource.asset_id.trim();
  const key = `asset:${assetId}`;
  const locationContext = cloneLocationContext(input.locationContext);
  return {
    key,
    kind: "resource",
    connectionId: input.connectionId,
    displayName: name,
    selector: {
      kind: "asset",
      asset_id: assetId,
      display_name: name,
    },
    ancestryKeys: ancestryPath(input.ancestryKeys, key),
    ...(locationContext ? { locationContext } : {}),
  };
}

export function assetCleanupTarget(input: {
  connectionId: string;
  asset: Asset;
  ancestryKeys?: readonly string[];
  locationContext?: CleanupTargetLocationContext;
}): CleanupTarget {
  const name = assetDisplayName(input.asset);
  const assetId = input.asset.id.trim();
  const key = `asset:${assetId}`;
  const locationContext = cloneLocationContext(input.locationContext);
  return {
    key,
    kind: "resource",
    connectionId: input.connectionId,
    displayName: name,
    selector: {
      kind: "asset",
      asset_id: assetId,
      display_name: name,
    },
    ancestryKeys: ancestryPath(input.ancestryKeys ?? [], key),
    ...(locationContext ? { locationContext } : {}),
  };
}

export function vSwitchCleanupTarget(input: {
  connectionId: string;
  vSwitch: TopologyVSwitch;
  ancestryKeys: readonly string[];
  locationContext?: CleanupTargetLocationContext;
}): CleanupTarget | null {
  const assetId = input.vSwitch.asset_id?.trim();
  if (!assetId) return null;
  const name =
    input.vSwitch.name.trim() ||
    input.vSwitch.native_id.trim() ||
    input.vSwitch.key;
  const key = `asset:${assetId}`;
  const locationContext = cloneLocationContext(input.locationContext);
  return {
    key,
    kind: "resource",
    connectionId: input.connectionId,
    displayName: name,
    selector: {
      kind: "asset",
      asset_id: assetId,
      display_name: name,
    },
    ancestryKeys: ancestryPath(input.ancestryKeys, key),
    ...(locationContext ? { locationContext } : {}),
  };
}

export function entryCleanupTarget(input: {
  connectionId: string;
  entry: TopologyEntrySummary;
  kind: "region" | "vpc";
  ancestryKeys: readonly string[];
  locationContext?: CleanupTargetLocationContext;
}): CleanupTarget {
  const locationContext = cloneLocationContext(input.locationContext);
  return {
    key: input.entry.key,
    kind: input.kind,
    connectionId: input.connectionId,
    displayName: input.entry.name,
    selector: selectorForEntry(input.connectionId, input.entry, input.kind),
    ancestryKeys: ancestryPath(input.ancestryKeys, input.entry.key),
    ...(locationContext ? { locationContext } : {}),
    resourceCount: input.entry.resource_count,
  };
}

export function batchCleanupTarget(input: {
  connectionId: string;
  key: string;
  displayName: string;
  resources: readonly TopologyResource[];
  ancestryKeys: readonly string[];
  locationContext?: CleanupTargetLocationContext;
}): CleanupTarget {
  const resourcesByAssetId = new Map<string, TopologyResource>();
  for (const resource of input.resources) {
    const assetId = resource.asset_id.trim();
    if (assetId && !resourcesByAssetId.has(assetId)) {
      resourcesByAssetId.set(assetId, resource);
    }
  }
  const selectors: CleanupSelector[] = [...resourcesByAssetId].map(
    ([assetId, resource]) => ({
      kind: "asset",
      asset_id: assetId,
      display_name: displayName(resource),
    }),
  );
  const memberAssetIds = [...resourcesByAssetId.keys()];
  const locationContext = cloneLocationContext(input.locationContext);
  return {
    key: input.key,
    kind: "resource_batch",
    connectionId: input.connectionId,
    displayName: input.displayName,
    selector: selectors,
    ancestryKeys: ancestryPath(input.ancestryKeys, input.key),
    ...(locationContext ? { locationContext } : {}),
    memberAssetIds,
    resourceCount: memberAssetIds.length,
  };
}
