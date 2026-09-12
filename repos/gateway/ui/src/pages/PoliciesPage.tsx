import { ModalFrame } from "../components/ModalFrame";
import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import { useSearchParams } from "react-router-dom";
import { type APIClient } from "../api/client";
import { useAuth } from "../auth/AuthContext";
import { ActionsMenu } from "../components/ActionsMenu";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { ChipMultiSelect, type ChipOption } from "../components/ChipMultiSelect";
import { ManagedDataTable } from "../components/ManagedDataTable";
import { PageHeader } from "../components/PageHeader";
import { PageTabs } from "../components/PageTabs";
import { PolicyResolutionResult, type PolicyResolution } from "../components/PolicyResolutionResult";
import { StatCard } from "../components/StatCard";
import type { Row } from "../components/DataTable";

type GuardrailPolicy = { name: string; description?: string; dlp: boolean; output_dlp: boolean; av: boolean; anonymization?: string; anonymization_rules?: string[]; enabled: boolean };
type PolicyAttachment = { id: string; policy_name: string; scope: "*" | "specific" | ""; organizations?: string[]; teams?: string[]; users?: string[]; keys?: string[]; models?: string[]; providers?: string[]; deployments?: string[]; tags?: string[] };
type DirectoryOrganization = { id: string; name?: string };
type DirectoryTeam = { id: string; name?: string };
type DirectoryUser = { id: string; name?: string; email?: string };
type VirtualKey = { id: string; alias?: string };
type CatalogModel = { model: string; provider?: string };
type ManagedProvider = { id: string; type?: string };
type Deployment = { id: string; provider_id?: string; upstream_model?: string };
type TagDefinition = { name: string; description?: string; enabled: boolean };
type AttachmentDraft = { id: string; policy_name: string; scope: "*" | "specific"; organizations: string[]; teams: string[]; users: string[]; keys: string[]; models: string[]; providers: string[]; deployments: string[]; tags: string[] };
type AttachmentSeed = Partial<AttachmentDraft>;
type SimulatorContext = { organization_id: string; team_id: string; user_id: string; credential_id: string; credential_alias: string; model: string; provider_id: string; deployment_id: string; tags: string[] };

const emptyDraft: AttachmentDraft = { id: "", policy_name: "", scope: "specific", organizations: [], teams: [], users: [], keys: [], models: [], providers: [], deployments: [], tags: [] };
const emptyContext: SimulatorContext = { organization_id: "", team_id: "", user_id: "", credential_id: "", credential_alias: "", model: "", provider_id: "", deployment_id: "", tags: [] };

function records<T>(payload: unknown, key = "data"): T[] {
  if (Array.isArray(payload)) return payload as T[];
  if (!payload || typeof payload !== "object") return [];
  const value = (payload as Record<string, unknown>)[key];
  return Array.isArray(value) ? value as T[] : [];
}

function values(value?: string[]) { return value || []; }
function policyModules(policy?: GuardrailPolicy) { return [policy?.dlp && "Input DLP", policy?.output_dlp && "Output DLP", policy?.av && "Antivirus", policy?.anonymization && `Anonymizer: ${policy.anonymization}`].filter(Boolean) as string[]; }
function scopeDimensions(attachment: PolicyAttachment | AttachmentDraft) {
  if (attachment.scope === "*") return ["Global"];
  return [[attachment.organizations, "Organizations"], [attachment.teams, "Teams"], [attachment.users, "Users"], [attachment.keys, "Keys"], [attachment.models, "Models"], [attachment.providers, "Providers"], [attachment.deployments, "Deployments"], [attachment.tags, "Tags"]]
    .flatMap(([items, label]) => (items as string[] | undefined)?.length ? [label as string] : []);
}
function options<T>(items: T[], value: (item: T) => string, label: (item: T) => string, description?: (item: T) => string | undefined): ChipOption[] {
  return items.flatMap((item) => { const id = value(item); return id ? [{ value: id, label: label(item) || id, description: description?.(item) }] : []; });
}

