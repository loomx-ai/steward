import type { CleanupSelector, TopologyViewContext } from "@/api/types";
import { selectorKey } from "@/features/cleanup/selection";

export type CleanupTargetKind =
  "region" | "vpc" | "resource" | "resource_batch";

export interface CleanupTargetLocationContext {
  region?: TopologyViewContext;
  vpc?: TopologyViewContext;
  scope?: TopologyViewContext;
}

export interface CleanupTarget {
  key: string;
  kind: CleanupTargetKind;
  connectionId: string;
  displayName: string;
  selector: CleanupSelector | CleanupSelector[];
  ancestryKeys: string[];
  locationContext?: CleanupTargetLocationContext;
  memberAssetIds?: string[];
  resourceCount?: number;
}

export interface CleanupMergeResult {
  targets: CleanupTarget[];
  mergedCount: number;
  coveredCount: number;
  addedCount: number;
}

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

export function addCleanupTargets(
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

export function removeCleanupTarget(
  current: readonly CleanupTarget[],
  targetKey: string,
  connectionId?: string,
): CleanupTarget[] {
  return current
    .filter(
      (target) =>
        target.key !== targetKey ||
        (connectionId !== undefined && target.connectionId !== connectionId),
    )
    .map(cloneTarget);
}

export function removeCleanupBatchMember(
  current: readonly CleanupTarget[],
  targetKey: string,
  assetID: string,
  connectionId?: string,
): CleanupTarget[] {
  return current.flatMap((target) => {
    if (
      target.key !== targetKey ||
      target.kind !== "resource_batch" ||
      (connectionId !== undefined && target.connectionId !== connectionId)
    ) {
      return [cloneTarget(target)];
    }
    const next = withBatchAssets(
      target,
      assetIDs(target).filter((id) => id !== assetID),
    );
    return next ? [next] : [];
  });
}

export function expandCleanupSelectors(
  targets: readonly CleanupTarget[],
): CleanupSelector[] {
  const seen = new Set<string>();
  return targets.flatMap((target) =>
    asSelectors(target.selector).filter((selector) => {
      const key = selectorKey(selector);
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    }),
  );
}

export function isCleanupTargetPending(
  targets: readonly CleanupTarget[],
  candidate: Pick<CleanupTarget, "key" | "ancestryKeys" | "selector"> & {
    connectionId?: string;
  },
): boolean {
  const candidateKeys = asSelectors(candidate.selector).map(selectorKey);
  const candidateConnectionId = candidate.connectionId;
  return targets.some((target) => {
    if (
      candidateConnectionId !== undefined &&
      target.connectionId !== candidateConnectionId
    ) {
      return false;
    }
    if (target.key === candidate.key) return true;
    if (candidateKeys.some((key) => selectorKeys(target).includes(key))) {
      return true;
    }
    if (candidateConnectionId === undefined || !isRangeTarget(target)) {
      return false;
    }
    return (
      candidate.ancestryKeys.includes(target.key) &&
      candidate.ancestryKeys.at(-1) !== target.key
    );
  });
}
