import { createContext, useCallback, useContext, useEffect, useRef, useState } from "react";
import type { APIClient } from "../../api/client";

export const StorageClient = createContext<APIClient | undefined>(undefined);
export function useStorageClient() {
  const client = useContext(StorageClient);
  if (!client) throw new Error("Storage connection is unavailable.");
  return client;
}

// Connection changes remount the workspace; each request also observes teardown.
export function useStorageQuery<T>(path: string | undefined, parse: (value: unknown) => T) {
  const client = useStorageClient();
  const [data, setData] = useState<{ path: string; value: T }>();
  const [state, setState] = useState({ loading: Boolean(path), error: "" });
  const generation = useRef(0), pending = useRef<AbortController | undefined>(undefined);
  const reload = useCallback(async () => {
    if (!path) return;
    const current = ++generation.current; pending.current?.abort();
    const controller = new AbortController(); pending.current = controller;
    let timedOut = false;
    const timer = setTimeout(() => { timedOut = true; controller.abort(); }, 15000);
    setState({ loading: true, error: "" });
    try {
      const value = parse(await client.request<unknown>(path, { signal: controller.signal, maximumResponseBytes: 2 * 1024 * 1024 }));
      if (current === generation.current) setData({ path, value });
    } catch (cause) {
      if (current === generation.current && (!controller.signal.aborted || timedOut)) setState({ loading: false, error: timedOut ? "Storage request timed out. Refresh to try again." : cause instanceof Error ? cause.message : "Storage request failed." });
    } finally { clearTimeout(timer); if (current === generation.current) setState((value) => ({ ...value, loading: false })); }
  }, [client, parse, path]);
  useEffect(() => { void reload(); return () => { generation.current++; pending.current?.abort(); }; }, [reload]);
  return { ...state, data: data?.value, previous: Boolean(data && data.path !== path), reload };
}

export function useStorageTask() {
  const [busy, setBusy] = useState(false), [error, setError] = useState("");
  const alive = useRef(true), running = useRef(false), pending = useRef<AbortController | undefined>(undefined);
  useEffect(() => { alive.current = true; return () => { alive.current = false; pending.current?.abort(); }; }, []);
  const run = async (task: (signal: AbortSignal, current: () => boolean) => Promise<void>) => {
    if (running.current || !alive.current) return;
    running.current = true; setBusy(true); setError("");
    const controller = new AbortController(); pending.current = controller;
    let timedOut = false;
    const timer = setTimeout(() => { timedOut = true; controller.abort(); }, 60000);
    const current = () => alive.current && !controller.signal.aborted;
    try { await task(controller.signal, current); if (alive.current && timedOut) setError("Request timed out. A write may have completed; refresh before retrying."); }
    catch (cause) { if (alive.current && (!controller.signal.aborted || timedOut)) setError(timedOut ? "Request timed out. A write may have completed; refresh before retrying." : cause instanceof Error ? cause.message : "Storage request failed."); }
    finally { clearTimeout(timer); running.current = false; if (alive.current) setBusy(false); }
  };
  return { busy, error, setError, run };
}
