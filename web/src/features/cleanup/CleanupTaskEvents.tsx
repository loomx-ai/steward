import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Search } from "lucide-react";
import {
  getCleanupTaskLogs,
  listProviderCatalog,
  streamCleanupTaskEvents,
} from "@/api/client";
import type {
  Asset,
  CleanupTask,
  JobLog,
  CleanupTaskAggregate,
  ResourceKind,
} from "@/api/types";
import { ResourceKindPicker } from "@/components/domain/ResourceKindPicker";
import {
  compareResourceKindOptionsByProduct,
  resourceProductName,
  resourceTypeName,
} from "@/components/domain/resourceKindLabel";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useLocale } from "@/i18n/LocaleProvider";
import { cn } from "@/lib/utils";
import {
  formatCloudPayload,
  formatScanLogMessage,
  formatTerminalTimestamp,
} from "@/features/scans/ScanTaskEvents";

export function CleanupTaskEvents({
  connectionID,
  taskID,
  live,
  assetsByID,
  resourceIDFilter,
  onResourceIDFilterChange,
}: {
  connectionID: string;
  taskID: string;
  live: boolean;
  assetsByID: ReadonlyMap<string, Asset>;
  resourceIDFilter?: string;
  onResourceIDFilterChange?: (value: string) => void;
}) {
  const [logs, setLogs] = useState<JobLog[]>([]);
  const [error, setError] = useState<unknown>();
  const [historyCursor, setHistoryCursor] = useState("");
  const [loadingEarlier, setLoadingEarlier] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [historyVersion, setHistoryVersion] = useState(0);
  const [localResourceIDFilter, setLocalResourceIDFilter] = useState("");
  const [resourceKindIDs, setResourceKindIDs] = useState<string[]>([]);
  const terminalRef = useRef<HTMLDivElement>(null);
  const followingRef = useRef(true);
  const historyControllerRef = useRef<AbortController | null>(null);
  const pendingScrollAdjustmentRef = useRef<{
    height: number;
    top: number;
  } | null>(null);
  const queryClient = useQueryClient();
  const { formatError, locale, t } = useLocale();
  const activeResourceIDFilter =
    resourceIDFilter === undefined ? localResourceIDFilter : resourceIDFilter;
  const debouncedResourceIDFilter = useDebounced(activeResourceIDFilter, 300);
  const hasActiveFilters =
    activeResourceIDFilter.trim() !== "" || resourceKindIDs.length > 0;
  const setResourceIDFilter =
    onResourceIDFilterChange ?? setLocalResourceIDFilter;
  const catalog = useQuery({
    queryKey: ["catalog"],
    queryFn: listProviderCatalog,
  });
  const resourceKindsByID = useMemo(
    () =>
      new Map(
        (catalog.data ?? []).flatMap((bundle) =>
          bundle.kinds.map((kind) => [kind.id, kind] as const),
        ),
      ),
    [catalog.data],
  );
  const resourceKindOptions = useMemo(() => {
    const available = new Map(
      [...assetsByID.values()].map((asset) => [
        asset.resource_kind_id,
        {
          id: asset.resource_kind_id,
          nativeType: asset.identity.native_type,
          kind: resourceKindsByID.get(asset.resource_kind_id),
        },
      ]),
    );
    return [...available.values()]
      .map(({ id, nativeType, kind }) => {
        const product = resourceProductName(kind?.native_type || nativeType);
        return {
          value: id,
          label: resourceTypeName(
            kind?.native_type || nativeType,
            kind?.display_name || nativeType,
            kind?.display_names,
            locale,
          ),
          tag: product === "—" ? undefined : product,
          keywords: [
            product,
            nativeType,
            ...Object.values(kind?.display_names ?? {}),
          ],
        };
      })
      .sort((left, right) =>
        compareResourceKindOptionsByProduct(left, right, locale),
      );
  }, [assetsByID, locale, resourceKindsByID]);
  useEffect(() => {
    const filters = {
      resourceID: debouncedResourceIDFilter,
      resourceKindIDs,
    };
    const controller = new AbortController();
    let cursor = "";
    let ended = false;
    setLogs([]);
    setLoadingEarlier(false);
    setRefreshing(true);
    setError(undefined);
    followingRef.current = true;
    pendingScrollAdjustmentRef.current = null;

    const receive = (event: {
      type: "snapshot" | "log" | "end";
      data: CleanupTask | JobLog;
      id?: string;
    }) => {
      if (event.id) cursor = event.id;
      if (event.type === "log") {
        setLogs((current) => mergeLogs(current, [event.data as JobLog]));
        return;
      }
      if (event.type === "end") ended = true;
      queryClient.setQueryData<CleanupTaskAggregate>(
        ["cleanup-task", connectionID, taskID],
        (current) =>
          current ? { ...current, task: event.data as CleanupTask } : current,
      );
      void queryClient.invalidateQueries({
        queryKey: ["cleanup", connectionID],
      });
      void queryClient.invalidateQueries({
        queryKey: ["cleanup-task-executions", connectionID, taskID],
      });
      void queryClient.invalidateQueries({
        queryKey: ["cleanup-actions", connectionID],
      });
    };

    const connect = async () => {
      while (!controller.signal.aborted && !ended) {
        try {
          setError(undefined);
          const latest = await streamCleanupTaskEvents(
            connectionID,
            taskID,
            cursor,
            filters,
            receive,
            controller.signal,
          );
          if (latest) cursor = latest;
          if (ended || controller.signal.aborted) return;
          setError(new Error(t("scans.streamClosed")));
        } catch (reason) {
          if (controller.signal.aborted) return;
          setError(reason);
        }
        await reconnectDelay(controller.signal, 1000);
      }
    };

    const start = async () => {
      try {
        const history = await getCleanupTaskLogs(
          connectionID,
          taskID,
          "",
          filters,
          controller.signal,
        );
        if (controller.signal.aborted) return;
        setLogs(mergeLogs([], history.items));
        setHistoryCursor(history.next_cursor ?? "");
        cursor = history.live_cursor ?? "";
        setHistoryVersion((current) => current + 1);
        setRefreshing(false);
        if (live) await connect();
      } catch (reason) {
        if (!controller.signal.aborted) {
          setRefreshing(false);
          setError(reason);
        }
      }
    };

    void start();
    return () => {
      controller.abort();
      historyControllerRef.current?.abort();
    };
  }, [
    connectionID,
    debouncedResourceIDFilter,
    live,
    queryClient,
    resourceKindIDs,
    t,
    taskID,
  ]);

  useLayoutEffect(() => {
    const terminal = terminalRef.current;
    if (!terminal) return;
    const adjustment = pendingScrollAdjustmentRef.current;
    if (adjustment) {
      terminal.scrollTop =
        adjustment.top + Math.max(0, terminal.scrollHeight - adjustment.height);
      pendingScrollAdjustmentRef.current = null;
      return;
    }
    if (!followingRef.current) return;
    terminal.scrollTop = terminal.scrollHeight;
  }, [error, historyVersion, logs.length]);

  const loadEarlier = async () => {
    if (!historyCursor || loadingEarlier || refreshing) return;
    const terminal = terminalRef.current;
    if (terminal) {
      pendingScrollAdjustmentRef.current = {
        height: terminal.scrollHeight,
        top: terminal.scrollTop,
      };
    }
    followingRef.current = false;
    const controller = new AbortController();
    historyControllerRef.current?.abort();
    historyControllerRef.current = controller;
    setLoadingEarlier(true);
    try {
      const history = await getCleanupTaskLogs(
        connectionID,
        taskID,
        historyCursor,
        {
          resourceID: debouncedResourceIDFilter,
          resourceKindIDs,
        },
        controller.signal,
      );
      if (controller.signal.aborted) return;
      setLogs((current) => mergeLogs(history.items, current));
      setHistoryCursor(history.next_cursor ?? "");
      setHistoryVersion((current) => current + 1);
    } catch (reason) {
      if (!controller.signal.aborted) setError(reason);
    } finally {
      if (!controller.signal.aborted) setLoadingEarlier(false);
    }
  };

  return (
    <section className="space-y-3" aria-label={t("cleanup.cleanupLogs")}>
      <h2 className="text-sm font-semibold">{t("cleanup.cleanupLogs")}</h2>
      <div className="flex flex-col gap-2 sm:flex-row sm:items-start">
        <div className="relative min-w-64 flex-1 sm:max-w-xl">
          <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            className="h-11 pl-9 sm:h-9"
            value={activeResourceIDFilter}
            onChange={(event) => setResourceIDFilter(event.target.value)}
            placeholder={t("cleanup.filterLogsByResourceId")}
            aria-label={t("cleanup.filterLogsByResourceId")}
          />
        </div>
        <div className="w-full sm:w-72">
          <ResourceKindPicker
            label={t("common.resourceKind")}
            values={resourceKindIDs}
            options={resourceKindOptions}
            allLabel={t("assets.allResourceKinds")}
            selectedCountLabel={(count) =>
              t("common.selectedResourceKindCount", { count })
            }
            selectedListLabel={t("common.selectedResourceKinds")}
            removeLabel={(name) => t("common.removeResourceKind", { name })}
            searchPlaceholder={t("assets.searchResourceKind")}
            emptyLabel={t("assets.noResourceKinds")}
            onValuesChange={setResourceKindIDs}
          />
        </div>
      </div>
      <div
        ref={terminalRef}
        role="log"
        aria-live="polite"
        className="min-h-64 max-h-[36rem] overflow-auto rounded-xl bg-neutral-950 px-4 py-3 font-mono text-[11px] leading-5 text-neutral-200"
        onScroll={(event) => {
          const terminal = event.currentTarget;
          followingRef.current =
            terminal.scrollHeight -
              terminal.scrollTop -
              terminal.clientHeight <=
            24;
        }}
      >
        {historyCursor && (
          <div className="flex justify-center pb-2">
            <Button
              type="button"
              size="sm"
              variant="ghost"
              className="h-7 text-neutral-400 hover:bg-neutral-800 hover:text-neutral-100"
              onClick={() => void loadEarlier()}
              disabled={loadingEarlier || refreshing}
            >
              {loadingEarlier
                ? t("common.loading")
                : t("scans.loadEarlierLogs")}
            </Button>
          </div>
        )}
        {error !== undefined && (
          <div className="whitespace-pre-wrap break-words text-amber-300">
            {`${formatTerminalTimestamp(new Date().toISOString())} WARN [stream] ${t("scans.reconnecting")} · ${formatError(error)}`}
          </div>
        )}
        {refreshing && logs.length === 0 ? (
          <div className="whitespace-pre text-neutral-500">
            {t("common.loading")}
          </div>
        ) : logs.length === 0 ? (
          <div className="whitespace-pre-wrap text-neutral-500">
            {hasActiveFilters
              ? t("cleanup.noMatchingLogs")
              : t("cleanup.waitingForLogs")}
          </div>
        ) : (
          logs.map((log) => {
            const payload = formatCloudPayload(log);
            return (
              <div key={log.id} className="whitespace-pre-wrap break-words">
                <span className="text-neutral-500">
                  {formatTerminalTimestamp(log.created_at)}
                </span>{" "}
                <span className={cn("font-semibold", levelClass(log.level))}>
                  {log.level.toUpperCase()}
                </span>{" "}
                <span className="text-sky-300">
                  {cleanupLogTarget(
                    log.target_key,
                    assetsByID,
                    resourceKindsByID,
                    locale,
                  )
                    .map((part) => `[${part}]`)
                    .join(" ")}
                </span>{" "}
                <span>{formatScanLogMessage(log.message)}</span>
                {payload && (
                  <>
                    {" "}
                    <span className="text-neutral-400">{payload}</span>
                  </>
                )}
              </div>
            );
          })
        )}
      </div>
    </section>
  );
}

