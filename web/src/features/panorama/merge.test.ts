import { expect, it } from "vitest";
import type {
  TopologyResponse,
  TopologyResource,
  VPCTopologyView,
} from "@/api/types";
import { STALE_TOPOLOGY_WARNING_CODE, mergeTopologyPages } from "./merge";

const revision = {
  inventory: "inventory-a",
  graph: "graph-a",
  spec_bundle: "spec-a",
  projected_at: "2026-07-24T00:00:00Z",
};
const coverage = { status: "complete", failed_shards: 0 };
const cleanup = { selectable: true, potential_blockers: 0 };

function resource(
  key: string,
  externalRelations: TopologyResource["external_relations"] = [],
): TopologyResource {
  return {
    key,
    asset_id: key,
    resource_kind_id: "ecs",
    name: key,
    native_id: key,
    type_name: "ECS",
    domain: "compute",
    finding_count: 0,
    actionable: true,
    external_relations: externalRelations,
    cleanup,
  };
}

function page(
  view: VPCTopologyView,
  options: Pick<TopologyResponse, "truncated"> &
    Partial<Pick<TopologyResponse, "next_cursor" | "warnings" | "revision">>,
): TopologyResponse {
  return {
    revision: options.revision ?? revision,
    coverage,
    view,
    warnings: options.warnings,
    next_cursor: options.next_cursor,
    truncated: options.truncated,
  };
}

it("merges split VPC resources, edges, external relations, vSwitches, and warnings", () => {
  const base = {
    kind: "vpc" as const,
    region: {
      key: "region-a",
      name: "杭州",
      native_id: "cn-hangzhou",
    },
    vpc: { key: "vpc-a", name: "生产网络", native_id: "vpc-a" },
  };
  const external = {
    key: "external-a",
    kind: "relationship" as const,
    relation: "uses",
    direction: "outgoing",
    target_id: "external",
    target_name: "共享服务",
    target_type: "service",
  };
  const first = page(
    {
      ...base,
      public_resource_keys: [],
      vswitches: [
        {
          key: "vsw-a",
          name: "交换机 A",
          native_id: "vsw-a",
          zone: "zone-a",
          resource_count: 2,
          resource_keys: ["ecs-a"],
        },
        {
          key: "vsw-empty",
          name: "空交换机",
          native_id: "vsw-empty",
          resource_count: 0,
          resource_keys: [],
        },
      ],
      resources: [resource("ecs-a", [external])],
      edges: [],
    },
    {
      warnings: [
        {
          code: "endpoint_unavailable",
          relation_key: "relation-missing",
          message: "missing",
        },
      ],
      next_cursor: "cursor-2",
      truncated: true,
    },
  );
  const second = page(
    {
      ...base,
      public_resource_keys: ["security-group"],
      vswitches: [
        {
          key: "vsw-a",
          name: "交换机 A",
          native_id: "vsw-a",
          zone: "zone-a",
          resource_count: 2,
          resource_keys: ["ecs-a", "ecs-b", "ecs-b"],
        },
        {
          key: "vsw-empty",
          name: "空交换机",
          native_id: "vsw-empty",
          resource_count: 0,
          resource_keys: [],
        },
      ],
      resources: [resource("ecs-b"), resource("security-group")],
      edges: [
        {
          key: "edge-a",
          source_key: "ecs-a",
          target_key: "ecs-b",
          kind: "relationship",
          relation: "uses",
        },
        {
          key: "edge-a",
          source_key: "ecs-a",
          target_key: "ecs-b",
          kind: "relationship",
          relation: "uses",
        },
      ],
    },
    {
      warnings: [
        {
          code: "membership_unknown",
          visible_asset_id: "security-group",
          message: "unknown",
        },
      ],
      truncated: false,
    },
  );

  const merged = mergeTopologyPages([first, second]);
  expect(merged.truncated).toBe(false);
  expect(merged.next_cursor).toBeUndefined();
  expect(merged.warnings).toHaveLength(2);
  expect(merged.view.kind).toBe("vpc");
  const view = merged.view as VPCTopologyView;
  expect(view.resources.map((value) => value.key)).toEqual([
    "ecs-a",
    "ecs-b",
    "security-group",
  ]);
  expect(view.resources[0]?.external_relations).toEqual([external]);
  expect(view.edges).toHaveLength(1);
  expect(view.public_resource_keys).toEqual(["security-group"]);
  expect(view.vswitches.map((value) => value.key)).toEqual([
    "vsw-a",
    "vsw-empty",
  ]);
  expect(view.vswitches[0]?.resource_keys).toEqual(["ecs-a", "ecs-b"]);
  expect(view.vswitches[1]?.resource_keys).toEqual([]);
});

it("returns a stale-revision result without combining snapshots", () => {
  const view: VPCTopologyView = {
    kind: "vpc",
    region: {
      key: "region-a",
      name: "杭州",
      native_id: "cn-hangzhou",
    },
    vpc: { key: "vpc-a", name: "生产网络", native_id: "vpc-a" },
    public_resource_keys: [],
    vswitches: [],
    resources: [resource("ecs-a")],
    edges: [],
  };
  const first = page(view, { next_cursor: "cursor-2", truncated: true });
  const second = page(
    { ...view, resources: [resource("ecs-b")] },
    {
      revision: { ...revision, inventory: "inventory-b" },
      truncated: false,
    },
  );

  const merged = mergeTopologyPages([first, second]);
  expect(merged.truncated).toBe(true);
  expect(merged.next_cursor).toBeUndefined();
  expect(merged.warnings).toContainEqual(
    expect.objectContaining({ code: STALE_TOPOLOGY_WARNING_CODE }),
  );
  expect(merged.view).toEqual(first.view);
});

it("merges pages when only their projection timestamps differ", () => {
  const view: VPCTopologyView = {
    kind: "vpc",
    region: {
      key: "region-a",
      name: "杭州",
      native_id: "cn-hangzhou",
    },
    vpc: { key: "vpc-a", name: "生产网络", native_id: "vpc-a" },
    public_resource_keys: [],
    vswitches: [],
    resources: [resource("ecs-a")],
    edges: [],
  };
  const first = page(view, { next_cursor: "cursor-2", truncated: true });
  const second = page(
    { ...view, resources: [resource("ecs-b")] },
    {
      revision: {
        ...revision,
        projected_at: "2026-07-24T00:00:01Z",
      },
      truncated: false,
    },
  );

  const merged = mergeTopologyPages([first, second]);
  expect(merged.truncated).toBe(false);
  expect(merged.warnings).not.toContainEqual(
    expect.objectContaining({ code: STALE_TOPOLOGY_WARNING_CODE }),
  );
  expect(merged.revision.projected_at).toBe(revision.projected_at);
  expect(
    merged.view.kind === "vpc"
      ? merged.view.resources.map((value) => value.key)
      : [],
  ).toEqual(["ecs-a", "ecs-b"]);
});
