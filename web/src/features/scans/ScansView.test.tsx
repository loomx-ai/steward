import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  createScan,
  listConnectionRegions,
  listProviderCatalog,
  listScans,
  searchNetworkTargets,
} from "@/api/client";
import type {
  CloudConnection,
  NetworkTargetOption,
  ProviderCatalogBundle,
  ScanTask,
} from "@/api/types";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import { canonicalNetworkTargets } from "./CreateScanDialog";
import { ScansView } from "./ScansView";

const connection = {
  id: "con-23456789abcdefgh",
  name: "Production",
  provider: "alicloud",
} as CloudConnection;
vi.mock("@/api/client", () => ({
  createScan: vi.fn(),
  listConnectionRegions: vi.fn(),
  listProviderCatalog: vi.fn(),
  listScans: vi.fn(),
  searchNetworkTargets: vi.fn(),
}));
vi.mock("@/connections/ActiveConnectionProvider", () => ({
  useRequiredConnection: () => connection,
}));

beforeEach(() => {
  localStorage.setItem("steward.locale", "en-US");
  vi.mocked(createScan).mockReset();
  vi.mocked(listProviderCatalog).mockReset().mockResolvedValue([]);
  vi.mocked(listConnectionRegions)
    .mockReset()
    .mockResolvedValue({
      items: [
        {
          id: "rgn-23456789abcdefgh",
          connection_id: connection.id,
          region_id: "cn-hangzhou",
          name: "China East 1",
          origin: "api",
          lifecycle: "active",
          created_at: "2026-07-21T00:00:00Z",
          updated_at: "2026-07-21T00:00:00Z",
        },
      ],
    });
  vi.mocked(listScans).mockReset().mockResolvedValue({ items: [] });
  vi.mocked(searchNetworkTargets).mockReset().mockResolvedValue({ items: [] });
  vi.stubGlobal(
    "ResizeObserver",
    class ResizeObserver {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  Object.defineProperty(Element.prototype, "scrollIntoView", {
    configurable: true,
    value: vi.fn(),
  });
  Object.defineProperties(Element.prototype, {
    hasPointerCapture: { configurable: true, value: vi.fn(() => false) },
    releasePointerCapture: { configurable: true, value: vi.fn() },
    setPointerCapture: { configurable: true, value: vi.fn() },
  });
});

afterEach(() => vi.unstubAllGlobals());

function renderView() {
  return render(
    <MemoryRouter>
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <LocaleProvider>
          <ScansView />
        </LocaleProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

function scanTask(id: string): ScanTask {
  return {
    id,
    connection_id: connection.id,
    status: "succeeded",
    scope_mode: "all_active_regions",
    requested_by: "alice",
    targets: [],
    retry_generation: 0,
    retry_count: 0,
    control_version: 0,
    created_at: "2026-07-21T00:00:00Z",
    target_progress: [],
    progress: {
      completed: 1,
      total: 1,
      running: 0,
      failed: 0,
      resource_count: 1,
    },
    allowed_actions: [],
  };
}

it("loads scan creation options only after opening the dialog", async () => {
  const user = userEvent.setup();
  renderView();

  await waitFor(() => expect(listScans).toHaveBeenCalledOnce());
  expect(listScans).toHaveBeenCalledWith(
    connection.id,
    "",
    20,
    expect.any(AbortSignal),
  );
  expect(listConnectionRegions).not.toHaveBeenCalled();
  expect(listProviderCatalog).not.toHaveBeenCalled();

  await user.click(screen.getByRole("button", { name: "Start scan" }));

  await waitFor(() => {
    expect(listConnectionRegions).toHaveBeenCalledWith(connection.id, {
      lifecycle: "active",
    });
    expect(listProviderCatalog).toHaveBeenCalledOnce();
  });
});

it("aborts an in-flight scan list request when leaving the view", async () => {
  let signal: AbortSignal | undefined;
  vi.mocked(listScans).mockImplementation(
    (_connectionID, _cursor, _limit, requestSignal) => {
      signal = requestSignal;
      return new Promise(() => undefined);
    },
  );
  const view = renderView();

  await waitFor(() => expect(signal).toBeDefined());
  expect(signal?.aborted).toBe(false);

  view.unmount();

  expect(signal?.aborted).toBe(true);
});

it("replaces the current scan page when navigating forward and back", async () => {
  vi.mocked(listScans).mockImplementation(async (_connectionID, cursor) =>
    cursor === "cursor-2"
      ? { items: [scanTask("scan-page-2")] }
      : { items: [scanTask("scan-page-1")], next_cursor: "cursor-2" },
  );
  const user = userEvent.setup();
  renderView();

  expect(await screen.findByText("scan-page-1")).toBeVisible();
  expect(screen.queryByText("Load more")).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Next" }));
  expect(await screen.findByText("scan-page-2")).toBeVisible();
  expect(screen.queryByText("scan-page-1")).not.toBeInTheDocument();

  await user.click(screen.getByRole("button", { name: "Previous" }));
  expect(await screen.findByText("scan-page-1")).toBeVisible();
  expect(listScans).toHaveBeenCalledWith(
    connection.id,
    "",
    20,
    expect.any(AbortSignal),
  );
  expect(listScans).toHaveBeenCalledWith(
    connection.id,
    "cursor-2",
    20,
    expect.any(AbortSignal),
  );
});

it("changes the scan page size and restarts at the first page", async () => {
  vi.mocked(listScans).mockImplementation(async (_connectionID, cursor) =>
    cursor === "cursor-2"
      ? { items: [scanTask("scan-page-2")] }
      : { items: [scanTask("scan-page-1")], next_cursor: "cursor-2" },
  );
  const user = userEvent.setup();
  renderView();

  expect(await screen.findByText("scan-page-1")).toBeVisible();
  await user.click(screen.getByRole("button", { name: "Next" }));
  expect(await screen.findByText("scan-page-2")).toBeVisible();

  await user.click(screen.getByRole("combobox", { name: "Items per page" }));
  await user.click(screen.getByRole("option", { name: "100" }));

  await waitFor(() =>
    expect(listScans).toHaveBeenLastCalledWith(
      connection.id,
      "",
      100,
      expect.any(AbortSignal),
    ),
  );
  expect(await screen.findByText("scan-page-1")).toBeVisible();
});

it("refreshes the current scan page from the toolbar", async () => {
  let finishRefresh = () => {};
  vi.mocked(listScans)
    .mockResolvedValueOnce({ items: [scanTask("scan-before-refresh")] })
    .mockImplementationOnce(
      () =>
        new Promise<Awaited<ReturnType<typeof listScans>>>((resolve) => {
          finishRefresh = () =>
            resolve({ items: [scanTask("scan-after-refresh")] });
        }),
    );
  const user = userEvent.setup();
  renderView();

  expect(await screen.findByText("scan-before-refresh")).toBeVisible();
  const refresh = screen.getByRole("button", { name: "Refresh scan list" });
  expect(refresh).toHaveAttribute("title", "Refresh scan list");

  await user.click(refresh);

  const refreshing = await screen.findByRole("button", {
    name: "Refreshing scan list…",
  });
  expect(refreshing).toBeDisabled();
  expect(refreshing).toHaveAttribute("aria-busy", "true");
  expect(refreshing.querySelector("svg")).toHaveClass(
    "motion-safe:animate-spin",
  );
  expect(listScans).toHaveBeenLastCalledWith(
    connection.id,
    "",
    20,
    expect.any(AbortSignal),
  );

  finishRefresh();

  expect(await screen.findByText("scan-after-refresh")).toBeVisible();
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Refresh scan list" }),
    ).toBeEnabled(),
  );
});

it("keeps search, status filtering, and actions in one list toolbar", async () => {
  vi.mocked(listScans).mockResolvedValue({
    items: [
      { ...scanTask("scan-running"), status: "running" },
      scanTask("scan-succeeded"),
    ],
  });
  const user = userEvent.setup();
  renderView();

  expect(await screen.findByText("scan-running")).toBeVisible();
  const search = screen.getByRole("textbox", {
    name: "Search task ID or requester",
  });
  const statusFilter = screen.getByRole("combobox", {
    name: "Filter by status",
  });
  const shell = search.closest('[data-slot="data-table-shell"]');
  const startScan = screen.getByRole("button", { name: "Start scan" });
  expect(statusFilter).toHaveClass("sm:w-40");
  expect(shell).toContainElement(statusFilter);
  expect(shell).toContainElement(
    screen.getByRole("button", { name: "Refresh scan list" }),
  );
  expect(shell).toContainElement(startScan);
  expect(startScan.parentElement).toHaveClass("ml-auto");
  expect(document.querySelector('[data-slot="page-layout"]')).toHaveClass(
    "pt-0",
  );

  await user.type(search, "scan-succeeded");
  expect(screen.queryByText("scan-running")).not.toBeInTheDocument();
  expect(screen.getByText("scan-succeeded")).toBeVisible();

  await user.clear(search);
  await user.click(statusFilter);
  await user.click(screen.getByRole("option", { name: "Running" }));
  expect(screen.getByText("scan-running")).toBeVisible();
  expect(screen.queryByText("scan-succeeded")).not.toBeInTheDocument();
});

it("presents reconciling scans as in progress in the list and status filter", async () => {
  localStorage.setItem("steward.locale", "zh-CN");
  vi.mocked(listScans).mockResolvedValue({
    items: [{ ...scanTask("scan-reconciling"), status: "reconciling" }],
  });
  const user = userEvent.setup();
  renderView();

  expect(await screen.findByText("scan-reconciling")).toBeVisible();
  expect(screen.getByText("进行中")).toBeVisible();
  expect(screen.queryByText("确认中")).not.toBeInTheDocument();

  await user.click(screen.getByRole("combobox", { name: "按状态筛选" }));
  expect(screen.getByRole("option", { name: "进行中" })).toBeVisible();
});

it("lists all-region and partial-region scan scopes", async () => {
  vi.mocked(listScans).mockResolvedValue({
    items: [
      {
        id: "scn-23456789abcdefgh",
        connection_id: connection.id,
        status: "running",
        scope_mode: "all_active_regions",
        requested_by: "alice",
        targets: [],
        retry_generation: 0,
        retry_count: 0,
        control_version: 0,
        created_at: "2026-07-21T00:00:00Z",
        target_progress: [],
        progress: {
          completed: 1,
          total: 3,
          running: 2,
          failed: 0,
          resource_count: 42,
        },
        allowed_actions: ["pause", "cancel"],
      } satisfies ScanTask,
      {
        id: "scn-partialabcdefgh",
        connection_id: connection.id,
        status: "succeeded",
        scope_mode: "selected_regions",
        requested_by: "alice",
        targets: [],
        retry_generation: 0,
        retry_count: 0,
        control_version: 0,
        created_at: "2026-07-20T00:00:00Z",
        target_progress: [],
        progress: {
          completed: 1,
          total: 1,
          running: 0,
          failed: 0,
          resource_count: 8,
        },
        allowed_actions: [],
      } satisfies ScanTask,
    ],
  });
  renderView();
  const taskLink = await screen.findByRole("link", {
    name: "scn-23456789abcdefgh",
  });
  expect(taskLink).toBeVisible();
  expect(taskLink).toHaveClass("text-info");
  expect(taskLink).toHaveAttribute("href", "/scans/scn-23456789abcdefgh");
  expect(taskLink.closest("tr")).not.toHaveClass("cursor-pointer");
  expect(
    screen.getByRole("columnheader", { name: "Scan scope" }),
  ).toBeVisible();
  expect(screen.queryByText("Target progress")).not.toBeInTheDocument();
  expect(screen.getByText("All regions")).toBeVisible();
  expect(screen.getByText("Partial regions")).toBeVisible();
  expect(screen.getByText("42")).toBeVisible();
});

it("offers mutually exclusive network scope and hides the resource filter", async () => {
  const user = userEvent.setup();
  renderView();
  await user.click(screen.getByRole("button", { name: "Start scan" }));
  expect(screen.queryByText("Production · alicloud")).not.toBeInTheDocument();
  expect(
    screen.getByRole("radio", { name: "All active regions + global" }),
  ).toBeVisible();
  expect(screen.getByRole("radio", { name: "Selected regions" })).toBeVisible();
  await user.click(
    screen.getByRole("radio", { name: "Selected VPCs / vSwitches" }),
  );
  expect(
    screen.queryByRole("combobox", { name: "Resource kind" }),
  ).not.toBeInTheDocument();
  expect(screen.getByRole("combobox", { name: "VPC" })).toBeVisible();
  expect(screen.getByRole("combobox", { name: "vSwitch" })).toBeVisible();
});

it("uses the searchable resource-type selector pattern for regions", async () => {
  const user = userEvent.setup();
  renderView();
  await user.click(screen.getByRole("button", { name: "Start scan" }));
  await user.click(screen.getByRole("radio", { name: "Selected regions" }));

  await user.click(screen.getByRole("combobox", { name: "Scan regions" }));
  const regionSearch = screen.getByRole("combobox", {
    name: "Scan regions",
  });
  expect(regionSearch).toHaveAttribute(
    "placeholder",
    "Search region name or ID",
  );
  expect(screen.getByRole("option", { name: "global" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  expect(screen.getByRole("option", { name: "global" })).not.toHaveAttribute(
    "aria-disabled",
  );
  expect(
    screen.getByRole("list", { name: "Selected regions" }),
  ).toHaveTextContent("global");
  await user.click(screen.getByRole("button", { name: "Remove global" }));
  expect(screen.getByRole("option", { name: "global" })).toHaveAttribute(
    "aria-selected",
    "false",
  );
  expect(
    screen.queryByRole("button", { name: "Remove global" }),
  ).not.toBeInTheDocument();
  expect(
    screen.getByRole("option", {
      name: "China East 1 · cn-hangzhou",
    }),
  ).toBeVisible();
  await user.type(regionSearch, "hangzhou");
  await user.click(
    screen.getByRole("option", {
      name: "China East 1 · cn-hangzhou",
    }),
  );
  expect(
    screen.getByRole("list", { name: "Selected regions" }),
  ).toHaveTextContent("China East 1 · cn-hangzhou");
  expect(
    screen.getByRole("button", {
      name: "Remove China East 1 · cn-hangzhou",
    }),
  ).toBeVisible();
  const regionPicker = screen
    .getByRole("combobox", { name: "Scan regions" })
    .closest('[data-slot="resource-kind-picker-control"]');
  expect(regionPicker?.querySelector(".lucide-search")).toBeNull();
  await waitFor(() =>
    expect(
      screen.getByRole("combobox", { name: "Scan regions" }),
    ).toHaveFocus(),
  );
  await user.clear(screen.getByRole("combobox", { name: "Scan regions" }));
  await user.keyboard("{Backspace}");
  expect(
    screen.queryByRole("button", {
      name: "Remove China East 1 · cn-hangzhou",
    }),
  ).not.toBeInTheDocument();
  expect(
    screen.queryByRole("list", { name: "Selected regions" }),
  ).not.toBeInTheDocument();
  expect(regionPicker?.querySelector(".lucide-search")).toBeVisible();

  await user.click(
    screen.getByRole("radio", { name: "Selected VPCs / vSwitches" }),
  );
  const queryRegion = screen.getByRole("combobox", { name: "Query region" });
  expect(queryRegion).toHaveTextContent("China East 1 · cn-hangzhou");
  await user.click(queryRegion);
  expect(
    screen.getByRole("combobox", { name: "Query region" }),
  ).toHaveAttribute("placeholder", "Search region name or ID");
});

it("orders scan regions by geography and naturally within each group", async () => {
  const regionIDs = [
    "af-south-1",
    "me-central-1",
    "us-east-1",
    "ap-southeast-10",
    "cn-hongkong",
    "moon-1",
    "ap-northeast-1",
    "cn-beijing",
    "eu-central-1",
    "ap-southeast-2",
  ];
  vi.mocked(listConnectionRegions).mockResolvedValue({
    items: regionIDs.map((regionID, index) => ({
      id: `region-${index}`,
      connection_id: connection.id,
      region_id: regionID,
      name: regionID,
      origin: "api",
      lifecycle: "active",
      created_at: "2026-07-21T00:00:00Z",
      updated_at: "2026-07-21T00:00:00Z",
    })),
  });
  const user = userEvent.setup();
  renderView();

  await user.click(screen.getByRole("button", { name: "Start scan" }));
  await user.click(screen.getByRole("radio", { name: "Selected regions" }));
  await user.click(screen.getByRole("combobox", { name: "Scan regions" }));

  expect(
    screen.getAllByRole("option").map((option) => option.textContent?.trim()),
  ).toEqual([
    "Select regions",
    "global",
    "cn-beijing · cn-beijing",
    "cn-hongkong · cn-hongkong",
    "ap-northeast-1 · ap-northeast-1",
    "ap-southeast-2 · ap-southeast-2",
    "ap-southeast-10 · ap-southeast-10",
    "eu-central-1 · eu-central-1",
    "us-east-1 · us-east-1",
    "me-central-1 · me-central-1",
    "af-south-1 · af-south-1",
    "moon-1 · moon-1",
  ]);

  await user.click(
    screen.getByRole("radio", { name: "Selected VPCs / vSwitches" }),
  );
  expect(
    screen.getByRole("combobox", { name: "Query region" }),
  ).toHaveTextContent("cn-beijing · cn-beijing");
});

it("submits global as a selected region ID", async () => {
  vi.mocked(createScan).mockResolvedValue(scanTask("scan-selected-region"));
  const user = userEvent.setup();
  renderView();

  await user.click(screen.getByRole("button", { name: "Start scan" }));
  await user.click(screen.getByRole("radio", { name: "Selected regions" }));
  await user.click(screen.getByRole("combobox", { name: "Scan regions" }));
  await user.click(
    screen.getByRole("option", {
      name: "China East 1 · cn-hangzhou",
    }),
  );
  await user.click(screen.getByRole("button", { name: "Schedule scan" }));

  await waitFor(() =>
    expect(createScan).toHaveBeenCalledWith(connection.id, {
      scope_mode: "selected_regions",
      region_ids: ["cn-hangzhou", "global"],
      network_targets: undefined,
      resource_kind_ids: undefined,
    }),
  );
});

it("omits global when it is removed from selected regions", async () => {
  vi.mocked(createScan).mockResolvedValue(scanTask("scan-without-global"));
  const user = userEvent.setup();
  renderView();

  await user.click(screen.getByRole("button", { name: "Start scan" }));
  await user.click(screen.getByRole("radio", { name: "Selected regions" }));
  await user.click(screen.getByRole("button", { name: "Remove global" }));
  await user.click(screen.getByRole("combobox", { name: "Scan regions" }));
  await user.click(
    screen.getByRole("option", {
      name: "China East 1 · cn-hangzhou",
    }),
  );
  await user.click(screen.getByRole("button", { name: "Schedule scan" }));

  await waitFor(() =>
    expect(createScan).toHaveBeenCalledWith(connection.id, {
      scope_mode: "selected_regions",
      region_ids: ["cn-hangzhou"],
      network_targets: undefined,
      resource_kind_ids: undefined,
    }),
  );
});

it("searches and submits multiple resource kinds from a compact menu", async () => {
  const kinds = [
    {
      id: "alicloud:ACS::ACK::Cluster",
      provider: "alicloud",
      native_type: "ACS::ACK::Cluster",
      capabilities: ["inventory"],
      display_name: "ACK Cluster",
      display_names: { "en-US": "ACK Cluster", "zh-CN": "ACK 集群" },
      bundle_revision: "catalog-1",
    },
    {
      id: "alicloud:ACS::ECS::Instance",
      provider: "alicloud",
      native_type: "ACS::ECS::Instance",
      capabilities: ["inventory"],
      display_name: "ECS Instance",
      display_names: { "en-US": "ECS Instance", "zh-CN": "ECS 实例" },
      bundle_revision: "catalog-1",
    },
    {
      id: "alicloud:ACS::OSS::Bucket",
      provider: "alicloud",
      native_type: "ACS::OSS::Bucket",
      capabilities: ["inventory"],
      display_name: "OSS Bucket",
      display_names: { "en-US": "OSS Bucket", "zh-CN": "OSS Bucket" },
      bundle_revision: "catalog-1",
    },
  ].reverse();
  vi.mocked(listProviderCatalog).mockResolvedValue([
    {
      provider: "alicloud",
      revision: "catalog-1",
      hash: "hash-1",
      kinds_revision: "kinds-1",
      kinds,
      specs: kinds.map((resourceKind) => ({
        resource_kind: resourceKind,
        revision: "catalog-1",
        hash: `hash-${resourceKind.id}`,
        definition: {
          metadata: {
            provider: "alicloud",
            nativeType: resourceKind.native_type,
          },
          scope: { kind: "region" },
          discovery: { source: "product-api" },
        },
      })),
    } satisfies ProviderCatalogBundle,
  ]);
  vi.mocked(createScan).mockResolvedValue(scanTask("scan-multiple-kinds"));
  const user = userEvent.setup();
  renderView();

  await user.click(screen.getByRole("button", { name: "Start scan" }));
  const picker = screen.getByRole("combobox", { name: "Resource kind" });
  await user.click(picker);

  const search = screen.getByRole("combobox", { name: "Resource kind" });
  expect(search).toHaveAttribute(
    "placeholder",
    "Search product or resource kind",
  );
  expect(search).toHaveValue("");
  expect(screen.getByText("ACK Cluster")).toBeVisible();
  expect(screen.getByText("ECS Instance")).toBeVisible();
  expect(screen.getByText("OSS Bucket")).toBeVisible();
  const listbox = screen.getByRole("listbox");
  expect(listbox).toHaveAttribute("aria-multiselectable", "true");
  expect(
    within(listbox)
      .getAllByRole("option")
      .map((option) => option.getAttribute("aria-label")),
  ).toEqual([
    "All indexed resource kinds",
    "ACK Cluster · ACK",
    "ECS Instance · ECS",
    "OSS Bucket · OSS",
  ]);
  expect(
    screen.getByRole("option", { name: "All indexed resource kinds" }),
  ).toHaveAttribute("aria-selected", "true");
  expect(screen.getByRole("dialog", { name: "Start scan" })).toContainElement(
    listbox,
  );
  expect(listbox).toHaveStyle({ overflowY: "auto" });
  expect(listbox.style.maxHeight).toContain(
    "--radix-popover-content-available-height",
  );

  await user.type(search, "ecs");
  expect(search).toHaveValue("ecs");
  expect(screen.getByText("ECS Instance")).toBeVisible();
  expect(screen.queryByText("ACK Cluster")).not.toBeInTheDocument();
  expect(screen.queryByText("OSS Bucket")).not.toBeInTheDocument();

  await user.click(screen.getByText("ECS Instance"));
  expect(search).toBeVisible();
  expect(
    screen.getByRole("option", { name: "ECS Instance · ECS" }),
  ).toHaveAttribute("aria-selected", "true");

  await user.clear(search);
  await user.type(search, "oss");
  await user.click(screen.getByText("OSS Bucket"));
  expect(search).toBeVisible();
  expect(
    screen.getByRole("option", { name: "OSS Bucket · OSS" }),
  ).toHaveAttribute("aria-selected", "true");

  await user.keyboard("{Escape}");
  const selectedKinds = screen.getByLabelText("Selected resource kinds");
  const pickerControl = selectedKinds.closest(
    '[data-slot="resource-kind-picker-control"]',
  );
  expect(pickerControl).toContainElement(selectedKinds);
  expect(pickerControl).toHaveClass("h-11", "sm:h-9");
  expect(selectedKinds).toHaveClass(
    "overflow-x-auto",
    "overflow-y-hidden",
    "whitespace-nowrap",
  );
  expect(within(selectedKinds).getByText("ECS Instance")).toBeVisible();
  expect(within(selectedKinds).getByText("OSS Bucket")).toBeVisible();
  expect(
    within(selectedKinds).getByRole("button", {
      name: "Remove ECS Instance",
    }),
  ).toBeVisible();

  await user.click(screen.getByRole("button", { name: "Schedule scan" }));
  await waitFor(() =>
    expect(createScan).toHaveBeenCalledWith(connection.id, {
      scope_mode: "all_active_regions",
      region_ids: undefined,
      network_targets: undefined,
      resource_kind_ids: [
        "alicloud:ACS::ECS::Instance",
        "alicloud:ACS::OSS::Bucket",
      ],
    }),
  );
});

it("uses the shared centered dialog and restores focus to its trigger", async () => {
  const user = userEvent.setup();
  renderView();
  const trigger = screen.getByRole("button", { name: "Start scan" });
  await user.click(trigger);
  const dialog = screen.getByRole("dialog", { name: "Start scan" });
  expect(dialog).toHaveClass("sm:max-w-3xl");
  expect(dialog).not.toHaveAttribute("aria-describedby");
  expect(
    screen.queryByText(/Target names and IDs are snapshotted/),
  ).not.toBeInTheDocument();
  expect(dialog).toContainElement(
    screen.getByRole("button", { name: "Cancel" }),
  );
  await user.click(screen.getByRole("button", { name: "Cancel" }));
  await waitFor(() =>
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
  );
  expect(trigger).toHaveFocus();
});

it("retains selected VPCs while querying and selecting another VPC", async () => {
  const first = {
    kind: "vpc",
    region_id: "cn-hangzhou",
    native_id: "vpc-a",
    name: "VPC A",
  } satisfies NetworkTargetOption;
  const second = {
    kind: "vpc",
    region_id: "cn-hangzhou",
    native_id: "vpc-b",
    name: "VPC B",
  } satisfies NetworkTargetOption;
  vi.mocked(searchNetworkTargets).mockImplementation(
    async (_connectionID, kind, input) => ({
      items:
        kind === "vpc" ? (input.query === "second" ? [second] : [first]) : [],
    }),
  );
  const user = userEvent.setup();
  renderView();
  await user.click(screen.getByRole("button", { name: "Start scan" }));
  await user.click(
    screen.getByRole("radio", { name: "Selected VPCs / vSwitches" }),
  );
  const picker = screen.getByRole("combobox", { name: "VPC" });
  await user.click(picker);
  await user.click(await screen.findByText("VPC A"));
  await user.clear(screen.getByPlaceholderText("Search VPC name or ID"));
  await user.type(
    screen.getByPlaceholderText("Search VPC name or ID"),
    "second",
  );
  await user.click(await screen.findByText("VPC B"));
  await user.keyboard("{Escape}");
  expect(screen.getByRole("combobox", { name: "VPC" })).toHaveTextContent(
    "VPC · 2",
  );
  expect(screen.getByLabelText("Selected network targets")).toBeVisible();
});

it("paginates live VPC queries and consumes the query abort signal", async () => {
  let consumedSignal = false;
  vi.mocked(searchNetworkTargets).mockImplementation(
    async (_connectionID, kind, input) => {
      consumedSignal ||= Boolean(
        (input as typeof input & { signal?: AbortSignal }).signal,
      );
      if (kind !== "vpc") return { items: [] };
      if (input.cursor === "vpc-page-2") {
        return {
          items: [
            {
              kind: "vpc",
              region_id: "cn-hangzhou",
              native_id: "vpc-b",
              name: "VPC B",
            },
          ],
        };
      }
      return {
        items: [
          {
            kind: "vpc",
            region_id: "cn-hangzhou",
            native_id: "vpc-a",
            name: "VPC A",
          },
        ],
        next_cursor: "vpc-page-2",
      };
    },
  );
  const user = userEvent.setup();
  renderView();
  await user.click(screen.getByRole("button", { name: "Start scan" }));
  await user.click(
    screen.getByRole("radio", { name: "Selected VPCs / vSwitches" }),
  );
  await user.click(screen.getByRole("combobox", { name: "VPC" }));
  await user.click(await screen.findByRole("button", { name: "Load more" }));
  expect(await screen.findByText("VPC B")).toBeVisible();
  expect(consumedSignal).toBe(true);
  expect(searchNetworkTargets).toHaveBeenCalledWith(
    connection.id,
    "vpc",
    expect.objectContaining({ cursor: "vpc-page-2" }),
  );
});

it("deduplicates network union and lets a VPC cover its vSwitches", () => {
  const vpc = {
    kind: "vpc",
    region_id: "cn-hangzhou",
    native_id: "vpc-a",
  } satisfies NetworkTargetOption;
  const covered = {
    kind: "vswitch",
    region_id: "cn-hangzhou",
    native_id: "vsw-a",
    parent_native_id: "vpc-a",
  } satisfies NetworkTargetOption;
  const other = {
    kind: "vswitch",
    region_id: "cn-hangzhou",
    native_id: "vsw-b",
    parent_native_id: "vpc-b",
  } satisfies NetworkTargetOption;
  expect(canonicalNetworkTargets([vpc, vpc], [covered, other])).toEqual([
    vpc,
    other,
  ]);
});
