import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { useAuth } from "../auth/AuthContext";
import { ErrorState, LoadingState } from "../components/AsyncState";

type Limits = { rpm: number; tpm: number };
type Settings = { limits: Limits; defaults: Limits; source: "environment" | "configuration" };
const path = "/admin/v1/settings/global-rate-limits";

export function GlobalRateLimitsForm() {
  const { client } = useAuth();
  const [settings, setSettings] = useState<Settings>();
  const [rpm, setRPM] = useState("");
  const [tpm, setTPM] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const generation = useRef(0);
  const apply = useCallback((value: Settings) => {
    if (!value?.limits || !value.defaults) throw new Error("Global rate-limit settings are unavailable.");
    setSettings(value); setRPM(String(value.limits.rpm)); setTPM(String(value.limits.tpm));
  }, []);
  const load = useCallback(async () => {
    const current = ++generation.current;
    setBusy(true); setError("");
    try { const value = await client.request<Settings>(path); if (current === generation.current) apply(value); }
    catch (cause) { if (current === generation.current) setError(cause instanceof Error ? cause.message : "Could not load global limits"); }
    finally { if (current === generation.current) setBusy(false); }
  }, [client, apply]);
  useEffect(() => { void load(); return () => { ++generation.current; }; }, [load]);
  async function save(method: "PUT" | "DELETE") {
    setBusy(true); setError(""); setNotice("");
    const current = ++generation.current;
    try {
      const value = await client.request<Settings>(path, { method, ...(method === "PUT" ? { body: { rpm: Number(rpm), tpm: Number(tpm) } } : {}) });
      if (current === generation.current) { apply(value); setNotice(method === "PUT" ? "Global limits saved." : "Helm / ENV defaults restored."); }
    } catch (cause) { if (current === generation.current) setError(cause instanceof Error ? cause.message : "Could not save global limits"); }
    finally { if (current === generation.current) setBusy(false); }
  }
  function submit(event: FormEvent) { event.preventDefault(); void save("PUT"); }
  return <section className="form-card section-block global-rate-limits" aria-label="Global rate limits">
    <h2>Global rate limits</h2>
    <p>Aggregate inference traffic across users, teams and models. Credential, provider and deployment limits also apply. TPM reserves input context plus maximum output tokens.</p>
    {error && <ErrorState message={error} retry={() => void load()} />}
    {notice && <p role="status">{notice}</p>}
    {!settings ? busy && <LoadingState /> : <form onSubmit={submit}>
      <fieldset disabled={busy} className="form-grid">
        <label>Global requests per minute (RPM)<input type="number" required min="0" max="10000000" step="1" value={rpm} onChange={(event) => setRPM(event.target.value)} /></label>
        <label>Global tokens per minute (TPM)<input type="number" required min="0" max="1000000000" step="1" value={tpm} onChange={(event) => setTPM(event.target.value)} /></label>
      </fieldset>
      <p className="muted">Zero disables that limit. Source: {settings.source === "configuration" ? "saved configuration" : "Helm / ENV"}. Helm / ENV defaults: {settings.defaults.rpm.toLocaleString()} RPM · {settings.defaults.tpm.toLocaleString()} TPM. Saved configuration takes precedence until reset.</p>
      <div className="inline-actions"><button disabled={busy}>Save global limits</button><button type="button" className="secondary" disabled={busy || settings.source === "environment"} onClick={() => void save("DELETE")}>Use Helm / ENV defaults</button></div>
    </form>}
  </section>;
}
