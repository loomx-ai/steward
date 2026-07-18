const naturalRegionIDCollator = new Intl.Collator("en", {
  numeric: true,
  sensitivity: "base",
});

export function compareRegionIDs(left: string, right: string): number {
  const leftGroup = regionGeographyGroup(left);
  const rightGroup = regionGeographyGroup(right);
  if (leftGroup !== rightGroup) return leftGroup - rightGroup;

  const normalizedLeft = left.trim().toLocaleLowerCase();
  const normalizedRight = right.trim().toLocaleLowerCase();
  return (
    naturalRegionIDCollator.compare(normalizedLeft, normalizedRight) ||
    left.localeCompare(right)
  );
}

function regionGeographyGroup(regionID: string): number {
  const normalized = regionID.trim().toLocaleLowerCase();
  if (normalized.startsWith("cn-")) return 0;
  if (normalized.startsWith("ap-")) return 1;
  if (
    hasRegionPrefix(
      normalized,
      "eu-",
      "eusc-",
      "us-",
      "ca-",
      "na-",
      "sa-",
      "mx-",
      "br-",
    )
  ) {
    return 2;
  }
  if (hasRegionPrefix(normalized, "me-", "il-", "ae-")) return 3;
  return 4;
}

function hasRegionPrefix(regionID: string, ...prefixes: string[]): boolean {
  return prefixes.some((prefix) => regionID.startsWith(prefix));
}
