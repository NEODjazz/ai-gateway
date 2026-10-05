package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"

	"ai-gateway-gateway/internal/openai"
)

// Lemonade uses the OpenAI wire format without inheriting unrelated operations
// (storage, moderation, managed tools, jobs or generation Realtime).
type Lemonade struct {
	compatible OpenAICompatible
}

func NewLemonade(baseURL, apiKey string, stream bool) Lemonade {
	compatible := NewOpenAICompatible(baseURL, apiKey, stream)
	compatible.errorProvider = "lemonade"
	compatible.imageUnitUsage = true
	compatible.transcriptionDecoder = decodeLemonadeTranscription
	compatible.rerankDecoder = decodeLemonadeRerank
	compatible.validateReportedUsage = true
	return Lemonade{compatible: compatible}
}

func (Lemonade) SupportsResponses() bool        { return true }
func (Lemonade) SupportsTools() bool            { return true }
func (Lemonade) SupportsStructuredOutput() bool { return true }
func (Lemonade) SupportsVision() bool           { return true }
func (Lemonade) SupportsAudioInput() bool       { return true }
func (Lemonade) SupportsMCP() bool              { return false }
func (Lemonade) SupportsFileInput() bool        { return false }
func (Lemonade) SupportsRerank() bool           { return true }

func lemonadeInvalidParameter(param, message string) error {
	return &Error{Class: FailureClientRequest, Provider: "lemonade", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: param, Err: errors.New(message)}
}

// An allowlist also rejects future optional public fields until their native
// contract has been implemented. Marshaling uses the existing JSON tags and
// leaves caller-owned requests untouched.
func lemonadeRequestFields(request any, allowed ...string) error {
	payload, err := json.Marshal(request)
	if err != nil {
		return lemonadeInvalidParameter("", "request cannot be encoded")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return err
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		if name != "provider" && !containsString(allowed, name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) > 0 {
		return rejectParameters("lemonade", parameterCheck{names[0], true})
	}
	return nil
}

func (p Lemonade) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	if err := lemonadeRequestFields(request, "model", "messages", "stream", "stream_options", "max_tokens", "max_completion_tokens", "temperature", "top_p", "top_k", "stop", "tools", "tool_choice", "parallel_tool_calls", "response_format", "seed", "reasoning_effort", "repetition_penalty", "store"); err != nil {
		return err
	}
	if request.MaxTokens != nil && request.MaxCompletionTokens != nil {
		return lemonadeInvalidParameter("max_completion_tokens", "max_tokens and max_completion_tokens are mutually exclusive")
	}
	if request.RepetitionPenalty != nil && (*request.RepetitionPenalty < 1 || *request.RepetitionPenalty > 2) {
		return lemonadeInvalidParameter("repetition_penalty", "Lemonade repeat_penalty must be between 1 and 2")
	}
	if request.StreamOptions != nil && request.StreamOptions.IncludeObfuscation != nil {
		return rejectParameters("lemonade", parameterCheck{"stream_options.include_obfuscation", true})
	}
	for _, message := range request.Messages {
		if err := lemonadeRequestFields(message, "role", "content", "name", "tool_calls", "tool_call_id", "reasoning_content"); err != nil {
			return err
		}
	}
	for _, tool := range request.Tools {
		if tool.Type != "function" {
			return rejectParameters("lemonade", parameterCheck{"tools.type", true})
		}
		if err := lemonadeRequestFields(tool.Function, "name", "description", "parameters", "strict"); err != nil {
			return err
		}
		if err := validateLemonadeToolSchema(tool.Function.Parameters); err != nil {
			return err
		}
	}
	return p.compatible.ValidateChatParameters(request)
}

func (p Lemonade) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if err := p.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	decode := func(reader io.Reader, response *openai.ChatCompletionResponse) error {
		reader, err := lemonadeUsageReader(reader, false)
		if err != nil {
			return err
		}
		return decodeChatCompletionResponse(reader, response)
	}
	return p.compatible.chatCompletions(ctx, request, decode, newLemonadeChatStreamNormalizer())
}

