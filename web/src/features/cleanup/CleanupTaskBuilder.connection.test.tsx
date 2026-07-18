import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import type { CloudConnection, CleanupSelector } from "@/api/types";
import {
  ActiveConnectionProvider,
  useActiveConnection,
} from "@/connections/ActiveConnectionProvider";
import type { CleanupTarget } from "@/features/panorama/cleanupSelection";
import {
  cleanupSelectionStorageKey,
  CleanupSelectionProvider,
  useCleanupSelection,
} from "@/features/panorama/CleanupSelectionContext";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import { CleanupTaskBuilder } from "./CleanupTaskBuilder";
import {
  readCleanupSelectionHandoff,
  writeCleanupSelectionHandoff,
} from "./selection";

const apiHarness = vi.hoisted(() => ({
  createCleanupTask: vi.fn(),
  findAssets: vi.fn(),
  listConnections: vi.fn(),
  listProviderCatalog: vi.fn(),
}));

vi.mock("@/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/client")>()),
  createCleanupTask: apiHarness.createCleanupTask,
  findAssets: apiHarness.findAssets,
  listConnections: apiHarness.listConnections,
  listProviderCatalog: apiHarness.listProviderCatalog,
}));

function connection(id: string, name: string): CloudConnection {
  return {
    id,
    name,
    provider: "alicloud",
    partition: "public",
    principal: `${id}-principal`,
    status: "active",
    credential: {
      type: "access_key",
      updated_at: "2026-07-30T00:00:00Z",
    },
    created_at: "2026-07-30T00:00:00Z",
    updated_at: "2026-07-30T00:00:00Z",
  };
}

const connectionA = connection("connection-a", "Production");
const connectionB = connection("connection-b", "Staging");

const selectorA: CleanupSelector = {
  kind: "scope",
  connection_id: "connection-a",
  scope_id: "region:cn-hangzhou",
  scope_kind: "region",
  display_name: "Hangzhou",
};
const selectorB: CleanupSelector = {
  kind: "scope",
  connection_id: "connection-b",
  scope_id: "region:cn-shanghai",
  scope_kind: "region",
  display_name: "Shanghai",
};
const laterSelectorB: CleanupSelector = {
  kind: "scope",
  connection_id: "connection-b",
  scope_id: "region:cn-beijing",
  scope_kind: "region",
  display_name: "B target added later",
};
const laterSelectorA: CleanupSelector = {
  kind: "scope",
  connection_id: "connection-a",
  scope_id: "region:cn-beijing",
  scope_kind: "region",
  display_name: "A target added later",
};

const targetA: CleanupTarget = {
  key: "region:cn-hangzhou",
  kind: "region",
  connectionId: "connection-a",
  displayName: "Hangzhou",
  selector: selectorA,
  ancestryKeys: ["region:cn-hangzhou"],
};
const targetB: CleanupTarget = {
  key: "region:cn-shanghai",
  kind: "region",
  connectionId: "connection-b",
  displayName: "Shanghai",
  selector: selectorB,
  ancestryKeys: ["region:cn-shanghai"],
};
const laterTargetB: CleanupTarget = {
  key: "region:cn-beijing",
  kind: "region",
  connectionId: "connection-b",
  displayName: "B target added later",
  selector: laterSelectorB,
  ancestryKeys: ["region:cn-beijing"],
};
const laterTargetA: CleanupTarget = {
  key: "region:cn-beijing",
  kind: "region",
  connectionId: "connection-a",
  displayName: "A target added later",
  selector: laterSelectorA,
  ancestryKeys: ["region:cn-beijing"],
};

function storeCleanupTargets(
  connectionID: string,
  targets: readonly CleanupTarget[],
) {
  sessionStorage.setItem(
    cleanupSelectionStorageKey(connectionID),
    JSON.stringify({ version: 1, targets }),
  );
}

function readCleanupTargets(connectionID: string): CleanupTarget[] {
  const stored = sessionStorage.getItem(
    cleanupSelectionStorageKey(connectionID),
  );
  return stored
    ? (JSON.parse(stored) as { targets: CleanupTarget[] }).targets
    : [];
}

function ConnectionControls() {
  const { activeConnectionID } = useActiveConnection();
  const cleanupSelection = useCleanupSelection();
  return (
    <div>
      <span data-testid="active-connection">{activeConnectionID}</span>
      <button
        type="button"
        onClick={() => cleanupSelection.requestConnectionChange("connection-b")}
      >
        Switch to B
      </button>
      <button
        type="button"
        onClick={() => cleanupSelection.addTargets([laterTargetB])}
      >
        Add B target
      </button>
      <button
        type="button"
        onClick={() => cleanupSelection.addTargets([laterTargetA])}
      >
        Add A target
      </button>
    </div>
  );
}

function LocationProbe() {
  const location = useLocation();
  return <span data-testid="location">{location.pathname}</span>;
}

