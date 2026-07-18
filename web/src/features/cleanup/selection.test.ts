import { beforeEach, describe, expect, it, vi } from "vitest";
import type { CleanupSelector } from "@/api/types";
import {
  clearCleanupSelectionHandoff,
  decodeSelector,
  encodeSelector,
  readCleanupSelectionHandoff,
  writeCleanupSelectionHandoff,
} from "./selection";

const connectionKey = "steward:cleanup-cln-handoff:v1:connection-a";
const otherConnectionKey = "steward:cleanup-cln-handoff:v1:connection-b";
const selectors: CleanupSelector[] = [
  {
    kind: "scope",
    connection_id: "connection-a",
    scope_id: "region:cn-hangzhou",
    scope_kind: "region",
    display_name: "Hangzhou",
  },
  {
    kind: "asset",
    asset_id: "asset-a",
    display_name: "Asset A",
  },
];

describe("cleanup selection handoff", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    clearCleanupSelectionHandoff("connection-a");
    clearCleanupSelectionHandoff("connection-b");
    sessionStorage.clear();
  });

  it("round-trips cleanup selectors under the connection-scoped key", () => {
    expect(writeCleanupSelectionHandoff("connection-a", selectors)).toBe(true);

    expect(JSON.parse(sessionStorage.getItem(connectionKey) ?? "{}")).toEqual({
      version: 1,
      selectors,
    });
    expect(readCleanupSelectionHandoff("connection-a")).toEqual(selectors);
  });

  it.each([
    ["version", { version: 2, selectors }],
    [
      "selector",
      {
        version: 1,
        selectors: [{ kind: "asset", display_name: "Missing ID" }],
      },
    ],
    [
      "selector fields",
      {
        version: 1,
        selectors: [
          {
            kind: "scope",
            scope_id: "region:cn-hangzhou",
            descendants: "yes",
          },
        ],
      },
    ],
    [
      "selector connection",
      {
        version: 1,
        selectors: [
          {
            kind: "group",
            connection_id: "connection-b",
            group_key: "group-vpc-a",
          },
        ],
      },
    ],
  ])("rejects and removes a handoff with an invalid %s", (_label, payload) => {
    sessionStorage.setItem(connectionKey, JSON.stringify(payload));

    expect(readCleanupSelectionHandoff("connection-a")).toEqual([]);
    expect(sessionStorage.getItem(connectionKey)).toBeNull();
  });

  it("deduplicates selectors while reading a valid handoff", () => {
    sessionStorage.setItem(
      connectionKey,
      JSON.stringify({
        version: 1,
        selectors: [selectors[0], selectors[1], selectors[0]],
      }),
    );

    expect(readCleanupSelectionHandoff("connection-a")).toEqual(selectors);
  });

  it("clears only the current connection handoff", () => {
    sessionStorage.setItem(
      connectionKey,
      JSON.stringify({ version: 1, selectors }),
    );
    sessionStorage.setItem(
      otherConnectionKey,
      JSON.stringify({ version: 1, selectors: [selectors[1]] }),
    );

    clearCleanupSelectionHandoff("connection-a");

    expect(sessionStorage.getItem(connectionKey)).toBeNull();
    expect(sessionStorage.getItem(otherConnectionKey)).not.toBeNull();
  });

  it("returns false and ignores a stale handoff when storage cannot replace it", () => {
    sessionStorage.setItem(
      connectionKey,
      JSON.stringify({ version: 1, selectors: [selectors[0]] }),
    );
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("Quota exceeded", "QuotaExceededError");
    });
    vi.spyOn(Storage.prototype, "removeItem").mockImplementation(() => {
      throw new DOMException("Storage unavailable", "SecurityError");
    });

    expect(writeCleanupSelectionHandoff("connection-a", [selectors[1]])).toBe(
      false,
    );
    expect(sessionStorage.getItem(connectionKey)).not.toBeNull();
    expect(readCleanupSelectionHandoff("connection-a")).toEqual([]);

    clearCleanupSelectionHandoff("connection-a");

    expect(readCleanupSelectionHandoff("connection-a")).toEqual([]);
  });

  it("keeps legacy selector URL values compatible", () => {
    for (const selector of selectors) {
      expect(decodeSelector(encodeSelector(selector))).toEqual(selector);
    }
  });

  it("keeps legacy comma-separated asset URL values compatible", () => {
    const params = new URLSearchParams({ assets: "asset-a,asset-b" });

    expect((params.get("assets") ?? "").split(",").filter(Boolean)).toEqual([
      "asset-a",
      "asset-b",
    ]);
  });
});
