import type { ComponentProps } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import type { CleanupSelector } from "@/api/types";
import { PageLayout } from "@/components/patterns/PageLayout";
import type { CleanupTarget } from "@/features/panorama/cleanupSelection";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import { CleanupTaskBuilder } from "./CleanupTaskBuilder";
import {
  encodeSelector,
  executionConfirmationSatisfied,
  parseRequestOptions,
  readCleanupSelectionHandoff,
  writeCleanupSelectionHandoff,
} from "./selection";

const cleanupHarness = vi.hoisted(() => ({
  createCleanupTask: vi.fn(),
  findAssets: vi.fn(),
  listProviderCatalog: vi.fn(),
  connection: {
    id: "connection-a",
    name: "Production",
    provider: "alicloud",
  },
}));
const cleanupSelectionHarness = vi.hoisted(() => ({
  targets: [] as CleanupTarget[],
  consumeTargets: vi.fn(() => true),
}));
const routerHarness = vi.hoisted(() => ({
  navigate: vi.fn(),
}));

vi.mock("@/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/client")>()),
  createCleanupTask: cleanupHarness.createCleanupTask,
  findAssets: cleanupHarness.findAssets,
  listProviderCatalog: cleanupHarness.listProviderCatalog,
}));

vi.mock("@/features/panorama/CleanupSelectionContext", () => ({
  useCleanupSelection: () => ({
    ...cleanupSelectionHarness,
    hydratedConnectionID: cleanupHarness.connection.id,
  }),
}));

vi.mock("react-router-dom", async (importOriginal) => ({
  ...(await importOriginal<typeof import("react-router-dom")>()),
  useNavigate: () => routerHarness.navigate,
}));

vi.mock("@/connections/ActiveConnectionProvider", () => ({
  useRequiredConnection: () => cleanupHarness.connection,
}));

beforeEach(() => {
  localStorage.setItem("steward.locale", "en-US");
  sessionStorage.clear();
  cleanupHarness.createCleanupTask.mockReset();
  cleanupHarness.findAssets.mockReset();
  cleanupHarness.listProviderCatalog.mockReset();
  cleanupHarness.findAssets.mockResolvedValue([]);
  cleanupHarness.listProviderCatalog.mockResolvedValue([
    {
      provider: "alicloud",
      specs: [],
      revision: "catalog-1",
      hash: "catalog-hash",
      kinds_revision: "catalog-1",
      kinds: [
        {
          id: "alicloud:ACS::ECS::Instance",
          provider: "alicloud",
          native_type: "ACS::ECS::Instance",
          capabilities: [],
          display_name: "ECS instance",
          bundle_revision: "catalog-1",
        },
      ],
    },
  ]);
  cleanupHarness.createCleanupTask.mockResolvedValue({ task: { id: "cln-a" } });
  cleanupHarness.connection = {
    id: "connection-a",
    name: "Production",
    provider: "alicloud",
  };
  cleanupSelectionHarness.targets = [];
  cleanupSelectionHarness.consumeTargets.mockReset();
  cleanupSelectionHarness.consumeTargets.mockReturnValue(true);
  routerHarness.navigate.mockReset();
});

const scopeSelector: CleanupSelector = {
  kind: "scope",
  connection_id: "connection-a",
  scope_id: "region:cn-hangzhou",
  scope_kind: "region",
  display_name: "Hangzhou",
};
const scopeSelectorB: CleanupSelector = {
  kind: "scope",
  connection_id: "connection-b",
  scope_id: "region:cn-shanghai",
  scope_kind: "region",
  display_name: "Shanghai",
};
const assetSelector: CleanupSelector = {
  kind: "asset",
  asset_id: "asset-a",
  display_name: "Asset A",
};
const groupSelector: CleanupSelector = {
  kind: "group",
  connection_id: "connection-a",
  scope_id: "region:cn-hangzhou",
  group_key: "group-vpc-a",
  display_name: "Production VPC",
};
const cleanupTarget: CleanupTarget = {
  key: "group:group-vpc-a",
  kind: "vpc",
  connectionId: "connection-a",
  displayName: "Production VPC",
  selector: groupSelector,
  ancestryKeys: ["region:cn-hangzhou", "group:group-vpc-a"],
};
type MemoryRouterEntry = NonNullable<
  ComponentProps<typeof MemoryRouter>["initialEntries"]
