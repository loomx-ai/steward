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

// Canvas nodes pass getters for values that are only needed once the menu is
// open, so building thousands of nodes does not compute them up front.
export type MenuValue<T> = T | (() => T);

function resolve<T>(value: MenuValue<T>): T {
  return typeof value === "function" ? (value as () => T)() : value;
}

export interface TopologyContextMenuProps {
  kind: "resource" | "region" | "vpc" | "vswitch" | "stack";
  pendingCleanup: boolean;
  inheritedCleanup?: MenuValue<boolean | undefined>;
  children: ReactElement;
  onViewDetails: () => void;
  consoleURL?: MenuValue<string | undefined>;
  onRescan?: () => void;
  onAddToCleanup?: () => void;
  onRemoveFromCleanup?: () => void;
  onOpenChange?: (open: boolean) => void;
  dirtyAsset?: DirtyAssetTarget;
  dirtyAssets?: MenuValue<readonly DirtyAssetTarget[] | undefined>;
}

export function TopologyContextMenu({
  children,
  onOpenChange,
  ...menu
}: TopologyContextMenuProps) {
  const triggerRef = useRef<HTMLSpanElement>(null);

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
        <TopologyContextMenuItems {...menu} />
      </ContextMenuContent>
    </ContextMenu>
  );
}

function TopologyContextMenuItems({
  kind,
  pendingCleanup,
  inheritedCleanup: inheritedCleanupValue = false,
  onViewDetails,
  consoleURL: consoleURLValue,
  onRescan,
  onAddToCleanup,
  onRemoveFromCleanup,
  dirtyAsset,
  dirtyAssets,
}: Omit<TopologyContextMenuProps, "children" | "onOpenChange">) {
  const { t } = useLocale();
  const consoleURL = resolve(consoleURLValue);
  const inheritedCleanup = resolve(inheritedCleanupValue);
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
    <>
      <ContextMenuItem onSelect={onViewDetails}>{detailsLabel}</ContextMenuItem>
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
              targets={resolve(dirtyAssets)}
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
    </>
  );
}
