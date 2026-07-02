import { useState } from "react";
import type { Copy } from "../../lib/i18n";
import type { PlanDetail } from "../../lib/api";
import { formatSavings } from "../../lib/format";
import { ConfirmDialog } from "../../components/ConfirmDialog";

export function PlanActionDialog({
  open,
  kind,
  detail,
  copy,
  onConfirm,
  onCancel,
}: {
  open: boolean;
  kind: "approve" | "execute";
  detail: PlanDetail;
  copy: Copy;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const [ack, setAck] = useState(false);
  const { plan, items } = detail;
  const regions = new Set(items.map((item) => item.resource.region));
  const highRisk = items.filter((item) => item.risk === "high").length;
  const blocked = items.filter((item) => item.blocked);
  const isLiveExecute = kind === "execute" && !plan.dry_run;
  const canConfirm = !isLiveExecute || ack;

  return (
    <ConfirmDialog
      open={open}
      title={copy.guardrail.impactTitle}
      confirmLabel={
        kind === "approve"
          ? copy.guardrail.confirmApprove
          : copy.guardrail.confirmExecute
      }
      cancelLabel={copy.guardrail.cancel}
      canConfirm={canConfirm}
      onConfirm={onConfirm}
      onCancel={onCancel}
    >
      <div className="impact-badge">
        {plan.dry_run ? copy.labels.dryRun : copy.labels.live}
      </div>
      <dl className="impact-list">
        <dt>{copy.guardrail.resources}</dt>
        <dd>{plan.resource_count}</dd>
        <dt>{copy.guardrail.regions}</dt>
        <dd>{regions.size}</dd>
        <dt>{copy.guardrail.highRisk}</dt>
        <dd>{highRisk}</dd>
        <dt>{copy.guardrail.totalSavings}</dt>
        <dd>{formatSavings(plan.estimated_monthly_savings, copy)}</dd>
      </dl>

      {blocked.length > 0 && (
        <div className="impact-blocked">
          <div className="impact-blocked-title">
            {copy.guardrail.blockedSkipped}
          </div>
          <ul>
            {blocked.map((item) => (
              <li key={item.id}>
                {item.resource.name} — {item.block_reason || "-"}
              </li>
            ))}
          </ul>
        </div>
      )}

      {isLiveExecute && (
        <div className="impact-live">
          <p className="impact-live-warning">{copy.guardrail.liveWarning}</p>
          <label className="impact-ack">
            <input
              type="checkbox"
              checked={ack}
              onChange={(event) => setAck(event.target.checked)}
            />
            <span>{copy.guardrail.liveAck}</span>
          </label>
        </div>
      )}
    </ConfirmDialog>
  );
}
