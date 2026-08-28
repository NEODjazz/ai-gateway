import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from "react";
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
  status: string;
  model?: string;
  upstream_model?: string;
  provider?: string;
  provider_id?: string;
  provider_endpoint_name?: string;
  provider_endpoint_type?: string;
  cache_status?: string;
  input_tokens: number;
  output_tokens: number;
  total_tokens: number;
  latency_ms: number;
  cost: number;
  currency: string;
};

type LogsResponse = { data?: RequestLog[]; next_before?: string; next_request_id?: string };
type Cursor = { before: string; requestID: string } | null;
const requestLogColumns = [
  { key: "timestamp", label: "Time" }, { key: "request_id", label: "Request" }, { key: "session_id", label: "Session" }, { key: "status", label: "Status" },
  { key: "model", label: "Public model" }, { key: "upstream_model", label: "Upstream model" }, { key: "provider_id", label: "Provider" },
  { key: "cache_status", label: "Cache" }, { key: "total_tokens", label: "Tokens" }, { key: "latency_ms", label: "Latency" }, { key: "cost", label: "Cost" }, { key: "currency", label: "Currency" }
];
function RefreshIcon() { return <svg aria-hidden="true" viewBox="0 0 24 24"><path d="M20 11a8 8 0 1 0-2.34 5.66M20 4v7h-7" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /></svg>; }

function cacheStatus(value: unknown): ReactNode {
  if (typeof value !== "string" || !value) return <span className="muted">—</span>;
  const hit = value === "hit";
  return <span className={`status ${hit ? "enabled" : "disabled"}`}>{hit ? "Hit" : value[0].toUpperCase() + value.slice(1)}</span>;
}

export function RequestLogsPage({ embedded = false }: { embedded?: boolean }) {
  const { client } = useAuth();
  const [rows, setRows] = useState<RequestLog[]>([]);
  const [filters, setFilters] = useState({ request_id: "", session_id: "", status: "", model: "", provider: "" });
  const [nextCursor, setNextCursor] = useState<Cursor>(null);
  const [detail, setDetail] = useState<unknown>();
  const [settings, setSettings] = useState<unknown>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [filtersOpen, setFiltersOpen] = useState(false);
  const [visibleColumns, setVisibleColumns] = useState(() => new Set(requestLogColumns.map((column) => column.key)));
  const load = useCallback(async (append = false, requestedCursor: Cursor = null) => {
    setLoading(true); setError("");
    const query = new URLSearchParams({ limit: "50" });
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
  }, [client, filters]);
  useEffect(() => { void load(); client.request("/admin/v1/request-logs/settings").then(setSettings).catch(() => undefined); }, []); // Initial request intentionally uses empty filters.
  async function apply(event: FormEvent) { event.preventDefault(); setFiltersOpen(false); await load(false); }
  async function showDetail(row: RequestLog) {
    try { setDetail(await client.request(`/admin/v1/request-logs/${encodeURIComponent(row.request_id)}`)); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load request details"); }
  }
  const columns = [
    { key: "timestamp", label: "Time", render: formatTimestamp },
    { key: "request_id", label: "Request" },
    { key: "session_id", label: "Session" },
    { key: "status", label: "Status" },
    { key: "model", label: "Public model" },
    { key: "upstream_model", label: "Upstream model" },
    { key: "provider_id", label: "Provider", render: (_: unknown, row: Row) => String(row.provider_id || row.provider || row.provider_endpoint_name || "—") },
    { key: "cache_status", label: "Cache", render: cacheStatus },
    { key: "total_tokens", label: "Tokens" },
    { key: "latency_ms", label: "Latency", render: (value: unknown) => `${Number(value || 0).toLocaleString("en-US")} ms` },
    { key: "cost", label: "Cost", render: (value: unknown, row: Row) => formatCost(Number(value || 0), String(row.currency || "USD")) },
    { key: "currency", label: "Currency" }
  ];
  return <>{!embedded && <PageHeader eyebrow="Observability" title="Request logs" description="Cursor-paginated final outcomes and session identity without prompts, responses or raw provider errors." />}<div className="key-toolbar"><span /><div className="key-toolbar-right"><label className="key-search"><span className="sr-only">Search request logs</span><input aria-label="Search request logs" placeholder="Search by request ID" value={filters.request_id} onChange={(event) => setFilters((current) => ({ ...current, request_id: event.target.value }))} onKeyDown={(event) => { if (event.key === "Enter") void load(false); }} /></label><ColumnsMenu columns={requestLogColumns} visible={visibleColumns} onChange={setVisibleColumns} /><button className="secondary" onClick={() => setFiltersOpen(true)}>Filter{Object.entries(filters).some(([key, value]) => key !== "request_id" && value) ? " (active)" : ""}</button><button className="secondary icon-only-button" aria-label="Refresh request logs" onClick={() => void load(false)}><RefreshIcon /></button></div></div>{settings !== undefined && <div className="operation-result">Privacy settings: {JSON.stringify(settings)}</div>}{error && <ErrorState message={error} retry={() => void load()} />}{loading && !rows.length ? <LoadingState /> : <DataTable rows={rows} columns={columns.filter((column) => visibleColumns.has(column.key))} actions={(row) => <ActionsMenu label={`Actions for ${String(row.request_id)}`} items={[{ label: "Details", onSelect: () => showDetail(row as RequestLog) }]} />} />}{nextCursor && <button className="secondary load-more" onClick={() => void load(true, nextCursor)}>Load older</button>}{filtersOpen && <div className="modal-backdrop" role="presentation"><form className="modal compact-modal" role="dialog" aria-modal="true" aria-label="Filter request logs" onSubmit={apply}><div className="modal-heading"><h2>Filter request logs</h2><button type="button" className="icon-button" aria-label="Close filters" onClick={() => setFiltersOpen(false)}>×</button></div><div className="form-grid">{Object.keys(filters).map((key) => <label key={key}>{key.replaceAll("_", " ")}<input value={filters[key as keyof typeof filters]} onChange={(event) => setFilters((current) => ({ ...current, [key]: event.target.value }))} /></label>)}</div><div className="modal-actions"><button type="button" className="secondary" onClick={() => setFilters({ request_id: "", session_id: "", status: "", model: "", provider: "" })}>Reset filters</button><button>Apply filters</button></div></form></div>}{detail !== undefined && <div className="modal-backdrop" role="presentation"><section className="modal" role="dialog" aria-modal="true" aria-label="Request details"><div className="modal-heading"><h2>Request details</h2><button className="icon-button" aria-label="Close" onClick={() => setDetail(undefined)}>×</button></div><pre>{JSON.stringify(detail, null, 2)}</pre></section></div>}</>;
}
