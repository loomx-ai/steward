import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import {
  listAssets,
  listConnectionRegions,
  listProviderCatalog,
  setAssetDirty,
} from "@/api/client";
import type { Asset, ProviderCatalogBundle } from "@/api/types";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import type { CleanupTarget } from "../panorama/cleanupSelection";
import { AssetsView, filterAssets } from "./AssetsView";

const cleanupSelectionMock = vi.hoisted(() => ({
  targets: [] as import("../panorama/cleanupSelection").CleanupTarget[],
  addTargets: vi.fn(),
  removeTarget: vi.fn(),
  removeBatchMember: vi.fn(),
  clearTargets: vi.fn(),
}));

vi.mock("@/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/client")>()),
  listAssets: vi.fn(),
  listConnectionRegions: vi.fn(),
  listProviderCatalog: vi.fn(),
  setAssetDirty: vi.fn(),
}));

vi.mock("@/connections/ActiveConnectionProvider", () => ({
  useRequiredConnection: () => ({
    id: "conn-1",
    name: "Production",
    provider: "alicloud",
  }),
}));

vi.mock("@/features/panorama/CleanupSelectionContext", () => ({
  useCleanupSelection: () => cleanupSelectionMock,
}));

const assets: Asset[] = [
  {
    id: "a-1",
    identity: {
      provider: "alicloud",
      partition: "aliyun",
      connection_id: "conn-1",
      native_type: "ALIYUN::ECS::Instance",
      native_id: "i-123",
    },
    scope_id: "cn-hangzhou",
    resource_kind_id: "ecs-instance",
    name: "production-api",
    state: "running",
    location: "cn-hangzhou-h",
    capabilities: ["inventory", "cleanup"],
    first_seen_at: "2026-07-14T00:00:00Z",
    last_seen_at: "2026-07-14T01:00:00Z",
  },
  {
    id: "a-2",
    identity: {
      provider: "aws",
      partition: "aws",
      connection_id: "conn-1",
      native_type: "AWS::S3::Bucket",
      native_id: "archive-bucket",
    },
    scope_id: "global",
    resource_kind_id: "s3-bucket",
    name: "archive",
    capabilities: ["inventory"],
    first_seen_at: "2026-07-14T00:00:00Z",
    last_seen_at: "2026-07-14T01:00:00Z",
  },
];

const catalog: ProviderCatalogBundle[] = [
  {
    provider: "alicloud",
    specs: [],
    revision: "catalog-1",
    hash: "catalog-hash",
    kinds_revision: "kinds-1",
    kinds: [
      {
        id: "ecs-instance",
        provider: "alicloud",
        native_type: "ALIYUN::ECS::Instance",
        capabilities: ["inventory", "cleanup"],
        display_name: "ECS Instance",
        display_names: {
          "en-US": "ECS Instance",
          "zh-CN": "ECS 实例",
        },
        icon: "/icons/alicloud/acs-ecs-instance.svg",
        console_link_template:
          "https://ecs.console.aliyun.com/server/region/{regionId}?instanceId={nativeId}",
        properties: [
          {
            path: "instanceType",
            type: "string",
            display_names: {
              "en-US": "Instance type",
              "zh-CN": "实例规格",
            },
            enum: ["ecs.g8i.large", "ecs.g8i.xlarge"],
            operators: ["=", "!=", "in"],
          },
        ],
        bundle_revision: "catalog-1",
      },
      {
        id: "vpc",
        provider: "alicloud",
        native_type: "ALIYUN::VPC::VPC",
        capabilities: ["inventory"],
        display_name: "VPC",
        display_names: {
          "en-US": "VPC",
          "zh-CN": "专有网络",
        },
        bundle_revision: "catalog-1",
      },
    ],
  },
];

function resourceListTarget(assetID: string): CleanupTarget {
  return {
    key: `asset:${assetID}`,
    kind: "resource",
    connectionId: "conn-1",
    displayName: assetID,
    selector: {
      kind: "asset",
      asset_id: assetID,
      display_name: assetID,
    },
    ancestryKeys: [`asset:${assetID}`],
  };
}

