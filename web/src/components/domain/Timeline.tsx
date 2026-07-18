import { Check, Circle, LoaderCircle, X } from "lucide-react";
import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

export interface TimelineItem {
  id: string;
  title: string;
  description?: ReactNode;
  meta?: ReactNode;
  state: "complete" | "active" | "pending" | "error";
}

export function Timeline({ items }: { items: TimelineItem[] }) {
  return (
    <ol className="space-y-0">
      {items.map((item, index) => {
        const Icon =
          item.state === "complete"
            ? Check
            : item.state === "active"
              ? LoaderCircle
              : item.state === "error"
                ? X
                : Circle;
        return (
          <li
            key={item.id}
            data-state={item.state}
            className="relative grid grid-cols-[1.5rem_1fr] gap-3 pb-5 last:pb-0"
          >
            {index < items.length - 1 && (
              <span className="absolute top-6 bottom-0 left-[0.7rem] w-px bg-border" />
            )}
            <span
              className={cn(
                "relative z-10 flex size-6 items-center justify-center rounded-full border bg-background",
                item.state === "complete" &&
                  "border-success/40 bg-success/10 text-success-foreground",
                item.state === "active" &&
                  "border-info/40 bg-info/10 text-info-foreground",
                item.state === "error" &&
                  "border-destructive/40 bg-destructive/10 text-destructive",
                item.state === "pending" && "text-muted-foreground",
              )}
            >
              <Icon
                className={cn(
                  "size-3.5",
                  item.state === "active" && "animate-spin",
                )}
              />
            </span>
            <div className="min-w-0 pt-0.5">
              <div className="flex items-center justify-between gap-3">
                <p className="text-sm font-medium">{item.title}</p>
                {item.meta && (
                  <span className="text-xs text-muted-foreground">
                    {item.meta}
                  </span>
                )}
              </div>
              {item.description && (
                <div className="mt-1 text-xs leading-relaxed text-muted-foreground">
                  {item.description}
                </div>
              )}
            </div>
          </li>
        );
      })}
    </ol>
  );
}
