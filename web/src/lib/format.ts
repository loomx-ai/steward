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
