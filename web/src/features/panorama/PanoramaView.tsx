import {
  Fragment,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle } from "lucide-react";
import { useLocation, useNavigate } from "react-router-dom";
import { toast } from "sonner";
import {
  APIRequestError,
  createScan,
  findAsset,
  getTopology,
  listProviderCatalog,
} from "@/api/client";
import type {
  TopologyEntrySummary,
  TopologyResource,
  TopologyResponse,
  TopologyView,
} from "@/api/types";
import type { ResourceQueryFieldValues } from "@/components/domain/ResourceQuery";
import { StateBadge } from "@/components/domain/StateBadge";
import { PageLayout } from "@/components/patterns/PageLayout";
import { Toolbar } from "@/components/patterns/Toolbar";
import { Alert, AlertDescription } from "@/components/ui/alert";
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
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbSeparator,
} from "@/components/ui/breadcrumb";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  usePageHeaderActionsTarget,
  usePageHeaderNavigationTarget,
} from "@/app/PageHeaderActionsContext";
import { useRequiredConnection } from "@/connections/ActiveConnectionProvider";
import { useLocale } from "@/i18n/LocaleProvider";
import { TopologySummaryView } from "./TopologySummaryView";
import { TopologyCanvas } from "./TopologyCanvas";
import { CleanupListPopover } from "./CleanupListPopover";
import type { CleanupTarget } from "./cleanupSelection";
import { cleanupTargetCanvasPath } from "./cleanupLocation";
import { isStaleTopologyResult, mergeTopologyPages } from "./merge";
import { loadAllScopes, PanoramaSearch } from "./PanoramaSearch";
import {
  panoramaAccountPath,
  panoramaGlobalPath,
  panoramaRegionPath,
  panoramaRegionPublicPath,
  panoramaVPCPath,
  parsePanoramaRoute,
  type PanoramaRoute,
} from "./route";
import {
  panoramaResourceSearchResult,
  panoramaSearchHighlight,
  type PanoramaSearchResult,
} from "./search";

export interface TopologyCrumb {
  key: string;
  label: string;
  pathname: string;
}

interface LoadedPage {
  cursor: string;
  response: TopologyResponse;
}

interface PaginationState {
  scope: string;
  cursor: string;
  pages: LoadedPage[];
  generation: number;
  refreshing: boolean;
  cursorRecoveryUsed: boolean;
  handledCursorStaleGeneration?: number;
}