function LocationProbe() {
  return <output data-testid="location">{useLocation().pathname}</output>;
}

beforeEach(() => {
  localStorage.setItem("steward.locale", "en-US");
  vi.stubGlobal(
    "ResizeObserver",
    class ResizeObserver {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  cleanupSelectionMock.targets = [];
  cleanupSelectionMock.addTargets.mockReset();
  cleanupSelectionMock.removeTarget.mockReset();
  cleanupSelectionMock.removeBatchMember.mockReset();
  cleanupSelectionMock.clearTargets.mockReset();
  vi.mocked(listAssets).mockResolvedValue({ items: assets });
  vi.mocked(listConnectionRegions).mockResolvedValue({
    items: [
      {
        id: "region-hangzhou",
        connection_id: "conn-1",
        region_id: "cn-hangzhou",
        name: "华东 1（杭州）",
        origin: "api",
        lifecycle: "active",
        created_at: "2026-07-01T00:00:00Z",
        updated_at: "2026-07-01T00:00:00Z",
      },
    ],
  });
  vi.mocked(listProviderCatalog).mockResolvedValue(catalog);
  vi.mocked(setAssetDirty)
    .mockReset()
    .mockImplementation(async (_connectionID, id, dirty) => ({
      ...assets.find((value) => value.id === id)!,
      dirty,
    }));
});

it("filters resources by query, provider, and capability", () => {
  expect(
    filterAssets(assets, {
      search: "i-123",
      provider: "alicloud",
      capability: "cleanup",
      resourceKindIDs: ["ecs-instance", "vpc"],
    }).map((asset) => asset.id),
  ).toEqual(["a-1"]);
  expect(
    filterAssets(assets, {
      search: "archive",
      provider: "__all__",
      capability: "__all__",
      resourceKindIDs: [],
    }).map((asset) => asset.id),
  ).toEqual(["a-2"]);
});

it("refreshes resources on entry even when the cached list is fresh", async () => {
  vi.mocked(listAssets).mockResolvedValueOnce({ items: [] });
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false, staleTime: Number.POSITIVE_INFINITY },
    },
  });
  queryClient.setQueryData(["assets", "conn-1", "", [], "", 20], {
    items: assets,
  });

  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <LocaleProvider>
          <AssetsView />
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  await waitFor(() => expect(listAssets).toHaveBeenCalled());
  await waitFor(() =>
    expect(screen.queryByText("production-api")).not.toBeInTheDocument(),
  );
});

it("marks a dirty resource from the resource list", async () => {
  const user = userEvent.setup();
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <LocaleProvider>
          <AssetsView />
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  await user.click(
    await screen.findByRole("button", {
      name: "Mark as dirty resource: production-api",
    }),
  );
  await waitFor(() =>
    expect(setAssetDirty).toHaveBeenCalledWith("conn-1", "a-1", true),
  );
});

