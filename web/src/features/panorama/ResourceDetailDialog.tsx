import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { findAsset, listProviderCatalog } from "@/api/client";
import type { TopologyResource } from "@/api/types";
import { resourceTypeName } from "@/components/domain/resourceKindLabel";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useLocale } from "@/i18n/LocaleProvider";
import { DirtyAssetButton } from "../assets/DirtyAssetButton";
import {
  AssetPropertySection,
  resourceIdForProperties,
} from "./AssetPropertySection";
import { ResourceIcon } from "./TopologyNodes";

export { resourcePropertyRows } from "./AssetPropertySection";

export function ResourceDetailDialog({
  open,
  onOpenChange,
  connectionId,
  resource,
  regionName,
  pendingCleanup,
  inheritedCleanup = false,
  onAddToCleanup,
  onRemoveFromCleanup,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  connectionId: string;
  resource: TopologyResource | null | undefined;
  regionName?: string;
  pendingCleanup: boolean;
  inheritedCleanup?: boolean;
  onAddToCleanup: () => void;
  onRemoveFromCleanup: () => void;
}) {
  const { locale, t } = useLocale();
  const asset = useQuery({
    queryKey: ["panorama-resource-detail", connectionId, resource?.asset_id],
    queryFn: () => findAsset(connectionId, resource!.asset_id),
    enabled: open && !!resource?.asset_id,
  });
  const catalog = useQuery({
    queryKey: ["catalog"],
    queryFn: listProviderCatalog,
    enabled: open && !!resource,
  });
  if (!resource) return null;

  const kind = catalog.data
    ?.flatMap((bundle) => bundle.kinds)
    .find((candidate) => candidate.id === asset.data?.resource_kind_id);
  const error = asset.error ?? catalog.error;
  const loading = asset.isPending || catalog.isPending;
  const localizedTypeName =
    kind?.display_names?.[locale]?.trim() ||
    resource.type_names?.[locale]?.trim();
  const fallbackNativeType =
    asset.data?.identity.native_type || resource.resource_kind_id;
  const typeName = resourceTypeName(
    kind?.native_type || fallbackNativeType,
    localizedTypeName || fallbackNativeType,
    localizedTypeName ? { [locale]: localizedTypeName } : undefined,
    locale,
  );

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        aria-describedby={undefined}
        className="max-h-[min(44rem,calc(100vh-2rem))] grid-rows-[auto_minmax(0,1fr)_auto] sm:max-w-2xl"
      >
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <ResourceIcon
              icon={resource.icon}
              className="mt-0 size-5 text-muted-foreground"
            />
            <span>
              {resource.name.trim() || resource.native_id || resource.key}
            </span>
          </DialogTitle>
        </DialogHeader>
        <div className="min-h-0 space-y-5 overflow-y-auto pr-1">
          <dl className="grid gap-4 rounded-xl border p-4 sm:grid-cols-2">
            <PropertyFact
              label={t("common.resourceKind")}
              value={typeName || "—"}
            />
            <PropertyFact
              label={t("panorama.resourceRegion")}
              value={
                regionName?.trim() || asset.data?.location || t("common.global")
              }
            />
          </dl>
          <AssetPropertySection
            asset={asset.data}
            kind={kind}
            loading={loading}
            error={error}
            resourceId={resourceIdForProperties(
              resource.name,
              resource.native_id,
            )}
            onRetry={() => {
              void asset.refetch();
              void catalog.refetch();
            }}
          />
        </div>
        <DialogFooter>
          {asset.data && (
            <DirtyAssetButton asset={asset.data} connectionId={connectionId} />
          )}
          <Button variant="outline" asChild>
            <Link to={`/assets/${encodeURIComponent(resource.asset_id)}`}>
              {t("panorama.openAsset")}
            </Link>
          </Button>
          {asset.data?.dirty && !pendingCleanup ? (
            <Button type="button" variant="outline" disabled>
              {t("asset.dirtyCleanupIgnored")}
            </Button>
          ) : inheritedCleanup ? (
            <Button type="button" variant="outline" disabled>
              {t("panorama.inheritedCleanup")}
            </Button>
          ) : (
            <Button
              type="button"
              variant={pendingCleanup ? "outline" : "default"}
              onClick={pendingCleanup ? onRemoveFromCleanup : onAddToCleanup}
            >
              {t(
                pendingCleanup
                  ? "panorama.removeFromCleanup"
                  : "panorama.addToCleanup",
              )}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function PropertyFact({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="mt-1 break-words text-sm">{value}</dd>
    </div>
  );
}
