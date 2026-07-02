import {
  FormEvent,
  KeyboardEvent,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import {
  Check,
  CheckCircle2,
  ChevronDown,
  ClipboardCheck,
  Download,
  Eye,
  Network,
  Play,
  RefreshCcw,
  Search,
  Zap,
} from "lucide-react";
import {
  approvePlan,
  createPlan,
  createScan,
  executePlan,
  getPlan,
  getResource,
  getSavingsReport,
  listAudits,
  listCandidates,
  listGraph,
  listPlans,
  listResources,
  listScans,
  reconcileScan,
  updateCandidateStatus,
} from "./api";
import { copyForBrowser, type Copy } from "./i18n";
import type {
  AuditEvent,
  CleanupCandidate,
  CleanupPlan,
  PlanDetail,
  Resource,
  ResourceEdge,
  SavingsReport,
  ScanJob,
} from "./api";

const resourceTypes = [
  "",
  "ecs_instance",
  "disk",
  "eip",
  "security_group",
  "snapshot",
  "vpc",
  "vswitch",
];

type CandidateStatusFilter = CleanupCandidate["status"] | "all";

type DropdownOption = {
  value: string;
  label: string;
};

export function App() {
  const [copy] = useState(() => copyForBrowser());
  const [accountName, setAccountName] = useState("local-demo");
  const [mode, setMode] = useState<"demo" | "alicloud">("demo");
  const [regions, setRegions] = useState("cn-hangzhou");
  const [accessKeyId, setAccessKeyId] = useState("");
  const [accessKeySecret, setAccessKeySecret] = useState("");
  const [scans, setScans] = useState<ScanJob[]>([]);
  const [resources, setResources] = useState<Resource[]>([]);
  const [edges, setEdges] = useState<ResourceEdge[]>([]);
  const [candidates, setCandidates] = useState<CleanupCandidate[]>([]);
  const [plans, setPlans] = useState<CleanupPlan[]>([]);
  const [audits, setAudits] = useState<AuditEvent[]>([]);
  const [report, setReport] = useState<SavingsReport | null>(null);
  const [selectedScanId, setSelectedScanId] = useState("");
  const [resourceType, setResourceType] = useState("");
  const [candidateStatus, setCandidateStatus] =
    useState<CandidateStatusFilter>("open");
  const [query, setQuery] = useState("");
  const [selectedResource, setSelectedResource] = useState<Resource | null>(
    null,
  );
  const [selectedCandidateIds, setSelectedCandidateIds] = useState<string[]>(
    [],
  );
  const [maxResourceCount, setMaxResourceCount] = useState("");
  const [maxRegionCount, setMaxRegionCount] = useState("");
  const [maxHighRiskCount, setMaxHighRiskCount] = useState("0");
  const [selectedPlan, setSelectedPlan] = useState<PlanDetail | null>(null);
  const [loading, setLoading] = useState(false);
  const [message, setMessage] = useState("");

  const activeScanId = selectedScanId || scans[0]?.id || "";

  useEffect(() => {
    document.documentElement.lang = copy.htmlLang;
  }, [copy]);

  const resourceByID = useMemo(() => {
    return new Map(resources.map((resource) => [resource.id, resource]));
  }, [resources]);

  const visibleCandidates = useMemo(() => {
    if (candidateStatus === "all") return candidates;
    return candidates.filter(
      (candidate) => candidate.status === candidateStatus,
    );
  }, [candidateStatus, candidates]);

  const summary = useMemo(() => {
    const protectedCount = resources.filter(
      (resource) => resource.protected,
    ).length;
    const running = scans.filter(
      (scan) => scan.status === "running" || scan.status === "pending",
    ).length;
    const openCandidates = candidates.filter(
      (candidate) => candidate.status === "open",
    ).length;
    return {
      total: resources.length,
      protectedCount,
      running,
      openCandidates,
      savings: report?.estimated_monthly_savings ?? 0,
    };
  }, [resources, scans, candidates, report]);

  async function refresh(nextScanId = activeScanId) {
    setLoading(true);
    setMessage("");
    try {
      const nextScans = await listScans();
      const scanId = nextScanId || selectedScanId || nextScans[0]?.id || "";
      const [
        nextResources,
        nextEdges,
        nextCandidates,
        nextPlans,
        nextAudits,
        nextReport,
      ] = await Promise.all([
        listResources({ scanId, type: resourceType, query }),
        listGraph(scanId || undefined),
        listCandidates(scanId || undefined),
        listPlans(),
        listAudits(),
        getSavingsReport(),
      ]);

      setScans(nextScans);
      setResources(nextResources);
      setEdges(nextEdges);
      setCandidates(nextCandidates);
      setPlans(nextPlans);
      setAudits(nextAudits);
      setReport(nextReport);
      setSelectedCandidateIds((ids) =>
        ids.filter((id) =>
          nextCandidates.some(
            (candidate) => candidate.id === id && candidate.status === "open",
          ),
        ),
      );
      if (!selectedScanId && scanId) setSelectedScanId(scanId);
      if (selectedPlan?.plan.id) {
        const refreshed = await getPlan(selectedPlan.plan.id).catch(() => null);
        setSelectedPlan(refreshed);
      }
    } catch (error) {
      setMessage(
        error instanceof Error ? error.message : copy.messages.requestFailed,
      );
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void refresh();
  }, []);

  useEffect(() => {
    const id = window.setInterval(() => {
      void refresh(selectedScanId || activeScanId);
    }, 5000);
    return () => window.clearInterval(id);
  }, [selectedScanId, activeScanId, resourceType, query]);

  useEffect(() => {
    void refresh(activeScanId);
  }, [selectedScanId, resourceType]);

  async function submitScan(event: FormEvent) {
    event.preventDefault();
    setLoading(true);
    setMessage("");
    try {
      const job = await createScan({
        accountName,
        provider: mode === "demo" ? "demo" : "alicloud",
        mode,
        regions: regions
          .split(",")
          .map((item) => item.trim())
          .filter(Boolean),
        accessKeyId: mode === "alicloud" ? accessKeyId : undefined,
        accessKeySecret: mode === "alicloud" ? accessKeySecret : undefined,
      });
      setSelectedScanId(job.id);
      setMessage(copy.messages.scanQueued(shortID(job.id)));
      await refresh(job.id);
    } catch (error) {
      setMessage(
        error instanceof Error ? error.message : copy.messages.scanFailed,
      );
    } finally {
      setLoading(false);
    }
  }

  async function analyzeActiveScan() {
    if (!activeScanId) {
      setMessage(copy.messages.noScanSelected);
      return;
    }
    setLoading(true);
    setMessage("");
    try {
      const result = await reconcileScan(activeScanId);
      setMessage(
        copy.messages.analysisCompleted(
          result.edge_count,
          result.candidate_count,
        ),
      );
      await refresh(activeScanId);
    } catch (error) {
      setMessage(
        error instanceof Error ? error.message : copy.messages.analysisFailed,
      );
    } finally {
      setLoading(false);
    }
  }

  async function selectResource(id: string) {
    setMessage("");
    try {
      setSelectedResource(await getResource(id));
    } catch (error) {
      setMessage(
        error instanceof Error
          ? error.message
          : copy.messages.resourceLoadFailed,
      );
    }
  }

  function toggleCandidate(id: string) {
    setSelectedCandidateIds((ids) =>
      ids.includes(id) ? ids.filter((item) => item !== id) : [...ids, id],
    );
  }

  async function changeCandidateStatus(
    id: string,
    status: CleanupCandidate["status"],
  ) {
    setLoading(true);
    setMessage("");
    try {
      await updateCandidateStatus(id, status);
      await refresh(activeScanId);
    } catch (error) {
      setMessage(
        error instanceof Error
          ? error.message
          : copy.messages.candidateUpdateFailed,
      );
    } finally {
      setLoading(false);
    }
  }

  async function createSelectedPlan() {
    if (selectedCandidateIds.length === 0) {
      setMessage(copy.messages.selectOpenCandidate);
      return;
    }
    setLoading(true);
    setMessage("");
    try {
      const plan = await createPlan(selectedCandidateIds, {
        maxResourceCount: numericLimit(maxResourceCount),
        maxRegionCount: numericLimit(maxRegionCount),
        maxHighRiskCount: numericLimit(maxHighRiskCount) ?? 0,
      });
      setSelectedCandidateIds([]);
      setMessage(copy.messages.planCreated(shortID(plan.id)));
      await refresh(activeScanId);
      setSelectedPlan(await getPlan(plan.id));
    } catch (error) {
      setMessage(
        error instanceof Error
          ? error.message
          : copy.messages.planCreationFailed,
      );
    } finally {
      setLoading(false);
    }
  }

  async function openPlan(id: string) {
    setMessage("");
    try {
      setSelectedPlan(await getPlan(id));
    } catch (error) {
      setMessage(
        error instanceof Error ? error.message : copy.messages.planLoadFailed,
      );
    }
  }

  async function approveSelectedPlan(id: string) {
    setLoading(true);
    setMessage("");
    try {
      const plan = await approvePlan(id);
      setMessage(copy.messages.planApproved(shortID(plan.id)));
      await refresh(activeScanId);
      setSelectedPlan(await getPlan(id));
    } catch (error) {
      setMessage(
        error instanceof Error
          ? error.message
          : copy.messages.planApprovalFailed,
      );
    } finally {
      setLoading(false);
    }
  }

  async function executeSelectedPlan(id: string) {
    setLoading(true);
    setMessage("");
    try {
      const plan = await executePlan(id);
      setMessage(copy.messages.planExecuted(shortID(plan.id)));
      await refresh(activeScanId);
      setSelectedPlan(await getPlan(id));
    } catch (error) {
      setMessage(
        error instanceof Error
          ? error.message
          : copy.messages.planExecutionFailed,
      );
    } finally {
      setLoading(false);
    }
  }

  function resourceLabel(id: string) {
    const resource = resourceByID.get(id);
    if (!resource) return shortID(id);
    return `${resource.name} (${resource.type})`;
  }

  return (
    <main className="min-h-screen bg-slate-50 text-ink">
      <header className="border-b border-line bg-white">
        <div className="mx-auto flex max-w-7xl flex-wrap items-center justify-between gap-3 px-5 py-4">
          <div>
            <h1 className="text-xl font-semibold">Cloud Steward</h1>
            <p className="text-sm text-slate-600">{copy.subtitle}</p>
          </div>
          <div className="toolbar">
            <button
              className="icon-button"
              onClick={() => refresh()}
              disabled={loading}
              title={copy.actions.refresh}
            >
              <RefreshCcw size={17} />
              <span>{copy.actions.refresh}</span>
            </button>
            <button
              className="primary-button"
              onClick={analyzeActiveScan}
              disabled={loading || !activeScanId}
              title={copy.actions.analyze}
            >
              <Network size={17} />
              <span>{copy.actions.analyze}</span>
            </button>
          </div>
        </div>
      </header>

      <div className="mx-auto grid max-w-7xl gap-4 px-5 py-5 lg:grid-cols-[340px_1fr]">
        <section className="panel">
          <h2 className="panel-title">{copy.sections.newScan}</h2>
          <form className="space-y-4" onSubmit={submitScan}>
            <label className="field">
              <span>{copy.fields.account}</span>
              <input
                value={accountName}
                onChange={(event) => setAccountName(event.target.value)}
              />
            </label>

            <div className="segmented">
              <button
                type="button"
                className={mode === "demo" ? "active" : ""}
                onClick={() => setMode("demo")}
              >
                Demo
              </button>
              <button
                type="button"
                className={mode === "alicloud" ? "active" : ""}
                onClick={() => setMode("alicloud")}
              >
                Alibaba Cloud
              </button>
            </div>

            <label className="field">
              <span>{copy.fields.regions}</span>
              <input
                value={regions}
                onChange={(event) => setRegions(event.target.value)}
              />
            </label>

            {mode === "alicloud" && (
              <div className="space-y-3">
                <label className="field">
                  <span>{copy.fields.accessKeyID}</span>
                  <input
                    value={accessKeyId}
                    onChange={(event) => setAccessKeyId(event.target.value)}
                  />
                </label>
                <label className="field">
                  <span>{copy.fields.accessKeySecret}</span>
                  <input
                    type="password"
                    value={accessKeySecret}
                    onChange={(event) => setAccessKeySecret(event.target.value)}
                  />
                </label>
              </div>
            )}

            <button className="primary-button w-full" disabled={loading}>
              <Play size={17} />
              <span>{copy.actions.startScan}</span>
            </button>
          </form>
        </section>

        <section className="grid gap-4">
          <div className="grid gap-3 md:grid-cols-5">
            <Metric label={copy.metrics.resources} value={summary.total} />
            <Metric
              label={copy.metrics.protected}
              value={summary.protectedCount}
            />
            <Metric label={copy.metrics.activeJobs} value={summary.running} />
            <Metric
              label={copy.metrics.openCandidates}
              value={summary.openCandidates}
            />
            <Metric
              label={copy.metrics.monthlySavings}
              value={formatSavings(summary.savings, copy)}
            />
          </div>

          <section className="panel">
            <div className="section-heading">
              <h2 className="panel-title">{copy.sections.scanJobs}</h2>
              <Dropdown
                label={copy.sections.scanJobs}
                value={selectedScanId}
                options={[
                  { value: "", label: copy.fields.latestScan },
                  ...scans.map((scan) => ({
                    value: scan.id,
                    label: `${scan.account_name} / ${statusLabel(copy, scan.status)}`,
                  })),
                ]}
                onValueChange={setSelectedScanId}
              />
            </div>
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
                  </tr>
                </thead>
                <tbody>
                  {scans.map((scan) => (
                    <tr key={scan.id}>
                      <td>{scan.account_name}</td>
                      <td>{modeLabel(copy, scan.mode)}</td>
                      <td>{scan.regions.join(", ")}</td>
                      <td>
                        <span className={`status ${scan.status}`}>
                          {statusLabel(copy, scan.status)}
                        </span>
                        {scan.failure_reason && (
                          <div className="mt-1 text-xs text-red-700">
                            {scan.failure_reason}
                          </div>
                        )}
                      </td>
                      <td>{scan.resource_count}</td>
                      <td>{formatDate(scan.created_at, copy)}</td>
                    </tr>
                  ))}
                  {scans.length === 0 && (
                    <EmptyRow colSpan={6} text={copy.empty.noScans} />
                  )}
                </tbody>
              </table>
            </div>
          </section>

          <section className="panel">
            <div className="section-heading">
              <h2 className="panel-title">{copy.sections.resources}</h2>
              <div className="filters">
                <Dropdown
                  label={copy.table.type}
                  value={resourceType}
                  options={resourceTypes.map((type) => ({
                    value: type,
                    label: type
                      ? resourceTypeLabel(copy, type)
                      : copy.fields.allTypes,
                  }))}
                  onValueChange={setResourceType}
                />
                <div className="search">
                  <Search size={16} />
                  <input
                    value={query}
                    onChange={(event) => setQuery(event.target.value)}
                    onBlur={() => refresh()}
                    placeholder={copy.fields.searchPlaceholder}
                  />
                </div>
              </div>
            </div>
            <div className="grid gap-4 xl:grid-cols-[1fr_360px]">
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>{copy.table.name}</th>
                      <th>{copy.table.type}</th>
                      <th>{copy.table.region}</th>
                      <th>{copy.table.state}</th>
                      <th>{copy.table.team}</th>
                      <th></th>
                    </tr>
                  </thead>
                  <tbody>
                    {resources.map((resource) => (
                      <tr key={resource.id}>
                        <td>
                          <div className="font-medium">{resource.name}</div>
                          <div className="text-xs text-slate-500">
                            {resource.native_id}
                          </div>
                        </td>
                        <td>{resourceTypeLabel(copy, resource.type)}</td>
                        <td>{resource.region}</td>
                        <td>{resource.state || "-"}</td>
                        <td>{resource.ownership.team || "-"}</td>
                        <td className="text-right">
                          <button
                            className="small-icon"
                            onClick={() => selectResource(resource.id)}
                            title={copy.actions.viewDetails}
                          >
                            <Eye size={16} />
                          </button>
                        </td>
                      </tr>
                    ))}
                    {resources.length === 0 && (
                      <EmptyRow colSpan={6} text={copy.empty.noResources} />
                    )}
                  </tbody>
                </table>
              </div>
              <ResourceDetail resource={selectedResource} copy={copy} />
            </div>
          </section>

          <div className="grid gap-4 xl:grid-cols-[0.9fr_1.35fr]">
            <section className="panel">
              <div className="section-heading">
                <h2 className="panel-title">{copy.sections.resourceGraph}</h2>
                <span className="muted">
                  {edges.length} {copy.labels.edges}
                </span>
              </div>
              <div className="table-wrap compact">
                <table>
                  <thead>
                    <tr>
                      <th>{copy.table.source}</th>
                      <th>{copy.table.relation}</th>
                      <th>{copy.table.target}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {edges.map((edge) => (
                      <tr key={edge.id}>
                        <td>{resourceLabel(edge.source_resource_id)}</td>
                        <td>
                          <span className="pill">{edge.type}</span>
                        </td>
                        <td>{resourceLabel(edge.target_resource_id)}</td>
                      </tr>
                    ))}
                    {edges.length === 0 && (
                      <EmptyRow colSpan={3} text={copy.empty.runAnalyze} />
                    )}
                  </tbody>
                </table>
              </div>
            </section>

            <section className="panel">
              <div className="section-heading">
                <h2 className="panel-title">
                  {copy.sections.cleanupCandidates}
                </h2>
                <div className="toolbar">
                  <Dropdown
                    label={copy.table.status}
                    value={candidateStatus}
                    options={copy.filters.candidateStatuses}
                    onValueChange={(value) =>
                      setCandidateStatus(value as CandidateStatusFilter)
                    }
                  />
                  <button
                    className="primary-button"
                    onClick={createSelectedPlan}
                    disabled={loading || selectedCandidateIds.length === 0}
                  >
                    <ClipboardCheck size={17} />
                    <span>{copy.actions.createPlan}</span>
                  </button>
                </div>
              </div>
              <div className="limit-grid">
                <label className="field">
                  <span>{copy.fields.maxResources}</span>
                  <input
                    type="number"
                    min="1"
                    value={maxResourceCount}
                    onChange={(event) =>
                      setMaxResourceCount(event.target.value)
                    }
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
                    onChange={(event) =>
                      setMaxHighRiskCount(event.target.value)
                    }
                  />
                </label>
              </div>
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
                    {visibleCandidates.map((candidate) => (
                      <tr key={candidate.id}>
                        <td className="checkbox-cell">
                          <input
                            type="checkbox"
                            checked={selectedCandidateIds.includes(
                              candidate.id,
                            )}
                            disabled={candidate.status !== "open"}
                            onChange={() => toggleCandidate(candidate.id)}
                            aria-label={copy.messages.selectCandidateAria(
                              candidate.resource.name,
                            )}
                          />
                        </td>
                        <td>
                          <div className="font-medium">
                            {candidate.resource.name}
                          </div>
                          <div className="text-xs text-slate-500">
                            {resourceTypeLabel(copy, candidate.resource.type)}
                          </div>
                          <div className="mt-1 text-xs text-slate-500">
                            {candidate.reason}
                          </div>
                        </td>
                        <td>
                          <div>{candidate.rule_id}</div>
                          <span className={`risk ${candidate.risk}`}>
                            {riskLabel(copy, candidate.risk)}
                          </span>
                        </td>
                        <td>
                          {actionLabel(copy, candidate.recommended_action)}
                        </td>
                        <td>
                          {formatSavings(
                            candidate.estimated_monthly_savings,
                            copy,
                          )}
                        </td>
                        <td>
                          <span className={`status ${candidate.status}`}>
                            {statusLabel(copy, candidate.status)}
                          </span>
                        </td>
                        <td>
                          <CandidateActions
                            candidate={candidate}
                            onStatus={changeCandidateStatus}
                            copy={copy}
                          />
                        </td>
                      </tr>
                    ))}
                    {visibleCandidates.length === 0 && (
                      <EmptyRow colSpan={7} text={copy.empty.noCandidates} />
                    )}
                  </tbody>
                </table>
              </div>
            </section>
          </div>

          <section className="panel">
            <div className="section-heading">
              <h2 className="panel-title">{copy.sections.cleanupPlans}</h2>
              <span className="muted">
                {plans.length} {copy.labels.plans}
              </span>
            </div>
            <div className="grid gap-4 xl:grid-cols-[1fr_380px]">
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
                    {plans.map((plan) => (
                      <tr key={plan.id}>
                        <td>
                          <div className="font-medium">{shortID(plan.id)}</div>
                          <div className="text-xs text-slate-500">
                            {plan.dry_run
                              ? copy.labels.dryRun
                              : copy.labels.live}
                          </div>
                        </td>
                        <td>
                          <span className={`status ${plan.status}`}>
                            {statusLabel(copy, plan.status)}
                          </span>
                        </td>
                        <td>{plan.resource_count}</td>
                        <td>
                          <span className={`risk ${plan.risk}`}>
                            {riskLabel(copy, plan.risk)}
                          </span>
                        </td>
                        <td>
                          {formatSavings(plan.estimated_monthly_savings, copy)}
                        </td>
                        <td>{formatDate(plan.created_at, copy)}</td>
                        <td>
                          <div className="action-row">
                            <button
                              className="small-icon"
                              onClick={() => openPlan(plan.id)}
                              title={copy.actions.viewPlan}
                            >
                              <Eye size={16} />
                            </button>
                            <button
                              className="small-icon"
                              onClick={() => approveSelectedPlan(plan.id)}
                              disabled={loading || !canApprove(plan)}
                              title={copy.actions.approvePlan}
                            >
                              <CheckCircle2 size={16} />
                            </button>
                            <button
                              className="small-icon"
                              onClick={() => executeSelectedPlan(plan.id)}
                              disabled={loading || !canExecute(plan)}
                              title={copy.actions.executePlan}
                            >
                              <Zap size={16} />
                            </button>
                          </div>
                        </td>
                      </tr>
                    ))}
                    {plans.length === 0 && (
                      <EmptyRow colSpan={7} text={copy.empty.noPlans} />
                    )}
                  </tbody>
                </table>
              </div>
              <PlanDetailView detail={selectedPlan} copy={copy} />
            </div>
          </section>

          <div className="grid gap-4 xl:grid-cols-[360px_1fr]">
            <SavingsPanel report={report} copy={copy} />
            <AuditLog audits={audits} copy={copy} />
          </div>
        </section>
      </div>

      {message && <div className="toast">{message}</div>}
    </main>
  );
}

function Dropdown({
  label,
  value,
  options,
  onValueChange,
}: {
  label: string;
  value: string;
  options: DropdownOption[];
  onValueChange: (value: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState(0);
  const listID = useRef(`dropdown-${Math.random().toString(36).slice(2)}`);
  const selectedIndex = Math.max(
    0,
    options.findIndex((option) => option.value === value),
  );
  const selected = options[selectedIndex] ?? options[0];

  function openMenu(nextIndex = selectedIndex) {
    setActiveIndex(nextIndex);
    setOpen(true);
  }

  function choose(option: DropdownOption) {
    onValueChange(option.value);
    setOpen(false);
  }

  function move(delta: number) {
    if (options.length === 0) return;
    setActiveIndex(
      (index) => (index + delta + options.length) % options.length,
    );
  }

  function handleKeyDown(event: KeyboardEvent<HTMLButtonElement>) {
    switch (event.key) {
      case "ArrowDown":
        event.preventDefault();
        if (!open) openMenu();
        else move(1);
        break;
      case "ArrowUp":
        event.preventDefault();
        if (!open) openMenu();
        else move(-1);
        break;
      case "Home":
        event.preventDefault();
        openMenu(0);
        break;
      case "End":
        event.preventDefault();
        openMenu(Math.max(0, options.length - 1));
        break;
      case "Enter":
      case " ":
        event.preventDefault();
        if (!open) {
          openMenu();
          return;
        }
        if (options[activeIndex]) choose(options[activeIndex]);
        break;
      case "Escape":
        setOpen(false);
        break;
    }
  }

  return (
    <div
      className="dropdown"
      onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget)) {
          setOpen(false);
        }
      }}
    >
      <button
        type="button"
        className="dropdown-trigger"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={listID.current}
        onClick={() => (open ? setOpen(false) : openMenu())}
        onKeyDown={handleKeyDown}
      >
        <span>{selected?.label ?? label}</span>
        <ChevronDown size={16} />
      </button>
      {open && (
        <div
          id={listID.current}
          className="dropdown-menu"
          role="listbox"
          aria-label={label}
        >
          {options.map((option, index) => {
            const selectedOption = option.value === value;
            return (
              <button
                type="button"
                role="option"
                aria-selected={selectedOption}
                key={option.value}
                className={
                  index === activeIndex
                    ? "dropdown-option active"
                    : "dropdown-option"
                }
                onMouseEnter={() => setActiveIndex(index)}
                onClick={() => choose(option)}
              >
                <span>{option.label}</span>
                {selectedOption && <Check size={15} />}
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

function Metric({ label, value }: { label: string; value: string | number }) {
  return (
    <div className="metric">
      <div className="text-sm text-slate-600">{label}</div>
      <div className="mt-1 text-2xl font-semibold">{value}</div>
    </div>
  );
}

function CandidateActions({
  candidate,
  onStatus,
  copy,
}: {
  candidate: CleanupCandidate;
  onStatus: (id: string, status: CleanupCandidate["status"]) => void;
  copy: Copy;
}) {
  return (
    <div className="mini-actions">
      <button
        onClick={() => onStatus(candidate.id, "accepted")}
        disabled={candidate.status === "accepted"}
      >
        {copy.actions.accept}
      </button>
      <button
        onClick={() => onStatus(candidate.id, "ignored")}
        disabled={candidate.status === "ignored"}
      >
        {copy.actions.ignore}
      </button>
      <button
        onClick={() => onStatus(candidate.id, "snoozed")}
        disabled={candidate.status === "snoozed"}
      >
        {copy.actions.snooze}
      </button>
    </div>
  );
}

function PlanDetailView({
  detail,
  copy,
}: {
  detail: PlanDetail | null;
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
  return (
    <aside className="detail-panel">
      <div className="detail-heading">
        <h3>{copy.sections.planDetail}</h3>
        <div className="export-links">
          <a
            href={`/api/plans/${detail.plan.id}/export?format=markdown`}
            title={copy.actions.exportMarkdown}
          >
            <Download size={14} />
            <span>MD</span>
          </a>
          <a
            href={`/api/plans/${detail.plan.id}/export?format=json`}
            title={copy.actions.exportJSON}
          >
            <Download size={14} />
            <span>JSON</span>
          </a>
        </div>
      </div>
      <dl>
        <dt>{copy.table.status}</dt>
        <dd>
          <span className={`status ${detail.plan.status}`}>
            {statusLabel(copy, detail.plan.status)}
          </span>
        </dd>
        <dt>{copy.fields.approval}</dt>
        <dd>
          {detail.plan.approved_by
            ? `${detail.plan.approved_by} / ${detail.plan.approval_comment || "-"}`
            : "-"}
        </dd>
        <dt>{copy.fields.items}</dt>
        <dd>
          <div className="plan-items">
            {detail.items.map((item) => (
              <div key={item.id} className="plan-item">
                <div className="font-medium">{item.resource.name}</div>
                <div className="text-xs text-slate-500">
                  {actionLabel(copy, item.action)} /{" "}
                  {riskLabel(copy, item.risk)} /{" "}
                  {formatSavings(item.estimated_monthly_savings, copy)}
                </div>
                <div className="text-xs text-slate-500">
                  {item.blocked
                    ? `${copy.labels.blocked}: ${item.block_reason || "-"}`
                    : statusLabel(copy, item.result || copy.labels.pending)}
                </div>
              </div>
            ))}
          </div>
        </dd>
      </dl>
    </aside>
  );
}

function ResourceDetail({
  resource,
  copy,
}: {
  resource: Resource | null;
  copy: Copy;
}) {
  if (!resource) {
    return (
      <aside className="detail-panel">
        <h3>{copy.sections.resourceDetail}</h3>
        <div className="empty">{copy.empty.noResourceSelected}</div>
      </aside>
    );
  }
  return (
    <aside className="detail-panel">
      <h3>{copy.sections.resourceDetail}</h3>
      <dl>
        <dt>{copy.table.name}</dt>
        <dd>{resource.name}</dd>
        <dt>{copy.fields.nativeID}</dt>
        <dd>{resource.native_id}</dd>
        <dt>{copy.fields.protected}</dt>
        <dd>{resource.protected ? copy.fields.yes : copy.fields.no}</dd>
        <dt>{copy.fields.tags}</dt>
        <dd>
          {Object.keys(resource.tags).length === 0 ? (
            "-"
          ) : (
            <div className="tag-grid">
              {Object.entries(resource.tags).map(([key, value]) => (
                <span key={key}>
                  {key}={value}
                </span>
              ))}
            </div>
          )}
        </dd>
        <dt>{copy.fields.raw}</dt>
        <dd>
          <pre>{JSON.stringify(resource.raw, null, 2)}</pre>
        </dd>
      </dl>
    </aside>
  );
}

function SavingsPanel({
  report,
  copy,
}: {
  report: SavingsReport | null;
  copy: Copy;
}) {
  return (
    <section className="panel">
      <h2 className="panel-title">{copy.sections.savingsReport}</h2>
      <dl className="report-list">
        <dt>{copy.labels.candidates}</dt>
        <dd>{report?.candidate_count ?? 0}</dd>
        <dt>{copy.labels.plans}</dt>
        <dd>{report?.plan_count ?? 0}</dd>
        <dt>{copy.labels.completedPlans}</dt>
        <dd>{report?.completed_plan_count ?? 0}</dd>
        <dt>{copy.labels.monthlyEstimate}</dt>
        <dd>{formatSavings(report?.estimated_monthly_savings ?? 0, copy)}</dd>
      </dl>
      <h3 className="subheading">{copy.labels.byType}</h3>
      <MiniBreakdown
        rows={report?.estimated_savings_by_type ?? {}}
        copy={copy}
      />
      <h3 className="subheading">{copy.labels.byTeam}</h3>
      <MiniBreakdown
        rows={report?.estimated_savings_by_team ?? {}}
        copy={copy}
      />
    </section>
  );
}

function MiniBreakdown({
  rows,
  copy,
}: {
  rows: Record<string, number>;
  copy: Copy;
}) {
  const entries = Object.entries(rows);
  if (entries.length === 0) {
    return <div className="empty small">{copy.empty.noData}</div>;
  }
  return (
    <div className="breakdown">
      {entries.map(([name, value]) => (
        <div key={name}>
          <span>{name || copy.fields.unknown}</span>
          <strong>{formatSavings(value, copy)}</strong>
        </div>
      ))}
    </div>
  );
}

function AuditLog({ audits, copy }: { audits: AuditEvent[]; copy: Copy }) {
  return (
    <section className="panel">
      <div className="section-heading">
        <h2 className="panel-title">{copy.sections.auditLog}</h2>
        <div className="toolbar">
          <span className="muted">
            {audits.length} {copy.labels.events}
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
            {audits.slice(0, 12).map((audit) => (
              <tr key={audit.id}>
                <td>{formatDate(audit.created_at, copy)}</td>
                <td>{audit.actor}</td>
                <td>{audit.action}</td>
                <td>
                  {audit.target_type} / {shortID(audit.target_id)}
                </td>
                <td>
                  <span className={`status ${audit.result}`}>
                    {statusLabel(copy, audit.result)}
                  </span>
                </td>
                <td>{audit.message || "-"}</td>
              </tr>
            ))}
            {audits.length === 0 && (
              <EmptyRow colSpan={6} text={copy.empty.noAudits} />
            )}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function EmptyRow({ colSpan, text }: { colSpan: number; text: string }) {
  return (
    <tr>
      <td colSpan={colSpan}>
        <div className="empty small">{text}</div>
      </td>
    </tr>
  );
}

function canApprove(plan: CleanupPlan) {
  return plan.status === "draft" || plan.status === "pending_approval";
}

function canExecute(plan: CleanupPlan) {
  return plan.status === "approved";
}

function formatDate(value: string, copy: Copy) {
  if (!value) return "-";
  return new Intl.DateTimeFormat(copy.locale, {
    month: "short",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  }).format(new Date(value));
}

function formatSavings(value: number, copy: Copy) {
  return `${value.toFixed(1)} ${copy.labels.perMonth}`;
}

function shortID(value: string) {
  if (!value) return "-";
  return value.length <= 10 ? value : value.slice(0, 8);
}

function numericLimit(value: string) {
  const parsed = Number.parseInt(value, 10);
  if (!Number.isFinite(parsed) || parsed < 0) return undefined;
  return parsed;
}

function statusLabel(copy: Copy, value: string) {
  return copy.labels.statuses[value] ?? value;
}

function riskLabel(copy: Copy, value: string) {
  return copy.labels.risks[value] ?? value;
}

function actionLabel(copy: Copy, value: string) {
  return copy.labels.actions[value] ?? value;
}

function resourceTypeLabel(copy: Copy, value: string) {
  return copy.labels.resourceTypes[value] ?? value;
}

function modeLabel(copy: Copy, value: string) {
  return copy.labels.modes[value] ?? value;
}
