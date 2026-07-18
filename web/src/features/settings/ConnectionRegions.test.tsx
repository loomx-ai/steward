import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  addConnectionRegion,
  excludeConnectionRegion,
  getJob,
  listConnectionRegions,
  refreshConnectionRegions,
  restoreConnectionRegion,
  updateConnectionRegion,
} from "@/api/client";
import type { CloudConnection, ConnectionRegion, Job } from "@/api/types";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import { ConnectionRegions } from "./ConnectionRegions";

vi.mock("@/api/client", () => ({
  addConnectionRegion: vi.fn(),
  excludeConnectionRegion: vi.fn(),
  getJob: vi.fn(),
  listConnectionRegions: vi.fn(),
  refreshConnectionRegions: vi.fn(),
  restoreConnectionRegion: vi.fn(),
  updateConnectionRegion: vi.fn(),
}));

const connection = {
  id: "connection-a",
  name: "Production",
  last_region_refresh_at: "2026-07-21T08:00:00Z",
} as CloudConnection;
const region = {
  id: "region-a",
  connection_id: connection.id,
  region_id: "cn-hangzhou",
  name: "华东 1（杭州）",
  discovered_name: "China East 1",
  name_override: "华东 1（杭州）",
  origin: "api",
  lifecycle: "active",
  created_at: "2026-07-21T08:00:00Z",
  updated_at: "2026-07-21T08:00:00Z",
} as ConnectionRegion;

beforeEach(() => {
  localStorage.setItem("steward.locale", "zh-CN");
  vi.mocked(listConnectionRegions)
    .mockReset()
    .mockResolvedValue({ items: [region] });
  vi.mocked(addConnectionRegion).mockReset();
  vi.mocked(excludeConnectionRegion).mockReset();
  vi.mocked(getJob).mockReset();
  vi.mocked(refreshConnectionRegions).mockReset();
  vi.mocked(restoreConnectionRegion).mockReset();
  vi.mocked(updateConnectionRegion).mockReset().mockResolvedValue(region);
  vi.stubGlobal(
    "ResizeObserver",
    class ResizeObserver {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
});

afterEach(() => vi.useRealTimers());

it("loads all regions in one request and reloads the full result for a filter", async () => {
  const secondRegion = {
    ...region,
    id: "region-b",
    region_id: "cn-beijing",
    name: "华北 2（北京）",
  };
  vi.mocked(listConnectionRegions).mockImplementation(
    async (_connectionID, options) =>
      options?.lifecycle === "retired"
        ? { items: [] }
        : { items: [region, secondRegion] },
  );
  const user = userEvent.setup();
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider>
        <ConnectionRegions
          connection={connection}
          panelID="connection-regions-connection-a"
          onCollapse={vi.fn()}
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );

  expect(await screen.findByText("华东 1（杭州）")).toBeVisible();
  expect(await screen.findByText("华北 2（北京）")).toBeVisible();
  expect(
    screen.queryByRole("button", { name: "下一页" }),
  ).not.toBeInTheDocument();
  const initialOptions = vi.mocked(listConnectionRegions).mock.calls[0]?.[1];
  expect(initialOptions).toEqual(
    expect.objectContaining({
      lifecycle: undefined,
      query: undefined,
    }),
  );
  expect(initialOptions).not.toHaveProperty("cursor");
  expect(initialOptions).not.toHaveProperty("limit");

  await user.click(screen.getByRole("button", { name: /已关停/ }));
  await waitFor(() =>
    expect(listConnectionRegions).toHaveBeenLastCalledWith(
      "connection-a",
      expect.objectContaining({
        lifecycle: "retired",
        query: undefined,
      }),
    ),
  );
});

it("orders the complete connection region result by geography", async () => {
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
      ...region,
      id: `region-${index}`,
      region_id: regionID,
      name: regionID,
      name_override: regionID,
    })),
  });

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider>
        <ConnectionRegions
          connection={connection}
          panelID="connection-regions-connection-a"
          onCollapse={vi.fn()}
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );

  const rows = await screen.findAllByRole("article");
  expect(
    rows.map((row) => row.querySelector(".font-mono")?.textContent?.trim()),
  ).toEqual([
    "cn-beijing",
    "cn-hongkong",
    "ap-northeast-1",
    "ap-southeast-2",
    "ap-southeast-10",
    "eu-central-1",
    "us-east-1",
    "me-central-1",
    "af-south-1",
    "moon-1",
  ]);
});

