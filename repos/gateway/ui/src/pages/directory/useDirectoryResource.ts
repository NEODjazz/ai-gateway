import { useCallback, useEffect, useRef, useState } from "react";
import { useAuth } from "../../auth/AuthContext";

export type DirectoryPage<T> = { data: T[]; total: number; offset: number; limit: number };
export function isDirectoryPage(value: unknown): boolean {
  if (!value || typeof value !== "object") return false;
  const page = value as DirectoryPage<unknown>;
  return Array.isArray(page.data) && Number.isSafeInteger(page.total) && page.total >= 0 && Number.isSafeInteger(page.offset) && page.offset >= 0 && Number.isInteger(page.limit) && page.limit >= 1 && page.limit <= 500 && page.data.length <= page.limit;
}

// Retain only data belonging to the current verified identity and workspace.
// A page can retain its last successful result while a new query is pending.
export function useDirectoryResource<T>(path: string | undefined, workspace = path, validate?: (value: unknown) => boolean) {
  const { client, session } = useAuth();
  const scope = JSON.stringify([workspace, session?.credential_id, session?.user_id, session?.team_id, session?.organization_id, session?.roles]);
  const key = JSON.stringify([scope, path]);
  const activeKey = useRef(key); activeKey.current = key;
  const generation = useRef(0);
  const controller = useRef<AbortController | undefined>(undefined);
  const [snapshot, setSnapshot] = useState<{ scope: string; path: string; value: T }>();
  const [state, setState] = useState({ key, loading: Boolean(path), error: "" });
  const reload = useCallback(async () => {
    if (!path || activeKey.current !== key) return;
    const current = ++generation.current;
    controller.current?.abort(); const pending = new AbortController(); controller.current = pending;
    setState({ key, loading: true, error: "" });
    try {
      const value = await client.request<T>(path, { signal: pending.signal });
      if (validate && !validate(value)) throw new Error("The directory returned invalid data.");
      if (current === generation.current && activeKey.current === key) setSnapshot({ scope, path, value });
    } catch (cause) {
      if (current === generation.current && activeKey.current === key && !pending.signal.aborted) setState({ key, loading: false, error: cause instanceof Error ? cause.message : "Could not load directory data" });
    } finally { if (current === generation.current && activeKey.current === key) setState((value) => ({ ...value, key, loading: false })); }
  }, [client, key, path, scope, validate]);
  useEffect(() => { void reload(); return () => { ++generation.current; controller.current?.abort(); }; }, [reload]);
  return { value: snapshot?.scope === scope ? snapshot.value : undefined, loading: Boolean(path) && (state.key !== key || state.loading), error: state.key === key ? state.error : "", previous: snapshot?.scope === scope && snapshot.path !== path, reload, scope };
}
