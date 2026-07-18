import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import {
  getCleanupTaskLogs,
  listProviderCatalog,
  streamCleanupTaskEvents,
} from "@/api/client";
import type { Asset, JobLog } from "@/api/types";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import { CleanupTaskEvents } from "./CleanupTaskEvents";

vi.mock("@/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/client")>()),
  getCleanupTaskLogs: vi.fn(),
  listProviderCatalog: vi.fn(),
  streamCleanupTaskEvents: vi.fn(),
}));

beforeEach(() => {
  localStorage.setItem("steward.locale", "en-US");
  vi.mocked(getCleanupTaskLogs).mockReset();
  vi.mocked(listProviderCatalog).mockReset();
  vi.mocked(streamCleanupTaskEvents).mockReset();
  vi.mocked(listProviderCatalog).mockResolvedValue([
    {
      provider: "alicloud",
      specs: [],
      revision: "catalog-1",
      hash: "catalog-hash-1",
      kinds_revision: "catalog-1",
      kinds: [
        {
          id: "kind-stack",
          provider: "alicloud",
          native_type: "ALIYUN::ROS::Stack",
          capabilities: ["cleanup"],
          display_name: "ROS Stack",
          bundle_revision: "catalog-1",
        },
        {
          id: "kind-instance",
          provider: "alicloud",
          native_type: "ACS::ECS::Instance",
          capabilities: ["cleanup"],
          display_name: "ECS Instance",
          bundle_revision: "catalog-1",
        },
      ],
    },
  ]);
});

it("renders one chronological terminal and tags every line with its resource type and native id", async () => {
  const user = userEvent.setup();
  const jobLogs: JobLog[] = [
    {
      id: "log-1",
      job_id: "job-1",
      aggregate_type: "cleanup_task",
      aggregate_id: "cln-1",
      target_key: "asset-a",
      sequence: 1,
      kind: "text",
      level: "info",
      message: "provider preflight started",
      created_at: "2026-08-03T10:00:00.123Z",
    },
    {
      id: "log-2",
      job_id: "job-2",
      aggregate_type: "cleanup_task",
      aggregate_id: "cln-1",
      target_key: "asset-b",
      sequence: 1,
      kind: "cloud_api_response",
      level: "info",
      message: "ecs DeleteInstance returned",
      payload: { RequestId: "req-1" },
      created_at: "2026-08-03T10:00:00.456Z",
    },
  ];
  const assetsByID = new Map<string, Asset>([
    [
      "asset-a",
      {
        id: "asset-a",
        identity: {
          provider: "alicloud",
          partition: "public",
          connection_id: "connection-a",
          native_type: "ALIYUN::ROS::Stack",
          native_id: "stack-production",
        },
        scope_id: "scope-region",
        resource_kind_id: "kind-stack",
        capabilities: ["indexed", "actionable"],
        first_seen_at: "2026-08-03T00:00:00Z",
        last_seen_at: "2026-08-03T00:00:00Z",
      },
    ],
    [
      "asset-b",
      {
        id: "asset-b",
        identity: {
          provider: "alicloud",
          partition: "public",
          connection_id: "connection-a",
          native_type: "ACS::ECS::Instance",
          native_id: "i-production",
        },
        scope_id: "scope-region",
        resource_kind_id: "kind-instance",
        capabilities: ["indexed", "actionable"],
        first_seen_at: "2026-08-03T00:00:00Z",
        last_seen_at: "2026-08-03T00:00:00Z",
      },
    ],
  ]);
  vi.mocked(getCleanupTaskLogs).mockImplementation(
    async (_connectionID, _taskID, _before, requestedFilters) => {
      const filters = requestedFilters ?? {};
      return {
        items: jobLogs.filter((log) => {
          const target = assetsByID.get(log.target_key ?? "");
          const resourceID = filters.resourceID?.toLowerCase() ?? "";
          return (
            (!resourceID ||
              target?.identity.native_id.toLowerCase().includes(resourceID)) &&
            (!filters.resourceKindIDs?.length ||
              (target &&
                filters.resourceKindIDs.includes(target.resource_kind_id)))
          );
        }),
      };
    },
  );

  render(
    <QueryClientProvider client={new QueryClient()}>
      <LocaleProvider>
        <CleanupTaskEvents
          connectionID="connection-a"
          taskID="cln-1"
          live={false}
          assetsByID={assetsByID}
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );
  await act(async () => Promise.resolve());

  const terminal = screen.getByRole("log");
  expect(terminal).toHaveTextContent(
    "INFO [ROS Stack] [stack-production] provider preflight started",
  );
  expect(terminal).toHaveTextContent(
    'INFO [ECS Instance] [i-production] ecs DeleteInstance returned {"RequestId":"req-1"}',
  );
  expect(terminal).toHaveClass("bg-neutral-950", "overflow-auto");
  expect(streamCleanupTaskEvents).not.toHaveBeenCalled();

  const resourceIDFilter = screen.getByRole("textbox", {
    name: "Filter by resource ID",
  });
  await user.type(resourceIDFilter, "i-production");
  await waitFor(() => {
    expect(getCleanupTaskLogs).toHaveBeenLastCalledWith(
      "connection-a",
      "cln-1",
      "",
      { resourceID: "i-production", resourceKindIDs: [] },
      expect.any(AbortSignal),
    );
    expect(terminal).not.toHaveTextContent("provider preflight started");
    expect(terminal).toHaveTextContent("ecs DeleteInstance returned");
  });

  await user.clear(resourceIDFilter);
  await user.click(screen.getByRole("combobox", { name: "Resource kind" }));
  await user.click(await screen.findByRole("option", { name: /ROS Stack/ }));
  await waitFor(() => {
    expect(getCleanupTaskLogs).toHaveBeenLastCalledWith(
      "connection-a",
      "cln-1",
      "",
      { resourceID: "", resourceKindIDs: ["kind-stack"] },
      expect.any(AbortSignal),
    );
    expect(terminal).toHaveTextContent("provider preflight started");
    expect(terminal).not.toHaveTextContent("ecs DeleteInstance returned");
  });
});
