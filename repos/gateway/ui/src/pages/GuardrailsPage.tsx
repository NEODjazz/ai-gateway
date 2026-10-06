import { ModalFrame } from "../components/ModalFrame";
import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { type APIClient } from "../api/client";
import { useAuth } from "../auth/AuthContext";
import { ActionsMenu } from "../components/ActionsMenu";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { ChipMultiSelect } from "../components/ChipMultiSelect";
import { ManagedDataTable } from "../components/ManagedDataTable";
import { PageHeader } from "../components/PageHeader";
import { ResourceForm, type Field } from "../components/ResourceForm";
import { StatCard } from "../components/StatCard";
import type { Row } from "../components/DataTable";

type PromptInjection = { heuristics_check: boolean; llm_api_check: boolean; similarity_threshold?: number; judge_deployment_id?: string; judge_system_prompt?: string; safe_response?: string; unsafe_response?: string; fail_on_error?: boolean; skip_unscannable_attachments: boolean; timeout_seconds?: number; max_input_bytes?: number };
type Policy = { prompt_injection?: PromptInjection; name: string; description?: string; dlp: boolean; output_dlp: boolean; av: boolean; anonymization?: "disabled" | "basic" | "strict" | "custom"; anonymization_rules?: string[]; enabled: boolean };
type PolicyAttachment = { id: string; policy_name: string; scope: string; teams?: string[]; keys?: string[]; models?: string[]; tags?: string[] };
type Deployment = { id: string; guardrail_policy?: string; capabilities?: string[]; enabled: boolean; runtime_state?: string };
type ComplianceResult = { request_id: string; policy: string; allowed: boolean; checks: Record<string, "passed" | "rejected" | "unavailable" | "disabled">; content_stored: false; anonymized_text?: string; replacements: number };
type TestOutcome = { policy: string; latencyMS: number; result?: ComplianceResult; error?: string };

function policyFields(anonymizerRules: string[], deployments: Deployment[]): Field[] { return [
  { key: "name", label: "Policy name", required: true, readOnlyOnEdit: true, placeholder: "production-strict" },
  { key: "description", label: "Description", type: "textarea", placeholder: "What this policy protects" },
  { key: "dlp", label: "Run DLP scanner", type: "boolean", defaultValue: true },
  { key: "output_dlp", label: "Scan provider output before delivery", type: "boolean" },
  { key: "av", label: "Run antivirus scanner", type: "boolean" },
  { key: "anonymization", label: "Anonymization profile", type: "select", options: ["disabled", "basic", "strict", "custom"], placeholder: "Use safe global default" },
  { key: "anonymization_rules", label: "Custom anonymization rules", type: "multi-select", options: anonymizerRules, visibleWhen: { fieldKey: "anonymization", equals: "custom" } },
  { key: "prompt_injection_mode", label: "Prompt injection detection", type: "select", options: ["disabled", "heuristics", "llm", "heuristics+llm"], defaultValue: "disabled" },
  { key: "similarity_threshold", label: "Heuristic similarity threshold (0.75–1)", type: "number", defaultValue: 0.85, visibleWhen: { fieldKey: "prompt_injection_mode", oneOf: ["heuristics", "heuristics+llm"] } },
  { key: "judge_deployment_id", label: "Classifier deployment", type: "select", options: deployments.filter((item) => item.enabled && (!item.capabilities?.length || item.capabilities.includes("chat"))).map((item) => item.id), required: true, visibleWhen: { fieldKey: "prompt_injection_mode", oneOf: ["llm", "heuristics+llm"] } },
  { key: "judge_system_prompt", label: "Classifier system instructions (optional)", type: "textarea", visibleWhen: { fieldKey: "prompt_injection_mode", oneOf: ["llm", "heuristics+llm"] } },
  { key: "safe_response", label: "Exact safe verdict", defaultValue: "SAFE", visibleWhen: { fieldKey: "prompt_injection_mode", oneOf: ["llm", "heuristics+llm"] } },
  { key: "unsafe_response", label: "Exact unsafe verdict", defaultValue: "UNSAFE", visibleWhen: { fieldKey: "prompt_injection_mode", oneOf: ["llm", "heuristics+llm"] } },
  { key: "fail_on_error", label: "Block when detection is unavailable", type: "boolean", defaultValue: true, visibleWhen: { fieldKey: "prompt_injection_mode", oneOf: ["heuristics", "llm", "heuristics+llm"] } },
  { key: "skip_unscannable_attachments", label: "Allow attachments the detector cannot read", type: "boolean", visibleWhen: { fieldKey: "prompt_injection_mode", oneOf: ["heuristics", "llm", "heuristics+llm"] } },
  { key: "timeout_seconds", label: "Detection timeout in seconds (1–30)", type: "number", defaultValue: 5, visibleWhen: { fieldKey: "prompt_injection_mode", oneOf: ["heuristics", "llm", "heuristics+llm"] } },
  { key: "max_input_bytes", label: "Maximum scanned UTF-8 bytes (1024–1048576)", type: "number", defaultValue: 262144, visibleWhen: { fieldKey: "prompt_injection_mode", oneOf: ["heuristics", "llm", "heuristics+llm"] } },
  { key: "enabled", label: "Enabled", type: "boolean", defaultValue: true },
]; }

