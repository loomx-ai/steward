import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  findAsset,
  findAssetsByNativeIDs,
  listProviderCatalog,
  setAssetDirty,
} from "@/api/client";
import type {
  Asset,
  ProviderCatalogBundle,
  ResourceKind,
  TopologyResource,
} from "@/api/types";
import { TooltipProvider } from "@/components/ui/tooltip";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import {
  ResourceDetailDialog,
  resourcePropertyRows,
} from "./ResourceDetailDialog";
import { resourcePropertyLabel } from "./resourceProperties";
import { ScopeDetailDialog } from "./ScopeDetailDialog";
import { StackMembersDialog } from "./StackMembersDialog";

vi.mock("@/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/client")>()),
  findAsset: vi.fn(),
  findAssetsByNativeIDs: vi.fn(),
  listProviderCatalog: vi.fn(),
  setAssetDirty: vi.fn(),
}));

const resource: TopologyResource = {
  key: "resource:ecs:i-123",
  asset_id: "asset-123",
  resource_kind_id: "ecs-instance",
  name: "production-api",
  native_id: "i-123",
  type_name: "ECS Instance",
  icon: "/icons/ecs.svg",
  domain: "compute",
  finding_count: 4,
  actionable: false,
  cleanup: {
    selectable: false,
    selector_kind: "asset",
    selector_key: "asset-123",
    potential_blockers: 2,
  },
};

const asset: Asset = {
  id: "asset-123",
  identity: {
    provider: "alicloud",
    partition: "aliyun",
    connection_id: "connection-a",
    native_type: "ALIYUN::ECS::Instance",
    native_id: "i-123",
  },
  scope_id: "cn-hangzhou",
  resource_kind_id: "ecs-instance",
  name: "production-api",
  location: "cn-hangzhou-h",
  capabilities: ["inventory", "cleanup"],
  normalized: {
    instance_name: "production-api",
    network_config: { vpc_id: "vpc-a", private_ip: "10.0.0.8" },
    instance_charge_type: "PostPaid",
    cpu: 4,
    tags: ["api", "production"],
  },
  first_seen_at: "2026-07-30T00:00:00Z",
  last_seen_at: "2026-07-30T01:00:00Z",
};

const kind: ResourceKind = {
  id: "ecs-instance",
  provider: "alicloud",
  native_type: "ALIYUN::ECS::Instance",
  capabilities: ["inventory", "cleanup"],
  display_name: "ECS Instance",
  display_names: {
    "en-US": "ECS Instance",
    "zh-CN": "ECS 实例",
  },
  field_display_names: {
    instance_name: {
      "en-US": "Instance name",
      "zh-CN": "实例名称",
    },
    network_config: {
      "en-US": "Network configuration",
      "zh-CN": "网络配置",
    },
    instance_charge_type: {
      "en-US": "Billing method",
    },
  },
  summary_fields: ["instance_name", "network_config"],
  bundle_revision: "catalog-1",
};

const catalog: ProviderCatalogBundle[] = [
  {
    provider: "alicloud",
    revision: "catalog-1",
    hash: "hash-1",
    kinds: [kind],
    kinds_revision: "catalog-1",
    specs: [
      {
        resource_kind: kind,
        revision: "catalog-1",
        hash: "hash-1",
        definition: {
          metadata: {
            provider: "alicloud",
            nativeType: "ALIYUN::ECS::Instance",
          },
          scope: { kind: "region" },
          discovery: { source: "ecs" },
        },
      },
    ],
  },
];

