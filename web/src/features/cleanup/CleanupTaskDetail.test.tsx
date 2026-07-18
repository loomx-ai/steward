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
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  APIRequestError,
  addCleanupTaskAssets,
  continueCleanupExecution,
  createExecution,
  findAssets,
  getCleanupTaskLogs,
  getCleanupTask,
  listExecutionActions,
  listCleanupTaskExecutions,
  listConnectionRegions,
  listProviderCatalog,
  pauseCleanupExecution,
  resumeCleanupExecution,
} from "@/api/client";
import type { ActionAttempt } from "@/api/types";
import { WorkspaceProvider } from "@/app/WorkspaceContext";
import { InspectorSheet } from "@/components/patterns/InspectorSheet";
import { TooltipProvider } from "@/components/ui/tooltip";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import {
  CleanupTaskDetail,
  cleanupActionDisplayStatus,
  cleanupExecutionBlockedByFailure,
  cleanupResourceRows,
  shouldPollCleanupActions,
} from "./CleanupTaskDetail";
import { readCleanupSelectionHandoff } from "./selection";

vi.mock("@/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/client")>()),
  addCleanupTaskAssets: vi.fn(),
  continueCleanupExecution: vi.fn(),
  createExecution: vi.fn(),
  findAssets: vi.fn(),
  getCleanupTaskLogs: vi.fn(),
  getCleanupTask: vi.fn(),
  listExecutionActions: vi.fn(),
  listCleanupTaskExecutions: vi.fn(),
  listConnectionRegions: vi.fn(),
  listProviderCatalog: vi.fn(),
  pauseCleanupExecution: vi.fn(),
  resumeCleanupExecution: vi.fn(),
}));

vi.mock("@/connections/ActiveConnectionProvider", () => ({
  useRequiredConnection: () => ({
    id: "connection-a",
    name: "Production",
    provider: "alicloud",
  }),
}));

it("recognizes an execution whose active resources only wait on failed dependencies", () => {
  const steps = [
    { id: "step-image" },
    { id: "step-snapshot", depends_on: ["step-image"] },
  ];
  expect(
    cleanupExecutionBlockedByFailure(steps, [
      { cleanup_task_step_id: "step-image", status: "failed" },
      { cleanup_task_step_id: "step-snapshot", status: "invoking" },
    ]),
  ).toBe(true);
  expect(
    cleanupExecutionBlockedByFailure(
      [...steps, { id: "step-independent" }],
      [
        { cleanup_task_step_id: "step-image", status: "failed" },
        { cleanup_task_step_id: "step-snapshot", status: "invoking" },
        { cleanup_task_step_id: "step-independent", status: "invoking" },
      ],
    ),
  ).toBe(false);
});

it.each([
  [undefined, undefined, "waiting"],
  [{ status: "pending" }, "pending", "waiting"],
  [{ status: "intent_persisted" }, "running", "in_progress"],
  [{ status: "invoking" }, "running", "in_progress"],
  [{ status: "waiting" }, "waiting", "in_progress"],
  [{ status: "reading_back" }, "running", "in_progress"],
  [{ status: "reconciling" }, "reconciling", "in_progress"],
  [{ status: "waiting" }, "failed", "in_progress"],
  [{ status: "pending" }, "failed", "waiting"],
  [{ status: "invoking" }, "pausing", "pausing"],
  [{ status: "waiting" }, "paused", "paused"],
  [{ status: "succeeded" }, "succeeded", "succeeded"],
  [{ status: "failed" }, "failed", "failed"],
  [
    { status: "skipped", skip_reason: "product_unsupported" },
    "succeeded",
    "not_actionable",
  ],
] as const)(
  "maps cleanup action %j in execution %s to resource status %s",
  (action, executionStatus, expected) => {
    expect(cleanupActionDisplayStatus(action, executionStatus)).toBe(expected);
  },
);

it("keeps polling independent resource actions after a peer fails", () => {
  expect(shouldPollCleanupActions("paused", [cleanupAction("waiting")])).toBe(
    false,
  );
  expect(
    shouldPollCleanupActions("failed", [
      cleanupAction("failed"),
      cleanupAction("waiting"),
    ]),
  ).toBe(true);
  expect(
    shouldPollCleanupActions("failed", [
      cleanupAction("failed"),
      cleanupAction("succeeded"),
    ]),
  ).toBe(false);
});

function cleanupAction(status: string) {
  return { status } as ActionAttempt;
}

it("shows public-image preparation as a reason without changing the delete action", () => {
  const rows = cleanupResourceRows(
    [
      {
        id: "step-image",
        asset_id: "image-a",
        kind: "direct",
        action: "delete",
      },
    ],
    [],
    [],
    [
      {
        code: "pre_delete_image_visibility_change",
        asset_id: "image-a",
        message:
          "public custom image will be changed to private before deletion",
      },
    ],
    [],
  );

  expect(rows).toHaveLength(1);
  expect(rows[0]).toMatchObject({
    assetID: "image-a",
    action: "delete",
    status: "waiting",
    warning: { code: "pre_delete_image_visibility_change" },
  });
});

function CleanupTaskDetailHarness() {
  return (
    <TooltipProvider>
      <WorkspaceProvider>
        <CleanupTaskDetail />
        <InspectorSheet />
      </WorkspaceProvider>
    </TooltipProvider>
  );
}

function LocationProbe() {
  const location = useLocation();
  return (
    <output data-testid="location">
      {location.pathname}
      {location.search}
    </output>
  );
}

beforeEach(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class ResizeObserver {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  Object.defineProperties(Element.prototype, {
    hasPointerCapture: { configurable: true, value: vi.fn(() => false) },
    releasePointerCapture: { configurable: true, value: vi.fn() },
    setPointerCapture: { configurable: true, value: vi.fn() },
    scrollIntoView: { configurable: true, value: vi.fn() },
  });
  localStorage.setItem("steward.locale", "en-US");
  sessionStorage.clear();
  vi.mocked(addCleanupTaskAssets).mockReset();
  vi.mocked(continueCleanupExecution).mockReset();
  vi.mocked(createExecution).mockReset();
  vi.mocked(findAssets).mockReset();
  vi.mocked(getCleanupTaskLogs).mockReset();
  vi.mocked(getCleanupTask).mockReset();
  vi.mocked(listExecutionActions).mockReset();
  vi.mocked(listCleanupTaskExecutions).mockReset();
  vi.mocked(listConnectionRegions).mockReset().mockResolvedValue({ items: [] });
  vi.mocked(listProviderCatalog).mockReset();
  vi.mocked(pauseCleanupExecution).mockReset();
  vi.mocked(resumeCleanupExecution).mockReset();
  vi.mocked(listProviderCatalog).mockResolvedValue([
    {
      provider: "alicloud",
      specs: [],
      revision: "catalog-1",
      hash: "catalog-hash",
      kinds_revision: "catalog-1",
      kinds: [
        {
          id: "kind-instance",
          provider: "alicloud",
          native_type: "ACS::ECS::Instance",
          capabilities: [],
          display_name: "ECS Instance",
          display_names: {
            "en-US": "ECS Instance",
            "zh-CN": "ECS 实例",
          },
          console_link_template:
            "https://ecs.console.aliyun.com/server/{regionId}?instanceId={nativeId}",
          bundle_revision: "catalog-1",
        },
        {
          id: "kind-stack",
          provider: "alicloud",
          native_type: "ALIYUN::ROS::Stack",
          capabilities: [],
          display_name: "ROS Stack",
          display_names: {
            "en-US": "ROS Stack",
            "zh-CN": "ROS 资源栈",
          },
          console_link_template:
            "https://ros.console.aliyun.com/{regionId}/stacks/{nativeId}",
          bundle_revision: "catalog-1",
        },
        {
          id: "kind-cen-vpc-attachment",
          provider: "alicloud",
          native_type: "ACS::CEN::TransitRouterVpcAttachment",
          capabilities: [],
          display_name: "CEN intra-region connection",
          display_names: {
            "en-US": "CEN intra-region connection",
            "zh-CN": "CEN 地域内连接",
          },
          bundle_revision: "catalog-1",
        },
        {
          id: "kind-transit-router",
          provider: "alicloud",
          native_type: "ACS::CEN::TransitRouter",
          capabilities: [],
          display_name: "Transit router",
          display_names: {
            "en-US": "Transit router",
            "zh-CN": "转发路由器",
          },
          bundle_revision: "catalog-1",
        },
        {
          id: "kind-nat-gateway",
          provider: "alicloud",
          native_type: "ACS::NAT::NatGateway",
          capabilities: [],
          display_name: "NAT Gateway",
          display_names: {
            "en-US": "NAT Gateway",
            "zh-CN": "NAT 网关",
          },
          bundle_revision: "catalog-1",
        },
      ],
    },
  ]);
  vi.mocked(getCleanupTaskLogs).mockResolvedValue({ items: [] });
  vi.mocked(findAssets).mockResolvedValue([
    {
      id: "external-instance",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::ECS::Instance",
        native_id: "i-002",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-instance",
      current_observation_id: "observation-instance",
      name: "external-instance",
      state: "Running",
      tags: {},
      capabilities: ["indexed", "actionable"],
      normalized: {},
      first_seen_at: "2026-08-03T00:00:00Z",
      last_seen_at: "2026-08-03T00:00:00Z",
    },
  ]);
  vi.mocked(getCleanupTask).mockResolvedValue({
    task: {
      id: "cln-1",
      connection_id: "connection-a",
      status: "draft",
      selectors: [{ kind: "asset", asset_id: "stack" }],
      resolved_asset_ids: ["stack"],
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "not_required" },
      snapshot_hash: "snapshot-1",
      warnings: [
        {
          code: "managed_resource_direct_cleanup",
          asset_id: "stack-child",
          controller_id: "stack",
          message: "fallback",
        },
      ],
      blockers: [
        {
          code: "cross_scope_dependency",
          asset_id: "stack-vswitch",
          message: "blocked",
          evidence: {
            dependent_asset_id: "external-instance",
            relationship_type: "member_of",
          },
        },
        {
          code: "cross_scope_dependency",
          asset_id: "stack-vpc",
          message: "blocked",
          evidence: {
            dependent_asset_id: "external-instance",
            relationship_type: "member_of",
          },
        },
      ],
      created_by: "operator",
      created_at: "2026-08-03T00:00:00Z",
    },
    steps: [],
    impact_items: [],
  });
});

afterEach(() => {
  vi.useRealTimers();
});

it("deep-links cleanup task tabs and keeps tab changes in the URL", async () => {
  const user = userEvent.setup();

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter initialEntries={["/cleanup/cln-1?tab=logs"]}>
        <LocaleProvider>
          <Routes>
            <Route
              path="/cleanup/:id"
              element={
                <>
                  <CleanupTaskDetailHarness />
                  <LocationProbe />
                </>
              }
            />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  expect(
    await screen.findByRole("tab", { name: "Cleanup logs" }),
  ).toHaveAttribute("aria-selected", "true");
  expect(screen.getByTestId("location")).toHaveTextContent(
    "/cleanup/cln-1?tab=logs",
  );

  await user.click(screen.getByRole("tab", { name: "Resource results" }));
  expect(screen.getByTestId("location")).toHaveTextContent(
    "/cleanup/cln-1?tab=resources",
  );

  await user.click(screen.getByRole("tab", { name: "Cleanup targets" }));
  expect(screen.getByTestId("location")).toHaveTextContent("/cleanup/cln-1");
});

it("sets bounded concurrency when starting cleanup", async () => {
  const user = userEvent.setup();
  vi.mocked(getCleanupTask).mockResolvedValue({
    task: {
      id: "cln-ready",
      connection_id: "connection-a",
      status: "ready",
      selectors: [{ kind: "asset", asset_id: "direct" }],
      resolved_asset_ids: ["direct"],
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "complete" },
      snapshot_hash: "snapshot-ready",
      created_by: "operator",
      created_at: "2026-08-10T00:00:00Z",
    },
    steps: [
      {
        id: "step-direct",
        cleanup_task_id: "cln-ready",
        asset_id: "direct",
        kind: "direct",
        action: "delete",
      },
    ],
    impact_items: [],
  });
  vi.mocked(listCleanupTaskExecutions).mockResolvedValue({ items: [] });
  vi.mocked(createExecution).mockResolvedValue({
    id: "execution-ready",
    connection_id: "connection-a",
    cleanup_task_id: "cln-ready",
    status: "pending",
    concurrency: 35,
    requested_by: "operator",
    idempotency_key: "execute-ready",
    created_at: "2026-08-10T00:00:01Z",
  });

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter initialEntries={["/cleanup/cln-ready"]}>
        <LocaleProvider>
          <Routes>
            <Route path="/cleanup/:id" element={<CleanupTaskDetailHarness />} />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  await user.click(
    await screen.findByRole("button", { name: "Start cleanup" }),
  );
  const dialog = screen.getByRole("alertdialog");
  const concurrency = within(dialog).getByRole("spinbutton", {
    name: "Concurrency",
  });
  expect(concurrency).toHaveValue(20);
  await user.clear(concurrency);
  await user.type(concurrency, "35");
  await user.click(
    within(dialog).getByLabelText(
      "I have reviewed this cleanup task and its impacts.",
    ),
  );
  await user.click(
    within(dialog).getByRole("button", { name: "Start cleanup" }),
  );

  await waitFor(() =>
    expect(createExecution).toHaveBeenCalledWith(
      "connection-a",
      "cln-ready",
      expect.any(String),
      35,
      { typed_names: {}, acknowledged: true },
    ),
  );
});

