# Cloud Steward Web Console Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restructure the Cloud Steward web console from a single 1364-line dashboard into a routed, task-flow-driven SPA with a lifecycle sidebar, action-first overview, interactive topology graph, cleanup execution guardrails, and a per-view TanStack Query data layer.

**Architecture:** A routed multi-view React SPA. `AppShell` (sidebar + top bar) wraps feature views mounted by react-router. Data flows through TanStack Query hooks scoped to the active scan (held in the `?scan=` URL param); only in-flight work polls. Mutating API calls read the current actor from `ActorProvider`. The existing bilingual i18n and typed API client are preserved and extended, not replaced.

**Tech Stack:** React 19, TypeScript, Vite, Tailwind, lucide-react (existing) + `react-router-dom`, `@tanstack/react-query`, `@xyflow/react` (new).

**Verification convention:** The `web/` package has no test runner; the repo verifies the frontend with `npm --prefix web run build` (`tsc -b` + `vite build`) and `npm --prefix web run lint` (`tsc --noEmit`). This plan uses a compile-and-build gate per task instead of unit tests, matching the repo's existing verification step, plus a final manual smoke against a running server. The Go static handler already serves `index.html` for non-asset paths (`internal/webui/static.go`), so `BrowserRouter` needs no backend change.

---

## File Structure

Target layout under `web/src/`:

- `main.tsx` — mounts providers + router (modified).
- `App.tsx` — deleted after migration; its panels move into feature views.
- `routes.tsx` — route table under `AppShell`.
- `lib/api.ts` — moved from `src/api.ts`; mutating calls take an `actor` arg.
- `lib/i18n.ts` — moved from `src/i18n.ts`; extended copy.
- `lib/actor.tsx` — actor context (default `local-user`).
- `lib/format.ts` — extracted formatting/label helpers.
- `lib/queries.ts` — TanStack Query hooks + query keys + mutations.
- `lib/useActiveScan.ts` — reads/writes the `?scan=` param, resolves the active scan.
- `components/AppShell.tsx`, `Sidebar.tsx`, `TopBar.tsx`, `Dropdown.tsx`, `StatusBadge.tsx`, `RiskBadge.tsx`, `Metric.tsx`, `EmptyState.tsx`, `Skeleton.tsx`, `ConfirmDialog.tsx`, `Toast.tsx`.
- `features/overview/OverviewView.tsx`
- `features/scans/ScansView.tsx`, `features/scans/NewScanForm.tsx`
- `features/resources/ResourcesView.tsx`, `ResourceDetail.tsx`, `TopologyGraph.tsx`
- `features/candidates/CandidatesView.tsx`
- `features/plans/PlansView.tsx`, `PlanDetail.tsx`, `PlanActionDialog.tsx`
- `features/reports/ReportsView.tsx`
- `features/audit/AuditView.tsx`

---

## Task 1: Add dependencies and provider scaffold

**Files:**
- Modify: `web/package.json`
- Create: `web/src/lib/actor.tsx`
- Modify: `web/src/main.tsx`

- [ ] **Step 1: Install new dependencies**

Run:
```bash
npm --prefix web install react-router-dom @tanstack/react-query @xyflow/react
```
Expected: three packages added to `dependencies` in `web/package.json`, lockfile updated.

- [ ] **Step 2: Create the actor context**

Create `web/src/lib/actor.tsx`:
```tsx
import { createContext, useContext, useState, type ReactNode } from "react";

type ActorContextValue = {
  actor: string;
  setActor: (actor: string) => void;
};

const ActorContext = createContext<ActorContextValue | null>(null);

export function ActorProvider({ children }: { children: ReactNode }) {
  const [actor, setActor] = useState("local-user");
  return (
    <ActorContext.Provider value={{ actor, setActor }}>
      {children}
    </ActorContext.Provider>
  );
}

export function useActor(): ActorContextValue {
  const value = useContext(ActorContext);
  if (!value) throw new Error("useActor must be used within ActorProvider");
  return value;
}
```

- [ ] **Step 3: Wire providers + router in main.tsx**

Replace `web/src/main.tsx` with:
```tsx
import React from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ActorProvider } from "./lib/actor";
import { AppRoutes } from "./routes";
import "./styles.css";
import "@xyflow/react/dist/style.css";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 10_000,
      refetchOnWindowFocus: true,
      retry: 1,
    },
  },
});

createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <ActorProvider>
        <BrowserRouter>
          <AppRoutes />
        </BrowserRouter>
      </ActorProvider>
    </QueryClientProvider>
  </React.StrictMode>,
);
```

- [ ] **Step 4: Temporary routes stub so the build passes**

Create `web/src/routes.tsx`:
```tsx
export function AppRoutes() {
  return <div style={{ padding: 24 }}>Cloud Steward console (scaffolding)</div>;
}
```

- [ ] **Step 5: Verify build**

Run: `npm --prefix web run build`
Expected: PASS (dist emitted). The old `App.tsx` is now unused but still compiles; it is removed in Task 12.

