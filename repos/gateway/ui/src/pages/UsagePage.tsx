import { useEffect, useState } from "react";
import { useAuth } from "../auth/AuthContext";
import { DataTable, type Row } from "../components/DataTable";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { PageHeader } from "../components/PageHeader";
import { StatCard } from "../components/StatCard";

type UsageReport = { totals?: Record<string, unknown>; by_model?: Row[]; by_provider?: Row[]; models?: Row[]; providers?: Row[] };

export function UsagePage() {
  const { client } = useAuth();
  const [days, setDays] = useState(30);
  const [report, setReport] = useState<UsageReport | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    setReport(null); setError("");
    client.request<UsageReport>(`/admin/v1/usage/report?days=${days}`).then(setReport).catch((cause) => setError(cause instanceof Error ? cause.message : "Could not load usage"));
  }, [client, days]);
  const totals = report?.totals || {};
  const columns = [{ key: "model", label: "Model" }, { key: "provider", label: "Provider" }, { key: "requests", label: "Requests" }, { key: "tokens", label: "Tokens" }, { key: "spend", label: "Spend" }];
  function exportCSV() {
    if (!report) return;
    const lines = ["dimension,type,requests,tokens,spend"];
    for (const [type, rows] of [["model", report.by_model || report.models || []], ["provider", report.by_provider || report.providers || []]] as const) for (const row of rows) lines.push([JSON.stringify(String(row[type] ?? "")), type, row.requests ?? 0, row.tokens ?? 0, JSON.stringify(String(row.spend ?? ""))].join(","));
    const url = URL.createObjectURL(new Blob([`${lines.join("\n")}\n`], { type: "text/csv" }));
    const link = document.createElement("a"); link.href = url; link.download = `ai-gateway-usage-${days}d.csv`; link.click(); URL.revokeObjectURL(url);
  }
  return <><PageHeader eyebrow="Analytics" title="Usage & spend" description="Final outcomes aggregated without combining currencies." actions={<><select aria-label="Window" value={days} onChange={(event) => setDays(Number(event.target.value))}><option value={7}>7 days</option><option value={30}>30 days</option><option value={90}>90 days</option></select><button className="secondary" disabled={!report} onClick={exportCSV}>Export CSV</button></>} />{error ? <ErrorState message={error} /> : !report ? <LoadingState /> : <><div className="stats-grid"><StatCard label="Requests" value={String(totals.requests ?? 0)} /><StatCard label="Tokens" value={String(totals.tokens ?? totals.total_tokens ?? 0)} /><StatCard label="Spend" value={String(totals.spend ?? "$0.00")} /><StatCard label="Average latency" value={String(totals.average_latency_ms ?? 0)} detail="ms" /></div><div className="split-grid"><section><h2>Usage by model</h2><DataTable rows={report.by_model || report.models || []} columns={columns.filter((column) => column.key !== "provider")} /></section><section><h2>Usage by provider</h2><DataTable rows={report.by_provider || report.providers || []} columns={columns.filter((column) => column.key !== "model")} /></section></div></>}</>;
}
