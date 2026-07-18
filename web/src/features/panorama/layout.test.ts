import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { expect, it } from "vitest";
import type {
  ResourceGraphTopologyView,
  TopologyResource,
  TopologyResourceEdge,
  VPCTopologyView,
} from "@/api/types";
import {
  buildTopologyLayout,
  topologyEdgeHandles,
  type TopologyLayout,
  type TopologyLayoutNode,
} from "./layout";

interface AlibabaResourceCatalog {
  resources: Array<{ native_type: string; level: string }>;
}

const alibabaIndependentResourceTypes = (
  JSON.parse(
    readFileSync(
      resolve(
        process.cwd(),
        "../providers/alicloud/resourcecenter/resources.json",
      ),
      "utf8",
    ),
  ) as AlibabaResourceCatalog
).resources
  .filter((resource) => resource.level === "instance")
  .map((resource) => resource.native_type);

const cleanup = { selectable: true, potential_blockers: 0 };

function resource(
  key: string,
  resourceKindID: string,
  domain: TopologyResource["domain"],
  className: string,
): TopologyResource {
  return {
    key,
    asset_id: key,
    resource_kind_id: resourceKindID,
    name: key,
    native_id: key,
    type_name: resourceKindID,
    class: className,
    domain,
    finding_count: 0,
    actionable: true,
    cleanup,
  };
}

function graphView(
  resources: TopologyResource[],
  edges: TopologyResourceEdge[] = [],
): ResourceGraphTopologyView {
  return {
    kind: "resource_graph",
    context: { key: "region-public", name: "地域公共资源" },
    resources,
    edges,
  };
}

function vpcView(
  resources: TopologyResource[],
  edges: TopologyResourceEdge[],
  publicResourceKeys: string[],
  vSwitchResourceKeys: Record<string, string[]>,
): VPCTopologyView {
  return {
    kind: "vpc",
    region: { key: "region-a", name: "杭州", native_id: "cn-hangzhou" },
    vpc: { key: "vpc-a", name: "生产网络", native_id: "vpc-a" },
    public_resource_keys: publicResourceKeys,
    vswitches: Object.entries(vSwitchResourceKeys).map(
      ([key, resourceKeys], index) => ({
        key,
        name: `交换机 ${key}`,
        native_id: key,
        zone: index === 0 ? "zone-a" : "zone-b",
        resource_count: resourceKeys.length,
        resource_keys: resourceKeys,
      }),
    ),
    resources,
    edges,
  };
}

function node(layout: TopologyLayout, key: string): TopologyLayoutNode {
  const value = layout.nodes.find((candidate) => candidate.key === key);
  if (!value) throw new Error(`missing layout node ${key}`);
  return value;
}

it("materializes every independently scannable Alibaba Cloud resource type", () => {
  const resources = alibabaIndependentResourceTypes.map((nativeType, index) =>
    resource(`independent-${index}`, `alicloud:${nativeType}`, "unknown", ""),
  );

  const layout = buildTopologyLayout(graphView(resources), new Set(), {
    complete: true,
  });
  const materializedResourceKeys = layout.nodes
    .filter((candidate) => candidate.kind === "resource")
    .map((candidate) => candidate.resource.key)
    .sort();

  expect(alibabaIndependentResourceTypes).toHaveLength(137);
  expect(layout.nodes).toHaveLength(resources.length);
  expect(materializedResourceKeys).toEqual(
    resources.map((candidate) => candidate.key).sort(),
  );
});

it("maps resource classes to y bands without emitting layer artifacts", () => {
  const resources = [
    resource(
      "network-a",
      "security-group",
      "unknown",
      "network.security_group",
    ),
    resource("compute-a", "ecs", "unknown", "compute.instance"),
    resource("container-a", "pod", "unknown", "container.workload"),
    resource("storage-a", "disk", "unknown", "storage.block"),
    resource("other-a", "queue", "network", "messaging.queue"),
  ];

  const layout = buildTopologyLayout(graphView(resources), new Set(), {
    complete: true,
  });

  const y = resources.map((value) => node(layout, value.key).position.y);
  expect(y[0]).toBeLessThan(y[1] ?? 0);
  expect(y[1]).toBe(y[2]);
  expect(y[2]).toBeLessThan(y[3] ?? 0);
  expect(y[3]).toBeLessThan(y[4] ?? 0);
  expect(layout.nodes.every((value) => value.kind === "resource")).toBe(true);
  expect(JSON.stringify(layout)).not.toMatch(/layer/i);
});

it("keeps every network row above every compute row", () => {
  const resources = [
    resource(
      "network-linked",
      "security-group",
      "network",
      "network.security_group",
    ),
    resource("compute-linked", "ecs", "compute", "compute.instance"),
    resource("compute-isolated", "function", "compute", "compute.function"),
  ];
  const layout = buildTopologyLayout(
    graphView(resources, [
      {
        key: "uses",
        source_key: "compute-linked",
        target_key: "network-linked",
        kind: "relationship",
        relation: "uses",
      },
    ]),
    new Set(),
    { complete: true },
  );

  const networkBottom =
    node(layout, "network-linked").position.y +
    node(layout, "network-linked").size.height;
  const computeTop = Math.min(
    node(layout, "compute-linked").position.y,
    node(layout, "compute-isolated").position.y,
  );
  expect(networkBottom).toBeLessThan(computeTop);
});

it("keeps bare and aggregated resources on one shared row geometry", () => {
  const resources = [
    resource(
      "route-table-a",
      "alicloud:ACS::VPC::RouteTable",
      "network",
      "network.route",
    ),
    resource(
      "route-table-b",
      "alicloud:ACS::VPC::RouteTable",
      "network",
      "network.route",
    ),
    {
      ...resource(
        "network-acl",
        "alicloud:ACS::VPC::NetworkAcl",
        "network",
        "network.acl",
      ),
      membership_unknown: true,
    },
  ];

  const layout = buildTopologyLayout(graphView(resources), new Set(), {
    complete: true,
  });
  const stack = node(
    layout,
    "stack:region-public%3Aregion-public:alicloud%3AACS%3A%3AVPC%3A%3ARouteTable",
  );
  const bare = node(layout, "network-acl");

  expect(stack.position.y).toBe(bare.position.y);
  expect(stack.size.height).toBe(bare.size.height);
});

