export type ScanStatus = "pending" | "running" | "succeeded" | "failed";

export interface ScanJob {
  id: string;
  account_id: string;
  account_name: string;
  provider: string;
  mode: string;
  regions: string[];
  status: ScanStatus;
  resource_count: number;
  failure_reason?: string;
  created_at: string;
  started_at?: string;
  finished_at?: string;
}

export interface Ownership {
  owner?: string;
  team?: string;
  application?: string;
  environment?: string;
  cost_center?: string;
}

export interface Resource {
  id: string;
  scan_id: string;
  provider: string;
  account_id: string;
  region: string;
  type: string;
  native_id: string;
  name: string;
  state: string;
  tags: Record<string, string>;
  ownership: Ownership;
  protected: boolean;
  created_at: string;
  last_seen_at: string;
  raw: Record<string, unknown>;
}

export interface ResourceEdge {
  id: string;
  scan_id: string;
  source_resource_id: string;
  target_resource_id: string;
  type: string;
  source: string;
  confidence: number;
  evidence: Record<string, unknown>;
  created_at: string;
}

export interface CleanupCandidate {
  id: string;
  scan_id: string;
  resource_id: string;
  resource: Resource;
  rule_id: string;
  reason: string;
  confidence: number;
  risk: "low" | "medium" | "high";
  recommended_action: string;
  estimated_monthly_savings: number;
  status: "open" | "accepted" | "ignored" | "snoozed";
  evidence: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

export interface CleanupPlan {
  id: string;
  status:
    | "draft"
    | "pending_approval"
    | "approved"
    | "running"
    | "completed"
    | "failed"
    | "canceled";
  dry_run: boolean;
  resource_count: number;
  risk: "low" | "medium" | "high";
  estimated_monthly_savings: number;
  created_by: string;
  approved_by?: string;
  approval_comment?: string;
  created_at: string;
  approved_at?: string;
  executed_at?: string;
}

export interface CleanupPlanItem {
  id: string;
  plan_id: string;
  candidate_id: string;
  resource_id: string;
  resource: Resource;
  action: string;
  order: number;
  risk: string;
  blocked: boolean;
  block_reason?: string;
  reason: string;
  estimated_monthly_savings: number;
  result?: string;
  request_id?: string;
}

export interface PlanDetail {
  plan: CleanupPlan;
  items: CleanupPlanItem[];
}

export interface AuditEvent {
  id: string;
  actor: string;
  action: string;
  target_type: string;
  target_id: string;
  result: string;
  message: string;
  request_id: string;
  created_at: string;
}

export interface SavingsReport {
  candidate_count: number;
  plan_count: number;
  completed_plan_count: number;
  estimated_monthly_savings: number;
  estimated_savings_by_team: Record<string, number>;
  estimated_savings_by_type: Record<string, number>;
}

export interface CreateScanInput {
  accountName: string;
  provider: string;
  mode: string;
  regions: string[];
  accessKeyId?: string;
  accessKeySecret?: string;
}

export interface PlanLimitsInput {
  maxResourceCount?: number;
  maxRegionCount?: number;
  maxHighRiskCount?: number;
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    headers: {
      "Content-Type": "application/json",
      ...(init?.headers ?? {}),
    },
    ...init,
  });
  if (!response.ok) {
    const payload = await response.json().catch(() => undefined);
    const message =
      payload?.error?.message ?? `request failed with ${response.status}`;
    throw new Error(message);
  }
  return response.json() as Promise<T>;
}

export function createScan(input: CreateScanInput): Promise<ScanJob> {
  return request<ScanJob>("/api/scans", {
    method: "POST",
    body: JSON.stringify(input),
  });
}

export function listScans(): Promise<ScanJob[]> {
  return request<ScanJob[]>("/api/scans");
}

export function listResources(filters: {
  scanId?: string;
  type?: string;
  query?: string;
}): Promise<Resource[]> {
  const params = new URLSearchParams();
  if (filters.scanId) params.set("scan_id", filters.scanId);
  if (filters.type) params.set("type", filters.type);
  if (filters.query) params.set("q", filters.query);
  const query = params.toString();
  return request<Resource[]>(`/api/resources${query ? `?${query}` : ""}`);
}

export function getResource(id: string): Promise<Resource> {
  return request<Resource>(`/api/resources/${id}`);
}

export function reconcileScan(
  scanId: string,
): Promise<{ edge_count: number; candidate_count: number }> {
  return request(`/api/scans/${scanId}/reconcile`, {
    method: "POST",
    body: JSON.stringify({ actor: "local-user" }),
  });
}

export function listGraph(scanId?: string): Promise<ResourceEdge[]> {
  const params = new URLSearchParams();
  if (scanId) params.set("scan_id", scanId);
  const query = params.toString();
  return request<ResourceEdge[]>(`/api/graph${query ? `?${query}` : ""}`);
}

export function listCandidates(scanId?: string): Promise<CleanupCandidate[]> {
  const params = new URLSearchParams();
  if (scanId) params.set("scan_id", scanId);
  const query = params.toString();
  return request<CleanupCandidate[]>(
    `/api/candidates${query ? `?${query}` : ""}`,
  );
}

export function updateCandidateStatus(
  id: string,
  status: CleanupCandidate["status"],
): Promise<CleanupCandidate> {
  return request<CleanupCandidate>(`/api/candidates/${id}`, {
    method: "PATCH",
    body: JSON.stringify({ status }),
  });
}

export function createPlan(
  candidateIds: string[],
  limits: PlanLimitsInput = {},
): Promise<CleanupPlan> {
  return request<CleanupPlan>("/api/plans", {
    method: "POST",
    body: JSON.stringify({
      candidate_ids: candidateIds,
      actor: "local-user",
      max_resource_count: limits.maxResourceCount,
      max_region_count: limits.maxRegionCount,
      max_high_risk_count: limits.maxHighRiskCount ?? 0,
    }),
  });
}

export function listPlans(): Promise<CleanupPlan[]> {
  return request<CleanupPlan[]>("/api/plans");
}

export function getPlan(id: string): Promise<PlanDetail> {
  return request<PlanDetail>(`/api/plans/${id}`);
}

export function approvePlan(id: string): Promise<CleanupPlan> {
  return request<CleanupPlan>(`/api/plans/${id}/approve`, {
    method: "POST",
    body: JSON.stringify({
      actor: "local-user",
      comment: "approved in local console",
    }),
  });
}

export function executePlan(id: string): Promise<CleanupPlan> {
  return request<CleanupPlan>(`/api/plans/${id}/execute`, {
    method: "POST",
    body: JSON.stringify({ actor: "local-user" }),
  });
}

export function listAudits(): Promise<AuditEvent[]> {
  return request<AuditEvent[]>("/api/audits");
}

export function getSavingsReport(): Promise<SavingsReport> {
  return request<SavingsReport>("/api/reports/savings");
}
