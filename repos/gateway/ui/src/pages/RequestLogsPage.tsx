import { useCallback, useEffect, useState, type FormEvent } from "react";
import { useAuth } from "../auth/AuthContext";
import { DataTable, type Row } from "../components/DataTable";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { PageHeader } from "../components/PageHeader";

type LogsResponse = { data?: Row[]; next_cursor?: string };

export function RequestLogsPage() {
  const { client } = useAuth();
  const [rows, setRows] = useState<Row[]>([]);
  const [filters, setFilters] = useState({ request_id: "", session_id: "", status: "", model: "", provider: "" });
  const [nextCursor, setNextCursor] = useState("");
  const [detail, setDetail] = useState<unknown>();
  const [settings, setSettings] = useState<unknown>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const load = useCallback(async (append = false, requestedCursor = "") => {
    setLoading(true); setError("");
    const query = new URLSearchParams({ limit: "50" });
    for (const [key, value] of Object.entries(filters)) if (value) query.set(key, value);
    if (append && requestedCursor) query.set("cursor", requestedCursor);
    try { const response = await client.request<LogsResponse>(`/admin/v1/request-logs?${query}`); setRows((current) => append ? [...current, ...(response.data || [])] : response.data || []); setNextCursor(response.next_cursor || ""); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load request logs"); }
    finally { setLoading(false); }
  }, [client, filters]);
  useEffect(() => { void load(); client.request("/admin/v1/request-logs/settings").then(setSettings).catch(() => undefined); }, []); // Initial request intentionally uses empty filters.
  async function apply(event: FormEvent) { event.preventDefault(); await load(false); }
  async function showDetail(row: Row) {
    try { setDetail(await client.request(`/admin/v1/request-logs/${encodeURIComponent(String(row.request_id))}`)); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load request details"); }
  }
  return <><PageHeader eyebrow="Observability" title="Request logs" description="Cursor-paginated final outcomes and session identity without prompts, responses or raw provider errors." /><form className="filter-card logs-filter" onSubmit={apply}>{Object.keys(filters).map((key) => <label key={key}>{key.replace("_", " ")}<input value={filters[key as keyof typeof filters]} onChange={(event) => setFilters((current) => ({ ...current, [key]: event.target.value }))} /></label>)}<button>Apply</button></form>{settings !== undefined && <div className="operation-result">Privacy settings: {JSON.stringify(settings)}</div>}{error && <ErrorState message={error} retry={() => void load()} />}{loading && !rows.length ? <LoadingState /> : <DataTable rows={rows} columns={[{ key: "occurred_at", label: "Time" }, { key: "request_id", label: "Request" }, { key: "session_id", label: "Session" }, { key: "status", label: "Status" }, { key: "model", label: "Public model" }, { key: "upstream_model", label: "Upstream model" }, { key: "provider", label: "Provider" }, { key: "total_tokens", label: "Tokens" }, { key: "latency_ms", label: "Latency" }, { key: "cost", label: "Cost" }]} actions={(row) => <button className="text-button" onClick={() => void showDetail(row)}>Details</button>} />}{nextCursor && <button className="secondary load-more" onClick={() => void load(true, nextCursor)}>Load older</button>}{detail !== undefined && <div className="modal-backdrop" role="presentation"><section className="modal" role="dialog" aria-modal="true" aria-label="Request details"><div className="modal-heading"><h2>Request details</h2><button className="icon-button" aria-label="Close" onClick={() => setDetail(undefined)}>×</button></div><pre>{JSON.stringify(detail, null, 2)}</pre></section></div>}</>;
}
