import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import { APIError } from "../api/client";
import { ActionsMenu } from "../components/ActionsMenu";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { ChipMultiSelect, type ChipOption } from "../components/ChipMultiSelect";
import { ManagedDataTable } from "../components/ManagedDataTable";
import { PageHeader } from "../components/PageHeader";
import { StatCard } from "../components/StatCard";
import type { Row } from "../components/DataTable";
import { formatCost, formatTimestamp } from "../format";

type AccessGroup = Row & { id: string; name: string; description?: string; project_id?: string; allowed_models?: string[]; allowed_tools?: string[]; tags?: string[]; enabled: boolean };
type Project = { id: string; name: string; enabled: boolean };
type Model = { id?: string; model?: string };
type Toolset = { id: string; name: string; description?: string; tools?: string[]; enabled: boolean };
type VirtualKey = Row & { id: string; alias?: string; organization_id?: string; team_id?: string; user_id?: string; allowed_models?: string[]; revoked_at?: string; disabled_at?: string; expires_at?: string; created_at: string };
type BudgetPolicy = { currency: string; max_cost?: number; max_tokens?: number };
type BudgetSummary = { policy: BudgetPolicy; used_cost: number; used_tokens: number };
type KeyBudgetProjection = { policies: BudgetSummary[] };
type KeyPage = { data?: VirtualKey[]; total?: number; financials?: Record<string, KeyBudgetProjection> };
type GroupDraft = { id: string; name: string; description: string; project_id: string; allowed_models: string[]; allowed_tools: string[]; tags: string; enabled: boolean };

const emptyDraft: GroupDraft = { id: "", name: "", description: "", project_id: "", allowed_models: [], allowed_tools: [], tags: "", enabled: true };

function records<T>(payload: unknown): T[] {
  if (Array.isArray(payload)) return payload as T[];
  const data = payload && typeof payload === "object" ? (payload as { data?: unknown }).data : undefined;
  return Array.isArray(data) ? data as T[] : [];
}

function csv(value: string) {
  return [...new Set(value.split(",").map((item) => item.trim()).filter(Boolean))];
}

function keyStatus(key: VirtualKey) {
  if (key.revoked_at) return "revoked";
  if (key.disabled_at) return "disabled";
  if (key.expires_at && new Date(key.expires_at) <= new Date()) return "expired";
  return "active";
}

function keyOwner(key: VirtualKey) {
  if (key.user_id) return `User · ${key.user_id}`;
  if (key.team_id) return `Team · ${key.team_id}`;
  if (key.organization_id) return `Organization · ${key.organization_id}`;
  return "—";
}

function budgetSummary(projection?: KeyBudgetProjection) {
  if (!projection?.policies.length) return "No budget policy";
  return projection.policies.map(({ policy, used_cost, used_tokens }) => {
    const values: string[] = [];
    if (policy.max_cost !== undefined) values.push(`${formatCost(used_cost, policy.currency)} / ${formatCost(policy.max_cost, policy.currency)}`);
    if (policy.max_tokens !== undefined) values.push(`${used_tokens.toLocaleString()} / ${policy.max_tokens.toLocaleString()} tokens`);
    return values.join(" · ") || "Policy without a finite cap";
  }).join("; ");
}

