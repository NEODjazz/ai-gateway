import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import { useAuth } from "../auth/AuthContext";
import { ManagedDataTable } from "../components/ManagedDataTable";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { PageHeader } from "../components/PageHeader";
import { StatCard } from "../components/StatCard";
import { formatCost } from "../format";

type UsageAggregate = {
  date?: string; name?: string; currency: string; requests: number; errors: number; input_tokens: number; output_tokens: number;
  total_tokens: number; cost: number; avg_latency_ms: number; cache_hits: number; cost_per_request: number;
};
type UsageReport = {
  days?: number; from?: string; to?: string; totals: UsageAggregate[]; daily: UsageAggregate[];
  by_model: UsageAggregate[]; by_upstream_model: UsageAggregate[]; by_provider: UsageAggregate[]; by_endpoint: UsageAggregate[]; by_tag: UsageAggregate[]; by_key: UsageAggregate[];
  by_user: UsageAggregate[]; by_team: UsageAggregate[]; by_organization: UsageAggregate[];
};
type UsageFilters = { window: string; from: string; to: string; model: string; provider: string; tag: string };
type UsageView = "overview" | "models" | "upstream-models" | "providers" | "endpoints" | "tags" | "keys" | "users" | "teams" | "organizations";

const number = new Intl.NumberFormat("en-US", { maximumFractionDigits: 1 });
const defaultFilters: UsageFilters = { window: "30", from: "", to: "", model: "", provider: "", tag: "" };
const usageViews: { id: UsageView; label: string }[] = [
  { id: "overview", label: "Overview" }, { id: "models", label: "Public models" }, { id: "upstream-models", label: "Upstream models" }, { id: "providers", label: "Providers" }, { id: "endpoints", label: "Endpoints" }, { id: "tags", label: "Tags" },
  { id: "keys", label: "Virtual keys" }, { id: "users", label: "Users" }, { id: "teams", label: "Teams" }, { id: "organizations", label: "Organizations" }
];

function totalsFor(report: UsageReport) {
  const requests = report.totals.reduce((sum, row) => sum + row.requests, 0);
  const errors = report.totals.reduce((sum, row) => sum + row.errors, 0);
  const tokens = report.totals.reduce((sum, row) => sum + row.total_tokens, 0);
  const cacheHits = report.totals.reduce((sum, row) => sum + row.cache_hits, 0);
  const weightedLatency = requests === 0 ? 0 : report.totals.reduce((sum, row) => sum + row.avg_latency_ms * row.requests, 0) / requests;
  const spend = report.totals.length ? report.totals.map((row) => formatCost(row.cost, row.currency)).join(" · ") : formatCost(0, "USD");
  return { requests, errors, successful: Math.max(0, requests - errors), tokens, cacheHits, weightedLatency, spend };
}

function queryFor(filters: UsageFilters) {
  const query = new URLSearchParams();
  if (filters.window === "custom" && filters.from && filters.to) {
    query.set("from", new Date(`${filters.from}T00:00:00Z`).toISOString());
    query.set("to", new Date(`${filters.to}T23:59:59Z`).toISOString());
  } else query.set("days", filters.window === "custom" ? "30" : filters.window);
  if (filters.model.trim()) query.set("model", filters.model.trim());
  if (filters.provider.trim()) query.set("provider", filters.provider.trim());
  if (filters.tag.trim()) query.set("tag", filters.tag.trim());
  return query;
}

function UsageBars({ title, rows, value, format }: { title: string; rows: UsageAggregate[]; value: (row: UsageAggregate) => number; format: (value: number, row: UsageAggregate) => string }) {
  const max = Math.max(0, ...rows.map(value));
  return <section className="usage-chart-card"><h2>{title}</h2>{rows.length ? <div className="usage-bars">{rows.map((row, index) => { const amount = value(row); return <div className="usage-bar-row" key={`${row.date}-${row.currency}-${index}`}><span>{row.date || "Unknown"}{title === "Spend per day" ? ` · ${row.currency}` : ""}</span><div className="usage-bar-track"><span style={{ width: `${max ? Math.max(2, amount / max * 100) : 0}%` }} /></div><strong>{format(amount, row)}</strong></div>; })}</div> : <p className="muted">No daily usage in this period.</p>}</section>;
}

