import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type MouseEvent as ReactMouseEvent,
  type PointerEvent as ReactPointerEvent,
} from "react";
import {
  Background,
  ReactFlow,
  type Node,
  type NodeProps,
  type ReactFlowInstance,
  type Viewport,
} from "@xyflow/react";
import { ChevronUp } from "lucide-react";
import type {
  AccountTopologyView,
  RegionTopologyView,
  ResourceKind,
  TopologyEntrySummary,
} from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { useRequiredConnection } from "@/connections/ActiveConnectionProvider";
import { useLocale } from "@/i18n/LocaleProvider";
import { cn } from "@/lib/utils";
import { selectorKey } from "../cleanup/selection";
import { BoxSelectionActionBar } from "./BoxSelectionActionBar";
import { completeBoxSelection, type CanvasMode } from "./boxSelection";
import { CanvasToolbar } from "./CanvasToolbar";
import { cloudConsoleURL } from "./consoleLinks";
import { isCleanupTargetPending, type CleanupTarget } from "./cleanupSelection";
import { useCleanupSelection } from "./CleanupSelectionContext";
import { entryCleanupTarget } from "./cleanupTargets";
import {
  NetworkResourceDetailDialog,
  type NetworkResourceDetail,
} from "./NetworkResourceDetailDialog";
import { ScopeDetailDialog } from "./ScopeDetailDialog";
import { TopologyContextMenu } from "./TopologyContextMenu";
import {
  DirtyAssetLabel,
  PendingCleanupLabel,
  REGION_ICON_SRC,
  ResourceIcon,
  SelectionIndicator,
  VPC_ICON_SRC,
} from "./TopologyNodes";

const SUMMARY_WIDTH = 256;
const SUMMARY_HEIGHT = 72;
const SUMMARY_X_GAP = 20;
const SUMMARY_Y_GAP = 16;
const SUMMARY_MAX_ZOOM = 2.5;
const SUMMARY_STACK_FIT_PADDING = 0.04;
const EXPANDED_COLUMNS = 4;
const EMPTY_SCOPE_EXPANDED_HEADER_HEIGHT = 40;
const EMPTY_SCOPE_EXPANDED_X_GAP = 20;
const EMPTY_SCOPE_EXPANDED_Y_GAP = 20;
const EMPTY_SCOPE_EXPANDED_CONTENT_INSET = 48;
const EMPTY_SCOPE_EXPANDED_CONTENT_PADDING_Y = 24;
const EMPTY_RESOURCE_KINDS = new Map<string, ResourceKind>();

function networkNativeType(provider: string, kind: "vpc" | "vswitch") {
  if (provider === "alicloud") {
    return kind === "vpc" ? "ACS::VPC::VPC" : "ACS::VPC::VSwitch";
  }
  if (provider === "aws") {
    return kind === "vpc" ? "AWS::EC2::VPC" : "AWS::EC2::Subnet";
  }
  return "";
}

type SummaryEntryKind = "region" | "vpc";

function summaryColumnCount(
  count: number,
  canvasSize: { width: number; height: number },
) {
  if (count <= 1) return 1;
  let bestColumns = 1;
  let bestOccupiedArea = -1;
  for (let columns = 1; columns <= count; columns += 1) {
    const rows = Math.ceil(count / columns);
    const contentWidth =
      columns * SUMMARY_WIDTH + Math.max(0, columns - 1) * SUMMARY_X_GAP;
    const contentHeight =
      rows * SUMMARY_HEIGHT + Math.max(0, rows - 1) * SUMMARY_Y_GAP;
    const scale = Math.min(
      SUMMARY_MAX_ZOOM,
      canvasSize.width / contentWidth,
      canvasSize.height / contentHeight,
    );
    const occupiedArea = contentWidth * contentHeight * scale * scale;
    if (occupiedArea > bestOccupiedArea) {
      bestColumns = columns;
      bestOccupiedArea = occupiedArea;
    }
  }
  return bestColumns;
}

interface SummaryNodeData extends Record<string, unknown> {
  entry: TopologyEntrySummary;
  entryKind?: SummaryEntryKind;
  coverageStatus: string;
  isAccountGlobal: boolean;
  isRegionPublic: boolean;
  cleanupTarget: CleanupTarget | null;
  selected: boolean;
  searchHighlighted: boolean;
  embedded?: boolean;
  pendingCleanup: boolean;
  interactionEnabled: boolean;
  contextMenu?: SummaryContextMenuData;
  onSelect: (target: CleanupTarget, additive: boolean) => void;
  onNavigate: (entry: TopologyEntrySummary) => void;
}

type SummaryFlowNode = Node<SummaryNodeData, "summary">;

interface ZeroStackNodeData extends Record<string, unknown> {
  stackKey: string;
  label: string;
  name: string;
  count: number;
  entryKind: SummaryEntryKind;
  cleanupTargets: readonly CleanupTarget[];
  selected: boolean;
  searchHighlighted: boolean;
  pendingCleanupCount: number;
  interactionEnabled: boolean;
  contextMenu: SummaryContextMenuData;
  onSelect: (targets: readonly CleanupTarget[], additive: boolean) => void;
  onExpand: (stackKey: string) => void;
}

type ZeroStackFlowNode = Node<ZeroStackNodeData, "zeroStack">;

interface ExpandedZeroStackNodeData extends Record<string, unknown> {
  stackKey: string;
  label: string;
  name: string;
  entries: readonly TopologyEntrySummary[];
  entryKind: SummaryEntryKind;
  coverageStatus: string;
  cleanupTargets: readonly CleanupTarget[];
  selectedTargetKeys: ReadonlySet<string>;
  highlightedEntryKey?: string;
  pendingCleanupCount: number;
  interactionEnabled: boolean;
  contextMenus: ReadonlyMap<string, SummaryContextMenuData>;
  onSelect: (target: CleanupTarget, additive: boolean) => void;
  onNavigate: (entry: TopologyEntrySummary) => void;
  onCollapse: (stackKey: string) => void;
}

type ExpandedZeroStackFlowNode = Node<
  ExpandedZeroStackNodeData,
  "expandedZeroStack"
