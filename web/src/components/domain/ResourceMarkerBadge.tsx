import type { ComponentProps } from "react";
import type { Asset, LifecycleBinding } from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { useLocale } from "@/i18n/LocaleProvider";
import type { MessageKey } from "@/i18n/messages";
import { cn } from "@/lib/utils";

export type ResourceMarker =
  "managed_asset" | "system_disk" | "system_route_table";

const markerMessageKeys: Record<ResourceMarker, MessageKey> = {
  managed_asset: "resourceMarker.managedAsset",
  system_disk: "resourceMarker.systemDisk",
  system_route_table: "resourceMarker.systemRouteTable",
};

export function ResourceMarkerBadge({
  marker,
  className,
  ...props
}: {
  marker: ResourceMarker;
  className?: string;
} & Omit<ComponentProps<typeof Badge>, "children">) {
  const { t } = useLocale();

  return (
    <Badge
      variant="secondary"
      className={cn(
        "h-5 rounded-md px-1.5 py-0 text-[10px] leading-none",
        className,
      )}
      {...props}
    >
      {t(markerMessageKeys[marker])}
    </Badge>
  );
}

export function resourceMarkerForAsset(
  value: Asset | undefined,
  bindings: LifecycleBinding[] = [],
) {
  const matchingBindings = value
    ? bindings.filter((binding) => binding.managed_asset_id === value.id)
    : [];
  return resolveResourceMarker(value, {
    managed: matchingBindings.length > 0,
    lifecycleKinds: matchingBindings.map((binding) =>
      lifecycleKind(binding.evidence),
    ),
  });
}

export function resolveResourceMarker(
  value: Asset | undefined,
  {
    lifecycleKinds = [],
    managed = false,
  }: {
    lifecycleKinds?: Array<string | undefined>;
    managed?: boolean;
  } = {},
): ResourceMarker | undefined {
  const kinds = new Set(lifecycleKinds.filter(Boolean));
  if (
    kinds.has("vpc_system_route_table") ||
    kinds.has("cen_transit_router_system_route_table") ||
    isSystemRouteTable(value)
  ) {
    return "system_route_table";
  }
  if (kinds.has("ecs_disk_delete_with_instance") || isSystemDisk(value)) {
    return "system_disk";
  }
  return managed || isManagedAsset(value) ? "managed_asset" : undefined;
}

function isManagedAsset(value: Asset | undefined) {
  return normalizedBoolean(value?.normalized, "_service_managed");
}

function isSystemRouteTable(value: Asset | undefined) {
  if (
    value?.identity.native_type !== "ACS::VPC::RouteTable" &&
    value?.identity.native_type !== "ACS::CEN::TransitRouterRouteTable"
  ) {
    return false;
  }
  return [
    normalizedString(value.normalized, "routeTableType"),
    normalizedString(value.normalized, "route_table_type"),
    normalizedString(value.normalized, "configuration.RouteTableType"),
  ].some((routeTableType) => routeTableType.toLowerCase() === "system");
}

function isSystemDisk(value: Asset | undefined) {
  if (value?.identity.native_type !== "ACS::ECS::Disk") return false;
  return [
    normalizedString(value.normalized, "disk_type"),
    normalizedString(value.normalized, "type"),
    normalizedString(value.normalized, "configuration.Type"),
  ].some((diskType) => diskType.toLowerCase() === "system");
}

function normalizedString(
  normalized: Asset["normalized"] | undefined,
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

function normalizedBoolean(
  normalized: Asset["normalized"] | undefined,
  path: string,
) {
  let current: unknown = normalized;
  for (const segment of path.split(".")) {
    if (!current || typeof current !== "object" || Array.isArray(current)) {
      return false;
    }
    current = (current as Record<string, unknown>)[segment];
  }
  return current === true || current === 1 || current === "true";
}

function lifecycleKind(evidence: Record<string, unknown> | undefined) {
  return typeof evidence?.lifecycle_kind === "string"
    ? evidence.lifecycle_kind
    : undefined;
}
