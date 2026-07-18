export function resourceTypeName(
  _resourceKindID: string,
  typeName: string,
  typeNames: Record<string, string> | undefined,
  locale: string,
): string {
  const localized = typeNames?.[locale]?.trim();
  const fallback = typeName.trim();
  return localized || fallback;
}

export function resourceProductName(nativeType: string | undefined): string {
  const [, product] = nativeType?.split("::") ?? [];
  return product?.trim() || "—";
}

export function compareResourceKindOptionsByProduct(
  left: { label: string; tag?: string },
  right: { label: string; tag?: string },
  locale: string,
): number {
  const leftProduct = left.tag?.toLocaleUpperCase("en-US") ?? "";
  const rightProduct = right.tag?.toLocaleUpperCase("en-US") ?? "";
  if (leftProduct < rightProduct) return -1;
  if (leftProduct > rightProduct) return 1;
  return left.label.localeCompare(right.label, locale);
}
