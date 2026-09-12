import { FormEvent, useCallback, useEffect, useRef, useState } from "react";
import { useAuth } from "../auth/AuthContext";
import { ActionsMenu } from "../components/ActionsMenu";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { GatewayFileButton } from "../components/GatewayFileButton";
import { ManagedDataTable } from "../components/ManagedDataTable";
import { ModalCloseButton } from "../components/ModalCloseButton";
import { ModalFrame } from "../components/ModalFrame";
import { PageHeader } from "../components/PageHeader";
import { StatCard } from "../components/StatCard";
import type { Row } from "../components/DataTable";

type Skill = {
  id: string;
  type: "skill";
  display_name?: string;
  latest_version_id?: string;
  created_at?: string;
  updated_at?: string;
  source: { type: "custom" | "anthropic" | "anthropic_example" | "plugin" };
};

type SkillList = { data?: Skill[]; has_more?: boolean };
type SkillVersion = { id: string; type: "skill_version"; skill_id: string; name?: string; description?: string; created_at?: string };
type SkillVersionList = { data?: SkillVersion[]; has_more?: boolean };

function CreateSkillForm({ onClose, onCreate }: { onClose: () => void; onCreate: (files: File[], displayName: string) => Promise<void> }) {
  const [files, setFiles] = useState<File[]>([]);
  const [displayName, setDisplayName] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!files.length) { setError("Select at least one skill file."); return; }
    setSaving(true); setError("");
    try { await onCreate(files, displayName.trim()); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not create skill"); }
    finally { setSaving(false); }
  }
  return <ModalFrame label="Create skill" onClose={onClose}><form className="modal key-form-modal" onSubmit={submit}>
    <div className="modal-heading"><div><h2>Create skill</h2><span className="muted">Upload the files that form one provider-managed skill package.</span></div><ModalCloseButton label="Close skill form" onClick={onClose} /></div>
    <div className="key-form"><label><span>Display name</span><input aria-label="Skill display name" maxLength={255} value={displayName} onChange={(event) => setDisplayName(event.target.value)} /></label><div><GatewayFileButton label="Skill files" multiple onChange={(event) => setFiles(Array.from(event.target.files || []))}>Select files</GatewayFileButton><p className="muted">{files.length ? `${files.length} file${files.length === 1 ? "" : "s"} selected` : "No files selected"}</p></div></div>
    {error && <p className="form-error" role="alert">{error}</p>}<div className="modal-actions"><button type="button" className="secondary" onClick={onClose}>Cancel</button><button disabled={saving}>{saving ? "Uploading…" : "Create skill"}</button></div>
  </form></ModalFrame>;
}

function UpdateSkillForm({ skill, onClose, onUpdate }: { skill: Skill; onClose: () => void; onUpdate: (version: string) => Promise<void> }) {
  const [version, setVersion] = useState(skill.latest_version_id || "");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!/^[A-Za-z0-9_.-]{1,128}$/.test(version)) { setError("Enter a valid version ID."); return; }
    setSaving(true); setError("");
    try { await onUpdate(version); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not update skill"); }
    finally { setSaving(false); }
  }
  return <ModalFrame label={`Update ${skill.display_name || skill.id}`} onClose={onClose}><form className="modal compact-modal" onSubmit={submit}>
    <div className="modal-heading"><h2>Set default version</h2><ModalCloseButton label="Close skill update" onClick={onClose} /></div>
    <label><span>Version ID</span><input aria-label="Default version ID" required maxLength={128} value={version} onChange={(event) => setVersion(event.target.value)} /></label>
    {error && <p className="form-error" role="alert">{error}</p>}<div className="modal-actions"><button type="button" className="secondary" onClick={onClose}>Cancel</button><button disabled={saving}>{saving ? "Saving…" : "Save default"}</button></div>
  </form></ModalFrame>;
}

