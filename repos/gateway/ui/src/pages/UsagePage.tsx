import { csvCell } from "../csv";
import { ModalFrame } from "../components/ModalFrame";
import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { Card, Select, TextInput } from "@gravity-ui/uikit";
import { useAuth } from "../auth/AuthContext";
import { ActionsMenu } from "../components/ActionsMenu";
import { ManagedDataTable } from "../components/ManagedDataTable";
import type { Row } from "../components/DataTable";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { PageHeader } from "../components/PageHeader";
import { PageTabs } from "../components/PageTabs";
import { StatCard } from "../components/StatCard";
import { formatCost } from "../format";
import { GravityThemeScope } from "../components/GravityThemeScope";
import { GatewayButton } from "../components/GatewayButton";

type UsageAggregate = {
  date?: string; name?: string; currency: string; requests: number; errors: number; input_tokens: number; output_tokens: number;
  training_tokens: number; total_tokens: number; input_characters: number; input_pages: number; input_audio_milliseconds: number; video_seconds: number; output_images: number; tool_requests: number; cache_read_input_tokens: number; cache_write_input_tokens: number; search_requests: number; cost: number; avg_latency_ms: number; cache_hits: number; cost_per_request: number;
};
type UsageReport = {
  days?: number; from?: string; to?: string; totals: UsageAggregate[]; daily: UsageAggregate[];
  by_model: UsageAggregate[]; by_upstream_model: UsageAggregate[]; by_provider: UsageAggregate[]; by_endpoint: UsageAggregate[]; by_tag: UsageAggregate[]; by_key: UsageAggregate[];
  by_user: UsageAggregate[]; by_team: UsageAggregate[]; by_organization: UsageAggregate[];
};
type UsageFilters = { window: string; from: string; to: string; model: string; provider: string; tag: string };
type UsageView = "overview" | "models" | "upstream-models" | "providers" | "endpoints" | "tags" | "keys" | "users" | "teams" | "organizations";
type UsageDrilldown = { view: Exclude<UsageView, "overview">; name: string };

const number = new Intl.NumberFormat("en-US", { maximumFractionDigits: 1 });
const defaultFilters: UsageFilters = { window: "30", from: "", to: "", model: "", provider: "", tag: "" };
const usageViews: { id: UsageView; label: string }[] = [
  { id: "overview", label: "Overview" }, { id: "organizations", label: "Organizations" }, { id: "teams", label: "Teams" }, { id: "users", label: "Users" }, { id: "keys", label: "Keys" },
  { id: "providers", label: "Providers" }, { id: "models", label: "Models" }, { id: "tags", label: "Tags" }, { id: "upstream-models", label: "Upstream models" }, { id: "endpoints", label: "Endpoints" }
];
const drilldownParameters: Record<Exclude<UsageView, "overview">, { parameter: string; label: string }> = {
  models: { parameter: "model", label: "Public model" },
  "upstream-models": { parameter: "upstream_model", label: "Upstream model" },
  providers: { parameter: "provider", label: "Provider" },
  endpoints: { parameter: "endpoint", label: "Endpoint" },
  tags: { parameter: "tag", label: "Tag" },
  keys: { parameter: "scope_type", label: "Virtual key" },
  users: { parameter: "scope_type", label: "User" },
  teams: { parameter: "scope_type", label: "Team" },
  organizations: { parameter: "scope_type", label: "Organization" }
};
const scopeTypes: Partial<Record<Exclude<UsageView, "overview">, string>> = { keys: "key", users: "user", teams: "team", organizations: "organization" };

