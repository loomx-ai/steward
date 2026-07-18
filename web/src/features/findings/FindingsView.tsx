import { useEffect, useState } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
import { listFindings } from "@/api/client";
import type { Finding } from "@/api/types";
import { useWorkspace } from "@/app/WorkspaceContext";
import { AsyncState } from "@/components/domain/AsyncState";
import { CursorPagination } from "@/components/domain/CursorPagination";
import { DataTableShell } from "@/components/domain/DataTableShell";
import { JsonViewer } from "@/components/domain/JsonViewer";
import { StateBadge } from "@/components/domain/StateBadge";
import { PageLayout } from "@/components/patterns/PageLayout";
import { Button } from "@/components/ui/button";
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

export function FindingsView() {
  const navigate = useNavigate();
  const connection = useRequiredConnection();
  const { openInspector, closeInspector } = useWorkspace();
  const { formatDate, formatError, label, t } = useLocale();
  const [focused, setFocused] = useState<Finding>();
  const pagination = useCursorPagination(connection.id);
  const findings = useQuery({
    queryKey: [
      "findings",
      connection.id,
      pagination.cursor,
      pagination.pageSize,
    ],
    queryFn: () =>
      listFindings(connection.id, pagination.cursor, pagination.pageSize),
    placeholderData: keepPreviousData,
  });
  const rows = findings.data?.items ?? [];

  const inspectFinding = (finding: Finding, trigger: HTMLElement) => {
    trigger.focus();
    setFocused(finding);
    openInspector({
      title: finding.title || finding.rule_id,
      description: finding.description,
      body: (
        <div className="space-y-4">
          <div className="flex flex-wrap gap-2">
            <StateBadge
              value={finding.severity}
              label={label(finding.severity)}
            />
            <StateBadge value={finding.status} label={label(finding.status)} />
          </div>
          <dl className="space-y-3 text-sm">
            <Fact label={t("findings.asset")} value={finding.asset_id} mono />
            <Fact label={t("findings.ruleSpec")} value={finding.rule_id} />
            <Fact
              label={t("common.lastSeen")}
              value={formatDate(finding.last_seen_at)}
            />
          </dl>
          <JsonViewer
            value={finding.evidence}
            labels={{ show: t("common.showJson"), hide: t("common.hideJson") }}
          />
          <Button
            className="w-full"
            onClick={() => navigate(`/assets/${finding.asset_id}`)}
          >
            {t("assets.openDetails")}
          </Button>
        </div>
      ),
    });
  };

  useEffect(() => {
    setFocused(undefined);
    closeInspector({ restoreFocus: false });
  }, [closeInspector, pagination.cursor]);

  return (
    <PageLayout mode="list">
      <AsyncState
        pending={findings.isPending}
        error={findings.error}
        empty={!findings.isPending && rows.length === 0}
        onRetry={() => void findings.refetch()}
        formatError={formatError}
        labels={{
          empty: t("findings.empty"),
          retry: t("shell.retry"),
          failed: t("shell.routeFailure"),
          loading: t("findings.loading"),
        }}
      >
        <DataTableShell
          pagination={
            <CursorPagination
              page={pagination.page}
              pageCount={pagination.pageCount}
              hasNextPage={Boolean(findings.data?.next_cursor)}
              pending={findings.isFetching}
              pageSize={pagination.pageSize}
              onPrevious={pagination.goPrevious}
              onNext={() => pagination.goNext(findings.data?.next_cursor ?? "")}
              onPageSelect={pagination.goToPage}
              onPageSizeChange={pagination.setPageSize}
              labels={{
                page: (page) => t("common.page", { page }),
                pageSize: t("common.pageSize"),
                previous: t("common.previous"),
                next: t("common.next"),
              }}
            />
          }
        >
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("findings.finding")}</TableHead>
                <TableHead>{t("findings.asset")}</TableHead>
                <TableHead>{t("findings.severity")}</TableHead>
                <TableHead>{t("common.status")}</TableHead>
                <TableHead>{t("findings.ruleSpec")}</TableHead>
                <TableHead>{t("common.lastSeen")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((finding) => (
                <TableRow
                  key={finding.id}
                  aria-selected={focused?.id === finding.id}
                  className="group cursor-pointer"
                  onClick={(event) => {
                    const trigger =
                      event.currentTarget.querySelector<HTMLElement>(
                        "[data-row-inspector-trigger]",
                      );
                    if (trigger) inspectFinding(finding, trigger);
                  }}
                >
                  <TableCell className="max-w-sm">
                    <Button
                      variant="link"
                      className="block max-w-full truncate text-left font-semibold"
                      data-row-inspector-trigger
                      aria-label={`${t("common.details")}: ${finding.title || finding.rule_id}`}
                      onClick={(event) => {
                        event.stopPropagation();
                        inspectFinding(finding, event.currentTarget);
                      }}
                    >
                      {finding.title || finding.rule_id}
                    </Button>
                    <span className="mt-0.5 block truncate text-xs text-muted-foreground">
                      {finding.description}
                    </span>
                  </TableCell>
                  <TableCell>
                    <Button
                      asChild
                      variant="link"
                      className="h-auto p-0 font-mono text-xs"
                    >
                      <Link
                        to={`/assets/${finding.asset_id}`}
                        onClick={(event) => event.stopPropagation()}
                      >
                        {finding.asset_id}
                      </Link>
                    </Button>
                  </TableCell>
                  <TableCell>
                    <StateBadge
                      value={finding.severity}
                      label={label(finding.severity)}
                    />
                  </TableCell>
                  <TableCell>
                    <StateBadge
                      value={finding.status}
                      label={label(finding.status)}
                    />
                  </TableCell>
                  <TableCell>
                    {finding.rule_id}
                    <span className="block font-mono text-[11px] text-muted-foreground">
                      {finding.spec_bundle_revision}
                    </span>
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    {formatDate(finding.last_seen_at)}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </DataTableShell>
      </AsyncState>
    </PageLayout>
  );
}

function Fact({
  label,
  value,
  mono,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  return (
    <div>
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className={`mt-1 break-all ${mono ? "font-mono text-xs" : ""}`}>
        {value}
      </dd>
    </div>
  );
}