func (p Lemonade) StreamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	if err := p.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return p.compatible.streamChatCompletions(ctx, request, write, newLemonadeChatStreamNormalizer())
}

func (p Lemonade) ValidateResponseParameters(request openai.ResponseRequest) error {
	if err := lemonadeRequestFields(request, "model", "input", "instructions", "stream", "max_output_tokens", "max_tokens", "temperature", "top_p", "tools", "tool_choice", "parallel_tool_calls", "text", "reasoning", "store"); err != nil {
		return err
	}
	if request.Store != nil && *request.Store {
		return rejectParameters("lemonade", parameterCheck{"store", true})
	}
	for _, tool := range request.Tools {
		if tool.Type != "function" || tool.OutputSchema != nil {
			return rejectParameters("lemonade", parameterCheck{"tools", true})
		}
		if err := lemonadeRequestFields(tool, "type", "name", "description", "parameters", "strict"); err != nil {
			return err
		}
		if err := validateLemonadeToolSchema(tool.Parameters); err != nil {
			return err
		}
	}
	return p.compatible.ValidateResponseParameters(request)
}

func (p Lemonade) Responses(ctx context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	if err := p.ValidateResponseParameters(request); err != nil {
		return openai.ResponseResponse{}, err
	}
	return p.compatible.Responses(ctx, request)
}

func (p Lemonade) StreamResponses(ctx context.Context, request openai.ResponseRequest, write ResponseStreamWriter) (openai.ResponseResponse, error) {
	if err := p.ValidateResponseParameters(request); err != nil {
		return openai.ResponseResponse{}, err
	}
	return p.compatible.StreamResponses(ctx, request, write)
}

func (p Lemonade) ValidateCompletionParameters(request openai.CompletionRequest) error {
	if err := lemonadeRequestFields(request, "model", "prompt", "max_tokens", "temperature", "top_p", "stop", "stream", "seed"); err != nil {
		return err
	}
	return p.compatible.ValidateCompletionParameters(request)
}

func (p Lemonade) Completions(ctx context.Context, request openai.CompletionRequest) (openai.CompletionResponse, error) {
	if err := p.ValidateCompletionParameters(request); err != nil {
		return openai.CompletionResponse{}, err
	}
	return p.compatible.Completions(ctx, request)
}

func (p Lemonade) StreamCompletions(ctx context.Context, request openai.CompletionRequest, write CompletionStreamWriter) (openai.CompletionResponse, error) {
	if err := p.ValidateCompletionParameters(request); err != nil {
		return openai.CompletionResponse{}, err
	}
	return p.compatible.StreamCompletions(ctx, request, write)
}

func (p Lemonade) ValidateEmbeddingParameters(request openai.EmbeddingRequest) error {
	if err := lemonadeRequestFields(request, "model", "input", "encoding_format"); err != nil {
		return err
	}
	input, err := openai.InspectEmbeddingInput(request.Input)
	if err != nil || input.Tokenized() {
		return lemonadeInvalidParameter("input", "Lemonade embeddings require text input")
	}
	return p.compatible.ValidateEmbeddingParameters(request)
}

func (p Lemonade) Embeddings(ctx context.Context, request openai.EmbeddingRequest) (openai.EmbeddingResponse, error) {
	if err := p.ValidateEmbeddingParameters(request); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	return p.compatible.Embeddings(ctx, request)
}

func (p Lemonade) ValidateRerankParameters(request openai.RerankRequest) error {
	if err := lemonadeRequestFields(request, "model", "query", "documents"); err != nil {
		return err
	}
	for _, document := range request.Documents {
		if _, ok := document.(string); !ok {
			return lemonadeInvalidParameter("documents", "Lemonade reranking requires text documents")
		}
	}
	return nil
}

func (p Lemonade) Rerank(ctx context.Context, request openai.RerankRequest) (openai.RerankResponse, error) {
	if err := p.ValidateRerankParameters(request); err != nil {
		return openai.RerankResponse{}, err
	}
	return p.compatible.Rerank(ctx, request)
}
