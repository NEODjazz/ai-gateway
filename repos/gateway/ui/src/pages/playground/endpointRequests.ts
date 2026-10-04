import { jsonObject, optionalNumber } from "./requests";
import { nativeToolContent, nativeUserContent, type NativeToolResult } from "./nativeConversation";
import { agentRequest, type AgentTask } from "./agents";

export type SpecializedEndpoint = "messages" | "interactions" | "images" | "image-edits" | "embeddings" | "speech" | "transcription" | "a2a" | "mcp";
export const endpointPaths: Record<SpecializedEndpoint, string> = { messages: "/v1/messages", interactions: "/v1/interactions", images: "/v1/images/generations", "image-edits": "/v1/images/edits", embeddings: "/v1/embeddings", speech: "/v1/audio/speech", transcription: "/v1/audio/transcriptions", a2a: "/a2a", mcp: "/v1/mcp/servers" };
export type Attachment = { filename: string; media_type: string; data_base64: string };
export type EndpointSettings = { instructions: string; limit: string; temperature: string; topP: string; advanced: string; size: string; count: string; quality: string; dimensions: string; voice: string; format: string; speed: string; language: string; agent: string; server: string; tool: string; arguments: string; stream: boolean; agentStream: boolean };
export const defaultEndpointSettings: EndpointSettings = { instructions: "", limit: "256", temperature: "", topP: "", advanced: "", size: "", count: "1", quality: "", dimensions: "", voice: "alloy", format: "mp3", speed: "1", language: "", agent: "", server: "", tool: "", arguments: "{}", stream: true, agentStream: false };
const safeID = /^[a-zA-Z0-9._:-]{1,128}$/;
const maximumFileBytes = 8 * 1024 * 1024;

export async function readAttachment(file: File, kind: "image" | "audio" | "document"): Promise<Attachment> {
  const types = kind === "image" ? ["image/png", "image/jpeg", "image/gif", "image/webp"] : kind === "audio" ? ["audio/mpeg", "audio/mp3", "audio/wav", "audio/x-wav", "audio/ogg", "audio/flac", "audio/mp4", "audio/webm", "video/webm"] : ["application/pdf"];
  if (!types.includes(file.type)) throw new Error(`Unsupported ${kind} attachment type.`);
  if (!file.size || file.size > maximumFileBytes) throw new Error("Attachment must contain 1 byte to 8 MiB.");
  const data = await new Promise<string>((resolve, reject) => { const reader = new FileReader(); reader.onload = () => resolve(String(reader.result)); reader.onerror = () => reject(new Error("Could not read attachment.")); reader.readAsDataURL(file); });
  const index = data.indexOf(",");
  if (index < 0 || !data.slice(0, index).endsWith(";base64")) throw new Error("Could not encode attachment.");
  return { filename: file.name, media_type: file.type === "audio/x-wav" ? "audio/wav" : file.type === "audio/mp3" ? "audio/mpeg" : file.type, data_base64: data.slice(index + 1) };
}

