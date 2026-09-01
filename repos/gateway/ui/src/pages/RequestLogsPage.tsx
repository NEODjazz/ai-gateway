import { useCallback, useEffect, useMemo, useState, type FormEvent, type ReactNode } from "react";
import { useAuth } from "../auth/AuthContext";
import { DataTable, type Row } from "../components/DataTable";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { PageHeader } from "../components/PageHeader";
import { ActionsMenu } from "../components/ActionsMenu";
import { ColumnsMenu } from "../components/ColumnsMenu";
import { formatCost, formatTimestamp } from "../format";

type RequestLog = Row & {
  timestamp: string;
  request_id: string;
  session_id?: string;
  trace_id?: string;
  tags?: string[];
  status: string;
  model?: string;
  upstream_model?: string;
  provider?: string;
  provider_id?: string;
  provider_endpoint_name?: string;
  provider_endpoint_type?: string;
  cache_status?: string;
  cache_kind?: string;
  input_tokens: number;
  output_tokens: number;
  total_tokens: number;
  latency_ms: number;
  first_token_latency_ms: number;
  retry_count: number;
  fallback_count: number;
  usage_estimated: boolean;
  cost: number;
  currency: string;
};

type LogsResponse = { data?: RequestLog[]; next_before?: string; next_request_id?: string };
type Cursor = { before: string; requestID: string } | null;
type IdentityOption = { id: string; name?: string; email?: string };
type LogView = "requests" | "sessions" | "traces";
type GroupedRequestLog = Row & {
  id: string; group_id: string; requests: number; errors: number; models: string[]; providers: string[];
  total_tokens: number; cache_hits: number; latency_ms: number; cost: number; currency: string; started_at: string; ended_at: string;
};
const emptyFilters = { request_id: "", session_id: "", trace_id: "", status: "", model: "", provider: "", cache_status: "", organization_id: "", team_id: "", user_id: "", credential_id: "" };
const requestLogColumns = [
  { key: "timestamp", label: "Time" }, { key: "request_id", label: "Request" }, { key: "session_id", label: "Session" }, { key: "trace_id", label: "Trace" }, { key: "tags", label: "Tags" }, { key: "status", label: "Status" },
  { key: "model", label: "Public model" }, { key: "upstream_model", label: "Upstream model" }, { key: "provider_id", label: "Provider" },
  { key: "cache_status", label: "Cache" }, { key: "cache_kind", label: "Cache type" }, { key: "total_tokens", label: "Tokens" },
  { key: "usage_estimated", label: "Token source" }, { key: "latency_ms", label: "Latency" }, { key: "first_token_latency_ms", label: "TTFT" },
  { key: "retry_count", label: "Retries" }, { key: "fallback_count", label: "Fallbacks" }, { key: "cost", label: "Cost" }, { key: "currency", label: "Currency" }
];
function RefreshIcon() { return <svg aria-hidden="true" viewBox="0 0 24 24"><path d="M20 11a8 8 0 1 0-2.34 5.66M20 4v7h-7" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /></svg>; }

function cacheStatus(value: unknown): ReactNode {
  if (typeof value !== "string" || !value) return <span className="muted">—</span>;
  const hit = value === "hit";
  return <span className={`status ${hit ? "enabled" : "disabled"}`}>{hit ? "Hit" : value[0].toUpperCase() + value.slice(1)}</span>;
}