>;

type SummaryCanvasNode =
  SummaryFlowNode | ZeroStackFlowNode | ExpandedZeroStackFlowNode;

function summaryNodeContainsTarget(
  node: SummaryCanvasNode,
  targetKey: string,
): boolean {
  switch (node.type) {
    case "summary":
      return node.data.entry.key === targetKey;
    case "zeroStack":
      return node.data.cleanupTargets.some(
        (target) => target.key === targetKey,
      );
    case "expandedZeroStack":
      return node.data.entries.some((entry) => entry.key === targetKey);
  }
}

interface PendingStackFocus {
  key: string;
  action: "expand" | "collapse";
}

type PendingViewportTransition =
  | { kind: "focus"; stackKey: string }
  | { kind: "restore"; stackKey: string; viewport: Viewport };

interface CanvasPanGesture {
  pointerID: number;
  origin: { x: number; y: number };
  viewport: Viewport;
  moved: boolean;
}

interface SummaryContextMenuData {
  kind: SummaryEntryKind | "stack";
  pendingCleanup: boolean;
  inheritedCleanup: boolean;
  dirtyAsset?: {
    connectionId: string;
    id: string;
    dirty: boolean;
  };
  onViewDetails: () => void;
  consoleURL?: string;
  onRescan?: () => void;
  onAddToCleanup?: () => void;
  onRemoveFromCleanup?: () => void;
  onOpenChange: (open: boolean) => void;
}

interface ScopeDetailState {
  kind: SummaryEntryKind;
  entry: TopologyEntrySummary;
}

const summaryNodeTypes = {
  summary: SummaryFrameNode,
  zeroStack: ZeroStackNode,
  expandedZeroStack: ExpandedZeroStackNode,
};

function SummaryFrameNode({
  data,
  selected: flowSelected,
}: NodeProps<SummaryFlowNode>) {
  const { formatNumber, t } = useLocale();
  const name =
    data.isAccountGlobal || data.isRegionPublic
      ? t("panorama.regionGlobalResources")
      : data.entry.name;
  const count =
    data.entry.resource_count > 0
      ? t("panorama.resourceCount", {
          count: formatNumber(data.entry.resource_count),
        })
      : null;
  const selected = Boolean(data.selected || flowSelected);

  const button = (
    <button
      type="button"
      className={cn(
        "nodrag nopan nowheel relative flex h-full w-full min-w-0 items-center rounded-lg px-3 py-2 text-left outline-none transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring",
        "border border-border bg-background/95 shadow-sm hover:bg-accent/50",
        data.searchHighlighted &&
          "bg-primary/5 outline-2 outline-offset-2 outline-solid outline-primary",
        selected && "ring-2 ring-ring bg-primary/5",
        data.entry.dirty && !selected && !data.pendingCleanup && "bg-muted/50",
        data.pendingCleanup && !selected && "bg-warning/5",
      )}
      aria-pressed={selected}
      data-search-highlighted={data.searchHighlighted ? "true" : undefined}
      data-cleanup-pending={data.pendingCleanup ? "true" : undefined}
      data-dirty-asset={data.entry.dirty ? "true" : undefined}
      onClick={(event) => {
        event.stopPropagation();
        if (!data.interactionEnabled) return;
        if (event.shiftKey && data.cleanupTarget) {
          data.onSelect(data.cleanupTarget, true);
          return;
        }
        data.onNavigate(data.entry);
      }}
    >
      <SelectionIndicator selected={selected} />
      <DirtyAssetLabel
        dirty={Boolean(data.entry.dirty)}
        label={t("asset.dirty")}
      />
      <PendingCleanupLabel
        pendingCleanup={data.pendingCleanup}
        label={t("panorama.pendingCleanup")}
        className={data.entry.dirty ? "top-auto -bottom-2.5" : undefined}
      />
      <span
        data-summary-primary
        className={cn(
          "flex min-w-0 flex-1 items-center gap-2",
          !data.embedded && count !== null && "pr-12",
        )}
      >
        <SummaryEntryIcon kind={data.entryKind} />
        <span data-summary-identity className="min-w-0 flex-1">
          <strong
            data-summary-name
            className="block truncate text-xs leading-4 font-medium"
            title={name}
          >
            {name}
          </strong>
          {data.entryKind && data.entry.native_id && (
            <small
              data-summary-native-id
              className="block whitespace-nowrap text-[10px] leading-3 text-muted-foreground"
            >
              {data.entry.native_id}
            </small>
          )}
        </span>
      </span>
      {!data.embedded && count !== null && (
        <small
          data-summary-resource-count
          className="absolute right-3 bottom-1 shrink-0 text-[10px] leading-3 text-muted-foreground tabular-nums"
        >
          {count}
        </small>
      )}
      {data.pendingCleanup && (
        <span className="sr-only">{t("panorama.pendingCleanup")}</span>
      )}
    </button>
  );
  if (!data.contextMenu) return button;
  return (
    <TopologyContextMenu
      kind={data.contextMenu.kind}
      pendingCleanup={data.contextMenu.pendingCleanup}
      inheritedCleanup={data.contextMenu.inheritedCleanup}
      onViewDetails={data.contextMenu.onViewDetails}
      consoleURL={data.contextMenu.consoleURL}
      onRescan={data.contextMenu.onRescan}
      onAddToCleanup={data.contextMenu.onAddToCleanup}
      onRemoveFromCleanup={data.contextMenu.onRemoveFromCleanup}
      dirtyAsset={data.contextMenu.dirtyAsset}
      onOpenChange={data.contextMenu.onOpenChange}
    >
      {button}
    </TopologyContextMenu>
  );
}

function SummaryEntryIcon({ kind }: { kind?: SummaryEntryKind }) {
  if (kind === "region") {
    return (
      <span
        aria-hidden="true"
        className="shrink-0"
        data-summary-entry-icon="region"
      >
        <ResourceIcon
          icon={REGION_ICON_SRC}
          className="mt-0 size-4 text-muted-foreground"
        />
      </span>
    );
  }
  if (kind === "vpc") {
    return (
      <span
        aria-hidden="true"
        className="shrink-0"
        data-summary-entry-icon="vpc"
      >
        <ResourceIcon
          icon={VPC_ICON_SRC}
          className="mt-0 size-4 text-muted-foreground"
        />
      </span>
    );
  }
  return null;
}

