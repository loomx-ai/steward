import type { QueryClient } from "@tanstack/react-query";
import {
  streamCleanupTaskProgress,
  type CleanupProgressEvent,
} from "@/api/client";
import type {
  ActionAttempt,
  CleanupTaskAggregate,
  ExecutionAttempt,
  Page,
} from "@/api/types";
import { reconnectDelay } from "./CleanupTaskEvents";

// followCleanupTaskProgress keeps the detail page's caches current from the
// task's progress stream, reconnecting until the task settles or the signal
// aborts. onStreaming reports whether the stream currently owns the caches.
export async function followCleanupTaskProgress(
  queryClient: QueryClient,
  connectionID: string,
  taskID: string,
  onStreaming: (streaming: boolean) => void,
  signal: AbortSignal,
) {
  let ended = false;
  while (!signal.aborted && !ended) {
    let applied = Promise.resolve();
    let first = true;
    try {
      await streamCleanupTaskProgress(
        connectionID,
        taskID,
        (event) => {
          applied = applied.then(async () => {
            if (signal.aborted) return;
            if (first) {
              first = false;
              // A fetch that started before the stream read older state; it
              // must not land over what the stream sends.
              await Promise.all(
                [
                  ["cleanup-task", connectionID, taskID],
                  ["cleanup-task-executions", connectionID, taskID],
                  ["cleanup-actions", connectionID],
                ].map((queryKey) => queryClient.cancelQueries({ queryKey })),
              );
              if (signal.aborted) return;
              onStreaming(true);
            }
            ended =
              applyCleanupProgress(queryClient, connectionID, taskID, event) ||
              ended;
          });
        },
        signal,
      );
      await applied;
    } catch {
      // Reconnect below; the page polls while the stream is down.
    }
    if (signal.aborted) return;
    onStreaming(false);
    if (!ended) await reconnectDelay(signal, 1000);
  }
}

// applyCleanupProgress writes one progress event into the query cache and
// reports whether it ended the stream.
export function applyCleanupProgress(
  queryClient: QueryClient,
  connectionID: string,
  taskID: string,
  event: CleanupProgressEvent,
): boolean {
  const taskKey = ["cleanup-task", connectionID, taskID];
  switch (event.type) {
    case "aggregate":
      queryClient.setQueryData<CleanupTaskAggregate>(taskKey, event.data);
      return false;
    case "task":
    case "end":
      queryClient.setQueryData<CleanupTaskAggregate>(
        taskKey,
        (current) => current && { ...current, task: event.data },
      );
      void queryClient.invalidateQueries({
        queryKey: ["cleanup", connectionID],
      });
      return event.type === "end";
    case "impacts":
      queryClient.setQueryData<CleanupTaskAggregate>(
        taskKey,
        (current) =>
          current && {
            ...current,
            impact_items: upsertByID(
              current.impact_items ?? [],
              event.data.items,
            ),
          },
      );
      return false;
    case "execution":
      queryClient.setQueryData<Page<ExecutionAttempt>>(
        ["cleanup-task-executions", connectionID, taskID],
        (current) => ({
          ...current,
          items: upsertByID(current?.items ?? [], [event.data]),
        }),
      );
      return false;
    case "actions":
      queryClient.setQueryData<Page<ActionAttempt>>(
        ["cleanup-actions", connectionID, event.data.execution_id],
        (current) => ({
          ...current,
          items: upsertByID(current?.items ?? [], event.data.items),
        }),
      );
      return false;
  }
}

function upsertByID<T extends { id: string }>(items: T[], changes: T[]): T[] {
  const changed = new Map(changes.map((item) => [item.id, item]));
  const known = new Set(items.map((item) => item.id));
  return [
    ...items.map((item) => changed.get(item.id) ?? item),
    ...changes.filter((item) => !known.has(item.id)),
  ];
}
