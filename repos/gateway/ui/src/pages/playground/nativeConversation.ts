import { exactObjectText, stringifyExactJSON } from "./exactJSON";
import type { Attachment } from "./endpointRequests";

export type NativeEndpoint = "messages" | "interactions";
export type NativeTurn = { role: "user" | "assistant" | "tool"; content: unknown; text?: string; reasoning?: string };
export type NativeToolCall = { id: string; name: string; arguments: Record<string, unknown>; rawArguments: string; definition?: string; issue?: string };
export type NativeToolResult = { id: string; text: string; declined: boolean };
function record(value: unknown): Record<string, unknown> | undefined { return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : undefined; }

export function validateConversationFiles(files: Attachment[]): void {
  let bytes = 0;
  for (const file of files) {
    if (files.length > 5 || !["image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf"].includes(file.media_type) || !file.filename || file.filename.length > 256 || /[\\/\x00]/.test(file.filename) || !file.data_base64) throw new Error("Invalid native attachment; choose at most 5 images/PDF files.");
    bytes += file.data_base64.length / 4 * 3 - (file.data_base64.endsWith("==") ? 2 : file.data_base64.endsWith("=") ? 1 : 0);
    if (bytes > 8 * 1024 * 1024) throw new Error("Native attachments exceed the 8 MiB Playground limit.");
    if (file.data_base64.length % 4 !== 0 || !/^[A-Za-z0-9+/]*={0,2}$/.test(file.data_base64)) throw new Error("Invalid native attachment encoding.");
  }
}

export function nativeUserContent(endpoint: NativeEndpoint, text: string, files: Attachment[]): unknown {
  if (!files.length) return text;
  validateConversationFiles(files);
  return [...(text.trim() ? [{ type: endpoint === "messages" ? "text" : "input_text", text }] : []), ...files.map((file) => {
    if (endpoint === "messages") return { type: file.media_type === "application/pdf" ? "document" : "image", source: { type: "base64", media_type: file.media_type, data: file.data_base64 } };
    return file.media_type === "application/pdf" ? { type: "input_file", filename: file.filename, file_data: `data:application/pdf;base64,${file.data_base64}` } : { type: "input_image", image_url: `data:${file.media_type};base64,${file.data_base64}` };
  })];
}

export function nativeToolCalls(endpoint: NativeEndpoint, payload: Record<string, unknown>, tools: unknown = []): NativeToolCall[] {
  const content = endpoint === "messages" ? payload.content : payload.steps;
  if (!Array.isArray(content)) throw new Error("Native response is missing conversation content.");
  const calls: NativeToolCall[] = [], seen = new Set<string>();
  for (const value of content) {
    const item = record(value);
    if (item?.type !== (endpoint === "messages" ? "tool_use" : "function_call")) continue;
    const args = endpoint === "messages" ? item.input : item.arguments;
    if (calls.length >= 32 || typeof item.id !== "string" || !/^[A-Za-z0-9._:-]{1,128}$/.test(item.id) || seen.has(item.id) || typeof item.name !== "string" || !/^[A-Za-z0-9_-]{1,64}$/.test(item.name) || !record(args) || new TextEncoder().encode(exactObjectText(args)).length > 65536) throw new Error("Native response contains an invalid or oversized tool call. Clear the conversation before retrying.");
    const definitions = nativeDefinitions(endpoint, tools, item.name);
    seen.add(item.id); calls.push({ id: item.id, name: item.name, arguments: args as Record<string, unknown>, rawArguments: exactObjectText(args),
      definition: definitions.length === 1 ? stringifyExactJSON(definitions[0]) : undefined,
      issue: definitions.length === 1 ? undefined : "This call is not declared as one tool in the submitted request. It can only be declined." });
  }
  return calls;
}

export function nativeToolContent(endpoint: NativeEndpoint, results: NativeToolResult[]): Record<string, unknown>[] {
  const ids = new Set<string>();
  return results.map((result) => {
    if (results.length > 32 || !/^[A-Za-z0-9._:-]{1,128}$/.test(result.id) || ids.has(result.id) || !result.text.trim() || new TextEncoder().encode(result.text).length > 128 * 1024) throw new Error("Supply a non-empty tool result of at most 128 KiB for each distinct call.");
    ids.add(result.id);
    return endpoint === "messages" ? { type: "tool_result", tool_use_id: result.id, content: result.text, ...(result.declined ? { is_error: true } : {}) } : { type: "function_call_output", call_id: result.id, output: result.declined ? JSON.stringify({ error: result.text }) : result.text };
  });
}

export function nativeHistory(endpoint: NativeEndpoint, turns: NativeTurn[]): unknown[] {
  if (endpoint === "messages") return turns.map(({ role, content }) => ({ role: role === "tool" ? "user" : role, content }));
  return turns.flatMap((turn): unknown[] => {
    if (turn.role === "tool") return Array.isArray(turn.content) ? turn.content : [];
    if (turn.role === "user") return [{ role: "user", content: turn.content }];
    if (!Array.isArray(turn.content)) throw new Error("Interaction history is missing response steps.");
    return turn.content.flatMap((value): unknown[] => {
      const step = record(value);
      if (step?.type === "model_output") {
        if (!Array.isArray(step.content) || step.content.some((part) => record(part)?.type !== "text" || typeof record(part)?.text !== "string")) throw new Error("Interaction output cannot be replayed as text. Use stored continuity or clear the conversation.");
        return [{ role: "assistant", content: step.content.map((part) => ({ type: "output_text", text: record(part)!.text })) }];
      }
      if (step?.type === "function_call") return [{ type: "function_call", call_id: step.id, name: step.name, arguments: exactObjectText(step.arguments) }];
      // Thought summaries are display-only; they are not signed reasoning items.
      if (step?.type === "thought") return [];
      throw new Error("Interaction contains a step that cannot be replayed. Use stored continuity or clear the conversation.");
    });
  });
}

function nativeDefinitions(endpoint: NativeEndpoint, tools: unknown, name: string) {
  return Array.isArray(tools) ? tools.filter((tool) => record(tool)?.name === name && (endpoint === "messages" || record(tool)?.type === "function")) : [];
}

export function validateNativeContinuation(endpoint: NativeEndpoint, calls: NativeToolCall[], results: NativeToolResult[], tools: unknown) {
  for (const call of calls) {
    if (call.issue && !results.find((result) => result.id === call.id)?.declined) throw new Error("This native tool call can only be declined.");
    if (call.definition === undefined) continue;
    const definitions = nativeDefinitions(endpoint, tools, call.name);
    if (definitions.length !== 1 || stringifyExactJSON(definitions[0]) !== call.definition) throw new Error("The native tool definition changed after this call was requested. Restore its original configuration or clear the conversation.");
  }
}