it("collapses unambiguous parent-child resources and reveals them on demand", () => {
  const cen = resource(
    "cen",
    "alicloud:ACS::CEN::CenInstance",
    "network",
    "network.cen",
  );
  const routers = Array.from({ length: 3 }, (_, index) => ({
    ...resource(
      `router-${index}`,
      "alicloud:ACS::CEN::TransitRouter",
      "network",
      "network.router",
    ),
    finding_count: index,
  }));
  const peer = resource(
    "peer",
    "alicloud:ACS::CEN::CenInstance",
    "network",
    "network.cen",
  );
  const membershipEdges = routers.map((router, index) => ({
    key: `membership-${index}`,
    source_key: router.key,
    target_key: cen.key,
    kind: "relationship" as const,
    relation: "member_of",
  }));
  const view = graphView(
    [cen, ...routers, peer],
    [
      ...membershipEdges,
      {
        key: "router-0-peer",
        source_key: "router-0",
        target_key: "peer",
        kind: "relationship",
        relation: "attached_to",
      },
      {
        key: "router-1-peer",
        source_key: "router-1",
        target_key: "peer",
        kind: "relationship",
        relation: "attached_to",
      },
    ],
  );

  const collapsed = buildTopologyLayout(view, new Set(), {
    complete: true,
  });
  const collapsedGroup = node(collapsed, cen.key);
  expect(collapsedGroup).toMatchObject({
    kind: "resourceGroup",
    childKeys: ["router-0", "router-1", "router-2"],
    childFindingCount: 3,
    childTypeSummaries: [
      {
        resourceKindID: "alicloud:ACS::CEN::TransitRouter",
        count: 3,
      },
    ],
  });
  expect(
    collapsed.nodes.some((candidate) => candidate.key === "router-0"),
  ).toBe(false);
  expect(collapsed.edges).toHaveLength(1);
  expect(collapsed.edges[0]).toMatchObject({
    source_key: "cen",
    target_key: "peer",
    relation: "attached_to",
    metadata: { aggregate_count: 2 },
  });

  const expanded = buildTopologyLayout(view, new Set([cen.key]), {
    complete: true,
  });
  expect(node(expanded, cen.key)).toMatchObject({
    kind: "expandedResourceGroup",
    size: { height: 204 },
  });
  for (const router of routers) {
    expect(node(expanded, router.key)).toMatchObject({
      kind: "resource",
      parentKey: cen.key,
    });
  }
  expect(
    routers
      .map((router) => node(expanded, router.key).position.y)
      .sort((left, right) => left - right),
  ).toEqual([40, 40, 120]);
  expect(expanded.edges.map((edge) => edge.key)).toEqual([
    "router-0-peer",
    "router-1-peer",
  ]);
});

it("preserves immediate parentage and progressively reveals multi-level resources", () => {
  const cen = resource("cen", "cen", "network", "network.cen");
  const transitRouter = resource(
    "transit-router",
    "transit-router",
    "network",
    "network.transit_router",
  );
  const cidr = resource(
    "cidr",
    "transit-router-cidr",
    "network",
    "network.transit_router_cidr",
  );
  const peer = resource(
    "peer",
    "peer-attachment",
    "network",
    "network.cen_attachment",
  );
  const routeTable = resource(
    "route-table",
    "route-table",
    "network",
    "network.route_table",
  );
  const routeMap = resource(
    "route-map",
    "route-map",
    "network",
    "network.route_policy",
  );
  const membership = (source: TopologyResource, target: TopologyResource) => ({
    key: `${source.key}-${target.key}`,
    source_key: source.key,
    target_key: target.key,
    kind: "relationship" as const,
    relation: "member_of",
  });
  const view = graphView(
    [cen, transitRouter, cidr, peer, routeTable, routeMap],
    [
      membership(transitRouter, cen),
      membership(cidr, cen),
      membership(cidr, transitRouter),
      membership(peer, cen),
      membership(peer, transitRouter),
      membership(routeTable, cen),
      membership(routeTable, transitRouter),
      membership(routeMap, cen),
      membership(routeMap, transitRouter),
      membership(routeMap, routeTable),
    ],
  );

  const collapsed = buildTopologyLayout(view, new Set(), {
    complete: true,
  });
  const transitRouterGroup = node(collapsed, transitRouter.key);
  expect(transitRouterGroup).toMatchObject({
    kind: "resourceGroup",
    directChildCount: 3,
  });
  expect(
    new Set(
      transitRouterGroup.kind === "resourceGroup"
        ? transitRouterGroup.childKeys
        : [],
    ),
  ).toEqual(new Set(["cidr", "peer", "route-table", "route-map"]));
  expect(
    collapsed.nodes.some((candidate) => candidate.key === routeTable.key),
  ).toBe(false);

  const firstLevel = buildTopologyLayout(view, new Set([transitRouter.key]), {
    complete: true,
  });
  expect(node(firstLevel, routeTable.key)).toMatchObject({
    kind: "resourceGroup",
    directChildCount: 1,
    childKeys: ["route-map"],
    parentKey: transitRouter.key,
  });
  expect(
    firstLevel.nodes.some((candidate) => candidate.key === routeMap.key),
  ).toBe(false);

  const secondLevel = buildTopologyLayout(
    view,
    new Set([transitRouter.key, routeTable.key]),
    { complete: true },
  );
  expect(node(secondLevel, routeTable.key)).toMatchObject({
    kind: "expandedResourceGroup",
    parentKey: transitRouter.key,
  });
  expect(node(secondLevel, routeMap.key)).toMatchObject({
    kind: "resource",
    parentKey: routeTable.key,
  });
  expect(
    secondLevel.edges.filter(
      (edge) =>
        (edge.source_key === routeMap.key &&
          (edge.target_key === routeTable.key ||
            edge.target_key === transitRouter.key)) ||
        (edge.source_key === routeTable.key &&
          edge.target_key === transitRouter.key),
    ),
  ).toHaveLength(0);
});

it("groups IPsec connections under their customer gateway", () => {
  const customerGateway = resource(
    "customer-gateway",
    "customer-gateway",
    "network",
    "network.customer_gateway",
  );
  const firstConnection = resource(
    "vpn-connection-a",
    "vpn-connection",
    "network",
    "network.vpn_connection",
  );
  const secondConnection = resource(
    "vpn-connection-b",
    "vpn-connection",
    "network",
    "network.vpn_connection",
  );
  const usesCustomerGateway = [firstConnection, secondConnection].map(
    (connection) => ({
      key: `${connection.key}-customer-gateway`,
      source_key: connection.key,
      target_key: customerGateway.key,
      kind: "relationship" as const,
      relation: "uses",
    }),
  );

  const layout = buildTopologyLayout(
    graphView(
      [customerGateway, firstConnection, secondConnection],
      usesCustomerGateway,
    ),
    new Set(),
    { complete: true },
  );

  expect(node(layout, customerGateway.key)).toMatchObject({
    kind: "resourceGroup",
    directChildCount: 2,
    childKeys: ["vpn-connection-a", "vpn-connection-b"],
  });
  expect(layout.edges).toHaveLength(0);
});

it("does not infer a containment group for resources with multiple parents", () => {
  const parentA = resource("parent-a", "parent", "network", "network.group");
  const parentB = resource("parent-b", "parent", "network", "network.group");
  const childA = resource("child-a", "child", "network", "network.route");
  const childB = resource("child-b", "child", "network", "network.route");
  const edges = [childA, childB].flatMap((child) => [
    {
      key: `${child.key}-parent-a`,
      source_key: child.key,
      target_key: parentA.key,
      kind: "relationship" as const,
      relation: "member_of",
    },
    {
      key: `${child.key}-parent-b`,
      source_key: child.key,
      target_key: parentB.key,
      kind: "relationship" as const,
      relation: "member_of",
    },
  ]);

  const layout = buildTopologyLayout(
    graphView([parentA, parentB, childA, childB], edges),
    new Set(),
    { complete: true },
  );

  expect(layout.nodes.every((candidate) => candidate.kind === "resource")).toBe(
    true,
  );
  expect(layout.edges).toHaveLength(4);
});

