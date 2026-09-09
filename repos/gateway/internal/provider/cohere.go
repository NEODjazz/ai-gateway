package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ai-gateway-gateway/internal/openai"
)

const maxCohereRerankResponseBytes = 8 << 20
const maxCohereEmbeddingInputs = 96

type Cohere struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

type cohereRerankRequest struct {
	Model           string   `json:"model"`
	Query           string   `json:"query"`
	Documents       []string `json:"documents"`
	TopN            *int     `json:"top_n,omitempty"`
	MaxTokensPerDoc *int     `json:"max_tokens_per_doc,omitempty"`
}

type cohereEmbeddingRequest struct {
	Model           string   `json:"model"`
	Texts           []string `json:"texts"`
	InputType       string   `json:"input_type"`
	EmbeddingTypes  []string `json:"embedding_types"`
	OutputDimension *int     `json:"output_dimension,omitempty"`
}

func NewCohere(baseURL, apiKey string) Cohere {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.cohere.com"
	}
	client := newProviderHTTPClient(180 * time.Second)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return Cohere{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, client: client}
}

func (Cohere) SupportsResponses() bool { return false }

func (Cohere) ChatCompletions(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, rejectParameters("cohere", parameterCheck{"chat_completions", true})
}

func (Cohere) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, rejectParameters("cohere", parameterCheck{"responses", true})
}

func (Cohere) ValidateEmbeddingParameters(request openai.EmbeddingRequest) error {
	input, err := openai.InspectEmbeddingInput(request.Input)
	if err != nil || input.Tokenized() || len(input.Texts) > maxCohereEmbeddingInputs {
		return cohereEmbeddingError("input", "Cohere v2 embed requires at most 96 text inputs")
	}
	if request.InputType != "search_document" && request.InputType != "search_query" && request.InputType != "classification" && request.InputType != "clustering" {
		return cohereEmbeddingError("input_type", "Cohere v2 embed requires a supported input_type")
	}
	if request.User != "" {
		return rejectParameters("cohere", parameterCheck{"user", true})
	}
	if request.EncodingFormat != "" && request.EncodingFormat != "float" && request.EncodingFormat != "base64" {
		return cohereEmbeddingError("encoding_format", "encoding_format must be float or base64")
	}
	if request.Dimensions != nil && *request.Dimensions != 256 && *request.Dimensions != 512 && *request.Dimensions != 1024 && *request.Dimensions != 1536 {
		return cohereEmbeddingError("dimensions", "Cohere output dimensions must be 256, 512, 1024, or 1536")
	}
	if strings.TrimSpace(request.Model) == "" || len(request.Model) > 512 {
		return cohereEmbeddingError("model", "model is invalid")
	}
	return nil
}

func (p Cohere) Embeddings(ctx context.Context, request openai.EmbeddingRequest) (openai.EmbeddingResponse, error) {
	if err := p.ValidateEmbeddingParameters(request); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	texts, _ := openai.EmbeddingInputStrings(request.Input)
	body, err := json.Marshal(cohereEmbeddingRequest{
		Model: request.Model, Texts: texts, InputType: request.InputType,
		EmbeddingTypes: []string{"float"}, OutputDimension: request.Dimensions,
	})
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	if len(body) > openai.MaxInferenceBodyBytes {
		return openai.EmbeddingResponse{}, cohereEmbeddingError("input", "embedding request exceeds limit")
	}
	endpoint, err := cohereEndpoint(p.baseURL, "v2/embed")
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	if p.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return openai.EmbeddingResponse{}, responseStatusError("cohere", response)
	}
	var upstream struct {
		Embeddings struct {
			Float [][]float64 `json:"float"`
		} `json:"embeddings"`
		Meta *struct {
			BilledUnits *struct {
				InputTokens *int `json:"input_tokens"`
			} `json:"billed_units"`
		} `json:"meta"`
	}
	if err := decodeEmbeddingResponse(response.Body, &upstream); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	result := openai.EmbeddingResponse{Object: "list", Model: request.Model, Data: make([]openai.Embedding, len(upstream.Embeddings.Float))}
	for index, vector := range upstream.Embeddings.Float {
		result.Data[index] = openai.Embedding{Object: "embedding", Embedding: vector, Index: index}
	}
	floatRequest := request
	floatRequest.EncodingFormat = "float"
	if err := validateEmbeddingVectors(floatRequest, result.Data); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	if request.EncodingFormat == "base64" {
		for index := range result.Data {
			encoded, err := encodeCohereEmbedding(result.Data[index].Embedding)
			if err != nil {
				return openai.EmbeddingResponse{}, err
			}
			result.Data[index].Embedding = nil
			result.Data[index].EmbeddingBase64 = encoded
		}
		if err := validateEmbeddingVectors(request, result.Data); err != nil {
			return openai.EmbeddingResponse{}, err
		}
	}
	tokens := openai.EmbeddingInputTokenCount(request.Input)
	if upstream.Meta != nil && upstream.Meta.BilledUnits != nil && upstream.Meta.BilledUnits.InputTokens != nil {
		tokens = *upstream.Meta.BilledUnits.InputTokens
		if tokens < 0 {
			return openai.EmbeddingResponse{}, errors.New("invalid Cohere embedding usage")
		}
		result.UsageReported = true
	}
	result.Usage = openai.Usage{PromptTokens: tokens, TotalTokens: tokens}
	return result, nil
}

