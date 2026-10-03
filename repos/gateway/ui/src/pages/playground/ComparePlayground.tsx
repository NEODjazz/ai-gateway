import { useEffect, useRef, useState, type CSSProperties, type FormEvent, type ReactNode } from "react";
import { Checkbox } from "@gravity-ui/uikit";
import { GatewayButton } from "../../components/GatewayButton";
import { GravityThemeScope } from "../../components/GravityThemeScope";
import { AreaControl, SelectControl, TextControl } from "./Controls";
import { buildTextRequest, defaultGenerationSettings, type GenerationSettings, type Message, type PlaygroundConnection } from "./requests";
import { runText, type TextRun } from "./runText";
import { PricingControls, defaultPricing, estimateCost, type PricingInputs } from "./PriceEstimate";
import { retainConversation } from "./attachments";
import { csvCell } from "../../csv";

type Panel = {
  id: number; model: string; settings: GenerationSettings; instructions: string; sessionID: string;
  historyDropped: number; pricing: PricingInputs; history: Message[]; pending: string; lastPrompt?: string; result?: TextRun; error: string; status: "idle" | "running" | "complete" | "failed" | "cancelled";
};

function newPanel(id: number, model: string): Panel {
  return { id, model, settings: { ...defaultGenerationSettings }, instructions: "", sessionID: `playground-compare-${crypto.randomUUID()}`,
    historyDropped: 0, pricing: { ...defaultPricing }, history: [], pending: "", error: "", status: "idle" };
}

