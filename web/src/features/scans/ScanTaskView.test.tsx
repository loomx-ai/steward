import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  controlScan,
  getScan,
  getScanLogs,
  streamScanEvents,
} from "@/api/client";
import type { CloudConnection, JobLog, ScanTask } from "@/api/types";
import { TooltipProvider } from "@/components/ui/tooltip";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import { collapsedTargetLimit, ScanTaskView } from "./ScanTaskView";

const connection = {
  id: "con-23456789abcdefgh",
  name: "Production",
  provider: "alicloud",
} as CloudConnection;

const task = {
  id: "scn-23456789abcdefgh",
  connection_id: connection.id,
  status: "succeeded",
  scope_mode: "all_active_regions",
  requested_by: "alice",
  targets: [],
  retry_generation: 0,
  retry_count: 0,
  control_version: 0,
  created_at: "2026-07-23T11:57:48.000",
  updated_at: "2026-07-23T12:00:00.000",
  duration_ms: 72_000,
  target_progress: [
    {
      key: "region:ap-northeast-1",
      kind: "region",
      region_id: "ap-northeast-1",
      region_name: "Japan (Tokyo)",
      status: "succeeded",
      completed: 1,
      total: 1,
      resource_count: 33,
      error_count: 0,
      summary: "33 resources",
    },
    {
      key: "region:ap-northeast-2",
      kind: "region",
      region_id: "ap-northeast-2",
      region_name: "Korea (Seoul)",
      status: "succeeded",
      completed: 1,
      total: 1,
      resource_count: 19,
      error_count: 0,
      summary: "19 resources",
    },
  ],
  progress: {
    completed: 2,
    total: 2,
    running: 0,
    failed: 0,
    resource_count: 52,
  },
  allowed_actions: [],
} satisfies ScanTask;

function scanTarget(
  suffix: string,
  name: string,
  status: string,
): ScanTask["target_progress"][number] {
  return {
    key: `region:${suffix}`,
    kind: "region",
    region_id: suffix,
    region_name: name,
    status,
    completed: status === "succeeded" ? 1 : 0,
    total: 1,
    resource_count: status === "succeeded" ? 1 : 0,
    error_count: status === "failed" ? 1 : 0,
    summary: status,
  };
}

const crowdedTask = {
  ...task,
  status: "running",
  target_progress: [
    scanTarget("success-1", "Success 1", "succeeded"),
    scanTarget("success-2", "Success 2", "succeeded"),
    scanTarget("success-3", "Success 3", "succeeded"),
    scanTarget("success-4", "Success 4", "succeeded"),
    scanTarget("success-5", "Success 5", "succeeded"),
    scanTarget("success-6", "Success 6", "succeeded"),
    scanTarget("success-7", "Success 7", "succeeded"),
    scanTarget("failed", "Failed region", "failed"),
    scanTarget("running", "Running region", "running"),
    scanTarget("waiting", "Waiting region", "waiting"),
  ],
  progress: {
    completed: 7,
    total: 10,
    running: 1,
    failed: 1,
    resource_count: 7,
  },
} satisfies ScanTask;

const tokyoLog = {
  id: "log-23456789abcdefga",
  job_id: "job-23456789abcdefgh",
  aggregate_type: "scan_task",
  aggregate_id: task.id,
  target_key: "region:ap-northeast-1",
  sequence: 1,
  kind: "text",
  level: "info",
  message: "Tokyo scan completed",
  created_at: "2026-07-23T13:38:30.386",
} satisfies JobLog;

const seoulLog = {
  ...tokyoLog,
  id: "log-23456789abcdefgb",
  target_key: "region:ap-northeast-2",
  sequence: 2,
  message: "Seoul scan completed",
  created_at: "2026-07-23T13:38:31.004",
} satisfies JobLog;

vi.mock("@/api/client", () => ({
  controlScan: vi.fn(),
  getScan: vi.fn(),
  getScanLogs: vi.fn(),
  streamScanEvents: vi.fn(),
}));

vi.mock("@/connections/ActiveConnectionProvider", () => ({
  useRequiredConnection: () => connection,
}));