export function groupRequestLogs(rows: RequestLog[], field: "session_id" | "trace_id"): GroupedRequestLog[] {
  const groups = new Map<string, GroupedRequestLog & { latency_total: number }>();
  for (const row of rows) {
    const groupID = String(row[field] || `Unassigned · ${row.request_id}`);
    // A session may span providers with different billing currencies. Keeping
    // currency in the key prevents misleading cross-currency spend totals.
    const currency = row.currency || "USD";
    const key = `${groupID}\0${currency}`;
    const current = groups.get(key) || {
      id: key, group_id: groupID, requests: 0, errors: 0, models: [], providers: [], total_tokens: 0,
      cache_hits: 0, latency_ms: 0, latency_total: 0, cost: 0, currency, started_at: row.timestamp, ended_at: row.timestamp
    };
    current.requests += 1;
    current.errors += row.status === "error" ? 1 : 0;
    current.total_tokens += Number(row.total_tokens || 0);
    current.cache_hits += row.cache_status === "hit" ? 1 : 0;
    current.latency_total += Number(row.latency_ms || 0);
    current.latency_ms = current.latency_total / current.requests;
    current.cost += Number(row.cost || 0);
    if (row.model && !current.models.includes(row.model)) current.models.push(row.model);
    const provider = row.provider_id || row.provider || row.provider_endpoint_name;
    if (provider && !current.providers.includes(provider)) current.providers.push(provider);
    if (row.timestamp < current.started_at) current.started_at = row.timestamp;
    if (row.timestamp > current.ended_at) current.ended_at = row.timestamp;
    groups.set(key, current);
  }
  return [...groups.values()].sort((left, right) => right.ended_at.localeCompare(left.ended_at));
}

