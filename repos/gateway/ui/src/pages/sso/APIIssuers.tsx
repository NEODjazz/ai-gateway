import { useEffect, useRef, useState, type FormEvent } from "react";
import { useAuth } from "../../auth/AuthContext";
import { GatewayButton } from "../../components/GatewayButton";
import { SSOMappings, parseMappings } from "./SSOMappings";

type Config = { jwks_url: string; roles_claim: string; role_mappings: Record<string, string> };
type Profile = Config & { id: string; enabled: boolean };
type Issuer = { id: string; name: string; organization_id?: string; issuer: string; audience: string; revision: number; active: Profile | null; draft: Profile | null; can_rollback: boolean; last_test_at?: number; last_test_status?: string; test_expires_at?: number };
const emptyConfig = (): Config => ({ jwks_url: "", roles_claim: "roles", role_mappings: { owners: "admin", users: "user" } });
const errorMessage = (error: unknown) => error instanceof Error ? error.message : "API issuer operation failed";

export function APIIssuers() {
  const { client } = useAuth();
  const [rows, setRows] = useState<Issuer[]>([]);
  const [selected, setSelected] = useState("");
  const [keySession, setKeySession] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true); setError("");
    void client.request<{ data: Issuer[]; key_session: boolean }>("/admin/v1/api-issuers", { signal: controller.signal }).then((result) => {
      if (controller.signal.aborted) return;
      if (!Array.isArray(result.data) || result.data.length > 16 || result.data.some((row) => !/^[a-z0-9_-]{1,64}$/.test(row.id) || !Number.isSafeInteger(row.revision) || row.revision < 1) || new Set(result.data.map((row) => row.id)).size !== result.data.length) throw new Error("Invalid API issuer inventory");
      setRows(result.data); setKeySession(result.key_session === true);
    }).catch((reason) => { if (!controller.signal.aborted) { setRows([]); setKeySession(false); setError(errorMessage(reason)); } }).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [client, reload]);
  const onSaved = (view: Issuer) => { setRows((current) => [...current.filter((row) => row.id !== view.id), view]); setSelected(view.id); };
  const row = rows.find((entry) => entry.id === selected);
  return <section className="form-card" aria-label="API JWT issuers">
    <h2>API JWT issuers</h2>
    <p>Independent resource access-token trust. Browser login connections and the primary API environment configuration remain separate.</p>
    <p>At most 16 entries. Issuer, resource audience and organization are immutable. Test verifies the same approved operator before activation.</p>
    {error && <p role="alert">{error}</p>}
    {loading ? <p role="status">Loading API issuers…</p> : <>
      {!keySession && <p role="note">Sign in with an administrator virtual key to change API trust.</p>}
      <GatewayButton onClick={() => setReload((n) => n + 1)}>Reload API issuers</GatewayButton>
      <GatewayButton disabled={!keySession || rows.length >= 16} onClick={() => setSelected("")}>New API issuer</GatewayButton>
      <div className="table-scroll"><table><thead><tr><th>Name</th><th>Organization</th><th>Issuer</th><th>Audience</th><th>Status</th><th>Last test</th></tr></thead><tbody>{rows.map((entry) => <tr key={entry.id}><td><button onClick={() => setSelected(entry.id)}>{entry.name}</button></td><td>{entry.organization_id || "Platform"}</td><td>{entry.issuer}</td><td>{entry.audience}</td><td>{entry.active?.enabled ? "Active" : entry.active ? "Disabled" : "Draft"}</td><td>{entry.last_test_at ? `${entry.last_test_status} · ${new Date(entry.last_test_at * 1000).toLocaleString()}` : "Never tested"}</td></tr>)}</tbody></table></div>
      <APIIssuerEditor key={`${selected}:${row?.revision || "new"}`} view={row} keySession={keySession} onSaved={onSaved} />
    </>}
  </section>;
}