it("keeps browsing focused on search and resource details", async () => {
  const user = userEvent.setup();
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const { container } = render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <LocaleProvider>
          <AssetsView />
          <LocationProbe />
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  expect(screen.getByRole("textbox")).toHaveAttribute(
    "placeholder",
    "Search by name, resource ID, or type",
  );
  expect(document.querySelector('[data-slot="page-layout"]')).toHaveClass(
    "pt-0",
  );
  expect(
    await screen.findByRole("checkbox", {
      name: "Select all available resources on this page",
    }),
  ).toBeVisible();
  expect(
    await screen.findByRole("columnheader", { name: "Region" }),
  ).toBeVisible();
  expect(
    screen.queryByRole("columnheader", { name: "State" }),
  ).not.toBeInTheDocument();
  expect(screen.getByText("ECS Instance")).toBeVisible();
  expect(screen.getByText("华东 1（杭州）")).toBeVisible();
  expect(screen.queryByText("cn-hangzhou-h")).not.toBeInTheDocument();
  expect(
    container.querySelector("[data-resource-icon-mask]"),
  ).toBeInTheDocument();
  expect(screen.queryByText("2 on this page")).not.toBeInTheDocument();
  const resourceLink = screen.getByRole("link", { name: "production-api" });
  expect(resourceLink).toHaveAttribute("href", "/assets/a-1");
  expect(resourceLink).toHaveClass("text-info");
  expect(resourceLink.closest("tr")).not.toHaveClass("cursor-pointer");

  const panoramaLink = screen.getByRole("link", {
    name: "View in resource panorama: production-api",
  });
  expect(panoramaLink).toHaveAttribute("href", "/panorama?resource=a-1");
  await user.hover(panoramaLink);
  expect(await screen.findByRole("tooltip")).toHaveTextContent(
    "View in resource panorama",
  );
  await user.unhover(panoramaLink);

  const consoleLink = screen.getByRole("link", {
    name: "Cloud console: production-api",
  });
  expect(consoleLink).toHaveAttribute(
    "href",
    "https://ecs.console.aliyun.com/server/region/cn-hangzhou-h?instanceId=i-123",
  );
  expect(consoleLink).toHaveAttribute("target", "_blank");
  expect(consoleLink).toHaveAttribute("rel", "noopener noreferrer");
  expect(consoleLink).toHaveAttribute("data-slot", "tooltip-trigger");

  await user.click(screen.getByText("ECS Instance"));
  expect(screen.getByTestId("location")).toHaveTextContent("/");

  resourceLink.focus();
  await user.keyboard("{Enter}");
  expect(screen.getByTestId("location")).toHaveTextContent("/assets/a-1");
  expect(
    screen.queryByRole("link", {
      name: "Open resource details: production-api",
    }),
  ).not.toBeInTheDocument();
});

it("adds multiple selected resources to the shared resource list", async () => {
  const user = userEvent.setup();
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });

  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <LocaleProvider>
          <AssetsView />
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  expect(
    screen.queryByRole("button", { name: "Add to resource list" }),
  ).not.toBeInTheDocument();
  await user.click(
    await screen.findByRole("checkbox", {
      name: "Select all available resources on this page",
    }),
  );
  const addButton = screen.getByRole("button", {
    name: "Add to resource list",
  });
  expect(addButton).toHaveClass("ml-auto");
  expect(addButton).toHaveTextContent("(2)");
  expect(addButton).toBeEnabled();
  await user.click(addButton);

  expect(cleanupSelectionMock.addTargets).toHaveBeenCalledWith([
    expect.objectContaining({
      key: "asset:a-1",
      connectionId: "conn-1",
      selector: expect.objectContaining({ kind: "asset", asset_id: "a-1" }),
      ancestryKeys: expect.arrayContaining(["asset:a-1"]),
    }),
    expect.objectContaining({
      key: "asset:a-2",
      connectionId: "conn-1",
      selector: expect.objectContaining({ kind: "asset", asset_id: "a-2" }),
      ancestryKeys: expect.arrayContaining(["account-global", "asset:a-2"]),
    }),
  ]);
  expect(
    screen.queryByRole("button", { name: "Add to resource list" }),
  ).not.toBeInTheDocument();
});

it("removes selected resources when all of them are already listed", async () => {
  cleanupSelectionMock.targets = [
    resourceListTarget("a-1"),
    resourceListTarget("a-2"),
  ];
  const user = userEvent.setup();
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });

  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <LocaleProvider>
          <AssetsView />
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  await user.click(
    await screen.findByRole("checkbox", {
      name: "Select all available resources on this page",
    }),
  );
  const removeButton = screen.getByRole("button", {
    name: "Remove from resource list",
  });
  expect(removeButton).toHaveTextContent("(2)");
  await user.click(removeButton);

  expect(cleanupSelectionMock.removeTarget).toHaveBeenCalledWith("asset:a-1");
  expect(cleanupSelectionMock.removeTarget).toHaveBeenCalledWith("asset:a-2");
  expect(cleanupSelectionMock.addTargets).not.toHaveBeenCalled();
  expect(
    screen.queryByRole("button", { name: "Remove from resource list" }),
  ).not.toBeInTheDocument();
});

