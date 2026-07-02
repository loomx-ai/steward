import { Link } from "react-router-dom";
import { ArrowRight, ListChecks, ClipboardCheck, AlertTriangle } from "lucide-react";
import { useShell } from "../../components/AppShell";
import { Metric } from "../../components/Metric";
import {
  useScans,
  useResources,
  useCandidates,
  usePlans,
  useSavings,
  useAudits,
} from "../../lib/queries";
import { formatSavings, formatDate } from "../../lib/format";

export function OverviewView() {
  const { copy, activeScanId } = useShell();
  const scans = useScans();
  const resources = useResources(activeScanId, "", "");
  const candidates = useCandidates(activeScanId);
  const plans = usePlans();
  const savings = useSavings();
  const audits = useAudits();

  const scanList = scans.data ?? [];
  const resourceList = resources.data ?? [];
  const candidateList = candidates.data ?? [];
  const planList = plans.data ?? [];

  const activeScan = scanList.find((s) => s.id === activeScanId) ?? scanList[0];
  const openCandidates = candidateList.filter((c) => c.status === "open");
  const openSavings = openCandidates.reduce(
    (sum, c) => sum + c.estimated_monthly_savings,
    0,
  );
  const pendingPlans = planList.filter(
    (p) => p.status === "pending_approval" || p.status === "draft",
  );
  const failedScans = scanList.filter((s) => s.status === "failed");

  const scannedCount = activeScan?.resource_count ?? resourceList.length;
  const analyzed = candidateList.length > 0;

  const stages = [
    { label: `${copy.overview.scanned} ${scannedCount}`, done: scannedCount > 0 },
    { label: copy.overview.analyzed, done: analyzed },
    {
      label: `${openCandidates.length} ${copy.overview.toReview}`,
      done: openCandidates.length === 0 && analyzed,
      active: openCandidates.length > 0,
    },
    {
      label: `${pendingPlans.length} ${copy.overview.toApprove}`,
      done: pendingPlans.length === 0 && planList.length > 0,
      active: pendingPlans.length > 0,
    },
    { label: copy.overview.executed, done: planList.some((p) => p.status === "completed") },
  ];

  const hasWork =
    openCandidates.length > 0 || pendingPlans.length > 0 || failedScans.length > 0;

  return (
    <div className="grid gap-4">
      <section className="panel">
        <h2 className="panel-title">{copy.overview.pipelineTitle}</h2>
        <div className="pipeline">
          {stages.map((stage, index) => (
            <div key={index} className="pipeline-step">
              <span
                className={
                  "pipeline-pill" +
                  (stage.done ? " done" : "") +
                  (stage.active ? " active" : "")
                }
              >
                {stage.label}
              </span>
              {index < stages.length - 1 && (
                <ArrowRight size={15} className="pipeline-arrow" />
              )}
            </div>
          ))}
        </div>
      </section>

      <section className="panel">
        <h2 className="panel-title">{copy.overview.needsYou}</h2>
        <div className="needs-you">
          {openCandidates.length > 0 && (
            <Link className="needs-you-card warning" to="/candidates">
              <ListChecks size={18} />
              <span className="needs-you-text">
                {openCandidates.length} {copy.nav.candidates} ·{" "}
                {formatSavings(openSavings, copy)}
              </span>
              <span className="needs-you-cta">
                {copy.overview.reviewCta} <ArrowRight size={15} />
              </span>
            </Link>
          )}
          {pendingPlans.length > 0 && (
            <Link className="needs-you-card accent" to="/plans">
              <ClipboardCheck size={18} />
              <span className="needs-you-text">
                {pendingPlans.length} {copy.nav.plans} · {copy.overview.toApprove}
              </span>
              <span className="needs-you-cta">
                {copy.overview.approveCta} <ArrowRight size={15} />
              </span>
            </Link>
          )}
          {failedScans.length > 0 && (
            <Link className="needs-you-card danger" to="/scans">
              <AlertTriangle size={18} />
              <span className="needs-you-text">
                {failedScans.length} {copy.overview.failedScans}
              </span>
              <span className="needs-you-cta">
                {copy.nav.scans} <ArrowRight size={15} />
              </span>
            </Link>
          )}
          {!hasWork && (
            <div className="empty-state small">{copy.overview.allClear}</div>
          )}
        </div>
      </section>

      <div className="grid gap-3 md:grid-cols-3">
        <Metric
          label={copy.metrics.monthlySavings}
          value={formatSavings(savings.data?.estimated_monthly_savings ?? 0, copy)}
        />
        <Metric label={copy.metrics.resources} value={resourceList.length} />
        <Metric
          label={copy.metrics.protected}
          value={resourceList.filter((r) => r.protected).length}
        />
      </div>

      <section className="panel">
        <h2 className="panel-title">{copy.overview.recentActivity}</h2>
        <div className="table-wrap compact">
          <table>
            <tbody>
              {(audits.data ?? []).slice(0, 5).map((audit) => (
                <tr key={audit.id}>
                  <td className="text-xs text-slate-500">
                    {formatDate(audit.created_at, copy)}
                  </td>
                  <td>{audit.actor}</td>
                  <td>{audit.action}</td>
                </tr>
              ))}
              {(audits.data ?? []).length === 0 && (
                <tr>
                  <td>
                    <div className="empty-state small">{copy.empty.noAudits}</div>
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}
