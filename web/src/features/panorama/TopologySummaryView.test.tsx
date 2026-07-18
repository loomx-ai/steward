import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  fireEvent,
  render as testingRender,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import type { ComponentProps, ReactElement } from "react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { findAsset, listProviderCatalog } from "@/api/client";
import type {
  AccountTopologyView,
  Asset,
  ProviderCatalogBundle,
  RegionTopologyView,
  ResourceKind,
} from "@/api/types";
import { TooltipProvider } from "@/components/ui/tooltip";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import type { CleanupTarget } from "./cleanupSelection";
import { REGION_ICON_SRC, VPC_ICON_SRC } from "./TopologyNodes";
import { TopologySummaryView } from "./TopologySummaryView";

const cleanup = { selectable: false, potential_blockers: 0 };
const cleanupSelection = vi.hoisted(() => ({
  targets: [] as CleanupTarget[],
  addTargets: vi.fn(),
  removeTarget: vi.fn(),
  removeBatchMember: vi.fn(),
}));
const reactFlowFitView = vi.hoisted(() => vi.fn());
const reactFlowGetViewport = vi.hoisted(() => vi.fn());
const reactFlowSetViewport = vi.hoisted(() => vi.fn());
const consoleResourceKinds = new Map<string, ResourceKind>([
  [
    "alicloud:ACS::VPC::VPC",
    {
      id: "alicloud:ACS::VPC::VPC",
      provider: "alicloud",
      native_type: "ACS::VPC::VPC",
      capabilities: ["indexed"],
      console_link_template:
        "https://vpc.console.aliyun.com/vpc/{regionId}/vpcs/{nativeId}",
      bundle_revision: "runtime-1",
    },
  ],
]);

vi.mock("@xyflow/react", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@xyflow/react")>();
  const react = await import("react");
  const ActualReactFlow = actual.ReactFlow;
  return {
    ...actual,
    ReactFlow: (props: ComponentProps<typeof ActualReactFlow>) =>
      react.createElement(ActualReactFlow, {
        ...props,
        onInit: (instance) => {
          const instrumentedInstance = new Proxy(instance, {
            get(target, property, receiver) {
              if (property === "fitView") {
                return (...args: Parameters<typeof instance.fitView>) => {
                  reactFlowFitView(...args);
                  return instance.fitView(...args);
                };
              }
              if (property === "getViewport") {
                return () => {
                  const viewport = instance.getViewport();
                  reactFlowGetViewport(viewport);
                  return viewport;
                };
              }
              if (property === "setViewport") {
                return (...args: Parameters<typeof instance.setViewport>) => {
                  reactFlowSetViewport(...args);
                  return instance.setViewport(...args);
                };
              }
              return Reflect.get(target, property, receiver);
            },
          });
          props.onInit?.(instrumentedInstance);
        },
      }),
  };
});

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
}));

vi.mock("./CleanupSelectionContext", () => ({
  useCleanupSelection: () => cleanupSelection,
}));

function render(ui: ReactElement) {
  return testingRender(<TooltipProvider>{ui}</TooltipProvider>);
}

it("keeps Region and VPC summary icons as local Alibaba Cloud SVGs", () => {
  expect(REGION_ICON_SRC).toBe("/icons/alicloud/acs-region.svg");
  expect(VPC_ICON_SRC).toBe("/icons/alicloud/acs-vpc-vpc.svg");
  expect(REGION_ICON_SRC).toMatch(/^\/icons\/alicloud\/.*\.svg$/);
  expect(VPC_ICON_SRC).toMatch(/^\/icons\/alicloud\/.*\.svg$/);
});

it("renders a visible solid highlight for a search-related Region or VPC", async () => {
  const view: AccountTopologyView = {
    kind: "account",
    regions: [
      {
        key: "region-zhangjiakou",
        name: "华北3（张家口）",
        native_id: "cn-zhangjiakou",
        resource_count: 178,
        cleanup,
      },
    ],
  };

  render(
    <LocaleProvider>
      <div style={{ width: 1600, height: 800 }}>
        <TopologySummaryView
          view={view}
          coverageStatus="complete"
          highlightedEntryKey="region-zhangjiakou"
          focusedCleanupTargetKey="region-zhangjiakou"
          onNavigate={() => undefined}
        />
      </div>
    </LocaleProvider>,
  );
  await revealMeasuredSummaryNodes();

  const region = screen.getByRole("button", {
    name: /华北3（张家口）.*178/,
  });
  expect(region).toHaveAttribute("data-search-highlighted", "true");
  expect(region).toHaveClass(
    "outline-solid",
    "outline-2",
    "outline-primary",
    "bg-primary/5",
  );
  await waitFor(() =>
    expect(reactFlowFitView).toHaveBeenCalledWith(
      expect.objectContaining({
        nodes: [expect.objectContaining({ id: "region-zhangjiakou" })],
      }),
    ),
  );
});

beforeEach(() => {
  localStorage.clear();
  cleanupSelection.targets = [];
  cleanupSelection.addTargets.mockReset();
  cleanupSelection.removeTarget.mockReset();
  cleanupSelection.removeBatchMember.mockReset();
  reactFlowFitView.mockReset();
  reactFlowGetViewport.mockReset();
  reactFlowSetViewport.mockReset();
  vi.mocked(findAsset).mockReset();
  vi.mocked(listProviderCatalog).mockReset();
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
      readonly callback: ResizeObserverCallback;

      constructor(callback: ResizeObserverCallback) {
        this.callback = callback;
      }

      observe(target: Element) {
        this.callback(
          [{ target } as ResizeObserverEntry],
          this as unknown as globalThis.ResizeObserver,
        );
      }

      unobserve() {}
      disconnect() {}
    },
  );
  vi.stubGlobal(
    "DOMMatrixReadOnly",
    class DOMMatrixReadOnly {
      readonly m22 = 1;
    },
  );
  vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(
    function getBoundingClientRect(this: Element) {
      const element = this as HTMLElement;
      const width =
        Number.parseFloat(element.style.width) ||
        Number.parseFloat(element.parentElement?.style.width ?? "") ||
        1600;
      const height =
        Number.parseFloat(element.style.height) ||
        Number.parseFloat(element.parentElement?.style.height ?? "") ||
        800;
      const translation = element.style.transform.match(
        /translate\((-?[\d.]+)px,\s*(-?[\d.]+)px\)/,
      );
      const left = translation ? Number.parseFloat(translation[1]) : 0;
      const top = translation ? Number.parseFloat(translation[2]) : 0;
      return {
        bottom: top + height,
        height,
        left,
        right: left + width,
        top,
        width,
        x: left,
        y: top,
        toJSON: () => undefined,
      };
    },
  );
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function revealMeasuredSummaryNodes() {
  await waitFor(() =>
    expect(document.querySelector(".react-flow__node")).not.toBeNull(),
  );
  document
    .querySelectorAll<HTMLElement>(".react-flow__node")
    .forEach((node) => {
      node.style.visibility = "visible";
    });
}

async function waitForSummaryFocusHandoff() {
  await new Promise<void>((resolve) => {
    requestAnimationFrame(() => resolve());
  });
}