it("pauses and resumes an executing cleanup task", async () => {
  const user = userEvent.setup();
  vi.mocked(getCleanupTask).mockResolvedValue({
    task: {
      id: "cln-running",
      connection_id: "connection-a",
      status: "executing",
      selectors: [{ kind: "asset", asset_id: "direct" }],
      resolved_asset_ids: ["direct"],
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "complete" },
      snapshot_hash: "snapshot-running",
      created_by: "operator",
      created_at: "2026-08-11T00:00:00Z",
    },
    steps: [
      {
        id: "step-direct",
        cleanup_task_id: "cln-running",
        asset_id: "direct",
        kind: "direct",
        action: "delete",
      },
    ],
    impact_items: [],
  });
  const runningExecution = {
    id: "execution-running",
    connection_id: "connection-a",
    cleanup_task_id: "cln-running",
    status: "running",
    concurrency: 20,
    requested_by: "operator",
    idempotency_key: "execution-running-key",
    created_at: "2026-08-11T00:00:01Z",
  };
  vi.mocked(listCleanupTaskExecutions).mockResolvedValue({
    items: [runningExecution],
  });
  vi.mocked(listExecutionActions).mockResolvedValue({ items: [] });
  vi.mocked(pauseCleanupExecution).mockResolvedValue({
    ...runningExecution,
    status: "pausing",
  });
  vi.mocked(resumeCleanupExecution).mockResolvedValue(runningExecution);

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter initialEntries={["/cleanup/cln-running"]}>
        <LocaleProvider>
          <Routes>
            <Route path="/cleanup/:id" element={<CleanupTaskDetailHarness />} />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  await user.click(await screen.findByRole("button", { name: "Pause" }));
  await waitFor(() =>
    expect(pauseCleanupExecution).toHaveBeenCalledWith(
      "connection-a",
      "cln-running",
    ),
  );
  await user.click(await screen.findByRole("button", { name: "Resume" }));
  await waitFor(() =>
    expect(resumeCleanupExecution).toHaveBeenCalledWith(
      "connection-a",
      "cln-running",
    ),
  );
  expect(await screen.findByRole("button", { name: "Pause" })).toBeVisible();
});

it("refreshes a cleanup task when execution discovers that it is invalidated", async () => {
  const user = userEvent.setup();
  const ready = {
    task: {
      id: "cln-stale",
      connection_id: "connection-a",
      status: "ready" as const,
      selectors: [{ kind: "asset" as const, asset_id: "direct" }],
      resolved_asset_ids: ["direct"],
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "complete" as const },
      snapshot_hash: "snapshot-ready",
      created_by: "operator",
      created_at: "2026-08-10T00:00:00Z",
    },
    steps: [
      {
        id: "step-direct",
        cleanup_task_id: "cln-stale",
        asset_id: "direct",
        kind: "direct" as const,
        action: "delete",
      },
    ],
    impact_items: [],
  };
  vi.mocked(getCleanupTask)
    .mockResolvedValueOnce(ready)
    .mockResolvedValueOnce({
      ...ready,
      task: {
        ...ready.task,
        status: "invalidated",
        invalidation_reason: "the selected resource is already absent",
      },
    });
  vi.mocked(listCleanupTaskExecutions).mockResolvedValue({ items: [] });
  vi.mocked(createExecution).mockRejectedValue(
    new APIRequestError(
      "cleanup task snapshot is no longer current",
      "cleanup.invalidated",
      undefined,
      "request-stale",
    ),
  );

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter initialEntries={["/cleanup/cln-stale"]}>
        <LocaleProvider>
          <Routes>
            <Route path="/cleanup/:id" element={<CleanupTaskDetailHarness />} />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  await user.click(
    await screen.findByRole("button", { name: "Start cleanup" }),
  );
  const dialog = screen.getByRole("alertdialog");
  await user.click(
    within(dialog).getByLabelText(
      "I have reviewed this cleanup task and its impacts.",
    ),
  );
  await user.click(
    within(dialog).getByRole("button", { name: "Start cleanup" }),
  );

  expect(
    await screen.findByText(
      /This cleanup task is no longer current.*Request ID: request-stale/,
    ),
  ).toBeVisible();
  await waitFor(() => expect(getCleanupTask).toHaveBeenCalledTimes(2));
  expect(
    screen.queryByRole("button", { name: "Start cleanup" }),
  ).not.toBeInTheDocument();
  expect(screen.getByText("Invalidated")).toBeVisible();
});

it("continues a failed cleanup even while a resource action is active", async () => {
  const user = userEvent.setup();
  vi.mocked(getCleanupTask).mockResolvedValue({
    task: {
      id: "cln-failed",
      connection_id: "connection-a",
      status: "failed",
      selectors: [{ kind: "asset", asset_id: "asset-a" }],
      resolved_asset_ids: ["asset-a"],
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "complete" },
      snapshot_hash: "snapshot-failed",
      created_by: "operator",
      created_at: "2026-08-03T00:00:00Z",
    },
    steps: [
      {
        id: "step-a",
        cleanup_task_id: "cln-failed",
        asset_id: "asset-a",
        kind: "direct",
        action: "delete",
      },
    ],
    impact_items: [],
  });
  const failedExecution = {
    id: "execution-failed",
    connection_id: "connection-a",
    cleanup_task_id: "cln-failed",
    status: "failed",
    requested_by: "operator",
    idempotency_key: "key-failed",
    created_at: "2026-08-03T00:00:01Z",
  };
  vi.mocked(listCleanupTaskExecutions).mockResolvedValue({
    items: [failedExecution],
  });
  vi.mocked(listExecutionActions).mockResolvedValue({
    items: [
      {
        id: "action-failed",
        execution_id: "execution-failed",
        cleanup_task_step_id: "step-a",
        asset_id: "asset-a",
        action: "delete",
        status: "waiting",
        idempotency_key: "key-action",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
        created_at: "2026-08-03T00:00:02Z",
        updated_at: "2026-08-03T00:00:03Z",
      },
      {
        id: "action-vpc",
        execution_id: "execution-vpc-waiting",
        cleanup_task_step_id: "step-vpc",
        asset_id: "vpc",
        action: "delete",
        status: "invoking",
        idempotency_key: "action-vpc",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
        created_at: "2026-08-03T00:00:02Z",
        updated_at: "2026-08-03T00:00:03Z",
      },
    ],
  });
  vi.mocked(continueCleanupExecution).mockResolvedValue({
    ...failedExecution,
    status: "running",
    updated_at: "2026-08-03T00:00:04Z",
  });

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter initialEntries={["/cleanup/cln-failed"]}>
        <LocaleProvider>
          <Routes>
            <Route path="/cleanup/:id" element={<CleanupTaskDetailHarness />} />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  await user.click(await screen.findByRole("button", { name: "Continue" }));
  const dialog = screen.getByRole("alertdialog");
  expect(
    within(dialog).getByText(
      "Failed resources will be retried and resources that have not started will continue. Completed resources will not run again.",
    ),
  ).toBeVisible();
  const concurrency = within(dialog).getByRole("spinbutton", {
    name: "Concurrency",
  });
  expect(concurrency).toHaveValue(20);
  await user.clear(concurrency);
  await user.type(concurrency, "200000");
  expect(concurrency).toHaveValue(100);
  await user.clear(concurrency);
  await user.type(concurrency, "0");
  expect(concurrency).toHaveValue(1);
  await user.clear(concurrency);
  await user.type(concurrency, "37");
  await user.click(within(dialog).getByRole("button", { name: "Continue" }));

  await waitFor(() =>
    expect(continueCleanupExecution).toHaveBeenCalledWith(
      "connection-a",
      "cln-failed",
      expect.any(String),
      37,
    ),
  );
  expect(
    screen.queryByRole("button", { name: "Continue" }),
  ).not.toBeInTheDocument();
});

it("shows the exact scheduled KMS key deletion time as a success reason", async () => {
  const user = userEvent.setup();
  localStorage.setItem("steward.locale", "zh-CN");
  vi.mocked(getCleanupTask).mockResolvedValue({
    task: {
      id: "cln-kms-scheduled",
      connection_id: "connection-a",
      status: "completed",
      selectors: [{ kind: "asset", asset_id: "kms-key" }],
      resolved_asset_ids: ["kms-key"],
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "complete" },
      snapshot_hash: "snapshot-kms",
      created_by: "operator",
      created_at: "2026-08-06T00:00:00Z",
    },
    steps: [
      {
        id: "step-kms",
        cleanup_task_id: "cln-kms-scheduled",
        asset_id: "kms-key",
        kind: "direct",
        action: "delete",
      },
    ],
    impact_items: [],
  });
  vi.mocked(listCleanupTaskExecutions).mockResolvedValue({
    items: [
      {
        id: "execution-kms",
        connection_id: "connection-a",
        cleanup_task_id: "cln-kms-scheduled",
        status: "succeeded",
        requested_by: "operator",
        idempotency_key: "key-kms",
        created_at: "2026-08-06T00:00:01Z",
      },
    ],
  });
  vi.mocked(listExecutionActions).mockResolvedValue({
    items: [
      {
        id: "action-kms",
        execution_id: "execution-kms",
        cleanup_task_step_id: "step-kms",
        asset_id: "kms-key",
        action: "delete",
        status: "succeeded",
        idempotency_key: "action-key-kms",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
        readback: {
          exists: false,
          state: "scheduled_deletion",
          data: { pending_window_days: 7 },
        },
        created_at: "2026-08-06T00:00:02Z",
        updated_at: "2026-08-06T00:00:03Z",
      },
    ],
  });
  vi.mocked(findAssets).mockResolvedValue([
    {
      id: "kms-key",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::KMS::Key",
        native_id: "815cb574-fa98-44e5-a090-98e0f9c21514",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-kms-key",
      name: "815cb574-fa98-44e5-a090-98e0f9c21514",
      capabilities: ["indexed", "actionable"],
      normalized: {
        configuration: { DeleteDate: "2026-08-13T11:54:34Z" },
      },
      first_seen_at: "2026-08-06T00:00:00Z",
      last_seen_at: "2026-08-06T00:00:00Z",
    },
  ]);

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter initialEntries={["/cleanup/cln-kms-scheduled"]}>
        <LocaleProvider>
          <Routes>
            <Route path="/cleanup/:id" element={<CleanupTaskDetailHarness />} />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  await user.click(await screen.findByRole("tab", { name: "资源结果" }));
  expect(
    await screen.findByText(
      "已处于计划删除，将于 2026-08-13 19:54:34 删除。",
      {},
      { timeout: 5000 },
    ),
  ).toBeVisible();
});

