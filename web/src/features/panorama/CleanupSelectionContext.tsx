import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { toast } from "sonner";
import type { CleanupSelector } from "@/api/types";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { useActiveConnection } from "@/connections/ActiveConnectionProvider";
import { useLocale } from "@/i18n/LocaleProvider";
import {
  addCleanupTargets,
  removeCleanupBatchMember,
  removeCleanupTarget,
  type CleanupMergeResult,
  type CleanupTarget,
  type CleanupTargetKind,
} from "./cleanupSelection";

interface StoredCleanupSelection {
  version: 1;
  targets: CleanupTarget[];
}

interface CleanupSelectionContextValue {
  hydratedConnectionID: string | null;
  targets: CleanupTarget[];
  addTargets: (targets: readonly CleanupTarget[]) => CleanupMergeResult;
  removeTarget: (targetKey: string) => void;
  removeBatchMember: (targetKey: string, assetID: string) => void;
  clearTargets: () => void;
  consumeTargets: (
    connectionID: string,
    expectedTargets: readonly CleanupTarget[],
  ) => boolean;
  requestConnectionChange: (nextConnectionID: string) => void;
}

interface ConnectionSelection {
  connectionID: string;
  hydrated: boolean;
  targets: CleanupTarget[];
}

const targetKinds = new Set<CleanupTargetKind>([
  "region",
  "vpc",
  "resource",
  "resource_batch",
]);

const CleanupSelectionContext = createContext<
  CleanupSelectionContextValue | undefined
>(undefined);

