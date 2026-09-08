import type { CleanupSelector } from "../../api/types";

export type ConfirmationMode = "type_name" | "confirm";

export interface CleanupReviewSummary {
  resolved: number;
  controllerSteps: number;
  directSteps: number;
  retained: number;
  blockers: number;
  possibleBilling: number;
}

export interface CleanupSelectionHandoff {
  version: 1;
  selectors: CleanupSelector[];
}

export function cleanupReviewSummary(input: {
  task: {
    resolved_asset_ids: readonly string[];
    blockers?: readonly { code: string }[];
  };
  steps: readonly { kind: string }[];
  impact_items: readonly {
    expected: string;
    may_continue_billing: boolean;
  }[];
}): CleanupReviewSummary {
  const retainedOutcomes = new Set([
    "retain_shared",
    "retain_explicit",
    "provider_default_retain",
  ]);
  return {
    resolved: input.task.resolved_asset_ids.length,
    controllerSteps: input.steps.filter((step) => step.kind === "controller")
      .length,
    directSteps: input.steps.filter((step) => step.kind === "direct").length,
    retained: input.impact_items.filter((impact) =>
      retainedOutcomes.has(impact.expected),
    ).length,
    blockers: input.task.blockers?.length ?? 0,
    possibleBilling: input.impact_items.filter(
      (impact) => impact.may_continue_billing,
    ).length,
  };
}

export function confirmationMode(selector: {
  kind: string;
  scope_kind?: string;
}): ConfirmationMode {
  if (selector.kind === "connection") return "type_name";
  if (
    selector.kind === "scope" &&
    (selector.scope_kind === "account" || selector.scope_kind === "project" || selector.scope_kind === "subscription" || selector.scope_kind === "region")
  ) {
    return "type_name";
  }
  return "confirm";
}

export function typedConfirmationNames(selectors: CleanupSelector[]): string[] {
  return [
    ...new Set(
      selectors
        .filter((selector) => confirmationMode(selector) === "type_name")
        .map((selector) =>
          (selector.display_name || selectorIdentity(selector)).trim(),
        )
        .filter((value): value is string => !!value),
    ),
  ].sort();
}

export function executionConfirmationSatisfied({
  requiredNames,
  typedNames,
  acknowledgementRequired,
  acknowledged,
}: {
  requiredNames: readonly string[];
  typedNames: Readonly<Record<string, string>>;
  acknowledgementRequired: boolean;
  acknowledged: boolean;
}): boolean {
  return (
    requiredNames.every((name) => typedNames[name] === name) &&
    (!acknowledgementRequired || acknowledged)
  );
}

export function parseRequestOptions(
  value: string,
): Record<string, Record<string, unknown>> {
  const parsed = JSON.parse(value) as unknown;
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error("request options must be an object");
  }
  return parsed as Record<string, Record<string, unknown>>;
}

function selectorIdentity(selector: CleanupSelector): string {
  switch (selector.kind) {
    case "connection":
      return selector.connection_id;
    case "scope":
      return selector.scope_id;
    case "group":
      return selector.group_key;
    case "asset":
      return selector.asset_id;
  }
}

export function selectorKey(selector: CleanupSelector): string {
  switch (selector.kind) {
    case "connection":
      return `connection:${selector.connection_id}`;
    case "scope":
      return `scope:${selector.scope_id}`;
    case "group":
      return `group:${selector.group_key}`;
    case "asset":
      return `asset:${selector.asset_id}`;
  }
}

export function dedupeSelectors(values: CleanupSelector[]): CleanupSelector[] {
  return [
    ...new Map(values.map((value) => [selectorKey(value), value])).values(),
  ];
}

function cleanupSelectionHandoffKey(connectionID: string): string {
  return `steward:cleanup-cln-handoff:v1:${connectionID}`;
}

const invalidatedCleanupSelectionHandoffs = new Set<string>();

export function writeCleanupSelectionHandoff(
  connectionID: string,
  selectors: readonly CleanupSelector[],
): boolean {
  try {
    sessionStorage.setItem(
      cleanupSelectionHandoffKey(connectionID),
      JSON.stringify({
        version: 1,
        selectors: [...selectors],
      } satisfies CleanupSelectionHandoff),
    );
    invalidatedCleanupSelectionHandoffs.delete(connectionID);
    return true;
  } catch {
    invalidatedCleanupSelectionHandoffs.add(connectionID);
    removeStoredCleanupSelectionHandoff(connectionID);
    return false;
  }
}

