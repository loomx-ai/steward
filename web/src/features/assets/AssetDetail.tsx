import { useMemo, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { LocateFixed } from "lucide-react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import {
  findAsset,
  findAssets,
  getAssetGraph,
  getAssetLifecycle,
  listConnectionRegions,
  listProviderCatalog,
} from "@/api/client";
import type {
  Asset,
  LifecycleBinding,
  Relationship,
  ResourceKind,
} from "@/api/types";
import { PageTitle } from "@/app/PageTitleContext";
import { AsyncState } from "@/components/domain/AsyncState";
import { CloudProviderIcon } from "@/components/domain/CloudProviderIcon";
import { CopyableId } from "@/components/domain/CopyableId";
import {
  ResourceMarkerBadge,
  resourceMarkerForAsset,
} from "@/components/domain/ResourceMarkerBadge";
import { StateBadge } from "@/components/domain/StateBadge";
import {
  resourceProductName,
  resourceTypeName,
} from "@/components/domain/resourceKindLabel";
import { DetailSection } from "@/components/patterns/DetailSection";
import { PageLayout } from "@/components/patterns/PageLayout";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useRequiredConnection } from "@/connections/ActiveConnectionProvider";
import { useLocale } from "@/i18n/LocaleProvider";
import { cn } from "@/lib/utils";
import { cloudConsoleURL } from "../panorama/consoleLinks";
import {
  resourcePropertyRows,
  type ResourcePropertyRow,
} from "../panorama/resourceProperties";
import { CopyResourcePropertiesButton } from "../panorama/CopyResourcePropertiesButton";
import { ResourcePropertyValue } from "../panorama/ResourcePropertyValue";
import {
  useResourcePropertyLinks,
  type ResourcePropertyLink,
} from "../panorama/resourcePropertyLinks";
import {
  assetConsoleRegionID,
  assetRegionLabel,
  resolveAssetRegion,
} from "./assetRegions";
import { DirtyAssetButton } from "./DirtyAssetButton";
import { panoramaResourcePath } from "../panorama/route";
import { AssetRelationshipPanorama } from "./AssetRelationshipPanorama";

type DetailView = "overview" | "relationships";

interface ResourceRelation {
  key: string;
  peerID: string;
  direction: "incoming" | "outgoing";
  type: string;
  kind: "relationship" | "lifecycle";
}