function ZeroStackNode({ data }: NodeProps<ZeroStackFlowNode>) {
  const { formatNumber, t } = useLocale();
  const pendingCleanup = data.pendingCleanupCount > 0;
  const fullyPending = data.pendingCleanupCount === data.count;
  const button = (
    <button
      type="button"
      className={cn(
        "nodrag nopan nowheel relative h-full w-full rounded-lg bg-transparent text-left outline-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring",
        data.searchHighlighted &&
          "bg-primary/5 outline-2 outline-offset-2 outline-solid outline-primary",
        data.selected && "ring-2 ring-ring bg-primary/5",
        pendingCleanup && !data.selected && "bg-warning/5",
      )}
      aria-pressed={data.selected}
      data-search-highlighted={data.searchHighlighted ? "true" : undefined}
      aria-label={`${data.label}${
        pendingCleanup
          ? ` ${t("panorama.pendingCleanupFraction", {
              selected: data.pendingCleanupCount,
              total: data.count,
            })}`
          : ""
      }`}
      data-cleanup-pending={pendingCleanup ? "true" : undefined}
      data-empty-stack-key={data.stackKey}
      data-empty-stack-action="expand"
      onClick={(event) => {
        event.stopPropagation();
        if (!data.interactionEnabled) return;
        if (event.shiftKey) {
          data.onSelect(data.cleanupTargets, true);
          return;
        }
        data.onExpand(data.stackKey);
      }}
    >
      <SelectionIndicator selected={data.selected} />
      <PendingCleanupLabel
        pendingCleanup={pendingCleanup}
        label={t("panorama.pendingCleanupFraction", {
          selected: data.pendingCleanupCount,
          total: data.count,
        })}
        className="right-8"
      />
      <span
        className="relative flex h-full w-full items-center gap-2 rounded-lg border border-border bg-background/95 px-3 py-2 shadow-sm"
        data-empty-stack-frame
      >
        <Badge
          data-count-badge
          className="absolute -top-2 -right-2 z-10 min-w-6 px-1.5 tabular-nums"
        >
          {formatNumber(data.count)}
        </Badge>
        <SummaryEntryIcon kind={data.entryKind} />
        <strong className="min-w-0 flex-1 truncate text-xs font-medium">
          {data.name}
        </strong>
      </span>
    </button>
  );
  return (
    <TopologyContextMenu
      kind="stack"
      pendingCleanup={fullyPending}
      inheritedCleanup={data.contextMenu.inheritedCleanup}
      onViewDetails={data.contextMenu.onViewDetails}
      onAddToCleanup={data.contextMenu.onAddToCleanup}
      onRemoveFromCleanup={data.contextMenu.onRemoveFromCleanup}
      onOpenChange={data.contextMenu.onOpenChange}
    >
      {button}
    </TopologyContextMenu>
  );
}

function ExpandedZeroStackNode({ data }: NodeProps<ExpandedZeroStackFlowNode>) {
  const { formatNumber, t } = useLocale();
  const grid = (
    <div
      className="grid gap-5"
      data-empty-stack-grid
      style={{
        gridTemplateColumns: `repeat(${Math.min(EXPANDED_COLUMNS, data.entries.length)}, minmax(0, 1fr))`,
        gridAutoRows: `${SUMMARY_HEIGHT}px`,
      }}
    >
      {data.entries.map((entry) => (
        <SummaryFrameNode
          key={entry.key}
          data={{
            entry,
            entryKind: data.entryKind,
            coverageStatus: data.coverageStatus,
            isAccountGlobal: false,
            isRegionPublic: false,
            cleanupTarget:
              data.cleanupTargets.find((target) => target.key === entry.key) ??
              null,
            selected: data.selectedTargetKeys.has(entry.key),
            searchHighlighted: data.highlightedEntryKey === entry.key,
            embedded: true,
            pendingCleanup: data.contextMenus.get(entry.key)?.pendingCleanup
              ? true
              : false,
            interactionEnabled: data.interactionEnabled,
            contextMenu: data.contextMenus.get(entry.key),
            onSelect: data.onSelect,
            onNavigate: data.onNavigate,
          }}
          id={entry.key}
          type="summary"
          selected={false}
          dragging={false}
          zIndex={0}
          draggable={false}
          selectable={false}
          deletable={false}
          isConnectable={false}
          positionAbsoluteX={0}
          positionAbsoluteY={0}
        />
      ))}
    </div>
  );
  return (
    <section
      className="relative h-full w-full rounded-lg border-l-2 border-border/70 bg-muted/[0.12]"
      data-empty-stack-expanded
    >
      <header
        className="flex h-10 items-center gap-2 px-3"
        data-empty-stack-header
      >
        <SummaryEntryIcon kind={data.entryKind} />
        <strong className="min-w-0 flex-1 truncate text-xs font-medium">
          {data.name}
        </strong>
        <span
          className="text-[10px] text-muted-foreground tabular-nums"
          aria-hidden="true"
        >
          · {formatNumber(data.entries.length)}
        </span>
        <span className="sr-only">{data.label}</span>
        <button
          type="button"
          className="nodrag nopan nowheel inline-flex size-7 items-center justify-center rounded-sm bg-transparent text-foreground outline-none hover:bg-accent focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
          aria-label={t("panorama.collapse")}
          data-empty-stack-key={data.stackKey}
          data-empty-stack-action="collapse"
          onClick={(event) => {
            event.stopPropagation();
            if (!data.interactionEnabled) return;
            data.onCollapse(data.stackKey);
          }}
        >
          <ChevronUp aria-hidden="true" className="size-3.5" />
        </button>
      </header>
      <div className="px-6 pb-6">{grid}</div>
    </section>
  );
}