function APIIssuerEditor({ view, keySession, onSaved }: { view?: Issuer; keySession: boolean; onSaved: (view: Issuer) => void }) {
  const { client } = useAuth();
  const [metadata, setMetadata] = useState({ id: view?.id || "", name: view?.name || "", organization_id: view?.organization_id || "", issuer: view?.issuer || "", audience: view?.audience || "" });
  const [config, setConfig] = useState<Config>(view?.draft || view?.active || emptyConfig());
  const [mapping, setMapping] = useState(JSON.stringify(config.role_mappings));
  const [token, setToken] = useState("");
  const [dirty, setDirty] = useState(false);
  const pending = useRef<AbortController | null>(null);
  const [now, setNow] = useState(Date.now);
  useEffect(() => () => pending.current?.abort(), []);
  useEffect(() => {
    const remaining = (view?.test_expires_at || 0) * 1000 - Date.now();
    if (remaining <= 0) return;
    const timer = window.setTimeout(() => setNow(Date.now()), Math.min(remaining, 2147483647));
    return () => window.clearTimeout(timer);
  }, [view?.test_expires_at]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const mutate = async (path: string, method: string, body: unknown) => {
    pending.current?.abort();
    const controller = new AbortController(); pending.current = controller;
    setBusy(true); setError("");
    try { const result = await client.request<Issuer>(path, { method, body, signal: controller.signal }); if (!controller.signal.aborted) onSaved(result); }
    catch (reason) { if (!controller.signal.aborted) setError(errorMessage(reason)); }
    finally { if (!controller.signal.aborted) setBusy(false); }
  };
  const save = async (event: FormEvent) => {
    event.preventDefault();
    try {
      const body = { jwks_url: config.jwks_url, roles_claim: config.roles_claim, role_mappings: parseMappings(mapping, "Role", Boolean(metadata.organization_id)) };
      if (!view && (!/^[a-z0-9_-]{1,64}$/.test(metadata.id) || metadata.id === "default")) throw new Error("Use a unique lowercase API issuer ID other than default");
      await mutate(view ? `/admin/v1/api-issuers/${view.id}` : "/admin/v1/api-issuers", view ? "PUT" : "POST", view ? { ...body, expected_revision: view.revision } : { ...metadata, ...body });
    } catch (reason) { setError(errorMessage(reason)); }
  };
  const test = () => {
    if (!view) return;
    const resourceToken = token.trim(); setToken("");
    void mutate(`/admin/v1/api-issuers/${view.id}/test`, "POST", { expected_revision: view.revision, token: resourceToken });
  };
  const action = (name: string) => { if (view) void mutate(`/admin/v1/api-issuers/${view.id}/action`, "POST", { expected_revision: view.revision, action: name }); };
  const tested = view?.last_test_status === "passed" && (view.test_expires_at || 0) * 1000 > now;
  const change = (name: keyof Config, value: string) => { setConfig((current) => ({ ...current, [name]: value })); setDirty(true); };
  return <div aria-label={view ? `API issuer ${view.name}` : "New API issuer configuration"}>
    <h3>{view ? `${view.name} · revision ${view.revision}` : "New API issuer"}</h3>
    {error && <p role="alert">{error}</p>}
    <form onSubmit={save} aria-label="API issuer draft">
      <fieldset disabled={busy || !keySession}>
        {(Object.keys(metadata) as (keyof typeof metadata)[]).map((name) => <label key={name}>{({ id: "API issuer ID", name: "API issuer name", organization_id: "API organization ID", issuer: "API issuer URL", audience: "API resource audience" })[name]}<input required={name !== "organization_id"} readOnly={Boolean(view)} value={metadata[name]} maxLength={name === "issuer" ? 2048 : name === "id" ? 64 : name === "name" ? 128 : 256} onChange={(event) => { setMetadata((current) => ({ ...current, [name]: event.target.value })); if (name === "organization_id") setMapping(JSON.stringify(event.target.value ? { owners: "org_admin", users: "user" } : emptyConfig().role_mappings)); setDirty(true); }} /></label>)}
        <label>API JWKS URL<input required value={config.jwks_url} maxLength={2048} onChange={(event) => change("jwks_url", event.target.value)} /></label>
        <label>API roles claim<input required value={config.roles_claim} maxLength={256} onChange={(event) => change("roles_claim", event.target.value)} /></label>
        <SSOMappings kind="Role" value={mapping} tenant={Boolean(metadata.organization_id)} onChange={(value) => { setMapping(value); setDirty(true); }} />
        <GatewayButton type="submit">Save API issuer draft</GatewayButton>
      </fieldset>
    </form>
    {view && <fieldset disabled={busy || !keySession}>
      <label>Resource access token for test<input type="password" autoComplete="off" maxLength={32768} value={token} onChange={(event) => setToken(event.target.value)} /></label>
      <small>Token is cleared when submitted and never stored. Approved directory identity and organization membership are still required.</small>
      <GatewayButton disabled={dirty || !view.draft || !token.trim()} onClick={test}>Test API issuer</GatewayButton>
      <GatewayButton disabled={dirty || !view.draft || !tested} onClick={() => action("activate")}>Activate API issuer</GatewayButton>
      <GatewayButton disabled={!view.active?.enabled} onClick={() => action("disable")}>Disable API issuer</GatewayButton>
      <GatewayButton disabled={!view.can_rollback} onClick={() => action("rollback")}>Rollback API issuer</GatewayButton>
    </fieldset>}
  </div>;
}