it("shows PrivateLink lifecycle ownership as a connection without drawing cleanup semantics", () => {
  const endpoint = resource(
    "endpoint",
    "endpoint",
    "network",
    "network.endpoint",
  );
  const eni = resource("eni", "eni", "network", "network.interface");
  const layout = buildTopologyLayout(
    graphView(
      [endpoint, eni],
      [
        {
          key: "endpoint-cleans-eni",
          source_key: endpoint.key,
          target_key: eni.key,
          kind: "lifecycle",
          relation: "delegate",
        },
        {
          key: "eni-belongs-to-endpoint",
          source_key: eni.key,
          target_key: endpoint.key,
          kind: "relationship",
          relation: "member_of",
        },
      ],
    ),
    new Set(),
    { complete: true },
  );

  expect(layout.edges).toEqual([
    expect.objectContaining({
      key: "endpoint-cleans-eni",
      source_key: endpoint.key,
      target_key: eni.key,
      kind: "relationship",
      relation: "connected_to",
    }),
  ]);

  const current = buildTopologyLayout(
    graphView(
      [endpoint, eni],
      [
        {
          key: "endpoint-cleans-eni",
          source_key: endpoint.key,
          target_key: eni.key,
          kind: "lifecycle",
          relation: "delegate",
        },
        {
          key: "eni-connects-to-endpoint",
          source_key: eni.key,
          target_key: endpoint.key,
          kind: "relationship",
          relation: "connected_to",
        },
      ],
    ),
    new Set(),
    { complete: true },
  );
  expect(current.edges.map((edge) => edge.key)).toEqual([
    "eni-connects-to-endpoint",
  ]);
});

it("places VPC endpoints in a centered public row nearest the vSwitches", () => {
  const publicResources = [
    resource("sg-a", "sg-a", "network", "network.security_group"),
    resource("sg-b", "sg-b", "network", "network.security_group"),
    resource("route-table", "route-table", "network", "network.route"),
    resource("endpoint-a", "endpoint-a", "network", "network.endpoint"),
    resource("endpoint-b", "endpoint-b", "network", "network.endpoint"),
  ];
  const eniA = resource("eni-a", "eni-a", "network", "network.eni");
  const eniB = resource("eni-b", "eni-b", "network", "network.eni");
  const layout = buildTopologyLayout(
    vpcView(
      [...publicResources, eniA, eniB],
      [
        {
          key: "endpoint-a-eni-a",
          source_key: "endpoint-a",
          target_key: "eni-a",
          kind: "lifecycle",
          relation: "delegate",
        },
        {
          key: "endpoint-b-eni-b",
          source_key: "endpoint-b",
          target_key: "eni-b",
          kind: "lifecycle",
          relation: "delegate",
        },
      ],
      publicResources.map((value) => value.key),
      { "vsw-a": [eniA.key, eniB.key] },
    ),
    new Set(),
    { complete: true },
  );

  const infrastructure = ["sg-a", "sg-b", "route-table"].map((key) =>
    node(layout, key),
  );
  const endpoints = ["endpoint-a", "endpoint-b"].map((key) =>
    node(layout, key),
  );
  expect(new Set(infrastructure.map((value) => value.position.y))).toEqual(
    new Set([0]),
  );
  expect(endpoints[0]?.position.y).toBe(endpoints[1]?.position.y);
  expect(endpoints[0]?.position.y).toBeGreaterThan(
    infrastructure[0]?.position.y ?? 0,
  );
  const endpointRowCenter =
    ((endpoints[0]?.position.x ?? 0) +
      (endpoints[0]?.size.width ?? 0) / 2 +
      (endpoints[1]?.position.x ?? 0) +
      (endpoints[1]?.size.width ?? 0) / 2) /
    2;
  expect(endpointRowCenter).toBe(layout.size.width / 2);
  expect(layout.size.width).toBe(560);
});

it("wraps a large public resource band into a canvas-shaped footprint", () => {
  const resources = Array.from({ length: 120 }, (_, index) =>
    resource(
      `network-${index.toString().padStart(3, "0")}`,
      `network-kind-${index}`,
      "network",
      "network.route",
    ),
  );
  const edges = resources.slice(1).map((value, index) => ({
    key: `edge-${index}`,
    source_key: resources[0]!.key,
    target_key: value.key,
    kind: "relationship" as const,
    relation: "references",
  }));

  const layout = buildTopologyLayout(graphView(resources, edges), new Set(), {
    complete: true,
  });
  const yCoordinates = new Set(
    layout.nodes
      .filter((value) => value.kind === "resource")
      .map((value) => value.position.y),
  );

  expect(yCoordinates.size).toBeGreaterThan(1);
  expect(layout.size.width / layout.size.height).toBeGreaterThan(1);
  expect(layout.size.width / layout.size.height).toBeLessThan(3);
});

it("uses the available landscape canvas for a mixed public resource graph", () => {
  const resources = [
    resource(
      "security-group",
      "security-group",
      "network",
      "network.security_group",
    ),
    resource(
      "network-interface",
      "network-interface",
      "network",
      "network.interface",
    ),
    resource("disk", "disk", "storage", "storage.block"),
    ...Array.from({ length: 82 }, (_, index) =>
      resource(
        `other-${index.toString().padStart(2, "0")}`,
        `other-kind-${index}`,
        "unknown",
        "unknown",
      ),
    ),
  ];

  const layout = buildTopologyLayout(graphView(resources), new Set(), {
    complete: true,
  });
  const aspect = layout.size.width / layout.size.height;

  expect(aspect).toBeGreaterThan(1.5);
  expect(aspect).toBeLessThan(2.1);
});

it("adapts the vSwitch grid columns to fill wide and tall canvases", () => {
  const resources = Array.from({ length: 18 }, (_, index) =>
    resource(
      `ecs-${index.toString().padStart(2, "0")}`,
      "ecs",
      "compute",
      "compute.instance",
    ),
  );
  const members = Object.fromEntries(
    resources.map((value, index) => [`vsw-${index}`, [value.key]]),
  );
  const view = vpcView(resources, [], [], members);
  const wide = buildTopologyLayout(view, new Set(), {
    complete: true,
    viewport: { width: 1600, height: 900 },
  });
  const tall = buildTopologyLayout(view, new Set(), {
    complete: true,
    viewport: { width: 800, height: 1200 },
  });
  const wideColumns = new Set(
    wide.nodes
      .filter((value) => value.kind === "vswitch")
      .map((value) => value.position.x),
  ).size;
  const tallColumns = new Set(
    tall.nodes
      .filter((value) => value.kind === "vswitch")
      .map((value) => value.position.x),
  ).size;

  expect(wideColumns).toBeGreaterThan(2);
  expect(wideColumns).toBeGreaterThan(tallColumns);
});

it("fills each vSwitch row horizontally before wrapping", () => {
  const resources = Array.from({ length: 4 }, (_, index) =>
    resource(`ecs-${index}`, `ecs-${index}`, "compute", "compute.instance"),
  );
  const view = vpcView(
    resources,
    [],
    [],
    Object.fromEntries(
      resources.map((value, index) => [`vsw-${index}`, [value.key]]),
    ),
  );

  const layout = buildTopologyLayout(view, new Set(), {
    complete: true,
    viewport: { width: 1200, height: 800 },
  });
  const vSwitches = layout.nodes.filter((value) => value.kind === "vswitch");

  expect(new Set(vSwitches.map((value) => value.position.x)).size).toBe(3);
  expect(vSwitches.slice(0, 3).map((value) => value.position.y)).toEqual([
    0, 0, 0,
  ]);
  expect(vSwitches[3]?.position.y).toBeGreaterThan(0);
});

