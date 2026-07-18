import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

export function RowActions({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      data-slot="row-actions"
      className={cn(
        "flex items-center justify-end gap-1 opacity-100 transition-opacity duration-[120ms] md:opacity-0 md:group-hover:opacity-100 md:group-focus-within:opacity-100",
        className,
      )}
      {...props}
    />
  );
}
