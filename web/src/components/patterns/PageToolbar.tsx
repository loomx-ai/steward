import type { ComponentProps, ReactNode } from "react";
import { createPortal } from "react-dom";
import {
  usePageHeaderActionsEnabled,
  usePageHeaderActionsTarget,
} from "@/app/PageHeaderActionsContext";
import { cn } from "@/lib/utils";

export function PageToolbar({
  label,
  leading,
  trailing,
  className,
  ...props
}: Omit<ComponentProps<"div">, "children"> & {
  label: string;
  leading?: ReactNode;
  trailing?: ReactNode;
}) {
  const headerTarget = usePageHeaderActionsTarget();
  const headerActionsEnabled = usePageHeaderActionsEnabled();
  const portaledTrailing =
    trailing && headerTarget ? createPortal(trailing, headerTarget) : null;
  const inlineTrailing = headerActionsEnabled ? null : trailing;

  return (
    <>
      {portaledTrailing}
      {(leading || inlineTrailing) && (
        <div
          role="toolbar"
          aria-label={label}
          data-slot="page-toolbar"
          className={cn(
            "mb-4 flex min-h-9 flex-wrap items-center gap-2",
            className,
          )}
          {...props}
        >
          {leading && (
            <div className="flex min-w-0 flex-wrap items-center gap-2">
              {leading}
            </div>
          )}
          {inlineTrailing && (
            <div className="ml-auto flex min-w-0 flex-wrap items-center justify-end gap-2">
              {inlineTrailing}
            </div>
          )}
        </div>
      )}
    </>
  );
}