export function AssetDetail() {
  const { id = "" } = useParams();
  const [searchParams, setSearchParams] = useSearchParams();
  const connection = useRequiredConnection();
  const { formatDate, formatError, locale, t } = useLocale();
  const selectedView: DetailView =
    searchParams.get("view") === "relationships" ? "relationships" : "overview";
  const asset = useQuery({
    queryKey: ["asset", connection.id, id],
    queryFn: () => findAsset(connection.id, id),
    enabled: !!id,
  });
  const graph = useQuery({
    queryKey: ["asset-graph", connection.id, id],
    queryFn: () => getAssetGraph(connection.id, id),
    enabled: !!id,
  });
  const lifecycle = useQuery({
    queryKey: ["asset-lifecycle", connection.id, id],
    queryFn: () => getAssetLifecycle(connection.id, id),
    enabled: !!id,
  });
  const catalog = useQuery({
    queryKey: ["catalog"],
    queryFn: listProviderCatalog,
  });
  const regions = useQuery({
    queryKey: ["connection-regions", connection.id, "asset-detail"],
    queryFn: () => listConnectionRegions(connection.id),
  });
  const relatedIDs = useMemo(
    () => [
      ...new Set(
        [
          ...(graph.data?.relationships.flatMap((edge) => [
            edge.source_asset_id,
            edge.target_asset_id,
          ]) ?? []),
          ...(lifecycle.data?.bindings.flatMap((binding) => [
            binding.controller_asset_id,
            binding.managed_asset_id,
          ]) ?? []),
        ].filter((value) => value !== id),
      ),
    ],
    [graph.data, lifecycle.data, id],
  );
  const related = useQuery({
    queryKey: ["related-assets", connection.id, relatedIDs],
    queryFn: () => findAssets(connection.id, relatedIDs),
    enabled: relatedIDs.length > 0,
  });
  const value = asset.data;
  const kind = catalog.data
    ?.flatMap((bundle) => bundle.kinds)
    .find((item) => item.id === value?.resource_kind_id);
  const kindName = assetResourceKindName(
    value?.identity.native_type,
    kind,
    locale,
  );
  const propertyRows = useMemo(
    () => (value ? resourcePropertyRows(value, kind, locale, "") : []),
    [kind, locale, value],
  );
  const propertyLinks = useResourcePropertyLinks(value, propertyRows);
  const matchedRegion = resolveAssetRegion(
    value?.location,
    regions.data?.items ?? [],
  );
  const regionName = assetRegionLabel(
    value?.location,
    matchedRegion,
    t("common.global"),
  );
  const relations = useMemo(
    () =>
      buildResourceRelations(
        id,
        graph.data?.relationships ?? [],
        lifecycle.data?.bindings ?? [],
      ),
    [graph.data, id, lifecycle.data],
  );
  const parentRelations = relations.filter(
    (relation) =>
      relation.kind === "relationship" &&
      relation.direction === "outgoing" &&
      relation.type === "member_of",
  );
  const parentAsset =
    parentRelations.length === 1
      ? related.data?.find((item) => item.id === parentRelations[0].peerID)
      : undefined;
  const consoleURL = value
    ? cloudConsoleURL({
        provider: value.identity.provider,
        nativeType: value.identity.native_type,
        nativeId: value.identity.native_id,
        regionId: assetConsoleRegionID(value.location),
        consoleLinkTemplate: kind?.console_link_template,
        templateValues: {
          ...value.normalized,
          parentId: parentAsset?.identity.native_id,
        },
      })
    : undefined;
  const supportingError = graph.error ?? lifecycle.error ?? related.error;
  const relationshipsPending =
    graph.isPending ||
    lifecycle.isPending ||
    (relatedIDs.length > 0 && related.isPending);

  const changeView = (view: string) => {
    if (view === "relationships") {
      setSearchParams({ view: "relationships" }, { replace: true });
    } else {
      setSearchParams({}, { replace: true });
    }
  };

  return (
    <>
      <PageTitle
        title={
          value?.name || value?.identity.native_id || id || t("assets.asset")
        }
        parent={{ label: t("nav.assets"), to: "/assets" }}
      />
      <PageLayout mode="reading" className="flex h-full flex-col p-0">
        <AsyncState
          pending={asset.isPending}
          error={
            asset.error ??
            (!asset.isPending && !value ? t("asset.notFound") : null)
          }
          empty={false}
          onRetry={() => void asset.refetch()}
          formatError={formatError}
          labels={{
            empty: t("asset.notFound"),
            retry: t("shell.retry"),
            failed: t("shell.routeFailure"),
            loading: t("asset.loading"),
          }}
        >
          {value && (
            <div className="flex h-full min-h-0 flex-col">
              {supportingError && (
                <Alert
                  variant="destructive"
                  className="mx-4 mt-4 shrink-0 sm:mx-6 lg:mx-8"
                >
                  <AlertDescription>
                    {formatError(supportingError)}
                  </AlertDescription>
                </Alert>
              )}
              <Tabs
                value={selectedView}
                onValueChange={changeView}
                className="min-h-0 flex-1 gap-0"
              >
                <div
                  className={cn(
                    "flex shrink-0 flex-wrap items-center justify-between gap-3 border-b px-4 pt-6 pb-3 sm:px-6 lg:px-8",
                    selectedView === "relationships"
                      ? "border-border"
                      : "border-transparent",
                  )}
                >
                  <TabsList
                    variant="line"
                    className="h-auto"
                    aria-label={t("asset.detailViews")}
                  >
                    <TabsTrigger
                      value="overview"
                      className="min-h-11 sm:min-h-0"
                    >
                      {t("asset.overview")}
                    </TabsTrigger>
                    <TabsTrigger
                      value="relationships"
                      className="min-h-11 sm:min-h-0"
                    >
                      {t("asset.relationships")}
                      {relations.length > 0 && (
                        <Badge variant="secondary" className="ml-1 px-1.5">
                          {relations.length}
                        </Badge>
                      )}
                    </TabsTrigger>
                  </TabsList>
                  {!value.deleted_at && (
                    <div className="flex flex-wrap items-center justify-end gap-2">
                      <AssetDetailActions
                        asset={value}
                        connectionId={connection.id}
                        consoleURL={consoleURL}
                      />
                    </div>
                  )}
                </div>
                <TabsContent
                  value="overview"
                  className="min-h-0 flex-1 space-y-0 overflow-auto px-4 pt-6 pb-6 sm:px-6 lg:px-8"
                >
                  <FactSection title={t("asset.basicInfo")}>
                    <Fact
                      label={t("common.product")}
                      value={resourceProductName(value.identity.native_type)}
                    />
                    <Fact label={t("common.resourceKind")} value={kindName} />
                    <AssetResourceIDFact
                      asset={value}
                      lifecycleBindings={lifecycle.data?.bindings ?? []}
                    />
                    <Fact
                      label={t("common.provider")}
                      value={value.identity.provider}
                    />
                    <Fact
                      label={t("panorama.resourceRegion")}
                      value={regionName}
                    />
                    <Fact
                      label={t("common.lastSeen")}
                      value={formatDate(value.last_seen_at)}
                    />
                    <Fact
                      label={t("asset.dataQuality")}
                      value={t(value.dirty ? "asset.dirty" : "asset.clean")}
                    />
                  </FactSection>
                  <FactSection
                    title={t("asset.properties")}
                    titleAction={
                      propertyRows.length > 0 ? (
                        <CopyResourcePropertiesButton rows={propertyRows} />
                      ) : undefined
                    }
                  >
                    {propertyRows.length > 0 ? (
                      propertyRows.map((row) => (
                        <PropertyFact
                          key={row.path}
                          row={row}
                          kind={kind}
                          resourceLinks={propertyLinks}
                        />
                      ))
                    ) : (
                      <p className="text-sm text-muted-foreground md:col-span-2">
                        {t("asset.noProperties")}
                      </p>
                    )}
                  </FactSection>
                </TabsContent>
                <TabsContent
                  value="relationships"
                  className="min-h-0 flex-1 overflow-hidden"
                >
                  {relationshipsPending ? (
                    <div
                      className="grid h-full min-h-48 place-items-center text-sm text-muted-foreground"
                      aria-busy="true"
                    >
                      {t("asset.relationshipsLoading")}
                    </div>
                  ) : (
                    <AssetRelationshipPanorama
                      key={value.id}
                      focus={value}
                      assets={related.data ?? []}
                      relationships={graph.data?.relationships ?? []}
                      lifecycleBindings={lifecycle.data?.bindings ?? []}
                      resourceKinds={
                        catalog.data?.flatMap((bundle) => bundle.kinds) ?? []
                      }
                    />
                  )}
                </TabsContent>
              </Tabs>
            </div>
          )}
        </AsyncState>
      </PageLayout>
    </>
  );
}

