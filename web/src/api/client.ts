import type {
  ActionAttempt,
  Asset,
  AuditEvent,
  CloudConnection,
  ConnectionRegion,
  CleanupSelector,
  CleanupTask,
  ExecutionConfirmation,
  CreateConnectionInput,
  ExecutionAttempt,
  Finding,
  Job,
  JobLog,
  LifecycleBinding,
  Page,
  CleanupTaskAggregate,
  Principal,
  ProviderBundle,
  ProviderCatalogBundle,
  ProviderDescriptor,
  Relationship,
  CreateScanInput,
  NetworkTargetPage,
  OAuthFlow,
  ScanLogPage,
  ScanTask,
  Scope,
  TopologyQuery,
  TopologyResponse,
} from "./types";

let accessTokenProvider: () => string | undefined = () => undefined;
let principalObserver: (principal: Principal) => void = () => undefined;
let unauthorizedObserver: () => void = () => undefined;

export interface Session {
  mode: "local" | "token" | "cloud";
  authenticated: boolean;
  principal: Principal | null;
  display_name?: string;
}

export function getSession(): Promise<Session> {
  return request<Session>("/api/session", { cache: "no-store" });
}

export function setAccessTokenProvider(provider: () => string | undefined) {
  accessTokenProvider = provider;
}

export function setPrincipalObserver(observer: (principal: Principal) => void) {
  principalObserver = observer;
}

export function setUnauthorizedObserver(observer: () => void) {
  unauthorizedObserver = observer;
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const token = accessTokenProvider()?.trim();
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body) headers.set("Content-Type", "application/json");
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const response = await fetch(path, { ...init, headers });
  observePrincipal(response);
  if (!response.ok) {
    const payload = (await response.json().catch(() => ({}))) as {
      error?:
        | string
        | {
            code?: string;
            message?: string;
            details?: Record<string, unknown>;
            request_id?: string;
          };
    };
    if (payload.error && typeof payload.error === "object") {
      throw new APIRequestError(
        payload.error.message || `request failed with ${response.status}`,
        payload.error.code,
        payload.error.details,
        payload.error.request_id,
      );
    }
    throw new Error(payload.error || `request failed with ${response.status}`);
  }
  if (response.status === 204) return undefined as T;
  return (await response.json()) as T;
}

export class APIRequestError extends Error {
  constructor(
    message: string,
    readonly code?: string,
    readonly details?: Record<string, unknown>,
    readonly requestID?: string,
  ) {
    super(message);
    this.name = "APIRequestError";
  }
}

function observePrincipal(response: Response) {
  if (response.status === 401) {
    unauthorizedObserver();
    return;
  }
  const subject = response.headers.get("X-Steward-Subject")?.trim();
  if (!subject) return;
  const roles = (response.headers.get("X-Steward-Roles") ?? "")
    .split(",")
    .map((role) => role.trim())
    .filter(Boolean);
  principalObserver({ subject, roles });
}

function listPath(
  path: string,
  cursor = "",
  limit = 100,
  filters: Record<string, string | string[] | undefined> = {},
): string {
  const params = new URLSearchParams({ limit: String(limit) });
  if (cursor) params.set("cursor", cursor);
  for (const [key, value] of Object.entries(filters)) {
    if (Array.isArray(value)) {
      for (const item of value) {
        if (item) params.append(key, item);
      }
    } else if (value) {
      params.set(key, value);
    }
  }
  return `${path}?${params.toString()}`;
}

function scopedPath(path: string, connectionID: string): string {
  if (!connectionID.trim())
    throw new Error("a cloud connection must be selected");
  const [pathname, query = ""] = path.split("?", 2);
  const params = new URLSearchParams(query);
  params.set("connection_id", connectionID);
  return `${pathname}?${params.toString()}`;
}

