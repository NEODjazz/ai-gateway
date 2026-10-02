import { useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { PageHeader } from "../components/PageHeader";
import { GatewayButton } from "../components/GatewayButton";
import { SSOConnections, type Connection } from "./sso/SSOConnections";
import { SSOMappings, parseMappings } from "./sso/SSOMappings";
import { APIIssuers } from "./sso/APIIssuers";
import { SSOPreset } from "./sso/SSOPreset";

type Profile = {
  organization_id?: string; groups_claim?: string; group_mappings?: Record<string, string>; endpoint_origins?: string[]; issuer: string; audience: string; jwks_url: string; authorization_url: string; token_url: string;
  client_id: string; redirect_url: string; scopes: string[]; roles_claim: string;
  role_mappings: Record<string, string>; session_ttl_seconds: number;
  id?: string; enabled?: boolean; client_secret_configured?: boolean;
};
type VerifiedIdentity = { issuer: string; subject: string; audience: string; user_id: string; organization_id?: string; roles: string[]; verified_at: number; approved: boolean };
type Settings = { verified_identity?: VerifiedIdentity; revision: number; active: Profile | null; draft: Profile | null; can_rollback: boolean; test_status: string; test_expires_at?: number; key_session: boolean };
type Binding = { issuer: string; audience: string; subject: string; user_id: string; enabled: boolean; allowed_models?: string[]; allowed_tools?: string[] };
const emptyProfile = (): Profile => ({ issuer: "", audience: "", jwks_url: "", authorization_url: "", token_url: "", client_id: "", redirect_url: `${window.location.origin}/auth/sso/callback`, scopes: ["openid", "profile", "email"], roles_claim: "roles", role_mappings: { "gateway-admin": "admin", "gateway-user": "user" }, session_ttl_seconds: 28800 });
const split = (value: string) => value.split(/[\s,]+/).filter(Boolean);
const message = (error: unknown) => error instanceof Error ? error.message : "SSO operation failed";

export function SSOSettingsPage() {
  const [connection, setConnection] = useState<Connection>({ id: "default", name: "Default", provider: "oidc" });
  const [reload, setReload] = useState(0);
  const [showAPI, setShowAPI] = useState(false);
  return <div className="sso-settings">
    <PageHeader eyebrow="System" title="Settings · Single sign-on" description="Configure browser OIDC connections and organization bindings. API JWT trust is managed independently." />
    <GatewayButton aria-expanded={showAPI} onClick={() => setShowAPI((value) => !value)}>Manage API JWT issuers</GatewayButton>
    {showAPI && <APIIssuers />}
    <SSOConnections selected={connection.id} reload={reload} onSelect={setConnection} />
    <SSOConnectionEditor key={connection.id} connection={connection} onChanged={() => setReload((n) => n + 1)} />
  </div>;
}

function SSOConnectionEditor({ connection, onChanged }: { connection: Connection; onChanged: () => void }) {
  const { client, session } = useAuth();
  const [settings, setSettings] = useState<Settings | null>(null);
  const [profile, setProfile] = useState<Profile>(emptyProfile);
  const [origins, setOrigins] = useState("");
  const [scopes, setScopes] = useState(emptyProfile().scopes.join(" "));
  const [groups, setGroups] = useState("{}");
  const path = (name: string) => `/admin/v1/sso/${name}${connection.id === "default" ? "" : `?connection=${encodeURIComponent(connection.id)}`}`;
  const [mapping, setMapping] = useState(JSON.stringify(emptyProfile().role_mappings, null, 2));
  const [secret, setSecret] = useState("");
  const [clearSecret, setClearSecret] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [testURL, setTestURL] = useState("");
  const [reload, setReload] = useState(0);
  const [now, setNow] = useState(Date.now);

  useEffect(() => {
    if (settings?.test_status !== "passed" || !settings.test_expires_at) return;
    const timer = window.setTimeout(() => setNow(Date.now()), Math.max(0, settings.test_expires_at * 1000 - Date.now() + 1));
    return () => window.clearTimeout(timer);
  }, [settings?.test_expires_at, settings?.test_status]);

  useEffect(() => {
    const controller = new AbortController();
    setError("");
    void client.request<Settings>(path("settings"), { signal: controller.signal }).then((data) => {
      if (controller.signal.aborted) return;
      setSettings(data);
      const selected = data.draft || data.active || { ...emptyProfile(), organization_id: connection.organization_id, role_mappings: connection.organization_id ? { "gateway-org-admin": "org_admin", "gateway-user": "user" } : emptyProfile().role_mappings };
      setProfile(selected); setOrigins((selected.endpoint_origins || []).join(", ")); setMapping(JSON.stringify(selected.role_mappings, null, 2));
      setScopes(selected.scopes.join(" ")); setGroups(JSON.stringify(selected.group_mappings || {}, null, 2));
      setSecret(""); setClearSecret(false); setDirty(false);
    }).catch((cause) => { if (!controller.signal.aborted) setError(message(cause)); });
    return () => controller.abort();
  }, [client, reload, connection.id, connection.organization_id]);

  const update = (key: keyof Profile, value: string | number | string[]) => { setProfile((current) => ({ ...current, [key]: value })); setDirty(true); };
  const operation = async (run: () => Promise<void>) => {
    setBusy(true); setError(""); setNotice("");
    try { await run(); } catch (cause) { setError(message(cause)); } finally { setBusy(false); }
  };
  const save = (event: FormEvent) => {
    event.preventDefault();
    void operation(async () => {
      const roles = parseMappings(mapping, "Role", Boolean(connection.organization_id));
      const groupMappings = parseMappings(groups, "Group", Boolean(connection.organization_id));
      if (Object.keys(groupMappings).length && !profile.groups_claim?.trim()) throw new Error("Group mappings require a groups claim path.");
      const { id: _id, enabled: _enabled, client_secret_configured: _configured, ...config } = profile;
      const data = await client.request<Settings>(path("settings"), { method: "PUT", body: { ...config, audience: profile.client_id, endpoint_origins: split(origins), scopes: split(scopes), role_mappings: roles, group_mappings: groupMappings, organization_id: connection.organization_id || undefined, expected_revision: settings!.revision, ...(clearSecret ? { client_secret: "" } : secret ? { client_secret: secret } : {}) } });
      onChanged(); setSettings(data); setProfile(data.draft!); setOrigins((data.draft?.endpoint_origins || []).join(", ")); setSecret(""); setClearSecret(false); setDirty(false); setTestURL(""); setNotice("Draft saved. Complete a test sign-in before activation.");
    });
  };
  const discover = () => void operation(async () => {
    const data = await client.request<Pick<Profile, "issuer" | "authorization_url" | "token_url" | "jwks_url">>("/admin/v1/sso/discover", { method: "POST", body: { issuer: profile.issuer, endpoint_origins: split(origins) } });
    setProfile((current) => ({ ...current, ...data })); setDirty(true); setNotice("Endpoints loaded. Review audience, scopes and role mappings before saving.");
  });
  const refreshTest = () => void operation(async () => { setSettings(await client.request<Settings>(path("settings"))); onChanged(); });
  const startTest = () => {
    const popup = window.open("about:blank", "_blank");
    if (popup) popup.opener = null;
    void operation(async () => {
      try {
        const data = await client.request<{ start_url: string }>(path("test"), { method: "POST", body: { expected_revision: settings!.revision } });
        setTestURL(data.start_url);
        if (popup) popup.location.replace(data.start_url);
        setSettings(await client.request<Settings>(path("settings")));
        setNotice("Sign in as the same internal administrator in the test tab, then refresh test status here.");
      } catch (cause) { popup?.close(); throw cause; }
    });
  };
  const action = (kind: "activate" | "disable" | "rollback") => void operation(async () => {
    const data = await client.request<Settings>(path("action"), { method: "POST", body: { action: kind, expected_revision: settings!.revision } });
    onChanged(); setSettings(data); setDirty(false); setTestURL(""); setSecret(""); setClearSecret(false);
    const selected = data.draft || data.active || { ...emptyProfile(), organization_id: connection.organization_id, role_mappings: connection.organization_id ? { "gateway-org-admin": "org_admin", "gateway-user": "user" } : emptyProfile().role_mappings }; setProfile(selected); setOrigins((selected.endpoint_origins || []).join(", ")); setMapping(JSON.stringify(selected.role_mappings, null, 2));
    setScopes(selected.scopes.join(" ")); setGroups(JSON.stringify(selected.group_mappings || {}, null, 2));
    setNotice(kind === "activate" ? "Browser SSO activated. API JWT trust remains unchanged." : kind === "disable" ? "Browser SSO disabled. API JWT trust remains unchanged." : "Previous browser configuration restored. API JWT trust remains unchanged.");
  });
  const verified = settings?.test_status === "passed" && (settings.test_expires_at || 0) > Math.max(now, Date.now()) / 1000;

  return <>

    {error && <ErrorState message={error} retry={() => setReload((value) => value + 1)} />}
    {notice && <p className="operation-result" role="status">{notice}</p>}
    {!settings && !error && <LoadingState />}
    {settings && <>
      <section className="form-card" aria-label="SSO status">
        <h2>{connection.name} · Current configuration</h2>
        <p>Organization: <strong>{connection.organization_id || "Platform"}</strong>. This binding cannot be changed after creation.</p>
        <p>{settings.active ? settings.active.enabled ? `Browser SSO enabled: ${settings.active.issuer}` : "Managed browser SSO disabled" : connection.id === "default" ? "No managed browser configuration. Save a draft and test sign-in before activation. API trust remains unchanged." : "This connection is not active. Save a draft and complete a test sign-in before activation."}</p>
        <p>Test status: <strong>{settings.test_status === "passed" && !verified ? "expired" : settings.test_status}</strong>{settings.test_expires_at ? ` · expires ${new Date(settings.test_expires_at * 1000).toLocaleString()}` : ""}</p>
        <p>Signed in as: <strong>{session?.user_id || "Unknown user"}</strong>. The test principal must map to this internal user and have approved directory roles. Organization-bound tests also require approved org_admin membership in this organization.</p>
        {!settings.key_session && <p role="note">Sign in with an administrator virtual key to activate, disable or roll back SSO.</p>}
        <p>Browser SSO settings are independent of API JWT trust. Activation, disable and rollback do not change API issuer, audience or role mappings. Virtual keys remain available.</p>
        <div className="page-actions">
          <GatewayButton disabled={busy || dirty || !settings.draft} onClick={startTest}>Test sign-in</GatewayButton>
          <GatewayButton disabled={busy} onClick={refreshTest}>Refresh test status</GatewayButton>
          <GatewayButton disabled={busy || dirty || !settings.draft || !settings.key_session || !verified} onClick={() => action("activate")}>Activate SSO</GatewayButton>
          <GatewayButton disabled={busy || !settings.key_session || !settings.active?.enabled} onClick={() => action("disable")}>Disable browser SSO</GatewayButton>
          <GatewayButton disabled={busy || !settings.key_session || !settings.can_rollback} onClick={() => action("rollback")}>Roll back</GatewayButton>
        </div>
        {testURL && <p><a href={testURL} target="_blank" rel="noopener noreferrer">Open test sign-in</a></p>}
      </section>
      <form className="form-card" onSubmit={save} aria-label="SSO configuration">
        <h2>Configuration draft</h2>
        <SSOPreset provider={connection.provider} disabled={busy} onApply={(preset) => { setProfile((current) => ({ ...current, ...preset, authorization_url: "", token_url: "", jwks_url: "", endpoint_origins: [] })); setOrigins(""); setDirty(true); }} />
        <fieldset disabled={busy} className="form-grid">
          <label>Issuer URL<input required type="url" value={profile.issuer} onChange={(event) => update("issuer", event.target.value)} /></label>
          <label>Trusted additional endpoint origins<input value={origins} onChange={(event) => { setOrigins(event.target.value); setDirty(true); }} placeholder="https://tokens.example, https://keys.example" /></label>
          <GatewayButton type="button" disabled={!profile.issuer} onClick={discover}>Discover endpoints</GatewayButton>
          {([ ["client_id", "Client ID"], ["authorization_url", "Authorization endpoint"], ["token_url", "Token endpoint"], ["jwks_url", "JWKS URL"], ["redirect_url", "Callback URL"], ["roles_claim", "Roles claim path"] ] as const).map(([key, label]) => <label key={key}>{label}<input required value={profile[key]} onChange={(event) => update(key, event.target.value)} /></label>)}
          <label>Scopes<input required value={scopes} onChange={(event) => { setScopes(event.target.value); setDirty(true); }} /></label>
          <label>Maximum session lifetime (seconds)<input required type="number" min="60" max="86400" value={profile.session_ttl_seconds} onChange={(event) => update("session_ttl_seconds", Number(event.target.value))} /></label>
          <SSOMappings kind="Role" value={mapping} tenant={Boolean(profile.organization_id || connection.organization_id)} onChange={(value) => { setMapping(value); setDirty(true); }} />
          <label>Groups claim path<input value={profile.groups_claim || ""} onChange={(event) => update("groups_claim", event.target.value)} placeholder="groups" /></label>
          <SSOMappings kind="Group" value={groups} tenant={Boolean(profile.organization_id || connection.organization_id)} onChange={(value) => { setGroups(value); setDirty(true); }} />
          <label>Client secret<input type="password" autoComplete="new-password" value={secret} disabled={clearSecret} placeholder={profile.client_secret_configured ? "Configured · leave blank to preserve" : "Optional for public clients"} onChange={(event) => { setSecret(event.target.value); setDirty(true); }} /></label>
          <label className="checkbox-line"><input type="checkbox" checked={clearSecret} onChange={(event) => { setClearSecret(event.target.checked); setSecret(""); setDirty(true); }} />Clear saved client secret</label>
        </fieldset>
        <p>Register both redirect URIs at your identity provider:</p>
        <p><code>{profile.redirect_url}</code><br /><code>{profile.redirect_url.replace(/\/auth\/sso\/callback$/, "/auth/sso/test/callback")}</code></p>
        <p>Use HTTPS in production. HTTP is allowed only for loopback testing. Browser identity uses a verified ID token for this client ID. Sessions are stored on the server and expire at the configured lifetime. When changing issuer or client ID, enter the new client secret or explicitly clear it.</p>
        <GatewayButton type="submit" disabled={busy}>Save draft</GatewayButton>
      </form>
      {settings.verified_identity && <section className="form-card" aria-label="Verified identity">
        <h2>Verified identity</h2>
        <p>{settings.verified_identity.approved ? "Directory approval verified." : "Token identity verified; directory approval is still required. This result does not permit activation or sign-in."}</p>
        <dl><dt>Issuer</dt><dd>{settings.verified_identity.issuer}</dd><dt>Subject (sub)</dt><dd>{settings.verified_identity.subject}</dd><dt>Audience</dt><dd>{settings.verified_identity.audience}</dd><dt>Verified at</dt><dd>{new Date(settings.verified_identity.verified_at * 1000).toLocaleString()}</dd><dt>Gateway user</dt><dd>{settings.verified_identity.user_id || "Not approved"}</dd><dt>Organization</dt><dd>{settings.verified_identity.organization_id || "Platform / not approved"}</dd><dt>Approved roles</dt><dd>{settings.verified_identity.roles.join(", ") || "None"}</dd></dl>
      </section>}
      <SSOPrincipalBinding profile={settings.draft || settings.active} identity={settings.verified_identity} />
    </>}
  </>;
}

function SSOPrincipalBinding({ profile, identity }: { profile: Profile | null; identity?: VerifiedIdentity }) {
  const { client, session } = useAuth();
  const [user, setUser] = useState(session?.user_id || "");
  const [subject, setSubject] = useState("");
  const [models, setModels] = useState("");
  const [tools, setTools] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState("");
  const [error, setError] = useState("");
  const save = (event: FormEvent) => {
    event.preventDefault(); if (!profile) return;
    setBusy(true); setResult(""); setError("");
    void client.request<Binding>("/admin/v1/jwt-principals", { method: "PUT", body: { issuer: profile.issuer, audience: profile.client_id, ...(profile.organization_id ? { organization_id: profile.organization_id } : {}), subject, user_id: user, enabled: true, allowed_models: split(models), allowed_tools: split(tools) } })
      .then(() => setResult("Principal binding saved. Permissions also require an active directory user and approved roles."))
      .catch((cause) => setError(message(cause))).finally(() => setBusy(false));
  };
  return <form className="form-card" aria-label="SSO principal binding" onSubmit={save}>
    <h2>Bind identity to a Gateway user</h2>
    <p>Create the user and assign approved roles in <Link to="/users">Users</Link>. Bind the exact subject from the verified ID token; email is not used as identity. Existing subject ownership cannot be reassigned.</p>
    <p>Issuer: {profile?.issuer || "Save a configuration draft first"} · Audience: {profile?.client_id || "—"}</p>
    {error && <ErrorState message={error} />}{result && <p role="status">{result}</p>}
    <fieldset className="form-grid" disabled={busy || !profile}>
      <label>Internal user ID<input required value={user} onChange={(event) => setUser(event.target.value)} /></label>
      <label>IdP subject (sub)<input required value={subject} onChange={(event) => setSubject(event.target.value)} /></label>
      {identity && identity.issuer === profile?.issuer && identity.audience === profile?.client_id && <GatewayButton type="button" onClick={() => setSubject(identity.subject)}>Use verified subject</GatewayButton>}
      <label>Allowed models<input value={models} onChange={(event) => setModels(event.target.value)} placeholder="Comma separated; empty denies inference" /></label>
      <label>Allowed tools<input value={tools} onChange={(event) => setTools(event.target.value)} placeholder="Comma separated; empty denies tool execution" /></label>
    </fieldset>
    <GatewayButton type="submit" disabled={busy || !profile}>Save principal binding</GatewayButton>
  </form>;
}
