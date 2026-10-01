import { useMemo } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { toast } from "sonner";
import { getScheduleSettings, updateScheduleSettings } from "@/api/client";
import type { ScanSchedule, ScheduleSettings as Settings } from "@/api/types";
import { useHasRole } from "@/auth/roles";
import { StateBadge } from "@/components/domain/StateBadge";
import { SettingsGroup, SettingsRow } from "@/components/patterns/SettingsList";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Combobox } from "@/components/ui/combobox";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useCleanupSelection } from "@/features/panorama/CleanupSelectionContext";
import {
  browserTimezone,
  describeFrequency,
  pauseReason,
  timezoneOptions,
} from "@/features/schedules/scheduleFormat";
import {
  nextRunAt,
  useScheduleOverview,
} from "@/features/schedules/useScheduleOverview";
import { useLocale } from "@/i18n/LocaleProvider";

const retentionOptions = [7, 30, 90, 0];

export function ScheduleSettings() {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const { requestConnectionChange } = useCleanupSelection();
  const { formatDate, formatError, formatRelative, locale, t } = useLocale();
  const isAdmin = useHasRole("admin");
  const settings = useQuery({
    queryKey: ["schedule-settings"],
    queryFn: getScheduleSettings,
  });
  const overview = useScheduleOverview();
  const save = useMutation({
    mutationFn: (next: Settings) =>
      updateScheduleSettings({
        default_schedule_enabled: next.default_schedule_enabled,
        retention_days: next.retention_days,
        default_timezone: next.default_timezone,
      }),
    onSuccess: (value) => {
      queryClient.setQueryData(["schedule-settings"], value);
      toast.success(t("schedules.settings.saved"));
    },
    onError: (error) => toast.error(formatError(error)),
  });
  const timezones = useMemo(
    () =>
      timezoneOptions().map((zone) => ({
        value: zone,
        label: zone.replaceAll("_", " "),
      })),
    [],
  );
  const value = settings.data;
  const update = (patch: Partial<Settings>) => {
    if (!value) return;
    const next = { ...value, ...patch };
    // Turning the default on fixes its time zone, so the random night-time
    // run lands at night where the workspace is.
    if (next.default_schedule_enabled && !next.default_timezone) {
      next.default_timezone = browserTimezone();
    }
    save.mutate(next);
  };
  const connections = overview.data ?? [];
  const scheduleCount = connections.reduce(
    (count, item) => count + item.schedules.length,
    0,
  );
  const open = (connectionID: string) => {
    requestConnectionChange(connectionID);
    navigate("/scans?tab=schedules");
  };
  const error = settings.error ?? overview.error;
  return (
    <div className="space-y-8">
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{formatError(error)}</AlertDescription>
        </Alert>
      )}
      {!isAdmin && (
        <p className="text-sm text-muted-foreground">
          {t("schedules.settings.adminOnly")}
        </p>
      )}
      {value && (
        <>
          <SettingsGroup title={t("schedules.settings.defaultTitle")}>
            <SettingsRow
              label={t("schedules.settings.defaultLabel")}
              description={t("schedules.settings.defaultDescription")}
              control={
                <Switch
                  checked={value.default_schedule_enabled}
                  disabled={!isAdmin || save.isPending}
                  aria-label={t("schedules.settings.defaultLabel")}
                  onCheckedChange={(checked) =>
                    update({ default_schedule_enabled: checked })
                  }
                />
              }
            />
            <SettingsRow
              label={t("schedules.settings.timezone")}
              description={t("schedules.settings.timezoneDescription")}
              control={
                <div className="w-64 max-w-full text-left">
                  <Combobox
                    label={t("schedules.settings.timezone")}
                    value={value.default_timezone || browserTimezone()}
                    options={timezones}
                    placeholder={t("schedules.timezone")}
                    searchPlaceholder={t("schedules.searchTimezone")}
                    emptyLabel={t("schedules.noTimezones")}
                    onValueChange={(zone) =>
                      isAdmin && update({ default_timezone: zone })
                    }
                  />
                </div>
              }
            />
          </SettingsGroup>
          <SettingsGroup title={t("schedules.settings.history")}>
            <SettingsRow
              label={t("schedules.settings.retention")}
              description={t("schedules.settings.retentionDescription")}
              control={
                <Select
                  value={String(value.retention_days)}
                  disabled={!isAdmin || save.isPending}
                  onValueChange={(days) =>
                    update({ retention_days: Number(days) })
                  }
                >
                  <SelectTrigger
                    className="w-32"
                    aria-label={t("schedules.settings.retention")}
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {[
                      ...new Set([...retentionOptions, value.retention_days]),
                    ].map((days) => (
                      <SelectItem key={days} value={String(days)}>
                        {days === 0
                          ? t("schedules.settings.forever")
                          : t("schedules.settings.days", { days })}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              }
            />
          </SettingsGroup>
        </>
      )}
      <SettingsGroup
        title={t("schedules.settings.overview")}
        description={t("schedules.settings.overviewCount", {
          schedules: scheduleCount,
          connections: connections.length,
        })}
        contentClassName="divide-y-0"
      >
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t("common.connection")}</TableHead>
              <TableHead>{t("schedules.settings.plans")}</TableHead>
              <TableHead>{t("schedules.settings.state")}</TableHead>
              <TableHead>{t("schedules.nextRun")}</TableHead>
              <TableHead>{t("schedules.settings.completeScan")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {connections.map((item) => {
              const paused = item.schedules.find(
                (schedule) => schedule.pause_reason,
              );
              const failed = item.schedules.some(
                (schedule) =>
                  schedule.last_run?.final_status === "partial" ||
                  schedule.last_run?.final_status === "failed",
              );
              const next = nextRunAt(item);
              const shortest = item.schedules
                .filter((schedule) => schedule.enabled)
                .sort(
                  (left, right) =>
                    approximateHours(left.frequency) -
                    approximateHours(right.frequency),
                )[0];
              return (
                <TableRow
                  key={item.connection_id}
                  className="cursor-pointer"
                  onClick={() => open(item.connection_id)}
                >
                  <TableCell>
                    <button
                      type="button"
                      className="text-left font-medium text-info underline-offset-4 hover:underline"
                      onClick={(event) => {
                        event.stopPropagation();
                        open(item.connection_id);
                      }}
                    >
                      {item.connection_name}
                    </button>
                    <span className="block text-xs text-muted-foreground">
                      {item.provider}
                    </span>
                  </TableCell>
                  <TableCell className="text-sm">
                    {shortest
                      ? t("schedules.settings.planCount", {
                          count: item.schedules.length,
                          frequency: describeFrequency(
                            shortest.frequency,
                            t,
                            locale,
                          ),
                        })
                      : item.schedules.length || "—"}
                  </TableCell>
                  <TableCell>
                    {paused ? (
                      <StateBadge
                        value="failed"
                        label={`${t("schedules.state.paused")} · ${pauseReason(paused, t)}`}
                      />
                    ) : item.schedules.every(
                        (schedule) => !schedule.enabled,
                      ) ? (
                      <StateBadge
                        value="off"
                        label={t("schedules.settings.none")}
                      />
                    ) : failed ? (
                      <StateBadge
                        value="partial"
                        label={t("schedules.settings.lastFailed")}
                      />
                    ) : (
                      <StateBadge
                        value="succeeded"
                        label={t("schedules.settings.healthy")}
                      />
                    )}
                  </TableCell>
                  <TableCell className="text-xs tabular-nums">
                    {next ? formatDate(next) : "—"}
                  </TableCell>
                  <TableCell
                    className="text-xs tabular-nums"
                    title={
                      item.last_complete_scan_at
                        ? formatDate(item.last_complete_scan_at)
                        : undefined
                    }
                  >
                    {item.last_complete_scan_at
                      ? formatRelative(item.last_complete_scan_at)
                      : t("schedules.settings.never")}
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </SettingsGroup>
    </div>
  );
}

// approximateHours ranks frequencies by how often they run; cron expressions
// rank last because their spacing is not known without evaluating them.
function approximateHours(frequency: ScanSchedule["frequency"]) {
  switch (frequency.kind) {
    case "hourly":
      return frequency.every_hours ?? 1;
    case "daily":
      return 24;
    case "weekly":
      return 168 / Math.max(1, frequency.weekdays?.length ?? 1);
    case "monthly":
      return 720;
    default:
      return Number.MAX_SAFE_INTEGER;
  }
}
