import { useMemo, useState } from "react";
import { useNavigate, useLocation } from "react-router-dom";
import { ClipboardCheck } from "lucide-react";
import { useShell } from "../../components/AppShell";
import { Dropdown } from "../../components/Dropdown";
import { RiskBadge } from "../../components/RiskBadge";
import { StatusBadge } from "../../components/StatusBadge";
import { EmptyState } from "../../components/EmptyState";
import { Skeleton } from "../../components/Skeleton";
import {
  useCandidates,
  useUpdateCandidate,
  useCreatePlan,
} from "../../lib/queries";
import {
  actionLabel,
  formatSavings,
  numericLimit,
  resourceTypeLabel,
} from "../../lib/format";
import type { CleanupCandidate } from "../../lib/api";

type StatusFilter = CleanupCandidate["status"] | "all";

export function CandidatesView() {
  const { copy, activeScanId } = useShell();
  const navigate = useNavigate();
  const { search } = useLocation();
  const candidates = useCandidates(activeScanId);
  const updateCandidate = useUpdateCandidate();
  const createPlan = useCreatePlan();

  const [status, setStatus] = useState<StatusFilter>("open");
  const [selectedIds, setSelectedIds] = useState<string[]>([]);
  const [maxResourceCount, setMaxResourceCount] = useState("");
  const [maxRegionCount, setMaxRegionCount] = useState("");
  const [maxHighRiskCount, setMaxHighRiskCount] = useState("0");

  const list = candidates.data ?? [];
  const visible = useMemo(
    () => (status === "all" ? list : list.filter((c) => c.status === status)),
    [list, status],
  );
  const selectedSavings = useMemo(
    () =>
      list
        .filter((c) => selectedIds.includes(c.id))
        .reduce((sum, c) => sum + c.estimated_monthly_savings, 0),
    [list, selectedIds],
  );

  function toggle(id: string) {
    setSelectedIds((ids) =>
      ids.includes(id) ? ids.filter((item) => item !== id) : [...ids, id],
    );
  }

  async function assemble() {
    if (selectedIds.length === 0) return;
    const plan = await createPlan.mutateAsync({
      ids: selectedIds,
      limits: {
        maxResourceCount: numericLimit(maxResourceCount),
        maxRegionCount: numericLimit(maxRegionCount),
        maxHighRiskCount: numericLimit(maxHighRiskCount) ?? 0,
      },
    });
    setSelectedIds([]);
    navigate(`/plans/${plan.id}${search}`);
  }

  return (
    <section className="panel">
      <div className="section-heading">
        <h2 className="panel-title">{copy.sections.cleanupCandidates}</h2>
        <Dropdown
          label={copy.table.status}
          value={status}
          options={copy.filters.candidateStatuses}
          onValueChange={(value) => setStatus(value as StatusFilter)}
        />
      </div>

      {candidates.isPending ? (
        <Skeleton rows={5} />
      ) : visible.length > 0 ? (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th></th>
                <th>{copy.table.resource}</th>
                <th>{copy.table.rule}</th>
                <th>{copy.table.action}</th>
                <th>{copy.table.savings}</th>
                <th>{copy.table.status}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {visible.map((candidate) => (
                <tr key={candidate.id}>
                  <td className="checkbox-cell">
                    <input
                      type="checkbox"
                      checked={selectedIds.includes(candidate.id)}
                      disabled={candidate.status !== "open"}
                      onChange={() => toggle(candidate.id)}
                      aria-label={copy.messages.selectCandidateAria(
                        candidate.resource.name,
                      )}
                    />
                  </td>
                  <td>
                    <div className="font-medium">{candidate.resource.name}</div>
                    <div className="text-xs text-slate-500">
                      {resourceTypeLabel(copy, candidate.resource.type)}
                    </div>
                    <div className="mt-1 text-xs text-slate-500">
                      {candidate.reason}
                    </div>
                  </td>
                  <td>
                    <div>{candidate.rule_id}</div>
                    <RiskBadge value={candidate.risk} copy={copy} />
                  </td>
                  <td>{actionLabel(copy, candidate.recommended_action)}</td>
                  <td>
                    {formatSavings(candidate.estimated_monthly_savings, copy)}
                  </td>
                  <td>
                    <StatusBadge value={candidate.status} copy={copy} />
                  </td>
                  <td>
                    <div className="mini-actions">
                      <button
                        onClick={() =>
                          updateCandidate.mutate({
                            id: candidate.id,
                            status: "accepted",
                          })
                        }
                        disabled={candidate.status === "accepted"}
                      >
                        {copy.actions.accept}
                      </button>
                      <button
                        onClick={() =>
                          updateCandidate.mutate({
                            id: candidate.id,
                            status: "ignored",
                          })
                        }
                        disabled={candidate.status === "ignored"}
                      >
                        {copy.actions.ignore}
                      </button>
                      <button
                        onClick={() =>
                          updateCandidate.mutate({
                            id: candidate.id,
                            status: "snoozed",
                          })
                        }
                        disabled={candidate.status === "snoozed"}
                      >
                        {copy.actions.snooze}
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <EmptyState title={copy.empty.noCandidates} />
      )}

      {selectedIds.length > 0 && (
        <div className="assemble-bar">
          <div className="assemble-summary">
            <strong>{selectedIds.length}</strong>
            <span> · {formatSavings(selectedSavings, copy)}</span>
          </div>
          <div className="limit-grid">
            <label className="field">
              <span>{copy.fields.maxResources}</span>
              <input
                type="number"
                min="1"
                value={maxResourceCount}
                onChange={(event) => setMaxResourceCount(event.target.value)}
                placeholder={copy.fields.noLimit}
              />
            </label>
            <label className="field">
              <span>{copy.fields.maxRegions}</span>
              <input
                type="number"
                min="1"
                value={maxRegionCount}
                onChange={(event) => setMaxRegionCount(event.target.value)}
                placeholder={copy.fields.noLimit}
              />
            </label>
            <label className="field">
              <span>{copy.fields.highRiskCap}</span>
              <input
                type="number"
                min="0"
                value={maxHighRiskCount}
                onChange={(event) => setMaxHighRiskCount(event.target.value)}
              />
            </label>
          </div>
          <button
            className="primary-button"
            onClick={assemble}
            disabled={createPlan.isPending}
          >
            <ClipboardCheck size={17} />
            <span>{copy.actions.createPlan}</span>
          </button>
        </div>
      )}
    </section>
  );
}
