import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import { Layout } from "./Layout";
import { LoginPage } from "./LoginPage";
import { appRoutes } from "./routes";

export function App() {
  const { token } = useAuth();
  if (!token) return <LoginPage />;
  return <BrowserRouter basename="/ui"><Routes><Route element={<Layout />}>{appRoutes.map((route) => <Route key={route.path} path={route.path} element={route.element} />)}<Route index element={<Navigate to="/overview" replace />} /><Route path="*" element={<Navigate to="/overview" replace />} /></Route></Routes></BrowserRouter>;
}
