import type { ResourceKind } from "@/api/types";
import { useState, type ReactNode } from "react";
import { ArrowLeft, ChevronRight } from "lucide-react";
import { Link } from "react-router-dom";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { useLocale } from "@/i18n/LocaleProvider";
import type { Locale } from "@/i18n/locales";
import {
  displayBrowserTime,
  displayPropertyValue,
  isTagProperty,
  isTimeProperty,
  isResourcePropertyHidden,
  resourcePropertyLabel,
  tagValues,
} from "./resourceProperties";
import type { ResourcePropertyLink } from "./resourcePropertyLinks";

export function ResourcePropertyValue({
  path,
  value,
  kind,
  resourceLinks,
}: {
  path: string;
  value: unknown;
  kind: ResourceKind | undefined;
  resourceLinks?: ReadonlyMap<string, ResourcePropertyLink>;
}) {
  const { formatDate, locale, t } = useLocale();
  if (isTagProperty(path)) {
    const tags = tagValues(value, locale);
    if (tags.length > 0) {
      return (
        <span className="flex min-w-0 flex-wrap gap-1.5">
          {tags.map((tag) => (
            <Badge
              key={tag.key}
              variant="secondary"
              className="max-w-full whitespace-normal break-all font-normal"
            >
              {tag.label}
            </Badge>
          ))}
        </span>
      );
    }
  }
  if (Array.isArray(value)) {
    return (
      <ArrayPropertyValue
        path={path}
        values={value}
        kind={kind}
        resourceLinks={resourceLinks}
      />
    );
  }
  if (isRecord(value)) {
    const unwrapped = singleArrayValue(value);
    if (unwrapped) {
      return (
        <ArrayPropertyValue
          path={path}
          values={unwrapped}
          kind={kind}
          resourceLinks={resourceLinks}
        />
      );
    }
    return (
      <StructuredPropertyDialog
        path={path}
        count={meaningfulEntries(value).length}
        kind={kind}
      >
        <ObjectPropertyValue
          path={path}
          value={value}
          kind={kind}
          resourceLinks={resourceLinks}
        />
      </StructuredPropertyDialog>
    );
  }
  const boolean = booleanValue(value);
  if (boolean !== undefined) {
    return <span>{t(boolean ? "common.yes" : "common.no")}</span>;
  }
  const localizedTime = isTimeProperty(path)
    ? displayBrowserTime(value, formatDate)
    : undefined;
  if (localizedTime) {
    return (
      <time dateTime={String(value)} title={String(value)}>
        {localizedTime}
      </time>
    );
  }
  if (
    typeof value === "string" &&
    (value.length > 240 || value.includes("\n"))
  ) {
    return <LongTextPropertyValue path={path} value={value} kind={kind} />;
  }
  return <ScalarPropertyValue value={value} resourceLinks={resourceLinks} />;
}

function ArrayPropertyValue({
  path,
  values,
  kind,
  resourceLinks,
}: {
  path: string;
  values: unknown[];
  kind: ResourceKind | undefined;
  resourceLinks?: ReadonlyMap<string, ResourcePropertyLink>;
}) {
  const items = values.filter((value) => !isEmptyValue(value));
  if (items.every(isScalar)) {
    return (
      <span className="flex min-w-0 flex-wrap gap-1.5">
        {items.map((value, index) => {
          const label = displayPropertyValue(value) || "—";
          const resourceLink = linkedResource(value, resourceLinks);
          return resourceLink ? (
            <Badge
              key={`${index}:${label}`}
              variant="secondary"
              asChild
              className="max-w-full whitespace-normal break-all font-normal"
            >
              <Link to={`/assets/${resourceLink.assetId}`}>{label}</Link>
            </Badge>
          ) : (
            <Badge
              key={`${index}:${label}`}
              variant="secondary"
              className="max-w-full whitespace-normal break-all font-normal"
            >
              {label}
            </Badge>
          );
        })}
      </span>
    );
  }
  return (
    <StructuredArrayPropertyDialog
      path={path}
      items={items}
      kind={kind}
      resourceLinks={resourceLinks}
    />
  );
}

