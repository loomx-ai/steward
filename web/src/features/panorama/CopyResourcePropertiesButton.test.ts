import { expect, it } from "vitest";
import { resourcePropertiesClipboardText } from "./CopyResourcePropertiesButton";

it("formats all visible resource properties for one-click copying", () => {
  expect(
    resourcePropertiesClipboardText([
      { path: "vpcId", label: "所属专有网络", value: "vpc-a" },
      {
        path: "configuration.RouteEntrys",
        label: "路由条目",
        value: { RouteEntry: [{ DestinationCidrBlock: "10.0.0.0/8" }] },
      },
    ]),
  ).toBe(
    [
      "所属专有网络: vpc-a",
      "路由条目:",
      "  {",
      '    "RouteEntry": [',
      "      {",
      '        "DestinationCidrBlock": "10.0.0.0/8"',
      "      }",
      "    ]",
      "  }",
    ].join("\n"),
  );
});
