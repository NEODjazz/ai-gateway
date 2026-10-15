import { useState, type FormEvent } from "react";
import { ModalFrame } from "../../components/ModalFrame";
import { ChipMultiSelect } from "../../components/ChipMultiSelect";

export type IdentityKind = "users" | "teams";
export type DirectoryIdentity = { id: string; name?: string; email?: string; description?: string; external_id?: string; roles?: string[]; team_ids?: string[]; status: string; member_count?: number; created_at?: string; updated_at?: string; deleted_at?: string };
export const directoryRoles = ["user", "developer", "team_admin", "org_admin", "admin"];

export function IdentityForm({ kind, initial, onClose, onSave }: { kind: IdentityKind; initial?: DirectoryIdentity; onClose: () => void; onSave: (value: DirectoryIdentity) => Promise<void> }) {
  const [id, setID] = useState(() => initial?.id || `${kind === "users" ? "user" : "team"}-${crypto.randomUUID()}`);
  const [name, setName] = useState(initial?.name || "");
  const [email, setEmail] = useState(initial?.email || "");
  const [description, setDescription] = useState(initial?.description || "");
  const [externalID, setExternalID] = useState(initial?.external_id || "");
  const [status, setStatus] = useState(initial?.status || "active");
  const [roles, setRoles] = useState(initial?.roles || ["user"]);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const title = `${initial ? "Edit" : "Create"} ${kind === "users" ? "user" : "team"}`;
  const close = () => { if (!saving) onClose(); };
  async function submit(event: FormEvent) {
    event.preventDefault(); if (saving) return;
    if (initial?.status === "active" && status === "disabled" && !window.confirm(`Disable ${kind === "users" ? "user" : "team"} ${id}? Existing access that depends on this identity will be rejected.`)) return;
    setSaving(true); setError("");
    try { await onSave({ id: id.trim(), name: name.trim(), external_id: externalID.trim(), status, ...(kind === "users" ? { email: email.trim(), roles } : { description: description.trim() }) }); onClose(); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not save identity"); }
    finally { setSaving(false); }
  }
  return <ModalFrame label={title} onClose={close} dismissDisabled={saving}>
    <section className="modal directory-form"><div className="modal-heading"><h2>{title}</h2><button type="button" className="icon-button" aria-label="Close identity form" disabled={saving} onClick={close}>×</button></div>
      <form onSubmit={submit}><fieldset disabled={saving} className="form-grid">
        <label>ID<input required maxLength={256} value={id} readOnly={Boolean(initial)} onChange={(event) => setID(event.target.value)} /></label>
        <label>Name<input required={kind === "teams"} maxLength={256} value={name} onChange={(event) => setName(event.target.value)} /></label>
        {kind === "users" ? <label>Email<input type="email" maxLength={320} value={email} onChange={(event) => setEmail(event.target.value)} /></label> : <label>Description<textarea maxLength={1024} value={description} onChange={(event) => setDescription(event.target.value)} /></label>}
        <label>External ID<input maxLength={256} value={externalID} onChange={(event) => setExternalID(event.target.value)} /></label>
        <label>Status<select value={status} onChange={(event) => setStatus(event.target.value)}><option value="active">Active</option><option value="disabled">Disabled</option></select></label>
        {kind === "users" && <div className="span-2"><ChipMultiSelect label="Approved global roles" options={[...new Set([...directoryRoles, ...(initial?.roles || [])])].map((value) => ({ value, label: value }))} value={roles} onChange={setRoles} /></div>}
      </fieldset>
      <p className="muted">{kind === "users" ? "This creates a directory identity, not an email invitation. Sign-in requires a configured identity provider and an approved subject binding. Team and organization roles also require matching memberships." : "Team membership, organization ownership, model access and budgets are managed in the team workspace."}</p>
      {error && <p className="form-error" role="alert">{error}</p>}
      <div className="modal-actions"><button type="button" className="secondary" disabled={saving} onClick={close}>Cancel</button><button disabled={saving}>{saving ? "Saving…" : initial ? "Save changes" : kind === "users" ? "Create user" : "Create team"}</button></div></form>
    </section>
  </ModalFrame>;
}
