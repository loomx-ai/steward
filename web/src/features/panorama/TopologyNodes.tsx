import { Handle, Position, type Node, type NodeProps } from "@xyflow/react";
import {
  Boxes,
  Check,
  ChevronDown,
  ChevronUp,
  CircleHelp,
  Link2,
  Trash2,
} from "lucide-react";
import { Fragment, useId, type ReactElement, type ReactNode } from "react";
import type { TopologyResource, TopologyVSwitch } from "@/api/types";
import { DirtyDataIcon } from "@/components/domain/DirtyDataIcon";
import { ResourceIcon } from "@/components/domain/ResourceIcon";
import { resourceTypeName } from "@/components/domain/resourceKindLabel";
import { Badge } from "@/components/ui/badge";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { useLocale } from "@/i18n/LocaleProvider";
import { cn } from "@/lib/utils";
import type { CleanupTarget } from "./cleanupSelection";
import type { TopologyResourceGroupTypeSummary } from "./layout";
import {
  TopologyContextMenu,
  type DirtyAssetTarget,
} from "./TopologyContextMenu";

export interface TopologyNodeContextMenuActions {
  onViewDetails: () => void;
  consoleURL?: string;
  onAddToCleanup?: () => void;
  onRemoveFromCleanup?: () => void;
  inheritedCleanup?: boolean;
  dirtyAsset?: DirtyAssetTarget;
  dirtyAssets?: readonly DirtyAssetTarget[];
  onOpenChange: (open: boolean) => void;
}

export interface ResourceNodeData extends Record<string, unknown> {
  resource: TopologyResource;
  selected: boolean;
  current?: boolean;
  searchHighlighted?: boolean;
  related: boolean;
  pendingCleanup: boolean;
  cleanupTarget: CleanupTarget | null;
  interactionEnabled: boolean;
  onActivate: (resource: TopologyResource, trigger: HTMLElement | null) => void;
  onSelect?: (additive: boolean) => boolean;
  onContextMenu: (
    resource: TopologyResource,
    trigger: HTMLElement | null,
  ) => void;
  contextMenu?: TopologyNodeContextMenuActions;
}

export interface VSwitchNodeData extends Record<string, unknown> {
  vSwitch: TopologyVSwitch;
  selected: boolean;
  searchHighlighted?: boolean;
  pendingCleanup: boolean;
  cleanupTarget: CleanupTarget | null;
  interactionEnabled: boolean;
  onSelect?: (additive: boolean) => void;
  onContextMenu?: (trigger: HTMLElement | null) => void;
  contextMenu?: TopologyNodeContextMenuActions;
}

export interface StackNodeData extends Record<string, unknown> {
  stackKey: string;
  resourceKindID: string;
  typeName: string;
  typeNames?: Record<string, string>;
  icon?: string;
  count: number;
  pendingCleanup: boolean;
  pendingCleanupCount: number;
  selected?: boolean;
  searchHighlighted?: boolean;
  cleanupTarget: CleanupTarget;
  interactionEnabled: boolean;
  onSelect?: (
    stackKey: string,
    trigger: HTMLElement | null,
    additive: boolean,
  ) => void;
  onExpand: (stackKey: string, trigger: HTMLElement | null) => void;
  onContextMenu: (stackKey: string, trigger: HTMLElement | null) => void;
  contextMenu?: TopologyNodeContextMenuActions;
}

export interface ExpandedStackNodeData extends Record<string, unknown> {
  stackKey: string;
  resourceKindID: string;
  typeName: string;
  typeNames?: Record<string, string>;
  icon?: string;
  count: number;
  pendingCleanup: boolean;
  searchHighlighted?: boolean;
  cleanupTarget: CleanupTarget;
  interactionEnabled: boolean;
  onCollapse: (stackKey: string, trigger: HTMLElement | null) => void;
  onContextMenu: (stackKey: string, trigger: HTMLElement | null) => void;
  contextMenu?: TopologyNodeContextMenuActions;
}

interface ResourceGroupNodeDataBase extends ResourceNodeData {
  childKeys: readonly string[];
  directChildCount: number;
  childTypeSummaries: readonly TopologyResourceGroupTypeSummary[];
  childFindingCount: number;
}

export interface ResourceGroupNodeData extends ResourceGroupNodeDataBase {
  onExpand: (resourceKey: string, trigger: HTMLElement | null) => void;
}

export interface ExpandedResourceGroupNodeData extends ResourceGroupNodeDataBase {
  onCollapse: (
    resourceKey: string,
    childKeys: readonly string[],
    trigger: HTMLElement | null,
  ) => void;
}

export interface VSwitchStackNodeData extends Record<string, unknown> {
  stackKey: string;
  count: number;
  vSwitches: TopologyVSwitch[];
  selected: boolean;
  searchHighlighted?: boolean;
  pendingCleanup: boolean;
  pendingCleanupCount: number;
  cleanupTargets: readonly CleanupTarget[];
  interactionEnabled: boolean;
  onSelect: (targets: readonly CleanupTarget[], additive: boolean) => void;
  onExpand: (stackKey: string, trigger: HTMLElement | null) => void;
  onContextMenu?: (trigger: HTMLElement | null) => void;
  contextMenu?: TopologyNodeContextMenuActions;
}

export interface ExpandedVSwitchStackNodeData extends Record<string, unknown> {
  stackKey: string;
  count: number;
  vSwitches: TopologyVSwitch[];
  cleanupTargets: readonly CleanupTarget[];
  cleanupTargetsByVSwitchKey: ReadonlyMap<string, CleanupTarget>;
  selectedTargetKeys: ReadonlySet<string>;
  pendingTargetKeys: ReadonlySet<string>;
  highlightedVSwitchKey?: string;
  interactionEnabled: boolean;
  onSelect: (target: CleanupTarget, additive: boolean) => void;
  onContextMenu?: (target: CleanupTarget) => void;
  contextMenus?: ReadonlyMap<string, TopologyNodeContextMenuActions>;
  onCollapse: (stackKey: string, trigger: HTMLElement | null) => void;
}