export async function listProviderCatalog(): Promise<ProviderCatalogBundle[]> {
  const bundles = await request<ProviderBundle[]>("/api/providers/catalog");
  return bundles.map((bundle) => {
    const kinds =
      bundle.kinds ?? bundle.specs.map((compiled) => compiled.resource_kind);
    const runtimeByID = new Map(kinds.map((kind) => [kind.id, kind]));
    return {
      ...bundle,
      kinds,
      kinds_revision: bundle.kinds_revision ?? bundle.revision,
      specs: bundle.specs.map((compiled) => ({
        ...compiled,
        resource_kind:
          runtimeByID.get(compiled.resource_kind.id) ?? compiled.resource_kind,
      })),
    };
  });
}

export function getTopology(
  connectionID: string,
  query: TopologyQuery = {},
): Promise<TopologyResponse> {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (Array.isArray(value)) {
      for (const item of value) {
        if (item !== "") params.append(key, String(item));
      }
    } else if (value !== undefined && value !== "") {
      params.set(key, String(value));
    }
  }
  const suffix = params.size ? `?${params.toString()}` : "";
  return request<TopologyResponse>(
    scopedPath(`/api/topology${suffix}`, connectionID),
  );
}

export function listProviders(): Promise<ProviderDescriptor[]> {
  return request<ProviderDescriptor[]>("/api/providers");
}

export function startAliCloudOAuthFlow(site: string): Promise<OAuthFlow> {
  return request<OAuthFlow>("/api/providers/alicloud/oauth/flows", {
    method: "POST",
    body: JSON.stringify({ site }),
  });
}

export function getAliCloudOAuthFlow(id: string): Promise<OAuthFlow> {
  return request<OAuthFlow>(
    `/api/providers/alicloud/oauth/flows/${encodeURIComponent(id)}`,
  );
}

export function listConnections(
  options: { cursor?: string; limit?: number; provider?: string } = {},
): Promise<Page<CloudConnection>> {
  return request<Page<CloudConnection>>(
    listPath("/api/connections", options.cursor, options.limit, {
      provider: options.provider,
    }),
  );
}

export function createConnection(
  input: CreateConnectionInput,
): Promise<CloudConnection> {
  return request<CloudConnection>("/api/connections", {
    method: "POST",
    body: JSON.stringify(input),
  });
}

export function renameConnection(
  id: string,
  name: string,
): Promise<CloudConnection> {
  return request<CloudConnection>(
    `/api/connections/${encodeURIComponent(id)}`,
    {
      method: "PATCH",
      body: JSON.stringify({ name }),
    },
  );
}

export function replaceConnectionCredential(
  id: string,
  credential: CreateConnectionInput["credential"],
): Promise<CloudConnection> {
  return request<CloudConnection>(
    `/api/connections/${encodeURIComponent(id)}/credential`,
    { method: "PUT", body: JSON.stringify(credential) },
  );
}

export function validateConnection(id: string): Promise<CloudConnection> {
  return request<CloudConnection>(
    `/api/connections/${encodeURIComponent(id)}/validate`,
    { method: "POST" },
  );
}

export function deleteConnection(
  id: string,
  confirmation: string,
): Promise<void> {
  return request<void>(`/api/connections/${encodeURIComponent(id)}`, {
    method: "DELETE",
    body: JSON.stringify({ confirmation }),
  });
}

export function listConnectionRegions(
  connectionID: string,
  options: {
    lifecycle?: string;
    query?: string;
    signal?: AbortSignal;
  } = {},
): Promise<Page<ConnectionRegion>> {
  const params = new URLSearchParams();
  if (options.lifecycle) params.set("lifecycle", options.lifecycle);
  if (options.query) params.set("q", options.query);
  const query = params.size > 0 ? `?${params.toString()}` : "";
  return request<Page<ConnectionRegion>>(
    `/api/connections/${encodeURIComponent(connectionID)}/regions${query}`,
    { signal: options.signal },
  );
}

export function refreshConnectionRegions(
  connectionID: string,
): Promise<{ job_id: string; status: string }> {
  return request(
    `/api/connections/${encodeURIComponent(connectionID)}/regions/refresh`,
    { method: "POST" },
  );
}

