import { QueryClient } from "@tanstack/react-query";
import { expect, it } from "vitest";
import type { ActionAttempt, CleanupTaskAggregate, Page } from "@/api/types";
import { applyCleanupProgress } from "./cleanupProgress";

it("merges pushed progress into the cached task, execution and actions", () => {
  const queryClient = new QueryClient();
  const taskKey = ["cleanup-task", "connection-a", "cln-1"];
  const impact = (id: string, result?: string) =>
    ({
      id,
      asset_id: id,
      result,
    }) as CleanupTaskAggregate["impact_items"][number];
  const action = (id: string, status: string) =>
    ({ id, status }) as ActionAttempt;
  const apply = (event: Parameters<typeof applyCleanupProgress>[3]) =>
    applyCleanupProgress(queryClient, "connection-a", "cln-1", event);

  expect(
    apply({
      type: "aggregate",
      data: {
        task: { id: "cln-1", status: "executing" },
        steps: [],
        impact_items: [impact("impact-a"), impact("impact-b")],
      } as unknown as CleanupTaskAggregate,
    }),
  ).toBe(false);
  apply({
    type: "impacts",
    data: { items: [impact("impact-b", "deleted_by_controller")] },
  });
  apply({
    type: "actions",
    data: {
      execution_id: "execution-1",
      items: [action("action-a", "invoking"), action("action-b", "invoking")],
    },
  });
  apply({
    type: "actions",
    data: {
      execution_id: "execution-1",
      items: [action("action-b", "succeeded")],
    },
  });

  const aggregate = queryClient.getQueryData<CleanupTaskAggregate>(taskKey);
  expect(aggregate?.impact_items.map((item) => item.result)).toEqual([
    undefined,
    "deleted_by_controller",
  ]);
  expect(
    queryClient
      .getQueryData<Page<ActionAttempt>>([
        "cleanup-actions",
        "connection-a",
        "execution-1",
      ])
      ?.items.map((item) => `${item.id}:${item.status}`),
  ).toEqual(["action-a:invoking", "action-b:succeeded"]);
  expect(
    apply({
      type: "end",
      data: {
        id: "cln-1",
        status: "completed",
      } as CleanupTaskAggregate["task"],
    }),
  ).toBe(true);
  expect(
    queryClient.getQueryData<CleanupTaskAggregate>(taskKey)?.task.status,
  ).toBe("completed");
});
