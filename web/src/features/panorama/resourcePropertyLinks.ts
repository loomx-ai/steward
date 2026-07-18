import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { findAssetsByNativeIDs } from "@/api/client";
import type { Asset } from "@/api/types";
import type { ResourcePropertyRow } from "./resourceProperties";

export interface ResourcePropertyLink {
  assetId: string;
  name: string;
  nativeType: string;
}

export function useResourcePropertyLinks(
  asset: Asset | undefined,
  rows: ResourcePropertyRow[],
): ReadonlyMap<string, ResourcePropertyLink> {
  const nativeIDs = useMemo(
    () => resourcePropertyReferenceValues(rows, asset?.identity.native_id),
    [asset?.identity.native_id, rows],
  );
  const references = useQuery({
    queryKey: [
      "resource-property-links",
      asset?.identity.connection_id,
      nativeIDs,
    ],
    queryFn: ({ signal }) =>
      findAssetsByNativeIDs(asset!.identity.connection_id, nativeIDs, signal),
    enabled: Boolean(asset?.identity.connection_id) && nativeIDs.length > 0,
  });

  return useMemo(() => {
    const result = new Map<string, ResourcePropertyLink>();
    for (const related of references.data ?? []) {
      const nativeID = related.identity.native_id.trim();
      if (!nativeID || related.id === asset?.id || result.has(nativeID)) {
        continue;
      }
      result.set(nativeID, {
        assetId: related.id,
        name: related.name?.trim() || nativeID,
        nativeType: related.identity.native_type,
      });
    }
    return result;
  }, [asset?.id, references.data]);
}

export function resourcePropertyReferenceValues(
  rows: ResourcePropertyRow[],
  currentNativeID = "",
): string[] {
  const result = new Set<string>();
  for (const row of rows) {
    collectReferenceValues(row.value, [row.path], result);
  }
  result.delete(currentNativeID.trim());
  return [...result].sort();
}

function collectReferenceValues(
  value: unknown,
  path: string[],
  result: Set<string>,
) {
  if (Array.isArray(value)) {
    for (const item of value) {
      collectReferenceValues(item, path, result);
    }
    return;
  }
  if (isRecord(value)) {
    for (const [key, child] of Object.entries(value)) {
      collectReferenceValues(child, [...path, key], result);
    }
    return;
  }
  if (
    (typeof value !== "string" && typeof value !== "number") ||
    !isReferencePath(path)
  ) {
    return;
  }
  const text = String(value).trim();
  if (
    text.length > 0 &&
    text.length <= 256 &&
    !text.includes("\n") &&
    !text.includes("\r")
  ) {
    result.add(text);
  }
}

function isReferencePath(path: string[]): boolean {
  return path.some((segment) => {
    const token = segment
      .split(".")
      .at(-1)!
      .toLocaleLowerCase("en-US")
      .replaceAll(/[^a-z0-9]/g, "");
    return (
      token.endsWith("id") ||
      token.endsWith("ids") ||
      token.endsWith("name") ||
      token.endsWith("names") ||
      token.endsWith("arn") ||
      token.endsWith("arns")
    );
  });
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}