it("renders account-global and every Region including a zero count as canvas frames", async () => {
  const view: AccountTopologyView = {
    kind: "account",
    global_resources: {
      key: "account-global",
      name: "账号全局资源",
      resource_count: 12,
      cleanup,
    },
    regions: [
      {
        key: "region-a",
        name: "华东1（杭州）",
        native_id: "cn-hangzhou",
        resource_count: 3376,
        cleanup,
      },
      {
        key: "region-b",
        name: "华北2（北京）",
        resource_count: 0,
        cleanup,
      },
    ],
  };
  const onNavigate = vi.fn();
  const user = userEvent.setup();
  localStorage.setItem("steward.locale", "en-US");

  render(
    <LocaleProvider>
      <div style={{ width: 1600, height: 800 }}>
        <TopologySummaryView
          view={view}
          coverageStatus="complete"
          onNavigate={onNavigate}
        />
      </div>
    </LocaleProvider>,
  );
  await revealMeasuredSummaryNodes();

  expect(screen.getByTestId("topology-summary-canvas")).toBeVisible();
  expect(
    screen.getByTestId("topology-summary-canvas").parentElement,
  ).toHaveClass("box-border");
  expect(
    screen.getByTestId("topology-summary-canvas").parentElement,
  ).not.toHaveClass("pt-12");
  expect(screen.getByRole("application")).toBeVisible();
  expect(
    screen.queryByRole("link", { name: "React Flow attribution" }),
  ).not.toBeInTheDocument();
  expect(screen.queryByTestId("topology-summary-list")).not.toBeInTheDocument();
  const zeroRegionButton = screen.getByRole("button", {
    name: "华北2（北京）",
  });
  const populatedRegionButton = screen.getByRole("button", {
    name: /华东1（杭州）.*3,376 resources/,
  });
  expect(zeroRegionButton).toHaveClass("rounded-lg");
  expect(
    zeroRegionButton.querySelector("[data-summary-resource-count]"),
  ).toBeNull();
  expect(populatedRegionButton).toHaveClass("rounded-lg");
  expect(
    populatedRegionButton.querySelector(
      '[data-summary-entry-icon="region"] [data-resource-icon-mask]',
    ),
  ).toHaveStyle({
    maskImage: `url(${REGION_ICON_SRC})`,
    WebkitMaskImage: `url(${REGION_ICON_SRC})`,
  });
  expect(
    zeroRegionButton.querySelector(
      '[data-summary-entry-icon="region"] [data-resource-icon-mask]',
    ),
  ).toHaveStyle({
    maskImage: `url(${REGION_ICON_SRC})`,
    WebkitMaskImage: `url(${REGION_ICON_SRC})`,
  });
  expect(
    populatedRegionButton.querySelector("[data-summary-name]"),
  ).toHaveAttribute("title", "华东1（杭州）");
  const populatedRegionPrimary = populatedRegionButton.querySelector(
    "[data-summary-primary]",
  );
  expect(populatedRegionPrimary).toHaveClass("items-center");
  expect(populatedRegionPrimary).toContainElement(
    populatedRegionButton.querySelector('[data-summary-entry-icon="region"]'),
  );
  expect(populatedRegionPrimary).toContainElement(
    populatedRegionButton.querySelector("[data-summary-name]"),
  );
  expect(
    populatedRegionButton.querySelector("[data-summary-native-id]"),
  ).toHaveTextContent("cn-hangzhou");
  const populatedRegionIdentity = populatedRegionButton.querySelector(
    "[data-summary-identity]",
  );
  expect(populatedRegionIdentity).toContainElement(
    populatedRegionButton.querySelector("[data-summary-name]"),
  );
  expect(populatedRegionIdentity).toContainElement(
    populatedRegionButton.querySelector("[data-summary-native-id]"),
  );
  expect(
    populatedRegionButton.querySelector("[data-summary-resource-count]"),
  ).toHaveClass("absolute", "right-3", "bottom-1");
  expect(
    screen
      .getByRole("button", { name: /Global resources.*12 resources/ })
      .querySelector("[data-summary-entry-icon]"),
  ).toBeNull();
  expect(zeroRegionButton).toBeVisible();
  expect(
    screen.queryByRole("button", { name: /Empty Regions/ }),
  ).not.toBeInTheDocument();
  expect(zeroRegionButton).toHaveClass("nodrag", "nopan", "nowheel");
  expect(zeroRegionButton.closest(".react-flow__node")).toHaveStyle({
    height: "72px",
    pointerEvents: "all",
    width: "256px",
  });
  expect(screen.getByTestId("rf__node-account-global")).toHaveStyle({
    transform: "translate(0px,0px)",
  });
  expect(screen.getByTestId("rf__node-region-a")).toHaveStyle({
    transform: "translate(276px,0px)",
  });
  expect(screen.getByTestId("rf__node-region-b")).toHaveStyle({
    transform: "translate(0px,88px)",
  });
  expect(screen.queryByText("生产账号")).not.toBeInTheDocument();

  await user.click(screen.getByRole("button", { name: /华东1（杭州）/ }));
  expect(onNavigate).toHaveBeenCalledWith(view.regions[0]);
});

it("chooses Region rows and columns from the canvas aspect ratio", async () => {
  const regions = Array.from({ length: 37 }, (_, index) => ({
    key: `region-${index}`,
    name: `地域 ${index}`,
    resource_count: index + 1,
    cleanup,
  }));
  const view: AccountTopologyView = { kind: "account", regions };

  render(
    <LocaleProvider>
      <div style={{ width: 1600, height: 800 }}>
        <TopologySummaryView
          view={view}
          coverageStatus="complete"
          onNavigate={vi.fn()}
        />
      </div>
    </LocaleProvider>,
  );
  await revealMeasuredSummaryNodes();

  expect(screen.getByTestId("rf__node-region-30")).toHaveStyle({
    transform: "translate(0px,528px)",
  });
});