>[number];

function renderCleanupTaskBuilder(initialEntry: MemoryRouterEntry) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const tree = () => (
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[initialEntry]}>
        <LocaleProvider>
          <CleanupTaskBuilder
            onOpenChange={(open) => {
              if (!open) routerHarness.navigate("/cleanup");
            }}
          />
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>
  );
  const rendered = render(tree());
  return {
    ...rendered,
    rerenderCleanupTaskBuilder: () => rendered.rerender(tree()),
  };
}

async function submitTask() {
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Confirm" }));
}

it("lets a task builder reading layout override the default minimum height", () => {
  render(
    <PageLayout
      mode="reading"
      className="pb-10"
      data-testid="builder-layout"
    />,
  );
  const layout = screen.getByTestId("builder-layout");
  expect(layout).toHaveClass("pb-10");
});

it("uses a flat target list with an all-types filter and no advanced options", () => {
  renderCleanupTaskBuilder({
    pathname: "/cleanup/new",
    search: `?${new URLSearchParams({
      selector: encodeSelector(scopeSelector),
    }).toString()}`,
  });

  expect(
    screen.queryByRole("button", { name: /Advanced options/ }),
  ).not.toBeInTheDocument();
  expect(
    screen.queryByRole("textbox", { name: "Request options" }),
  ).not.toBeInTheDocument();
  expect(
    screen.getByRole("combobox", { name: "Resource kind" }),
  ).toHaveAttribute("placeholder", "All resource types");
  expect(
    screen.getByRole("combobox", { name: "Resource kind" }),
  ).toHaveAttribute("aria-expanded", "false");
  const targetRow = screen
    .getByRole("button", { name: "Remove Hangzhou" })
    .closest("li");
  expect(targetRow).not.toBeNull();
  expect(targetRow).not.toHaveClass("rounded-xl", "border");
  expect(targetRow?.parentElement).toHaveClass("divide-y");
  expect(screen.getByRole("button", { name: "Confirm" })).toBeEnabled();
});

it("keeps the resource type menu scrollable inside the task dialog", async () => {
  const user = userEvent.setup();
  renderCleanupTaskBuilder({ pathname: "/cleanup/new" });

  await user.click(screen.getByRole("combobox", { name: "Resource kind" }));

  const listbox = screen.getByRole("listbox", { name: "Resource kind" });
  expect(
    screen.getByRole("dialog", { name: "New cleanup task" }),
  ).toContainElement(listbox);
  expect(listbox).toHaveStyle({ overflowY: "auto" });
});

it("requires exact typed names and explicit acknowledgement", () => {
  expect(
    executionConfirmationSatisfied({
      requiredNames: ["production-account"],
      typedNames: { "production-account": "production-account" },
      acknowledgementRequired: true,
      acknowledged: false,
    }),
  ).toBe(false);
  expect(
    executionConfirmationSatisfied({
      requiredNames: ["production-account"],
      typedNames: { "production-account": "production-account" },
      acknowledgementRequired: true,
      acknowledged: true,
    }),
  ).toBe(true);
  expect(
    executionConfirmationSatisfied({
      requiredNames: ["production-account"],
      typedNames: { "production-account": "Production-account" },
      acknowledgementRequired: false,
      acknowledged: false,
    }),
  ).toBe(false);
});

it("accepts only object-shaped provider request options", () => {
  expect(parseRequestOptions('{"delete":{"force":true}}')).toEqual({
    delete: { force: true },
  });
  expect(() => parseRequestOptions("[]")).toThrow();
});

it("submits the VPC group selector with its connection through createCleanupTask", async () => {
  const selector: CleanupSelector = {
    kind: "group",
    connection_id: "connection-a",
    group_key: "group-vpc-a",
    display_name: "Production VPC",
  };
  const params = new URLSearchParams({ selector: encodeSelector(selector) });
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const user = userEvent.setup();
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[`/cleanup/new?${params.toString()}`]}>
        <LocaleProvider>
          <CleanupTaskBuilder />
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  expect(screen.getByText("Production VPC")).toBeVisible();
  await user.click(screen.getByRole("button", { name: "Confirm" }));

  expect(cleanupHarness.createCleanupTask).toHaveBeenCalledWith(
    "connection-a",
    {
      selectors: [selector],
      request_options: {},
    },
  );
});

