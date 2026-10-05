import { lazy, Suspense, useEffect } from "react";
import { Navigate, Route, Routes } from "react-router-dom";
import { AppShell } from "./app/AppShell";
import { LoginView } from "./auth/LoginView";
import { RequireAuth, useAuth } from "./auth/AuthProvider";
import { AssetsView } from "./features/assets/AssetsView";

const AssetDetail = lazy(() =>
  import("./features/assets/AssetDetail").then((module) => ({
    default: module.AssetDetail,
  })),
);
const ScansView = lazy(() =>
  import("./features/scans/ScansView").then((module) => ({
    default: module.ScansView,
  })),
);
const ScanTaskView = lazy(() =>
  import("./features/scans/ScanTaskView").then((module) => ({
    default: module.ScanTaskView,
  })),
);
const ScheduleDetail = lazy(() =>
  import("./features/schedules/ScheduleDetail").then((module) => ({
    default: module.ScheduleDetail,
  })),
);
const CleanupView = lazy(() =>
  import("./features/cleanup/CleanupView").then((module) => ({
    default: module.CleanupView,
  })),
);
const CleanupTaskDetail = lazy(() =>
  import("./features/cleanup/CleanupTaskDetail").then((module) => ({
    default: module.CleanupTaskDetail,
  })),
);
const FindingsView = lazy(() =>
  import("./features/findings/FindingsView").then((module) => ({
    default: module.FindingsView,
  })),
);
const AuditsView = lazy(() =>
  import("./features/audits/AuditsView").then((module) => ({
    default: module.AuditsView,
  })),
);
const SettingsRoute = lazy(() =>
  import("./features/settings/SettingsRoute").then((module) => ({
    default: module.SettingsRoute,
  })),
);
const loadPanoramaView = () => import("./features/panorama/PanoramaView");
const PanoramaView = lazy(() =>
  loadPanoramaView().then((module) => ({
    default: module.PanoramaView,
  })),
);

export function AppRoutes() {
  const { authenticated } = useAuth();
  // Panorama is the landing page, so fetch its canvas chunk as soon as the
  // session is authenticated, but not for the login page.
  useEffect(() => {
    if (authenticated) void loadPanoramaView().catch(() => undefined);
  }, [authenticated]);
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
          <Route path="scans/schedules/:id" element={<ScheduleDetail />} />
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
