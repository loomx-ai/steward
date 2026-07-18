import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

export type PageLayoutMode = "reading" | "list" | "canvas";

const widths: Record<PageLayoutMode, string> = {
  reading: "w-full",
  list: "w-full",
  canvas: "w-full",
};

export function PageLayout({
  mode,
  className,
  ...props
}: ComponentProps<"div"> & { mode: PageLayoutMode }) {
  return (
    <div
      data-slot="page-layout"
      data-layout-mode={mode}
      className={cn(
        "min-h-full",
        mode === "canvas"
          ? "flex min-h-0 flex-1 flex-col"
          : "px-4 py-6 sm:px-6 lg:px-8",
        widths[mode],
        className,
      )}
      {...props}
    />
  );
}