export function PanoramaView() {
  const connection = useRequiredConnection();
  const { formatDate, formatError, formatNumber, label, locale, t } =
    useLocale();
  const location = useLocation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const headerActionsTarget = usePageHeaderActionsTarget();
  const headerNavigationTarget = usePageHeaderNavigationTarget();
  const [risk, setRisk] = useState("__all__");
  const [selectedResourceKey, setSelectedResourceKey] = useState<string>();
  const [rescanRegions, setRescanRegions] = useState<
    readonly TopologyEntrySummary[]
  >([]);
  const [locatedCleanupTarget, setLocatedCleanupTarget] = useState<{
    pathname: string;
    target: CleanupTarget;
  }>();
  const [hoveredSearchResult, setHoveredSearchResult] =
    useState<PanoramaSearchResult>();
  const [selectedSearchResult, setSelectedSearchResult] =
    useState<PanoramaSearchResult>();
  const [searchFocusRequest, setSearchFocusRequest] = useState<{
    id: number;
    resultKey: string;
  }>();
  const nextSearchFocusRequestID = useRef(0);
  const handledResourceFocus = useRef<string | undefined>(undefined);
  const handledRevisionKey = useRef("");
  const pendingSearchSelection = useRef<
    | {
        pathname: string;
        resourceKey: string;
      }
    | undefined
  >(undefined);
  const panoramaRoute = useMemo(
    () => parsePanoramaRoute(location.pathname),
    [location.pathname],
  );
  const requestedResourceID = useMemo(
    () => new URLSearchParams(location.search).get("resource")?.trim() || "",
    [location.search],
  );
  const resourceKindIDs = useMemo(
    () =>
      normalizeResourceKindIDs(
        new URLSearchParams(location.search).getAll("resource_kind_id"),
      ),
    [location.search],
  );
  const resourceQuery = useMemo(
    () =>
      new URLSearchParams(location.search).get("resource_query")?.trim() || "",
    [location.search],
  );
  const setResourceQuery = useCallback(
    (value: string) => {
      const params = new URLSearchParams(location.search);
      params.delete("resource_kind_id");
      if (value.trim()) params.set("resource_query", value.trim());
      else params.delete("resource_query");
      const search = params.toString();
      navigate(
        {
          pathname: location.pathname,
          search: search ? `?${search}` : "",
        },
        { replace: true },
      );
    },
    [location.pathname, location.search, navigate],
  );
  const focusKey = panoramaRoute?.focusKey ?? "";
  const requestScope = useMemo(
    () =>
      [
        connection.id,
        focusKey,
        resourceKindIDs.join("\u0001"),
        risk,
        resourceQuery,
      ].join("\u0000"),
    [connection.id, focusKey, resourceKindIDs, resourceQuery, risk],
  );
  const [pagination, setPagination] = useState<PaginationState>(() => ({
    scope: requestScope,
    cursor: "",
    pages: [],
    generation: 0,
    refreshing: false,
    cursorRecoveryUsed: false,
  }));
  const activePagination: PaginationState =
    pagination.scope === requestScope
      ? pagination
      : {
          scope: requestScope,
          cursor: "",
          pages: [],
          generation: pagination.generation,
          refreshing: false,
          cursorRecoveryUsed: false,
        };
  const catalog = useQuery({
    queryKey: ["catalog"],
    queryFn: listProviderCatalog,
  });
  const requestedResource = useQuery({
    queryKey: ["asset", connection.id, requestedResourceID],
    queryFn: () => findAsset(connection.id, requestedResourceID),
    enabled: requestedResourceID.length > 0,
  });
  const requestedResourceScopes = useQuery({
    queryKey: ["panorama-search-scopes", connection.id],
    queryFn: () => loadAllScopes(connection.id),
    enabled: requestedResourceID.length > 0,
    staleTime: 60_000,
  });
  const topology = useQuery({
    queryKey: [
      "topology",
      connection.id,
      focusKey,
      resourceKindIDs,
      risk,
      resourceQuery,
      activePagination.cursor,
      activePagination.generation,
    ],
    queryFn: () =>
      getTopology(connection.id, {
        focus_key: focusKey || undefined,
        cursor: activePagination.cursor || undefined,
        limit: 10_000,
        resource_kind_id:
          resourceKindIDs.length > 0 ? resourceKindIDs : undefined,
        risk: risk === "__all__" ? undefined : risk,
        resource_query: resourceQuery || undefined,
      }),
    enabled: panoramaRoute !== undefined,
  });
  const cursorStaleError = isCursorStaleError(topology.error);
  const previousConnectionID = useRef(connection.id);
  const rescanRegionMutation = useMutation({
    mutationFn: (regionIDs: string[]) =>
      createScan(connection.id, {
        scope_mode: "selected_regions",
        region_ids: regionIDs,
      }),
    onSuccess: (task) => {
      setRescanRegions([]);
      toast.success(t("scans.scheduled"));
      void queryClient.invalidateQueries({
        queryKey: ["scans", connection.id],
      });
      navigate(`/scans/${encodeURIComponent(task.id)}`);
    },
  });

  useEffect(() => {
    if (panoramaRoute || !location.pathname.startsWith("/panorama")) return;
    navigate(panoramaAccountPath(), { replace: true });
  }, [location.pathname, navigate, panoramaRoute]);

  useEffect(() => {
    if (previousConnectionID.current === connection.id) return;
    previousConnectionID.current = connection.id;
    setSelectedResourceKey(undefined);
    setLocatedCleanupTarget(undefined);
    setHoveredSearchResult(undefined);
    setSelectedSearchResult(undefined);
    setSearchFocusRequest(undefined);
    pendingSearchSelection.current = undefined;
    if (panoramaRoute?.kind !== "account" || location.search) {
      navigate(panoramaAccountPath(), { replace: true });
    }
  }, [connection.id, location.search, navigate, panoramaRoute?.kind]);

  useEffect(() => {
    if (pagination.scope === requestScope) return;
    setPagination((current) => ({
      scope: requestScope,
      cursor: "",
      pages: [],
      generation: current.generation,
      refreshing: false,
      cursorRecoveryUsed: false,
    }));
  }, [pagination.scope, requestScope]);

  useEffect(() => {
    const response = topology.data;
    if (!response) return;
    const requestedCursor = activePagination.cursor;
    setPagination((current) => {
      const base =
        current.scope === requestScope
          ? current
          : {
              scope: requestScope,
              cursor: "",
              pages: [],
              generation: current.generation,
              refreshing: false,
              cursorRecoveryUsed: false,
            };
      if (
        requestedCursor &&
        base.pages.some((page) => page.cursor === requestedCursor)
      ) {
        return base;
      }

      const pages =
        requestedCursor === ""
          ? [{ cursor: "", response }]
          : [...base.pages, { cursor: requestedCursor, response }];
      let merged: TopologyResponse;
      try {
        merged = mergeTopologyPages(pages.map((page) => page.response));
      } catch {
        return {
          ...base,
          cursor: "",
          pages: [],
          generation: base.generation + 1,
          refreshing: true,
        };
      }
      if (isStaleTopologyResult(merged)) {
        return {
          ...base,
          cursor: "",
          pages: [],
          generation: base.generation + 1,
          refreshing: true,
        };
      }
      return {
        ...base,
        pages,
        cursor:
          response.truncated && response.next_cursor
            ? response.next_cursor
            : requestedCursor,
        refreshing: false,
        cursorRecoveryUsed: merged.truncated ? base.cursorRecoveryUsed : false,
        handledCursorStaleGeneration: merged.truncated
          ? base.handledCursorStaleGeneration
          : undefined,
      };
    });
  }, [activePagination.cursor, requestScope, topology.data]);

  useEffect(() => {
    if (!cursorStaleError || !activePagination.cursor) return;
    const failedGeneration = activePagination.generation;
    setSelectedResourceKey(undefined);
    setPagination((current) => {
      if (
        current.scope !== requestScope ||
        current.generation !== failedGeneration ||
        current.handledCursorStaleGeneration === failedGeneration
      ) {
        return current;
      }
      const shouldRecover = !current.cursorRecoveryUsed;
      return {
        ...current,
        cursor: shouldRecover ? "" : current.cursor,
        pages: [],
        generation: shouldRecover ? current.generation + 1 : current.generation,
        refreshing: shouldRecover,
        cursorRecoveryUsed: true,
        handledCursorStaleGeneration: failedGeneration,
      };
    });
  }, [
    activePagination.cursor,
    activePagination.generation,
    cursorStaleError,
    requestScope,
  ]);

  const merged = useMemo(
    () =>
      pagination.scope === requestScope && pagination.pages.length > 0
        ? mergeTopologyPages(pagination.pages.map((page) => page.response))
        : undefined,
    [pagination.pages, pagination.scope, requestScope],
  );
  const resourceKinds = useMemo(
    () =>
      new Map(
        (catalog.data ?? []).flatMap((bundle) =>
          [
            ...bundle.kinds,
            ...bundle.specs.map((spec) => spec.resource_kind),
          ].map((kind) => [kind.id, kind] as const),
        ),
      ),
    [catalog.data],
  );
  const queryKinds = useMemo(
    () =>
      (catalog.data ?? [])
        .filter((bundle) => bundle.provider === connection.provider)
        .flatMap((bundle) => bundle.kinds),
    [catalog.data, connection.provider],
  );
  const paginationComplete = Boolean(merged && !merged.truncated);
  const loadingRemaining = Boolean(
    merged && (!paginationComplete || topology.isFetching),
  );
  const view = merged?.view;
  const resourceQueryFieldValues = useMemo<ResourceQueryFieldValues>(() => {
    const resources =
      view?.kind === "resource_graph" || view?.kind === "vpc"
        ? view.resources
        : [];
    return {
      state: resources.flatMap((resource) =>
        resource.state ? [{ value: resource.state }] : [],
      ),
    };
  }, [view]);
  const selectedSearchHighlight = useMemo(
    () => panoramaSearchHighlight(view, selectedSearchResult),
    [selectedSearchResult, view],
  );
  const searchHighlight = useMemo(
    () =>
      panoramaSearchHighlight(
        view,
        hoveredSearchResult ?? selectedSearchResult,
      ),
    [hoveredSearchResult, selectedSearchResult, view],
  );
  const activeSearchFocusRequest = useMemo(
    () =>
      searchFocusRequest &&
      selectedSearchResult?.key === searchFocusRequest.resultKey &&
      selectedSearchHighlight
        ? {
            id: searchFocusRequest.id,
            ...selectedSearchHighlight,
          }
        : undefined,
    [searchFocusRequest, selectedSearchHighlight, selectedSearchResult?.key],
  );
  const activeLocatedCleanupTarget =
    locatedCleanupTarget?.pathname === location.pathname
      ? locatedCleanupTarget.target
      : undefined;
  const crumbs = useMemo(
    () => panoramaCrumbs(panoramaRoute, view, t),
    [panoramaRoute, t, view],
  );

  const clearTransientState = useCallback(() => {
    setSelectedResourceKey(undefined);
  }, []);
  const clearSearchSelection = useCallback(() => {
    setHoveredSearchResult(undefined);
    setSelectedSearchResult(undefined);
    setSearchFocusRequest(undefined);
    pendingSearchSelection.current = undefined;
  }, []);
  const clearResourceFocus = useCallback(() => {
    setSelectedResourceKey(undefined);
    setLocatedCleanupTarget(undefined);
    clearSearchSelection();
  }, [clearSearchSelection]);
  const resetRoot = useCallback(() => {
    clearTransientState();
    clearSearchSelection();
    setLocatedCleanupTarget(undefined);
    const destination = `${panoramaAccountPath()}${resourceFilterSearch(
      resourceKindIDs,
      resourceQuery,
    )}`;
    if (`${location.pathname}${location.search}` !== destination) {
      navigate(destination);
    }
  }, [
    clearSearchSelection,
    clearTransientState,
    location.pathname,
    location.search,
    navigate,
    resourceKindIDs,
    resourceQuery,
  ]);
  useEffect(() => {
    window.addEventListener("steward:panorama-root", resetRoot);
    return () => window.removeEventListener("steward:panorama-root", resetRoot);
  }, [resetRoot]);
  const selectCrumb = (crumb: TopologyCrumb) => {
    clearTransientState();
    clearSearchSelection();
    setLocatedCleanupTarget(undefined);
    if (location.pathname !== crumb.pathname) {
      navigate(
        `${crumb.pathname}${resourceFilterSearch(resourceKindIDs, resourceQuery)}`,
      );
    }
  };
  const navigateToSummary = (entry: TopologyEntrySummary) => {
    const target = summaryEntryPath(view, entry);
    if (!target) return;
    clearTransientState();
    clearSearchSelection();
    setLocatedCleanupTarget(undefined);
    if (location.pathname !== target) {
      navigate(
        `${target}${resourceFilterSearch(resourceKindIDs, resourceQuery)}`,
      );
    }
  };
  const selectResource = useCallback(
    (resource: TopologyResource) => {
      clearSearchSelection();
      setLocatedCleanupTarget(undefined);
      setSelectedResourceKey(resource.key);
    },
    [clearSearchSelection],
  );
  const locateCleanupTarget = useCallback(
    (target: CleanupTarget) => {
      const pathname = cleanupTargetCanvasPath(target) ?? location.pathname;
      clearSearchSelection();
      setSelectedResourceKey(undefined);
      setRisk("__all__");
      setLocatedCleanupTarget({ pathname, target });
      if (location.pathname !== pathname || location.search) navigate(pathname);
    },
    [clearSearchSelection, location.pathname, location.search, navigate],
  );
  const selectSearchResult = useCallback(
    (result: PanoramaSearchResult, options: { replace?: boolean } = {}) => {
      const destinationSearch =
        options.replace &&
        location.pathname !== result.pathname &&
        location.search
          ? searchWithoutResourceFilters(location.search)
          : "";
      const destination = `${result.pathname}${destinationSearch}`;
      setHoveredSearchResult(undefined);
      setSelectedSearchResult(result);
      setSearchFocusRequest({
        id: ++nextSearchFocusRequestID.current,
        resultKey: result.key,
      });
      setLocatedCleanupTarget(undefined);
      setSelectedResourceKey(undefined);
      setRisk("__all__");
      if (
        result.kind === "resource" &&
        (view?.kind === "resource_graph" || view?.kind === "vpc")
      ) {
        const visibleResource = view.resources.find(
          (resource) => resource.key === result.resourceKey,
        );
        const visibleBoundary =
          view.kind === "vpc" &&
          (view.vpc.key === result.resourceKey ||
            view.vswitches.some(
              (vSwitch) => vSwitch.key === result.resourceKey,
            ));
        if (visibleResource || visibleBoundary) {
          pendingSearchSelection.current = undefined;
          if (visibleResource) setSelectedResourceKey(visibleResource.key);
          if (options.replace && location.search) {
            navigate(destination, { replace: true });
          }
          return;
        }
      }
      pendingSearchSelection.current =
        result.kind === "resource"
          ? {
              pathname: result.pathname,
              resourceKey: result.resourceKey,
            }
          : undefined;
      if (
        location.pathname !== result.pathname ||
        (options.replace && location.search)
      ) {
        navigate(destination, { replace: options.replace });
      }
    },
    [location.pathname, location.search, navigate, view],
  );
  useEffect(() => {
    if (
      !requestedResourceID ||
      !requestedResource.data ||
      !requestedResourceScopes.data ||
      !catalog.isSuccess
    ) {
      return;
    }
    const requestKey = `${connection.id}:${requestedResourceID}`;
    if (handledResourceFocus.current === requestKey) return;
    handledResourceFocus.current = requestKey;
    selectSearchResult(
      panoramaResourceSearchResult(
        requestedResource.data,
        new Map(requestedResourceScopes.data.map((scope) => [scope.id, scope])),
        resourceKinds,
        locale,
      ),
      { replace: true },
    );
  }, [
    catalog.isSuccess,
    connection.id,
    locale,
    requestedResource.data,
    requestedResourceID,
    requestedResourceScopes.data,
    resourceKinds,
    selectSearchResult,
  ]);
  useEffect(() => {
    clearTransientState();
  }, [clearTransientState, focusKey]);
  const selectedResource = useMemo(
    () =>
      selectedResourceKey &&
      (view?.kind === "resource_graph" || view?.kind === "vpc")
        ? view.resources.find(
            (resource) => resource.key === selectedResourceKey,
          )
        : undefined,
    [selectedResourceKey, view],
  );
  useEffect(() => {
    if (selectedResourceKey && !selectedResource) {
      setSelectedResourceKey(undefined);
    }
  }, [selectedResource, selectedResourceKey]);
  useEffect(() => {
    const pending = pendingSearchSelection.current;
    if (!pending || pending.pathname !== location.pathname || !view) return;
    if (view.kind === "resource_graph" || view.kind === "vpc") {
      const resource = view.resources.find(
        (candidate) => candidate.key === pending.resourceKey,
      );
      if (resource) {
        setSelectedResourceKey(resource.key);
        pendingSearchSelection.current = undefined;
        return;
      }
      const boundaryVisible =
        view.kind === "vpc" &&
        (view.vpc.key === pending.resourceKey ||
          view.vswitches.some(
            (vSwitch) => vSwitch.key === pending.resourceKey,
          ));
      if (boundaryVisible || paginationComplete) {
        pendingSearchSelection.current = undefined;
      }
      return;
    }
    if (paginationComplete) {
      pendingSearchSelection.current = undefined;
    }
  }, [location.pathname, paginationComplete, view]);
  useEffect(() => {
    if (
      !requestedResourceID ||
      selectedSearchResult?.kind !== "resource" ||
      selectedResourceKey !== selectedSearchResult.resourceKey ||
      location.pathname !== selectedSearchResult.pathname
    ) {
      return;
    }
    const params = new URLSearchParams(location.search);
    params.delete("resource");
    const search = params.toString();
    navigate(
      { pathname: location.pathname, search: search ? `?${search}` : "" },
      { replace: true },
    );
  }, [
    location.pathname,
    location.search,
    navigate,
    requestedResourceID,
    selectedResourceKey,
    selectedSearchResult,
  ]);

  const canRecoverCursorStale = Boolean(
    cursorStaleError &&
    activePagination.cursor &&
    !activePagination.cursorRecoveryUsed &&
    activePagination.handledCursorStaleGeneration !==
      activePagination.generation,
  );
  const error =
    catalog.error ??
    requestedResource.error ??
    requestedResourceScopes.error ??
    (cursorStaleError && canRecoverCursorStale
      ? null
      : isResourceQueryError(topology.error)
        ? null
        : topology.error);
  const resourceQueryError = isResourceQueryError(topology.error)
    ? formatError(topology.error)
    : undefined;
  const revisionKey = merged
    ? [
        merged.revision.inventory,
        merged.revision.graph,
        merged.revision.spec_bundle,
      ].join(":")
    : "";

  useEffect(() => {
    if (!revisionKey || handledRevisionKey.current === revisionKey) return;
    handledRevisionKey.current = revisionKey;
    if (selectedSearchHighlight) return;
    setSelectedResourceKey(undefined);
  }, [revisionKey, selectedSearchHighlight]);

  const panoramaNavigation = (
    <Breadcrumb
      className="flex min-w-0 items-center gap-0 overflow-x-auto"
      aria-label={t("panorama.title")}
    >
      <BreadcrumbList className="flex-nowrap">
        {crumbs.map((crumb, index) => {
          const isCurrent = index === crumbs.length - 1;
          return (
            <Fragment key={crumb.key}>
              <BreadcrumbSeparator />
              <BreadcrumbItem className="min-w-0">
                <BreadcrumbLink
                  asChild
                  className={
                    isCurrent ? "min-w-0 font-semibold" : "min-w-0 font-normal"
                  }
                >
                  <button
                    type="button"
                    className="min-w-0 truncate"
                    title={crumb.label}
                    aria-current={isCurrent ? "page" : undefined}
                    onClick={() => selectCrumb(crumb)}
                  >
                    {crumb.label}
                  </button>
                </BreadcrumbLink>
              </BreadcrumbItem>
            </Fragment>
          );
        })}
      </BreadcrumbList>
    </Breadcrumb>
  );
  const panoramaActions = (
    <>
      {merged && !error && (
        <div
          data-panorama-coverage
          className="flex shrink-0 items-center gap-2 text-[10px] text-muted-foreground"
        >
          <span className="flex items-center gap-1">
            {t("panorama.coverage")}
            <StateBadge
              value={merged.coverage.status}
              label={label(merged.coverage.status)}
            />
          </span>
          {merged.coverage.last_complete_scan_at && (
            <span>
              {t("panorama.lastCompleteScan", {
                time: formatDate(merged.coverage.last_complete_scan_at),
              })}
            </span>
          )}
          {merged.coverage.failed_shards > 0 && (
            <span className="text-destructive">
              {t("panorama.failedShards", {
                count: formatNumber(merged.coverage.failed_shards),
              })}
            </span>
          )}
        </div>
      )}
      <Select value={risk} onValueChange={setRisk}>
        <SelectTrigger
          size="sm"
          className="w-36"
          aria-label={t("panorama.allRisk")}
        >
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="__all__">{t("panorama.allRisk")}</SelectItem>
          <SelectItem value="actionable">{t("panorama.actionable")}</SelectItem>
          <SelectItem value="findings">{t("panorama.withFindings")}</SelectItem>
          <SelectItem value="blockers">{t("panorama.withBlockers")}</SelectItem>
        </SelectContent>
      </Select>
    </>
  );

  return (
    <PageLayout mode="canvas" className="h-full p-0">
      {headerNavigationTarget &&
        createPortal(panoramaNavigation, headerNavigationTarget)}
      {headerActionsTarget &&
        createPortal(panoramaActions, headerActionsTarget)}
      {(!headerNavigationTarget || !headerActionsTarget) && (
        <Toolbar label={t("panorama.title")}>
          {!headerNavigationTarget && panoramaNavigation}
          {!headerActionsTarget && panoramaActions}
        </Toolbar>
      )}

      <div className="relative min-h-0 flex-1">
        {panoramaRoute && (
          <div className="pointer-events-none absolute top-3 left-1/2 z-40 -translate-x-1/2">
            <PanoramaSearch
              key={`${connection.id}:${panoramaRoute.pathname}`}
              connectionId={connection.id}
              kinds={resourceKinds}
              queryKinds={queryKinds}
              scope={panoramaRoute}
              resourceQuery={resourceQuery}
              resourceQueryFieldValues={resourceQueryFieldValues}
              resourceQueryError={resourceQueryError}
              onResourceQueryApply={setResourceQuery}
              onHighlight={setHoveredSearchResult}
              onSelect={selectSearchResult}
            />
          </div>
        )}
        {error && (
          <Alert
            variant="destructive"
            className="absolute top-5 right-5 z-30 max-w-[min(34rem,calc(100%-2.5rem))] bg-background/95 shadow-md backdrop-blur"
          >
            <AlertTriangle />
            <AlertDescription>{String(error)}</AlertDescription>
          </Alert>
        )}

        {topology.isPending && !view ? (
          <div
            className="mx-auto max-w-3xl space-y-2 px-4 py-24"
            aria-busy="true"
          >
            <Skeleton className="h-12 w-full" />
            <Skeleton className="h-12 w-full" />
            <Skeleton className="h-12 w-full" />
          </div>
        ) : view?.kind === "account" || view?.kind === "region" ? (
          <TopologySummaryView
            view={view}
            complete={paginationComplete}
            coverageStatus={merged?.coverage.status ?? "unknown"}
            resourceKinds={resourceKinds}
            highlightedEntryKey={
              searchHighlight?.nodeKey ?? activeLocatedCleanupTarget?.key
            }
            focusedCleanupTargetKey={activeLocatedCleanupTarget?.key}
            onNavigate={navigateToSummary}
            onRescanRegions={(entries) => {
              rescanRegionMutation.reset();
              setRescanRegions(entries);
            }}
          />
        ) : view?.kind === "resource_graph" || view?.kind === "vpc" ? (
          <TopologyCanvas
            key={`${revisionKey}:${focusKey}`}
            view={view}
            complete={paginationComplete}
            warnings={merged?.warnings}
            resourceKinds={resourceKinds}
            selectedResourceKey={selectedResourceKey}
            highlightedNodeKey={
              searchHighlight?.nodeKey ?? activeLocatedCleanupTarget?.key
            }
            searchFocusRequest={activeSearchFocusRequest}
            focusedCleanupTargetKey={activeLocatedCleanupTarget?.key}
            highlightedScope={searchHighlight?.scope}
            onSelectResource={selectResource}
            onClearFocus={clearResourceFocus}
          />
        ) : !error ? (
          <div className="grid h-full place-items-center text-sm text-muted-foreground">
            {t("panorama.empty")}
          </div>
        ) : null}

        {(loadingRemaining || pagination.refreshing) && (
          <div className="pointer-events-none absolute right-20 bottom-6 left-6 z-10 flex justify-center lg:right-6">
            <span className="rounded-lg bg-background/90 px-2.5 py-1.5 text-xs text-muted-foreground shadow-sm backdrop-blur">
              {pagination.refreshing
                ? t("panorama.dataUpdated")
                : t("panorama.loadingRemaining")}
            </span>
          </div>
        )}
        <CleanupListPopover onLocate={locateCleanupTarget} />
      </div>
      <AlertDialog
        open={rescanRegions.length > 0}
        onOpenChange={(open) => {
          if (open || rescanRegionMutation.isPending) return;
          setRescanRegions([]);
          rescanRegionMutation.reset();
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {rescanRegions.length > 1
                ? t("panorama.rescanRegionsTitle", {
                    count: rescanRegions.length,
                  })
                : t("panorama.rescanRegionTitle")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {rescanRegions.length > 1
                ? t("panorama.rescanRegionsDescription", {
                    count: rescanRegions.length,
                  })
                : rescanRegions[0]
                  ? t("panorama.rescanRegionDescription", {
                      name:
                        rescanRegions[0].name.trim() ||
                        rescanRegions[0].native_id?.trim() ||
                        rescanRegions[0].key,
                      id:
                        rescanRegions[0].native_id?.trim() ||
                        rescanRegions[0].key,
                    })
                  : ""}
            </AlertDialogDescription>
          </AlertDialogHeader>
          {rescanRegionMutation.error && (
            <Alert variant="destructive">
              <AlertDescription>
                {formatError(rescanRegionMutation.error)}
              </AlertDescription>
            </Alert>
          )}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={rescanRegionMutation.isPending}>
              {t("common.cancel")}
            </AlertDialogCancel>
            <AlertDialogAction
              disabled={
                rescanRegionMutation.isPending ||
                rescanRegions.length === 0 ||
                rescanRegions.some((region) => !region.native_id?.trim())
              }
              onClick={(event) => {
                event.preventDefault();
                const regionIDs = [
                  ...new Set(
                    rescanRegions.flatMap((region) => {
                      const regionID = region.native_id?.trim();
                      return regionID ? [regionID] : [];
                    }),
                  ),
                ].sort();
                if (regionIDs.length === rescanRegions.length) {
                  rescanRegionMutation.mutate(regionIDs);
                }
              }}
            >
              {rescanRegionMutation.isPending
                ? t("scans.scheduling")
                : t("panorama.rescanRegion")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </PageLayout>
  );
}

function summaryEntryPath(
  view: TopologyView | undefined,
  entry: TopologyEntrySummary,
) {
  if (view?.kind === "account") {
    if (entry.key === view.global_resources?.key) {
      return panoramaGlobalPath();
    }
    const regionId = entry.native_id?.trim();
    return regionId ? panoramaRegionPath(regionId) : undefined;
  }
  if (view?.kind === "region") {
    const regionId = view.region.native_id?.trim();
    if (!regionId) return undefined;
    if (entry.key === view.public_resources.key) {
      return panoramaRegionPublicPath(regionId);
    }
    const vpcId = entry.native_id?.trim();
    return vpcId ? panoramaVPCPath(regionId, vpcId) : undefined;
  }
  return undefined;
}

function panoramaCrumbs(
  route: PanoramaRoute | undefined,
  view: TopologyView | undefined,
  t: ReturnType<typeof useLocale>["t"],
): TopologyCrumb[] {
  if (!route || route.kind === "account") return [];
  if (route.kind === "account-global") {
    return [
      {
        key: route.focusKey,
        label: t("panorama.regionGlobalResources"),
        pathname: panoramaGlobalPath(),
      },
    ];
  }
  if (route.kind === "region") {
    const label = view?.kind === "region" ? view.region.name.trim() : "";
    return [
      {
        key: route.focusKey,
        label: label || route.regionId,
        pathname: panoramaRegionPath(route.regionId),
      },
    ];
  }
  if (route.kind === "region-public") {
    const regionLabel =
      view?.kind === "resource_graph" ? view.context.name.trim() : "";
    return [
      {
        key: `region:${route.regionId}`,
        label: regionLabel || route.regionId,
        pathname: panoramaRegionPath(route.regionId),
      },
      {
        key: route.focusKey,
        label: t("panorama.regionGlobalResources"),
        pathname: panoramaRegionPublicPath(route.regionId),
      },
    ];
  }

  const regionLabel = view?.kind === "vpc" ? view.region.name.trim() : "";
  const vpcLabel =
    view?.kind === "vpc"
      ? view.vpc.native_id?.trim() || view.vpc.name.trim()
      : "";
  return [
    {
      key: `region:${route.regionId}`,
      label: regionLabel || route.regionId,
      pathname: panoramaRegionPath(route.regionId),
    },
    {
      key: route.focusKey,
      label: vpcLabel || route.vpcId,
      pathname: panoramaVPCPath(route.regionId, route.vpcId),
    },
  ];
}

function isCursorStaleError(error: unknown): error is APIRequestError {
  return (
    error instanceof APIRequestError && error.code === "topology.cursor_stale"
  );
}

function normalizeResourceKindIDs(values: readonly string[]): string[] {
  return [
    ...new Set(values.map((value) => value.trim()).filter(Boolean)),
  ].sort();
}

function resourceFilterSearch(
  values: readonly string[],
  resourceQuery: string,
): string {
  const params = new URLSearchParams();
  for (const value of normalizeResourceKindIDs(values)) {
    params.append("resource_kind_id", value);
  }
  if (resourceQuery.trim()) params.set("resource_query", resourceQuery.trim());
  const search = params.toString();
  return search ? `?${search}` : "";
}

function searchWithoutResourceFilters(search: string): string {
  const params = new URLSearchParams(search);
  params.delete("resource_kind_id");
  params.delete("resource_query");
  const remaining = params.toString();
  return remaining ? `?${remaining}` : "";
}

function isResourceQueryError(error: unknown): error is APIRequestError {
  return (
    error instanceof APIRequestError &&
    error.code === "topology.resource_query_invalid"
  );
}