it("shows a service-managed KMS key as skipped with a Chinese reason", async () => {
  const user = userEvent.setup();
  localStorage.setItem("steward.locale", "zh-CN");
  vi.mocked(getCleanupTask).mockResolvedValue({
    task: {
      id: "cln-kms-managed",
      connection_id: "connection-a",
      status: "completed",
      selectors: [{ kind: "asset", asset_id: "kms-managed-key" }],
      resolved_asset_ids: ["kms-managed-key"],
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "complete" },
      snapshot_hash: "snapshot-kms-managed",
      created_by: "operator",
      created_at: "2026-08-06T00:00:00Z",
    },
    steps: [
      {
        id: "step-kms-managed",
        cleanup_task_id: "cln-kms-managed",
        asset_id: "kms-managed-key",
        kind: "direct",
        action: "delete",
      },
    ],
    impact_items: [],
  });
  vi.mocked(listCleanupTaskExecutions).mockResolvedValue({
    items: [
      {
        id: "execution-kms-managed",
        connection_id: "connection-a",
        cleanup_task_id: "cln-kms-managed",
        status: "succeeded",
        requested_by: "operator",
        idempotency_key: "key-kms-managed",
        created_at: "2026-08-06T00:00:01Z",
      },
    ],
  });
  vi.mocked(listExecutionActions).mockResolvedValue({
    items: [
      {
        id: "action-kms-managed",
        execution_id: "execution-kms-managed",
        cleanup_task_step_id: "step-kms-managed",
        asset_id: "kms-managed-key",
        action: "delete",
        status: "skipped",
        skip_reason: "product_unsupported",
        idempotency_key: "action-key-kms-managed",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
        provider_error: {
          category: "unsupported",
          code: "CleanupUnsupported.ServiceManagedKMSKey",
          message:
            "the KMS key is managed by the Ecs cloud service and cannot be deleted directly",
          summary: {
            creator: "Ecs",
            skip_reason: "product_unsupported",
          },
        },
        created_at: "2026-08-06T00:00:02Z",
        updated_at: "2026-08-06T00:00:03Z",
      },
    ],
  });
  vi.mocked(findAssets).mockResolvedValue([
    {
      id: "kms-managed-key",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::KMS::Key",
        native_id: "786bd86b-c782-4378-8a5f-16d7161daea5",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-kms-key",
      name: "ECS 托管密钥",
      normalized: {
        _service_managed: true,
        _service_creator: "Ecs",
        configuration: { Creator: "Ecs" },
      },
      capabilities: ["indexed", "actionable"],
      first_seen_at: "2026-08-06T00:00:00Z",
      last_seen_at: "2026-08-06T00:00:00Z",
    },
  ]);

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter initialEntries={["/cleanup/cln-kms-managed"]}>
        <LocaleProvider>
          <Routes>
            <Route path="/cleanup/:id" element={<CleanupTaskDetailHarness />} />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  await user.click(await screen.findByRole("tab", { name: "资源结果" }));
  const resourceID = await screen.findByText(
    "786bd86b-c782-4378-8a5f-16d7161daea5",
  );
  const row = resourceID.closest("tr");
  expect(row).not.toBeNull();
  const copyResourceID = within(row!).getByRole("button", {
    name: "复制资源 ID",
  });
  expect(copyResourceID).toHaveClass(
    "opacity-0",
    "group-hover:opacity-100",
    "focus-visible:opacity-100",
  );
  const resourceIDLine = copyResourceID.parentElement;
  expect(resourceIDLine).not.toBeNull();
  const managedBadge = within(resourceIDLine!).getByText("托管资源");
  expect(managedBadge).toBeVisible();
  expect(
    resourceID.compareDocumentPosition(managedBadge) &
      Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy();
  expect(
    managedBadge.compareDocumentPosition(copyResourceID) &
      Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy();
  expect(within(row!).getByText("跳过")).toBeVisible();
  expect(
    within(row!).getByText("该 KMS 密钥由云服务托管，不能直接删除，已跳过。"),
  ).toBeVisible();
});

it("stops polling actions and assets after every resource action is terminal", async () => {
  vi.useFakeTimers();
  vi.mocked(getCleanupTask).mockResolvedValue({
    task: {
      id: "cln-terminal",
      connection_id: "connection-a",
      status: "failed",
      selectors: [{ kind: "asset", asset_id: "asset-a" }],
      resolved_asset_ids: ["asset-a"],
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "complete" },
      snapshot_hash: "snapshot-1",
      created_by: "operator",
      created_at: "2026-08-03T00:00:00Z",
    },
    steps: [
      {
        id: "step-a",
        cleanup_task_id: "cln-terminal",
        asset_id: "asset-a",
        kind: "direct",
        action: "delete",
      },
    ],
    impact_items: [],
  });
  vi.mocked(listCleanupTaskExecutions).mockResolvedValue({
    items: [
      {
        id: "execution-terminal",
        connection_id: "connection-a",
        cleanup_task_id: "cln-terminal",
        status: "failed",
        requested_by: "operator",
        idempotency_key: "key-terminal",
        created_at: "2026-08-03T00:00:01Z",
      },
    ],
  });
  vi.mocked(listExecutionActions).mockResolvedValue({
    items: [
      {
        id: "action-terminal",
        execution_id: "execution-terminal",
        cleanup_task_step_id: "step-a",
        asset_id: "asset-a",
        action: "delete",
        status: "failed",
        idempotency_key: "key-action",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
        created_at: "2026-08-03T00:00:02Z",
        updated_at: "2026-08-03T00:00:03Z",
      },
    ],
  });
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });

  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={["/cleanup/cln-terminal"]}>
        <LocaleProvider>
          <Routes>
            <Route path="/cleanup/:id" element={<CleanupTaskDetailHarness />} />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  await act(async () => {
    for (let index = 0; index < 5; index += 1) {
      await vi.advanceTimersByTimeAsync(1);
    }
  });
  expect(listExecutionActions).toHaveBeenCalledWith(
    "connection-a",
    "execution-terminal",
  );
  expect(findAssets).toHaveBeenCalledTimes(1);
  expect(screen.getByText("Execution result")).toBeVisible();
  expect(
    screen.getByRole("img", { name: "0 succeeded, 1 failed" }),
  ).toBeVisible();
  expect(screen.getByText("0 succeeded")).toBeVisible();
  expect(screen.getByText("1 failed")).toBeVisible();
  expect(screen.queryByText("Overall progress")).not.toBeInTheDocument();
  expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
  const failedSegment = document.querySelector<HTMLElement>(
    '[data-status-segment="failed"]',
  );
  expect(failedSegment).not.toBeNull();
  fireEvent.pointerMove(failedSegment!, { pointerType: "mouse" });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(screen.getByRole("tooltip")).toHaveTextContent(/1 failed.*100%/);

  await act(async () => {
    await vi.advanceTimersByTimeAsync(2100);
  });
  expect(listExecutionActions).toHaveBeenCalledTimes(1);
  expect(findAssets).toHaveBeenCalledTimes(1);
});

it("settles stale resource actions after the task completes before its execution cache", async () => {
  vi.useFakeTimers();
  vi.mocked(getCleanupTask).mockResolvedValue({
    task: {
      id: "cln-completed",
      connection_id: "connection-a",
      status: "completed",
      selectors: [{ kind: "asset", asset_id: "asset-a" }],
      resolved_asset_ids: ["asset-a"],
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "complete" },
      snapshot_hash: "snapshot-completed",
      created_by: "operator",
      created_at: "2026-08-03T00:00:00Z",
    },
    steps: [
      {
        id: "step-a",
        cleanup_task_id: "cln-completed",
        asset_id: "asset-a",
        kind: "direct",
        action: "delete",
      },
    ],
    impact_items: [],
  });
  const runningExecution = {
    id: "execution-completed",
    connection_id: "connection-a",
    cleanup_task_id: "cln-completed",
    status: "running",
    requested_by: "operator",
    idempotency_key: "key-completed",
    created_at: "2026-08-03T00:00:01Z",
    updated_at: "2026-08-03T00:00:02Z",
  };
  vi.mocked(listCleanupTaskExecutions)
    .mockResolvedValueOnce({ items: [runningExecution] })
    .mockResolvedValue({
      items: [
        {
          ...runningExecution,
          status: "succeeded",
          updated_at: "2026-08-03T00:00:04Z",
          finished_at: "2026-08-03T00:00:04Z",
        },
      ],
    });
  const waitingAction = {
    id: "action-completed",
    execution_id: "execution-completed",
    cleanup_task_step_id: "step-a",
    asset_id: "asset-a",
    action: "delete",
    status: "reading_back",
    idempotency_key: "key-action",
    spec_bundle_revision: "bundle-1",
    spec_hash: "spec-1",
    created_at: "2026-08-03T00:00:02Z",
    updated_at: "2026-08-03T00:00:03Z",
  };
  vi.mocked(listExecutionActions)
    .mockResolvedValueOnce({ items: [waitingAction] })
    .mockResolvedValueOnce({ items: [waitingAction] })
    .mockResolvedValue({
      items: [
        {
          ...waitingAction,
          status: "succeeded",
          updated_at: "2026-08-03T00:00:04Z",
          finished_at: "2026-08-03T00:00:04Z",
        },
      ],
    });

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter initialEntries={["/cleanup/cln-completed"]}>
        <LocaleProvider>
          <Routes>
            <Route path="/cleanup/:id" element={<CleanupTaskDetailHarness />} />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  await act(async () => {
    for (let index = 0; index < 5; index += 1) {
      await vi.advanceTimersByTimeAsync(1);
    }
  });
  expect(screen.getByText("1 in progress")).toBeVisible();
  expect(listCleanupTaskExecutions).toHaveBeenCalledTimes(1);

  await act(async () => {
    await vi.advanceTimersByTimeAsync(2100);
  });
  expect(listExecutionActions).toHaveBeenCalledTimes(2);
  expect(listCleanupTaskExecutions).toHaveBeenCalledTimes(1);

  await act(async () => {
    await vi.advanceTimersByTimeAsync(500);
  });
  expect(listCleanupTaskExecutions).toHaveBeenCalledTimes(2);
  expect(listExecutionActions).toHaveBeenCalledTimes(3);
  expect(screen.queryByText("0 in progress")).not.toBeInTheDocument();
  expect(screen.getByText("1 succeeded")).toBeVisible();
  expect(screen.getByText("Succeeded")).toBeVisible();
});

it("identifies a dirty resource that will be ignored when cleanup starts", async () => {
  const user = userEvent.setup();
  vi.mocked(getCleanupTask).mockResolvedValue({
    task: {
      id: "cln-dirty",
      connection_id: "connection-a",
      status: "ready",
      selectors: [{ kind: "asset", asset_id: "asset-dirty" }],
      resolved_asset_ids: ["asset-dirty"],
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "complete" },
      snapshot_hash: "snapshot-dirty",
      created_by: "operator",
      created_at: "2026-08-05T00:00:00Z",
    },
    steps: [
      {
        id: "step-dirty",
        cleanup_task_id: "cln-dirty",
        asset_id: "asset-dirty",
        kind: "direct",
        action: "delete",
      },
    ],
    impact_items: [],
  });
  vi.mocked(findAssets).mockResolvedValue([
    {
      id: "asset-dirty",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::ECS::NetworkInterface",
        native_id: "eni-dirty",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-instance",
      name: "eni-dirty",
      dirty: true,
      capabilities: ["indexed", "actionable"],
      first_seen_at: "2026-08-05T00:00:00Z",
      last_seen_at: "2026-08-05T00:00:00Z",
    },
  ]);
  vi.mocked(listCleanupTaskExecutions).mockResolvedValue({ items: [] });
  vi.mocked(listExecutionActions).mockResolvedValue({ items: [] });

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter initialEntries={["/cleanup/cln-dirty"]}>
        <LocaleProvider>
          <Routes>
            <Route path="/cleanup/:id" element={<CleanupTaskDetailHarness />} />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  await user.click(
    await screen.findByRole("tab", { name: "Resource results" }),
  );
  expect(
    await screen.findByText(/Dirty resource.*Ignored during cleanup/),
  ).toBeVisible();
});

it("keeps cleanup executable when unsupported resources are skipped", async () => {
  const user = userEvent.setup();
  vi.mocked(getCleanupTask).mockResolvedValue({
    task: {
      id: "cln-unsupported",
      connection_id: "connection-a",
      status: "ready",
      selectors: [
        {
          kind: "scope",
          connection_id: "connection-a",
          scope_id: "scope-region",
          scope_kind: "region",
        },
      ],
      resolved_asset_ids: ["asset-actionable", "asset-unsupported"],
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "complete" },
      snapshot_hash: "snapshot-unsupported",
      warnings: [
        {
          code: "not_actionable",
          asset_id: "asset-unsupported",
          message:
            "asset does not declare an actionable capability and will be skipped",
        },
      ],
      created_by: "operator",
      created_at: "2026-08-05T00:00:00Z",
    },
    steps: [
      {
        id: "step-actionable",
        cleanup_task_id: "cln-unsupported",
        asset_id: "asset-actionable",
        kind: "direct",
        action: "delete",
      },
    ],
    impact_items: [],
  });
  vi.mocked(findAssets).mockResolvedValue([
    {
      id: "asset-actionable",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::ECS::Instance",
        native_id: "i-actionable",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-instance",
      name: "Actionable instance",
      capabilities: ["indexed", "actionable"],
      first_seen_at: "2026-08-05T00:00:00Z",
      last_seen_at: "2026-08-05T00:00:00Z",
    },
    {
      id: "asset-unsupported",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::CEN::CenRouteMap",
        native_id: "cenrmap-unsupported",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-route-map",
      name: "Unsupported route map",
      capabilities: ["indexed"],
      normalized: { priority: 5000, transitRouterId: "tr-system" },
      first_seen_at: "2026-08-05T00:00:00Z",
      last_seen_at: "2026-08-05T00:00:00Z",
    },
  ]);

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter initialEntries={["/cleanup/cln-unsupported"]}>
        <LocaleProvider>
          <Routes>
            <Route path="/cleanup/:id" element={<CleanupTaskDetailHarness />} />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  expect(
    await screen.findByRole("button", { name: "Start cleanup" }),
  ).toBeVisible();
  expect(
    screen.getByText(
      "Unsupported resources: 1. They will be skipped during cleanup.",
    ),
  ).toBeVisible();
  expect(
    screen.queryByText("Dirty resources will be ignored during cleanup."),
  ).not.toBeInTheDocument();
  expect(screen.queryByText("Execution blockers")).not.toBeInTheDocument();

  await user.click(screen.getByRole("tab", { name: "Resource results" }));
  expect(await screen.findByText("Unsupported route map")).toBeVisible();
  expect(screen.getByText("Cleanup unsupported")).toBeVisible();
  expect(screen.getByText("Skip")).toBeVisible();
  expect(
    screen.queryByText("Delete with transit router"),
  ).not.toBeInTheDocument();
  expect(screen.queryByText("Not scheduled")).not.toBeInTheDocument();
  const actionExplanation =
    "The transit router is not included, so this task will skip the system routing policy.";
  expect(screen.getAllByText(actionExplanation)).toHaveLength(1);
  await user.hover(screen.getByRole("button", { name: "Explain action Skip" }));
  expect(await screen.findByRole("tooltip")).toHaveTextContent(
    actionExplanation,
  );
});