export function buildEndpointRequest(endpoint: SpecializedEndpoint, model: string, input: string, settings: EndpointSettings, attachments: Attachment[] = [], mask?: Attachment, history: unknown[] = [], previousID = "", toolResults: NativeToolResult[] = [], agentTask?: AgentTask): { path: string; body: Record<string, unknown>; headers?: Record<string, string> } {
  const native = endpoint === "messages" || endpoint === "interactions";
  if (!["a2a", "mcp"].includes(endpoint) && !model.trim()) throw new Error("Select a model before sending a request.");
  if (!["transcription", "mcp"].includes(endpoint) && !input.trim() && !((native || endpoint === "a2a") && (attachments.length || toolResults.length))) throw new Error("Enter input before sending a request.");
  if (toolResults.length && (!native || input.trim() || attachments.length)) throw new Error("Tool continuation must contain only the reviewed tool results.");
  if (new TextEncoder().encode(input).length > 1024 * 1024) throw new Error("Prompt exceeds the 1 MiB Playground limit.");
  if (new TextEncoder().encode(settings.instructions).length > 65536) throw new Error("Instructions exceed the 64 KiB Playground limit.");
  const extras = settings.advanced.trim() ? jsonObject(settings.advanced, "Advanced parameters") : {};
  const protectedFields = ["model", "provider", "input", "messages", "system", "system_instruction", "prompt", "file", "images", "mask", "stream", "max_tokens", "generation_config", "previous_interaction_id", "arguments", "temperature", "top_p", "instructions", "dimensions", "voice", "response_format", "speed", "n", "quality", "size", "language", "encoding_format", "jsonrpc", "id", "method", "params", "__proto__", "prototype", "constructor"];
  for (const name of Object.keys(extras)) if (protectedFields.includes(name)) throw new Error(`Configure ${name} with its dedicated control.`);
  if (endpoint === "interactions" && extras.background === true && settings.stream) throw new Error("Disable Stream native response to run a background interaction.");
  const body: Record<string, unknown> = { ...extras, model: model.trim() };
  let path = endpointPaths[endpoint];
  if (endpoint === "messages" || endpoint === "interactions") {
    const limit = optionalNumber(settings.limit, "Maximum output tokens", 1, Number.MAX_SAFE_INTEGER, true);
    const temperature = optionalNumber(settings.temperature, "Temperature", 0, endpoint === "messages" ? 1 : 2), topP = optionalNumber(settings.topP, "Top P", 0, 1);
    body.stream = settings.stream;
    const content = toolResults.length ? nativeToolContent(endpoint, toolResults) : nativeUserContent(endpoint, input, attachments);
    if (endpoint === "messages") {
      if (limit === undefined) throw new Error("Messages requires a maximum output token limit.");
      body.max_tokens = limit; body.messages = [...history, { role: "user", content }];
      if (settings.instructions.trim()) body.system = settings.instructions.trim();
      if (temperature !== undefined) body.temperature = temperature; if (topP !== undefined) body.top_p = topP;
    } else {
      const next = toolResults.length ? content as unknown[] : [{ role: "user", content }];
      body.input = previousID || !history.length ? (toolResults.length ? content : attachments.length ? next : input) : [...history, ...next];
      if (previousID) body.previous_interaction_id = previousID;
      if (settings.instructions.trim()) body.system_instruction = settings.instructions.trim();
      body.generation_config = { ...(limit === undefined ? {} : { max_output_tokens: limit }), ...(temperature === undefined ? {} : { temperature }), ...(topP === undefined ? {} : { top_p: topP }) };
    }
  } else if (endpoint === "images" || endpoint === "image-edits") {
    body.prompt = input; body.n = optionalNumber(settings.count, "Image count", 1, 10, true); body.response_format = "b64_json";
    if (settings.size.trim()) body.size = settings.size.trim(); if (settings.quality.trim()) body.quality = settings.quality.trim();
    if (endpoint === "image-edits") { if (!attachments.length) throw new Error("Attach an image to edit."); body.images = attachments.map(({ media_type, data_base64 }) => ({ media_type, data_base64 })); if (mask) body.mask = { media_type: mask.media_type, data_base64: mask.data_base64 }; }
  } else if (endpoint === "embeddings") {
    body.input = input; body.encoding_format = "float";
    const dimensions = optionalNumber(settings.dimensions, "Dimensions", 1, 65536, true); if (dimensions !== undefined) body.dimensions = dimensions;
  } else if (endpoint === "speech") {
    if (!settings.voice.trim()) throw new Error("Enter a speech voice.");
    body.input = input; body.voice = settings.voice.trim(); body.response_format = settings.format;
    const speed = optionalNumber(settings.speed, "Speech speed", 0.25, 4); if (speed !== undefined) body.speed = speed;
    if (settings.instructions.trim()) body.instructions = settings.instructions.trim();
  } else if (endpoint === "transcription") {
    if (attachments.length !== 1) throw new Error("Attach one audio file to transcribe.");
    body.file = attachments[0]; body.response_format = "verbose_json";
    if (input.trim()) body.prompt = input; if (settings.language.trim()) body.language = settings.language.trim();
  } else if (endpoint === "a2a") {
    if (!safeID.test(settings.agent)) throw new Error("Enter a valid agent ID.");
    if (Object.keys(extras).length) throw new Error("A2A parameters are configured by the agent and task controls.");
    return agentRequest(settings.agent, input, agentTask, attachments, settings.agentStream);
  } else {
    if (!safeID.test(settings.server) || !safeID.test(settings.tool)) throw new Error("Enter a valid MCP server ID and tool name.");
    path += `/${encodeURIComponent(settings.server)}/tools/${encodeURIComponent(settings.tool)}`;
    return { path, body: { ...extras, arguments: jsonObject(settings.arguments, "Tool arguments") }, headers: { "Idempotency-Key": `playground-${crypto.randomUUID()}` } };
  }
  if (new TextEncoder().encode(JSON.stringify(body)).length > 24 * 1024 * 1024) throw new Error("Request exceeds the 24 MiB Playground limit.");
  return { path, body, ...(endpoint === "messages" ? { headers: { "anthropic-version": "2023-06-01" } } : {}) };
}

export function safeMediaURL(value: unknown): string | undefined {
  if (typeof value !== "string" || value.length > 24 * 1024 * 1024) return undefined;
  if (/^data:image\/(png|jpeg|gif|webp);base64,[A-Za-z0-9+/]+={0,2}$/.test(value)) return value;
  try { const url = new URL(value); if (["https:", "http:"].includes(url.protocol) && !url.username && !url.password) return url.href; } catch { /* Invalid output is displayed as data only. */ }
  return undefined;
}
