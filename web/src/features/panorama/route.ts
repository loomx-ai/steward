export type PanoramaRoute =
  | { kind: "account"; focusKey: ""; pathname: string }
  | { kind: "account-global"; focusKey: "account-global"; pathname: string }
  | { kind: "region"; regionId: string; focusKey: string; pathname: string }
  | {
      kind: "region-public";
      regionId: string;
      focusKey: string;
      pathname: string;
    }
  | {
      kind: "vpc";
      regionId: string;
      vpcId: string;
      focusKey: string;
      pathname: string;
    };

export function parsePanoramaRoute(
  pathname: string,
): PanoramaRoute | undefined {
  if (pathname === panoramaAccountPath()) {
    return { kind: "account", focusKey: "", pathname };
  }
  if (pathname === panoramaGlobalPath()) {
    return { kind: "account-global", focusKey: "account-global", pathname };
  }

  const vpcMatch = pathname.match(
    /^\/panorama\/regions\/([^/]+)\/vpcs\/([^/]+)$/,
  );
  if (vpcMatch) {
    const regionId = decodePathSegment(vpcMatch[1]);
    const vpcId = decodePathSegment(vpcMatch[2]);
    if (!regionId || !vpcId) return undefined;
    return {
      kind: "vpc",
      regionId,
      vpcId,
      focusKey: `vpc:${encodeFocusSegment(regionId)}:${encodeFocusSegment(vpcId)}`,
      pathname,
    };
  }

  const publicMatch = pathname.match(/^\/panorama\/regions\/([^/]+)\/public$/);
  if (publicMatch) {
    const regionId = decodePathSegment(publicMatch[1]);
    if (!regionId) return undefined;
    return {
      kind: "region-public",
      regionId,
      focusKey: `region-public:${encodeFocusSegment(regionId)}`,
      pathname,
    };
  }

  const regionMatch = pathname.match(/^\/panorama\/regions\/([^/]+)$/);
  if (regionMatch) {
    const regionId = decodePathSegment(regionMatch[1]);
    if (!regionId) return undefined;
    return {
      kind: "region",
      regionId,
      focusKey: `region:${encodeFocusSegment(regionId)}`,
      pathname,
    };
  }

  return undefined;
}

export function panoramaAccountPath() {
  return "/panorama";
}

export function panoramaResourcePath(resourceID: string) {
  const query = new URLSearchParams({ resource: resourceID });
  return `${panoramaAccountPath()}?${query.toString()}`;
}

export function panoramaGlobalPath() {
  return "/panorama/global";
}

export function panoramaRegionPath(regionId: string) {
  return `/panorama/regions/${encodeURIComponent(regionId)}`;
}

export function panoramaRegionPublicPath(regionId: string) {
  return `${panoramaRegionPath(regionId)}/public`;
}

export function panoramaVPCPath(regionId: string, vpcId: string) {
  return `${panoramaRegionPath(regionId)}/vpcs/${encodeURIComponent(vpcId)}`;
}

function decodePathSegment(value: string | undefined) {
  if (!value) return undefined;
  try {
    return decodeURIComponent(value) || undefined;
  } catch {
    return undefined;
  }
}

function encodeFocusSegment(value: string) {
  const bytes = new TextEncoder().encode(value);
  let binary = "";
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return btoa(binary)
    .replaceAll("+", "-")
    .replaceAll("/", "_")
    .replace(/=+$/, "");
}
