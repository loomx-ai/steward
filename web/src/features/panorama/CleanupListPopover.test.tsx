import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useLocation, MemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import type { CleanupSelector } from "@/api/types";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import { readCleanupSelectionHandoff } from "../cleanup/selection";
import {
  cleanupSelectionStorageKey,
  CleanupSelectionProvider,
} from "./CleanupSelectionContext";
import type { CleanupTarget } from "./cleanupSelection";
import { CleanupListPopover } from "./CleanupListPopover";

const connectionHarness = vi.hoisted(() => ({
  setActiveConnectionID: vi.fn(),
}));

vi.mock("@/connections/ActiveConnectionProvider", () => ({
  useActiveConnection: () => ({
    activeConnectionID: "connection-a",
    setActiveConnectionID: connectionHarness.setActiveConnectionID,
  }),
  useRequiredConnection: () => ({
    id: "connection-a",
    name: "Production",
  }),
}));

function target(
  overrides: Partial<CleanupTarget> &
    Pick<CleanupTarget, "key" | "kind" | "displayName" | "selector">,
): CleanupTarget {
  return {
    connectionId: "connection-a",
    ancestryKeys: [overrides.key],
    ...overrides,
  };
}

function storedTargets(targets: readonly CleanupTarget[]) {
  sessionStorage.setItem(
    cleanupSelectionStorageKey("connection-a"),
    JSON.stringify({ version: 1, targets }),
  );
}

function renderCleanupList(
  targets: readonly CleanupTarget[] = [],
  onLocate = vi.fn(),
) {
  storedTargets(targets);
  return render(
    <LocaleProvider>
      <MemoryRouter initialEntries={["/panorama"]}>
        <CleanupSelectionProvider>
          <div className="relative h-96">
            <CleanupListPopover onLocate={onLocate} />
          </div>
          <LocationProbe />
        </CleanupSelectionProvider>
      </MemoryRouter>
    </LocaleProvider>,
  );
}

function LocationProbe() {
  const location = useLocation();
  return (
    <output data-testid="location">
      {JSON.stringify({
        pathname: location.pathname,
        state: location.state,
      })}
    </output>
  );
}

function resourceTarget(assetID: string, displayName = assetID): CleanupTarget {
  return target({
    key: `asset:${assetID}`,
    kind: "resource",
    displayName,
    selector: {
      kind: "asset",
      asset_id: assetID,
      display_name: displayName,
    },
  });
}

function batchTarget(): CleanupTarget {
  return target({
    key: "stack:database",
    kind: "resource_batch",
    displayName: "Database nodes",
    selector: [
      {
        kind: "asset",
        asset_id: "asset-db-a",
        display_name: "database-a",
      },
      {
        kind: "asset",
        asset_id: "asset-db-b",
        display_name: "database-b",
      },
    ],
    memberAssetIds: ["asset-db-a", "asset-db-b"],
    resourceCount: 2,
  });
}

beforeEach(() => {
  sessionStorage.clear();
  connectionHarness.setActiveConnectionID.mockReset();
});