function usageRows(rows: UsageAggregate[]) {
  return rows.map((row) => ({ ...row, _identity: `${row.name || "Unknown"}\u0000${row.currency}`, name: row.name || "Unknown", successful: Math.max(0, row.requests - row.errors), success_rate: row.requests ? `${((row.requests - row.errors) / row.requests * 100).toFixed(1)}%` : "—", spend: formatCost(row.cost, row.currency), cost_per_request_display: formatCost(row.cost_per_request, row.currency), latency: `${number.format(row.avg_latency_ms)} ms` }));
}

function aggregateDailyActivity(rows: UsageAggregate[]) {
  const grouped = new Map<string, UsageAggregate>();
  for (const row of rows) {
    const date = row.date || "Unknown"; const current = grouped.get(date) || { date, currency: "", requests: 0, errors: 0, input_tokens: 0, output_tokens: 0, total_tokens: 0, cost: 0, avg_latency_ms: 0, cache_hits: 0, cost_per_request: 0 };
    const latencyTotal = current.avg_latency_ms * current.requests + row.avg_latency_ms * row.requests;
    current.requests += row.requests; current.errors += row.errors; current.input_tokens += row.input_tokens; current.output_tokens += row.output_tokens; current.total_tokens += row.total_tokens; current.cache_hits += row.cache_hits;
    current.avg_latency_ms = current.requests ? latencyTotal / current.requests : 0; grouped.set(date, current);
  }
  return [...grouped.values()].sort((left, right) => String(left.date).localeCompare(String(right.date)));
}

const usageColumns = [
  { key: "name", label: "Name" }, { key: "requests", label: "Requests" }, { key: "successful", label: "Successful" }, { key: "errors", label: "Failed" },
  { key: "success_rate", label: "Success rate" }, { key: "total_tokens", label: "Total tokens" }, { key: "input_tokens", label: "Input tokens" },
  { key: "output_tokens", label: "Output tokens" }, { key: "cache_hits", label: "Cache hits" }, { key: "latency", label: "Average latency" },
  { key: "spend", label: "Spend" }, { key: "cost_per_request_display", label: "Cost / request" }, { key: "currency", label: "Currency" }
];

