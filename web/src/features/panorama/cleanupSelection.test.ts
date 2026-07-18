import { expect, it } from "vitest";
import type { CleanupSelector } from "@/api/types";
import {
  addCleanupTargets,
  expandCleanupSelectors,
  isCleanupTargetPending,
  removeCleanupBatchMember,
  removeCleanupTarget,
  type CleanupTarget,
} from "./cleanupSelection";

function target(input: CleanupTarget): CleanupTarget {
  return input;
}

const region = target({
  key: "region:cn-hangzhou",
  kind: "region",
  connectionId: "connection-a",
  displayName: "Hangzhou",
  ancestryKeys: ["region:cn-hangzhou"],
  selector: {
    kind: "scope",
    connection_id: "connection-a",
    scope_id: "scope-region-a",
    scope_kind: "region",
    descendants: true,
  },
});
const vpc = target({
  key: "vpc:cn-hangzhou:vpc-a",
  kind: "vpc",
  connectionId: "connection-a",
  displayName: "VPC A",
  ancestryKeys: ["region:cn-hangzhou", "vpc:cn-hangzhou:vpc-a"],
  selector: {
    kind: "group",
    connection_id: "connection-a",
    group_key: "vpc:cn-hangzhou:vpc-a",
  },
});
const resource = target({
  key: "asset:asset-a",
  kind: "resource",
  connectionId: "connection-a",
  displayName: "Asset A",
  ancestryKeys: [
    "region:cn-hangzhou",
    "vpc:cn-hangzhou:vpc-a",
    "asset:asset-a",
  ],
  selector: { kind: "asset", asset_id: "asset-a" },
});

function asset(id: string): CleanupTarget {
  return {
    ...resource,
    key: `asset:${id}`,
    displayName: `Asset ${id}`,
    ancestryKeys: [
      "region:cn-hangzhou",
      "vpc:cn-hangzhou:vpc-a",
      `asset:${id}`,
    ],
    selector: { kind: "asset", asset_id: id },
  };
}

function batch(ids: string[]): CleanupTarget {
  const selectors: CleanupSelector[] = ids.map((id) => ({
    kind: "asset",
    asset_id: id,
  }));
  return {
    key: "batch:vpc:cn-hangzhou:vpc-a",
    kind: "resource_batch",
    connectionId: "connection-a",
    displayName: "VPC A resources",
    ancestryKeys: ["region:cn-hangzhou", "vpc:cn-hangzhou:vpc-a"],
    selector: selectors,
    memberAssetIds: ids,
    resourceCount: ids.length,
  };
}

it("retains one target when the same selector is added twice", () => {
  const result = addCleanupTargets([], [resource, { ...resource }]);

  expect(result.targets).toEqual([resource]);
  expect(result).toMatchObject({
    mergedCount: 0,
    coveredCount: 1,
    addedCount: 1,
  });
});

it("counts a VPC and resource as covered by an existing region", () => {
  const vpcResult = addCleanupTargets([region], [vpc]);
  const resourceResult = addCleanupTargets([region], [resource]);

  expect(vpcResult).toMatchObject({
    targets: [region],
    mergedCount: 0,
    coveredCount: 1,
    addedCount: 0,
  });
  expect(resourceResult).toMatchObject({
    targets: [region],
    mergedCount: 0,
    coveredCount: 1,
    addedCount: 0,
  });
});

it("merges VPC and resource targets when adding their region", () => {
  const result = addCleanupTargets([vpc, resource], [region]);

  expect(result).toMatchObject({
    targets: [region],
    mergedCount: 2,
    coveredCount: 0,
    addedCount: 1,
  });
});

it("lets a resource batch absorb matching individual resources", () => {
  const result = addCleanupTargets(
    [asset("asset-a"), asset("asset-b")],
    [batch(["asset-a", "asset-b", "asset-c"])],
  );

  expect(result).toMatchObject({
    targets: [batch(["asset-a", "asset-b", "asset-c"])],
    mergedCount: 2,
    coveredCount: 0,
    addedCount: 1,
  });
});

it("deduplicates incoming batch selectors without shrinking its declared total", () => {
  const result = addCleanupTargets([], [batch(["asset-a", "asset-a"])]);

  expect(result.targets).toEqual([
    {
      ...batch(["asset-a", "asset-a"]),
      selector: [{ kind: "asset", asset_id: "asset-a" }],
      memberAssetIds: ["asset-a"],
      resourceCount: 2,
    },
  ]);
});

it("normalizes a single partial batch without shrinking its declared total", () => {
  const partial = {
    ...batch(["asset-a"]),
    resourceCount: 2,
  };

  const result = addCleanupTargets([], [partial]);

  expect(result.targets).toHaveLength(1);
  expect(result.targets[0]).toMatchObject({
    memberAssetIds: ["asset-a"],
    resourceCount: 2,
  });
});

it("merges new members into a same-key batch on the same connection", () => {
  const result = addCleanupTargets([batch(["asset-a"])], [batch(["asset-b"])]);

  expect(result.targets).toEqual([batch(["asset-a", "asset-b"])]);
});

it("merges same-key partial batches without shrinking either declared total", () => {
  const existing = {
    ...batch(["asset-a"]),
    resourceCount: 2,
  };
  const incoming = {
    ...batch(["asset-b"]),
    resourceCount: 3,
  };

  const result = addCleanupTargets([existing], [incoming]);

  expect(result.targets).toHaveLength(1);
  expect(result.targets[0]).toMatchObject({
    resourceCount: 3,
  });
  expect([...(result.targets[0]?.memberAssetIds ?? [])].sort()).toEqual([
    "asset-a",
    "asset-b",
  ]);
});

