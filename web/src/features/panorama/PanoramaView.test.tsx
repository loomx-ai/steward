import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  createElement,
  type ComponentProps,
  type ComponentType,
  type ReactNode,
} from "react";
import { MemoryRouter, useLocation, useNavigate } from "react-router-dom";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { APIRequestError } from "@/api/client";
import type {
  AccountTopologyView,
  ResourceGraphTopologyView,
  TopologyQuery,
  TopologyResource,
  TopologyResponse,
} from "@/api/types";
import { ThemeProvider } from "@/app/ThemeProvider";
import { TooltipProvider } from "@/components/ui/tooltip";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import type { CleanupTarget } from "./cleanupSelection";
import { PanoramaView } from "./PanoramaView";
import { REGION_ICON_SRC, VPC_ICON_SRC } from "./TopologyNodes";

const panoramaHarness = vi.hoisted(() => ({
  createScan: vi.fn(),
  getTopology: vi.fn(),
  findAsset: vi.fn(),
  listAssets: vi.fn(),
  listConnectionRegions: vi.fn(),
  listProviderCatalog: vi.fn(),
  listScopes: vi.fn(),
  useActualTopologyCanvas: false,
  cleanupTargets: [] as CleanupTarget[],
  connectionID: "connection-a",
  connectionName: "生产账号",
  connectionProvider: "alicloud",
  flowNodes: [] as MockFlowNode[],
  onSelectionChange: undefined as
    ((selection: { nodes: MockFlowNode[] }) => void) | undefined,
  onSelectionEnd: undefined as
    ((event: { shiftKey: boolean }) => void) | undefined,
}));

interface MockFlowNode {
  id: string;
  type?: string;
  data: Record<string, unknown>;
}

vi.mock("@xyflow/react", () => ({
  ReactFlow: ({
    children,
    edges,
    nodeTypes,
    nodes,
    onInit,
    onPaneClick,
    onSelectionChange,
    onSelectionEnd,
    "data-testid": testID,
  }: {
    children: ReactNode;
    edges: readonly unknown[];
    nodeTypes: Record<string, ComponentType<Record<string, unknown>>>;
    nodes: readonly MockFlowNode[];
    onInit?: (instance: {
      fitView: () => Promise<void>;
      getViewport: () => { x: number; y: number; zoom: number };
      setViewport: () => Promise<void>;
      zoomIn: () => Promise<void>;
      zoomOut: () => Promise<void>;
    }) => void;
    onPaneClick?: () => void;
    onSelectionChange?: (selection: { nodes: MockFlowNode[] }) => void;
    onSelectionEnd?: (event: { shiftKey: boolean }) => void;
    "data-testid"?: string;
  }) => {
    panoramaHarness.flowNodes = [...nodes];
    panoramaHarness.onSelectionChange = onSelectionChange;
    panoramaHarness.onSelectionEnd = onSelectionEnd;
    onInit?.({
      fitView: async () => undefined,
      getViewport: () => ({ x: 0, y: 0, zoom: 1 }),
      setViewport: async () => undefined,
      zoomIn: async () => undefined,
      zoomOut: async () => undefined,
    });
    return (
      <div role="application" data-testid={testID}>
        {nodes.map((node) => {
          const Renderer = nodeTypes[node.type ?? ""];
          return Renderer
            ? createElement(Renderer, {
                key: node.id,
                id: node.id,
                data: node.data,
                selected: false,
                type: node.type,
                dragging: false,
                zIndex: 0,
                selectable: false,
                deletable: false,
                isConnectable: false,
                positionAbsoluteX: 0,
                positionAbsoluteY: 0,
              })
            : null;
        })}
        {edges.map((_, index) => (
          <div key={index} className="react-flow__edge" />
        ))}
        <button type="button" onClick={onPaneClick}>
          Blank canvas
        </button>
        {children}
      </div>
    );
  },
  Background: () => null,
  Controls: () => null,
  Handle: () => null,
  Position: {
    Left: "left",
    Right: "right",
  },
}));

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return {
    ...actual,
    createScan: panoramaHarness.createScan,
    getTopology: panoramaHarness.getTopology,
    findAsset: panoramaHarness.findAsset,
    listAssets: panoramaHarness.listAssets,
    listConnectionRegions: panoramaHarness.listConnectionRegions,
    listScopes: panoramaHarness.listScopes,
    listProviderCatalog: panoramaHarness.listProviderCatalog,
  };
});

vi.mock("@/connections/ActiveConnectionProvider", () => ({
  useRequiredConnection: () => ({
    id: panoramaHarness.connectionID,
    name: panoramaHarness.connectionName,
    provider: panoramaHarness.connectionProvider,
  }),
}));

vi.mock("./CleanupSelectionContext", () => ({
  useCleanupSelection: () => ({
    targets: panoramaHarness.cleanupTargets,
    addTargets: vi.fn(),
    removeTarget: vi.fn(),
    removeBatchMember: vi.fn(),
    clearTargets: vi.fn(),
    requestConnectionChange: vi.fn(),
  }),
}));

vi.mock("./TopologyCanvas", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./TopologyCanvas")>();
  return {
    ...actual,
    TopologyCanvas: (props: ComponentProps<typeof actual.TopologyCanvas>) => {
      if (panoramaHarness.useActualTopologyCanvas) {
        return <actual.TopologyCanvas {...props} />;
      }
      const {
        complete,
        onSelectResource,
        onClearFocus,
        selectedResourceKey,
        highlightedNodeKey,
        searchFocusRequest,
        focusedCleanupTargetKey,
        view,
        warnings,
      } = props;
      const resources =
        view.kind === "resource_graph" || view.kind === "vpc"
          ? view.resources
          : [];
      return (
        <div
          data-testid="topology-canvas"
          data-view-kind={view.kind}
          data-complete={complete}
          data-selected-resource-key={selectedResourceKey}
          data-highlighted-node-key={highlightedNodeKey}
          data-search-focus-request={
            searchFocusRequest
              ? `${searchFocusRequest.id}:${
                  searchFocusRequest.nodeKey ?? "scope"
                }`
              : undefined
          }
          data-focused-cleanup-target-key={focusedCleanupTargetKey}
        >
          <button type="button" onClick={onClearFocus}>
            Blank canvas
          </button>
          {resources.map((resource) => (
            <button
              key={resource.key}
              type="button"
              data-resource-key={resource.key}
              aria-current={
                selectedResourceKey === resource.key ? "true" : undefined
              }
              onClick={(event) =>
                onSelectResource(resource, event.currentTarget)
              }
            >
              {resource.name}
            </button>
          ))}
          {warnings?.map((warning) => (
            <p key={`${warning.code}:${warning.relation_key}`} role="status">
              {warning.message}
            </p>
          ))}
        </div>
      );
    },
  };
});

const revision = {
  inventory: "inventory-a",
  graph: "graph-a",
  spec_bundle: "spec-a",
  projected_at: "2026-07-24T00:00:00Z",
};
const coverage = { status: "complete", failed_shards: 0 };
const cleanup = { selectable: false, potential_blockers: 0 };
const REGION_ID = "cn-hangzhou";
const REGION_FOCUS_KEY = "region:Y24taGFuZ3pob3U";
const REGION_PUBLIC_FOCUS_KEY = "region-public:Y24taGFuZ3pob3U";
const VPC_ID = "vpc-production";
const VPC_FOCUS_KEY = "vpc:Y24taGFuZ3pob3U:dnBjLXByb2R1Y3Rpb24";

const accountResponse: TopologyResponse = {
  revision,
  coverage,
  view: {
    kind: "account",
    global_resources: {
      key: "account-global",
      name: "账号全局资源",
      resource_count: 12,
      cleanup,
    },
    regions: [
      {
        key: REGION_FOCUS_KEY,
        name: "华东1（杭州）",
        native_id: REGION_ID,
        resource_count: 3376,
        cleanup,
      },
      {
        key: "region-empty-a",
        name: "华北2（北京）",
        resource_count: 0,
        cleanup,
      },
      {
        key: "region-empty-b",
        name: "华南1（深圳）",
        resource_count: 0,
        cleanup,
      },
      {
        key: "region-empty-c",
        name: "西南1（成都）",
        resource_count: 0,
        cleanup,
      },
    ],
  },
  truncated: false,
};

