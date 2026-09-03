import { useState } from "react";
import { Outlet, useLocation, useNavigate } from "react-router-dom";
import { PageLayout, PageLayoutAside, type AsideHeaderItem } from "@gravity-ui/navigation/build/esm/index.js";
import { ChartColumn, Gear, ScalesBalanced, Shield, Sliders, Sparkles } from "@gravity-ui/icons";
import { useAuth } from "../auth/AuthContext";
import { appRoutes, routeCapability } from "./routes";

const groups = ["Manage", "Monitor", "Access Control", "AI Hub", "Govern", "System"] as const;
const groupIcons = { Manage: Gear, Monitor: ChartColumn, "Access Control": Shield, "AI Hub": Sparkles, Govern: ScalesBalanced, System: Sliders } as const;
const groupIDs = { Manage: "manage", Monitor: "monitor", "Access Control": "access-control", "AI Hub": "ai-hub", Govern: "govern", System: "system" } as const;

export function Layout() {
  const { session, signOut } = useAuth();
  const location = useLocation();
  const navigate = useNavigate();
  const [compact, setCompact] = useState(false);
  const routes = appRoutes.filter((route) => route.navigation !== false && Boolean(session?.capabilities.includes(routeCapability(route))));
  const menuGroups = groups.flatMap((group) => routes.some((route) => route.group === group) ? [{ id: groupIDs[group], title: group, popupTitle: group, icon: groupIcons[group] }] : []);
  const orderedRoutes = groups.flatMap((group) => routes.filter((route) => route.group === group));
  const menuItems: AsideHeaderItem[] = orderedRoutes.map((route) => ({
    id: route.path,
    title: route.title,
    icon: groupIcons[route.group],
    groupId: groupIDs[route.group],
    href: `/ui${route.path}`,
    current: location.pathname === route.path || (route.path !== "/overview" && location.pathname.startsWith(`${route.path}/`)),
    rightAdornment: !route.available ? <span className="nav-dot" title="Backend unavailable" /> : undefined,
    onItemClick: (_item, _collapsed, event) => {
      event.preventDefault();
      navigate(route.path);
    },
  }));

  return <PageLayout compact={compact} className="app-shell gravity-app-shell"><nav className="gravity-sidebar-landmark" aria-label="Dashboard"><PageLayoutAside
    logo={{ text: "AI Gateway", icon: Sparkles, href: "/ui/", onClick: (event) => { event.preventDefault(); navigate("/"); } }}
    menuGroups={menuGroups}
    menuItems={menuItems}
    menuOverflow="scroll"
    onChangeCompact={setCompact}
    collapseTitle="Collapse navigation"
    expandTitle="Expand navigation"
    renderFooter={({ compact: footerCompact }) => footerCompact ? <button className="gravity-sidebar-signout-compact" aria-label="Sign out" title="Sign out" onClick={signOut}>↪</button> : <div className="sidebar-footer"><div className="sidebar-account"><strong>{session?.user_id || session?.credential_alias || "Authenticated user"}</strong><small>{session?.roles.join(", ") || "gateway credential"}{session?.team_id ? ` · ${session.team_id}` : ""}</small></div><a href="/docs/" target="_blank" rel="noreferrer">API docs ↗</a><button className="secondary" onClick={signOut}>Sign out</button></div>}
  /></nav><PageLayout.Content><main className="content"><Outlet /></main></PageLayout.Content></PageLayout>;
}
