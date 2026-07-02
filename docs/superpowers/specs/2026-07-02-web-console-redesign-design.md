# Cloud Steward Web Console Redesign

## Scope

The current web console is a single 1364-line `web/src/App.tsx` that renders every stage of the governance workflow (scan, resources, graph, candidates, plans, savings, audit) stacked on one long page of dense tables. It has no task flow, no visual hierarchy, no guardrails on irreversible actions, and it reloads all data every 5 seconds (plus on input blur), which resets selection and interrupts the operator.

This redesign restructures the console around the governance lifecycle. It is an information-architecture rework, not a restyle. The backend API contract is unchanged; this spec covers only `web/`.

The governance lifecycle the UI must express:

```
scan → analyze → review candidates → assemble plan → approve → execute → report
```

Out of scope: backend/API changes, real authentication/RBAC (only the actor-context seam is added), and any change to the Go server, executor, or persistence.

## Goals

1. Give the operator a clear answer to "what do I do now?" on every screen.
2. Make the topology (the product's differentiator) a real interactive graph.
3. Add guardrails to irreversible cleanup execution (impact summary + confirmation, live vs dry-run clearly flagged).
4. Replace the 5s full-refresh polling with per-view stale-while-revalidate queries that never blank the screen or drop selection.
5. Prepare for operator/approver role separation without building full RBAC now.
6. Preserve the existing bilingual (en-US / zh-CN) i18n.

## Non-Goals

- No full RBAC, login, or multi-tenant auth.
- No backend or API signature changes.
- No marketing/landing surfaces — this stays an operational console.

## Architecture

The app becomes a routed multi-view SPA. Directory layout under `web/src/`:

- `lib/api.ts` — existing typed API client, unchanged signatures. Extended only so mutating calls take an `actor` argument instead of hardcoding `"local-user"`.
- `lib/i18n.ts` — existing copy dictionary, extended with new strings for the redesigned views. `Copy` type and `copyForBrowser()` preserved.
- `lib/actor.tsx` — a React context holding the current actor identity (default `"local-user"`), read by mutating API calls and shown in the top bar. This is the seam for future auth.
- `lib/queries.ts` — TanStack Query hooks (`useScans`, `useResources`, `useGraph`, `useCandidates`, `usePlans`, `usePlan`, `useAudits`, `useSavings`) plus mutation hooks. Query keys are scoped by the active scan where relevant. Polling is per-query: only in-flight work (scans with status `running`/`pending`, plans with status `running`) uses a `refetchInterval`; everything else refetches on window focus and after related mutations. `keepPreviousData` prevents blanking on refetch.
- `components/` — shared primitives: `AppShell` (sidebar + top bar), `Sidebar`, `TopBar`, `ScanContextSwitcher`, `Metric`, `StatusBadge`, `RiskBadge`, `Dropdown` (reuse existing accessible dropdown), `ConfirmDialog`, `Toast`, `EmptyState`, `Skeleton`, `DataTable`.
- `features/` — one folder per view, each owning its screen component and view-local pieces:
  - `overview/` — action-first home.
  - `scans/` — scan list plus the "new scan" form.
  - `resources/` — inventory table + resource detail + topology graph tab.
  - `candidates/` — triage list, bulk selection, plan assembly.
  - `plans/` — plan list, plan detail timeline, approve/execute with guardrails.
  - `reports/` — savings report.
  - `audit/` — audit log with export.
- `routes.tsx` — react-router route table mounting each feature under `AppShell`.
- `App.tsx` — thin: providers (QueryClientProvider, ActorProvider, i18n) + router.

New dependencies: `react-router-dom`, `@tanstack/react-query`, `@xyflow/react`. Keep `tailwindcss`, `lucide-react`, existing Vite/TS toolchain.

## Navigation & Shell

Left sidebar, fixed, ordered by lifecycle with count badges where actionable:

`概览 / Overview` · `扫描 / Scans` · `资源·拓扑 / Resources` · `清理项 / Cleanup (n)` · `计划 / Plans (n)` · `复盘 / Reports` · `审计 / Audit`

Top bar contains:
- Scan context switcher (account + regions of the active scan; drives scan-scoped queries). Selecting "latest" tracks the newest scan.
- Actor identity (from `ActorProvider`, editable, default `local-user`).
- Language indicator (existing auto-detect; toggle optional, may defer).

Routes: `/` → overview, `/scans`, `/resources`, `/candidates`, `/plans`, `/plans/:id`, `/reports`, `/audit`. The active scan id lives in a URL search param (`?scan=`) so views deep-link and survive refresh.

## Views

### Overview (`/`)
Answers "what needs me now." Contains:
- Pipeline progress strip for the active scan: `已扫描 N → 已分析 → n 待审查 → m 待审批 → 执行`, each stage colored by state.
- "待你处理 / Needs you" cards, each linking to the relevant view: open candidates (with total estimated savings), plans awaiting approval, failed scans.
- Secondary KPI cards: potential monthly savings, resource total, protected count.
- Recent activity: last few audit events.

### Scans (`/scans`)
- "New scan" form (account, demo vs Alibaba Cloud segmented control, regions, AccessKey fields when Alibaba Cloud) — reused from current form, moved here.
- Scan jobs table with status, resource count, failure reason, an "Analyze" action per succeeded scan (calls reconcile), and row click sets active scan.

### Resources (`/resources`)
Two tabs sharing the active-scan context:
- Inventory: filterable/searchable table (type filter + text query, debounced — no blur-triggered global refresh). Row → resource detail panel (name, native id, protected, tags, raw JSON).
- Topology: interactive `@xyflow/react` graph. Nodes = resources colored by type, badged when protected, highlighted when they are a cleanup candidate. Edges = derived dependency edges (`ResourceEdge`) labeled by relation type. Clicking a node opens the detail panel and highlights its dependents ("deleting this affects…"). Filter by type; toggle "highlight candidates."

### Cleanup / Candidates (`/candidates`)
- Status filter (open / accepted / ignored / snoozed / all), default open.
- Candidate rows as scannable cards: resource name + type, reason + rule id, recommended action, estimated savings, risk badge, per-row accept/ignore/snooze.
- Bulk checkbox selection (open candidates only) → sticky action bar showing selected count and total savings → "Assemble plan" with inline plan limits (max resources, max regions, high-risk cap). Creating a plan navigates to `/plans/:id`.

### Plans (`/plans`, `/plans/:id`)
- List: id, dry-run/live badge, status, resource count, risk, savings, created — row → detail.
- Detail: lifecycle timeline (created by → approved by + comment → executed), per-item list with action, risk, savings, blocked reason or result + request id, and Markdown/JSON export links.
- Guardrails:
  - Approve and Execute are explicit buttons gated by plan status (`canApprove`, `canExecute` preserved).
  - Both open a `ConfirmDialog` showing an impact summary: X resources, Y regions, Z high-risk, total estimated savings, dry-run vs live prominently, and the list of blocked items that will be skipped.
  - For a live (non-dry-run) execute, the confirm requires an explicit second acknowledgement (checkbox or typed confirm) before the execute button enables.

### Reports (`/reports`)
Savings report: totals (candidates, plans, completed plans, monthly estimate) plus breakdown by type and by team, rendered as labeled bars rather than a bare list.

### Audit (`/audit`)
Audit event table (time, actor, action, target, result, message) with CSV/JSON export links. Paginated or capped with a "view more" affordance rather than a hard slice of 12.

## Interaction Principles

- Confirmation dialogs only for irreversible/high-stakes actions (execute, especially live). Accept/ignore/snooze and plan creation are reversible → immediate with toast feedback.
- Toasts report success and error with the returned message; errors never blank the view.
- Empty states teach the next step (e.g., "No candidates yet — run Analyze on a succeeded scan") with a CTA, not an empty table.
- Loading uses skeletons; refetches keep previous data visible (no spinner-reset).
- Numbers are formatted through the existing `formatSavings`/locale helpers.

## Data Flow

1. `AppShell` mounts providers. `ActorProvider` supplies the actor; `QueryClientProvider` supplies the cache.
2. The active scan id comes from `?scan=` (falling back to the latest scan). Scan-scoped queries key on it.
3. Views read data through TanStack Query hooks. Only running scans/plans poll; others refetch on focus and after mutations invalidate their keys.
4. Mutations (create scan, reconcile, update candidate, create/approve/execute plan) read `actor` from context, call the existing API, then invalidate affected query keys so dependent views update without a global reload.
5. Export links remain plain `<a href>` to existing export endpoints.

## Testing

- `npm --prefix web run build` (tsc + vite) must pass — the primary gate, matching the repo's existing verification step.
- `npm --prefix web run lint` (tsc `--noEmit`) must pass.
- Manual smoke against a running `steward server start`: run a demo scan → analyze → review candidates → assemble plan → approve → execute (dry-run) → confirm the plan timeline and audit log update, and that the topology graph renders edges for the active scan.
- Verify no regression in bilingual copy: switch browser locale between en and zh and confirm all new strings resolve through `Copy`.

## Migration Notes

- `App.tsx` is decomposed; no behavior is dropped — every current panel maps to a view above.
- The `Dropdown` component and formatting/label helpers (`statusLabel`, `riskLabel`, `actionLabel`, `resourceTypeLabel`, `formatDate`, `formatSavings`, `shortID`) are extracted into `components/` and `lib/` and reused.
- Hardcoded `"local-user"` actors in `lib/api.ts` are replaced by the actor argument sourced from `ActorProvider`.
