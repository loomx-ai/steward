import { useEffect, useMemo, useState, type FormEvent } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ChevronDown } from "lucide-react";
import {
  APIRequestError,
  createSchedule,
  getScheduleSettings,
  listConnectionRegions,
  listProviderCatalog,
  previewSchedule,
  runSchedule,
  updateSchedule,
} from "@/api/client";
import type {
  CloudConnection,
  FrequencyKind,
  ScanSchedule,
  ScheduleFrequency,
  ScheduleInput,
  ScheduleRules,
  ScheduleRun,
} from "@/api/types";
import { TaskCreationDialog } from "@/components/patterns/TaskCreationDialog";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { Combobox } from "@/components/ui/combobox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useScanScope } from "@/features/scans/ScanScopeFields";
import { useLocale } from "@/i18n/LocaleProvider";
import { cn } from "@/lib/utils";
import {
  browserTimezone,
  defaultRules,
  describeRules,
  hourlyIntervals,
  timezoneOptions,
  weekdayNames,
} from "./scheduleFormat";

export type ScheduleDialogMode =
  | { kind: "create" }
  | { kind: "edit"; schedule: ScanSchedule }
  | { kind: "duplicate"; schedule: ScanSchedule };

const frequencyKinds: FrequencyKind[] = [
  "hourly",
  "daily",
  "weekly",
  "monthly",
  "cron",
];

// The dialog mounts its form only while open, so each opening starts from the
// schedule being edited rather than from the previous session's edits.
export function ScheduleDialog({
  mode,
  connection,
  onOpenChange,
  onSaved,
}: {
  mode?: ScheduleDialogMode;
  connection: CloudConnection;
  onOpenChange: (open: boolean) => void;
  onSaved: (schedule: ScanSchedule, run?: ScheduleRun) => void;
}) {
  return mode ? (
    <ScheduleForm
      key={
        mode.kind === "create" ? "create" : `${mode.kind}:${mode.schedule.id}`
      }
      mode={mode}
      connection={connection}
      onOpenChange={onOpenChange}
      onSaved={onSaved}
    />
  ) : null;
}

