class GatewayMicrophone extends AudioWorkletProcessor {
  constructor() {
    super();
    this.samples = new Float32Array(1024);
    this.count = 0;
    this.port.onmessage = (event) => {
      if (event.data === "flush") {
        if (this.count) this.port.postMessage(this.samples.slice(0, this.count));
        this.count = 0;
        this.port.postMessage("flushed");
      }
    };
  }
  process(inputs) {
    const channels = inputs[0];
    if (!channels?.length) return true;
    for (let index = 0; index < channels[0].length; index++) {
      let sample = 0;
      for (const channel of channels) sample += channel[index] || 0;
      this.samples[this.count++] = sample / channels.length;
      if (this.count === this.samples.length) {
        const samples = this.samples;
        this.port.postMessage(samples, [samples.buffer]);
        this.samples = new Float32Array(1024);
        this.count = 0;
      }
    }
    return true;
  }
}
registerProcessor("gateway-microphone", GatewayMicrophone);
