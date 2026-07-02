import { useEffect, useMemo } from "react";
import { Outlet, useOutletContext } from "react-router-dom";
import { copyForBrowser, type Copy } from "../lib/i18n";
import type { ScanJob } from "../lib/api";
import { useScans, useCandidates, usePlans } from "../lib/queries";
import { useActiveScan } from "../lib/useActiveScan";
import { Sidebar } from "./Sidebar";
import { TopBar } from "./TopBar";

export type ShellContext = {
  copy: Copy;
  activeScanId: string;
  setScan: (id: string) => void;
  scans: ScanJob[];
};

export function useShell() {
  return useOutletContext<ShellContext>();
}

export function AppShell() {
  const copy = useMemo(() => copyForBrowser(), []);
  const { data: scans = [] } = useScans();
  const { activeScanId, setScan } = useActiveScan(scans);
  const { data: candidates = [] } = useCandidates(activeScanId);
  const { data: plans = [] } = usePlans();

  const openCandidates = candidates.filter((c) => c.status === "open").length;
  const pendingPlans = plans.filter(
    (p) => p.status === "pending_approval" || p.status === "draft",
  ).length;

  useEffect(() => {
    document.documentElement.lang = copy.htmlLang;
  }, [copy]);

  return (
    <div className="app-shell">
      <Sidebar
        copy={copy}
        openCandidates={openCandidates}
        pendingPlans={pendingPlans}
      />
      <div className="app-main">
        <TopBar
          copy={copy}
          scans={scans}
          activeScanId={activeScanId}
          onScanChange={setScan}
        />
        <div className="app-content">
          <Outlet
            context={
              { copy, activeScanId, setScan, scans } satisfies ShellContext
            }
          />
        </div>
      </div>
    </div>
  );
}
