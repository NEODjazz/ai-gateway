import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import { useAuth } from "../auth/AuthContext";
import { ActionsMenu } from "../components/ActionsMenu";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { ManagedDataTable } from "../components/ManagedDataTable";
import { PageHeader } from "../components/PageHeader";
import { StatCard } from "../components/StatCard";
import type { Row } from "../components/DataTable";
import { formatTimestamp } from "../format";

type Credential = Row & { id: string; provider_id?: string; description?: string; created_at: string; updated_at: string };
type Provider = { id: string; type: string; enabled: boolean };
type Deployment = { id: string; provider_id: string; credential_id?: string };
type FormMode = "create" | "edit" | "rotate";
type CredentialForm = { id: string; provider_id: string; description: string; secret: string; confirm: string };

const emptyForm: CredentialForm = { id: "", provider_id: "", description: "", secret: "", confirm: "" };

function records<T>(payload: unknown): T[] {
  if (Array.isArray(payload)) return payload as T[];
  const data = payload && typeof payload === "object" ? (payload as { data?: unknown }).data : undefined;
  return Array.isArray(data) ? data as T[] : [];
}

const columns = [
  { key: "id", label: "Credential" },
  { key: "provider_display", label: "Provider" },
  { key: "description", label: "Description" },
  { key: "deployment_count", label: "Deployments" },
  { key: "scope", label: "Scope", render: (value: unknown) => <span className={`status ${value === "Bound" ? "enabled" : "disabled"}`}>{String(value)}</span> },
  { key: "created_at", label: "Created", render: formatTimestamp },
  { key: "updated_at", label: "Updated", render: formatTimestamp }
];

