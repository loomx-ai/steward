import { expect, it } from "vitest";
import type { CleanupTarget } from "./cleanupSelection";
import {
  cleanupTargetCanvasPath,
  cleanupTargetContainsKey,
} from "./cleanupLocation";

function target(
  overrides: Partial<CleanupTarget> & Pick<CleanupTarget, "key" | "kind">,
): CleanupTarget {
  return {
    connectionId: "connection-a",
    displayName: overrides.key,
    selector: { kind: "asset", asset_id: "asset-a" },
    ancestryKeys: [overrides.key],
    ...overrides,
  };
}

it("maps cleanup targets back to their owning canvas", () => {
  expect(
    cleanupTargetCanvasPath(target({ key: "region-a", kind: "region" })),
  ).toBe("/panorama");
  expect(
    cleanupTargetCanvasPath(
      target({
        key: "vpc-a",
        kind: "vpc",
        locationContext: {
          region: {
            key: "region-a",
            name: "Hangzhou",
            native_id: "cn-hangzhou",
          },
        },
      }),
    ),
  ).toBe("/panorama/regions/cn-hangzhou");
  expect(
    cleanupTargetCanvasPath(
      target({
        key: "asset-a",
        kind: "resource",
        locationContext: {
          region: {
            key: "region-a",
            name: "Hangzhou",
            native_id: "cn-hangzhou",
          },
          vpc: { key: "vpc-a", name: "Production", native_id: "vpc-a" },
        },
      }),
    ),
  ).toBe("/panorama/regions/cn-hangzhou/vpcs/vpc-a");
  expect(
    cleanupTargetCanvasPath(
      target({
        key: "asset-global",
        kind: "resource",
        locationContext: {
          scope: { key: "account-global", name: "Global resources" },
        },
      }),
    ),
  ).toBe("/panorama/global");
  expect(
    cleanupTargetCanvasPath(
      target({
        key: "asset-public",
        kind: "resource",
        locationContext: {
          region: {
            key: "region-a",
            name: "Hangzhou",
            native_id: "cn-hangzhou",
          },
          scope: { key: "region-public-a", name: "Public resources" },
        },
      }),
    ),
  ).toBe("/panorama/regions/cn-hangzhou/public");
});

it("matches both a cleanup group and one of its resource members", () => {
  const group = target({
    key: "stack:database",
    kind: "resource_batch",
    selector: [
      { kind: "asset", asset_id: "asset-a" },
      { kind: "asset", asset_id: "asset-b" },
    ],
  });

  expect(cleanupTargetContainsKey(group, "stack:database")).toBe(true);
  expect(cleanupTargetContainsKey(group, "asset:asset-b")).toBe(true);
  expect(cleanupTargetContainsKey(group, "asset:other")).toBe(false);
});