func cohereEmbeddingError(param, message string) error {
	return &Error{Class: FailureClientRequest, Provider: "cohere", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_parameter", Param: param, Err: errors.New(message)}
}

func encodeCohereEmbedding(values []float64) (string, error) {
	encoded := make([]byte, len(values)*4)
	for index, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) || math.IsInf(float64(float32(value)), 0) {
			return "", errors.New("invalid Cohere embedding value")
		}
		binary.LittleEndian.PutUint32(encoded[index*4:], math.Float32bits(float32(value)))
	}
	return base64.StdEncoding.EncodeToString(encoded), nil
}

func (p Cohere) Rerank(ctx context.Context, request openai.RerankRequest) (openai.RerankResponse, error) {
	if err := rejectParameters("cohere",
		parameterCheck{"rank_fields", len(request.RankFields) > 0},
		parameterCheck{"max_chunks_per_doc", request.MaxChunksPerDoc != nil},
	); err != nil {
		return openai.RerankResponse{}, err
	}
	documents := make([]string, len(request.Documents))
	for index, document := range request.Documents {
		text, ok := document.(string)
		if !ok {
			return openai.RerankResponse{}, &Error{Class: FailureClientRequest, Provider: "cohere", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: "documents", Err: errors.New("Cohere v2 rerank requires string documents")}
		}
		documents[index] = text
	}
	body, err := json.Marshal(cohereRerankRequest{Model: request.Model, Query: request.Query, Documents: documents, TopN: request.TopN, MaxTokensPerDoc: request.MaxTokensPerDoc})
	if err != nil {
		return openai.RerankResponse{}, err
	}
	endpoint, err := cohereEndpoint(p.baseURL, "v2/rerank")
	if err != nil {
		return openai.RerankResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return openai.RerankResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	if p.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return openai.RerankResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return openai.RerankResponse{}, responseStatusError("cohere", response)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxCohereRerankResponseBytes+1))
	if err != nil {
		return openai.RerankResponse{}, err
	}
	if len(payload) > maxCohereRerankResponseBytes {
		return openai.RerankResponse{}, errors.New("Cohere rerank response exceeds limit")
	}
	var result openai.RerankResponse
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&result); err != nil {
		return openai.RerankResponse{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return openai.RerankResponse{}, errors.New("Cohere rerank response contains trailing data")
	}
	return result, nil
}

func cohereEndpoint(baseURL, suffix string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return "", errors.New("invalid Cohere base URL")
	}
	path := strings.TrimRight(base.Path, "/")
	if strings.HasSuffix(path, "/v1") || strings.HasSuffix(path, "/v2") {
		path = path[:len(path)-3]
	}
	base.Path = strings.TrimRight(path, "/") + "/" + suffix
	return base.String(), nil
}
