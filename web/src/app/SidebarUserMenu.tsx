import { Boxes, LogOut, Settings } from "lucide-react";
import { NavLink } from "react-router-dom";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { useSidebar } from "@/components/ui/sidebar";

export interface SidebarUserMenuProps {
  subject: string;
  roles: string[];
  automaticDevelopmentSession: boolean;
  onSignOut: () => void;
  workspaceLabel?: string;
  labels: {
    settings: string;
    signOut: string;
    identityPending: string;
  };
}

export function SidebarUserMenu({
  subject,
  roles,
  automaticDevelopmentSession,
  onSignOut,
  workspaceLabel,
  labels,
}: SidebarUserMenuProps) {
  const { setOpenMobile } = useSidebar();
  const identity = roles.join(", ") || labels.identityPending;

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          className="h-auto w-full justify-start gap-2 px-2 py-2 group-data-[collapsible=icon]:size-9 group-data-[collapsible=icon]:p-0"
          aria-label={subject}
        >
          <Avatar className="size-8 shrink-0">
            <AvatarFallback className="bg-sidebar-accent text-sidebar-accent-foreground">
              {subject.slice(0, 2).toUpperCase()}
            </AvatarFallback>
          </Avatar>
          <span className="min-w-0 text-left group-data-[collapsible=icon]:hidden">
            <span className="block truncate text-xs font-medium">
              {subject}
            </span>
            <span className="block truncate text-[11px] text-sidebar-foreground/70">
              {identity}
            </span>
          </span>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="start"
        side="top"
        sideOffset={8}
        className="w-64"
      >
        <DropdownMenuLabel>
          <span className="block truncate">{subject}</span>
          <span className="block truncate text-xs font-normal text-sidebar-foreground/70">
            {identity}
          </span>
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        {workspaceLabel && (
          <DropdownMenuItem asChild>
            <a href="/workspaces"><Boxes />{workspaceLabel}</a>
          </DropdownMenuItem>
        )}
        <DropdownMenuItem asChild>
          <NavLink to="/settings" onClick={() => setOpenMobile(false)}>
            <Settings />
            {labels.settings}
          </NavLink>
        </DropdownMenuItem>
        {!automaticDevelopmentSession && (
          <DropdownMenuItem onSelect={onSignOut}>
            <LogOut />
            {labels.signOut}
          </DropdownMenuItem>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