beforeEach(() => {
  localStorage.setItem("steward.locale", "zh-CN");
  vi.stubGlobal(
    "ResizeObserver",
    class ResizeObserver {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  vi.mocked(findAsset).mockReset().mockResolvedValue(asset);
  vi.mocked(findAssetsByNativeIDs).mockReset().mockResolvedValue([]);
  vi.mocked(listProviderCatalog).mockReset().mockResolvedValue(catalog);
  vi.mocked(setAssetDirty)
    .mockReset()
    .mockImplementation(async (_connectionID, _id, dirty) => ({
      ...asset,
      dirty,
    }));
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function renderDialog(
  resourceValue: TopologyResource = resource,
  props: Partial<React.ComponentProps<typeof ResourceDetailDialog>> = {},
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const callbacks = {
    onAddToCleanup: vi.fn(),
    onRemoveFromCleanup: vi.fn(),
    onOpenChange: vi.fn(),
  };
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <LocaleProvider>
          <TooltipProvider>
            <ResourceDetailDialog
              open
              connectionId="connection-a"
              resource={resourceValue}
              pendingCleanup={false}
              {...callbacks}
              {...props}
            />
          </TooltipProvider>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return callbacks;
}

it("shows inherited cleanup as unavailable instead of offering a no-op removal", () => {
  localStorage.setItem("steward.locale", "en-US");
  renderDialog(resource, {
    pendingCleanup: true,
    inheritedCleanup: true,
  });

  expect(
    screen.getByRole("button", {
      name: "Included by broader cleanup selection",
    }),
  ).toBeDisabled();
  expect(
    screen.queryByRole("button", { name: "Remove from resource list" }),
  ).not.toBeInTheDocument();
});

it("marks a dirty resource from the panorama detail", async () => {
  const user = userEvent.setup();
  renderDialog();

  const dirtyButton = await screen.findByRole("button", {
    name: "标记为脏资源",
  });
  for (const button of [
    dirtyButton,
    screen.getByRole("link", { name: "打开资源详情" }),
    screen.getByRole("button", { name: "加入资源清单" }),
  ]) {
    expect(button).toHaveAttribute("data-size", "default");
  }

  await user.click(dirtyButton);
  await waitFor(() =>
    expect(setAssetDirty).toHaveBeenCalledWith(
      "connection-a",
      "asset-123",
      true,
    ),
  );
});

it("orders preferred fields before summary and alphabetical properties", () => {
  expect(resourcePropertyRows(asset, kind, "zh-CN")).toEqual([
    { path: "$nativeId", label: "ID", value: "i-123" },
    {
      path: "instance_name",
      label: "实例名称",
      value: "production-api",
    },
    { path: "tags", label: "标签", value: ["api", "production"] },
    {
      path: "network_config",
      label: "网络配置",
      value: { vpc_id: "vpc-a", private_ip: "10.0.0.8" },
    },
    { path: "cpu", label: "CPU", value: 4 },
    {
      path: "instance_charge_type",
      label: "实例计费类型",
      value: "PostPaid",
    },
  ]);
});

it("prioritizes useful fields, hides internal and empty values, and puts time last", () => {
  expect(
    resourcePropertyRows(
      {
        ...asset,
        normalized: {
          createTime: "2024-07-30T03:02:34Z",
          emptyArray: [],
          accountId: "1754580903499898",
          deleted: false,
          location: "us-east-1",
          other: "value",
          tags: { app: "test" },
          resourceGroupId: "rg-a",
          zone_id: "us-east-1b",
          zoneId: "us-east-1b",
          vswitch_id: "vsw-a",
          vpc_id: "vpc-a",
          name: "production-api",
          expireTime: "",
        },
      },
      kind,
      "zh-CN",
    ).map(({ path, label }) => ({ path, label })),
  ).toEqual([
    { path: "$nativeId", label: "ID" },
    { path: "name", label: "名称" },
    { path: "vpc_id", label: "所属专有网络" },
    { path: "vswitch_id", label: "所属交换机" },
    { path: "zone_id", label: "可用区" },
    { path: "resourceGroupId", label: "资源组 ID" },
    { path: "tags", label: "标签" },
    { path: "other", label: "Other" },
    { path: "createTime", label: "创建时间" },
  ]);
});

it("expands resource-center configuration and removes duplicate or internal fields", () => {
  expect(
    resourcePropertyRows(
      {
        ...asset,
        normalized: {
          _inventory_source: "resource-center",
          accountId: "1754580903499898",
          name: "test-bucket",
          createdAt: "2025-08-08T09:41:52.000Z",
          expireTime: "",
          tags: { environment: "test" },
          status: "Active",
          configuration: {
            AccessControlList: { Grant: "private" },
            AllowEmptyReferer: "true",
            BucketId: "i-123",
            BucketName: "test-bucket",
            BucketPolicy: { LogBucket: "", LogPrefix: "" },
            CreationDate: "2025-08-08T09:41:52.000Z",
            ExpireDate: "2027-08-08T09:41:52.000Z",
            Location: "oss-cn-beijing",
            Name: "test-bucket",
            Status: "Active",
            TagSet: {
              Tag: [{ Key: "environment", Value: "test" }],
            },
          },
        },
      },
      undefined,
      "zh-CN",
    ).map(({ path, label }) => ({ path, label })),
  ).toEqual([
    { path: "$nativeId", label: "ID" },
    { path: "name", label: "名称" },
    { path: "tags", label: "标签" },
    {
      path: "configuration.AccessControlList",
      label: "访问控制列表",
    },
    {
      path: "configuration.AllowEmptyReferer",
      label: "允许空来源",
    },
    { path: "createdAt", label: "创建时间" },
    {
      path: "configuration.ExpireDate",
      label: "到期日期",
    },
  ]);
});

it("keeps VPC and vSwitch references together and localizes route-table fields", () => {
  const rows = resourcePropertyRows(
    {
      ...asset,
      identity: {
        ...asset.identity,
        native_type: "ACS::VPC::RouteTable",
        native_id: "vtb-a",
      },
      normalized: {
        vpcId: "vpc-a",
        resourceGroupId: "rg-a",
        configuration: {
          AssociateType: "VSwitch",
          OwnerId: 1234,
          RouteEntrys: { RouteEntry: [{ DestinationCidrBlock: "10.0.0.0/8" }] },
          RoutePropagationEnable: true,
          RouterType: "VRouter",
          VSwitchIds: { VSwitchId: ["vsw-a"] },
          VpcId: "vpc-a",
        },
      },
    },
    undefined,
    "zh-CN",
  );

  expect(rows.slice(0, 4).map(({ path, label }) => ({ path, label }))).toEqual([
    { path: "$nativeId", label: "ID" },
    { path: "vpcId", label: "所属专有网络" },
    { path: "configuration.VSwitchIds", label: "所属交换机" },
    { path: "resourceGroupId", label: "资源组 ID" },
  ]);
  expect(
    rows
      .filter((row) => row.path.startsWith("configuration."))
      .map((row) => row.label),
  ).toEqual(
    expect.arrayContaining([
      "关联类型",
      "所有者 ID",
      "路由条目",
      "路由传播",
      "路由器类型",
    ]),
  );
  expect(
    resourcePropertyLabel("configuration.Priority", undefined, "zh-CN"),
  ).toBe("优先级");
  expect(
    resourcePropertyLabel("configuration.ACKNetworkPolicy", undefined, "zh-CN"),
  ).toBe("ACK网络策略");
});

it("uses the wrapped collection length and keeps its items progressively disclosed", async () => {
  vi.mocked(findAsset).mockResolvedValue({
    ...asset,
    normalized: {
      name: "production-api",
      configuration: {
        Permissions: {
          Permission: [
            { IpProtocol: "TCP", PortRange: "80/80" },
            { IpProtocol: "TCP", PortRange: "443/443" },
            { IpProtocol: "ICMP", PortRange: "-1/-1" },
          ],
        },
      },
    },
  });
  renderDialog();
  const user = userEvent.setup();

  const properties = await screen.findByRole("region", {
    name: "资源属性",
  });
  const permissions = within(properties).getByText("权限", {
    exact: true,
  });
  const row = permissions.closest("div");
  expect(row).not.toBeNull();
  const disclosure = within(row as HTMLElement).getByRole("button", {
    name: "查看权限详情，共 3 项",
  });
  expect(disclosure).toBeVisible();
  expect(screen.queryByText("80/80")).not.toBeInTheDocument();

  await user.click(disclosure);

  const details = screen.getByRole("dialog", { name: "权限" });
  expect(
    within(details).getByRole("button", { name: "查看权限第 1 项" }),
  ).toHaveTextContent("TCP · 80/80");
  expect(
    within(details).getByRole("button", { name: "查看权限第 2 项" }),
  ).toHaveTextContent("TCP · 443/443");
  expect(
    within(details).getByRole("button", { name: "查看权限第 3 项" }),
  ).toHaveTextContent("ICMP · -1/-1");
  expect(within(details).queryByText("IP协议")).not.toBeInTheDocument();
  expect(details).not.toHaveTextContent(
    '{"IpProtocol":"TCP","PortRange":"80/80"}',
  );

  await user.click(
    within(details).getByRole("button", { name: "查看权限第 1 项" }),
  );

  const itemDetails = screen.getByRole("dialog", {
    name: "权限 · 第 1 项",
  });
  const backToList = within(itemDetails).getByRole("button", {
    name: "返回权限列表",
  });
  expect(backToList).toHaveFocus();
  expect(within(itemDetails).getByText("IP协议")).toBeVisible();
  expect(within(itemDetails).getByText("80/80")).toBeVisible();
  expect(within(itemDetails).queryByText("443/443")).not.toBeInTheDocument();

  await user.click(backToList);
  expect(
    within(screen.getByRole("dialog", { name: "权限" })).getByRole("button", {
      name: "查看权限第 1 项",
    }),
  ).toHaveFocus();
});

it("links scanned resource references and exposes the resource-property copy action", async () => {
  vi.mocked(findAsset).mockResolvedValue({
    ...asset,
    resource_kind_id: "route-table",
    identity: {
      ...asset.identity,
      native_type: "ACS::VPC::RouteTable",
      native_id: "vtb-a",
    },
    normalized: {
      vpcId: "vpc-a",
      configuration: {
        RouteEntrys: {
          RouteEntry: [{ DestinationCidrBlock: "10.0.0.0/8" }],
        },
        VSwitchIds: { VSwitchId: ["vsw-a"] },
      },
    },
  });
  vi.mocked(findAssetsByNativeIDs).mockResolvedValue([
    {
      ...asset,
      id: "asset-vpc",
      name: "test-vpc",
      identity: {
        ...asset.identity,
        native_type: "ACS::VPC::VPC",
        native_id: "vpc-a",
      },
    },
    {
      ...asset,
      id: "asset-vswitch",
      name: "test-vswitch",
      identity: {
        ...asset.identity,
        native_type: "ACS::VPC::VSwitch",
        native_id: "vsw-a",
      },
    },
  ]);
  const user = userEvent.setup();
  renderDialog();

  const properties = await screen.findByRole("region", {
    name: "资源属性",
  });
  expect(
    await within(properties).findByRole("link", { name: "vpc-a" }),
  ).toHaveAttribute("href", "/assets/asset-vpc");
  expect(
    within(properties).getByRole("link", { name: "vsw-a" }),
  ).toHaveAttribute("href", "/assets/asset-vswitch");
  expect(
    within(properties).getByRole("button", {
      name: "查看路由条目详情，共 1 项",
    }),
  ).toBeVisible();

  const copyProperties = within(properties).getByRole("button", {
    name: "复制资源属性",
  });
  await user.hover(copyProperties);
  expect(await screen.findByRole("tooltip")).toHaveTextContent("复制资源属性");
});

it("shows only resource-owned identity, location, and normalized properties", async () => {
  const callbacks = renderDialog(resource, {
    regionName: "华东 1（杭州）",
  });
  const user = userEvent.setup();

  const dialog = await screen.findByRole("dialog");
  await within(dialog).findByText("ECS 实例");
  expect(
    within(dialog).getByRole("heading", { name: "production-api" }),
  ).toBeVisible();
  expect(dialog.querySelector("[data-resource-icon-mask]")).toHaveStyle({
    maskImage: "url(/icons/ecs.svg)",
  });
  expect(
    dialog.querySelector('[data-slot="dialog-description"]'),
  ).not.toBeInTheDocument();
  expect(dialog).toHaveTextContent("production-api");
  expect(dialog).toHaveTextContent("i-123");
  expect(dialog).toHaveTextContent("ECS 实例");
  expect(dialog).toHaveTextContent("华东 1（杭州）");
  expect(dialog).not.toHaveTextContent("cn-hangzhou-h");

  const properties = within(dialog).getByRole("region", {
    name: "资源属性",
  });
  const topLevelTerms = Array.from(
    properties.querySelectorAll(":scope > dl > div > dt"),
  ).map((term) => term.textContent);
  expect(topLevelTerms).toEqual([
    "ID",
    "实例名称",
    "标签",
    "网络配置",
    "CPU",
    "实例计费类型",
  ]);
  expect(properties).not.toHaveTextContent(
    '{"vpc_id":"vpc-a","private_ip":"10.0.0.8"}',
  );
  const networkConfiguration = within(properties).getByText("网络配置");
  const networkProperty = networkConfiguration.closest("div");
  expect(networkProperty).not.toBeNull();
  expect(
    within(networkProperty as HTMLElement).getByText("共 2 项"),
  ).toBeVisible();
  await user.click(
    within(networkProperty as HTMLElement).getByRole("button", {
      name: "查看网络配置详情，共 2 项",
    }),
  );
  const networkDetails = screen.getByRole("dialog", { name: "网络配置" });
  expect(within(networkDetails).getByText("所属专有网络")).toBeVisible();
  expect(within(networkDetails).getByText("私网IP")).toBeVisible();
  expect(networkDetails).toHaveTextContent("vpc-a");
  expect(networkDetails).toHaveTextContent("10.0.0.8");
  await user.keyboard("{Escape}");
  expect(within(properties).getByText("api")).toHaveAttribute(
    "data-slot",
    "badge",
  );
  expect(within(properties).getByText("production")).toHaveAttribute(
    "data-slot",
    "badge",
  );
  expect(properties).not.toHaveTextContent('["api","production"]');
  expect(
    within(properties).getByRole("button", { name: "复制网络配置" }),
  ).toBeInTheDocument();
  expect(
    within(properties).getByRole("button", { name: "复制标签" }),
  ).toBeInTheDocument();
  expect(
    within(properties).getByRole("button", { name: "复制实例名称" }),
  ).toBeInTheDocument();
  expect(
    within(properties).getByRole("button", { name: "复制CPU" }),
  ).toBeInTheDocument();
  expect(
    within(properties).getByRole("button", {
      name: "复制实例计费类型",
    }),
  ).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "打开资源详情" })).toHaveAttribute(
    "href",
    "/assets/asset-123",
  );
  await user.click(screen.getByRole("button", { name: "加入资源清单" }));
  expect(callbacks.onAddToCleanup).toHaveBeenCalledOnce();

  for (const forbidden of [
    "扫描覆盖",
    "治理发现",
    "可清理资源",
    "阻断项",
    "清理策略",
  ]) {
    expect(dialog).not.toHaveTextContent(forbidden);
  }
});

it("uses runtime-only catalog kinds while hiding account IDs", async () => {
  const ossKind: ResourceKind = {
    id: "alicloud:ACS::OSS::Bucket",
    provider: "alicloud",
    native_type: "ACS::OSS::Bucket",
    capabilities: ["indexed"],
    display_name: "OSS Bucket",
    display_names: {
      "en-US": "OSS Bucket",
      "zh-CN": "OSS Bucket",
    },
    field_display_names: {
      accountId: {
        "en-US": "Account ID",
        "zh-CN": "账号 ID",
      },
    },
    bundle_revision: "runtime-catalog-1",
  };
  vi.mocked(findAsset).mockResolvedValue({
    ...asset,
    identity: {
      ...asset.identity,
      native_type: "ACS::OSS::Bucket",
      native_id: "bucket-a",
    },
    resource_kind_id: ossKind.id,
    name: "bucket-a",
    normalized: {
      accountId: "123456789",
      storage_class: "Standard",
    },
  });
  vi.mocked(listProviderCatalog).mockResolvedValue([
    {
      ...catalog[0],
      kinds_revision: "runtime-catalog-1",
      kinds: [kind, ossKind],
    },
  ]);

  renderDialog({
    ...resource,
    key: "resource:oss:bucket-a",
    asset_id: "asset-bucket-a",
    resource_kind_id: ossKind.id,
    name: "bucket-a",
    native_id: "bucket-a",
    type_name: "OSS Bucket",
    domain: "storage",
  });

  const properties = await screen.findByRole("region", {
    name: "资源属性",
  });
  expect(
    within(properties)
      .getAllByRole("term")
      .map((term) => term.textContent),
  ).toEqual(["存储规格"]);
  expect(within(properties).queryByRole("term", { name: "ID" })).toBeNull();
  expect(
    within(properties).queryByRole("button", { name: "复制账号 ID" }),
  ).not.toBeInTheDocument();
  expect(properties).not.toHaveTextContent("123456789");
});

it("renders tags as badges and timestamps in the browser time zone", async () => {
  const createTime = "2024-07-30T03:02:34Z";
  vi.mocked(findAsset).mockResolvedValue({
    ...asset,
    normalized: {
      name: "production-api",
      tags: {
        app: "test",
        "acs:ros:stackId": "stack-a",
        MultiVSwitches: "",
      },
      createTime,
      expireTime: null,
    },
  });
  renderDialog();

  const properties = await screen.findByRole("region", {
    name: "资源属性",
  });
  expect(
    within(properties)
      .getAllByRole("term")
      .map((term) => term.textContent),
  ).toEqual(["ID", "名称", "标签", "创建时间"]);
  for (const text of [
    "app: test",
    "acs:ros:stackId: stack-a",
    "MultiVSwitches",
  ]) {
    expect(within(properties).getByText(text)).toHaveAttribute(
      "data-slot",
      "badge",
    );
  }
  const formatter = new Intl.DateTimeFormat("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hourCycle: "h23",
  });
  const parts = Object.fromEntries(
    formatter
      .formatToParts(new Date(createTime))
      .filter((part) => part.type !== "literal")
      .map((part) => [part.type, part.value]),
  );
  const localTime = `${parts.year}-${parts.month}-${parts.day} ${parts.hour}:${parts.minute}:${parts.second}`;
  expect(within(properties).getByText(localTime).tagName).toBe("TIME");
  expect(properties).not.toHaveTextContent(createTime);
  expect(properties).not.toHaveTextContent("到期时间");
});