export type ResourceFlowNode = Node<ResourceNodeData, "resource">;
export type VSwitchFlowNode = Node<VSwitchNodeData, "vswitch">;
export type StackFlowNode = Node<StackNodeData, "stack">;
export type ExpandedStackFlowNode = Node<
  ExpandedStackNodeData,
  "expandedStack"
>;
export type ResourceGroupFlowNode = Node<
  ResourceGroupNodeData,
  "resourceGroup"
>;
export type ExpandedResourceGroupFlowNode = Node<
  ExpandedResourceGroupNodeData,
  "expandedResourceGroup"
>;
export type VSwitchStackFlowNode = Node<VSwitchStackNodeData, "vSwitchStack">;
export type ExpandedVSwitchStackFlowNode = Node<
  ExpandedVSwitchStackNodeData,
  "expandedVSwitchStack"
>;

export type TopologyFlowNode =
  | ResourceFlowNode
  | VSwitchFlowNode
  | StackFlowNode
  | ExpandedStackFlowNode
  | ResourceGroupFlowNode
  | ExpandedResourceGroupFlowNode
  | VSwitchStackFlowNode
  | ExpandedVSwitchStackFlowNode;

export const REGION_ICON_SRC = "/icons/alicloud/acs-region.svg";
export const VPC_ICON_SRC = "/icons/alicloud/acs-vpc-vpc.svg";
export const VSWITCH_ICON_SRC = "/icons/alicloud/acs-vpc-vswitch.svg";
export { ResourceIcon } from "@/components/domain/ResourceIcon";

export function SelectionIndicator({ selected }: { selected: boolean }) {
  if (!selected) return null;
  return (
    <span
      aria-hidden="true"
      className="pointer-events-none absolute -top-2 -left-2 z-20 grid size-5 place-items-center rounded-full border-2 border-background bg-ring text-background shadow-sm"
      data-selection-indicator
    >
      <Check className="size-3" strokeWidth={3} />
    </span>
  );
}

export function PendingCleanupLabel({
  pendingCleanup,
  label,
  className,
}: {
  pendingCleanup: boolean;
  label: string;
  className?: string;
}) {
  if (!pendingCleanup) return null;
  return (
    <span
      aria-hidden="true"
      className={cn(
        "pointer-events-none absolute -top-2.5 right-0 z-20 inline-flex h-5 items-center gap-1 rounded-full border border-warning/50 bg-warning/10 px-1.5 text-[9px] leading-none font-medium whitespace-nowrap text-warning-foreground shadow-sm backdrop-blur-sm",
        className,
      )}
      data-pending-cleanup-label
    >
      <Trash2 className="size-3" />
      {label}
    </span>
  );
}

export function DirtyAssetLabel({
  dirty,
  label,
  className,
}: {
  dirty: boolean;
  label: string;
  className?: string;
}) {
  if (!dirty) return null;
  return (
    <span
      className={cn(
        "pointer-events-none absolute -top-2.5 right-0 z-20 inline-flex h-5 items-center gap-1 rounded-full border border-border bg-muted px-1.5 text-[10px] leading-none font-medium whitespace-nowrap text-muted-foreground shadow-sm backdrop-blur-sm",
        className,
      )}
      data-dirty-asset-label
    >
      <DirtyDataIcon aria-hidden="true" className="size-3" />
      {label}
    </span>
  );
}

const RESOURCE_HANDLE_POSITIONS = [
  ["top", Position.Top],
  ["right", Position.Right],
  ["bottom", Position.Bottom],
  ["left", Position.Left],
] as const;

function ResourceEdgeHandles() {
  return (
    <>
      {RESOURCE_HANDLE_POSITIONS.map(([side, position]) => (
        <Handle
          key={`target-${side}`}
          id={`target-${side}`}
          type="target"
          position={position}
          className="pointer-events-none !size-0 !border-0 !bg-transparent !opacity-0"
        />
      ))}
      {RESOURCE_HANDLE_POSITIONS.map(([side, position]) => (
        <Handle
          key={`source-${side}`}
          id={`source-${side}`}
          type="source"
          position={position}
          className="pointer-events-none !size-0 !border-0 !bg-transparent !opacity-0"
        />
      ))}
    </>
  );
}

function withContextMenu(
  trigger: ReactElement,
  input:
    | {
        kind: "resource" | "vswitch" | "stack";
        pendingCleanup: boolean;
        actions: TopologyNodeContextMenuActions;
      }
    | undefined,
) {
  if (!input) return trigger;
  return (
    <TopologyContextMenu
      kind={input.kind}
      pendingCleanup={input.pendingCleanup}
      inheritedCleanup={input.actions.inheritedCleanup}
      onViewDetails={input.actions.onViewDetails}
      consoleURL={input.actions.consoleURL}
      onAddToCleanup={input.actions.onAddToCleanup}
      onRemoveFromCleanup={input.actions.onRemoveFromCleanup}
      dirtyAsset={input.actions.dirtyAsset}
      dirtyAssets={input.actions.dirtyAssets}
      onOpenChange={input.actions.onOpenChange}
    >
      {trigger}
    </TopologyContextMenu>
  );
}

