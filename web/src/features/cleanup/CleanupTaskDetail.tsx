import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import {
  AlertTriangle,
  ChevronRight,
  CircleHelp,
  FileText,
  Info,
  ListPlus,
  LocateFixed,
  Pause,
  Play,
  Search,
} from "lucide-react";
import {
  Link,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router-dom";
import { toast } from "sonner";
import {
  addCleanupTaskAssets,
  continueCleanupExecution,
  createExecution,
  findAssets,
  getCleanupTask,
  listExecutionActions,
  listCleanupTaskExecutions,
  listConnectionRegions,
  listProviderCatalog,
  pauseCleanupExecution,
  resumeCleanupExecution,
} from "@/api/client";
import type {
  ActionAttempt,
  Asset,
  ConnectionRegion,
  CleanupTask,
  CleanupTaskAggregate,
  CleanupSelector,
  ExecutionAttempt,
  ImpactItem,
  CleanupBlocker,
  CleanupWarning,
  ResourceKind,
} from "@/api/types";
import { PageTitle } from "@/app/PageTitleContext";
import { AsyncState } from "@/components/domain/AsyncState";
import { CloudProviderIcon } from "@/components/domain/CloudProviderIcon";
import { CopyableId } from "@/components/domain/CopyableId";
import { CursorPagination } from "@/components/domain/CursorPagination";
import { DataTableShell } from "@/components/domain/DataTableShell";
import { DirtyDataIcon } from "@/components/domain/DirtyDataIcon";
import {
  ResourceMarkerBadge,
  resolveResourceMarker,
  type ResourceMarker,
} from "@/components/domain/ResourceMarkerBadge";
import {
  compareResourceKindOptionsByProduct,
  resourceProductName,
  resourceTypeName,
} from "@/components/domain/resourceKindLabel";
import { ResourceKindPicker } from "@/components/domain/ResourceKindPicker";
import { StateBadge } from "@/components/domain/StateBadge";
import { PageLayout } from "@/components/patterns/PageLayout";
import { PageToolbar } from "@/components/patterns/PageToolbar";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { useRequiredConnection } from "@/connections/ActiveConnectionProvider";
import { DEFAULT_PAGE_SIZE, type PageSize } from "@/hooks/useCursorPagination";
import { useLocale } from "@/i18n/LocaleProvider";
import { formatDuration } from "@/lib/formatDuration";
import {
  assetConsoleRegionID,
  assetRegionLabel,
  resolveAssetRegion,
} from "../assets/assetRegions";
import { cloudConsoleURL } from "../panorama/consoleLinks";
import { panoramaResourcePath } from "../panorama/route";
import { CleanupTaskEvents } from "./CleanupTaskEvents";
import {
  confirmationMode,
  dedupeSelectors,
  executionConfirmationSatisfied,
  selectorKey,
  typedConfirmationNames,
  writeCleanupSelectionHandoff,
} from "./selection";

interface CleanupTargetDialogState {
  selector: CleanupSelector;
  name: string;
  assetIDs: string[];
  initialAssets: Asset[];
  initialAssetsReady: boolean;
}

const allStatuses = "__all__";
const defaultExecutionConcurrency = "20";
type CleanupTaskTab = "resources" | "logs" | "review";
const cleanupTaskTabs: CleanupTaskTab[] = ["resources", "logs", "review"];

function cleanupTaskTab(value: string | null): CleanupTaskTab | null {
  return cleanupTaskTabs.includes(value as CleanupTaskTab)
    ? (value as CleanupTaskTab)
    : null;
}

function parseExecutionConcurrency(value: string) {
  if (!/^\d+$/.test(value)) return null;
  const parsed = Number(value);
  return Number.isInteger(parsed) && parsed >= 1 && parsed <= 100
    ? parsed
    : null;
}

function clampExecutionConcurrency(value: string) {
  if (value === "") return value;
  const parsed = Number(value);
  if (parsed === Number.POSITIVE_INFINITY) return "100";
  if (parsed === Number.NEGATIVE_INFINITY) return "1";
  if (Number.isNaN(parsed)) return defaultExecutionConcurrency;
  return String(Math.min(100, Math.max(1, Math.trunc(parsed))));
}

export function CleanupTaskDetail() {
  const { id = "" } = useParams();
  const [searchParams, setSearchParams] = useSearchParams();
  const connection = useRequiredConnection();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const {
    formatDate,
    formatError,
    formatNumber,
    label,
    locale,
    messageForCode,
    t,
  } = useLocale();
  const [confirmationOpen, setConfirmationOpen] = useState(false);
  const [continueConfirmationOpen, setContinueConfirmationOpen] =
    useState(false);
  const [executionConcurrency, setExecutionConcurrency] = useState(
    defaultExecutionConcurrency,
  );
  const [continueConcurrency, setContinueConcurrency] = useState(
    defaultExecutionConcurrency,
  );
  const [dependencyAssetIDs, setDependencyAssetIDs] = useState<string[]>([]);
  const [targetDialog, setTargetDialog] =
    useState<CleanupTargetDialogState | null>(null);
  const [typedConfirmations, setTypedConfirmations] = useState<
    Record<string, string>
  >({});
  const [explicitConfirmation, setExplicitConfirmation] = useState(false);
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState(allStatuses);
  const [resourceKindIDs, setResourceKindIDs] = useState<string[]>([]);
  const [logResourceID, setLogResourceID] = useState("");
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState<PageSize>(DEFAULT_PAGE_SIZE);
  const activeActionsRef = useRef(false);

  const detail = useQuery({
    queryKey: ["cleanup-task", connection.id, id],
    queryFn: () => getCleanupTask(connection.id, id),
    enabled: Boolean(id),
    refetchInterval: (value) =>
      ["executing", "pausing"].includes(value.state.data?.task.status ?? "")
        ? 2500
        : false,
  });
  const aggregate = detail.data
    ? {
        ...detail.data,
        steps: detail.data.steps ?? [],
        impact_items: detail.data.impact_items ?? [],
      }
    : undefined;
  const hasExecution =
    aggregate !== undefined &&
    [
      "executing",
      "pausing",
      "paused",
      "completed",
      "failed",
      "canceled",
    ].includes(aggregate.task.status);
  const defaultTab: CleanupTaskTab = hasExecution ? "resources" : "review";
  const activeTab = cleanupTaskTab(searchParams.get("tab")) ?? defaultTab;
  const changeTab = (nextTab: CleanupTaskTab) => {
    setSearchParams((current) => {
      const next = new URLSearchParams(current);
      if (nextTab === defaultTab) next.delete("tab");
      else next.set("tab", nextTab);
      return next;
    });
  };
  useEffect(() => {
    setLogResourceID("");
  }, [hasExecution, id]);
  const executions = useQuery({
    queryKey: ["cleanup-task-executions", connection.id, id],
    queryFn: () => listCleanupTaskExecutions(connection.id, id, "", 20),
    enabled: Boolean(id) && hasExecution,
    refetchInterval: (query) => {
      const latest = latestExecution(query.state.data?.items ?? []);
      if (!latest) {
        return ["executing", "pausing"].includes(aggregate?.task.status ?? "")
          ? 2500
          : false;
      }
      return isActiveExecution(latest.status) ? 2500 : false;
    },
  });
  const attempt = latestExecution(executions.data?.items ?? []);
  const actions = useQuery({
    queryKey: ["cleanup-actions", connection.id, attempt?.id],
    queryFn: () => listExecutionActions(connection.id, attempt!.id),
    enabled: Boolean(attempt?.id),
    refetchInterval: (query) =>
      shouldPollCleanupActions(attempt?.status, query.state.data?.items ?? [])
        ? 2000
        : false,
  });
  const terminalActionsPending =
    isTerminalExecution(attempt?.status ?? "") &&
    shouldPollCleanupActions(attempt?.status, actions.data?.items ?? []) &&
    (actions.data?.items ?? []).some(
      (action) => !isTerminalAction(action.status),
    );
  useEffect(() => {
    if (!terminalActionsPending) return;
    void queryClient.invalidateQueries({
      queryKey: ["cleanup-actions", connection.id, attempt?.id],
      exact: true,
    });
  }, [
    attempt?.id,
    attempt?.status,
    connection.id,
    queryClient,
    terminalActionsPending,
  ]);
  const hasActiveActions =
    shouldPollCleanupActions(attempt?.status, actions.data?.items ?? []) &&
    (actions.data?.items ?? []).some(
      (action) => !isTerminalAction(action.status),
    );

  const baseResourceRows = useMemo(
    () =>
      aggregate
        ? cleanupResourceRows(
            aggregate.steps,
            aggregate.impact_items,
            aggregate.task.blockers ?? [],
            aggregate.task.warnings ?? [],
            actions.data?.items ?? [],
            attempt?.status,
            messageForCode,
          )
        : [],
    [actions.data?.items, aggregate, attempt?.status, messageForCode],
  );

  const assetIDs = useMemo(() => {
    if (!aggregate) return [];
    return [
      ...new Set(
        [
          ...aggregate.task.resolved_asset_ids,
          ...baseResourceRows.map((row) => row.assetID),
          ...aggregate.impact_items.map((impact) => impact.controller_id),
          ...aggregate.task.selectors.flatMap((selector) =>
            selector.kind === "asset" ? [selector.asset_id] : [],
          ),
          ...(aggregate.task.warnings ?? []).flatMap((warning) => [
            warning.asset_id ?? "",
            warning.controller_id ?? "",
          ]),
          ...(aggregate.task.blockers ?? []).flatMap((blocker) => [
            blocker.asset_id ?? "",
            blocker.controller_id ?? "",
            dependentAssetID(blocker.evidence),
          ]),
        ].filter(Boolean),
      ),
    ];
  }, [aggregate, baseResourceRows]);
  const assets = useQuery({
    queryKey: [
      "cln-detail-assets",
      connection.id,
      id,
      aggregate?.task.snapshot_hash,
      assetIDs,
    ],
    queryFn: () => findAssets(connection.id, assetIDs),
    enabled: assetIDs.length > 0,
  });
  const actionStatusKey = (actions.data?.items ?? [])
    .map((action) => `${action.id}:${action.status}`)
    .sort()
    .join("|");
  useEffect(() => {
    if (!actions.data) return;
    const previouslyActive = activeActionsRef.current;
    activeActionsRef.current = hasActiveActions;
    if (!previouslyActive || hasActiveActions) return;
    void queryClient.invalidateQueries({
      queryKey: ["cln-detail-assets", connection.id, id],
    });
  }, [
    actionStatusKey,
    actions.data,
    connection.id,
    hasActiveActions,
    id,
    queryClient,
  ]);
  const byID = useMemo(
    () => new Map((assets.data ?? []).map((asset) => [asset.id, asset])),
    [assets.data],
  );
  const resourceRows = useMemo(
    () => projectManagedResourceRows(baseResourceRows, byID),
    [baseResourceRows, byID],
  );
  const catalog = useQuery({
    queryKey: ["catalog"],
    queryFn: listProviderCatalog,
  });
  const regions = useQuery({
    queryKey: ["connection-regions", connection.id, "cleanup-detail"],
    queryFn: () => listConnectionRegions(connection.id),
  });
  const connectionRegions = useMemo(
    () => regions.data?.items ?? [],
    [regions.data],
  );
  const resourceKindsByID = useMemo(
    () =>
      new Map(
        (catalog.data ?? []).flatMap((bundle) =>
          bundle.kinds.map((kind) => [kind.id, kind] as const),
        ),
      ),
    [catalog.data],
  );
  const dependencyTargets = dependencyAssetIDs.map((assetID) => {
    const asset = byID.get(assetID);
    const kind = asset
      ? resourceKindsByID.get(asset.resource_kind_id)
      : undefined;
    return {
      assetID,
      name: resourceDisplayName(assetID, byID),
      nativeID: asset?.identity.native_id || assetID,
      typeName: asset
        ? resourceTypeName(
            kind?.native_type || asset.identity.native_type,
            kind?.display_name || asset.identity.native_type,
            kind?.display_names,
            locale,
          )
        : "",
    };
  });
  const resourceKindOptions = useMemo(() => {
    const available = new Map(
      resourceRows.flatMap((row) => {
        const asset = byID.get(row.assetID);
        return asset
          ? [
              [
                asset.resource_kind_id,
                {
                  id: asset.resource_kind_id,
                  nativeType: asset.identity.native_type,
                  kind: resourceKindsByID.get(asset.resource_kind_id),
                },
              ] as const,
            ]
          : [];
      }),
    );
    return [...available.values()]
      .map(({ id, nativeType, kind }) => {
        const product = resourceProductName(kind?.native_type || nativeType);
        return {
          value: id,
          label: resourceTypeName(
            kind?.native_type || nativeType,
            kind?.display_name || nativeType,
            kind?.display_names,
            locale,
          ),
          tag: product === "—" ? undefined : product,
          keywords: [
            product,
            nativeType,
            ...Object.values(kind?.display_names ?? {}),
          ],
        };
      })
      .sort((left, right) =>
        compareResourceKindOptionsByProduct(left, right, locale),
      );
  }, [byID, locale, resourceKindsByID, resourceRows]);
  const taskStatus = aggregate?.task.status;
  useEffect(() => {
    if (
      !taskStatus ||
      !["completed", "failed", "canceled", "invalidated"].includes(taskStatus)
    ) {
      return;
    }
    void queryClient.invalidateQueries({
      queryKey: ["assets", connection.id],
    });
    void queryClient.invalidateQueries({
      queryKey: ["topology", connection.id],
    });
    void queryClient.invalidateQueries({
      queryKey: ["panorama-search", connection.id],
    });
  }, [connection.id, taskStatus, queryClient]);

  const refreshRejectedExecution = () => {
    void queryClient.invalidateQueries({
      queryKey: ["cleanup-task", connection.id, id],
    });
    void queryClient.invalidateQueries({
      queryKey: ["cleanup", connection.id],
    });
    void queryClient.invalidateQueries({
      queryKey: ["cleanup-task-executions", connection.id, id],
    });
  };

  const execute = useMutation({
    mutationFn: () =>
      createExecution(
        connection.id,
        id,
        crypto.randomUUID(),
        parseExecutionConcurrency(executionConcurrency)!,
        {
          typed_names: typedConfirmations,
          acknowledged: explicitConfirmation,
        },
      ),
    onSuccess: (created) => {
      setConfirmationOpen(false);
      queryClient.setQueryData(["cleanup-task-executions", connection.id, id], {
        items: [created],
      });
      queryClient.setQueryData(
        ["cleanup-task", connection.id, id],
        (current: typeof detail.data) =>
          current
            ? {
                ...current,
                task: { ...current.task, status: "executing" },
              }
            : current,
      );
      void queryClient.invalidateQueries({
        queryKey: ["cleanup", connection.id],
      });
    },
    onError: refreshRejectedExecution,
  });
  const continueExecution = useMutation({
    mutationFn: () =>
      continueCleanupExecution(
        connection.id,
        id,
        crypto.randomUUID(),
        parseExecutionConcurrency(continueConcurrency)!,
      ),
    onSuccess: (continued) => {
      setContinueConfirmationOpen(false);
      queryClient.setQueryData(
        ["cleanup-task-executions", connection.id, id],
        (current: typeof executions.data) =>
          current
            ? {
                ...current,
                items: current.items.map((item) =>
                  item.id === continued.id ? continued : item,
                ),
              }
            : { items: [continued] },
      );
      queryClient.setQueryData(
        ["cleanup-task", connection.id, id],
        (current: typeof detail.data) =>
          current
            ? {
                ...current,
                task: { ...current.task, status: "executing" },
              }
            : current,
      );
      void queryClient.invalidateQueries({
        queryKey: ["cleanup-actions", connection.id, continued.id],
      });
      void queryClient.invalidateQueries({
        queryKey: ["cleanup", connection.id],
      });
    },
    onError: refreshRejectedExecution,
  });
  const pauseExecution = useMutation({
    mutationFn: () => pauseCleanupExecution(connection.id, id),
    onSuccess: (paused) => {
      updateControlledExecutionCache(
        queryClient,
        connection.id,
        id,
        paused,
        paused.status === "paused" ? "paused" : "pausing",
      );
      void queryClient.invalidateQueries({
        queryKey: ["cleanup", connection.id],
      });
    },
    onError: refreshRejectedExecution,
  });
  const resumeExecution = useMutation({
    mutationFn: () => resumeCleanupExecution(connection.id, id),
    onSuccess: (resumed) => {
      updateControlledExecutionCache(
        queryClient,
        connection.id,
        id,
        resumed,
        "executing",
      );
      void queryClient.invalidateQueries({
        queryKey: ["cleanup", connection.id],
      });
    },
    onError: refreshRejectedExecution,
  });
  const addDependencies = useMutation({
    mutationFn: (assetIDs: string[]) =>
      addCleanupTaskAssets(connection.id, id, assetIDs),
    onSuccess: (updated) => {
      setDependencyAssetIDs([]);
      setStatus(allStatuses);
      setPage(1);
      queryClient.setQueryData(["cleanup-task", connection.id, id], updated);
      void queryClient.invalidateQueries({
        queryKey: ["cleanup", connection.id],
      });
      toast.success(t("cleanup.dependenciesAdded"));
    },
  });

  const completedCount = resourceRows.filter((row) =>
    isCompletedResource(row.status),
  ).length;
  const failedCount = resourceRows.filter((row) =>
    isFailedResource(row.status),
  ).length;
  const pausedCount = resourceRows.filter(
    (row) => row.status === "paused",
  ).length;
  const processingCount = resourceRows.filter(
    (row) =>
      !isCompletedResource(row.status) &&
      !isFailedResource(row.status) &&
      ![
        "blocked",
        "not_actionable",
        "paused",
        "pending",
        "skipped",
        "waiting",
      ].includes(row.status),
  ).length;
  const resourceStatuses = useMemo(
    () =>
      [...new Set(resourceRows.map((row) => row.status))].sort(
        (left, right) =>
          resourcePriority(left) - resourcePriority(right) ||
          label(left).localeCompare(label(right), locale),
      ),
    [label, locale, resourceRows],
  );
  const filteredRows = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return resourceRows
      .filter((row) => status === allStatuses || row.status === status)
      .filter((row) => {
        if (resourceKindIDs.length === 0) return true;
        const asset = byID.get(row.assetID);
        return Boolean(
          asset && resourceKindIDs.includes(asset.resource_kind_id),
        );
      })
      .filter(
        (row) =>
          !normalized ||
          byID.get(row.assetID)?.name?.toLowerCase().includes(normalized) ||
          byID
            .get(row.assetID)
            ?.identity.native_id.toLowerCase()
            .includes(normalized) ||
          cleanupActionDisplayLabel(
            row,
            byID,
            resourceKindsByID,
            locale,
            label,
            t,
          )
            .toLowerCase()
            .includes(normalized) ||
          cleanupAssetRegionLabel(
            byID.get(row.assetID),
            connectionRegions,
            t("common.global"),
          )
            .toLowerCase()
            .includes(normalized) ||
          row.status.toLowerCase().includes(normalized),
      )
      .sort(
        (left, right) =>
          resourcePriority(left.status) - resourcePriority(right.status) ||
          left.assetID.localeCompare(right.assetID),
      );
  }, [
    byID,
    connectionRegions,
    label,
    locale,
    query,
    resourceKindIDs,
    resourceKindsByID,
    resourceRows,
    status,
    t,
  ]);
  const pageCount = Math.max(1, Math.ceil(filteredRows.length / pageSize));
  const visibleRows = filteredRows.slice(
    (page - 1) * pageSize,
    page * pageSize,
  );
  useEffect(() => setPage(1), [query, resourceKindIDs, status]);
  useEffect(() => {
    if (page > pageCount) setPage(pageCount);
  }, [page, pageCount]);

  if (!aggregate) {
    return (
      <>
        <PageTitle
          title={t("cleanup.detailTitle", { id })}
          parent={{ label: t("nav.cleanup"), to: "/cleanup" }}
        />
        <AsyncState
          pending={detail.isPending}
          error={
            detail.error ?? (!detail.isPending ? t("cleanup.notFound") : null)
          }
          empty={false}
          onRetry={() => void detail.refetch()}
          formatError={formatError}
          labels={{
            empty: t("cleanup.notFound"),
            retry: t("shell.retry"),
            failed: t("shell.routeFailure"),
            loading: t("cleanup.loadingDetail"),
          }}
        >
          <span />
        </AsyncState>
      </>
    );
  }

  const typedNames = typedConfirmationNames(aggregate.task.selectors);
  const needsExplicitConfirmation = aggregate.task.selectors.some(
    (selector) => confirmationMode(selector) === "confirm",
  );
  const confirmationSatisfied = executionConfirmationSatisfied({
    requiredNames: typedNames,
    typedNames: typedConfirmations,
    acknowledgementRequired: needsExplicitConfirmation,
    acknowledged: explicitConfirmation,
  });
  const scanCoverageAdvisories = (aggregate.task.blockers ?? []).filter(
    (blocker) => blocker.code === "scan_coverage_incomplete",
  );
  const executionBlockers = (aggregate.task.blockers ?? []).filter(
    (blocker) => blocker.code !== "scan_coverage_incomplete",
  );
  const legacyCoverageAdvisoryTask =
    aggregate.task.status === "draft" &&
    scanCoverageAdvisories.length > 0 &&
    executionBlockers.length === 0;
  const canExecute =
    (aggregate.task.status === "ready" || legacyCoverageAdvisoryTask) &&
    aggregate.steps.length > 0 &&
    executionBlockers.length === 0;
  const recoverableBlockedExecution =
    aggregate.task.status === "executing" &&
    attempt?.status === "running" &&
    cleanupExecutionBlockedByFailure(
      aggregate.steps,
      actions.data?.items ?? [],
    );
  const canContinue =
    aggregate.steps.length > 0 &&
    ((aggregate.task.status === "failed" && attempt?.status === "failed") ||
      recoverableBlockedExecution);
  const canPause =
    aggregate.task.status === "executing" &&
    Boolean(attempt && pausableExecutionStatus(attempt.status));
  const canResume =
    ["pausing", "paused"].includes(aggregate.task.status) &&
    Boolean(attempt && !isTerminalExecution(attempt.status));
  const dependentAssetIDs = [
    ...new Set(
      (aggregate.task.blockers ?? [])
        .map((blocker) => dependentAssetID(blocker.evidence))
        .filter(Boolean),
    ),
  ];
  const managedControllerIDs = [
    ...new Set(
      (aggregate.task.blockers ?? [])
        .filter(
          (blocker) =>
            blocker.code === "managed_by_controller" &&
            lifecycleKind(blocker.evidence) === "vpc_system_route_table",
        )
        .map(managedControllerAssetID)
        .filter(Boolean),
    ),
  ];
  const managedSourceAssetIDs = [
    ...new Set(
      (aggregate.task.blockers ?? [])
        .filter(
          (blocker) =>
            blocker.code === "managed_by_controller" &&
            lifecycleKind(blocker.evidence) !== "vpc_system_route_table" &&
            !isCENSystemLifecycle(lifecycleKind(blocker.evidence)),
        )
        .map(managedControllerAssetID)
        .filter(Boolean),
    ),
  ];
  const managedTransitRouterIDs = [
    ...new Set(
      [...(aggregate.task.blockers ?? []), ...(aggregate.task.warnings ?? [])]
        .filter(
          (item) =>
            item.code === "managed_by_controller" &&
            isCENSystemLifecycle(lifecycleKind(item.evidence)),
        )
        .map(managedControllerAssetID)
        .filter(Boolean),
    ),
  ];
  const rebuildWithAssets = (assetIDs: string[]) => {
    const selectors = dedupeSelectors([
      ...aggregate.task.selectors,
      ...assetIDs.map((asset_id): CleanupSelector => ({
        kind: "asset",
        asset_id,
      })),
    ]);
    writeCleanupSelectionHandoff(connection.id, selectors);
    navigate("/cleanup/new", {
      state: { fromCleanupReview: true, selectors },
    });
  };
  const total = Math.max(aggregate.steps.length, resourceRows.length);
  const pendingCount = Math.max(
    0,
    total - completedCount - failedCount - pausedCount - processingCount,
  );
  const hasExecutionResult =
    processingCount === 0 &&
    ["completed", "failed"].includes(aggregate.task.status);
  const distributionSegments: CleanupStatusSegment[] = [
    {
      key: "succeeded",
      count: completedCount,
      label: t("cleanup.resultSucceeded", { count: completedCount }),
      colorClass: "bg-success",
    },
    {
      key: "failed",
      count: failedCount,
      label: t("cleanup.resultFailed", { count: failedCount }),
      colorClass: "bg-destructive",
    },
  ];
  if (processingCount > 0) {
    distributionSegments.push({
      key: "processing",
      count: processingCount,
      label: t("cleanup.resultActive", { count: processingCount }),
      colorClass: "bg-info",
    });
  }
  if (pausedCount > 0) {
    distributionSegments.push({
      key: "paused",
      count: pausedCount,
      label: t("cleanup.resultPaused", { count: pausedCount }),
      colorClass: "bg-muted-foreground/60",
    });
  }
  if (pendingCount > 0) {
    distributionSegments.push({
      key: "pending",
      count: pendingCount,
      label: t(
        aggregate.task.status === "failed"
          ? "cleanup.resultUnprocessed"
          : "cleanup.resultPending",
        { count: pendingCount },
      ),
      colorClass: "bg-muted-foreground/40",
    });
  }
  const loadError =
    assets.error ??
    executions.error ??
    actions.error ??
    execute.error ??
    continueExecution.error ??
    pauseExecution.error ??
    resumeExecution.error;
  const liveLogs =
    ["executing", "pausing"].includes(aggregate.task.status) ||
    Boolean(attempt && isActiveExecution(attempt.status)) ||
    hasActiveActions;
  const inspectCleanupTarget = (
    selector: CleanupSelector,
    selectorIndex: number,
  ) => {
    const asset =
      selector.kind === "asset" ? byID.get(selector.asset_id) : undefined;
    const name = cleanupTargetName(selector, asset, assets.isPending, t);
    const targetAssetIDs = cleanupTargetAssetIDs(
      aggregate.task,
      selector,
      selectorIndex,
    );
    setTargetDialog({
      selector,
      name,
      assetIDs: targetAssetIDs,
      initialAssets: targetAssetIDs.flatMap((assetID) => {
        const value = byID.get(assetID);
        return value ? [value] : [];
      }),
      initialAssetsReady: assets.isSuccess,
    });
  };

  return (
    <>
      <PageTitle
        title={aggregate.task.id}
        parent={{ label: t("nav.cleanup"), to: "/cleanup" }}
      />
      <PageLayout mode="reading" className="space-y-6">
        <PageToolbar
          label={t("cleanup.taskDetail")}
          trailing={
            canExecute ? (
              <Button
                variant="destructive"
                onClick={() => setConfirmationOpen(true)}
              >
                <Play />
                {t("cleanup.execute")}
              </Button>
            ) : canContinue ? (
              <Button
                variant="destructive"
                disabled={continueExecution.isPending}
                onClick={() => setContinueConfirmationOpen(true)}
              >
                <Play />
                {continueExecution.isPending
                  ? t("cleanup.continuing")
                  : t("cleanup.continue")}
              </Button>
            ) : canPause ? (
              <Button
                variant="outline"
                disabled={pauseExecution.isPending}
                onClick={() => pauseExecution.mutate()}
              >
                <Pause />
                {pauseExecution.isPending
                  ? t("cleanup.pausing")
                  : t("cleanup.pause")}
              </Button>
            ) : canResume ? (
              <Button
                variant="outline"
                disabled={resumeExecution.isPending}
                onClick={() => resumeExecution.mutate()}
              >
                <Play />
                {resumeExecution.isPending
                  ? t("cleanup.resuming")
                  : t("cleanup.resume")}
              </Button>
            ) : null
          }
        />

        {loadError && (
          <Alert variant="destructive">
            <AlertDescription>{formatError(loadError)}</AlertDescription>
          </Alert>
        )}
        <CleanupWarnings
          warnings={[
            ...(aggregate.task.warnings ?? []),
            ...scanCoverageAdvisories,
          ]}
          blockers={executionBlockers}
          dependentAssetIDs={dependentAssetIDs}
          managedControllerIDs={managedControllerIDs}
          managedSourceAssetIDs={managedSourceAssetIDs}
          managedTransitRouterIDs={managedTransitRouterIDs}
          onRebuildAssets={rebuildWithAssets}
          onAddDependencies={setDependencyAssetIDs}
          onShowResourceBlockers={() => {
            setQuery("");
            setResourceKindIDs([]);
            setStatus("blocked");
            setPage(1);
            changeTab("resources");
          }}
          messageForCode={messageForCode}
          t={t}
        />

        <dl className="grid gap-x-16 gap-y-3 md:grid-cols-2">
          <Fact label={t("cleanup.taskId")} mono>
            <CopyableId label={t("cleanup.taskId")} value={aggregate.task.id} />
          </Fact>
          <Fact label={t("common.status")}>
            <StateBadge
              value={
                legacyCoverageAdvisoryTask ? "ready" : aggregate.task.status
              }
              label={label(
                legacyCoverageAdvisoryTask ? "ready" : aggregate.task.status,
              )}
            />
          </Fact>
          <Fact label={t("common.requestedBy")}>
            {attempt?.requested_by || aggregate.task.created_by}
          </Fact>
          <Fact label={t("cleanup.duration")}>
            {formatDuration(attempt?.duration_ms)}
          </Fact>
          <Fact label={t("common.created")}>
            {formatDate(aggregate.task.created_at)}
          </Fact>
          <Fact label={t("common.updated")}>
            {formatDate(
              attempt?.updated_at ||
                aggregate.task.updated_at ||
                aggregate.task.created_at,
            )}
          </Fact>
          <Fact label={t("cleanup.resources")}>
            {formatNumber(aggregate.task.resolved_asset_ids.length)}
          </Fact>
          <Fact label={t("cleanup.scope")}>
            {scopeLabel(aggregate.task.selectors, t)}
          </Fact>
        </dl>

        <CleanupStatusDistribution
          title={t(
            hasExecutionResult
              ? "cleanup.executionResult"
              : "cleanup.resourceStatus",
          )}
          totalLabel={t("cleanup.resultTotal", { count: total })}
          total={total}
          segments={distributionSegments}
        />

        <Tabs
          value={activeTab}
          onValueChange={(value) => changeTab(value as CleanupTaskTab)}
          className="gap-4"
        >
          <TabsList className="h-auto flex-wrap justify-start">
            <TabsTrigger value="resources">
              {t("cleanup.resourceResults")}
            </TabsTrigger>
            <TabsTrigger value="logs">{t("cleanup.cleanupLogs")}</TabsTrigger>
            <TabsTrigger value="review">{t("cleanup.selectors")}</TabsTrigger>
          </TabsList>

          <TabsContent value="resources">
            <DataTableShell
              toolbar={
                <>
                  <div className="relative min-w-64 flex-1 self-start md:max-w-xl">
                    <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
                    <Input
                      className="h-11 pl-9 sm:h-9"
                      value={query}
                      onChange={(event) => setQuery(event.target.value)}
                      placeholder={t("cleanup.searchResources")}
                      aria-label={t("cleanup.searchResources")}
                    />
                  </div>
                  <div className="flex w-full flex-wrap items-start gap-2 sm:w-auto">
                    <Select value={status} onValueChange={setStatus}>
                      <SelectTrigger
                        className="h-11 w-full sm:h-9 sm:w-40"
                        aria-label={t("common.statusFilter")}
                      >
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent align="start">
                        <SelectItem value={allStatuses}>
                          {t("common.allStatuses")}
                        </SelectItem>
                        {resourceStatuses.map((value) => (
                          <SelectItem key={value} value={value}>
                            {label(value)}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    <div className="w-full sm:w-72">
                      <ResourceKindPicker
                        label={t("common.resourceKind")}
                        values={resourceKindIDs}
                        options={resourceKindOptions}
                        allLabel={t("assets.allResourceKinds")}
                        selectedCountLabel={(count) =>
                          t("common.selectedResourceKindCount", { count })
                        }
                        selectedListLabel={t("common.selectedResourceKinds")}
                        removeLabel={(name) =>
                          t("common.removeResourceKind", { name })
                        }
                        searchPlaceholder={t("assets.searchResourceKind")}
                        emptyLabel={t("assets.noResourceKinds")}
                        onValuesChange={setResourceKindIDs}
                      />
                    </div>
                  </div>
                </>
              }
              pagination={
                filteredRows.length > 0 ? (
                  <CursorPagination
                    page={page}
                    pageCount={pageCount}
                    hasNextPage={page < pageCount}
                    pending={false}
                    pageSize={pageSize}
                    onPrevious={() =>
                      setPage((current) => Math.max(1, current - 1))
                    }
                    onNext={() =>
                      setPage((current) => Math.min(pageCount, current + 1))
                    }
                    onPageSelect={setPage}
                    onPageSizeChange={(value) => {
                      setPageSize(value);
                      setPage(1);
                    }}
                    labels={{
                      page: (value) => t("common.page", { page: value }),
                      pageSize: t("common.pageSize"),
                      previous: t("common.previous"),
                      next: t("common.next"),
                    }}
                  />
                ) : undefined
              }
            >
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("cleanup.resource")}</TableHead>
                    <TableHead>{t("common.resourceKind")}</TableHead>
                    <TableHead className="hidden md:table-cell">
                      {t("panorama.resourceRegion")}
                    </TableHead>
                    <TableHead>{t("cleanup.action")}</TableHead>
                    <TableHead>{t("common.status")}</TableHead>
                    <TableHead>{t("cleanup.result")}</TableHead>
                    <TableHead>{t("common.updated")}</TableHead>
                    <TableHead className="w-24">
                      <span className="sr-only">{t("common.actions")}</span>
                    </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {visibleRows.map((row) => {
                    const asset = byID.get(row.assetID);
                    const displayAction = row.action;
                    const displayActionLabel = cleanupActionDisplayLabel(
                      row,
                      byID,
                      resourceKindsByID,
                      locale,
                      label,
                      t,
                    );
                    const managementExplanation =
                      cleanupManagedResourceExplanation(
                        row,
                        byID,
                        resourceKindsByID,
                        locale,
                        t,
                      );
                    const resourceMarker = resolveResourceMarker(asset, {
                      lifecycleKinds: [row.lifecycleKind],
                      managed: Boolean(managementExplanation),
                    });
                    const statusReason = cleanupResourceStatusReason(
                      row,
                      byID,
                      resourceKindsByID,
                      locale,
                      formatDate,
                      t,
                    );
                    const transitRouterID =
                      displayAction === "delete_with_transit_router"
                        ? cleanupTransitRouterNativeID(row, asset, byID)
                        : "";
                    const actionExplanation =
                      cleanupActionExplanation(
                        row,
                        asset,
                        byID,
                        transitRouterID,
                        t,
                      ) ||
                      (displayAction === "delegated_delete"
                        ? statusReason
                        : undefined);
                    const kind = asset
                      ? resourceKindsByID.get(asset.resource_kind_id)
                      : undefined;
                    const typeName = asset
                      ? resourceTypeName(
                          kind?.native_type || asset.identity.native_type,
                          kind?.display_name || asset.identity.native_type,
                          kind?.display_names,
                          locale,
                        )
                      : undefined;
                    return (
                      <TableRow key={row.key}>
                        <TableCell className="max-w-0 sm:max-w-none">
                          <ResourceIdentity
                            asset={asset}
                            loading={assets.isPending}
                            managementExplanation={managementExplanation}
                            resourceMarker={resourceMarker}
                            t={t}
                          />
                        </TableCell>
                        <TableCell>{typeName || "—"}</TableCell>
                        <TableCell className="hidden md:table-cell">
                          {cleanupAssetRegionLabel(
                            asset,
                            connectionRegions,
                            t("common.global"),
                          )}
                        </TableCell>
                        <TableCell>
                          <CleanupActionLabel
                            action={displayActionLabel}
                            explanation={actionExplanation}
                            t={t}
                          />
                        </TableCell>
                        <TableCell>
                          <StateBadge
                            value={row.status}
                            label={label(row.status)}
                          />
                        </TableCell>
                        <TableCell className="max-w-md whitespace-normal break-words text-xs leading-5 text-muted-foreground">
                          {statusReason && (
                            <span className="block break-words">
                              {statusReason}
                            </span>
                          )}
                          {row.blockers?.map((blocker, index) => (
                            <span
                              key={`${blocker.code}-${index}`}
                              className="block break-words"
                            >
                              {blockerMessage(blocker, messageForCode, t)}
                            </span>
                          ))}
                          {row.warning && (
                            <span className="block break-words">
                              {unsupportedCleanupReason(
                                asset,
                                row.warning,
                                messageForCode,
                                t,
                              )}
                            </span>
                          )}
                          {!statusReason &&
                            !row.blockers?.length &&
                            !row.warning &&
                            (row.status === "ignored_dirty"
                              ? t("cleanup.dirtyIgnoredSummary")
                              : "—")}
                        </TableCell>
                        <TableCell className="text-xs text-muted-foreground">
                          {row.updatedAt ? formatDate(row.updatedAt) : "—"}
                        </TableCell>
                        <TableCell className="w-24 px-1 sm:px-2">
                          <ResourceNavigationActions
                            asset={asset}
                            kind={kind}
                            resourceID={
                              asset?.identity.native_id || row.assetID
                            }
                            onViewLogs={() => {
                              setLogResourceID(
                                asset?.identity.native_id || row.assetID,
                              );
                              changeTab("logs");
                            }}
                            t={t}
                          />
                        </TableCell>
                      </TableRow>
                    );
                  })}
                  {visibleRows.length === 0 && (
                    <TableRow>
                      <TableCell
                        colSpan={8}
                        className="h-28 text-center text-sm text-muted-foreground"
                      >
                        {t("cleanup.noResourceResults")}
                      </TableCell>
                    </TableRow>
                  )}
                </TableBody>
              </Table>
            </DataTableShell>
          </TabsContent>

          <TabsContent value="logs">
            <CleanupTaskEvents
              connectionID={connection.id}
              taskID={aggregate.task.id}
              live={liveLogs}
              assetsByID={byID}
              resourceIDFilter={logResourceID}
              onResourceIDFilterChange={setLogResourceID}
            />
          </TabsContent>

          <TabsContent value="review">
            <ul className="divide-y">
              {aggregate.task.selectors.map((selector, selectorIndex) => {
                const asset =
                  selector.kind === "asset"
                    ? byID.get(selector.asset_id)
                    : undefined;
                const name = cleanupTargetName(
                  selector,
                  asset,
                  assets.isPending,
                  t,
                );
                const targetAssetIDs = cleanupTargetAssetIDs(
                  aggregate.task,
                  selector,
                  selectorIndex,
                );
                return (
                  <li key={selectorKey(selector)}>
                    <button
                      type="button"
                      className="flex min-h-16 w-full cursor-pointer items-center gap-3 px-3 py-3 text-left transition-colors hover:bg-muted/55 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset"
                      aria-label={t("cleanup.viewTargetResources", { name })}
                      onClick={() =>
                        inspectCleanupTarget(selector, selectorIndex)
                      }
                    >
                      <span className="min-w-0 flex-1">
                        <span className="flex min-w-0 items-center gap-2">
                          <Badge
                            variant="secondary"
                            className="h-5 rounded-md px-1.5 py-0 text-[10px] leading-none font-medium"
                          >
                            {cleanupTargetKindLabel(selector, t)}
                          </Badge>
                          <strong className="min-w-0 truncate text-sm font-medium">
                            {name}
                          </strong>
                        </span>
                        <span className="mt-0.5 block truncate text-xs text-muted-foreground">
                          {t("cleanup.targetResourceCount", {
                            count: formatNumber(targetAssetIDs.length),
                          })}
                        </span>
                      </span>
                      <ChevronRight
                        aria-hidden="true"
                        className="size-4 shrink-0 text-muted-foreground"
                      />
                    </button>
                  </li>
                );
              })}
            </ul>
          </TabsContent>
        </Tabs>

        <Dialog
          open={Boolean(targetDialog)}
          onOpenChange={(open) => !open && setTargetDialog(null)}
        >
          {targetDialog && (
            <DialogContent className="max-h-[min(44rem,calc(100dvh-2rem))] grid-rows-[auto_minmax(0,1fr)] gap-0 p-0 sm:max-w-3xl">
              <DialogHeader className="border-b px-6 py-5 pr-12">
                <DialogTitle>{targetDialog.name}</DialogTitle>
                <DialogDescription>
                  {t("cleanup.targetResourceCount", {
                    count: formatNumber(targetDialog.assetIDs.length),
                  })}
                </DialogDescription>
              </DialogHeader>
              <div className="min-h-0 overflow-y-auto">
                <CleanupTargetResources
                  connectionID={connection.id}
                  cleanupTaskID={aggregate.task.id}
                  selector={targetDialog.selector}
                  assetIDs={targetDialog.assetIDs}
                  initialAssets={targetDialog.initialAssets}
                  initialAssetsReady={targetDialog.initialAssetsReady}
                  regions={connectionRegions}
                  onNavigate={() => setTargetDialog(null)}
                />
              </div>
            </DialogContent>
          )}
        </Dialog>

        <AlertDialog
          open={dependencyAssetIDs.length > 0}
          onOpenChange={(open) => {
            if (open || addDependencies.isPending) return;
            setDependencyAssetIDs([]);
            addDependencies.reset();
          }}
        >
          <AlertDialogContent className="sm:max-w-xl">
            <AlertDialogHeader>
              <AlertDialogTitle>
                {t("cleanup.addDependenciesTitle")}
              </AlertDialogTitle>
              <AlertDialogDescription>
                {t("cleanup.addDependenciesDetail", {
                  count: formatNumber(dependencyAssetIDs.length),
                })}
              </AlertDialogDescription>
              {addDependencies.error && (
                <div className="text-sm text-destructive" role="alert">
                  {formatError(addDependencies.error)}
                </div>
              )}
            </AlertDialogHeader>
            <div
              className="space-y-2"
              aria-labelledby="cleanup-dependency-targets-title"
            >
              <p
                id="cleanup-dependency-targets-title"
                className="text-sm font-medium"
              >
                {t("cleanup.dependenciesToAdd")}
              </p>
              <ul className="max-h-64 divide-y overflow-y-auto rounded-lg border bg-muted/20">
                {dependencyTargets.map((target) => (
                  <li
                    key={target.assetID}
                    className="grid gap-1 px-3 py-2.5 sm:grid-cols-[minmax(0,1fr)_minmax(8rem,40%)] sm:items-center sm:gap-4"
                  >
                    <span className="block min-w-0">
                      <strong
                        className="block truncate text-sm font-medium"
                        title={target.name}
                      >
                        {target.name}
                      </strong>
                      {target.nativeID !== target.name && (
                        <span
                          className="block truncate font-mono text-xs text-muted-foreground"
                          title={target.nativeID}
                        >
                          {target.nativeID}
                        </span>
                      )}
                    </span>
                    {target.typeName && (
                      <span
                        className="truncate text-xs text-muted-foreground sm:text-right"
                        title={target.typeName}
                      >
                        {target.typeName}
                      </span>
                    )}
                  </li>
                ))}
              </ul>
            </div>
            <AlertDialogFooter>
              <AlertDialogCancel disabled={addDependencies.isPending}>
                {t("common.cancel")}
              </AlertDialogCancel>
              <Button
                type="button"
                disabled={addDependencies.isPending}
                onClick={() => addDependencies.mutate(dependencyAssetIDs)}
              >
                {addDependencies.isPending
                  ? t("cleanup.addDependenciesUpdating")
                  : t("cleanup.addDependenciesConfirm")}
              </Button>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>

        <ExecutionConfirmation
          open={confirmationOpen}
          onOpenChange={setConfirmationOpen}
          typedNames={typedNames}
          typedConfirmations={typedConfirmations}
          setTypedConfirmations={setTypedConfirmations}
          needsExplicitConfirmation={needsExplicitConfirmation}
          explicitConfirmation={explicitConfirmation}
          setExplicitConfirmation={setExplicitConfirmation}
          concurrency={executionConcurrency}
          setConcurrency={setExecutionConcurrency}
          satisfied={
            confirmationSatisfied &&
            parseExecutionConcurrency(executionConcurrency) !== null
          }
          pending={execute.isPending}
          onExecute={() => execute.mutate()}
          t={t}
        />
        <AlertDialog
          open={continueConfirmationOpen}
          onOpenChange={setContinueConfirmationOpen}
        >
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>{t("cleanup.continueTitle")}</AlertDialogTitle>
              <AlertDialogDescription>
                {t("cleanup.continueDetail")}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <ExecutionConcurrencyField
              id="continue-execution-concurrency"
              value={continueConcurrency}
              onChange={setContinueConcurrency}
              t={t}
            />
            <AlertDialogFooter>
              <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
              <AlertDialogAction
                disabled={
                  continueExecution.isPending ||
                  parseExecutionConcurrency(continueConcurrency) === null
                }
                onClick={() => continueExecution.mutate()}
              >
                {continueExecution.isPending
                  ? t("cleanup.continuing")
                  : t("cleanup.continue")}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </PageLayout>
    </>
  );
}

function ResourceNavigationActions({
  asset,
  kind,
  includeClosed = false,
  resourceID,
  onViewLogs,
  t,
}: {
  asset?: Asset;
  kind?: ResourceKind;
  includeClosed?: boolean;
  resourceID?: string;
  onViewLogs?: () => void;
  t: any;
}) {
  const name =
    asset?.name?.trim() || asset?.identity.native_id || resourceID || "—";
  const provider = asset?.identity.provider;
  const canNavigate = Boolean(asset && (includeClosed || !asset.closed_at));
  const consoleURL =
    asset && canNavigate
      ? cloudConsoleURL({
          provider: asset.identity.provider,
          nativeType: asset.identity.native_type,
          nativeId: asset.identity.native_id,
          regionId: assetConsoleRegionID(asset.location),
          consoleLinkTemplate: kind?.console_link_template,
          templateValues: asset.normalized,
        })
      : undefined;

  if (!canNavigate && !onViewLogs) return null;

  return (
    <div className="flex items-center justify-end gap-1">
      {asset && canNavigate && (
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              asChild
              variant="ghost"
              size="icon-sm"
              className="size-11 text-muted-foreground hover:text-foreground sm:size-8"
            >
              <Link
                to={panoramaResourcePath(asset.id)}
                aria-label={`${t("asset.viewInPanorama")}: ${name}`}
              >
                <LocateFixed aria-hidden="true" />
              </Link>
            </Button>
          </TooltipTrigger>
          <TooltipContent>{t("asset.viewInPanorama")}</TooltipContent>
        </Tooltip>
      )}
      {asset && consoleURL && (
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              asChild
              variant="ghost"
              size="icon-sm"
              className="size-11 text-muted-foreground hover:text-foreground sm:size-8"
            >
              <a
                href={consoleURL}
                target="_blank"
                rel="noopener noreferrer"
                aria-label={`${t("panorama.openConsole")}: ${name}`}
              >
                <CloudProviderIcon
                  provider={provider}
                  consoleURL={consoleURL}
                />
              </a>
            </Button>
          </TooltipTrigger>
          <TooltipContent>{t("panorama.openConsole")}</TooltipContent>
        </Tooltip>
      )}
      {onViewLogs && (
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              className="size-11 text-muted-foreground hover:text-foreground sm:size-8"
              aria-label={`${t("cleanup.viewResourceLogs")}: ${name}`}
              onClick={onViewLogs}
            >
              <FileText aria-hidden="true" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{t("cleanup.viewResourceLogs")}</TooltipContent>
        </Tooltip>
      )}
    </div>
  );
}

function CleanupTargetResources({
  connectionID,
  cleanupTaskID,
  selector,
  assetIDs,
  initialAssets,
  initialAssetsReady,
  regions,
  onNavigate,
}: {
  connectionID: string;
  cleanupTaskID: string;
  selector: CleanupSelector;
  assetIDs: string[];
  initialAssets: Asset[];
  initialAssetsReady: boolean;
  regions: ConnectionRegion[];
  onNavigate: () => void;
}) {
  const { formatError, locale, t } = useLocale();
  const [search, setSearch] = useState("");
  const [resourceKindIDs, setResourceKindIDs] = useState<string[]>([]);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState<PageSize>(DEFAULT_PAGE_SIZE);
  const targetAssets = useQuery({
    queryKey: [
      "cleanup-target-assets",
      connectionID,
      cleanupTaskID,
      selectorKey(selector),
    ],
    queryFn: () => findAssets(connectionID, assetIDs),
    enabled: assetIDs.length > 0,
    initialData: initialAssetsReady ? initialAssets : undefined,
    staleTime: initialAssetsReady ? Number.POSITIVE_INFINITY : 0,
  });
  const catalog = useQuery({
    queryKey: ["catalog"],
    queryFn: listProviderCatalog,
  });
  const assetsByID = useMemo(
    () => new Map((targetAssets.data ?? []).map((asset) => [asset.id, asset])),
    [targetAssets.data],
  );
  const kindsByID = useMemo(
    () =>
      new Map(
        (catalog.data ?? []).flatMap((bundle) =>
          bundle.kinds.map((kind) => [kind.id, kind] as const),
        ),
      ),
    [catalog.data],
  );
  const kindOptions = useMemo(() => {
    const available = new Map(
      (targetAssets.data ?? []).map((asset) => [
        asset.resource_kind_id,
        {
          id: asset.resource_kind_id,
          nativeType: asset.identity.native_type,
          kind: kindsByID.get(asset.resource_kind_id),
        },
      ]),
    );
    return [...available.values()]
      .map(({ id, nativeType, kind }) => {
        const product = resourceProductName(kind?.native_type || nativeType);
        return {
          value: id,
          label: resourceTypeName(
            kind?.native_type || nativeType,
            kind?.display_name || nativeType,
            kind?.display_names,
            locale,
          ),
          tag: product === "—" ? undefined : product,
          keywords: [
            product,
            nativeType,
            ...Object.values(kind?.display_names ?? {}),
          ],
        };
      })
      .sort((left, right) =>
        compareResourceKindOptionsByProduct(left, right, locale),
      );
  }, [kindsByID, locale, targetAssets.data]);
  const rows = useMemo(() => {
    const normalized = search.trim().toLowerCase();
    return assetIDs
      .map((assetID) => ({ assetID, asset: assetsByID.get(assetID) }))
      .filter(({ assetID, asset }) => {
        if (
          resourceKindIDs.length > 0 &&
          (!asset || !resourceKindIDs.includes(asset.resource_kind_id))
        ) {
          return false;
        }
        if (!normalized) return true;
        const kind = asset ? kindsByID.get(asset.resource_kind_id) : undefined;
        const typeName = asset
          ? resourceTypeName(
              kind?.native_type || asset.identity.native_type,
              kind?.display_name || asset.identity.native_type,
              kind?.display_names,
              locale,
            )
          : "";
        return [
          assetID,
          asset?.name,
          asset?.identity.native_id,
          asset?.identity.native_type,
          typeName,
          cleanupAssetRegionLabel(asset, regions, t("common.global")),
        ].some((value) => value?.toLowerCase().includes(normalized));
      })
      .sort((left, right) => {
        const leftName =
          left.asset?.name?.trim() ||
          left.asset?.identity.native_id ||
          left.assetID;
        const rightName =
          right.asset?.name?.trim() ||
          right.asset?.identity.native_id ||
          right.assetID;
        return (
          leftName.localeCompare(rightName) ||
          left.assetID.localeCompare(right.assetID)
        );
      });
  }, [
    assetIDs,
    assetsByID,
    kindsByID,
    locale,
    regions,
    resourceKindIDs,
    search,
    t,
  ]);
  const pageCount = Math.max(1, Math.ceil(rows.length / pageSize));
  const visibleRows = rows.slice((page - 1) * pageSize, page * pageSize);
  const pending = assetIDs.length > 0 && targetAssets.isPending;
  useEffect(() => setPage(1), [resourceKindIDs, search]);
  useEffect(() => {
    if (page > pageCount) setPage(pageCount);
  }, [page, pageCount]);

  return (
    <div>
      <DataTableShell
        toolbar={
          <div className="flex w-full min-w-0 items-center gap-2">
            <div className="relative min-w-0 flex-1">
              <Search
                aria-hidden="true"
                className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground"
              />
              <Input
                className="h-11 pl-9 sm:h-9"
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                placeholder={t("cleanup.searchTargetResources")}
                aria-label={t("cleanup.searchTargetResources")}
              />
            </div>
            <div className="w-2/5 min-w-0 shrink-0 sm:w-64">
              <ResourceKindPicker
                label={t("common.resourceKind")}
                values={resourceKindIDs}
                options={kindOptions}
                allLabel={t("assets.allResourceKinds")}
                selectedCountLabel={(count) =>
                  t("common.selectedResourceKindCount", { count })
                }
                selectedListLabel={t("common.selectedResourceKinds")}
                removeLabel={(name) => t("common.removeResourceKind", { name })}
                searchPlaceholder={t("assets.searchResourceKind")}
                emptyLabel={t("assets.noResourceKinds")}
                onValuesChange={setResourceKindIDs}
              />
            </div>
          </div>
        }
        pagination={
          rows.length > 0 ? (
            <CursorPagination
              page={page}
              pageCount={pageCount}
              hasNextPage={page < pageCount}
              pending={targetAssets.isFetching}
              pageSize={pageSize}
              onPrevious={() => setPage((current) => Math.max(1, current - 1))}
              onNext={() =>
                setPage((current) => Math.min(pageCount, current + 1))
              }
              onPageSelect={setPage}
              onPageSizeChange={(value) => {
                setPageSize(value);
                setPage(1);
              }}
              labels={{
                page: (value) => t("common.page", { page: value }),
                pageSize: t("common.pageSize"),
                previous: t("common.previous"),
                next: t("common.next"),
              }}
            />
          ) : undefined
        }
      >
        <AsyncState
          pending={pending}
          error={targetAssets.error}
          empty={!pending && rows.length === 0}
          onRetry={() => void targetAssets.refetch()}
          formatError={formatError}
          labels={{
            empty: t("cleanup.noTargetResources"),
            retry: t("shell.retry"),
            failed: t("shell.routeFailure"),
            loading: t("cleanup.loadingTargetResources"),
          }}
        >
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("cleanup.resource")}</TableHead>
                <TableHead>{t("common.resourceKind")}</TableHead>
                <TableHead className="hidden md:table-cell">
                  {t("panorama.resourceRegion")}
                </TableHead>
                <TableHead className="w-24">
                  <span className="sr-only">{t("common.actions")}</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {visibleRows.map(({ assetID, asset }) => {
                const name =
                  asset?.name?.trim() || asset?.identity.native_id || assetID;
                const kind = asset
                  ? kindsByID.get(asset.resource_kind_id)
                  : undefined;
                const typeName = asset
                  ? resourceTypeName(
                      kind?.native_type || asset.identity.native_type,
                      kind?.display_name || asset.identity.native_type,
                      kind?.display_names,
                      locale,
                    )
                  : undefined;
                return (
                  <TableRow key={assetID}>
                    <TableCell className="max-w-0 sm:max-w-none">
                      <span className="block min-w-0">
                        <Link
                          to={`/assets/${encodeURIComponent(assetID)}`}
                          className="block truncate font-medium text-info underline-offset-4 hover:underline focus-visible:rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                          onClick={onNavigate}
                        >
                          {name}
                        </Link>
                        <span className="mt-0.5 block truncate font-mono text-[11px] text-muted-foreground">
                          {asset?.identity.native_id || assetID}
                        </span>
                      </span>
                    </TableCell>
                    <TableCell>{typeName || "—"}</TableCell>
                    <TableCell className="hidden md:table-cell">
                      {cleanupAssetRegionLabel(
                        asset,
                        regions,
                        t("common.global"),
                      )}
                    </TableCell>
                    <TableCell className="w-24 px-1 sm:px-2">
                      <ResourceNavigationActions
                        asset={asset}
                        kind={kind}
                        includeClosed
                        t={t}
                      />
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </AsyncState>
      </DataTableShell>
    </div>
  );
}

function cleanupAssetRegionLabel(
  asset: Asset | undefined,
  regions: ConnectionRegion[],
  globalLabel: string,
) {
  if (!asset) return "—";
  return assetRegionLabel(
    asset.location,
    resolveAssetRegion(asset.location, regions),
    globalLabel,
  );
}

function cleanupTargetAssetIDs(
  cleanupTask: CleanupTask,
  selector: CleanupSelector,
  selectorIndex: number,
) {
  const resolved = cleanupTask.selector_asset_ids?.[selectorIndex];
  if (resolved) return [...new Set(resolved)];
  if (selector.kind === "asset") return [selector.asset_id];
  return [...new Set(cleanupTask.resolved_asset_ids)];
}

function cleanupTargetKindLabel(selector: CleanupSelector, t: any) {
  switch (selector.kind) {
    case "connection":
      return t("cleanup.selectorKindConnection");
    case "scope":
      return selector.scope_kind === "region"
        ? t("panorama.cleanupListRegion")
        : t("cleanup.selectorKindScope");
    case "group":
      return t("panorama.cleanupListVpc");
    case "asset":
      return t("panorama.cleanupListResource");
  }
}

interface ResourceRow {
  key: string;
  assetID: string;
  action: string;
  status: string;
  statusReason?: string;
  readbackState?: string;
  readbackExists?: boolean;
  scheduledDeletionAt?: string;
  skipReason?: string;
  updatedAt?: string;
  controllerID?: string;
  lifecycleKind?: string;
  providerErrorCode?: string;
  verificationStartedAt?: string;
  verificationDeadline?: string;
  verificationTimeoutSeconds?: number;
  managedVerification?: boolean;
  controllerDeleted?: boolean;
  dependencyAssetIDs?: string[];
  blockers?: CleanupBlocker[];
  warning?: CleanupWarning;
}

function ResourceIdentity({
  asset,
  loading,
  managementExplanation,
  resourceMarker,
  t,
}: {
  asset?: Asset;
  loading: boolean;
  managementExplanation?: string;
  resourceMarker?: ResourceMarker;
  t: any;
}) {
  if (!asset) {
    return (
      <span className="text-xs text-muted-foreground">
        {loading ? t("common.loading") : t("common.unknown")}
      </span>
    );
  }
  const name = asset.name?.trim() || asset.identity.native_id;
  return (
    <span className="block min-w-0">
      <span className="block min-w-0">
        <Link
          to={`/assets/${encodeURIComponent(asset.id)}`}
          className="block truncate font-medium text-info underline-offset-4 hover:underline focus-visible:rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {name}
        </Link>
      </span>
      <CopyableId
        label={t("cleanup.assetId")}
        value={asset.identity.native_id}
        className="mt-0.5 flex min-w-0 text-muted-foreground"
        trailing={
          resourceMarker &&
          (managementExplanation ? (
            <Tooltip>
              <TooltipTrigger asChild>
                <ResourceMarkerBadge
                  marker={resourceMarker}
                  tabIndex={0}
                  aria-label={managementExplanation}
                  className="shrink-0 cursor-help"
                />
              </TooltipTrigger>
              <TooltipContent className="max-w-xs whitespace-normal leading-5">
                {managementExplanation}
              </TooltipContent>
            </Tooltip>
          ) : (
            <ResourceMarkerBadge marker={resourceMarker} className="shrink-0" />
          ))
        }
      >
        <span className="block truncate font-mono text-[11px]">
          {asset.identity.native_id}
        </span>
      </CopyableId>
      {asset.dirty && (
        <Badge
          variant="outline"
          className="mt-1 border-destructive/35 text-destructive"
        >
          <DirtyDataIcon aria-hidden="true" className="size-3" />
          {t("asset.dirty")}
          <span aria-hidden="true">·</span>
          {t("asset.dirtyCleanupIgnored")}
        </Badge>
      )}
    </span>
  );
}

function CleanupActionLabel({
  action,
  explanation,
  t,
}: {
  action: string;
  explanation?: string;
  t: any;
}) {
  return (
    <span className="inline-flex items-center gap-1">
      <span>{action}</span>
      {explanation && (
        <Tooltip>
          <TooltipTrigger asChild>
            <button
              type="button"
              aria-label={t("cleanup.actionExplanation", { action })}
              className="inline-flex size-6 shrink-0 cursor-help items-center justify-center rounded-full text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <CircleHelp aria-hidden="true" className="size-3.5" />
            </button>
          </TooltipTrigger>
          <TooltipContent className="max-w-xs whitespace-normal leading-5">
            {explanation}
          </TooltipContent>
        </Tooltip>
      )}
    </span>
  );
}

export function cleanupResourceRows(
  steps: Array<{
    id: string;
    asset_id: string;
    kind?: string;
    action: string;
    depends_on?: string[];
    evidence?: Record<string, unknown>;
  }>,
  impacts: ImpactItem[],
  blockers: CleanupBlocker[],
  warnings: CleanupWarning[],
  actions: ActionAttempt[],
  executionStatus?: string,
  messageForCode?: (
    code: string,
    fallback: string,
    details?: Record<string, unknown>,
  ) => string,
) {
  const actionByStep = new Map(
    actions.map((action) => [action.cleanup_task_step_id, action]),
  );
  const stepByID = new Map(steps.map((step) => [step.id, step]));
  const impactByAssetID = new Map(
    impacts.map((impact) => [impact.asset_id, impact]),
  );
  const rows: ResourceRow[] = steps.map((step) => {
    const action = actionByStep.get(step.id);
    const readbackState = action?.readback?.state;
    const readbackExists =
      typeof action?.readback?.exists === "boolean"
        ? action.readback.exists
        : undefined;
    const impact = impactByAssetID.get(step.asset_id);
    const verification = step.action === "verify_managed_absent";
    const impactLifecycleKind = lifecycleKind(
      step.evidence ?? impact?.evidence,
    );
    const dependencyAssetIDs = [
      ...new Set(
        (step.depends_on ?? []).flatMap((dependencyStepID) => {
          const dependencyAction = actionByStep.get(dependencyStepID);
          if (
            dependencyAction &&
            ["succeeded", "skipped"].includes(dependencyAction.status)
          ) {
            return [];
          }
          const dependencyStep = stepByID.get(dependencyStepID);
          return dependencyStep ? [dependencyStep.asset_id] : [];
        }),
      ),
    ];
    return {
      key: `step:${step.id}`,
      assetID: step.asset_id,
      action: verification
        ? managedCleanupAction(impactLifecycleKind, impact)
        : action?.status === "skipped" &&
            ["product_unsupported", "provider_region_unavailable"].includes(
              action.skip_reason ?? "",
            )
          ? "skip"
          : step.action,
      status:
        verification &&
        readbackExists === false &&
        action?.status === "succeeded"
          ? "succeeded"
          : verification &&
              (readbackExists === true ||
                impact?.result === "still_present" ||
                action?.provider_error?.code === "ManagedResourceStillPresent")
            ? "still_present"
            : action?.skip_reason === "dirty_asset"
              ? "ignored_dirty"
              : dependencyAssetIDs.length > 0
                ? "waiting"
                : cleanupActionDisplayStatus(action, executionStatus),
      statusReason:
        verification &&
        (readbackExists === true ||
          action?.provider_error?.code === "ManagedResourceStillPresent")
          ? undefined
          : providerErrorReason(action?.provider_error, messageForCode),
      readbackState:
        typeof readbackState === "string" ? readbackState : undefined,
      readbackExists,
      scheduledDeletionAt: actionScheduledDeletionAt(action),
      skipReason: action?.skip_reason,
      updatedAt: action?.updated_at,
      controllerID:
        normalizedString(step.evidence, "controller_asset_id") ||
        impact?.controller_id,
      lifecycleKind: impactLifecycleKind,
      providerErrorCode: action?.provider_error?.code,
      verificationStartedAt:
        action?.deletion_check_started_at ||
        normalizedDateTime(impact?.evidence, "verification_started_at"),
      verificationDeadline: normalizedDateTime(
        impact?.evidence,
        "verification_deadline",
      ),
      verificationTimeoutSeconds:
        normalizedNumber(step.evidence, "wait_timeout_seconds") ||
        normalizedNumber(impact?.evidence, "verification_timeout_seconds") ||
        (verification ? 120 : undefined),
      managedVerification: verification,
      dependencyAssetIDs,
    };
  });
  const stepAssets = new Set(steps.map((step) => step.asset_id));
  for (const impact of impacts) {
    if (stepAssets.has(impact.asset_id)) continue;
    const impactLifecycleKind = lifecycleKind(impact.evidence);
    const delegatedAction = actionByStep.get(impact.delegated_to);
    rows.push({
      key: `impact:${impact.id}`,
      assetID: impact.asset_id,
      action: managedCleanupAction(impactLifecycleKind, impact),
      status:
        impact.result === "deleted_by_controller"
          ? "succeeded"
          : impact.result === "cleanup_failed" &&
              delegatedAction?.status !== "succeeded"
            ? "waiting"
            : impact.result || "waiting",
      controllerID: impact.controller_id,
      lifecycleKind: impactLifecycleKind,
      verificationStartedAt: normalizedDateTime(
        impact.evidence,
        "verification_started_at",
      ),
      verificationDeadline: normalizedDateTime(
        impact.evidence,
        "verification_deadline",
      ),
      verificationTimeoutSeconds:
        normalizedNumber(impact.evidence, "verification_timeout_seconds") ||
        120,
      managedVerification: impact.expected === "delegated_delete",
    });
  }
  const rowByAsset = new Map(rows.map((row) => [row.assetID, row]));
  for (const blocker of blockers) {
    if (!blocker.asset_id) continue;
    const existing = rowByAsset.get(blocker.asset_id);
    if (existing) {
      existing.status = "blocked";
      existing.blockers = [...(existing.blockers ?? []), blocker];
      continue;
    }
    const row = {
      key: `blocker:${blocker.asset_id}`,
      assetID: blocker.asset_id,
      action: "not_planned",
      status: "blocked",
      controllerID: managedControllerAssetID(blocker),
      lifecycleKind: lifecycleKind(blocker.evidence),
      blockers: [blocker],
    };
    rows.push(row);
    rowByAsset.set(blocker.asset_id, row);
  }
  for (const warning of warnings) {
    if (
      [
        "pre_delete_image_visibility_change",
        "scaling_group_force_delete",
      ].includes(warning.code) &&
      warning.asset_id
    ) {
      const existing = rowByAsset.get(warning.asset_id);
      if (existing) existing.warning = warning;
      continue;
    }
    if (
      !["not_actionable", "managed_by_controller"].includes(warning.code) ||
      !warning.asset_id
    ) {
      continue;
    }
    const managedSkip = warning.code === "managed_by_controller";
    const existing = rowByAsset.get(warning.asset_id);
    if (existing) {
      if (existing.status !== "blocked") {
        existing.action = "skip";
        existing.status = managedSkip ? "skipped" : "not_actionable";
        existing.controllerID = warning.controller_id;
        existing.lifecycleKind = lifecycleKind(warning.evidence);
        existing.warning = warning;
      }
      continue;
    }
    const row = {
      key: `warning:${warning.asset_id}`,
      assetID: warning.asset_id,
      action: "skip",
      status: managedSkip ? "skipped" : "not_actionable",
      controllerID: warning.controller_id,
      lifecycleKind: lifecycleKind(warning.evidence),
      warning,
    };
    rows.push(row);
    rowByAsset.set(warning.asset_id, row);
  }
  return rows;
}

function projectManagedResourceRows(
  rows: ResourceRow[],
  byID: ReadonlyMap<string, Asset>,
) {
  const rowByAssetID = new Map(rows.map((row) => [row.assetID, row]));
  return rows.map((row) => {
    const value = byID.get(row.assetID);
    if (
      row.skipReason === "delegated_to_transit_router" &&
      isCENSystemRouteTable(value)
    ) {
      const transitRouterID =
        normalizedString(value?.normalized, "transitRouterId") ||
        normalizedString(value?.normalized, "configuration.TransitRouterId");
      const controller = [...byID.values()].find(
        (candidate) =>
          candidate.identity.native_type === "ACS::CEN::TransitRouter" &&
          candidate.identity.native_id === transitRouterID,
      );
      const controllerRow = controller
        ? rowByAssetID.get(controller.id)
        : undefined;
      return {
        ...row,
        action: "delete_with_transit_router",
        status:
          controllerRow?.status === "succeeded" ? "succeeded" : row.status,
        statusReason: undefined,
        controllerID: controller?.id,
        controllerDeleted: controllerRow?.status === "succeeded",
        lifecycleKind: "cen_transit_router_system_route_table",
        managedVerification: false,
      };
    }
    if (
      value?.identity.native_type !== "ACS::ECS::SecurityGroup" ||
      !normalizedBoolean(value.normalized, "_service_managed") ||
      row.lifecycleKind === "nat_service_managed_security_group"
    ) {
      return projectManagedVerificationRow(row, rowByAssetID);
    }
    const managedENIRow = rows.find((candidateRow) => {
      if (
        candidateRow.lifecycleKind !== "nat_service_managed_eni" ||
        !candidateRow.controllerID
      ) {
        return false;
      }
      const networkInterface = byID.get(candidateRow.assetID);
      return (
        networkInterface?.identity.native_type ===
          "ACS::ECS::NetworkInterface" &&
        normalizedStringValues(
          networkInterface.normalized,
          "security_group_ids",
        ).includes(value.identity.native_id)
      );
    });
    if (!managedENIRow?.controllerID) {
      return projectManagedVerificationRow(row, rowByAssetID);
    }
    const controller = byID.get(managedENIRow.controllerID);
    if (controller?.identity.native_type !== "ACS::NAT::NatGateway") {
      return projectManagedVerificationRow(row, rowByAssetID);
    }
    const controllerRow = rowByAssetID.get(controller.id);
    const lastCheckConfirmedPresence =
      row.readbackExists === true ||
      row.status === "still_present" ||
      row.providerErrorCode ===
        "InvalidOperation.ResourceManagedByCloudProduct";
    return projectManagedVerificationRow(
      {
        ...row,
        action: "delete_with_nat_gateway",
        status:
          row.readbackExists === false && row.status === "succeeded"
            ? "succeeded"
            : controllerRow?.status === "succeeded" &&
                lastCheckConfirmedPresence
              ? "still_present"
              : "waiting",
        statusReason: undefined,
        skipReason: undefined,
        controllerID: controller.id,
        lifecycleKind: "nat_service_managed_security_group",
        managedVerification: true,
        verificationStartedAt: row.verificationStartedAt || row.updatedAt,
        verificationTimeoutSeconds: row.verificationTimeoutSeconds || 120,
      },
      rowByAssetID,
    );
  });
}

function projectManagedVerificationRow(
  row: ResourceRow,
  rowByAssetID: ReadonlyMap<string, ResourceRow>,
) {
  if (!row.managedVerification || !row.controllerID) return row;
  const controllerRow = rowByAssetID.get(row.controllerID);
  const controllerDeleted = controllerRow?.status === "succeeded";
  if (isSystemRouteTableLifecycle(row.lifecycleKind)) {
    return {
      ...row,
      status: controllerDeleted ? "succeeded" : row.status,
      statusReason: undefined,
      managedVerification: false,
      controllerDeleted,
    };
  }
  if (row.readbackExists === false && row.status === "succeeded") {
    return { ...row, controllerDeleted };
  }
  if (row.readbackExists === true || row.status === "still_present") {
    return {
      ...row,
      status: "still_present",
      statusReason: undefined,
      controllerDeleted,
    };
  }
  if (
    controllerDeleted &&
    ["succeeded", "deleted_by_controller", "delegated"].includes(row.status)
  ) {
    return {
      ...row,
      status: "waiting",
      statusReason: undefined,
      controllerDeleted,
    };
  }
  return { ...row, controllerDeleted };
}

function managedCleanupAction(
  lifecycle: string,
  impact: ImpactItem | undefined,
) {
  if (impact && isROSStackManagedImpact(impact)) return "delete_with_ros_stack";
  if (lifecycle === "vpc_system_route_table") return "delete_with_vpc";
  if (lifecycle === "nat_service_managed_security_group") {
    return "delete_with_nat_gateway";
  }
  if (lifecycle === "ecs_disk_delete_with_instance") {
    return "delete_with_ecs_instance";
  }
  if (
    ["cen_transit_router_system_route_table", "cen_system_route_map"].includes(
      lifecycle,
    )
  ) {
    return "delete_with_transit_router";
  }
  return "delegated_delete";
}

function normalizedBoolean(
  normalized: Record<string, unknown> | undefined,
  path: string,
) {
  const value = normalizedValue(normalized, path);
  return (
    value === true ||
    value === 1 ||
    (typeof value === "string" &&
      ["true", "1"].includes(value.trim().toLowerCase()))
  );
}

function normalizedNumber(
  normalized: Record<string, unknown> | undefined,
  path: string,
) {
  const value = normalizedValue(normalized, path);
  if (typeof value === "number" && Number.isFinite(value)) return value;
  if (typeof value === "string") {
    const parsed = Number(value);
    if (Number.isFinite(parsed)) return parsed;
  }
  return undefined;
}

function normalizedDateTime(
  normalized: Record<string, unknown> | undefined,
  path: string,
) {
  const value = normalizedValue(normalized, path);
  if (typeof value !== "string") return undefined;
  return validDateTime(value) || undefined;
}

function normalizedStringValues(
  normalized: Record<string, unknown> | undefined,
  path: string,
) {
  const value = normalizedValue(normalized, path);
  const values = Array.isArray(value) ? value : [value];
  return [
    ...new Set(
      values.flatMap((item) =>
        typeof item === "string" || typeof item === "number"
          ? [String(item).trim()]
          : [],
      ),
    ),
  ].filter(Boolean);
}

function normalizedValue(
  normalized: Record<string, unknown> | undefined,
  path: string,
) {
  let current: unknown = normalized;
  for (const segment of path.split(".")) {
    if (!current || typeof current !== "object" || Array.isArray(current)) {
      return undefined;
    }
    current = (current as Record<string, unknown>)[segment];
  }
  return current;
}

function isCENSystemRouteTable(value: Asset | undefined) {
  return (
    value?.identity.native_type === "ACS::CEN::TransitRouterRouteTable" &&
    resolveResourceMarker(value) === "system_route_table"
  );
}

function isCENSystemRouteMap(value: Asset | undefined) {
  return (
    value?.identity.native_type === "ACS::CEN::CenRouteMap" &&
    (normalizedString(value.normalized, "priority") === "5000" ||
      normalizedString(value.normalized, "configuration.Priority") === "5000")
  );
}

function isCENSystemLifecycle(value: string) {
  return [
    "cen_transit_router_system_route_table",
    "cen_system_route_map",
  ].includes(value);
}

function isSystemRouteTableLifecycle(value: string | undefined) {
  return (
    !!value &&
    [
      "vpc_system_route_table",
      "cen_transit_router_system_route_table",
    ].includes(value)
  );
}

function isROSStackManagedImpact(impact: ImpactItem) {
  return (
    impact.evidence?.evidence_source === "ros:system-tag" ||
    impact.evidence?.source === "ros:system-tag"
  );
}

function cleanupTransitRouterNativeID(
  row: ResourceRow,
  value: Asset | undefined,
  byID: Map<string, Asset>,
) {
  const controller = row.controllerID ? byID.get(row.controllerID) : undefined;
  if (controller?.identity.native_type === "ACS::CEN::TransitRouter") {
    return controller.identity.native_id;
  }
  return (
    normalizedString(value?.normalized, "transitRouterId") ||
    normalizedString(value?.normalized, "configuration.TransitRouterId")
  );
}

function cleanupActionExplanation(
  row: ResourceRow,
  value: Asset | undefined,
  byID: Map<string, Asset>,
  transitRouterID: string,
  t: any,
) {
  if (row.lifecycleKind === "vpc_system_route_table" && row.controllerID) {
    return t("cleanup.attachedToVpc", {
      id: resourceNativeID(byID.get(row.controllerID)),
    });
  }
  if (
    row.lifecycleKind === "ecs_disk_delete_with_instance" &&
    row.controllerID
  ) {
    return t("cleanup.attachedToEcsInstance", {
      id: resourceNativeID(byID.get(row.controllerID)),
    });
  }
  if (row.action === "delete_with_transit_router") {
    return transitRouterID
      ? t("cleanup.attachedToTransitRouter", { id: transitRouterID })
      : t("cleanup.attachedToTransitRouterUnknown");
  }
  if (row.action === "delete_with_nat_gateway" && row.controllerID) {
    return t("cleanup.attachedToNatGateway", {
      id: resourceNativeID(byID.get(row.controllerID)),
    });
  }
  if (row.action === "skip" && isCENSystemRouteMap(value)) {
    return t("cleanup.cenSystemRouteMapSkipped");
  }
  return undefined;
}

function cleanupActionDisplayLabel(
  row: ResourceRow,
  byID: ReadonlyMap<string, Asset>,
  resourceKindsByID: ReadonlyMap<string, ResourceKind>,
  locale: string,
  label: (value: string) => string,
  t: any,
) {
  if (row.action !== "delegated_delete") return label(row.action);
  return t("cleanup.deleteWithController", {
    controller: cleanupControllerTypeName(
      row.controllerID,
      byID,
      resourceKindsByID,
      locale,
      t,
    ),
  });
}

function cleanupControllerTypeName(
  controllerID: string | undefined,
  byID: ReadonlyMap<string, Asset>,
  resourceKindsByID: ReadonlyMap<string, ResourceKind>,
  locale: string,
  t: any,
) {
  const controller = controllerID ? byID.get(controllerID) : undefined;
  const kind = controller
    ? resourceKindsByID.get(controller.resource_kind_id)
    : undefined;
  if (
    !kind ||
    (!kind.display_name && !Object.keys(kind.display_names ?? {}).length)
  ) {
    return t("cleanup.controller");
  }
  return resourceTypeName(
    kind.native_type,
    kind.display_name || t("cleanup.controller"),
    kind.display_names,
    locale,
  );
}

function cleanupManagedResourceExplanation(
  row: ResourceRow,
  byID: ReadonlyMap<string, Asset>,
  resourceKindsByID: ReadonlyMap<string, ResourceKind>,
  locale: string,
  t: any,
) {
  if (!row.controllerID) return undefined;
  return t("cleanup.managedByResource", {
    controller: cleanupControllerTypeName(
      row.controllerID,
      byID,
      resourceKindsByID,
      locale,
      t,
    ),
    id: resourceControllerReference(row.controllerID, byID),
  });
}

function cleanupResourceStatusReason(
  row: ResourceRow,
  byID: ReadonlyMap<string, Asset>,
  resourceKindsByID: ReadonlyMap<string, ResourceKind>,
  locale: string,
  formatDate: (value: string | Date) => string,
  t: any,
) {
  if (row.statusReason) return row.statusReason;
  if (row.status === "waiting" && row.dependencyAssetIDs?.length) {
    const dependencyID = resourceIdentifier(row.dependencyAssetIDs[0], byID);
    return row.dependencyAssetIDs.length === 1
      ? t("cleanup.waitingForDependency", { id: dependencyID })
      : t("cleanup.waitingForDependencies", {
          id: dependencyID,
          count: row.dependencyAssetIDs.length,
        });
  }
  if (
    row.managedVerification &&
    row.controllerDeleted &&
    row.controllerID &&
    ["waiting", "in_progress", "still_present"].includes(row.status)
  ) {
    const timeoutSeconds = row.verificationTimeoutSeconds || 120;
    const minutes = Math.max(1, Math.ceil(timeoutSeconds / 60));
    const startedAt = row.verificationStartedAt
      ? new Date(row.verificationStartedAt).getTime()
      : Number.NaN;
    const deadline = row.verificationDeadline
      ? new Date(row.verificationDeadline).getTime()
      : Number.isFinite(startedAt)
        ? startedAt + timeoutSeconds * 1000
        : Number.NaN;
    const details = {
      controller: cleanupControllerTypeName(
        row.controllerID,
        byID,
        resourceKindsByID,
        locale,
        t,
      ),
      id: resourceIdentifier(row.controllerID, byID),
      minutes,
    };
    if (row.status === "still_present") {
      return Number.isFinite(deadline) && Date.now() >= deadline
        ? t("cleanup.managedResourceStillPresent", details)
        : t("cleanup.managedResourceChecking", details);
    }
    return t("cleanup.managedResourceVerificationPending", details);
  }
  if (row.readbackState === "scheduled_deletion") {
    const scheduledDeletionAt = validDateTime(
      row.scheduledDeletionAt ||
        normalizedString(
          byID.get(row.assetID)?.normalized,
          "configuration.DeleteDate",
        ),
    );
    if (scheduledDeletionAt) {
      return t("cleanup.scheduledDeletionAt", {
        time: formatDate(scheduledDeletionAt),
      });
    }
    return t("cleanup.scheduledDeletion");
  }
  if (row.action === "delete_with_ros_stack" && row.controllerID) {
    return t("cleanup.managedByRosStack", {
      name: resourceDisplayName(row.controllerID, byID),
    });
  }
  if (
    row.action === "delete_with_vpc" &&
    row.lifecycleKind === "vpc_system_route_table" &&
    row.controllerID
  ) {
    return t("cleanup.attachedToVpc", {
      id: resourceIdentifier(row.controllerID, byID),
    });
  }
  if (row.action === "delete_with_transit_router" && row.controllerID) {
    return t("cleanup.attachedToTransitRouter", {
      id: resourceIdentifier(row.controllerID, byID),
    });
  }
  if (row.action === "delete_with_nat_gateway" && row.controllerID) {
    return t("cleanup.attachedToNatGateway", {
      id: resourceIdentifier(row.controllerID, byID),
    });
  }
  if (row.action === "delegated_delete" && row.controllerID) {
    return t("cleanup.attachedToController", {
      controller: cleanupControllerTypeName(
        row.controllerID,
        byID,
        resourceKindsByID,
        locale,
        t,
      ),
      id: resourceIdentifier(row.controllerID, byID),
    });
  }
  return "";
}

function actionScheduledDeletionAt(action: ActionAttempt | undefined) {
  return validDateTime(
    normalizedString(action?.readback, "data.scheduled_deletion_at") ||
      normalizedString(action?.provider_result, "scheduled_deletion_at"),
  );
}

function validDateTime(value: string) {
  if (!value || !Number.isFinite(new Date(value).getTime())) return "";
  return value;
}

export function cleanupActionDisplayStatus(
  action: Pick<ActionAttempt, "status" | "skip_reason"> | undefined,
  executionStatus?: string,
) {
  if (!action) {
    if (executionStatus === "pausing" || executionStatus === "paused") {
      return executionStatus;
    }
    return executionStatus === "failed" ? "skipped" : "waiting";
  }
  if (action.skip_reason === "dirty_asset") return "ignored_dirty";
  if (
    action.skip_reason === "product_unsupported" ||
    action.skip_reason === "provider_region_unavailable"
  ) {
    return "not_actionable";
  }
  if (["succeeded", "failed", "skipped"].includes(action.status)) {
    return action.status;
  }
  if (executionStatus === "pausing" || executionStatus === "paused") {
    return executionStatus;
  }
  if (executionStatus === "canceled") {
    return "failed";
  }
  if (action.status === "pending") return "waiting";
  if (
    [
      "intent_persisted",
      "invoking",
      "waiting",
      "reading_back",
      "reconciling",
    ].includes(action.status)
  ) {
    return "in_progress";
  }
  return action.status;
}

export function cleanupExecutionBlockedByFailure(
  steps: Array<{ id: string; depends_on?: string[] }>,
  actions: Array<Pick<ActionAttempt, "cleanup_task_step_id" | "status">>,
) {
  const actionByStep = new Map(
    actions.map((action) => [action.cleanup_task_step_id, action]),
  );
  const stepByID = new Map(steps.map((step) => [step.id, step]));
  const hasFailure = actions.some((action) => action.status === "failed");
  if (!hasFailure) return false;

  const memo = new Map<string, boolean>();
  const visiting = new Set<string>();
  const blockedByFailure = (stepID: string): boolean => {
    const cached = memo.get(stepID);
    if (cached !== undefined) return cached;
    if (visiting.has(stepID)) return false;
    visiting.add(stepID);
    const step = stepByID.get(stepID);
    const blocked = (step?.depends_on ?? []).some((dependencyID) => {
      const dependency = actionByStep.get(dependencyID);
      return dependency?.status === "failed" || blockedByFailure(dependencyID);
    });
    visiting.delete(stepID);
    memo.set(stepID, blocked);
    return blocked;
  };

  return steps.every((step) => {
    const status = actionByStep.get(step.id)?.status;
    return (
      status === "succeeded" ||
      status === "skipped" ||
      status === "failed" ||
      blockedByFailure(step.id)
    );
  });
}

function providerErrorReason(
  providerError: ActionAttempt["provider_error"] | undefined,
  messageForCode?: (
    code: string,
    fallback: string,
    details?: Record<string, unknown>,
  ) => string,
) {
  if (!providerError) return "";
  const code = providerError.code?.trim() ?? "";
  const message = providerError.message?.trim() ?? "";
  if (!code) return message;
  const localized = messageForCode?.(code, message, providerError.summary);
  if (localized && localized !== message) return localized;
  if (!message) return code;
  if (message.toLocaleLowerCase().includes(code.toLocaleLowerCase())) {
    return message;
  }
  return `${code}: ${message}`;
}

function unsupportedCleanupReason(
  value: Asset | undefined,
  warning: CleanupWarning,
  messageForCode: (
    code: string,
    fallback: string,
    evidence?: Record<string, unknown>,
  ) => string,
  t: any,
) {
  switch (value?.identity.native_type) {
    case "ACS::CEN::CenRouteMap":
      return isCENSystemRouteMap(value)
        ? t("cleanup.cenSystemRouteMapSkipped")
        : t("cleanup.cenCleanupNowSupported");
    case "ACS::CEN::TransitRouterCidr":
      return t("cleanup.cenCleanupNowSupported");
    case "ACS::CEN::TransitRouterPeerAttachment":
      return t("cleanup.cenCleanupNowSupported");
    case "ACS::CEN::TransitRouterRouteTable":
      if (
        normalizedString(value.normalized, "routeTableType") === "System" ||
        normalizedString(value.normalized, "configuration.RouteTableType") ===
          "System"
      ) {
        return t("cleanup.cenSystemRouteTableManaged");
      }
      break;
  }
  return messageForCode(warning.code, warning.message, warning.evidence);
}

function normalizedString(
  normalized: Record<string, unknown> | undefined,
  path: string,
) {
  let current: unknown = normalized;
  for (const segment of path.split(".")) {
    if (!current || typeof current !== "object" || Array.isArray(current)) {
      return "";
    }
    current = (current as Record<string, unknown>)[segment];
  }
  return typeof current === "string" || typeof current === "number"
    ? String(current).trim()
    : "";
}

function resourceName(value: Asset | undefined) {
  return value?.name?.trim() || value?.identity.native_id || "—";
}

function resourceNativeID(value: Asset | undefined) {
  return value?.identity.native_id || "—";
}

function resourceIdentifier(assetID: string, byID: ReadonlyMap<string, Asset>) {
  return byID.get(assetID)?.identity.native_id || assetID;
}

function resourceDisplayName(
  assetID: string,
  byID: ReadonlyMap<string, Asset>,
) {
  const value = byID.get(assetID);
  return value?.name?.trim() || value?.identity.native_id || assetID;
}

function resourceControllerReference(
  assetID: string,
  byID: ReadonlyMap<string, Asset>,
) {
  const value = byID.get(assetID);
  const name = value?.name?.trim();
  const nativeID = value?.identity.native_id;
  if (name && nativeID && name !== nativeID) return `${name} (${nativeID})`;
  return nativeID || name || assetID;
}

function lifecycleKind(evidence: Record<string, unknown> | undefined) {
  return typeof evidence?.lifecycle_kind === "string"
    ? evidence.lifecycle_kind
    : "";
}

function latestExecution(values: ExecutionAttempt[]) {
  return [...values].sort((left, right) =>
    right.created_at.localeCompare(left.created_at),
  )[0];
}

function updateControlledExecutionCache(
  queryClient: QueryClient,
  connectionID: string,
  cleanupTaskID: string,
  attempt: ExecutionAttempt,
  taskStatus: string,
) {
  queryClient.setQueryData<{ items: ExecutionAttempt[] }>(
    ["cleanup-task-executions", connectionID, cleanupTaskID],
    (current) =>
      current
        ? {
            ...current,
            items: current.items.map((item) =>
              item.id === attempt.id ? attempt : item,
            ),
          }
        : { items: [attempt] },
  );
  queryClient.setQueryData<CleanupTaskAggregate>(
    ["cleanup-task", connectionID, cleanupTaskID],
    (current) =>
      current
        ? {
            ...current,
            task: { ...current.task, status: taskStatus },
          }
        : current,
  );
}

function isTerminalExecution(status: string) {
  return ["succeeded", "failed", "canceled"].includes(status);
}

function isActiveExecution(status: string) {
  return ["pending", "running", "waiting", "reconciling", "pausing"].includes(
    status,
  );
}

function pausableExecutionStatus(status: string) {
  return ["pending", "running", "waiting", "reconciling"].includes(status);
}

function isTerminalAction(status: string) {
  return ["succeeded", "skipped", "failed"].includes(status);
}

export function shouldPollCleanupActions(
  executionStatus: string | undefined,
  actions: ActionAttempt[],
) {
  if (!executionStatus) return false;
  if (executionStatus === "paused") return false;
  if (!isTerminalExecution(executionStatus)) return true;
  if (executionStatus === "canceled") return false;
  if (executionStatus === "succeeded" && actions.length === 0) return true;
  return actions.some((action) => !isTerminalAction(action.status));
}

function isFailedResource(status: string) {
  return ["failed", "cleanup_failed", "still_present"].includes(status);
}

function isCompletedResource(status: string) {
  return [
    "succeeded",
    "deleted_by_controller",
    "retained_by_policy",
    "retained_shared",
    "delegated",
    "ignored_dirty",
  ].includes(status);
}

function resourcePriority(status: string) {
  if (isFailedResource(status)) return 0;
  if (
    !isCompletedResource(status) &&
    status !== "pending" &&
    status !== "waiting"
  ) {
    return 1;
  }
  if (status === "pending" || status === "waiting") return 2;
  return 3;
}

interface CleanupStatusSegment {
  key: string;
  count: number;
  label: string;
  colorClass: string;
}

function CleanupStatusDistribution({
  title,
  totalLabel,
  total,
  segments,
}: {
  title: string;
  totalLabel: string;
  total: number;
  segments: CleanupStatusSegment[];
}) {
  const summary = segments.map((segment) => segment.label).join(", ");

  return (
    <section className="space-y-2.5">
      <div className="flex items-center justify-between gap-4 text-sm">
        <strong>{title}</strong>
        <span className="tabular-nums text-muted-foreground">{totalLabel}</span>
      </div>
      <div
        role="img"
        aria-label={summary}
        className="flex h-2.5 w-full overflow-hidden rounded-full bg-muted"
      >
        {segments
          .filter((segment) => segment.count > 0)
          .map((segment) => {
            const percentage =
              total === 0 ? 0 : Math.round((segment.count / total) * 100);
            return (
              <Tooltip key={segment.key}>
                <TooltipTrigger asChild>
                  <span
                    data-status-segment={segment.key}
                    aria-hidden="true"
                    className={`h-full min-w-0 border-r-2 border-background last:border-r-0 ${segment.colorClass}`}
                    style={{ flexGrow: segment.count, flexBasis: 0 }}
                  />
                </TooltipTrigger>
                <TooltipContent sideOffset={6}>
                  <span className="flex items-center gap-1.5">
                    <span
                      aria-hidden="true"
                      className={`size-2 rounded-[2px] ${segment.colorClass}`}
                    />
                    <span>{segment.label}</span>
                    <span aria-hidden="true">·</span>
                    <span className="tabular-nums">{percentage}%</span>
                  </span>
                </TooltipContent>
              </Tooltip>
            );
          })}
      </div>
      <div className="flex flex-wrap gap-x-5 gap-y-1 text-xs text-muted-foreground">
        {segments.map((segment) => (
          <span key={segment.key} className="inline-flex items-center gap-1.5">
            <span
              aria-hidden="true"
              className={`size-2 rounded-[2px] ${segment.colorClass}`}
            />
            <span>{segment.label}</span>
          </span>
        ))}
      </div>
    </section>
  );
}

function Fact({
  label,
  children,
  mono,
}: {
  label: string;
  children: ReactNode;
  mono?: boolean;
}) {
  return (
    <div className="grid min-w-0 grid-cols-[7rem_minmax(0,1fr)] items-start gap-4 text-sm">
      <dt className="font-medium text-muted-foreground">{label}</dt>
      <dd
        className={mono ? "truncate font-mono text-xs" : "min-w-0 tabular-nums"}
      >
        {children}
      </dd>
    </div>
  );
}

function CleanupWarnings({
  warnings,
  blockers,
  dependentAssetIDs,
  managedControllerIDs,
  managedSourceAssetIDs,
  managedTransitRouterIDs,
  onRebuildAssets,
  onAddDependencies,
  onShowResourceBlockers,
  messageForCode,
  t,
}: {
  warnings: Array<{
    code: string;
    asset_id?: string;
    controller_id?: string;
    message: string;
    evidence?: Record<string, unknown>;
  }>;
  blockers: Array<{
    code: string;
    asset_id?: string;
    controller_id?: string;
    message: string;
    evidence?: Record<string, unknown>;
  }>;
  dependentAssetIDs: string[];
  managedControllerIDs: string[];
  managedSourceAssetIDs: string[];
  managedTransitRouterIDs: string[];
  onRebuildAssets: (assetIDs: string[]) => void;
  onAddDependencies: (assetIDs: string[]) => void;
  onShowResourceBlockers: () => void;
  messageForCode: (
    code: string,
    fallback: string,
    evidence?: Record<string, unknown>,
  ) => string;
  t: any;
}) {
  const unsupportedWarnings = warnings.filter(
    (warning) => warning.code === "not_actionable",
  );
  const resourceWarnings = warnings.filter(
    (warning) => warning.code !== "not_actionable",
  );
  const hasTransitRouterWarning = warnings.some(
    (warning) =>
      warning.code === "managed_by_controller" &&
      isCENSystemLifecycle(lifecycleKind(warning.evidence)),
  );
  const taskBlockers = blockers.filter((blocker) => !blocker.asset_id);
  const resourceBlockers = blockers.filter((blocker) => blocker.asset_id);
  return (
    <>
      {warnings.length > 0 && (
        <Alert className="border-warning/30 bg-warning/10 text-warning-foreground">
          <Info />
          <AlertTitle>{t("cleanup.cleanupWarnings")}</AlertTitle>
          <AlertDescription className="gap-3">
            {unsupportedWarnings.length > 0 && (
              <p>
                {t("cleanup.unsupportedResourcesSummary", {
                  count: unsupportedWarnings.length,
                })}
              </p>
            )}
            {resourceWarnings.map((warning, index) => (
              <div key={`${warning.code}-${index}`} className="space-y-1">
                <strong>
                  {warning.code === "scan_coverage_incomplete"
                    ? t("cleanup.scanCoverageWarning")
                    : lifecycleKind(warning.evidence) === "cen_system_route_map"
                      ? t("cleanup.cenSystemRouteMapNeedsTransitRouter")
                      : messageForCode(
                          warning.code,
                          warning.message,
                          warning.evidence,
                        )}
                </strong>
                {warning.asset_id && (
                  <CopyableId
                    label={t("cleanup.assetId")}
                    value={warning.asset_id}
                    className="block font-mono text-xs"
                  />
                )}
              </div>
            ))}
            {hasTransitRouterWarning && managedTransitRouterIDs.length > 0 && (
              <Button
                type="button"
                variant="outline"
                onClick={() => onRebuildAssets(managedTransitRouterIDs)}
              >
                <ListPlus />
                {managedTransitRouterIDs.length === 1
                  ? t("cleanup.addTransitRouter")
                  : t("cleanup.addTransitRouters", {
                      count: managedTransitRouterIDs.length,
                    })}
              </Button>
            )}
          </AlertDescription>
        </Alert>
      )}
      {blockers.length > 0 && (
        <Alert className="border-warning/30 bg-warning/10 text-warning-foreground">
          <AlertTriangle />
          <AlertTitle>{t("cleanup.executionBlockers")}</AlertTitle>
          <AlertDescription className="gap-3">
            {taskBlockers.map((blocker, index) => (
              <p key={`${blocker.code}-${index}`}>
                {blockerMessage(blocker, messageForCode, t)}
              </p>
            ))}
            {(resourceBlockers.length > 0 || dependentAssetIDs.length > 0) && (
              <div className="flex flex-wrap items-center gap-3">
                {resourceBlockers.length > 0 && (
                  <>
                    <p>
                      {t("cleanup.blockerSummary", {
                        count: resourceBlockers.length,
                      })}
                    </p>
                    <Button
                      type="button"
                      variant="outline"
                      onClick={onShowResourceBlockers}
                    >
                      <LocateFixed />
                      {t("cleanup.viewBlockedResources", {
                        count: resourceBlockers.length,
                      })}
                    </Button>
                  </>
                )}
                {dependentAssetIDs.length > 0 && (
                  <Button
                    type="button"
                    variant="outline"
                    onClick={() => onAddDependencies(dependentAssetIDs)}
                  >
                    <ListPlus />
                    {t("cleanup.addDependencies", {
                      count: dependentAssetIDs.length,
                    })}
                  </Button>
                )}
              </div>
            )}
            {managedControllerIDs.length > 0 && (
              <Button
                type="button"
                variant="outline"
                onClick={() => onRebuildAssets(managedControllerIDs)}
              >
                <ListPlus />
                {managedControllerIDs.length === 1
                  ? t("cleanup.addOwningVpc")
                  : t("cleanup.addOwningVpcs", {
                      count: managedControllerIDs.length,
                    })}
              </Button>
            )}
            {managedSourceAssetIDs.length > 0 && (
              <Button
                type="button"
                variant="outline"
                onClick={() => onRebuildAssets(managedSourceAssetIDs)}
              >
                <ListPlus />
                {managedSourceAssetIDs.length === 1
                  ? t("cleanup.addSourceResource")
                  : t("cleanup.addSourceResources", {
                      count: managedSourceAssetIDs.length,
                    })}
              </Button>
            )}
            {!hasTransitRouterWarning && managedTransitRouterIDs.length > 0 && (
              <Button
                type="button"
                variant="outline"
                onClick={() => onRebuildAssets(managedTransitRouterIDs)}
              >
                <ListPlus />
                {managedTransitRouterIDs.length === 1
                  ? t("cleanup.addTransitRouter")
                  : t("cleanup.addTransitRouters", {
                      count: managedTransitRouterIDs.length,
                    })}
              </Button>
            )}
          </AlertDescription>
        </Alert>
      )}
    </>
  );
}

function blockerMessage(
  blocker: CleanupBlocker,
  messageForCode: (
    code: string,
    fallback: string,
    evidence?: Record<string, unknown>,
  ) => string,
  t: any,
) {
  if (blocker.code === "scan_coverage_incomplete") {
    return t("cleanup.scanCoverageWarning");
  }
  switch (lifecycleKind(blocker.evidence)) {
    case "vpc_system_route_table":
      return t("cleanup.systemRouteTableBlocked");
    case "cen_transit_router_vpc_attachment":
      return t("cleanup.cenVpcAttachmentBlocked");
    case "cen_transit_router_system_route_table":
    case "cen_system_route_map":
      return t("cleanup.cenSystemResourceBlocked");
    default:
      if (
        blocker.code === "cross_scope_dependency" &&
        blocker.evidence?.target_native_type === "ACS::VPN::CustomerGateway"
      ) {
        return t("cleanup.customerGatewayVpnBlocked");
      }
      return messageForCode(blocker.code, blocker.message, blocker.evidence);
  }
}

function ExecutionConfirmation({
  open,
  onOpenChange,
  typedNames,
  typedConfirmations,
  setTypedConfirmations,
  needsExplicitConfirmation,
  explicitConfirmation,
  setExplicitConfirmation,
  concurrency,
  setConcurrency,
  satisfied,
  pending,
  onExecute,
  t,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  typedNames: string[];
  typedConfirmations: Record<string, string>;
  setTypedConfirmations: React.Dispatch<
    React.SetStateAction<Record<string, string>>
  >;
  needsExplicitConfirmation: boolean;
  explicitConfirmation: boolean;
  setExplicitConfirmation: (value: boolean) => void;
  concurrency: string;
  setConcurrency: (value: string) => void;
  satisfied: boolean;
  pending: boolean;
  onExecute: () => void;
  t: any;
}) {
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("cleanup.confirmTitle")}</AlertDialogTitle>
          <AlertDialogDescription>
            {t("cleanup.confirmDetail")}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <div className="max-h-[55vh] space-y-4 overflow-y-auto py-1">
          <ExecutionConcurrencyField
            id="execution-concurrency"
            value={concurrency}
            onChange={setConcurrency}
            t={t}
          />
          {typedNames.map((name) => (
            <div className="space-y-2" key={name}>
              <Label htmlFor={`confirmation-${name}`} className="select-text">
                {t("cleanup.typeName", { name })}
              </Label>
              <Input
                id={`confirmation-${name}`}
                value={typedConfirmations[name] ?? ""}
                onChange={(event) =>
                  setTypedConfirmations((current) => ({
                    ...current,
                    [name]: event.target.value,
                  }))
                }
                placeholder={t("cleanup.typeNamePlaceholder")}
                autoComplete="off"
              />
            </div>
          ))}
          {needsExplicitConfirmation && (
            <div className="flex items-start gap-3 rounded-lg border border-destructive/30 p-3">
              <Checkbox
                id="explicit-confirmation"
                checked={explicitConfirmation}
                onCheckedChange={(checked) =>
                  setExplicitConfirmation(checked === true)
                }
              />
              <Label
                htmlFor="explicit-confirmation"
                className="leading-relaxed"
              >
                {t("cleanup.confirmCheckbox")}
              </Label>
            </div>
          )}
        </div>
        <AlertDialogFooter>
          <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={!satisfied || pending}
            onClick={onExecute}
          >
            {pending ? t("cleanup.queuing") : t("cleanup.execute")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

function ExecutionConcurrencyField({
  id,
  value,
  onChange,
  t,
}: {
  id: string;
  value: string;
  onChange: (value: string) => void;
  t: any;
}) {
  const descriptionID = `${id}-description`;
  return (
    <div className="space-y-2">
      <Label htmlFor={id}>{t("cleanup.concurrency")}</Label>
      <Input
        id={id}
        type="number"
        inputMode="numeric"
        min={1}
        max={100}
        step={1}
        value={value}
        aria-describedby={descriptionID}
        onBlur={() => {
          if (value === "") onChange(defaultExecutionConcurrency);
        }}
        onChange={(event) =>
          onChange(clampExecutionConcurrency(event.target.value))
        }
      />
      <p id={descriptionID} className="text-xs text-muted-foreground">
        {t("cleanup.concurrencyRange")}
      </p>
    </div>
  );
}

function dependentAssetID(
  evidence: Record<string, unknown> | undefined,
): string {
  return typeof evidence?.dependent_asset_id === "string"
    ? evidence.dependent_asset_id
    : "";
}

function managedControllerAssetID(
  item: Pick<CleanupBlocker, "controller_id" | "evidence">,
): string {
  if (
    ["cen_transit_router_system_route_table", "cen_system_route_map"].includes(
      lifecycleKind(item.evidence),
    )
  ) {
    return item.controller_id ?? "";
  }
  if (typeof item.evidence?.immediate_controller_id === "string") {
    return item.evidence.immediate_controller_id;
  }
  return item.controller_id ?? "";
}

function selectorName(selector: CleanupSelector) {
  return selector.display_name || selectorIdentity(selector);
}

function cleanupTargetName(
  selector: CleanupSelector,
  asset: Asset | undefined,
  loading: boolean,
  t: any,
) {
  const displayName = selector.display_name?.trim() || asset?.name?.trim();
  if (displayName) return displayName;
  if (selector.kind !== "asset") return selectorIdentity(selector);
  if (asset) return asset.identity.native_id;
  return loading ? t("common.loading") : t("common.unknown");
}

function selectorIdentity(selector: CleanupSelector) {
  switch (selector.kind) {
    case "connection":
      return selector.connection_id;
    case "scope":
      return selector.scope_id;
    case "group":
      return selector.group_key;
    case "asset":
      return selector.asset_id;
  }
}

function scopeLabel(selectors: CleanupSelector[], t: any) {
  if (selectors.length === 1 && selectors[0]?.kind === "connection") {
    return t("cleanup.wholeAccount");
  }
  if (selectors.length === 1) return selectorName(selectors[0]!);
  return t("cleanup.resourceScope", { count: selectors.length });
}
