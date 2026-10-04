import type { SSEEvent } from "../../api/client";
import { textEndpointPaths, type PlaygroundConnection, type TextEndpoint } from "./requests";

export type ReportedUsage = {
  prompt_tokens?: number; completion_tokens?: number; input_tokens?: number; output_tokens?: number; total_tokens?: number;
  completion_tokens_details?: { reasoning_tokens?: number }; output_tokens_details?: { reasoning_tokens?: number };
};
export type TextRun = {
  text: string; reasoning: string; id?: string; model: string; usage?: ReportedUsage; response: Record<string, unknown>;
  streamed: boolean; lifecycle?: true; latencyMS: number; firstTokenMS?: number; events: SSEEvent[]; eventCount: number;
};

const maxEvents = 50;
const maxDisplayedEventCharacters = 8192;
const maxOutputCharacters = 2 * 1024 * 1024;

export function contentText(value: unknown): string {
  if (typeof value === "string") return value;
  if (Array.isArray(value)) return value.map(contentText).filter(Boolean).join("\n");
  if (!value || typeof value !== "object") return "";
  const item = value as Record<string, unknown>;
  if (typeof item.text === "string") return item.text;
  if (item.type === "refusal" && typeof item.refusal === "string") return item.refusal;
  if (item.content !== undefined) return contentText(item.content);
  return "";
}

function object(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
}

function responseError(payload: Record<string, unknown>, event = ""): void {
  const response = object(payload.response);
  const failure = payload.error || response?.error;
  if (!failure && event !== "error" && payload.type !== "response.failed" && response?.status !== "failed" && payload.status !== "failed") return;
  const message = object(failure)?.message;
  throw new Error(typeof message === "string" ? message : typeof failure === "string" ? failure : "Streaming request failed");
}

export function responsePending(response: Record<string, unknown>): boolean {
  return response.status === "queued" || response.status === "in_progress";
}
const terminalResponseEvents = ["response.completed", "response.incomplete", "response.failed", "response.cancelled"];
function identifiedFailedResponse(payload: Record<string, unknown> | undefined): boolean {
  return payload?.status === "failed" && typeof payload.id === "string" && /^[A-Za-z0-9_-]{1,256}$/.test(payload.id);
}
function responsePath(id: unknown): string {
  if (typeof id !== "string" || !/^[A-Za-z0-9_-]{1,256}$/.test(id)) throw new Error("Response lifecycle requires a valid response ID.");
  return `/v1/responses/${encodeURIComponent(id)}`;
}
function textResponseFields(payload: Record<string, unknown>, endpoint: TextEndpoint): { text: string; reasoning: string } {
  if (!object(payload)) throw new Error("Invalid response: expected a JSON object");
  if (JSON.stringify(payload).length > maxOutputCharacters) throw new Error("Structured response exceeds the 2 MiB Playground limit.");
  const message = object((Array.isArray(payload.choices) ? object(payload.choices[0]) : undefined)?.message);
  const text = endpoint === "chat" ? (contentText(message?.content) || contentText(message?.refusal)) : typeof payload.output_text === "string" ? payload.output_text : contentText(payload.output);
  const reasoning = contentText(message?.reasoning_content || message?.reasoning) || (endpoint === "responses" && Array.isArray(payload.output) ? payload.output.filter((item) => object(item)?.type === "reasoning").map((item) => contentText(object(item)?.summary)).filter(Boolean).join("\n") : "");
  if (text.length + reasoning.length > maxOutputCharacters) throw new Error("Text output exceeds the 2 MiB Playground limit.");
  if (endpoint === "responses" && responsePending(payload)) responsePath(payload.id);
  return { text, reasoning };
}
export async function runResponseResource(connection: PlaygroundConnection, id: string, operation: "refresh" | "cancel", signal: AbortSignal, sessionID: string): Promise<TextRun> {
  const start = performance.now(), path = responsePath(id);
  const response = await connection.client.request<Record<string, unknown>>(connection.path(path + (operation === "cancel" ? "/cancel" : "")), {
    method: operation === "cancel" ? "POST" : "GET", cache: "no-store", signal, maximumResponseBytes: maxOutputCharacters, headers: { "X-Session-ID": sessionID },
  });
  if (signal.aborted) throw new DOMException("Request cancelled", "AbortError");
  if (!object(response) || response.id !== id || !["queued", "in_progress", "completed", "incomplete", "failed", "cancelled"].includes(String(response.status))) throw new Error("Response lifecycle returned a mismatching ID or invalid status.");
  if (response.status !== "failed") responseError(response);
  const fields = textResponseFields(response, "responses");
  return { ...fields, response, id, model: typeof response.model === "string" ? response.model : "", usage: object(response.usage) as ReportedUsage | undefined, streamed: false, lifecycle: true, latencyMS: performance.now() - start, events: [], eventCount: 0 };
}