export function BareResourceNode({
  data,
  selected: flowSelected,
}: NodeProps<ResourceFlowNode>) {
  const { locale, t } = useLocale();
  const { resource } = data;
  const typeName = resourceTypeName(
    resource.resource_kind_id,
    resource.type_name,
    resource.type_names,
    locale,
  );
  const resourceName = resource.name.trim();
  const nativeID = resource.native_id.trim();
  const primaryLabel = resourceName || nativeID || resource.key;
  const secondaryID =
    resourceName.length > 0 && nativeID !== resourceName ? nativeID : "";
  const selected = data.selected || flowSelected;
  const current = data.current ?? selected;
  const membershipDescriptionID = useId();
  const showMembershipUnknown =
    resource.membership_unknown &&
    resource.class !== "orchestration.stack_group";
  const accessibleLabel = [
    primaryLabel,
    ...(current ? [t("panorama.currentResource")] : []),
    ...(data.related ? [t("panorama.directRelation")] : []),
    ...(data.pendingCleanup ? [t("panorama.pendingCleanup")] : []),
    ...(resource.dirty ? [t("asset.dirty")] : []),
  ].join(" · ");
  const resourceButton = (
    <button
      type="button"
      className={cn(
        "nodrag nopan group flex h-full min-w-0 flex-1 items-center gap-1.5 rounded-lg bg-transparent px-1 py-0 text-left outline-none transition-opacity",
        "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring",
        data.searchHighlighted &&
          "bg-primary/5 outline-2 outline-offset-2 outline-solid outline-primary",
        selected && "ring-2 ring-ring bg-primary/5",
        data.related && !selected && "ring-1 ring-ring/55 bg-ring/[0.03]",
      )}
      aria-label={accessibleLabel}
      aria-describedby={
        showMembershipUnknown ? membershipDescriptionID : undefined
      }
      aria-current={current ? "true" : undefined}
      aria-pressed={selected}
      data-resource-key={resource.key}
      data-search-highlighted={data.searchHighlighted ? "true" : undefined}
      data-related={data.related ? "true" : undefined}
      onClick={(event) => {
        event.stopPropagation();
        if (data.interactionEnabled === false) return;
        const selected = data.onSelect?.(event.shiftKey);
        if (!event.shiftKey && selected !== false) {
          data.onActivate(resource, event.currentTarget);
        }
      }}
      onContextMenu={(event) => {
        if (!data.contextMenu) {
          event.stopPropagation();
          event.preventDefault();
        }
        data.onContextMenu(resource, event.currentTarget);
      }}
    >
      <span
        className="grid h-full min-w-0 flex-1 grid-cols-[1.75rem_minmax(0,1fr)] grid-rows-[2.25rem_1rem] content-center gap-x-1.5 gap-y-1"
        data-resource-identity
        data-resource-layout
      >
        <span
          className="col-start-1 row-start-1 self-start"
          data-resource-icon-cell
        >
          <ResourceIcon icon={resource.icon} className="mt-0" />
        </span>
        <span
          className={cn(
            "col-start-2 row-start-1 min-w-0",
            secondaryID ? "self-start pt-0.5" : "self-center",
          )}
          data-resource-details
        >
          <strong
            className="block truncate text-xs leading-4 font-medium text-foreground"
            title={primaryLabel}
          >
            {primaryLabel}
          </strong>
          {secondaryID && (
            <small
              className="mt-0.5 block truncate text-[10px] leading-4 font-normal text-muted-foreground"
              title={secondaryID}
            >
              {secondaryID}
            </small>
          )}
        </span>
        <strong
          className="col-span-2 col-start-1 row-start-2 block w-full truncate text-xs leading-4 font-medium"
          title={typeName}
          data-resource-type
        >
          {typeName}
        </strong>
      </span>
      {data.related && (
        <span
          className="mt-0.5 inline-flex shrink-0 items-center gap-1 text-[10px] text-foreground"
          title={t("panorama.directRelation")}
        >
          <Link2 aria-hidden="true" className="size-3.5" />
          <span>{t("panorama.directRelation")}</span>
        </span>
      )}
    </button>
  );
  const resourceTrigger = showMembershipUnknown ? (
    <div className="nodrag nopan flex h-full w-full min-w-0 items-center">
      {resourceButton}
      <Popover>
        <PopoverTrigger asChild>
          <button
            type="button"
            className="nodrag nopan grid size-5 shrink-0 cursor-pointer touch-manipulation place-items-center rounded-full text-muted-foreground outline-none transition-colors hover:bg-muted hover:text-foreground active:bg-muted/80 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
            aria-label={t("panorama.membershipUnknownDescription")}
            title={t("panorama.membershipUnknownDescription")}
            data-membership-unknown
            onClick={(event) => event.stopPropagation()}
            onContextMenu={(event) => event.stopPropagation()}
          >
            <CircleHelp aria-hidden="true" className="size-4" />
          </button>
        </PopoverTrigger>
        <PopoverContent side="top" sideOffset={4} className="w-64 text-xs">
          {t("panorama.membershipUnknownDescription")}
        </PopoverContent>
        <span id={membershipDescriptionID} className="sr-only">
          {t("panorama.membershipUnknownDescription")}
        </span>
      </Popover>
    </div>
  ) : (
    resourceButton
  );
  return (
    <>
      <ResourceEdgeHandles />
      <span
        className={cn(
          "relative block h-full w-full rounded-lg p-0.5",
          resource.dirty && !selected && !data.pendingCleanup && "bg-muted/50",
          data.pendingCleanup && !selected && "bg-warning/5",
        )}
        data-cleanup-pending={data.pendingCleanup ? "true" : undefined}
        data-dirty-asset={resource.dirty ? "true" : undefined}
      >
        <SelectionIndicator selected={selected} />
        <DirtyAssetLabel
          dirty={Boolean(resource.dirty)}
          label={t("asset.dirty")}
        />
        <PendingCleanupLabel
          pendingCleanup={data.pendingCleanup}
          label={t("panorama.pendingCleanup")}
          className={resource.dirty ? "top-auto -bottom-2.5" : undefined}
        />
        {withContextMenu(
          resourceTrigger,
          data.contextMenu && {
            kind: "resource",
            pendingCleanup: data.pendingCleanup,
            actions: data.contextMenu,
          },
        )}
      </span>
    </>
  );
}

function ResourceGroupParentIdentity({
  resource,
  related,
}: {
  resource: TopologyResource;
  related: boolean;
}) {
  const { locale, t } = useLocale();
  const typeName = resourceTypeName(
    resource.resource_kind_id,
    resource.type_name,
    resource.type_names,
    locale,
  );
  const resourceName = resource.name.trim();
  const nativeID = resource.native_id.trim();
  const primaryLabel = resourceName || nativeID || resource.key;
  return (
    <span className="flex min-w-0 flex-1 items-center gap-2">
      <ResourceIcon icon={resource.icon} className="mt-0 shrink-0" />
      <span className="min-w-0 flex-1">
        <strong
          className="block truncate text-xs leading-4 font-medium text-foreground"
          title={primaryLabel}
        >
          {primaryLabel}
        </strong>
        <small
          className="block truncate text-[10px] leading-4 text-muted-foreground"
          title={typeName}
        >
          {typeName}
        </small>
      </span>
      {related && (
        <span
          className="inline-flex shrink-0 items-center gap-1 text-[10px] text-foreground"
          title={t("panorama.directRelation")}
        >
          <Link2 aria-hidden="true" className="size-3.5" />
          <span className="sr-only">{t("panorama.directRelation")}</span>
        </span>
      )}
    </span>
  );
}