const regionResponse: TopologyResponse = {
  revision,
  coverage,
  view: {
    kind: "region",
    region: {
      key: REGION_FOCUS_KEY,
      name: "华东1（杭州）",
      native_id: REGION_ID,
    },
    public_resources: {
      key: REGION_PUBLIC_FOCUS_KEY,
      name: "Public resources",
      resource_count: 7,
      cleanup,
    },
    vpcs: [
      {
        key: VPC_FOCUS_KEY,
        name: "生产网络",
        native_id: VPC_ID,
        resource_count: 88,
        cleanup,
      },
      {
        key: "vpc-empty-a",
        name: "空网络",
        native_id: "vpc-empty-a",
        resource_count: 0,
        cleanup,
      },
      {
        key: "vpc-empty-b",
        name: "空网络 2",
        native_id: "vpc-empty-b",
        resource_count: 0,
        cleanup,
      },
      {
        key: "vpc-empty-c",
        name: "空网络 3",
        resource_count: 0,
        cleanup,
      },
    ],
  },
  truncated: false,
};

const globalResponse: TopologyResponse & {
  view: ResourceGraphTopologyView;
} = {
  revision,
  coverage,
  view: {
    kind: "resource_graph",
    context: { key: "account-global", name: "账号全局资源" },
    resources: [
      {
        key: "asset-a",
        asset_id: "asset-a",
        resource_kind_id: "ecs-instance",
        name: "生产 ECS",
        native_id: "i-production-ecs",
        type_name: "ECS Instance",
        domain: "compute",
        state: "Running",
        finding_count: 2,
        actionable: true,
        cleanup: {
          selectable: true,
          selector_kind: "asset",
          selector_key: "asset-a",
          confirmation: "confirm",
          potential_blockers: 1,
        },
      },
    ],
    edges: [],
  },
  truncated: false,
};

const regionPublicResponse: TopologyResponse & {
  view: ResourceGraphTopologyView;
} = {
  ...globalResponse,
  view: {
    ...globalResponse.view,
    context: {
      key: REGION_PUBLIC_FOCUS_KEY,
      name: "华东1（杭州）",
      native_id: REGION_ID,
    },
  },
};

const escapeLayerResponse: TopologyResponse & {
  view: ResourceGraphTopologyView;
} = {
  ...globalResponse,
  view: {
    ...globalResponse.view,
    resources: [
      globalResponse.view.resources[0]!,
      {
        ...globalResponse.view.resources[0]!,
        key: "asset-security-group",
        asset_id: "asset-security-group",
        resource_kind_id: "security-group",
        name: "共享安全组",
        native_id: "sg-shared",
        type_name: "Security Group",
        domain: "network",
        cleanup: {
          ...globalResponse.view.resources[0]!.cleanup,
          selector_key: "asset-security-group",
        },
      },
      ...["a", "b"].map((suffix): TopologyResource => ({
        ...globalResponse.view.resources[0]!,
        key: `asset-database-${suffix}`,
        asset_id: `asset-database-${suffix}`,
        resource_kind_id: "database",
        name: `数据库 ${suffix.toUpperCase()}`,
        native_id: `database-${suffix}`,
        type_name: "Database",
        domain: "storage",
        cleanup: {
          ...globalResponse.view.resources[0]!.cleanup,
          selector_key: `asset-database-${suffix}`,
        },
      })),
    ],
    edges: [
      {
        key: "uses-security-group",
        source_key: "asset-a",
        target_key: "asset-security-group",
        kind: "relationship",
        relation: "uses",
      },
    ],
  },
};

const vpcResponse: TopologyResponse = {
  revision,
  coverage,
  view: {
    kind: "vpc",
    region: {
      key: REGION_FOCUS_KEY,
      name: "华东1（杭州）",
      native_id: REGION_ID,
    },
    vpc: { key: "vpc-a", name: "生产网络", native_id: VPC_ID },
    public_resource_keys: [],
    vswitches: [],
    resources: [],
    edges: [],
  },
  truncated: false,
};

function renderPanorama({
  retry = false,
  initialEntries = ["/panorama"],
  initialIndex,
  remountOnPathname = false,
}: {
  retry?: false | number;
  initialEntries?: string[];
  initialIndex?: number;
  remountOnPathname?: boolean;
} = {}) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry, retryDelay: 0 } },
  });
  const tree = () => (
    <QueryClientProvider client={queryClient}>
      <ThemeProvider>
        <TooltipProvider>
          <LocaleProvider>
            <MemoryRouter
              initialEntries={initialEntries}
              initialIndex={initialIndex}
            >
              {remountOnPathname ? <PathnameKeyedPanorama /> : <PanoramaView />}
              <LocationProbe />
              <HistoryControls />
            </MemoryRouter>
          </LocaleProvider>
        </TooltipProvider>
      </ThemeProvider>
    </QueryClientProvider>
  );
  const result = render(tree());
  return {
    ...result,
    queryClient,
    rerenderPanorama: () => result.rerender(tree()),
  };
}

function PathnameKeyedPanorama() {
  const location = useLocation();
  return <PanoramaView key={location.pathname} />;
}

function LocationProbe() {
  const location = useLocation();
  return (
    <output data-testid="current-location">
      {location.pathname}
      {location.search}
    </output>
  );
}

function HistoryControls() {
  const navigate = useNavigate();
  return (
    <>
      <button type="button" onClick={() => navigate(-1)}>
        Browser back
      </button>
      <button type="button" onClick={() => navigate(1)}>
        Browser forward
      </button>
    </>
  );
}

function queryForCall(call: unknown[]): TopologyQuery {
  return call[1] as TopologyQuery;
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((next) => {
    resolve = next;
  });
  return { promise, resolve };
}

