import { useState, type FormEvent } from "react";
import { TextInput } from "@gravity-ui/uikit";
import { loginConnectionURL } from "../auth/ssoConnections";
import { useAuth } from "../auth/AuthContext";
import { GatewayButton } from "../components/GatewayButton";
import { GravityThemeScope } from "../components/GravityThemeScope";

export function LoginPage() {
  const { signIn, ssoEnabled, ssoConnections } = useAuth();
  const [token, setToken] = useState("");
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!token.trim()) return;
    setSubmitting(true); setError("");
    try { await signIn(token); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not validate this credential"); }
    finally { setSubmitting(false); }
  }
  return <main className="login-page"><section className="login-card"><div className="brand login-brand"><span className="brand-mark">AI</span><div><strong>Gateway Console</strong><small>Secure control plane</small></div></div><h1>Sign in</h1>{ssoEnabled && <>{ssoConnections.length ? <div aria-label="Organization sign-in">{ssoConnections.map((connection) => <a className="sso-login" key={connection.id} href={loginConnectionURL(connection)}>Continue with {connection.name}{connection.organization_id ? ` · ${connection.organization_id}` : " · Platform"}</a>)}<p>Each connection verifies your identity and organization access before starting a session.</p></div> : <a className="sso-login" href="/auth/sso/start">Continue with SSO</a>}<div className="login-separator"><span>or use a gateway token</span></div></>}<p>Enter a gateway bearer token. It is validated before being stored in this browser tab.</p><form onSubmit={submit}><label htmlFor="gateway-token">Gateway bearer token<GravityThemeScope className="gravity-login-control"><TextInput id="gateway-token" controlProps={{ "aria-label": "Gateway bearer token" }} autoFocus={!ssoEnabled} type="password" autoComplete="off" size="xl" value={token} onUpdate={setToken} /></GravityThemeScope></label>{error && <p className="form-error" role="alert">{error}</p>}<GatewayButton type="submit" size="xl" disabled={!token.trim() || submitting}>{submitting ? "Validating…" : "Open console"}</GatewayButton></form><small>Provider credentials, prompts and responses are never loaded into the console.</small></section></main>;
}
