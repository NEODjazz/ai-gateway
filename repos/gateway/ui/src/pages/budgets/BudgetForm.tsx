import { useEffect, useState, type FormEvent } from "react";
import { ModalFrame } from "../../components/ModalFrame";

export type BudgetPolicy = { id: number; scope_type: string; scope_id: string; period: string; currency: string; max_cost?: number; max_tokens?: number; enabled: boolean; created_at: string; updated_at: string };
export type BudgetSummary = { policy: BudgetPolicy; window_start: string; window_end: string; used_cost: number; remaining_cost?: number; used_tokens: number; remaining_tokens?: number };
export type TargetOption = { value: string; label: string };
export type BudgetDraft = { scope_type: string; scope_id: string; period: string; currency: string; max_cost: string; max_tokens: string; enabled: boolean };
export const scopeTypes = ["global", "organization", "team", "user", "key", "model", "provider", "deployment", "tag"];
const periods = ["hour", "day", "week", "month"];
const emptyDraft: BudgetDraft = { scope_type: "global", scope_id: "*", period: "month", currency: "USD", max_cost: "", max_tokens: "", enabled: true };

export function BudgetForm({ initial, scope, loadTargets, onClose, onSave }: { initial?: BudgetPolicy; scope?: { type: "team" | "user"; id: string }; loadTargets?: (scope: string) => Promise<TargetOption[]>; onClose: () => void; onSave: (draft: BudgetDraft) => Promise<void> }) {
  const [draft, setDraft] = useState<BudgetDraft>(initial ? { scope_type: initial.scope_type, scope_id: initial.scope_id, period: initial.period, currency: initial.currency, max_cost: initial.max_cost === undefined ? "" : String(initial.max_cost), max_tokens: initial.max_tokens === undefined ? "" : String(initial.max_tokens), enabled: initial.enabled } : scope ? { ...emptyDraft, scope_type: scope.type, scope_id: scope.id } : emptyDraft);
  const [targets, setTargets] = useState<TargetOption[]>(draft.scope_type === "global" ? [{ value: "*", label: "All gateway traffic (*)" }] : draft.scope_id ? [{ value: draft.scope_id, label: `${draft.scope_id} — current selection` }] : []);
  const [targetError, setTargetError] = useState("");
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    if (scope || !loadTargets) return;
    let active = true; setTargetError("");
    const selected = draft.scope_id;
    loadTargets(draft.scope_type).then((options) => {
      if (!active) return;
      setTargets(selected && !options.some((option) => option.value === selected) ? [{ value: selected, label: `${selected} — current selection` }, ...options] : options);
    }).catch((cause) => { if (active) { setTargets(selected ? [{ value: selected, label: `${selected} — current selection` }] : []); setTargetError(cause instanceof Error ? cause.message : "Could not load configured targets"); } });
    return () => { active = false; };
  }, [draft.scope_type, loadTargets, scope]);
  function changeScope(scope_type: string) { setDraft((value) => ({ ...value, scope_type, scope_id: scope_type === "global" ? "*" : "" })); }
  async function submit(event: FormEvent) {
    event.preventDefault(); if (saving) return; setError("");
    const maxCost = draft.max_cost.trim() ? Number(draft.max_cost) : undefined;
    const maxTokens = draft.max_tokens.trim() ? Number(draft.max_tokens) : undefined;
    if (!draft.scope_id || (!maxCost && !maxTokens) || draft.currency.trim().length !== 3) { setError("Select a scope, enter at least one positive limit, and use a three-letter currency code."); return; }
    if ((maxCost !== undefined && (!Number.isFinite(maxCost) || maxCost <= 0)) || (maxTokens !== undefined && (!Number.isSafeInteger(maxTokens) || maxTokens <= 0))) { setError("Limits must be positive; token limits must be whole numbers."); return; }
    setSaving(true);
    try { await onSave({ ...draft, currency: draft.currency.trim().toUpperCase(), max_cost: maxCost === undefined ? "" : String(maxCost), max_tokens: maxTokens === undefined ? "" : String(maxTokens) }); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not save budget"); }
    finally { setSaving(false); }
  }
  return <ModalFrame label={initial ? "Edit budget" : "Create budget"} onClose={onClose} dismissDisabled={saving}><form className="modal key-form-modal"    onSubmit={submit}>
    <div className="modal-heading"><div><h2>{initial ? "Edit" : "Create"} Budget</h2><span className="muted">Limits are enforced atomically within one currency and reset window.</span></div><button type="button" className="icon-button" aria-label="Close budget form" disabled={saving} onClick={onClose}>×</button></div>
    <fieldset className="key-form budget-form-fields" disabled={saving}>
      {scope ? <p className="span-2">Budget scope: <strong>{scope.type} · {scope.id}</strong></p> : <><label><span>Scope type</span><select aria-label="Budget scope type" value={draft.scope_type} onChange={(event) => changeScope(event.target.value)}>{scopeTypes.map((scope) => <option key={scope} value={scope}>{scope}</option>)}</select></label>
      <label><span>Scope</span><select aria-label="Budget scope" required value={draft.scope_id} onChange={(event) => setDraft({ ...draft, scope_id: event.target.value })}><option value="">Select configured item</option>{targets.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}</select></label></>}
      <label><span>Reset period</span><select aria-label="Budget reset period" value={draft.period} onChange={(event) => setDraft({ ...draft, period: event.target.value })}>{periods.map((period) => <option key={period} value={period}>{period}</option>)}</select></label>
      <label><span>Currency</span><input aria-label="Budget currency" maxLength={3} value={draft.currency} onChange={(event) => setDraft({ ...draft, currency: event.target.value.toUpperCase() })} /></label>
      <label><span>Maximum cost</span><input aria-label="Budget maximum cost" type="number" min="0" step="0.000001" placeholder="Optional" value={draft.max_cost} onChange={(event) => setDraft({ ...draft, max_cost: event.target.value })} /></label>
      <label><span>Maximum tokens</span><input aria-label="Budget maximum tokens" type="number" min="1" step="1" placeholder="Optional" value={draft.max_tokens} onChange={(event) => setDraft({ ...draft, max_tokens: event.target.value })} /></label>
      <label className="checkbox-line"><input aria-label="Budget enabled" type="checkbox" checked={draft.enabled} onChange={(event) => setDraft({ ...draft, enabled: event.target.checked })} /> Enabled</label>
    </fieldset>
    {targetError && <p className="form-error" role="alert">Configured targets are unavailable: {targetError}</p>}{error && <p className="form-error" role="alert">{error}</p>}
    <div className="modal-actions"><button type="button" className="secondary" disabled={saving} onClick={onClose}>Cancel</button><button disabled={saving}>{saving ? "Saving…" : initial ? "Save changes" : "Create budget"}</button></div>
  </form></ModalFrame>;
}
