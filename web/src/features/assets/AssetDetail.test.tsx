import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";
import type {
  Asset,
  ConnectionRegion,
  LifecycleBinding,
  Relationship,
} from "@/api/types";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import {
  AssetDetailActions,
  AssetResourceIDFact,
  assetResourceKindName,
  buildResourceRelations,
} from "./AssetDetail";
import { assetRegionLabel, resolveAssetRegion } from "./assetRegions";

const regions: ConnectionRegion[] = [
  {
    id: "region-wulanchabu",
    connection_id: "connection-a",
    region_id: "cn-wulanchabu",
    name: "华北 6（乌兰察布）",
    discovered_name: "华北 6（乌兰察布）",
    origin: "api",
    lifecycle: "active",
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
  },
  {
    id: "region-wulanchabu-gic",
    connection_id: "connection-a",
    region_id: "cn-wulanchabu-gic",
    name: "乌兰察布政企云",
    origin: "manual",
    lifecycle: "active",
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
  },
];

const deletedAsset: Asset = {
  id: "asset-deleted",
  identity: {
    provider: "alicloud",
    partition: "public",
    connection_id: "connection-a",
    native_type: "ACS::ECS::Instance",
    native_id: "i-deleted",
  },
  scope_id: "scope-a",
  resource_kind_id: "ecs-instance",
  capabilities: ["actionable"],
  first_seen_at: "2026-08-01T00:00:00Z",
  last_seen_at: "2026-08-04T00:00:00Z",
  closed_at: "2026-08-04T01:00:00Z",
  deleted_at: "2026-08-04T01:00:00Z",
};

