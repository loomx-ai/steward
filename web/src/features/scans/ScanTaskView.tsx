import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Pause, Play, RotateCcw, XCircle } from "lucide-react";
import { useParams } from "react-router-dom";
import { controlScan, getScan } from "@/api/client";
import type { ScanTargetProgress, ScanTask } from "@/api/types";
import { PageTitle } from "@/app/PageTitleContext";
import { AsyncState } from "@/components/domain/AsyncState";
import { CopyableId } from "@/components/domain/CopyableId";
import { StateBadge, stateTone } from "@/components/domain/StateBadge";
import { PageLayout } from "@/components/patterns/PageLayout";
import { PageToolbar } from "@/components/patterns/PageToolbar";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { useRequiredConnection } from "@/connections/ActiveConnectionProvider";
import { useLocale } from "@/i18n/LocaleProvider";
import { formatDuration } from "@/lib/formatDuration";
import { compareRegionIDs } from "@/lib/regionOrder";
import { ScanTargetCard, scanTargetTitle } from "./ScanTargetCard";
import { ScanTaskEvents } from "./ScanTaskEvents";
import { scanStatusLabel } from "./scanStatus";

type ScanAction = "pause" | "resume" | "cancel" | "retry";

export function ScanTaskView() {
  const { id = "" } = useParams();
  const connection = useRequiredConnection();
  const queryClient = useQueryClient();
  const { formatDate, formatError, formatNumber, label, t } = useLocale();
  const [confirmation, setConfirmation] = useState<ScanAction>();
  const [selectedTargetKey, setSelectedTargetKey] = useState<string>();
  const [targetsExpanded, setTargetsExpanded] = useState(false);
  const targetLimit = useCollapsedTargetLimit();
  const task = useQuery({
    queryKey: ["scan", connection.id, id],
    queryFn: () => getScan(connection.id, id),
    enabled: Boolean(id),
  });
  const action = useMutation({
    mutationFn: (value: ScanAction) => controlScan(connection.id, id, value),
    onSuccess: (value) => {
      queryClient.setQueryData(["scan", connection.id, id], value);
      void queryClient.invalidateQueries({
        queryKey: ["scans", connection.id],
      });
      setConfirmation(undefined);
    },
  });
  const value = task.data;
  const orderedTargets = useMemo(
    () => orderScanTargets(value?.target_progress ?? []),
    [value?.target_progress],
  );
  const selectedTarget = value?.target_progress.find(
    (target) => target.key === selectedTargetKey,
  );
  const hasCollapsedTargets = orderedTargets.length > targetLimit;
  const visibleTargets =
    targetsExpanded || !hasCollapsedTargets
      ? orderedTargets
      : orderedTargets.slice(0, targetLimit);
  return (
    <>
      <PageTitle
        title={value?.id || id}
        parent={{ label: t("nav.scans"), to: "/scans" }}
      />
      <PageLayout mode="reading" className="space-y-6">
        <AsyncState
          pending={task.isPending}
          error={task.error}
          empty={!task.isPending && !value}
          onRetry={() => void task.refetch()}
          formatError={formatError}
          labels={{
            empty: t("scans.empty"),
            retry: t("shell.retry"),
            failed: t("shell.routeFailure"),
            loading: t("scans.loading"),
          }}
        >
          {value && (
            <>
              <PageToolbar
                label={t("common.actions")}
                trailing={
                  <div className="flex flex-wrap gap-2">
                    {value.allowed_actions.map((item) => (
                      <Button
                        key={item}
                        size="sm"
                        variant={item === "cancel" ? "destructive" : "outline"}
                        onClick={() => setConfirmation(item)}
                        disabled={action.isPending}
                      >
                        {actionIcon(item)}
                        {t(`scans.action.${item}` as Parameters<typeof t>[0])}
                      </Button>
                    ))}
                  </div>
                }
              />
              <dl className="grid gap-x-16 gap-y-3 md:grid-cols-2">
                <Fact label={t("scans.taskId")} mono>
                  <CopyableId label={t("scans.taskId")} value={value.id} />
                </Fact>
                <Fact label={t("common.status")}>
                  <StateBadge
                    value={value.status}
                    label={scanStatusLabel(
                      value.status,
                      label(value.status),
                      t("scans.inProgress"),
                    )}
                  />
                </Fact>
                <Fact label={t("common.requestedBy")}>
                  {value.requested_by}
                </Fact>
                <Fact label={t("scans.targetProgress")}>
                  {value.progress.completed} / {value.progress.total}
                </Fact>
                <Fact label={t("common.created")}>
                  {formatDate(value.created_at)}
                </Fact>
                <Fact label={t("common.updated")}>
                  {formatDate(value.updated_at ?? value.created_at)}
                </Fact>
                <Fact label={t("scans.resources")}>
                  {formatNumber(value.progress.resource_count)}
                </Fact>
                <Fact label={t("scans.scope")}>
                  {scopeLabel(value.scope_mode, t)}
                </Fact>
                <Fact label={t("scans.retryCount")}>
                  {formatNumber(value.retry_count)}
                </Fact>
                <Fact label={t("scans.duration")}>
                  {formatDuration(value.duration_ms)}
                </Fact>
              </dl>
              <section className="space-y-3">
                <div className="flex items-center justify-between gap-3">
                  <h2 className="text-sm font-semibold">
                    {value.scope_mode === "selected_networks"
                      ? t("scans.targetProgress")
                      : t("scans.regionalProgress")}
                  </h2>
                  {hasCollapsedTargets && (
                    <Button
                      type="button"
                      size="sm"
                      variant="ghost"
                      className="h-7 px-2 text-muted-foreground"
                      aria-expanded={targetsExpanded}
                      onClick={() => setTargetsExpanded((current) => !current)}
                    >
                      {targetsExpanded
                        ? t("scans.showLessTargets")
                        : t("scans.showAllTargets", {
                            count: value.target_progress.length,
                          })}
                    </Button>
                  )}
                </div>
                <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
                  {visibleTargets.map((target) => (
                    <ScanTargetCard
                      key={target.key}
                      target={target}
                      selected={selectedTargetKey === target.key}
                      onSelect={() =>
                        setSelectedTargetKey((current) =>
                          current === target.key ? undefined : target.key,
                        )
                      }
                    />
                  ))}
                </div>
              </section>
              <ScanTaskEvents
                connectionID={connection.id}
                scanID={value.id}
                targetKey={selectedTargetKey}
                targetLabel={
                  selectedTarget
                    ? scanTargetTitle(selectedTarget, t("common.global"))
                    : undefined
                }
                onClearTarget={
                  selectedTarget
                    ? () => setSelectedTargetKey(undefined)
                    : undefined
                }
              />
            </>
          )}
        </AsyncState>
        <ActionConfirmation
          task={value}
          action={confirmation}
          onOpenChange={(open) => !open && setConfirmation(undefined)}
          onConfirm={() => confirmation && action.mutate(confirmation)}
          busy={action.isPending}
        />
      </PageLayout>
    </>
  );
}

