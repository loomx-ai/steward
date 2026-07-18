import { describe, expect, it } from "vitest";
import type { Asset, ResourceKind, Scope, TopologyView } from "@/api/types";
import { buildPanoramaSearchResults, panoramaSearchHighlight } from "./search";

const regionScope: Scope = {
  id: "scope-region",
  connection_id: "connection-a",
  kind: "region",
  native_id: "cn-hangzhou",
  name: "杭州",
  created_at: "2026-07-31T00:00:00Z",
  updated_at: "2026-07-31T00:00:00Z",
};

const ecs: Asset = {
  id: "asset-ecs",
  identity: {
    provider: "alicloud",
    partition: "aliyun",
    connection_id: "connection-a",
    native_type: "ACS::ECS::Instance",
    native_id: "i-production",
  },
  scope_id: "scope-zone",
  resource_kind_id: "ecs",
  name: "生产 ECS",
  capabilities: [],
  normalized: { vpc_id: "vpc-production" },
  first_seen_at: "2026-07-31T00:00:00Z",
  last_seen_at: "2026-07-31T00:00:00Z",
};

const kinds = new Map<string, ResourceKind>([
  [
    "ecs",
    {
      id: "ecs",
      provider: "alicloud",
      native_type: "ACS::ECS::Instance",
      class: "compute.instance",
      capabilities: [],
      display_name: "ECS Instance",
      display_names: { "zh-CN": "ECS 实例" },
      bundle_revision: "revision-a",
    },
  ],
]);

