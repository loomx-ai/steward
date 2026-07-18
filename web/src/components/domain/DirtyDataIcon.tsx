import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

export function DirtyDataIcon({
  className,
  ...props
}: Omit<ComponentProps<"img">, "alt" | "src">) {
  return (
    <img
      alt=""
      src="/icons/dirty-data.png"
      {...props}
      className={cn("inline-block shrink-0 object-contain", className)}
    />
  );
}
