import type { ChipOption } from "./components/ChipMultiSelect";

export const modelCapabilityOptions: ChipOption[] = [
  { value: "chat", label: "Chat", description: "Chat Completions API" },
  { value: "responses", label: "Responses", description: "Responses API" },
  { value: "embeddings", label: "Embeddings", description: "Vector embeddings" },
  { value: "rerank", label: "Rerank", description: "Document reranking" },
  { value: "moderation", label: "Moderation", description: "Text and image safety classification" },
  { value: "image_generation", label: "Image generation", description: "Generate images from prompts" },
  { value: "image_edit", label: "Image editing", description: "Edit uploaded images from prompts" },
  { value: "image_variation", label: "Image variations", description: "Create variations of uploaded images" },
  { value: "audio_transcription", label: "Audio transcription", description: "Transcribe uploaded audio" },
  { value: "stream", label: "Stream", description: "Streaming responses" },
  { value: "tools", label: "Tools", description: "Function and tool calling" },
  { value: "structured_output", label: "Structured output", description: "Structured JSON output" },
  { value: "mcp", label: "MCP", description: "MCP tool calls" },
  { value: "vision", label: "Vision", description: "Image inputs" },
  { value: "web_search", label: "Web search", description: "Provider web search tools" },
  { value: "audio", label: "Audio", description: "Audio input and output" },
  { value: "prompt_cache", label: "Prompt cache", description: "Explicit provider prompt caching" },
  { value: "assistant_prefill", label: "Assistant prefill", description: "Continue a final assistant prefix" }
];
