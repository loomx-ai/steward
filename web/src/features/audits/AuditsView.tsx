import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { Search, X } from "lucide-react";
import { Link } from "react-router-dom";
import { findAssets, listAuditEvents } from "@/api/client";
import type { Asset, AuditEvent } from "@/api/types";
import { AsyncState } from "@/components/domain/AsyncState";
import { CursorPagination } from "@/components/domain/CursorPagination";
import { DataTableShell } from "@/components/domain/DataTableShell";
import { StateBadge } from "@/components/domain/StateBadge";
import { PageLayout } from "@/components/patterns/PageLayout";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useRequiredConnection } from "@/connections/ActiveConnectionProvider";
import { useCursorPagination } from "@/hooks/useCursorPagination";
import { useLocale } from "@/i18n/LocaleProvider";
import type { MessageKey } from "@/i18n/messages";

type Translate = (
  key: MessageKey,
  values?: Record<string, string | number>,
) => string;

interface AuditFact {
  key: string;
  label: string;
  value: string;
  mono?: boolean;
}

export function filterAuditEvents(
  events: AuditEvent[],
  search: string,
  localizedLabel: (value: string) => string = (value) => value,
) {
  const term = search.trim().toLocaleLowerCase();
  if (!term) return events;
  return events.filter((event) =>
    [
      event.id,
      event.actor,
      localizedLabel(event.actor),
      event.action,
      localizedLabel(event.action),
      event.target_type,
      localizedLabel(event.target_type),
      event.target_id,
      event.result,
      localizedLabel(event.result),
      event.request_id,
      ...evidenceSearchValues(event.evidence),
    ].some((value) => value?.toLocaleLowerCase().includes(term)),
  );
}

export function sortAuditEventsNewestFirst(events: AuditEvent[]) {
  return [...events].sort(
    (left, right) =>
      right.created_at.localeCompare(left.created_at) ||
      right.id.localeCompare(left.id),
  );
}

export function auditTargetPath(event: AuditEvent) {
  switch (event.target_type) {
    case "cloud_connection":
    case "connection_region":
      return "/settings?section=connections";
    case "scan_run":
    case "scan_task":
      return `/scans/${encodeURIComponent(event.target_id)}`;
    case "cleanup_task":
      return `/cleanup/${encodeURIComponent(event.target_id)}`;
    case "execution_attempt": {
      const cleanupTaskID = stringValue(event.evidence?.cleanup_task_id);
      return cleanupTaskID
        ? `/cleanup/${encodeURIComponent(cleanupTaskID)}`
        : undefined;
    }
    case "asset":
      return `/assets/${encodeURIComponent(event.target_id)}`;
    default:
      return undefined;
  }
}

