import { createContext, type PropsWithChildren, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import { loginConnections, type LoginConnection } from "./ssoConnections";
import { APIClient } from "../api/client";

const storageKey = "ai-gateway.admin-token";
const browserSSOMarker = "browser-sso";

export type ConsoleCapability = "admin" | "api_docs" | "inference" | "team_directory" | "organization_reports" | "organization_keys";

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
  organization_capabilities?: ("organization_reports" | "organization_keys")[];
};

type AuthValue = {
  token: string;
  session: AdminSession | null;
  client: APIClient;
  signIn: (token: string) => Promise<void>;
  restoreSession: () => Promise<void>;
  ssoConnections: LoginConnection[];
  ssoEnabled: boolean;
  ssoChecking: boolean;
  hasCapability: (capability: ConsoleCapability) => boolean;
  prepareSSOSignIn: () => void;
  signOut: () => Promise<void>;
  signingOut: boolean;
  signOutError: string;
};

const AuthContext = createContext<AuthValue | null>(null);

export function AuthProvider({ children }: PropsWithChildren) {
  const [token, setToken] = useState(() => sessionStorage.getItem(storageKey) || "");
  const [session, setSession] = useState<AdminSession | null>(null);
  const [ssoConnections, setSSOConnections] = useState<LoginConnection[]>([]);
  const [ssoEnabled, setSSOEnabled] = useState(false);
  const [ssoChecking, setSSOChecking] = useState(() => !token && window.location.pathname.startsWith("/ui"));
  const ssoDiscoveryStarted = useRef(false);
  const identityGeneration = useRef(0);
  const logoutInFlight = useRef<Promise<void> | null>(null);
  const [signingOut, setSigningOut] = useState(false);
  const [signOutError, setSignOutError] = useState("");
  const prepareSSOSignIn = useCallback(() => {
    identityGeneration.current++;
    setSignOutError("");
    sessionStorage.removeItem(storageKey); setSession(null); setToken("");
  }, []);
  const signOut = useCallback(() => {
    if (logoutInFlight.current) return logoutInFlight.current;
    const generation = identityGeneration.current;
    setSigningOut(true);
    setSignOutError("");
    const operation = (async () => {
      try {
        const response = await fetch("/auth/sso/logout", { method: "POST", signal: AbortSignal.timeout(10000) });
        if (!response.ok) throw new Error("logout failed");
        if (generation === identityGeneration.current) prepareSSOSignIn();
      } catch {
        if (generation === identityGeneration.current) setSignOutError("Server sign out could not be confirmed. Retry sign out.");
      } finally {
        logoutInFlight.current = null;
        setSigningOut(false);
      }
    })();
    logoutInFlight.current = operation;
    return operation;
  }, [prepareSSOSignIn]);
  const validate = useCallback(async (candidate: string) => {
    const candidateClient = new APIClient(() => candidate === browserSSOMarker ? "" : candidate);
    const identity = await candidateClient.request<AdminSession>("/admin/v1/session");
    const scopedCapabilities = (identity.organization_capabilities || []).filter((capability) => ["organization_reports", "organization_keys"].includes(capability));
    return { ...identity, capabilities: [...identity.capabilities, ...scopedCapabilities] };
  }, []);
  const restoreSession = useCallback(async () => {
    if (!token) return;
    const generation = identityGeneration.current;
    const identity = await validate(token);
    if (generation === identityGeneration.current) setSession(identity);
  }, [token, validate]);
  useEffect(() => {
    const expired = () => prepareSSOSignIn();
    window.addEventListener("control-plane-session-expired", expired);
    return () => window.removeEventListener("control-plane-session-expired", expired);
  }, [prepareSSOSignIn]);
  useEffect(() => {
    let active = true;
    if (ssoDiscoveryStarted.current || !window.location.pathname.startsWith("/ui")) {
      setSSOChecking(false);
      return () => { active = false; };
    }
    ssoDiscoveryStarted.current = true;
    const generation = identityGeneration.current;
    void fetch("/auth/sso/config", { headers: { Accept: "application/json" } })
      .then(async (response) => response.ok ? response.json() as Promise<{ enabled?: boolean; connections?: unknown }> : { enabled: false })
      .then(async (config) => {
        if (!active) return;
        const choices = loginConnections("connections" in config ? config.connections : undefined);
        setSSOConnections(choices);
        setSSOEnabled(Boolean(config.enabled));
        if (!config.enabled || token) return;
        try {
          const authenticated = await validate("");
          if (!active || generation !== identityGeneration.current) return;
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
      const generation = ++identityGeneration.current;
      const authenticated = await validate(normalized);
      if (generation !== identityGeneration.current) return;
      setSignOutError("");
      sessionStorage.setItem(storageKey, normalized);
      setSession(authenticated);
      setToken(normalized);
    },
    restoreSession,
    ssoConnections,
    ssoEnabled,
    ssoChecking,
    hasCapability: (capability) => Boolean(session?.capabilities.includes(capability)),
    prepareSSOSignIn,
    signOut,
    signingOut,
    signOutError
  }), [restoreSession, session, signOut, ssoChecking, ssoEnabled, ssoConnections, token, validate, prepareSSOSignIn, signingOut, signOutError]);
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth() {
  const value = useContext(AuthContext);
  if (!value) throw new Error("useAuth must be used inside AuthProvider");
  return value;
}
