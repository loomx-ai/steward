import { useRef, type ReactElement } from "react";
import { CloudProviderIcon } from "@/components/domain/CloudProviderIcon";
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
  ContextMenuTrigger,
} from "@/components/ui/context-menu";
import { useLocale } from "@/i18n/LocaleProvider";
import {
  DirtyAssetContextMenuItem,
  type DirtyAssetTarget,
} from "../assets/DirtyAssetButton";

export type { DirtyAssetTarget };

export interface TopologyContextMenuProps {
  kind: "resource" | "region" | "vpc" | "vswitch" | "stack";
  pendingCleanup: boolean;
  inheritedCleanup?: boolean;
  children: ReactElement;
  onViewDetails: () => void;
  consoleURL?: string;
  onRescan?: () => void;
  onAddToCleanup?: () => void;
  onRemoveFromCleanup?: () => void;
  onOpenChange?: (open: boolean) => void;
  dirtyAsset?: DirtyAssetTarget;
  dirtyAssets?: readonly DirtyAssetTarget[];
}

export function TopologyContextMenu({
  kind,
  pendingCleanup,
  inheritedCleanup = false,
  children,
  onViewDetails,
  consoleURL,
  onRescan,
  onAddToCleanup,
  onRemoveFromCleanup,
  onOpenChange,
  dirtyAsset,
  dirtyAssets,
}: TopologyContextMenuProps) {
  const { t } = useLocale();
  const triggerRef = useRef<HTMLSpanElement>(null);
  const detailsLabel =
    kind === "stack"
      ? t("panorama.viewStackMembers")
      : t("panorama.viewDetails");
  const cleanupAction = pendingCleanup ? onRemoveFromCleanup : onAddToCleanup;
  const cleanupLabel =
    kind === "stack"
      ? t(
          pendingCleanup
            ? "panorama.removeAllFromCleanup"
            : "panorama.addAllToCleanup",
        )
      : t(
          pendingCleanup
            ? "panorama.removeFromCleanup"
            : "panorama.addToCleanup",
        );

  return (
    <ContextMenu onOpenChange={onOpenChange}>
      <ContextMenuTrigger
        ref={triggerRef}
        asChild
        onKeyDown={(event) => {
          if (!(event.shiftKey && event.key === "F10")) return;
          event.preventDefault();
          const bounds = event.currentTarget.getBoundingClientRect();
          event.currentTarget.dispatchEvent(
            new MouseEvent("contextmenu", {
              bubbles: true,
              cancelable: true,
              clientX: bounds.left + bounds.width / 2,
              clientY: bounds.top + bounds.height / 2,
            }),
          );
        }}
      >
        {children}
      </ContextMenuTrigger>
      <ContextMenuContent
        onCloseAutoFocus={(event) => {
          event.preventDefault();
          triggerRef.current?.focus({ preventScroll: true });
        }}
      >
        <ContextMenuItem onSelect={onViewDetails}>
          {detailsLabel}
        </ContextMenuItem>
        {consoleURL && (
          <ContextMenuItem asChild>
            <a href={consoleURL} target="_blank" rel="noopener noreferrer">
              {t("panorama.openConsole")}
              <CloudProviderIcon
                consoleURL={consoleURL}
                className="ml-auto size-3.5"
              />
            </a>
          </ContextMenuItem>
        )}
        {kind === "region" && onRescan && (
          <>
            <ContextMenuSeparator />
            <ContextMenuItem onSelect={onRescan}>
              {t("panorama.rescanRegion")}
            </ContextMenuItem>
          </>
        )}
        {(dirtyAsset || cleanupAction) && (
          <>
            <ContextMenuSeparator />
            {dirtyAsset && (
              <DirtyAssetContextMenuItem
                target={dirtyAsset}
                targets={dirtyAssets}
              />
            )}
            {cleanupAction &&
              (inheritedCleanup ? (
                <ContextMenuItem disabled>
                  {t("panorama.inheritedCleanup")}
                </ContextMenuItem>
              ) : (
                <ContextMenuItem onSelect={cleanupAction}>
                  {cleanupLabel}
                </ContextMenuItem>
              ))}
          </>
        )}
      </ContextMenuContent>
    </ContextMenu>
  );
}