function cleanupLogTarget(
  targetKey: string | undefined,
  assetsByID: ReadonlyMap<string, Asset>,
  resourceKindsByID: ReadonlyMap<string, ResourceKind>,
  locale: string,
) {
  if (!targetKey) return ["task"];
  const asset = assetsByID.get(targetKey);
  if (!asset) return [targetKey];
  const kind = resourceKindsByID.get(asset.resource_kind_id);
  const kindName = resourceTypeName(
    asset.resource_kind_id,
    kind?.display_name || asset.identity.native_type,
    kind?.display_names,
    locale,
  );
  return [kindName, asset.identity.native_id];
}

function mergeLogs(first: JobLog[], second: JobLog[]) {
  return [
    ...new Map([...first, ...second].map((item) => [item.id, item])).values(),
  ].sort(
    (left, right) =>
      left.created_at.localeCompare(right.created_at) ||
      left.id.localeCompare(right.id),
  );
}

function reconnectDelay(signal: AbortSignal, delay: number) {
  return new Promise<void>((resolve) => {
    const timer = window.setTimeout(resolve, delay);
    signal.addEventListener(
      "abort",
      () => {
        window.clearTimeout(timer);
        resolve();
      },
      { once: true },
    );
  });
}

function useDebounced(value: string, delay: number) {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(value), delay);
    return () => window.clearTimeout(timer);
  }, [delay, value]);
  return debounced;
}

function levelClass(level: string) {
  const normalized = level.toLowerCase();
  if (["error", "fatal"].includes(normalized)) return "text-red-300";
  if (["warn", "warning"].includes(normalized)) return "text-amber-300";
  if (normalized === "success") return "text-emerald-300";
  return "text-sky-300";
}
