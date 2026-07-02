import type { Copy } from "../lib/i18n";
import { statusLabel } from "../lib/format";

export function StatusBadge({ value, copy }: { value: string; copy: Copy }) {
  return <span className={`status ${value}`}>{statusLabel(copy, value)}</span>;
}