it("adds only missing resources when the selection mixes list states", async () => {
  cleanupSelectionMock.targets = [resourceListTarget("a-1")];
  const user = userEvent.setup();
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });

  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <LocaleProvider>
          <AssetsView />
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  await user.click(
    await screen.findByRole("checkbox", {
      name: "Select all available resources on this page",
    }),
  );
  const addButton = screen.getByRole("button", {
    name: "Add to resource list",
  });
  expect(addButton).toHaveTextContent("(1)");
  await user.click(addButton);

  expect(cleanupSelectionMock.addTargets).toHaveBeenCalledWith([
    expect.objectContaining({ key: "asset:a-2" }),
  ]);
  expect(cleanupSelectionMock.removeTarget).not.toHaveBeenCalled();
});

it("applies a typed resource query and completes properties from the selected type", async () => {
  vi.mocked(listAssets).mockImplementation(async (_connectionID, options) => ({
    items: options?.resourceQuery
      ? assets.filter((asset) => asset.resource_kind_id === "ecs-instance")
      : assets,
  }));
  const user = userEvent.setup();
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });

  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <LocaleProvider>
          <AssetsView />
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  const modeToggle = await screen.findByRole("button", {
    name: "Normal search · Switch to advanced query",
  });
  await user.hover(modeToggle);
  expect(await screen.findByRole("tooltip")).toHaveTextContent(
    "Normal search · Switch to advanced query",
  );
  await user.unhover(modeToggle);
  await user.click(modeToggle);
  expect(
    await screen.findByRole("option", { name: /resourceId/ }),
  ).toBeVisible();
  expect(screen.queryByRole("option", { name: /nativeId/ })).toBeNull();
  const editor = await screen.findByRole("combobox", {
    name: "Resource query editor",
  });
  await user.type(editor, "provider = ");
  await user.click(await screen.findByRole("option", { name: "alicloud" }));
  expect(editor).toHaveValue('provider = "alicloud" ');

  await user.clear(editor);
  await user.type(editor, "region = ");
  await user.click(await screen.findByRole("option", { name: /cn-hangzhou/ }));
  expect(editor).toHaveValue('region = "cn-hangzhou" ');

  await user.clear(editor);
  await user.type(editor, "state = ");
  await user.click(await screen.findByRole("option", { name: "running" }));
  expect(editor).toHaveValue('state = "running" ');

  await user.clear(editor);
  await user.type(editor, "type = ALIYUN::ECS::Inst");
  await user.click(
    await screen.findByRole("option", { name: /ALIYUN::ECS::Instance/ }),
  );
  expect(editor).toHaveValue('type = "ALIYUN::ECS::Instance" ');
  await user.type(editor, "AND prop");
  await user.click(
    await screen.findByRole("option", { name: /properties\.instanceType/ }),
  );
  await user.type(editor, "IN (");
  await user.click(
    await screen.findByRole("option", { name: "ecs.g8i.large" }),
  );
  await user.type(editor, ")");
  await user.keyboard("{Control>}{Enter}{/Control}");

  await waitFor(() =>
    expect(listAssets).toHaveBeenLastCalledWith(
      "conn-1",
      expect.objectContaining({
        resourceQuery:
          'type = "ALIYUN::ECS::Instance" AND properties.instanceType IN ("ecs.g8i.large")',
      }),
    ),
  );
  expect(
    await screen.findByRole("link", { name: "production-api" }),
  ).toBeVisible();
  expect(screen.queryByRole("link", { name: "archive" })).toBeNull();

  await user.click(
    screen.getByRole("button", {
      name: "Advanced query · Switch to normal search",
    }),
  );
  await waitFor(() =>
    expect(listAssets).toHaveBeenLastCalledWith(
      "conn-1",
      expect.objectContaining({ resourceQuery: undefined }),
    ),
  );
});

it("uses localized resource kind names and region labels", async () => {
  localStorage.setItem("steward.locale", "zh-CN");
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });

  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <LocaleProvider>
          <AssetsView />
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  expect(await screen.findByText("ECS 实例")).toBeVisible();
  expect(screen.getByRole("columnheader", { name: "地域" })).toBeVisible();
  expect(
    screen.getByRole("button", { name: "普通搜索 · 切换到高级查询" }),
  ).toBeVisible();
});
