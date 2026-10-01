import { Link } from "react-router-dom";
import type { ChangeCounts } from "@/api/types";
import { useLocale } from "@/i18n/LocaleProvider";

// ScheduleChanges shows "+added −removed ~modified" and links to the scan's
// change list when there is something to see.
export function ChangeCountsLink({
  counts,
  scanID,
}: {
  counts?: ChangeCounts;
  scanID?: string;
}) {
  const { t } = useLocale();
  if (!counts || !scanID)
    return <span className="text-muted-foreground">—</span>;
  const total = counts.added + counts.removed + counts.modified;
  if (total === 0) return <span className="text-muted-foreground">0</span>;
  return (
    <Link
      to={`/scans/${encodeURIComponent(scanID)}#changes`}
      className="inline-flex gap-1.5 font-mono text-xs underline-offset-4 hover:underline"
      aria-label={t("scans.changesLabel", { ...counts })}
    >
      {counts.added > 0 && (
        <span className="text-success">+{counts.added}</span>
      )}
      {counts.removed > 0 && (
        <span className="text-destructive">−{counts.removed}</span>
      )}
      {counts.modified > 0 && (
        <span className="text-warning-foreground">~{counts.modified}</span>
      )}
    </Link>
  );
}
