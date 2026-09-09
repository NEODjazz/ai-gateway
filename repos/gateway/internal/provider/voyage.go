package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ai-gateway-gateway/internal/openai"
)

const maxVoyageInputs = 1000

type Voyage struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

type voyageEmbeddingRequest struct {
	Input           any    `json:"input"`
	Model           string `json:"model"`
	InputType       string `json:"input_type,omitempty"`
	Truncation      bool   `json:"truncation"`
	OutputDimension *int   `json:"output_dimension,omitempty"`
	OutputDType     string `json:"output_dtype,omitempty"`
	EncodingFormat  string `json:"encoding_format,omitempty"`
}

type voyageEmbeddingResponse struct {
	Object string             `json:"object"`
	Data   []openai.Embedding `json:"data"`
	Model  string             `json:"model"`
	Usage  struct {
		TotalTokens *int `json:"total_tokens"`
	} `json:"usage"`
}

type voyageRerankRequest struct {
	Query      string   `json:"query"`
	Documents  []string `json:"documents"`
	Model      string   `json:"model"`
	TopK       *int     `json:"top_k,omitempty"`
	Truncation bool     `json:"truncation"`
}

type voyageRerankResponse struct {
	Data  []openai.RerankResult `json:"data"`
	Usage struct {
		TotalTokens *int `json:"total_tokens"`
	} `json:"usage"`
}

func NewVoyage(baseURL, apiKey string) Voyage {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.voyageai.com"
	}
	return Voyage{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, client: newProviderHTTPClient(180 * time.Second)}
}

func (Voyage) ChatCompletions(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, voyageUnsupported("chat completions")
}

func (Voyage) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, voyageUnsupported("Responses")
}

func voyageUnsupported(operation string) error {
	return &Error{Class: FailureClientRequest, Provider: "voyage", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_operation", Err: errors.New(operation + " are not supported by this adapter")}
}

func (Voyage) SupportsResponses() bool { return false }

func (Voyage) ValidateEmbeddingParameters(request openai.EmbeddingRequest) error {
	input, err := openai.InspectEmbeddingInput(request.Input)
	if err != nil || input.Tokenized() || len(input.Texts) > maxVoyageInputs {
		return voyageParameterError("input", "Voyage embeddings require between 1 and 1000 text inputs")
	}
	if request.Metadata != nil {
		return rejectParameters("voyage", parameterCheck{"metadata", true})
	}
	if request.User != "" {
		return rejectParameters("voyage", parameterCheck{"user", true})
	}
	if request.InputType != "" && request.InputType != "query" && request.InputType != "document" {
		return voyageParameterError("input_type", "input_type must be query or document")
	}
	if request.EncodingFormat != "" && request.EncodingFormat != "float" && request.EncodingFormat != "base64" {
		return voyageParameterError("encoding_format", "encoding_format must be float or base64")
	}
	switch request.OutputDType {
	case "", "float", "int8", "uint8", "binary", "ubinary":
	default:
		return voyageParameterError("output_dtype", "output_dtype must be float, int8, uint8, binary, or ubinary")
	}
	if request.Dimensions != nil && (*request.Dimensions <= 0 || *request.Dimensions > 65536 || ((request.OutputDType == "binary" || request.OutputDType == "ubinary") && *request.Dimensions%8 != 0)) {
		return voyageParameterError("dimensions", "dimensions must be positive, bounded, and divisible by 8 for binary output")
	}
	return nil
}

func voyageParameterError(param, message string) error {
	return &Error{Class: FailureClientRequest, Provider: "voyage", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_parameter", Param: param, Err: errors.New(message)}
}

func (p Voyage) Embeddings(ctx context.Context, request openai.EmbeddingRequest) (openai.EmbeddingResponse, error) {
	if err := p.ValidateEmbeddingParameters(request); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	encoding := request.EncodingFormat
	if encoding == "float" {
		encoding = ""
	}
	body := voyageEmbeddingRequest{Input: request.Input, Model: request.Model, InputType: request.InputType, Truncation: false, OutputDimension: request.Dimensions, OutputDType: request.OutputDType, EncodingFormat: encoding}
	var upstream voyageEmbeddingResponse
	if err := p.post(ctx, "embeddings", body, maxEmbeddingResponseBytes, &upstream); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	if upstream.Usage.TotalTokens == nil || *upstream.Usage.TotalTokens < 0 {
		return openai.EmbeddingResponse{}, errors.New("invalid Voyage embedding usage")
	}
	result := openai.EmbeddingResponse{UsageReported: true, Object: upstream.Object, Data: upstream.Data, Model: upstream.Model, Usage: openai.Usage{PromptTokens: *upstream.Usage.TotalTokens, TotalTokens: *upstream.Usage.TotalTokens}}
	if result.Object == "" {
		result.Object = "list"
	}
	if result.Model == "" {
		result.Model = request.Model
	}
	if err := validateEmbeddingVectors(request, result.Data); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	return result, nil
}

func (p Voyage) Rerank(ctx context.Context, request openai.RerankRequest) (openai.RerankResponse, error) {
	if err := rejectParameters("voyage", parameterCheck{"rank_fields", len(request.RankFields) > 0}, parameterCheck{"max_chunks_per_doc", request.MaxChunksPerDoc != nil}, parameterCheck{"max_tokens_per_doc", request.MaxTokensPerDoc != nil}); err != nil {
		return openai.RerankResponse{}, err
	}
	if len(request.Documents) == 0 || len(request.Documents) > maxVoyageInputs {
		return openai.RerankResponse{}, voyageParameterError("documents", "Voyage rerank requires between 1 and 1000 string documents")
	}
	documents := make([]string, len(request.Documents))
	for index, document := range request.Documents {
		text, ok := document.(string)
		if !ok || text == "" {
			return openai.RerankResponse{}, voyageParameterError("documents", "Voyage rerank requires non-empty string documents")
		}
		documents[index] = text
	}
	body := voyageRerankRequest{Query: request.Query, Documents: documents, Model: request.Model, TopK: request.TopN, Truncation: false}
	var upstream voyageRerankResponse
	if err := p.post(ctx, "rerank", body, 8<<20, &upstream); err != nil {
		return openai.RerankResponse{}, err
	}
	if upstream.Usage.TotalTokens == nil || *upstream.Usage.TotalTokens < 0 {
		return openai.RerankResponse{}, errors.New("invalid Voyage rerank usage")
	}
	return openai.RerankResponse{Results: upstream.Data, Meta: &openai.RerankResponseMeta{BilledUnits: &openai.RerankBilledUnits{TotalTokens: *upstream.Usage.TotalTokens}}}, nil
}

func (p Voyage) post(ctx context.Context, suffix string, body any, limit int64, target any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	endpoint, err := voyageEndpoint(p.baseURL, suffix)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if p.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	response, err := p.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return responseStatusError("voyage", response)
	}
	payload, err = io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return err
	}
	if int64(len(payload)) > limit {
		return errors.New("Voyage response exceeds its limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("Voyage response contains trailing data or exceeds its limit")
	}
	return nil
}

func voyageEndpoint(baseURL, suffix string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base.Scheme == "" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return "", errors.New("invalid Voyage base URL")
	}
	path := strings.TrimRight(base.Path, "/")
	if !strings.HasSuffix(path, "/v1") {
		path += "/v1"
	}
	base.Path = path + "/" + suffix
	return base.String(), nil
}