function StructuredPropertyList({
  path,
  items,
  kind,
  resourceLinks,
  onSelect,
}: {
  path: string;
  items: unknown[];
  kind: ResourceKind | undefined;
  resourceLinks?: ReadonlyMap<string, ResourcePropertyLink>;
  onSelect: (index: number) => void;
}) {
  const { formatDate, locale, t } = useLocale();
  const label = resourcePropertyLabel(path, kind, locale);
  return (
    <ol className="overflow-hidden rounded-xl border">
      {items.map((value, index) => {
        const itemPath = `${path}.${index}`;
        const itemNumber = index + 1;
        const summary = propertyListItemSummary(
          itemPath,
          value,
          kind,
          locale,
          formatDate,
          (key) => t(key),
        );
        return (
          <li key={index} className="border-b last:border-b-0">
            <Button
              type="button"
              variant="ghost"
              className="grid min-h-11 w-full grid-cols-[2rem_minmax(0,1fr)_auto] items-center gap-3 rounded-none px-3 py-2 text-left font-normal"
              aria-label={t("asset.openPropertyItemDetails", {
                label,
                index: itemNumber,
              })}
              autoFocus={index === 0}
              onClick={() => onSelect(index)}
            >
              <span className="text-xs tabular-nums text-muted-foreground">
                {itemNumber}
              </span>
              <span className="min-w-0 truncate">{summary}</span>
              <ChevronRight
                aria-hidden="true"
                className="size-4 text-muted-foreground"
              />
            </Button>
          </li>
        );
      })}
    </ol>
  );
}

