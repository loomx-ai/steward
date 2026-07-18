import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createElement,
  useEffect,
  useState,
  type ComponentType,
  type CSSProperties,
  type MouseEventHandler,
  type PointerEventHandler,
  type ReactNode,
} from "react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { findAsset, listProviderCatalog, setAssetDirty } from "@/api/client";
import type {
  Asset,
  ProviderCatalogBundle,
  ResourceKind,
  ResourceGraphTopologyView,
  TopologyProjectionWarning,
  TopologyResource,
  VPCTopologyView,
} from "@/api/types";
import { ThemeProvider } from "@/app/ThemeProvider";
import { TooltipProvider } from "@/components/ui/tooltip";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import type { CleanupTarget } from "./cleanupSelection";
import {
  TopologyCanvas,
  type TopologySearchFocusRequest,
} from "./TopologyCanvas";

const flow = vi.hoisted(() => ({
  fitView: vi.fn(),
  getViewport: vi.fn(() => ({ x: 10, y: 20, zoom: 1 })),
  setViewport: vi.fn(),
  zoomIn: vi.fn(),
  zoomOut: vi.fn(),
  zoomTo: vi.fn(),
  nodes: [] as MockFlowNode[],
  onSelectionChange: undefined as
    ((selection: { nodes: MockFlowNode[] }) => void) | undefined,
  onSelectionEnd: undefined as
    ((event: { button?: number; shiftKey: boolean }) => void) | undefined,
}));
const cleanupSelection = vi.hoisted(() => ({
  targets: [] as CleanupTarget[],
  addTargets: vi.fn(),
  removeTarget: vi.fn(),
  removeBatchMember: vi.fn(),
}));

vi.mock("./CleanupSelectionContext", () => ({
  useCleanupSelection: () => cleanupSelection,
}));

vi.mock("@/connections/ActiveConnectionProvider", () => ({
  useRequiredConnection: () => ({
    id: "connection-a",
    provider: "alicloud",
  }),
}));

vi.mock("@/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/client")>()),
  findAsset: vi.fn(),
  listProviderCatalog: vi.fn(),
  setAssetDirty: vi.fn(),
}));

interface MockFlowNode {
  id: string;
  type?: string;
  selectable?: boolean;
  parentId?: string;
  extent?: string;
  position: { x: number; y: number };
  style?: CSSProperties;
  className?: string;
  data: Record<string, unknown>;
}

interface MockFlowEdge {
  id: string;
  source: string;
  target: string;
  type?: string;
  ariaLabel?: string;
  label?: ReactNode;
  labelStyle?: {
    fill?: string;
    fontSize?: number;
    fontWeight?: number;
    opacity?: number;
  };
  labelShowBg?: boolean;
  markerEnd?: {
    type?: string;
  };
  style?: CSSProperties;
  className?: string;
}

vi.mock("@xyflow/react", () => ({
  ReactFlow: ({
    children,
    colorMode,
    edges,
    nodeTypes,
    nodes,
    nodesFocusable,
    elementsSelectable,
    selectionOnDrag,
    panOnDrag,
    minZoom,
    onInit,
    onClickCapture,
    onPaneClick,
    onPointerCancelCapture,
    onPointerDownCapture,
    onPointerMoveCapture,
    onPointerUpCapture,
    onSelectionChange,
    onSelectionEnd,
    proOptions,
    ...unsupportedProps
  }: {
    children: ReactNode;
    colorMode: string;
    edges: MockFlowEdge[];
    nodeTypes: Record<string, ComponentType<Record<string, unknown>>>;
    nodes: MockFlowNode[];
    nodesFocusable?: boolean;
    elementsSelectable?: boolean;
    selectionOnDrag?: boolean;
    panOnDrag?: boolean | number[];
    minZoom?: number;
    onInit?: (instance: {
      fitView: typeof flow.fitView;
      getViewport: typeof flow.getViewport;
      setViewport: typeof flow.setViewport;
      zoomIn: typeof flow.zoomIn;
      zoomOut: typeof flow.zoomOut;
      zoomTo: typeof flow.zoomTo;
    }) => void;
    onClickCapture?: MouseEventHandler<HTMLDivElement>;
    onPaneClick?: () => void;
    onPointerCancelCapture?: PointerEventHandler<HTMLDivElement>;
    onPointerDownCapture?: PointerEventHandler<HTMLDivElement>;
    onPointerMoveCapture?: PointerEventHandler<HTMLDivElement>;
    onPointerUpCapture?: PointerEventHandler<HTMLDivElement>;
    onSelectionChange?: (selection: { nodes: MockFlowNode[] }) => void;
    onSelectionEnd?: (event: { button?: number; shiftKey: boolean }) => void;
    proOptions?: { hideAttribution?: boolean };
  }) => {
    flow.nodes = nodes;
    flow.onSelectionChange = onSelectionChange;
    flow.onSelectionEnd = onSelectionEnd;
    onInit?.({
      fitView: flow.fitView,
      getViewport: flow.getViewport,
      setViewport: flow.setViewport,
      zoomIn: flow.zoomIn,
      zoomOut: flow.zoomOut,
      zoomTo: flow.zoomTo,
    });
    return (
      <div
        data-testid="react-flow"
        onClickCapture={onClickCapture}
        onPointerCancelCapture={onPointerCancelCapture}
        onPointerDownCapture={onPointerDownCapture}
        onPointerMoveCapture={onPointerMoveCapture}
        onPointerUpCapture={onPointerUpCapture}
        data-color-mode={colorMode}
        data-nodes-focusable={nodesFocusable}
        data-elements-selectable={elementsSelectable}
        data-has-unsupported-nodes-selectable={Object.hasOwn(
          unsupportedProps,
          "nodesSelectable",
        )}
        data-selection-on-drag={selectionOnDrag}
        data-pan-on-drag={JSON.stringify(panOnDrag)}
        data-min-zoom={minZoom}
        data-hide-attribution={proOptions?.hideAttribution}
      >
        {nodes.map((node) => {
          const Renderer = nodeTypes[node.type ?? ""];
          return (
            <div
              key={node.id}
              className="react-flow__node"
              data-testid={`flow-node-${node.id}`}
              data-node-type={node.type}
              data-node-selectable={node.selectable}
              data-parent-id={node.parentId}
              data-extent={node.extent}
              data-position={`${node.position.x},${node.position.y}`}
              data-node-class={node.className}
              style={node.style}
            >
              {Renderer
                ? createElement(Renderer, {
                    id: node.id,
                    data: node.data,
                    selected: false,
                    type: node.type,
                    dragging: false,
                    zIndex: 0,
                    selectable: node.selectable ?? false,
                    deletable: false,
                    isConnectable: false,
                    positionAbsoluteX: node.position.x,
                    positionAbsoluteY: node.position.y,
                  })
                : null}
            </div>
          );
        })}
        {edges.map((edge) => (
          <div
            key={edge.id}
            data-testid={`flow-edge-${edge.id}`}
            data-edge-class={edge.className}
            data-source={edge.source}
            data-target={edge.target}
            data-edge-type={edge.type}
            data-aria-label={edge.ariaLabel}
            data-marker-end={edge.markerEnd?.type}
            data-stroke-dasharray={edge.style?.strokeDasharray}
            data-stroke-width={edge.style?.strokeWidth}
            data-opacity={edge.style?.opacity}
            data-stroke={edge.style?.stroke}
            data-label-fill={edge.labelStyle?.fill}
            data-label-font-size={edge.labelStyle?.fontSize}
            data-label-opacity={edge.labelStyle?.opacity}
            data-label-show-bg={edge.labelShowBg}
          >
            {edge.label}
          </div>
        ))}
        <button type="button" data-testid="flow-pane" onClick={onPaneClick}>
          pane
        </button>
        {children}
      </div>
    );
  },
  Background: () => null,
  Controls: ({ className }: { className?: string }) => (
    <div data-testid="react-flow-controls" data-class={className} />
  ),
  Handle: () => null,
  Position: { Left: "left", Right: "right" },
}));

const cleanup = { selectable: false, potential_blockers: 0 };

function resource(
  key: string,
  name: string,
  typeName: string,
  domain: TopologyResource["domain"],
): TopologyResource {
  return {
    key,
    asset_id: key,
    resource_kind_id:
      typeName === "ECS Instance" ? "ecs" : typeName.toLowerCase(),
    name,
    native_id: `${key}-native-id`,
    type_name: typeName,
    domain,
    finding_count: 0,
    actionable: false,
    cleanup,
  };
}

const resources = [
  resource("sg", "shared-security-group", "Security Group", "network"),
  resource("ecs-a", "production-api", "ECS Instance", "compute"),
  resource("ecs-b", "production-worker", "ECS Instance", "compute"),
  resource("ecs-c", "production-cron", "ECS Instance", "compute"),
  resource("isolated-a", "isolated-a", "ECS Instance", "compute"),
  resource("isolated-b", "isolated-b", "ECS Instance", "compute"),
  resource("isolated-c", "isolated-c", "ECS Instance", "compute"),
  resource("disk-unrelated", "archive-disk", "Disk", "storage"),
];

const view: VPCTopologyView = {
  kind: "vpc",
  region: { key: "region-a", name: "杭州", native_id: "cn-hangzhou" },
  vpc: { key: "vpc-a", name: "production-vpc", native_id: "vpc-a" },
  public_resource_keys: [],
  vswitches: [
    {
      key: "vsw-a",
      asset_id: "asset-vsw-a",
      name: "application-zone",
      native_id: "vsw-a",
      zone: "cn-hangzhou-h",
      resource_count: 7,
      resource_keys: resources.slice(0, 7).map((value) => value.key),
    },
    {
      key: "vsw-b",
      asset_id: "asset-vsw-b",
      name: "storage-zone",
      native_id: "vsw-b",
      zone: "cn-hangzhou-i",
      resource_count: 1,
      resource_keys: ["disk-unrelated"],
    },
  ],
  resources,
  edges: ["a", "b", "c"].map((suffix) => ({
    key: `uses-${suffix}`,
    source_key: `ecs-${suffix}`,
    target_key: "sg",
    kind: "relationship" as const,
    relation: "uses",
  })),
};

const warnings: TopologyProjectionWarning[] = [
  {
    code: "topology.endpoint_unavailable",
    relation_key: "missing-relation",
    message: "one endpoint is unavailable",
  },
];

