package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ai-gateway-gateway/internal/openai"
)

type openAICompatibleChatRequest struct {
	openai.ChatGenerationOptions
	Model               string                 `json:"model"`
	Messages            []openai.Message       `json:"messages"`
	Tools               []openai.Tool          `json:"tools,omitempty"`
	ToolChoice          any                    `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool                  `json:"parallel_tool_calls,omitempty"`
	ResponseFormat      *openai.ResponseFormat `json:"response_format,omitempty"`
	Stream              bool                   `json:"stream,omitempty"`
	MaxTokens           *int                   `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int                   `json:"max_completion_tokens,omitempty"`
	Temperature         *float64               `json:"temperature,omitempty"`
	TopP                *float64               `json:"top_p,omitempty"`
	Stop                any                    `json:"stop,omitempty"`
	Seed                *int64                 `json:"seed,omitempty"`
}

type openAICompatibleResponseRequest struct {
	Reasoning         *openai.ResponseReasoning `json:"reasoning,omitempty"`
	Store             *bool                     `json:"store,omitempty"`
	Include           []string                  `json:"include,omitempty"`
	Model             string                    `json:"model"`
	Input             any                       `json:"input"`
	Instructions      string                    `json:"instructions,omitempty"`
	Tools             []openai.ResponseTool     `json:"tools,omitempty"`
	ToolChoice        any                       `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool                     `json:"parallel_tool_calls,omitempty"`
	Text              any                       `json:"text,omitempty"`
	PreviousResponse  string                    `json:"previous_response_id,omitempty"`
	Stream            bool                      `json:"stream,omitempty"`
	MaxOutputTokens   *int                      `json:"max_output_tokens,omitempty"`
	MaxTokens         *int                      `json:"max_tokens,omitempty"`
	Temperature       *float64                  `json:"temperature,omitempty"`
	TopP              *float64                  `json:"top_p,omitempty"`
}

type openAICompatibleEmbeddingRequest struct {
	Model          string `json:"model"`
	Input          any    `json:"input"`
	EncodingFormat string `json:"encoding_format,omitempty"`
	Dimensions     *int   `json:"dimensions,omitempty"`
	User           string `json:"user,omitempty"`
}

type openAICompatibleRerankRequest struct {
	Model           string   `json:"model"`
	Query           string   `json:"query"`
	Documents       []any    `json:"documents"`
	TopN            *int     `json:"top_n,omitempty"`
	RankFields      []string `json:"rank_fields,omitempty"`
	ReturnDocuments *bool    `json:"return_documents,omitempty"`
	MaxChunksPerDoc *int     `json:"max_chunks_per_doc,omitempty"`
	MaxTokensPerDoc *int     `json:"max_tokens_per_doc,omitempty"`
}

type OpenAICompatible struct {
	baseURL        string
	apiKey         string
	upstreamStream bool
	rerankPath     string
	client         *http.Client
}

func NewOpenAICompatible(baseURL string, apiKey string, upstreamStream bool) OpenAICompatible {
	return NewOpenAICompatibleWithRerankPath(baseURL, apiKey, upstreamStream, "")
}

func NewOpenAICompatibleWithRerankPath(baseURL string, apiKey string, upstreamStream bool, rerankPath string) OpenAICompatible {
	return OpenAICompatible{
		baseURL:        strings.TrimRight(baseURL, "/"),
		apiKey:         apiKey,
		upstreamStream: upstreamStream,
		rerankPath:     rerankPath,
		client:         newProviderHTTPClient(180 * time.Second),
	}
}

func (p OpenAICompatible) Rerank(ctx context.Context, request openai.RerankRequest) (openai.RerankResponse, error) {
	body, err := json.Marshal(openAICompatibleRerankRequest{
		Model: request.Model, Query: request.Query, Documents: request.Documents, TopN: request.TopN,
		RankFields: request.RankFields, ReturnDocuments: request.ReturnDocuments,
		MaxChunksPerDoc: request.MaxChunksPerDoc, MaxTokensPerDoc: request.MaxTokensPerDoc,
	})
	if err != nil {
		return openai.RerankResponse{}, err
	}
	url := providerURL(p.baseURL, "rerank")
	if p.rerankPath != "" {
		url = strings.TrimRight(p.baseURL, "/")
		if index := strings.Index(url, "://"); index >= 0 {
			if slash := strings.Index(url[index+3:], "/"); slash >= 0 {
				url = url[:index+3+slash]
			}
		}
		url += p.rerankPath
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return openai.RerankResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return openai.RerankResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openai.RerankResponse{}, responseStatusError("openai-compatible", resp)
	}
	var response openai.RerankResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&response); err != nil {
		return openai.RerankResponse{}, err
	}
	return response, nil
}

func (OpenAICompatible) SupportsMCP() bool    { return true }
func (OpenAICompatible) SupportsVision() bool { return true }

func (p OpenAICompatible) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if err := p.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	upstreamRequest := openAICompatibleChatRequest{
		ChatGenerationOptions: request.ChatGenerationOptions,
		Model:                 request.Model, Messages: request.Messages, Tools: request.Tools,
		ToolChoice: request.ToolChoice, ParallelToolCalls: request.ParallelToolCalls,
		ResponseFormat: request.ResponseFormat, Stream: request.Stream && p.upstreamStream,
		MaxTokens: request.MaxTokens, MaxCompletionTokens: request.MaxCompletionTokens,
		Temperature: request.Temperature, TopP: request.TopP,
		Stop: request.Stop, Seed: request.Seed,
	}
	resp, err := p.chatCompletionResponse(ctx, &upstreamRequest)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	defer resp.Body.Close()

	if request.Stream && p.upstreamStream {
		return decodeChatCompletionStream(resp.Body, request.Model)
	}

	var response openai.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return response, nil
}

func (p OpenAICompatible) Embeddings(ctx context.Context, request openai.EmbeddingRequest) (openai.EmbeddingResponse, error) {
	body, err := json.Marshal(openAICompatibleEmbeddingRequest{
		Model: request.Model, Input: request.Input, EncodingFormat: request.EncodingFormat,
		Dimensions: request.Dimensions, User: request.User,
	})
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(p.baseURL, "embeddings"), bytes.NewReader(body))
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openai.EmbeddingResponse{}, responseStatusError("openai-compatible", resp)
	}
	var upstream struct {
		openai.EmbeddingResponse
		Usage *struct {
			openai.Usage
			PromptTokens *int `json:"prompt_tokens"`
			TotalTokens  *int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := decodeEmbeddingResponse(resp.Body, &upstream); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	response := upstream.EmbeddingResponse
	if usage := upstream.Usage; usage != nil {
		if usage.PromptTokens == nil || usage.TotalTokens == nil || *usage.PromptTokens < 0 || *usage.TotalTokens < *usage.PromptTokens || usage.CompletionTokens != 0 {
			return openai.EmbeddingResponse{}, errors.New("invalid embedding usage")
		}
		response.Usage = usage.Usage
		response.Usage.PromptTokens = *usage.PromptTokens
		response.Usage.TotalTokens = *usage.TotalTokens
		response.UsageReported = true
	}
	if err := validateEmbeddingVectors(request, response.Data); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	return response, nil
}

func (p OpenAICompatible) StreamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	if err := p.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if !p.upstreamStream {
		return openai.ChatCompletionResponse{}, ErrStreamingUnsupported
	}

	upstreamRequest := openAICompatibleChatRequest{
		ChatGenerationOptions: request.ChatGenerationOptions,
		Model:                 request.Model, Messages: request.Messages, Tools: request.Tools,
		ToolChoice: request.ToolChoice, ParallelToolCalls: request.ParallelToolCalls,
		ResponseFormat: request.ResponseFormat, Stream: true,
		MaxTokens: request.MaxTokens, MaxCompletionTokens: request.MaxCompletionTokens,
		Temperature: request.Temperature, TopP: request.TopP,
		Stop: request.Stop, Seed: request.Seed,
	}
	resp, err := p.chatCompletionResponse(ctx, &upstreamRequest)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	defer resp.Body.Close()

	return streamChatCompletionData(resp.Body, request.Model, write)
}

func (p OpenAICompatible) chatCompletionResponse(ctx context.Context, request *openAICompatibleChatRequest) (*http.Response, error) {
	for attempt := 0; attempt < 2; attempt++ {
		body, err := json.Marshal(request)
		if err != nil {
			return nil, err
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(p.baseURL, "chat/completions"), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if p.apiKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
		}
		response, err := p.client.Do(httpReq)
		if err != nil {
			return nil, err
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return response, nil
		}
		providerErr := responseStatusError("openai-compatible", response)
		_ = response.Body.Close()
		if attempt == 0 && useMaxCompletionTokens(request, providerErr) {
			continue
		}
		return nil, providerErr
	}
	return nil, errors.New("openai-compatible chat compatibility retry exhausted")
}

func useMaxCompletionTokens(request *openAICompatibleChatRequest, err error) bool {
	if request.MaxTokens == nil || request.MaxCompletionTokens != nil {
		return false
	}
	var providerErr *Error
	if !errors.As(err, &providerErr) || providerErr.UpstreamCode != "unsupported_parameter" || providerErr.Param != "max_tokens" {
		return false
	}
	request.MaxCompletionTokens = request.MaxTokens
	request.MaxTokens = nil
	return true
}

func (p OpenAICompatible) Responses(ctx context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	body, err := json.Marshal(openAICompatibleResponseRequest{
		Include: request.Include, Store: request.Store, Reasoning: request.Reasoning,
		Model: request.Model, Input: request.Input, Instructions: request.Instructions,
		Tools: request.Tools, ToolChoice: request.ToolChoice, ParallelToolCalls: request.ParallelToolCalls,
		Text: request.Text, PreviousResponse: request.PreviousResponse, Stream: false,
		MaxOutputTokens: request.MaxOutputTokens, MaxTokens: request.MaxTokens,
		Temperature: request.Temperature, TopP: request.TopP,
	})
	if err != nil {
		return openai.ResponseResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(p.baseURL, "responses"), bytes.NewReader(body))
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openai.ResponseResponse{}, responseStatusError("openai-compatible", resp)
	}

	return decodeResponseJSON(resp.Body)
}

func (p OpenAICompatible) StreamResponses(ctx context.Context, request openai.ResponseRequest, write ResponseStreamWriter) (openai.ResponseResponse, error) {
	if !p.upstreamStream {
		return openai.ResponseResponse{}, ErrStreamingUnsupported
	}

	body, err := json.Marshal(openAICompatibleResponseRequest{
		Include: request.Include, Store: request.Store, Reasoning: request.Reasoning,
		Model: request.Model, Input: request.Input, Instructions: request.Instructions,
		Tools: request.Tools, ToolChoice: request.ToolChoice, ParallelToolCalls: request.ParallelToolCalls,
		Text: request.Text, PreviousResponse: request.PreviousResponse, Stream: true,
		MaxOutputTokens: request.MaxOutputTokens, MaxTokens: request.MaxTokens,
		Temperature: request.Temperature, TopP: request.TopP,
	})
	if err != nil {
		return openai.ResponseResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(p.baseURL, "responses"), bytes.NewReader(body))
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openai.ResponseResponse{}, responseStatusError("openai-compatible", resp)
	}

	return streamResponseData(resp.Body, request.Model, write)
}

func providerURL(baseURL string, path string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL + "/" + strings.TrimLeft(path, "/")
	}
	return baseURL + "/v1/" + strings.TrimLeft(path, "/")
}

func openAIChatCompletionChunkPayload(id string, model string, index int, role string, content string, finishReason *string, stopSequence ...*string) string {
	delta := map[string]any{}
	if role != "" {
		delta["role"] = role
	}
	if content != "" {
		delta["content"] = content
	}
	choice := map[string]any{"index": index, "delta": delta, "finish_reason": finishReason}
	if len(stopSequence) > 0 && stopSequence[0] != nil {
		choice["stop_sequence"] = stopSequence[0]
	}
	payload, err := json.Marshal(map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().UTC().Unix(),
		"model":   model,
		"choices": []map[string]any{choice},
	})
	if err != nil {
		return "{}"
	}
	return string(payload)
}

func decodeChatCompletionStream(body io.Reader, fallbackModel string) (openai.ChatCompletionResponse, error) {
	return streamChatCompletionData(body, fallbackModel, nil)
}

const maxChatStreamChoices = 128
const maxChatStreamToolCalls = 128

func streamChatCompletionData(body io.Reader, fallbackModel string, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	response := openai.ChatCompletionResponse{
		Object: "chat.completion",
		Model:  fallbackModel,
		Choices: []openai.Choice{
			{
				Index:        0,
				Message:      openai.Message{Role: "assistant"},
				FinishReason: "stop",
			},
		},
	}
	err := scanSSEData(body, func(payload string) error {
		if payload == "[DONE]" {
			return io.EOF
		}
		var chunk struct {
			ID      string        `json:"id"`
			Model   string        `json:"model"`
			Usage   *openai.Usage `json:"usage"`
			Choices []struct {
				Index int `json:"index"`
				Delta struct {
					Role      string            `json:"role"`
					Content   string            `json:"content"`
					ToolCalls []openai.ToolCall `json:"tool_calls,omitempty"`
				} `json:"delta"`
				FinishReason *string                `json:"finish_reason"`
				StopSequence *string                `json:"stop_sequence"`
				Logprobs     *openai.ChoiceLogprobs `json:"logprobs"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return err
		}
		if chunk.ID != "" {
			response.ID = chunk.ID
		}
		if chunk.Model != "" {
			response.Model = chunk.Model
		}
		if chunk.Usage != nil {
			response.Usage = *chunk.Usage
		}
		for _, choice := range chunk.Choices {
			if choice.Index < 0 || choice.Index >= maxChatStreamChoices {
				return fmt.Errorf("invalid upstream choice index")
			}
			for len(response.Choices) <= choice.Index {
				response.Choices = append(response.Choices, openai.Choice{Index: len(response.Choices), Message: openai.Message{Role: "assistant"}})
			}
			current := &response.Choices[choice.Index]
			current.Index = choice.Index
			if choice.Logprobs != nil {
				if current.Logprobs == nil {
					current.Logprobs = &openai.ChoiceLogprobs{}
				}
				current.Logprobs.Content = append(current.Logprobs.Content, choice.Logprobs.Content...)
				current.Logprobs.Refusal = append(current.Logprobs.Refusal, choice.Logprobs.Refusal...)
			}
			if choice.Delta.Role != "" {
				current.Message.Role = choice.Delta.Role
			}
			if choice.Delta.Content != "" {
				current.Message.Content = openai.ContentText(current.Message.Content) + choice.Delta.Content
			}
			if err := mergeToolCallDeltas(&current.Message.ToolCalls, choice.Delta.ToolCalls); err != nil {
				return err
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				current.FinishReason = *choice.FinishReason
			}
			if choice.StopSequence != nil {
				current.StopSequence = choice.StopSequence
			}
		}
		if write != nil {
			return write(payload)
		}
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return openai.ChatCompletionResponse{}, err
	}
	return response, nil
}

