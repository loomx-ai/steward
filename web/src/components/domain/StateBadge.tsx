import {
  AlertCircle,
  CheckCircle2,
  Circle,
  Clock3,
  LoaderCircle,
  PauseCircle,
  XCircle,
} from "lucide-react";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

export type StateTone =
  "success" | "warning" | "destructive" | "info" | "muted";

const successStates = new Set([
  "active",
  "complete",
  "completed",
  "succeeded",
  "success",
  "ready",
  "healthy",
]);
const warningStates = new Set([
  "pending",
  "queued",
  "waiting",
  "warning",
  "partial",
  "incomplete",
  "blocked",
]);
const destructiveStates = new Set([
  "failed",
  "error",
  "invalid",
  "deleted",
  "cancelled",
  "canceled",
]);
const infoStates = new Set([
  "running",
  "executing",
  "scanning",
  "processing",
  "intent-persisted",
  "invoking",
  "reading-back",
  "reconciling",
  "in-progress",
]);

export function stateTone(value: string): StateTone {
  const normalized = value.toLowerCase().replaceAll("_", "-");
  if (successStates.has(normalized)) return "success";
  if (warningStates.has(normalized)) return "warning";
  if (destructiveStates.has(normalized)) return "destructive";
  if (infoStates.has(normalized)) return "info";
  return "muted";
}

export function StateBadge({
  value,
  label,
  className,
}: {
  value: string;
  label?: string;
  className?: string;
}) {
  const tone = stateTone(value);
  const Icon = iconForTone(tone, value);
  return (
    <span
      data-slot="state-badge"
      className={cn(
        "inline-flex w-fit shrink-0 items-center justify-center gap-1.5 text-xs font-medium whitespace-nowrap",
        tone === "success" && "text-success-foreground",
        tone === "warning" && "text-warning-foreground",
        tone === "destructive" && "text-destructive",
        tone === "info" && "text-info-foreground",
        tone === "muted" && "text-muted-foreground",
        className,
      )}
    >
      <Icon
        data-testid="state-icon"
        className={cn(
          "size-3.5",
          tone === "success" && "text-success",
          tone === "warning" && "text-warning",
          tone === "destructive" && "text-destructive",
          tone === "info" && "animate-spin text-info",
          tone === "muted" && "text-muted-foreground",
        )}
      />
      {label ?? value.replaceAll("_", " ")}
    </span>
  );
}

export function StateIcon({
  value,
  label,
  className,
}: {
  value: string;
  label?: string;
  className?: string;
}) {
  const tone = stateTone(value);
  const Icon = iconForTone(tone, value);
  const accessibleLabel = label ?? value.replaceAll("_", " ");
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span
          role="img"
          aria-label={accessibleLabel}
          className={cn(
            "inline-flex size-4 shrink-0 items-center justify-center",
            tone === "success" && "text-success",
            tone === "warning" && "text-warning",
            tone === "destructive" && "text-destructive",
            tone === "info" && "text-info",
            tone === "muted" && "text-muted-foreground",
            className,
          )}
        >
          <Icon className={cn("size-3.5", tone === "info" && "animate-spin")} />
        </span>
      </TooltipTrigger>
      <TooltipContent>{accessibleLabel}</TooltipContent>
    </Tooltip>
  );
}

function iconForTone(tone: StateTone, value: string) {
  if (tone === "success") return CheckCircle2;
  if (tone === "destructive") return XCircle;
  if (tone === "info") return LoaderCircle;
  if (value.toLowerCase().includes("pause")) return PauseCircle;
  if (tone === "warning")
    return value.toLowerCase().includes("wait") ? Clock3 : AlertCircle;
  return Circle;
}
