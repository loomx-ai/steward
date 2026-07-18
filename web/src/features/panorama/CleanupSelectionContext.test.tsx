import { useRef, useState, type ReactNode } from "react";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { CleanupTarget } from "./cleanupSelection";
import {
  CleanupSelectionProvider,
  useCleanupSelection,
} from "./CleanupSelectionContext";

const activeConnectionMock = vi.hoisted(() => ({
  activeConnectionID: "connection-a",
  setActiveConnectionID: vi.fn(),
}));
const warningMock = vi.hoisted(() => vi.fn());
const infoMock = vi.hoisted(() => vi.fn());

vi.mock("@/connections/ActiveConnectionProvider", () => ({
  useActiveConnection: () => activeConnectionMock,
}));

vi.mock("@/i18n/LocaleProvider", () => ({
  useLocale: () => ({
    t: (key: string, values?: Record<string, string | number>) =>
      key === "panorama.switchConnectionDescription"
        ? `Switching clears ${values?.count} cleanup targets.`
        : key === "panorama.targetAlreadyCovered"
          ? "This cleanup target is already covered by the list."
          : key === "panorama.targetsMerged"
            ? `Merged ${values?.count} existing cleanup targets.`
            : key,
  }),
}));

vi.mock("sonner", () => ({
  toast: {
    info: infoMock,
    warning: warningMock,
  },
}));

const storageKey = "steward:panorama-cleanup:v1:connection-a";
const resourceTarget: CleanupTarget = {
  key: "asset:asset-a",
  kind: "resource",
  connectionId: "connection-a",
  displayName: "Asset A",
  ancestryKeys: ["region:cn-hangzhou", "asset:asset-a"],
  selector: { kind: "asset", asset_id: "asset-a" },
};
const regionTarget: CleanupTarget = {
  key: "region:cn-hangzhou",
  kind: "region",
  connectionId: "connection-a",
  displayName: "Hangzhou",
  ancestryKeys: ["region:cn-hangzhou"],
  selector: {
    kind: "scope",
    connection_id: "connection-a",
    scope_id: "cn-hangzhou",
    scope_kind: "region",
    descendants: true,
  },
};

function stored(targets: CleanupTarget[] = [resourceTarget]) {
  return JSON.stringify({ version: 1, targets });
}

function Harness() {
  const selection = useCleanupSelection();
  const capturedTargets = useRef<readonly CleanupTarget[]>([]);
  const [consumeResult, setConsumeResult] = useState("not-consumed");
  return (
    <div>
      <span data-testid="target-count">{selection.targets.length}</span>
      <span data-testid="consume-result">{consumeResult}</span>
      {selection.targets.map((target) => (
        <span key={target.key}>{target.displayName}</span>
      ))}
      <button
        type="button"
        onClick={() => selection.addTargets([resourceTarget])}
      >
        add target
      </button>
      <button
        type="button"
        onClick={() => selection.removeTarget(resourceTarget.key)}
      >
        remove target
      </button>
      <button
        type="button"
        onClick={() => selection.addTargets([regionTarget])}
      >
        add region
      </button>
      <button
        type="button"
        onClick={() => selection.requestConnectionChange("connection-b")}
      >
        switch connection
      </button>
      <button
        type="button"
        onClick={() => {
          capturedTargets.current = selection.targets;
        }}
      >
        capture targets
      </button>
      <button
        type="button"
        onClick={() => {
          setConsumeResult(
            selection.consumeTargets(
              activeConnectionMock.activeConnectionID,
              capturedTargets.current,
            )
              ? "consumed"
              : "not-consumed",
          );
        }}
      >
        consume captured targets
      </button>
    </div>
  );
}

function renderProvider(children: ReactNode = <Harness />) {
  return render(
    <CleanupSelectionProvider>{children}</CleanupSelectionProvider>,
  );
}

