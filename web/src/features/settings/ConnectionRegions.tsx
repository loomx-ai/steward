import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
  type KeyboardEvent,
} from "react";
import {
  keepPreviousData,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import {
  Check,
  ChevronUp,
  MapPinned,
  MoreHorizontal,
  Pencil,
  Plus,
  RefreshCw,
  RotateCcw,
  Trash2,
  X,
} from "lucide-react";
import {
  addConnectionRegion,
  excludeConnectionRegion,
  getJob,
  listConnectionRegions,
  refreshConnectionRegions,
  restoreConnectionRegion,
  updateConnectionRegion,
} from "@/api/client";
import type {
  CloudConnection,
  ConnectionRegion,
  RegionLifecycle,
} from "@/api/types";
import { StateBadge } from "@/components/domain/StateBadge";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { useLocale } from "@/i18n/LocaleProvider";
import { compareRegionIDs } from "@/lib/regionOrder";

type Filter = "all" | RegionLifecycle;

export function ConnectionRegions({
  connection,
  panelID,
  onCollapse,
}: {
  connection: CloudConnection;
  panelID: string;
  onCollapse: () => void;
}) {
  const queryClient = useQueryClient();
  const { formatDate, formatError, label, t } = useLocale();
  const [filter, setFilter] = useState<Filter>("all");
  const [search, setSearch] = useState("");
  const [adding, setAdding] = useState(false);
  const [regionID, setRegionID] = useState("");
  const [regionName, setRegionName] = useState("");
  const [editingID, setEditingID] = useState<string>();
  const [editingName, setEditingName] = useState("");
  const [refreshJobID, setRefreshJobID] = useState<string>();
  const handledRefreshJobID = useRef<string | undefined>(undefined);
  const addTriggerRef = useRef<HTMLButtonElement>(null);
  const connectionID = connection.id;
  const normalizedSearch = search.trim();
  const regions = useQuery({
    queryKey: ["connection-regions", connectionID, filter, normalizedSearch],
    queryFn: ({ signal }) =>
      loadConnectionRegions(
        connectionID,
        {
          lifecycle: filter === "all" ? undefined : filter,
          query: normalizedSearch || undefined,
        },
        signal,
      ),
    enabled: !!connectionID,
    placeholderData: keepPreviousData,
  });
  const invalidate = useCallback(async () => {
    await Promise.all([
      queryClient.invalidateQueries({
        queryKey: ["connection-regions", connectionID],
      }),
      queryClient.invalidateQueries({ queryKey: ["connections"] }),
    ]);
  }, [connectionID, queryClient]);
  const refreshJob = useQuery({
    queryKey: ["region-refresh-job", connectionID, refreshJobID],
    queryFn: () => getJob(connectionID, refreshJobID ?? ""),
    enabled: Boolean(refreshJobID),
    refetchInterval: (query) =>
      query.state.error || isTerminalJobStatus(query.state.data?.status)
        ? false
        : 1000,
  });
  useEffect(() => {
    const job = refreshJob.data;
    if (
      !job ||
      !isTerminalJobStatus(job.status) ||
      handledRefreshJobID.current === job.id
    ) {
      return;
    }
    handledRefreshJobID.current = job.id;
    void invalidate();
  }, [invalidate, refreshJob.data]);

  const refresh = useMutation({
    mutationFn: () => refreshConnectionRegions(connectionID),
    onMutate: () => {
      setRefreshJobID(undefined);
    },
    onSuccess: (job) => {
      handledRefreshJobID.current = undefined;
      setRefreshJobID(job.job_id);
    },
  });
  const add = useMutation({
    mutationFn: () =>
      addConnectionRegion(connectionID, {
        region_id: regionID.trim(),
        name: regionName.trim(),
      }),
    onSuccess: async () => {
      setRegionID("");
      setRegionName("");
      setAdding(false);
      await invalidate();
    },
  });
  const update = useMutation({
    mutationFn: ({
      id,
      input,
    }: {
      id: string;
      input: Parameters<typeof updateConnectionRegion>[2];
    }) => updateConnectionRegion(connectionID, id, input),
    onSuccess: async () => {
      setEditingID(undefined);
      await invalidate();
    },
  });
  const exclude = useMutation({
    mutationFn: (id: string) => excludeConnectionRegion(connectionID, id),
    onSuccess: invalidate,
  });
  const restore = useMutation({
    mutationFn: (id: string) => restoreConnectionRegion(connectionID, id),
    onSuccess: invalidate,
  });
  const mutationError =
    refresh.error ??
    refreshJob.error ??
    add.error ??
    update.error ??
    exclude.error ??
    restore.error;
  const refreshFailure =
    refreshJob.data &&
    (refreshJob.data.status === "failed" ||
      refreshJob.data.status === "canceled")
      ? refreshJob.data.last_error?.trim() || t("regions.refreshFailed")
      : "";
  const rows = regions.data ?? [];
  const activeCount =
    connection.active_region_count ??
    rows.filter((region) => region.lifecycle === "active").length;
  const retiredCount =
    connection.retired_region_count ??
    rows.filter((region) => region.lifecycle === "retired").length;
  const excludedCount =
    connection.excluded_region_count ??
    rows.filter((region) => region.lifecycle === "excluded").length;
  const counts = {
    all: activeCount + retiredCount + excludedCount,
    active: activeCount,
    retired: retiredCount,
    excluded: excludedCount,
  };
  const submitAdd = (event: FormEvent) => {
    event.preventDefault();
    if (regionID.trim() && regionName.trim()) add.mutate();
  };
  const closeAddForm = () => {
    setAdding(false);
    addTriggerRef.current?.focus();
  };
  const handleAddKeyDown = (event: KeyboardEvent<HTMLFormElement>) => {
    if (event.key !== "Escape") return;
    event.preventDefault();
    closeAddForm();
  };

  const titleID = `${panelID}-title`;
  const refreshing =
    refresh.isPending ||
    (Boolean(refreshJobID) &&
      !refreshJob.error &&
      !isTerminalJobStatus(refreshJob.data?.status));
  const refreshLabel = refreshing
    ? t("regions.refreshing")
    : t("regions.refresh");

  return (
    <TooltipProvider>
      <section
        id={panelID}
        role="region"
        aria-labelledby={titleID}
        className="border-t bg-muted/15 px-4 py-3"
      >
        <div className="space-y-3">
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
            <h3 id={titleID} className="truncate text-sm font-semibold">
              {t("regions.title")} · {connection.name}
            </h3>
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
              <span>
                {connection?.last_region_refresh_at
                  ? t("regions.lastRefresh", {
                      time: formatDate(connection.last_region_refresh_at),
                    })
                  : t("regions.neverRefreshed")}
              </span>
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-sm"
                    aria-label={t("regions.collapse")}
                    onClick={onCollapse}
                  >
                    <ChevronUp />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>{t("regions.collapse")}</TooltipContent>
              </Tooltip>
            </div>
          </div>
          {mutationError && (
            <Alert variant="destructive">
              <AlertDescription>{formatError(mutationError)}</AlertDescription>
            </Alert>
          )}
          {refreshFailure && (
            <Alert variant="destructive">
              <AlertDescription>{refreshFailure}</AlertDescription>
            </Alert>
          )}
          <div className="flex flex-col gap-2 lg:flex-row lg:items-center lg:justify-between">
            <div
              className="flex flex-wrap items-center gap-1"
              role="group"
              aria-label={t("regions.filter")}
            >
              {(["all", "active", "retired", "excluded"] as const).map(
                (value) => (
                  <Button
                    key={value}
                    type="button"
                    variant={filter === value ? "secondary" : "ghost"}
                    size="sm"
                    aria-pressed={filter === value}
                    onClick={() => setFilter(value)}
                  >
                    {t(`regions.${value}`)} {counts[value]}
                  </Button>
                ),
              )}
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    type="button"
                    variant="outline"
                    size="icon"
                    aria-label={refreshLabel}
                    aria-busy={refreshing}
                    onClick={() => refresh.mutate()}
                    disabled={refreshing}
                  >
                    <RefreshCw className={refreshing ? "animate-spin" : ""} />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>{refreshLabel}</TooltipContent>
              </Tooltip>
              <Button
                ref={addTriggerRef}
                variant={adding ? "ghost" : "default"}
                aria-expanded={adding}
                aria-controls={`${panelID}-add-form`}
                onClick={() => (adding ? closeAddForm() : setAdding(true))}
              >
                {adding ? <X /> : <Plus />}
                {adding ? t("regions.cancelAdd") : t("regions.add")}
              </Button>
            </div>
          </div>

          {adding && (
            <form
              id={`${panelID}-add-form`}
              className="grid gap-3 rounded-lg border bg-muted/20 p-4 sm:grid-cols-[1fr_1fr_auto] sm:items-end"
              onSubmit={submitAdd}
              onKeyDown={handleAddKeyDown}
            >
              <div className="space-y-2">
                <Label htmlFor="manual-region-id">{t("regions.id")}</Label>
                <Input
                  id="manual-region-id"
                  value={regionID}
                  onChange={(event) => setRegionID(event.target.value)}
                  placeholder="cn-hangzhou"
                  autoFocus
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="manual-region-name">{t("regions.name")}</Label>
                <Input
                  id="manual-region-name"
                  value={regionName}
                  onChange={(event) => setRegionName(event.target.value)}
                  placeholder={t("regions.namePlaceholder")}
                />
              </div>
              <Button
                type="submit"
                disabled={
                  add.isPending || !regionID.trim() || !regionName.trim()
                }
              >
                {t("common.add")}
              </Button>
            </form>
          )}

          <div>
            <Input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder={t("regions.search")}
              aria-label={t("regions.search")}
              className="sm:max-w-md"
            />
          </div>

          {regions.isPending ? (
            <div className="space-y-2">
              <Skeleton className="h-16 w-full" />
              <Skeleton className="h-16 w-full" />
              <Skeleton className="h-16 w-full" />
            </div>
          ) : regions.error ? (
            <Alert variant="destructive">
              <AlertDescription>{formatError(regions.error)}</AlertDescription>
            </Alert>
          ) : rows.length === 0 ? (
            <div className="flex min-h-40 flex-col items-center justify-center rounded-lg border border-dashed text-center">
              <MapPinned className="mb-3 size-7 text-muted-foreground" />
              <p className="text-sm font-medium">{t("regions.empty")}</p>
              <p className="mt-1 text-xs text-muted-foreground">
                {t("regions.emptyDetail")}
              </p>
            </div>
          ) : (
            <div className="divide-y border-y">
              {rows.map((region) => (
                <RegionRow
                  key={region.id}
                  region={region}
                  editing={editingID === region.region_id}
                  editingName={editingName}
                  busy={
                    update.isPending || exclude.isPending || restore.isPending
                  }
                  onEditingName={setEditingName}
                  onStartEdit={() => {
                    setEditingID(region.region_id);
                    setEditingName(region.name);
                  }}
                  onCancelEdit={() => setEditingID(undefined)}
                  onSaveName={() =>
                    update.mutate({
                      id: region.region_id,
                      input: { name: editingName.trim() },
                    })
                  }
                  onResetName={() =>
                    update.mutate({
                      id: region.region_id,
                      input: { reset_name: true },
                    })
                  }
                  onLifecycle={(lifecycle) =>
                    update.mutate({
                      id: region.region_id,
                      input: { lifecycle },
                    })
                  }
                  onExclude={() => exclude.mutate(region.region_id)}
                  onRestore={() => restore.mutate(region.region_id)}
                  label={label}
                  t={t}
                />
              ))}
            </div>
          )}
        </div>
      </section>
    </TooltipProvider>
  );
}

async function loadConnectionRegions(
  connectionID: string,
  filters: { lifecycle?: RegionLifecycle; query?: string },
  signal?: AbortSignal,
) {
  const page = await listConnectionRegions(connectionID, {
    lifecycle: filters.lifecycle,
    query: filters.query,
    signal,
  });
  return page.items.sort((left, right) =>
    compareRegionIDs(left.region_id, right.region_id),
  );
}

function isTerminalJobStatus(status?: string) {
  return status === "succeeded" || status === "failed" || status === "canceled";
}

function RegionRow({
  region,
  editing,
  editingName,
  busy,
  onEditingName,
  onStartEdit,
  onCancelEdit,
  onSaveName,
  onResetName,
  onLifecycle,
  onExclude,
  onRestore,
  label,
  t,
}: {
  region: ConnectionRegion;
  editing: boolean;
  editingName: string;
  busy: boolean;
  onEditingName: (name: string) => void;
  onStartEdit: () => void;
  onCancelEdit: () => void;
  onSaveName: () => void;
  onResetName: () => void;
  onLifecycle: (lifecycle: "active" | "retired") => void;
  onExclude: () => void;
  onRestore: () => void;
  label: (value: string) => string;
  t: ReturnType<typeof useLocale>["t"];
}) {
  return (
    <article className="grid gap-3 px-3 py-2.5 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center">
      <div className="min-w-0">
        {editing ? (
          <div className="flex max-w-md gap-2">
            <Input
              value={editingName}
              onChange={(event) => onEditingName(event.target.value)}
              aria-label={t("regions.name")}
              autoFocus
            />
            <Button
              size="icon"
              onClick={onSaveName}
              disabled={!editingName.trim()}
              aria-label={t("regions.saveName")}
            >
              <Check />
            </Button>
            <Button
              size="icon"
              variant="ghost"
              onClick={onCancelEdit}
              aria-label={t("common.cancel")}
            >
              <X />
            </Button>
          </div>
        ) : (
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="truncate text-sm font-medium">{region.name}</h3>
            {region.lifecycle !== "active" && (
              <StateBadge
                value={region.lifecycle}
                label={label(region.lifecycle)}
              />
            )}
            {region.origin === "manual" && (
              <Badge variant="outline">{t("regions.manual")}</Badge>
            )}
          </div>
        )}
        <p className="mt-1 font-mono text-xs text-muted-foreground">
          {region.region_id}
        </p>
      </div>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            variant="ghost"
            size="icon"
            disabled={busy}
            aria-label={t("common.actions")}
          >
            <MoreHorizontal />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {region.lifecycle !== "excluded" && (
            <>
              <DropdownMenuItem onSelect={onStartEdit}>
                <Pencil /> {t("regions.rename")}
              </DropdownMenuItem>
              {region.name_override && (
                <DropdownMenuItem onSelect={onResetName}>
                  <RotateCcw /> {t("regions.resetName")}
                </DropdownMenuItem>
              )}
              <DropdownMenuSeparator />
              {region.lifecycle === "active" ? (
                <DropdownMenuItem onSelect={() => onLifecycle("retired")}>
                  {t("regions.retire")}
                </DropdownMenuItem>
              ) : (
                <DropdownMenuItem onSelect={() => onLifecycle("active")}>
                  {t("regions.activate")}
                </DropdownMenuItem>
              )}
              <DropdownMenuItem variant="destructive" onSelect={onExclude}>
                <Trash2 /> {t("regions.exclude")}
              </DropdownMenuItem>
            </>
          )}
          {region.lifecycle === "excluded" && (
            <DropdownMenuItem onSelect={onRestore}>
              <RotateCcw /> {t("regions.restore")}
            </DropdownMenuItem>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
    </article>
  );
}
