import { User } from "lucide-react";
import type { Copy } from "../lib/i18n";
import type { ScanJob } from "../lib/api";
import { statusLabel } from "../lib/format";
import { useActor } from "../lib/actor";
import { Dropdown } from "./Dropdown";

export function TopBar({
  copy,
  scans,
  activeScanId,
  onScanChange,
}: {
  copy: Copy;
  scans: ScanJob[];
  activeScanId: string;
  onScanChange: (id: string) => void;
}) {
  const { actor, setActor } = useActor();
  const options = [
    { value: "", label: copy.fields.latestScan },
    ...scans.map((scan) => ({
      value: scan.id,
      label: `${scan.account_name} / ${statusLabel(copy, scan.status)}`,
    })),
  ];
  const selected = scans.find((s) => s.id === activeScanId);
  return (
    <header className="top-bar">
      <div className="top-bar-context">
        <Dropdown
          label={copy.nav.scans}
          value={selected ? activeScanId : ""}
          options={options}
          onValueChange={onScanChange}
        />
        {selected && (
          <span className="top-bar-regions">{selected.regions.join(", ")}</span>
        )}
      </div>
      <label className="top-bar-actor">
        <User size={15} />
        <span className="top-bar-actor-label">{copy.actorLabel}</span>
        <input
          value={actor}
          onChange={(event) => setActor(event.target.value)}
          aria-label={copy.actorLabel}
        />
      </label>
    </header>
  );
}