it("reveals accessible copy actions on property hover or keyboard focus", async () => {
  const user = userEvent.setup();
  const writeText = vi
    .spyOn(navigator.clipboard, "writeText")
    .mockResolvedValue(undefined);
  vi.mocked(findAsset).mockResolvedValue({
    ...asset,
    normalized: {
      long_id: "resource-identifier-with-a-long-scalar-value",
      enabled: false,
      retries: 0,
    },
  });
  renderDialog();

  const properties = await screen.findByRole("region", {
    name: "资源属性",
  });
  const copyLongID = within(properties).getByRole("button", {
    name: "复制长整型ID",
  });
  expect(copyLongID).toHaveClass(
    "opacity-0",
    "group-hover:opacity-100",
    "focus-visible:opacity-100",
  );
  expect(copyLongID.closest("dd")?.parentElement).toHaveClass("group");
  expect(copyLongID.querySelector(".lucide-copy")).toBeInTheDocument();
  expect(
    within(properties).getByRole("button", { name: "复制已启用" }),
  ).toHaveClass("opacity-0", "group-hover:opacity-100");
  expect(
    within(properties).getByRole("button", { name: "复制重试" }),
  ).toHaveClass("opacity-0", "group-hover:opacity-100");

  await user.click(copyLongID);

  expect(writeText).toHaveBeenCalledWith(
    "resource-identifier-with-a-long-scalar-value",
  );
  const copiedLongID = within(properties).getByRole("button", {
    name: "已复制长整型ID",
  });
  expect(copiedLongID.querySelector(".lucide-check")).toHaveClass(
    "text-success",
  );
  expect(copiedLongID.querySelector(".lucide-copy")).not.toBeInTheDocument();
});