const consoleResourceKinds = new Map<string, ResourceKind>([
  [
    "alicloud:ACS::VPC::VSwitch",
    {
      id: "alicloud:ACS::VPC::VSwitch",
      provider: "alicloud",
      native_type: "ACS::VPC::VSwitch",
      capabilities: ["indexed"],
      console_link_template:
        "https://vpc.console.aliyun.com/vpc/{regionId}/switches/{nativeId}",
      bundle_revision: "runtime-1",
    },
  ],
  [
    "alicloud:ACS::ECS::Instance",
    {
      id: "alicloud:ACS::ECS::Instance",
      provider: "alicloud",
      native_type: "ACS::ECS::Instance",
      capabilities: ["indexed"],
      console_link_template:
        "https://ecs.console.aliyun.com/server/region/{regionId}?instanceId={nativeId}",
      bundle_revision: "runtime-1",
    },
  ],
  [
    "alicloud:ACS::OSS::Bucket",
    {
      id: "alicloud:ACS::OSS::Bucket",
      provider: "alicloud",
      native_type: "ACS::OSS::Bucket",
      capabilities: ["indexed"],
      console_link_template:
        "https://oss.console.aliyun.com/bucket/oss-{regionId}/{nativeId}/object",
      bundle_revision: "runtime-1",
    },
  ],
  [
    "alicloud:ACS::CEN::TransitRouter",
    {
      id: "alicloud:ACS::CEN::TransitRouter",
      provider: "alicloud",
      native_type: "ACS::CEN::TransitRouter",
      capabilities: ["indexed"],
      console_link_template:
        "https://cen.console.aliyun.com/cen/attachment/{regionId}/{parentId}/{nativeId}",
      bundle_revision: "runtime-1",
    },
  ],
]);

beforeEach(() => {
  flow.fitView.mockClear();
  flow.getViewport.mockClear();
  flow.setViewport.mockClear();
  flow.zoomIn.mockClear();
  flow.zoomOut.mockClear();
  flow.zoomTo.mockClear();
  flow.nodes = [];
  flow.onSelectionChange = undefined;
  flow.onSelectionEnd = undefined;
  cleanupSelection.targets = [];
  cleanupSelection.addTargets.mockReset();
  cleanupSelection.removeTarget.mockReset();
  cleanupSelection.removeBatchMember.mockReset();
  vi.mocked(setAssetDirty)
    .mockReset()
    .mockImplementation(
      async (connectionID, id, dirty) =>
        ({
          id,
          dirty,
          identity: {
            connection_id: connectionID,
            provider: "alicloud",
            partition: "aliyun",
            native_type: "ACS::ECS::Instance",
            native_id: id,
          },
          scope_id: "region-a",
          resource_kind_id: "ecs",
          capabilities: [],
          first_seen_at: "2026-08-05T00:00:00Z",
          last_seen_at: "2026-08-05T00:00:00Z",
        }) satisfies Asset,
    );
  vi.mocked(findAsset)
    .mockReset()
    .mockImplementation(async (_connectionId, assetId) => {
      const zoneId = assetId.startsWith("asset-vsw-empty")
        ? "cn-hangzhou-j"
        : assetId === "asset-vsw-b"
          ? "cn-hangzhou-i"
          : "cn-hangzhou-h";
      const result: Asset = {
        id: assetId,
        identity: {
          provider: "alicloud",
          partition: "aliyun",
          connection_id: "connection-a",
          native_type: "ACS::VPC::VSwitch",
          native_id: assetId.replace(/^asset-/, ""),
        },
        scope_id: "cn-hangzhou",
        resource_kind_id: "alicloud:ACS::VPC::VSwitch",
        name: assetId,
        location: zoneId,
        capabilities: ["indexed"],
        normalized: {
          cidrBlock: "10.0.0.0/24",
          vpcId: "vpc-a",
          zoneId,
        },
        first_seen_at: "2026-07-31T00:00:00Z",
        last_seen_at: "2026-07-31T01:00:00Z",
      };
      return result;
    });
  const catalog: ProviderCatalogBundle[] = [
    {
      provider: "alicloud",
      revision: "compiled-1",
      hash: "hash-1",
      kinds: [
        {
          id: "alicloud:ACS::VPC::VSwitch",
          provider: "alicloud",
          native_type: "ACS::VPC::VSwitch",
          capabilities: ["indexed"],
          display_name: "vSwitch",
          field_display_names: {
            cidrBlock: { "en-US": "CIDR block", "zh-CN": "网段" },
          },
          bundle_revision: "runtime-1",
        },
      ],
      kinds_revision: "runtime-1",
      specs: [],
    },
  ];
  vi.mocked(listProviderCatalog).mockReset().mockResolvedValue(catalog);
  localStorage.clear();
  localStorage.setItem("steward.theme", "dark");
  vi.stubGlobal(
    "matchMedia",
    vi.fn().mockReturnValue({
      matches: false,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }),
  );
  vi.stubGlobal(
    "ResizeObserver",
    class ResizeObserver {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
});

async function waitForStackFocusHandoff() {
  await new Promise<void>((resolve) => {
    requestAnimationFrame(() => resolve());
  });
}

async function clearInitialFitView() {
  await waitForStackFocusHandoff();
  flow.fitView.mockClear();
}

function renderCanvas({
  onSelectResource = vi.fn(),
  topologyView = view,
  highlightedNodeKey,
  searchFocusRequest,
  focusedCleanupTargetKey,
}: {
  onSelectResource?: (
    resource: TopologyResource,
    trigger: HTMLElement | null,
  ) => void;
  topologyView?: ResourceGraphTopologyView | VPCTopologyView;
  highlightedNodeKey?: string;
  searchFocusRequest?: TopologySearchFocusRequest;
  focusedCleanupTargetKey?: string;
} = {}) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });

  function Harness() {
    const [selectedKey, setSelectedKey] = useState<string>();
    return (
      <QueryClientProvider client={queryClient}>
        <ThemeProvider>
          <TooltipProvider>
            <LocaleProvider>
              <MemoryRouter>
                <TopologyCanvas
                  view={topologyView}
                  complete
                  warnings={warnings}
                  resourceKinds={consoleResourceKinds}
                  selectedResourceKey={selectedKey}
                  highlightedNodeKey={highlightedNodeKey}
                  searchFocusRequest={searchFocusRequest}
                  focusedCleanupTargetKey={focusedCleanupTargetKey}
                  onSelectResource={(selected, trigger) => {
                    setSelectedKey(selected.key);
                    onSelectResource(selected, trigger);
                  }}
                  onClearFocus={() => setSelectedKey(undefined)}
                />
              </MemoryRouter>
            </LocaleProvider>
          </TooltipProvider>
        </ThemeProvider>
      </QueryClientProvider>
    );
  }

  return render(<Harness />);
}

it("focuses and highlights a node located by its cleanup target key", async () => {
  renderCanvas({
    highlightedNodeKey: "asset:sg",
    focusedCleanupTargetKey: "asset:sg",
  });

  expect(
    within(screen.getByTestId("flow-node-sg")).getByRole("button", {
      name: /shared-security-group/,
    }),
  ).toHaveAttribute("data-search-highlighted", "true");
  await waitFor(() =>
    expect(flow.fitView).toHaveBeenCalledWith(
      expect.objectContaining({
        nodes: [expect.objectContaining({ id: "sg" })],
        padding: 0.35,
        minZoom: 1,
        maxZoom: 1.8,
      }),
    ),
  );
});

it("uses a bounded node fit when Enter activates a highlighted search resource", async () => {
  renderCanvas({
    highlightedNodeKey: "sg",
    searchFocusRequest: {
      id: 1,
      nodeKey: "sg",
      scope: false,
    },
  });

  expect(
    within(screen.getByTestId("flow-node-sg")).getByRole("button", {
      name: /shared-security-group/,
    }),
  ).toHaveAttribute("data-search-highlighted", "true");
  await waitFor(() =>
    expect(flow.fitView).toHaveBeenCalledWith({
      nodes: [expect.objectContaining({ id: "sg" })],
      padding: 0.35,
      minZoom: 1,
      maxZoom: 1.8,
    }),
  );
});

it("keeps a searched vSwitch at a readable scale", async () => {
  renderCanvas({
    highlightedNodeKey: "vsw-a",
    searchFocusRequest: {
      id: 2,
      nodeKey: "vsw-a",
      scope: false,
    },
  });

  await waitFor(() =>
    expect(flow.fitView).toHaveBeenCalledWith({
      nodes: [expect.objectContaining({ id: "vsw-a" })],
      padding: 0.35,
      minZoom: 1,
      maxZoom: 1.8,
    }),
  );
});

it("collapses member resources into their parent and expands the local group", async () => {
  const user = userEvent.setup();
  const parent = {
    ...resource("cen", "ros-ut-beijing", "CEN Instance", "network"),
    native_id: "cen-b46rksvi4fcjop46b6",
  };
  const children = ["a", "b", "c"].map((suffix) => ({
    ...resource(
      `router-${suffix}`,
      `tr-${suffix}`,
      "CEN Transit Router",
      "network",
    ),
    resource_kind_id: "alicloud:ACS::CEN::TransitRouter",
    native_id:
      suffix === "a" ? "tr-j6ca472kucep5zasasdfz" : `tr-${suffix}-native-id`,
    console_link_values: {
      regionId: "cn-hongkong",
      parentId: parent.native_id,
    },
  }));
  const topologyView: ResourceGraphTopologyView = {
    kind: "resource_graph",
    context: { key: "account-global", name: "Global resources" },
    resources: [parent, ...children],
    edges: children.map((child) => ({
      key: `${child.key}-membership`,
      source_key: child.key,
      target_key: parent.key,
      kind: "relationship",
      relation: "member_of",
    })),
  };
  renderCanvas({ topologyView });

  expect(screen.getByTestId("flow-node-cen")).toHaveAttribute(
    "data-node-type",
    "resourceGroup",
  );
  expect(screen.queryByTestId("flow-node-router-a")).not.toBeInTheDocument();
  expect(screen.queryByTestId(/flow-edge-/)).not.toBeInTheDocument();

  const expand = screen.getByRole("button", {
    name: "Expand 3 child resources for ros-ut-beijing",
  });
  await user.click(expand);

  expect(screen.getByTestId("flow-node-cen")).toHaveAttribute(
    "data-node-type",
    "expandedResourceGroup",
  );
  expect(screen.getByTestId("flow-node-router-a")).toHaveAttribute(
    "data-parent-id",
    "cen",
  );
  expect(screen.queryByTestId(/flow-edge-/)).not.toBeInTheDocument();

  fireEvent.contextMenu(
    within(screen.getByTestId("flow-node-router-a")).getByRole("button", {
      name: "tr-a",
    }),
  );
  expect(
    await screen.findByRole("menuitem", { name: "Cloud console" }),
  ).toHaveAttribute(
    "href",
    "https://cen.console.aliyun.com/cen/attachment/cn-hongkong/cen-b46rksvi4fcjop46b6/tr-j6ca472kucep5zasasdfz",
  );
  await user.keyboard("{Escape}");

  await user.click(
    screen.getByRole("button", {
      name: "Collapse child resources for ros-ut-beijing",
    }),
  );
  expect(screen.queryByTestId("flow-node-router-a")).not.toBeInTheDocument();
  expect(screen.getByTestId("flow-node-cen")).toHaveAttribute(
    "data-node-type",
    "resourceGroup",
  );
});