it("pre-populates every selector row from a valid cleanup handoff", () => {
  writeCleanupSelectionHandoff("connection-a", [
    scopeSelector,
    assetSelector,
    groupSelector,
  ]);

  renderCleanupTaskBuilder({
    pathname: "/cleanup/new",
    state: { fromPanoramaCleanup: true },
  });

  expect(screen.getByText("Hangzhou")).toBeVisible();
  expect(screen.getByText("Asset A")).toBeVisible();
  expect(screen.getByText("Production VPC")).toBeVisible();
});

it("pre-populates a rebuilt task with dependent resources from task review", () => {
  writeCleanupSelectionHandoff("connection-a", [
    scopeSelector,
    { kind: "asset", asset_id: "external-instance" },
  ]);

  renderCleanupTaskBuilder({
    pathname: "/cleanup/new",
    state: { fromCleanupReview: true },
  });

  expect(screen.getByText("Hangzhou")).toBeVisible();
  expect(screen.getAllByText("external-instance")).toHaveLength(2);
  expect(cleanupSelectionHarness.consumeTargets).not.toHaveBeenCalled();
});

it("merges cleanup handoff selectors with legacy URL inputs without duplicates", async () => {
  writeCleanupSelectionHandoff("connection-a", [
    scopeSelector,
    assetSelector,
    groupSelector,
  ]);
  const params = new URLSearchParams({
    selector: encodeSelector(scopeSelector),
    assets: "asset-a,asset-b",
  });

  renderCleanupTaskBuilder({
    pathname: "/cleanup/new",
    search: `?${params.toString()}`,
    state: { fromPanoramaCleanup: true },
  });
  await submitTask();

  await waitFor(() =>
    expect(cleanupHarness.createCleanupTask).toHaveBeenCalledWith(
      "connection-a",
      {
        selectors: [
          scopeSelector,
          { kind: "asset", asset_id: "asset-a" },
          groupSelector,
          { kind: "asset", asset_id: "asset-b" },
        ],
        request_options: {},
      },
    ),
  );
});

it("preserves a single-page draft for the same connection and resets it for a different connection", async () => {
  writeCleanupSelectionHandoff("connection-a", [scopeSelector]);
  writeCleanupSelectionHandoff("connection-b", [scopeSelectorB]);
  const user = userEvent.setup();
  const view = renderCleanupTaskBuilder({
    pathname: "/cleanup/new",
    state: { fromPanoramaCleanup: true },
  });

  const assetInput = screen.getByRole("textbox", {
    name: "Add individual resource",
  });
  await user.type(assetInput, "draft-from-a");
  view.rerenderCleanupTaskBuilder();
  expect(
    screen.getByRole("textbox", { name: "Add individual resource" }),
  ).toHaveValue("draft-from-a");

  const kindFilter = screen.getByRole("combobox", {
    name: "Resource kind",
  });
  await user.click(kindFilter);
  await user.click(
    await screen.findByRole("option", { name: "ECS instance · ECS" }),
  );
  await user.keyboard("{Escape}");
  view.rerenderCleanupTaskBuilder();
  expect(screen.getByText("ECS instance")).toBeVisible();

  cleanupHarness.connection = {
    id: "connection-b",
    name: "Staging",
    provider: "alicloud",
  };
  view.rerenderCleanupTaskBuilder();

  expect(
    await screen.findByRole("textbox", { name: "Add individual resource" }),
  ).toHaveValue("");
  expect(screen.getByText("Shanghai")).toBeVisible();
  expect(screen.queryByText("Hangzhou")).not.toBeInTheDocument();
  expect(
    screen.getByRole("combobox", { name: "Resource kind" }),
  ).toHaveAttribute("placeholder", "All resource types");
});