export function AssetDetailActions({
  asset,
  connectionId,
  consoleURL,
}: {
  asset: Asset;
  connectionId: string;
  consoleURL?: string;
}) {
  const { t } = useLocale();

  if (asset.deleted_at) {
    return null;
  }

  return (
    <>
      <DirtyAssetButton asset={asset} connectionId={connectionId} size="sm" />
      <Button
        variant="outline"
        size="sm"
        className="min-h-11 sm:min-h-8"
        asChild
      >
        <Link to={panoramaResourcePath(asset.id)}>
          {t("asset.viewInPanorama")}
          <LocateFixed aria-hidden="true" />
        </Link>
      </Button>
      {consoleURL && (
        <Button
          variant="outline"
          size="sm"
          className="min-h-11 sm:min-h-8"
          asChild
        >
          <a href={consoleURL} target="_blank" rel="noopener noreferrer">
            {t("panorama.openConsole")}
            <CloudProviderIcon
              provider={asset.identity.provider}
              consoleURL={consoleURL}
            />
          </a>
        </Button>
      )}
    </>
  );
}

export function AssetResourceIDFact({
  asset,
  lifecycleBindings = [],
}: {
  asset: Asset;
  lifecycleBindings?: LifecycleBinding[];
}) {
  const { t } = useLocale();
  const marker = resourceMarkerForAsset(asset, lifecycleBindings);
  const hasTrailing = Boolean(marker || asset.deleted_at);

  return (
    <Fact
      label={t("common.nativeId")}
      value={asset.identity.native_id}
      copyable
      trailing={
        hasTrailing ? (
          <>
            {marker && <ResourceMarkerBadge marker={marker} />}
            {asset.deleted_at && (
              <StateBadge value="deleted" label={t("domain.deleted")} />
            )}
          </>
        ) : undefined
      }
    />
  );
}