export function ComparePlayground({ connection, models, connectionControls, connectionChanged, active = true }: {
  connection: PlaygroundConnection; models: string[]; connectionControls: ReactNode; connectionChanged: boolean; active?: boolean;
}) {
  const [panels, setPanels] = useState(() => [newPanel(1, models[0] || ""), newPanel(2, models[1] || models[0] || "")]);
  const [prompt, setPrompt] = useState("");
  const [sync, setSync] = useState(true);
  const [stream, setStream] = useState(true);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState("");
  const nextID = useRef(3);
  const generation = useRef(0);
  const controllers = useRef(new Map<number, AbortController>());
  const previousConnection = useRef(connection);
  useEffect(() => { if (!active) cancel(); }, [active]);
  useEffect(() => () => { generation.current++; for (const controller of controllers.current.values()) controller.abort(); controllers.current.clear(); }, []);
  useEffect(() => {
    if (previousConnection.current === connection) return;
    previousConnection.current = connection; generation.current++;
    for (const controller of controllers.current.values()) controller.abort(); controllers.current.clear();
    setRunning(false); setError("");
    setPanels((current) => current.map((panel) => ({ ...newPanel(panel.id, ""), settings: panel.settings, instructions: panel.instructions })));
  }, [connection]);
  useEffect(() => {
    if (!models.length) return;
    setPanels((current) => current.map((panel, index) => panel.model && models.includes(panel.model) ? panel : { ...panel, model: models[index] || models[0], history: [], result: undefined, pending: "", status: "idle" }));
  }, [models]);

  function settings(id: number, patch: Partial<GenerationSettings>) {
    setPanels((current) => current.map((panel) => sync || panel.id === id ? { ...panel, settings: { ...panel.settings, ...patch } } : panel));
  }
  function updateInstructions(id: number, value: string) {
    setPanels((current) => current.map((panel) => sync || panel.id === id ? { ...panel, instructions: value } : panel));
  }
  function cancel() {
    generation.current++;
    for (const controller of controllers.current.values()) controller.abort(); controllers.current.clear();
    setPanels((current) => current.map((panel) => panel.status === "running" ? { ...panel, status: "cancelled", error: "Request cancelled" } : panel));
    setRunning(false);
  }
  async function submit(event: FormEvent) {
    event.preventDefault();
    const input = prompt.trim();
    if (running || !input || panels.some((panel) => !panel.model)) return;
    if (connectionChanged) { setError("Apply connection changes before comparing."); return; }
    let requests: { panel: Panel; body: Record<string, unknown> }[];
    try { requests = panels.map((panel) => ({ panel, body: buildTextRequest({ endpoint: "chat", model: panel.model, input, instructions: panel.instructions, history: panel.history, streaming: stream, settings: panel.settings }) })); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Invalid comparison settings"); return; }
    const activeGeneration = ++generation.current;
    setError(""); setRunning(true);
    setPanels((current) => current.map((panel) => ({ ...panel, status: "running", error: "", pending: "", result: undefined, lastPrompt: input })));
    await Promise.all(requests.map(async ({ panel, body }) => {
      const controller = new AbortController(); controllers.current.set(panel.id, controller);
      try {
        const result = await runText(connection, "chat", body, { signal: controller.signal, sessionID: panel.sessionID,
          onText: (text) => { if (generation.current === activeGeneration) setPanels((current) => current.map((item) => item.id === panel.id ? { ...item, pending: text } : item)); } });
        if (generation.current !== activeGeneration) return;
        const choice = (result.response.choices as { message?: Message }[] | undefined)?.[0]?.message;
        const assistant: Message = { ...choice, role: "assistant", content: choice?.content ?? result.text };
        const retained = retainConversation([...panel.history, { role: "user", content: input }, assistant]);
        setPanels((current) => current.map((item) => item.id === panel.id ? { ...item, historyDropped: item.historyDropped + retained.dropped, result, pending: "", status: "complete", history: retained.turns } : item));
      } catch (cause) {
        if (generation.current === activeGeneration) setPanels((current) => current.map((item) => item.id === panel.id ? { ...item, status: controller.signal.aborted ? "cancelled" : "failed", error: cause instanceof Error ? cause.message : "Request failed" } : item));
      } finally { if (controllers.current.get(panel.id) === controller) controllers.current.delete(panel.id); }
    }));
    if (generation.current === activeGeneration) { setRunning(false); setPrompt(""); }
  }

  function exportResults() {
    const rows = [["model", "status", "prompt", "response", "error", "input_tokens", "output_tokens", "latency_ms", "first_token_ms", "estimated_token_cost"]];
    const csv = rows[0].map(csvCell).join(",") + "\r\n" + panels.map((panel) => [panel.model, panel.status, panel.lastPrompt, panel.result?.text || panel.pending, panel.error,
      panel.result?.usage?.prompt_tokens ?? panel.result?.usage?.input_tokens, panel.result?.usage?.completion_tokens ?? panel.result?.usage?.output_tokens,
      panel.result?.latencyMS, panel.result?.firstTokenMS, estimateCost(panel.pricing, panel.result?.usage?.prompt_tokens ?? panel.result?.usage?.input_tokens, panel.result?.usage?.completion_tokens ?? panel.result?.usage?.output_tokens)].map(csvCell).join(",")).join("\r\n") + "\r\n";
    const url = URL.createObjectURL(new Blob([csv], { type: "text/csv;charset=utf-8" }));
    const link = document.createElement("a"); link.href = url; link.download = "ai-gateway-comparison.csv"; link.click(); URL.revokeObjectURL(url);
  }

  return <div className="playground-compare">
    <section className="playground-parameters-card playground-compare-connection" aria-label="Comparison connection">{connectionControls}</section>
    <div className="playground-compare-toolbar">
      <GravityThemeScope><Checkbox controlProps={{ "aria-label": "Sync settings across models" }} checked={sync} disabled={running} onUpdate={(value) => {
        setSync(value); if (value) setPanels((current) => current.map((panel) => ({ ...panel, settings: { ...current[0].settings }, instructions: current[0].instructions })));
      }}>Sync settings across models</Checkbox></GravityThemeScope>
      <GravityThemeScope><Checkbox controlProps={{ "aria-label": "Stream comparison" }} checked={stream} disabled={running} onUpdate={setStream}>Stream responses</Checkbox></GravityThemeScope>
      <div className="playground-actions"><GatewayButton view="outlined" disabled={running || !panels.some((panel) => panel.lastPrompt)} onClick={exportResults}>Export results</GatewayButton><GatewayButton view="outlined" disabled={running} onClick={() => { setPanels((current) => current.map((panel) => ({ ...newPanel(panel.id, panel.model), settings: panel.settings, instructions: panel.instructions, pricing: panel.pricing }))); setError(""); }}>Clear all chats</GatewayButton><GatewayButton view="outlined" disabled={running || panels.length >= 3} onClick={() => {
        const id = nextID.current++; setPanels((current) => [...current, { ...newPanel(id, models[current.length] || models[0] || ""), ...(sync ? { settings: { ...current[0].settings }, instructions: current[0].instructions } : {}) }]);
      }}>Add comparison</GatewayButton></div>
    </div>
    <div className="playground-comparison-panels" style={{ "--comparison-count": panels.length } as CSSProperties}>
      {panels.map((panel, index) => <section key={panel.id} className="playground-comparison-panel" aria-label={`Comparison ${index + 1}`}>
        <div className="playground-output-heading"><h2>Model {index + 1}</h2><GatewayButton view="flat" aria-label={`Remove comparison ${index + 1}`} disabled={running || panels.length <= 1} onClick={() => setPanels((current) => current.filter((item) => item.id !== panel.id))}>Remove</GatewayButton></div>
        {models.length ? <SelectControl label={`Model ${index + 1}`} value={panel.model} disabled={running} options={models.map((model) => ({ value: model, content: model }))} onUpdate={(model) => setPanels((current) => current.map((item) => item.id === panel.id ? { ...newPanel(item.id, model), settings: item.settings, instructions: item.instructions } : item))} />
          : <TextControl label={`Model ${index + 1}`} value={panel.model} disabled={running} onUpdate={(model) => setPanels((current) => current.map((item) => item.id === panel.id ? { ...newPanel(item.id, model), settings: item.settings, instructions: item.instructions } : item))} placeholder="Enter a model ID" />}
        <details className="playground-advanced"><summary>Model settings</summary>
          <AreaControl label={`Instructions ${index + 1}`} rows={2} value={panel.instructions} disabled={running} onUpdate={(value) => updateInstructions(panel.id, value)} />
          <TextControl label={`Temperature ${index + 1}`} type="number" controlProps={{ min: 0, max: 2, step: 0.1 }} value={panel.settings.temperature} disabled={running} placeholder="Provider default" onUpdate={(temperature) => settings(panel.id, { temperature })} />
          <TextControl label={`Max tokens ${index + 1}`} type="number" controlProps={{ min: 1, step: 1 }} value={panel.settings.maxTokens} disabled={running} onUpdate={(maxTokens) => settings(panel.id, { maxTokens })} />
          <TextControl label={`Top P ${index + 1}`} type="number" controlProps={{ min: 0, max: 1, step: 0.05 }} value={panel.settings.topP} disabled={running} placeholder="Provider default" onUpdate={(topP) => settings(panel.id, { topP })} />
          <SelectControl label={`Response format ${index + 1}`} value={panel.settings.responseFormat} disabled={running} options={[{ value: "text", content: "Text" }, { value: "json_object", content: "JSON object" }, { value: "json_schema", content: "JSON schema" }]} onUpdate={(responseFormat) => settings(panel.id, { responseFormat })} />
          {panel.settings.responseFormat === "json_schema" && <AreaControl label={`Output schema ${index + 1}`} rows={3} value={panel.settings.schema} disabled={running} onUpdate={(schema) => settings(panel.id, { schema })} />}
          <AreaControl label={`Advanced parameters ${index + 1}`} rows={3} value={panel.settings.advanced} disabled={running} onUpdate={(advanced) => settings(panel.id, { advanced })} />
        </details>
        <PricingControls label={` ${index + 1}`} disabled={running} value={panel.pricing} onUpdate={(pricing) => setPanels((current) => current.map((item) => item.id === panel.id ? { ...item, pricing } : item))} />
        {panel.historyDropped > 0 && <p className="muted">{panel.historyDropped} earlier turns removed to keep browser history bounded.</p>}
        <div className="playground-comparison-output" aria-live="polite">
          {!panel.history.length && !panel.pending && <p className="muted">Send the same prompt to compare responses.</p>}
          {panel.history.map((turn, turnIndex) => <article className={`playground-turn ${turn.role}`} key={turnIndex}><strong>{turn.role}</strong><pre>{typeof turn.content === "string" ? turn.content : turn.tool_calls ? "Tool call returned" : "No text output"}</pre></article>)}
          {panel.pending && <article className="playground-turn assistant streaming"><strong>{panel.status === "running" ? "Streaming" : "Partial response"}</strong><pre>{panel.pending}</pre></article>}
          {panel.error && <p role="alert" className="form-error">{panel.error}</p>}
        </div>
        {panel.result && <dl className="playground-metadata">
          <div><dt>Input tokens</dt><dd>{panel.result.usage?.prompt_tokens ?? panel.result.usage?.input_tokens ?? "—"}</dd></div>
          <div><dt>Output tokens</dt><dd>{panel.result.usage?.completion_tokens ?? panel.result.usage?.output_tokens ?? "—"}</dd></div>
          <div><dt>Reasoning tokens</dt><dd>{panel.result.usage?.completion_tokens_details?.reasoning_tokens ?? panel.result.usage?.output_tokens_details?.reasoning_tokens ?? "—"}</dd></div>
          <div><dt>Latency</dt><dd>{Math.round(panel.result.latencyMS)} ms</dd></div>
          <div><dt>First token</dt><dd>{panel.result.firstTokenMS === undefined ? "—" : `${Math.round(panel.result.firstTokenMS)} ms`}</dd></div>
          <div><dt>Estimated token cost</dt><dd>{estimateCost(panel.pricing, panel.result.usage?.prompt_tokens ?? panel.result.usage?.input_tokens, panel.result.usage?.completion_tokens ?? panel.result.usage?.output_tokens)}</dd></div>
          <div><dt>Finalized cost</dt><dd>See Usage &amp; spend</dd></div>
        </dl>}
      </section>)}
    </div>
    <form className="playground-compare-composer" onSubmit={submit}>
      <AreaControl label="Comparison prompt" rows={3} value={prompt} disabled={running} onUpdate={setPrompt} onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); if (!running) event.currentTarget.form?.requestSubmit(); } }} placeholder="Send the same prompt to all models" />
      <div className="playground-actions"><GatewayButton type="submit" disabled={running || connectionChanged || !prompt.trim() || panels.some((panel) => !panel.model)}>Compare models</GatewayButton>{running && <GatewayButton view="outlined" onClick={cancel}>Stop comparison</GatewayButton>}</div>
      {error && <p role="alert" className="form-error">{error}</p>}
    </form>
  </div>;
}
