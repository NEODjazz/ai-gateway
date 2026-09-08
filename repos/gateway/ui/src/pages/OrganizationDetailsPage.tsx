import { ModalFrame } from "../components/ModalFrame";
import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import { ActionsMenu } from "../components/ActionsMenu";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { ManagedDataTable } from "../components/ManagedDataTable";
import { PageHeader } from "../components/PageHeader";
import { StatCard } from "../components/StatCard";
import type { Row } from "../components/DataTable";
import { formatTimestamp } from "../format";

type Organization = { id: string; name: string; description?: string; status: "active" | "disabled"; team_ids: string[]; created_at: string; updated_at: string };
type Team = { id: string; name: string; description?: string; status: string; member_count: number };
type VirtualKey = { id: string; alias?: string; status?: string; user_id?: string; team_id?: string; allowed_models?: string[] };

function records<T>(payload: unknown): T[] {
  const data = payload && typeof payload === "object" ? (payload as { data?: unknown }).data : undefined;
  return Array.isArray(data) ? data as T[] : [];
}

function TeamAssignmentForm({ teams, onClose, onSave }: { teams: Team[]; onClose: () => void; onSave: (teamID: string) => Promise<void> }) {
  const [teamID, setTeamID] = useState("");
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  async function submit(event: FormEvent) {
    event.preventDefault(); setSaving(true); setError("");
    try { await onSave(teamID); onClose(); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not assign team"); }
    finally { setSaving(false); }
  }
  return <ModalFrame label="Assign team" onClose={onClose}><section className="modal compact-modal"><div className="modal-heading"><h2>Assign configured team</h2><button className="icon-button" aria-label="Close team assignment" onClick={onClose}>×</button></div><form onSubmit={submit}><div className="form-grid"><label className="span-2">Team<select aria-label="Organization team" required value={teamID} onChange={(event) => setTeamID(event.target.value)}><option value="">Select unassigned team</option>{teams.map((team) => <option key={team.id} value={team.id}>{team.name} · {team.id}</option>)}</select></label></div><p className="muted">Teams already owned by another organization are excluded to prevent implicit re-parenting.</p>{error && <p className="form-error" role="alert">{error}</p>}<div className="modal-actions"><button type="button" className="secondary" onClick={onClose}>Cancel</button><button disabled={saving || !teamID}>{saving ? "Saving…" : "Assign team"}</button></div></form></section></ModalFrame>;
}

export function OrganizationDetailsPage() {
  const { id = "" } = useParams();
  const { client } = useAuth();
  const navigate = useNavigate();
  const [organizations, setOrganizations] = useState<Organization[]>([]);
  const [teams, setTeams] = useState<Team[]>([]);
  const [keys, setKeys] = useState<VirtualKey[]>([]);
  const [assigning, setAssigning] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    setLoading(true); setError("");
    try {
      const [organizationPayload, teamPayload, keyPayload] = await Promise.all([
        client.request("/admin/v1/organizations?limit=500"), client.request("/admin/v1/teams?limit=500"), client.request(`/admin/v1/keys?organization_id=${encodeURIComponent(id)}&limit=100`)
      ]);
      setOrganizations(records<Organization>(organizationPayload)); setTeams(records<Team>(teamPayload)); setKeys(records<VirtualKey>(keyPayload));
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load organization details"); }
    finally { setLoading(false); }
  }, [client, id]);
  useEffect(() => { void load(); }, [load]);
  const organization = organizations.find((item) => item.id === id);
  const teamsByID = useMemo(() => new Map(teams.map((team) => [team.id, team])), [teams]);
  const assignedElsewhere = useMemo(() => new Set(organizations.filter((item) => item.id !== id).flatMap((item) => item.team_ids || [])), [id, organizations]);
  const availableTeams = teams.filter((team) => team.status === "active" && !organization?.team_ids.includes(team.id) && !assignedElsewhere.has(team.id));
  const teamRows: Row[] = (organization?.team_ids || []).map((teamID) => { const team = teamsByID.get(teamID); return { id: teamID, name: team?.name || "Unavailable team", member_count: team?.member_count ?? "—", status: team?.status || "unavailable", description: team?.description || "—" }; });
  const keyRows: Row[] = keys.map((key) => ({ id: key.id, alias: key.alias || "—", owner: key.team_id ? `team ${key.team_id}` : key.user_id ? `user ${key.user_id}` : "organization", status: key.status || "active", allowed_models: key.allowed_models || [] }));
  async function assignTeam(teamID: string) { await client.request(`/admin/v1/organizations/${encodeURIComponent(id)}/teams/${encodeURIComponent(teamID)}`, { method: "PUT", body: {} }); await load(); }
  async function removeTeam(teamID: string) { if (!window.confirm(`Remove ${teamID} from ${organization?.name || id}?`)) return; await client.request(`/admin/v1/organizations/${encodeURIComponent(id)}/teams/${encodeURIComponent(teamID)}`, { method: "DELETE" }); await load(); }
  if (loading && !organization) return <LoadingState />;
  if (error && !organization) return <ErrorState message={error} retry={() => void load()} />;
  if (!organization) return <ErrorState message="Organization was not found" retry={() => void load()} />;
  return <><PageHeader eyebrow="Access control" title={organization.name} description="Configured team ownership and organization-scoped virtual keys." actions={<button className="secondary" onClick={() => navigate("/organizations")}>Back to organizations</button>} />{error && <ErrorState message={error} retry={() => void load()} />}<div className="usage-stats-grid"><StatCard label="Teams" value={organization.team_ids.length.toLocaleString()} /><StatCard label="Virtual keys" value={keys.length.toLocaleString()} detail="first 100 matching keys" /><StatCard label="Status" value={organization.status} /><StatCard label="Updated" value={formatTimestamp(organization.updated_at)} /></div><section className="table-card access-group-details"><h2>Organization details</h2><dl className="detail-grid"><div><dt>Organization ID</dt><dd>{organization.id}</dd></div><div><dt>Created</dt><dd>{formatTimestamp(organization.created_at)}</dd></div><div><dt>Description</dt><dd>{organization.description || "—"}</dd></div><div><dt>Ownership rule</dt><dd>Each team belongs to at most one organization</dd></div></dl></section><section className="section-block"><h2>Teams</h2><p>Assignment is explicit; re-parenting requires removing the existing organization assignment first.</p><ManagedDataTable rows={teamRows} columns={[{ key: "name", label: "Team" }, { key: "id", label: "Team ID" }, { key: "member_count", label: "Members" }, { key: "status", label: "Status" }, { key: "description", label: "Description" }]} primaryAction={<button disabled={!availableTeams.length} onClick={() => setAssigning(true)}>Assign team</button>} onRefresh={load} searchPlaceholder="Search organization teams" actions={(row) => <ActionsMenu label={`Actions for organization team ${row.id}`} items={[{ label: "Remove", tone: "danger", onSelect: () => void removeTeam(String(row.id)) }]} />} /></section><section className="section-block"><h2>Virtual keys</h2><p>Includes direct organization ownership and keys inherited through assigned team membership.</p><ManagedDataTable rows={keyRows} columns={[{ key: "id", label: "Key" }, { key: "alias", label: "Alias" }, { key: "owner", label: "Owner" }, { key: "status", label: "Status" }, { key: "allowed_models", label: "Models" }]} onRefresh={load} searchPlaceholder="Search organization keys" actions={(row) => <ActionsMenu label={`Actions for virtual key ${row.id}`} items={[{ label: "Inspect", href: `/api-keys/${encodeURIComponent(String(row.id))}` }]} />} /></section>{assigning && <TeamAssignmentForm teams={availableTeams} onClose={() => setAssigning(false)} onSave={assignTeam} />}</>;
}
