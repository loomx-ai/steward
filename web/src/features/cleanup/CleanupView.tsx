import { useMemo, useRef, useState, type MouseEvent } from "react";
import {
  keepPreviousData,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { Plus, Search } from "lucide-react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { toast } from "sonner";
import { listExecutions, listCleanupTasks } from "@/api/client";
import type {
  CleanupTask,
  CleanupSelector,
  ExecutionAttempt,
} from "@/api/types";
import { AsyncState } from "@/components/domain/AsyncState";
import { CursorPagination } from "@/components/domain/CursorPagination";
import { DataTableShell } from "@/components/domain/DataTableShell";
import { StateBadge } from "@/components/domain/StateBadge";
import { PageLayout } from "@/components/patterns/PageLayout";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
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
import { formatDuration } from "@/lib/formatDuration";
import { useLocale } from "@/i18n/LocaleProvider";
import { CleanupTaskBuilder } from "./CleanupTaskBuilder";

const allStatuses = "__all__";
const taskStatuses = [
  "draft",
  "ready",
  "invalidated",
  "executing",
  "pausing",
  "paused",
  "completed",
  "failed",
  "canceled",
] as const;

export function filterCleanupTasks(
  values: CleanupTask[],
  filters: { search: string; status: string },
  requestedByTask: ReadonlyMap<string, string> = new Map(),
) {
  const term = filters.search.trim().toLowerCase();
  return values
    .filter((task) => {
      if (filters.status !== allStatuses && task.status !== filters.status) {
        return false;
      }
      if (!term) return true;
      return [
        task.id,
        task.created_by,
        requestedByTask.get(task.id),
        task.status,
        ...task.selectors.flatMap(selectorSearchValues),
      ].some((value) => value?.toLowerCase().includes(term));
    })
    .sort(
      (left, right) =>
        right.created_at.localeCompare(left.created_at) ||
        right.id.localeCompare(left.id),
    );
}

export function CleanupView() {
  const connection = useRequiredConnection();
  const location = useLocation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { formatDate, formatError, formatNumber, label, t } = useLocale();
  const [formOpen, setFormOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState(allStatuses);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const routeCreateOpen = location.pathname === "/cleanup/new";
  const openForm = (event: MouseEvent<HTMLButtonElement>) => {
    triggerRef.current = event.currentTarget;
    setFormOpen(true);
  };
  const setCreateOpen = (open: boolean) => {
    setFormOpen(open);
    if (!open && routeCreateOpen) navigate("/cleanup", { replace: true });
  };
  const pagination = useCursorPagination(
    `${connection.id}:${search}:${status}`,
  );
  const tasks = useQuery({
    queryKey: [
      "cleanup",
      connection.id,
      pagination.cursor,
      pagination.pageSize,
    ],
    queryFn: () =>
      listCleanupTasks(connection.id, pagination.cursor, pagination.pageSize),
    placeholderData: keepPreviousData,
  });
  const executions = useQuery({
    queryKey: ["cleanup-executions-for-list", connection.id],
    queryFn: () => listExecutions(connection.id, "", 500),
  });
  const sourceRows = tasks.data?.items ?? [];
  const latestExecutionByTask = useMemo(
    () => latestExecutions(executions.data?.items ?? []),
    [executions.data?.items],
  );
  const requestedByTask = useMemo(
    () =>
      new Map(
        [...latestExecutionByTask].map(([cleanupTaskID, attempt]) => [
          cleanupTaskID,
          attempt.requested_by,
        ]),
      ),
    [latestExecutionByTask],
  );
  const rows = useMemo(
    () => filterCleanupTasks(sourceRows, { search, status }, requestedByTask),
    [requestedByTask, search, sourceRows, status],
  );
  const hasFilters = search.trim().length > 0 || status !== allStatuses;

  return (
    <PageLayout mode="list" className="pt-0">
      <DataTableShell
        toolbar={
          <>
            <div className="relative min-w-64 flex-1 self-start md:max-w-xl">
              <Search
                aria-hidden="true"
                className="absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground"
              />
              <Input
                className="h-11 pl-9 sm:h-9"
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                placeholder={t("cleanup.searchTasks")}
                aria-label={t("cleanup.searchTasks")}
              />
            </div>
            <Select value={status} onValueChange={setStatus}>
              <SelectTrigger
                className="h-11 w-full self-start sm:h-9 sm:w-40"
                aria-label={t("common.statusFilter")}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent align="start">
                <SelectItem value={allStatuses}>
                  {t("common.allStatuses")}
                </SelectItem>
                {taskStatuses.map((value) => (
                  <SelectItem key={value} value={value}>
                    {label(value)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Button
              type="button"
              className="ml-auto h-11 w-full sm:h-9 sm:w-auto"
              onClick={openForm}
            >
              <Plus />
              {t("cleanup.new")}
            </Button>
          </>
        }
        pagination={
          sourceRows.length > 0 ? (
            <CursorPagination
              page={pagination.page}
              pageCount={pagination.pageCount}
              hasNextPage={Boolean(tasks.data?.next_cursor)}
              pending={tasks.isFetching}
              pageSize={pagination.pageSize}
              onPrevious={pagination.goPrevious}
              onNext={() => pagination.goNext(tasks.data?.next_cursor ?? "")}
              onPageSelect={pagination.goToPage}
              onPageSizeChange={pagination.setPageSize}
              labels={{
                page: (page) => t("common.page", { page }),
                pageSize: t("common.pageSize"),
                previous: t("common.previous"),
                next: t("common.next"),
              }}
            />
          ) : undefined
        }
      >
        <AsyncState
          pending={tasks.isPending}
          error={tasks.error}
          empty={!tasks.isPending && rows.length === 0}
          onRetry={() => void tasks.refetch()}
          formatError={formatError}
          labels={{
            empty: hasFilters ? t("cleanup.noMatches") : t("cleanup.empty"),
            retry: t("shell.retry"),
            failed: t("shell.routeFailure"),
            loading: t("cleanup.loading"),
          }}
          emptyAction={
            !hasFilters ? (
              <Button type="button" size="sm" onClick={openForm}>
                {t("cleanup.createFirst")}
              </Button>
            ) : undefined
          }
        >
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("cleanup.task")}</TableHead>
                <TableHead>{t("common.status")}</TableHead>
                <TableHead>{t("cleanup.scope")}</TableHead>
                <TableHead>{t("cleanup.resources")}</TableHead>
                <TableHead>{t("common.requestedBy")}</TableHead>
                <TableHead>{t("common.created")}</TableHead>
                <TableHead>{t("cleanup.duration")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((task) => {
                const attempt = latestExecutionByTask.get(task.id);
                return (
                  <TableRow key={task.id}>
                    <TableCell>
                      <Link
                        className="font-mono text-xs text-info underline-offset-4 hover:underline"
                        to={`/cleanup/${encodeURIComponent(task.id)}`}
                      >
                        {task.id}
                      </Link>
                    </TableCell>
                    <TableCell>
                      <StateBadge
                        value={task.status}
                        label={label(task.status)}
                      />
                    </TableCell>
                    <TableCell className="max-w-64 truncate text-sm">
                      {cleanupScope(task, t)}
                    </TableCell>
                    <TableCell>
                      {formatNumber(task.resolved_asset_ids.length)}
                    </TableCell>
                    <TableCell>
                      {attempt?.requested_by || task.created_by}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {formatDate(task.created_at)}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {formatDuration(attempt?.duration_ms)}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </AsyncState>
      </DataTableShell>
      {(formOpen || routeCreateOpen) && (
        <CleanupTaskBuilder
          open
          onOpenChange={setCreateOpen}
          returnFocusRef={triggerRef}
          onCreated={(id) => {
            setFormOpen(false);
            toast.success(t("cleanup.created"));
            void queryClient.invalidateQueries({
              queryKey: ["cleanup", connection.id],
            });
            navigate(`/cleanup/${encodeURIComponent(id)}`);
          }}
        />
      )}
    </PageLayout>
  );
}

function latestExecutions(values: ExecutionAttempt[]) {
  const result = new Map<string, ExecutionAttempt>();
  for (const value of values) {
    const existing = result.get(value.cleanup_task_id);
    if (!existing || existing.created_at.localeCompare(value.created_at) < 0) {
      result.set(value.cleanup_task_id, value);
    }
  }
  return result;
}

function cleanupScope(
  task: CleanupTask,
  t: (
    key: "cleanup.wholeAccount" | "cleanup.resourceScope",
    values?: { count: number },
  ) => string,
) {
  if (task.selectors.length === 1 && task.selectors[0]?.kind === "connection") {
    return t("cleanup.wholeAccount");
  }
  if (task.selectors.length === 1 && task.selectors[0]?.display_name?.trim()) {
    return task.selectors[0].display_name.trim();
  }
  return t("cleanup.resourceScope", { count: task.selectors.length });
}

function selectorSearchValues(selector: CleanupSelector) {
  switch (selector.kind) {
    case "connection":
      return [selector.display_name, selector.connection_id];
    case "scope":
      return [
        selector.display_name,
        selector.connection_id,
        selector.scope_id,
        selector.scope_kind,
      ];
    case "group":
      return [
        selector.display_name,
        selector.connection_id,
        selector.scope_id,
        selector.group_key,
      ];
    case "asset":
      return [selector.display_name, selector.asset_id];
  }
}
