import { Laptop, Moon, Sun } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { useTheme, type Theme } from "./ThemeProvider";

export function ThemeToggle({
  labels,
}: {
  labels: Record<Theme | "toggle", string>;
}) {
  const { setTheme } = useTheme();
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon" aria-label={labels.toggle}>
          <Sun className="size-4 scale-100 rotate-0 dark:scale-0 dark:-rotate-90" />
          <Moon className="absolute size-4 scale-0 rotate-90 dark:scale-100 dark:rotate-0" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <ThemeItem
          icon={Sun}
          label={labels.light}
          onSelect={() => setTheme("light")}
        />
        <ThemeItem
          icon={Moon}
          label={labels.dark}
          onSelect={() => setTheme("dark")}
        />
        <ThemeItem
          icon={Laptop}
          label={labels.system}
          onSelect={() => setTheme("system")}
        />
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function ThemeItem({
  icon: Icon,
  label,
  onSelect,
}: {
  icon: typeof Sun;
  label: string;
  onSelect: () => void;
}) {
  return (
    <DropdownMenuItem onSelect={onSelect}>
      <Icon className="size-4" />
      {label}
    </DropdownMenuItem>
  );
}
