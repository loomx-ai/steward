import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { findAssets, listAuditEvents } from "@/api/client";
import type { Asset, AuditEvent } from "@/api/types";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import {
  AuditsView,
  filterAuditEvents,
  sortAuditEventsNewestFirst,
} from "./AuditsView";

vi.mock("@/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/client")>()),
  findAssets: vi.fn(),
  listAuditEvents: vi.fn(),
}));

vi.mock("@/connections/ActiveConnectionProvider", () => ({
  useRequiredConnection: () => ({
    id: "connection-a",
    name: "Production",
    provider: "alicloud",
  }),
}));

const events: AuditEvent[] = [
  {
    id: "audit-validation",
    connection_id: "connection-a",
    actor: "local-admin",
    action: "connection.validate",
    target_type: "cloud_connection",
    target_id: "connection-a",
    result: "validated",
    request_id: "app-request-validation",
    evidence: {
      provider: "alicloud",
      root_scope_count: 2,
      provider_request_id: "provider-request-validation",
    },
    created_at: "2026-08-04T09:44:12Z",
  },
  {
    id: "audit-scan",
    connection_id: "connection-a",
    actor: "system",
    action: "inventory.scan.create",
    target_type: "scan_run",
    target_id: "scan-a",
    result: "accepted",
    evidence: {
      target_count: 3,
      region_count: 2,
      shard_count: 4,
      scope_mode: "selected_regions",
    },
    created_at: "2026-08-04T09:46:37Z",
  },
];

const asset: Asset = {
  id: "asset-a",
  identity: {
    provider: "alicloud",
    partition: "aliyun",
    connection_id: "connection-a",
    native_type: "ACS::ECS::Disk",
    native_id: "d-6we5ypd9as5am03yq0pv",
  },
  scope_id: "scope-a",
  resource_kind_id: "alicloud:ACS::ECS::Disk",
  name: "data-disk",
  capabilities: [],
  first_seen_at: "2026-08-04T09:00:00Z",
  last_seen_at: "2026-08-04T09:00:00Z",
};