function records<T>(payload: unknown): T[] {
  if (Array.isArray(payload)) return payload as T[];
  const data = payload && typeof payload === "object" ? (payload as { data?: unknown }).data : undefined;
  return Array.isArray(data) ? data as T[] : [];
}

function checksLabel(policy: Policy) {
  return [policy.prompt_injection && "Prompt injection", policy.dlp && "Input DLP", policy.output_dlp && "Output DLP", policy.av && "Antivirus", policy.anonymization && `Anonymizer: ${policy.anonymization}`].filter(Boolean) as string[];
}

function GuardrailPolicyForm({ initial, rules, deployments, client, onClose, onSaved }: { initial?: Policy; rules: string[]; deployments: Deployment[]; client: APIClient; onClose: () => void; onSaved: () => Promise<void> }) {
  const fields = useMemo(() => policyFields(rules, deployments), [rules, deployments]);
  const initialValues = useMemo(() => initial ? { ...initial, ...initial.prompt_injection, prompt_injection_mode: initial.prompt_injection ? initial.prompt_injection.heuristics_check ? initial.prompt_injection.llm_api_check ? "heuristics+llm" : "heuristics" : "llm" : "disabled" } as unknown as Row : undefined, [initial]);
  async function save(value: Row) {
    const name = String(value.name || "").trim();
    const dlp = Boolean(value.dlp);
    const outputDLP = Boolean(value.output_dlp);
    const av = Boolean(value.av);
    const anonymization = String(value.anonymization || "");
    const anonymizationRules = Array.isArray(value.anonymization_rules) ? value.anonymization_rules.map(String) : [];
    if (outputDLP && !dlp) throw new Error("Output DLP requires the DLP scanner");
    const mode = String(value.prompt_injection_mode || "disabled");
    let promptInjection: PromptInjection | undefined;
    if (mode !== "disabled") {
      const llm = mode === "llm" || mode === "heuristics+llm";
      const threshold = Number(value.similarity_threshold ?? initial?.prompt_injection?.similarity_threshold ?? 0.85);
      const timeout = Number(value.timeout_seconds);
      const maxBytes = Number(value.max_input_bytes);
      if (threshold < 0.75 || threshold > 1 || !Number.isFinite(threshold)) throw new Error("Similarity threshold must be between 0.75 and 1");
      if (!Number.isInteger(timeout) || timeout < 1 || timeout > 30) throw new Error("Detection timeout must be between 1 and 30 seconds");
      if (!Number.isInteger(maxBytes) || maxBytes < 1024 || maxBytes > 1048576) throw new Error("Scanned input limit must be between 1024 and 1048576 bytes");
      if (llm && !value.judge_deployment_id) throw new Error("Select a classifier deployment");
      const safe = String(value.safe_response || "SAFE").trim();
      const unsafe = String(value.unsafe_response || "UNSAFE").trim();
      if (llm && (safe.toLowerCase() === unsafe.toLowerCase() || /\s/.test(safe + unsafe) || new TextEncoder().encode(safe).length > 64 || new TextEncoder().encode(unsafe).length > 64)) throw new Error("Verdicts must be distinct single values of up to 64 UTF-8 bytes");
      if (llm && new TextEncoder().encode(String(value.judge_system_prompt || "")).length > 8192) throw new Error("Classifier system instructions must not exceed 8192 UTF-8 bytes");
      promptInjection = { heuristics_check: mode !== "llm", llm_api_check: llm, similarity_threshold: threshold, fail_on_error: Boolean(value.fail_on_error), skip_unscannable_attachments: Boolean(value.skip_unscannable_attachments), timeout_seconds: timeout, max_input_bytes: maxBytes,
        ...(llm ? { judge_deployment_id: String(value.judge_deployment_id), judge_system_prompt: String(value.judge_system_prompt || "") || undefined, safe_response: safe, unsafe_response: unsafe } : {}) };
    }
    if (!dlp && !av && !anonymization && !promptInjection) throw new Error("Select at least one scanner, prompt injection detector or anonymization profile");
    if (anonymization === "custom" && !anonymizationRules.length) throw new Error("Select at least one custom anonymization rule");
    await client.request(`/admin/v1/guardrail-policies/${encodeURIComponent(name)}`, { method: "PUT", body: { prompt_injection: promptInjection, description: String(value.description || ""), dlp, output_dlp: outputDLP, av, anonymization: anonymization || undefined, anonymization_rules: anonymization === "custom" ? anonymizationRules : undefined, enabled: Boolean(value.enabled) } });
    onClose();
    await onSaved();
  }
  return <ResourceForm title={initial ? `Edit ${initial.name}` : "Create Guardrail Policy"} fields={fields} initial={initialValues} onClose={onClose} onSubmit={save} />;
}

