import { useEffect, useState } from "react";
import { useAuth } from "../auth/AuthContext";
import { DataTable, type Row } from "../components/DataTable";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { PageHeader } from "../components/PageHeader";
import { StatCard } from "../components/StatCard";
import { formatCost } from "../format";

type UsageAggregate = {
  name?: string;
  currency: string;
  requests: number;
  errors: number;
  input_tokens: number;
  output_tokens: number;
  total_tokens: number;
  cost: number;
  avg_latency_ms: number;
  cache_hits: number;
  cost_per_request: number;
};

type UsageReport = {
  totals: UsageAggregate[];
  by_model: UsageAggregate[];
  by_provider: UsageAggregate[];
};

const number = new Intl.NumberFormat("en-US", { maximumFractionDigits: 1 });

function totalRows(report: UsageReport) {
  const requests = report.totals.reduce((sum, row) => sum + row.requests, 0);
  const tokens = report.totals.reduce((sum, row) => sum + row.total_tokens, 0);
  const weightedLatency = requests === 0 ? 0 : report.totals.reduce((sum, row) => sum + row.avg_latency_ms * row.requests, 0) / requests;
  const spend = report.totals.length ? report.totals.map((row) => formatCost(row.cost, row.currency)).join(" · ") : formatCost(0, "USD");
  return { requests, tokens, weightedLatency, spend };
}

function displayRows(rows: UsageAggregate[], dimension: "model" | "provider"): Row[] {
  return rows.map((row) => ({
    [dimension]: row.name || "Unknown",
    requests: row.requests,
    tokens: row.total_tokens,
    spend: formatCost(row.cost, row.currency)
  }));
}

export function UsagePage() {
  const { client } = useAuth();
  const [days, setDays] = useState(30);
  const [report, setReport] = useState<UsageReport | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    setReport(null); setError("");
    client.request<UsageReport>(`/admin/v1/usage/report?days=${days}`).then(setReport).catch((cause) => setError(cause instanceof Error ? cause.message : "Could not load usage"));
  }, [client, days]);
  const totals = report ? totalRows(report) : { requests: 0, tokens: 0, weightedLatency: 0, spend: formatCost(0, "USD") };
  const models = report ? displayRows(report.by_model || [], "model") : [];
  const providers = report ? displayRows(report.by_provider || [], "provider") : [];
  function exportCSV() {
    if (!report) return;
    const lines = ["dimension,type,requests,total_tokens,cost,currency"];
    for (const [type, rows] of [["model", report.by_model || []], ["provider", report.by_provider || []]] as const) for (const row of rows) lines.push([JSON.stringify(row.name || ""), type, row.requests, row.total_tokens, row.cost, JSON.stringify(row.currency)].join(","));
    const url = URL.createObjectURL(new Blob([`${lines.join("\n")}\n`], { type: "text/csv" }));
    const link = document.createElement("a"); link.href = url; link.download = `ai-gateway-usage-${days}d.csv`; link.click(); URL.revokeObjectURL(url);
  }
  return <><PageHeader eyebrow="Analytics" title="Usage & spend" description="Final outcomes aggregated without combining currencies." actions={<><select aria-label="Window" value={days} onChange={(event) => setDays(Number(event.target.value))}><option value={7}>7 days</option><option value={30}>30 days</option><option value={90}>90 days</option></select><button className="secondary" disabled={!report} onClick={exportCSV}>Export CSV</button></>} />{error ? <ErrorState message={error} /> : !report ? <LoadingState /> : <><div className="stats-grid"><StatCard label="Requests" value={number.format(totals.requests)} /><StatCard label="Tokens" value={number.format(totals.tokens)} /><StatCard label="Spend" value={totals.spend} /><StatCard label="Average latency" value={number.format(totals.weightedLatency)} detail="ms" /></div><div className="split-grid"><section><h2>Usage by model</h2><DataTable rows={models} columns={[{ key: "model", label: "Model" }, { key: "requests", label: "Requests" }, { key: "tokens", label: "Tokens" }, { key: "spend", label: "Spend" }]} /></section><section><h2>Usage by provider</h2><DataTable rows={providers} columns={[{ key: "provider", label: "Provider" }, { key: "requests", label: "Requests" }, { key: "tokens", label: "Tokens" }, { key: "spend", label: "Spend" }]} /></section></div></>}</>;
}
