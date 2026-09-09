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
      "stream",
      "tools",
      "structured_output",
      "mcp",
      "vision",
      "web_search",
      "audio",
      "prompt_cache",
      "assistant_prefill"
    ]);
  });
});
