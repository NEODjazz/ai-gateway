import { useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { PageHeader } from "../components/PageHeader";
import { GatewayButton } from "../components/GatewayButton";

type Profile = {
  endpoint_origins?: string[]; issuer: string; audience: string; jwks_url: string; authorization_url: string; token_url: string;
  client_id: string; redirect_url: string; scopes: string[]; roles_claim: string;
  role_mappings: Record<string, string>; session_ttl_seconds: number;
  id?: string; enabled?: boolean; client_secret_configured?: boolean;
};
type Settings = { revision: number; active: Profile | null; draft: Profile | null; can_rollback: boolean; test_status: string; test_expires_at?: number; key_session: boolean };
type Binding = { issuer: string; audience: string; subject: string; user_id: string; enabled: boolean; allowed_models?: string[]; allowed_tools?: string[] };
const emptyProfile = (): Profile => ({ issuer: "", audience: "", jwks_url: "", authorization_url: "", token_url: "", client_id: "", redirect_url: `${window.location.origin}/auth/sso/callback`, scopes: ["openid", "profile", "email"], roles_claim: "roles", role_mappings: { "gateway-admin": "admin", "gateway-user": "user" }, session_ttl_seconds: 28800 });
const split = (value: string) => value.split(/[\s,]+/).filter(Boolean);
const message = (error: unknown) => error instanceof Error ? error.message : "SSO operation failed";

export function SSOSettingsPage() {
  const { client, session } = useAuth();
  const [settings, setSettings] = useState<Settings | null>(null);
  const [profile, setProfile] = useState<Profile>(emptyProfile);
  const [origins, setOrigins] = useState("");
  const [scopes, setScopes] = useState(emptyProfile().scopes.join(" "));
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
    void client.request<Settings>("/admin/v1/sso/settings", { signal: controller.signal }).then((data) => {
      if (controller.signal.aborted) return;
      setSettings(data);
      const selected = data.draft || data.active || emptyProfile();
      setProfile(selected); setOrigins((selected.endpoint_origins || []).join(", ")); setMapping(JSON.stringify(selected.role_mappings, null, 2));
      setScopes(selected.scopes.join(" "));
      setSecret(""); setClearSecret(false); setDirty(false);
    }).catch((cause) => { if (!controller.signal.aborted) setError(message(cause)); });
    return () => controller.abort();
  }, [client, reload]);

  const update = (key: keyof Profile, value: string | number | string[]) => { setProfile((current) => ({ ...current, [key]: value })); setDirty(true); };
  const operation = async (run: () => Promise<void>) => {
    setBusy(true); setError(""); setNotice("");
    try { await run(); } catch (cause) { setError(message(cause)); } finally { setBusy(false); }
  };
  const save = (event: FormEvent) => {
    event.preventDefault();
    void operation(async () => {
      let roles: unknown;
      try { roles = JSON.parse(mapping); } catch { throw new Error("Role mappings must be a JSON object."); }
      if (!roles || Array.isArray(roles) || typeof roles !== "object" || !Object.values(roles).every((role) => typeof role === "string")) throw new Error("Role mappings must map IdP roles to Gateway roles.");
      const { id: _id, enabled: _enabled, client_secret_configured: _configured, ...config } = profile;
      const data = await client.request<Settings>("/admin/v1/sso/settings", { method: "PUT", body: { ...config, audience: profile.client_id, endpoint_origins: split(origins), scopes: split(scopes), role_mappings: roles, expected_revision: settings!.revision, ...(clearSecret ? { client_secret: "" } : secret ? { client_secret: secret } : {}) } });
      setSettings(data); setProfile(data.draft!); setOrigins((data.draft?.endpoint_origins || []).join(", ")); setSecret(""); setClearSecret(false); setDirty(false); setTestURL(""); setNotice("Draft saved. Complete a test sign-in before activation.");
    });
  };
  const discover = () => void operation(async () => {
    const data = await client.request<Pick<Profile, "issuer" | "authorization_url" | "token_url" | "jwks_url">>("/admin/v1/sso/discover", { method: "POST", body: { issuer: profile.issuer, endpoint_origins: split(origins) } });
    setProfile((current) => ({ ...current, ...data })); setDirty(true); setNotice("Endpoints loaded. Review audience, scopes and role mappings before saving.");
  });
  const refreshTest = () => void operation(async () => { setSettings(await client.request<Settings>("/admin/v1/sso/settings")); });
  const startTest = () => {
    const popup = window.open("about:blank", "_blank");
    if (popup) popup.opener = null;
    void operation(async () => {
      try {
        const data = await client.request<{ start_url: string }>("/admin/v1/sso/test", { method: "POST", body: { expected_revision: settings!.revision } });
        setTestURL(data.start_url);
        if (popup) popup.location.replace(data.start_url);
        setSettings(await client.request<Settings>("/admin/v1/sso/settings"));
        setNotice("Sign in as the same internal administrator in the test tab, then refresh test status here.");
      } catch (cause) { popup?.close(); throw cause; }
    });
  };
  const action = (kind: "activate" | "disable" | "rollback") => void operation(async () => {
    const data = await client.request<Settings>("/admin/v1/sso/action", { method: "POST", body: { action: kind, expected_revision: settings!.revision } });
    setSettings(data); setDirty(false); setTestURL(""); setSecret(""); setClearSecret(false);
    const selected = data.draft || data.active || emptyProfile(); setProfile(selected); setOrigins((selected.endpoint_origins || []).join(", ")); setMapping(JSON.stringify(selected.role_mappings, null, 2));
    setScopes(selected.scopes.join(" "));
    setNotice(kind === "activate" ? "Browser SSO activated. API JWT trust remains unchanged." : kind === "disable" ? "Browser SSO disabled. API JWT trust remains unchanged." : "Previous browser configuration restored. API JWT trust remains unchanged.");
  });
  const verified = settings?.test_status === "passed" && (settings.test_expires_at || 0) > Math.max(now, Date.now()) / 1000;

  return <>
    <PageHeader eyebrow="System" title="Settings · Single sign-on" description="Configure browser OIDC sign-in. Save a draft, verify administrator access and activate it with a virtual key." />
    {error && <ErrorState message={error} retry={() => setReload((value) => value + 1)} />}
    {notice && <p className="operation-result" role="status">{notice}</p>}
    {!settings && !error && <LoadingState />}
    {settings && <>
      <section className="form-card" aria-label="SSO status">
        <h2>Current configuration</h2>
        <p>{settings.active ? settings.active.enabled ? `Browser SSO enabled: ${settings.active.issuer}` : "Managed browser SSO disabled" : "No managed configuration. Existing environment settings remain in use."}</p>
        <p>Test status: <strong>{settings.test_status === "passed" && !verified ? "expired" : settings.test_status}</strong>{settings.test_expires_at ? ` · expires ${new Date(settings.test_expires_at * 1000).toLocaleString()}` : ""}</p>
        <p>Signed in as: <strong>{session?.user_id || "Unknown user"}</strong>. The test principal must map to this internal user and have the admin role in Users.</p>
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
        <fieldset disabled={busy} className="form-grid">
          <label>Issuer URL<input required type="url" value={profile.issuer} onChange={(event) => update("issuer", event.target.value)} /></label>
          <label>Trusted additional endpoint origins<input value={origins} onChange={(event) => { setOrigins(event.target.value); setDirty(true); }} placeholder="https://tokens.example, https://keys.example" /></label>
          <GatewayButton type="button" disabled={!profile.issuer} onClick={discover}>Discover endpoints</GatewayButton>
          {([ ["client_id", "Client ID"], ["authorization_url", "Authorization endpoint"], ["token_url", "Token endpoint"], ["jwks_url", "JWKS URL"], ["redirect_url", "Callback URL"], ["roles_claim", "Roles claim path"] ] as const).map(([key, label]) => <label key={key}>{label}<input required value={profile[key]} onChange={(event) => update(key, event.target.value)} /></label>)}
          <label>Scopes<input required value={scopes} onChange={(event) => { setScopes(event.target.value); setDirty(true); }} /></label>
          <label>Maximum session lifetime (seconds)<input required type="number" min="60" max="86400" value={profile.session_ttl_seconds} onChange={(event) => update("session_ttl_seconds", Number(event.target.value))} /></label>
          <label>Role mappings (JSON)<textarea required rows={5} value={mapping} onChange={(event) => { setMapping(event.target.value); setDirty(true); }} /></label>
          <label>Client secret<input type="password" autoComplete="new-password" value={secret} disabled={clearSecret} placeholder={profile.client_secret_configured ? "Configured · leave blank to preserve" : "Optional for public clients"} onChange={(event) => { setSecret(event.target.value); setDirty(true); }} /></label>
          <label className="checkbox-line"><input type="checkbox" checked={clearSecret} onChange={(event) => { setClearSecret(event.target.checked); setSecret(""); setDirty(true); }} />Clear saved client secret</label>
        </fieldset>
        <p>Register both redirect URIs at your identity provider:</p>
        <p><code>{profile.redirect_url}</code><br /><code>{profile.redirect_url.replace(/\/auth\/sso\/callback$/, "/auth/sso/test/callback")}</code></p>
        <p>Use HTTPS in production. HTTP is allowed only for loopback testing. Browser identity uses a verified ID token for this client ID. Sessions are stored on the server and expire at the configured lifetime. When changing issuer or client ID, enter the new client secret or explicitly clear it.</p>
        <GatewayButton type="submit" disabled={busy}>Save draft</GatewayButton>
      </form>
      <SSOPrincipalBinding profile={settings.draft || settings.active} />
    </>}
  </>;
}

function SSOPrincipalBinding({ profile }: { profile: Profile | null }) {
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
    void client.request<Binding>("/admin/v1/jwt-principals", { method: "PUT", body: { issuer: profile.issuer, audience: profile.client_id, subject, user_id: user, enabled: true, allowed_models: split(models), allowed_tools: split(tools) } })
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
      <label>Allowed models<input value={models} onChange={(event) => setModels(event.target.value)} placeholder="Comma separated; empty denies inference" /></label>
      <label>Allowed tools<input value={tools} onChange={(event) => setTools(event.target.value)} placeholder="Comma separated; empty denies tool execution" /></label>
    </fieldset>
    <GatewayButton type="submit" disabled={busy || !profile}>Save principal binding</GatewayButton>
  </form>;
}
