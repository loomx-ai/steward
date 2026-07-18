import type { TopologyEntrySummary } from "@/api/types";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useLocale } from "@/i18n/LocaleProvider";

export function ScopeDetailDialog({
  open,
  onOpenChange,
  kind,
  entry,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  kind: "region" | "vpc";
  entry: TopologyEntrySummary | null | undefined;
}) {
  const { formatNumber, label, t } = useLocale();
  if (!entry) return null;
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent aria-describedby={undefined}>
        <DialogHeader>
          <DialogTitle>{entry.name}</DialogTitle>
        </DialogHeader>
        <dl className="grid gap-4 rounded-xl border p-4 sm:grid-cols-2">
          <Fact
            label={t("common.kind")}
            value={kind === "vpc" ? "VPC" : label(kind)}
          />
          <Fact
            label={t(kind === "region" ? "regions.id" : "common.nativeId")}
            value={entry.native_id?.trim() || entry.key}
          />
          <Fact
            label={t("panorama.scopeResourceCount")}
            value={formatNumber(entry.resource_count)}
          />
        </dl>
      </DialogContent>
    </Dialog>
  );
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="mt-1 break-words text-sm">{value}</dd>
    </div>
  );
}