it("polls the refresh job every second and reloads regions once at completion", async () => {
  const runningJob = {
    id: "region-refresh-job",
    connection_id: connection.id,
    type: "region_refresh",
    status: "running",
    payload: {},
    run_at: "2026-07-21T08:00:00Z",
    attempts: 1,
    created_at: "2026-07-21T08:00:00Z",
    updated_at: "2026-07-21T08:00:00Z",
  } satisfies Job;
  vi.mocked(refreshConnectionRegions).mockResolvedValue({
    job_id: runningJob.id,
    status: "pending",
  });
  vi.mocked(getJob)
    .mockResolvedValueOnce(runningJob)
    .mockResolvedValueOnce({
      ...runningJob,
      status: "succeeded",
      finished_at: "2026-07-21T08:00:01Z",
    });

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider>
        <ConnectionRegions
          connection={connection}
          panelID="connection-regions-connection-a"
          onCollapse={vi.fn()}
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );
  expect(await screen.findByText("华东 1（杭州）")).toBeVisible();
  const initialRegionCalls = vi.mocked(listConnectionRegions).mock.calls.length;
  vi.useFakeTimers();

  fireEvent.click(screen.getByRole("button", { name: "从云 API 刷新" }));
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
  expect(refreshConnectionRegions).toHaveBeenCalledTimes(1);
  expect(getJob).toHaveBeenCalledTimes(1);
  expect(screen.getByRole("button", { name: "正在刷新地域…" })).toBeDisabled();

  await act(async () => {
    await vi.advanceTimersByTimeAsync(1_000);
    await Promise.resolve();
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1);
    await Promise.resolve();
  });
  expect(getJob).toHaveBeenCalledTimes(2);
  expect(listConnectionRegions).toHaveBeenCalledTimes(initialRegionCalls + 1);
  expect(screen.getByRole("button", { name: "从云 API 刷新" })).toBeEnabled();

  await act(async () => {
    await vi.advanceTimersByTimeAsync(5_000);
  });
  expect(getJob).toHaveBeenCalledTimes(2);
  expect(listConnectionRegions).toHaveBeenCalledTimes(initialRegionCalls + 1);
});

it("stops polling and shows the refresh job failure", async () => {
  const failedJob = {
    id: "failed-region-refresh-job",
    connection_id: connection.id,
    type: "region_refresh",
    status: "failed",
    payload: {},
    run_at: "2026-07-21T08:00:00Z",
    attempts: 1,
    last_error: "cloud region discovery failed",
    created_at: "2026-07-21T08:00:00Z",
    updated_at: "2026-07-21T08:00:01Z",
    finished_at: "2026-07-21T08:00:01Z",
  } satisfies Job;
  vi.mocked(refreshConnectionRegions).mockResolvedValue({
    job_id: failedJob.id,
    status: "pending",
  });
  vi.mocked(getJob).mockResolvedValue(failedJob);

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider>
        <ConnectionRegions
          connection={connection}
          panelID="connection-regions-connection-a"
          onCollapse={vi.fn()}
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );
  expect(await screen.findByText("华东 1（杭州）")).toBeVisible();
  vi.useFakeTimers();

  fireEvent.click(screen.getByRole("button", { name: "从云 API 刷新" }));
  await act(async () => {
    await Promise.resolve();
    await vi.advanceTimersByTimeAsync(1);
  });
  expect(screen.getByText("cloud region discovery failed")).toBeVisible();
  expect(getJob).toHaveBeenCalledTimes(1);

  await act(async () => {
    await vi.advanceTimersByTimeAsync(5_000);
  });
  expect(getJob).toHaveBeenCalledTimes(1);
});

