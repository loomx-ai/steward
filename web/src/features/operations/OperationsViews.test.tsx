import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { listCleanupTasks } from "@/api/client";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import {
  executionTimeline,
  isExecutionActive,
} from "../executions/ExecutionDetail";
import { CleanupView } from "../cleanup/CleanupView";

vi.mock("@/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/client")>()),
  listCleanupTasks: vi.fn(),
}));

vi.mock("@/connections/ActiveConnectionProvider", () => ({
  useRequiredConnection: () => ({
    id: "conn-1",
    name: "Production",
    provider: "alicloud",
  }),
}));

beforeEach(() => {
  localStorage.setItem("steward.locale", "en-US");
  vi.mocked(listCleanupTasks).mockResolvedValue({
    items: [
      {
        id: "cln-1",
        connection_id: "conn-1",
        status: "ready",
        selectors: [],
        resolved_asset_ids: ["asset-1"],
        revision: {
          inventory_revision: "inventory-1",
          graph_revision: "graph-1",
          spec_bundle_revision: "spec-1",
          spec_hash: "hash-1",
        },
        scan_coverage: { status: "complete" },
        snapshot_hash: "snapshot-1",
        blockers: [],
        created_by: "operator",
        created_at: "2026-07-14T00:00:00Z",
      },
    ],
  });
});

function LocationProbe() {
  return <output aria-label="location">{useLocation().pathname}</output>;
}

function renderCleanupTasks() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={["/cleanup"]}>
        <LocaleProvider>
          <CleanupView />
          <LocationProbe />
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

it("polls only active executions and projects a stable timeline", () => {
  expect(isExecutionActive("running")).toBe(true);
  expect(isExecutionActive("reconciling")).toBe(true);
  expect(isExecutionActive("succeeded")).toBe(false);
  expect(executionTimeline("running").map((item) => item.state)).toEqual([
    "complete",
    "active",
    "pending",
  ]);
  expect(executionTimeline("failed").at(-1)?.state).toBe("error");
});

it("uses a native cleanup task link for Enter navigation", async () => {
  const user = userEvent.setup();
  renderCleanupTasks();
  const link = await screen.findByRole("link", { name: "cln-1" });
  link.focus();
  await user.keyboard("{Enter}");
  expect(screen.getByRole("status", { name: "location" })).toHaveTextContent(
    "/cleanup/cln-1",
  );
});

it("does not turn Space on a cleanup task link into row navigation", async () => {
  const user = userEvent.setup();
  renderCleanupTasks();
  const link = await screen.findByRole("link", { name: "cln-1" });
  link.focus();
  await user.keyboard(" ");
  expect(screen.getByRole("status", { name: "location" })).toHaveTextContent(
    "/cleanup",
  );
});
