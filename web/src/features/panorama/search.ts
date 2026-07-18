import type {
  Asset,
  ConnectionRegion,
  ResourceKind,
  Scope,
  TopologyView,
} from "@/api/types";
import { resourceTypeName } from "@/components/domain/resourceKindLabel";
import {
  panoramaGlobalPath,
  panoramaRegionPath,
  panoramaRegionPublicPath,
  panoramaVPCPath,
  type PanoramaRoute,
} from "./route";

export type PanoramaSearchResult =
  | {
      key: string;
      kind: "region";
      name: string;
      resourceId: string;
      typeName: string;
      pathname: string;
      regionId: string;
    }
  | {
      key: string;
      kind: "resource";
      name: string;
      resourceId: string;
      typeName: string;
      pathname: string;
      resourceKey: string;
      resourceClass?: string;
      resourceNativeType: string;
      regionId?: string;
      vpcId?: string;
    };

export interface PanoramaSearchHighlight {
  nodeKey?: string;
  scope: boolean;
}

export function buildPanoramaSearchResults(input: {
  term: string;
  assets: readonly Asset[];
  regions: readonly ConnectionRegion[];
  scopes: readonly Scope[];
  kinds: ReadonlyMap<string, ResourceKind>;
  locale: string;
  regionTypeName: string;
  scope: PanoramaRoute;
  limit?: number;
}): PanoramaSearchResult[] {
  const scopeByID = new Map(input.scopes.map((scope) => [scope.id, scope]));
  const results: PanoramaSearchResult[] = [
    ...input.regions
      .filter((region) => region.lifecycle === "active")
      .map((region): PanoramaSearchResult => ({
        key: `region:${region.region_id}`,
        kind: "region",
        name: region.name || region.region_id,
        resourceId: region.region_id,
        typeName: input.regionTypeName,
        pathname: panoramaRegionPath(region.region_id),
        regionId: region.region_id,
      })),
    ...input.assets
      .filter((asset) => !asset.closed_at)
      .map((asset) =>
        panoramaResourceSearchResult(
          asset,
          scopeByID,
          input.kinds,
          input.locale,
        ),
      ),
  ];
  const term = normalizeSearchValue(input.term);
  return results
    .filter(
      (result) =>
        resultBelongsToScope(result, input.scope) &&
        resultScore(result, term) > 0,
    )
    .sort((left, right) => {
      const priorityDifference =
        resultTypePriority(left) - resultTypePriority(right);
      if (priorityDifference !== 0) return priorityDifference;
      const scoreDifference =
        resultScore(right, term) - resultScore(left, term);
      if (scoreDifference !== 0) return scoreDifference;
      const nameDifference = left.name.localeCompare(right.name, input.locale);
      if (nameDifference !== 0) return nameDifference;
      return left.resourceId.localeCompare(right.resourceId, input.locale);
    })
    .slice(0, input.limit ?? 12);
}

function resultBelongsToScope(
  result: PanoramaSearchResult,
  scope: PanoramaRoute,
) {
  switch (scope.kind) {
    case "account":
      return true;
    case "account-global":
      return result.kind === "resource" && !result.regionId;
    case "region":
      return result.kind === "resource" && result.regionId === scope.regionId;
    case "region-public":
      return (
        result.kind === "resource" &&
        result.regionId === scope.regionId &&
        !result.vpcId
      );
    case "vpc":
      return (
        result.kind === "resource" &&
        result.regionId === scope.regionId &&
        result.vpcId === scope.vpcId
      );
  }
}

export function panoramaSearchHighlight(
  view: TopologyView | undefined,
  result: PanoramaSearchResult | undefined,
): PanoramaSearchHighlight | undefined {
  if (!view || !result) return undefined;

  if (view.kind === "account") {
    const regionID =
      result.kind === "region" ? result.regionId : result.regionId;
    if (!regionID) {
      return result.kind === "resource" && view.global_resources
        ? { nodeKey: view.global_resources.key, scope: false }
        : undefined;
    }
    const region = view.regions.find(
      (entry) => entry.native_id?.trim() === regionID,
    );
    return region ? { nodeKey: region.key, scope: false } : undefined;
  }

  if (view.kind === "region") {
    if (
      result.kind !== "resource" ||
      result.regionId !== view.region.native_id
    ) {
      return undefined;
    }
    if (!result.vpcId) {
      return { nodeKey: view.public_resources.key, scope: false };
    }
    const vpc = view.vpcs.find(
      (entry) => entry.native_id?.trim() === result.vpcId,
    );
    return vpc ? { nodeKey: vpc.key, scope: false } : undefined;
  }

  if (result.kind !== "resource") return undefined;
  if (
    view.kind === "vpc" &&
    (view.resources.some((resource) => resource.key === result.resourceKey) ||
      view.vswitches.some((vSwitch) => vSwitch.key === result.resourceKey))
  ) {
    return { nodeKey: result.resourceKey, scope: false };
  }
  if (view.kind === "resource_graph") {
    const isGlobal = view.context.key === "account-global";
    const belongsHere = isGlobal
      ? !result.regionId
      : result.regionId === view.context.native_id && !result.vpcId;
    return belongsHere &&
      view.resources.some((resource) => resource.key === result.resourceKey)
      ? { nodeKey: result.resourceKey, scope: false }
      : undefined;
  }

  if (
    result.regionId !== view.region.native_id ||
    result.vpcId !== view.vpc.native_id
  ) {
    return undefined;
  }
  const isCurrentVPC =
    result.resourceClass === "network.vpc" &&
    result.resourceId === view.vpc.native_id;
  return isCurrentVPC ? { scope: true } : undefined;
}