it("collapses multiple zero-resource Regions at one anchor and restores the stack there", async () => {
  const view: AccountTopologyView = {
    kind: "account",
    global_resources: {
      key: "account-global",
      name: "账号全局资源",
      resource_count: 0,
      cleanup,
    },
    regions: [
      {
        key: "region-a",
        name: "华东1（杭州）",
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
  };
  const onNavigate = vi.fn();
  const user = userEvent.setup();
  localStorage.setItem("steward.locale", "zh-CN");

  render(
    <LocaleProvider>
      <div style={{ width: 1200, height: 800 }}>
        <TopologySummaryView
          view={view}
          coverageStatus="complete"
          onNavigate={onNavigate}
        />
      </div>
    </LocaleProvider>,
  );
  await revealMeasuredSummaryNodes();

  expect(screen.getByRole("button", { name: "全局资源" })).toBeVisible();
  const collapsedStack = screen.getByRole("button", {
    name: "空地域 · 3",
  });
  const collapsedAnchor = collapsedStack.closest(".react-flow__node");
  expect(collapsedAnchor).toHaveAttribute(
    "data-testid",
    "rf__node-empty-account",
  );
  expect(collapsedAnchor).toHaveStyle({
    height: "72px",
    width: "256px",
  });
  expect(collapsedStack.querySelector("[data-empty-stack-frame]")).toHaveClass(
    "rounded-lg",
    "border",
    "bg-background/95",
  );
  expect(
    Array.from(collapsedStack.children).filter(
      (child) => child.tagName === "SPAN",
    ),
  ).toHaveLength(1);
  expect(collapsedStack.querySelector("[data-count-badge]")).toHaveTextContent(
    "3",
  );
  expect(
    collapsedStack.querySelector(
      '[data-summary-entry-icon="region"] [data-resource-icon-mask]',
    ),
  ).toHaveStyle({
    maskImage: `url(${REGION_ICON_SRC})`,
    WebkitMaskImage: `url(${REGION_ICON_SRC})`,
  });
  expect(within(collapsedStack).getByText("空地域")).toBeVisible();
  expect(collapsedAnchor).toHaveStyle({ transform: "translate(0px,88px)" });
  expect(
    screen.queryByRole("button", { name: /华北2（北京）/ }),
  ).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "全局资源" })).toBeVisible();

  await waitFor(() => expect(reactFlowFitView).toHaveBeenCalled());
  reactFlowFitView.mockClear();
  reactFlowGetViewport.mockClear();
  reactFlowSetViewport.mockClear();
  collapsedStack.focus();
  await user.keyboard("{Enter}");
  await waitForSummaryFocusHandoff();
  expect(reactFlowFitView).toHaveBeenCalledExactlyOnceWith({
    nodes: [{ id: "empty-account" }],
    padding: 0.04,
    maxZoom: 2.5,
  });

  const expandedAnchor = screen.getByTestId("rf__node-empty-account");
  expect(expandedAnchor).toHaveStyle({ transform: "translate(0px,88px)" });
  expect(
    expandedAnchor.querySelector("[data-empty-stack-expanded]"),
  ).toHaveClass("rounded-lg", "border-l-2", "bg-muted/[0.12]");
  expect(
    expandedAnchor.querySelector("[data-empty-stack-expanded]"),
  ).not.toHaveClass("border", "bg-background/50");
  const expandedHeader = expandedAnchor.querySelector<HTMLElement>(
    "[data-empty-stack-header]",
  );
  expect(expandedHeader).not.toBeNull();
  expect(expandedHeader).toHaveClass("h-10", "px-3");
  expect(
    within(expandedHeader as HTMLElement).getByText("空地域"),
  ).toBeVisible();
  expect(within(expandedHeader as HTMLElement).getByText("· 3")).toBeVisible();
  expect(expandedAnchor.querySelector("[data-empty-stack-grid]")).toHaveStyle({
    gridAutoRows: "72px",
  });
  expect(expandedAnchor.querySelector("[data-empty-stack-grid]")).toHaveClass(
    "gap-5",
  );
  expect(
    expandedAnchor.querySelector("[data-empty-stack-guide]"),
  ).not.toBeInTheDocument();
  const collapseButton = screen.getByRole("button", { name: "收缩" });
  expect(collapseButton).toBeVisible();
  expect(collapseButton).toHaveClass("size-7", "rounded-sm");
  expect(collapseButton).toHaveFocus();
  expect(
    within(expandedAnchor).queryByText("0 个资源"),
  ).not.toBeInTheDocument();
  const zeroRegion = screen.getByRole("button", {
    name: /华北2（北京）/,
  });
  expect(zeroRegion).toBeVisible();
  expect(zeroRegion).toHaveClass(
    "rounded-lg",
    "border-border",
    "bg-background/95",
    "shadow-sm",
  );
  expect(screen.getByRole("button", { name: /华南1（深圳）/ })).toBeVisible();
  expect(screen.getByRole("button", { name: /西南1（成都）/ })).toBeVisible();

  await user.click(zeroRegion);
  expect(onNavigate).toHaveBeenCalledWith(view.regions[1]);
  expect(onNavigate.mock.calls[0]?.[0]).toBe(view.regions[1]);

  await user.click(collapseButton);
  await waitForSummaryFocusHandoff();
  const viewportBeforeExpand = reactFlowGetViewport.mock.calls[0]?.[0];
  expect(viewportBeforeExpand).toEqual(
    expect.objectContaining({
      x: expect.any(Number),
      y: expect.any(Number),
      zoom: expect.any(Number),
    }),
  );
  await waitFor(() =>
    expect(reactFlowSetViewport).toHaveBeenCalledWith(viewportBeforeExpand),
  );
  const restoredStack = screen.getByRole("button", { name: "空地域 · 3" });
  expect(restoredStack).toHaveFocus();
  expect(restoredStack.closest(".react-flow__node")).toHaveStyle({
    transform: "translate(0px,88px)",
  });
  await user.keyboard(" ");
  await waitForSummaryFocusHandoff();
  expect(screen.getByRole("button", { name: "收缩" })).toHaveFocus();
  await user.keyboard(" ");
  await waitForSummaryFocusHandoff();
  expect(screen.getByRole("button", { name: "空地域 · 3" })).toHaveFocus();
});

it("places the empty Region stack in the next available grid cell", async () => {
  const regions = [
    ...Array.from({ length: 37 }, (_, index) => ({
      key: `region-${index}`,
      name: `地域 ${index}`,
      resource_count: index + 1,
      cleanup,
    })),
    ...Array.from({ length: 16 }, (_, index) => ({
      key: `empty-region-${index}`,
      name: `空地域 ${index}`,
      resource_count: 0,
      cleanup,
    })),
  ];

  render(
    <LocaleProvider>
      <div style={{ width: 1600, height: 800 }}>
        <TopologySummaryView
          view={{ kind: "account", regions }}
          coverageStatus="complete"
          onNavigate={vi.fn()}
        />
      </div>
    </LocaleProvider>,
  );
  await revealMeasuredSummaryNodes();

  expect(screen.getByTestId("rf__node-empty-account")).toHaveStyle({
    transform: "translate(552px,616px)",
  });
});

it("collapses zero-resource Regions after loading even when scan coverage is incomplete", async () => {
  const view: AccountTopologyView = {
    kind: "account",
    regions: [
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
    ],
  };

  render(
    <LocaleProvider>
      <div style={{ width: 1200, height: 800 }}>
        <TopologySummaryView
          view={view}
          coverageStatus="partial"
          onNavigate={vi.fn()}
        />
      </div>
    </LocaleProvider>,
  );
  await revealMeasuredSummaryNodes();

  expect(
    screen.getByRole("button", { name: /Empty Regions.*2/ }),
  ).toBeVisible();
  expect(
    screen.queryByRole("button", { name: "华北2（北京）" }),
  ).not.toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: "华南1（深圳）" }),
  ).not.toBeInTheDocument();
});

it("renders localized Region-public and every VPC as canvas frames without edges", async () => {
  const view: RegionTopologyView = {
    kind: "region",
    region: {
      key: "region-a",
      name: "华东1（杭州）",
      native_id: "cn-hangzhou",
    },
    public_resources: {
      key: "region-public-a",
      name: "Public resources",
      resource_count: 7,
      cleanup,
    },
    vpcs: [
      {
        key: "vpc-a",
        name: "生产网络名称很长需要截断展示",
        native_id: "vpc-production-abcdefghijklmnop",
        resource_count: 88,
        cleanup,
      },
      {
        key: "vpc-empty",
        name: "空网络",
        native_id: "vpc-empty",
        resource_count: 0,
        cleanup,
      },
      {
        key: "vpc-empty-2",
        name: "空网络 2",
        native_id: "vpc-empty-2",
        resource_count: 0,
        cleanup,
      },
    ],
  };
  const onNavigate = vi.fn();
  const user = userEvent.setup();
  localStorage.setItem("steward.locale", "zh-CN");

  render(
    <LocaleProvider>
      <div style={{ width: 1200, height: 800 }}>
        <TopologySummaryView
          view={view}
          complete={false}
          coverageStatus="partial"
          onNavigate={onNavigate}
        />
      </div>
    </LocaleProvider>,
  );
  await revealMeasuredSummaryNodes();

  expect(screen.getByTestId("topology-summary-canvas")).toBeVisible();
  expect(screen.getByText("全局资源")).toBeVisible();
  expect(screen.getByText("vpc-production-abcdefghijklmnop")).toBeVisible();
  expect(screen.queryByText("Public resources")).not.toBeInTheDocument();
  const populatedVPCButton = screen.getByRole("button", {
    name: /生产网络名称很长需要截断展示.*88 个资源/,
  });
  expect(populatedVPCButton).toBeVisible();
  expect(
    populatedVPCButton.querySelector("[data-summary-name]"),
  ).toHaveAttribute("title", "生产网络名称很长需要截断展示");
  expect(
    populatedVPCButton.querySelector("[data-summary-native-id]"),
  ).toHaveClass("whitespace-nowrap");
  expect(
    populatedVPCButton.querySelector("[data-summary-native-id]"),
  ).not.toHaveClass("truncate");
  const populatedVPCIdentity = populatedVPCButton.querySelector(
    "[data-summary-identity]",
  );
  expect(populatedVPCIdentity).toContainElement(
    populatedVPCButton.querySelector("[data-summary-name]"),
  );
  expect(populatedVPCIdentity).toContainElement(
    populatedVPCButton.querySelector("[data-summary-native-id]"),
  );
  expect(
    populatedVPCButton.querySelector("[data-summary-resource-count]"),
  ).toHaveClass("absolute", "right-3", "bottom-1");
  expect(
    populatedVPCButton.querySelector(
      '[data-summary-entry-icon="vpc"] [data-resource-icon-mask]',
    ),
  ).toHaveStyle({
    maskImage: `url(${VPC_ICON_SRC})`,
    WebkitMaskImage: `url(${VPC_ICON_SRC})`,
  });
  expect(
    screen
      .getByRole("button", { name: /全局资源.*7 个资源/ })
      .querySelector("[data-summary-entry-icon]"),
  ).toBeNull();
  const emptyVPCButton = screen.getByRole("button", {
    name: /^空网络vpc-empty$/,
  });
  expect(emptyVPCButton).toBeVisible();
  expect(
    emptyVPCButton.querySelector("[data-summary-resource-count]"),
  ).toBeNull();
  expect(
    screen.getByRole("button", {
      name: /^空网络 2vpc-empty-2$/,
    }),
  ).toBeVisible();
  expect(screen.queryByText(/已发现/)).not.toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: /空 VPC/ }),
  ).not.toBeInTheDocument();
  expect(screen.queryByTestId("topology-summary-list")).not.toBeInTheDocument();
  expect(document.querySelectorAll(".react-flow__edge")).toHaveLength(0);

  await user.click(
    screen.getByRole("button", {
      name: /生产网络名称很长需要截断展示/,
    }),
  );
  expect(onNavigate).toHaveBeenCalledWith(view.vpcs[0]);
});

