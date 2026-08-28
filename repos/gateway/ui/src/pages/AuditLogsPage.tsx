import { useCallback, useEffect, useState, type FormEvent } from "react";
import { useAuth } from "../auth/AuthContext";
import { ActionsMenu } from "../components/ActionsMenu";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { ManagedDataTable } from "../components/ManagedDataTable";
import { formatTimestamp } from "../format";

type AuditEvent = { id: number; occurred_at: string; request_id?: string; actor_id?: string; actor_credential_id?: string; action: string; target_type: string; target_id?: string; outcome: string; details?: Record<string, unknown> };
const columns = [
  { key: "occurred_at", label: "Time", render: formatTimestamp }, { key: "action", label: "Action" }, { key: "target_type", label: "Type" }, { key: "target_id", label: "Target" },
  { key: "actor_id", label: "Actor" }, { key: "outcome", label: "Outcome", render: (value: unknown) => <span className={`status ${value === "succeeded" ? "enabled" : "disabled"}`}>{String(value || "—")}</span> },
  { key: "request_id", label: "Request ID" }, { key: "actor_credential_id", label: "Credential" }, { key: "details", label: "Details" }
];

function records(payload: unknown): AuditEvent[] {
  const data = payload && typeof payload === "object" ? (payload as { data?: unknown }).data : undefined;
  return Array.isArray(data) ? data as AuditEvent[] : [];
}

export function AuditLogsPage() {
  const { client } = useAuth();
  const [rows, setRows] = useState<AuditEvent[]>([]);
  const [draft, setDraft] = useState({ actor_id: "", action: "" });
  const [filters, setFilters] = useState(draft);
  const [filterOpen, setFilterOpen] = useState(false);
  const [detail, setDetail] = useState<AuditEvent>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    setLoading(true); setError("");
    try { const query = new URLSearchParams({ limit: "500" }); if (filters.actor_id) query.set("actor_id", filters.actor_id); if (filters.action) query.set("action", filters.action); setRows(records(await client.request(`/admin/v1/audit/events?${query}`))); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load audit logs"); }
    finally { setLoading(false); }
  }, [client, filters]);
  useEffect(() => { void load(); }, [load]);
  function apply(event: FormEvent) { event.preventDefault(); setFilters(draft); setFilterOpen(false); }
  if (loading && !rows.length) return <LoadingState />;
  return <>{error && <ErrorState message={error} retry={() => void load()} />}<ManagedDataTable rows={rows} columns={columns} rowKey="id" defaultHidden={["request_id", "actor_credential_id", "details"]} toolbarExtra={<button className="secondary" onClick={() => setFilterOpen(true)}>Filter{filters.actor_id || filters.action ? " (active)" : ""}</button>} onRefresh={load} searchPlaceholder="Search audit logs" actions={(row) => <ActionsMenu label={`Actions for audit event ${String(row.id)}`} items={[{ label: "Details", onSelect: () => setDetail(row as AuditEvent) }]} />} />{filterOpen && <div className="modal-backdrop" role="presentation"><form className="modal compact-modal" role="dialog" aria-modal="true" aria-label="Filter audit logs" onSubmit={apply}><div className="modal-heading"><h2>Filter audit logs</h2><button type="button" className="icon-button" aria-label="Close filters" onClick={() => setFilterOpen(false)}>×</button></div><div className="form-grid"><label>Actor ID<input value={draft.actor_id} onChange={(event) => setDraft({ ...draft, actor_id: event.target.value })} /></label><label>Action<input value={draft.action} onChange={(event) => setDraft({ ...draft, action: event.target.value })} /></label></div><div className="modal-actions"><button type="button" className="secondary" onClick={() => setDraft({ actor_id: "", action: "" })}>Reset filters</button><button>Apply filters</button></div></form></div>}{detail && <div className="modal-backdrop" role="presentation"><section className="modal" role="dialog" aria-modal="true" aria-label="Audit log details"><div className="modal-heading"><h2>Audit log details</h2><button className="icon-button" aria-label="Close audit details" onClick={() => setDetail(undefined)}>×</button></div><dl className="detail-grid"><div><dt>Event</dt><dd>{detail.id}</dd></div><div><dt>Time</dt><dd>{formatTimestamp(detail.occurred_at)}</dd></div><div><dt>Action</dt><dd>{detail.action}</dd></div><div><dt>Outcome</dt><dd>{detail.outcome}</dd></div><div><dt>Target</dt><dd>{detail.target_type} · {detail.target_id || "—"}</dd></div><div><dt>Actor</dt><dd>{detail.actor_id || "—"}</dd></div><div><dt>Request</dt><dd>{detail.request_id || "—"}</dd></div><div><dt>Credential</dt><dd>{detail.actor_credential_id || "—"}</dd></div></dl><h3>Metadata</h3><pre>{JSON.stringify(detail.details || {}, null, 2)}</pre></section></div>}</>;
}
