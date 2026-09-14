package provider

// Feature capabilities are explicit because implementing the base chat or
// responses client does not prove that an adapter preserves optional request
// semantics.

func (OpenAICompatible) SupportsTools() bool               { return true }
func (OpenAICompatible) SupportsStructuredOutput() bool    { return true }
func (OpenAICompatible) SupportsWebSearch() bool           { return true }
func (OpenAICompatible) SupportsResponseWebSearch() bool   { return true }
func (OpenAICompatible) SupportsResponseCustomTools() bool { return true }
func (OpenAICompatible) SupportsResponseComputer() bool    { return true }
func (OpenAICompatible) SupportsResponseShell() bool       { return true }
func (OpenAICompatible) SupportsResponseImageGeneration() bool {
	return true
}
func (OpenAICompatible) SupportsChatAudio() bool { return true }
func (OpenAICompatible) SupportsFileInput() bool { return true }
func (p OpenAICompatible) SupportsAssistantPrefill() bool {
	return p.supportsMessagePrefix
}

func (Ollama) SupportsTools() bool            { return true }
func (Ollama) SupportsStructuredOutput() bool { return true }

func (Anthropic) SupportsTools() bool             { return true }
func (Anthropic) SupportsStructuredOutput() bool  { return true }
func (Anthropic) SupportsWebSearch() bool         { return true }
func (Anthropic) SupportsToolSearch() bool        { return true }
func (Anthropic) SupportsPromptCache() bool       { return true }
func (Anthropic) SupportsAssistantPrefill() bool  { return true }
func (Anthropic) SupportsMemoryTool() bool        { return true }
func (Anthropic) SupportsBashTool() bool          { return true }
func (Anthropic) SupportsTextEditorTool() bool    { return true }
func (Anthropic) SupportsComputerToolset() bool   { return true }
func (Anthropic) SupportsBrowserToolset() bool    { return true }
func (Anthropic) SupportsThinking() bool          { return true }
func (Anthropic) SupportsZeroOutput() bool        { return true }
func (Anthropic) SupportsInferenceGeo() bool      { return true }
func (Anthropic) SupportsContextManagement() bool { return true }
func (Anthropic) SupportsToolResultError() bool   { return true }
func (Anthropic) SupportsFileInput() bool         { return true }
func (Anthropic) SupportsDocumentCitations() bool { return true }
func (Anthropic) SupportsDocumentMetadata() bool  { return true }
func (Anthropic) SupportsTextDocuments() bool     { return true }

func (Gemini) SupportsTools() bool            { return true }
func (Gemini) SupportsStructuredOutput() bool { return true }

func (Cohere) SupportsTools() bool            { return true }
func (Cohere) SupportsStructuredOutput() bool { return true }

func (Bedrock) SupportsPromptCache() bool { return true }

// Mistral embeds the compatible adapter but rejects these optional request
// fields in its native parameter validator.
func (Mistral) SupportsMCP() bool                 { return false }
func (Mistral) SupportsWebSearch() bool           { return false }
func (Mistral) SupportsResponseWebSearch() bool   { return false }
func (Mistral) SupportsResponseCustomTools() bool { return false }
func (Mistral) SupportsResponseComputer() bool    { return false }
func (Mistral) SupportsResponseShell() bool       { return false }
func (Mistral) SupportsResponseImageGeneration() bool {
	return false
}
func (Mistral) SupportsChatAudio() bool   { return false }
func (Mistral) SupportsPromptCache() bool { return false }