it("keeps two vSwitches side by side on a landscape topology", () => {
  const eni = resource("eni", "eni", "network", "network.eni");
  const layout = buildTopologyLayout(
    vpcView([eni], [], [], { "vsw-a": [eni.key], "vsw-b": [] }),
    new Set(),
    {
      complete: true,
      viewport: { width: 1744, height: 1048 },
    },
  );
  const vSwitches = layout.nodes.filter((value) => value.kind === "vswitch");

  expect(vSwitches).toHaveLength(2);
  expect(vSwitches[0]?.position.y).toBe(vSwitches[1]?.position.y);
  expect(vSwitches[0]?.position.x).toBeLessThan(vSwitches[1]?.position.x ?? 0);
});

it("aligns the ECS, ENI, security group, and disk chain on the shortest display path", () => {
  const resources = [
    resource(
      "security-group",
      "security-group",
      "network",
      "network.security_group",
    ),
    resource("eni", "eni", "network", "network.eni"),
    resource("ecs", "ecs", "compute", "compute.instance"),
    resource("disk", "disk", "storage", "storage.block"),
  ];
  const edges: TopologyResourceEdge[] = [
    {
      key: "ecs-security-group",
      source_key: "ecs",
      target_key: "security-group",
      kind: "relationship",
      relation: "uses",
    },
    {
      key: "ecs-eni",
      source_key: "ecs",
      target_key: "eni",
      kind: "relationship",
      relation: "uses",
    },
    {
      key: "eni-security-group",
      source_key: "eni",
      target_key: "security-group",
      kind: "relationship",
      relation: "uses",
    },
    {
      key: "disk-ecs",
      source_key: "disk",
      target_key: "ecs",
      kind: "relationship",
      relation: "attached_to",
    },
  ];
  const layout = buildTopologyLayout(
    vpcView(resources, edges, ["security-group"], {
      "vsw-active": ["eni", "ecs", "disk"],
      "vsw-empty-a": [],
      "vsw-empty-b": [],
    }),
    new Set(),
    { complete: true },
  );
  const vSwitch = node(layout, "vsw-active");
  const absoluteCenterX = (key: string) => {
    const value = node(layout, key);
    const parentX = value.parentKey
      ? node(layout, value.parentKey).position.x
      : 0;
    return parentX + value.position.x + value.size.width / 2;
  };

  expect(layout.edges.map((edge) => edge.key)).toEqual([
    "disk-ecs",
    "ecs-eni",
    "eni-security-group",
  ]);
  expect(absoluteCenterX("security-group")).toBe(absoluteCenterX("eni"));
  expect(absoluteCenterX("eni")).toBe(absoluteCenterX("ecs"));
  expect(absoluteCenterX("ecs")).toBe(absoluteCenterX("disk"));
  expect(absoluteCenterX("eni")).toBe(
    vSwitch.position.x + vSwitch.size.width / 2,
  );
  for (const key of ["security-group", "eni", "ecs", "disk"]) {
    expect(node(layout, key).size.height).toBe(60);
  }

  expect(
    Object.fromEntries(topologyEdgeHandles(layout.nodes, layout.edges)),
  ).toEqual({
    "disk-ecs": {
      sourceHandle: "source-top",
      targetHandle: "target-bottom",
    },
    "ecs-eni": {
      sourceHandle: "source-top",
      targetHandle: "target-bottom",
    },
    "eni-security-group": {
      sourceHandle: "source-top",
      targetHandle: "target-bottom",
    },
  });
});

it("shapes a large expanded stack to occupy the current canvas", () => {
  const resources = Array.from({ length: 26 }, (_, index) =>
    resource(
      `eni-${index.toString().padStart(2, "0")}`,
      "eni",
      "network",
      "network.interface",
    ),
  );
  const view = vpcView(resources, [], [], {
    "vsw-a": resources.map((value) => value.key),
  });
  const stackKey = "stack:vswitch%3Avsw-a:eni";
  const viewport = { width: 1600, height: 900 };
  const expanded = buildTopologyLayout(view, new Set([stackKey]), {
    complete: true,
    viewport,
  });
  const stack = node(expanded, stackKey);
  const memberColumns = new Set(
    expanded.nodes
      .filter(
        (value) => value.kind === "resource" && value.parentKey === stackKey,
      )
      .map((value) => value.position.x),
  ).size;
  const scale = Math.min(
    1.8,
    viewport.width / stack.size.width,
    viewport.height / stack.size.height,
  );
  const occupiedRatio =
    (stack.size.width * stack.size.height * scale * scale) /
    (viewport.width * viewport.height);

  expect(memberColumns).toBeGreaterThan(3);
  expect(stack.size.width / stack.size.height).toBeGreaterThan(1.3);
  expect(stack.size.width / stack.size.height).toBeLessThan(2);
  expect(occupiedRatio).toBeGreaterThan(0.75);
});

