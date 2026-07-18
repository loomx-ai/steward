import { useEffect, useRef, useState, type ReactNode } from "react";
import { Check, Copy } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useLocale } from "@/i18n/LocaleProvider";
import { cn } from "@/lib/utils";

export function CopyableId({
  label,
  value,
  children,
  trailing,
  className,
}: {
  label: string;
  value: string;
  children?: ReactNode;
  trailing?: ReactNode;
  className?: string;
}) {
  const { t } = useLocale();
  const [copied, setCopied] = useState(false);
  const resetTimer = useRef<ReturnType<typeof setTimeout> | undefined>(
    undefined,
  );

  useEffect(
    () => () => {
      if (resetTimer.current) clearTimeout(resetTimer.current);
    },
    [],
  );

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      if (resetTimer.current) clearTimeout(resetTimer.current);
      resetTimer.current = setTimeout(() => setCopied(false), 1600);
    } catch {
      setCopied(false);
    }
  };

  const actionLabel = t(copied ? "common.copiedId" : "common.copyId", {
    label,
  });

  return (
    <span
      className={cn(
        "group inline-flex max-w-full items-center gap-1",
        className,
      )}
    >
      <span className="min-w-0 truncate">{children ?? value}</span>
      {trailing && (
        <span className="inline-flex shrink-0 items-center gap-1.5">
          {trailing}
        </span>
      )}
      <Button
        type="button"
        size="icon-xs"
        variant="ghost"
        className="opacity-0 group-hover:opacity-100 focus-visible:opacity-100"
        aria-label={actionLabel}
        title={actionLabel}
        onClick={() => void copy()}
      >
        {copied ? <Check /> : <Copy />}
      </Button>
    </span>
  );
}