vi.mock("@/app/PageTitleContext", () => ({
  PageTitle: ({ parent }: { parent?: { label: string } }) => (
    <span data-testid="page-title-parent">{parent?.label}</span>
  ),
}));

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
  vi.mocked(controlScan).mockReset();
  vi.mocked(getScan).mockReset().mockResolvedValue(task);
  vi.mocked(getScanLogs).mockReset().mockResolvedValue({
    items: [],
    live_cursor: "history-live",
  });
  vi.mocked(streamScanEvents)
    .mockReset()
    .mockImplementation(
      async (_connection, _scan, targetKey, _after, onEvent, signal) => {
        if (!targetKey || targetKey === tokyoLog.target_key) {
          onEvent({ type: "log", data: tokyoLog, id: "cursor-1" });
        }
        if (!targetKey || targetKey === seoulLog.target_key) {
          onEvent({ type: "log", data: seoulLog, id: "cursor-2" });
        }
        return new Promise<string>((_resolve, reject) => {
          signal.addEventListener(
            "abort",
            () => reject(new DOMException("aborted", "AbortError")),
            { once: true },
          );
        });
      },
    );
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function renderView() {
  return render(
    <MemoryRouter initialEntries={[`/scans/${task.id}`]}>
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <TooltipProvider>
          <LocaleProvider>
            <Routes>
              <Route path="/scans/:id" element={<ScanTaskView />} />
            </Routes>
          </LocaleProvider>
        </TooltipProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

it("limits collapsed targets to three responsive rows", () => {
  expect(collapsedTargetLimit(639)).toBe(3);
  expect(collapsedTargetLimit(640)).toBe(6);
  expect(collapsedTargetLimit(1279)).toBe(6);
  expect(collapsedTargetLimit(1280)).toBe(9);
  expect(collapsedTargetLimit(1535)).toBe(9);
  expect(collapsedTargetLimit(1536)).toBe(12);
});

it("keeps global before China even when China needs attention", async () => {
  vi.mocked(getScan).mockResolvedValue({
    ...task,
    status: "running",
    target_progress: [
      scanTarget("cn-hangzhou", "China East 1", "failed"),
      {
        ...scanTarget("global", "Global", "succeeded"),
        key: "global",
        kind: "global",
        region_id: "global",
      },
    ],
  });
  const view = renderView();

  const heading = await screen.findByRole("heading", {
    name: "Region progress",
  });
  const targetButtons = within(heading.closest("section")!)
    .getAllByRole("button")
    .filter((button) => button.hasAttribute("aria-pressed"));
  expect(targetButtons[0]).toHaveAccessibleName(/^global/i);
  expect(targetButtons[1]).toHaveAccessibleName(/China East 1/);

  view.unmount();
  await act(async () => Promise.resolve());
});

it("labels the failed scan action as retry", async () => {
  vi.mocked(getScan).mockResolvedValue({
    ...task,
    status: "failed",
    allowed_actions: ["retry"],
  });
  const user = userEvent.setup();
  renderView();

  const retry = await screen.findByRole("button", { name: "Retry" });
  expect(
    screen.queryByRole("button", { name: "Complete and retry" }),
  ).not.toBeInTheDocument();

  await user.click(retry);
  expect(
    screen.getByRole("alertdialog", { name: "Retry this scan?" }),
  ).toBeVisible();
  expect(
    screen.getByText(`Task ${task.id} will retry failed or unfinished work.`),
  ).toBeVisible();
});

it("presents scan reconciliation as an in-progress scan", async () => {
  localStorage.setItem("steward.locale", "zh-CN");
  vi.mocked(getScan).mockResolvedValue({
    ...task,
    status: "reconciling",
  });
  renderView();

  expect(await screen.findByText("进行中")).toBeVisible();
  expect(screen.queryByText("确认中")).not.toBeInTheDocument();
});

it("keeps the overview flat and uses a region card as the only log filter", async () => {
  const user = userEvent.setup();
  const view = renderView();

  expect(await screen.findByTestId("page-title-parent")).toHaveTextContent(
    "Scans",
  );
  expect(await screen.findByText("Scan ID")).toBeVisible();
  expect(
    screen.getByRole("button", { name: "Copy Scan ID" }),
  ).toBeInTheDocument();
  expect(screen.getAllByText("Succeeded")).toHaveLength(1);
  expect(screen.queryByText("Back")).not.toBeInTheDocument();
  expect(
    screen.queryByText("Click a region to view logs"),
  ).not.toBeInTheDocument();
  expect(screen.getByText("All regions")).toBeVisible();
  expect(screen.getByText("Updated")).toBeVisible();
  expect(screen.getByText("1m 12s")).toBeVisible();

  expect(await screen.findByText("Tokyo scan completed")).toBeVisible();
  expect(screen.getByText("Seoul scan completed")).toBeVisible();

  const tokyo = screen.getByRole("button", { name: /Japan \(Tokyo\)/ });
  await user.hover(
    within(tokyo).getByRole("img", {
      name: "Succeeded",
    }),
  );
  expect(await screen.findByRole("tooltip")).toHaveTextContent("Succeeded");
  await user.unhover(
    within(tokyo).getByRole("img", {
      name: "Succeeded",
    }),
  );

  await user.click(tokyo);
  await waitFor(() =>
    expect(getScanLogs).toHaveBeenCalledWith(
      connection.id,
      task.id,
      "region:ap-northeast-1",
      "",
      expect.any(AbortSignal),
    ),
  );
  await waitFor(() =>
    expect(screen.queryByText("Seoul scan completed")).not.toBeInTheDocument(),
  );
  expect(screen.getByText("Tokyo scan completed")).toBeVisible();
  expect(tokyo).toHaveAttribute("aria-pressed", "true");
  expect(tokyo).toHaveClass(
    "border-info",
    "bg-info/5",
    "ring-1",
    "ring-info/40",
  );
  const regionGrid = tokyo.parentElement;
  expect(regionGrid).toHaveClass("2xl:grid-cols-4");
  expect(regionGrid?.className).not.toMatch(/grid-cols-(?:[5-9]|1\d)/);

  await user.click(tokyo);
  await waitFor(() =>
    expect(getScanLogs).toHaveBeenCalledWith(
      connection.id,
      task.id,
      "",
      "",
      expect.any(AbortSignal),
    ),
  );
  expect(await screen.findByText("Seoul scan completed")).toBeVisible();
  expect(tokyo).toHaveAttribute("aria-pressed", "false");
  view.unmount();
  await act(async () => Promise.resolve());
});

it("keeps logs close by collapsing to three rows with actionable regions first", async () => {
  vi.stubGlobal("innerWidth", 1280);
  vi.mocked(getScan).mockResolvedValue(crowdedTask);
  const user = userEvent.setup();
  const view = renderView();

  const heading = await screen.findByRole("heading", {
    name: "Region progress",
  });
  const regionSection = heading.closest("section");
  expect(regionSection).not.toBeNull();

  const collapsedTargets = within(regionSection!)
    .getAllByRole("button")
    .filter((button) => button.hasAttribute("aria-pressed"));
  expect(collapsedTargets).toHaveLength(9);
  expect(collapsedTargets[0]).toHaveAccessibleName(/Failed region/);
  expect(collapsedTargets[1]).toHaveAccessibleName(/Running region/);
  expect(collapsedTargets[2]).toHaveAccessibleName(/Waiting region/);
  expect(
    within(regionSection!).queryByRole("button", { name: /Success 7/ }),
  ).not.toBeInTheDocument();

  const showAll = within(regionSection!).getByRole("button", {
    name: "Show all (10)",
  });
  expect(showAll).toHaveAttribute("aria-expanded", "false");
  await user.click(showAll);

  expect(
    within(regionSection!)
      .getAllByRole("button")
      .filter((button) => button.hasAttribute("aria-pressed")),
  ).toHaveLength(10);
  const expandedOrder = within(regionSection!)
    .getAllByRole("button")
    .filter((button) => button.hasAttribute("aria-pressed"))
    .map((button) => button.textContent);
  const selected = within(regionSection!).getByRole("button", {
    name: /Success 7/,
  });
  await user.click(selected);
  expect(
    within(regionSection!)
      .getAllByRole("button")
      .filter((button) => button.hasAttribute("aria-pressed"))
      .map((button) => button.textContent),
  ).toEqual(expandedOrder);
  expect(
    screen.getByRole("heading", { name: "Logs · Success 7" }),
  ).toBeVisible();

  const showLess = within(regionSection!).getByRole("button", {
    name: "Show less",
  });
  expect(showLess).toHaveAttribute("aria-expanded", "true");
  await user.click(showLess);

  const recollapsedTargets = within(regionSection!)
    .getAllByRole("button")
    .filter((button) => button.hasAttribute("aria-pressed"));
  expect(recollapsedTargets).toHaveLength(9);
  expect(recollapsedTargets[0]).toHaveAccessibleName(/Failed region/);
  expect(
    within(regionSection!).queryByRole("button", { name: /Success 7/ }),
  ).not.toBeInTheDocument();
  expect(
    screen.getByRole("heading", { name: "Logs · Success 7" }),
  ).toBeVisible();

  await user.click(screen.getByRole("button", { name: "Clear filter" }));
  await waitFor(() =>
    expect(getScanLogs).toHaveBeenCalledWith(
      connection.id,
      task.id,
      "",
      "",
      expect.any(AbortSignal),
    ),
  );
  expect(screen.getByRole("heading", { name: "Logs" })).toBeVisible();
  expect(
    screen.queryByRole("button", { name: "Clear filter" }),
  ).not.toBeInTheDocument();
  expect(
    within(regionSection!).queryByRole("button", { name: /Success 7/ }),
  ).not.toBeInTheDocument();

  view.unmount();
  await act(async () => Promise.resolve());
});