describe("CleanupSelectionProvider", () => {
  beforeEach(() => {
    sessionStorage.clear();
    activeConnectionMock.activeConnectionID = "connection-a";
    activeConnectionMock.setActiveConnectionID.mockReset();
    infoMock.mockReset();
    warningMock.mockReset();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("restores a valid version-1 payload for the active connection", async () => {
    sessionStorage.setItem(storageKey, stored());

    renderProvider();

    expect(await screen.findByText("Asset A")).toBeVisible();
    expect(screen.getByTestId("target-count")).toHaveTextContent("1");
  });

  it("persists additions and removals under the active connection key", async () => {
    const user = userEvent.setup();
    renderProvider();

    await user.click(screen.getByRole("button", { name: "add target" }));
    expect(JSON.parse(sessionStorage.getItem(storageKey) ?? "{}")).toEqual({
      version: 1,
      targets: [resourceTarget],
    });

    await user.click(screen.getByRole("button", { name: "remove target" }));
    expect(JSON.parse(sessionStorage.getItem(storageKey) ?? "{}")).toEqual({
      version: 1,
      targets: [],
    });
  });

  it("notifies when an added cleanup target is already covered", async () => {
    const user = userEvent.setup();
    renderProvider();

    await user.click(screen.getByRole("button", { name: "add target" }));
    await user.click(screen.getByRole("button", { name: "add target" }));

    expect(infoMock).toHaveBeenCalledOnce();
    expect(infoMock).toHaveBeenCalledWith(
      "This cleanup target is already covered by the list.",
    );
  });

  it("notifies when a broader target merges existing cleanup targets", async () => {
    const user = userEvent.setup();
    renderProvider();

    await user.click(screen.getByRole("button", { name: "add target" }));
    await user.click(screen.getByRole("button", { name: "add region" }));

    expect(infoMock).toHaveBeenCalledOnce();
    expect(infoMock).toHaveBeenCalledWith("Merged 1 existing cleanup targets.");
  });

  it("does not consume a stale snapshot after the same connection selection changes", async () => {
    sessionStorage.setItem(storageKey, stored());
    const user = userEvent.setup();
    renderProvider();

    await screen.findByText("Asset A");
    await user.click(screen.getByRole("button", { name: "capture targets" }));
    await user.click(screen.getByRole("button", { name: "add region" }));
    expect(screen.getByText("Hangzhou")).toBeVisible();

    await user.click(
      screen.getByRole("button", { name: "consume captured targets" }),
    );

    expect(screen.getByTestId("consume-result")).toHaveTextContent(
      "not-consumed",
    );
    expect(screen.getByText("Hangzhou")).toBeVisible();
    expect(JSON.parse(sessionStorage.getItem(storageKey) ?? "{}")).toEqual({
      version: 1,
      targets: [regionTarget],
    });
  });

  it("discards corrupt JSON without throwing", async () => {
    sessionStorage.setItem(storageKey, "{not-json");

    renderProvider();

    await waitFor(() =>
      expect(screen.getByTestId("target-count")).toHaveTextContent("0"),
    );
    expect(sessionStorage.getItem(storageKey)).toBeNull();
    expect(warningMock).toHaveBeenCalledTimes(1);
    expect(warningMock).toHaveBeenCalledWith(
      "panorama.cleanupPersistenceFailed",
    );
  });

  it.each([
    ["payload version", { version: 2, targets: [resourceTarget] }],
    [
      "target connection",
      {
        version: 1,
        targets: [{ ...resourceTarget, connectionId: "connection-b" }],
      },
    ],
    [
      "target kind",
      {
        version: 1,
        targets: [{ ...resourceTarget, kind: "account" }],
      },
    ],
    [
      "target string fields",
      {
        version: 1,
        targets: [{ ...resourceTarget, displayName: 42 }],
      },
    ],
    [
      "target ancestry",
      {
        version: 1,
        targets: [{ ...resourceTarget, ancestryKeys: ["region", 42] }],
      },
    ],
    [
      "target location context",
      {
        version: 1,
        targets: [
          {
            ...resourceTarget,
            locationContext: {
              region: { key: 42, name: "华东1（杭州）" },
            },
          },
        ],
      },
    ],
    [
      "target selector",
      {
        version: 1,
        targets: [{ ...resourceTarget, selector: { kind: "asset" } }],
      },
    ],
  ])("discards a payload with an invalid %s", async (_label, payload) => {
    sessionStorage.setItem(storageKey, JSON.stringify(payload));

    renderProvider();

    await waitFor(() =>
      expect(screen.getByTestId("target-count")).toHaveTextContent("0"),
    );
    expect(sessionStorage.getItem(storageKey)).toBeNull();
    expect(warningMock).toHaveBeenCalledTimes(1);
    expect(warningMock).toHaveBeenCalledWith(
      "panorama.cleanupPersistenceFailed",
    );
  });

  it("keeps an added target in memory and warns when persistence fails", async () => {
    const originalSetItem = Storage.prototype.setItem;
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(function (
      this: Storage,
      key,
      value,
    ) {
      if (this === sessionStorage) {
        throw new DOMException("Quota exceeded", "QuotaExceededError");
      }
      return originalSetItem.call(this, key, value);
    });
    const user = userEvent.setup();
    renderProvider();

    await user.click(screen.getByRole("button", { name: "add target" }));

    expect(screen.getByText("Asset A")).toBeVisible();
    expect(warningMock).toHaveBeenCalledWith(
      "panorama.cleanupPersistenceFailed",
    );
  });

  it("switches immediately when there are no cleanup targets", async () => {
    const user = userEvent.setup();
    renderProvider();

    await user.click(screen.getByRole("button", { name: "switch connection" }));

    expect(activeConnectionMock.setActiveConnectionID).toHaveBeenCalledWith(
      "connection-b",
    );
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });

  it("asks for confirmation and includes the current target count", async () => {
    sessionStorage.setItem(storageKey, stored());
    const user = userEvent.setup();
    renderProvider();

    await screen.findByText("Asset A");
    await user.click(screen.getByRole("button", { name: "switch connection" }));

    expect(
      screen.getByRole("heading", {
        name: "panorama.switchConnectionTitle",
      }),
    ).toBeVisible();
    expect(
      screen.getByText("Switching clears 1 cleanup targets."),
    ).toBeVisible();
    expect(activeConnectionMock.setActiveConnectionID).not.toHaveBeenCalled();
  });

  it("leaves the active connection and targets untouched when canceled", async () => {
    sessionStorage.setItem(storageKey, stored());
    const user = userEvent.setup();
    renderProvider();

    await screen.findByText("Asset A");
    await user.click(screen.getByRole("button", { name: "switch connection" }));
    await user.click(
      screen.getByRole("button", {
        name: "panorama.switchConnectionCancel",
      }),
    );

    expect(activeConnectionMock.setActiveConnectionID).not.toHaveBeenCalled();
    expect(screen.getByText("Asset A")).toBeVisible();
    expect(sessionStorage.getItem(storageKey)).toBe(stored());
  });

  it("clears the old selection before confirming a connection change", async () => {
    sessionStorage.setItem(storageKey, stored());
    activeConnectionMock.setActiveConnectionID.mockImplementation(() => {
      expect(sessionStorage.getItem(storageKey)).toBeNull();
    });
    const user = userEvent.setup();
    renderProvider();

    await screen.findByText("Asset A");
    await user.click(screen.getByRole("button", { name: "switch connection" }));
    await user.click(
      screen.getByRole("button", {
        name: "panorama.switchConnectionConfirm",
      }),
    );

    expect(screen.getByTestId("target-count")).toHaveTextContent("0");
    expect(activeConnectionMock.setActiveConnectionID).toHaveBeenCalledWith(
      "connection-b",
    );
  });
});
