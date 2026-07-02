import type { Copy } from "../lib/i18n";
import { riskLabel } from "../lib/format";

export function RiskBadge({ value, copy }: { value: string; copy: Copy }) {
  return <span className={`risk ${value}`}>{riskLabel(copy, value)}</span>;
}