it("offers to add a transit router while skipping its unselected system route map", async () => {
  const user = userEvent.setup();
  vi.mocked(getCleanupTask).mockResolvedValue({
    task: {
      id: "cln-managed-route-map",
      connection_id: "connection-a",
      status: "ready",
      selectors: [
        {
          kind: "scope",
          connection_id: "connection-a",
          scope_id: "scope-region",
          scope_kind: "region",
        },
      ],
      resolved_asset_ids: ["asset-actionable", "system-route-map"],
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "complete" },
      snapshot_hash: "snapshot-managed-route-map",
      warnings: [
        {
          code: "managed_by_controller",
          asset_id: "system-route-map",
          controller_id: "transit-router",
          message: "fallback",
          evidence: {
            immediate_controller_id: "transit-router",
            lifecycle_kind: "cen_system_route_map",
          },
        },
      ],
      created_by: "operator",
      created_at: "2026-08-06T00:00:00Z",
    },
    steps: [
      {
        id: "step-actionable",
        cleanup_task_id: "cln-managed-route-map",
        asset_id: "asset-actionable",
        kind: "direct",
        action: "delete",
      },
    ],
    impact_items: [],
  });
  vi.mocked(findAssets).mockResolvedValue([
    {
      id: "asset-actionable",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::ECS::Instance",
        native_id: "i-actionable",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-instance",
      name: "Actionable instance",
      capabilities: ["indexed", "actionable"],
      first_seen_at: "2026-08-06T00:00:00Z",
      last_seen_at: "2026-08-06T00:00:00Z",
    },
    {
      id: "system-route-map",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::CEN::CenRouteMap",
        native_id: "cenrmap-tiqzpr62ykf6hm3i9t",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-route-map",
      capabilities: ["indexed"],
      normalized: { priority: 5000, transitRouterId: "tr-system" },
      first_seen_at: "2026-08-06T00:00:00Z",
      last_seen_at: "2026-08-06T00:00:00Z",
    },
    {
      id: "transit-router",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::CEN::TransitRouter",
        native_id: "tr-system",
      },
      scope_id: "scope-global",
      resource_kind_id: "kind-transit-router",
      capabilities: ["indexed", "actionable"],
      first_seen_at: "2026-08-06T00:00:00Z",
      last_seen_at: "2026-08-06T00:00:00Z",
    },
  ]);

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter initialEntries={["/cleanup/cln-managed-route-map"]}>
        <LocaleProvider>
          <Routes>
            <Route path="/cleanup/:id" element={<CleanupTaskDetailHarness />} />
            <Route path="/cleanup/new" element={<div>New task builder</div>} />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  expect(
    await screen.findByText(
      "This system routing policy is managed by its transit router. Add the transit router to delete it with the router; otherwise this task will skip it.",
    ),
  ).toBeVisible();
  expect(
    screen.getByRole("button", {
      name: "Add owning transit router and rebuild",
    }),
  ).toBeVisible();
  expect(screen.getByRole("button", { name: "Start cleanup" })).toBeVisible();
  expect(screen.queryByText("Execution blockers")).not.toBeInTheDocument();

  await user.click(screen.getByRole("tab", { name: "Resource results" }));
  expect(await screen.findByText("Skip")).toBeVisible();
  expect(screen.getByText("Skipped")).toBeVisible();

  await user.click(
    screen.getByRole("button", {
      name: "Add owning transit router and rebuild",
    }),
  );
  expect(await screen.findByText("New task builder")).toBeVisible();
  expect(readCleanupSelectionHandoff("connection-a")).toEqual([
    {
      kind: "scope",
      connection_id: "connection-a",
      scope_id: "scope-region",
      scope_kind: "region",
    },
    { kind: "asset", asset_id: "transit-router" },
  ]);
});

it("summarizes blockers once and marks each affected resource result", async () => {
  const user = userEvent.setup();
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  vi.mocked(findAssets).mockResolvedValue([
    {
      id: "stack-vswitch",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::VPC::VSwitch",
        native_id: "vsw-blocked",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-vswitch",
      name: "Blocked vSwitch",
      capabilities: ["indexed", "actionable"],
      first_seen_at: "2026-08-03T00:00:00Z",
      last_seen_at: "2026-08-03T00:00:00Z",
    },
    {
      id: "stack-vpc",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::VPC::VPC",
        native_id: "vpc-blocked",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-vpc",
      name: "Blocked VPC",
      capabilities: ["indexed", "actionable"],
      first_seen_at: "2026-08-03T00:00:00Z",
      last_seen_at: "2026-08-03T00:00:00Z",
    },
    {
      id: "external-instance",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::ECS::Instance",
        native_id: "i-002",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-instance",
      name: "external-instance",
      capabilities: ["indexed", "actionable"],
      first_seen_at: "2026-08-03T00:00:00Z",
      last_seen_at: "2026-08-03T00:00:00Z",
    },
  ]);
  vi.mocked(addCleanupTaskAssets).mockResolvedValue({
    task: {
      id: "cln-1",
      connection_id: "connection-a",
      status: "ready",
      selectors: [
        { kind: "asset", asset_id: "stack" },
        { kind: "asset", asset_id: "external-instance" },
      ],
      resolved_asset_ids: ["external-instance", "stack"],
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "not_required" },
      snapshot_hash: "snapshot-2",
      warnings: [
        {
          code: "managed_resource_direct_cleanup",
          asset_id: "stack-child",
          controller_id: "stack",
          message: "fallback",
        },
      ],
      created_by: "operator",
      created_at: "2026-08-03T00:00:00Z",
      updated_at: "2026-08-03T00:01:00Z",
    },
    steps: [
      {
        id: "step-stack",
        cleanup_task_id: "cln-1",
        asset_id: "stack",
        kind: "direct",
        action: "delete",
      },
      {
        id: "step-external",
        cleanup_task_id: "cln-1",
        asset_id: "external-instance",
        kind: "direct",
        action: "delete",
      },
    ],
    impact_items: [],
  });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={["/cleanup/cln-1"]}>
        <LocaleProvider>
          <Routes>
            <Route path="/cleanup/:id" element={<CleanupTaskDetailHarness />} />
            <Route path="/cleanup/new" element={<div>New task builder</div>} />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  expect(await screen.findByText("Cleanup warnings")).toBeVisible();
  expect(
    screen.getByText(
      "This resource belongs to a ROS Stack. Direct cleanup is allowed, but cleaning it through the Stack is recommended.",
    ),
  ).toBeVisible();
  expect(screen.getByText("Execution blockers")).toBeVisible();
  expect(
    screen.getByText(
      "Execution blockers found: 2. Resource-level blockers are marked in Resource results.",
    ),
  ).toBeVisible();
  const viewBlockersButton = screen.getByRole("button", {
    name: "View 2 blocked resources",
  });
  const addDependenciesButton = screen.getByRole("button", {
    name: "Add 1 dependent resources",
  });
  expect(viewBlockersButton).toBeVisible();
  expect(addDependenciesButton).toBeVisible();
  expect(viewBlockersButton.parentElement).toBe(
    addDependenciesButton.parentElement,
  );
  expect(
    viewBlockersButton.compareDocumentPosition(addDependenciesButton) &
      Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy();
  expect(
    screen.queryByText(
      "Other resources depend on the VPC or vSwitch this task would delete. Add those dependent resources and rebuild the task first.",
    ),
  ).not.toBeInTheDocument();

  await user.click(
    screen.getByRole("button", { name: "View 2 blocked resources" }),
  );
  expect(await screen.findByText("Blocked vSwitch")).toBeVisible();
  expect(screen.getByText("Blocked VPC")).toBeVisible();
  expect(
    within(screen.getByRole("row", { name: /Blocked vSwitch/ })).getByText(
      "Blocked",
    ),
  ).toBeVisible();
  expect(
    within(screen.getByRole("row", { name: /Blocked VPC/ })).getByText(
      "Blocked",
    ),
  ).toBeVisible();

  await user.click(addDependenciesButton);
  const addDependenciesDialog = await screen.findByRole("alertdialog", {
    name: "Add dependent resources?",
  });
  expect(addDependenciesDialog).toBeVisible();
  expect(
    within(addDependenciesDialog).getByText("Cleanup targets to add"),
  ).toBeVisible();
  expect(
    within(addDependenciesDialog).getByText("external-instance"),
  ).toBeVisible();
  expect(within(addDependenciesDialog).getByText("i-002")).toBeVisible();
  expect(within(addDependenciesDialog).getByText("ECS Instance")).toBeVisible();
  expect(addCleanupTaskAssets).not.toHaveBeenCalled();
  expect(screen.queryByText("New task builder")).not.toBeInTheDocument();

  await user.click(screen.getByRole("button", { name: "Add and update task" }));
  await waitFor(() =>
    expect(addCleanupTaskAssets).toHaveBeenCalledWith("connection-a", "cln-1", [
      "external-instance",
    ]),
  );
  await waitFor(() =>
    expect(
      screen.queryByRole("alertdialog", {
        name: "Add dependent resources?",
      }),
    ).not.toBeInTheDocument(),
  );
  expect(screen.queryByText("Execution blockers")).not.toBeInTheDocument();
  expect(screen.queryByText("New task builder")).not.toBeInTheDocument();
  expect(readCleanupSelectionHandoff("connection-a")).toEqual([]);
});

it("treats incomplete scan coverage as an executable warning", async () => {
  vi.mocked(getCleanupTask).mockResolvedValue({
    task: {
      id: "cln-incomplete-scan",
      connection_id: "connection-a",
      status: "draft",
      selectors: [
        {
          kind: "scope",
          connection_id: "connection-a",
          scope_id: "scope-region",
          scope_kind: "region",
        },
      ],
      resolved_asset_ids: [],
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "incomplete" },
      snapshot_hash: "snapshot-incomplete-scan",
      blockers: [
        {
          code: "scan_coverage_incomplete",
          message: "cleanup range requires a complete scan",
          evidence: {
            coverage: {
              status: "incomplete",
            },
          },
        },
      ],
      created_by: "operator",
      created_at: "2026-08-03T00:00:00Z",
    },
    steps: [
      {
        id: "step-current",
        cleanup_task_id: "cln-incomplete-scan",
        asset_id: "asset-current",
        kind: "direct",
        action: "delete",
      },
    ],
    impact_items: [],
  });
  vi.mocked(findAssets).mockResolvedValue([]);

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter initialEntries={["/cleanup/cln-incomplete-scan"]}>
        <LocaleProvider>
          <Routes>
            <Route path="/cleanup/:id" element={<CleanupTaskDetailHarness />} />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  expect(
    await screen.findByText(
      "Scan coverage is incomplete. Cleanup will proceed using the resources currently discovered and may miss unscanned dependencies.",
    ),
  ).toBeVisible();
  expect(screen.getByRole("button", { name: "Start cleanup" })).toBeVisible();
  expect(screen.getByText("Ready")).toBeVisible();
  expect(screen.queryByText("Execution blockers")).not.toBeInTheDocument();
  expect(
    screen.queryByText(
      "Execution blockers found: 1. Resource-level blockers are marked in Resource results.",
    ),
  ).not.toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: "View 1 blocked resources" }),
  ).not.toBeInTheDocument();
});

it("shows a system route table as deleted with its selected VPC", async () => {
  const user = userEvent.setup();
  vi.mocked(getCleanupTask).mockResolvedValue({
    task: {
      id: "cln-vpc-route-table",
      connection_id: "connection-a",
      status: "ready",
      selectors: [
        { kind: "asset", asset_id: "route-table" },
        { kind: "asset", asset_id: "vpc" },
      ],
      resolved_asset_ids: ["route-table", "vpc"],
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "complete" },
      snapshot_hash: "snapshot-vpc-route-table",
      created_by: "operator",
      created_at: "2026-08-03T00:00:00Z",
    },
    steps: [
      {
        id: "step-vpc",
        cleanup_task_id: "cln-vpc-route-table",
        asset_id: "vpc",
        kind: "controller",
        action: "delete",
      },
    ],
    impact_items: [
      {
        id: "impact-route-table",
        cleanup_task_id: "cln-vpc-route-table",
        asset_id: "route-table",
        controller_id: "vpc",
        delegated_to: "step-vpc",
        expected: "delegated_delete",
        result: "deleted_by_controller",
        evidence: { lifecycle_kind: "vpc_system_route_table" },
        may_continue_billing: false,
      },
    ],
  });
  vi.mocked(findAssets).mockResolvedValue(systemRouteTableAssets());

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter initialEntries={["/cleanup/cln-vpc-route-table"]}>
        <LocaleProvider>
          <Routes>
            <Route path="/cleanup/:id" element={<CleanupTaskDetailHarness />} />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  await user.click(
    await screen.findByRole("tab", { name: "Resource results" }),
  );
  expect(await screen.findByText("Delete with VPC")).toBeVisible();
  expect(screen.getByText("Succeeded")).toBeVisible();
  expect(screen.queryByText("Deleted by controller")).not.toBeInTheDocument();
  const routeTableRow = screen
    .getByRole("link", { name: "System route table" })
    .closest("tr");
  expect(routeTableRow).not.toBeNull();
  expect(
    within(routeTableRow!).getAllByText("System route table"),
  ).toHaveLength(2);
  expect(
    within(routeTableRow!).queryByText("Managed asset"),
  ).not.toBeInTheDocument();
  const actionExplanation =
    "Attached to VPC vpc-production; removed automatically with the VPC.";
  expect(screen.getByText(actionExplanation)).toBeVisible();
  await user.hover(
    screen.getByRole("button", { name: "Explain action Delete with VPC" }),
  );
  expect(await screen.findByRole("tooltip")).toHaveTextContent(
    actionExplanation,
  );
  expect(
    screen.queryByRole("button", { name: "Add owning VPC and rebuild" }),
  ).not.toBeInTheDocument();
});

