import { lazy, Suspense } from "react";
import { Navigate, Route, Routes } from "react-router-dom";
import { AppShell } from "./app/AppShell";
import { LoginView } from "./auth/LoginView";
import { RequireAuth } from "./auth/AuthProvider";
import { AssetsView } from "./features/assets/AssetsView";
import { ScansView } from "./features/scans/ScansView";
import { ScanTaskView } from "./features/scans/ScanTaskView";
import { CleanupView } from "./features/cleanup/CleanupView";
import { CleanupTaskDetail } from "./features/cleanup/CleanupTaskDetail";
import { FindingsView } from "./features/findings/FindingsView";
import { AuditsView } from "./features/audits/AuditsView";
import { SettingsRoute } from "./features/settings/SettingsRoute";

const AssetDetail = lazy(() =>
  import("./features/assets/AssetDetail").then((module) => ({
    default: module.AssetDetail,
  })),
);
const panoramaViewModule = import("./features/panorama/PanoramaView");
void panoramaViewModule.catch(() => undefined);
const PanoramaView = lazy(() =>
  panoramaViewModule.then((module) => ({
    default: module.PanoramaView,
  })),
);

export function AppRoutes() {
  return (
    <Suspense fallback={<div className="route-loading" aria-busy="true" />}>
      <Routes>
        <Route path="login" element={<LoginView />} />
        <Route
          element={
            <RequireAuth>
              <AppShell />
            </RequireAuth>
          }
        >
          <Route index element={<Navigate to="/panorama" replace />} />
          <Route path="panorama" element={<PanoramaView />} />
          <Route path="panorama/global" element={<PanoramaView />} />
          <Route path="panorama/regions/:regionId" element={<PanoramaView />} />
          <Route
            path="panorama/regions/:regionId/public"
            element={<PanoramaView />}
          />
          <Route
            path="panorama/regions/:regionId/vpcs/:vpcId"
            element={<PanoramaView />}
          />
          <Route path="assets" element={<AssetsView />} />
          <Route path="assets/:id" element={<AssetDetail />} />
          <Route path="scans" element={<ScansView />} />
          <Route path="scans/:id" element={<ScanTaskView />} />
          <Route path="cleanup" element={<CleanupView />} />
          <Route path="cleanup/new" element={<CleanupView />} />
          <Route path="cleanup/:id" element={<CleanupTaskDetail />} />
          <Route
            path="executions"
            element={<Navigate to="/cleanup" replace />}
          />
          <Route
            path="executions/:id"
            element={<Navigate to="/cleanup" replace />}
          />
          <Route path="findings" element={<FindingsView />} />
          <Route path="audits" element={<AuditsView />} />
          <Route path="settings" element={<SettingsRoute />} />
          <Route path="*" element={<Navigate to="/panorama" replace />} />
        </Route>
      </Routes>
    </Suspense>
  );
}
