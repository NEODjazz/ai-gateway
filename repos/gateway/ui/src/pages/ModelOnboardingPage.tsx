import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import { useSearchParams } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { PageHeader } from "../components/PageHeader";
import type { Row } from "../components/DataTable";
import { ChipMultiSelect } from "../components/ChipMultiSelect";
import { defaultModelCapabilities, providerModelCapabilityOptions } from "../modelCapabilities";

type Provider = { id: string; type: string; base_url: string; api_version?: string; auth_type?: string; region?: string; enabled: boolean };
type Credential = { id: string; provider_id?: string; description?: string };
type Catalog = { version?: string; unknown_model_policy?: string; models?: Row[]; [key: string]: unknown };
type ModelGroup = { id: string; deployment_ids: string[]; strategy: string; enabled: boolean };
type OnboardingPlan = { revision: number; catalog_version: string; deployments: Row[]; model_groups: Row[]; changes: string[] };
type ProviderCapabilityProfile = { type: string; operations: string[]; capabilities?: string[]; auth_types?: string[] };
type Candidate = {
  upstream: string;
  modelName?: string;
  publisher?: string;
  selected: boolean;
  publicModel: string;
  deploymentID: string;
  capabilities: string[];
  inputCost: string;
  outputCost: string;
  currency: string;
};

function records<T>(payload: unknown): T[] {
  if (Array.isArray(payload)) return payload as T[];
  const data = payload && typeof payload === "object" ? (payload as { data?: unknown }).data : undefined;
  return Array.isArray(data) ? data as T[] : [];
}

function safeID(value: string) {
  return value.trim().replace(/[^a-zA-Z0-9._:-]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 128);
}

function requiresVerifiedCapabilities(providerType: string) {
  return providerType === "ollama" || providerType === "azure-openai";
}

function discoveredModelLabel(candidate: Candidate) {
  if (!candidate.modelName) return candidate.upstream;
  return `${candidate.upstream} — ${candidate.modelName}${candidate.publisher ? ` (${candidate.publisher})` : ""}`;
}

function defaultProviderAuthTypes(providerType: string) {
  if (providerType === "azure-openai") return ["api_key", "entra"];
  if (providerType === "gemini") return ["api_key", "gcp_adc"];
  if (providerType === "vertex-gemini") return ["gcp_adc"];
  if (providerType === "bedrock") return ["bearer", "aws_sigv4"];
  return [];
}