it("shows an inline error and retries loading details", async () => {
  vi.mocked(findAsset)
    .mockReset()
    .mockRejectedValueOnce(new Error("details unavailable"))
    .mockResolvedValueOnce(asset);
  const user = userEvent.setup();
  renderDialog();

  expect(await screen.findByText("details unavailable")).toBeVisible();
  await user.click(screen.getByRole("button", { name: "重试详情" }));

  await waitFor(() => expect(findAsset).toHaveBeenCalledTimes(2));
  expect(await screen.findByRole("region", { name: "资源属性" })).toBeVisible();
});

it("prefers a topology current-locale type when the catalog locale is missing", async () => {
  vi.mocked(listProviderCatalog).mockResolvedValue([
    {
      ...catalog[0],
      kinds: [
        {
          ...kind,
          display_name: "Catalog default prose",
          display_names: { "en-US": "Catalog English" },
        },
      ],
    },
  ]);
  renderDialog({
    ...resource,
    type_name: "Topology default prose",
    type_names: { "zh-CN": "拓扑资源类型" },
  });

  await screen.findByRole("region", { name: "资源属性" });
  const dialog = screen.getByRole("dialog");
  expect(dialog).toHaveTextContent("拓扑资源类型");
  expect(dialog).not.toHaveTextContent("Catalog default prose");
});