- [ ] **Step 6: Commit**

```bash
git add web/package.json web/package-lock.json web/src/lib/actor.tsx web/src/main.tsx web/src/routes.tsx
git commit -m "feat(web): add router/query/actor providers and deps"
```

---

## Task 2: Move api/i18n into lib and add actor to mutations

**Files:**
- Move: `web/src/api.ts` → `web/src/lib/api.ts`
- Move: `web/src/i18n.ts` → `web/src/lib/i18n.ts`
- Modify: `web/src/App.tsx` imports (temporary, until deletion)

- [ ] **Step 1: Move files**

Run:
```bash
git mv web/src/api.ts web/src/lib/api.ts
git mv web/src/i18n.ts web/src/lib/i18n.ts
```

- [ ] **Step 2: Add actor argument to mutating API functions**

In `web/src/lib/api.ts`, change these functions to accept `actor: string` and use it instead of the hardcoded `"local-user"`/`"approved in local console"` actor:

```ts
export function reconcileScan(
  scanId: string,
  actor: string,
): Promise<{ edge_count: number; candidate_count: number }> {
  return request(`/api/scans/${scanId}/reconcile`, {
    method: "POST",
    body: JSON.stringify({ actor }),
  });
}

export function createPlan(
  candidateIds: string[],
  actor: string,
  limits: PlanLimitsInput = {},
): Promise<CleanupPlan> {
  return request<CleanupPlan>("/api/plans", {
    method: "POST",
    body: JSON.stringify({
      candidate_ids: candidateIds,
      actor,
      max_resource_count: limits.maxResourceCount,
      max_region_count: limits.maxRegionCount,
      max_high_risk_count: limits.maxHighRiskCount ?? 0,
    }),
  });
}

export function approvePlan(
  id: string,
  actor: string,
  comment = "approved in local console",
): Promise<CleanupPlan> {
  return request<CleanupPlan>(`/api/plans/${id}/approve`, {
    method: "POST",
    body: JSON.stringify({ actor, comment }),
  });
}

export function executePlan(id: string, actor: string): Promise<CleanupPlan> {
  return request<CleanupPlan>(`/api/plans/${id}/execute`, {
    method: "POST",
    body: JSON.stringify({ actor }),
  });
}
```
`createScan` already carries no actor; leave its signature but keep it in this module. All read functions are unchanged.

- [ ] **Step 3: Fix the old App.tsx imports so the build still passes**

In `web/src/App.tsx`, update the two import paths and the now-changed call sites (this file is deleted in Task 12; this keeps the tree compiling in between):
- `from "./api"` → `from "./lib/api"`
- `from "./i18n"` → `from "./lib/i18n"`
- `reconcileScan(activeScanId)` → `reconcileScan(activeScanId, "local-user")`
- `createPlan(selectedCandidateIds, {...})` → `createPlan(selectedCandidateIds, "local-user", {...})`
- `approvePlan(id)` → `approvePlan(id, "local-user")`
- `executePlan(id)` → `executePlan(id, "local-user")`

- [ ] **Step 4: Verify build**

Run: `npm --prefix web run build`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src
git commit -m "refactor(web): move api/i18n to lib, thread actor through mutations"
```

---

## Task 3: Extract formatting helpers and shared badges

**Files:**
- Create: `web/src/lib/format.ts`
- Create: `web/src/components/StatusBadge.tsx`, `RiskBadge.tsx`, `Metric.tsx`, `EmptyState.tsx`, `Skeleton.tsx`

- [ ] **Step 1: Create lib/format.ts**

Move these helpers out of `App.tsx` into `web/src/lib/format.ts` (exported), keeping identical behavior:
```ts
import type { Copy } from "./i18n";

export function formatDate(value: string, copy: Copy) {
  if (!value) return "-";
  return new Intl.DateTimeFormat(copy.locale, {
    month: "short",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  }).format(new Date(value));
}

export function formatSavings(value: number, copy: Copy) {
  return `${value.toFixed(1)} ${copy.labels.perMonth}`;
}

export function shortID(value: string) {
  if (!value) return "-";
  return value.length <= 10 ? value : value.slice(0, 8);
}

export function numericLimit(value: string) {
  const parsed = Number.parseInt(value, 10);
  if (!Number.isFinite(parsed) || parsed < 0) return undefined;
  return parsed;
}

export function statusLabel(copy: Copy, value: string) {
  return copy.labels.statuses[value] ?? value;
}
export function riskLabel(copy: Copy, value: string) {
  return copy.labels.risks[value] ?? value;
}
export function actionLabel(copy: Copy, value: string) {
  return copy.labels.actions[value] ?? value;
}
export function resourceTypeLabel(copy: Copy, value: string) {
  return copy.labels.resourceTypes[value] ?? value;
}
export function modeLabel(copy: Copy, value: string) {
  return copy.labels.modes[value] ?? value;
}
```

- [ ] **Step 2: Create StatusBadge and RiskBadge**

`web/src/components/StatusBadge.tsx`:
```tsx
import type { Copy } from "../lib/i18n";
import { statusLabel } from "../lib/format";

