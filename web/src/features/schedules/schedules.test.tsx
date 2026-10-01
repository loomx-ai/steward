import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  createSchedule,
  getScheduleSettings,
  listConnectionRegions,
  listProviderCatalog,
  listScanChanges,
  listSchedules,
  previewSchedule,
  setScheduleEnabled,
} from "@/api/client";
import type {
  CloudConnection,
  ConnectionScheduleOverview,
  ScanSchedule,
  ScheduleRun,
} from "@/api/types";
import { LocaleProvider, useLocale } from "@/i18n/LocaleProvider";
import { ScanChanges } from "@/features/scans/ScanChanges";
import { ScheduleDialog } from "./ScheduleDialog";
import { SchedulesTab } from "./SchedulesTab";
import { describeFrequency, runResult } from "./scheduleFormat";
import { connectionFreshness } from "./useScheduleOverview";

const connection = {
  id: "con-23456789abcdefgh",
  name: "Production",
  provider: "alicloud",
} as CloudConnection;

vi.mock("@/api/client", () => ({
  createSchedule: vi.fn(),
  deleteSchedule: vi.fn(),
  getScheduleSettings: vi.fn(),
  listConnectionRegions: vi.fn(),
  listProviderCatalog: vi.fn(),
  listScanChanges: vi.fn(),
  listSchedules: vi.fn(),
  previewSchedule: vi.fn(),
  runSchedule: vi.fn(),
  searchNetworkTargets: vi.fn(),
  setScheduleEnabled: vi.fn(),
  updateSchedule: vi.fn(),
}));

function schedule(overrides: Partial<ScanSchedule> = {}): ScanSchedule {
  return {
    id: "sch-1",
    connection_id: connection.id,
    name: "Core network",
    enabled: true,
    scope: { scope_mode: "all_active_regions" },
    frequency: { kind: "daily", time: "03:12", timezone: "Asia/Shanghai" },
    rules: {
      overlap: "skip",
      missed: "catch_up",
      retry_failed_targets: true,
      pause_after_failures: 3,
    },
    next_run_at: "2026-10-02T19:12:00Z",
    consecutive_failures: 0,
    created_by: "alice",
    updated_by: "alice",
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
    ...overrides,
  };
}

