import type { ChipOption } from "./components/ChipMultiSelect";

export const modelCapabilityOptions: ChipOption[] = [
  { value: "chat", label: "Chat", description: "Chat Completions API" },
  { value: "bedrock_invoke", label: "Bedrock Invoke", description: "Anthropic Messages through Bedrock InvokeModel" },
  { value: "responses", label: "Responses", description: "Responses API" },
  { value: "interactions", label: "Interactions", description: "Native model interactions" },
  { value: "interaction_agents", label: "Interaction agents", description: "Native provider agent interactions" },
  { value: "interaction_environment_reuse", label: "Interaction environment reuse", description: "Reuse an owner-bound native agent environment" },
  { value: "gemini_safety_settings", label: "Gemini safety settings", description: "Native per-category content blocking thresholds" },
  { value: "gemini_code_execution", label: "Gemini code execution", description: "Native managed code execution tool" },
  { value: "gemini_audio_timestamp", label: "Vertex audio timestamps", description: "Timestamp-aware processing for audio inputs" },
  { value: "gemini_media_resolution", label: "Gemini media resolution", description: "Control input media token resolution" },
  { value: "gemini_media_processing", label: "Gemini video processing", description: "Static or agentic processing for video inputs" },
  { value: "gemini_search_time_range", label: "Gemini search time range", description: "Bound native Google Search results by time" },
  { value: "gemini_file_search", label: "Gemini file search", description: "Search explicitly authorized provider-managed stores" },
  { value: "gemini_computer_use", label: "Gemini computer use", description: "Generate client-executed browser, mobile, or desktop actions" },
  { value: "gemini_mcp", label: "Gemini MCP", description: "Use explicitly approved remote MCP servers" },
  { value: "url_context", label: "URL context", description: "Native retrieval of URLs supplied in the prompt" },
  { value: "google_maps", label: "Google Maps grounding", description: "Native location-aware grounding with source metadata" },
  { value: "background_responses", label: "Background responses", description: "Durable asynchronous Responses execution" },
  { value: "background_interactions", label: "Background interactions", description: "Durable asynchronous native Interactions execution" },
  { value: "embeddings", label: "Embeddings", description: "Vector embeddings" },
  { value: "rerank", label: "Rerank", description: "Document reranking" },
  { value: "moderation", label: "Moderation", description: "Text and image safety classification" },
  { value: "image_generation", label: "Image generation", description: "Generate images from prompts" },
  { value: "response_image_generation", label: "Responses image generation", description: "Run provider-hosted image generation from Responses" },
  { value: "response_computer", label: "Responses computer", description: "Continue owner-isolated computer interaction loops in Responses" },
  { value: "response_shell", label: "Responses shell", description: "Route validated shell execution loops to compatible Responses deployments" },
  { value: "response_apply_patch", label: "Responses apply patch", description: "Continue validated file-patch tool loops in Responses" },
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
  { value: "container", label: "Containers", description: "Create and manage provider containers" },
  { value: "container_files", label: "Container files", description: "Upload and manage files in provider containers" },
  { value: "container_network", label: "Container network", description: "Apply provider-enforced container egress policies" },
  { value: "cached_content", label: "Cached content", description: "Create and reuse durable provider-side Gemini context" },
  { value: "sandbox", label: "Sandbox", description: "Execute code in an ephemeral isolated runtime" },
  { value: "realtime", label: "Realtime", description: "Bidirectional low-latency sessions" },
  { value: "stream", label: "Stream", description: "Streaming responses" },
  { value: "tools", label: "Tools", description: "Function and tool calling" },
  { value: "custom_tools", label: "Custom tools", description: "Responses custom tools with text or grammar input" },
  { value: "structured_output", label: "Structured output", description: "Structured JSON output" },
  { value: "mcp", label: "MCP", description: "MCP tool calls" },
  { value: "code_interpreter", label: "Code interpreter", description: "Provider-managed code execution in Responses" },
  { value: "file_search", label: "File search", description: "Provider-managed file search in Responses" },
  { value: "vision", label: "Vision", description: "Image inputs" },
  { value: "web_search", label: "Web search", description: "Provider web search tools" },
  { value: "web_fetch", label: "Web fetch", description: "Provider retrieval of explicitly allowed web domains" },
  { value: "memory_tool", label: "Memory tool", description: "Provider-defined client memory operations" },
  { value: "bash_tool", label: "Bash tool", description: "Provider-defined client shell operations" },
  { value: "text_editor_tool", label: "Text editor tool", description: "Provider-defined client file editing operations" },
  { value: "computer_toolset", label: "Computer toolset", description: "Provider-defined client desktop interaction tools" },
  { value: "browser_toolset", label: "Browser toolset", description: "Provider-defined client browser interaction tools" },
  { value: "thinking", label: "Thinking", description: "Native adaptive or budgeted reasoning" },
  { value: "zero_output", label: "Zero output", description: "Native zero-token cache population requests" },
  { value: "inference_geo", label: "Inference geography", description: "Native US or global inference placement" },
  { value: "context_management", label: "Context management", description: "Native bounded context editing strategies" },
  { value: "tool_result_error", label: "Tool result errors", description: "Preserve failed native client tool results" },
  { value: "document_citations", label: "Document citations", description: "Native citations for inline documents" },
  { value: "document_metadata", label: "Document metadata", description: "Policy-visible title and context for inline documents" },
  { value: "document_text", label: "Text documents", description: "Native inline plain-text document blocks" },
  { value: "audio", label: "Audio output", description: "Generated audio in chat responses" },
  { value: "audio_input", label: "Audio input", description: "Inline audio input for native multimodal requests" },
  { value: "video_input", label: "Video input", description: "Bounded inline video input for native multimodal requests" },
  { value: "prompt_cache", label: "Prompt cache", description: "Explicit provider prompt caching" },
  { value: "assistant_prefill", label: "Assistant prefill", description: "Continue a final assistant prefix" },
  { value: "file_input", label: "File input", description: "Bounded inline PDF input" }
];

export function providerModelCapabilityOptions(capabilities?: string[]): ChipOption[] {
  if (!capabilities) return modelCapabilityOptions;
  const supported = new Set(capabilities);
  return modelCapabilityOptions.filter(({ value }) => supported.has(value));
}

export function defaultModelCapabilities(providerType: string): string[] {
  switch (providerType) {
    case "opensandbox":
      return ["sandbox"];
    case "voyage":
      return ["embeddings"];
    case "bedrock":
      return ["chat"];
    case "groq":
      return ["chat", "stream"];
    case "deepseek":
      return ["chat", "responses", "stream"];
    case "cerebras":
      return ["chat", "stream"];
    case "nvidia-nim":
      return ["chat", "completions", "responses", "stream"];
    case "together":
      return ["chat", "completions", "stream"];
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
