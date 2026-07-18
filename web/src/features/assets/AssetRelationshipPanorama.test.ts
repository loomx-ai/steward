import { describe, expect, it } from "vitest";
import type {
  Asset,
  LifecycleBinding,
  Relationship,
  ResourceKind,
} from "@/api/types";
import { buildAssetRelationshipTopologyView } from "./AssetRelationshipPanorama";

const focus: Asset = {
  id: "focus",
  identity: {
    provider: "alicloud",
    partition: "public",
    connection_id: "connection-a",
    native_type: "ACS::CEN::TransitRouter",
    native_id: "tr-focus",
  },
  scope_id: "scope-a",
  resource_kind_id: "transit-router",
  name: "Current router",
  location: "cn-beijing",
  capabilities: ["actionable"],
  normalized: { transitRouterId: "tr-focus", ignored: { nested: true } },
  first_seen_at: "2026-08-01T00:00:00Z",
  last_seen_at: "2026-08-05T00:00:00Z",
};

const parent: Asset = {
  ...focus,
  id: "parent",
  identity: {
    ...focus.identity,
    native_type: "ACS::CEN::CenInstance",
    native_id: "cen-parent",
  },
  resource_kind_id: "cen-instance",
  name: "Parent CEN",
};

const relationships: Relationship[] = [
  {
    id: "membership",
    source_asset_id: "focus",
    target_asset_id: "parent",
    type: "member_of",
    source: "provider",
    confidence: 1,
    graph_revision: "graph-1",
    observed_at: "2026-08-05T00:00:00Z",
  },
  {
    id: "missing-peer",
    source_asset_id: "missing",
    target_asset_id: "focus",
    type: "uses",
    source: "provider",
    confidence: 0.8,
    graph_revision: "graph-1",
    observed_at: "2026-08-05T00:00:00Z",
  },
];

const bindings: LifecycleBinding[] = [
  {
    id: "binding-1",
    controller_asset_id: "parent",
    managed_asset_id: "focus",
    authority: "authoritative",
    ownership: "exclusive",
    cleanup_policy: "delegate",
    confidence: 1,
  },
];

const kinds = new Map<string, ResourceKind>([
  [
    "transit-router",
    {
      id: "transit-router",
      provider: "alicloud",
      native_type: "ACS::CEN::TransitRouter",
      class: "network.router",
      scope_kinds: ["region"],
      capabilities: ["actionable"],
      display_name: "Transit Router",
      display_names: { "zh-CN": "CEN 转发路由器" },
      console_link_template:
        "https://example.com/{regionId}/{parentId}/{transitRouterId}",
      bundle_revision: "bundle-1",
    },
  ],
]);

describe("asset relationship panorama projection", () => {
  it("projects relationship and lifecycle data into the shared topology canvas model", () => {
    const view = buildAssetRelationshipTopologyView({
      focus,
      assets: [parent],
      relationships,
      lifecycleBindings: bindings,
      resourceKinds: kinds,
      locale: "zh-CN",
    });

    expect(view.context).toEqual({
      key: "focus",
      name: "Current router",
      native_id: "tr-focus",
    });
    expect(view.resources.map((resource) => resource.key)).toEqual([
      "focus",
      "parent",
      "missing",
    ]);
    expect(view.resources[0]).toMatchObject({
      type_name: "CEN 转发路由器",
      domain: "network",
      actionable: true,
      console_link_values: {
        transitRouterId: "tr-focus",
        regionId: "cn-beijing",
        parentId: "cen-parent",
      },
      cleanup: {
        selectable: true,
        selector_kind: "asset",
        selector_key: "focus",
      },
    });
    expect(view.resources[2]).toMatchObject({
      name: "missing",
      domain: "unknown",
      actionable: false,
    });
    expect(view.edges).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          key: "relationship:membership",
          source_key: "focus",
          target_key: "parent",
          kind: "relationship",
          relation: "member_of",
        }),
        expect.objectContaining({
          key: "lifecycle:binding-1",
          source_key: "parent",
          target_key: "focus",
          kind: "lifecycle",
          relation: "delegate",
        }),
      ]),
    );
  });
});
