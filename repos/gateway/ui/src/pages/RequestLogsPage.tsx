import { ModalFrame } from "../components/ModalFrame";
import { useCallback, useEffect, useMemo, useState, type FormEvent, type ReactNode } from "react";
import { Magnifier } from "@gravity-ui/icons";
import { Icon, TextInput } from "@gravity-ui/uikit";
import { useSearchParams } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import { DataTable, type Row } from "../components/DataTable";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { PageHeader } from "../components/PageHeader";
import { PageTabs } from "../components/PageTabs";
import { ActionsMenu } from "../components/ActionsMenu";
import { ColumnsMenu } from "../components/ColumnsMenu";
import { ToolbarIconButton } from "../components/ToolbarIconButton";
import { GravityThemeScope } from "../components/GravityThemeScope";
import { formatCost, formatTimestamp } from "../format";

type RequestLog = Row & {
  timestamp: string;
  request_id: string;
  session_id?: string;
  trace_id?: string;
  tags?: string[];
  status: string;
  failure_class?: string;
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
  training_tokens: number;
  total_tokens: number;
  input_characters: number;
  input_pages: number;
  input_audio_milliseconds: number;
  video_seconds: number;
  tool_requests: number;
  cache_read_input_tokens: number;
  cache_write_input_tokens: number;
  search_requests: number;
  search_requests_estimated: boolean;
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
type GroupLogsResponse = { data?: GroupedRequestLog[]; next_before?: string; next_before_group_id?: string; next_before_currency?: string };
type GroupCursor = { before: string; groupID: string; currency: string } | null;
type IdentityOption = { id: string; name?: string; email?: string };
type LogView = "requests" | "sessions" | "traces";
type GroupedRequestLog = Row & {
  id: string; group_id: string; requests: number; errors: number; models: string[]; providers: string[];
  total_tokens: number; training_tokens: number; input_characters: number; input_pages: number; input_audio_milliseconds: number; video_seconds: number; tool_requests: number; cache_read_input_tokens: number; cache_write_input_tokens: number; search_requests: number; cache_hits: number; latency_ms: number; cost: number; currency: string; started_at: string; ended_at: string;
};
const emptyFilters = { request_id: "", session_id: "", trace_id: "", status: "", failure_class: "", model: "", provider: "", tag: "", cache_status: "", min_cost: "", max_cost: "", organization_id: "", team_id: "", user_id: "", credential_id: "" };
const filterKeys = Object.keys(emptyFilters) as (keyof typeof emptyFilters)[];
const requestLogColumns = [
  { key: "timestamp", label: "Time" }, { key: "request_id", label: "Request" }, { key: "session_id", label: "Session" }, { key: "trace_id", label: "Trace" }, { key: "tags", label: "Tags" }, { key: "status", label: "Status" }, { key: "failure_class", label: "Failure class" },
  { key: "model", label: "Public model" }, { key: "upstream_model", label: "Upstream model" }, { key: "provider_id", label: "Provider" },
  { key: "cache_status", label: "Cache" }, { key: "cache_kind", label: "Cache type" }, { key: "total_tokens", label: "Tokens" }, { key: "training_tokens", label: "Training tokens" }, { key: "input_characters", label: "Characters" }, { key: "input_pages", label: "Pages" }, { key: "input_audio_milliseconds", label: "Audio (ms)" }, { key: "video_seconds", label: "Video (seconds)" }, { key: "tool_requests", label: "Tool calls" }, { key: "cache_read_input_tokens", label: "Cache read tokens" }, { key: "cache_write_input_tokens", label: "Cache write tokens" }, { key: "search_requests", label: "Searches" },
  { key: "usage_estimated", label: "Token source" }, { key: "latency_ms", label: "Latency" }, { key: "first_token_latency_ms", label: "TTFT" },
  { key: "retry_count", label: "Retries" }, { key: "fallback_count", label: "Fallbacks" }, { key: "cost", label: "Cost" }, { key: "currency", label: "Currency" }
];
function cacheStatus(value: unknown): ReactNode {
  if (typeof value !== "string" || !value) return <span className="muted">—</span>;
  const hit = value === "hit";
  return <span className={`status ${hit ? "enabled" : "disabled"}`}>{hit ? "Hit" : value[0].toUpperCase() + value.slice(1)}</span>;
}

function localDateTime(value: string): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return new Date(date.getTime() - date.getTimezoneOffset() * 60_000).toISOString().slice(0, 16);
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
      id: key, group_id: groupID, requests: 0, errors: 0, models: [], providers: [], total_tokens: 0, training_tokens: 0, input_characters: 0, input_pages: 0, input_audio_milliseconds: 0, video_seconds: 0, tool_requests: 0,
      cache_read_input_tokens: 0, cache_write_input_tokens: 0, search_requests: 0, cache_hits: 0, latency_ms: 0, latency_total: 0, cost: 0, currency, started_at: row.timestamp, ended_at: row.timestamp
    };
    current.requests += 1;
    current.errors += row.status === "error" ? 1 : 0;
    current.total_tokens += Number(row.total_tokens || 0);
    current.training_tokens += Number(row.training_tokens || 0);
    current.input_characters += Number(row.input_characters || 0);
    current.input_pages += Number(row.input_pages || 0);
    current.input_audio_milliseconds += Number(row.input_audio_milliseconds || 0);
    current.video_seconds += Number(row.video_seconds || 0);
    current.tool_requests += Number(row.tool_requests || 0);
    current.cache_read_input_tokens += Number(row.cache_read_input_tokens || 0);
    current.cache_write_input_tokens += Number(row.cache_write_input_tokens || 0);
    current.search_requests += Number(row.search_requests || 0);
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
  const [searchParams, setSearchParams] = useSearchParams();
  const filterSignature = JSON.stringify(filterKeys.map((key) => searchParams.get(key) || ""));
  const filters = useMemo(() => Object.fromEntries(filterKeys.map((key) => [key, searchParams.get(key) || ""])) as typeof emptyFilters, [filterSignature]);
  const requestedDays = searchParams.get("days") || "7";
  const days = ["7", "30", "90"].includes(requestedDays) ? requestedDays : "7";
  const rangeFrom = searchParams.get("from") || "";
  const rangeTo = searchParams.get("to") || "";
  const rangeSignature = `${rangeFrom}\u0000${rangeTo}\u0000${days}`;
  const requestedView = searchParams.get("view");
  const view: LogView = requestedView === "sessions" || requestedView === "traces" ? requestedView : "requests";
  const detailID = searchParams.get("log") || "";
  const [rows, setRows] = useState<RequestLog[]>([]);
  const [groupRows, setGroupRows] = useState<GroupedRequestLog[]>([]);
  const [draftFilters, setDraftFilters] = useState(filters);
  const [windowDraft, setWindowDraft] = useState(rangeFrom && rangeTo ? "custom" : days);
  const [fromDraft, setFromDraft] = useState(localDateTime(rangeFrom));
  const [toDraft, setToDraft] = useState(localDateTime(rangeTo));
  const [liveTail, setLiveTail] = useState(false);
  const [organizations, setOrganizations] = useState<IdentityOption[]>([]);
  const [teams, setTeams] = useState<IdentityOption[]>([]);
  const [users, setUsers] = useState<IdentityOption[]>([]);
  const [nextCursor, setNextCursor] = useState<Cursor>(null);
  const [nextGroupCursor, setNextGroupCursor] = useState<GroupCursor>(null);
  const [detail, setDetail] = useState<unknown>();
  const [settings, setSettings] = useState<unknown>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [filtersOpen, setFiltersOpen] = useState(false);
  const [visibleColumns, setVisibleColumns] = useState(() => new Set(requestLogColumns.map((column) => column.key)));
  const updateQuery = useCallback((updates: Record<string, string>) => {
    const next = new URLSearchParams(searchParams);
    for (const [key, value] of Object.entries(updates)) {
      if (value) next.set(key, value);
      else next.delete(key);
    }
    setSearchParams(next);
  }, [searchParams, setSearchParams]);
  const load = useCallback(async (append = false, requestedCursor: Cursor | GroupCursor = null) => {
    setLoading(true); setError("");
    const query = new URLSearchParams({ limit: "50", days });
    if (rangeFrom && rangeTo) { query.set("from", rangeFrom); query.set("to", rangeTo); }
    for (const [key, value] of Object.entries(filters)) if (value) query.set(key, value);
    try {
      if (view === "requests") {
        const cursor = requestedCursor as Cursor;
        if (append && cursor) {
          query.set("before", cursor.before);
          query.set("before_request_id", cursor.requestID);
        }
        const response = await client.request<LogsResponse>(`/admin/v1/request-logs?${query}`);
        setRows((current) => append ? [...current, ...(response.data || [])] : response.data || []);
        setNextCursor(response.next_before && response.next_request_id ? { before: response.next_before, requestID: response.next_request_id } : null);
      } else {
        const cursor = requestedCursor as GroupCursor;
        query.set("dimension", view === "sessions" ? "session" : "trace");
        if (append && cursor) {
          query.set("before", cursor.before);
          query.set("before_group_id", cursor.groupID);
          query.set("before_currency", cursor.currency);
        }
        const response = await client.request<GroupLogsResponse>(`/admin/v1/request-logs/groups?${query}`);
        const incoming = (response.data || []).map((row) => ({ ...row, id: `${row.group_id}\0${row.currency}` }));
        setGroupRows((current) => append ? [...current, ...incoming] : incoming);
        setNextGroupCursor(response.next_before && response.next_before_group_id && response.next_before_currency ? { before: response.next_before, groupID: response.next_before_group_id, currency: response.next_before_currency } : null);
      }
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load request logs"); }
    finally { setLoading(false); }
  }, [client, days, filters, rangeFrom, rangeTo, view]);
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
  useEffect(() => { void load(); }, [load]);
  useEffect(() => { setDraftFilters(filters); }, [filterSignature]);
  useEffect(() => { setWindowDraft(rangeFrom && rangeTo ? "custom" : days); setFromDraft(localDateTime(rangeFrom)); setToDraft(localDateTime(rangeTo)); }, [rangeSignature]);
  useEffect(() => {
    if (!detailID) { setDetail(undefined); return; }
    let active = true;
    client.request(`/admin/v1/request-logs/${encodeURIComponent(detailID)}`).then((value) => { if (active) setDetail(value); }).catch((cause) => { if (active) setError(cause instanceof Error ? cause.message : "Could not load request details"); });
    return () => { active = false; };
  }, [client, detailID]);
  useEffect(() => {
    if (!liveTail) return;
    const timer = window.setInterval(() => { void load(false); }, 15_000);
    return () => window.clearInterval(timer);
  }, [liveTail, load]);
  function apply(event: FormEvent) {
    event.preventDefault();
    setFiltersOpen(false);
    updateQuery(Object.fromEntries(filterKeys.map((key) => [key, draftFilters[key]])));
  }
  function showDetail(row: RequestLog) { updateQuery({ log: row.request_id }); }
  function applyWindow(event: FormEvent) {
    event.preventDefault();
    if (windowDraft !== "custom") { updateQuery({ days: windowDraft === "7" ? "" : windowDraft, from: "", to: "" }); return; }
    const from = new Date(fromDraft); const to = new Date(toDraft);
    if (!fromDraft || !toDraft || Number.isNaN(from.getTime()) || Number.isNaN(to.getTime()) || to <= from || to.getTime() - from.getTime() > 90 * 24 * 60 * 60 * 1000) { setError("Custom log window must be a valid range of at most 90 days"); return; }
    updateQuery({ days: "", from: from.toISOString(), to: to.toISOString() });
  }
  function showGroupRequests(row: GroupedRequestLog) {
    const unassignedPrefix = "Unassigned · ";
    const requestID = row.group_id.startsWith(unassignedPrefix) ? row.group_id.slice(unassignedPrefix.length) : "";
    updateQuery({ view: "", request_id: requestID, session_id: !requestID && view === "sessions" ? row.group_id : "", trace_id: !requestID && view === "traces" ? row.group_id : "" });
  }
  const columns = [
    { key: "timestamp", label: "Time", render: formatTimestamp },
    { key: "request_id", label: "Request" },
    { key: "session_id", label: "Session" },
    { key: "trace_id", label: "Trace" },
    { key: "tags", label: "Tags" },
    { key: "status", label: "Status" },
    { key: "failure_class", label: "Failure class", render: (value: unknown) => String(value || "—") },
    { key: "model", label: "Public model" },
    { key: "upstream_model", label: "Upstream model" },
    { key: "provider_id", label: "Provider", render: (_: unknown, row: Row) => String(row.provider_id || row.provider || row.provider_endpoint_name || "—") },
    { key: "cache_status", label: "Cache", render: cacheStatus },
    { key: "cache_kind", label: "Cache type", render: (value: unknown) => String(value || "—") },
    { key: "total_tokens", label: "Tokens" }, { key: "training_tokens", label: "Training tokens" }, { key: "input_characters", label: "Characters" }, { key: "input_pages", label: "Pages" }, { key: "input_audio_milliseconds", label: "Audio (ms)" }, { key: "video_seconds", label: "Video (seconds)" }, { key: "tool_requests", label: "Tool calls" },
    { key: "cache_read_input_tokens", label: "Cache read tokens" },
    { key: "cache_write_input_tokens", label: "Cache write tokens" },
    { key: "search_requests", label: "Searches" },
    { key: "usage_estimated", label: "Token source", render: (value: unknown) => value ? <span className="status disabled">Estimated</span> : <span className="status enabled">Provider</span> },
    { key: "latency_ms", label: "Latency", render: (value: unknown) => `${Number(value || 0).toLocaleString("en-US")} ms` },
    { key: "first_token_latency_ms", label: "TTFT", render: (value: unknown) => Number(value || 0) > 0 ? `${Number(value).toLocaleString("en-US")} ms` : "—" },
    { key: "retry_count", label: "Retries" },
    { key: "fallback_count", label: "Fallbacks" },
    { key: "cost", label: "Cost", render: (value: unknown, row: Row) => formatCost(Number(value || 0), String(row.currency || "USD")) },
    { key: "currency", label: "Currency" }
  ];
  const groupedColumns = [
    { key: "group_id", label: view === "sessions" ? "Session" : "Trace" }, { key: "requests", label: "Requests" }, { key: "errors", label: "Errors" },
    { key: "models", label: "Models" }, { key: "providers", label: "Providers" }, { key: "total_tokens", label: "Tokens" }, { key: "training_tokens", label: "Training tokens" }, { key: "input_characters", label: "Characters" }, { key: "input_pages", label: "Pages" }, { key: "input_audio_milliseconds", label: "Audio (ms)" }, { key: "video_seconds", label: "Video (seconds)" }, { key: "tool_requests", label: "Tool calls" }, { key: "cache_read_input_tokens", label: "Cache read tokens" }, { key: "cache_write_input_tokens", label: "Cache write tokens" }, { key: "search_requests", label: "Searches" }, { key: "cache_hits", label: "Cache hits" },
    { key: "latency_ms", label: "Average latency", render: (value: unknown) => `${Number(value || 0).toLocaleString("en-US", { maximumFractionDigits: 1 })} ms` },
    { key: "cost", label: "Spend", render: (value: unknown, row: Row) => formatCost(Number(value || 0), String(row.currency || "USD")) }, { key: "currency", label: "Currency" },
    { key: "started_at", label: "Started", render: formatTimestamp }, { key: "ended_at", label: "Last request", render: formatTimestamp }
  ];
  const activeCursor = view === "requests" ? nextCursor : nextGroupCursor;
  const hasRows = view === "requests" ? rows.length > 0 : groupRows.length > 0;
  return <>
    {!embedded && <PageHeader eyebrow="Observability" title="Request logs" description="Cursor-paginated final outcomes and server-aggregated sessions and traces without prompts, responses or raw provider errors." />}
    <PageTabs label="Request log views" value={view} items={[{ value: "requests", label: "Requests" }, { value: "sessions", label: "Sessions" }, { value: "traces", label: "Traces" }]} onUpdate={(next) => updateQuery({ view: next === "requests" ? "" : next })} />
    <div className="key-toolbar">
      <form className="key-toolbar-right" onSubmit={applyWindow}>
        <label>Window<select aria-label="Request log window" value={windowDraft} onChange={(event) => setWindowDraft(event.target.value)}><option value="7">7 days</option><option value="30">30 days</option><option value="90">90 days</option><option value="custom">Custom</option></select></label>
        {windowDraft === "custom" && <><label>From<input aria-label="Request logs from" type="datetime-local" required value={fromDraft} onChange={(event) => setFromDraft(event.target.value)} /></label><label>To<input aria-label="Request logs to" type="datetime-local" required value={toDraft} onChange={(event) => setToDraft(event.target.value)} /></label></>}
        <button className="secondary">Apply window</button>
        <label><input type="checkbox" checked={liveTail} onChange={(event) => setLiveTail(event.target.checked)} /> Live tail</label>
        {liveTail && <span className="status enabled">Every 15s</span>}
      </form>
      <div className="key-toolbar-right">
        <GravityThemeScope className="gravity-search-scope"><TextInput className="key-search" size="l" type="search" controlProps={{ "aria-label": "Search request logs", onKeyDown: (event) => { if (event.key === "Enter") updateQuery({ request_id: draftFilters.request_id }); } }} placeholder="Search by request ID" value={draftFilters.request_id} startContent={<Icon data={Magnifier} size={16} />} onUpdate={(value) => setDraftFilters((current) => ({ ...current, request_id: value }))} /></GravityThemeScope>
        {view === "requests" && <ColumnsMenu columns={requestLogColumns} visible={visibleColumns} onChange={setVisibleColumns} />}
        <ToolbarIconButton icon="filter" label="Filter" active={Object.entries(filters).some(([key, value]) => key !== "request_id" && Boolean(value))} onClick={() => setFiltersOpen(true)} />
        <ToolbarIconButton icon="refresh" label="Refresh request logs" onClick={() => void load(false)} />
      </div>
    </div>
    {settings !== undefined && <div className="operation-result">Privacy settings: {JSON.stringify(settings)}</div>}
    {view !== "requests" && <p className="muted">Aggregated by ClickHouse across the complete selected window; spend remains separated by currency.</p>}
    {error && <ErrorState message={error} retry={() => void load()} />}
    {loading && !hasRows ? <LoadingState /> : view === "requests"
      ? <DataTable rows={rows} columns={columns.filter((column) => visibleColumns.has(column.key))} actions={(row) => <ActionsMenu label={`Actions for ${String(row.request_id)}`} items={[{ label: "Details", onSelect: () => showDetail(row as RequestLog) }]} />} />
      : <DataTable rows={groupRows} columns={groupedColumns} actions={(row) => <ActionsMenu label={`Actions for ${String(row.group_id)}`} items={[{ label: "View requests", onSelect: () => showGroupRequests(row as GroupedRequestLog) }]} />} />}
    {activeCursor && <button className="secondary load-more" onClick={() => void load(true, activeCursor)}>Load older</button>}
    {filtersOpen && <ModalFrame label="Filter request logs" onClose={() => setFiltersOpen(false)}>
      <form className="modal compact-modal"    onSubmit={apply}>
        <div className="modal-heading"><h2>Filter request logs</h2><button type="button" className="icon-button" aria-label="Close filters" onClick={() => setFiltersOpen(false)}>×</button></div>
        <div className="form-grid">
          <label>Session ID<input value={draftFilters.session_id} onChange={(event) => setDraftFilters((current) => ({ ...current, session_id: event.target.value }))} /></label>
          <label>Trace ID<input value={draftFilters.trace_id} onChange={(event) => setDraftFilters((current) => ({ ...current, trace_id: event.target.value }))} /></label>
          <label>Status<select value={draftFilters.status} onChange={(event) => setDraftFilters((current) => ({ ...current, status: event.target.value }))}><option value="">All statuses</option><option value="ok">Success</option><option value="error">Error</option></select></label>
          <label>Failure class<input value={draftFilters.failure_class} onChange={(event) => setDraftFilters((current) => ({ ...current, failure_class: event.target.value }))} /></label>
          <label>Cache<select value={draftFilters.cache_status} onChange={(event) => setDraftFilters((current) => ({ ...current, cache_status: event.target.value }))}><option value="">All requests</option><option value="hit">Hit</option><option value="miss">Miss</option><option value="error">Error</option></select></label>
          <label>Model<input value={draftFilters.model} onChange={(event) => setDraftFilters((current) => ({ ...current, model: event.target.value }))} /></label>
          <label>Provider<input value={draftFilters.provider} onChange={(event) => setDraftFilters((current) => ({ ...current, provider: event.target.value }))} /></label>
          <label>Tag<input value={draftFilters.tag} onChange={(event) => setDraftFilters((current) => ({ ...current, tag: event.target.value }))} /></label>
          <label>Minimum cost<input type="number" min="0" step="any" value={draftFilters.min_cost} onChange={(event) => setDraftFilters((current) => ({ ...current, min_cost: event.target.value }))} /></label>
          <label>Maximum cost<input type="number" min="0" step="any" value={draftFilters.max_cost} onChange={(event) => setDraftFilters((current) => ({ ...current, max_cost: event.target.value }))} /></label>
          <label>Organization<select value={draftFilters.organization_id} onChange={(event) => setDraftFilters((current) => ({ ...current, organization_id: event.target.value }))}><option value="">All organizations</option>{organizations.map((item) => <option key={item.id} value={item.id}>{item.name || item.id}</option>)}</select></label>
          <label>Team<select value={draftFilters.team_id} onChange={(event) => setDraftFilters((current) => ({ ...current, team_id: event.target.value }))}><option value="">All teams</option>{teams.map((item) => <option key={item.id} value={item.id}>{item.name || item.id}</option>)}</select></label>
          <label>User<select value={draftFilters.user_id} onChange={(event) => setDraftFilters((current) => ({ ...current, user_id: event.target.value }))}><option value="">All users</option>{users.map((item) => <option key={item.id} value={item.id}>{item.name || item.email || item.id}</option>)}</select></label>
          <label>Credential ID<input value={draftFilters.credential_id} onChange={(event) => setDraftFilters((current) => ({ ...current, credential_id: event.target.value }))} /></label>
        </div>
        <div className="modal-actions"><button type="button" className="secondary" onClick={() => setDraftFilters(emptyFilters)}>Reset filters</button><button>Apply filters</button></div>
      </form>
    </ModalFrame>}
    {detail !== undefined && <ModalFrame label="Request details" onClose={() => updateQuery({ log: "" })}><section className="modal"><div className="modal-heading"><h2>Request details</h2><button className="icon-button" aria-label="Close" onClick={() => updateQuery({ log: "" })}>×</button></div><pre>{JSON.stringify(detail, null, 2)}</pre></section></ModalFrame>}
  </>;
}