function PolicyDetails({ policy, attachments, deployments, onClose, onEdit, onTest, onMonitor, onAttachments }: { policy: Policy; attachments: PolicyAttachment[]; deployments: Deployment[]; onClose: () => void; onEdit: () => void; onTest: () => void; onMonitor: () => void; onAttachments: () => void }) {
  const linkedAttachments = attachments.filter((item) => item.policy_name === policy.name);
  const linkedDeployments = deployments.filter((item) => item.guardrail_policy === policy.name);
  return <ModalFrame label={`Guardrail policy ${policy.name}`} onClose={onClose}><section className="modal guardrail-details-modal">
    <div className="modal-heading"><div><span className="eyebrow">Guardrail policy</span><h2>{policy.name}</h2></div><button className="icon-button" aria-label="Close policy details" onClick={onClose}>×</button></div>
    <dl className="detail-grid"><div><dt>Status</dt><dd><span className={`status ${policy.enabled ? "enabled" : "disabled"}`}>{policy.enabled ? "Enabled" : "Disabled"}</span></dd></div><div><dt>Controls</dt><dd><div className="tag-list">{checksLabel(policy).map((item) => <span className="tag" key={item}>{item}</span>)}</div></dd></div><div><dt>Anonymization rules</dt><dd>{policy.anonymization === "custom" ? policy.anonymization_rules?.join(", ") || "None" : policy.anonymization || "Safe global default"}</dd></div><div><dt>Direct deployments</dt><dd>{linkedDeployments.length ? linkedDeployments.map((item) => item.id).join(", ") : "None"}</dd></div><div><dt>Scoped attachments</dt><dd>{linkedAttachments.length ? linkedAttachments.map((item) => item.id).join(", ") : "None"}</dd></div>{policy.prompt_injection && <><div><dt>Prompt injection detector</dt><dd>{policy.prompt_injection.heuristics_check ? "Heuristics" : ""}{policy.prompt_injection.heuristics_check && policy.prompt_injection.llm_api_check ? " + " : ""}{policy.prompt_injection.llm_api_check ? `Classifier: ${policy.prompt_injection.judge_deployment_id}` : ""}</dd></div><div><dt>Detection failure</dt><dd>{policy.prompt_injection.fail_on_error !== false ? "Block request" : "Allow request and record unavailable"}</dd></div><div><dt>Unreadable attachments</dt><dd>{policy.prompt_injection.skip_unscannable_attachments ? "Allowed without attachment scanning" : "Rejected"}</dd></div><div><dt>Detection limits</dt><dd>{policy.prompt_injection.timeout_seconds || 5} seconds · {(policy.prompt_injection.max_input_bytes || 262144).toLocaleString()} bytes</dd></div></>}<div className="span-2"><dt>Description</dt><dd>{policy.description || "—"}</dd></div><div className="span-2"><dt>Runtime behavior</dt><dd>Matching profiles are combined. Strict anonymization wins; selected rule sets are merged. Disabled applies only when no matching profile requires anonymization.</dd></div></dl>
    <div className="modal-actions"><button className="secondary" onClick={onAttachments}>Policy attachments</button><button className="secondary" onClick={onMonitor}>Open monitor</button><button className="secondary" onClick={onEdit}>Edit</button><button disabled={!policy.enabled} onClick={onTest}>Test policy</button></div>
  </section></ModalFrame>;
}

