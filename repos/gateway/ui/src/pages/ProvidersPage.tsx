import { ModalFrame } from "../components/ModalFrame";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import { ActionsMenu } from "../components/ActionsMenu";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { ManagedDataTable } from "../components/ManagedDataTable";
import { PageHeader } from "../components/PageHeader";
import { ResourceForm } from "../components/ResourceForm";
import { StatCard } from "../components/StatCard";
import type { Row } from "../components/DataTable";
import { resourceConfigs } from "./resourceConfigs";
import { GatewayButton } from "../components/GatewayButton";
import { ModalCloseButton } from "../components/ModalCloseButton";

type Provider = Row & { id: string; type: string; base_url?: string; enabled: boolean };
type Credential = { id: string; provider_id?: string; description?: string };
type Deployment = { id: string; provider_id: string; enabled: boolean };
type ProviderProbe = { provider_id: string; status: string; latency_ms: number; model_count: number };
type DiscoveredModel = { id: string };
type ConnectionDialog = { provider: Provider; mode: "test" | "discover" };

function records<T>(payload: unknown): T[] {
  if (Array.isArray(payload)) return payload as T[];
  const data = payload && typeof payload === "object" ? (payload as { data?: unknown }).data : undefined;
  return Array.isArray(data) ? data as T[] : [];
}

function status(value: unknown) {
  if (value === "available") return <span className="status enabled">Available</span>;
  if (value === "unavailable") return <span className="status disabled">Unavailable</span>;
  return <span className="muted">Not tested</span>;
}

const columns = [
  { key: "id", label: "Provider" },
  { key: "type", label: "Type" },
  { key: "base_url", label: "Base URL" },
  { key: "credential_count", label: "Credentials" },
  { key: "deployment_count", label: "Deployments" },
  { key: "connection_status", label: "Connection", render: status },
  { key: "last_latency", label: "Last latency" },
  { key: "enabled", label: "Status", render: (value: unknown) => <span className={`status ${value ? "enabled" : "disabled"}`}>{value ? "Enabled" : "Disabled"}</span> }
];

