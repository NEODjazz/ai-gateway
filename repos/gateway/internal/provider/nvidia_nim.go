package provider

import (
	"context"

	"ai-gateway-gateway/internal/openai"
)

// NVIDIANIM exposes the OpenAI-compatible inference operations documented by
// the NIM LLM runtime without advertising unrelated compatible API families.
type NVIDIANIM struct {
	compatible OpenAICompatible
	messages   Anthropic
}

func NewNVIDIANIM(baseURL, apiKey string, stream bool) NVIDIANIM {
	compatible := NewOpenAICompatible(baseURL, apiKey, stream)
	compatible.errorProvider = "nvidia-nim"
	return NVIDIANIM{compatible: compatible, messages: newBearerAnthropic(baseURL, apiKey, stream, "nvidia-nim")}
}

func (NVIDIANIM) SupportsResponses() bool        { return true }
func (NVIDIANIM) SupportsTools() bool            { return true }
func (NVIDIANIM) SupportsStructuredOutput() bool { return true }
func (NVIDIANIM) SupportsVision() bool           { return true }
func (NVIDIANIM) SupportsAudioInput() bool       { return true }
func (NVIDIANIM) SupportsVideoInput() bool       { return true }

func (n NVIDIANIM) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	return n.compatible.ValidateChatParameters(request)
}

func (n NVIDIANIM) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return n.compatible.ChatCompletions(ctx, request)
}

func (n NVIDIANIM) StreamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	return n.compatible.StreamChatCompletions(ctx, request, write)
}

func (n NVIDIANIM) Messages(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return n.messages.ChatCompletions(ctx, request)
}

func (n NVIDIANIM) StreamMessages(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	return n.messages.StreamChatCompletions(ctx, request, write)
}

func (n NVIDIANIM) CountTokens(ctx context.Context, request TokenCountRequest) (TokenCountResult, error) {
	return n.messages.CountTokens(ctx, request)
}

func (n NVIDIANIM) ValidateResponseParameters(request openai.ResponseRequest) error {
	return n.compatible.ValidateResponseParameters(request)
}

func (n NVIDIANIM) Responses(ctx context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	return n.compatible.Responses(ctx, request)
}

func (n NVIDIANIM) StreamResponses(ctx context.Context, request openai.ResponseRequest, write ResponseStreamWriter) (openai.ResponseResponse, error) {
	return n.compatible.StreamResponses(ctx, request, write)
}

func (n NVIDIANIM) ValidateCompletionParameters(request openai.CompletionRequest) error {
	return n.compatible.ValidateCompletionParameters(request)
}

func (n NVIDIANIM) Completions(ctx context.Context, request openai.CompletionRequest) (openai.CompletionResponse, error) {
	return n.compatible.Completions(ctx, request)
}

func (n NVIDIANIM) StreamCompletions(ctx context.Context, request openai.CompletionRequest, write CompletionStreamWriter) (openai.CompletionResponse, error) {
	return n.compatible.StreamCompletions(ctx, request, write)
}

func (n NVIDIANIM) ValidateEmbeddingParameters(request openai.EmbeddingRequest) error {
	return n.compatible.ValidateEmbeddingParameters(request)
}

func (n NVIDIANIM) Embeddings(ctx context.Context, request openai.EmbeddingRequest) (openai.EmbeddingResponse, error) {
	return n.compatible.Embeddings(ctx, request)
}
