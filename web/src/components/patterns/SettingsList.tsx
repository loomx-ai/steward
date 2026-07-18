import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

export function SettingsGroup({
  title,
  description,
  action,
  children,
  className,
  contentClassName,
}: {
  title: string;
  description?: string;
  action?: ReactNode;
  children: ReactNode;
  className?: string;
  contentClassName?: string;
}) {
  return (
    <section
      aria-label={title}
      role="group"
      className={cn("space-y-3", className)}
    >
      <div className="flex items-end justify-between gap-4">
        <div>
          <h2 className="text-sm font-semibold">{title}</h2>
          {description && (
            <p className="mt-1 text-sm text-muted-foreground">{description}</p>
          )}
        </div>
        {action}
      </div>
      <div
        className={cn(
          "divide-y overflow-hidden rounded-xl border bg-background",
          contentClassName,
        )}
      >
        {children}
      </div>
    </section>
  );
}

export function SettingsRow({
  label,
  description,
  control,
  children,
  className,
}: {
  label: string;
  description?: string;
  control?: ReactNode;
  children?: ReactNode;
  className?: string;
}) {
  return (
    <div
      data-testid="settings-row"
      className={cn(
        "flex min-h-14 flex-col items-stretch gap-4 px-4 py-3 transition-colors duration-[120ms] hover:bg-muted/45 sm:flex-row sm:items-center",
        className,
      )}
    >
      <div className="min-w-0 flex-1">
        <p className="text-sm font-medium">{label}</p>
        {description && (
          <p className="mt-0.5 text-xs leading-relaxed text-muted-foreground">
            {description}
          </p>
        )}
        {children}
      </div>
      {control && (
        <div className="min-w-0 max-w-full self-end break-words text-right sm:max-w-[60%]">
          {control}
        </div>
      )}
    </div>
  );
}
