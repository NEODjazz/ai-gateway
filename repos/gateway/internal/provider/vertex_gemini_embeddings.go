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
	"time"

	"ai-gateway-gateway/internal/openai"
)

const maxVertexEmbeddingInputs = 5

func (VertexGemini) ValidateEmbeddingParameters(request openai.EmbeddingRequest) error {
	if err := rejectParameters("vertex-gemini",
		parameterCheck{"metadata", request.Metadata != nil},
		parameterCheck{"output_dtype", request.OutputDType != ""},
		parameterCheck{"user", request.User != ""},
		parameterCheck{"encoding_format", request.EncodingFormat != "" && request.EncodingFormat != "float"},
	); err != nil {
		return err
	}
	input, err := openai.InspectEmbeddingInput(request.Input)
	if err != nil || input.Tokenized() {
		return vertexGeminiEmbeddingError("input", "input must contain between one and five non-empty strings")
	}
	if limit := vertexEmbeddingInputLimit(request.Model); input.Count > limit {
		if limit == 1 {
			return vertexGeminiEmbeddingError("input", "gemini-embedding-001 accepts exactly one input text per request")
		}
		return vertexGeminiEmbeddingError("input", "input accepts at most five texts per request")
	}
	switch request.InputType {
	case "", "search_query", "search_document", "classification", "clustering":
	default:
		return vertexGeminiEmbeddingError("input_type", "input_type must be search_query, search_document, classification, or clustering")
	}
	if request.Dimensions != nil && (*request.Dimensions <= 0 || *request.Dimensions > 3072) {
		return vertexGeminiEmbeddingError("dimensions", "dimensions must be between 1 and 3072")
	}
	model := strings.TrimPrefix(request.Model, "models/")
	if strings.TrimSpace(model) == "" || strings.ContainsAny(model, "/\\?#%") || model == "." || model == ".." {
		return vertexGeminiEmbeddingError("model", "model must be a safe publisher model ID")
	}
	return nil
}

func vertexEmbeddingInputLimit(model string) int {
	if strings.TrimPrefix(model, "models/") == "gemini-embedding-001" {
		return 1
	}
	return maxVertexEmbeddingInputs
}

func (v VertexGemini) Embeddings(ctx context.Context, request openai.EmbeddingRequest) (openai.EmbeddingResponse, error) {
	if err := v.ValidateEmbeddingParameters(request); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	inputs, _ := openai.EmbeddingInputStrings(request.Input)
	taskType := vertexEmbeddingTaskType(request.InputType)
	type instance struct {
		Content  string `json:"content"`
		TaskType string `json:"task_type,omitempty"`
	}
	native := struct {
		Instances  []instance `json:"instances"`
		Parameters struct {
			AutoTruncate         bool `json:"autoTruncate"`
			OutputDimensionality *int `json:"outputDimensionality,omitempty"`
		} `json:"parameters"`
	}{Instances: make([]instance, len(inputs))}
	for index, input := range inputs {
		native.Instances[index] = instance{Content: input, TaskType: taskType}
	}
	native.Parameters.AutoTruncate = false
	native.Parameters.OutputDimensionality = request.Dimensions
	body, err := json.Marshal(native)
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	if len(body) > openai.MaxInferenceBodyBytes {
		return openai.EmbeddingResponse{}, vertexGeminiEmbeddingError("input", "embedding request exceeds the gateway body limit")
	}
	endpoint, err := v.gemini.modelEndpoint(request.Model, "predict")
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	requestContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	upstreamRequest, err := http.NewRequestWithContext(requestContext, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	upstreamRequest.Header.Set("Content-Type", "application/json")
	if err := v.gemini.authorize(upstreamRequest); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	response, err := v.gemini.client.Do(upstreamRequest)
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return openai.EmbeddingResponse{}, responseStatusError("vertex-gemini", response)
	}
	const maxResponseBytes = 32 << 20
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	if len(payload) > maxResponseBytes {
		return openai.EmbeddingResponse{}, errors.New("Vertex Gemini embedding response exceeds limit")
	}
	return vertexEmbeddingResponse(request, inputs, payload)
}

func vertexEmbeddingResponse(request openai.EmbeddingRequest, inputs []string, payload []byte) (openai.EmbeddingResponse, error) {
	var upstream struct {
		Predictions []struct {
			Embeddings struct {
				Values     []float64 `json:"values"`
				Statistics struct {
					TokenCount      *int `json:"token_count"`
					TokenCountCamel *int `json:"tokenCount"`
				} `json:"statistics"`
			} `json:"embeddings"`
		} `json:"predictions"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(payload, &upstream) != nil || len(upstream.Error) != 0 || len(upstream.Predictions) != len(inputs) {
		return openai.EmbeddingResponse{}, errors.New("invalid Vertex Gemini embedding response")
	}
	result := openai.EmbeddingResponse{Object: "list", Model: request.Model, Data: make([]openai.Embedding, len(inputs))}
	dimensions := 0
	reportedTokens := 0
	reportedUsage := false
	for index, prediction := range upstream.Predictions {
		values := prediction.Embeddings.Values
		if index == 0 {
			dimensions = len(values)
		}
		if dimensions == 0 || dimensions > 3072 || len(values) != dimensions || (request.Dimensions != nil && dimensions != *request.Dimensions) {
			return openai.EmbeddingResponse{}, errors.New("invalid Vertex Gemini embedding dimensions")
		}
		for _, value := range values {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return openai.EmbeddingResponse{}, errors.New("invalid Vertex Gemini embedding value")
			}
		}
		result.Data[index] = openai.Embedding{Object: "embedding", Index: index, Embedding: values}
		tokenCount := prediction.Embeddings.Statistics.TokenCount
		if tokenCount == nil {
			tokenCount = prediction.Embeddings.Statistics.TokenCountCamel
		}
		if tokenCount != nil {
			if *tokenCount < 0 || reportedTokens > int(^uint(0)>>1)-*tokenCount {
				return openai.EmbeddingResponse{}, errors.New("invalid Vertex Gemini embedding usage")
			}
			reportedTokens += *tokenCount
			reportedUsage = true
		} else if reportedUsage {
			return openai.EmbeddingResponse{}, errors.New("incomplete Vertex Gemini embedding usage")
		}
	}
	if reportedUsage {
		for _, prediction := range upstream.Predictions {
			if prediction.Embeddings.Statistics.TokenCount == nil && prediction.Embeddings.Statistics.TokenCountCamel == nil {
				return openai.EmbeddingResponse{}, errors.New("incomplete Vertex Gemini embedding usage")
			}
		}
		result.UsageReported = true
		result.Usage = openai.Usage{PromptTokens: reportedTokens, TotalTokens: reportedTokens}
	} else {
		tokens := openai.EmbeddingInputTokenCount(request.Input)
		result.Usage = openai.Usage{PromptTokens: tokens, TotalTokens: tokens}
	}
	return result, nil
}

func vertexEmbeddingTaskType(inputType string) string {
	switch inputType {
	case "search_document":
		return "RETRIEVAL_DOCUMENT"
	case "search_query":
		return "RETRIEVAL_QUERY"
	case "classification":
		return "CLASSIFICATION"
	case "clustering":
		return "CLUSTERING"
	default:
		return ""
	}
}

func vertexGeminiEmbeddingError(param, message string) error {
	return &Error{Class: FailureClientRequest, Provider: "vertex-gemini", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: param, Err: errors.New(message)}
}