it("automatically reveals a grouped child selected from canvas search", async () => {
  const parent = resource("cen", "ros-ut-beijing", "CEN Instance", "network");
  const children = ["a", "b"].map((suffix) =>
    resource(
      `router-${suffix}`,
      `tr-${suffix}`,
      "CEN Transit Router",
      "network",
    ),
  );
  const topologyView: ResourceGraphTopologyView = {
    kind: "resource_graph",
    context: { key: "account-global", name: "Global resources" },
    resources: [parent, ...children],
    edges: children.map((child) => ({
      key: `${child.key}-membership`,
      source_key: child.key,
      target_key: parent.key,
      kind: "relationship",
      relation: "member_of",
    })),
  };
  renderCanvas({
    topologyView,
    highlightedNodeKey: "router-b",
    searchFocusRequest: {
      id: 7,
      nodeKey: "router-b",
      scope: false,
    },
  });

  expect(screen.getByTestId("flow-node-router-b")).toHaveAttribute(
    "data-parent-id",
    "cen",
  );
  expect(
    within(screen.getByTestId("flow-node-router-b")).getByRole("button", {
      name: /tr-b/,
    }),
  ).toHaveAttribute("data-search-highlighted", "true");
  await waitFor(() =>
    expect(flow.fitView).toHaveBeenCalledWith({
      nodes: [expect.objectContaining({ id: "router-b" })],
      padding: 0.35,
      minZoom: 1,
      maxZoom: 1.8,
    }),
  );
  await waitForStackFocusHandoff();
  expect(flow.fitView).toHaveBeenLastCalledWith({
    nodes: [expect.objectContaining({ id: "router-b" })],
    padding: 0.35,
    minZoom: 1,
    maxZoom: 1.8,
  });
});

function completeFlowSelection(
  nodeIDs: readonly string[],
  { shiftKey = false }: { shiftKey?: boolean } = {},
) {
  const selectedNodes = flow.nodes.filter((node) => nodeIDs.includes(node.id));
  act(() => {
    flow.onSelectionChange?.({ nodes: selectedNodes });
    flow.onSelectionEnd?.({ shiftKey });
  });
}

it("switches between arrow selection and hand panning modes", async () => {
  const onSelectResource = vi.fn();
  const user = userEvent.setup();
  renderCanvas({ onSelectResource });

  const reactFlow = screen.getByTestId("react-flow");
  expect(reactFlow).toHaveAttribute("data-selection-on-drag", "true");
  expect(reactFlow).toHaveAttribute("data-elements-selectable", "true");
  expect(reactFlow).toHaveAttribute(
    "data-has-unsupported-nodes-selectable",
    "false",
  );
  expect(reactFlow).toHaveAttribute("data-pan-on-drag", "[1]");
  expect(screen.getByTestId("flow-node-ecs-a")).toHaveAttribute(
    "data-node-selectable",
    "true",
  );
  expect(screen.getByTestId("flow-node-vsw-a")).toHaveAttribute(
    "data-node-selectable",
    "true",
  );
  expect(screen.queryByTestId("react-flow-controls")).not.toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: "Box select" }),
  ).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Select" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  expect(screen.getByRole("button", { name: "Pan" })).toHaveAttribute(
    "aria-pressed",
    "false",
  );

  await user.click(screen.getByRole("button", { name: "Pan" }));
  expect(reactFlow).toHaveAttribute("data-selection-on-drag", "false");
  expect(reactFlow).toHaveAttribute("data-elements-selectable", "false");
  expect(reactFlow).toHaveAttribute("data-pan-on-drag", "true");
  expect(screen.getByTestId("topology-canvas")).toHaveAttribute(
    "data-canvas-mode",
    "pan",
  );
  await user.click(screen.getByRole("button", { name: "production-api" }));
  expect(onSelectResource).not.toHaveBeenCalled();

  await user.click(screen.getByRole("button", { name: "Select" }));
  expect(reactFlow).toHaveAttribute("data-selection-on-drag", "true");
  expect(reactFlow).toHaveAttribute("data-pan-on-drag", "[1]");

  await user.click(screen.getByRole("button", { name: "production-api" }));
  expect(onSelectResource).toHaveBeenCalledOnce();
  await user.click(screen.getByRole("button", { name: "production-worker" }));
  await user.click(
    screen.getByRole("button", { name: /ECS Instance.*3 resources/ }),
  );
  expect(onSelectResource).toHaveBeenCalledTimes(2);
  expect(
    screen.getByTestId("flow-node-stack:vswitch%3Avsw-a:ecs"),
  ).toHaveAttribute("data-node-type", "stack");
});

it("selects resources on click, appends with Shift, and applies the context action to the selection", async () => {
  const user = userEvent.setup();
  renderCanvas();

  const api = screen.getByRole("button", { name: "production-api" });
  const worker = screen.getByRole("button", { name: "production-worker" });
  await user.click(api);
  expect(
    screen.getByRole("toolbar", { name: "Box selection actions" }),
  ).toHaveTextContent("1 selected");

  fireEvent.click(worker, { shiftKey: true });
  expect(
    screen.getByRole("toolbar", { name: "Box selection actions" }),
  ).toHaveTextContent("2 selected");
  expect(api).toHaveAttribute("aria-pressed", "true");
  expect(api).toHaveAttribute("aria-current", "true");
  expect(worker).toHaveAttribute("aria-pressed", "true");
  expect(worker).not.toHaveAttribute("aria-current");

  fireEvent.click(worker, { shiftKey: true });
  expect(worker).toHaveAttribute("aria-pressed", "false");
  expect(
    screen.getByRole("toolbar", { name: "Box selection actions" }),
  ).toHaveTextContent("1 selected");
  fireEvent.click(worker, { shiftKey: true });
  expect(worker).toHaveAttribute("aria-pressed", "true");

  fireEvent.contextMenu(worker);
  await user.click(
    await screen.findByRole("menuitem", { name: "Add to resource list" }),
  );
  expect(cleanupSelection.addTargets).toHaveBeenCalledExactlyOnceWith([
    expect.objectContaining({ key: "asset:ecs-a", kind: "resource" }),
    expect.objectContaining({ key: "asset:ecs-b", kind: "resource" }),
  ]);
});

it("marks every selected resource dirty through one context action", async () => {
  const user = userEvent.setup();
  renderCanvas();

  const api = screen.getByRole("button", { name: "production-api" });
  const worker = screen.getByRole("button", { name: "production-worker" });
  await user.click(api);
  fireEvent.click(worker, { shiftKey: true });

  fireEvent.contextMenu(worker);
  await user.click(
    await screen.findByRole("menuitem", { name: "Mark as dirty resource" }),
  );

  await waitFor(() => expect(setAssetDirty).toHaveBeenCalledTimes(2));
  expect(setAssetDirty).toHaveBeenCalledWith("connection-a", "ecs-a", true);
  expect(setAssetDirty).toHaveBeenCalledWith("connection-a", "ecs-b", true);
});

it("toggles a selected resource off on the next click", async () => {
  const user = userEvent.setup();
  renderCanvas();

  const api = screen.getByRole("button", { name: "production-api" });
  await user.click(api);
  expect(api).toHaveAttribute("aria-pressed", "true");

  await user.click(api);
  expect(api).toHaveAttribute("aria-pressed", "false");
  expect(
    screen.queryByRole("toolbar", { name: "Box selection actions" }),
  ).not.toBeInTheDocument();
});

it("selects vSwitches by click, Shift click, box selection, and context cleanup", async () => {
  const user = userEvent.setup();
  renderCanvas();

  const application = screen.getByRole("button", {
    name: /application-zone.*vsw-a/,
  });
  const storage = screen.getByRole("button", {
    name: /storage-zone.*vsw-b/,
  });
  await user.click(application);
  fireEvent.click(storage, { shiftKey: true });
  expect(application).toHaveAttribute("aria-pressed", "true");
  expect(storage).toHaveAttribute("aria-pressed", "true");
  expect(
    screen.getByRole("toolbar", { name: "Box selection actions" }),
  ).toHaveTextContent("2 selected");

  fireEvent.contextMenu(storage);
  await user.click(
    await screen.findByRole("menuitem", { name: "Add to resource list" }),
  );
  expect(cleanupSelection.addTargets).toHaveBeenCalledWith([
    expect.objectContaining({ key: "asset:asset-vsw-a" }),
    expect.objectContaining({ key: "asset:asset-vsw-b" }),
  ]);

  cleanupSelection.addTargets.mockClear();
  completeFlowSelection(["vsw-a"]);
  await user.click(screen.getByRole("button", { name: "Add selected" }));
  expect(cleanupSelection.addTargets).toHaveBeenCalledWith([
    expect.objectContaining({ key: "asset:asset-vsw-a" }),
  ]);
});

it("pans the viewport in hand mode when a primary-button drag starts on a resource and suppresses the trailing click", async () => {
  const onSelectResource = vi.fn();
  const user = userEvent.setup();
  renderCanvas({ onSelectResource });
  await user.click(screen.getByRole("button", { name: "Pan" }));

  const resourceButton = screen.getByRole("button", {
    name: "production-api",
  });
  fireEvent.pointerDown(resourceButton, {
    button: 0,
    pointerId: 7,
    clientX: 100,
    clientY: 120,
  });
  fireEvent.pointerMove(resourceButton, {
    button: 0,
    pointerId: 7,
    clientX: 135,
    clientY: 148,
  });
  expect(screen.getByTestId("topology-canvas")).toHaveAttribute(
    "data-panning",
    "true",
  );
  fireEvent.pointerUp(resourceButton, {
    button: 0,
    pointerId: 7,
    clientX: 135,
    clientY: 148,
  });
  fireEvent.click(resourceButton);

  expect(flow.setViewport).toHaveBeenCalledWith({
    x: 45,
    y: 48,
    zoom: 1,
  });
  expect(screen.getByTestId("topology-canvas")).toHaveAttribute(
    "data-panning",
    "false",
  );
  expect(onSelectResource).not.toHaveBeenCalled();
});