export function UsagePage() {
  const { client } = useAuth();
  const [draft, setDraft] = useState(defaultFilters);
  const [filters, setFilters] = useState(defaultFilters);
  const [view, setView] = useState<UsageView>("overview");
  const [report, setReport] = useState<UsageReport | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    setReport(null); setError("");
    try { setReport(await client.request<UsageReport>(`/admin/v1/usage/report?${queryFor(filters)}`)); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load usage"); }
  }, [client, filters]);
  useEffect(() => { void load(); }, [load]);
  const totals = report ? totalsFor(report) : { requests: 0, errors: 0, successful: 0, tokens: 0, cacheHits: 0, weightedLatency: 0, spend: formatCost(0, "USD") };
  const daily = report?.daily || [];
  const requestDaily = useMemo(() => aggregateDailyActivity(daily), [daily]);
  function apply(event: FormEvent) { event.preventDefault(); if (draft.window === "custom" && (!draft.from || !draft.to)) return; setFilters(draft); }
  function exportCSV() {
    if (!report) return;
    const lines = ["dimension,type,requests,errors,input_tokens,output_tokens,total_tokens,cache_hits,avg_latency_ms,cost,currency"];
    for (const [type, rows] of [["public_model", report.by_model || []], ["upstream_model", report.by_upstream_model || []], ["provider", report.by_provider || []], ["endpoint", report.by_endpoint || []], ["tag", report.by_tag || []], ["key", report.by_key || []], ["user", report.by_user || []], ["team", report.by_team || []], ["organization", report.by_organization || []]] as const) for (const row of rows) lines.push([JSON.stringify(row.name || ""), type, row.requests, row.errors, row.input_tokens, row.output_tokens, row.total_tokens, row.cache_hits, row.avg_latency_ms, row.cost, JSON.stringify(row.currency)].join(","));
    const url = URL.createObjectURL(new Blob([`${lines.join("\n")}\n`], { type: "text/csv" })); const link = document.createElement("a"); link.href = url; link.download = "ai-gateway-usage.csv"; link.click(); URL.revokeObjectURL(url);
  }
  return <><PageHeader eyebrow="Analytics" title="Usage & spend" description="Gateway activity, spend, token and reliability trends without combining currencies." actions={<button className="secondary" disabled={!report} onClick={exportCSV}>Export CSV</button>} />
    <form className="usage-filter-bar" onSubmit={apply}><label>Period<select aria-label="Window" value={draft.window} onChange={(event) => setDraft({ ...draft, window: event.target.value })}><option value="7">7 days</option><option value="30">30 days</option><option value="90">90 days</option><option value="custom">Custom</option></select></label>{draft.window === "custom" && <><label>From<input aria-label="Usage from" type="date" required value={draft.from} onChange={(event) => setDraft({ ...draft, from: event.target.value })} /></label><label>To<input aria-label="Usage to" type="date" required value={draft.to} onChange={(event) => setDraft({ ...draft, to: event.target.value })} /></label></>}<label>Model<input aria-label="Usage model" placeholder="All models" value={draft.model} onChange={(event) => setDraft({ ...draft, model: event.target.value })} /></label><label>Provider<input aria-label="Usage provider" placeholder="All providers" value={draft.provider} onChange={(event) => setDraft({ ...draft, provider: event.target.value })} /></label><label>Tag<input aria-label="Usage tag" placeholder="All tags" value={draft.tag} onChange={(event) => setDraft({ ...draft, tag: event.target.value })} /></label><button>Apply</button><button type="button" className="secondary" onClick={() => { setDraft(defaultFilters); setFilters(defaultFilters); }}>Reset</button></form>
    <div className="page-tabs" role="tablist" aria-label="Usage views">{usageViews.map((item) => <button role="tab" aria-selected={view === item.id} className={view === item.id ? "active" : ""} key={item.id} onClick={() => setView(item.id)}>{item.label}</button>)}</div>
    {error ? <ErrorState message={error} retry={() => void load()} /> : !report ? <LoadingState /> : view === "overview" ? <><div className="usage-stats-grid"><StatCard label="Total requests" value={number.format(totals.requests)} /><StatCard label="Successful" value={number.format(totals.successful)} /><StatCard label="Failed" value={number.format(totals.errors)} /><StatCard label="Total tokens" value={number.format(totals.tokens)} /><StatCard label="Total spend" value={totals.spend} /><StatCard label="Cache hits" value={number.format(totals.cacheHits)} /><StatCard label="Average latency" value={number.format(totals.weightedLatency)} detail="ms" /></div><div className="usage-chart-grid"><UsageBars title="Spend per day" rows={daily} value={(row) => row.cost} format={(value, row) => formatCost(value, row.currency)} /><UsageBars title="Requests per day" rows={requestDaily} value={(row) => row.requests} format={(value) => number.format(value)} /><UsageBars title="Tokens per day" rows={requestDaily} value={(row) => row.total_tokens} format={(value) => number.format(value)} /><UsageBars title="Failed requests per day" rows={requestDaily} value={(row) => row.errors} format={(value) => number.format(value)} /></div></> : <ManagedDataTable rows={usageRows(view === "models" ? report.by_model || [] : view === "upstream-models" ? report.by_upstream_model || [] : view === "providers" ? report.by_provider || [] : view === "endpoints" ? report.by_endpoint || [] : view === "tags" ? report.by_tag || [] : view === "keys" ? report.by_key || [] : view === "users" ? report.by_user || [] : view === "teams" ? report.by_team || [] : report.by_organization || [])} columns={usageColumns} rowKey="_identity" defaultHidden={["input_tokens", "output_tokens", "cost_per_request_display", "currency"]} onRefresh={load} searchPlaceholder={`Search ${view}`} />}
  </>;
}
