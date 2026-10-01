import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Radar } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { toast } from "sonner";
import { createScan } from "@/api/client";
import { useHasRole } from "@/auth/roles";
import { Button } from "@/components/ui/button";
import { useLocale } from "@/i18n/LocaleProvider";
import { useScheduleOverview } from "./useScheduleOverview";

// CompleteScanPrompt appears where cleanup needs a complete scan: it says how
// old the last one is and starts a new one in one step.
export function CompleteScanPrompt({ connectionID }: { connectionID: string }) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const { formatDate, formatError, formatRelative, t } = useLocale();
  const canOperate = useHasRole("operator");
  const overview = useScheduleOverview();
  const last = overview.data?.find(
    (item) => item.connection_id === connectionID,
  )?.last_complete_scan_at;
  const start = useMutation({
    mutationFn: () =>
      createScan(connectionID, { scope_mode: "all_active_regions" }),
    onSuccess: async (scan) => {
      await queryClient.invalidateQueries({
        queryKey: ["scans", connectionID],
      });
      toast.success(t("cleanup.completeScanStarted"));
      navigate(`/scans/${encodeURIComponent(scan.id)}`);
    },
    onError: (error) => toast.error(formatError(error)),
  });
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-lg border px-3 py-2 text-sm">
      <Radar className="size-4 text-muted-foreground" aria-hidden="true" />
      <span
        className="min-w-0 flex-1"
        title={last ? formatDate(last) : undefined}
      >
        {last
          ? t("cleanup.completeScanLast", { time: formatRelative(last) })
          : t("cleanup.completeScanNever")}
      </span>
      {canOperate && (
        <Button
          size="sm"
          variant="outline"
          disabled={start.isPending}
          onClick={() => start.mutate()}
        >
          {t("cleanup.startCompleteScan")}
        </Button>
      )}
    </div>
  );
}