it("keeps resource presses in arrow mode as clicks instead of capturing them for canvas panning", () => {
  renderCanvas();

  const reactFlow = screen.getByTestId("react-flow");
  const setPointerCapture = vi.fn();
  Object.defineProperty(reactFlow, "setPointerCapture", {
    configurable: true,
    value: setPointerCapture,
  });
  const resourceButton = screen.getByRole("button", {
    name: "production-api",
  });

  fireEvent.pointerDown(resourceButton, {
    button: 0,
    pointerId: 9,
    clientX: 100,
    clientY: 120,
  });
  fireEvent.pointerUp(resourceButton, {
    button: 0,
    pointerId: 9,
    clientX: 100,
    clientY: 120,
  });
  fireEvent.click(resourceButton);

  expect(setPointerCapture).not.toHaveBeenCalled();
  expect(resourceButton).toHaveAttribute("aria-pressed", "true");
  expect(
    screen.getByRole("toolbar", { name: "Box selection actions" }),
  ).toHaveTextContent("1 selected");
});

it("uses the icon toolbar to zoom out, fit the view, and zoom in", async () => {
  const user = userEvent.setup();
  renderCanvas();
  await clearInitialFitView();

  await user.click(screen.getByRole("button", { name: "Zoom out" }));
  await user.click(screen.getByRole("button", { name: "Fit view" }));
  await user.click(screen.getByRole("button", { name: "Zoom in" }));
  const zoomInput = screen.getByLabelText("Zoom level");
  await user.clear(zoomInput);
  await user.type(zoomInput, "72{Enter}");

  expect(flow.zoomOut).toHaveBeenCalledOnce();
  expect(flow.fitView).toHaveBeenCalledExactlyOnceWith({
    padding: 0.06,
    maxZoom: 1.8,
  });
  expect(flow.zoomIn).toHaveBeenCalledOnce();
  expect(flow.zoomTo).toHaveBeenCalledWith(0.72);
});

it("replaces box candidates, appends with Shift, and commits resolved targets only on confirmation", async () => {
  const user = userEvent.setup();
  renderCanvas();

  completeFlowSelection(["sg"]);
  expect(
    screen.getByRole("toolbar", { name: "Box selection actions" }),
  ).toHaveTextContent("1 selected");
  expect(cleanupSelection.addTargets).not.toHaveBeenCalled();

  completeFlowSelection(["ecs-a"]);
  expect(
    screen.getByRole("toolbar", { name: "Box selection actions" }),
  ).toHaveTextContent("1 selected");

  completeFlowSelection(["sg"], { shiftKey: true });
  expect(
    screen.getByRole("toolbar", { name: "Box selection actions" }),
  ).toHaveTextContent("2 selected");
  expect(cleanupSelection.addTargets).not.toHaveBeenCalled();

  await user.click(screen.getByRole("button", { name: "Add selected" }));
  expect(cleanupSelection.addTargets).toHaveBeenCalledOnce();
  expect(cleanupSelection.addTargets).toHaveBeenCalledWith([
    expect.objectContaining({
      key: "asset:ecs-a",
      kind: "resource",
      connectionId: "connection-a",
      ancestryKeys: ["region-a", "vpc-a", "asset:ecs-a"],
      locationContext: {
        region: { key: "region-a", name: "杭州", native_id: "cn-hangzhou" },
        vpc: { key: "vpc-a", name: "production-vpc", native_id: "vpc-a" },
      },
      selector: expect.objectContaining({ kind: "asset", asset_id: "ecs-a" }),
    }),
    expect.objectContaining({
      key: "asset:sg",
      kind: "resource",
      connectionId: "connection-a",
      ancestryKeys: ["region-a", "vpc-a", "asset:sg"],
      locationContext: {
        region: { key: "region-a", name: "杭州", native_id: "cn-hangzhou" },
        vpc: { key: "vpc-a", name: "production-vpc", native_id: "vpc-a" },
      },
      selector: expect.objectContaining({ kind: "asset", asset_id: "sg" }),
    }),
  ]);
  expect(
    screen.queryByRole("toolbar", { name: "Box selection actions" }),
  ).not.toBeInTheDocument();
});

it("selects collapsed and expanded resource stacks, preferring a stack over children and vSwitch frames", async () => {
  const user = userEvent.setup();
  renderCanvas();

  completeFlowSelection(["stack:vswitch%3Avsw-a:ecs"]);
  const collapsedStack = screen.getByRole("button", {
    name: /ECS Instance.*3 resources/,
  });
  expect(collapsedStack).toHaveAttribute("aria-pressed", "true");
  expect(collapsedStack).toHaveClass("ring-2", "ring-ring");
  await user.click(screen.getByRole("button", { name: "Add selected" }));
  expect(cleanupSelection.addTargets).toHaveBeenCalledWith([
    expect.objectContaining({
      key: "stack:vswitch%3Avsw-a:ecs",
      kind: "resource_batch",
      connectionId: "connection-a",
      memberAssetIds: ["isolated-a", "isolated-b", "isolated-c"],
    }),
  ]);

  cleanupSelection.addTargets.mockClear();
  await user.click(screen.getByRole("button", { name: "Expand ECS Instance" }));
  completeFlowSelection(["vsw-a", "stack:vswitch%3Avsw-a:ecs", "isolated-a"]);

  expect(
    screen.getByRole("toolbar", { name: "Box selection actions" }),
  ).toHaveTextContent("2 selected");
  await user.click(screen.getByRole("button", { name: "Add selected" }));
  expect(cleanupSelection.addTargets).toHaveBeenCalledWith([
    expect.objectContaining({
      key: "asset:asset-vsw-a",
      kind: "resource",
    }),
    expect.objectContaining({
      key: "stack:vswitch%3Avsw-a:ecs",
      kind: "resource_batch",
      connectionId: "connection-a",
      memberAssetIds: ["isolated-a", "isolated-b", "isolated-c"],
    }),
  ]);
});

it("clears focus and candidates on a pane click", async () => {
  const user = userEvent.setup();
  renderCanvas();

  await user.click(screen.getByRole("button", { name: "production-api" }));
  expect(screen.getByTestId("topology-canvas")).toHaveAttribute(
    "data-selected-resource-key",
    "ecs-a",
  );
  await user.click(screen.getByTestId("flow-pane"));
  expect(screen.getByTestId("topology-canvas")).not.toHaveAttribute(
    "data-selected-resource-key",
  );

  completeFlowSelection(["ecs-a"]);
  expect(
    screen.getByRole("toolbar", { name: "Box selection actions" }),
  ).toBeVisible();
  await user.click(screen.getByTestId("flow-pane"));
  expect(
    screen.queryByRole("toolbar", { name: "Box selection actions" }),
  ).not.toBeInTheDocument();
});

it("lets an open menu consume Escape before candidates and resource focus", async () => {
  const user = userEvent.setup();
  renderCanvas();

  await user.click(screen.getByRole("button", { name: "production-api" }));
  completeFlowSelection(["sg"]);

  const worker = screen.getByRole("button", { name: "production-worker" });
  fireEvent.contextMenu(worker);
  expect(await screen.findByRole("menu")).toBeVisible();
  expect(screen.getByTestId("topology-canvas")).toHaveAttribute(
    "data-selected-resource-key",
    "ecs-a",
  );

  await user.keyboard("{Escape}");
  await waitFor(() =>
    expect(screen.queryByRole("menu")).not.toBeInTheDocument(),
  );
  expect(
    screen.getByRole("toolbar", { name: "Box selection actions" }),
  ).toBeVisible();
  expect(screen.getByTestId("topology-canvas")).toHaveAttribute(
    "data-selected-resource-key",
    "ecs-a",
  );

  await user.keyboard("{Escape}");
  expect(
    screen.queryByRole("toolbar", { name: "Box selection actions" }),
  ).not.toBeInTheDocument();
  expect(screen.getByTestId("topology-canvas")).not.toHaveAttribute(
    "data-selected-resource-key",
  );
});

it("wires resource and stack context actions to cleanup and exact stack members", async () => {
  const user = userEvent.setup();
  renderCanvas();

  fireEvent.contextMenu(screen.getByRole("button", { name: "production-api" }));
  await user.click(
    await screen.findByRole("menuitem", { name: "Add to resource list" }),
  );
  expect(cleanupSelection.addTargets).toHaveBeenLastCalledWith([
    expect.objectContaining({
      key: "asset:ecs-a",
      selector: expect.objectContaining({ asset_id: "ecs-a" }),
    }),
  ]);

  fireEvent.contextMenu(
    screen.getByRole("button", { name: /ECS Instance.*3 resources/ }),
  );
  await user.click(
    await screen.findByRole("menuitem", {
      name: "View included resources",
    }),
  );
  const dialog = await screen.findByRole("dialog");
  expect(within(dialog).getByText("isolated-a")).toBeVisible();
  expect(within(dialog).getByText("isolated-b")).toBeVisible();
  expect(within(dialog).getByText("isolated-c")).toBeVisible();
  expect(within(dialog).queryByText("production-api")).not.toBeInTheDocument();
});

it("removes an expanded resource from the exact cleanup batch that covers it", async () => {
  const user = userEvent.setup();
  cleanupSelection.targets = [
    {
      key: "stack:vswitch%3Avsw-a:ecs",
      kind: "resource_batch",
      connectionId: "connection-a",
      displayName: "ECS Instance",
      selector: ["isolated-a", "isolated-b", "isolated-c"].map((assetID) => ({
        kind: "asset" as const,
        asset_id: assetID,
      })),
      ancestryKeys: ["region-a", "vpc-a", "stack:vswitch%3Avsw-a:ecs"],
      memberAssetIds: ["isolated-a", "isolated-b", "isolated-c"],
      resourceCount: 3,
    },
  ];
  renderCanvas();

  await user.click(screen.getByRole("button", { name: "Expand ECS Instance" }));
  const member = screen.getByRole("button", {
    name: /isolated-a.*Pending cleanup/,
  });
  fireEvent.contextMenu(member);
  await user.click(
    await screen.findByRole("menuitem", {
      name: "Remove from resource list",
    }),
  );

  expect(cleanupSelection.removeBatchMember).toHaveBeenCalledExactlyOnceWith(
    "stack:vswitch%3Avsw-a:ecs",
    "isolated-a",
  );
  expect(cleanupSelection.removeTarget).not.toHaveBeenCalled();
});