export function getJob(connectionID: string, jobID: string): Promise<Job> {
  return request<Job>(
    scopedPath(`/api/jobs/${encodeURIComponent(jobID)}`, connectionID),
  );
}

export function addConnectionRegion(
  connectionID: string,
  input: { region_id: string; name: string },
): Promise<ConnectionRegion> {
  return request(
    `/api/connections/${encodeURIComponent(connectionID)}/regions`,
    {
      method: "POST",
      body: JSON.stringify(input),
    },
  );
}

export function updateConnectionRegion(
  connectionID: string,
  regionID: string,
  input: {
    name?: string;
    reset_name?: boolean;
    lifecycle?: "active" | "retired";
  },
): Promise<ConnectionRegion> {
  return request(
    `/api/connections/${encodeURIComponent(connectionID)}/regions/${encodeURIComponent(regionID)}`,
    {
      method: "PATCH",
      body: JSON.stringify(input),
    },
  );
}

export function excludeConnectionRegion(
  connectionID: string,
  regionID: string,
): Promise<ConnectionRegion> {
  return request(
    `/api/connections/${encodeURIComponent(connectionID)}/regions/${encodeURIComponent(regionID)}`,
    { method: "DELETE" },
  );
}

export function restoreConnectionRegion(
  connectionID: string,
  regionID: string,
): Promise<ConnectionRegion> {
  return request(
    `/api/connections/${encodeURIComponent(connectionID)}/regions/${encodeURIComponent(regionID)}/restore`,
    { method: "POST" },
  );
}

export function listScopes(
  connectionID: string,
  cursor = "",
): Promise<Page<Scope>> {
  return request<Page<Scope>>(
    scopedPath(listPath("/api/scopes", cursor), connectionID),
  );
}

export function listScans(
  connectionID: string,
  cursor = "",
  limit = 100,
  signal?: AbortSignal,
): Promise<Page<ScanTask>> {
  return request<Page<ScanTask>>(
    scopedPath(listPath("/api/scans", cursor, limit), connectionID),
    { signal },
  );
}

export function getScan(connectionID: string, id: string): Promise<ScanTask> {
  return request<ScanTask>(
    scopedPath(`/api/scans/${encodeURIComponent(id)}`, connectionID),
  );
}

export function createScan(
  connectionID: string,
  input: CreateScanInput,
): Promise<ScanTask> {
  return request<ScanTask>(scopedPath("/api/scans", connectionID), {
    method: "POST",
    body: JSON.stringify(input),
  });
}

export function controlScan(
  connectionID: string,
  id: string,
  action: "pause" | "resume" | "cancel" | "retry",
): Promise<ScanTask> {
  return request<ScanTask>(
    scopedPath(`/api/scans/${encodeURIComponent(id)}/${action}`, connectionID),
    { method: "POST" },
  );
}

export function searchNetworkTargets(
  connectionID: string,
  kind: "vpc" | "vswitch",
  input: {
    regionID: string;
    query?: string;
    vpcID?: string;
    cursor?: string;
    signal?: AbortSignal;
  },
): Promise<NetworkTargetPage> {
  const params = new URLSearchParams({
    region_id: input.regionID,
    limit: "50",
  });
  if (input.query) params.set("query", input.query);
  if (input.vpcID) params.set("vpc_id", input.vpcID);
  if (input.cursor) params.set("cursor", input.cursor);
  const endpoint =
    kind === "vpc" ? "/api/scan-targets/vpcs" : "/api/scan-targets/vswitches";
  return request<NetworkTargetPage>(
    scopedPath(`${endpoint}?${params.toString()}`, connectionID),
    { signal: input.signal },
  );
}

