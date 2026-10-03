import { ModalFrame } from "../components/ModalFrame";
import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { useAuth } from "../auth/AuthContext";
import { ActionsMenu } from "../components/ActionsMenu";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { ChipMultiSelect, type ChipOption } from "../components/ChipMultiSelect";
import { ManagedDataTable } from "../components/ManagedDataTable";
import { PageHeader } from "../components/PageHeader";
import { StatCard } from "../components/StatCard";
import type { Row } from "../components/DataTable";

type MCPServer = { id: string; label: string; description?: string; server_url: string; transport: "streamable-http" | "sse"; tools?: string[]; enabled: boolean; allow_provider_execution: boolean; credential_configured?: boolean; bearer_token?: string };
type MCPToolset = { id: string; name: string; description?: string; tools: string[]; enabled: boolean };
type ServerReferences = { toolset_ids: string[] };
type ToolsetReferences = { access_group_ids: string[]; virtual_key_ids: string[] };
type ServerPayload = { data?: MCPServer[]; references?: Record<string, ServerReferences> };
type ToolsetPayload = { data?: MCPToolset[]; references?: Record<string, ToolsetReferences> };

function canonicalConnector(id: string, rawURL: string) {
  try {
    const url = new URL(rawURL);
    if (url.protocol !== "https:") return "";
    url.hash = ""; url.search = ""; url.pathname = url.pathname.replace(/\/$/, "");
    return id.trim() ? `mcp:${id.trim()}@${url.toString().replace(/\/$/, "")}` : "";
  } catch { return ""; }
}

function MCPServerForm({ initial, onClose, onSave }: { initial?: MCPServer; onClose: () => void; onSave: (server: MCPServer) => Promise<void> }) {
  const [draft, setDraft] = useState<MCPServer>(initial ? { ...initial, tools: [...(initial.tools || [])] } : { id: "", label: "", description: "", server_url: "", transport: "streamable-http", tools: [], enabled: true, allow_provider_execution: false });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [clearCredential, setClearCredential] = useState(false);
  const suggestion = canonicalConnector(draft.id, draft.server_url);
  async function submit(event: FormEvent) {
    event.preventDefault(); setError("");
    if (!draft.id.trim() || !draft.label.trim() || !draft.server_url.trim()) { setError("ID, label and HTTPS URL are required."); return; }
    if (!(draft.tools || []).length) { setError("Add at least one exact or wildcard connector grant."); return; }
    setSaving(true);
    try {
      const saved = { ...draft, id: draft.id.trim(), label: draft.label.trim(), description: draft.description?.trim(), server_url: draft.server_url.trim() };
      if (clearCredential) saved.bearer_token = "";
      else if (!saved.bearer_token) delete saved.bearer_token;
      await onSave(saved);
    }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not save MCP server"); }
    finally { setSaving(false); }
  }
  return <ModalFrame label={initial ? "Edit MCP server" : "Create MCP server"} onClose={onClose}><form className="modal key-form-modal"    onSubmit={submit}>
    <div className="modal-heading"><div><h2>{initial ? "Edit" : "Create"} MCP Server</h2><span className="muted">Safe endpoint metadata and the connector grants accepted by gateway policy.</span></div><button type="button" className="icon-button" aria-label="Close MCP server form" onClick={onClose}>×</button></div>
    <div className="key-form">
      <label><span>ID</span><input aria-label="MCP server ID" required disabled={Boolean(initial)} value={draft.id} onChange={(event) => setDraft({ ...draft, id: event.target.value })} /></label>
      <label><span>Display label</span><input aria-label="MCP server label" required value={draft.label} onChange={(event) => setDraft({ ...draft, label: event.target.value })} /></label>
      <label><span>Description</span><textarea aria-label="MCP server description" rows={3} value={draft.description || ""} onChange={(event) => setDraft({ ...draft, description: event.target.value })} /></label>
      <label><span>HTTPS server URL</span><input aria-label="MCP server URL" required type="url" placeholder="https://mcp.example.com/v1" value={draft.server_url} onChange={(event) => setDraft({ ...draft, server_url: event.target.value })} /></label>
      <label><span>Transport</span><select aria-label="MCP server transport" value={draft.transport} onChange={(event) => setDraft({ ...draft, transport: event.target.value as MCPServer["transport"] })}><option value="streamable-http">Streamable HTTP</option><option value="sse">SSE</option></select></label>
      <label><span>Server bearer credential</span><input aria-label="MCP server bearer credential" type="password" autoComplete="new-password" placeholder={draft.credential_configured ? "Stored credential remains unchanged" : "Optional"} value={draft.bearer_token || ""} disabled={clearCredential} onChange={(event) => setDraft({ ...draft, bearer_token: event.target.value })} /></label>
      {initial?.credential_configured && <label className="checkbox-line"><input aria-label="Clear MCP server credential" type="checkbox" checked={clearCredential} onChange={(event) => setClearCredential(event.target.checked)} /> Clear stored bearer credential</label>}
      <label className="checkbox-line"><input aria-label="Allow native provider execution" type="checkbox" checked={draft.allow_provider_execution} onChange={(event) => setDraft({ ...draft, allow_provider_execution: event.target.checked })} /> Allow this URL and stored bearer credential to be sent to an explicitly selected native model provider</label>
      <ChipMultiSelect label="Connector grants" options={[]} value={draft.tools || []} onChange={(tools) => setDraft({ ...draft, tools })} allowCustom />
      {suggestion && !(draft.tools || []).includes(suggestion) && <div className="mcp-suggestion"><span><strong>Suggested exact grant</strong><code>{suggestion}</code></span><button type="button" className="secondary" onClick={() => setDraft({ ...draft, tools: [...(draft.tools || []), suggestion] })}>Use suggestion</button></div>}
      <label className="checkbox-line"><input aria-label="MCP server enabled" type="checkbox" checked={draft.enabled} onChange={(event) => setDraft({ ...draft, enabled: event.target.checked })} /> Enabled</label>
    </div>
    {error && <p className="form-error key-form-error" role="alert">{error}</p>}<div className="modal-actions"><button type="button" className="secondary" onClick={onClose}>Cancel</button><button disabled={saving}>{saving ? "Saving…" : initial ? "Save changes" : "Create MCP server"}</button></div>
  </form></ModalFrame>;
}

