import { useMemo, useRef, useState } from "react";
import {
  keepPreviousData,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { Play, RefreshCw, Search } from "lucide-react";
import { Link, useNavigate } from "react-router-dom";
import { toast } from "sonner";
import {
  listConnectionRegions,
  listProviderCatalog,
  listScans,
} from "@/api/client";
import { AsyncState } from "@/components/domain/AsyncState";
import { CursorPagination } from "@/components/domain/CursorPagination";
import { DataTableShell } from "@/components/domain/DataTableShell";
import { StateBadge } from "@/components/domain/StateBadge";
import { PageLayout } from "@/components/patterns/PageLayout";
import { Alert, AlertDescription } from "@/components/ui/alert";
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
import { useLocale } from "@/i18n/LocaleProvider";
import { useCursorPagination } from "@/hooks/useCursorPagination";
import { formatDuration } from "@/lib/formatDuration";
import { CreateScanDialog } from "./CreateScanDialog";
import { scanStatusLabel } from "./scanStatus";

const allStatuses = "__all__";
const scanStatuses = [
  "pending",
  "running",
  "pausing",
  "paused",
  "canceling",
  "canceled",
  "reconciling",
  "succeeded",
  "partial",
  "failed",
] as const;

export function filterScanTasks(
  values: Awaited<ReturnType<typeof listScans>>["items"],
  filters: { search: string; status: string },
) {
  const term = filters.search.trim().toLowerCase();
  return values.filter((task) => {
    if (filters.status !== allStatuses && task.status !== filters.status) {
      return false;
    }
    if (!term) return true;
    return [
      task.id,
      task.requested_by,
      task.status,
      task.scope_mode,
      ...(task.resource_kind_ids ?? []),
      ...task.targets.flatMap((target) => [
        target.region_id,
        target.region_name,
        target.native_id,
        target.name,
      ]),
    ].some((value) => value?.toLowerCase().includes(term));
  });
}

export function ScansView() {
  const connection = useRequiredConnection();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { formatDate, formatError, formatNumber, label, t } = useLocale();
  const [formOpen, setFormOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState(allStatuses);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const pagination = useCursorPagination(
    `${connection.id}:${search}:${status}`,
  );
  const scans = useQuery({
    queryKey: ["scans", connection.id, pagination.cursor, pagination.pageSize],
    queryFn: ({ signal }) =>
      listScans(connection.id, pagination.cursor, pagination.pageSize, signal),
    placeholderData: keepPreviousData,
  });
  const regions = useQuery({
    queryKey: ["connection-regions", connection.id, "active"],
    queryFn: () =>
      listConnectionRegions(connection.id, { lifecycle: "active" }),
    enabled: formOpen,
  });
  const catalog = useQuery({
    queryKey: ["catalog"],
    queryFn: listProviderCatalog,
    enabled: formOpen,
  });
  const sourceRows = scans.data?.items ?? [];
  const rows = useMemo(
    () => filterScanTasks(sourceRows, { search, status }),
    [search, sourceRows, status],
  );
  const hasFilters = search.trim().length > 0 || status !== allStatuses;
  const supportingError = formOpen ? (regions.error ?? catalog.error) : null;
  return (
    <PageLayout mode="list" className="pt-0">
      <div className="space-y-4">
        {supportingError && (
          <Alert variant="destructive">
            <AlertDescription>{formatError(supportingError)}</AlertDescription>
          </Alert>
        )}
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
                  placeholder={t("scans.searchTasks")}
                  aria-label={t("scans.searchTasks")}
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
                  {scanStatuses.map((value) => (
                    <SelectItem key={value} value={value}>
                      {scanStatusLabel(
                        value,
                        label(value),
                        t("scans.inProgress"),
                      )}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <div className="ml-auto flex w-full items-center gap-2 sm:w-auto">
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  className="size-11 sm:size-9"
                  aria-label={
                    scans.isFetching
                      ? t("scans.refreshing")
                      : t("scans.refresh")
                  }
                  aria-busy={scans.isFetching}
                  title={
                    scans.isFetching
                      ? t("scans.refreshing")
                      : t("scans.refresh")
                  }
                  disabled={scans.isFetching}
                  onClick={() => void scans.refetch()}
                >
                  <RefreshCw
                    className={
                      scans.isFetching ? "motion-safe:animate-spin" : ""
                    }
                  />
                </Button>
                <Button
                  ref={triggerRef}
                  className="h-11 min-w-0 flex-1 sm:h-9 sm:flex-none"
                  onClick={() => setFormOpen(true)}
                >
                  <Play />
                  {t("scans.start")}
                </Button>
              </div>
            </>
          }
          pagination={
            sourceRows.length > 0 ? (
              <CursorPagination
                page={pagination.page}
                pageCount={pagination.pageCount}
                hasNextPage={Boolean(scans.data?.next_cursor)}
                pending={scans.isFetching}
                pageSize={pagination.pageSize}
                onPrevious={pagination.goPrevious}
                onNext={() => pagination.goNext(scans.data?.next_cursor ?? "")}
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
            pending={scans.isPending}
            error={scans.error}
            empty={!scans.isPending && rows.length === 0}
            onRetry={() => void scans.refetch()}
            formatError={formatError}
            labels={{
              empty: hasFilters ? t("scans.noMatches") : t("scans.empty"),
              retry: t("shell.retry"),
              failed: t("shell.routeFailure"),
              loading: t("scans.loading"),
            }}
          >
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("scans.run")}</TableHead>
                  <TableHead>{t("common.status")}</TableHead>
                  <TableHead>{t("scans.scope")}</TableHead>
                  <TableHead>{t("scans.resources")}</TableHead>
                  <TableHead>{t("common.requestedBy")}</TableHead>
                  <TableHead>{t("common.created")}</TableHead>
                  <TableHead>{t("scans.duration")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((task) => (
                  <TableRow key={task.id}>
                    <TableCell>
                      <Link
                        className="font-mono text-xs text-info underline-offset-4 hover:underline"
                        to={`/scans/${encodeURIComponent(task.id)}`}
                      >
                        {task.id}
                      </Link>
                    </TableCell>
                    <TableCell>
                      <StateBadge
                        value={task.status}
                        label={scanStatusLabel(
                          task.status,
                          label(task.status),
                          t("scans.inProgress"),
                        )}
                      />
                    </TableCell>
                    <TableCell className="text-sm">
                      {task.scope_mode === "all_active_regions"
                        ? t("scans.allRegions")
                        : t("scans.partialRegions")}
                    </TableCell>
                    <TableCell>
                      {formatNumber(task.progress.resource_count)}
                    </TableCell>
                    <TableCell>{task.requested_by}</TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {formatDate(task.created_at)}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {formatDuration(task.duration_ms)}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </AsyncState>
        </DataTableShell>
      </div>
      <CreateScanDialog
        open={formOpen}
        onOpenChange={setFormOpen}
        returnFocusRef={triggerRef}
        connection={connection}
        regions={regions.data?.items ?? []}
        bundles={catalog.data ?? []}
        onCreated={(id) => {
          setFormOpen(false);
          toast.success(t("scans.scheduled"));
          void queryClient.invalidateQueries({
            queryKey: ["scans", connection.id],
          });
          navigate(`/scans/${encodeURIComponent(id)}`);
        }}
      />
    </PageLayout>
  );
}