func mergeToolCallDeltas(target *[]openai.ToolCall, deltas []openai.ToolCall) error {
	for order, delta := range deltas {
		index := order
		if delta.Index != nil {
			index = *delta.Index
		}
		if index < 0 || index >= maxChatStreamToolCalls {
			return fmt.Errorf("invalid upstream tool call index")
		}
		for len(*target) <= index {
			*target = append(*target, openai.ToolCall{Type: "function"})
		}
		current := &(*target)[index]
		if delta.ExtraContent != nil {
			current.ExtraContent = delta.ExtraContent
		}
		if delta.ID != "" {
			current.ID = delta.ID
		}
		if delta.Type != "" {
			current.Type = delta.Type
		}
		if delta.Function.Name != "" {
			current.Function.Name += delta.Function.Name
		}
		current.Function.Arguments += delta.Function.Arguments
	}
	return nil
}

func decodeResponseStream(body io.Reader, fallbackModel string) (openai.ResponseResponse, error) {
	return streamResponseData(body, fallbackModel, nil)
}

func streamResponseData(body io.Reader, fallbackModel string, write ResponseStreamWriter) (openai.ResponseResponse, error) {
	response := openai.ResponseResponse{
		Object: "response",
		Model:  fallbackModel,
		Status: "completed",
		Output: []openai.ResponseOutputItem{
			{
				Type:   "message",
				Status: "completed",
				Role:   "assistant",
				Content: []openai.ResponseOutputContent{
					{Type: "output_text"},
				},
			},
		},
	}
	terminal := false
	err := scanSSEEvents(&responseStreamReader{source: body, remaining: maxResponseStreamBytes}, func(event string, payload string) error {
		if payload == "[DONE]" {
			if !terminal {
				return io.ErrUnexpectedEOF
			}
			return io.EOF
		}
		var decoded map[string]any
		decoder := json.NewDecoder(strings.NewReader(payload))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded); err != nil {
			return err
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			return errors.New("invalid trailing data in Responses SSE event")
		}
		if event == "" {
			event = eventName(decoded)
		}
		outputIndex, err := responseOutputIndex(decoded)
		if err != nil {
			return err
		}
		if id, ok := decoded["response_id"].(string); ok && response.ID == "" {
			response.ID = id
		}
		if event == "response.function_call_arguments.delta" || event == "response.function_call_arguments.done" {
			item := ensureResponseOutputItem(&response, outputIndex)
			item.Type = "function_call"
			item.Role, item.Content = "", nil
			if id, ok := decoded["item_id"].(string); ok {
				item.ID = id
			}
			if event == "response.function_call_arguments.delta" {
				if delta, ok := decoded["delta"].(string); ok {
					item.Arguments += delta
				}
			} else if arguments, ok := decoded["arguments"].(string); ok {
				item.Arguments = arguments
			}
		}
		if event == "response.refusal.delta" || event == "response.refusal.done" ||
			event == "response.output_text.delta" || event == "response.output_text.done" {
			contentIndex, err := boundedResponseStreamIndex(decoded, "content_index", maxResponseStreamContentParts)
			if err != nil {
				return err
			}
			item := ensureResponseOutputItem(&response, outputIndex)
			item.Type, item.Role = "message", "assistant"
			if id, ok := decoded["item_id"].(string); ok {
				item.ID = id
			}
			for len(item.Content) <= contentIndex {
				item.Content = append(item.Content, openai.ResponseOutputContent{})
			}
			part := &item.Content[contentIndex]
			switch event {
			case "response.output_text.delta", "response.output_text.done":
				part.Type, part.Refusal = "output_text", ""
				if event == "response.output_text.delta" {
					if delta, ok := decoded["delta"].(string); ok {
						part.Text += delta
					}
				} else if text, ok := decoded["text"].(string); ok {
					part.Text = text
				}
			case "response.refusal.delta", "response.refusal.done":
				part.Type, part.Text = "refusal", ""
				if event == "response.refusal.delta" {
					if delta, ok := decoded["delta"].(string); ok {
						part.Refusal += delta
					}
				} else if refusal, ok := decoded["refusal"].(string); ok {
					part.Refusal = refusal
				}
			}
		}
		if event == "response.content_part.added" || event == "response.content_part.done" {
			contentIndex, err := boundedResponseStreamIndex(decoded, "content_index", maxResponseStreamContentParts)
			if err != nil {
				return err
			}
			part, ok := decoded["part"].(map[string]any)
			if !ok {
				return errors.New("Responses content event is missing its part")
			}
			payload, err := json.Marshal(part)
			if err != nil {
				return err
			}
			var snapshot openai.ResponseOutputContent
			if err := json.Unmarshal(payload, &snapshot); err != nil {
				return err
			}
			item := ensureResponseOutputItem(&response, outputIndex)
			item.Type, item.Role = "message", "assistant"
			if id, ok := decoded["item_id"].(string); ok {
				item.ID = id
			}
			for len(item.Content) <= contentIndex {
				item.Content = append(item.Content, openai.ResponseOutputContent{})
			}
			item.Content[contentIndex] = snapshot
			response.OutputText = ""
		}
		if err := applyResponseSummaryEvent(&response, outputIndex, event, decoded); err != nil {
			return err
		}
		if event == "response.output_text.annotation.added" {
			contentIndex, err := boundedResponseStreamIndex(decoded, "content_index", maxResponseStreamContentParts)
			if err != nil {
				return err
			}
			annotationIndex, err := boundedResponseStreamIndex(decoded, "annotation_index", maxResponseStreamContentParts)
			if err != nil {
				return err
			}
			annotation, present := decoded["annotation"]
			if !present {
				return errors.New("Responses annotation event is missing its annotation")
			}
			if annotation != nil {
				if _, ok := annotation.(map[string]any); !ok {
					return errors.New("invalid Responses annotation")
				}
			}
			encoded, err := json.Marshal(annotation)
			if err != nil {
				return err
			}
			item := ensureResponseOutputItem(&response, outputIndex)
			item.Type, item.Role = "message", "assistant"
			if id, ok := decoded["item_id"].(string); ok {
				item.ID = id
			}
			for len(item.Content) <= contentIndex {
				item.Content = append(item.Content, openai.ResponseOutputContent{})
			}
			part := &item.Content[contentIndex]
			part.Type, part.Refusal = "output_text", ""
			for len(part.Annotations) <= annotationIndex {
				part.Annotations = append(part.Annotations, json.RawMessage("null"))
			}
			part.Annotations[annotationIndex] = encoded
		}
		if itemValue, ok := decoded["item"].(map[string]any); ok {
			marshaled, err := json.Marshal(itemValue)
			if err != nil {
				return err
			}
			var snapshot openai.ResponseOutputItem
			if err := json.Unmarshal(marshaled, &snapshot); err != nil {
				return err
			}
			*ensureResponseOutputItem(&response, outputIndex) = snapshot
			response.OutputText = ""
		}
		if typed, ok := decoded["response"].(map[string]any); ok {
			marshaled, err := json.Marshal(typed)
			if err != nil {
				return err
			}
			// An output snapshot replaces text assembled from earlier events.
			if _, present := typed["output"]; present {
				response.OutputText = ""
			}
			if err := json.Unmarshal(marshaled, &response); err != nil {
				return err
			}
			if err := recordResponseInputUsage(marshaled, &response); err != nil {
				return err
			}
			if err := validateResponseUsage(response.Usage); err != nil {
				return err
			}
			response.OutputText = responseText(response)
		}
		switch event {
		case "response.completed", "response.incomplete", "response.failed":
			outcome, ok := decoded["response"].(map[string]any)
			if !ok {
				return errors.New("Responses terminal event is missing its response")
			}
			status := strings.TrimPrefix(event, "response.")
			if reported, exists := outcome["status"]; exists && reported != status {
				return errors.New("Responses terminal event has contradictory status")
			}
			response.Status = status
			terminal = true
		}
		if write != nil {
			if err := write(event, payload); err != nil {
				return err
			}
		}
		if terminal {
			return io.EOF
		}
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return openai.ResponseResponse{}, err
	}
	if !terminal {
		return openai.ResponseResponse{}, io.ErrUnexpectedEOF
	}
	response.OutputText = responseText(response)
	return response, nil
}

