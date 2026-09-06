import { Search } from "lucide-react";
import { NavLink, useLocation } from "react-router-dom";
import { useAuth } from "@/auth/AuthProvider";
import { useActiveConnection } from "@/connections/ActiveConnectionProvider";
import { useCleanupSelection } from "@/features/panorama/CleanupSelectionContext";
import { useLocale } from "@/i18n/LocaleProvider";
import type { MessageKey } from "@/i18n/messages";
import { Button } from "@/components/ui/button";
import { StewardBrand } from "@/components/StewardBrand";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarTrigger,
  useSidebar,
} from "@/components/ui/sidebar";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { navigationItems, type NavigationGroup } from "./navigation";
import { SidebarUserMenu } from "./SidebarUserMenu";

const groups: Array<{
  value: Exclude<NavigationGroup, "global">;
  label: MessageKey;
}> = [
  { value: "resources", label: "nav.group.resources" },
  { value: "operations", label: "nav.group.operations" },
];

export function AppSidebar({
  sidebarExpanded = true,
  onSearch = () => undefined,
}: {
  sidebarExpanded?: boolean;
  onSearch?: () => void;
}) {
  const auth = useAuth();
  const location = useLocation();
  const { connections, activeConnectionID, loading } = useActiveConnection();
  const { requestConnectionChange } = useCleanupSelection();
  const { t } = useLocale();
  const { isMobile, setOpenMobile } = useSidebar();
  const subject = auth.displayName ?? auth.principal?.subject ?? t("auth.authenticated");
  const searchLabel = t("command.open");
  const sidebarToggleLabel = sidebarExpanded
    ? t("shell.collapseSidebar")
    : t("shell.expandSidebar");
  const closeMobileSidebar = () => setOpenMobile(false);
  const openSearch = () => {
    if (isMobile) closeMobileSidebar();
    onSearch();
  };

  return (
    <Sidebar collapsible="icon" variant="sidebar">
      <SidebarHeader className="gap-2 p-2">
        <div className="flex h-9 items-center gap-2 group-data-[collapsible=icon]:justify-center">
          <NavLink
            to="/panorama"
            aria-label="Steward"
            onClick={closeMobileSidebar}
            className="flex min-w-0 flex-1 items-center gap-2 overflow-hidden rounded-md px-1 text-sm font-semibold tracking-tight group-data-[collapsible=icon]:hidden"
          >
            <StewardBrand className="min-w-0" />
          </NavLink>
          {(sidebarExpanded || isMobile) && (
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  className="shrink-0"
                  onClick={openSearch}
                  aria-label={searchLabel}
                >
                  <Search />
                </Button>
              </TooltipTrigger>
              <TooltipContent side="right" sideOffset={6}>
                {searchLabel}
              </TooltipContent>
            </Tooltip>
          )}
          {!isMobile && (
            <Tooltip>
              <TooltipTrigger asChild>
                <SidebarTrigger
                  className="shrink-0"
                  aria-label={sidebarToggleLabel}
                />
              </TooltipTrigger>
              <TooltipContent side="right" sideOffset={6}>
                {sidebarToggleLabel}
              </TooltipContent>
            </Tooltip>
          )}
        </div>
        {!sidebarExpanded && !isMobile && (
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="ghost"
                size="icon-sm"
                className="mx-auto shrink-0"
                onClick={openSearch}
                aria-label={searchLabel}
              >
                <Search />
              </Button>
            </TooltipTrigger>
            <TooltipContent side="right" sideOffset={6}>
              {searchLabel}
            </TooltipContent>
          </Tooltip>
        )}
        <div className="group-data-[collapsible=icon]:hidden">
          {connections.length > 0 ? (
            <Select
              value={activeConnectionID}
              onValueChange={requestConnectionChange}
              disabled={loading}
            >
              <SelectTrigger
                className="h-9 w-full bg-sidebar-accent/40"
                aria-label={t("connectionContext.label")}
              >
                <SelectValue placeholder={t("connectionContext.label")} />
              </SelectTrigger>
              <SelectContent>
                {connections.map((connection) => (
                  <SelectItem key={connection.id} value={connection.id}>
                    {connection.name} · {connection.provider}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          ) : (
            <NavLink
              to="/settings"
              onClick={closeMobileSidebar}
              className="block rounded-md border border-dashed p-2 text-xs text-muted-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground"
            >
              {loading ? t("common.loading") : t("connectionContext.configure")}
            </NavLink>
          )}
        </div>
      </SidebarHeader>
      <SidebarContent>
        {groups.map((group) => (
          <SidebarGroup key={group.value}>
            <SidebarGroupLabel>{t(group.label)}</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu>
                {navigationItems
                  .filter((item) => item.group === group.value)
                  .map((item) => {
                    const active =
                      location.pathname === item.to ||
                      location.pathname.startsWith(`${item.to}/`);
                    return (
                      <SidebarMenuItem key={item.to}>
                        <SidebarMenuButton
                          asChild
                          isActive={active}
                          tooltip={t(item.label)}
                        >
                          <NavLink to={item.to} onClick={closeMobileSidebar}>
                            <item.icon />
                            <span>{t(item.label)}</span>
                          </NavLink>
                        </SidebarMenuButton>
                      </SidebarMenuItem>
                    );
                  })}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        ))}
      </SidebarContent>
      <SidebarFooter>
        <SidebarUserMenu
          subject={subject}
          roles={auth.principal?.roles ?? []}
          automaticDevelopmentSession={auth.automaticDevelopmentSession}
          onSignOut={auth.logout}
          workspaceLabel={auth.mode === "cloud" ? t("auth.workspaces") : undefined}
          labels={{
            settings: t("nav.settings"),
            signOut: t("auth.signOut"),
            identityPending: t("auth.identityPending"),
          }}
        />
      </SidebarFooter>
    </Sidebar>
  );
}