it("names the controller for delegated deletion and explains the lifecycle in Chinese", async () => {
  const user = userEvent.setup();
  localStorage.setItem("steward.locale", "zh-CN");
  vi.mocked(getCleanupTask).mockResolvedValue({
    task: {
      id: "cln-cen-eni",
      connection_id: "connection-a",
      status: "ready",
      selectors: [{ kind: "asset", asset_id: "cen-vpc-attachment" }],
      resolved_asset_ids: ["cen-vpc-attachment", "cen-managed-eni"],
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "complete" },
      snapshot_hash: "snapshot-cen-eni",
      created_by: "operator",
      created_at: "2026-08-07T00:00:00Z",
    },
    steps: [
      {
        id: "step-cen-vpc-attachment",
        cleanup_task_id: "cln-cen-eni",
        asset_id: "cen-vpc-attachment",
        kind: "controller",
        action: "delete",
      },
    ],
    impact_items: [
      {
        id: "impact-cen-managed-eni",
        cleanup_task_id: "cln-cen-eni",
        asset_id: "cen-managed-eni",
        controller_id: "cen-vpc-attachment",
        delegated_to: "step-cen-vpc-attachment",
        expected: "delegated_delete",
        may_continue_billing: false,
      },
    ],
  });
  vi.mocked(findAssets).mockResolvedValue([
    {
      id: "cen-vpc-attachment",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::CEN::TransitRouterVpcAttachment",
        native_id: "tr-attach-b9owgteq5t2u1izyqx",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-cen-vpc-attachment",
      name: "tr-attach-b9owgteq5t2u1izyqx",
      capabilities: ["indexed", "actionable"],
      first_seen_at: "2026-08-07T00:00:00Z",
      last_seen_at: "2026-08-07T00:00:00Z",
    },
    {
      id: "cen-managed-eni",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::ECS::NetworkInterface",
        native_id: "eni-mj7bdaysvwp3epwuh1bh",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-network-interface",
      name: "Interface for ntr-ap-northeast-2",
      capabilities: ["indexed"],
      first_seen_at: "2026-08-07T00:00:00Z",
      last_seen_at: "2026-08-07T00:00:00Z",
    },
  ]);

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter initialEntries={["/cleanup/cln-cen-eni"]}>
        <LocaleProvider>
          <Routes>
            <Route path="/cleanup/:id" element={<CleanupTaskDetailHarness />} />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  await user.click(await screen.findByRole("tab", { name: "资源结果" }));
  const action = await screen.findByText("随 CEN 地域内连接 删除");
  expect(action).toBeVisible();
  expect(screen.queryByText("由控制器委托删除")).not.toBeInTheDocument();
  const reason =
    "附属于 CEN 地域内连接 tr-attach-b9owgteq5t2u1izyqx；删除 CEN 地域内连接 时会自动删除。";
  expect(screen.getByText(reason)).toBeVisible();
  const managedBadge = screen.getByText("托管资源");
  expect(managedBadge).toBeVisible();
  expect(managedBadge).toHaveAttribute("tabindex", "0");
  await user.hover(managedBadge);
  expect(await screen.findByRole("tooltip")).toHaveTextContent(
    "管理方：CEN 地域内连接 tr-attach-b9owgteq5t2u1izyqx。",
  );
});

it("shows a ROS-managed VPC as successfully deleted with its Stack", async () => {
  vi.mocked(getCleanupTask).mockResolvedValue({
    task: {
      id: "cln-ros-vpc",
      connection_id: "connection-a",
      status: "completed",
      selectors: [{ kind: "asset", asset_id: "stack" }],
      resolved_asset_ids: ["stack", "vpc"],
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "complete" },
      snapshot_hash: "snapshot-ros-vpc",
      created_by: "operator",
      created_at: "2026-08-06T00:00:00Z",
    },
    steps: [
      {
        id: "step-stack",
        cleanup_task_id: "cln-ros-vpc",
        asset_id: "stack",
        kind: "controller",
        action: "delete",
      },
    ],
    impact_items: [
      {
        id: "impact-vpc",
        cleanup_task_id: "cln-ros-vpc",
        asset_id: "vpc",
        controller_id: "stack",
        delegated_to: "step-stack",
        expected: "delegated_delete",
        result: "deleted_by_controller",
        evidence: {
          evidence_source: "ros:system-tag",
          stack_id: "stack-001",
        },
        may_continue_billing: false,
      },
    ],
  });
  vi.mocked(findAssets).mockResolvedValue([
    {
      id: "stack",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::ROS::Stack",
        native_id: "stack-001",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-stack",
      name: "my-vpc-stack",
      capabilities: ["indexed", "actionable"],
      first_seen_at: "2026-08-06T00:00:00Z",
      last_seen_at: "2026-08-06T00:00:00Z",
    },
    {
      id: "vpc",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::VPC::VPC",
        native_id: "vpc-001",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-vpc",
      name: "Production VPC",
      capabilities: ["indexed", "actionable"],
      first_seen_at: "2026-08-06T00:00:00Z",
      last_seen_at: "2026-08-06T00:00:00Z",
    },
  ]);
  vi.mocked(listCleanupTaskExecutions).mockResolvedValue({
    items: [
      {
        id: "execution-ros-vpc",
        connection_id: "connection-a",
        cleanup_task_id: "cln-ros-vpc",
        status: "succeeded",
        requested_by: "operator",
        idempotency_key: "execution-ros-vpc",
        created_at: "2026-08-06T00:00:01Z",
      },
    ],
  });
  vi.mocked(listExecutionActions).mockResolvedValue({
    items: [
      {
        id: "action-stack",
        execution_id: "execution-ros-vpc",
        cleanup_task_step_id: "step-stack",
        asset_id: "stack",
        action: "delete",
        status: "succeeded",
        idempotency_key: "action-stack",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
        created_at: "2026-08-06T00:00:02Z",
        updated_at: "2026-08-06T00:00:03Z",
      },
    ],
  });

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter initialEntries={["/cleanup/cln-ros-vpc"]}>
        <LocaleProvider>
          <Routes>
            <Route path="/cleanup/:id" element={<CleanupTaskDetailHarness />} />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  const vpcRow = (await screen.findByText("Production VPC")).closest("tr");
  expect(vpcRow).not.toBeNull();
  expect(within(vpcRow!).getByText("Delete with ROS Stack")).toBeVisible();
  expect(within(vpcRow!).getByText("Succeeded")).toBeVisible();
  expect(
    within(vpcRow!).getByText(
      "Managed by ROS Stack my-vpc-stack; it is deleted automatically when the Stack is deleted.",
    ),
  ).toBeVisible();
  expect(
    within(vpcRow!).queryByText("Delegated; awaiting scan confirmation"),
  ).not.toBeInTheDocument();
});

it("shows managed VPC resources and blocked dependency chains as waiting", async () => {
  const user = userEvent.setup();
  vi.mocked(getCleanupTask).mockResolvedValue({
    task: {
      id: "cln-vpc-waiting",
      connection_id: "connection-a",
      status: "failed",
      selectors: [
        { kind: "asset", asset_id: "route-table" },
        { kind: "asset", asset_id: "vpc" },
        { kind: "asset", asset_id: "security-group" },
        { kind: "asset", asset_id: "vswitch" },
      ],
      resolved_asset_ids: ["route-table", "vpc", "security-group", "vswitch"],
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "complete" },
      snapshot_hash: "snapshot-vpc-waiting",
      created_by: "operator",
      created_at: "2026-08-03T00:00:00Z",
    },
    steps: [
      {
        id: "step-security-group",
        cleanup_task_id: "cln-vpc-waiting",
        asset_id: "security-group",
        kind: "direct",
        action: "delete",
      },
      {
        id: "step-vswitch",
        cleanup_task_id: "cln-vpc-waiting",
        asset_id: "vswitch",
        kind: "direct",
        action: "delete",
      },
      {
        id: "step-vpc",
        cleanup_task_id: "cln-vpc-waiting",
        asset_id: "vpc",
        kind: "controller",
        action: "delete",
        depends_on: ["step-security-group", "step-vswitch"],
      },
    ],
    impact_items: [
      {
        id: "impact-route-table",
        cleanup_task_id: "cln-vpc-waiting",
        asset_id: "route-table",
        controller_id: "vpc",
        delegated_to: "step-vpc",
        expected: "delegated_delete",
        evidence: { lifecycle_kind: "vpc_system_route_table" },
        may_continue_billing: false,
      },
    ],
  });
  vi.mocked(findAssets).mockResolvedValue([
    ...systemRouteTableAssets(),
    {
      id: "security-group",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::ECS::SecurityGroup",
        native_id: "sg-production",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-security-group",
      name: "Production security group",
      capabilities: ["indexed", "actionable"],
      first_seen_at: "2026-08-03T00:00:00Z",
      last_seen_at: "2026-08-03T00:00:00Z",
    },
    {
      id: "vswitch",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::VPC::VSwitch",
        native_id: "vsw-production",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-vswitch",
      name: "Production vSwitch",
      capabilities: ["indexed", "actionable"],
      first_seen_at: "2026-08-03T00:00:00Z",
      last_seen_at: "2026-08-03T00:00:00Z",
    },
  ]);
  vi.mocked(listCleanupTaskExecutions).mockResolvedValue({
    items: [
      {
        id: "execution-vpc-waiting",
        connection_id: "connection-a",
        cleanup_task_id: "cln-vpc-waiting",
        status: "failed",
        requested_by: "operator",
        idempotency_key: "execution-vpc-waiting",
        created_at: "2026-08-03T00:00:01Z",
      },
    ],
  });
  vi.mocked(listExecutionActions).mockResolvedValue({
    items: [
      {
        id: "action-security-group",
        execution_id: "execution-vpc-waiting",
        cleanup_task_step_id: "step-security-group",
        asset_id: "security-group",
        action: "delete",
        status: "failed",
        idempotency_key: "action-security-group",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
        provider_error: {
          category: "dependency_violation",
          code: "DependencyViolation",
          message: "The security group is still in use.",
        },
        created_at: "2026-08-03T00:00:02Z",
        updated_at: "2026-08-03T00:00:03Z",
      },
    ],
  });

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter initialEntries={["/cleanup/cln-vpc-waiting"]}>
        <LocaleProvider>
          <Routes>
            <Route path="/cleanup/:id" element={<CleanupTaskDetailHarness />} />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  await user.click(
    await screen.findByRole("tab", { name: "Resource results" }),
  );
  const routeTableRow = screen.getByText("vtb-system").closest("tr");
  expect(routeTableRow).not.toBeNull();
  expect(within(routeTableRow!).getByText("Waiting")).toBeVisible();
  expect(
    within(routeTableRow!).getByText(
      "Attached to VPC vpc-production; removed automatically with the VPC.",
    ),
  ).toBeVisible();

  const vpcRow = screen.getByText("vpc-production").closest("tr");
  expect(vpcRow).not.toBeNull();
  expect(within(vpcRow!).getByText("Waiting")).toBeVisible();
  expect(
    within(vpcRow!).getByText(
      "Waiting for dependency sg-production to be deleted (2 dependencies in total).",
    ),
  ).toBeVisible();
});

it("shows a failed ECS system disk as waiting for its selected instance", async () => {
  const user = userEvent.setup();
  vi.mocked(getCleanupTask).mockResolvedValue({
    task: {
      id: "cln-ecs-system-disk",
      connection_id: "connection-a",
      status: "failed",
      selectors: [
        { kind: "asset", asset_id: "system-disk" },
        { kind: "asset", asset_id: "ecs-instance" },
      ],
      resolved_asset_ids: ["system-disk", "ecs-instance"],
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "complete" },
      snapshot_hash: "snapshot-ecs-system-disk",
      created_by: "operator",
      created_at: "2026-08-06T00:00:00Z",
    },
    steps: [
      {
        id: "step-ecs-instance",
        cleanup_task_id: "cln-ecs-system-disk",
        asset_id: "ecs-instance",
        kind: "controller",
        action: "delete",
      },
    ],
    impact_items: [
      {
        id: "impact-system-disk",
        cleanup_task_id: "cln-ecs-system-disk",
        asset_id: "system-disk",
        controller_id: "ecs-instance",
        delegated_to: "step-ecs-instance",
        expected: "delegated_delete",
        result: "cleanup_failed",
        evidence: { lifecycle_kind: "ecs_disk_delete_with_instance" },
        may_continue_billing: false,
      },
    ],
  });
  vi.mocked(findAssets).mockResolvedValue([
    {
      id: "ecs-instance",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::ECS::Instance",
        native_id: "i-6we5ypd9as5am03ubd5n",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-instance",
      name: "ros-dev-xianyan",
      capabilities: ["indexed", "actionable"],
      first_seen_at: "2026-08-06T00:00:00Z",
      last_seen_at: "2026-08-06T00:00:00Z",
    },
    {
      id: "system-disk",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::ECS::Disk",
        native_id: "d-6we5ypd9as5am03yq0pv",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-disk",
      capabilities: ["indexed", "actionable"],
      normalized: {
        attached_instance_id: "i-6we5ypd9as5am03ubd5n",
        disk_type: "system",
        delete_with_instance: true,
      },
      first_seen_at: "2026-08-06T00:00:00Z",
      last_seen_at: "2026-08-06T00:00:00Z",
    },
  ]);
  vi.mocked(listCleanupTaskExecutions).mockResolvedValue({
    items: [
      {
        id: "execution-ecs-system-disk",
        connection_id: "connection-a",
        cleanup_task_id: "cln-ecs-system-disk",
        status: "failed",
        requested_by: "operator",
        idempotency_key: "execution-ecs-system-disk",
        created_at: "2026-08-06T00:00:01Z",
      },
    ],
  });
  vi.mocked(listExecutionActions).mockResolvedValue({
    items: [
      {
        id: "action-ecs-instance",
        execution_id: "execution-ecs-system-disk",
        cleanup_task_step_id: "step-ecs-instance",
        asset_id: "ecs-instance",
        action: "delete",
        status: "failed",
        idempotency_key: "action-ecs-instance",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
        provider_error: {
          category: "conflict",
          code: "IncorrectInstanceStatus",
          message:
            "The current status of the resource does not support this operation.",
        },
        created_at: "2026-08-06T00:00:02Z",
        updated_at: "2026-08-06T00:00:03Z",
      },
    ],
  });

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter initialEntries={["/cleanup/cln-ecs-system-disk"]}>
        <LocaleProvider>
          <Routes>
            <Route path="/cleanup/:id" element={<CleanupTaskDetailHarness />} />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  await user.click(
    await screen.findByRole("tab", { name: "Resource results" }),
  );
  const action = await screen.findByText("Delete with ECS instance");
  expect(action).toBeVisible();
  const diskRow = action.closest("tr");
  expect(diskRow).not.toBeNull();
  expect(within(diskRow!).getByText("System disk")).toBeVisible();
  expect(within(diskRow!).getByText("Waiting")).toBeVisible();
  expect(
    within(diskRow!).queryByText("Cleanup failed"),
  ).not.toBeInTheDocument();
  const actionExplanation =
    "Attached to ECS instance i-6we5ypd9as5am03ubd5n; removed automatically with the instance.";
  expect(screen.queryByText(actionExplanation)).not.toBeInTheDocument();
  await user.hover(
    screen.getByRole("button", {
      name: "Explain action Delete with ECS instance",
    }),
  );
  expect(await screen.findByRole("tooltip")).toHaveTextContent(
    actionExplanation,
  );
});