function SkillVersions({ skill, onClose }: { skill: Skill; onClose: () => void }) {
  const { client } = useAuth();
  const [versions, setVersions] = useState<SkillVersion[]>([]);
  const [hasMore, setHasMore] = useState(false);
  const [files, setFiles] = useState<File[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const requestGeneration = useRef(0);
  const load = useCallback(async () => {
    const generation = ++requestGeneration.current;
    setLoading(true); setError("");
    try { const payload = await client.request<SkillVersionList>(`/v1/skills/${encodeURIComponent(skill.id)}/versions?limit=1000`); if (generation === requestGeneration.current) { setVersions(payload.data || []); setHasMore(Boolean(payload.has_more)); } }
    catch (cause) { if (generation === requestGeneration.current) setError(cause instanceof Error ? cause.message : "Could not load skill versions"); }
    finally { if (generation === requestGeneration.current) setLoading(false); }
  }, [client, skill.id]);
  useEffect(() => { void load(); return () => { requestGeneration.current++; }; }, [load]);
  async function create() {
    if (!files.length) { setError("Select one or more skill version files."); return; }
    setBusy(true); setError("");
    try { const form = new FormData(); for (const file of files) form.append("files", file); await client.requestForm(`/v1/skills/${encodeURIComponent(skill.id)}/versions`, form, { method: "POST" }); setFiles([]); await load(); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not create skill version"); }
    finally { setBusy(false); }
  }
  async function remove(version: SkillVersion) {
    if (!window.confirm(`Delete version ${version.id}?`)) return;
    setBusy(true); setError("");
    try { await client.request(`/v1/skills/${encodeURIComponent(skill.id)}/versions/${encodeURIComponent(version.id)}`, { method: "DELETE" }); await load(); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not delete skill version"); }
    finally { setBusy(false); }
  }
  async function download(version: SkillVersion) {
    try { const response = await client.download(`/v1/skills/${encodeURIComponent(skill.id)}/versions/${encodeURIComponent(version.id)}/content`); const url = URL.createObjectURL(response.body); const link = document.createElement("a"); link.href = url; link.download = `${skill.id}-${version.id}.zip`; link.click(); URL.revokeObjectURL(url); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not download skill version"); }
  }
  const mutable = skill.source.type === "custom";
  return <ModalFrame label={`Versions for ${skill.display_name || skill.id}`} onClose={onClose}><section className="modal key-form-modal"><div className="modal-heading"><div><h2>Skill versions</h2><span className="muted">{skill.display_name || skill.id}</span></div><ModalCloseButton label="Close skill versions" onClick={onClose} /></div>
    {error && <ErrorState message={error} retry={() => void load()} />}{loading && !versions.length ? <LoadingState /> : <><div className="table-card"><div className="table-scroll"><table><thead><tr><th>Version</th><th>Name</th><th>Created</th><th>Actions</th></tr></thead><tbody>{versions.map((version) => <tr key={version.id}><td><code>{version.id}</code></td><td>{version.name || "—"}</td><td>{version.created_at || "—"}</td><td><button className="secondary" onClick={() => void download(version)}>Download</button>{mutable && <button className="danger" disabled={busy} onClick={() => void remove(version)}>Delete</button>}</td></tr>)}</tbody></table></div></div>{!versions.length && <p className="muted">No versions returned.</p>}{hasMore && <p className="muted" role="status">Additional versions are available beyond the 1,000 loaded rows.</p>}</>}
    {mutable && <div className="key-toolbar"><div><GatewayFileButton label="Skill version files" multiple onChange={(event) => setFiles(Array.from(event.target.files || []))}>Select version files</GatewayFileButton>{files.length > 0 && <span className="muted"> {files.map((file) => file.name).join(", ")}</span>}</div><button disabled={busy || !files.length} onClick={() => void create()}>{busy ? "Uploading…" : "Create version"}</button></div>}
  </section></ModalFrame>;
}

export function SkillsPage() {
  const { client } = useAuth();
  const [skills, setSkills] = useState<Skill[]>([]);
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<Skill>();
  const [versionsFor, setVersionsFor] = useState<Skill>();
  const requestGeneration = useRef(0);
  const load = useCallback(async () => {
    const generation = ++requestGeneration.current;
    setLoading(true); setError("");
    try {
      const payload = await client.request<SkillList>("/v1/skills?limit=1000");
      if (generation === requestGeneration.current) { setSkills(payload.data || []); setHasMore(Boolean(payload.has_more)); }
    } catch (cause) {
      if (generation === requestGeneration.current) setError(cause instanceof Error ? cause.message : "Could not load skills");
    } finally { if (generation === requestGeneration.current) setLoading(false); }
  }, [client]);
  useEffect(() => { void load(); return () => { requestGeneration.current++; }; }, [load]);
  async function create(files: File[], displayName: string) {
    const form = new FormData();
    for (const file of files) form.append("files", file);
    if (displayName) form.append("display_name", displayName);
    await client.requestForm<Skill>("/v1/skills", form, { method: "POST" });
    setCreating(false); await load();
  }
  async function update(skill: Skill, version: string) {
    await client.request(`/v1/skills/${encodeURIComponent(skill.id)}`, { method: "POST", body: { default_version: version } });
    setEditing(undefined); await load();
  }
  async function remove(skill: Skill) {
    if (!window.confirm(`Delete skill ${skill.display_name || skill.id}?`)) return;
    try { await client.request(`/v1/skills/${encodeURIComponent(skill.id)}`, { method: "DELETE" }); await load(); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not delete skill"); }
  }
  async function download(skill: Skill) {
    try {
      const response = await client.download(`/v1/skills/${encodeURIComponent(skill.id)}/content`);
      const url = URL.createObjectURL(response.body);
      const link = document.createElement("a"); link.href = url; link.download = `${skill.id}.zip`; link.click(); URL.revokeObjectURL(url);
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not download skill"); }
  }
  const custom = skills.filter((skill) => skill.source.type === "custom").length;
  const rows: Row[] = skills.map((skill) => ({ id: skill.id, name: skill.display_name || skill.id, source: skill.source.type, version: skill.latest_version_id || "—", updated_at: skill.updated_at || skill.created_at || "—", _skill: skill }));
  if (loading && !skills.length) return <LoadingState />;
  return <><PageHeader eyebrow="AI Hub" title="Skills" description="Owner-isolated provider skill packages and shared read-only skills." />{error && <ErrorState message={error} retry={() => void load()} />}
    <div className="usage-stats-grid"><StatCard label="Loaded" value={skills.length} /><StatCard label="Custom" value={custom} /><StatCard label="Shared" value={skills.length - custom} /><StatCard label="More available" value={hasMore ? "Yes" : "No"} /></div>
    {hasMore && <section className="notice-card" role="status"><h2>Additional skills are available</h2><p>The provider returned more than the 1,000 rows loaded in this view.</p></section>}
    <ManagedDataTable rows={rows} columns={[{ key: "id", label: "Skill" }, { key: "name", label: "Name" }, { key: "source", label: "Source" }, { key: "version", label: "Latest version" }, { key: "updated_at", label: "Updated" }]} primaryAction={<button onClick={() => setCreating(true)}>Create skill</button>} onRefresh={load} searchPlaceholder="Search skills" actions={(row) => { const skill = row._skill as Skill; const mutable = skill.source.type === "custom"; return <ActionsMenu label={`Actions for skill ${skill.id}`} items={[{ label: "Manage versions", onSelect: () => setVersionsFor(skill) }, { label: "Download", onSelect: () => download(skill) }, { label: "Set default version", onSelect: () => setEditing(skill), disabled: !mutable }, { label: "Delete", onSelect: () => remove(skill), disabled: !mutable, tone: "danger" }]} />; }} />
    {creating && <CreateSkillForm onClose={() => setCreating(false)} onCreate={create} />}{editing && <UpdateSkillForm skill={editing} onClose={() => setEditing(undefined)} onUpdate={(version) => update(editing, version)} />}{versionsFor && <SkillVersions skill={versionsFor} onClose={() => setVersionsFor(undefined)} />}
  </>;
}
