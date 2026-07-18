import { Braces, ChevronDown } from "lucide-react";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { ScrollArea } from "@/components/ui/scroll-area";
import { cn } from "@/lib/utils";

export function JsonViewer({
  value,
  labels,
  defaultOpen = false,
  className,
}: {
  value: unknown;
  labels: { show: string; hide: string };
  defaultOpen?: boolean;
  className?: string;
}) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <Collapsible open={open} onOpenChange={setOpen} className={className}>
      <CollapsibleTrigger asChild>
        <Button variant="ghost" size="sm" className="w-full justify-start">
          <Braces />
          {open ? labels.hide : labels.show}
          <ChevronDown
            className={cn("ml-auto transition-transform", open && "rotate-180")}
          />
        </Button>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <ScrollArea className="mt-2 max-h-96 rounded-lg border bg-muted/30">
          <pre className="min-w-max p-4 font-mono text-xs leading-relaxed">
            {JSON.stringify(value, null, 2)}
          </pre>
        </ScrollArea>
      </CollapsibleContent>
    </Collapsible>
  );
}