it("keeps Region-public independent while collapsing multiple zero-resource VPCs", async () => {
  const view: RegionTopologyView = {
    kind: "region",
    region: {
      key: "region-a",
      name: "华东1（杭州）",
      native_id: "cn-hangzhou",
    },
    public_resources: {
      key: "region-public-a",
      name: "Public resources",
      resource_count: 0,
      cleanup,
    },
    vpcs: [
      { key: "vpc-a", name: "生产网络", resource_count: 88, cleanup },
      { key: "vpc-empty-a", name: "空网络 A", resource_count: 0, cleanup },
      { key: "vpc-empty-b", name: "空网络 B", resource_count: 0, cleanup },
      { key: "vpc-empty-c", name: "空网络 C", resource_count: 0, cleanup },
    ],
  };
  const onNavigate = vi.fn();
  const user = userEvent.setup();
  localStorage.setItem("steward.locale", "zh-CN");

  render(
    <LocaleProvider>
      <div style={{ width: 1200, height: 800 }}>
        <TopologySummaryView
          view={view}
          coverageStatus="complete"
          onNavigate={onNavigate}
        />
      </div>
    </LocaleProvider>,
  );
  await revealMeasuredSummaryNodes();

  expect(screen.getByRole("button", { name: "全局资源" })).toBeVisible();
  const collapsedStack = screen.getByRole("button", { name: "空 VPC · 3" });
  expect(collapsedStack.closest(".react-flow__node")).toHaveAttribute(
    "data-testid",
    "rf__node-empty-region",
  );
  expect(
    Array.from(collapsedStack.children).filter(
      (child) => child.tagName === "SPAN",
    ),
  ).toHaveLength(1);
  expect(collapsedStack.querySelector("[data-count-badge]")).toHaveTextContent(
    "3",
  );
  expect(
    collapsedStack.querySelector(
      '[data-summary-entry-icon="vpc"] [data-resource-icon-mask]',
    ),
  ).toHaveStyle({
    maskImage: `url(${VPC_ICON_SRC})`,
    WebkitMaskImage: `url(${VPC_ICON_SRC})`,
  });
  expect(within(collapsedStack).getByText("空 VPC")).toBeVisible();
  expect(
    screen.queryByRole("button", { name: /空网络 A/ }),
  ).not.toBeInTheDocument();

  collapsedStack.focus();
  await user.keyboard("{Enter}");
  await waitForSummaryFocusHandoff();
  const expandedVPCStack = screen.getByTestId("rf__node-empty-region");
  expect(
    expandedVPCStack.querySelector("[data-empty-stack-expanded]"),
  ).toHaveClass("rounded-lg", "border-l-2", "bg-muted/[0.12]");
  expect(
    expandedVPCStack.querySelector("[data-empty-stack-expanded]"),
  ).not.toHaveClass("border", "bg-background/50");
  const expandedHeader = expandedVPCStack.querySelector<HTMLElement>(
    "[data-empty-stack-header]",
  );
  expect(expandedHeader).not.toBeNull();
  expect(
    within(expandedHeader as HTMLElement).getByText("空 VPC"),
  ).toBeVisible();
  expect(expandedHeader).toHaveClass("h-10", "px-3");
  expect(within(expandedHeader as HTMLElement).getByText("· 3")).toBeVisible();
  expect(
    expandedVPCStack.querySelector("[data-empty-stack-guide]"),
  ).not.toBeInTheDocument();
  const collapseButton = screen.getByRole("button", { name: "收缩" });
  expect(collapseButton).toBeVisible();
  expect(collapseButton).toHaveFocus();
  expect(
    within(expandedVPCStack).queryByText("0 个资源"),
  ).not.toBeInTheDocument();
  const emptyVPC = screen.getByRole("button", { name: /空网络 A/ });
  expect(emptyVPC).toHaveClass(
    "rounded-lg",
    "border-border",
    "bg-background/95",
    "shadow-sm",
  );
  await user.click(emptyVPC);
  expect(onNavigate.mock.calls[0]?.[0]).toBe(view.vpcs[1]);

  await user.click(collapseButton);
  await waitForSummaryFocusHandoff();
  expect(screen.getByRole("button", { name: "空 VPC · 3" })).toHaveFocus();
});

it("removes cleanup actions from every summary frame while preserving navigation", async () => {
  const selectableScope = {
    selectable: true,
    selector_kind: "scope" as const,
    selector_key: "scope-a",
    potential_blockers: 0,
  };
  const view: AccountTopologyView = {
    kind: "account",
    global_resources: {
      key: "account-global",
      name: "账号全局资源",
      resource_count: 2,
      cleanup: selectableScope,
    },
    regions: [
      {
        key: "region-selectable",
        name: "可清理地域",
        resource_count: 1,
        cleanup: selectableScope,
      },
      {
        key: "region-missing-key",
        name: "缺少选择器",
        resource_count: 1,
        cleanup: {
          selectable: true,
          selector_kind: "scope",
          selector_key: " ",
          potential_blockers: 0,
        },
      },
      {
        key: "region-empty-a",
        name: "华北2（北京）",
        resource_count: 0,
        cleanup: selectableScope,
      },
      {
        key: "region-empty-b",
        name: "华南1（深圳）",
        resource_count: 0,
        cleanup: selectableScope,
      },
    ],
  };
  const onNavigate = vi.fn();
  const user = userEvent.setup();

  render(
    <LocaleProvider>
      <div style={{ width: 1200, height: 800 }}>
        <TopologySummaryView
          view={view}
          coverageStatus="complete"
          onNavigate={onNavigate}
        />
      </div>
    </LocaleProvider>,
  );
  await revealMeasuredSummaryNodes();

  const navigateGlobal = screen.getByRole("button", {
    name: /Global resources.*2 resources/,
  });
  expect(navigateGlobal).toBeVisible();
  expect(
    screen.queryByRole("button", { name: "Clean up Global resources" }),
  ).not.toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: "Clean up 可清理地域" }),
  ).not.toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: "Clean up 缺少选择器" }),
  ).not.toBeInTheDocument();
  expect(document.querySelector("button button")).toBeNull();

  await user.click(navigateGlobal);
  expect(onNavigate).toHaveBeenCalledWith(view.global_resources);

  await user.click(screen.getByRole("button", { name: "Empty Regions · 2" }));
  expect(
    screen.queryByRole("button", {
      name: "Clean up 华北2（北京）",
    }),
  ).not.toBeInTheDocument();
  expect(
    screen.queryByRole("button", {
      name: "Clean up 华南1（深圳）",
    }),
  ).not.toBeInTheDocument();
  expect(onNavigate).toHaveBeenCalledOnce();
});