export function collapsedTargetLimit(width: number) {
  if (width >= 1536) return 12;
  if (width >= 1280) return 9;
  if (width >= 640) return 6;
  return 3;
}

function useCollapsedTargetLimit() {
  const [limit, setLimit] = useState(() =>
    collapsedTargetLimit(window.innerWidth),
  );
  useEffect(() => {
    const updateLimit = () => setLimit(collapsedTargetLimit(window.innerWidth));
    window.addEventListener("resize", updateLimit);
    return () => window.removeEventListener("resize", updateLimit);
  }, []);
  return limit;
}

function orderScanTargets(targets: ScanTargetProgress[]) {
  const priority = {
    destructive: 0,
    info: 1,
    warning: 2,
    muted: 3,
    success: 4,
  };
  return targets
    .map((target, index) => ({ target, index }))
    .sort((left, right) => {
      return (
        Number(right.target.kind === "global") -
          Number(left.target.kind === "global") ||
        priority[stateTone(left.target.status)] -
          priority[stateTone(right.target.status)] ||
        compareScanTargetRegions(left.target, right.target) ||
        left.index - right.index
      );
    })
    .map(({ target }) => target);
}

function compareScanTargetRegions(
  left: ScanTargetProgress,
  right: ScanTargetProgress,
) {
  if (left.kind === "global" || right.kind === "global") {
    if (left.kind !== right.kind) return left.kind === "global" ? -1 : 1;
  }
  return (
    compareRegionIDs(left.region_id, right.region_id) ||
    left.key.localeCompare(right.key)
  );
}

function Fact({
  label,
  children,
  mono,
}: {
  label: string;
  children: ReactNode;
  mono?: boolean;
}) {
  return (
    <div className="grid min-w-0 grid-cols-[7rem_minmax(0,1fr)] items-start gap-4 text-sm">
      <dt className="font-medium text-muted-foreground">{label}</dt>
      <dd
        className={mono ? "truncate font-mono text-xs" : "min-w-0 tabular-nums"}
      >
        {children}
      </dd>
    </div>
  );
}

function scopeLabel(
  scopeMode: ScanTask["scope_mode"],
  t: ReturnType<typeof useLocale>["t"],
) {
  return scopeMode === "all_active_regions"
    ? t("scans.allRegions")
    : t("scans.partialRegions");
}

function actionIcon(action: ScanAction) {
  if (action === "pause") return <Pause />;
  if (action === "resume") return <Play />;
  if (action === "retry") return <RotateCcw />;
  return <XCircle />;
}

function ActionConfirmation({
  task,
  action,
  onOpenChange,
  onConfirm,
  busy,
}: {
  task?: ScanTask;
  action?: ScanAction;
  onOpenChange: (open: boolean) => void;
  onConfirm: () => void;
  busy: boolean;
}) {
  const { t } = useLocale();
  return (
    <AlertDialog open={Boolean(action)} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            {action
              ? t(`scans.confirm.${action}.title` as Parameters<typeof t>[0])
              : ""}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {action && task
              ? t(
                  `scans.confirm.${action}.description` as Parameters<
                    typeof t
                  >[0],
                  { id: task.id },
                )
              : ""}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
          <AlertDialogAction
            variant={action === "cancel" ? "destructive" : "default"}
            disabled={busy}
            onClick={onConfirm}
          >
            {action === "retry"
              ? t("common.confirm")
              : action
                ? t(`scans.action.${action}` as Parameters<typeof t>[0])
                : ""}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
