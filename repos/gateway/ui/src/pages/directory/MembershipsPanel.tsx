import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { useAuth } from "../../auth/AuthContext";
import { ActionsMenu } from "../../components/ActionsMenu";
import { ChipMultiSelect } from "../../components/ChipMultiSelect";
import { ErrorState, LoadingState } from "../../components/AsyncState";
import { ManagedDataTable } from "../../components/ManagedDataTable";
import { ModalFrame } from "../../components/ModalFrame";
import { formatTimestamp } from "../../format";
import { DirectoryPicker } from "./DirectoryPicker";
import { isDirectoryPage, useDirectoryResource, type DirectoryPage } from "./useDirectoryResource";
import type { DirectoryIdentity } from "./IdentityForm";

export type DirectoryMembership = { team_id: string; user_id: string; roles?: string[]; user_name: string; user_email: string; user_status: string; team_name: string; team_status: string; created_at: string; updated_at: string };

function MembershipEditor({ teamID, userID, initial, onClose, onSave }: { teamID?: string; userID?: string; initial?: DirectoryMembership; onClose: () => void; onSave: (team: string, user: string, roles: string[]) => Promise<void> }) {
  const [selected, setSelected] = useState<DirectoryIdentity>();
  const [roles, setRoles] = useState(initial ? initial.roles || [] : ["member"]);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const title = initial ? "Edit team member" : teamID ? "Add team member" : "Add user to team";
  const close = () => { if (!saving) onClose(); };
  async function submit(event: FormEvent) {
    event.preventDefault(); if (saving) return;
    setSaving(true); setError("");
    try { await onSave(initial?.team_id || teamID || selected?.id || "", initial?.user_id || userID || selected?.id || "", roles); onClose(); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not save membership"); }
    finally { setSaving(false); }
  }
  return <ModalFrame label={title} onClose={close} dismissDisabled={saving}><section className="modal directory-form"><div className="modal-heading"><h2>{title}</h2><button type="button" className="icon-button" aria-label="Close member form" disabled={saving} onClick={close}>×</button></div><form onSubmit={submit}><fieldset disabled={saving}>
    {initial ? <p>{initial.user_name || initial.user_id} · {initial.team_name || initial.team_id}</p> : <DirectoryPicker kind={teamID ? "users" : "teams"} label={teamID ? "Team member user" : "Membership team"} selected={selected} onChange={setSelected} />}
    <ChipMultiSelect label="Team roles" value={roles} options={[...new Set(["member", "developer", "team_admin", ...(initial?.roles || [])])].map((value) => ({ value, label: value === "team_admin" ? "Team admin" : value === "developer" ? "Developer" : value === "member" ? "Member" : value }))} onChange={setRoles} />
    <p className="muted">Membership roles are independent from approved global roles and inference grants.</p>
  </fieldset>{error && <p className="form-error" role="alert">{error}</p>}<div className="modal-actions"><button type="button" className="secondary" disabled={saving} onClick={close}>Cancel</button><button disabled={saving || (!initial && !selected)}>{saving ? "Saving…" : "Save membership"}</button></div></form></section></ModalFrame>;
}

export function MembershipsPanel({ teamID, userID, onChanged, allowCreate = true }: { teamID?: string; userID?: string; onChanged?: () => void; allowCreate?: boolean }) {
  const { client, session, hasCapability } = useAuth();
  const [search, setSearch] = useState(""); const [offset, setOffset] = useState(0); const [limit, setLimit] = useState(25);
  const [sort, setSort] = useState("name"); const [direction, setDirection] = useState<"asc" | "desc">("asc");
  const [editor, setEditor] = useState<{ scope: string; member: DirectoryMembership | null }>();
  const [mutation, setMutation] = useState<{ scope: string; error?: string; busy?: boolean }>();
  const byTeam = Boolean(teamID);
  const query = new URLSearchParams({ ...(teamID ? { team_id: teamID } : { user_id: userID || "" }), search: search.trim(), limit: String(limit), offset: String(offset), sort_by: sort === "joined" ? "joined" : byTeam ? "user" : "team", sort_order: direction });
  const verifiedTeam = teamID || (!hasCapability("admin") ? session?.team_id : undefined);
  const validate = useCallback((value: unknown) => isDirectoryPage(value) && (value as DirectoryPage<DirectoryMembership>).data.every((row) => typeof row.team_id === "string" && typeof row.user_id === "string" && Boolean(row.team_id && row.user_id) && (!verifiedTeam || row.team_id === verifiedTeam) && (!userID || row.user_id === userID)), [verifiedTeam, userID]);
  const data = useDirectoryResource<DirectoryPage<DirectoryMembership>>(`/admin/v1/team-memberships?${query}`, `members:${teamID || ""}:${userID || ""}`, validate);
  const activeScope = useRef(data.scope); activeScope.current = data.scope;
  const busy = mutation?.scope === data.scope && mutation.busy;
  useEffect(() => { if (!data.loading && data.value && data.value.offset > 0 && data.value.offset >= data.value.total) setOffset(data.value.total ? Math.floor((data.value.total - 1) / limit) * limit : 0); }, [data.loading, data.value, limit]);
  const reset = (update: () => void) => { setOffset(0); update(); };
  const rows = (data.value?.data || []).map((row) => ({ ...row, id: JSON.stringify([row.team_id, row.user_id]), name: byTeam ? row.user_name || row.user_email || row.user_id : row.team_name || row.team_id, email: row.user_email, status: byTeam ? row.user_status : row.team_status, joined: row.created_at }));
  async function save(team: string, user: string, roles: string[]) {
    const scope = data.scope;
    await client.request(`/admin/v1/teams/${encodeURIComponent(team)}/members${editor?.member ? `/${encodeURIComponent(user)}` : ""}`, { method: editor?.member ? "PUT" : "POST", body: editor?.member ? { roles } : { user_id: user, roles } });
    if (activeScope.current !== scope) return;
    await data.reload(); onChanged?.();
  }
  async function remove(row: DirectoryMembership) {
    if (mutation?.scope === data.scope && mutation.busy) return;
    if (!window.confirm(`Remove ${row.user_name || row.user_id} from ${row.team_name || row.team_id}?`)) return;
    const scope = data.scope; setMutation({ scope, busy: true });
    try { await client.request(`/admin/v1/teams/${encodeURIComponent(row.team_id)}/members/${encodeURIComponent(row.user_id)}`, { method: "DELETE" }); if (activeScope.current === scope) { if (data.value?.data.length === 1 && offset > 0) setOffset(Math.max(0, offset - limit)); else await data.reload(); onChanged?.(); } }
    catch (cause) { if (activeScope.current === scope) setMutation({ scope, error: cause instanceof Error ? cause.message : "Could not remove membership" }); }
    finally { if (activeScope.current === scope) setMutation((value) => ({ ...value, scope, busy: false })); }
  }
  return <section className="section-block"><h2>{byTeam ? "Members" : "Teams"}</h2><p>Roles belong to this membership. Search and paging cover every visible membership.</p>
    {data.error && <ErrorState message={data.error} retry={() => void data.reload()} />}{mutation?.scope === data.scope && mutation.error && <ErrorState message={mutation.error} />}
    {data.loading && !data.value ? <LoadingState /> : <><div aria-busy={data.loading}>{data.loading && <p role="status">Refreshing memberships…</p>}{data.previous && data.error && <p>Showing the previous successful page.</p>}
      <ManagedDataTable rows={rows} columns={[{ key: "name", label: byTeam ? "User" : "Team", render: (value, row) => <Link to={`/${byTeam ? "users" : "teams"}/${encodeURIComponent(String(byTeam ? row.user_id : row.team_id))}`}>{String(value)}</Link> }, ...(byTeam ? [{ key: "email", label: "Email" }] : []), { key: "roles", label: "Team roles" }, { key: "status", label: "Status" }, { key: "joined", label: "Joined", render: (value) => formatTimestamp(String(value)) }]}
        primaryAction={<button disabled={!allowCreate} title={allowCreate ? undefined : "Activate this identity before adding memberships"} onClick={() => setEditor({ scope: data.scope, member: null })}>{byTeam ? "Add member" : "Add team"}</button>} onRefresh={data.reload} searchPlaceholder={byTeam ? "Search team members" : "Search user teams"}
        server={{ search, onSearchChange: (value) => reset(() => setSearch(value)), total: data.value?.total || 0, offset: data.value?.offset || 0, pageSize: data.value?.limit || limit, onOffsetChange: setOffset, onPageSizeChange: (value) => reset(() => setLimit(value)), sort, direction, sortableKeys: ["name", "joined"], onSortChange: (key, order) => reset(() => { setSort(key); setDirection(order); }) }}
        actions={(row) => <ActionsMenu label={byTeam ? `Actions for member ${row.user_id}` : `Actions for team membership ${row.team_id}`} items={[{ label: "Edit roles", disabled: data.loading || busy, onSelect: () => setEditor({ scope: data.scope, member: row as DirectoryMembership }) }, { label: "Remove", tone: "danger", disabled: data.loading || busy, onSelect: () => remove(row as DirectoryMembership) }]} />} />
    </div></>}
    {editor?.scope === data.scope && (editor.member || allowCreate) && <MembershipEditor teamID={teamID} userID={userID} initial={editor.member || undefined} onClose={() => setEditor((current) => current === editor ? undefined : current)} onSave={save} />}
  </section>;
}
