import {
  panoramaAccountPath,
  panoramaGlobalPath,
  panoramaRegionPath,
  panoramaRegionPublicPath,
  panoramaVPCPath,
} from "./route";
import type { CleanupTarget } from "./cleanupSelection";

export function cleanupTargetCanvasPath(
  target: CleanupTarget,
): string | undefined {
  if (target.kind === "region") return panoramaAccountPath();

  const regionID = target.locationContext?.region?.native_id?.trim();
  if (target.kind === "vpc") {
    return regionID ? panoramaRegionPath(regionID) : undefined;
  }

  const vpcID = target.locationContext?.vpc?.native_id?.trim();
  if (regionID && vpcID) return panoramaVPCPath(regionID, vpcID);

  const scopeKey = target.locationContext?.scope?.key;
  if (scopeKey === "account-global") return panoramaGlobalPath();
  if (regionID) return panoramaRegionPublicPath(regionID);
  return undefined;
}

export function cleanupTargetContainsKey(
  target: CleanupTarget | null | undefined,
  key: string | undefined,
): boolean {
  if (!target || !key) return false;
  if (target.key === key) return true;
  if (!key.startsWith("asset:")) return false;

  const assetID = key.slice("asset:".length);
  const selectors = Array.isArray(target.selector)
    ? target.selector
    : [target.selector];
  return selectors.some(
    (selector) => selector.kind === "asset" && selector.asset_id === assetID,
  );
}
