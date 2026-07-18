import { expect, it } from "vitest";
import type {
  Asset,
  TopologyEntrySummary,
  TopologyResource,
  TopologyVSwitch,
} from "@/api/types";
import {
  assetCleanupTarget,
  batchCleanupTarget,
  entryCleanupTarget,
  resourceCleanupTarget,
  vSwitchCleanupTarget,
} from "./cleanupTargets";

function asset(overrides: Partial<Asset> = {}): Asset {
  return {
    id: "asset-123",
    identity: {
      provider: "alicloud",
      partition: "aliyun",
      connection_id: "connection-a",
      native_type: "ALIYUN::ECS::Instance",
      native_id: "i-123",
    },
    scope_id: "scope-hangzhou",
    resource_kind_id: "ecs-instance",
    name: "production-api",
    capabilities: ["inventory", "cleanup"],
    first_seen_at: "2026-08-01T00:00:00Z",
    last_seen_at: "2026-08-01T01:00:00Z",
    ...overrides,
  };
}

function resource(overrides: Partial<TopologyResource> = {}): TopologyResource {
  return {
    key: "resource:ecs:i-123",
    asset_id: "asset-123",
    resource_kind_id: "ecs-instance",
    name: "production-api",
    native_id: "i-123",
    type_name: "ECS Instance",
    domain: "compute",
    finding_count: 0,
    actionable: false,
    cleanup: {
      selectable: false,
      potential_blockers: 0,
    },
    ...overrides,
  };
}

function entry(
  overrides: Partial<TopologyEntrySummary> = {},
): TopologyEntrySummary {
  return {
    key: "region:cn-hangzhou",
    name: "Hangzhou",
    native_id: "cn-hangzhou",
    resource_count: 7,
    cleanup: {
      selectable: false,
      potential_blockers: 0,
    },
    ...overrides,
  };
}

it("always turns a visible resource into its stable asset selector", () => {
  const target = resourceCleanupTarget({
    connectionId: "connection-a",
    resource: resource({
      cleanup: {
        selectable: false,
        selector_kind: "group",
        selector_key: "ignored-group",
        potential_blockers: 3,
      },
    }),
    ancestryKeys: ["region:cn-hangzhou", "vpc:vpc-a"],
    locationContext: {
      region: {
        key: "region:cn-hangzhou",
        name: "华东1（杭州）",
        native_id: "cn-hangzhou",
      },
      vpc: {
        key: "vpc:vpc-a",
        name: "生产网络",
        native_id: "vpc-a",
      },
    },
  });

  expect(target).toEqual({
    key: "asset:asset-123",
    kind: "resource",
    connectionId: "connection-a",
    displayName: "production-api",
    selector: {
      kind: "asset",
      asset_id: "asset-123",
      display_name: "production-api",
    },
    ancestryKeys: ["region:cn-hangzhou", "vpc:vpc-a", "asset:asset-123"],
    locationContext: {
      region: {
        key: "region:cn-hangzhou",
        name: "华东1（杭州）",
        native_id: "cn-hangzhou",
      },
      vpc: {
        key: "vpc:vpc-a",
        name: "生产网络",
        native_id: "vpc-a",
      },
    },
  });
});

it("turns a resource-list asset into the same stable cleanup target", () => {
  expect(
    assetCleanupTarget({
      connectionId: "connection-a",
      asset: asset(),
      ancestryKeys: ["region:hangzhou"],
      locationContext: {
        region: {
          key: "region:hangzhou",
          name: "华东1（杭州）",
          native_id: "cn-hangzhou",
        },
      },
    }),
  ).toEqual({
    key: "asset:asset-123",
    kind: "resource",
    connectionId: "connection-a",
    displayName: "production-api",
    selector: {
      kind: "asset",
      asset_id: "asset-123",
      display_name: "production-api",
    },
    ancestryKeys: ["region:hangzhou", "asset:asset-123"],
    locationContext: {
      region: {
        key: "region:hangzhou",
        name: "华东1（杭州）",
        native_id: "cn-hangzhou",
      },
    },
  });
});

