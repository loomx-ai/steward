import { useState } from "react";
import { Download } from "lucide-react";
import { useShell } from "../../components/AppShell";
import { StatusBadge } from "../../components/StatusBadge";
import { EmptyState } from "../../components/EmptyState";
import { Skeleton } from "../../components/Skeleton";
import { useAudits } from "../../lib/queries";
import { formatDate, shortID } from "../../lib/format";

export function AuditView() {
  const { copy } = useShell();
  const audits = useAudits();
  const [limit, setLimit] = useState(20);

  const list = audits.data ?? [];
  const shown = list.slice(0, limit);

  return (
    <section className="panel">
      <div className="section-heading">
        <h2 className="panel-title">{copy.sections.auditLog}</h2>
        <div className="toolbar">
          <span className="muted">
            {list.length} {copy.labels.events}
          </span>
          <div className="export-links">
            <a
              href="/api/audits/export?format=csv"
              title={copy.actions.exportAuditCSV}
            >
              <Download size={14} />
              <span>CSV</span>
            </a>
            <a
              href="/api/audits/export?format=json"
              title={copy.actions.exportAuditJSON}
            >
              <Download size={14} />
              <span>JSON</span>
            </a>
          </div>
        </div>
      </div>
      {audits.isPending ? (
        <Skeleton rows={6} />
      ) : list.length > 0 ? (
        <>
          <div className="table-wrap compact">
            <table>
              <thead>
                <tr>
                  <th>{copy.table.time}</th>
                  <th>{copy.table.actor}</th>
                  <th>{copy.table.action}</th>
                  <th>{copy.table.target}</th>
                  <th>{copy.table.result}</th>
                  <th>{copy.table.message}</th>
                </tr>
              </thead>
              <tbody>
                {shown.map((audit) => (
                  <tr key={audit.id}>
                    <td>{formatDate(audit.created_at, copy)}</td>
                    <td>{audit.actor}</td>
                    <td>{audit.action}</td>
                    <td>
                      {audit.target_type} / {shortID(audit.target_id)}
                    </td>
                    <td>
                      <StatusBadge value={audit.result} copy={copy} />
                    </td>
                    <td>{audit.message || "-"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {limit < list.length && (
            <button
              className="icon-button mt-3"
              onClick={() => setLimit((value) => value + 20)}
            >
              {copy.labels.events} +20
            </button>
          )}
        </>
      ) : (
        <EmptyState title={copy.empty.noAudits} />
      )}
    </section>
  );
}