function GuardrailTestDialog({ policies, initialNames, client, onClose }: { policies: Policy[]; initialNames: string[]; client: APIClient; onClose: () => void }) {
  const [selected, setSelected] = useState(initialNames);
  const [text, setText] = useState("");
  const [outcomes, setOutcomes] = useState<TestOutcome[]>();
  const [error, setError] = useState("");
  const [testing, setTesting] = useState(false);
  const options = policies.filter((policy) => policy.enabled).map((policy) => ({ value: policy.name, label: policy.name, description: `${checksLabel(policy).join(" + ")} · ${policy.description || "No description"}` }));
  const textBytes = useMemo(() => new TextEncoder().encode(text).length, [text]);

  async function run(event: FormEvent) {
    event.preventDefault();
    setError("");
    if (!selected.length) { setError("Select at least one enabled policy"); return; }
    if (!text.trim()) { setError("Enter a text projection to test"); return; }
    if (textBytes > 65_536) { setError("Text projection must not exceed 65,536 UTF-8 bytes"); return; }
    setTesting(true); setOutcomes(undefined);
    const next = await Promise.all(selected.map(async (policy): Promise<TestOutcome> => {
      const started = Date.now();
      try {
        const result = await client.request<ComplianceResult>("/admin/v1/compliance/check", { method: "POST", body: { policy, text } });
        return { policy, latencyMS: Date.now() - started, result };
      } catch (cause) {
        return { policy, latencyMS: Date.now() - started, error: cause instanceof Error ? cause.message : "Compliance check failed" };
      }
    }));
    setOutcomes(next); setTesting(false);
  }

  function updateSelection(values: string[]) {
    if (values.length > 8) { setError("Compare at most 8 policies at once"); return; }
    setError(""); setSelected(values); setOutcomes(undefined);
  }

  return <ModalFrame label="Test guardrails" onClose={onClose} dismissDisabled={testing}><section className="modal guardrail-test-modal">
    <div className="modal-heading"><div><span className="eyebrow">Compliance playground</span><h2>Compare guardrail policies</h2></div><button className="icon-button" aria-label="Close guardrail test" disabled={testing} onClick={onClose}>×</button></div>
    <form onSubmit={run} className="guardrail-test-form"><ChipMultiSelect label="Policies" options={options} value={selected} onChange={updateSelection} /><small>Select up to 8 enabled policies. Each policy is evaluated independently so results can be compared.</small><label>Text projection<textarea aria-label="Text projection" required rows={7} value={text} onChange={(event) => { setText(event.target.value); setOutcomes(undefined); }} placeholder="Enter text to evaluate against the selected policies…" /><small>{textBytes.toLocaleString()} / 65,536 UTF-8 bytes</small></label><div className="guardrail-test-privacy">Configured DLP/AV services receive this text projection. LLM detection also sends it to the selected classifier deployment, with separate billed usage. Submitted text and raw scanner responses are not retained or returned by the guardrail monitor. Classifier provider retention follows its own policy.</div>{error && <p className="form-error" role="alert">{error}</p>}<div className="modal-actions"><button type="button" className="secondary" disabled={testing} onClick={onClose}>Close</button><button disabled={testing || !selected.length || !text.trim() || textBytes > 65_536}>{testing ? `Testing ${selected.length} policies…` : `Test ${selected.length || "selected"} policies`}</button></div></form>
    {outcomes && <section className="guardrail-test-results" aria-label="Guardrail test results"><h3>Results</h3><div className="table-card"><div className="table-scroll"><table><thead><tr><th>Policy</th><th>Decision</th><th>Checks</th><th>Anonymized preview</th><th>Latency</th><th>Request</th><th>Content stored</th></tr></thead><tbody>{outcomes.map((outcome) => <tr key={outcome.policy}><td><strong>{outcome.policy}</strong></td><td>{outcome.error ? <span className="status error">Unavailable</span> : <span className={`status ${outcome.result?.allowed ? "enabled" : "warning"}`}>{outcome.result?.allowed ? "Allowed" : "Rejected"}</span>}</td><td>{outcome.error || Object.entries(outcome.result?.checks || {}).map(([name, status]) => `${name.toUpperCase()}: ${status}`).join(" · ")}</td><td>{outcome.result ? <><code>{outcome.result.anonymized_text || "—"}</code><small>{outcome.result.replacements ?? 0} replacements</small></> : "—"}</td><td>{outcome.latencyMS} ms</td><td><code>{outcome.result?.request_id || "—"}</code></td><td>{outcome.result ? "No" : "—"}</td></tr>)}</tbody></table></div></div></section>}
  </section></ModalFrame>;
}