function resourceGroupSummary(
  childTypeSummaries: readonly TopologyResourceGroupTypeSummary[],
  locale: string,
  formatNumber: (value: number) => string,
): string {
  return childTypeSummaries
    .map((summary) => {
      const typeName = resourceTypeName(
        summary.resourceKindID,
        summary.typeName,
        summary.typeNames,
        locale,
      );
      return `${typeName} ${formatNumber(summary.count)}`;
    })
    .join(" · ");
}

export function ResourceGroupNode({
  data,
  selected: flowSelected,
}: NodeProps<ResourceGroupFlowNode>) {
  const { formatNumber, locale, t } = useLocale();
  const { resource } = data;
  const selected = Boolean(data.selected || flowSelected);
  const current = data.current ?? selected;
  const childCountLabel = t("panorama.childResourceCount", {
    count: formatNumber(data.directChildCount),
  });
  const findingCountLabel =
    data.childFindingCount > 0
      ? t("panorama.findingCount", {
          count: formatNumber(data.childFindingCount),
        })
      : "";
  const typeSummary = resourceGroupSummary(
    data.childTypeSummaries,
    locale,
    formatNumber,
  );
  const primaryLabel =
    resource.name.trim() || resource.native_id.trim() || resource.key;
  const accessibleLabel = [
    primaryLabel,
    childCountLabel,
    typeSummary,
    findingCountLabel,
    ...(current ? [t("panorama.currentResource")] : []),
    ...(data.related ? [t("panorama.directRelation")] : []),
    ...(data.pendingCleanup ? [t("panorama.pendingCleanup")] : []),
    ...(resource.dirty ? [t("asset.dirty")] : []),
  ]
    .filter(Boolean)
    .join(" · ");
  const trigger = (
    <span
      className={cn(
        "relative grid h-full w-full grid-cols-[minmax(0,1fr)] grid-rows-2 rounded-lg border border-border/70 bg-background/90 shadow-sm",
        data.searchHighlighted &&
          "bg-primary/5 outline-2 outline-offset-2 outline-solid outline-primary",
        selected && "ring-2 ring-ring bg-primary/5",
        data.related && !selected && "ring-1 ring-ring/55 bg-ring/[0.03]",
        resource.dirty && !selected && !data.pendingCleanup && "bg-muted/50",
        data.pendingCleanup && !selected && "bg-warning/5",
      )}
      data-resource-group
      data-child-count={data.directChildCount}
      data-cleanup-pending={data.pendingCleanup ? "true" : undefined}
      data-dirty-asset={resource.dirty ? "true" : undefined}
    >
      <SelectionIndicator selected={selected} />
      <DirtyAssetLabel
        dirty={Boolean(resource.dirty)}
        label={t("asset.dirty")}
      />
      <PendingCleanupLabel
        pendingCleanup={data.pendingCleanup}
        label={t("panorama.pendingCleanup")}
        className={resource.dirty ? "top-auto -bottom-2.5" : undefined}
      />
      <button
        type="button"
        className="nodrag nopan flex min-h-11 min-w-0 cursor-pointer touch-manipulation items-center px-2 text-left outline-none focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring"
        aria-label={accessibleLabel}
        aria-current={current ? "true" : undefined}
        aria-pressed={selected}
        data-resource-key={resource.key}
        onClick={(event) => {
          event.stopPropagation();
          if (data.interactionEnabled === false) return;
          const nowSelected = data.onSelect?.(event.shiftKey);
          if (!event.shiftKey && nowSelected !== false) {
            data.onActivate(resource, event.currentTarget);
          }
        }}
        onContextMenu={(event) => {
          if (!data.contextMenu) {
            event.stopPropagation();
            event.preventDefault();
          }
          data.onContextMenu(resource, event.currentTarget);
        }}
      >
        <ResourceGroupParentIdentity
          resource={resource}
          related={data.related}
        />
      </button>
      <button
        type="button"
        className="nodrag nopan relative flex min-h-11 w-full min-w-0 max-w-full cursor-pointer touch-manipulation items-center gap-1.5 overflow-hidden rounded-b-[7px] border-t border-border/60 py-0 pr-12 pl-2 text-[10px] text-muted-foreground outline-none transition-colors hover:bg-muted/60 hover:text-foreground focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring"
        aria-label={t("panorama.expandChildResources", {
          name: primaryLabel,
          count: formatNumber(data.directChildCount),
        })}
        aria-expanded="false"
        data-stack-key={resource.key}
        data-stack-action="expand"
        title={typeSummary}
        onClick={(event) => {
          event.stopPropagation();
          if (data.interactionEnabled === false) return;
          data.onExpand(resource.key, event.currentTarget);
        }}
      >
        <Boxes aria-hidden="true" className="size-3.5 shrink-0" />
        <span className="min-w-0 flex-1 truncate text-left">
          {childCountLabel}
          {typeSummary ? ` · ${typeSummary}` : ""}
        </span>
        {findingCountLabel && (
          <span className="shrink-0 text-warning-foreground">
            {findingCountLabel}
          </span>
        )}
        <span
          aria-hidden="true"
          className="pointer-events-none absolute top-1/2 right-3 inline-flex size-7 -translate-y-1/2 items-center justify-center rounded-md"
          data-resource-group-toggle-icon
        >
          <ChevronDown className="size-3.5" />
        </span>
      </button>
    </span>
  );
  return (
    <>
      <ResourceEdgeHandles />
      {withContextMenu(
        trigger,
        data.contextMenu && {
          kind: "resource",
          pendingCleanup: data.pendingCleanup,
          actions: data.contextMenu,
        },
      )}
    </>
  );
}

