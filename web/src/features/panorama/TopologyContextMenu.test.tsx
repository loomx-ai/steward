import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { setAssetDirty } from "@/api/client";
import type { Asset } from "@/api/types";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import { TopologyContextMenu } from "./TopologyContextMenu";

vi.mock("@/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/client")>()),
  setAssetDirty: vi.fn(),
}));

beforeEach(() => {
  localStorage.setItem("steward.locale", "en-US");
  vi.mocked(setAssetDirty)
    .mockReset()
    .mockImplementation(
      async (connectionID, id, dirty) =>
        ({
          id,
          dirty,
          identity: {
            connection_id: connectionID,
            provider: "alicloud",
            partition: "aliyun",
            native_type: "ACS::ECS::NetworkInterface",
            native_id: "eni-a",
          },
          scope_id: "region-a",
          resource_kind_id: "network-interface",
          capabilities: [],
          first_seen_at: "2026-08-05T00:00:00Z",
          last_seen_at: "2026-08-05T00:00:00Z",
        }) satisfies Asset,
    );
});

function renderMenu(
  props: Partial<React.ComponentProps<typeof TopologyContextMenu>> = {},
) {
  const callbacks = {
    onViewDetails: vi.fn(),
    onRescan: vi.fn(),
    onAddToCleanup: vi.fn(),
    onRemoveFromCleanup: vi.fn(),
  };
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={queryClient}>
      <LocaleProvider>
        <TopologyContextMenu
          kind="resource"
          pendingCleanup={false}
          {...callbacks}
          {...props}
        >
          <button type="button">production-api</button>
        </TopologyContextMenu>
      </LocaleProvider>
    </QueryClientProvider>,
  );
  return {
    callbacks,
    trigger: screen.getByRole("button", { name: "production-api" }),
  };
}

it("opens the same Radix menu by mouse right-click and Shift+F10", async () => {
  const user = userEvent.setup();
  const { trigger } = renderMenu();

  trigger.focus();
  fireEvent.contextMenu(trigger, { clientX: 24, clientY: 32 });
  expect(await screen.findByRole("menu")).toHaveTextContent("View details");
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("menu")).not.toBeInTheDocument();

  trigger.focus();
  fireEvent.keyDown(trigger, { key: "F10", shiftKey: true });
  expect(await screen.findByRole("menu")).toHaveTextContent("View details");
});

it("runs resource detail and add callbacks, then restores trigger focus", async () => {
  const user = userEvent.setup();
  const { callbacks, trigger } = renderMenu();

  fireEvent.contextMenu(trigger);
  await user.click(
    await screen.findByRole("menuitem", { name: "View details" }),
  );
  expect(callbacks.onViewDetails).toHaveBeenCalledOnce();
  await waitFor(() => expect(trigger).toHaveFocus());

  fireEvent.contextMenu(trigger);
  await user.click(
    await screen.findByRole("menuitem", { name: "Add to resource list" }),
  );
  expect(callbacks.onAddToCleanup).toHaveBeenCalledOnce();
});

it("offers removal for a pending resource", async () => {
  const user = userEvent.setup();
  const { callbacks, trigger } = renderMenu({ pendingCleanup: true });

  fireEvent.contextMenu(trigger);
  await user.click(
    await screen.findByRole("menuitem", { name: "Remove from resource list" }),
  );

  expect(callbacks.onRemoveFromCleanup).toHaveBeenCalledOnce();
  expect(callbacks.onAddToCleanup).not.toHaveBeenCalled();
});

it("marks a dirty resource from a text-only context-menu item", async () => {
  const user = userEvent.setup();
  const { trigger } = renderMenu({
    dirtyAsset: {
      connectionId: "connection-a",
      id: "asset-a",
      dirty: false,
    },
  });

  fireEvent.contextMenu(trigger);
  const menuItem = await screen.findByRole("menuitem", {
    name: "Mark as dirty resource",
  });
  expect(menuItem.querySelector("img, svg")).toBeNull();
  await user.click(menuItem);
  await waitFor(() =>
    expect(setAssetDirty).toHaveBeenCalledWith("connection-a", "asset-a", true),
  );
});

it("removes a dirty resource mark from the context menu", async () => {
  const user = userEvent.setup();
  const { trigger } = renderMenu({
    dirtyAsset: {
      connectionId: "connection-a",
      id: "asset-a",
      dirty: true,
    },
  });

  fireEvent.contextMenu(trigger);
  const menuItem = await screen.findByRole("menuitem", {
    name: "Remove dirty resource mark",
  });
  expect(menuItem.querySelector("img, svg")).toBeNull();
  await user.click(menuItem);
  await waitFor(() =>
    expect(setAssetDirty).toHaveBeenCalledWith(
      "connection-a",
      "asset-a",
      false,
    ),
  );
});