export function StatusBadge({ value, copy }: { value: string; copy: Copy }) {
  return <span className={`status ${value}`}>{statusLabel(copy, value)}</span>;
}
```
`web/src/components/RiskBadge.tsx`:
```tsx
import type { Copy } from "../lib/i18n";
import { riskLabel } from "../lib/format";

export function RiskBadge({ value, copy }: { value: string; copy: Copy }) {
  return <span className={`risk ${value}`}>{riskLabel(copy, value)}</span>;
}
```

- [ ] **Step 3: Create Metric, EmptyState, Skeleton**

`web/src/components/Metric.tsx`:
```tsx
export function Metric({ label, value }: { label: string; value: string | number }) {
  return (
    <div className="metric">
      <div className="text-sm text-slate-600">{label}</div>
      <div className="mt-1 text-2xl font-semibold">{value}</div>
    </div>
  );
}
```
`web/src/components/EmptyState.tsx`:
```tsx
import type { ReactNode } from "react";

export function EmptyState({ title, hint, action }: { title: string; hint?: string; action?: ReactNode }) {
  return (
    <div className="empty-state">
      <div className="empty-state-title">{title}</div>
      {hint && <div className="empty-state-hint">{hint}</div>}
      {action && <div className="empty-state-action">{action}</div>}
    </div>
  );
}
```
`web/src/components/Skeleton.tsx`:
```tsx
export function Skeleton({ rows = 3 }: { rows?: number }) {
  return (
    <div className="skeleton" aria-busy="true">
      {Array.from({ length: rows }).map((_, i) => (
        <div key={i} className="skeleton-row" />
      ))}
    </div>
  );
}
```

- [ ] **Step 4: Move the Dropdown component**

Cut the `Dropdown` component (and its `DropdownOption` type) verbatim from `App.tsx` into `web/src/components/Dropdown.tsx`, exporting both. Update `App.tsx` to import it from `./components/Dropdown` and to import the format helpers from `./lib/format` (removing the now-duplicated local copies).

- [ ] **Step 5: Verify build**

Run: `npm --prefix web run build`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add web/src
git commit -m "refactor(web): extract format helpers and shared components"
```

---

## Task 4: i18n copy for the new shell and views

**Files:**
- Modify: `web/src/lib/i18n.ts`

- [ ] **Step 1: Extend the Copy type**

Add a `nav` group and a few new keys to the `Copy` type and both locale objects. Add to the type:
```ts
  nav: {
    overview: string;
    scans: string;
    resources: string;
    candidates: string;
    plans: string;
    reports: string;
    audit: string;
  };
  overview: {
    pipelineTitle: string;
    needsYou: string;
    scanned: string;
    analyzed: string;
    toReview: string;
    toApprove: string;
    executed: string;
    reviewCta: string;
    approveCta: string;
    failedScans: string;
    recentActivity: string;
  };
  guardrail: {
    impactTitle: string;
    resources: string;
    regions: string;
    highRisk: string;
    totalSavings: string;
    blockedSkipped: string;
    liveWarning: string;
    liveAck: string;
    confirmApprove: string;
    confirmExecute: string;
    cancel: string;
  };
  actorLabel: string;
  latestScan: string;
```
(`latestScan` may already exist under `fields`; if so reference `copy.fields.latestScan` in views instead of adding a duplicate — do not define it twice.)

- [ ] **Step 2: Fill en-US values**

```ts
  nav: { overview: "Overview", scans: "Scans", resources: "Resources", candidates: "Cleanup", plans: "Plans", reports: "Reports", audit: "Audit" },
  overview: {
    pipelineTitle: "Current scan progress",
    needsYou: "Needs you",
    scanned: "Scanned",
    analyzed: "Analyzed",
    toReview: "to review",
    toApprove: "to approve",
    executed: "Execute",
    reviewCta: "Review",
    approveCta: "Approve",
    failedScans: "scan failed",
    recentActivity: "Recent activity",
  },
  guardrail: {
    impactTitle: "Impact summary",
    resources: "Resources",
    regions: "Regions",
    highRisk: "High-risk items",
    totalSavings: "Estimated monthly savings",
    blockedSkipped: "Blocked items (will be skipped)",
    liveWarning: "This is a LIVE run and will act on real resources.",
    liveAck: "I understand this will modify live resources",
    confirmApprove: "Approve plan",
    confirmExecute: "Execute plan",
    cancel: "Cancel",
  },
  actorLabel: "Actor",
```

- [ ] **Step 3: Fill zh-CN values**