export function ExpandedResourceGroupFrameNode({
  data,
  selected: flowSelected,
}: NodeProps<ExpandedResourceGroupFlowNode>) {
  const { formatNumber, locale, t } = useLocale();
  const { resource } = data;
  const selected = Boolean(data.selected || flowSelected);
  const current = data.current ?? selected;
  const primaryLabel =
    resource.name.trim() || resource.native_id.trim() || resource.key;
  const childCountLabel = t("panorama.childResourceCount", {
    count: formatNumber(data.directChildCount),
  });
  const typeSummary = resourceGroupSummary(
    data.childTypeSummaries,
    locale,
    formatNumber,
  );
  const accessibleLabel = [
    primaryLabel,
    childCountLabel,
    typeSummary,
    ...(current ? [t("panorama.currentResource")] : []),
    ...(data.related ? [t("panorama.directRelation")] : []),
    ...(data.pendingCleanup ? [t("panorama.pendingCleanup")] : []),
  ]
    .filter(Boolean)
    .join(" · ");
  const parentTrigger = (
    <button
      type="button"
      className="nodrag nopan flex h-full min-w-0 flex-1 cursor-pointer touch-manipulation items-center gap-2 text-left outline-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
      aria-label={accessibleLabel}
      aria-current={current ? "true" : undefined}
      aria-pressed={selected}
      data-resource-key={resource.key}
      onClick={(event) => {
        event.stopPropagation();
        if (data.interactionEnabled === false) return;
        const nowSelected = data.onSelect?.(event.shiftKey);
        if (!event.shiftKey && nowSelected !== false) {
          data.onActivate(resource, event.currentTarget);
        }
      }}
      onContextMenu={(event) => {
        if (!data.contextMenu) {
          event.stopPropagation();
          event.preventDefault();
        }
        data.onContextMenu(resource, event.currentTarget);
      }}
    >
      <ResourceIcon
        icon={resource.icon}
        className="mt-0 size-4 shrink-0 text-muted-foreground"
      />
      <strong className="min-w-0 flex-1 truncate text-xs font-medium">
        {primaryLabel}
      </strong>
      {data.related && (
        <span
          className="inline-flex shrink-0 items-center text-foreground"
          title={t("panorama.directRelation")}
        >
          <Link2 aria-hidden="true" className="size-3.5" />
          <span className="sr-only">{t("panorama.directRelation")}</span>
        </span>
      )}
    </button>
  );
  return (
    <>
      <ResourceEdgeHandles />
      <section
        className={cn(
          "relative h-full w-full rounded-lg border-l-2 border-border/70 bg-muted/[0.12]",
          data.searchHighlighted &&
            "bg-primary/5 outline-2 outline-offset-2 outline-solid outline-primary",
          selected && "ring-2 ring-ring",
          data.pendingCleanup && !selected && "bg-warning/5",
        )}
        data-resource-group
        data-resource-group-expanded
        data-child-count={data.directChildCount}
      >
        <SelectionIndicator selected={selected} />
        <DirtyAssetLabel
          dirty={Boolean(resource.dirty)}
          label={t("asset.dirty")}
        />
        <PendingCleanupLabel
          pendingCleanup={data.pendingCleanup}
          label={t("panorama.pendingCleanup")}
          className={resource.dirty ? "top-auto -bottom-2.5" : undefined}
        />
        <header
          className="flex h-10 min-w-0 items-center gap-2 px-3"
          data-resource-group-label
        >
          {withContextMenu(
            parentTrigger,
            data.contextMenu && {
              kind: "resource",
              pendingCleanup: data.pendingCleanup,
              actions: data.contextMenu,
            },
          )}
          <span
            className="shrink-0 text-[10px] text-muted-foreground tabular-nums"
            title={typeSummary}
            aria-hidden="true"
          >
            · {formatNumber(data.directChildCount)}
          </span>
          <span className="sr-only">{childCountLabel}</span>
          <button
            type="button"
            className="nodrag nopan inline-flex size-7 shrink-0 cursor-pointer touch-manipulation items-center justify-center rounded-sm bg-transparent text-foreground outline-none transition-colors hover:bg-accent focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
            aria-label={t("panorama.collapseChildResources", {
              name: primaryLabel,
            })}
            aria-expanded="true"
            data-stack-key={resource.key}
            data-stack-action="collapse"
            onClick={(event) => {
              event.stopPropagation();
              if (data.interactionEnabled === false) return;
              data.onCollapse(
                resource.key,
                data.childKeys,
                event.currentTarget,
              );
            }}
          >
            <ChevronUp aria-hidden="true" className="size-4" />
          </button>
        </header>
      </section>
    </>
  );
}

function ResourceIdentity({
  icon,
  typeName,
  badge,
  className,
}: {
  icon?: string;
  typeName: string;
  badge?: ReactNode;
  className?: string;
}) {
  return (
    <span
      className={cn(
        "flex min-w-0 flex-col items-start justify-center gap-1",
        className,
      )}
      data-resource-identity
    >
      <span className="relative shrink-0" aria-hidden="true">
        <ResourceIcon icon={icon} />
        {badge}
      </span>
      <strong
        className="block w-full truncate text-xs font-medium"
        title={typeName}
      >
        {typeName}
      </strong>
    </span>
  );
}

