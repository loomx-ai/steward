import { CalendarClock } from "lucide-react";
import { Link, useLocation } from "react-router-dom";
import type { ConnectionScheduleOverview } from "@/api/types";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { useActiveConnection } from "@/connections/ActiveConnectionProvider";
import { useLocale } from "@/i18n/LocaleProvider";
import { cn } from "@/lib/utils";
import { pauseReason, scheduleName } from "./scheduleFormat";
import {
  connectionFreshness,
  nextRunAt,
  useScheduleOverview,
  type Freshness,
} from "./useScheduleOverview";

const freshnessPages = ["/panorama", "/assets", "/cleanup", "/findings"];

const dotTone: Record<Freshness, string> = {
  fresh: "bg-success",
  stale: "bg-warning",
  paused: "bg-destructive",
  unscheduled: "bg-muted-foreground/40",
};

function useActiveOverview() {
  const { activeConnection } = useActiveConnection();
  const overview = useScheduleOverview();
  return overview.data?.find(
    (item) => item.connection_id === activeConnection?.id,
  );
}

// FreshnessIndicator tells people on inventory pages how current the data is
// without opening the scans page.
export function FreshnessIndicator() {
  const location = useLocation();
  const overview = useActiveOverview();
  const { formatDate, formatRelative, t } = useLocale();
  const show = freshnessPages.some(
    (page) =>
      location.pathname === page || location.pathname.startsWith(`${page}/`),
  );
  if (!show || !overview) return null;
  const freshness = connectionFreshness(overview);
  const next = nextRunAt(overview);
  const complete = overview.last_complete_scan_at
    ? t("freshness.complete", {
        time: formatRelative(overview.last_complete_scan_at),
      })
    : t("freshness.never");
  return (
    <Link
      to="/scans?tab=schedules"
      aria-label={t("freshness.label")}
      title={
        overview.last_complete_scan_at
          ? formatDate(overview.last_complete_scan_at)
          : undefined
      }
      className="hidden shrink-0 items-center gap-2 rounded-full border px-2.5 py-1 text-xs text-muted-foreground hover:bg-muted md:inline-flex"
    >
      <span
        aria-hidden="true"
        className={cn("size-1.5 rounded-full", dotTone[freshness])}
      />
      <span>{complete}</span>
      {freshness === "paused" ? (
        <span className="text-destructive">· {t("freshness.paused")}</span>
      ) : next ? (
        <span>· {t("freshness.next", { time: formatRelative(next) })}</span>
      ) : null}
    </Link>
  );
}

// ConnectionFreshness is the short status shown beside each connection in
// the connection switcher.
export function ConnectionFreshness({
  overview,
}: {
  overview?: ConnectionScheduleOverview;
}) {
  const { formatRelative, t } = useLocale();
  if (!overview) return null;
  const freshness = connectionFreshness(overview);
  const text =
    freshness === "paused"
      ? t("freshness.paused")
      : freshness === "unscheduled"
        ? t("freshness.noSchedule")
        : overview.last_complete_scan_at
          ? formatRelative(overview.last_complete_scan_at)
          : t("freshness.never");
  return (
    <span
      className={cn(
        "ml-auto inline-flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground",
        freshness === "paused" && "text-destructive",
      )}
    >
      <span
        aria-hidden="true"
        className={cn("size-1.5 rounded-full", dotTone[freshness])}
      />
      {text}
    </span>
  );
}

// PausedSchedulesBanner warns that the active connection's inventory has
// stopped refreshing because Steward paused its schedules.
export function PausedSchedulesBanner() {
  const location = useLocation();
  const overview = useActiveOverview();
  const { t } = useLocale();
  if (!overview || location.pathname.startsWith("/scans/schedules/")) {
    return null;
  }
  const paused = overview.schedules.filter((schedule) => schedule.pause_reason);
  if (paused.length === 0) return null;
  const first = paused[0];
  return (
    <div className="px-4 pt-4 sm:px-7">
      <Alert variant="destructive">
        <CalendarClock />
        <AlertTitle>
          {t("schedules.pausedBanner", {
            connection: overview.connection_name,
            reason: pauseReason(first, t),
          })}
        </AlertTitle>
        <AlertDescription className="flex flex-wrap items-center gap-x-3 gap-y-2">
          {first.pause_detail && (
            <span className="basis-full break-words">{first.pause_detail}</span>
          )}
          {first.pause_reason === "consecutive_failures" && (
            <span className="basis-full">
              {t("schedules.pausedBannerHint")}
            </span>
          )}
          {first.pause_reason === "consecutive_failures" && (
            <Button asChild size="sm" variant="outline">
              <Link to="/settings?section=connections">
                {t("schedules.replaceCredential")}
              </Link>
            </Button>
          )}
          <Button asChild size="sm" variant="ghost">
            <Link to={`/scans/schedules/${encodeURIComponent(first.id)}`}>
              {t("schedules.viewSchedule")}
              {paused.length > 1 ? "" : ` · ${scheduleName(first, t)}`}
            </Link>
          </Button>
        </AlertDescription>
      </Alert>
    </div>
  );
}
