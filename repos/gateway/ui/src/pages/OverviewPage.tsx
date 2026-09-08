import { useEffect, useState } from "react";
import { useAuth } from "../auth/AuthContext";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { PageHeader } from "../components/PageHeader";
import { StatCard } from "../components/StatCard";

const sources: Record<string, string> = {
  "Virtual keys": "/admin/v1/keys",
  Providers: "/admin/v1/providers",
  Deployments: "/admin/v1/model-deployments",
  Projects: "/admin/v1/projects",
  "MCP servers": "/admin/v1/mcp/servers",
  Guardrails: "/admin/v1/guardrail-policies"
};

function InventoryCard({ label, path }: { label: string; path: string }) {
  const { client } = useAuth();
  const [count, setCount] = useState<number>();
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setError(""); setCount(undefined);
    client.request<{ data?: unknown[]; total?: number }>(path, { signal: controller.signal }).then((payload) => {
      if (!controller.signal.aborted) setCount(typeof payload.total === "number" ? payload.total : (payload.data?.length || 0));
    }).catch((cause) => {
      if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : "Could not load inventory");
    });
    return () => controller.abort();
  }, [client, path, attempt]);
  return count !== undefined ? <StatCard label={label} value={count} detail="Configured" /> : <section aria-label={label}><h2>{label}</h2>{error ? <ErrorState message={error} retry={() => setAttempt((value) => value + 1)} /> : <LoadingState />}</section>;
}

export function OverviewPage() {
  return <><PageHeader eyebrow="Control plane" title="Overview" description="Operational inventory without prompt or response content." /><div className="stats-grid">{Object.entries(sources).map(([label, path]) => <InventoryCard key={label} label={label} path={path} />)}</div><section className="notice-card"><strong>Content storage is off</strong><p>The console works with identities, routing metadata, token counts and bounded diagnostics. Provider credentials and bearer tokens remain write-only.</p></section></>;
}
