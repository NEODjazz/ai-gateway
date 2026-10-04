import { startRealtimeRecorder } from "./realtimeRecorder";
class Port {
  onmessage?: (event: MessageEvent<unknown>) => void;
  close = vi.fn();
  postMessage = vi.fn((value: string) => { if (value === "flush") this.onmessage?.({ data: "flushed" } as MessageEvent); });
}
class Node {
  static instances: Node[] = [];
  port = new Port(); connect = vi.fn(); disconnect = vi.fn();
  constructor() { Node.instances.push(this); }
}
class Context {
  static instances: Context[] = [];
  sampleRate = 48000; state = "running"; destination = {};
  source = { connect: vi.fn(), disconnect: vi.fn() };
  audioWorklet = { addModule: vi.fn(async (_url: string) => {}) };
  resume = vi.fn(async () => {}); close = vi.fn(async () => { this.state = "closed"; });
  createMediaStreamSource = vi.fn(() => this.source);
  constructor() { Context.instances.push(this); }
}
beforeEach(() => { Node.instances = []; Context.instances = []; vi.stubGlobal("AudioContext", Context); vi.stubGlobal("AudioWorkletNode", Node); });
afterEach(() => { vi.unstubAllGlobals(); Reflect.deleteProperty(navigator, "mediaDevices"); });
function media(getUserMedia: unknown) { Object.defineProperty(navigator, "mediaDevices", { configurable: true, value: { getUserMedia } }); }
describe("Realtime microphone lifecycle", () => {
  it("resamples worklet samples, flushes explicitly and closes every capture resource", async () => {
    const stop = vi.fn(), getUserMedia = vi.fn(async () => ({ getTracks: () => [{ stop }] })); media(getUserMedia);
    const samples = vi.fn(); const recorder = await startRealtimeRecorder(new AbortController().signal, samples);
    expect(getUserMedia).toHaveBeenCalledWith({ audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true } });
    Node.instances[0].port.onmessage?.({ data: new Float32Array(1024) } as MessageEvent);
    expect(samples.mock.calls[0][0]).toHaveLength(1024); // 48 kHz float -> 24 kHz PCM16.
    expect(Context.instances[0].audioWorklet.addModule.mock.calls[0][0]).toContain("realtimeRecorder.worklet.js");
    await recorder.stop(true); expect(Node.instances[0].port.postMessage).toHaveBeenCalledWith("flush");
    expect(stop).toHaveBeenCalled(); expect(Context.instances[0].close).toHaveBeenCalledOnce(); expect(Node.instances[0].disconnect).toHaveBeenCalledOnce(); expect(Node.instances[0].port.close).toHaveBeenCalledOnce();
  });
  it("closes a late permission grant immediately after cancellation without creating an AudioContext", async () => {
    let resolve!: (stream: unknown) => void; media(vi.fn(() => new Promise((done) => resolve = done)));
    const abort = new AbortController(), samples = vi.fn(); const recording = startRealtimeRecorder(abort.signal, samples); abort.abort(); const stop = vi.fn(); resolve({ getTracks: () => [{ stop }] });
    await expect(recording).rejects.toMatchObject({ name: "AbortError" }); expect(stop).toHaveBeenCalledOnce(); expect(Context.instances).toHaveLength(0); expect(samples).not.toHaveBeenCalled();
  });
  it("releases capture on abort and ignores stale worklet messages", async () => {
    const stop = vi.fn(); media(vi.fn(async () => ({ getTracks: () => [{ stop }] })));
    const abort = new AbortController(), samples = vi.fn(); await startRealtimeRecorder(abort.signal, samples);
    const late = Node.instances[0].port.onmessage; abort.abort(); late?.({ data: new Float32Array(1024) } as MessageEvent);
    expect(stop).toHaveBeenCalledOnce(); expect(Context.instances[0].close).toHaveBeenCalledOnce(); expect(samples).not.toHaveBeenCalled();
  });
  it("reports unsupported browser capture without asking for microphone access", async () => {
    const request = vi.fn(); media(request); vi.stubGlobal("AudioWorkletNode", undefined);
    await expect(startRealtimeRecorder(new AbortController().signal, vi.fn())).rejects.toThrow("AudioWorklet"); expect(request).not.toHaveBeenCalled();
  });
});
