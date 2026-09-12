import type { ChipOption } from "./components/ChipMultiSelect";

export const modelCapabilityOptions: ChipOption[] = [
  { value: "chat", label: "Chat", description: "Chat Completions API" },
  { value: "bedrock_invoke", label: "Bedrock Invoke", description: "Anthropic Messages through Bedrock InvokeModel" },
  { value: "responses", label: "Responses", description: "Responses API" },
  { value: "interactions", label: "Interactions", description: "Native model interactions" },
  { value: "interaction_agents", label: "Interaction agents", description: "Native provider agent interactions" },
  { value: "interaction_environment_reuse", label: "Interaction environment reuse", description: "Reuse an owner-bound native agent environment" },
  { value: "gemini_safety_settings", label: "Gemini safety settings", description: "Native per-category content blocking thresholds" },
  { value: "background_responses", label: "Background responses", description: "Durable asynchronous Responses execution" },
  { value: "background_interactions", label: "Background interactions", description: "Durable asynchronous native Interactions execution" },
  { value: "embeddings", label: "Embeddings", description: "Vector embeddings" },
  { value: "rerank", label: "Rerank", description: "Document reranking" },
  { value: "moderation", label: "Moderation", description: "Text and image safety classification" },
  { value: "image_generation", label: "Image generation", description: "Generate images from prompts" },
  { value: "image_edit", label: "Image editing", description: "Edit uploaded images from prompts" },
  { value: "image_variation", label: "Image variations", description: "Create variations of uploaded images" },
  { value: "audio_transcription", label: "Audio transcription", description: "Transcribe uploaded audio" },
  { value: "audio_translation", label: "Audio translation", description: "Translate uploaded audio to English" },
  { value: "audio_speech", label: "Text to speech", description: "Generate speech audio from text" },
  { value: "ocr", label: "OCR", description: "Extract structured text from documents and images" },
  { value: "search", label: "Search", description: "Execute standalone search requests" },
  { value: "skills", label: "Skills", description: "Manage provider-native skill packages" },
  { value: "fine_tuning", label: "Fine-tuning", description: "Train and manage custom model jobs" },
  { value: "video", label: "Video", description: "Generate and manage video jobs" },
  { value: "video_remix", label: "Video remix", description: "Create a remix from an existing video" },
  { value: "video_extension", label: "Video extension", description: "Extend an existing video" },
  { value: "realtime", label: "Realtime", description: "Bidirectional low-latency sessions" },
  { value: "stream", label: "Stream", description: "Streaming responses" },
  { value: "tools", label: "Tools", description: "Function and tool calling" },
  { value: "structured_output", label: "Structured output", description: "Structured JSON output" },
  { value: "mcp", label: "MCP", description: "MCP tool calls" },
  { value: "vision", label: "Vision", description: "Image inputs" },
  { value: "web_search", label: "Web search", description: "Provider web search tools" },
  { value: "web_fetch", label: "Web fetch", description: "Provider retrieval of explicitly allowed web domains" },
  { value: "audio", label: "Audio output", description: "Generated audio in chat responses" },
  { value: "audio_input", label: "Audio input", description: "Inline audio input for native multimodal requests" },
  { value: "prompt_cache", label: "Prompt cache", description: "Explicit provider prompt caching" },
  { value: "assistant_prefill", label: "Assistant prefill", description: "Continue a final assistant prefix" },
  { value: "file_input", label: "File input", description: "Bounded inline PDF input for Responses" }
];

export function providerModelCapabilityOptions(capabilities?: string[]): ChipOption[] {
  if (!capabilities) return modelCapabilityOptions;
  const supported = new Set(capabilities);
  return modelCapabilityOptions.filter(({ value }) => supported.has(value));
}

export function defaultModelCapabilities(providerType: string): string[] {
  switch (providerType) {
    case "voyage":
      return ["embeddings"];
    case "bedrock":
      return ["chat"];
    case "groq":
      return ["chat", "stream"];
    case "deepseek":
      return ["chat", "responses", "stream"];
    case "gemini":
      return ["chat", "interactions", "stream"];
    case "xai":
      return ["chat", "responses", "embeddings", "stream"];
    case "openrouter":
      return ["chat", "responses", "stream"];
    case "demo":
      return ["chat", "responses", "embeddings"];
    case "ollama":
      return ["chat", "responses", "embeddings", "stream"];
    case "anthropic":
      return ["chat", "responses", "stream"];
    default:
      return ["chat", "stream"];
  }
}