beforeEach(() => {
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
  panoramaHarness.getTopology.mockReset();
  panoramaHarness.createScan
    .mockReset()
    .mockResolvedValue({ id: "scan-region-a" });
  panoramaHarness.findAsset.mockReset();
  panoramaHarness.listAssets.mockReset().mockResolvedValue({ items: [] });
  panoramaHarness.listConnectionRegions
    .mockReset()
    .mockResolvedValue({ items: [] });
  panoramaHarness.listProviderCatalog.mockReset().mockResolvedValue([
    {
      provider: "alicloud",
      revision: "bundle-a",
      hash: "bundle-a",
      kinds_revision: "bundle-a",
      specs: [],
      kinds: [
        {
          id: "ack-cluster",
          provider: "alicloud",
          native_type: "ACS::CS::Cluster",
          class: "container.cluster",
          capabilities: [],
          display_name: "ACK Cluster",
          display_names: { "zh-CN": "ACK 集群" },
          bundle_revision: "bundle-a",
        },
        {
          id: "ecs-instance",
          provider: "alicloud",
          native_type: "ACS::ECS::Instance",
          class: "compute.instance",
          capabilities: [],
          display_name: "ECS Instance",
          display_names: { "zh-CN": "ECS 实例" },
          properties: [
            {
              path: "instanceType",
              type: "string",
              display_names: { "en-US": "Instance type", "zh-CN": "实例规格" },
              enum: ["ecs.g8i.large", "ecs.g8i.xlarge"],
              operators: ["=", "!=", "in"],
            },
          ],
          bundle_revision: "bundle-a",
        },
      ],
    },
  ]);
  panoramaHarness.listScopes.mockReset().mockResolvedValue({ items: [] });
  panoramaHarness.connectionID = "connection-a";
  panoramaHarness.connectionName = "生产账号";
  panoramaHarness.connectionProvider = "alicloud";
  panoramaHarness.useActualTopologyCanvas = false;
  panoramaHarness.cleanupTargets = [];
  panoramaHarness.flowNodes = [];
  panoramaHarness.onSelectionChange = undefined;
  panoramaHarness.onSelectionEnd = undefined;
  panoramaHarness.findAsset.mockResolvedValue({
    identity: {
      provider: "alicloud",
      native_type: "ALIYUN::ECS::INSTANCE",
      native_id: "i-production-ecs",
    },
    id: "asset-a",
    connection_id: "connection-a",
    resource_kind_id: "ecs-instance",
    name: "生产 ECS",
    location: "cn-hangzhou",
    normalized: {
      instance_charge_type: "PostPaid",
    },
  });
  panoramaHarness.getTopology.mockImplementation(
    async (_connectionID: string, query: TopologyQuery) => {
      switch (query.focus_key) {
        case REGION_FOCUS_KEY:
          return regionResponse;
        case "account-global":
          return globalResponse;
        case REGION_PUBLIC_FOCUS_KEY:
          return regionPublicResponse;
        case VPC_FOCUS_KEY:
          return vpcResponse;
        default:
          return accountResponse;
      }
    },
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

it("filters the panorama with the typed resource query", async () => {
  const user = userEvent.setup();
  renderPanorama();

  await user.click(
    await screen.findByRole("button", {
      name: "Normal search · Switch to advanced query",
    }),
  );
  const editor = await screen.findByRole("combobox", {
    name: "Resource query editor",
  });
  await user.type(editor, 'type = "ACS::ECS::Instance" AND prop');
  await user.click(
    await screen.findByRole("option", { name: /properties\.instanceType/ }),
  );
  await user.type(editor, '= "ecs.g8i.large"');
  await user.keyboard("{Control>}{Enter}{/Control}");

  const resourceQuery =
    'type = "ACS::ECS::Instance" AND properties.instanceType = "ecs.g8i.large"';
  await waitFor(() =>
    expect(
      queryForCall(panoramaHarness.getTopology.mock.calls.at(-1) ?? []),
    ).toEqual(expect.objectContaining({ resource_query: resourceQuery })),
  );
  expect(screen.getByTestId("current-location")).toHaveTextContent(
    `/panorama?${new URLSearchParams({ resource_query: resourceQuery }).toString()}`,
  );

  await user.click(
    screen.getByRole("button", {
      name: "Advanced query · Switch to normal search",
    }),
  );
  await waitFor(() =>
    expect(
      queryForCall(panoramaHarness.getTopology.mock.calls.at(-1) ?? []),
    ).toEqual(expect.objectContaining({ resource_query: undefined })),
  );
  expect(screen.getByTestId("current-location")).toHaveTextContent(
    /^\/panorama$/,
  );
});

it("preserves the resource query while drilling into the panorama", async () => {
  const user = userEvent.setup();
  const resourceQuery = 'type = "ACS::ECS::Instance"';
  const search = new URLSearchParams({
    resource_query: resourceQuery,
  }).toString();
  renderPanorama({
    initialEntries: [`/panorama?${search}`],
  });

  await user.click(
    await screen.findByRole("button", { name: /华东1（杭州）/ }),
  );

  expect(screen.getByTestId("current-location")).toHaveTextContent(
    `/panorama/regions/${REGION_ID}?${search}`,
  );
  await waitFor(() =>
    expect(
      queryForCall(panoramaHarness.getTopology.mock.calls.at(-1) ?? []),
    ).toEqual(
      expect.objectContaining({
        focus_key: REGION_FOCUS_KEY,
        resource_query: resourceQuery,
      }),
    ),
  );
});

it("confirms a Region rescan and opens the new scan details", async () => {
  const user = userEvent.setup();
  renderPanorama();

  const region = await screen.findByRole("button", {
    name: /华东1（杭州）.*3,376 resources/,
  });
  fireEvent.contextMenu(region);
  await user.click(await screen.findByRole("menuitem", { name: "Rescan" }));

  const confirmation = await screen.findByRole("alertdialog");
  expect(within(confirmation).getByText("Rescan this Region?")).toBeVisible();
  expect(confirmation).toHaveTextContent(
    "A new scan task will be created for 华东1（杭州） (cn-hangzhou).",
  );
  expect(panoramaHarness.createScan).not.toHaveBeenCalled();

  await user.click(
    within(confirmation).getByRole("button", { name: "Rescan" }),
  );

  await waitFor(() =>
    expect(panoramaHarness.createScan).toHaveBeenCalledWith("connection-a", {
      scope_mode: "selected_regions",
      region_ids: ["cn-hangzhou"],
    }),
  );
  await waitFor(() =>
    expect(screen.getByTestId("current-location")).toHaveTextContent(
      "/scans/scan-region-a",
    ),
  );
});

it("rescans every selected Region from one context-menu action", async () => {
  if (accountResponse.view.kind !== "account") {
    throw new Error("expected account topology fixture");
  }
  const multipleRegionView: AccountTopologyView = {
    kind: "account",
    regions: [
      accountResponse.view.regions[0]!,
      {
        key: "region-beijing",
        name: "华北2（北京）",
        native_id: "cn-beijing",
        resource_count: 208,
        cleanup,
      },
    ],
  };
  panoramaHarness.getTopology.mockResolvedValue({
    ...accountResponse,
    view: multipleRegionView,
  });
  const user = userEvent.setup();
  renderPanorama();

  const hangzhou = await screen.findByRole("button", {
    name: /华东1（杭州）.*3,376 resources/,
  });
  const beijing = screen.getByRole("button", {
    name: /华北2（北京）.*208 resources/,
  });
  fireEvent.click(hangzhou, { shiftKey: true });
  fireEvent.click(beijing, { shiftKey: true });
  expect(hangzhou).toHaveAttribute("aria-pressed", "true");
  expect(beijing).toHaveAttribute("aria-pressed", "true");

  fireEvent.contextMenu(hangzhou);
  await user.click(await screen.findByRole("menuitem", { name: "Rescan" }));

  const confirmation = await screen.findByRole("alertdialog");
  expect(within(confirmation).getByText("Rescan 2 Regions?")).toBeVisible();
  expect(confirmation).toHaveTextContent(
    "A new scan task will be created for the selected 2 Regions.",
  );
  await user.click(
    within(confirmation).getByRole("button", { name: "Rescan" }),
  );

  await waitFor(() =>
    expect(panoramaHarness.createScan).toHaveBeenCalledWith("connection-a", {
      scope_mode: "selected_regions",
      region_ids: ["cn-beijing", "cn-hangzhou"],
    }),
  );
  await waitFor(() =>
    expect(screen.getByTestId("current-location")).toHaveTextContent(
      "/scans/scan-region-a",
    ),
  );
});

it("loads the selected Region and VPC with region-name and VPC-ID breadcrumbs", async () => {
  const user = userEvent.setup();
  renderPanorama();

  expect(await screen.findByTestId("topology-summary-canvas")).toBeVisible();
  const toolbar = screen.getByRole("toolbar", { name: "Resource Panorama" });
  expect(within(toolbar).getByText("Scan coverage")).toBeVisible();
  expect(
    within(toolbar).queryByRole("button", { name: "Account" }),
  ).not.toBeInTheDocument();
  expect(queryForCall(panoramaHarness.getTopology.mock.calls[0])).toEqual(
    expect.objectContaining({ limit: 10_000 }),
  );
  expect(screen.getByRole("application")).toBeVisible();
  expect(screen.queryByTestId("topology-summary-list")).not.toBeInTheDocument();
  const hangzhouRegion = screen.getByRole("button", {
    name: /华东1（杭州）.*3,376 resources/,
  });
  expect(
    hangzhouRegion.querySelector(
      '[data-summary-entry-icon="region"] [data-resource-icon-mask]',
    ),
  ).toHaveStyle({
    maskImage: `url(${REGION_ICON_SRC})`,
    WebkitMaskImage: `url(${REGION_ICON_SRC})`,
  });
  await user.click(screen.getByRole("button", { name: "Empty Regions · 3" }));
  expect(screen.getByRole("button", { name: /华北2（北京）/ })).toBeVisible();
  expect(screen.queryByText("0 resources")).not.toBeInTheDocument();

  await user.click(screen.getByRole("button", { name: /华东1（杭州）/ }));
  await waitFor(() =>
    expect(
      panoramaHarness.getTopology.mock.calls.some(
        (call) => queryForCall(call).focus_key === REGION_FOCUS_KEY,
      ),
    ).toBe(true),
  );
  expect(screen.getByTestId("current-location")).toHaveTextContent(
    "/panorama/regions/cn-hangzhou",
  );
  expect(await screen.findByText("Global resources")).toBeVisible();
  expect(
    within(toolbar).queryByRole("button", { name: "Account" }),
  ).not.toBeInTheDocument();
  const regionBreadcrumb = within(toolbar).getByRole("button", {
    name: "华东1（杭州）",
  });
  expect(regionBreadcrumb).toBeVisible();
  expect(regionBreadcrumb).toHaveClass("text-sm");
  expect(regionBreadcrumb).not.toHaveClass("text-xs");
  expect(regionBreadcrumb).toHaveClass(
    "text-muted-foreground",
    "transition-colors",
    "hover:text-foreground",
  );
  expect(regionBreadcrumb).toHaveAttribute("aria-current", "page");
  expect(
    within(toolbar).queryByRole("button", { name: "cn-hangzhou" }),
  ).not.toBeInTheDocument();
  expect(screen.queryByText("Public resources")).not.toBeInTheDocument();
  const productionVPC = screen.getByRole("button", {
    name: /生产网络.*vpc-production.*88 resources/,
  });
  expect(
    productionVPC.querySelector(
      '[data-summary-entry-icon="vpc"] [data-resource-icon-mask]',
    ),
  ).toHaveStyle({
    maskImage: `url(${VPC_ICON_SRC})`,
    WebkitMaskImage: `url(${VPC_ICON_SRC})`,
  });
  await user.click(screen.getByRole("button", { name: "Empty VPCs · 3" }));
  expect(screen.getByText("空网络")).toBeVisible();

  await user.click(screen.getByRole("button", { name: /生产网络/ }));
  await waitFor(() =>
    expect(
      panoramaHarness.getTopology.mock.calls.some(
        (call) => queryForCall(call).focus_key === VPC_FOCUS_KEY,
      ),
    ).toBe(true),
  );
  expect(screen.getByTestId("current-location")).toHaveTextContent(
    "/panorama/regions/cn-hangzhou/vpcs/vpc-production",
  );
  expect(await screen.findByTestId("topology-canvas")).toHaveAttribute(
    "data-view-kind",
    "vpc",
  );
  expect(screen.getByRole("button", { name: "华东1（杭州）" })).toBeVisible();
  const vpcBreadcrumb = screen.getByRole("button", {
    name: "vpc-production",
  });
  expect(vpcBreadcrumb).toBeVisible();
  expect(vpcBreadcrumb).toHaveClass("px-0.5");
  expect(vpcBreadcrumb).toHaveClass("font-semibold");
  expect(vpcBreadcrumb).toHaveAttribute("aria-current", "page");
  expect(
    screen.getByRole("button", { name: "华东1（杭州）" }),
  ).not.toHaveAttribute("aria-current");
  expect(
    screen.queryByRole("button", { name: "生产网络" }),
  ).not.toBeInTheDocument();
  const breadcrumbNavigation = within(toolbar).getByRole("navigation", {
    name: "Resource Panorama",
  });
  expect(breadcrumbNavigation).toHaveClass("gap-0");
  expect(vpcBreadcrumb.parentElement).toHaveClass("gap-0");

  const callsBeforeRootReset = panoramaHarness.getTopology.mock.calls.length;
  window.dispatchEvent(new Event("steward:panorama-root"));
  await waitFor(() =>
    expect(panoramaHarness.getTopology.mock.calls.length).toBeGreaterThan(
      callsBeforeRootReset,
    ),
  );
  expect(
    queryForCall(panoramaHarness.getTopology.mock.calls.at(-1) ?? []),
  ).toHaveProperty("focus_key", undefined);
  expect(await screen.findByTestId("topology-summary-canvas")).toBeVisible();
  expect(screen.getByTestId("current-location")).toHaveTextContent(
    /^\/panorama$/,
  );
  expect(
    within(toolbar).queryByRole("button", { name: "华东1（杭州）" }),
  ).not.toBeInTheDocument();
});

it("keeps a chosen search resource highlighted and requests zoom for click and Enter", async () => {
  panoramaHarness.listAssets.mockResolvedValue({
    items: [
      {
        id: "asset-a",
        identity: {
          provider: "alicloud",
          partition: "aliyun",
          connection_id: "connection-a",
          native_type: "ACS::ECS::Instance",
          native_id: "i-production-ecs",
        },
        scope_id: "scope-global",
        resource_kind_id: "ecs-instance",
        name: "生产 ECS",
        capabilities: [],
        first_seen_at: "2026-08-03T00:00:00Z",
        last_seen_at: "2026-08-03T00:00:00Z",
      },
    ],
  });
  renderPanorama({ initialEntries: ["/panorama/global"] });

  const canvas = await screen.findByTestId("topology-canvas");
  const input = screen.getByRole("combobox", {
    name: "Search the current resource canvas",
  });
  fireEvent.change(input, { target: { value: "production" } });
  const option = await screen.findByRole("option", { name: /生产 ECS/ });

  fireEvent.pointerDown(option);
  await waitFor(() =>
    expect(canvas).toHaveAttribute("data-highlighted-node-key", "asset-a"),
  );
  expect(canvas).toHaveAttribute("data-search-focus-request", "1:asset-a");

  fireEvent.focus(input);
  fireEvent.keyDown(input, { key: "ArrowDown" });
  fireEvent.keyDown(input, { key: "Enter" });
  await waitFor(() =>
    expect(canvas).toHaveAttribute("data-search-focus-request", "2:asset-a"),
  );
  expect(canvas).toHaveAttribute("data-highlighted-node-key", "asset-a");
});

it("consumes a resource detail deep link through the search focus flow", async () => {
  renderPanorama({
    initialEntries: ["/panorama?resource=asset-a"],
    remountOnPathname: true,
  });

  const canvas = await screen.findByTestId("topology-canvas");
  await waitFor(() =>
    expect(canvas).toHaveAttribute("data-view-kind", "resource_graph"),
  );
  expect(panoramaHarness.findAsset).toHaveBeenCalledWith(
    "connection-a",
    "asset-a",
  );
  expect(canvas).toHaveAttribute("data-selected-resource-key", "asset-a");
  expect(canvas).toHaveAttribute("data-highlighted-node-key", "asset-a");
  expect(canvas).toHaveAttribute("data-search-focus-request", "1:asset-a");
  await waitFor(() =>
    expect(screen.getByTestId("current-location")).toHaveTextContent(
      /^\/panorama\/global$/,
    ),
  );
});

it("restores a VPC deep link and its breadcrumbs from the URL", async () => {
  renderPanorama({
    initialEntries: ["/panorama/regions/cn-hangzhou/vpcs/vpc-production"],
  });

  expect(await screen.findByTestId("topology-canvas")).toHaveAttribute(
    "data-view-kind",
    "vpc",
  );
  expect(queryForCall(panoramaHarness.getTopology.mock.calls[0])).toEqual(
    expect.objectContaining({ focus_key: VPC_FOCUS_KEY }),
  );
  expect(screen.getByRole("button", { name: "华东1（杭州）" })).toBeVisible();
  expect(screen.getByRole("button", { name: VPC_ID })).toBeVisible();
  expect(screen.getByTestId("current-location")).toHaveTextContent(
    "/panorama/regions/cn-hangzhou/vpcs/vpc-production",
  );
});

it("replaces a deep panorama path with the account root after switching connections", async () => {
  const user = userEvent.setup();
  const { rerenderPanorama } = renderPanorama({
    initialEntries: ["/panorama/regions/cn-hangzhou/vpcs/vpc-production"],
  });

  expect(await screen.findByTestId("topology-canvas")).toHaveAttribute(
    "data-view-kind",
    "vpc",
  );

  panoramaHarness.connectionID = "connection-b";
  panoramaHarness.connectionName = "测试账号";
  rerenderPanorama();

  await waitFor(() =>
    expect(screen.getByTestId("current-location")).toHaveTextContent(
      /^\/panorama$/,
    ),
  );
  await waitFor(() =>
    expect(panoramaHarness.getTopology.mock.calls.at(-1)?.[0]).toBe(
      "connection-b",
    ),
  );
  expect(
    queryForCall(panoramaHarness.getTopology.mock.calls.at(-1) ?? []),
  ).toHaveProperty("focus_key", undefined);

  await user.click(screen.getByRole("button", { name: "Browser back" }));
  expect(screen.getByTestId("current-location")).toHaveTextContent(
    /^\/panorama$/,
  );
});

it("uses browser history to restore each panorama level", async () => {
  const user = userEvent.setup();
  renderPanorama();

  await user.click(
    await screen.findByRole("button", { name: /华东1（杭州）/ }),
  );
  await user.click(await screen.findByRole("button", { name: /生产网络/ }));
  expect(await screen.findByTestId("topology-canvas")).toHaveAttribute(
    "data-view-kind",
    "vpc",
  );

  await user.click(screen.getByRole("button", { name: "Browser back" }));
  expect(await screen.findByText("Global resources")).toBeVisible();
  expect(screen.getByTestId("current-location")).toHaveTextContent(
    "/panorama/regions/cn-hangzhou",
  );

  await user.click(screen.getByRole("button", { name: "Browser back" }));
  await waitFor(() =>
    expect(
      screen.queryByRole("button", { name: "华东1（杭州）" }),
    ).not.toBeInTheDocument(),
  );
  expect(screen.getByTestId("current-location")).toHaveTextContent(
    /^\/panorama$/,
  );

  await user.click(screen.getByRole("button", { name: "Browser forward" }));
  expect(await screen.findByText("Global resources")).toBeVisible();
  expect(screen.getByTestId("current-location")).toHaveTextContent(
    "/panorama/regions/cn-hangzhou",
  );
});

it("uses semantic paths for account and Region public resources", async () => {
  const user = userEvent.setup();
  renderPanorama();

  await user.click(
    await screen.findByRole("button", { name: /Global resources/ }),
  );
  expect(await screen.findByTestId("topology-canvas")).toHaveAttribute(
    "data-view-kind",
    "resource_graph",
  );
  expect(screen.getByTestId("current-location")).toHaveTextContent(
    "/panorama/global",
  );

  window.dispatchEvent(new Event("steward:panorama-root"));
  await user.click(
    await screen.findByRole("button", { name: /华东1（杭州）/ }),
  );
  await user.click(
    await screen.findByRole("button", { name: /Global resources/ }),
  );

  expect(await screen.findByTestId("topology-canvas")).toHaveAttribute(
    "data-view-kind",
    "resource_graph",
  );
  expect(screen.getByTestId("current-location")).toHaveTextContent(
    "/panorama/regions/cn-hangzhou/public",
  );
});

it("does not expose account-global cleanup from summary frames", async () => {
  if (accountResponse.view.kind !== "account") {
    throw new Error("expected account fixture");
  }
  panoramaHarness.getTopology.mockResolvedValue({
    ...accountResponse,
    view: {
      ...accountResponse.view,
      global_resources: {
        ...accountResponse.view.global_resources,
        cleanup: {
          selectable: true,
          selector_kind: "scope",
          selector_key: "scope-global",
          potential_blockers: 0,
        },
      },
    },
  });
  renderPanorama();

  expect(
    await screen.findByRole("button", {
      name: /Global resources.*12 resources/,
    }),
  ).toBeVisible();
  expect(
    screen.queryByRole("button", {
      name: "Clean up Global resources",
    }),
  ).not.toBeInTheDocument();
  expect(
    panoramaHarness.getTopology.mock.calls.some(
      (call) => queryForCall(call).focus_key === "account-global",
    ),
  ).toBe(false);
});

it("does not expose VPC cleanup while the summary frame still drills down", async () => {
  if (regionResponse.view.kind !== "region") {
    throw new Error("expected Region fixture");
  }
  const selectableRegionResponse: TopologyResponse = {
    ...regionResponse,
    view: {
      ...regionResponse.view,
      vpcs: regionResponse.view.vpcs.map((entry) =>
        entry.key === VPC_FOCUS_KEY
          ? {
              ...entry,
              cleanup: {
                selectable: true,
                selector_kind: "group",
                selector_key: "group-vpc-a",
                potential_blockers: 0,
              },
            }
          : entry,
      ),
    },
  };
  panoramaHarness.getTopology.mockImplementation(
    async (_connectionID: string, query: TopologyQuery) =>
      query.focus_key === REGION_FOCUS_KEY
        ? selectableRegionResponse
        : accountResponse,
  );
  const user = userEvent.setup();
  renderPanorama();

  await user.click(
    await screen.findByRole("button", { name: /华东1（杭州）/ }),
  );
  expect(
    screen.queryByRole("button", { name: "Clean up 生产网络" }),
  ).not.toBeInTheDocument();
  await user.click(await screen.findByRole("button", { name: /生产网络/ }));

  expect(
    panoramaHarness.getTopology.mock.calls.some(
      (call) => queryForCall(call).focus_key === VPC_FOCUS_KEY,
    ),
  ).toBe(true);
});

it("opens the account-global resource graph without fetching inactive summaries", async () => {
  const user = userEvent.setup();
  renderPanorama();

  await user.click(
    await screen.findByRole("button", { name: /Global resources/ }),
  );

  await waitFor(() =>
    expect(
      panoramaHarness.getTopology.mock.calls.some(
        (call) => queryForCall(call).focus_key === "account-global",
      ),
    ).toBe(true),
  );
  expect(await screen.findByTestId("topology-canvas")).toHaveAttribute(
    "data-view-kind",
    "resource_graph",
  );
  expect(screen.getByTestId("current-location")).toHaveTextContent(
    "/panorama/global",
  );
  expect(
    screen.getByRole("button", { name: "Global resources" }),
  ).toBeVisible();
  expect(
    screen.queryByRole("button", { name: "account-global" }),
  ).not.toBeInTheDocument();
});

it("changes only canvas focus when a resource is clicked", async () => {
  const user = userEvent.setup();
  renderPanorama();

  await user.click(
    await screen.findByRole("button", { name: /Global resources/ }),
  );
  const resourceButton = await screen.findByRole("button", {
    name: /生产 ECS/,
  });
  await user.click(resourceButton);

  expect(resourceButton).toHaveAttribute("aria-current", "true");
  expect(screen.getByTestId("topology-canvas")).toHaveAttribute(
    "data-selected-resource-key",
    "asset-a",
  );
  expect(
    document.querySelector('[data-slot="sheet-content"]'),
  ).not.toBeInTheDocument();
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(screen.getByTestId("current-location")).toHaveTextContent(
    "/panorama/global",
  );
});

it("keeps external relations off canvas and opens details in the centered canvas dialog", async () => {
  const response: TopologyResponse = {
    ...globalResponse,
    view: {
      ...globalResponse.view,
      resources: [
        {
          ...globalResponse.view.resources[0],
          external_relations: [
            {
              key: "external-a",
              kind: "relationship",
              relation: "uses",
              direction: "outgoing",
              target_id: "external-security-group",
              target_name: "shared-security-group",
              target_type: "Security Group",
            },
          ],
        },
      ],
    },
  };
  panoramaHarness.useActualTopologyCanvas = true;
  panoramaHarness.getTopology.mockImplementation(
    async (_connectionID: string, query: TopologyQuery) =>
      query.focus_key ? response : accountResponse,
  );
  const user = userEvent.setup();
  renderPanorama();

  await user.click(
    await screen.findByRole("button", { name: /Global resources/ }),
  );
  const resourceButton = await screen.findByRole("button", {
    name: /生产 ECS/,
  });
  expect(screen.queryByText("shared-security-group")).not.toBeInTheDocument();

  fireEvent.contextMenu(resourceButton);
  await user.click(
    await screen.findByRole("menuitem", { name: "View details" }),
  );
  const details = await screen.findByRole("dialog");
  expect(within(details).getByText("生产 ECS")).toBeVisible();
  expect(within(details).getByText("Resource properties")).toBeVisible();
  expect(
    document.querySelector('[data-slot="sheet-content"]'),
  ).not.toBeInTheDocument();
});

it("lets the real canvas consume one Escape layer at a time before clearing panorama focus", async () => {
  vi.stubGlobal(
    "ResizeObserver",
    class ResizeObserver {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  expect(globalThis.ResizeObserver).toBeDefined();
  panoramaHarness.useActualTopologyCanvas = true;
  panoramaHarness.getTopology.mockImplementation(
    async (_connectionID: string, query: TopologyQuery) =>
      query.focus_key ? escapeLayerResponse : accountResponse,
  );
  const user = userEvent.setup();
  renderPanorama();

  await user.click(
    await screen.findByRole("button", { name: /Global resources/ }),
  );
  const focusedResource = await screen.findByRole("button", {
    name: "生产 ECS",
  });
  await user.click(focusedResource);
  const selectedNodes = panoramaHarness.flowNodes.filter(
    (node) => node.id === "asset-security-group",
  );
  act(() => {
    panoramaHarness.onSelectionChange?.({ nodes: selectedNodes });
    panoramaHarness.onSelectionEnd?.({ shiftKey: false });
  });
  expect(
    screen.getByRole("toolbar", { name: "Box selection actions" }),
  ).toHaveTextContent("1 selected");
  expect(screen.getByTestId("topology-canvas")).toHaveAttribute(
    "data-selected-resource-key",
    "asset-a",
  );

  fireEvent.contextMenu(screen.getByRole("button", { name: /共享安全组/ }));
  expect(await screen.findByRole("menu")).toBeVisible();
  await user.keyboard("{Escape}");
  await waitFor(() =>
    expect(screen.queryByRole("menu")).not.toBeInTheDocument(),
  );
  expect(
    screen.getByRole("toolbar", { name: "Box selection actions" }),
  ).toBeVisible();
  expect(screen.getByTestId("topology-canvas")).toHaveAttribute(
    "data-selected-resource-key",
    "asset-a",
  );

  fireEvent.contextMenu(screen.getByRole("button", { name: /共享安全组/ }));
  await user.click(
    await screen.findByRole("menuitem", { name: "View details" }),
  );
  expect(await screen.findByRole("dialog")).toBeVisible();
  await user.keyboard("{Escape}");
  await waitFor(() =>
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
  );
  expect(
    screen.getByRole("toolbar", { name: "Box selection actions" }),
  ).toBeVisible();
  expect(screen.getByTestId("topology-canvas")).toHaveAttribute(
    "data-selected-resource-key",
    "asset-a",
  );

  fireEvent.contextMenu(
    screen.getByRole("button", { name: /Database.*2 resources/ }),
  );
  await user.click(
    await screen.findByRole("menuitem", {
      name: "View included resources",
    }),
  );
  expect(await screen.findByRole("dialog")).toBeVisible();
  await user.keyboard("{Escape}");
  await waitFor(() =>
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
  );
  expect(
    screen.getByRole("toolbar", { name: "Box selection actions" }),
  ).toBeVisible();
  expect(screen.getByTestId("topology-canvas")).toHaveAttribute(
    "data-selected-resource-key",
    "asset-a",
  );

  await user.keyboard("{Escape}");
  expect(
    screen.queryByRole("toolbar", { name: "Box selection actions" }),
  ).not.toBeInTheDocument();
  expect(screen.getByTestId("topology-canvas")).not.toHaveAttribute(
    "data-selected-resource-key",
  );
});

it("clears focus from blank click and Escape without opening another surface", async () => {
  panoramaHarness.useActualTopologyCanvas = true;
  const user = userEvent.setup();
  renderPanorama();

  await user.click(
    await screen.findByRole("button", { name: /Global resources/ }),
  );
  const resourceButton = await screen.findByRole("button", {
    name: /生产 ECS/,
  });
  await user.click(resourceButton);
  expect(resourceButton).toHaveAttribute("aria-current", "true");

  await user.click(screen.getByRole("button", { name: "Blank canvas" }));
  expect(resourceButton).not.toHaveAttribute("aria-current");

  await user.click(resourceButton);
  expect(resourceButton).toHaveAttribute("aria-current", "true");
  fireEvent.keyDown(window, { key: "Escape" });
  await waitFor(() =>
    expect(resourceButton).not.toHaveAttribute("aria-current"),
  );
  expect(
    document.querySelector('[data-slot="sheet-content"]'),
  ).not.toBeInTheDocument();
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
});

it("keeps cleanup targets and the bottom-right list mounted across panorama levels", async () => {
  panoramaHarness.cleanupTargets = [
    {
      key: "asset:asset-a",
      kind: "resource",
      connectionId: "connection-a",
      displayName: "生产 ECS",
      selector: {
        kind: "asset",
        asset_id: "asset-a",
        display_name: "生产 ECS",
      },
      ancestryKeys: ["account-global", "asset:asset-a"],
    },
  ];
  const user = userEvent.setup();
  renderPanorama();

  expect(
    await screen.findByRole("button", { name: "Resource list · 1" }),
  ).toBeVisible();
  expect(await screen.findByTestId("topology-summary-canvas")).toBeVisible();
  await user.click(screen.getByRole("button", { name: /华东1（杭州）/ }));
  expect(
    await screen.findByRole("button", { name: "Resource list · 1" }),
  ).toBeVisible();
  await user.click(await screen.findByRole("button", { name: /生产网络/ }));
  expect(await screen.findByTestId("topology-canvas")).toHaveAttribute(
    "data-view-kind",
    "vpc",
  );
  expect(
    screen.queryByRole("button", { name: /生产 ECS/ }),
  ).not.toBeInTheDocument();
  expect(
    screen.getByRole("button", { name: "Resource list · 1" }),
  ).toBeVisible();

  await user.click(screen.getByRole("button", { name: "华东1（杭州）" }));
  expect(
    await screen.findByRole("button", { name: "Resource list · 1" }),
  ).toBeVisible();
  window.dispatchEvent(new Event("steward:panorama-root"));
  expect(
    await screen.findByRole("button", { name: "Resource list · 1" }),
  ).toBeVisible();
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(
    document.querySelector('[data-slot="sheet-content"]'),
  ).not.toBeInTheDocument();
});

it("navigates from a cleanup item to its owning canvas and highlights it", async () => {
  panoramaHarness.cleanupTargets = [
    {
      key: "asset:asset-a",
      kind: "resource",
      connectionId: "connection-a",
      displayName: "生产 ECS",
      selector: {
        kind: "asset",
        asset_id: "asset-a",
        display_name: "生产 ECS",
      },
      ancestryKeys: ["account-global", "asset:asset-a"],
      locationContext: {
        scope: { key: "account-global", name: "账号全局资源" },
      },
    },
  ];
  const user = userEvent.setup();
  renderPanorama({ initialEntries: ["/panorama/regions/cn-hangzhou"] });

  await user.click(
    await screen.findByRole("button", { name: "Resource list · 1" }),
  );
  await user.click(
    screen.getByRole("button", { name: "Locate 生产 ECS on canvas" }),
  );

  await waitFor(() =>
    expect(screen.getByTestId("current-location")).toHaveTextContent(
      "/panorama/global",
    ),
  );
  expect(await screen.findByTestId("topology-canvas")).toHaveAttribute(
    "data-highlighted-node-key",
    "asset:asset-a",
  );
  expect(screen.getByTestId("topology-canvas")).toHaveAttribute(
    "data-focused-cleanup-target-key",
    "asset:asset-a",
  );
  expect(
    screen.queryByRole("dialog", { name: "Resource list" }),
  ).not.toBeInTheDocument();
});

it("does not retain focus when the topology revision changes", async () => {
  let currentResponse = globalResponse;
  panoramaHarness.getTopology.mockImplementation(
    async (_connectionID: string, query: TopologyQuery) =>
      query.focus_key ? currentResponse : accountResponse,
  );
  const user = userEvent.setup();
  const { queryClient } = renderPanorama();

  await user.click(
    await screen.findByRole("button", { name: /Global resources/ }),
  );
  const resourceButton = await screen.findByRole("button", {
    name: /生产 ECS/,
  });
  await user.click(resourceButton);
  expect(resourceButton).toHaveAttribute("aria-current", "true");

  currentResponse = {
    ...globalResponse,
    revision: {
      ...globalResponse.revision,
      inventory: "inventory-b",
    },
  };
  await act(async () => {
    await queryClient.refetchQueries({
      queryKey: ["topology"],
      type: "active",
    });
  });

  await waitFor(() =>
    expect(screen.getByTestId("topology-canvas")).not.toHaveAttribute(
      "data-selected-resource-key",
    ),
  );
});

it("passes projection warnings to the canvas as compact non-blocking status", async () => {
  panoramaHarness.getTopology.mockImplementation(
    async (_connectionID: string, query: TopologyQuery) => {
      if (!query.focus_key) return accountResponse;
      if (query.focus_key === REGION_FOCUS_KEY) return regionResponse;
      return {
        ...vpcResponse,
        warnings: [
          {
            code: "topology.endpoint_unavailable",
            relation_key: "relation-a",
            message: "one endpoint is unavailable",
          },
        ],
      };
    },
  );
  const user = userEvent.setup();
  renderPanorama();

  await user.click(
    await screen.findByRole("button", { name: /华东1（杭州）/ }),
  );
  await user.click(await screen.findByRole("button", { name: /生产网络/ }));

  expect(
    await screen.findByText("one endpoint is unavailable"),
  ).toHaveAttribute("role", "status");
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
});

it("keeps fetching only the current focus until its cursor is complete", async () => {
  const pageOne = {
    ...globalResponse,
    next_cursor: "cursor-2",
    truncated: true,
  };
  const pageTwo = {
    ...globalResponse,
    view: {
      ...globalResponse.view,
      resources: [],
      edges: [],
    },
    truncated: false,
  } as TopologyResponse;
  panoramaHarness.getTopology.mockImplementation(
    async (_connectionID: string, query: TopologyQuery) => {
      if (!query.focus_key) return accountResponse;
      return query.cursor === "cursor-2" ? pageTwo : pageOne;
    },
  );
  const user = userEvent.setup();
  renderPanorama();

  await user.click(
    await screen.findByRole("button", { name: /Global resources/ }),
  );

  await waitFor(() =>
    expect(
      panoramaHarness.getTopology.mock.calls.some(
        (call) => queryForCall(call).cursor === "cursor-2",
      ),
    ).toBe(true),
  );
  await waitFor(() =>
    expect(screen.getByTestId("topology-canvas")).toHaveAttribute(
      "data-complete",
      "true",
    ),
  );
  expect(
    panoramaHarness.getTopology.mock.calls.every(
      (call) =>
        !queryForCall(call).focus_key ||
        queryForCall(call).focus_key === "account-global",
    ),
  ).toBe(true);
});

it("stacks loaded independent resources when pagination is complete but coverage is incomplete", async () => {
  const incompleteCoverage: TopologyResponse = {
    ...globalResponse,
    coverage: { status: "partial", failed_shards: 1 },
    view: {
      ...globalResponse.view,
      resources: [
        globalResponse.view.resources[0]!,
        {
          ...globalResponse.view.resources[0]!,
          key: "asset-b",
          asset_id: "asset-b",
          name: "备用 ECS",
        },
      ],
    },
    truncated: false,
  };
  panoramaHarness.getTopology.mockImplementation(
    async (_connectionID: string, query: TopologyQuery) =>
      query.focus_key ? incompleteCoverage : accountResponse,
  );
  panoramaHarness.useActualTopologyCanvas = true;
  const user = userEvent.setup();
  renderPanorama();

  await user.click(
    await screen.findByRole("button", { name: /Global resources/ }),
  );

  const canvas = await screen.findByTestId("topology-canvas");
  expect(canvas).toHaveAttribute("data-complete", "true");
  expect(
    screen.getByRole("button", { name: /ECS Instance.*2 resources/ }),
  ).toBeVisible();
  expect(
    screen.queryByRole("button", { name: "生产 ECS" }),
  ).not.toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: "备用 ECS" }),
  ).not.toBeInTheDocument();
});

it("keeps the selected resource focused while a partial view becomes complete", async () => {
  const pageTwo = deferred<TopologyResponse>();
  panoramaHarness.getTopology.mockImplementation(
    async (_connectionID: string, query: TopologyQuery) => {
      if (!query.focus_key) return accountResponse;
      if (query.cursor === "cursor-2") return pageTwo.promise;
      return {
        ...globalResponse,
        next_cursor: "cursor-2",
        truncated: true,
      };
    },
  );
  const user = userEvent.setup();
  renderPanorama();

  await user.click(
    await screen.findByRole("button", { name: /Global resources/ }),
  );
  const resourceButton = await screen.findByRole("button", {
    name: "生产 ECS",
  });
  expect(screen.getByTestId("topology-canvas")).toHaveAttribute(
    "data-complete",
    "false",
  );
  await user.click(resourceButton);

  pageTwo.resolve({
    ...globalResponse,
    view: { ...globalResponse.view, resources: [], edges: [] },
    truncated: false,
  });

  await waitFor(() =>
    expect(screen.getByTestId("topology-canvas")).toHaveAttribute(
      "data-complete",
      "true",
    ),
  );
  const retainedButton = screen.getByRole("button", { name: "生产 ECS" });
  expect(retainedButton).toHaveAttribute("aria-current", "true");
  expect(screen.getByTestId("topology-canvas")).toHaveAttribute(
    "data-selected-resource-key",
    "asset-a",
  );
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
});

it("discards a stale cursor page and reloads the same focus from its first page once", async () => {
  const pageOne = {
    ...globalResponse,
    next_cursor: "cursor-2",
    truncated: true,
  };
  const freshPage = deferred<TopologyResponse>();
  let firstPageRequests = 0;
  panoramaHarness.getTopology.mockImplementation(
    async (_connectionID: string, query: TopologyQuery) => {
      if (!query.focus_key) return accountResponse;
      if (query.cursor === "cursor-2") {
        throw new APIRequestError(
          "topology cursor belongs to an older revision",
          "topology.cursor_stale",
        );
      }
      firstPageRequests += 1;
      return firstPageRequests === 1 ? pageOne : freshPage.promise;
    },
  );
  const user = userEvent.setup();
  renderPanorama();

  await user.click(
    await screen.findByRole("button", { name: /Global resources/ }),
  );

  await waitFor(() =>
    expect(
      panoramaHarness.getTopology.mock.calls.some(
        (call) => queryForCall(call).cursor === "cursor-2",
      ),
    ).toBe(true),
  );
  await waitFor(() => expect(firstPageRequests).toBe(2));
  expect(screen.queryByTestId("topology-canvas")).not.toBeInTheDocument();

  freshPage.resolve({
    ...globalResponse,
    revision: {
      ...globalResponse.revision,
      inventory: "inventory-fresh",
      projected_at: "2026-07-24T00:00:02Z",
    },
  });

  expect(await screen.findByTestId("topology-canvas")).toHaveAttribute(
    "data-complete",
    "true",
  );
  expect(firstPageRequests).toBe(2);
  expect(
    panoramaHarness.getTopology.mock.calls.filter(
      (call) => queryForCall(call).cursor === "cursor-2",
    ),
  ).toHaveLength(1);
});

it("stops after one cursor-stale recovery in the same unfinished load cycle", async () => {
  const pageOne = {
    ...globalResponse,
    next_cursor: "cursor-2",
    truncated: true,
  };
  const unexpectedThirdPage = deferred<TopologyResponse>();
  let firstPageRequests = 0;
  let cursorRequests = 0;
  panoramaHarness.getTopology.mockImplementation(
    async (_connectionID: string, query: TopologyQuery) => {
      if (!query.focus_key) return accountResponse;
      if (query.cursor === "cursor-2") {
        cursorRequests += 1;
        throw new APIRequestError(
          "topology cursor belongs to an older revision",
          "topology.cursor_stale",
        );
      }
      firstPageRequests += 1;
      return firstPageRequests <= 2 ? pageOne : unexpectedThirdPage.promise;
    },
  );
  const user = userEvent.setup();
  renderPanorama({ retry: 1 });

  await user.click(
    await screen.findByRole("button", { name: /Global resources/ }),
  );

  await waitFor(() => expect(cursorRequests).toBe(4));
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "topology cursor belongs to an older revision",
  );
  expect(firstPageRequests).toBe(2);
  expect(screen.queryByTestId("topology-canvas")).not.toBeInTheDocument();
});

it("resets the cursor-stale recovery budget after switching focus", async () => {
  const truncatedGlobal = {
    ...globalResponse,
    next_cursor: "cursor-2",
    truncated: true,
  };
  const truncatedRegionPublic = {
    ...regionPublicResponse,
    next_cursor: "region-cursor-2",
    truncated: true,
  };
  let globalFirstPageRequests = 0;
  let regionFirstPageRequests = 0;
  panoramaHarness.getTopology.mockImplementation(
    async (_connectionID: string, query: TopologyQuery) => {
      if (!query.focus_key) return accountResponse;
      if (query.focus_key === REGION_FOCUS_KEY) return regionResponse;
      if (query.focus_key === "account-global") {
        if (query.cursor === "cursor-2") {
          throw new APIRequestError(
            "global cursor is stale",
            "topology.cursor_stale",
          );
        }
        globalFirstPageRequests += 1;
        return truncatedGlobal;
      }
      if (query.focus_key === REGION_PUBLIC_FOCUS_KEY) {
        if (query.cursor === "region-cursor-2") {
          throw new APIRequestError(
            "region cursor is stale",
            "topology.cursor_stale",
          );
        }
        regionFirstPageRequests += 1;
        return regionFirstPageRequests === 1
          ? truncatedRegionPublic
          : regionPublicResponse;
      }
      return vpcResponse;
    },
  );
  const user = userEvent.setup();
  renderPanorama();

  await user.click(
    await screen.findByRole("button", { name: /Global resources/ }),
  );
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "global cursor is stale",
  );
  expect(globalFirstPageRequests).toBe(2);

  window.dispatchEvent(new Event("steward:panorama-root"));
  await user.click(
    await screen.findByRole("button", { name: /华东1（杭州）/ }),
  );
  await user.click(
    await screen.findByRole("button", { name: /Global resources/ }),
  );

  await waitFor(() =>
    expect(screen.getByTestId("topology-canvas")).toHaveAttribute(
      "data-complete",
      "true",
    ),
  );
  expect(screen.getByRole("button", { name: "华东1（杭州）" })).toBeVisible();
  expect(
    screen.getByRole("button", { name: "Global resources" }),
  ).toBeVisible();
  expect(
    screen.queryByRole("button", { name: REGION_PUBLIC_FOCUS_KEY }),
  ).not.toBeInTheDocument();
  expect(regionFirstPageRequests).toBe(2);
});

