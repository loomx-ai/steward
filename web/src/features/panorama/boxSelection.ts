export type CanvasMode = "select" | "pan";

export interface BoxSelectionState {
  candidateKeys: string[];
}

export interface BoxSelectionTarget {
  key: string;
  kind: "resource" | "resource_batch";
  memberKeys?: readonly string[];
}

function uniqueKeys(keys: readonly string[]): string[] {
  return [...new Set(keys)];
}

export function completeBoxSelection(
  currentKeys: readonly string[],
  selectedKeys: readonly string[],
  append: boolean,
): string[] {
  return uniqueKeys(append ? [...currentKeys, ...selectedKeys] : selectedKeys);
}

export function clearBoxSelection(
  _current: BoxSelectionState,
): BoxSelectionState {
  return { candidateKeys: [] };
}

export function prioritizeBoxSelectionTargets(
  selectedTargets: readonly BoxSelectionTarget[],
): BoxSelectionTarget[] {
  const selectedBatchMembers = new Set(
    selectedTargets
      .filter((target) => target.kind === "resource_batch")
      .flatMap((target) => target.memberKeys ?? []),
  );
  const seenKeys = new Set<string>();

  return selectedTargets.filter((target) => {
    if (seenKeys.has(target.key)) return false;
    seenKeys.add(target.key);
    return !(
      target.kind === "resource" && selectedBatchMembers.has(target.key)
    );
  });
}
