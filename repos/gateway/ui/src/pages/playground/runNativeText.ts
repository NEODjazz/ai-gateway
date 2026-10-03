import type { SSEEvent } from "../../api/client";
import type { PlaygroundConnection } from "./requests";
import { contentText } from "./runText";

export type NativeTextRun = { text: string; reasoning: string; response: Record<string, unknown>; events: SSEEvent[]; eventCount: number; latencyMS: number; firstTokenMS?: number };
const maximumCharacters = 2 * 1024 * 1024;
function record(value: unknown): Record<string, unknown> | undefined { return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : undefined; }
function publicText(content: unknown): string { return Array.isArray(content) ? content.filter((item) => record(item)?.type === "text").map(contentText).join("\n") : ""; }
function interactionText(response: Record<string, unknown>, type: string): string { return Array.isArray(response.steps) ? response.steps.filter((item) => record(item)?.type === type).map(contentText).join("\n") : ""; }

export async function runNativeText(connection: PlaygroundConnection, endpoint: "messages" | "interactions", body: Record<string, unknown>, signal: AbortSignal, onText: (text: string) => void): Promise<NativeTextRun> {
  const start = performance.now(), events: SSEEvent[] = [], blocks = new Map<number, Record<string, unknown>>(), argumentsByIndex = new Map<number, string>();
  let text = "", reasoning = "", firstTokenMS: number | undefined, response: Record<string, unknown> = {}, eventCount = 0, terminal = false, characters = 0;
  const active = () => { if (signal.aborted) throw new DOMException("Request cancelled", "AbortError"); };
  const bound = (addition: string) => { characters += addition.length; if (characters > maximumCharacters) throw new Error("Native output exceeds the 2 MiB Playground limit."); };
  const fail = (payload: Record<string, unknown>) => { const failure = record(payload.error); if (failure || payload.status === "failed") throw new Error(typeof failure?.message === "string" ? failure.message : "Native request failed"); };
  const accept = (payload: Record<string, unknown>) => {
    active(); if (!record(payload)) throw new Error("Invalid native response"); fail(payload); response = payload;
    text = endpoint === "messages" ? publicText(payload.content) : interactionText(payload, "model_output");
    reasoning = endpoint === "messages" && Array.isArray(payload.content) ? payload.content.filter((item) => record(item)?.type === "thinking").map((item) => record(item)?.thinking || "").join("\n") : interactionText(payload, "thought");
    if (text.length + reasoning.length > maximumCharacters) throw new Error("Native output exceeds the 2 MiB Playground limit."); onText(text);
  };
  const options = { method: "POST", body, signal };
  const path = connection.path(endpoint === "messages" ? "/v1/messages" : "/v1/interactions");
  if (!body.stream) accept(await connection.client.request<Record<string, unknown>>(path, options));
  else {
    const result = await connection.client.stream<Record<string, unknown>>(path, options, (event) => {
      active(); if (event.data === "[DONE]") return;
      let payload: Record<string, unknown> | undefined;
      try { payload = record(JSON.parse(event.data)); } catch { throw new Error("Malformed native stream event"); }
      if (!payload) throw new Error("Malformed native stream event"); fail(payload);
      eventCount++; events.push({ event: event.event, data: event.data.slice(0, 8192) }); if (events.length > 50) events.shift();
      const type = String(payload.type || payload.event_type || event.event), delta = record(payload.delta);
      if (endpoint === "messages") {
        if (type === "message_start") { response = record(payload.message) || {}; fail(response); }
        if (type === "content_block_start") {
          if (!Number.isInteger(payload.index) || Number(payload.index) < 0 || Number(payload.index) >= 128) throw new Error("Invalid native block index");
          const block = record(payload.content_block); if (!block) throw new Error("Invalid native content block"); bound(JSON.stringify(block)); blocks.set(Number(payload.index), { ...block });
          if (block.type === "text" && typeof block.text === "string") { text += block.text; onText(text); }
        }
        if (type === "content_block_delta") {
          const index = Number(payload.index), block = blocks.get(index);
          if (!block) throw new Error("Native delta references an unavailable content block");
          if (delta?.type === "text_delta" && typeof delta.text === "string") { bound(delta.text); if (firstTokenMS === undefined) firstTokenMS = performance.now() - start; text += delta.text; block.text = String(block.text || "") + delta.text; onText(text); }
          if (delta?.type === "thinking_delta" && typeof delta.thinking === "string") { bound(delta.thinking); reasoning += delta.thinking; block.thinking = String(block.thinking || "") + delta.thinking; }
          if (delta?.type === "signature_delta" && typeof delta.signature === "string") { bound(delta.signature); block.signature = String(block.signature || "") + delta.signature; }
          if (delta?.type === "input_json_delta" && typeof delta.partial_json === "string") { bound(delta.partial_json); argumentsByIndex.set(index, (argumentsByIndex.get(index) || "") + delta.partial_json); }
          if (delta?.type === "citations_delta") { bound(JSON.stringify(delta.citation)); block.citations = [...(Array.isArray(block.citations) ? block.citations : []), delta.citation]; }
        }
        if (type === "message_delta") { if (record(payload.usage)) response.usage = { ...record(response.usage), ...record(payload.usage) }; if (delta?.stop_reason) response.stop_reason = delta.stop_reason; }
        if (type === "message_stop") terminal = true;
      } else {
        if (type === "step.delta" && typeof delta?.text === "string") {
          bound(delta.text);
          if (delta.type === "thought_summary") reasoning += delta.text;
          else { if (firstTokenMS === undefined) firstTokenMS = performance.now() - start; text += delta.text; onText(text); }
        }
        const interaction = record(payload.interaction);
        if (interaction) { fail(interaction); response = interaction; }
        if (["interaction.completed", "interaction.incomplete"].includes(type)) terminal = true;
      }
    }, true);
    if (!result.streamed) accept(result.data);
    else {
      if (!terminal) throw new Error("Native stream ended before completion.");
      if (endpoint === "messages") {
        for (const [index, json] of argumentsByIndex) { try { blocks.get(index)!.input = JSON.parse(json); } catch { throw new Error("Native tool arguments contain invalid JSON"); } }
        response.content = [...blocks.entries()].sort(([a], [b]) => a - b).map(([, value]) => value);
      } else if (!text) text = interactionText(response, "model_output");
    }
  }
  active(); return { text, reasoning, response, events, eventCount, firstTokenMS, latencyMS: performance.now() - start };
}
