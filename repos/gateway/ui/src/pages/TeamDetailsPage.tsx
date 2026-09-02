import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import { ActionsMenu } from "../components/ActionsMenu";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { ChipMultiSelect } from "../components/ChipMultiSelect";
import { ManagedDataTable } from "../components/ManagedDataTable";
import { PageHeader } from "../components/PageHeader";
import { StatCard } from "../components/StatCard";
import type { Row } from "../components/DataTable";
import { formatTimestamp } from "../format";

type Team = { id: string; name: string; description?: string; status: "active" | "disabled"; member_count: number; created_at: string; updated_at: string };
type User = { id: string; name?: string; email?: string; status: string; team_ids?: string[] };
type Membership = { team_id: string; user_id: string; roles?: string[]; created_at: string; updated_at: string };
type VirtualKey = { id: string; alias?: string; status?: string; allowed_models?: string[]; budget_id?: string };

function records<T>(payload: unknown): T[] {
  const data = payload && typeof payload === "object" ? (payload as { data?: unknown }).data : undefined;
  return Array.isArray(data) ? data as T[] : [];
}

function MembershipForm({ initial, users, onClose, onSave }: { initial?: Membership; users: User[]; onClose: () => void; onSave: (userID: string, roles: string[]) => Promise<void> }) {
  const [userID, setUserID] = useState(initial?.user_id || "");
  const [roles, setRoles] = useState(initial?.roles || []);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  async function submit(event: FormEvent) {
    event.preventDefault(); setSaving(true); setError("");
    try { await onSave(userID, roles); onClose(); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not save membership"); }
    finally { setSaving(false); }
  }
  return <div className="modal-backdrop" role="presentation"><section className="modal compact-modal" role="dialog" aria-modal="true" aria-label={initial ? "Edit team member" : "Add team member"}><div className="modal-heading"><h2>{initial ? "Edit team member" : "Add team member"}</h2><button className="icon-button" aria-label="Close member form" onClick={onClose}>×</button></div><form onSubmit={submit}><div className="form-grid"><label className="span-2">User<select aria-label="Team member user" required disabled={Boolean(initial)} value={userID} onChange={(event) => setUserID(event.target.value)}><option value="">Select configured user</option>{users.map((user) => <option key={user.id} value={user.id}>{user.name || user.email || user.id} · {user.id}</option>)}</select></label><div className="span-2"><ChipMultiSelect label="Team roles" options={[{ value: "member", label: "Member" }, { value: "developer", label: "Developer" }, { value: "team_admin", label: "Team admin" }]} value={roles} onChange={setRoles} allowCustom /></div></div>{error && <p className="form-error" role="alert">{error}</p>}<div className="modal-actions"><button type="button" className="secondary" onClick={onClose}>Cancel</button><button disabled={saving || !userID}>{saving ? "Saving…" : "Save membership"}</button></div></form></section></div>;
}

export function TeamDetailsPage() {
  const { id = "" } = useParams();
  const { client, session } = useAuth();
  const navigate = useNavigate();
  const isAdmin = Boolean(session?.roles.includes("admin"));
  const [team, setTeam] = useState<Team>();
  const [memberships, setMemberships] = useState<Membership[]>([]);
  const [users, setUsers] = useState<User[]>([]);
  const [keys, setKeys] = useState<VirtualKey[]>([]);
  const [editing, setEditing] = useState<Membership | null | undefined>(undefined);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    setLoading(true); setError("");
    try {
      const [teamPayload, membershipPayload, userPayload, keyPayload] = await Promise.all([
        client.request(`/admin/v1/teams?team_id=${encodeURIComponent(id)}&limit=1`),
        client.request(`/admin/v1/teams/${encodeURIComponent(id)}/members?limit=500`),
        client.request("/admin/v1/users?limit=500"),
        isAdmin ? client.request(`/admin/v1/keys?team_id=${encodeURIComponent(id)}&limit=100`) : Promise.resolve({ data: [] })
      ]);
      setTeam(records<Team>(teamPayload)[0]); setMemberships(records<Membership>(membershipPayload)); setUsers(records<User>(userPayload)); setKeys(records<VirtualKey>(keyPayload));
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load team details"); }
    finally { setLoading(false); }
  }, [client, id, isAdmin]);
  useEffect(() => { void load(); }, [load]);
  const usersByID = useMemo(() => new Map(users.map((user) => [user.id, user])), [users]);
  const memberRows: Row[] = memberships.map((membership) => { const user = usersByID.get(membership.user_id); return { id: membership.user_id, user: user?.name || membership.user_id, email: user?.email || "—", roles: membership.roles || [], status: user?.status || "unknown", joined: formatTimestamp(membership.created_at), _membership: membership }; });
  const keyRows: Row[] = keys.map((key) => ({ id: key.id, alias: key.alias || "—", status: key.status || "active", allowed_models: key.allowed_models || [], budget: key.budget_id || "—" }));
  const availableUsers = users.filter((user) => !memberships.some((membership) => membership.user_id === user.id) && user.status === "active");
  async function saveMembership(userID: string, roles: string[]) { await client.request(`/admin/v1/teams/${encodeURIComponent(id)}/members/${encodeURIComponent(userID)}`, { method: "PUT", body: { roles } }); await load(); }
  async function removeMembership(userID: string) { if (!window.confirm(`Remove ${userID} from ${team?.name || id}?`)) return; await client.request(`/admin/v1/teams/${encodeURIComponent(id)}/members/${encodeURIComponent(userID)}`, { method: "DELETE" }); await load(); }
  if (loading && !team) return <LoadingState />;
  if (error && !team) return <ErrorState message={error} retry={() => void load()} />;
  if (!team) return <ErrorState message="Team was not found or is outside your scope" retry={() => void load()} />;
  return <><PageHeader eyebrow="Access control" title={team.name} description="Membership roles and team-owned virtual keys in one scoped workspace." actions={<button className="secondary" onClick={() => navigate("/teams")}>Back to teams</button>} />{error && <ErrorState message={error} retry={() => void load()} />}<div className="usage-stats-grid"><StatCard label="Members" value={memberships.length.toLocaleString()} /><StatCard label="Virtual keys" value={isAdmin ? keys.length.toLocaleString() : "Scoped"} detail={isAdmin ? "first 100 matching keys" : "admin-only inventory"} /><StatCard label="Status" value={team.status} /><StatCard label="Updated" value={formatTimestamp(team.updated_at)} /></div><section className="table-card access-group-details"><h2>Team details</h2><dl className="detail-grid"><div><dt>Team ID</dt><dd>{team.id}</dd></div><div><dt>Created</dt><dd>{formatTimestamp(team.created_at)}</dd></div><div><dt>Description</dt><dd>{team.description || "—"}</dd></div><div><dt>Access boundary</dt><dd>{isAdmin ? "Global administrator" : "Matching team administrator"}</dd></div></dl></section><section className="section-block"><h2>Members</h2><p>Roles are attached to this team membership, independently from directory-wide user roles.</p><ManagedDataTable rows={memberRows} columns={[{ key: "user", label: "User" }, { key: "email", label: "Email" }, { key: "roles", label: "Team roles" }, { key: "status", label: "Status" }, { key: "joined", label: "Joined" }]} primaryAction={<button disabled={!availableUsers.length} onClick={() => setEditing(null)}>Add member</button>} onRefresh={load} searchPlaceholder="Search team members" actions={(row) => <ActionsMenu label={`Actions for member ${row.id}`} items={[{ label: "Edit roles", onSelect: () => setEditing(row._membership as Membership) }, { label: "Remove", tone: "danger", onSelect: () => void removeMembership(String(row.id)) }]} />} /></section><section className="section-block"><h2>Virtual keys</h2><p>{isAdmin ? "Keys directly owned by this team or inherited through membership-aware ownership." : "Virtual-key inventory is restricted to global administrators."}</p>{isAdmin ? <ManagedDataTable rows={keyRows} columns={[{ key: "id", label: "Key" }, { key: "alias", label: "Alias" }, { key: "status", label: "Status" }, { key: "allowed_models", label: "Models" }, { key: "budget", label: "Budget" }]} onRefresh={load} searchPlaceholder="Search team keys" /> : <div className="empty-state">No global key inventory in this scoped session.</div>}</section>{editing !== undefined && <MembershipForm initial={editing || undefined} users={editing ? users : availableUsers} onClose={() => setEditing(undefined)} onSave={saveMembership} />}</>;
}