it("removes every selected resource from cleanup through one context action", async () => {
  const user = userEvent.setup();
  cleanupSelection.targets = ["ecs-a", "ecs-b"].map(
    (assetID): CleanupTarget => ({
      key: `asset:${assetID}`,
      kind: "resource",
      connectionId: "connection-a",
      displayName: assetID,
      selector: { kind: "asset", asset_id: assetID },
      ancestryKeys: ["region-a", "vpc-a", `asset:${assetID}`],
    }),
  );
  renderCanvas();

  const api = screen.getByRole("button", {
    name: /production-api.*Pending cleanup/,
  });
  const worker = screen.getByRole("button", {
    name: /production-worker.*Pending cleanup/,
  });
  await user.click(api);
  fireEvent.click(worker, { shiftKey: true });
  act(() => {
    flow.onSelectionChange?.({
      nodes: flow.nodes.filter((node) => node.id === "ecs-b"),
    });
    flow.onSelectionEnd?.({ button: 2, shiftKey: false });
  });
  expect(screen.getByText("2 selected")).toBeVisible();
  fireEvent.contextMenu(worker);
  await user.click(
    await screen.findByRole("menuitem", {
      name: "Remove from resource list",
    }),
  );

  expect(cleanupSelection.removeTarget).toHaveBeenCalledTimes(2);
  expect(cleanupSelection.removeTarget).toHaveBeenNthCalledWith(
    1,
    "asset:ecs-a",
  );
  expect(cleanupSelection.removeTarget).toHaveBeenNthCalledWith(
    2,
    "asset:ecs-b",
  );
});

it("removes every exact member target when a stack is pending from individual resources", async () => {
  const user = userEvent.setup();
  cleanupSelection.targets = ["isolated-a", "isolated-b", "isolated-c"].map(
    (assetID): CleanupTarget => ({
      key: `asset:${assetID}`,
      kind: "resource",
      connectionId: "connection-a",
      displayName: assetID,
      selector: { kind: "asset", asset_id: assetID },
      ancestryKeys: ["region-a", "vpc-a", `asset:${assetID}`],
    }),
  );
  renderCanvas();

  const stack = screen.getByRole("button", {
    name: /ECS Instance.*3 resources.*Pending cleanup/,
  });
  fireEvent.contextMenu(stack);
  await user.click(
    await screen.findByRole("menuitem", {
      name: "Remove all from resource list",
    }),
  );

  expect(cleanupSelection.removeTarget).toHaveBeenCalledTimes(3);
  expect(cleanupSelection.removeTarget).toHaveBeenNthCalledWith(
    1,
    "asset:isolated-a",
  );
  expect(cleanupSelection.removeTarget).toHaveBeenNthCalledWith(
    2,
    "asset:isolated-b",
  );
  expect(cleanupSelection.removeTarget).toHaveBeenNthCalledWith(
    3,
    "asset:isolated-c",
  );
  expect(cleanupSelection.removeBatchMember).not.toHaveBeenCalled();
});

it("keeps inherited pending visuals but exposes no child removal action", async () => {
  const user = userEvent.setup();
  cleanupSelection.targets = [
    {
      key: "region-a",
      kind: "region",
      connectionId: "connection-a",
      displayName: "杭州",
      selector: {
        kind: "scope",
        connection_id: "connection-a",
        scope_id: "region-a",
        scope_kind: "region",
        descendants: true,
      },
      ancestryKeys: ["region-a"],
    },
  ];
  renderCanvas();

  const resourceButton = screen.getByRole("button", {
    name: /production-api.*Pending cleanup/,
  });
  expect(resourceButton.parentElement).toHaveAttribute(
    "data-cleanup-pending",
    "true",
  );
  expect(
    resourceButton.parentElement?.querySelector("[data-pending-cleanup-label]"),
  ).toHaveTextContent("Pending cleanup");
  fireEvent.contextMenu(resourceButton);
  expect(
    await screen.findByRole("menuitem", {
      name: "Included by broader cleanup selection",
    }),
  ).toHaveAttribute("data-disabled");
  expect(
    screen.queryByRole("menuitem", { name: "Remove from resource list" }),
  ).not.toBeInTheDocument();
  await user.keyboard("{Escape}");

  const stack = screen.getByRole("button", {
    name: /ECS Instance.*3 resources.*Pending cleanup/,
  });
  expect(stack.querySelector("[data-pending-cleanup-label]")).toHaveTextContent(
    "Pending cleanup",
  );
  fireEvent.contextMenu(stack);
  expect(
    await screen.findByRole("menuitem", {
      name: "Included by broader cleanup selection",
    }),
  ).toHaveAttribute("data-disabled");
  expect(
    screen.queryByRole("menuitem", {
      name: "Remove all from resource list",
    }),
  ).not.toBeInTheDocument();
  expect(cleanupSelection.removeTarget).not.toHaveBeenCalled();
  expect(cleanupSelection.removeBatchMember).not.toHaveBeenCalled();
});

it("inherits Region cleanup in the Region global-resources graph", () => {
  cleanupSelection.targets = [
    {
      key: "region-a",
      kind: "region",
      connectionId: "connection-a",
      displayName: "杭州",
      selector: {
        kind: "scope",
        connection_id: "connection-a",
        scope_id: "region-a",
        scope_kind: "region",
        descendants: true,
      },
      ancestryKeys: ["region-a"],
    },
  ];
  const publicView: ResourceGraphTopologyView = {
    kind: "resource_graph",
    context: {
      key: "region-public-a",
      name: "杭州",
      native_id: "cn-hangzhou",
    },
    ancestors: [{ key: "region-a", name: "杭州", native_id: "cn-hangzhou" }],
    resources: [
      resource(
        "region-public-resource",
        "region-public-resource",
        "Route Table",
        "network",
      ),
    ],
    edges: [],
  };

  renderCanvas({ topologyView: publicView });

  const resourceButton = screen.getByRole("button", {
    name: /region-public-resource.*Pending cleanup/,
  });
  expect(resourceButton.parentElement).toHaveAttribute(
    "data-cleanup-pending",
    "true",
  );
  expect(
    resourceButton.parentElement?.querySelector("[data-pending-cleanup-label]"),
  ).toHaveTextContent("Pending cleanup");
});

it("renders no VPC frame and exactly one persistent frame for each vSwitch", () => {
  renderCanvas();

  expect(screen.getByTestId("react-flow")).toHaveAttribute(
    "data-min-zoom",
    "0.0001",
  );
  expect(screen.getByTestId("react-flow")).toHaveAttribute(
    "data-hide-attribution",
    "true",
  );
  expect(
    screen.queryByRole("img", { name: "Topology overview" }),
  ).not.toBeInTheDocument();
  expect(screen.queryByTestId("react-flow-minimap")).not.toBeInTheDocument();
  expect(screen.queryByText("production-vpc")).not.toBeInTheDocument();
  const frames = screen.getAllByTestId(/flow-node-vsw-/);
  expect(frames).toHaveLength(2);
  expect(frames.every((frame) => frame.dataset.nodeType === "vswitch")).toBe(
    true,
  );
  expect(
    within(screen.getByTestId("flow-node-vsw-a")).getByText("application-zone"),
  ).toBeVisible();
  expect(
    within(screen.getByTestId("flow-node-vsw-a")).getByText(
      "vsw-a · cn-hangzhou-h",
    ),
  ).toBeVisible();
  expect(screen.getByText("7 resources")).toBeVisible();

  expect(screen.queryByText(/network layer/i)).not.toBeInTheDocument();
  expect(screen.queryByText(/compute layer/i)).not.toBeInTheDocument();
  expect(screen.queryByText(/storage layer/i)).not.toBeInTheDocument();
  expect(screen.getByRole("status")).toHaveTextContent(
    "one endpoint is unavailable",
  );
});

it("opens details and cleanup actions from a standalone vSwitch frame", async () => {
  const user = userEvent.setup();
  renderCanvas();

  const frame = screen.getByTestId("flow-node-vsw-a");
  const header = within(frame).getByText("application-zone").closest("header");
  expect(header).not.toBeNull();
  fireEvent.contextMenu(header!);

  const viewDetails = await screen.findByRole("menuitem", {
    name: "View details",
  });
  expect(
    screen.getByRole("menuitem", { name: "Add to resource list" }),
  ).toBeVisible();
  expect(
    screen.getByRole("menuitem", { name: "Cloud console" }),
  ).toHaveAttribute(
    "href",
    "https://vpc.console.aliyun.com/vpc/cn-hangzhou/switches/vsw-a",
  );
  await user.click(viewDetails);

  const dialog = await screen.findByRole("dialog");
  expect(within(dialog).getByText("application-zone")).toBeVisible();
  expect(within(dialog).getByText("杭州")).toBeVisible();
  expect(within(dialog).getByText("cn-hangzhou-h")).toBeVisible();
  expect(within(dialog).queryByText("7")).not.toBeInTheDocument();
  expect(await within(dialog).findByText("CIDR block")).toBeVisible();
  expect(within(dialog).getByText("10.0.0.0/24")).toBeVisible();

  await user.click(within(dialog).getByRole("button", { name: "Close" }));
  header!.focus();
  fireEvent.keyDown(header!, { key: "F10", shiftKey: true });
  expect(
    await screen.findByRole("menuitem", { name: "View details" }),
  ).toBeVisible();
});

