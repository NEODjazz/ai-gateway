import { describe, expect, it } from "vitest";
import { defaultModelCapabilities, modelCapabilityOptions, providerModelCapabilityOptions } from "./modelCapabilities";

describe("modelCapabilityOptions", () => {
  it("exposes every configurable inference capability", () => {
    expect(modelCapabilityOptions.map(({ value }) => value)).toEqual([
      "chat",
      "bedrock_invoke",
      "responses",
      "interactions",
      "interaction_agents",
      "interaction_environment_reuse",
      "gemini_safety_settings",
      "background_responses",
      "background_interactions",
      "embeddings",
      "rerank",
      "moderation",
      "image_generation",
      "image_edit",
      "image_variation",
      "audio_transcription",
      "audio_translation",
      "audio_speech",
      "ocr",
      "search",
      "skills",
      "fine_tuning",
      "video",
      "video_remix",
      "video_extension",
      "container",
      "container_files",
      "container_network",
      "realtime",
      "stream",
      "tools",
      "structured_output",
      "mcp",
      "vision",
      "web_search",
      "web_fetch",
      "audio",
      "audio_input",
      "video_input",
      "prompt_cache",
      "assistant_prefill",
      "file_input"
    ]);
  });

  it("uses adapter-safe onboarding defaults", () => {
    expect(defaultModelCapabilities("voyage")).toEqual(["embeddings"]);
    expect(defaultModelCapabilities("bedrock")).toEqual(["chat"]);
    expect(defaultModelCapabilities("groq")).toEqual(["chat", "stream"]);
    expect(defaultModelCapabilities("deepseek")).toEqual(["chat", "responses", "stream"]);
    expect(defaultModelCapabilities("gemini")).toEqual(["chat", "interactions", "stream"]);
    expect(defaultModelCapabilities("xai")).toEqual(["chat", "responses", "embeddings", "stream"]);
    expect(defaultModelCapabilities("demo")).toEqual(["chat", "responses", "embeddings"]);
    expect(defaultModelCapabilities("ollama")).toEqual(["chat", "responses", "embeddings", "stream"]);
    expect(defaultModelCapabilities("anthropic")).toEqual(["chat", "responses", "stream"]);
    expect(defaultModelCapabilities("openrouter")).toEqual(["chat", "responses", "stream"]);
  });

  it("filters operations and features against the adapter profile", () => {
    const values = providerModelCapabilityOptions(["embeddings", "rerank"]).map(({ value }) => value);
    expect(values).toEqual(["embeddings", "rerank"]);
  });
});
