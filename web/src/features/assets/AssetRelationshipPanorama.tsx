import { useCallback, useMemo, useState } from "react";
import type {
  Asset,
  LifecycleBinding,
  Relationship,
  ResourceGraphTopologyView,
  ResourceKind,
  TopologyResource,
  TopologyResourceDomain,
} from "@/api/types";
import { resourceTypeName } from "@/components/domain/resourceKindLabel";
import { useLocale } from "@/i18n/LocaleProvider";
import { TopologyCanvas } from "../panorama/TopologyCanvas";

export function AssetRelationshipPanorama({
  focus,
  assets,
  relationships,
  lifecycleBindings,
  resourceKinds,
}: {
  focus: Asset;
  assets: Asset[];
  relationships: Relationship[];
  lifecycleBindings: LifecycleBinding[];
  resourceKinds: ResourceKind[];
}) {
  const { locale, t } = useLocale();
  const [selectedResourceKey, setSelectedResourceKey] = useState(focus.id);
  const resourceKindsByID = useMemo(
    () => new Map(resourceKinds.map((kind) => [kind.id, kind])),
    [resourceKinds],
  );
  const view = useMemo(
    () =>
      buildAssetRelationshipTopologyView({
        focus,
        assets,
        relationships,
        lifecycleBindings,
        resourceKinds: resourceKindsByID,
        locale,
      }),
    [
      assets,
      focus,
      lifecycleBindings,
      locale,
      relationships,
      resourceKindsByID,
    ],
  );
  const selectResource = useCallback(
    (resource: TopologyResource) => setSelectedResourceKey(resource.key),
    [],
  );
  const clearFocus = useCallback(
    () => setSelectedResourceKey(focus.id),
    [focus.id],
  );

  return (
    <section
      aria-label={t("asset.relationshipGraph")}
      className="h-full min-h-0 w-full overflow-hidden"
    >
      <TopologyCanvas
        view={view}
        complete
        resourceKinds={resourceKindsByID}
        selectedResourceKey={selectedResourceKey}
        onSelectResource={selectResource}
        onClearFocus={clearFocus}
      />
    </section>
  );
}

export function buildAssetRelationshipTopologyView({
  focus,
  assets,
  relationships,
  lifecycleBindings,
  resourceKinds,
  locale,
}: {
  focus: Asset;
  assets: Asset[];
  relationships: Relationship[];
  lifecycleBindings: LifecycleBinding[];
  resourceKinds: ReadonlyMap<string, ResourceKind>;
  locale: string;
}): ResourceGraphTopologyView {
  const assetsByID = new Map(
    [focus, ...assets].map((asset) => [asset.id, asset]),
  );
  const resourceIDs = [
    ...new Set([
      focus.id,
      ...assets.map((asset) => asset.id),
      ...relationships.flatMap((relationship) => [
        relationship.source_asset_id,
        relationship.target_asset_id,
      ]),
      ...lifecycleBindings.flatMap((binding) => [
        binding.controller_asset_id,
        binding.managed_asset_id,
      ]),
    ]),
  ];

  return {
    kind: "resource_graph",
    context: {
      key: focus.id,
      name: focus.name || focus.identity.native_id || focus.id,
      native_id: focus.identity.native_id,
    },
    resources: resourceIDs.map((id) => {
      const asset = assetsByID.get(id);
      return asset
        ? topologyResource(
            asset,
            resourceKinds.get(asset.resource_kind_id),
            relationships,
            assetsByID,
            locale,
          )
        : missingTopologyResource(id);
    }),
    edges: [
      ...relationships.map((relationship) => ({
        key: `relationship:${relationship.id}`,
        source_key: relationship.source_asset_id,
        target_key: relationship.target_asset_id,
        kind: "relationship" as const,
        relation: relationship.type,
        metadata: {
          source: relationship.source,
          confidence: relationship.confidence,
          evidence: relationship.evidence,
          graph_revision: relationship.graph_revision,
        },
      })),
      ...lifecycleBindings.map((binding, index) => ({
        key: `lifecycle:${
          binding.id ||
          `${binding.controller_asset_id}:${binding.managed_asset_id}:${index}`
        }`,
        source_key: binding.controller_asset_id,
        target_key: binding.managed_asset_id,
        kind: "lifecycle" as const,
        relation: binding.cleanup_policy,
        metadata: {
          authority: binding.authority,
          ownership: binding.ownership,
          cleanup_policy: binding.cleanup_policy,
          confidence: binding.confidence,
          direct_cleanup_allowed: binding.direct_cleanup_allowed,
          evidence_source: binding.evidence_source,
          evidence: binding.evidence,
          graph_revision: binding.graph_revision,
        },
      })),
    ],
  };
}