it("expands an isolated stack in place and collapses it back with focus", async () => {
  const user = userEvent.setup();
  renderCanvas();
  await clearInitialFitView();

  const stack = screen.getByRole("button", {
    name: /ECS Instance.*3 resources/,
  });
  const anchor = screen
    .getByTestId("flow-node-stack:vswitch%3Avsw-a:ecs")
    .getAttribute("data-position");
  await user.click(stack);
  expect(
    screen.getByTestId("flow-node-stack:vswitch%3Avsw-a:ecs"),
  ).toHaveAttribute("data-node-type", "stack");
  expect(
    screen.getByRole("toolbar", { name: "Box selection actions" }),
  ).toHaveTextContent("1 selected");
  fireEvent.doubleClick(stack);

  expect(
    screen.queryByRole("button", { name: /ECS Instance.*3 resources/ }),
  ).not.toBeInTheDocument();
  const expanded = screen.getByTestId("flow-node-stack:vswitch%3Avsw-a:ecs");
  expect(expanded).toHaveAttribute("data-node-type", "expandedStack");
  expect(expanded).toHaveAttribute("data-position", anchor);
  expect(expanded).toHaveAttribute("data-parent-id", "vsw-a");
  expect(expanded).toHaveAttribute("data-extent", "parent");
  expect(within(expanded).getByText("ECS Instance")).toBeVisible();
  expect(within(expanded).getByText("3 resources")).toBeVisible();
  expect(screen.getByRole("button", { name: "isolated-a" })).toBeVisible();
  expect(screen.getByRole("button", { name: "isolated-b" })).toBeVisible();
  expect(screen.getByRole("button", { name: "isolated-c" })).toBeVisible();

  const collapse = within(expanded).getByRole("button", { name: "Collapse" });
  expect(document.activeElement).toBe(document.body);
  await waitForStackFocusHandoff();
  expect(flow.fitView).toHaveBeenCalledExactlyOnceWith({
    nodes: [{ id: "stack:vswitch%3Avsw-a:ecs" }],
    padding: 0.04,
    maxZoom: 1.8,
  });
  expect(collapse).toHaveFocus();
  await user.keyboard("{Enter}");

  const restored = screen.getByRole("button", {
    name: /ECS Instance.*3 resources/,
  });
  await waitForStackFocusHandoff();
  expect(flow.fitView).toHaveBeenCalledOnce();
  expect(flow.setViewport).toHaveBeenCalledExactlyOnceWith({
    x: 10,
    y: 20,
    zoom: 1,
  });
  expect(
    screen.getByRole("button", { name: "Expand ECS Instance" }),
  ).toHaveFocus();
  expect(
    screen.getByTestId("flow-node-stack:vswitch%3Avsw-a:ecs"),
  ).toHaveAttribute("data-position", anchor);

  fireEvent.doubleClick(restored);
  fireEvent.keyDown(screen.getByTestId("topology-canvas"), { key: "Escape" });
  expect(
    screen.getByTestId("flow-node-stack:vswitch%3Avsw-a:ecs"),
  ).toHaveAttribute("data-node-type", "expandedStack");
  expect(screen.getByRole("button", { name: "isolated-a" })).toBeVisible();
});

it("expands truly empty vSwitches at their stack anchor and restores focus on collapse", async () => {
  const user = userEvent.setup();
  const emptyVSwitchView: VPCTopologyView = {
    ...view,
    vswitches: [
      ...view.vswitches,
      {
        key: "vsw-empty-c",
        asset_id: "asset-vsw-empty-c",
        name: "cache-zone",
        native_id: "vsw-empty-c",
        zone: "cn-hangzhou-j",
        resource_count: 0,
        resource_keys: [],
      },
      {
        key: "vsw-empty-a",
        asset_id: "asset-vsw-empty-a",
        name: "batch-zone",
        native_id: "vsw-empty-a",
        zone: "cn-hangzhou-j",
        resource_count: 0,
        resource_keys: [],
      },
      {
        key: "vsw-empty-b",
        asset_id: "asset-vsw-empty-b",
        name: "edge-zone",
        native_id: "vsw-empty-b",
        zone: "cn-hangzhou-j",
        resource_count: 0,
        resource_keys: [],
      },
    ],
  };
  renderCanvas({ topologyView: emptyVSwitchView });
  await clearInitialFitView();

  const stack = screen.getByRole("button", {
    name: /Empty vSwitches.*3/,
  });
  expect(stack).toHaveClass("nodrag", "nopan");

  await user.click(
    screen.getByRole("button", { name: "Expand Empty vSwitches" }),
  );

  const expanded = screen.getByTestId("flow-node-vswitch-stack:vpc-a");
  expect(expanded).toHaveAttribute("data-node-type", "expandedVSwitchStack");
  expect(expanded.querySelector("[data-vswitch-stack-expanded]")).toHaveClass(
    "rounded-lg",
    "border-l-2",
    "bg-muted/[0.12]",
  );
  expect(
    expanded.querySelector("[data-vswitch-stack-expanded]"),
  ).not.toHaveClass("border", "bg-background/50");
  const expandedHeader = expanded.querySelector<HTMLElement>(
    "[data-vswitch-stack-header]",
  );
  expect(expandedHeader).not.toBeNull();
  expect(expandedHeader).toHaveClass("h-10", "px-3");
  expect(
    within(expandedHeader as HTMLElement).getByText("Empty vSwitches"),
  ).toBeVisible();
  expect(within(expandedHeader as HTMLElement).getByText("· 3")).toBeVisible();
  expect(
    expanded.querySelector("[data-vswitch-stack-guide]"),
  ).not.toBeInTheDocument();
  expect(expanded.querySelector("[data-vswitch-stack-grid]")).toHaveClass(
    "gap-5",
  );
  expect(expanded.querySelector("[data-vswitch-stack-grid]")).toHaveStyle({
    gridTemplateColumns: "repeat(3, minmax(0, 1fr))",
    gridAutoRows: "72px",
  });
  for (const [name, zone] of [
    ["batch-zone", "cn-hangzhou-j"],
    ["edge-zone", "cn-hangzhou-j"],
    ["cache-zone", "cn-hangzhou-j"],
  ]) {
    expect(within(expanded).getByText(name)).toBeVisible();
    expect(within(expanded).getAllByText(zone).length).toBeGreaterThan(0);
  }
  expect(within(expanded).queryByText("0 resources")).not.toBeInTheDocument();
  expect(
    within(expanded).getByText("batch-zone").closest("[role=button]"),
  ).toHaveClass("border-border/70", "bg-transparent", "shadow-none");

  const collapse = within(expanded).getByRole("button", { name: "Collapse" });
  expect(document.activeElement).toBe(document.body);
  await waitForStackFocusHandoff();
  expect(flow.fitView).toHaveBeenCalledExactlyOnceWith({
    nodes: [{ id: "vswitch-stack:vpc-a" }],
    padding: 0.04,
    maxZoom: 1.8,
  });
  expect(collapse).toHaveFocus();

  const member = within(expanded).getByText("batch-zone").closest("section");
  expect(member).not.toBeNull();
  fireEvent.contextMenu(member!);
  expect(
    await screen.findByRole("menuitem", { name: "Cloud console" }),
  ).toHaveAttribute(
    "href",
    "https://vpc.console.aliyun.com/vpc/cn-hangzhou/switches/vsw-empty-a",
  );
  await user.click(
    await screen.findByRole("menuitem", { name: "View details" }),
  );
  const detailDialog = await screen.findByRole("dialog");
  expect(within(detailDialog).getByText("batch-zone")).toBeVisible();
  expect(within(detailDialog).getByText("cn-hangzhou-j")).toBeVisible();
  expect(within(detailDialog).queryByText("0")).not.toBeInTheDocument();
  expect(await within(detailDialog).findByText("CIDR block")).toBeVisible();
  expect(within(detailDialog).getByText("10.0.0.0/24")).toBeVisible();
  await user.click(within(detailDialog).getByRole("button", { name: "Close" }));

  collapse.focus();
  await user.keyboard("{Enter}");
  await waitForStackFocusHandoff();
  expect(flow.fitView).toHaveBeenCalledOnce();
  expect(flow.setViewport).toHaveBeenCalledExactlyOnceWith({
    x: 10,
    y: 20,
    zoom: 1,
  });
  expect(
    screen.getByRole("button", { name: "Expand Empty vSwitches" }),
  ).toHaveFocus();
});

it("does not refit the canvas for an ordinary resource focus change", async () => {
  const user = userEvent.setup();
  renderCanvas();
  await clearInitialFitView();

  await user.click(screen.getByRole("button", { name: "production-api" }));
  await waitForStackFocusHandoff();

  expect(flow.fitView).not.toHaveBeenCalled();
});

it("keeps a selected isolated resource visible when a partial view becomes complete", async () => {
  const user = userEvent.setup();

  function Harness({ complete }: { complete: boolean }) {
    const [selectedKey, setSelectedKey] = useState<string>();
    useEffect(() => {
      if (!selectedKey) return;
      const clearFocusOnEscape = (event: KeyboardEvent) => {
        if (event.key === "Escape") setSelectedKey(undefined);
      };
      window.addEventListener("keydown", clearFocusOnEscape);
      return () => window.removeEventListener("keydown", clearFocusOnEscape);
    }, [selectedKey]);
    return (
      <ThemeProvider>
        <TooltipProvider>
          <LocaleProvider>
            <TopologyCanvas
              view={view}
              complete={complete}
              selectedResourceKey={selectedKey}
              onSelectResource={(selected) => setSelectedKey(selected.key)}
              onClearFocus={() => setSelectedKey(undefined)}
            />
          </LocaleProvider>
        </TooltipProvider>
      </ThemeProvider>
    );
  }

  const rendered = render(<Harness complete={false} />);
  const selected = screen.getByRole("button", { name: "isolated-a" });
  await user.click(selected);
  expect(selected).toHaveAttribute("aria-current", "true");

  rendered.rerender(<Harness complete />);

  expect(
    screen.getByTestId("flow-node-stack:vswitch%3Avsw-a:ecs"),
  ).toHaveAttribute("data-node-type", "expandedStack");
  expect(
    screen.getByRole("button", {
      name: "isolated-a · Current resource",
    }),
  ).toHaveAttribute("aria-current", "true");
  expect(screen.getByRole("button", { name: "isolated-b" })).toBeVisible();
  expect(screen.getByRole("button", { name: "isolated-c" })).toBeVisible();

  fireEvent.keyDown(window, { key: "Escape" });
  const retainedResource = screen.getByRole("button", { name: "isolated-a" });
  expect(retainedResource).not.toHaveAttribute("aria-current");
  expect(
    screen.getByTestId("flow-node-stack:vswitch%3Avsw-a:ecs"),
  ).toHaveAttribute("data-node-type", "expandedStack");
  const resolveReturnFocus = () =>
    document.querySelector<HTMLElement>('[data-resource-key="isolated-a"]');
  expect(resolveReturnFocus()).toBe(retainedResource);
  resolveReturnFocus()?.focus();
  expect(retainedResource).toHaveFocus();

  await user.click(retainedResource);
  await user.click(screen.getByTestId("flow-pane"));
  expect(
    screen.getByTestId("flow-node-stack:vswitch%3Avsw-a:ecs"),
  ).toHaveAttribute("data-node-type", "expandedStack");
  expect(
    screen.getByRole("button", { name: "isolated-a" }),
  ).not.toHaveAttribute("aria-current");
});

