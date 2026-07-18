import { describe, expect, it } from "vitest";
import {
  panoramaAccountPath,
  panoramaGlobalPath,
  panoramaRegionPath,
  panoramaRegionPublicPath,
  panoramaResourcePath,
  panoramaVPCPath,
  parsePanoramaRoute,
} from "./route";

describe("panorama routes", () => {
  it.each([
    ["/panorama", { kind: "account", focusKey: "" }],
    [
      "/panorama/global",
      { kind: "account-global", focusKey: "account-global" },
    ],
    [
      "/panorama/regions/cn-beijing",
      {
        kind: "region",
        regionId: "cn-beijing",
        focusKey: "region:Y24tYmVpamluZw",
      },
    ],
    [
      "/panorama/regions/cn-beijing/public",
      {
        kind: "region-public",
        regionId: "cn-beijing",
        focusKey: "region-public:Y24tYmVpamluZw",
      },
    ],
    [
      "/panorama/regions/cn-beijing/vpcs/vpc-22ep",
      {
        kind: "vpc",
        regionId: "cn-beijing",
        vpcId: "vpc-22ep",
        focusKey: "vpc:Y24tYmVpamluZw:dnBjLTIyZXA",
      },
    ],
  ] as const)("parses %s into its topology focus", (pathname, expected) => {
    expect(parsePanoramaRoute(pathname)).toEqual({
      ...expected,
      pathname,
    });
  });

  it("decodes one encoded path segment before creating the focus key", () => {
    expect(parsePanoramaRoute("/panorama/regions/%E5%8C%97%E4%BA%AC")).toEqual({
      kind: "region",
      regionId: "北京",
      focusKey: "region:5YyX5Lqs",
      pathname: "/panorama/regions/%E5%8C%97%E4%BA%AC",
    });
  });

  it("builds encoded semantic paths from native cloud IDs", () => {
    expect(panoramaAccountPath()).toBe("/panorama");
    expect(panoramaResourcePath("asset prod/1")).toBe(
      "/panorama?resource=asset+prod%2F1",
    );
    expect(panoramaGlobalPath()).toBe("/panorama/global");
    expect(panoramaRegionPath("cn beijing/1")).toBe(
      "/panorama/regions/cn%20beijing%2F1",
    );
    expect(panoramaRegionPublicPath("cn beijing/1")).toBe(
      "/panorama/regions/cn%20beijing%2F1/public",
    );
    expect(panoramaVPCPath("cn beijing/1", "vpc prod/1")).toBe(
      "/panorama/regions/cn%20beijing%2F1/vpcs/vpc%20prod%2F1",
    );
  });

  it.each([
    "/panorama/",
    "/panorama/unknown",
    "/panorama/regions",
    "/panorama/regions//public",
    "/panorama/regions/cn-beijing/vpcs",
    "/panorama/regions/%E0%A4%A",
  ])("rejects the malformed panorama path %s", (pathname) => {
    expect(parsePanoramaRoute(pathname)).toBeUndefined();
  });
});
