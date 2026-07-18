import { useEffect, useMemo, useState } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { ListMinus, ListPlus, LocateFixed } from "lucide-react";
import { Link, useNavigate } from "react-router-dom";
import {
  APIRequestError,
  listAssets,
  listConnectionRegions,
  listProviderCatalog,
} from "@/api/client";
import type { Asset, ConnectionRegion, ResourceKind } from "@/api/types";
import { AsyncState } from "@/components/domain/AsyncState";
import { CursorPagination } from "@/components/domain/CursorPagination";
import { CloudProviderIcon } from "@/components/domain/CloudProviderIcon";
import { DataTableShell } from "@/components/domain/DataTableShell";
import { ResourceIcon } from "@/components/domain/ResourceIcon";
import {
  ResourceQueryInput,
  ResourceSearchModeToggle,
  type ResourceQueryFieldValues,
  type ResourceSearchMode,
} from "@/components/domain/ResourceQuery";
import { resourceTypeName } from "@/components/domain/resourceKindLabel";
import { PageLayout } from "@/components/patterns/PageLayout";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { useRequiredConnection } from "@/connections/ActiveConnectionProvider";
import { useCursorPagination } from "@/hooks/useCursorPagination";
import { useLocale } from "@/i18n/LocaleProvider";
import {
  isCleanupTargetPending,
  type CleanupTarget,
} from "../panorama/cleanupSelection";
import { useCleanupSelection } from "../panorama/CleanupSelectionContext";
import { cleanupTargetCanvasPath } from "../panorama/cleanupLocation";
import { CleanupListPopover } from "../panorama/CleanupListPopover";
import { assetCleanupTarget } from "../panorama/cleanupTargets";
import { cloudConsoleURL } from "../panorama/consoleLinks";
import {
  panoramaGlobalPath,
  panoramaRegionPath,
  panoramaRegionPublicPath,
  panoramaResourcePath,
  panoramaVPCPath,
  parsePanoramaRoute,
} from "../panorama/route";
import {
  assetConsoleRegionID,
  assetRegionLabel,
  resolveAssetRegion,
} from "./assetRegions";
import { DirtyAssetButton } from "./DirtyAssetButton";

export interface AssetFilters {
  search: string;
  provider: string;
  capability: string;
  resourceKindIDs: string[];
}

export function filterAssets(values: Asset[], filters: AssetFilters) {
  const term = filters.search.trim().toLowerCase();
  return values.filter((asset) => {
    if (
      filters.provider !== "__all__" &&
      asset.identity.provider !== filters.provider
    )
      return false;
    if (
      filters.capability !== "__all__" &&
      !asset.capabilities?.includes(filters.capability)
    )
      return false;
    if (
      filters.resourceKindIDs.length > 0 &&
      !filters.resourceKindIDs.includes(asset.resource_kind_id)
    )
      return false;
    if (!term) return true;
    return [
      asset.name,
      asset.identity.native_id,
      asset.identity.native_type,
      asset.location,
    ].some((value) => value?.toLowerCase().includes(term));
  });
}

