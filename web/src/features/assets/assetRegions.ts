import type { ConnectionRegion } from "@/api/types";

export function resolveAssetRegion(
  location: string | undefined,
  regions: ConnectionRegion[],
): ConnectionRegion | undefined {
  const value = assetConsoleRegionID(location);
  if (!value) return undefined;
  return regions.reduce<ConnectionRegion | undefined>((best, region) => {
    const regionID = region.region_id.trim();
    const matches =
      value === regionID || (regionID && value.startsWith(`${regionID}-`));
    if (!matches) return best;
    return !best || regionID.length > best.region_id.trim().length
      ? region
      : best;
  }, undefined);
}

export function assetRegionLabel(
  location: string | undefined,
  region: ConnectionRegion | undefined,
  globalLabel: string,
) {
  const value = location?.trim() ?? "";
  if (!value || value.toLowerCase() === "global") return globalLabel;
  if (!region) return value;
  const name = region.name.trim();
  if (name && name !== region.region_id.trim()) return name;
  return region.discovered_name?.trim() || name || value;
}

export function assetConsoleRegionID(location: string | undefined) {
  const value = location?.trim() ?? "";
  return value.toLowerCase() === "global" ? "" : value;
}
