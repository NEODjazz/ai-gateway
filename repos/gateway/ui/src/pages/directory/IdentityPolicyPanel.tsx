import { useCallback, useMemo, useRef, useState, type FormEvent } from "react";
import { useAuth } from "../../auth/AuthContext";
import { ErrorState, LoadingState } from "../../components/AsyncState";
import { ChipMultiSelect } from "../../components/ChipMultiSelect";
import { ModalFrame } from "../../components/ModalFrame";
import { useDirectoryResource } from "./useDirectoryResource";

type Policy = { scope_type: "team" | "user"; scope_id: string; model_access: "inherit" | "allowlist" | "deny"; allowed_models: string[]; rpm: number; tpm: number };
function validModelCatalog(value: unknown) {
  const models = (value as { models?: unknown })?.models;
  return Array.isArray(models) && models.every((row) => row && typeof row.model === "string");
}

type Settings = { policy: Policy; configured: boolean; durable: boolean };

function PolicyForm({ initial, onClose, onSave }: { initial: Policy; onClose: () => void; onSave: (value: Policy) => Promise<void> }) {
  const [mode, setMode] = useState(initial.model_access);
  const [models, setModels] = useState(initial.allowed_models);
  const [rpm, setRPM] = useState(String(initial.rpm));
  const [tpm, setTPM] = useState(String(initial.tpm));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const catalog = useDirectoryResource<{ models: { model: string }[] }>("/admin/v1/model-catalog", undefined, validModelCatalog);
  const options = useMemo(() => [...new Set([...(catalog.value?.models || []).map((row) => row.model), ...initial.allowed_models])].filter(Boolean).map((value) => ({ value, label: value })), [catalog.value, initial.allowed_models]);
  async function submit(event: FormEvent) {
    event.preventDefault(); if (saving) return;
    const requests = Number(rpm), tokens = Number(tpm);
    if (!rpm.trim() || !tpm.trim() || !Number.isSafeInteger(requests) || requests < 0 || requests > 10_000_000 || !Number.isSafeInteger(tokens) || tokens < 0 || tokens > 1_000_000_000) { setError("RPM must be 0–10000000 and TPM 0–1000000000, using whole numbers."); return; }
    if (mode === "allowlist" && (!models.length || models.length > 100 || models.some((model) => model.length > 512 || /[\x00-\x1f\x7f]/.test(model) || /[*?]/.test(model.replace(/\*$/, ""))))) { setError("Select 1–100 models or patterns. A wildcard is supported only at the end."); return; }
    setSaving(true); setError("");
    try { await onSave({ ...initial, model_access: mode, allowed_models: mode === "allowlist" ? models : [], rpm: requests, tpm: tokens }); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not save identity policy"); }
    finally { setSaving(false); }
  }
  return <ModalFrame label="Edit models and limits" onClose={onClose} dismissDisabled={saving}><section className="modal directory-form"><div className="modal-heading"><h2>Edit models and limits</h2><button className="icon-button" aria-label="Close identity policy" disabled={saving} onClick={onClose}>×</button></div>
    <p>Policy for <strong>{initial.scope_type} · {initial.scope_id}</strong>. This narrows existing access and shares quotas across all credentials of this identity.</p>
    <form onSubmit={submit}><fieldset className="form-grid" disabled={saving}>
      <label className="span-2">Model access<select value={mode} onChange={(event) => setMode(event.target.value as Policy["model_access"])}><option value="inherit">No additional model restriction</option><option value="allowlist">Selected models and patterns</option><option value="deny">Block all models</option></select></label>
      {mode === "allowlist" && <div className="span-2"><ChipMultiSelect label="Allowed models" options={options} value={models} onChange={setModels} allowCustom />{catalog.error && <p role="alert">Model catalog is unavailable. Existing selections and explicit model patterns remain editable.</p>}</div>}
      <label>Aggregate RPM<input type="number" min="0" max="10000000" step="1" required value={rpm} onChange={(event) => setRPM(event.target.value)} /></label>
      <label>Aggregate TPM<input type="number" min="0" max="1000000000" step="1" required value={tpm} onChange={(event) => setTPM(event.target.value)} /></label>
    </fieldset><p className="muted">Zero adds no limit for that dimension. Credential, access-group, tag, global, provider and deployment rules still apply. TPM reserves input plus maximum output. Team limits cover requests explicitly bound to that team.</p>
      {error && <p className="form-error" role="alert">{error}</p>}
      <div className="modal-actions"><button type="button" className="secondary" disabled={saving} onClick={onClose}>Cancel</button><button disabled={saving}>{saving ? "Saving…" : "Save models and limits"}</button></div>
    </form></section></ModalFrame>;
}

export function IdentityPolicyPanel({ kind, id, readOnly = false }: { kind: "team" | "user"; id: string; readOnly?: boolean }) {
  const { client, hasCapability } = useAuth();
  const admin = hasCapability("admin");
  const readable = admin || kind === "team" && hasCapability("team_directory");
  const path = `/admin/v1/identity-policies/${kind}/${encodeURIComponent(id)}`;
  const validate = useCallback((value: unknown) => {
    const settings = value as Settings | undefined, p = settings?.policy;
    return Boolean(p && p.scope_type === kind && p.scope_id === id && ["inherit", "allowlist", "deny"].includes(p.model_access) && Array.isArray(p.allowed_models) && p.allowed_models.every((model) => typeof model === "string") && (p.model_access === "allowlist") === (p.allowed_models.length > 0) && Number.isSafeInteger(p.rpm) && p.rpm >= 0 && p.rpm <= 10_000_000 && Number.isSafeInteger(p.tpm) && p.tpm >= 0 && p.tpm <= 1_000_000_000 && typeof settings?.configured === "boolean" && typeof settings?.durable === "boolean");
  }, [kind, id]);
  const data = useDirectoryResource<Settings>(readable ? path : undefined, `identity-policy:${kind}:${id}`, validate);
  const activeScope = useRef(data.scope); activeScope.current = data.scope;
  const [editor, setEditor] = useState<{ scope: string; policy: Policy }>();
  const [mutation, setMutation] = useState<{ scope: string; busy?: boolean; error?: string; notice?: string }>();
  const current = data.value;
  const busy = Boolean(mutation?.scope === data.scope && mutation.busy);
  async function save(value: Policy) {
    const scope = data.scope;
    await client.request(path, { method: "PUT", body: { model_access: value.model_access, allowed_models: value.allowed_models, rpm: value.rpm, tpm: value.tpm } });
    if (activeScope.current !== scope) return;
    setEditor((value) => value === editor ? undefined : value);
    setMutation({ scope, notice: "Identity policy saved. Changes apply to subsequent authorized requests." });
    await data.reload();
  }
  async function reset() {
    if (busy || !window.confirm(`Remove additional model restrictions and aggregate quotas for ${kind} ${id}? All other applicable policies remain enforced.`)) return;
    const scope = data.scope; setMutation({ scope, busy: true });
    try {
      await client.request(path, { method: "DELETE" });
      if (activeScope.current === scope) { setMutation({ scope, notice: "Additional identity policy removed." }); await data.reload(); }
    } catch (cause) { if (activeScope.current === scope) setMutation({ scope, error: cause instanceof Error ? cause.message : "Could not reset identity policy" }); }
    finally { if (activeScope.current === scope) setMutation((value) => ({ ...value, scope, busy: false })); }
  }
  return <section className="section-block" aria-label="Identity models and limits"><h2>Models &amp; limits</h2>{!readable ? <p>User-wide inference policy is restricted to global administrators.</p> : <>
    <p>Additional restrictions for this {kind}; these do not replace credential grants or other applicable policy. Quotas are shared across credentials, independently of budgets. Redis shares counters across replicas; memory counters apply per process.</p>
    {data.error && <ErrorState message={data.error} retry={() => void data.reload()} />}
    {mutation?.scope === data.scope && mutation.error && <ErrorState message={mutation.error} />}{mutation?.scope === data.scope && mutation.notice && <p role="status">{mutation.notice}</p>}
    {data.loading && !current ? <LoadingState /> : current && <div aria-busy={data.loading}>
      {data.loading && <p role="status">Refreshing models and limits…</p>}
      <dl className="detail-grid"><div><dt>Model restriction</dt><dd>{current.policy.model_access === "deny" ? "All models blocked" : current.policy.model_access === "allowlist" ? current.policy.allowed_models.join(", ") : "No additional restriction"}</dd></div><div><dt>Aggregate RPM</dt><dd>{current.policy.rpm ? current.policy.rpm.toLocaleString() : "No additional limit"}</dd></div><div><dt>Aggregate TPM</dt><dd>{current.policy.tpm ? current.policy.tpm.toLocaleString() : "No additional limit"}</dd></div><div><dt>Policy source</dt><dd>{current.configured ? "Configured" : "Default"} · {current.durable ? "Persisted control plane" : "Process memory only"}</dd></div></dl>
      {!current.durable && <p role="status">This configuration is process-local and is lost on restart. Use PostgreSQL and shared Redis quotas for multiple replicas.</p>}
      <div className="inline-actions">{admin && !readOnly && <><button disabled={data.loading || busy} onClick={() => setEditor({ scope: data.scope, policy: current.policy })}>Edit models and limits</button><button className="secondary" disabled={!current.configured || data.loading || busy} onClick={() => void reset()}>Reset identity policy</button></>}<button className="secondary" onClick={() => void data.reload()}>Refresh models and limits</button></div>
    </div>}
    {admin && !readOnly && editor?.scope === data.scope && <PolicyForm initial={editor.policy} onClose={() => setEditor((value) => value === editor ? undefined : value)} onSave={save} />}
  </>}</section>;
}
