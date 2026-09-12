import { createContext, type PropsWithChildren, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import { APIClient } from "../api/client";

const storageKey = "ai-gateway.admin-token";
const browserSSOMarker = "browser-sso";

export type ConsoleCapability = "admin" | "api_docs" | "inference" | "team_directory";

export type AdminSession = {
  user_id?: string;
  team_id?: string;
  organization_id?: string;
  credential_id?: string;
  credential_alias?: string;
  roles: string[];
  allowed_models: string[];
  allowed_tools: string[];
  capabilities: ConsoleCapability[];
};

type AuthValue = {
  token: string;
  session: AdminSession | null;
  client: APIClient;
  signIn: (token: string) => Promise<void>;
  restoreSession: () => Promise<void>;
  ssoEnabled: boolean;
  ssoChecking: boolean;
  hasCapability: (capability: ConsoleCapability) => boolean;
  signOut: () => void;
};

const AuthContext = createContext<AuthValue | null>(null);

export function AuthProvider({ children }: PropsWithChildren) {
  const [token, setToken] = useState(() => sessionStorage.getItem(storageKey) || "");
  const [session, setSession] = useState<AdminSession | null>(null);
  const [ssoEnabled, setSSOEnabled] = useState(false);
  const [ssoChecking, setSSOChecking] = useState(() => !token && window.location.pathname.startsWith("/ui"));
  const ssoDiscoveryStarted = useRef(false);
  const signOut = useCallback(() => {
    sessionStorage.removeItem(storageKey);
    setSession(null);
    setToken("");
    void fetch("/auth/sso/logout", { method: "POST" }).catch(() => undefined);
  }, []);
  const validate = useCallback(async (candidate: string) => {
    const candidateClient = new APIClient(() => candidate === browserSSOMarker ? "" : candidate);
    return candidateClient.request<AdminSession>("/admin/v1/session");
  }, []);
  const restoreSession = useCallback(async () => {
    if (!token) return;
    setSession(await validate(token));
  }, [token, validate]);
  useEffect(() => {
    const expired = () => signOut();
    window.addEventListener("control-plane-session-expired", expired);
    return () => window.removeEventListener("control-plane-session-expired", expired);
  }, [signOut]);
  useEffect(() => {
    let active = true;
    if (ssoDiscoveryStarted.current || !window.location.pathname.startsWith("/ui")) {
      setSSOChecking(false);
      return () => { active = false; };
    }
    ssoDiscoveryStarted.current = true;
    void fetch("/auth/sso/config", { headers: { Accept: "application/json" } })
      .then(async (response) => response.ok ? response.json() as Promise<{ enabled?: boolean }> : { enabled: false })
      .then(async (config) => {
        if (!active) return;
        setSSOEnabled(Boolean(config.enabled));
        if (!config.enabled || token) return;
        try {
          const authenticated = await validate("");
          if (!active) return;
          sessionStorage.setItem(storageKey, browserSSOMarker);
          setToken(browserSSOMarker);
          setSession(authenticated);
        } catch {
          // An absent or expired HttpOnly session leaves the explicit sign-in choices visible.
        }
      })
      .catch(() => undefined)
      .finally(() => { if (active) setSSOChecking(false); });
    return () => { active = false; };
  }, [token, validate]);
  const value = useMemo<AuthValue>(() => ({
    token,
    session,
    client: new APIClient(() => token === browserSSOMarker ? "" : token),
    signIn: async (next) => {
      const normalized = next.trim().replace(/^Bearer\s+/i, "");
      const authenticated = await validate(normalized);
      sessionStorage.setItem(storageKey, normalized);
      setSession(authenticated);
      setToken(normalized);
    },
    restoreSession,
    ssoEnabled,
    ssoChecking,
    hasCapability: (capability) => Boolean(session?.capabilities.includes(capability)),
    signOut
  }), [restoreSession, session, signOut, ssoChecking, ssoEnabled, token, validate]);
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth() {
  const value = useContext(AuthContext);
  if (!value) throw new Error("useAuth must be used inside AuthProvider");
  return value;
}