function StructuredArrayPropertyDialog({
  path,
  items,
  kind,
  resourceLinks,
}: {
  path: string;
  items: unknown[];
  kind: ResourceKind | undefined;
  resourceLinks?: ReadonlyMap<string, ResourcePropertyLink>;
}) {
  const { locale, t } = useLocale();
  const [selectedIndex, setSelectedIndex] = useState<number | undefined>();
  const label = resourcePropertyLabel(path, kind, locale);
  const selectedValue =
    selectedIndex === undefined ? undefined : items[selectedIndex];
  const itemNumber =
    selectedIndex === undefined ? undefined : selectedIndex + 1;
  return (
    <Dialog
      onOpenChange={(open) => {
        if (!open) setSelectedIndex(undefined);
      }}
    >
      <DialogTrigger asChild>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-8 min-w-28 justify-between gap-3 font-normal text-muted-foreground"
          aria-label={t("asset.openPropertyDetails", {
            label,
            count: items.length,
          })}
        >
          {t("common.count", { count: items.length })}
          <ChevronRight aria-hidden="true" />
        </Button>
      </DialogTrigger>
      <DialogContent
        aria-describedby={undefined}
        className="max-h-[calc(100dvh-2rem)] grid-rows-[auto_minmax(0,1fr)] sm:max-w-3xl"
      >
        <DialogHeader className="flex-row items-center gap-2 text-left">
          {selectedIndex !== undefined ? (
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              aria-label={t("asset.backToPropertyList", { label })}
              autoFocus
              onClick={() => setSelectedIndex(undefined)}
            >
              <ArrowLeft aria-hidden="true" />
            </Button>
          ) : null}
          <DialogTitle>
            {itemNumber === undefined
              ? label
              : t("asset.propertyItemTitle", { label, index: itemNumber })}
          </DialogTitle>
        </DialogHeader>
        <div className="min-h-0 overflow-y-auto pr-1">
          {selectedIndex === undefined ? (
            <StructuredPropertyList
              path={path}
              items={items}
              kind={kind}
              resourceLinks={resourceLinks}
              onSelect={setSelectedIndex}
            />
          ) : isRecord(selectedValue) ? (
            <ObjectPropertyValue
              path={`${path}.${selectedIndex}`}
              value={selectedValue}
              kind={kind}
              resourceLinks={resourceLinks}
            />
          ) : (
            <ResourcePropertyValue
              path={`${path}.${selectedIndex}`}
              value={selectedValue}
              kind={kind}
              resourceLinks={resourceLinks}
            />
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

function ObjectPropertyValue({
  path,
  value,
  kind,
  resourceLinks,
}: {
  path: string;
  value: Record<string, unknown>;
  kind: ResourceKind | undefined;
  resourceLinks?: ReadonlyMap<string, ResourcePropertyLink>;
}) {
  const { locale } = useLocale();
  const fields = objectPropertyFields(path, value, kind, locale);
  return (
    <dl className="divide-y">
      {fields.map((field) => (
        <div
          key={field.key}
          className="grid gap-1 py-2 first:pt-0 last:pb-0 sm:grid-cols-[minmax(8rem,0.7fr)_minmax(0,1.3fr)] sm:gap-3"
        >
          <dt className="text-xs text-muted-foreground">{field.label}</dt>
          <dd className="min-w-0 break-all text-sm">
            <ResourcePropertyValue
              path={field.path}
              value={field.value}
              kind={kind}
              resourceLinks={resourceLinks}
            />
          </dd>
        </div>
      ))}
    </dl>
  );
}

interface ObjectPropertyField {
  key: string;
  path: string;
  label: string;
  value: unknown;
}

function objectPropertyFields(
  path: string,
  value: Record<string, unknown>,
  kind: ResourceKind | undefined,
  locale: Locale,
): ObjectPropertyField[] {
  return meaningfulEntries(value)
    .filter(([key]) => !isResourcePropertyHidden(`${path}.${key}`))
    .map(([key, child]) => {
      const childPath = `${path}.${key}`;
      return {
        key,
        path: childPath,
        label: resourcePropertyLabel(childPath, kind, locale),
        value: child,
      };
    })
    .sort((left, right) => left.label.localeCompare(right.label, locale));
}

function propertyListItemSummary(
  path: string,
  value: unknown,
  kind: ResourceKind | undefined,
  locale: Locale,
  formatDate: (value: string | Date) => string,
  translate: (key: "common.yes" | "common.no" | "common.details") => string,
): string {
  if (!isRecord(value)) {
    return displayPropertyValue(value) || translate("common.details");
  }
  const seen = new Set<string>();
  const parts = objectPropertyFields(path, value, kind, locale)
    .filter((field) => isScalar(field.value))
    .sort((left, right) => {
      const priority =
        listSummaryPriority(left.key) - listSummaryPriority(right.key);
      return priority || left.label.localeCompare(right.label, locale);
    })
    .map((field) => {
      const boolean = booleanValue(field.value);
      if (boolean !== undefined) {
        return translate(boolean ? "common.yes" : "common.no");
      }
      if (isTimeProperty(field.path)) {
        return displayBrowserTime(field.value, formatDate);
      }
      return displayPropertyValue(field.value);
    })
    .filter((part): part is string => {
      if (!part || seen.has(part)) return false;
      seen.add(part);
      return true;
    })
    .slice(0, 3);
  return parts.join(" · ") || translate("common.details");
}

function listSummaryPriority(key: string): number {
  const normalized = key.replace(/[^a-z0-9]/gi, "").toLocaleLowerCase("en-US");
  if (/^(name|title|displayname|rulename|routeentryname)$/.test(normalized)) {
    return 0;
  }
  if (/(ipprotocol|protocol)$/.test(normalized)) return 1;
  if (/port(range)?$/.test(normalized)) return 2;
  if (
    /(destinationcidrblock|sourcecidrip|sourcecidr|cidrblock|cidr)$/.test(
      normalized,
    )
  ) {
    return 3;
  }
  if (/(nexthop|direction|policy|action|type|kind)$/.test(normalized)) {
    return 4;
  }
  if (/description$/.test(normalized)) return 5;
  if (/(id|arn)$/.test(normalized)) return 50;
  if (/(time|date|at)$/.test(normalized)) return 60;
  return 20;
}

function StructuredPropertyDialog({
  path,
  count,
  kind,
  children,
}: {
  path: string;
  count: number;
  kind: ResourceKind | undefined;
  children: ReactNode;
}) {
  const { locale, t } = useLocale();
  const label = resourcePropertyLabel(path, kind, locale);
  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-8 min-w-28 justify-between gap-3 font-normal text-muted-foreground"
          aria-label={t("asset.openPropertyDetails", { label, count })}
        >
          {t("common.count", { count })}
          <ChevronRight aria-hidden="true" />
        </Button>
      </DialogTrigger>
      <DialogContent
        aria-describedby={undefined}
        className="max-h-[calc(100dvh-2rem)] grid-rows-[auto_minmax(0,1fr)] sm:max-w-3xl"
      >
        <DialogHeader>
          <DialogTitle>{label}</DialogTitle>
        </DialogHeader>
        <div className="min-h-0 overflow-y-auto pr-1">{children}</div>
      </DialogContent>
    </Dialog>
  );
}

function LongTextPropertyValue({
  path,
  value,
  kind,
}: {
  path: string;
  value: string;
  kind: ResourceKind | undefined;
}) {
  const { locale, t } = useLocale();
  const label = resourcePropertyLabel(path, kind, locale);
  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-8 min-w-28 justify-between gap-3 font-normal text-muted-foreground"
          aria-label={t("asset.openTextPropertyDetails", { label })}
        >
          {t("common.details")}
          <ChevronRight aria-hidden="true" />
        </Button>
      </DialogTrigger>
      <DialogContent
        aria-describedby={undefined}
        className="max-h-[calc(100dvh-2rem)] grid-rows-[auto_minmax(0,1fr)] sm:max-w-3xl"
      >
        <DialogHeader>
          <DialogTitle>{label}</DialogTitle>
        </DialogHeader>
        <pre className="min-h-0 overflow-auto whitespace-pre-wrap break-all rounded-xl border bg-muted/20 p-4 font-mono text-xs">
          {value}
        </pre>
      </DialogContent>
    </Dialog>
  );
}

function ScalarPropertyValue({
  value,
  resourceLinks,
}: {
  value: unknown;
  resourceLinks?: ReadonlyMap<string, ResourcePropertyLink>;
}) {
  const text = displayPropertyValue(value) || "—";
  const resourceLink = linkedResource(value, resourceLinks);
  return resourceLink ? (
    <Link
      to={`/assets/${resourceLink.assetId}`}
      className="text-primary underline-offset-4 hover:underline focus-visible:rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
    >
      {text}
    </Link>
  ) : (
    <span>{text}</span>
  );
}

function linkedResource(
  value: unknown,
  resourceLinks: ReadonlyMap<string, ResourcePropertyLink> | undefined,
): ResourcePropertyLink | undefined {
  if (
    !resourceLinks ||
    (typeof value !== "string" && typeof value !== "number")
  ) {
    return undefined;
  }
  return resourceLinks.get(String(value).trim());
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function singleArrayValue(
  value: Record<string, unknown>,
): unknown[] | undefined {
  const entries = meaningfulEntries(value);
  if (entries.length !== 1 || !Array.isArray(entries[0][1])) return undefined;
  return entries[0][1];
}

function meaningfulEntries(
  value: Record<string, unknown>,
): Array<[string, unknown]> {
  return Object.entries(value).filter(([, child]) => !isEmptyValue(child));
}

function isScalar(value: unknown): boolean {
  return value === null || typeof value !== "object";
}

function booleanValue(value: unknown): boolean | undefined {
  if (typeof value === "boolean") return value;
  if (typeof value !== "string") return undefined;
  const normalized = value.trim().toLocaleLowerCase("en-US");
  if (normalized === "true") return true;
  if (normalized === "false") return false;
  return undefined;
}

function isEmptyValue(value: unknown): boolean {
  if (value === undefined || value === null) return true;
  if (typeof value === "string") return value.trim() === "";
  if (Array.isArray(value)) {
    return value.length === 0 || value.every(isEmptyValue);
  }
  if (isRecord(value)) {
    const values = Object.values(value);
    return values.length === 0 || values.every(isEmptyValue);
  }
  return false;
}
