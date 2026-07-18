import type {
  ResourceGraphTopologyView,
  TopologyProjectionWarning,
  TopologyResource,
  TopologyResponse,
  TopologyRevision,
  TopologyView,
  TopologyVSwitch,
  VPCTopologyView,
} from "@/api/types";

export const STALE_TOPOLOGY_WARNING_CODE = "topology_revision_stale";

export function mergeTopologyPages(
  pages: readonly TopologyResponse[],
): TopologyResponse {
  const first = pages[0];
  if (!first) throw new Error("at least one topology page is required");

  for (const page of pages.slice(1)) {
    if (!sameRevision(first.revision, page.revision)) {
      return staleRevisionResult(first);
    }
    if (
      first.view.kind !== page.view.kind ||
      focusContext(first.view) !== focusContext(page.view)
    ) {
      throw new Error("topology pages do not share one focus context");
    }
  }

  const last = pages.at(-1) ?? first;
  return {
    ...first,
    view: mergeViews(pages.map((page) => page.view)),
    warnings: uniqueBy(
      pages.flatMap((page) => page.warnings ?? []),
      warningKey,
    ),
    next_cursor: last.next_cursor,
    truncated: last.truncated,
  };
}

export function isStaleTopologyResult(response: TopologyResponse): boolean {
  return (
    response.warnings?.some(
      (warning) => warning.code === STALE_TOPOLOGY_WARNING_CODE,
    ) ?? false
  );
}

function mergeViews(views: readonly TopologyView[]): TopologyView {
  const first = views[0];
  if (!first) throw new Error("at least one topology view is required");
  if (first.kind === "account" || first.kind === "region") return first;

  if (first.kind === "resource_graph") {
    const graphViews = views as readonly ResourceGraphTopologyView[];
    return {
      ...first,
      resources: mergeResources(graphViews.flatMap((view) => view.resources)),
      edges: uniqueBy(
        graphViews.flatMap((view) => view.edges),
        (edge) => edge.key,
      ),
    };
  }

  const vpcViews = views as readonly VPCTopologyView[];
  return {
    ...first,
    public_resource_keys: unique(
      vpcViews.flatMap((view) => view.public_resource_keys),
    ),
    vswitches: mergeVSwitches(vpcViews),
    resources: mergeResources(vpcViews.flatMap((view) => view.resources)),
    edges: uniqueBy(
      vpcViews.flatMap((view) => view.edges),
      (edge) => edge.key,
    ),
  };
}

function mergeResources(resources: readonly TopologyResource[]) {
  const merged = new Map<string, TopologyResource>();
  for (const resource of resources) {
    const current = merged.get(resource.key);
    if (!current) {
      merged.set(resource.key, {
        ...resource,
        external_relations: uniqueBy(
          resource.external_relations ?? [],
          (relation) => relation.key,
        ),
      });
      continue;
    }
    merged.set(resource.key, {
      ...current,
      external_relations: uniqueBy(
        [
          ...(current.external_relations ?? []),
          ...(resource.external_relations ?? []),
        ],
        (relation) => relation.key,
      ),
    });
  }
  return [...merged.values()];
}

function mergeVSwitches(views: readonly VPCTopologyView[]): TopologyVSwitch[] {
  const merged = new Map<string, TopologyVSwitch>();
  for (const view of views) {
    for (const vswitch of view.vswitches) {
      const current = merged.get(vswitch.key);
      merged.set(vswitch.key, {
        ...(current ?? vswitch),
        resource_keys: unique([
          ...(current?.resource_keys ?? []),
          ...vswitch.resource_keys,
        ]),
      });
    }
  }
  return [...merged.values()];
}

function staleRevisionResult(first: TopologyResponse): TopologyResponse {
  const warning: TopologyProjectionWarning = {
    code: STALE_TOPOLOGY_WARNING_CODE,
    message: "Topology revision changed while loading cursor pages.",
  };
  return {
    ...first,
    warnings: uniqueBy([...(first.warnings ?? []), warning], warningKey),
    next_cursor: undefined,
    truncated: true,
  };
}

function sameRevision(
  left: TopologyRevision,
  right: TopologyRevision,
): boolean {
  return (
    left.inventory === right.inventory &&
    left.graph === right.graph &&
    left.spec_bundle === right.spec_bundle
  );
}

function focusContext(view: TopologyView): string {
  switch (view.kind) {
    case "account":
      return "account";
    case "region":
      return `region:${view.region.key}:${view.region.native_id ?? ""}`;
    case "resource_graph":
      return `resource_graph:${view.context.key}:${view.context.native_id ?? ""}`;
    case "vpc":
      return `vpc:${view.region.key}:${view.vpc.key}:${view.vpc.native_id ?? ""}`;
  }
}

function warningKey(warning: TopologyProjectionWarning): string {
  return [
    warning.code,
    warning.relation_key ?? "",
    warning.visible_asset_id ?? "",
    warning.message,
  ].join(":");
}

function unique<T>(values: readonly T[]): T[] {
  return [...new Set(values)];
}

function uniqueBy<T>(values: readonly T[], key: (value: T) => string): T[] {
  return [...new Map(values.map((value) => [key(value), value])).values()];
}