function ScheduleForm({
  mode,
  connection,
  onOpenChange,
  onSaved,
}: {
  mode: ScheduleDialogMode;
  connection: CloudConnection;
  onOpenChange: (open: boolean) => void;
  onSaved: (schedule: ScanSchedule, run?: ScheduleRun) => void;
}) {
  const { formatDate, formatError, locale, messageForCode, t } = useLocale();
  const source = mode.kind === "create" ? undefined : mode.schedule;
  const regions = useQuery({
    queryKey: ["connection-regions", connection.id, "active"],
    queryFn: () =>
      listConnectionRegions(connection.id, { lifecycle: "active" }),
  });
  const catalog = useQuery({
    queryKey: ["catalog"],
    queryFn: listProviderCatalog,
  });
  const settings = useQuery({
    queryKey: ["schedule-settings"],
    queryFn: getScheduleSettings,
  });
  const scanScope = useScanScope({
    connection,
    regions: regions.data?.items ?? [],
    bundles: catalog.data ?? [],
    enabled: true,
    initial: source?.scope,
  });
  const [name, setName] = useState(() =>
    mode.kind === "duplicate"
      ? t("schedules.copyName", {
          name: mode.schedule.name || t("schedules.defaultName"),
        })
      : (source?.name ?? ""),
  );
  const [frequency, setFrequency] = useState<ScheduleFrequency>(
    () =>
      source?.frequency ?? {
        kind: "daily",
        time: "03:00",
        timezone: browserTimezone(),
      },
  );
  const [rules, setRules] = useState<ScheduleRules>(
    source?.rules ?? defaultRules,
  );
  const [rulesOpen, setRulesOpen] = useState(false);
  useEffect(() => {
    // A workspace default time zone wins over the browser's for new schedules.
    if (!source && settings.data?.default_timezone) {
      setFrequency((current) => ({
        ...current,
        timezone: settings.data.default_timezone ?? current.timezone,
      }));
    }
  }, [settings.data?.default_timezone, source]);
  const minHours = (settings.data?.min_interval_seconds ?? 3600) / 3600;
  const intervals = hourlyIntervals.filter((hours) => hours >= minHours);
  const debouncedFrequency = useDebouncedValue(
    normalizeFrequency(frequency),
    300,
  );
  const preview = useQuery({
    queryKey: ["schedule-preview", connection.id, debouncedFrequency],
    queryFn: ({ signal }) =>
      previewSchedule(connection.id, debouncedFrequency, signal),
    retry: false,
  });
  const input: ScheduleInput = {
    name: name.trim(),
    scope: scanScope.scope,
    frequency: normalizeFrequency(frequency),
    rules,
    ...(mode.kind === "edit" ? {} : { enabled: true }),
  };
  const save = useMutation({
    mutationFn: async (runNow: boolean) => {
      const saved =
        mode.kind === "edit"
          ? await updateSchedule(connection.id, mode.schedule.id, input)
          : await createSchedule(connection.id, input);
      const run = runNow
        ? await runSchedule(connection.id, saved.id)
        : undefined;
      return { saved, run };
    },
    onSuccess: ({ saved, run }) => onSaved(saved, run),
  });
  const canSubmit =
    scanScope.valid && !preview.isError && name.trim().length <= 100;
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (canSubmit) save.mutate(false);
  };
  const timezones = useMemo(
    () =>
      timezoneOptions().map((zone) => ({
        value: zone,
        label: zone.replaceAll("_", " "),
      })),
    [],
  );
  const weekdays = weekdayNames(locale);
  const title = mode.kind === "edit" ? t("schedules.edit") : t("schedules.new");
  const supportingError = regions.error ?? catalog.error;
  return (
    <TaskCreationDialog
      open
      onOpenChange={onOpenChange}
      title={title}
      description={connection.name}
      pending={save.isPending}
      onSubmit={submit}
      bodyClassName="space-y-5"
      footer={
        <>
          <Button
            type="button"
            variant="ghost"
            disabled={save.isPending}
            onClick={() => onOpenChange(false)}
          >
            {t("common.cancel")}
          </Button>
          {mode.kind !== "edit" && (
            <Button
              type="button"
              variant="outline"
              disabled={!canSubmit || save.isPending}
              onClick={() => save.mutate(true)}
            >
              {t("schedules.saveAndRun")}
            </Button>
          )}
          <Button type="submit" disabled={!canSubmit || save.isPending}>
            {save.isPending ? t("schedules.saving") : t("schedules.save")}
          </Button>
        </>
      }
    >
      {(save.error || supportingError) && (
        <Alert variant="destructive">
          <AlertDescription>
            {formatError(save.error ?? supportingError)}
          </AlertDescription>
        </Alert>
      )}
      <div className="space-y-2">
        <Label htmlFor="schedule-name">{t("schedules.name")}</Label>
        <Input
          id="schedule-name"
          value={name}
          maxLength={100}
          placeholder={t("schedules.namePlaceholder")}
          onChange={(event) => setName(event.target.value)}
        />
      </div>
      {scanScope.fields}
      <fieldset className="space-y-3">
        <legend className="mb-3 text-sm font-medium">
          {t("schedules.frequency")}
        </legend>
        <div
          role="group"
          aria-label={t("schedules.frequency")}
          className="inline-flex flex-wrap gap-1 rounded-lg bg-muted p-1"
        >
          {frequencyKinds.map((kind) => (
            <button
              key={kind}
              type="button"
              aria-pressed={frequency.kind === kind}
              className={cn(
                "h-8 rounded-md px-3 text-sm text-muted-foreground transition-colors",
                frequency.kind === kind &&
                  "bg-background font-medium text-foreground shadow-xs",
              )}
              onClick={() =>
                setFrequency((current) => switchKind(current, kind, intervals))
              }
            >
              {t(`schedules.freq.${kind}`)}
            </button>
          ))}
        </div>
        <div className="flex flex-wrap items-end gap-3">
          {frequency.kind === "hourly" && (
            <div className="space-y-1.5">
              <Label>{t("schedules.freq.interval")}</Label>
              <Select
                value={String(frequency.every_hours ?? intervals[0])}
                onValueChange={(value) =>
                  setFrequency((current) => ({
                    ...current,
                    every_hours: Number(value),
                  }))
                }
              >
                <SelectTrigger
                  className="w-40"
                  aria-label={t("schedules.freq.interval")}
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {intervals.map((hours) => (
                    <SelectItem key={hours} value={String(hours)}>
                      {hours === 1
                        ? t("schedules.freq.everyHour")
                        : t("schedules.freq.everyHours", { hours })}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}
          {frequency.kind === "monthly" && (
            <div className="space-y-1.5">
              <Label>{t("schedules.freq.monthDay")}</Label>
              <Select
                value={String(frequency.month_day ?? 1)}
                onValueChange={(value) =>
                  setFrequency((current) => ({
                    ...current,
                    month_day: Number(value),
                  }))
                }
              >
                <SelectTrigger
                  className="w-32"
                  aria-label={t("schedules.freq.monthDay")}
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {Array.from({ length: 28 }, (_, index) => index + 1).map(
                    (day) => (
                      <SelectItem key={day} value={String(day)}>
                        {t("schedules.freq.dayOption", { day })}
                      </SelectItem>
                    ),
                  )}
                </SelectContent>
              </Select>
            </div>
          )}
          {frequency.kind !== "cron" && (
            <div className="space-y-1.5">
              <Label htmlFor="schedule-time">
                {frequency.kind === "hourly"
                  ? t("schedules.freq.firstRun")
                  : t("schedules.freq.time")}
              </Label>
              <Input
                id="schedule-time"
                type="time"
                className="w-32"
                value={frequency.time ?? "00:00"}
                onChange={(event) =>
                  setFrequency((current) => ({
                    ...current,
                    time: event.target.value || "00:00",
                  }))
                }
              />
            </div>
          )}
          {frequency.kind === "cron" && (
            <div className="min-w-64 flex-1 space-y-1.5">
              <Label htmlFor="schedule-cron">
                {t("schedules.freq.cronExpression")}
              </Label>
              <Input
                id="schedule-cron"
                className="font-mono"
                value={frequency.cron ?? ""}
                placeholder="0 2 * * 1"
                aria-describedby="schedule-cron-hint"
                onChange={(event) =>
                  setFrequency((current) => ({
                    ...current,
                    cron: event.target.value,
                  }))
                }
              />
              <p
                id="schedule-cron-hint"
                className="text-xs text-muted-foreground"
              >
                {t("schedules.freq.cronHint")}
              </p>
            </div>
          )}
        </div>
        {frequency.kind === "weekly" && (
          <div
            role="group"
            aria-label={t("schedules.freq.weekdays")}
            className="flex flex-wrap gap-1"
          >
            {[1, 2, 3, 4, 5, 6, 0].map((day) => {
              const selected = frequency.weekdays?.includes(day) ?? false;
              return (
                <button
                  key={day}
                  type="button"
                  aria-pressed={selected}
                  className={cn(
                    "h-8 min-w-11 rounded-md border px-2 text-sm",
                    selected &&
                      "border-foreground bg-foreground text-background",
                  )}
                  onClick={() =>
                    setFrequency((current) => ({
                      ...current,
                      weekdays: selected
                        ? (current.weekdays ?? []).filter(
                            (value) => value !== day,
                          )
                        : [...(current.weekdays ?? []), day],
                    }))
                  }
                >
                  {weekdays[day]}
                </button>
              );
            })}
          </div>
        )}
        <div className="max-w-sm space-y-1.5">
          <Label>{t("schedules.timezone")}</Label>
          <Combobox
            label={t("schedules.timezone")}
            value={frequency.timezone}
            options={timezones}
            placeholder={t("schedules.timezone")}
            searchPlaceholder={t("schedules.searchTimezone")}
            emptyLabel={t("schedules.noTimezones")}
            onValueChange={(timezone) =>
              setFrequency((current) => ({ ...current, timezone }))
            }
          />
        </div>
        <div
          aria-live="polite"
          className="space-y-1 rounded-lg bg-muted/50 px-3 py-2.5 text-sm"
        >
          {preview.isError ? (
            <p className="text-destructive">
              {preview.error instanceof APIRequestError && preview.error.code
                ? messageForCode(preview.error.code, preview.error.message)
                : formatError(preview.error)}
            </p>
          ) : (
            <>
              <p className="text-muted-foreground">{t("schedules.preview")}</p>
              {preview.data ? (
                <ol className="list-decimal space-y-0.5 pl-5 tabular-nums">
                  {preview.data.next_runs.map((time) => (
                    <li key={time}>{formatDate(time)}</li>
                  ))}
                </ol>
              ) : (
                <p className="text-muted-foreground">
                  {t("schedules.previewLoading")}
                </p>
              )}
            </>
          )}
          {minHours > 1 && (
            <p className="text-xs text-muted-foreground">
              {t("schedules.minInterval", { hours: minHours })}
            </p>
          )}
        </div>
      </fieldset>
      <Collapsible
        open={rulesOpen}
        onOpenChange={setRulesOpen}
        className="rounded-lg border"
      >
        <CollapsibleTrigger className="flex w-full items-center justify-between gap-3 px-3 py-2.5 text-left text-sm">
          <span className="font-medium">{t("schedules.rules")}</span>
          <span className="flex min-w-0 items-center gap-2 text-xs text-muted-foreground">
            <span className="truncate">{describeRules(rules, t)}</span>
            <ChevronDown
              className={cn(
                "size-4 shrink-0 transition-transform",
                rulesOpen && "rotate-180",
              )}
            />
          </span>
        </CollapsibleTrigger>
        <CollapsibleContent className="grid gap-3 border-t px-3 py-3 sm:grid-cols-2">
          <RuleSelect
            label={t("schedules.rules.overlap")}
            value={rules.overlap}
            options={[
              ["skip", t("schedules.rules.overlap.skip")],
              ["wait", t("schedules.rules.overlap.wait")],
            ]}
            onChange={(overlap) =>
              setRules((current) => ({
                ...current,
                overlap: overlap as ScheduleRules["overlap"],
              }))
            }
          />
          <RuleSelect
            label={t("schedules.rules.missed")}
            value={rules.missed}
            options={[
              ["catch_up", t("schedules.rules.missed.catch_up")],
              ["skip", t("schedules.rules.missed.skip")],
            ]}
            onChange={(missed) =>
              setRules((current) => ({
                ...current,
                missed: missed as ScheduleRules["missed"],
              }))
            }
          />
          <RuleSelect
            label={t("schedules.rules.retry")}
            value={rules.retry_failed_targets ? "on" : "off"}
            options={[
              ["on", t("schedules.rules.retry.on")],
              ["off", t("schedules.rules.retry.off")],
            ]}
            onChange={(value) =>
              setRules((current) => ({
                ...current,
                retry_failed_targets: value === "on",
              }))
            }
          />
          <RuleSelect
            label={t("schedules.rules.pause")}
            value={String(rules.pause_after_failures)}
            options={[2, 3, 5].map((count) => [
              String(count),
              t("schedules.rules.pauseAfter", { count }),
            ])}
            onChange={(value) =>
              setRules((current) => ({
                ...current,
                pause_after_failures: Number(value),
              }))
            }
          />
        </CollapsibleContent>
      </Collapsible>
    </TaskCreationDialog>
  );
}

function RuleSelect({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: string;
  options: Array<[string, string]>;
  onChange: (value: string) => void;
}) {
  return (
    <div className="space-y-1.5">
      <Label className="text-xs text-muted-foreground">{label}</Label>
      <Select value={value} onValueChange={onChange}>
        <SelectTrigger className="w-full" aria-label={label}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {options.map(([optionValue, optionLabel]) => (
            <SelectItem key={optionValue} value={optionValue}>
              {optionLabel}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );
}

function switchKind(
  current: ScheduleFrequency,
  kind: FrequencyKind,
  intervals: number[],
): ScheduleFrequency {
  return {
    ...current,
    kind,
    time: current.time ?? "03:00",
    every_hours:
      kind === "hourly"
        ? (current.every_hours ?? intervals[Math.min(1, intervals.length - 1)])
        : current.every_hours,
    weekdays: kind === "weekly" ? (current.weekdays ?? [1]) : current.weekdays,
    month_day:
      kind === "monthly" ? (current.month_day ?? 1) : current.month_day,
    cron: kind === "cron" ? (current.cron ?? "0 3 * * *") : current.cron,
  };
}

// normalizeFrequency sends only the fields the chosen kind uses.
export function normalizeFrequency(
  frequency: ScheduleFrequency,
): ScheduleFrequency {
  const base = { kind: frequency.kind, timezone: frequency.timezone };
  switch (frequency.kind) {
    case "hourly":
      return {
        ...base,
        every_hours: frequency.every_hours,
        time: frequency.time,
      };
    case "daily":
      return { ...base, time: frequency.time };
    case "weekly":
      return { ...base, weekdays: frequency.weekdays, time: frequency.time };
    case "monthly":
      return { ...base, month_day: frequency.month_day, time: frequency.time };
    default:
      return { ...base, cron: frequency.cron?.trim() };
  }
}

function useDebouncedValue<T>(value: T, delay: number) {
  const [debounced, setDebounced] = useState(value);
  const key = JSON.stringify(value);
  useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(value), delay);
    return () => window.clearTimeout(timer);
  }, [key, delay]);
  return debounced;
}