it("stacks only complete independent resources by display domain and kind", () => {
  const resources = [
    resource("ecs-linked", "ecs", "compute", "compute.instance"),
    resource("disk-linked", "disk", "storage", "storage.block"),
    {
      ...resource("ecs-isolated-a", "ecs", "compute", "compute.instance"),
      icon: "shield",
    },
    resource("ecs-isolated-b", "ecs", "compute", "compute.instance"),
    resource("ecs-isolated-c", "ecs", "compute", "compute.instance"),
    resource(
      "security-linked-a",
      "security-group",
      "network",
      "network.security_group",
    ),
    resource(
      "security-linked-b",
      "security-group",
      "network",
      "network.security_group",
    ),
    resource(
      "security-isolated",
      "security-group",
      "network",
      "network.security_group",
    ),
    resource("eni-lifecycle-a", "eni", "network", "network.eni"),
    resource("eni-lifecycle-b", "eni", "network", "network.eni"),
    resource("function-a", "function", "compute", "compute.function"),
    resource("function-b", "function", "compute", "compute.function"),
    resource("ecs-other-a", "ecs", "compute", "compute.instance"),
    resource("ecs-other-b", "ecs", "compute", "compute.instance"),
    resource(
      "public-sg-a",
      "security-group",
      "network",
      "network.security_group",
    ),
    resource(
      "public-sg-b",
      "security-group",
      "network",
      "network.security_group",
    ),
    resource(
      "public-sg-c",
      "security-group",
      "network",
      "network.security_group",
    ),
    resource("public-eni-a", "eni", "network", "network.eni"),
    resource("public-eni-b", "eni", "network", "network.eni"),
    resource("public-disk-a", "disk", "storage", "storage.block"),
    resource("public-disk-b", "disk", "storage", "storage.block"),
    {
      ...resource(
        "public-external-a",
        "route-entry",
        "network",
        "network.route_entry",
      ),
      external_relations: [
        {
          key: "external-route",
          kind: "relationship" as const,
          relation: "routes_to",
          direction: "outbound",
          target_id: "outside",
          target_name: "outside",
          target_type: "route-table",
        },
      ],
    },
    resource(
      "public-external-b",
      "route-entry",
      "network",
      "network.route_entry",
    ),
  ];
  const edges: TopologyResourceEdge[] = [
    {
      key: "attached",
      source_key: "disk-linked",
      target_key: "ecs-linked",
      kind: "relationship",
      relation: "attached_to",
    },
    {
      key: "lifecycle",
      source_key: "function-a",
      target_key: "function-b",
      kind: "lifecycle",
      relation: "delegate",
    },
    {
      key: "security-visible-edge",
      source_key: "security-linked-a",
      target_key: "security-linked-b",
      kind: "relationship",
      relation: "references",
    },
    {
      key: "eni-lifecycle-edge",
      source_key: "eni-lifecycle-a",
      target_key: "eni-lifecycle-b",
      kind: "lifecycle",
      relation: "managed_by",
    },
  ];
  const view = vpcView(
    resources,
    edges,
    [
      "public-sg-a",
      "public-sg-b",
      "public-sg-c",
      "public-eni-a",
      "public-eni-b",
      "public-disk-a",
      "public-disk-b",
      "public-external-a",
      "public-external-b",
    ],
    {
      "vsw-a": [
        "ecs-linked",
        "disk-linked",
        "ecs-isolated-a",
        "ecs-isolated-b",
        "ecs-isolated-c",
        "security-linked-a",
        "security-linked-b",
        "security-isolated",
        "eni-lifecycle-a",
        "eni-lifecycle-b",
        "function-a",
        "function-b",
      ],
      "vsw-b": ["ecs-other-a", "ecs-other-b"],
    },
  );

  const layout = buildTopologyLayout(view, new Set(), { complete: true });
  const stacks = layout.nodes.filter((value) => value.kind === "stack");

  expect(stacks).toEqual(
    expect.arrayContaining([
      expect.objectContaining({
        key: "stack:vswitch%3Avsw-a:ecs",
        count: 3,
        icon: "shield",
        memberKeys: ["ecs-isolated-a", "ecs-isolated-b", "ecs-isolated-c"],
        parentKey: "vsw-a",
      }),
      expect.objectContaining({
        key: "stack:vswitch%3Avsw-b:ecs",
        count: 2,
        memberKeys: ["ecs-other-a", "ecs-other-b"],
        parentKey: "vsw-b",
      }),
    ]),
  );
  expect(stacks).toHaveLength(5);
  expect(node(layout, "ecs-linked").kind).toBe("resource");
  expect(node(layout, "disk-linked").kind).toBe("resource");
  expect(node(layout, "security-linked-a").kind).toBe("resource");
  expect(node(layout, "security-linked-b").kind).toBe("resource");
  expect(node(layout, "security-isolated").kind).toBe("resource");
  expect(node(layout, "eni-lifecycle-a").kind).toBe("resource");
  expect(node(layout, "eni-lifecycle-b").kind).toBe("resource");
  expect(node(layout, "function-a").kind).toBe("resource");
  expect(node(layout, "function-b").kind).toBe("resource");
  expect(node(layout, "stack:vpc-public%3Avpc-a:security-group")).toMatchObject(
    {
      kind: "stack",
      count: 3,
      memberKeys: ["public-sg-a", "public-sg-b", "public-sg-c"],
    },
  );
  expect(node(layout, "stack:vpc-public%3Avpc-a:eni")).toMatchObject({
    kind: "stack",
    memberKeys: ["public-eni-a", "public-eni-b"],
  });
  expect(node(layout, "stack:vpc-public%3Avpc-a:disk")).toMatchObject({
    kind: "stack",
    memberKeys: ["public-disk-a", "public-disk-b"],
  });
  expect(node(layout, "public-external-a").kind).toBe("resource");
  expect(node(layout, "public-external-b").kind).toBe("resource");

  const partial = buildTopologyLayout(view, new Set(), { complete: false });
  expect(partial.nodes.some((value) => value.kind === "stack")).toBe(false);
  expect(
    partial.nodes.filter((value) => value.kind === "resource"),
  ).toHaveLength(resources.length);

  const publicGraph = buildTopologyLayout(
    graphView([
      resource("region-sg-a", "security-group", "network", "network.sg"),
      resource("region-sg-b", "security-group", "network", "network.sg"),
      resource("region-sg-c", "security-group", "network", "network.sg"),
    ]),
    new Set(),
    { complete: true },
  );
  expect(
    node(publicGraph, "stack:region-public%3Aregion-public:security-group"),
  ).toMatchObject({
    kind: "stack",
    count: 3,
    memberKeys: ["region-sg-a", "region-sg-b", "region-sg-c"],
  });
});

it("stacks snapshots unless they are bound to an image or disk", () => {
  const snapshot = (key: string): TopologyResource =>
    resource(key, "alicloud:ACS::ECS::Snapshot", "storage", "storage.snapshot");
  const kms = resource(
    "kms",
    "alicloud:ACS::KMS::Key",
    "unknown",
    "security.key",
  );
  const image = resource(
    "image",
    "alicloud:ACS::ECS::Image",
    "compute",
    "compute.image",
  );
  const disk = resource(
    "disk",
    "alicloud:ACS::ECS::Disk",
    "storage",
    "storage.block",
  );
  const resources = [
    snapshot("snapshot-kms"),
    snapshot("snapshot-source"),
    snapshot("snapshot-copy"),
    snapshot("snapshot-image"),
    snapshot("snapshot-disk"),
    {
      ...snapshot("snapshot-external-image"),
      external_relations: [
        {
          key: "external-image",
          kind: "relationship" as const,
          relation: "created_from",
          direction: "incoming",
          target_id: "filtered-image",
          target_name: "filtered-image",
          target_type: "Image",
          target_resource_kind_id: "alicloud:ACS::ECS::Image",
          target_class: "compute.image",
        },
      ],
    },
    {
      ...snapshot("snapshot-external-kms"),
      external_relations: [
        {
          key: "external-kms",
          kind: "relationship" as const,
          relation: "uses",
          direction: "outgoing",
          target_id: "filtered-kms",
          target_name: "filtered-kms",
          target_type: "KMS Key",
          target_resource_kind_id: "alicloud:ACS::KMS::Key",
          target_class: "security.key",
        },
      ],
    },
    kms,
    image,
    disk,
  ];
  const edges: TopologyResourceEdge[] = [
    {
      key: "snapshot-kms",
      source_key: "snapshot-kms",
      target_key: "kms",
      kind: "relationship",
      relation: "uses",
    },
    {
      key: "snapshot-source",
      source_key: "snapshot-copy",
      target_key: "snapshot-source",
      kind: "relationship",
      relation: "created_from",
    },
    {
      key: "image-snapshot",
      source_key: "image",
      target_key: "snapshot-image",
      kind: "relationship",
      relation: "created_from",
    },
    {
      key: "snapshot-disk",
      source_key: "snapshot-disk",
      target_key: "disk",
      kind: "relationship",
      relation: "created_from",
    },
  ];

  const stackKey =
    "stack:region-public%3Aregion-public:alicloud%3AACS%3A%3AECS%3A%3ASnapshot";
  const layout = buildTopologyLayout(graphView(resources, edges), new Set(), {
    complete: true,
  });

  expect(node(layout, stackKey)).toMatchObject({
    kind: "stack",
    count: 4,
    memberKeys: [
      "snapshot-kms",
      "snapshot-copy",
      "snapshot-source",
      "snapshot-external-kms",
    ],
  });
  expect(node(layout, "snapshot-image").kind).toBe("resource");
  expect(node(layout, "snapshot-disk").kind).toBe("resource");
  expect(node(layout, "snapshot-external-image").kind).toBe("resource");
  expect(layout.edges).toEqual(
    expect.arrayContaining([
      expect.objectContaining({
        source_key: stackKey,
        target_key: "kms",
      }),
      expect.objectContaining({
        source_key: "image",
        target_key: "snapshot-image",
      }),
      expect.objectContaining({
        source_key: "snapshot-disk",
        target_key: "disk",
      }),
    ]),
  );
  expect(layout.edges.some((edge) => edge.key === "snapshot-source")).toBe(
    false,
  );

  const expanded = buildTopologyLayout(
    graphView(resources, edges),
    new Set([stackKey]),
    { complete: true },
  );
  expect(expanded.edges).toEqual(
    expect.arrayContaining([
      expect.objectContaining({
        key: "snapshot-kms",
        source_key: "snapshot-kms",
        target_key: "kms",
      }),
      expect.objectContaining({
        key: "snapshot-source",
        source_key: "snapshot-copy",
        target_key: "snapshot-source",
      }),
    ]),
  );
});

