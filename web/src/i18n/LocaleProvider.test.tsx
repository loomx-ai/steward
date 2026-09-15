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
    ["unresolved_cleanup_dependency", "清理依赖尚未核验"],
    ["elastic_san_group_delete", "保留资源可能继续计费"],
    ["elastic_san_volume_soft_delete", "需要单独清理"],
    ["elastic_san_volume_delete", "数据将被永久移除"],
    ["elastic_san_volume_force_delete", "可能中断工作负载"],
  ])("localizes Elastic SAN deletion consequence %s", (code, expected) => {
    localStorage.setItem(localePreferenceKey, "zh-CN");
    render(
      <LocaleProvider>
        <CodeProbe code={code} fallback="untranslated" />
      </LocaleProvider>,
    );
    expect(screen.getByTestId("code-message")).toHaveTextContent(expected);
  });

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

for (const locale of ["en-US", "zh-CN"] as const) {
  it(`explains the Synapse workspace cascade and retained lake in ${locale}`, () => {
    localStorage.setItem(localePreferenceKey, locale);
    render(
      <LocaleProvider>
        <ErrorProbe
          error={{ code: "synapse_workspace_delete", message: "fallback" }}
        />
      </LocaleProvider>,
    );
    const text = screen.getByTestId("error").textContent;
    expect(text).not.toContain(locale === "zh-CN" ? "永久" : "permanently");
    expect(text).toContain(
      locale === "zh-CN"
        ? "此操作不会清除这些备份"
        : "this operation does not purge them",
    );
    expect(text).toContain(
      locale === "zh-CN" ? "移除 SQL 池" : "removes its SQL pools",
    );
    expect(text).toContain(
      locale === "zh-CN"
        ? "Data Lake 存储将保留"
        : "Data Lake storage is retained",
    );
  });
}

for (const locale of ["en-US", "zh-CN"] as const) {
  it(`explains SQL pool-only deletion in ${locale}`, () => {
    localStorage.setItem(localePreferenceKey, locale);
    render(
      <LocaleProvider>
        <ErrorProbe
          error={{ code: "synapse_sql_delete", message: "fallback" }}
        />
      </LocaleProvider>,
    );
    const text = screen.getByTestId("error").textContent;
    expect(text).toContain(
      locale === "zh-CN"
        ? "工作区和其他池将保留"
        : "workspace and other pools are retained",
    );
    expect(text).toContain(
      locale === "zh-CN"
        ? "此操作不会清除这些备份"
        : "this operation does not purge them",
    );
  });
}

for (const locale of ["en-US", "zh-CN"] as const) {
  it(`explains restore-point-only deletion in ${locale}`, () => {
    localStorage.setItem(localePreferenceKey, locale);
    render(
      <LocaleProvider>
        <ErrorProbe
          error={{ code: "synapse_restore_point_delete", message: "fallback" }}
        />
      </LocaleProvider>,
    );
    const text = screen.getByTestId("error").textContent;
    expect(text).toContain(
      locale === "zh-CN"
        ? "移除对应的恢复选项"
        : "removes that recovery option",
    );
    expect(text).toContain(
      locale === "zh-CN"
        ? "SQL 池、工作区和其他备份将保留"
        : "SQL pool, workspace and other backups are retained",
    );
  });
}

for (const locale of ["en-US", "zh-CN"] as const) {
  it(`explains NetApp volume deletion and retained backups in ${locale}`, () => {
    localStorage.setItem(localePreferenceKey, locale);
    render(
      <LocaleProvider>
        <ErrorProbe
          error={{ code: "netapp_volume_delete", message: "fallback" }}
        />
      </LocaleProvider>,
    );
    const text = screen.getByTestId("error").textContent;
    expect(text).toContain(
      locale === "zh-CN"
        ? "从所有主机卸载此卷"
        : "unmount the volume from all hosts",
    );
    expect(text).toContain(
      locale === "zh-CN"
        ? "快照、子卷和配额规则"
        : "snapshots, subvolumes and quota rules",
    );
    expect(text).toContain(
      locale === "zh-CN" ? "备份保管库内的备份" : "Backups in backup vaults",
    );
  });
}

for (const locale of ["en-US", "zh-CN"] as const) {
  for (const code of ["netapp_snapshot_delete", "netapp_backup_delete"]) {
    it(`explains independent recovery deletion ${code} in ${locale}`, () => {
      localStorage.setItem(localePreferenceKey, locale);
      render(
        <LocaleProvider>
          <ErrorProbe error={{ code, message: "fallback" }} />
        </LocaleProvider>,
      );
      const text = screen.getByTestId("error").textContent;
      expect(text).toContain(
        locale === "zh-CN"
          ? "永久移除对应的恢复点"
          : "permanently removes that recovery point",
      );
      if (code === "netapp_snapshot_delete")
        expect(text).toContain(
          locale === "zh-CN" ? "卷、其他快照" : "The volume, other snapshots",
        );
      else
        expect(text).toContain(
          locale === "zh-CN"
            ? "后续增量备份的参考点"
            : "reference point for future incremental backups",
        );
    });
  }
}

for (const locale of ["en-US", "zh-CN"] as const) {
  for (const code of ["netapp_subvolume_delete", "netapp_quota_delete"]) {
    it(`explains native child deletion ${code} in ${locale}`, () => {
      localStorage.setItem(localePreferenceKey, locale);
      render(
        <LocaleProvider>
          <ErrorProbe error={{ code, message: "fallback" }} />
        </LocaleProvider>,
      );
      const value = screen.getByTestId("error").textContent;
      if (code === "netapp_subvolume_delete") {
        expect(value).toContain(
          locale === "zh-CN" ? "移除其数据" : "removes its data",
        );
        expect(value).toContain(
          locale === "zh-CN" ? "可能中断" : "can interrupt",
        );
      } else {
        expect(value).toContain(
          locale === "zh-CN"
            ? "其他适用的配额规则"
            : "Other applicable quota rules",
        );
        expect(value).toContain(
          locale === "zh-CN"
            ? "文件和父卷将保留"
            : "Files and the parent volume are retained",
        );
      }
    });
  }
}

for (const locale of ["en-US", "zh-CN"] as const) {
  it(`explains ordered NetApp capacity pool deletion in ${locale}`, () => {
    localStorage.setItem(localePreferenceKey, locale);
    render(
      <LocaleProvider>
        <ErrorProbe
          error={{ code: "netapp_pool_delete", message: "fallback" }}
        />
      </LocaleProvider>,
    );
    const text = screen.getByTestId("error").textContent;
    expect(text).toContain(
      locale === "zh-CN"
        ? "先删除已审查的卷"
        : "first deletes its reviewed volumes",
    );
    expect(text).toContain(
      locale === "zh-CN"
        ? "停止应用并卸载这些卷"
        : "Stop applications and unmount these volumes",
    );
    expect(text).toContain(
      locale === "zh-CN"
        ? "备份保管库内的备份将保留"
        : "backup-vault backups are retained",
    );
  });
}
