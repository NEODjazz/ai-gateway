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

type Policy = { name: string; description?: string; dlp: boolean; output_dlp: boolean; av: boolean; anonymization?: "disabled" | "basic" | "strict" | "custom"; anonymization_rules?: string[]; enabled: boolean };
type PolicyAttachment = { id: string; policy_name: string; scope: string; teams?: string[]; keys?: string[]; models?: string[]; tags?: string[] };
type Deployment = { id: string; guardrail_policy?: string; enabled: boolean; runtime_state?: string };
type ComplianceResult = { request_id: string; policy: string; allowed: boolean; checks: Record<string, "passed" | "rejected" | "unavailable" | "disabled">; content_stored: false; anonymized_text?: string; replacements: number };
type TestOutcome = { policy: string; latencyMS: number; result?: ComplianceResult; error?: string };

function policyFields(anonymizerRules: string[]): Field[] { return [
  { key: "name", label: "Policy name", required: true, readOnlyOnEdit: true, placeholder: "production-strict" },
  { key: "description", label: "Description", type: "textarea", placeholder: "What this policy protects" },
  { key: "dlp", label: "Run DLP scanner", type: "boolean", defaultValue: true },
  { key: "output_dlp", label: "Scan provider output before delivery", type: "boolean" },
  { key: "av", label: "Run antivirus scanner", type: "boolean" },
  { key: "anonymization", label: "Anonymization profile", type: "select", options: ["disabled", "basic", "strict", "custom"], placeholder: "Use safe global default" },
  { key: "anonymization_rules", label: "Custom anonymization rules", type: "multi-select", options: anonymizerRules, visibleWhen: { fieldKey: "anonymization", equals: "custom" } },
  { key: "enabled", label: "Enabled", type: "boolean", defaultValue: true },
]; }

function records<T>(payload: unknown): T[] {
  if (Array.isArray(payload)) return payload as T[];
  const data = payload && typeof payload === "object" ? (payload as { data?: unknown }).data : undefined;
  return Array.isArray(data) ? data as T[] : [];
}

function checksLabel(policy: Policy) {
  return [policy.dlp && "Input DLP", policy.output_dlp && "Output DLP", policy.av && "Antivirus", policy.anonymization && `Anonymizer: ${policy.anonymization}`].filter(Boolean) as string[];
}

function GuardrailPolicyForm({ initial, rules, client, onClose, onSaved }: { initial?: Policy; rules: string[]; client: APIClient; onClose: () => void; onSaved: () => Promise<void> }) {
  async function save(value: Row) {
    const name = String(value.name || "").trim();
    const dlp = Boolean(value.dlp);
    const outputDLP = Boolean(value.output_dlp);
    const av = Boolean(value.av);
    const anonymization = String(value.anonymization || "");
    const anonymizationRules = Array.isArray(value.anonymization_rules) ? value.anonymization_rules.map(String) : [];
    if (outputDLP && !dlp) throw new Error("Output DLP requires the DLP scanner");
    if (!dlp && !av && !anonymization) throw new Error("Select at least one scanner or an anonymization profile");
    if (anonymization === "custom" && !anonymizationRules.length) throw new Error("Select at least one custom anonymization rule");
    await client.request(`/admin/v1/guardrail-policies/${encodeURIComponent(name)}`, { method: "PUT", body: { description: String(value.description || ""), dlp, output_dlp: outputDLP, av, anonymization: anonymization || undefined, anonymization_rules: anonymization === "custom" ? anonymizationRules : undefined, enabled: Boolean(value.enabled) } });
    onClose();
    await onSaved();
  }
  return <ResourceForm title={initial ? `Edit ${initial.name}` : "Create Guardrail Policy"} fields={policyFields(rules)} initial={initial as unknown as Row | undefined} onClose={onClose} onSubmit={save} />;
}