it("treats a skipped CEN system route table as deleted with its transit router", async () => {
  const user = userEvent.setup();
  localStorage.setItem("steward.locale", "zh-CN");
  vi.mocked(getCleanupTask).mockResolvedValue({
    task: {
      id: "cln-cen-system-route-table",
      connection_id: "connection-a",
      status: "completed",
      selectors: [
        { kind: "asset", asset_id: "system-route-table" },
        { kind: "asset", asset_id: "transit-router" },
      ],
      resolved_asset_ids: ["system-route-table", "transit-router"],
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "complete" },
      snapshot_hash: "snapshot-cen-system-route-table",
      created_by: "operator",
      created_at: "2026-08-07T00:00:00Z",
    },
    steps: [
      {
        id: "step-system-route-table",
        cleanup_task_id: "cln-cen-system-route-table",
        asset_id: "system-route-table",
        kind: "direct",
        action: "delete",
      },
      {
        id: "step-transit-router",
        cleanup_task_id: "cln-cen-system-route-table",
        asset_id: "transit-router",
        kind: "controller",
        action: "delete",
      },
    ],
    impact_items: [],
  });
  vi.mocked(listCleanupTaskExecutions).mockResolvedValue({
    items: [
      {
        id: "execution-cen-system-route-table",
        connection_id: "connection-a",
        cleanup_task_id: "cln-cen-system-route-table",
        status: "succeeded",
        requested_by: "operator",
        idempotency_key: "execution-cen-system-route-table",
        created_at: "2026-08-07T00:00:01Z",
      },
    ],
  });
  vi.mocked(listExecutionActions).mockResolvedValue({
    items: [
      {
        id: "action-system-route-table",
        execution_id: "execution-cen-system-route-table",
        cleanup_task_step_id: "step-system-route-table",
        asset_id: "system-route-table",
        action: "delete",
        status: "skipped",
        skip_reason: "delegated_to_transit_router",
        idempotency_key: "action-system-route-table",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
        created_at: "2026-08-07T00:00:02Z",
        updated_at: "2026-08-07T00:00:03Z",
      },
      {
        id: "action-transit-router",
        execution_id: "execution-cen-system-route-table",
        cleanup_task_step_id: "step-transit-router",
        asset_id: "transit-router",
        action: "delete",
        status: "succeeded",
        idempotency_key: "action-transit-router",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
        created_at: "2026-08-07T00:00:04Z",
        updated_at: "2026-08-07T00:00:05Z",
      },
    ],
  });
  vi.mocked(findAssets).mockResolvedValue([
    {
      id: "system-route-table",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::CEN::TransitRouterRouteTable",
        native_id: "vtb-mj7bqh7ib1krxovq6x3is",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-transit-router-route-table",
      capabilities: ["indexed"],
      normalized: {
        routeTableType: "System",
        transitRouterId: "tr-mj7f5z1btac2s1x4jusp5",
      },
      first_seen_at: "2026-08-07T00:00:00Z",
      last_seen_at: "2026-08-07T00:00:00Z",
    },
    {
      id: "transit-router",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::CEN::TransitRouter",
        native_id: "tr-mj7f5z1btac2s1x4jusp5",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-transit-router",
      name: "test",
      capabilities: ["indexed", "actionable"],
      normalized: {},
      first_seen_at: "2026-08-07T00:00:00Z",
      last_seen_at: "2026-08-07T00:00:00Z",
    },
  ]);

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter initialEntries={["/cleanup/cln-cen-system-route-table"]}>
        <LocaleProvider>
          <Routes>
            <Route path="/cleanup/:id" element={<CleanupTaskDetailHarness />} />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  await user.click(await screen.findByRole("tab", { name: "资源结果" }));
  const routeTableRow = (
    await screen.findByRole("link", {
      name: "vtb-mj7bqh7ib1krxovq6x3is",
    })
  ).closest("tr");
  expect(routeTableRow).not.toBeNull();
  expect(within(routeTableRow!).getByText("系统路由表")).toBeVisible();
  expect(
    within(routeTableRow!).queryByText("托管资源"),
  ).not.toBeInTheDocument();
  expect(within(routeTableRow!).getByText("随转发路由器删除")).toBeVisible();
  expect(within(routeTableRow!).getByText("成功")).toBeVisible();
  expect(
    within(routeTableRow!).getByText(
      "附属于转发路由器 tr-mj7f5z1btac2s1x4jusp5；删除转发路由器时会自动删除。",
    ),
  ).toBeVisible();
  const systemRouteTableBadge = within(routeTableRow!).getByText("系统路由表");
  await user.hover(systemRouteTableBadge);
  expect(await screen.findByRole("tooltip")).toHaveTextContent(
    "管理方：转发路由器 test (tr-mj7f5z1btac2s1x4jusp5)。",
  );
});

it("shows a legacy NAT-managed security group as still present", async () => {
  const user = userEvent.setup();
  localStorage.setItem("steward.locale", "zh-CN");
  vi.mocked(getCleanupTask).mockResolvedValue({
    task: {
      id: "cln-nat-managed-security-group",
      connection_id: "connection-a",
      status: "failed",
      selectors: [
        { kind: "asset", asset_id: "nat-gateway" },
        { kind: "asset", asset_id: "managed-security-group" },
      ],
      resolved_asset_ids: [
        "nat-gateway",
        "managed-security-group",
        "managed-eni",
      ],
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "complete" },
      snapshot_hash: "snapshot-nat-managed-security-group",
      created_by: "operator",
      created_at: "2026-08-07T00:00:00Z",
    },
    steps: [
      {
        id: "step-nat-gateway",
        cleanup_task_id: "cln-nat-managed-security-group",
        asset_id: "nat-gateway",
        kind: "controller",
        action: "delete",
      },
      {
        id: "step-security-group",
        cleanup_task_id: "cln-nat-managed-security-group",
        asset_id: "managed-security-group",
        kind: "direct",
        action: "delete",
        depends_on: ["step-nat-gateway"],
      },
    ],
    impact_items: [
      {
        id: "impact-managed-eni",
        cleanup_task_id: "cln-nat-managed-security-group",
        asset_id: "managed-eni",
        controller_id: "nat-gateway",
        delegated_to: "step-nat-gateway",
        ownership: "exclusive",
        cleanup_policy: "delegate",
        expected: "delegated_delete",
        result: "deleted_by_controller",
        may_continue_billing: false,
        evidence: {
          lifecycle_kind: "nat_service_managed_eni",
          evidence_source: "nat:service-managed-eni",
          controller_delete_guaranteed: true,
        },
      },
    ],
  });
  vi.mocked(listCleanupTaskExecutions).mockResolvedValue({
    items: [
      {
        id: "execution-nat-managed-security-group",
        connection_id: "connection-a",
        cleanup_task_id: "cln-nat-managed-security-group",
        status: "failed",
        requested_by: "operator",
        idempotency_key: "execution-nat-managed-security-group",
        created_at: "2026-08-07T00:00:01Z",
      },
    ],
  });
  vi.mocked(listExecutionActions).mockResolvedValue({
    items: [
      {
        id: "action-nat-gateway",
        execution_id: "execution-nat-managed-security-group",
        cleanup_task_step_id: "step-nat-gateway",
        asset_id: "nat-gateway",
        action: "delete",
        status: "succeeded",
        idempotency_key: "action-nat-gateway",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
        created_at: "2026-08-07T00:00:02Z",
        updated_at: "2026-08-07T00:00:03Z",
      },
      {
        id: "action-security-group",
        execution_id: "execution-nat-managed-security-group",
        cleanup_task_step_id: "step-security-group",
        asset_id: "managed-security-group",
        action: "delete",
        status: "failed",
        idempotency_key: "action-security-group",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
        provider_error: {
          category: "permission_denied",
          code: "InvalidOperation.ResourceManagedByCloudProduct",
          message:
            'The specified SecurityGroup "sg-managed" has been managed by product "natgw".',
        },
        created_at: "2026-08-07T00:00:04Z",
        updated_at: "2026-08-07T00:00:05Z",
      },
    ],
  });
  vi.mocked(findAssets).mockResolvedValue([
    {
      id: "nat-gateway",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::NAT::NatGateway",
        native_id: "ngw-2vcxmgkhbdp5cn6fc0w2t",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-nat-gateway",
      name: "production-nat",
      capabilities: ["indexed", "actionable"],
      normalized: {},
      first_seen_at: "2026-08-07T00:00:00Z",
      last_seen_at: "2026-08-07T00:00:00Z",
    },
    {
      id: "managed-security-group",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::ECS::SecurityGroup",
        native_id: "sg-managed",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-security-group",
      name: "ngw-2vcxmgkhbdp5cn6fc0w2t_security_group",
      capabilities: ["indexed", "actionable"],
      normalized: {
        _service_managed: true,
        _service_id: "1679259531804325",
      },
      first_seen_at: "2026-08-07T00:00:00Z",
      last_seen_at: "2026-08-07T00:00:00Z",
    },
    {
      id: "managed-eni",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::ECS::NetworkInterface",
        native_id: "eni-managed",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-network-interface",
      capabilities: ["indexed"],
      normalized: {
        _service_managed: true,
        security_group_ids: ["sg-managed"],
      },
      first_seen_at: "2026-08-07T00:00:00Z",
      last_seen_at: "2026-08-07T00:00:00Z",
    },
  ]);

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter
        initialEntries={["/cleanup/cln-nat-managed-security-group"]}
      >
        <LocaleProvider>
          <Routes>
            <Route path="/cleanup/:id" element={<CleanupTaskDetailHarness />} />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  await user.click(await screen.findByRole("tab", { name: "资源结果" }));
  const securityGroupRow = (
    await screen.findByRole("link", {
      name: "ngw-2vcxmgkhbdp5cn6fc0w2t_security_group",
    })
  ).closest("tr");
  expect(securityGroupRow).not.toBeNull();
  expect(within(securityGroupRow!).getByText("托管资源")).toBeVisible();
  expect(within(securityGroupRow!).getByText("随 NAT 网关删除")).toBeVisible();
  expect(within(securityGroupRow!).getByText("仍然存在")).toBeVisible();
  expect(
    within(securityGroupRow!).getByText(
      "NAT 网关 ngw-2vcxmgkhbdp5cn6fc0w2t 已删除；超过 2 分钟后仍能检查到该托管资源。重试只会检查一次状态，不会发起删除。",
    ),
  ).toBeVisible();
  expect(
    within(securityGroupRow!).queryByText(
      /InvalidOperation\.ResourceManagedByCloudProduct/,
    ),
  ).not.toBeInTheDocument();
  await user.hover(within(securityGroupRow!).getByText("托管资源"));
  expect(await screen.findByRole("tooltip")).toHaveTextContent(
    "管理方：NAT 网关 production-nat (ngw-2vcxmgkhbdp5cn6fc0w2t)。",
  );
});

it("shows a system CEN route map as deleted with its transit router", async () => {
  const user = userEvent.setup();
  vi.mocked(getCleanupTask).mockResolvedValue({
    task: {
      id: "cln-cen-route-map",
      connection_id: "connection-a",
      status: "ready",
      selectors: [
        { kind: "asset", asset_id: "system-route-map" },
        { kind: "asset", asset_id: "transit-router" },
      ],
      resolved_asset_ids: ["system-route-map", "transit-router"],
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "complete" },
      snapshot_hash: "snapshot-cen-route-map",
      created_by: "operator",
      created_at: "2026-08-06T00:00:00Z",
    },
    steps: [
      {
        id: "step-transit-router",
        cleanup_task_id: "cln-cen-route-map",
        asset_id: "transit-router",
        kind: "controller",
        action: "delete",
      },
    ],
    impact_items: [
      {
        id: "impact-system-route-map",
        cleanup_task_id: "cln-cen-route-map",
        asset_id: "system-route-map",
        controller_id: "transit-router",
        delegated_to: "step-transit-router",
        expected: "delegated_delete",
        evidence: { lifecycle_kind: "cen_system_route_map" },
        may_continue_billing: false,
      },
    ],
  });
  vi.mocked(findAssets).mockResolvedValue([
    {
      id: "transit-router",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::CEN::TransitRouter",
        native_id: "tr-system",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-transit-router",
      name: "System transit router",
      capabilities: ["indexed", "actionable"],
      first_seen_at: "2026-08-06T00:00:00Z",
      last_seen_at: "2026-08-06T00:00:00Z",
    },
    {
      id: "system-route-map",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::CEN::CenRouteMap",
        native_id: "cenrmap-tiqzpr62ykf6hm3i9t",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-route-map",
      name: "cenrmap-tiqzpr62ykf6hm3i9t",
      capabilities: ["indexed"],
      normalized: { priority: 5000, transitRouterId: "tr-system" },
      first_seen_at: "2026-08-06T00:00:00Z",
      last_seen_at: "2026-08-06T00:00:00Z",
    },
  ]);

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter initialEntries={["/cleanup/cln-cen-route-map"]}>
        <LocaleProvider>
          <Routes>
            <Route path="/cleanup/:id" element={<CleanupTaskDetailHarness />} />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  await user.click(
    await screen.findByRole("tab", { name: "Resource results" }),
  );
  expect(await screen.findByText("Delete with transit router")).toBeVisible();
  const actionExplanation =
    "Attached to transit router tr-system; removed automatically with the transit router.";
  expect(screen.getByText(actionExplanation)).toBeVisible();
  await user.hover(
    screen.getByRole("button", {
      name: "Explain action Delete with transit router",
    }),
  );
  expect(await screen.findByRole("tooltip")).toHaveTextContent(
    actionExplanation,
  );
  expect(screen.queryByText("Not scheduled")).not.toBeInTheDocument();
});

