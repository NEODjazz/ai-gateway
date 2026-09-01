import { createContext, type PropsWithChildren, useCallback, useContext, useEffect, useMemo, useState } from "react";
import { APIClient } from "../api/client";

const storageKey = "ai-gateway.admin-token";

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
  hasCapability: (capability: ConsoleCapability) => boolean;
  signOut: () => void;
};

const AuthContext = createContext<AuthValue | null>(null);

export function AuthProvider({ children }: PropsWithChildren) {
  const [token, setToken] = useState(() => sessionStorage.getItem(storageKey) || "");
  const [session, setSession] = useState<AdminSession | null>(null);
  const signOut = useCallback(() => {
    sessionStorage.removeItem(storageKey);
    setSession(null);
    setToken("");
  }, []);
  const validate = useCallback(async (candidate: string) => {
    const candidateClient = new APIClient(() => candidate);
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
  const value = useMemo<AuthValue>(() => ({
    token,
    session,
    client: new APIClient(() => token),
    signIn: async (next) => {
      const normalized = next.trim().replace(/^Bearer\s+/i, "");
      const authenticated = await validate(normalized);
      sessionStorage.setItem(storageKey, normalized);
      setSession(authenticated);
      setToken(normalized);
    },
    restoreSession,
    hasCapability: (capability) => Boolean(session?.capabilities.includes(capability)),
    signOut
  }), [restoreSession, session, signOut, token, validate]);
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth() {
  const value = useContext(AuthContext);
  if (!value) throw new Error("useAuth must be used inside AuthProvider");
  return value;
}