it("identifies inherited cleanup without offering a child removal", async () => {
  const { callbacks, trigger } = renderMenu({
    pendingCleanup: true,
    inheritedCleanup: true,
  });

  fireEvent.contextMenu(trigger);
  expect(
    await screen.findByRole("menuitem", {
      name: "Included by broader cleanup selection",
    }),
  ).toHaveAttribute("data-disabled");
  expect(
    screen.queryByRole("menuitem", { name: "Remove from resource list" }),
  ).not.toBeInTheDocument();
  expect(callbacks.onRemoveFromCleanup).not.toHaveBeenCalled();
});

it("uses details and cleanup actions for a region", async () => {
  const user = userEvent.setup();
  const { callbacks, trigger } = renderMenu({ kind: "region" });

  fireEvent.contextMenu(trigger);
  await user.click(
    await screen.findByRole("menuitem", { name: "View details" }),
  );
  expect(callbacks.onViewDetails).toHaveBeenCalledOnce();

  fireEvent.contextMenu(trigger);
  await user.click(await screen.findByRole("menuitem", { name: "Rescan" }));
  expect(callbacks.onRescan).toHaveBeenCalledOnce();

  fireEvent.contextMenu(trigger);
  await user.click(
    await screen.findByRole("menuitem", { name: "Add to resource list" }),
  );
  expect(callbacks.onAddToCleanup).toHaveBeenCalledOnce();
});

it("uses resource details and cleanup actions for a VPC", async () => {
  const user = userEvent.setup();
  const { callbacks, trigger } = renderMenu({ kind: "vpc" });

  fireEvent.contextMenu(trigger);
  await user.click(
    await screen.findByRole("menuitem", { name: "View details" }),
  );
  expect(callbacks.onViewDetails).toHaveBeenCalledOnce();

  fireEvent.contextMenu(trigger);
  await user.click(
    await screen.findByRole("menuitem", { name: "Add to resource list" }),
  );
  expect(callbacks.onAddToCleanup).toHaveBeenCalledOnce();
});

it("links a resource to its cloud product console in a new tab", async () => {
  const { trigger } = renderMenu({
    consoleURL:
      "https://vpc.console.aliyun.com/vpc/cn-hangzhou/vpcs/vpc-production",
  });

  fireEvent.contextMenu(trigger);

  expect(
    await screen.findByRole("menuitem", { name: "Cloud console" }),
  ).toMatchObject({
    href: "https://vpc.console.aliyun.com/vpc/cn-hangzhou/vpcs/vpc-production",
    target: "_blank",
    rel: "noopener noreferrer",
  });
});

it("supports a details-only menu without cleanup actions or a separator", async () => {
  const { trigger } = renderMenu({
    kind: "vpc",
    onAddToCleanup: undefined,
    onRemoveFromCleanup: undefined,
  });

  fireEvent.contextMenu(trigger);

  expect(
    await screen.findByRole("menuitem", { name: "View details" }),
  ).toBeVisible();
  expect(
    screen.queryByRole("menuitem", { name: "Add to resource list" }),
  ).not.toBeInTheDocument();
  expect(screen.queryByRole("separator")).not.toBeInTheDocument();
});

it("offers removal for a pending scope", async () => {
  const user = userEvent.setup();
  const { callbacks, trigger } = renderMenu({
    kind: "vpc",
    pendingCleanup: true,
  });

  fireEvent.contextMenu(trigger);
  await user.click(
    await screen.findByRole("menuitem", {
      name: "Remove from resource list",
    }),
  );

  expect(callbacks.onRemoveFromCleanup).toHaveBeenCalledOnce();
});

it("uses included-resource and all-member actions for a stack", async () => {
  const user = userEvent.setup();
  const { callbacks, trigger } = renderMenu({ kind: "stack" });

  fireEvent.contextMenu(trigger);
  await user.click(
    await screen.findByRole("menuitem", {
      name: "View included resources",
    }),
  );
  expect(callbacks.onViewDetails).toHaveBeenCalledOnce();

  fireEvent.contextMenu(trigger);
  await user.click(
    await screen.findByRole("menuitem", {
      name: "Add all to resource list",
    }),
  );
  expect(callbacks.onAddToCleanup).toHaveBeenCalledOnce();
});

it("uses remove-all for a pending stack", async () => {
  const user = userEvent.setup();
  const { callbacks, trigger } = renderMenu({
    kind: "stack",
    pendingCleanup: true,
  });

  fireEvent.contextMenu(trigger);
  await user.click(
    await screen.findByRole("menuitem", {
      name: "Remove all from resource list",
    }),
  );

  expect(callbacks.onRemoveFromCleanup).toHaveBeenCalledOnce();
});
