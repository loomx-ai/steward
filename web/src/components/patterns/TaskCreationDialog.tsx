import type { FormEventHandler, ReactNode, RefObject } from "react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { cn } from "@/lib/utils";

export function TaskCreationDialog({
  open,
  onOpenChange,
  returnFocusRef,
  title,
  description,
  children,
  footer,
  pending = false,
  bodyClassName,
  onSubmit,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  returnFocusRef?: RefObject<HTMLButtonElement | null>;
  title: ReactNode;
  description?: ReactNode;
  children: ReactNode;
  footer: ReactNode;
  pending?: boolean;
  bodyClassName?: string;
  onSubmit: FormEventHandler<HTMLFormElement>;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="max-h-[calc(100dvh-2rem)] w-[calc(100%-2rem)] grid-rows-[auto_minmax(0,1fr)_auto] gap-0 overflow-hidden p-0 sm:max-w-3xl"
        {...(!description ? { "aria-describedby": undefined } : {})}
        onCloseAutoFocus={(event) => {
          if (!returnFocusRef?.current) return;
          event.preventDefault();
          queueMicrotask(() => returnFocusRef.current?.focus());
        }}
      >
        <form className="contents" aria-busy={pending} onSubmit={onSubmit}>
          <DialogHeader className="border-b px-4 py-4 pr-12 sm:px-6">
            <DialogTitle>{title}</DialogTitle>
            {description && (
              <DialogDescription>{description}</DialogDescription>
            )}
          </DialogHeader>
          <div
            className={cn(
              "min-h-0 overflow-y-auto overscroll-contain px-4 py-5 sm:px-6",
              bodyClassName,
            )}
          >
            {children}
          </div>
          <DialogFooter className="border-t bg-muted/15 px-4 py-3 sm:px-6 [&>button]:w-full sm:[&>button]:w-auto">
            {footer}
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