export function buildResourceRelations(
  focusID: string,
  relationships: Relationship[],
  bindings: LifecycleBinding[],
): ResourceRelation[] {
  const relations: ResourceRelation[] = [];
  for (const relationship of relationships) {
    if (relationship.source_asset_id === focusID) {
      relations.push({
        key: `relationship:${relationship.id}`,
        peerID: relationship.target_asset_id,
        direction: "outgoing",
        type: relationship.type,
        kind: "relationship",
      });
    } else if (relationship.target_asset_id === focusID) {
      relations.push({
        key: `relationship:${relationship.id}`,
        peerID: relationship.source_asset_id,
        direction: "incoming",
        type: relationship.type,
        kind: "relationship",
      });
    }
  }
  for (const [index, binding] of bindings.entries()) {
    const key =
      binding.id ||
      `${binding.controller_asset_id}:${binding.managed_asset_id}:${index}`;
    if (binding.controller_asset_id === focusID) {
      relations.push({
        key: `lifecycle:${key}`,
        peerID: binding.managed_asset_id,
        direction: "outgoing",
        type: binding.ownership,
        kind: "lifecycle",
      });
    } else if (binding.managed_asset_id === focusID) {
      relations.push({
        key: `lifecycle:${key}`,
        peerID: binding.controller_asset_id,
        direction: "incoming",
        type: binding.ownership,
        kind: "lifecycle",
      });
    }
  }
  return relations;
}

export function assetResourceKindName(
  nativeType: string | undefined,
  kind:
    | {
        native_type: string;
        display_name?: string;
        display_names?: Record<string, string>;
      }
    | undefined,
  locale: string,
): string {
  const fallback = nativeType?.trim() ?? "";
  if (!kind && !fallback) return "—";
  return resourceTypeName(
    kind?.native_type || fallback,
    kind?.display_name || fallback,
    kind?.display_names,
    locale,
  );
}

function FactSection({
  title,
  titleAction,
  children,
}: {
  title: string;
  titleAction?: ReactNode;
  children: ReactNode;
}) {
  return (
    <DetailSection title={title} titleAction={titleAction}>
      <dl className="grid gap-x-16 gap-y-3 md:grid-cols-2">{children}</dl>
    </DetailSection>
  );
}

function Fact({
  label,
  value,
  copyable = false,
  trailing,
  className,
}: {
  label: string;
  value: string;
  copyable?: boolean;
  trailing?: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "grid min-h-7 min-w-0 grid-cols-[7rem_minmax(0,1fr)] items-center gap-4 text-sm",
        className,
      )}
    >
      <dt className="font-medium text-muted-foreground">{label}</dt>
      <dd
        className={cn(
          "min-w-0 break-words tabular-nums",
          trailing && "flex flex-wrap items-center gap-2",
        )}
      >
        {copyable ? (
          <CopyableId label={label} value={value} trailing={trailing} />
        ) : (
          <>
            {value}
            {trailing}
          </>
        )}
      </dd>
    </div>
  );
}

function PropertyFact({
  row,
  kind,
  resourceLinks,
}: {
  row: ResourcePropertyRow;
  kind: ResourceKind | undefined;
  resourceLinks: ReadonlyMap<string, ResourcePropertyLink>;
}) {
  const value =
    row.value === undefined || row.value === null
      ? "—"
      : typeof row.value === "string"
        ? row.value
        : String(row.value);
  const linked =
    (typeof row.value === "string" || typeof row.value === "number") &&
    resourceLinks.has(String(row.value).trim());
  return (
    <div className="grid min-h-8 min-w-0 grid-cols-[8rem_minmax(0,1fr)] items-center gap-4 text-sm">
      <dt className="font-medium leading-5 text-muted-foreground">
        {row.label}
      </dt>
      <dd className="flex min-w-0 items-center gap-1 break-words">
        {!linked &&
        isIdField(row.path) &&
        (typeof row.value === "string" || typeof row.value === "number") ? (
          <CopyableId label={row.label} value={value} />
        ) : (
          <ResourcePropertyValue
            path={row.path}
            value={row.value}
            kind={kind}
            resourceLinks={resourceLinks}
          />
        )}
      </dd>
    </div>
  );
}

function isIdField(field: string) {
  return /Id$|(?:^|[._-])id$/u.test(field);
}
