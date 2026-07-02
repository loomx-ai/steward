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