export function cleanupSelectionStorageKey(connectionID: string): string {
  return `steward:panorama-cleanup:v1:${connectionID}`;
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isOptionalString(value: unknown): boolean {
  return value === undefined || typeof value === "string";
}

function isTopologyViewContext(value: unknown): boolean {
  return (
    isObject(value) &&
    typeof value.key === "string" &&
    typeof value.name === "string" &&
    isOptionalString(value.native_id)
  );
}

function isTargetLocationContext(value: unknown): boolean {
  return (
    isObject(value) &&
    (value.region === undefined || isTopologyViewContext(value.region)) &&
    (value.vpc === undefined || isTopologyViewContext(value.vpc)) &&
    (value.scope === undefined || isTopologyViewContext(value.scope))
  );
}

function selectorBelongsToConnection(
  value: Record<string, unknown>,
  connectionID: string,
): boolean {
  return (
    value.connection_id === undefined || value.connection_id === connectionID
  );
}

function isCleanupSelector(
  value: unknown,
  connectionID: string,
): value is CleanupSelector {
  if (!isObject(value) || !isOptionalString(value.display_name)) return false;
  switch (value.kind) {
    case "connection":
      return (
        typeof value.connection_id === "string" &&
        value.connection_id === connectionID
      );
    case "scope":
      return (
        typeof value.scope_id === "string" &&
        isOptionalString(value.connection_id) &&
        selectorBelongsToConnection(value, connectionID) &&
        isOptionalString(value.scope_kind) &&
        (value.descendants === undefined ||
          typeof value.descendants === "boolean")
      );
    case "group":
      return (
        typeof value.group_key === "string" &&
        isOptionalString(value.connection_id) &&
        selectorBelongsToConnection(value, connectionID) &&
        isOptionalString(value.scope_id)
      );
    case "asset":
      return typeof value.asset_id === "string";
    default:
      return false;
  }
}

function isCleanupTarget(
  value: unknown,
  connectionID: string,
): value is CleanupTarget {
  if (!isObject(value)) return false;
  if (
    typeof value.key !== "string" ||
    typeof value.kind !== "string" ||
    !targetKinds.has(value.kind as CleanupTargetKind) ||
    value.connectionId !== connectionID ||
    typeof value.displayName !== "string" ||
    !Array.isArray(value.ancestryKeys) ||
    !value.ancestryKeys.every((key) => typeof key === "string") ||
    (value.locationContext !== undefined &&
      !isTargetLocationContext(value.locationContext))
  ) {
    return false;
  }
  const selectors = Array.isArray(value.selector)
    ? value.selector
    : [value.selector];
  if (
    selectors.length === 0 ||
    !selectors.every((selector) => isCleanupSelector(selector, connectionID))
  ) {
    return false;
  }
  if (
    value.memberAssetIds !== undefined &&
    (!Array.isArray(value.memberAssetIds) ||
      !value.memberAssetIds.every((id) => typeof id === "string"))
  ) {
    return false;
  }
  return (
    value.resourceCount === undefined ||
    (typeof value.resourceCount === "number" &&
      Number.isInteger(value.resourceCount) &&
      value.resourceCount >= 0)
  );
}

function isStoredCleanupSelection(
  value: unknown,
  connectionID: string,
): value is StoredCleanupSelection {
  return (
    isObject(value) &&
    value.version === 1 &&
    Array.isArray(value.targets) &&
    value.targets.every((target) => isCleanupTarget(target, connectionID))
  );
}

function restoreTargets(
  connectionID: string,
  warnPersistenceFailure: () => void,
): CleanupTarget[] {
  if (!connectionID) return [];
  const key = cleanupSelectionStorageKey(connectionID);
  try {
    const stored = sessionStorage.getItem(key);
    if (stored === null) return [];
    const parsed: unknown = JSON.parse(stored);
    if (isStoredCleanupSelection(parsed, connectionID)) {
      return parsed.targets;
    }
    try {
      sessionStorage.removeItem(key);
    } catch {
      // The warning below covers the rejected restore attempt.
    }
    warnPersistenceFailure();
  } catch {
    try {
      sessionStorage.removeItem(key);
    } catch {
      // The read failure already produces the persistence warning below.
    }
    warnPersistenceFailure();
  }
  return [];
}

export function CleanupSelectionProvider({
  children,
}: {
  children: ReactNode;
}) {
  const { activeConnectionID, setActiveConnectionID } = useActiveConnection();
  const { t } = useLocale();
  const translationRef = useRef(t);
  translationRef.current = t;
  const warnPersistenceFailure = useCallback(() => {
    toast.warning(translationRef.current("panorama.cleanupPersistenceFailed"));
  }, []);
  const [selection, setSelectionState] = useState<ConnectionSelection>({
    connectionID: activeConnectionID,
    hydrated: false,
    targets: [],
  });
  const selectionRef = useRef(selection);
  const [pendingConnectionID, setPendingConnectionID] = useState<string | null>(
    null,
  );

  const setSelection = useCallback((next: ConnectionSelection) => {
    selectionRef.current = next;
    setSelectionState(next);
  }, []);

  useEffect(() => {
    setSelection({
      connectionID: activeConnectionID,
      hydrated: true,
      targets: restoreTargets(activeConnectionID, warnPersistenceFailure),
    });
    setPendingConnectionID(null);
  }, [activeConnectionID, setSelection, warnPersistenceFailure]);

  const currentTargets =
    selection.connectionID === activeConnectionID && selection.hydrated
      ? selection.targets
      : [];
  const hydratedConnectionID =
    selection.connectionID === activeConnectionID && selection.hydrated
      ? activeConnectionID
      : null;

  const persistTargets = useCallback(
    (targets: CleanupTarget[]) => {
      setSelection({
        connectionID: activeConnectionID,
        hydrated: true,
        targets,
      });
      if (!activeConnectionID) return;
      try {
        sessionStorage.setItem(
          cleanupSelectionStorageKey(activeConnectionID),
          JSON.stringify({ version: 1, targets }),
        );
      } catch {
        warnPersistenceFailure();
      }
    },
    [activeConnectionID, setSelection, warnPersistenceFailure],
  );

  const addTargets = useCallback(
    (incoming: readonly CleanupTarget[]) => {
      const existing =
        selectionRef.current.connectionID === activeConnectionID
          ? selectionRef.current.targets
          : [];
      const result = addCleanupTargets(existing, incoming);
      persistTargets(result.targets);
      if (result.coveredCount > 0) {
        toast.info(translationRef.current("panorama.targetAlreadyCovered"));
      }
      if (result.mergedCount > 0) {
        toast.info(
          translationRef.current("panorama.targetsMerged", {
            count: result.mergedCount,
          }),
        );
      }
      return result;
    },
    [activeConnectionID, persistTargets],
  );

  const removeTarget = useCallback(
    (targetKey: string) => {
      const existing =
        selectionRef.current.connectionID === activeConnectionID
          ? selectionRef.current.targets
          : [];
      persistTargets(
        removeCleanupTarget(existing, targetKey, activeConnectionID),
      );
    },
    [activeConnectionID, persistTargets],
  );

  const removeBatchMember = useCallback(
    (targetKey: string, assetID: string) => {
      const existing =
        selectionRef.current.connectionID === activeConnectionID
          ? selectionRef.current.targets
          : [];
      persistTargets(
        removeCleanupBatchMember(
          existing,
          targetKey,
          assetID,
          activeConnectionID,
        ),
      );
    },
    [activeConnectionID, persistTargets],
  );

  const clearTargets = useCallback(() => {
    persistTargets([]);
  }, [persistTargets]);

  const consumeTargets = useCallback(
    (
      expectedConnectionID: string,
      expectedTargets: readonly CleanupTarget[],
    ) => {
      const current = selectionRef.current;
      if (
        activeConnectionID !== expectedConnectionID ||
        current.connectionID !== expectedConnectionID ||
        !current.hydrated ||
        current.targets !== expectedTargets
      ) {
        return false;
      }
      persistTargets([]);
      return true;
    },
    [activeConnectionID, persistTargets],
  );

  const requestConnectionChange = useCallback(
    (nextConnectionID: string) => {
      if (nextConnectionID === activeConnectionID) return;
      const existing =
        selectionRef.current.connectionID === activeConnectionID
          ? selectionRef.current.targets
          : [];
      if (existing.length === 0) {
        setActiveConnectionID(nextConnectionID);
        return;
      }
      setPendingConnectionID(nextConnectionID);
    },
    [activeConnectionID, setActiveConnectionID],
  );

  const confirmConnectionChange = useCallback(() => {
    if (pendingConnectionID === null) return;
    if (activeConnectionID) {
      try {
        sessionStorage.removeItem(
          cleanupSelectionStorageKey(activeConnectionID),
        );
      } catch {
        warnPersistenceFailure();
      }
    }
    setSelection({
      connectionID: activeConnectionID,
      hydrated: true,
      targets: [],
    });
    const nextConnectionID = pendingConnectionID;
    setPendingConnectionID(null);
    setActiveConnectionID(nextConnectionID);
  }, [
    activeConnectionID,
    pendingConnectionID,
    setActiveConnectionID,
    setSelection,
    warnPersistenceFailure,
  ]);

  const value = useMemo<CleanupSelectionContextValue>(
    () => ({
      hydratedConnectionID,
      targets: currentTargets,
      addTargets,
      removeTarget,
      removeBatchMember,
      clearTargets,
      consumeTargets,
      requestConnectionChange,
    }),
    [
      addTargets,
      clearTargets,
      consumeTargets,
      currentTargets,
      hydratedConnectionID,
      removeBatchMember,
      removeTarget,
      requestConnectionChange,
    ],
  );

  return (
    <CleanupSelectionContext.Provider value={value}>
      {children}
      <AlertDialog
        open={pendingConnectionID !== null}
        onOpenChange={(open) => {
          if (!open) setPendingConnectionID(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("panorama.switchConnectionTitle")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("panorama.switchConnectionDescription", {
                count: currentTargets.length,
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>
              {t("panorama.switchConnectionCancel")}
            </AlertDialogCancel>
            <AlertDialogAction onClick={confirmConnectionChange}>
              {t("panorama.switchConnectionConfirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </CleanupSelectionContext.Provider>
  );
}

export function useCleanupSelection(): CleanupSelectionContextValue {
  const value = useContext(CleanupSelectionContext);
  if (!value) {
    throw new Error("useCleanupSelection requires CleanupSelectionProvider");
  }
  return value;
}
