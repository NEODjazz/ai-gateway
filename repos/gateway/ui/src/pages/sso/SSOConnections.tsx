import { useEffect, useState, type FormEvent } from "react";
import { useAuth } from "../../auth/AuthContext";
import { ErrorState, LoadingState } from "../../components/AsyncState";
import { SSODialog } from "./SSODialog";
import { providerName } from "./SSODetails";
import { GatewayButton } from "../../components/GatewayButton";

export type Connection = { id: string; name: string; provider: string; organization_id?: string };
type View = Connection & { active?: { issuer: string; enabled: boolean }; draft?: { issuer: string }; test_status: string; last_test_at?: number; last_test_status?: string };
export function SSOConnections({ selected, reload, onSelect }: { selected: string; reload: number; onSelect: (value: Connection) => void }) {
  const { client } = useAuth();
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(true);
  const [views, setViews] = useState<View[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [refresh, setRefresh] = useState(0);
  const [input, setInput] = useState<Connection>({ id: "", name: "", provider: "oidc", organization_id: "" });
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    void client.request<{ data: View[] }>("/admin/v1/sso/connections", { signal: controller.signal }).then((data) => {
      if (controller.signal.aborted) return;
      if (!Array.isArray(data.data)) throw new Error("Connections response is invalid.");
      setViews(data.data); setError("");
    }).catch((cause) => { if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : "Could not load SSO connections."); }).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [client, reload, refresh]);
  async function create(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError("");
    try {
      if (!/^[a-z0-9_-]{1,64}$/.test(input.id) || input.id === "default" || input.name.trim() !== input.name || !input.name || input.organization_id?.trim() !== input.organization_id) throw new Error("Use a unique connection ID, a name without surrounding spaces and an exact organization ID.");
      await client.request("/admin/v1/sso/connections", { method: "POST", body: { ...input, organization_id: input.organization_id || undefined } });
      onSelect(input); setOpen(false); setInput({ id: "", name: "", provider: "oidc", organization_id: "" }); setRefresh((n) => n + 1);
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not create SSO connection."); } finally { setBusy(false); }
  }
  return <section className="form-card" aria-label="SSO connections">
    <div className="sso-card-heading"><div><h2>SSO connections</h2><span>Separate identity providers and organization boundaries</span></div><GatewayButton disabled={busy || loading || Boolean(error) || views.filter((view) => view.id !== "default").length >= 16} onClick={() => setOpen(true)}>Add connection</GatewayButton></div>
    {!open && error && <ErrorState message={error} retry={() => setRefresh((n) => n + 1)} />}
    {loading && <LoadingState />}
    <div className="table-scroll"><table><thead><tr><th>Name</th><th>Provider</th><th>Organization</th><th>Issuer</th><th>Status</th><th>Last test</th><th>Configuration</th></tr></thead><tbody>{views.map((view) => <tr key={view.id}><td>{view.name}</td><td>{providerName(view.provider)}</td><td>{view.organization_id || "Platform"}</td><td>{view.active?.issuer || view.draft?.issuer || "Not configured"}</td><td>{view.active?.enabled ? "Active" : view.active ? "Disabled" : view.draft ? "Draft" : "Not configured"}</td><td>{view.last_test_at ? `${view.last_test_status || view.test_status} · ${new Date(view.last_test_at * 1000).toLocaleString()}` : "Not tested"}</td><td><GatewayButton aria-label={`Configure ${view.name}`} view="normal" disabled={busy || view.id === selected} onClick={() => onSelect(view)}>View</GatewayButton></td></tr>)}</tbody></table></div>
    {open && <SSODialog title="Add SSO connection" busy={busy} dirty={Boolean(input.id || input.name || input.organization_id || input.provider !== "oidc")} onClose={() => { setOpen(false); setInput({ id: "", name: "", provider: "oidc", organization_id: "" }); setError(""); }} actions={<GatewayButton type="submit" form="sso-connection-form" disabled={busy}>Create connection</GatewayButton>}>
      {error && <ErrorState message={error} />}
      <form id="sso-connection-form" className="sso-edit-form" onSubmit={create} aria-label="New SSO connection"><fieldset disabled={busy} className="form-grid">
      <label>Connection ID<input required maxLength={64} value={input.id} onChange={(event) => setInput({ ...input, id: event.target.value })} /></label>
      <label>Connection name<input required maxLength={128} value={input.name} onChange={(event) => setInput({ ...input, name: event.target.value })} /></label>
      <label>Connection provider<select value={input.provider} onChange={(event) => setInput({ ...input, provider: event.target.value })}><option value="entra">Microsoft Entra</option><option value="keycloak">Keycloak</option><option value="oidc">Generic OIDC</option></select></label>
      <label>Organization ID<input value={input.organization_id} onChange={(event) => setInput({ ...input, organization_id: event.target.value })} placeholder="Empty for platform access" /></label>
      <p>Organization and provider metadata are immutable. Only active existing organizations are accepted. Each connection has its own encrypted credentials and test, activation and rollback state.</p>
    </fieldset></form></SSODialog>}
  </section>;
}