```ts
  nav: { overview: "概览", scans: "扫描", resources: "资源·拓扑", candidates: "清理项", plans: "计划", reports: "复盘", audit: "审计" },
  overview: {
    pipelineTitle: "当前扫描进度",
    needsYou: "待你处理",
    scanned: "已扫描",
    analyzed: "已分析",
    toReview: "待审查",
    toApprove: "待审批",
    executed: "执行",
    reviewCta: "去审查",
    approveCta: "去审批",
    failedScans: "扫描失败",
    recentActivity: "最近活动",
  },
  guardrail: {
    impactTitle: "影响摘要",
    resources: "资源",
    regions: "地域",
    highRisk: "高风险项",
    totalSavings: "预计每月可省",
    blockedSkipped: "被拦截项(将跳过)",
    liveWarning: "这是实操(LIVE)执行,会作用于真实资源。",
    liveAck: "我已知晓这会修改真实资源",
    confirmApprove: "确认审批",
    confirmExecute: "确认执行",
    cancel: "取消",
  },
  actorLabel: "操作者",
```

- [ ] **Step 4: Verify build**

Run: `npm --prefix web run build`
Expected: PASS (type errors surface any missed key in either locale).

- [ ] **Step 5: Commit**

```bash
git add web/src/lib/i18n.ts
git commit -m "feat(web): add i18n copy for redesigned shell and views"
```

---

## Task 5: Query hooks and active-scan resolution

**Files:**
- Create: `web/src/lib/queries.ts`
- Create: `web/src/lib/useActiveScan.ts`

- [ ] **Step 1: Query keys and read hooks**

Create `web/src/lib/queries.ts`:
```tsx
import { useQuery, useMutation, useQueryClient, keepPreviousData } from "@tanstack/react-query";
import * as api from "./api";
import { useActor } from "./actor";

export const keys = {
  scans: ["scans"] as const,
  resources: (scanId: string, type: string, query: string) => ["resources", scanId, type, query] as const,
  graph: (scanId: string) => ["graph", scanId] as const,
  candidates: (scanId: string) => ["candidates", scanId] as const,
  plans: ["plans"] as const,
  plan: (id: string) => ["plan", id] as const,
  audits: ["audits"] as const,
  savings: ["savings"] as const,
};

const anyRunning = (scans: api.ScanJob[]) =>
  scans.some((s) => s.status === "running" || s.status === "pending");

export function useScans() {
  return useQuery({
    queryKey: keys.scans,
    queryFn: api.listScans,
    refetchInterval: (q) => (q.state.data && anyRunning(q.state.data) ? 4000 : false),
  });
}

export function useResources(scanId: string, type: string, query: string) {
  return useQuery({
    queryKey: keys.resources(scanId, type, query),
    queryFn: () => api.listResources({ scanId, type, query }),
    placeholderData: keepPreviousData,
    enabled: scanId !== "",
  });
}

export function useGraph(scanId: string) {
  return useQuery({
    queryKey: keys.graph(scanId),
    queryFn: () => api.listGraph(scanId || undefined),
    placeholderData: keepPreviousData,
  });
}

export function useCandidates(scanId: string) {
  return useQuery({
    queryKey: keys.candidates(scanId),
    queryFn: () => api.listCandidates(scanId || undefined),
    placeholderData: keepPreviousData,
  });
}

export function usePlans() {
  return useQuery({
    queryKey: keys.plans,
    queryFn: api.listPlans,
    refetchInterval: (q) =>
      q.state.data?.some((p) => p.status === "running") ? 3000 : false,
  });
}

export function usePlan(id: string | undefined) {
  return useQuery({
    queryKey: keys.plan(id ?? ""),
    queryFn: () => api.getPlan(id as string),
    enabled: !!id,
    refetchInterval: (q) => (q.state.data?.plan.status === "running" ? 3000 : false),
  });
}

export function useAudits() {
  return useQuery({ queryKey: keys.audits, queryFn: api.listAudits });
}

export function useSavings() {
  return useQuery({ queryKey: keys.savings, queryFn: api.getSavingsReport });
}
```

- [ ] **Step 2: Mutation hooks**

Append to `web/src/lib/queries.ts`:
```tsx
export function useReconcile() {
  const qc = useQueryClient();
  const { actor } = useActor();
  return useMutation({
    mutationFn: (scanId: string) => api.reconcileScan(scanId, actor),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["graph"] });
      qc.invalidateQueries({ queryKey: ["candidates"] });
      qc.invalidateQueries({ queryKey: keys.savings });
    },
  });
}

export function useCreateScan() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.createScan,
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.scans }),
  });
}

export function useUpdateCandidate() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, status }: { id: string; status: api.CleanupCandidate["status"] }) =>
      api.updateCandidateStatus(id, status),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["candidates"] });
      qc.invalidateQueries({ queryKey: keys.savings });
    },
  });
}

export function useCreatePlan() {
  const qc = useQueryClient();
  const { actor } = useActor();
  return useMutation({
    mutationFn: ({ ids, limits }: { ids: string[]; limits: api.PlanLimitsInput }) =>
      api.createPlan(ids, actor, limits),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: keys.plans });
      qc.invalidateQueries({ queryKey: ["candidates"] });
    },
  });
}

export function useApprovePlan() {
  const qc = useQueryClient();
  const { actor } = useActor();
  return useMutation({
    mutationFn: (id: string) => api.approvePlan(id, actor),
    onSuccess: (_d, id) => {
      qc.invalidateQueries({ queryKey: keys.plans });
      qc.invalidateQueries({ queryKey: keys.plan(id) });
      qc.invalidateQueries({ queryKey: keys.audits });
    },
  });
}

export function useExecutePlan() {
  const qc = useQueryClient();
  const { actor } = useActor();
  return useMutation({
    mutationFn: (id: string) => api.executePlan(id, actor),
    onSuccess: (_d, id) => {
      qc.invalidateQueries({ queryKey: keys.plans });
      qc.invalidateQueries({ queryKey: keys.plan(id) });
      qc.invalidateQueries({ queryKey: keys.audits });
      qc.invalidateQueries({ queryKey: keys.savings });
    },
  });
}
```

