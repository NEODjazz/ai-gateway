import { NavLink, Outlet } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import { appRoutes, routeCapability } from "./routes";

const groups = ["Manage", "Monitor", "Access Control", "AI Hub", "Govern", "System"] as const;

export function Layout() {
  const { session, signOut } = useAuth();
  const routes = appRoutes.filter((route) => route.navigation !== false && Boolean(session?.capabilities.includes(routeCapability(route))));
  return <div className="app-shell"><aside className="sidebar"><div className="brand"><span className="brand-mark">AI</span><div><strong>Gateway</strong><small>Control plane</small></div></div><nav aria-label="Dashboard">{groups.map((group) => { const groupRoutes = routes.filter((route) => route.group === group); return groupRoutes.length ? <section className="nav-group" key={group}><h2>{group}</h2>{groupRoutes.map((route) => <NavLink key={route.path} to={route.path} className={({ isActive }) => `nav-link${isActive ? " active" : ""}`}><span>{route.title}</span>{!route.available && <span className="nav-dot" title="Backend unavailable" />}</NavLink>)}</section> : null; })}</nav><div className="sidebar-footer"><div className="sidebar-account"><strong>{session?.user_id || session?.credential_alias || "Authenticated user"}</strong><small>{session?.roles.join(", ") || "gateway credential"}{session?.team_id ? ` · ${session.team_id}` : ""}</small></div><a href="/docs/" target="_blank" rel="noreferrer">API docs ↗</a><button className="secondary" onClick={signOut}>Sign out</button></div></aside><main className="content"><Outlet /></main></div>;
}