function MCPToolsetForm({ initial, options, onClose, onSave }: { initial?: MCPToolset; options: ChipOption[]; onClose: () => void; onSave: (toolset: MCPToolset) => Promise<void> }) {
  const [draft, setDraft] = useState<MCPToolset>(initial ? { ...initial, tools: [...initial.tools] } : { id: "", name: "", description: "", tools: [], enabled: true });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const available = useMemo(() => [...options, ...draft.tools.filter((tool) => !options.some((option) => option.value === tool)).map((tool) => ({ value: tool, label: tool, description: "Existing custom grant" }))], [draft.tools, options]);
  async function submit(event: FormEvent) {
    event.preventDefault(); setError("");
    if (!draft.id.trim() || !draft.name.trim()) { setError("ID and name are required."); return; }
    if (!draft.tools.length) { setError("Select at least one connector grant from a configured MCP server."); return; }
    setSaving(true);
    try { await onSave({ ...draft, id: draft.id.trim(), name: draft.name.trim(), description: draft.description?.trim() }); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not save MCP toolset"); }
    finally { setSaving(false); }
  }
  return <ModalFrame label={initial ? "Edit MCP toolset" : "Create MCP toolset"} onClose={onClose}><form className="modal key-form-modal"    onSubmit={submit}>
    <div className="modal-heading"><div><h2>{initial ? "Edit" : "Create"} MCP Toolset</h2><span className="muted">Reusable connector permissions selected from configured servers.</span></div><button type="button" className="icon-button" aria-label="Close MCP toolset form" onClick={onClose}>×</button></div>
    <div className="key-form">
      <label><span>ID</span><input aria-label="MCP toolset ID" required disabled={Boolean(initial)} value={draft.id} onChange={(event) => setDraft({ ...draft, id: event.target.value })} /></label>
      <label><span>Name</span><input aria-label="MCP toolset name" required value={draft.name} onChange={(event) => setDraft({ ...draft, name: event.target.value })} /></label>
      <label><span>Description</span><textarea aria-label="MCP toolset description" rows={3} value={draft.description || ""} onChange={(event) => setDraft({ ...draft, description: event.target.value })} /></label>
      <ChipMultiSelect label="Connector grants" options={available} value={draft.tools} onChange={(tools) => setDraft({ ...draft, tools })} />
      <label className="checkbox-line"><input aria-label="MCP toolset enabled" type="checkbox" checked={draft.enabled} onChange={(event) => setDraft({ ...draft, enabled: event.target.checked })} /> Enabled</label>
    </div>
    {error && <p className="form-error key-form-error" role="alert">{error}</p>}<div className="modal-actions"><button type="button" className="secondary" onClick={onClose}>Cancel</button><button disabled={saving}>{saving ? "Saving…" : initial ? "Save changes" : "Create MCP toolset"}</button></div>
  </form></ModalFrame>;
}

export function MCPServersPage() {
  const { client } = useAuth();
  const [servers, setServers] = useState<MCPServer[]>([]);
  const [references, setReferences] = useState<Record<string, ServerReferences>>({});
  const [editing, setEditing] = useState<MCPServer | null | undefined>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const load = useCallback(async () => { setLoading(true); setError(""); try { const payload = await client.request<ServerPayload>("/admin/v1/mcp/servers?expand=references"); setServers(payload.data || []); setReferences(payload.references || {}); } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load MCP servers"); } finally { setLoading(false); } }, [client]);
  useEffect(() => { void load(); }, [load]);
  async function save(server: MCPServer) { const { id, ...body } = server; await client.request(`/admin/v1/mcp/servers/${encodeURIComponent(id)}`, { method: "PUT", body }); setEditing(undefined); await load(); }
  async function remove(server: MCPServer) { if (!window.confirm(`Delete MCP server ${server.label}?`)) return; try { await client.request(`/admin/v1/mcp/servers/${encodeURIComponent(server.id)}`, { method: "DELETE" }); await load(); } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not delete MCP server"); } }
  const rows: Row[] = servers.map((server) => ({ id: server.id, label: server.label, server_url: server.server_url, transport: server.transport, provider_execution: server.allow_provider_execution ? "Allowed" : "Blocked", credential: server.credential_configured ? "Configured" : "None", connectors: server.tools || [], used_by: references[server.id]?.toolset_ids.length || 0, status: server.enabled ? "Enabled" : "Disabled", _server: server }));
  if (loading && !servers.length) return <LoadingState />;
  return <><PageHeader eyebrow="Tool governance" title="MCP servers" description="Approved HTTPS MCP endpoint metadata and connector-level policy grants." />{error && <ErrorState message={error} retry={() => void load()} />}
    <div className="usage-stats-grid"><StatCard label="Servers" value={servers.length} /><StatCard label="Enabled" value={servers.filter((server) => server.enabled).length} /><StatCard label="Connector grants" value={servers.reduce((sum, server) => sum + (server.tools?.length || 0), 0)} /><StatCard label="Toolset links" value={Object.values(references).reduce((sum, item) => sum + item.toolset_ids.length, 0)} /></div>
    <section className="notice-card mcp-boundary"><h2>Credential boundary</h2><p>Optional server bearer credentials are encrypted in durable state and are never returned to the browser. Native provider execution is blocked by default; when explicitly enabled, the configured URL and stored credential may be sent to the selected model provider. Client gateway credentials are never forwarded.</p></section>
    <ManagedDataTable rows={rows} columns={[{ key: "id", label: "Server" }, { key: "label", label: "Label" }, { key: "server_url", label: "HTTPS URL" }, { key: "transport", label: "Transport" }, { key: "provider_execution", label: "Native provider" }, { key: "credential", label: "Credential" }, { key: "connectors", label: "Connector grants" }, { key: "used_by", label: "Toolsets" }, { key: "status", label: "Status", render: (value) => <span className={`status ${value === "Enabled" ? "enabled" : "disabled"}`}>{String(value)}</span> }]} defaultHidden={["transport", "connectors"]} primaryAction={<button onClick={() => setEditing(null)}>Create MCP Server</button>} onRefresh={load} searchPlaceholder="Search MCP servers" actions={(row) => { const server = row._server as MCPServer; return <ActionsMenu label={`Actions for MCP server ${server.id}`} items={[{ label: "Edit", onSelect: () => setEditing(server) }, { label: "Delete", onSelect: () => remove(server), disabled: Number(row.used_by) > 0, tone: "danger" }]} />; }} />
    {editing !== undefined && <MCPServerForm initial={editing || undefined} onClose={() => setEditing(undefined)} onSave={save} />}
  </>;
}

export function MCPToolsetsPage() {
  const { client } = useAuth();
  const [servers, setServers] = useState<MCPServer[]>([]);
  const [toolsets, setToolsets] = useState<MCPToolset[]>([]);
  const [references, setReferences] = useState<Record<string, ToolsetReferences>>({});
  const [editing, setEditing] = useState<MCPToolset | null | undefined>();
  const [inspecting, setInspecting] = useState<MCPToolset>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const load = useCallback(async () => { setLoading(true); setError(""); try { const [serverPayload, toolsetPayload] = await Promise.all([client.request<ServerPayload>("/admin/v1/mcp/servers"), client.request<ToolsetPayload>("/admin/v1/mcp/toolsets?expand=references")]); setServers(serverPayload.data || []); setToolsets(toolsetPayload.data || []); setReferences(toolsetPayload.references || {}); } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load MCP toolsets"); } finally { setLoading(false); } }, [client]);
  useEffect(() => { void load(); }, [load]);
  const options = useMemo<ChipOption[]>(() => servers.flatMap((server) => (server.tools || []).map((tool) => ({ value: tool, label: server.label || server.id, description: `${server.id} · ${server.enabled ? "enabled" : "disabled"}` }))), [servers]);
  async function save(toolset: MCPToolset) { const { id, ...body } = toolset; await client.request(`/admin/v1/mcp/toolsets/${encodeURIComponent(id)}`, { method: "PUT", body }); setEditing(undefined); await load(); }
  async function remove(toolset: MCPToolset) { if (!window.confirm(`Delete MCP toolset ${toolset.name}?`)) return; try { await client.request(`/admin/v1/mcp/toolsets/${encodeURIComponent(toolset.id)}`, { method: "DELETE" }); setInspecting(undefined); await load(); } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not delete MCP toolset"); } }
  const rows: Row[] = toolsets.map((toolset) => { const reference = references[toolset.id] || { access_group_ids: [], virtual_key_ids: [] }; return { id: toolset.id, name: toolset.name, tools: toolset.tools, assigned: reference.access_group_ids.length + reference.virtual_key_ids.length, status: toolset.enabled ? "Enabled" : "Disabled", _toolset: toolset }; });
  const inspectedReferences = inspecting ? references[inspecting.id] || { access_group_ids: [], virtual_key_ids: [] } : undefined;
  if (loading && !toolsets.length) return <LoadingState />;
  return <><PageHeader eyebrow="Tool governance" title="MCP toolsets" description="Reusable connector permissions with live virtual-key and access-group impact." />{error && <ErrorState message={error} retry={() => void load()} />}
    <div className="usage-stats-grid"><StatCard label="Toolsets" value={toolsets.length} /><StatCard label="Enabled" value={toolsets.filter((toolset) => toolset.enabled).length} /><StatCard label="Configured grants" value={toolsets.reduce((sum, toolset) => sum + toolset.tools.length, 0)} /><StatCard label="Assignments" value={Object.values(references).reduce((sum, item) => sum + item.access_group_ids.length + item.virtual_key_ids.length, 0)} /></div>
    <section className="notice-card mcp-boundary"><h2>Fail-closed lifecycle</h2><p>Toolsets can only select grants declared by configured servers. Deletion is rejected while any virtual key or access group still references the toolset.</p></section>
    {inspecting && inspectedReferences && <section className="notice-card mcp-impact" role="status"><div><h2>{inspecting.name} assignments</h2><p><strong>Access groups:</strong> {inspectedReferences.access_group_ids.join(", ") || "None"}</p><p><strong>Virtual keys:</strong> {inspectedReferences.virtual_key_ids.join(", ") || "None"}</p></div><button className="secondary" onClick={() => setInspecting(undefined)}>Close</button></section>}
    <ManagedDataTable rows={rows} columns={[{ key: "id", label: "Toolset" }, { key: "name", label: "Name" }, { key: "tools", label: "Connector grants" }, { key: "assigned", label: "Assignments" }, { key: "status", label: "Status", render: (value) => <span className={`status ${value === "Enabled" ? "enabled" : "disabled"}`}>{String(value)}</span> }]} defaultHidden={["tools"]} primaryAction={<button onClick={() => setEditing(null)}>Create MCP Toolset</button>} onRefresh={load} searchPlaceholder="Search MCP toolsets" actions={(row) => { const toolset = row._toolset as MCPToolset; return <ActionsMenu label={`Actions for MCP toolset ${toolset.id}`} items={[{ label: "View assignments", onSelect: () => setInspecting(toolset) }, { label: "Edit", onSelect: () => setEditing(toolset) }, { label: "Delete", onSelect: () => remove(toolset), disabled: Number(row.assigned) > 0, tone: "danger" }]} />; }} />
    {editing !== undefined && <MCPToolsetForm initial={editing || undefined} options={options} onClose={() => setEditing(undefined)} onSave={save} />}
  </>;
}
