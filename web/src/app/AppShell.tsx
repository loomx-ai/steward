import { Outlet, useLocation } from "react-router-dom";
import {
  ActiveConnectionProvider,
  useActiveConnection,
} from "@/connections/ActiveConnectionProvider";
import { useLocale } from "@/i18n/LocaleProvider";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { SidebarInset, SidebarProvider } from "@/components/ui/sidebar";
import { InspectorSheet } from "@/components/patterns/InspectorSheet";
import { CleanupSelectionProvider } from "@/features/panorama/CleanupSelectionContext";
import { AppHeader } from "./AppHeader";
import { AppSidebar } from "./AppSidebar";
import { CommandMenu } from "./CommandMenu";
import { ConnectionGate } from "./ConnectionGate";
import { PageHeaderActionsProvider } from "./PageHeaderActionsContext";
import { PageTitleProvider } from "./PageTitleContext";
import { RouteErrorBoundary } from "./RouteErrorBoundary";
import { WorkspaceProvider, useWorkspace } from "./WorkspaceContext";

export function AppShell() {
  return (
    <ActiveConnectionProvider>
      <CleanupSelectionProvider>
        <WorkspaceProvider>
          <PageHeaderActionsProvider>
            <PageTitleProvider>
              <ShellFrame />
            </PageTitleProvider>
          </PageHeaderActionsProvider>
        </WorkspaceProvider>
      </CleanupSelectionProvider>
    </ActiveConnectionProvider>
  );
}

function ShellFrame() {
  const { sidebarExpanded, setSidebarExpanded, setCommandOpen } =
    useWorkspace();
  return (
    <SidebarProvider open={sidebarExpanded} onOpenChange={setSidebarExpanded}>
      <AppSidebar
        sidebarExpanded={sidebarExpanded}
        onSearch={() => setCommandOpen(true)}
      />
      <SidebarInset className="min-w-0 overflow-hidden">
        <AppHeader />
        <main className="min-h-0 min-w-0 flex-1 overflow-auto bg-background">
          <ConnectionBoundary />
        </main>
      </SidebarInset>
      <CommandMenu />
      <InspectorSheet />
    </SidebarProvider>
  );
}

function ConnectionBoundary() {
  const location = useLocation();
  const { activeConnection, error, loading, retry } = useActiveConnection();
  const { t } = useLocale();
  const isGlobalPage = location.pathname === "/settings";

  if (isGlobalPage)
    return (
      <RouteErrorBoundary key={location.pathname}>
        <Outlet />
      </RouteErrorBoundary>
    );
  if (loading) {
    return (
      <div className="space-y-4 p-5 sm:p-7" aria-busy="true">
        <Skeleton className="h-8 w-52" />
        <Skeleton className="h-4 w-96 max-w-full" />
        <Skeleton className="h-72 w-full rounded-xl" />
      </div>
    );
  }
  if (error) {
    return (
      <div className="mx-auto grid min-h-[60vh] max-w-md place-items-center px-6 py-12 text-center">
        <div className="space-y-4">
          <h2 className="text-lg font-semibold">
            {t("shell.connectionError")}
          </h2>
          <p className="text-sm leading-relaxed text-muted-foreground">
            {error.message}
          </p>
          <Button onClick={retry}>{t("shell.retry")}</Button>
        </div>
      </div>
    );
  }
  if (!activeConnection) return <ConnectionGate />;
  return (
    <RouteErrorBoundary key={location.pathname}>
      <Outlet />
    </RouteErrorBoundary>
  );
}
