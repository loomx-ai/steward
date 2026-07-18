import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { APIRequestError } from "@/api/client";
import { LocaleProvider, useLocale } from "./LocaleProvider";
import { localePreferenceKey, type Locale } from "./locales";

function FormatterProbe({ value }: { value: Date }) {
  const { formatDate, formatTime } = useLocale();
  return (
    <>
      <output data-testid="date-time">{formatDate(value)}</output>
      <output data-testid="time">{formatTime(value)}</output>
    </>
  );
}

function ErrorProbe({ error }: { error: unknown }) {
  const { formatError } = useLocale();
  return <output data-testid="error">{formatError(error)}</output>;
}

function LabelProbe({ values }: { values: string[] }) {
  const { label } = useLocale();
  return <output data-testid="labels">{values.map(label).join(" · ")}</output>;
}

function CodeProbe({
  code = "DeletionCheckTimeout",
  fallback = "fallback",
  details = {},
}: {
  code?: string;
  fallback?: string;
  details?: Record<string, string | number>;
}) {
  const { messageForCode } = useLocale();
  return (
    <output data-testid="code-message">
      {messageForCode(code, fallback, details)}
    </output>
  );
}

describe("LocaleProvider date formatting", () => {
  afterEach(() => localStorage.removeItem(localePreferenceKey));

  it.each(["en-US", "zh-CN"] satisfies Locale[])(
    "uses the same numeric date and time format for %s",
    (locale) => {
      localStorage.setItem(localePreferenceKey, locale);
      render(
        <LocaleProvider>
          <FormatterProbe value={new Date(2026, 6, 23, 11, 57, 48)} />
        </LocaleProvider>,
      );

      expect(screen.getByTestId("date-time")).toHaveTextContent(
        "2026-07-23 11:57:48",
      );
      expect(screen.getByTestId("time")).toHaveTextContent("11:57:48");
    },
  );
});

describe("LocaleProvider error formatting", () => {
  afterEach(() => localStorage.removeItem(localePreferenceKey));

  it.each([
    ["en-US", "An internal server error occurred."],
    ["zh-CN", "服务发生内部错误。"],
  ] satisfies [Locale, string][])(
    "hides internal error details in %s",
    (locale, expected) => {
      localStorage.setItem(localePreferenceKey, locale);
      render(
        <LocaleProvider>
          <ErrorProbe
            error={
              new APIRequestError(
                "table scan_tasks has no column named resource_count",
                "internal_error",
                undefined,
                "server-request",
              )
            }
          />
        </LocaleProvider>,
      );

      expect(screen.getByTestId("error")).toHaveTextContent(expected);
      expect(screen.getByTestId("error")).toHaveTextContent("server-request");
      expect(screen.getByTestId("error")).not.toHaveTextContent("scan_tasks");
      expect(screen.getByTestId("error")).not.toHaveTextContent(
        "resource_count",
      );
    },
  );

  it("shows safe provider diagnostics and distinguishes provider and application request IDs", () => {
    localStorage.setItem(localePreferenceKey, "en-US");
    render(
      <LocaleProvider>
        <ErrorProbe
          error={
            new APIRequestError(
              "cloud provider rejected the credential",
              "credential_validation_failed",
              {
                category: "invalid_request",
                provider_code: "InvalidAccessKeyId.NotFound",
                provider_message: "The specified access key is not found.",
                provider_request_id: "provider-request",
              },
              "server-request",
            )
          }
        />
      </LocaleProvider>,
    );

    expect(screen.getByTestId("error")).toHaveTextContent(
      "The cloud provider rejected this credential.",
    );
    expect(screen.getByTestId("error")).toHaveTextContent(
      "Provider error: The specified access key is not found. (InvalidAccessKeyId.NotFound)",
    );
    expect(screen.getByTestId("error")).toHaveTextContent(
      "Provider request ID: provider-request",
    );
    expect(screen.getByTestId("error")).toHaveTextContent(
      "Request ID: server-request",
    );
  });

  it.each([
    ["en-US", "Resource deletion could not be confirmed within 30 seconds."],
    ["zh-CN", "30 秒内未能确认资源已删除。"],
  ] satisfies [Locale, string][])(
    "uses the reported deletion timeout in %s",
    (locale, expected) => {
      localStorage.setItem(localePreferenceKey, locale);
      render(
        <LocaleProvider>
          <CodeProbe details={{ timeout_seconds: 30 }} />
        </LocaleProvider>,
      );

      expect(screen.getByTestId("code-message")).toHaveTextContent(expected);
    },
  );

  it.each([
    [
      "InternalServerError",
      "an internal error has occurred. Please retry.",
      {},
      "云服务内部错误，请稍后重试。",
    ],
    [
      "CleanupUnsupported.PrimaryNetworkInterface",
      "primary network interfaces are deleted with their owning instance and cannot be deleted directly",
      {},
      "主网卡会随所属实例删除，不能直接删除，已跳过。",
    ],
    [
      "CleanupSkipped.RetainedDependency",
      "cleanup dependency step-a was retained; retaining dependent resource",
      { dependency_step_id: "step-a" },
      "依赖的清理步骤 step-a 已保留，因此当前资源也已保留。",
    ],
    [
      "CleanupUnsupported.BackupPlanBoundVault",
      "backup vaults bound to backup plans cannot be deleted directly",
      {},
      "已绑定备份计划的备份库不能直接删除，已跳过。",
    ],
  ] satisfies [string, string, Record<string, string | number>, string][])(
    "localizes cleanup provider error %s",
    (code, fallback, details, expected) => {
      localStorage.setItem(localePreferenceKey, "zh-CN");
      render(
        <LocaleProvider>
          <CodeProbe code={code} fallback={fallback} details={details} />
        </LocaleProvider>,
      );

      expect(screen.getByTestId("code-message")).toHaveTextContent(expected);
    },
  );
});

describe("LocaleProvider domain labels", () => {
  afterEach(() => localStorage.removeItem(localePreferenceKey));

  it.each([
    [
      "en-US",
      "Preparing · Invoking · Waiting · Verifying result · Reconciling",
    ],
    ["zh-CN", "准备中 · 调用中 · 等待中 · 结果确认中 · 确认中"],
  ] satisfies [Locale, string][])(
    "localizes every active cleanup action status for %s",
    (locale, expected) => {
      localStorage.setItem(localePreferenceKey, locale);
      render(
        <LocaleProvider>
          <LabelProbe
            values={[
              "intent_persisted",
              "invoking",
              "waiting",
              "reading_back",
              "reconciling",
            ]}
          />
        </LocaleProvider>,
      );

      expect(screen.getByTestId("labels")).toHaveTextContent(expected);
    },
  );
});
