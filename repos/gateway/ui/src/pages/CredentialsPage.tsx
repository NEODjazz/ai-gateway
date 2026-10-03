import { ModalFrame } from "../components/ModalFrame";
import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import { useAuth } from "../auth/AuthContext";
import { ActionsMenu } from "../components/ActionsMenu";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { ManagedDataTable } from "../components/ManagedDataTable";
import { PageHeader } from "../components/PageHeader";
import { StatCard } from "../components/StatCard";
import type { Row } from "../components/DataTable";
import { formatTimestamp } from "../format";
import { GatewayButton } from "../components/GatewayButton";
import { ModalCloseButton } from "../components/ModalCloseButton";

type Credential = Row & { id: string; provider_id?: string; description?: string; kind?: string; created_at: string; updated_at: string };
type Provider = { id: string; type: string; auth_type?: string; enabled: boolean };
type Deployment = { id: string; provider_id: string; credential_id?: string };
type FormMode = "create" | "edit" | "rotate";
type CredentialForm = { id: string; provider_id: string; description: string; secret: string; confirm: string; tenantID: string; clientID: string };

const emptyForm: CredentialForm = { id: "", provider_id: "", description: "", secret: "", confirm: "", tenantID: "", clientID: "" };

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
  const [credentialKind, setCredentialKind] = useState<"bearer" | "service_principal">("bearer");
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
    setCredentialKind(credential?.kind === "azure_service_principal" ? "service_principal" : "bearer");
    setForm(credential ? { ...emptyForm, id: credential.id, provider_id: credential.provider_id || "", description: credential.description || "" } : emptyForm);
  }

  function closeForm() { setMode(undefined); setForm(emptyForm); setCredentialKind("bearer"); }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if ((mode === "create" || mode === "rotate") && form.secret !== form.confirm) { setFormError("Secret confirmation does not match"); return; }
    if ((mode === "create" || mode === "rotate") && credentialKind === "service_principal" && (!form.tenantID.trim() || !form.clientID.trim())) { setFormError("Tenant ID and Client ID are required"); return; }
    setSaving(true); setFormError("");
    try {
      const secret = credentialKind === "service_principal"
        ? `azure-sp:v1:${JSON.stringify({ tenant_id: form.tenantID.trim(), client_id: form.clientID.trim(), client_secret: form.secret })}`
        : form.secret;
      if (mode === "create") {
        await client.request("/admin/v1/credentials", { method: "POST", body: { id: form.id, provider_id: form.provider_id, description: form.description, secret } });
        setNotice(`Credential ${form.id} created. The secret remains write-only.`);
      } else if (mode === "edit" && selected) {
        await client.request(`/admin/v1/credentials/${encodeURIComponent(selected.id)}`, { method: "PUT", body: { provider_id: form.provider_id, description: form.description } });
        setNotice(`Credential ${selected.id} metadata updated without changing its secret.`);
      } else if (mode === "rotate" && selected) {
        await client.request(`/admin/v1/credentials/${encodeURIComponent(selected.id)}/rotate`, { method: "POST", body: { secret } });
        setNotice(`Credential ${selected.id} secret rotated. Existing deployments use the new value.`);
      }
      closeForm(); await load();
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
  const azureEntra = providers.some((provider) => provider.id === form.provider_id && provider.type === "azure-openai" && provider.auth_type === "entra");
  return <>
    <PageHeader eyebrow="Secret vault" title="Credentials" description="Manage encrypted write-only provider credentials independently from endpoints and deployments." />
    <div className="usage-stats-grid credential-stats"><StatCard label="Credentials" value={String(credentials.length)} /><StatCard label="Provider-bound" value={String(bound)} /><StatCard label="Shared" value={String(credentials.length - bound)} /><StatCard label="Used by deployments" value={String(inUse)} /></div>
    {error && <ErrorState message={error} retry={() => void load()} />}
    {notice && <div className="operation-result" role="status">{notice}</div>}
    {loading ? <LoadingState /> : <ManagedDataTable rows={rows} columns={columns} primaryAction={<GatewayButton size="l" onClick={() => openForm("create")}>Create Credential</GatewayButton>} onRefresh={load} searchPlaceholder="Search credentials" defaultHidden={["created_at"]} actions={(row) => {
      const credential = credentials.find((item) => item.id === row.id)!;
      return <ActionsMenu label={`Actions for ${credential.id}`} items={[
        { label: "Edit metadata", onSelect: () => openForm("edit", credential) },
        { label: "Rotate secret", onSelect: () => openForm("rotate", credential) },
        { label: "Delete", tone: "danger", onSelect: () => void remove(credential) }
      ]} />;
    }} />}
    {mode && <ModalFrame label={title} onClose={closeForm}><section className="modal credential-form-modal"><div className="modal-heading"><div><h2>{title}</h2>{selected && <span className="muted">{selected.id}</span>}</div><ModalCloseButton label="Close credential form" onClick={closeForm} /></div><form className="credential-form" onSubmit={submit}>
      {mode === "create" && <label><span>Credential ID</span><input required maxLength={128} value={form.id} onChange={(event) => setForm((current) => ({ ...current, id: event.target.value }))} /></label>}
      {mode !== "rotate" && <><label><span>Provider</span><select aria-label="Credential provider" value={form.provider_id} onChange={(event) => { setForm((current) => ({ ...current, provider_id: event.target.value })); setCredentialKind("bearer"); }}><option value="">Shared credential</option>{providers.map((provider) => <option key={provider.id} value={provider.id}>{provider.id} — {provider.type}{provider.enabled ? "" : " — disabled"}</option>)}</select></label><label><span>Description</span><input maxLength={512} value={form.description} onChange={(event) => setForm((current) => ({ ...current, description: event.target.value }))} /></label></>}
      {(mode === "create" || mode === "rotate") && <>{azureEntra && <label><span>Azure credential type</span><select value={credentialKind} onChange={(event) => setCredentialKind(event.target.value as "bearer" | "service_principal")}><option value="bearer">Bearer token</option><option value="service_principal">Service principal</option></select></label>}{credentialKind === "service_principal" && <><label><span>Tenant ID</span><input required maxLength={128} value={form.tenantID} onChange={(event) => setForm((current) => ({ ...current, tenantID: event.target.value }))} /></label><label><span>Client ID</span><input required maxLength={128} value={form.clientID} onChange={(event) => setForm((current) => ({ ...current, clientID: event.target.value }))} /></label></>}<label><span>{credentialKind === "service_principal" ? "Client secret" : "New secret"}</span><input required type="password" autoComplete="new-password" value={form.secret} onChange={(event) => setForm((current) => ({ ...current, secret: event.target.value }))} /></label><label><span>{credentialKind === "service_principal" ? "Confirm client secret" : "Confirm secret"}</span><input required type="password" autoComplete="new-password" value={form.confirm} onChange={(event) => setForm((current) => ({ ...current, confirm: event.target.value }))} /></label><p className="credential-write-only-notice">The plaintext value is sent once over this form and is never returned by the gateway.</p></>}
      {mode === "edit" && <p className="credential-write-only-notice">Metadata changes preserve the current encrypted secret. Use “Rotate secret” to replace it.</p>}
      {formError && <p className="form-error credential-form-error" role="alert">{formError}</p>}
      <div className="modal-actions credential-form-actions"><GatewayButton type="button" view="outlined" onClick={closeForm}>Cancel</GatewayButton><GatewayButton type="submit" disabled={saving}>{saving ? "Saving…" : mode === "rotate" ? "Rotate secret" : "Save"}</GatewayButton></div>
    </form></section></ModalFrame>}
  </>;
}
