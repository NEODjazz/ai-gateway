package provider

// Feature capabilities are explicit because implementing the base chat or
// responses client does not prove that an adapter preserves optional request
// semantics.

func (OpenAICompatible) SupportsTools() bool            { return true }
func (OpenAICompatible) SupportsStructuredOutput() bool { return true }
func (OpenAICompatible) SupportsWebSearch() bool        { return true }
func (OpenAICompatible) SupportsChatAudio() bool        { return true }
func (OpenAICompatible) SupportsFileInput() bool        { return true }
func (p OpenAICompatible) SupportsAssistantPrefill() bool {
	return p.supportsMessagePrefix
}

func (Ollama) SupportsTools() bool            { return true }
func (Ollama) SupportsStructuredOutput() bool { return true }

func (Anthropic) SupportsTools() bool            { return true }
func (Anthropic) SupportsStructuredOutput() bool { return true }
func (Anthropic) SupportsWebSearch() bool        { return true }
func (Anthropic) SupportsPromptCache() bool      { return true }
func (Anthropic) SupportsAssistantPrefill() bool { return true }

func (Gemini) SupportsTools() bool            { return true }
func (Gemini) SupportsStructuredOutput() bool { return true }

func (Cohere) SupportsTools() bool            { return true }
func (Cohere) SupportsStructuredOutput() bool { return true }

// Mistral embeds the compatible adapter but rejects these optional request
// fields in its native parameter validator.
func (Mistral) SupportsMCP() bool         { return false }
func (Mistral) SupportsWebSearch() bool   { return false }
func (Mistral) SupportsChatAudio() bool   { return false }
func (Mistral) SupportsPromptCache() bool { return false }