export function ModelOnboardingPage() {
  const { client } = useAuth();
  const [searchParams] = useSearchParams();
  const requestedProviderID = searchParams.get("provider_id") || "";
  const requestedCredentialID = searchParams.get("credential_id") || "";
  const [providers, setProviders] = useState<Provider[]>([]);
  const [providerCapabilities, setProviderCapabilities] = useState<Record<string, string[]>>({});
  const [providerAuthTypes, setProviderAuthTypes] = useState<Record<string, string[]>>({});
  const [credentials, setCredentials] = useState<Credential[]>([]);
  const [catalog, setCatalog] = useState<Catalog>();
  const [groups, setGroups] = useState<ModelGroup[]>([]);
  const [providerID, setProviderID] = useState(requestedProviderID);
  const [credentialID, setCredentialID] = useState(requestedCredentialID);
  const [newProvider, setNewProvider] = useState({ id: "", type: "openai-compatible", base_url: "", api_version: "", auth_type: "", region: "" });
  const [newCredential, setNewCredential] = useState({ id: "", description: "", secret: "" });
  const [manualModel, setManualModel] = useState("");
  const [candidates, setCandidates] = useState<Candidate[]>([]);
  const [probe, setProbe] = useState<Row>();
  const [runtime, setRuntime] = useState({ priority: 0, weight: 1, max_retries: 1, request_timeout_ms: 30000, cooldown_after_failures: 3, cooldown_seconds: 30 });
  const [step, setStep] = useState(1);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<Row>();
  const [serverPlan, setServerPlan] = useState<OnboardingPlan>();

  const load = useCallback(async () => {
    setLoading(true); setError("");
    try {
      const [providerPayload, credentialPayload, catalogPayload, groupPayload, capabilityPayload] = await Promise.all([
        client.request("/admin/v1/providers"), client.request("/admin/v1/credentials"),
        client.request<Catalog>("/admin/v1/model-catalog"), client.request("/admin/v1/model-groups"),
        client.request<{ data?: ProviderCapabilityProfile[] }>("/admin/v1/provider-capabilities").catch(() => ({ data: [] }))
      ]);
      const loadedProviders = records<Provider>(providerPayload);
      const loadedCredentials = records<Credential>(credentialPayload);
      setProviders(loadedProviders); setCredentials(loadedCredentials);
      setProviderCapabilities(Object.fromEntries((capabilityPayload.data || []).map((profile) => [profile.type, profile.capabilities || profile.operations])));
      setProviderAuthTypes(Object.fromEntries((capabilityPayload.data || []).map((profile) => [profile.type, profile.auth_types || []])));
      setCatalog(catalogPayload); setGroups(records<ModelGroup>(groupPayload));
      setProviderID((current) => loadedProviders.some((provider) => provider.id === current) ? current : loadedProviders[0]?.id || "");
      setCredentialID((current) => loadedCredentials.some((credential) => credential.id === current && (!credential.provider_id || credential.provider_id === (requestedProviderID || loadedProviders[0]?.id))) ? current : "");
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load onboarding data"); }
    finally { setLoading(false); }
  }, [client, requestedProviderID]);
  useEffect(() => { void load(); }, [load]);

  const selectedProvider = providers.find((provider) => provider.id === providerID);
  const selectedProviderType = selectedProvider?.type || newProvider.type;
  const manualProvider = selectedProviderType === "vertex-gemini";
  const capabilityOptions = providerModelCapabilityOptions(providerCapabilities[selectedProviderType]);
  const newProviderAuthTypes = providerAuthTypes[newProvider.type] ?? defaultProviderAuthTypes(newProvider.type);
  const availableCredentials = manualProvider ? [] : credentials.filter((credential) => !credential.provider_id || credential.provider_id === providerID);
  const selectedCandidates = candidates.filter((candidate) => candidate.selected);
  const validCost = (value: string) => value.trim() !== "" && Number.isFinite(Number(value)) && Number(value) >= 0;
  const hasAzurePricing = (candidate: Candidate) => selectedProviderType !== "azure-openai" ||
    (validCost(candidate.inputCost) && validCost(candidate.outputCost) && /^[A-Za-z]{3}$/.test(candidate.currency.trim()));
  const canReview = selectedCandidates.length > 0 && selectedCandidates.every((candidate) =>
    candidate.publicModel.trim() && candidate.deploymentID.trim() && hasAzurePricing(candidate));
  const canApply = canReview && selectedCandidates.every((candidate) => candidate.capabilities.length > 0);
  const groupedCandidates = useMemo(() => {
    const grouped = new Map<string, Candidate[]>();
    for (const candidate of selectedCandidates) grouped.set(candidate.publicModel, [...(grouped.get(candidate.publicModel) || []), candidate]);
    return grouped;
  }, [selectedCandidates]);

  async function ensureProviderAndCredential() {
    let nextProviderID = providerID;
    let nextProviderType = selectedProvider?.type || newProvider.type;
    if (providerID === "__new") {
      const providerInput = newProvider.type === "azure-openai" || newProvider.type === "gemini" || newProvider.type === "vertex-gemini" || newProvider.type === "bedrock" ? newProvider : { id: newProvider.id, type: newProvider.type, base_url: newProvider.base_url };
      const created = await client.request<Provider>("/admin/v1/providers", { method: "POST", body: { ...providerInput, enabled: true } });
      nextProviderID = created.id;
      nextProviderType = created.type;
      setProviders((current) => [...current, created]); setProviderID(created.id);
    }
    let nextCredentialID = credentialID;
    if (nextProviderType !== "vertex-gemini" && credentialID === "__new") {
      const created = await client.request<Credential>("/admin/v1/credentials", { method: "POST", body: { ...newCredential, provider_id: nextProviderID } });
      nextCredentialID = created.id;
      setCredentials((current) => [...current, created]); setCredentialID(created.id);
    }
    return { providerID: nextProviderID, providerType: nextProviderType, credentialID: nextCredentialID };
  }

  async function discover(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError(""); setProbe(undefined);
    try {
      const selected = await ensureProviderAndCredential();
      if (selected.providerType === "vertex-gemini") {
        const upstream = manualModel.trim();
        if (!upstream) throw new Error("Enter an upstream model");
        const capabilities = upstream.toLowerCase().includes("embedding") ? ["embeddings"] : defaultModelCapabilities(selected.providerType);
        setCredentialID("");
        setCandidates([{ upstream, selected: true, publicModel: upstream, deploymentID: safeID(`${selected.providerID}-${upstream}`), capabilities, inputCost: "", outputCost: "", currency: "USD" }]);
        setStep(2);
        return;
      }
      const body = { credential_id: selected.credentialID && selected.credentialID !== "__none" ? selected.credentialID : "" };
      const [probeResult, discovery] = await Promise.all([
        client.request<Row>(`/admin/v1/providers/${encodeURIComponent(selected.providerID)}/test`, { method: "POST", body }),
        client.request<{ data?: Array<{ id: string; capabilities?: string[]; model_name?: string; model_publisher?: string }> }>(`/admin/v1/providers/${encodeURIComponent(selected.providerID)}/discover-models`, { method: "POST", body })
      ]);
      setProbe(probeResult);
      setCandidates((discovery.data || []).map(({ id, capabilities, model_name, model_publisher }) => ({ upstream: id, modelName: model_name, publisher: model_publisher, selected: false, publicModel: id, deploymentID: safeID(`${selected.providerID}-${id}`), capabilities: requiresVerifiedCapabilities(selected.providerType) ? capabilities || [] : defaultModelCapabilities(selected.providerType), inputCost: "", outputCost: "", currency: "USD" })));
      setStep(2);
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Provider discovery failed"); }
    finally { setBusy(false); }
  }

  function updateCandidate(index: number, patch: Partial<Candidate>) {
    setCandidates((current) => current.map((candidate, candidateIndex) => candidateIndex === index ? { ...candidate, ...patch } : candidate));
  }

  function onboardingInput() {
    if (!catalog) throw new Error("Model catalog is unavailable");
    const nextModels = [...(catalog.models || [])];
    for (const candidate of selectedCandidates) {
      const key = `${providerID}\u0000${candidate.publicModel}`;
      const index = nextModels.findIndex((entry) => `${String(entry.provider)}\u0000${String(entry.model)}` === key);
      const entry: Row = {
        provider: providerID, model: candidate.publicModel,
        capabilities: [...candidate.capabilities],
        input_cost_per_1m: Number(candidate.inputCost || 0), output_cost_per_1m: Number(candidate.outputCost || 0),
        currency: candidate.inputCost || candidate.outputCost ? candidate.currency.trim().toUpperCase() : ""
      };
      if (index >= 0) nextModels[index] = entry; else nextModels.push(entry);
    }
    const deployments = selectedCandidates.map((candidate) => ({
      id: candidate.deploymentID, provider_id: providerID,
      credential_id: credentialID && credentialID !== "__none" ? credentialID : "",
      upstream_model: candidate.upstream, models: [candidate.publicModel],
      capabilities: [...candidate.capabilities],
      ...runtime, enabled: true
    }));
    const model_groups = [...groupedCandidates].map(([publicModel, modelCandidates]) => {
      const existing = groups.find((group) => group.id === publicModel);
      return { id: publicModel, deployment_ids: [...new Set([...(existing?.deployment_ids || []), ...modelCandidates.map((candidate) => candidate.deploymentID)])], strategy: existing?.strategy || "weighted", enabled: true };
    });
    return { catalog: { ...catalog, version: `onboarding-${Date.now()}`, models: nextModels }, deployments, model_groups };
  }

  async function review() {
    if (!canReview) return;
    setBusy(true); setError(""); setServerPlan(undefined);
    try {
      const plan = await client.request<OnboardingPlan>("/admin/v1/model-onboarding/plan", { method: "POST", body: onboardingInput() });
      setServerPlan(plan); setStep(3);
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not validate onboarding plan"); }
    finally { setBusy(false); }
  }

  async function apply() {
    if (!catalog || !canApply) return;
    setBusy(true); setError(""); setResult(undefined);
    try {
      const input = onboardingInput();
      const plan = await client.request<OnboardingPlan>("/admin/v1/model-onboarding/plan", { method: "POST", body: input });
      const applied = await client.request<OnboardingPlan>("/admin/v1/model-onboarding/apply", { method: "POST", body: { ...input, expected_revision: plan.revision } });
      setCatalog(input.catalog); setServerPlan(applied); setResult(applied as unknown as Row); setStep(4);
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not apply onboarding plan"); }
    finally { setBusy(false); }
  }

  if (loading) return <><PageHeader eyebrow="Models & endpoints" title="Model onboarding" description="Discover and configure provider models without copying resource IDs between pages." /><LoadingState /></>;
  return <><PageHeader eyebrow="Models & endpoints" title="Model onboarding" description="A guided workflow over independent providers, credentials, catalog entries, deployments and model groups." actions={<button className="secondary" onClick={() => void load()}>Refresh</button>} />
    <ol className="wizard-steps" aria-label="Onboarding progress">{["Connection", "Models", "Review", "Complete"].map((label, index) => <li key={label} className={step === index + 1 ? "active" : step > index + 1 ? "complete" : ""}>{index + 1}. {label}</li>)}</ol>
    {error && <ErrorState message={error} />}
    {step === 1 && <form className="form-card" onSubmit={discover}><div className="form-grid"><label>Provider<select required value={providerID} onChange={(event) => { setProviderID(event.target.value); setCredentialID(""); setManualModel(""); }}><option value="">Select configured provider</option>{providers.map((provider) => <option key={provider.id} value={provider.id}>{provider.id} — {provider.type}</option>)}<option value="__new">+ Create provider</option></select></label>{providerID === "__new" && <><label>Provider ID<input required value={newProvider.id} onChange={(event) => setNewProvider((value) => ({ ...value, id: event.target.value }))} /></label><label>Provider type<select value={newProvider.type} onChange={(event) => { const type = event.target.value; const authTypes = providerAuthTypes[type] ?? defaultProviderAuthTypes(type); setNewProvider((value) => ({ ...value, type, api_version: "", auth_type: authTypes[0] || "", region: "" })); setCredentialID(""); setManualModel(""); }}>{["ollama", "openai", "openai-compatible", "openrouter", "azure-openai", "anthropic", "gemini", "vertex-gemini", "cohere", "mistral", "voyage", "bedrock", "groq", "deepseek", "cerebras", "nvidia-nim", "together", "xai", "demo"].map((type) => <option key={type}>{type}</option>)}</select></label><label>Base URL<input required value={newProvider.base_url} onChange={(event) => setNewProvider((value) => ({ ...value, base_url: event.target.value }))} /></label>{newProvider.type === "azure-openai" && <><label>Azure API version<input value={newProvider.api_version} placeholder="Optional for /openai/v1" onChange={(event) => setNewProvider((value) => ({ ...value, api_version: event.target.value }))} /></label><label>Azure authentication<select value={newProvider.auth_type} onChange={(event) => setNewProvider((value) => ({ ...value, auth_type: event.target.value }))}>{newProviderAuthTypes.map((authType) => <option key={authType}>{authType}</option>)}</select></label></>}{(newProvider.type === "gemini" || newProvider.type === "vertex-gemini") && <label>Google authentication<select value={newProvider.auth_type} onChange={(event) => setNewProvider((value) => ({ ...value, auth_type: event.target.value }))}>{newProviderAuthTypes.map((authType) => <option key={authType}>{authType}</option>)}</select></label>}{newProvider.type === "bedrock" && <><label>Bedrock authentication<select value={newProvider.auth_type} onChange={(event) => setNewProvider((value) => ({ ...value, auth_type: event.target.value }))}>{newProviderAuthTypes.map((authType) => <option key={authType}>{authType}</option>)}</select></label><label>AWS region<input required={newProvider.auth_type === "aws_sigv4"} value={newProvider.region} placeholder="us-east-1" onChange={(event) => setNewProvider((value) => ({ ...value, region: event.target.value }))} /></label></>}</>}{manualProvider ? <label>Upstream model<input required value={manualModel} onChange={(event) => setManualModel(event.target.value)} placeholder="gemini-2.5-pro" /></label> : <><label>Credential<select value={credentialID} onChange={(event) => setCredentialID(event.target.value)}><option value="__none">No credential</option>{availableCredentials.map((credential) => <option key={credential.id} value={credential.id}>{credential.id}{credential.description ? ` — ${credential.description}` : ""}</option>)}<option value="__new">+ Create credential</option></select></label>{credentialID === "__new" && <><label>Credential ID<input required value={newCredential.id} onChange={(event) => setNewCredential((value) => ({ ...value, id: event.target.value }))} /></label><label>Description<input value={newCredential.description} onChange={(event) => setNewCredential((value) => ({ ...value, description: event.target.value }))} /></label><label>Secret<input required type="password" value={newCredential.secret} onChange={(event) => setNewCredential((value) => ({ ...value, secret: event.target.value }))} /></label></>}</>}</div><button disabled={busy || (!selectedProvider && providerID !== "__new")}>{busy ? manualProvider ? "Preparing…" : "Testing and discovering…" : manualProvider ? "Configure model" : "Test & discover models"}</button></form>}
    {step === 2 && <section className="form-card"><div><h2>{manualProvider ? "Configured model" : "Discovered models"}</h2>{probe && <p className="muted">Provider {String(probe.status)} · {String(probe.latency_ms)} ms · {String(probe.model_count)} models</p>}{requiresVerifiedCapabilities(selectedProviderType) && <p className="muted">Capabilities are suggested only when provider metadata verifies them. Choose capabilities explicitly during review when none are shown.</p>}</div><div className="candidate-list">{candidates.map((candidate, index) => <label className="candidate-row" key={candidate.upstream}><input type="checkbox" checked={candidate.selected} onChange={(event) => updateCandidate(index, { selected: event.target.checked })} /><span>{discoveredModelLabel(candidate)}</span><input aria-label={`Public model ${candidate.upstream}`} disabled={!candidate.selected} value={candidate.publicModel} onChange={(event) => updateCandidate(index, { publicModel: event.target.value })} placeholder="Public model" /><input aria-label={`Deployment ID ${candidate.upstream}`} disabled={!candidate.selected} value={candidate.deploymentID} onChange={(event) => updateCandidate(index, { deploymentID: event.target.value })} placeholder="Deployment ID" /></label>)}</div>{selectedProviderType === "azure-openai" && <><p className="muted">Enter input and output cost per million tokens for each selected model, including 0 if free, before validating the plan.</p><div className="form-grid">{selectedCandidates.map((candidate) => { const candidateIndex = candidates.findIndex((item) => item.upstream === candidate.upstream); return <div key={candidate.deploymentID}><strong>{candidate.upstream}</strong><label>Input cost {candidate.upstream}<input aria-label={`Input cost ${candidate.upstream}`} type="number" min="0" step="any" value={candidate.inputCost} onChange={(event) => updateCandidate(candidateIndex, { inputCost: event.target.value })} /></label><label>Output cost {candidate.upstream}<input aria-label={`Output cost ${candidate.upstream}`} type="number" min="0" step="any" value={candidate.outputCost} onChange={(event) => updateCandidate(candidateIndex, { outputCost: event.target.value })} /></label><label>Currency {candidate.upstream}<input aria-label={`Currency ${candidate.upstream}`} maxLength={3} value={candidate.currency} onChange={(event) => updateCandidate(candidateIndex, { currency: event.target.value })} /></label></div>; })}</div></>}<div className="modal-actions"><button className="secondary" onClick={() => setStep(1)}>Back</button><button disabled={!canReview || busy} onClick={() => void review()}>{busy ? "Validating…" : `Review ${selectedCandidates.length} model(s)`}</button></div></section>}
    {step === 3 && <section className="form-card"><h2>Review onboarding plan</h2>{serverPlan && <p className="muted">Validated against control-plane revision {serverPlan.revision}. {serverPlan.changes.length} changes will be committed together.</p>}<div className="form-grid"><label>Priority<input type="number" value={runtime.priority} onChange={(event) => setRuntime((value) => ({ ...value, priority: Number(event.target.value) }))} /></label><label>Weight<input type="number" min="1" value={runtime.weight} onChange={(event) => setRuntime((value) => ({ ...value, weight: Number(event.target.value) }))} /></label><label>Max retries<input type="number" min="0" value={runtime.max_retries} onChange={(event) => setRuntime((value) => ({ ...value, max_retries: Number(event.target.value) }))} /></label><label>Request timeout (ms)<input type="number" min="0" value={runtime.request_timeout_ms} onChange={(event) => setRuntime((value) => ({ ...value, request_timeout_ms: Number(event.target.value) }))} /></label><label>Failures before cooldown<input type="number" min="0" value={runtime.cooldown_after_failures} onChange={(event) => setRuntime((value) => ({ ...value, cooldown_after_failures: Number(event.target.value) }))} /></label><label>Cooldown seconds<input type="number" min="0" value={runtime.cooldown_seconds} onChange={(event) => setRuntime((value) => ({ ...value, cooldown_seconds: Number(event.target.value) }))} /></label></div><div className="table-card"><table><thead><tr><th>Upstream</th><th>Public model / group</th><th>Deployment</th><th>Capabilities</th><th>Input / 1M</th><th>Output / 1M</th><th>Currency</th></tr></thead><tbody>{selectedCandidates.map((candidate) => { const candidateIndex = candidates.findIndex((item) => item.upstream === candidate.upstream); return <tr key={candidate.deploymentID}><td>{candidate.upstream}</td><td>{candidate.publicModel}</td><td>{candidate.deploymentID}</td><td><ChipMultiSelect label="Capabilities" ariaLabel={`Capabilities ${candidate.upstream}`} options={capabilityOptions} value={candidate.capabilities} onChange={(capabilities) => updateCandidate(candidateIndex, { capabilities })} controlOnly /></td><td>{selectedProviderType === "azure-openai" ? candidate.inputCost : <input aria-label={`Input cost ${candidate.upstream}`} type="number" min="0" step="any" value={candidate.inputCost} onChange={(event) => updateCandidate(candidateIndex, { inputCost: event.target.value })} />}</td><td>{selectedProviderType === "azure-openai" ? candidate.outputCost : <input aria-label={`Output cost ${candidate.upstream}`} type="number" min="0" step="any" value={candidate.outputCost} onChange={(event) => updateCandidate(candidateIndex, { outputCost: event.target.value })} />}</td><td>{selectedProviderType === "azure-openai" ? candidate.currency : <input aria-label={`Currency ${candidate.upstream}`} maxLength={3} value={candidate.currency} onChange={(event) => updateCandidate(candidateIndex, { currency: event.target.value })} />}</td></tr>; })}</tbody></table></div>{selectedProviderType === "azure-openai" && <p className="muted">To change Azure prices, go back and validate a new plan.</p>}<p className="muted">The server revalidates the plan and commits catalog, deployments and model groups in one optimistic control-plane transaction.</p><div className="modal-actions"><button className="secondary" onClick={() => setStep(2)}>Back</button><button disabled={busy || !canApply} onClick={() => void apply()}>{busy ? "Applying…" : "Apply configuration"}</button></div></section>}
    {step === 4 && <section className="operation-result"><strong>Onboarding complete</strong><pre>{JSON.stringify(result, null, 2)}</pre><button onClick={() => { setCandidates([]); setProbe(undefined); setResult(undefined); setStep(1); void load(); }}>Onboard more models</button></section>}
  </>;
}