const maxResponseStreamOutputItems = 1024
const maxResponseStreamContentParts = 128

func responseOutputIndex(decoded map[string]any) (int, error) {
	return boundedResponseStreamIndex(decoded, "output_index", maxResponseStreamOutputItems)
}

func boundedResponseStreamIndex(decoded map[string]any, field string, limit int) (int, error) {
	raw, present := decoded[field]
	if !present {
		return 0, nil
	}
	value, ok := raw.(float64)
	if number, isNumber := raw.(json.Number); isNumber {
		var err error
		value, err = number.Float64()
		ok = err == nil
	}
	// Check the small range before converting to int, including on 32-bit builds.
	if !ok || !(value >= 0 && value < float64(limit)) || value != float64(int(value)) {
		return 0, fmt.Errorf("invalid upstream response %s", field)
	}
	return int(value), nil
}

func ensureResponseOutputItem(response *openai.ResponseResponse, index int) *openai.ResponseOutputItem {
	for len(response.Output) <= index {
		response.Output = append(response.Output, openai.ResponseOutputItem{})
	}
	return &response.Output[index]
}

func sseData(body io.Reader) []string {
	var payloads []string
	_ = scanSSEData(body, func(payload string) error {
		payloads = append(payloads, payload)
		return nil
	})
	return payloads
}

