import { NavLink, useLocation } from "react-router-dom";
import {
  LayoutDashboard,
  Radar,
  Network,
  ListChecks,
  ClipboardCheck,
  PiggyBank,
  ScrollText,
} from "lucide-react";
import type { ComponentType } from "react";
import type { Copy } from "../lib/i18n";

type Item = {
  to: string;
  label: string;
  Icon: ComponentType<{ size?: number }>;
  badge?: number;
};

export function Sidebar({
  copy,
  openCandidates,
  pendingPlans,
}: {
  copy: Copy;
  openCandidates: number;
  pendingPlans: number;
}) {
  const { search } = useLocation();
  const items: Item[] = [
    { to: "/", label: copy.nav.overview, Icon: LayoutDashboard },
    { to: "/scans", label: copy.nav.scans, Icon: Radar },
    { to: "/resources", label: copy.nav.resources, Icon: Network },
    {
      to: "/candidates",
      label: copy.nav.candidates,
      Icon: ListChecks,
      badge: openCandidates,
    },
    {
      to: "/plans",
      label: copy.nav.plans,
      Icon: ClipboardCheck,
      badge: pendingPlans,
    },
    { to: "/reports", label: copy.nav.reports, Icon: PiggyBank },
    { to: "/audit", label: copy.nav.audit, Icon: ScrollText },
  ];
  return (
    <nav className="sidebar">
      <div className="sidebar-brand">
        <Network size={18} />
        <span>Cloud Steward</span>
      </div>
      {items.map(({ to, label, Icon, badge }) => (
        <NavLink
          key={to}
          to={{ pathname: to, search }}
          end={to === "/"}
          className={({ isActive }) =>
            isActive ? "nav-link active" : "nav-link"
          }
        >
          <Icon size={17} />
          <span className="nav-link-label">{label}</span>
          {badge ? <span className="nav-badge">{badge}</span> : null}
        </NavLink>
      ))}
    </nav>
  );
}