it("clears focus before collapsing the auto-expanded stack and returns focus to the stack button", async () => {
  const user = userEvent.setup();
  const onClearFocus = vi.fn();

  function Harness({ complete }: { complete: boolean }) {
    const [selectedKey, setSelectedKey] = useState<string>();
    return (
      <ThemeProvider>
        <TooltipProvider>
          <LocaleProvider>
            <TopologyCanvas
              view={view}
              complete={complete}
              selectedResourceKey={selectedKey}
              onSelectResource={(selected) => setSelectedKey(selected.key)}
              onClearFocus={() => {
                onClearFocus();
                setSelectedKey(undefined);
              }}
            />
          </LocaleProvider>
        </TooltipProvider>
      </ThemeProvider>
    );
  }

  const rendered = render(<Harness complete={false} />);
  await user.click(screen.getByRole("button", { name: "isolated-a" }));
  rendered.rerender(<Harness complete />);

  await user.click(screen.getByRole("button", { name: "Collapse" }));

  expect(onClearFocus).toHaveBeenCalledOnce();
  expect(screen.queryByRole("button", { name: "isolated-a" })).toBeNull();
  await waitForStackFocusHandoff();
  expect(
    screen.getByRole("button", { name: "Expand ECS Instance" }),
  ).toHaveFocus();
});

it("does not clear an unrelated selection when another stack is collapsed", async () => {
  const user = userEvent.setup();
  const onClearFocus = vi.fn();

  function Harness() {
    const [selectedKey, setSelectedKey] = useState<string>();
    return (
      <ThemeProvider>
        <TooltipProvider>
          <LocaleProvider>
            <TopologyCanvas
              view={view}
              complete
              selectedResourceKey={selectedKey}
              onSelectResource={(selected) => setSelectedKey(selected.key)}
              onClearFocus={() => {
                onClearFocus();
                setSelectedKey(undefined);
              }}
            />
          </LocaleProvider>
        </TooltipProvider>
      </ThemeProvider>
    );
  }

  render(<Harness />);
  await user.click(screen.getByRole("button", { name: "Expand ECS Instance" }));
  await user.click(screen.getByRole("button", { name: "production-api" }));
  await user.click(screen.getByRole("button", { name: "Collapse" }));

  expect(onClearFocus).not.toHaveBeenCalled();
  expect(
    screen.getByRole("button", {
      name: "production-api · Current resource",
    }),
  ).toHaveAttribute("aria-current", "true");
  await waitForStackFocusHandoff();
  expect(
    screen.getByRole("button", { name: "Expand ECS Instance" }),
  ).toHaveFocus();
});

it("highlights every connected edge and clears on Escape or pane click", async () => {
  const user = userEvent.setup();
  renderCanvas();

  const unrelatedResource = screen.getByRole("button", {
    name: "archive-disk",
  });
  const unrelatedResourceBaseline = {
    buttonClass: unrelatedResource.className,
    wrapperClass: unrelatedResource.parentElement?.className,
  };
  await user.click(screen.getByRole("button", { name: "production-api" }));
  expect(
    screen.getByRole("button", {
      name: "production-api · Current resource",
    }),
  ).toHaveAttribute("aria-current", "true");
  expect(
    screen.getByRole("button", {
      name: "shared-security-group · Directly related",
    }),
  ).toHaveAttribute("data-related", "true");
  expect(screen.getByTestId("flow-edge-uses-a")).toHaveAttribute(
    "data-stroke-dasharray",
    "5 5",
  );
  expect(screen.getByTestId("flow-edge-uses-a")).toHaveAttribute(
    "data-stroke-width",
    "2",
  );
  expect(screen.getByTestId("flow-edge-uses-a")).toHaveAttribute(
    "data-opacity",
    "1",
  );
  for (const suffix of ["a", "b", "c"]) {
    expect(screen.getByTestId(`flow-edge-uses-${suffix}`)).toHaveAttribute(
      "data-stroke",
      "var(--warning)",
    );
    expect(screen.getByTestId(`flow-edge-uses-${suffix}`)).toHaveAttribute(
      "data-label-fill",
      "var(--warning)",
    );
    expect(screen.getByTestId(`flow-edge-uses-${suffix}`)).toHaveAttribute(
      "data-stroke-width",
      "2",
    );
    expect(screen.getByTestId(`flow-edge-uses-${suffix}`)).toHaveAttribute(
      "data-opacity",
      "1",
    );
  }
  expect(screen.getByRole("button", { name: "archive-disk" })).not.toHaveClass(
    "opacity-25",
  );
  expect(screen.getByRole("button", { name: "archive-disk" }).className).toBe(
    unrelatedResourceBaseline.buttonClass,
  );
  expect(
    screen.getByRole("button", { name: "archive-disk" }).parentElement
      ?.className,
  ).toBe(unrelatedResourceBaseline.wrapperClass);
  expect(document.querySelector("[data-dimmed]")).toBeNull();

  await user.click(
    screen.getByRole("button", {
      name: "shared-security-group · Directly related",
    }),
  );
  for (const name of [
    "production-api",
    "production-worker",
    "production-cron",
  ]) {
    expect(
      screen.getByRole("button", {
        name: `${name} · Directly related`,
      }),
    ).toHaveAttribute("data-related", "true");
  }
  for (const suffix of ["a", "b", "c"]) {
    expect(screen.getByTestId(`flow-edge-uses-${suffix}`)).toHaveAttribute(
      "data-opacity",
      "1",
    );
  }

  fireEvent.keyDown(window, { key: "Escape" });
  expect(
    screen.getByRole("button", { name: "shared-security-group" }),
  ).not.toHaveAttribute("aria-current");
  for (const suffix of ["a", "b", "c"]) {
    expect(
      screen.queryByTestId(`flow-edge-uses-${suffix}`),
    ).not.toBeInTheDocument();
  }
  expect(screen.getByRole("button", { name: "archive-disk" })).not.toHaveClass(
    "opacity-25",
  );

  await user.click(screen.getByRole("button", { name: "production-api" }));
  await user.click(screen.getByTestId("flow-pane"));
  expect(
    screen.getByRole("button", { name: "production-api" }),
  ).not.toHaveAttribute("aria-current");
  for (const suffix of ["a", "b", "c"]) {
    expect(
      screen.queryByTestId(`flow-edge-uses-${suffix}`),
    ).not.toBeInTheDocument();
  }
});

it("marks non-actionable resources and partial stacks pending by asset selector", () => {
  cleanupSelection.targets = [
    {
      key: "asset:ecs-a",
      kind: "resource",
      connectionId: "connection-a",
      displayName: "production-api",
      selector: { kind: "asset", asset_id: "ecs-a" },
      ancestryKeys: ["asset:ecs-a"],
    },
    {
      key: "asset:isolated-a",
      kind: "resource",
      connectionId: "connection-a",
      displayName: "isolated-a",
      selector: { kind: "asset", asset_id: "isolated-a" },
      ancestryKeys: ["asset:isolated-a"],
    },
  ];

  renderCanvas();

  const resourceButton = screen.getByRole("button", {
    name: /production-api/,
  });
  expect(resourceButton.parentElement).toHaveAttribute(
    "data-cleanup-pending",
    "true",
  );
  expect(resourceButton).toHaveAccessibleName(
    "production-api · Pending cleanup",
  );
  expect(resourceButton).not.toHaveAccessibleDescription();
  expect(
    screen.getByRole("button", { name: /ECS Instance.*3 resources/ }),
  ).toHaveTextContent("Pending cleanup 1/3");
});

it("uses keyboard activation to select, then deselect, a resource", async () => {
  const onSelectResource = vi.fn();
  const user = userEvent.setup();
  renderCanvas({ onSelectResource });

  const resourceButton = screen.getByRole("button", {
    name: "production-api",
  });
  resourceButton.focus();
  await user.keyboard("{Enter}");
  expect(onSelectResource).toHaveBeenCalledOnce();

  onSelectResource.mockClear();
  await user.keyboard(" ");
  expect(onSelectResource).not.toHaveBeenCalled();
  expect(resourceButton).toHaveAttribute("aria-pressed", "false");
});

it("opens an ordinary resource in its cloud product console", async () => {
  const ecs = {
    ...resource("ecs-console", "console-api", "ECS Instance", "compute"),
    resource_kind_id: "alicloud:ACS::ECS::Instance",
    native_id: "i-console-api",
  };
  const consoleView: ResourceGraphTopologyView = {
    kind: "resource_graph",
    context: {
      key: "region-public:cn-hangzhou",
      name: "Region public",
      native_id: "cn-hangzhou",
    },
    resources: [ecs],
    edges: [],
  };
  renderCanvas({ topologyView: consoleView });

  fireEvent.contextMenu(screen.getByRole("button", { name: "console-api" }));

  expect(
    await screen.findByRole("menuitem", { name: "Cloud console" }),
  ).toHaveAttribute(
    "href",
    "https://ecs.console.aliyun.com/server/region/cn-hangzhou?instanceId=i-console-api",
  );
});

it("renders an isolated scanned resource and opens its product console", async () => {
  const bucket = {
    ...resource("oss-console", "production-artifacts", "OSS Bucket", "storage"),
    resource_kind_id: "alicloud:ACS::OSS::Bucket",
    native_id: "production-artifacts",
  };
  const consoleView: ResourceGraphTopologyView = {
    kind: "resource_graph",
    context: {
      key: "region-public:cn-hangzhou",
      name: "Region public",
      native_id: "cn-hangzhou",
    },
    resources: [bucket],
    edges: [],
  };
  renderCanvas({ topologyView: consoleView });

  const bucketButton = screen.getByRole("button", {
    name: "production-artifacts",
  });
  expect(bucketButton).toBeInTheDocument();

  fireEvent.contextMenu(bucketButton);

  expect(
    await screen.findByRole("menuitem", { name: "Cloud console" }),
  ).toHaveAttribute(
    "href",
    "https://oss.console.aliyun.com/bucket/oss-cn-hangzhou/production-artifacts/object",
  );
});

