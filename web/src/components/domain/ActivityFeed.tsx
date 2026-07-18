import { AlertCircle, CheckCircle2, Circle, Info } from "lucide-react";
import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

export interface ActivityItem {
  id: string;
  time: string;
  title: string;
  detail?: ReactNode;
  tone?: "default" | "info" | "success" | "warning" | "destructive";
}

export function ActivityFeed({
  items,
  formatTime,
}: {
  items: ActivityItem[];
  formatTime: (value: string) => string;
}) {
  return (
    <ol className="divide-y">
      {items.map((item) => {
        const Icon =
          item.tone === "success"
            ? CheckCircle2
            : item.tone === "warning" || item.tone === "destructive"
              ? AlertCircle
              : item.tone === "info"
                ? Info
                : Circle;
        return (
          <li
            key={item.id}
            className="grid grid-cols-[1rem_1fr_auto] gap-3 py-3"
          >
            <Icon
              className={cn(
                "mt-0.5 size-4 text-muted-foreground",
                item.tone === "success" && "text-success-foreground",
                item.tone === "info" && "text-info-foreground",
                item.tone === "warning" && "text-warning-foreground",
                item.tone === "destructive" && "text-destructive",
              )}
            />
            <div className="min-w-0">
              <p className="text-sm font-medium">{item.title}</p>
              {item.detail && (
                <div className="mt-1 text-xs leading-relaxed text-muted-foreground">
                  {item.detail}
                </div>
              )}
            </div>
            <time className="font-mono text-[11px] text-muted-foreground">
              {formatTime(item.time)}
            </time>
          </li>
        );
      })}
    </ol>
  );
}
