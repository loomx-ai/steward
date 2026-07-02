import { Network } from "lucide-react";
import { useShell } from "../../components/AppShell";
import { StatusBadge } from "../../components/StatusBadge";
import { EmptyState } from "../../components/EmptyState";
import { Skeleton } from "../../components/Skeleton";
import { useScans, useReconcile } from "../../lib/queries";
import { formatDate, modeLabel } from "../../lib/format";
import { NewScanForm } from "./NewScanForm";

export function ScansView() {
  const { copy, activeScanId, setScan } = useShell();
  const scans = useScans();
  const reconcile = useReconcile();

  return (
    <div className="grid gap-4 lg:grid-cols-[340px_1fr]">
      <NewScanForm copy={copy} onCreated={setScan} />

      <section className="panel">
        <div className="section-heading">
          <h2 className="panel-title">{copy.sections.scanJobs}</h2>
        </div>
        {scans.isPending ? (
          <Skeleton rows={4} />
        ) : scans.data && scans.data.length > 0 ? (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>{copy.table.account}</th>
                  <th>{copy.table.mode}</th>
                  <th>{copy.table.regions}</th>
                  <th>{copy.table.status}</th>
                  <th>{copy.table.resources}</th>
                  <th>{copy.table.created}</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {scans.data.map((scan) => (
                  <tr
                    key={scan.id}
                    className={
                      scan.id === activeScanId ? "row-active clickable" : "clickable"
                    }
                    onClick={() => setScan(scan.id)}
                  >
                    <td>{scan.account_name}</td>
                    <td>{modeLabel(copy, scan.mode)}</td>
                    <td>{scan.regions.join(", ")}</td>
                    <td>
                      <StatusBadge value={scan.status} copy={copy} />
                      {scan.failure_reason && (
                        <div className="mt-1 text-xs text-red-700">
                          {scan.failure_reason}
                        </div>
                      )}
                    </td>
                    <td>{scan.resource_count}</td>
                    <td>{formatDate(scan.created_at, copy)}</td>
                    <td className="text-right">
                      {scan.status === "succeeded" && (
                        <button
                          className="icon-button"
                          disabled={reconcile.isPending}
                          onClick={(event) => {
                            event.stopPropagation();
                            reconcile.mutate(scan.id);
                          }}
                          title={copy.actions.analyze}
                        >
                          <Network size={15} />
                          <span>{copy.actions.analyze}</span>
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyState title={copy.empty.noScans} />
        )}
      </section>
    </div>
  );
}
