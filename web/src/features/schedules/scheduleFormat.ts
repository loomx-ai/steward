import type {
  ScanSchedule,
  ScheduleFrequency,
  ScheduleRules,
  ScheduleRun,
  ScheduleScope,
} from "@/api/types";
import type { useLocale } from "@/i18n/LocaleProvider";

type Translate = ReturnType<typeof useLocale>["t"];

export const hourlyIntervals = [1, 2, 3, 4, 6, 8, 12];

export const defaultRules: ScheduleRules = {
  overlap: "skip",
  missed: "catch_up",
  retry_failed_targets: true,
  pause_after_failures: 3,
};

export function scheduleName(
  schedule: Pick<ScanSchedule, "name">,
  t: Translate,
) {
  return schedule.name || t("schedules.defaultName");
}

export function scheduleState(schedule: ScanSchedule) {
  if (schedule.enabled) return "active" as const;
  return schedule.pause_reason ? ("paused" as const) : ("off" as const);
}

export function pauseReason(schedule: ScanSchedule, t: Translate) {
  if (schedule.pause_reason === "connection_removed") {
    return t("schedules.pauseReason.connection_removed");
  }
  return t("schedules.pauseReason.consecutive_failures", {
    count: schedule.consecutive_failures,
  });
}

// weekdayNames uses a week that starts on Sunday 2026-01-04 so index 0 is
// Sunday, matching the server's cron numbering.
export function weekdayNames(locale: string) {
  const formatter = new Intl.DateTimeFormat(locale, { weekday: "short" });
  return Array.from({ length: 7 }, (_, day) =>
    formatter.format(new Date(Date.UTC(2026, 0, 4 + day, 12))),
  );
}

export function describeFrequency(
  frequency: ScheduleFrequency,
  t: Translate,
  locale: string,
) {
  const time = frequency.time ?? "";
  switch (frequency.kind) {
    case "hourly":
      return frequency.every_hours === 1
        ? t("schedules.desc.hourlyOne", { minute: time.slice(3) })
        : t("schedules.desc.hourly", {
            hours: frequency.every_hours ?? 1,
            time,
          });
    case "daily":
      return t("schedules.desc.daily", { time });
    case "weekly": {
      const names = weekdayNames(locale);
      const days = [...(frequency.weekdays ?? [])]
        .sort((left, right) => ((left + 6) % 7) - ((right + 6) % 7))
        .map((day) => names[day])
        .join(locale.startsWith("zh") ? "、" : ", ");
      return t("schedules.desc.weekly", { days, time });
    }
    case "monthly":
      return t("schedules.desc.monthly", {
        day: frequency.month_day ?? 1,
        time,
      });
    default:
      return t("schedules.desc.cron", { cron: frequency.cron ?? "" });
  }
}

export function describeScope(scope: ScheduleScope, t: Translate) {
  const kinds = scope.resource_kind_ids?.length
    ? t("common.selectedResourceKindCount", {
        count: scope.resource_kind_ids.length,
      })
    : t("scans.allKinds");
  switch (scope.scope_mode) {
    case "all_active_regions":
      return { title: t("scans.allActiveRegions"), detail: kinds };
    case "selected_regions":
      return {
        title: t("scans.partialRegions"),
        detail: `${(scope.region_ids ?? [])
          .map((region) => (region === "global" ? t("common.global") : region))
          .join(", ")} · ${kinds}`,
      };
    default:
      return {
        title: t("scans.selectedNetworks"),
        detail: (scope.network_targets ?? [])
          .map((target) => target.name || target.native_id)
          .join(", "),
      };
  }
}

export function describeRules(rules: ScheduleRules, t: Translate) {
  return [
    rules.overlap === "skip"
      ? t("schedules.rulesSummary.skip")
      : t("schedules.rulesSummary.wait"),
    rules.missed === "catch_up"
      ? t("schedules.rulesSummary.catch_up")
      : t("schedules.rulesSummary.noCatchUp"),
    rules.retry_failed_targets
      ? t("schedules.rulesSummary.retry")
      : t("schedules.rulesSummary.noRetry"),
    t("schedules.rulesSummary.pause", { count: rules.pause_after_failures }),
  ].join(" · ");
}

// runResult turns a planned run into what a person reads: a state for the
// badge, a short label and an optional explanation.
export function runResult(
  run: ScheduleRun,
  t: Translate,
  label: (value: string) => string,
): { value: string; label: string; detail?: string } {
  if (run.outcome === "skipped") {
    return {
      value: "skipped",
      label: t("schedules.run.skipped"),
      detail:
        run.skip_reason === "missed"
          ? t("schedules.run.missedDetail")
          : t("schedules.run.overlapDetail"),
    };
  }
  if (run.outcome === "failed_to_start") {
    return {
      value: "failed",
      label: t("schedules.run.failedToStart"),
      detail: run.error,
    };
  }
  if (run.outcome === "starting") {
    return { value: "pending", label: t("schedules.run.starting") };
  }
  const status = run.final_status || run.scan?.status || "running";
  const detail = [
    run.failure_summary,
    run.auto_retried ? t("schedules.run.autoRetried") : undefined,
  ]
    .filter(Boolean)
    .join(" · ");
  if (status === "partial") {
    return {
      value: "partial",
      label: t("schedules.run.partial", { count: run.failed_items ?? 0 }),
      detail: detail || undefined,
    };
  }
  return { value: status, label: label(status), detail: detail || undefined };
}

export function runTrigger(run: ScheduleRun, t: Translate) {
  if (run.trigger === "catch_up") return t("schedules.run.catchUp");
  if (run.trigger === "manual") return t("schedules.run.manual");
  return undefined;
}

export function browserTimezone() {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

export function timezoneOptions() {
  const zones =
    typeof Intl.supportedValuesOf === "function"
      ? Intl.supportedValuesOf("timeZone")
      : [];
  return zones.includes("UTC") ? zones : ["UTC", ...zones];
}
