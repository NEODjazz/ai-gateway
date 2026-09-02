import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import { useSearchParams } from "react-router-dom";
import { type APIClient } from "../api/client";
import { useAuth } from "../auth/AuthContext";
import { ActionsMenu } from "../components/ActionsMenu";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { ChipMultiSelect, type ChipOption } from "../components/ChipMultiSelect";
import { ManagedDataTable } from "../components/ManagedDataTable";
import { PageHeader } from "../components/PageHeader";
import { PolicyResolutionResult, type PolicyResolution } from "../components/PolicyResolutionResult";
import { StatCard } from "../components/StatCard";
import type { Row } from "../components/DataTable";

type GuardrailPolicy = { name: string; description?: string; dlp: boolean; av: boolean; enabled: boolean };
type PolicyAttachment = { id: string; policy_name: string; scope: "*" | "specific" | ""; teams?: string[]; keys?: string[]; models?: string[]; tags?: string[] };
type DirectoryTeam = { id: string; name?: string };
type VirtualKey = { id: string; alias?: string };
type CatalogModel = { model: string; provider?: string };
type TagDefinition = { name: string; description?: string; enabled: boolean };
type AttachmentDraft = { id: string; policy_name: string; scope: "*" | "specific"; teams: string[]; keys: string[]; models: string[]; tags: string[] };
type AttachmentSeed = Partial<AttachmentDraft>;
type SimulatorContext = { team_id: string; credential_id: string; credential_alias: string; model: string; tags: string[] };

const emptyDraft: AttachmentDraft = { id: "", policy_name: "", scope: "specific", teams: [], keys: [], models: [], tags: [] };
const emptyContext: SimulatorContext = { team_id: "", credential_id: "", credential_alias: "", model: "", tags: [] };

function records<T>(payload: unknown, key = "data"): T[] {
  if (Array.isArray(payload)) return payload as T[];
  if (!payload || typeof payload !== "object") return [];
  const value = (payload as Record<string, unknown>)[key];
  return Array.isArray(value) ? value as T[] : [];
}

function values(value?: string[]) { return value || []; }
function policyModules(policy?: GuardrailPolicy) { return [policy?.dlp && "DLP", policy?.av && "Antivirus"].filter(Boolean) as string[]; }
function scopeDimensions(attachment: PolicyAttachment | AttachmentDraft) {
  if (attachment.scope === "*") return ["Global"];
  return [[attachment.teams, "Teams"], [attachment.keys, "Keys"], [attachment.models, "Models"], [attachment.tags, "Tags"]]
    .flatMap(([items, label]) => (items as string[] | undefined)?.length ? [label as string] : []);
}
function options<T>(items: T[], value: (item: T) => string, label: (item: T) => string, description?: (item: T) => string | undefined): ChipOption[] {
  return items.flatMap((item) => { const id = value(item); return id ? [{ value: id, label: label(item) || id, description: description?.(item) }] : []; });
}

function ScopePreview({ draft }: { draft: AttachmentDraft }) {
  const dimensions = scopeDimensions(draft);
  return <section className={`notice-card policy-scope-preview ${draft.scope === "*" ? "warning" : ""}`} aria-label="Scope impact preview"><h3>Impact preview</h3>{draft.scope === "*" ? <p>This attachment applies to all inference requests. Saving it changes enforcement immediately.</p> : <><p>{dimensions.length ? "A request must match every configured dimension. Values inside one dimension are alternatives." : "Choose at least one dimension. An empty specific scope cannot be saved."}</p><div className="tag-list">{dimensions.map((item) => <span className="tag" key={item}>{item}</span>)}</div><small>Only a trailing * is a wildcard prefix. Other characters are matched literally.</small></>}</section>;
}

