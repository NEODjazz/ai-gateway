import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { PageHeader } from "../components/PageHeader";
import { GatewayButton } from "../components/GatewayButton";
import { SSOConnections, type Connection } from "./sso/SSOConnections";
import { SSOMappings, parseMappings } from "./sso/SSOMappings";
import { APIIssuers } from "./sso/APIIssuers";
import { PageTabs } from "../components/PageTabs";
import { SSODialog } from "./sso/SSODialog";
import { CopyValue, MappingSummary, providerName } from "./sso/SSODetails";
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
  const [tab, setTab] = useState<"browser" | "api">("browser");
  return <div className="sso-settings">
    <PageHeader eyebrow="System" title="Settings · Single sign-on" description="Configure browser OIDC connections and organization bindings. API JWT trust is managed independently." />
    <PageTabs label="Identity settings" value={tab} items={[{ value: "browser", label: "Browser SSO" }, { value: "api", label: "API JWT" }]} onUpdate={setTab} />
    {tab === "api" ? <APIIssuers /> : <>
      <SSOConnections selected={connection.id} reload={reload} onSelect={setConnection} />
      <SSOConnectionEditor key={connection.id} connection={connection} onChanged={() => setReload((n) => n + 1)} />
    </>}
  </div>;
}

function SSOConnectionEditor({ connection, onChanged }: { connection: Connection; onChanged: () => void }) {
  const { client, session } = useAuth();
  const [editing, setEditing] = useState(false);
  const pending = useRef<AbortController | null>(null);
  useEffect(() => () => pending.current?.abort(), []);
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

  const reset = (data: Settings) => {
    const selected = data.draft || data.active || { ...emptyProfile(), organization_id: connection.organization_id, role_mappings: connection.organization_id ? { "gateway-org-admin": "org_admin", "gateway-user": "user" } : emptyProfile().role_mappings };
    setProfile(selected); setOrigins((selected.endpoint_origins || []).join(", ")); setMapping(JSON.stringify(selected.role_mappings, null, 2));
    setScopes(selected.scopes.join(" ")); setGroups(JSON.stringify(selected.group_mappings || {}, null, 2));
    setSecret(""); setClearSecret(false); setDirty(false);
  };
  const closeEditor = () => { if (settings) reset(settings); setEditing(false); setError(""); };
  const update = (key: keyof Profile, value: string | number | string[]) => { setProfile((current) => ({ ...current, [key]: value })); setDirty(true); };
  const operation = async (run: (signal: AbortSignal) => Promise<void>) => {
    const controller = new AbortController(); pending.current?.abort(); pending.current = controller;
    setBusy(true); setError(""); setNotice("");
    try { await run(controller.signal); } catch (cause) { if (!controller.signal.aborted) setError(message(cause)); } finally { if (!controller.signal.aborted) setBusy(false); }
  };
  const save = (event: FormEvent) => {
    event.preventDefault();
    void operation(async (signal) => {
      const roles = parseMappings(mapping, "Role", Boolean(profile.organization_id || connection.organization_id));
      const groupMappings = parseMappings(groups, "Group", Boolean(profile.organization_id || connection.organization_id));
      if (Object.keys(groupMappings).length && !profile.groups_claim?.trim()) throw new Error("Group mappings require a groups claim path.");
      const { id: _id, enabled: _enabled, client_secret_configured: _configured, ...config } = profile;
      const data = await client.request<Settings>(path("settings"), { method: "PUT", signal, body: { ...config, audience: profile.client_id, endpoint_origins: split(origins), scopes: split(scopes), role_mappings: roles, group_mappings: groupMappings, organization_id: connection.organization_id || profile.organization_id || undefined, expected_revision: settings!.revision, ...(clearSecret ? { client_secret: "" } : secret ? { client_secret: secret } : {}) } });
      if (signal.aborted) return;
      onChanged(); setSettings(data); reset(data); setEditing(false); setTestURL(""); setNotice("Draft saved. Complete a test sign-in before activation.");
    });
  };
  const discover = () => void operation(async (signal) => {
    const data = await client.request<Pick<Profile, "issuer" | "authorization_url" | "token_url" | "jwks_url">>("/admin/v1/sso/discover", { method: "POST", signal, body: { issuer: profile.issuer, endpoint_origins: split(origins) } });
    if (signal.aborted) return;
    setProfile((current) => ({ ...current, ...data })); setDirty(true); setNotice("Endpoints loaded. Review audience, scopes and role mappings before saving.");
  });
  const refreshTest = () => void operation(async (signal) => { const data = await client.request<Settings>(path("settings"), { signal }); if (!signal.aborted) { setSettings(data); onChanged(); } });
  const startTest = () => {
    const popup = window.open("about:blank", "_blank");
    if (popup) popup.opener = null;
    void operation(async (signal) => {
      try {
        const data = await client.request<{ start_url: string }>(path("test"), { method: "POST", signal, body: { expected_revision: settings!.revision } });
        if (signal.aborted) { popup?.close(); return; }
        setTestURL(data.start_url);
        if (popup) popup.location.replace(data.start_url);
        const refreshed = await client.request<Settings>(path("settings"), { signal });
        if (signal.aborted) return;
        setSettings(refreshed);
        setNotice("Sign in as the same internal administrator in the test tab, then refresh test status here.");
      } catch (cause) { popup?.close(); throw cause; }
    });
  };
  const action = (kind: "activate" | "disable" | "rollback") => void operation(async (signal) => {
    const data = await client.request<Settings>(path("action"), { method: "POST", signal, body: { action: kind, expected_revision: settings!.revision } });
    if (signal.aborted) return;
    onChanged(); setSettings(data); reset(data); setTestURL("");
    setNotice(kind === "activate" ? "Browser SSO activated. API JWT trust remains unchanged." : kind === "disable" ? "Browser SSO disabled. API JWT trust remains unchanged." : "Previous browser configuration restored. API JWT trust remains unchanged.");
  });
  const verified = settings?.test_status === "passed" && (settings.test_expires_at || 0) > Math.max(now, Date.now()) / 1000;

  const saved = settings?.draft || settings?.active;
  return <>

    {!editing && error && <ErrorState message={error} retry={() => setReload((value) => value + 1)} />}
    {notice && <p className="operation-result" role="status">{notice}</p>}
    {!settings && !error && <LoadingState />}
    {settings && <>
      <section className="form-card" aria-label="SSO status">
        <div className="sso-card-heading"><div><h2>SSO configuration</h2><span>{connection.name} · {providerName(connection.provider)}</span></div>
          <GatewayButton onClick={() => { reset(settings); setEditing(true); setError(""); }}>{saved ? "Edit SSO settings" : "Set up SSO"}</GatewayButton>
        </div>
        <p>Organization: <strong>{connection.organization_id || saved?.organization_id || "Platform"}</strong>. This binding cannot be changed after creation.</p>
        <p>{settings.active ? settings.active.enabled ? `Browser SSO enabled: ${settings.active.issuer}` : "Managed browser SSO disabled" : connection.id === "default" ? "No managed browser configuration. Save a draft and test sign-in before activation. API trust remains unchanged." : "This connection is not active. Save a draft and complete a test sign-in before activation."}</p>
        {saved ? <>
          <p className="sso-config-source">{settings.draft ? "Saved draft · not yet active" : settings.active?.enabled ? "Active profile" : "Saved profile · disabled"} · Revision {settings.revision}</p>
          {settings.draft && settings.active && <p>Current active issuer: <code>{settings.active.issuer}</code>. The draft below has not changed the active profile.</p>}
          <dl className="sso-details"><dt>Issuer</dt><dd>{saved.issuer}</dd><dt>Client ID</dt><dd>{saved.client_id}</dd><dt>Client secret</dt><dd>{saved.client_secret_configured ? "Configured" : "Not configured"}</dd><dt>Scopes</dt><dd>{saved.scopes.join(" ")}</dd><dt>Session lifetime</dt><dd>{saved.session_ttl_seconds} seconds</dd><dt>Login callback</dt><dd><CopyValue label="login callback" value={saved.redirect_url} /></dd><dt>Test callback</dt><dd><CopyValue label="test callback" value={saved.redirect_url.replace(/\/auth\/sso\/callback$/, "/auth/sso/test/callback")} /></dd></dl>
          <details><summary>OIDC endpoints</summary><dl className="sso-details"><dt>Authorization</dt><dd>{saved.authorization_url}</dd><dt>Token</dt><dd>{saved.token_url}</dd><dt>JWKS</dt><dd>{saved.jwks_url}</dd><dt>Additional origins</dt><dd>{saved.endpoint_origins?.join(", ") || "None"}</dd></dl></details>
        </> : <p>Select a provider, save a draft and complete a test sign-in to enable browser login.</p>}
        <p>Test status: <strong>{settings.test_status === "passed" && !verified ? "expired" : settings.test_status}</strong>{settings.test_expires_at ? ` · expires ${new Date(settings.test_expires_at * 1000).toLocaleString()}` : ""}</p>
        <details><summary>Sign-in and recovery requirements</summary>
        <p>Signed in as: <strong>{session?.user_id || "Unknown user"}</strong>. The test principal must map to this internal user and have approved directory roles. Organization-bound tests also require approved org_admin membership in this organization.</p>
        {!settings.key_session && <p role="note">Sign in with an administrator virtual key to activate, disable or roll back SSO.</p>}
        <p>Browser SSO settings are independent of API JWT trust. Activation, disable and rollback do not change API issuer, audience or role mappings. Virtual keys remain available.</p></details>
        <div className="page-actions">
          <GatewayButton disabled={busy || dirty || !settings.draft} onClick={startTest}>Test sign-in</GatewayButton>
          <GatewayButton disabled={busy} onClick={refreshTest}>Refresh test status</GatewayButton>
          <GatewayButton disabled={busy || dirty || !settings.draft || !settings.key_session || !verified} onClick={() => action("activate")}>Activate SSO</GatewayButton>
          <GatewayButton disabled={busy || !settings.key_session || !settings.active?.enabled} onClick={() => action("disable")}>Disable browser SSO</GatewayButton>
          <GatewayButton disabled={busy || !settings.key_session || !settings.can_rollback} onClick={() => action("rollback")}>Roll back</GatewayButton>
        </div>
        {testURL && <p><a href={testURL} target="_blank" rel="noopener noreferrer">Open test sign-in</a></p>}
      </section>
      {saved && <MappingSummary roles={saved.role_mappings} groups={saved.group_mappings} rolesClaim={saved.roles_claim} groupsClaim={saved.groups_claim} />}
      {editing && <SSODialog title={saved ? "Edit SSO settings" : "Set up SSO"} busy={busy} dirty={dirty} onClose={closeEditor}>
      <form className="sso-edit-form" onSubmit={save} aria-label="SSO configuration">
        <p>Save a draft to review and test it before activation. Organization: <strong>{connection.organization_id || profile.organization_id || "Platform"}</strong>.</p>
        {error && <ErrorState message={error} />}
        <h3>Identity provider</h3>
        <SSOPreset provider={connection.provider} disabled={busy} onApply={(preset) => { setProfile((current) => ({ ...current, ...preset, authorization_url: "", token_url: "", jwks_url: "", endpoint_origins: [] })); setOrigins(""); setDirty(true); }} />
        <fieldset disabled={busy} className="form-grid">
          <label>Issuer URL<input required type="url" value={profile.issuer} onChange={(event) => update("issuer", event.target.value)} /></label>
          <GatewayButton type="button" disabled={!profile.issuer} onClick={discover}>Discover endpoints</GatewayButton>
          <label>Client ID<input required value={profile.client_id} onChange={(event) => update("client_id", event.target.value)} /></label>
          <label>Client secret<input type="password" autoComplete="new-password" value={secret} disabled={clearSecret} placeholder={profile.client_secret_configured ? "Configured · leave blank to preserve" : "Optional for public clients"} onChange={(event) => { setSecret(event.target.value); setDirty(true); }} /></label>
          <label className="checkbox-line"><input type="checkbox" checked={clearSecret} onChange={(event) => { setClearSecret(event.target.checked); setSecret(""); setDirty(true); }} />Clear saved client secret</label>
          <label>Callback URL<input required value={profile.redirect_url} onChange={(event) => update("redirect_url", event.target.value)} /></label>
          <details className="sso-advanced" open={!profile.authorization_url || !profile.token_url || !profile.jwks_url}><summary>Advanced OIDC endpoints</summary><div className="form-grid">
            {([ ["authorization_url", "Authorization endpoint"], ["token_url", "Token endpoint"], ["jwks_url", "JWKS URL"] ] as const).map(([key, label]) => <label key={key}>{label}<input required value={profile[key]} onChange={(event) => update(key, event.target.value)} /></label>)}
            <label>Trusted additional endpoint origins<input value={origins} onChange={(event) => { setOrigins(event.target.value); setDirty(true); }} placeholder="https://tokens.example, https://keys.example" /></label>
          </div></details>
          <label>Scopes<input required value={scopes} onChange={(event) => { setScopes(event.target.value); setDirty(true); }} /></label>
          <label>Maximum session lifetime (seconds)<input required type="number" min="60" max="86400" value={profile.session_ttl_seconds} onChange={(event) => update("session_ttl_seconds", Number(event.target.value))} /></label>
          <h3>Role and group mappings</h3>
          <label>Roles claim path<input required value={profile.roles_claim} onChange={(event) => update("roles_claim", event.target.value)} /></label>
          <SSOMappings kind="Role" value={mapping} tenant={Boolean(profile.organization_id || connection.organization_id)} onChange={(value) => { setMapping(value); setDirty(true); }} />
          <label>Groups claim path<input value={profile.groups_claim || ""} onChange={(event) => update("groups_claim", event.target.value)} placeholder="groups" /></label>
          <SSOMappings kind="Group" value={groups} tenant={Boolean(profile.organization_id || connection.organization_id)} onChange={(value) => { setGroups(value); setDirty(true); }} />
        </fieldset>
        <p>Register both redirect URIs at your identity provider:</p>
        <p><code>{profile.redirect_url}</code><br /><code>{profile.redirect_url.replace(/\/auth\/sso\/callback$/, "/auth/sso/test/callback")}</code></p>
        <p>Use HTTPS in production. HTTP is allowed only for loopback testing. Browser identity uses a verified ID token for this client ID. Sessions are stored on the server and expire at the configured lifetime. When changing issuer or client ID, enter the new client secret or explicitly clear it.</p>
        <div className="modal-actions"><GatewayButton type="submit" disabled={busy}>Save draft</GatewayButton></div>
      </form></SSODialog>}
      {settings.verified_identity && <section className="form-card" aria-label="Verified identity">
        <h2>Verified identity</h2>
        <p>{settings.verified_identity.approved ? "Directory approval verified." : "Token identity verified; directory approval is still required. This result does not permit activation or sign-in."}</p>
        <dl className="sso-details"><dt>Issuer</dt><dd>{settings.verified_identity.issuer}</dd><dt>Subject (sub)</dt><dd>{settings.verified_identity.subject}</dd><dt>Audience</dt><dd>{settings.verified_identity.audience}</dd><dt>Verified at</dt><dd>{new Date(settings.verified_identity.verified_at * 1000).toLocaleString()}</dd><dt>Gateway user</dt><dd>{settings.verified_identity.user_id || "Not approved"}</dd><dt>Organization</dt><dd>{settings.verified_identity.organization_id || "Platform / not approved"}</dd><dt>Approved roles</dt><dd>{settings.verified_identity.roles.join(", ") || "None"}</dd></dl>
      </section>}
      <SSOPrincipalBinding profile={settings.draft || settings.active} identity={settings.verified_identity} />
    </>}
  </>;
}