export function GuardrailsPage() {
  const { client } = useAuth();
  const navigate = useNavigate();
  const [policies, setPolicies] = useState<Policy[]>([]);
  const [attachments, setAttachments] = useState<PolicyAttachment[]>([]);
  const [deployments, setDeployments] = useState<Deployment[]>([]);
  const [anonymizerRules, setAnonymizerRules] = useState<string[]>([]);
  const [rulesWarning, setRulesWarning] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [editing, setEditing] = useState<Policy | null>();
  const [inspecting, setInspecting] = useState<Policy>();
  const [testNames, setTestNames] = useState<string[]>();

  const load = useCallback(async (signal?: AbortSignal) => {
    setLoading(true); setError(""); setRulesWarning("");
    try {
      const ruleRequest = client.request("/admin/v1/anonymizer/rules", { signal }).then((payload) => records<string>(payload), () => null);
      const [policyPayload, attachmentPayload, deploymentPayload] = await Promise.all([
        client.request("/admin/v1/guardrail-policies", { signal }),
        client.request("/admin/v1/policy-attachments", { signal }),
        client.request("/admin/v1/model-deployments", { signal }),
      ]);
      setPolicies(records<Policy>(policyPayload)); setAttachments(records<PolicyAttachment>(attachmentPayload)); setDeployments(records<Deployment>(deploymentPayload));
      const rules = await ruleRequest;
      if (rules) setAnonymizerRules(rules); else setRulesWarning("The active anonymizer rule inventory is unavailable. Existing profiles remain visible, but custom profiles cannot be edited safely.");
    } catch (cause) {
      if (!signal?.aborted) setError(cause instanceof Error ? cause.message : "Could not load guardrail policies");
    } finally { if (!signal?.aborted) setLoading(false); }
  }, [client]);
  useEffect(() => { const controller = new AbortController(); void load(controller.signal); return () => controller.abort(); }, [load]);

  const rows: Row[] = useMemo(() => policies.map((policy) => ({
    id: policy.name, policy: policy.name, description: policy.description || "—", scanners: checksLabel(policy),
    deployments: deployments.filter((item) => item.guardrail_policy === policy.name).length,
    attachments: attachments.filter((item) => item.policy_name === policy.name).length,
    status: policy.enabled ? "enabled" : "disabled", _policy: policy,
  })), [attachments, deployments, policies]);
  const enabledCount = policies.filter((policy) => policy.enabled).length;
  const enabledPolicies = new Set(policies.filter((policy) => policy.enabled).map((policy) => policy.name));
  const activeDeployments = new Set(deployments.filter((item) => item.enabled && enabledPolicies.has(item.guardrail_policy || "")).map((item) => item.id)).size;
  const scopedAttachments = attachments.length;
  if (loading && !policies.length) return <LoadingState />;
  if (error && !policies.length) return <ErrorState message={error} retry={() => void load()} />;

  return <><PageHeader eyebrow="Compliance" title="Guardrails" description="Configure prompt injection protection, scanners and anonymization profiles. Inspect coverage and test decisions." />
    {error && <ErrorState message={error} retry={() => void load()} />}
    {rulesWarning && <section className="notice-card" role="status"><p>{rulesWarning}</p></section>}
    <div className="usage-stats-grid"><StatCard label="Policies" value={policies.length.toLocaleString()} /><StatCard label="Enabled" value={enabledCount.toLocaleString()} /><StatCard label="Protected deployments" value={activeDeployments.toLocaleString()} detail="enabled deployments with a policy" /><StatCard label="Scoped attachments" value={scopedAttachments.toLocaleString()} detail="identity and model scopes" /></div>
    <section className="notice-card guardrail-policy-boundary"><h2>Effective protection</h2><p>The safe default anonymizes with every configured rule. Attach profiles to identities, models, providers or deployments to select rules or explicitly disable anonymization. Matching profiles are combined for each provider attempt. Prompt injection protection scans prompts, tool results and readable document text before inference and cache replay. Heuristics can flag quoted attack examples; LLM detection adds a billed classifier call. Binary attachments are rejected unless explicitly allowed without scanning.</p></section>
    <section className="section-block"><h2>Configured policies</h2><p>Use the action menu to inspect coverage, edit a policy or open a preselected compliance test.</p><ManagedDataTable rows={rows} columns={[{ key: "policy", label: "Policy" }, { key: "description", label: "Description" }, { key: "scanners", label: "Scanners" }, { key: "deployments", label: "Deployments" }, { key: "attachments", label: "Attachments" }, { key: "status", label: "Status" }]} primaryAction={<div className="page-actions"><button onClick={() => setEditing(null)}>Create Guardrail Policy</button><button className="secondary" disabled={!enabledCount} onClick={() => setTestNames([])}>Test Guardrails</button></div>} onRefresh={load} searchPlaceholder="Search guardrail policies" actions={(row) => { const policy = row._policy as Policy; return <ActionsMenu label={`Actions for guardrail policy ${policy.name}`} items={[{ label: "Inspect", onSelect: () => setInspecting(policy) }, { label: "Edit", onSelect: () => setEditing(policy) }, { label: "Test", disabled: !policy.enabled, onSelect: () => setTestNames([policy.name]) }, { label: "Open monitor", onSelect: () => navigate(`/guardrails-monitor?policy=${encodeURIComponent(policy.name)}`) }]} />; }} /></section>
    {editing !== undefined && <GuardrailPolicyForm initial={editing || undefined} rules={anonymizerRules} deployments={deployments} client={client} onClose={() => setEditing(undefined)} onSaved={() => load()} />}
    {inspecting && <PolicyDetails policy={inspecting} attachments={attachments} deployments={deployments} onClose={() => setInspecting(undefined)} onEdit={() => { setInspecting(undefined); setEditing(inspecting); }} onTest={() => { setInspecting(undefined); setTestNames([inspecting.name]); }} onMonitor={() => navigate(`/guardrails-monitor?policy=${encodeURIComponent(inspecting.name)}`)} onAttachments={() => navigate("/policies")} />}
    {testNames !== undefined && <GuardrailTestDialog policies={policies} initialNames={testNames} client={client} onClose={() => setTestNames(undefined)} />}
  </>;
}