export function CredentialsPage() {
  const { client } = useAuth();
  const [credentials, setCredentials] = useState<Credential[]>([]);
  const [providers, setProviders] = useState<Provider[]>([]);
  const [deployments, setDeployments] = useState<Deployment[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [mode, setMode] = useState<FormMode>();
  const [selected, setSelected] = useState<Credential>();
  const [form, setForm] = useState<CredentialForm>(emptyForm);
  const [formError, setFormError] = useState("");
  const [saving, setSaving] = useState(false);

  const load = useCallback(async () => {
    setLoading(true); setError("");
    try {
      const [credentialPayload, providerPayload, deploymentPayload] = await Promise.all([
        client.request("/admin/v1/credentials"), client.request("/admin/v1/providers"), client.request("/admin/v1/model-deployments")
      ]);
      setCredentials(records<Credential>(credentialPayload));
      setProviders(records<Provider>(providerPayload));
      setDeployments(records<Deployment>(deploymentPayload));
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load credentials"); }
    finally { setLoading(false); }
  }, [client]);
  useEffect(() => { void load(); }, [load]);

  const rows = useMemo(() => credentials.map((credential) => ({
    ...credential,
    provider_display: credential.provider_id || "Shared",
    deployment_count: deployments.filter((deployment) => deployment.credential_id === credential.id).length,
    scope: credential.provider_id ? "Bound" : "Shared"
  })), [credentials, deployments]);

  function openForm(nextMode: FormMode, credential?: Credential) {
    setMode(nextMode); setSelected(credential); setFormError(""); setNotice("");
    setForm(credential ? { id: credential.id, provider_id: credential.provider_id || "", description: credential.description || "", secret: "", confirm: "" } : emptyForm);
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if ((mode === "create" || mode === "rotate") && form.secret !== form.confirm) { setFormError("Secret confirmation does not match"); return; }
    setSaving(true); setFormError("");
    try {
      if (mode === "create") {
        await client.request("/admin/v1/credentials", { method: "POST", body: { id: form.id, provider_id: form.provider_id, description: form.description, secret: form.secret } });
        setNotice(`Credential ${form.id} created. The secret remains write-only.`);
      } else if (mode === "edit" && selected) {
        await client.request(`/admin/v1/credentials/${encodeURIComponent(selected.id)}`, { method: "PUT", body: { provider_id: form.provider_id, description: form.description } });
        setNotice(`Credential ${selected.id} metadata updated without changing its secret.`);
      } else if (mode === "rotate" && selected) {
        await client.request(`/admin/v1/credentials/${encodeURIComponent(selected.id)}/rotate`, { method: "POST", body: { secret: form.secret } });
        setNotice(`Credential ${selected.id} secret rotated. Existing deployments use the new value.`);
      }
      setMode(undefined); setForm(emptyForm); await load();
    } catch (cause) { setFormError(cause instanceof Error ? cause.message : "Could not save credential"); }
    finally { setSaving(false); }
  }

  async function remove(credential: Credential) {
    if (!window.confirm(`Delete ${credential.id}?`)) return;
    setError(""); setNotice("");
    try { await client.request(`/admin/v1/credentials/${encodeURIComponent(credential.id)}`, { method: "DELETE" }); await load(); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not delete credential"); }
  }

  const bound = credentials.filter((credential) => credential.provider_id).length;
  const inUse = credentials.filter((credential) => deployments.some((deployment) => deployment.credential_id === credential.id)).length;
  const title = mode === "create" ? "Create Credential" : mode === "edit" ? "Edit credential metadata" : "Rotate credential secret";
  return <>
    <PageHeader eyebrow="Secret vault" title="Credentials" description="Manage encrypted write-only provider credentials independently from endpoints and deployments." />
    <div className="usage-stats-grid credential-stats"><StatCard label="Credentials" value={String(credentials.length)} /><StatCard label="Provider-bound" value={String(bound)} /><StatCard label="Shared" value={String(credentials.length - bound)} /><StatCard label="Used by deployments" value={String(inUse)} /></div>
    {error && <ErrorState message={error} retry={() => void load()} />}
    {notice && <div className="operation-result" role="status">{notice}</div>}
    {loading ? <LoadingState /> : <ManagedDataTable rows={rows} columns={columns} primaryAction={<button onClick={() => openForm("create")}>Create Credential</button>} onRefresh={load} searchPlaceholder="Search credentials" defaultHidden={["created_at"]} actions={(row) => {
      const credential = credentials.find((item) => item.id === row.id)!;
      return <ActionsMenu label={`Actions for ${credential.id}`} items={[
        { label: "Edit metadata", onSelect: () => openForm("edit", credential) },
        { label: "Rotate secret", onSelect: () => openForm("rotate", credential) },
        { label: "Delete", tone: "danger", onSelect: () => void remove(credential) }
      ]} />;
    }} />}
    {mode && <div className="modal-backdrop" role="presentation"><section className="modal credential-form-modal" role="dialog" aria-modal="true" aria-label={title}><div className="modal-heading"><div><h2>{title}</h2>{selected && <span className="muted">{selected.id}</span>}</div><button className="icon-button" aria-label="Close credential form" onClick={() => setMode(undefined)}>×</button></div><form className="credential-form" onSubmit={submit}>
      {mode === "create" && <label><span>Credential ID</span><input required maxLength={128} value={form.id} onChange={(event) => setForm((current) => ({ ...current, id: event.target.value }))} /></label>}
      {mode !== "rotate" && <><label><span>Provider</span><select aria-label="Credential provider" value={form.provider_id} onChange={(event) => setForm((current) => ({ ...current, provider_id: event.target.value }))}><option value="">Shared credential</option>{providers.map((provider) => <option key={provider.id} value={provider.id}>{provider.id} — {provider.type}{provider.enabled ? "" : " — disabled"}</option>)}</select></label><label><span>Description</span><input maxLength={512} value={form.description} onChange={(event) => setForm((current) => ({ ...current, description: event.target.value }))} /></label></>}
      {(mode === "create" || mode === "rotate") && <><label><span>New secret</span><input required type="password" autoComplete="new-password" value={form.secret} onChange={(event) => setForm((current) => ({ ...current, secret: event.target.value }))} /></label><label><span>Confirm secret</span><input required type="password" autoComplete="new-password" value={form.confirm} onChange={(event) => setForm((current) => ({ ...current, confirm: event.target.value }))} /></label><p className="credential-write-only-notice">The plaintext value is sent once over this form and is never returned by the gateway.</p></>}
      {mode === "edit" && <p className="credential-write-only-notice">Metadata changes preserve the current encrypted secret. Use “Rotate secret” to replace it.</p>}
      {formError && <p className="form-error credential-form-error" role="alert">{formError}</p>}
      <div className="modal-actions credential-form-actions"><button type="button" className="secondary" onClick={() => setMode(undefined)}>Cancel</button><button disabled={saving}>{saving ? "Saving…" : mode === "rotate" ? "Rotate secret" : "Save"}</button></div>
    </form></section></div>}
  </>;
}