function PolicyDetails({ policy, attachments, deployments, onClose, onEdit, onTest, onMonitor, onAttachments }: { policy: Policy; attachments: PolicyAttachment[]; deployments: Deployment[]; onClose: () => void; onEdit: () => void; onTest: () => void; onMonitor: () => void; onAttachments: () => void }) {
  const linkedAttachments = attachments.filter((item) => item.policy_name === policy.name);
  const linkedDeployments = deployments.filter((item) => item.guardrail_policy === policy.name);
  return <ModalFrame label={`Guardrail policy ${policy.name}`} onClose={onClose}><section className="modal guardrail-details-modal">
    <div className="modal-heading"><div><span className="eyebrow">Guardrail policy</span><h2>{policy.name}</h2></div><button className="icon-button" aria-label="Close policy details" onClick={onClose}>×</button></div>
    <dl className="detail-grid"><div><dt>Status</dt><dd><span className={`status ${policy.enabled ? "enabled" : "disabled"}`}>{policy.enabled ? "Enabled" : "Disabled"}</span></dd></div><div><dt>Controls</dt><dd><div className="tag-list">{checksLabel(policy).map((item) => <span className="tag" key={item}>{item}</span>)}</div></dd></div><div><dt>Anonymization rules</dt><dd>{policy.anonymization === "custom" ? policy.anonymization_rules?.join(", ") || "None" : policy.anonymization || "Safe global default"}</dd></div><div><dt>Direct deployments</dt><dd>{linkedDeployments.length ? linkedDeployments.map((item) => item.id).join(", ") : "None"}</dd></div><div><dt>Scoped attachments</dt><dd>{linkedAttachments.length ? linkedAttachments.map((item) => item.id).join(", ") : "None"}</dd></div><div className="span-2"><dt>Description</dt><dd>{policy.description || "—"}</dd></div><div className="span-2"><dt>Runtime behavior</dt><dd>Matching profiles are combined. Strict anonymization wins; selected rule sets are merged. Disabled applies only when no matching profile requires anonymization.</dd></div></dl>
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
    <form onSubmit={run} className="guardrail-test-form"><ChipMultiSelect label="Policies" options={options} value={selected} onChange={updateSelection} /><small>Select up to 8 enabled policies. Each policy is evaluated independently so results can be compared.</small><label>Text projection<textarea aria-label="Text projection" required rows={7} value={text} onChange={(event) => { setText(event.target.value); setOutcomes(undefined); }} placeholder="Enter text to evaluate without calling a model…" /><small>{textBytes.toLocaleString()} / 65,536 UTF-8 bytes</small></label><div className="guardrail-test-privacy">The gateway sends only request ID and this text projection to configured DLP/AV services. Submitted text and raw scanner responses are not retained or returned.</div>{error && <p className="form-error" role="alert">{error}</p>}<div className="modal-actions"><button type="button" className="secondary" disabled={testing} onClick={onClose}>Close</button><button disabled={testing || !selected.length || !text.trim() || textBytes > 65_536}>{testing ? `Testing ${selected.length} policies…` : `Test ${selected.length || "selected"} policies`}</button></div></form>
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

  return <><PageHeader eyebrow="Compliance" title="Guardrails" description="Configure reusable scanner and anonymization profiles, inspect their coverage and preview transformations without calling a model." />
    {error && <ErrorState message={error} retry={() => void load()} />}
    {rulesWarning && <section className="notice-card" role="status"><p>{rulesWarning}</p></section>}
    <div className="usage-stats-grid"><StatCard label="Policies" value={policies.length.toLocaleString()} /><StatCard label="Enabled" value={enabledCount.toLocaleString()} /><StatCard label="Protected deployments" value={activeDeployments.toLocaleString()} detail="enabled deployments with a policy" /><StatCard label="Scoped attachments" value={scopedAttachments.toLocaleString()} detail="identity and model scopes" /></div>
    <section className="notice-card guardrail-policy-boundary"><h2>Effective protection</h2><p>The safe default anonymizes with every configured rule. Attach profiles to identities, models, providers or deployments to select rules or explicitly disable anonymization. Matching profiles are combined for each provider attempt.</p></section>
    <section className="section-block"><h2>Configured policies</h2><p>Use the action menu to inspect coverage, edit a policy or open a preselected compliance test.</p><ManagedDataTable rows={rows} columns={[{ key: "policy", label: "Policy" }, { key: "description", label: "Description" }, { key: "scanners", label: "Scanners" }, { key: "deployments", label: "Deployments" }, { key: "attachments", label: "Attachments" }, { key: "status", label: "Status" }]} primaryAction={<div className="page-actions"><button onClick={() => setEditing(null)}>Create Guardrail Policy</button><button className="secondary" disabled={!enabledCount} onClick={() => setTestNames([])}>Test Guardrails</button></div>} onRefresh={load} searchPlaceholder="Search guardrail policies" actions={(row) => { const policy = row._policy as Policy; return <ActionsMenu label={`Actions for guardrail policy ${policy.name}`} items={[{ label: "Inspect", onSelect: () => setInspecting(policy) }, { label: "Edit", onSelect: () => setEditing(policy) }, { label: "Test", disabled: !policy.enabled, onSelect: () => setTestNames([policy.name]) }, { label: "Open monitor", onSelect: () => navigate(`/guardrails-monitor?policy=${encodeURIComponent(policy.name)}`) }]} />; }} /></section>
    {editing !== undefined && <GuardrailPolicyForm initial={editing || undefined} rules={anonymizerRules} client={client} onClose={() => setEditing(undefined)} onSaved={() => load()} />}
    {inspecting && <PolicyDetails policy={inspecting} attachments={attachments} deployments={deployments} onClose={() => setInspecting(undefined)} onEdit={() => { setInspecting(undefined); setEditing(inspecting); }} onTest={() => { setInspecting(undefined); setTestNames([inspecting.name]); }} onMonitor={() => navigate(`/guardrails-monitor?policy=${encodeURIComponent(inspecting.name)}`)} onAttachments={() => navigate("/policies")} />}
    {testNames !== undefined && <GuardrailTestDialog policies={policies} initialNames={testNames} client={client} onClose={() => setTestNames(undefined)} />}
  </>;
}
