import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { AlertCircle, Check, Copy } from "lucide-react";
import type { Asset, ResourceKind } from "@/api/types";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { useLocale } from "@/i18n/LocaleProvider";
import {
  displayPropertyValue,
  resourcePropertyRows,
  type ResourcePropertyRow,
} from "./resourceProperties";
import { CopyResourcePropertiesButton } from "./CopyResourcePropertiesButton";
import { ResourcePropertyValue } from "./ResourcePropertyValue";
import {
  useResourcePropertyLinks,
  type ResourcePropertyLink,
} from "./resourcePropertyLinks";

export {
  resourceIdForProperties,
  resourcePropertyRows,
} from "./resourceProperties";
export type { ResourcePropertyRow } from "./resourceProperties";

export function AssetPropertySection({
  asset,
  kind,
  loading,
  error,
  unavailable = false,
  resourceId,
  onRetry,
}: {
  asset: Asset | undefined;
  kind: ResourceKind | undefined;
  loading: boolean;
  error: unknown;
  unavailable?: boolean;
  resourceId?: string;
  onRetry: () => void;
}) {
  const { formatError, locale, t } = useLocale();
  const identityRows = useMemo(
    () => resourceIdentityRows(resourceId ?? asset?.identity.native_id),
    [asset?.identity.native_id, resourceId],
  );
  const rows = useMemo(
    () =>
      asset
        ? resourcePropertyRows(asset, kind, locale, resourceId)
        : identityRows,
    [asset, identityRows, kind, locale, resourceId],
  );
  const linkableAsset = !unavailable && !error && !loading ? asset : undefined;
  const resourceLinks = useResourcePropertyLinks(
    linkableAsset,
    linkableAsset ? rows : [],
  );
  if (unavailable) {
    return (
      <PropertySection
        rows={identityRows}
        kind={kind}
        resourceLinks={resourceLinks}
      >
        <p className="rounded-xl border p-4 text-sm text-muted-foreground">
          {t("panorama.resourcePropertiesUnavailable")}
        </p>
      </PropertySection>
    );
  }
  if (error) {
    return (
      <Alert variant="destructive">
        <AlertCircle />
        <AlertDescription>
          <span>{formatError(error)}</span>
          <Button type="button" size="xs" variant="outline" onClick={onRetry}>
            {t("panorama.retryDetails")}
          </Button>
        </AlertDescription>
      </Alert>
    );
  }
  if (loading || !asset) {
    return (
      <p className="text-sm text-muted-foreground">{t("common.loading")}</p>
    );
  }

  if (rows.length === 0) return null;

  return (
    <PropertySection rows={rows} kind={kind} resourceLinks={resourceLinks} />
  );
}

function PropertySection({
  rows,
  kind,
  resourceLinks,
  children,
}: {
  rows: ResourcePropertyRow[];
  kind: ResourceKind | undefined;
  resourceLinks: ReadonlyMap<string, ResourcePropertyLink>;
  children?: ReactNode;
}) {
  const { t } = useLocale();
  return (
    <section
      role="region"
      aria-label={t("panorama.resourceProperties")}
      className="space-y-3"
    >
      <div className="flex items-center gap-1">
        <h3 className="text-sm font-semibold">
          {t("panorama.resourceProperties")}
        </h3>
        {rows.length > 0 && <CopyResourcePropertiesButton rows={rows} />}
      </div>
      {rows.length > 0 && (
        <dl className="divide-y rounded-xl border">
          {rows.map((row) => (
            <ResourcePropertyFact
              key={row.path}
              row={row}
              kind={kind}
              resourceLinks={resourceLinks}
            />
          ))}
        </dl>
      )}
      {children}
    </section>
  );
}

function ResourcePropertyFact({
  row,
  kind,
  resourceLinks,
}: {
  row: ResourcePropertyRow;
  kind: ResourceKind | undefined;
  resourceLinks: ReadonlyMap<string, ResourcePropertyLink>;
}) {
  const rawValue = displayPropertyValue(row.value);
  return (
    <div className="group grid items-center gap-1 px-4 py-3 sm:grid-cols-[minmax(10rem,0.8fr)_minmax(0,1.2fr)] sm:gap-4">
      <dt className="text-xs text-muted-foreground">{row.label}</dt>
      <dd className="flex min-w-0 items-center gap-1 break-all text-sm">
        <ResourcePropertyValue
          path={row.path}
          value={row.value}
          kind={kind}
          resourceLinks={resourceLinks}
        />
        <PropertyCopyButton label={row.label} value={rawValue} />
      </dd>
    </div>
  );
}

function PropertyCopyButton({
  label,
  value,
}: {
  label: string;
  value: string;
}) {
  const { t } = useLocale();
  const [copied, setCopied] = useState(false);
  const resetTimer = useRef<ReturnType<typeof setTimeout> | undefined>(
    undefined,
  );

  useEffect(
    () => () => {
      if (resetTimer.current) clearTimeout(resetTimer.current);
    },
    [],
  );

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      if (resetTimer.current) clearTimeout(resetTimer.current);
      resetTimer.current = setTimeout(() => setCopied(false), 1600);
    } catch {
      setCopied(false);
    }
  };
  const actionLabel = copied
    ? t("common.copiedId", { label })
    : t("panorama.copyProperty", { property: label });

  return (
    <Button
      type="button"
      size="icon-xs"
      variant="ghost"
      className="shrink-0 opacity-0 group-hover:opacity-100 focus-visible:opacity-100"
      aria-label={actionLabel}
      title={actionLabel}
      onClick={() => void copy()}
    >
      {copied ? <Check className="text-success" /> : <Copy />}
    </Button>
  );
}

function resourceIdentityRows(
  resourceId: string | undefined,
): ResourcePropertyRow[] {
  const value = resourceId?.trim();
  return value ? [{ path: "$nativeId", label: "ID", value }] : [];
}
