import { APIClient } from "../../api/client";
import { playgroundConnection } from "./requests";
import { audioBase64, audioBytes, decodeRealtimeEvent, defaultRealtimeSettings, maximumAudioBytes, openRealtimeSocket, PCMResampler, pcmWAV, realtimeEventSummary, realtimeSession, realtimeSocketURL } from "./realtime";
import { realtimeCode } from "./realtimeCode";

const connection = () => playgroundConnection(new APIClient(() => "synthetic-key"), "session", "", "");
describe("Realtime transport and audio", () => {
  it.each(["current", "legacy"] as const)("binds the %s dialect in a bounded ticket without secrets in the URL", async (dialect) => {
    const response = { ticket: "a".repeat(43), protocol: "ai-gateway.realtime.v1", socket_path: "/v1/realtime/browser", expires_at: new Date(Date.now() + 20000).toISOString() };
    const fetch = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify(response)));
    const websocket = vi.fn(function (_url: string, _protocols: string[]) {}); vi.stubGlobal("WebSocket", websocket);
    try {
      await openRealtimeSocket(connection(), "model", new AbortController().signal, window.location.origin, dialect);
      expect(fetch.mock.calls[0][0]).toBe("/v1/realtime/browser-tickets");
      expect(new Headers(fetch.mock.calls[0][1]?.headers).get("Authorization")).toBe("Bearer synthetic-key");
      expect(JSON.parse(String(fetch.mock.calls[0][1]?.body))).toEqual({ model: "model", origin: window.location.origin, dialect });
      expect(websocket.mock.calls[0]).toEqual([realtimeSocketURL(connection(), "model", window.location.origin), ["ai-gateway.realtime.v1", `ai-gateway.realtime.ticket.${response.ticket}`]]);
      expect(websocket.mock.calls[0][0]).not.toContain(response.ticket);
    } finally { vi.unstubAllGlobals(); }
  });
  it("rejects cross-origin gateways before sending a credential and ignores a late cancelled ticket", async () => {
    const fetch = vi.spyOn(globalThis, "fetch");
    const meta = document.createElement("meta"); meta.name = "ai-gateway-playground-origins"; meta.content = '["https://other.test"]'; document.head.append(meta);
    const external = playgroundConnection(new APIClient(() => "session"), "custom", "test-key", "https://other.test/v1"); meta.remove();
    await expect(openRealtimeSocket(external, "model", new AbortController().signal)).rejects.toThrow("same origin"); expect(fetch).not.toHaveBeenCalled();
    const abort = new AbortController(); fetch.mockImplementation(async () => { abort.abort(); return new Response('{}'); });
    await expect(openRealtimeSocket(connection(), "model", abort.signal)).rejects.toMatchObject({ name: "AbortError" });
  });
  it.each([{ ticket: "invalid" }, { expires_at: "2020-01-01" }, { protocol: "secret" }, { socket_path: "/other" }])("rejects invalid ticket replies %j", async (patch) => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ ticket: "a".repeat(43), protocol: "ai-gateway.realtime.v1", socket_path: "/v1/realtime/browser", expires_at: new Date(Date.now() + 20000).toISOString(), ...patch })));
    await expect(openRealtimeSocket(connection(), "model", new AbortController().signal)).rejects.toThrow("invalid or expired");
  });
  it("preserves both session dialects and disables automatic voice turns", () => {
    expect(realtimeSession(defaultRealtimeSettings)).toEqual({ type: "session.update", session: { type: "realtime", instructions: "", max_output_tokens: 256, output_modalities: ["text"] } });
    expect(realtimeSession({ ...defaultRealtimeSettings, mode: "voice" })).toMatchObject({ session: { output_modalities: ["audio"], audio: { input: { format: { type: "audio/pcm", rate: 24000 }, turn_detection: null }, output: { voice: "alloy" } } } });
    expect(realtimeSession({ ...defaultRealtimeSettings, mode: "voice", dialect: "legacy" })).toMatchObject({ session: { modalities: ["text", "audio"], input_audio_format: "pcm16", output_audio_format: "pcm16", turn_detection: null } });
    for (const limit of ["0", "", "4097"]) expect(() => realtimeSession({ ...defaultRealtimeSettings, limit })).toThrow("Maximum response tokens");
  });
  it("resamples continuously across irregular chunks and clips PCM16 samples", () => {
    const input = Float32Array.from({ length: 2401 }, (_, index) => Math.sin(index / 11) * 1.2);
    const combined = new PCMResampler(44100).convert(input);
    const sampler = new PCMResampler(44100), chunks = [input.subarray(0, 101), input.subarray(101, 432), input.subarray(432)].map((chunk) => sampler.convert(chunk));
    expect(Uint8Array.from(chunks.flatMap((chunk) => [...chunk]))).toEqual(combined);
    const clipped = new PCMResampler(24000).convert(new Float32Array([-2, 2, 0]));
    expect(new DataView(clipped.buffer).getInt16(0, true)).toBe(-32768); expect(new DataView(clipped.buffer).getInt16(2, true)).toBe(32767);
  });
  it("creates a valid bounded WAV and rejects malformed PCM audio", async () => {
    const bytes = new Uint8Array([1, 0, 2, 0]); expect(audioBytes(audioBase64(bytes))).toEqual(bytes);
    const wav = pcmWAV([bytes]); expect(wav.type).toBe("audio/wav"); expect(wav.size).toBe(48);
    const result = await new Promise<ArrayBuffer>((resolve) => { const reader = new FileReader(); reader.onload = () => resolve(reader.result as ArrayBuffer); reader.readAsArrayBuffer(wav); });
    expect(new DataView(result).getUint32(24, true)).toBe(24000); expect(new DataView(result).getUint32(40, true)).toBe(4);
    expect(() => audioBytes("AA==")).toThrow("complete samples"); expect(() => audioBytes("not base64")).toThrow("Invalid");
    expect(() => pcmWAV([new Uint8Array(maximumAudioBytes + 2)])).toThrow("8 MiB");
  });
  it("bounds and sanitizes diagnostics without exposing audio or arbitrary credentials", () => {
    expect(() => decodeRealtimeEvent("x".repeat(1024 * 1024 + 1))).toThrow("1 MiB"); expect(() => decodeRealtimeEvent("{}")).toThrow("type");
    const event = decodeRealtimeEvent('{"type":"response.audio.delta","delta":"AAAA","credential":"secret"}');
    expect(realtimeEventSummary(event)).not.toContain("AAAA"); expect(realtimeEventSummary(event)).not.toContain("secret");
  });
  it.each(["javascript", "python"] as const)("exports %s authorization and one-time socket flow without actual credentials", (language) => {
    const code = realtimeCode(language, connection(), "model", { ...defaultRealtimeSettings, instructions: 'Quote " and newline\n' });
    expect(code).toContain("browser-tickets"); expect(code).toContain("ai-gateway.realtime.ticket."); expect(code).toContain("response.create"); expect(code).not.toContain("synthetic-key");
  });
});
