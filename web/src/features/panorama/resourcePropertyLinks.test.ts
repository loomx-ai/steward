import { expect, it } from "vitest";
import { resourcePropertyReferenceValues } from "./resourcePropertyLinks";

it("collects only resource references from nested properties", () => {
  expect(
    resourcePropertyReferenceValues(
      [
        {
          path: "configuration.VpcId",
          label: "所属专有网络",
          value: "vpc-a",
        },
        {
          path: "configuration.VSwitchIds",
          label: "所属交换机",
          value: { VSwitchId: ["vsw-a", "vsw-b", ""] },
        },
        {
          path: "configuration.RouteEntrys",
          label: "路由条目",
          value: {
            RouteEntry: [
              {
                RouteTableId: "vtb-a",
                DestinationCidrBlock: "10.0.0.0/8",
                NextHopType: "local",
              },
            ],
          },
        },
        {
          path: "configuration.Description",
          label: "描述",
          value: "vpc-not-a-reference",
        },
      ],
      "vtb-a",
    ),
  ).toEqual(["vpc-a", "vsw-a", "vsw-b"]);
});