export function listAssets(
  connectionID: string,
  options: {
    cursor?: string;
    limit?: number;
    query?: string;
    resourceQuery?: string;
    provider?: string;
    capability?: string;
    resourceKindIDs?: string[];
    resourceKindID?: string;
    assetIDs?: string[];
    nativeIDs?: string[];
    canvas?: "account" | "global" | "region" | "region-public" | "vpc";
    regionID?: string;
    vpcID?: string;
    searchOrder?: "panorama";
    includeClosed?: boolean;
    signal?: AbortSignal;
  } = {},
): Promise<Page<Asset>> {
  return request<Page<Asset>>(
    scopedPath(
      listPath("/api/assets", options.cursor, options.limit, {
        q: options.query,
        resource_query: options.resourceQuery,
        provider: options.provider,
        capability: options.capability,
        resource_kind_id:
          options.resourceKindIDs ??
          (options.resourceKindID ? [options.resourceKindID] : undefined),
        asset_id: options.assetIDs,
        native_id: options.nativeIDs,
        canvas: options.canvas,
        region_id: options.regionID,
        vpc_id: options.vpcID,
        order:
          options.searchOrder === "panorama" ? "panorama-search" : undefined,
        include_closed: options.includeClosed ? "true" : undefined,
      }),
      connectionID,
    ),
    { signal: options.signal },
  );
}

export async function findAssetsByNativeIDs(
  connectionID: string,
  nativeIDs: string[],
  signal?: AbortSignal,
): Promise<Asset[]> {
  const values = [
    ...new Set(nativeIDs.map((value) => value.trim()).filter(Boolean)),
  ];
  const result: Asset[] = [];
  const batchSize = 50;
  for (let start = 0; start < values.length; start += batchSize) {
    const batch = values.slice(start, start + batchSize);
    const page = await listAssets(connectionID, {
      limit: 100,
      nativeIDs: batch,
      includeClosed: true,
      signal,
    });
    result.push(...page.items);
  }
  return result;
}

export async function findAssets(
  connectionID: string,
  ids: string[],
): Promise<Asset[]> {
  const values = [...new Set(ids.map((value) => value.trim()).filter(Boolean))];
  const result = new Map<string, Asset>();
  const batchSize = 50;
  for (let start = 0; start < values.length; start += batchSize) {
    const batch = values.slice(start, start + batchSize);
    const page = await listAssets(connectionID, {
      limit: batchSize,
      assetIDs: batch,
      includeClosed: true,
    });
    for (const asset of page.items) {
      result.set(asset.id, asset);
    }
  }
  return ids.flatMap((id) => {
    const asset = result.get(id.trim());
    return asset ? [asset] : [];
  });
}

export async function findAsset(
  connectionID: string,
  id: string,
): Promise<Asset> {
  const [asset] = await findAssets(connectionID, [id]);
  if (!asset) throw new Error(`asset ${id} was not found`);
  return asset;
}

export function setAssetDirty(
  connectionID: string,
  id: string,
  dirty: boolean,
): Promise<Asset> {
  return request<Asset>(
    scopedPath(`/api/assets/${encodeURIComponent(id)}`, connectionID),
    {
      method: "PATCH",
      body: JSON.stringify({ dirty }),
    },
  );
}

export function getAssetGraph(
  connectionID: string,
  id: string,
): Promise<{ relationships: Relationship[] }> {
  return request(
    scopedPath(`/api/assets/${encodeURIComponent(id)}/graph`, connectionID),
  );
}

export function getAssetLifecycle(
  connectionID: string,
  id: string,
): Promise<{ bindings: LifecycleBinding[] }> {
  return request(
    scopedPath(`/api/assets/${encodeURIComponent(id)}/lifecycle`, connectionID),
  );
}

export function listFindings(
  connectionID: string,
  cursor = "",
  limit = 100,
): Promise<Page<Finding>> {
  return request<Page<Finding>>(
    scopedPath(listPath("/api/findings", cursor, limit), connectionID),
  );
}

export function listCleanupTasks(
  connectionID: string,
  cursor = "",
  limit = 100,
): Promise<Page<CleanupTaskAggregate["task"]>> {
  return request(
    scopedPath(listPath("/api/cleanup", cursor, limit), connectionID),
  );
}

