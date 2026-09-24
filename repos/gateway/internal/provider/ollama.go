package provider

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ai-gateway-gateway/internal/openai"
)

type Ollama struct {
	baseURL        string
	upstreamStream bool
	client         *http.Client
}

type ollamaBearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t ollamaBearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.Header.Set("Authorization", "Bearer "+t.token)
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(cloned)
}

type ollamaChatRequest struct {
	Model       string                 `json:"model"`
	Messages    []ollamaRequestMessage `json:"messages"`
	Tools       []openai.Tool          `json:"tools,omitempty"`
	Format      any                    `json:"format,omitempty"`
	Options     ollamaOptions          `json:"options,omitempty"`
	Stream      bool                   `json:"stream"`
	Logprobs    *bool                  `json:"logprobs,omitempty"`
	TopLogprobs *int                   `json:"top_logprobs,omitempty"`
	Think       any                    `json:"think,omitempty"`
}

type ollamaOptions struct {
	NumPredict  *int     `json:"num_predict,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	TopK        *int     `json:"top_k,omitempty"`
	MinP        *float64 `json:"min_p,omitempty"`
	Stop        any      `json:"stop,omitempty"`
	Seed        *int64   `json:"seed,omitempty"`
}

type ollamaRequestMessage struct {
	Role       string                  `json:"role"`
	Content    string                  `json:"content"`
	Thinking   string                  `json:"thinking,omitempty"`
	Images     []string                `json:"images,omitempty"`
	ToolCalls  []ollamaRequestToolCall `json:"tool_calls,omitempty"`
	ToolCallID string                  `json:"tool_call_id,omitempty"`
}

type ollamaResponseMessage struct {
	Role      string            `json:"role"`
	Content   string            `json:"content"`
	Thinking  string            `json:"thinking"`
	ToolCalls []openai.ToolCall `json:"tool_calls"`
}

type ollamaRequestToolCall struct {
	ID       string                    `json:"id,omitempty"`
	Type     string                    `json:"type,omitempty"`
	Function ollamaRequestFunctionCall `json:"function"`
}

type ollamaRequestFunctionCall struct {
	Name      string `json:"name"`
	Arguments any    `json:"arguments"`
}

type ollamaChatResponse struct {
	Model                 string                `json:"model"`
	Message               ollamaResponseMessage `json:"message"`
	Done                  bool                  `json:"done"`
	PromptEvalCount       *int                  `json:"prompt_eval_count,omitempty"`
	PromptEvalCachedCount *int                  `json:"prompt_eval_cached_count,omitempty"`
	EvalCount             *int                  `json:"eval_count,omitempty"`
	DoneReason            string                `json:"done_reason"`
	TotalDuration         int64                 `json:"total_duration"`
	LoadDuration          int64                 `json:"load_duration"`
	PromptEvalDuration    int64                 `json:"prompt_eval_duration"`
	EvalDuration          int64                 `json:"eval_duration"`
	Logprobs              []ollamaLogprob       `json:"logprobs"`
}

type ollamaTokenLogprob struct {
	Token   string  `json:"token"`
	Logprob float64 `json:"logprob"`
	Bytes   []int   `json:"bytes"`
}

type ollamaLogprob struct {
	ollamaTokenLogprob
	TopLogprobs []ollamaTokenLogprob `json:"top_logprobs"`
}

type ollamaEmbeddingRequest struct {
	Model      string `json:"model"`
	Input      any    `json:"input"`
	Dimensions *int   `json:"dimensions,omitempty"`
	// Ollama otherwise silently truncates inputs that exceed the model context.
	Truncate bool `json:"truncate"`
}

type ollamaEmbeddingResponse struct {
	Model           string      `json:"model"`
	Embeddings      [][]float64 `json:"embeddings"`
	PromptEvalCount *int        `json:"prompt_eval_count"`
}

func NewOllama(baseURL string, upstreamStream bool) Ollama {
	return newOllamaWithToken(baseURL, "", upstreamStream)
}

func newOllamaWithToken(baseURL, token string, upstreamStream bool) Ollama {
	client := newProviderHTTPClient(180 * time.Second)
	if token != "" {
		client.Transport = ollamaBearerTransport{base: client.Transport, token: token}
	}
	return Ollama{
		baseURL:        normalizeOllamaBaseURL(baseURL),
		upstreamStream: upstreamStream,
		client:         client,
	}
}

func normalizeOllamaBaseURL(value string) string {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	parsed, err := url.Parse(value)
	if err != nil {
		return value
	}
	path := strings.TrimRight(parsed.Path, "/")
	if strings.HasSuffix(path, "/api") {
		parsed.Path = strings.TrimSuffix(path, "/api")
	} else if strings.HasSuffix(path, "/v1") {
		parsed.Path = strings.TrimSuffix(path, "/v1")
	} else {
		return value
	}
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/")
}

func (Ollama) SupportsVision() bool { return true }

func (p Ollama) Completions(ctx context.Context, request openai.CompletionRequest) (openai.CompletionResponse, error) {
	if err := p.ValidateCompletionParameters(request); err != nil {
		return openai.CompletionResponse{}, err
	}
	return p.completionAdapter().Completions(ctx, request)
}

func (p Ollama) StreamCompletions(ctx context.Context, request openai.CompletionRequest, write CompletionStreamWriter) (openai.CompletionResponse, error) {
	if err := p.ValidateCompletionParameters(request); err != nil {
		return openai.CompletionResponse{}, err
	}
	return p.completionAdapter().StreamCompletions(ctx, request, write)
}

func (p Ollama) completionAdapter() OpenAICompatible {
	return OpenAICompatible{baseURL: p.baseURL, upstreamStream: p.upstreamStream, errorProvider: "ollama", exactCompletionUsage: true, completionStreamUsage: true, client: p.client}
}

func (p Ollama) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if err := p.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	messages, err := ollamaMessages(request.Messages)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	body, err := json.Marshal(ollamaChatRequest{
		Model:    request.Model,
		Messages: messages,
		Tools:    request.Tools,
		Format:   ollamaResponseFormat(request.ResponseFormat),
		Options:  ollamaRequestOptions(request),
		Stream:   request.Stream && p.upstreamStream,
		Logprobs: request.Logprobs, TopLogprobs: request.TopLogprobs,
		Think: ollamaThink(request.ReasoningEffort),
	})
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openai.ChatCompletionResponse{}, responseStatusError("ollama", resp)
	}

	var ollamaResp ollamaChatResponse
	if err := decodeOllamaChatResponse(resp.Body, &ollamaResp); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if !ollamaResp.Done {
		return openai.ChatCompletionResponse{}, errors.New("Ollama chat response is not complete")
	}
	if ollamaResp.Message.Role != "" && ollamaResp.Message.Role != "assistant" {
		return openai.ChatCompletionResponse{}, errors.New("invalid Ollama chat response role")
	}
	message := ollamaResp.Message.openAI()
	message.Role = "assistant"
	if err := openai.ValidateChatReasoningContent(message.Role, message.ReasoningContent); err != nil {
		return openai.ChatCompletionResponse{}, fmt.Errorf("invalid Ollama chat reasoning content: %w", err)
	}
	normalizeOllamaToolCalls(&message)

	finishReason := ollamaFinishReason(ollamaResp.DoneReason, len(message.ToolCalls) > 0)
	logprobs, logprobText, err := ollamaChoiceLogprobs(ollamaResp.Logprobs)
	if err != nil || len(ollamaResp.Logprobs) > 0 && logprobText != openai.ContentText(message.Content) {
		return openai.ChatCompletionResponse{}, errors.New("invalid Ollama chat logprobs")
	}
	if request.Logprobs != nil && *request.Logprobs && len(ollamaResp.Logprobs) == 0 && openai.ContentText(message.Content) != "" {
		return openai.ChatCompletionResponse{}, errors.New("Ollama chat response omitted requested logprobs")
	}
	usage, err := ollamaChatUsage(ollamaResp.PromptEvalCount, ollamaResp.PromptEvalCachedCount, ollamaResp.EvalCount)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	var choiceLogprobs *openai.ChoiceLogprobs
	if len(ollamaResp.Logprobs) > 0 {
		choiceLogprobs = &logprobs
	}

	return openai.ChatCompletionResponse{
		ID:     "chatcmpl-" + rand.Text(),
		Object: "chat.completion",
		Model:  ollamaResp.Model,
		Choices: []openai.Choice{
			{
				Index:        0,
				Message:      message,
				FinishReason: finishReason,
				Logprobs:     choiceLogprobs,
			},
		},
		Usage: usage,
	}, nil
}

func (p Ollama) Embeddings(ctx context.Context, request openai.EmbeddingRequest) (openai.EmbeddingResponse, error) {
	if err := p.ValidateEmbeddingParameters(request); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	body, err := json.Marshal(ollamaEmbeddingRequest{Model: request.Model, Input: request.Input, Dimensions: request.Dimensions})
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openai.EmbeddingResponse{}, responseStatusError("ollama", resp)
	}
	var upstream ollamaEmbeddingResponse
	if err := decodeEmbeddingResponse(resp.Body, &upstream); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	if upstream.PromptEvalCount == nil || *upstream.PromptEvalCount < 0 {
		return openai.EmbeddingResponse{}, errors.New("invalid Ollama embedding usage")
	}
	tokens := *upstream.PromptEvalCount
	data := make([]openai.Embedding, len(upstream.Embeddings))
	for index, vector := range upstream.Embeddings {
		data[index] = openai.Embedding{Object: "embedding", Embedding: vector, Index: index}
	}
	if err := validateEmbeddingVectors(request, data); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	return openai.EmbeddingResponse{
		Object: "list", Data: data, Model: upstream.Model,
		UsageReported: true,
		Usage:         openai.Usage{PromptTokens: tokens, TotalTokens: tokens},
	}, nil
}

func (p Ollama) StreamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	if err := p.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if !p.upstreamStream {
		return openai.ChatCompletionResponse{}, ErrStreamingUnsupported
	}
	messages, err := ollamaMessages(request.Messages)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}

	body, err := json.Marshal(ollamaChatRequest{
		Model:    request.Model,
		Messages: messages,
		Tools:    request.Tools,
		Format:   ollamaResponseFormat(request.ResponseFormat),
		Options:  ollamaRequestOptions(request),
		Stream:   true,
		Logprobs: request.Logprobs, TopLogprobs: request.TopLogprobs,
		Think: ollamaThink(request.ReasoningEffort),
	})
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openai.ChatCompletionResponse{}, responseStatusError("ollama", resp)
	}

	response := openai.ChatCompletionResponse{
		ID:     "chatcmpl-" + rand.Text(),
		Object: "chat.completion",
		Model:  request.Model,
		Choices: []openai.Choice{
			{
				Index:   0,
				Message: openai.Message{Role: "assistant"},
			},
		},
	}

	decoder := json.NewDecoder(&responseStreamReader{source: resp.Body, remaining: maxResponseStreamBytes})
	sentRole := false
	for {
		var chunk ollamaChatResponse
		if err := decoder.Decode(&chunk); err != nil {
			if err == io.EOF {
				break
			}
			return openai.ChatCompletionResponse{}, err
		}
		var terminalUsage openai.Usage
		if chunk.Done {
			var usageErr error
			terminalUsage, usageErr = ollamaChatUsage(chunk.PromptEvalCount, chunk.PromptEvalCachedCount, chunk.EvalCount)
			if usageErr != nil {
				return openai.ChatCompletionResponse{}, usageErr
			}
		}
		if chunk.Model != "" {
			response.Model = chunk.Model
		}
		if chunk.Message.Role != "" && chunk.Message.Role != "assistant" {
			return openai.ChatCompletionResponse{}, errors.New("invalid Ollama chat response role")
		}
		message := chunk.Message.openAI()
		if message.Role != "" {
			response.Choices[0].Message.Role = message.Role
		}
		normalizeOllamaToolCalls(&message)
		reasoningContent := message.ReasoningContent
		if reasoningContent != "" {
			current := response.Choices[0].Message.ReasoningContent
			if len(current) > openai.MaxChatReasoningContentBytes-len(reasoningContent) {
				return openai.ChatCompletionResponse{}, errors.New("Ollama chat reasoning stream exceeds limit")
			}
			response.Choices[0].Message.ReasoningContent += reasoningContent
			role := ""
			if !sentRole {
				role = response.Choices[0].Message.Role
				if role == "" {
					role = "assistant"
				}
				sentRole = true
			}
			if err := write(openAIChatReasoningContentChunkPayload(response.ID, response.Model, role, reasoningContent)); err != nil {
				return openai.ChatCompletionResponse{}, err
			}
		}
		content := openai.ContentText(message.Content)
		if content != "" {
			logprobs, logprobText, err := ollamaChoiceLogprobs(chunk.Logprobs)
			if err != nil || len(chunk.Logprobs) > 0 && logprobText != content {
				return openai.ChatCompletionResponse{}, errors.New("invalid Ollama chat stream logprobs")
			}
			if request.Logprobs != nil && *request.Logprobs && len(chunk.Logprobs) == 0 {
				return openai.ChatCompletionResponse{}, errors.New("Ollama chat stream omitted requested logprobs")
			}
			response.Choices[0].Message.Content = openai.ContentText(response.Choices[0].Message.Content) + content
			role := ""
			if !sentRole {
				role = response.Choices[0].Message.Role
				if role == "" {
					role = "assistant"
				}
				sentRole = true
			}
			if len(chunk.Logprobs) > 0 {
				if response.Choices[0].Logprobs == nil {
					response.Choices[0].Logprobs = &openai.ChoiceLogprobs{}
				}
				response.Choices[0].Logprobs.Content = append(response.Choices[0].Logprobs.Content, logprobs.Content...)
				payload, err := chatCompletionLogprobChunkPayload(response.ID, response.Model, time.Now().Unix(), role, content, &logprobs)
				if err != nil {
					return openai.ChatCompletionResponse{}, err
				}
				if err := write(payload); err != nil {
					return openai.ChatCompletionResponse{}, err
				}
			} else if err := write(openAIChatCompletionChunkPayload(response.ID, response.Model, 0, role, content, nil)); err != nil {
				return openai.ChatCompletionResponse{}, err
			}
		}
		for _, call := range message.ToolCalls {
			toolIndex := len(response.Choices[0].Message.ToolCalls)
			response.Choices[0].Message.ToolCalls = append(response.Choices[0].Message.ToolCalls, call)
			role := ""
			if !sentRole {
				role = "assistant"
				sentRole = true
			}
			if err := write(openAIChatToolCallChunkPayload(response.ID, response.Model, toolIndex, call, role)); err != nil {
				return openai.ChatCompletionResponse{}, err
			}
		}
		if chunk.Done {
			finishReason := ollamaFinishReason(chunk.DoneReason, len(response.Choices[0].Message.ToolCalls) > 0)
			response.Choices[0].FinishReason = finishReason
			response.Usage = terminalUsage
			role := ""
			if !sentRole {
				role = "assistant"
			}
			if err := write(openAIChatCompletionChunkPayload(response.ID, response.Model, 0, role, "", &finishReason)); err != nil {
				return openai.ChatCompletionResponse{}, err
			}
			return response, nil
		}
	}
	return openai.ChatCompletionResponse{}, errors.New("Ollama chat stream ended without a terminal chunk")
}

func ollamaChatUsage(promptTokens, cachedTokens, completionTokens *int) (openai.Usage, error) {
	if promptTokens == nil || completionTokens == nil || *promptTokens < 0 || *completionTokens < 0 || *promptTokens > math.MaxInt-*completionTokens || cachedTokens != nil && (*cachedTokens < 0 || *cachedTokens > *promptTokens) {
		return openai.Usage{}, errors.New("invalid Ollama chat usage")
	}
	usage := openai.Usage{
		PromptTokens: *promptTokens, CompletionTokens: *completionTokens,
		TotalTokens: *promptTokens + *completionTokens,
	}
	if cachedTokens != nil {
		usage.PromptTokensDetails = &openai.PromptTokenDetails{CachedTokens: *cachedTokens}
	}
	return usage, nil
}

func ollamaFinishReason(reason string, hasToolCalls bool) string {
	if hasToolCalls && (reason == "" || reason == "stop") {
		return "tool_calls"
	}
	if reason == "" {
		return "stop"
	}
	return reason
}

func decodeOllamaChatResponse(reader io.Reader, response *ollamaChatResponse) error {
	payload, err := io.ReadAll(io.LimitReader(reader, maxChatCompletionResponseBytes+1))
	if err != nil {
		return err
	}
	if len(payload) > maxChatCompletionResponseBytes {
		return errors.New("Ollama chat response exceeds limit")
	}
	return json.Unmarshal(payload, response)
}

func ollamaThink(reasoningEffort string) any {
	switch reasoningEffort {
	case "none":
		return false
	case "minimal":
		return "low"
	case "xhigh":
		return "max"
	case "low", "medium", "high", "max":
		return reasoningEffort
	case "default":
		return nil
	default:
		return nil
	}
}

func openAIChatReasoningContentChunkPayload(id, model, role, reasoningContent string) string {
	delta := map[string]any{"reasoning_content": reasoningContent}
	if role != "" {
		delta["role"] = role
	}
	payload, err := json.Marshal(map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": time.Now().UTC().Unix(), "model": model,
		"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": nil}},
	})
	if err != nil {
		return "{}"
	}
	return string(payload)
}

func ollamaChoiceLogprobs(items []ollamaLogprob) (openai.ChoiceLogprobs, string, error) {
	if len(items) > 100_000 {
		return openai.ChoiceLogprobs{}, "", errors.New("too many Ollama logprobs")
	}
	converted := openai.ChoiceLogprobs{Content: make([]openai.TokenLogprob, 0, len(items))}
	var content strings.Builder
	for _, item := range items {
		token, err := ollamaOpenAITokenLogprob(item.ollamaTokenLogprob)
		if err != nil || len(item.TopLogprobs) > 20 {
			return openai.ChoiceLogprobs{}, "", errors.New("invalid Ollama logprobs")
		}
		for _, candidate := range item.TopLogprobs {
			top, err := ollamaOpenAITokenLogprob(candidate)
			if err != nil {
				return openai.ChoiceLogprobs{}, "", errors.New("invalid Ollama top logprobs")
			}
			token.TopLogprobs = append(token.TopLogprobs, openai.TopLogprob{Token: top.Token, Logprob: top.Logprob, Bytes: top.Bytes})
		}
		content.WriteString(token.Token)
		converted.Content = append(converted.Content, token)
	}
	return converted, content.String(), nil
}

func ollamaOpenAITokenLogprob(item ollamaTokenLogprob) (openai.TokenLogprob, error) {
	if !finiteProbability(item.Logprob) {
		return openai.TokenLogprob{}, errors.New("invalid log probability")
	}
	bytes := tokenBytes(item.Token)
	if len(item.Bytes) > 0 {
		if len(item.Bytes) != len(bytes) {
			return openai.TokenLogprob{}, errors.New("invalid token bytes")
		}
		for index := range bytes {
			if item.Bytes[index] != bytes[index] {
				return openai.TokenLogprob{}, errors.New("invalid token bytes")
			}
		}
		bytes = append([]int(nil), item.Bytes...)
	}
	return openai.TokenLogprob{Token: item.Token, Logprob: item.Logprob, Bytes: bytes, TopLogprobs: []openai.TopLogprob{}}, nil
}

func ollamaMessages(messages []openai.Message) ([]ollamaRequestMessage, error) {
	converted := make([]ollamaRequestMessage, len(messages))
	for index, message := range messages {
		converted[index] = ollamaRequestMessage{
			Role: message.Role, Content: openai.ContentText(message.Content), Thinking: message.ReasoningContent,
			ToolCalls: ollamaToolCalls(message.ToolCalls), ToolCallID: message.ToolCallID,
		}
		attachments, err := openai.ChatImageAttachments([]openai.Message{message})
		if err != nil {
			return nil, err
		}
		for _, attachment := range attachments {
			converted[index].Images = append(converted[index].Images, attachment.Data)
		}
	}
	return converted, nil
}

func (message ollamaResponseMessage) openAI() openai.Message {
	return openai.Message{Role: message.Role, Content: message.Content, ReasoningContent: message.Thinking, ToolCalls: message.ToolCalls}
}

func ollamaToolCalls(calls []openai.ToolCall) []ollamaRequestToolCall {
	converted := make([]ollamaRequestToolCall, len(calls))
	for index, call := range calls {
		arguments := any(call.Function.Arguments)
		if strings.TrimSpace(call.Function.Arguments) != "" {
			var decoded any
			if json.Unmarshal([]byte(call.Function.Arguments), &decoded) == nil {
				arguments = decoded
			}
		}
		converted[index] = ollamaRequestToolCall{
			ID: call.ID, Type: call.Type,
			Function: ollamaRequestFunctionCall{Name: call.Function.Name, Arguments: arguments},
		}
	}
	return converted
}

func normalizeOllamaToolCalls(message *openai.Message) {
	if message == nil {
		return
	}
	for index := range message.ToolCalls {
		if message.ToolCalls[index].ID == "" {
			message.ToolCalls[index].ID = "call_" + rand.Text()
		}
		if message.ToolCalls[index].Type == "" {
			message.ToolCalls[index].Type = "function"
		}
	}
}

func ollamaRequestOptions(request openai.ChatCompletionRequest) ollamaOptions {
	maxTokens := request.MaxTokens
	if maxTokens == nil {
		maxTokens = request.MaxCompletionTokens
	}
	return ollamaOptions{
		NumPredict: maxTokens, Temperature: request.Temperature, TopP: request.TopP,
		TopK: request.TopK, MinP: request.MinP, Stop: request.Stop, Seed: request.Seed,
	}
}

func ollamaResponseFormat(format *openai.ResponseFormat) any {
	if format == nil {
		return nil
	}
	if format.Type == "json_object" {
		return "json"
	}
	if format.Type == "json_schema" && format.JSONSchema != nil {
		return format.JSONSchema.Schema
	}
	return nil
}

func normalizeOllamaResponseText(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return value, nil
	}
	var text map[string]json.RawMessage
	if json.Unmarshal(encoded, &text) != nil {
		return value, nil
	}
	formatJSON, supplied := text["format"]
	if !supplied || len(formatJSON) == 0 || string(formatJSON) == "null" {
		return value, nil
	}
	var format map[string]json.RawMessage
	if json.Unmarshal(formatJSON, &format) != nil || format == nil {
		return value, nil
	}
	var formatType string
	if json.Unmarshal(format["type"], &formatType) != nil {
		return nil, &Error{Class: FailureClientRequest, Provider: "ollama", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "text.format.type", Err: errors.New("text format type is required")}
	}
	switch formatType {
	case "text", "json_object":
		if len(format) != 1 {
			return nil, &Error{Class: FailureClientRequest, Provider: "ollama", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: "text.format", Err: errors.New("this text format does not accept additional controls")}
		}
		if formatType == "text" {
			return value, nil
		}
		text["format"] = json.RawMessage(`{"type":"json_schema","name":"response","schema":{"type":"object"}}`)
		return text, nil
	case "json_schema":
		var schema map[string]json.RawMessage
		if json.Unmarshal(format["schema"], &schema) != nil || schema == nil {
			return nil, &Error{Class: FailureClientRequest, Provider: "ollama", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "text.format.schema", Err: errors.New("json_schema requires an object schema")}
		}
		for field := range format {
			if field != "type" && field != "name" && field != "schema" {
				return nil, &Error{Class: FailureClientRequest, Provider: "ollama", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: "text.format." + field, Err: fmt.Errorf("text format field %s is not supported", field)}
			}
		}
		return value, nil
	default:
		return nil, &Error{Class: FailureClientRequest, Provider: "ollama", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: "text.format.type", Err: fmt.Errorf("text format type %q is not supported", formatType)}
	}
}

func ollamaResponseReasoning(reasoning *openai.ResponseReasoning) *openai.ResponseReasoning {
	if reasoning == nil || reasoning.Effort == nil || *reasoning.Effort != "default" {
		return reasoning
	}
	// Ollama uses its model default when effort is omitted.
	copy := *reasoning
	copy.Effort = nil
	return &copy
}

func ollamaResponseToolChoiceNone(choice any) bool {
	value, ok := choice.(string)
	return ok && value == "none"
}

func ollamaResponseTools(request openai.ResponseRequest) []openai.ResponseTool {
	if ollamaResponseToolChoiceNone(request.ToolChoice) {
		return nil
	}
	return request.Tools
}

func (p Ollama) Responses(ctx context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	if err := p.ValidateResponseParameters(request); err != nil {
		return openai.ResponseResponse{}, err
	}
	text, err := normalizeOllamaResponseText(request.Text)
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	body, err := json.Marshal(openAICompatibleResponseRequest{
		Include: request.Include, Store: request.Store, Reasoning: ollamaResponseReasoning(request.Reasoning), Truncation: request.Truncation, TopLogprobs: request.TopLogprobs, Metadata: request.Metadata,
		Model: request.Model, Input: request.Input, Instructions: request.Instructions,
		Tools: ollamaResponseTools(request), ParallelToolCalls: request.ParallelToolCalls,
		Text: text, PreviousResponse: request.PreviousResponse, Stream: false,
		MaxOutputTokens: responseOutputTokenLimit(request),
		Temperature:     request.Temperature, TopP: request.TopP,
	})
	if err != nil {
		return openai.ResponseResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/responses", bytes.NewReader(body))
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openai.ResponseResponse{}, responseStatusError("ollama", resp)
	}

	response, err := decodeResponseJSON(resp.Body)
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	if err := validateExactResponseUsage(response, "Ollama"); err != nil {
		return openai.ResponseResponse{}, err
	}
	return response, nil
}

func (p Ollama) StreamResponses(ctx context.Context, request openai.ResponseRequest, write ResponseStreamWriter) (openai.ResponseResponse, error) {
	if err := p.ValidateResponseParameters(request); err != nil {
		return openai.ResponseResponse{}, err
	}
	if !p.upstreamStream {
		return openai.ResponseResponse{}, ErrStreamingUnsupported
	}
	text, err := normalizeOllamaResponseText(request.Text)
	if err != nil {
		return openai.ResponseResponse{}, err
	}

	body, err := json.Marshal(openAICompatibleResponseRequest{
		Include: request.Include, Store: request.Store, Reasoning: ollamaResponseReasoning(request.Reasoning), Truncation: request.Truncation, TopLogprobs: request.TopLogprobs, Metadata: request.Metadata,
		Model: request.Model, Input: request.Input, Instructions: request.Instructions,
		Tools: ollamaResponseTools(request), ParallelToolCalls: request.ParallelToolCalls,
		Text: text, PreviousResponse: request.PreviousResponse, Stream: true,
		MaxOutputTokens: responseOutputTokenLimit(request),
		Temperature:     request.Temperature, TopP: request.TopP,
	})
	if err != nil {
		return openai.ResponseResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/responses", bytes.NewReader(body))
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openai.ResponseResponse{}, responseStatusError("ollama", resp)
	}

	response, err := streamResponseData(resp.Body, request.Model, func(event, payload string) error {
		if event == "response.completed" || event == "response.incomplete" {
			if err := validateExactResponseTerminalUsage(payload, "Ollama"); err != nil {
				return err
			}
		}
		if write != nil {
			return write(event, payload)
		}
		return nil
	})
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	if err := validateExactResponseUsage(response, "Ollama"); err != nil {
		return openai.ResponseResponse{}, err
	}
	return response, nil
}

func responseText(response openai.ResponseResponse) string {
	var text strings.Builder
	for _, item := range response.Output {
		for _, content := range item.Content {
			if content.Type == "output_text" {
				text.WriteString(content.Text)
			}
		}
	}
	if text.Len() > 0 {
		return text.String()
	}
	return response.OutputText
}
