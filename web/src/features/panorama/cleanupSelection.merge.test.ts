import { expect, it } from "vitest";
import type { CleanupSelector } from "@/api/types";
import { selectorKey } from "@/features/cleanup/selection";
import {
  addCleanupTargets,
  type CleanupMergeResult,
  type CleanupTarget,
  type CleanupTargetLocationContext,
} from "./cleanupSelection";

// Random targets drawn from small pools so keys, selectors, ancestry and batch
// members collide often across kinds and connections.
function randomTargets(next: () => number, count: number): CleanupTarget[] {
  const pick = <T>(values: readonly T[]) =>
    values[Math.floor(next() * values.length)] as T;
  const connections = ["c1", "c2"];
  const regions = ["region:r1", "region:r2"];
  const vpcs = ["vpc:v1", "vpc:v2", "vpc:v3"];
  const assets = Array.from({ length: 12 }, (_, i) => `a${i}`);
  return Array.from({ length: count }, (): CleanupTarget => {
    const connectionId = pick(connections);
    const region = pick(regions);
    const vpc = pick(vpcs);
    const roll = next();
    if (roll < 0.15) {
      return {
        key: region,
        kind: "region",
        connectionId,
        displayName: region,
        ancestryKeys: [region],
        selector: {
          kind: "scope",
          connection_id: connectionId,
          scope_id: region,
          scope_kind: "region",
          descendants: true,
        },
      };
    }
    if (roll < 0.3) {
      return {
        key: vpc,
        kind: "vpc",
        connectionId,
        displayName: vpc,
        ancestryKeys: [region, vpc],
        selector: {
          kind: "group",
          connection_id: connectionId,
          group_key: vpc,
        },
      };
    }
    if (roll < 0.7) {
      const id = pick(assets);
      return {
        key: `asset:${id}`,
        kind: "resource",
        connectionId,
        displayName: id,
        ancestryKeys: [region, vpc, `asset:${id}`],
        selector: { kind: "asset", asset_id: id },
      };
    }
    const ids = Array.from({ length: Math.floor(next() * 5) }, () =>
      pick(assets),
    );
    return {
      key: `batch:${vpc}`,
      kind: "resource_batch",
      connectionId,
      displayName: vpc,
      ancestryKeys: [region, vpc],
      selector: ids.map((id) => ({ kind: "asset", asset_id: id })),
      memberAssetIds: ids,
      resourceCount: ids.length + Math.floor(next() * 3),
    };
  });
}

it("matches the previous quadratic merge on randomized input", () => {
  let seed = 42;
  const next = () => {
    seed = (seed * 1103515245 + 12345) % 2147483648;
    return seed / 2147483648;
  };
  for (let round = 0; round < 400; round += 1) {
    const current = legacyAddCleanupTargets(
      [],
      randomTargets(next, Math.floor(next() * 20)),
    ).targets;
    const incoming = randomTargets(next, Math.floor(next() * 20));
    expect(addCleanupTargets(current, incoming)).toEqual(
      legacyAddCleanupTargets(current, incoming),
    );
    // Unnormalized current lists must behave the same way too.
    const raw = randomTargets(next, Math.floor(next() * 10));
    expect(addCleanupTargets(raw, incoming)).toEqual(
      legacyAddCleanupTargets(raw, incoming),
    );
  }
});

// The pre-index implementation, kept verbatim as the reference oracle.
function asSelectors(
  selector: CleanupSelector | readonly CleanupSelector[],
): CleanupSelector[] {
  return (Array.isArray(selector) ? selector : [selector]).map((value) => ({
    ...value,
  }));
}

function cloneTarget(target: CleanupTarget): CleanupTarget {
  return {
    ...target,
    selector: Array.isArray(target.selector)
      ? asSelectors(target.selector)
      : { ...target.selector },
    ancestryKeys: [...target.ancestryKeys],
    locationContext: cloneLocationContext(target.locationContext),
    memberAssetIds: target.memberAssetIds && [...target.memberAssetIds],
  };
}

function cloneLocationContext(
  context: CleanupTargetLocationContext | undefined,
): CleanupTargetLocationContext | undefined {
  if (!context) return undefined;
  return {
    region: context.region && { ...context.region },
    vpc: context.vpc && { ...context.vpc },
    scope: context.scope && { ...context.scope },
  };
}

function isRangeTarget(target: CleanupTarget): boolean {
  return target.kind === "region" || target.kind === "vpc";
}

function covers(parent: CleanupTarget, child: CleanupTarget): boolean {
  if (parent.connectionId !== child.connectionId) return false;
  const parentIndex = child.ancestryKeys.indexOf(parent.key);
  return parentIndex >= 0 && parentIndex < child.ancestryKeys.length - 1;
}

function selectorKeys(target: CleanupTarget): string[] {
  return asSelectors(target.selector).map(selectorKey);
}

function assetIDs(target: CleanupTarget): string[] {
  return asSelectors(target.selector).flatMap((selector) =>
    selector.kind === "asset" ? [selector.asset_id] : [],
  );
}

function selectorSetsMatch(
  left: readonly string[],
  right: readonly string[],
): boolean {
  return (
    left.length === right.length &&
    left.every((key) => right.includes(key)) &&
    right.every((key) => left.includes(key))
  );
}

function sameConnection(
  left: Pick<CleanupTarget, "connectionId">,
  right: Pick<CleanupTarget, "connectionId">,
): boolean {
  return left.connectionId === right.connectionId;
}