export function readCleanupSelectionHandoff(
  connectionID: string,
): CleanupSelector[] {
  if (invalidatedCleanupSelectionHandoffs.has(connectionID)) {
    if (removeStoredCleanupSelectionHandoff(connectionID)) {
      invalidatedCleanupSelectionHandoffs.delete(connectionID);
    }
    return [];
  }
  const key = cleanupSelectionHandoffKey(connectionID);
  try {
    const stored = sessionStorage.getItem(key);
    if (stored === null) return [];
    const parsed = JSON.parse(stored) as unknown;
    if (isCleanupSelectionHandoff(parsed, connectionID)) {
      return dedupeSelectors(parsed.selectors);
    }
  } catch {
    // Treat unavailable or corrupt storage as an empty handoff.
  }
  clearCleanupSelectionHandoff(connectionID);
  return [];
}

export function clearCleanupSelectionHandoff(connectionID: string): void {
  if (removeStoredCleanupSelectionHandoff(connectionID)) {
    invalidatedCleanupSelectionHandoffs.delete(connectionID);
  } else {
    invalidatedCleanupSelectionHandoffs.add(connectionID);
  }
}

function removeStoredCleanupSelectionHandoff(connectionID: string): boolean {
  try {
    sessionStorage.removeItem(cleanupSelectionHandoffKey(connectionID));
    return true;
  } catch {
    return false;
  }
}

function isCleanupSelectionHandoff(
  value: unknown,
  connectionID: string,
): value is CleanupSelectionHandoff {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const envelope = value as Record<string, unknown>;
  const selectors = envelope.selectors;
  return (
    envelope.version === 1 &&
    Array.isArray(selectors) &&
    selectors.every((selector) =>
      isCleanupSelectionSelector(selector, connectionID),
    )
  );
}

function isCleanupSelectionSelector(
  value: unknown,
  connectionID: string,
): value is CleanupSelector {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const selector = value as Record<string, unknown>;
  const optionalString = (field: unknown) =>
    field === undefined || typeof field === "string";
  if (!optionalString(selector.display_name)) return false;
  const belongsToConnection =
    selector.connection_id === undefined ||
    selector.connection_id === connectionID;
  switch (selector.kind) {
    case "connection":
      return selector.connection_id === connectionID;
    case "scope":
      return (
        typeof selector.scope_id === "string" &&
        optionalString(selector.connection_id) &&
        belongsToConnection &&
        optionalString(selector.scope_kind) &&
        (selector.descendants === undefined ||
          typeof selector.descendants === "boolean")
      );
    case "group":
      return (
        typeof selector.group_key === "string" &&
        optionalString(selector.connection_id) &&
        belongsToConnection &&
        optionalString(selector.scope_id)
      );
    case "asset":
      return typeof selector.asset_id === "string";
    default:
      return false;
  }
}

export function encodeSelector(selector: CleanupSelector): string {
  const bytes = new TextEncoder().encode(JSON.stringify(selector));
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary)
    .replaceAll("+", "-")
    .replaceAll("/", "_")
    .replace(/=+$/, "");
}

export function decodeSelector(value: string): CleanupSelector | null {
  try {
    const normalized = value.trim().replaceAll("-", "+").replaceAll("_", "/");
    if (!normalized || !/^[A-Za-z0-9+/]+$/.test(normalized)) return null;
    const padded = normalized.padEnd(
      normalized.length + ((4 - (normalized.length % 4)) % 4),
      "=",
    );
    const binary = atob(padded);
    const bytes = Uint8Array.from(binary, (character) =>
      character.charCodeAt(0),
    );
    const parsed = JSON.parse(new TextDecoder().decode(bytes)) as unknown;
    return isSelector(parsed) ? parsed : null;
  } catch {
    return null;
  }
}

function isSelector(value: unknown): value is CleanupSelector {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const selector = value as Record<string, unknown>;
  switch (selector.kind) {
    case "connection":
      return typeof selector.connection_id === "string";
    case "scope":
      return typeof selector.scope_id === "string";
    case "group":
      return typeof selector.group_key === "string";
    case "asset":
      return typeof selector.asset_id === "string";
    default:
      return false;
  }
}
