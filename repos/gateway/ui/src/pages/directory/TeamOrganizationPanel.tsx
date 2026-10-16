import { useCallback, useRef, useState, type FormEvent } from "react";
import { useAuth } from "../../auth/AuthContext";
import { ErrorState, LoadingState } from "../../components/AsyncState";
import { ModalFrame } from "../../components/ModalFrame";
import { OrganizationPicker, isOrganization, type OrganizationMetadata } from "./OrganizationPicker";
import { useDirectoryResource } from "./useDirectoryResource";

type TeamOrganization = { team_id: string; organization: OrganizationMetadata | null };
function AssignOrganizationForm({ onClose, onSave }: { onClose: () => void; onSave: (organization: OrganizationMetadata) => Promise<void> }) {
  const [selected, setSelected] = useState<OrganizationMetadata>(); const [saving, setSaving] = useState(false); const [error, setError] = useState("");
  async function submit(event: FormEvent) { event.preventDefault(); if (!selected || saving) return; setSaving(true); setError(""); try { await onSave(selected); } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not assign organization"); } finally { setSaving(false); } }
  return <ModalFrame label="Assign team organization" onClose={onClose} dismissDisabled={saving}><section className="modal directory-form"><div className="modal-heading"><h2>Assign team organization</h2><button className="icon-button" aria-label="Close organization assignment" disabled={saving} onClick={onClose}>×</button></div><form onSubmit={submit}><fieldset disabled={saving}><OrganizationPicker selected={selected} onChange={setSelected} /></fieldset><p>This sets directory ownership. Credentials and principal bindings keep their explicit organization scope; they are not migrated or implicitly granted new access.</p>{error && <p className="form-error" role="alert">{error}</p>}<div className="modal-actions"><button type="button" className="secondary" disabled={saving} onClick={onClose}>Cancel</button><button disabled={saving || !selected}>{saving ? "Saving…" : "Assign organization"}</button></div></form></section></ModalFrame>;
}

export function TeamOrganizationPanel({ id, readOnly = false }: { id: string; readOnly?: boolean }) {
  const { client, hasCapability } = useAuth(); const admin = hasCapability("admin");
  const path = `/admin/v1/teams/${encodeURIComponent(id)}/organization`;
  const validate = useCallback((value: unknown) => { const result = value as TeamOrganization | undefined; return Boolean(result && result.team_id === id && (result.organization === null || isOrganization(result.organization))); }, [id]);
  const data = useDirectoryResource<TeamOrganization>(path, path, validate);
  const activeScope = useRef(data.scope); activeScope.current = data.scope;
  const [editor, setEditor] = useState<{ scope: string }>(); const [mutation, setMutation] = useState<{ scope: string; busy?: boolean; error?: string; notice?: string }>();
  const current = data.value; const busy = mutation?.scope === data.scope && mutation.busy;
  async function assign(organization: OrganizationMetadata) {
    const scope = data.scope;
    await client.request(path, { method: "PUT", body: { organization_id: organization.id } });
    if (activeScope.current !== scope) return;
    setEditor((value) => value === editor ? undefined : value); setMutation({ scope, notice: "Organization assignment saved." }); await data.reload();
  }
  async function unassign() {
    if (!current?.organization || busy || !window.confirm(`Unassign ${id} from ${current.organization.name}? Credentials and principal bindings pinned to this organization may stop working. Directory ownership is removed without rewriting credentials or principal bindings.`)) return;
    const scope = data.scope; setMutation({ scope, busy: true });
    try { await client.request(path, { method: "DELETE", body: { organization_id: current.organization.id } }); if (activeScope.current === scope) { setMutation({ scope, notice: "Organization assignment removed." }); await data.reload(); } }
    catch (cause) { if (activeScope.current === scope) setMutation({ scope, error: cause instanceof Error ? cause.message : "Could not remove organization assignment" }); }
    finally { if (activeScope.current === scope) setMutation((value) => ({ ...value, scope, busy: false })); }
  }
  return <section className="section-block" aria-label="Team organization"><h2>Organization</h2><p>A team has at most one organization owner. Moving it requires an explicit unassignment before choosing a new organization.</p>
    {data.error && <ErrorState message={data.error} retry={() => void data.reload()} />}{data.error && current && <p>Showing the last successful ownership read. Refresh before changing this assignment.</p>}
    {mutation?.scope === data.scope && mutation.error && <ErrorState message={mutation.error} />}{mutation?.scope === data.scope && mutation.notice && <p role="status">{mutation.notice}</p>}
    {data.loading && !current ? <LoadingState /> : current && <div aria-busy={data.loading}>{data.loading && <p role="status">Refreshing organization ownership…</p>}
      {current.organization ? <dl className="detail-grid"><div><dt>Organization</dt><dd>{current.organization.name}</dd></div><div><dt>Organization ID</dt><dd>{current.organization.id}</dd></div><div><dt>Status</dt><dd>{current.organization.status}</dd></div></dl> : <p>No organization is assigned to this team.</p>}
      {!admin && <p>Only global administrators can change organization ownership.</p>}
      <div className="inline-actions">{admin && !readOnly && (current.organization ? <button className="secondary" disabled={data.loading || Boolean(data.error) || busy} onClick={() => void unassign()}>Unassign organization</button> : <button disabled={data.loading || Boolean(data.error) || busy} onClick={() => setEditor({ scope: data.scope })}>Assign organization</button>)}<button className="secondary" onClick={() => void data.reload()}>Refresh organization</button></div>
    </div>}
    {admin && !readOnly && editor?.scope === data.scope && !current?.organization && <AssignOrganizationForm onClose={() => setEditor((value) => value === editor ? undefined : value)} onSave={assign} />}
  </section>;
}
