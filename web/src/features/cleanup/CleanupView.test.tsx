import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { listExecutions, listCleanupTasks } from "@/api/client";
import type { CleanupTask } from "@/api/types";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import { filterCleanupTasks, CleanupView } from "./CleanupView";

vi.mock("@/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/client")>()),
  listExecutions: vi.fn(),
  listCleanupTasks: vi.fn(),
}));

vi.mock("@/connections/ActiveConnectionProvider", () => ({
  useRequiredConnection: () => ({
    id: "connection-a",
    name: "Production",
    provider: "alicloud",
  }),
}));

vi.mock("@/features/panorama/CleanupSelectionContext", () => ({
  useCleanupSelection: () => ({
    hydratedConnectionID: "connection-a",
    targets: [],
    consumeTargets: vi.fn(() => true),
  }),
}));

function cleanupTask(id: string, createdAt: string): CleanupTask {
  return {
    id,
    connection_id: "connection-a",
    status: "completed",
    selectors: [
      {
        kind: "scope",
        scope_id: "region:cn-hangzhou",
        display_name: "Hangzhou",
      },
    ],
    resolved_asset_ids: ["asset-a", "asset-b"],
    revision: {
      inventory_revision: "inventory-1",
      graph_revision: "graph-1",
      spec_bundle_revision: "bundle-1",
      spec_hash: "spec-1",
    },
    scan_coverage: { status: "complete" },
    snapshot_hash: "snapshot-1",
    created_by: "operator",
    created_at: createdAt,
  };
}

beforeEach(() => {
  localStorage.setItem("steward.locale", "en-US");
  Object.defineProperties(Element.prototype, {
    hasPointerCapture: { configurable: true, value: vi.fn(() => false) },
    releasePointerCapture: { configurable: true, value: vi.fn() },
    setPointerCapture: { configurable: true, value: vi.fn() },
    scrollIntoView: { configurable: true, value: vi.fn() },
  });
  vi.mocked(listCleanupTasks).mockResolvedValue({
    items: [cleanupTask("cln-1", "2026-08-03T00:00:00Z")],
  });
  vi.mocked(listExecutions).mockResolvedValue({
    items: [
      {
        id: "execution-1",
        connection_id: "connection-a",
        cleanup_task_id: "cln-1",
        status: "succeeded",
        requested_by: "operator",
        idempotency_key: "key-1",
        created_at: "2026-08-03T00:00:01Z",
        updated_at: "2026-08-03T00:01:32Z",
        duration_ms: 90_000,
        started_at: "2026-08-03T00:00:02Z",
        finished_at: "2026-08-03T00:01:32Z",
      },
    ],
  });
});

it("orders cleanup tasks from newest to oldest without mutating the source", () => {
  const source = [
    cleanupTask("cln-oldest", "2026-07-31T00:00:00Z"),
    cleanupTask("cln-middle", "2026-08-03T00:00:00Z"),
    cleanupTask("cln-newest", "2026-08-04T00:00:00Z"),
  ];

  expect(
    filterCleanupTasks(source, { search: "", status: "__all__" }).map(
      (task) => task.id,
    ),
  ).toEqual(["cln-newest", "cln-middle", "cln-oldest"]);
  expect(source.map((task) => task.id)).toEqual([
    "cln-oldest",
    "cln-middle",
    "cln-newest",
  ]);
});

it("uses the same compact task-table pattern as the scan list", async () => {
  const user = userEvent.setup();
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter>
        <LocaleProvider>
          <CleanupView />
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  expect(await screen.findByRole("link", { name: "cln-1" })).toHaveAttribute(
    "href",
    "/cleanup/cln-1",
  );
  expect(screen.getByRole("columnheader", { name: "Task" })).toBeVisible();
  expect(screen.getByRole("columnheader", { name: "Duration" })).toBeVisible();
  expect(screen.getByText("Hangzhou")).toBeVisible();
  expect(screen.getByText("1m 30s")).toBeVisible();
  expect(
    screen.queryByText("Safe cleanup, one decision at a time"),
  ).not.toBeInTheDocument();

  const search = screen.getByRole("textbox", {
    name: "Search task ID, scope, or requester",
  });
  const statusFilter = screen.getByRole("combobox", {
    name: "Filter by status",
  });
  const newTask = screen.getByRole("button", { name: "New task" });
  const shell = search.closest('[data-slot="data-table-shell"]');
  expect(statusFilter).toHaveClass("sm:w-40");
  expect(shell).toContainElement(statusFilter);
  expect(shell).toContainElement(newTask);
  expect(newTask).toHaveClass("ml-auto");
  expect(document.querySelector('[data-slot="page-layout"]')).toHaveClass(
    "pt-0",
  );

  await user.type(search, "missing-task");
  expect(
    await screen.findByText("No cleanup tasks match the current filters."),
  ).toBeVisible();

  await user.clear(search);
  expect(await screen.findByRole("link", { name: "cln-1" })).toBeVisible();
  await user.click(statusFilter);
  await user.click(screen.getByRole("option", { name: "Draft" }));
  expect(
    await screen.findByText("No cleanup tasks match the current filters."),
  ).toBeVisible();
});

it("summarizes a single unnamed target without exposing its internal ID", async () => {
  const task = cleanupTask("cln-unnamed", "2026-08-03T00:00:00Z");
  task.selectors = [{ kind: "asset", asset_id: "ast-internal-id" }];
  task.resolved_asset_ids = ["ast-internal-id"];
  vi.mocked(listCleanupTasks).mockResolvedValue({ items: [task] });
  vi.mocked(listExecutions).mockResolvedValue({ items: [] });

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter>
        <LocaleProvider>
          <CleanupView />
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  expect(await screen.findByText("1 selected targets")).toBeVisible();
  expect(screen.queryByText("ast-internal-id")).not.toBeInTheDocument();
});

it("opens new cleanup task in the shared centered dialog and restores focus", async () => {
  const user = userEvent.setup();
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter initialEntries={["/cleanup"]}>
        <LocaleProvider>
          <CleanupView />
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  const trigger = await screen.findByRole("button", { name: "New task" });
  await user.click(trigger);

  const dialog = screen.getByRole("dialog", { name: "New cleanup task" });
  expect(dialog).toHaveClass("sm:max-w-3xl");
  expect(dialog).not.toHaveAttribute("aria-describedby");
  expect(
    screen.queryByText(/server resolves dependencies and lifecycle ownership/),
  ).not.toBeInTheDocument();
  expect(dialog).toContainElement(
    screen.getByRole("button", { name: "Cancel" }),
  );
  expect(screen.getByRole("button", { name: "Confirm" })).toBeDisabled();

  await user.click(screen.getByRole("button", { name: "Cancel" }));
  await waitFor(() =>
    expect(
      screen.queryByRole("dialog", { name: "New cleanup task" }),
    ).not.toBeInTheDocument(),
  );
  expect(trigger).toHaveFocus();
});