function SSOPrincipalBinding({ profile, identity }: { profile: Profile | null; identity?: VerifiedIdentity }) {
  const { client, session } = useAuth();
  const [open, setOpen] = useState(false);
  const [dirty, setDirty] = useState(false);
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
      .then(() => { setDirty(false); setResult("Principal binding saved. Permissions also require an active directory user and approved roles."); })
      .catch((cause) => setError(message(cause))).finally(() => setBusy(false));
  };
  return <section className="form-card">
    <div className="sso-card-heading"><h2>Directory identity</h2><GatewayButton view="normal" disabled={!profile} onClick={() => setOpen(true)}>Bind identity to a Gateway user</GatewayButton></div>
    <p>Manage approved users and roles in <Link to="/users">Users</Link>. An exact subject binding is required for sign-in; email does not grant access.</p>
    {open && <SSODialog title="Bind identity to a Gateway user" busy={busy} dirty={dirty} onClose={() => { setOpen(false); setSubject(""); setModels(""); setTools(""); setDirty(false); setError(""); setResult(""); }}>
    <form className="sso-edit-form" aria-label="SSO principal binding" onSubmit={save}>
    <p>Create the user and assign approved roles in <Link to="/users">Users</Link>. Bind the exact subject from the verified ID token; email is not used as identity. Existing subject ownership cannot be reassigned.</p>
    <p>Issuer: {profile?.issuer || "Save a configuration draft first"} · Audience: {profile?.client_id || "—"}</p>
    {error && <ErrorState message={error} />}{result && <p role="status">{result}</p>}
    <fieldset className="form-grid" disabled={busy || !profile}>
      <label>Internal user ID<input required value={user} onChange={(event) => { setUser(event.target.value); setDirty(true); }} /></label>
      <label>IdP subject (sub)<input required value={subject} onChange={(event) => { setSubject(event.target.value); setDirty(true); }} /></label>
      {identity && identity.issuer === profile?.issuer && identity.audience === profile?.client_id && <GatewayButton type="button" onClick={() => { setSubject(identity.subject); setDirty(true); }}>Use verified subject</GatewayButton>}
      <label>Allowed models<input value={models} onChange={(event) => { setModels(event.target.value); setDirty(true); }} placeholder="Comma separated; empty denies inference" /></label>
      <label>Allowed tools<input value={tools} onChange={(event) => { setTools(event.target.value); setDirty(true); }} placeholder="Comma separated; empty denies tool execution" /></label>
    </fieldset>
    <GatewayButton type="submit" disabled={busy || !profile}>Save principal binding</GatewayButton>
  </form></SSODialog>}
  </section>;
}
