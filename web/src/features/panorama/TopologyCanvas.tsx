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
  type Edge,
  type NodeMouseHandler,
  type ReactFlowInstance,
  type Viewport,
} from "@xyflow/react";
import type {
  ResourceKind,
  ResourceGraphTopologyView,
  TopologyProjectionWarning,
  TopologyResource,
  TopologyVSwitch,
  VPCTopologyView,
} from "@/api/types";
import { useTheme } from "@/app/ThemeProvider";
import { useRequiredConnection } from "@/connections/ActiveConnectionProvider";
import { useLocale } from "@/i18n/LocaleProvider";
import { cn } from "@/lib/utils";
import { BoxSelectionActionBar } from "./BoxSelectionActionBar";
import {
  completeBoxSelection,
  prioritizeBoxSelectionTargets,
  type CanvasMode,
} from "./boxSelection";
import { CanvasToolbar } from "./CanvasToolbar";
import { isCleanupTargetPending, type CleanupTarget } from "./cleanupSelection";
import { cleanupTargetContainsKey } from "./cleanupLocation";
import { cloudConsoleURL } from "./consoleLinks";
import { useCleanupSelection } from "./CleanupSelectionContext";
import {
  batchCleanupTarget,
  resourceCleanupTarget,
  vSwitchCleanupTarget,
} from "./cleanupTargets";
import { connectedEdgeFocus } from "./focus";
import { buildTopologyLayout, topologyEdgeHandles } from "./layout";
import {
  NetworkResourceDetailDialog,
  type NetworkResourceDetail,
} from "./NetworkResourceDetailDialog";
import { ResourceDetailDialog } from "./ResourceDetailDialog";
import { StackMembersDialog } from "./StackMembersDialog";
import {
  BareResourceNode,
  ExpandedResourceGroupFrameNode,
  ExpandedStackFrameNode,
  ExpandedVSwitchStackFrameNode,
  ResourceGroupNode,
  ResourceStackNode,
  VSwitchStackNode,
  VSwitchFrameNode,
  VSWITCH_ICON_SRC,
  resourceTypeName,
  type ExpandedResourceGroupFlowNode,
  type ExpandedStackFlowNode,
  type ExpandedVSwitchStackFlowNode,
  type ResourceGroupFlowNode,
  type ResourceFlowNode,
  type StackFlowNode,
  type TopologyFlowNode,
  type VSwitchStackFlowNode,
  type VSwitchFlowNode,
} from "./TopologyNodes";

const nodeTypes = {
  resource: BareResourceNode,
  resourceGroup: ResourceGroupNode,
  expandedResourceGroup: ExpandedResourceGroupFrameNode,
  vswitch: VSwitchFrameNode,
  stack: ResourceStackNode,
  expandedStack: ExpandedStackFrameNode,
  vSwitchStack: VSwitchStackNode,
  expandedVSwitchStack: ExpandedVSwitchStackFrameNode,
};

interface PendingStackFocus {
  key: string;
  action: "expand" | "collapse";
}

type PendingViewportTransition =
  | { kind: "focus"; stackKey: string }
  | { kind: "restore"; stackKey: string; viewport: Viewport };

interface StackDetailState {
  displayName: string;
  resources: TopologyResource[];
}

interface CleanupRemovalSet {
  targetKeys: string[];
  batchMembers: Array<{ targetKey: string; assetID: string }>;
  inherited: boolean;
}

interface CanvasPanGesture {
  pointerID: number;
  origin: { x: number; y: number };
  viewport: { x: number; y: number; zoom: number };
  moved: boolean;
}

export type ResourceContextMenuHandler = (
  resource: TopologyResource,
  trigger: HTMLElement | null,
) => void;

export type StackContextMenuHandler = (
  stackKey: string,
  trigger: HTMLElement | null,
) => void;

export interface TopologySearchFocusRequest {
  id: number;
  nodeKey?: string;
  scope: boolean;
}

const ignoreResourceContextMenu: ResourceContextMenuHandler = () => undefined;
const ignoreStackContextMenu: StackContextMenuHandler = () => undefined;
const DEFAULT_CANVAS_SIZE = { width: 1200, height: 800 };
const CANVAS_FIT_PADDING = 0.06;
const SEARCH_FIT_PADDING = 0.35;
const SEARCH_FIT_MIN_ZOOM = 1;
const STACK_FIT_PADDING = 0.04;
const MAX_CANVAS_ZOOM = 1.8;
const EMPTY_RESOURCE_KINDS = new Map<string, ResourceKind>();

function resourceKindNativeType(provider: string, resourceKindID: string) {
  const prefix = `${provider}:`;
  return resourceKindID.startsWith(prefix)
    ? resourceKindID.slice(prefix.length)
    : resourceKindID;
}

function networkNativeType(provider: string, kind: "vpc" | "vswitch") {
  if (provider === "alicloud") {
    return kind === "vpc" ? "ACS::VPC::VPC" : "ACS::VPC::VSwitch";
  }
  if (provider === "aws") {
    return kind === "vpc" ? "AWS::EC2::VPC" : "AWS::EC2::Subnet";
  }
  return "";
}

function topologyNodeContainsTarget(
  node: TopologyFlowNode,
  targetKey: string,
): boolean {
  switch (node.type) {
    case "resource":
      return (
        node.data.resource.key === targetKey ||
        cleanupTargetContainsKey(node.data.cleanupTarget, targetKey)
      );
    case "resourceGroup":
    case "expandedResourceGroup":
      return (
        node.data.resource.key === targetKey ||
        node.data.childKeys.includes(targetKey) ||
        cleanupTargetContainsKey(node.data.cleanupTarget, targetKey)
      );
    case "vswitch":
      return (
        node.data.vSwitch.key === targetKey ||
        cleanupTargetContainsKey(node.data.cleanupTarget, targetKey)
      );
    case "stack":
    case "expandedStack":
      return cleanupTargetContainsKey(node.data.cleanupTarget, targetKey);
    case "vSwitchStack":
    case "expandedVSwitchStack":
      return (
        node.data.vSwitches.some((vSwitch) => vSwitch.key === targetKey) ||
        node.data.cleanupTargets.some((target) =>
          cleanupTargetContainsKey(target, targetKey),
        )
      );
  }
}

function cleanupTargetAssetIDs(target: CleanupTarget): string[] {
  const selectors = Array.isArray(target.selector)
    ? target.selector
    : [target.selector];
  return selectors.flatMap((selector) =>
    selector.kind === "asset" ? [selector.asset_id] : [],
  );
}