beforeEach(() => {
  localStorage.setItem("steward.locale", "zh-CN");
  vi.mocked(findAssets).mockReset().mockResolvedValue([]);
  vi.mocked(listAuditEvents).mockReset().mockResolvedValue({ items: events });
  vi.stubGlobal(
    "ResizeObserver",
    class ResizeObserver {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
});

it("shows an asset target name and resource ID without a resource icon", async () => {
  vi.mocked(listAuditEvents).mockResolvedValue({
    items: [
      {
        id: "audit-asset",
        connection_id: "connection-a",
        actor: "local-admin",
        action: "cleanup.action.succeeded",
        target_type: "asset",
        target_id: "asset-a",
        result: "succeeded",
        created_at: "2026-08-04T09:47:00Z",
      },
    ],
  });
  vi.mocked(findAssets).mockResolvedValue([asset]);

  const { container } = renderView();

  expect(await screen.findByText("data-disk")).toBeVisible();
  expect(screen.getByText("d-6we5ypd9as5am03yq0pv")).toBeVisible();
  expect(screen.queryByText("asset-a")).not.toBeInTheDocument();
  expect(container.querySelector("[data-resource-icon-mask]")).toBeNull();
  expect(container.querySelector(".lucide-hard-drive")).toBeNull();
  expect(screen.getByRole("link", { name: /data-disk/ })).toHaveAttribute(
    "href",
    "/assets/asset-a",
  );
});

function renderView() {
  return render(
    <MemoryRouter>
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <LocaleProvider>
          <AuditsView />
        </LocaleProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

it("searches audit events across localized action and evidence values", () => {
  expect(
    filterAuditEvents(events, "验证云连接", (value) =>
      value === "connection.validate" ? "验证云连接" : value,
    ),
  ).toEqual([events[0]]);
  expect(filterAuditEvents(events, "provider-request-validation")).toEqual([
    events[0],
  ]);
});

it("orders audit events from newest to oldest without mutating the response", () => {
  expect(sortAuditEventsNewestFirst(events).map((event) => event.id)).toEqual([
    "audit-scan",
    "audit-validation",
  ]);
  expect(events.map((event) => event.id)).toEqual([
    "audit-validation",
    "audit-scan",
  ]);
});

it("translates audit values, links supported targets, and filters the list", async () => {
  const user = userEvent.setup();
  renderView();

  expect(await screen.findByText("验证云连接")).toBeVisible();
  expect(screen.getByText("创建资源扫描")).toBeVisible();
  expect(screen.getByText("验证通过")).toBeVisible();
  expect(screen.getByText("系统")).toBeVisible();
  expect(screen.queryByText("connection.validate")).not.toBeInTheDocument();
  expect(screen.queryByText("scan run")).not.toBeInTheDocument();
  expect(
    screen.getByRole("link", { name: /云连接.*connection-a/ }),
  ).toHaveAttribute("href", "/settings?section=connections");
  expect(
    screen.getByRole("link", { name: /资源扫描.*scan-a/ }),
  ).toHaveAttribute("href", "/scans/scan-a");

  const search = screen.getByRole("textbox", {
    name: "搜索操作人、动作、目标、结果或请求 ID",
  });
  await user.type(search, "local-admin");
  expect(screen.getByText("验证云连接")).toBeVisible();
  expect(screen.queryByText("创建资源扫描")).not.toBeInTheDocument();

  await user.clear(search);
  await user.type(search, "不存在的审计事件");
  expect(await screen.findByText("没有符合搜索条件的审计事件。")).toBeVisible();
});

it("opens a human-readable action detail without exposing JSON", async () => {
  const user = userEvent.setup();
  renderView();

  await user.click(await screen.findByText("local-admin"));
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

  await user.click(
    await screen.findByRole("button", {
      name: "详情: 验证云连接 connection-a",
    }),
  );

  const dialog = await screen.findByRole("dialog");
  expect(dialog).toHaveAttribute("data-slot", "dialog-content");
  expect(dialog).toHaveClass("left-[50%]", "top-[50%]");
  expect(dialog).not.toHaveAttribute("data-side", "right");
  expect(
    within(dialog).getByRole("heading", { name: "验证云连接" }),
  ).toBeVisible();
  const dialogHeader = dialog.querySelector<HTMLElement>(
    '[data-slot="dialog-header"]',
  );
  expect(dialogHeader).not.toBeNull();
  expect(within(dialogHeader!).getByText("验证通过")).toBeVisible();
  const dialogDescription = dialog.querySelector<HTMLElement>(
    '[data-slot="dialog-description"]',
  );
  expect(dialogDescription).toHaveClass("sr-only");
  expect(within(dialog).getByText("动作详情")).toBeVisible();
  expect(within(dialog).getByText("阿里云")).toBeVisible();
  expect(within(dialog).getByText("根范围数")).toBeVisible();
  expect(within(dialog).getByText("云厂商请求 ID")).toBeVisible();
  expect(within(dialog).getByText("provider-request-validation")).toBeVisible();
  expect(within(dialog).getByText("app-request-validation")).toBeVisible();
  expect(
    within(dialog).queryByRole("button", { name: "显示 JSON" }),
  ).not.toBeInTheDocument();
  expect(
    within(dialog).queryByText("provider_request_id"),
  ).not.toBeInTheDocument();
  const timeFact = within(dialog).getByText("时间").parentElement;
  expect(timeFact).toHaveClass("grid", "grid-cols-[7rem_minmax(0,1fr)]");
  expect(timeFact?.querySelector("dd")).toBeVisible();

  await waitFor(() =>
    expect(
      within(dialog).getByRole("link", {
        name: "云连接 · connection-a",
      }),
    ).toHaveAttribute("href", "/settings?section=connections"),
  );

  await user.click(
    within(dialog).getByRole("button", { name: "关闭审计详情" }),
  );
  await waitFor(() => expect(dialog).not.toBeInTheDocument());
  expect(
    screen.getByRole("button", {
      name: "详情: 验证云连接 connection-a",
    }),
  ).toHaveFocus();
});
