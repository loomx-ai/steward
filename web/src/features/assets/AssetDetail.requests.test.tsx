import {
  focusManager,
  QueryClient,
  QueryClientProvider,
} from "@tanstack/react-query";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { Asset, CloudConnection, Relationship } from "@/api/types";
import { TooltipProvider } from "@/components/ui/tooltip";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import { AssetDetail } from "./AssetDetail";

const connection = {
  id: "connection-a",
  name: "Production",
  provider: "alicloud",
} as CloudConnection;

vi.mock("@/connections/ActiveConnectionProvider", () => ({
  useRequiredConnection: () => connection,
}));

vi.mock("@/app/PageTitleContext", () => ({
  PageTitle: () => null,
}));

const canvasViews = new Set<unknown>();
vi.mock("../panorama/TopologyCanvas", () => ({
  TopologyCanvas: ({ view }: { view: unknown }) => {
    canvasViews.add(view);
    return <div data-testid="topology" />;
  },
}));

function fixtureAsset(id: string): Asset {
  return {
    id,
    identity: {
      provider: "alicloud",
      partition: "public",
      connection_id: connection.id,
      native_type: "ACS::VPC::VPC",
      native_id: `native-${id}`,
    },
    scope_id: "scope-a",
    resource_kind_id: "vpc",
    capabilities: [],
    first_seen_at: "2026-08-01T00:00:00Z",
    last_seen_at: "2026-08-01T00:00:00Z",
  };
}

function edge(source: string, target: string): Relationship {
  return {
    id: `${source}->${target}`,
    source_asset_id: source,
    target_asset_id: target,
    type: "member_of",
    source: "test",
    evidence: {},
    confidence: 1,
    graph_revision: "graph-1",
    observed_at: "2026-08-01T00:00:00Z",
  };
}

// A VPC with 2,000 members and one parent.
const relationships = [
  edge("vpc-1", "parent-1"),
  ...Array.from({ length: 2000 }, (_, index) => edge(`m-${index}`, "vpc-1")),
];

let paths: string[] = [];

beforeEach(() => {
  localStorage.setItem("steward.locale", "en-US");
  paths = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string) => {
      const url = new URL(input, "http://localhost");
      paths.push(url.pathname + url.search);
      let body: unknown = { items: [] };
      if (url.pathname === "/api/assets") {
        body = { items: url.searchParams.getAll("asset_id").map(fixtureAsset) };
      } else if (url.pathname.endsWith("/graph")) {
        body = { relationships, bindings: [] };
      } else if (url.pathname.endsWith("/lifecycle")) {
        body = { bindings: [] };
      } else if (url.pathname === "/api/providers/catalog") {
        body = [];
      }
      return new Response(JSON.stringify(body), { status: 200 });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderAt(entry: string) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const tree = () => (
    <MemoryRouter initialEntries={[entry]}>
      <QueryClientProvider client={client}>
        <LocaleProvider>
          <TooltipProvider>
            <Routes>
              <Route path="/assets/:id" element={<AssetDetail />} />
            </Routes>
          </TooltipProvider>
        </LocaleProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  const { rerender } = render(tree());
  return { rerender: () => rerender(tree()), client };
}

it("loads only direct relations and the parent on the overview tab", async () => {
  renderAt("/assets/vpc-1");

  expect(await screen.findByText("2001")).toBeInTheDocument();
  await waitFor(() =>
    expect(paths.some((path) => path.includes("asset_id=parent-1"))).toBe(true),
  );
  const assetRequests = paths.filter((path) => path.startsWith("/api/assets"));
  expect(assetRequests).toHaveLength(3);
  expect(assetRequests.filter((path) => path.includes("/graph"))).toEqual([
    "/api/assets/vpc-1/graph?include=lifecycle&edges=direct&connection_id=connection-a",
  ]);
});

it("keeps the topology view stable and stops 3-hop refetches after leaving the relationships tab", async () => {
  const { rerender, client } = renderAt("/assets/vpc-1?view=relationships");
  const memberRequests = () =>
    paths.filter(
      (path) => path.startsWith("/api/assets?") && path.includes("asset_id=m-"),
    ).length;

  expect(await screen.findByTestId("topology")).toBeInTheDocument();
  expect(memberRequests()).toBeGreaterThan(0);
  // Related assets may land after the first canvas render; count views only
  // once the initial loads settle.
  await waitFor(() => expect(client.isFetching()).toBe(0));
  canvasViews.clear();
  rerender();
  let directLoads = 1;
  const refocus = async () => {
    const expected = ++directLoads;
    act(() => {
      focusManager.setFocused(false);
      focusManager.setFocused(true);
    });
    await waitFor(() =>
      expect(
        paths.filter((path) => path.includes("edges=direct")),
      ).toHaveLength(expected),
    );
  };

  // Refetched, structurally equal data must not rebuild the canvas view.
  await refocus();
  rerender();
  expect(screen.getByTestId("topology")).toBeInTheDocument();
  expect(canvasViews.size).toBe(1);

  await userEvent.click(screen.getByRole("tab", { name: /Overview/ }));
  const before = memberRequests();
  await refocus();
  expect(memberRequests()).toBe(before);
  focusManager.setFocused(undefined);
});