export function AuditsView() {
  const { formatDate, formatError, label, t } = useLocale();
  const connection = useRequiredConnection();
  const [focused, setFocused] = useState<AuditEvent>();
  const dialogTriggerRef = useRef<HTMLElement | null>(null);
  const [search, setSearch] = useState("");
  const pagination = useCursorPagination(`${connection.id}:${search}`);
  const audits = useQuery({
    queryKey: [
      "audit-events",
      connection.id,
      pagination.cursor,
      pagination.pageSize,
    ],
    queryFn: () =>
      listAuditEvents(connection.id, pagination.cursor, pagination.pageSize),
    placeholderData: keepPreviousData,
  });
  const sourceRows = audits.data?.items ?? [];
  const assetTargetIDs = useMemo(
    () =>
      sourceRows
        .filter((event) => event.target_type === "asset")
        .map((event) => event.target_id),
    [sourceRows],
  );
  const targetAssets = useQuery({
    queryKey: ["audit-target-assets", connection.id, assetTargetIDs],
    queryFn: () => findAssets(connection.id, assetTargetIDs),
    enabled: assetTargetIDs.length > 0,
  });
  const assetsByID = useMemo(
    () => new Map((targetAssets.data ?? []).map((asset) => [asset.id, asset])),
    [targetAssets.data],
  );
  const rows = useMemo(
    () =>
      filterAuditEvents(sortAuditEventsNewestFirst(sourceRows), search, label),
    [label, search, sourceRows],
  );
  const hasSearch = search.trim().length > 0;

  const inspectAudit = (audit: AuditEvent, trigger: HTMLElement) => {
    trigger.focus();
    dialogTriggerRef.current = trigger;
    setFocused(audit);
  };

  const closeAudit = () => {
    const trigger = dialogTriggerRef.current;
    dialogTriggerRef.current = null;
    setFocused(undefined);
    queueMicrotask(() => trigger?.isConnected && trigger.focus());
  };

  useEffect(() => {
    dialogTriggerRef.current = null;
    setFocused(undefined);
  }, [pagination.cursor]);

  return (
    <PageLayout mode="list" className="pt-0">
      <DataTableShell
        toolbar={
          <div className="relative min-w-64 flex-1 self-start md:max-w-xl">
            <Search
              aria-hidden="true"
              className="absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground"
            />
            <Input
              className="h-11 pl-9 sm:h-9"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder={t("audit.search")}
              aria-label={t("audit.search")}
            />
          </div>
        }
        pagination={
          sourceRows.length > 0 ? (
            <CursorPagination
              page={pagination.page}
              pageCount={pagination.pageCount}
              hasNextPage={Boolean(audits.data?.next_cursor)}
              pending={audits.isFetching}
              pageSize={pagination.pageSize}
              onPrevious={pagination.goPrevious}
              onNext={() => pagination.goNext(audits.data?.next_cursor ?? "")}
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
          pending={audits.isPending}
          error={audits.error}
          empty={!audits.isPending && rows.length === 0}
          onRetry={() => void audits.refetch()}
          formatError={formatError}
          labels={{
            empty: hasSearch ? t("audit.noMatches") : t("audit.empty"),
            retry: t("shell.retry"),
            failed: t("shell.routeFailure"),
            loading: t("audit.loading"),
          }}
        >
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("common.time")}</TableHead>
                <TableHead>{t("common.actor")}</TableHead>
                <TableHead>{t("common.action")}</TableHead>
                <TableHead>{t("common.target")}</TableHead>
                <TableHead>{t("common.result")}</TableHead>
                <TableHead>{t("audit.requestId")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((event) => {
                const targetPath = auditTargetPath(event);
                const asset =
                  event.target_type === "asset"
                    ? assetsByID.get(event.target_id)
                    : undefined;
                const target = asset ? (
                  <ResourceAuditTarget asset={asset} targetPath={targetPath!} />
                ) : (
                  <>
                    <span>{label(event.target_type)}</span>
                    <span className="block font-mono text-[11px] text-muted-foreground">
                      {event.target_id}
                    </span>
                  </>
                );
                return (
                  <TableRow key={event.id}>
                    <TableCell className="text-xs text-muted-foreground">
                      {formatDate(event.created_at)}
                    </TableCell>
                    <TableCell className="font-medium">
                      {event.actor === "system"
                        ? label(event.actor)
                        : event.actor}
                    </TableCell>
                    <TableCell>
                      <Button
                        variant="link"
                        data-row-inspector-trigger
                        aria-label={`${t("common.details")}: ${label(event.action)} ${event.target_id}`}
                        onClick={(clickEvent) => {
                          inspectAudit(event, clickEvent.currentTarget);
                        }}
                      >
                        {label(event.action)}
                      </Button>
                    </TableCell>
                    <TableCell>
                      {asset ? (
                        target
                      ) : targetPath ? (
                        <Button
                          asChild
                          variant="link"
                          className="h-auto min-h-11 flex-col items-start p-0 text-left sm:min-h-0"
                        >
                          <Link to={targetPath}>{target}</Link>
                        </Button>
                      ) : (
                        target
                      )}
                    </TableCell>
                    <TableCell>
                      <StateBadge
                        value={event.result}
                        label={label(event.result)}
                      />
                    </TableCell>
                    <TableCell className="font-mono text-xs">
                      {event.request_id || "—"}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </AsyncState>
      </DataTableShell>
      <AuditDetailDialog
        audit={focused}
        onOpenChange={(open) => !open && closeAudit()}
      />
    </PageLayout>
  );
}

function ResourceAuditTarget({
  asset,
  targetPath,
}: {
  asset: Asset;
  targetPath: string;
}) {
  const name = asset.name || asset.identity.native_id;

  return (
    <Link
      to={targetPath}
      className="block min-w-0 text-left underline-offset-4 hover:underline focus-visible:rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <span className="block truncate font-medium text-info">{name}</span>
      <span className="mt-0.5 block truncate font-mono text-[11px] text-muted-foreground">
        {asset.identity.native_id}
      </span>
    </Link>
  );
}

function AuditDetailDialog({
  audit,
  onOpenChange,
}: {
  audit?: AuditEvent;
  onOpenChange: (open: boolean) => void;
}) {
  const { formatDate, formatNumber, label, t } = useLocale();
  if (!audit) return null;
  const targetPath = auditTargetPath(audit);
  const details = auditDetailFacts(audit, t, label, formatNumber);

  return (
    <Dialog open onOpenChange={onOpenChange}>
      <DialogContent
        showCloseButton={false}
        className="max-h-[min(42rem,calc(100dvh-2rem))] grid-rows-[auto_minmax(0,1fr)] gap-0 overflow-hidden p-0 sm:max-w-xl"
      >
        <DialogHeader className="border-b px-5 py-4 pr-14 text-left">
          <div className="flex min-w-0 items-center gap-3">
            <DialogTitle>{label(audit.action)}</DialogTitle>
            <StateBadge value={audit.result} label={label(audit.result)} />
          </div>
          <DialogDescription className="sr-only">
            {label(audit.target_type)} · {audit.target_id}
          </DialogDescription>
        </DialogHeader>
        <DialogClose asChild>
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="absolute top-3 right-3"
            aria-label={t("audit.closeDetails")}
          >
            <X />
          </Button>
        </DialogClose>
        <div className="min-h-0 overflow-y-auto p-5">
          <div className="space-y-5">
            <dl className="space-y-3 text-sm">
              <Fact
                label={t("common.time")}
                value={formatDate(audit.created_at)}
              />
              <Fact
                label={t("common.actor")}
                value={
                  audit.actor === "system" ? label(audit.actor) : audit.actor
                }
              />
              <Fact
                label={t("common.target")}
                value={
                  targetPath ? (
                    <Link
                      className="font-medium text-primary underline-offset-4 hover:underline focus-visible:rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                      to={targetPath}
                    >
                      {label(audit.target_type)} · {audit.target_id}
                    </Link>
                  ) : (
                    `${label(audit.target_type)} · ${audit.target_id}`
                  )
                }
              />
              <Fact
                label={t("audit.requestId")}
                value={audit.request_id || "—"}
                mono
              />
            </dl>
            {details.length > 0 && (
              <section className="space-y-3 border-t pt-4">
                <h3 className="text-sm font-medium">
                  {t("audit.actionDetails")}
                </h3>
                <dl className="space-y-3 text-sm">
                  {details.map((fact) => (
                    <Fact
                      key={fact.key}
                      label={fact.label}
                      value={fact.value}
                      mono={fact.mono}
                    />
                  ))}
                </dl>
              </section>
            )}
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function auditDetailFacts(
  event: AuditEvent,
  t: Translate,
  label: (value: string) => string,
  formatNumber: (value: number) => string,
) {
  const evidence = event.evidence ?? {};
  const facts: AuditFact[] = [];
  const addString = (
    key: string,
    message: MessageKey,
    options: { translated?: boolean; mono?: boolean } = {},
  ) => {
    const value = stringValue(evidence[key]);
    if (!value) return;
    facts.push({
      key,
      label: t(message),
      value: options.translated ? label(value) : value,
      mono: options.mono,
    });
  };
  const addNumber = (key: string, message: MessageKey) => {
    const value = numberValue(evidence[key]);
    if (value === undefined) return;
    facts.push({ key, label: t(message), value: formatNumber(value) });
  };
  const addCount = (key: string, message: MessageKey) => {
    const value = evidence[key];
    if (!Array.isArray(value)) return;
    facts.push({ key, label: t(message), value: formatNumber(value.length) });
  };

  addString("provider", "common.provider", { translated: true });
  addString("credential_type", "audit.credentialType", { translated: true });
  addNumber("root_scope_count", "audit.rootScopeCount");
  addString("error_category", "audit.errorCategory", { translated: true });
  addString("error_code", "audit.errorCode", { mono: true });
  addString("error_message", "audit.errorMessage");
  addNumber("added", "audit.addedCount");
  addNumber("updated", "audit.updatedCount");
  addNumber("missing", "audit.missingCount");
  addNumber("active", "audit.activeCount");
  addNumber("retired", "audit.retiredCount");
  addNumber("excluded", "audit.excludedCount");
  addString("old_name_override", "audit.previousName");
  if (Object.hasOwn(evidence, "new_name_override")) {
    facts.push({
      key: "new_name_override",
      label: t("audit.newName"),
      value: stringValue(evidence.new_name_override) || t("audit.defaultName"),
    });
  }
  addString("old_lifecycle", "audit.previousStatus", { translated: true });
  addString("new_lifecycle", "audit.newStatus", { translated: true });
  addNumber("target_count", "audit.targetCount");
  addNumber("region_count", "audit.regionCount");
  addNumber("shard_count", "audit.shardCount");
  addString("scope_mode", "audit.scopeMode", { translated: true });
  addCount("selectors", "audit.selectorCount");
  addCount("resolved_asset_ids", "audit.resourceCount");
  addCount("blockers", "audit.blockerCount");
  addCount("warnings", "audit.warningCount");
  addString("cleanup_task_id", "cleanup.taskId", { mono: true });
  addString("execution_id", "executions.id", { mono: true });
  addString("provider_operation_id", "audit.providerOperationId", {
    mono: true,
  });

  const coverage = recordValue(evidence.scan_coverage);
  const coverageStatus = stringValue(coverage?.status);
  if (coverageStatus) {
    facts.push({
      key: "scan_coverage",
      label: t("audit.scanCoverage"),
      value: label(coverageStatus),
    });
  }
  const confirmation = recordValue(evidence.confirmation);
  if (typeof confirmation?.acknowledged === "boolean") {
    facts.push({
      key: "confirmation.acknowledged",
      label: t("audit.confirmed"),
      value: t(confirmation.acknowledged ? "common.yes" : "common.no"),
    });
  }

  const providerError = recordValue(evidence.provider_error);
  const providerErrorCategory = stringValue(providerError?.category);
  const providerErrorCode = stringValue(providerError?.code);
  const providerErrorMessage = stringValue(providerError?.message);
  if (providerErrorCategory && !stringValue(evidence.error_category)) {
    facts.push({
      key: "provider_error.category",
      label: t("audit.errorCategory"),
      value: label(providerErrorCategory),
    });
  }
  if (providerErrorCode && !stringValue(evidence.error_code)) {
    facts.push({
      key: "provider_error.code",
      label: t("audit.errorCode"),
      value: providerErrorCode,
      mono: true,
    });
  }
  if (providerErrorMessage && !stringValue(evidence.error_message)) {
    facts.push({
      key: "provider_error.message",
      label: t("audit.errorMessage"),
      value: providerErrorMessage,
    });
  }

  const providerRequestID =
    stringValue(evidence.provider_request_id) ||
    stringValue(providerError?.request_id) ||
    // Older audit records stored the provider request ID under this key.
    stringValue(evidence.request_id);
  if (providerRequestID) {
    facts.push({
      key: "provider_request_id",
      label: t("common.providerRequestId"),
      value: providerRequestID,
      mono: true,
    });
  }
  return facts;
}

function evidenceSearchValues(value: unknown): string[] {
  if (typeof value === "string" || typeof value === "number") {
    return [String(value)];
  }
  if (typeof value === "boolean") return [value ? "true" : "false"];
  if (Array.isArray(value)) return value.flatMap(evidenceSearchValues);
  const record = recordValue(value);
  return record ? Object.values(record).flatMap(evidenceSearchValues) : [];
}

function recordValue(value: unknown) {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}

function stringValue(value: unknown) {
  return typeof value === "string" ? value.trim() : "";
}

function numberValue(value: unknown) {
  return typeof value === "number" && Number.isFinite(value)
    ? value
    : undefined;
}

function Fact({
  label,
  value,
  mono,
}: {
  label: string;
  value: ReactNode;
  mono?: boolean;
}) {
  return (
    <div className="grid grid-cols-[7rem_minmax(0,1fr)] items-start gap-x-4">
      <dt className="leading-5 text-muted-foreground">{label}</dt>
      <dd
        className={`min-w-0 break-all leading-5 ${mono ? "font-mono text-xs" : ""}`}
      >
        {value}
      </dd>
    </div>
  );
}
