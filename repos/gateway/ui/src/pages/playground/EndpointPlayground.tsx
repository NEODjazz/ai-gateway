import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { Checkbox } from "@gravity-ui/uikit";
import { GatewayButton } from "../../components/GatewayButton";
import { GravityThemeScope } from "../../components/GravityThemeScope";
import { AreaControl, SelectControl, TextControl } from "./Controls";
import { CodeDialog } from "./CodeDialog";
import { buildEndpointRequest, defaultEndpointSettings, readAttachment, safeMediaURL, type Attachment, type EndpointSettings, type SpecializedEndpoint } from "./endpointRequests";
import { runNativeText, type NativeTextRun } from "./runNativeText";
import type { PlaygroundConnection } from "./requests";
import { contentText } from "./runText";

type Output = { payload?: Record<string, unknown>; native?: NativeTextRun; audio?: { url: string; type: string }; latencyMS: number };
function object(value: unknown): Record<string, unknown> | undefined { return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : undefined; }
export function EndpointPlayground({ endpoint, connection, connectionChanged, connectionControls, models }: {
  endpoint: SpecializedEndpoint; connection: PlaygroundConnection; connectionChanged: boolean; connectionControls: ReactNode; models: string[];
}) {
  const [model, setModel] = useState(models[0] || ""), [input, setInput] = useState(""), [settings, setSettings] = useState<EndpointSettings>(defaultEndpointSettings);
  const [attachments, setAttachments] = useState<Attachment[]>([]), [mask, setMask] = useState<Attachment>();
  const [output, setOutput] = useState<Output>(), [pending, setPending] = useState(""), [error, setError] = useState("");
  const [running, setRunning] = useState(false), [reading, setReading] = useState(false), [code, setCode] = useState<{ path: string; body: unknown; baseURL: string; headers?: Record<string, string>; binaryOutput?: boolean }>();
  const [history, setHistory] = useState<unknown[]>([]), [previousID, setPreviousID] = useState("");
  const abort = useRef<AbortController | undefined>(undefined), generation = useRef(0), audioURL = useRef<string | undefined>(undefined);
  const textEndpoint = endpoint === "messages" || endpoint === "interactions";
  const imageEndpoint = endpoint === "images" || endpoint === "image-edits";
  useEffect(() => () => { generation.current++; abort.current?.abort(); if (audioURL.current) URL.revokeObjectURL(audioURL.current); }, []);
  useEffect(() => { clear(); setAttachments([]); setMask(undefined); setModel(models[0] || ""); }, [connection]);
  useEffect(() => { if (models.length && !models.includes(model)) { setModel(models[0]); clear(); } }, [models]);
  const patch = (value: Partial<EndpointSettings>) => setSettings((current) => ({ ...current, ...value }));
  function clear() { generation.current++; abort.current?.abort(); abort.current = undefined; setRunning(false); setOutput(undefined); setPending(""); setHistory([]); setPreviousID(""); setError(""); if (audioURL.current) URL.revokeObjectURL(audioURL.current); audioURL.current = undefined; }
  async function attach(files: FileList | null, isMask = false) {
    if (!files?.length || reading || running) return;
    const current = generation.current; setReading(true); setError("");
    try { if (files.length > 8 || [...files].reduce((sum, file) => sum + file.size, 0) > 16 * 1024 * 1024) throw new Error("Choose at most 8 images, up to 16 MiB in total.");
      if (isMask && files[0].type !== "image/png") throw new Error("Image mask must be PNG.");
      const items = await Promise.all([...files].map((file) => readAttachment(file, endpoint === "transcription" ? "audio" : "image")));
      if (current === generation.current) { if (isMask) setMask(items[0]); else setAttachments(items); }
    } catch (cause) { if (current === generation.current) setError(cause instanceof Error ? cause.message : "Attachment failed"); }
    finally { setReading(false); }
  }
  function body() { return buildEndpointRequest(endpoint, model, input, settings, attachments, mask, history, previousID); }
  function getCode() {
    try { const request = body(); setCode({ path: request.path, body: request.body, baseURL: connection.baseURL, headers: request.headers, binaryOutput: endpoint === "speech" }); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not generate code"); }
  }
  async function submit(event: FormEvent) {
    event.preventDefault(); if (running || reading) return;
    if (connectionChanged) { setError("Apply connection changes before running."); return; }
    let request: ReturnType<typeof body>;
    try { request = body(); } catch (cause) { setError(cause instanceof Error ? cause.message : "Invalid request"); return; }
    const controller = new AbortController(); abort.current = controller; const current = ++generation.current, start = performance.now(); setRunning(true); setError(""); setPending(""); setOutput(undefined);
    try {
      let result: Output;
      if (textEndpoint) {
        const native = await runNativeText(connection, endpoint, request.body, controller.signal, (text) => { if (current === generation.current) setPending(text); });
        result = { native, payload: native.response, latencyMS: native.latencyMS };
        if (current !== generation.current || controller.signal.aborted) return;
        if (endpoint === "messages") setHistory((existing) => [...existing, { role: "user", content: input }, { role: "assistant", content: native.response.content }].slice(-40));
        else if (typeof native.response.id === "string") setPreviousID(native.response.id);
      } else if (endpoint === "speech") {
        const audio = await connection.client.requestBinary(connection.path(request.path), { method: "POST", body: request.body, signal: controller.signal });
        if (current !== generation.current || controller.signal.aborted) return;
        if (audio.body.size > 32 * 1024 * 1024) throw new Error("Audio output exceeds the 32 MiB Playground limit.");
        if (audioURL.current) URL.revokeObjectURL(audioURL.current); const url = URL.createObjectURL(audio.body); audioURL.current = url;
        result = { audio: { url, type: audio.contentType.split(";")[0].toLowerCase() }, latencyMS: performance.now() - start };
      } else {
        const payload = await connection.client.request<Record<string, unknown>>(connection.path(request.path), { method: "POST", body: request.body, headers: request.headers, signal: controller.signal });
        if (!object(payload)) throw new Error("Gateway returned an invalid response.");
        const failure = object(payload.error); if (failure) throw new Error(String(failure.message || "Request failed"));
        if (endpoint === "mcp" && payload.isError === true) throw new Error(contentText(payload.content) || "MCP tool returned an error");
        result = { payload, latencyMS: performance.now() - start };
      }
      if (current !== generation.current || controller.signal.aborted) return;
      setOutput(result); setPending("");
    } catch (cause) { if (current === generation.current) setError(controller.signal.aborted ? "Request cancelled" : cause instanceof Error ? cause.message : "Request failed"); }
    finally { if (current === generation.current) { setRunning(false); abort.current = undefined; } }
  }
  const payload = output?.payload, native = output?.native;
  const usage = object(payload?.usage);
  const images = imageEndpoint && Array.isArray(payload?.data) ? payload.data.slice(0, 10).map((value) => { const item = object(value); return { src: safeMediaURL(item?.b64_json ? `data:image/png;base64,${item.b64_json}` : item?.url), prompt: item?.revised_prompt }; }) : [];
  const embeddings = endpoint === "embeddings" && Array.isArray(payload?.data) ? payload.data.slice(0, 100).map((value) => object(value)) : [];
  const rpcResult = object(payload?.result), task = object(rpcResult?.task), agentMessage = object(rpcResult?.message);
  return <div className="playground-workspace playground-config-layout">
    <aside className="playground-side-panel"><section className="playground-parameters-card"><h2>Configurations</h2>{connectionControls}
      {!["a2a", "mcp"].includes(endpoint) && (models.length ? <SelectControl label="Endpoint model" value={model} disabled={running} options={models.map((id) => ({ value: id, content: id }))} onUpdate={(value) => { setModel(value); clear(); }} /> : <TextControl label="Endpoint model" value={model} disabled={running} onUpdate={(value) => { setModel(value); clear(); }} />)}
      {textEndpoint && <><AreaControl label="Native system instructions" rows={3} value={settings.instructions} disabled={running} onUpdate={(instructions) => patch({ instructions })} /><TextControl label="Native maximum output tokens" value={settings.limit} type="number" disabled={running} onUpdate={(limit) => patch({ limit })} /><TextControl label="Native temperature" value={settings.temperature} type="number" disabled={running} onUpdate={(temperature) => patch({ temperature })} /><TextControl label="Native Top P" value={settings.topP} type="number" disabled={running} onUpdate={(topP) => patch({ topP })} /><GravityThemeScope><Checkbox controlProps={{ "aria-label": "Stream native response" }} checked={settings.stream} disabled={running} onUpdate={(stream) => patch({ stream })}>Stream response</Checkbox></GravityThemeScope></>}
      {imageEndpoint && <><TextControl label="Image size" value={settings.size} disabled={running} onUpdate={(size) => patch({ size })} placeholder="Provider default" /><TextControl label="Image count" type="number" value={settings.count} disabled={running} onUpdate={(count) => patch({ count })} /><TextControl label="Image quality" value={settings.quality} disabled={running} onUpdate={(quality) => patch({ quality })} placeholder="Provider default" /></>}
      {endpoint === "embeddings" && <TextControl label="Embedding dimensions" value={settings.dimensions} disabled={running} type="number" onUpdate={(dimensions) => patch({ dimensions })} placeholder="Model default" />}
      {endpoint === "speech" && <><TextControl label="Speech voice" value={settings.voice} disabled={running} onUpdate={(voice) => patch({ voice })} /><SelectControl label="Speech format" value={settings.format} disabled={running} options={["mp3", "wav", "opus", "aac", "flac", "pcm"].map((value) => ({ value, content: value }))} onUpdate={(format) => patch({ format })} /><TextControl label="Speech speed" type="number" value={settings.speed} disabled={running} onUpdate={(speed) => patch({ speed })} /></>}
      {endpoint === "transcription" && <TextControl label="Transcription language" value={settings.language} disabled={running} onUpdate={(language) => patch({ language })} placeholder="Optional language code" />}
      {endpoint === "a2a" && <TextControl label="Agent ID" value={settings.agent} disabled={running} onUpdate={(agent) => { patch({ agent }); clear(); }} />}
      {endpoint === "mcp" && <><TextControl label="MCP server ID" value={settings.server} disabled={running} onUpdate={(server) => patch({ server })} /><TextControl label="MCP tool name" value={settings.tool} disabled={running} onUpdate={(tool) => patch({ tool })} /><AreaControl label="MCP arguments JSON" rows={6} value={settings.arguments} disabled={running} onUpdate={(args) => patch({ arguments: args })} /></>}
      <details><summary>Advanced endpoint parameters</summary><AreaControl label="Endpoint parameters JSON" rows={5} value={settings.advanced} disabled={running} onUpdate={(advanced) => patch({ advanced })} /><p className="muted">Uses this endpoint's native parameter dialect. Unsupported parameters produce an error.</p></details>
    </section></aside>
    <div className="playground-main-panel"><form className="playground-conversation-card" onSubmit={submit}>
      <div className="playground-output-heading"><h2>{textEndpoint ? "Native conversation" : "Endpoint request"}</h2><div className="playground-actions"><GatewayButton view="outlined" disabled={running} onClick={clear}>Clear endpoint output</GatewayButton><GatewayButton view="outlined" disabled={running} onClick={getCode}>Get endpoint code</GatewayButton></div></div>
      {endpoint !== "mcp" && <AreaControl label={endpoint === "speech" ? "Speech text" : endpoint === "transcription" ? "Transcription prompt" : endpoint === "embeddings" ? "Embedding input" : "Endpoint input"} rows={5} value={input} disabled={running} onUpdate={setInput} />}
      {(endpoint === "image-edits" || endpoint === "transcription") && <><label>Input {endpoint === "transcription" ? "audio" : "images"}<input aria-label="Endpoint attachment" type="file" multiple={endpoint === "image-edits"} disabled={running || reading} accept={endpoint === "transcription" ? "audio/*,.webm" : "image/png,image/jpeg,image/webp,image/gif"} onChange={(event) => void attach(event.currentTarget.files)} /></label>{attachments.map((item) => <p key={item.filename}>{item.filename} · {item.media_type}</p>)}<GatewayButton view="flat" disabled={running || reading} onClick={() => setAttachments([])}>Remove attachments</GatewayButton></>}
      {endpoint === "image-edits" && <label>Optional mask<input aria-label="Image mask attachment" type="file" accept="image/png" disabled={running || reading} onChange={(event) => void attach(event.currentTarget.files, true)} />{mask && <><span>{mask.filename}</span><GatewayButton view="flat" disabled={running} onClick={() => setMask(undefined)}>Remove mask</GatewayButton></>}</label>}
      {endpoint === "mcp" && <p className="muted">Running this request executes the selected tool with these arguments and a unique idempotency key. Review the tool and arguments before execution.</p>}
      <div className="playground-actions"><GatewayButton type="submit" disabled={running || reading || connectionChanged}>{running ? "Running…" : endpoint === "mcp" ? "Execute MCP tool" : "Run endpoint request"}</GatewayButton>{running && <GatewayButton view="outlined" onClick={() => abort.current?.abort()}>Stop endpoint request</GatewayButton>}</div>
      {error && <p role="alert" className="form-error">{error}</p>}{pending && <pre className="playground-native-text">{pending}</pre>}
      {native && <div className="playground-native-output"><h3>Assistant</h3><pre>{native.text || "No text output"}</pre>{native.reasoning && <details><summary>Reasoning</summary><pre>{native.reasoning}</pre></details>}</div>}
      {images.map((item, index) => <figure key={index}>{item.src ? <><img className="playground-generated-image" src={item.src} alt={`Generated image ${index + 1}`} referrerPolicy="no-referrer" /><a href={item.src} download={`ai-gateway-image-${index + 1}.png`} target="_blank" rel="noopener noreferrer" referrerPolicy="no-referrer">Open/download image</a></> : <p>Image output URL is unavailable or unsupported.</p>}{typeof item.prompt === "string" && <figcaption>{item.prompt}</figcaption>}</figure>)}
      {output?.audio && <div>{/^audio\/(mpeg|wav|x-wav|ogg|flac|mp4|webm|aac)$/.test(output.audio.type) ? <audio controls src={output.audio.url} aria-label="Generated speech" /> : <p>Audio format has no browser preview. Download to play it.</p>}<a href={output.audio.url} download={`ai-gateway-speech.${settings.format}`}>Download speech</a></div>}
      {!!embeddings.length && <section aria-label="Embedding vectors"><h3>Embedding vectors</h3>{embeddings.map((item, index) => <details key={index}><summary>Vector {String(item?.index ?? index)} · {Array.isArray(item?.embedding) ? item.embedding.length : "—"} dimensions</summary><pre>{JSON.stringify(item?.embedding ?? null).slice(0, 65536)}</pre></details>)}</section>}
      {endpoint === "transcription" && typeof payload?.text === "string" && <section><h3>Transcript</h3><pre>{payload.text}</pre></section>}
      {endpoint === "a2a" && rpcResult && <section><h3>Agent response</h3>{task && <p>Status: {String(object(task.status)?.state || "unknown")} · Task {String(task.id || "—")}</p>}<pre>{contentText(agentMessage?.parts) || (Array.isArray(task?.artifacts) ? task.artifacts.map((artifact) => contentText(object(artifact)?.parts)).join("\n") : "") || "No text output"}</pre></section>}
      {endpoint === "mcp" && payload && <section><h3>Tool output</h3><pre>{contentText(payload.content) || JSON.stringify(payload, null, 2).slice(0, 65536)}</pre></section>}
      {payload && <details><summary>Response details</summary><pre>{JSON.stringify(payload, null, 2).slice(0, 65536)}</pre></details>}
    </form><section className="playground-metadata-card"><h2>Response metadata</h2><dl className="playground-metadata"><div><dt>Status</dt><dd>{String(payload?.status ?? payload?.stop_reason ?? (output ? "Response received" : "—"))}</dd></div><div><dt>Latency</dt><dd>{output ? `${Math.round(output.latencyMS)} ms` : "—"}</dd></div><div><dt>First token</dt><dd>{native?.firstTokenMS === undefined ? "—" : `${Math.round(native.firstTokenMS)} ms`}</dd></div><div><dt>Input tokens</dt><dd>{String(usage?.input_tokens ?? usage?.prompt_tokens ?? usage?.total_input_tokens ?? "—")}</dd></div><div><dt>Output tokens</dt><dd>{String(usage?.output_tokens ?? usage?.completion_tokens ?? usage?.total_output_tokens ?? "—")}</dd></div></dl>{native?.events.length ? <details><summary>Native events ({native.events.length}/{native.eventCount})</summary><pre>{native.events.map((event) => `${event.event}: ${event.data}`).join("\n\n")}</pre></details> : null}</section></div>
    {code && <CodeDialog {...code} onClose={() => setCode(undefined)} />}
  </div>;
}