func scanSSEData(body io.Reader, handle func(payload string) error) error {
	return scanSSEEvents(body, func(_ string, payload string) error {
		return handle(payload)
	})
}

func scanSSEEvents(body io.Reader, handle func(event string, payload string) error) error {
	var builder strings.Builder
	var event string
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			if builder.Len() > 0 {
				if err := handle(event, strings.TrimSpace(builder.String())); err != nil {
					return err
				}
				builder.Reset()
				event = ""
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if builder.Len() > 0 {
				builder.WriteByte('\n')
			}
			builder.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if builder.Len() > 0 {
		if err := handle(event, strings.TrimSpace(builder.String())); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func eventName(event map[string]any) string {
	if typed, ok := event["type"].(string); ok {
		return typed
	}
	return ""
}

func ensureResponseOutputTextSlot(response *openai.ResponseResponse) *openai.ResponseOutputContent {
	for outputIndex := range response.Output {
		for contentIndex := range response.Output[outputIndex].Content {
			if response.Output[outputIndex].Content[contentIndex].Type == "output_text" {
				return &response.Output[outputIndex].Content[contentIndex]
			}
		}
	}
	response.Output = append(response.Output, openai.ResponseOutputItem{
		Type:   "message",
		Status: "completed",
		Role:   "assistant",
		Content: []openai.ResponseOutputContent{
			{Type: "output_text"},
		},
	})
	outputIndex := len(response.Output) - 1
	return &response.Output[outputIndex].Content[0]
}
