import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

export function Toolbar({
  label,
  className,
  ...props
}: ComponentProps<"div"> & { label: string }) {
  return (
    <div
      role="toolbar"
      aria-label={label}
      data-slot="toolbar"
      className={cn(
        "flex min-h-10 flex-wrap items-center gap-2 border-b border-border/70 bg-background px-3 py-2",
        className,
      )}
      {...props}
    />
  );
}