function expandedZeroStackSize(entryCount: number) {
  const columns = Math.min(EXPANDED_COLUMNS, entryCount);
  const rows = Math.ceil(entryCount / EXPANDED_COLUMNS);
  return {
    width:
      EMPTY_SCOPE_EXPANDED_CONTENT_INSET +
      columns * SUMMARY_WIDTH +
      Math.max(0, columns - 1) * EMPTY_SCOPE_EXPANDED_X_GAP,
    height:
      EMPTY_SCOPE_EXPANDED_HEADER_HEIGHT +
      EMPTY_SCOPE_EXPANDED_CONTENT_PADDING_Y +
      rows * SUMMARY_HEIGHT +
      Math.max(0, rows - 1) * EMPTY_SCOPE_EXPANDED_Y_GAP,
  };
}

function targetSelectors(target: CleanupTarget) {
  return Array.isArray(target.selector) ? target.selector : [target.selector];
}

function directCleanupTargetKeys(
  targets: readonly CleanupTarget[],
  candidate: CleanupTarget,
): string[] {
  const candidateSelectorKeys = new Set(
    targetSelectors(candidate).map(selectorKey),
  );
  return targets.flatMap((target) => {
    if (target.connectionId !== candidate.connectionId) return [];
    if (target.key === candidate.key) return [target.key];
    return targetSelectors(target).some((selector) =>
      candidateSelectorKeys.has(selectorKey(selector)),
    )
      ? [target.key]
      : [];
  });
}