function ScopePreview({ draft }: { draft: AttachmentDraft }) {
  const dimensions = scopeDimensions(draft);
  return <section className={`notice-card policy-scope-preview ${draft.scope === "*" ? "warning" : ""}`} aria-label="Scope impact preview"><h3>Impact preview</h3>{draft.scope === "*" ? <p>This attachment applies to all inference requests. Saving it changes enforcement immediately.</p> : <><p>{dimensions.length ? "A request must match every configured dimension. Values inside one dimension are alternatives." : "Choose at least one dimension. An empty specific scope cannot be saved."}</p><div className="tag-list">{dimensions.map((item) => <span className="tag" key={item}>{item}</span>)}</div><small>Only a trailing * is a wildcard prefix. Other characters are matched literally.</small></>}</section>;
}

function AttachmentForm({ initial, seed, policies, selectorOptions, client, onClose, onSaved }: { initial?: PolicyAttachment; seed?: AttachmentSeed; policies: GuardrailPolicy[]; selectorOptions: Record<"organizations" | "teams" | "users" | "keys" | "models" | "providers" | "deployments" | "tags", ChipOption[]>; client: APIClient; onClose: () => void; onSaved: () => Promise<void> }) {
  const [draft, setDraft] = useState<AttachmentDraft>(() => initial ? { ...emptyDraft, ...initial, scope: initial.scope === "*" ? "*" : "specific", organizations: values(initial.organizations), teams: values(initial.teams), users: values(initial.users), keys: values(initial.keys), models: values(initial.models), providers: values(initial.providers), deployments: values(initial.deployments), tags: values(initial.tags) } : { ...emptyDraft, ...seed, scope: "specific" });
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const enabledPolicies = policies.filter((policy) => policy.enabled || policy.name === draft.policy_name);
  async function save(event: FormEvent) {
    event.preventDefault(); setError("");
    if (!/^[A-Za-z0-9_.-]{1,128}$/.test(draft.id.trim())) { setError("Attachment ID may contain letters, numbers, dot, underscore and hyphen"); return; }
    if (!policies.some((policy) => policy.name === draft.policy_name && policy.enabled)) { setError("Select an enabled guardrail policy"); return; }
    if (draft.scope === "specific" && !scopeDimensions(draft).length) { setError("Select at least one request or route scope"); return; }
    setSaving(true);
    try {
      const scoped = draft.scope === "specific";
      await client.request(`/admin/v1/policy-attachments/${encodeURIComponent(draft.id.trim())}`, { method: "PUT", body: { policy_name: draft.policy_name, scope: draft.scope, organizations: scoped && draft.organizations.length ? draft.organizations : undefined, teams: scoped ? draft.teams : [], users: scoped && draft.users.length ? draft.users : undefined, keys: scoped ? draft.keys : [], models: scoped ? draft.models : [], providers: scoped && draft.providers.length ? draft.providers : undefined, deployments: scoped && draft.deployments.length ? draft.deployments : undefined, tags: scoped ? draft.tags : [] } });
      onClose(); await onSaved();
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not save policy attachment"); }
    finally { setSaving(false); }
  }
  const updateList = (key: keyof typeof selectorOptions) => (next: string[]) => setDraft((current) => ({ ...current, [key]: next }));
  return <ModalFrame label={initial ? `Edit attachment ${initial.id}` : "Create Policy Attachment"} onClose={onClose} dismissDisabled={saving}><section className="modal policy-attachment-modal"><div className="modal-heading"><div><span className="eyebrow">Runtime scope</span><h2>{initial ? `Edit ${initial.id}` : "Create Policy Attachment"}</h2></div><button className="icon-button" disabled={saving} aria-label="Close policy attachment form" onClick={onClose}>×</button></div><form className="policy-attachment-form" onSubmit={save}>
    <label><span>Attachment ID</span><input required readOnly={Boolean(initial)} value={draft.id} onChange={(event) => setDraft((current) => ({ ...current, id: event.target.value }))} placeholder="clinical-production" /></label>
    <label><span>Guardrail policy</span><select required value={draft.policy_name} onChange={(event) => setDraft((current) => ({ ...current, policy_name: event.target.value }))}><option value="">Select enabled policy</option>{enabledPolicies.map((policy) => <option key={policy.name} value={policy.name} disabled={!policy.enabled}>{policy.name} — {policyModules(policy).join(" + ") || "No scanners"}{!policy.enabled ? " (disabled)" : ""}</option>)}</select></label>
    <fieldset><legend>Scope</legend><label><input type="radio" name="scope" checked={draft.scope === "specific"} onChange={() => setDraft((current) => ({ ...current, scope: "specific" }))} /> Specific request context</label><label><input type="radio" name="scope" checked={draft.scope === "*"} onChange={() => setDraft((current) => ({ ...current, scope: "*" }))} /> Global — all requests</label></fieldset>
    {draft.scope === "specific" && <div className="policy-scope-fields">{(["organizations", "teams", "users", "keys", "models", "providers", "deployments", "tags"] as const).map((key) => <ChipMultiSelect key={key} label={key[0].toUpperCase() + key.slice(1)} options={selectorOptions[key]} value={draft[key]} onChange={updateList(key)} allowCustom />)}</div>}
    <ScopePreview draft={draft} />{error && <p className="form-error" role="alert">{error}</p>}<div className="modal-actions"><button type="button" className="secondary" disabled={saving} onClick={onClose}>Cancel</button><button disabled={saving}>{saving ? "Saving…" : "Save attachment"}</button></div>
  </form></section></ModalFrame>;
}

function AttachmentDetails({ attachment, policy, onClose, onEdit, onDelete, onSimulate }: { attachment: PolicyAttachment; policy?: GuardrailPolicy; onClose: () => void; onEdit: () => void; onDelete: () => void; onSimulate: () => void }) {
  const sections: Array<[string, string[]]> = [["Organizations", values(attachment.organizations)], ["Teams", values(attachment.teams)], ["Users", values(attachment.users)], ["Keys", values(attachment.keys)], ["Models", values(attachment.models)], ["Providers", values(attachment.providers)], ["Deployments", values(attachment.deployments)], ["Tags", values(attachment.tags)]];
  return <ModalFrame label={`Policy attachment ${attachment.id}`} onClose={onClose}><section className="modal policy-attachment-modal"><div className="modal-heading"><div><span className="eyebrow">Policy attachment</span><h2>{attachment.id}</h2></div><button className="icon-button" aria-label="Close attachment details" onClick={onClose}>×</button></div><dl className="detail-grid"><div><dt>Policy</dt><dd>{attachment.policy_name}</dd></div><div><dt>Policy state</dt><dd><span className={`status ${policy?.enabled ? "enabled" : "error"}`}>{policy ? policy.enabled ? "Enabled" : "Disabled" : "Missing"}</span></dd></div><div><dt>Scope</dt><dd>{attachment.scope === "*" ? "Global" : "Specific"}</dd></div><div><dt>Modules</dt><dd>{policyModules(policy).join(" + ") || "—"}</dd></div>{sections.map(([label, items]) => <div key={label}><dt>{label}</dt><dd>{items.length ? items.join(", ") : "Any"}</dd></div>)}</dl><section className="notice-card"><h3>Runtime semantics</h3><p>{attachment.scope === "*" ? "This attachment matches every request." : "Every configured dimension must match; values inside each dimension use OR semantics."} Missing or disabled policies fail closed.</p></section><div className="modal-actions"><button className="danger secondary" onClick={onDelete}>Delete</button><button className="secondary" onClick={onSimulate}>Open simulator</button><button onClick={onEdit}>Edit</button></div></section></ModalFrame>;
}

function contextFromSearchParams(searchParams: URLSearchParams): SimulatorContext {
  const tags = [...searchParams.getAll("tag"), ...(searchParams.get("tags") || "").split(",")].map((tag) => tag.trim()).filter(Boolean);
  return {
    organization_id: searchParams.get("organization_id") || "",
    team_id: searchParams.get("team_id") || "",
    user_id: searchParams.get("user_id") || "",
    credential_id: searchParams.get("credential_id") || "",
    credential_alias: searchParams.get("credential_alias") || "",
    model: searchParams.get("model") || "",
    provider_id: searchParams.get("provider_id") || "",
    deployment_id: searchParams.get("deployment_id") || "",
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

function PolicySimulator({ client, selectorOptions, initialContext }: { client: APIClient; selectorOptions: Record<"organizations" | "teams" | "users" | "keys" | "models" | "providers" | "deployments" | "tags", ChipOption[]>; initialContext: SimulatorContext }) {
  const [context, setContext] = useState(initialContext);
  const [result, setResult] = useState<PolicyResolution>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function simulate(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError(""); setResult(undefined);
    try { setResult(await client.request<PolicyResolution>("/admin/v1/policy-attachments/resolve", { method: "POST", body: { organization_id: context.organization_id || undefined, team_id: context.team_id || undefined, user_id: context.user_id || undefined, credential_id: context.credential_id || undefined, credential_alias: context.credential_alias || undefined, model: context.model || undefined, provider_id: context.provider_id || undefined, deployment_id: context.deployment_id || undefined, tags: context.tags.length ? context.tags : undefined } })); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not resolve policies"); }
    finally { setBusy(false); }
  }
  function clear() { setContext(emptyContext); setResult(undefined); setError(""); }
  const fields: Array<[keyof Omit<SimulatorContext, "tags">, string, keyof typeof selectorOptions | undefined]> = [["organization_id", "Organization ID", "organizations"], ["team_id", "Team ID", "teams"], ["user_id", "User ID", "users"], ["credential_alias", "Virtual key alias", "keys"], ["credential_id", "Virtual key ID", undefined], ["model", "Model", "models"], ["provider_id", "Provider ID", "providers"], ["deployment_id", "Deployment ID", "deployments"]];
  return <section className="policy-simulator"><div className="notice-card policy-simulator-boundary"><h2>Runtime policy simulator</h2><p>Resolve attachments using the same identity, model and provider-route dimensions as inference. This performs no provider request or scan.</p></div><form className="table-card policy-simulator-form" onSubmit={simulate}><div className="form-grid">{fields.map(([key, label, optionsKey]) => <label key={key}>{label}<input list={optionsKey ? `policy-${optionsKey}-options` : undefined} aria-label={label} value={context[key]} onChange={(event) => setContext((current) => ({ ...current, [key]: event.target.value }))} placeholder={`Select or enter ${label.toLowerCase()}`} /></label>)}</div>{Object.entries(selectorOptions).filter(([key]) => key !== "tags").map(([key, list]) => <datalist id={`policy-${key}-options`} key={key}>{list.map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}</datalist>)}<ChipMultiSelect label="Tags" options={selectorOptions.tags} value={context.tags} onChange={(tags) => setContext((current) => ({ ...current, tags }))} allowCustom />{error && <p className="form-error" role="alert">{error}</p>}<div className="modal-actions"><button type="button" className="secondary" onClick={clear}>Reset</button><button disabled={busy}>{busy ? "Simulating…" : "Simulate"}</button></div></form>
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
  const [organizations, setOrganizations] = useState<DirectoryOrganization[]>([]);
  const [teams, setTeams] = useState<DirectoryTeam[]>([]);
  const [users, setUsers] = useState<DirectoryUser[]>([]);
  const [keys, setKeys] = useState<VirtualKey[]>([]);
  const [models, setModels] = useState<CatalogModel[]>([]);
  const [providers, setProviders] = useState<ManagedProvider[]>([]);
  const [deployments, setDeployments] = useState<Deployment[]>([]);
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
      const references = await Promise.allSettled([client.request("/admin/v1/organizations?limit=500", { signal }), client.request("/admin/v1/teams?limit=500", { signal }), client.request("/admin/v1/users?limit=500", { signal }), client.request("/admin/v1/keys?limit=500&status=non_revoked&sort_by=alias&sort_order=asc", { signal }), client.request("/admin/v1/model-catalog", { signal }), client.request("/admin/v1/providers", { signal }), client.request("/admin/v1/model-deployments", { signal }), client.request("/admin/v1/tags", { signal })]);
      if (signal?.aborted) return;
      if (references[0].status === "fulfilled") setOrganizations(records<DirectoryOrganization>(references[0].value));
      if (references[1].status === "fulfilled") setTeams(records<DirectoryTeam>(references[1].value));
      if (references[2].status === "fulfilled") setUsers(records<DirectoryUser>(references[2].value));
      if (references[3].status === "fulfilled") setKeys(records<VirtualKey>(references[3].value));
      if (references[4].status === "fulfilled") setModels(records<CatalogModel>(references[4].value, "models"));
      if (references[5].status === "fulfilled") setProviders(records<ManagedProvider>(references[5].value));
      if (references[6].status === "fulfilled") setDeployments(records<Deployment>(references[6].value));
      if (references[7].status === "fulfilled") setTags(records<TagDefinition>(references[7].value));
      if (references.some((item) => item.status === "rejected")) setDirectoryWarning("Some configured selector lists are unavailable. Exact IDs and trailing-* patterns can still be entered manually.");
    } catch (cause) { if (!signal?.aborted) setError(cause instanceof Error ? cause.message : "Could not load policy attachments"); }
    finally { if (!signal?.aborted) setLoading(false); }
  }, [client]);
  useEffect(() => { const controller = new AbortController(); void load(controller.signal); return () => controller.abort(); }, [load]);
  useEffect(() => { if (searchParams.get("create") === "1") setEditing(null); }, [searchParams]);
  const policyByName = useMemo(() => new Map(policies.map((policy) => [policy.name, policy])), [policies]);
  const organizationOptions = useMemo(() => options(organizations, (organization) => organization.id, (organization) => organization.name || organization.id), [organizations]);
  const teamOptions = useMemo(() => options(teams, (team) => team.id, (team) => team.name || team.id, (team) => team.name && team.name !== team.id ? team.id : undefined), [teams]);
  const userOptions = useMemo(() => options(users, (user) => user.id, (user) => user.name || user.email || user.id, (user) => user.email), [users]);
  const keyOptions = useMemo(() => options(keys, (key) => key.alias || key.id, (key) => key.alias || key.id, (key) => key.alias ? key.id : undefined), [keys]);
  const modelOptions = useMemo(() => options(models, (model) => model.model, (model) => model.model, (model) => model.provider), [models]);
  const providerOptions = useMemo(() => options(providers, (provider) => provider.id, (provider) => provider.id, (provider) => provider.type), [providers]);
  const deploymentOptions = useMemo(() => options(deployments, (deployment) => deployment.id, (deployment) => deployment.id, (deployment) => [deployment.provider_id, deployment.upstream_model].filter(Boolean).join(" · ")), [deployments]);
  const tagOptions = useMemo(() => options(tags.filter((tag) => tag.enabled), (tag) => tag.name, (tag) => tag.name, (tag) => tag.description), [tags]);
  const selectorOptions = useMemo(() => ({ organizations: organizationOptions, teams: teamOptions, users: userOptions, keys: keyOptions, models: modelOptions, providers: providerOptions, deployments: deploymentOptions, tags: tagOptions }), [deploymentOptions, keyOptions, modelOptions, organizationOptions, providerOptions, tagOptions, teamOptions, userOptions]);
  const rows: Row[] = attachments.map((attachment) => { const policy = policyByName.get(attachment.policy_name); return { id: attachment.id, policy: attachment.policy_name, scope: attachment.scope === "*" ? "Global" : "Specific", dimensions: scopeDimensions(attachment), organizations: values(attachment.organizations), teams: values(attachment.teams), users: values(attachment.users), keys: values(attachment.keys), models: values(attachment.models), providers: values(attachment.providers), deployments: values(attachment.deployments), tags: values(attachment.tags), policy_status: policy ? policy.enabled ? "enabled" : "disabled" : "missing", modules: policyModules(policy), _attachment: attachment }; });
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
  return <><PageHeader eyebrow="Governance" title="Policies" description="Attach reusable guardrail policies to identity, model and provider-route scopes, then simulate runtime resolution before traffic is affected." />{error && <ErrorState message={error} retry={() => void load()} />}{directoryWarning && <section className="notice-card" role="status"><p>{directoryWarning}</p></section>}<PageTabs label="Policy views" value={tab} items={[{ value: "attachments", label: "Attachments" }, { value: "simulator", label: "Policy Simulator" }]} onUpdate={selectTab} />
    {tab === "attachments" ? <><div className="usage-stats-grid"><StatCard label="Attachments" value={attachments.length.toLocaleString()} /><StatCard label="Global" value={attachments.filter((item) => item.scope === "*").length.toLocaleString()} /><StatCard label="Policies in use" value={new Set(attachments.map((item) => item.policy_name)).size.toLocaleString()} /><StatCard label="Fail-closed risks" value={broken.toLocaleString()} detail="missing or disabled policies" /></div><section className="notice-card"><h2>Composition semantics</h2><p>All matching attachments apply. Their enabled policies are combined: scanner requirements accumulate, strict anonymization wins and selected rule sets are merged. Missing or disabled attached policies fail closed.</p></section><section className="section-block"><h2>Policy attachments</h2><p>Specific scopes use AND across dimensions and OR within each dimension.</p><ManagedDataTable rows={rows} columns={[{ key: "id", label: "Attachment" }, { key: "policy", label: "Policy" }, { key: "scope", label: "Scope" }, { key: "dimensions", label: "Dimensions" }, { key: "modules", label: "Controls" }, { key: "policy_status", label: "Policy state" }, { key: "organizations", label: "Organizations" }, { key: "teams", label: "Teams" }, { key: "users", label: "Users" }, { key: "keys", label: "Keys" }, { key: "models", label: "Models" }, { key: "providers", label: "Providers" }, { key: "deployments", label: "Deployments" }, { key: "tags", label: "Tags" }]} defaultHidden={["organizations", "teams", "users", "keys", "models", "providers", "deployments", "tags"]} primaryAction={<button onClick={() => setEditing(null)}>Create Policy Attachment</button>} onRefresh={load} searchPlaceholder="Search policy attachments" actions={(row) => { const attachment = row._attachment as PolicyAttachment; return <ActionsMenu label={`Actions for policy attachment ${attachment.id}`} items={[{ label: "Inspect", onSelect: () => setInspecting(attachment) }, { label: "Edit", onSelect: () => setEditing(attachment) }, { label: "Open simulator", onSelect: () => selectTab("simulator") }, { label: "Delete", tone: "danger", onSelect: () => remove(attachment) }]} />; }} /></section></> : <PolicySimulator client={client} selectorOptions={selectorOptions} initialContext={initialSimulatorContext} />}
    {editing !== undefined && <AttachmentForm initial={editing || undefined} seed={editing === null ? attachmentSeed : undefined} policies={policies} selectorOptions={selectorOptions} client={client} onClose={closeEditor} onSaved={() => load()} />}{inspecting && <AttachmentDetails attachment={inspecting} policy={policyByName.get(inspecting.policy_name)} onClose={() => setInspecting(undefined)} onEdit={() => { setInspecting(undefined); setEditing(inspecting); }} onDelete={() => void remove(inspecting)} onSimulate={() => { setInspecting(undefined); selectTab("simulator"); }} />}
  </>;
}
