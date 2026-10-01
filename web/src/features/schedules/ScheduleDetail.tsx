import type { ReactNode } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { MoreHorizontal, Pause, Pencil, Play } from "lucide-react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  getSchedule,
  getScheduleSettings,
  listScheduleRuns,
} from "@/api/client";
import { PageTitle } from "@/app/PageTitleContext";
import { useHasRole } from "@/auth/roles";
import { AsyncState } from "@/components/domain/AsyncState";
import { CursorPagination } from "@/components/domain/CursorPagination";
import { StateBadge } from "@/components/domain/StateBadge";
import { PageLayout } from "@/components/patterns/PageLayout";
import { PageToolbar } from "@/components/patterns/PageToolbar";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useRequiredConnection } from "@/connections/ActiveConnectionProvider";
import { useCursorPagination } from "@/hooks/useCursorPagination";
import { useLocale } from "@/i18n/LocaleProvider";
import { formatDuration } from "@/lib/formatDuration";
import { ChangeCountsLink } from "./ScheduleChanges";
import { useScheduleActions } from "./ScheduleActions";
import {
  describeFrequency,
  describeRules,
  describeScope,
  pauseReason,
  runResult,
  runTrigger,
  scheduleName,
  scheduleState,
} from "./scheduleFormat";

export function ScheduleDetail() {
  const { id = "" } = useParams();
  const connection = useRequiredConnection();
  const navigate = useNavigate();
  const { formatDate, formatError, formatNumber, label, locale, t } =
    useLocale();
  const canOperate = useHasRole("operator");
  const actions = useScheduleActions(connection, {
    onDeleted: () => navigate("/scans?tab=schedules"),
  });
  const schedule = useQuery({
    queryKey: ["schedule", connection.id, id],
    queryFn: () => getSchedule(connection.id, id),
    enabled: Boolean(id),
    refetchInterval: 30_000,
  });
  const pagination = useCursorPagination(`${connection.id}:${id}`);
  const runs = useQuery({
    queryKey: [
      "schedules",
      connection.id,
      id,
      "runs",
      pagination.cursor,
      pagination.pageSize,
    ],
    queryFn: () =>
      listScheduleRuns(
        connection.id,
        id,
        pagination.cursor,
        pagination.pageSize,
      ),
    enabled: Boolean(id),
    placeholderData: keepPreviousData,
    refetchInterval: 30_000,
  });
  const settings = useQuery({
    queryKey: ["schedule-settings"],
    queryFn: getScheduleSettings,
  });
  const value = schedule.data;
  const name = value ? scheduleName(value, t) : id;
  const state = value ? scheduleState(value) : undefined;
  const actor = (subject: string) =>
    subject === "scheduler" ? t("schedules.byScheduler") : subject;
  return (
    <>
      <PageTitle
        title={name}
        parent={{
          label: t("scans.tabs.schedules"),
          to: "/scans?tab=schedules",
        }}
      />
      <PageLayout mode="reading" className="space-y-6">
        <AsyncState
          pending={schedule.isPending}
          error={schedule.error}
          empty={!schedule.isPending && !value}
          onRetry={() => void schedule.refetch()}
          formatError={formatError}
          labels={{
            empty: t("schedules.empty"),
            retry: t("shell.retry"),
            failed: t("shell.routeFailure"),
            loading: t("schedules.loading"),
          }}
        >
          {value && state && (
            <>
              {canOperate && (
                <PageToolbar
                  label={t("common.actions")}
                  trailing={
                    <div className="flex flex-wrap gap-2">
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={actions.toggling}
                        onClick={() =>
                          actions.setEnabled(value, state !== "active")
                        }
                      >
                        {state === "active" ? <Pause /> : <Play />}
                        {state === "active"
                          ? t("schedules.pause")
                          : t("schedules.resume")}
                      </Button>
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => actions.edit(value)}
                      >
                        <Pencil />
                        {t("schedules.editAction")}
                      </Button>
                      <Button
                        size="sm"
                        disabled={actions.running}
                        onClick={() => actions.run(value)}
                      >
                        <Play />
                        {t("schedules.runNow")}
                      </Button>
                      <DropdownMenu>
                        <DropdownMenuTrigger asChild>
                          <Button
                            size="icon-sm"
                            variant="ghost"
                            aria-label={t("schedules.more")}
                          >
                            <MoreHorizontal />
                          </Button>
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end">
                          <DropdownMenuItem
                            onSelect={() => actions.duplicate(value)}
                          >
                            {t("schedules.duplicate")}
                          </DropdownMenuItem>
                          <DropdownMenuItem
                            variant="destructive"
                            onSelect={() => actions.confirmDelete(value)}
                          >
                            {t("schedules.delete")}
                          </DropdownMenuItem>
                        </DropdownMenuContent>
                      </DropdownMenu>
                    </div>
                  }
                />
              )}
              {state === "paused" && (
                <Alert variant="destructive">
                  <AlertTitle>
                    {t("schedules.state.paused")} · {pauseReason(value, t)}
                  </AlertTitle>
                  <AlertDescription>
                    {value.pause_detail && <p>{value.pause_detail}</p>}
                    {value.pause_reason === "consecutive_failures" && (
                      <p>
                        {t("schedules.pausedBannerHint")}{" "}
                        <Link
                          className="underline underline-offset-4"
                          to="/settings?section=connections"
                        >
                          {t("schedules.replaceCredential")}
                        </Link>
                      </p>
                    )}
                  </AlertDescription>
                </Alert>
              )}
              <dl className="grid gap-x-16 gap-y-3 md:grid-cols-2">
                <Fact label={t("common.status")}>
                  <StateBadge
                    value={state === "active" ? "active" : state}
                    label={t(`schedules.state.${state}`)}
                  />
                </Fact>
                <Fact label={t("schedules.scope")}>
                  {describeScope(value.scope, t).title}
                  <span className="block text-xs text-muted-foreground">
                    {describeScope(value.scope, t).detail}
                  </span>
                </Fact>
                <Fact label={t("schedules.frequency")}>
                  {describeFrequency(value.frequency, t, locale)}
                  <span className="block text-xs text-muted-foreground">
                    {value.frequency.timezone}
                  </span>
                </Fact>
                <Fact label={t("schedules.nextRun")}>
                  {value.next_run_at ? formatDate(value.next_run_at) : "—"}
                </Fact>
                <Fact label={t("schedules.rules")}>
                  {describeRules(value.rules, t)}
                </Fact>
                <Fact label={t("schedules.createdBy")}>
                  {actor(value.created_by)} · {formatDate(value.created_at)}
                </Fact>
                <Fact label={t("schedules.updatedBy")}>
                  {actor(value.updated_by)} · {formatDate(value.updated_at)}
                </Fact>
              </dl>
              <section className="space-y-3">
                <h2 className="text-sm font-semibold">{t("schedules.runs")}</h2>
                <AsyncState
                  pending={runs.isPending}
                  error={runs.error}
                  empty={
                    !runs.isPending && (runs.data?.items.length ?? 0) === 0
                  }
                  onRetry={() => void runs.refetch()}
                  formatError={formatError}
                  labels={{
                    empty: t("schedules.runsEmpty"),
                    retry: t("shell.retry"),
                    failed: t("shell.routeFailure"),
                    loading: t("schedules.loading"),
                  }}
                >
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>{t("schedules.plannedAt")}</TableHead>
                        <TableHead>{t("schedules.result")}</TableHead>
                        <TableHead>{t("schedules.scanTask")}</TableHead>
                        <TableHead>{t("scans.duration")}</TableHead>
                        <TableHead>{t("scans.resources")}</TableHead>
                        <TableHead>{t("scans.changes")}</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {(runs.data?.items ?? []).map((run) => {
                        const result = runResult(run, t, label);
                        const trigger = runTrigger(run, t);
                        return (
                          <TableRow
                            key={run.id}
                            className={
                              run.outcome === "skipped"
                                ? "text-muted-foreground"
                                : undefined
                            }
                          >
                            <TableCell className="text-xs tabular-nums">
                              {formatDate(run.planned_at)}
                              {trigger && (
                                <span className="block text-muted-foreground">
                                  {trigger}
                                  {run.trigger === "manual" &&
                                    ` · ${actor(run.actor)}`}
                                </span>
                              )}
                            </TableCell>
                            <TableCell className="max-w-96">
                              <StateBadge
                                value={result.value}
                                label={result.label}
                              />
                              {result.detail && (
                                <span className="block text-xs break-words text-muted-foreground">
                                  {result.detail}
                                </span>
                              )}
                              {run.blocking_scan_id && (
                                <Link
                                  className="block font-mono text-xs text-info underline-offset-4 hover:underline"
                                  to={`/scans/${encodeURIComponent(run.blocking_scan_id)}`}
                                >
                                  {run.blocking_scan_id}
                                </Link>
                              )}
                            </TableCell>
                            <TableCell>
                              {run.scan_task_id && run.scan ? (
                                <Link
                                  className="font-mono text-xs text-info underline-offset-4 hover:underline"
                                  to={`/scans/${encodeURIComponent(run.scan_task_id)}`}
                                >
                                  {run.scan_task_id}
                                </Link>
                              ) : (
                                <span className="text-muted-foreground">—</span>
                              )}
                            </TableCell>
                            <TableCell className="text-xs text-muted-foreground">
                              {formatDuration(run.scan?.duration_ms)}
                            </TableCell>
                            <TableCell className="tabular-nums">
                              {run.scan
                                ? formatNumber(run.scan.progress.resource_count)
                                : "—"}
                            </TableCell>
                            <TableCell>
                              <ChangeCountsLink
                                counts={run.scan?.changes}
                                scanID={run.scan ? run.scan_task_id : undefined}
                              />
                            </TableCell>
                          </TableRow>
                        );
                      })}
                    </TableBody>
                  </Table>
                </AsyncState>
                {(runs.data?.items.length ?? 0) > 0 && (
                  <CursorPagination
                    page={pagination.page}
                    pageCount={pagination.pageCount}
                    hasNextPage={Boolean(runs.data?.next_cursor)}
                    pending={runs.isFetching}
                    pageSize={pagination.pageSize}
                    onPrevious={pagination.goPrevious}
                    onNext={() =>
                      pagination.goNext(runs.data?.next_cursor ?? "")
                    }
                    onPageSelect={pagination.goToPage}
                    onPageSizeChange={pagination.setPageSize}
                    labels={{
                      page: (page) => t("common.page", { page }),
                      pageSize: t("common.pageSize"),
                      previous: t("common.previous"),
                      next: t("common.next"),
                    }}
                  />
                )}
                {settings.data && (
                  <p className="text-xs text-muted-foreground">
                    {settings.data.retention_days > 0
                      ? t("schedules.retentionHint", {
                          days: settings.data.retention_days,
                        })
                      : t("schedules.retentionForever")}
                  </p>
                )}
              </section>
            </>
          )}
        </AsyncState>
      </PageLayout>
      {actions.dialogs}
    </>
  );
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid min-w-0 grid-cols-[7rem_minmax(0,1fr)] items-start gap-4 text-sm">
      <dt className="font-medium text-muted-foreground">{label}</dt>
      <dd className="min-w-0 tabular-nums">{children}</dd>
    </div>
  );
}
