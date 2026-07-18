import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

export function DetailSection({
  title,
  titleAction,
  description,
  actions,
  children,
  className,
}: {
  title: string;
  titleAction?: ReactNode;
  description?: string;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section
      aria-label={title}
      data-slot="detail-section"
      className={cn("border-t py-6 first:border-t-0 first:pt-0", className)}
    >
      <div className="mb-4 flex items-start justify-between gap-4">
        <div>
          <div className="flex items-center gap-1">
            <h2 className="text-sm font-semibold">{title}</h2>
            {titleAction}
          </div>
          {description && (
            <p className="mt-1 text-sm text-muted-foreground">{description}</p>
          )}
        </div>
        {actions}
      </div>
      {children}
    </section>
  );
}
