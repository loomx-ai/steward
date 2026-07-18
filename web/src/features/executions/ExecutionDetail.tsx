import { useQuery } from "@tanstack/react-query";
import { AlertTriangle } from "lucide-react";
import { Link, useParams } from "react-router-dom";
import { getExecution } from "@/api/client";
import { PageTitle } from "@/app/PageTitleContext";
import { AsyncState } from "@/components/domain/AsyncState";
import { CopyableId } from "@/components/domain/CopyableId";
import { StateBadge } from "@/components/domain/StateBadge";
import { Timeline, type TimelineItem } from "@/components/domain/Timeline";
import { DetailSection } from "@/components/patterns/DetailSection";
import { PageLayout } from "@/components/patterns/PageLayout";
import { PageToolbar } from "@/components/patterns/PageToolbar";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { useRequiredConnection } from "@/connections/ActiveConnectionProvider";
import { useLocale } from "@/i18n/LocaleProvider";

const active = new Set(["pending", "running", "waiting", "reconciling"]);

export function isExecutionActive(status: string) {
  return active.has(status);
}

export function executionTimeline(status: string): TimelineItem[] {
  const failed = status === "failed" || status === "error";
  const complete = status === "succeeded" || status === "completed";
  const reconciling = status === "reconciling";
  const running = ["running", "waiting", "reconciling"].includes(status);
  return [
    {
      id: "queued",
      title: "Queued",
      state: status === "pending" ? "active" : "complete",
    },
    {
      id: "running",
      title: "Provider execution",
      state: failed
        ? "complete"
        : running && !reconciling
          ? "active"
          : running || complete
            ? "complete"
            : "pending",
    },
    {
      id: "reconcile",
      title: "Reconciliation",
      state: failed
        ? "error"
        : reconciling
          ? "active"
          : complete
            ? "complete"
            : "pending",
    },
  ];
}

export function ExecutionDetail() {
  const { id = "" } = useParams();
  const connection = useRequiredConnection();
  const { formatDate, formatError, label, t } = useLocale();
  const execution = useQuery({
    queryKey: ["execution", connection.id, id],
    queryFn: () => getExecution(connection.id, id),
    enabled: !!id,
    refetchInterval: (query) =>
      isExecutionActive(query.state.data?.status ?? "") ? 2500 : false,
  });
  const attempt = execution.data;
  if (!attempt) {
    return (
      <>
        <PageTitle
          title={t("executions.detailTitle", { id })}
          parent={{ label: t("nav.executions"), to: "/executions" }}
        />
        <AsyncState
          pending={execution.isPending}
          error={
            execution.error ??
            (!execution.isPending ? t("executions.notFound") : null)
          }
          empty={false}
          onRetry={() => void execution.refetch()}
          formatError={formatError}
          labels={{
            empty: t("executions.notFound"),
            retry: t("shell.retry"),
            failed: t("shell.routeFailure"),
            loading: t("executions.loadingDetail"),
          }}
        >
          <span />
        </AsyncState>
      </>
    );
  }
  const timeline = executionTimeline(attempt.status).map((item, index) => ({
    ...item,
    title: [
      t("executions.phaseQueued"),
      t("executions.phaseProvider"),
      t("executions.phaseReconcile"),
    ][index],
  }));
  return (
    <>
      <PageTitle
        title={t("executions.detailTitle", { id: attempt.id })}
        parent={{ label: t("nav.executions"), to: "/executions" }}
      />
      <PageLayout mode="list">
        <PageToolbar
          label={t("common.actions")}
          leading={
            <StateBadge value={attempt.status} label={label(attempt.status)} />
          }
        />
        <div className="space-y-4">
          {attempt.failure_reason && (
            <Alert variant="destructive">
              <AlertTriangle />
              <AlertTitle>{t("executions.failed")}</AlertTitle>
              <AlertDescription>{attempt.failure_reason}</AlertDescription>
            </Alert>
          )}
          <DetailSection title={t("common.status")}>
            <Timeline items={timeline} />
          </DetailSection>
          <DetailSection title={t("executions.attemptIdentity")}>
            <dl className="grid gap-x-6 gap-y-4 sm:grid-cols-2">
              <Fact
                label={t("executions.id")}
                value={
                  <CopyableId label={t("executions.id")} value={attempt.id} />
                }
                mono
              />
              <Fact
                label={t("common.requestedBy")}
                value={attempt.requested_by}
              />
              <Fact
                label={t("common.cleanupTask")}
                value={
                  <CopyableId
                    label={t("cleanup.id")}
                    value={attempt.cleanup_task_id}
                  >
                    <Link
                      to={`/cleanup/${attempt.cleanup_task_id}`}
                      className="text-primary hover:underline"
                    >
                      {attempt.cleanup_task_id}
                    </Link>
                  </CopyableId>
                }
              />
              <Fact
                label={t("common.created")}
                value={formatDate(attempt.created_at)}
              />
              <Fact
                label={t("common.started")}
                value={
                  attempt.started_at ? formatDate(attempt.started_at) : "—"
                }
              />
              <Fact
                label={t("common.finished")}
                value={
                  attempt.finished_at ? formatDate(attempt.finished_at) : "—"
                }
              />
            </dl>
          </DetailSection>
        </div>
      </PageLayout>
    </>
  );
}

function Fact({
  label,
  value,
  mono,
}: {
  label: string;
  value: React.ReactNode;
  mono?: boolean;
}) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd
        className={`mt-1 break-all text-sm ${mono ? "font-mono text-xs" : ""}`}
      >
        {value}
      </dd>
    </div>
  );
}