- [ ] **Step 3: Active-scan hook**

Create `web/src/lib/useActiveScan.ts`:
```tsx
import { useSearchParams } from "react-router-dom";
import type { ScanJob } from "./api";

export function useActiveScan(scans: ScanJob[] | undefined) {
  const [params, setParams] = useSearchParams();
  const requested = params.get("scan") ?? "";
  const resolved = requested || scans?.[0]?.id || "";
  const setScan = (id: string) => {
    const next = new URLSearchParams(params);
    if (id) next.set("scan", id);
    else next.delete("scan");
    setParams(next, { replace: true });
  };
  return { activeScanId: resolved, requestedScanId: requested, setScan };
}
```

- [ ] **Step 4: Verify build**

Run: `npm --prefix web run build`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src/lib/queries.ts web/src/lib/useActiveScan.ts
git commit -m "feat(web): add TanStack Query hooks and active-scan resolution"
```

---

## Task 6: App shell (sidebar + top bar) and routes

**Files:**
- Create: `web/src/components/AppShell.tsx`, `Sidebar.tsx`, `TopBar.tsx`
- Modify: `web/src/routes.tsx`

- [ ] **Step 1: Sidebar**

`web/src/components/Sidebar.tsx` — nav ordered by lifecycle; `candidates` and `plans` show count badges passed as props. Use `NavLink` from react-router with `className` reflecting active state, and lucide icons (`LayoutDashboard`, `Radar`, `Network`, `ListChecks`, `ClipboardCheck`, `PiggyBank`/`FileClock` — reuse available lucide icons already imported elsewhere, e.g. `Network`, `ClipboardCheck`). Signature:
```tsx
export function Sidebar({ copy, openCandidates, pendingPlans }: { copy: Copy; openCandidates: number; pendingPlans: number }) { /* NavLink list */ }
```
Each item links to `/`, `/scans`, `/resources`, `/candidates`, `/plans`, `/reports`, `/audit`, preserving the current `?scan=` param via `useSearchParams` so context follows navigation.

- [ ] **Step 2: TopBar**

`web/src/components/TopBar.tsx` — a `ScanContextSwitcher` (reuse `Dropdown`: options are "latest" + each scan `account_name / status`, value bound to `?scan=` via `useActiveScan`'s `setScan`) and the actor identity from `useActor()` rendered as an inline editable field (a small text input bound to `setActor`). Signature:
```tsx
export function TopBar({ copy, scans, activeScanId, onScanChange }: { copy: Copy; scans: ScanJob[]; activeScanId: string; onScanChange: (id: string) => void }) { /* ... */ }
```

- [ ] **Step 3: AppShell**

`web/src/components/AppShell.tsx` — layout grid `[220px_1fr]`; loads shared data used by chrome (scans, candidates count, plans count) via hooks, resolves active scan, renders `Sidebar` + `TopBar` + `<Outlet />`. It provides the active scan via `Outlet` context:
```tsx
import { Outlet, useOutletContext } from "react-router-dom";
// ...
export type ShellContext = { copy: Copy; activeScanId: string; setScan: (id: string) => void; scans: ScanJob[] };
export function useShell() { return useOutletContext<ShellContext>(); }

export function AppShell() {
  const copy = useMemo(() => copyForBrowser(), []);
  const { data: scans = [] } = useScans();
  const { activeScanId, setScan } = useActiveScan(scans);
  const { data: candidates = [] } = useCandidates(activeScanId);
  const { data: plans = [] } = usePlans();
  const openCandidates = candidates.filter((c) => c.status === "open").length;
  const pendingPlans = plans.filter((p) => p.status === "pending_approval" || p.status === "draft").length;
  useEffect(() => { document.documentElement.lang = copy.htmlLang; }, [copy]);
  return (
    <div className="app-shell">
      <Sidebar copy={copy} openCandidates={openCandidates} pendingPlans={pendingPlans} />
      <div className="app-main">
        <TopBar copy={copy} scans={scans} activeScanId={activeScanId} onScanChange={setScan} />
        <div className="app-content">
          <Outlet context={{ copy, activeScanId, setScan, scans } satisfies ShellContext} />
        </div>
      </div>
    </div>
  );
}
```

- [ ] **Step 4: Routes**

Replace `web/src/routes.tsx`:
```tsx
import { Routes, Route } from "react-router-dom";
import { AppShell } from "./components/AppShell";
import { OverviewView } from "./features/overview/OverviewView";
import { ScansView } from "./features/scans/ScansView";
import { ResourcesView } from "./features/resources/ResourcesView";
import { CandidatesView } from "./features/candidates/CandidatesView";
import { PlansView } from "./features/plans/PlansView";
import { ReportsView } from "./features/reports/ReportsView";
import { AuditView } from "./features/audit/AuditView";