export function VSwitchFrameNode({
  data,
  selected: flowSelected,
}: NodeProps<VSwitchFlowNode>) {
  const { formatNumber, t } = useLocale();
  const selected = Boolean(data.selected || flowSelected);
  const metadata = [data.vSwitch.native_id, data.vSwitch.zone]
    .filter(Boolean)
    .join(" · ");
  const countLabel =
    data.vSwitch.resource_count > 0
      ? t("panorama.resourceCount", {
          count: formatNumber(data.vSwitch.resource_count),
        })
      : "";
  const frame = (
    <section
      role="button"
      tabIndex={0}
      aria-label={[
        data.vSwitch.name,
        metadata,
        countLabel,
        ...(data.pendingCleanup ? [t("panorama.pendingCleanup")] : []),
        ...(data.vSwitch.dirty ? [t("asset.dirty")] : []),
      ]
        .filter(Boolean)
        .join(" · ")}
      aria-pressed={selected}
      className={cn(
        "nodrag nopan relative h-full w-full cursor-default rounded-lg border border-border/70 bg-transparent outline-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring",
        data.searchHighlighted &&
          "bg-primary/5 outline-2 outline-offset-2 outline-solid outline-primary",
        selected && "ring-2 ring-ring bg-primary/5",
        data.vSwitch.dirty &&
          !selected &&
          !data.pendingCleanup &&
          "bg-muted/50",
        data.pendingCleanup && !selected && "bg-warning/5",
      )}
      data-cleanup-pending={data.pendingCleanup ? "true" : undefined}
      data-dirty-asset={data.vSwitch.dirty ? "true" : undefined}
      data-search-highlighted={data.searchHighlighted ? "true" : undefined}
      onClick={(event) => {
        event.stopPropagation();
        if (!data.interactionEnabled) return;
        data.onSelect?.(event.shiftKey);
      }}
      onKeyDown={(event) => {
        if (
          !data.interactionEnabled ||
          (event.key !== "Enter" && event.key !== " ")
        ) {
          return;
        }
        event.preventDefault();
        event.stopPropagation();
        data.onSelect?.(event.shiftKey);
      }}
      onContextMenu={(event) => {
        event.stopPropagation();
        if (!data.contextMenu) event.preventDefault();
        data.onContextMenu?.(event.currentTarget);
      }}
    >
      <SelectionIndicator selected={selected} />
      <DirtyAssetLabel
        dirty={Boolean(data.vSwitch.dirty)}
        label={t("asset.dirty")}
      />
      <PendingCleanupLabel
        pendingCleanup={data.pendingCleanup}
        label={t("panorama.pendingCleanup")}
        className={data.vSwitch.dirty ? "top-auto -bottom-2.5" : undefined}
      />
      <header
        className="grid h-14 min-w-0 grid-cols-[1rem_minmax(0,1fr)_auto] grid-rows-2 items-center gap-x-2 px-3 py-2"
        data-vswitch-summary
      >
        <ResourceIcon
          icon={VSWITCH_ICON_SRC}
          className="row-span-2 mt-0 size-4 self-center text-muted-foreground"
        />
        <strong
          className="col-start-2 row-start-1 min-w-0 self-end truncate text-xs leading-4 font-medium"
          data-vswitch-name
          title={data.vSwitch.name}
        >
          {data.vSwitch.name}
        </strong>
        {countLabel && (
          <span
            className="col-start-3 row-start-1 self-end whitespace-nowrap text-[10px] leading-4 text-muted-foreground"
            data-vswitch-resource-count
          >
            {countLabel}
          </span>
        )}
        <small
          className="col-span-2 col-start-2 row-start-2 min-w-0 self-start truncate text-[10px] leading-3 text-muted-foreground"
          data-vswitch-metadata
          title={metadata}
        >
          {metadata}
        </small>
      </header>
    </section>
  );
  return withContextMenu(
    frame,
    data.contextMenu
      ? {
          kind: "vswitch",
          pendingCleanup: data.pendingCleanup,
          actions: data.contextMenu,
        }
      : undefined,
  );
}

export function ResourceStackNode({
  data,
  selected: flowSelected,
}: NodeProps<StackFlowNode>) {
  const { formatNumber, locale, t } = useLocale();
  const typeName = resourceTypeName(
    data.resourceKindID,
    data.typeName,
    data.typeNames,
    locale,
  );
  const countLabel = t("panorama.resourceCount", {
    count: formatNumber(data.count),
  });
  const partialCleanup =
    data.pendingCleanupCount > 0 && data.pendingCleanupCount < data.count;
  const selected = Boolean(data.selected || flowSelected);
  const cleanupStateLabel = data.pendingCleanup
    ? t("panorama.pendingCleanup")
    : partialCleanup
      ? t("panorama.pendingCleanupFraction", {
          selected: formatNumber(data.pendingCleanupCount),
          total: formatNumber(data.count),
        })
      : "";
  const accessibleLabel = [typeName, countLabel, cleanupStateLabel]
    .filter(Boolean)
    .join(" · ");
  const trigger = (
    <div
      className="relative h-full w-full"
      onContextMenu={(event) => {
        if (!data.contextMenu) event.preventDefault();
        const trigger =
          event.target instanceof Element
            ? event.target.closest<HTMLElement>("button")
            : null;
        data.onContextMenu(data.stackKey, trigger ?? event.currentTarget);
      }}
    >
      <ResourceEdgeHandles />
      <button
        type="button"
        className={cn(
          "nodrag nopan flex h-full w-full flex-col items-start justify-center gap-1 rounded-lg bg-transparent p-1 text-left outline-none transition-colors",
          "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring",
          data.searchHighlighted &&
            "bg-primary/5 outline-2 outline-offset-2 outline-solid outline-primary",
          selected && "ring-2 ring-ring bg-primary/5",
          (data.pendingCleanup || partialCleanup) &&
            !selected &&
            "bg-warning/5",
        )}
        aria-label={accessibleLabel}
        aria-pressed={selected}
        data-stack-key={data.stackKey}
        data-stack-action="select"
        data-cleanup-pending={data.pendingCleanup ? "true" : undefined}
        data-search-highlighted={data.searchHighlighted ? "true" : undefined}
        onClick={(event) => {
          event.stopPropagation();
          if (data.interactionEnabled === false) return;
          data.onSelect?.(data.stackKey, event.currentTarget, event.shiftKey);
        }}
        onDoubleClick={(event) => {
          event.stopPropagation();
          if (data.interactionEnabled === false) return;
          data.onExpand(data.stackKey, event.currentTarget);
        }}
      >
        <SelectionIndicator selected={selected} />
        <PendingCleanupLabel
          pendingCleanup={cleanupStateLabel.length > 0}
          label={cleanupStateLabel}
        />
        <ResourceIdentity
          icon={data.icon}
          typeName={typeName}
          className="w-full"
        />
      </button>
      <button
        type="button"
        className="nodrag nopan absolute top-3 left-5 grid size-7 place-items-center rounded-full outline-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
        aria-label={t("panorama.expandCleanupBatch", { name: typeName })}
        data-stack-key={data.stackKey}
        data-stack-action="expand"
        onClick={(event) => {
          event.stopPropagation();
          if (data.interactionEnabled === false) return;
          data.onExpand(data.stackKey, event.currentTarget);
        }}
      >
        <Badge
          data-count-badge
          className="h-4 min-w-4 px-1 py-0 text-[9px] leading-none tabular-nums"
        >
          {formatNumber(data.count)}
        </Badge>
      </button>
    </div>
  );
  return withContextMenu(
    trigger,
    data.contextMenu && {
      kind: "stack",
      pendingCleanup: data.pendingCleanup,
      actions: data.contextMenu,
    },
  );
}