it("keeps the native type when no zh-CN resource type exists", async () => {
  vi.mocked(listProviderCatalog).mockResolvedValue([
    {
      ...catalog[0],
      kinds: [
        {
          ...kind,
          display_name: "Catalog default prose",
          display_names: { "en-US": "Catalog English" },
        },
      ],
    },
  ]);
  renderDialog({
    ...resource,
    type_name: "Topology default prose",
    type_names: { "en-US": "Topology English" },
  });

  await screen.findByRole("region", { name: "资源属性" });
  const dialog = screen.getByRole("dialog");
  expect(dialog).toHaveTextContent("ALIYUN::ECS::Instance");
  expect(dialog).not.toHaveTextContent("Catalog default prose");
  expect(dialog).not.toHaveTextContent("Topology default prose");
});

it("limits VPC scope details to kind, resource ID, and resource count", () => {
  render(
    <LocaleProvider>
      <ScopeDetailDialog
        open
        onOpenChange={() => {}}
        kind="vpc"
        entry={{
          key: "vpc:vpc-a",
          name: "Application VPC",
          native_id: "vpc-a",
          resource_count: 7,
          cleanup: {
            selectable: false,
            potential_blockers: 2,
          },
        }}
      />
    </LocaleProvider>,
  );

  const dialog = screen.getByRole("dialog");
  expect(
    dialog.querySelector('[data-slot="dialog-description"]'),
  ).not.toBeInTheDocument();
  expect(dialog).toHaveTextContent("Application VPC");
  expect(dialog).toHaveTextContent("VPC");
  expect(dialog).toHaveTextContent("vpc-a");
  expect(
    within(dialog)
      .getAllByRole("term")
      .map((term) => term.textContent),
  ).toEqual(["类型", "资源 ID", "资源数"]);
  expect(
    within(dialog)
      .getAllByRole("definition")
      .map((definition) => definition.textContent),
  ).toEqual(["VPC", "vpc-a", "7"]);
  expect(dialog.textContent?.match(/7/g)).toHaveLength(1);
  expect(dialog).not.toHaveTextContent("资源属性");
  expect(dialog).not.toHaveTextContent("清理策略");
  expect(dialog).not.toHaveTextContent("阻断项");
});

