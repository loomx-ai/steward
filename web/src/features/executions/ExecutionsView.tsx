import { useEffect, useState } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { ExternalLink } from "lucide-react";
import { Link, useNavigate } from "react-router-dom";
import { listExecutions } from "@/api/client";
import type { ExecutionAttempt } from "@/api/types";
import { useWorkspace } from "@/app/WorkspaceContext";
import { AsyncState } from "@/components/domain/AsyncState";
import { CursorPagination } from "@/components/domain/CursorPagination";
import { DataTableShell } from "@/components/domain/DataTableShell";
import { StateBadge } from "@/components/domain/StateBadge";
import { PageLayout } from "@/components/patterns/PageLayout";
import { RowActions } from "@/components/patterns/RowActions";
import { Button } from "@/components/ui/button";
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
import { isExecutionActive } from "./ExecutionDetail";

export function ExecutionsView() {
  const navigate = useNavigate();
  const connection = useRequiredConnection();
  const { openInspector, closeInspector } = useWorkspace();
  const { formatDate, formatError, label, t } = useLocale();
  const [focused, setFocused] = useState<ExecutionAttempt>();
  const pagination = useCursorPagination(connection.id);
  const executions = useQuery({
    queryKey: [
      "executions",
      connection.id,
      pagination.cursor,
      pagination.pageSize,
    ],
    queryFn: () =>
      listExecutions(connection.id, pagination.cursor, pagination.pageSize),
    placeholderData: keepPreviousData,
    refetchInterval: (query) =>
      query.state.data?.items.some((attempt) =>
        isExecutionActive(attempt.status),
      )
        ? 3000
        : false,
  });
  const rows = executions.data?.items ?? [];

  const inspectExecution = (
    attempt: ExecutionAttempt,
    trigger: HTMLElement,
  ) => {
    trigger.focus();
    setFocused(attempt);
    openInspector({
      title: t("executions.detailTitle", { id: attempt.id }),
      description: attempt.cleanup_task_id,
      body: (
        <div className="space-y-4 text-sm">
          <StateBadge value={attempt.status} label={label(attempt.status)} />
          <dl className="space-y-3">
            <Fact
              label={t("common.cleanupTask")}
              value={attempt.cleanup_task_id}
              mono
            />
            <Fact
              label={t("common.requestedBy")}
              value={attempt.requested_by}
            />
            <Fact
              label={t("common.created")}
              value={formatDate(attempt.created_at)}
            />
          </dl>
          <Button
            className="w-full"
            onClick={() => navigate(`/executions/${attempt.id}`)}
          >
            {t("common.details")}
          </Button>
        </div>
      ),
    });
  };

  useEffect(() => {
    setFocused(undefined);
    closeInspector({ restoreFocus: false });
  }, [closeInspector, pagination.cursor]);

  return (
    <PageLayout mode="list">
      <AsyncState
        pending={executions.isPending}
        error={executions.error}
        empty={!executions.isPending && rows.length === 0}
        onRetry={() => void executions.refetch()}
        formatError={formatError}
        labels={{
          empty: t("executions.empty"),
          retry: t("shell.retry"),
          failed: t("shell.routeFailure"),
          loading: t("executions.loading"),
        }}
      >
        <DataTableShell
          pagination={
            <CursorPagination
              page={pagination.page}
              pageCount={pagination.pageCount}
              hasNextPage={Boolean(executions.data?.next_cursor)}
              pending={executions.isFetching}
              pageSize={pagination.pageSize}
              onPrevious={pagination.goPrevious}
              onNext={() =>
                pagination.goNext(executions.data?.next_cursor ?? "")
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
          }
        >
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("executions.execution")}</TableHead>
                <TableHead>{t("executions.cleanupTask")}</TableHead>
                <TableHead>{t("common.status")}</TableHead>
                <TableHead>{t("common.requestedBy")}</TableHead>
                <TableHead>{t("common.created")}</TableHead>
                <TableHead>{t("common.finished")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((attempt) => (
                <TableRow
                  key={attempt.id}
                  aria-selected={focused?.id === attempt.id}
                  className="group cursor-pointer"
                  onClick={(event) => {
                    const trigger =
                      event.currentTarget.querySelector<HTMLElement>(
                        "[data-row-inspector-trigger]",
                      );
                    if (trigger) inspectExecution(attempt, trigger);
                  }}
                  onDoubleClick={() => navigate(`/executions/${attempt.id}`)}
                >
                  <TableCell>
                    <div className="flex items-center justify-between gap-2">
                      <Button
                        variant="link"
                        className="font-mono text-xs"
                        data-row-inspector-trigger
                        aria-label={`${t("common.details")}: ${attempt.id}`}
                        onClick={(event) => {
                          event.stopPropagation();
                          inspectExecution(attempt, event.currentTarget);
                        }}
                        onDoubleClick={(event) => event.stopPropagation()}
                      >
                        {attempt.id}
                      </Button>
                      <RowActions className="shrink-0">
                        <Button asChild variant="link" size="icon-xs">
                          <Link
                            to={`/executions/${attempt.id}`}
                            aria-label={t("executions.detailTitle", {
                              id: attempt.id,
                            })}
                            onClick={(event) => event.stopPropagation()}
                            onDoubleClick={(event) => event.stopPropagation()}
                          >
                            <ExternalLink />
                          </Link>
                        </Button>
                      </RowActions>
                    </div>
                  </TableCell>
                  <TableCell className="font-mono text-xs">
                    {attempt.cleanup_task_id}
                  </TableCell>
                  <TableCell>
                    <StateBadge
                      value={attempt.status}
                      label={label(attempt.status)}
                    />
                  </TableCell>
                  <TableCell>{attempt.requested_by}</TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    {formatDate(attempt.created_at)}
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    {attempt.finished_at
                      ? formatDate(attempt.finished_at)
                      : "—"}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </DataTableShell>
      </AsyncState>
    </PageLayout>
  );
}

function Fact({
  label,
  value,
  mono,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  return (
    <div>
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className={`mt-1 break-all ${mono ? "font-mono text-xs" : ""}`}>
        {value}
      </dd>
    </div>
  );
}
