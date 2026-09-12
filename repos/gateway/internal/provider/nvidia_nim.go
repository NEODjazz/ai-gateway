package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"

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

func (n NVIDIANIM) RetrieveResponse(ctx context.Context, id string) (openai.ResponseResponse, error) {
	return n.compatible.RetrieveResponse(ctx, id)
}

func (n NVIDIANIM) CancelResponse(ctx context.Context, id string) (openai.ResponseResponse, error) {
	return n.compatible.CancelResponse(ctx, id)
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

const nvidiaNIMMaxRerankPassages = 512

type nvidiaNIMRankingRequest struct {
	Model    string                 `json:"model"`
	Query    nvidiaNIMRankingText   `json:"query"`
	Passages []nvidiaNIMRankingText `json:"passages"`
	Truncate string                 `json:"truncate,omitempty"`
}

type nvidiaNIMRankingText struct {
	Text string `json:"text"`
}

func (NVIDIANIM) SupportsRerank() bool { return true }

func (NVIDIANIM) ValidateRerankParameters(request openai.RerankRequest) error {
	if err := rejectParameters("nvidia-nim",
		parameterCheck{"rank_fields", len(request.RankFields) > 0},
		parameterCheck{"max_chunks_per_doc", request.MaxChunksPerDoc != nil},
		parameterCheck{"max_tokens_per_doc", request.MaxTokensPerDoc != nil},
	); err != nil {
		return err
	}
	if len(request.Documents) == 0 || len(request.Documents) > nvidiaNIMMaxRerankPassages {
		return nvidiaNIMRerankParameterError("documents", "NVIDIA NIM rerank requires between 1 and 512 text passages")
	}
	if request.TopN != nil && (*request.TopN <= 0 || *request.TopN > len(request.Documents)) {
		return nvidiaNIMRerankParameterError("top_n", "top_n must be between 1 and the number of passages")
	}
	if request.Truncate != "" && request.Truncate != "NONE" && request.Truncate != "END" {
		return nvidiaNIMRerankParameterError("truncate", "truncate must be NONE or END")
	}
	for _, document := range request.Documents {
		text, ok := document.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nvidiaNIMRerankParameterError("documents", "NVIDIA NIM rerank requires non-empty string documents")
		}
	}
	return nil
}

func nvidiaNIMRerankParameterError(param, message string) error {
	return &Error{Class: FailureClientRequest, Provider: "nvidia-nim", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: param, Err: errors.New(message)}
}

func (n NVIDIANIM) Rerank(ctx context.Context, request openai.RerankRequest) (openai.RerankResponse, error) {
	if err := n.ValidateRerankParameters(request); err != nil {
		return openai.RerankResponse{}, err
	}
	passages := make([]nvidiaNIMRankingText, len(request.Documents))
	for index, document := range request.Documents {
		passages[index].Text = document.(string)
	}
	payload, err := json.Marshal(nvidiaNIMRankingRequest{
		Model: request.Model, Query: nvidiaNIMRankingText{Text: request.Query}, Passages: passages, Truncate: request.Truncate,
	})
	if err != nil {
		return openai.RerankResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(n.compatible.baseURL, "ranking"), bytes.NewReader(payload))
	if err != nil {
		return openai.RerankResponse{}, err
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Content-Type", "application/json")
	if n.compatible.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+n.compatible.apiKey)
	}
	response, err := n.compatible.client.Do(httpRequest)
	if err != nil {
		return openai.RerankResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return openai.RerankResponse{}, responseStatusError("nvidia-nim", response)
	}
	return decodeNVIDIANIMRerankResponse(response.Body, len(passages), request.TopN)
}

func decodeNVIDIANIMRerankResponse(reader io.Reader, passageCount int, topN *int) (openai.RerankResponse, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, (8<<20)+1))
	if err != nil {
		return openai.RerankResponse{}, err
	}
	if len(payload) > 8<<20 {
		return openai.RerankResponse{}, errors.New("NVIDIA NIM rerank response exceeds 8 MiB")
	}
	var wire struct {
		Rankings []struct {
			Index int     `json:"index"`
			Logit float64 `json:"logit"`
		} `json:"rankings"`
		Usage *struct {
			PromptTokens int `json:"prompt_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&wire); err != nil {
		return openai.RerankResponse{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return openai.RerankResponse{}, errors.New("invalid trailing NVIDIA NIM rerank response data")
	}
	if wire.Usage == nil || wire.Usage.PromptTokens <= 0 || wire.Usage.TotalTokens != wire.Usage.PromptTokens {
		return openai.RerankResponse{}, errors.New("invalid NVIDIA NIM rerank usage")
	}
	if len(wire.Rankings) != passageCount {
		return openai.RerankResponse{}, errors.New("NVIDIA NIM rerank response must rank every passage")
	}
	results := make([]openai.RerankResult, len(wire.Rankings))
	seen := make(map[int]bool, len(wire.Rankings))
	for index, ranking := range wire.Rankings {
		if ranking.Index < 0 || ranking.Index >= passageCount || seen[ranking.Index] || math.IsNaN(ranking.Logit) || math.IsInf(ranking.Logit, 0) {
			return openai.RerankResponse{}, errors.New("invalid NVIDIA NIM rerank ranking")
		}
		if index > 0 && ranking.Logit > wire.Rankings[index-1].Logit {
			return openai.RerankResponse{}, errors.New("NVIDIA NIM rerank rankings are not ordered by descending logit")
		}
		seen[ranking.Index] = true
		results[index] = openai.RerankResult{Index: ranking.Index, RelevanceScore: ranking.Logit}
	}
	if topN != nil {
		results = results[:*topN]
	}
	return openai.RerankResponse{Results: results, Meta: &openai.RerankResponseMeta{Tokens: &openai.RerankTokens{InputTokens: wire.Usage.PromptTokens}}}, nil
}