function renderCleanupTaskBuilderWithRealProviders() {
  const queryClient = new QueryClient({
    defaultOptions: {
      mutations: { retry: false },
      queries: { retry: false },
    },
  });
  queryClient.setQueryData(["connections", "active-context"], {
    items: [connectionA, connectionB],
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter
        initialEntries={[
          {
            pathname: "/cleanup/new",
            state: { fromPanoramaCleanup: true },
          },
        ]}
      >
        <LocaleProvider>
          <ActiveConnectionProvider>
            <CleanupSelectionProvider>
              <ConnectionControls />
              <LocationProbe />
              <CleanupTaskBuilder />
            </CleanupSelectionProvider>
          </ActiveConnectionProvider>
        </LocaleProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

async function submitTask() {
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Confirm" }));
}

async function switchToConnectionB() {
  const user = userEvent.setup();
  fireEvent.click(screen.getByText("Switch to B"));
  const confirmation = screen.queryByRole("button", {
    name: "Clear and switch",
  });
  if (confirmation) await user.click(confirmation);
  await waitFor(() =>
    expect(screen.getByTestId("active-connection")).toHaveTextContent(
      "connection-b",
    ),
  );
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, reject, resolve };
}

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem("steward.locale", "en-US");
  localStorage.setItem("steward.active-connection", "connection-a");
  sessionStorage.clear();
  apiHarness.createCleanupTask.mockReset();
  apiHarness.findAssets.mockReset();
  apiHarness.listConnections.mockReset();
  apiHarness.listProviderCatalog.mockReset();
  apiHarness.findAssets.mockResolvedValue([]);
  apiHarness.listConnections.mockResolvedValue({
    items: [connectionA, connectionB],
  });
  apiHarness.listProviderCatalog.mockResolvedValue([]);
});

it("loads the switched connection only after its cleanup selection is restored", async () => {
  storeCleanupTargets("connection-b", [targetB]);
  renderCleanupTaskBuilderWithRealProviders();

  await switchToConnectionB();

  expect(await screen.findByText("Shanghai")).toBeVisible();
  expect(screen.queryByText("Hangzhou")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Confirm" })).toBeEnabled();
});

it("creates from and consumes the hydrated same-connection snapshot", async () => {
  storeCleanupTargets("connection-a", [targetA]);
  writeCleanupSelectionHandoff("connection-a", [selectorA]);
  apiHarness.createCleanupTask.mockResolvedValue({ task: { id: "cln-a" } });
  renderCleanupTaskBuilderWithRealProviders();

  expect(await screen.findByText("Hangzhou")).toBeVisible();
  await submitTask();

  await waitFor(() =>
    expect(screen.getByTestId("location")).toHaveTextContent("/cleanup/cln-a"),
  );
  expect(readCleanupTargets("connection-a")).toEqual([]);
  expect(readCleanupSelectionHandoff("connection-a")).toEqual([]);
  expect(apiHarness.createCleanupTask).toHaveBeenCalledWith("connection-a", {
    selectors: [selectorA],
    request_options: {},
  });
});

it("ignores an old connection success without consuming newer targets or navigating", async () => {
  const pending = deferred<{ task: { id: string } }>();
  storeCleanupTargets("connection-a", [targetA]);
  storeCleanupTargets("connection-b", [targetB]);
  writeCleanupSelectionHandoff("connection-a", [selectorA]);
  writeCleanupSelectionHandoff("connection-b", [selectorB]);
  apiHarness.createCleanupTask.mockReturnValue(pending.promise);
  const user = userEvent.setup();
  renderCleanupTaskBuilderWithRealProviders();

  expect(await screen.findByText("Hangzhou")).toBeVisible();
  await submitTask();
  await switchToConnectionB();
  expect(await screen.findByText("Shanghai")).toBeVisible();
  fireEvent.click(screen.getByText("Add B target"));
  expect(readCleanupTargets("connection-b")).toEqual([targetB, laterTargetB]);

  await act(async () => {
    pending.resolve({ task: { id: "cln-a" } });
    await pending.promise;
  });

  expect(screen.getByTestId("location")).toHaveTextContent("/cleanup/new");
  expect(readCleanupTargets("connection-b")).toEqual([targetB, laterTargetB]);
  expect(readCleanupSelectionHandoff("connection-b")).toEqual([selectorB]);
  expect(screen.getByText("Shanghai")).toBeVisible();
});

it("does not consume or navigate when the submitted connection snapshot changes", async () => {
  const pending = deferred<{ task: { id: string } }>();
  storeCleanupTargets("connection-a", [targetA]);
  writeCleanupSelectionHandoff("connection-a", [selectorA]);
  apiHarness.createCleanupTask.mockReturnValue(pending.promise);
  const user = userEvent.setup();
  renderCleanupTaskBuilderWithRealProviders();

  expect(await screen.findByText("Hangzhou")).toBeVisible();
  await submitTask();
  fireEvent.click(screen.getByText("Add A target"));
  expect(readCleanupTargets("connection-a")).toEqual([targetA, laterTargetA]);

  await act(async () => {
    pending.resolve({ task: { id: "cln-a" } });
    await pending.promise;
  });

  expect(screen.getByTestId("location")).toHaveTextContent("/cleanup/new");
  expect(readCleanupTargets("connection-a")).toEqual([targetA, laterTargetA]);
  expect(readCleanupSelectionHandoff("connection-a")).toEqual([selectorA]);
});

it("ignores an old connection failure after the new selection is restored", async () => {
  const pending = deferred<{ task: { id: string } }>();
  storeCleanupTargets("connection-a", [targetA]);
  storeCleanupTargets("connection-b", [targetB]);
  writeCleanupSelectionHandoff("connection-a", [selectorA]);
  writeCleanupSelectionHandoff("connection-b", [selectorB]);
  apiHarness.createCleanupTask.mockReturnValue(pending.promise);
  renderCleanupTaskBuilderWithRealProviders();

  expect(await screen.findByText("Hangzhou")).toBeVisible();
  await submitTask();
  await switchToConnectionB();
  expect(await screen.findByText("Shanghai")).toBeVisible();

  await act(async () => {
    pending.reject(new Error("connection A create failed"));
    await pending.promise.catch(() => undefined);
  });

  expect(screen.getByTestId("location")).toHaveTextContent("/cleanup/new");
  expect(
    screen.queryByText("connection A create failed"),
  ).not.toBeInTheDocument();
  expect(readCleanupTargets("connection-b")).toEqual([targetB]);
  expect(screen.getByText("Shanghai")).toBeVisible();
});
