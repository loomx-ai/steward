import { expect, it } from "vitest";
import { compareRegionIDs } from "./regionOrder";

it("orders regions by geography and naturally within each group", () => {
  const regionIDs = [
    "af-south-1",
    "me-central-1",
    "us-east-1",
    "na-south-1",
    "ap-southeast-10",
    "cn-hongkong",
    "moon-1",
    "il-central-1",
    "sa-east-1",
    "ap-northeast-1",
    "cn-beijing",
    "eu-central-1",
    "ap-southeast-2",
  ];

  expect(regionIDs.sort(compareRegionIDs)).toEqual([
    "cn-beijing",
    "cn-hongkong",
    "ap-northeast-1",
    "ap-southeast-2",
    "ap-southeast-10",
    "eu-central-1",
    "na-south-1",
    "sa-east-1",
    "us-east-1",
    "il-central-1",
    "me-central-1",
    "af-south-1",
    "moon-1",
  ]);
});
