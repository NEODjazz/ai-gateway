import { PCMResampler } from "./realtime";

export type RealtimeRecorder = { stop: (flush: boolean) => Promise<void> };
export async function startRealtimeRecorder(signal: AbortSignal, onSamples: (bytes: Uint8Array) => void): Promise<RealtimeRecorder> {
  if (!navigator.mediaDevices?.getUserMedia || typeof AudioContext === "undefined" || typeof AudioWorkletNode === "undefined") throw new Error("Microphone capture requires a secure context with AudioWorklet support.");
  if (signal.aborted) throw new DOMException("Recording cancelled", "AbortError");
  const stream = await navigator.mediaDevices.getUserMedia({ audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true } });
  let context: AudioContext | undefined, source: MediaStreamAudioSourceNode | undefined, node: AudioWorkletNode | undefined;
  let released = false, stopping: Promise<void> | undefined, acknowledge: (() => void) | undefined;
  const release = () => {
    if (released) return;
    released = true; signal.removeEventListener("abort", release); acknowledge?.();
    stream.getTracks().forEach((track) => track.stop()); source?.disconnect(); node?.disconnect();
    if (node) { node.port.onmessage = null; node.port.close(); }
    if (context && context.state !== "closed") void context.close().catch(() => {});
  };
  signal.addEventListener("abort", release, { once: true });
  try {
    if (signal.aborted) throw new DOMException("Recording cancelled", "AbortError");
    context = new AudioContext({ sampleRate: 24000 });
    const resampler = new PCMResampler(context.sampleRate);
    await context.audioWorklet.addModule(new URL("./realtimeRecorder.worklet.js?no-inline", import.meta.url).href);
    if (signal.aborted || released) throw new DOMException("Recording cancelled", "AbortError");
    node = new AudioWorkletNode(context, "gateway-microphone");
    node.port.onmessage = (event: MessageEvent<unknown>) => {
      if (released) return;
      if (event.data === "flushed") { acknowledge?.(); return; }
      if (event.data instanceof Float32Array) {
        const bytes = resampler.convert(event.data);
        if (bytes.length) onSamples(bytes);
      }
    };
    source = context.createMediaStreamSource(stream); source.connect(node); node.connect(context.destination);
    await context.resume();
    if (signal.aborted || released) throw new DOMException("Recording cancelled", "AbortError");
    return { stop: (flush) => {
      if (stopping) return stopping;
      if (!flush || released) { release(); return Promise.resolve(); }
      source?.disconnect(); stream.getTracks().forEach((track) => track.stop());
      stopping = new Promise<void>((resolve, reject) => {
        const timeout = setTimeout(() => { acknowledge = undefined; release(); reject(new Error("Could not finish the microphone buffer. Recording was discarded.")); }, 2000);
        acknowledge = () => { clearTimeout(timeout); release(); resolve(); };
        node?.port.postMessage("flush");
      });
      return stopping;
    } };
  } catch (cause) { release(); throw cause; }
}