export function ExpandedStackFrameNode({
  data,
}: NodeProps<ExpandedStackFlowNode>) {
  const { formatNumber, locale, t } = useLocale();
  const typeName = resourceTypeName(
    data.resourceKindID,
    data.typeName,
    data.typeNames,
    locale,
  );
  const countLabel = t("panorama.resourceCount", {
    count: formatNumber(data.count),
  });
  const trigger = (
    <section
      className={cn(
        "relative h-full w-full rounded-lg border-l-2 border-border/70 bg-muted/[0.12]",
        data.searchHighlighted &&
          "bg-primary/5 outline-2 outline-offset-2 outline-solid outline-primary",
        data.pendingCleanup && "bg-warning/5",
      )}
      data-stack-group
      data-search-highlighted={data.searchHighlighted ? "true" : undefined}
      data-cleanup-pending={data.pendingCleanup ? "true" : undefined}
      onContextMenu={(event) => {
        event.stopPropagation();
        if (!data.contextMenu) event.preventDefault();
        data.onContextMenu?.(data.stackKey, event.currentTarget);
      }}
    >
      <PendingCleanupLabel
        pendingCleanup={data.pendingCleanup}
        label={t("panorama.pendingCleanup")}
      />
      <header
        className="flex h-10 items-center gap-2 px-3"
        data-stack-group-label
      >
        <ResourceIcon
          icon={data.icon}
          className="mt-0 size-4 text-muted-foreground"
        />
        <strong className="min-w-0 flex-1 truncate text-xs font-medium">
          {typeName}
        </strong>
        <span
          className="text-[10px] text-muted-foreground tabular-nums"
          aria-hidden="true"
        >
          · {formatNumber(data.count)}
        </span>
        <span className="sr-only">{countLabel}</span>
        <button
          type="button"
          className="nodrag nopan inline-flex size-7 items-center justify-center rounded-sm bg-transparent text-foreground outline-none hover:bg-accent focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
          aria-label={t("panorama.collapse")}
          data-stack-key={data.stackKey}
          data-stack-action="collapse"
          onClick={(event) => {
            event.stopPropagation();
            if (data.interactionEnabled === false) return;
            data.onCollapse(data.stackKey, event.currentTarget);
          }}
        >
          <ChevronUp aria-hidden="true" className="size-3.5" />
        </button>
      </header>
    </section>
  );
  return withContextMenu(
    trigger,
    data.contextMenu && {
      kind: "stack",
      pendingCleanup: data.pendingCleanup,
      actions: data.contextMenu,
    },
  );
}

export function VSwitchStackNode({
  data,
  selected: flowSelected,
}: NodeProps<VSwitchStackFlowNode>) {
  const { formatNumber, t } = useLocale();
  const countLabel = t("panorama.emptyVSwitches", {
    count: formatNumber(data.count),
  });
  const name = t("panorama.emptyVSwitchesLabel");
  const selected = Boolean(data.selected || flowSelected);
  const partialCleanup =
    data.pendingCleanupCount > 0 && data.pendingCleanupCount < data.count;
  const cleanupStateLabel = data.pendingCleanup
    ? t("panorama.pendingCleanup")
    : partialCleanup
      ? t("panorama.pendingCleanupFraction", {
          selected: formatNumber(data.pendingCleanupCount),
          total: formatNumber(data.count),
        })
      : "";
  const trigger = (
    <div
      className="relative h-full w-full"
      onContextMenu={(event) => {
        event.stopPropagation();
        if (!data.contextMenu) event.preventDefault();
        data.onContextMenu?.(event.currentTarget);
      }}
    >
      <button
        type="button"
        className={cn(
          "nodrag nopan flex h-full w-full flex-col items-start justify-center gap-1 rounded-lg bg-transparent p-1 text-left outline-none transition-colors",
          "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring",
          data.searchHighlighted &&
            "bg-primary/5 outline-2 outline-offset-2 outline-solid outline-primary",
          selected && "ring-2 ring-ring bg-primary/5",
          (data.pendingCleanup || partialCleanup) &&
            !selected &&
            "bg-warning/5",
        )}
        aria-label={[countLabel, cleanupStateLabel].filter(Boolean).join(" · ")}
        aria-pressed={selected}
        data-stack-key={data.stackKey}
        data-stack-action="select"
        data-cleanup-pending={data.pendingCleanup ? "true" : undefined}
        data-search-highlighted={data.searchHighlighted ? "true" : undefined}
        onClick={(event) => {
          event.stopPropagation();
          if (data.interactionEnabled === false) return;
          data.onSelect?.(data.cleanupTargets, event.shiftKey);
        }}
        onDoubleClick={(event) => {
          event.stopPropagation();
          if (data.interactionEnabled === false) return;
          data.onExpand(data.stackKey, event.currentTarget);
        }}
      >
        <SelectionIndicator selected={selected} />
        <PendingCleanupLabel
          pendingCleanup={cleanupStateLabel.length > 0}
          label={cleanupStateLabel}
        />
        <span className="shrink-0" aria-hidden="true">
          <ResourceIcon icon={VSWITCH_ICON_SRC} />
        </span>
        <span className="min-w-0">
          <strong className="block truncate text-xs font-medium">{name}</strong>
        </span>
      </button>
      <button
        type="button"
        className="nodrag nopan absolute top-3 left-5 grid size-7 place-items-center rounded-full outline-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
        aria-label={t("panorama.expandCleanupBatch", { name })}
        data-stack-key={data.stackKey}
        data-stack-action="expand"
        onClick={(event) => {
          event.stopPropagation();
          if (data.interactionEnabled === false) return;
          data.onExpand(data.stackKey, event.currentTarget);
        }}
      >
        <Badge
          data-count-badge
          className="h-4 min-w-4 px-1 py-0 text-[9px] leading-none tabular-nums"
        >
          {formatNumber(data.count)}
        </Badge>
      </button>
    </div>
  );
  return withContextMenu(
    trigger,
    data.contextMenu && {
      kind: "vswitch",
      pendingCleanup: data.pendingCleanup,
      actions: data.contextMenu,
    },
  );
}