export function createCleanupTask(
  connectionID: string,
  input: {
    selectors: CleanupSelector[];
    request_options?: Record<string, Record<string, unknown>>;
  },
): Promise<CleanupTaskAggregate> {
  return request<CleanupTaskAggregate>(
    scopedPath("/api/cleanup", connectionID),
    {
      method: "POST",
      body: JSON.stringify(input),
    },
  );
}

export function getCleanupTask(
  connectionID: string,
  id: string,
): Promise<CleanupTaskAggregate> {
  return request<CleanupTaskAggregate>(
    scopedPath(`/api/cleanup/${encodeURIComponent(id)}`, connectionID),
  );
}

export function addCleanupTaskAssets(
  connectionID: string,
  id: string,
  assetIDs: string[],
): Promise<CleanupTaskAggregate> {
  return request<CleanupTaskAggregate>(
    scopedPath(`/api/cleanup/${encodeURIComponent(id)}/assets`, connectionID),
    {
      method: "POST",
      body: JSON.stringify({
        selectors: assetIDs.map((asset_id) => ({
          kind: "asset",
          asset_id,
        })),
      }),
    },
  );
}

export function createExecution(
  connectionID: string,
  cleanupTaskID: string,
  idempotencyKey: string,
  concurrency: number,
  confirmation: ExecutionConfirmation,
): Promise<ExecutionAttempt> {
  return request<ExecutionAttempt>(
    scopedPath(
      `/api/cleanup/${encodeURIComponent(cleanupTaskID)}/executions`,
      connectionID,
    ),
    {
      method: "POST",
      body: JSON.stringify({
        idempotency_key: idempotencyKey,
        concurrency,
        confirmation,
      }),
    },
  );
}

export function continueCleanupExecution(
  connectionID: string,
  cleanupTaskID: string,
  idempotencyKey: string,
  concurrency: number,
): Promise<ExecutionAttempt> {
  return request<ExecutionAttempt>(
    scopedPath(
      `/api/cleanup/${encodeURIComponent(cleanupTaskID)}/continue`,
      connectionID,
    ),
    {
      method: "POST",
      body: JSON.stringify({ idempotency_key: idempotencyKey, concurrency }),
    },
  );
}

export function pauseCleanupExecution(
  connectionID: string,
  cleanupTaskID: string,
): Promise<ExecutionAttempt> {
  return request<ExecutionAttempt>(
    scopedPath(
      `/api/cleanup/${encodeURIComponent(cleanupTaskID)}/pause`,
      connectionID,
    ),
    { method: "POST" },
  );
}

export function resumeCleanupExecution(
  connectionID: string,
  cleanupTaskID: string,
): Promise<ExecutionAttempt> {
  return request<ExecutionAttempt>(
    scopedPath(
      `/api/cleanup/${encodeURIComponent(cleanupTaskID)}/resume`,
      connectionID,
    ),
    { method: "POST" },
  );
}

export function listExecutions(
  connectionID: string,
  cursor = "",
  limit = 100,
): Promise<Page<ExecutionAttempt>> {
  return request<Page<ExecutionAttempt>>(
    scopedPath(
      listPath("/api/execution-attempts", cursor, limit),
      connectionID,
    ),
  );
}

export function listCleanupTaskExecutions(
  connectionID: string,
  cleanupTaskID: string,
  cursor = "",
  limit = 100,
): Promise<Page<ExecutionAttempt>> {
  return request<Page<ExecutionAttempt>>(
    scopedPath(
      listPath(
        `/api/cleanup/${encodeURIComponent(cleanupTaskID)}/executions`,
        cursor,
        limit,
      ),
      connectionID,
    ),
  );
}

export function getExecution(
  connectionID: string,
  id: string,
): Promise<ExecutionAttempt> {
  return request<ExecutionAttempt>(
    scopedPath(
      `/api/execution-attempts/${encodeURIComponent(id)}`,
      connectionID,
    ),
  );
}