function topologyResource(
  asset: Asset,
  kind: ResourceKind | undefined,
  relationships: readonly Relationship[],
  assetsByID: ReadonlyMap<string, Asset>,
  locale: string,
): TopologyResource {
  const selectable =
    asset.capabilities.includes("actionable") &&
    !asset.closed_at &&
    !asset.deleted_at;
  const consoleLinkValues = scalarValues(asset.normalized);
  const location = asset.location?.trim();
  if (location && location.toLowerCase() !== "global") {
    consoleLinkValues.regionId ??= location;
  }
  const parentNativeIDs = new Set(
    relationships.flatMap((relationship) => {
      if (
        relationship.type !== "member_of" ||
        relationship.source_asset_id !== asset.id
      ) {
        return [];
      }
      const parentNativeID = assetsByID
        .get(relationship.target_asset_id)
        ?.identity.native_id.trim();
      return parentNativeID ? [parentNativeID] : [];
    }),
  );
  if (parentNativeIDs.size === 1) {
    consoleLinkValues.parentId = [...parentNativeIDs][0];
  }

  return {
    key: asset.id,
    asset_id: asset.id,
    dirty: asset.dirty,
    resource_kind_id: asset.resource_kind_id,
    name: asset.name || asset.identity.native_id || asset.id,
    native_id: asset.identity.native_id,
    type_name: resourceTypeName(
      kind?.native_type || asset.identity.native_type,
      kind?.display_name || asset.identity.native_type,
      kind?.display_names,
      locale,
    ),
    type_names: kind?.display_names,
    icon: kind?.icon,
    class: kind?.class,
    console_link_values:
      Object.keys(consoleLinkValues).length > 0 ? consoleLinkValues : undefined,
    domain: resourceDomain(kind?.class),
    state: asset.state,
    finding_count: 0,
    actionable: selectable,
    cleanup: selectable
      ? {
          selectable: true,
          selector_kind: "asset",
          selector_key: asset.id,
          confirmation: "confirm",
          potential_blockers: 0,
        }
      : { selectable: false, potential_blockers: 0 },
  };
}

function missingTopologyResource(id: string): TopologyResource {
  return {
    key: id,
    asset_id: id,
    resource_kind_id: `unknown:${id}`,
    name: id,
    native_id: id,
    type_name: "—",
    domain: "unknown",
    finding_count: 0,
    actionable: false,
    cleanup: { selectable: false, potential_blockers: 0 },
  };
}

function scalarValues(
  values: Record<string, unknown> | undefined,
): Record<string, string> {
  return Object.fromEntries(
    Object.entries(values ?? {}).flatMap(([key, value]) => {
      if (
        typeof value !== "string" &&
        typeof value !== "number" &&
        typeof value !== "boolean"
      ) {
        return [];
      }
      return [[key, String(value)] as const];
    }),
  );
}

function resourceDomain(
  resourceClass: string | undefined,
): TopologyResourceDomain {
  if (resourceClass?.startsWith("network.")) return "network";
  if (
    resourceClass?.startsWith("compute.") ||
    resourceClass?.startsWith("container.")
  ) {
    return "compute";
  }
  if (resourceClass?.startsWith("storage.")) return "storage";
  return "unknown";
}