function withBatchAssets(
  target: CleanupTarget,
  assetIDs: readonly string[],
  declaredResourceCount = target.resourceCount,
): CleanupTarget | null {
  const allowed = new Set(assetIDs);
  const selectors = asSelectors(target.selector).filter(
    (selector) => selector.kind !== "asset" || allowed.has(selector.asset_id),
  );
  const seen = new Set<string>();
  const uniqueSelectors = selectors.filter((selector) => {
    const key = selectorKey(selector);
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
  const members = uniqueSelectors.flatMap((selector) =>
    selector.kind === "asset" ? [selector.asset_id] : [],
  );
  if (members.length === 0) return null;
  const resourceCount = Math.max(
    declaredResourceCount ?? members.length,
    members.length,
  );
  return {
    ...cloneTarget(target),
    selector: uniqueSelectors,
    memberAssetIds: members,
    resourceCount,
  };
}

function batchResourceCount(target: CleanupTarget): number {
  const memberCount = new Set(assetIDs(target)).size;
  return Math.max(target.resourceCount ?? memberCount, memberCount);
}

function mergeBatchTargets(
  existing: CleanupTarget,
  incoming: CleanupTarget,
): CleanupTarget | null {
  const incomingMemberCount = new Set(assetIDs(incoming)).size;
  const incomingResourceCount = batchResourceCount(incoming);
  const mergedResourceCount =
    incomingResourceCount === incomingMemberCount
      ? incomingResourceCount
      : Math.max(batchResourceCount(existing), incomingResourceCount);
  return withBatchAssets(
    {
      ...cloneTarget(existing),
      selector: [
        ...asSelectors(existing.selector),
        ...asSelectors(incoming.selector),
      ],
    },
    [...assetIDs(existing), ...assetIDs(incoming)],
    mergedResourceCount,
  );
}

function normalizeIncomingBatch(target: CleanupTarget): CleanupTarget | null {
  if (target.kind !== "resource_batch") return cloneTarget(target);
  return withBatchAssets(target, [...new Set(assetIDs(target))]);
}

function existingCovers(
  current: readonly CleanupTarget[],
  incoming: CleanupTarget,
): boolean {
  const incomingKeys = selectorKeys(incoming);
  return current.some((target) => {
    if (!sameConnection(target, incoming)) return false;
    if (target.key === incoming.key) return true;
    if (isRangeTarget(target) && covers(target, incoming)) return true;
    if (incoming.kind === "resource_batch") {
      return (
        target.kind === "resource_batch" &&
        selectorSetsMatch(selectorKeys(target), incomingKeys)
      );
    }
    return incomingKeys.some((key) => selectorKeys(target).includes(key));
  });
}

function withoutBatchDuplicateAssets(
  target: CleanupTarget,
  current: readonly CleanupTarget[],
): CleanupTarget | null {
  if (target.kind !== "resource_batch") return target;
  const representedByBatch = new Set(
    current
      .filter(
        (existing) =>
          existing.kind === "resource_batch" &&
          sameConnection(existing, target),
      )
      .flatMap(assetIDs),
  );
  return withBatchAssets(
    target,
    assetIDs(target).filter((id) => !representedByBatch.has(id)),
  );
}

function incomingCovers(
  incoming: CleanupTarget,
  existing: CleanupTarget,
): boolean {
  if (!sameConnection(incoming, existing)) return false;
  if (isRangeTarget(incoming) && covers(incoming, existing)) return true;
  if (incoming.kind !== "resource_batch" || existing.kind !== "resource") {
    return false;
  }
  return assetIDs(existing).some((id) => assetIDs(incoming).includes(id));
}

function legacyAddCleanupTargets(
  current: readonly CleanupTarget[],
  incoming: readonly CleanupTarget[],
): CleanupMergeResult {
  let targets = current.map(cloneTarget);
  let mergedCount = 0;
  let coveredCount = 0;
  let addedCount = 0;

  for (const source of incoming) {
    const normalized = normalizeIncomingBatch(source);
    const matchingBatchIndex = normalized
      ? targets.findIndex(
          (target) =>
            target.kind === "resource_batch" &&
            normalized.kind === "resource_batch" &&
            target.key === normalized.key &&
            sameConnection(target, normalized),
        )
      : -1;
    if (normalized && matchingBatchIndex >= 0) {
      const existing = targets[matchingBatchIndex];
      if (existing) {
        const merged = mergeBatchTargets(existing, normalized);
        if (merged) {
          targets[matchingBatchIndex] = merged;
          const removed = targets.filter(
            (target, index) =>
              index !== matchingBatchIndex && incomingCovers(merged, target),
          );
          mergedCount += removed.length;
          targets = targets.filter(
            (target, index) =>
              index === matchingBatchIndex || !incomingCovers(merged, target),
          );
        }
      }
      continue;
    }
    if (!normalized || existingCovers(targets, normalized)) {
      coveredCount += 1;
      continue;
    }
    const candidate = withoutBatchDuplicateAssets(normalized, targets);
    if (!candidate) {
      coveredCount += 1;
      continue;
    }
    const removed = targets.filter((target) =>
      incomingCovers(candidate, target),
    );
    mergedCount += removed.length;
    targets = targets.filter((target) => !incomingCovers(candidate, target));
    targets.push(cloneTarget(candidate));
    addedCount += 1;
  }

  return {
    targets: targets.map(cloneTarget),
    mergedCount,
    coveredCount,
    addedCount,
  };
}
