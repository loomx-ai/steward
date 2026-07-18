import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { listAssets, listConnectionRegions, listScopes } from "@/api/client";
import type { ResourceKind } from "@/api/types";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import { PanoramaSearch } from "./PanoramaSearch";

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return {
    ...actual,
    listAssets: vi.fn(),
    listConnectionRegions: vi.fn(),
    listScopes: vi.fn(),
  };
});

const kinds = new Map<string, ResourceKind>([
  [
    "ecs",
    {
      id: "ecs",
      provider: "alicloud",
      native_type: "ACS::ECS::Instance",
      class: "compute.instance",
      capabilities: [],
      display_name: "ECS Instance",
      bundle_revision: "revision-a",
    },
  ],
]);
const queryProps = {
  queryKinds: [...kinds.values()],
  resourceQuery: "",
  onResourceQueryApply: vi.fn(),
};

beforeEach(() => {
  localStorage.setItem("steward.locale", "en-US");
  vi.mocked(listScopes)
    .mockReset()
    .mockResolvedValue({
      items: [
        {
          id: "scope-region",
          connection_id: "connection-a",
          kind: "region",
          native_id: "cn-hangzhou",
          name: "Hangzhou",
          created_at: "2026-07-31T00:00:00Z",
          updated_at: "2026-07-31T00:00:00Z",
        },
      ],
    });
  vi.mocked(listConnectionRegions).mockReset().mockResolvedValue({ items: [] });
  vi.mocked(listAssets)
    .mockReset()
    .mockResolvedValue({
      items: [
        {
          id: "asset-ecs",
          identity: {
            provider: "alicloud",
            partition: "aliyun",
            connection_id: "connection-a",
            native_type: "ACS::ECS::Instance",
            native_id: "i-production",
          },
          scope_id: "scope-region",
          resource_kind_id: "ecs",
          name: "生产 ECS",
          capabilities: [],
          normalized: { vpc_id: "vpc-production" },
          first_seen_at: "2026-07-31T00:00:00Z",
          last_seen_at: "2026-07-31T00:00:00Z",
        },
      ],
    });
});

it("waits for Chinese IME composition, then shows and activates live results", async () => {
  const onHighlight = vi.fn();
  const onSelect = vi.fn();
  render(
    <QueryClientProvider
      client={
        new QueryClient({
          defaultOptions: { queries: { retry: false } },
        })
      }
    >
      <LocaleProvider>
        <PanoramaSearch
          connectionId="connection-a"
          kinds={kinds}
          {...queryProps}
          scope={{
            kind: "account",
            focusKey: "",
            pathname: "/panorama",
          }}
          onHighlight={onHighlight}
          onSelect={onSelect}
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );

  const input = screen.getByRole("combobox", {
    name: "Search the current resource canvas",
  });
  fireEvent.compositionStart(input);
  fireEvent.change(input, { target: { value: "生产" } });
  await new Promise((resolve) => window.setTimeout(resolve, 220));
  expect(listAssets).not.toHaveBeenCalled();

  fireEvent.compositionEnd(input);
  const row = await screen.findByText("生产 ECS");
  expect(screen.getByText("i-production")).toBeVisible();
  expect(screen.getByText("ECS Instance")).toBeVisible();
  await waitFor(() =>
    expect(listAssets).toHaveBeenCalledWith(
      "connection-a",
      expect.objectContaining({
        query: "生产",
        limit: 50,
        canvas: "account",
        searchOrder: "panorama",
      }),
    ),
  );

  const option = row.closest('[role="option"]');
  expect(option).not.toBeNull();
  fireEvent.pointerMove(option!);
  expect(onHighlight).toHaveBeenLastCalledWith(
    expect.objectContaining({ resourceKey: "asset-ecs" }),
  );
  fireEvent.pointerDown(option!);
  expect(onSelect).toHaveBeenCalledWith(
    expect.objectContaining({
      pathname: "/panorama/regions/cn-hangzhou/vpcs/vpc-production",
    }),
  );
});

it("activates the selected result with Enter", async () => {
  const onSelect = vi.fn();
  render(
    <QueryClientProvider
      client={
        new QueryClient({
          defaultOptions: { queries: { retry: false } },
        })
      }
    >
      <LocaleProvider>
        <PanoramaSearch
          connectionId="connection-a"
          kinds={kinds}
          {...queryProps}
          scope={{
            kind: "account",
            focusKey: "",
            pathname: "/panorama",
          }}
          onHighlight={() => undefined}
          onSelect={onSelect}
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );

  const input = screen.getByRole("combobox", {
    name: "Search the current resource canvas",
  });
  fireEvent.change(input, { target: { value: "production" } });
  await screen.findByText("生产 ECS");
  fireEvent.keyDown(input, { key: "Enter" });

  expect(onSelect).toHaveBeenCalledWith(
    expect.objectContaining({ resourceKey: "asset-ecs" }),
  );
});

it("suggests region values in advanced query mode", async () => {
  render(
    <QueryClientProvider
      client={
        new QueryClient({
          defaultOptions: { queries: { retry: false } },
        })
      }
    >
      <LocaleProvider>
        <PanoramaSearch
          connectionId="connection-a"
          kinds={kinds}
          {...queryProps}
          scope={{
            kind: "account",
            focusKey: "",
            pathname: "/panorama",
          }}
          onHighlight={() => undefined}
          onSelect={() => undefined}
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );

  fireEvent.click(
    screen.getByRole("button", {
      name: "Normal search · Switch to advanced query",
    }),
  );
  const editor = await screen.findByRole("combobox", {
    name: "Resource query editor",
  });
  fireEvent.change(editor, { target: { value: "region = " } });
  fireEvent.click(await screen.findByRole("option", { name: /cn-hangzhou/ }));
  expect(editor).toHaveValue('region = "cn-hangzhou" ');
});

it("removes Region from the search copy and scopes requests below the account canvas", async () => {
  render(
    <QueryClientProvider
      client={
        new QueryClient({
          defaultOptions: { queries: { retry: false } },
        })
      }
    >
      <LocaleProvider>
        <PanoramaSearch
          connectionId="connection-a"
          kinds={kinds}
          {...queryProps}
          scope={{
            kind: "region",
            regionId: "cn-hangzhou",
            focusKey: "region:test",
            pathname: "/panorama/regions/cn-hangzhou",
          }}
          onHighlight={() => undefined}
          onSelect={() => undefined}
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );

  expect(
    screen.getByPlaceholderText("Search this canvas by resource ID or name…"),
  ).toBeVisible();
  expect(
    screen.queryByPlaceholderText(
      "Search this canvas by Region, resource ID, or name…",
    ),
  ).not.toBeInTheDocument();

  fireEvent.change(
    screen.getByRole("combobox", {
      name: "Search the current resource canvas",
    }),
    { target: { value: "production" } },
  );
  await waitFor(() =>
    expect(listAssets).toHaveBeenCalledWith(
      "connection-a",
      expect.objectContaining({
        canvas: "region",
        regionID: "cn-hangzhou",
        searchOrder: "panorama",
      }),
    ),
  );
});
