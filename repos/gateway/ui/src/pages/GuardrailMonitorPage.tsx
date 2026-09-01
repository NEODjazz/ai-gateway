import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import { ActionsMenu } from "../components/ActionsMenu";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { ManagedDataTable } from "../components/ManagedDataTable";
import { PageHeader } from "../components/PageHeader";
import { StatCard } from "../components/StatCard";
import type { Row } from "../components/DataTable";
import { formatTimestamp } from "../format";

type Summary = { total: number; passed: number; rejected: number; unavailable: number; duration_ms: number; average_duration_ms: number };
type Event = { occurred_at: string; request_id?: string; policy?: string; module: "dlp" | "av"; source: "inference" | "compliance"; outcome: "passed" | "rejected" | "unavailable"; duration_ms: number };
type Bucket = { started_at: string; summary: Summary };
type Filters = { window: "retained" | "15m" | "1h" | "24h"; module?: string; policy?: string; outcome?: string; source?: string };
type Report = { retention: number; retained_events: number; retention_full: boolean; scope: "shared_redis" | "current_replica"; store_available: boolean; store_errors: number; started_at: string; oldest_retained_at?: string; filters: Filters; summary: Summary; by_module: Record<string, Summary>; filtered_summary: Summary; filtered_by_module: Record<string, Summary>; by_policy: Record<string, Summary>; timeline: Bucket[]; events: Event[]; content_stored: boolean };
type Policy = { name: string; description?: string; dlp: boolean; av: boolean; enabled: boolean };

const emptySummary: Summary = { total: 0, passed: 0, rejected: 0, unavailable: 0, duration_ms: 0, average_duration_ms: 0 };

function percentage(value: number, total: number) { return total ? value / total * 100 : 0; }
function status(summary: Summary) { if (!summary.total) return "No data"; if (summary.unavailable > 0) return "Critical"; if (percentage(summary.rejected, summary.total) > 10) return "Warning"; return "Healthy"; }
function records<T>(payload: unknown): T[] { const data = payload && typeof payload === "object" ? (payload as { data?: unknown }).data : undefined; return Array.isArray(data) ? data as T[] : []; }

