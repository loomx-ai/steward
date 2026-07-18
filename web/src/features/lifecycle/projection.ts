import type { Asset, LifecycleBinding } from "../../api/types";

export type LifecycleGroupKey = "delegated" | "retained" | "unknown";

export interface LifecycleProjectionItem {
  asset: Pick<Asset, "id" | "name" | "capabilities">;
  binding: LifecycleBinding;
  expected: string;
  providerDefault?: string;
  possibleBillingResidual: boolean;
}

export interface LifecycleProjectionGroup {
  key: LifecycleGroupKey;
  items: LifecycleProjectionItem[];
}

const groupOrder: LifecycleGroupKey[] = ["delegated", "retained", "unknown"];

export function projectLifecycle(
  controllerID: string,
  assets: Array<Pick<Asset, "id" | "name" | "capabilities">>,
  bindings: LifecycleBinding[],
): { groups: LifecycleProjectionGroup[] } {
  const assetsByID = new Map(assets.map((asset) => [asset.id, asset]));
  const grouped = new Map<LifecycleGroupKey, LifecycleProjectionItem[]>();
  for (const binding of bindings) {
    if (binding.controller_asset_id !== controllerID) continue;
    const managed = assetsByID.get(binding.managed_asset_id);
    if (!managed) continue;
    const key = lifecycleGroup(binding);
    const evidence = binding.evidence ?? {};
    const item: LifecycleProjectionItem = {
      asset: managed,
      binding,
      expected: expectedImpact(binding),
      providerDefault:
        typeof evidence.provider_default === "string"
          ? evidence.provider_default
          : undefined,
      possibleBillingResidual: evidence.possible_billing_residual === true,
    };
    grouped.set(key, [...(grouped.get(key) ?? []), item]);
  }
  return {
    groups: groupOrder
      .filter((key) => (grouped.get(key)?.length ?? 0) > 0)
      .map((key) => ({
        key,
        items: [...(grouped.get(key) ?? [])].sort((left, right) =>
          left.asset.id.localeCompare(right.asset.id),
        ),
      })),
  };
}

export function canSelectForCleanup(
  assetID: string,
  bindings: LifecycleBinding[],
): boolean {
  return !bindings.some(
    (binding) =>
      binding.managed_asset_id === assetID &&
      binding.authority === "authoritative" &&
      binding.ownership === "exclusive" &&
      binding.cleanup_policy === "delegate" &&
      binding.direct_cleanup_allowed !== true &&
      binding.confidence >= 1,
  );
}

function lifecycleGroup(binding: LifecycleBinding): LifecycleGroupKey {
  if (
    binding.authority === "authoritative" &&
    binding.ownership === "exclusive" &&
    binding.cleanup_policy === "delegate"
  ) {
    return "delegated";
  }
  if (binding.cleanup_policy === "retain") return "retained";
  return "unknown";
}

function expectedImpact(binding: LifecycleBinding): string {
  if (lifecycleGroup(binding) === "delegated") return "delegated_delete";
  if (binding.cleanup_policy === "retain" && binding.ownership === "shared") {
    return "retain_shared";
  }
  if (binding.cleanup_policy === "retain") return "retain_explicit";
  return "unknown";
}
