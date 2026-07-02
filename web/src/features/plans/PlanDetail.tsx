import { Download } from "lucide-react";
import type { Copy } from "../../lib/i18n";
import type { PlanDetail as PlanDetailData } from "../../lib/api";
import { StatusBadge } from "../../components/StatusBadge";
import { RiskBadge } from "../../components/RiskBadge";
import {
  actionLabel,
  formatDate,
  formatSavings,
  statusLabel,
} from "../../lib/format";

export function PlanDetail({
  detail,
  copy,
}: {
  detail: PlanDetailData | null;
  copy: Copy;
}) {
  if (!detail) {
    return (
      <aside className="detail-panel">
        <h3>{copy.sections.planDetail}</h3>
        <div className="empty">{copy.empty.noPlanSelected}</div>
      </aside>
    );
  }
  const { plan, items } = detail;
  return (
    <aside className="detail-panel">
      <div className="detail-heading">
        <h3>{copy.sections.planDetail}</h3>
        <div className="export-links">
          <a
            href={`/api/plans/${plan.id}/export?format=markdown`}
            title={copy.actions.exportMarkdown}
          >
            <Download size={14} />
            <span>MD</span>
          </a>
          <a
            href={`/api/plans/${plan.id}/export?format=json`}
            title={copy.actions.exportJSON}
          >
            <Download size={14} />
            <span>JSON</span>
          </a>
        </div>
      </div>

      <div className="timeline">
        <div className="timeline-item">
          <span className="timeline-dot" />
          <div>
            <div className="font-medium">{plan.created_by}</div>
            <div className="text-xs text-slate-500">
              {formatDate(plan.created_at, copy)}
            </div>
          </div>
        </div>
        {plan.approved_by && (
          <div className="timeline-item">
            <span className="timeline-dot" />
            <div>
              <div className="font-medium">
                {plan.approved_by}
                {plan.approval_comment ? ` · ${plan.approval_comment}` : ""}
              </div>
              <div className="text-xs text-slate-500">
                {plan.approved_at ? formatDate(plan.approved_at, copy) : "-"}
              </div>
            </div>
          </div>
        )}
        {plan.executed_at && (
          <div className="timeline-item">
            <span className="timeline-dot" />
            <div>
              <div className="font-medium">
                <StatusBadge value={plan.status} copy={copy} />
              </div>
              <div className="text-xs text-slate-500">
                {formatDate(plan.executed_at, copy)}
              </div>
            </div>
          </div>
        )}
      </div>

      <div className="plan-items">
        {items.map((item) => (
          <div key={item.id} className="plan-item">
            <div className="font-medium">{item.resource.name}</div>
            <div className="text-xs text-slate-500">
              {actionLabel(copy, item.action)} ·{" "}
              <RiskBadge value={item.risk} copy={copy} /> ·{" "}
              {formatSavings(item.estimated_monthly_savings, copy)}
            </div>
            <div className="text-xs text-slate-500">
              {item.blocked
                ? `${copy.labels.blocked}: ${item.block_reason || "-"}`
                : statusLabel(copy, item.result || copy.labels.pending)}
              {item.request_id ? ` · ${item.request_id}` : ""}
            </div>
          </div>
        ))}
      </div>
    </aside>
  );
}
