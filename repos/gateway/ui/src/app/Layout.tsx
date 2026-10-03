import { useState } from "react";
import { Outlet, useNavigate } from "react-router-dom";
import { PageLayout, PageLayoutAside } from "@gravity-ui/navigation/build/esm/index.js";
import { Sparkles } from "@gravity-ui/icons";
import { loginConnectionURL } from "../auth/ssoConnections";
import { useAuth } from "../auth/AuthContext";
import { appRoutes, canAccessRoute } from "./routes";
import { visibleNavigationSections } from "./navigation";
import { SidebarNavigation } from "./SidebarNavigation";

export function Layout() {
  const { session, signOut, signingOut, signOutError, ssoConnections, prepareSSOSignIn } = useAuth();
  const navigate = useNavigate();
  const [compact, setCompact] = useState(false);
  const routes = appRoutes.filter((route) => route.navigation !== false && canAccessRoute(route, session));

  return <PageLayout compact={compact} className="app-shell gravity-app-shell"><nav className="gravity-sidebar-landmark" aria-label="Dashboard"><PageLayoutAside
    logo={{ text: "AI Gateway", icon: Sparkles, href: "/ui/", onClick: (event) => { event.preventDefault(); navigate("/"); } }}
    aboveMenuContent={<SidebarNavigation sections={visibleNavigationSections(routes)} compact={compact} />}
    menuOverflow="scroll"
    onChangeCompact={setCompact}
    collapseTitle="Collapse navigation"
    expandTitle="Expand navigation"
    renderFooter={({ compact: footerCompact }) => footerCompact ? <button className="gravity-sidebar-signout-compact" aria-label="Sign out" title="Sign out" disabled={signingOut} onClick={() => void signOut()}>↪</button> : <div className="sidebar-footer"><div className="sidebar-account"><strong>{session?.user_id || session?.credential_alias || "Authenticated user"}</strong><small>{session?.roles.join(", ") || "gateway credential"}{session?.organization_id ? ` · Organization: ${session.organization_id}` : ""}{session?.team_id ? ` · ${session.team_id}` : ""}</small></div>{ssoConnections.length > 0 && <details><summary>Switch organization / connection</summary>{ssoConnections.map((connection) => <p key={connection.id}><a href={loginConnectionURL(connection)} onClick={prepareSSOSignIn}>{connection.name} · {connection.organization_id || "Platform"}</a></p>)}<small>Requires a fresh identity and organization verification.</small></details>}<a href="/docs/" target="_blank" rel="noreferrer">API docs ↗</a><button className="secondary" disabled={signingOut} onClick={() => void signOut()}>Sign out</button></div>}
  /></nav><PageLayout.Content><main className="content">{signOutError && <p role="alert">{signOutError} <button disabled={signingOut} onClick={() => void signOut()}>Retry sign out</button></p>}<Outlet /></main></PageLayout.Content></PageLayout>;
}