export function ExpandedVSwitchStackFrameNode({
  data,
}: NodeProps<ExpandedVSwitchStackFlowNode>) {
  const { formatNumber, t } = useLocale();
  const countLabel = t("panorama.emptyVSwitches", {
    count: formatNumber(data.count),
  });
  const name = t("panorama.emptyVSwitchesLabel");
  return (
    <section
      className="relative h-full w-full rounded-lg border-l-2 border-border/70 bg-muted/[0.12]"
      data-vswitch-stack-expanded
    >
      <header
        className="flex h-10 items-center gap-2 px-3"
        data-vswitch-stack-header
      >
        <ResourceIcon
          icon={VSWITCH_ICON_SRC}
          className="mt-0 size-4 text-muted-foreground"
        />
        <strong className="min-w-0 flex-1 truncate text-xs font-medium">
          {name}
        </strong>
        <span
          className="text-[10px] text-muted-foreground tabular-nums"
          aria-hidden="true"
        >
          · {formatNumber(data.count)}
        </span>
        <span className="sr-only">{countLabel}</span>
        <button
          type="button"
          className="nodrag nopan inline-flex size-7 items-center justify-center rounded-sm bg-transparent text-foreground outline-none hover:bg-accent focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
          aria-label={t("panorama.collapse")}
          data-stack-key={data.stackKey}
          data-stack-action="collapse"
          onClick={(event) => {
            event.stopPropagation();
            if (data.interactionEnabled === false) return;
            data.onCollapse(data.stackKey, event.currentTarget);
          }}
        >
          <ChevronUp aria-hidden="true" className="size-3.5" />
        </button>
      </header>
      <div className="px-6 pb-6">
        <div
          className="grid gap-5"
          data-vswitch-stack-grid
          style={{
            gridTemplateColumns: `repeat(${Math.min(4, data.vSwitches.length)}, minmax(0, 1fr))`,
            gridAutoRows: "72px",
          }}
        >
          {data.vSwitches.map((vSwitch) => {
            const cleanupTarget = data.cleanupTargetsByVSwitchKey.get(
              vSwitch.key,
            );
            const selected =
              cleanupTarget !== undefined &&
              data.selectedTargetKeys.has(cleanupTarget.key);
            const pendingCleanup =
              cleanupTarget !== undefined &&
              data.pendingTargetKeys.has(cleanupTarget.key);
            return (
              <Fragment key={vSwitch.key}>
                {withContextMenu(
                  <section
                    role="button"
                    aria-label={[
                      vSwitch.name,
                      vSwitch.native_id,
                      vSwitch.zone,
                      ...(pendingCleanup ? [t("panorama.pendingCleanup")] : []),
                      ...(vSwitch.dirty ? [t("asset.dirty")] : []),
                    ]
                      .filter(Boolean)
                      .join(" · ")}
                    aria-pressed={selected}
                    className={cn(
                      "nodrag nopan relative flex h-[72px] cursor-default items-center gap-2 rounded-lg border border-border/70 bg-transparent px-3 shadow-none outline-none transition-colors hover:bg-accent/40 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring",
                      data.highlightedVSwitchKey === vSwitch.key &&
                        "outline-2 outline-offset-2 outline-primary",
                      selected && "ring-2 ring-ring bg-primary/5",
                      vSwitch.dirty &&
                        !selected &&
                        !pendingCleanup &&
                        "bg-muted/50",
                      pendingCleanup && !selected && "bg-warning/5",
                    )}
                    tabIndex={0}
                    data-search-highlighted={
                      data.highlightedVSwitchKey === vSwitch.key
                        ? "true"
                        : undefined
                    }
                    data-dirty-asset={vSwitch.dirty ? "true" : undefined}
                    onClick={(event) => {
                      event.stopPropagation();
                      if (!data.interactionEnabled || !cleanupTarget) return;
                      data.onSelect(cleanupTarget, event.shiftKey);
                    }}
                    onKeyDown={(event) => {
                      if (
                        !data.interactionEnabled ||
                        !cleanupTarget ||
                        (event.key !== "Enter" && event.key !== " ")
                      ) {
                        return;
                      }
                      event.preventDefault();
                      event.stopPropagation();
                      data.onSelect(cleanupTarget, event.shiftKey);
                    }}
                    onContextMenu={(event) => {
                      event.stopPropagation();
                      if (!data.contextMenus?.has(vSwitch.key)) {
                        event.preventDefault();
                      }
                      if (cleanupTarget) data.onContextMenu?.(cleanupTarget);
                    }}
                  >
                    <SelectionIndicator selected={selected} />
                    <DirtyAssetLabel
                      dirty={Boolean(vSwitch.dirty)}
                      label={t("asset.dirty")}
                    />
                    <PendingCleanupLabel
                      pendingCleanup={pendingCleanup}
                      label={t("panorama.pendingCleanup")}
                      className={
                        vSwitch.dirty ? "top-auto -bottom-2.5" : undefined
                      }
                    />
                    <ResourceIcon
                      icon={VSWITCH_ICON_SRC}
                      className="mt-0 size-4 text-muted-foreground"
                    />
                    <span className="min-w-0 flex-1">
                      <strong
                        className="block truncate text-xs font-medium"
                        title={vSwitch.name}
                      >
                        {vSwitch.name}
                      </strong>
                      <small className="block whitespace-nowrap text-[10px] leading-3 text-muted-foreground">
                        {vSwitch.native_id}
                      </small>
                      {vSwitch.zone && (
                        <small className="block truncate text-[10px] leading-3 text-muted-foreground">
                          {vSwitch.zone}
                        </small>
                      )}
                    </span>
                  </section>,
                  data.contextMenus?.get(vSwitch.key)
                    ? {
                        kind: "vswitch",
                        pendingCleanup,
                        actions: data.contextMenus.get(vSwitch.key)!,
                      }
                    : undefined,
                )}
              </Fragment>
            );
          })}
        </div>
      </div>
    </section>
  );
}

export { resourceTypeName } from "@/components/domain/resourceKindLabel";