export function AppRoutes() {
  return (
    <Routes>
      <Route element={<AppShell />}>
        <Route index element={<OverviewView />} />
        <Route path="scans" element={<ScansView />} />
        <Route path="resources" element={<ResourcesView />} />
        <Route path="candidates" element={<CandidatesView />} />
        <Route path="plans" element={<PlansView />} />
        <Route path="plans/:id" element={<PlansView />} />
        <Route path="reports" element={<ReportsView />} />
        <Route path="audit" element={<AuditView />} />
      </Route>
    </Routes>
  );
}
```
Create minimal placeholder exports for each view now (`export function OverviewView() { return null; }` etc.) so this compiles; they are implemented in Tasks 7–11.

- [ ] **Step 5: Verify build**

Run: `npm --prefix web run build`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add web/src
git commit -m "feat(web): app shell, sidebar, top bar, route table"
```

---

## Task 7: Scans view and new-scan form

**Files:**
- Create: `web/src/features/scans/ScansView.tsx`, `NewScanForm.tsx`

- [ ] **Step 1: NewScanForm**

Extract the scan form from the old `App.tsx` (account input, demo/alicloud segmented control, regions, conditional AccessKey fields, submit). On submit call `useCreateScan().mutateAsync`, then `onCreated(job.id)` to set active scan. Keep the existing `copy.fields.*` labels and `segmented`/`field` classNames.

- [ ] **Step 2: ScansView**

Renders `NewScanForm` + a scan jobs table (account, mode, regions, status via `StatusBadge`, resource count, created via `formatDate`, plus an "Analyze" button per succeeded scan calling `useReconcile().mutate(scan.id)`). Row click calls `setScan(scan.id)` from `useShell()`. Empty state uses `EmptyState` with `copy.empty.noScans`. Show `Skeleton` while `useScans().isPending`.

- [ ] **Step 3: Verify build**

Run: `npm --prefix web run build`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add web/src/features/scans
git commit -m "feat(web): scans view and new-scan form"
```

---

## Task 8: Overview view

**Files:**
- Create: `web/src/features/overview/OverviewView.tsx`

- [ ] **Step 1: Implement OverviewView**

Uses `useShell()` for `copy`/`activeScanId`, and hooks `useScans`, `useCandidates(activeScanId)`, `usePlans`, `useSavings`, `useAudits`. Renders:
- Pipeline strip: `已扫描 N → 已分析 → n 待审查 → m 待审批 → 执行` using `copy.overview.*`; stage colors from resource count, edge/candidate presence, open-candidate count, pending-plan count.
- "Needs you" cards (only shown when count > 0), each a `Link`:
  - open candidates → `/candidates?scan=…` with total savings (sum of `estimated_monthly_savings` over open candidates).
  - plans pending approval → `/plans`.
  - failed scans → `/scans`.
- Secondary KPI `Metric` grid: monthly savings (`report.estimated_monthly_savings`), resource total, protected count.
- Recent activity: first 5 audits (time, actor, action) via `formatDate`.

- [ ] **Step 2: Verify build**

Run: `npm --prefix web run build`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add web/src/features/overview
git commit -m "feat(web): action-first overview"
```

---

## Task 9: Resources view — inventory, detail, topology graph

**Files:**
- Create: `web/src/features/resources/ResourcesView.tsx`, `ResourceDetail.tsx`, `TopologyGraph.tsx`

- [ ] **Step 1: ResourceDetail**

Port the existing `ResourceDetail` aside (name, native id, protected, tags grid, raw JSON `<pre>`) into its own file, props `{ resource: Resource | null; copy: Copy }`.

- [ ] **Step 2: Inventory tab**

In `ResourcesView`, a local tab state `"inventory" | "topology"`. Inventory: type `Dropdown` filter + debounced search input (local state, 300ms debounce → passed to `useResources`); table of resources with a row action that calls `getResource(id)` (or reuses row data) to set `selectedResource`; render `ResourceDetail` beside it. Use `keepPreviousData` behavior from the hook (no blanking).

- [ ] **Step 3: TopologyGraph with @xyflow/react**

