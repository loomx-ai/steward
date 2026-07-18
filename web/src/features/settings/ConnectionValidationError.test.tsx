import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { APIRequestError } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import { ConnectionValidationError } from "./ConnectionValidationError";

const writeText = vi.fn();

function validationError(
  providerCode = "InvalidAccessKeyId.Inactive",
  providerMessage = "The Access Key ID is inactive.",
  category = "invalid_request",
) {
  return new APIRequestError(
    "cloud provider rejected the credential",
    "credential_validation_failed",
    {
      category,
      provider_code: providerCode,
      provider_message: providerMessage,
      provider_request_id: "provider-request",
    },
    "application-request",
  );
}

function renderValidationError(error: unknown) {
  render(
    <LocaleProvider>
      <Alert variant="destructive">
        <ConnectionValidationError error={error} />
      </Alert>
    </LocaleProvider>,
  );
}

beforeEach(() => {
  localStorage.setItem("steward.locale", "zh-CN");
  writeText.mockReset().mockResolvedValue(undefined);
});

it("shows localized inactive-key guidance while error details stay collapsed", () => {
  renderValidationError(validationError());

  const alert = screen.getByRole("alert");
  expect(alert).toHaveTextContent("凭证验证失败");
  expect(alert).toHaveTextContent(
    "Access Key ID 已停用，请启用或更换后重新验证。",
  );
  expect(alert).not.toHaveTextContent("InvalidAccessKeyId.Inactive");
  expect(alert).not.toHaveTextContent("provider-request");
  expect(alert).not.toHaveTextContent("application-request");
  expect(alert).not.toHaveTextContent("The Access Key ID is inactive.");
});

it("reveals aligned error details in diagnostic order and copies request IDs", async () => {
  const user = userEvent.setup();
  vi.spyOn(navigator.clipboard, "writeText").mockImplementation(writeText);
  renderValidationError(validationError());

  await user.click(screen.getByRole("button", { name: "查看错误详情" }));

  const alert = screen.getByRole("alert");
  expect(alert).toHaveTextContent("错误代码");
  expect(alert).toHaveTextContent("InvalidAccessKeyId.Inactive");
  expect(alert).toHaveTextContent("错误内容");
  expect(alert).toHaveTextContent("The Access Key ID is inactive.");
  expect(alert).toHaveTextContent("云厂商请求 ID");
  expect(alert).toHaveTextContent("provider-request");
  expect(alert).toHaveTextContent("应用请求 ID");
  expect(alert).toHaveTextContent("application-request");

  const details = alert.querySelector("dl");
  expect(details).not.toBeNull();
  expect(details).toHaveClass("grid", "grid-cols-[max-content_minmax(0,1fr)]");
  expect(details).not.toHaveClass("border-t");
  const detailCells = Array.from(details!.children);
  expect(detailCells.map((cell) => [cell.tagName, cell.textContent])).toEqual([
    ["DT", "错误代码"],
    ["DD", "InvalidAccessKeyId.Inactive"],
    ["DT", "错误内容"],
    ["DD", "The Access Key ID is inactive."],
    ["DT", "云厂商请求 ID"],
    ["DD", "provider-request"],
    ["DT", "应用请求 ID"],
    ["DD", "application-request"],
  ]);
  for (let index = 0; index < detailCells.length; index += 2) {
    expect(detailCells[index]).toHaveClass("self-start", "leading-5");
    expect(detailCells[index + 1]).toHaveClass("self-start", "leading-5");
  }

  await user.click(
    within(alert).getByRole("button", { name: "复制云厂商请求 ID" }),
  );
  expect(writeText).toHaveBeenCalledWith("provider-request");
});

it.each(["permission_denied", "throttled", "provider_failure"])(
  "uses the raw provider message for an unknown provider error categorized as %s",
  (category) => {
    renderValidationError(
      validationError(
        "VendorSpecificCredentialError",
        "The cloud account rejected this credential.",
        category,
      ),
    );

    expect(screen.getByRole("alert")).toHaveTextContent(
      "The cloud account rejected this credential.",
    );
  },
);

it("offers error details when the provider only returns a message", () => {
  renderValidationError(
    new APIRequestError(
      "cloud provider rejected the credential",
      "credential_validation_failed",
      { provider_message: "The cloud account rejected this credential." },
    ),
  );

  expect(screen.getByRole("button", { name: "查看错误详情" })).toBeVisible();
});

it("translates a typed local credential validation error", () => {
  renderValidationError(
    new APIRequestError(
      "The cloud credential has expired.",
      "credential_expired",
      {},
      "application-request",
    ),
  );

  const alert = screen.getByRole("alert");
  expect(alert).toHaveTextContent("凭证已过期，请替换凭证后重新验证。");
  expect(alert).not.toHaveTextContent("The cloud credential has expired.");
});