it("skips a selected system route table and offers to add its VPC", async () => {
  const user = userEvent.setup();
  vi.mocked(getCleanupTask).mockResolvedValue({
    task: {
      id: "cln-route-table-only",
      connection_id: "connection-a",
      status: "draft",
      selectors: [{ kind: "asset", asset_id: "route-table" }],
      resolved_asset_ids: ["route-table"],
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "complete" },
      snapshot_hash: "snapshot-route-table-only",
      blockers: [
        {
          code: "managed_by_controller",
          asset_id: "route-table",
          controller_id: "vpc",
          message: "fallback",
          evidence: {
            immediate_controller_id: "vpc",
            lifecycle_kind: "vpc_system_route_table",
          },
        },
      ],
      created_by: "operator",
      created_at: "2026-08-03T00:00:00Z",
    },
    steps: [],
    impact_items: [],
  });
  vi.mocked(findAssets).mockResolvedValue(systemRouteTableAssets());

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter initialEntries={["/cleanup/cln-route-table-only"]}>
        <LocaleProvider>
          <Routes>
            <Route path="/cleanup/:id" element={<CleanupTaskDetailHarness />} />
            <Route path="/cleanup/new" element={<div>New task builder</div>} />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  expect(
    await screen.findByText(
      "Execution blockers found: 1. Resource-level blockers are marked in Resource results.",
    ),
  ).toBeVisible();
  expect(screen.getByText("Execution blockers")).toBeVisible();
  await user.click(screen.getByRole("tab", { name: "Resource results" }));
  expect(await screen.findByText("Not scheduled")).toBeVisible();
  expect(screen.getByText("Blocked")).toBeVisible();
  expect(
    screen.getByText(
      "This system route table is not scheduled. Add its VPC to remove it with the VPC.",
    ),
  ).toBeVisible();
  expect(
    screen.queryByText(
      "Attached to VPC vpc-production; removed automatically with the VPC.",
    ),
  ).not.toBeInTheDocument();
  await user.hover(
    screen.getByRole("button", { name: "Explain action Not scheduled" }),
  );
  expect(await screen.findByRole("tooltip")).toHaveTextContent(
    "Attached to VPC vpc-production; removed automatically with the VPC.",
  );

  await user.click(
    screen.getByRole("button", { name: "Add owning VPC and rebuild" }),
  );
  expect(await screen.findByText("New task builder")).toBeVisible();
  expect(readCleanupSelectionHandoff("connection-a")).toEqual([
    { kind: "asset", asset_id: "route-table" },
    { kind: "asset", asset_id: "vpc" },
  ]);
});

it("offers to add the CEN source connection for a managed ENI", async () => {
  const user = userEvent.setup();
  vi.mocked(getCleanupTask).mockResolvedValue({
    task: {
      id: "cln-cen-eni",
      connection_id: "connection-a",
      status: "draft",
      selectors: [{ kind: "asset", asset_id: "managed-eni" }],
      resolved_asset_ids: ["managed-eni"],
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "complete" },
      snapshot_hash: "snapshot-cen-eni",
      blockers: [
        {
          code: "managed_by_controller",
          asset_id: "managed-eni",
          controller_id: "vpc-attachment",
          message: "fallback",
          evidence: {
            immediate_controller_id: "vpc-attachment",
            lifecycle_kind: "cen_transit_router_vpc_attachment",
            transit_router_attachment_id: "tr-attach-b9owgteq5t2u1izyqx",
          },
        },
      ],
      created_by: "operator",
      created_at: "2026-08-05T00:00:00Z",
    },
    steps: [],
    impact_items: [],
  });
  vi.mocked(findAssets).mockResolvedValue([
    {
      id: "managed-eni",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::ECS::NetworkInterface",
        native_id: "eni-mj7bdaysvwp3epwuh1bh",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-eni",
      name: "Managed ENI",
      location: "ap-northeast-2",
      capabilities: ["indexed", "actionable"],
      normalized: {},
      first_seen_at: "2026-08-04T00:00:00Z",
      last_seen_at: "2026-08-04T00:00:00Z",
    },
    {
      id: "vpc-attachment",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::CEN::TransitRouterVpcAttachment",
        native_id: "tr-attach-b9owgteq5t2u1izyqx",
      },
      scope_id: "scope-global",
      resource_kind_id: "kind-cen-vpc-attachment",
      name: "Seoul VPC connection",
      location: "ap-northeast-2",
      capabilities: ["indexed", "actionable"],
      normalized: {},
      first_seen_at: "2026-08-04T00:00:00Z",
      last_seen_at: "2026-08-04T00:00:00Z",
    },
  ]);

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter initialEntries={["/cleanup/cln-cen-eni"]}>
        <LocaleProvider>
          <Routes>
            <Route path="/cleanup/:id" element={<CleanupTaskDetailHarness />} />
            <Route path="/cleanup/new" element={<div>New task builder</div>} />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  expect(
    await screen.findByText(
      "Execution blockers found: 1. Resource-level blockers are marked in Resource results.",
    ),
  ).toBeVisible();
  await user.click(screen.getByRole("tab", { name: "Resource results" }));
  expect(
    await screen.findByText(
      "This ENI is managed by a CEN intra-region connection. Add the source connection to clean it up.",
    ),
  ).toBeVisible();
  expect(screen.getByText("Blocked")).toBeVisible();
  await user.click(
    screen.getByRole("button", {
      name: "Add source resource and rebuild",
    }),
  );
  expect(await screen.findByText("New task builder")).toBeVisible();
  expect(readCleanupSelectionHandoff("connection-a")).toEqual([
    { kind: "asset", asset_id: "managed-eni" },
    { kind: "asset", asset_id: "vpc-attachment" },
  ]);
});

it("filters cleanup target resources and preserves navigation for closed target resources", async () => {
  const user = userEvent.setup();
  vi.mocked(listConnectionRegions).mockResolvedValue({
    items: [
      {
        id: "region-hangzhou",
        connection_id: "connection-a",
        region_id: "cn-hangzhou",
        name: "China East 1 (Hangzhou)",
        origin: "api",
        lifecycle: "active",
        created_at: "2026-08-03T00:00:00Z",
        updated_at: "2026-08-03T00:00:00Z",
      },
    ],
  });
  vi.mocked(getCleanupTask).mockResolvedValue({
    task: {
      id: "cln-resource",
      connection_id: "connection-a",
      status: "draft",
      selectors: [
        {
          kind: "scope",
          connection_id: "connection-a",
          scope_id: "region:cn-hangzhou",
          scope_kind: "region",
          display_name: "Production region target",
        },
      ],
      resolved_asset_ids: ["asset-stack", "asset-deleted"],
      selector_asset_ids: [["asset-stack", "asset-deleted"]],
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "not_required" },
      snapshot_hash: "snapshot-resource",
      created_by: "operator",
      created_at: "2026-08-03T00:00:00Z",
    },
    steps: [
      {
        id: "step-stack",
        cleanup_task_id: "cln-resource",
        asset_id: "asset-stack",
        kind: "direct",
        action: "delete",
      },
      {
        id: "step-deleted",
        cleanup_task_id: "cln-resource",
        asset_id: "asset-deleted",
        kind: "direct",
        action: "delete",
      },
    ],
    impact_items: [],
  });
  vi.mocked(findAssets).mockResolvedValue([
    {
      id: "asset-stack",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ALIYUN::ROS::Stack",
        native_id: "stack_2026-08-04_abcd1234",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-stack",
      name: "Production Stack",
      location: "cn-hangzhou",
      capabilities: ["indexed", "actionable"],
      first_seen_at: "2026-08-03T00:00:00Z",
      last_seen_at: "2026-08-03T00:00:00Z",
    },
    {
      id: "asset-deleted",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::ECS::Instance",
        native_id: "i-deleted",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-instance",
      name: "Deleted Instance",
      location: "cn-hangzhou",
      capabilities: ["indexed", "actionable"],
      first_seen_at: "2026-08-03T00:00:00Z",
      last_seen_at: "2026-08-03T00:00:00Z",
      closed_at: "2026-08-04T00:00:00Z",
    },
  ]);

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter initialEntries={["/cleanup/cln-resource"]}>
        <LocaleProvider>
          <Routes>
            <Route path="/cleanup/:id" element={<CleanupTaskDetailHarness />} />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  expect(
    await screen.findByRole("tab", { name: "Cleanup targets" }),
  ).toBeVisible();
  expect(
    screen.queryByRole("tab", { name: "Task review" }),
  ).not.toBeInTheDocument();
  expect(await screen.findAllByText("Production region target")).toHaveLength(
    2,
  );
  expect(screen.getByText("2 resources")).toBeVisible();

  await user.click(
    screen.getByRole("button", {
      name: "View resources in Production region target",
    }),
  );
  const targetDialog = await screen.findByRole("dialog", {
    name: "Production region target",
  });
  expect(targetDialog).toHaveClass("sm:max-w-3xl");
  expect(targetDialog).toHaveAttribute("data-slot", "dialog-content");
  expect(
    document.querySelector('[data-slot="sheet-content"]'),
  ).not.toBeInTheDocument();
  expect(
    await screen.findByRole("link", { name: "Production Stack" }),
  ).toHaveAttribute("href", "/assets/asset-stack");
  expect(screen.getByText("stack_2026-08-04_abcd1234")).toBeVisible();
  expect(
    screen.getByRole("link", { name: "Deleted Instance" }),
  ).toHaveAttribute("href", "/assets/asset-deleted");
  expect(
    screen.getByRole("textbox", {
      name: "Search name, resource ID, or type",
    }),
  ).toBeVisible();
  const targetResourceTable = within(targetDialog).getByRole("table");
  expect(
    within(targetResourceTable).getByRole("columnheader", {
      name: "Resource",
    }),
  ).toBeVisible();
  expect(
    within(targetResourceTable).getByRole("columnheader", {
      name: "Resource kind",
    }),
  ).toBeVisible();
  expect(
    within(targetResourceTable).getByRole("columnheader", {
      name: "Region",
    }),
  ).toBeVisible();
  expect(
    within(targetResourceTable).getAllByText("China East 1 (Hangzhou)"),
  ).toHaveLength(2);
  const targetStackRow = within(targetResourceTable).getByRole("row", {
    name: /Production Stack/,
  });
  expect(
    within(targetStackRow).getByText("ROS Stack").closest("td"),
  ).not.toHaveClass("text-xs", "text-muted-foreground");
  expect(
    within(targetStackRow).getByText("China East 1 (Hangzhou)").closest("td"),
  ).not.toHaveClass("text-xs", "text-muted-foreground");
  expect(
    within(targetDialog).getByRole("link", {
      name: "View in resource panorama: Production Stack",
    }),
  ).toHaveAttribute("href", "/panorama?resource=asset-stack");
  expect(
    within(targetDialog).getByRole("link", {
      name: "Cloud console: Production Stack",
    }),
  ).toHaveAttribute(
    "href",
    "https://ros.console.aliyun.com/cn-hangzhou/stacks/stack_2026-08-04_abcd1234",
  );

  const kindFilter = screen.getByRole("combobox", { name: "Resource kind" });
  await user.click(kindFilter);
  await user.click(screen.getByRole("option", { name: "ROS Stack · ROS" }));
  await user.keyboard("{Escape}");
  expect(screen.getByRole("link", { name: "Production Stack" })).toBeVisible();
  expect(
    screen.queryByRole("link", { name: "Deleted Instance" }),
  ).not.toBeInTheDocument();

  await user.click(kindFilter);
  await user.click(screen.getByRole("option", { name: "All resource types" }));
  await user.keyboard("{Escape}");
  await user.type(
    screen.getByRole("textbox", {
      name: "Search name, resource ID, or type",
    }),
    "i-deleted",
  );
  expect(screen.getByRole("link", { name: "Deleted Instance" })).toBeVisible();
  expect(
    screen.queryByRole("link", { name: "Production Stack" }),
  ).not.toBeInTheDocument();
  expect(
    within(targetDialog).getByRole("link", {
      name: "View in resource panorama: Deleted Instance",
    }),
  ).toHaveAttribute("href", "/panorama?resource=asset-deleted");
  expect(
    within(targetDialog).getByRole("link", {
      name: "Cloud console: Deleted Instance",
    }),
  ).toHaveAttribute(
    "href",
    "https://ecs.console.aliyun.com/server/cn-hangzhou?instanceId=i-deleted",
  );
  await user.keyboard("{Escape}");

  await user.click(screen.getByRole("tab", { name: "Resource results" }));

  const resourceLink = await screen.findByRole("link", {
    name: "Production Stack",
  });
  expect(resourceLink).toHaveAttribute("href", "/assets/asset-stack");
  expect(screen.getByText("stack_2026-08-04_abcd1234")).toBeVisible();
  expect(screen.queryByText("asset-stack")).not.toBeInTheDocument();
  expect(
    screen.getByRole("link", {
      name: "View in resource panorama: Production Stack",
    }),
  ).toHaveAttribute("href", "/panorama?resource=asset-stack");
  expect(
    screen.getByRole("link", { name: "Cloud console: Production Stack" }),
  ).toHaveAttribute(
    "href",
    "https://ros.console.aliyun.com/cn-hangzhou/stacks/stack_2026-08-04_abcd1234",
  );
  expect(
    screen.queryByRole("link", {
      name: "View in resource panorama: Deleted Instance",
    }),
  ).not.toBeInTheDocument();
  expect(
    screen.queryByRole("link", { name: "Cloud console: Deleted Instance" }),
  ).not.toBeInTheDocument();
  const resourceTable = screen.getByRole("table");
  expect(
    within(resourceTable)
      .getAllByRole("columnheader")
      .slice(0, 3)
      .map((header) => header.textContent),
  ).toEqual(["Resource", "Resource kind", "Region"]);
  expect(
    within(
      within(resourceTable).getByRole("row", { name: /Production Stack/ }),
    ).getByText("China East 1 (Hangzhou)"),
  ).toBeVisible();
  const resultStackRow = within(resourceTable).getByRole("row", {
    name: /Production Stack/,
  });
  expect(
    within(resultStackRow).getByText("ROS Stack").closest("td"),
  ).not.toHaveClass("text-xs", "text-muted-foreground");
  expect(
    within(resultStackRow).getByText("China East 1 (Hangzhou)").closest("td"),
  ).not.toHaveClass("text-xs", "text-muted-foreground");
  expect(within(resourceTable).getByText("ROS Stack")).toBeVisible();
  const resourceTableShell = resourceTable.closest(
    '[data-slot="data-table-shell"]',
  );
  expect(resourceTableShell).not.toBeNull();
  expect(resourceTableShell).toContainElement(
    screen.getByRole("textbox", {
      name: "Search name, resource ID, action, or status",
    }),
  );
  const resultKindFilter = screen.getByRole("combobox", {
    name: "Resource kind",
  });
  const resultStatusFilter = screen.getByRole("combobox", {
    name: "Filter by status",
  });
  expect(resultStatusFilter.parentElement).not.toHaveClass("ml-auto");
  expect(resourceTableShell).toContainElement(resultStatusFilter);
  expect(resourceTableShell).toContainElement(resultKindFilter);
  await user.click(resultKindFilter);
  await user.click(screen.getByRole("option", { name: "ROS Stack · ROS" }));
  await user.keyboard("{Escape}");
  expect(screen.getByRole("link", { name: "Production Stack" })).toBeVisible();
  expect(
    screen.queryByRole("link", { name: "Deleted Instance" }),
  ).not.toBeInTheDocument();
  expect(resourceTable.closest(".rounded-xl")).toBeNull();
});