function cleanupRemovalSet(
  targets: readonly CleanupTarget[],
  candidate: CleanupTarget,
): CleanupRemovalSet {
  const candidateAssetIDs = new Set(cleanupTargetAssetIDs(candidate));
  const targetKeys = new Set<string>();
  const batchMembers = new Map<
    string,
    { targetKey: string; assetID: string }
  >();
  let inherited = false;

  for (const target of targets) {
    if (target.connectionId !== candidate.connectionId) continue;
    if (target.kind === "region" || target.kind === "vpc") {
      const ancestorIndex = candidate.ancestryKeys.indexOf(target.key);
      if (
        ancestorIndex >= 0 &&
        ancestorIndex < candidate.ancestryKeys.length - 1
      ) {
        inherited = true;
      }
      continue;
    }
    if (
      target.kind === "resource_batch" &&
      candidate.kind === "resource_batch" &&
      target.key === candidate.key
    ) {
      targetKeys.add(target.key);
      continue;
    }
    const matchedAssetIDs = cleanupTargetAssetIDs(target).filter((assetID) =>
      candidateAssetIDs.has(assetID),
    );
    if (target.kind === "resource" && matchedAssetIDs.length > 0) {
      targetKeys.add(target.key);
    } else if (target.kind === "resource_batch") {
      for (const assetID of matchedAssetIDs) {
        batchMembers.set(`${target.key}\u0000${assetID}`, {
          targetKey: target.key,
          assetID,
        });
      }
    }
  }

  return {
    targetKeys: [...targetKeys],
    batchMembers: [...batchMembers.values()],
    inherited,
  };
}

function hasRemovalOperations(task: CleanupRemovalSet): boolean {
  return task.targetKeys.length > 0 || task.batchMembers.length > 0;
}

function mergeCleanupTargetSelection(
  current: readonly CleanupTarget[],
  incoming: readonly CleanupTarget[],
  append: boolean,
): CleanupTarget[] {
  const targetByKey = new Map(
    [...current, ...incoming].map((target) => [target.key, target]),
  );
  const describe = (targets: readonly CleanupTarget[]) =>
    targets.flatMap((target) =>
      target.kind === "resource" || target.kind === "resource_batch"
        ? [
            {
              key: target.key,
              kind: target.kind,
              memberKeys:
                target.kind === "resource_batch"
                  ? target.memberAssetIds?.map((id) => `asset:${id}`)
                  : undefined,
            },
          ]
        : [],
    );
  const incomingTargets = prioritizeBoxSelectionTargets(describe(incoming));
  const candidateKeys = completeBoxSelection(
    current.map((target) => target.key),
    incomingTargets.map((target) => target.key),
    append,
  );
  const combinedTargets = candidateKeys.flatMap((key) => {
    const target = targetByKey.get(key);
    return target ? [target] : [];
  });
  const prioritizedTargets = prioritizeBoxSelectionTargets(
    describe(combinedTargets),
  );
  return prioritizedTargets.flatMap((target) => {
    const resolved = targetByKey.get(target.key);
    return resolved ? [resolved] : [];
  });
}