it("keeps account-global drill-down-only while Region cards expose scope cleanup actions", async () => {
  const view: AccountTopologyView = {
    kind: "account",
    global_resources: {
      key: "account-global",
      name: "账号全局资源",
      resource_count: 2,
      cleanup: {
        selectable: true,
        selector_kind: "group",
        selector_key: "account-global-selector",
        potential_blockers: 0,
      },
    },
    regions: [
      {
        key: "region-a",
        name: "华东1（杭州）",
        native_id: "cn-hangzhou",
        resource_count: 4,
        cleanup: {
          selectable: false,
          selector_kind: "scope",
          selector_key: "scope-region-a",
          potential_blockers: 0,
        },
      },
    ],
  };
  const onNavigate = vi.fn();
  const user = userEvent.setup();

  render(
    <LocaleProvider>
      <div style={{ width: 1200, height: 800 }}>
        <TopologySummaryView
          view={view}
          coverageStatus="complete"
          onNavigate={onNavigate}
        />
      </div>
    </LocaleProvider>,
  );
  await revealMeasuredSummaryNodes();

  const globalEntry = screen.getByRole("button", {
    name: /Global resources.*2 resources/,
  });
  const regionEntry = screen.getByRole("button", {
    name: /华东1（杭州）.*4 resources/,
  });

  fireEvent.contextMenu(globalEntry);
  expect(screen.queryByText("Add to resource list")).not.toBeInTheDocument();
  await user.click(globalEntry);
  expect(onNavigate).toHaveBeenCalledWith(view.global_resources);

  fireEvent.contextMenu(regionEntry);
  expect(await screen.findByText("View details")).toBeInTheDocument();
  expect(onNavigate).toHaveBeenCalledOnce();
  await user.click(screen.getByText("Add to resource list"));

  expect(cleanupSelection.addTargets).toHaveBeenCalledWith([
    expect.objectContaining({
      key: "region-a",
      kind: "region",
      connectionId: "connection-a",
      ancestryKeys: ["region-a"],
      selector: expect.objectContaining({
        kind: "scope",
        connection_id: "connection-a",
        scope_id: "scope-region-a",
        descendants: true,
      }),
    }),
  ]);
});

it("offers regional public resources as a group target and VPCs as scoped targets", async () => {
  const view: RegionTopologyView = {
    kind: "region",
    region: {
      key: "region-a",
      name: "华东1（杭州）",
      native_id: "cn-hangzhou",
    },
    public_resources: {
      key: "region-public-a",
      name: "Public resources",
      resource_count: 7,
      cleanup: {
        selectable: false,
        selector_kind: "group",
        selector_key: "region-public-selector",
        potential_blockers: 0,
      },
    },
    vpcs: [
      {
        key: "vpc-a",
        name: "生产网络",
        native_id: "vpc-production",
        resource_count: 8,
        cleanup: {
          selectable: false,
          selector_kind: "group",
          selector_key: "vpc-selector-a",
          potential_blockers: 0,
        },
      },
    ],
  };

  render(
    <LocaleProvider>
      <div style={{ width: 1200, height: 800 }}>
        <TopologySummaryView
          view={view}
          coverageStatus="complete"
          onNavigate={vi.fn()}
        />
      </div>
    </LocaleProvider>,
  );
  await revealMeasuredSummaryNodes();

  fireEvent.contextMenu(
    screen.getByRole("button", { name: /Global resources.*7 resources/ }),
  );
  await userEvent.click(await screen.findByText("Add to resource list"));
  expect(cleanupSelection.addTargets).toHaveBeenLastCalledWith([
    expect.objectContaining({
      key: "region-public-a",
      kind: "vpc",
      ancestryKeys: ["region-a", "region-public-a"],
      locationContext: {
        region: {
          key: "region-a",
          name: "华东1（杭州）",
          native_id: "cn-hangzhou",
        },
      },
      selector: expect.objectContaining({
        kind: "group",
        connection_id: "connection-a",
        group_key: "region-public-selector",
      }),
    }),
  ]);

  fireEvent.contextMenu(
    screen.getByRole("button", { name: /生产网络.*8 resources/ }),
  );
  await userEvent.click(await screen.findByText("Add to resource list"));
  expect(cleanupSelection.addTargets).toHaveBeenLastCalledWith([
    expect.objectContaining({
      key: "vpc-a",
      kind: "vpc",
      ancestryKeys: ["region-a", "vpc-a"],
      locationContext: {
        region: {
          key: "region-a",
          name: "华东1（杭州）",
          native_id: "cn-hangzhou",
        },
      },
      selector: expect.objectContaining({
        kind: "group",
        group_key: "vpc-selector-a",
      }),
    }),
  ]);
});