export function listExecutionActions(
  connectionID: string,
  executionID: string,
): Promise<Page<ActionAttempt>> {
  return request<Page<ActionAttempt>>(
    scopedPath(
      `/api/execution-attempts/${encodeURIComponent(executionID)}/actions`,
      connectionID,
    ),
  );
}

export function listAuditEvents(
  connectionID: string,
  cursor = "",
  limit = 100,
): Promise<Page<AuditEvent>> {
  return request<Page<AuditEvent>>(
    scopedPath(listPath("/api/audit-events", cursor, limit), connectionID),
  );
}

export async function streamJobEvents(
  connectionID: string,
  jobID: string,
  after: number,
  onLog: (log: JobLog) => void,
  signal: AbortSignal,
): Promise<number> {
  const token = accessTokenProvider()?.trim();
  const headers = new Headers({ Accept: "text/event-stream" });
  if (token) headers.set("Authorization", `Bearer ${token}`);
  if (after > 0) headers.set("Last-Event-ID", String(after));
  const response = await fetch(
    scopedPath(`/api/jobs/${encodeURIComponent(jobID)}/events`, connectionID),
    { headers, signal },
  );
  observePrincipal(response);
  if (!response.ok || !response.body) {
    throw new Error(`job event stream failed with ${response.status}`);
  }
  const reader = response.body.pipeThrough(new TextDecoderStream()).getReader();
  let buffer = "";
  let latest = after;
  while (true) {
    const { done, value } = await reader.read();
    buffer += value ?? "";
    let boundary = buffer.indexOf("\n\n");
    while (boundary >= 0) {
      const frame = buffer.slice(0, boundary);
      buffer = buffer.slice(boundary + 2);
      const idLine = frame.split("\n").find((line) => line.startsWith("id:"));
      const dataLine = frame
        .split("\n")
        .find((line) => line.startsWith("data:"));
      if (idLine && dataLine) {
        latest = Number(idLine.slice(3).trim()) || latest;
        onLog(JSON.parse(dataLine.slice(5).trim()) as JobLog);
      }
      boundary = buffer.indexOf("\n\n");
    }
    if (done) break;
  }
  return latest;
}

export async function streamScanEvents(
  connectionID: string,
  scanID: string,
  targetKey: string,
  after: string,
  onEvent: (event: {
    type: "snapshot" | "log" | "end";
    data: ScanTask | JobLog;
    id?: string;
  }) => void,
  signal: AbortSignal,
): Promise<string> {
  const token = accessTokenProvider()?.trim();
  const headers = new Headers({ Accept: "text/event-stream" });
  if (token) headers.set("Authorization", `Bearer ${token}`);
  if (after) headers.set("Last-Event-ID", after);
  const params = new URLSearchParams();
  if (targetKey) params.set("target_key", targetKey);
  const query = params.toString();
  const response = await fetch(
    scopedPath(
      `/api/scans/${encodeURIComponent(scanID)}/events${query ? `?${query}` : ""}`,
      connectionID,
    ),
    { headers, signal },
  );
  observePrincipal(response);
  if (!response.ok || !response.body)
    throw new Error(`scan event stream failed with ${response.status}`);
  const reader = response.body.pipeThrough(new TextDecoderStream()).getReader();
  let buffer = "";
  let latest = after;
  while (true) {
    const { done, value } = await reader.read();
    buffer += value ?? "";
    let boundary = buffer.indexOf("\n\n");
    while (boundary >= 0) {
      const frame = buffer.slice(0, boundary);
      buffer = buffer.slice(boundary + 2);
      const lines = frame.split("\n");
      const event = lines
        .find((line) => line.startsWith("event:"))
        ?.slice(6)
        .trim();
      const id = lines
        .find((line) => line.startsWith("id:"))
        ?.slice(3)
        .trim();
      const data = lines
        .find((line) => line.startsWith("data:"))
        ?.slice(5)
        .trim();
      if (id) latest = id;
      if (
        (event === "snapshot" || event === "log" || event === "end") &&
        data
      ) {
        onEvent({
          type: event,
          data: JSON.parse(data) as ScanTask | JobLog,
          id,
        });
      }
      boundary = buffer.indexOf("\n\n");
    }
    if (done) break;
  }
  return latest;
}

