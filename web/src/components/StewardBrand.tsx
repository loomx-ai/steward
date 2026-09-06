import { cn } from "@/lib/utils";

export function StewardBrand({ className }: { className?: string }) {
  return (
    <span className={cn("inline-flex items-center gap-2", className)}>
      <img
        src="/brand/steward-symbol.svg"
        alt=""
        width={32}
        height={32}
        className="size-8 shrink-0"
      />
      <span className="truncate text-lg font-bold tracking-tight">steward</span>
    </span>
  );
}