export function RequestLogsPage({ embedded = false }: { embedded?: boolean }) {
  const { client } = useAuth();
  const [rows, setRows] = useState<RequestLog[]>([]);
  const [filters, setFilters] = useState(emptyFilters);
  const [days, setDays] = useState("7");
  const [view, setView] = useState<LogView>("requests");
  const [liveTail, setLiveTail] = useState(false);
  const [organizations, setOrganizations] = useState<IdentityOption[]>([]);
  const [teams, setTeams] = useState<IdentityOption[]>([]);
  const [users, setUsers] = useState<IdentityOption[]>([]);
  const [nextCursor, setNextCursor] = useState<Cursor>(null);
  const [detail, setDetail] = useState<unknown>();
  const [settings, setSettings] = useState<unknown>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [filtersOpen, setFiltersOpen] = useState(false);
  const [visibleColumns, setVisibleColumns] = useState(() => new Set(requestLogColumns.map((column) => column.key)));
  const load = useCallback(async (append = false, requestedCursor: Cursor = null) => {
    setLoading(true); setError("");
    const query = new URLSearchParams({ limit: "50", days });
    for (const [key, value] of Object.entries(filters)) if (value) query.set(key, value);
    if (append && requestedCursor) {
      query.set("before", requestedCursor.before);
      query.set("before_request_id", requestedCursor.requestID);
    }
    try {
      const response = await client.request<LogsResponse>(`/admin/v1/request-logs?${query}`);
      setRows((current) => append ? [...current, ...(response.data || [])] : response.data || []);
      setNextCursor(response.next_before && response.next_request_id ? { before: response.next_before, requestID: response.next_request_id } : null);
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load request logs"); }
    finally { setLoading(false); }
  }, [client, days, filters]);
  useEffect(() => {
    client.request("/admin/v1/request-logs/settings").then(setSettings).catch(() => undefined);
    const loadOptions = async (path: string, setter: (rows: IdentityOption[]) => void) => {
      try {
        const response = await client.request<{ data?: IdentityOption[] }>(`${path}?limit=500`);
        setter(response.data || []);
      } catch { setter([]); }
    };
    void loadOptions("/admin/v1/organizations", setOrganizations);
    void loadOptions("/admin/v1/teams", setTeams);
    void loadOptions("/admin/v1/users", setUsers);
  }, []);
  useEffect(() => { void load(); }, [days]); // Time-window changes reset the cursor and refresh immediately.
  useEffect(() => {
    if (!liveTail) return;
    const timer = window.setInterval(() => { void load(false); }, 15_000);
    return () => window.clearInterval(timer);
  }, [liveTail, load]);
  async function apply(event: FormEvent) { event.preventDefault(); setFiltersOpen(false); await load(false); }
  async function showDetail(row: RequestLog) {
    try { setDetail(await client.request(`/admin/v1/request-logs/${encodeURIComponent(row.request_id)}`)); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load request details"); }
  }
  const columns = [
    { key: "timestamp", label: "Time", render: formatTimestamp },
    { key: "request_id", label: "Request" },
    { key: "session_id", label: "Session" },
    { key: "trace_id", label: "Trace" },
    { key: "tags", label: "Tags" },
    { key: "status", label: "Status" },
    { key: "model", label: "Public model" },
    { key: "upstream_model", label: "Upstream model" },
    { key: "provider_id", label: "Provider", render: (_: unknown, row: Row) => String(row.provider_id || row.provider || row.provider_endpoint_name || "—") },
    { key: "cache_status", label: "Cache", render: cacheStatus },
    { key: "cache_kind", label: "Cache type", render: (value: unknown) => String(value || "—") },
    { key: "total_tokens", label: "Tokens" },
    { key: "usage_estimated", label: "Token source", render: (value: unknown) => value ? <span className="status disabled">Estimated</span> : <span className="status enabled">Provider</span> },
    { key: "latency_ms", label: "Latency", render: (value: unknown) => `${Number(value || 0).toLocaleString("en-US")} ms` },
    { key: "first_token_latency_ms", label: "TTFT", render: (value: unknown) => Number(value || 0) > 0 ? `${Number(value).toLocaleString("en-US")} ms` : "—" },
    { key: "retry_count", label: "Retries" },
    { key: "fallback_count", label: "Fallbacks" },
    { key: "cost", label: "Cost", render: (value: unknown, row: Row) => formatCost(Number(value || 0), String(row.currency || "USD")) },
    { key: "currency", label: "Currency" }
  ];
  const groupedRows = useMemo(() => view === "sessions" ? groupRequestLogs(rows, "session_id") : view === "traces" ? groupRequestLogs(rows, "trace_id") : [], [rows, view]);
  const groupedColumns = [
    { key: "group_id", label: view === "sessions" ? "Session" : "Trace" }, { key: "requests", label: "Requests" }, { key: "errors", label: "Errors" },
    { key: "models", label: "Models" }, { key: "providers", label: "Providers" }, { key: "total_tokens", label: "Tokens" }, { key: "cache_hits", label: "Cache hits" },
    { key: "latency_ms", label: "Average latency", render: (value: unknown) => `${Number(value || 0).toLocaleString("en-US", { maximumFractionDigits: 1 })} ms` },
    { key: "cost", label: "Spend", render: (value: unknown, row: Row) => formatCost(Number(value || 0), String(row.currency || "USD")) }, { key: "currency", label: "Currency" },
    { key: "started_at", label: "Started", render: formatTimestamp }, { key: "ended_at", label: "Last request", render: formatTimestamp }
  ];
  return <>{!embedded && <PageHeader eyebrow="Observability" title="Request logs" description="Cursor-paginated final outcomes and session identity without prompts, responses or raw provider errors." />}<div className="page-tabs" role="tablist" aria-label="Request log views"><button role="tab" aria-selected={view === "requests"} className={view === "requests" ? "active" : ""} onClick={() => setView("requests")}>Requests</button><button role="tab" aria-selected={view === "sessions"} className={view === "sessions" ? "active" : ""} onClick={() => setView("sessions")}>Sessions</button><button role="tab" aria-selected={view === "traces"} className={view === "traces" ? "active" : ""} onClick={() => setView("traces")}>Traces</button></div><div className="key-toolbar"><div className="key-toolbar-right"><label>Window<select aria-label="Request log window" value={days} onChange={(event) => setDays(event.target.value)}><option value="7">7 days</option><option value="30">30 days</option><option value="90">90 days</option></select></label><label><input type="checkbox" checked={liveTail} onChange={(event) => setLiveTail(event.target.checked)} /> Live tail</label>{liveTail && <span className="status enabled">Every 15s</span>}</div><div className="key-toolbar-right"><label className="key-search"><span className="sr-only">Search request logs</span><input aria-label="Search request logs" placeholder="Search by request ID" value={filters.request_id} onChange={(event) => setFilters((current) => ({ ...current, request_id: event.target.value }))} onKeyDown={(event) => { if (event.key === "Enter") void load(false); }} /></label>{view === "requests" && <ColumnsMenu columns={requestLogColumns} visible={visibleColumns} onChange={setVisibleColumns} />}<button className="secondary" onClick={() => setFiltersOpen(true)}>Filter{Object.entries(filters).some(([key, value]) => key !== "request_id" && value) ? " (active)" : ""}</button><button className="secondary icon-only-button" aria-label="Refresh request logs" onClick={() => void load(false)}><RefreshIcon /></button></div></div>{settings !== undefined && <div className="operation-result">Privacy settings: {JSON.stringify(settings)}</div>}{view !== "requests" && <p className="muted">Aggregated from the loaded request window; spend remains separated by currency.</p>}{error && <ErrorState message={error} retry={() => void load()} />}{loading && !rows.length ? <LoadingState /> : view === "requests" ? <DataTable rows={rows} columns={columns.filter((column) => visibleColumns.has(column.key))} actions={(row) => <ActionsMenu label={`Actions for ${String(row.request_id)}`} items={[{ label: "Details", onSelect: () => showDetail(row as RequestLog) }]} />} /> : <DataTable rows={groupedRows} columns={groupedColumns} />}{nextCursor && <button className="secondary load-more" onClick={() => void load(true, nextCursor)}>Load older</button>}{filtersOpen && <div className="modal-backdrop" role="presentation"><form className="modal compact-modal" role="dialog" aria-modal="true" aria-label="Filter request logs" onSubmit={apply}><div className="modal-heading"><h2>Filter request logs</h2><button type="button" className="icon-button" aria-label="Close filters" onClick={() => setFiltersOpen(false)}>×</button></div><div className="form-grid"><label>Session ID<input value={filters.session_id} onChange={(event) => setFilters((current) => ({ ...current, session_id: event.target.value }))} /></label><label>Trace ID<input value={filters.trace_id} onChange={(event) => setFilters((current) => ({ ...current, trace_id: event.target.value }))} /></label><label>Status<select value={filters.status} onChange={(event) => setFilters((current) => ({ ...current, status: event.target.value }))}><option value="">All statuses</option><option value="ok">Success</option><option value="error">Error</option></select></label><label>Cache<select value={filters.cache_status} onChange={(event) => setFilters((current) => ({ ...current, cache_status: event.target.value }))}><option value="">All requests</option><option value="hit">Hit</option><option value="miss">Miss</option><option value="error">Error</option></select></label><label>Model<input value={filters.model} onChange={(event) => setFilters((current) => ({ ...current, model: event.target.value }))} /></label><label>Provider<input value={filters.provider} onChange={(event) => setFilters((current) => ({ ...current, provider: event.target.value }))} /></label><label>Organization<select value={filters.organization_id} onChange={(event) => setFilters((current) => ({ ...current, organization_id: event.target.value }))}><option value="">All organizations</option>{organizations.map((item) => <option key={item.id} value={item.id}>{item.name || item.id}</option>)}</select></label><label>Team<select value={filters.team_id} onChange={(event) => setFilters((current) => ({ ...current, team_id: event.target.value }))}><option value="">All teams</option>{teams.map((item) => <option key={item.id} value={item.id}>{item.name || item.id}</option>)}</select></label><label>User<select value={filters.user_id} onChange={(event) => setFilters((current) => ({ ...current, user_id: event.target.value }))}><option value="">All users</option>{users.map((item) => <option key={item.id} value={item.id}>{item.name || item.email || item.id}</option>)}</select></label><label>Credential ID<input value={filters.credential_id} onChange={(event) => setFilters((current) => ({ ...current, credential_id: event.target.value }))} /></label></div><div className="modal-actions"><button type="button" className="secondary" onClick={() => setFilters(emptyFilters)}>Reset filters</button><button>Apply filters</button></div></form></div>}{detail !== undefined && <div className="modal-backdrop" role="presentation"><section className="modal" role="dialog" aria-modal="true" aria-label="Request details"><div className="modal-heading"><h2>Request details</h2><button className="icon-button" aria-label="Close" onClick={() => setDetail(undefined)}>×</button></div><pre>{JSON.stringify(detail, null, 2)}</pre></section></div>}</>;
}
