import { useEffect, useRef, useState } from "react";
import { Check, Copy } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { useLocale } from "@/i18n/LocaleProvider";
import type { ResourcePropertyRow } from "./resourceProperties";

export function CopyResourcePropertiesButton({
  rows,
}: {
  rows: ResourcePropertyRow[];
}) {
  const { t } = useLocale();
  const [copied, setCopied] = useState(false);
  const resetTimer = useRef<ReturnType<typeof setTimeout> | undefined>(
    undefined,
  );
  const label = t(copied ? "asset.propertiesCopied" : "asset.copyProperties");

  useEffect(
    () => () => {
      if (resetTimer.current) clearTimeout(resetTimer.current);
    },
    [],
  );

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(
        resourcePropertiesClipboardText(rows),
      );
      setCopied(true);
      if (resetTimer.current) clearTimeout(resetTimer.current);
      resetTimer.current = setTimeout(() => setCopied(false), 1600);
    } catch {
      setCopied(false);
    }
  };

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button
          type="button"
          size="icon-xs"
          variant="ghost"
          aria-label={label}
          onClick={() => void copy()}
        >
          {copied ? <Check className="text-success" /> : <Copy />}
        </Button>
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}

export function resourcePropertiesClipboardText(
  rows: ResourcePropertyRow[],
): string {
  return rows
    .map((row) => {
      if (row.value !== null && typeof row.value === "object") {
        return `${row.label}:\n${indent(JSON.stringify(row.value, null, 2))}`;
      }
      return `${row.label}: ${displayScalar(row.value)}`;
    })
    .join("\n");
}

function indent(value: string): string {
  return value
    .split("\n")
    .map((line) => `  ${line}`)
    .join("\n");
}

function displayScalar(value: unknown): string {
  if (value === undefined || value === null || value === "") return "—";
  return String(value);
}