function GroupForm({ initial, projects, modelOptions, toolOptions, referencedKeys, onClose, onSave }: { initial?: AccessGroup; projects: Project[]; modelOptions: ChipOption[]; toolOptions: ChipOption[]; referencedKeys: number; onClose: () => void; onSave: (draft: GroupDraft) => Promise<void> }) {
  const [draft, setDraft] = useState<GroupDraft>(initial ? { id: initial.id, name: initial.name, description: initial.description || "", project_id: initial.project_id || "", allowed_models: [...(initial.allowed_models || [])], allowed_tools: [...(initial.allowed_tools || [])], tags: (initial.tags || []).join(", "), enabled: initial.enabled } : { ...emptyDraft, allowed_models: [], allowed_tools: [] });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  async function submit(event: FormEvent) {
    event.preventDefault(); setError("");
    if (!draft.id.trim() || !draft.name.trim()) { setError("ID and name are required."); return; }
    setSaving(true);
    try { await onSave({ ...draft, id: draft.id.trim(), name: draft.name.trim(), description: draft.description.trim(), tags: csv(draft.tags).join(",") }); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not save access group"); }
    finally { setSaving(false); }
  }
  return <div className="modal-backdrop" role="presentation"><form className="modal key-form-modal" role="dialog" aria-modal="true" aria-label={initial ? "Edit access group" : "Create access group"} onSubmit={submit}>
    <div className="modal-heading"><div><h2>{initial ? "Edit" : "Create"} Access Group</h2><span className="muted">Reusable model and tool permissions evaluated on every request.</span></div><button type="button" className="icon-button" aria-label="Close access group form" onClick={onClose}>×</button></div>
    {initial && referencedKeys > 0 && <p className="access-impact-warning" role="status">Changes apply immediately to {referencedKeys} non-revoked virtual {referencedKeys === 1 ? "key" : "keys"}. Disabling this group will block their requests.</p>}
    <div className="key-form">
      <label><span>ID</span><input aria-label="Access group ID" required disabled={Boolean(initial)} value={draft.id} onChange={(event) => setDraft({ ...draft, id: event.target.value })} /></label>
      <label><span>Name</span><input aria-label="Access group name" required value={draft.name} onChange={(event) => setDraft({ ...draft, name: event.target.value })} /></label>
      <label><span>Description</span><textarea aria-label="Access group description" rows={3} value={draft.description} onChange={(event) => setDraft({ ...draft, description: event.target.value })} /></label>
      <label><span>Project</span><select aria-label="Access group project" value={draft.project_id} onChange={(event) => setDraft({ ...draft, project_id: event.target.value })}><option value="">No project</option>{projects.filter((project) => project.enabled || project.id === draft.project_id).map((project) => <option value={project.id} key={project.id}>{project.name || project.id} · {project.id}</option>)}</select></label>
      <ChipMultiSelect label="Models" options={modelOptions} value={draft.allowed_models} onChange={(allowed_models) => setDraft({ ...draft, allowed_models })} allowCustom />
      <ChipMultiSelect label="Tools" options={toolOptions} value={draft.allowed_tools} onChange={(allowed_tools) => setDraft({ ...draft, allowed_tools })} allowCustom />
      <label><span>Tags</span><input aria-label="Access group tags" placeholder="production, regulated" value={draft.tags} onChange={(event) => setDraft({ ...draft, tags: event.target.value })} /></label>
      <label className="checkbox-line access-group-enabled"><input aria-label="Access group enabled" type="checkbox" checked={draft.enabled} onChange={(event) => setDraft({ ...draft, enabled: event.target.checked })} /> Enabled</label>
    </div>
    {error && <p className="form-error key-form-error" role="alert">{error}</p>}<div className="modal-actions"><button type="button" className="secondary" onClick={onClose}>Cancel</button><button disabled={saving}>{saving ? "Saving…" : initial ? "Save changes" : "Create access group"}</button></div>
  </form></div>;
}

function AccessGroupDetail({ id, projects, modelOptions, toolOptions }: { id: string; projects: Project[]; modelOptions: ChipOption[]; toolOptions: ChipOption[] }) {
  const { client } = useAuth();
  const navigate = useNavigate();
  const [group, setGroup] = useState<AccessGroup>();
  const [keys, setKeys] = useState<VirtualKey[]>([]);
  const [financials, setFinancials] = useState<Record<string, KeyBudgetProjection>>({});
  const [total, setTotal] = useState(0);
  const [referenced, setReferenced] = useState(0);
  const [offset, setOffset] = useState(0);
  const [pageSize, setPageSize] = useState(25);
  const [search, setSearch] = useState("");
  const [sort, setSort] = useState("created");
  const [direction, setDirection] = useState<"asc" | "desc">("desc");
  const [editing, setEditing] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    setLoading(true); setError("");
    try {
      const query = new URLSearchParams({ limit: String(pageSize), offset: String(offset), access_group_id: id, sort_by: sort, sort_order: direction, expand: "financials" });
      if (search.trim()) query.set("search", search.trim());
      const liveQuery = new URLSearchParams({ limit: "1", access_group_id: id, status: "non_revoked" });
      const keyRequest = client.request<KeyPage>(`/admin/v1/keys?${query}`).catch((cause) => {
        if (!(cause instanceof APIError) || cause.code !== "budget_unavailable") throw cause;
        query.delete("expand");
        return client.request<KeyPage>(`/admin/v1/keys?${query}`);
      });
      const [groupPayload, keyPayload, livePayload] = await Promise.all([client.request<AccessGroup>(`/admin/v1/access-groups/${encodeURIComponent(id)}`), keyRequest, client.request<KeyPage>(`/admin/v1/keys?${liveQuery}`)]);
      setGroup(groupPayload); setKeys(keyPayload.data || []); setTotal(keyPayload.total || 0); setFinancials(keyPayload.financials || {}); setReferenced(livePayload.total || 0);
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load access group"); }
    finally { setLoading(false); }
  }, [client, direction, id, offset, pageSize, search, sort]);
  useEffect(() => { const timer = window.setTimeout(() => { void load(); }, search ? 250 : 0); return () => window.clearTimeout(timer); }, [load, search]);

  async function save(draft: GroupDraft) {
    await client.request(`/admin/v1/access-groups/${encodeURIComponent(id)}`, { method: "PUT", body: { name: draft.name, description: draft.description, project_id: draft.project_id, allowed_models: draft.allowed_models, allowed_tools: draft.allowed_tools, tags: csv(draft.tags), enabled: draft.enabled } });
    setEditing(false); await load();
  }
  async function remove() {
    if (!group || referenced > 0 || !window.confirm(`Delete access group ${group.name}?`)) return;
    try { await client.request(`/admin/v1/access-groups/${encodeURIComponent(id)}`, { method: "DELETE" }); navigate("/access-groups"); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not delete access group"); }
  }
  if (loading && !group) return <LoadingState />;
  if (error && !group) return <ErrorState message={error} retry={() => void load()} />;
  if (!group) return <ErrorState message="Access group was not found" />;
  const project = projects.find((item) => item.id === group.project_id);
  const rows = keys.map((key) => ({ ...key, key: key.id, owner: keyOwner(key), status: keyStatus(key), budget: budgetSummary(financials[key.id]), created: formatTimestamp(key.created_at) }));
  const columns = [
    { key: "key", label: "Key", render: (value: unknown) => <button className="text-button" onClick={() => navigate(`/api-keys?key_id=${encodeURIComponent(String(value))}`)}>{String(value)}</button> },
    { key: "alias", label: "Alias" }, { key: "owner", label: "Owner" },
    { key: "status", label: "Status", render: (value: unknown) => <span className={`status ${value === "active" ? "enabled" : "disabled"}`}>{String(value)}</span> },
    { key: "allowed_models", label: "Direct model grants" }, { key: "budget", label: "Budget exposure" },
    { key: "created", label: "Created" }
  ];
  return <><PageHeader eyebrow="Access control" title={group.name} description={`Access Group · ${group.id}`} actions={<><button className="secondary" onClick={() => navigate("/access-groups")}>← Back</button><button className="secondary" onClick={() => setEditing(true)}>Edit</button><button className="danger-button" disabled={referenced > 0} title={referenced > 0 ? "Remove this group from every non-revoked key first" : "Delete access group"} onClick={() => void remove()}>Delete</button></>} />
    {error && <ErrorState message={error} retry={() => void load()} />}
    <div className="usage-stats-grid"><StatCard label="Assigned keys" value={total} /><StatCard label="Non-revoked keys" value={referenced} /><StatCard label="Model grants" value={group.allowed_models?.length || 0} /><StatCard label="Tool grants" value={group.allowed_tools?.length || 0} /></div>
    {referenced > 0 && <section className="notice-card access-impact-card"><h2>Immediate policy impact</h2><p>Updates affect {referenced} non-revoked virtual {referenced === 1 ? "key" : "keys"} immediately. Deletion is protected until those assignments are removed or the keys are revoked.</p></section>}
    <section className="table-card access-group-details"><h2>Group details</h2><dl className="detail-grid"><div><dt>Status</dt><dd><span className={`status ${group.enabled ? "enabled" : "disabled"}`}>{group.enabled ? "Enabled" : "Disabled"}</span></dd></div><div><dt>Project</dt><dd>{project ? `${project.name} · ${project.id}` : group.project_id || "No project"}</dd></div><div><dt>Description</dt><dd>{group.description || "—"}</dd></div><div><dt>Tags</dt><dd><div className="tag-list">{(group.tags || []).map((tag) => <span className="tag" key={tag}>{tag}</span>)}</div></dd></div></dl></section>
    <section className="section-block"><h2>Effective resource grants</h2><p>These grants are unioned with grants from other assigned groups, then intersected with each key’s direct permissions.</p><div className="split-grid"><div className="notice-card"><h2>Models</h2><div className="tag-list">{(group.allowed_models || []).map((model) => <span className="tag" key={model}>{model}</span>)}{!group.allowed_models?.length && <span className="muted">No models allowed</span>}</div></div><div className="notice-card"><h2>Tools</h2><div className="tag-list">{(group.allowed_tools || []).map((tool) => <span className="tag" key={tool}>{tool}</span>)}{!group.allowed_tools?.length && <span className="muted">No tools allowed</span>}</div></div></div></section>
    <section className="section-block"><h2>Attached virtual keys</h2><p>Safe metadata and existing key-budget projections. Plaintext tokens and provider credentials are never loaded.</p><ManagedDataTable rows={rows} columns={columns} onRefresh={load} searchPlaceholder="Search attached keys by alias" defaultHidden={["allowed_models"]} server={{ search, onSearchChange: (value) => { setOffset(0); setSearch(value); }, total, offset, pageSize, onPageSizeChange: (value) => { setOffset(0); setPageSize(value); }, onOffsetChange: setOffset, sort, direction, onSortChange: (key, nextDirection) => { setOffset(0); setSort(key); setDirection(nextDirection); }, sortableKeys: ["key", "alias", "status", "created"] }} /></section>
    {editing && <GroupForm initial={group} projects={projects} modelOptions={modelOptions} toolOptions={toolOptions} referencedKeys={referenced} onClose={() => setEditing(false)} onSave={save} />}
  </>;
}

export function AccessGroupsPage() {
  const { id } = useParams();
  const { client } = useAuth();
  const navigate = useNavigate();
  const [groups, setGroups] = useState<AccessGroup[]>([]);
  const [projects, setProjects] = useState<Project[]>([]);
  const [models, setModels] = useState<string[]>([]);
  const [toolsets, setToolsets] = useState<Toolset[]>([]);
  const [editing, setEditing] = useState<AccessGroup | null | undefined>(undefined);
  const [editingReferences, setEditingReferences] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    setLoading(true); setError("");
    try {
      const [groupPayload, projectPayload, modelPayload, toolsetPayload] = await Promise.all([client.request("/admin/v1/access-groups"), client.request("/admin/v1/projects"), client.request("/v1/models"), client.request("/admin/v1/mcp/toolsets")]);
      setGroups(records<AccessGroup>(groupPayload)); setProjects(records<Project>(projectPayload)); setToolsets(records<Toolset>(toolsetPayload)); setModels([...new Set(records<Model>(modelPayload).map((model) => model.id || model.model || "").filter(Boolean))].sort());
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load access groups"); }
    finally { setLoading(false); }
  }, [client]);
  useEffect(() => { void load(); }, [load]);
  const modelOptions = useMemo(() => models.map((model) => ({ value: model, label: model })), [models]);
  const toolOptions = useMemo(() => {
    const options: ChipOption[] = [];
    for (const toolset of toolsets.filter((item) => item.enabled)) {
      options.push({ value: `toolset:${toolset.id}`, label: `${toolset.name || toolset.id} toolset`, description: toolset.description || `${toolset.tools?.length || 0} tools` });
      for (const tool of toolset.tools || []) options.push({ value: tool, label: tool, description: `From ${toolset.name || toolset.id}` });
    }
    return [...new Map(options.map((option) => [option.value, option])).values()];
  }, [toolsets]);
  if (id) return <AccessGroupDetail id={id} projects={projects} modelOptions={modelOptions} toolOptions={toolOptions} />;
  async function save(draft: GroupDraft) {
    await client.request(`/admin/v1/access-groups/${encodeURIComponent(draft.id)}`, { method: "PUT", body: { name: draft.name, description: draft.description, project_id: draft.project_id, allowed_models: draft.allowed_models, allowed_tools: draft.allowed_tools, tags: csv(draft.tags), enabled: draft.enabled } });
    setEditing(undefined); await load(); navigate(`/access-groups/${encodeURIComponent(draft.id)}`);
  }
  async function edit(group: AccessGroup) {
    try {
      const query = new URLSearchParams({ limit: "1", access_group_id: group.id, status: "non_revoked" });
      const page = await client.request<KeyPage>(`/admin/v1/keys?${query}`);
      setEditingReferences(page.total || 0); setEditing(group);
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not inspect access-group impact"); }
  }
  async function remove(group: AccessGroup) {
    if (!window.confirm(`Delete access group ${group.name}? The gateway will reject deletion when non-revoked keys still reference it.`)) return;
    try { await client.request(`/admin/v1/access-groups/${encodeURIComponent(group.id)}`, { method: "DELETE" }); await load(); }
    catch (cause) { setError(cause instanceof APIError && cause.code === "access_group_in_use" ? "This access group is still assigned to non-revoked virtual keys. Open its details to review the impact." : cause instanceof Error ? cause.message : "Could not delete access group"); }
  }
  const projectsByID = new Map(projects.map((project) => [project.id, project]));
  const rows = groups.map((group) => ({ ...group, models: group.allowed_models || [], tools: group.allowed_tools || [], project: group.project_id ? `${projectsByID.get(group.project_id)?.name || group.project_id}` : "—" }));
  const columns = [
    { key: "name", label: "Access group", render: (value: unknown, row: Row) => <button className="text-button" onClick={() => navigate(`/access-groups/${encodeURIComponent(String(row.id))}`)}>{String(value)}</button> },
    { key: "id", label: "ID", render: (value: unknown) => <code>{String(value)}</code> }, { key: "project", label: "Project" },
    { key: "models", label: "Models" }, { key: "tools", label: "Tools" }, { key: "tags", label: "Tags" },
    { key: "enabled", label: "Status" }
  ];
  return <><PageHeader eyebrow="Access control" title="Access groups" description="Manage reusable model and tool permissions, inspect attached keys, and understand runtime impact before changing policy." />
    {error && <ErrorState message={error} retry={() => void load()} />}
    {loading ? <LoadingState /> : <ManagedDataTable rows={rows} columns={columns} primaryAction={<button onClick={() => { setEditingReferences(0); setEditing(null); }}>Create Access Group</button>} onRefresh={load} searchPlaceholder="Search groups by name, ID, or description" defaultHidden={["tags"]} actions={(row) => { const group = groups.find((item) => item.id === row.id)!; return <ActionsMenu label={`Actions for ${group.name}`} items={[{ label: "View details", onSelect: () => navigate(`/access-groups/${encodeURIComponent(group.id)}`) }, { label: "Edit", onSelect: () => void edit(group) }, { label: "Delete", tone: "danger", onSelect: () => void remove(group) }]} />; }} />}
    {editing !== undefined && <GroupForm initial={editing || undefined} projects={projects} modelOptions={modelOptions} toolOptions={toolOptions} referencedKeys={editingReferences} onClose={() => setEditing(undefined)} onSave={save} />}
  </>;
}