function boundedEvent(event: SSEEvent): SSEEvent {
  return { event: event.event, data: event.data.length > maxDisplayedEventCharacters ? `${event.data.slice(0, maxDisplayedEventCharacters)}\n… (event display truncated)` : event.data };
}

export async function runText(connection: PlaygroundConnection, endpoint: TextEndpoint, body: Record<string, unknown>, {
  signal, sessionID, onText = () => undefined, clock = () => performance.now()
}: { signal: AbortSignal; sessionID: string; onText?: (text: string) => void; clock?: () => number }): Promise<TextRun> {
  const start = clock();
  let text = "", reasoning = "", chatContent = "", refusal = "", firstTokenMS: number | undefined;
  let response: Record<string, unknown> = {};
  const toolCalls = new Map<number, { id: string; type: string; function: { name: string; arguments: string } }>();
  let toolCharacters = 0;
  const events: SSEEvent[] = [];
  let eventCount = 0;
  let terminal = false;
  const ensureActive = () => { if (signal.aborted) throw new DOMException("Request cancelled", "AbortError"); };
  const append = (delta: string) => {
    if (text.length + reasoning.length + toolCharacters + delta.length > maxOutputCharacters) throw new Error("Text output exceeds the 2 MiB Playground limit.");
    if (delta && firstTokenMS === undefined) firstTokenMS = clock() - start;
    text += delta; onText(text);
  };
  const acceptJSON = (payload: Record<string, unknown>) => {
    ensureActive();
    if (!object(payload)) throw new Error("Invalid response: expected a JSON object");
    if (endpoint !== "responses" || !identifiedFailedResponse(payload)) responseError(payload);
    const fields = textResponseFields(payload, endpoint);
    response = payload; text = fields.text; reasoning = fields.reasoning;
    onText(text);
  };
  const options = { maximumResponseBytes: 32 * 1024 * 1024, method: "POST", body, signal, headers: { "X-Session-ID": sessionID } };
  let streamed = false;
  if (!body.stream) {
    acceptJSON(await connection.client.request<Record<string, unknown>>(connection.path(textEndpointPaths[endpoint]), options));
  } else {
    const result = await connection.client.stream<Record<string, unknown>>(connection.path(textEndpointPaths[endpoint]), options, (event) => {
      ensureActive();
      if (event.data === "[DONE]") { if (endpoint === "chat") terminal = true; return; }
      let payload: Record<string, unknown> | undefined;
      try { payload = object(JSON.parse(event.data)); } catch { throw new Error("Malformed streaming event"); }
      if (!payload) throw new Error("Malformed streaming event");
      const type = typeof payload.type === "string" ? payload.type : event.event;
      const finalResponse = object(payload.response);
      const retainedFailure = endpoint === "responses" && terminalResponseEvents.includes(type) && event.event !== "error" && !payload.error && identifiedFailedResponse(finalResponse);
      if (!retainedFailure) responseError(payload, event.event);
      if (retainedFailure && response.id !== undefined && response.id !== finalResponse!.id) throw new Error("Final response belongs to another request.");
      eventCount++; events.push(boundedEvent(event)); if (events.length > maxEvents) events.shift();
      if (endpoint === "responses") {
        if (terminalResponseEvents.includes(type)) terminal = true;
        if (["response.output_text.delta", "response.refusal.delta"].includes(type) && typeof payload.delta === "string") append(payload.delta);
        if (type === "response.reasoning_summary_text.delta" && typeof payload.delta === "string") {
          if (text.length + reasoning.length + toolCharacters + payload.delta.length > maxOutputCharacters) throw new Error("Reasoning output exceeds the Playground limit.");
          reasoning += payload.delta;
        }
        const completed = object(payload.response);
        if (completed) {
          if (JSON.stringify(completed).length > maxOutputCharacters) throw new Error("Structured response exceeds the 2 MiB Playground limit.");
          response = completed;
          if (!text && terminalResponseEvents.includes(type)) {
            text = typeof completed.output_text === "string" ? completed.output_text : contentText(completed.output);
            if (text.length > maxOutputCharacters) throw new Error("Text output exceeds the 2 MiB Playground limit.");
            onText(text);
          }
        }
      } else {
        if (typeof payload.id === "string") response.id = payload.id;
        if (typeof payload.model === "string") response.model = payload.model;
        if (object(payload.usage)) response.usage = payload.usage;
        const firstChoice = Array.isArray(payload.choices) ? object(payload.choices[0]) : undefined;
        const delta = object(firstChoice?.delta);
        const contentDelta = contentText(delta?.content), refusalDelta = contentText(delta?.refusal);
        append(contentDelta); chatContent += contentDelta;
        append(refusalDelta); refusal += refusalDelta;
        const reasoningDelta = contentText(delta?.reasoning_content || delta?.reasoning);
        if (text.length + reasoning.length + toolCharacters + reasoningDelta.length > maxOutputCharacters) throw new Error("Reasoning output exceeds the Playground limit.");
        reasoning += reasoningDelta;
        if (Array.isArray(delta?.tool_calls)) for (const entry of delta.tool_calls) {
          const item = object(entry), fn = object(item?.function);
          if (!item || !Number.isInteger(item.index) || Number(item.index) < 0 || Number(item.index) >= 128) throw new Error("Invalid streamed tool call index");
          const index = Number(item.index), call = toolCalls.get(index) || { id: "", type: "function", function: { name: "", arguments: "" } };
          if (typeof item.id === "string") { toolCharacters += item.id.length - call.id.length; call.id = item.id; }
          if (typeof fn?.name === "string") { toolCharacters += fn.name.length; call.function.name += fn.name; }
          if (typeof fn?.arguments === "string") { toolCharacters += fn.arguments.length; call.function.arguments += fn.arguments; }
          if (text.length + reasoning.length + toolCharacters > maxOutputCharacters) throw new Error("Tool arguments exceed the Playground limit.");
          toolCalls.set(index, call);
        }
        if (firstChoice?.finish_reason != null) { response.finish_reason = firstChoice.finish_reason; terminal = true; }
      }
    }, true);
    if (!result.streamed) acceptJSON(result.data);
    else streamed = true;
  }
  ensureActive();
  if (streamed && !terminal) throw new Error("Stream ended before the response completed.");
  if (streamed && endpoint === "chat") response.choices = [{ message: { role: "assistant", content: chatContent || null, ...(refusal ? { refusal } : {}), ...(toolCalls.size ? { tool_calls: [...toolCalls.entries()].sort(([a], [b]) => a - b).map(([, value]) => value) } : {}), ...(reasoning ? { reasoning_content: reasoning } : {}) }, finish_reason: response.finish_reason }];
  return { text, reasoning, response, streamed, model: typeof response.model === "string" ? response.model : String(body.model || ""),
    id: typeof response.id === "string" ? response.id : undefined, usage: object(response.usage) as ReportedUsage | undefined,
    events, eventCount, latencyMS: clock() - start, firstTokenMS };
}