it("shows region name first and lets an operator edit it", async () => {
  const user = userEvent.setup();
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider>
        <ConnectionRegions
          connection={connection}
          panelID="connection-regions-connection-a"
          onCollapse={vi.fn()}
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );

  expect(await screen.findByText("华东 1（杭州）")).toBeVisible();
  expect(screen.getByText("cn-hangzhou")).toHaveClass("text-muted-foreground");
  await user.click(screen.getByRole("button", { name: "操作" }));
  await user.click(screen.getByRole("menuitem", { name: /修改显示名称/ }));
  const input = screen.getByRole("textbox", { name: "地域名称" });
  await user.clear(input);
  await user.type(input, "杭州生产地域");
  await user.click(screen.getByRole("button", { name: "保存地域名称" }));

  await waitFor(() =>
    expect(updateConnectionRegion).toHaveBeenCalledWith(
      "connection-a",
      "cn-hangzhou",
      { name: "杭州生产地域" },
    ),
  );
});

it("keeps the region list compact by hiding repeated default metadata", async () => {
  const user = userEvent.setup();
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider>
        <ConnectionRegions
          connection={connection}
          panelID="connection-regions-connection-a"
          onCollapse={vi.fn()}
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );

  expect(await screen.findByText("华东 1（杭州）")).toBeVisible();
  expect(
    screen.queryByText(
      "维护 Production 的全部地域列表，仅启用地域会参与后续扫描。",
    ),
  ).not.toBeInTheDocument();
  expect(screen.queryByText("云 API")).not.toBeInTheDocument();
  expect(screen.queryByText("启用")).not.toBeInTheDocument();
  expect(
    screen.queryByText("API 返回名称：China East 1"),
  ).not.toBeInTheDocument();
  expect(screen.queryByText("1 个地域")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "全部地域 1" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  expect(screen.getByRole("button", { name: "已关停 0" })).toHaveAttribute(
    "aria-pressed",
    "false",
  );
  expect(
    screen.queryByRole("combobox", { name: "筛选地域" }),
  ).not.toBeInTheDocument();

  const collapse = screen.getByRole("button", { name: "收起地域管理" });
  expect(collapse).toHaveTextContent("");
  await user.hover(collapse);
  expect(await screen.findByRole("tooltip")).toHaveTextContent("收起地域管理");

  const refresh = screen.getByRole("button", { name: "从云 API 刷新" });
  expect(refresh).toHaveTextContent("");
  expect(refresh).toHaveAttribute("data-slot", "tooltip-trigger");
});

it("lets an operator close the manual region form from its trigger or Escape", async () => {
  const user = userEvent.setup();
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider>
        <ConnectionRegions
          connection={connection}
          panelID="connection-regions-connection-a"
          onCollapse={vi.fn()}
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );

  const addTrigger = screen.getByRole("button", { name: "手动添加地域" });
  await user.click(addTrigger);
  expect(screen.getByRole("textbox", { name: "地域 ID" })).toBeVisible();

  const cancelTrigger = screen.getByRole("button", { name: "取消添加地域" });
  expect(cancelTrigger).toHaveAttribute("aria-expanded", "true");
  await user.click(cancelTrigger);
  expect(
    screen.queryByRole("textbox", { name: "地域 ID" }),
  ).not.toBeInTheDocument();
  expect(addTrigger).toHaveAttribute("aria-expanded", "false");

  await user.click(addTrigger);
  await user.keyboard("{Escape}");
  expect(
    screen.queryByRole("textbox", { name: "地域 ID" }),
  ).not.toBeInTheDocument();
  expect(addTrigger).toHaveFocus();
});
