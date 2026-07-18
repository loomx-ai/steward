import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { CloudProviderIcon } from "./CloudProviderIcon";

describe("CloudProviderIcon", () => {
  it.each([
    ["alicloud", undefined, "alicloud"],
    ["aws", undefined, "aws"],
    [undefined, "https://ecs.console.aliyun.com/server", "alicloud"],
    [undefined, "https://console.aws.amazon.com/ec2/home", "aws"],
  ])(
    "renders the matching cloud logo for provider %s and URL %s",
    (provider, consoleURL, expected) => {
      const { container } = render(
        <CloudProviderIcon provider={provider} consoleURL={consoleURL} />,
      );

      expect(
        container.querySelector(`[data-cloud-provider="${expected}"]`),
      ).toBeInTheDocument();
    },
  );

  it("falls back to a generic cloud for an unknown provider", () => {
    const { container } = render(
      <CloudProviderIcon
        provider="private-cloud"
        consoleURL="https://console.example.com"
      />,
    );

    expect(
      container.querySelector('[data-cloud-provider="unknown"]'),
    ).toBeInTheDocument();
  });
});
