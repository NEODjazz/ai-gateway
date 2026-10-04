describe("Microphone worklet", () => {
  it("mixes channels, transfers bounded chunks and flushes the last partial chunk", async () => {
    type Worklet = { port: { onmessage?: (event: { data: string }) => void; postMessage: ReturnType<typeof vi.fn> }; process: (inputs: Float32Array[][]) => boolean };
    let Constructor!: new () => Worklet;
    class Processor { port = { onmessage: undefined, postMessage: vi.fn() }; }
    vi.stubGlobal("AudioWorkletProcessor", Processor);
    vi.stubGlobal("registerProcessor", (name: string, value: new () => Worklet) => { expect(name).toBe("gateway-microphone"); Constructor = value; });
    try {
      vi.resetModules(); const file = "./realtimeRecorder.worklet.js"; await import(file);
      const worklet = new Constructor();
      expect(worklet.process([])).toBe(true);
      worklet.process([[new Float32Array(600).fill(1), new Float32Array(600).fill(-1)]]);
      expect(worklet.port.postMessage).not.toHaveBeenCalled();
      worklet.process([[new Float32Array(600).fill(0.5)]]);
      const first = worklet.port.postMessage.mock.calls[0][0] as Float32Array;
      expect(first).toHaveLength(1024); expect(first[0]).toBe(0); expect(first[600]).toBe(0.5);
      expect(worklet.port.postMessage.mock.calls[0][1]).toEqual([first.buffer]);
      worklet.port.onmessage?.({ data: "flush" });
      expect(worklet.port.postMessage.mock.calls[1][0]).toHaveLength(176);
      expect(worklet.port.postMessage.mock.calls[2][0]).toBe("flushed");
    } finally { vi.unstubAllGlobals(); }
  });
});