it("keeps resources with unknown membership bare instead of guessing a public display domain", () => {
  const unknownA = {
    ...resource("unknown-eni-a", "eni", "network", "network.eni"),
    membership_unknown: true,
  };
  const unknownB = {
    ...resource("unknown-eni-b", "eni", "network", "network.eni"),
    membership_unknown: true,
  };
  const layout = buildTopologyLayout(
    vpcView([unknownA, unknownB], [], ["unknown-eni-a", "unknown-eni-b"], {}),
    new Set(),
    { complete: true },
  );

  expect(layout.nodes.filter((value) => value.kind === "stack")).toHaveLength(
    0,
  );
  expect(node(layout, "unknown-eni-a")).toMatchObject({
    kind: "resource",
    resource: { membership_unknown: true },
  });
  expect(node(layout, "unknown-eni-b").kind).toBe("resource");
});

it("keeps identical kinds in separate vSwitch and public display domains", () => {
  const resources = [
    resource("vsw-a-eni-a", "eni", "network", "network.eni"),
    resource("vsw-a-eni-b", "eni", "network", "network.eni"),
    resource("vsw-a-eni-c", "eni", "network", "network.eni"),
    resource("vsw-b-eni-a", "eni", "network", "network.eni"),
    resource("vsw-b-eni-b", "eni", "network", "network.eni"),
    resource("public-eni-a", "eni", "network", "network.eni"),
    resource("public-eni-b", "eni", "network", "network.eni"),
  ];
  const layout = buildTopologyLayout(
    vpcView(resources, [], ["public-eni-a", "public-eni-b"], {
      "vsw-a": ["vsw-a-eni-a", "vsw-a-eni-b", "vsw-a-eni-c"],
      "vsw-b": ["vsw-b-eni-a", "vsw-b-eni-b"],
    }),
    new Set(),
    { complete: true },
  );

  expect(layout.nodes.filter((value) => value.kind === "stack")).toEqual(
    expect.arrayContaining([
      expect.objectContaining({
        key: "stack:vswitch%3Avsw-a:eni",
        memberKeys: ["vsw-a-eni-a", "vsw-a-eni-b", "vsw-a-eni-c"],
      }),
      expect.objectContaining({
        key: "stack:vswitch%3Avsw-b:eni",
        memberKeys: ["vsw-b-eni-a", "vsw-b-eni-b"],
      }),
      expect.objectContaining({
        key: "stack:vpc-public%3Avpc-a:eni",
        memberKeys: ["public-eni-a", "public-eni-b"],
      }),
    ]),
  );
});

it("uses collision-safe stack keys when display-domain keys and kinds contain colons", () => {
  const layout = buildTopologyLayout(
    vpcView(
      [
        resource("first-a", "c", "network", "network.eni"),
        resource("first-b", "c", "network", "network.eni"),
        resource("second-a", "b:c", "network", "network.eni"),
        resource("second-b", "b:c", "network", "network.eni"),
      ],
      [],
      [],
      {
        "a:b": ["first-a", "first-b"],
        a: ["second-a", "second-b"],
      },
    ),
    new Set(),
    { complete: true },
  );
  const stacks = layout.nodes.filter((value) => value.kind === "stack");

  expect(stacks).toHaveLength(2);
  expect([...new Set(stacks.map((stack) => stack.key))]).toHaveLength(2);
  expect(stacks).toEqual(
    expect.arrayContaining([
      expect.objectContaining({
        key: "stack:vswitch%3Aa%3Ab:c",
        memberKeys: ["first-a", "first-b"],
      }),
      expect.objectContaining({
        key: "stack:vswitch%3Aa:b%3Ac",
        memberKeys: ["second-a", "second-b"],
      }),
    ]),
  );
});

it("uses resource kind as the stack label when type names are blank", () => {
  const layout = buildTopologyLayout(
    graphView([
      {
        ...resource("type-fallback-a", "eni", "network", "network.eni"),
        type_name: "  ",
      },
      {
        ...resource("type-fallback-b", "eni", "network", "network.eni"),
        type_name: "",
      },
      {
        ...resource("type-fallback-c", "eni", "network", "network.eni"),
        type_name: "\t",
      },
    ]),
    new Set(),
    { complete: true },
  );

  expect(node(layout, "stack:region-public%3Aregion-public:eni")).toMatchObject(
    {
      kind: "stack",
      typeName: "eni",
      memberKeys: ["type-fallback-a", "type-fallback-b", "type-fallback-c"],
    },
  );
});

it("uses the code-unit minimum member key for stack component and item order", () => {
  const resources = [
    { ...resource("a", "ecs", "compute", "compute.instance"), name: "Z" },
    { ...resource("z", "ecs", "compute", "compute.instance"), name: "A" },
    {
      ...resource("b", "function", "compute", "compute.function"),
      name: "B",
    },
  ];
  const view = vpcView(resources, [], [], {
    "vsw-a": ["a", "z", "b"],
  });

  const layout = buildTopologyLayout(view, new Set(), { complete: true });
  const stack = node(layout, "stack:vswitch%3Avsw-a:ecs");

  expect(stack).toMatchObject({
    kind: "stack",
    memberKeys: ["a", "z"],
  });
  expect(stack.position.x).toBeLessThan(node(layout, "b").position.x);
});