export function TopologyCanvas({
  view,
  complete,
  warnings = [],
  resourceKinds = EMPTY_RESOURCE_KINDS,
  selectedResourceKey,
  highlightedNodeKey,
  searchFocusRequest,
  focusedCleanupTargetKey,
  highlightedScope = false,
  onSelectResource,
  onResourceContextMenu = ignoreResourceContextMenu,
  onStackContextMenu = ignoreStackContextMenu,
  onClearFocus,
}: {
  view: ResourceGraphTopologyView | VPCTopologyView;
  complete: boolean;
  warnings?: readonly TopologyProjectionWarning[];
  resourceKinds?: ReadonlyMap<string, ResourceKind>;
  selectedResourceKey?: string;
  highlightedNodeKey?: string;
  searchFocusRequest?: TopologySearchFocusRequest;
  focusedCleanupTargetKey?: string;
  highlightedScope?: boolean;
  onSelectResource: (
    resource: TopologyResource,
    trigger: HTMLElement | null,
  ) => void;
  onResourceContextMenu?: ResourceContextMenuHandler;
  onStackContextMenu?: StackContextMenuHandler;
  onClearFocus: () => void;
}) {
  const connection = useRequiredConnection();
  const { resolvedTheme } = useTheme();
  const { formatNumber, label, locale, t } = useLocale();
  const {
    targets: cleanupTargets,
    addTargets,
    removeBatchMember,
    removeTarget,
  } = useCleanupSelection();
  const [mode, setMode] = useState<CanvasMode>("select");
  const [relationshipsVisible, setRelationshipsVisible] = useState(false);
  const [candidateTargets, setCandidateTargets] = useState<CleanupTarget[]>([]);
  const [isPanning, setIsPanning] = useState(false);
  const candidateTargetsRef = useRef<CleanupTarget[]>([]);
  const [contextMenuOpen, setContextMenuOpen] = useState(false);
  const [detailResource, setDetailResource] = useState<TopologyResource | null>(
    null,
  );
  const [stackDetail, setStackDetail] = useState<StackDetailState | null>(null);
  const [networkDetail, setNetworkDetail] =
    useState<NetworkResourceDetail | null>(null);
  const [expandedStackKeys, setExpandedStackKeys] = useState<Set<string>>(
    () => new Set(),
  );
  const [canvasSize, setCanvasSize] = useState(DEFAULT_CANVAS_SIZE);
  const [zoom, setZoom] = useState(1);
  const flow = useRef<ReactFlowInstance<TopologyFlowNode, Edge> | null>(null);
  const selectedFlowNodes = useRef<TopologyFlowNode[]>([]);
  const nodePanGesture = useRef<CanvasPanGesture | null>(null);
  const suppressNodeClick = useRef(false);
  const canvasRef = useRef<HTMLDivElement>(null);
  const pendingStackFocus = useRef<PendingStackFocus | undefined>(undefined);
  const pendingViewportTransition = useRef<
    PendingViewportTransition | undefined
  >(undefined);
  const collapsedStackViewports = useRef<Map<string, Viewport>>(new Map());
  const baseLayout = useMemo(
    () =>
      buildTopologyLayout(view, expandedStackKeys, {
        complete,
        viewport: canvasSize,
      }),
    [canvasSize, complete, expandedStackKeys, view],
  );
  const contextAncestry = useMemo(
    () =>
      view.kind === "vpc"
        ? [view.region.key, view.vpc.key]
        : [
            ...(view.ancestors ?? []).map((ancestor) => ancestor.key),
            view.context.key,
          ],
    [view],
  );
  const locationContext = useMemo(() => {
    if (view.kind === "vpc") {
      return { region: view.region, vpc: view.vpc };
    }
    const region = view.ancestors?.find((ancestor) =>
      ancestor.key.startsWith("region:"),
    );
    return { ...(region ? { region } : {}), scope: view.context };
  }, [view]);
  const regionID = useMemo(() => {
    if (view.kind === "vpc") return view.region.native_id ?? "";
    const region = view.ancestors?.find((ancestor) =>
      ancestor.key.startsWith("region:"),
    );
    if (region?.native_id) return region.native_id;
    return view.context.key.startsWith("region-public:")
      ? (view.context.native_id ?? "")
      : "";
  }, [view]);
  const showVSwitchDetails = useCallback(
    (vSwitch: TopologyVSwitch) => {
      if (view.kind !== "vpc") return;
      setNetworkDetail({
        kind: "vswitch",
        assetId: vSwitch.asset_id,
        name: vSwitch.name,
        nativeId: vSwitch.native_id || vSwitch.key,
        region: view.region.name.trim() || view.region.native_id || "",
        zone: vSwitch.zone,
        resourceCount: vSwitch.resource_count,
        icon: VSWITCH_ICON_SRC,
      });
    },
    [view],
  );
  const resourcesByKey = useMemo(
    () => new Map(view.resources.map((resource) => [resource.key, resource])),
    [view.resources],
  );
  const resourcesByAssetID = useMemo(
    () =>
      new Map(view.resources.map((resource) => [resource.asset_id, resource])),
    [view.resources],
  );
  const resourceTargetsByKey = useMemo(
    () =>
      new Map(
        view.resources.map((resource) => [
          resource.key,
          resourceCleanupTarget({
            connectionId: connection.id,
            resource,
            ancestryKeys: contextAncestry,
            locationContext,
          }),
        ]),
      ),
    [connection.id, contextAncestry, locationContext, view.resources],
  );
  const vSwitchTargetsByKey = useMemo(() => {
    if (view.kind !== "vpc") return new Map<string, CleanupTarget>();
    return new Map(
      view.vswitches.flatMap((vSwitch) => {
        const target = vSwitchCleanupTarget({
          connectionId: connection.id,
          vSwitch,
          ancestryKeys: contextAncestry,
          locationContext,
        });
        return target ? [[vSwitch.key, target] as const] : [];
      }),
    );
  }, [connection.id, contextAncestry, locationContext, view]);
  const selectedExpansionKey = useMemo(() => {
    if (!selectedResourceKey) return undefined;
    return baseLayout.nodes.find(
      (node) =>
        (node.kind === "stack" &&
          node.memberKeys.includes(selectedResourceKey)) ||
        (node.kind === "resourceGroup" &&
          node.childKeys.includes(selectedResourceKey)),
    )?.key;
  }, [baseLayout.nodes, selectedResourceKey]);
  const highlightedExpansionKey = useMemo(() => {
    if (!highlightedNodeKey) return undefined;
    return baseLayout.nodes.find(
      (node) =>
        (node.kind === "stack" &&
          node.memberKeys.includes(highlightedNodeKey)) ||
        (node.kind === "resourceGroup" &&
          node.childKeys.includes(highlightedNodeKey)),
    )?.key;
  }, [baseLayout.nodes, highlightedNodeKey]);
  const layout = useMemo(() => {
    if (!selectedExpansionKey && !highlightedExpansionKey) return baseLayout;
    const effectiveExpandedStackKeys = new Set(expandedStackKeys);
    if (selectedExpansionKey) {
      effectiveExpandedStackKeys.add(selectedExpansionKey);
    }
    if (highlightedExpansionKey) {
      effectiveExpandedStackKeys.add(highlightedExpansionKey);
    }
    return buildTopologyLayout(view, effectiveExpandedStackKeys, {
      complete,
      viewport: canvasSize,
    });
  }, [
    baseLayout,
    canvasSize,
    complete,
    expandedStackKeys,
    highlightedExpansionKey,
    selectedExpansionKey,
    view,
  ]);
  useEffect(() => {
    if (!selectedExpansionKey) return;
    setExpandedStackKeys((current) => {
      if (current.has(selectedExpansionKey)) return current;
      const next = new Set(current);
      next.add(selectedExpansionKey);
      return next;
    });
  }, [selectedExpansionKey]);
  const focus = useMemo(
    () =>
      connectedEdgeFocus(
        view.resources.map((resource) => resource.key),
        layout.edges,
        selectedResourceKey,
      ),
    [layout.edges, selectedResourceKey, view.resources],
  );
  const pendingCleanupResourceKeys = useMemo(() => {
    const pending = new Set<string>();
    for (const resource of view.resources) {
      const candidate = resourceTargetsByKey.get(resource.key);
      if (candidate && isCleanupTargetPending(cleanupTargets, candidate)) {
        pending.add(resource.key);
      }
    }
    return pending;
  }, [cleanupTargets, resourceTargetsByKey, view.resources]);
  const selectedCandidateKeys = useMemo(
    () => new Set(candidateTargets.map((target) => target.key)),
    [candidateTargets],
  );
  const selectCandidateTargets = useCallback(
    (incoming: readonly CleanupTarget[], append: boolean) => {
      setCandidateTargets((current) => {
        const next = mergeCleanupTargetSelection(current, incoming, append);
        candidateTargetsRef.current = next;
        return next;
      });
    },
    [],
  );
  const toggleCandidateTarget = useCallback(
    (target: CleanupTarget, append: boolean): boolean => {
      const current = candidateTargetsRef.current;
      const selected = current.some(
        (candidate) => candidate.key === target.key,
      );
      const next = selected
        ? current.filter((candidate) => candidate.key !== target.key)
        : mergeCleanupTargetSelection(current, [target], append);
      candidateTargetsRef.current = next;
      setCandidateTargets(next);
      return !selected;
    },
    [],
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
        : mergeCleanupTargetSelection(current, incoming, append);
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
  const dirtyAssetTargets = useCallback(
    (targets: readonly CleanupTarget[]) => {
      const result = new Map<
        string,
        { connectionId: string; id: string; dirty: boolean }
      >();
      for (const target of targets) {
        for (const assetID of cleanupTargetAssetIDs(target)) {
          const resource = resourcesByAssetID.get(assetID);
          if (!resource) continue;
          result.set(assetID, {
            connectionId: connection.id,
            id: assetID,
            dirty: Boolean(resource.dirty),
          });
        }
      }
      return [...result.values()];
    },
    [connection.id, resourcesByAssetID],
  );

  const expandStack = useCallback((stackKey: string) => {
    const viewport = flow.current?.getViewport();
    if (viewport) {
      collapsedStackViewports.current.set(stackKey, { ...viewport });
    }
    pendingViewportTransition.current = { kind: "focus", stackKey };
    pendingStackFocus.current = { key: stackKey, action: "collapse" };
    setExpandedStackKeys((current) => {
      const next = new Set(current);
      next.add(stackKey);
      return next;
    });
  }, []);
  const collapseStack = useCallback(
    (stackKey: string, memberKeys: readonly string[]) => {
      if (selectedResourceKey && memberKeys.includes(selectedResourceKey)) {
        onClearFocus();
      }
      const viewport = collapsedStackViewports.current.get(stackKey);
      if (viewport) {
        pendingViewportTransition.current = {
          kind: "restore",
          stackKey,
          viewport,
        };
        collapsedStackViewports.current.delete(stackKey);
      } else {
        pendingViewportTransition.current = undefined;
      }
      pendingStackFocus.current = { key: stackKey, action: "expand" };
      setExpandedStackKeys((current) => {
        const next = new Set(current);
        next.delete(stackKey);
        return next;
      });
    },
    [onClearFocus, selectedResourceKey],
  );
  const handleNodeClick = useCallback<NodeMouseHandler<TopologyFlowNode>>(
    (_event, node) => {
      if (mode !== "select") return;
      if (
        node.type === "expandedResourceGroup" ||
        node.type === "expandedStack" ||
        node.type === "expandedVSwitchStack"
      ) {
        onClearFocus();
      }
    },
    [mode, onClearFocus],
  );
  const executeRemoval = useCallback(
    (task: CleanupRemovalSet) => {
      for (const targetKey of task.targetKeys) {
        removeTarget(targetKey);
      }
      for (const member of task.batchMembers) {
        removeBatchMember(member.targetKey, member.assetID);
      }
    },
    [removeBatchMember, removeTarget],
  );
  const removeCleanupTargets = useCallback(
    (targets: readonly CleanupTarget[]) => {
      for (const target of targets) {
        executeRemoval(cleanupRemovalSet(cleanupTargets, target));
      }
    },
    [cleanupTargets, executeRemoval],
  );

  const resourceState = useCallback(
    (
      resource: TopologyResource,
    ): {
      selectable: boolean;
      selected: boolean;
      data: ResourceFlowNode["data"];
    } => {
      const cleanupTarget = resourceTargetsByKey.get(resource.key) ?? null;
      const selected =
        cleanupTarget !== null && selectedCandidateKeys.has(cleanupTarget.key);
      const pendingCleanup =
        cleanupTarget !== null &&
        isCleanupTargetPending(cleanupTargets, cleanupTarget);
      const removalSet = cleanupTarget
        ? cleanupRemovalSet(cleanupTargets, cleanupTarget)
        : { targetKeys: [], batchMembers: [], inherited: false };
      return {
        selectable: cleanupTarget !== null,
        selected,
        data: {
          resource,
          current: focus.selectedKey === resource.key,
          searchHighlighted:
            highlightedNodeKey === resource.key ||
            cleanupTargetContainsKey(cleanupTarget, highlightedNodeKey),
          selected,
          related: focus.relatedKeys.has(resource.key),
          pendingCleanup,
          cleanupTarget,
          interactionEnabled: mode === "select",
          onSelect: (additive) => {
            if (!cleanupTarget) return false;
            const nowSelected = toggleCandidateTarget(cleanupTarget, additive);
            if (!nowSelected && focus.selectedKey === resource.key) {
              onClearFocus();
            }
            return nowSelected;
          },
          onActivate: onSelectResource,
          onContextMenu: (value, trigger) => {
            if (
              cleanupTarget &&
              !candidateTargetsRef.current.some(
                (target) => target.key === cleanupTarget.key,
              )
            ) {
              selectCandidateTargets([cleanupTarget], false);
            }
            onResourceContextMenu(value, trigger);
          },
          contextMenu: {
            onViewDetails: () => setDetailResource(resource),
            dirtyAsset: {
              connectionId: connection.id,
              id: resource.asset_id,
              dirty: Boolean(resource.dirty),
            },
            dirtyAssets: cleanupTarget
              ? dirtyAssetTargets(contextMenuTargets(cleanupTarget))
              : undefined,
            consoleURL: cloudConsoleURL({
              provider: connection.provider,
              nativeType: resourceKindNativeType(
                connection.provider,
                resource.resource_kind_id,
              ),
              nativeId: resource.native_id,
              regionId: regionID,
              consoleLinkTemplate: resourceKinds.get(resource.resource_kind_id)
                ?.console_link_template,
              templateValues: resource.console_link_values,
            }),
            onAddToCleanup: cleanupTarget
              ? () => addTargets(contextMenuTargets(cleanupTarget))
              : undefined,
            onRemoveFromCleanup: cleanupTarget
              ? () => removeCleanupTargets(contextMenuTargets(cleanupTarget))
              : undefined,
            inheritedCleanup:
              pendingCleanup &&
              removalSet.inherited &&
              !hasRemovalOperations(removalSet),
            onOpenChange: setContextMenuOpen,
          },
        },
      };
    },
    [
      addTargets,
      cleanupTargets,
      connection.id,
      connection.provider,
      contextMenuTargets,
      dirtyAssetTargets,
      focus.relatedKeys,
      focus.selectedKey,
      highlightedNodeKey,
      mode,
      onClearFocus,
      onResourceContextMenu,
      onSelectResource,
      regionID,
      removeCleanupTargets,
      resourceKinds,
      resourceTargetsByKey,
      selectCandidateTargets,
      selectedCandidateKeys,
      toggleCandidateTarget,
    ],
  );
  const nodes = useMemo<TopologyFlowNode[]>(
    () =>
      layout.nodes.map((node): TopologyFlowNode => {
        const common = {
          id: node.key,
          position: node.position,
          width: node.size.width,
          height: node.size.height,
          style: {
            width: node.size.width,
            height: node.size.height,
          },
          className: "!border-0 !bg-transparent !p-0 !shadow-none",
          draggable: false,
          selectable: false,
          focusable: false,
          parentId: node.parentKey,
          extent: node.parentKey ? ("parent" as const) : undefined,
        };
        switch (node.kind) {
          case "resource": {
            const state = resourceState(node.resource);
            return {
              ...common,
              selectable: state.selectable,
              selected: state.selected,
              type: "resource",
              data: state.data,
            } satisfies ResourceFlowNode;
          }
          case "resourceGroup": {
            const state = resourceState(node.resource);
            return {
              ...common,
              selectable: state.selectable,
              selected: state.selected,
              type: "resourceGroup",
              data: {
                ...state.data,
                searchHighlighted:
                  state.data.searchHighlighted ||
                  node.childKeys.includes(highlightedNodeKey ?? ""),
                childKeys: node.childKeys,
                directChildCount: node.directChildCount,
                childTypeSummaries: node.childTypeSummaries,
                childFindingCount: node.childFindingCount,
                onExpand: expandStack,
              },
            } satisfies ResourceGroupFlowNode;
          }
          case "expandedResourceGroup": {
            const state = resourceState(node.resource);
            return {
              ...common,
              selectable: state.selectable,
              selected: state.selected,
              type: "expandedResourceGroup",
              data: {
                ...state.data,
                searchHighlighted:
                  state.data.searchHighlighted ||
                  node.childKeys.includes(highlightedNodeKey ?? ""),
                childKeys: node.childKeys,
                directChildCount: node.directChildCount,
                childTypeSummaries: node.childTypeSummaries,
                childFindingCount: node.childFindingCount,
                onCollapse: (resourceKey, childKeys) =>
                  collapseStack(resourceKey, childKeys),
              },
            } satisfies ExpandedResourceGroupFlowNode;
          }
          case "vswitch": {
            const cleanupTarget =
              vSwitchTargetsByKey.get(node.vSwitch.key) ?? null;
            const selected =
              cleanupTarget !== null &&
              selectedCandidateKeys.has(cleanupTarget.key);
            const pendingCleanup =
              cleanupTarget !== null &&
              isCleanupTargetPending(cleanupTargets, cleanupTarget);
            const removalSet = cleanupTarget
              ? cleanupRemovalSet(cleanupTargets, cleanupTarget)
              : { targetKeys: [], batchMembers: [], inherited: false };
            return {
              ...common,
              selectable: cleanupTarget !== null,
              selected,
              type: "vswitch",
              data: {
                vSwitch: node.vSwitch,
                selected,
                searchHighlighted:
                  highlightedNodeKey === node.vSwitch.key ||
                  cleanupTargetContainsKey(cleanupTarget, highlightedNodeKey),
                pendingCleanup,
                cleanupTarget,
                interactionEnabled: mode === "select" && cleanupTarget !== null,
                onSelect: (additive) => {
                  if (cleanupTarget) {
                    toggleCandidateTarget(cleanupTarget, additive);
                  }
                  onClearFocus();
                },
                onContextMenu: () => {
                  if (
                    cleanupTarget &&
                    !candidateTargetsRef.current.some(
                      (target) => target.key === cleanupTarget.key,
                    )
                  ) {
                    selectCandidateTargets([cleanupTarget], false);
                  }
                },
                contextMenu: {
                  onViewDetails: () => showVSwitchDetails(node.vSwitch),
                  dirtyAsset: node.vSwitch.asset_id
                    ? {
                        connectionId: connection.id,
                        id: node.vSwitch.asset_id,
                        dirty: Boolean(node.vSwitch.dirty),
                      }
                    : undefined,
                  consoleURL: cloudConsoleURL({
                    provider: connection.provider,
                    nativeType: networkNativeType(
                      connection.provider,
                      "vswitch",
                    ),
                    nativeId: node.vSwitch.native_id,
                    regionId: regionID,
                    consoleLinkTemplate: resourceKinds.get(
                      `${connection.provider}:${networkNativeType(
                        connection.provider,
                        "vswitch",
                      )}`,
                    )?.console_link_template,
                  }),
                  onAddToCleanup: cleanupTarget
                    ? () => addTargets(contextMenuTargets(cleanupTarget))
                    : undefined,
                  onRemoveFromCleanup: cleanupTarget
                    ? () =>
                        removeCleanupTargets(contextMenuTargets(cleanupTarget))
                    : undefined,
                  inheritedCleanup:
                    pendingCleanup &&
                    removalSet.inherited &&
                    !hasRemovalOperations(removalSet),
                  onOpenChange: setContextMenuOpen,
                },
              },
            } satisfies VSwitchFlowNode;
          }
          case "stack": {
            const resources = node.memberKeys.flatMap((key) => {
              const resource = resourcesByKey.get(key);
              return resource ? [resource] : [];
            });
            const displayName = resourceTypeName(
              node.resourceKindID,
              node.typeName,
              node.typeNames,
              locale,
            );
            const cleanupTarget = batchCleanupTarget({
              connectionId: connection.id,
              key: node.key,
              displayName,
              resources,
              ancestryKeys: contextAncestry,
              locationContext,
            });
            const removalSet = cleanupRemovalSet(cleanupTargets, cleanupTarget);
            const pendingCleanupCount = node.memberKeys.filter((key) =>
              pendingCleanupResourceKeys.has(key),
            ).length;
            const pendingCleanup =
              node.count > 0 && pendingCleanupCount === node.count;
            const selected = selectedCandidateKeys.has(cleanupTarget.key);
            return {
              ...common,
              selectable: true,
              selected,
              type: "stack",
              data: {
                stackKey: node.key,
                resourceKindID: node.resourceKindID,
                typeName: node.typeName,
                typeNames: node.typeNames,
                icon: node.icon,
                count: node.count,
                pendingCleanup,
                pendingCleanupCount,
                selected,
                searchHighlighted:
                  node.memberKeys.includes(highlightedNodeKey ?? "") ||
                  cleanupTargetContainsKey(cleanupTarget, highlightedNodeKey),
                cleanupTarget,
                interactionEnabled: mode === "select",
                onSelect: (_stackKey, _trigger, additive) => {
                  toggleCandidateTarget(cleanupTarget, additive);
                  onClearFocus();
                },
                onExpand: expandStack,
                onContextMenu: (stackKey, trigger) => {
                  if (
                    !candidateTargetsRef.current.some(
                      (target) => target.key === cleanupTarget.key,
                    )
                  ) {
                    selectCandidateTargets([cleanupTarget], false);
                  }
                  onStackContextMenu(stackKey, trigger);
                },
                contextMenu: {
                  onViewDetails: () =>
                    setStackDetail({ displayName, resources }),
                  onAddToCleanup: () =>
                    addTargets(contextMenuTargets(cleanupTarget)),
                  onRemoveFromCleanup: () =>
                    removeCleanupTargets(contextMenuTargets(cleanupTarget)),
                  inheritedCleanup:
                    pendingCleanup &&
                    removalSet.inherited &&
                    !hasRemovalOperations(removalSet),
                  onOpenChange: setContextMenuOpen,
                },
              },
            } satisfies StackFlowNode;
          }
          case "expandedStack": {
            const resources = node.memberKeys.flatMap((key) => {
              const resource = resourcesByKey.get(key);
              return resource ? [resource] : [];
            });
            const displayName = resourceTypeName(
              node.resourceKindID,
              node.typeName,
              node.typeNames,
              locale,
            );
            const cleanupTarget = batchCleanupTarget({
              connectionId: connection.id,
              key: node.key,
              displayName,
              resources,
              ancestryKeys: contextAncestry,
              locationContext,
            });
            const removalSet = cleanupRemovalSet(cleanupTargets, cleanupTarget);
            const pendingCleanup =
              node.count > 0 &&
              node.memberKeys.every((key) =>
                pendingCleanupResourceKeys.has(key),
              );
            return {
              ...common,
              selectable: true,
              selected: selectedCandidateKeys.has(cleanupTarget.key),
              type: "expandedStack",
              data: {
                stackKey: node.key,
                resourceKindID: node.resourceKindID,
                typeName: node.typeName,
                typeNames: node.typeNames,
                icon: node.icon,
                count: node.count,
                pendingCleanup,
                searchHighlighted:
                  node.memberKeys.includes(highlightedNodeKey ?? "") ||
                  cleanupTargetContainsKey(cleanupTarget, highlightedNodeKey),
                cleanupTarget,
                interactionEnabled: mode === "select",
                onCollapse: (stackKey) =>
                  collapseStack(stackKey, node.memberKeys),
                onContextMenu: onStackContextMenu,
                contextMenu: {
                  onViewDetails: () =>
                    setStackDetail({ displayName, resources }),
                  onAddToCleanup: () =>
                    addTargets(contextMenuTargets(cleanupTarget)),
                  onRemoveFromCleanup: () =>
                    removeCleanupTargets(contextMenuTargets(cleanupTarget)),
                  inheritedCleanup:
                    pendingCleanup &&
                    removalSet.inherited &&
                    !hasRemovalOperations(removalSet),
                  onOpenChange: setContextMenuOpen,
                },
              },
            } satisfies ExpandedStackFlowNode;
          }
          case "vSwitchStack": {
            const targets = node.vSwitches.flatMap((vSwitch) => {
              const target = vSwitchTargetsByKey.get(vSwitch.key);
              return target ? [target] : [];
            });
            const removalSets = targets.map((target) =>
              cleanupRemovalSet(cleanupTargets, target),
            );
            const pendingCleanupCount = targets.filter((target) =>
              isCleanupTargetPending(cleanupTargets, target),
            ).length;
            const selected =
              targets.length > 0 &&
              targets.every((target) => selectedCandidateKeys.has(target.key));
            const pendingCleanup =
              targets.length > 0 && pendingCleanupCount === targets.length;
            return {
              ...common,
              selectable: targets.length > 0,
              selected,
              type: "vSwitchStack",
              data: {
                stackKey: node.key,
                count: node.count,
                vSwitches: node.vSwitches,
                selected,
                searchHighlighted:
                  node.vSwitches.some(
                    (vSwitch) => vSwitch.key === highlightedNodeKey,
                  ) ||
                  targets.some((target) =>
                    cleanupTargetContainsKey(target, highlightedNodeKey),
                  ),
                pendingCleanup,
                pendingCleanupCount,
                cleanupTargets: targets,
                interactionEnabled: mode === "select" && targets.length > 0,
                onSelect: toggleCandidateTargets,
                onExpand: expandStack,
                onContextMenu: () => {
                  if (
                    !targets.every((candidate) =>
                      candidateTargetsRef.current.some(
                        (target) => target.key === candidate.key,
                      ),
                    )
                  ) {
                    selectCandidateTargets(targets, false);
                  }
                },
                contextMenu:
                  targets.length > 0
                    ? {
                        onViewDetails: () => expandStack(node.key),
                        onAddToCleanup: () =>
                          addTargets(contextMenuTargetGroup(targets)),
                        onRemoveFromCleanup: () =>
                          removeCleanupTargets(contextMenuTargetGroup(targets)),
                        inheritedCleanup:
                          pendingCleanup &&
                          removalSets.every(
                            (set) =>
                              set.inherited && !hasRemovalOperations(set),
                          ),
                        onOpenChange: setContextMenuOpen,
                      }
                    : undefined,
              },
            } satisfies VSwitchStackFlowNode;
          }
          case "expandedVSwitchStack": {
            const cleanupTargetsByVSwitchKey = new Map(
              node.vSwitches.flatMap((vSwitch) => {
                const target = vSwitchTargetsByKey.get(vSwitch.key);
                return target ? ([[vSwitch.key, target]] as const) : [];
              }),
            );
            const targets = [...cleanupTargetsByVSwitchKey.values()];
            const pendingTargetKeys = new Set(
              targets
                .filter((target) =>
                  isCleanupTargetPending(cleanupTargets, target),
                )
                .map((target) => target.key),
            );
            const highlightedVSwitchKey = node.vSwitches.find((vSwitch) => {
              const target = cleanupTargetsByVSwitchKey.get(vSwitch.key);
              return (
                vSwitch.key === highlightedNodeKey ||
                cleanupTargetContainsKey(target, highlightedNodeKey)
              );
            })?.key;
            return {
              ...common,
              selectable: targets.length > 0,
              selected:
                targets.length > 0 &&
                targets.every((target) =>
                  selectedCandidateKeys.has(target.key),
                ),
              type: "expandedVSwitchStack",
              data: {
                stackKey: node.key,
                count: node.count,
                vSwitches: node.vSwitches,
                cleanupTargets: targets,
                cleanupTargetsByVSwitchKey,
                selectedTargetKeys: selectedCandidateKeys,
                pendingTargetKeys,
                highlightedVSwitchKey,
                interactionEnabled: mode === "select" && targets.length > 0,
                contextMenus: new Map(
                  node.vSwitches.map((vSwitch) => {
                    const target = cleanupTargetsByVSwitchKey.get(vSwitch.key);
                    const removalSet = target
                      ? cleanupRemovalSet(cleanupTargets, target)
                      : {
                          targetKeys: [],
                          batchMembers: [],
                          inherited: false,
                        };
                    const pendingCleanup = target
                      ? pendingTargetKeys.has(target.key)
                      : false;
                    return [
                      vSwitch.key,
                      {
                        onViewDetails: () => showVSwitchDetails(vSwitch),
                        dirtyAsset: vSwitch.asset_id
                          ? {
                              connectionId: connection.id,
                              id: vSwitch.asset_id,
                              dirty: Boolean(vSwitch.dirty),
                            }
                          : undefined,
                        consoleURL: cloudConsoleURL({
                          provider: connection.provider,
                          nativeType: networkNativeType(
                            connection.provider,
                            "vswitch",
                          ),
                          nativeId: vSwitch.native_id,
                          regionId: regionID,
                          consoleLinkTemplate: resourceKinds.get(
                            `${connection.provider}:${networkNativeType(
                              connection.provider,
                              "vswitch",
                            )}`,
                          )?.console_link_template,
                        }),
                        onAddToCleanup: target
                          ? () => addTargets(contextMenuTargets(target))
                          : undefined,
                        onRemoveFromCleanup: target
                          ? () =>
                              removeCleanupTargets(contextMenuTargets(target))
                          : undefined,
                        inheritedCleanup:
                          pendingCleanup &&
                          removalSet.inherited &&
                          !hasRemovalOperations(removalSet),
                        onOpenChange: setContextMenuOpen,
                      },
                    ] as const;
                  }),
                ),
                onSelect: toggleCandidateTarget,
                onContextMenu: (target) => {
                  if (
                    !candidateTargetsRef.current.some(
                      (candidate) => candidate.key === target.key,
                    )
                  ) {
                    selectCandidateTargets([target], false);
                  }
                },
                onCollapse: (stackKey) => collapseStack(stackKey, []),
              },
            } satisfies ExpandedVSwitchStackFlowNode;
          }
        }
      }),
    [
      addTargets,
      collapseStack,
      connection.id,
      contextAncestry,
      contextMenuTargetGroup,
      contextMenuTargets,
      connection.provider,
      locationContext,
      cleanupTargets,
      executeRemoval,
      expandStack,
      dirtyAssetTargets,
      focus.relatedKeys,
      focus.selectedKey,
      highlightedNodeKey,
      layout.nodes,
      locale,
      mode,
      onResourceContextMenu,
      onClearFocus,
      onSelectResource,
      onStackContextMenu,
      pendingCleanupResourceKeys,
      removeCleanupTargets,
      regionID,
      resourceKinds,
      resourceState,
      resourceTargetsByKey,
      resourcesByKey,
      resourcesByAssetID,
      selectCandidateTargets,
      selectedCandidateKeys,
      showVSwitchDetails,
      toggleCandidateTarget,
      toggleCandidateTargets,
      vSwitchTargetsByKey,
    ],
  );
  const visibleLayoutEdges = useMemo(
    () =>
      layout.edges.filter(
        (edge) =>
          relationshipsVisible || focus.highlightedEdgeKeys.has(edge.key),
      ),
    [focus.highlightedEdgeKeys, layout.edges, relationshipsVisible],
  );
  const edgeHandles = useMemo(
    () => topologyEdgeHandles(layout.nodes, visibleLayoutEdges),
    [layout.nodes, visibleLayoutEdges],
  );
  const edges = useMemo<Edge[]>(
    () =>
      visibleLayoutEdges.map((edge) => {
        const highlighted = focus.highlightedEdgeKeys.has(edge.key);
        const lifecycle = edge.kind === "lifecycle";
        const handles = edgeHandles.get(edge.key);
        const aggregateCount =
          typeof edge.metadata?.aggregate_count === "number"
            ? edge.metadata.aggregate_count
            : 1;
        const relationLabel =
          aggregateCount > 1
            ? `${label(edge.relation)} × ${formatNumber(aggregateCount)}`
            : label(edge.relation);
        const color = highlighted
          ? "var(--warning)"
          : "var(--muted-foreground)";
        const opacity = highlighted ? 1 : 0.5;
        return {
          id: edge.key,
          source: edge.source_key,
          target: edge.target_key,
          sourceHandle: handles?.sourceHandle,
          targetHandle: handles?.targetHandle,
          type: "straight",
          ariaLabel: relationLabel,
          markerEnd: {
            type: "arrowclosed",
            width: 12,
            height: 12,
            color,
          },
          label: highlighted ? relationLabel : undefined,
          labelStyle: highlighted
            ? {
                fill: color,
                fontSize: 8,
                fontWeight: 600,
                opacity: 1,
              }
            : undefined,
          className: lifecycle
            ? "topology-edge-lifecycle"
            : "topology-edge-relationship",
          style: {
            stroke: color,
            strokeDasharray: lifecycle ? undefined : "5 5",
            strokeWidth: highlighted ? 2 : lifecycle ? 1.25 : 1,
            opacity,
          },
        };
      }),
    [
      edgeHandles,
      focus.highlightedEdgeKeys,
      formatNumber,
      label,
      visibleLayoutEdges,
    ],
  );
  const clearCandidates = useCallback(() => {
    selectedFlowNodes.current = [];
    candidateTargetsRef.current = [];
    setCandidateTargets([]);
  }, []);
  const handleSelectionEnd = useCallback(
    (event: ReactMouseEvent) => {
      if (event.button !== undefined && event.button !== 0) return;
      const selectedTargets = selectedFlowNodes.current.flatMap((node) => {
        const data = node.data as {
          cleanupTarget?: CleanupTarget | null;
          cleanupTargets?: readonly CleanupTarget[];
        };
        if (data.cleanupTargets) return [...data.cleanupTargets];
        return data.cleanupTarget ? [data.cleanupTarget] : [];
      });
      selectCandidateTargets(selectedTargets, event.shiftKey);
    },
    [selectCandidateTargets],
  );
  const handlePaneClick = useCallback(() => {
    if (mode !== "select") return;
    clearCandidates();
    onClearFocus();
  }, [clearCandidates, mode, onClearFocus]);
  const handleToolbarModeChange = useCallback(
    (nextMode: CanvasMode) => {
      if (
        !contextMenuOpen &&
        detailResource === null &&
        stackDetail === null &&
        networkDetail === null
      ) {
        setMode(nextMode);
      }
    },
    [contextMenuOpen, detailResource, networkDetail, stackDetail],
  );
  const handleToolbarEscape = useCallback(() => {
    if (
      contextMenuOpen ||
      detailResource !== null ||
      stackDetail !== null ||
      networkDetail !== null
    ) {
      return;
    }
    if (candidateTargets.length > 0) {
      clearCandidates();
      onClearFocus();
      return;
    }
    if (mode !== "select") {
      setMode("select");
      return;
    }
    onClearFocus();
  }, [
    candidateTargets.length,
    clearCandidates,
    contextMenuOpen,
    detailResource,
    mode,
    networkDetail,
    onClearFocus,
    stackDetail,
  ]);
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
  const zoomOut = useCallback(() => {
    void flow.current?.zoomOut();
  }, []);
  const fitView = useCallback(() => {
    void flow.current?.fitView({
      padding: CANVAS_FIT_PADDING,
      maxZoom: MAX_CANVAS_ZOOM,
    });
  }, []);
  const zoomIn = useCallback(() => {
    void flow.current?.zoomIn();
  }, []);
  const changeZoom = useCallback((nextZoom: number) => {
    const clampedZoom = Math.min(MAX_CANVAS_ZOOM, Math.max(0.0001, nextZoom));
    setZoom(clampedZoom);
    void flow.current?.zoomTo(clampedZoom);
  }, []);
  const viewportFocusRequest = useMemo<
    Pick<TopologySearchFocusRequest, "nodeKey" | "scope"> | undefined
  >(
    () =>
      searchFocusRequest ??
      (focusedCleanupTargetKey
        ? {
            nodeKey: focusedCleanupTargetKey,
            scope: false,
          }
        : undefined),
    [focusedCleanupTargetKey, searchFocusRequest],
  );
  const detailCleanupTarget = detailResource
    ? (resourceTargetsByKey.get(detailResource.key) ??
      resourceCleanupTarget({
        connectionId: connection.id,
        resource: detailResource,
        ancestryKeys: contextAncestry,
        locationContext,
      }))
    : null;
  const detailRemovalSet = detailCleanupTarget
    ? cleanupRemovalSet(cleanupTargets, detailCleanupTarget)
    : null;
  const detailPendingCleanup =
    detailCleanupTarget !== null &&
    isCleanupTargetPending(cleanupTargets, detailCleanupTarget);
  const detailInheritedCleanup =
    detailPendingCleanup &&
    detailRemovalSet !== null &&
    detailRemovalSet.inherited &&
    !hasRemovalOperations(detailRemovalSet);

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

  useEffect(() => {
    const transition = pendingViewportTransition.current;
    const frame = requestAnimationFrame(() => {
      if (transition?.kind === "focus") {
        void flow.current?.fitView({
          nodes: [{ id: transition.stackKey }],
          padding: STACK_FIT_PADDING,
          maxZoom: MAX_CANVAS_ZOOM,
        });
      } else if (transition?.kind === "restore") {
        void flow.current?.setViewport(transition.viewport);
      } else {
        void flow.current?.fitView({
          padding: CANVAS_FIT_PADDING,
          maxZoom: MAX_CANVAS_ZOOM,
        });
      }
      if (pendingViewportTransition.current === transition) {
        pendingViewportTransition.current = undefined;
      }
    });
    return () => cancelAnimationFrame(frame);
  }, [layout]);

  useEffect(() => {
    if (!viewportFocusRequest) return;
    const requestedNodeKey = viewportFocusRequest.nodeKey;
    const focusedNode = requestedNodeKey
      ? (nodes.find((node) => node.id === requestedNodeKey) ??
        nodes.find((node) =>
          topologyNodeContainsTarget(node, requestedNodeKey),
        ))
      : undefined;
    if (!focusedNode && !viewportFocusRequest.scope) return;
    const frame = requestAnimationFrame(() => {
      if (focusedNode) {
        void flow.current?.fitView({
          nodes: [focusedNode],
          padding: SEARCH_FIT_PADDING,
          minZoom: SEARCH_FIT_MIN_ZOOM,
          maxZoom: MAX_CANVAS_ZOOM,
        });
      } else {
        void flow.current?.fitView({
          padding: CANVAS_FIT_PADDING,
          maxZoom: MAX_CANVAS_ZOOM,
        });
      }
    });
    return () => cancelAnimationFrame(frame);
  }, [layout, viewportFocusRequest]);

  useEffect(() => {
    const pending = pendingStackFocus.current;
    if (!pending) return;
    const frame = requestAnimationFrame(() => {
      const target = Array.from(
        canvasRef.current?.querySelectorAll<HTMLElement>("[data-stack-key]") ??
          [],
      ).find(
        (candidate) =>
          candidate.dataset.stackKey === pending.key &&
          candidate.dataset.stackAction === pending.action,
      );
      target?.focus();
      if (
        target &&
        document.activeElement === target &&
        pendingStackFocus.current === pending
      ) {
        pendingStackFocus.current = undefined;
      }
    });
    return () => cancelAnimationFrame(frame);
  }, [nodes]);

  if (
    view.resources.length === 0 &&
    (view.kind !== "vpc" || view.vswitches.length === 0)
  ) {
    return (
      <div
        ref={canvasRef}
        data-testid="topology-canvas"
        data-view-kind={view.kind}
        data-complete={complete}
        data-search-highlighted-scope={highlightedScope ? "true" : undefined}
        className={cn(
          "relative grid h-full place-items-center text-sm text-muted-foreground",
          highlightedScope &&
            "outline-2 -outline-offset-2 outline-solid outline-primary",
        )}
      >
        <span>{t("panorama.empty")}</span>
        <ProjectionWarningStatus warnings={warnings} />
      </div>
    );
  }

  return (
    <div
      ref={canvasRef}
      data-testid="topology-canvas"
      data-view-kind={view.kind}
      data-complete={complete}
      data-selected-resource-key={selectedResourceKey}
      data-search-highlighted-scope={highlightedScope ? "true" : undefined}
      data-panorama-canvas
      data-canvas-mode={mode}
      data-panning={isPanning ? "true" : "false"}
      className={cn(
        "relative h-full w-full bg-muted/10",
        mode === "pan" &&
          "[&_.react-flow__pane]:cursor-grab [&_.react-flow__pane:active]:cursor-grabbing",
        highlightedScope &&
          "outline-2 -outline-offset-2 outline-solid outline-primary",
      )}
      tabIndex={-1}
    >
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        colorMode={resolvedTheme}
        minZoom={0.0001}
        maxZoom={MAX_CANVAS_ZOOM}
        fitView
        fitViewOptions={{
          padding: CANVAS_FIT_PADDING,
          maxZoom: MAX_CANVAS_ZOOM,
        }}
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
        onInit={(instance) => {
          flow.current = instance;
        }}
        onMove={(_event, viewport) => setZoom(viewport.zoom)}
        onNodeClick={handleNodeClick}
        onPaneClick={handlePaneClick}
        onSelectionChange={({ nodes: selectedNodes }) => {
          selectedFlowNodes.current = selectedNodes;
        }}
        onSelectionEnd={handleSelectionEnd}
      >
        <Background gap={22} size={1} color="var(--border)" />
      </ReactFlow>
      <CanvasToolbar
        mode={mode}
        zoom={zoom}
        onZoomChange={changeZoom}
        onModeChange={handleToolbarModeChange}
        relationshipsVisible={relationshipsVisible}
        onRelationshipsVisibilityChange={
          layout.edges.length > 0 ? setRelationshipsVisible : undefined
        }
        onEscape={handleToolbarEscape}
        onZoomOut={zoomOut}
        onFitView={fitView}
        onZoomIn={zoomIn}
      />
      <BoxSelectionActionBar
        candidateKeys={candidateTargets.map((target) => target.key)}
        onAdd={() => addTargets(candidateTargets)}
        onCancel={clearCandidates}
      />
      {detailResource && (
        <ResourceDetailDialog
          open
          onOpenChange={(open) => {
            if (!open) setDetailResource(null);
          }}
          connectionId={connection.id}
          resource={detailResource}
          regionName={locationContext.region?.name}
          pendingCleanup={detailPendingCleanup}
          inheritedCleanup={detailInheritedCleanup}
          onAddToCleanup={() => {
            if (detailCleanupTarget) addTargets([detailCleanupTarget]);
          }}
          onRemoveFromCleanup={() => {
            if (detailRemovalSet) executeRemoval(detailRemovalSet);
          }}
        />
      )}
      {stackDetail && (
        <StackMembersDialog
          open
          onOpenChange={(open) => {
            if (!open) setStackDetail(null);
          }}
          displayName={stackDetail.displayName}
          resources={stackDetail.resources}
          onOpenResource={setDetailResource}
        />
      )}
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
      <ProjectionWarningStatus warnings={warnings} />
    </div>
  );
}

function ProjectionWarningStatus({
  warnings,
}: {
  warnings: readonly TopologyProjectionWarning[];
}) {
  const { t } = useLocale();
  if (warnings.length === 0) return null;
  const visibleWarnings = warnings.slice(0, 3);
  const remainingWarningCount = warnings.length - visibleWarnings.length;
  return (
    <aside
      role="status"
      aria-label={t("panorama.projectionWarnings")}
      className="pointer-events-none absolute top-28 right-3 z-10 max-w-[min(28rem,calc(100%-1.5rem))] bg-background/90 px-2.5 py-1.5 text-[11px] text-warning-foreground shadow-sm backdrop-blur sm:top-16"
    >
      {visibleWarnings.map((warning, index) => (
        <p key={`${warning.code}:${warning.relation_key ?? ""}:${index}`}>
          {warning.message}
        </p>
      ))}
      {remainingWarningCount > 0 ? (
        <p className="text-muted-foreground">
          {t("panorama.projectionWarningsRemaining", {
            count: remainingWarningCount,
          })}
        </p>
      ) : null}
    </aside>
  );
}
