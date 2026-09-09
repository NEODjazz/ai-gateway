package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

type Mistral struct {
	OpenAICompatible
}

type mistralFIMRequest struct {
	Model       string   `json:"model"`
	Prompt      string   `json:"prompt"`
	Suffix      string   `json:"suffix,omitempty"`
	MaxTokens   *int     `json:"max_tokens,omitempty"`
	RandomSeed  *int64   `json:"random_seed,omitempty"`
	Stop        any      `json:"stop,omitempty"`
	Stream      bool     `json:"stream"`
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
}

type mistralFIMResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int            `json:"index"`
		Message      openai.Message `json:"message"`
		Delta        openai.Message `json:"delta"`
		FinishReason *string        `json:"finish_reason"`
	} `json:"choices"`
	Usage *openai.Usage `json:"usage,omitempty"`
}

func NewMistral(baseURL, apiKey string, upstreamStream bool) Mistral {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.mistral.ai"
	}
	compatible := NewOpenAICompatible(baseURL, apiKey, upstreamStream)
	compatible.errorProvider = "mistral"
	return Mistral{OpenAICompatible: compatible}
}

func (Mistral) SupportsResponses() bool { return false }
func (Mistral) SupportsRerank() bool    { return false }

func (Mistral) ValidateEmbeddingParameters(request openai.EmbeddingRequest) error {
	input, err := openai.InspectEmbeddingInput(request.Input)
	return rejectParameters("mistral",
		parameterCheck{"input", err != nil || input.Tokenized()},
		parameterCheck{"input_type", request.InputType != ""},
		parameterCheck{"user", request.User != ""},
	)
}

func (p Mistral) Embeddings(ctx context.Context, request openai.EmbeddingRequest) (openai.EmbeddingResponse, error) {
	if err := p.ValidateEmbeddingParameters(request); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	return p.OpenAICompatible.Embeddings(ctx, request)
}

func (p Mistral) Moderations(ctx context.Context, request openai.ModerationRequest) (openai.ModerationResponse, error) {
	if !mistralModerationTextInput(request.Input) {
		return openai.ModerationResponse{}, &Error{Class: FailureClientRequest, Provider: "mistral", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: "input", Err: errors.New("Mistral moderation requires text input")}
	}
	return p.OpenAICompatible.Moderations(ctx, request)
}