function systemRouteTableAssets() {
  return [
    {
      id: "route-table",
      identity: {
        provider: "alicloud" as const,
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::VPC::RouteTable",
        native_id: "vtb-system",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-route-table",
      name: "System route table",
      location: "cn-hangzhou",
      capabilities: ["indexed" as const],
      normalized: {
        routeTableType: "System",
        vpcId: "vpc-production",
      },
      first_seen_at: "2026-08-03T00:00:00Z",
      last_seen_at: "2026-08-03T00:00:00Z",
    },
    {
      id: "vpc",
      identity: {
        provider: "alicloud" as const,
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::VPC::VPC",
        native_id: "vpc-production",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-vpc",
      name: "Production VPC",
      location: "cn-hangzhou",
      capabilities: ["indexed" as const, "actionable" as const],
      normalized: {},
      first_seen_at: "2026-08-03T00:00:00Z",
      last_seen_at: "2026-08-03T00:00:00Z",
    },
  ];
}

it("allows the exact confirmation name to be selected for copying", async () => {
  const user = userEvent.setup();
  vi.mocked(getCleanupTask).mockResolvedValue({
    task: {
      id: "cln-ready",
      connection_id: "connection-a",
      status: "ready",
      selectors: [
        {
          kind: "scope",
          connection_id: "connection-a",
          scope_id: "region:cn-chengdu",
          scope_kind: "region",
          display_name: "Chengdu Dedicated Cloud POC",
        },
      ],
      resolved_asset_ids: ["asset-a"],
      selector_asset_ids: [["asset-a"]],
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "complete" },
      snapshot_hash: "snapshot-ready",
      created_by: "operator",
      created_at: "2026-08-03T00:00:00Z",
    },
    steps: [
      {
        id: "step-a",
        cleanup_task_id: "cln-ready",
        asset_id: "asset-a",
        kind: "direct",
        action: "delete",
      },
    ],
    impact_items: [],
  });

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter initialEntries={["/cleanup/cln-ready"]}>
        <LocaleProvider>
          <Routes>
            <Route path="/cleanup/:id" element={<CleanupTaskDetailHarness />} />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  await user.click(
    await screen.findByRole("button", { name: "Start cleanup" }),
  );

  const confirmationLabel = screen.getByText(
    "Type the exact target name: Chengdu Dedicated Cloud POC",
  );
  expect(confirmationLabel).toHaveClass("select-text");
  expect(
    screen.getByRole("textbox", {
      name: "Type the exact target name: Chengdu Dedicated Cloud POC",
    }),
  ).toBeVisible();
});

it("shows, filters, prioritizes, and paginates all resource results", async () => {
  const user = userEvent.setup();
  const steps = Array.from({ length: 1001 }, (_, index) => ({
    id: `step-${index}`,
    cleanup_task_id: "cln-large",
    asset_id: `asset-${index}`,
    kind: "direct",
    action: "delete",
  }));
  vi.mocked(getCleanupTask).mockResolvedValue({
    task: {
      id: "cln-large",
      connection_id: "connection-a",
      status: "completed",
      selectors: [{ kind: "connection", connection_id: "connection-a" }],
      resolved_asset_ids: steps.map((step) => step.asset_id),
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "complete" },
      snapshot_hash: "snapshot-1",
      created_by: "operator",
      created_at: "2026-08-03T00:00:00Z",
    },
    steps,
    impact_items: [],
  });
  vi.mocked(listCleanupTaskExecutions).mockResolvedValue({
    items: [
      {
        id: "execution-1",
        connection_id: "connection-a",
        cleanup_task_id: "cln-large",
        status: "succeeded",
        requested_by: "operator",
        idempotency_key: "key-1",
        created_at: "2026-08-03T00:00:01Z",
        updated_at: "2026-08-03T00:02:02Z",
        duration_ms: 100_000,
        started_at: "2026-08-03T00:00:02Z",
        finished_at: "2026-08-03T00:02:02Z",
      },
    ],
  });
  vi.mocked(listExecutionActions).mockResolvedValue({
    items: steps.map((step, index) => ({
      id: `action-${index}`,
      execution_id: "execution-1",
      cleanup_task_step_id: step.id,
      asset_id: step.asset_id,
      action: "delete",
      status: index === 1000 ? "failed" : "succeeded",
      idempotency_key: `key-${index}`,
      spec_bundle_revision: "bundle-1",
      spec_hash: "spec-1",
      provider_error:
        index === 1000
          ? { category: "provider_failure", message: "delete failed" }
          : undefined,
      provider_request_id: `request-${index}`,
      created_at: "2026-08-03T00:00:02Z",
      updated_at: "2026-08-03T00:02:02Z",
    })),
  });
  vi.mocked(findAssets).mockResolvedValue(
    [0, 1000].map((index) => ({
      id: `asset-${index}`,
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::ECS::Instance",
        native_id: `resource-${index}`,
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-instance",
      name: `Resource ${index}`,
      capabilities: ["indexed", "actionable"],
      first_seen_at: "2026-08-03T00:00:00Z",
      last_seen_at: "2026-08-03T00:00:00Z",
    })),
  );
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const invalidateQueries = vi.spyOn(queryClient, "invalidateQueries");

  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={["/cleanup/cln-large"]}>
        <LocaleProvider>
          <Routes>
            <Route path="/cleanup/:id" element={<CleanupTaskDetailHarness />} />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  expect(await screen.findByText("resource-1000")).toBeVisible();
  expect(screen.getByText("Updated", { selector: "dt" })).toBeVisible();
  expect(screen.getByText("1m 40s")).toBeVisible();
  expect(
    await screen.findByRole("columnheader", { name: "Status reason" }),
  ).toBeVisible();
  expect(await screen.findByText("delete failed")).toBeVisible();
  expect(screen.queryByText("request-1000")).not.toBeInTheDocument();
  expect(invalidateQueries).toHaveBeenCalledWith({
    queryKey: ["assets", "connection-a"],
  });
  expect(await screen.findByText("resource-0")).toBeVisible();
  expect(
    screen.queryByRole("button", { name: "Show 1000 completed" }),
  ).not.toBeInTheDocument();
  expect(screen.queryByText("resource-999")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Page 1" })).toHaveAttribute(
    "aria-current",
    "page",
  );
  expect(screen.getByRole("button", { name: "Previous" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "Next" })).toBeEnabled();

  const statusFilter = screen.getByRole("combobox", {
    name: "Filter by status",
  });
  await user.click(statusFilter);
  await user.click(screen.getByRole("option", { name: "Failed" }));
  expect(await screen.findByText("resource-1000")).toBeVisible();
  expect(screen.queryByText("resource-0")).not.toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: "Next" }),
  ).not.toBeInTheDocument();
});

it("opens cleanup logs from a resource result and filters by its resource id", async () => {
  const user = userEvent.setup();
  vi.mocked(getCleanupTask).mockResolvedValue({
    task: {
      id: "cln-resource-logs",
      connection_id: "connection-a",
      status: "completed",
      selectors: [{ kind: "asset", asset_id: "asset-a" }],
      resolved_asset_ids: ["asset-a"],
      revision: {
        inventory_revision: "inventory-1",
        graph_revision: "graph-1",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
      },
      scan_coverage: { status: "complete" },
      snapshot_hash: "snapshot-resource-logs",
      created_by: "operator",
      created_at: "2026-08-03T00:00:00Z",
    },
    steps: [
      {
        id: "step-a",
        cleanup_task_id: "cln-resource-logs",
        asset_id: "asset-a",
        kind: "direct",
        action: "delete",
      },
    ],
    impact_items: [],
  });
  vi.mocked(findAssets).mockResolvedValue([
    {
      id: "asset-a",
      identity: {
        provider: "alicloud",
        partition: "public",
        connection_id: "connection-a",
        native_type: "ACS::ECS::Instance",
        native_id: "i-resource-a",
      },
      scope_id: "scope-region",
      resource_kind_id: "kind-instance",
      name: "Resource A",
      capabilities: ["indexed", "actionable"],
      first_seen_at: "2026-08-03T00:00:00Z",
      last_seen_at: "2026-08-03T00:00:00Z",
    },
  ]);
  vi.mocked(listCleanupTaskExecutions).mockResolvedValue({
    items: [
      {
        id: "execution-a",
        connection_id: "connection-a",
        cleanup_task_id: "cln-resource-logs",
        status: "succeeded",
        requested_by: "operator",
        idempotency_key: "execution-key",
        created_at: "2026-08-03T00:00:01Z",
      },
    ],
  });
  vi.mocked(listExecutionActions).mockResolvedValue({
    items: [
      {
        id: "action-a",
        execution_id: "execution-a",
        cleanup_task_step_id: "step-a",
        asset_id: "asset-a",
        action: "delete",
        status: "succeeded",
        idempotency_key: "action-key",
        spec_bundle_revision: "bundle-1",
        spec_hash: "spec-1",
        created_at: "2026-08-03T00:00:02Z",
        updated_at: "2026-08-03T00:00:03Z",
      },
    ],
  });

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter initialEntries={["/cleanup/cln-resource-logs"]}>
        <LocaleProvider>
          <Routes>
            <Route
              path="/cleanup/:id"
              element={
                <>
                  <CleanupTaskDetailHarness />
                  <LocationProbe />
                </>
              }
            />
          </Routes>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  const viewLogs = await screen.findByRole("button", {
    name: "View resource logs: Resource A",
  });
  expect(viewLogs.querySelector("svg")).toHaveClass("lucide-file-text");
  await user.click(viewLogs);

  expect(screen.getByRole("tab", { name: "Cleanup logs" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  expect(screen.getByTestId("location")).toHaveTextContent(
    "/cleanup/cln-resource-logs?tab=logs",
  );
  expect(
    screen.getByRole("textbox", { name: "Filter by resource ID" }),
  ).toHaveValue("i-resource-a");
  await waitFor(() =>
    expect(getCleanupTaskLogs).toHaveBeenCalledWith(
      "connection-a",
      "cln-resource-logs",
      "",
      { resourceID: "i-resource-a", resourceKindIDs: [] },
      expect.any(AbortSignal),
    ),
  );
});
