import { useEffect, useState } from "react";
import { useAuth } from "../auth/AuthContext";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { PageHeader } from "../components/PageHeader";
import { StatCard } from "../components/StatCard";

type Counts = Record<string, number>;
const sources: Record<string, string> = {
  "Virtual keys": "/admin/v1/keys",
  Providers: "/admin/v1/providers",
  Deployments: "/admin/v1/model-deployments",
  Projects: "/admin/v1/projects",
  "MCP servers": "/admin/v1/mcp/servers",
  Guardrails: "/admin/v1/guardrail-policies"
};

export function OverviewPage() {
  const { client } = useAuth();
  const [counts, setCounts] = useState<Counts | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    Promise.all(Object.entries(sources).map(async ([label, path]) => {
      const payload = await client.request<{ data?: unknown[] }>(path);
      return [label, Array.isArray(payload.data) ? payload.data.length : 0] as const;
    })).then((entries) => active && setCounts(Object.fromEntries(entries))).catch((cause) => active && setError(cause instanceof Error ? cause.message : "Could not load overview"));
    return () => { active = false; };
  }, [client]);
  return <><PageHeader eyebrow="Control plane" title="Overview" description="Operational inventory without prompt or response content." />{error ? <ErrorState message={error} /> : !counts ? <LoadingState /> : <div className="stats-grid">{Object.entries(counts).map(([label, value]) => <StatCard key={label} label={label} value={value} detail="Configured" />)}</div>}<section className="notice-card"><strong>Content storage is off</strong><p>The console works with identities, routing metadata, token counts and bounded diagnostics. Provider credentials and bearer tokens remain write-only.</p></section></>;
}
