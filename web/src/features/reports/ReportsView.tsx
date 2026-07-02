import { useShell } from "../../components/AppShell";
import { Metric } from "../../components/Metric";
import { EmptyState } from "../../components/EmptyState";
import { useSavings } from "../../lib/queries";
import { formatSavings } from "../../lib/format";
import type { Copy } from "../../lib/i18n";

function Breakdown({
  rows,
  copy,
}: {
  rows: Record<string, number>;
  copy: Copy;
}) {
  const entries = Object.entries(rows);
  if (entries.length === 0) {
    return <div className="empty-state small">{copy.empty.noData}</div>;
  }
  const max = Math.max(...entries.map(([, value]) => value), 1);
  return (
    <div className="bars">
      {entries.map(([name, value]) => (
        <div key={name} className="bar-row">
          <span className="bar-label">{name || copy.fields.unknown}</span>
          <span className="bar-track">
            <span
              className="bar-fill"
              style={{ width: `${(value / max) * 100}%` }}
            />
          </span>
          <strong className="bar-value">{formatSavings(value, copy)}</strong>
        </div>
      ))}
    </div>
  );
}

export function ReportsView() {
  const { copy } = useShell();
  const savings = useSavings();
  const report = savings.data;

  return (
    <div className="grid gap-4">
      <div className="grid gap-3 md:grid-cols-4">
        <Metric label={copy.labels.candidates} value={report?.candidate_count ?? 0} />
        <Metric label={copy.labels.plans} value={report?.plan_count ?? 0} />
        <Metric
          label={copy.labels.completedPlans}
          value={report?.completed_plan_count ?? 0}
        />
        <Metric
          label={copy.labels.monthlyEstimate}
          value={formatSavings(report?.estimated_monthly_savings ?? 0, copy)}
        />
      </div>

      <section className="panel">
        <h2 className="panel-title">{copy.labels.byType}</h2>
        <Breakdown rows={report?.estimated_savings_by_type ?? {}} copy={copy} />
      </section>

      <section className="panel">
        <h2 className="panel-title">{copy.labels.byTeam}</h2>
        <Breakdown rows={report?.estimated_savings_by_team ?? {}} copy={copy} />
      </section>

      {!report && <EmptyState title={copy.empty.noData} />}
    </div>
  );
}