function totalsFor(report: UsageReport) {
  const requests = report.totals.reduce((sum, row) => sum + row.requests, 0);
  const errors = report.totals.reduce((sum, row) => sum + row.errors, 0);
  const tokens = report.totals.reduce((sum, row) => sum + row.total_tokens, 0);
  const cacheHits = report.totals.reduce((sum, row) => sum + row.cache_hits, 0);
  const cacheReadTokens = report.totals.reduce((sum, row) => sum + (row.cache_read_input_tokens || 0), 0);
  const cacheWriteTokens = report.totals.reduce((sum, row) => sum + (row.cache_write_input_tokens || 0), 0);
  const weightedLatency = requests === 0 ? 0 : report.totals.reduce((sum, row) => sum + row.avg_latency_ms * row.requests, 0) / requests;
  const spend = report.totals.length ? report.totals.map((row) => formatCost(row.cost, row.currency)).join(" · ") : formatCost(0, "USD");
  return { requests, errors, successful: Math.max(0, requests - errors), tokens, cacheHits, cacheReadTokens, cacheWriteTokens, weightedLatency, spend };
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

function UsageTrend({ rows }: { rows: UsageAggregate[] }) {
  const maxSpend = Math.max(0, ...rows.map((row) => row.cost));
  const maxRequests = Math.max(0, ...rows.map((row) => row.requests));
  return <GravityThemeScope className="gravity-card-scope usage-trend-scope"><Card type="container" view="raised" className="usage-analytics-card usage-trend-card"><div className="usage-card-heading"><div><h2>Spend and requests</h2><p>Daily finalized traffic by currency</p><span className="sr-only">Spend per day</span></div><div className="usage-legend"><span><i className="spend" />Spend</span><span><i className="requests" />Requests</span></div></div>{rows.length ? <div className="usage-combo-chart">{rows.map((row, index) => <div className="usage-combo-column" key={`${row.date}-${row.currency}-${index}`} title={`${row.date || "Unknown"} · ${formatCost(row.cost, row.currency)} · ${row.requests} requests`}><div className="usage-combo-bars"><i className="spend" style={{ height: `${maxSpend ? Math.max(3, row.cost / maxSpend * 100) : 0}%` }} /><i className="requests" style={{ height: `${maxRequests ? Math.max(3, row.requests / maxRequests * 100) : 0}%` }} /></div><span>{row.date?.slice(5) || "—"}<small>{row.currency}</small></span></div>)}</div> : <p className="muted">No daily usage in this period.</p>}</Card></GravityThemeScope>;
}

function CacheOutcomes({ requests, successful, errors, cacheHits, cacheReadTokens, cacheWriteTokens }: { requests: number; successful: number; errors: number; cacheHits: number; cacheReadTokens: number; cacheWriteTokens: number }) {
  const hitRate = requests ? cacheHits / requests * 100 : 0;
  const percent = (value: number) => requests ? `${(value / requests * 100).toFixed(1)}%` : "0.0%";
  return <GravityThemeScope className="gravity-card-scope"><Card type="container" view="raised" className="usage-analytics-card usage-outcomes-card"><div className="usage-card-heading"><div><h2>Cache & outcomes</h2><p>Hit rate and final response states</p></div></div><strong className="usage-hit-rate">{number.format(hitRate)}%</strong><div className="usage-hit-track"><span style={{ width: `${Math.min(100, hitRate)}%` }} /></div><dl className="usage-outcome-list"><div><dt>Successful</dt><dd>{number.format(successful)} <span>{percent(successful)}</span></dd></div><div><dt>Failed</dt><dd>{number.format(errors)} <span>{percent(errors)}</span></dd></div><div><dt>Cache read tokens</dt><dd>{number.format(cacheReadTokens)}</dd></div><div><dt>Cache write tokens</dt><dd>{number.format(cacheWriteTokens)}</dd></div></dl></Card></GravityThemeScope>;
}

function usageRows(rows: UsageAggregate[]) {
  return rows.map((row) => ({ ...row, _identity: `${row.name || "Unknown"}\u0000${row.currency}`, name: row.name || "Unknown", successful: Math.max(0, row.requests - row.errors), success_rate: row.requests ? `${((row.requests - row.errors) / row.requests * 100).toFixed(1)}%` : "—", spend: formatCost(row.cost, row.currency), cost_per_request_display: formatCost(row.cost_per_request, row.currency), latency: `${number.format(row.avg_latency_ms)} ms` }));
}

function aggregateDailyActivity(rows: UsageAggregate[]) {
  const grouped = new Map<string, UsageAggregate>();
  for (const row of rows) {
    const date = row.date || "Unknown"; const current = grouped.get(date) || { date, currency: "", requests: 0, errors: 0, input_tokens: 0, output_tokens: 0, training_tokens: 0, total_tokens: 0, input_characters: 0, input_pages: 0, input_audio_milliseconds: 0, video_seconds: 0, output_images: 0, tool_requests: 0, cache_read_input_tokens: 0, cache_write_input_tokens: 0, search_requests: 0, cost: 0, avg_latency_ms: 0, cache_hits: 0, cost_per_request: 0 };
    const latencyTotal = current.avg_latency_ms * current.requests + row.avg_latency_ms * row.requests;
    current.requests += row.requests; current.errors += row.errors; current.input_tokens += row.input_tokens; current.output_tokens += row.output_tokens; current.training_tokens += row.training_tokens || 0; current.total_tokens += row.total_tokens; current.input_characters += row.input_characters || 0; current.input_pages += row.input_pages || 0; current.input_audio_milliseconds += row.input_audio_milliseconds || 0; current.video_seconds += row.video_seconds || 0; current.output_images += row.output_images || 0; current.tool_requests += row.tool_requests || 0; current.cache_read_input_tokens += row.cache_read_input_tokens || 0; current.cache_write_input_tokens += row.cache_write_input_tokens || 0; current.search_requests += row.search_requests || 0; current.cache_hits += row.cache_hits;
    current.avg_latency_ms = current.requests ? latencyTotal / current.requests : 0; grouped.set(date, current);
  }
  return [...grouped.values()].sort((left, right) => String(left.date).localeCompare(String(right.date)));
}

const usageColumns = [
  { key: "name", label: "Name" }, { key: "requests", label: "Requests" }, { key: "successful", label: "Successful" }, { key: "errors", label: "Failed" },
  { key: "success_rate", label: "Success rate" }, { key: "total_tokens", label: "Total tokens" }, { key: "input_tokens", label: "Input tokens" },
  { key: "output_tokens", label: "Output tokens" }, { key: "training_tokens", label: "Training tokens" }, { key: "input_characters", label: "Input characters" }, { key: "input_pages", label: "Input pages" }, { key: "input_audio_milliseconds", label: "Input audio (ms)" }, { key: "video_seconds", label: "Video (seconds)" }, { key: "output_images", label: "Images" }, { key: "tool_requests", label: "Tool calls" }, { key: "cache_read_input_tokens", label: "Cache read tokens" }, { key: "cache_write_input_tokens", label: "Cache write tokens" }, { key: "search_requests", label: "Searches" }, { key: "cache_hits", label: "Cache hits" }, { key: "latency", label: "Average latency" },
  { key: "spend", label: "Spend" }, { key: "cost_per_request_display", label: "Cost / request" }, { key: "currency", label: "Currency" }
];

export function UsagePage() {
  const { client } = useAuth();
  const [draft, setDraft] = useState(defaultFilters);
  const [filters, setFilters] = useState(defaultFilters);
  const [view, setView] = useState<UsageView>("overview");
  const [report, setReport] = useState<UsageReport | null>(null);
  const [error, setError] = useState("");
  const [drilldown, setDrilldown] = useState<UsageDrilldown>();
  const [drilldownReport, setDrilldownReport] = useState<UsageReport | null>(null);
  const [drilldownError, setDrilldownError] = useState("");
  const loadGeneration = useRef(0);
  const activeLoad = useRef<AbortController | null>(null);
  const drilldownGeneration = useRef(0);
  const activeDrilldown = useRef<AbortController | null>(null);
  const load = useCallback(async () => {
    activeLoad.current?.abort();
    const controller = new AbortController(); activeLoad.current = controller;
    const generation = ++loadGeneration.current;
    setReport(null); setError("");
    try {
      const next = await client.request<UsageReport>(`/admin/v1/usage/report?${queryFor(filters)}`, { signal: controller.signal });
      if (!controller.signal.aborted && generation === loadGeneration.current) setReport(next);
    } catch (cause) {
      if (!controller.signal.aborted && generation === loadGeneration.current) setError(cause instanceof Error ? cause.message : "Could not load usage");
    }
  }, [client, filters]);
  useEffect(() => {
    void load();
    activeDrilldown.current?.abort(); setDrilldown(undefined);
    return () => { activeLoad.current?.abort(); activeDrilldown.current?.abort(); };
  }, [load]);
  function closeDrilldown() { activeDrilldown.current?.abort(); setDrilldown(undefined); }
  const totals = report ? totalsFor(report) : { requests: 0, errors: 0, successful: 0, tokens: 0, cacheHits: 0, cacheReadTokens: 0, cacheWriteTokens: 0, weightedLatency: 0, spend: formatCost(0, "USD") };
  const drilldownTotals = drilldownReport ? totalsFor(drilldownReport) : null;
  const daily = report?.daily || [];
  function apply(event: FormEvent) { event.preventDefault(); if (draft.window === "custom" && (!draft.from || !draft.to)) return; setFilters(draft); }
  async function inspectDimension(selectedView: Exclude<UsageView, "overview">, name: string) {
    activeDrilldown.current?.abort();
    const controller = new AbortController(); activeDrilldown.current = controller;
    const generation = ++drilldownGeneration.current;
    setDrilldown({ view: selectedView, name }); setDrilldownReport(null); setDrilldownError("");
    const query = queryFor(filters);
    const scopeType = scopeTypes[selectedView];
    if (scopeType) { query.set("scope_type", scopeType); query.set("scope_id", name); }
    else query.set(drilldownParameters[selectedView].parameter, name);
    try {
      const next = await client.request<UsageReport>(`/admin/v1/usage/report?${query}`, { signal: controller.signal });
      if (!controller.signal.aborted && generation === drilldownGeneration.current) setDrilldownReport(next);
    } catch (cause) { if (!controller.signal.aborted && generation === drilldownGeneration.current) setDrilldownError(cause instanceof Error ? cause.message : "Could not load usage details"); }
  }
  function exportCSV() {
    if (!report) return;
    const lines = ["dimension,type,requests,errors,input_tokens,output_tokens,training_tokens,total_tokens,input_characters,input_pages,input_audio_milliseconds,video_seconds,output_images,tool_requests,cache_read_input_tokens,cache_write_input_tokens,search_requests,cache_hits,avg_latency_ms,cost,currency"];
    for (const [type, rows] of [["public_model", report.by_model || []], ["upstream_model", report.by_upstream_model || []], ["provider", report.by_provider || []], ["endpoint", report.by_endpoint || []], ["tag", report.by_tag || []], ["key", report.by_key || []], ["user", report.by_user || []], ["team", report.by_team || []], ["organization", report.by_organization || []]] as const) for (const row of rows) lines.push([row.name || "", type, row.requests, row.errors, row.input_tokens, row.output_tokens, row.training_tokens || 0, row.total_tokens, row.input_characters || 0, row.input_pages || 0, row.input_audio_milliseconds || 0, row.video_seconds || 0, row.output_images || 0, row.tool_requests || 0, row.cache_read_input_tokens || 0, row.cache_write_input_tokens || 0, row.search_requests || 0, row.cache_hits, row.avg_latency_ms, row.cost, row.currency].map(csvCell).join(","));
    const url = URL.createObjectURL(new Blob([`${lines.join("\r\n")}\r\n`], { type: "text/csv" })); const link = document.createElement("a"); link.href = url; link.download = "ai-gateway-usage.csv"; link.click(); URL.revokeObjectURL(url);
  }
  return <><PageHeader eyebrow="Analytics" title="Usage & spend" description="Gateway activity, spend, token and reliability trends without combining currencies." actions={<><GravityThemeScope className="usage-window"><Select aria-label="Window" size="l" width="max" value={[draft.window]} options={[{ value: "7", content: "7 days" }, { value: "30", content: "30 days" }, { value: "90", content: "90 days" }, { value: "custom", content: "Custom range" }]} onUpdate={([window]) => setDraft({ ...draft, window })} /></GravityThemeScope><GatewayButton view="outlined" size="l" disabled={!report} onClick={exportCSV}>Export CSV</GatewayButton></>} />
    <PageTabs label="Usage views" value={view} items={usageViews.map((item) => ({ value: item.id, label: item.label }))} onUpdate={setView} />
    <form id="usage-filters" className="usage-filter-bar" onSubmit={apply}>{draft.window === "custom" && <><label>From<input aria-label="Usage from" type="date" required value={draft.from} onChange={(event) => setDraft({ ...draft, from: event.target.value })} /></label><label>To<input aria-label="Usage to" type="date" required value={draft.to} onChange={(event) => setDraft({ ...draft, to: event.target.value })} /></label></>}<label htmlFor="usage-model">Model<GravityThemeScope className="gravity-usage-filter"><TextInput id="usage-model" controlProps={{ "aria-label": "Usage model" }} size="l" placeholder="All models" value={draft.model} onUpdate={(model) => setDraft({ ...draft, model })} /></GravityThemeScope></label><label htmlFor="usage-provider">Provider<GravityThemeScope className="gravity-usage-filter"><TextInput id="usage-provider" controlProps={{ "aria-label": "Usage provider" }} size="l" placeholder="All providers" value={draft.provider} onUpdate={(provider) => setDraft({ ...draft, provider })} /></GravityThemeScope></label><label htmlFor="usage-tag">Tag<GravityThemeScope className="gravity-usage-filter"><TextInput id="usage-tag" controlProps={{ "aria-label": "Usage tag" }} size="l" placeholder="All tags" value={draft.tag} onUpdate={(tag) => setDraft({ ...draft, tag })} /></GravityThemeScope></label><GatewayButton size="l" type="submit">Apply</GatewayButton><GatewayButton size="l" type="button" view="outlined" onClick={() => { setDraft(defaultFilters); setFilters(defaultFilters); }}>Reset</GatewayButton></form>
    {error ? <ErrorState message={error} retry={() => void load()} /> : !report ? <LoadingState /> : view === "overview" ? <><div className="usage-stats-grid"><StatCard label="Requests" value={number.format(totals.requests)} detail="Finalized gateway requests" /><StatCard label="Tokens" value={number.format(totals.tokens)} detail="Input and output tokens" /><StatCard label="Spend" value={totals.spend} detail="Separated by currency" /><StatCard label="Average latency" value={number.format(totals.weightedLatency)} detail="ms" /></div><div className="usage-overview-grid"><UsageTrend rows={daily} /><CacheOutcomes requests={totals.requests} successful={totals.successful} errors={totals.errors} cacheHits={totals.cacheHits} cacheReadTokens={totals.cacheReadTokens} cacheWriteTokens={totals.cacheWriteTokens} /></div><section className="usage-breakdown-section"><div><h2>Usage breakdown</h2><p>Switch tabs to compare organizations, teams, users, keys, providers, models and tags.</p></div><ManagedDataTable rows={usageRows(report.by_model || [])} columns={usageColumns} rowKey="_identity" defaultHidden={["input_tokens", "output_tokens", "input_characters", "input_pages", "input_audio_milliseconds", "video_seconds", "output_images", "tool_requests", "cache_read_input_tokens", "cache_write_input_tokens", "cost_per_request_display", "currency"]} onRefresh={load} searchPlaceholder="Search models" actions={(row: Row) => <ActionsMenu label={`Actions for ${String(row.name)}`} items={[{ label: "Inspect", onSelect: () => inspectDimension("models", String(row.name)), disabled: row.name === "Unknown" }]} />} /></section></> : <ManagedDataTable rows={usageRows(view === "models" ? report.by_model || [] : view === "upstream-models" ? report.by_upstream_model || [] : view === "providers" ? report.by_provider || [] : view === "endpoints" ? report.by_endpoint || [] : view === "tags" ? report.by_tag || [] : view === "keys" ? report.by_key || [] : view === "users" ? report.by_user || [] : view === "teams" ? report.by_team || [] : report.by_organization || [])} columns={usageColumns} rowKey="_identity" defaultHidden={["input_tokens", "output_tokens", "input_characters", "input_pages", "input_audio_milliseconds", "video_seconds", "output_images", "tool_requests", "cache_read_input_tokens", "cache_write_input_tokens", "cost_per_request_display", "currency"]} onRefresh={load} searchPlaceholder={`Search ${view}`} actions={(row: Row) => <ActionsMenu label={`Actions for ${String(row.name)}`} items={[{ label: "Inspect", onSelect: () => inspectDimension(view, String(row.name)), disabled: row.name === "Unassigned" || row.name === "Untagged" }]} />} />}
    {drilldown && <ModalFrame label="Usage details" onClose={closeDrilldown}><section className="modal usage-detail-modal"><div className="modal-heading"><div><h2>{drilldownParameters[drilldown.view].label}: {drilldown.name}</h2><span className="muted">Server-filtered activity for the selected period and active filters.</span></div><button className="icon-button" aria-label="Close usage details" onClick={closeDrilldown}>×</button></div>{drilldownError ? <ErrorState message={drilldownError} /> : !drilldownReport || !drilldownTotals ? <LoadingState /> : <><div className="usage-stats-grid"><StatCard label="Requests" value={number.format(drilldownTotals.requests)} /><StatCard label="Successful" value={number.format(drilldownTotals.successful)} /><StatCard label="Failed" value={number.format(drilldownTotals.errors)} /><StatCard label="Tokens" value={number.format(drilldownTotals.tokens)} /><StatCard label="Spend" value={drilldownTotals.spend} /><StatCard label="Cache hits" value={number.format(drilldownTotals.cacheHits)} /><StatCard label="Cache read tokens" value={number.format(drilldownTotals.cacheReadTokens)} /><StatCard label="Cache write tokens" value={number.format(drilldownTotals.cacheWriteTokens)} /><StatCard label="Average latency" value={number.format(drilldownTotals.weightedLatency)} detail="ms" /></div><div className="usage-chart-grid"><UsageBars title="Spend per day" rows={drilldownReport.daily || []} value={(row) => row.cost} format={(value, row) => formatCost(value, row.currency)} /><UsageBars title="Requests per day" rows={aggregateDailyActivity(drilldownReport.daily || [])} value={(row) => row.requests} format={(value) => number.format(value)} /></div></>}</section></ModalFrame>}
  </>;
}