beforeEach(() => {
  localStorage.setItem("steward.locale", "en-US");
  vi.mocked(getScheduleSettings).mockReset().mockResolvedValue({
    default_schedule_enabled: false,
    retention_days: 30,
    min_interval_seconds: 3600,
  });
  vi.mocked(listProviderCatalog).mockReset().mockResolvedValue([]);
  vi.mocked(listConnectionRegions)
    .mockReset()
    .mockResolvedValue({
      items: [
        {
          id: "rgn-1",
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
  vi.mocked(previewSchedule)
    .mockReset()
    .mockResolvedValue({
      next_runs: ["2026-10-01T22:30:00Z"],
      min_interval_seconds: 3600,
    });
  vi.mocked(createSchedule).mockReset();
  vi.mocked(listSchedules).mockReset();
  vi.mocked(setScheduleEnabled).mockReset();
  vi.mocked(listScanChanges).mockReset();
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

function wrap(children: React.ReactNode) {
  return render(
    <MemoryRouter>
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <LocaleProvider>{children}</LocaleProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

describe("schedule wording", () => {
  it("lists weekly days from Monday and explains run results", () => {
    let text = "";
    let partial = "";
    let skipped = "";
    function Probe() {
      const { t, label } = useLocale();
      text = describeFrequency(
        { kind: "weekly", weekdays: [0, 5, 1], time: "04:00", timezone: "UTC" },
        t,
        "en-US",
      );
      const base = {
        id: "r",
        schedule_id: "s",
        connection_id: "c",
        planned_at: "",
        actor: "scheduler",
        auto_retried: true,
        settled: true,
        created_at: "",
      } as const;
      partial = runResult(
        {
          ...base,
          trigger: "schedule",
          outcome: "started",
          final_status: "partial",
          failed_items: 2,
          failure_summary: "cn-hongkong · NAS",
        } as ScheduleRun,
        t,
        label,
      ).label;
      skipped =
        runResult(
          {
            ...base,
            trigger: "schedule",
            outcome: "skipped",
            skip_reason: "missed",
          } as ScheduleRun,
          t,
          label,
        ).detail ?? "";
      return null;
    }
    wrap(<Probe />);
    expect(text).toBe("Mon, Fri, Sun at 04:00");
    expect(partial).toBe("2 failed");
    expect(skipped).toBe("Steward was not running at this time");
  });

  it("ranks a paused schedule above age when judging freshness", () => {
    const base: ConnectionScheduleOverview = {
      connection_id: "c",
      connection_name: "c",
      provider: "aws",
      status: "active",
      schedules: [schedule()],
      last_complete_scan_at: "2026-10-01T00:00:00Z",
    };
    const now = Date.parse("2026-10-01T12:00:00Z");
    expect(connectionFreshness(base, now)).toBe("fresh");
    expect(
      connectionFreshness(
        { ...base, last_complete_scan_at: "2026-09-28T00:00:00Z" },
        now,
      ),
    ).toBe("stale");
    expect(
      connectionFreshness(
        {
          ...base,
          schedules: [
            schedule({ enabled: false, pause_reason: "consecutive_failures" }),
          ],
        },
        now,
      ),
    ).toBe("paused");
    expect(connectionFreshness({ ...base, schedules: [] }, now)).toBe(
      "unscheduled",
    );
  });
});

it("lists schedules and turns one off from its switch", async () => {
  const user = userEvent.setup();
  vi.mocked(listSchedules).mockResolvedValue([
    schedule(),
    schedule({
      id: "sch-2",
      name: "",
      enabled: false,
      pause_reason: "consecutive_failures",
      consecutive_failures: 3,
    }),
  ]);
  vi.mocked(setScheduleEnabled).mockResolvedValue(schedule({ enabled: false }));
  wrap(<SchedulesTab connection={connection} />);

  expect(await screen.findByText("Core network")).toBeInTheDocument();
  expect(screen.getByText("Daily full scan")).toBeInTheDocument();
  expect(
    screen.getByText("Paused · 3 scans in a row failed"),
  ).toBeInTheDocument();
  expect(screen.getAllByText("Daily at 03:12")).toHaveLength(2);
  await user.click(
    screen.getByRole("switch", { name: "Turn Core network on or off" }),
  );
  await waitFor(() =>
    expect(setScheduleEnabled).toHaveBeenCalledWith(
      connection.id,
      "sch-1",
      false,
    ),
  );
});

it("saves an hourly schedule with the chosen time zone and rules", async () => {
  const user = userEvent.setup();
  const onSaved = vi.fn();
  vi.mocked(createSchedule).mockResolvedValue(schedule());
  wrap(
    <ScheduleDialog
      mode={{ kind: "create" }}
      connection={connection}
      onOpenChange={vi.fn()}
      onSaved={onSaved}
    />,
  );
  await user.type(await screen.findByLabelText("Name"), "Singapore");
  await user.click(screen.getByRole("button", { name: "Every few hours" }));
  expect(await screen.findByText("Next 3 runs")).toBeInTheDocument();
  await waitFor(() => expect(previewSchedule).toHaveBeenCalled());
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "Save" })).toBeEnabled(),
  );
  await user.click(screen.getByRole("button", { name: "Save" }));

  await waitFor(() => expect(createSchedule).toHaveBeenCalledOnce());
  const [connectionID, input] = vi.mocked(createSchedule).mock.calls[0];
  expect(connectionID).toBe(connection.id);
  expect(input).toMatchObject({
    name: "Singapore",
    enabled: true,
    scope: { scope_mode: "all_active_regions" },
    frequency: { kind: "hourly", every_hours: 2, time: "03:00" },
    rules: { overlap: "skip", pause_after_failures: 3 },
  });
  expect(input.frequency.timezone).toBeTruthy();
  expect(input.frequency).not.toHaveProperty("weekdays");
  await waitFor(() => expect(onSaved).toHaveBeenCalled());
});

it("shows what changed on modified assets and filters by change type", async () => {
  const user = userEvent.setup();
  vi.mocked(listScanChanges).mockResolvedValue({
    items: [
      {
        id: "chg-1",
        connection_id: connection.id,
        scan_task_id: "scn-1",
        asset_id: "ast-1",
        change_type: "modified",
        resource_kind_id: "kind",
        native_type: "ACS::ECS::Instance",
        native_id: "i-1",
        name: "api-01",
        changed_at: "2026-10-01T00:00:00Z",
        fields: [
          { path: "state", before: "Running", after: "Stopped" },
          { path: "tags.env", before: null, after: "prod" },
          { path: "a", before: 1, after: 2 },
          { path: "b", before: 1, after: 2 },
        ],
      },
    ],
  });
  wrap(
    <ScanChanges
      connectionID={connection.id}
      scanID="scn-1"
      provider="alicloud"
      counts={{ added: 2, removed: 0, modified: 1 }}
    />,
  );
  expect(await screen.findByText("api-01")).toBeInTheDocument();
  const row = screen.getByText("api-01").closest("tr") as HTMLElement;
  expect(within(row).getByText("Running")).toBeInTheDocument();
  expect(within(row).getByText("(none)")).toBeInTheDocument();
  expect(within(row).queryByText("b")).not.toBeInTheDocument();
  await user.click(within(row).getByRole("button", { name: "1 more" }));
  expect(within(row).getByText("b")).toBeInTheDocument();

  await user.click(screen.getByRole("button", { name: /Added 2/ }));
  await waitFor(() =>
    expect(listScanChanges).toHaveBeenLastCalledWith(
      connection.id,
      "scn-1",
      expect.objectContaining({ type: "added" }),
    ),
  );
});
