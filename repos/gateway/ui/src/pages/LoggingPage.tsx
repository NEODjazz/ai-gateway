import { ModalFrame } from "../components/ModalFrame";
import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { useAuth } from "../auth/AuthContext";
import { ActionsMenu } from "../components/ActionsMenu";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { ManagedDataTable } from "../components/ManagedDataTable";
import { PageHeader } from "../components/PageHeader";
import { StatCard } from "../components/StatCard";
import type { Row } from "../components/DataTable";

type LoggingDestination = Row & { id: string; name: string; type: string; url: string; event_types: string[]; enabled: boolean; secret_configured: boolean };
type DeliveryStats = { queued: number; delivered: number; failed: number; dropped: number };
type LoggingList = { data?: LoggingDestination[]; delivery?: DeliveryStats; content_stored?: boolean };
type ProbeResult = { status: string; latency_ms: number; content_sent: boolean };
type DestinationDraft = { id: string; name: string; type: string; url: string; event_types: string[]; enabled: boolean; secret: string };

const emptyDraft: DestinationDraft = { id: "", name: "", type: "webhook", url: "", event_types: ["request_outcome"], enabled: true, secret: "" };

function DestinationForm({ initial, onClose, onSave }: { initial?: LoggingDestination; onClose: () => void; onSave: (draft: DestinationDraft) => Promise<void> }) {
  const [draft, setDraft] = useState<DestinationDraft>(initial ? { id: initial.id, name: initial.name, type: initial.type, url: initial.url, event_types: [...initial.event_types], enabled: initial.enabled, secret: "" } : { ...emptyDraft, event_types: [...emptyDraft.event_types] });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  async function submit(event: FormEvent) {
    event.preventDefault(); setError("");
    if (!draft.id.trim() || !draft.name.trim() || !draft.url.trim().startsWith("https://")) { setError("ID, name, and an HTTPS destination URL are required."); return; }
    setSaving(true);
    try { await onSave({ ...draft, id: draft.id.trim(), name: draft.name.trim(), url: draft.url.trim(), secret: draft.secret.trim() }); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not save logging destination"); }
    finally { setSaving(false); }
  }
  return <ModalFrame label={initial ? "Edit logging destination" : "Create logging destination"} onClose={onClose}><form className="modal key-form-modal"    onSubmit={submit}>
    <div className="modal-heading"><div><h2>{initial ? "Edit" : "Create"} Logging Destination</h2><span className="muted">Only bounded request metadata is delivered. Prompt and response content is never included.</span></div><button type="button" className="icon-button" aria-label="Close logging destination form" onClick={onClose}>×</button></div>
    <div className="key-form">
      <label><span>ID</span><input aria-label="Logging destination ID" required disabled={Boolean(initial)} value={draft.id} onChange={(event) => setDraft({ ...draft, id: event.target.value })} /></label>
      <label><span>Name</span><input aria-label="Logging destination name" required value={draft.name} onChange={(event) => setDraft({ ...draft, name: event.target.value })} /></label>
      <label><span>Type</span><select aria-label="Logging destination type" value={draft.type} onChange={(event) => setDraft({ ...draft, type: event.target.value })}><option value="webhook">Webhook</option></select></label>
      <label><span>HTTPS URL</span><input aria-label="Logging destination URL" type="url" required placeholder="https://logs.example.com/events" value={draft.url} onChange={(event) => setDraft({ ...draft, url: event.target.value })} /></label>
      <label><span>Events</span><select aria-label="Logging destination events" multiple value={draft.event_types} onChange={(event) => setDraft({ ...draft, event_types: Array.from(event.target.selectedOptions, (option) => option.value) })}><option value="request_outcome">Request outcome</option></select></label>
      <label><span>Bearer secret</span><input aria-label="Logging destination secret" type="password" autoComplete="new-password" placeholder={initial?.secret_configured ? "Leave blank to keep current secret" : "Optional write-only secret"} value={draft.secret} onChange={(event) => setDraft({ ...draft, secret: event.target.value })} /></label>
      <label className="checkbox-line"><input aria-label="Logging destination enabled" type="checkbox" checked={draft.enabled} onChange={(event) => setDraft({ ...draft, enabled: event.target.checked })} /> Enabled</label>
    </div>
    {initial?.secret_configured && <p className="credential-write-only-notice">A secret is configured. It cannot be read back; leave this field blank to preserve it.</p>}
    {error && <p className="form-error" role="alert">{error}</p>}
    <div className="modal-actions"><button type="button" className="secondary" onClick={onClose}>Cancel</button><button disabled={saving}>{saving ? "Saving…" : initial ? "Save changes" : "Create destination"}</button></div>
  </form></ModalFrame>;
}

export function LoggingPage() {
  const { client } = useAuth();
  const [destinations, setDestinations] = useState<LoggingDestination[]>([]);
  const [delivery, setDelivery] = useState<DeliveryStats>({ queued: 0, delivered: 0, failed: 0, dropped: 0 });
  const [contentStored, setContentStored] = useState(false);
  const [editing, setEditing] = useState<LoggingDestination | null | undefined>(undefined);
  const [probe, setProbe] = useState<{ id: string; result: ProbeResult }>();
  const [probing, setProbing] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    setLoading(true); setError("");
    try { const payload = await client.request<LoggingList>("/admin/v1/logging/destinations"); setDestinations(payload.data || []); setDelivery(payload.delivery || { queued: 0, delivered: 0, failed: 0, dropped: 0 }); setContentStored(Boolean(payload.content_stored)); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load logging destinations"); }
    finally { setLoading(false); }
  }, [client]);
  useEffect(() => { void load(); }, [load]);
  async function save(draft: DestinationDraft) {
    const body: Record<string, unknown> = { name: draft.name, type: draft.type, url: draft.url, event_types: draft.event_types, enabled: draft.enabled };
    if (draft.secret) body.secret = draft.secret;
    await client.request(`/admin/v1/logging/destinations/${encodeURIComponent(draft.id)}`, { method: "PUT", body });
    setEditing(undefined); await load();
  }
  async function test(destination: LoggingDestination) {
    setProbing(destination.id); setProbe(undefined); setError("");
    try { const result = await client.request<ProbeResult>(`/admin/v1/logging/destinations/${encodeURIComponent(destination.id)}/test`, { method: "POST" }); setProbe({ id: destination.id, result }); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Destination probe failed"); }
    finally { setProbing(""); }
  }
  async function remove(destination: LoggingDestination) {
    if (!window.confirm(`Delete logging destination ${destination.name}?`)) return;
    try { await client.request(`/admin/v1/logging/destinations/${encodeURIComponent(destination.id)}`, { method: "DELETE" }); if (probe?.id === destination.id) setProbe(undefined); await load(); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not delete logging destination"); }
  }
  const rows = destinations.map((destination) => ({ ...destination, events: destination.event_types, secret: destination.secret_configured ? "Configured" : "Not configured", status: destination.enabled ? "Enabled" : "Disabled", _destination: destination }));
  const columns = useMemo(() => [{ key: "id", label: "Destination" }, { key: "name", label: "Name" }, { key: "type", label: "Type" }, { key: "url", label: "HTTPS URL" }, { key: "events", label: "Events" }, { key: "secret", label: "Secret" }, { key: "status", label: "Status", render: (value: unknown) => <span className={`status ${value === "Enabled" ? "enabled" : "disabled"}`}>{String(value)}</span> }], []);
  if (loading && !destinations.length) return <LoadingState />;
  return <><PageHeader eyebrow="Observability" title="Logging & alerts" description="Metadata-only HTTPS callbacks with write-only authentication and delivery health for the current gateway replica." />
    {error && <ErrorState message={error} retry={() => void load()} />}
    <div className="usage-stats-grid"><StatCard label="Queued" value={delivery.queued} /><StatCard label="Delivered" value={delivery.delivered} /><StatCard label="Failed" value={delivery.failed} /><StatCard label="Dropped" value={delivery.dropped} /></div>
    <section className="notice-card logging-safety"><h2>Content boundary</h2><p>{contentStored ? "Callback content storage is enabled." : "Prompts, responses, raw errors, and bearer credentials are not stored or sent. Only bounded request-outcome metadata is delivered."}</p></section>
    {probe && <section className="notice-card logging-probe-result" role="status"><h2>{probe.id} is available</h2><p>Probe completed in {probe.result.latency_ms.toLocaleString()} ms. Content sent: {probe.result.content_sent ? "yes" : "no"}.</p></section>}
    <ManagedDataTable rows={rows} columns={columns} defaultHidden={["type", "events"]} searchPlaceholder="Search logging destinations" onRefresh={load} primaryAction={<button onClick={() => setEditing(null)}>Create Logging Destination</button>} actions={(row) => { const destination = row._destination as LoggingDestination; return <ActionsMenu label={`Actions for logging destination ${destination.id}`} items={[{ label: probing === destination.id ? "Testing…" : "Test", onSelect: () => test(destination), disabled: Boolean(probing) }, { label: "Edit", onSelect: () => setEditing(destination) }, { label: "Delete", onSelect: () => remove(destination), tone: "danger" }]} />; }} />
    {editing !== undefined && <DestinationForm initial={editing || undefined} onClose={() => setEditing(undefined)} onSave={save} />}
  </>;
}