export function TopologySummaryView({
  view,
  complete = true,
  coverageStatus,
  resourceKinds = EMPTY_RESOURCE_KINDS,
  highlightedEntryKey,
  focusedCleanupTargetKey,
  onNavigate,
  onRescanRegions,
}: {
  view: AccountTopologyView | RegionTopologyView;
  complete?: boolean;
  coverageStatus: string;
  resourceKinds?: ReadonlyMap<string, ResourceKind>;
  highlightedEntryKey?: string;
  focusedCleanupTargetKey?: string;
  onNavigate: (entry: TopologyEntrySummary) => void;
  onRescanRegions?: (entries: readonly TopologyEntrySummary[]) => void;
}) {
  const connection = useRequiredConnection();
  const { formatNumber, t } = useLocale();
  const {
    targets: cleanupTargets,
    addTargets,
    removeTarget,
  } = useCleanupSelection();
  const [mode, setMode] = useState<CanvasMode>("select");
  const [candidateTargets, setCandidateTargets] = useState<CleanupTarget[]>([]);
  const [isPanning, setIsPanning] = useState(false);
  const candidateTargetsRef = useRef<CleanupTarget[]>([]);
  const [contextMenuOpen, setContextMenuOpen] = useState(false);
  const [scopeDetail, setScopeDetail] = useState<ScopeDetailState | null>(null);
  const [networkDetail, setNetworkDetail] =
    useState<NetworkResourceDetail | null>(null);
  const [expandedStackKey, setExpandedStackKey] = useState<string>();
  const flow = useRef<ReactFlowInstance<SummaryCanvasNode, never> | null>(null);
  const selectedFlowNodes = useRef<SummaryCanvasNode[]>([]);
  const nodePanGesture = useRef<CanvasPanGesture | null>(null);
  const suppressNodeClick = useRef(false);
  const canvasRef = useRef<HTMLDivElement>(null);
  const [canvasSize, setCanvasSize] = useState({ width: 1200, height: 800 });
  const [zoom, setZoom] = useState(1);
  const pendingStackFocus = useRef<PendingStackFocus | undefined>(undefined);
  const pendingViewportTransition = useRef<
    PendingViewportTransition | undefined
  >(undefined);
  const collapsedStackViewports = useRef<Map<string, Viewport>>(new Map());
  const stackKey = `empty-${view.kind}`;
  const selectedCandidateKeys = useMemo(
    () => new Set(candidateTargets.map((target) => target.key)),
    [candidateTargets],
  );
  const toggleCandidateTargets = useCallback(
    (incoming: readonly CleanupTarget[], append: boolean) => {
      if (incoming.length === 0) return;
      const current = candidateTargetsRef.current;
      const incomingKeys = new Set(incoming.map((target) => target.key));
      const allSelected = incoming.every((target) =>
        current.some((candidate) => candidate.key === target.key),
      );
      const next = allSelected
        ? current.filter((target) => !incomingKeys.has(target.key))
        : append
          ? [
              ...new Map(
                [...current, ...incoming].map((target) => [target.key, target]),
              ).values(),
            ]
          : [...incoming];
      candidateTargetsRef.current = next;
      setCandidateTargets(next);
    },
    [],
  );
  const contextMenuTargets = useCallback((clicked: CleanupTarget) => {
    const current = candidateTargetsRef.current;
    return current.some((target) => target.key === clicked.key)
      ? current
      : [clicked];
  }, []);
  const contextMenuTargetGroup = useCallback(
    (clicked: readonly CleanupTarget[]) => {
      const current = candidateTargetsRef.current;
      return clicked.every((candidate) =>
        current.some((target) => target.key === candidate.key),
      )
        ? current
        : [...clicked];
    },
    [],
  );
  const entryTargetsByKey = useMemo(() => {
    const targets = new Map<string, CleanupTarget>();
    if (view.kind === "account") {
      for (const entry of view.regions) {
        targets.set(
          entry.key,
          entryCleanupTarget({
            connectionId: connection.id,
            entry,
            kind: "region",
            ancestryKeys: [],
          }),
        );
      }
      return targets;
    }
    targets.set(
      view.public_resources.key,
      entryCleanupTarget({
        connectionId: connection.id,
        entry: view.public_resources,
        kind: "vpc",
        ancestryKeys: [view.region.key],
        locationContext: { region: view.region },
      }),
    );
    for (const entry of view.vpcs) {
      targets.set(
        entry.key,
        entryCleanupTarget({
          connectionId: connection.id,
          entry,
          kind: "vpc",
          ancestryKeys: [view.region.key],
          locationContext: { region: view.region },
        }),
      );
    }
    return targets;
  }, [connection.id, view]);
  const { fixedEntries, zeroEntries, collapsible } = useMemo(() => {
    if (view.kind === "account") {
      const zeroEntries = view.regions.filter(
        (entry) => entry.resource_count === 0,
      );
      return {
        fixedEntries: [
          ...(view.global_resources ? [view.global_resources] : []),
          ...view.regions.filter((entry) => entry.resource_count > 0),
        ],
        zeroEntries,
        collapsible: complete && zeroEntries.length >= 2,
      };
    }
    const zeroEntries = view.vpcs.filter((entry) => entry.resource_count === 0);
    return {
      fixedEntries: [
        view.public_resources,
        ...view.vpcs.filter((entry) => entry.resource_count > 0),
      ],
      zeroEntries,
      collapsible: complete && zeroEntries.length >= 2,
    };
  }, [complete, view]);
  const expandStack = useCallback((key: string) => {
    const viewport = flow.current?.getViewport();
    if (viewport) {
      collapsedStackViewports.current.set(key, { ...viewport });
    }
    pendingViewportTransition.current = { kind: "focus", stackKey: key };
    pendingStackFocus.current = { key, action: "collapse" };
    setExpandedStackKey(key);
  }, []);
  const collapseStack = useCallback((key: string) => {
    const viewport = collapsedStackViewports.current.get(key);
    if (viewport) {
      pendingViewportTransition.current = {
        kind: "restore",
        stackKey: key,
        viewport,
      };
      collapsedStackViewports.current.delete(key);
    } else {
      pendingViewportTransition.current = undefined;
    }
    pendingStackFocus.current = { key, action: "expand" };
    setExpandedStackKey(undefined);
  }, []);
  const removeDirectTargets = useCallback(
    (candidates: readonly CleanupTarget[]) => {
      const targetKeys = new Set(
        candidates.flatMap((candidate) =>
          directCleanupTargetKeys(cleanupTargets, candidate),
        ),
      );
      for (const targetKey of targetKeys) {
        removeTarget(targetKey);
      }
    },
    [cleanupTargets, removeTarget],
  );
  const contextMenuForEntry = useCallback(
    (
      entry: TopologyEntrySummary,
      kind: SummaryEntryKind,
      target: CleanupTarget | null,
    ): SummaryContextMenuData => {
      const pendingCleanup =
        target !== null && isCleanupTargetPending(cleanupTargets, target);
      const directKeys = target
        ? directCleanupTargetKeys(cleanupTargets, target)
        : [];
      return {
        kind,
        pendingCleanup,
        inheritedCleanup: pendingCleanup && directKeys.length === 0,
        dirtyAsset: entry.asset_id
          ? {
              connectionId: connection.id,
              id: entry.asset_id,
              dirty: Boolean(entry.dirty),
            }
          : undefined,
        consoleURL:
          kind === "vpc" &&
          view.kind === "region" &&
          entry.key !== view.public_resources.key
            ? cloudConsoleURL({
                provider: connection.provider,
                nativeType: networkNativeType(connection.provider, "vpc"),
                nativeId: entry.native_id ?? "",
                regionId: view.region.native_id,
                consoleLinkTemplate: resourceKinds.get(
                  `${connection.provider}:${networkNativeType(
                    connection.provider,
                    "vpc",
                  )}`,
                )?.console_link_template,
              })
            : undefined,
        onViewDetails: () => {
          if (
            kind === "vpc" &&
            view.kind === "region" &&
            entry.key !== view.public_resources.key
          ) {
            setNetworkDetail({
              kind: "vpc",
              assetId: entry.asset_id,
              name: entry.name,
              nativeId: entry.native_id || entry.key,
              region: view.region.name.trim() || view.region.native_id || "",
              resourceCount: entry.resource_count,
              icon: VPC_ICON_SRC,
            });
            return;
          }
          setScopeDetail({ kind, entry });
        },
        onRescan:
          kind === "region" && entry.native_id?.trim() && onRescanRegions
            ? () => {
                const regionsByKey = new Map(
                  (view.kind === "account" ? view.regions : []).map(
                    (region) => [region.key, region] as const,
                  ),
                );
                const selectedRegions = target
                  ? contextMenuTargets(target).flatMap((candidate) => {
                      const region = regionsByKey.get(candidate.key);
                      return region ? [region] : [];
                    })
                  : [];
                onRescanRegions(
                  selectedRegions.length > 0 ? selectedRegions : [entry],
                );
              }
            : undefined,
        onAddToCleanup: target
          ? () => addTargets(contextMenuTargets(target))
          : undefined,
        onRemoveFromCleanup: target
          ? () => removeDirectTargets(contextMenuTargets(target))
          : undefined,
        onOpenChange: setContextMenuOpen,
      };
    },
    [
      addTargets,
      cleanupTargets,
      connection.id,
      connection.provider,
      contextMenuTargets,
      onRescanRegions,
      resourceKinds,
      removeDirectTargets,
      view,
    ],
  );
  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const measure = () => {
      const bounds = canvas.getBoundingClientRect();
      if (bounds.width <= 0 || bounds.height <= 0) return;
      setCanvasSize((current) =>
        current.width === bounds.width && current.height === bounds.height
          ? current
          : { width: bounds.width, height: bounds.height },
      );
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(canvas);
    return () => observer.disconnect();
  }, []);
  const nodes = useMemo<SummaryCanvasNode[]>(() => {
    const visibleEntries = collapsible
      ? [...fixedEntries, ...zeroEntries.slice(0, 1)]
      : [...fixedEntries, ...zeroEntries];
    const columns = summaryColumnCount(visibleEntries.length, canvasSize);
    const stackedEntryKind: SummaryEntryKind =
      view.kind === "account" ? "region" : "vpc";
    const regularNodes: SummaryFlowNode[] = fixedEntries.map((entry, index) => {
      const cleanupTarget = entryTargetsByKey.get(entry.key) ?? null;
      const entryKind = summaryEntryKind(view, entry);
      const cleanupKind: SummaryEntryKind =
        view.kind === "account" ? "region" : "vpc";
      return {
        id: entry.key,
        type: "summary",
        position: {
          x: (index % columns) * (SUMMARY_WIDTH + SUMMARY_X_GAP),
          y: Math.floor(index / columns) * (SUMMARY_HEIGHT + SUMMARY_Y_GAP),
        },
        style: { width: SUMMARY_WIDTH, height: SUMMARY_HEIGHT },
        draggable: false,
        selectable: cleanupTarget !== null,
        selected:
          cleanupTarget !== null &&
          selectedCandidateKeys.has(cleanupTarget.key),
        focusable: false,
        data: {
          entry,
          entryKind,
          coverageStatus,
          isAccountGlobal:
            view.kind === "account" && entry.key === view.global_resources?.key,
          isRegionPublic:
            view.kind === "region" && entry.key === view.public_resources.key,
          cleanupTarget,
          selected:
            cleanupTarget !== null &&
            selectedCandidateKeys.has(cleanupTarget.key),
          searchHighlighted: highlightedEntryKey === entry.key,
          pendingCleanup:
            cleanupTarget !== null &&
            isCleanupTargetPending(cleanupTargets, cleanupTarget),
          interactionEnabled: mode === "select",
          contextMenu:
            cleanupTarget || entryKind
              ? contextMenuForEntry(
                  entry,
                  entryKind ?? cleanupKind,
                  cleanupTarget,
                )
              : undefined,
          onSelect: (target, additive) =>
            toggleCandidateTargets([target], additive),
          onNavigate,
        },
      };
    });
    if (!collapsible) {
      return [
        ...regularNodes,
        ...zeroEntries.map((entry, index) => {
          const cleanupTarget = entryTargetsByKey.get(entry.key) ?? null;
          return {
            id: entry.key,
            type: "summary" as const,
            position: {
              x:
                ((fixedEntries.length + index) % columns) *
                (SUMMARY_WIDTH + SUMMARY_X_GAP),
              y:
                Math.floor((fixedEntries.length + index) / columns) *
                (SUMMARY_HEIGHT + SUMMARY_Y_GAP),
            },
            style: { width: SUMMARY_WIDTH, height: SUMMARY_HEIGHT },
            draggable: false,
            selectable: cleanupTarget !== null,
            selected:
              cleanupTarget !== null &&
              selectedCandidateKeys.has(cleanupTarget.key),
            focusable: false,
            data: {
              entry,
              entryKind: stackedEntryKind,
              coverageStatus,
              isAccountGlobal: false,
              isRegionPublic: false,
              cleanupTarget,
              selected:
                cleanupTarget !== null &&
                selectedCandidateKeys.has(cleanupTarget.key),
              searchHighlighted: highlightedEntryKey === entry.key,
              pendingCleanup:
                cleanupTarget !== null &&
                isCleanupTargetPending(cleanupTargets, cleanupTarget),
              interactionEnabled: mode === "select",
              contextMenu: contextMenuForEntry(
                entry,
                stackedEntryKind,
                cleanupTarget,
              ),
              onSelect: (target: CleanupTarget, additive: boolean) =>
                toggleCandidateTargets([target], additive),
              onNavigate,
            },
          };
        }),
      ];
    }
    const position = {
      x: (fixedEntries.length % columns) * (SUMMARY_WIDTH + SUMMARY_X_GAP),
      y:
        Math.floor(fixedEntries.length / columns) *
        (SUMMARY_HEIGHT + SUMMARY_Y_GAP),
    };
    const label = t(
      view.kind === "account" ? "panorama.emptyRegions" : "panorama.emptyVPCs",
      { count: formatNumber(zeroEntries.length) },
    );
    const name = t(
      view.kind === "account"
        ? "panorama.emptyRegionsLabel"
        : "panorama.emptyVPCsLabel",
    );
    const zeroTargets = zeroEntries.flatMap((entry) => {
      const target = entryTargetsByKey.get(entry.key);
      return target ? [target] : [];
    });
    const pendingCleanupCount = zeroTargets.filter((target) =>
      isCleanupTargetPending(cleanupTargets, target),
    ).length;
    const directTargetKeys = new Set(
      zeroTargets.flatMap((target) =>
        directCleanupTargetKeys(cleanupTargets, target),
      ),
    );
    const stackContextMenu: SummaryContextMenuData = {
      kind: "stack",
      pendingCleanup:
        zeroTargets.length > 0 && pendingCleanupCount === zeroTargets.length,
      inheritedCleanup:
        zeroTargets.length > 0 &&
        pendingCleanupCount === zeroTargets.length &&
        directTargetKeys.size === 0,
      onViewDetails: () => expandStack(stackKey),
      onAddToCleanup: () => addTargets(contextMenuTargetGroup(zeroTargets)),
      onRemoveFromCleanup: () =>
        removeDirectTargets(contextMenuTargetGroup(zeroTargets)),
      onOpenChange: setContextMenuOpen,
    };
    if (expandedStackKey === stackKey) {
      const size = expandedZeroStackSize(zeroEntries.length);
      const contextMenus = new Map(
        zeroEntries.map(
          (entry) =>
            [
              entry.key,
              contextMenuForEntry(
                entry,
                stackedEntryKind,
                entryTargetsByKey.get(entry.key) ?? null,
              ),
            ] as const,
        ),
      );
      return [
        ...regularNodes,
        {
          id: stackKey,
          type: "expandedZeroStack",
          position,
          style: size,
          draggable: false,
          selectable: zeroTargets.length > 0,
          selected:
            zeroTargets.length > 0 &&
            zeroTargets.every((target) =>
              selectedCandidateKeys.has(target.key),
            ),
          focusable: false,
          data: {
            stackKey,
            label,
            name,
            entries: zeroEntries,
            entryKind: stackedEntryKind,
            coverageStatus,
            cleanupTargets: zeroTargets,
            selectedTargetKeys: selectedCandidateKeys,
            highlightedEntryKey,
            pendingCleanupCount,
            interactionEnabled: mode === "select",
            contextMenus,
            onSelect: (target, additive) =>
              toggleCandidateTargets([target], additive),
            onNavigate,
            onCollapse: collapseStack,
          },
        },
      ];
    }
    return [
      ...regularNodes,
      {
        id: stackKey,
        type: "zeroStack",
        position,
        style: { width: SUMMARY_WIDTH, height: SUMMARY_HEIGHT },
        draggable: false,
        selectable: zeroTargets.length > 0,
        selected:
          zeroTargets.length > 0 &&
          zeroTargets.every((target) => selectedCandidateKeys.has(target.key)),
        focusable: false,
        data: {
          stackKey,
          label,
          name,
          count: zeroEntries.length,
          entryKind: stackedEntryKind,
          cleanupTargets: zeroTargets,
          selected:
            zeroTargets.length > 0 &&
            zeroTargets.every((target) =>
              selectedCandidateKeys.has(target.key),
            ),
          searchHighlighted: zeroEntries.some(
            (entry) => entry.key === highlightedEntryKey,
          ),
          pendingCleanupCount,
          interactionEnabled: mode === "select",
          contextMenu: stackContextMenu,
          onSelect: toggleCandidateTargets,
          onExpand: expandStack,
        },
      },
    ];
  }, [
    canvasSize,
    addTargets,
    collapsible,
    collapseStack,
    cleanupTargets,
    contextMenuForEntry,
    contextMenuTargetGroup,
    coverageStatus,
    entryTargetsByKey,
    expandStack,
    expandedStackKey,
    fixedEntries,
    formatNumber,
    highlightedEntryKey,
    mode,
    onNavigate,
    removeDirectTargets,
    selectedCandidateKeys,
    stackKey,
    t,
    toggleCandidateTargets,
    view.kind,
    zeroEntries,
  ]);
  const layoutGeometryKey = [
    `${canvasSize.width}:${canvasSize.height}`,
    ...nodes.map(
      (node) =>
        `${node.id}:${node.position.x}:${node.position.y}:${String(
          node.style?.width,
        )}:${String(node.style?.height)}`,
    ),
  ].join("|");

  useEffect(() => {
    const transition = pendingViewportTransition.current;
    const frame = requestAnimationFrame(() => {
      if (transition?.kind === "focus") {
        void flow.current?.fitView({
          nodes: [{ id: transition.stackKey }],
          padding: SUMMARY_STACK_FIT_PADDING,
          maxZoom: SUMMARY_MAX_ZOOM,
        });
      } else if (transition?.kind === "restore") {
        void flow.current?.setViewport(transition.viewport);
      } else {
        void flow.current?.fitView({
          padding: 0.06,
          maxZoom: SUMMARY_MAX_ZOOM,
        });
      }
      if (pendingViewportTransition.current === transition) {
        pendingViewportTransition.current = undefined;
      }
    });
    return () => cancelAnimationFrame(frame);
  }, [layoutGeometryKey]);

  useEffect(() => {
    if (!focusedCleanupTargetKey) return;
    const focusedNode = nodes.find((node) =>
      summaryNodeContainsTarget(node, focusedCleanupTargetKey),
    );
    if (!focusedNode) return;
    const frame = requestAnimationFrame(() => {
      void flow.current?.fitView({
        nodes: [focusedNode],
        padding: 0.45,
        maxZoom: SUMMARY_MAX_ZOOM,
      });
    });
    return () => cancelAnimationFrame(frame);
  }, [focusedCleanupTargetKey, nodes]);

  useEffect(() => {
    const pending = pendingStackFocus.current;
    if (!pending) return;
    const frame = requestAnimationFrame(() => {
      const target = Array.from(
        canvasRef.current?.querySelectorAll<HTMLElement>(
          "[data-empty-stack-key]",
        ) ?? [],
      ).find(
        (candidate) =>
          candidate.dataset.emptyStackKey === pending.key &&
          candidate.dataset.emptyStackAction === pending.action,
      );
      target?.focus();
      if (target && pendingStackFocus.current === pending) {
        pendingStackFocus.current = undefined;
      }
    });
    return () => cancelAnimationFrame(frame);
  }, [nodes]);

  const clearCandidates = useCallback(() => {
    selectedFlowNodes.current = [];
    candidateTargetsRef.current = [];
    setCandidateTargets([]);
  }, []);
  const handleSelectionEnd = useCallback((event: ReactMouseEvent) => {
    const selectedTargets = selectedFlowNodes.current.flatMap((node) => {
      const data = node.data as {
        cleanupTarget?: CleanupTarget | null;
        cleanupTargets?: readonly CleanupTarget[];
      };
      if (data.cleanupTargets) return [...data.cleanupTargets];
      return data.cleanupTarget ? [data.cleanupTarget] : [];
    });
    setCandidateTargets((current) => {
      const targetsByKey = new Map(
        [...current, ...selectedTargets].map((target) => [target.key, target]),
      );
      const candidateKeys = completeBoxSelection(
        current.map((target) => target.key),
        selectedTargets.map((target) => target.key),
        event.shiftKey,
      );
      const next = candidateKeys.flatMap((key) => {
        const target = targetsByKey.get(key);
        return target ? [target] : [];
      });
      candidateTargetsRef.current = next;
      return next;
    });
  }, []);
  const handlePaneClick = useCallback(() => {
    if (mode === "select") clearCandidates();
  }, [clearCandidates, mode]);
  const handleNodePanStart = useCallback(
    (event: ReactPointerEvent<HTMLDivElement>) => {
      if (
        event.button !== 0 ||
        !(event.target instanceof Element) ||
        !event.target.closest(".react-flow__node")
      ) {
        return;
      }
      const viewport = flow.current?.getViewport();
      if (!viewport) return;
      nodePanGesture.current = {
        pointerID: event.pointerId,
        origin: { x: event.clientX, y: event.clientY },
        viewport,
        moved: false,
      };
    },
    [],
  );
  const handleNodePanMove = useCallback(
    (event: ReactPointerEvent<HTMLDivElement>) => {
      const gesture = nodePanGesture.current;
      if (!gesture || gesture.pointerID !== event.pointerId) return;
      const deltaX = event.clientX - gesture.origin.x;
      const deltaY = event.clientY - gesture.origin.y;
      if (!gesture.moved && Math.hypot(deltaX, deltaY) < 4) return;
      if (!gesture.moved) {
        gesture.moved = true;
        setIsPanning(true);
        event.currentTarget.setPointerCapture?.(event.pointerId);
      }
      void flow.current?.setViewport({
        x: gesture.viewport.x + deltaX,
        y: gesture.viewport.y + deltaY,
        zoom: gesture.viewport.zoom,
      });
    },
    [],
  );
  const finishNodePan = useCallback(
    (event: ReactPointerEvent<HTMLDivElement>) => {
      const gesture = nodePanGesture.current;
      if (!gesture || gesture.pointerID !== event.pointerId) return;
      nodePanGesture.current = null;
      setIsPanning(false);
      if (event.currentTarget.hasPointerCapture?.(event.pointerId)) {
        event.currentTarget.releasePointerCapture(event.pointerId);
      }
      if (!gesture.moved) return;
      suppressNodeClick.current = true;
      window.setTimeout(() => {
        suppressNodeClick.current = false;
      }, 0);
    },
    [],
  );
  const handleModeChange = useCallback(
    (nextMode: CanvasMode) => {
      if (!contextMenuOpen && scopeDetail === null && networkDetail === null) {
        setMode(nextMode);
      }
    },
    [contextMenuOpen, networkDetail, scopeDetail],
  );
  const handleEscape = useCallback(() => {
    if (contextMenuOpen || scopeDetail !== null || networkDetail !== null) {
      return;
    }
    if (candidateTargets.length > 0) {
      clearCandidates();
      return;
    }
    if (mode !== "select") setMode("select");
  }, [
    candidateTargets.length,
    clearCandidates,
    contextMenuOpen,
    mode,
    networkDetail,
    scopeDetail,
  ]);
  const zoomOut = useCallback(() => {
    void flow.current?.zoomOut();
  }, []);
  const fitView = useCallback(() => {
    void flow.current?.fitView({
      padding: 0.06,
      maxZoom: SUMMARY_MAX_ZOOM,
    });
  }, []);
  const zoomIn = useCallback(() => {
    void flow.current?.zoomIn();
  }, []);
  const changeZoom = useCallback((nextZoom: number) => {
    const clampedZoom = Math.min(SUMMARY_MAX_ZOOM, Math.max(0.02, nextZoom));
    setZoom(clampedZoom);
    void flow.current?.zoomTo(clampedZoom);
  }, []);

  return (
    <div
      ref={canvasRef}
      data-panorama-canvas
      data-canvas-mode={mode}
      data-panning={isPanning ? "true" : "false"}
      className={cn(
        "relative box-border h-full min-h-0 w-full min-w-0",
        (mode === "pan" || isPanning) &&
          "[&_.react-flow__pane]:cursor-grab [&_.react-flow__pane:active]:cursor-grabbing",
      )}
    >
      <ReactFlow
        data-testid="topology-summary-canvas"
        nodes={nodes}
        edges={[]}
        nodeTypes={summaryNodeTypes}
        nodesDraggable={false}
        nodesConnectable={false}
        nodesFocusable={false}
        elementsSelectable={mode === "select"}
        selectionOnDrag={mode === "select"}
        panOnDrag={mode === "pan" ? true : [1]}
        selectionKeyCode={null}
        multiSelectionKeyCode="Shift"
        proOptions={{ hideAttribution: true }}
        onClickCapture={(event) => {
          if (
            !suppressNodeClick.current ||
            !(event.target instanceof Element) ||
            !event.target.closest(".react-flow__node")
          ) {
            return;
          }
          suppressNodeClick.current = false;
          event.preventDefault();
          event.stopPropagation();
        }}
        onPointerDownCapture={handleNodePanStart}
        onPointerMoveCapture={handleNodePanMove}
        onPointerUpCapture={finishNodePan}
        onPointerCancelCapture={finishNodePan}
        onNodeClick={() => undefined}
        onPaneClick={handlePaneClick}
        onSelectionChange={({ nodes: selectedNodes }) => {
          selectedFlowNodes.current = selectedNodes;
        }}
        onSelectionEnd={handleSelectionEnd}
        onInit={(instance) => {
          flow.current = instance;
          void instance.fitView({
            padding: 0.06,
            maxZoom: SUMMARY_MAX_ZOOM,
          });
        }}
        onMove={(_event, viewport) => setZoom(viewport.zoom)}
        fitView
        fitViewOptions={{ padding: 0.06, maxZoom: SUMMARY_MAX_ZOOM }}
        minZoom={0.02}
        maxZoom={SUMMARY_MAX_ZOOM}
      >
        <Background />
      </ReactFlow>
      <CanvasToolbar
        mode={mode}
        zoom={zoom}
        onZoomChange={changeZoom}
        onModeChange={handleModeChange}
        onEscape={handleEscape}
        onZoomOut={zoomOut}
        onFitView={fitView}
        onZoomIn={zoomIn}
      />
      <BoxSelectionActionBar
        candidateKeys={candidateTargets.map((target) => target.key)}
        onAdd={() => addTargets(candidateTargets)}
        onCancel={clearCandidates}
      />
      <ScopeDetailDialog
        open={scopeDetail !== null}
        onOpenChange={(open) => {
          if (!open) setScopeDetail(null);
        }}
        kind={scopeDetail?.kind ?? "region"}
        entry={scopeDetail?.entry}
      />
      {networkDetail && (
        <NetworkResourceDetailDialog
          open
          onOpenChange={(open) => {
            if (!open) setNetworkDetail(null);
          }}
          connectionId={connection.id}
          detail={networkDetail}
        />
      )}
    </div>
  );
}

function summaryEntryKind(
  view: AccountTopologyView | RegionTopologyView,
  entry: TopologyEntrySummary,
): SummaryEntryKind | undefined {
  if (view.kind === "account") {
    return entry.key === view.global_resources?.key ? undefined : "region";
  }
  return entry.key === view.public_resources.key ? undefined : "vpc";
}