it("removes individual resources newly covered by a same-key batch merge", () => {
  const result = addCleanupTargets(
    [batch(["asset-a"]), asset("asset-b")],
    [batch(["asset-b"])],
  );

  expect(result).toMatchObject({
    targets: [batch(["asset-a", "asset-b"])],
    mergedCount: 1,
  });
});

it("preserves a batch topology total when duplicate coverage trims its members", () => {
  const existing = batch(["asset-a"]);
  const incoming = {
    ...batch(["asset-a", "asset-b"]),
    key: "batch:vpc:cn-hangzhou:vpc-b",
  };

  const result = addCleanupTargets([existing], [incoming]);

  expect(result.targets).toHaveLength(2);
  expect(result.targets[0]).toMatchObject(existing);
  expect(result.targets[1]).toMatchObject({
    key: incoming.key,
    selector: [{ kind: "asset", asset_id: "asset-b" }],
    memberAssetIds: ["asset-b"],
    resourceCount: 2,
  });
});

it("updates a partial batch total when a newer complete batch is merged", () => {
  const partial = {
    ...batch(["asset-b"]),
    selector: [{ kind: "asset" as const, asset_id: "asset-b" }],
    memberAssetIds: ["asset-b"],
    resourceCount: 5,
  };

  const result = addCleanupTargets(
    [partial],
    [batch(["asset-a", "asset-b", "asset-c"])],
  );

  expect(result.targets).toHaveLength(1);
  expect(result.targets[0]).toMatchObject({
    resourceCount: 3,
  });
  expect([...(result.targets[0]?.memberAssetIds ?? [])].sort()).toEqual([
    "asset-a",
    "asset-b",
    "asset-c",
  ]);
});

it("does not suppress a same-key batch from another connection", () => {
  const otherConnectionBatch = {
    ...batch(["asset-b"]),
    connectionId: "connection-b",
  };

  expect(
    addCleanupTargets([batch(["asset-a"])], [otherConnectionBatch]).targets,
  ).toEqual([batch(["asset-a"]), otherConnectionBatch]);
});

it("does not add an individual resource already represented by a batch", () => {
  const result = addCleanupTargets(
    [batch(["asset-a", "asset-b"])],
    [asset("asset-a")],
  );

  expect(result).toMatchObject({
    targets: [batch(["asset-a", "asset-b"])],
    mergedCount: 0,
    coveredCount: 1,
    addedCount: 0,
  });
});

it("removes one batch member while keeping correlated batch fields in sync", () => {
  const result = removeCleanupBatchMember(
    [batch(["asset-a", "asset-b"])],
    "batch:vpc:cn-hangzhou:vpc-a",
    "asset-a",
  );

  expect(result).toEqual([
    {
      ...batch(["asset-a", "asset-b"]),
      selector: [{ kind: "asset", asset_id: "asset-b" }],
      memberAssetIds: ["asset-b"],
      resourceCount: 2,
    },
  ]);
});

it("removes a batch when its final member is removed", () => {
  expect(
    removeCleanupBatchMember(
      [batch(["asset-a"])],
      "batch:vpc:cn-hangzhou:vpc-a",
      "asset-a",
    ),
  ).toEqual([]);
});

it("removes a complete target by key", () => {
  expect(removeCleanupTarget([vpc, resource], vpc.key)).toEqual([resource]);
});

it("removes a same-key target from only the requested connection", () => {
  const otherConnectionBatch = {
    ...batch(["asset-b"]),
    connectionId: "connection-b",
  };

  expect(
    removeCleanupTarget(
      [batch(["asset-a"]), otherConnectionBatch],
      otherConnectionBatch.key,
      "connection-b",
    ),
  ).toEqual([batch(["asset-a"])]);
});

it("edits a same-key batch in only the requested connection", () => {
  const otherConnectionBatch = {
    ...batch(["asset-b", "asset-c"]),
    connectionId: "connection-b",
  };

  expect(
    removeCleanupBatchMember(
      [batch(["asset-a", "asset-b"]), otherConnectionBatch],
      otherConnectionBatch.key,
      "asset-b",
      "connection-b",
    ),
  ).toEqual([
    batch(["asset-a", "asset-b"]),
    {
      ...otherConnectionBatch,
      selector: [{ kind: "asset", asset_id: "asset-c" }],
      memberAssetIds: ["asset-c"],
      resourceCount: 2,
    },
  ]);
});

it("expands targets into a deduplicated selector array", () => {
  expect(
    expandCleanupSelectors([
      resource,
      batch(["asset-a", "asset-b"]),
      asset("asset-b"),
    ]),
  ).toEqual([
    { kind: "asset", asset_id: "asset-a" },
    { kind: "asset", asset_id: "asset-b" },
  ]);
});

it("marks child nodes as pending when covered by a region or VPC", () => {
  expect(isCleanupTargetPending([region], resource)).toBe(true);
  expect(isCleanupTargetPending([vpc], resource)).toBe(true);
  expect(isCleanupTargetPending([], resource)).toBe(false);
});

it("does not mark an asset pending under a same-key ancestor from another connection", () => {
  expect(
    isCleanupTargetPending(
      [{ ...region, connectionId: "connection-b" }],
      resource,
    ),
  ).toBe(false);
});

it("keeps the original pending candidate shape for direct selector matching", () => {
  const candidate = {
    key: resource.key,
    ancestryKeys: resource.ancestryKeys,
    selector: resource.selector,
  };

  expect(isCleanupTargetPending([resource], candidate)).toBe(true);
  expect(isCleanupTargetPending([region], candidate)).toBe(false);
});

it("uses a full target's connection context for same-connection ancestor coverage", () => {
  expect(isCleanupTargetPending([region], resource)).toBe(true);
});