function Timeline({ buckets }: { buckets: Bucket[] }) {
  const maximum = Math.max(1, ...buckets.map((bucket) => bucket.summary.total));
  if (!buckets.length) return <div className="empty-state guardrail-empty-timeline">No evaluations in the selected window.</div>;
  return <div className="guardrail-timeline" aria-label="Guardrail evaluation timeline">{buckets.map((bucket) => { const blocked = bucket.summary.rejected + bucket.summary.unavailable; return <div className="guardrail-timeline-column" key={bucket.started_at} title={`${formatTimestamp(bucket.started_at)} · ${bucket.summary.total} evaluations · ${blocked} blocked or unavailable`}><div className="guardrail-timeline-bar" style={{ height: `${Math.max(8, bucket.summary.total / maximum * 100)}%` }}><span className="guardrail-timeline-risk" style={{ height: `${percentage(blocked, bucket.summary.total)}%` }} /></div><small>{new Date(bucket.started_at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}</small></div>; })}</div>;
}

function GuardrailFilters({ filters, policies, onChange }: { filters: Filters; policies: Policy[]; onChange: (filters: Filters) => void }) {
  return <section className="filter-bar guardrail-filter-bar" aria-label="Guardrail report filters">
    <label><span>Window</span><select aria-label="Guardrail window" value={filters.window} onChange={(event) => onChange({ ...filters, window: event.target.value as Filters["window"] })}><option value="retained">Retained history</option><option value="15m">Last 15 minutes</option><option value="1h">Last hour</option><option value="24h">Last 24 hours</option></select></label>
    <label><span>Policy</span><select aria-label="Guardrail policy filter" value={filters.policy || ""} onChange={(event) => onChange({ ...filters, policy: event.target.value || undefined })}><option value="">All policies</option>{policies.map((policy) => <option key={policy.name} value={policy.name}>{policy.name}</option>)}</select></label>
    <label><span>Source</span><select aria-label="Guardrail source filter" value={filters.source || ""} onChange={(event) => onChange({ ...filters, source: event.target.value || undefined })}><option value="">All sources</option><option value="inference">Inference</option><option value="compliance">Compliance playground</option></select></label>
    <label><span>Outcome</span><select aria-label="Guardrail outcome filter" value={filters.outcome || ""} onChange={(event) => onChange({ ...filters, outcome: event.target.value || undefined })}><option value="">All outcomes</option><option value="passed">Passed</option><option value="rejected">Rejected</option><option value="unavailable">Unavailable</option></select></label>
    <button className="secondary" onClick={() => onChange({ window: "retained" })}>Reset</button>
  </section>;
}

export function GuardrailMonitorPage() {
  const { client } = useAuth();
  const navigate = useNavigate();
  const { module } = useParams();
  const selectedModule = module === "dlp" || module === "av" ? module : undefined;
  const [filters, setFilters] = useState<Filters>({ window: "retained" });
  const [report, setReport] = useState<Report>();
  const [policies, setPolicies] = useState<Policy[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const load = useCallback(async (signal?: AbortSignal) => {
    setLoading(true); setError("");
    const query = new URLSearchParams({ limit: "250", window: filters.window });
    if (selectedModule) query.set("module", selectedModule);
    if (filters.policy) query.set("policy", filters.policy);
    if (filters.source) query.set("source", filters.source);
    if (filters.outcome) query.set("outcome", filters.outcome);
    try { const [nextReport, policyPayload] = await Promise.all([client.request<Report>(`/admin/v1/guardrails/monitor?${query}`, { signal }), client.request("/admin/v1/guardrail-policies", { signal })]); setReport(nextReport); setPolicies(records<Policy>(policyPayload)); }
    catch (cause) { if (!signal?.aborted) setError(cause instanceof Error ? cause.message : "Could not load guardrail report"); }
    finally { if (!signal?.aborted) setLoading(false); }
  }, [client, filters, selectedModule]);
  useEffect(() => { const controller = new AbortController(); void load(controller.signal); return () => controller.abort(); }, [load]);
  const summary = report?.filtered_summary || emptySummary;
  const passRate = summary.total ? 100 - percentage(summary.rejected + summary.unavailable, summary.total) : 0;
  const moduleRows: Row[] = useMemo(() => ["dlp", "av"].map((name) => { const item = report?.filtered_by_module[name] || emptySummary; const activePolicies = policies.filter((policy) => policy.enabled && policy[name as "dlp" | "av"]).length; return { id: name, module: name.toUpperCase(), evaluations: item.total, rejected: item.rejected, unavailable: item.unavailable, failure_rate: `${percentage(item.rejected + item.unavailable, item.total).toFixed(1)}%`, average_latency: `${Math.round(item.average_duration_ms)} ms`, policies: activePolicies, status: status(item), _module: name }; }), [policies, report]);
  const eventRows: Row[] = (report?.events || []).map((event, index) => ({ id: `${event.request_id || "event"}-${event.occurred_at}-${index}`, occurred_at: formatTimestamp(event.occurred_at), request_id: event.request_id || "—", policy: event.policy || "No policy", module: event.module.toUpperCase(), source: event.source, outcome: event.outcome, duration: `${event.duration_ms} ms` }));
  const policyRows: Row[] = Object.entries(report?.by_policy || {}).map(([name, item]) => ({ id: name || "unassigned", policy: name || "No policy", evaluations: item.total, rejected: item.rejected, unavailable: item.unavailable, failure_rate: `${percentage(item.rejected + item.unavailable, item.total).toFixed(1)}%`, average_latency: `${Math.round(item.average_duration_ms)} ms` }));
  if (loading && !report) return <LoadingState />;
  if (error && !report) return <ErrorState message={error} retry={() => void load()} />;
  if (!report) return <ErrorState message="Guardrail report is unavailable" retry={() => void load()} />;
  const scopeText = report.scope === "shared_redis" ? "Shared Redis history across gateway replicas" : "Current-replica fallback; shared history is unavailable";
  return <><PageHeader eyebrow="Compliance" title={selectedModule ? `${selectedModule.toUpperCase()} guardrail` : "Guardrail monitor"} description={selectedModule ? "Filtered performance, policy impact and metadata-only evaluation events." : "Guardrail performance across retained metadata-only evaluations."} actions={selectedModule ? <button className="secondary" onClick={() => navigate("/guardrails-monitor")}>Back to overview</button> : undefined} />
    {error && <ErrorState message={error} retry={() => void load()} />}
    <GuardrailFilters filters={filters} policies={policies} onChange={setFilters} />
    <section className={`notice-card guardrail-scope ${report.scope === "shared_redis" ? "shared" : "fallback"}`}><h2>{scopeText}</h2><p>{report.retained_events.toLocaleString()} of {report.retention.toLocaleString()} event slots are populated. {report.retention_full ? "Older events may have been evicted. " : ""}{report.store_errors ? `${report.store_errors.toLocaleString()} shared-store operations have failed since this replica started. ` : ""}Prompts, responses and scanner details are not stored.</p></section>
    <div className="usage-stats-grid"><StatCard label="Evaluations" value={summary.total.toLocaleString()} detail={filters.window === "retained" ? "retained events" : filters.window} /><StatCard label="Rejected" value={summary.rejected.toLocaleString()} /><StatCard label="Unavailable" value={summary.unavailable.toLocaleString()} /><StatCard label="Pass rate" value={`${passRate.toFixed(1)}%`} /><StatCard label="Average latency" value={`${Math.round(summary.average_duration_ms)} ms`} /></div>
    <section className="section-block"><h2>Evaluation timeline</h2><p>Server-side buckets for the selected window and filters.</p><Timeline buckets={report.timeline} /></section>
    {!selectedModule ? <section className="section-block"><h2>Guardrail performance</h2><p>Inspect a module for policy breakdown and individual metadata-only outcomes.</p><ManagedDataTable rows={moduleRows} columns={[{ key: "module", label: "Guardrail" }, { key: "evaluations", label: "Evaluations" }, { key: "failure_rate", label: "Failure rate" }, { key: "average_latency", label: "Avg latency" }, { key: "policies", label: "Active policies" }, { key: "status", label: "Status" }]} searchPlaceholder="Search guardrails" onRefresh={load} actions={(row) => <ActionsMenu label={`Actions for guardrail ${row.id}`} items={[{ label: "Inspect", onSelect: () => navigate(`/guardrails-monitor/${row._module}`) }]} />} /></section> : <section className="section-block"><h2>Policy breakdown</h2><p>Only policies represented in retained matching events are shown.</p><ManagedDataTable rows={policyRows} columns={[{ key: "policy", label: "Policy" }, { key: "evaluations", label: "Evaluations" }, { key: "rejected", label: "Rejected" }, { key: "unavailable", label: "Unavailable" }, { key: "failure_rate", label: "Failure rate" }, { key: "average_latency", label: "Avg latency" }]} searchPlaceholder="Search policies" onRefresh={load} /></section>}
    <section className="section-block"><h2>Evaluation events</h2><p>Request correlation and safe outcomes only; no submitted content or raw scanner response.</p><ManagedDataTable rows={eventRows} columns={[{ key: "occurred_at", label: "Time" }, { key: "request_id", label: "Request" }, { key: "policy", label: "Policy" }, { key: "module", label: "Module" }, { key: "source", label: "Source" }, { key: "outcome", label: "Outcome" }, { key: "duration", label: "Latency" }]} defaultHidden={selectedModule ? ["module"] : []} searchPlaceholder="Search guardrail events" onRefresh={load} /></section>
  </>;
}
