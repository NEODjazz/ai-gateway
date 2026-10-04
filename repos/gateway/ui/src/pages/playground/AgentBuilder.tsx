import { useEffect, useRef, useState, type ReactNode } from "react";
import { Checkbox } from "@gravity-ui/uikit";
import { GatewayButton } from "../../components/GatewayButton";
import { GravityThemeScope } from "../../components/GravityThemeScope";
import { ModalFrame } from "../../components/ModalFrame";
import { PageTabs } from "../../components/PageTabs";
import { ModelControl } from "./ModelControl";
import { AreaControl, SelectControl, TextControl } from "./Controls";
import { AgentMCPControls } from "./AgentMCPControls";
import { AgentExecution } from "./AgentExecution";
import { agentBody, agentDraft, emptyAgentDraft, type AgentDraft, type AgentPolicy, type AgentProfile } from "./agents";
import type { PlaygroundConnection } from "./requests";

export function AgentBuilder({ connection, models, connectionControls, connectionChanged, active = true }: { connection: PlaygroundConnection; models: string[]; connectionControls: ReactNode; connectionChanged: boolean; active?: boolean }) {
  const [profiles, setProfiles] = useState<AgentProfile[]>([]), [policies, setPolicies] = useState<AgentPolicy[]>([]), [selected, setSelected] = useState("");
  const [draft, setDraft] = useState<AgentDraft>(emptyAgentDraft), [baseline, setBaseline] = useState(""), [tab, setTab] = useState<"configure" | "chat" | "batch" | "connect">("configure");
  const [loading, setLoading] = useState(false), [busy, setBusy] = useState(false), [error, setError] = useState(""), [status, setStatus] = useState(""), [remove, setRemove] = useState(false);
  const loadAbort = useRef<AbortController | undefined>(undefined), detailAbort = useRef<AbortController | undefined>(undefined), mutationAbort = useRef<AbortController | undefined>(undefined);
  const loadEpoch = useRef(0), detailEpoch = useRef(0), mutationEpoch = useRef(0);
  const saved = profiles.find((item) => item.id === selected), dirty = JSON.stringify(draft) !== baseline;
  function invalidate() { loadEpoch.current++; detailEpoch.current++; mutationEpoch.current++; loadAbort.current?.abort(); detailAbort.current?.abort(); mutationAbort.current?.abort(); setLoading(false); setBusy(false); }
  useEffect(() => () => { loadEpoch.current++; detailEpoch.current++; mutationEpoch.current++; loadAbort.current?.abort(); detailAbort.current?.abort(); mutationAbort.current?.abort(); }, []);
  useEffect(() => {
    invalidate(); setProfiles([]); setPolicies([]); setSelected(""); setDraft({ ...emptyAgentDraft }); setBaseline(""); setStatus(""); setError(""); setRemove(false); setTab("configure"); void refresh("", true);
  }, [connection]);
  useEffect(() => { if (!active || connectionChanged) { mutationAbort.current?.abort(); setRemove(false); } }, [active, connectionChanged]);
  async function refresh(id = selected, scopeChanged = false) {
    if (!scopeChanged && (busy || connectionChanged)) return;
    const epoch = ++loadEpoch.current; loadAbort.current?.abort(); const controller = new AbortController(); loadAbort.current = controller; setLoading(true); setError("");
    try {
      const [agents, tools] = await Promise.all([
        connection.client.request<{ data: AgentProfile[] }>(connection.path("/admin/v1/agent-profiles"), { signal: controller.signal, maximumResponseBytes: 4 * 1024 * 1024 }),
        connection.client.request<{ data: AgentPolicy[] }>(connection.path("/admin/v1/tool-policies"), { signal: controller.signal, maximumResponseBytes: 4 * 1024 * 1024 })
      ]);
      if (epoch !== loadEpoch.current || controller.signal.aborted) return;
      if (!Array.isArray(agents.data) || !Array.isArray(tools.data)) throw new Error("Gateway returned an invalid agent catalog.");
      if (agents.data.length > 500 || tools.data.length > 500) throw new Error("Agent Builder supports catalogs up to 500 profiles and 500 policies. Use the management pages for larger catalogs.");
      setProfiles(agents.data); setPolicies(tools.data);
      if (id && !agents.data.some((item) => item.id === id)) { setSelected(""); setDraft({ ...emptyAgentDraft }); setBaseline(""); setStatus("The selected agent is no longer available."); }
      else if (id) await select(id);
    } catch (cause) { if (epoch === loadEpoch.current && !controller.signal.aborted) { setProfiles([]); setPolicies([]); setSelected(""); setDraft({ ...emptyAgentDraft }); setBaseline(""); setError(cause instanceof Error ? cause.message : "Could not load agents."); } }
    finally { if (epoch === loadEpoch.current) { setLoading(false); loadAbort.current = undefined; } }
  }
  async function select(id: string) {
    if (busy || connectionChanged) return;
    const epoch = ++detailEpoch.current; detailAbort.current?.abort(); const controller = new AbortController(); detailAbort.current = controller; setSelected(""); setDraft({ ...emptyAgentDraft }); setBaseline(""); setTab("configure"); setStatus(""); setError("");
    if (!id) return;
    setBusy(true);
    try {
      const detail = await connection.client.request<{ profile: AgentProfile; instructions: string }>(connection.path(`/admin/v1/agent-profiles/${encodeURIComponent(id)}`), { signal: controller.signal, maximumResponseBytes: 256 * 1024 });
      if (epoch !== detailEpoch.current || controller.signal.aborted) return;
      if (detail.profile?.id !== id || typeof detail.instructions !== "string") throw new Error("Gateway returned an invalid agent configuration.");
      const value = agentDraft(detail.profile, detail.instructions); setProfiles((items) => items.map((item) => item.id === id ? detail.profile : item)); setDraft(value); setBaseline(JSON.stringify(value)); setSelected(id);
    } catch (cause) { if (epoch === detailEpoch.current && !controller.signal.aborted) setError(cause instanceof Error ? cause.message : "Could not read agent."); }
    finally { if (epoch === detailEpoch.current) { setBusy(false); detailAbort.current = undefined; } }
  }
  function create() {
    if (busy || connectionChanged) return;
    detailEpoch.current++; detailAbort.current?.abort(); setSelected(""); setDraft({ ...emptyAgentDraft, model: models[0] || "", policy: policies.find((item) => item.enabled)?.id || "" }); setBaseline(""); setTab("configure"); setError(""); setStatus("");
  }
  function patch(value: Partial<AgentDraft>) { setDraft((previous) => ({ ...previous, ...value })); setStatus(""); }
  async function save() {
    if (busy || loading || connectionChanged) return;
    let body: ReturnType<typeof agentBody>;
    try {
      body = agentBody(draft);
      if (!selected && profiles.some((item) => item.id === draft.id)) throw new Error("Agent ID already exists. Select it to edit its saved configuration.");
      if (!policies.some((item) => item.id === draft.policy && item.enabled)) throw new Error("Choose an enabled tool policy.");
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Invalid agent configuration."); return; }
    const epoch = ++mutationEpoch.current, controller = new AbortController(); mutationAbort.current = controller; setBusy(true); setError(""); setStatus("");
    try {
      const profile = await connection.client.request<AgentProfile>(connection.path(`/admin/v1/agent-profiles/${encodeURIComponent(draft.id)}`), { method: "PUT", body, signal: controller.signal, maximumResponseBytes: 256 * 1024 });
      if (epoch !== mutationEpoch.current || controller.signal.aborted) return;
      if (profile.id !== draft.id) throw new Error("Gateway returned an invalid saved profile. Refresh agents to check the saved state.");
      setProfiles((items) => [...items.filter((item) => item.id !== profile.id), profile].sort((a, b) => a.id.localeCompare(b.id)));
      const value = agentDraft(profile, draft.instructions); setSelected(profile.id); setDraft(value); setBaseline(JSON.stringify(value)); setStatus("Agent saved. Chat and tests use this saved configuration.");
    } catch (cause) { if (epoch === mutationEpoch.current) setError(controller.signal.aborted ? "Save cancelled. It may have completed on the server; refresh before repeating it." : cause instanceof Error ? cause.message : "Could not save agent."); }
    finally { if (epoch === mutationEpoch.current) { mutationAbort.current = undefined; setBusy(false); } }
  }
  async function deleteAgent() {
    if (!saved || busy || connectionChanged) return;
    const id = saved.id, epoch = ++mutationEpoch.current, controller = new AbortController(); mutationAbort.current = controller; setBusy(true); setError("");
    try {
      await connection.client.request(connection.path(`/admin/v1/agent-profiles/${encodeURIComponent(id)}`), { method: "DELETE", signal: controller.signal, maximumResponseBytes: 256 * 1024 });
      if (epoch !== mutationEpoch.current || controller.signal.aborted) return;
      setProfiles((items) => items.filter((item) => item.id !== id)); setSelected(""); setDraft({ ...emptyAgentDraft }); setBaseline(""); setTab("configure"); setRemove(false); setStatus("Agent removed.");
    } catch (cause) { if (epoch === mutationEpoch.current) setError(controller.signal.aborted ? "Delete cancelled. Refresh to check whether it completed." : cause instanceof Error ? cause.message : "Could not delete agent."); }
    finally { if (epoch === mutationEpoch.current) { mutationAbort.current = undefined; setBusy(false); } }
  }
  const disabled = busy || loading || connectionChanged, policy = policies.find((item) => item.id === draft.policy);
  return <div className="playground-workspace playground-config-layout playground-agent-builder">
    <aside className="playground-side-panel"><section className="playground-parameters-card"><h2>Agent Builder</h2>{connectionControls}<SelectControl label="Saved agent" value={selected} disabled={disabled} options={[{ value: "", content: "Choose a saved agent" }, ...profiles.map((item) => ({ value: item.id, content: `${item.name} · ${item.model}${item.enabled ? "" : " · disabled"}` }))]} onUpdate={(id) => void select(id)} /><div className="playground-actions"><GatewayButton view="outlined" disabled={disabled} onClick={create}>New agent</GatewayButton><GatewayButton view="outlined" disabled={disabled} onClick={() => void refresh()}>Refresh agents</GatewayButton></div><p className="muted">Agent configuration requires a gateway administrator. Your active credential controls both management and execution.</p>{saved && <dl className="playground-metadata"><div><dt>Model</dt><dd>{saved.model}</dd></div><div><dt>A2A execution</dt><dd>{saved.execution_supported ? "Available" : "Unavailable"}</dd></div><div><dt>Instructions</dt><dd>{saved.instructions_configured ? "Configured" : "None"}</dd></div></dl>}</section></aside>
    <div className="playground-main-panel"><PageTabs label="Agent Builder view" value={tab} items={[{ value: "configure", label: "Configure" }, { value: "chat", label: "Chat" }, { value: "batch", label: "Batch Test" }, { value: "connect", label: "Connect" }]} onUpdate={setTab} />
      {tab === "configure" ? <section className="playground-parameters-card" aria-label="Agent configuration"><h2>{saved ? `Configure ${saved.name}` : "New agent configuration"}</h2><TextControl label="Agent ID" value={draft.id} disabled={disabled || !!saved} onUpdate={(id) => patch({ id })} /><TextControl label="Agent name" value={draft.name} disabled={disabled} onUpdate={(name) => patch({ name })} /><TextControl label="Agent description" value={draft.description} disabled={disabled} onUpdate={(description) => patch({ description })} /><ModelControl label="Agent model" value={draft.model} models={models} scope={connection} disabled={disabled} onUpdate={(model) => patch({ model })} /><AreaControl label="Agent instructions" rows={6} value={draft.instructions} disabled={disabled} onUpdate={(instructions) => patch({ instructions })} /><p className="muted">Instructions are encrypted by the gateway when saved and are readable only by administrators. Saving fails if configuration encryption is unavailable.</p><TextControl label="Agent temperature" type="number" value={draft.temperature} disabled={disabled} onUpdate={(temperature) => patch({ temperature })} placeholder="Model default" /><TextControl label="Agent maximum output tokens" type="number" value={draft.maxTokens} disabled={disabled} onUpdate={(maxTokens) => patch({ maxTokens })} placeholder="Model default" /><SelectControl label="Agent tool policy" value={draft.policy} disabled={disabled} options={[{ value: "", content: "Choose an enabled policy" }, ...policies.filter((item) => item.enabled || item.id === draft.policy).map((item) => ({ value: item.id, content: item.name + (item.enabled ? "" : " · disabled") }))]} onUpdate={(policy) => patch({ policy })} />{policy && <details><summary>Tool policy rules</summary><p>Allowed: {policy.allowed_tools.join(", ")} · Denied: {policy.denied_tools?.join(", ") || "None"} · Approval: {policy.approval_required?.join(", ") || "None"} · Maximum calls: {policy.max_tool_calls}</p></details>}<AgentMCPControls connection={connection} model={draft.model} tools={draft.tools} disabled={disabled} onUpdate={(tools) => patch({ tools })} /><TextControl label="Agent maximum iterations" type="number" value={draft.iterations} disabled={disabled} onUpdate={(iterations) => patch({ iterations })} /><TextControl label="Agent tags" value={draft.tags} disabled={disabled} onUpdate={(tags) => patch({ tags })} placeholder="Comma-separated tags" /><GravityThemeScope><Checkbox controlProps={{ "aria-label": "Agent enabled" }} checked={draft.enabled} disabled={disabled} onUpdate={(enabled) => patch({ enabled })}>Enabled</Checkbox></GravityThemeScope>{draft.template && <><p role="alert">This profile uses a metadata-only instruction template. Clear it to configure executable inline instructions.</p><TextControl label="Agent instruction template" value={draft.template} disabled={disabled} onUpdate={(template) => patch({ template })} /></>}<p className="muted">Saved MCP bindings execute through durable A2A tasks with tool-call and model-iteration limits. Maximum iterations counts model generations, including the final reply; a tool call needs at least two iterations. Required approvals pause the task for review. Current policies can revoke saved permissions. Push and background execution are not yet supported for these agents.</p>{dirty && <p className="muted">Unsaved changes. Choosing another agent discards this draft.</p>}<div className="playground-actions">{saved && <GatewayButton view="outlined" disabled={disabled} onClick={() => setRemove(true)}>Delete agent</GatewayButton>}<GatewayButton disabled={disabled || !dirty} onClick={() => void save()}>Save agent</GatewayButton></div></section> : saved ? <AgentExecution connection={connection} profile={saved} disabled={disabled || dirty} active={active} tab={tab} /> : <section className="playground-parameters-card"><p>Select or save an agent before using {tab === "chat" ? "Chat" : tab === "batch" ? "Batch Test" : "Connect"}.</p></section>}
      {loading && <p role="status">Loading agent configurations…</p>}{busy && <p role="status">Updating agent configuration…</p>}{status && <p role="status">{status}</p>}{error && <p role="alert" className="form-error">{error}</p>}
    </div>
    {remove && saved && <ModalFrame label="Delete agent" onClose={() => { if (!busy) setRemove(false); }}><section className="modal"><h2>Delete {saved.name}?</h2><p>This removes the saved profile {saved.id}. Existing conversations are not deleted.</p><div className="modal-actions"><GatewayButton view="outlined" disabled={busy} onClick={() => setRemove(false)}>Keep agent</GatewayButton><GatewayButton disabled={busy || connectionChanged} onClick={() => void deleteAgent()}>Confirm delete agent</GatewayButton></div></section></ModalFrame>}
  </div>;
}
