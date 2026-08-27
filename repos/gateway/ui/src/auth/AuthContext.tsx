import { createContext, type PropsWithChildren, useContext, useMemo, useState } from "react";
import { APIClient } from "../api/client";

const storageKey = "ai-gateway.admin-token";

type AuthValue = {
  token: string;
  client: APIClient;
  signIn: (token: string) => void;
  signOut: () => void;
};

const AuthContext = createContext<AuthValue | null>(null);

export function AuthProvider({ children }: PropsWithChildren) {
  const [token, setToken] = useState(() => sessionStorage.getItem(storageKey) || "");
  const value = useMemo<AuthValue>(() => ({
    token,
    client: new APIClient(() => token),
    signIn: (next) => {
      const normalized = next.trim().replace(/^Bearer\s+/i, "");
      sessionStorage.setItem(storageKey, normalized);
      setToken(normalized);
    },
    signOut: () => {
      sessionStorage.removeItem(storageKey);
      setToken("");
    }
  }), [token]);
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth() {
  const value = useContext(AuthContext);
  if (!value) throw new Error("useAuth must be used inside AuthProvider");
  return value;
}