it("uses an anchored neutral checklist trigger with an amber normalized target count", async () => {
  const user = userEvent.setup();
  renderCleanupList([
    target({
      key: "region:cn-hangzhou",
      kind: "region",
      displayName: "Hangzhou",
      selector: {
        kind: "scope",
        connection_id: "connection-a",
        scope_id: "scope-cn-hangzhou",
        scope_kind: "region",
        descendants: true,
      },
      resourceCount: 12,
    }),
    target({
      key: "vpc:vpc-a",
      kind: "vpc",
      displayName: "Production VPC",
      selector: {
        kind: "group",
        connection_id: "connection-a",
        group_key: "vpc:vpc-a",
      },
    }),
    resourceTarget("asset-api", "API server"),
    batchTarget(),
  ]);

  const trigger = await screen.findByRole("button", {
    name: "Resource list · 4",
  });
  expect(trigger).toHaveClass("h-11", "min-w-36", "px-3");
  expect(trigger.parentElement).toHaveClass(
    "absolute",
    "right-4",
    "bottom-4",
    "z-30",
  );
  expect(trigger.querySelector(".lucide-list-checks")).not.toBeNull();
  expect(
    trigger.querySelector(
      ".lucide-shopping-bag, .lucide-shopping-cart, .lucide-trash, .lucide-trash-2",
    ),
  ).toBeNull();
  expect(within(trigger).getByText("4")).toHaveClass(
    "bg-warning",
    "text-warning-foreground",
  );

  await user.click(trigger);

  const popover = await screen.findByRole("dialog", {
    name: "Resource list",
  });
  await waitFor(() => expect(popover).toHaveAttribute("data-side", "top"));
  expect(popover).toHaveAttribute("data-align", "end");
  expect(popover).toHaveClass("w-[min(26rem,calc(100vw-2rem))]", "p-0");
  expect(
    document.querySelector('[data-slot="popover-overlay"]'),
  ).not.toBeInTheDocument();

  expect(within(popover).getByText("Region")).toBeVisible();
  expect(within(popover).getByText("Region")).toHaveAttribute(
    "data-slot",
    "badge",
  );
  expect(within(popover).getByText("Hangzhou")).toBeVisible();
  expect(within(popover).getByText("VPC")).toBeVisible();
  expect(within(popover).getByText("Production VPC")).toBeVisible();
  expect(within(popover).getByText("Resource")).toBeVisible();
  expect(within(popover).getByText("API server")).toBeVisible();
  expect(within(popover).getByText("Resource group")).toBeVisible();
  expect(within(popover).getByText("Database nodes")).toBeVisible();
  expect(
    within(popover).queryByText("Resource list · 4"),
  ).not.toBeInTheDocument();
  expect(
    within(popover).getByRole("button", {
      name: "Locate Hangzhou on canvas",
    }),
  ).toBeVisible();
});

it("locates a target from the list and closes the popover", async () => {
  const user = userEvent.setup();
  const onLocate = vi.fn();
  const target = resourceTarget("asset-api", "API server");
  renderCleanupList([target], onLocate);

  await user.click(
    await screen.findByRole("button", { name: "Resource list · 1" }),
  );
  await user.click(
    screen.getByRole("button", { name: "Locate API server on canvas" }),
  );

  expect(onLocate).toHaveBeenCalledWith(target);
  expect(
    screen.queryByRole("dialog", { name: "Resource list" }),
  ).not.toBeInTheDocument();
});

it("shows owning topology context and current resource counts for non-batch targets", async () => {
  const user = userEvent.setup();
  renderCleanupList([
    target({
      key: "region:cn-hangzhou",
      kind: "region",
      displayName: "Hangzhou",
      selector: {
        kind: "scope",
        connection_id: "connection-a",
        scope_id: "scope-cn-hangzhou",
        scope_kind: "region",
        descendants: true,
      },
      resourceCount: 12,
    }),
    target({
      key: "vpc:vpc-a",
      kind: "vpc",
      displayName: "Production VPC",
      selector: {
        kind: "group",
        connection_id: "connection-a",
        group_key: "vpc:vpc-a",
      },
      ancestryKeys: ["region:cn-hangzhou", "vpc:vpc-a"],
      locationContext: {
        region: {
          key: "region:cn-hangzhou",
          name: "华东1（杭州）",
          native_id: "cn-hangzhou",
        },
      },
      resourceCount: 8,
    }),
    target({
      key: "asset:asset-api",
      kind: "resource",
      displayName: "API server",
      selector: {
        kind: "asset",
        asset_id: "asset-api",
        display_name: "API server",
      },
      ancestryKeys: ["region:cn-hangzhou", "vpc:vpc-a", "asset:asset-api"],
      locationContext: {
        region: {
          key: "region:cn-hangzhou",
          name: "华东1（杭州）",
          native_id: "cn-hangzhou",
        },
        vpc: {
          key: "vpc:vpc-a",
          name: "生产网络",
          native_id: "vpc-a",
        },
      },
    }),
  ]);

  await user.click(
    await screen.findByRole("button", { name: "Resource list · 3" }),
  );

  const regionRow = screen
    .getByRole("button", { name: "Remove Hangzhou" })
    .closest("li");
  const vpcRow = screen
    .getByRole("button", { name: "Remove Production VPC" })
    .closest("li");
  const resourceRow = screen
    .getByRole("button", { name: "Remove API server" })
    .closest("li");
  expect(regionRow).not.toBeNull();
  expect(vpcRow).not.toBeNull();
  expect(resourceRow).not.toBeNull();
  expect(within(regionRow!).getByText("12 resources")).toBeVisible();
  expect(
    within(vpcRow!).getByText("Region: 华东1（杭州） · 8 resources"),
  ).toBeVisible();
  expect(
    within(resourceRow!).getByText("Region: 华东1（杭州） · VPC: 生产网络"),
  ).toBeVisible();
});

