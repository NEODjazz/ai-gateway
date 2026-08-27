import { useState, type FormEvent } from "react";
import { useAuth } from "../auth/AuthContext";

export function LoginPage() {
  const { signIn } = useAuth();
  const [token, setToken] = useState("");
  function submit(event: FormEvent) { event.preventDefault(); if (token.trim()) signIn(token); }
  return <main className="login-page"><section className="login-card"><div className="brand login-brand"><span className="brand-mark">AI</span><div><strong>Gateway Console</strong><small>Secure control plane</small></div></div><h1>Sign in</h1><p>Enter an admin bearer token. It is stored only in this browser tab.</p><form onSubmit={submit}><label>Admin bearer token<input autoFocus type="password" autoComplete="off" value={token} onChange={(event) => setToken(event.target.value)} /></label><button disabled={!token.trim()}>Open console</button></form><small>Provider credentials, prompts and responses are never loaded into the console.</small></section></main>;
}