it("clears the selected resource immediately when a stale partial load is discarded", async () => {
  const stalePage = {
    ...globalResponse,
    next_cursor: "cursor-2",
    truncated: true,
  };
  const freshPage = deferred<TopologyResponse>();
  let loadingStaleCycle = false;
  let staleCycleFirstPageRequests = 0;
  panoramaHarness.getTopology.mockImplementation(
    async (_connectionID: string, query: TopologyQuery) => {
      if (!query.focus_key) return accountResponse;
      if (!loadingStaleCycle) return globalResponse;
      if (query.cursor === "cursor-2") {
        throw new APIRequestError(
          "topology cursor belongs to an older revision",
          "topology.cursor_stale",
        );
      }
      staleCycleFirstPageRequests += 1;
      return staleCycleFirstPageRequests === 1 ? stalePage : freshPage.promise;
    },
  );
  const user = userEvent.setup();
  const { queryClient } = renderPanorama();

  await user.click(
    await screen.findByRole("button", { name: /Global resources/ }),
  );
  await user.click(await screen.findByRole("button", { name: /生产 ECS/ }));
  expect(screen.getByTestId("topology-canvas")).toHaveAttribute(
    "data-selected-resource-key",
    "asset-a",
  );

  loadingStaleCycle = true;
  await act(async () => {
    await queryClient.refetchQueries({
      queryKey: ["topology"],
      type: "active",
    });
  });

  await waitFor(() => expect(staleCycleFirstPageRequests).toBe(2));
  expect(screen.queryByTestId("topology-canvas")).not.toBeInTheDocument();

  freshPage.resolve(globalResponse);
  const recoveredBoundary = await screen.findByTestId("topology-canvas");
  expect(recoveredBoundary).not.toHaveAttribute("data-selected-resource-key");
});

