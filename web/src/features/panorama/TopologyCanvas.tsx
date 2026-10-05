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
  ReactFlowProvider,
  type Edge,
  type NodeMouseHandler,
  type OnSelectionChangeFunc,
  type ReactFlowInstance,
  type Viewport,
} from "@xyflow/react";
import type {
  ResourceKind,
  ResourceGraphTopologyView,
  TopologyProjectionWarning,
  TopologyResource,
  TopologyResourceEdge,
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
import { ZoomAwareCanvasToolbar } from "./CanvasToolbar";
import { cleanupPendingMatcher, type CleanupTarget } from "./cleanupSelection";
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
const CANVAS_RESIZE_DEBOUNCE_MS = 150;
const CANVAS_FIT_VIEW_OPTIONS = {
  padding: CANVAS_FIT_PADDING,
  maxZoom: MAX_CANVAS_ZOOM,
};
const PRO_OPTIONS = { hideAttribution: true };
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

// Whether a pending target is only covered by a selected Region or VPC, so it
// cannot be removed on its own.
function removalInherited(
  targets: readonly CleanupTarget[],
  target: CleanupTarget,
): boolean {
  const removalSet = cleanupRemovalSet(targets, target);
  return removalSet.inherited && !hasRemovalOperations(removalSet);
}

interface CanvasNodeState {
  focus: ReturnType<typeof connectedEdgeFocus>;
  highlightedNodeKey?: string;
  selectedCandidateKeys: ReadonlySet<string>;
  pendingCleanupResourceKeys: ReadonlySet<string>;
  isCleanupPending: (target: CleanupTarget) => boolean;
  selectMode: boolean;
}

type CanvasNodeDynamic = { nodeSelected: boolean } & Record<string, unknown>;

// A canvas node split into the part built once per layout and the per-render
// state (selection, highlight, cleanup) that decides whether it must change.
interface CanvasNodeEntry {
  dynamic: (state: CanvasNodeState) => CanvasNodeDynamic;
  compose: (dynamic: CanvasNodeDynamic) => TopologyFlowNode;
}

function canvasNodeEntry<D extends CanvasNodeDynamic>(
  dynamic: (state: CanvasNodeState) => D,
  compose: (dynamic: D) => TopologyFlowNode,
): CanvasNodeEntry {
  return {
    dynamic,
    compose: compose as (dynamic: CanvasNodeDynamic) => TopologyFlowNode,
  };
}

function sameNodeDynamic(
  left: CanvasNodeDynamic,
  right: CanvasNodeDynamic,
): boolean {
  const keys = Object.keys(left);
  if (keys.length !== Object.keys(right).length) return false;
  return keys.every((key) => {
    const leftValue = left[key];
    const rightValue = right[key];
    if (Object.is(leftValue, rightValue)) return true;
    return (
      leftValue instanceof Set &&
      rightValue instanceof Set &&
      leftValue.size === rightValue.size &&
      [...leftValue].every((value) => rightValue.has(value))
    );
  });
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
  const handledViewportFocusRequest = useRef<object | undefined>(undefined);
  // Set once the viewport was moved by the user or a focus request; automatic
  // relayouts (resize, more pages) then keep it instead of fitting again.
  const viewportTouched = useRef(false);
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
  // Only a chosen search result or located cleanup target reveals a collapsed
  // member; hovering a search result highlights its stack without relayout.
  const focusRequestNodeKey = searchFocusRequest
    ? searchFocusRequest.nodeKey
    : focusedCleanupTargetKey;
  const focusExpansionKey = useMemo(() => {
    if (!focusRequestNodeKey) return undefined;
    return baseLayout.nodes.find(
      (node) =>
        (node.kind === "stack" &&
          node.memberKeys.includes(focusRequestNodeKey)) ||
        (node.kind === "resourceGroup" &&
          node.childKeys.includes(focusRequestNodeKey)),
    )?.key;
  }, [baseLayout.nodes, focusRequestNodeKey]);
  const layout = useMemo(() => {
    if (!selectedExpansionKey && !focusExpansionKey) return baseLayout;
    const effectiveExpandedStackKeys = new Set(expandedStackKeys);
    if (selectedExpansionKey) {
      effectiveExpandedStackKeys.add(selectedExpansionKey);
    }
    if (focusExpansionKey) {
      effectiveExpandedStackKeys.add(focusExpansionKey);
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
    focusExpansionKey,
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
  const isCleanupPending = useMemo(
    () => cleanupPendingMatcher(cleanupTargets),
    [cleanupTargets],
  );
  const pendingCleanupResourceKeys = useMemo(() => {
    const pending = new Set<string>();
    for (const resource of view.resources) {
      const candidate = resourceTargetsByKey.get(resource.key);
      if (candidate && isCleanupPending(candidate)) {
        pending.add(resource.key);
      }
    }
    return pending;
  }, [isCleanupPending, resourceTargetsByKey, view.resources]);
  // Current values for node callbacks and context-menu getters, so node data
  // stays stable while focus, selection and the cleanup list change.
  const latestValues = {
    cleanupTargets,
    isCleanupPending,
    pendingCleanupResourceKeys,
    focusSelectedKey: focus.selectedKey,
    selectedResourceKey,
    onClearFocus,
    onSelectResource,
    onResourceContextMenu,
    onStackContextMenu,
  };
  const latest = useRef(latestValues);
  latest.current = latestValues;
  const clearFocus = useCallback(() => latest.current.onClearFocus(), []);
  const activateResource = useCallback(
    (resource: TopologyResource, trigger: HTMLElement | null) =>
      latest.current.onSelectResource(resource, trigger),
    [],
  );
  const openStackContextMenu = useCallback(
    (stackKey: string, trigger: HTMLElement | null) =>
      latest.current.onStackContextMenu(stackKey, trigger),
    [],
  );
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
      const currentKeys = new Set(current.map((candidate) => candidate.key));
      const allSelected = incoming.every((target) =>
        currentKeys.has(target.key),
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
      const currentKeys = new Set(current.map((target) => target.key));
      return clicked.every((candidate) => currentKeys.has(candidate.key))
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
      const { selectedResourceKey, onClearFocus } = latest.current;
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
    [],
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
        executeRemoval(
          cleanupRemovalSet(latest.current.cleanupTargets, target),
        );
      }
    },
    [executeRemoval],
  );

  const staticNodes = useMemo<CanvasNodeEntry[]>(() => {
    const vSwitchNativeType = networkNativeType(connection.provider, "vswitch");
    const vSwitchConsoleURL = (vSwitch: TopologyVSwitch) =>
      cloudConsoleURL({
        provider: connection.provider,
        nativeType: vSwitchNativeType,
        nativeId: vSwitch.native_id,
        regionId: regionID,
        consoleLinkTemplate: resourceKinds.get(
          `${connection.provider}:${vSwitchNativeType}`,
        )?.console_link_template,
      });
    const targetInheritsCleanup = (target: CleanupTarget | null) => () =>
      target !== null &&
      latest.current.isCleanupPending(target) &&
      removalInherited(latest.current.cleanupTargets, target);
    const resourceParts = (resource: TopologyResource) => {
      const cleanupTarget = resourceTargetsByKey.get(resource.key) ?? null;
      return {
        selectable: cleanupTarget !== null,
        data: {
          resource,
          cleanupTarget,
          onSelect: (additive: boolean) => {
            if (!cleanupTarget) return false;
            const nowSelected = toggleCandidateTarget(cleanupTarget, additive);
            if (
              !nowSelected &&
              latest.current.focusSelectedKey === resource.key
            ) {
              latest.current.onClearFocus();
            }
            return nowSelected;
          },
          onActivate: activateResource,
          onContextMenu: (
            value: TopologyResource,
            trigger: HTMLElement | null,
          ) => {
            if (
              cleanupTarget &&
              !candidateTargetsRef.current.some(
                (target) => target.key === cleanupTarget.key,
              )
            ) {
              selectCandidateTargets([cleanupTarget], false);
            }
            latest.current.onResourceContextMenu(value, trigger);
          },
          contextMenu: {
            onViewDetails: () => setDetailResource(resource),
            dirtyAsset: {
              connectionId: connection.id,
              id: resource.asset_id,
              dirty: Boolean(resource.dirty),
            },
            dirtyAssets: cleanupTarget
              ? () => dirtyAssetTargets(contextMenuTargets(cleanupTarget))
              : undefined,
            consoleURL: () =>
              cloudConsoleURL({
                provider: connection.provider,
                nativeType: resourceKindNativeType(
                  connection.provider,
                  resource.resource_kind_id,
                ),
                nativeId: resource.native_id,
                regionId: regionID,
                consoleLinkTemplate: resourceKinds.get(
                  resource.resource_kind_id,
                )?.console_link_template,
                templateValues: resource.console_link_values,
              }),
            onAddToCleanup: cleanupTarget
              ? () => addTargets(contextMenuTargets(cleanupTarget))
              : undefined,
            onRemoveFromCleanup: cleanupTarget
              ? () => removeCleanupTargets(contextMenuTargets(cleanupTarget))
              : undefined,
            inheritedCleanup: targetInheritsCleanup(cleanupTarget),
            onOpenChange: setContextMenuOpen,
          },
        },
        dynamic: (state: CanvasNodeState, childKeys?: readonly string[]) => {
          const selected =
            cleanupTarget !== null &&
            state.selectedCandidateKeys.has(cleanupTarget.key);
          return {
            nodeSelected: selected,
            current: state.focus.selectedKey === resource.key,
            searchHighlighted:
              state.highlightedNodeKey === resource.key ||
              cleanupTargetContainsKey(
                cleanupTarget,
                state.highlightedNodeKey,
              ) ||
              Boolean(childKeys?.includes(state.highlightedNodeKey ?? "")),
            selected,
            related: state.focus.relatedKeys.has(resource.key),
            pendingCleanup:
              cleanupTarget !== null && state.isCleanupPending(cleanupTarget),
            interactionEnabled: state.selectMode,
          };
        },
      };
    };

    return layout.nodes.map((node): CanvasNodeEntry => {
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
          const parts = resourceParts(node.resource);
          return canvasNodeEntry(
            (state) => parts.dynamic(state),
            ({ nodeSelected, ...dynamic }) =>
              ({
                ...common,
                selectable: parts.selectable,
                selected: nodeSelected,
                type: "resource",
                data: { ...parts.data, ...dynamic },
              }) satisfies ResourceFlowNode,
          );
        }
        case "resourceGroup": {
          const parts = resourceParts(node.resource);
          return canvasNodeEntry(
            (state) => parts.dynamic(state, node.childKeys),
            ({ nodeSelected, ...dynamic }) =>
              ({
                ...common,
                selectable: parts.selectable,
                selected: nodeSelected,
                type: "resourceGroup",
                data: {
                  ...parts.data,
                  ...dynamic,
                  childKeys: node.childKeys,
                  directChildCount: node.directChildCount,
                  childTypeSummaries: node.childTypeSummaries,
                  childFindingCount: node.childFindingCount,
                  onExpand: expandStack,
                },
              }) satisfies ResourceGroupFlowNode,
          );
        }
        case "expandedResourceGroup": {
          const parts = resourceParts(node.resource);
          return canvasNodeEntry(
            (state) => parts.dynamic(state, node.childKeys),
            ({ nodeSelected, ...dynamic }) =>
              ({
                ...common,
                selectable: parts.selectable,
                selected: nodeSelected,
                type: "expandedResourceGroup",
                data: {
                  ...parts.data,
                  ...dynamic,
                  childKeys: node.childKeys,
                  directChildCount: node.directChildCount,
                  childTypeSummaries: node.childTypeSummaries,
                  childFindingCount: node.childFindingCount,
                  onCollapse: collapseStack,
                },
              }) satisfies ExpandedResourceGroupFlowNode,
          );
        }
        case "vswitch": {
          const vSwitch = node.vSwitch;
          const cleanupTarget = vSwitchTargetsByKey.get(vSwitch.key) ?? null;
          const data = {
            vSwitch,
            cleanupTarget,
            onSelect: (additive: boolean) => {
              if (cleanupTarget) {
                toggleCandidateTarget(cleanupTarget, additive);
              }
              clearFocus();
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
              onViewDetails: () => showVSwitchDetails(vSwitch),
              dirtyAsset: vSwitch.asset_id
                ? {
                    connectionId: connection.id,
                    id: vSwitch.asset_id,
                    dirty: Boolean(vSwitch.dirty),
                  }
                : undefined,
              consoleURL: () => vSwitchConsoleURL(vSwitch),
              onAddToCleanup: cleanupTarget
                ? () => addTargets(contextMenuTargets(cleanupTarget))
                : undefined,
              onRemoveFromCleanup: cleanupTarget
                ? () => removeCleanupTargets(contextMenuTargets(cleanupTarget))
                : undefined,
              inheritedCleanup: targetInheritsCleanup(cleanupTarget),
              onOpenChange: setContextMenuOpen,
            },
          };
          return canvasNodeEntry(
            (state) => {
              const selected =
                cleanupTarget !== null &&
                state.selectedCandidateKeys.has(cleanupTarget.key);
              return {
                nodeSelected: selected,
                selected,
                searchHighlighted:
                  state.highlightedNodeKey === vSwitch.key ||
                  cleanupTargetContainsKey(
                    cleanupTarget,
                    state.highlightedNodeKey,
                  ),
                pendingCleanup:
                  cleanupTarget !== null &&
                  state.isCleanupPending(cleanupTarget),
                interactionEnabled: state.selectMode && cleanupTarget !== null,
              };
            },
            ({ nodeSelected, ...dynamic }) =>
              ({
                ...common,
                selectable: cleanupTarget !== null,
                selected: nodeSelected,
                type: "vswitch",
                data: { ...data, ...dynamic },
              }) satisfies VSwitchFlowNode,
          );
        }
        case "stack":
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
          const memberPendingCount = (pending: ReadonlySet<string>) =>
            node.memberKeys.filter((key) => pending.has(key)).length;
          const stackPending =
            node.kind === "stack"
              ? (pending: ReadonlySet<string>) =>
                  node.count > 0 && memberPendingCount(pending) === node.count
              : (pending: ReadonlySet<string>) =>
                  node.count > 0 &&
                  node.memberKeys.every((key) => pending.has(key));
          const shared = {
            stackKey: node.key,
            resourceKindID: node.resourceKindID,
            typeName: node.typeName,
            typeNames: node.typeNames,
            icon: node.icon,
            count: node.count,
            cleanupTarget,
            contextMenu: {
              onViewDetails: () => setStackDetail({ displayName, resources }),
              onAddToCleanup: () =>
                addTargets(contextMenuTargets(cleanupTarget)),
              onRemoveFromCleanup: () =>
                removeCleanupTargets(contextMenuTargets(cleanupTarget)),
              inheritedCleanup: () =>
                stackPending(latest.current.pendingCleanupResourceKeys) &&
                removalInherited(latest.current.cleanupTargets, cleanupTarget),
              onOpenChange: setContextMenuOpen,
            },
          };
          const stackDynamic = (state: CanvasNodeState) => ({
            nodeSelected: state.selectedCandidateKeys.has(cleanupTarget.key),
            pendingCleanup: stackPending(state.pendingCleanupResourceKeys),
            searchHighlighted:
              node.memberKeys.includes(state.highlightedNodeKey ?? "") ||
              cleanupTargetContainsKey(cleanupTarget, state.highlightedNodeKey),
            interactionEnabled: state.selectMode,
          });
          if (node.kind === "expandedStack") {
            const data = {
              ...shared,
              onCollapse: (stackKey: string) =>
                collapseStack(stackKey, node.memberKeys),
              onContextMenu: openStackContextMenu,
            };
            return canvasNodeEntry(
              stackDynamic,
              ({ nodeSelected, ...dynamic }) =>
                ({
                  ...common,
                  selectable: true,
                  selected: nodeSelected,
                  type: "expandedStack",
                  data: { ...data, ...dynamic },
                }) satisfies ExpandedStackFlowNode,
            );
          }
          const data = {
            ...shared,
            onSelect: (
              _stackKey: string,
              _trigger: HTMLElement | null,
              additive: boolean,
            ) => {
              toggleCandidateTarget(cleanupTarget, additive);
              clearFocus();
            },
            onExpand: expandStack,
            onContextMenu: (stackKey: string, trigger: HTMLElement | null) => {
              if (
                !candidateTargetsRef.current.some(
                  (target) => target.key === cleanupTarget.key,
                )
              ) {
                selectCandidateTargets([cleanupTarget], false);
              }
              latest.current.onStackContextMenu(stackKey, trigger);
            },
          };
          return canvasNodeEntry(
            (state) => ({
              ...stackDynamic(state),
              pendingCleanupCount: memberPendingCount(
                state.pendingCleanupResourceKeys,
              ),
              selected: state.selectedCandidateKeys.has(cleanupTarget.key),
            }),
            ({ nodeSelected, ...dynamic }) =>
              ({
                ...common,
                selectable: true,
                selected: nodeSelected,
                type: "stack",
                data: { ...data, ...dynamic },
              }) satisfies StackFlowNode,
          );
        }
        case "vSwitchStack": {
          const targets = node.vSwitches.flatMap((vSwitch) => {
            const target = vSwitchTargetsByKey.get(vSwitch.key);
            return target ? [target] : [];
          });
          const pendingCount = (
            isPending: (target: CleanupTarget) => boolean,
          ) => targets.filter(isPending).length;
          const data = {
            stackKey: node.key,
            count: node.count,
            vSwitches: node.vSwitches,
            cleanupTargets: targets,
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
                    inheritedCleanup: () =>
                      pendingCount(latest.current.isCleanupPending) ===
                        targets.length &&
                      targets.every((target) =>
                        removalInherited(latest.current.cleanupTargets, target),
                      ),
                    onOpenChange: setContextMenuOpen,
                  }
                : undefined,
          };
          return canvasNodeEntry(
            (state) => {
              const pendingCleanupCount = pendingCount(state.isCleanupPending);
              const selected =
                targets.length > 0 &&
                targets.every((target) =>
                  state.selectedCandidateKeys.has(target.key),
                );
              return {
                nodeSelected: selected,
                selected,
                searchHighlighted:
                  node.vSwitches.some(
                    (vSwitch) => vSwitch.key === state.highlightedNodeKey,
                  ) ||
                  targets.some((target) =>
                    cleanupTargetContainsKey(target, state.highlightedNodeKey),
                  ),
                pendingCleanup:
                  targets.length > 0 && pendingCleanupCount === targets.length,
                pendingCleanupCount,
                interactionEnabled: state.selectMode && targets.length > 0,
              };
            },
            ({ nodeSelected, ...dynamic }) =>
              ({
                ...common,
                selectable: targets.length > 0,
                selected: nodeSelected,
                type: "vSwitchStack",
                data: { ...data, ...dynamic },
              }) satisfies VSwitchStackFlowNode,
          );
        }
        case "expandedVSwitchStack": {
          const cleanupTargetsByVSwitchKey = new Map(
            node.vSwitches.flatMap((vSwitch) => {
              const target = vSwitchTargetsByKey.get(vSwitch.key);
              return target ? ([[vSwitch.key, target]] as const) : [];
            }),
          );
          const targets = [...cleanupTargetsByVSwitchKey.values()];
          const data = {
            stackKey: node.key,
            count: node.count,
            vSwitches: node.vSwitches,
            cleanupTargets: targets,
            cleanupTargetsByVSwitchKey,
            contextMenus: new Map(
              node.vSwitches.map((vSwitch) => {
                const target =
                  cleanupTargetsByVSwitchKey.get(vSwitch.key) ?? null;
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
                    consoleURL: () => vSwitchConsoleURL(vSwitch),
                    onAddToCleanup: target
                      ? () => addTargets(contextMenuTargets(target))
                      : undefined,
                    onRemoveFromCleanup: target
                      ? () => removeCleanupTargets(contextMenuTargets(target))
                      : undefined,
                    inheritedCleanup: targetInheritsCleanup(target),
                    onOpenChange: setContextMenuOpen,
                  },
                ] as const;
              }),
            ),
            onSelect: toggleCandidateTarget,
            onContextMenu: (target: CleanupTarget) => {
              if (
                !candidateTargetsRef.current.some(
                  (candidate) => candidate.key === target.key,
                )
              ) {
                selectCandidateTargets([target], false);
              }
            },
            onCollapse: (stackKey: string) => collapseStack(stackKey, []),
          };
          return canvasNodeEntry(
            (state) => ({
              nodeSelected:
                targets.length > 0 &&
                targets.every((target) =>
                  state.selectedCandidateKeys.has(target.key),
                ),
              selectedTargetKeys: new Set(
                targets
                  .filter((target) =>
                    state.selectedCandidateKeys.has(target.key),
                  )
                  .map((target) => target.key),
              ),
              pendingTargetKeys: new Set(
                targets
                  .filter((target) => state.isCleanupPending(target))
                  .map((target) => target.key),
              ),
              highlightedVSwitchKey: node.vSwitches.find((vSwitch) => {
                const target = cleanupTargetsByVSwitchKey.get(vSwitch.key);
                return (
                  vSwitch.key === state.highlightedNodeKey ||
                  cleanupTargetContainsKey(target, state.highlightedNodeKey)
                );
              })?.key,
              interactionEnabled: state.selectMode && targets.length > 0,
            }),
            ({ nodeSelected, ...dynamic }) =>
              ({
                ...common,
                selectable: targets.length > 0,
                selected: nodeSelected,
                type: "expandedVSwitchStack",
                data: { ...data, ...dynamic },
              }) satisfies ExpandedVSwitchStackFlowNode,
          );
        }
      }
    });
  }, [
    activateResource,
    addTargets,
    clearFocus,
    collapseStack,
    connection.id,
    connection.provider,
    contextAncestry,
    contextMenuTargetGroup,
    contextMenuTargets,
    dirtyAssetTargets,
    expandStack,
    layout.nodes,
    locale,
    locationContext,
    openStackContextMenu,
    regionID,
    removeCleanupTargets,
    resourceKinds,
    resourceTargetsByKey,
    resourcesByKey,
    selectCandidateTargets,
    showVSwitchDetails,
    toggleCandidateTarget,
    toggleCandidateTargets,
    vSwitchTargetsByKey,
  ]);
  const nodeState = useMemo<CanvasNodeState>(
    () => ({
      focus,
      highlightedNodeKey,
      selectedCandidateKeys,
      pendingCleanupResourceKeys,
      isCleanupPending,
      selectMode: mode === "select",
    }),
    [
      focus,
      highlightedNodeKey,
      isCleanupPending,
      mode,
      pendingCleanupResourceKeys,
      selectedCandidateKeys,
    ],
  );
  // Node objects are reused while their selection, highlight and cleanup state
  // is unchanged, so React Flow only re-renders the nodes that changed.
  const composedNodes = useRef(
    new WeakMap<
      CanvasNodeEntry,
      { dynamic: CanvasNodeDynamic; node: TopologyFlowNode }
    >(),
  );
  const nodes = useMemo(
    () =>
      staticNodes.map((entry) => {
        const dynamic = entry.dynamic(nodeState);
        const cached = composedNodes.current.get(entry);
        if (cached && sameNodeDynamic(cached.dynamic, dynamic)) {
          return cached.node;
        }
        const node = entry.compose(dynamic);
        composedNodes.current.set(entry, { dynamic, node });
        return node;
      }),
    [nodeState, staticNodes],
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
  // Like nodes, edge objects are reused while their highlight, handles and
  // labels are unchanged, so a selection only rebuilds the edges it touches.
  const composedEdges = useRef(
    new WeakMap<
      TopologyResourceEdge,
      { key: readonly unknown[]; edge: Edge }
    >(),
  );
  const edges = useMemo<Edge[]>(
    () =>
      visibleLayoutEdges.map((edge) => {
        const highlighted = focus.highlightedEdgeKeys.has(edge.key);
        const handles = edgeHandles.get(edge.key);
        const key = [
          highlighted,
          handles?.sourceHandle,
          handles?.targetHandle,
          label,
          formatNumber,
        ];
        const cached = composedEdges.current.get(edge);
        if (
          cached &&
          cached.key.every((value, index) => value === key[index])
        ) {
          return cached.edge;
        }
        const lifecycle = edge.kind === "lifecycle";
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
        const composed: Edge = {
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
        composedEdges.current.set(edge, { key, edge: composed });
        return composed;
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
  const handleClickCapture = useCallback((event: ReactMouseEvent) => {
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
  }, []);
  const handleInit = useCallback(
    (instance: ReactFlowInstance<TopologyFlowNode, Edge>) => {
      flow.current = instance;
    },
    [],
  );
  const handleMoveStart = useCallback((event: unknown) => {
    if (event) viewportTouched.current = true;
  }, []);
  const handleSelectionChange = useCallback<
    OnSelectionChangeFunc<TopologyFlowNode, Edge>
  >(({ nodes: selectedNodes }) => {
    selectedFlowNodes.current = selectedNodes;
  }, []);
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
      viewportTouched.current = true;
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
    viewportTouched.current = true;
    void flow.current?.zoomOut();
  }, []);
  const fitView = useCallback(() => {
    viewportTouched.current = false;
    void flow.current?.fitView({
      padding: CANVAS_FIT_PADDING,
      maxZoom: MAX_CANVAS_ZOOM,
    });
  }, []);
  const zoomIn = useCallback(() => {
    viewportTouched.current = true;
    void flow.current?.zoomIn();
  }, []);
  const changeZoom = useCallback((nextZoom: number) => {
    viewportTouched.current = true;
    const clampedZoom = Math.min(MAX_CANVAS_ZOOM, Math.max(0.0001, nextZoom));
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
    detailCleanupTarget !== null && isCleanupPending(detailCleanupTarget);
  const detailInheritedCleanup =
    detailPendingCleanup &&
    detailRemovalSet !== null &&
    detailRemovalSet.inherited &&
    !hasRemovalOperations(detailRemovalSet);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    let timer: number | undefined;
    const measure = () => {
      const bounds = canvas.getBoundingClientRect();
      const width = Math.round(bounds.width);
      const height = Math.round(bounds.height);
      if (width <= 0 || height <= 0) return;
      setCanvasSize((current) =>
        current.width === width && current.height === height
          ? current
          : { width, height },
      );
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    // Relayout once the size settles (e.g. after the sidebar animation)
    // instead of on every animation frame.
    const observer = new ResizeObserver(() => {
      window.clearTimeout(timer);
      timer = window.setTimeout(measure, CANVAS_RESIZE_DEBOUNCE_MS);
    });
    observer.observe(canvas);
    return () => {
      observer.disconnect();
      window.clearTimeout(timer);
    };
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
      } else if (!viewportTouched.current) {
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
    if (
      !viewportFocusRequest ||
      handledViewportFocusRequest.current === viewportFocusRequest
    ) {
      return;
    }
    const requestedNodeKey = viewportFocusRequest.nodeKey;
    const focusedNode = requestedNodeKey
      ? (nodes.find((node) => node.id === requestedNodeKey) ??
        nodes.find((node) =>
          topologyNodeContainsTarget(node, requestedNodeKey),
        ))
      : undefined;
    if (!focusedNode && !viewportFocusRequest.scope) return;
    viewportTouched.current = true;
    const frame = requestAnimationFrame(() => {
      // Fit each request once; later relayouts keep the focused viewport.
      handledViewportFocusRequest.current = viewportFocusRequest;
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
      <ReactFlowProvider>
        <ReactFlow
          nodes={nodes}
          edges={edges}
          nodeTypes={nodeTypes}
          colorMode={resolvedTheme}
          minZoom={0.0001}
          maxZoom={MAX_CANVAS_ZOOM}
          onlyRenderVisibleElements
          fitView
          fitViewOptions={CANVAS_FIT_VIEW_OPTIONS}
          nodesDraggable={false}
          nodesConnectable={false}
          nodesFocusable={false}
          elementsSelectable={mode === "select"}
          selectionOnDrag={mode === "select"}
          panOnDrag={mode === "pan" ? true : [1]}
          selectionKeyCode={null}
          multiSelectionKeyCode="Shift"
          proOptions={PRO_OPTIONS}
          onClickCapture={handleClickCapture}
          onPointerDownCapture={handleNodePanStart}
          onPointerMoveCapture={handleNodePanMove}
          onPointerUpCapture={finishNodePan}
          onPointerCancelCapture={finishNodePan}
          onInit={handleInit}
          onMoveStart={handleMoveStart}
          onNodeClick={handleNodeClick}
          onPaneClick={handlePaneClick}
          onSelectionChange={handleSelectionChange}
          onSelectionEnd={handleSelectionEnd}
        >
          <Background gap={22} size={1} color="var(--border)" />
        </ReactFlow>
        <ZoomAwareCanvasToolbar
          mode={mode}
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
      </ReactFlowProvider>
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
