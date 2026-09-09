import { describe, expect, it } from "vitest";
import { modelCapabilityOptions } from "./modelCapabilities";

describe("modelCapabilityOptions", () => {
  it("exposes every configurable inference capability", () => {
    expect(modelCapabilityOptions.map(({ value }) => value)).toEqual([
      "chat",
      "responses",
      "embeddings",
      "rerank",
      "moderation",
      "image_generation",
      "image_edit",
      "image_variation",
      "audio_transcription",
      "audio_speech",
      "ocr",
      "search",
      "stream",
      "tools",
      "structured_output",
      "mcp",
      "vision",
      "web_search",
      "web_fetch",
      "audio",
      "prompt_cache",
      "assistant_prefill"
    ]);
  });
});