describe("panorama search projection", () => {
  it("resolves authoritative Region ancestry and routes resources to their VPC canvas", () => {
    const results = buildPanoramaSearchResults({
      term: "生产",
      assets: [ecs],
      regions: [],
      scopes: [
        regionScope,
        {
          ...regionScope,
          id: "scope-zone",
          parent_id: regionScope.id,
          kind: "zone",
          native_id: "cn-hangzhou-h",
        },
      ],
      kinds,
      locale: "zh-CN",
      regionTypeName: "地域",
      scope: {
        kind: "account",
        focusKey: "",
        pathname: "/panorama",
      },
    });

    expect(results).toEqual([
      expect.objectContaining({
        kind: "resource",
        name: "生产 ECS",
        resourceId: "i-production",
        typeName: "ECS 实例",
        regionId: "cn-hangzhou",
        vpcId: "vpc-production",
        pathname: "/panorama/regions/cn-hangzhou/vpcs/vpc-production",
      }),
    ]);
  });

  it("highlights the containing Region or VPC but never highlights an off-canvas VPC resource", () => {
    const [result] = buildPanoramaSearchResults({
      term: "i-production",
      assets: [ecs],
      regions: [],
      scopes: [
        regionScope,
        {
          ...regionScope,
          id: "scope-zone",
          parent_id: regionScope.id,
          kind: "zone",
        },
      ],
      kinds,
      locale: "en-US",
      regionTypeName: "Region",
      scope: {
        kind: "account",
        focusKey: "",
        pathname: "/panorama",
      },
    });
    const account: TopologyView = {
      kind: "account",
      regions: [
        {
          key: "region-hangzhou",
          name: "Hangzhou",
          native_id: "cn-hangzhou",
          resource_count: 1,
          cleanup: { selectable: false, potential_blockers: 0 },
        },
      ],
    };
    const otherVPC: TopologyView = {
      kind: "vpc",
      region: {
        key: "region-hangzhou",
        name: "Hangzhou",
        native_id: "cn-hangzhou",
      },
      vpc: {
        key: "asset-vpc-other",
        name: "Other",
        native_id: "vpc-other",
      },
      public_resource_keys: [],
      vswitches: [],
      resources: [],
      edges: [],
    };
    const region: TopologyView = {
      kind: "region",
      region: {
        key: "region-hangzhou",
        name: "Hangzhou",
        native_id: "cn-hangzhou",
      },
      public_resources: {
        key: "region-public-hangzhou",
        name: "Public resources",
        resource_count: 0,
        cleanup: { selectable: false, potential_blockers: 0 },
      },
      vpcs: [
        {
          key: "vpc-production-key",
          name: "Production",
          native_id: "vpc-production",
          resource_count: 1,
          cleanup: { selectable: false, potential_blockers: 0 },
        },
      ],
    };

    expect(panoramaSearchHighlight(account, result)).toEqual({
      nodeKey: "region-hangzhou",
      scope: false,
    });
    expect(panoramaSearchHighlight(region, result)).toEqual({
      nodeKey: "vpc-production-key",
      scope: false,
    });
    expect(panoramaSearchHighlight(otherVPC, result)).toBeUndefined();
  });

  it("limits results to the current Region and VPC canvas", () => {
    const otherRegionScope: Scope = {
      ...regionScope,
      id: "scope-beijing",
      native_id: "cn-beijing",
      name: "北京",
    };
    const beijingAsset: Asset = {
      ...ecs,
      id: "asset-beijing",
      identity: {
        ...ecs.identity,
        native_id: "i-beijing",
      },
      scope_id: otherRegionScope.id,
      name: "北京 ECS",
      normalized: { vpc_id: "vpc-beijing" },
    };
    const regionResults = buildPanoramaSearchResults({
      term: "ecs",
      assets: [ecs, beijingAsset],
      regions: [],
      scopes: [regionScope, otherRegionScope],
      kinds,
      locale: "zh-CN",
      regionTypeName: "地域",
      scope: {
        kind: "region",
        regionId: "cn-beijing",
        focusKey: "region:test",
        pathname: "/panorama/regions/cn-beijing",
      },
    });
    const vpcResults = buildPanoramaSearchResults({
      term: "ecs",
      assets: [ecs, beijingAsset],
      regions: [],
      scopes: [regionScope, otherRegionScope],
      kinds,
      locale: "zh-CN",
      regionTypeName: "地域",
      scope: {
        kind: "vpc",
        regionId: "cn-beijing",
        vpcId: "vpc-other",
        focusKey: "vpc:test",
        pathname: "/panorama/regions/cn-beijing/vpcs/vpc-other",
      },
    });

    expect(regionResults.map((result) => result.resourceId)).toEqual([
      "i-beijing",
    ]);
    expect(vpcResults).toEqual([]);
  });

  it("orders resources by infrastructure priority before match strength", () => {
    const priorityKinds = new Map<string, ResourceKind>(
      [
        ["vpc", "ACS::VPC::VPC", "network.vpc"],
        ["vswitch", "ACS::VPC::VSwitch", "network.subnet"],
        ["ecs", "ACS::ECS::Instance", "compute.instance"],
        ["sg", "ACS::ECS::SecurityGroup", "network.security_group"],
        ["disk", "ACS::ECS::Disk", "storage.block"],
        ["bucket", "ACS::OSS::Bucket", "storage.bucket"],
        ["route-table", "ACS::VPC::RouteTable", "network.route_table"],
      ].map(([id, nativeType, resourceClass]) => [
        id,
        {
          id,
          provider: "alicloud",
          native_type: nativeType,
          class: resourceClass,
          capabilities: [],
          display_name: id,
          bundle_revision: "revision-a",
        },
      ]),
    );
    const asset = (
      id: string,
      nativeType: string,
      nativeId: string,
    ): Asset => ({
      ...ecs,
      id: `asset-${id}`,
      identity: {
        ...ecs.identity,
        native_type: nativeType,
        native_id: nativeId,
      },
      scope_id: regionScope.id,
      resource_kind_id: id,
      name: "ros-test-beijing",
      normalized: id === "vswitch" ? { vpc_id: "vpc-priority" } : undefined,
    });
    const results = buildPanoramaSearchResults({
      term: "ros-test-beijing",
      assets: [
        asset("bucket", "ACS::OSS::Bucket", "ros-test-beijing"),
        asset("sg", "ACS::ECS::SecurityGroup", "sg-priority"),
        asset("route-table", "ACS::VPC::RouteTable", "rtb-priority"),
        asset("vpc", "ACS::VPC::VPC", "vpc-priority"),
        asset("disk", "ACS::ECS::Disk", "d-priority"),
        asset("ecs", "ACS::ECS::Instance", "i-priority"),
        asset("vswitch", "ACS::VPC::VSwitch", "vsw-priority"),
      ],
      regions: [],
      scopes: [regionScope],
      kinds: priorityKinds,
      locale: "en-US",
      regionTypeName: "Region",
      scope: {
        kind: "region",
        regionId: "cn-hangzhou",
        focusKey: "region:test",
        pathname: "/panorama/regions/cn-hangzhou",
      },
    });

    expect(results.map((result) => result.resourceId)).toEqual([
      "vpc-priority",
      "vsw-priority",
      "i-priority",
      "sg-priority",
      "d-priority",
      "ros-test-beijing",
      "rtb-priority",
    ]);
  });
});
