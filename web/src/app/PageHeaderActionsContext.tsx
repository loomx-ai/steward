import {
  createContext,
  useContext,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { cn } from "@/lib/utils";

interface PageHeaderActionsContextValue {
  target: HTMLDivElement | null;
  navigationTarget: HTMLDivElement | null;
  setTarget: (target: HTMLDivElement | null) => void;
  setNavigationTarget: (target: HTMLDivElement | null) => void;
}

const PageHeaderActionsContext =
  createContext<PageHeaderActionsContextValue | null>(null);

export function PageHeaderActionsProvider({
  children,
}: {
  children: ReactNode;
}) {
  const [target, setTarget] = useState<HTMLDivElement | null>(null);
  const [navigationTarget, setNavigationTarget] =
    useState<HTMLDivElement | null>(null);
  const value = useMemo(
    () => ({ target, navigationTarget, setTarget, setNavigationTarget }),
    [navigationTarget, target],
  );

  return (
    <PageHeaderActionsContext.Provider value={value}>
      {children}
    </PageHeaderActionsContext.Provider>
  );
}

export function PageHeaderNavigationTarget() {
  const context = useContext(PageHeaderActionsContext);

  return (
    <div
      ref={context?.setNavigationTarget}
      data-slot="page-header-navigation"
      className="flex min-w-0 flex-1 items-center empty:hidden"
    />
  );
}

export function PageHeaderActionsTarget({
  ariaLabel,
  className,
}: {
  ariaLabel: string;
  className?: string;
}) {
  const context = useContext(PageHeaderActionsContext);

  return (
    <div
      ref={context?.setTarget}
      role="toolbar"
      aria-label={ariaLabel}
      data-slot="page-header-actions"
      className={cn(
        "flex min-w-0 shrink-0 items-center justify-end gap-2 empty:hidden",
        className,
      )}
    />
  );
}

export function usePageHeaderActionsTarget() {
  return useContext(PageHeaderActionsContext)?.target ?? null;
}

export function usePageHeaderNavigationTarget() {
  return useContext(PageHeaderActionsContext)?.navigationTarget ?? null;
}

export function usePageHeaderActionsEnabled() {
  return useContext(PageHeaderActionsContext) !== null;
}
