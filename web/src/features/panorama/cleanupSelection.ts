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

type Counts = Map<string, number>;
type Slots = Map<string, Set<number>>;

function count(map: Counts, key: string, delta: number) {
  const next = (map.get(key) ?? 0) + delta;
  if (next > 0) map.set(key, next);
  else map.delete(key);
}

function slot(map: Slots, key: string, index: number, delta: number) {
  let indexes = map.get(key);
  if (delta > 0) {
    if (!indexes) map.set(key, (indexes = new Set()));
    indexes.add(index);
    return;
  }
  indexes?.delete(index);
  if (indexes?.size === 0) map.delete(key);
}

// Duplicate-insensitive, order-insensitive set equality plus the original
// length check, so equal signatures mean the selector sets match.
function selectorSetSignature(keys: readonly string[]): string {
  return `${keys.length}\u0000${[...new Set(keys)].sort().join("\u0000")}`;
}

export function addCleanupTargets(
  current: readonly CleanupTarget[],
  incoming: readonly CleanupTarget[],
): CleanupMergeResult {
  // Live targets keep their list position; removed ones become undefined.
  // Every index key is scoped by connection, mirroring sameConnection().
  const targets: (CleanupTarget | undefined)[] = current.map(cloneTarget);
  const keys: Counts = new Map();
  const rangeKeys: Counts = new Map();
  const selectors: Counts = new Map();
  const batchSignatures: Counts = new Map();
  const batchAssets: Counts = new Map();
  const batchesByKey: Slots = new Map();
  const descendantsOf: Slots = new Map();
  const resourcesByAsset: Slots = new Map();
  let mergedCount = 0;
  let coveredCount = 0;
  let addedCount = 0;

  const scoped = (target: CleanupTarget, key: string) =>
    `${target.connectionId}\u0000${key}`;
  const index = (at: number, delta: number) => {
    const target = targets[at];
    if (!target) return;
    count(keys, scoped(target, target.key), delta);
    if (isRangeTarget(target)) {
      count(rangeKeys, scoped(target, target.key), delta);
    }
    const own = selectorKeys(target);
    for (const key of own) count(selectors, scoped(target, key), delta);
    for (const key of target.ancestryKeys.slice(0, -1)) {
      slot(descendantsOf, scoped(target, key), at, delta);
    }
    if (target.kind === "resource_batch") {
      count(batchSignatures, scoped(target, selectorSetSignature(own)), delta);
      for (const id of assetIDs(target)) {
        count(batchAssets, scoped(target, id), delta);
      }
      slot(batchesByKey, scoped(target, target.key), at, delta);
    }
    if (target.kind === "resource") {
      for (const id of assetIDs(target)) {
        slot(resourcesByAsset, scoped(target, id), at, delta);
      }
    }
  };
  // Removes every target the incoming one covers and returns how many.
  const removeCovered = (cover: CleanupTarget) => {
    const covered = new Set<number>();
    if (isRangeTarget(cover)) {
      for (const at of descendantsOf.get(scoped(cover, cover.key)) ?? []) {
        covered.add(at);
      }
    }
    if (cover.kind === "resource_batch") {
      for (const id of assetIDs(cover)) {
        for (const at of resourcesByAsset.get(scoped(cover, id)) ?? []) {
          covered.add(at);
        }
      }
    }
    for (const at of covered) {
      index(at, -1);
      targets[at] = undefined;
    }
    return covered.size;
  };
  const existingCovers = (target: CleanupTarget) => {
    if (keys.has(scoped(target, target.key))) return true;
    if (
      target.ancestryKeys
        .slice(0, -1)
        .some((key) => rangeKeys.has(scoped(target, key)))
    ) {
      return true;
    }
    const own = selectorKeys(target);
    if (target.kind === "resource_batch") {
      return batchSignatures.has(scoped(target, selectorSetSignature(own)));
    }
    return own.some((key) => selectors.has(scoped(target, key)));
  };
  targets.forEach((_, at) => index(at, 1));

  for (const source of incoming) {
    const normalized = normalizeIncomingBatch(source);
    const matchingBatches =
      normalized?.kind === "resource_batch"
        ? batchesByKey.get(scoped(normalized, normalized.key))
        : undefined;
    if (normalized && matchingBatches) {
      const matchingBatchIndex = Math.min(...matchingBatches);
      const existing = targets[matchingBatchIndex];
      if (existing) {
        const merged = mergeBatchTargets(existing, normalized);
        if (merged) {
          index(matchingBatchIndex, -1);
          targets[matchingBatchIndex] = merged;
          index(matchingBatchIndex, 1);
          mergedCount += removeCovered(merged);
        }
      }
      continue;
    }
    if (!normalized || existingCovers(normalized)) {
      coveredCount += 1;
      continue;
    }
    const candidate =
      normalized.kind === "resource_batch"
        ? withBatchAssets(
            normalized,
            assetIDs(normalized).filter(
              (id) => !batchAssets.has(scoped(normalized, id)),
            ),
          )
        : normalized;
    if (!candidate) {
      coveredCount += 1;
      continue;
    }
    mergedCount += removeCovered(candidate);
    targets.push(cloneTarget(candidate));
    index(targets.length - 1, 1);
    addedCount += 1;
  }

  return {
    targets: targets.flatMap((target) => (target ? [cloneTarget(target)] : [])),
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

type PendingCandidate = Pick<
  CleanupTarget,
  "key" | "ancestryKeys" | "selector"
> & {
  connectionId?: string;
};

interface PendingIndex {
  keys: Set<string>;
  selectorKeys: Set<string>;
  rangeKeys: Set<string>;
}

// Indexes the pending targets once so many canvas nodes can be checked without
// scanning every target and selector for each node.
export function cleanupPendingMatcher(
  targets: readonly CleanupTarget[],
): (candidate: PendingCandidate) => boolean {
  const all: PendingIndex = {
    keys: new Set(),
    selectorKeys: new Set(),
    rangeKeys: new Set(),
  };
  const byConnection = new Map<string, PendingIndex>();
  for (const target of targets) {
    let index = byConnection.get(target.connectionId);
    if (!index) {
      index = {
        keys: new Set(),
        selectorKeys: new Set(),
        rangeKeys: new Set(),
      };
      byConnection.set(target.connectionId, index);
    }
    index.keys.add(target.key);
    all.keys.add(target.key);
    for (const key of selectorKeys(target)) {
      index.selectorKeys.add(key);
      all.selectorKeys.add(key);
    }
    if (isRangeTarget(target)) index.rangeKeys.add(target.key);
  }
  return (candidate) => {
    const index =
      candidate.connectionId === undefined
        ? all
        : byConnection.get(candidate.connectionId);
    if (!index) return false;
    if (index.keys.has(candidate.key)) return true;
    const candidateSelectors = Array.isArray(candidate.selector)
      ? candidate.selector
      : [candidate.selector];
    if (
      candidateSelectors.some((selector) =>
        index.selectorKeys.has(selectorKey(selector)),
      )
    ) {
      return true;
    }
    if (candidate.connectionId === undefined) return false;
    const last = candidate.ancestryKeys.at(-1);
    return candidate.ancestryKeys.some(
      (key) => key !== last && index.rangeKeys.has(key),
    );
  };
}

export function isCleanupTargetPending(
  targets: readonly CleanupTarget[],
  candidate: PendingCandidate,
): boolean {
  return cleanupPendingMatcher(targets)(candidate);
}