it("expands and collapses a stack at one stable anchor while compactly reflowing the grid", () => {
  const resources = [
    resource("ecs-a", "ecs", "compute", "compute.instance"),
    resource("ecs-b", "ecs", "compute", "compute.instance"),
    resource("ecs-c", "ecs", "compute", "compute.instance"),
    resource("other-a", "queue", "unknown", "unknown"),
    resource("other-b", "queue", "unknown", "unknown"),
  ];
  const view = vpcView(resources, [], [], {
    "vsw-a": ["ecs-a", "ecs-b", "ecs-c"],
    "vsw-b": ["other-a", "other-b"],
  });
  const stackKey = "stack:vswitch%3Avsw-a:ecs";

  const collapsed = buildTopologyLayout(view, new Set(), { complete: true });
  const expanded = buildTopologyLayout(view, new Set([stackKey]), {
    complete: true,
  });
  const collapsedAgain = buildTopologyLayout(view, new Set(), {
    complete: true,
  });

  expect(node(expanded, stackKey)).toMatchObject({
    kind: "expandedStack",
    key: stackKey,
    position: node(collapsed, stackKey).position,
    parentKey: "vsw-a",
    memberKeys: ["ecs-a", "ecs-b", "ecs-c"],
  });
  for (const key of ["ecs-a", "ecs-b", "ecs-c"]) {
    expect(node(expanded, key)).toMatchObject({
      kind: "resource",
      parentKey: stackKey,
    });
  }
  expect(node(collapsedAgain, stackKey)).toMatchObject({
    kind: "stack",
    position: node(collapsed, stackKey).position,
  });
  expect(node(expanded, "vsw-a").size).not.toEqual(
    node(collapsed, "vsw-a").size,
  );
  expect(
    rectanglesOverlap(node(expanded, "vsw-a"), node(expanded, "vsw-b")),
  ).toBe(false);
  expect(node(collapsedAgain, "vsw-b")).toEqual(node(collapsed, "vsw-b"));
});

it("uses the current public-stack footprint and reflows vSwitches after expansion", () => {
  const publicResources = Array.from({ length: 10 }, (_, index) =>
    resource(
      `public-sg-${index}`,
      "security-group",
      "network",
      "network.security_group",
    ),
  );
  const scopedResources = Array.from({ length: 4 }, (_, index) =>
    resource(`ecs-${index}`, `ecs-${index}`, "compute", "compute.instance"),
  );
  const view = vpcView(
    [...publicResources, ...scopedResources],
    [],
    publicResources.map((value) => value.key),
    Object.fromEntries(
      scopedResources.map((value, index) => [`vsw-${index}`, [value.key]]),
    ),
  );
  const stackKey = "stack:vpc-public%3Avpc-a:security-group";

  const collapsed = buildTopologyLayout(view, new Set(), { complete: true });
  const expanded = buildTopologyLayout(view, new Set([stackKey]), {
    complete: true,
  });

  expect(node(expanded, stackKey)).toMatchObject({ kind: "expandedStack" });
  expect(node(expanded, stackKey).position.y).toBe(
    node(collapsed, stackKey).position.y,
  );
  const collapsedTop = Math.min(
    ...collapsed.nodes
      .filter((value) => value.kind === "vswitch")
      .map((value) => value.position.y),
  );
  const expandedTop = Math.min(
    ...expanded.nodes
      .filter((value) => value.kind === "vswitch")
      .map((value) => value.position.y),
  );
  expect(expandedTop).toBeGreaterThan(collapsedTop);
  expect(collapsed.size.height).toBeLessThan(expanded.size.height);
});

it("collapses truly empty vSwitches into one stable stack while retaining singleton boundaries", () => {
  const resources = [
    resource("ecs-a", "ecs", "compute", "compute.instance"),
    resource("disk-a", "disk", "storage", "storage.block"),
  ];
  const view: VPCTopologyView = {
    ...vpcView(resources, [], [], {
      "vsw-populated-a": ["ecs-a"],
      "vsw-populated-b": ["disk-a"],
    }),
    vswitches: [
      {
        key: "vsw-populated-a",
        name: "application",
        native_id: "vsw-populated-a",
        zone: "zone-a",
        resource_count: 1,
        resource_keys: ["ecs-a"],
      },
      {
        key: "vsw-populated-b",
        name: "storage",
        native_id: "vsw-populated-b",
        zone: "zone-b",
        resource_count: 1,
        resource_keys: ["disk-a"],
      },
      {
        key: "vsw-empty-z",
        name: "Zulu",
        native_id: "vsw-empty-z",
        zone: "zone-a",
        resource_count: 0,
        resource_keys: [],
      },
      {
        key: "vsw-empty-a",
        name: "Alpha",
        native_id: "vsw-empty-a",
        zone: "zone-z",
        resource_count: 0,
        resource_keys: [],
      },
      {
        key: "vsw-empty-b",
        name: "Beta",
        native_id: "vsw-empty-b",
        zone: "zone-y",
        resource_count: 0,
        resource_keys: [],
      },
    ],
  };
  const stackKey = "vswitch-stack:vpc-a";

  const collapsed = buildTopologyLayout(view, new Set(), { complete: true });
  const expanded = buildTopologyLayout(view, new Set([stackKey]), {
    complete: true,
  });
  const singleton = buildTopologyLayout(
    { ...view, vswitches: [view.vswitches[0]!, view.vswitches[2]!] },
    new Set(),
    { complete: true },
  );
  const incomplete = buildTopologyLayout(view, new Set(), {
    complete: false,
  });

  expect(
    collapsed.nodes
      .filter((value) => value.kind === "vswitch")
      .map((value) => value.key),
  ).toEqual(["vsw-populated-a", "vsw-populated-b"]);
  expect(collapsed.nodes.map((value) => value.key)).not.toContain(
    "vsw-empty-a",
  );
  expect(node(collapsed, stackKey)).toMatchObject({
    kind: "vSwitchStack",
    count: 3,
    vSwitches: [
      { key: "vsw-empty-a", name: "Alpha" },
      { key: "vsw-empty-b", name: "Beta" },
      { key: "vsw-empty-z", name: "Zulu" },
    ],
  });
  expect(node(collapsed, stackKey).size).toEqual({ width: 168, height: 60 });
  expect(node(expanded, stackKey)).toMatchObject({
    kind: "expandedVSwitchStack",
    position: node(collapsed, stackKey).position,
    vSwitches: [
      { key: "vsw-empty-a", name: "Alpha" },
      { key: "vsw-empty-b", name: "Beta" },
      { key: "vsw-empty-z", name: "Zulu" },
    ],
  });
  expect(node(expanded, stackKey).size).toEqual({ width: 808, height: 136 });
  expect(expanded.size.height).toBe(
    node(expanded, "vsw-populated-a").position.y +
      node(expanded, "vsw-populated-a").size.height,
  );
  expect(expanded.size.height).toBeGreaterThan(
    node(expanded, stackKey).position.y + node(expanded, stackKey).size.height,
  );
  for (const key of ["vsw-populated-a", "vsw-populated-b"]) {
    expect(
      rectanglesOverlap(node(expanded, stackKey), node(expanded, key)),
    ).toBe(false);
  }
  expect(node(singleton, "vsw-empty-z")).toMatchObject({
    kind: "vswitch",
    vSwitch: { resource_count: 0, resource_keys: [] },
  });
  expect(
    incomplete.nodes.filter((value) => value.kind === "vSwitchStack"),
  ).toHaveLength(0);
  expect(
    incomplete.nodes
      .filter((value) => value.kind === "vswitch")
      .map((value) => value.key)
      .sort(),
  ).toEqual([
    "vsw-empty-a",
    "vsw-empty-b",
    "vsw-empty-z",
    "vsw-populated-a",
    "vsw-populated-b",
  ]);
});

