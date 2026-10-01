import { useQuery } from "@tanstack/react-query";
import { CalendarClock, MoreHorizontal, Plus } from "lucide-react";
import { Link } from "react-router-dom";
import { listSchedules } from "@/api/client";
import type { CloudConnection, ScanSchedule } from "@/api/types";
import { AsyncState } from "@/components/domain/AsyncState";
import { DataTableShell } from "@/components/domain/DataTableShell";
import { StateBadge } from "@/components/domain/StateBadge";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Empty,
  EmptyContent,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useHasRole } from "@/auth/roles";
import { useLocale } from "@/i18n/LocaleProvider";
import { ChangeCountsLink } from "./ScheduleChanges";
import { useScheduleActions } from "./ScheduleActions";
import {
  describeFrequency,
  describeScope,
  pauseReason,
  runResult,
  scheduleName,
  scheduleState,
} from "./scheduleFormat";

export function SchedulesTab({ connection }: { connection: CloudConnection }) {
  const { formatDate, formatError, label, locale, t } = useLocale();
  const canOperate = useHasRole("operator");
  const schedules = useQuery({
    queryKey: ["schedules", connection.id],
    queryFn: () => listSchedules(connection.id),
    refetchInterval: 30_000,
  });
  const actions = useScheduleActions(connection);
  const rows = schedules.data ?? [];
  return (
    <>
      <DataTableShell
        toolbar={
          canOperate ? (
            <div className="ml-auto flex w-full sm:w-auto">
              <Button
                className="h-11 flex-1 sm:h-9 sm:flex-none"
                onClick={actions.create}
              >
                <Plus />
                {t("schedules.new")}
              </Button>
            </div>
          ) : undefined
        }
      >
        <AsyncState
          pending={schedules.isPending}
          error={schedules.error}
          empty={false}
          onRetry={() => void schedules.refetch()}
          formatError={formatError}
          labels={{
            empty: t("schedules.empty"),
            retry: t("shell.retry"),
            failed: t("shell.routeFailure"),
            loading: t("schedules.loading"),
          }}
        >
          {rows.length === 0 ? (
            <Empty className="py-12">
              <EmptyHeader>
                <EmptyMedia variant="icon">
                  <CalendarClock />
                </EmptyMedia>
                <EmptyTitle className="max-w-md text-sm font-normal text-muted-foreground">
                  {t("schedules.empty")}
                </EmptyTitle>
              </EmptyHeader>
              {canOperate && (
                <EmptyContent>
                  <Button onClick={actions.create}>
                    <Plus />
                    {t("schedules.new")}
                  </Button>
                </EmptyContent>
              )}
            </Empty>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-14">
                    {t("schedules.enabledColumn")}
                  </TableHead>
                  <TableHead>{t("schedules.name")}</TableHead>
                  <TableHead>{t("schedules.scope")}</TableHead>
                  <TableHead>{t("schedules.frequency")}</TableHead>
                  <TableHead>{t("schedules.nextRun")}</TableHead>
                  <TableHead>{t("schedules.lastRun")}</TableHead>
                  <TableHead>{t("scans.changes")}</TableHead>
                  <TableHead className="w-10">
                    <span className="sr-only">{t("common.actions")}</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((schedule) => (
                  <ScheduleRow
                    key={schedule.id}
                    schedule={schedule}
                    canOperate={canOperate}
                    actions={actions}
                    describe={{
                      frequency: describeFrequency(
                        schedule.frequency,
                        t,
                        locale,
                      ),
                      scope: describeScope(schedule.scope, t),
                    }}
                    formatDate={formatDate}
                    label={label}
                  />
                ))}
              </TableBody>
            </Table>
          )}
        </AsyncState>
      </DataTableShell>
      {actions.dialogs}
    </>
  );
}

function ScheduleRow({
  schedule,
  canOperate,
  actions,
  describe,
  formatDate,
  label,
}: {
  schedule: ScanSchedule;
  canOperate: boolean;
  actions: ReturnType<typeof useScheduleActions>;
  describe: { frequency: string; scope: { title: string; detail: string } };
  formatDate: (value: string) => string;
  label: (value: string) => string;
}) {
  const { t } = useLocale();
  const name = scheduleName(schedule, t);
  const state = scheduleState(schedule);
  const last = schedule.last_run;
  const result = last ? runResult(last, t, label) : undefined;
  return (
    <TableRow
      className={state === "active" ? undefined : "text-muted-foreground"}
    >
      <TableCell>
        <Switch
          checked={schedule.enabled}
          disabled={!canOperate || actions.toggling}
          aria-label={t("schedules.toggle", { name })}
          onCheckedChange={(enabled) => actions.setEnabled(schedule, enabled)}
        />
      </TableCell>
      <TableCell className="max-w-64">
        <Link
          to={`/scans/schedules/${encodeURIComponent(schedule.id)}`}
          className="font-medium text-info underline-offset-4 hover:underline"
        >
          {name}
        </Link>
        {state === "paused" && (
          <span className="block text-xs text-destructive">
            {t("schedules.state.paused")} · {pauseReason(schedule, t)}
          </span>
        )}
      </TableCell>
      <TableCell className="max-w-72">
        <span className="block text-sm">{describe.scope.title}</span>
        <span className="block truncate text-xs text-muted-foreground">
          {describe.scope.detail}
        </span>
      </TableCell>
      <TableCell className="text-sm">
        {describe.frequency}
        <span className="block text-xs text-muted-foreground">
          {schedule.frequency.timezone}
        </span>
      </TableCell>
      <TableCell className="text-xs tabular-nums">
        {schedule.next_run_at ? formatDate(schedule.next_run_at) : "—"}
      </TableCell>
      <TableCell>
        {last && result ? (
          <>
            <StateBadge value={result.value} label={result.label} />
            <span className="block text-xs text-muted-foreground tabular-nums">
              {formatDate(last.planned_at)}
            </span>
          </>
        ) : (
          <span className="text-muted-foreground">—</span>
        )}
      </TableCell>
      <TableCell>
        <ChangeCountsLink
          counts={last?.scan?.changes}
          scanID={last?.scan_task_id}
        />
      </TableCell>
      <TableCell>
        {canOperate && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label={`${t("schedules.more")}: ${name}`}
              >
                <MoreHorizontal />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem
                disabled={actions.running}
                onSelect={() => actions.run(schedule)}
              >
                {t("schedules.runNow")}
              </DropdownMenuItem>
              <DropdownMenuItem onSelect={() => actions.edit(schedule)}>
                {t("schedules.editAction")}
              </DropdownMenuItem>
              <DropdownMenuItem onSelect={() => actions.duplicate(schedule)}>
                {t("schedules.duplicate")}
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                variant="destructive"
                onSelect={() => actions.confirmDelete(schedule)}
              >
                {t("schedules.delete")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </TableCell>
    </TableRow>
  );
}
