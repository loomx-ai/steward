import { Globe2, Network, Server } from "lucide-react";
import type { ScanTargetProgress } from "@/api/types";
import { StateIcon, stateTone } from "@/components/domain/StateBadge";
import { useLocale } from "@/i18n/LocaleProvider";
import { cn } from "@/lib/utils";

export function scanTargetTitle(
  target: ScanTargetProgress,
  globalLabel?: string,
) {
  if (target.kind === "global" && globalLabel) return globalLabel;
  return (
    target.name || target.region_name || target.native_id || target.region_id
  );
}

export function ScanTargetCard({
  target,
  selected,
  onSelect,
}: {
  target: ScanTargetProgress;
  selected: boolean;
  onSelect: () => void;
}) {
  const { label, t } = useLocale();
  const tone = stateTone(target.status);
  const Icon =
    target.kind === "global"
      ? Globe2
      : target.kind === "region"
        ? Server
        : Network;
  const title = scanTargetTitle(target, t("common.global"));
  const id =
    target.kind === "global"
      ? "global"
      : target.kind === "region"
        ? target.region_id
        : `${target.region_id} · ${target.native_id}`;
  return (
    <button
      type="button"
      aria-pressed={selected}
      onClick={onSelect}
      className={cn(
        "relative w-full overflow-hidden rounded-xl border bg-card px-3.5 py-3 text-left outline-none transition-colors before:pointer-events-none before:absolute before:inset-y-3 before:left-0 before:w-[3px] before:rounded-r-full before:content-[''] hover:bg-accent/50 focus-visible:ring-2 focus-visible:ring-ring/60",
        tone === "success" && "before:bg-success",
        tone === "warning" && "before:bg-warning",
        tone === "destructive" && "before:bg-destructive",
        tone === "info" && "before:bg-info",
        tone === "muted" && "before:bg-muted-foreground",
        selected && "border-info bg-info/5 ring-1 ring-info/40",
      )}
    >
      <div className="flex min-w-0 items-center gap-2">
        <Icon className="size-4 shrink-0 text-muted-foreground" />
        <div className="min-w-0 flex-1">
          <div className="truncate text-sm font-medium" title={title}>
            {title}
          </div>
          <div className="mt-0.5 flex min-w-0 items-center justify-between gap-3">
            <span
              className="truncate font-mono text-[11px] text-muted-foreground"
              title={id}
            >
              {id}
            </span>
            <span
              className="shrink-0 text-xs text-muted-foreground"
              title={target.summary}
            >
              {target.summary}
            </span>
          </div>
        </div>
        <StateIcon value={target.status} label={label(target.status)} />
      </div>
    </button>
  );
}
