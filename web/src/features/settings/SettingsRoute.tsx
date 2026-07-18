import { useSearchParams } from "react-router-dom";
import { SettingsView } from "./SettingsView";
import type { SettingsSection } from "./SettingsNavigation";

const settingsSections: SettingsSection[] = [
  "general",
  "connections",
  "account",
];

function settingsSection(value: string | null): SettingsSection {
  return settingsSections.includes(value as SettingsSection)
    ? (value as SettingsSection)
    : "general";
}

export function SettingsRoute() {
  const [searchParams, setSearchParams] = useSearchParams();
  const section = settingsSection(searchParams.get("section"));

  return (
    <SettingsView
      section={section}
      onSectionChange={(nextSection) => {
        setSearchParams((current) => {
          const next = new URLSearchParams(current);
          if (nextSection === "general") next.delete("section");
          else next.set("section", nextSection);
          return next;
        });
      }}
    />
  );
}
