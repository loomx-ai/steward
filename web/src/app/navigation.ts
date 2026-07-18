import type { LucideIcon } from "lucide-react";
import {
  Boxes,
  ClipboardCheck,
  FileSearch,
  History,
  Radar,
  Settings,
  Waypoints,
} from "lucide-react";
import type { MessageKey } from "@/i18n/messages";

export type NavigationGroup = "resources" | "operations" | "global";

export interface NavigationItem {
  to: string;
  label: MessageKey;
  group?: NavigationGroup;
  icon: LucideIcon;
}

export const navigationItems: NavigationItem[] = [
  {
    to: "/panorama",
    label: "nav.panorama",
    group: "resources",
    icon: Waypoints,
  },
  { to: "/assets", label: "nav.assets", group: "resources", icon: Boxes },
  { to: "/scans", label: "nav.scans", group: "operations", icon: Radar },
  {
    to: "/cleanup",
    label: "nav.cleanup",
    group: "operations",
    icon: ClipboardCheck,
  },
  {
    to: "/findings",
    label: "nav.findings",
    icon: FileSearch,
  },
  {
    to: "/audits",
    label: "nav.audit",
    group: "operations",
    icon: History,
  },
  {
    to: "/settings",
    label: "nav.settings",
    group: "global",
    icon: Settings,
  },
];

export function navigationItemForPath(pathname: string) {
  return navigationItems
    .filter(
      (item) => pathname === item.to || pathname.startsWith(`${item.to}/`),
    )
    .sort((left, right) => right.to.length - left.to.length)[0];
}
