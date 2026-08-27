import { NavLink, Outlet } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import { appRoutes } from "./routes";

const groups = ["Monitor", "Manage", "AI Hub", "Govern", "System"] as const;

export function Layout() {
  const { signOut } = useAuth();
  return <div className="app-shell"><aside className="sidebar"><div className="brand"><span className="brand-mark">AI</span><div><strong>Gateway</strong><small>Control plane</small></div></div><nav aria-label="Dashboard">{groups.map((group) => <section className="nav-group" key={group}><h2>{group}</h2>{appRoutes.filter((route) => route.group === group).map((route) => <NavLink key={route.path} to={route.path} className={({ isActive }) => `nav-link${isActive ? " active" : ""}`}><span>{route.title}</span>{!route.available && <span className="nav-dot" title="Backend unavailable" />}</NavLink>)}</section>)}</nav><div className="sidebar-footer"><a href="/docs/" target="_blank" rel="noreferrer">API docs ↗</a><button className="secondary" onClick={signOut}>Sign out</button></div></aside><main className="content"><Outlet /></main></div>;
}
