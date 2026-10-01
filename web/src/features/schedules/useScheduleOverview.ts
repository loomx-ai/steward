import { useQuery } from "@tanstack/react-query";
import { getScheduleOverview } from "@/api/client";
import type { ConnectionScheduleOverview } from "@/api/types";

export function useScheduleOverview() {
  return useQuery({
    queryKey: ["schedule-overview"],
    queryFn: getScheduleOverview,
    refetchInterval: 60_000,
  });
}

export type Freshness = "fresh" | "stale" | "paused" | "unscheduled";

const staleAfter = 24 * 60 * 60 * 1000;

// connectionFreshness sums up how much a connection's inventory can be
// trusted: a paused schedule outranks age, and a connection without any
// enabled schedule is not refreshed on its own.
export function connectionFreshness(
  overview: ConnectionScheduleOverview,
  now = Date.now(),
): Freshness {
  if (overview.schedules.some((schedule) => schedule.pause_reason)) {
    return "paused";
  }
  const enabled = overview.schedules.some((schedule) => schedule.enabled);
  if (!enabled) return "unscheduled";
  if (
    !overview.last_complete_scan_at ||
    now - new Date(overview.last_complete_scan_at).getTime() > staleAfter
  ) {
    return "stale";
  }
  return "fresh";
}

export function nextRunAt(overview: ConnectionScheduleOverview) {
  return overview.schedules
    .filter((schedule) => schedule.enabled && schedule.next_run_at)
    .map((schedule) => schedule.next_run_at as string)
    .sort()[0];
}