export function panoramaResourceSearchResult(
  asset: Asset,
  scopeByID: ReadonlyMap<string, Scope>,
  kinds: ReadonlyMap<string, ResourceKind>,
  locale: string,
): PanoramaSearchResult {
  const kind = kinds.get(asset.resource_kind_id);
  const authoritativeScope = findAuthoritativeScope(asset.scope_id, scopeByID);
  const regionId =
    authoritativeScope?.kind === "region"
      ? authoritativeScope.native_id.trim() ||
        authoritativeScope.location?.trim()
      : undefined;
  const explicitVPCID = normalizedString(asset.normalized, "vpc_id");
  const vpcId =
    kind?.class === "network.vpc"
      ? asset.identity.native_id.trim() || explicitVPCID
      : explicitVPCID;
  const pathname = regionId
    ? vpcId
      ? panoramaVPCPath(regionId, vpcId)
      : panoramaRegionPublicPath(regionId)
    : panoramaGlobalPath();
  return {
    key: `resource:${asset.id}`,
    kind: "resource",
    name: asset.name?.trim() || asset.identity.native_id,
    resourceId: asset.identity.native_id,
    typeName: resourceTypeName(
      kind?.native_type || asset.identity.native_type || asset.resource_kind_id,
      kind?.display_name ||
        asset.identity.native_type ||
        asset.resource_kind_id,
      kind?.display_names,
      locale,
    ),
    pathname,
    resourceKey: asset.id,
    resourceClass: kind?.class,
    resourceNativeType: asset.identity.native_type,
    ...(regionId ? { regionId } : {}),
    ...(vpcId ? { vpcId } : {}),
  };
}

function resultTypePriority(result: PanoramaSearchResult) {
  if (result.kind === "region") return -1;
  switch (result.resourceClass) {
    case "network.vpc":
      return 0;
    case "network.subnet":
      return 1;
    case "compute.instance":
      return 2;
    case "network.security_group":
      return 3;
    case "storage.block":
      return 4;
    case "storage.bucket":
      return 5;
  }

  const nativeType = normalizeSearchValue(result.resourceNativeType);
  if (nativeType.endsWith("::vpc")) return 0;
  if (nativeType.endsWith("::vswitch")) return 1;
  if (nativeType.endsWith("::instance") && nativeType.includes("::ecs::")) {
    return 2;
  }
  if (nativeType.endsWith("::securitygroup")) return 3;
  if (nativeType.endsWith("::disk")) return 4;
  if (nativeType.endsWith("::bucket")) return 5;
  return 6;
}

function findAuthoritativeScope(
  scopeID: string,
  scopeByID: ReadonlyMap<string, Scope>,
): Scope | undefined {
  const visited = new Set<string>();
  let current = scopeByID.get(scopeID);
  while (current && !visited.has(current.id)) {
    visited.add(current.id);
    if (current.kind === "region" || current.kind === "global") return current;
    current = current.parent_id ? scopeByID.get(current.parent_id) : undefined;
  }
  return undefined;
}

function normalizedString(
  values: Record<string, unknown> | undefined,
  key: string,
) {
  const value = values?.[key];
  return typeof value === "string" || typeof value === "number"
    ? String(value).trim()
    : "";
}

function resultScore(result: PanoramaSearchResult, term: string) {
  const name = normalizeSearchValue(result.name);
  const id = normalizeSearchValue(result.resourceId);
  const type = normalizeSearchValue(result.typeName);
  if (id === term) return 100;
  if (name === term) return 95;
  if (id.startsWith(term)) return 80;
  if (name.startsWith(term)) return 75;
  if (id.includes(term)) return 60;
  if (name.includes(term)) return 55;
  if (type.includes(term)) return 30;
  return 0;
}

function normalizeSearchValue(value: string) {
  return value.trim().toLocaleLowerCase();
}
