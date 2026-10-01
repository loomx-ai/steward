import { useEffect, useMemo, useRef, useState } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { Search } from "lucide-react";
import { Link } from "react-router-dom";
import { listProviderCatalog, listScanChanges } from "@/api/client";
import type {
  AssetChange,
  AssetChangeType,
  ChangeCounts,
  FieldChange,
} from "@/api/types";
import { AsyncState } from "@/components/domain/AsyncState";
import { CursorPagination } from "@/components/domain/CursorPagination";
import { resourceTypeName } from "@/components/domain/resourceKindLabel";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useCursorPagination } from "@/hooks/useCursorPagination";
import { useLocale } from "@/i18n/LocaleProvider";
import { cn } from "@/lib/utils";

type Filter = "all" | AssetChangeType;

const filters: Filter[] = ["all", "added", "removed", "modified"];

const typeTone: Record<AssetChangeType, string> = {
  added: "text-success",
  removed: "text-destructive",
  modified: "text-warning-foreground",
};

export function ScanChanges({
  connectionID,
  scanID,
  provider,
  counts,
}: {
  connectionID: string;
  scanID: string;
  provider: string;
  counts: ChangeCounts;
}) {
  const { formatError, formatNumber, locale, t } = useLocale();
  const [filter, setFilter] = useState<Filter>("all");
  const [search, setSearch] = useState("");
  const [query, setQuery] = useState("");
  const sectionRef = useRef<HTMLElement | null>(null);
  useEffect(() => {
    const timer = window.setTimeout(() => setQuery(search), 300);
    return () => window.clearTimeout(timer);
  }, [search]);
  useEffect(() => {
    // Scroll only the content pane; scrolling the window would shift the
    // whole application shell.
    const section = sectionRef.current;
    const pane = section?.closest("main");
    if (window.location.hash === "#changes" && section && pane) {
      pane.scrollTop +=
        section.getBoundingClientRect().top -
        pane.getBoundingClientRect().top -
        16;
    }
  }, []);
  const pagination = useCursorPagination(`${scanID}:${filter}:${query}`);
  const changes = useQuery({
    queryKey: [
      "scan-changes",
      connectionID,
      scanID,
      filter,
      query,
      pagination.cursor,
      pagination.pageSize,
    ],
    queryFn: () =>
      listScanChanges(connectionID, scanID, {
        type: filter === "all" ? undefined : filter,
        query,
        cursor: pagination.cursor,
        limit: pagination.pageSize,
      }),
    placeholderData: keepPreviousData,
  });
  const catalog = useQuery({
    queryKey: ["catalog"],
    queryFn: listProviderCatalog,
  });
  const kindNames = useMemo(() => {
    const names = new Map<string, string>();
    for (const bundle of catalog.data ?? []) {
      if (bundle.provider !== provider) continue;
      for (const kind of bundle.kinds) {
        names.set(
          kind.id,
          resourceTypeName(
            kind.native_type,
            kind.display_name || kind.native_type,
            kind.display_names,
            locale,
          ),
        );
      }
    }
    return names;
  }, [catalog.data, locale, provider]);
  const total = counts.added + counts.removed + counts.modified;
  const countFor = (value: Filter) => (value === "all" ? total : counts[value]);
  const rows = changes.data?.items ?? [];
  return (
    <section
      id="changes"
      ref={sectionRef}
      className="scroll-mt-16 space-y-3"
      aria-label={t("changes.title")}
    >
      <h2 className="text-sm font-semibold">{t("changes.title")}</h2>
      <div className="flex flex-wrap items-center gap-2">
        <div
          role="group"
          aria-label={t("changes.type")}
          className="inline-flex flex-wrap gap-1 rounded-lg bg-muted p-1"
        >
          {filters.map((value) => (
            <button
              key={value}
              type="button"
              aria-pressed={filter === value}
              className={cn(
                "h-7 rounded-md px-2.5 text-xs text-muted-foreground",
                filter === value &&
                  "bg-background font-medium text-foreground shadow-xs",
              )}
              onClick={() => setFilter(value)}
            >
              {t(`changes.${value}`)}{" "}
              <span className="tabular-nums">
                {formatNumber(countFor(value))}
              </span>
            </button>
          ))}
        </div>
        {total > 0 && (
          <div className="relative min-w-56 flex-1 sm:max-w-sm">
            <Search
              aria-hidden="true"
              className="absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground"
            />
            <Input
              className="h-9 pl-9"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder={t("changes.search")}
              aria-label={t("changes.search")}
            />
          </div>
        )}
      </div>
      <AsyncState
        pending={changes.isPending}
        error={changes.error}
        empty={!changes.isPending && rows.length === 0}
        onRetry={() => void changes.refetch()}
        formatError={formatError}
        labels={{
          empty: total === 0 ? t("changes.empty") : t("changes.noMatches"),
          retry: t("shell.retry"),
          failed: t("shell.routeFailure"),
          loading: t("changes.loading"),
        }}
      >
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-20">{t("changes.type")}</TableHead>
              <TableHead>{t("changes.resource")}</TableHead>
              <TableHead>{t("common.resourceKind")}</TableHead>
              <TableHead>{t("changes.fields")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((change) => (
              <TableRow key={change.id}>
                <TableCell
                  className={cn(
                    "text-xs font-medium",
                    typeTone[change.change_type],
                  )}
                >
                  {t(`changes.${change.change_type}`)}
                </TableCell>
                <TableCell className="max-w-72">
                  <Link
                    to={`/assets/${encodeURIComponent(change.asset_id)}`}
                    className="block truncate text-info underline-offset-4 hover:underline"
                  >
                    {change.name || change.native_id}
                  </Link>
                  <span className="block truncate font-mono text-xs text-muted-foreground">
                    {change.native_id}
                    {change.location ? ` · ${change.location}` : ""}
                  </span>
                </TableCell>
                <TableCell className="text-sm">
                  {kindNames.get(change.resource_kind_id) ?? change.native_type}
                </TableCell>
                <TableCell className="max-w-[28rem]">
                  <FieldChanges change={change} />
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </AsyncState>
      {rows.length > 0 && (
        <CursorPagination
          page={pagination.page}
          pageCount={pagination.pageCount}
          hasNextPage={Boolean(changes.data?.next_cursor)}
          pending={changes.isFetching}
          pageSize={pagination.pageSize}
          onPrevious={pagination.goPrevious}
          onNext={() => pagination.goNext(changes.data?.next_cursor ?? "")}
          onPageSelect={pagination.goToPage}
          onPageSizeChange={pagination.setPageSize}
          labels={{
            page: (page) => t("common.page", { page }),
            pageSize: t("common.pageSize"),
            previous: t("common.previous"),
            next: t("common.next"),
          }}
        />
      )}
    </section>
  );
}

const collapsedFieldCount = 3;

function FieldChanges({ change }: { change: AssetChange }) {
  const { t } = useLocale();
  const [expanded, setExpanded] = useState(false);
  const fields = change.fields ?? [];
  if (change.change_type !== "modified" || fields.length === 0) {
    return <span className="text-muted-foreground">—</span>;
  }
  const visible = expanded ? fields : fields.slice(0, collapsedFieldCount);
  return (
    <div className="space-y-1">
      <ul className="space-y-0.5 text-xs">
        {visible.map((field) => (
          <li key={field.path} className="break-words">
            <span className="font-mono text-muted-foreground">
              {field.path}
            </span>{" "}
            <FieldValue value={field.before} className="line-through" />
            {" → "}
            <FieldValue value={field.after} />
          </li>
        ))}
      </ul>
      {fields.length > collapsedFieldCount && (
        <Button
          type="button"
          variant="link"
          size="sm"
          className="h-auto p-0 text-xs"
          aria-expanded={expanded}
          onClick={() => setExpanded((current) => !current)}
        >
          {expanded
            ? t("changes.fewerFields")
            : t("changes.moreFields", {
                count: fields.length - collapsedFieldCount,
              })}
        </Button>
      )}
    </div>
  );
}

function FieldValue({
  value,
  className,
}: {
  value: FieldChange["before"];
  className?: string;
}) {
  const { t } = useLocale();
  if (value === null || value === undefined || value === "") {
    return (
      <span className="text-muted-foreground">{t("changes.emptyValue")}</span>
    );
  }
  const text = typeof value === "string" ? value : JSON.stringify(value);
  return <span className={cn("font-mono", className)}>{text}</span>;
}