function renderActions(
  asset: Asset,
  lifecycleBindings: LifecycleBinding[] = [],
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <LocaleProvider>
          <dl>
            <AssetResourceIDFact
              asset={asset}
              lifecycleBindings={lifecycleBindings}
            />
          </dl>
          <div>
            <AssetDetailActions
              asset={asset}
              connectionId="connection-a"
              consoleURL="https://example.com/resource"
            />
          </div>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("deleted resource detail actions", () => {
  it("shows a deleted status and hides every resource action", () => {
    localStorage.setItem("steward.locale", "en-US");

    renderActions(deletedAsset);

    expect(screen.getByRole("term")).toHaveTextContent("Resource ID");
    expect(screen.getByText("i-deleted")).toBeVisible();
    expect(screen.getByText("Deleted")).toHaveAttribute(
      "data-slot",
      "state-badge",
    );
    expect(
      screen.queryByRole("button", { name: "Mark as dirty resource" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("link", { name: "View in resource panorama" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("link", { name: "Cloud console" }),
    ).not.toBeInTheDocument();
  });

  it("keeps every resource action for an active resource", () => {
    localStorage.setItem("steward.locale", "en-US");

    renderActions({
      ...deletedAsset,
      closed_at: undefined,
      deleted_at: undefined,
    });

    expect(
      screen.getByRole("button", { name: "Mark as dirty resource" }),
    ).toBeVisible();
    expect(
      screen.getByRole("link", { name: "View in resource panorama" }),
    ).toBeVisible();
    expect(screen.getByRole("link", { name: "Cloud console" })).toBeVisible();
    expect(screen.getByRole("term")).toHaveTextContent("Resource ID");
    expect(screen.queryByText("Deleted")).not.toBeInTheDocument();
  });
});

describe("resource detail ID markers", () => {
  it.each([
    {
      name: "system disk",
      asset: {
        ...deletedAsset,
        id: "system-disk",
        identity: {
          ...deletedAsset.identity,
          native_type: "ACS::ECS::Disk",
          native_id: "d-system",
        },
        normalized: { disk_type: "system" },
        deleted_at: undefined,
        closed_at: undefined,
      },
      bindings: [],
      marker: "System disk",
    },
    {
      name: "system route table",
      asset: {
        ...deletedAsset,
        id: "system-route-table",
        identity: {
          ...deletedAsset.identity,
          native_type: "ACS::VPC::RouteTable",
          native_id: "vtb-system",
        },
        normalized: { route_table_type: "System" },
        deleted_at: undefined,
        closed_at: undefined,
      },
      bindings: [],
      marker: "System route table",
    },
    {
      name: "managed resource",
      asset: {
        ...deletedAsset,
        id: "managed-resource",
        identity: {
          ...deletedAsset.identity,
          native_type: "ACS::ECS::NetworkInterface",
          native_id: "eni-managed",
        },
        deleted_at: undefined,
        closed_at: undefined,
      },
      bindings: [
        {
          id: "binding-managed",
          controller_asset_id: "controller",
          managed_asset_id: "managed-resource",
          authority: "authoritative",
          ownership: "exclusive",
          cleanup_policy: "delegate",
          confidence: 1,
        },
      ],
      marker: "Managed asset",
    },
  ] as const)(
    "shows the $name marker after the resource ID",
    ({ asset, bindings, marker }) => {
      localStorage.setItem("steward.locale", "en-US");

      renderActions(asset, [...bindings]);

      const definition = screen.getByRole("definition");
      const resourceID = within(definition).getByText(asset.identity.native_id);
      const markerBadge = within(definition).getByText(marker);
      const copyButton = within(definition).getByRole("button", {
        name: "Copy Resource ID",
      });
      expect(
        resourceID.compareDocumentPosition(markerBadge) &
          Node.DOCUMENT_POSITION_FOLLOWING,
      ).toBeTruthy();
      expect(markerBadge).toHaveAttribute("data-slot", "badge");
      expect(
        markerBadge.compareDocumentPosition(copyButton) &
          Node.DOCUMENT_POSITION_FOLLOWING,
      ).toBeTruthy();
      expect(definition.parentElement).toHaveClass("min-h-7", "items-center");
    },
  );
});

describe("resource detail region display", () => {
  it("uses the longest matching region for zone-like locations", () => {
    const region = resolveAssetRegion("cn-wulanchabu-gic-1", regions);

    expect(region?.region_id).toBe("cn-wulanchabu-gic");
    expect(assetRegionLabel("cn-wulanchabu-gic-1", region, "全球")).toBe(
      "乌兰察布政企云",
    );
  });

  it("keeps global and unknown locations readable", () => {
    expect(assetRegionLabel("global", undefined, "全球")).toBe("全球");
    expect(assetRegionLabel("custom-region", undefined, "全球")).toBe(
      "custom-region",
    );
  });
});

describe("resource detail type display", () => {
  it("shows the cloud product and localized resource type", () => {
    expect(
      assetResourceKindName(
        "ACS::ROS::Stack",
        {
          native_type: "ACS::ROS::Stack",
          display_name: "Stack",
          display_names: { "zh-CN": "ROS 资源栈" },
        },
        "zh-CN",
      ),
    ).toBe("ROS 资源栈");
  });

  it("keeps the native type when catalog metadata is unavailable", () => {
    expect(
      assetResourceKindName("ACS::ECS::SecurityGroup", undefined, "zh-CN"),
    ).toBe("ACS::ECS::SecurityGroup");
  });
});

describe("resource detail relationships", () => {
  it("presents graph and lifecycle connections in one related-resource list", () => {
    const relationships: Relationship[] = [
      {
        id: "rel-1",
        source_asset_id: "focus",
        target_asset_id: "database",
        type: "depends_on",
        source: "provider",
        confidence: 1,
        graph_revision: "graph-1",
        observed_at: "2026-07-31T00:00:00Z",
      },
      {
        id: "rel-2",
        source_asset_id: "security-group",
        target_asset_id: "focus",
        type: "protects",
        source: "provider",
        confidence: 1,
        graph_revision: "graph-1",
        observed_at: "2026-07-31T00:00:00Z",
      },
    ];
    const bindings: LifecycleBinding[] = [
      {
        id: "binding-1",
        controller_asset_id: "stack",
        managed_asset_id: "focus",
        authority: "authoritative",
        ownership: "managed",
        cleanup_policy: "delegate",
        confidence: 1,
      },
    ];

    expect(buildResourceRelations("focus", relationships, bindings)).toEqual([
      {
        key: "relationship:rel-1",
        peerID: "database",
        direction: "outgoing",
        type: "depends_on",
        kind: "relationship",
      },
      {
        key: "relationship:rel-2",
        peerID: "security-group",
        direction: "incoming",
        type: "protects",
        kind: "relationship",
      },
      {
        key: "lifecycle:binding-1",
        peerID: "stack",
        direction: "incoming",
        type: "managed",
        kind: "lifecycle",
      },
    ]);
  });
});