`web/src/features/resources/TopologyGraph.tsx`, props `{ resources: Resource[]; edges: ResourceEdge[]; candidateIds: Set<string>; copy: Copy; onSelect: (r: Resource) => void }`:
```tsx
import { ReactFlow, Background, Controls, type Node, type Edge } from "@xyflow/react";
import { useMemo } from "react";
// build nodes with a simple grid layout keyed by index; color by resource.type;
// mark protected and candidate nodes with distinct className/style.
```
Layout: deterministic grid (`x = (i % cols) * 220`, `y = Math.floor(i / cols) * 120`). Node label = `resource.name` + type. Edges map `source_resource_id`→`target_resource_id` with `label: edge.type`. On `onNodeClick`, look up the resource and call `onSelect`; highlight the node's direct dependents by deriving them from `edges`. Include `<Background />` and `<Controls />`. Guard the empty case with `EmptyState` (`copy.empty.runAnalyze`).

- [ ] **Step 4: Wire topology tab**

The topology tab renders `TopologyGraph` in a fixed-height container (e.g. `height: 520px`) with `useResources` (unfiltered for the active scan) + `useGraph(activeScanId)` + candidate ids from `useCandidates`. Selecting a node opens the same `ResourceDetail` panel.

- [ ] **Step 5: Verify build**

Run: `npm --prefix web run build`
Expected: PASS. Confirm `@xyflow/react` types resolve.

- [ ] **Step 6: Commit**

```bash
git add web/src/features/resources
git commit -m "feat(web): resources inventory, detail, and interactive topology graph"
```

---

## Task 10: Candidates view (triage + plan assembly)

**Files:**
- Create: `web/src/features/candidates/CandidatesView.tsx`

- [ ] **Step 1: Implement CandidatesView**

Uses `useShell()`, `useCandidates(activeScanId)`, `useUpdateCandidate`, `useCreatePlan`, and `useNavigate`. Renders:
- Status `Dropdown` filter (`copy.filters.candidateStatuses`), default `open`.
- Candidate rows/cards: resource name + type, reason + rule id, recommended action (`actionLabel`), estimated savings (`formatSavings`), `RiskBadge`, per-row accept/ignore/snooze buttons calling `useUpdateCandidate().mutate({ id, status })`.
- Bulk selection checkboxes (enabled only for `status === "open"`), tracked in local `selectedIds` state.
- Sticky action bar (visible when `selectedIds.length > 0`): selected count, total savings, plan-limit inputs (`maxResourceCount`, `maxRegionCount`, `maxHighRiskCount` — default `"0"`), and an "Assemble plan" button calling `useCreatePlan().mutateAsync({ ids, limits })` (limits via `numericLimit`), then `navigate('/plans/' + plan.id + location.search)`.
- Empty state `copy.empty.noCandidates`; `Skeleton` while pending.

- [ ] **Step 2: Verify build**

Run: `npm --prefix web run build`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add web/src/features/candidates
git commit -m "feat(web): candidate triage and plan assembly"
```

---

## Task 11: Plans view, detail timeline, and guardrail dialogs

**Files:**
- Create: `web/src/features/plans/PlansView.tsx`, `PlanDetail.tsx`, `PlanActionDialog.tsx`
- Create: `web/src/components/ConfirmDialog.tsx`

- [ ] **Step 1: ConfirmDialog primitive**

`web/src/components/ConfirmDialog.tsx` — a normal-flow modal overlay (no `position: fixed`; use an overlay div in flow) with title, body (children), a confirm button (disabled until `canConfirm`), and cancel. Props:
```tsx
export function ConfirmDialog({ open, title, confirmLabel, cancelLabel, canConfirm = true, onConfirm, onCancel, children }: {
  open: boolean; title: string; confirmLabel: string; cancelLabel: string;
  canConfirm?: boolean; onConfirm: () => void; onCancel: () => void; children: React.ReactNode;
}) { /* returns null when !open */ }
```

- [ ] **Step 2: PlanActionDialog**

`web/src/features/plans/PlanActionDialog.tsx` — given a `PlanDetail` and a `kind: "approve" | "execute"`, renders `ConfirmDialog` with an impact summary computed from `detail`: resource count, distinct regions (from `item.resource.region`), high-risk count (`item.risk === "high"`), total savings, dry-run/live badge, and the list of blocked items (`item.blocked`). For `kind === "execute"` and `!detail.plan.dry_run`, show `copy.guardrail.liveWarning` and require an acknowledgement checkbox (`liveAck`) to enable confirm (`canConfirm`). On confirm, call the passed `onConfirm`.

- [ ] **Step 3: PlanDetail**

`web/src/features/plans/PlanDetail.tsx` — lifecycle timeline (created_by/created_at → approved_by + comment/approved_at → executed_at) and per-item list (name, action, risk, savings, blocked reason or result + request id), plus the existing Markdown/JSON export `<a>` links. Props `{ detail: PlanDetail | null; copy: Copy }`.

- [ ] **Step 4: PlansView**

Uses `usePlans`, `usePlan(routeId)`, `useApprovePlan`, `useExecutePlan`, and `useParams`/`useNavigate`. Renders the plans table (id + dry-run/live, `StatusBadge`, resource count, `RiskBadge`, savings, created) with row → `navigate('/plans/'+id+search)`. When `:id` is present, load `usePlan(id)` and render `PlanDetail` beside the table, with Approve/Execute buttons gated by `canApprove`/`canExecute` (port these two helpers into this file) that open `PlanActionDialog`. On dialog confirm, call the respective mutation and close.

- [ ] **Step 5: Verify build**

Run: `npm --prefix web run build`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add web/src
git commit -m "feat(web): plans list, detail timeline, and execution guardrails"
```

