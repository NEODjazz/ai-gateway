import { useEffect, useRef, useState, type ReactNode } from "react";
import { Checkbox } from "@gravity-ui/uikit";
import { GravityThemeScope } from "../../components/GravityThemeScope";
import { GatewayButton } from "../../components/GatewayButton";
import { ModalFrame } from "../../components/ModalFrame";
import { PageTabs } from "../../components/PageTabs";
import { AreaControl, SelectControl, TextControl } from "./Controls";
import { complianceCSV, compliancePolicies, maximumPolicies, maximumCases, maximumImportBytes, parseComplianceCSV, runCompliance, starterCases, type ComplianceCase, type ComplianceResult } from "./compliance";
import { parseResourceCatalog, type ResourceCatalog } from "./resources";
import type { PlaygroundConnection } from "./requests";

function download(name: string, value: string) {
  const url = URL.createObjectURL(new Blob([value], { type: "text/csv;charset=utf-8" }));
  const link = document.createElement("a"); link.href = url; link.download = name; link.click(); URL.revokeObjectURL(url);
}
function Check({ label, checked, disabled, onUpdate }: { label: string; checked: boolean; disabled?: boolean; onUpdate: (value: boolean) => void }) {
  return <GravityThemeScope><Checkbox controlProps={{ "aria-label": label }} checked={checked} disabled={disabled} onUpdate={onUpdate}>{label}</Checkbox></GravityThemeScope>;
}