function AttachmentForm({ initial, seed, policies, teamOptions, keyOptions, modelOptions, tagOptions, client, onClose, onSaved }: { initial?: PolicyAttachment; seed?: AttachmentSeed; policies: GuardrailPolicy[]; teamOptions: ChipOption[]; keyOptions: ChipOption[]; modelOptions: ChipOption[]; tagOptions: ChipOption[]; client: APIClient; onClose: () => void; onSaved: () => Promise<void> }) {
  const [draft, setDraft] = useState<AttachmentDraft>(() => initial ? { id: initial.id, policy_name: initial.policy_name, scope: initial.scope === "*" ? "*" : "specific", teams: values(initial.teams), keys: values(initial.keys), models: values(initial.models), tags: values(initial.tags) } : { ...emptyDraft, ...seed, scope: "specific", teams: values(seed?.teams), keys: values(seed?.keys), models: values(seed?.models), tags: values(seed?.tags) });
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const enabledPolicies = policies.filter((policy) => policy.enabled || policy.name === draft.policy_name);
  async function save(event: FormEvent) {
    event.preventDefault(); setError("");
    if (!/^[A-Za-z0-9_.-]{1,128}$/.test(draft.id.trim())) { setError("Attachment ID may contain letters, numbers, dot, underscore and hyphen"); return; }
    if (!policies.some((policy) => policy.name === draft.policy_name && policy.enabled)) { setError("Select an enabled guardrail policy"); return; }
    if (draft.scope === "specific" && !scopeDimensions(draft).length) { setError("Select at least one team, key, model or tag scope"); return; }
    setSaving(true);
    try {
      const scoped = draft.scope === "specific";
      await client.request(`/admin/v1/policy-attachments/${encodeURIComponent(draft.id.trim())}`, { method: "PUT", body: { policy_name: draft.policy_name, scope: draft.scope, teams: scoped ? draft.teams : [], keys: scoped ? draft.keys : [], models: scoped ? draft.models : [], tags: scoped ? draft.tags : [] } });
      onClose(); await onSaved();
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not save policy attachment"); }
    finally { setSaving(false); }
  }
  const updateList = (key: "teams" | "keys" | "models" | "tags") => (next: string[]) => setDraft((current) => ({ ...current, [key]: next }));
  return <div className="modal-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && !saving && onClose()}><section className="modal policy-attachment-modal" role="dialog" aria-modal="true" aria-label={initial ? `Edit attachment ${initial.id}` : "Create Policy Attachment"}><div className="modal-heading"><div><span className="eyebrow">Runtime scope</span><h2>{initial ? `Edit ${initial.id}` : "Create Policy Attachment"}</h2></div><button className="icon-button" disabled={saving} aria-label="Close policy attachment form" onClick={onClose}>×</button></div><form className="policy-attachment-form" onSubmit={save}>
    <label><span>Attachment ID</span><input required readOnly={Boolean(initial)} value={draft.id} onChange={(event) => setDraft((current) => ({ ...current, id: event.target.value }))} placeholder="clinical-production" /></label>
    <label><span>Guardrail policy</span><select required value={draft.policy_name} onChange={(event) => setDraft((current) => ({ ...current, policy_name: event.target.value }))}><option value="">Select enabled policy</option>{enabledPolicies.map((policy) => <option key={policy.name} value={policy.name} disabled={!policy.enabled}>{policy.name} — {policyModules(policy).join(" + ") || "No scanners"}{!policy.enabled ? " (disabled)" : ""}</option>)}</select></label>
    <fieldset><legend>Scope</legend><label><input type="radio" name="scope" checked={draft.scope === "specific"} onChange={() => setDraft((current) => ({ ...current, scope: "specific" }))} /> Specific request context</label><label><input type="radio" name="scope" checked={draft.scope === "*"} onChange={() => setDraft((current) => ({ ...current, scope: "*" }))} /> Global — all requests</label></fieldset>
    {draft.scope === "specific" && <div className="policy-scope-fields"><ChipMultiSelect label="Teams" options={teamOptions} value={draft.teams} onChange={updateList("teams")} allowCustom /><ChipMultiSelect label="Keys" options={keyOptions} value={draft.keys} onChange={updateList("keys")} allowCustom /><ChipMultiSelect label="Models" options={modelOptions} value={draft.models} onChange={updateList("models")} allowCustom /><ChipMultiSelect label="Tags" options={tagOptions} value={draft.tags} onChange={updateList("tags")} allowCustom /></div>}
    <ScopePreview draft={draft} />{error && <p className="form-error" role="alert">{error}</p>}<div className="modal-actions"><button type="button" className="secondary" disabled={saving} onClick={onClose}>Cancel</button><button disabled={saving}>{saving ? "Saving…" : "Save attachment"}</button></div>
  </form></section></div>;
}

function AttachmentDetails({ attachment, policy, onClose, onEdit, onDelete, onSimulate }: { attachment: PolicyAttachment; policy?: GuardrailPolicy; onClose: () => void; onEdit: () => void; onDelete: () => void; onSimulate: () => void }) {
  const sections: Array<[string, string[]]> = [["Teams", values(attachment.teams)], ["Keys", values(attachment.keys)], ["Models", values(attachment.models)], ["Tags", values(attachment.tags)]];
  return <div className="modal-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}><section className="modal policy-attachment-modal" role="dialog" aria-modal="true" aria-label={`Policy attachment ${attachment.id}`}><div className="modal-heading"><div><span className="eyebrow">Policy attachment</span><h2>{attachment.id}</h2></div><button className="icon-button" aria-label="Close attachment details" onClick={onClose}>×</button></div><dl className="detail-grid"><div><dt>Policy</dt><dd>{attachment.policy_name}</dd></div><div><dt>Policy state</dt><dd><span className={`status ${policy?.enabled ? "enabled" : "error"}`}>{policy ? policy.enabled ? "Enabled" : "Disabled" : "Missing"}</span></dd></div><div><dt>Scope</dt><dd>{attachment.scope === "*" ? "Global" : "Specific"}</dd></div><div><dt>Modules</dt><dd>{policyModules(policy).join(" + ") || "—"}</dd></div>{sections.map(([label, items]) => <div key={label}><dt>{label}</dt><dd>{items.length ? items.join(", ") : "Any"}</dd></div>)}</dl><section className="notice-card"><h3>Runtime semantics</h3><p>{attachment.scope === "*" ? "This attachment matches every request." : "Every configured dimension must match; values inside each dimension use OR semantics."} Missing or disabled policies fail closed.</p></section><div className="modal-actions"><button className="danger secondary" onClick={onDelete}>Delete</button><button className="secondary" onClick={onSimulate}>Open simulator</button><button onClick={onEdit}>Edit</button></div></section></div>;
}

function contextFromSearchParams(searchParams: URLSearchParams): SimulatorContext {
  const tags = [...searchParams.getAll("tag"), ...(searchParams.get("tags") || "").split(",")].map((tag) => tag.trim()).filter(Boolean);
  return {
    team_id: searchParams.get("team_id") || "",
    credential_id: searchParams.get("credential_id") || "",
    credential_alias: searchParams.get("credential_alias") || "",
    model: searchParams.get("model") || "",
    tags: [...new Set(tags)],
  };
}

function attachmentSeedFromSearchParams(searchParams: URLSearchParams): AttachmentSeed {
  const list = (name: string) => searchParams.getAll(name).map((value) => value.trim()).filter(Boolean);
  return {
    id: searchParams.get("suggested_id")?.trim() || "",
    policy_name: searchParams.get("attach_policy")?.trim() || "",
    teams: list("attach_team"),
    keys: list("attach_key"),
    models: list("attach_model"),
    tags: list("attach_tag"),
  };
}

function PolicySimulator({ client, teamOptions, keyOptions, modelOptions, tagOptions, initialContext }: { client: APIClient; teamOptions: ChipOption[]; keyOptions: ChipOption[]; modelOptions: ChipOption[]; tagOptions: ChipOption[]; initialContext: SimulatorContext }) {
  const [context, setContext] = useState(initialContext);
  const [result, setResult] = useState<PolicyResolution>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function simulate(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError(""); setResult(undefined);
    try { setResult(await client.request<PolicyResolution>("/admin/v1/policy-attachments/resolve", { method: "POST", body: { team_id: context.team_id || undefined, credential_id: context.credential_id || undefined, credential_alias: context.credential_alias || undefined, model: context.model || undefined, tags: context.tags.length ? context.tags : undefined } })); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not resolve policies"); }
    finally { setBusy(false); }
  }
  function clear() { setContext(emptyContext); setResult(undefined); setError(""); }
  return <section className="policy-simulator"><div className="notice-card policy-simulator-boundary"><h2>Runtime policy simulator</h2><p>Resolve attachments using production matcher semantics. This performs no provider request, model call, DLP scan or antivirus scan.</p></div><form className="table-card policy-simulator-form" onSubmit={simulate}><div className="form-grid"><label>Team ID<input list="policy-team-options" aria-label="Team ID" value={context.team_id} onChange={(event) => setContext((current) => ({ ...current, team_id: event.target.value }))} placeholder="Select or enter team ID" /></label><label>Virtual key alias<input list="policy-key-options" aria-label="Virtual key alias" value={context.credential_alias} onChange={(event) => setContext((current) => ({ ...current, credential_alias: event.target.value }))} placeholder="Select or enter key alias" /></label><label>Virtual key ID<input aria-label="Virtual key ID" value={context.credential_id} onChange={(event) => setContext((current) => ({ ...current, credential_id: event.target.value }))} placeholder="Optional immutable ID" /></label><label>Model<input list="policy-model-options" aria-label="Model" value={context.model} onChange={(event) => setContext((current) => ({ ...current, model: event.target.value }))} placeholder="Select or enter public model" /></label></div><datalist id="policy-team-options">{teamOptions.map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}</datalist><datalist id="policy-key-options">{keyOptions.map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}</datalist><datalist id="policy-model-options">{modelOptions.map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}</datalist><ChipMultiSelect label="Tags" options={tagOptions} value={context.tags} onChange={(tags) => setContext((current) => ({ ...current, tags }))} allowCustom />{error && <p className="form-error" role="alert">{error}</p>}<div className="modal-actions"><button type="button" className="secondary" onClick={clear}>Reset</button><button disabled={busy}>{busy ? "Simulating…" : "Simulate"}</button></div></form>
    {!result && !error && <div className="table-card empty-state"><div><strong>No simulation run yet</strong><p>Empty context is valid and reveals global policy attachments.</p></div></div>}
    {result && <PolicyResolutionResult result={result} />}
  </section>;
}

export function PoliciesPage() {
  const { client } = useAuth();
  const [searchParams, setSearchParams] = useSearchParams();
  const tab = searchParams.get("view") === "simulator" ? "simulator" : "attachments";
  const initialSimulatorContext = useMemo(() => contextFromSearchParams(searchParams), [searchParams]);
  const attachmentSeed = useMemo(() => attachmentSeedFromSearchParams(searchParams), [searchParams]);
  const [attachments, setAttachments] = useState<PolicyAttachment[]>([]);
  const [policies, setPolicies] = useState<GuardrailPolicy[]>([]);
  const [teams, setTeams] = useState<DirectoryTeam[]>([]);
  const [keys, setKeys] = useState<VirtualKey[]>([]);
  const [models, setModels] = useState<CatalogModel[]>([]);
  const [tags, setTags] = useState<TagDefinition[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [directoryWarning, setDirectoryWarning] = useState("");
  const [editing, setEditing] = useState<PolicyAttachment | null | undefined>(() => searchParams.get("create") === "1" ? null : undefined);
  const [inspecting, setInspecting] = useState<PolicyAttachment>();
  const load = useCallback(async (signal?: AbortSignal) => {
    setLoading(true); setError(""); setDirectoryWarning("");
    try {
      const [attachmentPayload, policyPayload] = await Promise.all([client.request("/admin/v1/policy-attachments", { signal }), client.request("/admin/v1/guardrail-policies", { signal })]);
      setAttachments(records<PolicyAttachment>(attachmentPayload)); setPolicies(records<GuardrailPolicy>(policyPayload));
      const references = await Promise.allSettled([client.request("/admin/v1/teams?limit=500", { signal }), client.request("/admin/v1/keys?limit=500&status=non_revoked&sort_by=alias&sort_order=asc", { signal }), client.request("/admin/v1/model-catalog", { signal }), client.request("/admin/v1/tags", { signal })]);
      if (signal?.aborted) return;
      if (references[0].status === "fulfilled") setTeams(records<DirectoryTeam>(references[0].value));
      if (references[1].status === "fulfilled") setKeys(records<VirtualKey>(references[1].value));
      if (references[2].status === "fulfilled") setModels(records<CatalogModel>(references[2].value, "models"));
      if (references[3].status === "fulfilled") setTags(records<TagDefinition>(references[3].value));
      if (references.some((item) => item.status === "rejected")) setDirectoryWarning("Some configured selector lists are unavailable. Exact IDs and trailing-* patterns can still be entered manually.");
    } catch (cause) { if (!signal?.aborted) setError(cause instanceof Error ? cause.message : "Could not load policy attachments"); }
    finally { if (!signal?.aborted) setLoading(false); }
  }, [client]);
  useEffect(() => { const controller = new AbortController(); void load(controller.signal); return () => controller.abort(); }, [load]);
  useEffect(() => { if (searchParams.get("create") === "1") setEditing(null); }, [searchParams]);
  const policyByName = useMemo(() => new Map(policies.map((policy) => [policy.name, policy])), [policies]);
  const teamOptions = useMemo(() => options(teams, (team) => team.id, (team) => team.name || team.id, (team) => team.name && team.name !== team.id ? team.id : undefined), [teams]);
  const keyOptions = useMemo(() => options(keys, (key) => key.alias || key.id, (key) => key.alias || key.id, (key) => key.alias ? key.id : undefined), [keys]);
  const modelOptions = useMemo(() => options(models, (model) => model.model, (model) => model.model, (model) => model.provider), [models]);
  const tagOptions = useMemo(() => options(tags.filter((tag) => tag.enabled), (tag) => tag.name, (tag) => tag.name, (tag) => tag.description), [tags]);
  const rows: Row[] = attachments.map((attachment) => { const policy = policyByName.get(attachment.policy_name); return { id: attachment.id, policy: attachment.policy_name, scope: attachment.scope === "*" ? "Global" : "Specific", dimensions: scopeDimensions(attachment), teams: values(attachment.teams), keys: values(attachment.keys), models: values(attachment.models), tags: values(attachment.tags), policy_status: policy ? policy.enabled ? "enabled" : "disabled" : "missing", modules: policyModules(policy), _attachment: attachment }; });
  const broken = rows.filter((row) => row.policy_status !== "enabled").length;
  async function remove(attachment: PolicyAttachment) { if (!window.confirm(`Delete policy attachment ${attachment.id}? Enforcement changes immediately.`)) return; try { await client.request(`/admin/v1/policy-attachments/${encodeURIComponent(attachment.id)}`, { method: "DELETE" }); setInspecting(undefined); await load(); } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not delete policy attachment"); } }
  function closeEditor() {
    setEditing(undefined);
    const query = new URLSearchParams(searchParams);
    const names = ["create", "suggested_id", "attach_policy", "attach_team", "attach_key", "attach_model", "attach_tag"];
    const changed = names.some((name) => query.has(name));
    for (const name of names) query.delete(name);
    if (changed) setSearchParams(query, { replace: true });
  }
  function selectTab(next: "attachments" | "simulator") { const query = new URLSearchParams(searchParams); if (next === "simulator") query.set("view", "simulator"); else query.delete("view"); setSearchParams(query, { replace: true }); }
  if (loading && !attachments.length && !policies.length) return <LoadingState />;
  if (error && !attachments.length && !policies.length) return <ErrorState message={error} retry={() => void load()} />;
  return <><PageHeader eyebrow="Governance" title="Policies" description="Attach reusable guardrail policies to request identity and model scopes, then simulate the exact runtime resolution before traffic is affected." />{error && <ErrorState message={error} retry={() => void load()} />}{directoryWarning && <section className="notice-card" role="status"><p>{directoryWarning}</p></section>}<div className="page-tabs" role="tablist" aria-label="Policy views"><button role="tab" aria-selected={tab === "attachments"} className={tab === "attachments" ? "active" : ""} onClick={() => selectTab("attachments")}>Attachments</button><button role="tab" aria-selected={tab === "simulator"} className={tab === "simulator" ? "active" : ""} onClick={() => selectTab("simulator")}>Policy Simulator</button></div>
    {tab === "attachments" ? <><div className="usage-stats-grid"><StatCard label="Attachments" value={attachments.length.toLocaleString()} /><StatCard label="Global" value={attachments.filter((item) => item.scope === "*").length.toLocaleString()} /><StatCard label="Policies in use" value={new Set(attachments.map((item) => item.policy_name)).size.toLocaleString()} /><StatCard label="Fail-closed risks" value={broken.toLocaleString()} detail="missing or disabled policies" /></div><section className="notice-card"><h2>Composition semantics</h2><p>All matching attachments apply. Their enabled policies are combined: DLP and antivirus requirements are cumulative. Missing or disabled attached policies fail closed.</p></section><section className="section-block"><h2>Policy attachments</h2><p>Specific scopes use AND across dimensions and OR within each dimension.</p><ManagedDataTable rows={rows} columns={[{ key: "id", label: "Attachment" }, { key: "policy", label: "Policy" }, { key: "scope", label: "Scope" }, { key: "dimensions", label: "Dimensions" }, { key: "modules", label: "Modules" }, { key: "policy_status", label: "Policy state" }, { key: "teams", label: "Teams" }, { key: "keys", label: "Keys" }, { key: "models", label: "Models" }, { key: "tags", label: "Tags" }]} defaultHidden={["teams", "keys", "models", "tags"]} primaryAction={<button onClick={() => setEditing(null)}>Create Policy Attachment</button>} onRefresh={load} searchPlaceholder="Search policy attachments" actions={(row) => { const attachment = row._attachment as PolicyAttachment; return <ActionsMenu label={`Actions for policy attachment ${attachment.id}`} items={[{ label: "Inspect", onSelect: () => setInspecting(attachment) }, { label: "Edit", onSelect: () => setEditing(attachment) }, { label: "Open simulator", onSelect: () => selectTab("simulator") }, { label: "Delete", tone: "danger", onSelect: () => remove(attachment) }]} />; }} /></section></> : <PolicySimulator client={client} teamOptions={teamOptions} keyOptions={keyOptions} modelOptions={modelOptions} tagOptions={tagOptions} initialContext={initialSimulatorContext} />}
    {editing !== undefined && <AttachmentForm initial={editing || undefined} seed={editing === null ? attachmentSeed : undefined} policies={policies} teamOptions={teamOptions} keyOptions={keyOptions} modelOptions={modelOptions} tagOptions={tagOptions} client={client} onClose={closeEditor} onSaved={() => load()} />}{inspecting && <AttachmentDetails attachment={inspecting} policy={policyByName.get(inspecting.policy_name)} onClose={() => setInspecting(undefined)} onEdit={() => { setInspecting(undefined); setEditing(inspecting); }} onDelete={() => void remove(inspecting)} onSimulate={() => { setInspecting(undefined); selectTab("simulator"); }} />}
  </>;
}
