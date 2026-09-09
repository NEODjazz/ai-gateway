import type { ChipOption } from "./components/ChipMultiSelect";

export const modelCapabilityOptions: ChipOption[] = [
  { value: "chat", label: "Chat", description: "Chat Completions API" },
  { value: "responses", label: "Responses", description: "Responses API" },
  { value: "embeddings", label: "Embeddings", description: "Vector embeddings" },
  { value: "rerank", label: "Rerank", description: "Document reranking" },
  { value: "moderation", label: "Moderation", description: "Text and image safety classification" },
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
