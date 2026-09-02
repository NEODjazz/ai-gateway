import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import { ActionsMenu } from "../components/ActionsMenu";
import { ErrorState, LoadingState } from "../components/AsyncState";
import type { Row } from "../components/DataTable";
import { ManagedDataTable } from "../components/ManagedDataTable";
import { PageHeader } from "../components/PageHeader";
import { ResourceForm, type Field } from "../components/ResourceForm";
import { StatCard } from "../components/StatCard";

type Project = { id: string; name: string; description?: string; team_id?: string; tags?: string[]; enabled: boolean };
type Team = { id: string; name?: string; status?: string };
type AccessGroup = { id: string; name: string; description?: string; project_id?: string; allowed_models?: string[]; allowed_tools?: string[]; tags?: string[]; enabled: boolean };
type VirtualKey = { id: string; alias?: string; status?: string; user_id?: string; team_id?: string; organization_id?: string; access_group_ids?: string[]; allowed_models?: string[] };
type KeyPage = { data?: VirtualKey[]; total?: number };

const projectFields: Field[] = [
  { key: "id", label: "Project ID", required: true, readOnlyOnEdit: true },
  { key: "name", label: "Name", required: true },
  { key: "description", label: "Description", type: "textarea" },
  { key: "team_id", label: "Owner team", type: "reference", placeholder: "No owner team", reference: { path: "/admin/v1/teams?limit=500", labelKeys: ["name", "status"] } },
  { key: "tags", label: "Tags", type: "csv", placeholder: "production, payments" },
  { key: "enabled", label: "Enabled", type: "boolean", defaultValue: true }
];

function records<T>(payload: unknown): T[] {
  if (Array.isArray(payload)) return payload as T[];
  const data = payload && typeof payload === "object" ? (payload as { data?: unknown }).data : undefined;
  return Array.isArray(data) ? data as T[] : [];
}

function ownerLabel(key: VirtualKey) {
  if (key.user_id) return `User · ${key.user_id}`;
  if (key.team_id) return `Team · ${key.team_id}`;
  if (key.organization_id) return `Organization · ${key.organization_id}`;
  return "Unassigned";
}

function ProjectForm({ initial, onClose, onSave }: { initial?: Project; onClose: () => void; onSave: (project: Project) => Promise<void> }) {
  const { client } = useAuth();
  return <ResourceForm title={initial ? "Edit Project" : "Create Project"} fields={projectFields} initial={initial as unknown as Row | undefined} loadOptions={(path) => client.request(path)} onClose={onClose} onSubmit={async (value) => onSave(value as Project)} />;
}

export function ProjectsPage() {
  const { client } = useAuth();
  const [projects, setProjects] = useState<Project[]>([]);
  const [teams, setTeams] = useState<Team[]>([]);
  const [groups, setGroups] = useState<AccessGroup[]>([]);
  const [editing, setEditing] = useState<Project | null | undefined>(undefined);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    setLoading(true); setError("");
    try {
      const [projectPayload, teamPayload, groupPayload] = await Promise.all([
        client.request("/admin/v1/projects"),
        client.request("/admin/v1/teams?limit=500"),
        client.request("/admin/v1/access-groups")
      ]);
      setProjects(records<Project>(projectPayload)); setTeams(records<Team>(teamPayload)); setGroups(records<AccessGroup>(groupPayload));
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load projects"); }
    finally { setLoading(false); }
  }, [client]);
  useEffect(() => { void load(); }, [load]);
  const teamByID = useMemo(() => new Map(teams.map((team) => [team.id, team])), [teams]);
  const rows: Row[] = projects.map((project) => ({
    ...project,
    team: project.team_id ? teamByID.get(project.team_id)?.name || project.team_id : "—",
    access_groups: groups.filter((group) => group.project_id === project.id).length,
    status: project.enabled ? "enabled" : "disabled",
    _project: project
  }));
  async function save(project: Project) {
    const id = project.id.trim();
    const { id: _id, ...payload } = project;
    await client.request(`/admin/v1/projects/${encodeURIComponent(id)}`, { method: "PUT", body: payload });
    setEditing(undefined); await load();
  }
  async function remove(project: Project) {
    if (!window.confirm(`Delete project ${project.name || project.id}?`)) return;
    try { await client.request(`/admin/v1/projects/${encodeURIComponent(project.id)}`, { method: "DELETE" }); await load(); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not delete project"); }
  }
  return <>
    <PageHeader eyebrow="Access control" title="Projects" description="Group access policies under an owner team and inspect the virtual-key impact before changing them." />
    {error && <ErrorState message={error} retry={() => void load()} />}
    {loading ? <LoadingState /> : <ManagedDataTable rows={rows} columns={[
      { key: "name", label: "Project" }, { key: "id", label: "ID" }, { key: "team", label: "Owner team" },
      { key: "access_groups", label: "Access groups" }, { key: "tags", label: "Tags" }, { key: "status", label: "Status" }
    ]} primaryAction={<button onClick={() => setEditing(null)}>Create Project</button>} onRefresh={load} searchPlaceholder="Search projects" actions={(row) => {
      const project = row._project as Project;
      return <ActionsMenu label={`Actions for project ${project.id}`} items={[
        { label: "Inspect", href: `/projects/${encodeURIComponent(project.id)}` },
        { label: "Edit", onSelect: () => setEditing(project) },
        { label: "Delete", tone: "danger", onSelect: () => void remove(project) }
      ]} />;
    }} />}
    {editing !== undefined && <ProjectForm initial={editing || undefined} onClose={() => setEditing(undefined)} onSave={save} />}
  </>;
}

