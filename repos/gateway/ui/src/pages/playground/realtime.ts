import type { PlaygroundConnection } from "./requests";
import { optionalNumber } from "./requests";

export const realtimeProtocol = "ai-gateway.realtime.v1";
export const maximumAudioBytes = 8 * 1024 * 1024;
export const maximumRealtimeTextBytes = 2 * 1024 * 1024;
export type RealtimeSettings = { mode: "text" | "voice"; dialect: "current" | "legacy"; voice: string; instructions: string; limit: string };
export const defaultRealtimeSettings: RealtimeSettings = { mode: "text", dialect: "current", voice: "alloy", instructions: "", limit: "256" };
export function realtimeSession(settings: RealtimeSettings) {
  if (new TextEncoder().encode(settings.instructions).length > 65536) throw new Error("Instructions exceed 64 KiB.");
  if (!/^[A-Za-z0-9._-]{1,64}$/.test(settings.voice)) throw new Error("Enter a valid voice name.");
  const limit = realtimeOutputLimit(settings);
  const base = { instructions: settings.instructions };
  if (settings.dialect === "legacy") return { type: "session.update", session: { ...base, max_response_output_tokens: limit, modalities: settings.mode === "voice" ? ["text", "audio"] : ["text"], ...(settings.mode === "voice" ? { voice: settings.voice, input_audio_format: "pcm16", output_audio_format: "pcm16", turn_detection: null } : {}) } };
  return { type: "session.update", session: { ...base, max_output_tokens: limit, type: "realtime", output_modalities: [settings.mode === "voice" ? "audio" : "text"], ...(settings.mode === "voice" ? { audio: { input: { format: { type: "audio/pcm", rate: 24000 }, turn_detection: null }, output: { format: { type: "audio/pcm", rate: 24000 }, voice: settings.voice } } } : {}) } };
}
function realtimeOutputLimit(settings: RealtimeSettings): number {
  const value = optionalNumber(settings.limit, "Maximum response tokens", 1, 4096, true);
  if (value === undefined) throw new Error("Maximum response tokens is required for bounded Realtime execution.");
  return value;
}
export function realtimeResponse(settings: RealtimeSettings) {
  return { type: "response.create", response: { max_output_tokens: realtimeOutputLimit(settings) } };
}
export function realtimeSocketURL(connection: PlaygroundConnection, model: string, origin: string): string {
  if (!model.trim() || new TextEncoder().encode(model).length > 256) throw new Error("Select a valid Realtime model.");
  const url = new URL(connection.path("/v1/realtime/browser"), origin);
  if (url.origin !== origin) throw new Error("Browser Realtime currently requires a gateway on the same origin.");
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  url.searchParams.set("model", model.trim());
  return url.toString();
}
export async function openRealtimeSocket(connection: PlaygroundConnection, model: string, signal: AbortSignal, origin = window.location.origin, dialect: RealtimeSettings["dialect"] = "current"): Promise<WebSocket> {
  const url = realtimeSocketURL(connection, model, origin);
  const reply = await connection.client.request<{ ticket: string; protocol: string; socket_path: string; expires_at: string }>(connection.path("/v1/realtime/browser-tickets"), {
    method: "POST", body: { model: model.trim(), origin, dialect }, signal, maximumResponseBytes: 4096,
  });
  if (signal.aborted) throw new DOMException("Connection cancelled", "AbortError");
  const expires = Date.parse(reply.expires_at);
  if (!/^[A-Za-z0-9_-]{43}$/.test(reply.ticket) || reply.protocol !== realtimeProtocol || reply.socket_path !== "/v1/realtime/browser" || !Number.isFinite(expires) || expires <= Date.now() || expires > Date.now() + 31000) throw new Error("Gateway returned an invalid or expired Realtime ticket.");
  return new WebSocket(url, [realtimeProtocol, `ai-gateway.realtime.ticket.${reply.ticket}`]);
}
export function decodeRealtimeEvent(data: unknown): Record<string, unknown> {
  if (typeof data !== "string" || new TextEncoder().encode(data).length > 1024 * 1024) throw new Error("Realtime event exceeds 1 MiB or is not text JSON.");
  let value: unknown;
  try { value = JSON.parse(data); } catch { throw new Error("Invalid Realtime JSON event."); }
  if (!value || typeof value !== "object" || Array.isArray(value) || typeof (value as Record<string, unknown>).type !== "string") throw new Error("Realtime event requires a type.");
  return value as Record<string, unknown>;
}
export function audioBytes(value: unknown): Uint8Array {
  if (typeof value !== "string" || !value.length || value.length % 4 !== 0 || !/^[A-Za-z0-9+/]*={0,2}$/.test(value) || value.length > 1024 * 1024) throw new Error("Invalid or oversized Realtime audio chunk.");
  const decoded = atob(value);
  const bytes = Uint8Array.from(decoded, (char) => char.charCodeAt(0));
  if (bytes.length % 2) throw new Error("Realtime PCM16 audio requires complete samples.");
  return bytes;
}
export function audioBase64(bytes: Uint8Array): string {
  let text = "";
  for (let offset = 0; offset < bytes.length; offset += 8192) text += String.fromCharCode(...bytes.subarray(offset, offset + 8192));
  return btoa(text);
}
export function pcmWAV(chunks: Uint8Array[]): Blob {
  const size = chunks.reduce((sum, chunk) => sum + chunk.length, 0);
  if (!size || size > maximumAudioBytes || chunks.some((chunk) => chunk.length % 2)) throw new Error("Audio output must contain complete PCM16 samples within 8 MiB.");
  const bytes = new Uint8Array(44 + size), view = new DataView(bytes.buffer);
  const text = (offset: number, value: string) => [...value].forEach((char, index) => view.setUint8(offset + index, char.charCodeAt(0)));
  text(0, "RIFF"); view.setUint32(4, 36 + size, true); text(8, "WAVE"); text(12, "fmt "); view.setUint32(16, 16, true);
  view.setUint16(20, 1, true); view.setUint16(22, 1, true); view.setUint32(24, 24000, true); view.setUint32(28, 48000, true); view.setUint16(32, 2, true); view.setUint16(34, 16, true); text(36, "data"); view.setUint32(40, size, true);
  let offset = 44; for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.length; }
  return new Blob([bytes], { type: "audio/wav" });
}
// Keep interpolation state between microphone chunks; AudioContext may ignore
// the requested 24 kHz rate and provide 44.1 or 48 kHz samples instead.
export class PCMResampler {
  private tail?: number;
  private position = 0;
  constructor(private readonly sourceRate: number) {
    if (!Number.isFinite(sourceRate) || sourceRate < 8000 || sourceRate > 192000) throw new Error("Unsupported microphone sample rate.");
  }
  convert(samples: Float32Array): Uint8Array {
    if (!samples.length) return new Uint8Array();
    const source = this.tail === undefined ? samples : Float32Array.from([this.tail, ...samples]);
    const output: number[] = [], step = this.sourceRate / 24000;
    while (this.position < source.length - 1) {
      const index = Math.floor(this.position), fraction = this.position - index;
      const sample = Math.max(-1, Math.min(1, source[index] * (1 - fraction) + source[index + 1] * fraction));
      output.push(Math.round(sample < 0 ? sample * 32768 : sample * 32767));
      this.position += step;
    }
    this.position -= source.length - 1; this.tail = source[source.length - 1];
    const bytes = new Uint8Array(output.length * 2), view = new DataView(bytes.buffer);
    output.forEach((sample, index) => view.setInt16(index * 2, sample, true));
    return bytes;
  }
}
export function realtimeEventSummary(event: Record<string, unknown>): string {
  // Audio bytes and credentials are never shown in diagnostic events.
  const summary = { type: event.type, response_id: event.response_id, item_id: event.item_id,
    ...(typeof event.delta === "string" ? { delta: /audio\.delta$/.test(String(event.type)) ? "[audio chunk]" : event.delta.slice(0, 1024) } : {}) };
  return JSON.stringify(summary).slice(0, 8192);
}