it("submits only the new connection handoff after the connection changes", async () => {
  writeCleanupSelectionHandoff("connection-a", [scopeSelector]);
  writeCleanupSelectionHandoff("connection-b", [scopeSelectorB]);
  const user = userEvent.setup();
  const view = renderCleanupTaskBuilder({
    pathname: "/cleanup/new",
    state: { fromPanoramaCleanup: true },
  });

  expect(screen.getByRole("button", { name: "Confirm" })).toBeVisible();

  cleanupHarness.connection = {
    id: "connection-b",
    name: "Staging",
    provider: "alicloud",
  };
  view.rerenderCleanupTaskBuilder();

  expect(
    await screen.findByRole("textbox", { name: "Add individual resource" }),
  ).toHaveValue("");
  expect(screen.getByText("Shanghai")).toBeVisible();
  expect(screen.queryByText("Hangzhou")).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Confirm" }));

  await waitFor(() =>
    expect(cleanupHarness.createCleanupTask).toHaveBeenCalledWith(
      "connection-b",
      {
        selectors: [scopeSelectorB],
        request_options: {},
      },
    ),
  );
});

it("keeps handoff and in-memory cleanup targets when task creation fails", async () => {
  writeCleanupSelectionHandoff("connection-a", [scopeSelector]);
  cleanupSelectionHarness.targets = [cleanupTarget];
  cleanupHarness.createCleanupTask.mockRejectedValue(
    new Error("create failed"),
  );
  const params = new URLSearchParams({
    selector: encodeSelector(assetSelector),
  });

  renderCleanupTaskBuilder({
    pathname: "/cleanup/new",
    search: `?${params.toString()}`,
    state: { fromPanoramaCleanup: true },
  });
  await submitTask();
  await screen.findByText("create failed");

  expect(readCleanupSelectionHandoff("connection-a")).toEqual([scopeSelector]);
  expect(cleanupSelectionHarness.targets).toEqual([cleanupTarget]);
  expect(cleanupSelectionHarness.consumeTargets).not.toHaveBeenCalled();
  expect(routerHarness.navigate).not.toHaveBeenCalled();
});

it("keeps handoff and in-memory cleanup targets on cancel navigation and unmount", async () => {
  writeCleanupSelectionHandoff("connection-a", [scopeSelector]);
  cleanupSelectionHarness.targets = [cleanupTarget];
  const params = new URLSearchParams({
    selector: encodeSelector(assetSelector),
  });
  const user = userEvent.setup();

  const view = renderCleanupTaskBuilder({
    pathname: "/cleanup/new",
    search: `?${params.toString()}`,
    state: { fromPanoramaCleanup: true },
  });
  await user.click(screen.getByRole("button", { name: "Cancel" }));
  expect(routerHarness.navigate).toHaveBeenCalledWith("/cleanup");

  expect(readCleanupSelectionHandoff("connection-a")).toEqual([scopeSelector]);
  expect(cleanupSelectionHarness.targets).toEqual([cleanupTarget]);
  expect(cleanupSelectionHarness.consumeTargets).not.toHaveBeenCalled();

  view.unmount();

  expect(readCleanupSelectionHandoff("connection-a")).toEqual([scopeSelector]);
  expect(cleanupSelectionHarness.targets).toEqual([cleanupTarget]);
  expect(cleanupSelectionHarness.consumeTargets).not.toHaveBeenCalled();
});

it("consumes the submitted cleanup snapshot before navigating after successful creation", async () => {
  writeCleanupSelectionHandoff("connection-a", [scopeSelector]);
  cleanupSelectionHarness.targets = [cleanupTarget];
  const params = new URLSearchParams({
    selector: encodeSelector(assetSelector),
  });

  renderCleanupTaskBuilder({
    pathname: "/cleanup/new",
    search: `?${params.toString()}`,
    state: { fromPanoramaCleanup: true },
  });
  await submitTask();

  await waitFor(() =>
    expect(routerHarness.navigate).toHaveBeenCalledWith("/cleanup/cln-a"),
  );
  expect(readCleanupSelectionHandoff("connection-a")).toEqual([]);
  expect(cleanupSelectionHarness.consumeTargets).toHaveBeenCalledWith(
    "connection-a",
    cleanupSelectionHarness.targets,
  );
  expect(
    cleanupSelectionHarness.consumeTargets.mock.invocationCallOrder[0],
  ).toBeLessThan(routerHarness.navigate.mock.invocationCallOrder[0] ?? 0);
  expect(cleanupHarness.createCleanupTask).toHaveBeenCalledWith(
    "connection-a",
    {
      selectors: [scopeSelector, groupSelector, assetSelector],
      request_options: {},
    },
  );
});
