import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { getScanLogs, streamScanEvents } from "@/api/client";
import type { JobLog, ScanTask } from "@/api/types";
import { Button } from "@/components/ui/button";
import { useLocale } from "@/i18n/LocaleProvider";
import { cn } from "@/lib/utils";

export function ScanTaskEvents({
  connectionID,
  scanID,
  targetKey,
  targetLabel,
  onClearTarget,
}: {
  connectionID: string;
  scanID: string;
  targetKey?: string;
  targetLabel?: string;
  onClearTarget?: () => void;
}) {
  const [logs, setLogs] = useState<JobLog[]>([]);
  const [error, setError] = useState<unknown>();
  const [historyCursor, setHistoryCursor] = useState("");
  const [loadingEarlier, setLoadingEarlier] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [historyVersion, setHistoryVersion] = useState(0);
  const terminalRef = useRef<HTMLDivElement>(null);
  const followingRef = useRef(true);
  const historyControllerRef = useRef<AbortController | null>(null);
  const pendingScrollAdjustmentRef = useRef<{
    height: number;
    top: number;
  } | null>(null);
  const queryClient = useQueryClient();
  const { formatError, t } = useLocale();
  useEffect(() => {
    const controller = new AbortController();
    let cursor = "";
    let ended = false;
    setLoadingEarlier(false);
    setRefreshing(true);
    setError(undefined);
    followingRef.current = true;
    pendingScrollAdjustmentRef.current = null;
    const receive = (event: {
      type: "snapshot" | "log" | "end";
      data: ScanTask | JobLog;
      id?: string;
    }) => {
      if (event.id) cursor = event.id;
      if (event.type === "log") {
        const log = event.data as JobLog;
        setLogs((current) =>
          [
            ...new Map(
              [...current, log].map((item) => [item.id, item]),
            ).values(),
          ].sort(
            (left, right) =>
              left.created_at.localeCompare(right.created_at) ||
              left.id.localeCompare(right.id),
          ),
        );
        return;
      }
      if (event.type === "end") ended = true;
      queryClient.setQueryData(
        ["scan", connectionID, scanID],
        event.data as ScanTask,
      );
      void queryClient.invalidateQueries({
        queryKey: ["scans", connectionID],
      });
    };
    const connect = async () => {
      while (!controller.signal.aborted && !ended) {
        try {
          setError(undefined);
          const latest = await streamScanEvents(
            connectionID,
            scanID,
            targetKey ?? "",
            cursor,
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
        const history = await getScanLogs(
          connectionID,
          scanID,
          targetKey ?? "",
          "",
          controller.signal,
        );
        if (controller.signal.aborted) return;
        setLogs(mergeLogs([], history.items));
        setHistoryCursor(history.next_cursor ?? "");
        cursor = history.live_cursor ?? "";
        followingRef.current = true;
        pendingScrollAdjustmentRef.current = null;
        setHistoryVersion((current) => current + 1);
        setRefreshing(false);
        await connect();
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
  }, [connectionID, queryClient, scanID, t, targetKey]);
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
    if (typeof terminal.scrollTo === "function") {
      terminal.scrollTo({ top: terminal.scrollHeight });
    } else {
      terminal.scrollTop = terminal.scrollHeight;
    }
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
      const history = await getScanLogs(
        connectionID,
        scanID,
        targetKey ?? "",
        historyCursor,
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
    <section className="space-y-2" aria-label={t("scans.liveLogs")}>
      <div className="flex min-w-0 items-center justify-between gap-3">
        <h2 className="min-w-0 truncate text-sm font-semibold">
          {t("scans.liveLogs")}
          {targetLabel ? ` · ${targetLabel}` : ""}
        </h2>
        {targetLabel && onClearTarget && (
          <Button
            type="button"
            size="sm"
            variant="ghost"
            className="h-7 shrink-0 px-2 text-muted-foreground"
            onClick={onClearTarget}
          >
            {t("scans.clearTargetFilter")}
          </Button>
        )}
      </div>
      <div
        ref={terminalRef}
        role="log"
        aria-live="polite"
        className="min-h-44 max-h-[32rem] overflow-auto rounded-xl bg-neutral-950 px-4 py-3 font-mono text-[11px] leading-5 text-neutral-200"
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
        {logs.length === 0 ? (
          <div className="whitespace-pre text-neutral-500">
            {t("jobs.waiting")}
          </div>
        ) : (
          logs.map((log) => {
            const payload = formatCloudPayload(log);
            return (
              <div key={log.id}>
                <div className="whitespace-pre-wrap break-words">
                  <span className="text-neutral-500">
                    {formatTerminalTimestamp(log.created_at)}
                  </span>{" "}
                  <span className={cn("font-semibold", levelClass(log.level))}>
                    {log.level.toUpperCase()}
                  </span>{" "}
                  <span className="text-sky-300">
                    [{log.target_key || "task"}]
                  </span>{" "}
                  <span>{formatScanLogMessage(log.message)}</span>
                  {payload && (
                    <>
                      {" "}
                      <span className="text-neutral-400">{payload}</span>
                    </>
                  )}
                </div>
              </div>
            );
          })
        )}
      </div>
    </section>
  );
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

export function formatCloudPayload(log: JobLog) {
  if (log.kind !== "cloud_api_request" && log.kind !== "cloud_api_response") {
    return "";
  }
  if (!log.payload || Object.keys(log.payload).length === 0) return "";
  try {
    return JSON.stringify(log.payload);
  } catch {
    return '{"error":"Unable to render structured log payload"}';
  }
}

const alibabaCloudOperationProducts: Record<string, string> = {
  DescribeInstances: "ecs",
  DescribeSecurityGroups: "ecs",
  DescribeNetworkInterfaces: "ecs",
  DescribeDisks: "ecs",
  DeleteInstance: "ecs",
  DescribeVSwitches: "vpc",
  DescribeVpcs: "vpc",
  DeleteVpc: "vpc",
  SearchResources: "resource-center",
};

export function formatScanLogMessage(message: string) {
  return message.replace(
    /\bAlibabaCloud\.([A-Za-z][A-Za-z0-9]*)\b/g,
    (_match, operation: string, offset: number, input: string) => {
      const product = alibabaCloudOperationProducts[operation];
      if (!product) return operation;
      const prefix = input.slice(0, offset);
      const previousToken = prefix.trimEnd().split(/\s+/).slice(-1)[0];
      if (previousToken === product) return operation;
      return `${product} ${operation}`;
    },
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

export function formatTerminalTimestamp(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return [
    `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`,
    `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())},${String(date.getMilliseconds()).padStart(3, "0")}`,
  ].join(" ");
}

function pad(value: number) {
  return String(value).padStart(2, "0");
}

function levelClass(level: string) {
  const normalized = level.toLowerCase();
  if (["error", "fatal"].includes(normalized)) return "text-red-300";
  if (["warn", "warning"].includes(normalized)) return "text-amber-300";
  if (normalized === "success") return "text-emerald-300";
  return "text-sky-300";
}