export function ProjectDetailsPage() {
  const { id = "" } = useParams();
  const { client } = useAuth();
  const navigate = useNavigate();
  const [project, setProject] = useState<Project>();
  const [team, setTeam] = useState<Team>();
  const [groups, setGroups] = useState<AccessGroup[]>([]);
  const [keys, setKeys] = useState<VirtualKey[]>([]);
  const [keyInventoryTotal, setKeyInventoryTotal] = useState(0);
  const [editing, setEditing] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    setLoading(true); setError("");
    try {
      const [projectPayload, teamPayload, groupPayload] = await Promise.all([
        client.request<Project>(`/admin/v1/projects/${encodeURIComponent(id)}`),
        client.request("/admin/v1/teams?limit=500"),
        client.request("/admin/v1/access-groups")
      ]);
      const projectGroups = records<AccessGroup>(groupPayload).filter((group) => group.project_id === id);
      const groupIDs = new Set(projectGroups.map((group) => group.id));
      const query = new URLSearchParams({ limit: "500", status: "non_revoked", sort_by: "alias", sort_order: "asc" });
      for (const groupID of groupIDs) query.append("access_group_id", groupID);
      const keyPayload = groupIDs.size ? await client.request<KeyPage>(`/admin/v1/keys?${query.toString()}`) : { data: [], total: 0 };
      const inventory = records<VirtualKey>(keyPayload);
      setProject(projectPayload); setTeam(records<Team>(teamPayload).find((item) => item.id === projectPayload.team_id)); setGroups(projectGroups);
      setKeys(inventory.filter((key) => (key.access_group_ids || []).some((groupID) => groupIDs.has(groupID))));
      setKeyInventoryTotal(keyPayload.total || inventory.length);
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load project details"); }
    finally { setLoading(false); }
  }, [client, id]);
  useEffect(() => { void load(); }, [load]);
  const models = [...new Set(groups.flatMap((group) => group.allowed_models || []))].sort();
  const tools = [...new Set(groups.flatMap((group) => group.allowed_tools || []))].sort();
  const keyCountByGroup = new Map(groups.map((group) => [group.id, keys.filter((key) => key.access_group_ids?.includes(group.id)).length]));
  async function save(next: Project) {
    const { id: _id, ...payload } = next;
    await client.request(`/admin/v1/projects/${encodeURIComponent(id)}`, { method: "PUT", body: payload });
    setEditing(false); await load();
  }
  if (loading && !project) return <LoadingState />;
  if (error && !project) return <ErrorState message={error} retry={() => void load()} />;
  if (!project) return <ErrorState message="Project was not found or is outside your scope" retry={() => void load()} />;
  const inventoryBounded = keyInventoryTotal > 500;
  const groupRows: Row[] = groups.map((group) => ({ ...group, models: group.allowed_models || [], tools: group.allowed_tools || [], linked_keys: `${keyCountByGroup.get(group.id) || 0}${inventoryBounded ? "+" : ""}`, status: group.enabled ? "enabled" : "disabled" }));
  const keyRows: Row[] = keys.map((key) => ({ ...key, owner: ownerLabel(key), groups: key.access_group_ids || [], models: key.allowed_models || [] }));
  return <>
    <PageHeader eyebrow="Project workspace" title={project.name} description="Project ownership, effective access grants and the non-revoked virtual keys reached through its access groups." actions={<><button className="secondary" onClick={() => navigate("/projects")}>Back to projects</button><button onClick={() => setEditing(true)}>Edit Project</button></>} />
    {error && <ErrorState message={error} retry={() => void load()} />}
    <div className="usage-stats-grid"><StatCard label="Access groups" value={groups.length.toLocaleString()} /><StatCard label="Effective models" value={models.length.toLocaleString()} detail={models.length ? models.join(", ") : "No model grants"} /><StatCard label="Effective tools" value={tools.length.toLocaleString()} detail={tools.length ? tools.join(", ") : "No tool grants"} /><StatCard label="Linked keys" value={keyInventoryTotal.toLocaleString()} detail={inventoryBounded ? "exact total; first 500 rows shown" : "deduplicated non-revoked keys"} /></div>
    <section className="table-card access-group-details"><h2>Project details</h2><dl className="detail-grid"><div><dt>Project ID</dt><dd><code>{project.id}</code></dd></div><div><dt>Status</dt><dd><span className={`status ${project.enabled ? "enabled" : "disabled"}`}>{project.enabled ? "Enabled" : "Disabled"}</span></dd></div><div><dt>Owner team</dt><dd>{team ? <a href={`/teams/${encodeURIComponent(team.id)}`}>{team.name || team.id}</a> : project.team_id || "No owner team"}</dd></div><div><dt>Description</dt><dd>{project.description || "—"}</dd></div><div><dt>Tags</dt><dd><div className="tag-list">{(project.tags || []).map((tag) => <span className="tag" key={tag}>{tag}</span>)}</div></dd></div><div><dt>Runtime identity</dt><dd>Keys inherit grants through access groups; project ID is not sent to providers or billed as a separate identity.</dd></div></dl></section>
    <section className="section-block"><h2>Access groups</h2><p>Every enabled group contributes model and tool grants to virtual keys that reference it.</p><ManagedDataTable rows={groupRows} columns={[{ key: "name", label: "Group" }, { key: "id", label: "ID" }, { key: "models", label: "Models" }, { key: "tools", label: "Tools" }, { key: "linked_keys", label: "Linked keys" }, { key: "status", label: "Status" }]} onRefresh={load} searchPlaceholder="Search project access groups" actions={(row) => <ActionsMenu label={`Actions for access group ${row.id}`} items={[{ label: "Inspect", href: `/access-groups/${encodeURIComponent(String(row.id))}` }]} />} /></section>
    <section className="section-block"><h2>Virtual-key impact</h2><p>{inventoryBounded ? "The total is exact; this view shows the first 500 matching non-revoked keys sorted by alias. Per-group counts below are marked as lower bounds." : "Keys are deduplicated when they reference more than one access group in this project."}</p><ManagedDataTable rows={keyRows} columns={[{ key: "alias", label: "Alias" }, { key: "id", label: "Key" }, { key: "owner", label: "Owner" }, { key: "groups", label: "Access groups" }, { key: "models", label: "Key model grants" }, { key: "status", label: "Status" }]} onRefresh={load} searchPlaceholder="Search linked virtual keys" actions={(row) => <ActionsMenu label={`Actions for virtual key ${row.id}`} items={[{ label: "Inspect", href: `/api-keys/${encodeURIComponent(String(row.id))}` }]} />} /></section>
    {editing && <ProjectForm initial={project} onClose={() => setEditing(false)} onSave={save} />}
  </>;
}