it("opens combined VPC details alongside cleanup actions", async () => {
  const view: RegionTopologyView = {
    kind: "region",
    region: {
      key: "region-a",
      name: "华东1（杭州）",
      native_id: "cn-hangzhou",
    },
    public_resources: {
      key: "region-public-a",
      name: "Public resources",
      resource_count: 0,
      cleanup,
    },
    vpcs: [
      {
        key: "vpc-a",
        asset_id: "asset-vpc-a",
        name: "compute-sandbox-vpc",
        native_id: "vpc-2zeutleg8gepzfs01xlyrk",
        resource_count: 4,
        cleanup,
      },
    ],
  };
  const asset: Asset = {
    id: "asset-vpc-a",
    identity: {
      provider: "alicloud",
      partition: "aliyun",
      connection_id: "connection-a",
      native_type: "ACS::VPC::VPC",
      native_id: "vpc-2zeutleg8gepzfs01xlyrk",
    },
    scope_id: "cn-hangzhou",
    resource_kind_id: "alicloud:ACS::VPC::VPC",
    name: "compute-sandbox-vpc",
    location: "cn-hangzhou",
    capabilities: ["indexed"],
    normalized: {
      cidrBlock: "10.0.0.0/16",
    },
    first_seen_at: "2026-07-31T00:00:00Z",
    last_seen_at: "2026-07-31T01:00:00Z",
  };
  const catalog: ProviderCatalogBundle[] = [
    {
      provider: "alicloud",
      revision: "compiled-1",
      hash: "hash-1",
      kinds: [
        {
          id: asset.resource_kind_id,
          provider: "alicloud",
          native_type: asset.identity.native_type,
          capabilities: ["indexed"],
          display_name: "VPC",
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
  vi.mocked(findAsset).mockResolvedValue(asset);
  vi.mocked(listProviderCatalog).mockResolvedValue(catalog);
  const onNavigate = vi.fn();
  const user = userEvent.setup();

  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  testingRender(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <TooltipProvider>
          <LocaleProvider>
            <div style={{ width: 1200, height: 800 }}>
              <TopologySummaryView
                view={view}
                coverageStatus="complete"
                resourceKinds={consoleResourceKinds}
                onNavigate={onNavigate}
              />
            </div>
          </LocaleProvider>
        </TooltipProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  await revealMeasuredSummaryNodes();

  const vpc = screen.getByRole("button", {
    name: /compute-sandbox-vpc.*4 resources/,
  });
  fireEvent.contextMenu(vpc);
  expect(
    await screen.findByRole("menuitem", { name: "View details" }),
  ).toBeVisible();
  expect(
    screen.getByRole("menuitem", { name: "Add to resource list" }),
  ).toBeVisible();
  expect(
    screen.getByRole("menuitem", { name: "Cloud console" }),
  ).toHaveAttribute(
    "href",
    "https://vpc.console.aliyun.com/vpc/cn-hangzhou/vpcs/vpc-2zeutleg8gepzfs01xlyrk",
  );

  await user.click(screen.getByRole("menuitem", { name: "View details" }));
  const dialog = await screen.findByRole("dialog");
  expect(within(dialog).getByText("compute-sandbox-vpc")).toBeVisible();
  expect(within(dialog).getByText("华东1（杭州）")).toBeVisible();
  expect(within(dialog).queryByText("4")).not.toBeInTheDocument();
  expect(await within(dialog).findByText("CIDR block")).toBeVisible();
  expect(within(dialog).getByText("10.0.0.0/16")).toBeVisible();

  await user.click(within(dialog).getByRole("button", { name: "Close" }));
  fireEvent.click(vpc, { shiftKey: true });
  expect(onNavigate).not.toHaveBeenCalled();
  expect(vpc).toHaveAttribute("aria-pressed", "true");
  fireEvent.click(vpc, { shiftKey: true });
  expect(vpc).toHaveAttribute("aria-pressed", "false");
  await user.click(vpc);
  expect(onNavigate).toHaveBeenCalledWith(view.vpcs[0]);
});

it("uses the reduced toolbar and reserves Shift click for scope selection", async () => {
  const view: AccountTopologyView = {
    kind: "account",
    global_resources: {
      key: "account-global",
      name: "账号全局资源",
      resource_count: 2,
      cleanup,
    },
    regions: [
      {
        key: "region-a",
        name: "华东1（杭州）",
        resource_count: 4,
        cleanup,
      },
    ],
  };
  const onNavigate = vi.fn();
  const user = userEvent.setup();

  const rendered = render(
    <LocaleProvider>
      <div style={{ width: 1200, height: 800 }}>
        <TopologySummaryView
          view={view}
          coverageStatus="complete"
          onNavigate={onNavigate}
        />
      </div>
    </LocaleProvider>,
  );
  await revealMeasuredSummaryNodes();

  const toolbar = screen.getByRole("toolbar", { name: "Canvas tools" });
  expect(within(toolbar).getAllByRole("button")).toHaveLength(5);
  expect(within(toolbar).getAllByRole("separator")).toHaveLength(1);
  expect(screen.queryByTestId("rf__controls")).not.toBeInTheDocument();
  for (const name of ["Zoom out", "Fit view", "Zoom in"]) {
    expect(within(toolbar).getByRole("button", { name })).toBeInTheDocument();
  }
  expect(
    within(toolbar).getByRole("button", { name: "Select" }),
  ).toHaveAttribute("aria-pressed", "true");
  expect(
    within(toolbar).queryByRole("button", { name: "Box select" }),
  ).not.toBeInTheDocument();
  expect(within(toolbar).getByRole("button", { name: "Pan" })).toHaveAttribute(
    "aria-pressed",
    "false",
  );

  const viewport = document.querySelector<HTMLElement>(".react-flow__viewport");
  expect(viewport).not.toBeNull();
  const initialTransform = viewport!.style.transform;
  const zoomLevel =
    within(toolbar).getByLabelText<HTMLInputElement>("Zoom level");
  const initialZoomLevel = zoomLevel.value;
  expect(initialZoomLevel).toMatch(/^\d/);
  await user.click(within(toolbar).getByRole("button", { name: "Zoom in" }));
  await waitFor(() =>
    expect(viewport!.style.transform).not.toBe(initialTransform),
  );
  await waitFor(() => expect(zoomLevel.value).not.toBe(initialZoomLevel));
  await user.click(within(toolbar).getByRole("button", { name: "Fit view" }));
  await user.click(within(toolbar).getByRole("button", { name: "Zoom out" }));

  const regionEntry = screen.getByRole("button", {
    name: /华东1（杭州）.*4 resources/,
  });
  await user.click(within(toolbar).getByRole("button", { name: "Pan" }));
  await user.click(regionEntry);
  expect(onNavigate).not.toHaveBeenCalled();
  await user.click(within(toolbar).getByRole("button", { name: "Select" }));

  fireEvent.pointerDown(regionEntry, {
    button: 0,
    pointerId: 9,
    clientX: 100,
    clientY: 120,
  });
  fireEvent.pointerMove(regionEntry, {
    button: 0,
    pointerId: 9,
    clientX: 132,
    clientY: 148,
  });
  expect(regionEntry.closest("[data-panorama-canvas]")).toHaveAttribute(
    "data-panning",
    "true",
  );
  fireEvent.pointerUp(regionEntry, {
    button: 0,
    pointerId: 9,
    clientX: 132,
    clientY: 148,
  });
  fireEvent.click(regionEntry);
  expect(onNavigate).not.toHaveBeenCalled();

  await user.click(regionEntry);
  expect(onNavigate).toHaveBeenCalledOnce();
  expect(regionEntry.closest(".react-flow__node")).toHaveClass("selectable");
  expect(
    screen
      .getByRole("button", { name: /Global resources.*2 resources/ })
      .closest(".react-flow__node"),
  ).not.toHaveClass("selectable");
  fireEvent.click(regionEntry, { shiftKey: true });
  expect(onNavigate).toHaveBeenCalledOnce();
  expect(regionEntry).toHaveAttribute("aria-pressed", "true");
  expect(
    screen.getByRole("toolbar", { name: "Box selection actions" }),
  ).toHaveTextContent("1 selected");

  cleanupSelection.targets = [
    {
      key: "region-a",
      kind: "region",
      connectionId: "connection-a",
      displayName: "华东1（杭州）",
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
  rendered.rerender(
    <TooltipProvider>
      <LocaleProvider>
        <div style={{ width: 1200, height: 800 }}>
          <TopologySummaryView
            view={view}
            coverageStatus="complete"
            onNavigate={onNavigate}
          />
        </div>
      </LocaleProvider>
    </TooltipProvider>,
  );
  await revealMeasuredSummaryNodes();
  fireEvent.click(regionEntry, { shiftKey: true });
  expect(regionEntry).toHaveAttribute("aria-pressed", "false");
  expect(
    screen.queryByRole("toolbar", { name: "Box selection actions" }),
  ).not.toBeInTheDocument();
});

it("keeps selected pending scope targets blue with separate amber labels", async () => {
  cleanupSelection.targets = [
    {
      key: "vpc-a",
      kind: "vpc",
      connectionId: "connection-a",
      displayName: "生产网络",
      selector: {
        kind: "group",
        connection_id: "connection-a",
        group_key: "vpc-a",
      },
      ancestryKeys: ["region-a", "vpc-a"],
    },
    {
      key: "vpc-empty-a",
      kind: "vpc",
      connectionId: "connection-a",
      displayName: "空网络 A",
      selector: {
        kind: "group",
        connection_id: "connection-a",
        group_key: "vpc-empty-a",
      },
      ancestryKeys: ["region-a", "vpc-empty-a"],
    },
  ];
  const view: RegionTopologyView = {
    kind: "region",
    region: { key: "region-a", name: "华东1（杭州）" },
    public_resources: {
      key: "region-public-a",
      name: "Public resources",
      resource_count: 0,
      cleanup,
    },
    vpcs: [
      { key: "vpc-a", name: "生产网络", resource_count: 8, cleanup },
      { key: "vpc-empty-a", name: "空网络 A", resource_count: 0, cleanup },
      { key: "vpc-empty-b", name: "空网络 B", resource_count: 0, cleanup },
      { key: "vpc-empty-c", name: "空网络 C", resource_count: 0, cleanup },
    ],
  };
  localStorage.setItem("steward.locale", "zh-CN");

  render(
    <LocaleProvider>
      <div style={{ width: 1200, height: 800 }}>
        <TopologySummaryView
          view={view}
          coverageStatus="complete"
          onNavigate={vi.fn()}
        />
      </div>
    </LocaleProvider>,
  );
  await revealMeasuredSummaryNodes();

  const directPending = screen.getByRole("button", {
    name: /生产网络.*待清理/,
  });
  expect(directPending).toHaveAttribute("data-cleanup-pending", "true");
  expect(directPending).toHaveClass("bg-warning/5");
  expect(directPending).not.toHaveClass("ring-warning");
  expect(
    directPending.querySelector("[data-pending-cleanup-label]"),
  ).toHaveTextContent("待清理");

  fireEvent.click(directPending, { shiftKey: true });
  expect(directPending).toHaveAttribute("aria-pressed", "true");
  expect(directPending).toHaveClass("ring-2", "ring-ring", "bg-primary/5");
  expect(directPending).not.toHaveClass("bg-warning/5", "ring-warning");
  expect(
    directPending.querySelector("[data-pending-cleanup-label]"),
  ).toHaveTextContent("待清理");
  expect(
    directPending.querySelector("[data-selection-indicator]"),
  ).toBeVisible();

  const emptyStack = screen.getByRole("button", {
    name: /空 VPC · 3.*待清理 1\/3/,
  });
  expect(emptyStack).toHaveAttribute("data-cleanup-pending", "true");
  expect(within(emptyStack).getByText("待清理 1/3")).toBeVisible();
});

it("disables inherited removal and removes a directly selected VPC without drilling down", async () => {
  const view: RegionTopologyView = {
    kind: "region",
    region: { key: "region-a", name: "华东1（杭州）" },
    public_resources: {
      key: "region-public-a",
      name: "Public resources",
      resource_count: 0,
      cleanup,
    },
    vpcs: [
      {
        key: "vpc-a",
        name: "生产网络",
        native_id: "vpc-production",
        resource_count: 8,
        cleanup,
      },
    ],
  };
  const onNavigate = vi.fn();
  cleanupSelection.targets = [
    {
      key: "region-a",
      kind: "region",
      connectionId: "connection-a",
      displayName: "华东1（杭州）",
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
  const rendered = render(
    <LocaleProvider>
      <div style={{ width: 1200, height: 800 }}>
        <TopologySummaryView
          view={view}
          coverageStatus="complete"
          onNavigate={onNavigate}
        />
      </div>
    </LocaleProvider>,
  );
  await revealMeasuredSummaryNodes();

  const vpc = screen.getByRole("button", { name: /生产网络.*8 resources/ });
  fireEvent.contextMenu(vpc);
  expect(
    await screen.findByText("Included by broader cleanup selection"),
  ).toHaveAttribute("data-disabled");
  expect(
    screen.queryByText("Remove from resource list"),
  ).not.toBeInTheDocument();
  expect(cleanupSelection.removeTarget).not.toHaveBeenCalled();
  expect(onNavigate).not.toHaveBeenCalled();
  fireEvent.keyDown(document, { key: "Escape" });
  await waitFor(() =>
    expect(
      screen.queryByText("Included by broader cleanup selection"),
    ).not.toBeInTheDocument(),
  );

  cleanupSelection.targets = [
    {
      key: "vpc-a",
      kind: "vpc",
      connectionId: "connection-a",
      displayName: "生产网络",
      selector: {
        kind: "group",
        connection_id: "connection-a",
        group_key: "vpc-a",
        display_name: "生产网络",
      },
      ancestryKeys: ["region-a", "vpc-a"],
    },
  ];
  rendered.rerender(
    <TooltipProvider>
      <LocaleProvider>
        <div style={{ width: 1200, height: 800 }}>
          <TopologySummaryView
            view={view}
            coverageStatus="complete"
            onNavigate={onNavigate}
          />
        </div>
      </LocaleProvider>
    </TooltipProvider>,
  );
  await revealMeasuredSummaryNodes();

  fireEvent.contextMenu(
    screen
      .getByTestId("rf__node-vpc-a")
      .querySelector<HTMLButtonElement>("[data-cleanup-pending]")!,
  );
  await userEvent.click(await screen.findByText("Remove from resource list"));
  expect(cleanupSelection.removeTarget).toHaveBeenCalledWith("vpc-a");
  expect(onNavigate).not.toHaveBeenCalled();
});

it("removes every selected VPC through one context action", async () => {
  const view: RegionTopologyView = {
    kind: "region",
    region: { key: "region-a", name: "华东1（杭州）" },
    public_resources: {
      key: "region-public-a",
      name: "Public resources",
      resource_count: 0,
      cleanup,
    },
    vpcs: [
      { key: "vpc-a", name: "生产网络", resource_count: 8, cleanup },
      { key: "vpc-b", name: "测试网络", resource_count: 4, cleanup },
    ],
  };
  cleanupSelection.targets = view.vpcs.map((entry): CleanupTarget => ({
    key: entry.key,
    kind: "vpc",
    connectionId: "connection-a",
    displayName: entry.name,
    selector: {
      kind: "group",
      connection_id: "connection-a",
      group_key: entry.key,
    },
    ancestryKeys: ["region-a", entry.key],
  }));
  render(
    <LocaleProvider>
      <div style={{ width: 1200, height: 800 }}>
        <TopologySummaryView
          view={view}
          coverageStatus="complete"
          onNavigate={vi.fn()}
        />
      </div>
    </LocaleProvider>,
  );
  await revealMeasuredSummaryNodes();

  const production = screen.getByRole("button", {
    name: /生产网络.*8 resources/,
  });
  const test = screen.getByRole("button", {
    name: /测试网络.*4 resources/,
  });
  fireEvent.click(production, { shiftKey: true });
  fireEvent.click(test, { shiftKey: true });
  fireEvent.contextMenu(test);
  await userEvent.click(await screen.findByText("Remove from resource list"));

  expect(cleanupSelection.removeTarget).toHaveBeenCalledTimes(2);
  expect(cleanupSelection.removeTarget).toHaveBeenNthCalledWith(1, "vpc-a");
  expect(cleanupSelection.removeTarget).toHaveBeenNthCalledWith(2, "vpc-b");
});

it("removes every directly selected collapsed empty scope but preserves inherited members", async () => {
  const view: RegionTopologyView = {
    kind: "region",
    region: { key: "region-a", name: "华东1（杭州）" },
    public_resources: {
      key: "region-public-a",
      name: "Public resources",
      resource_count: 1,
      cleanup,
    },
    vpcs: [
      { key: "vpc-empty-a", name: "空网络 A", resource_count: 0, cleanup },
      { key: "vpc-empty-b", name: "空网络 B", resource_count: 0, cleanup },
    ],
  };
  const onNavigate = vi.fn();
  cleanupSelection.targets = [
    {
      key: "vpc-empty-a",
      kind: "vpc",
      connectionId: "connection-a",
      displayName: "空网络 A",
      selector: {
        kind: "group",
        connection_id: "connection-a",
        group_key: "vpc-empty-a",
      },
      ancestryKeys: ["region-a", "vpc-empty-a"],
    },
    {
      key: "vpc-empty-b",
      kind: "vpc",
      connectionId: "connection-a",
      displayName: "空网络 B",
      selector: {
        kind: "group",
        connection_id: "connection-a",
        group_key: "vpc-empty-b",
      },
      ancestryKeys: ["region-a", "vpc-empty-b"],
    },
  ];
  const rendered = render(
    <LocaleProvider>
      <div style={{ width: 1200, height: 800 }}>
        <TopologySummaryView
          view={view}
          coverageStatus="complete"
          onNavigate={onNavigate}
        />
      </div>
    </LocaleProvider>,
  );
  await revealMeasuredSummaryNodes();

  fireEvent.contextMenu(
    screen.getByRole("button", { name: /Empty VPCs · 2.*Pending cleanup/ }),
  );
  await userEvent.click(
    await screen.findByText("Remove all from resource list"),
  );
  expect(cleanupSelection.removeTarget).toHaveBeenNthCalledWith(
    1,
    "vpc-empty-a",
  );
  expect(cleanupSelection.removeTarget).toHaveBeenNthCalledWith(
    2,
    "vpc-empty-b",
  );
  expect(onNavigate).not.toHaveBeenCalled();

  cleanupSelection.removeTarget.mockClear();
  cleanupSelection.targets = [
    {
      key: "region-a",
      kind: "region",
      connectionId: "connection-a",
      displayName: "华东1（杭州）",
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
  rendered.rerender(
    <TooltipProvider>
      <LocaleProvider>
        <div style={{ width: 1200, height: 800 }}>
          <TopologySummaryView
            view={view}
            coverageStatus="complete"
            onNavigate={onNavigate}
          />
        </div>
      </LocaleProvider>
    </TooltipProvider>,
  );
  await revealMeasuredSummaryNodes();

  fireEvent.contextMenu(
    screen.getByRole("button", { name: /Empty VPCs · 2.*Pending cleanup/ }),
  );
  expect(
    await screen.findByText("Included by broader cleanup selection"),
  ).toHaveAttribute("data-disabled");
  expect(
    screen.queryByText("Remove all from resource list"),
  ).not.toBeInTheDocument();
  expect(cleanupSelection.removeTarget).not.toHaveBeenCalled();
  expect(onNavigate).not.toHaveBeenCalled();
});

it("lets an open scope detail dialog consume Escape before selection state", async () => {
  const view: AccountTopologyView = {
    kind: "account",
    regions: [
      { key: "region-a", name: "华东1（杭州）", resource_count: 4, cleanup },
      { key: "region-b", name: "华北2（北京）", resource_count: 3, cleanup },
    ],
  };
  const user = userEvent.setup();

  render(
    <LocaleProvider>
      <div style={{ width: 1200, height: 800 }}>
        <TopologySummaryView
          view={view}
          coverageStatus="complete"
          onNavigate={vi.fn()}
        />
      </div>
    </LocaleProvider>,
  );
  await revealMeasuredSummaryNodes();

  fireEvent.click(
    screen.getByRole("button", { name: /华东1（杭州）.*4 resources/ }),
    { shiftKey: true },
  );
  fireEvent.click(
    screen.getByRole("button", { name: /华北2（北京）.*3 resources/ }),
    { shiftKey: true },
  );
  expect(
    await screen.findByRole("toolbar", { name: "Box selection actions" }),
  ).toHaveTextContent("2 selected");
  fireEvent.contextMenu(
    screen.getByRole("button", { name: /华东1（杭州）.*4 resources/ }),
  );
  await user.click(
    await screen.findByRole("menuitem", { name: "View details" }),
  );
  expect(screen.getByRole("dialog")).toBeInTheDocument();

  fireEvent.keyDown(document, { key: "Escape" });
  await waitFor(() =>
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
  );
  expect(
    screen.getByRole("toolbar", { name: "Box selection actions" }),
  ).toHaveTextContent("2 selected");

  fireEvent.keyDown(document, { key: "Escape" });
  expect(
    screen.queryByRole("toolbar", { name: "Box selection actions" }),
  ).not.toBeInTheDocument();
});

it("adds every collapsed empty scope after box selection and supports Shift append", async () => {
  const view: AccountTopologyView = {
    kind: "account",
    regions: [
      { key: "region-a", name: "华东1（杭州）", resource_count: 4, cleanup },
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
    ],
  };
  const user = userEvent.setup();

  const rendered = render(
    <LocaleProvider>
      <div style={{ width: 1200, height: 800 }}>
        <TopologySummaryView
          view={view}
          coverageStatus="complete"
          onNavigate={vi.fn()}
        />
      </div>
    </LocaleProvider>,
  );
  await revealMeasuredSummaryNodes();

  const pane = document.querySelector<HTMLElement>(".react-flow__pane");
  expect(pane).not.toBeNull();
  fireEvent.pointerDown(pane!, {
    button: 0,
    clientX: 0,
    clientY: 0,
    isPrimary: true,
    pointerId: 1,
  });
  fireEvent.pointerMove(pane!, {
    clientX: 530,
    clientY: 50,
    isPrimary: true,
    pointerId: 1,
  });
  fireEvent.pointerUp(pane!, {
    button: 0,
    clientX: 530,
    clientY: 50,
    isPrimary: true,
    pointerId: 1,
  });

  expect(
    await screen.findByRole("toolbar", { name: "Box selection actions" }),
  ).toHaveTextContent("3 selected");

  const regionView: RegionTopologyView = {
    kind: "region",
    region: { key: "region-b", name: "华北2（北京）" },
    public_resources: {
      key: "region-public-b",
      name: "Public resources",
      resource_count: 2,
      cleanup,
    },
    vpcs: [
      {
        key: "vpc-b",
        name: "测试网络",
        resource_count: 3,
        cleanup,
      },
    ],
  };
  rendered.rerender(
    <TooltipProvider>
      <LocaleProvider>
        <div style={{ width: 1200, height: 800 }}>
          <TopologySummaryView
            view={regionView}
            coverageStatus="complete"
            onNavigate={vi.fn()}
          />
        </div>
      </LocaleProvider>
    </TooltipProvider>,
  );
  await revealMeasuredSummaryNodes();
  const regionPane = document.querySelector<HTMLElement>(".react-flow__pane");
  expect(regionPane).not.toBeNull();

  fireEvent.pointerDown(regionPane!, {
    button: 0,
    clientX: 0,
    clientY: 0,
    isPrimary: true,
    pointerId: 2,
    shiftKey: true,
  });
  fireEvent.pointerMove(regionPane!, {
    clientX: 530,
    clientY: 100,
    isPrimary: true,
    pointerId: 2,
    shiftKey: true,
  });
  fireEvent.pointerUp(regionPane!, {
    button: 0,
    clientX: 530,
    clientY: 100,
    isPrimary: true,
    pointerId: 2,
    shiftKey: true,
  });

  expect(
    await screen.findByRole("toolbar", { name: "Box selection actions" }),
  ).toHaveTextContent("5 selected");
  await user.click(screen.getByRole("button", { name: "Add selected" }));

  expect(cleanupSelection.addTargets).toHaveBeenCalledWith([
    expect.objectContaining({ key: "region-a", kind: "region" }),
    expect.objectContaining({ key: "region-empty-a", kind: "region" }),
    expect.objectContaining({ key: "region-empty-b", kind: "region" }),
    expect.objectContaining({ key: "region-public-b", kind: "vpc" }),
    expect.objectContaining({ key: "vpc-b", kind: "vpc" }),
  ]);
});