---

## Task 12: Reports, Audit, and removing the old monolith

**Files:**
- Create: `web/src/features/reports/ReportsView.tsx`, `web/src/features/audit/AuditView.tsx`
- Delete: `web/src/App.tsx`

- [ ] **Step 1: ReportsView**

Uses `useSavings`. Totals (candidates, plans, completed plans, monthly estimate) as `Metric` cards, plus by-type and by-team breakdowns rendered as labeled bars (compute each bar width as `value / max * 100%`). Empty rows use `copy.empty.noData`.

- [ ] **Step 2: AuditView**

Uses `useAudits`. Table (time, actor, action, target, result via `StatusBadge`, message) with CSV/JSON export `<a>` links. Replace the hard `.slice(0, 12)` with a "show more" that raises a local `limit` (start 20, +20 per click). Empty state `copy.empty.noAudits`.

- [ ] **Step 3: Delete App.tsx**

Run: `git rm web/src/App.tsx`
Verify no remaining imports reference it: `grep -rn "\"./App\"\|from \"\\./App\"" web/src` returns nothing (main.tsx already imports routes, not App).

- [ ] **Step 4: Verify build**

Run: `npm --prefix web run build && npm --prefix web run lint`
Expected: PASS both.

- [ ] **Step 5: Commit**

```bash
git add web/src
git commit -m "feat(web): reports and audit views; remove legacy App monolith"
```

---

## Task 13: Styles pass for the new shell and components

**Files:**
- Modify: `web/src/styles.css`

- [ ] **Step 1: Add layout and component styles**

Add CSS for `.app-shell` (grid `220px 1fr`, min-height 100vh), `.app-main`, `.app-content` (max-width, padding), `.sidebar` + `.nav-link`/`.nav-link.active` + `.nav-badge`, `.top-bar`, `.pipeline` strip + stage pills, `.needs-you-card`, `.empty-state`, `.skeleton`/`.skeleton-row` (subtle pulse), `.confirm-overlay`/`.confirm-dialog`, `.timeline`, `.bar`/`.bar-fill`. Reuse existing tokens (`bg-slate-50`, `text-ink`, `border-line`, `.panel`, `.status`, `.risk`, `.metric`) already defined in `styles.css`. Keep the existing color variables; do not restyle unrelated selectors.

- [ ] **Step 2: Verify build and visually check key selectors exist**

Run: `npm --prefix web run build`
Expected: PASS. Grep confirms new classes present: `grep -c "app-shell\|nav-link\|pipeline\|confirm-dialog" web/src/styles.css` ≥ 4.

- [ ] **Step 3: Commit**

```bash
git add web/src/styles.css
git commit -m "style(web): shell, sidebar, pipeline, dialog, and skeleton styles"
```

---

## Task 14: End-to-end manual smoke and final verification

**Files:** none (verification only)

- [ ] **Step 1: Full build + embed**

Run: `npm --prefix web run build`
Expected: dist emitted to `internal/webui/dist`.

- [ ] **Step 2: Backend build still green**

Run: `go build ./... && go test ./internal/webui/...`
Expected: PASS (static handler tests still serve index.html for `/plans/123`).

- [ ] **Step 3: Manual smoke**

Start `go run ./cmd/steward server start`, open `http://127.0.0.1:8585`, then:
- Create a demo scan on `/scans`; confirm it appears and reaches `succeeded` without a manual full-page refresh (polling only while running).
- Click Analyze; confirm candidates/graph populate.
- On `/resources`, switch to Topology; confirm nodes and edges render and clicking a node opens detail.
- On `/candidates`, accept one, select a few, assemble a plan; confirm navigation to `/plans/:id`.
- Approve, then Execute (dry-run); confirm the guardrail dialog shows the impact summary, and after execute the plan timeline and `/audit` update.
- Switch browser language between en and zh; confirm all new strings resolve.

- [ ] **Step 4: Final commit if any smoke fixes were needed**

```bash
git add -A && git commit -m "fix(web): address issues found during redesign smoke test"
```

---

## Self-Review Notes

- Spec coverage: shell/nav (T6), overview (T8), scans (T7), resources+topology (T9), candidates+assembly (T10), plans+guardrails (T11), reports+audit (T12), actor seam (T1–T2, T5 mutations), TanStack Query per-view polling (T5), i18n preserved+extended (T4), styles (T13), verification (T14). All spec sections mapped.
- Type consistency: `ShellContext`/`useShell`, `keys.*`, mutation hook names, and `useActiveScan` return shape are referenced consistently across T5–T12.
- No test-runner steps are used because `web/` has none; compile+build+manual-smoke is the repo's established gate (documented in the header).