it("labels a Region resource ID as Region ID", () => {
  render(
    <LocaleProvider>
      <ScopeDetailDialog
        open
        onOpenChange={() => {}}
        kind="region"
        entry={{
          key: "region:us-southeast-1",
          name: "美国（亚特兰大）",
          native_id: "us-southeast-1",
          resource_count: 1,
          cleanup: {
            selectable: false,
            potential_blockers: 0,
          },
        }}
      />
    </LocaleProvider>,
  );

  const dialog = screen.getByRole("dialog");
  expect(
    within(dialog)
      .getAllByRole("term")
      .map((term) => term.textContent),
  ).toEqual(["类型", "地域 ID", "资源数"]);
  expect(dialog).not.toHaveTextContent("查看范围详情");
});

it("lists stack members and opens the selected resource detail", async () => {
  const user = userEvent.setup();
  const onOpenChange = vi.fn();
  const onOpenResource = vi.fn();
  render(
    <LocaleProvider>
      <StackMembersDialog
        open
        onOpenChange={onOpenChange}
        displayName="ECS Instances"
        resources={[
          resource,
          {
            ...resource,
            key: "resource:ecs:i-456",
            asset_id: "asset-456",
            name: "worker",
            native_id: "i-456",
          },
        ]}
        onOpenResource={onOpenResource}
      />
    </LocaleProvider>,
  );

  expect(screen.getByRole("dialog")).toHaveTextContent("production-api");
  expect(screen.getByRole("dialog")).toHaveTextContent("worker");
  await user.click(screen.getByRole("button", { name: "查看详情: worker" }));
  expect(onOpenChange).toHaveBeenCalledWith(false);
  expect(onOpenResource).toHaveBeenCalledWith(
    expect.objectContaining({ asset_id: "asset-456" }),
  );
});
