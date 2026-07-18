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

const sidebarStorageKey = "steward.sidebar-expanded";

export interface WorkspaceInspectorContent {
  title: string;
  description?: string;
  body: ReactNode;
}

export interface CloseInspectorOptions {
  restoreFocus?: boolean;
}

export interface OpenInspectorOptions {
  resolveReturnFocus?: () => HTMLElement | null;
}

export interface WorkspaceContextValue {
  sidebarExpanded: boolean;
  setSidebarExpanded: (expanded: boolean) => void;
  commandOpen: boolean;
  setCommandOpen: (open: boolean) => void;
  inspector: WorkspaceInspectorContent | null;
  openInspector: (
    content: WorkspaceInspectorContent,
    options?: OpenInspectorOptions,
  ) => void;
  closeInspector: (options?: CloseInspectorOptions) => void;
}

const WorkspaceContext = createContext<WorkspaceContextValue | null>(null);

export function WorkspaceProvider({ children }: { children: ReactNode }) {
  const [sidebarExpanded, setSidebarExpandedState] = useState(
    () => localStorage.getItem(sidebarStorageKey) !== "false",
  );
  const [commandOpen, setCommandOpen] = useState(false);
  const [inspector, setInspector] = useState<WorkspaceInspectorContent | null>(
    null,
  );
  const inspectorReturnFocus = useRef<HTMLElement | null>(null);
  const inspectorReturnFocusResolver = useRef<
    (() => HTMLElement | null) | null
  >(null);
  const inspectorGeneration = useRef(0);

  const setSidebarExpanded = useCallback((expanded: boolean) => {
    localStorage.setItem(sidebarStorageKey, String(expanded));
    setSidebarExpandedState(expanded);
  }, []);

  const openInspector = useCallback(
    (
      content: WorkspaceInspectorContent,
      options: OpenInspectorOptions = {},
    ) => {
      inspectorGeneration.current += 1;
      inspectorReturnFocus.current =
        document.activeElement instanceof HTMLElement
          ? document.activeElement
          : null;
      inspectorReturnFocusResolver.current = options.resolveReturnFocus ?? null;
      setInspector(content);
    },
    [],
  );

  const closeInspector = useCallback((options: CloseInspectorOptions = {}) => {
    const generation = ++inspectorGeneration.current;
    const returnFocus = inspectorReturnFocus.current;
    const resolveReturnFocus = inspectorReturnFocusResolver.current;
    setInspector(null);
    if (options.restoreFocus === false) {
      inspectorReturnFocus.current = null;
      inspectorReturnFocusResolver.current = null;
      return;
    }
    queueMicrotask(() => {
      if (inspectorGeneration.current !== generation) return;
      inspectorReturnFocus.current = null;
      inspectorReturnFocusResolver.current = null;
      const target = returnFocus?.isConnected
        ? returnFocus
        : resolveReturnFocus?.();
      if (!target?.isConnected) return;
      target.focus();
    });
  }, []);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      const target = event.target;
      const isTyping =
        target instanceof HTMLInputElement ||
        target instanceof HTMLTextAreaElement ||
        target instanceof HTMLSelectElement ||
        (target instanceof HTMLElement && target.isContentEditable);
      if (isTyping) return;
      if (event.key.toLowerCase() === "k" && (event.metaKey || event.ctrlKey)) {
        event.preventDefault();
        setCommandOpen((open) => !open);
        return;
      }
      if (event.key === "Escape") setCommandOpen(false);
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, []);

  const value = useMemo<WorkspaceContextValue>(
    () => ({
      sidebarExpanded,
      setSidebarExpanded,
      commandOpen,
      setCommandOpen,
      inspector,
      openInspector,
      closeInspector,
    }),
    [
      closeInspector,
      commandOpen,
      inspector,
      openInspector,
      setSidebarExpanded,
      sidebarExpanded,
    ],
  );

  return (
    <WorkspaceContext.Provider value={value}>
      {children}
    </WorkspaceContext.Provider>
  );
}

export function useWorkspace() {
  const value = useContext(WorkspaceContext);
  if (!value) throw new Error("useWorkspace requires WorkspaceProvider");
  return value;
}