it("reflows grid cells so expanded stacks cannot overlap adjacent vSwitches", () => {
  const resources: TopologyResource[] = [];
  const members: Record<string, string[]> = {};
  for (let vSwitchIndex = 0; vSwitchIndex < 4; vSwitchIndex += 1) {
    const vSwitchKey = `vsw-${vSwitchIndex}`;
    members[vSwitchKey] = [];
    for (let resourceIndex = 0; resourceIndex < 12; resourceIndex += 1) {
      const key = `ecs-${vSwitchIndex}-${resourceIndex}`;
      resources.push(resource(key, "ecs", "compute", "compute.instance"));
      members[vSwitchKey].push(key);
    }
  }
  const view = vpcView(resources, [], [], members);
  const collapsed = buildTopologyLayout(view, new Set(), { complete: true });

  for (let vSwitchIndex = 0; vSwitchIndex < 4; vSwitchIndex += 1) {
    const stackKey = `stack:vswitch%3Avsw-${vSwitchIndex}:ecs`;
    const expanded = buildTopologyLayout(view, new Set([stackKey]), {
      complete: true,
    });
    const expandedVSwitches = expanded.nodes.filter(
      (value) => value.kind === "vswitch",
    );

    expect(expanded.size.height).toBeGreaterThanOrEqual(collapsed.size.height);
    for (let left = 0; left < expandedVSwitches.length; left += 1) {
      for (let right = left + 1; right < expandedVSwitches.length; right += 1) {
        expect(
          rectanglesOverlap(
            expandedVSwitches[left]!,
            expandedVSwitches[right]!,
          ),
          `${expandedVSwitches[left]!.key} overlaps ${expandedVSwitches[right]!.key} when ${stackKey} is expanded`,
        ).toBe(false);
      }
    }
  }
});

it("lays out 2,000 resources deterministically with bounded indexed visits", () => {
  const { resources, edges, view } = performanceFixture(2);
  expect(resources).toHaveLength(2_000);

  const first = buildTopologyLayout(view, new Set(), { complete: true });
  const second = buildTopologyLayout(view, new Set(), { complete: true });

  expect(second).toEqual(first);
  expect(first.nodes.filter((value) => value.kind === "stack")).toHaveLength(5);
  expect(
    first.nodes
      .filter((value) => value.kind === "stack")
      .every((value) => value.count === 199),
  ).toBe(true);
  expect(first.diagnostics.resourceVisits).toBeLessThanOrEqual(
    resources.length * 16,
  );
  expect(first.diagnostics.membershipVisits).toBeLessThanOrEqual(
    resources.length,
  );
  expect(first.diagnostics.edgeVisits).toBeLessThanOrEqual(edges.length * 4);
});

it("audits every major phase with linear visits and n-log-n comparisons", () => {
  const smallFixture = performanceFixture(1);
  const largeFixture = performanceFixture(2);
  expect(smallFixture.resources).toHaveLength(1_000);
  expect(largeFixture.resources).toHaveLength(2_000);
  const small = buildTopologyLayout(smallFixture.view, new Set(), {
    complete: true,
  }).diagnostics;
  const large = buildTopologyLayout(largeFixture.view, new Set(), {
    complete: true,
  }).diagnostics;

  for (const diagnostics of [small, large]) {
    expect(diagnostics).toEqual(
      expect.objectContaining({
        groupingVisits: expect.any(Number),
        placementVisits: expect.any(Number),
        materializationVisits: expect.any(Number),
        gridVisits: expect.any(Number),
        sortComparisons: expect.any(Number),
      }),
    );
    expect(diagnostics.groupingVisits).toBeGreaterThan(0);
    expect(diagnostics.placementVisits).toBeGreaterThan(0);
    expect(diagnostics.materializationVisits).toBeGreaterThan(0);
    expect(diagnostics.gridVisits).toBeGreaterThan(0);
    expect(diagnostics.sortComparisons).toBeGreaterThan(0);
  }

  const smallLinear = linearVisits(small);
  const largeLinear = linearVisits(large);
  const largeInputUnits =
    largeFixture.resources.length + largeFixture.edges.length;
  expect(largeLinear).toBeLessThanOrEqual(largeInputUnits * 32);
  expect(largeLinear).toBeLessThanOrEqual(smallLinear * 2.3);

  const comparisonBound =
    64 *
    largeFixture.resources.length *
    Math.ceil(Math.log2(largeFixture.resources.length + 1));
  expect(large.sortComparisons).toBeLessThanOrEqual(comparisonBound);
  expect(large.sortComparisons).toBeLessThanOrEqual(
    small.sortComparisons * 2.5,
  );
});

function performanceFixture(scale: 1 | 2): {
  resources: TopologyResource[];
  edges: TopologyResourceEdge[];
  view: VPCTopologyView;
} {
  const resources: TopologyResource[] = [];
  const edges: TopologyResourceEdge[] = [];
  const publicKeys: string[] = [];
  const members: Record<string, string[]> = {};
  const pairCount = scale * 50;
  const isolatedCount = scale * 100 - 1;

  for (let vSwitchIndex = 0; vSwitchIndex < 5; vSwitchIndex += 1) {
    const vSwitchKey = `vsw-${vSwitchIndex}`;
    members[vSwitchKey] = [];
    const securityGroupKey = `security-${vSwitchIndex}`;
    publicKeys.push(securityGroupKey);
    resources.push(
      resource(
        securityGroupKey,
        "security-group",
        "network",
        "network.security_group",
      ),
    );
    for (let pairIndex = 0; pairIndex < pairCount; pairIndex += 1) {
      const ecsKey = `ecs-${vSwitchIndex}-${pairIndex}`;
      const diskKey = `disk-${vSwitchIndex}-${pairIndex}`;
      resources.push(
        resource(ecsKey, "ecs", "compute", "compute.instance"),
        resource(diskKey, "disk", "storage", "storage.block"),
      );
      members[vSwitchKey].push(ecsKey, diskKey);
      edges.push({
        key: `attached-${vSwitchIndex}-${pairIndex}`,
        source_key: diskKey,
        target_key: ecsKey,
        kind: "relationship",
        relation: "attached_to",
      });
      if (pairIndex === 0) {
        edges.push({
          key: `uses-${vSwitchIndex}`,
          source_key: ecsKey,
          target_key: securityGroupKey,
          kind: "relationship",
          relation: "uses",
        });
      }
    }
    for (
      let isolatedIndex = 0;
      isolatedIndex < isolatedCount;
      isolatedIndex += 1
    ) {
      const key = `isolated-${vSwitchIndex}-${isolatedIndex}`;
      resources.push(resource(key, "ecs", "compute", "compute.instance"));
      members[vSwitchKey].push(key);
    }
  }
  return {
    resources,
    edges,
    view: vpcView(resources, edges, publicKeys, members),
  };
}

function linearVisits(diagnostics: TopologyLayout["diagnostics"]): number {
  return (
    diagnostics.resourceVisits +
    diagnostics.membershipVisits +
    diagnostics.edgeVisits +
    diagnostics.groupingVisits +
    diagnostics.placementVisits +
    diagnostics.materializationVisits +
    diagnostics.gridVisits
  );
}

function rectanglesOverlap(
  left: Pick<TopologyLayoutNode, "position" | "size">,
  right: Pick<TopologyLayoutNode, "position" | "size">,
): boolean {
  return !(
    left.position.x + left.size.width <= right.position.x ||
    right.position.x + right.size.width <= left.position.x ||
    left.position.y + left.size.height <= right.position.y ||
    right.position.y + right.size.height <= left.position.y
  );
}
