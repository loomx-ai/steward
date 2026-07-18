import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  findAsset,
  findAssetsByNativeIDs,
  listProviderCatalog,
} from "@/api/client";
import type { Asset, ProviderCatalogBundle, ResourceKind } from "@/api/types";
import { TooltipProvider } from "@/components/ui/tooltip";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import {
  NetworkResourceDetailDialog,
  type NetworkResourceDetail,
} from "./NetworkResourceDetailDialog";

vi.mock("@/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/client")>()),
  findAsset: vi.fn(),
  findAssetsByNativeIDs: vi.fn(),
  listProviderCatalog: vi.fn(),
}));

const detail: NetworkResourceDetail = {
  kind: "vswitch",
  assetId: "asset-vsw-a",
  name: "vsw_streaming_Lakehouse",
  nativeId: "vsw-2zems2l3rigl0k81ub83c",
  region: "华北 2（北京）",
  zone: "cn-beijing-g",
  resourceCount: 1,
  icon: "/icons/alicloud/acs-vpc-vswitch.svg",
};

const asset: Asset = {
  id: "asset-vsw-a",
  identity: {
    provider: "alicloud",
    partition: "aliyun",
    connection_id: "connection-a",
    native_type: "ACS::VPC::VSwitch",
    native_id: detail.nativeId,
  },
  scope_id: "cn-beijing",
  resource_kind_id: "alicloud:ACS::VPC::VSwitch",
  name: detail.name,
  location: "cn-beijing-g",
  capabilities: ["indexed"],
  normalized: {
    cidrBlock: "10.0.0.0/24",
    vpcId: "vpc-2zeutleg8gepzfs01xlyrk",
    zoneId: detail.zone,
  },
  first_seen_at: "2026-07-31T00:00:00Z",
  last_seen_at: "2026-07-31T01:00:00Z",
};

const kind: ResourceKind = {
  id: asset.resource_kind_id,
  provider: "alicloud",
  native_type: asset.identity.native_type,
  capabilities: ["indexed"],
  display_name: "vSwitch",
  display_names: {
    "en-US": "vSwitch",
    "zh-CN": "交换机",
  },
  field_display_names: {
    cidrBlock: {
      "en-US": "CIDR block",
      "zh-CN": "网段",
    },
  },
  bundle_revision: "runtime-1",
};

const catalog: ProviderCatalogBundle[] = [
  {
    provider: "alicloud",
    revision: "compiled-1",
    hash: "hash-1",
    kinds: [kind],
    kinds_revision: "runtime-1",
    specs: [],
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
});

afterEach(() => vi.unstubAllGlobals());

function renderDialog(value: NetworkResourceDetail = detail) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <LocaleProvider>
          <TooltipProvider>
            <NetworkResourceDetailDialog
              open
              onOpenChange={() => {}}
              connectionId="connection-a"
              detail={value}
            />
          </TooltipProvider>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

it("shows topology facts and resource-owned properties in one dialog", async () => {
  renderDialog();

  const dialog = await screen.findByRole("dialog");
  expect(
    within(dialog).getByRole("heading", {
      name: "vsw_streaming_Lakehouse",
    }),
  ).toBeVisible();
  expect(dialog.querySelector("[data-resource-icon-mask]")).toHaveStyle({
    maskImage: "url(/icons/alicloud/acs-vpc-vswitch.svg)",
  });
  expect(
    dialog.querySelector('[data-slot="dialog-description"]'),
  ).not.toBeInTheDocument();
  await within(dialog).findByText("交换机");
  expect(dialog).toHaveTextContent("华北 2（北京）");
  expect(dialog).toHaveTextContent("vsw-2zems2l3rigl0k81ub83c");
  const summary = dialog.querySelector("dl");
  expect(summary).not.toBeNull();
  expect(
    within(summary!)
      .getAllByRole("term")
      .map((term) => term.textContent),
  ).toEqual(["资源类型", "地域"]);

  const properties = await within(dialog).findByRole("region", {
    name: "资源属性",
  });
  expect(
    within(properties)
      .getAllByRole("term")
      .map((term) => term.textContent),
  ).toEqual(["ID", "所属专有网络", "可用区", "网段"]);
  expect(properties).toHaveTextContent("10.0.0.0/24");
  expect(properties).toHaveTextContent("vpc-2zeutleg8gepzfs01xlyrk");
  expect(
    within(properties).getByRole("button", { name: "复制网段" }),
  ).toBeVisible();
  expect(screen.getByRole("link", { name: "打开资源详情" })).toHaveAttribute(
    "href",
    "/assets/asset-vsw-a",
  );
});

it("keeps topology facts visible when the asset cannot be loaded and retries", async () => {
  vi.mocked(findAsset)
    .mockReset()
    .mockRejectedValueOnce(new Error("details unavailable"))
    .mockResolvedValueOnce(asset);
  const user = userEvent.setup();
  renderDialog();

  const dialog = await screen.findByRole("dialog");
  expect(dialog).toHaveTextContent("华北 2（北京）");
  expect(await within(dialog).findByText("details unavailable")).toBeVisible();

  await user.click(within(dialog).getByRole("button", { name: "重试详情" }));
  await waitFor(() => expect(findAsset).toHaveBeenCalledTimes(2));
  expect(
    await within(dialog).findByRole("region", { name: "资源属性" }),
  ).toBeVisible();
});

it("shows basic facts without requesting an asset when asset ID is unavailable", async () => {
  renderDialog({ ...detail, assetId: undefined });

  const dialog = await screen.findByRole("dialog");
  expect(dialog).toHaveTextContent("vsw-2zems2l3rigl0k81ub83c");
  expect(dialog).toHaveTextContent("资源属性不可用");
  expect(findAsset).not.toHaveBeenCalled();
  expect(listProviderCatalog).not.toHaveBeenCalled();
  expect(
    within(dialog).queryByRole("link", { name: "打开资源详情" }),
  ).not.toBeInTheDocument();
});