export function AssetsView() {
  const navigate = useNavigate();
  const connection = useRequiredConnection();
  const { formatDate, formatError, locale, t } = useLocale();
  const {
    addTargets,
    removeBatchMember,
    removeTarget,
    targets: cleanupTargets,
  } = useCleanupSelection();
  const [search, setSearch] = useState("");
  const [query, setQuery] = useState("");
  const [resourceQuery, setResourceQuery] = useState("");
  const [searchMode, setSearchMode] = useState<ResourceSearchMode>("simple");
  const [selectedAssets, setSelectedAssets] = useState<Map<string, Asset>>(
    () => new Map(),
  );
  useEffect(() => {
    const timer = window.setTimeout(() => setQuery(search.trim()), 250);
    return () => window.clearTimeout(timer);
  }, [search]);
  useEffect(() => {
    setSelectedAssets(new Map());
  }, [connection.id]);
  const activeQuery = searchMode === "simple" ? query : "";
  const activeResourceQuery = searchMode === "advanced" ? resourceQuery : "";
  const filterKey = `${connection.id}:${activeQuery}:${activeResourceQuery}`;
  const pagination = useCursorPagination(filterKey);
  const assets = useQuery({
    queryKey: [
      "assets",
      connection.id,
      activeQuery,
      activeResourceQuery,
      pagination.cursor,
      pagination.pageSize,
    ],
    queryFn: () =>
      listAssets(connection.id, {
        cursor: pagination.cursor,
        limit: pagination.pageSize,
        query: activeQuery || undefined,
        resourceQuery: activeResourceQuery || undefined,
      }),
    placeholderData: keepPreviousData,
    refetchOnMount: "always",
  });
  const catalog = useQuery({
    queryKey: ["catalog"],
    queryFn: listProviderCatalog,
  });
  const regions = useQuery({
    queryKey: ["connection-regions", connection.id, "asset-list"],
    queryFn: () => listConnectionRegions(connection.id),
  });
  const rows = assets.data?.items ?? [];
  const kinds = useMemo(
    () =>
      new Map(
        (catalog.data ?? []).flatMap((bundle) =>
          bundle.kinds.map((kind) => [kind.id, kind] as const),
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
  const resourceQueryError =
    searchMode === "advanced" &&
    assets.error instanceof APIRequestError &&
    assets.error.code === "assets.query_invalid"
      ? formatError(assets.error)
      : undefined;
  const connectionRegions = useMemo(
    () => regions.data?.items ?? [],
    [regions.data],
  );
  const queryFieldValues = useMemo<ResourceQueryFieldValues>(
    () => ({
      region: connectionRegions.map((region) => ({
        value: region.region_id,
        detail: region.name,
      })),
      state: rows.flatMap((asset) =>
        asset.state ? [{ value: asset.state }] : [],
      ),
    }),
    [connectionRegions, rows],
  );
  const rowTargets = useMemo(
    () =>
      new Map(
        rows.map((asset) => [
          asset.id,
          cleanupTargetForAsset({
            asset,
            connectionId: connection.id,
            kind: kinds.get(asset.resource_kind_id),
            regions: connectionRegions,
            globalLabel: t("common.global"),
          }),
        ]),
      ),
    [connection.id, connectionRegions, kinds, rows, t],
  );
  const removalOperationsByAsset = useMemo(
    () =>
      new Map(
        rows.map((asset) => [
          asset.id,
          cleanupRemovalOperations(cleanupTargets, rowTargets.get(asset.id)!),
        ]),
      ),
    [cleanupTargets, rowTargets, rows],
  );
  const selectableRows = rows.filter((asset) => {
    const target = rowTargets.get(asset.id);
    const pending =
      target !== undefined && isCleanupTargetPending(cleanupTargets, target);
    const directlyListed =
      (removalOperationsByAsset.get(asset.id)?.length ?? 0) > 0;
    return (
      !asset.closed_at &&
      !asset.dirty &&
      target !== undefined &&
      (!pending || directlyListed)
    );
  });
  const selectedOnPage = selectableRows.filter((asset) =>
    selectedAssets.has(asset.id),
  ).length;
  const allOnPageSelected =
    selectableRows.length > 0 && selectedOnPage === selectableRows.length;
  const pageSelectionState = allOnPageSelected
    ? true
    : selectedOnPage > 0
      ? "indeterminate"
      : false;
  const selectedTargets = [...selectedAssets.values()].map((asset) =>
    cleanupTargetForAsset({
      asset,
      connectionId: connection.id,
      kind: kinds.get(asset.resource_kind_id),
      regions: connectionRegions,
      globalLabel: t("common.global"),
    }),
  );
  const selectedTargetsAlreadyListed = selectedTargets.filter((target) =>
    isCleanupTargetPending(cleanupTargets, target),
  );
  const allSelectedTargetsAlreadyListed =
    selectedTargets.length > 0 &&
    selectedTargetsAlreadyListed.length === selectedTargets.length;
  const selectedTargetsToAdd = selectedTargets.filter(
    (target) => !isCleanupTargetPending(cleanupTargets, target),
  );
  const selectionActionCount = allSelectedTargetsAlreadyListed
    ? selectedTargets.length
    : selectedTargetsToAdd.length;

  const toggleAsset = (asset: Asset) => {
    setSelectedAssets((current) => {
      const next = new Map(current);
      if (next.has(asset.id)) next.delete(asset.id);
      else next.set(asset.id, asset);
      return next;
    });
  };

  const togglePage = () => {
    setSelectedAssets((current) => {
      const next = new Map(current);
      if (allOnPageSelected) {
        for (const asset of selectableRows) next.delete(asset.id);
      } else {
        for (const asset of selectableRows) next.set(asset.id, asset);
      }
      return next;
    });
  };

  const addSelectedToCleanup = () => {
    addTargets(selectedTargetsToAdd);
    setSelectedAssets(new Map());
  };

  const removeSelectedFromCleanup = () => {
    const seen = new Set<string>();
    for (const target of selectedTargets) {
      for (const operation of cleanupRemovalOperations(
        cleanupTargets,
        target,
      )) {
        const operationKey =
          operation.kind === "target"
            ? `target:${operation.targetKey}`
            : `member:${operation.targetKey}:${operation.assetID}`;
        if (seen.has(operationKey)) continue;
        seen.add(operationKey);
        if (operation.kind === "target") removeTarget(operation.targetKey);
        else removeBatchMember(operation.targetKey, operation.assetID);
      }
    }
    setSelectedAssets(new Map());
  };

  const locateCleanupTarget = (target: CleanupTarget) => {
    const selectors = Array.isArray(target.selector)
      ? target.selector
      : [target.selector];
    const assetSelector = selectors.find(
      (selector) => selector.kind === "asset",
    );
    if (assetSelector?.kind === "asset") {
      navigate(`/assets/${assetSelector.asset_id}`);
      return;
    }
    navigate(cleanupTargetCanvasPath(target) ?? "/panorama");
  };

  const changeSearchMode = (mode: ResourceSearchMode) => {
    if (mode === searchMode) return;
    setSearchMode(mode);
  };

  return (
    <PageLayout mode="list" className="pt-0">
      <DataTableShell
        toolbar={
          <>
            <div className="relative min-w-64 flex-1 self-start md:max-w-xl">
              <ResourceSearchModeToggle
                mode={searchMode}
                onModeChange={changeSearchMode}
                className="absolute top-1/2 left-1.5 z-20 -translate-y-1/2"
              />
              {searchMode === "simple" ? (
                <Input
                  className="h-11 pr-9 pl-10 sm:h-9"
                  value={search}
                  onChange={(event) => setSearch(event.target.value)}
                  placeholder={t("assets.search")}
                  aria-label={t("assets.search")}
                />
              ) : (
                <ResourceQueryInput
                  value={resourceQuery}
                  resourceKinds={queryKinds}
                  fieldValues={queryFieldValues}
                  error={resourceQueryError}
                  onApply={setResourceQuery}
                  autoFocus
                  inputClassName="h-11 sm:h-9"
                />
              )}
            </div>
            {selectedAssets.size > 0 && (
              <Button
                type="button"
                className="ml-auto h-11 w-full sm:h-9 sm:w-auto"
                onClick={
                  allSelectedTargetsAlreadyListed
                    ? removeSelectedFromCleanup
                    : addSelectedToCleanup
                }
              >
                {allSelectedTargetsAlreadyListed ? (
                  <ListMinus aria-hidden="true" />
                ) : (
                  <ListPlus aria-hidden="true" />
                )}
                {t(
                  allSelectedTargetsAlreadyListed
                    ? "panorama.removeFromCleanup"
                    : "panorama.addToCleanup",
                )}
                <span aria-hidden="true">({selectionActionCount})</span>
              </Button>
            )}
          </>
        }
        pagination={
          rows.length > 0 ? (
            <CursorPagination
              page={pagination.page}
              pageCount={pagination.pageCount}
              hasNextPage={Boolean(assets.data?.next_cursor)}
              pending={assets.isFetching}
              pageSize={pagination.pageSize}
              onPrevious={pagination.goPrevious}
              onNext={() => pagination.goNext(assets.data?.next_cursor ?? "")}
              onPageSelect={pagination.goToPage}
              onPageSizeChange={pagination.setPageSize}
              labels={{
                page: (page) => t("common.page", { page }),
                pageSize: t("common.pageSize"),
                previous: t("common.previous"),
                next: t("common.next"),
              }}
            />
          ) : undefined
        }
      >
        <AsyncState
          pending={assets.isPending}
          error={
            resourceQueryError ? catalog.error : (assets.error ?? catalog.error)
          }
          empty={!assets.isPending && rows.length === 0}
          onRetry={() =>
            void Promise.all([
              assets.refetch(),
              catalog.refetch(),
              regions.refetch(),
            ])
          }
          formatError={formatError}
          labels={{
            empty: t("assets.empty"),
            retry: t("shell.retry"),
            failed: t("shell.routeFailure"),
            loading: t("assets.loading"),
          }}
        >
          <TooltipProvider delayDuration={300}>
            <Table className="table-fixed sm:table-auto">
              <TableHeader>
                <TableRow>
                  <TableHead className="w-10 px-3">
                    <Checkbox
                      checked={pageSelectionState}
                      disabled={selectableRows.length === 0}
                      onCheckedChange={togglePage}
                      aria-label={t("assets.selectPage")}
                    />
                  </TableHead>
                  <TableHead className="w-auto">{t("assets.asset")}</TableHead>
                  <TableHead className="hidden sm:table-cell">
                    {t("common.resourceKind")}
                  </TableHead>
                  <TableHead className="hidden md:table-cell">
                    {t("panorama.resourceRegion")}
                  </TableHead>
                  <TableHead className="hidden lg:table-cell">
                    {t("common.lastSeen")}
                  </TableHead>
                  <TableHead className="w-32 text-right">
                    <span className="sr-only">{t("common.actions")}</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((asset) => (
                  <AssetRow
                    key={asset.id}
                    asset={asset}
                    kind={kinds.get(asset.resource_kind_id)}
                    locale={locale}
                    formatDate={formatDate}
                    regions={connectionRegions}
                    globalLabel={t("common.global")}
                    selected={selectedAssets.has(asset.id)}
                    pendingCleanup={isCleanupTargetPending(
                      cleanupTargets,
                      rowTargets.get(asset.id)!,
                    )}
                    cleanupSelectionDisabled={
                      !!asset.closed_at ||
                      !!asset.dirty ||
                      (isCleanupTargetPending(
                        cleanupTargets,
                        rowTargets.get(asset.id)!,
                      ) &&
                        (removalOperationsByAsset.get(asset.id)?.length ??
                          0) === 0)
                    }
                    selectLabel={t("assets.select", {
                      name: asset.name || asset.identity.native_id,
                    })}
                    pendingCleanupLabel={t("panorama.pendingCleanup")}
                    dirtyLabel={t("asset.dirty")}
                    viewInPanoramaLabel={t("asset.viewInPanorama")}
                    openConsoleLabel={t("panorama.openConsole")}
                    connectionId={connection.id}
                    onToggle={() => toggleAsset(asset)}
                  />
                ))}
              </TableBody>
            </Table>
          </TooltipProvider>
        </AsyncState>
      </DataTableShell>
      <CleanupListPopover position="fixed" onLocate={locateCleanupTarget} />
    </PageLayout>
  );
}

function AssetRow({
  asset,
  kind,
  locale,
  formatDate,
  regions,
  globalLabel,
  selected,
  pendingCleanup,
  cleanupSelectionDisabled,
  selectLabel,
  pendingCleanupLabel,
  dirtyLabel,
  viewInPanoramaLabel,
  openConsoleLabel,
  connectionId,
  onToggle,
}: {
  asset: Asset;
  kind?: ResourceKind;
  locale: string;
  formatDate: (value: string) => string;
  regions: ConnectionRegion[];
  globalLabel: string;
  selected: boolean;
  pendingCleanup: boolean;
  cleanupSelectionDisabled: boolean;
  selectLabel: string;
  pendingCleanupLabel: string;
  dirtyLabel: string;
  viewInPanoramaLabel: string;
  openConsoleLabel: string;
  connectionId: string;
  onToggle: () => void;
}) {
  const name = asset.name || asset.identity.native_id;
  const kindName = resourceTypeName(
    kind?.native_type || asset.identity.native_type,
    kind?.display_name || asset.identity.native_type,
    kind?.display_names,
    locale,
  );
  const regionName = assetRegionLabel(
    asset.location,
    resolveAssetRegion(asset.location, regions),
    globalLabel,
  );
  const consoleURL = cloudConsoleURL({
    provider: asset.identity.provider,
    nativeType: asset.identity.native_type,
    nativeId: asset.identity.native_id,
    regionId: assetConsoleRegionID(asset.location),
    consoleLinkTemplate: kind?.console_link_template,
    templateValues: asset.normalized,
  });
  return (
    <TableRow
      data-state={selected ? "selected" : undefined}
      data-pending-cleanup={pendingCleanup ? "true" : undefined}
    >
      <TableCell className="px-3" onClick={(event) => event.stopPropagation()}>
        <Checkbox
          checked={selected}
          disabled={cleanupSelectionDisabled}
          onCheckedChange={onToggle}
          aria-label={
            pendingCleanup
              ? `${selectLabel} · ${pendingCleanupLabel}`
              : selectLabel
          }
        />
      </TableCell>
      <TableCell className="max-w-0 sm:max-w-none">
        <div className="flex min-w-0 items-center gap-3">
          <span className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-muted text-muted-foreground">
            <ResourceIcon icon={kind?.icon} className="mt-0 size-5" />
          </span>
          <span className="min-w-0">
            <Link
              to={`/assets/${asset.id}`}
              className="block truncate font-medium text-info underline-offset-4 hover:underline focus-visible:rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {name}
            </Link>
            <span className="mt-0.5 block truncate font-mono text-[11px] text-muted-foreground">
              {asset.identity.native_id}
            </span>
            {pendingCleanup && (
              <span className="mt-0.5 block truncate text-[11px] text-warning-foreground">
                {pendingCleanupLabel}
              </span>
            )}
            {asset.dirty && (
              <span className="mt-0.5 block truncate text-[11px] text-destructive">
                {dirtyLabel}
              </span>
            )}
          </span>
        </div>
      </TableCell>
      <TableCell className="hidden sm:table-cell">
        <span className="block max-w-64 truncate">{kindName}</span>
      </TableCell>
      <TableCell className="hidden md:table-cell">
        <span className="block">{regionName}</span>
      </TableCell>
      <TableCell className="hidden text-xs text-muted-foreground lg:table-cell">
        {formatDate(asset.last_seen_at)}
      </TableCell>
      <TableCell className="w-32 px-1 sm:px-2">
        <div className="flex items-center justify-end gap-1">
          <DirtyAssetButton
            asset={asset}
            connectionId={connectionId}
            iconOnly
          />
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
                  aria-label={`${viewInPanoramaLabel}: ${name}`}
                >
                  <LocateFixed aria-hidden="true" />
                </Link>
              </Button>
            </TooltipTrigger>
            <TooltipContent>{viewInPanoramaLabel}</TooltipContent>
          </Tooltip>
          {consoleURL && (
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
                    aria-label={`${openConsoleLabel}: ${name}`}
                  >
                    <CloudProviderIcon
                      provider={asset.identity.provider}
                      consoleURL={consoleURL}
                    />
                  </a>
                </Button>
              </TooltipTrigger>
              <TooltipContent>{openConsoleLabel}</TooltipContent>
            </Tooltip>
          )}
        </div>
      </TableCell>
    </TableRow>
  );
}

type CleanupRemovalOperation =
  | { kind: "target"; targetKey: string }
  | { kind: "batch-member"; targetKey: string; assetID: string };

function cleanupRemovalOperations(
  targets: readonly CleanupTarget[],
  candidate: CleanupTarget,
): CleanupRemovalOperation[] {
  const candidateAssetIDs = new Set(cleanupTargetAssetIDs(candidate));
  const operations = new Map<string, CleanupRemovalOperation>();

  for (const target of targets) {
    if (target.connectionId !== candidate.connectionId) continue;
    const matchedAssetIDs = cleanupTargetAssetIDs(target).filter((assetID) =>
      candidateAssetIDs.has(assetID),
    );
    if (target.kind === "resource" && matchedAssetIDs.length > 0) {
      operations.set(`target:${target.key}`, {
        kind: "target",
        targetKey: target.key,
      });
    } else if (target.kind === "resource_batch") {
      for (const assetID of matchedAssetIDs) {
        operations.set(`member:${target.key}:${assetID}`, {
          kind: "batch-member",
          targetKey: target.key,
          assetID,
        });
      }
    }
  }

  return [...operations.values()];
}

function cleanupTargetAssetIDs(target: CleanupTarget): string[] {
  const selectors = Array.isArray(target.selector)
    ? target.selector
    : [target.selector];
  return selectors.flatMap((selector) =>
    selector.kind === "asset" ? [selector.asset_id] : [],
  );
}

function cleanupTargetForAsset({
  asset,
  connectionId,
  kind,
  regions,
  globalLabel,
}: {
  asset: Asset;
  connectionId: string;
  kind?: ResourceKind;
  regions: ConnectionRegion[];
  globalLabel: string;
}): CleanupTarget {
  const matchedRegion = resolveAssetRegion(asset.location, regions);
  const regionID = matchedRegion?.region_id.trim();
  const explicitVPCID = normalizedString(asset.normalized, "vpc_id");
  const vpcID =
    kind?.class === "network.vpc"
      ? asset.identity.native_id.trim() || explicitVPCID
      : explicitVPCID;

  if (!regionID) {
    const scopeKey = parsePanoramaRoute(panoramaGlobalPath())?.focusKey;
    return assetCleanupTarget({
      connectionId,
      asset,
      ancestryKeys: scopeKey ? [scopeKey] : [],
      locationContext: scopeKey
        ? { scope: { key: scopeKey, name: globalLabel } }
        : undefined,
    });
  }

  const regionKey = parsePanoramaRoute(panoramaRegionPath(regionID))?.focusKey;
  const region = {
    key: regionKey ?? `region:${regionID}`,
    name:
      matchedRegion?.name.trim() ||
      matchedRegion?.discovered_name?.trim() ||
      regionID,
    native_id: regionID,
  };

  if (vpcID) {
    const vpcKey = parsePanoramaRoute(
      panoramaVPCPath(regionID, vpcID),
    )?.focusKey;
    const vpc = {
      key: vpcKey ?? `vpc:${regionID}:${vpcID}`,
      name: kind?.class === "network.vpc" ? asset.name?.trim() || vpcID : vpcID,
      native_id: vpcID,
    };
    return assetCleanupTarget({
      connectionId,
      asset,
      ancestryKeys: [region.key, vpc.key],
      locationContext: { region, vpc },
    });
  }

  const scopeKey = parsePanoramaRoute(
    panoramaRegionPublicPath(regionID),
  )?.focusKey;
  return assetCleanupTarget({
    connectionId,
    asset,
    ancestryKeys: [region.key, ...(scopeKey ? [scopeKey] : [])],
    locationContext: { region },
  });
}

function normalizedString(
  values: Record<string, unknown> | undefined,
  key: string,
) {
  const value = values?.[key];
  return typeof value === "string" || typeof value === "number"
    ? String(value).trim()
    : "";
}
