import { cn } from "@/lib/utils";

export type SettingsSection = "general" | "connections" | "account";

export function SettingsNavigation({
  value,
  onValueChange,
  labels,
}: {
  value: SettingsSection;
  onValueChange: (value: SettingsSection) => void;
  labels: Record<SettingsSection, string>;
}) {
  const items: SettingsSection[] = ["general", "connections", "account"];
  return (
    <nav
      aria-label={labels.general}
      className="flex gap-1 overflow-x-auto md:w-max md:flex-col"
    >
      {items.map((item) => (
        <button
          key={item}
          type="button"
          aria-current={value === item ? "page" : undefined}
          className={cn(
            "h-9 shrink-0 whitespace-nowrap rounded-lg px-3 text-left text-sm transition-colors duration-[120ms] hover:bg-muted",
            value === item && "bg-muted font-medium",
          )}
          onClick={() => onValueChange(item)}
        >
          {labels[item]}
        </button>
      ))}
    </nav>
  );
}
