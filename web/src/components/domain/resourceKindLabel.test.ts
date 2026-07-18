import { describe, expect, it } from "vitest";
import {
  compareResourceKindOptionsByProduct,
  resourceProductName,
  resourceTypeName,
} from "./resourceKindLabel";

describe("resourceTypeName", () => {
  it.each([
    ["ACS::ACK::Cluster", "ACK 集群"],
    ["ACS::ALB::LoadBalancer", "ALB 实例"],
    ["ACS::ARMS::Prometheus", "Prometheus 实例"],
    ["ACS::CBWP::CommonBandwidthPackage", "共享带宽实例"],
    ["ACS::CR::Instance", "容器镜像实例"],
  ])(
    "renders the explicitly curated Chinese label for %s",
    (nativeType, curatedName) => {
      expect(
        resourceTypeName(
          nativeType,
          "Catalog fallback",
          { "zh-CN": curatedName },
          "zh-CN",
        ),
      ).toBe(curatedName);
    },
  );

  it("does not infer or compose a name from generic catalog metadata", () => {
    expect(
      resourceTypeName(
        "ACS::ECS::Instance",
        "Instance",
        { "zh-CN": "实例" },
        "zh-CN",
      ),
    ).toBe("实例");
  });

  it("keeps the supplied fallback unchanged when localized metadata is absent", () => {
    expect(
      resourceTypeName(
        "ACS::ECS::SecurityGroup",
        "ACS::ECS::SecurityGroup",
        undefined,
        "zh-CN",
      ),
    ).toBe("ACS::ECS::SecurityGroup");
  });
});

describe("resourceProductName", () => {
  it.each([
    ["ACS::ROS::Stack", "ROS"],
    ["ACS::ECS::Instance", "ECS"],
    ["AWS::EC2::Instance", "EC2"],
  ])("extracts the product segment from %s", (nativeType, product) => {
    expect(resourceProductName(nativeType)).toBe(product);
  });

  it("does not guess a product from a malformed native type", () => {
    expect(resourceProductName("custom-resource")).toBe("—");
    expect(resourceProductName(undefined)).toBe("—");
  });
});

describe("compareResourceKindOptionsByProduct", () => {
  it("sorts by product code and then by the localized resource type", () => {
    const options = [
      { label: "交换机", tag: "VPC" },
      { label: "安全组", tag: "ECS" },
      { label: "操作审计跟踪", tag: "ActionTrail" },
      { label: "自动快照策略", tag: "ECS" },
    ];

    expect(
      options
        .sort((left, right) =>
          compareResourceKindOptionsByProduct(left, right, "zh-CN"),
        )
        .map((option) => `${option.tag}:${option.label}`),
    ).toEqual([
      "ActionTrail:操作审计跟踪",
      "ECS:安全组",
      "ECS:自动快照策略",
      "VPC:交换机",
    ]);
  });
});