it("shows the regional public group with supplied region metadata instead of its encoded key", async () => {
  const user = userEvent.setup();
  const encodedRegionKey = "region:Y24taGFuZ3pob3U";
  renderCleanupList([
    target({
      key: "region-public:Y24taGFuZ3pob3U",
      kind: "vpc",
      displayName: "Global resources",
      selector: {
        kind: "group",
        connection_id: "connection-a",
        group_key: "region-public:Y24taGFuZ3pob3U",
      },
      ancestryKeys: [encodedRegionKey, "region-public:Y24taGFuZ3pob3U"],
      locationContext: {
        region: {
          key: encodedRegionKey,
          name: "华东1（杭州）",
          native_id: "cn-hangzhou",
        },
      },
      resourceCount: 1538,
    }),
  ]);

  await user.click(
    await screen.findByRole("button", { name: "Resource list · 1" }),
  );

  expect(
    screen.getByText("Region: 华东1（杭州） · 1,538 resources"),
  ).toBeVisible();
  expect(screen.queryByText(/Y24taGFuZ3pob3U/)).not.toBeInTheDocument();
});

it("switches the batch disclosure name between expand and collapse", async () => {
  const user = userEvent.setup();
  renderCleanupList([batchTarget()]);

  await user.click(
    await screen.findByRole("button", { name: "Resource list · 1" }),
  );
  const expand = screen.getByRole("button", {
    name: "Expand Database nodes",
  });
  await user.click(expand);
  expect(
    screen.getByRole("button", { name: "Collapse Database nodes" }),
  ).toBeVisible();
  expect(screen.getByText("database-a")).toBeVisible();

  await user.click(
    screen.getByRole("button", { name: "Collapse Database nodes" }),
  );
  expect(
    screen.getByRole("button", { name: "Expand Database nodes" }),
  ).toBeVisible();
  expect(screen.queryByText("database-a")).not.toBeInTheDocument();
});

it("shows the owning region and VPC for a resource batch", async () => {
  const user = userEvent.setup();
  renderCleanupList([
    {
      ...batchTarget(),
      ancestryKeys: [
        "region:Y24taGFuZ3pob3U",
        "vpc:Y24taGFuZ3pob3U:dnBjLWJwMWE",
        "stack:database",
      ],
      locationContext: {
        region: {
          key: "region:Y24taGFuZ3pob3U",
          name: "华东1（杭州）",
          native_id: "cn-hangzhou",
        },
        vpc: {
          key: "vpc:Y24taGFuZ3pob3U:dnBjLWJwMWE",
          name: "生产网络",
          native_id: "vpc-a",
        },
      },
    },
  ]);

  await user.click(
    await screen.findByRole("button", { name: "Resource list · 1" }),
  );

  const batchRow = screen
    .getByRole("button", { name: "Remove Database nodes" })
    .closest("li");
  expect(batchRow).not.toBeNull();
  expect(
    within(batchRow!).getByText("Region: 华东1（杭州） · VPC: 生产网络"),
  ).toBeVisible();
  expect(
    within(batchRow!).queryByText(/Y24taGFuZ3pob3U/),
  ).not.toBeInTheDocument();
});

it("preserves a batch topology total while removing members", async () => {
  const user = userEvent.setup();
  renderCleanupList([batchTarget()]);

  await user.click(
    await screen.findByRole("button", { name: "Resource list · 1" }),
  );
  expect(screen.getByText("2/2")).toBeVisible();
  await user.click(
    screen.getByRole("button", { name: "Expand Database nodes" }),
  );
  expect(screen.getByText("database-a")).toBeVisible();
  expect(screen.getByText("database-b")).toBeVisible();

  await user.click(screen.getByRole("button", { name: "Remove database-a" }));
  expect(screen.queryByText("database-a")).not.toBeInTheDocument();
  expect(screen.getByText("1/2")).toBeVisible();

  await user.click(screen.getByRole("button", { name: "Remove database-b" }));
  expect(
    screen.queryByRole("button", { name: "Resource list · 0" }),
  ).not.toBeInTheDocument();
  expect(
    screen.queryByRole("dialog", { name: "Resource list" }),
  ).not.toBeInTheDocument();
});