func mistralModerationTextInput(input any) bool {
	switch value := input.(type) {
	case string:
		return strings.TrimSpace(value) != ""
	case []string:
		if len(value) == 0 || len(value) > openai.MaxModerationInputs {
			return false
		}
		for _, item := range value {
			if strings.TrimSpace(item) == "" {
				return false
			}
		}
		return true
	case []any:
		if len(value) == 0 || len(value) > openai.MaxModerationInputs {
			return false
		}
		for _, item := range value {
			text, ok := item.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func (Mistral) ValidateCompletionParameters(request openai.CompletionRequest) error {
	prompt, err := openai.InspectCompletionPrompt(request.Prompt)
	if err != nil || prompt.Kind != openai.CompletionPromptText {
		return &Error{Class: FailureClientRequest, Provider: "mistral", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: "prompt", Err: errors.New("Mistral FIM requires one string prompt")}
	}
	return rejectParameters("mistral",
		parameterCheck{"best_of", request.BestOf != nil},
		parameterCheck{"echo", request.Echo != nil},
		parameterCheck{"frequency_penalty", request.FrequencyPenalty != nil},
		parameterCheck{"logit_bias", request.LogitBias != nil},
		parameterCheck{"logprobs", request.Logprobs != nil},
		parameterCheck{"n", request.N != nil},
		parameterCheck{"presence_penalty", request.PresencePenalty != nil},
		parameterCheck{"user", request.User != ""},
	)
}

func (p Mistral) Completions(ctx context.Context, request openai.CompletionRequest) (openai.CompletionResponse, error) {
	return p.fimCompletion(ctx, request, false, nil)
}

func (p Mistral) StreamCompletions(ctx context.Context, request openai.CompletionRequest, write CompletionStreamWriter) (openai.CompletionResponse, error) {
	if !p.upstreamStream {
		return openai.CompletionResponse{}, ErrStreamingUnsupported
	}
	return p.fimCompletion(ctx, request, true, write)
}

func (p Mistral) fimCompletion(ctx context.Context, request openai.CompletionRequest, stream bool, write CompletionStreamWriter) (openai.CompletionResponse, error) {
	if err := p.ValidateCompletionParameters(request); err != nil {
		return openai.CompletionResponse{}, err
	}
	prompt, _ := request.Prompt.(string)
	body, err := json.Marshal(mistralFIMRequest{
		Model: request.Model, Prompt: prompt, Suffix: request.Suffix, MaxTokens: request.MaxTokens,
		RandomSeed: request.Seed, Stop: request.Stop, Stream: stream, Temperature: request.Temperature, TopP: request.TopP,
	})
	if err != nil {
		return openai.CompletionResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(p.baseURL, "fim/completions"), bytes.NewReader(body))
	if err != nil {
		return openai.CompletionResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")
	if !stream {
		httpRequest.Header.Set("Accept", "application/json")
	}
	if p.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return openai.CompletionResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return openai.CompletionResponse{}, responseStatusError("mistral", response)
	}
	if stream {
		return streamMistralFIM(response.Body, request, write)
	}
	return decodeMistralFIM(response.Body, request)
}

func decodeMistralFIM(reader io.Reader, request openai.CompletionRequest) (openai.CompletionResponse, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, maxResponseJSONBytes+1))
	if err != nil {
		return openai.CompletionResponse{}, err
	}
	if len(payload) > maxResponseJSONBytes {
		return openai.CompletionResponse{}, errors.New("Mistral FIM response exceeds 32 MiB")
	}
	var upstream mistralFIMResponse
	if err := json.Unmarshal(payload, &upstream); err != nil {
		return openai.CompletionResponse{}, err
	}
	return mistralFIMCompletion(upstream, request)
}

func mistralFIMCompletion(upstream mistralFIMResponse, request openai.CompletionRequest) (openai.CompletionResponse, error) {
	response := openai.CompletionResponse{ID: upstream.ID, Object: "text_completion", Created: upstream.Created, Model: upstream.Model}
	if upstream.Object != "chat.completion" || upstream.Usage == nil {
		return openai.CompletionResponse{}, errors.New("invalid Mistral FIM response envelope")
	}
	response.Usage = *upstream.Usage
	for _, choice := range upstream.Choices {
		text, ok := choice.Message.Content.(string)
		if !ok || choice.FinishReason == nil || choice.Message.Role != "assistant" || choice.Message.Name != "" || choice.Message.ToolCallID != "" || len(choice.Message.ToolCalls) > 0 || choice.Message.FunctionCall != nil || choice.Message.Audio != nil || choice.Message.Refusal != nil || len(choice.Message.Annotations) > 0 {
			return openai.CompletionResponse{}, errors.New("invalid Mistral FIM completion choice")
		}
		response.Choices = append(response.Choices, openai.CompletionChoice{Index: choice.Index, Text: text, FinishReason: *choice.FinishReason})
	}
	if err := validateCompletionResult(response, request); err != nil {
		return openai.CompletionResponse{}, err
	}
	return response, nil
}

func streamMistralFIM(reader io.Reader, request openai.CompletionRequest, write CompletionStreamWriter) (openai.CompletionResponse, error) {
	response := openai.CompletionResponse{Object: "text_completion"}
	seenDone := false
	err := scanSSEData(&responseStreamReader{source: reader, remaining: maxResponseStreamBytes}, func(payload string) error {
		if payload == "[DONE]" {
			seenDone = true
			return io.EOF
		}
		var upstream mistralFIMResponse
		if err := json.Unmarshal([]byte(payload), &upstream); err != nil {
			return err
		}
		if upstream.Object != "chat.completion.chunk" {
			return errors.New("invalid Mistral FIM stream object")
		}
		if err := mergeMistralFIMIdentity(&response, upstream); err != nil {
			return err
		}
		if upstream.Usage != nil {
			if err := validateCompletionUsage(*upstream.Usage); err != nil {
				return err
			}
			response.Usage = *upstream.Usage
		}
		for _, choice := range upstream.Choices {
			text := ""
			if choice.Delta.Content != nil {
				var ok bool
				text, ok = choice.Delta.Content.(string)
				if !ok {
					return errors.New("invalid Mistral FIM stream content")
				}
			}
			if choice.Index != 0 || choice.Delta.Role != "" && choice.Delta.Role != "assistant" || choice.Delta.Name != "" || choice.Delta.ToolCallID != "" || len(choice.Delta.ToolCalls) > 0 || choice.Delta.FunctionCall != nil || choice.Delta.Audio != nil || choice.Delta.Refusal != nil || len(choice.Delta.Annotations) > 0 {
				return errors.New("invalid Mistral FIM stream choice")
			}
			if response.ID == "" || response.Model == "" || response.Created <= 0 {
				return errors.New("incomplete Mistral FIM stream identity")
			}
			if len(response.Choices) == 0 {
				response.Choices = append(response.Choices, openai.CompletionChoice{Index: 0})
			}
			response.Choices[0].Text += text
			finish := any(nil)
			if choice.FinishReason != nil {
				response.Choices[0].FinishReason = *choice.FinishReason
				finish = *choice.FinishReason
			}
			if write != nil {
				chunk, err := json.Marshal(map[string]any{
					"id": response.ID, "object": "text_completion", "created": response.Created, "model": response.Model,
					"choices": []map[string]any{{"index": 0, "text": text, "logprobs": nil, "finish_reason": finish}},
				})
				if err != nil {
					return err
				}
				if err := write(string(chunk)); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return openai.CompletionResponse{}, err
	}
	if !seenDone {
		return openai.CompletionResponse{}, errors.New("Mistral FIM stream ended before [DONE]")
	}
	if err := validateCompletionResult(response, request); err != nil {
		return openai.CompletionResponse{}, err
	}
	return response, nil
}

func mergeMistralFIMIdentity(response *openai.CompletionResponse, upstream mistralFIMResponse) error {
	if upstream.ID != "" {
		if response.ID != "" && response.ID != upstream.ID {
			return errors.New("Mistral FIM changed stream ID")
		}
		response.ID = upstream.ID
	}
	if upstream.Model != "" {
		if response.Model != "" && response.Model != upstream.Model {
			return errors.New("Mistral FIM changed stream model")
		}
		response.Model = upstream.Model
	}
	if upstream.Created != 0 {
		if upstream.Created < 0 || response.Created != 0 && response.Created != upstream.Created {
			return errors.New("Mistral FIM changed stream timestamp")
		}
		response.Created = upstream.Created
	}
	return nil
}
