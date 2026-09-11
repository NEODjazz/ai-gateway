package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

type bedrockInvokeRequest struct {
	AnthropicVersion string                     `json:"anthropic_version"`
	MaxTokens        int                        `json:"max_tokens"`
	Messages         []messagesInput            `json:"messages"`
	System           json.RawMessage            `json:"system,omitempty"`
	Tools            []messagesTool             `json:"tools,omitempty"`
	ToolChoice       *messagesToolChoice        `json:"tool_choice,omitempty"`
	OutputConfig     *bedrockInvokeOutputConfig `json:"output_config,omitempty"`
	Temperature      *float64                   `json:"temperature,omitempty"`
	TopP             *float64                   `json:"top_p,omitempty"`
	StopSequences    []string                   `json:"stop_sequences,omitempty"`
}

type bedrockInvokeOutputConfig struct {
	Format *messagesJSONOutputFormat `json:"format"`
}

func (request bedrockInvokeRequest) chat(model, provider string) (openai.ChatCompletionRequest, error) {
	if request.AnthropicVersion != "bedrock-2023-05-31" {
		return openai.ChatCompletionRequest{}, errors.New("anthropic_version must be bedrock-2023-05-31")
	}
	var outputConfig *messagesOutputConfig
	if request.OutputConfig != nil {
		outputConfig = &messagesOutputConfig{Format: request.OutputConfig.Format}
	}
	messages := messagesRequest{
		Model: model, MaxTokens: request.MaxTokens, Messages: request.Messages, System: request.System,
		Tools: request.Tools, ToolChoice: request.ToolChoice, Temperature: request.Temperature,
		TopP: request.TopP, StopSequences: request.StopSequences, OutputConfig: outputConfig,
	}
	chat, err := messages.chat()
	if err != nil {
		return chat, err
	}
	if chat.WebSearchOptions != nil || chat.WebFetchOptions != nil {
		return chat, errors.New("InvokeModel supports function tools only")
	}
	chat.Provider = provider
	chat.BedrockInvoke = true
	return chat, nil
}

func (h Handler) BedrockInvoke(w http.ResponseWriter, r *http.Request) {
	output := &messagesWriter{destination: w, headers: make(http.Header), status: http.StatusOK, tools: map[int]int{}}
	defer output.finish()
	model := strings.TrimSpace(r.PathValue("model"))
	if model == "" || len(model) > 2048 {
		writeError(output, http.StatusBadRequest, "invalid_request", "model is required")
		return
	}
	var request bedrockInvokeRequest
	if !decodeInferenceRequest(output, r, &request) {
		return
	}
	chat, err := request.chat(model, strings.TrimSpace(r.URL.Query().Get("provider")))
	if err != nil {
		writeError(output, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	output.model = model
	h.serveChatAs(output, r, chat, "bedrock_invoke")
}

func (h Handler) BedrockInvokeStream(w http.ResponseWriter, r *http.Request) {
	output := newBedrockInvokeStreamWriter(w)
	defer output.finish()
	model := strings.TrimSpace(r.PathValue("model"))
	if model == "" || len(model) > 2048 {
		writeError(output, http.StatusBadRequest, "invalid_request", "model is required")
		return
	}
	var request bedrockInvokeRequest
	if !decodeInferenceRequest(output, r, &request) {
		return
	}
	chat, err := request.chat(model, strings.TrimSpace(r.URL.Query().Get("provider")))
	if err != nil {
		writeError(output, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	chat.Stream = true
	chat.StreamOptions = &openai.ChatStreamOptions{IncludeUsage: true}
	output.messages.model = model
	h.serveChatAs(output, r, chat, "bedrock_invoke_stream")
}

func (h Handler) BedrockConverse(w http.ResponseWriter, r *http.Request) {
	model := strings.TrimSpace(r.PathValue("model"))
	if model == "" || len(model) > 2048 {
		writeError(w, http.StatusBadRequest, "invalid_request", "model is required")
		return
	}
	var request openai.BedrockConverseRequest
	if !decodeInferenceRequest(w, r, &request) {
		return
	}
	chat, err := request.ChatRequest(model, strings.TrimSpace(r.URL.Query().Get("provider")))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	h.serveChatAdapted(w, r, chat, "bedrock_converse", func(response openai.ChatCompletionResponse) (any, error) {
		return openai.BedrockFromChat(response)
	})
}

func (h Handler) BedrockConverseStream(w http.ResponseWriter, r *http.Request) {
	output := newBedrockStreamWriter(w)
	defer output.finish()
	model := strings.TrimSpace(r.PathValue("model"))
	if model == "" || len(model) > 2048 {
		writeError(output, http.StatusBadRequest, "invalid_request", "model is required")
		return
	}
	var request openai.BedrockConverseRequest
	if !decodeInferenceRequest(output, r, &request) {
		return
	}
	chat, err := request.ChatRequest(model, strings.TrimSpace(r.URL.Query().Get("provider")))
	if err != nil {
		writeError(output, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	chat.Stream = true
	chat.StreamOptions = &openai.ChatStreamOptions{IncludeUsage: true}
	h.serveChatAs(output, r, chat, "bedrock_converse_stream")
}
