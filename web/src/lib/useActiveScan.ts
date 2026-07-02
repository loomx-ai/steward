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