it("keeps focus when a visible resource snapshot updates without a revision change", async () => {
  let currentResponse = globalResponse;
  panoramaHarness.getTopology.mockImplementation(
    async (_connectionID: string, query: TopologyQuery) =>
      query.focus_key ? currentResponse : accountResponse,
  );
  const user = userEvent.setup();
  const { queryClient } = renderPanorama();

  await user.click(
    await screen.findByRole("button", { name: /Global resources/ }),
  );
  await user.click(await screen.findByRole("button", { name: /生产 ECS/ }));
  expect(screen.getByTestId("topology-canvas")).toHaveAttribute(
    "data-selected-resource-key",
    "asset-a",
  );

  currentResponse = {
    ...globalResponse,
    revision: {
      ...globalResponse.revision,
      projected_at: "2026-07-24T00:00:10Z",
    },
    coverage: { status: "partial", failed_shards: 1 },
    view: {
      ...globalResponse.view,
      resources: [
        {
          ...globalResponse.view.resources[0],
          asset_id: "asset-a-latest",
          name: "生产 ECS（最新）",
          type_name: "Updated ECS",
          finding_count: 9,
          actionable: false,
          cleanup: {
            selectable: false,
            potential_blockers: 3,
          },
        },
      ],
    },
  };
  await act(async () => {
    await queryClient.refetchQueries({
      queryKey: ["topology"],
      type: "active",
    });
  });

  const updatedResource = await screen.findByRole("button", {
    name: /生产 ECS（最新）/,
  });
  expect(updatedResource).toHaveAttribute("aria-current", "true");
  expect(screen.getByTestId("topology-canvas")).toHaveAttribute(
    "data-selected-resource-key",
    "asset-a",
  );
  expect(
    document.querySelector('[data-slot="sheet-content"]'),
  ).not.toBeInTheDocument();
});

it("clears focus when its resource disappears without a revision change", async () => {
  let currentResponse = globalResponse;
  panoramaHarness.getTopology.mockImplementation(
    async (_connectionID: string, query: TopologyQuery) =>
      query.focus_key ? currentResponse : accountResponse,
  );
  const user = userEvent.setup();
  const { queryClient } = renderPanorama();

  await user.click(
    await screen.findByRole("button", { name: /Global resources/ }),
  );
  await user.click(await screen.findByRole("button", { name: /生产 ECS/ }));
  expect(screen.getByTestId("topology-canvas")).toHaveAttribute(
    "data-selected-resource-key",
    "asset-a",
  );

  currentResponse = {
    ...globalResponse,
    view: {
      ...globalResponse.view,
      resources: [],
    },
  };
  await act(async () => {
    await queryClient.refetchQueries({
      queryKey: ["topology"],
      type: "active",
    });
  });

  await waitFor(() =>
    expect(
      screen.queryByRole("button", { name: /生产 ECS/ }),
    ).not.toBeInTheDocument(),
  );
  await waitFor(() =>
    expect(screen.getByTestId("topology-canvas")).not.toHaveAttribute(
      "data-selected-resource-key",
    ),
  );
});
