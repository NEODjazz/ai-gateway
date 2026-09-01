import { useEffect, useMemo, useState } from "react";
import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import { Layout } from "./Layout";
import { LoginPage } from "./LoginPage";
import { appRoutes, routeCapability } from "./routes";

export function App() {
  const { token, session, restoreSession } = useAuth();
  const [checking, setChecking] = useState(Boolean(token && !session));
  useEffect(() => {
    let active = true;
    if (!token || session) { setChecking(false); return () => { active = false; }; }
    setChecking(true);
    void restoreSession().catch(() => undefined).finally(() => { if (active) setChecking(false); });
    return () => { active = false; };
  }, [restoreSession, session, token]);
  const routes = useMemo(() => session ? appRoutes.filter((route) => session.capabilities.includes(routeCapability(route))) : [], [session]);
  if (checking) return <main className="login-page"><section className="login-card"><h1>Validating session</h1><p>Checking the stored gateway credential…</p></section></main>;
  if (!token) return <LoginPage />;
  if (!session) return <LoginPage />;
  const landing = routes[0]?.path || "/api-reference";
  return <BrowserRouter basename="/ui"><Routes><Route element={<Layout />}>{routes.map((route) => <Route key={route.path} path={route.path} element={route.element} />)}<Route index element={<Navigate to={landing} replace />} /><Route path="*" element={<Navigate to={landing} replace />} /></Route></Routes></BrowserRouter>;
}