export function ProvidersPage() {
  const { client } = useAuth();
  const navigate = useNavigate();
  const [providers, setProviders] = useState<Provider[]>([]);
  const [credentials, setCredentials] = useState<Credential[]>([]);
  const [deployments, setDeployments] = useState<Deployment[]>([]);
  const [probes, setProbes] = useState<Record<string, ProviderProbe>>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [editing, setEditing] = useState<Provider | null | undefined>(undefined);
  const [connection, setConnection] = useState<ConnectionDialog>();
  const [credentialID, setCredentialID] = useState("");
  const [connectionError, setConnectionError] = useState("");
  const [busy, setBusy] = useState(false);
  const [discovered, setDiscovered] = useState<DiscoveredModel[]>();

  const load = useCallback(async () => {
    setLoading(true); setError("");
    try {
      const [providerPayload, credentialPayload, deploymentPayload] = await Promise.all([
        client.request("/admin/v1/providers"), client.request("/admin/v1/credentials"), client.request("/admin/v1/model-deployments")
      ]);
      setProviders(records<Provider>(providerPayload));
      setCredentials(records<Credential>(credentialPayload));
      setDeployments(records<Deployment>(deploymentPayload));
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load providers"); }
    finally { setLoading(false); }
  }, [client]);
  useEffect(() => { void load(); }, [load]);

  const rows = useMemo(() => providers.map((provider) => {
    const probe = probes[provider.id];
    return {
      ...provider,
      credential_count: credentials.filter((credential) => credential.provider_id === provider.id).length,
      deployment_count: deployments.filter((deployment) => deployment.provider_id === provider.id).length,
      connection_status: probe?.status || "",
      last_latency: probe ? `${probe.latency_ms.toLocaleString("en-US")} ms` : "—"
    };
  }), [credentials, deployments, probes, providers]);
  const connectionCredentials = connection ? credentials.filter((credential) => !credential.provider_id || credential.provider_id === connection.provider.id) : [];

  function openConnection(provider: Provider, mode: ConnectionDialog["mode"]) {
    const available = credentials.filter((credential) => !credential.provider_id || credential.provider_id === provider.id);
    const exact = available.filter((credential) => credential.provider_id === provider.id);
    setCredentialID(exact.length === 1 ? exact[0].id : "");
    setDiscovered(undefined); setConnectionError(""); setConnection({ provider, mode });
  }

  async function runConnection() {
    if (!connection) return;
    setBusy(true); setConnectionError(""); setDiscovered(undefined);
    try {
      const body = { credential_id: credentialID };
      if (connection.mode === "test") {
        const probe = await client.request<ProviderProbe>(`/admin/v1/providers/${encodeURIComponent(connection.provider.id)}/test`, { method: "POST", body });
        setProbes((current) => ({ ...current, [connection.provider.id]: probe }));
      } else {
        const payload = await client.request<{ data?: DiscoveredModel[] }>(`/admin/v1/providers/${encodeURIComponent(connection.provider.id)}/discover-models`, { method: "POST", body });
        setDiscovered(payload.data || []);
      }
    } catch (cause) { setConnectionError(cause instanceof Error ? cause.message : "Provider connection failed"); }
    finally { setBusy(false); }
  }

  async function saveProvider(value: Row) {
    const current = editing;
    const id = String(current?.id || value.id || "");
    await client.request(current ? `/admin/v1/providers/${encodeURIComponent(id)}` : "/admin/v1/providers", { method: current ? "PUT" : "POST", body: value });
    setEditing(undefined); await load();
  }

  async function deleteProvider(provider: Provider) {
    if (!window.confirm(`Delete ${provider.id}?`)) return;
    try { await client.request(`/admin/v1/providers/${encodeURIComponent(provider.id)}`, { method: "DELETE" }); await load(); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not delete provider"); }
  }

  const enabledCount = providers.filter((provider) => provider.enabled).length;
  const availableCount = Object.values(probes).filter((probe) => probe.status === "available").length;
  return <>
    <PageHeader eyebrow="Connectivity" title="Providers" description="Manage provider endpoints, select their stored credentials, test connectivity and discover models before onboarding." />
    <div className="usage-stats-grid provider-stats"><StatCard label="Providers" value={String(providers.length)} /><StatCard label="Enabled" value={String(enabledCount)} /><StatCard label="Credentials" value={String(credentials.length)} /><StatCard label="Available in this session" value={String(availableCount)} /></div>
    {error && <ErrorState message={error} retry={() => void load()} />}
    {loading ? <LoadingState /> : <ManagedDataTable rows={rows} columns={columns} primaryAction={<GatewayButton size="l" onClick={() => setEditing(null)}>Create Provider</GatewayButton>} onRefresh={load} searchPlaceholder="Search providers" defaultHidden={["last_latency"]} actions={(row) => {
      const provider = providers.find((item) => item.id === row.id)!;
      return <ActionsMenu label={`Actions for ${provider.id}`} items={[
        { label: "Test connection", onSelect: () => openConnection(provider, "test") },
        { label: "Discover models", onSelect: () => openConnection(provider, "discover") },
        { label: "Edit", onSelect: () => setEditing(provider) },
        { label: "Delete", tone: "danger", onSelect: () => void deleteProvider(provider) }
      ]} />;
    }} />}
    {editing !== undefined && <ResourceForm title={`${editing ? "Edit" : "Create"} Provider`} fields={resourceConfigs.providers.fields!} initial={editing || undefined} loadOptions={(path) => client.request(path)} onClose={() => setEditing(undefined)} onSubmit={saveProvider} />}
    {connection && <ModalFrame label={connection.mode === "test" ? "Test provider connection" : "Discover provider models"} onClose={() => setConnection(undefined)}><section className="modal provider-connection-modal">
      <div className="modal-heading"><div><h2>{connection.mode === "test" ? "Test connection" : "Discover models"}</h2><span className="muted">{connection.provider.id} · {connection.provider.type}</span></div><ModalCloseButton label="Close provider connection" onClick={() => setConnection(undefined)} /></div>
      <label>Credential<select aria-label="Provider credential" value={credentialID} onChange={(event) => setCredentialID(event.target.value)}><option value="">No credential</option>{connectionCredentials.map((credential) => <option key={credential.id} value={credential.id}>{credential.id}{credential.description ? ` — ${credential.description}` : ""}{credential.provider_id ? "" : " — shared"}</option>)}</select></label>
      {!connectionCredentials.length && connection.provider.type !== "ollama" && connection.provider.type !== "demo" && <p className="muted">No matching credential is configured. Create one on the Credentials page or test an endpoint that does not require authentication.</p>}
      {connectionError && <p className="form-error" role="alert">{connectionError}</p>}
      {connection.mode === "test" && probes[connection.provider.id] && <div className="provider-probe-result" role="status">{status(probes[connection.provider.id].status)}<span>{probes[connection.provider.id].latency_ms.toLocaleString("en-US")} ms</span><span>{probes[connection.provider.id].model_count.toLocaleString("en-US")} models</span></div>}
      {connection.mode === "discover" && discovered && <><div className="provider-discovery-heading" role="status"><strong>{discovered.length.toLocaleString("en-US")} models discovered</strong><span className="muted">Only identifiers are loaded; provider credentials remain server-side.</span></div><div className="provider-model-list">{discovered.length ? discovered.map((model) => <code key={model.id}>{model.id}</code>) : <span className="muted">No models returned by the provider.</span>}</div></>}
      <div className="modal-actions"><GatewayButton view="outlined" onClick={() => setConnection(undefined)}>Close</GatewayButton>{connection.mode === "discover" && discovered && <GatewayButton view="outlined" onClick={() => navigate(`/model-onboarding?provider_id=${encodeURIComponent(connection.provider.id)}&credential_id=${encodeURIComponent(credentialID)}`)}>Continue to onboarding</GatewayButton>}<GatewayButton disabled={busy} onClick={() => void runConnection()}>{busy ? "Working…" : connection.mode === "test" ? "Test connection" : discovered ? "Discover again" : "Discover models"}</GatewayButton></div>
    </section></ModalFrame>}
  </>;
}
