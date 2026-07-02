import { useState } from "react";
import { useParams, useNavigate, useLocation } from "react-router-dom";
import { CheckCircle2, Zap } from "lucide-react";
import { useShell } from "../../components/AppShell";
import { StatusBadge } from "../../components/StatusBadge";
import { RiskBadge } from "../../components/RiskBadge";
import { EmptyState } from "../../components/EmptyState";
import { Skeleton } from "../../components/Skeleton";
import {
  usePlans,
  usePlan,
  useApprovePlan,
  useExecutePlan,
} from "../../lib/queries";
import { formatDate, formatSavings, shortID } from "../../lib/format";
import type { CleanupPlan } from "../../lib/api";
import { PlanDetail } from "./PlanDetail";
import { PlanActionDialog } from "./PlanActionDialog";

function canApprove(plan: CleanupPlan) {
  return plan.status === "draft" || plan.status === "pending_approval";
}
function canExecute(plan: CleanupPlan) {
  return plan.status === "approved";
}

export function PlansView() {
  const { copy } = useShell();
  const { id } = useParams();
  const navigate = useNavigate();
  const { search } = useLocation();
  const plans = usePlans();
  const detail = usePlan(id);
  const approve = useApprovePlan();
  const execute = useExecutePlan();
  const [dialog, setDialog] = useState<"approve" | "execute" | null>(null);

  const plan = detail.data?.plan;

  return (
    <section className="panel">
      <div className="section-heading">
        <h2 className="panel-title">{copy.sections.cleanupPlans}</h2>
        <span className="muted">
          {plans.data?.length ?? 0} {copy.labels.plans}
        </span>
      </div>
      <div className="grid gap-4 xl:grid-cols-[1fr_380px]">
        {plans.isPending ? (
          <Skeleton rows={5} />
        ) : plans.data && plans.data.length > 0 ? (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>{copy.table.plan}</th>
                  <th>{copy.table.status}</th>
                  <th>{copy.table.resources}</th>
                  <th>{copy.table.risk}</th>
                  <th>{copy.table.savings}</th>
                  <th>{copy.table.created}</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {plans.data.map((row) => (
                  <tr
                    key={row.id}
                    className={
                      row.id === id ? "row-active clickable" : "clickable"
                    }
                    onClick={() => navigate(`/plans/${row.id}${search}`)}
                  >
                    <td>
                      <div className="font-medium">{shortID(row.id)}</div>
                      <div className="text-xs text-slate-500">
                        {row.dry_run ? copy.labels.dryRun : copy.labels.live}
                      </div>
                    </td>
                    <td>
                      <StatusBadge value={row.status} copy={copy} />
                    </td>
                    <td>{row.resource_count}</td>
                    <td>
                      <RiskBadge value={row.risk} copy={copy} />
                    </td>
                    <td>{formatSavings(row.estimated_monthly_savings, copy)}</td>
                    <td>{formatDate(row.created_at, copy)}</td>
                    <td className="text-right">
                      <div className="action-row" onClick={(e) => e.stopPropagation()}>
                        <button
                          className="small-icon"
                          onClick={() => {
                            navigate(`/plans/${row.id}${search}`);
                            setDialog("approve");
                          }}
                          disabled={approve.isPending || !canApprove(row)}
                          title={copy.actions.approvePlan}
                        >
                          <CheckCircle2 size={16} />
                        </button>
                        <button
                          className="small-icon"
                          onClick={() => {
                            navigate(`/plans/${row.id}${search}`);
                            setDialog("execute");
                          }}
                          disabled={execute.isPending || !canExecute(row)}
                          title={copy.actions.executePlan}
                        >
                          <Zap size={16} />
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyState title={copy.empty.noPlans} />
        )}

        <div>
          <PlanDetail detail={detail.data ?? null} copy={copy} />
          {plan && (
            <div className="detail-actions">
              <button
                className="icon-button"
                onClick={() => setDialog("approve")}
                disabled={approve.isPending || !canApprove(plan)}
              >
                <CheckCircle2 size={16} />
                <span>{copy.actions.approvePlan}</span>
              </button>
              <button
                className="primary-button"
                onClick={() => setDialog("execute")}
                disabled={execute.isPending || !canExecute(plan)}
              >
                <Zap size={16} />
                <span>{copy.actions.executePlan}</span>
              </button>
            </div>
          )}
        </div>
      </div>

      {detail.data && dialog && (
        <PlanActionDialog
          open
          kind={dialog}
          detail={detail.data}
          copy={copy}
          onCancel={() => setDialog(null)}
          onConfirm={() => {
            if (dialog === "approve") approve.mutate(detail.data!.plan.id);
            else execute.mutate(detail.data!.plan.id);
            setDialog(null);
          }}
        />
      )}
    </section>
  );
}