it("hides all relationship lines by default and reveals them from the toolbar", async () => {
  const user = userEvent.setup();
  const lifecycleView: VPCTopologyView = {
    ...view,
    edges: [
      ...view.edges,
      {
        key: "attached",
        source_key: "ecs-b",
        target_key: "disk-unrelated",
        kind: "relationship",
        relation: "attached_to",
      },
      {
        key: "lifecycle",
        source_key: "ecs-a",
        target_key: "disk-unrelated",
        kind: "lifecycle",
        relation: "managed_by",
      },
    ],
  };

  render(
    <ThemeProvider>
      <TooltipProvider>
        <LocaleProvider>
          <TopologyCanvas
            view={lifecycleView}
            complete
            warnings={[]}
            onSelectResource={vi.fn()}
            onClearFocus={vi.fn()}
          />
        </LocaleProvider>
      </TooltipProvider>
    </ThemeProvider>,
  );

  expect(screen.queryByTestId("flow-edge-uses-a")).not.toBeInTheDocument();
  expect(screen.queryByTestId("flow-edge-attached")).not.toBeInTheDocument();
  expect(screen.queryByTestId("flow-edge-lifecycle")).not.toBeInTheDocument();
  const showRelationshipLines = screen.getByRole("button", {
    name: "Show relationship lines",
  });
  expect(showRelationshipLines).toHaveAttribute("aria-pressed", "false");
  await user.click(showRelationshipLines);

  expect(
    screen.getByRole("button", { name: "Hide relationship lines" }),
  ).toHaveAttribute("aria-pressed", "true");
  expect(screen.getByTestId("flow-edge-uses-a")).toBeInTheDocument();
  expect(screen.getByTestId("flow-edge-attached")).toHaveAttribute(
    "data-stroke-dasharray",
    "5 5",
  );
  expect(screen.getByTestId("flow-edge-attached")).toHaveAttribute(
    "data-stroke-width",
    "1",
  );
  expect(screen.getByTestId("flow-edge-attached")).toHaveAttribute(
    "data-opacity",
    "0.5",
  );
  expect(screen.getByTestId("flow-edge-attached")).toHaveAttribute(
    "data-stroke",
    "var(--muted-foreground)",
  );
  expect(screen.getByTestId("flow-edge-attached")).not.toHaveAttribute(
    "data-label-fill",
  );
  expect(screen.getByTestId("flow-edge-attached")).not.toHaveAttribute(
    "data-label-font-size",
  );
  expect(screen.getByTestId("flow-edge-attached")).not.toHaveAttribute(
    "data-label-show-bg",
  );
  expect(screen.getByTestId("flow-edge-attached")).toHaveAttribute(
    "data-source",
    "ecs-b",
  );
  expect(screen.getByTestId("flow-edge-attached")).toHaveAttribute(
    "data-target",
    "disk-unrelated",
  );
  expect(screen.getByTestId("flow-edge-attached")).toHaveAttribute(
    "data-marker-end",
    "arrowclosed",
  );
  expect(screen.getByTestId("flow-edge-attached")).toHaveAttribute(
    "data-edge-type",
    "straight",
  );
  expect(screen.getByTestId("flow-edge-attached")).toHaveAttribute(
    "data-aria-label",
    "Attached to",
  );
  expect(screen.getByTestId("flow-edge-attached")).toBeEmptyDOMElement();
  expect(screen.getByTestId("flow-edge-lifecycle")).not.toHaveAttribute(
    "data-stroke-dasharray",
    "5 5",
  );
  expect(screen.getByTestId("flow-edge-lifecycle")).toHaveAttribute(
    "data-stroke",
    "var(--muted-foreground)",
  );
  expect(screen.getByTestId("flow-edge-lifecycle")).not.toHaveAttribute(
    "data-label-fill",
  );
  expect(screen.getByTestId("flow-edge-lifecycle")).not.toHaveAttribute(
    "data-label-font-size",
  );
  expect(screen.getByTestId("flow-edge-lifecycle")).not.toHaveAttribute(
    "data-label-show-bg",
  );
  expect(screen.getByTestId("flow-edge-lifecycle")).toHaveAttribute(
    "data-marker-end",
    "arrowclosed",
  );
  expect(screen.getByTestId("flow-edge-lifecycle")).toHaveAttribute(
    "data-aria-label",
    "managed by",
  );
  expect(screen.getByTestId("flow-edge-lifecycle")).toBeEmptyDOMElement();
});

it("shows selected resource relationships while all other lines remain hidden", async () => {
  const user = userEvent.setup();
  const selectedView: VPCTopologyView = {
    ...view,
    edges: [
      ...view.edges,
      {
        key: "attached",
        source_key: "ecs-a",
        target_key: "disk-unrelated",
        kind: "relationship",
        relation: "attached_to",
      },
    ],
  };
  renderCanvas({ topologyView: selectedView });

  expect(screen.queryByTestId("flow-edge-attached")).not.toBeInTheDocument();
  expect(
    screen.getByRole("button", { name: "Show relationship lines" }),
  ).toHaveAttribute("aria-pressed", "false");

  await user.click(screen.getByRole("button", { name: "production-api" }));

  expect(
    screen.getByRole("button", {
      name: "archive-disk · Directly related",
    }),
  ).toHaveAttribute("data-related", "true");
  expect(screen.getByTestId("flow-edge-attached")).toHaveAttribute(
    "data-stroke",
    "var(--warning)",
  );
  expect(screen.getByTestId("flow-edge-attached")).toHaveAttribute(
    "data-opacity",
    "1",
  );
  expect(
    screen.getByRole("button", { name: "Show relationship lines" }),
  ).toHaveAttribute("aria-pressed", "false");
});

it("expands a Region-public security-group stack in place and restores focus", async () => {
  const user = userEvent.setup();
  const publicView: ResourceGraphTopologyView = {
    kind: "resource_graph",
    context: { key: "region-public-a", name: "Region public" },
    resources: [
      resource("public-sg-a", "public-sg-a", "Security Group", "network"),
      resource("public-sg-b", "public-sg-b", "Security Group", "network"),
      resource("public-sg-c", "public-sg-c", "Security Group", "network"),
    ],
    edges: [],
  };

  render(
    <ThemeProvider>
      <TooltipProvider>
        <LocaleProvider>
          <TopologyCanvas
            view={publicView}
            complete
            warnings={[]}
            onSelectResource={vi.fn()}
            onClearFocus={vi.fn()}
          />
        </LocaleProvider>
      </TooltipProvider>
    </ThemeProvider>,
  );
  await clearInitialFitView();

  const stack = screen.getByRole("button", {
    name: /Security Group.*3 resources/,
  });
  const anchor = screen
    .getByTestId(
      "flow-node-stack:region-public%3Aregion-public-a:security%20group",
    )
    .getAttribute("data-position");
  await user.click(
    screen.getByRole("button", { name: "Expand Security Group" }),
  );

  const expanded = screen.getByTestId(
    "flow-node-stack:region-public%3Aregion-public-a:security%20group",
  );
  expect(expanded).toHaveAttribute("data-node-type", "expandedStack");
  expect(expanded).toHaveAttribute("data-position", anchor);
  await waitForStackFocusHandoff();
  expect(
    within(expanded).getByRole("button", { name: "Collapse" }),
  ).toHaveFocus();

  await user.click(within(expanded).getByRole("button", { name: "Collapse" }));
  await waitForStackFocusHandoff();
  expect(
    screen.getByRole("button", { name: "Expand Security Group" }),
  ).toHaveFocus();
});

it("uses resource kind when a Region-public stack has blank type names", async () => {
  const user = userEvent.setup();
  const blankTypeView: ResourceGraphTopologyView = {
    kind: "resource_graph",
    context: { key: "region-public-a", name: "Region public" },
    resources: ["a", "b", "c"].map((suffix, index) => ({
      ...resource(
        `blank-type-${suffix}`,
        `blank-type-${suffix}`,
        "",
        "network",
      ),
      resource_kind_id: "eni",
      type_name: index === 0 ? "  " : "",
    })),
    edges: [],
  };

  render(
    <ThemeProvider>
      <TooltipProvider>
        <LocaleProvider>
          <TopologyCanvas
            view={blankTypeView}
            complete
            warnings={[]}
            onSelectResource={vi.fn()}
            onClearFocus={vi.fn()}
          />
        </LocaleProvider>
      </TooltipProvider>
    </ThemeProvider>,
  );

  const stack = screen.getByRole("button", { name: /eni.*3 resources/ });
  expect(stack).toHaveTextContent("eni");
  await user.click(screen.getByRole("button", { name: "Expand eni" }));

  const expanded = screen.getByTestId(
    "flow-node-stack:region-public%3Aregion-public-a:eni",
  );
  expect(within(expanded).getByText("eni")).toBeVisible();
});

it("keeps projection warnings visible when the resource graph is empty", () => {
  render(
    <ThemeProvider>
      <TooltipProvider>
        <LocaleProvider>
          <TopologyCanvas
            view={{
              kind: "resource_graph",
              context: { key: "region-public", name: "Region public" },
              resources: [],
              edges: [],
            }}
            complete
            warnings={warnings}
            onSelectResource={vi.fn()}
            onClearFocus={vi.fn()}
          />
        </LocaleProvider>
      </TooltipProvider>
    </ThemeProvider>,
  );

  expect(
    screen.getByText("No resources match the current view."),
  ).toBeVisible();
  expect(screen.getByRole("status")).toHaveTextContent(
    "one endpoint is unavailable",
  );
});

it("keeps high-cardinality projection warnings compact without dropping the summary", () => {
  const highCardinalityWarnings: TopologyProjectionWarning[] = Array.from(
    { length: 100 },
    (_, index) => ({
      code: "topology.endpoint_unavailable",
      relation_key: `missing-relation-${index + 1}`,
      message: `warning ${index + 1}`,
    }),
  );

  render(
    <ThemeProvider>
      <TooltipProvider>
        <LocaleProvider>
          <TopologyCanvas
            view={{
              kind: "resource_graph",
              context: { key: "region-public", name: "Region public" },
              resources: [],
              edges: [],
            }}
            complete
            warnings={highCardinalityWarnings}
            onSelectResource={vi.fn()}
            onClearFocus={vi.fn()}
          />
        </LocaleProvider>
      </TooltipProvider>
    </ThemeProvider>,
  );

  const status = screen.getByRole("status");
  expect(within(status).getAllByText(/^warning \d+$/)).toHaveLength(3);
  expect(status).toHaveTextContent("warning 1");
  expect(status).toHaveTextContent("warning 2");
  expect(status).toHaveTextContent("warning 3");
  expect(status).not.toHaveTextContent("warning 4");
  expect(status).not.toHaveTextContent("warning 100");
  expect(status).toHaveTextContent("97 more warnings");
  expect(
    screen.getByText("No resources match the current view."),
  ).toBeVisible();
});