it("turns a vSwitch with an asset id into a selectable cleanup target", () => {
  const vSwitch: TopologyVSwitch = {
    key: "vswitch:vsw-a",
    asset_id: "asset-vsw-a",
    name: "application-zone",
    native_id: "vsw-a",
    zone: "cn-hangzhou-h",
    resource_count: 7,
    resource_keys: [],
  };

  expect(
    vSwitchCleanupTarget({
      connectionId: "connection-a",
      vSwitch,
      ancestryKeys: ["region:cn-hangzhou", "vpc:vpc-a"],
    }),
  ).toEqual({
    key: "asset:asset-vsw-a",
    kind: "resource",
    connectionId: "connection-a",
    displayName: "application-zone",
    selector: {
      kind: "asset",
      asset_id: "asset-vsw-a",
      display_name: "application-zone",
    },
    ancestryKeys: ["region:cn-hangzhou", "vpc:vpc-a", "asset:asset-vsw-a"],
  });
  expect(
    vSwitchCleanupTarget({
      connectionId: "connection-a",
      vSwitch: { ...vSwitch, asset_id: undefined },
      ancestryKeys: [],
    }),
  ).toBeNull();
});

it("preserves a projected region scope and selects its descendants", () => {
  const target = entryCleanupTarget({
    connectionId: "connection-a",
    entry: entry({
      cleanup: {
        selectable: false,
        selector_kind: "scope",
        selector_key: "scope-cn-hangzhou",
        potential_blockers: 0,
      },
    }),
    kind: "region",
    ancestryKeys: [],
  });

  expect(target).toEqual({
    key: "region:cn-hangzhou",
    kind: "region",
    connectionId: "connection-a",
    displayName: "Hangzhou",
    selector: {
      kind: "scope",
      connection_id: "connection-a",
      scope_id: "scope-cn-hangzhou",
      scope_kind: "region",
      descendants: true,
      display_name: "Hangzhou",
    },
    ancestryKeys: ["region:cn-hangzhou"],
    resourceCount: 7,
  });
});

it("falls a region without a selector back to its stable group key", () => {
  const target = entryCleanupTarget({
    connectionId: "connection-a",
    entry: entry(),
    kind: "region",
    ancestryKeys: [],
  });

  expect(target.selector).toEqual({
    kind: "group",
    connection_id: "connection-a",
    group_key: "region:cn-hangzhou",
    display_name: "Hangzhou",
  });
});

it("uses a VPC projected group selector and falls back to its entry key", () => {
  const projected = entryCleanupTarget({
    connectionId: "connection-a",
    entry: entry({
      key: "vpc:vpc-a",
      name: "Application VPC",
      cleanup: {
        selectable: false,
        selector_kind: "group",
        selector_key: "group:vpc-a",
        potential_blockers: 0,
      },
    }),
    kind: "vpc",
    ancestryKeys: ["region:cn-hangzhou"],
    locationContext: {
      region: {
        key: "region:cn-hangzhou",
        name: "华东1（杭州）",
        native_id: "cn-hangzhou",
      },
    },
  });
  const fallback = entryCleanupTarget({
    connectionId: "connection-a",
    entry: entry({
      key: "vpc:vpc-b",
      name: "Data VPC",
    }),
    kind: "vpc",
    ancestryKeys: ["region:cn-hangzhou"],
  });

  expect(projected.selector).toMatchObject({
    kind: "group",
    group_key: "group:vpc-a",
  });
  expect(projected.ancestryKeys).toEqual(["region:cn-hangzhou", "vpc:vpc-a"]);
  expect(projected.locationContext).toEqual({
    region: {
      key: "region:cn-hangzhou",
      name: "华东1（杭州）",
      native_id: "cn-hangzhou",
    },
  });
  expect(fallback.selector).toMatchObject({
    kind: "group",
    group_key: "vpc:vpc-b",
  });
});

it("creates one resource batch with deduplicated member asset selectors", () => {
  const target = batchCleanupTarget({
    connectionId: "connection-a",
    key: "stack:ecs",
    displayName: "ECS Instances",
    resources: [
      resource(),
      resource({
        key: "resource:ecs:i-123-copy",
        name: "duplicate projection",
      }),
      resource({
        key: "resource:ecs:i-456",
        asset_id: "asset-456",
        name: "worker",
        native_id: "i-456",
      }),
    ],
    ancestryKeys: ["region:cn-hangzhou", "vpc:vpc-a"],
  });

  expect(target).toEqual({
    key: "stack:ecs",
    kind: "resource_batch",
    connectionId: "connection-a",
    displayName: "ECS Instances",
    selector: [
      {
        kind: "asset",
        asset_id: "asset-123",
        display_name: "production-api",
      },
      {
        kind: "asset",
        asset_id: "asset-456",
        display_name: "worker",
      },
    ],
    ancestryKeys: ["region:cn-hangzhou", "vpc:vpc-a", "stack:ecs"],
    memberAssetIds: ["asset-123", "asset-456"],
    resourceCount: 2,
  });
});