export function CompliancePlayground({ connection, connectionChanged, connectionControls, active = true }: {
  connection: PlaygroundConnection; connectionChanged: boolean; connectionControls: ReactNode; active?: boolean;
}) {
  const [cases, setCases] = useState<ComplianceCase[]>(starterCases);
  const [selected, setSelected] = useState(() => new Set(starterCases.map((item) => item.id)));
  const [policy, setPolicy] = useState("");
  const [model, setModel] = useState("");
  const [query, setQuery] = useState("");
  const [category, setCategory] = useState("all");
  const [framework, setFramework] = useState("all");
  const [tab, setTab] = useState<"quick" | "batch">("quick");
  const [quickText, setQuickText] = useState("");
  const [quickResults, setQuickResults] = useState<ComplianceResult[]>([]);
  const [runPolicies, setRunPolicies] = useState<string[]>([]);
  const [catalog, setCatalog] = useState<ResourceCatalog>();
  const [catalogBusy, setCatalogBusy] = useState(false), [catalogError, setCatalogError] = useState("");
  const catalogGeneration = useRef(0), catalogAbort = useRef<AbortController | undefined>(undefined);
  const [runCases, setRunCases] = useState<ComplianceCase[]>([]);
  const [results, setResults] = useState<ComplianceResult[]>([]);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const [newPrompt, setNewPrompt] = useState("");
  const [newCategory, setNewCategory] = useState("Custom");
  const [newFramework, setNewFramework] = useState("Custom");
  const [expected, setExpected] = useState<"allow" | "block">("allow");
  const generation = useRef(0), abort = useRef<AbortController | undefined>(undefined);
  const file = useRef<HTMLInputElement>(null);
  const importing = useRef(false);
  const [importBusy, setImportBusy] = useState(false);
  const previousConnection = useRef(connection);
  const visible = cases.filter((item) => (category === "all" || item.category === category) && (framework === "all" || item.framework === framework) && `${item.prompt} ${item.category} ${item.framework}`.toLowerCase().includes(query.toLowerCase()));
  const selectedCases = cases.filter((item) => selected.has(item.id));
  useEffect(() => () => { generation.current++; abort.current?.abort(); }, []);
  useEffect(() => { if (!active) { abort.current?.abort(); catalogGeneration.current++; catalogAbort.current?.abort(); setCatalogBusy(false); } }, [active]);
  useEffect(() => {
    if (previousConnection.current !== connection) { previousConnection.current = connection; reset(); setPolicy(""); setModel(""); }
  }, [connection]);

  useEffect(() => {
    catalogGeneration.current++; catalogAbort.current?.abort(); setCatalogBusy(false); setCatalog(undefined); setCatalogError("");
    return () => { catalogGeneration.current++; catalogAbort.current?.abort(); };
  }, [connection, model]);
  async function loadPolicies() {
    if (running || catalogBusy || connectionChanged) return;
    const controller = new AbortController(), current = ++catalogGeneration.current;
    catalogAbort.current = controller; setCatalogBusy(true); setCatalogError("");
    try {
      const result = parseResourceCatalog(await connection.client.request<unknown>(connection.path(`/v1/playground/catalog${model.trim() ? `?model=${encodeURIComponent(model.trim())}` : ""}`), { signal: controller.signal, maximumResponseBytes: 1024 * 1024 }));
      if (current === catalogGeneration.current && !controller.signal.aborted) setCatalog(result);
    } catch (cause) { if (current === catalogGeneration.current) setCatalogError(cause instanceof Error ? cause.message : "Could not load policies."); }
    finally { if (current === catalogGeneration.current) { setCatalogBusy(false); catalogAbort.current = undefined; } }
  }
  function togglePolicy(name: string, checked: boolean) {
    const names = policy.trim() ? policy.split(",").map((item) => item.trim()) : [];
    const next = checked ? [...names, name] : names.filter((item) => item !== name);
    try { if (next.length) compliancePolicies(next); setPolicy(next.join(", ")); setError(""); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Invalid policies."); }
  }

  function reset() { generation.current++; abort.current?.abort(); abort.current = undefined; setRunning(false); setError(""); setResults([]); setRunCases([]); setQuickResults([]); setRunPolicies([]); }
  async function execute(items: ComplianceCase[], quick = false) {
    if (running) return;
    if (connectionChanged) { setError("Apply connection changes before testing."); return; }
    let policies: string[];
    try { policies = compliancePolicies(policy); } catch (cause) { setError(cause instanceof Error ? cause.message : "Invalid policies."); return; }
    if (!items.length) { setError("Select at least one test case."); return; }
    if (items.some((item) => !item.prompt.trim() || new TextEncoder().encode(item.prompt).length > 64 * 1024)) { setError("Each prompt must contain 1–65536 bytes."); return; }
    const controller = new AbortController(); abort.current = controller;
    const current = ++generation.current; setRunning(true); setError("");
    if (quick) setQuickResults([]); else { setRunPolicies(policies); setResults([]); setRunCases(items); setTab("batch"); }
    try { await runCompliance(connection, items, policies, model.trim(), controller.signal, (result) => {
      if (generation.current !== current) return;
      if (quick) setQuickResults((existing) => [...existing, { ...result, matched: undefined }]); else setResults((existing) => [...existing, result]);
    }); } catch (cause) { if (generation.current === current) setError(cause instanceof Error ? cause.message : "Tests failed"); }
    finally { if (generation.current === current) { setRunning(false); abort.current = undefined; } }
  }
  async function importFile(input: File | undefined) {
    if (!input || running || importing.current) return;
    importing.current = true; setImportBusy(true);
    const current = generation.current;
    try {
      if (input.size > maximumImportBytes) throw new Error("CSV exceeds 1 MiB.");
      const value = await new Promise<string>((resolve, reject) => { const reader = new FileReader(); reader.onload = () => resolve(String(reader.result)); reader.onerror = () => reject(new Error("Could not read CSV.")); reader.readAsText(input); });
      const imported = parseComplianceCSV(value);
      if (current !== generation.current) return;
      if (cases.length + imported.length > maximumCases) throw new Error("A suite may contain at most 500 cases.");
      setCases((existing) => [...existing, ...imported]); setSelected((existing) => new Set([...existing, ...imported.map((item) => item.id)])); setError("");
    } catch (cause) { if (generation.current === current) setError(cause instanceof Error ? cause.message : "Import failed"); }
    finally { importing.current = false; setImportBusy(false); if (file.current) file.current.value = ""; }
  }
  function addCase() {
    if (importing.current) return;
    if (!newPrompt.trim() || new TextEncoder().encode(newPrompt).length > 64 * 1024 || newCategory.length > 128 || newFramework.length > 128) { setError("Enter a prompt up to 64 KiB and category/framework up to 128 characters."); return; }
    if (cases.length >= maximumCases) { setError("A suite may contain at most 500 cases."); return; }
    const item: ComplianceCase = { id: `custom-${crypto.randomUUID()}`, prompt: newPrompt, category: newCategory.trim() || "Custom", framework: newFramework.trim() || "Custom", expected };
    setCases((existing) => [...existing, item]); setSelected((existing) => new Set([...existing, item.id])); setAdding(false); setNewPrompt(""); setError("");
  }
  const counts = { allowed: 0, blocked: 0, failed: 0, cancelled: 0, matched: 0 };
  results.forEach((result) => { counts[result.status]++; if (result.matched) counts.matched++; });
  function resultDetails(result: ComplianceResult) {
    return <><strong>{result.policy}: {result.status}</strong>{result.error && <span role="alert"> {result.error}</span>}{result.matched !== undefined && <span> · {result.matched ? "Expected outcome" : "Unexpected outcome"}</span>}
      <span> · {result.latencyMS === undefined ? "Latency unavailable" : `${Math.round(result.latencyMS)} ms`}</span>{result.executionID && <code> · {result.executionID}</code>}
      {result.checks && <details><summary>Checks</summary><pre>{JSON.stringify(result.checks, null, 2)}</pre></details>}
      {result.anonymizedText && <details><summary>Anonymized text ({result.replacements ?? "—"} replacements)</summary><pre>{result.anonymizedText}</pre></details>}</>;
  }
  return <div className="playground-compliance">
    <section className="playground-parameters-card playground-compliance-connection">{connectionControls}
      <TextControl label="Guardrail policy" value={policy} disabled={running} onUpdate={setPolicy} placeholder="strict, privacy (up to four distinct policies)" />
      <TextControl label="Policy test model" value={model} disabled={running || catalogBusy} onUpdate={(value) => { setModel(value); setPolicy(""); reset(); }} placeholder="Optional model ID for policy authorization" />
      <GatewayButton view="outlined" disabled={running || catalogBusy || connectionChanged} onClick={() => void loadPolicies()}>Load policies</GatewayButton>
      <p className="muted">Select up to {maximumPolicies} policies from the authorized catalog or enter comma-separated names. Each prompt is tested independently against each policy; failed checks never count as allowed.</p>
      <div className="playground-resource-list">{catalog?.policies.map((name) => <Check key={name} label={`Test policy ${name}`} checked={policy.split(",").some((item) => item.trim() === name)} disabled={running || connectionChanged} onUpdate={(checked) => togglePolicy(name, checked)} />)}</div>
      {catalog?.truncated && <p role="status">Catalog limited to 256 entries. Other authorized policies can be entered by name.</p>}
      {catalog && !catalog.policies.length && !catalog.policy_error && <p className="muted">No policies available in this authorization scope.</p>}
      {(catalogError || catalog?.policy_error) && <p role="alert">{catalogError || catalog?.policy_error}</p>}
      <p className="muted">Tests call the selected policies without generating model output. Mandatory policies and credential permissions remain enforced. Results are policy checks, not regulatory certification. Baseline expectations are examples; adapt them to your policy.</p>
    </section>
    <PageTabs label="Compliance results" value={tab} items={[{ value: "quick", label: "Quick test" }, { value: "batch", label: "Batch results" }]} onUpdate={setTab} />
    {tab === "quick" && <section className="playground-parameters-card"><AreaControl label="Quick test prompt" value={quickText} disabled={running} rows={4} onUpdate={setQuickText} /><GatewayButton disabled={running || connectionChanged || !quickText.trim()} onClick={() => void execute([{ id: "quick", category: "Custom", framework: "Custom", prompt: quickText, expected: "allow" }], true)}>Test policy</GatewayButton>
      <div aria-live="polite">{quickResults.map((result) => <div key={result.policy}>{resultDetails(result)}</div>)}</div></section>}
    <section className="playground-parameters-card">
      <h2>Test suite</h2><div className="playground-compliance-filters"><TextControl label="Search test cases" value={query} onUpdate={setQuery} />
        <SelectControl label="Category" value={category} options={[{ value: "all", content: "All categories" }, ...[...new Set(cases.map((item) => item.category))].sort().map((value) => ({ value, content: value }))]} onUpdate={setCategory} />
        <SelectControl label="Framework" value={framework} options={[{ value: "all", content: "All frameworks" }, ...[...new Set(cases.map((item) => item.framework))].sort().map((value) => ({ value, content: value }))]} onUpdate={setFramework} />
      </div><div className="playground-actions"><Check label="Select visible cases" checked={visible.length > 0 && visible.every((item) => selected.has(item.id))} disabled={running} onUpdate={(checked) => setSelected((existing) => { const next = new Set(existing); visible.forEach((item) => checked ? next.add(item.id) : next.delete(item.id)); return next; })} />
        <GatewayButton view="outlined" disabled={running || importBusy} onClick={() => setAdding(true)}>Add test case</GatewayButton><GatewayButton view="outlined" disabled={running || importBusy} onClick={() => file.current?.click()}>Import CSV</GatewayButton>
        <GatewayButton view="outlined" onClick={() => download("ai-gateway-policy-template.csv", complianceCSV(starterCases))}>CSV template</GatewayButton>
        <input ref={file} type="file" accept=".csv,text/csv" aria-label="Import policy test CSV" hidden onChange={(event) => void importFile(event.currentTarget.files?.[0])} />
      </div><p className="muted">{selectedCases.length} selected · {visible.length} shown · {cases.length} total</p>
      <div className="playground-case-list">{visible.map((item) => <article key={item.id} className="playground-policy-case"><Check label={`Select ${item.category}: ${item.prompt.slice(0, 60)}`} checked={selected.has(item.id)} disabled={running} onUpdate={(checked) => setSelected((existing) => { const next = new Set(existing); checked ? next.add(item.id) : next.delete(item.id); return next; })} />
        <span className="muted">{item.framework} · expected {item.expected}</span><pre>{item.prompt}</pre><GatewayButton view="flat" disabled={running || importBusy} aria-label={`Remove case ${item.id}`} onClick={() => { setCases((existing) => existing.filter((candidate) => candidate.id !== item.id)); setSelected((existing) => { const next = new Set(existing); next.delete(item.id); return next; }); }}>Remove</GatewayButton>
      </article>)}</div><div className="playground-actions"><GatewayButton disabled={running || connectionChanged || !selectedCases.length} onClick={() => void execute(selectedCases)}>Run selected tests</GatewayButton>
        {running && <GatewayButton view="outlined" onClick={() => abort.current?.abort()}>Stop tests</GatewayButton>}<GatewayButton view="outlined" onClick={reset}>Reset results</GatewayButton></div>
    </section>
    {tab === "batch" && <section className="playground-parameters-card"><h2>Batch results</h2><p aria-live="polite">{results.length}/{runCases.length * runPolicies.length} completed · {counts.allowed} allowed · {counts.blocked} blocked · {counts.failed} failed · {counts.cancelled} cancelled · {counts.matched} expected outcomes</p>
      <GatewayButton view="outlined" disabled={running || !results.length} onClick={() => download("ai-gateway-policy-results.csv", complianceCSV(runCases, results))}>Export policy results</GatewayButton>
      <p className="muted">{runCases.length} prompts × {runPolicies.length} policies · totals count individual policy checks.</p>
      {runCases.map((item) => <article className="playground-policy-case" key={item.id}><pre>{item.prompt}</pre>{runPolicies.map((name) => { const result = results.find((candidate) => candidate.id === item.id && candidate.policy === name); return <div key={name}>{result ? resultDetails(result) : <span>{name}: {running ? "Pending" : "Not run"}</span>}</div>; })}</article>)}</section>}
    {error && <p role="alert" className="form-error">{error}</p>}
    {adding && <ModalFrame label="Add policy test case" onClose={() => setAdding(false)}><div className="modal"><h2>Add test case</h2><AreaControl label="Test prompt" rows={4} value={newPrompt} onUpdate={setNewPrompt} /><TextControl label="Test category" value={newCategory} onUpdate={setNewCategory} /><TextControl label="Test framework" value={newFramework} onUpdate={setNewFramework} /><SelectControl label="Expected policy outcome" value={expected} options={[{ value: "allow", content: "Allow" }, { value: "block", content: "Block" }]} onUpdate={setExpected} />{error && <p role="alert" className="form-error">{error}</p>}<div className="playground-actions"><GatewayButton disabled={importBusy} onClick={addCase}>Add case</GatewayButton><GatewayButton view="outlined" onClick={() => setAdding(false)}>Cancel</GatewayButton></div></div></ModalFrame>}
  </div>;
}
