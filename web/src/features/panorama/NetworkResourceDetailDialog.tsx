import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { findAsset, listProviderCatalog } from "@/api/client";
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

export interface NetworkResourceDetail {
  kind: "vpc" | "vswitch";
  assetId?: string;
  name: string;
  nativeId: string;
  region: string;
  zone?: string;
  resourceCount: number;
  icon: string;
}

export function NetworkResourceDetailDialog({
  open,
  onOpenChange,
  connectionId,
  detail,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  connectionId: string;
  detail: NetworkResourceDetail | null | undefined;
}) {
  const { locale, t } = useLocale();
  const canLoadAsset = open && !!detail?.assetId;
  const asset = useQuery({
    queryKey: [
      "panorama-network-resource-detail",
      connectionId,
      detail?.assetId,
    ],
    queryFn: () => findAsset(connectionId, detail!.assetId!),
    enabled: canLoadAsset,
  });
  const catalog = useQuery({
    queryKey: ["catalog"],
    queryFn: listProviderCatalog,
    enabled: canLoadAsset,
  });
  if (!detail) return null;

  const kind = catalog.data
    ?.flatMap((bundle) => bundle.kinds)
    .find((candidate) => candidate.id === asset.data?.resource_kind_id);
  const error = canLoadAsset ? (asset.error ?? catalog.error) : null;
  const loading = canLoadAsset && (asset.isPending || catalog.isPending);
  const fallbackNativeType =
    detail.kind === "vpc" ? "ACS::VPC::VPC" : "ACS::VPC::VSwitch";
  const typeName = resourceTypeName(
    kind?.native_type || asset.data?.identity.native_type || fallbackNativeType,
    kind?.display_name || (detail.kind === "vpc" ? "VPC" : "vSwitch"),
    kind?.display_names,
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
              icon={detail.icon}
              className="mt-0 size-5 text-muted-foreground"
            />
            <span>{detail.name.trim() || detail.nativeId}</span>
          </DialogTitle>
        </DialogHeader>
        <div className="min-h-0 space-y-5 overflow-y-auto pr-1">
          <dl className="grid gap-4 rounded-xl border p-4 sm:grid-cols-2">
            <Fact label={t("common.resourceKind")} value={typeName} />
            <Fact
              label={t("panorama.resourceRegion")}
              value={detail.region || t("common.global")}
            />
          </dl>
          <AssetPropertySection
            asset={asset.data}
            kind={kind}
            loading={loading}
            error={error}
            unavailable={!detail.assetId}
            resourceId={resourceIdForProperties(detail.name, detail.nativeId)}
            onRetry={() => {
              void asset.refetch();
              void catalog.refetch();
            }}
          />
        </div>
        {detail.assetId && (
          <DialogFooter>
            {asset.data && (
              <DirtyAssetButton
                asset={asset.data}
                connectionId={connectionId}
              />
            )}
            <Button variant="outline" asChild>
              <Link to={`/assets/${encodeURIComponent(detail.assetId)}`}>
                {t("panorama.openAsset")}
              </Link>
            </Button>
          </DialogFooter>
        )}
      </DialogContent>
    </Dialog>
  );
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="mt-1 break-words text-sm">{value}</dd>
    </div>
  );
}