it("removes a target and confirms before clearing a non-empty list", async () => {
  const user = userEvent.setup();
  renderCleanupList([
    resourceTarget("asset-api", "API server"),
    resourceTarget("asset-worker", "Worker"),
  ]);

  await user.click(
    await screen.findByRole("button", { name: "Resource list · 2" }),
  );
  await user.click(screen.getByRole("button", { name: "Remove API server" }));
  expect(screen.queryByText("API server")).not.toBeInTheDocument();
  expect(
    screen.getByRole("button", { name: "Resource list · 1" }),
  ).toBeVisible();

  await user.click(screen.getByRole("button", { name: "Clear resource list" }));
  const confirmation = await screen.findByRole("alertdialog");
  expect(within(confirmation).getByText("Clear resource list?")).toBeVisible();
  expect(
    JSON.parse(
      sessionStorage.getItem(cleanupSelectionStorageKey("connection-a")) ??
        "null",
    ),
  ).toMatchObject({
    targets: [expect.objectContaining({ displayName: "Worker" })],
  });

  await user.click(
    within(confirmation).getByRole("button", {
      name: "Clear resource list",
    }),
  );
  expect(
    screen.queryByRole("button", { name: "Resource list · 0" }),
  ).not.toBeInTheDocument();
});

it("hands expanded deduplicated selectors to the task builder before navigating", async () => {
  const user = userEvent.setup();
  const duplicateSelector: CleanupSelector = {
    kind: "asset",
    asset_id: "asset-api",
    display_name: "API server duplicate",
  };
  renderCleanupList([
    resourceTarget("asset-api", "API server"),
    target({
      key: "stack:duplicate",
      kind: "resource_batch",
      displayName: "Duplicate projection",
      selector: [
        duplicateSelector,
        {
          kind: "asset",
          asset_id: "asset-worker",
          display_name: "Worker",
        },
      ],
      memberAssetIds: ["asset-api", "asset-worker"],
      resourceCount: 2,
    }),
  ]);

  await user.click(
    await screen.findByRole("button", { name: "Resource list · 2" }),
  );
  await user.click(screen.getByRole("button", { name: "Create cleanup task" }));

  expect(readCleanupSelectionHandoff("connection-a")).toEqual([
    {
      kind: "asset",
      asset_id: "asset-api",
      display_name: "API server",
    },
    {
      kind: "asset",
      asset_id: "asset-worker",
      display_name: "Worker",
    },
  ]);
  expect(screen.getByTestId("location")).toHaveTextContent(
    JSON.stringify({
      pathname: "/cleanup/new",
      state: { fromPanoramaCleanup: true },
    }),
  );
});

it("still navigates with live selection when handoff storage fails", async () => {
  const user = userEvent.setup();
  renderCleanupList([resourceTarget("asset-api", "API server")]);
  const originalSetItem = Storage.prototype.setItem;
  const setItem = vi
    .spyOn(Storage.prototype, "setItem")
    .mockImplementation(function setItem(
      this: Storage,
      key: string,
      value: string,
    ) {
      if (key.startsWith("steward:cleanup-cln-handoff:")) {
        throw new DOMException("quota exceeded", "QuotaExceededError");
      }
      return originalSetItem.call(this, key, value);
    });

  await user.click(
    await screen.findByRole("button", { name: "Resource list · 1" }),
  );
  await user.click(screen.getByRole("button", { name: "Create cleanup task" }));

  expect(setItem).toHaveBeenCalled();
  expect(screen.getByTestId("location")).toHaveTextContent(
    JSON.stringify({
      pathname: "/cleanup/new",
      state: { fromPanoramaCleanup: true },
    }),
  );
  expect(
    JSON.parse(
      sessionStorage.getItem(cleanupSelectionStorageKey("connection-a")) ??
        "null",
    ),
  ).toMatchObject({
    targets: [expect.objectContaining({ key: "asset:asset-api" })],
  });
});

it("hides the resource list while it is empty", () => {
  renderCleanupList();

  expect(
    screen.queryByRole("button", { name: /Resource list/ }),
  ).not.toBeInTheDocument();
  expect(
    screen.queryByRole("dialog", { name: "Resource list" }),
  ).not.toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: "Create cleanup task" }),
  ).not.toBeInTheDocument();
});
