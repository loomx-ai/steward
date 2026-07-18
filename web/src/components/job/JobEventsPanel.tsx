import { useEffect, useState } from "react";
import { streamJobEvents } from "../../api/client";
import type { JobLog } from "../../api/types";
import { useLocale } from "../../i18n/LocaleProvider";
import { ActivityFeed } from "../domain/ActivityFeed";
import { Alert, AlertDescription } from "../ui/alert";
import { Skeleton } from "../ui/skeleton";

export function JobEventsPanel({
  connectionID,
  jobID,
  title,
}: {
  connectionID: string;
  jobID: string;
  title: string;
}) {
  const [logs, setLogs] = useState<JobLog[]>([]);
  const [error, setError] = useState<unknown>();
  const { formatError, formatTime, t } = useLocale();
  useEffect(() => {
    const controller = new AbortController();
    let after = 0;
    void streamJobEvents(
      connectionID,
      jobID,
      after,
      (log) => {
        after = Math.max(after, log.sequence);
        setLogs((current) =>
          [
            ...current.filter((item) => item.sequence !== log.sequence),
            log,
          ].sort((left, right) => left.sequence - right.sequence),
        );
      },
      controller.signal,
    ).catch((reason) => {
      if (!controller.signal.aborted) setError(reason);
    });
    return () => controller.abort();
  }, [connectionID, jobID]);
  return (
    <section
      aria-label={title}
      className="overflow-hidden rounded-xl border bg-background"
    >
      <header className="border-b px-4 py-3">
        <h3 className="text-sm font-semibold">{title}</h3>
      </header>
      <div className="p-4">
        {error !== undefined && (
          <Alert variant="destructive" className="mb-4">
            <AlertDescription>{formatError(error)}</AlertDescription>
          </Alert>
        )}
        {logs.length === 0 ? (
          <div className="space-y-2" aria-label={t("jobs.waiting")}>
            <Skeleton className="h-4 w-3/4" />
            <Skeleton className="h-4 w-1/2" />
            <p className="pt-1 text-xs text-muted-foreground">
              {t("jobs.waiting")}
            </p>
          </div>
        ) : (
          <ActivityFeed
            formatTime={formatTime}
            items={logs.map((log) => ({
              id: String(log.sequence),
              time: log.created_at,
              title: log.message,
              detail: log.level,
              tone: logTone(log.level),
            }))}
          />
        )}
      </div>
    </section>
  );
}

function logTone(level: string) {
  const normalized = level.toLowerCase();
  if (normalized === "error" || normalized === "fatal")
    return "destructive" as const;
  if (normalized === "warn" || normalized === "warning")
    return "warning" as const;
  if (normalized === "success") return "success" as const;
  return "info" as const;
}
