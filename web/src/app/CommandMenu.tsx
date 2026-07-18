import { ChevronsLeftRight, Cloud } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { useActiveConnection } from "@/connections/ActiveConnectionProvider";
import { useCleanupSelection } from "@/features/panorama/CleanupSelectionContext";
import { useLocale } from "@/i18n/LocaleProvider";
import {
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandShortcut,
} from "@/components/ui/command";
import { navigationItems } from "./navigation";
import { useWorkspace } from "./WorkspaceContext";

export function CommandMenu() {
  const navigate = useNavigate();
  const { t } = useLocale();
  const { commandOpen, setCommandOpen, sidebarExpanded, setSidebarExpanded } =
    useWorkspace();
  const { connections, activeConnectionID } = useActiveConnection();
  const { requestConnectionChange } = useCleanupSelection();
  const run = (action: () => void) => {
    action();
    setCommandOpen(false);
  };
  return (
    <CommandDialog
      open={commandOpen}
      onOpenChange={setCommandOpen}
      title={t("command.title")}
      description={t("command.description")}
    >
      <CommandInput placeholder={t("command.search")} />
      <CommandList>
        <CommandEmpty>{t("command.empty")}</CommandEmpty>
        <CommandGroup heading={t("command.navigation")}>
          {navigationItems.map((item) => (
            <CommandItem
              key={item.to}
              value={`${t(item.label)} ${item.to}`}
              onSelect={() => run(() => navigate(item.to))}
            >
              <item.icon />
              {t(item.label)}
            </CommandItem>
          ))}
          <CommandItem
            onSelect={() => run(() => setSidebarExpanded(!sidebarExpanded))}
          >
            <ChevronsLeftRight />
            {sidebarExpanded
              ? t("shell.collapseSidebar")
              : t("shell.expandSidebar")}
            <CommandShortcut>⌘B</CommandShortcut>
          </CommandItem>
        </CommandGroup>
        {connections.length > 0 && (
          <CommandGroup heading={t("connectionContext.label")}>
            {connections.map((connection) => (
              <CommandItem
                key={connection.id}
                value={`${connection.name} ${connection.provider}`}
                onSelect={() =>
                  run(() => requestConnectionChange(connection.id))
                }
              >
                <Cloud />
                <span>{connection.name}</span>
                <CommandShortcut>
                  {connection.id === activeConnectionID
                    ? t("command.current")
                    : connection.provider}
                </CommandShortcut>
              </CommandItem>
            ))}
          </CommandGroup>
        )}
      </CommandList>
    </CommandDialog>
  );
}