export function getScanLogs(
  connectionID: string,
  scanID: string,
  targetKey = "",
  before = "",
  signal?: AbortSignal,
): Promise<ScanLogPage> {
  const params = new URLSearchParams({ limit: "100" });
  if (targetKey) params.set("target_key", targetKey);
  if (before) params.set("before", before);
  return request<ScanLogPage>(
    scopedPath(
      `/api/scans/${encodeURIComponent(scanID)}/logs?${params.toString()}`,
      connectionID,
    ),
    { signal },
  );
}

export async function streamCleanupTaskEvents(
  connectionID: string,
  taskID: string,
  after: string,
  filters: CleanupLogFilters,
  onEvent: (event: {
    type: "snapshot" | "log" | "end";
    data: CleanupTask | JobLog;
    id?: string;
  }) => void,
  signal: AbortSignal,
): Promise<string> {
  const params = cleanupLogParams(filters);
  const token = accessTokenProvider()?.trim();
  const headers = new Headers({ Accept: "text/event-stream" });
  if (token) headers.set("Authorization", `Bearer ${token}`);
  if (after) headers.set("Last-Event-ID", after);
  const response = await fetch(
    scopedPath(
      `/api/cleanup/${encodeURIComponent(taskID)}/events?${params.toString()}`,
      connectionID,
    ),
    { headers, signal },
  );
  observePrincipal(response);
  if (!response.ok || !response.body)
    throw new Error(`cleanup event stream failed with ${response.status}`);
  const reader = response.body.pipeThrough(new TextDecoderStream()).getReader();
  let buffer = "";
  let latest = after;
  while (true) {
    const { done, value } = await reader.read();
    buffer += value ?? "";
    let boundary = buffer.indexOf("\n\n");
    while (boundary >= 0) {
      const frame = buffer.slice(0, boundary);
      buffer = buffer.slice(boundary + 2);
      const lines = frame.split("\n");
      const event = lines
        .find((line) => line.startsWith("event:"))
        ?.slice(6)
        .trim();
      const id = lines
        .find((line) => line.startsWith("id:"))
        ?.slice(3)
        .trim();
      const data = lines
        .find((line) => line.startsWith("data:"))
        ?.slice(5)
        .trim();
      if (id) latest = id;
      if (
        (event === "snapshot" || event === "log" || event === "end") &&
        data
      ) {
        onEvent({
          type: event,
          data: JSON.parse(data) as CleanupTask | JobLog,
          id,
        });
      }
      boundary = buffer.indexOf("\n\n");
    }
    if (done) break;
  }
  return latest;
}

export function getCleanupTaskLogs(
  connectionID: string,
  taskID: string,
  before = "",
  filters: CleanupLogFilters = {},
  signal?: AbortSignal,
): Promise<ScanLogPage> {
  const params = cleanupLogParams(filters);
  params.set("limit", "100");
  if (before) params.set("before", before);
  return request<ScanLogPage>(
    scopedPath(
      `/api/cleanup/${encodeURIComponent(taskID)}/logs?${params.toString()}`,
      connectionID,
    ),
    { signal },
  );
}

export interface CleanupLogFilters {
  resourceID?: string;
  resourceKindIDs?: string[];
}

function cleanupLogParams(filters: CleanupLogFilters) {
  const params = new URLSearchParams();
  const resourceID = filters.resourceID?.trim();
  if (resourceID) params.set("resource_id", resourceID);
  for (const resourceKindID of new Set(filters.resourceKindIDs ?? [])) {
    const value = resourceKindID.trim();
    if (value) params.append("resource_kind_id", value);
  }
  return params;
}
